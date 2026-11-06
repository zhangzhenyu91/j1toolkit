# -*- coding: utf-8 -*-
"""集群节点协议：HMAC 请求签名、节点信息校验、JSON 信封。

节点之间只走 HTTP + JSON，不引入 WebSocket / TLS 依赖。每个请求带
X-UVMP-Cluster / X-UVMP-Node / X-UVMP-Timestamp / X-UVMP-Nonce /
X-UVMP-Signature；签名覆盖 method、path、时间戳、nonce 与 body 摘要，
服务端拒绝超过 60 秒的时间偏差与 120 秒内重复的 nonce。
"""

import hashlib
import hmac
import json
import re
import secrets
import threading
import time

PROTOCOL_VERSION = 1
MAX_BODY = 1024 * 1024
MAX_COMMAND_BODY = 48 * 1024 * 1024
CLOCK_SKEW_SEC = 60
NONCE_TTL_SEC = 120

HEADER_CLUSTER = "X-UVMP-Cluster"
HEADER_NODE = "X-UVMP-Node"
HEADER_TIMESTAMP = "X-UVMP-Timestamp"
HEADER_NONCE = "X-UVMP-Nonce"
HEADER_SIGNATURE = "X-UVMP-Signature"

CLUSTER_ID_RE = re.compile(r"^[A-Za-z0-9._:-]{1,64}$")
NODE_ID_RE = re.compile(r"^[A-Za-z0-9._:-]{1,64}$")
NONCE_RE = re.compile(r"^[A-Za-z0-9_-]{8,64}$")
APP_RE = re.compile(r"^[a-z][a-z0-9_]{1,31}$")
ROLE_VALUES = ("hub", "worker")


class ClusterProtocolError(ValueError):
    """请求不符合协议（默认 400）。"""

    status = 400


class ClusterAuthError(ClusterProtocolError):
    """签名/集群标识/时间戳/nonce 校验失败（401）。"""

    status = 401


class ClusterForbiddenError(ClusterProtocolError):
    """节点尚未审批或无权访问（403）。"""

    status = 403


class ClusterNotFoundError(ClusterProtocolError):
    """未知端点（404）。"""

    status = 404


def _sha256_hex(body: bytes) -> str:
    return hashlib.sha256(body or b"").hexdigest()


def canonical_request(
    method: str, path: str, timestamp: str, nonce: str, body: bytes
) -> bytes:
    """签名原文；服务端与客户端必须逐字一致。"""
    parts = [
        "UVMP-CLUSTER-1",
        (method or "").upper(),
        str(path or ""),
        str(timestamp or ""),
        str(nonce or ""),
        _sha256_hex(body),
    ]
    return "\n".join(parts).encode("utf-8")


def sign_request(
    secret: str,
    method: str,
    path: str,
    body: bytes,
    timestamp: str,
    nonce: str,
) -> str:
    if not secret:
        raise ClusterProtocolError("集群密钥未配置")
    mac = hmac.new(
        secret.encode("utf-8"),
        canonical_request(method, path, timestamp, nonce, body),
        hashlib.sha256,
    )
    return mac.hexdigest()


def build_headers(
    secret: str,
    cluster_id: str,
    node_id: str,
    method: str,
    path: str,
    body: bytes,
    timestamp: float = None,
    nonce: str = None,
) -> dict:
    """构造带签名的请求头；测试可注入 timestamp/nonce。"""
    ts = str(int(timestamp if timestamp is not None else time.time()))
    value = nonce or secrets.token_hex(16)
    return {
        "Content-Type": "application/json; charset=utf-8",
        "Accept": "application/json",
        HEADER_CLUSTER: cluster_id,
        HEADER_NODE: node_id,
        HEADER_TIMESTAMP: ts,
        HEADER_NONCE: value,
        HEADER_SIGNATURE: sign_request(secret, method, path, body, ts, value),
    }


class NonceCache:
    """进程内 nonce 去重；按 node+nonce 记录，过期自动清理。"""

    def __init__(self, ttl: int = NONCE_TTL_SEC):
        self.ttl = max(1, int(ttl))
        self._lock = threading.Lock()
        self._seen = {}

    def check_and_add(self, key: str, now: float = None) -> bool:
        current = time.time() if now is None else float(now)
        with self._lock:
            if self._seen:
                expired = [
                    item for item, ts in self._seen.items() if current - ts > self.ttl
                ]
                for item in expired:
                    self._seen.pop(item, None)
            if key in self._seen:
                return False
            self._seen[key] = current
            return True


