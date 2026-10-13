# Status

## Where things stand

Phase 1 code is complete and on main. One binary serves finger and HTTP from SQLite: the format parser and composer, username rules with the reserved list (`mike` held, issued only by the admin command), the store, the finger listener, the public pages (HTML with Open Graph, .txt, .json from one document path), sign-up and magic-link sign-in through Postmark with a log fallback in dev, the account page (static section, Plan, Status with expiry presets, tokens, directory opt-in, delete), the write API with per-device bearer tokens, the `plan` CLI with finger-first `plan show`, admin subcommands (create-user, create-token, suspend, release), a distroless Dockerfile, and CI that tests then pushes `ghcr.io/mglass/nowfile` on main. Smoke tested end to end on a scratch database: API write, expired write rejected with 422, finger with CRLF, forwarding refused, .txt, .json, and HTML all agree, unknown user is a 404, CLI set, show, and clear.

Not done: deployment. Nothing has touched glasseffect-prod-1 or DNS. Next step needs Mike: access to the prod box (or Mike runs the commands in docs/deploy.md), the HTTP port to use behind Caddy (8091 is a placeholder), the Postmark token and from address, and the A records for nowfile.org and www pointed at the box. Then create `mike` with the admin command, set his static section, and run the "done means" checks in docs/phase-1.md.

Choices the builders made where the spec was open: finger listens on 7979 inside the container and the host maps 79 to it, because the image runs as nonroot; sessions live in a `sessions` table (migration 0002), 30 days, cookie `nf_session`; CSRF is a per-session derived token, or a double-submit cookie when signed out; a sign-up with an email that already has an account sends that account a sign-in link and shows the same "check your email" page; images are linux/amd64 only.

Open: whether the prod box's firewall already allows 79/tcp; the inactivity pruning job from docs/usernames.md is not built (nothing in phase 1 asked for it).

## Recent

- 2026-10-13 (8b034de): slices 2 to 5 accepted: finger, web and accounts, API and CLI and admin, Dockerfile and CI; smoke tested
- 2026-10-13 (4097bd3): slice 1 accepted: format, username, store, config, entrypoint
- 2026-10-13 (7a3eb99): phase 1 session opened, work split into five slices
- 2026-10-13: format draft 1, phase 1 spec, username policy; build started
- 2026-10-13: renamed from dotplan to nowfile; repo mglass/nowfile
- 2026-10-13: repo created with brief, format draft 0, AGENTS.md, coordinator layout
