# -*- coding: utf-8 -*-
"""rpc 服务入口安全测试。

覆盖审查问题：
- m_runJob 用 manager.active 判定实例占用（不再遍历私有 _jobs 字典）
- openPath 只允许打开客户端产物目录/状态目录（拒绝任意路径）
- getJobLog 限长返回（超大日志只回尾部）
- 协议流独占 stdout：普通 print 走 stderr，不污染 JSON-RPC
"""

import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path, PurePosixPath, PureWindowsPath
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import rpc  # noqa: E402
import state  # noqa: E402


class RpcTestCase(unittest.TestCase):
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

    def _wait_terminal(self, job, timeout=10.0):
        """等后台任务线程收尾（含 _finalize 落盘）再退出用例：
        否则临时状态目录会在任务线程还在写文件时被清理。"""
        archive = state.jobs_dir() / (job.id + ".json")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if archive.exists() and job._log_fp.closed and job.finished_at:
                time.sleep(0.05)  # 让 append_history 也落地
                return
            time.sleep(0.02)
        self.fail("任务未在 %.0f 秒内收尾" % timeout)


class RunJobActiveTest(RpcTestCase):
    def _fake_app(self):
        class Backend:
            def run(self, job, params):
                return {}

        return {"id": "demo", "name": "演示", "_backend": Backend()}

    def test_实例占用判定走manager_active(self):
        self.server._backends = {"demo": self._fake_app()}
        running = self.server.manager.create("demo", "演示")
        running.instance_id = "inst-1"
        running.status = "running"

        def cleanup():
            running._log_fp.close()
            state.unregister_active_log(running._log_path)

        self.addCleanup(cleanup)

        with self.assertRaises(RuntimeError):
            self.server.m_runJob(
                {"app_id": "demo", "params": {"instance_id": "inst-1"}}
            )
        # 另一个实例不受影响
        other = self.server.m_runJob(
            {"app_id": "demo", "params": {"instance_id": "inst-2"}}
        )
        self.assertTrue(other["job_id"])
        job = self.server.manager.get(other["job_id"])
        self._wait_terminal(job)

    def test_不再依赖私有jobs字典(self):
        self.server._backends = {"demo": self._fake_app()}
        from_ = []

        real_active = self.server.manager.active

        def spy(app_id, instance_id=""):
            from_.append((app_id, instance_id))
            return real_active(app_id, instance_id)

        with mock.patch.object(self.server.manager, "active", side_effect=spy):
            out = self.server.m_runJob(
                {"app_id": "demo", "params": {"instance_id": "inst-9"}}
            )
        self.assertEqual([("demo", "inst-9")], from_)
        job = self.server.manager.get(out["job_id"])
        self._wait_terminal(job)
        self.assertEqual("success", job.status)


