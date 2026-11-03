# -*- coding: utf-8 -*-
"""usb-agent 单元测试：不依赖真实 U 盘/udev，注入假挂载点与假后端。

覆盖：成功写结果并删指令、失败也删指令并写 ok=false、损坏指令、崩溃残留恢复、
结果写盘失败后的本地 pending 补写、只读挂载跳过。
"""

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

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
        self._old_scan = platform_utils.scan_usb_device
        platform_utils.scan_usb_device = lambda label, log=print: (
            "/dev/fake",
            str(self.mount),
            False,
        )
        self._old_scan_apps = apps.scan_apps
        apps.scan_apps = lambda: []
        self.agent = usb_agent.UsbAgent(quiet=True)
        self.backend = FakeBackend()
        self.agent.apps = {
            "tree_sync": ({"id": "tree_sync", "name": "树障同步"}, self.backend)
        }
        self.agent._reload_apps = lambda force=False: None

    def tearDown(self):
        platform_utils.scan_usb_device = self._old_scan
        apps.scan_apps = self._old_scan_apps
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

    def test_readonly_mount_skips_execution(self):
        platform_utils.scan_usb_device = lambda label, log=print: (
            "/dev/fake",
            str(self.mount),
            True,
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
