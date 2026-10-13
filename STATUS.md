# Status

## Where things stand

Deployed to glasseffect-prod-1 on 2026-10-13. The nowfile container (image sha-de7ead6) and its Litestream sidecar run from /srv/clients/nowfile following the gemstone pattern; Litestream is replicating to B2 under nowfile/. Host port 79 maps to the container's 7979, ufw allows 79/tcp, and `finger mike@nowfile.org` answers from the public internet. Mike's account exists (created by admin command) with one token, and his `plan` CLI config is written at ~/.config/nowfile/config. Cloudflare hosts nowfile.org with A records for apex and www to 45.77.95.101, DNS-only.

Broken: HTTPS. Caddy has the nowfile.org site but cannot obtain a certificate because the Caddy container's DNS forwards to Tailscale MagicDNS, which SERVFAILs public names (624 such errors in the last day, since July; gemstone's renewals will hit it too). Fix is to pin resolvers (108.61.10.10, 1.1.1.1) on the caddy service in /srv/caddy/compose.yml and recreate the container. The coordinator was blocked from changing that shared file and is waiting on Mike. Until then plan show works over finger; `plan "..."` and the web page need HTTPS.

Also open: Postmark rejects From nowfile@nowfile.org until the domain is verified in Postmark (account-level, needs DKIM and Return-Path records, which the coordinator can add to Cloudflare once given). Sign-in mail is dead until then; admin create-user is the workaround. GHCR package visibility: the box pulled the image, so no blocker. Phase 1 "done means" remaining: HTTPS page and link preview, `plan "testing"` end to end, expiry disappearing live.

## Recent

- 2026-10-13: deployed to prod; finger live at nowfile.org; HTTPS blocked on Caddy container DNS, Postmark blocked on domain verification

- 2026-10-13 (6f0321e): one slot-write path for API and account page, parity test

- 2026-10-13 (8b034de): slices 2 to 5 accepted: finger, web and accounts, API and CLI and admin, Dockerfile and CI; smoke tested
- 2026-10-13 (4097bd3): slice 1 accepted: format, username, store, config, entrypoint
- 2026-10-13 (7a3eb99): phase 1 session opened, work split into five slices
- 2026-10-13: format draft 1, phase 1 spec, username policy; build started
- 2026-10-13: renamed from dotplan to nowfile; repo mglass/nowfile
- 2026-10-13: repo created with brief, format draft 0, AGENTS.md, coordinator layout
