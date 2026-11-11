# -*- coding: utf-8 -*-
"""openPath 目录白名单的集成回归。

覆盖交叉复核问题：
- 任务档案窗口固定取「最近 200 份」时，30 天保留期内但排序靠后的旧记录目录打不开；
  窗口必须与 30 天保留期一致，且能按 job_id 精确定位该次任务的输出目录
- U 盘卷标比较大小写敏感（配置 GLKVM、系统挂载成 glkvm 时整块盘被拒）
"""

import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import rpc  # noqa: E402
import state  # noqa: E402


class OpenPathTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {
                "LOCALAPPDATA": str(self.tmpdir / "state"),
                "XDG_DATA_HOME": str(self.tmpdir / "state"),
                "UVMP_TOOLKIT_CONFIG": str(self.tmpdir / "config.json"),
            },
        )
        self._env.start()
        self.addCleanup(self._env.stop)
        self.server = rpc.RpcServer()

    def _archive(self, job_id, outdir, age_days=0.0):
        outdir.mkdir(parents=True, exist_ok=True)
        path = state.jobs_dir() / (job_id + ".json")
        state.write_json(
            path,
            {"id": job_id, "result": {"dir": str(outdir)}},
        )
        stamp = time.time() - age_days * 86400
        os.utime(path, (stamp, stamp))
        return path


class ArchiveWindowTest(OpenPathTestCase):
    def test_超过200份档案后旧记录目录仍可打开(self):
        """30 天保留期内的旧任务不受"最近 200 份"这个任意上限影响。"""
        oldest = self.tmpdir / "output" / "oldest"
        self._archive("20260101000000-aaaaaa", oldest, age_days=20)
        # 造 260 份更新的档案，把目标挤到 200 名之外
        for i in range(260):
            outdir = self.tmpdir / "output" / ("new-%03d" % i)
            self._archive("2026020100%04d-bbbbbb" % i, outdir, age_days=1)
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(oldest)})
        opener.assert_called_once()

    def test_超出保留期的档案不再放行(self):
        """白名单窗口与 30 天保留期一致：过期档案不进入白名单。"""
        stale = self.tmpdir / "output" / "stale"
        self._archive("20250101000000-cccccc", stale, age_days=45)
        with mock.patch.object(rpc.platform_utils, "open_path", return_value=True):
            with self.assertRaises(RuntimeError):
                self.server.m_openPath({"path": str(stale)})

    def test_按job_id精确定位旧任务输出目录(self):
        """前端带上 job_id 时直接读这一份档案，不受时间窗口限制。"""
        old = self.tmpdir / "output" / "by-job"
        self._archive("20250101000000-dddddd", old, age_days=400)
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath(
                {"path": str(old), "job_id": "20250101000000-dddddd"}
            )
        opener.assert_called_once()

    def test_非法job_id不影响按档案窗口判定(self):
        target = self.tmpdir / "output" / "normal"
        self._archive("20260101000000-eeeeee", target, age_days=1)
        with mock.patch.object(rpc.platform_utils, "open_path", return_value=True):
            self.server.m_openPath({"path": str(target), "job_id": "../config"})
            with self.assertRaises(RuntimeError):
                self.server.m_openPath(
                    {
                        "path": str(self.tmpdir / "output" / "missing"),
                        "job_id": "../config",
                    }
                )


class VolumeLabelCaseTest(OpenPathTestCase):
    def _cfg(self, label):
        (self.tmpdir / "config.json").write_text(
            json.dumps({"usb": {"label": label}}), encoding="utf-8"
        )

    def test_卷标大小写不同仍视为同一U盘(self):
        self._cfg("GLKVM")
        for path in (
            "/media/user/glkvm",
            "/media/user/GlKvM/导出",
            "/Volumes/glkvm",
        ):
            self.assertTrue(
                rpc.RpcServer._is_removable_mount(Path(path)), path
            )

    def test_标签大小写不同也匹配已挂载卷(self):
        self._cfg("glkvm")
        self.assertTrue(
            rpc.RpcServer._is_removable_mount(Path("/media/user/GLKVM/导出"))
        )

    def test_卷标不同与非移动介质仍然拒绝(self):
        self._cfg("GLKVM")
        self.assertFalse(rpc.RpcServer._is_removable_mount(Path("/Volumes/Other")))
        self.assertFalse(
            rpc.RpcServer._is_removable_mount(Path("/tmp/glkvm")),
            "非可移动介质根目录下的同名目录不能放行",
        )
        self.assertFalse(rpc.RpcServer._is_removable_mount(Path("/Volumes/GLKVM2")))


if __name__ == "__main__":
    unittest.main()
