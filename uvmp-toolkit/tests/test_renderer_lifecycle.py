# -*- coding: utf-8 -*-
"""CdpBrowser 启动失败清理与 close 幂等、uvmp.start_renderer 半成品回收。

全部使用 fake subprocess / fake websocket，不启动真实浏览器、不访问网络。
覆盖：端口等待期进程退出 / 未拿到调试端口 / 未找到调试目标 / websocket 连接失败 /
strict 模式启动期 CDP 错误，均须 terminate（必要时 kill）并 wait 进程；close 重复调用只清理一次；
默认非 strict 模式对启动期/瞬时 CDP error 不抛、eval 返回 None 且 wait_expr 可重试；
strict=True 时 CDP error 进入 RuntimeError 消息；start_renderer 在 start 抛错时 stop 半成品渲染器。
"""

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT), str(ROOT / "pylib")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import official_track  # noqa: E402
import platform_utils  # noqa: E402
import uvmp  # noqa: E402


class FakeProc:
    def __init__(self, exit_code=None):
        self.exit_code = exit_code
        self.terminate_calls = 0
        self.kill_calls = 0
        self.wait_calls = []

    def poll(self):
        return self.exit_code

    def terminate(self):
        self.terminate_calls += 1
        self.exit_code = 0

    def kill(self):
        self.kill_calls += 1
        self.exit_code = -9

    def wait(self, timeout=None):
        self.wait_calls.append(timeout)
        return self.exit_code if self.exit_code is not None else 0


class FakeWs:
    """按请求 id 回包；error_responses 里的方法返回 CDP error 响应。"""

    def __init__(self, error_responses=None):
        self.sent = []
        self.closed = 0
        self.timeouts = []
        self.error_responses = error_responses or {}

    def settimeout(self, timeout):
        self.timeouts.append(timeout)

    def send(self, raw):
        self.sent.append(json.loads(raw))

    def recv(self):
        req = self.sent[-1]
        method = req["method"]
        if method in self.error_responses:
            return json.dumps(
                {
                    "id": req["id"],
                    "error": {"code": -32000, "message": self.error_responses[method]},
                }
            )
        return json.dumps({"id": req["id"], "result": {}})

    def close(self):
        self.closed += 1


class RetryWs(FakeWs):
    """首次 Runtime.evaluate 返回瞬时 context 错误，之后返回 True（模拟重试成功）"""

    def __init__(self):
        super().__init__()
        self.eval_calls = 0

    def recv(self):
        req = self.sent[-1]
        if req["method"] == "Runtime.evaluate":
            self.eval_calls += 1
            if self.eval_calls == 1:
                return json.dumps(
                    {
                        "id": req["id"],
                        "error": {
                            "code": -32000,
                            "message": "Cannot find context with specified id",
                        },
                    }
                )
            return json.dumps({"id": req["id"], "result": {"result": {"value": True}}})
        return super().recv()


class FakeJsonResponse:
    def __init__(self, payload):
        self.payload = payload

    def read(self):
        return json.dumps(self.payload).encode("utf-8")


class CdpBrowserLifecycleTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.work = Path(self._tmp.name)
        self.log_file = self.work / "browser.log"
        self.port_file = self.work / ".chrome-official-tmp" / "DevToolsActivePort"

    def _write_port(self, port=9222):
        self.port_file.parent.mkdir(parents=True, exist_ok=True)
        self.port_file.write_text(
            "%d\n/devtools/browser/fake\n" % port, encoding="utf-8"
        )

    def _start(self, proc, *, tabs=None, create_ws=None, popen_hook=None, strict=False):
        """启动 CdpBrowser；popen_hook 可在 Popen 返回前写 DevToolsActivePort。"""

        def _popen(*a, **kw):
            if popen_hook:
                popen_hook()
            return proc

        def _urlopen(_url, timeout=None):
            return FakeJsonResponse(tabs if tabs is not None else [])

        create_ws = create_ws or (lambda *a, **kw: FakeWs())
        patches = [
            mock.patch.object(official_track.subprocess, "Popen", side_effect=_popen),
            mock.patch.object(official_track.time, "sleep"),
            mock.patch.object(official_track.urllib.request, "urlopen", _urlopen),
            mock.patch.object(
                official_track.websocket, "create_connection", side_effect=create_ws
            ),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        return official_track.CdpBrowser(
            "C:/browser.exe", log_file=self.log_file, strict=strict
        )

    def _assert_proc_cleaned(self, proc):
        self.assertEqual(proc.terminate_calls, 1)
        self.assertTrue(proc.wait_calls, "启动失败后必须 wait 进程")
        self.assertIsNotNone(proc.wait_calls[-1], "wait 必须带超时，避免永久阻塞")

    def test_端口等待期进程提前退出时清理进程(self):
        proc = FakeProc(exit_code=1)  # 不写 DevToolsActivePort，poll 立即报退出
        with self.assertRaises(RuntimeError) as ctx:
            self._start(proc)
        self.assertIn("提前退出", str(ctx.exception))
        self._assert_proc_cleaned(proc)

    def test_未拿到调试端口时清理进程(self):
        proc = FakeProc()  # 一直存活但不写端口文件
        with self.assertRaises(RuntimeError) as ctx:
            self._start(proc)
        self.assertIn("调试端口", str(ctx.exception))
        self._assert_proc_cleaned(proc)

    def test_未找到调试目标时清理进程(self):
        proc = FakeProc()
        with self.assertRaises(RuntimeError) as ctx:
            self._start(proc, tabs=[], popen_hook=self._write_port)
        self.assertIn("调试目标", str(ctx.exception))
        self._assert_proc_cleaned(proc)

    def test_websocket连接失败时清理进程(self):
        proc = FakeProc()
        with self.assertRaises(RuntimeError) as ctx:
            self._start(
                proc,
                tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
                create_ws=RuntimeError("ws handshake failed"),
                popen_hook=self._write_port,
            )
        self.assertIn("ws handshake failed", str(ctx.exception))
        self._assert_proc_cleaned(proc)

    def test_strict启动阶段CDP错误响应清理进程并关闭ws(self):
        proc = FakeProc()
        ws = FakeWs(error_responses={"Page.enable": "Page domain unavailable"})
        with self.assertRaises(RuntimeError) as ctx:
            self._start(
                proc,
                tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
                create_ws=lambda *a, **kw: ws,
                popen_hook=self._write_port,
                strict=True,
            )
        text = str(ctx.exception)
        self.assertIn("Page.enable", text)
        self.assertIn("Page domain unavailable", text)
        self.assertEqual(ws.closed, 1)
        self._assert_proc_cleaned(proc)

    def test_默认模式启动期非关键CDP错误不终止(self):
        proc = FakeProc()
        ws = FakeWs(error_responses={"Page.enable": "Page domain unavailable"})
        br = self._start(
            proc,
            tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
            create_ws=lambda *a, **kw: ws,
            popen_hook=self._write_port,
        )
        try:
            self.assertEqual(proc.terminate_calls, 0)
            self.assertIsNone(br.eval("1+1"))
        finally:
            br.close()

    def test_popen失败也关闭日志句柄(self):
        opened = []

        class TrackingFile:
            closed = False

            def close(self):
                self.closed = True

        def _open(path, mode="r", *a, **kw):
            fh = TrackingFile()
            opened.append(fh)
            return fh

        with mock.patch.object(
            official_track.subprocess, "Popen", side_effect=OSError("no chrome")
        ):
            with mock.patch("builtins.open", side_effect=_open):
                with self.assertRaises(OSError):
                    official_track.CdpBrowser("C:/browser.exe", log_file=self.log_file)
        self.assertTrue(
            opened and opened[0].closed, "Popen 失败时须关闭已打开的浏览器日志"
        )

    def test_close幂等且只清理一次(self):
        proc = FakeProc()
        ws = FakeWs()
        br = self._start(
            proc,
            tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
            create_ws=lambda *a, **kw: ws,
            popen_hook=self._write_port,
        )
        br.close()
        br.close()
        self.assertEqual(ws.closed, 1)
        self.assertEqual(proc.terminate_calls, 1)
        self.assertEqual(len(proc.wait_calls), 1)
        self.assertIsNotNone(proc.wait_calls[0])

    def test_默认模式瞬时CDP错误不抛出且eval可重试(self):
        proc = FakeProc()
        ws = RetryWs()
        br = self._start(
            proc,
            tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
            create_ws=lambda *a, **kw: ws,
            popen_hook=self._write_port,
        )
        try:
            self.assertIsNone(br.eval("1+1"))  # 首次瞬时 context 错误 → None
            self.assertTrue(
                br.wait_expr("document.title === 'ok'", timeout=1, interval=0)
            )
        finally:
            br.close()
        self.assertEqual(ws.eval_calls, 2)

    def test_默认模式new_tab的非关键CDP错误不抛出(self):
        proc = FakeProc()
        tab_ws = FakeWs(error_responses={"Page.enable": "Page domain unavailable"})
        sockets = iter([FakeWs(), tab_ws])
        br = self._start(
            proc,
            tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
            create_ws=lambda *a, **kw: next(sockets),
            popen_hook=self._write_port,
        )

        def _urlopen(url, timeout=None):
            target = getattr(url, "full_url", url)
            if "/json/new" in target:
                return FakeJsonResponse(
                    {"id": "tab-1", "webSocketDebuggerUrl": "ws://tab"}
                )
            return FakeJsonResponse([{"type": "page"}])

        try:
            with mock.patch.object(official_track.urllib.request, "urlopen", _urlopen):
                tab = br.new_tab()
        finally:
            br.close()
        self.assertEqual(tab["id"], "tab-1")

    def test_strict模式CDP错误响应带方法名与消息(self):
        proc = FakeProc()
        ws = FakeWs(
            error_responses={
                "Page.navigate": "Cannot navigate to invalid URL",
                "Runtime.evaluate": "Cannot find context",
            }
        )
        br = self._start(
            proc,
            tabs=[{"type": "page", "webSocketDebuggerUrl": "ws://fake"}],
            create_ws=lambda *a, **kw: ws,
            popen_hook=self._write_port,
            strict=True,
        )
        try:
            with self.assertRaises(RuntimeError) as ctx:
                br.navigate("not-a-url")
            with self.assertRaises(RuntimeError) as eval_ctx:
                br.eval("1+1")
        finally:
            br.close()
        text = str(ctx.exception)
        self.assertIn("Page.navigate", text)
        self.assertIn("Cannot navigate to invalid URL", text)
        self.assertIn("Runtime.evaluate", str(eval_ctx.exception))
        self.assertIn("Cannot find context", str(eval_ctx.exception))


class StartRendererCleanupTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.work = Path(self._tmp.name)

    def test_start抛错时停止半成品渲染器(self):
        stopped = []

        class BadRenderer:
            def __init__(self, *a, **kw):
                self.args = a

            def start(self, _auth):
                raise RuntimeError("登录态注入失败")

            def stop(self):
                stopped.append(True)

        cfg = {"dev": {"mock": False}, "export": {"track_render": "official"}}
        logs = []
        with (
            mock.patch.object(uvmp._config, "ve_cfg", return_value=cfg["export"]),
            mock.patch.object(uvmp._config, "render_cfg", return_value={}),
            mock.patch.object(
                platform_utils, "detect_chrome", return_value="C:/browser.exe"
            ),
            mock.patch.object(official_track, "OfficialTrackRenderer", BadRenderer),
        ):
            renderer = uvmp.start_renderer(
                object(), object(), cfg, self.work, logs.append
            )
        self.assertIsNone(renderer)
        self.assertEqual(stopped, [True])
        self.assertTrue(any("启动失败" in line for line in logs), logs)


if __name__ == "__main__":
    unittest.main()
