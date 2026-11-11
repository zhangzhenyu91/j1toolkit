# -*- coding: utf-8 -*-
"""运行状态目录（遵循 XDG；Windows 用 %LOCALAPPDATA%）。

存放：定时运行戳（stamps/）、任务历史（history/<app_id>.jsonl）、任务日志（logs/）。
现行定时器与 GUI 同为桌面用户，状态共享；UVMP_TOOLKIT_MIRROR_STATE 仅为旧 root 部署兼容保留。

安全约定：
- 状态目录 0700、写入的状态文件/日志 0600（不依赖 umask；含任务摘要与业务明细）
- JSON 落盘统一“唯一临时名 + fsync + 原子替换”，并发写不产生半截文件
- 清理（一键清日志/过期自洁）跳过运行中任务的日志与常驻 agent 日志
"""

import json
import os
import re
import sys
import tempfile
import threading
import time
from pathlib import Path

APP_DIR_NAME = "uvmp-toolkit"
# 常驻 usb-agent 日志：删掉后写句柄仍在旧 inode，表面看整天无日志，因此清理一律豁免
AGENT_LOG_PREFIX = "usb-agent.log"
# 任务日志多久没人认领就按“已结束”处理（进程崩溃残留的 .log 仍可被清掉）
ORPHAN_LOG_GRACE_SEC = 3600

_app_id_re = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$")
# 新任务号：YYYYmmddHHMMSS-<6 位十六进制随机>；旧任务号是 -NN 两位序号，均需可读可删
_job_id_re = re.compile(r"^\d{14}-(?:[0-9a-f]{6}|\d{2,3})$")

# 运行中任务的日志路径（本进程登记；跨进程靠“无档案 + 近期修改”兜底）
_active_logs = set()
_active_lock = threading.Lock()


def _ensure_dir(path: Path) -> Path:
    """创建目录并收紧到 0700（Windows 无 Unix 权限位时静默跳过）"""
    path.mkdir(parents=True, exist_ok=True)
    try:
        os.chmod(path, 0o700)
    except OSError:
        pass
    return path


def state_dir() -> Path:
    if sys.platform.startswith("win"):
        base = os.environ.get("LOCALAPPDATA") or str(Path.home() / "AppData" / "Local")
    else:
        base = os.environ.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share")
    return _ensure_dir(Path(base) / APP_DIR_NAME)


def _sub(name: str) -> Path:
    return _ensure_dir(state_dir() / name)


def logs_dir() -> Path:
    return _sub("logs")


def jobs_dir() -> Path:
    return _sub("jobs")


def restrict_file(path: Path):
    """状态文件/日志落 0600（含任务摘要与业务明细）"""
    try:
        os.chmod(path, 0o600)
    except OSError:
        pass


# ---------------------------------------------------------------- 运行中日志登记


def register_active_log(path) -> None:
    """登记运行中任务的日志文件，清理时跳过"""
    with _active_lock:
        _active_logs.add(str(Path(path)))


def unregister_active_log(path) -> None:
    with _active_lock:
        _active_logs.discard(str(Path(path)))


def _is_protected(f: Path) -> bool:
    """清理豁免判断：agent 日志、本进程登记中的日志、以及“没有任务档案且刚创建”
    的日志（另一进程正在写；崩溃残留超过宽限期后照常清理）。"""
    if f.name.startswith(AGENT_LOG_PREFIX):
        return True
    with _active_lock:
        if str(f) in _active_logs:
            return True
    if f.suffix != ".log" or not is_job_id(f.stem):
        return False  # 非任务日志（如 agent 兼容日志）不走“运行中”判断
    archive = jobs_dir() / (f.stem + ".json")
    if archive.exists():
        return False  # 已归档：任务确实结束
    try:
        return time.time() - f.stat().st_mtime < ORPHAN_LOG_GRACE_SEC
    except OSError:
        return False


# ---------------------------------------------------------------- 日志清理