def verify_headers(
    secret: str,
    cluster_id: str,
    method: str,
    path: str,
    headers,
    body: bytes,
    nonce_cache: NonceCache,
    now: float = None,
) -> str:
    """校验签名并返回请求方 node_id；失败抛 ClusterProtocolError 子类。"""
    if not secret:
        raise ClusterAuthError("集群密钥未配置")
    if not cluster_id:
        raise ClusterAuthError("集群标识未配置")

    got_cluster = str(headers.get(HEADER_CLUSTER) or "").strip()
    node_id = str(headers.get(HEADER_NODE) or "").strip()
    ts_raw = str(headers.get(HEADER_TIMESTAMP) or "").strip()
    nonce = str(headers.get(HEADER_NONCE) or "").strip()
    signature = str(headers.get(HEADER_SIGNATURE) or "").strip()

    if got_cluster != cluster_id:
        raise ClusterAuthError("集群标识不匹配")
    if not NODE_ID_RE.match(node_id):
        raise ClusterAuthError("节点标识非法")
    if not NONCE_RE.match(nonce):
        raise ClusterAuthError("nonce 非法")
    if not re.match(r"^[0-9a-f]{64}$", signature):
        raise ClusterAuthError("签名格式非法")
    try:
        ts = float(ts_raw)
    except (TypeError, ValueError):
        raise ClusterAuthError("时间戳非法")
    current = time.time() if now is None else float(now)
    if abs(current - ts) > CLOCK_SKEW_SEC:
        raise ClusterAuthError("时间戳偏差过大（请检查节点时钟同步）")

    expected = sign_request(secret, method, path, body, ts_raw, nonce)
    if not hmac.compare_digest(expected, signature):
        raise ClusterAuthError("签名校验失败")
    if not nonce_cache.check_and_add("%s:%s" % (node_id, nonce), current):
        raise ClusterAuthError("请求重放（nonce 已使用）")
    return node_id


def parse_json_body(raw: bytes, max_bytes: int = MAX_BODY) -> dict:
    """解析请求 JSON；空 body 归一为 {}。"""
    if not raw:
        return {}
    if len(raw) > max_bytes:
        raise ClusterProtocolError("请求体超过 %d 字节" % max_bytes)
    try:
        data = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as e:
        raise ClusterProtocolError("请求 JSON 解析失败: %s" % e)
    if not isinstance(data, dict):
        raise ClusterProtocolError("请求体必须是 JSON 对象")
    return data


def json_bytes(data) -> bytes:
    return json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _text(value, field: str, limit: int, default: str = "") -> str:
    text = str(value if value is not None else default).strip()
    if len(text) > limit:
        raise ClusterProtocolError("%s 超过 %d 字符" % (field, limit))
    return text


def validate_node_info(raw) -> dict:
    """校验并归一节点自报信息；不接受额外字段。"""
    if not isinstance(raw, dict):
        raise ClusterProtocolError("node 必须是对象")
    node_id = _text(raw.get("node_id"), "node_id", 64)
    if not NODE_ID_RE.match(node_id):
        raise ClusterProtocolError("node_id 非法")
    role = _text(raw.get("role"), "role", 16)
    if role not in ROLE_VALUES:
        raise ClusterProtocolError("role 必须是 hub 或 worker")
    try:
        team_id = int(raw.get("team_id") or 0)
        port = int(raw.get("port") or 0)
        protocol = int(raw.get("protocol") or PROTOCOL_VERSION)
    except (TypeError, ValueError):
        raise ClusterProtocolError("team_id/port/protocol 必须是整数")
    if team_id < 0:
        raise ClusterProtocolError("team_id 不能为负")
    if not 1 <= port <= 65535:
        raise ClusterProtocolError("port 非法")
    if protocol != PROTOCOL_VERSION:
        raise ClusterProtocolError(
            "协议版本不兼容（本端 %d，对端 %d）" % (PROTOCOL_VERSION, protocol)
        )
    export_ready = {}
    raw_ready = raw.get("export_ready")
    if isinstance(raw_ready, dict):
        try:
            q = max(1, min(16, int(raw_ready.get("query_workers") or 8)))
            r = max(1, min(16, int(raw_ready.get("render_workers") or 4)))
        except (TypeError, ValueError):
            q, r = 8, 4
        export_ready = {
            "ready": bool(raw_ready.get("ready")),
            "reason": _text(raw_ready.get("reason"), "export_ready.reason", 200),
            "query_workers": q,
            "render_workers": r,
        }
    tables = {}
    raw_tables = raw.get("sync_tables")
    if isinstance(raw_tables, dict):
        for app, item in list(raw_tables.items())[:16]:
            if not isinstance(app, str) or not APP_RE.match(app):
                continue
            if not isinstance(item, dict):
                continue
            try:
                table_team = int(item.get("team_id") or 0)
            except (TypeError, ValueError):
                continue
            tables[app] = {
                "team_id": max(0, table_team),
                "configured": bool(item.get("configured")),
                "exists": bool(item.get("exists")),
            }
    return {
        "node_id": node_id,
        "name": _text(raw.get("name"), "name", 64) or node_id,
        "role": role,
        "team_id": team_id,
        "team_name": _text(raw.get("team_name"), "team_name", 64),
        "platform": _text(raw.get("platform"), "platform", 32),
        "arch": _text(raw.get("arch"), "arch", 32),
        "version": _text(raw.get("version"), "version", 64),
        "protocol": protocol,
        "port": port,
        "sync_tables": tables,
        "export_ready": export_ready,
    }
