# -*- coding: utf-8 -*-
"""无第三方依赖的 .xlsx 读写（zipfile + 手写 XML / ElementTree 解析）。

写侧 write_dispatch_xlsx 逐字移植自 export_dispatch_orders.py 的 json_to_excel：
列结构与顺序、runState 中文映射、时间格式、样式索引全部保持一致——
该文件格式是壹匣服务端「派车单每日自动同步」的解析契约，禁止改动。
读侧用于「按派车单号导出」应用解析用户提供的单号清单 xlsx。
"""
import re
import zipfile
from datetime import datetime
from pathlib import Path
from xml.etree import ElementTree as ET

# 每日派车单同步的列契约（字段, 表头），顺序固定
DISPATCH_COLUMNS = [
    ('vehicleNumber', '车牌号码'),
    ('licensePlateColor', '车牌颜色'),
    ('driverName', '驾驶员'),
    ('driverTel', '驾驶员电话'),
    ('userName', '用车人'),
    ('planSendTime', '预计用车时间'),
    ('planBackTime', '预计返回时间'),
    ('planUseTime', '预计用车时间(小时)'),
    ('frmAddr', '出发地'),
    ('toAddr', '目的地'),
    ('departName', '用车部门'),
    ('dispatchName', '派车人'),
    ('runCode', '派车单号'),
    ('runState', '派车单状态'),
    ('vehicleType', '车辆类型'),
    ('powerType', '能源类型'),
    ('userTel', '用车人电话'),
    ('useReason', '用车事由'),
    ('remark', '备注'),
    ('dispatchTime', '创建时间'),
]

RUN_STATE_MAP = {'0': '待派车', '1': '已派车', '2': '已完成', '3': '已取消'}

DATE_FIELDS = ('planSendTime', 'planBackTime', 'dispatchTime')


# ---------------------------------------------------------------- 写侧（移植，勿改格式）

def _escape_xml(s):
    """转义 XML 特殊字符，并过滤 XML 1.0 非法控制字符"""
    if not isinstance(s, str):
        s = str(s)
    s = re.sub(r'[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]', '', s)
    return (s.replace("&", "&amp;").replace("<", "&lt;")
             .replace(">", "&gt;").replace('"', "&quot;").replace("'", "&apos;"))


def _int_to_col_letter(col):
    result = ""
    while col > 0:
        col, remainder = divmod(col - 1, 26)
        result = chr(65 + remainder) + result
    return result


def _excel_ref(row, col):
    return "%s%d" % (_int_to_col_letter(col), row)


