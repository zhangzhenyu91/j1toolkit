# -*- coding: utf-8 -*-
"""U 盘指令协议：服务端 ↔ 被控机 usb-agent 的唯一契约。

盘根文件（全部 UTF-8 JSON，只允许落在盘根）：
  {app}_cmd.json                 服务端下发指令（每应用一个，服务端串行下发）
  {app}_result_<id>.json         客户端执行结果（按指令 id 分文件，避免覆盖旧轮次）
  usb_agent_status.json          agent 状态心跳（状态变化 + 每 30 分钟）
  .{app}_cmd.<id>.running        客户端认领中的隐藏文件（服务端忽略）

约定：
- 写入一律“同目录临时文件 + fsync + 原子替换”，避免服务端读到半截 JSON；
- 客户端先写结果、再删指令；崩在中间时下次挂载可恢复；
- 指令 id 全局唯一，结果按 id 关联，禁止用时间戳猜轮次；
- 兼容旧契约：tree_sync/crossing_sync 的 {version,cycle_id,kind,file,issued_at}
  会被归一成 id=cycle_id、action=kind、params.file=file。
"""

import json
import os
import re
import socket
from datetime import datetime, timedelta, timezone
from pathlib import Path

CST = timezone(timedelta(hours=8))

CMD_NAME_TMPL = "%s_cmd.json"
RESULT_NAME_TMPL = "%s_result_%s.json"
STATUS_NAME = "usb_agent_status.json"
RUNNING_PREFIX = "."

APP_RE = re.compile(r"^[a-z][a-z0-9_]{1,31}$")
ACTION_RE = re.compile(r"^[a-z][a-z0-9_]{0,31}$")
ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
RUNNING_RE = re.compile(
    r"^\.(?P<app>[a-z][a-z0-9_]{1,31})_cmd\.(?P<id>[A-Za-z0-9._:-]{1,128})\.running$"
)


class ProtocolError(ValueError):
    """指令/结果不符合协议（调用方负责写失败结果并删除指令）。"""


def now_iso() -> str:
    """当前北京时间 ISO 串（秒精度，带 +08:00）。"""
    return datetime.now(CST).isoformat(timespec="seconds")


def parse_iso(value) -> datetime | None:
    """解析 ISO 时间；无时区按北京时间处理；非法返回 None。"""
    if not value:
        return None
    text = str(value).strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(text)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=CST)
    return dt


def sanitize_id(cmd_id: str) -> str:
    """文件名安全化：只保留字母数字与 . _ -，长度钳到 128。"""
    out = re.sub(r"[^A-Za-z0-9._-]", "_", str(cmd_id or ""))
    return out[:128] or "unknown"


def cmd_name(app: str) -> str:
    return CMD_NAME_TMPL % app


def result_name(app: str, cmd_id: str) -> str:
    return RESULT_NAME_TMPL % (app, sanitize_id(cmd_id))


def running_name(app: str, cmd_id: str) -> str:
    return "%s%s_cmd.%s.running" % (RUNNING_PREFIX, app, sanitize_id(cmd_id))


def parse_running_name(name: str):
    """解析认领中的隐藏文件名，返回 (app, id) 或 None。"""
    m = RUNNING_RE.match(str(name or ""))
    if not m:
        return None
    return m.group("app"), m.group("id")


def normalize_command(raw, app_hint: str = "") -> dict:
    """校验并归一指令；非法抛 ProtocolError。兼容旧 cycle_id/kind/file 字段。"""
    if not isinstance(raw, dict):
        raise ProtocolError("指令不是 JSON 对象")
    try:
        version = int(raw.get("version", 1))
    except (TypeError, ValueError):
        raise ProtocolError("version 非法")
    if version != 1:
        raise ProtocolError("不支持的协议版本: %s" % version)

    app = str(raw.get("app") or app_hint or "").strip()
    if not APP_RE.match(app):
        raise ProtocolError("app 非法: %r" % app)

    action = str(raw.get("action") or raw.get("kind") or "").strip()
    if not ACTION_RE.match(action):
        raise ProtocolError("action 非法: %r" % action)

    cmd_id = str(raw.get("id") or raw.get("cycle_id") or "").strip()
    if not ID_RE.match(cmd_id):
        raise ProtocolError("id 非法或缺失: %r" % cmd_id)

    params = raw.get("params")
    if params is None:
        params = {}
    if not isinstance(params, dict):
        raise ProtocolError("params 必须是对象")
    params = dict(params)
    if raw.get("file") and "file" not in params:
        params["file"] = raw.get("file")

    issued_at = str(raw.get("issued_at") or "").strip()
    expires_at = str(raw.get("expires_at") or "").strip()
    if issued_at and parse_iso(issued_at) is None:
        raise ProtocolError("issued_at 非法: %r" % issued_at)
    if expires_at and parse_iso(expires_at) is None:
        raise ProtocolError("expires_at 非法: %r" % expires_at)

    return {
        "version": 1,
        "id": cmd_id,
        "app": app,
        "action": action,
        "params": params,
        "issued_at": issued_at,
        "expires_at": expires_at,
        "legacy": not bool(raw.get("action") or raw.get("app")),
    }


