# -*- coding: utf-8 -*-
"""构建门禁 CDP 冒烟驱动（packaging/cdp_smoke.py）测试。

用假核心（stdio JSON-RPC 脚本）模拟 uvmp-core rpc 的成功 / 无浏览器 / 失败 / 超时路径，
不启动真实浏览器、不访问网络。重点：无浏览器必须明确报告且退出码 2，不得静默当通过。
"""

import contextlib
import io
import os
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

PACKAGING_DIR = Path(__file__).resolve().parent
if str(PACKAGING_DIR) not in sys.path:
    sys.path.insert(0, str(PACKAGING_DIR))

import cdp_smoke  # noqa: E402

FAKE_CORE = textwrap.dedent(
    r"""
    import json
    import os
    import subprocess
    import sys
    import time

    mode = os.environ.get("UVMP_SMOKE_FAKE_MODE", "ok")
    marker = os.environ.get("UVMP_SMOKE_MARKER")
    if mode == "exit":
        sys.exit(3)
    if mode == "hang":
        # 模拟核心拉起的浏览器子进程：核心被终止时它必须随进程树一起回收
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(120)"])
        if marker:
            with open(marker, "w", encoding="utf-8") as fp:
                fp.write("core=%d\nchild=%d\n" % (os.getpid(), child.pid))
        sys.stderr.write("fake-core stderr: 超时前诊断\n")
        sys.stderr.flush()
    base = os.environ.get("LOCALAPPDATA") or os.environ.get("XDG_DATA_HOME")
    logs = os.path.join(base, "uvmp-toolkit", "logs")
    os.makedirs(logs, exist_ok=True)

    def send(obj):
        sys.stdout.write(json.dumps(obj) + "\n")
        sys.stdout.flush()

    def artifact(name, data):
        path = os.path.join(logs, name)
        with open(path, "wb") as fp:
            fp.write(data)
        return path

    for line in sys.stdin:
        req = json.loads(line)
        if req.get("method") == "cancelJob":
            if marker:
                with open(marker, "a", encoding="utf-8") as fp:
                    fp.write("cancel=%s\n" % (req.get("params") or {}).get("job_id", ""))
            send({"id": req["id"], "ok": True, "result": {"ok": True}})
            send({"event": "job.done", "job_id": "job-1", "job": {
                "id": "job-1", "status": "cancelled", "error": "", "result": {}}})
            if mode == "hang":
                while True:  # 核心不自行退出：验证驱动会终止进程树
                    time.sleep(1)
            continue
        if req.get("method") != "runJob":
            continue
        if mode == "reject":
            send({"id": req["id"], "ok": False, "error": "未知自检类型: cdp"})
            continue
        send({"id": req["id"], "ok": True, "result": {"job_id": "job-1"}})
        if mode == "silent":
            continue
        if mode == "hang":
            send({"event": "job.log", "job_id": "job-1", "line": "自检进行中：等待取消"})
            continue
        if mode == "nobrowser":
            msg = "CDP 渲染自检：未找到可用浏览器（未找到浏览器：请在设置页手动选择）"
            send({"event": "job.log", "job_id": "job-1", "line": msg})
            send({"event": "job.done", "job_id": "job-1", "job": {
                "id": "job-1", "status": "failed", "error": msg, "result": {}}})
            continue
        if mode == "error":
            send({"event": "job.done", "job_id": "job-1", "job": {
                "id": "job-1", "status": "failed",
                "error": "CDP 渲染自检：启动浏览器失败：boom", "result": {}}})
            continue
        png = artifact("cdp-selftest.png", b"\x89PNG\r\n\x1a\nfake-png")
        pdf = artifact("cdp-selftest.pdf", b"%PDF-1.4\nfake-pdf")
        if mode == "badartifact":
            png = artifact("cdp-selftest.png", b"not-a-png")
        checks = ["启动浏览器", "导航与 eval", "新 tab", "截图 PNG", "printToPDF"]
        send({"event": "job.log", "job_id": "job-1", "line": "CDP 渲染自检：完成"})
        send({"event": "job.done", "job_id": "job-1", "job": {
            "id": "job-1", "status": "success", "error": "",
            "result": {
                "ok": True,
                "browser": r"C:\browser.exe",
                "png": png,
                "pdf": pdf,
                "png_bytes": os.path.getsize(png),
                "pdf_bytes": os.path.getsize(pdf),
                "checks": checks,
            }}})
    """
)


