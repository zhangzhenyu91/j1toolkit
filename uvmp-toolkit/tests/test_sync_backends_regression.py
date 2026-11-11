# -*- coding: utf-8 -*-
"""每日/树障/交跨同步后端回归：日期校验、mock 拒写正式表、临时 xlsx 权限与清理、
原子备份与回退、usb.lock 共用、agent_owned 不写成功戳、结果路径最小化。

全部使用临时目录 + 假客户端，不连真实派车系统/U 盘/云端。
"""

import json
import os
import sys
import tempfile
import types
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

# vehicle_export 在 import 期就会 sys.exit（缺 requests），离线测试环境用最小桩替代，
# 不发起任何真实请求。
try:
    import requests  # noqa: F401
except ImportError:  # pragma: no cover - 仅在无依赖的开发机上生效
    _stub = types.ModuleType("requests")
    _stub.Session = type("Session", (), {"headers": {}})
    _stub.RequestException = OSError
    sys.modules["requests"] = _stub

import app  # noqa: E402

CST = timezone(timedelta(hours=8))
DAY = "2026-11-11"


class FakeJob:
    def __init__(self):
        self.lines = []

    def log(self, msg, *args):
        self.lines.append(str(msg))

    def set_progress(self, *_args, **_kwargs):
        pass

    def text(self):
        return "\n".join(self.lines)


def _cfg(**over):
    cfg = {
        "dev": {"mock": False},
        "usb": {"label": "GLKVM", "fallback_dir": ""},
        "daily_export": {"temp_dir": ""},
        "tree_sync": {},
        "crossing_sync": {},
    }
    cfg.update(over)
    return cfg


