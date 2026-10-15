package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ChangeRetention is how long the changes history keeps a row.
const ChangeRetention = 90 * 24 * time.Hour

// Change is one entry in a user's slot history: a slot taking a new value,
// or being cleared (Value empty). RSS and email subscriptions read it.
type Change struct {
	ID     int64
	UserID int64
	Key    string // canonical slot key
	Value  string // empty for a clear
	Set    time.Time
}

// recordChange appends a change for the slot write or clear about to happen
// in tx, if a reader would see a difference: value differs from what the
// slot shows now, where an expired or missing row shows nothing. So the
// calendar runner rewriting the same line every five minutes, or clearing a
// slot that is not set, adds nothing. It must run before the slot row is
// changed.
func recordChange(ctx context.Context, tx *sql.Tx, userID int64, key, value string, now time.Time) error {
	var prev string
	var until sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT value, until FROM slots WHERE user_id = ? AND key = ?`, userID, key).Scan(&prev, &until)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev = ""
	case err != nil:
		return err
	default:
		u, err := parseTime(until)
		if err != nil {
			return err
		}
		if !u.IsZero() && !u.After(now) {
			prev = ""
		}
	}
	if prev == value {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO changes (user_id, key, value, set_at) VALUES (?, ?, ?, ?)`,
		userID, key, value, fmtTime(now))
	return err
}

// Changes returns up to limit of the user's most recent changes to any of
// keys, newest first. Rows past ChangeRetention may still be returned until
// the next PruneChanges.
func (s *Store) Changes(ctx context.Context, userID int64, keys []string, limit int) ([]Change, error) {
	if len(keys) == 0 || limit <= 0 {
		return nil, nil
	}
	args := []any{userID}
	for _, k := range keys {
		args = append(args, k)
	}
	args = append(args, limit)
	return s.changes(ctx, `SELECT id, user_id, key, value, set_at FROM changes
		WHERE user_id = ? AND key IN (?`+strings.Repeat(", ?", len(keys)-1)+`)
		ORDER BY id DESC LIMIT ?`, args...)
}

// ChangesBetween returns the user's changes to any of keys set at or after
// from and before to, oldest first.
func (s *Store) ChangesBetween(ctx context.Context, userID int64, keys []string, from, to time.Time) ([]Change, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	args := []any{userID}
	for _, k := range keys {
		args = append(args, k)
	}
	args = append(args, fmtTime(from), fmtTime(to))
	return s.changes(ctx, `SELECT id, user_id, key, value, set_at FROM changes
		WHERE user_id = ? AND key IN (?`+strings.Repeat(", ?", len(keys)-1)+`)
		  AND set_at >= ? AND set_at < ?
		ORDER BY set_at, id`, args...)
}

func (s *Store) changes(ctx context.Context, query string, args ...any) ([]Change, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var set sql.NullString
		if err := rows.Scan(&c.ID, &c.UserID, &c.Key, &c.Value, &set); err != nil {
			return nil, err
		}
		if c.Set, err = parseTime(set); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneChanges deletes changes older than ChangeRetention and returns how
// many went.
func (s *Store) PruneChanges(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM changes WHERE set_at < ?`, fmtTime(s.now().Add(-ChangeRetention)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
