# Dev tooling: dev.sh, tests, CI

_Why dev.sh is built the way it is, and what the test suites cover._

To work on the full stack, `./dev.sh` from the repo root starts both and stops both — the two halves are useless apart, since the Vite proxy (`frontend/vite.config.ts`) forwards `/api/*` to the backend and the SPA uses same-origin relative URLs. `PORT` and `WEB_PORT` override either side — `frontend/vite.config.ts` reads the same `PORT`, so the proxy follows the backend rather than pointing at a hardcoded 8080 nothing is listening on. Output is prefixed `[api]` / `[web]` so one terminal reads as two.

Two things in that script are load-bearing rather than stylistic. It **builds** the binary instead of using `go run`, because `go run` compiles to a temporary binary and execs it as a child — the PID a supervisor would hold is the wrapper, so killing it leaves the server holding the port under a process nobody can find. And it runs each half in its **own process group** (`set -m`) so the whole tree can be signalled: `npm run dev` spawns Vite, and signalling only npm leaves Vite on the port. A busy port is refused up front, because the alternative is a backend that logs a bind error, exits, and leaves a frontend proxying to nothing with the real message scrolled off the top.

Running the two by hand still works and is what the commands above describe.

To check a change in the browser against the user's actual data, use the project skill `.claude/skills/verify-with-my-data`: it runs the working tree on a sqlite backup of `stockbook.db` on ports 8097/5197, logs in as the real user, and drives headless Chrome. Bugs here usually depend on the book's state (an instrument never synced, a flat fee profile), which a seeded test database does not have.

**Testing & CI.** `go test -race ./...` — the `-race` flag is not optional here. The concurrency tests in `internal/handlers/concurrency_test.go` are the ones that justify the compare-and-swap in the position write path, and without the race detector they are close to meaningless. Frontend tests are Vitest units colocated with their modules (`src/*.test.ts`) covering `money.ts`, `session.ts`, `positionMath.ts`, `curveMath.ts`, and `feeMath.ts` — `curveMath.ts` is the contract test mirroring the backend's valuation rules, so changing how a position is valued means updating `internal/db/positions.go`, `positionMath.ts`, and that test together. Tests run in a plain node environment; `frontend/vitest.setup.ts` stubs `localStorage`. There are no component tests yet; adding some means switching `test.environment` in `vite.config.ts` to a DOM implementation. CI (`.github/workflows/ci.yml`) runs on pushes to master and PRs.

## Environment

Backend env vars: `PORT` (default 8080), `DB_PATH` (default `stockbook.db`), `AUTH_SECRET` (HMAC key for bearer tokens — if unset a random key is generated at startup, so tokens do not survive a restart; set it in any real deployment), and `ADMIN_USERNAME`/`ADMIN_PASSWORD` (when both set and no users exist yet, an admin account is seeded on startup).
