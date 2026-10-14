package ics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// busyFile runs Busy over a fixture and returns spans as "start/end" strings
// in UTC.
func busyFile(t *testing.T, name string, floating *time.Location, from, to string) ([]string, []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	spans, warns, err := Busy(data, floating, ts(from), ts(to))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out []string
	for _, s := range spans {
		out = append(out, s.Start.UTC().Format(time.RFC3339)+"/"+s.End.UTC().Format(time.RFC3339))
	}
	return out, warns
}

func same(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\ngot\n  %s\nwant\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestBasic(t *testing.T) {
	// All-day, transparent, free, cancelled, and no-end events do not
	// count. Floating times read in the given zone; quoted TZID and folded
	// lines parse.
	got, warns := busyFile(t, "basic.ics", mustLoad(t, "America/New_York"), "2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z")
	same(t, "basic", got, []string{
		"2026-10-14T14:00:00Z/2026-10-14T15:00:00Z",
		"2026-10-14T17:00:00Z/2026-10-14T17:30:00Z",
		"2026-10-14T19:00:00Z/2026-10-14T20:00:00Z",
	})
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
}

func TestOverlapMerge(t *testing.T) {
	got, _ := busyFile(t, "overlap.ics", time.UTC, "2026-10-15T00:00:00Z", "2026-10-16T00:00:00Z")
	same(t, "overlap", got, []string{
		"2026-10-15T09:00:00Z/2026-10-15T11:00:00Z",
		"2026-10-15T11:15:00Z/2026-10-15T12:00:00Z",
	})
}

func TestDaily(t *testing.T) {
	// UNTIL with a TZID EXDATE, INTERVAL with COUNT, and BYDAY with COUNT
	// where a date-valued EXDATE still uses up one of the count.
	got, warns := busyFile(t, "daily.ics", time.UTC, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")
	same(t, "daily", got, []string{
		"2026-10-12T13:00:00Z/2026-10-12T13:30:00Z",
		"2026-10-13T13:00:00Z/2026-10-13T13:30:00Z",
		"2026-10-15T13:00:00Z/2026-10-15T13:30:00Z",
		"2026-10-16T08:00:00Z/2026-10-16T08:15:00Z",
		"2026-10-16T13:00:00Z/2026-10-16T13:30:00Z",
		"2026-10-20T08:00:00Z/2026-10-20T08:15:00Z",
		"2026-10-20T14:00:00Z/2026-10-20T15:00:00Z",
		"2026-10-22T14:00:00Z/2026-10-22T15:00:00Z",
		"2026-10-24T14:00:00Z/2026-10-24T15:00:00Z",
	})
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
}

func TestWeekly(t *testing.T) {
	// BYDAY with COUNT and EXDATE, INTERVAL with UNTIL, and an open-ended
	// rule from last year with one instance moved and one cancelled by
	// RECURRENCE-ID.
	got, warns := busyFile(t, "weekly.ics", time.UTC, "2026-10-01T00:00:00Z", "2026-11-05T00:00:00Z")
	same(t, "weekly", got, []string{
		"2026-10-01T18:00:00Z/2026-10-01T18:30:00Z",
		"2026-10-05T15:00:00Z/2026-10-05T16:00:00Z",
		"2026-10-06T12:00:00Z/2026-10-06T12:45:00Z",
		"2026-10-07T15:00:00Z/2026-10-07T16:00:00Z",
		"2026-10-08T18:00:00Z/2026-10-08T18:30:00Z",
		"2026-10-12T15:00:00Z/2026-10-12T16:00:00Z",
		"2026-10-14T15:00:00Z/2026-10-14T16:00:00Z",
		"2026-10-15T20:00:00Z/2026-10-15T20:30:00Z",
		"2026-10-20T12:00:00Z/2026-10-20T12:45:00Z",
		"2026-10-29T18:00:00Z/2026-10-29T18:30:00Z",
	})
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
}

func TestVTimezoneDST(t *testing.T) {
	// "Eastern Standard Time" is not in the Go database, so this exercises
	// the document's own VTIMEZONE. 09:00 stays 09:00 local across both
	// changes.
	if _, ok := loadLocation("Eastern Standard Time"); ok {
		t.Fatal("fixture TZID unexpectedly loadable; the VTIMEZONE path is not tested")
	}
	got, warns := busyFile(t, "vtimezone.ics", time.UTC, "2026-10-01T00:00:00Z", "2027-04-01T00:00:00Z")
	same(t, "vtimezone", got, []string{
		"2026-10-19T13:00:00Z/2026-10-19T14:00:00Z",
		"2026-10-26T13:00:00Z/2026-10-26T14:00:00Z",
		"2026-11-02T14:00:00Z/2026-11-02T15:00:00Z",
		"2026-11-09T14:00:00Z/2026-11-09T15:00:00Z",
		"2027-03-13T14:00:00Z/2027-03-13T15:00:00Z",
		"2027-03-15T13:00:00Z/2027-03-15T14:00:00Z",
	})
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
}

func TestVTimezoneMatchesGoDatabase(t *testing.T) {
	data, _ := os.ReadFile("testdata/vtimezone.ics")
	cal, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	vz, ok := newZones(cal, time.UTC).lookup("Eastern Standard Time")
	if !ok {
		t.Fatal("zone not defined")
	}
	loc := mustLoad(t, "America/New_York")
	ny := locZone{loc}
	// Every half hour through 2026 and 2027, including both gaps and both
	// repeated hours. In the skipped hour Go's choice is unspecified, so
	// there the RFC 5545 rule applies: read it in the offset before the gap
	// (EST), which lands an hour later on the clock.
	for c := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); c.Year() < 2028; c = c.Add(30 * time.Minute) {
		w := ny.instant(c)
		if wall := w.In(loc); wall.Hour() != c.Hour() || wall.Minute() != c.Minute() {
			w = c.Add(5 * time.Hour)
		}
		if g := vz.instant(c); !g.Equal(w) {
			t.Fatalf("civil %s: VTIMEZONE gives %s, Go gives %s", c.Format("2006-01-02 15:04"), g.UTC(), w.UTC())
		}
	}
}

func TestUnsupportedSkipped(t *testing.T) {
	got, warns := busyFile(t, "unsupported.ics", time.UTC, "2026-10-01T00:00:00Z", "2027-01-01T00:00:00Z")
	same(t, "unsupported", got, []string{
		"2026-10-13T17:00:00Z/2026-10-13T18:00:00Z",
		"2026-10-14T19:00:00Z/2026-10-14T19:30:00Z",
		"2026-11-10T17:00:00Z/2026-11-10T18:00:00Z",
		"2026-12-08T17:00:00Z/2026-12-08T18:00:00Z",
	})
	want := []string{"event 1: unsupported RRULE part BYSETPOS", "event 2: unsupported FREQ HOURLY", "event 3: DTSTART: unknown TZID"}
	if len(warns) != len(want) {
		t.Fatalf("warnings: %q", warns)
	}
	for i, w := range want {
		if !strings.HasPrefix(warns[i], w) {
			t.Errorf("warning %d = %q, want prefix %q", i, warns[i], w)
		}
	}
}

func TestNoSubjectSurvivesParsing(t *testing.T) {
	// Every fixture's subjects, descriptions, and locations start with
	// SECRET. The parser must not keep them anywhere, including warnings.
	files, _ := filepath.Glob("testdata/*.ics")
	for _, f := range files {
		data, _ := os.ReadFile(f)
		cal, err := parse(data)
		if err != nil {
			t.Fatal(err)
		}
		var walk func(c *component)
		walk = func(c *component) {
			for _, p := range c.props {
				if strings.Contains(p.value, "SECRET") || p.name == "SUMMARY" || p.name == "DESCRIPTION" || p.name == "LOCATION" {
					t.Errorf("%s: kept %s", f, p.name)
				}
			}
			for _, s := range c.subs {
				walk(s)
			}
		}
		walk(cal)
		_, warns, _ := Busy(data, time.UTC, ts("2020-01-01T00:00:00Z"), ts("2030-01-01T00:00:00Z"))
		for _, w := range warns {
			if strings.Contains(w, "SECRET") {
				t.Errorf("%s: warning carries content: %q", f, w)
			}
		}
	}
}

func TestNotCalendar(t *testing.T) {
	for _, in := range []string{"", "<html><body>Sign in</body></html>", "BEGIN:VCARD\r\nEND:VCARD\r\n"} {
		if _, _, err := Busy([]byte(in), time.UTC, time.Time{}, time.Now()); err != ErrNotCalendar {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestRunawayRuleTerminates(t *testing.T) {
	// BYMONTHDAY=31 with BYMONTH=2 never matches; expansion must stop.
	in := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260101T100000Z\r\nDTEND:20260101T110000Z\r\nRRULE:FREQ=DAILY;BYMONTH=2;BYMONTHDAY=31\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	done := make(chan struct{})
	go func() {
		Busy([]byte(in), time.UTC, ts("2026-01-01T00:00:00Z"), ts("2027-01-01T00:00:00Z"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expansion did not terminate")
	}
}

func TestLongRuleIsFast(t *testing.T) {
	// A daily rule from 1990 with no end should jump to the window rather
	// than walk 36 years.
	in := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nDTSTART;TZID=Europe/London:19900101T090000\nDURATION:PT1H\nRRULE:FREQ=DAILY\nEND:VEVENT\nEND:VCALENDAR\n"
	spans, _, err := Busy([]byte(in), time.UTC, ts("2026-10-14T00:00:00Z"), ts("2026-10-16T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || !spans[0].Start.Equal(ts("2026-10-14T08:00:00Z")) {
		t.Errorf("spans = %v", spans)
	}
}

func TestMergeCurrentNext(t *testing.T) {
	sp := func(a, b string) Span { return Span{ts(a), ts(b)} }
	m := Merge([]Span{
		sp("2026-10-14T13:00:00Z", "2026-10-14T14:00:00Z"),
		sp("2026-10-14T10:00:00Z", "2026-10-14T11:00:00Z"),
		sp("2026-10-14T11:00:00Z", "2026-10-14T11:30:00Z"), // touches
		sp("2026-10-14T10:15:00Z", "2026-10-14T10:45:00Z"), // inside
	})
	if len(m) != 2 || !m[0].End.Equal(ts("2026-10-14T11:30:00Z")) {
		t.Fatalf("merge = %v", m)
	}
	if c, ok := Current(m, ts("2026-10-14T11:00:00Z")); !ok || !c.End.Equal(ts("2026-10-14T11:30:00Z")) {
		t.Errorf("current at the join = %v %v", c, ok)
	}
	if _, ok := Current(m, ts("2026-10-14T11:30:00Z")); ok {
		t.Error("current at the end should be nothing")
	}
	if n, ok := Next(m, ts("2026-10-14T11:30:00Z")); !ok || !n.Start.Equal(ts("2026-10-14T13:00:00Z")) {
		t.Errorf("next = %v %v", n, ok)
	}
	if _, ok := Next(m, ts("2026-10-14T13:00:00Z")); ok {
		t.Error("next after the last start should be nothing")
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string][2]any{
		"PT1H30M": {0, 90 * time.Minute}, "P1D": {1, time.Duration(0)}, "P2W": {14, time.Duration(0)},
		"P1DT2H": {1, 2 * time.Hour}, "+PT15M": {0, 15 * time.Minute}, "PT45S": {0, 45 * time.Second},
	} {
		d, x, err := parseDuration(in)
		if err != nil || d != want[0].(int) || x != want[1].(time.Duration) {
			t.Errorf("%s = %d %s %v", in, d, x, err)
		}
	}
	for _, in := range []string{"", "1H", "P1H", "PT1D", "-PT1H", "PT1H2"} {
		if _, _, err := parseDuration(in); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
}
