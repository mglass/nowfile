package format

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// example is the document from docs/format.md, byte for byte.
const example = `nowfile/0
Name: Mike Glass
Headline: Builds internal tools for small companies. Jacksonville, FL.
Photo: https://glasseffect.com/mike.jpg
Link: https://glasseffect.com
Link: https://github.com/mglass

Status: Lunch, back at 1
  set: 2026-10-13T12:04:10-04:00
  until: 2026-10-13T13:00:00-04:00
Calendar: Busy until 15:00
  until: 2026-10-13T15:00:00-04:00
  source: calendar
Focus: Work
  set: 2026-10-13T09:00:00-04:00
Listening: The War on Drugs, Harmonia's Dream
  set: 2026-10-13T12:31:40-04:00
  until: 2026-10-13T12:40:00-04:00
  source: lastfm

Plan:
Reply within a day. Text for anything urgent.
`

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseExample(t *testing.T) {
	doc, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != "0" || doc.Name != "Mike Glass" || doc.Photo != "https://glasseffect.com/mike.jpg" {
		t.Errorf("static fields: %+v", doc)
	}
	if len(doc.Links) != 2 || doc.Links[1] != "https://github.com/mglass" {
		t.Errorf("links: %v", doc.Links)
	}
	if doc.Plan != "Reply within a day. Text for anything urgent." {
		t.Errorf("plan: %q", doc.Plan)
	}
	want := []Slot{
		{Key: "Status", Value: "Lunch, back at 1", Set: ts(t, "2026-10-13T12:04:10-04:00"), Until: ts(t, "2026-10-13T13:00:00-04:00")},
		{Key: "Calendar", Value: "Busy until 15:00", Until: ts(t, "2026-10-13T15:00:00-04:00"), Source: "calendar"},
		{Key: "Focus", Value: "Work", Set: ts(t, "2026-10-13T09:00:00-04:00")},
		{Key: "Listening", Value: "The War on Drugs, Harmonia's Dream", Set: ts(t, "2026-10-13T12:31:40-04:00"), Until: ts(t, "2026-10-13T12:40:00-04:00"), Source: "lastfm"},
	}
	if len(doc.Slots) != len(want) {
		t.Fatalf("got %d slots, want %d", len(doc.Slots), len(want))
	}
	for i, w := range want {
		g := doc.Slots[i]
		if g.Key != w.Key || g.Value != w.Value || !g.Set.Equal(w.Set) || !g.Until.Equal(w.Until) || g.Source != w.Source {
			t.Errorf("slot %d: got %+v, want %+v", i, g, w)
		}
	}
	if err := Validate(doc); err != nil {
		t.Errorf("Validate(example): %v", err)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	doc, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	if got := Render(doc, true); got != example {
		t.Errorf("long form differs from docs/format.md example:\n--- got\n%s--- want\n%s", got, example)
	}
	short := Render(doc, false)
	wantShort := strings.TrimSuffix(example, "\nPlan:\nReply within a day. Text for anything urgent.\n")
	if short != wantShort {
		t.Errorf("short form:\n--- got\n%s--- want\n%s", short, wantShort)
	}
	if strings.Contains(short, "Plan:") {
		t.Error("short form contains the Plan block")
	}
	again, _ := Parse(short)
	if Render(again, false) != short {
		t.Error("short form does not round-trip")
	}
}

func TestRenderSections(t *testing.T) {
	cases := []struct {
		name string
		doc  Document
		long bool
		want string
	}{
		{"empty", Document{Version: "0"}, true, "nowfile/0\n"},
		{"static only", Document{Version: "0", Name: "A"}, true, "nowfile/0\nName: A\n"},
		{"slots only", Document{Version: "0", Slots: []Slot{{Key: "Status", Value: "hi"}}}, true, "nowfile/0\n\nStatus: hi\n"},
		{"plan only long", Document{Version: "0", Plan: "x\ny"}, true, "nowfile/0\n\nPlan:\nx\ny\n"},
		{"plan only short", Document{Version: "0", Plan: "x"}, false, "nowfile/0\n"},
		{"utc timestamps", Document{Version: "0", Slots: []Slot{{Key: "Focus", Value: "Work", Set: time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)}}}, false,
			"nowfile/0\n\nFocus: Work\n  set: 2026-10-13T13:00:00Z\n"},
		{"classic", Document{Plan: "just text"}, true, "just text\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Render(c.doc, c.long); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestComposeDropsExpired(t *testing.T) {
	doc, _ := Parse(example)
	cases := []struct {
		now  string
		want []string
	}{
		{"2026-10-13T12:35:00-04:00", []string{"Status", "Calendar", "Focus", "Listening"}},
		{"2026-10-13T12:40:00-04:00", []string{"Status", "Calendar", "Focus"}}, // until == now is expired
		{"2026-10-13T13:30:00-04:00", []string{"Calendar", "Focus"}},
		{"2026-10-13T19:00:00Z", []string{"Focus"}}, // 15:00-04:00 == 19:00Z
		{"2030-01-01T00:00:00Z", []string{"Focus"}},
	}
	for _, c := range cases {
		t.Run(c.now, func(t *testing.T) {
			now := ts(t, c.now)
			got := Compose(doc, now)
			var keys []string
			for _, s := range got.Slots {
				keys = append(keys, s.Key)
			}
			if strings.Join(keys, ",") != strings.Join(c.want, ",") {
				t.Errorf("slots %v, want %v", keys, c.want)
			}
			// The invariant: no expired slot survives into any rendering.
			for _, s := range got.Slots {
				if s.Expired(now) {
					t.Errorf("expired slot %s emitted", s.Key)
				}
			}
			for _, long := range []bool{false, true} {
				out := Render(got, long)
				for _, s := range doc.Slots {
					if s.Expired(now) && strings.Contains(out, s.Key+":") {
						t.Errorf("render(long=%v) contains expired %s", long, s.Key)
					}
				}
			}
		})
	}
	if len(doc.Slots) != 4 {
		t.Errorf("Compose modified its input: %d slots", len(doc.Slots))
	}
}

func TestHeadlinePrecedence(t *testing.T) {
	s := func(k string) Slot { return Slot{Key: k, Value: k + " value"} }
	cases := []struct {
		slots []Slot
		want  string
	}{
		{[]Slot{s("Listening"), s("Focus"), s("Calendar"), s("Status")}, "Status value"},
		{[]Slot{s("Listening"), s("Focus"), s("Calendar")}, "Calendar value"},
		{[]Slot{s("Listening"), s("Focus"), s("Working")}, "Focus value"},
		{[]Slot{s("Working"), s("Listening")}, "Listening value"},
		{[]Slot{s("Working"), s("Reading")}, "static"},
		{nil, "static"},
	}
	for _, c := range cases {
		if got := Headline(Document{Version: "0", Headline: "static", Slots: c.slots}); got != c.want {
			t.Errorf("Headline(%v) = %q, want %q", c.slots, got, c.want)
		}
	}
	// Expired Status falls through once composed.
	doc, _ := Parse(example)
	if got := Headline(Compose(doc, ts(t, "2026-10-13T13:30:00-04:00"))); got != "Busy until 15:00" {
		t.Errorf("composed headline = %q", got)
	}
}

func TestParseClassic(t *testing.T) {
	cases := []struct{ name, in, plan string }{
		{"plain", "Working on the compiler.\nBack Monday.\n", "Working on the compiler.\nBack Monday."},
		{"crlf", "line one\r\nline two\r\n", "line one\nline two"},
		{"looks like fields", "Status: not a nowfile\nName: x\n", "Status: not a nowfile\nName: x"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := Parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Version != "" || doc.Plan != c.plan || len(doc.Slots) != 0 || doc.Name != "" {
				t.Errorf("got %+v, want classic with plan %q", doc, c.plan)
			}
		})
	}
}

func TestParseFingerdWrapper(t *testing.T) {
	in := "Login: mike           \t\t\tName: Mike Glass\r\n" +
		"Directory: /home/mike               \tShell: /bin/zsh\r\n" +
		"Last login Mon Oct 12 09:14 (EDT) on ttys000\r\n" +
		"No Mail.\r\n" +
		"Plan:\r\n" +
		"nowfile/0\r\n" +
		"Name: Mike Glass\r\n" +
		"\r\n" +
		"status: Heads down\r\n" +
		"  until: 2026-10-13T17:00:00-04:00\r\n" +
		"X-Mood: fine\r\n" +
		"  source: me\r\n" +
		"\r\n" +
		"Plan:\r\n" +
		"Long text.\r\n" +
		"  indented line\r\n"
	doc, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != "0" || doc.Name != "Mike Glass" {
		t.Errorf("wrapper not skipped: %+v", doc)
	}
	if len(doc.Slots) != 2 || doc.Slots[0].Key != "Status" || doc.Slots[0].Value != "Heads down" {
		t.Fatalf("slots: %+v", doc.Slots)
	}
	if !doc.Slots[0].Until.Equal(ts(t, "2026-10-13T17:00:00-04:00")) {
		t.Errorf("until: %v", doc.Slots[0].Until)
	}
	if doc.Slots[1].Key != "X-Mood" || doc.Slots[1].Source != "me" || IsSlotKey(doc.Slots[1].Key) {
		t.Errorf("unknown key not kept as-is: %+v", doc.Slots[1])
	}
	if doc.Plan != "Long text.\n  indented line" {
		t.Errorf("plan: %q", doc.Plan)
	}
}

func TestParseLenient(t *testing.T) {
	in := "nowfile/0\nName: First\nNAME: Second\nnot a field\nStatus: a\n  until: garbage\n  colour: blue\nStatus: b\n"
	doc, _ := Parse(in)
	if doc.Name != "First" {
		t.Errorf("first Name should win, got %q", doc.Name)
	}
	if len(doc.Slots) != 1 || doc.Slots[0].Value != "a" || !doc.Slots[0].Until.IsZero() {
		t.Errorf("slots: %+v", doc.Slots)
	}
}

func TestKnownSlotKeys(t *testing.T) {
	for _, k := range []string{"Status", "status", "CALENDAR", "focus", "Listening", "working", "Reading", "watching", "Playing"} {
		if !IsSlotKey(k) {
			t.Errorf("IsSlotKey(%q) = false", k)
		}
	}
	for _, k := range []string{"", "Name", "Headline", "Plan", "Link", "Mood"} {
		if IsSlotKey(k) {
			t.Errorf("IsSlotKey(%q) = true", k)
		}
	}
	if k, _ := CanonicalSlotKey("lIsTeNiNg"); k != "Listening" {
		t.Errorf("canonical = %q", k)
	}
}

func TestValidate(t *testing.T) {
	ok := Document{Version: "0", Name: "A", Slots: []Slot{{Key: "Status", Value: "x"}}}
	long := strings.Repeat("a", MaxLineBytes)
	cases := []struct {
		name string
		mod  func(*Document)
		bad  bool
	}{
		{"ok", func(*Document) {}, false},
		{"five links", func(d *Document) { d.Links = []string{"a", "b", "c", "d", "e"} }, false},
		{"six links", func(d *Document) { d.Links = []string{"a", "b", "c", "d", "e", "f"} }, true},
		{"empty link", func(d *Document) { d.Links = []string{""} }, true},
		{"duplicate slot", func(d *Document) { d.Slots = append(d.Slots, Slot{Key: "status", Value: "y"}) }, true},
		{"slot named Name", func(d *Document) { d.Slots = append(d.Slots, Slot{Key: "Name", Value: "y"}) }, true},
		{"slot named Plan", func(d *Document) { d.Slots = append(d.Slots, Slot{Key: "plan", Value: "y"}) }, true},
		{"bad key", func(d *Document) { d.Slots = append(d.Slots, Slot{Key: "a b", Value: "y"}) }, true},
		{"empty value", func(d *Document) { d.Slots[0].Value = "" }, true},
		{"newline value", func(d *Document) { d.Slots[0].Value = "a\nb" }, true},
		{"newline name", func(d *Document) { d.Name = "a\nWorking: injected" }, true},
		{"padded value", func(d *Document) { d.Name = " a" }, true},
		{"source with space", func(d *Document) { d.Slots[0].Source = "a b" }, true},
		{"line at limit", func(d *Document) { d.Name = long[:MaxLineBytes-len("Name: ")] }, false},
		{"line over limit", func(d *Document) { d.Name = long[:MaxLineBytes-len("Name: ")+1] }, true},
		{"slot line over limit", func(d *Document) { d.Slots[0].Value = long }, true},
		{"plan line over limit", func(d *Document) { d.Plan = "ok\n" + long + "a" }, true},
		{"plan at limit", func(d *Document) { d.Plan = long }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ok
			d.Slots = append([]Slot(nil), ok.Slots...)
			c.mod(&d)
			err := Validate(d)
			if (err != nil) != c.bad {
				t.Fatalf("Validate = %v, want error %v", err, c.bad)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}
