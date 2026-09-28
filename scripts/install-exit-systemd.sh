#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root: sudo $0 <exit.conf> <secret.txt> [--start]" >&2
  exit 1
fi

CONFIG_SRC="${1:-}"
SECRET_SRC="${2:-}"
START="${3:-}"

if [[ -z "$CONFIG_SRC" || -z "$SECRET_SRC" ]]; then
  echo "usage: sudo $0 <exit.conf> <secret.txt> [--start]" >&2
  exit 1
fi
if [[ ! -f "$CONFIG_SRC" ]]; then
  echo "config not found: $CONFIG_SRC" >&2
  exit 1
fi
if [[ ! -f "$SECRET_SRC" ]]; then
  echo "secret not found: $SECRET_SRC" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build OpenFlux (go.mod currently requires Go 1.26.4)." >&2
  exit 1
fi

echo "== building =="
go build -trimpath -o openflux .

echo "== installing =="
install -m 0755 openflux /usr/local/bin/openflux-multibond
install -d -m 0700 /etc/openflux-multibond
install -d -m 0700 /var/lib/openflux-multibond
install -m 0600 "$CONFIG_SRC" /etc/openflux-multibond/exit.conf
install -m 0600 "$SECRET_SRC" /var/lib/openflux-multibond/secret.txt

# The service starts in /var/lib/openflux-multibond, so normalize the
# installed config to the secret copied above regardless of the source path.
if grep -q '^[[:space:]]*EncryptionKeyFile[[:space:]]*=' /etc/openflux-multibond/exit.conf; then
  sed -i -E 's|^[[:space:]]*EncryptionKeyFile[[:space:]]*=.*$|EncryptionKeyFile = secret.txt|' /etc/openflux-multibond/exit.conf
else
  echo "EncryptionKeyFile = secret.txt" >> /etc/openflux-multibond/exit.conf
fi

install -m 0644 systemd/openflux-multibond.service /etc/systemd/system/openflux-multibond.service

systemctl daemon-reload
systemctl enable openflux-multibond.service

echo
echo "Installed:"
echo "  binary: /usr/local/bin/openflux-multibond"
echo "  config: /etc/openflux-multibond/exit.conf"
echo "  secret: /var/lib/openflux-multibond/secret.txt"
echo "  unit:   openflux-multibond.service"

if [[ "$START" == "--start" ]]; then
  systemctl restart openflux-multibond.service
  systemctl --no-pager --full status openflux-multibond.service || true
else
  echo
  echo "Review the config, then start with:"
  echo "  sudo systemctl start openflux-multibond"
fi
