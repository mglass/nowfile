-- Phase 2 sources. One source per kind per user. config is JSON the
-- source kind owns (calendar: {"url": "..."}). state is JSON the runner owns:
-- conditional-GET validators, the failure streak, and the busy spans last
-- computed, never the fetched body or any event subject.

CREATE TABLE sources (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	kind       TEXT    NOT NULL,
	config     TEXT    NOT NULL DEFAULT '{}',
	enabled    INTEGER NOT NULL DEFAULT 0,
	last_run   TEXT,
	last_error TEXT    NOT NULL DEFAULT '',
	created    TEXT    NOT NULL,
	state      TEXT    NOT NULL DEFAULT '{}',
	UNIQUE (user_id, kind)
);
CREATE INDEX sources_kind_enabled ON sources(kind, enabled);

-- The user's IANA time zone, set from the browser on the account page.
-- Empty means not set; sources then use UTC.
ALTER TABLE static ADD COLUMN tz TEXT NOT NULL DEFAULT '';
