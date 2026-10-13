# nowfile brief

Written 2026-10-13 from a conversation with Mike. This is the agreed shape of the project. Change it by decision, recorded in `coordination/decisions.md`, not by drift.

## What it is

A personal status page that updates itself, readable over the finger protocol and the web. The name is nowfile: a file that is current, in the spirit of the `.plan` file finger served in the 1990s. The public server lives at `nowfile.org`, so a person is `mike@nowfile.org` and `finger mike@nowfile.org` works from any terminal that still has a finger client, which includes every Mac.

It descends from three things: the AIM away message, the Unix `.plan` file, and the "now page" convention Derek Sivers started in 2015 (a page at `/now` saying what you are up to). Now pages rot because updating is manual. This one fills itself from sources the user has explicitly turned on, and every line on it carries an expiry so stale information disappears instead of lingering.

Open source, public good, no revenue model. Mike runs a free public server because most people will not self-host, and the same binary self-hosts for anyone who wants to.

## Principles

1. Only things the person wants showing. Every source is opt-in, shows a preview of the exact line it will publish, and can be switched off from the page itself.
2. Nothing stale. Every dynamic line has a freshness rule. A line past its expiry is not served.
3. Finger first. The plain-text document is the product. The web page, JSON, and apps are renderings of it.
4. One format, any server. The line format is published so a hand-written `.plan` on a classic fingerd works in the same buddy list as a hosted user.
5. Public by default. Buddies-only visibility comes with the buddy list, not before.
6. The app is a convenience, not a requirement. Reading needs a browser or a finger client. Writing needs anything that can make an HTTPS request.

## The document

One plain-text document per user, composed by the server. See `docs/format.md` for the exact format. In outline:

- A static section: name, headline, photo URL, links. The user writes it once. The photo is a link to an image hosted elsewhere; nowfile.org does not host images in version 1.
- Dynamic slots, each one line plus indented metadata (set time, expiry, source). Launch slots: `Status` (the typed line), `Calendar`, `Focus`, `Listening`.
- A `Plan:` block of free text at the end, for whatever the user wants to say at length.

The server applies priority (a typed Status beats Calendar beats Focus for the headline shown in previews), drops expired slots, and emits the result identically over finger, HTTPS as `text/plain`, and inside the HTML page.

## Reading

- `finger user@nowfile.org` on TCP 79. `finger @nowfile.org` lists public users (optional, per-user opt-in). `/W` returns the long form.
- `https://nowfile.org/user` is the HTML page with Open Graph tags so a pasted link previews the current headline. `https://nowfile.org/user.txt` is the finger document. `https://nowfile.org/user.json` is the same data structured.
- Readers can subscribe to changes without an account: email and RSS at launch, web push later. This is the buddy list for people without the app.
- Clients try finger first and fall back to HTTPS, because some carrier and corporate networks block port 79.

## Writing

Every writer uses the same HTTPS API with a per-device token and writes one slot at a time. Writers at launch:

- The web account page: edit the static section, type a Status with an optional expiry, manage sources and tokens.
- Server-side pulls, so a web-only user still self-updates: a subscribed calendar feed (ICS or CalDAV URL) for `Calendar`, Last.fm for `Listening`.
- A shell one-liner for terminal people, `plan "lunch, back at 1"`.
- Shortcuts on iOS and macOS calling the API, so Focus automations can set `Focus` with no app installed.

Phase two adds a Mac menu bar agent (typed line with a hotkey and expiry, Apple Music and Spotify now playing, EventKit calendar, Focus Filters) and an iOS app (widgets on Home, Lock Screen, and StandBy, a Live Activity for the current Status, App Intents, a Focus filter, a Messages extension that sends the current Status as a bubble with the link, buddy-change notifications).

Explicitly not sources, decided 2026-10-13: location, the frontmost app or document, idle time.

## AI

Small, invisible jobs, each a single-line rewrite or classification, never generation of content the sources do not contain, and never published before the user has approved the rule at least once:

1. Calendar privacy rewriter: "Dentist 2pm" becomes "Out until 3". Shows busy state, never the subject, unless the event is marked public. Learn mode at first, where the user approves each rewrite.
2. Expiry from the typed line: "lunch, back at 1" sets expiry 13:00.
3. Voice to one line: dictation condensed to a Status in the user's voice.
4. Suggested lines when the Status field opens, from time of day and the next calendar event.

Provider is pluggable and the feature is off by default for self-hosters.

## Stack

Go, standard library `net/http`, SQLite via `modernc.org/sqlite`, server-rendered HTML with one hand-written stylesheet and minimal JS, assets embedded. One binary serves finger on 79 and HTTP on a configurable port. One Docker image on GHCR. The pattern is Slipway's, not the client scaffold: no chi, no sqlx, no Tailwind.

Hosting for the public server: a small dedicated VM, separate from the Glass Effect client boxes, with Caddy for TLS and Litestream to B2 for the database. Decision pending on provider; Vultr by default.

## Phases

1. Format and server: `docs/format.md` final, finger listener, HTTP page and text and JSON, accounts with magic-link sign-in, static section, Status with expiry, per-device tokens, write API, CLI. Deploy at `nowfile.org` with Mike as the first user. Open the repo.
2. Sources and subscriptions: calendar feed pull, Last.fm pull, Shortcuts recipes for Focus, email and RSS subscriptions, user directory opt-in.
3. AI: expiry parsing, calendar rewriter with learn mode, suggestions.
4. Mac agent.
5. iOS app and buddy list, with buddies-only visibility.

## Non-goals

- Chat, comments, or any reader-to-author messaging.
- Tracking readers.
- Monetization of any kind.
- Compatibility with every historical finger quirk. Classic clients must read the output; the server does not emulate every old server.

## Open questions

- Hosting provider and region for the public server.
- Public user directory default (opt-in is the current answer).
- Username policy and reserved names.
