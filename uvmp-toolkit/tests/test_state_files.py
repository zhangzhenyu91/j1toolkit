# -*- coding: utf-8 -*-
"""state 落盘与清理安全测试。

覆盖审查问题：
- write_json 使用唯一临时名 + fsync，并发写不互踩、不留半截文件
- 状态目录/日志文件权限收紧（0700 / 0600），不依赖 umask
- clear_logs / prune_old_logs 豁免运行中任务日志与 usb-agent 常驻日志
- 历史（history）读写校验 app_id，禁止路径穿越
"""

import json
import os
import stat
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import state  # noqa: E402


class StateTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": str(self.tmpdir), "XDG_DATA_HOME": str(self.tmpdir)},
        )
        self._env.start()
        self.addCleanup(self._env.stop)


class WriteJsonAtomicTest(StateTestCase):
    def test_唯一临时名且写入后fsync(self):
        path = self.tmpdir / "stamps" / "daily.json"
        seen = []

        def fake_replace(src, dst):
            seen.append(Path(src).name)
            os.rename(src, dst)

        with mock.patch.object(state.os, "replace", side_effect=fake_replace), (
            mock.patch.object(state.os, "fsync")
        ) as fsync:
            state.write_json(path, {"a": 1})
        self.assertGreaterEqual(fsync.call_count, 1)
        self.assertTrue(seen[0].endswith(".tmp"))
        self.assertNotEqual("daily.json.tmp", seen[0])
        self.assertEqual({"a": 1}, json.loads(path.read_text(encoding="utf-8")))
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_并发写不产生半截文件(self):
        path = self.tmpdir / "stamps" / "shared.json"
        errors = []

        def worker(i):
            try:
                state.write_json(path, {"i": i, "pad": "x" * 200})
            except Exception as e:  # noqa: BLE001
                errors.append(e)

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(16)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual([], errors)
        self.assertIn("i", json.loads(path.read_text(encoding="utf-8")))


class PermissionsTest(StateTestCase):
    @unittest.skipIf(sys.platform.startswith("win"), "Windows 无 Unix 权限位")
    def test_状态目录与日志文件权限收紧(self):
        state.set_stamp("perm_check", {"date": "2026-01-01"})
        mode = stat.S_IMODE(state.state_dir().stat().st_mode)
        self.assertEqual(0o700, mode)
        stamp = state.state_dir() / "stamps" / "perm_check.json"
        self.assertEqual(0o600, stat.S_IMODE(stamp.stat().st_mode))


class LogCleanupTest(StateTestCase):
    def test_运行中任务与agent日志豁免清理(self):
        logs = state.logs_dir()
        agent = logs / "usb-agent.log"
        agent.write_text("agent\n", encoding="utf-8")
        running = logs / "20260101000000-01.log"
        running.write_text("running\n", encoding="utf-8")
        done_log = logs / "20251231000000-01.log"
        done_log.write_text("done\n", encoding="utf-8")
        state.write_json(state.jobs_dir() / "20251231000000-01.json", {"id": "x"})

        state.register_active_log(running)
        try:
            result = state.clear_logs()
        finally:
            state.unregister_active_log(running)

        self.assertTrue(agent.exists(), "agent 常驻日志不能删")
        self.assertTrue(running.exists(), "运行中任务日志不能删")
        self.assertFalse(done_log.exists())
        self.assertGreaterEqual(result["removed"], 1)
        self.assertGreaterEqual(result["skipped"], 2)

    def test_无档案的新任务日志按运行中保护(self):
        fresh = state.logs_dir() / "20260101000000-02.log"
        fresh.write_text("x\n", encoding="utf-8")
        state.clear_logs()
        self.assertTrue(fresh.exists())

    def test_过期无人认领日志仍会被自洁(self):
        stale = state.logs_dir() / "20250101000000-09.log"
        stale.write_text("x\n", encoding="utf-8")
        old = time.time() - 40 * 86400
        os.utime(stale, (old, old))
        removed = state.prune_old_logs(30)
        self.assertGreaterEqual(removed, 1)
        self.assertFalse(stale.exists())

    def test_agent日志不参与过期自洁(self):
        agent = state.logs_dir() / "usb-agent.log"
        agent.write_text("x\n", encoding="utf-8")
        old = time.time() - 90 * 86400
        os.utime(agent, (old, old))
        state.prune_old_logs(30)
        self.assertTrue(agent.exists())


class HistoryAppIdTest(StateTestCase):
    def test_非法app_id拒绝写入与读取(self):
        for bad in ("../escape", "a/b", "", "a" * 65, "app.json"):
            with self.assertRaises(ValueError):
                state.append_history(bad, {"status": "success"})
            with self.assertRaises(ValueError):
                state.recent_runs(bad, 5)
        self.assertFalse((self.tmpdir / "escape.jsonl").exists())

    def test_合法app_id正常读写(self):
        state.append_history("defect_review", {"job_id": "20260101000000-01"})
        runs = state.recent_runs("defect_review", 5)
        self.assertEqual("20260101000000-01", runs[0]["job_id"])

    def test_并发追加不丢记录(self):
        def worker(i):
            state.append_history("app_x", {"job_id": "20260101000000-%02d" % i})

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual(10, len(state.recent_runs("app_x", 50)))


class JobIdFormatTest(StateTestCase):
    def test_旧任务号仍视为合法(self):
        self.assertTrue(state.is_job_id("20260101000000-01"))
        self.assertTrue(state.is_job_id("20260101000000-99"))
        self.assertTrue(state.is_job_id("20260101000000-abc123"))

    def test_非法任务号拒绝(self):
        for bad in ("20260101000000", "../x", "20260101000000-01.log", "", "x" * 40):
            self.assertFalse(state.is_job_id(bad))

    def test_删除历史仍接受旧任务号(self):
        state.append_history(
            "app_y", {"job_id": "20260101000000-01", "title": "旧任务"}
        )
        out = state.delete_history_records("app_y", ["20260101000000-01"])
        self.assertEqual(1, out["records"])


if __name__ == "__main__":
    unittest.main()
