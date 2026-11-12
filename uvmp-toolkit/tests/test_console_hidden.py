# -*- coding: utf-8 -*-
"""运行期控制台子进程一律隐藏窗口（2026-11 实机反馈：客户端偶发弹 cmd/PowerShell 黑框）。

覆盖三层：
- platform_utils 的统一封装：CREATE_NO_WINDOW + STARTUPINFO/SW_HIDE，creationflags
  按位或合并（不能覆盖 CREATE_NEW_PROCESS_GROUP 等既有标志），非 Windows 原样透传；
- 各调用点改走封装：cluster ACL 的 powershell/icacls、defect_review 的进程定位/
  taskkill/OCR node、vehicle_export 的打印与 taskkill、dev_script 的脚本子进程；
- 反向约束：GUI 浏览器子进程只保留进程组标志，不得被 SW_HIDE 误藏窗口。

Windows 专属常量在 POSIX 上不存在，测试用假 STARTUPINFO / SW_HIDE 注入，
使同一批断言在任意平台都能跑（Windows CI 上则直接命中真实常量）。
"""

import importlib.util
import subprocess
import sys
import types
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import platform_utils  # noqa: E402

CREATE_NEW_PROCESS_GROUP = 0x00000200
CREATE_NO_WINDOW = 0x08000000


class _FakeStartupInfo:
    def __init__(self):
        self.dwFlags = 0
        self.wShowWindow = 1


class _OsProxy:
    """把 os 伪装成指定平台，其余属性仍走真实 os（不污染全局 os.name）。"""

    def __init__(self, name="nt", **overrides):
        self.name = name
        self.__dict__.update(overrides)

    def __getattr__(self, item):
        import os as _real_os

        return getattr(_real_os, item)


def _win_subprocess_patch():
    """补出 Windows 专属的 subprocess 常量，便于在任意平台断言隐藏参数。"""
    return (
        mock.patch.object(subprocess, "STARTUPINFO", _FakeStartupInfo, create=True),
        mock.patch.object(subprocess, "STARTF_USESHOWWINDOW", 0x1, create=True),
        mock.patch.object(subprocess, "SW_HIDE", 0, create=True),
        mock.patch.object(
            subprocess, "CREATE_NO_WINDOW", CREATE_NO_WINDOW, create=True
        ),
        # 该常量在 POSIX 上不存在；dev_script 用 getattr 取它，不补会退化成 0
        mock.patch.object(
            subprocess,
            "CREATE_NEW_PROCESS_GROUP",
            CREATE_NEW_PROCESS_GROUP,
            create=True,
        ),
    )


class _WinPatchMixin:
    def _enter_windows(self):
        for patcher in _win_subprocess_patch():
            patcher.start()
            self.addCleanup(patcher.stop)
        is_win = mock.patch.object(platform_utils, "is_windows", return_value=True)
        is_win.start()
        self.addCleanup(is_win.stop)


class MergeNoWindowTest(_WinPatchMixin, unittest.TestCase):
    """封装本身：按位或合并 + 非 Windows 零影响。"""

    def test_非Windows不注入任何参数(self):
        with mock.patch.object(platform_utils, "is_windows", return_value=False):
            got = platform_utils.merge_no_window({"creationflags": CREATE_NEW_PROCESS_GROUP})
        self.assertEqual({"creationflags": CREATE_NEW_PROCESS_GROUP}, got)
        self.assertNotIn("startupinfo", got)

    def test_非Windows返回新字典不改入参(self):
        original = {"timeout": 5}
        with mock.patch.object(platform_utils, "is_windows", return_value=False):
            got = platform_utils.merge_no_window(original)
        got["timeout"] = 9
        self.assertEqual(5, original["timeout"])

    def test_Windows按位或保留既有进程组标志(self):
        self._enter_windows()
        got = platform_utils.merge_no_window({"creationflags": CREATE_NEW_PROCESS_GROUP})
        self.assertEqual(
            CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW, got["creationflags"]
        )
        # 关键回归：不能整体覆盖成单独的 CREATE_NO_WINDOW
        self.assertTrue(got["creationflags"] & CREATE_NEW_PROCESS_GROUP)
        self.assertTrue(got["creationflags"] & CREATE_NO_WINDOW)

    def test_Windows无既有标志时补齐隐藏标志(self):
        self._enter_windows()
        got = platform_utils.merge_no_window({})
        self.assertEqual(CREATE_NO_WINDOW, got["creationflags"])

    def test_Windows启动信息设SW_HIDE(self):
        self._enter_windows()
        si = platform_utils.merge_no_window({})["startupinfo"]
        self.assertTrue(si.dwFlags & subprocess.STARTF_USESHOWWINDOW)
        self.assertEqual(subprocess.SW_HIDE, si.wShowWindow)

    def test_Windows保留调用方startupinfo的其它字段(self):
        self._enter_windows()
        existing = _FakeStartupInfo()
        existing.dwFlags = 0x00000001  # STARTF_FORCEONFEEDBACK，示意「已有其它标志」
        existing.lpTitle = "keep-me"
        got = platform_utils.merge_no_window({"startupinfo": existing})
        self.assertIs(existing, got["startupinfo"])
        self.assertTrue(got["startupinfo"].dwFlags & subprocess.STARTF_USESHOWWINDOW)
        self.assertTrue(got["startupinfo"].dwFlags & 0x00000001)
        self.assertEqual("keep-me", got["startupinfo"].lpTitle)
        self.assertEqual(subprocess.SW_HIDE, got["startupinfo"].wShowWindow)


