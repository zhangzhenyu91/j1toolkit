# -*- coding: utf-8 -*-
"""集群安全回归：审批授权、结果归属、分片 seq/缓存、心跳敏感变更、管理接口加固、更新预算。

全部用回环 HTTP 与 fake 后端，不读真实配置、不连云端/内网。
"""

import base64
import io
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
import zipfile
from pathlib import Path
from unittest import mock

TOOLKIT_DIR = Path(__file__).resolve().parent.parent / "toolkit"
if str(TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLKIT_DIR))

import usb_protocol  # noqa: E402
from cluster import export as cluster_export  # noqa: E402
from cluster import protocol  # noqa: E402
from cluster.export import ClusterExport  # noqa: E402
from cluster.identity import NodeIdentity  # noqa: E402
from cluster.registry import NodeRegistry  # noqa: E402
from cluster.service import (  # noqa: E402
    HEADER_ADMIN_TOKEN,
    ClusterService,
    ClusterSettings,
)
from jobs import JobManager  # noqa: E402

SECRET = "security-test-secret-0123456789"
CLUSTER = "cluster-sec"


def settings(role="hub", port=0, hubs=None, require_approval=True, team_id=3):
    return ClusterSettings(
        enabled=True,
        role=role,
        cluster_id=CLUSTER,
        secret=SECRET,
        node_name="安全测试-" + role,
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


def node_info(node_id="worker-0001", role="worker", team_id=3, port=18001):
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
        "port": port,
    }


def _request(port, path, body=None, headers=None, method=None):
    raw = protocol.json_bytes(body) if body is not None else None
    method = method or ("POST" if raw is not None else "GET")
    req = urllib.request.Request(
        "http://127.0.0.1:%d%s" % (port, path),
        data=raw,
        headers=headers or {},
        method=method,
    )
    try:
        with urllib.request.urlopen(req, timeout=3) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        try:
            payload = json.loads(e.read().decode("utf-8", "replace"))
        except ValueError:
            payload = {}
        finally:
            e.close()
        return e.code, payload


def signed(port, path, body, node_id, receiver=""):
    raw = protocol.json_bytes(body if body is not None else {})
    headers = protocol.build_headers(
        SECRET, CLUSTER, node_id, "POST", path, raw, receiver=receiver
    )
    return _request(port, path, body if body is not None else {}, headers=headers)


def admin_headers(service, host="127.0.0.1", token=None):
    return {
        "Content-Type": "application/json",
        "Host": host,
        HEADER_ADMIN_TOKEN: token if token is not None else service._admin_token(),
    }


def seed_hub(service, *hub_ids):
    """把节点标记为已加入这些 Hub（等价于心跳成功后的状态）。"""
    service.set_worker_state(
        {
            "hub": hub_ids[0] if hub_ids else "",
            "hubs": list(hub_ids),
            "hub_ids": list(hub_ids),
            "status": "approved",
            "nodes": [],
            "last_error": "",
        }
    )


class _FakeExportBackend:
    """按分片写逐单 PDF；可注入额外行模拟越权 seq。"""

    def __init__(self, extra_rows=None):
        self.extra_rows = list(extra_rows or [])
        self.calls = 0

    def run_cluster(self, job, params):
        self.calls += 1
        files_dir = Path(params["outdir"]) / "逐单"
        files_dir.mkdir(parents=True, exist_ok=True)
        rows = []
        for item in params["codes"]:
            name = "%03d_派车单_%s.pdf" % (int(item["seq"]), item["code"])
            (files_dir / name).write_bytes(b"%PDF-1.4\n")
            rows.append(
                {
                    "seq": int(item["seq"]),
                    "code": str(item["code"]),
                    "pcd_file": name,
                    "pcd_status": "成功",
                    "track_file": "",
                    "track_status": "",
                }
            )
        rows.extend(self.extra_rows)
        return {"rows": rows, "not_found": [], "noon_rows": []}

    def finalize_cluster(
        self,
        outdir,
        batch_name,
        batch_subdir,
        started_at,
        rows,
        not_found,
        noon_rows,
        files,
        export_track=True,
        log=print,
    ):
        return {"total": len(rows), "files": [Path(p).name for p in files]}


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


