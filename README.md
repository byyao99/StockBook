# StockBook

A personal portfolio ledger. Record the trades you actually made and the dividends you were paid; StockBook derives your holdings, moving-average cost, and realized/unrealized profit and loss.

It is a book of record, not a trading system — there is no cash balance, no order matching, and no broker connection. Quotes are fetched from Yahoo Finance on demand, or entered by hand.

- **Backend** — Go + Gin + GORM + SQLite (pure Go, no cgo)
- **Frontend** — Vue 3 + TypeScript + Vite, no state library and no UI framework

## Quick start

```bash
# terminal 1 — API on :8080
AUTH_SECRET=dev-secret ADMIN_USERNAME=admin ADMIN_PASSWORD=Passw0rd go run .

# terminal 2 — SPA on :5173, proxying /api to the backend
cd frontend && npm install && npm run dev
```

Open http://localhost:5173, sign in as `admin`, add an instrument on the **Instruments** page and give it a quote, then register a second account to keep a book with.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `DB_PATH` | `stockbook.db` | SQLite file |
| `AUTH_SECRET` | random | HMAC key for bearer tokens. Unset means a fresh key each start, so tokens do not survive a restart — set it in any real deployment. |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | — | Seeds an admin when both are set and no users exist yet |

`GET /health` reports `started_at` and `uptime` alongside the DB check. The backend does **not** hot-reload — after changing Go code, restart `go run .`, and check the start time if a request behaves like the old contract.

## Tests

```bash
go vet ./... && go build ./... && go test -race ./...
cd frontend && npm test && npm run build
```

`-race` is not optional: the concurrency tests are what justify the compare-and-swap in the position write path.

## How it works

Two ideas carry most of the design:

**The ledger is the truth; holdings are a cache.** Every position can be rebuilt by replaying a user's transactions in order. Appending a trade takes an incremental shortcut, but editing one, deleting one, or back-dating one into the middle of history triggers a full replay — moving-average cost is order-dependent, so a single incremental step would compute the basis against the wrong prefix.

A consequence worth knowing: **deleting a trade can be refused**. Removing a buy that a later sell drew on would punch a hole in history, so the replay catches it and the whole operation rolls back with a 409 naming the entry that would have been left short.

**Cost is stored as a total, never as an average.** Three shares at $10.00 plus one at $10.01 average 1000.25 cents — not an integer. Storing the total keeps it exact; the average is derived only for display. Every monetary value in the system is `int64` hundredths, converted at the UI edge and nowhere else.

An instrument with no quote reports an *unknown* market value, not zero — the portfolio summary tells you how many holdings its totals leave out rather than quietly presenting a partial figure as the whole.

**Currencies are tracked, never converted.** An instrument is denominated in TWD or USD, and the portfolio summary comes back as one total per currency rather than a single figure — there is no exchange rate in this system, and inventing one would be worse than showing two numbers. A currency is locked once trades exist against the instrument.

## Dividends

A cash dividend is recorded on the **Ledger** page like a trade — pick the stock, choose `dividend`, and enter the shares it was paid on, the amount per share, and any tax withheld. The form relabels itself; there is no separate screen.

**A dividend is income, not a discount on what you paid.** It is banked in full and your cost basis does not move, so the shares you still hold keep showing the gain they actually have. (Deducting the payout from the cost basis is the US convention for a return of capital; applying it to an ordinary Taiwanese cash dividend would quietly flatter every holding that pays one.)

Nothing checks the dividend against the shares you hold *today*, on purpose: a payout is earned on the ex-dividend date but arrives weeks later, by which time you may have sold, and refusing that entry would decline to record money you were really paid.

Stock dividends and splits are not supported — they change historical share counts, which is a much larger change. Dividends are also not fetched automatically yet, though the price provider does publish them.

## Realized results

The **Realized** page answers what you actually banked, and over which period. Pick a year (or any two dates) and it reports the result broken down by holding — one block per currency, never a combined figure.

**Trading and dividends are shown apart as well as together**, because they are taxed differently and because a year that lost money on price but made it back on income should not read as a flat one. The proceeds and cost sold describe the sales alone, so the return percentage is measured against the trading result: dividends are earned by holding shares, not by selling them.

