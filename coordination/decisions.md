# Decisions

Append only. Date, decision, why, alternatives rejected.

- 2026-10-13: Name is `.plan`, domain dotplan.io. Why: the literal file finger served; names the format and the server, not one feature. Rejected: away.ms and other "away" names, because the product has outgrown the away message; dotplan.com/.net/.org are parked or abandoned and unrelated.
- 2026-10-13: Finger is the primary read protocol; HTTPS serves the identical document as fallback and for the web page. Why: the protocol is the point, and port 79 is blocked on some networks.
- 2026-10-13: Open source (MIT), public good, no monetization. Free public server at dotplan.io plus self-hosting with the same binary. Why: most people will not self-host; those who want to should be able to.
- 2026-10-13: Public by default. Buddies-only visibility arrives with the buddy list in phase 5.
- 2026-10-13: No location, frontmost app, or idle-time sources. Only what the user wants showing.
- 2026-10-13: Launch slots are Status, Calendar, Focus, Listening. Static section is name, headline, photo, links.
- 2026-10-13: Stack follows Slipway (Go stdlib, SQLite, server-rendered HTML), not the client scaffold.
- 2026-10-13: AI is limited to single-line rewrites and classifications on existing source data, approved per rule, off by default for self-hosters.
