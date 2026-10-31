# -*- coding: utf-8 -*-
"""CDP 渲染自检的纯 Python 实现测试（Task 4）。

全部使用 fake 浏览器与临时日志目录，不启动真实浏览器、不访问网络。
覆盖：浏览器选择口径（用户指定优先/自动探测/缺失明确报错）、导航与 eval、
新 tab 导航与 eval、裁剪截图 PNG 落盘与魔数校验、printToPDF 落盘与魔数校验、
产物路径/结果形状、取消钩子关闭浏览器、失败时关闭浏览器并带步骤报错。
"""

import base64
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import jobs  # noqa: E402
import selftest  # noqa: E402
import official_track  # noqa: E402

PNG_BYTES = b"\x89PNG\r\n\x1a\n" + b"fake-png-body"
PDF_BYTES = b"%PDF-1.4\n" + b"fake-pdf-body"


class FakeJob:
    """与 jobs.Job 相同的业务侧接口；不落日志文件"""

    def __init__(self):
        self.id = "job-selftest-cdp"
        self.logs = []
        self.progress = []
        self.hooks = []
        self._cancel = threading.Event()

    def log(self, msg):
        self.logs.append(msg)

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    @property
    def cancelled(self):
        return self._cancel.is_set()

    def check_cancel(self):
        if self._cancel.is_set():
            raise jobs.Cancelled()

    def cancel(self):
        self._cancel.set()
        for fn in list(self.hooks):
            fn()

    def add_cancel_hook(self, fn):
        self.hooks.append(fn)
        if self._cancel.is_set():
            fn()


class FakeTab:
    def __init__(self):
        self.calls = []
        self.title = ""

    def call(self, method, params=None, timeout=120):
        self.calls.append((method, params or {}))
        if method == "Page.navigate":
            url = (params or {}).get("url", "")
            html = base64.b64decode(url.split(",", 1)[1]).decode("utf-8")
            self.title = html.split("<title>", 1)[1].split("</title>", 1)[0]
            return {"id": 1, "result": {}}
        if method == "Runtime.evaluate":
            expr = (params or {}).get("expression", "")
            value = None
            if expr == "document.title":
                value = self.title
            elif expr.startswith("document.title ==="):
                want = expr.split("===", 1)[1].strip().strip("'\"")
                value = self.title == want
            elif expr.replace(" ", "") == "1+1":
                value = 2
            return {"id": 1, "result": {"result": {"value": value}}}
        if method == "Page.printToPDF":
            return {"id": 1, "result": {"data": base64.b64encode(PDF_BYTES).decode()}}
        return {"id": 1, "result": {}}


class FakeCdpBrowser:
    """fake official_track.CdpBrowser：记录调用并落真实 PNG 文件"""

    instances = []
    fail_screenshot = False

    def __init__(self, chrome, width=1500, height=950, log_file=None, strict=False):
        self.chrome = chrome
        self.width = width
        self.height = height
        self.log_file = Path(log_file) if log_file else None
        self.strict = strict
        self.prof = None
        if self.log_file:
            self.log_file.parent.mkdir(parents=True, exist_ok=True)
            self.log_file.write_text("fake browser log\n", encoding="utf-8")
            self.prof = self.log_file.parent / ".chrome-official-tmp"
            self.prof.mkdir(parents=True, exist_ok=True)
            (self.prof / "Cookies").write_text("x", encoding="utf-8")
        self.navigated = []
        self.evaluated = []
        self.title = ""
        self.clip = None
        self.closed = False
        self.tab = None
        self.close_tab_calls = 0
        FakeCdpBrowser.instances.append(self)

    def navigate(self, url):
        self.navigated.append(url)
        html = base64.b64decode(url.split(",", 1)[1]).decode("utf-8")
        self.title = html.split("<title>", 1)[1].split("</title>", 1)[0]

    def eval(self, expression, timeout=30):
        self.evaluated.append(expression)
        if expression == "document.title":
            return self.title
        if expression.replace(" ", "") == "1+2":
            return 3
        return None

    def wait_expr(self, expression, timeout=60, interval=0.5):
        return "document.title" in expression and bool(self.title)

    def new_tab(self):
        self.tab = FakeTab()
        return {"ws": None, "call": self.tab.call, "id": "tab-1"}

    def close_tab(self, tab):
        self.close_tab_calls += 1
        tab["id"] = ""

    def screenshot_clip(self, clip, out_png):
        self.clip = clip
        if FakeCdpBrowser.fail_screenshot:
            Path(out_png).write_bytes(b"not-a-png")
            return
        Path(out_png).write_bytes(PNG_BYTES)

    def close(self):
        self.closed = True


def _reset(**kwargs):
    FakeCdpBrowser.instances = []
    FakeCdpBrowser.fail_screenshot = kwargs.get("fail_screenshot", False)


