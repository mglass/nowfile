# Status

## Where things stand

Phase 2 code is on main and tagged v0.1.0 (9be3db5). Three slices accepted on 2026-10-14: the calendar source (ICS fetch with ETag, recurrence expansion incl. monthly and RDATE, busy-span merge, preview before enable, runner writing Calendar through WriteSlot and never the subject, private-network fetches refused), subscriptions (changes history, RSS at /{name}.rss, email double opt-in with per-IP and per-address limits, one-click unsubscribe with List-Unsubscribe, 15-minute collapse, /directory with `directory` reserved), and releases (tag-triggered workflow building plan for darwin arm64 and amd64 with SHA256SUMS, `plan --version`, docs/install.md, docs/shortcuts.md). Tests, vet, gofmt clean; local smoke of RSS, directory, subscribe form, API passed.

Prod runs sha-9be3db5 as of 2026-10-15 00:47 UTC; health, RSS, and directory answer over HTTPS and finger is up. The v0.1.0 release on GitHub carries plan-darwin-arm64, plan-darwin-amd64, and SHA256SUMS; the arm64 asset was downloaded, checksum verified, and prints v0.1.0. Email subscriptions changed on 2026-10-15 to a daily digest at 08:00 in the page owner's time zone (2bebcf8), listing the previous 24 hours of Status and Calendar changes, no mail on quiet days; RSS stays per change. Two columns on subscribers (last_headline, last_sent) are now unused and left for a decision. Remaining phase 2 checks need Mike: his calendar ICS URL on the account page, and a subscriber receiving a digest the morning after a change.

Decided this phase: no Last.fm ever (Listening comes with the Mac agent); plan ships on GitHub Releases for Mac first; mail from nowfile@nowfile.org; CalDAV with auth rejected in this phase. Builder choices worth knowing: the runner evaluates stored spans every minute but fetches every 5; it only clears a Calendar slot it wrote itself; NOWFILE_FETCH_PRIVATE=1 is dev-only and must stay off on nowfile.org; tzdata is embedded in the binary.

Parked: a "N events could not be read" note on the calendar preview; signing and notarizing plan; Linux and Windows builds; inactivity pruning; AGENTS.md invariant wording.

## Recent

- 2026-10-15 (2bebcf8): email subscriptions are a daily digest; prod on sha-1c49a26
- 2026-10-15: prod on sha-9be3db5, v0.1.0 release assets verified
- 2026-10-14 (9be3db5): phase 2 slices accepted, tagged v0.1.0
- 2026-10-13: Postmark verified, repo public, phase 1 complete
- 2026-10-13: Caddy DNS fixed, HTTPS live, all phase 1 done checks pass; Postmark DNS records added, verification pending
- 2026-10-13: deployed to prod; finger live at nowfile.org; HTTPS blocked on Caddy container DNS, Postmark blocked on domain verification
- 2026-10-13 (6f0321e): one slot-write path for API and account page, parity test
- 2026-10-13 (8b034de): slices 2 to 5 accepted: finger, web and accounts, API and CLI and admin, Dockerfile and CI; smoke tested
