# -*- coding: utf-8 -*-
"""运行任务期间阻止宿主机关屏/休眠（Windows / Linux，尽力而为）。

- Windows：SetThreadExecutionState 同时请求系统与显示器保持唤醒；
- Linux：systemd-inhibit 阻止 idle/sleep，并用 xdg-screensaver/xset 定时重置防黑屏。

设置失败只记录日志，不影响任务本身。必须由同一个线程 start/stop
（Windows 的线程执行状态是线程级）。
"""

import shutil
import subprocess
import sys
import threading


def _linux_die_with_parent() -> None:
    """让 systemd-inhibit 子进程随脚本进程退出，避免孤儿进程长期占锁。"""
    try:
        import ctypes
        import signal

        libc = ctypes.CDLL(None, use_errno=True)
        libc.prctl(1, signal.SIGTERM)  # PR_SET_PDEATHSIG
    except Exception:
        pass


class HostAwakeGuard(object):
    """运行期间尽力阻止宿主机关屏/休眠，结束后恢复；失败不阻断任务。"""

    _ES_CONTINUOUS = 0x80000000
    _ES_SYSTEM_REQUIRED = 0x00000001
    _ES_DISPLAY_REQUIRED = 0x00000002

    def __init__(self, log=None):
        self._log = log or (lambda _message: None)
        self._started = False
        self._windows_active = False
        self._inhibit_proc = None
        self._stop_event = None
        self._display_thread = None

    def start(self) -> None:
        if self._started:
            return
        self._started = True
        try:
            if sys.platform.startswith("win"):
                self._start_windows()
            elif sys.platform.startswith("linux"):
                self._start_linux()
            else:
                self._log("当前系统不支持自动防休眠（不影响任务）")
        except Exception as exc:
            self._log("防休眠启动失败（不影响任务）：%s" % str(exc)[:200])

    def _start_windows(self) -> None:
        import ctypes

        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.SetThreadExecutionState.argtypes = [ctypes.c_uint]
        kernel32.SetThreadExecutionState.restype = ctypes.c_uint
        flags = (
            self._ES_CONTINUOUS | self._ES_SYSTEM_REQUIRED | self._ES_DISPLAY_REQUIRED
        )
        if kernel32.SetThreadExecutionState(flags):
            self._windows_active = True
            self._log("已请求 Windows 保持唤醒（阻止休眠/关屏）")
        else:
            self._log(
                "Windows 防休眠请求失败（错误码 %d），不影响任务"
                % ctypes.get_last_error()
            )

    def _start_linux(self) -> None:
        stop_event = threading.Event()
        self._stop_event = stop_event
        inhibitor = shutil.which("systemd-inhibit")
        if inhibitor:
            self._inhibit_proc = subprocess.Popen(
                [
                    inhibitor,
                    "--what=idle:sleep",
                    "--who=Shade toolkit",
                    "--why=Task is running",
                    "--mode=block",
                    "sleep",
                    "2147483647",
                ],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                preexec_fn=_linux_die_with_parent,
            )
            self._log("已启动 Linux 防休眠（systemd-inhibit）")
        else:
            self._log("未找到 systemd-inhibit，Linux 防休眠可能不生效")
        reset = None
        for name, command in (
            ("xdg-screensaver", ["xdg-screensaver", "reset"]),
            ("xset", ["xset", "s", "reset"]),
        ):
            if shutil.which(name):
                reset = command
                break
        if reset is not None:
            self._display_thread = threading.Thread(
                target=self._linux_reset_display,
                args=(reset, stop_event),
                daemon=True,
                name="host-awake-display",
            )
            self._display_thread.start()
            self._log("已启动 Linux 防黑屏（%s 定时重置）" % reset[0])

    def _linux_reset_display(self, command, stop_event) -> None:
        while not stop_event.is_set():
            try:
                subprocess.run(
                    command,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    timeout=5,
                )
            except Exception:
                pass
            if stop_event.wait(30.0):
                return

    def stop(self) -> None:
        if not self._started:
            return
        self._started = False
        active = self._windows_active or self._inhibit_proc is not None
        if self._windows_active:
            self._windows_active = False
            try:
                import ctypes

                kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
                kernel32.SetThreadExecutionState.argtypes = [ctypes.c_uint]
                kernel32.SetThreadExecutionState.restype = ctypes.c_uint
                kernel32.SetThreadExecutionState(self._ES_CONTINUOUS)
            except Exception:
                pass
        event, self._stop_event = self._stop_event, None
        if event is not None:
            event.set()
        proc, self._inhibit_proc = self._inhibit_proc, None
        if proc is not None:
            active = True
            if proc.poll() is None:
                try:
                    proc.terminate()
                    proc.wait(timeout=3)
                except Exception:
                    try:
                        proc.kill()
                    except Exception:
                        pass
        thread, self._display_thread = self._display_thread, None
        if thread is not None:
            active = True
        if active:
            self._log("已结束防休眠/防黑屏，恢复系统默认设置")
