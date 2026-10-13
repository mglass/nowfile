// Package admin implements "nowfile admin ...": operator commands run inside
// the container against the server's database.
package admin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mglass/nowfile/internal/store"
)

// Usage lists the admin commands.
const Usage = `usage:
  nowfile admin create-user <name> <email>   create an account; may issue reserved or held names such as mike
  nowfile admin create-token <name> <label>  mint an API token for an account and print it once
  nowfile admin suspend <name>               stop serving the account and reject its tokens and sign-in links
  nowfile admin release <name>               lift a suspension
`

// Run runs one admin command. args excludes "admin". Output goes to out.
func Run(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("no admin command\n" + Usage)
	}
	cmd, args := args[0], args[1:]
	want := func(n int, names string) error {
		if len(args) != n {
			return fmt.Errorf("usage: nowfile admin %s %s", cmd, names)
		}
		return nil
	}
	switch cmd {
	case "create-user":
		if err := want(2, "<name> <email>"); err != nil {
			return err
		}
		u, err := st.CreateUserHeld(ctx, args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "created %s <%s> id %d\n", u.Name, u.Email, u.ID)
	case "create-token":
		if err := want(2, "<name> <label>"); err != nil {
			return err
		}
		u, err := st.UserByName(ctx, args[0])
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		plain, tok, err := st.CreateToken(ctx, u.ID, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "token %d %q for %s (shown once):\n%s\n", tok.ID, tok.Label, u.Name, plain)
	case "suspend", "release":
		if err := want(1, "<name>"); err != nil {
			return err
		}
		f, done := st.Suspend, "suspended"
		if cmd == "release" {
			f, done = st.Release, "released"
		}
		if err := f(ctx, args[0]); err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		fmt.Fprintf(out, "%s %s\n", done, args[0])
	case "help", "-h", "--help":
		fmt.Fprint(out, Usage)
	default:
		return fmt.Errorf("unknown admin command %q\n%s", cmd, Usage)
	}
	return nil
}
