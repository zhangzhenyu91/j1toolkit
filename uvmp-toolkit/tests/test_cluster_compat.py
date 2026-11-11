# -*- coding: utf-8 -*-
"""同版/跨版互通回归：接收方绑定不得破坏既有 Hub↔Worker 心跳与中继。

本文件只跑回环 HTTP，不读真实配置。
"""

import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

TOOLKIT_DIR = Path(__file__).resolve().parent.parent / "toolkit"
if str(TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLKIT_DIR))

import usb_protocol  # noqa: E402
from cluster import protocol  # noqa: E402
from cluster.identity import NodeIdentity  # noqa: E402
from cluster.service import ClusterService, ClusterSettings  # noqa: E402
from jobs import JobManager  # noqa: E402

SECRET = "compat-test-secret-0123456789"
CLUSTER = "cluster-compat"


def settings(role="hub", port=0, hubs=None, team_id=3, require_approval=False):
    return ClusterSettings(
        enabled=True,
        role=role,
        cluster_id=CLUSTER,
        secret=SECRET,
        node_name="兼容测试-" + role,
        team_id=team_id,
        team_name="检修一班",
        listen_host="127.0.0.1",
        listen_port=port,
        hubs=hubs or [],
        require_approval=require_approval,
        heartbeat_sec=1,
        offline_sec=5,
        request_timeout_sec=2,
    )


class _FakeBackend:
    def handle_command(self, job, cmd, mount):
        path = Path(mount) / "out.xlsx"
        path.write_bytes(b"payload")
        return {"file": str(path)}


class _StateIsolated(unittest.TestCase):
    def setUp(self):
        self._state_tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        base = Path(self._state_tmp.name)
        self._old_state_env = {
            k: os.environ.get(k)
            for k in ("XDG_DATA_HOME", "LOCALAPPDATA", "APPDATA")
        }
        os.environ["XDG_DATA_HOME"] = str(base / "state")
        os.environ["LOCALAPPDATA"] = str(base / "localappdata")
        os.environ["APPDATA"] = str(base / "appdata")

    def tearDown(self):
        for key, value in self._old_state_env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        self._state_tmp.cleanup()


class SameVersionInteropTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.hub = ClusterService(root / "hub", NodeIdentity(root / "hub"))
        self.assertTrue(self.hub.start(settings("hub")))
        self.worker = ClusterService(root / "worker", NodeIdentity(root / "worker"))
        self.worker.set_runtime(
            JobManager(), lambda: {"fake_app": ({"id": "fake_app"}, _FakeBackend())}
        )
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % self.hub.actual_port],
                )
            )
        )

    def tearDown(self):
        self.worker.stop()
        self.hub.stop()
        self.tmp.cleanup()
        super().tearDown()

    def test_heartbeat_and_relay_work(self):
        state = self.worker.wait_worker_state(6.0)
        self.assertEqual(state.get("status"), "approved", state)
        hub_entry = self.hub.registry.get(self.worker.identity.node_id())
        self.assertIsNotNone(hub_entry)

        mount = Path(self.tmp.name) / "usb"
        mount.mkdir()
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "compat-0001",
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": self.worker.identity.node_id()},
                "params": {"team_id": 3, "file": "out.xlsx"},
                "expires_at": "2099-01-01T00:00:00+08:00",
            }
        )
        result, artifacts = self.hub.relay_command(cmd, str(mount))
        self.assertTrue(result["ok"], result)
        self.assertEqual(artifacts[0]["name"], "out.xlsx")

    def test_worker_receives_bound_signature(self):
        """Hub 已知道 Worker 支持绑定，因此转发请求必须带接收方签名并通过校验。"""
        self.worker.wait_worker_state(6.0)
        calls = []
        original = protocol.verify_headers

        def spy(*args, **kwargs):
            calls.append(kwargs.get("receiver_id") or "")
            return original(*args, **kwargs)

        with mock.patch.object(protocol, "verify_headers", side_effect=spy):
            mount = Path(self.tmp.name) / "usb2"
            mount.mkdir()
            cmd = usb_protocol.normalize_command(
                {
                    "version": 1,
                    "id": "compat-0002",
                    "app": "fake_app",
                    "action": "snapshot",
                    "cluster": CLUSTER,
                    "target": {"node_id": self.worker.identity.node_id()},
                    "params": {"team_id": 3, "file": "out.xlsx"},
                    "expires_at": "2099-01-01T00:00:00+08:00",
                }
            )
            result, _ = self.hub.relay_command(cmd, str(mount))
        self.assertTrue(result["ok"], result)
        self.assertIn(self.worker.identity.node_id(), calls)


class LegacyPeerTest(_StateIsolated):
    """旧版对端不声明 caps：签名原文保持旧格式，双向都必须可用。"""

    def test_legacy_join_and_export(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            hub = ClusterService(root / "hub", NodeIdentity(root / "hub"))
            self.assertTrue(hub.start(settings("hub", require_approval=False)))
            try:
                legacy = dict(
                    hub.node_info(hub.current_settings()),
                    node_id="legacy-node-0001",
                    role="worker",
                    port=hub.actual_port,
                )
                legacy.pop("caps", None)
                raw = protocol.json_bytes({"node": legacy})
                headers = protocol.build_headers(
                    SECRET, CLUSTER, "legacy-node-0001", "POST", "/cluster/join", raw
                )
                self.assertNotIn(protocol.HEADER_RECEIVER, headers)
                data = hub.handle_public(
                    "POST", "/cluster/join", raw, headers, "127.0.0.1"
                )
                self.assertEqual(data["status"], "approved")
                entry = hub.registry.get("legacy-node-0001")
                self.assertEqual(entry.get("caps"), [])
            finally:
                hub.stop()


if __name__ == "__main__":
    unittest.main()
