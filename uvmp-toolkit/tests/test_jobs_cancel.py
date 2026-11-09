# -*- coding: utf-8 -*-
"""jobs.Job 取消/提交锁语义测试。

- 钩子注册与 cancel 并发竞争：同一钩子恰好执行一次（含取消先到、注册先到两种顺序）
- cancel 幂等：重复调用不重复执行钩子
- remove 与在途快照：已进入调用中的快照不可撤回；移除后不再参与后续取消
- commit 与 cancel 同一把提交锁：提交中取消必须等待，提交结束后新 commit 立即 Cancelled
"""

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
from jobs import Cancelled  # noqa: E402


class SlowSetEvent(threading.Event):
    """is_set 首次调用时先停在时间窗内，强制出“注册已入表、取消已置位”的交错"""

    def __init__(self, delay=0.2):
        super().__init__()
        self.checked = threading.Event()
        self._delay = delay

    def is_set(self):
        self.checked.set()
        time.sleep(self._delay)
        return super().is_set()


class JobKeepAwakeTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)

    def _run(self, keep_awake):
        calls = []

        class FakeGuard:
            def __init__(self, log):
                calls.append("init")

            def start(self):
                calls.append("start")

            def stop(self):
                calls.append("stop")

        job = jobs.Job("20260101000000-02", "test", "防休眠测试")
        self.addCleanup(job._log_fp.close)
        job.keep_awake = keep_awake
        manager = jobs.JobManager()
        with mock.patch.object(jobs, "HostAwakeGuard", FakeGuard):
            manager._runner(job, lambda jb: {"ok": True})
        return calls

    def test_勾选后任务开始时启用并结束时释放(self):
        self.assertEqual(["init", "start", "stop"], self._run(True))

    def test_未勾选时不触碰防休眠(self):
        self.assertEqual([], self._run(False))


class JobActiveLookupTest(unittest.TestCase):
    def test_按应用与实例查找运行中任务(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            with mock.patch.dict(
                os.environ,
                {"LOCALAPPDATA": tmpdir, "XDG_DATA_HOME": tmpdir},
            ):
                manager = jobs.JobManager()
                job = manager.create("test-app", "测试")
                job.instance_id = "inst-1"
                job.status = "running"

                self.assertIs(job, manager.active("test-app", "inst-1"))
                self.assertIs(job, manager.active("test-app", "inst-1"))
                self.assertIsNone(manager.active("test-app", "inst-2"))
                self.assertFalse(job.to_dict()["cancelled"])

                job.cancel()
                self.assertTrue(job.to_dict()["cancelled"])
                job.status = "cancelled"
                self.assertIsNone(manager.active("test-app", "inst-1"))
                job._log_fp.close()


class JobCancelCommitTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)
        self.job = jobs.Job("20260101000000-01", "test", "取消/提交测试")
        self.addCleanup(self.job._log_fp.close)

    def test_钩子注册与cancel竞争时恰好执行一次(self):
        self.job._cancel = SlowSetEvent()
        runs = []
        hook = lambda: runs.append(1)  # noqa: E731

        register = threading.Thread(
            target=lambda: self.job.add_cancel_hook(hook), name="register"
        )
        register.start()
        self.assertTrue(self.job._cancel.checked.wait(5), "注册线程未进入检查窗口")
        cancel = threading.Thread(target=self.job.cancel, name="cancel")
        cancel.start()
        register.join(timeout=5)
        cancel.join(timeout=5)

        self.assertFalse(register.is_alive())
        self.assertFalse(cancel.is_alive())
        self.assertTrue(self.job.cancelled)
        self.assertEqual(len(runs), 1, "同一钩子在注册/取消竞争中被重复执行")

    def test_cancel重复调用不重复执行钩子(self):
        runs = []
        self.job.add_cancel_hook(lambda: runs.append(1))
        self.job.cancel()
        self.job.cancel()
        self.job.cancel()
        self.assertEqual(len(runs), 1)

    def test_remove后在途快照仍执行一次且后续取消不再触发(self):
        runs = []
        started = threading.Event()
        release = threading.Event()

        def hook():
            runs.append(1)
            started.set()
            release.wait(5)

        self.job.add_cancel_hook(hook)
        t = threading.Thread(target=self.job.cancel, name="cancel")
        t.start()
        self.assertTrue(started.wait(5))
        self.job.remove_cancel_hook(hook)  # 已进入在途快照：不可撤回
        release.set()
        t.join(timeout=5)
        self.assertFalse(t.is_alive())
        self.assertEqual(len(runs), 1)
        self.job.cancel()  # 重复取消不重放
        self.assertEqual(len(runs), 1)

    def test_提交中cancel必须等待且提交结束后commit立即取消(self):
        entered = threading.Event()
        release = threading.Event()
        cancel_done = threading.Event()

        def committer():
            with self.job.commit():
                entered.set()
                release.wait(5)

        t = threading.Thread(target=committer, name="commit")
        t.start()
        self.assertTrue(entered.wait(5))
        c = threading.Thread(
            target=lambda: (self.job.cancel(), cancel_done.set()), name="cancel"
        )
        c.start()
        self.assertFalse(cancel_done.wait(0.2), "cancel 未等待在途提交")

        release.set()
        t.join(timeout=5)
        c.join(timeout=5)
        self.assertFalse(t.is_alive())
        self.assertFalse(c.is_alive())
        self.assertTrue(cancel_done.is_set())
        self.assertTrue(self.job.cancelled)
        with self.assertRaises(Cancelled):
            with self.job.commit():
                pass


if __name__ == "__main__":
    unittest.main()
