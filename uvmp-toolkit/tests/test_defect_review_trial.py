# -*- coding: utf-8 -*-
"""缺陷识别自动审核脚本测试。

全部使用假输入与合成响应，不启动 Playwright、不连接内网。
覆盖筛选配置与响应解析（Task 1）、浏览器流程与候选迭代（Task 2），
以及图片加载重试、响应计数、提交保护和跳过/取消收尾（Task 3）。
"""

import argparse
import base64
import contextlib
import gc
import io
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import warnings
import zipfile
import zlib
from pathlib import Path
from unittest import mock
from urllib.parse import urlencode
from xml.etree import ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

import defect_review_trial as drt  # noqa: E402


def make_png_rgba(width, height, rows):
    """生成无滤波的 8 位 RGBA PNG，供截图回退测试使用。"""
    raw = b"".join(b"\x00" + bytes(row) for row in rows)

    def chunk(kind, data):
        return (
            struct.pack(">I", len(data))
            + kind
            + data
            + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
        )

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", ihdr)
        + chunk(b"IDAT", zlib.compress(raw))
        + chunk(b"IEND", b"")
    )


def read_xlsx_rows(path, sheet_name=None):
    """读取脚本写出的内联字符串 xlsx；默认第一个工作表，可按名称选择。"""
    namespace = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}
    rel_ns = {
        "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
    }
    with zipfile.ZipFile(path) as archive:
        sheet_path = "xl/worksheets/sheet1.xml"
        if sheet_name:
            workbook = ET.fromstring(archive.read("xl/workbook.xml"))
            rels = ET.fromstring(archive.read("xl/_rels/workbook.xml.rels"))
            targets = {
                rel.get("Id"): rel.get("Target")
                for rel in rels
                if rel.get("Id") and rel.get("Target")
            }
            for sheet in workbook.findall(".//m:sheets/m:sheet", namespace):
                if sheet.get("name") != sheet_name:
                    continue
                rid = sheet.get("{%s}id" % rel_ns["r"])
                target = targets.get(rid, "")
                if target:
                    sheet_path = (
                        "xl/" + target.lstrip("/")
                        if not target.startswith("xl/")
                        else target
                    )
                break
        sheet = ET.fromstring(archive.read(sheet_path))
    rows = []
    for row in sheet.findall(".//m:sheetData/m:row", namespace):
        values = []
        for cell in row.findall("m:c", namespace):
            if cell.get("t") == "inlineStr":
                node = cell.find("m:is/m:t", namespace)
            else:
                node = cell.find("m:v", namespace)
            values.append(node.text if node is not None and node.text else "")
        rows.append(values)
    return rows


def build_styled_workbook(path, sheets):
    """手工拼一个带样式/公式/合并单元格/数字格式/空表的 xlsx，用于校验只改 A 列。

    sheets: [(工作表名, {"before": ..., "after": ...}, sheetData 内容)]，
    sheetData 里已包含用户自定义的样式索引、公式与合并单元格。
    """
    rel_ns = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
    namespace = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr(
            "[Content_Types].xml",
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
            '<Default Extension="rels" '
            'ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
            '<Default Extension="xml" ContentType="application/xml"/>'
            '<Override PartName="/xl/workbook.xml" '
            'ContentType="application/vnd.openxmlformats-officedocument.'
            'spreadsheetml.sheet.main+xml"/>'
            + "".join(
                '<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/'
                'vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' % (i + 1)
                for i in range(len(sheets))
            )
            + '<Override PartName="/xl/styles.xml" ContentType="application/vnd.'
            'openxmlformats-officedocument.spreadsheetml.styles+xml"/>'
            "</Types>",
        )
        archive.writestr(
            "_rels/.rels",
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/'
            'relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.'
            'org/officeDocument/2006/relationships/officeDocument" '
            'Target="xl/workbook.xml"/></Relationships>',
        )
        archive.writestr(
            "xl/workbook.xml",
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<workbook xmlns="%s" xmlns:r="%s"><sheets>%s</sheets></workbook>'
            % (
                namespace,
                rel_ns,
                "".join(
                    '<sheet name="%s" sheetId="%d" r:id="rId%d"/>'
                    % (name, index + 1, index + 1)
                    for index, (name, _extra, _data) in enumerate(sheets)
                ),
            ),
        )
        archive.writestr(
            "xl/_rels/workbook.xml.rels",
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/'
            'relationships">%s<Relationship Id="rId%d" Type="http://schemas.'
            'openxmlformats.org/officeDocument/2006/relationships/styles" '
            'Target="styles.xml"/></Relationships>'
            % (
                "".join(
                    '<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/'
                    'officeDocument/2006/relationships/worksheet" '
                    'Target="worksheets/sheet%d.xml"/>' % (index + 1, index + 1)
                    for index in range(len(sheets))
                ),
                len(sheets) + 1,
            ),
        )
        archive.writestr(
            "xl/styles.xml",
            '<?xml version="1.0" encoding="UTF-8"?>'
            '<styleSheet xmlns="%s"><numFmts count="1">'
            '<numFmt numFmtId="164" formatCode="yyyy\\-mm\\-dd"/>'
            "</numFmts><fonts count=\"1\"><font><sz val=\"11\"/></font></fonts>"
            '<fills count="1"><fill><patternFill patternType="none"/></fill></fills>'
            '<borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs>'
            '<cellXfs count="2"><xf/><xf numFmtId="164" applyNumberFormat="1"/></cellXfs>'
            "</styleSheet>" % namespace,
        )
        for index, (_name, extra, data) in enumerate(sheets):
            archive.writestr(
                "xl/worksheets/sheet%d.xml" % (index + 1),
                '<?xml version="1.0" encoding="UTF-8"?>'
                '<worksheet xmlns="%s">%s<sheetData>%s</sheetData>%s</worksheet>'
                % (namespace, extra.get("before", ""), data, extra.get("after", "")),
            )


TASK_STATUSES = (
    "图片未上传",
    "图片上传中",
    "算法识别中",
    "待人工审核",
    "任务完成",
    "任务已终止",
    "审核完成",
    "审核驳回",
    "工单终止",
    "图片转换中",
)
MISSION_TYPES = (
    "线路交跨巡检",
    "精细化巡检",
    "应急巡检",
    "正射巡检",
    "红外巡检",
    "固定翼巡检",
    "通道巡检",
    "树障巡检",
    "特殊巡视",
    "仿线巡检",
    "联合巡视",
)
VOLTAGE_LEVELS = (
    "交流35kV",
    "交流110kV",
    "交流220kV",
    "交流500kV",
    "交流1000kV",
    "±800kV地",
    "直流500kV",
    "直流660kV",
    "直流800kV",
    "直流1000kV",
)


def make_args(**overrides):
    """构造全部筛选项为空的参数对象，线路和日期默认已提供。"""
    values = {
        "task_status": None,
        "mission_type": None,
        "voltage_level": None,
        "line_name": "测试线路",
        "start_date": "2026-11-01",
        "end_date": "2026-11-02",
    }
    values.update(overrides)
    return argparse.Namespace(**values)


def make_fixed_args(**overrides):
    """三项枚举已提供，便于只测试线路/日期交互。"""
    values = {
        "task_status": "待人工审核",
        "mission_type": "精细化巡检",
        "voltage_level": "交流220kV",
    }
    values.update(overrides)
    return make_args(**values)


def no_input(prompt):
    raise AssertionError("不应触发交互输入：%s" % prompt)


class FakeInput:
    def __init__(self, answers):
        self.answers = list(answers)
        self.prompts = []

    def __call__(self, prompt):
        self.prompts.append(prompt)
        if not self.answers:
            raise EOFError("测试输入已用尽")
        return self.answers.pop(0)


def ok_payload(data, msg="操作成功"):
    return {"code": 200, "success": True, "data": data, "msg": msg}


def record(guid, name="测试任务", line="测试线路"):
    return {
        "recognitionTaskGuid": guid,
        "taskName": name,
        "taskStatus": "待人工审核",
        "recognitionTaskStatus": "manual_review",
        "missionType": "fine_inspect",
        "missionTypeName": "精细化巡检",
        "voltageLevel": "交流220kV",
        "lineName": line,
        "lineGuid": "line-guid",
        "lineTowerName": [line + "：#1"] if line else [],
        "createTime": "2026-11-01 08:00:00",
    }


class SettingsDefaultsTest(unittest.TestCase):
    def test_三项缺省交互留空采用固定默认值(self):
        inp = FakeInput(["", "", ""])
        settings = drt.parse_settings(make_args(), input_fn=inp)

        self.assertEqual("待人工审核", settings.task_status)
        self.assertEqual("精细化巡检", settings.mission_type)
        self.assertEqual("交流220kV", settings.voltage_level)
        self.assertEqual(3, len(inp.prompts))

    def test_三项提示包含全部选项和默认值(self):
        inp = FakeInput(["", "", ""])
        drt.parse_settings(make_args(), input_fn=inp)

        for value in TASK_STATUSES:
            self.assertIn(value, inp.prompts[0])
        self.assertIn("待人工审核", inp.prompts[0])
        for value in MISSION_TYPES:
            self.assertIn(value, inp.prompts[1])
        self.assertIn("精细化巡检", inp.prompts[1])
        for value in VOLTAGE_LEVELS:
            self.assertIn(value, inp.prompts[2])
        self.assertIn("交流220kV", inp.prompts[2])

    def test_交互输入可覆盖三项默认值(self):
        inp = FakeInput(["任务完成", "联合巡视", "直流1000kV"])
        settings = drt.parse_settings(make_args(), input_fn=inp)

        self.assertEqual("任务完成", settings.task_status)
        self.assertEqual("联合巡视", settings.mission_type)
        self.assertEqual("直流1000kV", settings.voltage_level)


class SettingsEnumTest(unittest.TestCase):
    def test_全部任务状态均可通过参数设置(self):
        for value in TASK_STATUSES:
            with self.subTest(value=value):
                settings = drt.parse_settings(
                    make_fixed_args(task_status=value), input_fn=no_input
                )
                self.assertEqual(value, settings.task_status)

    def test_全部作业性质均可通过参数设置(self):
        for value in MISSION_TYPES:
            with self.subTest(value=value):
                settings = drt.parse_settings(
                    make_fixed_args(mission_type=value), input_fn=no_input
                )
                self.assertEqual(value, settings.mission_type)

    def test_全部电压等级均可通过参数设置(self):
        for value in VOLTAGE_LEVELS:
            with self.subTest(value=value):
                settings = drt.parse_settings(
                    make_fixed_args(voltage_level=value), input_fn=no_input
                )
                self.assertEqual(value, settings.voltage_level)

    def test_作业性质电压等级全部与空线路(self):
        settings = drt.parse_settings(
            make_fixed_args(
                mission_type="全部", voltage_level="全部", line_name=""
            ),
            input_fn=no_input,
        )

        self.assertEqual("", settings.mission_type)
        self.assertEqual("", settings.voltage_level)
        self.assertEqual("", settings.line_name)

    def test_非法枚举报错并列出合法值(self):
        cases = (
            ("task_status", "待审核"),
            ("mission_type", "巡检"),
            ("voltage_level", "220kV"),
        )
        for attr, bad in cases:
            with self.subTest(attr=attr):
                with self.assertRaises(ValueError) as ctx:
                    drt.parse_settings(
                        make_fixed_args(**{attr: bad}), input_fn=no_input
                    )
                message = str(ctx.exception)
                self.assertIn("必须是", message)
                self.assertIn(bad, message)

    def test_交互输入非法枚举后重新询问(self):
        inp = FakeInput(["待审核", "待人工审核", "", ""])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(make_args(), input_fn=inp)

        self.assertEqual("待人工审核", settings.task_status)
        self.assertEqual(4, len(inp.prompts))

    def test_缺少枚举且无法交互时报错(self):
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_settings(make_args(), input_fn=FakeInput([]))
        self.assertIn("任务状态", str(ctx.exception))


class SettingsRequiredTest(unittest.TestCase):
    def test_线路为空直接使用全部(self):
        inp = FakeInput(["   "])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(make_fixed_args(line_name=None), input_fn=inp)

        self.assertEqual("", settings.line_name)
        self.assertEqual(1, len(inp.prompts))

    def test_日期为空时重新询问(self):
        inp = FakeInput(["", "2026-11-01", "   ", "2026-11-02"])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(
                make_fixed_args(start_date=None, end_date=None), input_fn=inp
            )

        self.assertEqual("2026-11-01", settings.start_date)
        self.assertEqual("2026-11-02", settings.end_date)
        self.assertEqual(4, len(inp.prompts))

    def test_日期格式错误时重新询问(self):
        inp = FakeInput(["2026/11/01", "2026-1-1", "2026-11-01", "2026-11-02"])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(
                make_fixed_args(start_date=None, end_date=None), input_fn=inp
            )

        self.assertEqual("2026-11-01", settings.start_date)
        self.assertEqual("2026-11-02", settings.end_date)
        self.assertEqual(4, len(inp.prompts))

    def test_不存在的日期时重新询问(self):
        inp = FakeInput(["2026-02-30", "2026-11-01", "2026-11-02"])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(
                make_fixed_args(start_date=None, end_date=None), input_fn=inp
            )

        self.assertEqual("2026-11-01", settings.start_date)
        self.assertEqual(3, len(inp.prompts))

    def test_命令行日期格式错误被拒绝(self):
        with self.assertRaises(ValueError) as ctx:
            drt.parse_settings(
                make_fixed_args(start_date="2026/11/01"), input_fn=no_input
            )
        self.assertIn("YYYY-MM-DD", str(ctx.exception))

    def test_开始日期晚于结束日期被拒绝(self):
        with self.assertRaises(ValueError) as ctx:
            drt.parse_settings(
                make_fixed_args(start_date="2026-11-03", end_date="2026-11-02"),
                input_fn=no_input,
            )
        message = str(ctx.exception)
        self.assertIn("2026-11-03", message)
        self.assertIn("2026-11-02", message)

    def test_交互输入日期顺序错误被拒绝(self):
        inp = FakeInput(["2026-11-03", "2026-11-02"])
        with self.assertRaises(ValueError) as ctx:
            drt.parse_settings(
                make_fixed_args(start_date=None, end_date=None), input_fn=inp
            )
        self.assertIn("不能晚于", str(ctx.exception))

    def test_同一天日期允许(self):
        settings = drt.parse_settings(
            make_fixed_args(start_date="2026-11-03", end_date="2026-11-03"),
            input_fn=no_input,
        )
        self.assertEqual("2026-11-03", settings.start_date)
        self.assertEqual("2026-11-03", settings.end_date)

    def test_缺少必填项且无法交互时报错(self):
        cases = (
            ("start_date", "开始日期"),
            ("end_date", "结束日期"),
        )
        for attr, label in cases:
            with self.subTest(attr=attr):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.parse_settings(
                        make_fixed_args(**{attr: None}), input_fn=FakeInput([])
                    )
                self.assertIn(label, str(ctx.exception))


class SettingsParserTest(unittest.TestCase):
    def test_六个筛选参数可解析(self):
        args = drt.build_parser().parse_args(
            [
                "--task-status",
                "任务完成",
                "--mission-type",
                "联合巡视",
                "--voltage-level",
                "直流1000kV",
                "--line-name",
                "测试线路",
                "--start-date",
                "2026-11-01",
                "--end-date",
                "2026-11-02",
            ]
        )

        self.assertEqual("任务完成", args.task_status)
        self.assertEqual("联合巡视", args.mission_type)
        self.assertEqual("直流1000kV", args.voltage_level)
        self.assertEqual("测试线路", args.line_name)
        self.assertEqual("2026-11-01", args.start_date)
        self.assertEqual("2026-11-02", args.end_date)

    def test_命令行非法枚举由argparse拒绝(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as ctx:
                drt.build_parser().parse_args(["--task-status", "待审核"])
        self.assertEqual(2, ctx.exception.code)


class ApiEnvelopeTest(unittest.TestCase):
    def test_接口路径常量与录制接口一致(self):
        self.assertEqual(
            "/api/defect-server/code/recognitiontaskinfo/page",
            drt.TASK_PAGE_API,
        )
        self.assertEqual(
            "/api/basic-server/device/listByLineCondition",
            drt.LINE_CONDITION_API,
        )

    def test_成功响应返回data(self):
        data = {"records": [], "total": 0, "current": 1, "pages": 0}
        self.assertIs(
            data,
            drt.parse_api_data(ok_payload(data), "测试接口"),
        )

    def test_成功响应data为空时返回None(self):
        self.assertIsNone(
            drt.parse_api_data(ok_payload(None), "测试接口"),
        )

    def test_非对象响应被拒绝(self):
        for payload in (None, [], "OK", 200, True):
            with self.subTest(payload=payload):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.parse_api_data(payload, "测试接口")
                self.assertIn("不是 JSON 对象", str(ctx.exception))

    def test_失败响应被拒绝(self):
        cases = (
            (
                "code非200",
                {"code": 500, "success": True, "data": None, "msg": "服务异常"},
            ),
            (
                "success为False",
                {"code": 200, "success": False, "data": None, "msg": "审核失败"},
            ),
            ("success缺失", {"code": 200, "data": None, "msg": "未标记成功"}),
            (
                "success为数字1",
                {"code": 200, "success": 1, "data": None, "msg": "类型错误"},
            ),
            (
                "code为字符串",
                {"code": "200", "success": True, "data": None, "msg": "类型错误"},
            ),
        )
        for label, payload in cases:
            with self.subTest(label=label):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.parse_api_data(payload, "测试接口")
                message = str(ctx.exception)
                self.assertIn("测试接口", message)
                self.assertIn(payload["msg"], message)

    def test_失败响应缺少消息时使用未知错误(self):
        cases = (
            {"code": 500, "success": False, "data": None},
            {"code": 500, "success": False, "data": None, "msg": "   "},
            {"code": 500, "success": False, "data": None, "msg": None},
        )
        for payload in cases:
            with self.subTest(payload=payload):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.parse_api_data(payload, "测试接口")
                self.assertIn("未知错误", str(ctx.exception))


class LineNameWarningTest(unittest.TestCase):
    def test_中英文冒号拆分且无弃用警告(self):
        with warnings.catch_warnings():
            warnings.simplefilter("error", DeprecationWarning)
            self.assertEqual(
                "220kV测试线",
                drt._line_name_from_tower("220kV测试线：#1"),
            )
            self.assertEqual(
                "220kV测试线",
                drt._line_name_from_tower("220kV测试线:#1"),
            )
            self.assertEqual(
                "220kV测试线",
                drt._line_name_from_tower("220kV测试线"),
            )


class TaskPageTest(unittest.TestCase):
    def test_返回Spring分页字段(self):
        page = {
            "records": [record("guid-1"), record("guid-2")],
            "total": 21,
            "current": 2,
            "pages": 2,
        }
        records, total, current, pages = drt.parse_task_page(ok_payload(page))

        self.assertEqual(2, len(records))
        self.assertEqual("guid-1", records[0]["recognitionTaskGuid"])
        self.assertEqual("guid-2", records[1]["recognitionTaskGuid"])
        self.assertEqual(21, total)
        self.assertEqual(2, current)
        self.assertEqual(2, pages)

    def test_空页返回空记录和零总数(self):
        page = {"records": [], "total": 0, "current": 1, "pages": 0}
        records, total, current, pages = drt.parse_task_page(ok_payload(page))

        self.assertEqual([], records)
        self.assertEqual(0, total)
        self.assertEqual(1, current)
        self.assertEqual(0, pages)

    def test_失败响应直接报错(self):
        payload = {"code": 500, "success": False, "data": None, "msg": "查询失败"}
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_task_page(payload)
        self.assertIn("查询失败", str(ctx.exception))

    def test_分页字段缺失或类型错误被拒绝(self):
        cases = (
            ("data不是对象", None),
            ("data是数组", []),
            ("records缺失", {"total": 0, "current": 1, "pages": 0}),
            ("records不是数组", {"records": {}, "total": 0, "current": 1, "pages": 0}),
            (
                "records为None",
                {"records": None, "total": 0, "current": 1, "pages": 0},
            ),
            (
                "total不是整数",
                {"records": [], "total": "0", "current": 1, "pages": 0},
            ),
            (
                "total是布尔值",
                {"records": [], "total": True, "current": 1, "pages": 0},
            ),
            (
                "current不是整数",
                {"records": [], "total": 0, "current": None, "pages": 0},
            ),
            (
                "pages不是整数",
                {"records": [], "total": 0, "current": 1, "pages": 1.5},
            ),
        )
        for label, data in cases:
            with self.subTest(label=label):
                with self.assertRaises(RuntimeError):
                    drt.parse_task_page(ok_payload(data))


class LineChoiceTest(unittest.TestCase):
    def test_唯一候选直接返回(self):
        self.assertEqual(
            "测试线路",
            drt.choose_unique_line(["测试线路"], "测试"),
        )

    def test_唯一候选与输入线路不一致时拒绝(self):
        with self.assertRaises(RuntimeError) as ctx:
            drt.choose_unique_line(["别的线路"], "测试线路")
        message = str(ctx.exception)
        self.assertIn("测试线路", message)
        self.assertIn("别的线路", message)

    def test_零候选拒绝并提示补全(self):
        for options in ([], None):
            with self.subTest(options=options):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.choose_unique_line(options, "测试线路")
                message = str(ctx.exception)
                self.assertIn("测试线路", message)
                self.assertIn("没有匹配候选", message)

    def test_多候选拒绝且不猜选(self):
        with self.assertRaises(RuntimeError) as ctx:
            drt.choose_unique_line(["测试线路甲", "测试线路乙"], "测试线路")
        message = str(ctx.exception)
        self.assertIn("2", message)
        self.assertIn("测试线路甲", message)
        self.assertIn("测试线路乙", message)

    def test_空白候选被忽略(self):
        self.assertEqual(
            "测试线路",
            drt.choose_unique_line(["", "   ", " 测试线路 "], "测试"),
        )

    def test_空白线路输入被拒绝(self):
        with self.assertRaises(ValueError):
            drt.choose_unique_line(["测试线路"], "   ")


class TaskCandidateTest(unittest.TestCase):
    def test_返回第一个未处理记录(self):
        records = [record("guid-1", "任务一"), record("guid-2", "任务二")]
        self.assertIs(records[0], drt.next_task_candidate(records, set(), set()))

    def test_已处理和已跳过GUID被排除(self):
        records = [
            record("guid-1", "任务一"),
            record("guid-2", "任务二"),
            record("guid-3", "任务三"),
        ]
        self.assertIs(
            records[2],
            drt.next_task_candidate(records, ["guid-1"], {"guid-2"}),
        )

    def test_全部排除时返回None(self):
        records = [record("guid-1"), record("guid-2")]
        self.assertIsNone(drt.next_task_candidate(records, {"guid-1"}, {"guid-2"}))

    def test_无GUID记录被跳过(self):
        records = [
            {"taskName": "缺少GUID"},
            {"recognitionTaskGuid": "   ", "taskName": "空白GUID"},
            record("guid-1"),
        ]
        self.assertIs(records[2], drt.next_task_candidate(records, set(), set()))

    def test_非对象记录被跳过(self):
        records = ["坏记录", None, record("guid-1")]
        self.assertIs(records[2], drt.next_task_candidate(records, set(), set()))

    def test_空记录返回None(self):
        for records in (None, []):
            with self.subTest(records=records):
                self.assertIsNone(drt.next_task_candidate(records, set(), set()))


START_URL = "https://example.test" + drt.TASK_PAGE_PATH
TASK_ENTER = '[data-pid="IdentifyTasks_enter"]'
VOLTAGE_CODES = {
    "交流35kV": "25",
    "交流110kV": "32",
    "交流220kV": "33",
    "交流500kV": "35",
    "交流1000kV": "37",
    "±800kV地": "50",
    "直流500kV": "83",
    "直流660kV": "84",
    "直流800kV": "85",
    "直流1000kV": "86",
}


def make_settings(**overrides):
    values = {
        "task_status": "待人工审核",
        "mission_type": "精细化巡检",
        "voltage_level": "交流220kV",
        "line_name": "测试线路",
        "start_date": "2026-11-01",
        "end_date": "2026-11-02",
    }
    values.update(overrides)
    return drt.ReviewSettings(**values)


def runner_args(tmpdir, **overrides):
    values = {
        "task_status": None,
        "mission_type": None,
        "voltage_level": None,
        "line_name": "测试线路",
        "start_date": "2026-11-01",
        "end_date": "2026-11-02",
        "scan_only": False,
        "launch": False,
        "start_url": "",
        "cdp_url": "http://127.0.0.1:9222",
        "browser_path": "",
        "profile_dir": str(Path(tmpdir) / "browser_profile"),
        "out": str(tmpdir),
        "timeout": 2,
        # 单测默认关闭照片网络下载；归档用例显式传 save_photos=True。
        "save_photos": False,
    }
    values.update(overrides)
    return argparse.Namespace(**values)


def make_runner(page, tmpdir, log_handler=None, **overrides):
    args = runner_args(tmpdir, **overrides)
    handler = log_handler if log_handler is not None else (lambda message: None)
    runner = drt.ReviewRunner(args, Path(tmpdir), log_handler=handler)
    runner.page = page
    return runner


class FakeLocator:
    def __init__(self, items):
        self.items = list(items)

    def count(self):
        return len(self.items)

    def nth(self, index):
        return self.items[index]

    @property
    def first(self):
        return self.items[0] if self.items else FakeElement(visible=False)

    @property
    def last(self):
        return self.items[-1] if self.items else FakeElement(visible=False)

    def is_visible(self):
        return any(item.is_visible() for item in self.items)

    def is_enabled(self):
        return any(item.is_enabled() for item in self.items)

    def inner_text(self, timeout=None):
        return self.first.inner_text()

    def click(self):
        return self.first.click()

    def fill(self, value):
        return self.first.fill(value)

    def press(self, key):
        return self.first.press(key)

    def input_value(self):
        return self.first.input_value()

    def get_attribute(self, name):
        return self.first.get_attribute(name)

    def hover(self):
        return self.first.hover()

    def locator(self, selector):
        items = []
        for item in self.items:
            items.extend(item.locator(selector).items)
        return FakeLocator(items)

    def get_by_text(self, text, exact=True):
        items = []
        for item in self.items:
            items.extend(item.get_by_text(text, exact=exact).items)
        return FakeLocator(items)


class FakeElement:
    def __init__(self, text="", visible=True, enabled=True, name=""):
        self._text = text
        self.visible = bool(visible)
        self.enabled = bool(enabled)
        self.name = name
        self.value = ""
        self.attributes = {}
        self.children = {}
        self.parent = None
        self.box = None
        self.on_click = None
        self.on_fill = None
        self.on_hover = None
        self.on_press = None

    def add(self, selector, element):
        element.parent = self
        self.children.setdefault(selector, []).append(element)
        return element

    def _root(self):
        node = self
        while node.parent is not None:
            node = node.parent
        return node

    def _log(self, action, target=None):
        actions = getattr(self._root(), "actions", None)
        if actions is not None:
            label = target if target is not None else (self.name or self._text)
            actions.append((action, label))

    @property
    def first(self):
        return self

    def count(self):
        return 1

    def nth(self, index):
        if index != 0:
            raise IndexError(index)
        return self

    def locator(self, selector):
        if selector == "xpath=..":
            return FakeLocator([self.parent] if self.parent is not None else [])
        items = list(self.children.get(selector, []))
        if not items and ":" in selector:
            base = selector.split(":", 1)[0]
            items = list(self.children.get(base, []))
        if selector.endswith(":visible"):
            items = [item for item in items if item.is_visible()]
        return FakeLocator(items)

    def get_by_text(self, text, exact=True):
        matches = []

        def visit(node):
            matched = node._text == text if exact else text in node._text
            if matched:
                matches.append(node)
            for children in node.children.values():
                for child in children:
                    visit(child)

        visit(self)
        return FakeLocator(matches)

    def is_visible(self):
        return self.visible

    def is_enabled(self):
        return self.enabled

    def is_disabled(self):
        return not self.enabled

    def inner_text(self, timeout=None):
        parts = []
        if self._text:
            parts.append(self._text)
        for children in self.children.values():
            for child in children:
                if child.visible:
                    text = child.inner_text()
                    if text:
                        parts.append(text)
        return " ".join(" ".join(parts).split())

    def click(self):
        if not self.visible:
            raise RuntimeError("元素不可见")
        if not self.enabled:
            raise RuntimeError("元素已禁用")
        self._log("click")
        if self.on_click:
            self.on_click()

    def fill(self, value):
        self._log("fill")
        self.value = value
        if self.on_fill:
            self.on_fill(value)

    def press(self, key):
        self._log("press", key)
        if self.on_press:
            self.on_press(key)

    def input_value(self):
        return self.value

    def get_attribute(self, name):
        return self.attributes.get(name)

    def bounding_box(self):
        return self.box

    def hover(self):
        if not self.visible:
            raise RuntimeError("元素不可见")
        self._log("hover")
        if self.on_hover:
            self.on_hover()

    def wait_for(self, **kwargs):
        return None

    def set_default_timeout(self, timeout):
        return None

    def set_default_navigation_timeout(self, timeout):
        return None

    def evaluate(self, expression, arg=None):
        if "shade-review-loading-probe" in expression:
            probe = getattr(self, "loading_probe", False)
            return probe() if callable(probe) else bool(probe)
        if "el.click()" in expression:
            self.click()
            return None
        if "tagName" in expression:
            return self.attributes.get("tag") or ""
        if "arrayBuffer" in expression:
            return getattr(self, "fetch_result", None)
        if "currentSrc" in expression:
            return self.attributes.get("src") or ""
        if "toDataURL" in expression:
            return self.attributes.get("data_url") or ""
        if "history.pushState" in expression:
            return getattr(self, "client_route_ok", False)
        if "dispatchEvent" in expression:
            if arg is not None:
                self.value = str(arg)
            return None
        return True


class FakePage(FakeElement):
    def __init__(self, url=START_URL):
        super().__init__("", True, True, "页面")
        self.url = url
        self.keyboard = FakeKeyboard(self)
        self.actions = []
        self.goto_calls = []
        self.listeners = {}
        self.closed = False
        self.wait_calls = 0
        self.screenshots = []
        self.frames = []
        self.on_wait = None

    def on(self, event, handler):
        self.listeners.setdefault(event, []).append(handler)

    def remove_listener(self, event, handler):
        handlers = self.listeners.get(event, [])
        if handler in handlers:
            handlers.remove(handler)

    def emit(self, event, data):
        for handler in list(self.listeners.get(event, [])):
            handler(data)

    def goto(self, url, wait_until=None):
        self.goto_calls.append(url)
        self.actions.append(("goto", url))
        self.url = url

    def reload(self, wait_until=None):
        self.actions.append(("reload", self.url))

    def bring_to_front(self):
        return None

    def title(self):
        return "测试页面"

    def is_closed(self):
        return self.closed

    def wait_for_timeout(self, timeout_ms):
        self.wait_calls += 1
        self.actions.append(("wait", timeout_ms))
        if self.on_wait:
            self.on_wait()

    def screenshot(self, path=None, full_page=False, timeout=None):
        self.screenshots.append(path)
        if path:
            Path(path).write_bytes(b"fake-screenshot")

    def close(self):
        self.closed = True
        self.actions.append(("close_page", self.url))


class FakeKeyboard:
    def __init__(self, page):
        self.page = page

    def press(self, key):
        if key == "Escape":
            dropdown = getattr(self.page, "dropdown", None)
            if dropdown is not None:
                dropdown.visible = False
        # 与真实 page.keyboard 一样，最终触发页面的 keydown/press 监听。
        try:
            self.page.press(key)
        except Exception:
            pass


class FakeFrame:
    def __init__(self, page):
        self.page = page


class FakeRequest:
    def __init__(self, url, page=None, post_data_json=None):
        self.url = url
        self.frame = FakeFrame(page)
        self.post_data_json = post_data_json
        self.post_data = (
            None
            if post_data_json is None
            else json.dumps(post_data_json, ensure_ascii=False)
        )


class FakeResponse:
    def __init__(
        self,
        url,
        payload,
        status=200,
        page=None,
        post_data_json=None,
        query_params=None,
    ):
        if query_params:
            url = url + "?" + urlencode(query_params)
        self.url = url
        self.payload = payload
        self.status = status
        self.request = FakeRequest(url, page, post_data_json)

    def json(self):
        return self.payload


class FakeDropdown(FakeElement):
    def __init__(self, page):
        super().__init__("", visible=False, name="下拉")
        self.page_ref = page
        self.current_select = None
        page.add(".el-select-dropdown", self)

    def open_for(self, select):
        self.visible = True
        self.current_select = select
        typed = select.input.value if select.filter_on_input else ""
        self.children[".el-select-dropdown__item"] = []
        for text in select.current_options(typed):
            option = FakeElement(text, True, True)
            option.on_click = lambda text=text: self._choose(select, text)
            self.add(".el-select-dropdown__item", option)

    def _choose(self, select, text):
        previous = select.selected
        select.selected = text
        select._text = text
        self.visible = False
        if select.on_select and text != previous:
            select.on_select(text)


class FakeSelect(FakeElement):
    def __init__(
        self,
        page,
        options=None,
        dynamic_options=None,
        filter_on_input=False,
        name="",
    ):
        super().__init__("", True, True, name)
        self.page_ref = page
        self.options = list(options or [])
        self.dynamic_options = dynamic_options
        self.filter_on_input = filter_on_input
        self.selected = ""
        self.on_select = None
        self.input = FakeElement("", True, True, "输入")
        self.add("input", self.input)
        self.input.on_fill = lambda value: self.page_ref.dropdown.open_for(self)

    def current_options(self, typed=""):
        options = (
            list(self.dynamic_options()) if self.dynamic_options else list(self.options)
        )
        if self.filter_on_input and typed:
            options = [text for text in options if typed.casefold() in text.casefold()]
        return options

    def click(self):
        super().click()
        self.page_ref.dropdown.open_for(self)


class TaskListUI:
    def __init__(
        self,
        records=None,
        states=None,
        pages=None,
        initial_voltage="",
        suppress_line_for=None,
        line_payloads=None,
        line_code_override=None,
        request_task_status="manual_review",
        request_mission_type="fine_inspect",
    ):
        self.page = FakePage()
        self.states = dict(states or {})
        self.row_texts = {}
        self.enter_clicks = []
        self.next_clicks = 0
        self.jump_count = 0
        self.jump_target = 0
        self.query_count = 0
        self.query_current = 1
        self.request_times = None
        self.request_task_status = request_task_status
        self.request_mission_type = request_mission_type
        self.suppress_line_for = set(suppress_line_for or ())
        self.line_payloads = dict(line_payloads or {})
        self.line_code_override = dict(line_code_override or {})
        self.line_requests = []
        self.pages = [[]]
        self.current_page = 1
        self.rows = {}
        self.enter_handlers = {}
        self._voltage_value = initial_voltage
        self._build()
        if initial_voltage:
            self.voltage.selected = initial_voltage
            self.voltage._text = initial_voltage
        if pages is not None:
            self.set_pages(pages)
        else:
            self.set_records(records or [])

    def _build(self):
        page = self.page
        page.dropdown = FakeDropdown(page)
        self.task_status = FakeSelect(page, TASK_STATUSES, name="任务状态下拉")
        self.mission_type = FakeSelect(
            page, ("全部",) + MISSION_TYPES, name="作业性质下拉"
        )
        self.voltage = FakeSelect(
            page, ("全部",) + VOLTAGE_LEVELS, name="电压等级下拉"
        )
        self.line = FakeSelect(
            page,
            dynamic_options=self._line_options,
            filter_on_input=True,
            name="线路下拉",
        )
        self.voltage.on_select = self._on_voltage
        for label, select in (
            ("任务状态", self.task_status),
            ("作业性质", self.mission_type),
            ("电压等级", self.voltage),
            ("线路名称", self.line),
        ):
            item = FakeElement(label, True, True, label)
            item.add(".el-form-item__label", FakeElement(label))
            item.add(".el-select", select)
            page.add(".el-form-item", item)

        self.start_input = FakeElement("", True, True, "开始日期输入")
        self.end_input = FakeElement("", True, True, "结束日期输入")
        self.start_input.value = "2026-11-01"
        self.end_input.value = "2026-11-02"
        for label, input_el in (
            ("开始日期", self.start_input),
            ("结束日期", self.end_input),
        ):
            item = FakeElement(label, True, True, label)
            item.add(".el-form-item__label", FakeElement(label))
            item.add("input", input_el)
            page.add(".el-form-item", item)

        self.table = FakeElement(name="任务表格")
        self.header = FakeElement("任务名称 任务状态 作业性质 操作")
        self.table.add(".el-table__header", self.header)
        self.body = FakeElement(name="任务表体")
        self.table.add(".el-table__body-wrapper", self.body)
        page.add(".el-table", self.table)

        self.query_button = FakeElement("查询", True, True, "查询")
        self.query_button.on_click = self.emit_query
        page.add("button", self.query_button)

        self.pager = FakeElement(name="分页器")
        self.next_button = FakeElement("下一页", True, True, "下一页")
        self.next_button.on_click = self.next_page
        self.pager.add(".btn-next", self.next_button)
        self.jump_container = FakeElement(name="跳页")
        self.jump_input = FakeElement("", True, True, "跳页输入")
        self.jump_input.on_press = lambda key: (
            self.goto_page(int(self.jump_input.value or 1))
            if key == "Enter"
            else None
        )
        self.jump_container.add("input", self.jump_input)
        self.pager.add(".el-pagination__jump", self.jump_container)
        page.add(".el-pagination", self.pager)

    def _line_options(self):
        return ["全部", "测试线路"]

    def _on_voltage(self, value):
        self._voltage_value = "" if value in ("全部", "") else value
        if not self._voltage_value:
            return
        self.line_requests.append(value)
        if value in self.suppress_line_for:
            return
        code = self.line_code_override.get(value, VOLTAGE_CODES[value])
        if value in self.line_payloads:
            payload = self.line_payloads[value]
        else:
            payload = ok_payload(
                [
                    {
                        "lineGuid": "line-guid",
                        "lineName": "测试线路",
                        "voltageLevel": VOLTAGE_CODES[value],
                        "voltageName": value,
                    }
                ]
            )
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + drt.LINE_CONDITION_API,
                payload,
                page=self.page,
                query_params={"voltageLevel": code},
            ),
        )

    def set_records(self, records, states=None):
        self.pages = [list(records)]
        self.current_page = 1
        if states:
            self.states.update(states)
        self._render()

    def set_pages(self, pages, states=None):
        self.pages = [list(page) for page in pages]
        self.current_page = 1
        if states:
            self.states.update(states)
        self._render()

    def _current_records(self):
        if not self.pages:
            return []
        return self.pages[self.current_page - 1]

    def _request_params(self):
        if self.request_times is not None:
            start, end = self.request_times
            params = {"startTime": start, "endTime": end}
        else:
            params = {
                "startTime": self.start_input.value + " 00:00:00",
                "endTime": self.end_input.value + " 23:59:59",
            }
        if self.request_task_status is not None:
            params["taskStatus"] = self.request_task_status
        if self.request_mission_type is not None:
            params["missionType"] = self.request_mission_type
        return params

    def _payload(self):
        records = list(self._current_records())
        total = sum(len(page) for page in self.pages)
        pages = len(self.pages) if total else 0
        return ok_payload(
            {
                "records": records,
                "total": total,
                "current": self.current_page,
                "pages": pages,
            }
        )

    def _emit_page_response(self):
        self._render()
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + drt.TASK_PAGE_API,
                self._payload(),
                page=self.page,
                query_params=self._request_params(),
            ),
        )

    def emit_query(self):
        self.query_count += 1
        self.current_page = self.query_current
        self._emit_page_response()

    def goto_first_page(self):
        self.goto_page(1)

    def goto_page(self, target):
        target = max(1, min(int(target), len(self.pages)))
        if self.current_page == target:
            return
        self.jump_count += 1
        self.jump_target = target
        self.current_page = target
        self._emit_page_response()

    def next_page(self):
        if self.current_page >= len(self.pages):
            return
        self.next_clicks += 1
        self.current_page += 1
        self._emit_page_response()

    def _enter(self, guid):
        self.enter_clicks.append(guid)
        handler = self.enter_handlers.get(guid)
        if handler:
            handler()

    def _render(self):
        self.body.children["tbody tr.el-table__row"] = []
        self.rows = {}
        for item in self._current_records():
            guid = item.get("recognitionTaskGuid")
            row_text = self.row_texts.get(guid) or " ".join(
                [item.get("taskName", ""), item.get("taskStatus", "")]
            )
            row = FakeElement(row_text, True, True, "任务行")
            state = self.states.get(guid, "visible")
            if state != "none":
                button = FakeElement(
                    "进入",
                    visible=state != "hidden",
                    enabled=state != "disabled",
                    name="进入",
                )
                button.on_click = lambda guid=guid: self._enter(guid)
                row.add(TASK_ENTER, button)
            self.body.add("tbody tr.el-table__row", row)
            self.rows[guid] = row
        self.next_button.enabled = self.current_page < len(self.pages)


