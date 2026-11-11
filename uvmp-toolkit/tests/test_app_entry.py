# -*- coding: utf-8 -*-
"""app 入口测试：无头执行失败不上 stdout 噪声、失败原因走 stderr、退出码正确。"""

import contextlib
import io
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import app  # noqa: E402


class HeadlessRunTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": self._tmp.name, "XDG_DATA_HOME": self._tmp.name},
        )
        env.start()
        self.addCleanup(env.stop)

    def _backend(self, outcome):
        class Backend:
            def run(self, job, params):
                if isinstance(outcome, BaseException):
                    raise outcome
                return outcome

        return Backend()

    def _run(self, backend):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(app, "_load_backend", return_value=backend):
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                code = app._run_headless("demo", "测试任务", {})
        return code, out.getvalue(), err.getvalue()

    def test_成功返回0并打印结果(self):
        code, out, _err = self._run(self._backend({"count": 3}))
        self.assertEqual(0, code)
        self.assertIn("success", out)

    def test_失败返回1且原因在stderr(self):
        code, out, err = self._run(self._backend(RuntimeError("导出失败：无数据")))
        self.assertEqual(1, code)
        self.assertIn("failed", out)
        self.assertIn("导出失败：无数据", err)

    def test_后端SystemExit不逃逸出无头执行(self):
        code, _out, err = self._run(self._backend(SystemExit(5)))
        self.assertEqual(1, code)
        self.assertIn("SystemExit", err)


if __name__ == "__main__":
    unittest.main()
