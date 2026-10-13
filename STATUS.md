# Status

## Where things stand

Phase 1 build is underway (docs/phase-1.md). Slice 1 is in: the format parser and composer (internal/format), username rules (internal/username), the SQLite store with the full phase 1 data model (internal/store), config from env (internal/config), and the server entrypoint with hook points. Expired slots are dropped in format.Compose and the store exposes no document path that skips it. Four builders are running in parallel: finger listener, web site and accounts, write API plus CLI plus admin, Dockerfile plus CI. Nothing is deployed. Nothing touches the prod box or DNS until Mike says.

Choices made in slice 1 that the spec left open: store methods take ctx; tokens are `nf_` plus 43 random chars and are looked up by plaintext via LookupToken; a `plan` column was added to `static`; deleting an account removes its slots, tokens, and static section, soft-deletes the user, frees the email at once, and holds the name 90 days; a slot whose until equals now is expired; the 200-byte line limit applies to Plan lines too. Dev DB defaults to data/app.db.

Open: `mike` is not yet held in code; slice 4 adds it to the reserved list with an admin bypass for create-user.

## Recent

- 2026-10-13 (4097bd3): slice 1 accepted: format, username, store, config, entrypoint; tests green
- 2026-10-13 (7a3eb99): phase 1 session opened, work split into five slices
- 2026-10-13: format draft 1, phase 1 spec, username policy; build started
- 2026-10-13: renamed from dotplan to nowfile; repo mglass/nowfile
- 2026-10-13: repo created with brief, format draft 0, AGENTS.md, coordinator layout
