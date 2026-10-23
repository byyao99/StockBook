# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. It holds what applies to every change; the reasoning behind each feature lives in `docs/design/` — see **Design docs** below, and read the relevant one before changing that area.

## Overview

**StockBook** — a personal portfolio ledger: a Go + Gin REST API (repo root) serving **Instrument**, **Transaction** and **Position** resources, plus a Vue 3 + TypeScript SPA (`frontend/`) that consumes it. Users record trades and cash dividends that already happened; the system derives holdings, moving-average cost, and realized/unrealized profit and loss. There is no cash balance, no order matching, and no broker integration — it is a book of record, not a trading system.

Persistence is SQLite via GORM, using the pure-Go driver `github.com/glebarez/sqlite` (no cgo). The DB file path defaults to `stockbook.db` and is overridable with the `DB_PATH` env var.

## Commands

Backend (run from repo root):

```bash
go run .                    # start API on :8080 (PORT env var overrides)
go build ./...              # build
go vet ./...                # vet
go test -race ./...         # run the test suite — ALWAYS with -race
```

Backend env vars: `PORT` (default 8080), `DB_PATH` (default `stockbook.db`), `AUTH_SECRET` (HMAC key for bearer tokens — if unset a random key is generated at startup, so tokens do not survive a restart), and `ADMIN_USERNAME`/`ADMIN_PASSWORD` (seed an admin when no users exist yet).

Frontend (run from `frontend/`):

```bash
npm install
npm run dev                 # Vite dev server on :5173, proxies /api -> :8080
npm run build               # vue-tsc --noEmit type-check, then vite build
npm run type-check          # type-check only
npm test                    # vitest unit tests (npm run test:watch for watch mode)
```

`./dev.sh` from the repo root starts and stops both halves together; `PORT` / `WEB_PORT` override either side, and `vite.config.ts` reads the same `PORT` so the proxy follows the backend. It builds the binary and runs each half in its own process group on purpose — see `docs/design/dev-tooling.md` before changing it.

To check a change in the browser against the user's actual data, use the project skill `.claude/skills/verify-with-my-data`: it runs the working tree on a sqlite backup of `stockbook.db` on ports 8097/5197, logs in as the real user, and drives headless Chrome. Bugs here usually depend on the book's state (an instrument never synced, a flat fee profile), which a seeded test database does not have.

**Testing.** The `-race` flag is not optional: `internal/handlers/concurrency_test.go` justifies the compare-and-swap in the position write path and is close to meaningless without it. Frontend tests are Vitest units colocated as `src/*.test.ts` in a plain node environment (`vitest.setup.ts` stubs `localStorage`); there are no component tests. **No test ever reaches the network** — quote/news providers are stubbed or served from `httptest`. CI (`.github/workflows/ci.yml`) runs on pushes to master and PRs.

## Architecture

Requests flow `router → handlers → db → models`. `db.DB` wraps `*gorm.DB`, is created once in `main.go` via `db.Open` (which runs `AutoMigrate`), and is injected into handlers; **handlers never touch GORM directly**. Handlers generate UUID primary keys. The db layer maps `gorm.ErrRecordNotFound` to `db.ErrNotFound`; `handlers.respondDBError` (`common.go`) turns that into 404, the domain conflicts (`ErrInsufficientShares`, `ErrConflict`, `ErrSymbolTaken`, `ErrInstrumentInUse`, …) into 409, and anything else into a logged 500. The pool is capped at one connection because SQLite has one writer.

## Rules that apply everywhere

These are the things most easily broken by a well-meaning change. Each is pinned by tests; the full reasoning is in `docs/design/architecture.md` and `docs/design/invariants.md`.

