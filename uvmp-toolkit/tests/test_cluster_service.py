# -*- coding: utf-8 -*-
"""集群服务端到端单测：入网 pending/审批、心跳节点表、签名拒绝、Worker 心跳。"""

import json
import os
import socket
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "toolkit"))

import usb_protocol  # noqa: E402
from cluster import protocol  # noqa: E402
from cluster.service import ClusterService, ClusterSettings  # noqa: E402
from jobs import JobManager  # noqa: E402

SECRET = "service-test-secret-0123456789"
CLUSTER = "cluster-1"


def settings(role, port=0, hubs=None, require_approval=True):
    return ClusterSettings(
        enabled=True,
        role=role,
        cluster_id=CLUSTER,
        secret=SECRET,
        node_name="测试节点-" + role,
        team_id=3,
        team_name="检修一班",
        listen_host="127.0.0.1",
        listen_port=port,
        hubs=hubs or [],
        require_approval=require_approval,
        heartbeat_sec=1,
        offline_sec=5,
        request_timeout_sec=2,
    )


def node_info(node_id="worker-0001", role="worker", port=18001):
    return {
        "node_id": node_id,
        "name": "Worker-" + node_id[-2:],
        "role": role,
        "team_id": 3,
        "team_name": "检修一班",
        "platform": "linux",
        "arch": "amd64",
        "version": "2.6.0",
        "protocol": 1,
        "port": port,
    }


def signed_post(port, path, body, node_id, secret=SECRET, cluster_id=CLUSTER):
    raw = protocol.json_bytes(body)
    headers = protocol.build_headers(
        secret, cluster_id, node_id, "POST", path, raw
    )
    req = urllib.request.Request(
        "http://127.0.0.1:%d%s" % (port, path),
        data=raw,
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=2) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        try:
            body = e.read().decode("utf-8", "replace")
        finally:
            e.close()
        return {"http_status": e.code, "body": body}


def admin(port, path, body=None, token=""):
    raw = protocol.json_bytes(body) if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["X-UVMP-Admin-Token"] = token
    req = urllib.request.Request(
        "http://127.0.0.1:%d%s" % (port, path),
        data=raw,
        headers=headers,
        method="POST" if raw is not None else "GET",
    )
    with urllib.request.urlopen(req, timeout=2) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    return payload["data"]


class HubFlowTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.hub = ClusterService(base_dir=Path(self.tmp.name) / "hub")
        self.assertTrue(self.hub.start(settings("hub")))

    def tearDown(self):
        self.hub.stop()
        self.tmp.cleanup()

    def test_join_pending_then_approve(self):
        port = self.hub.actual_port
        token = self.hub._admin_token()
        info = node_info()
        first = signed_post(port, "/cluster/join", {"node": info}, info["node_id"])
        self.assertTrue(first["ok"])
        self.assertEqual(first["data"]["status"], "pending")
        self.assertEqual(first["data"]["nodes"], [])

        state = admin(port, "/cluster/admin/state", token=token)
        self.assertEqual(len(state["pending"]), 1)
        self.assertEqual(state["pending"][0]["node_id"], info["node_id"])

        admin(
            port,
            "/cluster/admin/approve",
            {"node_id": info["node_id"]},
            token=token,
        )
        second = signed_post(
            port, "/cluster/heartbeat", {"node": info}, info["node_id"]
        )
        self.assertEqual(second["data"]["status"], "approved")
        ids = [n["node_id"] for n in second["data"]["nodes"]]
        self.assertIn(info["node_id"], ids)
        self.assertIn(self.hub.identity.node_id(), ids)

    def test_wrong_secret_is_rejected(self):
        result = signed_post(
            self.hub.actual_port,
            "/cluster/join",
            {"node": node_info()},
            "worker-0001",
            secret="wrong-secret-0123456789",
        )
        self.assertEqual(result.get("http_status"), 401)

    def test_unapproved_cannot_read_nodes(self):
        result = signed_post(
            self.hub.actual_port, "/cluster/nodes", {}, "worker-0001"
        )
        self.assertEqual(result.get("http_status"), 403)

    def test_admin_is_local_only(self):
        self.assertFalse(self.hub.is_local_client("203.0.113.77", settings("hub")))
        self.assertTrue(self.hub.is_local_client("127.0.0.1", settings("hub")))

    def test_nodes_file_written_for_server(self):
        info = node_info()
        token = self.hub._admin_token()
        signed_post(
            self.hub.actual_port,
            "/cluster/join",
            {"node": info},
            info["node_id"],
        )
        admin(
            self.hub.actual_port,
            "/cluster/admin/approve",
            {"node_id": info["node_id"]},
            token=token,
        )
        data = self.hub.nodes_file()
        self.assertEqual(data["cluster_id"], CLUSTER)
        ids = [n["node_id"] for n in data["nodes"]]
        self.assertIn(info["node_id"], ids)
        self.assertIn(self.hub.identity.node_id(), ids)
        for node in data["nodes"]:
            self.assertEqual(node["home_hub"], self.hub.identity.node_id())
            self.assertIn("sync_tables", node)
        mount = Path(self.tmp.name) / "usb"
        mount.mkdir()
        self.assertTrue(self.hub.write_nodes_file(str(mount)))
        saved = json.loads((mount / "cluster_nodes.json").read_text("utf-8"))
        self.assertEqual(len(saved["nodes"]), len(data["nodes"]))

    def test_reject_removes_node(self):
        port = self.hub.actual_port
        token = self.hub._admin_token()
        info = node_info()
        signed_post(port, "/cluster/join", {"node": info}, info["node_id"])
        admin(port, "/cluster/admin/reject", {"node_id": info["node_id"]}, token=token)
        state = admin(port, "/cluster/admin/state", token=token)
        self.assertEqual(state["pending"], [])


class WorkerFlowTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.hub = ClusterService(base_dir=Path(self.tmp.name) / "hub")
        self.assertTrue(
            self.hub.start(settings("hub", require_approval=False))
        )
        self.worker = ClusterService(base_dir=Path(self.tmp.name) / "worker")
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % self.hub.actual_port],
                    require_approval=False,
                )
            )
        )

    def tearDown(self):
        self.worker.stop()
        self.hub.stop()
        self.tmp.cleanup()

    def test_worker_heartbeat_receives_nodes(self):
        state = self.worker.wait_worker_state(4.0)
        self.assertEqual(state.get("status"), "approved", state)
        ids = [n["node_id"] for n in state.get("nodes") or []]
        self.assertIn(self.worker.identity.node_id(), ids)
        self.assertIn(self.hub.identity.node_id(), ids)
        # set_worker_state 先更新内存状态再落盘 peers.json：轮询线程可能抢在写盘前
        # 看到 approved（Linux 容器里已实际踩到），这里给落盘留出有界等待。
        peers = self.worker.base / "peers.json"
        deadline = time.time() + 2.0
        while time.time() < deadline and not peers.exists():
            time.sleep(0.02)
        self.assertTrue(peers.exists(), "peers.json 未在 2s 内落盘")

    def test_hub_sees_worker(self):
        state = self.worker.wait_worker_state(4.0)
        self.assertEqual(state.get("status"), "approved", state)
        rows = self.hub.registry.snapshot()
        ids = [n["node_id"] for n in rows]
        self.assertIn(self.worker.identity.node_id(), ids)


def free_port():
    sock = socket.socket()
    try:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]
    finally:
        sock.close()


