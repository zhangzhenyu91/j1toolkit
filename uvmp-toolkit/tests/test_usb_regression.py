# -*- coding: utf-8 -*-
"""usb-agent 回归：认领文件类型、清理范围、processed 回执、半截 JSON 重试、
pending 路径校验、锁忙提示、时间戳溢出。

全部使用临时挂载点与假后端，不接触真实 U 盘/udev/云端。
"""

import json
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import apps  # noqa: E402
import platform_utils  # noqa: E402
import usb_agent  # noqa: E402
import usb_protocol as p  # noqa: E402


class FakeBackend:
    def __init__(self):
        self.calls = 0

    def handle_command(self, job, cmd, mount):
        self.calls += 1
        job.log("fake handle %s" % cmd["id"])
        return {"action": cmd["action"]}


class _Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        base = Path(self.tmp.name)
        self._old_env = {
            k: os.environ.get(k)
            for k in ("XDG_DATA_HOME", "XDG_CONFIG_HOME", "LOCALAPPDATA", "APPDATA")
        }
        os.environ["XDG_DATA_HOME"] = str(base / "state")
        os.environ["XDG_CONFIG_HOME"] = str(base / "cfg")
        os.environ["LOCALAPPDATA"] = str(base / "localappdata")
        os.environ["APPDATA"] = str(base / "appdata")
        self.mount = base / "usb"
        self.mount.mkdir()
        self._old_scan = platform_utils.scan_usb_mounts
        platform_utils.scan_usb_mounts = lambda label, log=print: (
            "/dev/fake",
            [(str(self.mount), False, True)],
        )
        self._old_scan_apps = apps.scan_apps
        apps.scan_apps = lambda: []
        self._old_retry_sleep = usb_agent.MOUNT_RETRY_SLEEP
        usb_agent.MOUNT_RETRY_SLEEP = 0
        self._old_delay = getattr(usb_agent, "JSON_READ_DELAY", None)
        usb_agent.JSON_READ_DELAY = 0
        self.agent = usb_agent.UsbAgent(quiet=True)
        self.backend = FakeBackend()
        self.agent.apps = {
            "tree_sync": ({"id": "tree_sync", "name": "树障同步"}, self.backend)
        }
        self.agent._reload_apps = lambda force=False: None

    def tearDown(self):
        platform_utils.scan_usb_mounts = self._old_scan
        apps.scan_apps = self._old_scan_apps
        usb_agent.MOUNT_RETRY_SLEEP = self._old_retry_sleep
        if self._old_delay is None:
            del usb_agent.JSON_READ_DELAY
        else:
            usb_agent.JSON_READ_DELAY = self._old_delay
        logger = getattr(self.agent, "logger", None)
        if logger is not None:
            for handler in list(logger.handlers):
                logger.removeHandler(handler)
                try:
                    handler.close()
                except Exception:  # noqa: BLE001
                    pass
        for k, v in self._old_env.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        self.tmp.cleanup()

    def write_cmd(self, cmd, app_id="tree_sync"):
        (self.mount / p.cmd_name(app_id)).write_text(
            json.dumps(cmd, ensure_ascii=False), encoding="utf-8"
        )

    def result_files(self, app_id="tree_sync"):
        return sorted(self.mount.glob(app_id + "_result_*.json"))


class RunningClaimTest(_Base):
    def test_non_regular_running_entry_is_ignored(self):
        """.running 只处理普通文件：目录/FIFO 冒充认领文件时不得读、不得写回执。"""
        running = self.mount / p.running_name("tree_sync", "dir-1")
        running.mkdir()
        try:
            self.agent.tick()
            self.assertEqual(self.result_files(), [])
            self.assertTrue(running.is_dir(), "非普通文件不得被当作认领残留处理")
        finally:
            if running.exists():
                running.rmdir()

    def test_symlink_running_entry_is_ignored(self):
        target = Path(self.tmp.name) / "outside.json"
        target.write_text("{}", encoding="utf-8")
        running = self.mount / p.running_name("tree_sync", "link-1")
        try:
            os.symlink(str(target), str(running))
        except (OSError, NotImplementedError):
            self.skipTest("当前平台不支持创建符号链接")
        self.agent.tick()
        self.assertEqual(self.result_files(), [])

    def test_processed_command_keeps_success_receipt(self):
        """已处理过的 id 残留 .running：只能清了残留，不能改写成失败回执。"""
        cmd_id = "tree_sync-done-1"
        res = p.build_result(
            {"id": cmd_id, "app": "tree_sync"}, True, data={"action": "snapshot"}
        )
        result_path = self.mount / p.result_name("tree_sync", cmd_id)
        p.write_json_atomic(result_path, res)
        self.agent._mark_processed(cmd_id)
        running = self.mount / p.running_name("tree_sync", cmd_id)
        running.write_text("{}", encoding="utf-8")

        self.agent._recover_running(str(self.mount))

        self.assertFalse(running.exists())
        kept = json.loads(result_path.read_text(encoding="utf-8"))
        self.assertTrue(kept["ok"], "成功回执不得被“中断”结果覆盖")
        self.assertFalse(kept.get("data", {}).get("interrupted"))

    def test_processed_id_is_not_replayed_without_result_file(self):
        """同 id 重放去重不依赖结果文件是否还在（服务端可能已取走/清理）。"""
        self.agent._mark_processed("tree_sync-dup-1")
        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-dup-1",
                "app": "tree_sync",
                "action": "snapshot",
            }
        )
        self.agent.tick()
        self.assertEqual(self.backend.calls, 0)
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        self.assertEqual(self.result_files(), [])

    def test_processed_list_is_fifo_and_deduped(self):
        limit = usb_agent.PROCESSED_LIMIT
        for i in range(limit + 5):
            self.agent._mark_processed("id-%03d" % i)
        self.assertEqual(len(self.agent.processed), limit)
        self.assertNotIn("id-000", self.agent.processed)
        self.assertIn("id-%03d" % (limit + 4), self.agent.processed)
        self.agent._mark_processed("id-%03d" % (limit + 4))
        self.assertEqual(len(self.agent.processed), limit)
        saved = json.loads(self.agent.processed_path.read_text(encoding="utf-8"))
        self.assertEqual(saved["ids"][-1], "id-%03d" % (limit + 4))


