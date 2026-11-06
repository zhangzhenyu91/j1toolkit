# -*- coding: utf-8 -*-
"""集群节点更新：分片接收、校验落盘、Hub 按平台/架构推送（本机回环 HTTP）。"""

import base64
import hashlib
import os
import socket
import sys
import tempfile
import time
import unittest
from pathlib import Path

TOOLKIT_DIR = Path(__file__).resolve().parent.parent / "toolkit"
if str(TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLKIT_DIR))

from cluster.identity import NodeIdentity
from cluster.registry import NodeRegistry
from cluster.service import ClusterService, ClusterSettings
from jobs import JobManager


def _free_port() -> int:
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def _service(root: Path) -> ClusterService:
    return ClusterService(root, identity_obj=NodeIdentity(root))


class _StateIsolated(unittest.TestCase):
    """隔离 JobManager 日志到临时 state，避免污染同进程后续 clear_logs 测试。"""

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


class NodeUpdateTransferTests(_StateIsolated):
    def test_transfer_commit_writes_pending_and_verifies_sha(self):
        with tempfile.TemporaryDirectory() as tmp:
            node = _service(Path(tmp))
            info = node.identity.info(ClusterSettings())
            payload = b"update-payload-" + b"z" * 5000
            sha = hashlib.sha256(payload).hexdigest()
            node.handle_update_begin(
                {
                    "update_id": sha,
                    "filename": "pkg.deb",
                    "size": len(payload),
                    "sha256": sha,
                    "version": "2.2.0",
                    "platform": info["platform"],
                    "arch": info["arch"],
                },
                "hub",
            )
            split = len(payload) // 2
            for offset, part in ((0, payload[:split]), (split, payload[split:])):
                node.handle_update_chunk(
                    {
                        "update_id": sha,
                        "offset": offset,
                        "data_b64": base64.b64encode(part).decode("ascii"),
                    },
                    "hub",
                )
            pending = node.handle_update_commit({"update_id": sha}, "hub")
            self.assertEqual(Path(pending["path"]).read_bytes(), payload)
            self.assertEqual(node.pending_update()["sha256"], sha)
            self.assertIn(node.pending_update()["platform"], ("darwin", "linux", "windows"))

    def test_transfer_rejects_bad_sha(self):
        with tempfile.TemporaryDirectory() as tmp:
            node = _service(Path(tmp))
            payload = b"abc"
            sha = hashlib.sha256(b"other").hexdigest()
            node.handle_update_begin(
                {
                    "update_id": sha,
                    "filename": "pkg.deb",
                    "size": len(payload),
                    "sha256": sha,
                },
                "hub",
            )
            node.handle_update_chunk(
                {
                    "update_id": sha,
                    "offset": 0,
                    "data_b64": base64.b64encode(payload).decode("ascii"),
                },
                "hub",
            )
            with self.assertRaises(Exception):
                node.handle_update_commit({"update_id": sha}, "hub")
            self.assertEqual(node.pending_update(), {})


class HubUpdatePushTests(_StateIsolated):
    def test_hub_pushes_matching_package_to_peer(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            secret = "s" * 20
            port = _free_port()
            peer = _service(root / "peer")
            peer.set_runtime(JobManager(), lambda: {})
            peer_settings = ClusterSettings(
                enabled=True,
                role="hub",
                cluster_id="c1",
                secret=secret,
                listen_host="127.0.0.1",
                listen_port=port,
            )
            self.assertTrue(peer.start(peer_settings), peer.status().get("last_error"))
            try:
                hub = _service(root / "hub")
                hub.set_runtime(JobManager(), lambda: {})
                hub._settings = ClusterSettings(
                    enabled=True,
                    role="hub",
                    cluster_id="c1",
                    secret=secret,
                    listen_host="127.0.0.1",
                    listen_port=_free_port(),
                )
                hub.registry = NodeRegistry(root / "hub" / "registry.json")
                info = peer.node_info(peer_settings)
                hub.registry.touch(info, ip="127.0.0.1")
                hub.registry.approve(info["node_id"])
                package_file = root / ("uvmp-toolkit_2.2.0_%s_%s.pkg" % (info["platform"], info["arch"]))
                payload = b"pkg-" + b"q" * 7000
                package_file.write_bytes(payload)
                hub.add_update_package(
                    {
                        "path": str(package_file),
                        "platform": info["platform"],
                        "arch": info["arch"],
                        "version": "2.2.0",
                    }
                )
                started = hub.push_update({"nodes": [info["node_id"]]})
                status = None
                for _ in range(300):
                    status = hub.update_status(started["job_id"])
                    if status["job"]["status"] in ("success", "failed", "cancelled"):
                        break
                    time.sleep(0.05)
                self.assertEqual(status["job"]["status"], "success", status)
                self.assertEqual(status["job"]["result"]["ok"], 1, status)
                pending = peer.pending_update()
                self.assertEqual(pending["version"], "2.2.0")
                self.assertEqual(Path(pending["path"]).read_bytes(), payload)
                self.assertEqual(pending["sha256"], hashlib.sha256(payload).hexdigest())
            finally:
                peer.stop()

    def test_push_without_matching_package_reports_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            secret = "s" * 20
            port = _free_port()
            peer = _service(root / "peer")
            peer.set_runtime(JobManager(), lambda: {})
            peer_settings = ClusterSettings(
                enabled=True,
                role="hub",
                cluster_id="c1",
                secret=secret,
                listen_host="127.0.0.1",
                listen_port=port,
            )
            self.assertTrue(peer.start(peer_settings), peer.status().get("last_error"))
            try:
                hub = _service(root / "hub")
                hub.set_runtime(JobManager(), lambda: {})
                hub._settings = ClusterSettings(
                    enabled=True,
                    role="hub",
                    cluster_id="c1",
                    secret=secret,
                    listen_host="127.0.0.1",
                    listen_port=_free_port(),
                )
                hub.registry = NodeRegistry(root / "hub" / "registry.json")
                info = peer.node_info(peer_settings)
                hub.registry.touch(info, ip="127.0.0.1")
                hub.registry.approve(info["node_id"])
                package_file = root / "pkg.deb"
                package_file.write_bytes(b"pkg")
                hub.add_update_package(
                    {
                        "path": str(package_file),
                        "platform": "plan9",
                        "arch": "mips",
                        "version": "1.0",
                    }
                )
                started = hub.push_update({"nodes": [info["node_id"]]})
                status = None
                for _ in range(300):
                    status = hub.update_status(started["job_id"])
                    if status["job"]["status"] in ("success", "failed", "cancelled"):
                        break
                    time.sleep(0.05)
                self.assertEqual(status["job"]["status"], "success", status)
                self.assertEqual(status["job"]["result"]["failed"], 1, status)
                self.assertEqual(peer.pending_update(), {})
            finally:
                peer.stop()


if __name__ == "__main__":
    unittest.main()
