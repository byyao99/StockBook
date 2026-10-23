---
name: verify-with-my-data
description: Run a throwaway copy of StockBook (API + Vite) on a copy of the user's real stockbook.db, log in as them, and drive the UI in headless Chrome to reproduce or verify behaviour. Use this whenever a StockBook bug or change has to be checked in the actual app rather than only in tests — "it doesn't fill in", "the number looks wrong", "check the Ledger page", "does this work with my data", a screenshot of the UI showing something off, or before telling the user a UI fix works. Prefer it over hand-building a test server, seeding SQLite, or writing puppeteer from scratch.
---

# Verify with my data

Reproduce what the user sees by running the current working tree against a
**copy** of their real book, then drive it the way they would. Their bugs almost
always depend on their data — an instrument whose price history was never
synced, a flat fee profile, a plan on an untraded symbol — and a seeded test
database will not have that state, so it will not reproduce the bug.

## Workflow

Scripts are in `scripts/` next to this file. Point `E2E_DIR` at the session
scratchpad so nothing lands in `/tmp`.

```bash
SK=.claude/skills/verify-with-my-data/scripts
export E2E_DIR=<scratchpad>/e2e
$SK/up.sh            # or: $SK/up.sh <username>
node $SK/look.mjs /transactions
# … investigate, write drivers, fix …
$SK/down.sh          # always, even after a failure
```

1. **`up.sh`** copies `stockbook.db` with sqlite's online backup (read-only on
   the real file), builds the API, starts it and Vite on ports 8097/5197, and
   makes the user's account log-in-able on the copy with a known password. With
   no argument it picks the user with the most ledger entries. It refuses to
   start if a run is already up or a port is busy.
2. **Look before you click.** `look.mjs <path>` prints each card's text, the trade
   form's fields, the API calls the page made, any errors, and saves a
   screenshot — Read the PNG to see layout problems the text cannot show.
3. **Query the copy when the UI surprises you.** The answer is often in the
   data: `sqlite3 "$E2E_DIR/book.db" "…"`. Querying the real file is fine too, as
   long as it is `sqlite3 -readonly`.
4. **Write a small driver** for anything interactive (template below) and run it
   with `node`. Keep it in `$E2E_DIR`.
5. **After a code change:** Vite hot-reloads the frontend, so just rerun the
   driver. A Go change needs `down.sh` + `up.sh`, because the API binary was
   built at startup. That is also how to get a fresh copy after a driver
   changed the data (synced prices, recorded a trade).
6. **`down.sh`** stops both servers and deletes the files `up.sh` created, by
   name.

## Driver template

```js
import * as e2e from '/abs/path/to/.claude/skills/verify-with-my-data/scripts/lib.mjs'

const ctx = await e2e.open()            // logged in as the user
try {
  await e2e.go(ctx, '/transactions')
  console.log(await e2e.texts(ctx, 'section.pending li'))
  await e2e.clickText(ctx, 'Record', { within: 'section.pending li', nth: 0 })
  await e2e.settle(ctx)                 // network idle, no "…" button left busy
  console.log(await e2e.fields(ctx))    // { Instrument, Shares, Price (USD), … }
  console.log(ctx.net, ctx.errors, ctx.dialogs)
  console.log(await e2e.shot(ctx, 'after-record'))
} finally {
  await e2e.close(ctx)
}
```

`lib.mjs` also has `fill(ctx, selector, value)` for inputs, and `mouseWorks(ctx)`.

## Things that will waste an hour if you don't know them

- **Click through the DOM, not the mouse.** After the login redirect, headless
  Chrome here sometimes stops delivering mouse input to the page entirely — no
  `pointerdown` arrives on any route, while `element.click()` keeps working. The
  symptom ("clicked Record, nothing happened, no request sent") looks exactly
  like the app bug you are hunting. `clickText` and `fill` avoid the mouse. If a
  test truly needs the mouse, check `mouseWorks(ctx)` first.
- **Reproduce the user's starting state before fixing.** If they say "focused
  the search box, then pressed Record", do exactly that in the driver. The
  picker bug only appeared once the box had been focused first.
- **Some actions reach the real price provider.** Sync prices, quote refresh,
  research sync, and Record on an instalment with no estimate all call Yahoo/cnyes
  from the copy. That is fine, but they are rate-limited per server (sync: 2/min),
  so do not loop them. The data they fetch lands in the copy, so after one run
  the "no price yet" state is gone — `down.sh` + `up.sh` restores it.
- **Logging in lands on Holdings, which refreshes quotes on arrival** (Research
  syncs news the same way). That call can run for a minute. `settle()` waits only
  for requests started by the last `go` / `clickText` / `fill`, so it is not held
  up — do not swap in puppeteer's whole-page `waitForNetworkIdle`, which is.
- **The user's own server is usually on 8080/5173.** Never stop it and never
  point a driver at it; it writes to the real book.
- **zsh aborts the whole command on an unmatched glob** (`rm x.db-*` with no
  `-wal` file present). Name files explicitly.

## Reporting

Tell the user what you reproduced, using their data's terms ("QQQM has no stored
closes, so…"). Show the field values and the API calls from the driver, and Read
the screenshot rather than describing one you have not looked at. Say plainly
what was checked through DOM events rather than a real mouse.
