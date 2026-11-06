# -*- coding: utf-8 -*-
"""Hub 侧节点注册表：入网申请、人工审批、心跳在线判定与持久化。

registry.json 只保存可重建的节点元数据；在线状态由最近心跳时间实时计算，
不落盘为“永久在线”。人工审批默认开启，未审批节点只能看到 pending。
"""

import json
import threading
import time
from pathlib import Path

import usb_protocol

from . import identity
from . import protocol

REGISTRY_FILE = "registry.json"


class NodeRegistry:
    def __init__(
        self,
        path: Path = None,
        offline_sec: int = 30,
        require_approval: bool = True,
    ):
        self.path = (
            Path(path) if path else identity.cluster_dir() / REGISTRY_FILE
        )
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.offline_sec = max(1, int(offline_sec or 30))
        self.require_approval = bool(require_approval)
        self._lock = threading.RLock()
        self._nodes = {}
        self._load()

    # ---------------- 持久化 ----------------

    def _load(self):
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return
        except (OSError, ValueError):
            self._backup_corrupt()
            return
        nodes = raw.get("nodes") if isinstance(raw, dict) else None
        if not isinstance(nodes, dict):
            self._backup_corrupt()
            return
        for node_id, entry in nodes.items():
            if not isinstance(entry, dict):
                continue
            try:
                info = protocol.validate_node_info(entry)
            except protocol.ClusterProtocolError:
                continue
            view = dict(entry)
            view.update(info)
            view["approved"] = bool(entry.get("approved"))
            view["first_seen"] = str(entry.get("first_seen") or "")
            view["last_seen"] = str(entry.get("last_seen") or "")
            try:
                view["last_seen_ts"] = float(entry.get("last_seen_ts") or 0)
            except (TypeError, ValueError):
                view["last_seen_ts"] = 0.0
            self._nodes[node_id] = view

    def _backup_corrupt(self):
        try:
            target = self.path.with_name(
                self.path.name + ".corrupt-%d" % int(time.time())
            )
            self.path.replace(target)
        except OSError:
            pass

    def _save_locked(self):
        data = {
            "version": 1,
            "updated_at": usb_protocol.now_iso(),
            "nodes": self._nodes,
        }
        try:
            usb_protocol.write_json_atomic(self.path, data)
        except OSError:
            # 状态目录不可写不应打断心跳；下次写成功时补上。
            pass

    # ---------------- 写入 ----------------

    def touch(self, info: dict, ip: str = "") -> dict:
        """登记/续约一个节点；未审批节点保留 pending 状态。"""
        info = protocol.validate_node_info(info)
        now = time.time()
        with self._lock:
            entry = self._nodes.get(info["node_id"])
            if entry is None:
                entry = {
                    "node_id": info["node_id"],
                    "approved": not self.require_approval,
                    "first_seen": usb_protocol.now_iso(),
                }
            entry.update(info)
            if ip:
                entry["ip"] = str(ip)[:64]
            entry["last_seen_ts"] = now
            entry["last_seen"] = usb_protocol.now_iso()
            if not self.require_approval:
                entry["approved"] = True
            self._nodes[info["node_id"]] = entry
            self._save_locked()
            return self._view(entry)

    def approve(self, node_id: str) -> dict:
        with self._lock:
            entry = self._nodes.get(str(node_id or ""))
            if entry is None:
                raise KeyError("节点不存在: %s" % node_id)
            entry["approved"] = True
            self._save_locked()
            return self._view(entry)

    def reject(self, node_id: str) -> bool:
        with self._lock:
            existed = self._nodes.pop(str(node_id or ""), None) is not None
            if existed:
                self._save_locked()
            return existed

    # ---------------- 读取 ----------------

    def is_approved(self, node_id: str) -> bool:
        with self._lock:
            entry = self._nodes.get(str(node_id or ""))
            return bool(entry and entry.get("approved"))

    def get(self, node_id: str):
        with self._lock:
            entry = self._nodes.get(str(node_id or ""))
            return self._view(entry) if entry else None

    def pending(self) -> list:
        with self._lock:
            return [
                self._view(entry)
                for entry in self._nodes.values()
                if not entry.get("approved")
            ]

    def approved(self) -> list:
        with self._lock:
            return [
                self._view(entry)
                for entry in self._nodes.values()
                if entry.get("approved")
            ]

    def snapshot(self) -> list:
        """已审批节点列表，按在线优先、名称排序。"""
        rows = self.approved()
        rows.sort(key=lambda row: (not row["online"], row.get("name") or row["node_id"]))
        return rows

    def counts(self) -> dict:
        with self._lock:
            approved = sum(1 for e in self._nodes.values() if e.get("approved"))
            return {"total": len(self._nodes), "approved": approved,
                    "pending": len(self._nodes) - approved}

    def _view(self, entry: dict) -> dict:
        view = dict(entry)
        ts = float(view.pop("last_seen_ts", 0) or 0)
        view["online"] = bool(ts and time.time() - ts <= self.offline_sec)
        view["self"] = False
        return view
