-- Phase 1 schema. Timestamps are RFC 3339 UTC text at second precision.

CREATE TABLE users (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	email      TEXT    NOT NULL,
	created    TEXT    NOT NULL,
	last_seen  TEXT,
	directory  INTEGER NOT NULL DEFAULT 0,
	suspended  INTEGER NOT NULL DEFAULT 0,
	deleted_at TEXT
);
-- Unique among live accounts only, so a released name or a deleted
-- account's email can be used again.
CREATE UNIQUE INDEX users_name ON users(name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_email ON users(email) WHERE deleted_at IS NULL;

CREATE TABLE static (
	user_id  INTEGER PRIMARY KEY REFERENCES users(id),
	name     TEXT NOT NULL DEFAULT '',
	headline TEXT NOT NULL DEFAULT '',
	photo    TEXT NOT NULL DEFAULT '',
	links    TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
	plan     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE slots (
	user_id INTEGER NOT NULL REFERENCES users(id),
	key     TEXT    NOT NULL, -- canonical casing, one of format.KnownSlotKeys
	value   TEXT    NOT NULL,
	set_at  TEXT    NOT NULL,
	until   TEXT,             -- NULL means no expiry
	source  TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, key)
);

CREATE TABLE tokens (
	id        INTEGER PRIMARY KEY,
	user_id   INTEGER NOT NULL REFERENCES users(id),
	label     TEXT    NOT NULL,
	hash      TEXT    NOT NULL UNIQUE, -- hex sha256 of the plaintext
	created   TEXT    NOT NULL,
	last_used TEXT
);
CREATE INDEX tokens_user ON tokens(user_id);

CREATE TABLE magic_links (
	hash    TEXT    PRIMARY KEY, -- hex sha256 of the plaintext
	user_id INTEGER NOT NULL REFERENCES users(id),
	expires TEXT    NOT NULL,
	used    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE released_names (
	name            TEXT PRIMARY KEY,
	available_after TEXT NOT NULL
);
