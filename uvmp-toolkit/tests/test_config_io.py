# -*- coding: utf-8 -*-
"""config 读写安全测试。

覆盖审查问题：
- 写回必须原子（同目录唯一临时名 + fsync + replace），不留临时文件、不产生半截 JSON
- 现有 config.json 损坏时先备份再写（不原地覆盖原内容）；不可读时拒绝覆盖
- 递归类型校验：类型与默认值不符的更新必须被拒绝，不允许把字符串塞进 dict/bool
- 机巡（含多实例 params）密码脱敏；空密码表示“不修改”，保存后原值保留
"""

import json
import os
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import config  # noqa: E402


class ConfigTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self.cfg_path = self.tmpdir / "config.json"
        self._env = mock.patch.dict(
            os.environ,
            {
                "UVMP_TOOLKIT_CONFIG": str(self.cfg_path),
                "LOCALAPPDATA": str(self.tmpdir / "state"),
                "XDG_DATA_HOME": str(self.tmpdir / "state"),
                "XDG_CONFIG_HOME": str(self.tmpdir / "state"),
            },
        )
        self._env.start()
        self.addCleanup(self._env.stop)

    def _write_raw(self, text):
        self.cfg_path.write_text(text, encoding="utf-8")

    def _load_raw(self):
        return json.loads(self.cfg_path.read_text(encoding="utf-8"))

    def _leftover_tmp(self):
        return [p.name for p in self.tmpdir.iterdir() if p.name.endswith(".tmp")]


class ConfigAtomicWriteTest(ConfigTestCase):
    def test_写回使用同目录唯一临时名并在写入后fsync(self):
        seen = []

        def fake_replace(src, dst):
            seen.append((Path(src).name, Path(dst).name))
            os.rename(src, dst)

        with mock.patch.object(config.os, "replace", side_effect=fake_replace) as rep, (
            mock.patch.object(config.os, "fsync")
        ) as fsync:
            config.save({"ui": {"close_to_tray": False}})

        self.assertEqual(1, rep.call_count)
        self.assertGreaterEqual(fsync.call_count, 1, "落盘前必须 fsync")
        tmp_name = seen[0][0]
        self.assertTrue(tmp_name.endswith(".tmp"))
        self.assertNotEqual("config.json.tmp", tmp_name, "临时名不能是固定名（并发互踩）")
        self.assertEqual([], self._leftover_tmp())
        self.assertFalse(self._load_raw()["ui"]["close_to_tray"])

    def test_并发写回不产生半截文件也不留临时文件(self):
        errors = []

        def worker(i):
            try:
                config.save({"order_export": {"query_workers": 1 + i % 8}})
            except Exception as e:  # noqa: BLE001
                errors.append(e)

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(12)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual([], errors)
        self.assertEqual([], self._leftover_tmp())
        self.assertIn("order_export", self._load_raw())


class ConfigCorruptTest(ConfigTestCase):
    def test_损坏文件先备份且备份保留原内容(self):
        self._write_raw('{"ui": {"close_to_tray": fal')
        result = config.save({"ui": {"autostart": True}})
        backups = list(self.tmpdir.glob("config.json.corrupt-*"))
        self.assertEqual(1, len(backups), "损坏的 config.json 未备份")
        self.assertIn("fal", backups[0].read_text(encoding="utf-8"))
        self.assertTrue(result["ui"]["autostart"])
        self.assertTrue(self._load_raw()["ui"]["autostart"])

    def test_现有配置不可读时拒绝覆盖(self):
        self._write_raw('{"ui": {"autostart": true}}')
        real_read_text = Path.read_text

        def failing_read_text(self, *args, **kwargs):
            if self.name == "config.json":
                raise PermissionError("拒绝访问")
            return real_read_text(self, *args, **kwargs)

        with mock.patch.object(Path, "read_text", failing_read_text):
            with self.assertRaises(RuntimeError):
                config.save({"ui": {"autostart": False}})
        # 未读到的旧配置不能被清空覆盖
        self.assertTrue(self._load_raw()["ui"]["autostart"])


class ConfigTypeValidationTest(ConfigTestCase):
    def test_顶层类型不符拒绝保存(self):
        with self.assertRaises(config.ConfigValidationError):
            config.save({"ui": "all"})
        self.assertFalse(self.cfg_path.exists())

    def test_嵌套类型不符拒绝保存(self):
        with self.assertRaises(config.ConfigValidationError) as ctx:
            config.save({"order_export": {"query_workers": "八个"}})
        self.assertIn("order_export.query_workers", str(ctx.exception))

    def test_布尔与数字不接受互换类型(self):
        for bad in (
            {"ui": {"autostart": 1}},
            {"order_export": {"lookback_days": "62"}},
        ):
            with self.assertRaises(config.ConfigValidationError):
                config.save(bad)

    def test_列表内元素类型不符拒绝保存(self):
        with self.assertRaises(config.ConfigValidationError):
            config.save({"render": {"certificate_spki_pins": [1, 2]}})

    def test_未知键与扩展结构允许保存(self):
        result = config.save(
            {
                "defect_review": {
                    "instances": [
                        {"id": "default", "params": {"username": "u", "password": ""}}
                    ]
                },
                "custom_section": {"anything": [1, 2, 3]},
            }
        )
        params = result["defect_review"]["instances"][0]["params"]
        self.assertEqual("u", params["username"])

    def test_下划线内部字段不落盘(self):
        result = config.save({"_ini": {"a": 1}, "ui": {"autostart": True}})
        self.assertNotIn("_ini", result)
        self.assertNotIn("_config_path", self._load_raw())


