# -*- coding: utf-8 -*-
"""缺陷识别自动审核脚本测试。

全部使用假输入与合成响应，不启动 Playwright、不连接内网。
覆盖筛选配置与响应解析（Task 1）、浏览器流程与候选迭代（Task 2），
以及图片加载重试、响应计数、提交保护和跳过/取消收尾（Task 3）。
"""

import argparse
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
import warnings
import zipfile
from pathlib import Path
from unittest import mock
from urllib.parse import urlencode
from xml.etree import ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

import defect_review_trial as drt  # noqa: E402


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
    def test_线路为空时重新询问并去除首尾空白(self):
        inp = FakeInput(["   ", " 测试线路 "])
        with contextlib.redirect_stderr(io.StringIO()):
            settings = drt.parse_settings(make_fixed_args(line_name=None), input_fn=inp)

        self.assertEqual("测试线路", settings.line_name)
        self.assertEqual(2, len(inp.prompts))

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
            ("line_name", "线路名称"),
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
        "login_timeout": 300,
        "manual_task_page": False,
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

    def inner_text(self):
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

    def inner_text(self):
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
        return True


class FakePage(FakeElement):
    def __init__(self, url=START_URL):
        super().__init__("", True, True, "页面")
        self.url = url
        self.actions = []
        self.goto_calls = []
        self.listeners = {}
        self.closed = False
        self.wait_calls = 0
        self.screenshots = []
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
        self.mission_type = FakeSelect(page, MISSION_TYPES, name="作业性质下拉")
        self.voltage = FakeSelect(page, VOLTAGE_LEVELS, name="电压等级下拉")
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
            self.goto_first_page() if key == "Enter" else None
        )
        self.jump_container.add("input", self.jump_input)
        self.pager.add(".el-pagination__jump", self.jump_container)
        page.add(".el-pagination", self.pager)

    def _line_options(self):
        return ["测试线路"] if self._voltage_value else []

    def _on_voltage(self, value):
        self._voltage_value = value
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
        if self.current_page == 1:
            return
        self.jump_count += 1
        self.current_page = 1
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
        self.assertIn("必须提供线路名称", err.getvalue())

    def test_客户端模式允许launch并等待登录(self):
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
        self.assertIn(("wait_for_login", 300000.0), runner.calls)
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


class ManualTaskPageTest(unittest.TestCase):
    def test_已手动进入任务列表时不走菜单导航(self):
        ui = TaskListUI([record("guid-1", "任务一")])
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                ui.page,
                tmpdir,
                manual_task_page=True,
                login_timeout=FAST,
            )
            changed = runner.open_task_list()

        self.assertFalse(changed)
        self.assertFalse(
            any(
                action[0] in ("click", "hover")
                and action[1] in ("智能分析", "智能识别", "缺陷识别任务")
                for action in ui.page.actions
            )
        )

    def test_未手动进入任务列表时等待超时(self):
        page = FakePage(url="about:blank")
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(
                page,
                tmpdir,
                timeout=FAST,
                manual_task_page=True,
                login_timeout=FAST,
            )
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.open_task_list()

        self.assertIn("手动进入缺陷识别任务列表", str(ctx.exception))


