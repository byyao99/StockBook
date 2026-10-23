#!/usr/bin/env bash
#
# Bring up a throwaway copy of StockBook running on a copy of the real book.
#
#   up.sh [username]     log in as this user (default: whoever has the most entries)
#
# Env overrides: E2E_DIR (run directory), E2E_API_PORT (8097), E2E_WEB_PORT (5197),
# E2E_SOURCE_DB (the repo's stockbook.db).
#
# The real database is only ever read, through sqlite's online backup, so a
# server the user has running on it is not disturbed and nothing here can write
# to it. Everything this starts lives on its own ports and in its own directory,
# and down.sh removes exactly what this created.

set -euo pipefail

ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
RUN="${E2E_DIR:-${TMPDIR:-/tmp}/stockbook-e2e}"
API_PORT="${E2E_API_PORT:-8097}"
WEB_PORT="${E2E_WEB_PORT:-5197}"
SRC_DB="${E2E_SOURCE_DB:-$ROOT/stockbook.db}"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/stockbook-e2e"
AS_USER="${1:-}"
# Only ever valid inside the copy. It satisfies the register endpoint's rules
# (8+ characters, two character classes).
PASSWORD="e2e-Probe-1234"

if [ -f "$RUN/env" ]; then
  echo "a run is already up in $RUN — run down.sh first" >&2
  exit 1
fi
if [ ! -f "$SRC_DB" ]; then
  echo "no database at $SRC_DB" >&2
  exit 1
fi
for port in "$API_PORT" "$WEB_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $port is in use — set E2E_API_PORT / E2E_WEB_PORT" >&2
    exit 1
  fi
done

mkdir -p "$RUN"

# puppeteer-core drives the Chrome already installed, so there is no browser
# download; it is cached outside the repo so no node_modules lands in it.
if [ ! -d "$CACHE/node_modules/puppeteer-core" ]; then
  echo "installing puppeteer-core into ${CACHE}…"
  mkdir -p "$CACHE"
  (cd "$CACHE" && { [ -f package.json ] || npm init -y >/dev/null; } && npm i puppeteer-core@23 --silent)
fi

echo "copying ${SRC_DB}…"
sqlite3 -readonly "$SRC_DB" ".backup '$RUN/book.db'"

if [ -z "$AS_USER" ]; then
  AS_USER="$(sqlite3 "$RUN/book.db" "select u.username from users u
    order by (select count(*) from transactions t where t.user_id = u.id) desc limit 1;")"
fi
if [ -z "$(sqlite3 "$RUN/book.db" "select id from users where username = '$AS_USER';")" ]; then
  echo "no user '$AS_USER' in the copy; users are:" >&2
  sqlite3 "$RUN/book.db" "select '  ' || username || ' (' || role || ')' from users;" >&2
  exit 1
fi

# Built rather than `go run`, so the PID recorded is the server and not a
# wrapper — the same reason dev.sh builds.
echo "building the API…"
(cd "$ROOT" && go build -o "$RUN/api" .)

# set -m gives each background job its own process group, so down.sh can stop
# Vite along with the npm that spawned it.
#
# Both are detached from this script's stdin/stdout. The Vite job is a
# subshell that lives as long as Vite does, and with this script's stdout still
# open in it, a caller reading that output (a pipe, a tool) never sees EOF and
# waits forever — even though everything has started.
set -m
DB_PATH="$RUN/book.db" PORT="$API_PORT" AUTH_SECRET=e2e nohup "$RUN/api" \
  </dev/null >"$RUN/api.log" 2>&1 &
API_PID=$!
(cd "$ROOT/frontend" && PORT="$API_PORT" exec nohup npx vite --port "$WEB_PORT" --strictPort) \
  </dev/null >"$RUN/web.log" 2>&1 &
WEB_PGID=$!
set +m

# Written before waiting, so a failed start can still be torn down.
cat >"$RUN/env" <<EOF
E2E_DIR=$RUN
BASE=http://localhost:$WEB_PORT
API=http://localhost:$API_PORT
E2E_USER=$AS_USER
E2E_PASSWORD=$PASSWORD
API_PID=$API_PID
WEB_PGID=$WEB_PGID
EOF

wait_for() {
  for _ in $(seq 1 60); do
    curl -sf -o /dev/null "$1" && return 0
    sleep 0.5
  done
  echo "$2 did not come up — see $3" >&2
  exit 1
}
wait_for "http://localhost:$API_PORT/health" "the API" "$RUN/api.log"
wait_for "http://localhost:$WEB_PORT" "Vite" "$RUN/web.log"

# Log in as the real user without knowing their password: register a probe
# account (the server hashes the password the normal way), copy that hash onto
# the real user's row in the copy, and drop the probe. Every table keyed by
# user — ledger, plans, fees, benchmarks, acknowledgements — then comes along
# as it is, with nothing to enumerate or forget.
PROBE="e2e-probe-$$"
curl -sf -o /dev/null "http://localhost:$API_PORT/api/v1/auth/register" \
  -H 'content-type: application/json' \
  -d "{\"username\":\"$PROBE\",\"password\":\"$PASSWORD\"}"
sqlite3 "$RUN/book.db" "
  update users set password_hash = (select password_hash from users where username = '$PROBE')
    where username = '$AS_USER';
  delete from users where username = '$PROBE';"

if ! curl -sf -o /dev/null "http://localhost:$API_PORT/api/v1/auth/login" \
  -H 'content-type: application/json' \
  -d "{\"username\":\"$AS_USER\",\"password\":\"$PASSWORD\"}"; then
  echo "could not log in as $AS_USER on the copy" >&2
  exit 1
fi

echo
echo "up: $RUN"
echo "  web  http://localhost:$WEB_PORT   (Vite serves the working tree, hot-reloaded)"
echo "  api  http://localhost:$API_PORT   (built from the working tree now; rerun after Go changes)"
echo "  user $AS_USER / $PASSWORD"
