# Setting Focus with Shortcuts

Two shortcuts keep the `Focus` line of your nowfile in step with your Focus mode, with no app to install. One sets `Focus: Work` when the Work Focus turns on; the other clears it when Work turns off. Focus automations run them for you.

This works on iPhone and iPad, and on a Mac running macOS 26 (Tahoe) or later, which added personal automations to Shortcuts. On an older Mac, build the automations on your iPhone and turn on Share Across Devices in Settings > Focus: turning Work on from the Mac turns it on for the iPhone too, and the iPhone's automation updates your nowfile.

## 1. Get a token

On your account page at https://nowfile.org/account, under Tokens, type the label `shortcuts` and press Create token. The page shows the token once; copy it then. If you lose it, revoke it and make another.

## 2. The "set" shortcut

In the Shortcuts app, tap + to make a new shortcut and name it `nowfile Focus Work`. Add one action, Get Contents of URL, and set it up:

- URL: `https://nowfile.org/api/v1/slots/Focus`
- Tap the arrow (iOS) or Show More (Mac) to see the rest.
- Method: `PUT`
- Headers: Add new header. Key `Authorization`, value `Bearer ` followed by your token, with one space after `Bearer`.
- Request Body: `JSON`. Add new field > Text, key `value`, text `Work`. Add new field > Text, key `source`, text `shortcuts`.

That sends this request:

```
PUT /api/v1/slots/Focus HTTP/1.1
Host: nowfile.org
Authorization: Bearer <your token>
Content-Type: application/json

{"value": "Work", "source": "shortcuts"}
```

Do not add other fields. The server rejects a body with keys it does not know. Leave out `until`: a Focus line stays until the next change, and the "clear" shortcut makes that change.

Run the shortcut once by hand. Shortcuts asks whether it may connect to nowfile.org; choose Always Allow, or the automation will stop at that prompt later. The server answers `204 No Content` with an empty body, so the shortcut shows no output. Check the result:

```sh
finger yourname@nowfile.org
```

which now includes `Focus: Work`.

## 3. The "clear" shortcut

Make a second shortcut, `nowfile Focus clear`, with one Get Contents of URL action:

- URL: `https://nowfile.org/api/v1/slots/Focus`
- Method: `DELETE`
- Headers: `Authorization`, value `Bearer <your token>`, as before.
- Request Body: none.

```
DELETE /api/v1/slots/Focus HTTP/1.1
Host: nowfile.org
Authorization: Bearer <your token>
```

Run it once by hand and allow the connection. The `Focus` line is gone from `finger`. Clearing when nothing is set is not an error.

## 4. The automations

In Shortcuts, open the Automation tab and tap + (New Automation).

1. Choose Focus, then Work, then When Turning On. Choose Run Immediately, not Run After Confirmation, or each one waits for a tap. Tap Next and pick `nowfile Focus Work`.
2. Make another: Focus, Work, When Turning Off, Run Immediately, then `nowfile Focus clear`.

Turn Work on in Control Center, and `finger` shows `Focus: Work` within a few seconds. Turn it off and the line goes away.

## Other Focus modes

The automation does not tell the shortcut which Focus turned on, so each mode gets its own "set" shortcut with its name typed into `value`: copy `nowfile Focus Work`, change `Work` to `Sleep` or `Driving`, and add a pair of automations for that mode. All of them can share the one "clear" shortcut.

The value is your text, up to one line; it does not have to match the Focus name. `{"value": "Heads down, back at 3", "source": "shortcuts"}` works too.

## Self-hosting

Replace `nowfile.org` with your own server's host in the URL. The request is the same.
