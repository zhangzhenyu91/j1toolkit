# -*- coding: utf-8 -*-
"""集群节点服务：Hub 注册表/审批、Worker 心跳、局域网 HTTP 入口。

同一服务承载节点身份/入网审批/心跳/节点表、Hub 指令转发与文件回传、
多节点导出分片、节点更新分发。服务由 usb-agent 常驻进程托管，GUI 通过
本机管理接口读取状态/审批节点/发起任务，不直接接触集群密钥。
"""

import base64
import hashlib
import io
import json
import logging
import os
import shutil
import socket
import threading
import time
import urllib.error
import urllib.request
import zipfile
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

import config as _config
import state as _state
import usb_protocol
import version as _version

from . import identity
from . import protocol
from .registry import NodeRegistry

SERVICE_FILE = "service.json"
PEERS_FILE = "peers.json"
NODES_FILE = "cluster_nodes.json"
DEFAULT_PORT = 17890
START_RETRY_SEC = 15
MAX_ARTIFACT = 32 * 1024 * 1024
MAX_EXPORT_ZIP = 32 * 1024 * 1024
MAX_UPDATE_SIZE = 2 * 1024 * 1024 * 1024
UPDATE_CHUNK_SIZE = 4 * 1024 * 1024
UPDATE_PACKAGES_FILE = "update_packages.json"
UPDATE_PENDING_FILE = "pending.json"
MAX_COMMAND_MINUTES = 8
REMOTE_DEADLINE_MARGIN_SEC = 30


def _now_iso() -> str:
    return usb_protocol.now_iso()


def _as_bool(value, default=False) -> bool:
    if value is None:
        return bool(default)
    if isinstance(value, bool):
        return value
    return str(value).strip().lower() in ("1", "true", "yes", "on", "是")


def _as_int(value, default, minimum=None, maximum=None) -> int:
    try:
        number = int(value)
    except (TypeError, ValueError):
        number = int(default)
    if minimum is not None:
        number = max(minimum, number)
    if maximum is not None:
        number = min(maximum, number)
    return number


def _normalize_urls(value) -> list:
    items = value if isinstance(value, (list, tuple)) else [value]
    out = []
    for item in items:
        text = str(item or "").strip()
        if not text:
            continue
        if "://" not in text:
            text = "http://" + text
        if not text.startswith(("http://", "https://")):
            continue
        out.append(text.rstrip("/"))
    return out


def _normalize_platform(value) -> str:
    text = str(value or "").strip().lower()
    if text.startswith("win"):
        return "windows"
    if "linux" in text:
        return "linux"
    if text in ("darwin", "macos", "mac"):
        return "darwin"
    return text


