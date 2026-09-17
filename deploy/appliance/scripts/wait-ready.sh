#!/usr/bin/env bash
# Polls GEOCAM_HEALTH_ADDR's /readyz until it returns 200 or a bounded
# number of attempts is exhausted. Used by update.sh/rollback.sh to decide
# whether an activation actually worked, and directly usable by an operator
# after a manual `systemctl start geocam-edge`.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

ENV_FILE="$(root_path "$GEOCAM_CONFIG_DIR")/geocam-edge.env"
ADDR="127.0.0.1:8091" # internal/config.DefaultHealthAddr
if [ -f "$ENV_FILE" ]; then
    configured="$(grep -E '^GEOCAM_HEALTH_ADDR=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
    [ -n "$configured" ] && ADDR="$configured"
fi

ATTEMPTS="${GEOCAM_WAIT_READY_ATTEMPTS:-15}"
SLEEP_SECS="${GEOCAM_WAIT_READY_INTERVAL:-2}"

have_cmd curl || die "curl is required to verify /readyz"

i=0
while [ "$i" -lt "$ATTEMPTS" ]; do
    if curl -fsS -o /dev/null "http://$ADDR/readyz"; then
        exit 0
    fi
    i=$((i + 1))
    sleep "$SLEEP_SECS"
done
exit 1