class LoginWaitTest(unittest.TestCase):
    def test_识别登录页(self):
        page = FakePage()
        page.add("button", FakeElement("登录", True, True, "登录按钮"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertTrue(runner.is_on_login_page())

    def test_等待到系统菜单出现(self):
        page = FakePage()
        page.add("div", FakeElement("智能分析", True, True, "系统菜单"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.wait_for_login(FAST * 1000)

    def test_非任务页URL的表格不算任务列表(self):
        page = FakePage(url="http://example.test/home")
        table = FakeElement(name="首页表格")
        table.add(".el-table__header", FakeElement("任务名称 任务状态 操作"))
        table.add(".el-table__body-wrapper", FakeElement())
        page.add(".el-table", table)
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            self.assertFalse(runner._task_list_visible())

    def test_登录URL即使有菜单文本也不放行(self):
        page = FakePage(url="http://example.test/login")
        page.add("div", FakeElement("智能分析", True, True, "背景菜单"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            with self.assertRaises(drt.ReviewStopped):
                runner.wait_for_login(FAST * 1000)

    def test_登录超时停止(self):
        page = FakePage()
        page.add("button", FakeElement("登录", True, True, "登录按钮"))
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            with self.assertRaises(drt.ReviewStopped) as ctx:
                runner.wait_for_login(FAST * 1000)

        self.assertIn("人工登录系统", str(ctx.exception))


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
        self.assertEqual(1, ui.next_clicks)
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

    def is_on_login_page(self):
        self.calls.append("is_on_login_page")
        return False

    def wait_for_login(self, timeout_ms):
        self.calls.append(("wait_for_login", timeout_ms))

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
        self.assertEqual(1, ui.jump_count)
        self.assertEqual(1, ui.query_count)


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
        flow.dialog.review_button.enabled = True
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = self._runner_with_dialog(tmpdir, flow)
            runner.wait_current_image(image, flow.dialog, mark=0)

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
            runner = self._runner(tmpdir, flow, timeout=FAST)
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
            # FAST=0.05s 在慢速 CI 上可能赶不及第 3 次轮询；给足 1 秒。
            runner = self._runner(tmpdir, flow, timeout=1)
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
            runner = make_runner(FakePage(), tmpdir, timeout=FAST)
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
            runner = make_runner(page, tmpdir, timeout=1)
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
    def test_读取任务名称跳过表忽略标题空行和注释(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "跳过任务.txt"
            path.write_text(
                "任务名称\n任务A\n\n# 注释\n任务B,额外列\n",
                encoding="utf-8",
            )
            names = drt.load_skip_task_names(path)

        self.assertEqual({"任务a", "任务b"}, names)

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
        self.assertEqual("测试线路：#1", row[2])
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
    def test_容错超时后刷新网页并回到任务列表(self):
        page = mock.Mock()
        page.is_closed.return_value = False
        page.reload = mock.Mock()
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = make_runner(page, tmpdir, timeout=FAST)
            runner.unknown_timeout_ms = 1
            runner.current_dialog = mock.Mock()
            runner.current_dialog.is_visible.return_value = True
            runner._visible_message_box = lambda: object()
            runner._wait_for_timeout = lambda *_args, **_kwargs: None
            runner.open_task_list = mock.Mock()
            runner.apply_filters = mock.Mock()
            recovered = runner._recover_after_unknown(make_settings())

        self.assertTrue(recovered)
        page.reload.assert_called_once()
        runner.open_task_list.assert_called_once()
        runner.apply_filters.assert_called_once()


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

    def test_弹层加载超时关闭成功后跳过继续下一任务(self):
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
        self.assertFalse(stuck.dialog.visible)
        self.assertNotIn(("reload", START_URL), ui.page.actions)
        self.assertIn("已安全关闭并跳过", "\n".join(logs))

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
        self.assertEqual(1, stuck.entered)
        self.assertEqual(1, done.entered)
        self.assertIn(("reload", START_URL), ui.page.actions)
        text = "\n".join(logs)
        self.assertIn("已刷新网页回到任务列表并跳过", text)

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

    def test_未知异常容错后继续下一任务(self):
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
            runner._recover_after_unknown = mock.Mock(return_value=True)
            summary = runner.run(make_settings())

        self.assertEqual(["task-1", "task-2"], calls)
        self.assertEqual(1, summary["unknown_errors"])
        self.assertEqual(1, summary["completed"])
        self.assertEqual(1, summary["skipped"])
        runner._recover_after_unknown.assert_called_once()

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
        self.assertFalse(broken.dialog.visible)
        self.assertEqual(1, done.entered)

    def test_安全关闭遇二次确认时停止且不进入下一任务(self):
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
            with self.assertRaises(drt.ReviewStopped):
                runner.run(make_settings())

        self.assertEqual(0, done.entered)
        self.assertEqual(0, broken.secondary_clicks)
        self.assertTrue(broken.dialog.secondary_box.visible)

    def test_WriteResultUnknown始终停止且不进入下一任务(self):
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
            with self.assertRaises(drt.WriteResultUnknown):
                runner.run(make_settings())

        self.assertEqual(1, broken.review_clicks)
        self.assertEqual([], broken.submits)
        self.assertEqual(0, done.entered)

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