def write_dispatch_xlsx(data_items, output_path, log=print):
    """生成每日派车单同步 xlsx（格式与旧脚本 json_to_excel 完全一致），返回记录数"""
    columns = DISPATCH_COLUMNS
    num_cols = len(columns)
    num_rows = len(data_items)

    content_types = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>"""

    rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""

    workbook = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<bookPr filterSpecialContents="1"/>
<sheets>
<sheet name="派车单" sheetId="1" r:id="rId1"/>
</sheets>
</workbook>"""

    workbook_rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>"""

    all_strings = []
    string_index = {}

    def get_sst_idx(s):
        if not s:
            return -1  # empty, use <v/> directly
        s = str(s)
        if s not in string_index:
            idx = len(all_strings)
            string_index[s] = idx
            all_strings.append(s)
        return string_index[s]

    styles = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="1">
<numFmt numFmtId="164" formatCode="yyyy-mm-dd h:mm:ss"/>
</numFmts>
<fonts count="2">
<font><name val="微软雅黑"/><family val="2"/><size val="11"/></font>
<font b="1"><name val="微软雅黑"/><family val="2"/><size val="11"/><color rgb="FFFFFFFF"/></font>
</fonts>
<fills count="2">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF4472C4"/></patternFill></fill>
</fills>
<borders count="1">
<border>
<left/><right/><top/><bottom/>
</border>
</borders>
<cellStyleXfs count="1">
<xf/>
</cellStyleXfs>
<cellXfs count="3">
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="1"
    fontId="1" numFmtId="0" xfId="0">
<alignment horizontal="center" textRotation="0" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="0"
    fontId="0" numFmtId="0" xfId="0">
<alignment textRotation="0" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="0"
    fontId="0" numFmtId="164" xfId="0">
<alignment textRotation="0" vertical="center" wrapText="1"/>
</xf>
</cellXfs>
</styleSheet>"""

    sheet_rows = []
    # 表头用 inlineStr（避免 openpyxl 解析问题），不加样式引用
    header_cells = []
    for col_idx, (_, caption) in enumerate(columns, 1):
        ref = _excel_ref(1, col_idx)
        header_cells.append('<c r="%s" s="0" t="inlineStr"><is><t>%s</t></is></c>'
                            % (ref, _escape_xml(caption)))
    sheet_rows.append('<row r="1" spans="1:%d">%s</row>' % (num_cols, "".join(header_cells)))

    for row_idx, item in enumerate(data_items, 2):
        cells = []
        for col_idx, (field, _) in enumerate(columns, 1):
            value = item.get(field, '')
            # 时间字段：epoch 毫秒用本地时区格式化（与「当日」过滤口径一致）
            if field in DATE_FIELDS and value:
                try:
                    if isinstance(value, (int, float)):
                        dt_obj = datetime.fromtimestamp(value / 1000)
                        value = dt_obj.strftime("%Y-%m-%d %H:%M:%S")
                    else:
                        dt_obj = datetime.strptime(str(value)[:19], "%Y-%m-%d %H:%M:%S")
                        value = dt_obj.strftime("%Y-%m-%d %H:%M:%S")
                except (ValueError, TypeError, OSError):
                    value = str(value)
            if field == 'runState':
                value = RUN_STATE_MAP.get(str(value), value)

            ref = _excel_ref(row_idx, col_idx)
            value_str = '' if value is None else str(value)

            if value is None or value == '':
                cells.append('<c r="%s"/>' % ref)
            elif isinstance(value, (int, float)) and field not in DATE_FIELDS:
                cells.append('<c r="%s"><v>%s</v></c>' % (ref, value))
            else:
                si_idx = get_sst_idx(value_str)
                if si_idx >= 0:
                    cells.append('<c r="%s" s="1" t="s"><v>%d</v></c>' % (ref, si_idx))
                else:
                    cells.append('<c r="%s" s="1"><v>%s</v></c>' % (ref, _escape_xml(value_str)))

        sheet_rows.append('<row r="%d" spans="1:%d">%s</row>'
                          % (row_idx, num_cols, "".join(cells)))

    sheet_data = "".join(sheet_rows)

    si_elements = ["<si><t>%s</t></si>" % _escape_xml(s) for s in all_strings]
    sst_content = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" uniqueCount="%d" count="%d">
%s
</sst>""" % (len(all_strings), len(all_strings), "\n".join(si_elements))

    sheet1 = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<dimension ref="A1:%s%d"/>
<sheetViews>
<sheetView workbookViewId="0" tabSelected="1">
<selection activeCell="A2" sqref="A2"/>
</sheetView>
</sheetViews>
<sheetFormatPr baseColWidth="10" defaultColWidth="15" defaultRowHeight="13" x14ac:dyDescent="0.15" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"/>
<cols>
%s
</cols>
<sheetData>
%s
</sheetData>
<pageMargins left="0.7" right="0.7" top="1.08" footer="0.78" header="0.78"/>
<pageSetUpPr fitToPage="1"/>
</worksheet>""" % (_int_to_col_letter(num_cols), num_rows + 1,
                    ''.join('<col collapsed="0" hidden="0" max="%d" min="%d" style="0" width="15"/>'
                            % (c, c) for c in range(1, num_cols + 1)),
                    sheet_data)

    with zipfile.ZipFile(output_path, 'w', zipfile.ZIP_DEFLATED) as zf:
        zf.writestr('[Content_Types].xml', content_types)
        zf.writestr('_rels/.rels', rels)
        zf.writestr('xl/workbook.xml', workbook)
        zf.writestr('xl/_rels/workbook.xml.rels', workbook_rels)
        zf.writestr('xl/styles.xml', styles)
        zf.writestr('xl/sharedStrings.xml', sst_content)
        zf.writestr('xl/worksheets/sheet1.xml', sheet1)

    log("  Excel 文件已保存: %s (%d 条记录)" % (output_path, num_rows))
    return num_rows