class OpenPathTest(RpcTestCase):
    def test_拒绝状态目录之外的任意路径(self):
        outside = self.tmpdir / "evil.txt"
        outside.write_text("x", encoding="utf-8")
        with mock.patch.object(rpc.platform_utils, "open_path") as opener:
            with self.assertRaises(RuntimeError):
                self.server.m_openPath({"path": str(outside)})
        opener.assert_not_called()

    def test_允许打开状态目录内的产物(self):
        inside = state.logs_dir() / "20260101000000-01.log"
        inside.write_text("x", encoding="utf-8")
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            result = self.server.m_openPath({"path": str(inside)})
        self.assertEqual({"ok": True}, result)
        opener.assert_called_once()

    def test_允许打开任务档案里记录的输出目录(self):
        outdir = self.tmpdir / "output" / "批次"
        outdir.mkdir(parents=True)
        state.write_json(
            state.jobs_dir() / "20260101000000-01.json",
            {"id": "20260101000000-01", "result": {"dir": str(outdir)}},
        )
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(outdir)})
        opener.assert_called_once()

    def test_不存在的路径仍然拒绝(self):
        with self.assertRaises(RuntimeError):
            self.server.m_openPath({"path": str(self.tmpdir / "missing")})

    def test_允许打开配置里的同步表目录(self):
        table_dir = self.tmpdir / "内网表"
        table_dir.mkdir()
        (table_dir / "树木统计表.xlsx").write_text("x", encoding="utf-8")
        state_path = self.tmpdir / "config.json"
        state_path.write_text(
            json.dumps(
                {"tree_sync": {"local_path": str(table_dir / "树木统计表.xlsx")}}
            ),
            encoding="utf-8",
        )
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(table_dir)})
        opener.assert_called_once()

    def test_允许打开集群已登记的安装包(self):
        pkg = self.tmpdir / "更新包.deb"
        pkg.write_text("bin", encoding="utf-8")
        state.write_json(
            state.state_dir() / "cluster" / "update_packages.json",
            {"packages": [{"id": "x", "path": str(pkg)}]},
        )
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(pkg)})
        opener.assert_called_once()

    def test_卷标匹配的移动介质才放行(self):
        self.assertTrue(rpc.RpcServer._is_removable_mount(Path("/Volumes/GLKVM")))
        self.assertTrue(
            rpc.RpcServer._is_removable_mount(Path("/media/user/GLKVM/导出"))
        )
        self.assertFalse(rpc.RpcServer._is_removable_mount(Path("/tmp/GLKVM")))
        self.assertFalse(rpc.RpcServer._is_removable_mount(Path("/Volumes/Other")))

    @unittest.skipUnless(os.name == "nt", "盘符判定仅 Windows 有效")
    def test_Windows只放行盘符根(self):
        self.assertTrue(rpc.RpcServer._is_removable_mount(Path("D:\\")))
        self.assertFalse(rpc.RpcServer._is_removable_mount(Path("D:\\导出")))
        self.assertFalse(
            rpc.RpcServer._is_removable_mount(Path("C:\\Windows\\System32\\calc.exe"))
        )

    def test_盘符判定不依赖宿主平台(self):
        """Windows 分支只看路径 drive：用 PureWindowsPath 在任意宿主上验证同一口径，
        否则这些断言只在 Windows CI 上跑，本地回归发现不了 drive 判定被改坏。"""
        pw = PureWindowsPath
        self.assertTrue(rpc.RpcServer._is_removable_mount(pw("D:\\")))
        self.assertTrue(rpc.RpcServer._is_removable_mount(pw("D:/")))
        self.assertFalse(rpc.RpcServer._is_removable_mount(pw("D:\\导出")))
        self.assertFalse(
            rpc.RpcServer._is_removable_mount(pw("C:\\Windows\\System32\\calc.exe"))
        )
        # UNC 共享是网络位置，不是盘符根，不能凭前缀放行
        self.assertFalse(rpc.RpcServer._is_removable_mount(pw("\\\\server\\share")))

    def test_路径大小写不同仍视为同一目录(self):
        """Windows 文件系统不区分大小写：realpath 解析出的规范大小写与配置/档案里登记的
        大小写常不一致，前缀判断必须大小写不敏感，否则合法目录被判成越界。
        比较键跟随路径形态（PureWindowsPath 与带盘符的字符串），因此任意宿主都能覆盖。"""
        upper = rpc._path_key(PureWindowsPath("C:\\Output\\批次A"))
        lower = rpc._path_key(PureWindowsPath("c:\\output\\批次A\\结果.pdf"))
        self.assertEqual(upper, lower[: len(upper)])
        self.assertEqual(upper, rpc._path_key(PureWindowsPath("c:/OUTPUT/批次A")))
        # 档案/配置里登记的字符串路径同样按形态判：POSIX 宿主上也不能退化成
        # 一个不可拆分的部件，否则大小写与盘符都失配
        self.assertEqual(upper, rpc._path_key("c:/OUTPUT/批次A"))
        self.assertEqual(upper, rpc._path_key("C:\\Output\\批次A\\"))
        self.assertNotEqual(
            upper,
            rpc._path_key(PureWindowsPath("C:\\Output2")),
            "同前缀不同目录不能相等",
        )
        # POSIX 保持大小写敏感：Linux 的 /media/user 与 /media/USER 是两个目录。
        # 必须用 PurePosixPath 显式表达形态——Windows 宿主上 Path("/media/user")
        # 就是 WindowsPath，本就是大小写不敏感的 Windows 路径，断言会自相矛盾。
        user = rpc._path_key(PurePosixPath("/media/user"))
        self.assertEqual(
            user, rpc._path_key(PurePosixPath("/media/user/GLKVM"))[: len(user)]
        )
        self.assertNotEqual(user, rpc._path_key(PurePosixPath("/media/USER")))
        # 大小写不敏感不等于前缀不敏感：C:\Output2 不是 C:\Output 的子目录
        self.assertNotEqual(
            upper,
            rpc._path_key(PureWindowsPath("c:\\OUTPUT2\\批次A"))[: len(upper)],
        )
        self.assertEqual(
            upper,
            rpc._path_key(PureWindowsPath("c:\\OUTPUT\\批次A\\导出"))[: len(upper)],
            "大小写不同的真实子目录仍要放行",
        )
        # POSIX 形态里含冒号的普通文件名不能被当成盘符：`a:b` 在 POSIX 上是合法名字
        self.assertEqual(
            rpc._path_key(PurePosixPath("/tmp/a:b")), ("/", "tmp", "a:b")
        )

    def test_同前缀的兄弟目录仍在白名单之外(self):
        """伪造前缀 `Output2` 不能借 `Output` 的登记混进来：前缀按 parts 逐段比，
        不做字符串前缀匹配。"""
        allowed = self.tmpdir / "Output"
        allowed.mkdir()
        forged = self.tmpdir / "Output2"
        forged.mkdir()
        (forged / "伪造.txt").write_text("x", encoding="utf-8")
        state.write_json(
            state.jobs_dir() / "20260101000000-01.json",
            {"id": "20260101000000-01", "result": {"dir": str(allowed)}},
        )
        with mock.patch.object(rpc.platform_utils, "open_path") as opener:
            with self.assertRaises(RuntimeError):
                self.server.m_openPath({"path": str(forged)})
        opener.assert_not_called()
        # 同一份档案下真实子目录照旧放行，排除「一律拒绝」式的假通过
        inside = allowed / "批次A"
        inside.mkdir()
        with mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(inside)})
        opener.assert_called_once()

    @unittest.skipUnless(os.name == "nt", "大小写不敏感语义仅 Windows 文件系统成立")
    def test_大小写不同的真实目录仍能打开(self):
        """配置里登记规范大小写、界面传来的却是另一种大小写：Windows 上必须能打开。

        这是 2.3.44 构建失败的现场。realpath 在本机也会把两侧都规范化，若只靠它，
        这条用例即使去掉大小写不敏感比较也照样通过；因此这里让 realpath 原样返回，
        直接把「解析出的规范大小写 vs 配置里登记的大小写」这一差异暴露给比较键。
        """
        table_dir = self.tmpdir / "内网表" / "Output"
        table_dir.mkdir(parents=True)
        (table_dir / "树木统计表.xlsx").write_text("x", encoding="utf-8")
        (self.tmpdir / "config.json").write_text(
            json.dumps({"tree_sync": {"local_path": str(table_dir / "树木统计表.xlsx")}}),
            encoding="utf-8",
        )
        swapped = table_dir.parent / table_dir.name.swapcase()
        self.assertTrue(swapped.exists(), "NTFS 上大小写变体应指向同一目录")
        self.assertNotEqual(str(swapped), str(table_dir))
        with mock.patch.object(
            rpc.os.path, "realpath", side_effect=lambda p: str(p)
        ), mock.patch.object(
            rpc.platform_utils, "open_path", return_value=True
        ) as opener:
            self.server.m_openPath({"path": str(swapped)})
        opener.assert_called_once()


