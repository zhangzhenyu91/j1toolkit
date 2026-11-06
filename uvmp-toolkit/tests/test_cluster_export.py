# -*- coding: utf-8 -*-
"""集群导出分片：节点执行、发起节点切分/汇总/重试（fake 后端，零网络）。"""

import base64
import io
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

TOOLKIT_DIR = Path(__file__).resolve().parent.parent / "toolkit"
if str(TOOLKIT_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLKIT_DIR))

from cluster.export import ClusterExport
from cluster.identity import NodeIdentity
from cluster.service import ClusterService
from jobs import JobManager


class _FakeExportBackend:
    """按 chunk 写逐单 PDF 并返回结果；可配置前 N 次失败。"""

    def __init__(self):
        self.calls = {}
        self.fail_first = 0
        self.finalized = None

    def run_cluster(self, job, params):
        files_dir = Path(params["outdir"]) / "逐单"
        files_dir.mkdir(parents=True, exist_ok=True)
        rows = []
        not_found = []
        for item in params["codes"]:
            code = str(item["code"])
            self.calls[code] = self.calls.get(code, 0) + 1
            if self.calls[code] <= self.fail_first:
                raise RuntimeError("模拟节点失败: " + code)
            if code == "NF":
                not_found.append(code)
                continue
            name = "%03d_派车单_%s.pdf" % (int(item["seq"]), code)
            (files_dir / name).write_bytes(b"%PDF-1.4\n")
            rows.append(
                {
                    "seq": int(item["seq"]),
                    "code": code,
                    "oid": code,
                    "vehicle": "京A00001",
                    "date": "2026-01-01",
                    "pcd_file": name,
                    "pcd_status": "成功",
                    "track_file": "",
                    "track_status": "",
                    "note": "",
                }
            )
        return {"rows": rows, "not_found": not_found, "noon_rows": []}

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
        self.finalized = {
            "outdir": str(outdir),
            "rows": [row["code"] for row in rows],
            "not_found": [item["code"] for item in not_found],
            "files": [Path(item).name for item in files],
        }
        return {
            "dir": str(outdir),
            "total": len(rows) + len(not_found),
            "found": len(rows),
            "not_found": self.finalized["not_found"],
            "pcd_ok": len(rows),
            "pcd_fail": 0,
            "track_ok": 0,
            "track_fail": 0,
            "rows": rows,
        }


def _service(root: Path, backend: _FakeExportBackend) -> ClusterService:
    service = ClusterService(root, NodeIdentity(root))
    service.set_runtime(
        JobManager(), lambda: {"pcd_export": ({"id": "pcd_export"}, backend)}
    )
    return service