class ReceiverBindingTest(unittest.TestCase):
    def test_bound_signature_rejects_other_receiver(self):
        body = b'{"node":1}'
        headers = protocol.build_headers(
            SECRET, CLUSTER, "node-a", "POST", "/cluster/join", body,
            receiver="node-b",
        )
        self.assertIn(protocol.HEADER_RECEIVER, headers)
        with self.assertRaises(protocol.ClusterAuthError):
            protocol.verify_headers(
                SECRET, CLUSTER, "POST", "/cluster/join", headers, body,
                protocol.NonceCache(), receiver_id="node-a",
            )
        self.assertEqual(
            protocol.verify_headers(
                SECRET, CLUSTER, "POST", "/cluster/join", headers, body,
                protocol.NonceCache(), receiver_id="node-b",
            ),
            "node-a",
        )

    def test_legacy_signature_still_accepted(self):
        body = b"{}"
        headers = protocol.build_headers(
            SECRET, CLUSTER, "node-a", "POST", "/cluster/join", body
        )
        self.assertNotIn(protocol.HEADER_RECEIVER, headers)
        self.assertEqual(
            protocol.verify_headers(
                SECRET, CLUSTER, "POST", "/cluster/join", headers, body,
                protocol.NonceCache(), receiver_id="node-b",
            ),
            "node-a",
        )

    def test_node_info_advertises_binding_cap(self):
        identity = NodeIdentity(Path(tempfile.mkdtemp()))
        info = identity.info(settings("worker"), port=18001)
        self.assertIn(protocol.CAP_RECEIVER_BINDING, info["caps"])
        self.assertIn(
            protocol.CAP_RECEIVER_BINDING,
            protocol.validate_node_info(info)["caps"],
        )


class CommandAuthorizationTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        self.hub = ClusterService(Path(self.tmp.name) / "hub")
        self.hub.set_runtime(JobManager(), lambda: {})
        self.assertTrue(self.hub.start(settings("hub")))
        self.port = self.hub.actual_port

    def tearDown(self):
        self.hub.stop()
        self.tmp.cleanup()
        super().tearDown()

    def _command(self, target, team_id=3):
        return usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "sec-0001",
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": target},
                "params": {"team_id": team_id},
                "expires_at": "2099-01-01T00:00:00+08:00",
            }
        )

    def _join(self, info):
        return signed(self.port, "/cluster/join", {"node": info}, info["node_id"])

    def test_command_requires_approved_requester(self):
        status, _ = signed(
            self.port,
            "/cluster/command",
            {"command": self._command(self.hub.identity.node_id())},
            "stranger-0001",
        )
        self.assertEqual(status, 403)

    def test_command_from_approved_worker_denied(self):
        info = node_info("worker-0001", role="worker")
        self._join(info)
        self.hub.registry.approve(info["node_id"])
        status, _ = signed(
            self.port,
            "/cluster/command",
            {"command": self._command(self.hub.identity.node_id())},
            info["node_id"],
        )
        self.assertEqual(status, 403)

    def test_command_from_approved_hub_allowed(self):
        info = node_info("hub-0002", role="hub", port=18002)
        self._join(info)
        self.hub.registry.approve(info["node_id"])
        status, payload = signed(
            self.port,
            "/cluster/command",
            {"command": self._command(self.hub.identity.node_id())},
            info["node_id"],
        )
        self.assertEqual(status, 200, payload)

    def test_update_requires_hub(self):
        info = node_info("worker-0001", role="worker")
        self._join(info)
        self.hub.registry.approve(info["node_id"])
        status, _ = signed(
            self.port,
            "/cluster/update/begin",
            {"update_id": "a" * 64, "filename": "p.deb", "size": 1},
            info["node_id"],
        )
        self.assertEqual(status, 403)

    def test_join_other_team_allowed_for_cross_team_hub(self):
        """跨班组 Hub 中转是既有能力：入网不按班组拦截，班组在指令层校验。"""
        info = node_info("worker-0009", team_id=9)
        status, payload = signed(
            self.port, "/cluster/join", {"node": info}, info["node_id"]
        )
        self.assertEqual(status, 200, payload)
        self.assertIn(payload["data"]["status"], ("pending", "approved"))
        self.assertEqual(self.hub.registry.get("worker-0009")["team_id"], 9)

    def test_join_without_team_id_rejected(self):
        info = node_info("worker-0010")
        info["team_id"] = 0
        status, _ = signed(
            self.port, "/cluster/join", {"node": info}, info["node_id"]
        )
        self.assertEqual(status, 400)

    def test_command_without_team_id_rejected(self):
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": "sec-0002",
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": self.hub.identity.node_id()},
                "params": {},
            }
        )
        data = self.hub.handle_remote_command({"command": cmd}, "hub-0002")
        self.assertFalse(data["result"]["ok"])
        self.assertIn("team_id", data["result"]["error"])


class _FakeCommandBackend:
    """最小指令后端：写一个产物文件并返回路径。"""

    def __init__(self):
        self.calls = []

    def handle_command(self, job, cmd, mount):
        self.calls.append(cmd["id"])
        path = Path(mount) / "out.xlsx"
        path.write_bytes(b"result-payload")
        return {"file": str(path)}


class ResultOwnershipTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.hub = ClusterService(root / "hub")
        self.hub.set_runtime(
            JobManager(),
            lambda: {
                "fake_app": (
                    {"id": "fake_app", "name": "测试"},
                    _FakeCommandBackend(),
                )
            },
        )
        self.assertTrue(self.hub.start(settings("hub")))
        self.port = self.hub.actual_port
        for node_id, role, port in (
            ("hub-owner", "hub", 18010),
            ("hub-other", "hub", 18011),
            ("worker-0001", "worker", 18012),
        ):
            self.hub.registry.touch(node_info(node_id, role=role, port=port))
            self.hub.registry.approve(node_id)

    def tearDown(self):
        self.hub.stop()
        self.tmp.cleanup()
        super().tearDown()

    def _store(self, cmd_id="own-0001", requester="hub-owner"):
        cmd = usb_protocol.normalize_command(
            {
                "version": 1,
                "id": cmd_id,
                "app": "fake_app",
                "action": "snapshot",
                "cluster": CLUSTER,
                "target": {"node_id": self.hub.identity.node_id()},
                "params": {"team_id": 3},
            }
        )
        self.hub.handle_remote_command({"command": cmd}, requester)
        return cmd_id

    def test_owner_and_hub_can_read_worker_cannot(self):
        cmd_id = self._store()
        status, _ = signed(
            self.port, "/cluster/command/%s" % cmd_id, {}, "worker-0001"
        )
        self.assertEqual(status, 403)
        status, payload = signed(
            self.port, "/cluster/command/%s" % cmd_id, {}, "hub-owner"
        )
        self.assertEqual(status, 200, payload)
        status, payload = signed(
            self.port, "/cluster/command/%s" % cmd_id, {}, "hub-other"
        )
        self.assertEqual(status, 200, payload)

    def test_result_meta_records_requester(self):
        cmd_id = self._store()
        meta = json.loads(
            (self.hub.base / "results" / (cmd_id + ".json")).read_text("utf-8")
        )
        self.assertEqual(meta["requester"], "hub-owner")

    def _write_legacy_result(self, cmd_id, node="worker-0001"):
        root = self.hub.base / "results"
        root.mkdir(parents=True, exist_ok=True)
        (root / (cmd_id + ".json")).write_text(
            json.dumps(
                {
                    "result": {
                        "version": 1,
                        "id": cmd_id,
                        "app": "fake_app",
                        "action": "snapshot",
                        "ok": True,
                        "node": node,
                        "target": node,
                        "data": {},
                    },
                    "artifacts": [],
                }
            ),
            encoding="utf-8",
        )

    def test_legacy_result_readable_by_hub_with_audit(self):
        """升级前的结果没有 requester：合法 Hub 仍可读回，但必须留审计。"""
        self._write_legacy_result("legacy-0001")
        with mock.patch.object(self.hub, "log_info") as log_info:
            status, payload = signed(
                self.port, "/cluster/command/legacy-0001", {}, "hub-owner"
            )
        self.assertEqual(status, 200, payload)
        self.assertTrue(payload["data"]["result"]["ok"])
        text = " ".join(str(call.args) for call in log_info.call_args_list)
        self.assertIn("legacy-0001", text)
        self.assertIn("hub-owner", text)

    def test_legacy_result_readable_by_target_node(self):
        self._write_legacy_result("legacy-0002", node="worker-0001")
        with mock.patch.object(self.hub, "log_info"):
            status, payload = signed(
                self.port, "/cluster/command/legacy-0002", {}, "worker-0001"
            )
        self.assertEqual(status, 200, payload)

    def test_legacy_result_denied_for_other_worker(self):
        info = node_info("worker-0013", role="worker", port=18013)
        self.hub.registry.touch(info)
        self.hub.registry.approve(info["node_id"])
        self._write_legacy_result("legacy-0003", node="worker-0001")
        status, _ = signed(
            self.port, "/cluster/command/legacy-0003", {}, info["node_id"]
        )
        self.assertEqual(status, 403)


class ExportAuthorizationTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.backend = _FakeExportBackend()

    def tearDown(self):
        self.tmp.cleanup()
        super().tearDown()

    def _peer(self, name="peer"):
        peer = ClusterService(self.root / name, NodeIdentity(self.root / name))
        peer.set_runtime(
            JobManager(), lambda: {"pcd_export": ({"id": "pcd_export"}, self.backend)}
        )
        return peer

    def _body(self, job_id="job-0001", chunk_id="c0001"):
        return {
            "job_id": job_id,
            "chunk": {"id": chunk_id, "items": [{"seq": 1, "code": "A1"}]},
            "options": {"export_pcd": True, "export_track": False},
            "concurrency": {},
        }

    def test_export_run_requires_approved_peer(self):
        peer = self._peer()
        seed_hub(peer, "hub-0001")
        peer._settings = settings("worker")
        raw = protocol.json_bytes(self._body())
        headers = protocol.build_headers(
            SECRET, CLUSTER, "hub-9999", "POST", "/cluster/export/run", raw
        )
        with self.assertRaises(protocol.ClusterForbiddenError):
            peer.handle_public(
                "POST", "/cluster/export/run", raw, headers, "127.0.0.1"
            )
        headers = protocol.build_headers(
            SECRET, CLUSTER, "hub-0001", "POST", "/cluster/export/run", raw
        )
        data = peer.handle_public(
            "POST", "/cluster/export/run", raw, headers, "127.0.0.1"
        )
        self.assertIn("result", data)

    def test_export_run_allows_worker_coordinator(self):
        """发起方可以是 Worker：由 Hub 下发的节点表即为审批依据。"""
        peer = self._peer()
        seed_hub(peer, "hub-0001")
        peer.set_worker_state(
            {
                "hub": "http://hub",
                "hub_ids": ["hub-0001"],
                "status": "approved",
                "nodes": [{"node_id": "coord-worker"}, {"node_id": "peer"}],
                "last_error": "",
            }
        )
        peer._settings = settings("worker")
        raw = protocol.json_bytes(self._body())
        headers = protocol.build_headers(
            SECRET, CLUSTER, "coord-worker", "POST", "/cluster/export/run", raw
        )
        data = peer.handle_public(
            "POST", "/cluster/export/run", raw, headers, "127.0.0.1"
        )
        self.assertIn("result", data)

    def test_chunk_cache_owner_mismatch_forbidden(self):
        peer = self._peer()
        seed_hub(peer, "hub-0001", "hub-0002")
        peer.handle_export_run(self._body(), "hub-0001")
        with self.assertRaises(protocol.ClusterForbiddenError):
            peer.handle_export_run(self._body(), "hub-0002")

    def test_chunk_cache_without_owner_recomputed(self):
        """升级前的分片缓存没有 owner.json：删除重算，不能被永久 403 卡死。"""
        peer = self._peer()
        chunk_dir = peer.base / "jobs" / "job-0001" / "chunks" / "c0001"
        files_dir = chunk_dir / "逐单"
        files_dir.mkdir(parents=True)
        (files_dir / "001_旧缓存.pdf").write_bytes(b"%PDF-1.4\nold")
        (chunk_dir / "result.json").write_text(
            json.dumps(
                {
                    "rows": [
                        {"seq": 1, "code": "旧缓存", "pcd_status": "成功"},
                    ],
                    "not_found": [],
                    "noon_rows": [],
                },
                ensure_ascii=False,
            ),
            encoding="utf-8",
        )
        data = peer.handle_export_run(self._body(), "hub-0001")
        self.assertEqual(self.backend.calls, 1)
        codes = [row["code"] for row in data["result"]["rows"]]
        self.assertEqual(codes, ["A1"])
        self.assertTrue((chunk_dir / "owner.json").exists())
        self.assertFalse((files_dir / "001_旧缓存.pdf").exists())
        # 重算后归属已落盘：同一发起方复用缓存，他人仍被拒。
        peer.handle_export_run(self._body(), "hub-0001")
        self.assertEqual(self.backend.calls, 1)
        with self.assertRaises(protocol.ClusterForbiddenError):
            peer.handle_export_run(self._body(), "hub-0002")

    def test_chunk_cache_without_owner_field_recomputed(self):
        """owner.json 存在但缺 requester（半写坏文件）同样按无归属处理。"""
        peer = self._peer()
        chunk_dir = peer.base / "jobs" / "job-0001" / "chunks" / "c0001"
        chunk_dir.mkdir(parents=True)
        (chunk_dir / "owner.json").write_text(
            json.dumps({"items": "sha256:broken"}), encoding="utf-8"
        )
        data = peer.handle_export_run(self._body(), "hub-0001")
        self.assertEqual(self.backend.calls, 1)
        self.assertEqual(
            [row["code"] for row in data["result"]["rows"]], ["A1"]
        )
        owner = json.loads((chunk_dir / "owner.json").read_text("utf-8"))
        self.assertEqual(owner["requester"], "hub-0001")

    def test_export_cancel_denied_for_other_worker(self):
        peer = self._peer("peer-cancel")
        seed_hub(peer, "hub-0001")
        peer.set_worker_state(
            {
                "hub": "http://hub",
                "hub_ids": ["hub-0001"],
                "status": "approved",
                "nodes": [{"node_id": "coord"}, {"node_id": "other-worker"}],
                "last_error": "",
            }
        )
        job = JobManager().create("pcd_export", "占位")
        peer._export_runs["job-0001/c0001"] = {"job": job, "requester": "coord"}
        with self.assertRaises(protocol.ClusterForbiddenError):
            peer.handle_export_cancel(
                {"job_id": "job-0001", "chunk_ids": ["c0001"]}, "other-worker"
            )
        data = peer.handle_export_cancel(
            {"job_id": "job-0001", "chunk_ids": ["c0001"]}, "coord"
        )
        self.assertEqual(data["canceled"], 1)

    def test_foreign_seq_dropped_on_peer(self):
        backend = _FakeExportBackend(
            extra_rows=[
                {
                    "seq": 99,
                    "code": "OTHER",
                    "pcd_file": "",
                    "pcd_status": "成功",
                    "track_status": "",
                }
            ]
        )
        self.backend = backend
        peer = self._peer()
        seed_hub(peer, "hub-0001")
        data = peer.handle_export_run(self._body(), "hub-0001")
        seqs = [row["seq"] for row in data["result"]["rows"]]
        self.assertEqual(seqs, [1])

    def test_foreign_seq_dropped_on_coordinator(self):
        coordinator = ClusterService(
            self.root / "coord", NodeIdentity(self.root / "coord")
        )
        coordinator.set_runtime(JobManager(), lambda: {})
        job = coordinator.jobs.create("pcd_export", "越权分片")
        params = {
            "codes": [{"seq": 1, "code": "A1"}],
            "outdir": str(self.root / "out"),
            "local_node_id": "coord",
            "export_pcd": True,
            "export_track": False,
        }
        export = ClusterExport(
            coordinator, job, params, [], lambda *a, **k: {}, self.backend,
            chunk_size=1,
        )
        chunk = dict(export.chunks[0])
        chunk["attempts"] = 1
        export._accept_result(
            chunk,
            {
                "rows": [
                    {"seq": 1, "code": "A1", "pcd_status": "成功", "pcd_file": ""},
                    {
                        "seq": 99,
                        "code": "OTHER",
                        "pcd_status": "成功",
                        "pcd_file": "",
                    },
                ],
                "not_found": [],
                "noon_rows": [{"seq": 99}, {"seq": 1}],
            },
            "",
            {"node_id": "peer1"},
        )
        self.assertEqual(sorted(export.rows), [1])
        self.assertEqual(sorted(export.noon), [1])

    def test_zip_total_budget_limited(self):
        coordinator = ClusterService(
            self.root / "coord2", NodeIdentity(self.root / "coord2")
        )
        coordinator.set_runtime(JobManager(), lambda: {})
        job = coordinator.jobs.create("pcd_export", "zip 预算")
        params = {
            "codes": [{"seq": 1, "code": "A1"}],
            "outdir": str(self.root / "out2"),
            "local_node_id": "coord2",
            "export_pcd": True,
            "export_track": False,
        }
        export = ClusterExport(
            coordinator, job, params, [], lambda *a, **k: {}, self.backend,
            chunk_size=1,
        )
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
            for index in range(4):
                archive.writestr("f%d.pdf" % index, b"x" * 800)
        raw = base64.b64encode(buffer.getvalue()).decode("ascii")
        with mock.patch.object(cluster_export, "MAX_ZIP_TOTAL_BYTES", 1000):
            self.assertEqual(export._extract_zip("c0001", raw), {})

    def test_export_files_cleaned_after_finalize(self):
        coordinator = ClusterService(
            self.root / "coord3", NodeIdentity(self.root / "coord3")
        )
        coordinator.set_runtime(JobManager(), lambda: {})
        job = coordinator.jobs.create("pcd_export", "清理")
        peer = self._peer("peer3")
        seed_hub(peer, "hub-0001")
        nodes = [
            {
                "node_id": "peer3",
                "name": "peer3",
                "url": "http://peer3",
                "query_workers": 1,
                "render_workers": 1,
            }
        ]
        params = {
            "codes": [{"seq": 1, "code": "A1"}],
            "outdir": str(self.root / "out3"),
            "local_node_id": "coord3",
            "export_pcd": True,
            "export_track": False,
        }
        export = ClusterExport(
            coordinator,
            job,
            params,
            nodes,
            lambda url, path, body, timeout=None: peer.handle_export_run(
                body, "hub-0001"
            ),
            self.backend,
            chunk_size=1,
        )
        coordinator.jobs.run_sync(job, lambda _job: export.run(), echo=False)
        self.assertFalse(export.files_root.exists())


class HeartbeatSensitiveChangeTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        self.hub = ClusterService(Path(self.tmp.name) / "hub")
        self.assertTrue(self.hub.start(settings("hub")))
        self.port = self.hub.actual_port

    def tearDown(self):
        self.hub.stop()
        self.tmp.cleanup()
        super().tearDown()

    def test_sensitive_change_cleared_after_reapproval(self):
        info = node_info("worker-0002")
        signed(self.port, "/cluster/join", {"node": info}, info["node_id"])
        self.hub.registry.approve(info["node_id"])
        signed(
            self.port,
            "/cluster/heartbeat",
            {"node": node_info("worker-0002", port=18098)},
            info["node_id"],
        )
        self.assertIn(
            "port", self.hub.registry.get("worker-0002").get("pending_change") or []
        )
        self.hub.registry.approve("worker-0002")
        entry = self.hub.registry.get("worker-0002")
        self.assertTrue(entry["approved"])
        self.assertNotIn("pending_change", entry)

    def test_sensitive_change_revokes_approval(self):
        info = node_info("worker-0001")
        signed(self.port, "/cluster/join", {"node": info}, info["node_id"])
        self.hub.registry.approve(info["node_id"])
        status, payload = signed(
            self.port,
            "/cluster/heartbeat",
            {"node": node_info("worker-0001", team_id=3, port=18099)},
            info["node_id"],
        )
        self.assertEqual(status, 200, payload)
        self.assertEqual(payload["data"]["status"], "pending")
        entry = self.hub.registry.get("worker-0001")
        self.assertFalse(entry["approved"])
        self.assertIn("port", entry.get("pending_change") or [])

    def test_role_change_revokes_approval(self):
        info = node_info("hub-0003", role="hub", port=18003)
        signed(self.port, "/cluster/join", {"node": info}, info["node_id"])
        self.hub.registry.approve(info["node_id"])
        _, payload = signed(
            self.port,
            "/cluster/heartbeat",
            {"node": node_info("hub-0003", role="worker", port=18003)},
            info["node_id"],
        )
        self.assertEqual(payload["data"]["status"], "pending")


