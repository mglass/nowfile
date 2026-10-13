# The .plan document format, draft 0

The document is UTF-8 plain text, lines ending in `\n`, no line longer than about 200 bytes. It is what a finger server returns for a user, what `https://host/user.txt` serves, and what a self-hoster writes by hand into a `.plan` file on a classic fingerd.

## Shape

```
.plan/0
Name: Mike Glass
Headline: Builds internal tools for small companies. Jacksonville, FL.
Photo: https://dotplan.io/mike/photo.jpg
Link: https://glasseffect.com
Link: https://github.com/mglass

Status: Lunch, back at 1
  set: 2026-10-13T12:04:10-04:00
  until: 2026-10-13T13:00:00-04:00
Calendar: Busy until 15:00
  until: 2026-10-13T15:00:00-04:00
  source: calendar
Focus: Work
  set: 2026-10-13T09:00:00-04:00
Listening: The War on Drugs, Harmonia's Dream
  set: 2026-10-13T12:31:40-04:00
  until: 2026-10-13T12:40:00-04:00
  source: lastfm

Plan:
Reply within a day. Text for anything urgent.
```

## Rules

- `.plan/0` on the first line marks the version. A document without it is a classic plan and is shown as raw text.
- A field is `Key: value` at column 0. Keys are case-insensitive ASCII. Unknown keys are kept and ignored by clients.
- Metadata for the field above is on following lines indented by two spaces, `key: value`. Known metadata: `set` (RFC 3339), `until` (RFC 3339), `source` (a short token naming the writer).
- A field with no `until` is static. A field whose `until` is in the past must not be emitted by a server and is dropped by a client.
- `Link` may repeat. Other fields appear at most once.
- `Plan:` alone on a line starts the free-text block, which runs to the end of the document. It is plain text.
- Blank lines are ignored outside the Plan block.
- Clients scan for known keys anywhere in the response, because classic fingerd wraps a user's `.plan` in its own `Login`, `Directory`, `Shell`, and `Plan:` lines. A `.plan/0` line inside that wrapper still counts.

## Fields

| Key | Meaning | Freshness |
|---|---|---|
| Name | display name | static |
| Headline | one line about the person | static |
| Photo | URL of an image | static |
| Link | a URL, repeatable | static |
| Status | the typed line, the away message | until, default none |
| Calendar | derived busy state, never the event subject unless public | event end |
| Focus | current Focus mode name | until next change |
| Listening | now playing | minutes |
| Working | what the person is working on | hours |
| Reading, Watching, Playing | reserved for later sources | days |

## Headline precedence

Where a renderer shows one line (link previews, a buddy list row), it takes the first present of: Status, Calendar, Focus, Listening, Headline.

## Finger transport

Query is the username and `\r\n`, or `/W username` for the long form. The short form is the document above without the Plan block. The long form is the whole document. An empty username lists public users one per line if the server allows it, otherwise returns a one-line refusal. Servers must not forward `user@host@host` queries.
