# -*- coding: utf-8 -*-
"""jobs 任务生命周期健壮性测试。

覆盖审查问题：
- job_id 跨进程唯一（不再按秒 + seq%100 生成），旧任务号仍可读
- _finalize 归档异常时不能吞掉 on_done，也不能留下未关闭的日志句柄
- 启动前 cancel 生效：任务函数不执行，直接落终态并回调
- 任务体抛出 SystemExit（BaseException）不能让线程静默死掉、任务永远 running
"""

import json
import os
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

import jobs  # noqa: E402
import state  # noqa: E402


class JobsTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self._env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        self._env.start()
        self.addCleanup(self._env.stop)

    def _wait_archived(self, job, timeout=10.0):
        """等后台任务线程把档案与历史写完：临时目录会在用例结束时被清理，
        线程还在写文件时清理会随机报 “Directory not empty”。"""
        archive = state.jobs_dir() / (job.id + ".json")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if archive.exists() and job._log_fp.closed and job.finished_at:
                time.sleep(0.05)
                return
            time.sleep(0.02)
        self.fail("任务未在 %.0f 秒内归档" % timeout)


class JobIdUniqueTest(JobsTestCase):
    def test_同一秒内大量任务号不重复(self):
        manager = jobs.JobManager()
        ids = set()
        with mock.patch.object(
            jobs.time, "strftime", side_effect=lambda fmt, *a: "20260101120000"
        ):
            for _ in range(150):
                job = manager.create("app", "并发创建")
                ids.add(job.id)
                manager._jobs.pop(job.id, None)
                job._log_fp.close()
        self.assertEqual(150, len(ids), "job_id 在同一秒内发生重复")

    def test_不同管理器之间任务号不冲突(self):
        ids = set()
        for _ in range(5):
            manager = jobs.JobManager()
            job = manager.create("app", "跨进程")
            ids.add(job.id)
            job._log_fp.close()
        self.assertEqual(5, len(ids))

    def test_新任务号满足可读格式且不与既有日志冲突(self):
        manager = jobs.JobManager()
        job = manager.create("app", "格式")
        self.assertTrue(state.is_job_id(job.id), job.id)
        self.assertTrue((state.logs_dir() / (job.id + ".log")).exists())
        job._log_fp.close()


class FinalizeFailureTest(JobsTestCase):
    def test_归档异常仍回调on_done且关闭句柄(self):
        manager = jobs.JobManager()
        job = manager.create("app", "归档失败")
        done = []
        job.on_done = lambda jb: done.append(jb.status)
        with mock.patch.object(state, "write_json", side_effect=OSError("磁盘满")):
            manager.run_sync(job, lambda jb: {"ok": True})
        self.assertEqual(["success"], done)
        self.assertEqual("success", job.status)
        self.assertTrue(job._log_fp.closed)

    def test_历史写入异常也不影响on_done(self):
        manager = jobs.JobManager()
        job = manager.create("app", "历史失败")
        done = []
        job.on_done = lambda jb: done.append(jb.status)
        with mock.patch.object(
            state, "append_history", side_effect=PermissionError("拒绝")
        ):
            manager.run_sync(job, lambda jb: {})
        self.assertEqual(["success"], done)

    def test_终态任务仍会被回收(self):
        manager = jobs.JobManager()
        with mock.patch.object(manager, "MAX_TERMINAL_JOBS", 2):
            with mock.patch.object(state, "write_json", side_effect=OSError("x")):
                for _ in range(4):
                    job = manager.create("app", "回收")
                    manager.run_sync(job, lambda jb: {})
        self.assertLessEqual(len(manager._jobs), 2)


class CancelBeforeStartTest(JobsTestCase):
    def test_启动前取消不执行任务体(self):
        manager = jobs.JobManager()
        job = manager.create("app", "取消")
        calls = []
        done = []
        job.on_done = lambda jb: done.append(jb.status)
        job.cancel()
        manager.run_sync(job, lambda jb: calls.append(1))
        self.assertEqual([], calls)
        self.assertEqual("cancelled", job.status)
        self.assertEqual(["cancelled"], done)
        archive = state.jobs_dir() / (job.id + ".json")
        self.assertTrue(archive.exists())
        archived = json.loads(archive.read_text(encoding="utf-8"))
        self.assertEqual("cancelled", archived["status"])

    def test_线程启动前取消同样生效(self):
        manager = jobs.JobManager()
        job = manager.create("app", "异步取消")
        calls = []
        job.cancel()
        manager.start(job, lambda jb: calls.append(1))
        self._wait_archived(job)
        self.assertEqual([], calls)
        self.assertEqual("cancelled", job.status)


class SystemExitTest(JobsTestCase):
    def test_任务体SystemExit落失败终态而不是永远running(self):
        manager = jobs.JobManager()
        job = manager.create("app", "sys.exit")
        done = []
        job.on_done = lambda jb: done.append(jb.status)

        def body(_job):
            raise SystemExit(3)

        manager.run_sync(job, body)
        self.assertEqual("failed", job.status)
        self.assertIn("SystemExit", job.error)
        self.assertEqual(["failed"], done)

    def test_线程内SystemExit不影响其他线程与核心(self):
        manager = jobs.JobManager()
        job = manager.create("app", "异步 sys.exit")

        def body(_job):
            raise SystemExit(2)

        manager.start(job, body)
        self._wait_archived(job)
        self.assertEqual("failed", job.status)
        self.assertTrue(job.finished_at)

    def test_KeyboardInterrupt同样落终态(self):
        manager = jobs.JobManager()
        job = manager.create("app", "ctrl-c")
        manager.run_sync(job, lambda jb: (_ for _ in ()).throw(KeyboardInterrupt()))
        self.assertEqual("failed", job.status)


if __name__ == "__main__":
    unittest.main()