class HiddenWrappersTest(_WinPatchMixin, unittest.TestCase):
    """run_hidden / popen_hidden / run_taskkill 的实际调用形态。"""

    def test_run_hidden隐藏控制台且透传其余参数(self):
        self._enter_windows()
        fake = mock.Mock(returncode=0)
        with mock.patch.object(platform_utils.subprocess, "run", return_value=fake) as run:
            platform_utils.run_hidden(["powershell", "-Command", "x"], timeout=7)
        args, kwargs = run.call_args
        self.assertEqual(["powershell", "-Command", "x"], args[0])
        self.assertEqual(7, kwargs["timeout"])
        self.assertTrue(kwargs["creationflags"] & CREATE_NO_WINDOW)
        self.assertEqual(subprocess.SW_HIDE, kwargs["startupinfo"].wShowWindow)

    def test_popen_hidden合并进程组标志(self):
        self._enter_windows()
        with mock.patch.object(
            platform_utils.subprocess, "Popen", return_value=mock.Mock()
        ) as popen:
            platform_utils.popen_hidden(
                ["python", "x.py"], creationflags=CREATE_NEW_PROCESS_GROUP
            )
        kwargs = popen.call_args[1]
        self.assertEqual(
            CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW, kwargs["creationflags"]
        )
        self.assertEqual(subprocess.SW_HIDE, kwargs["startupinfo"].wShowWindow)

    def test_popen_hidden可只抑制控制台不动GUI窗口(self):
        self._enter_windows()
        with mock.patch.object(
            platform_utils.subprocess, "Popen", return_value=mock.Mock()
        ) as popen:
            platform_utils.popen_hidden(["electron.exe", "x"], hide_window=False)
        kwargs = popen.call_args[1]
        self.assertTrue(kwargs["creationflags"] & CREATE_NO_WINDOW)
        # 不设 STARTUPINFO：nCmdShow 仍交给 GUI 进程自己决定
        self.assertNotIn("startupinfo", kwargs)

    def test_run_taskkill隐藏执行并返回是否成功(self):
        self._enter_windows()
        with mock.patch.object(
            platform_utils.subprocess, "run", return_value=mock.Mock(returncode=0)
        ) as run:
            self.assertTrue(platform_utils.run_taskkill(4321))
        args, kwargs = run.call_args
        self.assertEqual(["taskkill", "/F", "/T", "/PID", "4321"], args[0])
        self.assertTrue(kwargs["creationflags"] & CREATE_NO_WINDOW)
        self.assertEqual(subprocess.SW_HIDE, kwargs["startupinfo"].wShowWindow)

    def test_run_taskkill失败返回False(self):
        self._enter_windows()
        with mock.patch.object(
            platform_utils.subprocess, "run", return_value=mock.Mock(returncode=1)
        ):
            self.assertFalse(platform_utils.run_taskkill(4321))

    def test_run_taskkill非Windows不启动进程(self):
        with mock.patch.object(platform_utils, "is_windows", return_value=False):
            with mock.patch.object(platform_utils.subprocess, "run") as run:
                self.assertFalse(platform_utils.run_taskkill(4321))
        run.assert_not_called()


