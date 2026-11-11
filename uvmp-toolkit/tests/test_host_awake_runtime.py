# -*- coding: utf-8 -*-
"""防休眠组件在串行/多线程进程中的安全性测试。

覆盖审查问题：任务在线程里执行，Linux 分支若用 preexec_fn 会 fork 多线程进程
（已知死锁/崩溃风险）；必须改用 start_new_session，并在停止时回收进程组。

两个用真实 /bin/sh + kill -0 + SIGKILL 验证监督脚本的用例是 Linux/POSIX 专属：
Windows 没有 sh、没有 SIGKILL，也没有 /proc 僵尸态，必须在 win32 上 skip，
不能靠 mock 假装跑过（mock 过的 Popen 不会真的跑脚本，等于没验证）。
"""

import os
import shutil
import signal
import subprocess
import sys
import time
import unittest
from unittest import mock

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import host_awake  # noqa: E402

# 监督脚本只在 POSIX sh 下存在：Windows 既无 sh，也无 SIGKILL 与 /proc
SH_PATH = shutil.which("sh") if os.name == "posix" else None
POSIX_SH = SH_PATH is not None


class LinuxInhibitTest(unittest.TestCase):
    def setUp(self):
        self.logs = []
        self.guard = host_awake.HostAwakeGuard(self.logs.append)

    def _fake_which(self, name):
        if name in ("systemd-inhibit", "xdg-screensaver", "sh"):
            return "/usr/bin/" + name
        return None

    def test_不使用preexec_fn且独立会话(self):
        proc = mock.Mock(pid=999)
        proc.poll.return_value = None
        with mock.patch.object(sys, "platform", "linux"), mock.patch.object(
            host_awake.shutil, "which", side_effect=self._fake_which
        ), mock.patch.object(
            host_awake.subprocess, "Popen", return_value=proc
        ) as popen:
            self.guard._start_linux()

        _args, kwargs = popen.call_args_list[0]
        self.assertIn("systemd-inhibit", _args[0][0])
        self.assertNotIn("preexec_fn", kwargs, "多线程进程禁止 preexec_fn")
        self.assertTrue(kwargs.get("start_new_session"))

    def test_停止时按进程组回收(self):
        proc = mock.Mock(pid=999)
        proc.poll.return_value = None
        self.guard._inhibit_proc = proc
        self.guard._started = True
        with mock.patch.object(sys, "platform", "linux"), mock.patch.object(
            host_awake, "_terminate_process_tree"
        ) as terminator:
            self.guard.stop()
        terminator.assert_called_once_with(proc)


class LinuxInhibitWatchdogTest(unittest.TestCase):
    """核心进程被 SIGKILL 后不会再调用 stop()：持锁子进程必须能自己发现父进程
    消失并退出，否则 systemd-inhibit 锁会一直挂到关机。"""

    def _fake_which(self, name):
        return "/usr/bin/" + name if name in ("systemd-inhibit", "sh") else None

    def test_启动命令用数值PID监督而不是无限sleep(self):
        guard = host_awake.HostAwakeGuard()
        proc = mock.Mock(pid=999)
        proc.poll.return_value = None
        with mock.patch.object(sys, "platform", "linux"), mock.patch.object(
            host_awake.shutil, "which", side_effect=self._fake_which
        ), mock.patch.object(host_awake.subprocess, "Popen", return_value=proc) as popen:
            guard._start_linux()
        _args, _kwargs = popen.call_args_list[0]
        cmd = [str(part) for part in _args[0]]
        joined = " ".join(cmd)
        self.assertNotIn("2147483647", joined, "不能再靠永久 sleep 持锁")
        self.assertIn("kill -0", joined, "监督脚本要按 PID 判断父进程是否还在")
        self.assertIn("/proc/", joined, "核心被强杀后留下的僵尸进程也要能被识别")
        self.assertIn(str(os.getpid()), cmd, "监督脚本要拿到核心进程的真实 PID")
        guard.stop()

    @unittest.skipUnless(POSIX_SH, "监督脚本依赖 POSIX sh 与 /proc，Windows 不适用")
    def test_监督脚本在父进程消失后自行退出(self):
        victim = subprocess.Popen(
            [sys.executable, "-c", "import time; time.sleep(60)"]
        )
        self.addCleanup(lambda: victim.poll() is None and victim.kill())
        script = host_awake._inhibit_watch_script(0.1)
        watcher = subprocess.Popen(
            [SH_PATH, "-c", script, "uvmp-awake-watch", str(victim.pid)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            self.assertIsNone(watcher.poll(), "父进程还在时不能退出（否则防休眠失效）")
            victim.kill()
            victim.wait(timeout=10)
            self.assertEqual(
                0, watcher.wait(timeout=15), "父进程消失后监督进程必须自行退出"
            )
        finally:
            if watcher.poll() is None:
                watcher.kill()
                watcher.wait(timeout=10)

    @unittest.skipUnless(POSIX_SH, "僵尸态检测依赖 POSIX sh 与 /proc，Windows 不适用")
    def test_核心进程变僵尸时监督脚本退出(self):
        """核心被 SIGKILL 但父进程尚未 wait 时 PID 仍存在（kill -0 成功）：
        只按 PID 探活会让锁继续挂着，监督脚本必须识别僵尸态。"""
        if not Path("/proc/self/stat").exists():
            self.skipTest("当前系统无 /proc，僵尸态检测不适用")
        parent = subprocess.Popen(
            [
                sys.executable,
                "-c",
                "import subprocess, sys, time\n"
                "child = subprocess.Popen(\n"
                "    [sys.executable, '-c', 'import time; time.sleep(60)'],\n"
                "    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)\n"
                "print(child.pid, flush=True)\n"
                "time.sleep(60)\n",
            ],
            stdout=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(lambda: parent.poll() is None and parent.kill())
        try:
            child_pid = int(parent.stdout.readline().strip())
        except ValueError:  # pragma: no cover - 起子进程失败的兜底
            self.skipTest("无法取得子进程 PID")

        def _kill_child():
            try:
                os.kill(child_pid, signal.SIGKILL)
            except OSError:
                pass

        def _stop_parent():
            if parent.poll() is None:
                parent.kill()
            try:
                parent.wait(timeout=10)
            except Exception:  # noqa: BLE001
                pass

        self.addCleanup(_kill_child)
        self.addCleanup(_stop_parent)
        self.addCleanup(lambda: parent.stdout.close())
        script = host_awake._inhibit_watch_script(0.1)
        watcher = subprocess.Popen(
            [SH_PATH, "-c", script, "uvmp-awake-watch", str(child_pid)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            self.assertIsNone(watcher.poll(), "子进程还活着时监督进程不能退出")
            os.kill(child_pid, signal.SIGKILL)
            time.sleep(0.2)  # 让子进程进入僵尸态（父进程故意不 wait）
            self.assertEqual(
                0,
                watcher.wait(timeout=15),
                "核心被强杀变成僵尸后监督进程必须自行退出并释放锁",
            )
        finally:
            if watcher.poll() is None:
                watcher.kill()
                watcher.wait(timeout=10)


if __name__ == "__main__":
    unittest.main()