class _StateIsolated(unittest.TestCase):
    """隔离 state 目录，避免测试写真实 ~/.local/share/uvmp-toolkit。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.base = Path(self.tmp.name)
        self._old_env = {
            k: os.environ.get(k)
            for k in ("XDG_DATA_HOME", "XDG_CONFIG_HOME", "LOCALAPPDATA", "APPDATA")
        }
        os.environ["XDG_DATA_HOME"] = str(self.base / "state")
        os.environ["XDG_CONFIG_HOME"] = str(self.base / "cfg")
        os.environ["LOCALAPPDATA"] = str(self.base / "localappdata")
        os.environ["APPDATA"] = str(self.base / "appdata")

    def tearDown(self):
        for k, v in self._old_env.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        self.tmp.cleanup()

    def backend(self, app_id):
        return app._load_backend(app_id)


class DailyDateTest(_StateIsolated):
    def _backend(self):
        return self.backend("daily_sync")

    def test_invalid_date_is_rejected(self):
        backend = self._backend()
        mount = self.base / "usb"
        mount.mkdir()
        for bad in ("../../tmp", "2026-13-01", "2026-02-30", "20261111"):
            with mock.patch.object(backend._config, "load", return_value=_cfg()):
                with self.assertRaises(RuntimeError) as ctx:
                    backend.handle_command(
                        FakeJob(),
                        {"action": "export", "params": {"date": bad}},
                        str(mount),
                    )
            self.assertIn("日期", str(ctx.exception))
        self.assertEqual(list(mount.glob("*.xlsx")), [])

    def test_valid_date_kept(self):
        backend = self._backend()
        self.assertEqual(backend.normalize_day("2026-11-11"), "2026-11-11")
        self.assertEqual(backend.normalize_day(""), datetime.now(CST).strftime("%Y-%m-%d"))

    def test_default_day_uses_fixed_cst(self):
        backend = self._backend()
        old = os.environ.get("TZ")
        os.environ["TZ"] = "UTC"
        try:
            if hasattr(__import__("time"), "tzset"):
                __import__("time").tzset()
            self.assertEqual(
                backend.normalize_day(""), datetime.now(CST).strftime("%Y-%m-%d")
            )
        finally:
            if old is None:
                os.environ.pop("TZ", None)
            else:
                os.environ["TZ"] = old
            if hasattr(__import__("time"), "tzset"):
                __import__("time").tzset()


class DailyExportTest(_StateIsolated):
    def _setup(self, items):
        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        temp_dir = self.base / "tmpdl"
        cfg = _cfg(daily_export={"temp_dir": str(temp_dir)})
        fake = mock.Mock(return_value=(object(), None))
        return backend, mount, cfg, temp_dir, fake, items

    def test_mock_mode_refuses_formal_xlsx(self):
        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        cfg = _cfg(dev={"mock": True})
        make_client = mock.Mock()
        with mock.patch.object(backend._config, "load", return_value=cfg), mock.patch.object(
            backend.uvmp, "make_client", make_client
        ):
            with self.assertRaises(RuntimeError) as ctx:
                backend.handle_command(
                    FakeJob(), {"action": "export", "params": {"date": DAY}}, str(mount)
                )
        text = str(ctx.exception)
        self.assertIn("mock", text)
        self.assertTrue("正式" in text or "拒绝" in text, text)
        make_client.assert_not_called()
        self.assertEqual(list(mount.glob("*.xlsx")), [])

    def test_mock_client_marker_also_refuses(self):
        """配置未标 mock 但客户端带 is_mock 标记时同样拒绝写入。"""
        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        client = mock.Mock()
        client.is_mock = True
        with mock.patch.object(
            backend.uvmp, "make_client", return_value=(client, None)
        ):
            with self.assertRaises(RuntimeError) as ctx:
                backend._export(FakeJob(), _cfg(), DAY, dest_dir=str(mount))
        self.assertIn("mock", str(ctx.exception))
        self.assertEqual(list(mount.glob("*.xlsx")), [])

    @unittest.skipIf(os.name == "nt", "Windows 不保留 POSIX 权限位")
    def test_temp_xlsx_is_private_and_removed(self):
        backend, mount, cfg, temp_dir, fake, items = self._setup(
            [{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}]
        )
        captured = {}

        def fake_write(data_items, output_path, log):
            captured["mode"] = os.stat(output_path).st_mode & 0o777
            Path(output_path).write_bytes(b"xlsx")
            return len(data_items)

        with mock.patch.object(backend.uvmp, "make_client", fake), mock.patch.object(
            backend, "query_day_orders", return_value=items
        ), mock.patch.object(
            backend.xlsx_util, "write_dispatch_xlsx", side_effect=fake_write
        ):
            res = backend._export(FakeJob(), cfg, DAY, dest_dir=str(mount))

        self.assertEqual(captured["mode"], 0o600, "临时 xlsx 必须是 0600")
        final = mount / ("%s-派车单.xlsx" % DAY)
        self.assertTrue(final.is_file())
        self.assertEqual(final.stat().st_mode & 0o777, 0o644)
        self.assertEqual(list(temp_dir.glob("*.xlsx")), [], "成功后必须清理临时文件")
        self.assertEqual(list(mount.glob(".*.tmp")), [], "不得残留临时文件")
        self.assertEqual(res["file"], str(final), "后端保留绝对路径")
        self.assertNotIn("dir", res, "agent 结果不带产物目录")

    def test_result_keeps_dir_for_gui_mode(self):
        backend, mount, cfg, temp_dir, fake, items = self._setup(
            [{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}]
        )
        with mock.patch.object(backend.uvmp, "make_client", fake), mock.patch.object(
            backend, "query_day_orders", return_value=items
        ), mock.patch.object(
            backend.platform_utils, "usb_writable_dir", return_value=(str(mount), "usb")
        ):
            res = backend._export(FakeJob(), cfg, DAY)
        self.assertEqual(res["dir"], str(mount))
        self.assertTrue(res["file"].endswith("-派车单.xlsx"))

    def test_failure_keeps_temp_for_diagnostics(self):
        backend, mount, cfg, temp_dir, fake, items = self._setup(
            [{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}]
        )

        def fake_write(data_items, output_path, log):
            Path(output_path).write_bytes(b"xlsx")
            mount.rmdir()  # 生成期间 U 盘被拔
            return len(data_items)

        with mock.patch.object(backend.uvmp, "make_client", fake), mock.patch.object(
            backend, "query_day_orders", return_value=items
        ), mock.patch.object(
            backend.xlsx_util, "write_dispatch_xlsx", side_effect=fake_write
        ):
            with self.assertRaises(RuntimeError) as ctx:
                backend._export(FakeJob(), cfg, DAY, dest_dir=str(mount))
        self.assertIn(str(temp_dir), str(ctx.exception))
        self.assertEqual(len(list(temp_dir.glob("*.xlsx"))), 1, "失败时保留临时文件")


def _fake_xlsx(items, output_path, log):
    Path(output_path).write_bytes(b"xlsx")
    return len(items)


class DailyLockTest(_StateIsolated):
    def test_manual_export_reports_busy_lock(self):
        """agent 持锁时手动导出必须报“锁被占用”，而不是误报 U 盘不可用。"""
        import usb_agent

        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        fd = usb_agent.device_lock()
        self.assertIsNotNone(fd)
        try:
            with self.assertRaises(RuntimeError) as ctx:
                backend._export(FakeJob(), _cfg(), DAY)
        finally:
            usb_agent.release_device_lock(fd)
        msg = str(ctx.exception)
        self.assertIn("锁被占用", msg)
        self.assertNotIn("未配置后备目录", msg)

    def _mount_cfg(self, mount_and_source):
        """手动入口的 U 盘发现桩：记录调用次数并按顺序返回挂载点。"""
        cfg = _cfg(daily_export={"temp_dir": str(self.base / "tmpdl")})
        return cfg, mock.Mock(side_effect=mount_and_source)

    def test_slow_query_does_not_hold_lock(self):
        """登录/分页查询/生成 xlsx 期间不得持锁：否则 agent 指令只能等整轮结束。"""
        import usb_agent

        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        cfg, writable = self._mount_cfg(lambda *_a, **_k: (str(mount), "usb"))
        seen = {}

        def slow_query(_client, _day, _log):
            # 慢查询进行中：锁必须是空的，agent 此刻能立刻抢到并执行指令。
            fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
            seen["lock_free"] = fd is not None
            if fd is not None:
                usb_agent._release_lock(fd)
            return [{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}]

        with mock.patch.object(
            backend.platform_utils, "usb_writable_dir", writable
        ), mock.patch.object(
            backend.uvmp, "make_client", return_value=(object(), None)
        ), mock.patch.object(
            backend, "query_day_orders", side_effect=slow_query
        ), mock.patch.object(
            backend.xlsx_util, "write_dispatch_xlsx", side_effect=_fake_xlsx
        ), mock.patch.object(
            backend.platform_utils, "sync_mount"
        ):
            res = backend._export(FakeJob(), cfg, DAY)
        self.assertTrue(seen["lock_free"], "慢查询期间不得持有 usb.lock")
        self.assertTrue(Path(res["file"]).is_file())

    def test_write_phase_holds_lock(self):
        """复制落盘阶段必须持锁：否则会与 agent 的指令/结果写入并发抢 U 盘。"""
        import usb_agent

        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        cfg, writable = self._mount_cfg(lambda *_a, **_k: (str(mount), "usb"))
        seen = {}
        real_copy2 = backend.shutil.copy2

        def probing_copy2(src, dst):
            # 本进程再加一次 flock：拿不到说明写盘时锁确实被持有。
            fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
            seen["locked"] = fd is None
            if fd is not None:
                usb_agent._release_lock(fd)
            return real_copy2(src, dst)

        with mock.patch.object(
            backend.platform_utils, "usb_writable_dir", writable
        ), mock.patch.object(
            backend.uvmp, "make_client", return_value=(object(), None)
        ), mock.patch.object(
            backend,
            "query_day_orders",
            return_value=[{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}],
        ), mock.patch.object(
            backend.xlsx_util, "write_dispatch_xlsx", side_effect=_fake_xlsx
        ), mock.patch.object(
            backend.platform_utils, "sync_mount"
        ), mock.patch.object(
            backend.shutil, "copy2", side_effect=probing_copy2
        ):
            backend._export(FakeJob(), cfg, DAY)
        self.assertTrue(seen["locked"], "写盘阶段必须持有 usb.lock")
        # 结束后锁必须释放（release 只在手动入口加过的那次）。
        fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
        self.assertIsNotNone(fd)
        usb_agent._release_lock(fd)

    def test_write_phase_lock_busy_keeps_temp(self):
        """查询完成后 agent 抢先持锁：报错要指出临时文件位置，且临时文件不被删。"""
        import usb_agent

        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        temp_dir = self.base / "tmpdl"
        cfg, writable = self._mount_cfg(lambda *_a, **_k: (str(mount), "usb"))
        lock_fd = {}

        def slow_query(_client, _day, _log):
            # 慢查询期间锁是空的，agent 此刻合法地抢走了它。
            lock_fd["fd"] = usb_agent.device_lock()
            return [{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}]

        try:
            with mock.patch.object(
                backend.platform_utils, "usb_writable_dir", writable
            ), mock.patch.object(
                backend.uvmp, "make_client", return_value=(object(), None)
            ), mock.patch.object(
                backend, "query_day_orders", side_effect=slow_query
            ), mock.patch.object(
                backend.xlsx_util, "write_dispatch_xlsx", side_effect=_fake_xlsx
            ), mock.patch.object(
                backend.platform_utils, "sync_mount"
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    backend._export(FakeJob(), cfg, DAY)
        finally:
            usb_agent.release_device_lock(lock_fd.get("fd"))
        msg = str(ctx.exception)
        self.assertIn("锁被占用", msg)
        self.assertIn(str(temp_dir), msg)
        self.assertEqual(len(list(temp_dir.glob("*.xlsx"))), 1, "抢锁失败要保留临时文件")
        self.assertEqual(list(mount.glob("*.xlsx")), [], "不得写入 U 盘")

    def test_command_mode_does_not_relock(self):
        """指令模式已由 agent 持锁：导出不得再次 device_lock（同进程重入必然失败）。"""
        import usb_agent

        backend = self.backend("daily_sync")
        mount = self.base / "usb"
        mount.mkdir()
        cfg = _cfg(daily_export={"temp_dir": str(self.base / "tmpdl")})
        fd = usb_agent.device_lock()
        self.assertIsNotNone(fd)
        try:
            with mock.patch.object(
                backend.uvmp, "make_client", return_value=(object(), None)
            ), mock.patch.object(
                backend,
                "query_day_orders",
                return_value=[{"planSendTime": DAY + " 09:00:00", "runCode": "PCD1"}],
            ), mock.patch.object(
                backend.xlsx_util, "write_dispatch_xlsx", side_effect=_fake_xlsx
            ), mock.patch.object(
                backend.platform_utils, "sync_mount"
            ), mock.patch.object(
                backend.usb_agent, "device_lock"
            ) as relock:
                res = backend._export(
                    FakeJob(), cfg, DAY, dest_dir=str(mount), mark=False
                )
        finally:
            usb_agent.release_device_lock(fd)
        relock.assert_not_called()
        self.assertTrue((mount / ("%s-派车单.xlsx" % DAY)).is_file())
        self.assertEqual(res["count"], 1)
        self.assertNotIn("dir", res)


class OrderTimestampTest(_StateIsolated):
    def test_timestamp_overflow_does_not_raise(self):
        backend = self.backend("daily_sync")
        self.assertTrue(backend.is_today_order({"planSendTime": 10**20}, DAY))
        self.assertTrue(backend.is_today_order({"planSendTime": float("inf")}, DAY))
        self.assertTrue(backend.is_today_order({"planSendTime": -(10**20)}, DAY))

    def test_millisecond_timestamp_uses_fixed_cst(self):
        backend = self.backend("daily_sync")
        # 2026-11-11 00:30 +08:00 = 2026-11-10 16:30 UTC：本机时区为 UTC 时也必须算当日。
        ms = int(
            datetime(2026, 11, 11, 0, 30, tzinfo=CST).timestamp() * 1000
        )
        old = os.environ.get("TZ")
        os.environ["TZ"] = "UTC"
        try:
            if hasattr(__import__("time"), "tzset"):
                __import__("time").tzset()
            self.assertTrue(backend.is_today_order({"planSendTime": ms}, DAY))
            self.assertFalse(backend.is_today_order({"planSendTime": ms}, "2026-11-10"))
        finally:
            if old is None:
                os.environ.pop("TZ", None)
            else:
                os.environ["TZ"] = old
            if hasattr(__import__("time"), "tzset"):
                __import__("time").tzset()


class SyncReplaceAtomicTest(_StateIsolated):
    def _backend(self, app_id):
        return self.backend(app_id)

    def test_temp_failure_never_writes_target_directly(self):
        """同目录临时文件建不出来时不得退回直写：截断即等于写坏内网表。"""
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
                    if dst_s.endswith(".sync-tmp"):
                        raise PermissionError(13, "Permission denied", dst_s)
                    if Path(dst_s) == local:
                        Path(dst_s).write_bytes(b"")
                        raise OSError(28, "No space left on device", dst_s)
                    return real_copy(src, dst)

                with mock.patch.object(
                    backend.shutil, "copyfile", side_effect=fake_copy
                ):
                    with self.assertRaises(RuntimeError):
                        backend._replace(local, usb, "new.xlsx", lambda *_: None)
                self.assertEqual(local.read_bytes(), b"old", app_id)

    def test_backup_is_atomic(self):
        """备份写一半失败时，上一份 .bak 必须原样保留，不能被截断。"""
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"old")
                bak = root / "inner.xlsx.bak"
                bak.write_bytes(b"prev")
                (usb / "new.xlsx").write_bytes(b"new")
                real_copy = backend.shutil.copyfile

                def fake_copy(src, dst):
                    dst_s = str(dst)
                    if dst_s.endswith(".bak.sync-tmp"):
                        Path(dst_s).write_bytes(b"par")
                        raise OSError(28, "No space left on device", dst_s)
                    return real_copy(src, dst)

                with mock.patch.object(
                    backend.shutil, "copyfile", side_effect=fake_copy
                ):
                    backend._replace(local, usb, "new.xlsx", lambda *_: None)
                self.assertEqual(local.read_bytes(), b"new", app_id)
                self.assertEqual(bak.read_bytes(), b"prev", app_id)

    def test_result_paths_minimized_before_usb_write(self):
        """后端保留绝对路径（集群远端执行要靠它收集产物），写盘前收敛成文件名。"""
        import usb_protocol as proto

        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"old")
                (usb / "new.xlsx").write_bytes(b"new")
                res = backend._replace(local, usb, "new.xlsx", lambda *_: None)
                self.assertEqual(res["file"], str(local), "集群链路需要绝对路径")
                self.assertEqual(res["src_deleted"], "new.xlsx")
                out = proto.minimize_result(res)
                self.assertEqual(out["file"], "inner.xlsx")
                self.assertEqual(out["src_deleted"], "new.xlsx")

    def test_snapshot_result_keeps_abs_path_and_minimizes(self):
        import usb_protocol as proto

        for app_id in ("tree_sync", "crossing_sync"):
            backend = self._backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                root = Path(d)
                usb = root / "usb"
                usb.mkdir()
                local = root / "inner.xlsx"
                local.write_bytes(b"data")
                res = backend._snapshot(local, usb, "up.xlsx", lambda *_: None)
                self.assertEqual(res["file"], str(usb / "up.xlsx"))
                self.assertEqual(proto.minimize_result(res)["file"], "up.xlsx")
                self.assertEqual((usb / "up.xlsx").read_bytes(), b"data")
                self.assertFalse((usb / ".up.xlsx.tmp").exists(), "临时文件必须清理")


class SyncLockAndStampTest(_StateIsolated):
    def test_tick_skips_when_usb_lock_busy(self):
        import usb_agent

        for app_id, cmd_name in (
            ("tree_sync", "tree_sync_cmd.json"),
            ("crossing_sync", "crossing_sync_cmd.json"),
        ):
            backend = self.backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                usb = Path(d)
                cmd = usb / cmd_name
                cmd.write_text(
                    json.dumps({"kind": "snapshot", "cycle_id": "cid-1"}), encoding="utf-8"
                )
                fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
                self.assertIsNotNone(fd)
                try:
                    with mock.patch.object(
                        backend.platform_utils, "find_usb_mount", return_value=str(usb)
                    ):
                        res = backend._tick(
                            FakeJob(), _cfg(), {"tick": True}
                        )
                finally:
                    usb_agent._release_lock(fd)
                self.assertEqual(res.get("skipped"), "busy", app_id)
                self.assertTrue(cmd.exists(), app_id)

    def test_tick_runs_when_lock_free(self):
        import usb_agent

        backend = self.backend("tree_sync")
        with tempfile.TemporaryDirectory() as d:
            usb = Path(d)
            (usb / "tree_sync_cmd.json").write_text(
                json.dumps({"kind": "snapshot", "cycle_id": "cid-2"}), encoding="utf-8"
            )
            with mock.patch.object(
                backend.platform_utils, "find_usb_mount", return_value=str(usb)
            ):
                res = backend._tick(FakeJob(), _cfg(), {"tick": True})
            self.assertEqual(res.get("skipped"), "agent_owned")
            # 锁必须已释放：紧接着还能再抢到。
            fd = usb_agent._try_lock(usb_agent.state.state_dir() / "usb.lock")
            self.assertIsNotNone(fd)
            usb_agent._release_lock(fd)

    def test_agent_owned_does_not_write_stamp(self):
        for app_id, cmd_name in (
            ("tree_sync", "tree_sync_cmd.json"),
            ("crossing_sync", "crossing_sync_cmd.json"),
        ):
            backend = self.backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                usb = Path(d)
                (usb / cmd_name).write_text(
                    json.dumps({"kind": "snapshot", "cycle_id": "cid-3"}),
                    encoding="utf-8",
                )
                cfg = _cfg(**{app_id: {"local_path": str(usb / "inner.xlsx")}})
                with mock.patch.object(
                    backend._config, "load", return_value=cfg
                ), mock.patch.object(
                    backend.platform_utils, "find_usb_mount", return_value=str(usb)
                ):
                    res = backend.run(FakeJob(), {"nightly": True})
                self.assertEqual(res.get("skipped"), "agent_owned", app_id)
                self.assertIsNone(
                    backend.state.get_stamp(backend.STAMP_NIGHTLY),
                    "%s：指令归 agent 时不得写成功戳" % app_id,
                )

    def test_nightly_snapshot_writes_stamp(self):
        for app_id in ("tree_sync", "crossing_sync"):
            backend = self.backend(app_id)
            with tempfile.TemporaryDirectory() as d:
                usb = Path(d)
                local = usb / "inner.xlsx"
                local.write_bytes(b"data")
                cfg = _cfg(**{app_id: {"local_path": str(local)}})
                with mock.patch.object(
                    backend._config, "load", return_value=cfg
                ), mock.patch.object(
                    backend.platform_utils, "find_usb_mount", return_value=str(usb)
                ):
                    res = backend.run(FakeJob(), {"nightly": True})
                self.assertTrue(res.get("nightly"), app_id)
                stamp = backend.state.get_stamp(backend.STAMP_NIGHTLY)
                self.assertIsNotNone(stamp, app_id)
                self.assertEqual(stamp["date"], datetime.now(CST).strftime("%Y-%m-%d"))


if __name__ == "__main__":
    unittest.main()
