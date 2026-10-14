-- Phase 2 slice B: slot history for RSS and email, and email subscribers.

-- changes is appended by WriteSlot and ClearSlot in the same transaction as
-- the slot write. A clear records an empty value. Rows older than 90 days
-- are pruned.
CREATE TABLE changes (
	id      INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	key     TEXT    NOT NULL, -- canonical slot key
	value   TEXT    NOT NULL, -- empty for a clear
	set_at  TEXT    NOT NULL
);
CREATE INDEX changes_user ON changes(user_id, id);
CREATE INDEX changes_set_at ON changes(set_at);

-- subscribers follow one user's page by email. token_hash is the hex
-- sha256 of the one-use confirmation secret, cleared once used.
-- unsub_token is the opaque secret in every mail's unsubscribe link; it
-- grants nothing but unsubscribing. last_change_id, last_headline, and
-- last_sent drive the notifier.
CREATE TABLE subscribers (
	id             INTEGER PRIMARY KEY,
	user_id        INTEGER NOT NULL REFERENCES users(id),
	email          TEXT    NOT NULL,
	confirmed      INTEGER NOT NULL DEFAULT 0,
	token_hash     TEXT,
	token_expires  TEXT,
	unsub_token    TEXT    NOT NULL UNIQUE,
	created        TEXT    NOT NULL,
	last_change_id INTEGER NOT NULL DEFAULT 0,
	last_headline  TEXT    NOT NULL DEFAULT '',
	last_sent      TEXT,
	UNIQUE (user_id, email)
);
CREATE INDEX subscribers_token ON subscribers(token_hash);