def _normalize_arch(value) -> str:
    text = str(value or "").strip().lower()
    if text in ("x86_64", "amd64", "x64", "x86-64", "i686", "i386"):
        return "amd64"
    if text in ("arm64", "aarch64"):
        return "arm64"
    return text


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fp:
        for block in iter(lambda: fp.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _read_json(path: Path) -> dict:
    try:
        data = json.loads(Path(path).read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        return {}


def _write_json(path: Path, data: dict):
    try:
        usb_protocol.write_json_atomic(Path(path), data)
    except OSError:
        pass


def _http_error_text(err) -> str:
    try:
        raw = err.read()
        data = json.loads(raw.decode("utf-8"))
        if isinstance(data, dict) and data.get("error"):
            return str(data["error"])
    except Exception:  # noqa: BLE001
        pass
    return str(err)


def _join_url(base: str, path: str) -> str:
    return str(base or "").rstrip("/") + path


@dataclass
class ClusterSettings:
    enabled: bool = False
    role: str = "worker"
    cluster_id: str = ""
    secret: str = ""
    node_name: str = ""
    team_id: int = 0
    team_name: str = ""
    listen_host: str = "0.0.0.0"
    listen_port: int = DEFAULT_PORT
    advertise_host: str = ""
    hubs: list = field(default_factory=list)
    hub_peers: list = field(default_factory=list)
    require_approval: bool = True
    heartbeat_sec: int = 10
    offline_sec: int = 30
    request_timeout_sec: int = 5

    @classmethod
    def from_config(cls, cfg: dict) -> "ClusterSettings":
        raw = (cfg or {}).get("cluster")
        data = raw if isinstance(raw, dict) else {}
        return cls(
            enabled=_as_bool(data.get("enabled")),
            role=str(data.get("role") or "worker").strip().lower(),
            cluster_id=str(data.get("cluster_id") or "").strip(),
            secret=str(data.get("secret") or ""),
            node_name=str(data.get("node_name") or "").strip(),
            team_id=_as_int(data.get("team_id"), 0, 0),
            team_name=str(data.get("team_name") or "").strip(),
            listen_host=str(data.get("listen_host") or "0.0.0.0").strip()
            or "0.0.0.0",
            listen_port=_as_int(data.get("listen_port"), DEFAULT_PORT, 0),
            advertise_host=str(data.get("advertise_host") or "").strip(),
            hubs=_normalize_urls(data.get("hubs")),
            hub_peers=_normalize_urls(data.get("hub_peers")),
            require_approval=_as_bool(data.get("require_approval"), True),
            heartbeat_sec=_as_int(data.get("heartbeat_sec"), 10, 1),
            offline_sec=_as_int(data.get("offline_sec"), 30, 1),
            request_timeout_sec=_as_int(data.get("request_timeout_sec"), 5, 1),
        )

    def validate(self) -> str:
        if not self.enabled:
            return ""
        if not protocol.CLUSTER_ID_RE.match(self.cluster_id or ""):
            return "集群标识未配置或格式非法（1-64 位字母数字 . _ : -）"
        if len(self.secret or "") < 16:
            return "集群密钥至少 16 位；所有节点必须使用同一密钥"
        if self.role not in protocol.ROLE_VALUES:
            return "角色必须是 hub 或 worker"
        if not 1 <= int(self.listen_port) <= 65535:
            return "监听端口必须在 1-65535"
        if not 3 <= int(self.heartbeat_sec) <= 300:
            return "心跳间隔必须在 3-300 秒"
        if int(self.offline_sec) < int(self.heartbeat_sec):
            return "离线判定时间不能小于心跳间隔"
        if not 1 <= int(self.request_timeout_sec) <= 60:
            return "请求超时必须在 1-60 秒"
        if self.role == "worker" and not self.hubs:
            return "Worker 至少配置一个 Hub 地址"
        for url in list(self.hubs) + list(self.hub_peers):
            parts = urlsplit(url)
            if (
                parts.scheme not in ("http", "https")
                or not parts.netloc
                or parts.path not in ("", "/")
            ):
                return "地址非法（只填协议+主机+端口）: %s" % url
        return ""

    def public(self) -> dict:
        return {
            "enabled": self.enabled,
            "role": self.role,
            "cluster_id": self.cluster_id,
            "node_name": self.node_name,
            "team_id": self.team_id,
            "team_name": self.team_name,
            "listen_host": self.listen_host,
            "listen_port": self.listen_port,
            "advertise_host": self.advertise_host,
            "hubs": list(self.hubs),
            "hub_peers": list(self.hub_peers),
            "require_approval": self.require_approval,
            "heartbeat_sec": self.heartbeat_sec,
            "offline_sec": self.offline_sec,
            "request_timeout_sec": self.request_timeout_sec,
            "secret_set": bool(self.secret),
        }


def _collect_local_ips(settings: ClusterSettings) -> set:
    ips = {"127.0.0.1", "::1"}
    try:
        for item in socket.gethostbyname_ex(socket.gethostname())[2]:
            ips.add(item)
    except OSError:
        pass
    for value in (settings.listen_host, settings.advertise_host):
        if value and value not in ("0.0.0.0", "::"):
            try:
                ips.add(socket.gethostbyname(value))
            except OSError:
                ips.add(value)
    return ips


def _self_node(id_obj: identity.NodeIdentity, settings: ClusterSettings, port=None):
    info = id_obj.info(settings, port=port)
    try:
        info["sync_tables"] = identity.sync_tables(_config.load(), settings.team_id)
    except Exception:  # noqa: BLE001
        info["sync_tables"] = {}
    info.update(
        {
            "approved": True,
            "online": True,
            "self": True,
            "ip": settings.advertise_host or identity.best_local_ip() or "127.0.0.1",
            "last_seen": _now_iso(),
        }
    )
    return info


class _ClusterHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "UVMPCluster/1"

    def do_GET(self):  # noqa: N802
        self._handle("GET")

    def do_POST(self):  # noqa: N802
        self._handle("POST")

    def _handle(self, method: str):
        service = getattr(self.server, "cluster_service", None)
        if service is None:
            self._send(500, {"ok": False, "error": "集群服务未初始化"})
            return
        try:
            path = urlsplit(self.path).path
            limit = (
                protocol.MAX_COMMAND_BODY
                if path.startswith("/cluster/command")
                or path.startswith("/cluster/update/")
                else protocol.MAX_BODY
            )
            length = int(self.headers.get("Content-Length") or 0)
            if length < 0 or length > limit:
                raise protocol.ClusterProtocolError("请求体过大")
            raw = self.rfile.read(length) if length else b""
            client_ip = self.client_address[0]
            if path.startswith("/cluster/admin/"):
                data = service.handle_admin(method, path, raw, client_ip)
            else:
                data = service.handle_public(
                    method, path, raw, self.headers, client_ip
                )
            self._send(200, {"ok": True, "data": data})
        except protocol.ClusterProtocolError as e:
            self._send(getattr(e, "status", 400), {"ok": False, "error": str(e)})
        except Exception as e:  # noqa: BLE001
            service.log_exception("集群请求处理异常: %s", e)
            self._send(500, {"ok": False, "error": "服务端内部错误"})

    def _send(self, status: int, payload: dict):
        raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(raw)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(raw)
        except OSError:
            pass
        self.close_connection = True

    def log_message(self, fmt, *args):
        service = getattr(self.server, "cluster_service", None)
        if service is not None:
            service.log_debug(fmt, *args)


class _WorkerLoop(threading.Thread):
    def __init__(self, service: "ClusterService"):
        super().__init__(name="cluster-worker", daemon=True)
        self.service = service
        self.stop_event = service._worker_stop
        self._joined = set()

    def run(self):
        while not self.stop_event.is_set():
            settings = self.service.current_settings()
            if not settings.enabled or settings.role != "worker" or not settings.hubs:
                self.stop_event.wait(2.0)
                continue
            info = self.service.node_info(settings)
            approved = None
            pending = None
            errors = []
            for hub in settings.hubs:
                try:
                    path = (
                        "/cluster/heartbeat"
                        if hub in self._joined
                        else "/cluster/join"
                    )
                    data = request_json(
                        hub,
                        path,
                        {"node": info},
                        settings,
                        node_id=info["node_id"],
                    )
                    status = str(data.get("status") or "")
                    if status == "approved":
                        self._joined.add(hub)
                        approved = (hub, data)
                        break
                    if status == "pending":
                        self._joined.add(hub)
                        if pending is None:
                            pending = (hub, data)
                        continue
                    self._joined.discard(hub)
                    errors.append("%s: 未知状态 %s" % (hub, status))
                except Exception as e:  # noqa: BLE001
                    self._joined.discard(hub)
                    errors.append("%s: %s" % (hub, e))
            if self.stop_event.is_set():
                break
            if approved:
                hub, data = approved
                self.service.set_worker_state(
                    {
                        "hub": hub,
                        "status": "approved",
                        "nodes": data.get("nodes") or [],
                        "last_error": "",
                        "last_ok": _now_iso(),
                    }
                )
            elif pending:
                hub, data = pending
                self.service.set_worker_state(
                    {
                        "hub": hub,
                        "status": "pending",
                        "nodes": [],
                        "last_error": "等待 Hub 批准入网",
                    }
                )
            else:
                self.service.set_worker_state(
                    {
                        "hub": "",
                        "status": "error",
                        "nodes": [],
                        "last_error": "；".join(errors) or "无可达 Hub",
                    }
                )
            self.stop_event.wait(max(3, int(settings.heartbeat_sec or 10)))


def request_json(
    base_url: str,
    path: str,
    body: dict,
    settings: ClusterSettings,
    node_id: str,
    timeout: float = None,
    max_response: int = protocol.MAX_BODY,
) -> dict:
    """向 Hub 发一个签名请求，返回 data 段；失败抛 RuntimeError。"""
    raw = protocol.json_bytes(body or {})
    headers = protocol.build_headers(
        settings.secret,
        settings.cluster_id,
        node_id,
        "POST",
        path,
        raw,
    )
    req = urllib.request.Request(
        _join_url(base_url, path), data=raw, headers=headers, method="POST"
    )
    try:
        with urllib.request.urlopen(
            req, timeout=timeout or settings.request_timeout_sec
        ) as resp:
            payload = protocol.parse_json_body(
                resp.read(), max_bytes=max_response
            )
    except urllib.error.HTTPError as e:
        raise RuntimeError(_http_error_text(e))
    except (urllib.error.URLError, OSError) as e:
        raise RuntimeError(str(e))
    if not payload.get("ok"):
        raise RuntimeError(str(payload.get("error") or "请求失败"))
    data = payload.get("data")
    return data if isinstance(data, dict) else {}


class ClusterService:
    """节点服务单例；usb-agent 每轮调用 ensure() 按配置启停。"""

    def __init__(
        self,
        base_dir: Path = None,
        logger=None,
        identity_obj: identity.NodeIdentity = None,
        registry: NodeRegistry = None,
    ):
        self.base = Path(base_dir) if base_dir else identity.cluster_dir()
        self.base.mkdir(parents=True, exist_ok=True)
        self.logger = logger or logging.getLogger("uvmp.cluster")
        self.identity = identity_obj or identity.NodeIdentity(self.base)
        self.registry = registry
        self.status_path = self.base / SERVICE_FILE
        self.peers_path = self.base / PEERS_FILE
        self._settings = None
        self._server = None
        self._thread = None
        self._worker = None
        self._worker_stop = None
        self._worker_state = {}
        self._running = False
        self._actual_port = 0
        self._started_at = ""
        self._last_error = ""
        self._last_start_attempt = 0.0
        self._local_ips_set = set()
        self._nonce = protocol.NonceCache()
        self._lock = threading.RLock()
        self._state_lock = threading.RLock()
        self._status_write_lock = threading.Lock()
        self.jobs = None
        self.apps_provider = None
        self._exports = {}
        self._export_runs = {}
        self._export_lock = threading.RLock()
        self._export_ready_cache = None
        self._update_lock = threading.RLock()
        self._update_transfers = {}
        self._update_packages_path = self.base / UPDATE_PACKAGES_FILE

    def set_runtime(self, jobs, apps_provider):
        """注入 usb-agent 的任务管理器与应用表（远程指令在本进程执行）。"""
        self.jobs = jobs
        self.apps_provider = apps_provider

    # ---------------- 日志 ----------------

    def log_debug(self, message, *args):
        try:
            self.logger.debug(message, *args)
        except Exception:  # noqa: BLE001
            pass

    def log_info(self, message, *args):
        try:
            self.logger.info(message, *args)
        except Exception:  # noqa: BLE001
            pass

    def log_exception(self, message, *args):
        try:
            self.logger.exception(message, *args)
        except Exception:  # noqa: BLE001
            pass

    # ---------------- 生命周期 ----------------

    def current_settings(self) -> ClusterSettings:
        with self._state_lock:
            return self._settings or ClusterSettings()

    @property
    def actual_port(self) -> int:
        with self._state_lock:
            return int(self._actual_port or 0)

    def ensure(self, logger=None):
        """按 config.json 启停/刷新；只做轻量比较，失败不打断 usb-agent。"""
        if logger is not None:
            self.logger = logger
        with self._lock:
            settings = ClusterSettings.from_config(_config.load())
            if not settings.enabled:
                if self._running or self._server:
                    self._stop_locked("")
                with self._state_lock:
                    self._settings = settings
                    self._last_error = ""
                self._write_status()
                return
            error = settings.validate()
            if error:
                if self._running or self._server:
                    self._stop_locked(error)
                with self._state_lock:
                    self._settings = settings
                    self._last_error = error
                self._write_status()
                self.log_info("集群配置无效，服务未启动：%s", error)
                return
            if self._running:
                current = self._settings or settings
                if (
                    current.role,
                    current.listen_host,
                    current.listen_port,
                ) != (settings.role, settings.listen_host, settings.listen_port):
                    self._stop_locked("")
                    self._start_locked(settings)
                    return
                with self._state_lock:
                    self._settings = settings
                if self.registry is not None:
                    self.registry.offline_sec = settings.offline_sec
                    self.registry.require_approval = settings.require_approval
                if settings.role == "worker" and settings.hubs and not self._worker:
                    self._start_worker_locked()
                if (settings.role != "worker" or not settings.hubs) and self._worker:
                    self._stop_worker_locked()
                self._write_status()
                return
            now = time.time()
            if now - self._last_start_attempt < START_RETRY_SEC and self._last_error:
                self._write_status()
                return
            self._last_start_attempt = now
            self._start_locked(settings)

    def start(self, settings: ClusterSettings) -> bool:
        """测试/显式启动入口；生产走 ensure()。"""
        with self._lock:
            return self._start_locked(settings)

    def stop(self):
        with self._lock:
            self._stop_locked("")

    def _start_worker_locked(self):
        if self._worker:
            return
        self._worker_stop = threading.Event()
        self._worker = _WorkerLoop(self)
        self._worker.start()

    def _stop_worker_locked(self):
        worker = self._worker
        self._worker = None
        if self._worker_stop is not None:
            self._worker_stop.set()
        if worker and worker.is_alive():
            worker.join(timeout=2.0)

    def _start_locked(self, settings: ClusterSettings) -> bool:
        server = None
        try:
            with self._state_lock:
                self._settings = settings
            if self.registry is None:
                self.registry = NodeRegistry(
                    self.base / "registry.json",
                    offline_sec=settings.offline_sec,
                    require_approval=settings.require_approval,
                )
            else:
                self.registry.offline_sec = settings.offline_sec
                self.registry.require_approval = settings.require_approval
            server = _ClusterHTTPServer(
                (settings.listen_host or "0.0.0.0", int(settings.listen_port)),
                _Handler,
            )
            server.cluster_service = self
            thread = threading.Thread(
                target=server.serve_forever, name="cluster-http", daemon=True
            )
            with self._state_lock:
                self._server = server
                self._thread = thread
                self._actual_port = int(server.server_address[1])
                self._running = True
                self._started_at = _now_iso()
                self._last_error = ""
                self._local_ips_set = _collect_local_ips(settings)
            thread.start()
            try:
                from .export import recover_orphans

                recover_orphans(self.base)
            except Exception:  # noqa: BLE001
                pass
            if settings.role == "worker" and settings.hubs:
                self._start_worker_locked()
            self.log_info(
                "集群服务已启动：%s:%d（角色=%s，节点=%s）",
                settings.listen_host,
                self._actual_port,
                settings.role,
                self.identity.node_id(),
            )
            self._write_status()
            return True
        except Exception as e:  # noqa: BLE001
            with self._state_lock:
                self._running = False
                self._last_error = str(e)
                self._actual_port = 0
                self._server = None
                self._thread = None
            self.log_exception("集群服务启动失败: %s", e)
            if server is not None:
                try:
                    server.server_close()
                except Exception:  # noqa: BLE001
                    pass
            self._write_status()
            return False

    def _stop_locked(self, error: str = ""):
        with self._state_lock:
            self._running = False
        with self._export_lock:
            exports = list(self._exports.values())
        for export in exports:
            try:
                export.cancel()
            except Exception:  # noqa: BLE001
                pass
        self._stop_worker_locked()
        server = self._server
        thread = self._thread
        self._server = None
        self._thread = None
        if server is not None:
            try:
                server.shutdown()
            except Exception:  # noqa: BLE001
                pass
            try:
                server.server_close()
            except Exception:  # noqa: BLE001
                pass
        if thread and thread.is_alive():
            thread.join(timeout=2.0)
        with self._state_lock:
            self._actual_port = 0
            self._last_error = error
        self._write_status()

    # ---------------- 状态 ----------------

    def status(self) -> dict:
        with self._state_lock:
            settings = self._settings or ClusterSettings()
            running = self._running
            actual_port = self._actual_port or settings.listen_port
            worker = dict(self._worker_state) if self._worker_state else {}
            started_at = self._started_at
            last_error = self._last_error
        counts = (
            self.registry.counts()
            if self.registry is not None
            else {"total": 0, "approved": 0, "pending": 0}
        )
        return {
            "running": bool(running),
            "disabled": not settings.enabled,
            "role": settings.role,
            "cluster_id": settings.cluster_id,
            "node_id": self.identity.node_id(),
            "listen_host": settings.listen_host,
            "listen_port": actual_port,
            "secret_set": bool(settings.secret),
            "started_at": started_at,
            "updated_at": _now_iso(),
            "last_error": last_error,
            "pending": counts["pending"],
            "nodes": counts["approved"],
            "worker": worker,
        }

    def _write_status(self):
        data = self.status()
        with self._status_write_lock:
            _write_json(self.status_path, data)

    def set_worker_state(self, state: dict):
        with self._state_lock:
            previous = self._worker_state or {}
            merged = dict(state)
            if state.get("status") == "approved":
                merged["last_ok"] = state.get("last_ok") or _now_iso()
            else:
                merged["last_ok"] = state.get("last_ok") or previous.get(
                    "last_ok", ""
                )
            merged["updated_at"] = _now_iso()
            self._worker_state = merged
        _write_json(self.peers_path, merged)
        self._write_status()

    def node_info(self, settings: ClusterSettings) -> dict:
        info = self.identity.info(
            settings, port=self._actual_port or settings.listen_port
        )
        try:
            cfg = _config.load()
            info["sync_tables"] = identity.sync_tables(cfg, settings.team_id)
            info["export_ready"] = self._export_ready(cfg)
        except Exception:  # noqa: BLE001
            info["sync_tables"] = {}
            info["export_ready"] = {"ready": False, "reason": "本机状态读取失败"}
        return info

    def _export_ready(self, cfg: dict) -> dict:
        """导出就绪（SSO 凭据 + 浏览器/Electron），结果缓存 60s 避免心跳频繁探测。"""
        now = time.time()
        with self._state_lock:
            cached = getattr(self, "_export_ready_cache", None)
            if cached and now - cached[0] < 60:
                return dict(cached[1])
        reason = ""
        sso = cfg.get("sso") or {}
        ini_auth = (cfg.get("_ini") or {}).get("auth") or {}
        mock = bool((cfg.get("dev") or {}).get("mock"))
        has_token = str(ini_auth.get("mode") or "").strip() == "token" and bool(
            str(ini_auth.get("token") or "").strip()
        )
        has_sso = bool(str(sso.get("username") or "").strip()) and bool(
            str(sso.get("password") or "")
        )
        chrome = ""
        electron = ""
        try:
            import platform_utils

            chrome = platform_utils.detect_chrome(
                (_config.render_cfg(cfg).get("chrome") or "")
            )
        except Exception:  # noqa: BLE001
            chrome = ""
        try:
            import platform_utils

            electron = platform_utils.electron_binary()
        except Exception:  # noqa: BLE001
            electron = ""
        if not mock and not (has_sso or has_token):
            reason = "未配置 SSO 账号密码"
        elif not mock and not (chrome or electron):
            reason = "未找到可用浏览器/Electron"
        oe = cfg.get("order_export") or {}
        try:
            query_workers = max(1, min(16, int(oe.get("query_workers", 8))))
        except (TypeError, ValueError):
            query_workers = 8
        try:
            render_workers = max(1, min(16, int(oe.get("render_workers", 4))))
        except (TypeError, ValueError):
            render_workers = 4
        result = {
            "ready": mock or not reason,
            "reason": reason,
            "query_workers": query_workers,
            "render_workers": render_workers,
            "chrome": bool(chrome),
            "electron": bool(electron),
        }
        with self._state_lock:
            self._export_ready_cache = (now, dict(result))
        return result

    def _self_node(self, settings: ClusterSettings) -> dict:
        info = self.node_info(settings)
        info.update(
            {
                "approved": True,
                "online": True,
                "self": True,
                "ip": settings.advertise_host
                or identity.best_local_ip()
                or "127.0.0.1",
                "last_seen": _now_iso(),
            }
        )
        return info

    def _nodes_with_self(self, settings: ClusterSettings) -> list:
        self_node = self._self_node(settings)
        nodes = self.registry.snapshot() if self.registry is not None else []
        nodes = [n for n in nodes if n.get("node_id") != self_node["node_id"]]
        nodes.insert(0, self_node)
        return nodes

    def nodes_file(self) -> dict:
        """Hub 写盘根 cluster_nodes.json 的内容；非 Hub/未运行返回空。"""
        settings = self.current_settings()
        with self._state_lock:
            running = self._running
        if not settings.enabled or settings.role != "hub" or not running:
            return {}
        nodes = self._nodes_with_self(settings)
        hub_id = self.identity.node_id()
        for node in nodes:
            node["home_hub"] = hub_id
        return {
            "version": 1,
            "cluster_id": settings.cluster_id,
            "updated_at": _now_iso(),
            "nodes": nodes,
        }

    def write_nodes_file(self, mount: str) -> bool:
        data = self.nodes_file()
        if not data:
            return False
        try:
            usb_protocol.write_json_atomic(Path(mount) / NODES_FILE, data)
            return True
        except OSError:
            return False

    def admin_state(self) -> dict:
        with self._state_lock:
            settings = self._settings or ClusterSettings()
            worker = dict(self._worker_state) if self._worker_state else {}
        self_node = self._self_node(settings)
        if settings.role == "hub":
            nodes = self._nodes_with_self(settings)
            pending = self.registry.pending() if self.registry is not None else []
        else:
            nodes = [
                n
                for n in (worker.get("nodes") or [])
                if isinstance(n, dict)
            ]
            pending = []
        return {
            "service": self.status(),
            "identity": self_node,
            "config": settings.public(),
            "nodes": nodes,
            "pending": pending,
            "worker": worker,
        }

    # ---------------- HTTP 入口 ----------------

    def is_local_client(self, client_ip: str, settings: ClusterSettings) -> bool:
        value = str(client_ip or "").strip()
        if value.startswith("::ffff:"):
            value = value[7:]
        if value in ("127.0.0.1", "::1", "localhost"):
            return True
        if value in self._local_ips_set:
            return True
        return value in (settings.listen_host, settings.advertise_host)

    def handle_admin(self, method: str, path: str, raw: bytes, client_ip: str):
        settings = self.current_settings()
        if not self.is_local_client(client_ip, settings):
            raise protocol.ClusterForbiddenError("管理接口只允许本机访问")
        if path in ("/cluster/admin/state", "/cluster/admin/status"):
            if method != "GET":
                raise protocol.ClusterNotFoundError("方法不允许")
            return self.admin_state()
        if path in ("/cluster/admin/approve", "/cluster/admin/reject"):
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            if settings.role != "hub":
                raise protocol.ClusterForbiddenError("只有 Hub 可以审批节点")
            body = protocol.parse_json_body(raw)
            node_id = str(body.get("node_id") or "").strip()
            if not protocol.NODE_ID_RE.match(node_id):
                raise protocol.ClusterProtocolError("node_id 非法")
            if path.endswith("approve"):
                try:
                    self.registry.approve(node_id)
                except KeyError:
                    raise protocol.ClusterNotFoundError("节点不存在或已离线")
            else:
                if not self.registry.reject(node_id):
                    raise protocol.ClusterNotFoundError("节点不存在")
            self._write_status()
            return self.admin_state()
        if path in (
            "/cluster/admin/export/start",
            "/cluster/admin/export/status",
            "/cluster/admin/export/cancel",
        ):
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(raw)
            if path.endswith("/start"):
                return self.start_export(body)
            if path.endswith("/status"):
                return self.export_status(body.get("job_id"))
            return self.cancel_export(body.get("job_id"))
        if path in (
            "/cluster/admin/update/packages",
            "/cluster/admin/update/packages/remove",
        ):
            if path.endswith("/remove"):
                if method != "POST":
                    raise protocol.ClusterNotFoundError("方法不允许")
                return self.remove_update_package(protocol.parse_json_body(raw))
            if method == "GET":
                return {"packages": self.list_update_packages()}
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            return self.add_update_package(protocol.parse_json_body(raw))
        if path == "/cluster/admin/update/push":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            return self.push_update(protocol.parse_json_body(raw))
        if path == "/cluster/admin/update/status":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(raw)
            return self.update_status(body.get("job_id"))
        if path == "/cluster/admin/update/cancel":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(raw)
            return self.cancel_update(body.get("job_id"))
        if path == "/cluster/admin/update/pending":
            if method != "GET":
                raise protocol.ClusterNotFoundError("方法不允许")
            return {"pending": self.pending_update()}
        if path == "/cluster/admin/update/open":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            return self.open_pending_update()
        raise protocol.ClusterNotFoundError("未知管理端点")

    def handle_public(
        self, method: str, path: str, raw: bytes, headers, client_ip: str
    ):
        settings = self.current_settings()
        node_id = protocol.verify_headers(
            settings.secret,
            settings.cluster_id,
            method,
            path,
            headers,
            raw,
            self._nonce,
        )
        if path == "/cluster/ping":
            return {
                "node": self._self_node(settings),
                "protocol": protocol.PROTOCOL_VERSION,
            }
        if path in ("/cluster/join", "/cluster/heartbeat"):
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(raw)
            info = protocol.validate_node_info(body.get("node"))
            if info["node_id"] != node_id:
                raise protocol.ClusterAuthError("node_id 与签名不一致")
            entry = self.registry.touch(info, ip=client_ip)
            approved = bool(entry.get("approved"))
            return {
                "status": "approved" if approved else "pending",
                "hub": self._self_node(settings),
                "node": entry,
                "nodes": self._nodes_with_self(settings) if approved else [],
            }
        if path == "/cluster/nodes":
            if not self.registry.is_approved(node_id):
                raise protocol.ClusterForbiddenError("节点尚未审批")
            return {"nodes": self._nodes_with_self(settings)}
        if path == "/cluster/command":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(
                raw, max_bytes=protocol.MAX_COMMAND_BODY
            )
            return self.handle_remote_command(body, node_id)
        if path.startswith("/cluster/command/") and path.count("/") == 3:
            cmd_id = path.rsplit("/", 1)[-1]
            if not usb_protocol.ID_RE.match(cmd_id):
                raise protocol.ClusterProtocolError("指令 id 非法")
            return self.get_remote_result(cmd_id, node_id)
        if path == "/cluster/export/run":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(
                raw, max_bytes=protocol.MAX_COMMAND_BODY
            )
            return self.handle_export_run(body, node_id)
        if path == "/cluster/export/cancel":
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(raw)
            return self.handle_export_cancel(body, node_id)
        if path in (
            "/cluster/update/begin",
            "/cluster/update/chunk",
            "/cluster/update/commit",
        ):
            if method != "POST":
                raise protocol.ClusterNotFoundError("方法不允许")
            body = protocol.parse_json_body(
                raw, max_bytes=protocol.MAX_COMMAND_BODY
            )
            if path.endswith("/begin"):
                return self.handle_update_begin(body, node_id)
            if path.endswith("/chunk"):
                return self.handle_update_chunk(body, node_id)
            return self.handle_update_commit(body, node_id)
        raise protocol.ClusterNotFoundError("未知端点: %s" % path)

    # ---------------- 指令转发/远端执行 ----------------

    def route_command(self, cmd: dict):
        """返回 (local|remote|deny, 原因)。"""
        settings = self.current_settings()
        target = str((cmd.get("target") or {}).get("node_id") or "").strip()
        cluster_id = str(cmd.get("cluster") or "").strip()
        if cluster_id and settings.cluster_id and cluster_id != settings.cluster_id:
            return "deny", "集群标识不匹配"
        if not target or target == self.identity.node_id():
            return "local", ""
        if not settings.enabled:
            return "deny", "集群未启用，无法转发给目标节点"
        if settings.role != "hub":
            return "deny", "本节点不是 Hub，无法转发"
        with self._state_lock:
            if not self._running or self.registry is None:
                return "deny", "集群服务未运行，无法转发"
        return "remote", ""

    def _remote_timeout(self, cmd: dict):
        now = time.time()
        limit = now + MAX_COMMAND_MINUTES * 60
        expires = usb_protocol.parse_iso(cmd.get("expires_at"))
        if expires is not None:
            limit = min(limit, expires.timestamp() - REMOTE_DEADLINE_MARGIN_SEC)
        timeout = limit - now
        return timeout if timeout >= 5 else None

    def _relay_failure(self, cmd: dict, error: str) -> dict:
        target = str((cmd.get("target") or {}).get("node_id") or "")
        return usb_protocol.build_result(
            cmd,
            False,
            error,
            data={"relayed": True},
            version=_version.read_version(),
            node=target,
            relayed_by=self.identity.node_id(),
        )

    def relay_command(self, cmd: dict, mount: str):
        """Hub 把指令转发给目标节点；返回 (result, artifacts) 或失败结果。"""
        settings = self.current_settings()
        target_id = str((cmd.get("target") or {}).get("node_id") or "")
        if self.registry is None:
            return self._relay_failure(cmd, "集群注册表未就绪"), []
        entry = self.registry.get(target_id)
        if not entry:
            return self._relay_failure(cmd, "目标节点不在集群注册表"), []
        if not entry.get("approved"):
            return self._relay_failure(cmd, "目标节点尚未审批"), []
        if not entry.get("online"):
            return (
                self._relay_failure(
                    cmd, "目标节点离线（最近心跳 %s）" % entry.get("last_seen", "")
                ),
                [],
            )
        ip = str(entry.get("ip") or "").strip()
        try:
            port = int(entry.get("port") or 0)
        except (TypeError, ValueError):
            port = 0
        if not ip or not 1 <= port <= 65535:
            return self._relay_failure(cmd, "目标节点地址无效"), []
        timeout = self._remote_timeout(cmd)
        if timeout is None:
            return self._relay_failure(cmd, "指令剩余时间不足，无法转发"), []

        attachments = []
        if str(cmd.get("action") or "") == "replace":
            name = str((cmd.get("params") or {}).get("file") or "").strip()
            data = self._read_mount_file(mount, name)
            if data is None:
                return (
                    self._relay_failure(cmd, "U盘新表不存在或不可读: %s" % name),
                    [],
                )
            attachments.append(
                {
                    "name": name,
                    "data_b64": base64.b64encode(data).decode("ascii"),
                }
            )
        try:
            data = request_json(
                "http://%s:%d" % (ip, port),
                "/cluster/command",
                {"command": cmd, "attachments": attachments},
                settings,
                node_id=self.identity.node_id(),
                timeout=timeout,
                max_response=protocol.MAX_COMMAND_BODY,
            )
        except Exception as e:  # noqa: BLE001
            return self._relay_failure(cmd, "转发到目标节点失败: %s" % e), []
        result = data.get("result")
        if not isinstance(result, dict):
            return self._relay_failure(cmd, "目标节点未返回有效结果"), []
        if str(result.get("id")) != str(cmd.get("id")) or str(
            result.get("app")
        ) != str(cmd.get("app")):
            return self._relay_failure(cmd, "目标节点结果 id/app 不匹配"), []
        result["node"] = target_id
        result["relayed_by"] = self.identity.node_id()
        artifacts = data.get("artifacts") or []
        if not isinstance(artifacts, list):
            return self._relay_failure(cmd, "目标节点产物格式非法"), []
        checked = []
        for item in artifacts:
            if not isinstance(item, dict):
                return self._relay_failure(cmd, "目标节点产物格式非法"), []
            name = str(item.get("name") or "")
            if not self._safe_artifact_name(name):
                return self._relay_failure(cmd, "目标节点产物文件名非法"), []
            try:
                raw = base64.b64decode(str(item.get("data_b64") or ""), validate=True)
            except Exception:  # noqa: BLE001
                return self._relay_failure(cmd, "目标节点产物不是合法 base64"), []
            if len(raw) > MAX_ARTIFACT:
                return self._relay_failure(cmd, "目标节点产物超过大小限制"), []
            checked.append({"name": name, "_data": raw})
        result_data = result.get("data")
        if isinstance(result_data, dict):
            file_name = str(result_data.get("file") or "")
            if file_name:
                base_name = Path(file_name).name
                for item in checked:
                    if item["name"] == base_name:
                        result_data["file"] = item["name"]
                        break
        return result, checked

    def finish_replace_source(self, mount: str, cmd: dict):
        """远端 replace 成功后删除 U 盘新表（与本地 _replace 的消费语义一致）。"""
        name = str((cmd.get("params") or {}).get("file") or "").strip()
        if not self._safe_artifact_name(name):
            return
        try:
            (Path(mount) / name).unlink()
        except OSError:
            pass

    def _read_mount_file(self, mount: str, name: str):
        if not self._safe_artifact_name(name):
            return None
        path = Path(mount) / name
        try:
            if not path.is_file():
                return None
            if path.stat().st_size > MAX_ARTIFACT:
                return None
            return path.read_bytes()
        except OSError:
            return None

    @staticmethod
    def _safe_artifact_name(name) -> bool:
        value = str(name or "")
        if not value or value in (".", "..") or len(value) > 180:
            return False
        if any(ch in value for ch in ("/", "\\", "\x00")):
            return False
        if value in (NODES_FILE, usb_protocol.STATUS_NAME):
            return False
        if value.endswith("_cmd.json") or (
            "_result_" in value and value.endswith(".json")
        ):
            return False
        return Path(value).name == value

    def write_artifacts(self, mount: str, artifacts: list):
        for item in artifacts or []:
            name = str(item.get("name") or "")
            raw = item.get("_data")
            if not self._safe_artifact_name(name):
                raise ValueError("产物文件名非法: %s" % name)
            if not isinstance(raw, (bytes, bytearray)):
                raise ValueError("产物内容缺失: %s" % name)
            if len(raw) > MAX_ARTIFACT:
                raise ValueError("产物超过大小限制: %s" % name)
            path = Path(mount) / name
            tmp = path.with_name(name + ".relay-tmp")
            try:
                tmp.write_bytes(bytes(raw))
                os.replace(str(tmp), str(path))
            finally:
                try:
                    tmp.unlink()
                except OSError:
                    pass

    def recover_remote(self, cmd: dict):
        """Hub 重启后向目标节点取回已执行结果；取不到返回 None。"""
        settings = self.current_settings()
        target_id = str((cmd.get("target") or {}).get("node_id") or "")
        entry = self.registry.get(target_id) if self.registry is not None else None
        if not entry or not entry.get("approved"):
            return None
        ip = str(entry.get("ip") or "").strip()
        try:
            port = int(entry.get("port") or 0)
        except (TypeError, ValueError):
            port = 0
        if not ip or not 1 <= port <= 65535:
            return None
        try:
            data = request_json(
                "http://%s:%d" % (ip, port),
                "/cluster/command/%s" % usb_protocol.sanitize_id(cmd.get("id")),
                {},
                settings,
                node_id=self.identity.node_id(),
                timeout=8,
                max_response=protocol.MAX_COMMAND_BODY,
            )
        except Exception:  # noqa: BLE001
            return None
        result = data.get("result")
        if not isinstance(result, dict):
            return None
        result["node"] = target_id
        result["relayed_by"] = self.identity.node_id()
        artifacts = []
        for item in data.get("artifacts") or []:
            if not isinstance(item, dict):
                continue
            name = str(item.get("name") or "")
            if not self._safe_artifact_name(name):
                continue
            try:
                raw = base64.b64decode(str(item.get("data_b64") or ""), validate=True)
            except Exception:  # noqa: BLE001
                continue
            if len(raw) <= MAX_ARTIFACT:
                artifacts.append({"name": name, "_data": raw})
        return result, artifacts

    def handle_remote_command(self, body: dict, requester: str) -> dict:
        settings = self.current_settings()
        try:
            cmd = usb_protocol.normalize_command(body.get("command"))
        except usb_protocol.ProtocolError as e:
            raise protocol.ClusterProtocolError("远端指令非法: %s" % e)
        target = str((cmd.get("target") or {}).get("node_id") or "")
        if target != self.identity.node_id():
            raise protocol.ClusterForbiddenError("指令目标不是本机")
        cluster_id = str(cmd.get("cluster") or "")
        if cluster_id and settings.cluster_id and cluster_id != settings.cluster_id:
            raise protocol.ClusterForbiddenError("指令集群标识不匹配")
        if usb_protocol.is_expired(cmd):
            return {
                "result": self._remote_result(
                    cmd, False, "指令已过期", {}
                ),
                "artifacts": [],
            }
        team_id = 0
        try:
            team_id = int((cmd.get("params") or {}).get("team_id") or 0)
        except (TypeError, ValueError):
            team_id = 0
        if team_id and settings.team_id and team_id != settings.team_id:
            return {
                "result": self._remote_result(
                    cmd,
                    False,
                    "节点班组不匹配（本机 %d，指令 %d）"
                    % (settings.team_id, team_id),
                    {},
                ),
                "artifacts": [],
            }
        if self.jobs is None or self.apps_provider is None:
            return {
                "result": self._remote_result(
                    cmd, False, "本机任务管理器未接入", {}
                ),
                "artifacts": [],
            }
        apps = self.apps_provider() or {}
        app = apps.get(cmd.get("app"))
        if not app:
            return {
                "result": self._remote_result(
                    cmd, False, "本机未加载应用 %s" % cmd.get("app"), {}
                ),
                "artifacts": [],
            }
        staging = self.base / "inbox" / usb_protocol.sanitize_id(cmd["id"])
        if staging.exists():
            shutil.rmtree(staging, ignore_errors=True)
        staging.mkdir(parents=True, exist_ok=True)
        try:
            self._write_attachments(staging, body.get("attachments"))
        except Exception as e:  # noqa: BLE001
            return {
                "result": self._remote_result(
                    cmd, False, "附件写入失败: %s" % e, {}
                ),
                "artifacts": [],
            }
        meta, backend = app
        job = self.jobs.create(cmd["app"], "集群指令·%s" % meta.get("name", cmd["app"]))
        job.echo = False
        started = time.time()
        try:
            self.jobs.run_sync(
                job,
                lambda j: backend.handle_command(j, cmd, str(staging)),
                echo=False,
            )
        except Exception:  # noqa: BLE001
            pass
        finished = time.time()
        ok = job.status == "success"
        error = "" if ok else (job.error or "任务%s" % job.status)
        data = job.result if isinstance(job.result, dict) else {}
        artifacts = self._collect_artifacts(staging, data)
        result = self._remote_result(
            cmd,
            ok,
            error,
            data,
            started=started,
            finished=finished,
        )
        self._store_remote_result(cmd["id"], result, artifacts)
        if ok:
            shutil.rmtree(staging, ignore_errors=True)
        return {
            "result": result,
            "artifacts": [
                {"name": item["name"], "data_b64": item["data_b64"]}
                for item in artifacts
            ],
        }

    def _remote_result(
        self,
        cmd: dict,
        ok: bool,
        error: str,
        data: dict,
        started: float = 0,
        finished: float = 0,
    ) -> dict:
        return usb_protocol.build_result(
            cmd,
            ok,
            error,
            data=data,
            started_at="",
            finished_at="",
            duration_ms=int(max(0, finished - started) * 1000) if started else 0,
            version=_version.read_version(),
            node=self.identity.node_id(),
        )

    def _write_attachments(self, staging: Path, attachments):
        if attachments is None:
            return
        if not isinstance(attachments, list):
            raise ValueError("attachments 必须是数组")
        for item in attachments[:8]:
            if not isinstance(item, dict):
                raise ValueError("附件格式非法")
            name = str(item.get("name") or "")
            if not self._safe_artifact_name(name):
                raise ValueError("附件文件名非法: %s" % name)
            try:
                raw = base64.b64decode(str(item.get("data_b64") or ""), validate=True)
            except Exception:  # noqa: BLE001
                raise ValueError("附件不是合法 base64: %s" % name)
            if len(raw) > MAX_ARTIFACT:
                raise ValueError("附件超过大小限制: %s" % name)
            (staging / name).write_bytes(raw)

    def _collect_artifacts(self, staging: Path, data: dict) -> list:
        out = []
        if not isinstance(data, dict):
            return out
        value = data.get("file")
        if not value:
            return out
        try:
            resolved = Path(str(value)).resolve()
            root = staging.resolve()
            if root not in resolved.parents or not resolved.is_file():
                return out
            if resolved.stat().st_size > MAX_ARTIFACT:
                return out
            out.append(
                {
                    "name": resolved.name,
                    "data_b64": base64.b64encode(resolved.read_bytes()).decode("ascii"),
                }
            )
            data["file"] = resolved.name
        except OSError:
            return []
        return out

    def _store_remote_result(self, cmd_id: str, result: dict, artifacts: list):
        safe_id = usb_protocol.sanitize_id(cmd_id)
        root = self.base / "results"
        result_dir = root / safe_id
        if result_dir.exists():
            shutil.rmtree(result_dir, ignore_errors=True)
        result_dir.mkdir(parents=True, exist_ok=True)
        names = []
        for item in artifacts:
            name = str(item.get("name") or "")
            try:
                raw = base64.b64decode(str(item.get("data_b64") or ""), validate=True)
            except Exception:  # noqa: BLE001
                continue
            if not self._safe_artifact_name(name) or len(raw) > MAX_ARTIFACT:
                continue
            (result_dir / name).write_bytes(raw)
            names.append(name)
        try:
            usb_protocol.write_json_atomic(
                root / (safe_id + ".json"),
                {"result": result, "artifacts": names},
            )
        except OSError:
            pass
        self._prune_remote_results()

    def get_remote_result(self, cmd_id: str, requester: str) -> dict:
        safe_id = usb_protocol.sanitize_id(cmd_id)
        root = self.base / "results"
        meta = _read_json(root / (safe_id + ".json"))
        result = meta.get("result")
        if not isinstance(result, dict):
            raise protocol.ClusterNotFoundError("结果不存在或已过期")
        artifacts = []
        for name in meta.get("artifacts") or []:
            if not self._safe_artifact_name(name):
                continue
            path = root / safe_id / name
            try:
                if not path.is_file() or path.stat().st_size > MAX_ARTIFACT:
                    continue
                artifacts.append(
                    {
                        "name": name,
                        "data_b64": base64.b64encode(path.read_bytes()).decode(
                            "ascii"
                        ),
                    }
                )
            except OSError:
                continue
        return {"result": result, "artifacts": artifacts}

    def _prune_remote_results(self):
        root = self.base / "results"
        cutoff = time.time() - 86400
        try:
            for path in root.iterdir():
                try:
                    if path.stat().st_mtime < cutoff:
                        if path.is_dir():
                            shutil.rmtree(path, ignore_errors=True)
                        else:
                            path.unlink()
                except OSError:
                    pass
        except OSError:
            pass

    # ---------------- 集群导出 ----------------

    def _export_backend(self):
        apps = self.apps_provider() or {} if self.apps_provider else {}
        app = apps.get("pcd_export")
        if not app:
            return None
        _meta, backend = app
        return backend

    def _export_nodes(self, params: dict) -> list:
        settings = self.current_settings()
        self_node = self._self_node(settings)
        known = {}
        with self._state_lock:
            worker = dict(self._worker_state) if self._worker_state else {}
        if settings.role == "hub" and self.registry is not None:
            for node in self.registry.snapshot():
                known[node["node_id"]] = node
        else:
            for node in worker.get("nodes") or []:
                if isinstance(node, dict) and node.get("node_id"):
                    known[node["node_id"]] = node
        known[self_node["node_id"]] = self_node
        selected = params.get("nodes") or []
        if not selected:
            raise RuntimeError("未选择集群节点")
        out = []
        for raw in selected:
            item = raw if isinstance(raw, dict) else {"node_id": str(raw)}
            node_id = str(item.get("node_id") or "").strip()
            node = known.get(node_id)
            if node is None:
                raise RuntimeError("节点不在集群列表: %s" % node_id)
            if node_id != self_node["node_id"] and not node.get("online"):
                raise RuntimeError("节点离线: %s" % (node.get("name") or node_id))
            ready = node.get("export_ready") or {}
            if ready and not ready.get("ready"):
                raise RuntimeError(
                    "节点未就绪: %s（%s）"
                    % (node.get("name") or node_id, ready.get("reason") or "配置缺失")
                )
            if node_id == self_node["node_id"]:
                url = "http://127.0.0.1:%d" % (self.actual_port or settings.listen_port)
            else:
                ip = str(node.get("ip") or "").strip()
                try:
                    port = int(node.get("port") or 0)
                except (TypeError, ValueError):
                    port = 0
                if not ip or not 1 <= port <= 65535:
                    raise RuntimeError("节点地址无效: %s" % node_id)
                url = "http://%s:%d" % (ip, port)
            out.append(
                {
                    "node_id": node_id,
                    "name": node.get("name") or node_id,
                    "url": url,
                    "query_workers": _as_int(
                        item.get("query_workers")
                        or ready.get("query_workers")
                        or 8,
                        8,
                        1,
                        16,
                    ),
                    "render_workers": _as_int(
                        item.get("render_workers")
                        or ready.get("render_workers")
                        or 4,
                        4,
                        1,
                        16,
                    ),
                }
            )
        return out

    def _local_export_node(self, settings) -> dict:
        node = self._self_node(settings)
        ready = node.get("export_ready") or {}
        return {
            "node_id": node["node_id"],
            "name": node.get("name") or node["node_id"],
            "url": "http://127.0.0.1:%d"
            % (self.actual_port or settings.listen_port),
            "query_workers": _as_int(ready.get("query_workers") or 8, 8, 1, 16),
            "render_workers": _as_int(ready.get("render_workers") or 4, 4, 1, 16),
        }

    def start_export(self, params: dict) -> dict:
        codes = params.get("codes") or []
        if not codes:
            raise RuntimeError("没有可导出的派车单号")
        if not str(params.get("outdir") or "").strip():
            raise RuntimeError("未填写输出目录")
        backend = self._export_backend()
        if backend is None:
            raise RuntimeError("本机未加载轨迹导出后端（pcd_export）")
        nodes = self._export_nodes(params)
        fallback_node = self._local_export_node(self.current_settings())
        cfg = _config.load()
        export_cfg = ((cfg.get("cluster") or {}).get("export") or {})
        chunk_size = _as_int(export_cfg.get("chunk_size"), 6, 1, 50)
        max_attempts = _as_int(export_cfg.get("max_attempts"), 2, 1, 5)
        timeout_sec = _as_int(export_cfg.get("request_timeout_sec"), 1800, 60, 7200)
        try:
            max_seq = max(int(c.get("seq") or 0) for c in codes)
        except (TypeError, ValueError):
            max_seq = len(codes)
        merged = dict(params)
        merged["local_node_id"] = self.identity.node_id()
        merged["started_at"] = time.strftime("%Y%m%d_%H%M%S")
        merged["cluster_width"] = len(str(max(1, max_seq)))
        job = self.jobs.create("pcd_export", "集群导出·%d单" % len(codes))
        job.echo = False
        job.keep_awake = bool(merged.get("keep_awake"))
        from .export import ClusterExport

        export = ClusterExport(
            self,
            job,
            merged,
            nodes,
            self._request_peer,
            backend,
            chunk_size=chunk_size,
            max_attempts=max_attempts,
            timeout_sec=timeout_sec,
            fallback_node=fallback_node,
        )
        with self._export_lock:
            self._exports[job.id] = export
        self.jobs.start(job, lambda j: export.run())
        return {
            "job_id": job.id,
            "total": len(codes),
            "nodes": [n["node_id"] for n in nodes],
        }

    def export_status(self, job_id: str) -> dict:
        job_id = str(job_id or "").strip()
        if not usb_protocol.ID_RE.match(job_id):
            raise RuntimeError("job_id 非法")
        with self._export_lock:
            export = self._exports.get(job_id)
        if export is not None:
            return export.snapshot()
        state_file = self.base / "jobs" / job_id / "state.json"
        data = _read_json(state_file)
        if not data:
            raise RuntimeError("集群导出任务不存在")
        archive = _read_json(_state.jobs_dir() / (job_id + ".json"))
        job_dict = archive or {
            "id": job_id,
            "status": data.get("status"),
            "progress": {"current": 0, "total": 0},
            "result": {},
            "error": data.get("error", ""),
        }
        from .export import public_job

        job_dict = public_job(job_dict)
        chunks = data.get("chunks") or []
        rows = data.get("rows") or {}
        not_found = data.get("not_found") or []
        logs = []
        log_path = _state.logs_dir() / (job_id + ".log")
        try:
            lines = log_path.read_text(encoding="utf-8", errors="replace").splitlines()
            logs = lines[-500:]
        except OSError:
            pass
        return {
            "job": job_dict,
            "logs": logs,
            "queue": {
                "total": len(rows) + len(not_found)
                + sum(len(c.get("items") or []) for c in chunks if c.get("status") != "done"),
                "done": len(rows) + len(not_found),
                "pending": sum(1 for c in chunks if c.get("status") != "done"),
                "failed": len(data.get("failed") or {}),
            },
            "status": data.get("status", ""),
            "error": data.get("error", ""),
            "nodes": data.get("nodes") or [],
        }

    def cancel_export(self, job_id: str) -> dict:
        job_id = str(job_id or "").strip()
        with self._export_lock:
            export = self._exports.get(job_id)
        if export is None:
            raise RuntimeError("集群导出任务不存在")
        job = self.jobs.get(job_id) if self.jobs else None
        if job is not None:
            job.cancel()
        export.cancel()
        return {"job_id": job_id, "canceled": True}

    def _request_peer(self, url: str, path: str, body: dict, timeout: float = None):
        return request_json(
            url,
            path,
            body,
            self.current_settings(),
            node_id=self.identity.node_id(),
            timeout=timeout,
            max_response=protocol.MAX_COMMAND_BODY,
        )

    def handle_export_run(self, body: dict, requester: str) -> dict:
        job_id = str(body.get("job_id") or "").strip()
        chunk = body.get("chunk") or {}
        chunk_id = str(chunk.get("id") or "").strip()
        if not usb_protocol.ID_RE.match(job_id):
            raise protocol.ClusterProtocolError("集群任务 id 非法")
        if not usb_protocol.ID_RE.match(chunk_id):
            raise protocol.ClusterProtocolError("分片 id 非法")
        backend = self._export_backend()
        if backend is None:
            return {"result": {"error": "本机未加载轨迹导出后端"}}
        if self.jobs is None:
            return {"result": {"error": "本机任务管理器未接入"}}
        safe_job = usb_protocol.sanitize_id(job_id)
        safe_chunk = usb_protocol.sanitize_id(chunk_id)
        chunk_dir = self.base / "jobs" / safe_job / "chunks" / safe_chunk
        result_file = chunk_dir / "result.json"
        zip_file = chunk_dir / "result.zip"
        if result_file.exists():
            result = _read_json(result_file)
            zip_b64 = ""
            if zip_file.exists():
                try:
                    zip_b64 = base64.b64encode(zip_file.read_bytes()).decode("ascii")
                except OSError:
                    zip_b64 = ""
            return {"result": result, "zip_b64": zip_b64}
        chunk_dir.mkdir(parents=True, exist_ok=True)
        options = body.get("options") or {}
        concurrency = body.get("concurrency") or {}
        codes = chunk.get("items") or []
        try:
            max_seq = max(int(c.get("seq") or 0) for c in codes)
        except (TypeError, ValueError):
            max_seq = len(codes)
        params = {
            "codes": codes,
            "outdir": str(chunk_dir),
            "cluster": True,
            "cluster_width": _as_int(
                options.get("cluster_width"), len(str(max(1, max_seq))), 1, 12
            ),
            "export_pcd": bool(options.get("export_pcd", True)),
            "export_track": bool(options.get("export_track", True)),
            "calibrate_noon": bool(options.get("calibrate_noon", False)),
            "batch_subdir": False,
            "query_workers": _as_int(concurrency.get("query_workers"), 8, 1, 16),
            "render_workers": _as_int(concurrency.get("render_workers"), 4, 1, 16),
        }
        key = job_id + "/" + chunk_id
        job = self.jobs.create("pcd_export", "集群分片·%s" % job_id)
        job.echo = False
        with self._export_lock:
            self._export_runs[key] = job
        try:
            self.jobs.run_sync(
                job, lambda j: backend.run_cluster(j, params), echo=False
            )
        finally:
            with self._export_lock:
                self._export_runs.pop(key, None)
        if job.status == "success":
            result = job.result if isinstance(job.result, dict) else {}
        else:
            result = {"error": job.error or "任务%s" % job.status}
        zip_b64 = ""
        files_dir = chunk_dir / "逐单"
        try:
            buffer = io.BytesIO()
            total = 0
            with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
                if files_dir.is_dir():
                    for path in sorted(files_dir.iterdir()):
                        if not path.is_file():
                            continue
                        size = path.stat().st_size
                        total += size
                        if total > MAX_EXPORT_ZIP:
                            raise RuntimeError("分片产物超过大小限制")
                        archive.write(str(path), arcname=path.name)
            raw = buffer.getvalue()
            if raw:
                (chunk_dir / "result.zip").write_bytes(raw)
                zip_b64 = base64.b64encode(raw).decode("ascii")
        except Exception as e:  # noqa: BLE001
            result = {"error": "打包分片产物失败: %s" % e}
        try:
            usb_protocol.write_json_atomic(result_file, result)
        except OSError:
            pass
        return {"result": result, "zip_b64": zip_b64}

    def handle_export_cancel(self, body: dict, requester: str) -> dict:
        job_id = str(body.get("job_id") or "").strip()
        chunk_ids = body.get("chunk_ids") or []
        canceled = 0
        with self._export_lock:
            for chunk_id in chunk_ids:
                key = job_id + "/" + str(chunk_id)
                job = self._export_runs.get(key)
                if job is not None:
                    job.cancel()
                    canceled += 1
        return {"canceled": canceled}

    # ---------------- 节点更新（Hub 推送、本机手动安装） ----------------

    def _updates_root(self) -> Path:
        return self.base / "updates"

    def pending_update(self) -> dict:
        data = _read_json(self._updates_root() / UPDATE_PENDING_FILE)
        if not data:
            return {}
        path = Path(str(data.get("path") or ""))
        if not path.is_file():
            return {}
        return data

    def handle_update_begin(self, body: dict, requester: str) -> dict:
        update_id = str(body.get("update_id") or "").strip().lower()
        if len(update_id) != 64 or any(c not in "0123456789abcdef" for c in update_id):
            raise protocol.ClusterProtocolError("更新标识非法")
        filename = Path(str(body.get("filename") or "")).name
        if not filename or len(filename) > 180:
            raise protocol.ClusterProtocolError("安装包文件名非法")
        try:
            size = int(body.get("size") or 0)
        except (TypeError, ValueError):
            size = 0
        if not 0 < size <= MAX_UPDATE_SIZE:
            raise protocol.ClusterProtocolError("安装包大小非法")
        sha = str(body.get("sha256") or "").strip().lower()
        if sha != update_id:
            raise protocol.ClusterProtocolError("安装包校验值与更新标识不一致")
        target = self._updates_root() / update_id / filename
        target.parent.mkdir(parents=True, exist_ok=True)
        try:
            target.unlink()
        except OSError:
            pass
        platform = _normalize_platform(body.get("platform"))
        arch = _normalize_arch(body.get("arch"))
        current = self.identity.info(self.current_settings())
        if platform and platform != _normalize_platform(current.get("platform")):
            raise protocol.ClusterProtocolError("安装包平台与本机不一致")
        if arch and arch != _normalize_arch(current.get("arch")):
            raise protocol.ClusterProtocolError("安装包架构与本机不一致")
        transfer = {
            "target": target,
            "filename": filename,
            "size": size,
            "sha256": sha,
            "version": str(body.get("version") or "").strip()[:80],
            "platform": platform,
            "arch": arch,
            "written": 0,
            "from_node": str(requester or ""),
        }
        with self._update_lock:
            self._update_transfers[update_id] = transfer
        self.log_info("开始接收节点更新 %s（%s，%d 字节）", filename, update_id[:12], size)
        return {"update_id": update_id, "filename": filename, "size": size}

    def handle_update_chunk(self, body: dict, requester: str) -> dict:
        update_id = str(body.get("update_id") or "").strip().lower()
        with self._update_lock:
            transfer = self._update_transfers.get(update_id)
        if transfer is None:
            raise protocol.ClusterProtocolError("更新传输不存在或已结束")
        try:
            offset = int(body.get("offset") or 0)
        except (TypeError, ValueError):
            offset = -1
        try:
            raw = base64.b64decode(str(body.get("data_b64") or ""), validate=True)
        except Exception as e:  # noqa: BLE001
            raise protocol.ClusterProtocolError("更新分片编码非法: %s" % e)
        if not raw:
            raise protocol.ClusterProtocolError("更新分片为空")
        with self._update_lock:
            transfer = self._update_transfers.get(update_id)
            if transfer is None:
                raise protocol.ClusterProtocolError("更新传输不存在或已结束")
            if offset != transfer["written"]:
                raise protocol.ClusterProtocolError(
                    "更新分片偏移不一致（期望 %d）" % transfer["written"]
                )
            if transfer["written"] + len(raw) > transfer["size"]:
                raise protocol.ClusterProtocolError("更新分片超过声明大小")
            with open(transfer["target"], "ab") as fp:
                fp.write(raw)
            transfer["written"] += len(raw)
            written = transfer["written"]
            size = transfer["size"]
        return {"update_id": update_id, "written": written, "size": size}

    def handle_update_commit(self, body: dict, requester: str) -> dict:
        update_id = str(body.get("update_id") or "").strip().lower()
        with self._update_lock:
            transfer = self._update_transfers.get(update_id)
        if transfer is None:
            pending = self.pending_update()
            if pending.get("update_id") == update_id:
                return pending
            raise protocol.ClusterProtocolError("更新传输不存在或已结束")
        if transfer["written"] != transfer["size"]:
            raise protocol.ClusterProtocolError(
                "安装包未传完（%d/%d）" % (transfer["written"], transfer["size"])
            )
        actual = _sha256_file(transfer["target"])
        if actual != transfer["sha256"]:
            raise protocol.ClusterProtocolError("安装包校验失败")
        pending = {
            "update_id": update_id,
            "filename": transfer["filename"],
            "version": transfer["version"],
            "platform": transfer["platform"],
            "arch": transfer["arch"],
            "sha256": transfer["sha256"],
            "size": transfer["size"],
            "path": str(transfer["target"]),
            "from_node": transfer.get("from_node", ""),
            "pushed_at": _now_iso(),
        }
        usb_protocol.write_json_atomic(
            self._updates_root() / UPDATE_PENDING_FILE, pending
        )
        with self._update_lock:
            self._update_transfers.pop(update_id, None)
        self.log_info("节点更新已落盘待安装：%s", transfer["filename"])
        return pending

    def open_pending_update(self) -> dict:
        pending = self.pending_update()
        if not pending:
            raise RuntimeError("没有待安装的更新包")
        path = Path(str(pending.get("path") or "")).resolve()
        root = self._updates_root().resolve()
        if root not in path.parents:
            raise RuntimeError("更新包路径非法")
        try:
            import platform_utils

            platform_utils.open_path(str(path))
        except Exception as e:  # noqa: BLE001
            raise RuntimeError("打开安装包失败: %s" % e)
        return {"opened": True, "path": str(path), "filename": pending.get("filename", "")}

    def list_update_packages(self) -> list:
        data = _read_json(self._update_packages_path)
        packages = data.get("packages") or []
        return [item for item in packages if isinstance(item, dict) and item.get("path")]

    def add_update_package(self, params: dict) -> dict:
        raw_path = str(params.get("path") or "").strip()
        if not raw_path:
            raise RuntimeError("请选择安装包文件")
        path = Path(raw_path).expanduser().resolve()
        if not path.is_file():
            raise RuntimeError("安装包不存在: %s" % path)
        size = path.stat().st_size
        if not 0 < size <= MAX_UPDATE_SIZE:
            raise RuntimeError("安装包大小非法")
        sha = _sha256_file(path)
        current = self.identity.info(self.current_settings())
        platform = _normalize_platform(
            params.get("platform") or current.get("platform") or "linux"
        )
        arch = _normalize_arch(params.get("arch") or current.get("arch") or "amd64")
        version = str(params.get("version") or "").strip()[:80]
        package = {
            "id": sha,
            "sha256": sha,
            "path": str(path),
            "filename": path.name,
            "size": size,
            "platform": platform,
            "arch": arch,
            "version": version,
            "added_at": _now_iso(),
        }
        packages = [
            item for item in self.list_update_packages() if item.get("id") != sha
        ]
        packages.append(package)
        usb_protocol.write_json_atomic(
            self._update_packages_path, {"packages": packages}
        )
        self.log_info("登记更新安装包：%s（%s/%s）", path.name, platform, arch)
        return package

    def remove_update_package(self, params: dict) -> dict:
        package_id = str(params.get("id") or params.get("sha256") or "").strip()
        if not package_id:
            raise RuntimeError("请指定要移除的安装包")
        packages = self.list_update_packages()
        remaining = [item for item in packages if item.get("id") != package_id]
        if len(remaining) == len(packages):
            raise RuntimeError("安装包未登记: %s" % package_id)
        usb_protocol.write_json_atomic(
            self._update_packages_path, {"packages": remaining}
        )
        self.log_info("移除更新安装包登记：%s", package_id)
        return {"removed": package_id, "packages": remaining}

    def push_update(self, params: dict) -> dict:
        settings = self.current_settings()
        if settings.role != "hub":
            raise RuntimeError("只有 Hub 可以推送节点更新")
        if self.jobs is None:
            raise RuntimeError("本机任务管理器未接入")
        node_ids = params.get("nodes") or []
        if not isinstance(node_ids, list) or not node_ids:
            raise RuntimeError("未选择要更新的节点")
        packages = self.list_update_packages()
        if not packages:
            raise RuntimeError("尚未登记安装包")
        known = {n["node_id"]: n for n in self._nodes_with_self(settings)}
        targets = []
        for raw in node_ids:
            node_id = str(raw if not isinstance(raw, dict) else raw.get("node_id") or "")
            node = known.get(node_id)
            if node is None:
                raise RuntimeError("节点不在集群列表: %s" % node_id)
            if node_id == self.identity.node_id():
                continue
            platform = _normalize_platform(node.get("platform"))
            arch = _normalize_arch(node.get("arch"))
            package = next(
                (
                    item
                    for item in reversed(packages)
                    if item.get("platform") == platform and item.get("arch") == arch
                ),
                None,
            )
            ip = str(node.get("ip") or "").strip()
            try:
                port = int(node.get("port") or 0)
            except (TypeError, ValueError):
                port = 0
            error = ""
            if not node.get("online"):
                error = "节点离线"
            elif package is None:
                error = "无匹配安装包（%s/%s）" % (platform, arch)
            elif not ip or not 1 <= port <= 65535:
                error = "节点地址无效"
            targets.append(
                {
                    "node_id": node_id,
                    "name": node.get("name") or node_id,
                    "url": "http://%s:%d" % (ip, port) if ip and port else "",
                    "package": package,
                    "error": error,
                }
            )
        if not targets:
            raise RuntimeError("没有可推送的节点")
        job = self.jobs.create("cluster_update", "节点更新·%d 台" % len(targets))
        job.echo = False
        self.jobs.start(job, lambda j: self._run_update_push(j, targets))
        return {
            "job_id": job.id,
            "total": len(targets),
            "nodes": [item["node_id"] for item in targets],
        }

    def _run_update_push(self, job, targets: list) -> dict:
        from jobs import Cancelled

        total = len(targets)
        results = []
        for index, target in enumerate(targets, 1):
            if job.cancelled:
                raise Cancelled()
            job.set_progress(index - 1, total, "节点更新")
            if target.get("error"):
                results.append(
                    {
                        "node_id": target["node_id"],
                        "name": target["name"],
                        "ok": False,
                        "error": target["error"],
                    }
                )
                job.log(
                    "跳过 %s：%s" % (target["name"], target["error"])
                )
                continue
            job.log(
                "推送 %s（%s）到 %s"
                % (
                    target["package"]["filename"],
                    target["package"].get("version") or "未标注版本",
                    target["name"],
                )
            )
            try:
                data = self._send_update_file(job, target)
                results.append(
                    {
                        "node_id": target["node_id"],
                        "name": target["name"],
                        "ok": True,
                        "package": target["package"]["filename"],
                        "path": data.get("path", ""),
                    }
                )
                job.log("已推送并落盘：%s" % target["name"])
            except Cancelled:
                raise
            except Exception as e:  # noqa: BLE001
                results.append(
                    {
                        "node_id": target["node_id"],
                        "name": target["name"],
                        "ok": False,
                        "error": str(e),
                    }
                )
                job.log("推送失败 %s：%s" % (target["name"], e))
        job.set_progress(total, total, "节点更新完成")
        ok = sum(1 for item in results if item.get("ok"))
        return {
            "total": total,
            "ok": ok,
            "failed": total - ok,
            "nodes": results,
        }

    def _send_update_file(self, job, target: dict) -> dict:
        package = target["package"]
        path = Path(str(package.get("path") or ""))
        if not path.is_file():
            raise RuntimeError("安装包不存在: %s" % path)
        size = path.stat().st_size
        settings = self.current_settings()
        common = {
            "node_id": self.identity.node_id(),
            "timeout": 60,
            "max_response": protocol.MAX_COMMAND_BODY,
        }
        request_json(
            target["url"],
            "/cluster/update/begin",
            {
                "update_id": package["sha256"],
                "filename": path.name,
                "size": size,
                "sha256": package["sha256"],
                "version": package.get("version", ""),
                "platform": package.get("platform", ""),
                "arch": package.get("arch", ""),
            },
            settings,
            **common,
        )
        offset = 0
        with open(path, "rb") as fp:
            while True:
                if job.cancelled:
                    from jobs import Cancelled

                    raise Cancelled()
                chunk = fp.read(UPDATE_CHUNK_SIZE)
                if not chunk:
                    break
                request_json(
                    target["url"],
                    "/cluster/update/chunk",
                    {
                        "update_id": package["sha256"],
                        "offset": offset,
                        "data_b64": base64.b64encode(chunk).decode("ascii"),
                    },
                    settings,
                    **common,
                )
                offset += len(chunk)
                job.log(
                    "  %s %d/%d 字节"
                    % (target["name"], offset, size)
                )
        return request_json(
            target["url"],
            "/cluster/update/commit",
            {"update_id": package["sha256"]},
            settings,
            **common,
        )

    def update_status(self, job_id: str) -> dict:
        job_id = str(job_id or "").strip()
        if not usb_protocol.ID_RE.match(job_id):
            raise RuntimeError("job_id 非法")
        job = self.jobs.get(job_id) if self.jobs else None
        if job is None:
            raise RuntimeError("节点更新任务不存在")
        try:
            lines, _ = job.logs_since(0)
            logs = [line for _, line in lines][-500:]
        except Exception:  # noqa: BLE001
            logs = []
        return {"job": job.to_dict(), "logs": logs}

    def cancel_update(self, job_id: str) -> dict:
        job_id = str(job_id or "").strip()
        if not usb_protocol.ID_RE.match(job_id):
            raise RuntimeError("job_id 非法")
        job = self.jobs.get(job_id) if self.jobs else None
        if job is None:
            raise RuntimeError("节点更新任务不存在")
        job.cancel()
        return {"job_id": job_id, "canceled": True}

    # ---------------- 测试辅助 ----------------

    def wait_worker_state(self, timeout: float = 3.0) -> dict:
        """等待 Worker 首次上报（单测用）。"""
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self._state_lock:
                if self._worker_state:
                    return dict(self._worker_state)
            time.sleep(0.05)
        return {}


cluster_service = ClusterService()


# ---------------------------------------------------------------- 本机管理接口


def _settings_and_status():
    settings = ClusterSettings.from_config(_config.load())
    status = _read_json(identity.cluster_dir() / SERVICE_FILE)
    if not isinstance(status, dict):
        status = {}
    return settings, status


def _admin_urls(settings: ClusterSettings, status: dict) -> list:
    port = int(status.get("listen_port") or settings.listen_port or DEFAULT_PORT)
    hosts = []
    for host in (
        "127.0.0.1",
        status.get("listen_host") or "",
        settings.listen_host or "",
        settings.advertise_host or "",
        "localhost",
    ):
        host = str(host or "").strip()
        if not host or host in ("0.0.0.0", "::"):
            continue
        if host not in hosts:
            hosts.append(host)
    return ["http://%s:%d" % (host, port) for host in hosts]


def _admin_request(
    base: str, path: str, body: bytes = None, timeout: float = 2.0
) -> dict:
    url = str(base or "").rstrip("/") + path
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST" if body is not None else "GET",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            payload = protocol.parse_json_body(
                resp.read(), max_bytes=protocol.MAX_COMMAND_BODY
            )
    except urllib.error.HTTPError as e:
        raise RuntimeError(_http_error_text(e))
    except (urllib.error.URLError, OSError) as e:
        raise RuntimeError(str(e))
    if not payload.get("ok"):
        raise RuntimeError(str(payload.get("error") or "管理请求失败"))
    data = payload.get("data")
    return data if isinstance(data, dict) else {}


def _fallback_state(settings: ClusterSettings, status: dict) -> dict:
    id_obj = identity.NodeIdentity()
    port = int(status.get("listen_port") or settings.listen_port or DEFAULT_PORT)
    self_node = _self_node(id_obj, settings, port=port)
    workers = _read_json(identity.cluster_dir() / PEERS_FILE)
    nodes = []
    pending = []
    if settings.role == "hub":
        registry = NodeRegistry(
            identity.cluster_dir() / "registry.json",
            offline_sec=settings.offline_sec,
            require_approval=settings.require_approval,
        )
        nodes = [
            n
            for n in registry.snapshot()
            if n.get("node_id") != self_node["node_id"]
        ]
        nodes.insert(0, self_node)
        pending = registry.pending()
    else:
        nodes = [n for n in (workers.get("nodes") or []) if isinstance(n, dict)]
    if settings.enabled and not status.get("running") and not status.get(
        "last_error"
    ):
        status["last_error"] = "后台服务未运行（请确认 usb-agent 已启动）"
    if not settings.enabled:
        status["last_error"] = ""
    status["disabled"] = not settings.enabled
    status["node_id"] = self_node["node_id"]
    status["secret_set"] = bool(settings.secret)
    status["started_at"] = status.get("started_at") or ""
    status["pending"] = len(pending)
    status["nodes"] = len([n for n in nodes if not n.get("self")])
    status["worker"] = workers
    return {
        "service": status,
        "identity": self_node,
        "config": settings.public(),
        "nodes": nodes,
        "pending": pending,
        "worker": workers,
    }


def local_status(cfg: dict = None) -> dict:
    """RPC 进程用的只读状态：优先本机管理接口，失败回退到状态文件。"""
    settings = ClusterSettings.from_config(cfg or _config.load())
    status = _read_json(identity.cluster_dir() / SERVICE_FILE)
    if not isinstance(status, dict):
        status = {}
    status.setdefault("running", False)
    status.setdefault("disabled", not settings.enabled)
    status.setdefault("role", settings.role)
    status.setdefault("cluster_id", settings.cluster_id)
    status.setdefault("listen_host", settings.listen_host)
    status.setdefault("listen_port", settings.listen_port)
    status.setdefault("last_error", "")
    if status.get("running"):
        last_error = ""
        for base in _admin_urls(settings, status):
            try:
                data = _admin_request(base, "/cluster/admin/state")
                service = data.get("service")
                if not isinstance(service, dict):
                    service = dict(status)
                    data["service"] = service
                service["running"] = True
                return data
            except Exception as e:  # noqa: BLE001
                last_error = str(e)
        status["running"] = False
        status["last_error"] = "后台服务无响应：%s" % (
            last_error or "连接失败"
        )
    return _fallback_state(settings, status)


def local_admin(action: str, node_id: str = "") -> dict:
    """RPC 进程调用本机管理接口审批/拒绝节点。"""
    settings, status = _settings_and_status()
    if not status.get("running"):
        raise RuntimeError(
            status.get("last_error") or "集群后台服务未运行（请确认 usb-agent 已启动）"
        )
    if action not in ("approve", "reject"):
        raise RuntimeError("未知操作: %s" % action)
    body = protocol.json_bytes({"node_id": str(node_id or "")})
    last_error = ""
    for base in _admin_urls(settings, status):
        try:
            return _admin_request(base, "/cluster/admin/" + action, body)
        except Exception as e:  # noqa: BLE001
            last_error = str(e)
    raise RuntimeError(last_error or "无法连接集群后台服务")


def local_export(action: str, params: dict = None) -> dict:
    """RPC 进程调用本机集群导出协调器（start/status/cancel）。"""
    settings, status = _settings_and_status()
    if not status.get("running"):
        raise RuntimeError(
            status.get("last_error") or "集群后台服务未运行（请确认 usb-agent 已启动）"
        )
    if action not in ("start", "status", "cancel"):
        raise RuntimeError("未知操作: %s" % action)
    body = protocol.json_bytes(params or {})
    last_error = ""
    for base in _admin_urls(settings, status):
        try:
            return _admin_request(
                base,
                "/cluster/admin/export/" + action,
                body,
                timeout=15.0 if action == "cancel" else 10.0,
            )
        except Exception as e:  # noqa: BLE001
            last_error = str(e)
    raise RuntimeError(last_error or "无法连接集群后台服务")


def local_update(action: str, params: dict = None) -> dict:
    """RPC 进程调用本机集群更新管理（packages/add_package/push/status/cancel/pending/open）。"""
    settings, status = _settings_and_status()
    if not status.get("running"):
        raise RuntimeError(
            status.get("last_error") or "集群后台服务未运行（请确认 usb-agent 已启动）"
        )
    if action not in (
        "packages",
        "add_package",
        "remove_package",
        "push",
        "status",
        "cancel",
        "pending",
        "open",
    ):
        raise RuntimeError("未知操作: %s" % action)
    path = {
        "packages": "/cluster/admin/update/packages",
        "add_package": "/cluster/admin/update/packages",
        "remove_package": "/cluster/admin/update/packages/remove",
        "push": "/cluster/admin/update/push",
        "status": "/cluster/admin/update/status",
        "cancel": "/cluster/admin/update/cancel",
        "pending": "/cluster/admin/update/pending",
        "open": "/cluster/admin/update/open",
    }[action]
    body = None if action in ("packages", "pending") else protocol.json_bytes(params or {})
    timeout = (
        120.0
        if action == "add_package"
        else 30.0
        if action == "push"
        else 15.0
        if action == "open"
        else 10.0
    )
    last_error = ""
    for base in _admin_urls(settings, status):
        try:
            return _admin_request(base, path, body, timeout=timeout)
        except Exception as e:  # noqa: BLE001
            last_error = str(e)
    raise RuntimeError(last_error or "无法连接集群后台服务")