def is_expired(cmd: dict, now: datetime | None = None) -> bool:
    expires = parse_iso(cmd.get("expires_at"))
    if expires is None:
        return False
    current = now or datetime.now(CST)
    if current.tzinfo is None:
        current = current.replace(tzinfo=CST)
    return current > expires


def client_info(version: str = "") -> dict:
    """结果里的被控机标识（排障用，不含敏感信息）。"""
    try:
        import getpass
        import platform

        user = getpass.getuser()
        host = platform.node() or socket.gethostname()
    except Exception:  # noqa: BLE001
        user = host = ""
    return {"host": host, "user": user, "version": version}


def build_result(
    cmd: dict,
    ok: bool,
    error: str = "",
    data=None,
    started_at: str = "",
    finished_at: str = "",
    duration_ms: int = 0,
    version: str = "",
) -> dict:
    return {
        "version": 1,
        "id": cmd.get("id", ""),
        "app": cmd.get("app", ""),
        "action": cmd.get("action", ""),
        "ok": bool(ok),
        "started_at": started_at or now_iso(),
        "finished_at": finished_at or now_iso(),
        "duration_ms": int(duration_ms or 0),
        "error": str(error or ""),
        "data": data if isinstance(data, dict) else {},
        "client": client_info(version),
    }


def read_json(path, max_bytes: int = 65536):
    """读取小 JSON 文件；超限/损坏抛 ProtocolError，IO 错误（EIO/EACCES/断盘）原样抛 OSError。

    区分两者是必要的：U 盘被 root 挂载（EACCES）或残留挂载（EIO）时，指令本身没坏，
    调用方必须重试，不能按“指令损坏”写失败结果并删除指令。
    """
    p = Path(path)
    size = p.stat().st_size  # OSError 原样上抛
    if size > max_bytes:
        raise ProtocolError("文件超过 %d 字节" % max_bytes)
    try:
        text = p.read_text(encoding="utf-8")  # OSError 原样上抛
    except UnicodeDecodeError as e:
        raise ProtocolError("文件不是 UTF-8 文本: %s" % e)
    try:
        return json.loads(text)
    except ValueError as e:
        raise ProtocolError("JSON 解析失败: %s" % e)


def write_json_atomic(path, data, fsync: bool = True):
    """同目录临时文件 + fsync + 原子替换；Windows 目标占用时短重试。"""
    p = Path(path)
    payload = json.dumps(data, ensure_ascii=False, indent=1).encode("utf-8")
    tmp = p.with_name("." + p.name + ".tmp")
    fd = os.open(str(tmp), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
    try:
        os.write(fd, payload)
        if fsync:
            os.fsync(fd)
    finally:
        os.close(fd)
    last = None
    for attempt in range(5):
        try:
            os.replace(str(tmp), str(p))
            last = None
            break
        except OSError as e:  # Windows 杀软/索引短暂占用
            last = e
            if attempt < 4:
                import time

                time.sleep(0.2 * (attempt + 1))
    if last is not None:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise last
    if fsync:
        try:
            dfd = os.open(str(p.parent), os.O_RDONLY)
            try:
                os.fsync(dfd)
            finally:
                os.close(dfd)
        except OSError:
            pass


def write_status(root, status: dict):
    """写盘根状态心跳（失败由调用方记录，不影响指令执行）。"""
    write_json_atomic(Path(root) / STATUS_NAME, status)


def read_status(root):
    try:
        return read_json(Path(root) / STATUS_NAME, max_bytes=16384)
    except (OSError, ProtocolError):
        return None
