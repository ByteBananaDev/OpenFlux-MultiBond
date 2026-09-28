#!/usr/bin/env python3
"""Fetch a channel inventory from a local file, HTTP(S) URL, or Yandex Disk public link.

CSV and XLSX are accepted. The first worksheet of XLSX is converted to normalized CSV.
The resulting CSV is written atomically and can be fed to generate-multibond-config.py.
"""
import argparse
import csv
import io
import json
import os
from pathlib import Path
import re
import tempfile
import urllib.parse
import urllib.request
import zipfile
import xml.etree.ElementTree as ET

MAX_BYTES = 8 * 1024 * 1024
REQUIRED = ("name", "type")
CANONICAL = ("name", "type", "priority", "url", "dial", "listen", "token", "uid", "enabled")
YANDEX_HOSTS = {"disk.yandex.ru", "disk.yandex.com", "yadi.sk", "disk.360.yandex.ru", "disk.360.yandex.com"}

def read_limited(resp) -> bytes:
    data = resp.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise ValueError(f"source exceeds {MAX_BYTES} bytes")
    return data

def http_get(url: str) -> tuple[bytes, str]:
    req = urllib.request.Request(url, headers={"User-Agent": "OpenFlux-MultiBond/0.1"})
    with urllib.request.urlopen(req, timeout=20) as resp:
        return read_limited(resp), resp.headers.get("Content-Type", "")

def fetch_source(source: str) -> tuple[bytes, str]:
    p = Path(source)
    if p.exists():
        data = p.read_bytes()
        if len(data) > MAX_BYTES:
            raise ValueError(f"source exceeds {MAX_BYTES} bytes")
        return data, p.suffix.lower()

    parsed = urllib.parse.urlparse(source)
    if parsed.scheme not in ("http", "https"):
        raise ValueError("source must be a local path or http(s) URL")

    if (parsed.hostname or "").lower() in YANDEX_HOSTS:
        api = "https://cloud-api.yandex.net/v1/disk/public/resources/download?" + urllib.parse.urlencode({"public_key": source})
        meta_bytes, _ = http_get(api)
        meta = json.loads(meta_bytes.decode("utf-8"))
        href = meta.get("href")
        if not href:
            raise ValueError("Yandex Disk API did not return a download href; check sharing/download permissions")
        return http_get(href)

    return http_get(source)

def col_index(ref: str) -> int:
    letters = re.match(r"[A-Z]+", ref or "")
    if not letters:
        return 0
    n = 0
    for ch in letters.group(0):
        n = n * 26 + ord(ch) - 64
    return n - 1

def xlsx_rows(data: bytes):
    ns = {
        "m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
        "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
        "pr": "http://schemas.openxmlformats.org/package/2006/relationships",
    }
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        shared = []
        if "xl/sharedStrings.xml" in z.namelist():
            root = ET.fromstring(z.read("xl/sharedStrings.xml"))
            for si in root.findall("m:si", ns):
                shared.append("".join(t.text or "" for t in si.iterfind(".//m:t", ns)))

        wb = ET.fromstring(z.read("xl/workbook.xml"))
        first = wb.find("m:sheets/m:sheet", ns)
        if first is None:
            raise ValueError("XLSX has no worksheets")
        rel_id = first.attrib.get("{%s}id" % ns["r"])
        rels = ET.fromstring(z.read("xl/_rels/workbook.xml.rels"))
        target = None
        for rel in rels.findall("pr:Relationship", ns):
            if rel.attrib.get("Id") == rel_id:
                target = rel.attrib.get("Target")
                break
        if not target:
            raise ValueError("cannot resolve first XLSX worksheet")
        if target.startswith("/"):
            sheet_path = target.lstrip("/")
        else:
            sheet_path = "xl/" + target.lstrip("./")
        root = ET.fromstring(z.read(sheet_path))
        for row in root.findall(".//m:sheetData/m:row", ns):
            vals = []
            for c in row.findall("m:c", ns):
                idx = col_index(c.attrib.get("r", ""))
                while len(vals) <= idx:
                    vals.append("")
                kind = c.attrib.get("t")
                if kind == "inlineStr":
                    node = c.find("m:is", ns)
                    value = "" if node is None else "".join(t.text or "" for t in node.iterfind(".//m:t", ns))
                else:
                    v = c.find("m:v", ns)
                    value = "" if v is None else (v.text or "")
                    if kind == "s" and value:
                        value = shared[int(value)]
                vals[idx] = value
            yield vals

def parse_inventory(data: bytes, hint: str):
    is_xlsx = data[:4] == b"PK\x03\x04" or "xlsx" in hint.lower()
    if is_xlsx:
        rows = list(xlsx_rows(data))
    else:
        text = data.decode("utf-8-sig")
        rows = list(csv.reader(io.StringIO(text)))
    rows = [r for r in rows if any(str(v).strip() for v in r)]
    if not rows:
        raise ValueError("inventory is empty")
    header = [str(x).strip().lower() for x in rows[0]]
    for req in REQUIRED:
        if req not in header:
            raise ValueError(f"inventory is missing required column: {req}")
    out = []
    for raw in rows[1:]:
        item = {header[i]: (str(raw[i]).strip() if i < len(raw) else "") for i in range(len(header))}
        if not item.get("name"):
            continue
        out.append({k: item.get(k, "") for k in CANONICAL})
    if not out:
        raise ValueError("inventory contains no channel rows")
    return out

def write_csv_atomic(path: Path, rows) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmpname = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", newline="", encoding="utf-8") as f:
            w = csv.DictWriter(f, fieldnames=CANONICAL)
            w.writeheader()
            w.writerows(rows)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmpname, path)
    except Exception:
        try:
            os.unlink(tmpname)
        except FileNotFoundError:
            pass
        raise

def main() -> int:
    p = argparse.ArgumentParser(description="Sync MultiBond channel inventory from Yandex Disk/Table, URL, CSV or XLSX")
    p.add_argument("--source", required=True, help="local CSV/XLSX, direct URL, or public Yandex Disk link")
    p.add_argument("--out", default="channels.csv")
    args = p.parse_args()
    data, hint = fetch_source(args.source)
    rows = parse_inventory(data, hint)
    write_csv_atomic(Path(args.out), rows)
    disabled = {"0", "false", "no", "off", "disabled"}
    enabled = sum(1 for r in rows if r.get("enabled", "").strip().lower() not in disabled)
    print(f"wrote {args.out}: {len(rows)} rows, {enabled} enabled")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
