package username

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		in, want string
		err      error
	}{
		{"mary", "mary", nil},
		{"Mary", "mary", nil},
		{"MARY", "mary", nil},
		{"Mike", "", ErrReserved}, // held for Mike
		{"ab", "ab", nil},
		{"a1", "a1", nil},
		{"mary-jane", "mary-jane", nil},
		{"a-b-c9", "a-b-c9", nil},
		{strings.Repeat("a", 32), strings.Repeat("a", 32), nil},

		{"", "", ErrInvalid},
		{"a", "", ErrInvalid},                     // too short
		{strings.Repeat("a", 33), "", ErrInvalid}, // too long
		{"1abc", "", ErrInvalid},                  // must start with a letter
		{"-abc", "", ErrInvalid},
		{"abc-", "", ErrInvalid}, // trailing hyphen
		{"ab--c", "", ErrInvalid},
		{"ab_c", "", ErrInvalid},
		{"ab.c", "", ErrInvalid},
		{"ab c", "", ErrInvalid},
		{"mike@host", "", ErrInvalid},
		{"café", "", ErrInvalid},
		{"Ǆa", "", ErrInvalid}, // non-ASCII that lowercases oddly

		{"admin", "", ErrReserved},
		{"Admin", "", ErrReserved},
		{"nowfile", "", ErrReserved},
		{"postmaster", "", ErrReserved},
		{"me", "", ErrReserved},
		{"ops", "", ErrReserved},
		{"health", "", ErrReserved},
		{"api", "", ErrReserved},
		{"signin", "", ErrReserved},
	}
	for _, c := range cases {
		got, err := Validate(c.in)
		if c.err == nil {
			if err != nil || got != c.want {
				t.Errorf("Validate(%q) = %q, %v; want %q", c.in, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, c.err) {
			t.Errorf("Validate(%q) error = %v, want %v", c.in, err, c.err)
		}
		if got != "" {
			t.Errorf("Validate(%q) returned %q with an error", c.in, got)
		}
	}
}

func TestReservedListIsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range Reserved {
		if seen[n] {
			t.Errorf("%q listed twice", n)
		}
		seen[n] = true
		if n != strings.ToLower(n) {
			t.Errorf("%q not lowercase", n)
		}
		if !IsReserved(strings.ToUpper(n)) {
			t.Errorf("IsReserved(%q) false", strings.ToUpper(n))
		}
	}
	for _, route := range []string{"health", "api", "static", "account", "login", "logout", "signup", "signin", "about"} {
		if !seen[route] {
			t.Errorf("route %q not reserved", route)
		}
	}
	if !IsReserved("Mike") {
		t.Error("mike is held for Mike and must be reserved")
	}
}

func TestValidateHeld(t *testing.T) {
	if _, err := Validate("Mike"); !errors.Is(err, ErrReserved) {
		t.Errorf("Validate(Mike) = %v, want ErrReserved", err)
	}
	for _, n := range []string{"Mike", "admin", "alice"} {
		if got, err := ValidateHeld(n); err != nil || got != strings.ToLower(n) {
			t.Errorf("ValidateHeld(%q) = %q, %v", n, got, err)
		}
	}
	for _, n := range []string{"x", "1abc", "a--b", "bad-", "no_underscore"} {
		if _, err := ValidateHeld(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateHeld(%q) = %v, want ErrInvalid", n, err)
		}
	}
}
