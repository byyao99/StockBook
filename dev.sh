#!/usr/bin/env bash
#
# Start the API and the Vite dev server together, and stop them together.
#
# The two halves are useless apart — the SPA proxies /api to the backend — so
# running them from one terminal is the normal case, and two windows was only
# ever an accident of there being no script for it.
#
#   ./dev.sh              API on :8080, SPA on :5173
#   PORT=8081 ./dev.sh    a different API port
#
# Ctrl-C stops both. So does either one exiting on its own: half a stack is
# worse than none, because the half still up looks like it is working.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

API_PORT="${PORT:-8080}"
WEB_PORT="${WEB_PORT:-5173}"

# Job control, so each background job becomes its own process group leader and
# can be stopped as a whole. Both halves spawn children — npm execs vite — and
# signalling only the parent leaves those children holding the ports, which is
# the failure this script exists to avoid rather than cause.
set -m

# The Go binary is built rather than run through `go run`, and that is not a
# style choice: `go run` compiles to a temporary binary and execs it as a child,
# so the process this script starts is a wrapper and not the server. Building
# means the thing we signal is the thing that holds the port.
# A fixed path rather than mktemp: a run killed outright (SIGKILL, a closed
# laptop) never reaches the cleanup, and a random name each time would leave a
# new stray binary behind on every one of those. One name is overwritten by the
# next run instead.
BIN="${TMPDIR:-/tmp}/stockbook-dev-api"

cleanup() {
  trap - EXIT INT TERM
  echo
  echo "stopping…"
  for pgid in "${API_PID:-}" "${WEB_PID:-}"; do
    # The negative PID is the process group: the job and everything it spawned.
    [ -n "$pgid" ] && kill -TERM -- "-$pgid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -f "$BIN"
}
trap cleanup EXIT INT TERM

# A busy port is the failure worth catching before anything starts: the server
# would otherwise log the bind error and exit, leaving a frontend proxying to
# nothing and the real message scrolled off the top.
for port in "$API_PORT" "$WEB_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $port is already in use — stop what is on it, or set PORT / WEB_PORT" >&2
    exit 1
  fi
done

if [ ! -d frontend/node_modules ]; then
  echo "installing frontend dependencies…"
  (cd frontend && npm install)
fi

echo "building the API…"
go build -o "$BIN" .

# Output is prefixed so one terminal reads as two. awk rather than sed because
# line buffering is spelled differently on the macOS and GNU seds, while
# fflush() is the same everywhere. The prefixing runs *inside* the job, so the
# PID taken below is the job's own and not a pipeline's last stage.
API_PORT="$API_PORT" BIN="$BIN" bash -c '
  PORT="$API_PORT" "$BIN" 2>&1 | awk "{ print \"[api] \" \$0; fflush() }"
' &
API_PID=$!

WEB_PORT="$WEB_PORT" bash -c '
  cd frontend && npm run dev -- --port "$WEB_PORT" 2>&1 |
    awk "{ print \"[web] \" \$0; fflush() }"
' &
WEB_PID=$!

echo "api  http://localhost:$API_PORT"
echo "web  http://localhost:$WEB_PORT"
echo "ctrl-c stops both"

# Wait for either to exit, then let the trap stop the other. `wait -n` says this
# in one line but needs bash 4, and macOS still ships 3.2.
while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WEB_PID" 2>/dev/null; do
  sleep 1
done
