# -*- coding: utf-8 -*-
"""dev_script 执行链路回归测试（真实假脚本 + 真实子进程，零网络、零真实密钥）。

覆盖 2026-11 审查项：
- stdin 隔离：子进程 stdin 固定 DEVNULL，不继承 RPC 的 stdio 管道；脚本读 stdin 立即 EOF
- 输出收敛：读取线程 + 队列，主线程只在“进程已退出且读完 / 超时 / 取消”收敛；
  孙进程持有输出管道时任务不挂死
- 终止：SIGTERM 限时升级 SIGKILL；取消钩子不让调用线程等进程死
- cwd 经 --cwd 显式传给子进程，不被“默认脚本目录”覆盖
- 参数：Windows 命令行切分保留反斜杠/引号；日志与 result 中 password/secret/token 打码
- 输出限额：单行截断、总量截断一次提示并置 output_truncated
- 开发开关：app.json 的 dev 标记与配置 dev.allow_scripts 任一为假即拒绝运行
- Windows 实机差异：CRLF/重复 CR 不拆空行、存活探测不走 os.kill（否则探测即杀人）、
  taskkill 失败退回强杀、8.3 短名与长名视为同一目录
"""

import importlib.util
import json
import os
import signal
import subprocess
import sys
import tempfile
import textwrap
import threading
import time
import types
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import dev_script_runner  # noqa: E402
import jobs  # noqa: E402

BACKEND_PATH = ROOT / "toolkit" / "apps" / "dev_script" / "backend.py"
APP_ENTRY = ROOT / "toolkit" / "app.py"

IS_WINDOWS = os.name == "nt"


