#!/usr/bin/env bash
set -euo pipefail

CONFIG="${1:-/etc/openflux-multibond/exit.conf}"
BIN="${OPENFLUX_BIN:-/usr/local/bin/openflux-multibond}"

fail=0
ok()   { printf '[ OK ] %s\n' "$*"; }
warn() { printf '[WARN] %s\n' "$*"; }
bad()  { printf '[FAIL] %s\n' "$*"; fail=1; }

echo "OpenFlux MultiBond doctor"
echo "config: $CONFIG"
echo "binary: $BIN"
echo

if [[ -x "$BIN" ]]; then
  ok "binary exists"
else
  bad "binary is missing or not executable"
fi

if [[ -f "$CONFIG" ]]; then
  ok "config exists"
else
  bad "config missing"
  exit 1
fi

channels="$(grep -Ec '^\[Transport([[:space:]]|")' "$CONFIG" || true)"
bond_max="$(awk -F= '/^[[:space:]]*BondMax[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); print $2; exit}' "$CONFIG")"
bond_active="$(awk -F= '/^[[:space:]]*BondActive[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); print $2; exit}' "$CONFIG")"
bond_max="${bond_max:-256}"
bond_active="${bond_active:-196}"

if (( channels > 0 )); then
  ok "transport sections: $channels"
else
  bad "no [Transport ...] sections found"
fi

if (( bond_max >= 1 && bond_max <= 256 )); then
  ok "BondMax=$bond_max"
else
  bad "BondMax must be 1..256 (got $bond_max)"
fi

if (( bond_active >= 1 && bond_active <= bond_max )); then
  ok "BondActive=$bond_active"
else
  bad "BondActive must be 1..BondMax (got $bond_active)"
fi

if (( channels > bond_max )); then
  bad "$channels transports exceed BondMax=$bond_max"
fi

secret="$(awk -F= '/^[[:space:]]*EncryptionKeyFile[[:space:]]*=/{sub(/^[[:space:]]*/,"",$2); sub(/[[:space:]]*$/,"",$2); print $2; exit}' "$CONFIG")"
if [[ -n "$secret" ]]; then
  if [[ "$secret" != /* ]]; then
    secret="$(dirname "$CONFIG")/$secret"
  fi
  if [[ -f "$secret" ]]; then
    ok "encryption key exists"
    mode="$(stat -c '%a' "$secret" 2>/dev/null || stat -f '%Lp' "$secret" 2>/dev/null || true)"
    if [[ "$mode" == "600" || "$mode" == "400" ]]; then
      ok "encryption key permissions: $mode"
    else
      warn "encryption key permissions are $mode; 600 is recommended"
    fi
  else
    bad "EncryptionKeyFile not found: $secret"
  fi
else
  bad "EncryptionKeyFile is not set"
fi

dups="$(sed -n 's/^\[Transport[[:space:]]*"\([^"]*\)"\].*/\1/p' "$CONFIG" | sort | uniq -d)"
if [[ -n "$dups" ]]; then
  bad "duplicate transport names: $dups"
else
  ok "transport names are unique"
fi

while IFS= read -r port; do
  [[ -z "$port" ]] && continue
  if command -v ss >/dev/null 2>&1 && ss -ltn | awk '{print $4}' | grep -Eq "[:.]$port$"; then
    ok "TCP port $port is listening"
  else
    warn "TCP port $port is not listening yet"
  fi
done < <(awk -F= '/^[[:space:]]*Listen[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); n=split($2,a,":"); print a[n]}' "$CONFIG")

if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files openflux-multibond.service >/dev/null 2>&1; then
  if systemctl is-active --quiet openflux-multibond.service; then
    ok "systemd service is active"
  else
    warn "systemd service is installed but not active"
  fi
fi

echo
if (( fail )); then
  echo "Doctor found blocking problems."
  exit 1
fi
echo "Doctor found no blocking configuration problems."
