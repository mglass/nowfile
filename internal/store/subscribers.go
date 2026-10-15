package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Subscriber is one email address following one user's page. Readers have
// no account; the confirmation secret and the unsubscribe secret are their
// only credentials.
type Subscriber struct {
	ID        int64
	UserID    int64
	Email     string
	Confirmed bool
	Created   time.Time
	// Unsub is the secret in every mail's unsubscribe link. Unlike the
	// other secrets in this store it is kept readable, because every
	// notification mail has to carry the same link again; it grants
	// nothing but removing this one row.
	Unsub string
	// LastChangeID is the newest change the subscriber has been mailed,
	// or the newest at confirmation, so nothing from before is mailed.
	LastChangeID int64
	// LastDigest is the digest time the subscriber was last dealt with,
	// whether a mail went out or there was nothing to say. Zero if never.
	LastDigest time.Time
}

// ErrAlreadySubscribed is returned by PendSubscriber when the address is
// already a confirmed subscriber of the page.
var ErrAlreadySubscribed = errors.New("already subscribed")

// PendSubscriber records an unconfirmed subscription of email to userID's
// page whose confirmation secret hashes to confirmHash and is valid until
// expires. If the address already has a pending subscription to the page,
// its confirmation secret is replaced, so only the newest mail's link
// works. unsub is used only for a new row. A confirmed subscription is left
// alone and returns ErrAlreadySubscribed. The email is normalised like an
// account email; a bad one wraps ErrInvalidEmail.
func (s *Store) PendSubscriber(ctx context.Context, userID int64, email, confirmHash string, expires time.Time, unsub string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	now := s.now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		var confirmed bool
		err := tx.QueryRowContext(ctx, `SELECT confirmed FROM subscribers WHERE user_id = ? AND email = ?`, userID, email).Scan(&confirmed)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO subscribers (user_id, email, token_hash, token_expires, unsub_token, created)
				VALUES (?, ?, ?, ?, ?, ?)`, userID, email, confirmHash, fmtTime(expires), unsub, fmtTime(now))
			return err
		case err != nil:
			return err
		case confirmed:
			return ErrAlreadySubscribed
		}
		_, err = tx.ExecContext(ctx, `UPDATE subscribers SET token_hash = ?, token_expires = ? WHERE user_id = ? AND email = ?`,
			confirmHash, fmtTime(expires), userID, email)
		return err
	})
}

// ConfirmSubscriber confirms the pending subscription whose confirmation
// secret is token. It works once and only before the secret expires;
// otherwise it returns ErrLinkInvalid. The subscriber starts from the
// page's current history, so nothing already said is mailed.
func (s *Store) ConfirmSubscriber(ctx context.Context, token string) (Subscriber, error) {
	now := s.now()
	var id int64
	err := s.db.QueryRowContext(ctx, `UPDATE subscribers SET confirmed = 1, token_hash = NULL, token_expires = NULL,
			last_change_id = (SELECT COALESCE(MAX(c.id), 0) FROM changes c WHERE c.user_id = subscribers.user_id)
		WHERE token_hash = ? AND confirmed = 0 AND token_expires > ?
		  AND user_id IN (SELECT id FROM users WHERE deleted_at IS NULL)
		RETURNING id`, HashToken(token), fmtTime(now)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscriber{}, ErrLinkInvalid
	}
	if err != nil {
		return Subscriber{}, err
	}
	return s.subscriber(ctx, `id = ?`, id)
}

// SubscriberByUnsub returns the subscription whose unsubscribe secret is
// token, or ErrNotFound.
func (s *Store) SubscriberByUnsub(ctx context.Context, token string) (Subscriber, error) {
	if token == "" {
		return Subscriber{}, ErrNotFound
	}
	return s.subscriber(ctx, `unsub_token = ?`, token)
}

// Unsubscribe deletes the subscription whose unsubscribe secret is token.
// Nothing about the address is kept. It returns ErrNotFound if there is no
// such subscription, which includes one already removed.
func (s *Store) Unsubscribe(ctx context.Context, token string) error {
	if token == "" {
		return ErrNotFound
	}
	return s.exec(ctx, `DELETE FROM subscribers WHERE unsub_token = ?`, token)
}

// CountSubscribers returns how many confirmed subscribers follow the page.
func (s *Store) CountSubscribers(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscribers WHERE user_id = ? AND confirmed = 1`, userID).Scan(&n)
	return n, err
}

// DigestPage is a live, unsuspended account with confirmed subscribers,
// and its time zone name ("" if not set).
type DigestPage struct {
	UserID   int64
	TimeZone string
}

// DigestPages returns every account the digest notifier has to look at.
func (s *Store) DigestPages(ctx context.Context) ([]DigestPage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, COALESCE(st.tz, '') FROM users u
		LEFT JOIN static st ON st.user_id = u.id
		WHERE u.deleted_at IS NULL AND u.suspended = 0
		  AND EXISTS (SELECT 1 FROM subscribers s WHERE s.user_id = u.id AND s.confirmed = 1)
		ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DigestPage
	for rows.Next() {
		var p DigestPage
		if err := rows.Scan(&p.UserID, &p.TimeZone); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DigestDue returns the user's confirmed subscribers not yet dealt with for
// the digest at at: LastDigest before at, or never.
func (s *Store) DigestDue(ctx context.Context, userID int64, at time.Time) ([]Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+subscriberCols+` FROM subscribers s
		WHERE s.user_id = ? AND s.confirmed = 1 AND (s.last_digest_at IS NULL OR s.last_digest_at < ?)
		ORDER BY s.id`, userID, fmtTime(at))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		sub, err := scanSubscriber(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// MarkDigested records that the subscriber was dealt with for the digest
// at at, so DigestDue leaves them out until the next one, and was mailed
// every change up to changeID (0 when nothing was mailed).
func (s *Store) MarkDigested(ctx context.Context, id int64, at time.Time, changeID int64) error {
	return s.exec(ctx, `UPDATE subscribers SET last_digest_at = ?, last_change_id = MAX(last_change_id, ?) WHERE id = ?`,
		fmtTime(at), changeID, id)
}

// PruneSubscribers deletes pending subscriptions whose confirmation link
// has expired and returns how many went.
func (s *Store) PruneSubscribers(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subscribers WHERE confirmed = 0 AND (token_expires IS NULL OR token_expires <= ?)`, fmtTime(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const subscriberCols = `s.id, s.user_id, s.email, s.confirmed, s.created, s.unsub_token, s.last_change_id, s.last_digest_at`

func (s *Store) subscriber(ctx context.Context, where string, arg any) (Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+subscriberCols+` FROM subscribers s WHERE s.`+where, arg)
	if err != nil {
		return Subscriber{}, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		sub, err := scanSubscriber(rows.Scan)
		if err != nil {
			return Subscriber{}, err
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return Subscriber{}, err
	}
	if len(out) == 0 {
		return Subscriber{}, ErrNotFound
	}
	return out[0], nil
}

// scanSubscriber scans subscriberCols.
func scanSubscriber(scan func(...any) error) (Subscriber, error) {
	var sub Subscriber
	var created, lastDigest sql.NullString
	if err := scan(&sub.ID, &sub.UserID, &sub.Email, &sub.Confirmed, &created, &sub.Unsub, &sub.LastChangeID, &lastDigest); err != nil {
		return Subscriber{}, err
	}
	var err error
	if sub.Created, err = parseTime(created); err != nil {
		return Subscriber{}, err
	}
	if sub.LastDigest, err = parseTime(lastDigest); err != nil {
		return Subscriber{}, err
	}
	return sub, nil
}
