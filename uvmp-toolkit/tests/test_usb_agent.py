# -*- coding: utf-8 -*-
"""usb-agent 单元测试：不依赖真实 U 盘/udev，注入假挂载点与假后端。

覆盖：成功写结果并删指令、失败也删指令并写 ok=false、损坏指令、崩溃残留恢复、
结果写盘失败后的本地 pending 补写、只读挂载跳过。
"""

import json
import logging
import os
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import apps  # noqa: E402
import platform_utils  # noqa: E402
import usb_agent  # noqa: E402
import usb_protocol as p  # noqa: E402


class FakeBackend:
    def __init__(self, fail=False):
        self.fail = fail
        self.calls = 0

    def handle_command(self, job, cmd, mount):
        self.calls += 1
        job.log("fake handle %s" % cmd["id"])
        if self.fail:
            raise RuntimeError("boom")
        return {"action": cmd["action"], "file": cmd.get("params", {}).get("file", "")}


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name)
        self._old_env = {
            k: os.environ.get(k) for k in ("XDG_DATA_HOME", "XDG_CONFIG_HOME")
        }
        os.environ["XDG_DATA_HOME"] = str(base / "state")
        os.environ["XDG_CONFIG_HOME"] = str(base / "cfg")
        self.mount = base / "usb"
        self.mount.mkdir()
        self._old_scan = platform_utils.scan_usb_mounts
        platform_utils.scan_usb_mounts = lambda label, log=print: (
            "/dev/fake",
            [(str(self.mount), False, True)],
        )
        self._old_scan_apps = apps.scan_apps
        apps.scan_apps = lambda: []
        self._old_retry_sleep = usb_agent.MOUNT_RETRY_SLEEP
        usb_agent.MOUNT_RETRY_SLEEP = 0
        self.agent = usb_agent.UsbAgent(quiet=True)
        self.backend = FakeBackend()
        self.agent.apps = {
            "tree_sync": ({"id": "tree_sync", "name": "树障同步"}, self.backend)
        }
        self.agent._reload_apps = lambda force=False: None

    def tearDown(self):
        platform_utils.scan_usb_mounts = self._old_scan
        apps.scan_apps = self._old_scan_apps
        usb_agent.MOUNT_RETRY_SLEEP = self._old_retry_sleep
        for k, v in self._old_env.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        self.tmp.cleanup()

    def write_cmd(self, cmd):
        (self.mount / p.cmd_name("tree_sync")).write_text(
            json.dumps(cmd, ensure_ascii=False), encoding="utf-8"
        )

    def result_files(self):
        return sorted(self.mount.glob("tree_sync_result_*.json"))

    def test_success_writes_result_and_deletes_command(self):
        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-1",
                "app": "tree_sync",
                "action": "snapshot",
                "params": {"file": "a.xlsx"},
            }
        )
        self.agent.tick()
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        files = self.result_files()
        self.assertEqual(len(files), 1)
        res = json.loads(files[0].read_text(encoding="utf-8"))
        self.assertTrue(res["ok"])
        self.assertEqual(res["id"], "tree_sync-1")
        self.assertEqual(res["data"]["action"], "snapshot")
        self.assertEqual(self.backend.calls, 1)
        self.assertIn("tree_sync-1", self.agent.journal_path.read_text(encoding="utf-8"))

    def test_failure_still_deletes_command_and_writes_error(self):
        self.backend.fail = True
        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-2",
                "app": "tree_sync",
                "action": "replace",
            }
        )
        self.agent.tick()
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertFalse(res["ok"])
        self.assertIn("boom", res["error"])

    def test_invalid_command_writes_failure_and_deletes(self):
        (self.mount / p.cmd_name("tree_sync")).write_text("{not json", encoding="utf-8")
        self.agent.tick()
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertFalse(res["ok"])
        self.assertTrue(res["data"]["invalid"])

    def test_expired_command_is_skipped(self):
        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-3",
                "app": "tree_sync",
                "action": "snapshot",
                "expires_at": "2020-01-01T00:00:00+08:00",
            }
        )
        self.agent.tick()
        self.assertFalse((self.mount / p.cmd_name("tree_sync")).exists())
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertFalse(res["ok"])
        self.assertTrue(res["data"]["expired"])
        self.assertEqual(self.backend.calls, 0)

    def test_interrupted_running_file_is_recovered(self):
        running = self.mount / p.running_name("tree_sync", "tree_sync-4")
        running.write_text("{}", encoding="utf-8")
        self.agent.tick()
        self.assertFalse(running.exists())
        res = json.loads(self.result_files()[0].read_text(encoding="utf-8"))
        self.assertFalse(res["ok"])
        self.assertTrue(res["data"]["interrupted"])

    def test_pending_result_is_flushed_next_tick(self):
        res = p.build_result(
            {"id": "tree_sync-5", "app": "tree_sync"},
            True,
            data={"action": "snapshot"},
        )
        running = self.mount / p.running_name("tree_sync", "tree_sync-5")
        running.write_text("{}", encoding="utf-8")
        self.agent._save_pending("tree_sync", "tree_sync-5", res, running.name)
        self.agent.tick()
        self.assertFalse(running.exists())
        files = self.result_files()
        self.assertEqual(len(files), 1)
        self.assertTrue(json.loads(files[0].read_text(encoding="utf-8"))["ok"])

    def test_service_instance_waits_for_lock_handover(self):
        # systemd 服务设 WAIT_LOCK=1：GUI 拉起的实例占锁时等待接管，而不是退出后无人值守。
        fd = usb_agent._try_lock(self.agent.instance_lock_path)
        self.assertIsNotNone(fd)
        os.environ[usb_agent.WAIT_LOCK_ENV] = "1"
        acquired = []
        t = threading.Thread(target=lambda: acquired.append(self.agent._acquire_instance_lock()))
        t.start()
        time.sleep(0.3)
        self.assertTrue(t.is_alive(), "占锁期间应等待而不是立即返回")
        usb_agent._release_lock(fd)
        t.join(5)
        os.environ.pop(usb_agent.WAIT_LOCK_ENV, None)
        self.assertFalse(t.is_alive())
        self.assertIsNotNone(acquired[0])
        usb_agent._release_lock(acquired[0])

    def test_version_from_env_and_bundle_file(self):
        os.environ["UVMP_VERSION"] = "9.9.9"
        try:
            self.assertEqual(usb_agent._read_version(), "9.9.9")
        finally:
            os.environ.pop("UVMP_VERSION", None)
        (self.mount / "version.txt").write_text("2.1.16\n", encoding="utf-8")
        with mock.patch.object(usb_agent.sys, "frozen", True, create=True), mock.patch.object(
            usb_agent.sys, "executable", str(self.mount / "uvmp-core")
        ):
            self.assertEqual(usb_agent._read_version(), "2.1.16")

    def test_clear_logs_keeps_agent_log(self):
        logs = usb_agent.state.logs_dir()
        agent_log = logs / "usb-agent.log"
        job_log = logs / "job.log"
        agent_log.write_text("keep", encoding="utf-8")
        job_log.write_text("drop", encoding="utf-8")
        res = usb_agent.state.clear_logs()
        self.assertTrue(agent_log.exists(), "清日志不得删除常驻 agent 日志")
        self.assertFalse(job_log.exists())
        self.assertEqual(res["removed"], 1)

    def test_logging_falls_back_to_stderr(self):
        # 日志文件建不出来时不能变成“静默的 agent”：必须有 stderr handler 进 journald。
        with mock.patch.object(
            usb_agent.state, "logs_dir", side_effect=OSError("permission denied")
        ):
            logger = usb_agent._setup_logging("x.log", quiet=True)
        try:
            self.assertTrue(
                any(isinstance(h, logging.StreamHandler) for h in logger.handlers)
            )
        finally:
            for h in list(logger.handlers):
                logger.removeHandler(h)
                h.close()
            self.agent.logger = usb_agent._setup_logging("usb-agent.log", quiet=True)

    def test_mount_error_hint_explains_polkit_denial(self):
        text = platform_utils._mount_error_text(
            "Error: GDBus.Error:org.freedesktop.UDisks2.Error.NotAuthorized: "
            "Not authorized to perform operation"
        )
        self.assertIn("polkit", text)
        self.assertIn("udisksctl mount -b", text)

    def test_heartbeat_written_without_usb(self):
        platform_utils.scan_usb_mounts = lambda label, log=print: None
        self.agent.tick()
        data = json.loads(self.agent.heartbeat_path.read_text(encoding="utf-8"))
        self.assertEqual(data["state"], "no_device")
        self.assertFalse(data["mount"])
        self.assertTrue(data["ts"])
        self.assertEqual(data["label"], "GLKVM")

    def test_heartbeat_reports_mount_failure(self):
        platform_utils.scan_usb_mounts = lambda label, log=print: ("/dev/fake", [])
        self.agent.device = "/dev/fake"  # 跳过首次发现的 10s 挂载宽限
        with mock.patch.object(
            platform_utils, "mount_usb_device", return_value=(False, "udisks 未授权")
        ):
            self.agent.tick()
        data = json.loads(self.agent.heartbeat_path.read_text(encoding="utf-8"))
        self.assertEqual(data["state"], "device_unmounted")
        self.assertIn("udisks 未授权", data["last_error"])

    def test_mount_failure_backs_off_until_retry(self):
        # 连续失败先退避（不每轮重挂），退避到期后自动重试，而不是“等下次插拔”。
        platform_utils.scan_usb_mounts = lambda label, log=print: ("/dev/fake", [])
        self.agent.device = "/dev/fake"
        with mock.patch.object(
            platform_utils, "mount_usb_device", return_value=(False, "udisks 未授权")
        ) as m:
            self.agent.tick()
            self.agent.tick()
            self.agent.tick()
            self.assertEqual(m.call_count, usb_agent.MOUNT_RETRY_COUNT)
            self.assertIn("秒后自动重试", self.agent.last_error)
            self.agent.mount_retry_at = 0  # 退避到期
            self.agent.tick()
        self.assertEqual(m.call_count, usb_agent.MOUNT_RETRY_COUNT * 2)

    def test_remounts_after_mount_lost_while_device_present(self):
        # 服务端取件会断开共享但设备节点仍在（日志“U盘已卸载”）：一次成功挂载留下的
        # “已尝试”标记不得永久挡住后续挂载，否则指令文件永远读不到。
        state = {"mounted": False}

        def scan(label, log=print):
            if state["mounted"]:
                return ("/dev/fake", [(str(self.mount), False, True)])
            return ("/dev/fake", [])

        def mount_device(device, log=print):
            state["mounted"] = True
            return True, ""

        self.agent.device = "/dev/fake"
        self.agent.mount = str(self.mount)
        self.agent.mount_attempted_for = "/dev/fake"
        platform_utils.scan_usb_mounts = scan
        with mock.patch.object(
            platform_utils, "mount_usb_device", side_effect=mount_device
        ) as m:
            self.agent.tick()
        self.assertEqual(m.call_count, 1)
        self.assertEqual(self.agent.mount, str(self.mount))
        self.assertEqual(self.agent.mount_attempted_for, "")

    def test_reinsertion_allows_new_mount_attempt(self):
        platform_utils.scan_usb_mounts = lambda label, log=print: ("/dev/fake", [])
        with mock.patch.object(
            platform_utils, "mount_usb_device", return_value=(False, "udisks 未授权")
        ) as m, mock.patch.object(usb_agent, "_arg_int", return_value=0):
            self.agent.tick()  # 首次出现（宽限 0）
            first = m.call_count
            self.assertEqual(first, usb_agent.MOUNT_RETRY_COUNT)
            self.agent.tick()  # 同周期不再重试
            self.assertEqual(m.call_count, first)
            platform_utils.scan_usb_mounts = lambda label, log=print: None
            self.agent.tick()  # 拔出：复位插入周期
            platform_utils.scan_usb_mounts = lambda label, log=print: ("/dev/fake", [])
            self.agent.tick()  # 重新插入：允许再试
        self.assertEqual(m.call_count, first * 2)

    def test_target_udev_event_resets_attempt_marker(self):
        self.agent.device = "/dev/sda"
        self.agent.mount_attempted_for = "/dev/sda"
        self.agent._note_udev(("udev", 0, "add", "sda1"))
        self.assertEqual(self.agent.mount_attempted_for, "")
        self.agent.mount_attempted_for = "/dev/sda"
        self.agent._note_udev(("udev", 0, "add", "loop0"))
        self.assertEqual(self.agent.mount_attempted_for, "/dev/sda")
        self.agent._note_udev(("udev", 0, "remove", "sda"))
        self.assertEqual(self.agent.mount_attempted_for, "")

    def test_manual_check_forces_one_more_mount_attempt(self):
        platform_utils.scan_usb_mounts = lambda label, log=print: ("/dev/fake", [])
        self.agent.device = "/dev/fake"
        self.agent.mount_attempted_for = "/dev/fake"
        with mock.patch.object(
            platform_utils, "mount_usb_device", return_value=(False, "udisks 未授权")
        ) as m:
            self.agent.tick(force_mount=True)
        self.assertEqual(m.call_count, usb_agent.MOUNT_RETRY_COUNT)

    def test_permission_error_keeps_command_and_continues_other_apps(self):
        (self.mount / p.cmd_name("daily_sync")).write_text("{}", encoding="utf-8")
        self.write_cmd(
            {"version": 1, "id": "tree_sync-9", "app": "tree_sync", "action": "snapshot"}
        )
        daily = FakeBackend()
        self.agent.apps = {
            "daily_sync": ({"id": "daily_sync", "name": "派车同步"}, daily),
            "tree_sync": ({"id": "tree_sync", "name": "树障同步"}, self.backend),
        }
        calls = []
        real_run_one = self.agent._run_one

        def fake_run_one(app_id, *args, **kwargs):
            calls.append(app_id)
            if app_id == "daily_sync":
                raise PermissionError("Permission denied")
            return real_run_one(app_id, *args, **kwargs)

        self.agent._run_one = fake_run_one
        self.agent._process_commands(str(self.mount))
        self.assertEqual(calls, ["daily_sync", "tree_sync"])
        self.assertTrue((self.mount / p.cmd_name("daily_sync")).exists())
        self.assertIn("无权限", self.agent.last_problem)
        self.assertEqual(self.backend.calls, 1)

    def test_unusable_mount_keeps_command_and_reports(self):
        self.write_cmd(
            {"version": 1, "id": "tree_sync-7", "app": "tree_sync", "action": "snapshot"}
        )
        platform_utils.scan_usb_mounts = lambda label, log=print: (
            "/dev/fake",
            [(str(self.mount), False, False)],
        )
        with mock.patch.object(
            platform_utils, "mountpoint_usable", return_value=(False, "残留挂载")
        ):
            self.agent.tick()
        self.assertTrue((self.mount / p.cmd_name("tree_sync")).exists())
        self.assertEqual(self.backend.calls, 0)
        data = json.loads(self.agent.heartbeat_path.read_text(encoding="utf-8"))
        self.assertIn("不可用", data["last_error"])
        self.assertIn("残留挂载", data["last_error"])

    def test_mountpoint_writable(self):
        ok, why = platform_utils.mountpoint_writable(str(self.mount))
        self.assertTrue(ok, why)
        ok, _why = platform_utils.mountpoint_writable(str(self.mount / "missing"))
        self.assertFalse(ok)

    def test_pick_mount_prefers_command_mount(self):
        first = self.mount / "one"
        second = self.mount / "two"
        first.mkdir()
        second.mkdir()
        (second / p.cmd_name("tree_sync")).write_text("{}", encoding="utf-8")
        chosen = self.agent._pick_mount(
            [(str(first), False, True), (str(second), False, True)]
        )
        self.assertEqual(chosen[0], str(second))

    def test_pick_mount_prefers_newest_without_command(self):
        first = self.mount / "one"
        second = self.mount / "two"
        first.mkdir()
        second.mkdir()
        os.utime(first, (1000, 1000))
        os.utime(second, (2000, 2000))
        chosen = self.agent._pick_mount(
            [(str(first), False, True), (str(second), False, True)]
        )
        self.assertEqual(chosen[0], str(second))

    def test_readonly_mount_skips_execution(self):
        platform_utils.scan_usb_mounts = lambda label, log=print: (
            "/dev/fake",
            [(str(self.mount), True, True)],
        )
        self.write_cmd(
            {
                "version": 1,
                "id": "tree_sync-6",
                "app": "tree_sync",
                "action": "snapshot",
            }
        )
        self.agent.tick()
        self.assertTrue((self.mount / p.cmd_name("tree_sync")).exists())
        self.assertEqual(self.backend.calls, 0)


if __name__ == "__main__":
    unittest.main()
