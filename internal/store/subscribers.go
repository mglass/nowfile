package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/format"
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
	// LastChangeID is the newest change the notifier has dealt with,
	// LastHeadline the headline last mailed (or current at confirmation),
	// and LastSent when the last notification went out (zero if never).
	LastChangeID int64
	LastHeadline string
	LastSent     time.Time
	// PendingChangeID is the newest change to a watched key, filled only by
	// DueSubscribers.
	PendingChangeID int64
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
// page's current history and headline, so nothing already said is mailed.
func (s *Store) ConfirmSubscriber(ctx context.Context, token string) (Subscriber, error) {
	now := s.now()
	var id, userID int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE subscribers SET confirmed = 1, token_hash = NULL, token_expires = NULL,
				last_change_id = (SELECT COALESCE(MAX(c.id), 0) FROM changes c WHERE c.user_id = subscribers.user_id)
			WHERE token_hash = ? AND confirmed = 0 AND token_expires > ?
			  AND user_id IN (SELECT id FROM users WHERE deleted_at IS NULL)
			RETURNING id, user_id`, HashToken(token), fmtTime(now)).Scan(&id, &userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLinkInvalid
		}
		return err
	})
	if err != nil {
		return Subscriber{}, err
	}
	var headline string
	if doc, err := s.Document(ctx, userID, s.Now()); err == nil {
		headline = format.Headline(doc)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE subscribers SET last_headline = ? WHERE id = ?`, headline, id); err != nil {
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

// DueSubscribers returns the confirmed subscribers of live, unsuspended
// accounts that have a change to one of keys newer than their
// LastChangeID and were last mailed before sentBefore (or never), with
// PendingChangeID set to the newest such change.
func (s *Store) DueSubscribers(ctx context.Context, keys []string, sentBefore time.Time) ([]Subscriber, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	in := "(?" + strings.Repeat(", ?", len(keys)-1) + ")"
	var args []any
	for _, k := range keys {
		args = append(args, k)
	}
	args = append(args, fmtTime(sentBefore))
	rows, err := s.db.QueryContext(ctx, `SELECT `+subscriberCols+`, p.pending FROM subscribers s
		JOIN users u ON u.id = s.user_id
		JOIN (SELECT user_id, MAX(id) AS pending FROM changes WHERE key IN `+in+` GROUP BY user_id) p ON p.user_id = s.user_id
		WHERE s.confirmed = 1 AND u.deleted_at IS NULL AND u.suspended = 0
		  AND p.pending > s.last_change_id
		  AND (s.last_sent IS NULL OR s.last_sent <= ?)
		ORDER BY s.id`, args...)
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

// MarkNotified records that the notifier dealt with every change up to
// changeID for the subscriber and that headline is what they last saw. A
// non-zero sent also records a mail going out.
func (s *Store) MarkNotified(ctx context.Context, id, changeID int64, headline string, sent time.Time) error {
	if sent.IsZero() {
		return s.exec(ctx, `UPDATE subscribers SET last_change_id = ?, last_headline = ? WHERE id = ?`, changeID, headline, id)
	}
	return s.exec(ctx, `UPDATE subscribers SET last_change_id = ?, last_headline = ?, last_sent = ? WHERE id = ?`,
		changeID, headline, fmtTime(sent), id)
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

const subscriberCols = `s.id, s.user_id, s.email, s.confirmed, s.created, s.unsub_token, s.last_change_id, s.last_headline, s.last_sent`

func (s *Store) subscriber(ctx context.Context, where string, arg any) (Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+subscriberCols+`, 0 FROM subscribers s WHERE s.`+where, arg)
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

// scanSubscriber scans subscriberCols plus a pending change id.
func scanSubscriber(scan func(...any) error) (Subscriber, error) {
	var sub Subscriber
	var created, lastSent sql.NullString
	if err := scan(&sub.ID, &sub.UserID, &sub.Email, &sub.Confirmed, &created, &sub.Unsub,
		&sub.LastChangeID, &sub.LastHeadline, &lastSent, &sub.PendingChangeID); err != nil {
		return Subscriber{}, err
	}
	var err error
	if sub.Created, err = parseTime(created); err != nil {
		return Subscriber{}, err
	}
	if sub.LastSent, err = parseTime(lastSent); err != nil {
		return Subscriber{}, err
	}
	return sub, nil
}
