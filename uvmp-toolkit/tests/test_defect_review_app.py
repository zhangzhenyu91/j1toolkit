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
        # 未显式指定跳过表时用当前输出目录的「跳过任务.txt」，不再落到 state 隐藏表
        self.assertEqual(
            str(Path(values["outdir"]) / "跳过任务.txt"),
            values["skip_tasks_file"],
        )
        self.assertEqual(10, values["image_timeout"])
        self.assertFalse(values["ignore_defect_boxes"])
        self.assertNotIn("save_photos", values)

    def test_默认跳过表不读取状态目录旧表(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            state_dir = Path(tmpdir) / "state"
            legacy = state_dir / "defect-review" / "跳过任务.xlsx"
            legacy.parent.mkdir(parents=True)
            drt = self.backend._engine()
            drt.write_workbook_xlsx(
                legacy,
                [("任务名称", ("任务名称",), [["历史任务"]])],
                log=lambda _message: None,
            )
            with mock.patch.object(
                self.backend.state, "state_dir", return_value=state_dir
            ):
                values = self.backend._normalize(base_cfg(), {})
            names = drt.load_skip_task_names(values["skip_tasks_file"])

        self.assertNotEqual(str(legacy), values["skip_tasks_file"])
        self.assertTrue(values["skip_tasks_file"].endswith("跳过任务.txt"))
        self.assertEqual(set(), names)

    def test_显式跳过表路径原样保留(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            custom = os.path.join(tmpdir, "custom.txt")
            values = self.backend._normalize(
                base_cfg(skip_tasks_file=custom), {}
            )
        self.assertEqual(custom, values["skip_tasks_file"])

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

    def test_单实例也使用应用默认隔离profile(self):
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
        self.assertNotEqual(base_profile, values["profile_dir"])
        self.assertIn("browser-instances", values["profile_dir"])
        self.assertIn("defect_review-default", values["profile_dir"])

    def test_不同实例profile互不相同(self):
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9222",
            "profile_dir": "/tmp/base-profile",
            "start_url": "",
            "step_timeout": 1,
        }
        profiles = {
            self.backend._browser_values(cfg, {"instance": {"id": instance_id}})[
                "profile_dir"
            ]
            for instance_id in ("default", "a", "b", "")
        }
        # 空 id 与 default 是同一个默认实例，其余各自独立。
        self.assertEqual(3, len(profiles))

    def test_显式配置的应用级profile仍然优先(self):
        cfg = base_cfg(profile_dir="/tmp/app-profile")
        values = self.backend._browser_values(
            cfg, {"instance": {"id": "default"}}
        )
        self.assertEqual("/tmp/app-profile", values["profile_dir"])

    def test_配置布尔值字符串稳健解析(self):
        for value in ("false", "False", "0", "no", "否"):
            values = self.backend._normalize(
                base_cfg(ignore_defect_boxes=value), {}
            )
            self.assertFalse(values["ignore_defect_boxes"], value)
        for value in ("true", "1", "yes", "是"):
            values = self.backend._normalize(
                base_cfg(ignore_defect_boxes=value), {}
            )
            self.assertTrue(values["ignore_defect_boxes"], value)
        self.assertFalse(self.backend._normalize(base_cfg(), {})["ignore_defect_boxes"])

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
        self.assertEqual(10, values["reload_after_tasks"])
        self.assertEqual(200, values["reload_after_images"])
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(username=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(password=""), {})
        with self.assertRaises(RuntimeError):
            self.backend._normalize(base_cfg(start_url=""), {})

    def test_旧任务后刷新开关映射为阈值(self):
        values = self.backend._normalize(base_cfg(reload_after_task=False), {})
        self.assertEqual(0, values["reload_after_tasks"])
        self.assertEqual(0, values["reload_after_images"])

        values = self.backend._normalize(base_cfg(reload_after_task=True), {})
        self.assertEqual(10, values["reload_after_tasks"])
        self.assertEqual(200, values["reload_after_images"])

        values = self.backend._normalize(
            base_cfg(reload_after_task=False, reload_after_tasks=5, reload_after_images=50),
            {},
        )
        self.assertEqual(5, values["reload_after_tasks"])
        self.assertEqual(50, values["reload_after_images"])

    def test_自动启动失败仍连接并进入登录流程(self):
        engine = self.backend._engine()
        calls = {}

        class FakeRunner:
            def __init__(self, args, outdir, log_handler=None):
                self.args = args
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
        self.assertTrue(calls["runner"].args.save_photos)
        self.assertIn("自动启动浏览器", "\n".join(calls["runner"].logs))
        self.assertEqual(outdir, result["outdir"])

    def _run_backend(self, params, cfg=None):
        engine = self.backend._engine()
        calls = {"logs": []}

        class FakeRunner:
            def __init__(self, args, outdir, log_handler=None):
                self.args = args
                self.outdir = outdir
                calls["runner"] = self

            def log(self, message):
                calls["logs"].append(str(message))
                if self.args.__dict__.get("log_handler") is not None:
                    self.args.log_handler(str(message))

            def load_playwright(self):
                pass

            def connect(self, launched=False):
                pass

            def run(self, settings):
                return {}

            def close(self):
                pass

        class FakeJob:
            def check_cancel(self):
                return None

            def log(self, message):
                calls["logs"].append(str(message))

            def set_progress(self, current, total, label):
                pass

        outdir = tempfile.mkdtemp()
        cfg = cfg or base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9333",
            "profile_dir": os.path.join(outdir, "profile"),
            "start_url": "http://example.test/",
            "step_timeout": 1,
        }
        payload = {
            "auto_login": True,
            "username": "tester",
            "password": "secret",
            "start_url": "http://example.test/",
            "outdir": outdir,
            "skip_tasks_file": os.path.join(outdir, "skip.xlsx"),
        }
        payload.update(params)
        with (
            mock.patch.object(engine, "ReviewRunner", FakeRunner),
            mock.patch.object(self.backend._config, "load", return_value=cfg),
            mock.patch.object(
                self.backend.browser_automation,
                "status",
                return_value={"connected": True},
            ),
        ):
            self.backend.run(FakeJob(), payload)
        return "\n".join(calls["logs"])

    def test_无视缺陷框时日志不再声称始终归档(self):
        text = self._run_backend(
            {"ignore_defect_boxes": True},
            base_cfg(ignore_defect_boxes=True),
        )
        self.assertNotIn("始终开启", text)
        self.assertNotIn("缺陷照片归档：按", text)
        self.assertIn("不归档缺陷照片", text)

    def test_未勾选无视缺陷框时仍说明归档(self):
        text = self._run_backend({}, base_cfg())
        self.assertIn("缺陷照片归档：按", text)

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

    def test_实例空密码不覆盖应用级密码(self):
        # 前端「留空即不修改」会带空串：空串绝不能顶掉已保存的密码。
        values = self.backend._normalize(
            base_cfg(password="app-secret"),
            {
                "password": "",
                "instance": {"id": "defect-x", "params": {"password": ""}},
            },
        )
        self.assertEqual("app-secret", values["password"])

    def test_掩码不是密码按实例回填真实密码(self):
        cfg = base_cfg(password="app-secret")
        cfg["defect_review"]["instances"] = [
            {
                "id": "defect-a",
                "name": "实例 A",
                "params": {"password": "instance-a-secret", "username": "a"},
            },
            {
                "id": "defect-b",
                "name": "实例 B",
                "params": {"password": "instance-b-secret", "username": "b"},
            },
        ]
        values = self.backend._normalize(
            cfg,
            {
                "password": "******",
                "instance": {
                    "id": "defect-b",
                    "params": {"password": "******", "username": "b"},
                },
            },
        )
        self.assertEqual("instance-b-secret", values["password"])

    def test_脱敏视图经前端参数启动仍用实例真实密码(self):
        import config as core_config

        cfg = base_cfg(password="app-secret")
        cfg["defect_review"]["instances"] = [
            {
                "id": "defect-a",
                "name": "实例 A",
                "start_url": "http://example.test/a",
                "params": {
                    "password": "instance-a-secret",
                    "username": "a",
                    "start_url": "http://example.test/a",
                },
            }
        ]
        masked = core_config.masked_view(cfg)
        app = masked["defect_review"]
        self.assertEqual("", app["password"])
        # 前端 instanceParams：params 与 instance.params 两层都删掉空串密码
        front_params = {
            "password": "",
            "has_password": True,
            "instance_id": "defect-a",
            "username": "a",
            "start_url": "http://example.test/a",
            "instance": {
                "id": "defect-a",
                "name": "实例 A",
                "params": {
                    "username": "a",
                    "start_url": "http://example.test/a",
                    "password": "",
                    "has_password": True,
                },
            },
        }

        calls = {}

        class FakeRunner:
            def __init__(self, args, outdir, log_handler=None):
                self.args = args
                calls["args"] = args

            def log(self, message):
                pass

            def load_playwright(self):
                pass

            def connect(self, launched=False):
                pass

            def run(self, settings):
                return {}

            def close(self):
                pass

        class FakeJob:
            def check_cancel(self):
                return None

            def log(self, message):
                pass

            def set_progress(self, current, total, label):
                pass

        outdir = tempfile.mkdtemp()
        run_cfg = dict(cfg)
        run_cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9333",
            "profile_dir": os.path.join(outdir, "profile"),
            "start_url": "",
            "step_timeout": 1,
        }
        run_params = dict(front_params)
        run_params["outdir"] = outdir
        run_params["skip_tasks_file"] = os.path.join(outdir, "skip.xlsx")
        with (
            mock.patch.object(
                self.backend._engine(), "ReviewRunner", FakeRunner
            ),
            mock.patch.object(self.backend._config, "load", return_value=run_cfg),
            mock.patch.object(
                self.backend.browser_automation,
                "status",
                return_value={"connected": True},
            ),
        ):
            self.backend.run(FakeJob(), run_params)

        self.assertEqual("instance-a-secret", calls["args"].password)
        self.assertEqual("a", calls["args"].username)

    def test_实例参数缺省时回落应用级密码(self):
        cfg = base_cfg(password="app-secret")
        cfg["defect_review"]["instances"] = [{"id": "defect-a", "params": {}}]
        values = self.backend._normalize(
            cfg, {"instance": {"id": "defect-a", "params": {}}}
        )
        self.assertEqual("app-secret", values["password"])

    def test_实例标识撞名时profile目录加摘要(self):
        cfg = base_cfg()
        cfg["browser_automation"] = {
            "cdp_url": "http://127.0.0.1:9222",
            "profile_dir": "",
            "start_url": "",
            "step_timeout": 1,
        }
        slash = self.backend._browser_values(
            cfg, {"instance": {"id": "a/b"}}
        )["profile_dir"]
        underscore = self.backend._browser_values(
            cfg, {"instance": {"id": "a_b"}}
        )["profile_dir"]
        plain = self.backend._browser_values(
            cfg, {"instance": {"id": "a_b"}}
        )["profile_dir"]
        self.assertNotEqual(slash, underscore)
        # 未改写的安全标识保持原样，方便用户对照
        self.assertEqual(plain, underscore)
        self.assertIn("defect_review-a_b", underscore)
        # 改写过的标识带 sha1 前 8 位，与别的实例区分
        self.assertRegex(
            Path(slash).parent.name, r"^defect_review-a_b-[0-9a-f]{8}$"
        )


if __name__ == "__main__":
    unittest.main()
