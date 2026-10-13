# Phase 1: format, server, first user

Goal: nowfile.org serves Mike's page over finger and the web, with accounts, the typed Status, per-device tokens, and a CLI. No sources yet beyond the typed line. Read `docs/brief.md`, `docs/format.md` (draft 1), `docs/usernames.md`, and `AGENTS.md` first.

## Deliverables

1. `cmd/nowfile`: one binary. Flags or env: `NOWFILE_HTTP_ADDR` (default `:8080`), `NOWFILE_FINGER_ADDR` (default `:79`, dev `:7979`), `NOWFILE_DB_PATH` (default `/data/app.db`), `NOWFILE_BASE_URL`, `NOWFILE_POSTMARK_TOKEN` and `NOWFILE_MAIL_FROM` for sign-in mail, `NOWFILE_ADMIN_TOKEN`.
2. Composer: builds the document for a user from the static section and live slots, drops expired slots, renders short and long forms. Pure function over a struct, table-tested against the examples in `docs/format.md`.
3. Finger listener on TCP: reads one line, handles `name`, `/W name`, and empty query, answers in the document format, closes. Timeouts on read and write, max line length, no forwarding, no leaking of unknown-user versus suspended-user beyond a single generic line. Dev port 7979.
4. HTTP: `GET /{name}` HTML page with Open Graph tags carrying the headline line and photo; `GET /{name}.txt` the document as `text/plain; charset=utf-8`; `GET /{name}.json` the structured form; `GET /` a landing page explaining the project with `finger mike@nowfile.org` as the example; `GET /health`.
5. Accounts: sign-up with username and email, magic-link sign-in by email through Postmark, session cookie. Username rules from `docs/usernames.md`, with the reserved list enforced in code and tested.
6. Account page: edit the static section (Name, Headline, Photo URL, up to five Links), set Status with optional expiry (presets: 30 min, 1 hour, end of day, custom), clear Status, see the composed document as it will be served, manage tokens, opt in or out of the public directory, delete account.
7. Write API, bearer token per device: `PUT /api/v1/slots/{key}` with `{"value": "...", "until": "...", "source": "..."}`, `DELETE /api/v1/slots/{key}`, `GET /api/v1/me` returning the composed document as JSON. Keys limited to the ones in `docs/format.md`. 422 on validation errors. Tokens are created on the account page, shown once, stored hashed, named, revocable.
8. CLI `cmd/plan`: `plan "lunch, back at 1"`, `plan --until 13:00 "..."`, `plan --clear`, `plan show [name@host]` which speaks finger with HTTPS fallback. Config in `~/.config/nowfile/config` holding base URL and token.
9. Admin: `nowfile admin suspend <name>`, `nowfile admin release <name>`, run inside the container.
10. Dockerfile (multi-stage, distroless or alpine), GitHub Actions building `ghcr.io/mglass/nowfile:latest` and `sha-*` on push to main, tests and vet first.

## Data model

`users` (id, name unique, email unique, created, last_seen, directory bool, suspended bool, deleted_at), `static` (user_id, name, headline, photo, links as JSON), `slots` (user_id, key, value, set_at, until, source; primary key user_id+key), `tokens` (id, user_id, label, hash, created, last_used), `magic_links` (token hash, user_id, expires, used), `released_names` (name, available_after). Timestamps RFC 3339 UTC. All mutations in one transaction.

## Deployment

Runs on the Glass Effect production box `glasseffect-prod-1` as a client-style container with the slug `nowfile`, data at `/srv/clients/nowfile/data`, Litestream backup like any client. Caddy fronts `nowfile.org` and `www.nowfile.org` for HTTPS. Finger is not behind Caddy: the container publishes 79 on the host and the firewall allows 79/tcp. Mike points the `nowfile.org` A record and `www` at the prod box's public IP. The coordinator confirms with Mike before touching the prod box and before the DNS change.

## Done means

- `finger mike@nowfile.org` from a Mac returns Mike's document.
- `https://nowfile.org/mike` renders it, and a pasted link previews the Status line.
- `plan "testing"` from Mike's Mac changes both within a second.
- Expired Status disappears from all three without a restart.
- `go test ./...` covers the composer, the parser rules a client needs, username validation, the finger handler, and the API.
