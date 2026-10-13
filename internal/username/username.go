// Package username enforces the username policy in docs/usernames.md.
package username

import (
	"errors"
	"fmt"
	"strings"
)

// Length limits, in bytes (names are ASCII).
const (
	MinLen = 2
	MaxLen = 32
)

// Sentinel errors. Every error Validate returns wraps exactly one of these.
var (
	// ErrInvalid means the name breaks a character or length rule.
	ErrInvalid = errors.New("invalid username")
	// ErrReserved means the name is well-formed but never issued.
	ErrReserved = errors.New("reserved username")
)

// Reserved lists the names that are never issued: the list in
// docs/usernames.md plus every top-level route the site serves. Adding a
// route means adding its name here and to the doc. It does not include
// "mike", which is held for Mike rather than reserved.
var Reserved = []string{
	// docs/usernames.md
	"nowfile", "admin", "root", "www", "mail", "abuse", "postmaster", "hostmaster",
	"webmaster", "security", "support", "help", "about", "api", "static", "finger",
	"plan", "status", "me", "you", "null", "undefined", "anonymous", "nobody",
	"system", "daemon", "user", "test", "login", "logout", "signup", "signin",
	"settings", "account", "terms", "privacy", "blog", "docs", "dev", "ops",
	// top-level routes not already above
	"health",
}

var reserved = func() map[string]bool {
	m := make(map[string]bool, len(Reserved))
	for _, n := range Reserved {
		m[n] = true
	}
	return m
}()

// IsReserved reports whether name, compared case-insensitively, is reserved.
func IsReserved(name string) bool {
	return reserved[strings.ToLower(name)]
}

// Validate checks name against docs/usernames.md and returns the canonical
// lowercase form. Input is lowercased first, so "Mike" validates as "mike".
// It does not check whether the name is taken or in a release hold; the
// store does that.
func Validate(name string) (string, error) {
	n := strings.ToLower(name)
	if len(n) < MinLen || len(n) > MaxLen {
		return "", fmt.Errorf("%w: must be %d to %d characters", ErrInvalid, MinLen, MaxLen)
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", fmt.Errorf("%w: only letters, digits, and hyphens", ErrInvalid)
		}
	}
	if n[0] < 'a' || n[0] > 'z' {
		return "", fmt.Errorf("%w: must start with a letter", ErrInvalid)
	}
	if n[len(n)-1] == '-' {
		return "", fmt.Errorf("%w: cannot end with a hyphen", ErrInvalid)
	}
	if strings.Contains(n, "--") {
		return "", fmt.Errorf("%w: no two hyphens in a row", ErrInvalid)
	}
	if reserved[n] {
		return "", fmt.Errorf("%w: %s", ErrReserved, n)
	}
	return n, nil
}