class LocalRpcAdminTest(_StateIsolated):
    """RPC 子进程走 urllib 直连本机管理接口（不经浏览器），必须仍然可用。"""

    def setUp(self):
        super().setUp()
        import config

        self.config = config
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        # 状态目录与配置都指向临时目录：local_* 走的是全局 identity.cluster_dir()。
        state_home = self.root / "state"
        os.environ["XDG_DATA_HOME"] = str(state_home)
        os.environ["LOCALAPPDATA"] = str(state_home)
        os.environ["APPDATA"] = str(state_home)
        self.old_cfg = os.environ.get("UVMP_TOOLKIT_CONFIG")
        os.environ["UVMP_TOOLKIT_CONFIG"] = str(self.root / "config.json")
        cfg = config.load()
        cfg["cluster"] = {
            "enabled": True,
            "role": "hub",
            "cluster_id": CLUSTER,
            "secret": SECRET,
            "listen_host": "127.0.0.1",
            "listen_port": 0,
            "team_id": 3,
            "require_approval": True,
            "heartbeat_sec": 10,
            "offline_sec": 30,
        }
        config.save(cfg)
        from cluster import identity as cluster_identity

        self.hub = ClusterService(cluster_identity.cluster_dir())
        self.assertTrue(self.hub.start(settings("hub", port=0)))

    def tearDown(self):
        self.hub.stop()
        if self.old_cfg is None:
            os.environ.pop("UVMP_TOOLKIT_CONFIG", None)
        else:
            os.environ["UVMP_TOOLKIT_CONFIG"] = self.old_cfg
        self.tmp.cleanup()
        super().tearDown()

    def test_local_status_reads_admin_endpoint(self):
        from cluster import service as cluster_service

        data = cluster_service.local_status(self.config.load())
        self.assertTrue(data["service"]["running"], data)
        self.assertEqual(data["service"]["cluster_id"], CLUSTER)

    def test_local_admin_approves_node(self):
        from cluster import service as cluster_service

        info = node_info("worker-rpc-01")
        signed(self.hub.actual_port, "/cluster/join", {"node": info}, info["node_id"])
        cluster_service.local_admin("approve", info["node_id"])
        self.assertTrue(self.hub.registry.is_approved(info["node_id"]))


class AdminHardeningTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        self.hub = ClusterService(Path(self.tmp.name) / "hub")
        self.assertTrue(self.hub.start(settings("hub")))
        self.port = self.hub.actual_port

    def tearDown(self):
        self.hub.stop()
        self.tmp.cleanup()
        super().tearDown()

    def test_bad_host_rejected(self):
        status, _ = _request(
            self.port,
            "/cluster/admin/state",
            headers=admin_headers(self.hub, host="evil.example"),
        )
        self.assertEqual(status, 403)

    def test_missing_token_rejected(self):
        headers = admin_headers(self.hub)
        headers.pop(HEADER_ADMIN_TOKEN)
        status, _ = _request(self.port, "/cluster/admin/state", headers=headers)
        self.assertEqual(status, 403)

    def test_wrong_token_rejected(self):
        status, _ = _request(
            self.port,
            "/cluster/admin/state",
            headers=admin_headers(self.hub, token="0" * 64),
        )
        self.assertEqual(status, 403)

    def test_valid_token_allowed(self):
        status, payload = _request(
            self.port, "/cluster/admin/state", headers=admin_headers(self.hub)
        )
        self.assertEqual(status, 200, payload)
        self.assertTrue(payload["ok"])

    def test_cross_site_origin_rejected(self):
        headers = admin_headers(self.hub)
        headers["Origin"] = "http://evil.example"
        status, _ = _request(self.port, "/cluster/admin/state", headers=headers)
        self.assertEqual(status, 403)

    def test_origin_other_local_port_rejected(self):
        headers = admin_headers(self.hub)
        headers["Origin"] = "http://127.0.0.1:3000"
        status, _ = _request(self.port, "/cluster/admin/state", headers=headers)
        self.assertEqual(status, 403)

    def test_origin_same_port_allowed(self):
        headers = admin_headers(self.hub)
        headers["Origin"] = "http://127.0.0.1:%d" % self.port
        status, payload = _request(
            self.port, "/cluster/admin/state", headers=headers
        )
        self.assertEqual(status, 200, payload)

    def test_fetch_cross_site_rejected(self):
        headers = admin_headers(self.hub)
        headers["Sec-Fetch-Site"] = "cross-site"
        status, _ = _request(self.port, "/cluster/admin/state", headers=headers)
        self.assertEqual(status, 403)

    def test_non_json_content_type_rejected(self):
        headers = admin_headers(self.hub)
        headers["Content-Type"] = "text/plain"
        status, _ = _request(
            self.port,
            "/cluster/admin/update/packages",
            body={"path": "/tmp/x"},
            headers=headers,
        )
        self.assertEqual(status, 403)

    def test_token_file_is_private(self):
        """令牌文件只对本机用户可读：POSIX 校验权限位，Windows 校验 ACL。"""
        self.hub._admin_token()
        path = self.hub.base / "admin_token"
        self.assertTrue(path.is_file())
        if os.name == "nt":
            self._assert_windows_acl_private(path)
        else:
            self.assertEqual(path.stat().st_mode & 0o077, 0)

    def _assert_windows_acl_private(self, path):
        """Windows 用 ACL 表达权限：只允许本机用户/系统/管理员，且断开继承。

        宽泛组（Users / Authenticated Users / Everyone）能读就等于令牌泄露，
        所以按 SDDL 逐条断言受托人 SID（SDDL 不含本地化名称，任何语言的
        Windows 都能解析），而不是跳过。
        """
        script = (
            "$id=[System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value;"
            "$a=Get-Acl -LiteralPath '%s';"
            "'SID='+$id;"
            "'PROTECTED='+[bool]$a.AreAccessRulesProtected;"
            "'SDDL='+$a.Sddl" % path
        )
        proc = subprocess.run(
            ["powershell", "-NoProfile", "-NonInteractive", "-Command", script],
            capture_output=True,
            text=True,
            errors="replace",
            timeout=60,
        )
        self.assertEqual(0, proc.returncode, proc.stdout + proc.stderr)
        values = {}
        for line in (proc.stdout or "").splitlines():
            if "=" in line:
                key, _, value = line.partition("=")
                values[key.strip()] = value.strip()
        self.assertIn("SDDL", values, proc.stdout)
        self.assertEqual(
            "True", values.get("PROTECTED"), "令牌文件不应继承目录 ACL: %s" % values
        )
        trustees = set()
        for ace in values["SDDL"].split("D:")[-1].split("(")[1:]:
            trustees.add(ace.rstrip(")").split(";")[-1])
        # SDDL 里内置主体可能写成别名（SY/BA）也可能保留字面 SID，统一归一后再比。
        aliases = {
            "SY": "S-1-5-18",
            "BA": "S-1-5-32-544",
            "BU": "S-1-5-32-545",
            "AU": "S-1-5-11",
            "WD": "S-1-1-0",
        }
        normalized = {aliases.get(item, item) for item in trustees}
        self.assertIn(values.get("SID"), normalized, values["SDDL"])
        # 允许集合之外一律算失败：尤其不能出现 BU/AU/WD 这类宽泛组。
        allowed = {
            values.get("SID"),
            "S-1-5-18",  # SYSTEM
            "S-1-5-32-544",  # Administrators
            "S-1-3-4",  # OWNER RIGHTS（部分系统自动附加）
        }
        self.assertEqual(
            set(),
            normalized - allowed,
            "令牌文件出现了不该有权限的受托人: %s" % values["SDDL"],
        )

    def test_windows_acl_restriction_commands(self):
        """Windows 分支在本机不执行，但命令必须正确：按 SID 授权再断开继承。

        不做这一步，Windows 上 0600 完全不生效——令牌文件会跟随状态目录的
        宽松 ACL，任何本地账户都能读。这里用 mock 固定命令形态，避免只在
        Windows CI 上才发现写错。
        """
        from cluster import service as cluster_module

        calls = []

        def fake_run(args, **kwargs):
            calls.append(list(args))
            return mock.Mock(returncode=0, stdout="", stderr="")

        with mock.patch.object(cluster_module.os, "name", "nt"), mock.patch.object(
            cluster_module, "_WINDOWS_USER_SID", "S-1-5-21-111-222-333-1001"
        ), mock.patch.object(cluster_module.subprocess, "run", fake_run):
            cluster_module._restrict_windows_acl(Path("C:/state/admin_token"))

        self.assertEqual(2, len(calls), calls)
        self.assertEqual("icacls", calls[0][0])
        self.assertIn("*S-1-5-21-111-222-333-1001:(F)", calls[0])
        self.assertIn("*S-1-5-18:(F)", calls[0])
        self.assertIn("/grant:r", calls[0])
        self.assertEqual(["icacls", calls[1][1], "/inheritance:r"], calls[1])

    def test_windows_acl_skipped_without_sid(self):
        """取不到 SID 时宁可不动 ACL：错误授权会把本机用户自己挡在文件外。"""
        from cluster import service as cluster_module

        calls = []
        with mock.patch.object(cluster_module.os, "name", "nt"), mock.patch.object(
            cluster_module, "_windows_user_sid", lambda: ""
        ), mock.patch.object(
            cluster_module.subprocess,
            "run",
            lambda args, **kwargs: calls.append(args),
        ):
            cluster_module._restrict_windows_acl(Path("C:/state/admin_token"))
        self.assertEqual([], calls)

    def test_windows_acl_noop_on_posix(self):
        """POSIX 上由 0600 承担，不应调用 icacls。"""
        from cluster import service as cluster_module

        calls = []
        with mock.patch.object(
            cluster_module.subprocess,
            "run",
            lambda args, **kwargs: calls.append(args),
        ):
            cluster_module._restrict_windows_acl(Path("/tmp/admin_token"))
        self.assertEqual([], calls)

    def test_admin_token_restricts_acl_on_read(self):
        """已存在的令牌文件在首次读取时也要收紧，不能只管新建分支。"""
        from cluster import service as cluster_module

        path = self.hub.base / "admin_token"
        path.write_text("b" * 64, encoding="utf-8")
        self.hub._admin_token_cache = ""
        self.hub._admin_acl_checked = False
        tightened = []
        with mock.patch.object(
            cluster_module,
            "_restrict_windows_acl",
            lambda p: tightened.append(Path(p)),
        ):
            self.assertEqual("b" * 64, self.hub._admin_token())
        self.assertEqual([path], tightened)

    def test_business_error_returns_400(self):
        status, payload = _request(
            self.port,
            "/cluster/admin/export/start",
            body={},
            headers=admin_headers(self.hub),
        )
        self.assertEqual(status, 400, payload)
        self.assertIn("派车单", payload.get("error", ""))

    def test_bad_content_length_returns_400(self):
        sock = socket.create_connection(("127.0.0.1", self.port), timeout=3)
        try:
            sock.sendall(
                b"POST /cluster/admin/state HTTP/1.1\r\n"
                b"Host: 127.0.0.1\r\nContent-Length: abc\r\n\r\n"
            )
            data = sock.recv(4096)
        finally:
            sock.close()
        self.assertIn(b"400", data.split(b"\r\n", 1)[0])