class CleanupScopeTest(_Base):
    def test_cleanup_only_removes_own_app_results(self):
        old = 1000
        own = self.mount / "tree_sync_result_old.json"
        foreign = self.mount / "daily_sync_result_old.json"
        unknown = self.mount / "notes_result_old.json"
        other = self.mount / "someone_else.json"
        for f in (own, foreign, unknown, other):
            f.write_text("{}", encoding="utf-8")
            os.utime(f, (old, old))

        self.agent.agent_cfg = {"result_retention_days": 1}
        self.agent._cleanup_results(str(self.mount))

        self.assertFalse(own.exists(), "自有应用的过期结果应清理")
        for f in (foreign, unknown, other):
            self.assertTrue(f.exists(), "不得清理其它应用/无关文件: %s" % f.name)


class CommandReadTest(_Base):
    def test_half_written_command_is_retried(self):
        """第一次读到半截 JSON 时先重试，不能立刻判“指令损坏”。"""
        real_read = p.read_json
        calls = {"n": 0}

        def fake_read(path, max_bytes=65536):
            if str(path).endswith(p.cmd_name("tree_sync")):
                calls["n"] += 1
                if calls["n"] == 1:
                    raise p.ProtocolError("JSON 解析失败: Expecting value")
            return real_read(path, max_bytes)

        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-half-1",
                "app": "tree_sync",
                "action": "snapshot",
            }
        )
        with mock.patch.object(p, "read_json", side_effect=fake_read):
            self.agent.tick()
        self.assertEqual(calls["n"], 2)
        self.assertEqual(self.backend.calls, 1)
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertTrue(res["ok"])
        self.assertFalse(res["data"].get("invalid"))

    def test_command_still_being_written_is_kept(self):
        """重试期间文件内容仍在变化 = 服务端还在写：保留指令，下轮再读。"""
        cmd_path = self.mount / p.cmd_name("tree_sync")
        cmd_path.write_text('{"version": 1, "id": "x"', encoding="utf-8")

        real_read = p.read_json

        def growing_read(path, max_bytes=65536):
            if Path(path) == cmd_path:
                with open(cmd_path, "ab") as f:
                    f.write(b" ")
                raise p.ProtocolError("JSON 解析失败: 半截")
            return real_read(path, max_bytes)

        with mock.patch.object(p, "read_json", side_effect=growing_read):
            self.agent.tick()

        self.assertTrue(cmd_path.exists(), "正在写入的指令不得被删除")
        self.assertEqual(self.result_files(), [])
        self.assertEqual(self.backend.calls, 0)

    def test_stable_corrupt_command_is_marked_invalid(self):
        (self.mount / p.cmd_name("tree_sync")).write_text("{not json", encoding="utf-8")
        self.agent.tick()
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertTrue(res["data"]["invalid"])


