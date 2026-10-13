# Status

## Where things stand

Phase 1 is live. nowfile.org serves Mike's page over finger and HTTPS from glasseffect-prod-1 (image sha-de7ead6, Litestream to B2 under nowfile/). All "done means" checks in docs/phase-1.md passed on 2026-10-13: `finger mike@nowfile.org` from a Mac, `https://nowfile.org/mike` with the Status in og:description, `plan "testing"` from Mike's Mac changing finger and HTTPS within a second, and an expired Status disappearing from finger, .txt, and .json without a restart. www redirects to the apex. Mike's CLI config is at ~/.config/nowfile/config (token "mikes-mac", id 2; token id 1 was never usable and should be revoked from the account page).

Fixed on the box along the way: the Caddy container resolved DNS through Tailscale MagicDNS and could not reach any ACME issuer (failing since July, would have broken gemstone's renewal). /srv/caddy/compose.yml now pins dns 108.61.10.10 and 1.1.1.1; backup at compose.yml.bak-20261013.

Open: Postmark. The DKIM TXT and pm-bounces CNAME are in Cloudflare and resolve publicly, but Postmark still rejects From nowfile@nowfile.org until Mike clicks Verify on the domain in Postmark. Until then no one but Mike can sign in; admin create-user is the workaround. Next: open the repo (brief says phase 1 ends with that), then phase 2 sources.

Parked: inactivity pruning job (docs/usernames.md); AGENTS.md invariant wording about "same write API" should become "one slot-write code path".

## Recent

- 2026-10-13: Caddy DNS fixed, HTTPS live, all phase 1 done checks pass; Postmark DNS records added, verification pending
- 2026-10-13: deployed to prod; finger live at nowfile.org; HTTPS blocked on Caddy container DNS, Postmark blocked on domain verification
- 2026-10-13 (6f0321e): one slot-write path for API and account page, parity test
- 2026-10-13 (8b034de): slices 2 to 5 accepted: finger, web and accounts, API and CLI and admin, Dockerfile and CI; smoke tested
- 2026-10-13 (4097bd3): slice 1 accepted: format, username, store, config, entrypoint
- 2026-10-13 (7a3eb99): phase 1 session opened, work split into five slices
- 2026-10-13: format draft 1, phase 1 spec, username policy; build started
- 2026-10-13: renamed from dotplan to nowfile; repo mglass/nowfile