def wait_state(worker, predicate, timeout=6.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        state = worker.status().get("worker") or {}
        if state and predicate(state):
            return state
        time.sleep(0.05)
    return worker.status().get("worker") or {}


class WorkerStateRetentionTest(unittest.TestCase):
    """Hub 暂时离线/待审批时，保留最后一次成功心跳的已审批节点与 Hub 授权。"""

    SEED_LAST_OK = "2026-01-01T00:00:00+08:00"

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.worker = None
        self.hub = None

    def tearDown(self):
        if self.worker is not None:
            self.worker.stop()
        if self.hub is not None:
            self.hub.stop()
        self.tmp.cleanup()

    def _seed_approved(self):
        worker = ClusterService(base_dir=self.root / "worker")
        worker.set_worker_state(
            {
                "hub": "http://127.0.0.1:1",
                "hubs": [
                    {
                        "url": "http://127.0.0.1:1",
                        "node_id": "hub-0001",
                        "status": "approved",
                        "error": "",
                        "last_ok": self.SEED_LAST_OK,
                    }
                ],
                "hub_ids": ["hub-0001"],
                "status": "approved",
                "nodes": [
                    {"node_id": "hub-0001", "online": True},
                    {"node_id": "peer-0001", "online": True},
                ],
                "last_error": "",
                "last_ok": self.SEED_LAST_OK,
            }
        )
        return worker

    def _assert_seed_kept(self, state):
        ids = [n.get("node_id") for n in state.get("nodes") or []]
        self.assertIn("peer-0001", ids, state)
        self.assertIn("hub-0001", state.get("hub_ids") or [], state)
        self.assertEqual(state.get("last_ok"), self.SEED_LAST_OK, state)
        self.assertTrue(state.get("stale"), state)
        self.assertTrue(state.get("last_error"), state)

    def test_offline_hub_keeps_last_approved_nodes(self):
        self.worker = self._seed_approved()
        port = free_port()
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % port],
                    require_approval=False,
                )
            )
        )
        state = wait_state(
            self.worker,
            lambda s: s.get("status") == "error" and bool(s.get("hubs")),
        )
        self.assertEqual(state.get("status"), "error", state)
        self._assert_seed_kept(state)

    def test_pending_hub_keeps_last_approved_nodes(self):
        self.worker = self._seed_approved()
        self.hub = ClusterService(base_dir=self.root / "hub")
        self.assertTrue(self.hub.start(settings("hub", require_approval=True)))
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % self.hub.actual_port],
                    require_approval=False,
                )
            )
        )
        state = wait_state(
            self.worker, lambda s: s.get("status") == "pending"
        )
        self.assertEqual(state.get("status"), "pending", state)
        self._assert_seed_kept(state)
        self.assertIn(self.hub.identity.node_id(), state.get("hub_ids") or [])

    def test_recovered_hub_refreshes_state(self):
        self.worker = self._seed_approved()
        self.hub = ClusterService(base_dir=self.root / "hub")
        self.assertTrue(self.hub.start(settings("hub", require_approval=False)))
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % self.hub.actual_port],
                    require_approval=False,
                )
            )
        )
        state = wait_state(
            self.worker,
            lambda s: s.get("status") == "approved"
            and self.hub.identity.node_id() in (s.get("hub_ids") or []),
        )
        self.assertEqual(state.get("status"), "approved", state)
        self.assertFalse(state.get("stale"), state)
        self.assertIn(self.hub.identity.node_id(), state.get("hub_ids") or [])
        ids = [n.get("node_id") for n in state.get("nodes") or []]
        self.assertIn(self.worker.identity.node_id(), ids)
        self.assertIn(self.hub.identity.node_id(), ids)


class FakeBackend:
    def __init__(self):
        self.calls = []

    def handle_command(self, job, cmd, mount):
        self.calls.append(cmd)
        name = (cmd.get("params") or {}).get("file") or "out.xlsx"
        path = Path(mount) / name
        path.write_bytes(b"relay-payload")
        return {"file": str(path), "action": cmd.get("action")}


class RelayTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.old_data = os.environ.get("XDG_DATA_HOME")
        self.old_local = os.environ.get("LOCALAPPDATA")
        os.environ["XDG_DATA_HOME"] = str(Path(self.tmp.name) / "data")
        os.environ["LOCALAPPDATA"] = str(Path(self.tmp.name) / "data")

        self.hub = ClusterService(base_dir=Path(self.tmp.name) / "hub")
        self.assertTrue(
            self.hub.start(settings("hub", require_approval=False))
        )
        self.worker = ClusterService(base_dir=Path(self.tmp.name) / "worker")
        self.backend = FakeBackend()
        self.worker.set_runtime(
            JobManager(),
            lambda: {
                "fake_app": (
                    {"id": "fake_app", "name": "测试应用"},
                    self.backend,
                )
            },
        )
        self.assertTrue(
            self.worker.start(
                settings(
                    "worker",
                    hubs=["http://127.0.0.1:%d" % self.hub.actual_port],
                    require_approval=False,
                )
            )
        )
        # 等 Worker 入网并被 Hub 记录
        import time

        end = time.time() + 4.0
        while time.time() < end:
            entry = self.hub.registry.get(self.worker.identity.node_id())
            if entry and entry.get("online"):
                break
            time.sleep(0.05)
        self.assertIsNotNone(self.hub.registry.get(self.worker.identity.node_id()))

    def tearDown(self):
        self.worker.stop()
        self.hub.stop()
        if self.old_data is None:
            os.environ.pop("XDG_DATA_HOME", None)
        else:
            os.environ["XDG_DATA_HOME"] = self.old_data
        if self.old_local is None:
            os.environ.pop("LOCALAPPDATA", None)
        else:
            os.environ["LOCALAPPDATA"] = self.old_local
        self.tmp.cleanup()

    def test_remote_snapshot_result_and_artifact(self):
        mount = Path(self.tmp.name) / "usb"
        mount.mkdir()
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "relay-0001",
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
        self.assertEqual(result["node"], self.worker.identity.node_id())
        self.assertEqual(result["relayed_by"], self.hub.identity.node_id())
        self.assertEqual(result["data"]["file"], "out.xlsx")
        self.assertEqual(len(artifacts), 1)
        self.assertEqual(artifacts[0]["name"], "out.xlsx")
        self.hub.write_artifacts(str(mount), artifacts)
        self.assertEqual((mount / "out.xlsx").read_bytes(), b"relay-payload")
        self.assertEqual(len(self.backend.calls), 1)

    def test_cross_team_hub_relays_worker_team_command(self):
        """Hub 属于 A 班组，Worker 属于 B 班组：Hub 只中转，目标按自己的 team_id 校验。"""
        self.worker._settings.team_id = 4
        mount = Path(self.tmp.name) / "usb"
        mount.mkdir()
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "relay-cross-team",
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": self.worker.identity.node_id()},
                "params": {"team_id": 4, "file": "cross.xlsx"},
                "expires_at": "2099-01-01T00:00:00+08:00",
            }
        )
        result, artifacts = self.hub.relay_command(cmd, str(mount))
        self.assertTrue(result["ok"], result)
        self.assertEqual(result["node"], self.worker.identity.node_id())
        self.assertEqual(result["relayed_by"], self.hub.identity.node_id())
        self.assertEqual(len(artifacts), 1)
        self.assertEqual(artifacts[0]["name"], "cross.xlsx")
        self.assertEqual(len(self.backend.calls), 1)
        self.assertEqual(self.backend.calls[0]["params"]["team_id"], 4)

    def test_remote_team_mismatch_rejected(self):
        mount = Path(self.tmp.name) / "usb"
        mount.mkdir()
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "relay-0002",
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": self.worker.identity.node_id()},
                "params": {"team_id": 9},
            }
        )
        result, artifacts = self.hub.relay_command(cmd, str(mount))
        self.assertFalse(result["ok"])
        self.assertIn("班组", result["error"])
        self.assertEqual(artifacts, [])
        self.assertEqual(self.backend.calls, [])


class SettingsTest(unittest.TestCase):
    def test_validation(self):
        good = settings("hub")
        good.listen_port = 17890
        good.heartbeat_sec = 10
        good.offline_sec = 30
        self.assertEqual(good.validate(), "")
        empty = ClusterSettings(enabled=True)
        self.assertIn("集群标识", empty.validate())
        bad = settings("worker", hubs=[])
        bad.listen_port = 17890
        bad.heartbeat_sec = 10
        bad.offline_sec = 30
        self.assertIn("Hub", bad.validate())
        bad_secret = settings("hub")
        bad_secret.listen_port = 17890
        bad_secret.heartbeat_sec = 10
        bad_secret.offline_sec = 30
        bad_secret.secret = "short"
        self.assertIn("16", bad_secret.validate())

    def test_from_config_normalizes(self):
        cfg = {
            "cluster": {
                "enabled": True,
                "role": "Worker",
                "cluster_id": "c1",
                "secret": "x" * 20,
                "hubs": ["127.0.0.1:17890"],
            }
        }
        parsed = ClusterSettings.from_config(cfg)
        self.assertEqual(parsed.role, "worker")
        self.assertEqual(parsed.hubs, ["http://127.0.0.1:17890"])
        self.assertEqual(parsed.validate(), "")


if __name__ == "__main__":
    unittest.main()
