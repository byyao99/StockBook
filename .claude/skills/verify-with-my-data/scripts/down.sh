#!/usr/bin/env bash
#
# Stop what up.sh started and remove what it created — by name, never with a
# recursive delete, since E2E_DIR is whatever the caller pointed it at.

set -uo pipefail

RUN="${E2E_DIR:-${TMPDIR:-/tmp}/stockbook-e2e}"

if [ ! -f "$RUN/env" ]; then
  echo "nothing up in $RUN"
  exit 0
fi
# shellcheck disable=SC1091
. "$RUN/env"

kill -TERM "$API_PID" 2>/dev/null
# The negative id is the process group: npx and the Vite it spawned.
kill -TERM -- "-$WEB_PGID" 2>/dev/null

for _ in $(seq 1 20); do
  kill -0 "$API_PID" 2>/dev/null || kill -0 -- "-$WEB_PGID" 2>/dev/null || break
  sleep 0.25
done

rm -f "$RUN/env" "$RUN/api" "$RUN/api.log" "$RUN/web.log" \
  "$RUN/book.db" "$RUN/book.db-wal" "$RUN/book.db-shm"
rm -f "$RUN"/shot-*.png
rmdir "$RUN" 2>/dev/null || echo "left $RUN in place: it holds files this did not create"
echo "down"
