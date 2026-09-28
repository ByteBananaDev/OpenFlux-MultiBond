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
install -m 0600 "$CONFIG_SRC" /etc/openflux-multibond/exit.conf
install -m 0600 "$SECRET_SRC" /etc/openflux-multibond/secret.txt

# Config files generally refer to secret.txt relative to their own deployment
# directory. Make the service use /etc/openflux-multibond as its working dir.
install -d -m 0755 /var/lib/openflux-multibond
install -m 0644 systemd/openflux-multibond.service /etc/systemd/system/openflux-multibond.service

# Ensure the service starts where secret.txt and cookie files resolve.
if ! grep -q '^WorkingDirectory=' /etc/systemd/system/openflux-multibond.service; then
  sed -i '/^Group=root/a WorkingDirectory=/etc/openflux-multibond' /etc/systemd/system/openflux-multibond.service
fi

systemctl daemon-reload
systemctl enable openflux-multibond.service

echo
echo "Installed:"
echo "  binary: /usr/local/bin/openflux-multibond"
echo "  config: /etc/openflux-multibond/exit.conf"
echo "  secret: /etc/openflux-multibond/secret.txt"
echo "  unit:   openflux-multibond.service"

if [[ "$START" == "--start" ]]; then
  systemctl restart openflux-multibond.service
  systemctl --no-pager --full status openflux-multibond.service || true
else
  echo
  echo "Review the config, then start with:"
  echo "  sudo systemctl start openflux-multibond"
fi
