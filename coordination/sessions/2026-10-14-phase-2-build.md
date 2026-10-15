# 2026-10-14: phase 2 build

Mike's direction: start phase 2, no Last.fm (native apps read now-playing), `plan` on GitHub Releases for Mac first, mail from nowfile@nowfile.org. Spec written at docs/phase-2.md. Slices: A calendar source (opus/medium), B subscriptions and directory (opus/medium), C releases and docs (opus/medium for the workflow, docs included).

## Log

- Spec and decisions committed. Launching three builders in parallel.
- All three slices landed (17 commits, 51 files). Reviewed: runner writes only "Busy until ..." via WriteSlot and tests assert no SUMMARY text leaks; fetcher refuses userinfo and private addresses; subs has per-IP and per-address limits and List-Unsubscribe; release workflow runs tests then gh release create. Migrations 0003 (subs) and 0004 (sources) did not collide; store now refuses duplicate numbers. Smoke test passed. Accepted. Pushed main, tagged v0.1.0.
- Release and CI green. Prod rolled to sha-9be3db5; public checks pass. Release asset installed on Mike's Mac from GitHub, checksum OK, --version v0.1.0. Waiting on Mike for his ICS URL and a subscription test.
