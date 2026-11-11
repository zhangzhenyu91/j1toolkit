# -*- coding: utf-8 -*-
"""运行任务期间阻止宿主机关屏/休眠（Windows / Linux，尽力而为）。

- Windows：SetThreadExecutionState 同时请求系统与显示器保持唤醒；
- Linux：systemd-inhibit 阻止 idle/sleep，并用 xdg-screensaver/xset 定时重置防黑屏。
- 同时每 30 秒产生一次真实鼠标微动（Windows mouse_event / Linux xdotool 或 XTest），
  防止系统把长时间无操作判为空闲。

设置失败只记录日志，不影响任务本身。必须由同一个线程 start/stop
（Windows 的线程执行状态是线程级）。

子进程回收注意：本模块由任务线程启动（进程内还有 RPC 线程、job 线程、日志线程），
因此**禁止使用 preexec_fn**——它在多线程进程里 fork 后只保留调用线程，CPython 已知
会在 malloc/import 锁上死锁；改用 start_new_session 独立进程组，停止时按组回收。

持锁进程存活约束：systemd-inhibit 的锁只在它启动的命令存活期间有效。核心若被
SIGKILL（用户强杀/系统 OOM），stop() 再也不会执行，若底下挂的是 `sleep 2147483647`，
锁会一直挂到重启。改用极小的监督脚本按数值 PID 探测核心进程，核心消失（含变成
僵尸、父进程尚未 wait）即退出释放锁；不能靠 PPID（sh 的父进程始终是 systemd-inhibit，
与其是否被收养无关）。
"""

import os
import shutil
import signal
import subprocess
import sys
import threading

# 监督脚本探测核心进程的间隔（秒）：决定了核心被强杀后锁最多多挂多久
INHIBIT_WATCH_INTERVAL_SEC = 5.0


def _inhibit_watch_script(interval: float = INHIBIT_WATCH_INTERVAL_SEC) -> str:
    """systemd-inhibit 底下跑的监督脚本（POSIX sh）。

    参数 $1 = 核心进程 PID，按 PID 探活；核心进程消失即成僵尸也退出，让
    systemd-inhibit 释放 idle/sleep 锁。只用 sh 内建与基础命令，不依赖 xdotool 等。
    僵尸判断读 /proc/<pid>/stat（Linux 独有，systemd-inhibit 也只在 Linux 存在）；
    读不到 /proc（无权限/非 Linux）时退化为纯 kill -0 探活。
    """
    return (
        'pid="$1"\n'
        'while kill -0 "$pid" 2>/dev/null; do\n'
        '  line=""\n'
        '  { IFS= read -r line; } 2>/dev/null < "/proc/$pid/stat" || true\n'
        '  rest="${line##*) }"\n'
        '  case "$rest" in\n'
        '    Z\\ *) exit 0 ;;\n'
        "  esac\n"
        "  sleep %s\n"
        "done\n"
    ) % ("%g" % float(interval),)


