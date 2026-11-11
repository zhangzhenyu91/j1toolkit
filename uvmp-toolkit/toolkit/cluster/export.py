# -*- coding: utf-8 -*-
"""集群导出协调器（常驻节点服务内）：动态分片、跨节点租约、失败重派与汇总。

协调器只持有队列与结果；实际导出由各节点的 pcd_export 后端以 cluster 模式执行。
节点通过同步 HTTP 领取分片：节点线程空闲即向协调器队列取下一块，执行完回传结果与
逐单文件 zip；协调器按全局 seq 汇总，最后由发起节点调用 pcd_export.finalize_cluster。
"""

import base64
import io
import json
import shutil
import threading
import time
import zipfile
from datetime import datetime
from pathlib import Path

import config as _config
import usb_protocol
from jobs import Cancelled

from . import protocol

MAX_CHUNK_RESULT_BYTES = 48 * 1024 * 1024
# 单个分片 zip 解压总量预算：防 zip 炸弹把发起节点磁盘刷满。
MAX_ZIP_TOTAL_BYTES = 128 * 1024 * 1024
MAX_ZIP_ENTRIES = 512


def public_job(job_dict):
    """状态轮询返回精简结果：逐单 rows 可能上千条，只保留计数/目录/未找到。"""
    if not isinstance(job_dict, dict) or not isinstance(job_dict.get("result"), dict):
        return job_dict
    job_dict = dict(job_dict)
    result = dict(job_dict["result"])
    result.pop("rows", None)
    job_dict["result"] = result
    return job_dict


def _stamp() -> str:
    return datetime.now().strftime("%Y%m%d_%H%M%S")


def _now_iso() -> str:
    return usb_protocol.now_iso()


def _as_int(value, default, minimum=1, maximum=16) -> int:
    try:
        number = int(value)
    except (TypeError, ValueError):
        number = int(default)
    return max(minimum, min(maximum, number))