class PendingValidationTest(_Base):
    def test_pending_running_path_is_validated(self):
        bad = self.agent.pending_dir / "result_bad.json"
        p.write_json_atomic(
            bad,
            {
                "app": "tree_sync",
                "id": "bad-1",
                "running": "../../escape.running",
                "result": {"ok": True},
            },
        )
        self.agent._flush_pending(str(self.mount))
        self.assertFalse((self.mount.parent / "escape.running").exists())
        self.assertFalse(bad.exists(), "非法 pending 应丢弃")
        self.assertEqual(self.result_files(), [])

    def test_pending_bad_app_or_id_is_dropped(self):
        for name, app_id, cmd_id in (
            ("result_bad_app.json", "../etc", "x-1"),
            ("result_bad_id.json", "tree_sync", "../../x"),
        ):
            p.write_json_atomic(
                self.agent.pending_dir / name,
                {"app": app_id, "id": cmd_id, "result": {"ok": True}},
            )
        self.agent._flush_pending(str(self.mount))
        self.assertEqual(self.result_files(), [])
        self.assertEqual(list(self.agent.pending_dir.glob("*.json")), [])

    def test_valid_pending_is_still_flushed(self):
        cmd_id = "tree_sync-pend-1"
        res = p.build_result({"id": cmd_id, "app": "tree_sync"}, True)
        running = self.mount / p.running_name("tree_sync", cmd_id)
        running.write_text("{}", encoding="utf-8")
        p.write_json_atomic(
            self.agent.pending_dir / "result_ok.json",
            {
                "app": "tree_sync",
                "id": cmd_id,
                "running": running.name,
                "result": res,
            },
        )
        self.agent._flush_pending(str(self.mount))
        self.assertFalse(running.exists())
        self.assertEqual(len(self.result_files()), 1)
        self.assertEqual(list(self.agent.pending_dir.glob("*.json")), [])


class LockBusyTest(_Base):
    def test_tick_reports_busy_instead_of_no_device(self):
        fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
        self.assertIsNotNone(fd)
        self.write_cmd(
            {"version": 1, "id": "tree_sync-lock-1", "app": "tree_sync", "action": "snapshot"}
        )
        try:
            status = self.agent.tick()
            self.assertEqual(status, "busy")
            self.assertTrue((self.mount / p.cmd_name("tree_sync")).exists())
            self.assertEqual(self.backend.calls, 0)
            with mock.patch("builtins.print") as fake_print:
                self.agent.run_once()
            printed = " ".join(str(c.args[0]) for c in fake_print.call_args_list if c.args)
            self.assertIn("usb.lock", printed)
            self.assertNotIn("未检测到卷标", printed)
        finally:
            usb_agent._release_lock(fd)

    def test_tick_returns_ok_when_locked(self):
        self.write_cmd(
            {"version": 1, "id": "tree_sync-ok-1", "app": "tree_sync", "action": "snapshot"}
        )
        self.assertEqual(self.agent.tick(), "ok")


class PruneTest(_Base):
    def test_prune_survives_timestamp_overflow(self):
        self.agent.journal_path.write_text(
            json.dumps({"id": "a", "finished_at": "9999-12-31T23:59:59+08:00"}) + "\n",
            encoding="utf-8",
        )

        def bad_ts():
            raise OverflowError("timestamp out of range")

        fake_dt = types.SimpleNamespace(timestamp=bad_ts)
        with mock.patch.object(p, "parse_iso", return_value=fake_dt):
            self.agent._prune_local(30)
        self.assertTrue(self.agent.journal_path.exists())


class ResultPathTest(_Base):
    def test_written_result_has_no_absolute_path(self):
        backend = self.backend
        backend.handle_command = lambda job, cmd, mount: {
            "action": "snapshot",
            "file": str(Path(mount) / "a.xlsx"),
            "src_deleted": str(Path(mount) / "b.xlsx"),
            "dir": str(Path(mount).parent),
        }
        self.write_cmd(
            {"version": 1, "id": "tree_sync-p-1", "app": "tree_sync", "action": "snapshot"}
        )
        self.agent.tick()
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertEqual(res["data"]["file"], "a.xlsx")
        self.assertEqual(res["data"]["src_deleted"], "b.xlsx")
        self.assertNotIn("dir", res["data"], "本机目录不应出现在盘根结果里")
        self.assertNotIn(str(self.mount), json.dumps(res, ensure_ascii=False))

    def test_plain_filenames_are_kept(self):
        self.assertEqual(
            p.minimize_result({"file": "a.xlsx", "count": 3}), {"file": "a.xlsx", "count": 3}
        )

    def test_status_file_written_on_mount(self):
        """状态心跳必须真的落到盘根（_write_status 的异常会被吞，容易静默失效）。"""
        self.write_cmd(
            {"version": 1, "id": "tree_sync-st-1", "app": "tree_sync", "action": "snapshot"}
        )
        self.agent.tick()
        status_path = self.mount / p.STATUS_NAME
        self.assertTrue(status_path.is_file(), "盘根应写出 usb_agent_status.json")
        status = json.loads(status_path.read_text(encoding="utf-8"))
        self.assertEqual(status["usb"]["label"], "GLKVM")
        self.assertEqual(status["state"], "idle")


class ResultNameParseTest(unittest.TestCase):
    def test_parse_result_name(self):
        self.assertEqual(
            p.parse_result_name("tree_sync_result_20261103-1.json"),
            ("tree_sync", "20261103-1"),
        )
        for bad in (
            "tree_sync_result_.json",
            "tree_sync_cmd.json",
            "usb_agent_status.json",
            "daily_sync_result_a.txt",
        ):
            self.assertIsNone(p.parse_result_name(bad), bad)


if __name__ == "__main__":
    unittest.main()
