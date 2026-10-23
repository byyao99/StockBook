# API conventions, authentication, and route gating

_Pagination, sorting, auth, per-user scoping, logging._

**List endpoints are paginated and sortable.** All list endpoints take `?limit` (default 20, max 100), `?offset`, `?sort`, and `?order`, parsed by `handlers.parseListOptions` into a `db.ListOptions`. Response shape is `{"data": [...], "pagination": {limit, offset, total}}`; single resources are `{"data": ...}` and errors `{"error": "..."}`. **Sorting is SQL-injection-safe by construction:** `db.orderClause` only emits a column name present in a per-resource allowlist (`instrumentSortColumns` / `transactionSortColumns` / `userSortColumns`), else falls back to the default. The position list spans a join, so it has its own `positionSortColumns` mapping sort keys to *qualified* column names — needed both to disambiguate `updated_at` (present on both tables) and as the same injection guard. Never interpolate `ListOptions.Sort` into a query directly.

**Authentication & authorization.** Accounts live in a `users` table (bcrypt `PasswordHash`, `Role` of `user`/`admin`). `internal/auth` provides bcrypt helpers and a `Manager` that issues/verifies bearer tokens — base64url JSON claims joined by `.` to an HMAC-SHA256 signature (key from `AUTH_SECRET`). Signature/expiry checks are stateless, but `middleware.authenticate` then loads the account from the DB (via the `middleware.UserSource` interface) and requires that it still exists and that the token's `token_version` claim matches `User.TokenVersion` — so deletions, role changes and password changes take effect immediately. The identity placed in the Gin context comes from the DB record, not the token claims. `POST /api/v1/auth/register` always creates a **user** (clients can never self-assign a privileged role); `POST /api/v1/auth/login` is rate-limited per client IP (10/minute, in-memory fixed window, per-process). Admin-only `/api/v1/users` endpoints provision, list, re-role and delete accounts; an admin cannot change or delete their own account, so they cannot lock themselves out.

**Route gating** (`internal/router/router.go`):

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

`PATCH /instruments/:id/price` is a separate route from `PUT /instruments/:id` on purpose: keeping quotes current is routine daily work while editing master data is not, so they deserve to be separately grantable and separately auditable.

**A ledger is strictly personal — including from admins.** Every transaction and position query is scoped to the caller in the db query itself. An admin's privileges cover the shared instrument master data and user accounts, *not* other people's books. Reading or deleting someone else's transaction returns **404, not 403**, so IDs cannot be probed for existence. `TestAdminSeesOnlyTheirOwnBook` and `TestAnotherUsersTransactionIsNotFound` pin this down.

**Logging & request IDs.** `main.go` configures a JSON `slog` logger as the default. `internal/middleware` adds `RequestID` (reuses an inbound `X-Request-ID` or generates a UUID, echoes it back) and `Logger` (one structured record per request). The router uses `gin.New()` + these + `gin.Recovery()` instead of `gin.Default()`. `respondDBError` logs 500s via `slog` including the request id — never `log.Printf`.
