# -*- coding: utf-8 -*-
"""树障/交跨 replace：备份失败不得阻断覆盖；本地写失败提示内网表而不是 U 盘权限。"""

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

import app  # noqa: E402


class ReplaceRegressionTest(unittest.TestCase):
    def _backend(self, app_id):
        return app._load_backend(app_id)

    def test_replace_overwrites_and_removes_source(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                src = usb / "new.xlsx"
                local.write_bytes(b"old")
                src.write_bytes(b"new")
                logs = []
                result = backend._replace(local, usb, "new.xlsx", logs.append)
                self.assertEqual(local.read_bytes(), b"new")
                self.assertFalse(src.exists())
                self.assertEqual(result["action"], "replace")
                self.assertTrue(any("已用 U 盘新表覆盖" in line for line in logs))

    def test_backup_failure_does_not_block_replace(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"old")
                (usb / "new.xlsx").write_bytes(b"new")
                # .bak 为目录：备份必然失败，但覆盖仍须继续。
                (root / "inner.xlsx.bak").mkdir()
                backend._replace(local, usb, "new.xlsx", lambda *_: None)
                self.assertEqual(local.read_bytes(), b"new")

    def test_copystat_eperm_does_not_block_replace(self):
        """真实失败日志：copy2 的 copystat 在 SMB/FUSE/FAT 上 EPERM，覆盖仍须完成。"""
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"old")
                (usb / "new.xlsx").write_bytes(b"new")
                with mock.patch.object(
                    backend.shutil,
                    "copystat",
                    side_effect=PermissionError(1, "Operation not permitted"),
                ):
                    backend._replace(local, usb, "new.xlsx", lambda *_: None)
                self.assertEqual(local.read_bytes(), b"new")

    def test_local_path_same_as_usb_source_rejected(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = usb / "table.xlsx"
                local.write_bytes(b"new")
                with self.assertRaises(RuntimeError) as ctx:
                    backend._replace(local, usb, "table.xlsx", lambda *_: None)
                self.assertIn("不能指向 U 盘", str(ctx.exception))
                self.assertEqual(local.read_bytes(), b"new")

    def test_local_write_error_reports_local_path(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"old")
                (usb / "new.xlsx").write_bytes(b"new")
                real_copy = backend.shutil.copyfile

                def fake_copy(src, dst):
                    dst_s = str(dst)
                    if dst_s == str(local) or dst_s.endswith(".sync-tmp"):
                        raise PermissionError(13, "Permission denied", dst_s)
                    return real_copy(src, dst)

                with mock.patch.object(
                    backend.shutil, "copyfile", side_effect=fake_copy
                ):
                    with self.assertRaises(RuntimeError) as ctx:
                        backend._replace(local, usb, "new.xlsx", lambda *_: None)
                msg = str(ctx.exception)
                self.assertIn(str(local), msg)
                self.assertNotIn("U盘挂载点", msg)
                # 失败时本地旧内容保留，不能写坏。
                self.assertEqual(local.read_bytes(), b"old")


if __name__ == "__main__":
    unittest.main()