class FakePlaywright:
    def __init__(self):
        self.stopped = False

    def stop(self):
        self.stopped = True


class FakeBrowser:
    def __init__(self):
        self.closed = False

    def close(self):
        self.closed = True


class ClientConfigTest(unittest.TestCase):
    def test_读取客户端浏览器和CDP配置(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            browser = Path(tmpdir) / "chrome"
            browser.write_text("", encoding="utf-8")
            config = Path(tmpdir) / "config.json"
            config.write_text(
                json.dumps(
                    {
                        "render": {"chrome": str(browser)},
                        "browser_automation": {"cdp_url": "http://127.0.0.1:9333"},
                    }
                ),
                encoding="utf-8",
            )
            with mock.patch.dict(os.environ, {"UVMP_TOOLKIT_CONFIG": str(config)}):
                self.assertEqual(str(browser), drt._client_browser_path())
                self.assertEqual("http://127.0.0.1:9333", drt._client_cdp_url())
                args = drt.build_parser().parse_args([])
                self.assertEqual("http://127.0.0.1:9333", args.cdp_url)
                self.assertEqual(str(browser), drt.detect_browser())


class ClientDevScriptTest(unittest.TestCase):
    def test_枚举项在客户端模式取默认值(self):
        settings = drt.parse_settings(make_args(), input_fn=drt._client_input)

        self.assertEqual("待人工审核", settings.task_status)
        self.assertEqual("精细化巡检", settings.mission_type)
        self.assertEqual("交流220kV", settings.voltage_level)

    def test_客户端模式缺少必填项快速失败(self):
        with mock.patch.dict(os.environ, {"UVMP_DEV_SCRIPT": "1"}):
            with mock.patch("sys.stderr", new_callable=io.StringIO) as err:
                code = drt.main([])

        self.assertEqual(2, code)
        self.assertIn("开始日期", err.getvalue())

    def test_客户端模式允许launch并自动登录(self):
        FakeCliRunner.instances = []
        base = [
            "--task-status",
            "待人工审核",
            "--mission-type",
            "仿线巡检",
            "--voltage-level",
            "交流220kV",
            "--line-name",
            "测试线路",
            "--start-date",
            "2026-11-01",
            "--end-date",
            "2026-11-02",
            "--scan-only",
            "--launch",
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            base.extend(["--out", tmpdir])
            with mock.patch.dict(os.environ, {"UVMP_DEV_SCRIPT": "1"}):
                with (
                    mock.patch.object(drt, "launch_browser", lambda *args: None),
                    mock.patch.object(drt, "cdp_reachable", return_value=False),
                ):
                    code = drt.main(base, runner_factory=FakeCliRunner)

        self.assertEqual(0, code)
        runner = FakeCliRunner.instances[0]
        self.assertIn(("connect", True), runner.calls)
        self.assertIn("ensure_authenticated", runner.calls)
        self.assertIn("open_task_list", runner.calls)
        self.assertIn("close", runner.calls)


class RunLogTest(unittest.TestCase):
    def test_日志同时写入输出目录与客户端回调(self):
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, log_handler=logs.append)
            runner.log("测试日志")
            runner.close()
            text = (Path(tmpdir) / "run.log").read_text(encoding="utf-8")

        self.assertIn("测试日志", logs)
        self.assertIn("测试日志", text)

    def test_输出目录跳过任务表逐条写入去重(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
            runner._init_output_skip_file()
            runner._record_output_skip_task("任务一")
            runner._record_output_skip_task("任务一")
            runner._record_output_skip_task("任务二")
            text = (Path(tmpdir) / drt.SKIP_TASKS_OUTPUT_NAME).read_text(
                encoding="utf-8"
            )

        self.assertEqual(["任务名称", "任务一", "任务二"], text.splitlines())

    def test_输出目录跳过表追加前补缺失换行(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
            path = Path(tmpdir) / drt.SKIP_TASKS_OUTPUT_NAME
            path.write_text("任务名称\n任务一", encoding="utf-8")
            runner._record_output_skip_task("任务二")
            text = path.read_text(encoding="utf-8")

        self.assertEqual(["任务名称", "任务一", "任务二"], text.splitlines())

    def test_收到取消请求只记录一次停止日志(self):
        def cancel_check():
            raise RuntimeError("cancelled")

        with tempfile.TemporaryDirectory() as tmpdir:
            args = runner_args(tmpdir)
            args.cancel_check = cancel_check
            runner = drt.ReviewRunner(
                args, Path(tmpdir), log_handler=lambda _message: None
            )
            with self.assertRaises(RuntimeError):
                runner.check_cancel()
            with self.assertRaises(RuntimeError):
                runner.check_cancel()
            runner.close()
            text = (Path(tmpdir) / "run.log").read_text(encoding="utf-8")

        self.assertTrue(runner.stop_requested)
        self.assertEqual(1, text.count("收到停止请求"))


class HostAwakeTest(unittest.TestCase):
    def test_不支持的平台静默降级且可重复启停(self):
        logs = []
        guard = drt.HostAwakeGuard(logs.append)
        with mock.patch.object(drt.sys, "platform", "darwin"):
            guard.start()
            guard.start()
            guard.stop()
            guard.stop()

        self.assertIn("不支持自动防休眠", " ".join(logs))

    def test_关闭时释放防休眠(self):
        class FakeGuard:
            def __init__(self):
                self.starts = 0
                self.stops = 0

            def start(self):
                self.starts += 1

            def stop(self):
                self.stops += 1

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
            guard = FakeGuard()
            runner.awake_guard = guard
            runner.keep_awake()
            runner.close()

        self.assertEqual(1, guard.starts)
        self.assertEqual(1, guard.stops)
        self.assertIsNone(runner.awake_guard)


class RuntimeRootTest(unittest.TestCase):
    def test_只读目录下的回退路径不依赖客户端(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            with (
                mock.patch.object(drt.os, "access", return_value=False),
                mock.patch.object(drt.sys, "platform", "win32"),
                mock.patch.dict(drt.os.environ, {"LOCALAPPDATA": tmpdir}),
            ):
                self.assertEqual(drt.runtime_root(), Path(tmpdir) / "defect-review")


class StartUrlTest(unittest.TestCase):
    def test_完整的http和https地址可用(self):
        for value in (
            "http://example.test/app",
            "https://example.test:8443/app?x=1#/task",
        ):
            with self.subTest(value=value):
                self.assertEqual(value, drt.validate_start_url(value))

    def test_拒绝非http或不完整的地址(self):
        for value in (
            "example.test/app",
            "/app",
            "javascript:alert(1)",
            "ftp://example.test/app",
            "http://",
            "http:///app",
            "http://example.test/a b",
            "http://user:password@example.test/app",
        ):
            with self.subTest(value=value):
                with self.assertRaises(RuntimeError) as ctx:
                    drt.validate_start_url(value)
                self.assertIn("--start-url", str(ctx.exception))

    def test_空地址表示使用空白页(self):
        for value in ("", "   ", None):
            with self.subTest(value=value):
                self.assertEqual("", drt.validate_start_url(value))


class BrowserParserTest(unittest.TestCase):
    def test_新增浏览器参数可解析(self):
        args = drt.build_parser().parse_args(
            [
                "--scan-only",
                "--launch",
                "--start-url",
                "https://example.test/app",
                "--cdp-url",
                "http://127.0.0.1:9333",
                "--browser-path",
                "C:/Chrome/chrome.exe",
                "--profile-dir",
                "C:/tmp/profile",
                "--out",
                "C:/tmp/out",
                "--timeout",
                "30",
            ]
        )

        self.assertTrue(args.scan_only)
        self.assertTrue(args.launch)
        self.assertEqual("https://example.test/app", args.start_url)
        self.assertEqual("http://127.0.0.1:9333", args.cdp_url)
        self.assertEqual("C:/Chrome/chrome.exe", args.browser_path)
        self.assertEqual("C:/tmp/profile", args.profile_dir)
        self.assertEqual("C:/tmp/out", args.out)
        self.assertEqual(30, args.timeout)

    def test_浏览器参数默认值(self):
        args = drt.build_parser().parse_args([])

        self.assertFalse(args.scan_only)
        self.assertFalse(args.launch)
        self.assertEqual("", args.start_url)
        self.assertEqual("http://127.0.0.1:9222", args.cdp_url)
        self.assertEqual(120, args.timeout)
        self.assertEqual(400, args.max_unreviewed)
        self.assertEqual(120, args.unknown_timeout)


class DateRangeFillTest(unittest.TestCase):
    def test_创建日期范围填写两个输入(self):
        page = FakePage()
        item = FakeElement("创建日期", True, True, "日期范围")
        item.add(".el-form-item__label", FakeElement("创建日期"))
        start_input = FakeElement("", True, True, "开始日期输入")
        end_input = FakeElement("", True, True, "结束日期输入")
        start_input.value = "2026-11-01"
        end_input.value = "2026-11-30"
        item.add("input", start_input)
        item.add("input", end_input)
        page.add(".el-form-item", item)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner._fill_date_range("2026-01-01", "2026-11-08")

        self.assertEqual("2026-01-01", start_input.value)
        self.assertEqual("2026-11-08", end_input.value)


class FilterTest(unittest.TestCase):
    def test_先选择电压等级再选择线路且日期直接填写(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.apply_filters(make_settings())

        click_targets = [
            action[1] for action in ui.page.actions if action[0] == "click"
        ]
        self.assertLess(
            click_targets.index("交流220kV"), click_targets.index("线路下拉")
        )
        self.assertEqual("待人工审核", ui.task_status.selected)
        self.assertEqual("精细化巡检", ui.mission_type.selected)
        self.assertEqual("交流220kV", ui.voltage.selected)
        self.assertEqual("测试线路", ui.line.selected)
        self.assertEqual("2026-11-01", ui.start_input.value)
        self.assertEqual("2026-11-02", ui.end_input.value)

    def test_下拉项使用DOM点击避免遮罩超时(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            with mock.patch.object(
                runner, "_dom_click", wraps=runner._dom_click
            ) as dom_click:
                runner.apply_filters(make_settings())

        # 任务状态/作业性质/电压邻居/电压目标/线路均走 DOM click。
        self.assertGreaterEqual(dom_click.call_count, 5)
        self.assertEqual("待人工审核", ui.task_status.selected)

    def test_展开下拉优先使用原生wrapper点击(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
        select = mock.Mock()
        wrapper = mock.Mock()
        wrapper.count.return_value = 1
        select.locator.return_value.first = wrapper
        runner._select_is_open = mock.Mock(return_value=False)
        runner._dropdown_open = mock.Mock(side_effect=[False, True])
        runner._reset_dropdown = mock.Mock()
        runner._dom_click = mock.Mock()
        runner._open_select_dropdown(select, "任务状态")
        wrapper.click.assert_called_once()
        runner._dom_click.assert_not_called()

    def test_下拉展开状态读取输入框aria(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
        select = mock.Mock()

        def locator(selector):
            holder = mock.Mock()
            target = holder.first
            target.count.return_value = 1
            if selector.startswith("input"):
                target.get_attribute.return_value = "true"
            else:
                target.get_attribute.return_value = None
            return holder

        select.locator.side_effect = locator
        self.assertTrue(runner._select_is_open(select))

    def test_页面残留下拉不阻止当前筛选展开(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
        select = mock.Mock()
        wrapper = mock.Mock()
        wrapper.count.return_value = 1
        wrapper.click.side_effect = RuntimeError("blocked")
        select.locator.return_value.first = wrapper
        runner._select_is_open = mock.Mock(return_value=False)
        runner._dropdown_open = mock.Mock(side_effect=[True, False, True])
        runner._reset_dropdown = mock.Mock()
        runner._dom_click = mock.Mock()
        runner._open_select_dropdown(select, "任务状态")
        runner._reset_dropdown.assert_called_once()
        wrapper.click.assert_called()
        runner._dom_click.assert_called()

    def test_下拉为空会重新展开(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        calls = {"count": 0}

        def options():
            calls["count"] += 1
            return [] if calls["count"] == 1 else ["全部", "测试线路"]

        ui.line.dynamic_options = options
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.apply_filters(make_settings())

        self.assertEqual("测试线路", ui.line.selected)
        self.assertGreaterEqual(calls["count"], 2)

    def test_查询结果为空不算页面加载失败(self):
        ui = TaskListUI([])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.apply_filters(make_settings())
            candidates, summary = runner.scan_task_list(
                make_settings(), stop_at_first=True
            )

        self.assertEqual([], candidates)
        self.assertEqual(0, summary["total"])
        self.assertEqual(1, ui.query_count)

    def test_选项在非last下拉中也能找到(self):
        page = FakePage()
        full = FakeElement(name="有选项下拉")
        option = FakeElement("待人工审核", True, True, "待人工审核")
        full.add(".el-select-dropdown__item", option)
        empty = FakeElement(name="空下拉")
        page.add(".el-select-dropdown", full)
        page.add(".el-select-dropdown", empty)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            got = runner._wait_dropdown_option(
                "待人工审核", "任务状态", timeout_ms=100
            )

        self.assertIs(option, got)

    def test_连续筛选失败达到上限后停止(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner._start_watchdog = mock.Mock()
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock(side_effect=RuntimeError("下拉点击超时"))
            runner._refresh_and_resume = mock.Mock()
            with self.assertRaises(RuntimeError) as ctx:
                runner.run(make_settings())

        self.assertEqual(drt._PREPARE_MAX_FAILURES, runner.apply_filters.call_count)
        self.assertIn(
            "连续 %d 次" % drt._PREPARE_MAX_FAILURES, str(ctx.exception)
        )
        self.assertIn("下拉点击超时", str(ctx.exception))

    def test_刷新筛选连续失败会重启并最终停止(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.page.reload = mock.Mock()
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock(
                side_effect=RuntimeError("页面导航未完成")
            )
            runner._wait_for_page_recovery = mock.Mock(return_value=True)
            runner._wait_for_timeout = mock.Mock()

            def reconnect():
                runner.connection_lost = False

            runner._reconnect = mock.Mock(side_effect=reconnect)
            with self.assertRaises(RuntimeError) as ctx:
                runner._refresh_and_resume(make_settings(), "测试刷新")

        self.assertEqual(9, runner.apply_filters.call_count)
        self.assertEqual(2, runner._reconnect.call_count)
        self.assertIn("已重启浏览器 2 次", str(ctx.exception))

    def test_三项筛选为空时不操作对应筛选项且查询可用(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")], request_mission_type=None
        )
        settings = make_settings(mission_type="", voltage_level="", line_name="")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            line_name = runner.apply_filters(settings)
            records, _total, _current, _pages = runner.query_tasks(settings)

        self.assertEqual("", line_name)
        self.assertEqual("", ui.mission_type.selected)
        self.assertEqual("", ui.voltage.selected)
        self.assertEqual("", ui.line.selected)
        click_targets = [action[1] for action in ui.page.actions if action[0] == "click"]
        self.assertNotIn("作业性质下拉", click_targets)
        self.assertNotIn("电压等级下拉", click_targets)
        self.assertNotIn("线路下拉", click_targets)
        self.assertEqual(1, len(records))
        self.assertEqual(1, ui.query_count)

    def test_线路多候选时不选择也不查询(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        ui.line.dynamic_options = lambda: ["测试线路甲", "测试线路乙"]
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.apply_filters(make_settings())

        self.assertIn("2", str(ctx.exception))
        self.assertIn("测试线路甲", str(ctx.exception))
        self.assertEqual("", ui.line.selected)
        self.assertEqual(0, ui.query_count)

    def test_查询日期与筛选不一致时报错(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        ui.request_times = ("2026-11-01 00:00:00", "2026-11-02 12:00:00")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.apply_filters(make_settings())
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        self.assertIn("2026-11-02 23:59:59", str(ctx.exception))
        self.assertIn("2026-11-02 12:00:00", str(ctx.exception))

    def test_查询返回分页字段(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.apply_filters(make_settings())
            records, total, current, pages = runner.query_tasks(make_settings())

        self.assertEqual(1, len(records))
        self.assertEqual("guid-1", records[0]["recognitionTaskGuid"])
        self.assertEqual(1, total)
        self.assertEqual(1, current)
        self.assertEqual(1, pages)


class TaskNavigationTest(unittest.TestCase):
    def test_已经位于任务列表时不重复导航(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            changed = runner.open_task_list()

        self.assertFalse(changed)
        self.assertEqual([], ui.page.goto_calls)
        self.assertFalse(
            any(
                action[0] in ("click", "hover")
                and action[1] in ("智能分析", "智能识别", "缺陷识别任务")
                for action in ui.page.actions
            )
        )

    def test_不在任务列表时按菜单路径进入(self):
        page = FakePage(url="about:blank")
        menu = FakeElement(name="菜单")
        page.add(".el-menu", menu)
        labels = ("智能分析", "智能识别", "缺陷识别任务")
        for label in labels[:-1]:
            menu.add(".el-menu-item", FakeElement(label, True, True, label))
        leaf = FakeElement(labels[-1], True, True, labels[-1])

        def show_task_list():
            page.url = START_URL
            table = FakeElement(name="任务表格")
            table.add(".el-table__header", FakeElement("任务名称 任务状态 操作"))
            table.add(".el-table__body-wrapper", FakeElement())
            page.add(".el-table", table)

        leaf.on_click = show_task_list
        menu.add(".el-menu-item", leaf)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner.ensure_authenticated = mock.Mock()
            changed = runner.open_task_list()

        self.assertTrue(changed)
        navigation = [
            action for action in page.actions if action[0] in ("hover", "click")
        ]
        self.assertEqual(
            [
                ("hover", "智能分析"),
                ("hover", "智能识别"),
                ("click", "缺陷识别任务"),
            ],
            navigation,
        )

    def test_任务页只有筛选栏没有列表也算就绪(self):
        page = FakePage(url=START_URL + drt.TASK_PAGE_PATH)
        page.add("body", FakeElement("任务状态 查询 重置"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner._task_list_visible())

    def test_表格结构变化时按文案识别任务页(self):
        page = FakePage(url=START_URL + drt.TASK_PAGE_PATH)
        page.add("body", FakeElement("任务名称 任务状态 查询 重置"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner._task_list_visible())

    def test_前端路由直达任务页不整页跳转(self):
        page = FakePage(url="http://example.test/home")
        page.client_route_ok = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                start_url="http://example.test",
                timeout=FAST,
            )
            runner._wait_for_task_list = mock.Mock()
            self.assertTrue(runner._goto_task_page_direct())

        self.assertEqual([], page.goto_calls)

    def test_404页点返回首页(self):
        page = FakePage(url="http://example.test" + drt.TASK_PAGE_PATH)
        page.add("div", FakeElement("返回首页", True, True, "返回首页"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner._click_return_home())

        self.assertIn(("click", "返回首页"), page.actions)

    def test_任务页404点返回首页后走菜单(self):
        page = FakePage(url="https://example.test" + drt.TASK_PAGE_PATH)
        back = FakeElement("返回首页", True, True, "返回首页")
        page.add("div", back)
        labels = drt.TASK_LIST_MENU_PATH
        menu = FakeElement(name="菜单")
        leaf = FakeElement(labels[-1], True, True, labels[-1])

        def show_task_list():
            page.url = START_URL
            table = FakeElement(name="任务表格")
            table.add(".el-table__header", FakeElement("任务名称 任务状态 操作"))
            table.add(".el-table__body-wrapper", FakeElement())
            page.add(".el-table", table)

        leaf.on_click = show_task_list

        def install_menu():
            page.url = "https://example.test/home"
            page.add(".el-menu", menu)
            for label in labels[:-1]:
                menu.add(".el-menu-item", FakeElement(label, True, True, label))
            menu.add(".el-menu-item", leaf)

        back.on_click = install_menu
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                start_url="https://example.test",
                timeout=FAST,
            )
            runner.ensure_authenticated = mock.Mock()
            runner._goto_task_page_direct = mock.Mock(return_value=False)
            changed = runner.open_task_list()

        self.assertTrue(changed)
        self.assertIn(("click", "返回首页"), page.actions)
        self.assertEqual([], page.goto_calls)
        navigation = [
            action for action in page.actions if action[0] in ("hover", "click")
        ]
        self.assertEqual(
            [
                ("click", "返回首页"),
                ("hover", "智能分析"),
                ("hover", "智能识别"),
                ("click", "缺陷识别任务"),
            ],
            navigation,
        )

    def test_任务页深链接404时先回启动网址再走菜单(self):
        page = FakePage(url="https://example.test" + drt.TASK_PAGE_PATH)
        labels = drt.TASK_LIST_MENU_PATH
        menu = FakeElement(name="菜单")
        leaf = FakeElement(labels[-1], True, True, labels[-1])

        def show_task_list():
            page.url = START_URL
            table = FakeElement(name="任务表格")
            table.add(".el-table__header", FakeElement("任务名称 任务状态 操作"))
            table.add(".el-table__body-wrapper", FakeElement())
            page.add(".el-table", table)

        leaf.on_click = show_task_list

        def install_menu():
            page.add(".el-menu", menu)
            for label in labels[:-1]:
                menu.add(".el-menu-item", FakeElement(label, True, True, label))
            menu.add(".el-menu-item", leaf)

        original_goto = page.goto

        def goto(url, wait_until=None):
            original_goto(url, wait_until)
            if url == "https://example.test":
                install_menu()

        page.goto = goto
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                start_url="https://example.test",
                timeout=FAST,
            )
            runner.ensure_authenticated = mock.Mock()
            changed = runner.open_task_list()

        self.assertTrue(changed)
        self.assertEqual(["https://example.test"], page.goto_calls)
        self.assertNotIn(
            "https://example.test" + drt.TASK_PAGE_PATH, page.goto_calls
        )
        navigation = [
            action for action in page.actions if action[0] in ("hover", "click")
        ]
        self.assertEqual(
            [
                ("hover", "智能分析"),
                ("hover", "智能识别"),
                ("click", "缺陷识别任务"),
            ],
            navigation,
        )


class LoginPageDetectionTest(unittest.TestCase):
    def test_识别登录页(self):
        page = FakePage()
        page.add("button", FakeElement("登录", True, True, "登录按钮"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner._login_page_visible())

    def test_非任务页URL的表格不算任务列表(self):
        page = FakePage(url="http://example.test/home")
        table = FakeElement(name="首页表格")
        table.add(".el-table__header", FakeElement("任务名称 任务状态 操作"))
        table.add(".el-table__body-wrapper", FakeElement())
        page.add(".el-table", table)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertFalse(runner._task_list_visible())

    def test_登录URL即使有菜单文本也不算已登录(self):
        page = FakePage(url="http://example.test/login")
        page.add("div", FakeElement("智能分析", True, True, "背景菜单"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertFalse(runner._session_ready())


class AutoLoginTest(unittest.TestCase):
    def test_自动登录失败多次仍继续直到成功(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
            )
            runner._session_ready = mock.Mock(
                side_effect=[False] * 50 + [True]
            )
            runner._login_page_visible = mock.Mock(return_value=True)
            runner._auto_login_once = mock.Mock(side_effect=[False] * 50)
            runner._sleep_without_page = mock.Mock()
            runner.ensure_authenticated()

        self.assertEqual(50, runner._auto_login_once.call_count)
        self.assertEqual(51, runner._session_ready.call_count)
        self.assertEqual(50, runner._sleep_without_page.call_count)

    def test_自动登录缺少凭据时快速失败(self):
        page = FakePage(url="http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, auto_login=True)
            with self.assertRaises(RuntimeError) as ctx:
                runner.ensure_authenticated()
        self.assertIn("自动登录缺少用户名或密码", str(ctx.exception))

    def test_根路径未渲染系统菜单时不算已登录(self):
        page = FakePage(url="http://example.test/")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertFalse(runner._session_ready())

    def test_系统菜单出现才算已登录(self):
        page = FakePage(url="http://example.test/")
        page.add("div", FakeElement("智能分析", True, True, "系统菜单"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner._session_ready())

    def test_检测到404先点返回首页(self):
        page = FakePage(url="http://example.test/404")
        page.add("div", FakeElement("404 返回首页", True, True, "404提示"))
        home = FakeElement("返回首页", True, True, "返回首页按钮")
        page.add("a", home)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner._wait_for_login_or_session = mock.Mock()
            self.assertTrue(runner._is_404_page())
            self.assertTrue(runner._handle_404_if_visible())

        self.assertIn(("click", "返回首页按钮"), page.actions)

    def test_404数字与返回首页相距很远不误判(self):
        page = FakePage(url="http://example.test/home")
        page.add(
            "body",
            FakeElement("首页 统计 404 " + "x" * 60 + " 返回首页"),
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
        self.assertFalse(runner._is_404_page())

    def test_任务页已加载时直达不再导航(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.url = "http://example.test" + drt.TASK_PAGE_PATH
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, start_url="http://example.test")
            runner._task_list_visible = mock.Mock(return_value=True)
            runner._goto_task_page_client = mock.Mock(return_value=False)
            page.evaluate = mock.Mock()
            self.assertTrue(runner._goto_task_page_direct())
            page.evaluate.assert_not_called()

    def test_直接打开任务页导航异常但页面已可见(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.url = "http://example.test/home"
        page.evaluate.side_effect = RuntimeError(
            "Execution context was destroyed"
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, start_url="http://example.test")
            runner._goto_task_page_client = mock.Mock(return_value=False)
            runner._wait_for_task_list = mock.Mock(
                side_effect=RuntimeError("Timeout 15000ms exceeded")
            )
            runner._task_list_visible = mock.Mock(return_value=True)
            self.assertTrue(runner._goto_task_page_direct())

    def test_首页带菜单时即使有404提示也不误判(self):
        page = FakePage(url="http://example.test/home")
        page.add(
            "body",
            FakeElement("首页 智能分析 统计报表 404 返回首页"),
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
        self.assertFalse(runner._is_404_page())

    def test_普通页面不误判404(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
        self.assertFalse(runner._is_404_page())

    def test_打开启动网址不主动拼登录路由(self):
        page = FakePage(url="http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                start_url="http://example.test",
                timeout=FAST,
            )
            runner._open_start_url()

        self.assertEqual(["http://example.test"], page.goto_calls)

    def test_登录成功后以离开登录页为准(self):
        page = FakePage(url="http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertFalse(runner._login_succeeded())
            page.url = "http://example.test/home"
            self.assertTrue(runner._login_succeeded())

    def test_无验证码输入框时直接提交登录(self):
        page = FakePage(url="http://example.test/login")
        button = FakeElement("登录", True, True, "登录按钮")
        page.add("button", button)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                timeout=FAST,
            )
            username = FakeElement(name="用户名")
            password = FakeElement(name="密码")
            runner._login_input = mock.Mock(
                side_effect=[username, password, None]
            )
            runner._captcha_pixels = mock.Mock()
            runner._ocr_captcha = mock.Mock()
            runner._login_succeeded = mock.Mock(return_value=True)

            self.assertTrue(runner._auto_login_once())

        self.assertEqual("tester", username.value)
        self.assertEqual("secret", password.value)
        runner._captcha_pixels.assert_not_called()
        runner._ocr_captcha.assert_not_called()
        self.assertEqual(
            [("click", "登录按钮")],
            [action for action in page.actions if action[0] == "click"],
        )

    def test_验证码未出现时点击登录触发后再填码(self):
        page = FakePage(url="http://example.test/login")
        button = FakeElement("登录", True, True, "登录按钮")
        page.add("button", button)
        username = FakeElement(name="用户名")
        password = FakeElement(name="密码")
        captcha = FakeElement(name="验证码")
        pixels = {
            "width": 1,
            "height": 1,
            "gray": base64.b64encode(b"\x00").decode("ascii"),
        }
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                timeout=FAST,
            )

            def login_input(labels):
                if "验证码" in labels:
                    return captcha
                if "密码" in labels:
                    return password
                return username

            runner._login_input = login_input
            runner._wait_captcha_pixels = mock.Mock(
                side_effect=[None, pixels]
            )
            runner._ocr_captcha = mock.Mock(return_value="ABCD")
            runner._login_succeeded = mock.Mock(side_effect=[False, True])

            self.assertTrue(runner._auto_login_once())

        self.assertEqual("tester", username.value)
        self.assertEqual("secret", password.value)
        self.assertEqual("ABCD", captcha.value)
        self.assertEqual(2, runner._login_succeeded.call_count)
        self.assertEqual(2, len([a for a in page.actions if a[0] == "click"]))

    def test_应用被动跳登录页后直接登录且不重复导航(self):
        page = FakePage(url="http://example.test/")
        # 等待期间由应用自己把根地址跳到登录页；脚本不应再 goto。
        page.on_wait = lambda: setattr(page, "url", "http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                start_url="http://example.test",
                timeout=FAST,
            )
            runner._auto_login_once = mock.Mock(return_value=True)
            with mock.patch.object(drt, "_LOGIN_SETTLE_TIMEOUT_MS", 50):
                runner.ensure_authenticated()

        self.assertEqual([], page.goto_calls)
        runner._auto_login_once.assert_called_once_with()

    def test_空白页才补开一次启动网址(self):
        page = FakePage(url="about:blank")
        original_goto = page.goto

        def goto(url, wait_until=None):
            original_goto(url, wait_until=wait_until)
            page.url = "http://example.test/login"

        page.goto = goto
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                start_url="http://example.test",
                timeout=FAST,
            )
            runner._auto_login_once = mock.Mock(return_value=True)
            with mock.patch.object(drt, "_LOGIN_SETTLE_TIMEOUT_MS", 50):
                runner.ensure_authenticated()

        self.assertEqual(["http://example.test"], page.goto_calls)
        runner._auto_login_once.assert_called_once_with()

    def test_登录成功后等待首页加载再继续(self):
        page = FakePage(url="http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                timeout=FAST,
            )
            runner._auto_login_once = mock.Mock(return_value=True)
            runner._wait_for_post_login_home = mock.Mock()
            runner.ensure_authenticated()

        runner._wait_for_post_login_home.assert_called_once()

    def test_自动登录后自动进入缺陷识别任务(self):
        page = FakePage(url="http://example.test/home")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
            )
            runner.ensure_authenticated = mock.Mock()
            runner._wait_for_login_or_session = mock.Mock()
            runner._is_task_page_url = mock.Mock(return_value=False)
            runner._goto_task_page_direct = mock.Mock(return_value=False)
            runner._open_menu = mock.Mock()
            runner._click_menu = mock.Mock()
            runner._wait_for_task_list = mock.Mock()

            runner.open_task_list()

        runner.ensure_authenticated.assert_called_once_with()
        runner._goto_task_page_direct.assert_called_once_with()
        runner._click_menu.assert_called_once_with("缺陷识别任务")

    def test_首页加载完成后优先直达任务页(self):
        page = FakePage(url="http://example.test/home")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                start_url="http://example.test",
            )
            runner.ensure_authenticated = mock.Mock()
            runner._session_ready = mock.Mock(return_value=True)
            runner._task_list_visible = mock.Mock(return_value=False)
            runner._goto_task_page_direct = mock.Mock(return_value=True)
            runner._open_menu = mock.Mock()
            changed = runner.open_task_list()

        self.assertTrue(changed)
        runner._goto_task_page_direct.assert_called_once_with()
        runner._open_menu.assert_not_called()


class CaptchaFallbackTest(unittest.TestCase):
    def test_png原图转灰度(self):
        png = make_png_rgba(
            2,
            1,
            [[255, 0, 0, 255, 0, 255, 0, 255]],
        )
        got = drt._png_to_gray(png)
        self.assertEqual(2, got["width"])
        self.assertEqual(1, got["height"])
        raw = base64.b64decode(got["gray"])
        self.assertEqual(2, len(raw))
        self.assertLessEqual(abs(raw[0] - 54), 1)
        self.assertLessEqual(abs(raw[1] - 182), 1)

    def test_DOM取不到像素时取验证码原图(self):
        page = FakePage(url="http://example.test/login")
        image = FakeElement(name="验证码图")
        image.attributes["tag"] = "IMG"
        image.attributes["src"] = "http://other.test/captcha.png"
        image.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        page.add("img", image)
        response = mock.Mock(ok=True, headers={"content-type": "image/png"})
        response.body.return_value = make_png_rgba(
            4,
            2,
            [[0, 0, 0, 255] * 4, [255, 255, 255, 255] * 4],
        )
        page.context = mock.Mock()
        page.context.request.get.return_value = response
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page, tmpdir, timeout=FAST, log_handler=logs.append
            )
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        # 原图是 4x2，不是 40x20 的显示截图。
        self.assertEqual(4, got["width"])
        self.assertEqual(2, got["height"])
        self.assertIn("验证码像素改用原图", " ".join(logs))
        self.assertEqual([], page.screenshots)

    def test_图片响应缓存只记图片(self):
        page = FakePage(url="http://example.test/login")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            image = mock.Mock(
                url="http://example.test/captcha.png",
                headers={"content-type": "image/png"},
            )
            html = mock.Mock(
                url="http://example.test/login",
                headers={"content-type": "text/html"},
            )
            runner._on_image_response(image)
            runner._on_image_response(html)

        self.assertIn("http://example.test/captcha.png", runner._image_responses)
        self.assertNotIn("http://example.test/login", runner._image_responses)

    def test_优先使用页面已加载的验证码原图(self):
        page = FakePage(url="http://example.test/login")
        image = FakeElement(name="验证码图")
        image.attributes["tag"] = "IMG"
        image.attributes["src"] = "http://other.test/captcha.png"
        image.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        page.add("img", image)
        page.context = mock.Mock()
        response = mock.Mock(headers={"content-type": "image/png"})
        response.body.return_value = make_png_rgba(3, 1, [[0, 0, 0, 255] * 3])
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner._image_responses["http://other.test/captcha.png"] = response
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(3, got["width"])
        self.assertEqual(1, got["height"])
        page.context.request.get.assert_not_called()

    def test_页面fetch取验证码原图(self):
        page = FakePage(url="http://example.test/login")
        png = make_png_rgba(3, 1, [[0, 0, 0, 255] * 3])
        image = FakeElement(name="验证码图")
        image.attributes["tag"] = "IMG"
        image.attributes["src"] = "http://other.test/captcha.png"
        image.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        image.fetch_result = {
            "b64": base64.b64encode(png).decode("ascii"),
            "type": "image/png",
        }
        page.add("img", image)
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(3, got["width"])
        self.assertEqual(1, got["height"])

    def test_验证码data_url原图(self):
        page = FakePage(url="http://example.test/login")
        png = make_png_rgba(2, 1, [[0, 0, 0, 255, 255, 255, 255, 255]])
        image = FakeElement(name="验证码图")
        image.attributes["tag"] = "IMG"
        image.attributes["src"] = (
            "data:image/png;base64," + base64.b64encode(png).decode("ascii")
        )
        image.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        page.add("img", image)
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(2, got["width"])
        self.assertEqual(1, got["height"])

    def test_canvas取toDataURL原图(self):
        page = FakePage(url="http://example.test/login")
        png = make_png_rgba(3, 2, [[0, 0, 0, 255] * 3] * 2)
        canvas = FakeElement(name="验证码画布")
        canvas.attributes["tag"] = "CANVAS"
        canvas.attributes["data_url"] = (
            "data:image/png;base64," + base64.b64encode(png).decode("ascii")
        )
        canvas.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        page.add("canvas", canvas)
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(3, got["width"])
        self.assertEqual(2, got["height"])

    def test_验证码显示尺寸过小时仍按原图取像素(self):
        page = FakePage(url="http://example.test/login")
        png = make_png_rgba(40, 18, [[0, 0, 0, 255] * 40] * 18)
        image = FakeElement(name="验证码图")
        image.attributes["tag"] = "IMG"
        image.attributes["src"] = (
            "data:image/png;base64," + base64.b64encode(png).decode("ascii")
        )
        # 现场 CSS 把 130x48 的验证码显示成 23x9。
        image.box = {"x": 544, "y": 447, "width": 23, "height": 9}
        page.add("img", image)
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 493, "y": 446, "width": 47, "height": 10}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(40, got["width"])
        self.assertEqual(18, got["height"])

    def test_验证码延迟加载时会轮询等待(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                FakePage(url="http://example.test/login"), tmpdir, timeout=FAST
            )
            calls = {"count": 0}
            expected = {
                "width": 1,
                "height": 1,
                "gray": base64.b64encode(b"\x00").decode("ascii"),
            }

            def pixels(_near=None):
                calls["count"] += 1
                return expected if calls["count"] >= 3 else None

            runner._captcha_pixels = pixels
            got = runner._wait_captcha_pixels(object(), timeout_ms=1000)

        self.assertEqual(expected, got)
        self.assertEqual(3, calls["count"])

    def test_取原图时选离输入框最近的图(self):
        page = FakePage(url="http://example.test/login")
        far = FakeElement(name="远图")
        far.attributes["tag"] = "IMG"
        far.attributes["src"] = "http://other.test/far.png"
        far.box = {"x": 800, "y": 100, "width": 40, "height": 20}
        near = FakeElement(name="近图")
        near.attributes["tag"] = "IMG"
        near.attributes["src"] = "http://other.test/near.png"
        near.box = {"x": 210, "y": 100, "width": 40, "height": 20}
        page.add("img", far)
        page.add("img", near)
        response = mock.Mock(ok=True, headers={"content-type": "image/png"})
        response.body.return_value = make_png_rgba(1, 1, [[0, 0, 0, 255]])
        page.context = mock.Mock()
        page.context.request.get.return_value = response
        captcha_input = FakeElement(name="验证码输入")
        captcha_input.box = {"x": 100, "y": 100, "width": 90, "height": 24}
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            got = runner._captcha_pixels(captcha_input)

        self.assertIsNotNone(got)
        self.assertEqual(
            "http://other.test/near.png",
            page.context.request.get.call_args[0][0],
        )


class HealthDetectionTest(unittest.TestCase):
    def test_连续慢图判定实例不健康(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                slow_image_seconds=1,
                slow_image_count=3,
            )
            for index in range(3):
                runner._last_image_success_at = drt.time.monotonic() - 2.0
                self.assertTrue(runner._mark_reviewed("pic-%d" % index))
        self.assertTrue(runner.health_unhealthy)
        self.assertEqual(3, runner._slow_image_streak)

    def test_未审核数超过阈值时慢图立即触发重启(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
                slow_image_seconds=1,
                slow_image_count=3,
                slow_restart_unreviewed=5,
            )
            runner.current_unreviewed_count = 10
            with self.assertRaises(drt.BrowserUnhealthy):
                for index in range(3):
                    runner._last_image_success_at = drt.time.monotonic() - 2.0
                    runner._mark_reviewed("pic-%d" % index)
        self.assertTrue(runner.force_browser_restart)
        self.assertTrue(runner.connection_lost)

    def test_不健康任务结束后触发浏览器重启(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                auto_login=True,
                username="tester",
                password="secret",
            )
            runner.health_unhealthy = True
            runner._refresh_and_resume = mock.Mock()
            runner._refresh_after_task(make_settings(), "任务一")
        self.assertTrue(runner.force_browser_restart)
        self.assertTrue(runner.connection_lost)
        runner._refresh_and_resume.assert_called_once()

    def test_单次慢图不判定不健康(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                slow_image_seconds=1,
                slow_image_count=3,
            )
            for index in range(2):
                runner._last_image_success_at = drt.time.monotonic() - 2.0
                runner._mark_reviewed("pic-%d" % index)
        self.assertFalse(runner.health_unhealthy)


class RefreshThresholdTest(unittest.TestCase):
    def test_导航等待超时但列表可见按已加载(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.wait_for_load_state.side_effect = RuntimeError("navigation timeout")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner._task_list_visible = mock.Mock(return_value=True)
            self.assertTrue(runner._wait_page_settled())

    def test_导航等待超时且列表不可见按失败(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.wait_for_load_state.side_effect = RuntimeError("navigation timeout")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner._task_list_visible = mock.Mock(return_value=False)
            self.assertFalse(runner._wait_page_settled())

    def test_未到任务或图片阈值不刷新(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir)
            runner._refresh_and_resume = mock.Mock()
            runner._refresh_after_task(make_settings(), "任务一", task_images=50)

        runner._refresh_and_resume.assert_not_called()
        self.assertEqual(1, runner._tasks_since_refresh)
        self.assertEqual(50, runner._images_since_refresh)

    def test_达到任一阈值刷新并清零(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                FakePage(),
                tmpdir,
                reload_after_tasks=2,
                reload_after_images=100,
            )
            runner._refresh_and_resume = mock.Mock()
            runner._refresh_after_task(make_settings(), "任务一", task_images=10)
            runner._refresh_and_resume.assert_not_called()
            runner._refresh_after_task(make_settings(), "任务二", task_images=10)
            self.assertEqual(1, runner._refresh_and_resume.call_count)
            self.assertEqual(0, runner._tasks_since_refresh)
            self.assertEqual(0, runner._images_since_refresh)

            runner._refresh_and_resume.reset_mock()
            runner.reload_after_tasks = 0
            runner._refresh_after_task(make_settings(), "任务三", task_images=60)
            runner._refresh_and_resume.assert_not_called()
            runner._refresh_after_task(make_settings(), "任务四", task_images=50)
            self.assertEqual(1, runner._refresh_and_resume.call_count)

    def test_两个阈值都为零时不自动刷新(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                FakePage(),
                tmpdir,
                reload_after_tasks=0,
                reload_after_images=0,
            )
            runner._refresh_and_resume = mock.Mock()
            runner._refresh_after_task(make_settings(), "任务一", task_images=999)
            runner._refresh_and_resume.assert_not_called()


class TaskIterationTest(unittest.TestCase):
    def test_只扫描不会进入任何任务(self):
        ui = TaskListUI([record("guid-1", "任务一"), record("guid-2", "任务二")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual(["guid-1", "guid-2"], [e["guid"] for e in entries])
        self.assertEqual([], ui.enter_clicks)
        self.assertEqual(2, runner.last_scan_summary["candidates"])

    def test_不可见或禁用的进入按钮被跳过(self):
        records = [
            record("guid-1", "任务一"),
            record("guid-2", "任务二"),
            record("guid-3", "任务三"),
        ]
        ui = TaskListUI(
            records,
            states={"guid-1": "hidden", "guid-2": "disabled"},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual(["guid-3"], [e["guid"] for e in entries])
        self.assertEqual(2, runner.last_scan_summary["skipped"])
        self.assertEqual([], ui.enter_clicks)

    def test_已审核无进入按钮的行被跳过(self):
        ui = TaskListUI(
            [record("guid-1", "任务一"), record("guid-2", "任务二")],
            states={"guid-1": "none"},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual(["guid-2"], [e["guid"] for e in entries])
        self.assertEqual(1, runner.last_scan_summary["skipped"])

    def test_每轮从第一页重扫且不会重复处理同一任务(self):
        first = record("guid-1", "任务一")
        second = record("guid-2", "任务二")
        ui = TaskListUI([first, second])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            iterator = runner.iter_task_candidates(make_settings())
            self.assertEqual("guid-1", next(iterator)["guid"])
            self.assertEqual(1, ui.query_count)

            ui.set_records([second])
            self.assertEqual("guid-2", next(iterator)["guid"])
            self.assertEqual(2, ui.query_count)

            ui.set_records([])
            with self.assertRaises(StopIteration):
                next(iterator)
            self.assertEqual(3, ui.query_count)

    def test_跳过集合排除后仍能终止(self):
        ui = TaskListUI([record("guid-1", "任务一"), record("guid-2", "任务二")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(
                runner.iter_task_candidates(make_settings(), skipped_ids={"guid-1"})
            )

        self.assertEqual(["guid-2"], [e["guid"] for e in entries])

    def test_表格行与响应名称不一致时拒绝点击(self):
        item = record("guid-1", "任务一")
        ui = TaskListUI([item])
        ui.row_texts["guid-1"] = "别的任务"
        ui.set_records([item])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            with self.assertRaises(RuntimeError):
                list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual([], ui.enter_clicks)

    def test_跨页扫描全列表(self):
        ui = TaskListUI(
            pages=[
                [record("guid-1", "任务一")],
                [record("guid-2", "任务二")],
            ]
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual(["guid-1", "guid-2"], [e["guid"] for e in entries])
        self.assertEqual(1, ui.jump_count)
        self.assertEqual(2, ui.jump_target)
        self.assertEqual(0, ui.next_clicks)
        self.assertEqual(2, runner.last_scan_summary["scanned"])

    def test_进入只点击可见且启用的按钮(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))
            runner.enter_task(entries[0])

        self.assertEqual(["guid-1"], ui.enter_clicks)

    def test_进入任务遇到禁用按钮时拒绝点击(self):
        item = record("guid-1", "任务一")
        ui = TaskListUI([item], states={"guid-1": "disabled"})
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entry = {
                "item": item,
                "guid": "guid-1",
                "row": ui.rows["guid-1"],
            }
            with self.assertRaises(RuntimeError):
                runner.enter_task(entry)

        self.assertEqual([], ui.enter_clicks)


class CloseTest(unittest.TestCase):
    def test_关闭只断开连接不关闭浏览器或页面(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner._ensure_monitor()
            runner.playwright = FakePlaywright()
            runner.browser = FakeBrowser()
            runner.close()

        self.assertTrue(runner.playwright is None)
        self.assertFalse(ui.page.closed)
        self.assertFalse(runner.browser.closed)
        self.assertEqual([], ui.page.listeners.get("response", []))


class FakeCliRunner:
    instances = []

    def __init__(self, args, outdir):
        self.args = args
        self.outdir = outdir
        self.page = None
        self.calls = []
        self.last_scan_summary = {"candidates": 1}
        FakeCliRunner.instances.append(self)

    def log(self, message):
        pass

    def load_playwright(self):
        self.calls.append("load_playwright")

    def connect(self, launched=False):
        self.calls.append(("connect", launched))

    def ensure_authenticated(self):
        self.calls.append("ensure_authenticated")

    def open_task_list(self):
        self.calls.append("open_task_list")

    def apply_filters(self, settings):
        self.calls.append(("apply_filters", settings))

    def iter_task_candidates(self, settings, rescan=True):
        self.calls.append(("iter_task_candidates", rescan))
        yield {
            "item": record("guid-1", "任务一"),
            "guid": "guid-1",
        }

    def enter_task(self, entry):
        self.calls.append(("enter_task", entry))
        raise AssertionError("--scan-only 不得进入任务")

    def close(self):
        self.calls.append("close")


class ScanOnlyMainTest(unittest.TestCase):
    def test_只扫描命令行不进入任务(self):
        FakeCliRunner.instances = []
        with tempfile.TemporaryDirectory() as tmpdir:
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = drt.main(
                    [
                        "--scan-only",
                        "--task-status",
                        "待人工审核",
                        "--mission-type",
                        "精细化巡检",
                        "--voltage-level",
                        "交流220kV",
                        "--line-name",
                        "测试线路",
                        "--start-date",
                        "2026-11-01",
                        "--end-date",
                        "2026-11-02",
                        "--out",
                        tmpdir,
                    ],
                    runner_factory=FakeCliRunner,
                )

        self.assertEqual(0, code)
        runner = FakeCliRunner.instances[0]
        self.assertIn("load_playwright", runner.calls)
        self.assertIn(("connect", False), runner.calls)
        self.assertIn("open_task_list", runner.calls)
        self.assertIn(("iter_task_candidates", False), runner.calls)
        self.assertNotIn(
            "enter_task",
            [call[0] if isinstance(call, tuple) else call for call in runner.calls],
        )
        self.assertIn("close", runner.calls)


class VoltageChangeTest(unittest.TestCase):
    def test_同值电压也强制先选相邻项再选目标(self):
        ui = TaskListUI([record("guid-1", "任务一")], initial_voltage="交流220kV")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=1)
            runner.apply_filters(make_settings())

        self.assertNotEqual("交流220kV", ui.line_requests[0])
        self.assertEqual("交流220kV", ui.line_requests[-1])
        self.assertEqual("交流220kV", ui.voltage.selected)
        self.assertEqual(0, ui.query_count)

    def test_目标电压无变更响应时停止且不查询(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")],
            suppress_line_for={"交流220kV"},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=1)
            with self.assertRaises(RuntimeError) as ctx:
                runner.apply_filters(make_settings())

        self.assertIn("目标电压", str(ctx.exception))
        self.assertEqual(0, ui.query_count)

    def test_目标电压线路候选为空时停止(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")],
            line_payloads={"交流220kV": ok_payload([])},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=1)
            with self.assertRaises(RuntimeError) as ctx:
                runner.apply_filters(make_settings())

        self.assertIn("线路候选", str(ctx.exception))
        self.assertEqual(0, ui.query_count)

    def test_线路响应电压与目标不一致时停止(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")],
            line_code_override={"交流220kV": "32"},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=1)
            with self.assertRaises(RuntimeError) as ctx:
                runner.apply_filters(make_settings())

        self.assertIn("目标电压", str(ctx.exception))
        self.assertEqual(0, ui.query_count)


class FirstPageScanTest(unittest.TestCase):
    def test_查询返回第二页时自动回第一页后扫描(self):
        ui = TaskListUI(
            pages=[
                [record("guid-1", "任务一")],
                [record("guid-2", "任务二")],
            ]
        )
        ui.query_current = 2
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries = list(runner.iter_task_candidates(make_settings(), rescan=False))

        self.assertEqual(["guid-1", "guid-2"], [e["guid"] for e in entries])
        self.assertEqual(2, ui.jump_count)
        self.assertEqual(2, ui.jump_target)
        self.assertEqual(0, ui.next_clicks)
        self.assertEqual(1, ui.query_count)

    def test_换页时直接跳转目标页不再逐页点击(self):
        ui = TaskListUI(
            pages=[
                [record("guid-1", "任务一")],
                [record("guid-2", "任务二")],
                [record("guid-3", "任务三")],
            ]
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries, _summary = runner.scan_task_list(
                make_settings(),
                excluded_ids={"guid-1", "guid-2"},
                stop_at_first=True,
            )

        self.assertEqual(["guid-3"], [e["guid"] for e in entries])
        self.assertEqual(2, ui.jump_count)
        self.assertEqual(3, ui.jump_target)
        self.assertEqual(0, ui.next_clicks)

    def test_从指定页扫描时记录真实页码(self):
        ui = TaskListUI(
            pages=[
                [record("guid-1", "任务一")],
                [record("guid-2", "任务二")],
                [record("guid-3", "任务三")],
            ]
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            entries, _summary = runner.scan_task_list(
                make_settings(), stop_at_first=True, start_page=3
            )

        self.assertEqual(["guid-3"], [e["guid"] for e in entries])
        self.assertEqual(3, entries[0]["page"])

    def test_下一条任务从上一任务所在页继续扫描(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            calls = []

            def fake_scan(_settings, excluded, stop_at_first, start_page):
                calls.append((excluded, start_page))
                if not excluded:
                    return (
                        [{"guid": "guid-1", "page": 3, "item": {}}],
                        {"total": 1},
                    )
                return [], {"total": 1}

            runner.scan_task_list = fake_scan
            entries = list(runner.iter_task_candidates(make_settings()))

        self.assertEqual(["guid-1"], [e["guid"] for e in entries])
        self.assertEqual([None, 3], [page for _excluded, page in calls])


class QueryConditionTest(unittest.TestCase):
    def _runner_with_filters(self, ui, tmpdir):
        runner = make_runner(ui.page, tmpdir)
        runner.apply_filters(make_settings())
        return runner

    def test_默认请求与记录一致时可用(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            records, total, current, pages = runner.query_tasks(make_settings())

        self.assertEqual(1, len(records))
        self.assertEqual(1, total)
        self.assertEqual(1, current)
        self.assertEqual(1, pages)

    def test_记录线路与筛选不一致时拒绝(self):
        ui = TaskListUI([record("guid-1", "任务一", line="别的线路")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        message = str(ctx.exception)
        self.assertIn("别的线路", message)
        self.assertIn("测试线路", message)

    def test_记录缺少线路字段时拒绝(self):
        item = record("guid-1", "任务一")
        item["lineTowerName"] = []
        ui = TaskListUI([item])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        self.assertIn("lineTowerName", str(ctx.exception))

    def test_请求任务状态与筛选不一致时拒绝(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")], request_task_status="task_completed"
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        self.assertIn("taskStatus", str(ctx.exception))

    def test_请求作业性质与筛选不一致时拒绝(self):
        ui = TaskListUI(
            [record("guid-1", "任务一")], request_mission_type="union_inspect"
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        self.assertIn("missionType", str(ctx.exception))

    def test_请求不暴露任务状态时核对记录(self):
        item = record("guid-1", "任务一")
        item["recognitionTaskStatus"] = "task_completed"
        ui = TaskListUI([item], request_task_status=None)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_filters(ui, tmpdir)
            with self.assertRaises(RuntimeError) as ctx:
                runner.query_tasks(make_settings())

        self.assertIn("recognitionTaskStatus", str(ctx.exception))


class FakeSyncPlaywright:
    def __init__(self):
        self.started = False

    def __call__(self):
        return self

    def start(self):
        self.started = True
        raise AssertionError("不应在 CDP 校验前启动 Playwright")


class ConnectValidationTest(unittest.TestCase):
    def test_非回环CDP地址在连接前拒绝(self):
        fake_playwright = FakeSyncPlaywright()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, cdp_url="http://10.0.0.5:9222")
            runner.sync_playwright = fake_playwright
            with self.assertRaises(RuntimeError) as ctx:
                runner.connect()

        self.assertIn("本机", str(ctx.exception))
        self.assertFalse(fake_playwright.started)

    def test_重连复用同一个Playwright实例(self):
        page = FakePage(url="http://example.test/")
        context = mock.Mock()
        context.pages = [page]
        browser = mock.Mock()
        browser.contexts = [context]

        class ReusablePlaywright:
            def __init__(self):
                self.start_count = 0
                self.chromium = mock.Mock()
                self.chromium.connect_over_cdp.return_value = browser

            def start(self):
                self.start_count += 1
                return self

        playwright = ReusablePlaywright()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.sync_playwright = mock.Mock(return_value=playwright)
            runner.connect()
            self.assertIs(playwright, runner.playwright)
            runner.page = None
            runner.connect()

        self.assertEqual(1, playwright.start_count)
        self.assertIs(playwright, runner.playwright)

    def test_自动登录连接不重开启动网址(self):
        page = FakePage(url="http://example.test/login")
        context = mock.Mock()
        context.pages = [page]
        browser = mock.Mock()
        browser.contexts = [context]

        class FakeDriver:
            def __init__(self):
                self.chromium = mock.Mock()
                self.chromium.connect_over_cdp.return_value = browser

            def start(self):
                return self

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                timeout=FAST,
                auto_login=True,
                start_url="http://example.test/",
            )
            runner.sync_playwright = mock.Mock(return_value=FakeDriver())
            runner.connect()

        self.assertEqual([], page.goto_calls)

    def test_驱动进程退出后重建并重连(self):
        page = FakePage(url="http://example.test/")
        context = mock.Mock()
        context.pages = [page]
        browser = mock.Mock()
        browser.contexts = [context]

        class DyingDriver:
            def __init__(self):
                self.start_count = 0
                self.stop_count = 0
                self.chromium = mock.Mock()
                self.chromium.connect_over_cdp.side_effect = [
                    Exception(
                        "BrowserType.connect_over_cdp: "
                        "Connection closed while reading from the driver"
                    ),
                    browser,
                ]

            def start(self):
                self.start_count += 1
                return self

            def stop(self):
                self.stop_count += 1

        driver = DyingDriver()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.sync_playwright = mock.Mock(return_value=driver)
            runner.connect()

        self.assertEqual(2, driver.start_count)
        self.assertEqual(1, driver.stop_count)
        self.assertEqual(0, runner._driver_reset_count)

    def test_驱动持续退出时只重建一次(self):
        page = FakePage(url="http://example.test/")

        class DyingDriver:
            def __init__(self):
                self.start_count = 0
                self.stop_count = 0
                self.chromium = mock.Mock()
                self.chromium.connect_over_cdp.side_effect = Exception(
                    "BrowserType.connect_over_cdp: "
                    "Connection closed while reading from the driver"
                )

            def start(self):
                self.start_count += 1
                return self

            def stop(self):
                self.stop_count += 1

        driver = DyingDriver()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.sync_playwright = mock.Mock(return_value=driver)
            with self.assertRaises(RuntimeError):
                runner.connect()

        self.assertEqual(2, driver.start_count)
        self.assertEqual(1, driver.stop_count)
        self.assertEqual(1, runner._driver_reset_count)


class WatchdogTest(unittest.TestCase):
    def test_重启浏览器只打开配置根地址(self):
        page = FakePage(url="http://example.test/")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                timeout=FAST,
                start_url=(
                    "http://example.test/"
                    "intelligentAnalysis/identification/IdentifyTasks"
                ),
            )
            launched = {}

            def fake_launch(args, port, log):
                launched["url"] = args.start_url

            runner._sleep_without_page = mock.Mock()
            runner.connect = mock.Mock()
            runner.ensure_authenticated = mock.Mock()
            runner.open_task_list = mock.Mock()
            with (
                mock.patch.object(drt, "close_browser_for_restart"),
                mock.patch.object(drt, "cdp_reachable", return_value=False),
                mock.patch.object(drt, "launch_browser", side_effect=fake_launch),
            ):
                runner._restart_browser()

        self.assertEqual("http://example.test/", launched["url"])
    def test_页面崩溃后即使连接已断开也强制关浏览器(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner.page_crashed = True
            runner.connection_lost = True
            called = threading.Event()
            with mock.patch.object(
                drt,
                "close_browser_for_restart",
                side_effect=lambda *_args, **_kwargs: called.set(),
            ):
                runner._start_watchdog()
                self.assertTrue(called.wait(3))
            runner._stop_watchdog()

    def test_看门狗不阻塞runner回收(self):
        page = FakePage()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir)
            runner._start_watchdog()
            thread = runner._watchdog_thread
            self.assertTrue(thread.is_alive())
            # 先让看门狗至少跑完一轮：线程局部变量若跨 wait 持有 runner，
            # 弱引用就永远不会失效，任务对象无法回收。
            time.sleep(2.2)
            del runner
            gc.collect()
            thread.join(5)

        self.assertFalse(thread.is_alive())


class EnterButtonSafetyTest(unittest.TestCase):
    def _entry(self, ui, item):
        return {
            "item": item,
            "guid": item["recognitionTaskGuid"],
            "row": ui.rows[item["recognitionTaskGuid"]],
        }

    def test_aria_disabled锚点不进入(self):
        item = record("guid-1", "任务一")
        ui = TaskListUI([item])
        row = ui.rows["guid-1"]
        row.children[TASK_ENTER] = []
        anchor = FakeElement("进入", True, True, "进入")
        anchor.attributes["aria-disabled"] = "true"
        anchor.on_click = lambda: ui.enter_clicks.append("anchor")
        row.add(TASK_ENTER, anchor)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            with self.assertRaises(RuntimeError):
                runner.enter_task(self._entry(ui, item))

        self.assertEqual([], ui.enter_clicks)

    def test_祖先aria_disabled时按钮不进入(self):
        item = record("guid-1", "任务一")
        ui = TaskListUI([item])
        ui.rows["guid-1"].attributes["aria-disabled"] = "true"
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            with self.assertRaises(RuntimeError):
                runner.enter_task(self._entry(ui, item))

        self.assertEqual([], ui.enter_clicks)

    def test_优先点击真正的button而不是aria_disabled锚点(self):
        item = record("guid-1", "任务一")
        ui = TaskListUI([item])
        row = ui.rows["guid-1"]
        row.children[TASK_ENTER] = []
        anchor = FakeElement("进入", True, True, "进入")
        anchor.attributes["aria-disabled"] = "true"
        anchor.on_click = lambda: ui.enter_clicks.append("anchor")
        row.add(TASK_ENTER, anchor)
        button = FakeElement("进入", True, True, "进入按钮")
        button.on_click = lambda: ui.enter_clicks.append("button")
        row.add("button", button)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir)
            runner.enter_task(self._entry(ui, item))

        self.assertEqual(["button"], ui.enter_clicks)


PIC_GROUP_API = "/api/defect-server/code/picturedetail/getPicGroup"
PIC_DETAIL_API = "/api/defect-server/code/picturedetail/getPictureDetailList"
DOWNLOAD_API = "/api/allcore-file/publicFile/downLoadFile"
PICTURE_REVIEW_API = "/api/defect-server/code/recognitiontaskinfo/pictureReview"
SUBMIT_AUDIT_API = "/api/defect-server/code/recognitiontaskinfo/submitAudit"
FAST = 0.05


def image_item(
    picture_guid,
    file_guid,
    audit_status="unreviewed",
    pic_defect_flag="",
    line_name="测试线路",
    tower_name="#12",
):
    return {
        "pictureDetailGuid": picture_guid,
        "fileGuid": file_guid,
        "auditStatus": audit_status,
        "picDefectFlg": pic_defect_flag,
        "lineName": line_name,
        "deviceName": tower_name,
        "originalName": picture_guid + ".jpg",
        "shootTime": "2026-11-01 10:00:00",
    }


def group_payload(items):
    return ok_payload(
        {
            "examineList": [],
            "noTaggingList": list(items),
            "noExamineList": list(items),
            "taggingList": [],
            "pictureListAll": list(items),
        }
    )


def detail_payload(items):
    return ok_payload([{"lineName": "测试线路", "pictureDetailVOS": list(items)}])


class ParseImagesTest(unittest.TestCase):
    def test_按顺序返回图片GUID与文件GUID(self):
        items = [image_item("pic-1", "file-1"), image_item("pic-2", "file-2")]
        images = drt.parse_task_images(group_payload(items), detail_payload(items))

        self.assertEqual(2, len(images))
        self.assertEqual(
            ("pic-1", "file-1", 0),
            (
                images[0].picture_guid,
                images[0].file_guid,
                images[0].index,
            ),
        )
        self.assertEqual(
            ("pic-2", "file-2", 1),
            (
                images[1].picture_guid,
                images[1].file_guid,
                images[1].index,
            ),
        )

    def test_缺少pictureListAll时合并其他分组并去重(self):
        first = image_item("pic-1", "file-1")
        second = image_item("pic-2", "file-2")
        payload = ok_payload(
            {
                "examineList": [second],
                "noTaggingList": [first],
                "noExamineList": [second],
                "taggingList": [],
            }
        )
        images = drt.parse_task_images(payload)

        self.assertEqual(["pic-2", "pic-1"], [image.picture_guid for image in images])

    def test_空图片列表被拒绝(self):
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_task_images(group_payload([]), detail_payload([]))
        self.assertIn("没有图片", str(ctx.exception))

    def test_缺少图片GUID或文件GUID被拒绝(self):
        cases = (
            {"fileGuid": "file-1"},
            {"pictureDetailGuid": "pic-1"},
            {"pictureDetailGuid": "  ", "fileGuid": "file-1"},
            "坏记录",
        )
        for item in cases:
            with self.subTest(item=item):
                with self.assertRaises(RuntimeError):
                    drt.parse_task_images(group_payload([item]))

    def test_重复图片GUID被拒绝(self):
        items = [image_item("pic-1", "file-1"), image_item("pic-1", "file-2")]
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_task_images(group_payload(items))
        self.assertIn("重复", str(ctx.exception))

    def test_明细与分组文件GUID不一致被拒绝(self):
        grouped = [image_item("pic-1", "file-1")]
        detail = [image_item("pic-1", "file-other")]
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_task_images(group_payload(grouped), detail_payload(detail))
        self.assertIn("file-1", str(ctx.exception))
        self.assertIn("file-other", str(ctx.exception))

    def test_明细缺少对应图片被拒绝(self):
        grouped = [image_item("pic-1", "file-1")]
        with self.assertRaises(RuntimeError) as ctx:
            drt.parse_task_images(group_payload(grouped), detail_payload([]))
        self.assertIn("pic-1", str(ctx.exception))

    def test_保留系统缺陷标识并从明细补齐线路杆塔(self):
        grouped = [
            image_item(
                "pic-1",
                "file-1",
                pic_defect_flag="already_annotation",
                line_name="",
                tower_name="",
            )
        ]
        detail = ok_payload(
            [
                {
                    "lineName": "220kV吕汾Ⅰ线",
                    "towerName": "#162",
                    "pictureDetailVOS": list(grouped),
                }
            ]
        )
        images = drt.parse_task_images(group_payload(grouped), detail)

        self.assertEqual(1, len(images))
        self.assertEqual("already_annotation", images[0].pic_defect_flag)
        self.assertEqual("220kV吕汾Ⅰ线", images[0].line_name)
        self.assertEqual("#162", images[0].tower_name)
        self.assertTrue(drt.is_system_defect_image(images[0]))
        self.assertFalse(drt.is_system_defect_image(image_item("pic-2", "file-2")))

    def test_按审核状态区分未打勾和已打勾(self):
        unreviewed = image_item("pic-1", "file-1", "unreviewed", "no_defect")
        reviewed = image_item("pic-2", "file-2", "reviewed", "unlabeled")
        defect = image_item("pic-3", "file-3", "unreviewed", "already_annotation")

        self.assertTrue(drt.is_unreviewed_image(unreviewed))
        self.assertFalse(drt.is_unreviewed_image(reviewed))
        self.assertTrue(drt.is_reviewable_image(unreviewed))
        self.assertFalse(drt.is_reviewable_image(reviewed))
        self.assertFalse(drt.is_reviewable_image(defect))

    def test_系统缺陷框明细压缩为汇总列(self):
        details = [
            {
                "defectName": "导地线类-异物",
                "defectLevelName": "一般",
                "detailedDescription": "左侧导线有异物悬挂",
                "xmin": 322,
                "ymin": 1107,
                "xmax": 492,
                "ymax": 1175,
                "shortName": "仿线算法",
                "algorithmType": "yes",
            },
            {
                "defectName": "导地线类-异物",
                "defectLevelName": "一般",
                "xmin": 322,
                "ymin": 1107,
                "xmax": 492,
                "ymax": 1175,
                "shortName": "仿线算法",
                "algorithmType": "yes",
            },
        ]
        summary = drt.summarize_tagging_details(details)

        self.assertEqual("导地线类-异物（左侧导线有异物悬挂）", summary["description"])
        self.assertEqual("一般", summary["level"])
        self.assertEqual("322,1107,492,1175", summary["coordinate"])
        self.assertEqual("仿线算法", summary["algorithm"])


class SystemDefectXlsxTest(unittest.TestCase):
    def test_表头包含系统列名且内容可读回(self):
        row = [
            "交流220kV",
            "220kV吕汾Ⅰ线",
            "220kV吕汾Ⅰ线：#162",
            "#162",
            "任务一",
            "工单-1",
            "pic-1.jpg",
            "2026-11-01 10:00:00",
            "导地线类-异物",
            "一般",
            "322,1107,492,1175",
            "仿线算法",
            "task-1",
            "pic-1",
            "已跳过，待人工审核",
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "系统缺陷框汇总.xlsx"
            count = drt.write_table_xlsx(
                path,
                drt.DEFECT_SHEET_NAME,
                drt.SYSTEM_DEFECT_HEADERS,
                [row],
                log=lambda message: None,
            )
            rows = read_xlsx_rows(str(path))

        self.assertEqual(1, count)
        self.assertEqual(list(drt.SYSTEM_DEFECT_HEADERS), rows[0])
        self.assertEqual(row, rows[1])
        self.assertIn("电压等级", rows[0])
        self.assertIn("线路名称", rows[0])
        self.assertIn("线路杆塔", rows[0])


    def test_单文件包含缺陷与异常两个工作表(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / drt.SUMMARY_XLSX_NAME
            counts = drt.write_workbook_xlsx(
                path,
                (
                    (drt.DEFECT_SHEET_NAME, drt.SYSTEM_DEFECT_HEADERS, []),
                    (drt.ANOMALY_SHEET_NAME, drt.ANOMALY_HEADERS, [["x"] * 15]),
                ),
                log=lambda message: None,
            )
            defect_rows = read_xlsx_rows(str(path), drt.DEFECT_SHEET_NAME)
            anomaly_rows = read_xlsx_rows(str(path), drt.ANOMALY_SHEET_NAME)
            names = list(counts)

        self.assertEqual([drt.DEFECT_SHEET_NAME, drt.ANOMALY_SHEET_NAME], names)
        self.assertEqual(list(drt.SYSTEM_DEFECT_HEADERS), defect_rows[0])
        self.assertEqual(list(drt.ANOMALY_HEADERS), anomaly_rows[0])
        self.assertEqual(1, len(anomaly_rows) - 1)

    def test_写入失败保留旧汇总表(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / drt.SUMMARY_XLSX_NAME
            drt.write_workbook_xlsx(
                path,
                [(drt.DEFECT_SHEET_NAME, drt.SYSTEM_DEFECT_HEADERS, [["旧数据"]])],
                log=lambda message: None,
            )
            before = path.read_bytes()
            with self.assertRaises(TypeError):
                drt.write_workbook_xlsx(
                    path,
                    [(drt.DEFECT_SHEET_NAME, drt.SYSTEM_DEFECT_HEADERS, [object()])],
                    log=lambda message: None,
                )
            self.assertEqual(before, path.read_bytes())
            self.assertEqual([], list(Path(tmpdir).glob("*.tmp")))


class DefectPhotoArchiveTest(unittest.TestCase):
    @staticmethod
    def _image(**overrides):
        values = {
            "picture_guid": "pic-1",
            "file_guid": "file-1",
            "original_name": "DJI_0001.JPG",
            "line_name": "110kV胜西Ⅰ线",
            "tower_name": "43#",
        }
        values.update(overrides)
        return drt.ReviewImage(**values)

    @staticmethod
    def _context(page, body, content_type="image/jpeg"):
        response = mock.Mock()
        response.ok = True
        response.status = 200
        response.body.return_value = body
        response.headers = {"content-type": content_type}
        page.context = mock.Mock()
        page.context.request.get.return_value = response
        return response

    def test_按线路杆塔保存原图并保留原文件名(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"fake-jpeg-body"
            self._context(page, body, "image/jpeg")
            image = self._image(
                original_name="DJI_20260521141326_0004_Z.JPG",
                line_name="110kV胜西Ⅰ线",
                tower_name="43#",
            )
            runner._save_defect_photo(
                {"line_name": "备用线路", "tower_name": "#1"}, image, []
            )

            path = (
                Path(tmpdir)
                / "110kV胜西Ⅰ线"
                / "43#"
                / "DJI_20260521141326_0004_Z.JPG"
            )
            self.assertEqual(body, path.read_bytes())
            self.assertFalse(
                (path.parent / "DJI_20260521141326_0004_Z_标注.JPG").exists()
            )
            url = page.context.request.get.call_args[0][0]
            self.assertIn(drt.FILE_DOWNLOAD_API, url)
            self.assertIn("fileGuid=file-1", url)
            self.assertIn("isThumb=no", url)

    def test_标注复用当前页不打开空白标签(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            page.context = mock.Mock()
            page.evaluate = mock.Mock(
                return_value={
                    "b64": base64.b64encode(b"marked-body").decode("ascii"),
                    "type": "image/jpeg",
                }
            )
            marked = runner._annotate_photo(
                b"\xff\xd8\xff\xe0" + b"original",
                "image/jpeg",
                [{"xmin": 1, "ymin": 1, "xmax": 2, "ymax": 2, "label": "x"}],
            )
            self.assertEqual((b"marked-body", "image/jpeg", 0, 0), marked)
            page.context.new_page.assert_not_called()

    def test_缺陷框另存同名标注图(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"fake-jpeg-body"
            self._context(page, body, "image/jpeg")
            image = self._image(original_name="DJI_0002", tower_name="#44")
            details = [
                {
                    "defectName": "金具类-偏斜",
                    "xmin": 10,
                    "ymin": 20,
                    "xmax": 100,
                    "ymax": 200,
                },
                {
                    "defectName": "导地线类-松股",
                    "xmin": 210,
                    "ymin": 30,
                    "xmax": 320,
                    "ymax": 180,
                },
            ]
            with mock.patch.object(
                runner,
                "_annotate_photo",
                return_value=(b"marked-body", "image/jpeg", 2, 0),
            ) as annotate:
                runner._save_defect_photo({}, image, details)

            directory = Path(tmpdir) / "110kV胜西Ⅰ线" / "#44"
            self.assertEqual(body, (directory / "DJI_0002.jpg").read_bytes())
            self.assertEqual(
                b"marked-body", (directory / "DJI_0002_标注.jpg").read_bytes()
            )
            boxes = annotate.call_args[0][2]
            self.assertEqual(2, len(boxes))
            self.assertEqual("金具类-偏斜", boxes[0]["label"])
            self.assertEqual("导地线类-松股", boxes[1]["label"])
            self.assertFalse(boxes[0]["normalized"])
            self.assertFalse(boxes[1]["normalized"])

    def test_标注零框时不算标注成功(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"fake-jpeg-body"
            self._context(page, body, "image/jpeg")
            logs = []
            runner.log_handler = logs.append
            details = [{"defectName": "金具类-偏斜", "xmin": 10, "ymin": 20,
                        "xmax": 900, "ymax": 900}]
            with mock.patch.object(
                runner,
                "_annotate_photo",
                return_value=(b"marked-body", "image/jpeg", 0, 1),
            ):
                runner._save_defect_photo({}, self._image(original_name="zero"), details)

            directory = Path(tmpdir) / "110kV胜西Ⅰ线" / "43#"
            marked_exists = (directory / "zero_标注.jpg").exists()
            original_exists = (directory / "zero.jpg").exists()

        self.assertEqual(0, runner.photos_marked)
        self.assertEqual(1, runner.photo_failures)
        self.assertTrue(original_exists)
        self.assertFalse(marked_exists)
        self.assertTrue(any("未画出任何缺陷框" in line for line in logs))

    def test_同名原图标注名按实际落盘名派生(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            page.context = mock.Mock()
            responses = []
            for marker in (b"A", b"B"):
                response = mock.Mock()
                response.ok = True
                response.status = 200
                response.body.return_value = b"\xff\xd8\xff" + marker
                response.headers = {"content-type": "image/jpeg"}
                responses.append(response)
            page.context.request.get.side_effect = responses
            details = [{"defectName": "偏斜", "xmin": 1, "ymin": 1,
                        "xmax": 9, "ymax": 9}]
            with mock.patch.object(
                runner,
                "_annotate_photo",
                return_value=(b"marked", "image/jpeg", 1, 0),
            ):
                for _ in range(2):
                    runner._save_defect_photo(
                        {},
                        self._image(
                            picture_guid="p1",
                            file_guid="f1",
                            original_name="same.jpg",
                        ),
                        details,
                    )
            directory = Path(tmpdir) / "110kV胜西Ⅰ线" / "43#"
            names = sorted(path.name for path in directory.iterdir())

        self.assertEqual(
            ["same (2).jpg", "same (2)_标注.jpg", "same.jpg", "same_标注.jpg"],
            names,
        )

    def test_照片写入原子且失败不留半截文件(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            target = Path(tmpdir) / "photo.jpg"
            target.write_bytes(b"old-body")

            written = runner._write_photo_file(target, b"new-body")
            self.assertEqual("photo (2).jpg", written.name)
            self.assertEqual(b"old-body", target.read_bytes())
            self.assertEqual(b"new-body", written.read_bytes())

            with mock.patch.object(
                drt.os, "link", side_effect=RuntimeError("写入中断")
            ):
                with self.assertRaises(RuntimeError):
                    runner._write_photo_file(Path(tmpdir) / "broken.jpg", b"half")

            leftovers = sorted(path.name for path in Path(tmpdir).iterdir())

        self.assertFalse((Path(tmpdir) / "broken.jpg").exists())
        self.assertEqual(
            ["photo (2).jpg", "photo.jpg"],
            [name for name in leftovers if name != ".DS_Store"],
        )

    def test_标注脚本越界像素框丢弃而非夹取(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("缺少 node，无法执行页面标注脚本")
        harness = (
            "const script = ("
            + drt._PHOTO_ANNOTATE_SCRIPT
            + r"""
);
const calls = [];
const ctx = {
  lineWidth: 0, strokeStyle: '', font: '', textBaseline: '', fillStyle: '',
  drawImage() {},
  strokeRect(x, y, w, h) { calls.push(['strokeRect', x, y, w, h]); },
  fillRect() {},
  fillText() {},
  measureText() { return { width: 10 }; },
};
global.Image = class {
  constructor() { this.naturalWidth = 1000; this.naturalHeight = 800; }
  set src(value) { this._src = value; setTimeout(() => this.onload(), 0); }
};
global.document = {
  createElement() {
    return {
      width: 0, height: 0,
      getContext() { return ctx; },
      toDataURL() { return 'data:image/jpeg;base64,ZmFrZQ=='; },
    };
  },
};
const payload = {
  dataUrl: 'data:image/jpeg;base64,ZmFrZQ==',
  outputMime: 'image/jpeg',
  boxes: [
    { xmin: 10, ymin: 20, xmax: 110, ymax: 220, label: 'a', normalized: false },
    { xmin: 900, ymin: 20, xmax: 1400, ymax: 220, label: 'b', normalized: false },
    { xmin: -50, ymin: 10, xmax: 100, ymax: 100, label: 'c', normalized: false },
    { xmin: 0.1, ymin: 0.1, xmax: 0.2, ymax: 0.2, label: 'd', normalized: true },
  ],
};
script(payload).then((result) => {
  console.log(JSON.stringify({ result, calls }));
});
"""
        )
        completed = subprocess.run(
            [node, "-e", harness],
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(0, completed.returncode, completed.stderr)
        data = json.loads(completed.stdout.strip().splitlines()[-1])

        self.assertEqual(2, data["result"]["drawn"])
        self.assertEqual(2, data["result"]["skipped"])
        self.assertEqual(1000, data["result"]["width"])
        self.assertEqual(800, data["result"]["height"])
        rects = [call[1:] for call in data["calls"] if call[0] == "strokeRect"]
        # 像素框 10,20-110,220 与归一化框 100,80-200,160；越界框既不画也不夹取。
        self.assertIn([10, 20, 100, 200], rects)
        self.assertIn([100, 80, 100, 80], rects)
        self.assertEqual(2, len(rects))

    def test_多杆塔任务汇总优先图片级线路杆塔(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, timeout=FAST)
            task_info = {
                "task_name": "多杆塔任务",
                "task_guid": "guid-1",
                "line_name": "110kV胜西Ⅰ线",
                "line_tower": "110kV胜西Ⅰ线：#1、110kV胜西Ⅰ线：#2",
                "tower_name": "#1",
            }
            runner._record_system_defect(
                task_info,
                make_settings(),
                self._image(
                    picture_guid="pic-2",
                    file_guid="file-2",
                    tower_name="#2",
                    original_name="pic-2.jpg",
                ),
                [],
            )
            runner._record_anomaly(
                task_info,
                make_settings(),
                "图片加载失败",
                "超时",
                image=self._image(
                    picture_guid="pic-3",
                    file_guid="file-3",
                    tower_name="#3",
                ),
            )
            runner._record_anomaly(
                task_info,
                make_settings(),
                "按钮未就绪",
                "超时",
                image=self._image(
                    picture_guid="pic-4",
                    file_guid="file-4",
                    line_name="",
                    tower_name="",
                ),
            )
            defect_row = runner.system_defect_rows[0]
            anomaly_rows = runner.anomaly_rows

        self.assertEqual("110kV胜西Ⅰ线", defect_row[1])
        self.assertEqual("110kV胜西Ⅰ线：#2", defect_row[2])
        self.assertEqual("#2", defect_row[3])
        self.assertEqual("110kV胜西Ⅰ线：#3", anomaly_rows[0][8])
        self.assertEqual("#3", anomaly_rows[0][9])
        # 图片没有自带杆塔时回退到任务级线路杆塔。
        self.assertEqual(
            "110kV胜西Ⅰ线：#1、110kV胜西Ⅰ线：#2", anomaly_rows[1][8]
        )
        self.assertEqual("#1", anomaly_rows[1][9])

    def test_汇总线路杆塔逐字段回退到任务级(self):
        """图片只带杆塔时，线路名必须回退任务级，不能留空也不能拼出「：杆塔」。"""
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, timeout=FAST)
            task_info = {
                "task_name": "逐字段回退任务",
                "task_guid": "guid-2",
                "line_name": "110kV胜西Ⅰ线",
                "line_tower": "110kV胜西Ⅰ线：#1",
                "tower_name": "#1",
            }
            runner._record_anomaly(
                task_info,
                make_settings(),
                "图片加载失败",
                "超时",
                image=self._image(
                    picture_guid="pic-5",
                    file_guid="file-5",
                    line_name="",
                    tower_name="#7",
                ),
            )
            runner._record_anomaly(
                task_info,
                make_settings(),
                "按钮未就绪",
                "超时",
                image=self._image(
                    picture_guid="pic-6",
                    file_guid="file-6",
                    line_name="220kV龙湾线",
                    tower_name="",
                ),
            )
            only_tower = runner.anomaly_rows[0]
            only_line = runner.anomaly_rows[1]

        # 只有杆塔：线路回退任务级，拼出完整「线路：杆塔」
        self.assertEqual("110kV胜西Ⅰ线", only_tower[7])
        self.assertEqual("110kV胜西Ⅰ线：#7", only_tower[8])
        self.assertEqual("#7", only_tower[9])
        # 只有线路：杆塔回退任务级，线路保留图片自己的
        self.assertEqual("220kV龙湾线", only_line[7])
        self.assertEqual("220kV龙湾线：#1", only_line[8])
        self.assertEqual("#1", only_line[9])

    def test_汇总在线路杆塔函数级逐字段回退(self):
        task_info = {
            "line_name": "任务线路",
            "line_tower": "任务线路：#9",
            "tower_name": "#9",
        }
        image = drt.ReviewImage(
            picture_guid="p", file_guid="f", line_name="", tower_name="#5"
        )
        self.assertEqual(
            ("任务线路", "任务线路：#5", "#5"),
            drt._summary_line_values(task_info, image),
        )
        image = drt.ReviewImage(
            picture_guid="p", file_guid="f", line_name="图片线路", tower_name=""
        )
        self.assertEqual(
            ("图片线路", "图片线路：#9", "#9"),
            drt._summary_line_values(task_info, image),
        )

    def test_非法路径段与重名文件安全落盘(self):
        self.assertEqual(
            "_.._evil", drt._safe_path_part("../../evil", "未识别线路")
        )
        self.assertEqual("_CON", drt._safe_path_part("CON", "未识别杆塔"))
        self.assertEqual("未识别线路", drt._safe_path_part(" .. ", "未识别线路"))
        self.assertEqual(
            "DJI_0001.JPG",
            drt._photo_file_name(
                self._image(original_name="../../DJI_0001.JPG"), "image/jpeg"
            ),
        )

        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            page.context = mock.Mock()
            responses = []
            for marker in (b"A", b"B"):
                response = mock.Mock()
                response.ok = True
                response.status = 200
                response.body.return_value = b"\xff\xd8\xff" + marker
                response.headers = {"content-type": "image/jpeg"}
                responses.append(response)
            page.context.request.get.side_effect = responses
            runner._save_defect_photo(
                {},
                self._image(
                    picture_guid="p1",
                    file_guid="f1",
                    original_name="same.jpg",
                    line_name="../../evil",
                    tower_name="CON",
                ),
                [],
            )
            runner._save_defect_photo(
                {},
                self._image(
                    picture_guid="p2",
                    file_guid="f2",
                    original_name="same.jpg",
                    line_name="../../evil",
                    tower_name="CON",
                ),
                [],
            )
            directory = Path(tmpdir) / "_.._evil" / "_CON"
            self.assertTrue((directory / "same.jpg").is_file())
            self.assertTrue((directory / "same (2).jpg").is_file())

    def test_缺陷框坐标转换与无效项过滤(self):
        self.assertEqual(
            "DJI_0001_标注.JPG", drt._annotated_photo_name("DJI_0001.JPG", "image/jpeg")
        )
        self.assertEqual(
            "DJI_0001_标注.png", drt._annotated_photo_name("DJI_0001.JPG", "image/png")
        )
        boxes = drt._photo_boxes(
            [
                {
                    "defectName": "金具类-偏斜",
                    "xmin": "10",
                    "ymin": 20,
                    "xmax": 100,
                    "ymax": 200,
                },
                {"defectName": "坏坐标", "xmin": "x", "ymin": 1, "xmax": 2, "ymax": 3},
                {"defectName": "零面积", "xmin": 10, "ymin": 10, "xmax": 10, "ymax": 20},
                {
                    "defectName": "归一化",
                    "xmin": 0.1,
                    "ymin": 0.2,
                    "xmax": 0.3,
                    "ymax": 0.4,
                },
            ]
        )

        self.assertEqual(2, len(boxes))
        self.assertEqual("金具类-偏斜", boxes[0]["label"])
        self.assertFalse(boxes[0]["normalized"])
        self.assertTrue(boxes[1]["normalized"])

    def test_缩略图地址改写成原图地址(self):
        self.assertEqual(
            "http://example.test/api/allcore-file/publicFile/downLoadFile?"
            "fileGuid=file-1&isThumb=no",
            drt.ReviewRunner._original_photo_url(
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=yes"
            ),
        )
        self.assertEqual(
            "", drt.ReviewRunner._original_photo_url("http://example.test/a.jpg")
        )

    def test_原图接口失败时拒绝把缩略图当原图保存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"thumbnail"
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")
            loaded_value = {
                "b64": base64.b64encode(body).decode("ascii"),
                "type": "image/jpeg",
                "width": 400,
                "height": 300,
                "src": "http://example.test/api/allcore-file/publicFile/downLoadFile?fileGuid=file-1&isThumb=yes",
                "thumbnail": True,
            }

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                if script == "navigator.userAgent":
                    return "test-agent"
                return loaded_value

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            page.frames = []
            logs = []
            runner.log_handler = logs.append
            image = self._image(
                original_name="DJI_THUMB.JPG",
                line_name="110kV胜西Ⅰ线",
                tower_name="43#",
            )
            runner._save_defect_photo({}, image, [])
            self.assertFalse(
                (Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "DJI_THUMB.JPG").exists()
            )
            self.assertTrue(any("拒绝保存" in line for line in logs))

    def test_照片下载复用页面图片请求头(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            request = mock.Mock(url=page.url)
            request.resource_type = "image"
            request.all_headers.return_value = {
                "Authorization": "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA==",
                "Blade-Auth": "bearer token",
                "Cookie": "SESSION=1",
            }
            runner._capture_auth_header(request)
            self.assertEqual(
                "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA==",
                runner._file_auth_header,
            )
            self.assertEqual("bearer token", runner._file_request_headers["Blade-Auth"])

            body = b"\xff\xd8\xff\xe0" + b"fake-jpeg-body"
            self._context(page, body, "image/jpeg")
            runner._save_defect_photo({}, self._image(), [])

            headers = page.context.request.get.call_args.kwargs["headers"]
        self.assertEqual(
            "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA==",
            headers["Authorization"],
        )
        self.assertEqual("bearer token", headers["Blade-Auth"])
        self.assertEqual("SESSION=1", headers["Cookie"])
    def test_照片下载复用页面请求鉴权头(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            request = mock.Mock(url=page.url)
            request.all_headers.return_value = {
                "Authorization": "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA=="
            }
            runner._capture_auth_header(request)
            self.assertEqual(
                "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA==",
                runner._file_auth_header,
            )

            body = b"\xff\xd8\xff\xe0" + b"fake-jpeg-body"
            self._context(page, body, "image/jpeg")
            runner._save_defect_photo({}, self._image(), [])

            headers = page.context.request.get.call_args.kwargs["headers"]
        self.assertEqual(
            "Basic YWxsY29yZTphbGxjb3JlX3NlY3JldA==",
            headers["Authorization"],
        )

    def test_照片归档优先复用页面已加载的原图响应(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"page-original"
            response = mock.Mock()
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=no"
            )
            response.headers = {
                "content-type": "image/jpeg",
                "content-length": str(len(body)),
            }
            response.body.return_value = body
            runner._cache_photo_response(response)
            page.context = mock.Mock()
            page.context.request.get.side_effect = AssertionError("不应重复下载")

            runner._save_defect_photo({}, self._image(original_name="page.jpg"), [])

            path = Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "page.jpg"
            self.assertEqual(body, path.read_bytes())

    def test_照片优先按当前页面原图地址下载(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"exact-source"
            source = "http://example.test/proxy/file?fileGuid=file-1&isThumb=no"

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_SOURCE_SCRIPT:
                    return [source]
                if script == "navigator.userAgent":
                    return "test-agent"
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                return True

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            self._context(page, body, "image/jpeg")

            runner._save_defect_photo({}, self._image(), [])

            url = page.context.request.get.call_args[0][0]
            self.assertEqual(source, url)

    def test_页面缩略图响应不进入原图缓存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, save_photos=True)
            response = mock.Mock()
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=yes"
            )
            response.headers = {
                "content-type": "image/jpeg",
                "content-length": "1024",
            }

            runner._cache_photo_response(response)

            self.assertEqual({}, runner._photo_responses)

    def test_部分图片响应不进入原图缓存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, save_photos=True)
            response = mock.Mock()
            response.status = 206
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=no"
            )
            response.headers = {
                "content-type": "image/jpeg",
                "content-length": "1024",
                "content-range": "bytes 0-1023/2048",
            }

            runner._cache_photo_response(response)

            self.assertEqual({}, runner._photo_responses)

    def test_文件接口错标JSON时仍复用页面原图字节(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"page-original-mislabeled"
            response = mock.Mock()
            response.status = 200
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=no"
            )
            response.headers = {
                "content-type": "application/json;charset=UTF-8",
                "content-length": str(len(body)),
            }
            response.body.return_value = body
            runner._cache_photo_response(response)
            self.assertIn(response.url, runner._photo_responses)
            page.context = mock.Mock()
            page.context.request.get.side_effect = AssertionError("不应重复下载")

            runner._save_defect_photo({}, self._image(original_name="reuse.jpg"), [])

            path = Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "reuse.jpg"
            self.assertEqual(body, path.read_bytes())
            page.context.request.get.assert_not_called()

    def test_普通XML错误体不被当作图片(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = (
                b'<?xml version="1.0" encoding="UTF-8"?>'
                b"<Error><Code>AccessDenied</Code></Error>"
            )
            response = mock.Mock()
            response.status = 200
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-1&isThumb=no"
            )
            response.headers = {
                "content-type": "application/xml",
                "content-length": str(len(body)),
            }
            response.body.return_value = body
            runner._cache_photo_response(response)

            self.assertIsNone(runner._cached_photo_bytes(self._image()))
            self.assertEqual({}, runner._photo_responses)

            self._context(page, body, "application/xml")
            with self.assertRaises(RuntimeError) as raised:
                runner._fetch_photo_url(response.url)
            self.assertIn("application/xml", str(raised.exception))

    def test_带XML声明的SVG仍识别为图片(self):
        self.assertEqual("", drt._detect_image_mime(b"<Error><Code>x</Code></Error>"))
        self.assertEqual(
            "",
            drt._detect_image_mime(
                b'<?xml version="1.0" encoding="UTF-8"?>'
                b"<Error><Code>AccessDenied</Code></Error>"
            ),
        )
        self.assertEqual(
            "image/svg+xml",
            drt._detect_image_mime(
                b'<?xml version="1.0" encoding="UTF-8"?>\n'
                b'<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">'
                b"</svg>"
            ),
        )
        self.assertEqual(
            "image/svg+xml",
            drt._detect_image_mime(b'<svg viewBox="0 0 1 1"></svg>'),
        )

    def test_查询参数含文件接口路径时不进入原图缓存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"proxy-json"
            response = mock.Mock()
            response.status = 200
            response.url = (
                "http://example.test/api/proxy/render?"
                "next=/api/allcore-file/publicFile/downLoadFile&fileGuid=file-1"
            )
            response.headers = {
                "content-type": "application/json;charset=UTF-8",
                "content-length": str(len(body)),
            }
            response.body.return_value = body

            runner._cache_photo_response(response)

            self.assertEqual({}, runner._photo_responses)

    def test_文件接口缓存不把JSON或文本伪响应当作图片(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, save_photos=True)
            pairs = (
                ("application/json;charset=UTF-8", b'{"code":500,"msg":"error"}'),
                ("text/html;charset=UTF-8", b"<html>error</html>"),
            )
            for index, (content_type, body) in enumerate(pairs):
                response = mock.Mock()
                response.status = 200
                response.url = (
                    "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                    "fileGuid=file-1&isThumb=no&variant=%d" % index
                )
                response.headers = {
                    "content-type": content_type,
                    "content-length": str(len(body)),
                }
                response.body.return_value = body
                runner._cache_photo_response(response)

            self.assertEqual(2, len(runner._photo_responses))
            self.assertIsNone(runner._cached_photo_bytes(self._image()))
            self.assertEqual({}, runner._photo_responses)

    def test_页面只加载到小尺寸blob图时拒绝当原图保存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\x89PNG\r\n\x1a\n" + b"tiny-thumbnail"
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")
            loaded_value = {
                "b64": base64.b64encode(body).decode("ascii"),
                "type": "image/png",
                "width": 200,
                "height": 150,
                "src": "blob:http://example.test/6f1e",
                "thumbnail": False,
            }

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_LOADED_SCRIPT:
                    return loaded_value
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                if script == "navigator.userAgent":
                    return "test-agent"
                return []

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            page.frames = []
            logs = []
            runner.log_handler = logs.append

            runner._save_defect_photo(
                {}, self._image(original_name="blob-thumb.png"), []
            )

            self.assertFalse(
                (Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "blob-thumb.png").exists()
            )
            self.assertEqual(1, runner.photo_failures)
            self.assertTrue(
                any("拒绝保存" in line and "HTTP 状态 401" in line for line in logs)
            )

    def test_页面只加载到竖版小图blob时也拒绝当原图保存(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\x89PNG\r\n\x1a\n" + b"tiny-portrait"
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")
            loaded_value = {
                "b64": base64.b64encode(body).decode("ascii"),
                "type": "image/png",
                "width": 150,
                "height": 200,
                "src": "blob:http://example.test/portrait",
                "thumbnail": False,
            }

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_LOADED_SCRIPT:
                    return loaded_value
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                if script == "navigator.userAgent":
                    return "test-agent"
                return []

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            page.frames = []
            logs = []
            runner.log_handler = logs.append

            runner._save_defect_photo(
                {}, self._image(original_name="portrait-thumb.png"), []
            )

            self.assertFalse(
                (Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "portrait-thumb.png").exists()
            )
            self.assertEqual(1, runner.photo_failures)
            self.assertTrue(
                any("拒绝保存" in line and "HTTP 状态 401" in line for line in logs)
            )

    def test_原图接口失败原因写入页面回退日志(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"loaded-original"
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")
            loaded_value = {
                "b64": base64.b64encode(body).decode("ascii"),
                "type": "image/jpeg",
                "width": 1600,
                "height": 1200,
                "src": "blob:http://example.test/loaded",
                "thumbnail": False,
            }

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_LOADED_SCRIPT:
                    return loaded_value
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                if script == "navigator.userAgent":
                    return "test-agent"
                return []

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            page.frames = []
            logs = []
            runner.log_handler = logs.append

            runner._save_defect_photo({}, self._image(original_name="fallback.jpg"), [])

            path = Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "fallback.jpg"
            self.assertEqual(body, path.read_bytes())
            self.assertTrue(
                any(
                    "改用页面已加载原图" in line and "HTTP 状态 401" in line
                    for line in logs
                )
            )

    def test_页面无当前fileGuid匹配时不拿其他大图归档(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"other-big-photo"
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")
            # 页面上只有别的照片：地址不含当前 fileGuid，尺寸再大也不能归档。
            loaded_value = {
                "b64": base64.b64encode(body).decode("ascii"),
                "type": "image/jpeg",
                "width": 4000,
                "height": 3000,
                "src": (
                    "http://example.test/api/allcore-file/publicFile/downLoadFile"
                    "?fileGuid=file-2&isThumb=no"
                ),
                "thumbnail": False,
            }

            def fake_evaluate(script, *_args):
                if script == drt._PHOTO_LOADED_SCRIPT:
                    return loaded_value
                if script == drt._PHOTO_FETCH_SCRIPT:
                    return None
                if script == "navigator.userAgent":
                    return "test-agent"
                return []

            page.evaluate = mock.Mock(side_effect=fake_evaluate)
            page.frames = []
            logs = []
            runner.log_handler = logs.append

            runner._save_defect_photo(
                {}, self._image(file_guid="file-1", original_name="other.jpg"), []
            )

            self.assertFalse(
                (Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "other.jpg").exists()
            )
            self.assertEqual(0, runner.photos_saved)
            self.assertEqual(1, runner.photo_failures)

    def test_其他图片的页面响应不当作当前原图(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            body = b"\xff\xd8\xff\xe0" + b"other-photo"
            response = mock.Mock()
            response.url = (
                "http://example.test/api/allcore-file/publicFile/downLoadFile?"
                "fileGuid=file-2&isThumb=no"
            )
            response.headers = {
                "content-type": "image/jpeg",
                "content-length": str(len(body)),
            }
            response.body.return_value = body
            runner._cache_photo_response(response)
            page.context = mock.Mock()
            page.context.request.get.side_effect = RuntimeError("HTTP 状态 401")

            runner._save_defect_photo({}, self._image(file_guid="file-1"), [])

            self.assertEqual(0, runner.photos_saved)
            self.assertFalse(
                (Path(tmpdir) / "110kV胜西Ⅰ线" / "43#" / "DJI_0001.JPG").exists()
            )

    def test_非本系统请求不捕获鉴权头(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir, save_photos=True)
            request = mock.Mock(url="https://other.test/api")
            request.all_headers.return_value = {
                "Authorization": "Basic b3RoZXI6c2VjcmV0"
            }
            runner._capture_auth_header(request)
        self.assertEqual("", runner._file_auth_header)

    def test_引擎内部关闭时不下载照片(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            page = FakePage()
            runner = make_runner(page, tmpdir)
            page.context = mock.Mock()
            runner._save_defect_photo({}, self._image(), [])

            page.context.request.get.assert_not_called()
            self.assertFalse(list(Path(tmpdir).glob("**/*.JPG")))


class ResponseMonitorTest(unittest.TestCase):
    def test_只保留接口响应并忽略图片下载与静态资源(self):
        page = FakePage()
        monitor = drt.ResponseMonitor(page=page)
        monitor(FakeResponse(START_URL + drt.PIC_GROUP_API, ok_payload([]), page=page))
        monitor(FakeResponse(START_URL + drt.FILE_DOWNLOAD_API, None, page=page))
        monitor(FakeResponse(START_URL + "/static/app.js", None, page=page))

        self.assertEqual(1, len(monitor.responses))
        self.assertIn(drt.PIC_GROUP_API, monitor.responses[0].url)


class FakeUploadImage(FakeElement):
    def __init__(self, file_guid, thumb=False, ready=False):
        super().__init__("", True, True, "缩略图" if thumb else "原图")
        self.file_guid = file_guid
        self.thumb = thumb
        self.ready = ready
        self.src = (
            "https://example.test"
            + DOWNLOAD_API
            + "?fileGuid="
            + file_guid
            + "&isThumb="
            + ("yes" if thumb else "no")
        )
        self.attributes["src"] = self.src

    def get_attribute(self, name):
        if name == "src":
            return self.src
        return super().get_attribute(name)

    def evaluate(self, expression, arg=None):
        if "naturalWidth" in expression:
            return self.ready
        if "el.click()" in expression:
            self.click()
            return None
        return True


class FakeReviewDialog(FakeElement):
    def __init__(self, page):
        super().__init__("", False, True, "审核弹窗")
        self.page_ref = page
        self.close_button = FakeElement("关闭", True, True, "关闭弹窗")
        self.add(".el-dialog__headerbtn", self.close_button)
        self.review_button = FakeElement("审核完成", True, True, "审核完成")
        self.add("button", self.review_button)
        self.complete_button = FakeElement("完成任务", True, True, "完成任务")
        self.add("button", self.complete_button)
        self.confirm_button = FakeElement("确认", False, True, "弹窗确认")
        self.add("button", self.confirm_button)

        self.confirm_box = FakeElement(
            "审核完成，是否提交？", False, True, "完成确认框"
        )
        self.confirm_box_button = FakeElement("确认", True, True, "完成确认按钮")
        self.confirm_box.add("button", self.confirm_box_button)
        page.add(".el-message-box", self.confirm_box)
        page.add(".el-dialog", self)

        self.secondary_box = FakeElement("关闭二次确认", False, True, "关闭二次确认")
        self.secondary_ok = FakeElement("确认", True, True, "二次确认")
        self.secondary_cancel = FakeElement("取消", True, True, "二次取消")
        self.secondary_box.add("button", self.secondary_ok)
        self.secondary_box.add("button", self.secondary_cancel)
        page.add(".el-message-box", self.secondary_box)

    def show_confirm(self):
        self.confirm_box.visible = True


class ReviewFlow:
    def __init__(
        self,
        page,
        task_guid="task-guid",
        task_name="任务一",
        image_specs=None,
        show_dialog=True,
        auto_first_download=True,
        auto_next_download=True,
        render_ready=True,
        thumbnail_download_after=0,
        review_mode="success",
        duplicate_review_response=False,
        submit_mode="success",
        refresh_on_submit=True,
        close_mode="ok",
        cancel_check=None,
        group_task_guid=None,
        active_evidence=True,
        review_button_enabled=True,
        render_on_thumbnail_click=False,
        refresh_current=1,
        refresh_task_status="manual_review",
        pic_defect_flags=None,
        tagging_details=None,
        missing_downloads=None,
        suppress_final_confirm=False,
        auto_final_confirm=True,
    ):
        self.page = page
        self.dialog = FakeReviewDialog(page)
        self.task_guid = task_guid
        self.task_name = task_name
        self.image_specs = list(image_specs or [("pic-1", "file-1")])
        self.pic_defect_flags = dict(pic_defect_flags or {})
        self.tagging_details = dict(tagging_details or {})
        self.missing_downloads = set(missing_downloads or ())
        self.suppress_final_confirm = bool(suppress_final_confirm)
        self.auto_final_confirm = bool(auto_final_confirm)
        self.show_dialog = show_dialog
        self.auto_first_download = auto_first_download
        self.auto_next_download = auto_next_download
        self.render_ready = render_ready
        self.thumbnail_download_after = thumbnail_download_after
        self.review_mode = review_mode
        self.duplicate_review_response = duplicate_review_response
        self.submit_mode = submit_mode
        self.refresh_on_submit = refresh_on_submit
        self.close_mode = close_mode
        self.cancel_check = cancel_check
        self.group_task_guid = group_task_guid
        self.active_evidence = active_evidence
        self.review_button_enabled = bool(review_button_enabled)
        self.render_on_thumbnail_click = render_on_thumbnail_click
        self.refresh_current = refresh_current
        self.refresh_task_status = refresh_task_status

        self.entered = 0
        self.current_index = 0
        self.review_clicks = 0
        self.review_clicks_on_system_defect = 0
        self.complete_clicks = 0
        self.confirm_clicks = 0
        self.submit_clicks = 0
        self.close_clicks = 0
        self.secondary_clicks = 0
        self.thumbnail_clicks = []
        self.review_clicks_at_confirm = None
        self.downloads = []
        self.submits = []
        self.group_items = None
        self.tagging_emitted = set()

        self.main_images = []
        self.thumbnails = []
        for index, (_picture, file_guid) in enumerate(self.image_specs):
            main = FakeUploadImage(file_guid, thumb=False, ready=False)
            main.visible = index == 0
            self.dialog.add("img", main)
            self.main_images.append(main)
            thumb = FakeUploadImage(file_guid, thumb=True, ready=True)
            thumb.on_click = lambda index=index: self.click_thumbnail(index)
            self.dialog.add("img", thumb)
            self.thumbnails.append(thumb)

        self.dialog.review_button.on_click = self.click_review
        self.dialog.complete_button.on_click = self.click_complete
        self.dialog.close_button.on_click = self.click_close
        self.dialog.confirm_box_button.on_click = self.click_confirm
        self.dialog.secondary_ok.on_click = self.click_secondary
        self.page.on_press = self.on_press
        self.entry = self._make_entry()

    def _make_entry(self):
        item = record(self.task_guid, self.task_name)
        row = FakeElement(
            item["taskName"] + " " + item["taskStatus"], True, True, "任务行"
        )
        button = FakeElement("进入", True, True, "进入")
        button.on_click = self.enter
        row.add(TASK_ENTER, button)
        return {"item": item, "guid": self.task_guid, "row": row, "page": 1}

    def set_active(self, index):
        self.main_images[index].visible = True
        for position, thumbnail in enumerate(self.thumbnails):
            thumbnail.attributes.pop("class", None)
            thumbnail.attributes.pop("aria-selected", None)
            if self.active_evidence and position == index:
                thumbnail.attributes["class"] = "active"
                thumbnail.attributes["aria-selected"] = "true"
        self._set_review_ready(index)
        self._emit_tagging(index)

    def _set_review_ready(self, index):
        self.dialog.review_button.enabled = bool(
            self.review_button_enabled and self.main_images[index].ready
        )

    def _emit_tagging(self, index):
        if index in self.tagging_emitted or index not in self.tagging_details:
            return
        self.tagging_emitted.add(index)
        picture_guid = self.image_specs[index][0]
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + drt.TAGGING_DETAIL_API,
                ok_payload(self.tagging_details[index]),
                page=self.page,
                query_params={"pictureDetailGuid": picture_guid},
            ),
        )

    def enter(self):
        self.entered += 1
        if self.show_dialog:
            self.dialog.visible = True
        self.set_active(0)
        items = self.group_items
        if items is None:
            items = [
                image_item(
                    picture,
                    file_guid,
                    pic_defect_flag=self.pic_defect_flags.get(index, ""),
                )
                for index, (picture, file_guid) in enumerate(self.image_specs)
            ]
        query = (
            {"recognitionTaskGuid": self.group_task_guid}
            if self.group_task_guid
            else None
        )
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + PIC_GROUP_API,
                group_payload(items),
                page=self.page,
                query_params=query,
            ),
        )
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + PIC_DETAIL_API,
                detail_payload(items),
                page=self.page,
                query_params=query,
            ),
        )
        if self.auto_first_download:
            self.emit_download(0)

    def emit_download(self, index):
        if index >= len(self.main_images) or index in self.missing_downloads:
            return
        if self.render_ready:
            self.main_images[index].ready = True
        file_guid = self.image_specs[index][1]
        self.downloads.append(index)
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + DOWNLOAD_API,
                None,
                page=self.page,
                query_params={"fileGuid": file_guid, "isThumb": "no"},
            ),
        )
        if index == self.current_index:
            self._set_review_ready(index)

    def click_thumbnail(self, index):
        self.thumbnail_clicks.append(index)
        if self.cancel_check:
            self.cancel_check()
        self.current_index = index
        self.set_active(index)
        if self.render_on_thumbnail_click:
            self.main_images[index].ready = True
            self._set_review_ready(index)
        if (
            self.thumbnail_download_after is not None
            and len(self.thumbnail_clicks) > self.thumbnail_download_after
        ):
            self.emit_download(index)

    def click_review(self):
        self.review_clicks += 1
        if self.pic_defect_flags.get(self.current_index):
            self.review_clicks_on_system_defect += 1
        if self.cancel_check:
            self.cancel_check()
        picture_guid = self.image_specs[self.current_index][0]
        if self.review_mode == "unknown":
            return
        if self.review_mode == "business_failure":
            payload = {
                "code": 500,
                "success": False,
                "data": None,
                "msg": "审核失败",
            }
        elif self.review_mode == "data_false":
            payload = ok_payload(False)
        elif self.review_mode == "http_500":
            payload = ok_payload(True)
            self.page.emit(
                "response",
                FakeResponse(
                    START_URL + PICTURE_REVIEW_API,
                    payload,
                    status=500,
                    page=self.page,
                    query_params={"picDetailGuid": picture_guid},
                ),
            )
            return
        else:
            payload = ok_payload(True)
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + PICTURE_REVIEW_API,
                payload,
                page=self.page,
                query_params={"picDetailGuid": picture_guid},
            ),
        )
        if self.duplicate_review_response:
            self.page.emit(
                "response",
                FakeResponse(
                    START_URL + PICTURE_REVIEW_API,
                    payload,
                    page=self.page,
                    query_params={"picDetailGuid": picture_guid},
                ),
            )
        if self.review_mode != "success":
            return
        if self.current_index + 1 < len(self.image_specs):
            self.current_index += 1
            self.set_active(self.current_index)
            if self.auto_next_download:
                self.emit_download(self.current_index)
        elif (
            not self.pic_defect_flags
            and not self.suppress_final_confirm
            and self.auto_final_confirm
        ):
            self.dialog.show_confirm()

    def click_complete(self):
        self.complete_clicks += 1
        self.dialog.show_confirm()

    def click_confirm(self):
        self.confirm_clicks += 1
        self.submit_clicks += 1
        self.review_clicks_at_confirm = self.review_clicks
        self.dialog.confirm_box.visible = False
        self.submits.append(self.task_guid)
        if self.submit_mode == "unknown":
            return
        payload = (
            {"code": 500, "success": False, "data": None, "msg": "提交失败"}
            if self.submit_mode == "business_failure"
            else ok_payload({})
        )
        self.page.emit(
            "response",
            FakeResponse(
                START_URL + SUBMIT_AUDIT_API,
                payload,
                page=self.page,
                query_params={"recognitionTaskGuid": self.task_guid},
            ),
        )
        if self.refresh_on_submit:
            params = {
                "startTime": "2026-11-01 00:00:00",
                "endTime": "2026-11-02 23:59:59",
            }
            if self.refresh_task_status is not None:
                params["taskStatus"] = self.refresh_task_status
            self.page.emit(
                "response",
                FakeResponse(
                    START_URL + drt.TASK_PAGE_API,
                    ok_payload(
                        {
                            "records": [],
                            "total": 0,
                            "current": self.refresh_current,
                            "pages": 1,
                        }
                    ),
                    page=self.page,
                    query_params=params,
                ),
            )
        self.dialog.visible = False

    def click_close(self):
        self.close_clicks += 1
        if self.close_mode == "secondary":
            self.dialog.secondary_box.visible = True
            return
        if self.close_mode == "fail":
            return
        self.dialog.visible = False

    def click_secondary(self):
        self.secondary_clicks += 1
        self.dialog.secondary_box.visible = False

    def on_press(self, key):
        if key == "Escape" and self.close_mode in ("ok", "secondary"):
            self.dialog.visible = False


def attach_flow_to_ui(ui, flow):
    ui.enter_handlers[flow.task_guid] = flow.enter
    return flow


class ImageWaitTest(unittest.TestCase):
    def _runner_with_dialog(self, tmpdir, flow):
        runner = make_runner(flow.page, tmpdir, timeout=FAST, image_timeout=FAST)
        runner._ensure_monitor()
        return runner

    def test_审核完成按钮可用即判定加载成功(self):
        page = FakePage()
        flow = ReviewFlow(page, image_specs=[("pic-1", "file-1")])
        image = drt.ReviewImage("pic-1", "file-1", 0)
        ready_states = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_dialog(tmpdir, flow)
            original_ready = runner._review_button_ready

            def record_ready(dialog):
                ready = original_ready(dialog)
                ready_states.append(ready)
                return ready

            runner._review_button_ready = record_ready
            # 首轮轮询时按钮还不可用，第一次等待切片后才亮起：
            # 只有真正以「按钮可用」为完成条件，才会在第二次轮询时返回。
            flow.dialog.review_button.enabled = False
            page.on_wait = lambda: setattr(flow.dialog.review_button, "enabled", True)
            runner.image_timeout_ms = 1000.0
            runner.wait_current_image(image, flow.dialog, mark=0)

            self.assertEqual(ready_states, [False, True])
            # 机巡刻意保留「可点完成即提交」：点击由调用方负责，等待本身只做判定。
            self.assertEqual(flow.review_clicks, 0)
            self.assertEqual(flow.complete_clicks, 0)
            self.assertEqual(flow.submit_clicks, 0)

            # 对照：按钮始终不可用时不得提前完成，只能等到超时抛错。
            ready_states.clear()
            page.on_wait = None
            flow.dialog.review_button.enabled = False
            runner.image_timeout_ms = 200.0
            with self.assertRaises(drt.ImageLoadTimeout) as ctx:
                runner.wait_current_image(image, flow.dialog, mark=0)

        self.assertGreaterEqual(len(ready_states), 2)
        self.assertFalse(any(ready_states))
        self.assertIn("按钮", str(ctx.exception))
        self.assertEqual(flow.review_clicks, 0)
        self.assertEqual(flow.complete_clicks, 0)
        self.assertEqual(flow.submit_clicks, 0)

    def test_顶部加载蒙层未消失时等待审核按钮(self):
        page = FakePage()
        flow = ReviewFlow(page, image_specs=[("pic-1", "file-1")])
        image = drt.ReviewImage("pic-1", "file-1", 0)
        flow.dialog.visible = True
        flow.dialog.review_button.enabled = True
        checks = []

        def probe():
            checks.append(1)
            return len(checks) <= 2

        flow.dialog.review_button.loading_probe = probe
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_dialog(tmpdir, flow)
            # FAST=0.05s 在慢速 CI 上可能赶不及第 3 次轮询；
            # 本用例只验证“先等蒙层”，给足 1 秒仍会瞬间完成。
            runner.image_timeout_ms = 1000.0
            runner.wait_current_image(image, flow.dialog, mark=0)

        self.assertGreaterEqual(len(checks), 3)

    def test_审核完成按钮不可用时等待超时(self):
        page = FakePage()
        flow = ReviewFlow(page, image_specs=[("pic-1", "file-1")])
        image = drt.ReviewImage("pic-1", "file-1", 0)
        flow.dialog.review_button.enabled = False
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_dialog(tmpdir, flow)
            with self.assertRaises(drt.ImageLoadTimeout) as ctx:
                runner.wait_current_image(image, flow.dialog, mark=0)

        self.assertIn("按钮", str(ctx.exception))


class ThumbnailSelectionTest(unittest.TestCase):
    def _blob_thumbnail_item(self):
        item = FakeElement(name="缩略图项")
        box = FakeElement(name="缩略图盒")
        image = FakeElement("", True, True, "缩略图")
        image.attributes["src"] = "blob:https://example.test/thumbnail"
        box.add("img", image)
        item.add(".box", box)
        return item, image

    def test_点击缩略图后等待顶部加载蒙层消失再识别(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        image = drt.ReviewImage(
            "pic-1", "file-1", 0, "unreviewed", original_name="one.jpg"
        )
        item = FakeElement(name="缩略图项")
        box = FakeElement(name="缩略图盒")
        box.add("img", FakeUploadImage(image.file_guid, thumb=True, ready=True))
        item.add(".box", box)
        dialog.add("li", item)
        checks = []

        def probe():
            checks.append(1)
            return len(checks) <= 2

        dialog.review_button.loading_probe = probe
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            selected, _mark = runner._select_unreviewed_image(
                dialog, [image], set(), check_marks=False
            )

        self.assertEqual("pic-1", selected.picture_guid)
        self.assertGreaterEqual(len(checks), 3)

    def test_点击后标题滞后时不把上一张跳过图片当成当前图(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        old = drt.ReviewImage(
            "pic-old", "file-old", 0, "unreviewed", original_name="old.jpg"
        )
        new = drt.ReviewImage(
            "pic-new", "file-new", 1, "unreviewed", original_name="new.jpg"
        )
        state = {"name": "old.jpg", "pending": 0}

        def inner_text():
            if state["pending"] > 0:
                state["pending"] -= 1
                if state["pending"] == 0:
                    state["name"] = "new.jpg"
            return state["name"]

        dialog.inner_text = inner_text
        old_item, _old_img = self._blob_thumbnail_item()
        new_item, new_img = self._blob_thumbnail_item()
        dialog.add("li", old_item)
        dialog.add("li", new_item)
        new_img.on_click = lambda: state.__setitem__("pending", 2)
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=1, log_handler=logs.append)
            selected, _mark = runner._select_unreviewed_image(
                dialog, [old, new], {old.picture_guid}, check_marks=False
            )

        self.assertIsNotNone(selected)
        self.assertEqual("pic-new", selected.picture_guid)
        self.assertFalse(any("点击后识别为已失败/跳过" in line for line in logs))

    def test_取消时缩略图切换不吞掉停止异常(self):
        class Cancel(Exception):
            pass

        page = FakePage()
        dialog = FakeReviewDialog(page)
        dialog.add("li", self._blob_thumbnail_item()[0])
        image = drt.ReviewImage("pic-1", "file-1", 0, "unreviewed")

        def cancel_check():
            raise Cancel()

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.cancel_check = cancel_check
            with self.assertRaises(Cancel):
                runner._select_unreviewed_image(
                    dialog, [image], set(), check_marks=False
                )

    def test_点击后一直未切换不把上一张当成跳过图片(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        old = drt.ReviewImage(
            "pic-old", "file-old", 0, "unreviewed", original_name="old.jpg"
        )
        new = drt.ReviewImage(
            "pic-new", "file-new", 1, "unreviewed", original_name="new.jpg"
        )
        dialog._text = "old.jpg"
        dialog.add("li", self._blob_thumbnail_item()[0])
        dialog.add("li", self._blob_thumbnail_item()[0])
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=1, log_handler=logs.append)
            selected, _mark = runner._select_unreviewed_image(
                dialog, [old, new], {old.picture_guid}, check_marks=False
            )

        self.assertIsNone(selected)
        self.assertTrue(any("未切换" in line for line in logs))
        self.assertFalse(any("点击后识别为已失败/跳过" in line for line in logs))

    def test_跳过有对勾的缩略图选择未打勾(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        images = [
            drt.ReviewImage(
                "pic-1", "file-1", 0, "unreviewed", original_name="one.jpg"
            ),
            drt.ReviewImage(
                "pic-2", "file-2", 1, "unreviewed", original_name="two.jpg"
            ),
        ]
        for index, checked in ((0, True), (1, False)):
            item = FakeElement(name="缩略图项")
            box = FakeElement(name="缩略图盒")
            box.add(
                "img",
                FakeUploadImage(images[index].file_guid, thumb=True, ready=True),
            )
            item.add(".box", box)
            if checked:
                item.add(".check", FakeElement("", True, True, "对勾"))
            dialog.add("li", item)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            selected, mark = runner._select_unreviewed_image(
                dialog, images, set(), check_marks=True
            )

        self.assertIsNotNone(selected)
        self.assertEqual("pic-2", selected.picture_guid)
        self.assertEqual(0, mark)

    def test_选中未打勾图片后不因按钮未就绪继续点击下一张(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        dialog.review_button.visible = False
        image = drt.ReviewImage(
            "pic-1", "file-1", 0, "unreviewed", original_name="one.jpg"
        )
        item = FakeElement(name="缩略图项")
        box = FakeElement(name="缩略图盒")
        box.add(
            "img",
            FakeUploadImage(image.file_guid, thumb=True, ready=False),
        )
        item.add(".box", box)
        dialog.add("li", item)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            selected, mark = runner._select_unreviewed_image(
                dialog, [image], set(), check_marks=False
            )

        self.assertEqual("pic-1", selected.picture_guid)
        self.assertEqual(0, mark)


class CurrentImageIdentityTest(unittest.TestCase):
    def test_当前图片以active缩略图为准而不是滞后标题(self):
        page = FakePage()
        dialog = FakeReviewDialog(page)
        items = [image_item("pic-1", "file-1"), image_item("pic-2", "file-2")]
        images = drt.parse_task_images(group_payload(items), detail_payload(items))
        for index, image in enumerate(images):
            item = FakeElement(name="缩略图项")
            box = FakeElement(name="缩略图盒")
            box.attributes["class"] = "active" if index == 0 else ""
            box.add(
                "img",
                FakeUploadImage(image.file_guid, thumb=True, ready=True),
            )
            item.add(".box", box)
            dialog.add("li", item)
        dialog.add(
            "div",
            FakeElement("two.jpg", True, True, "滞后的图片标题"),
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            current = runner._current_image_from_dialog(dialog, images)

        self.assertEqual("pic-1", current.picture_guid)


class ThumbnailOptimizationTest(unittest.TestCase):
    def test_高亮缩略图优先单次求值并允许从上次下标继续(self):
        page = FakePage()
        dialog = FakeElement(name="审核弹窗")
        calls = []

        def evaluate(expression, arg=None):
            calls.append(arg)
            return 3

        dialog.evaluate = evaluate
        dialog.locator = mock.Mock(
            side_effect=AssertionError("不应再逐项扫描缩略图")
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            index = runner._active_thumbnail_index(dialog, 2)

        self.assertEqual(3, index)
        self.assertEqual([2], calls)

    def test_缩略图选中判断只读取高亮项(self):
        page = FakePage()
        dialog = FakeElement(name="审核弹窗")
        first = FakeElement(name="已审核缩略图项")
        first.locator = mock.Mock(
            side_effect=AssertionError("不应扫描非高亮缩略图")
        )
        second = FakeElement(name="当前缩略图项")
        second.add("img", FakeUploadImage("file-2", thumb=True, ready=True))
        dialog.add("li", first)
        dialog.add("li", second)
        dialog.evaluate = lambda expression, arg=None: 1
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            matched = runner._thumbnail_active(dialog, "file-2")
            mismatched = runner._thumbnail_active(dialog, "file-1")

        self.assertTrue(matched)
        self.assertFalse(mismatched)

    def test_任务内图片索引只构建一次(self):
        class CountingImages(list):
            def __init__(self, values):
                super().__init__(values)
                self.loops = 0

            def __iter__(self):
                self.loops += 1
                return super().__iter__()

        page = FakePage()
        item = FakeElement(name="缩略图项")
        item.add("img", FakeUploadImage("file-1", thumb=True, ready=True))
        images = CountingImages([drt.ReviewImage("pic-1", "file-1", 0)])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            first = runner._thumbnail_image(item, images)
            second = runner._thumbnail_image(item, images)

        self.assertIs(first, second)
        self.assertEqual("pic-1", first.picture_guid)
        self.assertEqual(1, images.loops)


class ReviewResponseTest(unittest.TestCase):
    def _runner(self, tmpdir, flow):
        runner = make_runner(flow.page, tmpdir, timeout=FAST, image_timeout=FAST)
        runner.reviewed_guids = set()
        runner.reviewed_count = 0
        runner.task_image_total = 1
        for image in flow.main_images:
            image.ready = True
        runner._ensure_monitor()
        return runner

    def test_仅data为True时计该GUID成功(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            mark = runner.review_current_image(image, flow.dialog)

        self.assertGreaterEqual(mark, 0)
        self.assertEqual({"pic-1"}, runner.reviewed_guids)
        self.assertEqual(1, runner.reviewed_count)
        self.assertEqual(1, flow.review_clicks)

    def test_业务失败不标记成功且只点击一次(self):
        page = FakePage()
        flow = ReviewFlow(page, review_mode="business_failure")
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteRejected):
                runner.review_current_image(image, flow.dialog)

        self.assertEqual(set(), runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)

    def test_响应data为False不标记成功(self):
        page = FakePage()
        flow = ReviewFlow(page, review_mode="data_false")
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteRejected):
                runner.review_current_image(image, flow.dialog)

        self.assertEqual(set(), runner.reviewed_guids)

    def test_响应超时视为结果不明且不重发写请求(self):
        page = FakePage()
        flow = ReviewFlow(page, review_mode="unknown")
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown):
                runner.review_current_image(image, flow.dialog)

        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(set(), runner.reviewed_guids)

    def test_HTTP失败视为结果不明且不重发写请求(self):
        page = FakePage()
        flow = ReviewFlow(page, review_mode="http_500")
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown):
                runner.review_current_image(image, flow.dialog)

        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(set(), runner.reviewed_guids)

    def test_重复成功响应同一GUID只计一张(self):
        page = FakePage()
        flow = ReviewFlow(page, duplicate_review_response=True)
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.review_current_image(image, flow.dialog)
            self.assertFalse(runner._mark_reviewed("pic-1"))

        self.assertEqual(1, runner.reviewed_count)
        self.assertEqual({"pic-1"}, runner.reviewed_guids)

    def test_点击前已注册响应监听(self):
        page = FakePage()
        flow = ReviewFlow(page)

        def click():
            self.assertIn("response", page.listeners)
            flow.click_review()

        flow.dialog.review_button.on_click = click
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.review_current_image(image, flow.dialog)

        self.assertEqual({"pic-1"}, runner.reviewed_guids)

    def test_POST请求体中的picDetailGuid也可匹配(self):
        page = FakePage()
        flow = ReviewFlow(page)

        def click():
            flow.review_clicks += 1
            page.emit(
                "response",
                FakeResponse(
                    START_URL + PICTURE_REVIEW_API,
                    ok_payload(True),
                    page=page,
                    post_data_json={"picDetailGuid": "pic-1"},
                ),
            )

        flow.dialog.review_button.on_click = click
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.review_current_image(image, flow.dialog)

        self.assertEqual({"pic-1"}, runner.reviewed_guids)
        self.assertEqual(1, runner.reviewed_count)

    def test_缺少picDetailGuid不得视为审核成功(self):
        page = FakePage()
        flow = ReviewFlow(page)

        def click():
            flow.review_clicks += 1
            page.emit(
                "response",
                FakeResponse(
                    START_URL + PICTURE_REVIEW_API,
                    ok_payload(True),
                    page=page,
                ),
            )

        flow.dialog.review_button.on_click = click
        flow.dialog.visible = True
        image = drt.ReviewImage("pic-1", "file-1", 0)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown):
                runner.review_current_image(image, flow.dialog)

        self.assertEqual(set(), runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)


class FinalConfirmTest(unittest.TestCase):
    def test_未知message_box时只在其中找确认且不回退审核弹窗(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        flow.dialog.confirm_button.visible = True
        clicked = []
        flow.dialog.confirm_button.on_click = lambda: clicked.append(1)
        flow.dialog.secondary_box.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner._wait_final_confirm()

        self.assertIn("确认", str(ctx.exception))
        self.assertEqual([], clicked)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertIn("审核完成", flow.dialog.inner_text())
        self.assertIn("关闭二次确认", flow.dialog.secondary_box.inner_text())


class ProcessTaskTest(unittest.TestCase):
    def _runner(self, tmpdir, flow, **overrides):
        values = {
            "timeout": FAST,
            "image_timeout": FAST,
            "image_retries": 1,
        }
        values.update(overrides)
        runner = make_runner(flow.page, tmpdir, **values)
        runner.settings = make_settings()
        return runner

    def test_首图加载遮罩不阻塞弹层就绪(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        mask = FakeElement("", True, True, "加载遮罩")
        flow.dialog.add(".el-loading-mask", mask)
        # 缩略图 URL 不含 fileGuid 时，靠任务名 + 未审核文案确认弹层属于本任务。
        flow.dialog.children.pop("img", None)
        flow.dialog._text = "任务一 未审核 1"
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=2)
            runner.task_images = [drt.ReviewImage("pic-1", "file-1", 0)]
            result = runner._wait_task_dialog_loaded(flow.dialog, flow.task_name)

        self.assertIs(flow.dialog, result)
        self.assertTrue(mask.is_visible())

    def test_顶部弹层加载蒙层消失后才算就绪(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        flow.dialog._text = "任务一 未审核 1"
        flow.dialog.add(".el-loading-mask", FakeElement("", True, True, "弹层加载遮罩"))
        checks = []

        def probe():
            checks.append(1)
            return len(checks) <= 2

        flow.dialog.review_button.loading_probe = probe
        with tempfile.TemporaryDirectory() as tmpdir:
            # FAST=0.05s / 1s 在慢速 CI 上可能赶不及第 3 次轮询；给足 3 秒。
            runner = self._runner(tmpdir, flow, timeout=3)
            runner.task_images = [drt.ReviewImage("pic-1", "file-1", 0)]
            result = runner._wait_task_dialog_loaded(flow.dialog, flow.task_name)

        self.assertIs(flow.dialog, result)
        self.assertGreaterEqual(len(checks), 3)

    def test_任务名在输入框内也可确认就绪(self):
        class InputDialog(FakeElement):
            def evaluate(self, expression, arg=None):
                if "querySelectorAll('input, textarea')" in expression:
                    return ["20266512657自动识别任务"]
                return True

        dialog = InputDialog("未审核 1", True, True, "审核弹窗")
        dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, timeout=2)
            result = runner._wait_task_dialog_loaded(dialog, "20266512657自动识别任务")

        self.assertIs(dialog, result)

    def test_任务名不匹配时不按未审核文案就绪(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        flow.dialog.children.pop("img", None)
        flow.dialog._text = "上一任务 未审核 99"
        flow.dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=FAST)
            runner.task_images = [drt.ReviewImage("pic-1", "file-1", 0)]
            with self.assertRaises(drt.ReviewStopped):
                runner._wait_task_dialog_loaded(flow.dialog, flow.task_name)

    def test_上一任务残留图片和文案不视为就绪(self):
        page = FakePage()
        flow = ReviewFlow(page, image_specs=[("pic-old", "file-old")])
        flow.dialog.visible = True
        flow.dialog._text = "未审核 99"
        flow.dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=FAST)
            runner.task_images = [drt.ReviewImage("pic-new", "file-new", 0)]
            with self.assertRaises(drt.ReviewStopped):
                runner._wait_task_dialog_loaded(flow.dialog)

    def test_弹层无内容时等待内容渲染(self):
        class FadingMask(FakeElement):
            def __init__(self):
                super().__init__("", True, True, "加载遮罩")
                self.checks = 0

            def is_visible(self):
                self.checks += 1
                return self.checks <= 2

        class ContentDialog(FakeElement):
            def __init__(self):
                super().__init__("", True, True, "审核弹窗")
                self.mask = FadingMask()
                self.add(".el-loading-mask", self.mask)

            def inner_text(self):
                return "未审核 1" if self.mask.checks >= 3 else ""

        page = FakePage()
        dialog = ContentDialog()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=3)
            runner.settings = make_settings()
            result = runner._wait_task_dialog_loaded(dialog)

        self.assertIs(dialog, result)

    def test_弹层加载超时先登记当前弹窗并记异常(self):
        page = FakePage()
        flow = ReviewFlow(page)
        # 弹层内容（未审核文案/图片）始终未渲染，只有加载遮罩。
        flow.dialog.children.pop("img", None)
        flow.dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=FAST)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME
            )

        self.assertTrue(getattr(ctx.exception, "shade_dialog_unresponsive", False))
        self.assertIs(flow.dialog, runner.current_dialog)
        self.assertTrue(flow.dialog.visible)
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual("任务弹层加载超时", rows[1][1])

    def test_未出现审核弹窗时按弹层无响应处理(self):
        page = FakePage()
        flow = ReviewFlow(page, show_dialog=False)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=FAST)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.process_task(flow.entry)

        self.assertTrue(getattr(ctx.exception, "shade_dialog_unresponsive", False))
        self.assertIsNone(runner.current_dialog)

    def test_新任务未审核数超过阈值时跳过并写异常(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2"), ("pic-3", "file-3")],
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, max_unreviewed=2)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME
            )
            defect_rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME
            )

        self.assertEqual("skipped", result["status"])
        self.assertTrue(result.get("threshold_skipped"))
        self.assertEqual(0, result["images"])
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertEqual(["未审核数超阈值"], [row[1] for row in rows[1:]])
        self.assertEqual("3", rows[1][13])
        self.assertEqual(list(drt.ANOMALY_HEADERS), rows[0])
        self.assertEqual(1, len(defect_rows))

    def test_两张图片依次加载审核后才最终确认并刷新完成(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
        )
        flow.dialog.confirm_box.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(2, result["images"])
        self.assertEqual([0, 1], flow.downloads)
        self.assertEqual(2, flow.review_clicks)
        self.assertEqual(2, flow.review_clicks_at_confirm)
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)
        self.assertFalse(flow.dialog.visible)
        self.assertEqual({"pic-1", "pic-2"}, runner.reviewed_guids)

    def test_两张可见主图有active缩略图时可审核(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            active_evidence=True,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(2, flow.review_clicks)

    def test_两张可见主图无active时按按钮审核完成(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            active_evidence=False,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(2, flow.review_clicks)
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_blob或data主图配合active缩略图可审核(self):
        for source in (
            "blob:https://example.test/0d6f3e2a",
            "data:image/png;base64,iVBORw0KGgo=",
        ):
            with self.subTest(source=source):
                page = FakePage()
                flow = ReviewFlow(page)
                flow.main_images[0].src = source
                flow.main_images[0].attributes["src"] = source
                with tempfile.TemporaryDirectory() as tmpdir:
                    runner = self._runner(tmpdir, flow)
                    result = runner.process_task(flow.entry)

                self.assertEqual("completed", result["status"])
                self.assertEqual(1, result["images"])
                self.assertEqual(1, flow.review_clicks)

    def test_未审核为0时点击完成任务再确认(self):
        page = FakePage()
        flow = ReviewFlow(page, auto_final_confirm=False)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(1, flow.complete_clicks)
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)
        self.assertFalse(flow.dialog.visible)

    def test_界面未审核为0时不再等待审核完成按钮(self):
        page = FakePage()
        flow = ReviewFlow(page, image_specs=[("pic-1", "file-1")])
        flow.group_items = [image_item("pic-1", "file-1", "reviewed")]
        flow.dialog.review_button.visible = False
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(0, result["images"])
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(1, flow.complete_clicks)
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_已有打勾图片时只审核未打勾并提交(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
        )
        flow.group_items = [
            image_item("pic-1", "file-1", "reviewed"),
            image_item("pic-2", "file-2", "unreviewed"),
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual({"pic-2"}, runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)
        self.assertFalse(flow.dialog.visible)

    def test_重试沿用首次200并重新检查渲染(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            render_ready=False,
            thumbnail_download_after=None,
            render_on_thumbnail_click=True,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual([0], flow.downloads)
        self.assertEqual([0], flow.thumbnail_clicks)
        self.assertEqual(1, flow.review_clicks)

    def test_刷新页码不一致不记完成且不重发(self):
        page = FakePage()
        flow = ReviewFlow(page, refresh_current=2)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown) as ctx:
                runner.process_task(flow.entry)

        self.assertIn("页码", str(ctx.exception))
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_任务消失导致页码前移一页仍记完成(self):
        page = FakePage()
        flow = ReviewFlow(page, refresh_current=1)
        flow.entry["page"] = 2
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])

    def test_刷新筛选不一致不记完成(self):
        page = FakePage()
        flow = ReviewFlow(page, refresh_task_status="task_completed")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown) as ctx:
                runner.process_task(flow.entry)

        self.assertIn("筛选", str(ctx.exception))
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_首次加载失败后点击缩略图重试成功(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            auto_first_download=False,
            thumbnail_download_after=0,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual([0], flow.thumbnail_clicks)
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(1, flow.confirm_clicks)

    def test_审核按钮稍后启用时等待而不是立即停止(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            auto_first_download=False,
            thumbnail_download_after=0,
        )
        flow.dialog.review_button.enabled = False
        downloaded = {"value": False}
        original_emit = flow.emit_download

        def emit_and_flag(index):
            original_emit(index)
            downloaded["value"] = True

        flow.emit_download = emit_and_flag

        def enable_review():
            if downloaded["value"]:
                flow.dialog.review_button.enabled = True

        page.on_wait = enable_review
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("completed", result["status"])
        self.assertEqual(1, flow.review_clicks)

    def test_首次加载加一次重试均失败后关闭弹窗并转人工(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            auto_first_download=False,
            thumbnail_download_after=None,
            close_mode="ok",
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            screenshots = list(Path(tmpdir).glob("failure_*.png"))
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(0, result["images"])
        self.assertEqual(["pic-1"], result["load_failures"])
        self.assertIn("图片加载失败", result["reason"])
        self.assertIn("1 张", result["reason"])
        self.assertEqual([0], flow.thumbnail_clicks)
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)
        self.assertTrue(screenshots)
        self.assertEqual(list(drt.ANOMALY_HEADERS), rows[0])
        self.assertEqual("pic-1", rows[1][12])
        self.assertEqual("已跳过，待人工审核", rows[1][14])
        self.assertEqual("任务未审核数", rows[0][13])
        self.assertEqual("1", rows[1][13])

    def test_第二张图片失败时不点最终确认也不提交(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            auto_next_download=False,
            thumbnail_download_after=None,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual(["pic-2"], result["load_failures"])
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)
        self.assertEqual("pic-2", rows[1][12])
        self.assertEqual("2", rows[1][13])

    def test_只审核未打勾图片并跳过已打勾图片(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[
                ("pic-1", "file-1"),
                ("pic-2", "file-2"),
                ("pic-3", "file-3"),
                ("pic-4", "file-4"),
            ],
            pic_defect_flags={2: drt.SYSTEM_DEFECT_FLAG},
            suppress_final_confirm=True,
        )
        flow.group_items = [
            image_item("pic-1", "file-1", "reviewed", "no_defect"),
            image_item("pic-2", "file-2", "unreviewed", "no_defect"),
            image_item("pic-3", "file-3", "unreviewed", drt.SYSTEM_DEFECT_FLAG),
            image_item("pic-4", "file-4", "unreviewed", "unlabeled"),
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(2, result["images"])
        self.assertEqual({"pic-2", "pic-4"}, runner.reviewed_guids)
        self.assertEqual(["pic-3"], result["system_defects"])
        self.assertEqual(2, flow.review_clicks)
        self.assertEqual(0, flow.review_clicks_on_system_defect)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)
        self.assertEqual("pic-3", rows[1][13])

    def test_首张加载失败后继续审核后面的未打勾图片(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            missing_downloads={0},
            suppress_final_confirm=True,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual(["pic-1"], result["load_failures"])
        self.assertEqual({"pic-2"}, runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)

    def test_首图遮罩常驻时仍进入图片审核并跳过失败首图(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            missing_downloads={0},
            suppress_final_confirm=True,
        )
        flow.dialog.add(".el-loading-mask", FakeElement("", True, True, "图片加载遮罩"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(["pic-1"], result["load_failures"])
        self.assertEqual({"pic-2"}, runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)

    def test_无法定位当前图片缩略图时保存截图并停止(self):
        page = FakePage()
        flow = ReviewFlow(page, auto_first_download=False)
        flow.dialog.children["img"] = [
            image
            for image in flow.dialog.children.get("img", [])
            if "isthumb=yes" not in image.src.casefold()
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.process_task(flow.entry)
            screenshots = list(Path(tmpdir).glob("failure_*.png"))

        self.assertIn("缩略图", str(ctx.exception))
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual([], flow.submits)
        self.assertTrue(screenshots)

    def test_关闭出现二次确认时保存截图并停止(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            auto_first_download=False,
            thumbnail_download_after=None,
            close_mode="secondary",
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.process_task(flow.entry)
            screenshots = list(Path(tmpdir).glob("failure_*.png"))

        self.assertIn("二次确认", str(ctx.exception))
        self.assertEqual(0, flow.secondary_clicks)
        self.assertEqual([], flow.submits)
        self.assertTrue(flow.dialog.visible)
        self.assertTrue(screenshots)

    def test_关闭失败时保存截图并停止(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            auto_first_download=False,
            thumbnail_download_after=None,
            close_mode="fail",
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped):
                runner.process_task(flow.entry)
            screenshots = list(Path(tmpdir).glob("failure_*.png"))

        self.assertEqual([], flow.submits)
        self.assertTrue(flow.dialog.visible)
        self.assertTrue(screenshots)

    def test_未出现审核弹窗时保存截图并停止(self):
        page = FakePage()
        flow = ReviewFlow(page, show_dialog=False)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.process_task(flow.entry)
            screenshots = list(Path(tmpdir).glob("failure_*.png"))

        self.assertIn("弹窗", str(ctx.exception))
        self.assertEqual(0, flow.review_clicks)
        self.assertTrue(screenshots)

    def test_图片分组响应任务GUID不一致时停止(self):
        page = FakePage()
        flow = ReviewFlow(page, group_task_guid="other-task")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped):
                runner.process_task(flow.entry)

        self.assertEqual(0, flow.review_clicks)
        self.assertEqual([], flow.submits)

    def test_图片缺少GUID时保存截图并停止(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.group_items = [{"fileGuid": "file-1"}]
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped):
                runner.process_task(flow.entry)
            screenshots = list(Path(tmpdir).glob("failure_*.png"))

        self.assertEqual(0, flow.review_clicks)
        self.assertTrue(screenshots)

    def test_最终确认文案不明确时不盲点(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.confirm_box._text = "是否关闭？"
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped):
                runner.process_task(flow.entry)

        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)

    def test_提交成功但列表未刷新时不记完成(self):
        page = FakePage()
        flow = ReviewFlow(page, refresh_on_submit=False)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown) as ctx:
                runner.process_task(flow.entry)

        self.assertIn("列表刷新", str(ctx.exception))
        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_提交结果不明时停止且不重发(self):
        page = FakePage()
        flow = ReviewFlow(page, submit_mode="unknown")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown):
                runner.process_task(flow.entry)

        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual(1, flow.submit_clicks)
        self.assertEqual([flow.task_guid], flow.submits)

    def test_提交已知业务失败时不重发(self):
        page = FakePage()
        flow = ReviewFlow(page, submit_mode="business_failure")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteRejected):
                runner.process_task(flow.entry)

        self.assertEqual(1, flow.confirm_clicks)
        self.assertEqual(1, flow.submit_clicks)

    def test_审核响应缺失时停止且不提交(self):
        page = FakePage()
        flow = ReviewFlow(page, review_mode="unknown")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.WriteResultUnknown):
                runner.process_task(flow.entry)

        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)

    def test_取消时既不审核也不提交且不关闭用户浏览器(self):
        page = FakePage()
        flow = ReviewFlow(page, auto_first_download=False)
        calls = {"count": 0}

        def cancel():
            if flow.entered:
                calls["count"] += 1
                if calls["count"] >= 3:
                    raise KeyboardInterrupt

        flow.cancel_check = cancel
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, cancel_check=cancel)
            runner.browser = FakeBrowser()
            runner.playwright = FakePlaywright()
            with self.assertRaises(KeyboardInterrupt):
                runner.process_task(flow.entry)
            runner.close()

        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertTrue(flow.dialog.visible)
        self.assertFalse(runner.browser.closed)
        self.assertFalse(page.closed)
        self.assertTrue(runner.playwright is None)


class SkipTasksFileTest(unittest.TestCase):
    def test_追加前补缺失换行(self):
        """旧表末行没有换行符时，直接追加会把两条任务名粘成一条。"""
        with tempfile.TemporaryDirectory() as tmpdir:
            txt = Path(tmpdir) / "跳过任务.txt"
            txt.write_text("任务名称\n任务A", encoding="utf-8")
            self.assertTrue(drt.append_skip_task_name(txt, "任务B"))
            text = txt.read_text(encoding="utf-8")
            txt_names = drt.load_skip_task_name_rows(txt)

            csv_path = Path(tmpdir) / "跳过任务.csv"
            csv_path.write_text("任务名称\n任务A", encoding="utf-8")
            self.assertTrue(drt.append_skip_task_name(csv_path, "任务B"))
            # read_text 会做换行归一，这里只校验「追加前补了换行」这一事实
            csv_text = csv_path.read_text(encoding="utf-8")
            csv_names = drt.load_skip_task_name_rows(csv_path)

        self.assertEqual("任务名称\n任务A\n任务B\n", text)
        self.assertEqual(["任务A", "任务B"], txt_names)
        self.assertEqual("任务名称\n任务A\n任务B\n", csv_text)
        self.assertEqual(["任务A", "任务B"], csv_names)

    def test_旧多列表按表头判定只取第一列(self):
        """带了备注/负责人的旧表：表头多列时按首列取任务名。"""
        with tempfile.TemporaryDirectory() as tmpdir:
            txt = Path(tmpdir) / "跳过任务.txt"
            txt.write_text(
                "任务名称\t备注\t负责人\n任务A\t误报\t张三\n", encoding="utf-8"
            )
            txt_names = drt.load_skip_task_name_rows(txt)
            self.assertTrue(drt.append_skip_task_name(txt, "任务B"))
            txt_after = txt.read_text(encoding="utf-8")

            csv_path = Path(tmpdir) / "跳过任务.csv"
            csv_path.write_text(
                "任务名称,备注,负责人\n任务A,误报,张三\n", encoding="utf-8"
            )
            csv_names = drt.load_skip_task_name_rows(csv_path)
            self.assertTrue(drt.append_skip_task_name(csv_path, "任务B"))
            csv_after = drt.load_skip_task_name_rows(csv_path)

            comma_txt = Path(tmpdir) / "逗号多列.txt"
            comma_txt.write_text(
                "任务名称,备注,负责人\n任务A,误报,张三\n", encoding="utf-8"
            )
            comma_names = drt.load_skip_task_name_rows(comma_txt)

        self.assertEqual(["任务A"], txt_names)
        # 追加只写首列，旧行的备注列原样保留
        self.assertIn("任务A\t误报\t张三", txt_after)
        self.assertEqual(["任务A"], csv_names)
        self.assertEqual(["任务A", "任务B"], csv_after)
        self.assertEqual(["任务A"], comma_names)

    def test_仅带逗号的单列表仍按整行取任务名(self):
        """表头只有一列时，行内逗号属于任务名本身，不能当分隔符。"""
        with tempfile.TemporaryDirectory() as tmpdir:
            csv_path = Path(tmpdir) / "跳过任务.csv"
            csv_path.write_text("任务名称\n110kV胜西线#1,巡检\n", encoding="utf-8")
            names = drt.load_skip_task_name_rows(csv_path)
            txt = Path(tmpdir) / "跳过任务.txt"
            txt.write_text("任务名称\n110kV胜西线#1,巡检\n", encoding="utf-8")
            txt_names = drt.load_skip_task_name_rows(txt)

        self.assertEqual(["110kV胜西线#1,巡检"], names)
        self.assertEqual(["110kV胜西线#1,巡检"], txt_names)

    def test_读取任务名称跳过表忽略标题空行和注释(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.txt"
            # 任务名称里可能本身就带逗号，整行都要保留。
            path.write_text(
                "任务名称\n任务A\n\n# 注释\n任务B,额外列\n",
                encoding="utf-8",
            )
            names = drt.load_skip_task_names(path)

        self.assertEqual({"任务a", "任务b,额外列"}, names)

    def test_读取并追加只有一列任务名称的xlsx(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.xlsx"
            drt.write_table_xlsx(
                path,
                drt.SKIP_TASK_HEADER,
                (drt.SKIP_TASK_HEADER,),
                [["任务A"]],
                log=lambda _message: None,
            )
            self.assertEqual({"任务a"}, drt.load_skip_task_names(path))
            self.assertTrue(drt.append_skip_task_name(path, "任务B"))
            self.assertFalse(drt.append_skip_task_name(path, "任务A"))
            rows = drt.load_skip_task_name_rows(path)
            raw = read_xlsx_rows(str(path))

        self.assertEqual(["任务A", "任务B"], rows)
        self.assertEqual([drt.SKIP_TASK_HEADER, "任务A", "任务B"], [r[0] for r in raw])

    def test_追加任务名称自动创建标题并去重(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.txt"
            self.assertTrue(drt.append_skip_task_name(path, "任务A"))
            self.assertFalse(drt.append_skip_task_name(path, "任务A"))
            text = path.read_text(encoding="utf-8")
            rows = [line for line in text.splitlines() if line.strip()]

        self.assertEqual(["任务名称", "任务A"], rows)

    def test_含逗号的任务名称原样读写(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            txt = Path(tmpdir) / "跳过任务.txt"
            self.assertTrue(drt.append_skip_task_name(txt, "110kV胜西线#1，巡检"))
            self.assertFalse(drt.append_skip_task_name(txt, "110kV胜西线#1，巡检"))
            csv_path = Path(tmpdir) / "跳过任务.csv"
            csv_path.write_text(
                "任务名称\n110kV胜西线#1,巡检\n另一条,线路\n", encoding="utf-8"
            )
            txt_names = drt.load_skip_task_name_rows(txt)
            csv_names = drt.load_skip_task_name_rows(csv_path)
            self.assertTrue(drt.append_skip_task_name(csv_path, "带,逗号的新任务"))
            csv_after = drt.load_skip_task_name_rows(csv_path)
            runner = make_runner(
                FakePage(), tmpdir, timeout=FAST, skip_tasks_file=str(csv_path)
            )
            self.assertTrue(runner._remember_skip_task_name("运行期,追加"))
            csv_runtime = drt.load_skip_task_name_rows(csv_path)

            xlsx_path = Path(tmpdir) / "跳过任务.xlsx"
            self.assertTrue(drt.append_skip_task_name(xlsx_path, "xlsx,逗号任务"))
            xlsx_names = drt.load_skip_task_name_rows(xlsx_path)

        self.assertEqual(["110kV胜西线#1，巡检"], txt_names)
        self.assertEqual(["110kV胜西线#1,巡检", "另一条,线路"], csv_names)
        self.assertEqual(["xlsx,逗号任务"], xlsx_names)
        self.assertEqual(
            ["110kV胜西线#1,巡检", "另一条,线路", "带,逗号的新任务"], csv_after
        )
        self.assertEqual(
            ["110kV胜西线#1,巡检", "另一条,线路", "带,逗号的新任务", "运行期,追加"],
            csv_runtime,
        )

    def test_跳过表读取失败不覆盖已有内容(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            txt = Path(tmpdir) / "跳过任务.txt"
            raw = b"\xff\xfe\x00broken"
            txt.write_bytes(raw)
            rows = drt.load_skip_task_name_rows(txt)
            added = drt.append_skip_task_name(txt, "任务A")
            after = txt.read_bytes()

            xlsx = Path(tmpdir) / "跳过任务.xlsx"
            xlsx.write_bytes(b"not-a-zip")
            xlsx_added = drt.append_skip_task_name(xlsx, "任务B")
            xlsx_after = xlsx.read_bytes()

        self.assertEqual([], rows)
        self.assertFalse(added)
        self.assertEqual(raw, after)
        self.assertFalse(xlsx_added)
        self.assertEqual(b"not-a-zip", xlsx_after)

    def test_空表仍可创建标题并追加(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            txt = Path(tmpdir) / "跳过任务.txt"
            txt.write_bytes(b"")
            self.assertTrue(drt.append_skip_task_name(txt, "任务A"))
            rows = drt.load_skip_task_name_rows(txt)

            xlsx = Path(tmpdir) / "空表.xlsx"
            drt.write_workbook_xlsx(
                xlsx,
                [(drt.SKIP_TASK_HEADER, (drt.SKIP_TASK_HEADER,), [])],
                log=lambda _message: None,
            )
            self.assertTrue(drt.append_skip_task_name(xlsx, "任务B"))
            xlsx_rows = drt.load_skip_task_name_rows(xlsx)

        self.assertEqual(["任务A"], rows)
        self.assertEqual(["任务B"], xlsx_rows)

    def test_更新xlsx跳过表保留其他列与其他工作表(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.xlsx"
            drt.write_workbook_xlsx(
                path,
                [
                    (
                        drt.SKIP_TASK_HEADER,
                        ("任务名称", "备注", "负责人"),
                        [["任务A", "误报", "张三"]],
                    ),
                    ("其他", ("列",), [["保留值"]]),
                ],
                log=lambda _message: None,
            )
            runner = make_runner(
                FakePage(), tmpdir, timeout=FAST, skip_tasks_file=str(path)
            )
            self.assertTrue(runner._remember_skip_task_name("任务B"))
            runner._flush_skip_table(force=True)
            first = read_xlsx_rows(str(path), drt.SKIP_TASK_HEADER)
            other = read_xlsx_rows(str(path), "其他")
            sheets = [name for name, _rows in drt.read_xlsx_workbook(path)]

        self.assertEqual(
            [
                ["任务名称", "备注", "负责人"],
                ["任务A", "误报", "张三"],
                # 新行只占 A 列：不再给备注/负责人补空单元格覆盖原有格式
                ["任务B"],
            ],
            first,
        )
        self.assertEqual([["列"], ["保留值"]], other)
        self.assertEqual([drt.SKIP_TASK_HEADER, "其他"], sheets)

    def test_xlsx跳过表读失败时批量刷新不落盘(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.xlsx"
            path.write_bytes(b"broken-zip")
            runner = make_runner(
                FakePage(), tmpdir, timeout=FAST, skip_tasks_file=str(path)
            )
            runner._skip_table_pending.append("任务C")
            runner._skip_table_flush_counter = drt.SKIP_TABLE_FLUSH_INTERVAL_NAMES
            logs = []
            runner.log_handler = logs.append
            runner._flush_skip_table(force=True)

            self.assertEqual(b"broken-zip", path.read_bytes())

        self.assertEqual(["任务C"], runner._skip_table_pending)
        self.assertTrue(any("批量写入失败" in line for line in logs))

    def test_xlsx跳过表只改首列A并保留整簿(self):
        """旧实现整簿重建，会丢其他工作表、样式、公式、合并与数字格式。"""
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.xlsx"
            build_styled_workbook(
                path,
                [
                    (
                        drt.SKIP_TASK_HEADER,
                        {
                            "before": '<cols><col min="1" max="1" width="24" '
                            'customWidth="1"/></cols>',
                            "after": '<mergeCells count="1"><mergeCell ref="B2:C2"/>'
                            "</mergeCells>",
                        },
                        # A1 标题、B2 合并区、C2 公式、B3 日期数字格式；A 列只有任务名
                        '<row r="1"><c r="A1" s="1" t="inlineStr"><is><t>任务名称</t></is></c>'
                        '<c r="B1" t="inlineStr"><is><t>备注</t></is></c></row>'
                        '<row r="2"><c r="A2" t="inlineStr"><is><t>任务A</t></is></c>'
                        '<c r="B2" t="inlineStr"><is><t>误报</t></is></c>'
                        '<c r="C2"><v>2</v></c><c r="D2" s="1"><v>45900</v></c></row>'
                        '<row r="3"><c r="A3" t="inlineStr"><is><t>任务B</t></is></c>'
                        '<c r="B3" t="inlineStr"><is><t>复核</t></is></c></row>',
                    ),
                    ("空白表", {}, ""),
                ],
            )
            runner = make_runner(
                FakePage(), tmpdir, timeout=FAST, skip_tasks_file=str(path)
            )
            with zipfile.ZipFile(path) as archive:
                blank_before = archive.read("xl/worksheets/sheet2.xml")
            self.assertTrue(runner._remember_skip_task_name("任务C"))
            runner._flush_skip_table(force=True)
            names = drt.load_skip_task_name_rows(path)
            with zipfile.ZipFile(path) as archive:
                sheet1 = archive.read("xl/worksheets/sheet1.xml").decode("utf-8")
                blank_after = archive.read("xl/worksheets/sheet2.xml")
                parts = sorted(archive.namelist())

        self.assertEqual(["任务A", "任务B", "任务C"], names)
        # 目标表：新增行只占 A 列，其他列原样保留
        self.assertIn('<c r="A4" t="inlineStr"><is><t>任务C</t></is></c>', sheet1)
        self.assertNotIn('r="B4"', sheet1)
        self.assertIn('<c r="B3" t="inlineStr"><is><t>复核</t></is></c>', sheet1)
        self.assertIn('<c r="C2"><v>2</v></c>', sheet1)
        self.assertIn('s="1"', sheet1)
        self.assertIn('<v>45900</v>', sheet1)
        self.assertIn('width="24"', sheet1)
        self.assertIn('<mergeCell ref="B2:C2"/>', sheet1)
        # 空表与其他部件原样保留
        self.assertEqual(blank_before, blank_after)
        for part in (
            "[Content_Types].xml",
            "xl/workbook.xml",
            "xl/_rels/workbook.xml.rels",
            "xl/styles.xml",
            "xl/worksheets/sheet1.xml",
            "xl/worksheets/sheet2.xml",
        ):
            self.assertIn(part, parts)


class BatchOutputTest(unittest.TestCase):
    def test_汇总表按任务批量刷新且JSONL即时追加(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1")],
            pic_defect_flags={0: drt.SYSTEM_DEFECT_FLAG},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.settings = make_settings()
            result = runner.process_task(flow.entry)
            xlsx_path = Path(tmpdir) / drt.SUMMARY_XLSX_NAME
            jsonl_path = Path(tmpdir) / drt.SUMMARY_JSONL_NAME
            self.assertFalse(xlsx_path.exists())
            lines = jsonl_path.read_text(encoding="utf-8").splitlines()
            runner._flush_outputs()
            rows = read_xlsx_rows(str(xlsx_path), drt.DEFECT_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(1, len(lines))
        self.assertEqual("defect", json.loads(lines[0])["type"])
        self.assertEqual(list(drt.SYSTEM_DEFECT_HEADERS), rows[0])
        self.assertEqual(2, len(rows))

    def test_汇总行数达到阈值时刷新xlsx(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(FakePage(), tmpdir, timeout=FAST)
            runner._record_system_defect(
                {"task_name": "任务一", "task_guid": "guid-1"},
                make_settings(),
                drt.ReviewImage("pic-1", "file-1", 0, original_name="pic-1.jpg"),
                [],
            )
            runner._write_summary_xlsx()
            xlsx_path = Path(tmpdir) / drt.SUMMARY_XLSX_NAME
            before = xlsx_path.exists()
            runner._rows_since_summary_write = drt.SUMMARY_WRITE_ROW_THRESHOLD
            runner._write_summary_xlsx()
            after = xlsx_path.exists()

        self.assertFalse(before)
        self.assertTrue(after)

    def test_xlsx跳过表批量追加而txt立即追加(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            xlsx_path = Path(tmpdir) / "跳过任务.xlsx"
            drt.write_table_xlsx(
                xlsx_path,
                drt.SKIP_TASK_HEADER,
                (drt.SKIP_TASK_HEADER,),
                [["任务A"]],
                log=lambda _message: None,
            )
            xlsx_runner = make_runner(
                FakePage(),
                tmpdir,
                timeout=FAST,
                skip_tasks_file=str(xlsx_path),
            )
            self.assertTrue(xlsx_runner._remember_skip_task_name("任务B"))
            self.assertFalse(xlsx_runner._remember_skip_task_name("任务B"))
            before = drt.load_skip_task_name_rows(xlsx_path)
            xlsx_runner._flush_skip_table(force=True)
            after = drt.load_skip_task_name_rows(xlsx_path)

            txt_path = Path(tmpdir) / "跳过任务.txt"
            txt_runner = make_runner(
                FakePage(),
                tmpdir,
                timeout=FAST,
                skip_tasks_file=str(txt_path),
            )
            self.assertTrue(txt_runner._remember_skip_task_name("任务C"))
            txt_names = drt.load_skip_task_names(txt_path)

        self.assertEqual(["任务A"], before)
        self.assertEqual(["任务A", "任务B"], after)
        self.assertIn("任务c", txt_names)


class ButtonNotReadyTest(unittest.TestCase):
    def test_加载失败任务自动写入任务名称跳过表(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1")],
            auto_first_download=False,
            thumbnail_download_after=None,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            skip_file = Path(tmpdir) / "跳过任务.txt"
            runner = make_runner(
                page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                skip_tasks_file=str(skip_file),
            )
            runner.settings = make_settings()
            result = runner.process_task(flow.entry)
            rows = [
                line
                for line in skip_file.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]

        self.assertEqual("deferred", result["status"])
        self.assertEqual(["pic-1"], result["load_failures"])
        self.assertEqual(["任务名称", "任务一"], rows)

    def test_图片已显示按钮未就绪时写单独表且不记加载失败(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1")],
            review_button_enabled=False,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.settings = make_settings()
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME)
            defect_rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME
            )

        self.assertEqual("deferred", result["status"])
        self.assertEqual(["pic-1"], result["button_not_ready"])
        self.assertEqual([], result["load_failures"])
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertEqual(list(drt.ANOMALY_HEADERS), rows[0])
        self.assertEqual(1, len(rows) - 1)
        self.assertIn("按钮未就绪", rows[1][1])
        self.assertIn("图片已显示", rows[1][2])
        self.assertEqual(1, len(defect_rows))


class IgnoreDefectBoxesTest(unittest.TestCase):
    def test_勾选无视缺陷框时正常审核已标记缺陷图片(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1")],
            pic_defect_flags={0: drt.SYSTEM_DEFECT_FLAG},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                ignore_defect_boxes=True,
            )
            runner.settings = make_settings()
            result = runner.process_task(flow.entry)
            defect_path = Path(tmpdir) / drt.SUMMARY_XLSX_NAME

        self.assertEqual("completed", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual({"pic-1"}, runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)
        self.assertFalse(defect_path.exists())


class SystemDefectProcessTest(unittest.TestCase):
    def _runner(self, tmpdir, flow, **overrides):
        values = {
            "timeout": FAST,
            "image_timeout": FAST,
            "image_retries": 1,
        }
        values.update(overrides)
        runner = make_runner(flow.page, tmpdir, **values)
        runner.settings = make_settings()
        return runner

    def test_进度按系统缺陷与审核图片推进(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            pic_defect_flags={0: drt.SYSTEM_DEFECT_FLAG},
        )
        calls = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow, timeout=3)
            runner.progress_handler = lambda current, total, _label: calls.append(
                (current, total)
            )
            runner.process_task(flow.entry)

        self.assertIn((1, 2), calls)
        self.assertIn((2, 2), calls)

    def test_跳过系统缺陷框图片后审核其他图片并手动关闭(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[
                ("pic-1", "file-1"),
                ("pic-2", "file-2"),
                ("pic-3", "file-3"),
            ],
            pic_defect_flags={1: drt.SYSTEM_DEFECT_FLAG},
            tagging_details={
                1: [
                    {
                        "defectName": "导地线类-异物",
                        "defectLevelName": "一般",
                        "detailedDescription": "左侧导线有异物悬挂",
                        "xmin": 322,
                        "ymin": 1107,
                        "xmax": 492,
                        "ymax": 1175,
                        "shortName": "仿线算法",
                        "algorithmType": "yes",
                    }
                ]
            },
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(2, result["images"])
        self.assertEqual(["pic-2"], result["system_defects"])
        self.assertEqual({"pic-1", "pic-3"}, runner.reviewed_guids)
        self.assertEqual(2, flow.review_clicks)
        self.assertEqual(0, flow.review_clicks_on_system_defect)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertEqual([2], flow.thumbnail_clicks)
        self.assertFalse(flow.dialog.visible)
        self.assertEqual(list(drt.SYSTEM_DEFECT_HEADERS), rows[0])
        self.assertEqual(1, len(rows) - 1)
        row = rows[1]
        self.assertEqual("交流220kV", row[0])
        self.assertEqual("测试线路", row[1])
        # 多杆塔任务按图片自身的杆塔记录，不再统一写成任务首基杆塔。
        self.assertEqual("测试线路：#12", row[2])
        self.assertEqual("#12", row[3])
        self.assertEqual("任务一", row[4])
        self.assertEqual("pic-2.jpg", row[6])
        self.assertEqual("导地线类-异物（左侧导线有异物悬挂）", row[8])
        self.assertEqual("一般", row[9])
        self.assertEqual("322,1107,492,1175", row[10])
        self.assertEqual("仿线算法", row[11])
        self.assertEqual("pic-2", row[13])
        self.assertEqual("已跳过，待人工审核", row[14])

    def test_图片框标记为unlabeled但明细有数据时也跳过(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[
                ("pic-1", "file-1"),
                ("pic-2", "file-2"),
            ],
            tagging_details={
                0: [
                    {
                        "defectName": "金具类-缺陷",
                        "defectLevelName": "一般",
                        "detailedDescription": "线夹破损",
                        "xmin": 572,
                        "ymin": 618,
                        "xmax": 685,
                        "ymax": 860,
                        "shortName": "仿线算法",
                        "algorithmType": "yes",
                    }
                ]
            },
            suppress_final_confirm=True,
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual(["pic-1"], result["system_defects"])
        self.assertEqual({"pic-2"}, runner.reviewed_guids)
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertEqual(1, len(rows) - 1)
        self.assertEqual("pic-1.jpg", rows[1][6])
        self.assertEqual("金具类-缺陷（线夹破损）", rows[1][8])

    def test_系统缺陷框在首张时先切到下一张再审核(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            pic_defect_flags={0: drt.SYSTEM_DEFECT_FLAG},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(1, result["images"])
        self.assertEqual({"pic-2"}, runner.reviewed_guids)
        self.assertEqual([1], flow.thumbnail_clicks)
        self.assertEqual(1, flow.review_clicks)
        self.assertEqual(0, flow.review_clicks_on_system_defect)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)

    def test_全部图片均为系统缺陷框时只关闭不审核(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            pic_defect_flags={
                0: drt.SYSTEM_DEFECT_FLAG,
                1: drt.SYSTEM_DEFECT_FLAG,
            },
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)
            runner._flush_outputs()
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME)

        self.assertEqual("deferred", result["status"])
        self.assertEqual(0, result["images"])
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual([], flow.thumbnail_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)
        self.assertEqual(2, len(rows) - 1)

    def test_未知缺陷标识跳过任务且不点审核(self):
        page = FakePage()
        flow = ReviewFlow(
            page,
            image_specs=[("pic-1", "file-1")],
            pic_defect_flags={0: "future_flag"},
        )
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            result = runner.process_task(flow.entry)

        self.assertEqual("skipped", result["status"])
        self.assertIn("未知图片缺陷标识", result["reason"])
        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(flow.dialog.visible)


class CloseTaskTest(unittest.TestCase):
    def _runner(self, tmpdir, flow):
        return make_runner(flow.page, tmpdir, timeout=FAST)

    def test_通过弹窗X关闭(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            self.assertTrue(runner.close_task(flow.dialog))

        self.assertFalse(flow.dialog.visible)
        self.assertEqual(1, flow.close_clicks)

    def test_缺少X时Escape兜底关闭(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        flow.dialog.children[".el-dialog__headerbtn"] = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            self.assertTrue(runner.close_task(flow.dialog))

        self.assertFalse(flow.dialog.visible)

    def test_X和Escape均无法关闭时停止(self):
        page = FakePage()
        flow = ReviewFlow(page, close_mode="fail")
        flow.dialog.visible = True
        flow.dialog.children[".el-dialog__headerbtn"] = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.close_task(flow.dialog)

        self.assertIn("关闭", str(ctx.exception))
        self.assertTrue(flow.dialog.visible)

    def test_关闭出现二次确认时不点击并停止(self):
        page = FakePage()
        flow = ReviewFlow(page, close_mode="secondary")
        flow.dialog.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.close_task(flow.dialog)

        self.assertIn("二次确认", str(ctx.exception))
        self.assertEqual(0, flow.secondary_clicks)
        self.assertTrue(flow.dialog.visible)

    def test_没有弹窗时无需关闭(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = False
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            self.assertTrue(runner.close_task(flow.dialog))

    def test_current_dialog为空时不关闭页面任意弹窗(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.current_dialog = None
            runner.close()

        self.assertTrue(flow.dialog.visible)

    def test_带标记弹窗在current_dialog为空时仍清理(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        flow.dialog.attributes["data-shade-review-dialog"] = "1"
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.current_dialog = None
            runner.close()

        self.assertFalse(flow.dialog.visible)

    def test_停止后不再清理弹窗且不关闭浏览器(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner.current_dialog = flow.dialog
            runner.browser = FakeBrowser()
            runner.playwright = FakePlaywright()
            runner.stop_requested = True
            runner.close()

        self.assertTrue(flow.dialog.visible)
        self.assertFalse(runner.browser.closed)
        self.assertFalse(page.closed)
        self.assertIsNone(runner.playwright)

    def test_close只清理自己的弹窗和监听而不关闭浏览器(self):
        page = FakePage()
        flow = ReviewFlow(page)
        flow.dialog.visible = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner(tmpdir, flow)
            runner._ensure_monitor()
            runner.current_dialog = flow.dialog
            runner.browser = FakeBrowser()
            runner.playwright = FakePlaywright()
            runner.close()

        self.assertFalse(flow.dialog.visible)
        self.assertFalse(runner.browser.closed)
        self.assertFalse(page.closed)
        self.assertEqual([], page.listeners.get("response", []))
        self.assertIsNone(runner.playwright)


class UnknownRecoveryTest(unittest.TestCase):
    def test_刷新网页后回到任务列表(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.reload = mock.Mock()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner._wait_for_timeout = lambda *_args, **_kwargs: None
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock()
            recovered = runner._refresh_and_resume(make_settings(), "测试")

        self.assertTrue(recovered)
        page.reload.assert_called_once()
        runner.open_task_list.assert_called_once()
        runner.apply_filters.assert_called_once()

    def test_刷新失败退避后重试(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.reload = mock.Mock(side_effect=[RuntimeError("boom"), None])
        sleeps = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner._wait_for_page_recovery = mock.Mock(return_value=False)
            runner._sleep_without_page = lambda seconds: sleeps.append(seconds)
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock()
            recovered = runner._refresh_and_resume(make_settings(), "测试")

        self.assertTrue(recovered)
        self.assertEqual(2, page.reload.call_count)
        self.assertEqual([2], sleeps)


class ConnectionRecoveryTest(unittest.TestCase):
    def test_连接断开时跳过失败截图(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST)
            runner.save_diagnostic(
                ui.page,
                "unexpected",
                error=RuntimeError("Connection closed while reading from the driver"),
            )

        self.assertTrue(runner.connection_lost)
        self.assertEqual([], ui.page.screenshots)

    def test_连接断开刷新前重建连接(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST)
            runner.connection_lost = True
            reconnects = []

            def reconnect():
                reconnects.append(True)
                runner.connection_lost = False

            runner._reconnect = reconnect
            ui.page.reload = mock.Mock()
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock()

            result = runner._refresh_and_resume(make_settings(), "测试")

        self.assertTrue(result)
        self.assertEqual([True], reconnects)
        ui.page.reload.assert_called_once()

    def test_刷新失败等待期间页面恢复后跳过重复刷新(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page, tmpdir, timeout=FAST, log_handler=logs.append
            )
            runner.page.reload = mock.Mock(
                side_effect=RuntimeError("nav timeout")
            )
            runner.open_task_list = mock.Mock(return_value=True)
            runner.apply_filters = mock.Mock()
            runner._wait_for_page_recovery = mock.Mock(return_value=True)
            result = runner._refresh_and_resume(make_settings(), "测试")

        self.assertTrue(result)
        self.assertEqual(1, runner.page.reload.call_count)
        runner.open_task_list.assert_called_once()
        runner.apply_filters.assert_called_once()
        self.assertIn("跳过刷新", "\n".join(logs))

    def test_等待恢复时轮询到任务列表立即继续(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST)
            calls = {"count": 0}

            def visible():
                calls["count"] += 1
                return calls["count"] >= 3

            runner._task_list_visible = mock.Mock(side_effect=visible)
            runner._login_page_visible = mock.Mock(return_value=False)
            runner._session_ready = mock.Mock(return_value=False)
            runner._wait_for_timeout = mock.Mock()
            recovered = runner._wait_for_page_recovery(0.1)

        self.assertTrue(recovered)
        self.assertEqual(3, runner._task_list_visible.call_count)


class RunTest(unittest.TestCase):
    def test_跳过任务后继续下一条并分别汇总(self):
        first = record("task-1", "任务一")
        second = record("task-2", "任务二")
        ui = TaskListUI([first, second])
        close = ReviewFlow(
            ui.page,
            task_guid="task-1",
            task_name="任务一",
            auto_first_download=False,
            thumbnail_download_after=None,
        )
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, close)
        attach_flow_to_ui(ui, done)

        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=logs.append,
            )
            summary = runner.run(make_settings())
            summary_data = json.loads(
                (Path(tmpdir) / "summary.json").read_text(encoding="utf-8")
            )
            screenshots = list(Path(tmpdir).glob("failure_*.png"))
            failure_rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME
            )

        self.assertEqual(1, summary["completed"])
        self.assertEqual(0, summary["skipped"])
        self.assertEqual(1, summary["deferred"])
        self.assertEqual(1, summary["images"])
        self.assertEqual(1, summary["image_load_failures"])
        self.assertEqual(1, summary["image_load_failure_tasks"])
        self.assertEqual(1, close.entered)
        self.assertEqual(1, len(summary["skip_reasons"]))
        self.assertEqual("task-1", summary["skip_reasons"][0]["guid"])
        self.assertEqual("image_failure", summary["skip_reasons"][0]["type"])
        self.assertEqual(2, len(failure_rows))
        self.assertEqual(summary, summary_data)
        self.assertTrue(screenshots)
        text = "\n".join(logs)
        self.assertIn("不能宣称全部审核完成", text)
        self.assertIn("审核汇总表", text)
        self.assertNotIn("downLoadFile", text)
        self.assertNotIn("token", text.lower())
        self.assertFalse(ui.page.closed)

    def test_弹层加载超时统一刷新网页后继续下一任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        stuck = ReviewFlow(ui.page, task_guid="task-1", task_name="任务一")
        stuck.dialog.children.pop("img", None)
        stuck.dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, stuck)
        attach_flow_to_ui(ui, done)
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=logs.append,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skipped"])
        self.assertEqual(1, done.entered)
        self.assertEqual([], stuck.submits)
        self.assertIn(("reload", START_URL), ui.page.actions)
        self.assertIn("刷新网页", "\n".join(logs))

    def test_弹层加载超时且无法关闭时刷新网页继续下一任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        stuck = ReviewFlow(
            ui.page,
            task_guid="task-1",
            task_name="任务一",
            close_mode="fail",
        )
        stuck.dialog.children.pop("img", None)
        stuck.dialog.add(".el-loading-mask", FakeElement("", True, True, "加载遮罩"))
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, stuck)
        attach_flow_to_ui(ui, done)
        logs = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=logs.append,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skipped"])
        self.assertEqual(2, stuck.entered)
        self.assertEqual(1, done.entered)
        self.assertIn(("reload", START_URL), ui.page.actions)
        text = "\n".join(logs)
        self.assertIn("已刷新网页回到任务列表，继续", text)

    def test_命中任务名称跳过表时不再进入任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            skip_file = Path(tmpdir) / "跳过任务.txt"
            skip_file.write_text("任务名称\n任务一\n", encoding="utf-8")
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                skip_tasks_file=str(skip_file),
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skip_list_tasks"])
        self.assertEqual("skip_list", summary["skip_reasons"][0]["type"])
        self.assertEqual("task-1", summary["skip_reasons"][0]["guid"])

    def test_xlsx跳过表中的任务不会被点进(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        skipped = ReviewFlow(
            ui.page,
            task_guid="task-1",
            task_name="任务一",
            image_specs=[("pic-1", "file-1")],
        )
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, skipped)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            skip_file = Path(tmpdir) / "跳过任务.xlsx"
            drt.write_table_xlsx(
                skip_file,
                "任务名称",
                ("任务名称",),
                [["任务一"]],
                log=lambda message: None,
            )
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                skip_tasks_file=str(skip_file),
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skip_list_tasks"])
        self.assertEqual(0, skipped.entered)
        self.assertEqual(1, done.entered)

    def test_任务异常刷新后重试同一条成功(self):
        ui = TaskListUI([record("task-1", "任务一")])
        calls = []

        def fake_process(entry, _settings):
            calls.append(entry.get("guid"))
            if len(calls) == 1:
                raise drt.ReviewStopped(
                    "读取任务图片失败：Network.getResponseBody evicted"
                )
            return {"status": "completed", "images": 1, "reason": ""}

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.process_task = fake_process
            runner._refresh_and_resume = mock.Mock()
            runner._find_task_entry = mock.Mock(
                return_value={
                    "guid": "task-1",
                    "page": 1,
                    "item": record("task-1", "任务一"),
                }
            )
            summary = runner.run(make_settings())

        self.assertEqual(["task-1", "task-1"], calls)
        self.assertEqual(1, summary["completed"])
        self.assertEqual(0, summary["unknown_errors"])
        runner._refresh_and_resume.assert_called_once()

    def test_重试成功移除本条审核中止异常(self):
        ui = TaskListUI([record("task-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST, image_timeout=FAST)
            info = {"task_guid": "task-1", "task_name": "任务一"}
            runner._record_anomaly(info, make_settings(), "审核中止", "读取响应失败")
            runner._record_anomaly(info, make_settings(), "系统缺陷框", "保留")
            runner._drop_task_abort_anomalies("task-1")

        self.assertEqual(["系统缺陷框"], [row[1] for row in runner.anomaly_rows])

    def test_任务异常重试失败后跳过并继续下一条(self):
        ui = TaskListUI(
            [record("task-1", "任务一"), record("task-2", "任务二")]
        )
        calls = []

        def fake_process(entry, _settings):
            guid = entry.get("guid")
            calls.append(guid)
            if guid == "task-1":
                raise drt.ReviewStopped("Network.getResponseBody evicted")
            return {"status": "completed", "images": 1, "reason": ""}

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.process_task = fake_process
            runner._refresh_and_resume = mock.Mock()
            runner._find_task_entry = mock.Mock(
                return_value={
                    "guid": "task-1",
                    "page": 1,
                    "item": record("task-1", "任务一"),
                }
            )
            summary = runner.run(make_settings())

        self.assertEqual(["task-1", "task-1", "task-2"], calls)
        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["unknown_errors"])
        self.assertEqual(2, runner._refresh_and_resume.call_count)

    def test_输出跳过表只收集缺陷加载失败阈值(self):
        ui = TaskListUI(
            [
                record("task-1", "任务一"),
                record("task-2", "任务二"),
                record("task-3", "任务三"),
            ]
        )
        results = {
            "task-1": {
                "status": "deferred",
                "images": 0,
                "reason": "系统缺陷",
                "system_defects": ["pic-1"],
            },
            "task-2": {
                "status": "deferred",
                "images": 0,
                "reason": "按钮未就绪",
                "button_not_ready": ["pic-2"],
            },
            "task-3": {
                "status": "skipped",
                "images": 0,
                "reason": "超阈值",
                "threshold_skipped": True,
            },
        }

        def fake_process(entry, _settings):
            return results[entry["guid"]]

        calls = []
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.process_task = fake_process
            runner.progress_handler = lambda current, total, label: calls.append(
                (current, total, label)
            )
            runner.run(make_settings())
            text = (Path(tmpdir) / drt.SKIP_TASKS_OUTPUT_NAME).read_text(
                encoding="utf-8"
            )

        self.assertEqual(["任务名称", "任务一", "任务三"], text.splitlines())
        self.assertIn((1, 3, "任务进度"), calls)
        self.assertIn((2, 3, "任务进度"), calls)
        self.assertIn((3, 3, "任务进度"), calls)

    def test_超阈值任务跳过后继续下一任务并记异常(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        skipped = ReviewFlow(
            ui.page,
            task_guid="task-1",
            task_name="任务一",
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2"), ("pic-3", "file-3")],
        )
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-4", "file-4")],
        )
        attach_flow_to_ui(ui, skipped)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                max_unreviewed=2,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())
            rows = read_xlsx_rows(
                str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.ANOMALY_SHEET_NAME
            )

        self.assertEqual(1, summary["threshold_skipped"])
        self.assertEqual(1, summary["completed"])
        self.assertEqual(0, skipped.review_clicks)
        self.assertEqual(1, done.entered)
        self.assertEqual("未审核数超阈值", rows[1][1])

    def test_未知异常刷新后重试本条成功(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        calls = []

        def fake_process(entry, _settings):
            calls.append(entry["guid"])
            if len(calls) == 1:
                raise drt.UnknownTaskError("未知异常")
            return {"status": "completed", "images": 0, "reason": ""}

        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(ui.page, tmpdir, timeout=FAST, image_timeout=FAST)
            runner.process_task = fake_process
            runner._refresh_and_resume = mock.Mock(return_value=True)
            summary = runner.run(make_settings())

        self.assertEqual(["task-1", "task-1", "task-2"], calls)
        self.assertEqual(0, summary["unknown_errors"])
        self.assertEqual(2, summary["completed"])
        self.assertEqual(0, summary["skipped"])
        runner._refresh_and_resume.assert_called_once()

    def test_系统缺陷框任务转人工并生成汇总表(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        deferred = ReviewFlow(
            ui.page,
            task_guid="task-1",
            task_name="任务一",
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            pic_defect_flags={0: drt.SYSTEM_DEFECT_FLAG},
        )
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            task_name="任务二",
            image_specs=[("pic-3", "file-3")],
        )
        attach_flow_to_ui(ui, deferred)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())
            rows = read_xlsx_rows(str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME), drt.DEFECT_SHEET_NAME)
            summary_data = json.loads(
                (Path(tmpdir) / "summary.json").read_text(encoding="utf-8")
            )

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["deferred"])
        self.assertEqual(0, summary["skipped"])
        self.assertEqual(1, summary["system_defect_tasks"])
        self.assertEqual(1, summary["system_defect_images"])
        self.assertEqual(2, len(rows))
        self.assertEqual("system_defect", summary["skip_reasons"][0]["type"])
        self.assertEqual(
            str(Path(tmpdir) / drt.SUMMARY_XLSX_NAME),
            summary["summary_xlsx"],
        )
        self.assertEqual(summary, summary_data)

    def test_无active缩略图仍按按钮完成两个任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        first = ReviewFlow(
            ui.page,
            task_guid="task-1",
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            active_evidence=False,
        )
        second = ReviewFlow(
            ui.page,
            task_guid="task-2",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, first)
        attach_flow_to_ui(ui, second)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(2, summary["completed"])
        self.assertEqual(0, summary["skipped"])
        self.assertEqual(3, summary["images"])
        self.assertFalse(first.dialog.visible)
        self.assertEqual(1, second.entered)

    def test_可安全关闭的WriteRejected跳过并继续下一任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        broken = ReviewFlow(ui.page, task_guid="task-1", review_mode="business_failure")
        done = ReviewFlow(
            ui.page,
            task_guid="task-2",
            image_specs=[("pic-2", "file-2")],
        )
        attach_flow_to_ui(ui, broken)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skipped"])
        self.assertEqual(1, summary["images"])
        self.assertIn("审核失败", summary["skip_reasons"][0]["reason"])
        self.assertIn(("reload", START_URL), ui.page.actions)
        self.assertEqual(1, done.entered)

    def test_安全关闭遇二次确认时刷新网页继续下一任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        broken = ReviewFlow(
            ui.page,
            task_guid="task-1",
            image_specs=[("pic-1", "file-1"), ("pic-2", "file-2")],
            review_mode="business_failure",
            close_mode="secondary",
        )
        done = ReviewFlow(ui.page, task_guid="task-2")
        attach_flow_to_ui(ui, broken)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["skipped"])
        self.assertEqual(1, done.entered)
        self.assertEqual(0, broken.secondary_clicks)
        self.assertIn(("reload", START_URL), ui.page.actions)

    def test_WriteResultUnknown刷新网页后继续下一任务(self):
        ui = TaskListUI([record("task-1", "任务一"), record("task-2", "任务二")])
        broken = ReviewFlow(ui.page, task_guid="task-1", review_mode="unknown")
        done = ReviewFlow(ui.page, task_guid="task-2")
        attach_flow_to_ui(ui, broken)
        attach_flow_to_ui(ui, done)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                log_handler=lambda message: None,
            )
            summary = runner.run(make_settings())

        self.assertEqual(1, summary["skipped"])
        self.assertEqual(1, broken.review_clicks)
        self.assertEqual([], broken.submits)
        self.assertEqual(1, done.entered)
        self.assertIn(("reload", START_URL), ui.page.actions)

    def test_取消时run向上抛出且不提交(self):
        ui = TaskListUI([record("task-1", "任务一")])
        flow = ReviewFlow(ui.page, task_guid="task-1", auto_first_download=False)

        def cancel():
            if flow.entered:
                raise KeyboardInterrupt

        flow.cancel_check = cancel
        attach_flow_to_ui(ui, flow)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                timeout=FAST,
                image_timeout=FAST,
                cancel_check=cancel,
                log_handler=lambda message: None,
            )
            runner.browser = FakeBrowser()
            runner.playwright = FakePlaywright()
            with self.assertRaises(KeyboardInterrupt):
                runner.run(make_settings())
            runner.close()

        self.assertEqual(0, flow.review_clicks)
        self.assertEqual(0, flow.confirm_clicks)
        self.assertEqual([], flow.submits)
        self.assertFalse(runner.browser.closed)
        self.assertFalse(ui.page.closed)
        self.assertEqual(0, runner.last_run_summary["completed"])


class RedactTest(unittest.TestCase):
    def test_异常文本中的URL降为来源(self):
        text = (
            "失败 http://example.test:8080/api?token=secret&x=1#frag "
            "以及 https://user:pass@example.test/b?q=1"
        )
        redacted = drt._redact_urls(text)

        self.assertIn("http://example.test:8080", redacted)
        self.assertIn("https://example.test", redacted)
        for forbidden in ("token", "secret", "user:pass", "/api", "/b", "q=1"):
            self.assertNotIn(forbidden, redacted)

    def test_main异常日志脱敏且退出码仍为1(self):
        FakeErrorRunner.instances = []
        base = [
            "--task-status",
            "待人工审核",
            "--mission-type",
            "精细化巡检",
            "--voltage-level",
            "交流220kV",
            "--line-name",
            "测试线路",
            "--start-date",
            "2026-11-01",
            "--end-date",
            "2026-11-02",
            "--out",
            tempfile.gettempdir(),
        ]
        with tempfile.TemporaryDirectory() as tmpdir:
            base[-1] = tmpdir
            code = drt.main(base, runner_factory=FakeErrorRunner)

        self.assertEqual(1, code)
        logs = "\n".join(FakeErrorRunner.instances[0].logs)
        self.assertIn("http://example.test:8080", logs)
        for forbidden in ("token", "secret", "/api", "?x=1", "#frag"):
            self.assertNotIn(forbidden, logs)


class ReviewCliTest(unittest.TestCase):
    def test_图片超时与重试参数默认值和显式值(self):
        defaults = drt.build_parser().parse_args([])
        self.assertEqual(10, defaults.image_timeout)
        self.assertEqual(0, defaults.image_retries)

        args = drt.build_parser().parse_args(
            ["--image-timeout", "30", "--image-retries", "1"]
        )
        self.assertEqual(30, args.image_timeout)
        self.assertEqual(1, args.image_retries)

    def test_非法图片参数返回2(self):
        base = [
            "--task-status",
            "待人工审核",
            "--mission-type",
            "精细化巡检",
            "--voltage-level",
            "交流220kV",
            "--line-name",
            "测试线路",
            "--start-date",
            "2026-11-01",
            "--end-date",
            "2026-11-02",
            "--out",
            tempfile.gettempdir(),
        ]
        for extra in (["--image-timeout", "0"], ["--image-retries", "-1"]):
            with self.subTest(extra=extra):
                with contextlib.redirect_stderr(io.StringIO()):
                    code = drt.main(base + extra, runner_factory=FakeCliRunner)
                self.assertEqual(2, code)

    def test_正式模式调用run与close(self):
        FakeRunRunner.instances = []
        with tempfile.TemporaryDirectory() as tmpdir:
            code = drt.main(
                [
                    "--task-status",
                    "待人工审核",
                    "--mission-type",
                    "精细化巡检",
                    "--voltage-level",
                    "交流220kV",
                    "--line-name",
                    "测试线路",
                    "--start-date",
                    "2026-11-01",
                    "--end-date",
                    "2026-11-02",
                    "--out",
                    tmpdir,
                ],
                runner_factory=FakeRunRunner,
            )

        self.assertEqual(0, code)
        runner = FakeRunRunner.instances[0]
        self.assertTrue(
            any(isinstance(call, tuple) and call[0] == "run" for call in runner.calls)
        )
        self.assertIn("close", runner.calls)

    def test_取消退出码为130(self):
        FakeInterruptRunner.instances = []
        with tempfile.TemporaryDirectory() as tmpdir:
            code = drt.main(
                [
                    "--task-status",
                    "待人工审核",
                    "--mission-type",
                    "精细化巡检",
                    "--voltage-level",
                    "交流220kV",
                    "--line-name",
                    "测试线路",
                    "--start-date",
                    "2026-11-01",
                    "--end-date",
                    "2026-11-02",
                    "--out",
                    tmpdir,
                ],
                runner_factory=FakeInterruptRunner,
            )

        self.assertEqual(130, code)
        self.assertIn("close", FakeInterruptRunner.instances[0].calls)


class FakeRunRunner:
    instances = []

    def __init__(self, args, outdir):
        self.args = args
        self.outdir = outdir
        self.calls = []
        self.last_run_summary = {}
        FakeRunRunner.instances.append(self)

    def log(self, message):
        pass

    def load_playwright(self):
        self.calls.append("load_playwright")

    def connect(self, launched=False):
        self.calls.append(("connect", launched))

    def ensure_authenticated(self):
        self.calls.append("ensure_authenticated")

    def run(self, settings):
        self.calls.append(("run", settings))
        self.last_run_summary = {"completed": 0}
        return self.last_run_summary

    def close(self):
        self.calls.append("close")


class FakeInterruptRunner(FakeRunRunner):
    instances = []

    def __init__(self, args, outdir):
        super().__init__(args, outdir)
        FakeInterruptRunner.instances.append(self)

    def run(self, settings):
        self.calls.append("run")
        raise KeyboardInterrupt


class FakeErrorRunner(FakeRunRunner):
    instances = []

    def __init__(self, args, outdir):
        super().__init__(args, outdir)
        self.logs = []
        FakeErrorRunner.instances.append(self)

    def log(self, message):
        self.logs.append(str(message))

    def run(self, settings):
        raise RuntimeError(
            "请求失败：http://example.test:8080/api?token=secret&x=1#frag"
        )

    def save_diagnostic(self, page, prefix):
        self.calls.append(("save_diagnostic", prefix))


if __name__ == "__main__":
    unittest.main()
