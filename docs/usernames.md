# Username policy

Decided 2026-10-13. Usernames are the left side of `name@nowfile.org`, so they have to work in a finger query, a URL path, and spoken aloud.

- 2 to 32 characters. Lowercase ASCII letters, digits, and hyphen. Must start with a letter. No trailing hyphen, no two hyphens in a row.
- Case-insensitive everywhere: input is lowercased on sign-up and on lookup. `Mike`, `mike`, and `MIKE` are the same person.
- One account per verified email address.
- Reserved and never issued: `nowfile`, `admin`, `root`, `www`, `mail`, `abuse`, `postmaster`, `hostmaster`, `webmaster`, `security`, `support`, `help`, `about`, `api`, `static`, `finger`, `plan`, `status`, `me`, `you`, `null`, `undefined`, `anonymous`, `nobody`, `system`, `daemon`, `user`, `test`, `login`, `logout`, `signup`, `signin`, `settings`, `account`, `terms`, `privacy`, `blog`, `docs`, `dev`, `ops`, `directory`, `subscribe`, `unsubscribe`. Every top-level route the site serves is also reserved, and adding a route means adding its name here. `mike` is held for Mike.
- Permanent for the life of the account. No renames in version 1. Deleting the account releases the name after a 90-day hold.
- Inactive accounts, meaning no sign-in and no write for 18 months, get a warning email, then are deleted 30 days later and the name goes through the same 90-day hold. This is the only squatting control.
- No profanity or impersonation list in version 1. Abuse reports go to a human; the server has an admin command to suspend a name.
