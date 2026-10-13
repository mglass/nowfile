# Decisions

Append only. Date, decision, why, alternatives rejected.

- 2026-10-13: Name was dotplan, domain dotplan.io. Why: the literal file finger served. Rejected: away.ms and other "away" names, because the product has outgrown the away message.
- 2026-10-13: Finger is the primary read protocol; HTTPS serves the identical document as fallback and for the web page. Why: the protocol is the point, and port 79 is blocked on some networks.
- 2026-10-13: Open source (MIT), public good, no monetization. Free public server at nowfile.org plus self-hosting with the same binary. Why: most people will not self-host; those who want to should be able to.
- 2026-10-13: Public by default. Buddies-only visibility arrives with the buddy list in phase 5.
- 2026-10-13: No location, frontmost app, or idle-time sources. Only what the user wants showing.
- 2026-10-13: Launch slots are Status, Calendar, Focus, Listening. Static section is name, headline, photo, links.
- 2026-10-13: Stack follows Slipway (Go stdlib, SQLite, server-rendered HTML), not the client scaffold.
- 2026-10-13: AI is limited to single-line rewrites and classifications on existing source data, approved per rule, off by default for self-hosters.
- 2026-10-13 (correction): dotplan.io is registered since 2020 and hosts a live product, "dotplan, daily team check-ins". The earlier availability check used the rdap.org proxy, which has no .io registry and answered 404. Name reopened.
- 2026-10-13: Name is nowfile, domain nowfile.org, with .dev and .app to register as redirects. Why: it says what it is, a file that is current, and the name is unused everywhere except a marketplace listing on .com. Rejected: upto.dev (better address, crowded neighborhood with a live calendar product on .com), thesedays.io, fingerplan.io, plandot.io.
- 2026-10-13: Version line is `nowfile/0`, not `.plan/0`. Why: the name changed.
- 2026-10-13: The Plan block is plain text everywhere, web included. Why: anything rendered as styling on the web would show as markup over finger. Rejected: markdown on the web.
- 2026-10-13: Photo stays in the format as a URL to an image hosted elsewhere; the web page renders it, finger shows the link. nowfile.org does not host user images in version 1. Why: hosting arbitrary images for the public carries abuse and moderation exposure. Revisit with re-encoded avatars (decode, resize, strip metadata, size cap, report path) or Libravatar/Gravatar lookup if link rot becomes a problem.
- 2026-10-13: Public server runs on the Glass Effect production box for now, as a client-style container, with finger published directly on 79 outside Caddy. Why: Mike's call; it exists and is backed up. Rejected for now: a dedicated VM.
- 2026-10-13: Username policy as in docs/usernames.md (2 to 32 lowercase ASCII letters, digits, hyphen; reserved list equals site routes plus role names; permanent, released 90 days after deletion; 18-month inactivity pruning). Devised by the agent at Mike's request.
- 2026-10-13: The container runs as nonroot with finger on 7979 inside; the host publishes 79 to it. Why: distroless nonroot cannot bind ports under 1024, and running as root to bind 79 is worse than a port map.
- 2026-10-13: Web sessions are server-side rows in a `sessions` table (30 days), not signed cookies. Why: sign-out and account deletion can revoke them at once.
- 2026-10-13: `mike` is on the reserved list; reserved names are issued only through `nowfile admin create-user`. Why: holds the name against squatting before launch without a special case in signup.
- 2026-10-13: Signing up with an email that already has an account sends that account a sign-in link and shows the generic "check your email" page. Why: the form must not reveal which addresses have accounts.
- 2026-10-13: No inactivity pruning job in phase 1. Why: docs/phase-1.md does not list it; it belongs with the first real users.
- 2026-10-13: The write API and the account page share one slot-write function (store.WriteSlot and ClearSlot) that owns validation, expiry rules, and the transaction. Transport can differ, the code path cannot. Why: Mike's call after the account page was found calling the store with its own validation. Side effect accepted: values are trimmed on both paths.
- 2026-10-13: Caddy on the prod box pins DNS resolvers (108.61.10.10, 1.1.1.1) in its compose file. Why: Docker forwarded the container's DNS to Tailscale MagicDNS, which SERVFAILs public names, so no ACME issuer was reachable.
- 2026-10-13: www.nowfile.org redirects permanently to nowfile.org rather than serving the site twice. Why: one canonical URL for link previews and finger hints.