def _iter_log_files():
    """logs/ 与 jobs/ 下的全部文件（任务日志 + 任务档案）"""
    for d in (logs_dir(), jobs_dir()):
        try:
            for f in d.iterdir():
                if f.is_file():
                    yield f
        except OSError:
            pass


def clear_logs() -> dict:
    """删除 logs/ 与 jobs/ 下全部文件，返回 {"removed": n, "skipped": m}。
    运行中任务的 .log 与常驻 usb-agent 日志一律跳过并计数（删掉后写句柄仍在旧 inode）。"""
    removed = skipped = 0
    for f in _iter_log_files():
        if _is_protected(f):
            skipped += 1
            continue
        try:
            f.unlink()
            removed += 1
        except OSError:
            skipped += 1
    return {"removed": removed, "skipped": skipped}


def prune_old_logs(days: int = 30) -> int:
    """按 mtime 删除 logs/、jobs/ 中超过 days 天的文件（启动自洁用）；
    运行中任务与 agent 常驻日志豁免，异常静默跳过，返回删除数"""
    cutoff = time.time() - days * 86400
    removed = 0
    for f in _iter_log_files():
        try:
            if f.stat().st_mtime >= cutoff:
                continue
            if _is_protected(f):
                continue
            f.unlink()
            removed += 1
        except OSError:
            pass
    return removed


