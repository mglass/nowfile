# nowfile

A personal status page that updates itself, served over finger and the web. Open source, public good, no revenue. Public server at nowfile.org; the same binary self-hosts. `CLAUDE.md` is a symlink to this file.

Read first:

1. `docs/brief.md`: what this is and is not. Use its names and phases. Do not add features it does not describe without a decision in `coordination/decisions.md`.
2. `docs/format.md`: the document format. The format is the product.
3. `STATUS.md`: where the project stands.
4. `~/Vault/preferences.md` before writing anything Mike reads.

## Stack

Go, standard library `net/http` with 1.22+ method patterns, `database/sql` + `modernc.org/sqlite`, server-rendered HTML with one hand-written `static/style.css` and minimal vanilla JS, assets embedded. No chi, no sqlx, no ORM, no Tailwind, no JS framework, no CDN. One binary serves finger on 79 and HTTP on a configurable port. This is Slipway's pattern, not the Glass Effect client scaffold.

Invariants:

- The plain-text document is the source of truth. HTML, JSON, and finger output are rendered from the same composed document. If they can disagree, the design is wrong.
- Expired slots are never emitted. Enforce in the composer, test it.
- Every writer goes through the same write API with a per-device token. The web UI is one client, not a privileged one.
- Timestamps are RFC 3339 everywhere.
- Nothing is published from a source the user has not switched on and previewed.

## Commands

```sh
go test ./...
gofmt -l . && go vet ./...
go run ./cmd/nowfile     # finger on :7979 and HTTP on :8080 in dev
```

## Rules

- Commit locally, push to main freely until CI deploys; once CI deploys on main, push only when Mike says.
- Deleting is a decision, not a side effect. No agent runs `rm -rf`, `rm -r`, `git clean`, `git reset --hard`, `git checkout -- .`, a force push, or anything that removes a directory on its own. Move it to `coordination/trash/` and report it.
- Scratch files and temp databases go in the agent's scratchpad, never under the workspace.
- No secrets in the repo. Public server config lives on the box.

## Status reporting

Keep `STATUS.md` current. After every meaningful change: rewrite "Where things stand" in place, add a dated entry with the commit hash to "Recent", keep the last 8 entries. Write what git cannot: why, what is broken, what is next, what is parked. Mike reads this through his vault.

## How work gets done: coordinator and subagents

This repo runs inside Herdr. A coordinating agent (Fable 5.1, low effort) talks to Mike, records conversations in `coordination/sessions/`, appends decisions to `coordination/decisions.md`, keeps `coordination/tasks.md` current, and rewrites `STATUS.md`. It delegates code to subagents in their own Herdr panes with `scripts/agent.sh`. In-process subagents leave no session and are not used for real work.

```
scripts/agent.sh <name> <model> <effort> "<prompt>" [--cwd <dir>] [--tab | --direction right|down] [--wait] [--advisor none|fable|opus|sonnet]
```

| Work | Model | Effort | Advisor |
|---|---|---|---|
| Coordination, product, specs, reviews of product work | Fable 5.1 (`fable`) | low | none |
| Copy, CSS, renames, config edits | latest Sonnet (`sonnet`) | medium | opus |
| Logic fixes, features, tests, infrastructure | Opus 5.5 (`opus`) | medium | fable |
| Stuck debugging, architecture, migrations, security | Opus 5.5 (`opus`) | xhigh | fable |

Long-running work (a slice, a deploy, anything over a few minutes) gets `--tab`, a new tab in this workspace. Split panes are for short tasks only (Mike, 2026-10-20). Small fixes lane: split by surface, run in parallel, verify once per batch, no brief or report. Fable above low or Opus above xhigh needs Mike's approval first.