def _terminate_process_tree(proc) -> None:
    """结束子进程及其派生进程（systemd-inhibit 在部分发行版会再 fork）"""
    try:
        if proc.poll() is not None:
            return
    except Exception:  # noqa: BLE001
        pass
    if os.name != "nt":
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        except Exception:  # noqa: BLE001
            pass
    try:
        proc.wait(timeout=3)
        return
    except Exception:  # noqa: BLE001
        pass
    if os.name != "nt":
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
            return
        except Exception:  # noqa: BLE001
            pass
    try:
        proc.kill()
    except Exception:  # noqa: BLE001
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
        self._jiggle_stop = None
        self._jiggle_thread = None

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
            self._start_mouse_jiggle()
        except Exception as exc:
            self._log("防休眠启动失败（不影响任务）：%s" % str(exc)[:200])

    def _start_mouse_jiggle(self) -> None:
        if not (sys.platform.startswith("win") or sys.platform.startswith("linux")):
            return
        stop = threading.Event()
        self._jiggle_stop = stop
        thread = threading.Thread(
            target=self._mouse_jiggle_loop,
            args=(stop,),
            daemon=True,
            name="host-awake-mouse",
        )
        self._jiggle_thread = thread
        thread.start()
        self._log("已启动鼠标微动（每 30 秒），防止无操作挂起")

    def _mouse_jiggle_loop(self, stop_event) -> None:
        warned = False
        while not stop_event.is_set():
            if stop_event.wait(30.0):
                return
            try:
                if sys.platform.startswith("win"):
                    import ctypes

                    user32 = ctypes.WinDLL("user32", use_last_error=True)
                    user32.mouse_event.argtypes = [
                        ctypes.c_uint,
                        ctypes.c_uint,
                        ctypes.c_uint,
                        ctypes.c_uint,
                        ctypes.c_size_t,
                    ]
                    # 相对移动 1px 再移回，产生真实鼠标输入事件，重置系统空闲计时。
                    user32.mouse_event(0x0001, 1, 0, 0, 0)
                    user32.mouse_event(0x0001, -1, 0, 0, 0)
                else:
                    xdotool = shutil.which("xdotool")
                    if xdotool:
                        subprocess.run(
                            [xdotool, "mousemove_relative", "--", "1", "0"],
                            stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL,
                            timeout=2,
                        )
                        subprocess.run(
                            [xdotool, "mousemove_relative", "--", "-1", "0"],
                            stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL,
                            timeout=2,
                        )
                    elif not self._linux_xtest_jiggle():
                        if not warned:
                            self._log(
                                "未找到 xdotool/libXtst，跳过系统鼠标微动"
                            )
                            warned = True
            except Exception:
                if not warned:
                    self._log("系统鼠标微动失败（不影响任务）")
                    warned = True

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
        # 监督脚本靠 POSIX sh 解释：没有 sh 就不启动 inhibit（也不要写死 /bin/sh 去撞
        # FileNotFoundError，否则会连带跳过下面的防黑屏与鼠标微动）
        shell = shutil.which("sh") if inhibitor else None
        if inhibitor and shell:
            # start_new_session 代替 preexec_fn：多线程进程里 preexec_fn 会死锁；
            # 独立会话/进程组也便于 stop() 时整组回收。
            # 命令不是无限 sleep，而是按数值 PID 监督核心进程的极小脚本：
            # 核心被 SIGKILL 后脚本自行退出，systemd-inhibit 随之释放 idle/sleep 锁。
            self._inhibit_proc = subprocess.Popen(
                [
                    inhibitor,
                    "--what=idle:sleep",
                    "--who=Shade toolkit",
                    "--why=Task is running",
                    "--mode=block",
                    shell,
                    "-c",
                    _inhibit_watch_script(),
                    "uvmp-awake-watch",
                    str(os.getpid()),
                ],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
            self._log("已启动 Linux 防休眠（systemd-inhibit）")
        elif inhibitor:
            self._log("未找到 sh，无法启动防休眠监督脚本（不影响任务）")
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

    @staticmethod
    def _linux_xtest_jiggle() -> bool:
        """无 xdotool 时用 XTest 产生真实相对鼠标输入（X11/XWayland）。"""
        try:
            import ctypes

            x11 = ctypes.CDLL("libX11.so.6")
            xtst = ctypes.CDLL("libXtst.so.6")
            x11.XOpenDisplay.restype = ctypes.c_void_p
            display = x11.XOpenDisplay(None)
            if not display:
                return False
            xtst.XTestFakeRelativeMotionEvent.argtypes = [
                ctypes.c_void_p,
                ctypes.c_int,
                ctypes.c_int,
                ctypes.c_ulong,
            ]
            x11.XFlush.argtypes = [ctypes.c_void_p]
            x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
            try:
                xtst.XTestFakeRelativeMotionEvent(display, 1, 0, 0)
                xtst.XTestFakeRelativeMotionEvent(display, -1, 0, 0)
                x11.XFlush(display)
            finally:
                x11.XCloseDisplay(display)
            return True
        except Exception:
            return False

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
            _terminate_process_tree(proc)
        jiggle_stop, self._jiggle_stop = self._jiggle_stop, None
        if jiggle_stop is not None:
            jiggle_stop.set()
        self._jiggle_thread = None
        thread, self._display_thread = self._display_thread, None
        if thread is not None:
            active = True
        if active:
            self._log("已结束防休眠/防黑屏，恢复系统默认设置")
