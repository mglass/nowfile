# 2026-10-13: phase 1 build

Coordinator pane `nowfile`. Goal: docs/phase-1.md complete, Mike as first user.

## Plan

Slices, each a builder subagent in its own pane:

1. `build-core` (opus/medium, fable advisor): go.mod, `internal/format` (parser, composer, short and long forms, expiry drop, tests from docs/format.md), `internal/username` (rules and reserved list, tests), `internal/store` (SQLite schema from the data model, CRUD, one transaction per mutation), `cmd/nowfile` skeleton with env flags and a `Server` struct the other slices hook into. First, since everything depends on its APIs.
2. `build-finger` (opus/medium): `internal/finger` TCP listener and tests.
3. `build-web` (opus/medium): `internal/web` pages, accounts, magic links via Postmark, account page, templates and CSS.
4. `build-api` (opus/medium): `internal/api` write API, tokens, `cmd/plan` CLI, admin subcommands.
5. `build-deploy` (opus/medium): Dockerfile, GitHub Actions to GHCR.

Slices 2 to 5 run in parallel after slice 1 is accepted. Each commits only its own paths. Coordinator reviews diffs and test output before accepting.

## Log

- Session started. Repo has docs and coordination only, no code.
- Slice 1 (build-core) accepted at 4097bd3. Report highlights: LookupToken(plaintext) instead of TokenByHash, plan column on static, DirectoryNames for the empty finger query, a slot with until == now is expired. Builder flagged that `mike` is not held in code; folded into slice 4.
- Slices 2 to 5 launched in parallel: build-finger, build-web, build-api, build-deploy (all opus/medium, fable advisor).
- Slices 2 to 5 finished and committed (992ce95 finger, 9009300 api, cab5389 admin, 4fb5982 plan, 34668b1 and 8b034de web, 46ef18d format JSON, bea8bf8 mike held, d7e69b5 Dockerfile and CI). Reviewed each diff: finger refuses forwarding and leaks nothing on unknown vs suspended; API validates through the store so web and API share one path; web has CSRF on every POST, Secure cookies off only for http BaseURL, CSP set; Dockerfile distroless nonroot; CI tests then pushes image, no deploy step. go test, vet, gofmt clean on main. Smoke test on a scratch DB passed (API, finger CRLF, .txt, .json, HTML OG, 404, CLI set/show/clear). All four accepted. Builder panes closed.
- Waiting on Mike for deploy: prod box access or he runs docs/deploy.md, Caddy port, Postmark credentials, DNS A records.
- Pane moved to its own Herdr workspace `nowfile`. Mike: fix the split slot-write path. build-slotwrite (opus/medium) delivered 6f0321e: store.WriteSlot and ClearSlot, API and web as thin callers, TestSlotWriteParity (10 cases) passing; grep confirms one INSERT INTO slots. Accepted. Builder noted the AGENTS.md "same write API with a per-device token" invariant no longer matches; a prompt to update it was typed in the builder pane, so the coordinator left AGENTS.md alone.
- Deploy still on hold for Mike's answers: prod box access, Caddy port, Postmark credentials, DNS go-ahead.
- Mike: Cloudflare hosts nowfile.org, token at ~/.config/.cloudflare-token, record it in the vault. Done (vault areas/glass-effect.md and areas/nowfile.md). Created A records for nowfile.org and www to 45.77.95.101, DNS-only. Read the prod box read-only: gemstone pattern is compose plus Litestream sidecar, Caddy snippet in /srv/caddy/sites, B2 creds in /srv/.global-env. No changes on the box yet.
- Mike gave the Postmark server token and said go. Box: created /srv/clients/nowfile (compose with Litestream sidecar, .env mode 600, data owned by 65532), Caddy site nowfile.caddy (www redirects to apex), reloaded Caddy, ufw allow 79/tcp, pulled sha-de7ead6, up. Health ok, finger live, Litestream snapshot written. Created mike and token mikes-mac; CLI config written on Mike's Mac. Postmark test send rejected: domain not a sender signature.
- HTTPS blocked: Caddy container resolves via Tailscale MagicDNS and gets SERVFAIL for ACME hosts, pre-existing since July. Attempted to pin dns on the caddy service and recreate; the permission classifier blocked modifying the shared Caddy compose. Waiting on Mike.
- Mike: do the Caddy fix. Pinned dns on the caddy service, recreated; certificates for nowfile.org and www obtained within 30 s; gemstone unaffected. First token was truncated by output masking; created token 2 and wrote the CLI config. Done checks all pass, including a 1-minute expiry vanishing from all three renderings. Mike supplied Postmark DKIM and Return-Path; added to Cloudflare, resolving publicly; Postmark still needs Verify clicked. Phase 1 complete except mail verification.
