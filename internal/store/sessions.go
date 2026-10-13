package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateSession creates a web sign-in session for the account, valid until
// expires. The plaintext goes in the cookie; only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, expires time.Time) (string, error) {
	plain, err := randomSecret()
	if err != nil {
		return "", err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (hash, user_id, created, expires) VALUES (?, ?, ?, ?)`,
			HashToken(plain), userID, fmtTime(s.now()), fmtTime(expires))
		return err
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// SessionUser returns the account for a session plaintext. Unknown, expired,
// and deleted-account sessions return ErrNotFound; a suspended account
// returns ErrSuspended.
func (s *Store) SessionUser(ctx context.Context, plaintext string) (User, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE hash = ? AND expires > ?`,
		HashToken(plaintext), fmtTime(s.now())).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if u.Suspended {
		return User{}, ErrSuspended
	}
	return u, nil
}

// DeleteSession ends a session. Deleting an unknown session is not an error.
func (s *Store) DeleteSession(ctx context.Context, plaintext string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, HashToken(plaintext))
		return err
	})
}
