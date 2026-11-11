# -*- coding: utf-8 -*-
"""受控浏览器启动/跳转安全测试。

覆盖审查问题：
- 启动后 CDP 未就绪时必须回收浏览器进程树，不留孤儿进程
- 已连接时不得覆盖用户正在看的页签（只在新页签或空白页打开启动网址）
- 默认 profile 目录按 CDP 端点（应用/实例）隔离，不与其他应用共用一套 Cookie
"""

import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import browser_automation  # noqa: E402
import state  # noqa: E402


class FakePage:
    def __init__(self, url):
        self.url = url
        self.goto_calls = []
        self.front_calls = 0

    def goto(self, url, **kwargs):
        self.goto_calls.append(url)
        self.url = url

    def bring_to_front(self):
        self.front_calls += 1


class FakeContext:
    def __init__(self, pages):
        self.pages = pages
        self.created = []

    def new_page(self):
        page = FakePage("about:blank")
        self.created.append(page)
        self.pages.append(page)
        return page


class FakeBrowser:
    def __init__(self, contexts):
        self.contexts = contexts


class FakePlaywright:
    def __init__(self, browser):
        self._browser = browser

    def connect_over_cdp(self, _url, timeout=None):
        return self._browser


class FakeSyncPlaywright:
    def __init__(self, browser):
        self._browser = browser

    def __enter__(self):
        return types.SimpleNamespace(
            chromium=FakePlaywright(self._browser),
        )

    def __exit__(self, *_exc):
        return False


def fake_playwright_modules(browser):
    """给 _open_start_url 注入假的 playwright.sync_api"""
    module = types.ModuleType("playwright.sync_api")
    module.sync_playwright = lambda: FakeSyncPlaywright(browser)
    pkg = types.ModuleType("playwright")
    pkg.sync_api = module
    return {"playwright": pkg, "playwright.sync_api": module}


class StartUrlTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)

    def _opts(self, start_url="http://portal.test/list"):
        return {
            "cdp_url": "http://127.0.0.1:9400",
            "port": 9400,
            "profile_dir": Path(self._tmp.name) / "profile",
            "step_timeout": 5,
            "start_url": start_url,
        }

    def test_已打开同源页签时只前置不覆盖(self):
        target = FakePage("http://portal.test/list?page=2")
        other = FakePage("http://portal.test/other")
        ctx = FakeContext([other, target])
        browser = FakeBrowser([ctx])
        with mock.patch.dict(sys.modules, fake_playwright_modules(browser)):
            browser_automation._open_start_url(self._opts())
        self.assertEqual([], target.goto_calls)
        self.assertEqual([], other.goto_calls)
        self.assertEqual([], ctx.created)
        self.assertEqual(1, target.front_calls)

    def test_无同源页签时不改写用户页签而是新建(self):
        user_page = FakePage("https://mail.test/inbox")
        ctx = FakeContext([user_page])
        browser = FakeBrowser([ctx])
        with mock.patch.dict(sys.modules, fake_playwright_modules(browser)):
            browser_automation._open_start_url(self._opts())
        self.assertEqual([], user_page.goto_calls, "不得覆盖用户正在看的页签")
        self.assertEqual(1, len(ctx.created))
        self.assertEqual(["http://portal.test/list"], ctx.created[0].goto_calls)

    def test_空白页优先复用(self):
        blank = FakePage("about:blank")
        ctx = FakeContext([blank])
        browser = FakeBrowser([ctx])
        with mock.patch.dict(sys.modules, fake_playwright_modules(browser)):
            browser_automation._open_start_url(self._opts())
        self.assertEqual(["http://portal.test/list"], blank.goto_calls)
        self.assertEqual([], ctx.created)


class LaunchFailureTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)

    def test_启动超时回收进程树(self):
        proc = mock.Mock(pid=4321)
        proc.poll.return_value = None
        cfg = {"render": {"chrome": str(Path(self._tmp.name) / "chrome")}}

        fake_time = mock.Mock()
        fake_time.monotonic.side_effect = [0, 100, 200, 300]
        with mock.patch.object(
            browser_automation.platform_utils, "detect_chrome", return_value="/x/chrome"
        ), mock.patch.object(
            browser_automation.subprocess, "Popen", return_value=proc
        ), mock.patch.object(
            browser_automation, "_page_list", side_effect=RuntimeError("未就绪")
        ), mock.patch.object(
            browser_automation, "_terminate_tree"
        ) as terminator, mock.patch.object(
            browser_automation, "_open_start_url"
        ), mock.patch.object(
            browser_automation.time, "monotonic", fake_time.monotonic
        ), mock.patch.object(browser_automation.time, "sleep"):
            with self.assertRaises(RuntimeError):
                browser_automation.launch(
                    cfg,
                    start_url="",
                    cdp_url="http://127.0.0.1:9400",
                    profile_dir=str(Path(self._tmp.name) / "profile"),
                )
        terminator.assert_called_once_with(proc)

    def test_已连接时不重复拉起进程(self):
        cfg = {"render": {"chrome": "/x/chrome"}}
        with mock.patch.object(
            browser_automation.platform_utils, "detect_chrome", return_value="/x/chrome"
        ), mock.patch.object(browser_automation.subprocess, "Popen") as popen, (
            mock.patch.object(browser_automation, "_page_list", return_value=[])
        ), mock.patch.object(browser_automation, "_open_start_url") as opener:
            result = browser_automation.launch(
                cfg,
                start_url="http://portal.test/list",
                cdp_url="http://127.0.0.1:9400",
                profile_dir=str(Path(self._tmp.name) / "profile"),
            )
        popen.assert_not_called()
        self.assertFalse(result["launched"])
        opener.assert_called_once()


class ProfileIsolationTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)

    def test_默认profile按端点隔离(self):
        shared = browser_automation.settings({})["profile_dir"]
        a = browser_automation._profile_for(
            {"profile_dir": shared, "port": 9400}
        )
        b = browser_automation._profile_for(
            {"profile_dir": shared, "port": 9401}
        )
        self.assertNotEqual(Path(a), Path(b))
        self.assertNotEqual(Path(a), Path(shared))
        self.assertIn("9400", str(a))
        self.assertIn("browser-automation", str(a))

    def test_显式profile原样使用(self):
        explicit = Path(self._tmp.name) / "自定义"
        self.assertEqual(
            explicit,
            Path(browser_automation._profile_for({"profile_dir": explicit, "port": 1})),
        )


if __name__ == "__main__":
    unittest.main()
