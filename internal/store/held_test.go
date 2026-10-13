package store

import (
	"errors"
	"testing"

	"github.com/mglass/nowfile/internal/username"
)

func TestCreateUserHeld(t *testing.T) {
	s, _ := open(t)
	if _, err := s.CreateUser(ctx, "mike", "mike@example.com"); !errors.Is(err, username.ErrReserved) {
		t.Fatalf("public CreateUser(mike) = %v, want ErrReserved", err)
	}
	u, err := s.CreateUserHeld(ctx, "Mike", "mike@example.com")
	if err != nil || u.Name != "mike" {
		t.Fatalf("CreateUserHeld(Mike) = %+v, %v", u, err)
	}
	if _, err := s.CreateUserHeld(ctx, "mike", "other@example.com"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("second CreateUserHeld(mike) = %v, want ErrNameTaken", err)
	}
	if _, err := s.CreateUser(ctx, "MIKE", "other@example.com"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("public CreateUser on an issued held name = %v, want ErrNameTaken", err)
	}
	if _, err := s.CreateUserHeld(ctx, "a--b", "x@example.com"); !errors.Is(err, username.ErrInvalid) {
		t.Errorf("CreateUserHeld bad name = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateUserHeld(ctx, "admin", "mike@example.com"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("CreateUserHeld taken email = %v, want ErrEmailTaken", err)
	}
}
