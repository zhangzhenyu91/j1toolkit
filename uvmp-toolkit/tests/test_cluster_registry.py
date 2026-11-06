# -*- coding: utf-8 -*-
"""节点注册表单测：审批、在线判定、持久化、拒绝。"""

import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

from cluster.registry import NodeRegistry  # noqa: E402


def info(node_id="node-0001", role="worker", team_id=3):
    return {
        "node_id": node_id,
        "name": "节点-" + node_id[-2:],
        "role": role,
        "team_id": team_id,
        "team_name": "检修一班",
        "platform": "linux",
        "arch": "amd64",
        "version": "2.6.0",
        "protocol": 1,
        "port": 17890,
    }


class RegistryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "registry.json"

    def tearDown(self):
        self.tmp.cleanup()

    def test_pending_approve_and_online(self):
        reg = NodeRegistry(self.path, offline_sec=1, require_approval=True)
        row = reg.touch(info(), ip="10.0.0.2")
        self.assertFalse(row["approved"])
        self.assertEqual(row["ip"], "10.0.0.2")
        self.assertEqual(len(reg.pending()), 1)
        self.assertEqual(reg.snapshot(), [])

        approved = reg.approve("node-0001")
        self.assertTrue(approved["approved"])
        self.assertEqual(len(reg.pending()), 0)
        rows = reg.snapshot()
        self.assertEqual(len(rows), 1)
        self.assertTrue(rows[0]["online"])

        time.sleep(1.1)
        self.assertFalse(reg.snapshot()[0]["online"])

    def test_persistence(self):
        reg = NodeRegistry(self.path, offline_sec=30, require_approval=True)
        reg.touch(info())
        reg.approve("node-0001")

        again = NodeRegistry(self.path, offline_sec=30, require_approval=True)
        row = again.get("node-0001")
        self.assertIsNotNone(row)
        self.assertTrue(row["approved"])

    def test_auto_approve(self):
        reg = NodeRegistry(self.path, offline_sec=30, require_approval=False)
        row = reg.touch(info())
        self.assertTrue(row["approved"])
        self.assertEqual(len(reg.snapshot()), 1)

    def test_reject(self):
        reg = NodeRegistry(self.path, offline_sec=30, require_approval=True)
        reg.touch(info())
        self.assertTrue(reg.reject("node-0001"))
        self.assertIsNone(reg.get("node-0001"))
        self.assertFalse(reg.reject("missing"))

    def test_heartbeat_updates_fields(self):
        reg = NodeRegistry(self.path, offline_sec=30, require_approval=False)
        reg.touch(info(team_id=3))
        reg.touch(info(team_id=5))
        self.assertEqual(reg.get("node-0001")["team_id"], 5)

    def test_corrupt_file_starts_empty(self):
        self.path.write_text("{bad", encoding="utf-8")
        reg = NodeRegistry(self.path, offline_sec=30, require_approval=True)
        self.assertEqual(reg.counts()["total"], 0)
        self.assertTrue(
            any(p.name.startswith("registry.json.corrupt-") for p in self.path.parent.iterdir())
        )


if __name__ == "__main__":
    unittest.main()
