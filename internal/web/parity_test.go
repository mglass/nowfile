package web

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

var errorRE = regexp.MustCompile(`<p class="error" role="alert">(.*?)</p>`)

// TestSlotWriteParity drives the same Status writes through the write API
// with a bearer token and through the account page with a session and CSRF,
// for two users in one store, and requires identical stored slots (all but
// user and source) and identical 422 messages. Both transports end in
// store.WriteSlot; this test is what keeps them from drifting.
func TestSlotWriteParity(t *testing.T) {
	e := newEnv(t)
	e.signIn("alice")
	webUser := e.user("alice")
	apiUser, err := e.st.CreateUserHeld(ctx, "bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	bearer, _, err := e.st.CreateToken(ctx, apiUser.ID, "Bob's phone")
	if err != nil {
		t.Fatal(err)
	}
	csrf := e.csrf("/account")

	api := func(method, key string, body any) (int, string) {
		t.Helper()
		var b strings.Builder
		if body != nil {
			json.NewEncoder(&b).Encode(body)
		}
		req, _ := http.NewRequest(method, e.srv.URL+"/api/v1/slots/"+key, strings.NewReader(b.String()))
		req.Header.Set("Authorization", "Bearer "+bearer)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var msg struct{ Error string }
		json.NewDecoder(r.Body).Decode(&msg)
		return r.StatusCode, msg.Error
	}
	web := func(form url.Values) (int, string) {
		t.Helper()
		form.Set("csrf", csrf)
		r := e.post("/account", form)
		msg := ""
		if m := errorRE.FindStringSubmatch(r.body); m != nil {
			msg = html.UnescapeString(m[1])
		}
		return r.code, msg
	}
	slots := func(u store.User) []format.Slot {
		t.Helper()
		sl, err := e.st.Slots(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return sl
	}
	clear := func() {
		t.Helper()
		for _, u := range []store.User{webUser, apiUser} {
			if err := e.st.ClearSlot(ctx, u.ID, "Status"); err != nil {
				t.Fatal(err)
			}
		}
	}

	cases := []struct {
		name, value, until string // until is RFC 3339 or empty
		want               string // "" for success, else the 422 message
	}{
		{"valid with until", "Lunch, back at 1", "2026-10-13T13:30:00-04:00", ""},
		{"valid without until", "Heads down", "", ""},
		{"padded value", "  Heads down  ", "", ""},
		{"empty value", "", "", "value is required; clear the slot to remove it"},
		{"value over 200 bytes", strings.Repeat("x", 201), "", "value is 201 bytes, limit 200"},
		{"value with a newline", "one\ntwo", "", "value must be one line"},
		{"until in the past", "Lunch", "2026-10-13T15:00:00Z", "until is not in the future"},
		{"until malformed", "Lunch", "13:00", "until must be RFC 3339, for example 2026-10-13T13:00:00-04:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clear()
			body := map[string]string{"value": c.value}
			form := url.Values{"action": {"status"}, "value": {c.value}, "expiry": {"none"}}
			if c.until != "" {
				body["until"] = c.until
				form.Set("expiry", "custom")
				form.Set("custom_utc", c.until)
			}
			apiCode, apiMsg := api("PUT", "Status", body)
			webCode, webMsg := web(form)

			if c.want == "" {
				if apiCode != http.StatusNoContent || webCode != http.StatusSeeOther {
					t.Fatalf("api %d %q, web %d %q; want success from both", apiCode, apiMsg, webCode, webMsg)
				}
			} else {
				if apiCode != http.StatusUnprocessableEntity || webCode != http.StatusUnprocessableEntity {
					t.Fatalf("api %d, web %d; want 422 from both", apiCode, webCode)
				}
				if apiMsg != c.want || webMsg != c.want {
					t.Fatalf("api %q, web %q; want %q from both", apiMsg, webMsg, c.want)
				}
			}

			a, w := slots(apiUser), slots(webUser)
			if len(a) != len(w) {
				t.Fatalf("api rows %+v, web rows %+v", a, w)
			}
			for i := range a {
				if a[i].Source != "bob-s-phone" || w[i].Source != webSource {
					t.Errorf("sources api %q web %q", a[i].Source, w[i].Source)
				}
				a[i].Source, w[i].Source = "", ""
				if a[i] != w[i] {
					t.Errorf("api row %+v, web row %+v", a[i], w[i])
				}
			}
			if (c.want == "") != (len(a) == 1) {
				t.Errorf("rows %+v for want %q", a, c.want)
			}
		})
	}

	// The account page writes only Status, so an unknown key cannot be sent
	// through its form; compare the API against WriteSlot itself instead.
	t.Run("unknown key", func(t *testing.T) {
		code, msg := api("PUT", "Mood", map[string]string{"value": "ok"})
		_, err := e.st.WriteSlot(ctx, webUser.ID, store.SlotWrite{Key: "Mood", Value: "ok", Source: webSource})
		var ve *store.ValidationError
		if code != http.StatusUnprocessableEntity || !errors.As(err, &ve) || ve.Message != msg || ve.Field != "key" {
			t.Errorf("api %d %q, WriteSlot %v", code, msg, err)
		}
	})

	// Clearing: DELETE and the Clear button both remove the row.
	t.Run("clear", func(t *testing.T) {
		if code, msg := api("PUT", "Status", map[string]string{"value": "x"}); code != http.StatusNoContent {
			t.Fatalf("api put: %d %q", code, msg)
		}
		if code, msg := web(url.Values{"action": {"status"}, "value": {"x"}, "expiry": {"none"}}); code != http.StatusSeeOther {
			t.Fatalf("web put: %d %q", code, msg)
		}
		if code, msg := api("DELETE", "Status", nil); code != http.StatusNoContent {
			t.Errorf("api delete: %d %q", code, msg)
		}
		if code, msg := web(url.Values{"action": {"clear-status"}}); code != http.StatusSeeOther {
			t.Errorf("web clear: %d %q", code, msg)
		}
		if a, w := slots(apiUser), slots(webUser); len(a) != 0 || len(w) != 0 {
			t.Errorf("rows after clear: api %+v, web %+v", a, w)
		}
	})
}
