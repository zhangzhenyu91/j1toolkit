# -*- coding: utf-8 -*-
"""U 盘指令协议单元测试：新旧字段归一、文件名安全化、原子读写、过期判定。"""

import json
import sys
import tempfile
import unittest
from datetime import datetime, timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import usb_protocol as p  # noqa: E402


class NormalizeTest(unittest.TestCase):
    def test_legacy_tree_command(self):
        cmd = p.normalize_command(
            {
                "version": 1,
                "cycle_id": "20261103-2350-abcd",
                "kind": "snapshot",
                "file": "检修一班树木统计表.xlsx",
                "issued_at": "2026-11-03T23:50:00+08:00",
            },
            app_hint="tree_sync",
        )
        self.assertEqual(cmd["id"], "20261103-2350-abcd")
        self.assertEqual(cmd["app"], "tree_sync")
        self.assertEqual(cmd["action"], "snapshot")
        self.assertEqual(cmd["params"]["file"], "检修一班树木统计表.xlsx")
        self.assertTrue(cmd["legacy"])

    def test_new_command_params(self):
        cmd = p.normalize_command(
            {
                "version": 1,
                "id": "daily_sync-20261103-0910-0001",
                "app": "daily_sync",
                "action": "export",
                "params": {"date": "2026-11-03"},
                "expires_at": "2026-11-03T13:10:00+08:00",
            }
        )
        self.assertEqual(cmd["action"], "export")
        self.assertEqual(cmd["params"]["date"], "2026-11-03")
        self.assertFalse(cmd["legacy"])

    def test_rejects_bad_inputs(self):
        for raw in (
            [],
            {"version": 2, "id": "x", "app": "tree_sync", "action": "snapshot"},
            {"version": 1, "id": "", "app": "tree_sync", "action": "snapshot"},
            {"version": 1, "id": "x", "app": "../bad", "action": "snapshot"},
            {"version": 1, "id": "x", "app": "tree_sync", "action": ""},
            {"version": 1, "id": "x", "app": "tree_sync", "action": "snapshot", "params": []},
        ):
            with self.assertRaises(p.ProtocolError):
                p.normalize_command(raw, app_hint="tree_sync")

    def test_expiry(self):
        cmd = p.normalize_command(
            {
                "version": 1,
                "id": "id1",
                "app": "tree_sync",
                "action": "snapshot",
                "expires_at": "2026-11-03T23:50:00+08:00",
            }
        )
        self.assertTrue(p.is_expired(cmd, datetime(2026, 11, 4, 0, 0)))
        self.assertFalse(p.is_expired(cmd, datetime(2026, 11, 3, 23, 0)))

    def test_names_are_sanitized(self):
        self.assertEqual(
            p.result_name("tree_sync", "a/b:c"), "tree_sync_result_a_b_c.json"
        )
        name = p.running_name("tree_sync", "a/b:c")
        self.assertEqual(p.parse_running_name(name), ("tree_sync", "a_b_c"))
        self.assertIsNone(p.parse_running_name("tree_sync_cmd.json"))


class IOtests(unittest.TestCase):
    def test_atomic_roundtrip(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "a.json"
            p.write_json_atomic(path, {"中文": "值", "n": 1})
            self.assertEqual(p.read_json(path)["中文"], "值")
            self.assertFalse((Path(d) / (".a.json.tmp")).exists())

    def test_rejects_oversized(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "a.json"
            path.write_text(json.dumps({"x": "y" * 100}), encoding="utf-8")
            with self.assertRaises(p.ProtocolError):
                p.read_json(path, max_bytes=10)

    def test_missing_file_is_io_error_not_protocol_error(self):
        # 读不出来（断盘/权限/文件消失）必须按 IO 错误上抛，调用方才能保留指令重试；
        # 若当成 ProtocolError，agent 会写“指令损坏”结果并删除指令。
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(OSError) as ctx:
                p.read_json(Path(d) / "missing.json")
            self.assertNotIsInstance(ctx.exception, p.ProtocolError)

    def test_result_shape(self):
        res = p.build_result(
            {"id": "x", "app": "tree_sync"},
            False,
            "boom",
            data={"n": 1},
            version="0.9.0",
        )
        self.assertFalse(res["ok"])
        self.assertEqual(res["error"], "boom")
        self.assertEqual(res["client"]["version"], "0.9.0")
        json.dumps(res, ensure_ascii=False)


if __name__ == "__main__":
    unittest.main()
