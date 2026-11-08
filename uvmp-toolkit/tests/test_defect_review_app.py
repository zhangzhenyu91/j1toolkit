# -*- coding: utf-8 -*-
"""缺陷审核客户端应用后端参数测试。"""

import importlib.util
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for path in (str(TOOLKIT), str(ROOT)):
    if path not in sys.path:
        sys.path.insert(0, path)

import apps  # noqa: E402


def load_backend():
    meta = next((a for a in apps.scan_apps() if a["id"] == "defect_review"), None)
    if meta is None or "_backend" not in meta:
        raise AssertionError("缺陷审核应用未注册")
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
    }
    values.update(overrides)
    return {"defect_review": values}


class DefectReviewBackendTest(unittest.TestCase):
    def setUp(self):
        self.backend = load_backend()

    def test_应用已注册且声明为主应用(self):
        meta = next(a for a in apps.scan_apps() if a["id"] == "defect_review")
        self.assertEqual("缺陷审核", meta["name"])
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
        status = self.backend.status(
            cfg,
            {
                "instance": {
                    "id": "defect-x",
                    "cdp_url": "http://127.0.0.1:9401",
                    "profile_dir": "/tmp/instance-profile",
                    "start_url": "http://example.test/list",
                }
            },
        )
        self.assertEqual("http://127.0.0.1:9401", status["cdp_url"])
        self.assertEqual("http://example.test/list", status["start_url"])
        self.assertEqual("/tmp/instance-profile", status["profile_dir"])

    def test_线路和日期必填(self):
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(line_name=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(start_date=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(end_date="2026/01/01"), {})


if __name__ == "__main__":
    unittest.main()