def _load_module(name, relpath):
    spec = importlib.util.spec_from_file_location(name, str(ROOT / relpath))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class ClusterAclHiddenTest(_WinPatchMixin, unittest.TestCase):
    """集群 ACL：每次建令牌文件/启动都会起 powershell 或 icacls（高频，最容易闪窗）。"""

    def setUp(self):
        import cluster.service as service

        self.service = service
        service._WINDOWS_USER_SID = ""

    def test_取用户SID走隐藏封装(self):
        self._enter_windows()
        ok = mock.Mock(returncode=0, stdout="S-1-5-21-1\n", stderr="")
        with mock.patch.object(
            self.service._platform_utils, "run_hidden", return_value=ok
        ) as run_hidden:
            sid = self.service._windows_user_sid()
        self.assertEqual("S-1-5-21-1", sid)
        args, kwargs = run_hidden.call_args
        self.assertEqual("powershell", args[0][0])
        self.assertEqual(15, kwargs["timeout"])

    def test_ACL脚本走隐藏封装(self):
        self._enter_windows()
        ok = mock.Mock(returncode=0, stdout="UVMP-ACL-OK\n", stderr="")
        with mock.patch.object(
            self.service._platform_utils, "run_hidden", return_value=ok
        ) as run_hidden:
            self.assertTrue(self.service._acl_via_powershell(Path("/x/token"), "S-1-5-18"))
        args, kwargs = run_hidden.call_args
        self.assertEqual("powershell", args[0][0])
        self.assertEqual(30, kwargs["timeout"])

    def test_icacls兜底走隐藏封装(self):
        self._enter_windows()
        ok = mock.Mock(returncode=0, stdout="", stderr="")
        with mock.patch.object(
            self.service._platform_utils, "run_hidden", return_value=ok
        ) as run_hidden:
            self.assertTrue(self.service._acl_via_icacls(Path("/x/token"), "S-1-5-18"))
        args, kwargs = run_hidden.call_args
        self.assertEqual("icacls", args[0][0])
        self.assertIn("/inheritance:r", args[0])

    def test_隐藏封装抛错时仍按原口径降级(self):
        self._enter_windows()
        with mock.patch.object(
            self.service._platform_utils,
            "run_hidden",
            side_effect=OSError("boom"),
        ):
            self.assertFalse(self.service._acl_via_powershell(Path("/x/t"), "S-1-5-18"))
            self.assertFalse(self.service._acl_via_icacls(Path("/x/t"), "S-1-5-18"))


