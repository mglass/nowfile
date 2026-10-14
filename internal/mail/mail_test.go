package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPostmark(t *testing.T) {
	var got map[string]string
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Postmark-Server-Token")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ErrorCode":0}`))
	}))
	defer srv.Close()

	p := &Postmark{Token: "tok", From: "hello@nowfile.org", Endpoint: srv.URL}
	if err := p.Send(context.Background(), Message{To: "a@example.com", Subject: "Sign in", Text: "link"}); err != nil {
		t.Fatal(err)
	}
	if token != "tok" {
		t.Errorf("token header = %q", token)
	}
	want := map[string]string{"From": "hello@nowfile.org", "To": "a@example.com", "Subject": "Sign in", "TextBody": "link", "MessageStream": "outbound"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestPostmarkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"ErrorCode":300,"Message":"Invalid email request"}`))
	}))
	defer srv.Close()
	p := &Postmark{Token: "tok", From: "x@y", Endpoint: srv.URL}
	err := p.Send(context.Background(), Message{To: "a@example.com"})
	if err == nil || !strings.Contains(err.Error(), "Invalid email request") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewFallsBackToLog(t *testing.T) {
	if _, ok := New("", "x@y").(Log); !ok {
		t.Fatal("New with empty token is not Log")
	}
	if _, ok := New("tok", "x@y").(*Postmark); !ok {
		t.Fatal("New with token is not Postmark")
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	Log{}.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Text: "https://nowfile.org/login/abc"})
	if !strings.Contains(buf.String(), "https://nowfile.org/login/abc") {
		t.Fatalf("log missing link: %q", buf.String())
	}
}

func TestPostmarkHeaders(t *testing.T) {
	var got struct {
		Headers []struct{ Name, Value string }
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ErrorCode":0}`))
	}))
	defer srv.Close()
	p := &Postmark{Token: "tok", From: "nowfile@nowfile.org", Endpoint: srv.URL}
	m := Message{To: "a@example.com", Subject: "s", Text: "t", Headers: map[string]string{
		"List-Unsubscribe":      "<https://nowfile.org/mike/unsubscribe/x>",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}}
	if err := p.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 2 ||
		got.Headers[0].Name != "List-Unsubscribe" || got.Headers[0].Value != "<https://nowfile.org/mike/unsubscribe/x>" ||
		got.Headers[1].Name != "List-Unsubscribe-Post" || got.Headers[1].Value != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %+v", got.Headers)
	}
}