def write_table_xlsx(path, sheet_name, headers, rows, log=print):
    """通用单工作表 xlsx 写出（不绑定 write_dispatch_xlsx 的派车单列契约，供各类统计报表用）。
    headers 为表头字符串列表；rows 为与表头等长的值列表——None/'' 出空单元格，
    int/float 出数值单元格，其余按文本（inlineStr）写出；表头加粗居中（s=0）、数据 s=1。
    列宽按表头与内容的显示宽度（CJK 计 2）取 10~30 的缺省值。返回记录数。"""
    num_cols = len(headers)
    num_rows = len(rows)

    content_types = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>"""

    rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""

    workbook = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>
<sheet name="%s" sheetId="1" r:id="rId1"/>
</sheets>
</workbook>""" % _escape_xml(sheet_name)

    workbook_rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>"""

    styles = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><name val="微软雅黑"/><family val="2"/><size val="11"/></font>
<font b="1"><name val="微软雅黑"/><family val="2"/><size val="11"/><color rgb="FFFFFFFF"/></font>
</fonts>
<fills count="2">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF4472C4"/></patternFill></fill>
</fills>
<borders count="1">
<border>
<left/><right/><top/><bottom/>
</border>
</borders>
<cellStyleXfs count="1">
<xf/>
</cellStyleXfs>
<cellXfs count="2">
<xf applyAlignment="1" applyFill="1" applyFont="1" borderId="0" fillId="1"
    fontId="1" numFmtId="0" xfId="0">
<alignment horizontal="center" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" borderId="0" fillId="0" fontId="0" numFmtId="0" xfId="0">
<alignment vertical="center" wrapText="1"/>
</xf>
</cellXfs>
</styleSheet>"""

    def disp_len(v):
        s = "" if v is None else str(v)
        return sum(2 if ord(ch) > 0x7F else 1 for ch in s)

    widths = []
    for c in range(num_cols):
        w = disp_len(headers[c])
        for row in rows:
            if c < len(row):
                w = max(w, disp_len(row[c]))
        widths.append(max(10, min(30, w + 2)))

    sheet_rows = []
    header_cells = []
    for col_idx, caption in enumerate(headers, 1):
        ref = _excel_ref(1, col_idx)
        header_cells.append('<c r="%s" s="0" t="inlineStr"><is><t>%s</t></is></c>'
                            % (ref, _escape_xml(caption)))
    sheet_rows.append('<row r="1" spans="1:%d">%s</row>' % (num_cols, "".join(header_cells)))

    for row_idx, row in enumerate(rows, 2):
        cells = []
        for col_idx in range(1, num_cols + 1):
            value = row[col_idx - 1] if col_idx - 1 < len(row) else None
            ref = _excel_ref(row_idx, col_idx)
            if value is None or value == '':
                cells.append('<c r="%s"/>' % ref)
            elif isinstance(value, (int, float)) and not isinstance(value, bool):
                cells.append('<c r="%s" s="1"><v>%s</v></c>' % (ref, value))
            else:
                cells.append('<c r="%s" s="1" t="inlineStr"><is><t>%s</t></is></c>'
                             % (ref, _escape_xml(value)))
        sheet_rows.append('<row r="%d" spans="1:%d">%s</row>'
                          % (row_idx, num_cols, "".join(cells)))

    sheet1 = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<dimension ref="A1:%s%d"/>
