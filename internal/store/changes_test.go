package store

import (
	"errors"
	"testing"
	"time"
)

func changeValues(t *testing.T, s *Store, userID int64, keys ...string) []string {
	t.Helper()
	cs, err := s.Changes(ctx, userID, keys, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(cs) - 1; i >= 0; i-- { // oldest first reads better in tests
		out = append(out, cs[i].Key+"="+cs[i].Value)
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChangesRecordedByWriteAndClear(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")

	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "status", Value: "Lunch"}); err != nil {
		t.Fatal(err)
	}
	c.Add(time.Minute)
	// The same line again, as the calendar runner does every five minutes,
	// is not a change.
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "Lunch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "Back"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearSlot(ctx, u.ID, "status"); err != nil {
		t.Fatal(err)
	}
	// Clearing an unset slot is not a change.
	if err := s.ClearSlot(ctx, u.ID, "Status"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Focus", Value: "Work"}); err != nil {
		t.Fatal(err)
	}

	got := changeValues(t, s, u.ID, "Status", "Calendar")
	want := []string{"Status=Lunch", "Status=Back", "Status="}
	if !eqStrings(got, want) {
		t.Fatalf("changes = %q, want %q", got, want)
	}
	all := changeValues(t, s, u.ID, "Status", "Focus")
	if len(all) != 4 || all[3] != "Focus=Work" {
		t.Fatalf("changes with Focus = %q", all)
	}
	cs, _ := s.Changes(ctx, u.ID, []string{"Status"}, 1)
	if len(cs) != 1 || cs[0].Value != "" || !cs[0].Set.Equal(c.Now().Truncate(time.Second)) {
		t.Fatalf("latest change = %+v", cs)
	}
}

func TestChangesAfterExpiry(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	until := c.Now().Add(10 * time.Minute).Format(time.RFC3339)
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Calendar", Value: "Busy until 16:10", Until: until}); err != nil {
		t.Fatal(err)
	}
	c.Add(time.Hour)
	// The old row expired, so the same line showing again is a change, and
	// clearing an expired slot is not.
	if err := s.ClearSlot(ctx, u.ID, "Calendar"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Calendar", Value: "Busy until 16:10"}); err != nil {
		t.Fatal(err)
	}
	got := changeValues(t, s, u.ID, "Calendar")
	want := []string{"Calendar=Busy until 16:10", "Calendar=Busy until 16:10"}
	if !eqStrings(got, want) {
		t.Fatalf("changes = %q, want %q", got, want)
	}
}

func TestChangesLimitAndPrune(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	other := mustUser(t, s, "ann", "ann@example.com")
	for i := 0; i < 5; i++ {
		if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
		c.Add(30 * 24 * time.Hour)
	}
	if _, err := s.WriteSlot(ctx, other.ID, SlotWrite{Key: "Status", Value: "other"}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Changes(ctx, u.ID, []string{"Status"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Value != "e" || cs[1].Value != "d" {
		t.Fatalf("Changes limit 2 = %+v", cs)
	}
	// Now is 150 days after the first write: a and b are older than 90
	// days; c is exactly 90 and stays.
	n, err := s.PruneChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	if got := changeValues(t, s, u.ID, "Status"); !eqStrings(got, []string{"Status=c", "Status=d", "Status=e"}) {
		t.Fatalf("after prune = %q", got)
	}
}

func TestDeleteUserRemovesChangesAndSubscribers(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PendSubscriber(ctx, u.ID, "r@example.com", HashToken("tok"), c.Now().Add(time.Hour), "unsub"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM changes) + (SELECT COUNT(*) FROM subscribers)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after delete", n)
	}
}

func TestSubscriberLifecycle(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	if _, err := s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "Before"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PendSubscriber(ctx, u.ID, "nobody", "h", c.Now(), "u0"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("bad email: %v", err)
	}
	exp := c.Now().Add(24 * time.Hour)
	if err := s.PendSubscriber(ctx, u.ID, "Reader@Example.com", HashToken("first"), exp, "u1"); err != nil {
		t.Fatal(err)
	}
	// A second request replaces the confirmation secret and keeps the
	// unsubscribe secret.
	if err := s.PendSubscriber(ctx, u.ID, "reader@example.com", HashToken("second"), exp, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSubscriber(ctx, "first"); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("superseded link: %v", err)
	}
	if n, _ := s.CountSubscribers(ctx, u.ID); n != 0 {
		t.Fatalf("count before confirm = %d", n)
	}
	sub, err := s.ConfirmSubscriber(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Confirmed || sub.Email != "reader@example.com" || sub.Unsub != "u1" || sub.LastHeadline != "Before" || sub.LastChangeID == 0 {
		t.Fatalf("confirmed = %+v", sub)
	}
	if _, err := s.ConfirmSubscriber(ctx, "second"); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("second use: %v", err)
	}
	if n, _ := s.CountSubscribers(ctx, u.ID); n != 1 {
		t.Fatalf("count = %d", n)
	}
	if err := s.PendSubscriber(ctx, u.ID, "reader@example.com", HashToken("third"), exp, "u3"); !errors.Is(err, ErrAlreadySubscribed) {
		t.Fatalf("resubscribe: %v", err)
	}

	// Due only once there is a newer Status or Calendar change.
	due, err := s.DueSubscribers(ctx, []string{"Status", "Calendar"}, c.Now())
	if err != nil || len(due) != 0 {
		t.Fatalf("due before change = %+v, %v", due, err)
	}
	s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Focus", Value: "Work"})
	if due, _ = s.DueSubscribers(ctx, []string{"Status", "Calendar"}, c.Now()); len(due) != 0 {
		t.Fatalf("due after Focus = %+v", due)
	}
	s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "After"})
	due, _ = s.DueSubscribers(ctx, []string{"Status", "Calendar"}, c.Now())
	if len(due) != 1 || due[0].PendingChangeID <= sub.LastChangeID {
		t.Fatalf("due after Status = %+v", due)
	}
	sent := c.Now()
	if err := s.MarkNotified(ctx, sub.ID, due[0].PendingChangeID, "After", sent); err != nil {
		t.Fatal(err)
	}
	s.WriteSlot(ctx, u.ID, SlotWrite{Key: "Status", Value: "Later"})
	if due, _ = s.DueSubscribers(ctx, []string{"Status"}, sent.Add(-time.Second)); len(due) != 0 {
		t.Fatalf("due inside the window = %+v", due)
	}
	if due, _ = s.DueSubscribers(ctx, []string{"Status"}, sent); len(due) != 1 || due[0].LastHeadline != "After" || !due[0].LastSent.Equal(sent.Truncate(time.Second)) {
		t.Fatalf("due after the window = %+v", due)
	}

	if got, err := s.SubscriberByUnsub(ctx, "u1"); err != nil || got.ID != sub.ID {
		t.Fatalf("by unsub = %+v, %v", got, err)
	}
	if err := s.Unsubscribe(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unsubscribe: %v", err)
	}
	if n, _ := s.CountSubscribers(ctx, u.ID); n != 0 {
		t.Fatalf("count after unsubscribe = %d", n)
	}
}

func TestSubscriberConfirmExpiresAndPrunes(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	if err := s.PendSubscriber(ctx, u.ID, "r@example.com", HashToken("tok"), c.Now().Add(24*time.Hour), "u"); err != nil {
		t.Fatal(err)
	}
	c.Add(24 * time.Hour)
	if _, err := s.ConfirmSubscriber(ctx, "tok"); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("expired link: %v", err)
	}
	if n, err := s.PruneSubscribers(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}
