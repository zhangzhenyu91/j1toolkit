# -*- coding: utf-8 -*-
"""派车单号清单 xlsx 读侧契约测试（Task 1）。

附件样表：表头「派车单号 / 用车日期 / 记录ID」，12 条记录。
"""

import sys
import tempfile
import unittest
import zipfile
from pathlib import Path
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from toolkit.xlsx_util import (
    extract_codes_from_rows,
    extract_run_codes,
    parse_codes_text,
    write_table_xlsx,
)

ATTACHMENT_HEADERS = ["派车单号", "用车日期", "记录ID"]

ATTACHMENT_ROWS = [
    ("A-PC-Lsx2026100200119", "2026-10-02"),
    ("A-PC-Lsx2026100200126", "2026-10-02"),
    ("A-PC-Lsx2026100300080", "2026-10-03"),
    ("A-PC-Lsx2026100300085", "2026-10-03"),
    ("A-PC-Lsx2026100400093", "2026-10-04"),
    ("A-PC-Lsx2026100500093", "2026-10-05"),
    ("A-PC-Lsx2026100800079", "2026-10-08"),
    ("A-PC-Lsx2026100800077", "2026-10-08"),
    ("A-PC-Lsx2026100900501", "2026-10-09"),
    ("A-PC-Lsx2026100900499", "2026-10-09"),
    ("A-PC-Lsx2026101000134", "2026-10-10"),
    ("A-PC-Lsx2026101000131", "2026-10-10"),
]


def _write_shared_strings_xlsx(path, headers, rows):
    """按真实附件风格（t="s" + sharedStrings.xml）手工生成最小 xlsx。"""
    shared, index = [], {}

    def sidx(value):
        if value not in index:
            index[value] = len(shared)
            shared.append(value)
        return index[value]

    def cell(ref, value):
        if value == "":
            return '<c r="%s"/>' % ref
        return '<c r="%s" t="s"><v>%d</v></c>' % (ref, sidx(value))

    def row_xml(row_idx, values):
        cells = "".join(
            cell("%s%d" % (chr(64 + c), row_idx), value)
            for c, value in enumerate(values, 1)
        )
        return '<row r="%d">%s</row>' % (row_idx, cells)

    sheet_rows = [row_xml(1, headers)] + [
        row_xml(r, row) for r, row in enumerate(rows, 2)
    ]
    content_types = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>"""
    rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""
    workbook = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>
<sheet name="派车汇总" sheetId="1" r:id="rId1"/>
</sheets>
</workbook>"""
    workbook_rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>"""
    sst = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="%d" uniqueCount="%d">
%s
</sst>""" % (
        len(shared),
        len(shared),
        "".join("<si><t>%s</t></si>" % escape(s) for s in shared),
    )
    sheet = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<sheetData>%s</sheetData>
</worksheet>""" % "".join(sheet_rows)

    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("[Content_Types].xml", content_types)
        zf.writestr("_rels/.rels", rels)
        zf.writestr("xl/workbook.xml", workbook)
        zf.writestr("xl/_rels/workbook.xml.rels", workbook_rels)
        zf.writestr("xl/sharedStrings.xml", sst)
        zf.writestr("xl/worksheets/sheet1.xml", sheet)


class XlsxPcdTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)

    def _write(self, rows, headers=ATTACHMENT_HEADERS, name="pcd.xlsx"):
        path = self.tmpdir / name
        write_table_xlsx(path, "派车汇总", headers, rows, log=lambda *_: None)
        return path

    def _assert_attachment(self, result):
        self.assertEqual(len(result), 12)
        self.assertEqual([item["seq"] for item in result], list(range(1, 13)))
        self.assertEqual(
            [item["code"] for item in result],
            [code for code, _ in ATTACHMENT_ROWS],
        )
        self.assertEqual(
            [item["date_hint"] for item in result],
            [date for _, date in ATTACHMENT_ROWS],
        )

    def test_attachment_shape_十二单号与日期提示(self):
        rows = [[code, date, ""] for code, date in ATTACHMENT_ROWS]
        path = self._write(rows)

        self._assert_attachment(extract_run_codes(path))

    def test_sharedStrings_附件同形解析(self):
        """真实附件使用 t="s" + sharedStrings.xml，此处表征既有读取分支。"""
        rows = [[code, date, ""] for code, date in ATTACHMENT_ROWS]
        path = self.tmpdir / "pcd-shared.xlsx"
        _write_shared_strings_xlsx(path, ATTACHMENT_HEADERS, rows)

        self._assert_attachment(extract_run_codes(path))

    def test_去重保序且日期提示保留首次出现(self):
        rows = [
            ["A-PC-Lsx001", "2026-10-01", ""],
            ["A-PC-Lsx002", "2026-10-01", ""],
            ["A-PC-Lsx001", "2026-10-02", ""],
            ["A-PC-Lsx003", "2026-10-03", ""],
            ["A-PC-Lsx002", "2026-10-04", ""],
        ]

        result = extract_codes_from_rows([ATTACHMENT_HEADERS] + rows)

        self.assertEqual(
            [item["code"] for item in result],
            ["A-PC-Lsx001", "A-PC-Lsx002", "A-PC-Lsx003"],
        )
        self.assertEqual([item["seq"] for item in result], [1, 2, 3])
        self.assertEqual(
            [item["date_hint"] for item in result],
            ["2026-10-01", "2026-10-01", "2026-10-03"],
        )

    def test_粘贴文本解析保序去重(self):
        text = "A-PC-Lsx001\nA-PC-Lsx002，A-PC-Lsx001;A-PC-Lsx003\n\n A-PC-Lsx004 \nA-PC-Lsx004"

        result = parse_codes_text(text)

        self.assertEqual(
            [item["code"] for item in result],
            ["A-PC-Lsx001", "A-PC-Lsx002", "A-PC-Lsx003", "A-PC-Lsx004"],
        )
        self.assertEqual([item["seq"] for item in result], [1, 2, 3, 4])
        self.assertEqual([item["date_hint"] for item in result], [None] * 4)


if __name__ == "__main__":
    unittest.main()