class UpdateBudgetTest(_StateIsolated):
    def setUp(self):
        super().setUp()
        self.tmp = tempfile.TemporaryDirectory()
        self.node = ClusterService(Path(self.tmp.name) / "node")
        seed_hub(self.node, "hub-0001")

    def tearDown(self):
        self.node.stop()
        self.tmp.cleanup()
        super().tearDown()

    def _begin(self, update_id, size, requester="hub-0001", filename="pkg.deb"):
        return self.node.handle_update_begin(
            {
                "update_id": update_id,
                "filename": filename,
                "size": size,
                "sha256": update_id,
            },
            requester,
        )

    def test_transfer_count_limited(self):
        limit = self.node.max_update_transfers
        for index in range(limit):
            self._begin("%064x" % (index + 1), 10)
        with self.assertRaises(protocol.ClusterProtocolError):
            self._begin("%064x" % (limit + 1), 10)

    def test_expired_transfer_rejected(self):
        update_id = "%064x" % 7
        self._begin(update_id, 10)
        with self.node._update_lock:
            self.node._update_transfers[update_id]["updated"] -= (
                self.node.update_transfer_ttl_sec + 5
            )
        with self.assertRaises(protocol.ClusterProtocolError):
            self.node.handle_update_chunk(
                {
                    "update_id": update_id,
                    "offset": 0,
                    "data_b64": base64.b64encode(b"x" * 10).decode("ascii"),
                },
                "hub-0001",
            )

    def test_requester_mismatch_rejected(self):
        update_id = "%064x" % 8
        self._begin(update_id, 4)
        seed_hub(self.node, "hub-0001", "hub-0002")
        with self.assertRaises(protocol.ClusterForbiddenError):
            self.node.handle_update_chunk(
                {
                    "update_id": update_id,
                    "offset": 0,
                    "data_b64": base64.b64encode(b"abcd").decode("ascii"),
                },
                "hub-0002",
            )
        with self.assertRaises(protocol.ClusterForbiddenError):
            self.node.handle_update_commit({"update_id": update_id}, "hub-0002")

    def test_disk_budget_enforced(self):
        self.node.max_update_disk_bytes = 100
        self._begin("%064x" % 9, 60)
        with self.assertRaises(protocol.ClusterProtocolError):
            self._begin("%064x" % 10, 60)


class MultiHubWorkerTest(_StateIsolated):
    def test_worker_heartbeats_all_hubs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            hub_a = ClusterService(root / "a")
            hub_b = ClusterService(root / "b")
            self.assertTrue(hub_a.start(settings("hub", require_approval=False)))
            self.assertTrue(hub_b.start(settings("hub", require_approval=False)))
            worker = ClusterService(root / "w")
            self.assertTrue(
                worker.start(
                    settings(
                        "worker",
                        hubs=[
                            "http://127.0.0.1:%d" % hub_a.actual_port,
                            "http://127.0.0.1:%d" % hub_b.actual_port,
                        ],
                        require_approval=False,
                    )
                )
            )
            try:
                state = worker.wait_worker_state(6.0)
                self.assertEqual(state.get("status"), "approved", state)
                hub_ids = set(state.get("hub_ids") or [])
                self.assertIn(hub_a.identity.node_id(), hub_ids)
                self.assertIn(hub_b.identity.node_id(), hub_ids)
                self.assertEqual(len(state.get("hubs") or []), 2)
            finally:
                worker.stop()
                hub_a.stop()
                hub_b.stop()


if __name__ == "__main__":
    unittest.main()
