#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}
need go
need curl
need python3

TMP="$(mktemp -d)"
EXIT_PID=""
CLIENT_PID=""
HTTP_PID=""
cleanup() {
  set +e
  [[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null
  [[ -n "$EXIT_PID" ]] && kill "$EXIT_PID" 2>/dev/null
  [[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null
  wait "$CLIENT_PID" "$EXIT_PID" "$HTTP_PID" 2>/dev/null
  if [[ "${KEEP_MULTIBOND_E2E_TMP:-0}" != "1" ]]; then
    rm -rf "$TMP"
  else
    echo "kept temp dir: $TMP"
  fi
}
trap cleanup EXIT INT TERM

BIN="$TMP/openflux"
echo "== build =="
go build -trimpath -o "$BIN" .

printf 'local-e2e-secret-0123456789-abcdefghijklmnopqrstuvwxyz\n' > "$TMP/secret.txt"
chmod 600 "$TMP/secret.txt"

mkdir -p "$TMP/www"
TOKEN="openflux-multibond-e2e-ok"
printf '%s\n' "$TOKEN" > "$TMP/www/token.txt"

cat > "$TMP/exit.conf" <<EOF
[Interface]
Role = exit
Mode = l4
EncryptionKeyFile = $TMP/secret.txt
SessionContext = multibond-local-e2e
Bond = true
BondMax = 8
BondActive = 2
BondMinActive = 1
BondPreferredRTT = 120ms
BondMaxRTT = 150ms
BondEmergencyRTT = 300ms
BondRTTSpread = 30ms
Debug = 2

[Transport "direct-01"]
Type = direct
Priority = 50
Listen = 127.0.0.1:18445

[Transport "direct-02"]
Type = direct
Priority = 50
Listen = 127.0.0.1:18446
EOF

cat > "$TMP/client.conf" <<EOF
[Interface]
Role = client
Inbound = socks5
Socks5 = 127.0.0.1:11080
EncryptionKeyFile = $TMP/secret.txt
SessionContext = multibond-local-e2e
Bond = true
BondMax = 8
BondActive = 2
BondMinActive = 1
BondPreferredRTT = 120ms
BondMaxRTT = 150ms
BondEmergencyRTT = 300ms
BondRTTSpread = 30ms
Debug = 2

[Transport "direct-01"]
Type = direct
Priority = 50
Dial = 127.0.0.1:18445

[Transport "direct-02"]
Type = direct
Priority = 50
Dial = 127.0.0.1:18446
EOF

echo "== local target HTTP server =="
python3 -m http.server 18080 --bind 127.0.0.1 --directory "$TMP/www" >"$TMP/http.log" 2>&1 &
HTTP_PID=$!

echo "== exit =="
"$BIN" --config="$TMP/exit.conf" >"$TMP/exit.log" 2>&1 &
EXIT_PID=$!

# Give listeners a moment to bind before the client begins its dial loop.
sleep 0.5

echo "== client =="
"$BIN" --config="$TMP/client.conf" >"$TMP/client.log" 2>&1 &
CLIENT_PID=$!

echo "== tunnel request =="
result=""
for _ in $(seq 1 40); do
  result="$(env -u NO_PROXY -u no_proxy curl --silent --show-error --max-time 2     --noproxy "" --socks5-hostname 127.0.0.1:11080     http://127.0.0.1:18080/token.txt 2>/dev/null || true)"
  if [[ "$result" == "$TOKEN" ]]; then
    break
  fi
  sleep 0.5
done

if [[ "$result" != "$TOKEN" ]]; then
  echo "E2E request failed" >&2
  echo "---- exit.log ----" >&2
  tail -120 "$TMP/exit.log" >&2 || true
  echo "---- client.log ----" >&2
  tail -120 "$TMP/client.log" >&2 || true
  exit 1
fi

echo "== bond pool =="
pool_ok=0
for _ in $(seq 1 20); do
  if grep -Eq '\[BOND\] pool total=2 active=2 reserve=0 failed=0' "$TMP/client.log"; then
    pool_ok=1
    break
  fi
  sleep 0.5
done

if [[ "$pool_ok" != "1" ]]; then
  echo "Tunnel worked, but 2-channel ACTIVE pool was not observed" >&2
  echo "---- client.log ----" >&2
  tail -160 "$TMP/client.log" >&2 || true
  exit 1
fi

echo "OK: HTTP crossed SOCKS5 -> MultiBond Session -> 2 direct carriers -> L4 exit"
echo "OK: scheduler reported total=2 active=2"