class ConfigPasswordMaskTest(ConfigTestCase):
    def _cfg(self):
        return {
            "sso": {"username": "u1", "password": "sso-pw"},
            "defect_review": {
                "username": "u2",
                "password": "review-pw",
                "instances": [
                    {
                        "id": "default",
                        "params": {"username": "u3", "password": "inst-pw"},
                    }
                ],
            },
            "_ini": {"auth": {"password": "ini-pw"}},
        }

    def test_机巡与实例密码一律脱敏(self):
        view = config.masked_view(self._cfg())
        self.assertEqual("", view["defect_review"]["password"])
        self.assertTrue(view["defect_review"]["has_password"])
        inst = view["defect_review"]["instances"][0]["params"]
        self.assertEqual("", inst["password"])
        self.assertTrue(inst["has_password"])
        self.assertEqual("", view["sso"]["password"])
        dump = json.dumps(view, ensure_ascii=False)
        for secret in ("sso-pw", "review-pw", "inst-pw", "ini-pw"):
            self.assertNotIn(secret, dump)

    def test_空密码保存时保留原值(self):
        cfg = self._cfg()
        cfg.pop("_ini")
        raw = {k: v for k, v in cfg.items()}
        raw["defect_review"]["instances"][0]["params"].pop("has_password", None)
        self._write_raw(json.dumps(raw, ensure_ascii=False))

        config.save(
            {
                "defect_review": {
                    "username": "u2",
                    "password": "",
                    "instances": [
                        {
                            "id": "default",
                            "params": {"username": "u3", "password": ""},
                        }
                    ],
                }
            }
        )
        saved = self._load_raw()["defect_review"]
        self.assertEqual("review-pw", saved["password"])
        self.assertEqual("inst-pw", saved["instances"][0]["params"]["password"])

    def test_掩码值保存时同样保留原值(self):
        self._write_raw(json.dumps({"defect_review": {"password": "keep-me"}}))
        config.save({"defect_review": {"password": "******"}})
        self.assertEqual("keep-me", self._load_raw()["defect_review"]["password"])

    def test_实例未填密码时has_password回退应用级(self):
        """实例 params 没写密码时运行期会用应用级密码登录，视图必须同样显示“已配置”，
        否则前端按 has_password 判定实例可启动会误判为缺密码。"""
        cfg = {
            "defect_review": {
                "username": "u2",
                "password": "review-pw",
                "instances": [
                    {"id": "a", "params": {"username": "u3"}},
                    {"id": "b", "username": "u4"},
                ],
            }
        }
        view = config.masked_view(cfg)
        inst_a = view["defect_review"]["instances"][0]
        self.assertTrue(inst_a["params"]["has_password"])
        self.assertEqual("", inst_a["params"]["password"])
        # 实例顶层也没密码：同样回退
        self.assertTrue(inst_a["has_password"])
        inst_b = view["defect_review"]["instances"][1]
        self.assertTrue(inst_b["has_password"])
        self.assertNotIn("review-pw", json.dumps(view, ensure_ascii=False))

    def test_实例自身密码优先于应用级(self):
        cfg = {
            "defect_review": {
                "password": "",
                "instances": [
                    {"id": "a", "params": {"password": "inst-pw"}},
                    {"id": "b", "params": {"username": "u"}},
                ],
            }
        }
        view = config.masked_view(cfg)
        self.assertTrue(view["defect_review"]["instances"][0]["params"]["has_password"])
        self.assertFalse(view["defect_review"]["instances"][1]["params"]["has_password"])
        self.assertFalse(view["defect_review"]["has_password"])
        self.assertNotIn("inst-pw", json.dumps(view, ensure_ascii=False))

    def test_实例顶层密码也回退给params(self):
        """实例对象自身的 password（非 params 内）在运行期同样生效，视图口径要一致。"""
        cfg = {
            "defect_review": {
                "password": "",
                "instances": [
                    {
                        "id": "a",
                        "password": "top-pw",
                        "params": {"username": "u"},
                    }
                ],
            }
        }
        view = config.masked_view(cfg)
        inst = view["defect_review"]["instances"][0]
        self.assertTrue(inst["params"]["has_password"])
        self.assertTrue(inst["has_password"])
        self.assertEqual("", inst["password"])
        self.assertNotIn("top-pw", json.dumps(view, ensure_ascii=False))

    def test_脱敏展示字段不落盘(self):
        result = config.save(
            {
                "sso": {"username": "u", "password": "p", "has_password": True},
                "defect_review": {
                    "password": "p2",
                    "has_password": True,
                    "instances": [{"id": "default", "params": {"has_password": True}}],
                },
            }
        )
        dump = json.dumps(result, ensure_ascii=False)
        self.assertNotIn("has_password", dump)


if __name__ == "__main__":
    unittest.main()
