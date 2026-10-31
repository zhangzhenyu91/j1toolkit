# -*- coding: utf-8 -*-
"""树障/交跨定时轮询静默：U盘不在或插入无指令时不创建任务/日志。

覆盖 has_pending_command 的三种结果与 CLI tick 快速路径（不调用 _run_headless）。
"""

import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT), str(ROOT / "pylib")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import app  # noqa: E402


class PendingCommandTest(unittest.TestCase):
    def _backend(self, app_id):
        return app._load_backend(app_id)

    def test_usb_absent_is_false(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with mock.patch.object(
                backend.platform_utils, "find_usb_mount", return_value=None
            ):
                self.assertFalse(
                    backend.has_pending_command({"usb": {"label": "GLKVM"}})
                )

    def test_usb_without_command_is_false(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                with mock.patch.object(
                    backend.platform_utils, "find_usb_mount", return_value=d
                ):
                    self.assertFalse(
                        backend.has_pending_command({"usb": {"label": "GLKVM"}})
                    )

    def test_usb_with_command_is_true(self):
        for app_id, cmd_name in (
            ("tree_sync", "tree_sync_cmd.json"),
            ("crossing_sync", "crossing_sync_cmd.json"),
        ):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                (Path(d) / cmd_name).write_text("{}", encoding="utf-8")
                with mock.patch.object(
                    backend.platform_utils, "find_usb_mount", return_value=d
                ):
                    self.assertTrue(
                        backend.has_pending_command({"usb": {"label": "GLKVM"}})
                    )


class QuietCliTest(unittest.TestCase):
    def _args(self):
        return SimpleNamespace(
            tick=True, nightly=False, if_due=False, quiet=True, date=""
        )

    def test_tree_tick_without_command_skips_job(self):
        fake = SimpleNamespace(has_pending_command=lambda cfg: False)
        with mock.patch.object(app, "_load_backend", return_value=fake):
            with mock.patch.object(app, "_run_headless") as run:
                self.assertEqual(app.cmd_tree_sync({}, self._args()), 0)
                run.assert_not_called()

    def test_crossing_tick_without_command_skips_job(self):
        fake = SimpleNamespace(has_pending_command=lambda cfg: False)
        with mock.patch.object(app, "_load_backend", return_value=fake):
            with mock.patch.object(app, "_run_headless") as run:
                self.assertEqual(app.cmd_crossing_sync({}, self._args()), 0)
                run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
