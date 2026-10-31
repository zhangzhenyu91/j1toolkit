# -*- coding: utf-8 -*-
"""PDF 打印子进程取消测试：Electron 通道与 Chrome 回退都必须能被 job 取消打断。

用 fake Popen 模拟打印进程在等待期间触发取消（不启动真实浏览器），断言：
- 取消钩子终止对应进程树（走统一的进程树终止入口）
- 抛 jobs.Cancelled（不得被 Electron→Chrome 回退吞掉，也不得当作普通渲染失败）
- 取消登记在打印结束后注销，长批次不累积钩子
- 打印超时同样回收进程树，异常口径保持 subprocess.TimeoutExpired
"""

import os
import subprocess
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

import jobs  # noqa: E402
import vehicle_export as ve  # noqa: E402


class FakeJob:
    """与 jobs.Job 相同的取消接口；不落日志文件"""

    def __init__(self):
        self._cancel = threading.Event()
        self.hooks = []

    @property
    def cancelled(self):
        return self._cancel.is_set()

    def check_cancel(self):
        if self._cancel.is_set():
            raise jobs.Cancelled()

    def cancel(self):
        self._cancel.set()
        for fn in list(self.hooks):
            try:
                fn()
            except Exception:  # noqa: BLE001
                pass

    def add_cancel_hook(self, fn):
        self.hooks.append(fn)
        if self._cancel.is_set():
            try:
                fn()
            except Exception:  # noqa: BLE001
                pass

    def remove_cancel_hook(self, fn):
        try:
            self.hooks.remove(fn)
        except ValueError:
            pass


class FakeProc:
    """fake Popen：communicate 期间可触发取消，或抛超时"""

    def __init__(self, pid, on_wait=None, exit_code=0, produce=None, timeout=False):
        self.pid = pid
        self.returncode = None
        self._on_wait = on_wait
        self._exit_code = exit_code
        self._produce = produce
        self._timeout = timeout
        self.communicate_calls = 0

    def communicate(self, timeout=None):
        self.communicate_calls += 1
        if self._timeout and self.communicate_calls == 1:
            raise subprocess.TimeoutExpired(cmd=["fake"], timeout=timeout)
        if self._produce is not None:
            self._produce()
        if self._on_wait is not None:
            cb, self._on_wait = self._on_wait, None
            cb()  # 模拟：等待期间用户点了取消
        self.returncode = self._exit_code
        return b"", b""

    def poll(self):
        return self.returncode

    def wait(self, timeout=None):
        return self.returncode

    def kill(self):
        self.returncode = -9


class PrintCancelTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.pdf = Path(self._tmp.name) / "out.pdf"
        self.job = FakeJob()

    def _run(self, popen, electron="", kills=None):
        kills = [] if kills is None else kills
        with mock.patch.object(ve.subprocess, "Popen", side_effect=popen):
            with mock.patch.object(
                ve,
                "_kill_process_tree",
                side_effect=lambda proc: kills.append(proc),
            ):
                ve.html_to_pdf(
                    "<html><body>x</body></html>",
                    self.pdf,
                    "fake-chrome",
                    electron=electron,
                    job=self.job,
                )
        return kills

    def test_electron通道取消终止进程树并抛Cancelled(self):
        procs = []

        def popen(cmd, **kwargs):
            proc = FakeProc(pid=1001, on_wait=self.job.cancel)
            proc.cmd = cmd
            procs.append(proc)
            return proc

        kills = []
        with self.assertRaises(jobs.Cancelled):
            self._run(popen, electron="fake-electron", kills=kills)

        self.assertEqual(len(procs), 1)
        self.assertEqual(procs[0].cmd[0], "fake-electron")
        self.assertIn("--print-to-pdf", procs[0].cmd)
        self.assertEqual(kills, procs)  # 取消即终止 Electron 进程树
        self.assertFalse(self.pdf.exists())
        self.assertEqual(self.job.hooks, [])

    def test_chrome回退取消终止进程树并抛Cancelled(self):
        procs = []

        def popen(cmd, **kwargs):
            if cmd[0] == "fake-electron":
                proc = FakeProc(pid=2001, exit_code=1)  # Electron 失败 → 回退 Chrome
            else:
                proc = FakeProc(pid=2002, on_wait=self.job.cancel)
            proc.cmd = cmd
            procs.append(proc)
            return proc

        kills = []
        with self.assertRaises(jobs.Cancelled):
            self._run(popen, electron="fake-electron", kills=kills)

        self.assertEqual([p.cmd[0] for p in procs], ["fake-electron", "fake-chrome"])
        self.assertEqual(kills, [procs[1]])  # Chrome 回退进程树被终止
        self.assertFalse(self.pdf.exists())
        self.assertEqual(self.job.hooks, [])

    def test_打印成功后注销钩子不累积(self):
        procs = []
        calls = []

        def popen(cmd, **kwargs):
            def produce():
                self.pdf.write_bytes(b"%PDF-1.4\n")

            proc = FakeProc(pid=3000 + len(procs), produce=produce)
            proc.cmd = cmd
            procs.append(proc)
            calls.append(len(self.job.hooks))
            return proc

        for _ in range(3):
            self.pdf.unlink(missing_ok=True)
            self._run(popen)

        self.assertEqual(len(procs), 3)
        self.assertEqual(calls, [1, 1, 1])  # 每次打印只有本次的登记在途
        self.assertEqual(self.job.hooks, [])  # 完成后全部注销

    def test_打印超时回收进程树并保持TimeoutExpired口径(self):
        procs = []
        kills = []

        def popen(cmd, **kwargs):
            proc = FakeProc(pid=4001, timeout=True)
            procs.append(proc)
            return proc

        def kill(proc):
            kills.append(proc)

        with mock.patch.object(ve.subprocess, "Popen", side_effect=popen):
            with mock.patch.object(ve, "_kill_process_tree", side_effect=kill):
                with self.assertRaises(subprocess.TimeoutExpired):
                    ve._run_print(["fake-chrome"], 1, self.job)

        self.assertEqual(kills, procs)
        self.assertEqual(self.job.hooks, [])
        self.assertEqual(procs[0].communicate_calls, 2)  # 超时后再次 communicate 回收

    def test_真实Job注销钩子后取消不再触发(self):
        with tempfile.TemporaryDirectory() as d:
            env = mock.patch.dict(os.environ, {"LOCALAPPDATA": d, "XDG_DATA_HOME": d})
            env.start()
            try:
                job = jobs.Job("20260101000000-01", "test", "测试")
                calls = []
                hook = lambda: calls.append(1)  # noqa: E731
                job.add_cancel_hook(hook)
                job.remove_cancel_hook(hook)
                job.cancel()
                self.assertEqual(calls, [])
                self.assertEqual(job._cancel_hooks, [])
                job.remove_cancel_hook(hook)  # 重复注销静默忽略
                job._log_fp.close()
            finally:
                env.stop()


if __name__ == "__main__":
    unittest.main()
