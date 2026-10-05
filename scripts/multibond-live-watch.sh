#!/usr/bin/env bash
set -u

SOCKS_ADDR="${1:-127.0.0.1:11080}"
TEST_URL="${2:-https://api.ipify.org}"
COUNT="${3:-120}"
INTERVAL="${4:-1}"
TIMEOUT="${OPENFLUX_LIVE_TIMEOUT:-8}"

if ! command -v curl >/dev/null 2>&1; then
  echo "missing required command: curl" >&2
  exit 2
fi

case "$COUNT" in
  ''|*[!0-9]*) echo "COUNT must be a positive integer" >&2; exit 2 ;;
esac
if (( COUNT < 1 )); then
  echo "COUNT must be >= 1" >&2
  exit 2
fi

ok=0
fail=0

echo "OpenFlux MultiBond live traffic watch"
echo "SOCKS:    $SOCKS_ADDR"
echo "URL:      $TEST_URL"
echo "requests: $COUNT"
echo "interval: ${INTERVAL}s"
echo "timeout:  ${TIMEOUT}s"
echo

for ((i=1; i<=COUNT; i++)); do
  ts="$(date '+%Y-%m-%d %H:%M:%S')"
  if result="$(curl --silent --show-error --max-time "$TIMEOUT"       --socks5-hostname "$SOCKS_ADDR"       --write-out $'\n__OPENFLUX_META__ http=%{http_code} time=%{time_total}'       "$TEST_URL" 2>&1)"; then
    meta="${result##*__OPENFLUX_META__ }"
    body="${result%$'\n'__OPENFLUX_META__*}"
    body="$(printf '%s' "$body" | tr '\n' ' ' | cut -c1-120)"
    ok=$((ok + 1))
    printf '[%s] %03d/%03d OK   %s body=%q\n' "$ts" "$i" "$COUNT" "$meta" "$body"
  else
    rc=$?
    fail=$((fail + 1))
    result="$(printf '%s' "$result" | tr '\n' ' ' | cut -c1-180)"
    printf '[%s] %03d/%03d FAIL rc=%d %s\n' "$ts" "$i" "$COUNT" "$rc" "$result"
  fi

  if (( i < COUNT )); then
    sleep "$INTERVAL"
  fi
done

echo
echo "SUMMARY ok=$ok fail=$fail total=$COUNT"

if (( fail > 0 )); then
  exit 1
fi
