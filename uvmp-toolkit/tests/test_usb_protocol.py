# -*- coding: utf-8 -*-
"""U 盘指令协议单元测试：新旧字段归一、文件名安全化、原子读写、过期判定。"""

import json
import os
import sys
import tempfile
import unittest
from datetime import datetime, timedelta
from pathlib import Path
from unittest import mock

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

    def test_target_and_cluster_preserved(self):
        cmd = p.normalize_command(
            {
                "version": 1,
                "id": "relay-1",
                "app": "tree_sync",
                "action": "snapshot",
                "cluster": "team-a",
                "target": {"node_id": "node-1"},
                "params": {"team_id": 3},
            }
        )
        self.assertEqual(cmd["target"]["node_id"], "node-1")
        self.assertEqual(cmd["cluster"], "team-a")
        with self.assertRaises(p.ProtocolError):
            p.normalize_command(
                {
                    "version": 1,
                    "id": "relay-2",
                    "app": "tree_sync",
                    "action": "snapshot",
                    "target": {"node_id": "../bad"},
                }
            )

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


class RecoverIdTest(unittest.TestCase):
    """损坏指令的 id 恢复：服务端只按原 id 等结果，兜底 id 必然超时。"""

    def test_recover_id_from_truncated_json(self):
        raw = (
            "{\n"
            ' "version": 1,\n'
            ' "id": "20261118-1015-7c3a",\n'
            ' "cycle_id": "20261118-1015-7c3a",\n'
            ' "app": "tree_sync",\n'
            ' "action": "snapshot",\n'
            ' "params": {\n'
            '  "note": "服务端写入中断\n'
        )
        self.assertEqual(p.recover_id(raw), "20261118-1015-7c3a")

    def test_recover_id_tolerates_field_order_and_whitespace(self):
        raw = '{\n "app" : "tree_sync",\n\t"cycle_id"\t:\t"20261118-1015-7c3a",\n "params": {'
        self.assertEqual(p.recover_id(raw), "20261118-1015-7c3a")
        # id 优先于旧字段 cycle_id，与 normalize_command 的取值顺序一致。
        self.assertEqual(
            p.recover_id('{"cycle_id": "old-id", "id" : "new-id"'), "new-id"
        )

    def test_recover_id_respects_id_re_and_length(self):
        self.assertEqual(p.recover_id(b'{"id": "a/b"'), "")
        self.assertEqual(p.recover_id(b'{"id": "' + b"x" * 129 + b'"'), "")
        self.assertEqual(p.recover_id(b'{"id": "' + b"x" * 128 + b'"'), "x" * 128)
        self.assertEqual(p.recover_id(b"{not json"), "")
        self.assertEqual(p.recover_id(b"\xff\xfe\x00"), "")
        self.assertEqual(p.recover_id(None), "")

    def test_invalid_name_not_scanned_as_command(self):
        self.assertEqual(p.invalid_name("tree_sync"), "tree_sync_cmd.invalid.json")
        self.assertNotEqual(p.invalid_name("tree_sync"), p.cmd_name("tree_sync"))

    def test_recover_id_ignores_nested_id(self):
        # params 里的 id 不是指令 id：只有顶层（depth=1）的键才算数。
        self.assertEqual(
            p.recover_id('{"params": {"id": "nested-1"}, "id": "top-1"'), "top-1"
        )
        self.assertEqual(
            p.recover_id('{"id": "top-1", "params": {"id": "nested-1"'), "top-1"
        )
        self.assertEqual(
            p.recover_id('{"params": {"id": "nested-1"}, "cycle_id": "top-2"'),
            "top-2",
        )
        self.assertEqual(
            p.recover_id('{"params": {"id": "nested-1", "cycle_id": "nested-2"'), ""
        )

    def test_recover_id_handles_escaped_quotes_and_braces_in_strings(self):
        # 字符串里的 { } 与转义引号不能打乱深度判定，否则后面的顶层 id 会被漏掉。
        raw = '{"note": "a \\" } { b", "id": "top-3", "params": {"note": "x"}'
        self.assertEqual(p.recover_id(raw), "top-3")


class IOtests(unittest.TestCase):
    def test_atomic_roundtrip(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "a.json"
            p.write_json_atomic(path, {"中文": "值", "n": 1})
            self.assertEqual(p.read_json(path)["中文"], "值")
            self.assertFalse((Path(d) / (".a.json.tmp")).exists())

    def test_atomic_bytes_roundtrip(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "b.json"
            payload = "原文\n第二行".encode()
            p.write_bytes_atomic(path, payload)
            self.assertEqual(path.read_bytes(), payload)
            self.assertFalse((Path(d) / (".b.json.tmp")).exists())

    def test_atomic_bytes_detects_short_write(self):
        # os.write 短写必须报错，不能把截断的“证据”当成留存成功。
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "c.json"
            with mock.patch.object(p.os, "write", return_value=0):
                with self.assertRaises(OSError):
                    p.write_bytes_atomic(path, b"abc")
            self.assertFalse(path.exists())

    def test_read_regular_bytes_rejects_oversized(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "a.json"
            path.write_bytes(b"x" * 101)
            with self.assertRaises(p.ProtocolError):
                p.read_regular_bytes(path, max_bytes=100)
            self.assertEqual(len(p.read_regular_bytes(path, max_bytes=101)), 101)

    def test_read_regular_bytes_rejects_directory_and_symlink(self):
        # 只接受普通文件：目录/符号链接一律在读取前拒绝，避免把本机任意文件复制上盘。
        with tempfile.TemporaryDirectory() as d:
            target = Path(d) / "secret.txt"
            target.write_text("TOPSECRET", encoding="utf-8")
            with self.assertRaises(p.ProtocolError):
                p.read_regular_bytes(Path(d))
            link = Path(d) / "link.json"
            try:
                os.symlink(target, link)
            except (OSError, NotImplementedError):
                # Windows 未开开发者模式时不允许建符号链接：改用 lstat 模式直接验证守卫，
                # 关键是“根本没读目标文件”。
                fake = mock.Mock()
                fake.st_mode = 0o120777
                fake.st_size = 9
                with (
                    mock.patch.object(Path, "lstat", return_value=fake),
                    mock.patch.object(Path, "read_bytes") as reader,
                ):
                    with self.assertRaises(p.ProtocolError):
                        p.read_regular_bytes(link)
                reader.assert_not_called()
                return
            with self.assertRaises(p.ProtocolError):
                p.read_regular_bytes(link)

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

    def test_result_relay_fields(self):
        res = p.build_result(
            {"id": "x", "app": "tree_sync"},
            True,
            node="node-1",
            relayed_by="hub-1",
        )
        self.assertEqual(res["node"], "node-1")
        self.assertEqual(res["relayed_by"], "hub-1")


if __name__ == "__main__":
    unittest.main()