class JobLogLimitTest(RpcTestCase):
    def test_超大日志只返回尾部并标记截断(self):
        job_id = "20260101000000-01"
        p = state.logs_dir() / (job_id + ".log")
        # 显式 CRLF 字节：任务日志由文本模式写入，Windows 落盘就是 CRLF，
        # 尾部断言必须跨平台成立（不能只在 LF 宿主上碰巧通过）
        p.write_bytes(("A" * 400000 + "\r\n尾部标记\r\n").encode("utf-8"))
        out = self.server.m_getJobLog({"job_id": job_id})
        self.assertTrue(out["truncated"])
        self.assertLessEqual(len(out["text"]), rpc.JOB_LOG_MAX_CHARS)
        self.assertTrue(out["text"].endswith("尾部标记\n"))
        self.assertNotIn("\r", out["text"])

    def test_小日志完整返回且不标记截断(self):
        job_id = "20260101000000-02"
        p = state.logs_dir() / (job_id + ".log")
        p.write_text("短日志\n", encoding="utf-8")
        out = self.server.m_getJobLog({"job_id": job_id})
        self.assertEqual("短日志\n", out["text"])
        self.assertFalse(out["truncated"])

    def test_CRLF日志换行归一为LF(self):
        """Windows 日志每行以 CRLF 结尾；换行符残留会让界面出现空行、行数翻倍。"""
        job_id = "20260101000000-03"
        p = state.logs_dir() / (job_id + ".log")
        p.write_bytes("第一行\r\n第二行\r\n".encode("utf-8"))
        out = self.server.m_getJobLog({"job_id": job_id})
        self.assertEqual("第一行\n第二行\n", out["text"])
        self.assertFalse(out["truncated"])

    def test_截断按字符数而非字节数判定(self):
        """CJK 日志 3 字节/字符：15 万字符（约 450KB）远未到字符上限，
        不能因为字节数超过 20 万就标记截断并把完整日志误裁。"""
        job_id = "20260101000000-04"
        p = state.logs_dir() / (job_id + ".log")
        body = "日" * 150000
        p.write_text(body, encoding="utf-8")
        out = self.server.m_getJobLog({"job_id": job_id})
        self.assertFalse(out["truncated"])
        self.assertEqual(body, out["text"])