def _pid_alive(pid: int) -> bool:
    """跨平台判断进程是否仍存活（僵尸进程视为已回收）"""
    if os.name == "nt":
        out = subprocess.run(
            ["tasklist", "/FI", "PID eq %d" % pid],
            capture_output=True,
            text=True,
            errors="replace",
            timeout=30,
        ).stdout
        return str(pid) in out
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    try:
        with open("/proc/%d/stat" % pid, encoding="utf-8") as fp:
            return fp.read().split()[2] != "Z"
    except OSError:
        return True


def _kill_pids(pids):
    """测试收尾：RED 阶段残留的假核心/假浏览器子进程强制清理"""
    for pid in pids:
        if not _pid_alive(pid):
            continue
        if os.name == "nt":
            subprocess.run(
                ["taskkill", "/T", "/F", "/PID", str(pid)],
                capture_output=True,
                timeout=30,
            )
        else:
            try:
                os.kill(pid, 9)
            except OSError:
                pass


class CdpSmokeTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.core = Path(self._tmp.name) / "fake_core.py"
        self.core.write_text(FAKE_CORE, encoding="utf-8")

    def _run(self, mode, timeout="15"):
        old = os.environ.get("UVMP_SMOKE_FAKE_MODE")
        os.environ["UVMP_SMOKE_FAKE_MODE"] = mode
        out = io.StringIO()
        try:
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
                code = cdp_smoke.main(["--core", str(self.core), "--timeout", timeout])
        finally:
            if old is None:
                os.environ.pop("UVMP_SMOKE_FAKE_MODE", None)
            else:
                os.environ["UVMP_SMOKE_FAKE_MODE"] = old
        return code, out.getvalue()

    def test_成功路径校验五项检查与产物魔数(self):
        code, out = self._run("ok")
        self.assertEqual(code, 0, out)
        self.assertIn("CDP 冒烟通过", out)
        self.assertIn(r"C:\browser.exe", out)
        self.assertIn("5/5", out)

    def test_无宿主浏览器时明确报告并以退出码2跳过(self):
        code, out = self._run("nobrowser")
        self.assertEqual(code, 2, out)
        self.assertIn("跳过", out)
        self.assertIn("未找到可用浏览器", out)
        self.assertNotIn("通过", out)

    def test_其它失败退出码1且带错误信息(self):
        code, out = self._run("error")
        self.assertEqual(code, 1, out)
        self.assertIn("启动浏览器失败", out)

    def test_协议拒绝退出码1(self):
        code, out = self._run("reject")
        self.assertEqual(code, 1, out)
        self.assertIn("未知自检类型", out)

    def test_产物不是PNG时退出码1(self):
        code, out = self._run("badartifact")
        self.assertEqual(code, 1, out)
        self.assertIn("PNG", out)

    def test_核心无响应时超时退出码1(self):
        code, out = self._run("silent", timeout="2")
        self.assertEqual(code, 1, out)
        self.assertIn("超时", out)

    def test_超时后发送cancelJob并回收核心与浏览器进程树(self):
        marker = Path(self._tmp.name) / "smoke-marker.txt"
        old = os.environ.get("UVMP_SMOKE_MARKER")
        os.environ["UVMP_SMOKE_MARKER"] = str(marker)
        try:
            code, out = self._run("hang", timeout="2")
        finally:
            if old is None:
                os.environ.pop("UVMP_SMOKE_MARKER", None)
            else:
                os.environ["UVMP_SMOKE_MARKER"] = old
        self.assertEqual(code, 1, out)
        self.assertIn("超时", out)
        text = marker.read_text(encoding="utf-8")
        self.assertIn("cancel=job-1", text)  # 超时先给核心发 cancelJob
        self.assertIn("自检进行中", out)  # 已累积 job.log 尾部保留
        self.assertIn("fake-core stderr", out)  # 核心 stderr 尾部保留
        pids = [int(x) for x in re.findall(r"(?:core|child)=(\d+)", text)]
        self.assertEqual(len(pids), 2, text)
        self.addCleanup(_kill_pids, pids)
        for pid in pids:  # 核心与（模拟的）浏览器子进程均随进程树回收
            self.assertFalse(_pid_alive(pid), "进程 %d 仍存活" % pid)

    def test_核心启动即退出时退出码1(self):
        code, out = self._run("exit")
        self.assertEqual(code, 1, out)
        self.assertIn("核心", out)


if __name__ == "__main__":
    unittest.main()
