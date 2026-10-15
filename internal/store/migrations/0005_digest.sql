-- Email subscriptions become a daily digest (decision 2026-10-15).
-- last_digest_at is the 08:00 local digest time the subscriber was last
-- dealt with, sent or skipped for having no changes; the notifier never
-- sends twice for one. last_change_id still keeps changes from before
-- confirmation out of the first digest; last_headline and last_sent are no
-- longer read.
ALTER TABLE subscribers ADD COLUMN last_digest_at TEXT;

-- Existing subscribers count as dealt with now, so a deploy after 08:00
-- does not mail everyone at once; changes since then go in the next digest.
UPDATE subscribers SET last_digest_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE confirmed = 1;
