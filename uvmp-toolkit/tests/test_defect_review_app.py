# -*- coding: utf-8 -*-
"""机巡审核客户端应用后端参数测试。"""

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for path in (str(TOOLKIT), str(ROOT)):
    if path not in sys.path:
        sys.path.insert(0, path)

import apps  # noqa: E402


def load_backend():
    meta = next((a for a in apps.scan_apps() if a["id"] == "defect_review"), None)
    if meta is None or "_backend" not in meta:
        raise AssertionError("机巡审核应用未注册")
    return meta["_backend"]


def base_cfg(**overrides):
    values = {
        "task_status": "待人工审核",
        "mission_type": "仿线巡检",
        "voltage_level": "交流110kV",
        "line_name": "测试线路",
        "start_date": "2026-01-01",
        "end_date": "2026-11-08",
        "outdir": "",
        "skip_tasks_file": "",
        "image_timeout": 10,
        "image_retries": 0,
        "ignore_defect_boxes": False,
        "start_url": "http://example.test/list",
        "username": "tester",
        "password": "secret",
    }
    values.update(overrides)
    return {"defect_review": values}


class DefectReviewBackendTest(unittest.TestCase):
    def setUp(self):
        self.backend = load_backend()

    def test_应用已注册且声明为主应用(self):
        meta = next(a for a in apps.scan_apps() if a["id"] == "defect_review")
        self.assertEqual("机巡审核", meta["name"])
        self.assertTrue(hasattr(meta.get("_backend"), "launch_browser"))
        self.assertTrue(hasattr(meta.get("_backend"), "status"))

    def test_参数默认输出目录与跳过表路径(self):
        values = self.backend._normalize(base_cfg(), {})
        self.assertTrue(values["outdir"])
        self.assertTrue(values["skip_tasks_file"].endswith("跳过任务.xlsx"))
        self.assertEqual(10, values["image_timeout"])
        self.assertFalse(values["ignore_defect_boxes"])

    def test_无视缺陷框开关透传(self):
        values = self.backend._normalize(base_cfg(ignore_defect_boxes=True), {})
        self.assertTrue(values["ignore_defect_boxes"])

    def test_多实例默认输出目录带实例标识(self):
        a = self.backend._normalize(base_cfg(), {"instance": {"id": "inst-a"}})
        b = self.backend._normalize(base_cfg(), {"instance": {"id": "inst-b"}})
        self.assertIn("inst-a", a["outdir"])
        self.assertIn("inst-b", b["outdir"])
        self.assertNotEqual(a["outdir"], b["outdir"])

    def test_实例参数覆盖应用参数(self):
        values = self.backend._normalize(
            base_cfg(),
            {
                "instance": {
                    "id": "defect-x",
                    "cdp_url": "http://127.0.0.1:9401",
                    "start_url": "http://example.test/list",
                    "params": {
                        "line_name": "实例线路",
                        "ignore_defect_boxes": True,
                    },
                }
            },
        )
        self.assertEqual("实例线路", values["line_name"])
        self.assertEqual("http://127.0.0.1:9401", values["cdp_url"])
        self.assertTrue(values["ignore_defect_boxes"])

    def test_状态按实例返回独立CDP与网址(self):
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9222",
            "profile_dir": "/tmp/base-profile",
            "start_url": "",
            "step_timeout": 1,
        }
        cfg["render"] = {"chrome": ""}
        profile = os.path.join(tempfile.gettempdir(), "instance-profile")
        status = self.backend.status(
            cfg,
            {
                "instance": {
                    "id": "defect-x",
                    "cdp_url": "http://127.0.0.1:9401",
                    "profile_dir": profile,
                    "start_url": "http://example.test/list",
                }
            },
        )
        self.assertEqual("http://127.0.0.1:9401", status["cdp_url"])
        self.assertEqual("http://example.test/list", status["start_url"])
        self.assertEqual(profile, status["profile_dir"])

    def test_多开实例自动分配独立profile目录(self):
        base_profile = os.path.join(tempfile.gettempdir(), "base-profile")
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9222",
            "profile_dir": base_profile,
            "start_url": "",
            "step_timeout": 1,
        }
        values = self.backend._browser_values(
            cfg,
            {
                "instance": {
                    "id": "defect-2",
                    "cdp_url": "http://127.0.0.1:9401",
                    "profile_dir": "",
                }
            },
        )
        self.assertNotEqual(base_profile, values["profile_dir"])
        self.assertIn("browser-instances", values["profile_dir"])
        self.assertIn("defect_review-defect-2", values["profile_dir"])

    def test_单实例继续复用全局profile(self):
        base_profile = os.path.join(tempfile.gettempdir(), "base-profile")
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9222",
            "profile_dir": base_profile,
            "start_url": "",
            "step_timeout": 1,
        }
        values = self.backend._browser_values(
            cfg,
            {"instance": {"id": "default", "cdp_url": "http://127.0.0.1:9400"}},
        )
        self.assertEqual(base_profile, values["profile_dir"])

    def test_线路可留空日期必填(self):
        values = self.backend._normalize(base_cfg(line_name=""), {})
        self.assertEqual("", values["line_name"])
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(start_date=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(end_date="2026/01/01"), {})

    def test_自动登录凭据和启动网址必填(self):
        values = self.backend._normalize(base_cfg(), {})
        self.assertTrue(values["auto_login"])
        self.assertTrue(values["reload_after_task"])
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(username=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(password=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(start_url=""), {})

    def test_自动启动失败仍连接并进入登录流程(self):
        engine = self.backend._engine()
        calls = {}

        class FakeRunner:
            def __init__(self, args, outdir, log_handler=None):
                self.logs = []
                calls["runner"] = self

            def log(self, message):
                self.logs.append(str(message))

            def load_playwright(self):
                pass

            def connect(self, launched=False):
                calls["launched"] = launched

            def run(self, settings):
                calls["ran"] = True
                return {}

            def close(self):
                calls["closed"] = True

        class FakeJob:
            def check_cancel(self):
                return None

            def log(self, message):
                pass

            def set_progress(self, current, total, label):
                pass

        outdir = tempfile.mkdtemp()
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9333",
            "profile_dir": os.path.join(outdir, "profile"),
            "start_url": "http://example.test/",
            "step_timeout": 1,
        }
        params = {
            "auto_login": True,
            "username": "tester",
            "password": "secret",
            "start_url": "http://example.test/",
            "outdir": outdir,
            "skip_tasks_file": os.path.join(outdir, "skip.xlsx"),
        }
        with (
            mock.patch.object(engine, "ReviewRunner", FakeRunner),
            mock.patch.object(self.backend._config, "load", return_value=cfg),
            mock.patch.object(
                self.backend.browser_automation,
                "status",
                return_value={"connected": False},
            ),
            mock.patch.object(
                self.backend.browser_automation,
                "launch",
                side_effect=RuntimeError("Timeout 15000ms exceeded"),
            ),
        ):
            result = self.backend.run(FakeJob(), params)

        self.assertTrue(calls.get("ran"))
        self.assertTrue(calls.get("launched"))
        self.assertTrue(calls.get("closed"))
        self.assertIn("自动启动浏览器", "\n".join(calls["runner"].logs))
        self.assertEqual(outdir, result["outdir"])

    def test_所有超时时间按实例覆盖(self):
        values = self.backend._normalize(
            base_cfg(),
            {
                "instance": {
                    "id": "defect-timeouts",
                    "params": {
                        "step_timeout": 45,
                        "action_timeout": 8,
                        "navigation_timeout": 25,
                        "login_result_timeout": 30,
                        "captcha_ocr_timeout": 40,
                        "image_timeout": 20,
                        "stall_timeout": 180,
                        "slow_image_seconds": 25,
                        "slow_image_count": 5,
                        "slow_restart_unreviewed": 80,
                    },
                }
            },
        )
        self.assertEqual(45, values["step_timeout"])
        self.assertEqual(8, values["action_timeout"])
        self.assertEqual(25, values["navigation_timeout"])
        self.assertEqual(30, values["login_result_timeout"])
        self.assertEqual(40, values["captcha_ocr_timeout"])
        self.assertEqual(20, values["image_timeout"])
        self.assertEqual(180, values["stall_timeout"])
        self.assertEqual(25, values["slow_image_seconds"])
        self.assertEqual(5, values["slow_image_count"])
        self.assertEqual(80, values["slow_restart_unreviewed"])

    def test_不同实例账号密码和网址互不继承(self):
        cfg = base_cfg(
            auto_login=True,
            start_url="http://example.test/a",
            username="account-a",
            password="pass-a",
        )
        values = self.backend._normalize(
            cfg,
            {
                "instance": {
                    "id": "defect-b",
                    "start_url": "http://example.test/b",
                    "params": {
                        "auto_login": True,
                        "start_url": "http://example.test/b",
                        "username": "account-b",
                        "password": "pass-b",
                    },
                }
            },
        )
        self.assertEqual("account-b", values["username"])
        self.assertEqual("pass-b", values["password"])
        self.assertEqual("http://example.test/b", values["start_url"])

    def test_作业性质电压线路空值覆盖为全部(self):
        values = self.backend._normalize(
            base_cfg(),
            {"mission_type": "", "voltage_level": "", "line_name": ""},
        )
        self.assertEqual("", values["mission_type"])
        self.assertEqual("", values["voltage_level"])
        self.assertEqual("", values["line_name"])


if __name__ == "__main__":
    unittest.main()
