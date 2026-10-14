package store

import (
	"errors"
	"testing"
	"time"
)

func TestSources(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	other := mustUser(t, s, "ann", "ann@example.com")

	if _, err := s.Source(ctx, u.ID, "calendar"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no source yet: %v", err)
	}
	if err := s.SetSourceEnabled(ctx, u.ID, "calendar", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable before preview: %v", err)
	}

	src, err := s.PutSource(ctx, u.ID, "calendar", `{"url":"https://a.example/cal.ics"}`, `{"n":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if src.Enabled || src.LastRun.IsZero() || src.State != `{"n":1}` {
		t.Fatalf("new source = %+v", src)
	}
	if list, _ := s.EnabledSources(ctx, "calendar"); len(list) != 0 {
		t.Fatalf("disabled source listed: %v", list)
	}
	if err := s.SetSourceEnabled(ctx, u.ID, "calendar", true); err != nil {
		t.Fatal(err)
	}
	list, err := s.EnabledSources(ctx, "calendar")
	if err != nil || len(list) != 1 || list[0].ID != src.ID {
		t.Fatalf("enabled = %v %v", list, err)
	}

	// Previewing the same URL again keeps it enabled; a new URL disables it.
	if src, _ = s.PutSource(ctx, u.ID, "calendar", `{"url":"https://a.example/cal.ics"}`, `{}`); !src.Enabled {
		t.Error("same config should stay enabled")
	}
	if src, _ = s.PutSource(ctx, u.ID, "calendar", `{"url":"https://b.example/cal.ics"}`, `{}`); src.Enabled {
		t.Error("new config must not stay enabled")
	}
	s.SetSourceEnabled(ctx, u.ID, "calendar", true)
	src, _ = s.Source(ctx, u.ID, "calendar")

	// A run records only against the config it read.
	c.Add(time.Minute)
	if err := s.RecordSourceRun(ctx, src, s.Now(), "boom", `{"f":3}`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Source(ctx, u.ID, "calendar")
	if got.LastError != "boom" || got.State != `{"f":3}` || !got.LastRun.Equal(s.Now().UTC().Truncate(time.Second)) {
		t.Errorf("after run = %+v", got)
	}
	stale := src
	stale.Config = `{"url":"https://old.example/"}`
	if err := s.RecordSourceRun(ctx, stale, s.Now(), "", `{}`); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale run: %v", err)
	}
	s.SetSourceEnabled(ctx, u.ID, "calendar", false)
	if err := s.RecordSourceRun(ctx, src, s.Now(), "", `{}`); !errors.Is(err, ErrNotFound) {
		t.Errorf("run after disable: %v", err)
	}
	// Preview clears the error.
	if src, _ = s.PutSource(ctx, u.ID, "calendar", src.Config, `{}`); src.LastError != "" {
		t.Errorf("error kept after preview: %q", src.LastError)
	}

	// Suspended and deleted accounts are not run.
	s.PutSource(ctx, other.ID, "calendar", `{}`, `{}`)
	s.SetSourceEnabled(ctx, other.ID, "calendar", true)
	s.SetSourceEnabled(ctx, u.ID, "calendar", true)
	if list, _ := s.EnabledSources(ctx, "calendar"); len(list) != 2 {
		t.Fatalf("want 2 enabled, got %d", len(list))
	}
	s.Suspend(ctx, "ann")
	if list, _ := s.EnabledSources(ctx, "calendar"); len(list) != 1 {
		t.Errorf("suspended account listed")
	}
	if err := s.DeleteSource(ctx, u.ID, "calendar"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, u.ID, "calendar"); err != nil {
		t.Errorf("second remove: %v", err)
	}
	s.Release(ctx, "ann")
	if err := s.DeleteUser(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.EnabledSources(ctx, "calendar"); len(list) != 0 {
		t.Errorf("deleted account's source listed: %v", list)
	}
}

func TestTimeZone(t *testing.T) {
	s, _ := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	if tz, err := s.TimeZone(ctx, u.ID); err != nil || tz != "" {
		t.Fatalf("default tz = %q %v", tz, err)
	}
	if err := s.SetTimeZone(ctx, u.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	// The profile form must not clobber it.
	if err := s.PutStatic(ctx, u.ID, Static{Name: "Mike"}); err != nil {
		t.Fatal(err)
	}
	if tz, _ := s.TimeZone(ctx, u.ID); tz != "America/New_York" {
		t.Errorf("tz = %q", tz)
	}
	for _, bad := range []string{"Local", "Mars/Olympus", "../../etc/passwd"} {
		if err := s.SetTimeZone(ctx, u.ID, bad); !errors.Is(err, ErrInvalidTimeZone) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := s.SetTimeZone(ctx, u.ID, ""); err != nil {
		t.Errorf("clearing: %v", err)
	}
}