class ClusterExport:
    """一个集群导出任务；状态持久化在 state/cluster/jobs/<job_id>/state.json。"""

    def __init__(
        self,
        service,
        job,
        params: dict,
        nodes: list,
        request_fn,
        backend,
        chunk_size: int = 6,
        max_attempts: int = 2,
        timeout_sec: int = 1800,
        fallback_node: dict = None,
    ):
        self.service = service
        self.job = job
        self.params = dict(params or {})
        self.nodes = list(nodes or [])
        self.fallback_node = dict(fallback_node) if fallback_node else None
        self.request_fn = request_fn
        self.backend = backend
        self.chunk_size = max(1, int(chunk_size or 6))
        self.max_attempts = max(1, int(max_attempts or 2))
        self.timeout_sec = max(60, int(timeout_sec or 1800))
        self.local_node_id = str(
            self.params.get("local_node_id") or service.identity.node_id()
        )
        self.export_pcd = bool(self.params.get("export_pcd", True))
        self.export_track = bool(self.params.get("export_track", True))
        self.calibrate_noon = bool(self.params.get("calibrate_noon", False))
        self.batch_name = str(self.params.get("batch_name") or "派车单")
        self.batch_subdir = bool(self.params.get("batch_subdir", True))
        self.outdir = str(self.params.get("outdir") or "")
        self.started_at = str(self.params.get("started_at") or _stamp())
        self.status = "running"
        self.error = ""
        self.finished_at = ""
        self.cancel_event = threading.Event()
        self.lock = threading.RLock()
        self._chunk_seq = 0
        self._current = {}
        self.rows = {}
        self.not_found = {}
        self.failed = {}
        self.noon = {}
        self.file_map = {}
        self.base = Path(service.base) / "jobs" / job.id
        self.files_root = self.base / "files"
        self.state_path = self.base / "state.json"
        self.base.mkdir(parents=True, exist_ok=True)
        codes = params.get("codes") or []
        self.items = sorted(
            [dict(item) for item in codes], key=lambda item: int(item.get("seq", 0))
        )
        self.total = len(self.items)
        self.width = int(params.get("cluster_width") or len(str(self.total or 1)))
        self.chunks = []
        for index in range(0, self.total, self.chunk_size):
            self._chunk_seq += 1
            self.chunks.append(
                {
                    "id": "c%04d" % self._chunk_seq,
                    "items": self.items[index : index + self.chunk_size],
                    "attempts": 1,
                    "avoid_node": "",
                    "status": "pending",
                    "node_id": "",
                    "error": "",
                }
            )
        self._save_state()

    # ---------------- 状态 ----------------

    def _save_state(self):
        with self.lock:
            data = {
                "job_id": self.job.id,
                "status": self.status,
                "error": self.error,
                "started_at": self.started_at,
                "finished_at": self.finished_at,
                "params": self.params,
                "nodes": self.nodes,
                "chunks": self.chunks,
                "rows": self.rows,
                "not_found": list(self.not_found.values()),
                "failed": self.failed,
                "noon": list(self.noon.values()),
                "files": self.file_map,
            }
        try:
            usb_protocol.write_json_atomic(self.state_path, data)
        except OSError:
            pass

    def counts(self) -> dict:
        with self.lock:
            pending = sum(1 for c in self.chunks if c["status"] != "done")
            done = len(self.rows) + len(self.not_found)
            failed = sum(
                1
                for row in self.rows.values()
                if (self.export_pcd and row.get("pcd_status") == "失败")
                or (self.export_track and row.get("track_status") == "失败")
            )
            return {
                "total": self.total,
                "done": done,
                "pending": pending,
                "failed": failed,
            }

    # ---------------- 队列 ----------------

    def _take_chunk(self, node_id: str):
        with self.lock:
            pending = [c for c in self.chunks if c["status"] == "pending"]
            if not pending:
                return None
            preferred = [c for c in pending if c.get("avoid_node") != node_id]
            chunk = (preferred or pending)[0]
            chunk["status"] = "running"
            chunk["node_id"] = node_id
            chunk["error"] = ""
            self._current[node_id] = chunk["id"]
            self._save_state()
            return {
                "id": chunk["id"],
                "items": list(chunk["items"]),
                "attempts": chunk["attempts"],
            }

    def _finish_chunk(self, chunk_id: str):
        with self.lock:
            chunk = self._find_chunk(chunk_id)
            if chunk is not None:
                chunk["status"] = "done"
            self._save_state()

    def _requeue_chunk(self, chunk_id: str, node_id: str, error: str):
        with self.lock:
            chunk = self._find_chunk(chunk_id)
            if chunk is None:
                return
            items = list(chunk["items"])
            chunk["status"] = "done"
            chunk["error"] = error
            can_retry = chunk["attempts"] < self.max_attempts
            if can_retry:
                self._chunk_seq += 1
                self.chunks.append(
                    {
                        "id": "c%04d" % self._chunk_seq,
                        "items": items,
                        "attempts": chunk["attempts"] + 1,
                        "avoid_node": node_id,
                        "status": "pending",
                        "node_id": "",
                        "error": error,
                    }
                )
            self._save_state()
        if not can_retry:
            for item in items:
                self._fail_item(item, error)

    def _find_chunk(self, chunk_id: str):
        for chunk in self.chunks:
            if chunk["id"] == chunk_id:
                return chunk
        return None

    def _add_retry(self, item: dict, attempts: int, avoid_node: str, error: str):
        with self.lock:
            self._chunk_seq += 1
            self.chunks.append(
                {
                    "id": "c%04d" % self._chunk_seq,
                    "items": [dict(item)],
                    "attempts": attempts,
                    "avoid_node": avoid_node,
                    "status": "pending",
                    "node_id": "",
                    "error": error,
                }
            )
            self._save_state()

    def _fail_item(self, item: dict, error: str):
        seq = int(item.get("seq", 0))
        with self.lock:
            row = self.rows.get(seq)
            if row is None:
                row = {
                    "seq": seq,
                    "code": item.get("code", ""),
                    "oid": "",
                    "vehicle": "",
                    "date": item.get("date_hint", "") or "",
                    "pcd_file": "",
                    "pcd_status": "失败" if self.export_pcd else "未导出",
                    "track_file": "",
                    "track_status": "失败" if self.export_track else "未导出",
                    "note": "",
                }
            if self.export_pcd and row.get("pcd_status") != "成功":
                row["pcd_status"] = "失败"
            if self.export_track and row.get("track_status") != "成功":
                row["track_status"] = "失败"
            row["note"] = (row.get("note") + "；" if row.get("note") else "") + str(
                error
            )
            row["note"] = row["note"][:500]
            self.rows[seq] = row
            self.failed[seq] = str(error)[:500]
            self._save_state()

    # ---------------- 执行 ----------------

    def run(self):
        self.job.log("集群导出开始：%d 单，%d 个节点，分片 %d" % (self.total, len(self.nodes), self.chunk_size))
        self.job.set_progress(0, self.total, "集群导出")
        try:
            threads = []
            for node in self.nodes:
                thread = threading.Thread(
                    target=self._node_worker,
                    args=(node,),
                    name="cluster-export-" + node["node_id"],
                    daemon=True,
                )
                thread.start()
                threads.append(thread)
            for thread in threads:
                thread.join()
            if self.cancel_event.is_set() or self.job.cancelled:
                raise Cancelled()
            if self._pending_chunks():
                # 远端失败后由发起节点兜底，保证任务闭环（重派仍按全局 seq）。
                local = self._local_node()
                if local is not None:
                    self.job.log("远端未完成，发起节点接管剩余分片")
                    self._node_worker(local)
            if self.cancel_event.is_set() or self.job.cancelled:
                raise Cancelled()
            if self._pending_chunks():
                for chunk in list(self.chunks):
                    if chunk["status"] != "done":
                        for item in chunk["items"]:
                            self._fail_item(item, "集群节点均未完成分片")
                        self._finish_chunk(chunk["id"])
            return self._finalize()
        except Cancelled:
            with self.lock:
                self.status = "cancelled"
                self.finished_at = _now_iso()
            self._cleanup_files()
            self._save_state()
            raise
        except Exception as e:  # noqa: BLE001
            with self.lock:
                self.status = "failed"
                self.error = str(e)
                self.finished_at = _now_iso()
            self._cleanup_files()
            self._save_state()
            raise

    def _pending_chunks(self) -> bool:
        with self.lock:
            return any(c["status"] != "done" for c in self.chunks)

    def _local_node(self):
        for node in self.nodes:
            if node["node_id"] == self.local_node_id:
                return node
        return self.fallback_node

    def _node_worker(self, node: dict):
        while not self.cancel_event.is_set() and not self.job.cancelled:
            chunk = self._take_chunk(node["node_id"])
            if chunk is None:
                return
            try:
                result = self._run_chunk(node, chunk)
            except Exception as e:  # noqa: BLE001
                self.job.log(
                    "节点 %s 执行分片 %s 失败：%s"
                    % (node.get("name") or node["node_id"], chunk["id"], e)
                )
                self._requeue_chunk(chunk["id"], node["node_id"], str(e))
                return
            finally:
                with self.lock:
                    self._current.pop(node["node_id"], None)
            if result is False:
                return

    def _run_chunk(self, node: dict, chunk: dict):
        body = {
            "job_id": self.job.id,
            "chunk": chunk,
            "options": {
                "export_pcd": self.export_pcd,
                "export_track": self.export_track,
                "calibrate_noon": self.calibrate_noon,
                "cluster_width": self.width,
                "batch_name": self.batch_name,
                "batch_subdir": False,
            },
            "concurrency": {
                "query_workers": node.get("query_workers", 8),
                "render_workers": node.get("render_workers", 4),
            },
        }
        self.job.log(
            "节点 %s 领取分片 %s（%d 单）"
            % (node.get("name") or node["node_id"], chunk["id"], len(chunk["items"]))
        )
        try:
            data = self.request_fn(
                node["url"],
                "/cluster/export/run",
                body,
                timeout=self.timeout_sec,
            )
        except Exception as e:  # noqa: BLE001
            self._requeue_chunk(chunk["id"], node["node_id"], str(e))
            return False
        result = data.get("result")
        if not isinstance(result, dict) or result.get("error"):
            error = (
                str(result.get("error"))
                if isinstance(result, dict)
                else "节点未返回有效结果"
            )
            self._requeue_chunk(chunk["id"], node["node_id"], error)
            return True
        self._accept_result(chunk, result, data.get("zip_b64") or "", node)
        return True

    def _accept_result(self, chunk: dict, result: dict, zip_b64: str, node: dict):
        # 只接受本分片 items 里的 seq：节点回传别人的行/午间行一律丢弃，
        # 否则一个越权节点就能改写其它单据的汇总结果。
        allowed = {}
        for item in chunk.get("items") or []:
            try:
                seq = int(item.get("seq") or 0)
            except (TypeError, ValueError):
                continue
            if seq > 0:
                allowed[seq] = str(item.get("code") or "")
        rows = result.get("rows") or []
        not_found_codes = set(result.get("not_found") or [])
        row_by_seq = {}
        dropped = 0
        with self.lock:
            for row in rows:
                if not isinstance(row, dict):
                    continue
                try:
                    seq = int(row.get("seq", 0))
                except (TypeError, ValueError):
                    continue
                expected = allowed.get(seq)
                if expected is None:
                    dropped += 1
                    continue
                row_code = str(row.get("code") or "")
                if row_code and row_code != expected:
                    dropped += 1
                    continue
                row_by_seq[seq] = row
                self.rows[seq] = row
            for nr in result.get("noon_rows") or []:
                if not isinstance(nr, dict):
                    continue
                try:
                    seq = int(nr.get("seq", 0))
                except (TypeError, ValueError):
                    continue
                if seq in allowed:
                    self.noon[seq] = nr
        if dropped:
            self.job.log(
                "分片 %s 回传 %d 条不属于本分片的结果，已丢弃"
                % (chunk["id"], dropped)
            )
        artifacts = self._extract_zip(chunk["id"], zip_b64)
        wanted = set()
        for seq, row in row_by_seq.items():
            ok_pcd = (not self.export_pcd) or row.get("pcd_status") == "成功"
            ok_track = (not self.export_track) or row.get("track_status") == "成功"
            if ok_pcd and row.get("pcd_file"):
                wanted.add(row["pcd_file"])
            if ok_track and row.get("track_file"):
                wanted.add(row["track_file"])
        missing = sorted(wanted - set(artifacts))
        if missing:
            with self.lock:
                for item in chunk["items"]:
                    self.noon.pop(int(item.get("seq", 0)), None)
            self._requeue_chunk(
                chunk["id"],
                node["node_id"],
                "分片产物缺失: %s" % ", ".join(missing[:3]),
            )
            return
        with self.lock:
            for name, path in artifacts.items():
                if name in wanted:
                    self.file_map[name] = str(path)
        for item in chunk["items"]:
            seq = int(item.get("seq", 0))
            code = str(item.get("code", ""))
            if code in not_found_codes:
                with self.lock:
                    self.rows.pop(seq, None)
                    self.failed.pop(seq, None)
                    self.not_found[seq] = {
                        "seq": seq,
                        "code": code,
                        "date_hint": item.get("date_hint", ""),
                    }
                continue
            row = row_by_seq.get(seq)
            if row is None:
                self._retry_or_fail(item, chunk["attempts"], node["node_id"], "分片未返回该单结果")
                continue
            with self.lock:
                self.not_found.pop(seq, None)
            ok = ((not self.export_pcd) or row.get("pcd_status") == "成功") and (
                (not self.export_track) or row.get("track_status") == "成功"
            )
            if ok:
                with self.lock:
                    self.failed.pop(seq, None)
                continue
            error = (
                row.get("note")
                or ("派车单失败" if self.export_pcd else "")
                or ("轨迹失败" if self.export_track else "")
                or "分片执行失败"
            )
            self._retry_or_fail(item, chunk["attempts"], node["node_id"], error)
        self._finish_chunk(chunk["id"])
        counts = self.counts()
        self.job.set_progress(counts["done"], self.total, "集群导出")
        self._save_state()

    def _retry_or_fail(self, item: dict, attempts: int, node_id: str, error: str):
        if attempts < self.max_attempts:
            self._add_retry(item, attempts + 1, node_id, error)
        else:
            self._fail_item(item, error)

    def _extract_zip(self, chunk_id: str, zip_b64: str) -> dict:
        if not zip_b64:
            return {}
        try:
            raw = base64.b64decode(zip_b64, validate=True)
        except Exception:  # noqa: BLE001
            return {}
        if len(raw) > MAX_CHUNK_RESULT_BYTES:
            return {}
        target = self.files_root / chunk_id
        target.mkdir(parents=True, exist_ok=True)
        out = {}
        total = 0
        try:
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                infos = [info for info in archive.infolist() if not info.is_dir()]
                if len(infos) > MAX_ZIP_ENTRIES:
                    return {}
                for info in infos:
                    name = Path(info.filename).name
                    if not name or info.file_size > 32 * 1024 * 1024:
                        continue
                    # 按条目声明大小与已解压总量双重限制，防 zip 炸弹。
                    total += int(info.file_size)
                    if total > MAX_ZIP_TOTAL_BYTES:
                        return {}
                    data = archive.read(info)
                    if len(data) > 32 * 1024 * 1024:
                        continue
                    path = target / name
                    path.write_bytes(data)
                    out[name] = path
        except (zipfile.BadZipFile, OSError, RuntimeError):
            return {}
        return out

    def _cleanup_files(self):
        """汇总完成后删除分片产物临时目录（终稿已由 backend 落到 outdir）。"""
        try:
            shutil.rmtree(self.files_root, ignore_errors=True)
        except OSError:
            pass

    def _finalize(self):
        with self.lock:
            rows = [self.rows[seq] for seq in sorted(self.rows)]
            not_found = [self.not_found[seq] for seq in sorted(self.not_found)]
            noon = [self.noon[seq] for seq in sorted(self.noon)]
            files = list(self.file_map.values())
        if not hasattr(self.backend, "finalize_cluster"):
            raise RuntimeError("pcd_export 后端未提供集群汇总")
        result = self.backend.finalize_cluster(
            self.outdir,
            self.batch_name,
            self.batch_subdir,
            self.started_at,
            rows,
            not_found,
            noon,
            files,
            export_track=self.export_track,
            log=self.job.log,
        )
        with self.lock:
            self.status = "success"
            self.finished_at = _now_iso()
        self._cleanup_files()
        self._save_state()
        self.job.set_progress(self.total, self.total, "集群导出完成")
        return result

    def cancel(self):
        self.cancel_event.set()
        with self.lock:
            active = list(self._current.items())
        for node_id, chunk_id in active:
            node = next((n for n in self.nodes if n["node_id"] == node_id), None)
            if not node:
                continue
            try:
                self.request_fn(
                    node["url"],
                    "/cluster/export/cancel",
                    {"job_id": self.job.id, "chunk_ids": [chunk_id]},
                    timeout=5,
                )
            except Exception:  # noqa: BLE001
                pass

    def snapshot(self) -> dict:
        job = self.service.jobs.get(self.job.id) if self.service.jobs else None
        job_dict = public_job(job.to_dict()) if job else None
        logs = []
        if job is not None:
            try:
                lines, _ = job.logs_since(0)
                logs = [line for _, line in lines][-500:]
            except Exception:  # noqa: BLE001
                logs = []
        if job_dict is None:
            job_dict = {
                "id": self.job.id,
                "status": self.status,
                "progress": {"current": self.counts()["done"], "total": self.total},
                "result": {},
                "error": self.error,
            }
        return {
            "job": job_dict,
            "logs": logs,
            "queue": self.counts(),
            "status": self.status,
            "error": self.error,
            "nodes": [
                {
                    "node_id": n["node_id"],
                    "name": n.get("name", ""),
                    "url": n.get("url", ""),
                }
                for n in self.nodes
            ],
        }

    @staticmethod
    def _read_json(path: Path):
        try:
            data = json.loads(Path(path).read_text(encoding="utf-8"))
            return data if isinstance(data, dict) else {}
        except (OSError, ValueError):
            return {}


def recover_orphans(base_dir) -> int:
    """服务重启后把仍标记 running 的集群导出置为中断（GUI 可重跑）。"""
    root = Path(base_dir) / "jobs"
    count = 0
    try:
        entries = list(root.iterdir())
    except OSError:
        return 0
    for path in entries:
        state_file = path / "state.json"
        try:
            data = json.loads(state_file.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            continue
        if data.get("status") == "running":
            data["status"] = "interrupted"
            data["error"] = "协调器重启，任务中断"
            data["finished_at"] = _now_iso()
            try:
                usb_protocol.write_json_atomic(state_file, data)
                count += 1
            except OSError:
                pass
    return count