class DevScriptHiddenTest(_WinPatchMixin, unittest.TestCase):
    """脚本测试：既要隐藏控制台，又必须保留 CREATE_NEW_PROCESS_GROUP（取消靠它）。"""

    def setUp(self):
        self.backend = _load_module(
            "dev_script_backend_hidden_under_test",
            "toolkit/apps/dev_script/backend.py",
        )

    def _patch_nt_os(self):
        patcher = mock.patch.object(self.backend, "os", _OsProxy(name="nt"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_Windows同时具备进程组与隐藏标志(self):
        self._enter_windows()
        self._patch_nt_os()
        kwargs = self.backend._popen_kwargs()
        self.assertEqual(
            CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW, kwargs["creationflags"]
        )
        self.assertTrue(kwargs["creationflags"] & CREATE_NEW_PROCESS_GROUP)
        self.assertTrue(kwargs["creationflags"] & CREATE_NO_WINDOW)
        self.assertEqual(subprocess.SW_HIDE, kwargs["startupinfo"].wShowWindow)

    def test_非Windows参数保持原样(self):
        with mock.patch.object(platform_utils, "is_windows", return_value=False):
            kwargs = self.backend._popen_kwargs()
        self.assertEqual(subprocess.DEVNULL, kwargs["stdin"])
        self.assertEqual(subprocess.PIPE, kwargs["stdout"])
        self.assertEqual(subprocess.STDOUT, kwargs["stderr"])
        self.assertTrue(kwargs["start_new_session"])
        self.assertNotIn("creationflags", kwargs)
        self.assertNotIn("startupinfo", kwargs)

    def test_强杀走隐藏taskkill(self):
        self._patch_nt_os()
        proc = mock.Mock(pid=987)
        proc.poll.return_value = None
        with mock.patch.object(
            self.backend._platform_utils, "run_taskkill", return_value=True
        ) as taskkill:
            self.backend._signal_tree(proc, hard=True)
        taskkill.assert_called_once_with(987, timeout=5)
        proc.kill.assert_not_called()

    def test_强杀失败回退proc_kill(self):
        self._patch_nt_os()
        proc = mock.Mock(pid=987)
        proc.poll.return_value = None
        with mock.patch.object(
            self.backend._platform_utils, "run_taskkill", return_value=False
        ):
            self.backend._signal_tree(proc, hard=True)
        proc.kill.assert_called_once()

    def test_优雅阶段仍按进程组投递CTRL_BREAK(self):
        self._patch_nt_os()
        proc = mock.Mock(pid=987)
        proc.poll.return_value = None
        with mock.patch.object(self.backend.os, "kill") as kill, mock.patch.object(
            self.backend.signal, "CTRL_BREAK_EVENT", 1, create=True
        ):
            self.backend._signal_tree(proc, hard=False)
        kill.assert_called_once_with(987, 1)


class DefectReviewHiddenTest(_WinPatchMixin, unittest.TestCase):
    """机巡审核：无响应恢复要跑 PowerShell 定位进程并 taskkill，OCR 要起 node。"""

    def setUp(self):
        import defect_review_trial as trial

        self.trial = trial

    def _patch_nt_os(self, module):
        patcher = mock.patch.object(module, "os", _OsProxy(name="nt"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_按端口定位进程走隐藏封装(self):
        self._patch_nt_os(self.trial)
        fake = mock.Mock(returncode=0, stdout="111\n222\n")
        with mock.patch.object(
            self.trial, "_run_hidden", return_value=fake
        ) as run_hidden:
            pids = self.trial._browser_pids_for_port(9333)
        self.assertEqual([111, 222], pids)
        args, kwargs = run_hidden.call_args
        self.assertEqual("powershell", args[0][0])
        self.assertIn("--remote-debugging-port=9333", args[0][3])

    def test_结束进程走隐藏taskkill(self):
        self._patch_nt_os(self.trial)
        logs = []
        with mock.patch.object(self.trial, "_taskkill_tree") as taskkill:
            self.trial._terminate_browser_pids([111, 222], logs.append)
        self.assertEqual(
            [mock.call(111, timeout=10), mock.call(222, timeout=10)],
            taskkill.call_args_list,
        )
        self.assertTrue(logs)

    def test_OCR助手走隐藏封装(self):
        source = (ROOT / "defect_review_trial.py").read_text(encoding="utf-8")
        self.assertIn("proc = _run_hidden(\n                [node, helper]", source)
        self.assertNotIn("proc = subprocess.run(\n                [node, helper]", source)

    def test_run_hidden优先使用platform_utils(self):
        marker = mock.Mock()
        with mock.patch.object(self.trial, "platform_utils", marker):
            marker.run_hidden.return_value = "ok"
            got = self.trial._run_hidden(["x"], timeout=3)
        self.assertEqual("ok", got)
        marker.run_hidden.assert_called_once_with(["x"], timeout=3)

    def test_run_hidden无toolkit时退化为本地隐藏实现(self):
        self._enter_windows()
        self._patch_nt_os(self.trial)
        with mock.patch.object(self.trial, "platform_utils", None):
            with mock.patch.object(
                self.trial.subprocess, "run", return_value=mock.Mock()
            ) as run:
                self.trial._run_hidden(["powershell", "-Command", "x"])
        kwargs = run.call_args[1]
        self.assertTrue(kwargs["creationflags"] & CREATE_NO_WINDOW)
        self.assertEqual(subprocess.SW_HIDE, kwargs["startupinfo"].wShowWindow)

    def test_非Windows不注入隐藏参数(self):
        with mock.patch.object(self.trial, "platform_utils", None):
            with mock.patch.object(
                self.trial.subprocess, "run", return_value=mock.Mock()
            ) as run:
                self.trial._run_hidden(["ls"], timeout=2)
        kwargs = run.call_args[1]
        self.assertNotIn("creationflags", kwargs)
        self.assertNotIn("startupinfo", kwargs)
        self.assertEqual(2, kwargs["timeout"])


class VehicleExportHiddenTest(_WinPatchMixin, unittest.TestCase):
    """打印链路：taskkill 隐藏；Electron/Chrome 只抑制控制台、不设 SW_HIDE。"""

    @classmethod
    def setUpClass(cls):
        try:
            import vehicle_export as ve
        except (ImportError, SystemExit) as exc:  # 本地无 requests 时按原口径跳过
            raise unittest.SkipTest("vehicle_export 依赖不可用: %s" % exc)
        cls.ve = ve

    def _patch_nt_os(self):
        patcher = mock.patch.object(self.ve, "os", _OsProxy(name="nt"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def _fake_pu(self, **kwargs):
        pu = mock.Mock()
        for key, value in kwargs.items():
            setattr(pu, key, value)
        return pu

    def test_taskkill走隐藏封装(self):
        self._patch_nt_os()
        proc = mock.Mock(pid=555)
        proc.poll.return_value = None
        pu = self._fake_pu(run_taskkill=mock.Mock(return_value=True))
        with mock.patch.object(self.ve, "_platform_utils", return_value=pu):
            self.ve._kill_process_tree(proc)
        pu.run_taskkill.assert_called_once_with(555, timeout=15)

    def test_taskkill失败时退回proc_kill(self):
        self._patch_nt_os()
        proc = mock.Mock(pid=555)
        proc.poll.return_value = None
        pu = self._fake_pu(run_taskkill=mock.Mock(return_value=False))
        with mock.patch.object(self.ve, "_platform_utils", return_value=pu):
            self.ve._kill_process_tree(proc)
        proc.kill.assert_called_once()

    def test_打印子进程只抑制控制台不隐藏GUI窗口(self):
        self._patch_nt_os()
        proc = mock.Mock()
        proc.communicate.return_value = (b"out", b"err")
        proc.returncode = 0
        pu = self._fake_pu(popen_hidden=mock.Mock(return_value=proc))
        with mock.patch.object(self.ve, "_platform_utils", return_value=pu):
            code, out, err = self.ve._run_print(["electron.exe", "x"], 5)
        self.assertEqual((0, b"out", b"err"), (code, out, err))
        args, kwargs = pu.popen_hidden.call_args
        self.assertEqual(["electron.exe", "x"], args[0])
        self.assertFalse(kwargs["hide_window"], "GUI 渲染进程不能被 SW_HIDE 藏窗口")
        self.assertEqual(subprocess.PIPE, kwargs["stdout"])

    def test_无toolkit时taskkill仍带隐藏标志(self):
        self._enter_windows()
        self._patch_nt_os()
        proc = mock.Mock(pid=555)
        proc.poll.return_value = None
        with mock.patch.object(self.ve, "_platform_utils", return_value=None):
            with mock.patch.object(
                self.ve.subprocess, "run", return_value=mock.Mock(returncode=0)
            ) as run:
                self.ve._kill_process_tree(proc)
        self.assertTrue(run.call_args[1]["creationflags"] & CREATE_NO_WINDOW)


class GuiBrowserNotHiddenTest(_WinPatchMixin, unittest.TestCase):
    """反向约束：GUI 浏览器子进程必须保持正常窗口。"""

    def test_浏览器启动不带隐藏标志(self):
        import tempfile

        import browser_automation

        self._enter_windows()
        fake = mock.Mock()
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(
                browser_automation.platform_utils,
                "detect_chrome",
                return_value="/x/chrome",
            ), mock.patch.object(
                browser_automation.subprocess, "Popen", return_value=fake
            ) as popen, mock.patch.object(
                browser_automation, "status", return_value={"connected": False}
            ), mock.patch.object(
                browser_automation, "_page_list", return_value=[{"type": "page"}]
            ), mock.patch.object(
                browser_automation, "_open_start_url"
            ), mock.patch.object(
                browser_automation, "os", _OsProxy(name="nt")
            ):
                browser_automation.launch(
                    {"render": {}},
                    start_url="http://portal.test/list",
                    cdp_url="http://127.0.0.1:9400",
                    profile_dir=str(Path(tmp) / "profile"),
                )
        kwargs = popen.call_args[1]
        self.assertEqual(CREATE_NEW_PROCESS_GROUP, kwargs["creationflags"])
        self.assertFalse(kwargs["creationflags"] & CREATE_NO_WINDOW)
        self.assertNotIn("startupinfo", kwargs)


if __name__ == "__main__":
    unittest.main()
