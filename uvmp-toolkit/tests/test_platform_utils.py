# -*- coding: utf-8 -*-
"""platform_utils 单元测试：Windows 控制台子进程必须隐藏。

usb-agent 每隔几秒用 PowerShell 扫一次 U 盘；若未带 CREATE_NO_WINDOW，从 GUI 拉起
的核心（无控制台）会新分配可见控制台，表现为桌面不停闪 PowerShell 窗口。
"""

import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import platform_utils  # noqa: E402


class NoWindowTest(unittest.TestCase):
    def test_non_windows_has_no_extra_kwargs(self):
        with mock.patch.object(platform_utils, "is_windows", return_value=False):
            self.assertEqual(platform_utils._no_window_kwargs(), {})

    @unittest.skipUnless(
        sys.platform.startswith("win"), "仅 Windows 有 CREATE_NO_WINDOW"
    )
    def test_windows_kwargs_hide_console(self):
        with mock.patch.object(platform_utils, "is_windows", return_value=True):
            opts = platform_utils._no_window_kwargs()
        self.assertEqual(opts["creationflags"], platform_utils.CREATE_NO_WINDOW)
        self.assertTrue(opts["startupinfo"].dwFlags & subprocess.STARTF_USESHOWWINDOW)
        self.assertEqual(opts["startupinfo"].wShowWindow, subprocess.SW_HIDE)

    @unittest.skipUnless(
        sys.platform.startswith("win"), "仅 Windows 有 CREATE_NO_WINDOW"
    )
    def test_find_usb_windows_runs_powershell_hidden(self):
        fake = mock.Mock(returncode=1, stdout="", stderr="")
        with mock.patch.object(
            platform_utils.subprocess, "run", return_value=fake
        ) as run:
            platform_utils._find_usb_windows("GLKVM", lambda _m: None)
        args, kwargs = run.call_args
        self.assertEqual(args[0][0], "powershell")
        self.assertEqual(kwargs["creationflags"], platform_utils.CREATE_NO_WINDOW)
        self.assertEqual(kwargs["startupinfo"].wShowWindow, subprocess.SW_HIDE)


if __name__ == "__main__":
    unittest.main()