class SaveConfigRpcTest(RpcTestCase):
    def test_机巡密码保存后回显为空但提示已配置(self):
        out = self.server.m_saveConfig(
            {
                "defect_review": {
                    "username": "u",
                    "password": "secret-1",
                    "instances": [
                        {"id": "default", "params": {"password": "secret-2"}}
                    ],
                }
            }
        )
        self.assertEqual("", out["defect_review"]["password"])
        self.assertTrue(out["defect_review"]["has_password"])
        self.assertEqual("", out["defect_review"]["instances"][0]["params"]["password"])
        # 留空再存不能把密码清掉
        again = self.server.m_saveConfig(
            {"defect_review": {"username": "u", "password": ""}}
        )
        self.assertTrue(again["defect_review"]["has_password"])
        raw = json.loads(Path(os.environ["UVMP_TOOLKIT_CONFIG"]).read_text("utf-8"))
        self.assertEqual("secret-1", raw["defect_review"]["password"])
        saved_params = raw["defect_review"]["instances"][0]["params"]
        self.assertEqual("secret-2", saved_params["password"])

    def test_非法app_id的RPC历史查询返回空(self):
        self.assertEqual([], self.server.m_getHistory({"app_id": "../config"}))
        self.assertEqual([], self.server.m_getHistory({}))

    def test_类型不符返回可读错误(self):
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_saveConfig({"order_export": {"query_workers": "八"}})
        self.assertIn("order_export.query_workers", str(ctx.exception))


class ProtocolStreamTest(unittest.TestCase):
    """端到端：子进程里跑 serve()，验证 stdout 只有协议行"""

    def test_普通print不影响协议流(self):
        script = (
            "import sys\n"
            "sys.path.insert(0, %r)\n"
            "import rpc\n"
            "class S(rpc.RpcServer):\n"
            "    def m_noise(self, _p):\n"
            "        print('NOISE-TO-STDOUT')\n"
            "        return {'ok': 1}\n"
            "S().serve()\n" % str(TOOLKIT)
        )
        proc = subprocess.run(
            [sys.executable, "-c", script],
            input='{"id": 7, "method": "noise"}\n',
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(0, proc.returncode, proc.stderr)
        lines = [ln for ln in proc.stdout.splitlines() if ln.strip()]
        self.assertEqual(1, len(lines), proc.stdout)
        msg = json.loads(lines[0])
        self.assertEqual(7, msg["id"])
        self.assertEqual({"ok": 1}, msg["result"])
        self.assertIn("NOISE-TO-STDOUT", proc.stderr)


if __name__ == "__main__":
    unittest.main()
