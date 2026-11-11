# -*- coding: utf-8 -*-
"""开发脚本测试：在客户端同款 Python 运行时里跑外部 .py，并把输出流回任务日志。

设计（2026-11 执行链路审查整改后）：
- 不把脚本 exec 在 RPC 进程里：子进程隔离崩溃/取消；冻结版用 uvmp-core 的
  `run-script` 子命令，源码版用当前 Python + toolkit/app.py；
- stdin 固定 `subprocess.DEVNULL`：RPC 的 stdin 是 stdio JSON-RPC 管道，脚本若继承
  会与协议抢读，可能把请求体读走或互相阻塞；脚本读 stdin 立即 EOF；
- stdout/stderr 合并后由独立读取线程按块（READ_CHUNK）读入队列，主线程只在
  “进程已退出且队列读完 / 超时 / 取消”三种条件下收敛；孙进程持有写端时主线程
  不等读取线程（不 join），只记一条警告后返回，任务不会挂死；
- 终止：先对进程组发优雅信号（POSIX SIGTERM；Windows CTRL_BREAK_EVENT 发给新建
  进程组，可覆盖孙进程），KILL_GRACE_SEC 内未退出升级强杀（POSIX SIGKILL；
  Windows taskkill /F /T 连子树）；取消钩子只投递终止线程，不在 RPC 线程里等进程
  退出；
- cwd 经 `--cwd=` 显式传给子进程，避免子进程入口按脚本目录 chdir 覆盖用户选择；
- 参数按平台切分（Windows 走 MSVCRT 规则）并由 dev_script_runner 统一脱敏，
  日志与 result 里的 password/secret/token 等值一律打码；
- 输出限额：单行 MAX_LINE_CHARS、单次任务总量 MAX_TOTAL_CHARS，超限截断并置
  result["output_truncated"]；
- 凭据提示：任务日志开头明确告知脚本以当前桌面用户权限运行、可读取本机配置与
  凭据文件；
- 开发者工具开关：app.json 的 dev 标记（默认 true）与配置 dev.allow_scripts
  （默认 true）都为真才允许运行——置 false 即整页拒绝执行，页面无需改动。
"""

import codecs
import json
import os
import queue
import re
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

import config as _config
import dev_script_runner as _runner
from jobs import Cancelled

APP_ID = "dev_script"
TOOLKIT_DIR = Path(__file__).resolve().parents[2]
APP_META_PATH = Path(__file__).with_name("app.json")

READ_CHUNK = 4096  # 单次从管道读取的字节数
MAX_LINE_CHARS = 4000  # 单行保留字符数（超出截断并标注原长）
MAX_TOTAL_CHARS = 4 * 1024 * 1024  # 单次任务记录的输出总量上限（字符）
POLL_SEC = 0.1  # 主线程收敛轮询间隔
EXIT_DRAIN_SEC = 1.0  # 进程退出后继续收尾输出的宽限
KILL_GRACE_SEC = 3.0  # SIGTERM 后等待升级 SIGKILL 的宽限

_EOF = object()
_NO_ITEM = object()
# 一个换行序列 = 任意个 CR + 至多一个 LF。Windows 的文本模式会先把脚本写的 "\n"
# 转成 "\r\n"，脚本显式写 "\r\n" 时管道里就是 "\r\r\n"；若把 CR 当独立换行，
# 每行后面都会多出一条空日志行。因此连续 CR（含其后可选 LF）整体算一次换行。
_EOL = re.compile(r"\r+\n?|\n")


def _split_args(raw, windows=None) -> list:
    """页面参数切分（与子进程入口共用一份实现，Windows 保真）。"""
    return _runner.split_args(raw, windows=windows)


def _command(path: str, args: list, cwd: str) -> list:
    path = str(Path(path).expanduser().resolve())
    if getattr(sys, "frozen", False):
        base = [sys.executable, "run-script"]
    else:
        base = [sys.executable, str(TOOLKIT_DIR / "app.py"), "run-script"]
    # --cwd 必须在脚本路径之前：argparse 的 REMAINDER 会把路径之后的选项都当成脚本参数
    return base + ["--cwd=" + cwd, path, "--"] + list(args)


def _popen_kwargs() -> dict:
    kwargs = {
        "stdin": subprocess.DEVNULL,  # 不继承 RPC 的 stdio 管道
        "stdout": subprocess.PIPE,
        "stderr": subprocess.STDOUT,
        "bufsize": 0,  # 原始管道：read 立即返回已到达的字节，不必凑满 READ_CHUNK
    }
    if os.name == "nt":
        kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        kwargs["start_new_session"] = True
    return kwargs


