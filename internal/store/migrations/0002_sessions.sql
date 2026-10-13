-- Web sign-in sessions. The cookie carries the plaintext; only its hash is
-- stored, like tokens and magic links.

CREATE TABLE sessions (
	hash    TEXT    PRIMARY KEY, -- hex sha256 of the plaintext
	user_id INTEGER NOT NULL REFERENCES users(id),
	created TEXT    NOT NULL,
	expires TEXT    NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);