class ClusterExportTests(unittest.TestCase):
    def test_peer_export_run_returns_result_and_zip(self):
        with tempfile.TemporaryDirectory() as tmp:
            backend = _FakeExportBackend()
            peer = _service(Path(tmp), backend)
            body = {
                "job_id": "20260101120000-01",
                "chunk": {
                    "id": "c0001",
                    "items": [{"seq": 1, "code": "A1"}],
                    "attempts": 1,
                },
                "options": {"export_pcd": True, "export_track": False},
                "concurrency": {"query_workers": 2, "render_workers": 2},
            }
            data = peer.handle_export_run(body, "coordinator")
            self.assertFalse(data["result"].get("error"))
            self.assertEqual([row["code"] for row in data["result"]["rows"]], ["A1"])
            raw = base64.b64decode(data["zip_b64"], validate=True)
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                self.assertEqual(archive.namelist(), ["001_派车单_A1.pdf"])

    def test_coordinator_splits_merges_and_finalizes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            backend = _FakeExportBackend()
            peer = _service(root / "peer", backend)
            coordinator = _service(root / "coord", backend)
            job = coordinator.jobs.create("pcd_export", "集群测试")
            nodes = [
                {
                    "node_id": "peer1",
                    "name": "peer1",
                    "url": "http://peer1",
                    "query_workers": 2,
                    "render_workers": 2,
                }
            ]

            def request_fn(url, path, body, timeout=None):
                self.assertEqual(path, "/cluster/export/run")
                return peer.handle_export_run(body, "coordinator")

            params = {
                "codes": [
                    {"seq": 1, "code": "A1"},
                    {"seq": 2, "code": "A2"},
                    {"seq": 3, "code": "NF"},
                    {"seq": 4, "code": "A4"},
                ],
                "outdir": str(root / "out"),
                "batch_name": "测试",
                "batch_subdir": True,
                "export_pcd": True,
                "export_track": False,
                "calibrate_noon": False,
                "local_node_id": "coord",
            }
            export = ClusterExport(
                coordinator,
                job,
                params,
                nodes,
                request_fn,
                backend,
                chunk_size=2,
                max_attempts=2,
                timeout_sec=10,
            )
            result = export.run()
            self.assertEqual(result["found"], 3)
            self.assertEqual(result["not_found"], ["NF"])
            self.assertEqual(backend.finalized["rows"], ["A1", "A2", "A4"])
            self.assertEqual(
                backend.finalized["files"],
                [
                    "001_派车单_A1.pdf",
                    "002_派车单_A2.pdf",
                    "004_派车单_A4.pdf",
                ],
            )
            self.assertEqual(len(export.chunks), 2)

    def test_missing_artifact_retries_chunk(self):
        class MissingOnceBackend(_FakeExportBackend):
            def __init__(self):
                super().__init__()
                self.file_calls = 0

            def run_cluster(self, job, params):
                payload = super().run_cluster(job, params)
                self.file_calls += 1
                if self.file_calls == 1:
                    files_dir = Path(params["outdir"]) / "逐单"
                    for path in files_dir.iterdir():
                        path.unlink()
                return payload

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            backend = MissingOnceBackend()
            peer = _service(root / "peer", backend)
            coordinator = _service(root / "coord", backend)
            job = coordinator.jobs.create("pcd_export", "缺文件测试")
            nodes = [
                {
                    "node_id": "peer1",
                    "name": "peer1",
                    "url": "http://peer1",
                    "query_workers": 2,
                    "render_workers": 2,
                }
            ]

            def request_fn(url, path, body, timeout=None):
                return peer.handle_export_run(body, "coordinator")

            params = {
                "codes": [{"seq": 1, "code": "A1"}],
                "outdir": str(root / "out"),
                "batch_name": "测试",
                "batch_subdir": True,
                "export_pcd": True,
                "export_track": False,
                "calibrate_noon": False,
                "local_node_id": "coord",
            }
            export = ClusterExport(
                coordinator,
                job,
                params,
                nodes,
                request_fn,
                backend,
                chunk_size=1,
                max_attempts=2,
                timeout_sec=10,
            )
            result = export.run()
            self.assertEqual(result["found"], 1)
            self.assertEqual(backend.file_calls, 2)
            self.assertEqual([c["attempts"] for c in export.chunks], [1, 2])
            self.assertEqual(len(backend.finalized["files"]), 1)

    def test_failed_chunk_retries_with_new_id_and_succeeds(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            backend = _FakeExportBackend()
            backend.fail_first = 1
            peer = _service(root / "peer", backend)
            coordinator = _service(root / "coord", backend)
            job = coordinator.jobs.create("pcd_export", "重试测试")
            nodes = [
                {
                    "node_id": "peer1",
                    "name": "peer1",
                    "url": "http://peer1",
                    "query_workers": 2,
                    "render_workers": 2,
                }
            ]

            def request_fn(url, path, body, timeout=None):
                return peer.handle_export_run(body, "coordinator")

            params = {
                "codes": [{"seq": 1, "code": "A1"}],
                "outdir": str(root / "out"),
                "batch_name": "测试",
                "batch_subdir": True,
                "export_pcd": True,
                "export_track": False,
                "calibrate_noon": False,
                "local_node_id": "coord",
            }
            export = ClusterExport(
                coordinator,
                job,
                params,
                nodes,
                request_fn,
                backend,
                chunk_size=1,
                max_attempts=2,
                timeout_sec=10,
            )
            result = export.run()
            self.assertEqual(result["found"], 1)
            self.assertEqual(backend.calls["A1"], 2)
            self.assertEqual([c["attempts"] for c in export.chunks], [1, 2])
            self.assertNotEqual(export.chunks[0]["id"], export.chunks[1]["id"])


if __name__ == "__main__":
    unittest.main()