Each sale and each dividend carries its own result, shown in the ledger next to it, and those per-entry numbers are exactly what the report sums: they are a decomposition of the running total on the Holdings page, not a second calculation of it. Because moving-average cost is order-dependent, correcting or back-dating a trade rewrites the result of every sale after it — which is the point of deriving positions from the ledger rather than the other way round. A buy shows a dash rather than a zero: it realizes nothing, which is not the same as breaking even.

## Adding instruments

There is no "add an instrument" step. The instrument field on the **Ledger** page is a search box: type a stock number or company name — `2330`, `6488`, `TSLA`, `nvidia` — and pick a result. Anything you have traded before appears instantly; anything else comes from the price provider and is added as you pick it, with its symbol, market, currency, name **and current price** all fetched for you.

**An instrument that cannot be priced cannot be added at all.** The quote is fetched as part of creating it, and a symbol the provider does not know is refused outright rather than sitting in the list looking normal until a quote fails to arrive weeks later. It also means a new instrument is priced immediately, so holdings can be valued as soon as a trade is recorded.

Taiwan and US listings can be priced; other countries' exchanges, along with indices, futures and currency pairs, are filtered out of results. US ETFs mostly list on NYSE Arca, Cboe or NYSE American rather than on NYSE or Nasdaq proper — VOO, SPY, VTI and the rest are all filed under `NYSE`, which is a slight simplification of where they actually trade but has no effect on how they are priced. The provider matches symbols and English names but **not Chinese** — search `2330`, not `台積電`. Adding is open to any signed-in user — otherwise you could not record a trade in a stock nobody had entered yet. Renaming and deleting an instrument stay with admins and have no UI; they are API-only.

Refreshing quotes lives on the Holdings page, where the numbers it feeds are shown.

## Quotes

Press **Refresh quotes** on the Holdings page to pull current prices from Yahoo Finance. Taiwan listings are looked up as `2330.TW` / `6488.TWO` and US listings by their bare symbol — enter the bare symbol and pick the market; the suffix is added for you.

Any signed-in user can refresh: the holdings page is built on unrealized profit and loss, so making that an admin's job would leave everyone else stuck with a stale book. Quotes already checked within the last 15 minutes are left alone, and the endpoint is rate-limited, so an open button cannot turn into a flood of outbound calls. Setting a price *by hand* is still admin-only.

Yahoo has no official public API; this uses the undocumented endpoint behind their charts, which needs no key but is unsupported and may break. Everything provider-specific lives in `internal/quotes`, so swapping in another source is a one-file change.

A refresh reports each symbol separately — one delisted ticker does not stop the rest — and a failure carries the provider's own explanation. **A failed fetch changes nothing**: the instrument keeps its previous price and its previous timestamp, so a stale quote keeps looking stale instead of being silently restamped. To run it on a schedule, point cron at `POST /api/v1/instruments/refresh-quotes`.

See `CLAUDE.md` for the full architecture and the invariants to preserve when changing it.

## API

All routes are under `/api/v1`. Single resources return `{"data": ...}`, lists add a `pagination` block, errors return `{"error": "..."}`.

| Method | Path | Access |
|---|---|---|
| `POST` | `/auth/register` | public (always creates a plain user) |
| `POST` | `/auth/login` | public, rate-limited 10/min per IP |
| `PUT` | `/auth/password` | authenticated |
| `GET` | `/instruments`, `/instruments/:id` | authenticated |
| `POST` `PUT` `DELETE` | `/instruments`, `/instruments/:id` | admin |
| `PATCH` | `/instruments/:id/price` | admin |
| `POST` | `/instruments`, `GET /instruments/search` | authenticated, rate-limited |
| `POST` | `/instruments/refresh-quotes` | authenticated, rate-limited 6/min |
| `PUT` `DELETE` | `/instruments/:id` | admin (no UI) |
| `GET` `POST` | `/transactions` | authenticated, own book only |
| `GET` `PUT` `DELETE` | `/transactions/:id` | authenticated, own book only |
| `GET` | `/positions`, `/positions/summary` | authenticated, own book only |
| `GET` | `/reports/realized?from=&to=` | authenticated, own book only |
| `GET` `POST` `PUT` `DELETE` | `/users`, `/users/:id/...` | admin |

A ledger is private even from admins: an admin manages instruments and accounts, not other people's holdings. Another user's transaction returns 404, not 403.