def _load_backend():
    spec = importlib.util.spec_from_file_location(
        "dev_script_backend_under_test", str(BACKEND_PATH)
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


backend = _load_backend()


class FakeJob:
    """与 jobs.Job 同接口的最小替身：只记录日志/进度/取消钩子，不落盘。"""

    def __init__(self, on_line=None):
        self.lines = []
        self.progress = []
        self._cancel = threading.Event()
        self._hooks = []
        self._lock = threading.Lock()
        self._on_line = on_line

    def log(self, msg):
        text = str(msg)
        with self._lock:
            self.lines.append(text)
        if self._on_line:
            self._on_line(text)

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    @property
    def cancelled(self):
        return self._cancel.is_set()

    def cancel(self):
        with self._lock:
            if self._cancel.is_set():
                return
            self._cancel.set()
            hooks = list(self._hooks)
            self._hooks.clear()
        for fn in hooks:
            fn()

    def add_cancel_hook(self, fn):
        with self._lock:
            if not self._cancel.is_set():
                self._hooks.append(fn)
                return
        fn()

    def text(self):
        with self._lock:
            return "\n".join(self.lines)


def pid_alive(pid):
    """进程存活探测。

    Windows 上不能直接用 `os.kill(pid, 0)`：CPython 把它当成 TerminateProcess 的
    退出码，探测本身就把目标杀掉（并且返回值也不代表第三方进程还在不在）。改用
    OpenProcess + GetExitCodeProcess 查询。
    """
    pid = int(pid)
    if pid <= 0:
        return False
    if IS_WINDOWS:
        return _win_pid_alive(pid)
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _win_pid_alive(pid):
    import ctypes

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    handle = kernel32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
    if not handle:
        return False
    try:
        code = ctypes.c_ulong()
        if not kernel32.GetExitCodeProcess(handle, ctypes.byref(code)):
            return False
        return code.value == 259  # STILL_ACTIVE
    finally:
        kernel32.CloseHandle(handle)


def kill_pid(pid, timeout=10.0):
    """强杀一个进程（Windows 连子树）并等它真正退出。

    用例收尾必须等进程消失：Windows 上退出中的进程仍持有工作目录句柄，紧接着删临时
    目录会报 WinError 32/5。
    """
    pid = int(pid)
    if pid <= 0:
        return
    if IS_WINDOWS:
        try:
            subprocess.run(
                ["taskkill", "/F", "/T", "/PID", str(pid)],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=timeout,
            )
        except Exception:  # noqa: BLE001
            pass
    else:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    deadline = time.time() + timeout
    while time.time() < deadline and pid_alive(pid):
        time.sleep(0.05)


def same_path(left, right):
    """两个路径是否指向同一位置。

    Windows 上同一目录有长名与 8.3 短名两种写法（`runneradmin` / `RUNNER~1`），
    逐字符比较会误判；按 realpath + normcase 归一后比较。
    """
    return os.path.normcase(os.path.realpath(str(left))) == os.path.normcase(
        os.path.realpath(str(right))
    )


class DevScriptTestBase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.scripts = self.tmp / "scripts"
        self.work = self.tmp / "work"
        self.scripts.mkdir()
        self.work.mkdir()
        self.pidfile = self.tmp / "child.pid"
        cfg = self.tmp / "config.json"
        cfg.write_text("{}", encoding="utf-8")
        env = mock.patch.dict(
            os.environ,
            {
                "XDG_DATA_HOME": str(self.tmp / "data"),
                "XDG_CONFIG_HOME": str(self.tmp / "cfg"),
                "LOCALAPPDATA": str(self.tmp / "data"),
                "UVMP_TOOLKIT_CONFIG": str(cfg),
                "DEV_SCRIPT_PIDFILE": str(self.pidfile),
            },
        )
        env.start()
        self.addCleanup(env.stop)
        # 缩短终止/收尾宽限，保证回归用例秒级收敛（生产用模块默认值）
        for name, value in (
            ("KILL_GRACE_SEC", 0.3),
            ("EXIT_DRAIN_SEC", 0.5),
            ("POLL_SEC", 0.05),
        ):
            patcher = mock.patch.object(backend, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self._children = []

    def tearDown(self):
        for pid in self._children:
            kill_pid(pid)
        self._children.clear()

    def script(self, name, body, work=None):
        path = (work or self.scripts) / name
        path.write_text(textwrap.dedent(body), encoding="utf-8")
        return path

    def run_backend(self, params, on_line=None):
        job = FakeJob(on_line=on_line)
        result = backend.run(job, params)
        return job, result

    def killed_pid(self, timeout=15.0):
        """等脚本把 pid 写进文件后返回。

        脚本可能刚 open 完文件就被终止信号打断（Windows 的 CTRL_BREAK 映射成
        KeyboardInterrupt，`SIG_IGN` 只管 SIGTERM），所以按“内容像 pid”轮询，
        而不是读到什么算什么。
        """
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                text = self.pidfile.read_text(encoding="utf-8").strip()
            except OSError:
                text = ""
            if text.isdigit():
                pid = int(text)
                self._children.append(pid)
                return pid
            time.sleep(0.02)
        self.fail("脚本未写出 pid 文件（或内容不是 pid）: %s" % self.pidfile)

    def assert_pid_gone(self, pid, msg="进程仍然存活", timeout=5.0):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if not pid_alive(pid):
                return
            time.sleep(0.05)
        self.fail("%s: pid=%s" % (msg, pid))

    def run_app(self, *argv, timeout=60):
        cmd = [sys.executable, str(APP_ENTRY), "run-script"] + list(argv)
        return subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=timeout,
            env=os.environ.copy(),
        )


class ArgSplitTest(unittest.TestCase):
    """参数切分：Windows 走 MSVCRT 规则，POSIX 走 shlex。"""

    def test_Windows保留反斜杠路径与空格(self):
        got = dev_script_runner.split_args(
            r'--file "C:\data\a b\x.py" --flag', windows=True
        )
        self.assertEqual(["--file", "C:\\data\\a b\\x.py", "--flag"], got)

    def test_Windows引号分组与转义引号(self):
        got = dev_script_runner.split_args(
            r'--msg "a b" --quote "x\"y"', windows=True
        )
        self.assertEqual(["--msg", "a b", "--quote", 'x"y'], got)

    def test_Windows连续反斜杠按MSVCRT折半(self):
        self.assertEqual(
            ["C:\\dir\\"], dev_script_runner.split_args(r'"C:\dir\\"', windows=True)
        )

    @unittest.skipIf(IS_WINDOWS, "默认切分在 Windows 上走 MSVCRT 规则，不是 shlex")
    def test_POSIX默认按shlex切分(self):
        self.assertEqual(["--x", "a b"], dev_script_runner.split_args("--x 'a b'"))

    def test_显式POSIX规则跨平台一致(self):
        self.assertEqual(
            ["--x", "a b"], dev_script_runner.split_args("--x 'a b'", windows=False)
        )

    def test_列表参数原样保留(self):
        self.assertEqual(
            ["--a", "1 2", "3"], dev_script_runner.split_args(["--a", "1 2", 3])
        )

    def test_空值与空白(self):
        self.assertEqual([], dev_script_runner.split_args(None))
        self.assertEqual([], dev_script_runner.split_args("   "))

    def test_后端切分沿用同一实现(self):
        """后端只做转发：固定两种规则逐条比对，不依赖当前平台的默认值。"""
        for text in ("--x 'a b'", r'--file "C:\data\a b\x.py"', "a  b", ""):
            for windows in (True, False):
                self.assertEqual(
                    dev_script_runner.split_args(text, windows=windows),
                    backend._split_args(text, windows=windows),
                )


class MaskArgsTest(unittest.TestCase):
    """参数脱敏：password/secret/token 等值打码，其余原样。"""

    def test_敏感开关的独立值打码(self):
        self.assertEqual(
            ["--password", "***", "--token", "***"],
            dev_script_runner.mask_args(["--password", "s3cret", "--token", "abc"]),
        )

    def test_等号形式打码(self):
        self.assertEqual(
            ["--token=***", "--sso-password=***"],
            dev_script_runner.mask_args(["--token=abc", "--sso-password=p@ss"]),
        )

    def test_查询串内敏感键打码(self):
        got = dev_script_runner.mask_args(["http://h/x?token=q9&a=1"])
        self.assertEqual(["http://h/x?token=***&a=1"], got)

    def test_普通参数保持原样(self):
        self.assertEqual(["--name", "bob"], dev_script_runner.mask_args(["--name", "bob"]))

    def test_空列表(self):
        self.assertEqual([], dev_script_runner.mask_args([]))


class CommandTest(DevScriptTestBase):
    """命令拼装：--cwd 在脚本路径前，脚本参数在 -- 之后。"""

    def test_源码版命令带cwd且在路径之前(self):
        cmd = backend._command(str(self.scripts / "a.py"), ["--x"], str(self.work))
        self.assertEqual(str(APP_ENTRY), cmd[1])
        self.assertEqual("run-script", cmd[2])
        self.assertEqual("--cwd=" + str(self.work), cmd[3])
        self.assertEqual(str((self.scripts / "a.py").resolve()), cmd[4])
        self.assertEqual("--", cmd[5])
        self.assertEqual(["--x"], cmd[6:])

    def test_冻结版命令不带app入口(self):
        with mock.patch.object(sys, "frozen", True, create=True):
            cmd = backend._command(str(self.scripts / "a.py"), [], str(self.work))
        self.assertEqual([sys.executable, "run-script", "--cwd=" + str(self.work)], cmd[:3])
        self.assertNotIn(str(APP_ENTRY), cmd)

    def test_子进程stdin固定DEVNULL(self):
        self.assertEqual(subprocess.DEVNULL, backend._popen_kwargs()["stdin"])

    def test_子进程输出合并到stdout(self):
        kwargs = backend._popen_kwargs()
        self.assertEqual(subprocess.PIPE, kwargs["stdout"])
        self.assertEqual(subprocess.STDOUT, kwargs["stderr"])


class TerminatorTest(unittest.TestCase):
    """终止实现：Windows 上 taskkill 失败时必须退回 proc.kill()。

    以前 taskkill 的非零返回码被当成成功返回，子进程会活着交给调用方收尾。
    """

    class _FakeProc:
        def __init__(self):
            self.pid = 4321
            self.killed = False

        def poll(self):
            return None

        def kill(self):
            self.killed = True

    def _signal_hard(self, returncode):
        proc = self._FakeProc()

        def fake_run(*args, **kwargs):
            return types.SimpleNamespace(returncode=returncode)

        with mock.patch.object(backend, "os", types.SimpleNamespace(name="nt")), (
            mock.patch.object(backend.subprocess, "run", fake_run)
        ):
            backend._signal_tree(proc, hard=True)
        return proc

    def test_taskkill失败时退回强杀(self):
        self.assertTrue(self._signal_hard(1).killed)

    def test_taskkill成功时不重复强杀(self):
        self.assertFalse(self._signal_hard(0).killed)


class SplitterTest(unittest.TestCase):
    """日志切行：CRLF/重复 CR/跨块换行都不产生空行，超长行仍按限额截断。"""

    def _splitter(self, limit=16):
        return backend._LineSplitter(limit=limit)

    def test_CRLF与重复CR各算一次换行(self):
        splitter = self._splitter()
        self.assertEqual(["a", "b", "c"], splitter.feed("a\r\nb\r\r\nc\r\n"))
        self.assertEqual([], splitter.finish())

    def test_跨块CRLF不产生空行(self):
        splitter = self._splitter()
        self.assertEqual([], splitter.feed("a\r"))
        self.assertEqual(["a"], splitter.feed("\nb\r"))
        self.assertEqual(["b"], splitter.feed("\r\nc"))
        self.assertEqual(["c"], splitter.finish())

    def test_EOF处独立CR按换行收尾(self):
        splitter = self._splitter()
        self.assertEqual([], splitter.feed("a\r"))
        self.assertEqual(["a"], splitter.finish())

    def test_超长行仍先吐一段并丢弃中间(self):
        splitter = self._splitter(limit=8)
        out = splitter.feed("x" * 20)
        self.assertEqual(["x" * 8], out)
        out = splitter.feed("y" * 20 + "\r\nnext\r\n")
        self.assertEqual(["…（单行过长，中间部分已丢弃）", "next"], out)
        self.assertEqual([], splitter.finish())


class RunnerCliTest(DevScriptTestBase):
    """子进程入口（toolkit/app.py run-script）的参数、cwd 与退出码契约。"""

    def test_参数与cwd原样传到脚本(self):
        s = self.script(
            "echo.py",
            """
            import json
            import os
            import sys
            print(json.dumps({"argv": sys.argv[1:], "cwd": os.getcwd()}), flush=True)
            """,
        )
        proc = self.run_app(
            "--cwd=" + str(self.work),
            str(s),
            "--",
            "a b",
            "--flag",
            "值",
            'quote"x',
            r"C:\win\path",
        )
        self.assertEqual(0, proc.returncode, proc.stderr)
        payload = json.loads(proc.stdout.strip().splitlines()[-1])
        self.assertEqual(["a b", "--flag", "值", 'quote"x', r"C:\win\path"], payload["argv"])
        # Windows 会把传进去的 8.3 短名原样留给 os.getcwd()：同一目录即可，不比字符串
        self.assertTrue(
            same_path(self.work, payload["cwd"]),
            "cwd 未落在指定工作目录: %r vs %r" % (payload["cwd"], str(self.work)),
        )

    def _echo_cwd_script(self):
        return self.script(
            "echo_cwd.py",
            "\nimport json, os\nprint(json.dumps(os.getcwd()), flush=True)\n",
        )

    def test_工作目录先归一再去执行(self):
        """带 `..` 的写法要归一成真实目录：Windows 长名/短名混用时日志与 os.getcwd()
        才对得上（8.3 短名见下条）。"""
        s = self._echo_cwd_script()
        quirky = str(self.work / ".." / self.work.name)
        proc = self.run_app("--cwd=" + quirky, str(s))
        self.assertEqual(0, proc.returncode, proc.stderr)
        got = json.loads(proc.stdout.strip().splitlines()[-1])
        self.assertEqual(str(self.work.resolve()), got)
        self.assertIn("工作目录: " + str(self.work.resolve()), proc.stdout)

    @unittest.skipUnless(IS_WINDOWS, "8.3 短名只在 Windows 上存在")
    def test_Windows短名工作目录归一到长名(self):
        import ctypes

        buf = ctypes.create_unicode_buffer(1024)
        if not ctypes.windll.kernel32.GetShortPathNameW(str(self.work), buf, 1024):
            self.skipTest("当前卷未启用 8.3 短名")
        short = buf.value
        if os.path.normcase(short) == os.path.normcase(str(self.work)):
            self.skipTest("短名与长名相同（8.3 已禁用）")
        s = self._echo_cwd_script()
        proc = self.run_app("--cwd=" + short, str(s))
        self.assertEqual(0, proc.returncode, proc.stderr)
        got = json.loads(proc.stdout.strip().splitlines()[-1])
        self.assertTrue(same_path(self.work, got), "cwd 未落在指定工作目录: %r" % got)
        self.assertNotIn("~", got, "脚本拿到的仍是 8.3 短名")

    def test_入口日志里的参数脱敏(self):
        s = self.script("quiet.py", "\nprint('done', flush=True)\n")
        proc = self.run_app(
            "--cwd=" + str(self.work), str(s), "--", "--password", "s3cret", "--token=abc"
        )
        self.assertEqual(0, proc.returncode, proc.stderr)
        self.assertNotIn("s3cret", proc.stdout)
        self.assertNotIn("abc", proc.stdout)
        self.assertIn("***", proc.stdout)

    def test_脚本不存在返回2(self):
        proc = self.run_app(str(self.tmp / "nope.py"))
        self.assertEqual(2, proc.returncode)
        self.assertIn("脚本不存在", proc.stderr)

    def test_非py返回2(self):
        bad = self.tmp / "a.txt"
        bad.write_text("x", encoding="utf-8")
        proc = self.run_app(str(bad))
        self.assertEqual(2, proc.returncode)
        self.assertIn("只支持 .py", proc.stderr)

    def test_工作目录不存在返回2(self):
        s = self.script("ok.py", "\nprint('x', flush=True)\n")
        proc = self.run_app("--cwd=" + str(self.tmp / "nope"), str(s))
        self.assertEqual(2, proc.returncode)
        self.assertIn("工作目录不存在", proc.stderr)

    def test_sys_exit码原样返回(self):
        s = self.script("exit7.py", "\nimport sys\nsys.exit(7)\n")
        proc = self.run_app("--cwd=" + str(self.work), str(s))
        self.assertEqual(7, proc.returncode)

    def test_异常返回1并带堆栈(self):
        s = self.script("boom.py", "\nraise RuntimeError('boom')\n")
        proc = self.run_app("--cwd=" + str(self.work), str(s))
        self.assertEqual(1, proc.returncode)
        self.assertIn("RuntimeError: boom", proc.stderr)


class BackendRunTest(DevScriptTestBase):
    """后端 run() 的核心行为（真实脚本、真实子进程）。"""

    def test_输出逐行回传与结果摘要(self):
        s = self.script(
            "basic.py",
            """
            import os
            import sys
            print("line-1", flush=True)
            print("line-2", flush=True)
            print("cwd=" + os.getcwd(), flush=True)
            print("argv=" + repr(sys.argv[1:]), flush=True)
            """,
        )
        job, result = self.run_backend(
            {"path": str(s), "cwd": str(self.work), "args": ["a b", "--flag"]}
        )
        text = job.text()
        self.assertIn("line-1", text)
        self.assertIn("line-2", text)
        self.assertIn("cwd=" + str(self.work.resolve()), text)
        self.assertIn("argv=['a b', '--flag']", text)
        self.assertEqual(0, result["exit_code"])
        self.assertEqual(str(self.work.resolve()), result["cwd"])
        self.assertFalse(result["output_truncated"])
        self.assertGreaterEqual(result["duration_ms"], 0)
        self.assertEqual((1, 1, "完成"), job.progress[-1])

    def test_启动时提示脚本可读凭据(self):
        s = self.script("ok.py", "\nprint('ok', flush=True)\n")
        job, _ = self.run_backend({"path": str(s), "cwd": str(self.work)})
        text = job.text()
        self.assertIn("当前桌面用户权限", text)
        self.assertIn("凭据", text)

    def test_读stdin立即EOF不挂死(self):
        s = self.script(
            "stdin.py",
            """
            import sys
            data = sys.stdin.read()
            print("stdin-len=%d" % len(data), flush=True)
            """,
        )
        started = time.time()
        job, result = self.run_backend(
            {"path": str(s), "cwd": str(self.work), "timeout": 20}
        )
        self.assertLess(time.time() - started, 10)
        self.assertIn("stdin-len=0", job.text())
        self.assertEqual(0, result["exit_code"])

    def test_input无交互直接EOFError(self):
        s = self.script(
            "input.py",
            """
            value = input("请输入：")
            print(value, flush=True)
            """,
        )
        job = FakeJob()
        with self.assertRaises(RuntimeError):
            backend.run(job, {"path": str(s), "cwd": str(self.work), "timeout": 20})
        self.assertIn("EOFError", job.text())

    def test_cwd不被脚本目录覆盖(self):
        s = self.script(
            "cwd.py",
            """
            import os
            with open("marker.txt", "w", encoding="utf-8") as fh:
                fh.write("x")
            print("cwd=" + os.getcwd(), flush=True)
            """,
        )
        job, result = self.run_backend({"path": str(s), "cwd": str(self.work)})
        self.assertIn("cwd=" + str(self.work.resolve()), job.text())
        self.assertEqual(str(self.work.resolve()), result["cwd"])
        self.assertTrue((self.work / "marker.txt").exists(), "脚本未在指定工作目录运行")
        self.assertFalse((self.scripts / "marker.txt").exists())

    def test_CRLF不产生空日志行(self):
        s = self.script(
            "crlf.py",
            """
            import sys
            sys.stdout.write("a\\r\\nb\\r\\nc\\r\\n")
            sys.stdout.flush()
            """,
        )
        job, _ = self.run_backend({"path": str(s), "cwd": str(self.work)})
        payload = [line.split("] ", 1)[-1] for line in job.lines]
        self.assertIn("a", payload)
        self.assertIn("b", payload)
        self.assertIn("c", payload)
        self.assertNotIn("", payload, "CRLF 被拆成了空行")

    def test_Windows文本模式重复CR不产生空行(self):
        """Windows 文本模式把脚本写的 "\\r\\n" 转成 "\\r\\r\\n"，管道里就是重复 CR。"""
        s = self.script(
            "crlf2.py",
            """
            import sys
            sys.stdout.write("a\\r\\r\\nb\\r\\r\\nc\\r\\n")
            sys.stdout.flush()
            """,
        )
        job, _ = self.run_backend({"path": str(s), "cwd": str(self.work)})
        payload = [line.split("] ", 1)[-1] for line in job.lines]
        self.assertIn("a", payload)
        self.assertIn("b", payload)
        self.assertIn("c", payload)
        self.assertNotIn("", payload, "重复 CR 被拆成了空行")

    def test_无换行尾部输出不丢失(self):
        s = self.script("tail.py", "\nimport sys\nsys.stdout.write('tail-only')\nsys.stdout.flush()\n")
        job, _ = self.run_backend({"path": str(s), "cwd": str(self.work)})
        self.assertIn("tail-only", job.text())

    def test_孙进程持管道不挂死(self):
        s = self.script(
            "grandchild.py",
            """
            import os
            import subprocess
            import sys
            child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
            with open(os.environ["DEV_SCRIPT_PIDFILE"], "w", encoding="utf-8") as fh:
                fh.write(str(child.pid))
            print("grandchild-started", flush=True)
            os._exit(0)
            """,
        )
        started = time.time()
        job, result = self.run_backend(
            {"path": str(s), "cwd": str(self.work), "timeout": 20}
        )
        elapsed = time.time() - started
        self.assertLess(elapsed, 8, "孙进程持管道时任务未及时收敛")
        self.assertIn("grandchild-started", job.text())
        self.assertEqual(0, result["exit_code"])
        self.assertIn("占用输出管道", job.text())
        # 孙进程仍在跑并握着工作目录：主动收掉，别把临时目录锁到清理阶段
        kill_pid(self.killed_pid())

    def test_超时后SIGTERM升级SIGKILL(self):
        s = self.script(
            "ignore_term.py",
            """
            import os
            import signal
            import time
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            with open(os.environ["DEV_SCRIPT_PIDFILE"], "w", encoding="utf-8") as fh:
                fh.write(str(os.getpid()))
            print("ready", flush=True)
            while True:
                time.sleep(0.2)
            """,
        )
        started = time.time()
        job = FakeJob()
        with self.assertRaises(RuntimeError) as ctx:
            backend.run(job, {"path": str(s), "cwd": str(self.work), "timeout": 2})
        self.assertIn("超时", str(ctx.exception))
        self.assertLess(time.time() - started, 8)
        pid = self.killed_pid()
        self.assert_pid_gone(pid, "忽略优雅信号的脚本未被强杀")
        self.assertIn("ready", job.text())

    def test_取消可收敛且钩子不阻塞(self):
        s = self.script(
            "sleep.py",
            """
            import os
            import signal
            import time
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            with open(os.environ["DEV_SCRIPT_PIDFILE"], "w", encoding="utf-8") as fh:
                fh.write(str(os.getpid()))
            print("started", flush=True)
            time.sleep(60)
            """,
        )
        patcher = mock.patch.object(backend, "KILL_GRACE_SEC", 2.0)
        patcher.start()
        self.addCleanup(patcher.stop)

        seen = threading.Event()
        job = FakeJob(on_line=lambda line: seen.set() if "started" in line else None)
        outcome = {}

        def worker():
            try:
                backend.run(job, {"path": str(s), "cwd": str(self.work)})
                outcome["result"] = "ok"
            except BaseException as exc:  # noqa: BLE001
                outcome["error"] = exc

        thread = threading.Thread(target=worker, daemon=True)
        thread.start()
        self.assertTrue(seen.wait(15), "脚本未输出启动标记")
        cancel_started = time.time()
        job.cancel()
        cancel_sec = time.time() - cancel_started
        thread.join(15)
        self.assertLess(cancel_sec, 0.5, "取消钩子在调用线程里等进程退出")
        self.assertFalse(thread.is_alive(), "取消后 run() 未收敛")
        self.assertIsInstance(outcome.get("error"), jobs.Cancelled)
        pid = self.killed_pid()
        self.assert_pid_gone(pid, "被取消的脚本未被杀掉")

    def test_参数日志与结果脱敏(self):
        s = self.script("quiet.py", "\nprint('done', flush=True)\n")
        job, result = self.run_backend(
            {
                "path": str(s),
                "cwd": str(self.work),
                "args": ["--password", "s3cret", "--token=abc123", "http://h/x?token=q9"],
            }
        )
        text = job.text()
        for secret in ("s3cret", "abc123", "q9"):
            self.assertNotIn(secret, text)
            self.assertNotIn(secret, json.dumps(result, ensure_ascii=False))
        self.assertIn("***", text)
        self.assertEqual("***", result["args"][1])
        self.assertEqual("--token=***", result["args"][2])
        self.assertIn("token=***", result["args"][3])

    def test_单行截断与总量截断只提示一次(self):
        patch_line = mock.patch.object(backend, "MAX_LINE_CHARS", 200)
        patch_total = mock.patch.object(backend, "MAX_TOTAL_CHARS", 900)
        patch_line.start()
        patch_total.start()
        self.addCleanup(patch_line.stop)
        self.addCleanup(patch_total.stop)
        s = self.script(
            "noisy.py",
            """
            print("L" * 1000, flush=True)
            for i in range(60):
                print("row-%02d" % i + "x" * 60, flush=True)
            print("done", flush=True)
            """,
        )
        job, result = self.run_backend({"path": str(s), "cwd": str(self.work)})
        self.assertTrue(result["output_truncated"])
        self.assertEqual(0, result["exit_code"])
        limit_lines = [line for line in job.lines if "本行已截断" in line]
        self.assertEqual(1, len(limit_lines))
        self.assertLessEqual(len(limit_lines[0]), 300)
        self.assertEqual(1, len([line for line in job.lines if "输出超过上限" in line]))
        self.assertNotIn("done", job.text(), "超限后仍继续记录输出")
        self.assertLess(sum(len(line) for line in job.lines), 2000)

    def test_退出码非零报错(self):
        s = self.script("exit5.py", "\nimport sys\nsys.exit(5)\n")
        job = FakeJob()
        with self.assertRaises(RuntimeError) as ctx:
            backend.run(job, {"path": str(s), "cwd": str(self.work)})
        self.assertIn("退出码 5", str(ctx.exception))

    def test_缺少参数与非法脚本直接报错(self):
        with self.assertRaises(RuntimeError):
            backend.run(FakeJob(), {"path": ""})
        bad = self.tmp / "a.txt"
        bad.write_text("x", encoding="utf-8")
        with self.assertRaises(RuntimeError) as ctx:
            backend.run(FakeJob(), {"path": str(bad)})
        self.assertIn("只支持 .py", str(ctx.exception))
        s = self.script("ok.py", "\nprint('x', flush=True)\n")
        with self.assertRaises(RuntimeError) as ctx:
            backend.run(FakeJob(), {"path": str(s), "cwd": str(self.tmp / "nope")})
        self.assertIn("工作目录不存在", str(ctx.exception))


class DevSwitchTest(DevScriptTestBase):
    """开发工具开关：app.json 的 dev 标记与 dev.allow_scripts 都要为真。"""

    def _run_ok(self):
        s = self.script("ok.py", "\nprint('ok', flush=True)\n")
        return backend.run(FakeJob(), {"path": str(s), "cwd": str(self.work)})

    def test_默认开启(self):
        self.assertEqual(0, self._run_ok()["exit_code"])

    def test_app_json标记关闭即拒绝(self):
        meta = self.tmp / "app.json"
        meta.write_text(json.dumps({"id": "dev_script", "dev": False}), encoding="utf-8")
        with mock.patch.object(backend, "APP_META_PATH", meta):
            with self.assertRaises(RuntimeError) as ctx:
                self._run_ok()
        self.assertIn("关闭", str(ctx.exception))

    def test_配置开关关闭即拒绝(self):
        with mock.patch.object(
            backend._config, "load", lambda: {"dev": {"allow_scripts": False}}
        ):
            with self.assertRaises(RuntimeError) as ctx:
                self._run_ok()
        self.assertIn("dev.allow_scripts", str(ctx.exception))


class RealJobIntegrationTest(DevScriptTestBase):
    """用真实 jobs.Job / JobManager 走一遍：日志落盘、终态、取消钩子语义一致。"""

    def setUp(self):
        super().setUp()
        self.manager = jobs.JobManager()

    def _job(self, title="脚本测试"):
        job = self.manager.create("dev_script", title)
        self.addCleanup(lambda: job._log_fp.close() if not job._log_fp.closed else None)
        return job

    def test_成功任务落终态且日志写入文件(self):
        s = self.script("ok.py", "\nprint('hello-from-script', flush=True)\n")
        job = self._job()
        self.manager.run_sync(
            job,
            lambda jb: backend.run(jb, {"path": str(s), "cwd": str(self.work)}),
            echo=False,
        )
        self.assertEqual("success", job.status, job.error)
        self.assertEqual(0, job.result["exit_code"])
        self.assertFalse(job.result["output_truncated"])
        text = Path(job._log_fp.name).read_text(encoding="utf-8")
        self.assertIn("hello-from-script", text)
        self.assertIn("当前桌面用户权限", text)

    def test_取消经Jobcancel终止进程且落cancelled(self):
        s = self.script(
            "sleep.py",
            """
            import os
            import signal
            import time
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            with open(os.environ["DEV_SCRIPT_PIDFILE"], "w", encoding="utf-8") as fh:
                fh.write(str(os.getpid()))
            print("started", flush=True)
            time.sleep(60)
            """,
        )
        started = threading.Event()
        job = self._job("取消测试")
        job.on_log = lambda line: started.set() if "started" in line else None

        def worker():
            self.manager.run_sync(
                job,
                lambda jb: backend.run(jb, {"path": str(s), "cwd": str(self.work)}),
                echo=False,
            )

        thread = threading.Thread(target=worker, daemon=True)
        thread.start()
        self.assertTrue(started.wait(20), "脚本未输出启动标记")
        job.cancel()
        thread.join(20)
        self.assertFalse(thread.is_alive(), "取消后任务未收敛")
        self.assertEqual("cancelled", job.status)
        self.assert_pid_gone(self.killed_pid(), "被取消的脚本未被杀掉")


if __name__ == "__main__":
    unittest.main()