- **Money is `int64` minor units, never a float** — every `price`, `fee`, `net_amount`, `cost_basis`, `realized_pl`. The frontend converts only at the UI edge (`money.ts`).
- **Share counts are `int64` in `models.SharesScale` (millionths).** Never divide a quantity by hand, and never multiply one by a price except through `models.Gross`, the single place the scale is divided out and the single rounding step. The frontend converts in `shares.ts`.
- **No FX rate exists, so amounts in different currencies are never added.** Every total and report returns a **slice, one entry per currency**. An instrument's currency is locked once trades exist (`ErrCurrencyLocked`).
- **Unknown is not zero.** A missing quote, close, estimate or rate is `nil`/`null` and renders as an em dash; anything unvalued is excluded from totals and *counted* (`PricedPositions` vs `OpenPositions`, `WithoutHistory`, `UnstampedEntries`) rather than folded in as 0. Do not "simplify" these to zero.
- **Nothing is ever posted to the ledger automatically.** `DividendEvent` and `RecurringPlan` generate prompts that fill the trade form in; saving is always the user's act.
- **Rates cross the wire as integers**: returns in basis points (`XIRRBps`), fee rates in parts per million (a basis point cannot express 0.1425%).
- **Dates**: calendar days are `YYYY-MM-DD` strings in UTC; the trade form stores a chosen date at noon UTC; a bare `?to=` covers the whole of that day (`parseDateRange`).
- **A ledger is strictly personal, including from admins.** Every query is scoped to the caller in SQL; another user's resource is **404, not 403**.
- **Provider calls are caller-triggered, never background jobs**, and rate-limited. A failed fetch changes nothing (no restamp, no zero); a series or quote in a currency the instrument is not recorded in is a failure, not a price.

**The six invariants** (`docs/design/invariants.md`):

1. `Position.CostBasis` is the **total** remaining cost, never an average — no `average_cost` column or response field. A full exit leaves exactly 0 (`TestApplyFullExitLeavesNoResidue`).
2. **Replaying the ledger defines a position**; the `positions` table is a cache. The incremental shortcut applies only when the new entry sorts last; edits, deletes and back-dated entries replay. Any new write path goes into `TestMaterializedPositionMatchesReplay`.
3. The ledger's sort key is `(traded_at, created_at, id)` (`ledgerOrder`, `isLastInLedger`).
4. Position writes are **compare-and-swap** on `(quantity, cost_basis, realized_pl)`; zero rows matched is `ErrConflict` → 409 (`internal/db/cas_test.go`).
5. `NetAmount` is always computed server-side (`models.NetAmount`); `Symbol` is snapshotted at entry time. `Fee` is the opposite: a recorded fact the server stores but never estimates.
6. Each sell's and dividend's `RealizedPL` stamp is derived by subtraction across one `Apply` and **must sum to the position's total** (`TestStampsSumToPositionRealized`); the replay path restamps the whole ledger.

All moving-average arithmetic lives in `models.PositionState.Apply` (pure, no DB):

```
BUY(q, price, fee):   qty += q;  cost += q*price + fee
SELL(q, price, fee):  requires qty >= q
                      costRemoved = round(cost * q / qty)   // half-up, integer only
                      realized   += (q*price - fee) - costRemoved
                      cost       -= costRemoved;  qty -= q
DIVIDEND:             realized += payout - withheld          // income, never a cost reduction
```

## Route gating

`internal/router/router.go` (details in `docs/design/api-and-auth.md`):

| Routes | Access |
|---|---|
| `GET /instruments`, `GET /instruments/:id` | any authenticated user (needed to enter a trade) |
| `POST /instruments`, `GET /instruments/search` | any authenticated user, rate-limited |
| `PUT/DELETE /instruments/:id`, `PATCH /instruments/:id/price` | admin |
| `POST /instruments/refresh-quotes` | any authenticated user, rate-limited 6/min per IP |
| `POST /instruments/sync-history` | any authenticated user, rate-limited 2/min per IP |
| `GET /research/news`, `GET /research/fundamentals` | any authenticated user; the feed is **scoped to their own holdings** |
| `POST /research/sync` | any authenticated user, rate-limited 2/min per IP |
| `/transactions/*`, `/positions/*` (incl. `/positions/:instrument_id`), `/reports/*` (incl. `/reports/dividends` and `/reports/splits`), `/plans/*`, `/settings/*` (fees **and** benchmarks) | any authenticated user, **scoped to themselves** |
| `/users/*` | admin |