def read_json(path: Path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return default


def write_json(path: Path, data):
    """唯一临时名 + fsync + 原子替换：并发写不互踩，断电不留半截文件，落盘 0600"""
    path.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(data, ensure_ascii=False, indent=1)
    fd, tmp_name = tempfile.mkstemp(
        prefix=".%s-" % path.name, suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fp:
            fp.write(text)
            fp.flush()
            os.fsync(fp.fileno())
        restrict_file(Path(tmp_name))
        os.replace(tmp_name, str(path))
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


# ---------------------------------------------------------------- 定时运行戳


def _mirror_dir():
    """镜像状态目录（环境变量 UVMP_TOOLKIT_MIRROR_STATE）。
    旧版 deb 的 systemd timer 曾以 root 跑 daily-export，状态在 /root/.local/share 下，
    桌面用户的 GUI 读不到。现行 timer 已改为桌面用户，状态直接共享；本镜像逻辑仅为旧部署兼容。"""
    m = os.environ.get("UVMP_TOOLKIT_MIRROR_STATE", "").strip()
    if not m:
        return None
    try:
        d = Path(m)
        d.mkdir(parents=True, exist_ok=True)
        return d
    except OSError:
        return None


def _mirror_chmod(path: Path):
    """root 镜像写入的文件默认 644，桌面用户后续追加/替换需要写权限"""
    try:
        os.chmod(path, 0o666)
    except OSError:
        pass


def get_stamp(name: str):
    """读取运行戳（dict 或 None）。约定字段：date=YYYY-MM-DD，time=ISO，其余任意"""
    return read_json(_sub("stamps") / (name + ".json"), None)


def set_stamp(name: str, data: dict):
    write_json(_sub("stamps") / (name + ".json"), data)
    md = _mirror_dir()
    if md:
        try:
            p = md / "stamps" / (name + ".json")
            write_json(p, data)
            _mirror_chmod(p)
        except OSError:
            pass


# ---------------------------------------------------------------- 应用运行历史（jsonl，每行一条）

# 历史文件读改写（粗修剪）必须串行，否则并发追加会互相吞记录
_history_lock = threading.Lock()


def _history_path(app_id: str) -> Path:
    if not _app_id_re.match(app_id or ""):
        raise ValueError("app_id 非法")
    return _sub("history") / (app_id + ".jsonl")


def append_history(app_id: str, record: dict):
    f = _history_path(app_id)
    record = dict(record)
    record.setdefault("time", time.strftime("%Y-%m-%d %H:%M:%S"))
    with _history_lock:
        with open(f, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(record, ensure_ascii=False) + "\n")
        restrict_file(f)
        # 粗修剪：超过 300 行时只留最近 200 行
        try:
            lines = f.read_text(encoding="utf-8").splitlines()
            if len(lines) > 300:
                _write_history_lines(f, lines[-200:])
        except OSError:
            pass
    md = _mirror_dir()  # 旧 root 部署兼容镜像（现行桌面用户运行不需要）
    if md:
        try:
            mf = md / "history" / (app_id + ".jsonl")
            mf.parent.mkdir(parents=True, exist_ok=True)
            with open(mf, "a", encoding="utf-8") as fp:
                fp.write(json.dumps(record, ensure_ascii=False) + "\n")
            _mirror_chmod(mf)
        except OSError:
            pass


def _write_history_lines(path: Path, lines) -> None:
    """历史文件整体重写（修剪用）：唯一临时名 + 原子替换，避免读方看到半截"""
    fd, tmp_name = tempfile.mkstemp(
        prefix=".%s-" % path.name, suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fp:
            fp.write("\n".join(lines) + "\n")
            fp.flush()
            os.fsync(fp.fileno())
        restrict_file(Path(tmp_name))
        os.replace(tmp_name, str(path))
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def recent_runs(app_id: str, n: int = 10) -> list:
    f = _history_path(app_id)  # 非法 app_id 抛 ValueError（RPC 层先行校验）
    if not f.exists():
        return []
    out = []
    try:
        lines = f.read_text(encoding="utf-8").splitlines()
    except OSError:
        return []
    for line in lines[::-1]:
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
        if len(out) >= n:
            break
    return out


def last_run(app_id: str):
    runs = recent_runs(app_id, 1)
    return runs[0] if runs else None


# ---------------------------------------------------------------- 运行历史删除

_APP_ID_RE = _app_id_re  # 旧名保留（集群/测试模块可能引用）
_JOB_ID_RE = _job_id_re


def is_job_id(value) -> bool:
    """任务号合法性：新格式 14 位时间 + 随机后缀，旧格式 -NN 序号均视为合法。
    统一入口，RPC/历史删除/日志读取都走它，避免各处正则漂移。"""
    return bool(_job_id_re.match(str(value or "")))


def is_app_id(value) -> bool:
    """应用标识合法性（历史文件名 = <app_id>.jsonl，必须防路径穿越）"""
    return bool(_app_id_re.match(str(value or "")))


def delete_history_records(app_id: str, job_ids=None) -> dict:
    """删除应用运行历史记录，连带对应任务日志（logs/）与任务档案（jobs/）。
    job_ids=None 清空该应用全部记录；否则只删指定 job_id 的记录。
    运行中任务被占用的文件跳过并计数（Windows 删不掉）。
    返回 {"records": 删的记录数, "files": 删的文件数, "skipped": 占用跳过数}"""
    f = _history_path(app_id)
    want = None if job_ids is None else {j for j in job_ids if is_job_id(j)}
    records = 0
    doomed = set()
    if f.exists():
        kept = []
        with _history_lock:
            try:
                lines = f.read_text(encoding="utf-8").splitlines()
            except OSError:
                lines = []
            for line in lines:
                line = line.strip()
                if not line:
                    continue
                jid = ""
                try:
                    jid = str(json.loads(line).get("job_id") or "")
                except ValueError:
                    pass
                if want is None or jid in want:
                    records += 1
                    if is_job_id(jid):
                        doomed.add(jid)
                else:
                    kept.append(line)
            try:
                if kept:
                    _write_history_lines(f, kept)
                else:
                    f.unlink()
            except OSError:
                pass
    files = skipped = 0
    for jid in doomed:
        for p in (logs_dir() / (jid + ".log"), jobs_dir() / (jid + ".json")):
            try:
                p.unlink()
                files += 1
            except FileNotFoundError:
                pass
            except OSError:
                skipped += 1
    return {"records": records, "files": files, "skipped": skipped}