def _scripts_allowed() -> bool:
    """开发者工具开关：app.json 的 dev 标记与配置 dev.allow_scripts 都必须为真。"""
    try:
        meta = json.loads(APP_META_PATH.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        meta = {}
    if not (meta or {}).get("dev", True):
        return False
    try:
        dev = (_config.load() or {}).get("dev") or {}
    except Exception:  # noqa: BLE001
        return True
    return bool(dev.get("allow_scripts", True))


class _LineSplitter:
    """按换行切行；单行超过 limit 仍无换行时先吐一段并丢弃中间内容，
    避免无换行的大块输出把内存撑爆。

    跨读取块的换行也要处理：块尾的 CR 可能是下一块 LF 的前半（CRLF 被 READ_CHUNK
    切开），此时先扣住不切，等下一块到齐再判定，否则会多出一条空日志行。
    """

    def __init__(self, limit: int = None):
        self._limit = MAX_LINE_CHARS * 2 if limit is None else limit
        self._buf = ""
        self._dropping = False

    def feed(self, text: str, hold_cr: bool = True) -> list:
        out = []
        buf = self._buf + text
        pending = ""
        if hold_cr and buf.endswith("\r"):
            cut = len(buf.rstrip("\r"))
            pending, buf = buf[cut:], buf[:cut]
        while True:
            if self._dropping:
                match = _EOL.search(buf)
                if match is None:
                    buf = ""  # 超长行剩余部分直接丢弃
                    break
                out.append("…（单行过长，中间部分已丢弃）")
                self._dropping = False
                buf = buf[match.end() :]
                continue
            match = _EOL.search(buf)
            if match is None:
                break
            out.append(buf[: match.start()])
            buf = buf[match.end() :]
        if not self._dropping and len(buf) > self._limit:
            out.append(buf[: self._limit])
            self._dropping = True
            buf = ""
        self._buf = buf + pending
        return out

    def finish(self, text: str = "") -> list:
        # 到 EOF：块尾扣住的 CR 不会再等来 LF，按普通换行处理
        out = self.feed(text, hold_cr=False)
        if self._dropping:
            out.append("…（单行过长，中间部分已丢弃）")
            self._dropping = False
        while self._buf.endswith(("\r", "\n")):
            self._buf = self._buf[:-1]
        if self._buf:
            out.append(self._buf)
            self._buf = ""
        return out


class _Reader(threading.Thread):
    """读子进程输出并入队。孙进程持写端时本线程会一直阻塞在 read 上，
    因此调用方只能“放弃等待”，不得 join。"""

    def __init__(self, stream, sink: queue.Queue, splitter: _LineSplitter):
        super().__init__(name="dev-script-reader", daemon=True)
        self._stream = stream
        self._sink = sink
        self._splitter = splitter
        # 不能叫 _stop：threading.Thread 内部有同名方法，is_alive/_wait_for_tstate_lock
        # 会调用它（Python ≤3.12），被 Event 覆盖即抛 TypeError。
        self._halt = threading.Event()

    def run(self):
        decoder = codecs.getincrementaldecoder("utf-8")("replace")
        try:
            while not self._halt.is_set():
                chunk = self._stream.read(READ_CHUNK)
                if not chunk:
                    break
                for line in self._splitter.feed(decoder.decode(chunk)):
                    self._sink.put(line)
        except Exception:  # noqa: BLE001
            pass
        finally:
            try:
                tail = decoder.decode(b"", True)
            except Exception:  # noqa: BLE001
                tail = ""
            try:
                for line in self._splitter.finish(tail):
                    self._sink.put(line)
            except Exception:  # noqa: BLE001
                pass
            self._sink.put(_EOF)
            try:
                self._stream.close()  # 谁读谁关：主线程不关正在被读的 fd
            except Exception:  # noqa: BLE001
                pass

    def stop(self):
        self._halt.set()


class _OutputBudget:
    """单行/总量限额：超单行截断并标注原长；超总量只提示一次后不再记录。"""

    def __init__(self, line: int = None, total: int = None):
        self.line = MAX_LINE_CHARS if line is None else line
        self.total = MAX_TOTAL_CHARS if total is None else total
        self.used = 0
        self.truncated = False
        self.stopped = False

    def offer(self, job, text: str):
        if self.stopped:
            return  # 超限提示已发出：后续输出一律不记，提示与实际一致
        text = str(text).rstrip("\r\n")
        size = len(text)
        if size > self.line:
            text = text[: self.line] + "…（本行已截断，原 %d 字符）" % size
            self.truncated = True
        if self.used + len(text) > self.total:
            self.truncated = True
            self.stopped = True
            job.log("输出超过上限 %d 字符，后续输出不再记录（脚本仍在运行）" % self.total)
            return
        self.used += len(text)
        job.log(text)


def _signal_tree(proc: subprocess.Popen, hard: bool):
    """向整个进程组发信号：脚本可能再 spawn 子进程，只杀直接子进程会留孤儿。

    Windows 没有 POSIX 进程组信号：优雅阶段用 CTRL_BREAK_EVENT 发给新建的进程组
    （`os.kill` 在 Windows 上按进程组投递该事件），它不会像 terminate 那样只杀直接
    子进程；升级阶段用 `taskkill /F /T` 连子树一起强杀，taskkill 本身失败（无权限 /
    PID 已失效）时再退回 `proc.kill()`，不能静默放过。
    """
    if proc is None or proc.poll() is not None:
        return
    if os.name == "nt":
        if hard:
            killed = False
            try:
                killed = (
                    subprocess.run(
                        ["taskkill", "/F", "/T", "/PID", str(proc.pid)],
                        stdin=subprocess.DEVNULL,
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                        timeout=5,
                    ).returncode
                    == 0
                )
            except Exception:  # noqa: BLE001
                killed = False
            if not killed:
                try:
                    proc.kill()
                except Exception:  # noqa: BLE001
                    pass
            return
        try:
            os.kill(proc.pid, signal.CTRL_BREAK_EVENT)
        except Exception:  # noqa: BLE001
            try:
                proc.terminate()
            except Exception:  # noqa: BLE001
                pass
        return
    sig = signal.SIGKILL if hard else signal.SIGTERM
    try:
        os.killpg(os.getpgid(proc.pid), sig)
    except OSError:
        try:
            proc.send_signal(sig)
        except Exception:  # noqa: BLE001
            pass


class _Terminator:
    """限时终止：先优雅（SIGTERM / CTRL_BREAK）→ 宽限 → 强杀（SIGKILL / taskkill /F /T）；
    幂等，且可在取消钩子里异步启动。

    强杀之后必须确认进程真的退出：Windows 上 taskkill /T 可能因权限或时序返回非零，
    此时若直接放行，调用方会拿着“仍在运行的子进程”继续收尾。所以再加一轮
    `proc.kill()`（对直接子进程 TerminateProcess）兜底并等它收敛。

    取消钩子由 RPC 线程执行，不能在里面等进程退出（最长 3×宽限），所以 start()
    只起线程；主线程收敛时用 wait() 等同一次终止完成。
    """

    def __init__(self, proc, grace: float = None):
        self._proc = proc
        self._grace = KILL_GRACE_SEC if grace is None else grace
        self._lock = threading.Lock()
        self._done = threading.Event()
        self._thread = None

    def start(self):
        with self._lock:
            if self._thread is None:
                self._thread = threading.Thread(
                    target=self._run, name="dev-script-kill", daemon=True
                )
                self._thread.start()

    def wait(self, timeout: float = None):
        if self._proc is None:
            return
        self.start()
        self._done.wait(self._grace * 3 + 2 if timeout is None else timeout)

    def _run(self):
        try:
            if self._proc.poll() is None:
                _signal_tree(self._proc, hard=False)
                if not self._wait_exit(self._grace):
                    _signal_tree(self._proc, hard=True)
                    if not self._wait_exit(self._grace):
                        try:
                            self._proc.kill()
                        except Exception:  # noqa: BLE001
                            pass
                        self._wait_exit(self._grace)
        finally:
            self._done.set()

    def _wait_exit(self, timeout: float) -> bool:
        try:
            self._proc.wait(timeout=timeout)
            return True
        except subprocess.TimeoutExpired:
            return False


def _parse_timeout(value) -> int:
    try:
        timeout = int(value) if value not in (None, "") else 0
    except (TypeError, ValueError):
        return 0
    return timeout if timeout > 0 else 0


def run(job, params: dict) -> dict:
    params = params or {}
    if not _scripts_allowed():
        raise RuntimeError(
            "脚本测试已关闭：开发者工具开关（app.json 的 dev 标记 / 配置 dev.allow_scripts）"
            "为关闭状态"
        )
    path = str(params.get("path") or "").strip()
    if not path:
        raise RuntimeError("请选择要运行的 .py 脚本")
    p = Path(path).expanduser()
    if not p.is_file():
        raise RuntimeError("脚本不存在: %s" % p)
    if p.suffix.lower() != ".py":
        raise RuntimeError("只支持 .py 文件: %s" % p)
    p = p.resolve()
    cwd = str(params.get("cwd") or "").strip()
    cwd_path = Path(cwd).expanduser().resolve() if cwd else p.parent
    if cwd and not cwd_path.is_dir():
        raise RuntimeError("工作目录不存在: %s" % cwd)
    args = _split_args(params.get("args"))
    timeout = _parse_timeout(params.get("timeout"))

    env = os.environ.copy()
    env.setdefault("UVMP_TOOLKIT_CONFIG", str(_config.config_path()))
    env["PYTHONUNBUFFERED"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"
    env["UVMP_DEV_SCRIPT"] = "1"

    job.log("执行脚本: %s" % p)
    job.log("解释器: %s" % sys.executable)
    job.log("工作目录: %s" % cwd_path)
    if args:
        job.log("参数: %s" % " ".join(_runner.mask_args(args)))
    job.log("提示：脚本以当前桌面用户权限运行，可读取本机配置与凭据文件，请只运行可信脚本。")
    if timeout:
        job.log("超时上限: %d 秒" % timeout)
    job.set_progress(0, 1, "运行脚本")
    started = time.time()
    try:
        proc = subprocess.Popen(
            _command(str(p), args, str(cwd_path)),
            cwd=str(cwd_path),
            env=env,
            **_popen_kwargs(),
        )
    except OSError as e:
        raise RuntimeError("启动脚本进程失败: %s" % e)

    sink = queue.Queue()
    reader = _Reader(proc.stdout, sink, _LineSplitter())
    reader.start()
    terminator = _Terminator(proc)
    job.add_cancel_hook(terminator.start)

    budget = _OutputBudget()
    timed_out = False
    drained = False
    deadline = started + timeout if timeout else None
    exit_deadline = None
    pipe_held = False
    try:
        while True:
            if job.cancelled:
                terminator.wait()
                raise Cancelled()
            if deadline is not None and time.time() >= deadline:
                timed_out = True
                terminator.wait()
                break
            try:
                item = sink.get(timeout=POLL_SEC)
            except queue.Empty:
                item = _NO_ITEM
            if item is _EOF:
                drained = True
            elif item is not _NO_ITEM:
                budget.offer(job, item)
            if proc.poll() is not None:
                if drained:
                    break
                if exit_deadline is None:
                    exit_deadline = time.time() + EXIT_DRAIN_SEC
                elif time.time() >= exit_deadline:
                    pipe_held = True
                    break
        # 收尾：把队列里已读到的行补齐（超时/退出后都可能还有残余）
        while True:
            try:
                item = sink.get_nowait()
            except queue.Empty:
                break
            if item is _EOF:
                drained = True
            else:
                budget.offer(job, item)
        if pipe_held and not drained:
            job.log(
                "警告：脚本已退出，但其子进程仍占用输出管道，已停止读取输出"
                "（子进程可能仍在运行，如需清理请手动结束）"
            )
    finally:
        reader.stop()
        if proc.poll() is None:
            terminator.wait()
        if not reader.is_alive():
            # 读取线程仍存活说明它是“孙进程持管道”放弃等待的一方，由该线程读到 EOF 后
            # 自己关闭；从主线程关一个正在被读的 fd 会有竞态，因此这里只关已收工的。
            try:
                proc.stdout and proc.stdout.close()
            except Exception:  # noqa: BLE001
                pass

    duration = int((time.time() - started) * 1000)
    job.set_progress(1, 1, "完成")
    if job.cancelled:
        raise Cancelled()
    if timed_out:
        raise RuntimeError("脚本运行超时（%d 秒）" % timeout)
    code = proc.returncode
    if code != 0:
        raise RuntimeError("脚本退出码 %d" % code)
    return {
        "path": str(p),
        "args": _runner.mask_args(args),
        "exit_code": code,
        "duration_ms": duration,
        "cwd": str(cwd_path),
        "output_truncated": budget.truncated,
    }
