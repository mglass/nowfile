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
