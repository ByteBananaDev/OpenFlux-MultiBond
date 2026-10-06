#!/usr/bin/env python3
import csv
import importlib.util
from pathlib import Path
import tempfile
import zipfile

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("sync_yandex_channels", HERE / "sync-yandex-channels.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

def make_xlsx(path: Path):
    workbook = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Channels" sheetId="1" r:id="rId1"/></sheets></workbook>'''
    rels = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>'''
    sheet = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>name</t></is></c><c r="B1" t="inlineStr"><is><t>type</t></is></c><c r="C1" t="inlineStr"><is><t>url</t></is></c><c r="D1" t="inlineStr"><is><t>enabled</t></is></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>volga-01</t></is></c><c r="B2" t="inlineStr"><is><t>vyandex</t></is></c><c r="C2" t="inlineStr"><is><t>https://example.invalid/1</t></is></c><c r="D2"><v>1</v></c></row>
<row r="3"><c r="A3" t="inlineStr"><is><t>volga-off</t></is></c><c r="B3" t="inlineStr"><is><t>vyandex</t></is></c><c r="C3" t="inlineStr"><is><t>https://example.invalid/2</t></is></c><c r="D3"><v>0</v></c></row>
</sheetData></worksheet>'''
    with zipfile.ZipFile(path, "w") as z:
        z.writestr("xl/workbook.xml", workbook)
        z.writestr("xl/_rels/workbook.xml.rels", rels)
        z.writestr("xl/worksheets/sheet1.xml", sheet)

def main():
    with tempfile.TemporaryDirectory() as td:
        td = Path(td)
        csv_path = td / "channels.csv"
        csv_path.write_text(
            "name,type,url,enabled\n"
            "volga-01,vyandex,https://example.invalid/1,true\n"
            "volga-off,vyandex,https://example.invalid/2,false\n",
            encoding="utf-8",
        )
        data, hint = mod.fetch_source(str(csv_path))
        rows = mod.parse_inventory(data, hint)
        assert len(rows) == 2 and rows[0]["name"] == "volga-01"

        # Exercise the Yandex public-link resolver without external network.
        real_http_get = mod.http_get
        calls = []
        def fake_http_get(url):
            calls.append(url)
            if url.startswith("https://cloud-api.yandex.net/v1/disk/public/resources/download?"):
                return b'{"href":"https://download.invalid/channels.csv"}', "application/json"
            if url == "https://download.invalid/channels.csv":
                return csv_path.read_bytes(), "text/csv"
            raise AssertionError(url)
        mod.http_get = fake_http_get
        try:
            data, hint = mod.fetch_source("https://disk.yandex.ru/i/test-public-key")
            rows = mod.parse_inventory(data, hint)
            assert rows[0]["name"] == "volga-01"
            assert len(calls) == 2 and "public_key=" in calls[0]
        finally:
            mod.http_get = real_http_get

        xlsx_path = td / "channels.xlsx"
        make_xlsx(xlsx_path)
        data, hint = mod.fetch_source(str(xlsx_path))
        rows = mod.parse_inventory(data, hint)
        assert len(rows) == 2 and rows[1]["enabled"] == "0"

        out = td / "normalized.csv"
        assert mod.write_csv_atomic(out, rows) is True
        assert mod.write_csv_atomic(out, rows) is False
        with out.open(newline="", encoding="utf-8") as f:
            parsed = list(csv.DictReader(f))
        assert parsed[0]["type"] == "vyandex"
        assert parsed[1]["enabled"] == "0"

    print("OK: channel sync CSV/XLSX tests passed")

if __name__ == "__main__":
    main()
