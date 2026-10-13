package admin

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/username"
)

var ctx = context.Background()

func run(t *testing.T, st *store.Store, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := Run(ctx, st, args, &out)
	return out.String(), err
}

func TestAdmin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The public path refuses mike; the admin path issues it.
	if _, err := st.CreateUser(ctx, "mike", "x@example.com"); !errors.Is(err, username.ErrReserved) {
		t.Fatalf("public CreateUser(mike) = %v", err)
	}
	out, err := run(t, st, "create-user", "Mike", "mike@example.com")
	if err != nil || !strings.Contains(out, "created mike <mike@example.com>") {
		t.Fatalf("create-user: %q, %v", out, err)
	}
	if _, err := run(t, st, "create-user", "mike", "other@example.com"); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("duplicate create-user: %v", err)
	}

	out, err = run(t, st, "create-token", "mike", "cli")
	if err != nil {
		t.Fatalf("create-token: %v", err)
	}
	plain := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	if _, err := st.LookupToken(ctx, plain); err != nil {
		t.Fatalf("minted token %q does not authenticate: %v", plain, err)
	}

	if out, err := run(t, st, "suspend", "MIKE"); err != nil || out != "suspended MIKE\n" {
		t.Errorf("suspend: %q, %v", out, err)
	}
	if u, _ := st.UserByName(ctx, "mike"); !u.Suspended {
		t.Error("not suspended")
	}
	if _, err := st.LookupToken(ctx, plain); !errors.Is(err, store.ErrSuspended) {
		t.Errorf("token of suspended account: %v", err)
	}
	if _, err := run(t, st, "release", "mike"); err != nil {
		t.Errorf("release: %v", err)
	}
	if u, _ := st.UserByName(ctx, "mike"); u.Suspended {
		t.Error("still suspended")
	}

	if _, err := run(t, st, "suspend", "nobody-here"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("suspend unknown: %v", err)
	}
	for _, args := range [][]string{{}, {"frobnicate"}, {"suspend"}, {"create-user", "ann"}, {"release", "a", "b"}} {
		if _, err := run(t, st, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