class CdpSelftestTest(unittest.TestCase):
    def setUp(self):
        _reset()
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.logs = Path(self._tmp.name)
        self.job = FakeJob()
        self.cfg = {"_ini": {}, "render": {}}

    def _run(self, chosen="", job=None):
        cfg = {"_ini": {}, "render": {"chrome": chosen} if chosen else {}}
        job = job or self.job
        with (
            mock.patch.object(selftest._config, "load", return_value=cfg),
            mock.patch.object(
                selftest._config,
                "render_cfg",
                side_effect=lambda c: c.get("render") or {},
            ),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ) as detect,
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", FakeCdpBrowser),
        ):
            result = selftest.run_cdp(job, {})
        return result, detect

    def test_自动探测浏览器并跑完全部检查项与产物落盘(self):
        result, detect = self._run()
        detect.assert_called_once_with("")
        self.assertTrue(result["ok"])
        self.assertEqual(result["browser"], r"C:\browser.exe")
        self.assertEqual(
            result["checks"],
            ["启动浏览器", "导航与 eval", "新 tab", "截图 PNG", "printToPDF"],
        )
        self.assertEqual(len(FakeCdpBrowser.instances), 1)
        br = FakeCdpBrowser.instances[0]
        self.assertEqual(br.chrome, r"C:\browser.exe")
        # 只打开本地 data: 页（零网络）
        self.assertTrue(br.navigated)
        for url in br.navigated:
            self.assertTrue(url.startswith("data:text/html;base64,"), url)
        # 浏览器 profile/日志落自检工作目录（按 job.id 安全后缀隔离）
        work = self.logs / "cdp-selftest-work-job-selftest-cdp"
        self.assertEqual(br.log_file, work / "browser.log")
        self.assertTrue(br.strict, "自检必须使用 strict=True 的 CdpBrowser")
        # Page.captureScreenshot 的 clip 必须带 scale（缺省会报 Invalid parameters）
        self.assertEqual(
            br.clip, {"x": 0, "y": 0, "width": 800, "height": 600, "scale": 1}
        )
        # 截图/PDF 产物写日志目录且内容校验通过
        png_path = Path(result["png"])
        pdf_path = Path(result["pdf"])
        self.assertEqual(png_path, self.logs / "cdp-selftest.png")
        self.assertEqual(pdf_path, self.logs / "cdp-selftest.pdf")
        self.assertEqual(png_path.read_bytes(), PNG_BYTES)
        self.assertEqual(pdf_path.read_bytes(), PDF_BYTES)
        self.assertEqual(result["png_bytes"], len(PNG_BYTES))
        self.assertEqual(result["pdf_bytes"], len(PDF_BYTES))
        # 新 tab 导航 + eval，并在收尾关闭
        self.assertIsNotNone(br.tab)
        tab_navs = [
            params for method, params in br.tab.calls if method == "Page.navigate"
        ]
        self.assertEqual(len(tab_navs), 1)
        self.assertTrue(
            tab_navs[0]["url"].startswith("data:text/html;base64,"), tab_navs[0]
        )
        self.assertEqual(br.close_tab_calls, 1)
        self.assertTrue(br.closed)
        # 成功路径清理临时 Chrome profile，但保留日志与 PNG/PDF 产物
        self.assertFalse((work / ".chrome-official-tmp").exists())
        self.assertTrue((work / "browser.log").exists())
        self.assertTrue(png_path.exists())
        self.assertTrue(pdf_path.exists())
        # 进度 5 步 + 日志
        self.assertEqual([p[0] for p in self.job.progress], [1, 2, 3, 4, 5])
        self.assertEqual([p[1] for p in self.job.progress], [5] * 5)
        log_text = "\n".join(self.job.logs)
        self.assertIn("CDP 渲染自检", log_text)
        self.assertIn("截图 PNG：OK", log_text)
        self.assertEqual(len(self.job.hooks), 1)
        # 纯 Python 实现不再经 goexport
        self.assertNotIn("goexport", sys.modules)

    def test_用户指定浏览器优先(self):
        result, detect = self._run(chosen=r"D:\company\360ent.exe")
        detect.assert_not_called()
        self.assertEqual(result["browser"], r"D:\company\360ent.exe")

    def test_无浏览器时明确报错且不启动(self):
        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils,
                "detect_chrome",
                side_effect=RuntimeError("未找到浏览器：请在设置页手动选择"),
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", FakeCdpBrowser),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                selftest.run_cdp(self.job, {})
        self.assertIn("未找到可用浏览器", str(ctx.exception))
        self.assertEqual(FakeCdpBrowser.instances, [])
        self.assertEqual(self.job.progress, [])

    def test_截图不是png时报错并关闭浏览器(self):
        _reset(fail_screenshot=True)
        with self.assertRaises(RuntimeError) as ctx:
            self._run()
        self.assertIn("截图", str(ctx.exception))
        self.assertTrue(FakeCdpBrowser.instances[0].closed)
        self.assertEqual(FakeCdpBrowser.instances[0].close_tab_calls, 1)

    def test_printToPDF失败时报错并关闭浏览器(self):
        class BadTab(FakeTab):
            def call(self, method, params=None, timeout=120):
                if method == "Page.printToPDF":
                    return {"id": 1, "result": {"data": ""}}
                return super().call(method, params, timeout)

        class BadBrowser(FakeCdpBrowser):
            def new_tab(self):
                self.tab = BadTab()
                return {"ws": None, "call": self.tab.call, "id": "tab-1"}

        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", BadBrowser),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                selftest.run_cdp(self.job, {})
        self.assertIn("printToPDF", str(ctx.exception))
        self.assertTrue(BadBrowser.instances[0].closed)

    def test_取消钩子关闭浏览器(self):
        result, _ = self._run()
        self.assertTrue(result["ok"])
        hook = self.job.hooks[0]
        hook()  # 幂等：成功后再次取消钩子不得抛错
        self.assertTrue(FakeCdpBrowser.instances[0].closed)

    def test_失败时保留profile供诊断(self):
        _reset(fail_screenshot=True)
        with self.assertRaises(RuntimeError):
            self._run()
        work = self.logs / "cdp-selftest-work-job-selftest-cdp"
        self.assertTrue((work / ".chrome-official-tmp").exists())
        self.assertTrue((work / "browser.log").exists())

    def test_不同job的workdir隔离且仅清本job的profile(self):
        job_a = FakeJob()
        job_a.id = "job/a 1"
        job_b = FakeJob()
        job_b.id = "job/b 2"
        work_a = self.logs / "cdp-selftest-work-job_a_1"
        work_b = self.logs / "cdp-selftest-work-job_b_2"
        self.assertNotEqual(work_a, work_b)

        _reset(fail_screenshot=True)
        with self.assertRaises(RuntimeError):
            self._run(job=job_a)
        self.assertTrue((work_a / ".chrome-official-tmp").exists())
        self.assertFalse(work_b.exists())

        _reset()
        result, _ = self._run(job=job_b)
        self.assertTrue(result["ok"])
        self.assertFalse((work_b / ".chrome-official-tmp").exists())
        # 只清本 job profile：job A 的失败现场仍在
        self.assertTrue((work_a / ".chrome-official-tmp").exists())
        self.assertTrue((work_a / "browser.log").exists())

    def test_取消中断后以Cancelled收尾而非failed(self):
        class CancelBrowser(FakeCdpBrowser):
            job = None

            def navigate(self, url):
                type(self).job.cancel()
                raise RuntimeError("websocket closed by cancel")

        CancelBrowser.job = self.job
        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", CancelBrowser),
        ):
            with self.assertRaises(jobs.Cancelled):
                selftest.run_cdp(self.job, {})
        self.assertTrue(self.job.cancelled)
        self.assertTrue(CancelBrowser.instances[0].closed)
        work = self.logs / "cdp-selftest-work-job-selftest-cdp"
        self.assertFalse((work / ".chrome-official-tmp").exists())

    def test_新tab的eval错误响应进入错误消息(self):
        class ErrorTab(FakeTab):
            def call(self, method, params=None, timeout=120):
                if method == "Runtime.evaluate":
                    return {
                        "id": 1,
                        "error": {"code": -32000, "message": "Cannot find context"},
                    }
                return super().call(method, params, timeout)

        class ErrorBrowser(FakeCdpBrowser):
            def new_tab(self):
                self.tab = ErrorTab()
                return {"ws": None, "call": self.tab.call, "id": "tab-1"}

        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", ErrorBrowser),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                selftest.run_cdp(self.job, {})
        self.assertIn("新 tab", str(ctx.exception))
        self.assertIn("Cannot find context", str(ctx.exception))
        self.assertTrue(ErrorBrowser.instances[0].closed)

    def test_新tab的navigate错误响应进入错误消息(self):
        class NavErrorTab(FakeTab):
            def call(self, method, params=None, timeout=120):
                if method == "Page.navigate":
                    return {
                        "id": 1,
                        "error": {"code": -32000, "message": "Frame not found"},
                    }
                return super().call(method, params, timeout)

        class NavErrorBrowser(FakeCdpBrowser):
            def new_tab(self):
                self.tab = NavErrorTab()
                return {"ws": None, "call": self.tab.call, "id": "tab-1"}

        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", NavErrorBrowser),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                selftest.run_cdp(self.job, {})
        self.assertIn("新 tab", str(ctx.exception))
        self.assertIn("Frame not found", str(ctx.exception))

    def test_浏览器构造失败明确报错(self):
        class BoomBrowser:
            def __init__(self, *a, **kw):
                raise RuntimeError("Chrome 丢失")

        with (
            mock.patch.object(selftest._config, "load", return_value=self.cfg),
            mock.patch.object(selftest._config, "render_cfg", return_value={}),
            mock.patch.object(
                selftest.platform_utils, "detect_chrome", return_value=r"C:\browser.exe"
            ),
            mock.patch.object(selftest.state, "logs_dir", return_value=self.logs),
            mock.patch.object(official_track, "CdpBrowser", BoomBrowser),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                selftest.run_cdp(self.job, {})
        self.assertIn("启动浏览器失败", str(ctx.exception))
        self.assertIn("Chrome 丢失", str(ctx.exception))
        self.assertEqual(self.job.progress, [])


if __name__ == "__main__":
    unittest.main()
