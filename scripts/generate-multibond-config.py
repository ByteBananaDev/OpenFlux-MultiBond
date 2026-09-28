#!/usr/bin/env python3
import argparse
import csv
from pathlib import Path

def q(v: str) -> str:
    return (v or "").strip()

def main() -> int:
    p = argparse.ArgumentParser(description="Generate OpenFlux MultiBond config from channels CSV")
    p.add_argument("--role", choices=("client", "exit"), required=True)
    p.add_argument("--channels", required=True, help="CSV with channel definitions")
    p.add_argument("--out", required=True)
    p.add_argument("--secret", default="secret.txt")
    p.add_argument("--session-context", default="multibond-v01")
    p.add_argument("--bond-max", type=int, default=256)
    p.add_argument("--bond-active", type=int, default=196)
    p.add_argument("--bond-min-active", type=int, default=4)
    p.add_argument("--preferred-rtt", default="120ms")
    p.add_argument("--max-rtt", default="150ms")
    p.add_argument("--emergency-rtt", default="300ms")
    p.add_argument("--rtt-spread", default="30ms")
    p.add_argument("--inbound", default="socks5")
    p.add_argument("--mode", default="l4")
    args = p.parse_args()

    rows = []
    with open(args.channels, newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f):
            if not q(row.get("name", "")):
                continue
            rows.append({k: q(v) for k, v in row.items()})

    if not rows:
        raise SystemExit("no channels found")
    if len(rows) > args.bond_max:
        raise SystemExit(f"{len(rows)} channels exceed --bond-max={args.bond_max}")
    if not (1 <= args.bond_max <= 256):
        raise SystemExit("--bond-max must be 1..256")
    if not (1 <= args.bond_active <= args.bond_max):
        raise SystemExit("--bond-active must be 1..bond-max")
    if not (1 <= args.bond_min_active <= args.bond_active):
        raise SystemExit("--bond-min-active must be 1..bond-active")

    names = [r["name"] for r in rows]
    dup = sorted({n for n in names if names.count(n) > 1})
    if dup:
        raise SystemExit("duplicate channel names: " + ", ".join(dup))

    lines = ["[Interface]", f"Role = {args.role}"]
    if args.role == "client":
        lines.append(f"Inbound = {args.inbound}")
        if args.inbound == "socks5":
            lines.append("Socks5 = :1080")
    else:
        lines.append(f"Mode = {args.mode}")

    lines += [
        f"EncryptionKeyFile = {args.secret}",
        f"SessionContext = {args.session_context}",
        "",
        "Bond = true",
        f"BondMax = {args.bond_max}",
        f"BondActive = {args.bond_active}",
        f"BondMinActive = {args.bond_min_active}",
        f"BondPreferredRTT = {args.preferred_rtt}",
        f"BondMaxRTT = {args.max_rtt}",
        f"BondEmergencyRTT = {args.emergency_rtt}",
        f"BondRTTSpread = {args.rtt_spread}",
        "Debug = 2",
    ]

    for row in rows:
        name = row["name"]
        ty = row.get("type", "")
        if not ty:
            raise SystemExit(f"{name}: missing type")
        priority = row.get("priority") or "50"
        lines += ["", f'[Transport "{name}"]', f"Type = {ty}", f"Priority = {priority}"]

        if ty == "direct":
            key = "dial" if args.role == "client" else "listen"
            value = row.get(key, "")
            if not value:
                raise SystemExit(f"{name}: direct transport requires {key} for role={args.role}")
            lines.append(f"{key.capitalize()} = {value}")
        elif ty == "oneme":
            token = row.get("token", "")
            uid = row.get("uid", "")
            if not token or not uid:
                raise SystemExit(f"{name}: oneme requires token and uid")
            lines += [f"Token = {token}", f"UID = {uid}"]
        else:
            url = row.get("url", "")
            if not url:
                raise SystemExit(f"{name}: {ty} requires url")
            lines.append(f"URL = {url}")

    Path(args.out).write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"wrote {args.out}: {len(rows)} channels")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
