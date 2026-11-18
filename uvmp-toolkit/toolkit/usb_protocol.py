# -*- coding: utf-8 -*-
"""U 盘指令协议：服务端 ↔ 被控机 usb-agent 的唯一契约。

盘根文件（全部 UTF-8 JSON，只允许落在盘根）：
  {app}_cmd.json                 服务端下发指令（每应用一个，服务端串行下发）
  {app}_result_<id>.json         客户端执行结果（按指令 id 分文件，避免覆盖旧轮次）
  usb_agent_status.json          agent 状态心跳（状态变化 + 每 30 分钟）
  .{app}_cmd.<id>.running        客户端认领中的隐藏文件（服务端忽略）
  {app}_cmd.invalid.json         损坏指令的留存副本（每应用最新一份，不参与指令扫描）

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
import stat
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

CST = timezone(timedelta(hours=8))

CMD_NAME_TMPL = "%s_cmd.json"
CMD_INVALID_TMPL = "%s_cmd.invalid.json"
RESULT_NAME_TMPL = "%s_result_%s.json"
STATUS_NAME = "usb_agent_status.json"
RUNNING_PREFIX = "."
# 指令/留存原文的字节上限：损坏指令只在这个范围内读取与复制。
CMD_MAX_BYTES = 65536
# 半截 JSON 的读取重试：服务端写盘虽为原子替换，但个别共享/杀软会暴露中间态。
JSON_READ_RETRY = 1
JSON_READ_DELAY = 0.2

APP_RE = re.compile(r"^[a-z][a-z0-9_]{1,31}$")
ACTION_RE = re.compile(r"^[a-z][a-z0-9_]{0,31}$")
ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
NODE_ID_RE = re.compile(r"^[A-Za-z0-9._:-]{1,64}$")
CLUSTER_ID_RE = re.compile(r"^[A-Za-z0-9._:-]{1,64}$")
RUNNING_RE = re.compile(
    r"^\.(?P<app>[a-z][a-z0-9_]{1,31})_cmd\.(?P<id>[A-Za-z0-9._:-]{1,128})\.running$"
)
RESULT_RE = re.compile(
    r"^(?P<app>[a-z][a-z0-9_]{1,31})_result_(?P<id>[A-Za-z0-9._:-]{1,128})\.json$"
)
# 结果里可能带本地路径的字段（写盘前收敛成文件名）与只在本地有意义的字段（整条丢弃）。
PATH_KEYS = ("file", "src_deleted")
LOCAL_ONLY_KEYS = ("dir",)


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


def invalid_name(app: str) -> str:
    """损坏指令的留存文件名：与指令命名不重合，因此不会被下一轮指令扫描。"""
    return CMD_INVALID_TMPL % app


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


def parse_result_name(name: str):
    """解析结果文件名，返回 (app, id) 或 None（用于限定清理范围）。"""
    m = RESULT_RE.match(str(name or ""))
    if not m:
        return None
    return m.group("app"), m.group("id")


def inside_root(path, root) -> bool:
    """path 解析后是否落在 root 内（防路径穿越/符号链接逃逸）。"""
    try:
        root_s = str(Path(root).resolve())
        target_s = str(Path(path).resolve())
    except (OSError, ValueError):
        return False
    if target_s == root_s:
        return True
    return target_s.startswith(root_s.rstrip(os.sep) + os.sep)


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

    target_raw = raw.get("target")
    target_node = ""
    if isinstance(target_raw, dict):
        target_node = str(target_raw.get("node_id") or "").strip()
    elif isinstance(target_raw, str):
        target_node = target_raw.strip()
    if target_node and not NODE_ID_RE.match(target_node):
        raise ProtocolError("target.node_id 非法: %r" % target_node)
    cluster_id = str(raw.get("cluster") or "").strip()
    if cluster_id and not CLUSTER_ID_RE.match(cluster_id):
        raise ProtocolError("cluster 非法: %r" % cluster_id)

    return {
        "version": 1,
        "id": cmd_id,
        "app": app,
        "action": action,
        "params": params,
        "issued_at": issued_at,
        "expires_at": expires_at,
        "target": {"node_id": target_node} if target_node else {},
        "cluster": cluster_id,
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
    node: str = "",
    relayed_by: str = "",
) -> dict:
    result = {
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
    target = str((cmd.get("target") or {}).get("node_id") or "")
    if target:
        result["target"] = target
    if node:
        result["node"] = str(node)
    if relayed_by:
        result["relayed_by"] = str(relayed_by)
    return result


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


def recover_id(raw) -> str:
    """从损坏/半截指令原文里尽最大努力恢复合法 id，恢复不出返回空串。

    服务端只按原 id 等结果：回执若落到 invalid-<epoch>，服务端 Await 匹配不到，
    用户看到的是“超时”而不是“指令格式非法”。因此这里按原文扫描顶层 id/cycle_id
    （字段顺序、冒号两侧空白都不敏感），再用与 normalize_command 相同的 ID_RE
    与 128 长度约束把关。

    只认 depth=1 的键：params.id 等嵌套同名字段不是指令 id，误取会让回执落到
    无关文件上。跟踪字符串/转义状态后花括号不会误计深度，因此截断处之前的顶层
    键仍能读到（截断发生在 params 内部时，其后的键本就不是顶层键）。
    """
    if isinstance(raw, (bytes, bytearray)):
        raw = bytes(raw).decode("utf-8", "replace")
    if not isinstance(raw, str) or not raw:
        return ""
    found = {}
    for key, value in _top_level_string_fields(raw):
        if key not in found and ID_RE.match(value):
            found[key] = value
    return found.get("id") or found.get("cycle_id") or ""


def _top_level_string_fields(text: str):
    """按出现顺序产出 depth=1 的「键: "字符串值"」对（容忍截断与转义）。"""
    depth = 0
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == '"':
            # 读完整字符串（含转义），拿到 key 或值。
            j = i + 1
            buf = []
            closed = False
            while j < n:
                c = text[j]
                if c == "\\":
                    if j + 1 < n:
                        buf.append(text[j : j + 2])
                        j += 2
                        continue
                    j += 1
                    break
                if c == '"':
                    closed = True
                    j += 1
                    break
                buf.append(c)
                j += 1
            literal = "".join(buf)
            if closed and depth == 1:
                k = j
                while k < n and text[k] in " \t\r\n":
                    k += 1
                if k < n and text[k] == ":":
                    k += 1
                    while k < n and text[k] in " \t\r\n":
                        k += 1
                    if k < n and text[k] == '"':
                        value, _end = _scan_string(text, k)
                        if value is not None:
                            yield literal, value
                        i = k
                        continue
            i = j
            continue
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth = max(0, depth - 1)
        i += 1


def _scan_string(text: str, start: int):
    """从 start 处的引号读一个 JSON 字符串，返回 (值或 None, 结束位置)。"""
    j = start + 1
    n = len(text)
    buf = []
    while j < n:
        c = text[j]
        if c == "\\":
            if j + 1 < n:
                buf.append(text[j : j + 2])
                j += 2
                continue
            return None, n
        if c == '"':
            return "".join(buf), j + 1
        buf.append(c)
        j += 1
    return None, n


def read_regular_bytes(path, max_bytes: int = CMD_MAX_BYTES) -> bytes:
    """读普通文件的原始字节；超限/非常规文件抛 ProtocolError，IO 错误原样抛 OSError。

    损坏指令的原文会被复制到盘根留存，所以读取前必须用 lstat 确认是普通文件：
    符号链接（可能指向本机任意文件）、目录、FIFO/设备节点一律拒绝，绝不读目标、
    不复制、不删除。
    """
    p = Path(path)
    st = p.lstat()  # OSError 原样上抛
    if not stat.S_ISREG(st.st_mode):
        raise ProtocolError("不是普通文件: %s" % p.name)
    if st.st_size > max_bytes:
        raise ProtocolError("文件超过 %d 字节" % max_bytes)
    return p.read_bytes()  # OSError 原样上抛


def read_json_stable(path, max_bytes: int = 65536, retries: int = JSON_READ_RETRY, delay=None):
    """读指令 JSON，容忍“服务端正在写”的半截内容。

    返回 (data, stable)：
    - 读到合法 JSON → (data, True)
    - 解析失败且重试期间文件内容仍在变化 → (None, False)，调用方保留指令下轮重试；
    - 解析失败但内容已稳定 → 抛 ProtocolError（真损坏，调用方写失败结果）。
    IO 错误（EACCES/EIO/断盘）一律原样上抛，不当成损坏。
    """
    p = Path(path)
    last = None
    prev_sig = None
    for attempt in range(max(0, retries) + 1):
        try:
            return read_json(p, max_bytes=max_bytes), True
        except ProtocolError as e:
            last = e
            st = p.stat()  # OSError 原样上抛
            sig = (st.st_size, st.st_mtime_ns)
            if prev_sig is not None and sig != prev_sig:
                return None, False
            prev_sig = sig
            if attempt < retries:
                wait = JSON_READ_DELAY if delay is None else delay
                if wait:
                    time.sleep(wait)
    raise last


def write_json_atomic(path, data, fsync: bool = True):
    """同目录临时文件 + fsync + 原子替换；Windows 目标占用时短重试。"""
    payload = json.dumps(data, ensure_ascii=False, indent=1).encode("utf-8")
    write_bytes_atomic(path, payload, fsync=fsync)


def write_bytes_atomic(path, payload: bytes, fsync: bool = True):
    """原子写原始字节（与 write_json_atomic 同一套临时文件/替换语义）。"""
    p = Path(path)
    tmp = p.with_name("." + p.name + ".tmp")
    # Windows 的 os.open 默认文本模式，会把 \n 改写成 \r\n：留存证据必须逐字节一致。
    fd = os.open(
        str(tmp),
        os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_BINARY", 0),
        0o644,
    )
    try:
        written = os.write(fd, payload)
        if written != len(payload):
            raise OSError("写入不完整: %d/%d 字节" % (written, len(payload)))
        if fsync:
            os.fsync(fd)
    except OSError:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise
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


def _minimize_path_value(value):
    """绝对路径收敛成文件名；相对/纯文件名原样返回。"""
    if not isinstance(value, str) or not value.strip():
        return value
    try:
        p = Path(value)
        if p.is_absolute() or ("/" in value or "\\" in value):
            return p.name or value
    except (OSError, ValueError):
        return value
    return value


def minimize_result(data):
    """结果写盘前收敛本地路径：绝对路径只留文件名，纯本地用途字段整条去掉。

    服务端只按 id/app/ok/error 使用结果，桌面端读的是任务历史而非 U 盘文件，
    因此盘根结果无需暴露被控机的目录结构（内网共享路径/家目录/挂载点）。
    应用后端仍返回绝对路径：集群远端执行要靠它从 staging 收集产物，
    收敛只发生在写 U 盘这一步（见 usb_agent.UsbAgent._finish_result 调用处）。
    """
    if not isinstance(data, dict):
        return data
    out = {}
    for key, value in data.items():
        if key in LOCAL_ONLY_KEYS:
            continue
        if key in PATH_KEYS:
            out[key] = _minimize_path_value(value)
            continue
        out[key] = value
    return out


def write_status(root, status: dict):
    """写盘根状态心跳（失败由调用方记录，不影响指令执行）。"""
    write_json_atomic(Path(root) / STATUS_NAME, status)


def read_status(root):
    try:
        return read_json(Path(root) / STATUS_NAME, max_bytes=16384)
    except (OSError, ProtocolError):
        return None