List endpoints take `?limit` (default 20, max 100), `?offset`, `?sort`, `?order` and answer `{"data": [...], "pagination": {...}}`; sorting only ever emits a column from a per-resource allowlist — never interpolate `ListOptions.Sort`.

## Design docs

Read the relevant file before changing an area — each records decisions that look arbitrary from the code alone, and several record approaches that were tried and rejected.

| Area | Doc | Main code |
|---|---|---|
| Money, shares, currencies, layering | `docs/design/architecture.md` | `internal/models`, `money.ts`, `shares.ts` |
| Ledger invariants, moving average, unknown-is-not-zero | `docs/design/invariants.md` | `position_state.go`, `internal/db/positions.go` |
| Brokerage fees | `docs/design/fees.md` | `FeeProfile`, `feeMath.ts` |
| Cash dividends and their prompt | `docs/design/dividends.md` | `dividends.go`, `DividendEvent` |
| Quotes, instrument create/search | `docs/design/quotes.md` | `internal/quotes`, `handlers/instrument*.go` |
| Daily closes and history sync | `docs/design/price-history.md` | `internal/db/history.go`, `handlers/history.go` |
| Split detection and warnings | `docs/design/splits.md` | `splits.go`, `SplitWarnings.vue` |
| Savings plans | `docs/design/plans.md` | `internal/db/plans.go`, `SavingsPlans.vue` |
| Company research (news, fundamentals) | `docs/design/research.md` | `internal/news`, `ResearchView.vue` |
| Equity curve and benchmark | `docs/design/curve-and-benchmark.md` | `curve.go`, `benchmarks.go`, `curveMath.ts` |
| Realized, hindsight, XIRR reports | `docs/design/reports.md` | `reports.go`, `hindsight.go`, `returns.go`, `xirr.go` |
| A holding's own page | `docs/design/holding.md` | `holding.go`, `HoldingView.vue` |
| Allocation | `docs/design/allocation.md` | `allocation.ts` |
| Pagination, auth, scoping, logging | `docs/design/api-and-auth.md` | `internal/auth`, `internal/middleware` |
| Frontend views and components | `docs/design/frontend.md` | `frontend/src` |
| dev.sh, tests, CI | `docs/design/dev-tooling.md` | `dev.sh` |

## Conventions

- Adding a backend resource touches every layer: model in `internal/models` (registered in `AutoMigrate` in `db.Open`), methods on `db.DB`, a handler with a `New*Handler` constructor, routes in `router.go`, and on the frontend `types.ts` (which mirrors the Go JSON shapes and is the contract between the halves) and `api/client.ts` (the only place that talks HTTP). Add a test that another user's resource is 404.
- `AutoMigrate` adds columns and tables but never alters or drops them. A change of **units** needs a one-off backfill guarded by a marker in `models.SchemaMeta` — `db.scaleShareCounts` must run exactly once, since nothing in the data says whether it already has.
- `respondDBError` logs 500s through `slog` with the request id — never `log.Printf`.
- `GET /health` reports `started_at`, `uptime` and `revision`, to tell whether the running server is the code just written (`go run .` does not reload).
- CORS is permissive for local dev; tighten before any real deployment.
- Code comments and all user-facing strings are in English.
- When a change alters a design decision, update the matching `docs/design/` file in the same commit.

## Deliberately out of scope

Cash balances and buying power; FX rates and cross-currency totals; FIFO or specific-lot cost basis (moving average is load-bearing — FIFO would need a lot table and would change invariants 1 and 2); adjusting a book for stock dividends, splits or reverse splits (they are *detected* and warned about — see `splits.md` — but never applied); automatic dividend or plan *posting*; estimating what is withheld from a dividend; multiple portfolios per user. The **earnings calendar** is unavailable rather than declined: it lives in Yahoo's `quoteSummary`, which now needs a cookie and crumb.
