package store

import (
	"errors"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "alice", "alice@example.com")

	plain, err := s.CreateSession(ctx, u.ID, c.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionUser(ctx, plain)
	if err != nil || got.ID != u.ID {
		t.Fatalf("SessionUser = %+v, %v; want user %d", got, err, u.ID)
	}
	if _, err := s.SessionUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v, want ErrNotFound", err)
	}

	if err := s.Suspend(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, plain); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended: %v, want ErrSuspended", err)
	}
	if err := s.Release(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	c.Add(time.Hour)
	if _, err := s.SessionUser(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v, want ErrNotFound", err)
	}

	plain, err = s.CreateSession(ctx, u.ID, c.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session: %v, want ErrNotFound", err)
	}

	plain, err = s.CreateSession(ctx, u.ID, c.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted account: %v, want ErrNotFound", err)
	}
	if _, err := s.CreateSession(ctx, u.ID, c.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateSession for deleted account: %v, want ErrNotFound", err)
	}
}
