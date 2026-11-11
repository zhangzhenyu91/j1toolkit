# -*- coding: utf-8 -*-
"""节点身份：持久 node_id（状态目录）与自报节点信息。

node_id 存 state/cluster/node.json，不写 config.json：用户改配置、导入模板都不会
换身份；状态目录被清空才会重新入网（Hub 侧会显示为新节点）。
"""

import os
import platform
import socket
import sys
import time
import uuid
from pathlib import Path

import state
import usb_protocol
import version as _version

from . import protocol

NODE_FILE = "node.json"


def cluster_dir() -> Path:
    path = state.state_dir() / "cluster"
    path.mkdir(parents=True, exist_ok=True)
    return path


def platform_name() -> str:
    if sys.platform.startswith("win"):
        return "windows"
    if sys.platform.startswith("linux"):
        return "linux"
    if sys.platform == "darwin":
        return "macos"
    return sys.platform


def platform_arch() -> str:
    machine = platform.machine().lower()
    return {
        "x86_64": "amd64",
        "amd64": "amd64",
        "aarch64": "arm64",
        "arm64": "arm64",
    }.get(machine, machine or "unknown")


def sync_tables(cfg: dict, team_id: int) -> dict:
    """节点本地表就绪情况：服务端绑定页据此提示“已配置/文件存在”。"""
    out = {}
    for app in ("tree_sync", "crossing_sync"):
        section = cfg.get(app) if isinstance(cfg.get(app), dict) else {}
        local = str(section.get("local_path") or "").strip()
        exists = False
        if local:
            try:
                exists = Path(local).expanduser().is_file()
            except OSError:
                exists = False
        out[app] = {
            "team_id": max(0, int(team_id or 0)),
            "configured": bool(local),
            "exists": exists,
        }
    return out


def best_local_ip() -> str:
    """本机出口网卡地址（UDP connect 不发包）；失败返回空串。"""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.connect(("10.255.255.255", 1))
        return sock.getsockname()[0]
    except OSError:
        return ""
    finally:
        try:
            sock.close()
        except OSError:
            pass


class NodeIdentity:
    """节点身份与自报信息；测试可传 base_dir 隔离状态。"""

    def __init__(self, base_dir: Path = None):
        self.base = Path(base_dir) if base_dir else cluster_dir()
        self.base.mkdir(parents=True, exist_ok=True)
        self.path = self.base / NODE_FILE
        self._node_id = ""

    def _read_node_id(self) -> str:
        try:
            data = usb_protocol.read_json(self.path)
        except (OSError, ValueError):
            return ""
        value = str((data or {}).get("node_id") or "").strip()
        return value if 8 <= len(value) <= 64 else ""

    def node_id(self) -> str:
        if self._node_id:
            return self._node_id
        value = self._read_node_id()
        if value:
            self._node_id = value
            return value
        # GUI 的 RPC 子进程与常驻 agent 可能同时首次生成；用状态目录锁避免双 ID。
        lock = self.base / "node.lock"
        acquired = False
        fd = None
        try:
            for _ in range(40):
                try:
                    fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
                    acquired = True
                    break
                except FileExistsError:
                    try:
                        if time.time() - lock.stat().st_mtime > 5:
                            lock.unlink()
                    except OSError:
                        pass
                    time.sleep(0.05)
            value = self._read_node_id()
            if not value:
                value = uuid.uuid4().hex
                try:
                    usb_protocol.write_json_atomic(
                        self.path,
                        {"node_id": value, "created_at": usb_protocol.now_iso()},
                    )
                except OSError:
                    # 状态目录不可写时仍返回内存身份；下次启动会重新生成。
                    pass
            self._node_id = value
            return value
        finally:
            if acquired and fd is not None:
                try:
                    os.close(fd)
                finally:
                    try:
                        lock.unlink()
                    except OSError:
                        pass

    def info(self, settings, port: int = None) -> dict:
        """构造协议节点信息；settings 为 ClusterSettings（鸭子类型）。

        port 传入实际监听端口（测试/监听 0 端口时与配置值不同）。
        """
        role = str(getattr(settings, "role", "worker") or "worker")
        team_id = int(getattr(settings, "team_id", 0) or 0)
        team_name = str(getattr(settings, "team_name", "") or "").strip()
        port = int(
            port
            if port is not None
            else (getattr(settings, "listen_port", 0) or 0)
        )
        name = str(getattr(settings, "node_name", "") or "").strip()
        if not name:
            try:
                name = socket.gethostname() or platform.node() or ""
            except OSError:
                name = platform.node() or ""
        return {
            "node_id": self.node_id(),
            "name": (name or self.node_id()[:8])[:64],
            "role": role,
            "team_id": team_id,
            "team_name": team_name[:64],
            "platform": platform_name(),
            "arch": platform_arch(),
            "version": _version.read_version(),
            "protocol": 1,
            "port": port,
            "caps": list(protocol.CAPS),
        }
