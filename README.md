# StockBook

A personal portfolio ledger. Record the trades you actually made; StockBook derives your holdings, moving-average cost, and realized/unrealized profit and loss.

It is a book of record, not a trading system — there is no cash balance, no order matching, and no broker connection. Quotes are entered by hand.

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

**Cost is stored as a total, never as an average.** Three shares at $10.00 plus one at $10.01 average 1000.25 cents — not an integer. Storing the total keeps it exact; the average is derived only for display. Every monetary value in the system is `int64` cents, converted to dollars at the UI edge and nowhere else.

An instrument with no quote reports an *unknown* market value, not zero — the portfolio summary tells you how many holdings its totals leave out rather than quietly presenting a partial figure as the whole.

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
| `GET` `POST` | `/transactions` | authenticated, own book only |
| `GET` `PUT` `DELETE` | `/transactions/:id` | authenticated, own book only |
| `GET` | `/positions`, `/positions/summary` | authenticated, own book only |
| `GET` `POST` `PUT` `DELETE` | `/users`, `/users/:id/...` | admin |

A ledger is private even from admins: an admin manages instruments and accounts, not other people's holdings. Another user's transaction returns 404, not 403.