<sheetViews>
<sheetView workbookViewId="0" tabSelected="1">
<selection activeCell="A2" sqref="A2"/>
</sheetView>
</sheetViews>
<sheetFormatPr baseColWidth="10" defaultColWidth="15" defaultRowHeight="13" x14ac:dyDescent="0.15" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"/>
<cols>
%s
</cols>
<sheetData>
%s
</sheetData>
<pageMargins left="0.7" right="0.7" top="1.08" footer="0.78" header="0.78"/>
</worksheet>""" % (_int_to_col_letter(num_cols), num_rows + 1,
                    ''.join('<col collapsed="0" hidden="0" max="%d" min="%d" style="0" width="%d"/>'
                            % (c, c, widths[c - 1]) for c in range(1, num_cols + 1)),
                    "".join(sheet_rows))

    with zipfile.ZipFile(path, 'w', zipfile.ZIP_DEFLATED) as zf:
        zf.writestr('[Content_Types].xml', content_types)
        zf.writestr('_rels/.rels', rels)
        zf.writestr('xl/workbook.xml', workbook)
        zf.writestr('xl/_rels/workbook.xml.rels', workbook_rels)
        zf.writestr('xl/styles.xml', styles)
        zf.writestr('xl/worksheets/sheet1.xml', sheet1)

    log("  Excel 文件已保存: %s (%d 条记录)" % (path, num_rows))
    return num_rows


# ---------------------------------------------------------------- 读侧

def _local(tag: str) -> str:
    """去掉 XML 命名空间，取本地名"""
    return tag.rsplit('}', 1)[-1]


def _col_index(cell_ref: str) -> int:
    """单元格引用（如 B3）的列号，1 起"""
    m = re.match(r"([A-Z]+)", cell_ref or "")
    if not m:
        return 0
    n = 0
    for ch in m.group(1):
        n = n * 26 + (ord(ch) - 64)
    return n


def _read_member(zf: zipfile.ZipFile, name: str) -> bytes:
    """读 zip 成员，带 WPS 怪癖兜底：WPS 的 xlsx 偶见「声明 deflate 实则异常」的成员，
    zipfile 直接抛 zlib Error -3（incorrect header check）；手工按本地文件头取压缩流，
    按 zlib 封装/裸 deflate 轮流试。（已踩坑：见《开发指南》排障速查）"""
    try:
        return zf.read(name)
    except Exception:
        pass
    import zlib
    info = zf.getinfo(name)
    fp = zf.fp
    fp.seek(info.header_offset)
    hdr = fp.read(30)
    if hdr[:4] != b"PK\x03\x04":
        raise RuntimeError("本地文件头异常")
    namelen = int.from_bytes(hdr[26:28], "little")
    extralen = int.from_bytes(hdr[28:30], "little")
    fp.seek(info.header_offset + 30 + namelen + extralen)
    comp = fp.read(info.compress_size)
    for wbits in (15, -15):
        try:
            return zlib.decompress(comp, wbits)
        except zlib.error:
            continue
    raise RuntimeError("成员解压失败")


def read_xlsx_rows(path) -> list:
    """读取第一个工作表，返回行列表（每行为字符串列表，按列对齐，空单元为 ''）。
    文件无效（WPS .et/损坏/非 zip）时抛 RuntimeError 并附中文指引。"""
    path = Path(path)
    guidance = ("清单文件不是有效的 xlsx（可能是 WPS 专有 .et 格式或文件已损坏）。"
                "请用 Excel/WPS「另存为 → .xlsx」，或直接在界面粘贴单号。")
    try:
        with zipfile.ZipFile(path) as zf:
            names = set(zf.namelist())
            # 找第一个工作表：workbook.xml 的第一个 sheet r:id → rels 里的 Target
            sheet_part = "xl/worksheets/sheet1.xml"
            try:
                wb = ET.fromstring(_read_member(zf, "xl/workbook.xml"))
                rid = None
                for el in wb.iter():
                    if _local(el.tag) == "sheet":
                        for k, v in el.attrib.items():
                            if _local(k) == "id":
                                rid = v
                                break
                        break
                if rid:
                    rels = ET.fromstring(_read_member(zf, "xl/_rels/workbook.xml.rels"))
                    for el in rels.iter():
                        if _local(el.tag) == "Relationship" and el.attrib.get("Id") == rid:
                            target = el.attrib.get("Target", "")
                            target = target.lstrip("/")
                            if not target.startswith("xl/"):
                                target = "xl/" + target
                            if target in names:
                                sheet_part = target
                            break
            except Exception:
                pass  # 用默认 sheet1.xml

            # sharedStrings
            shared = []
            if "xl/sharedStrings.xml" in names:
                sst = ET.fromstring(_read_member(zf, "xl/sharedStrings.xml"))
                for si in sst.iter():
                    if _local(si.tag) != "si":
                        continue
                    text = "".join(t.text or "" for t in si.iter() if _local(t.tag) == "t")
                    shared.append(text)

            sheet = ET.fromstring(_read_member(zf, sheet_part))
    except RuntimeError:
        raise RuntimeError(guidance)
    except (zipfile.BadZipFile, KeyError, ET.ParseError, OSError) as e:
        raise RuntimeError("%s（%s）" % (guidance, e))

    rows = []
    for row_el in sheet.iter():
        if _local(row_el.tag) != "row":
            continue
        cells = {}
        max_col = 0
        for c in row_el:
            if _local(c.tag) != "c":
                continue
            ref = c.attrib.get("r", "")
            col = _col_index(ref) or (max_col + 1)
            max_col = max(max_col, col)
            ctype = c.attrib.get("t", "")
            value = ""
            if ctype == "inlineStr":
                value = "".join(t.text or "" for t in c.iter() if _local(t.tag) == "t")
            else:
                v = None
                for child in c:
                    if _local(child.tag) == "v":
                        v = child.text
                        break
                if v is not None:
                    if ctype == "s":
                        try:
                            value = shared[int(v)]
                        except (ValueError, IndexError):
                            value = v
                    else:
                        value = v
            cells[col] = value
        rows.append([cells.get(i, "") for i in range(1, max_col + 1)])
    return rows


# 表头候选（识别「派车单号」列）
_CODE_HEADERS = ("派车单号", "runcode", "单号")
_DATE_HEADERS = ("预计用车时间", "plansendtime", "用车时间")


def extract_run_codes(path) -> list:
    """从 xlsx 提取派车单号清单。优先按表头「派车单号」定位列，否则取第一列；
    同时带出「预计用车时间」作为查询日期提示。保持行序、去重（保首次出现）。
    返回 [{seq, code, date_hint}]"""
    rows = read_xlsx_rows(path)
    return extract_codes_from_rows(rows)


def extract_codes_from_rows(rows: list) -> list:
    code_col, date_col, start = 0, None, 0
    for r_idx, row in enumerate(rows[:5]):
        for c_idx, cell in enumerate(row):
            text = (cell or "").strip().lower()
            if not text:
                continue
            if any(h in text for h in _CODE_HEADERS):
                code_col = c_idx
                # 同一行再找日期提示列
                for c2, cell2 in enumerate(row):
                    t2 = (cell2 or "").strip().lower()
                    if any(h in t2 for h in _DATE_HEADERS):
                        date_col = c2
                        break
                start = r_idx + 1
                break
        if start:
            break
    out, seen = [], set()
    for row in rows[start:]:
        code = (row[code_col] if code_col < len(row) else "").strip()
        if not code or code in seen:
            continue
        # 无表头模式下第一行若像标题（含中文「单号」字样）则跳过
        if start == 0 and not out and any(h in code.lower() for h in _CODE_HEADERS):
            continue
        seen.add(code)
        date_hint = None
        if date_col is not None and date_col < len(row):
            m = re.search(r"\d{4}-\d{2}-\d{2}", row[date_col] or "")
            if m:
                date_hint = m.group(0)
        out.append({"seq": len(out) + 1, "code": code, "date_hint": date_hint})
    return out


def parse_codes_text(text: str) -> list:
    """解析粘贴的单号文本（每行一个；兼容逗号/空白分隔），保持顺序去重"""
    out, seen = [], set()
    for token in re.split(r"[\s,，;；]+", text or ""):
        code = token.strip()
        if code and code not in seen:
            seen.add(code)
            out.append({"seq": len(out) + 1, "code": code, "date_hint": None})
    return out
