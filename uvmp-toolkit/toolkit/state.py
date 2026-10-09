# -*- coding: utf-8 -*-
"""运行状态目录（遵循 XDG；Windows 用 %LOCALAPPDATA%）。

存放：定时运行戳（stamps/）、任务历史（history/<app_id>.jsonl）、任务日志（logs/）。
root 定时器与桌面用户的 HOME 不同，状态天然分离：
GUI 里手动跑一次不会抑制 root 定时器当日的 due-check 判定。
"""
import json
import os
import sys
import time
from pathlib import Path

APP_DIR_NAME = "uvmp-toolkit"


def state_dir() -> Path:
    if sys.platform.startswith("win"):
        base = os.environ.get("LOCALAPPDATA") or str(Path.home() / "AppData" / "Local")
    else:
        base = os.environ.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share")
    d = Path(base) / APP_DIR_NAME
    d.mkdir(parents=True, exist_ok=True)
    return d


def _sub(name: str) -> Path:
    d = state_dir() / name
    d.mkdir(parents=True, exist_ok=True)
    return d


def logs_dir() -> Path:
    return _sub("logs")


def jobs_dir() -> Path:
    return _sub("jobs")


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
    """删除 logs/ 与 jobs/ 下全部文件；运行中任务的 .log 被占用
    （Windows 删不掉）时跳过并计数，返回 {"removed": n, "skipped": m}"""
    removed = skipped = 0
    for f in _iter_log_files():
        try:
            f.unlink()
            removed += 1
        except OSError:
            skipped += 1
    return {"removed": removed, "skipped": skipped}


def prune_old_logs(days: int = 30) -> int:
    """按 mtime 删除 logs/、jobs/ 中超过 days 天的文件（启动自洁用）；
    异常静默跳过，返回删除数"""
    cutoff = time.time() - days * 86400
    removed = 0
    for f in _iter_log_files():
        try:
            if f.stat().st_mtime < cutoff:
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
    """先写临时文件再替换，避免半截文件"""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
    tmp.replace(path)


# ---------------------------------------------------------------- 定时运行戳

def _mirror_dir():
    """镜像状态目录（环境变量 UVMP_TOOLKIT_MIRROR_STATE）。
    deb 的 systemd timer 以 root 跑 daily-export，其状态在 /root/.local/share 下，
    桌面用户的 GUI 读不到（已踩坑：自动执行的记录不显示）。安装脚本把桌面用户的
    状态目录写进服务的该环境变量，root 跑完把历史/成功戳镜像过来。"""
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

def append_history(app_id: str, record: dict):
    record = dict(record)
    record.setdefault("time", time.strftime("%Y-%m-%d %H:%M:%S"))
    f = _sub("history") / (app_id + ".jsonl")
    with open(f, "a", encoding="utf-8") as fp:
        fp.write(json.dumps(record, ensure_ascii=False) + "\n")
    # 粗修剪：超过 300 行时只留最近 200 行
    try:
        lines = f.read_text(encoding="utf-8").splitlines()
        if len(lines) > 300:
            f.write_text("\n".join(lines[-200:]) + "\n", encoding="utf-8")
    except Exception:
        pass
    md = _mirror_dir()   # root 定时器镜像给桌面用户（见 _mirror_dir）
    if md:
        try:
            mf = md / "history" / (app_id + ".jsonl")
            mf.parent.mkdir(parents=True, exist_ok=True)
            with open(mf, "a", encoding="utf-8") as fp:
                fp.write(json.dumps(record, ensure_ascii=False) + "\n")
            _mirror_chmod(mf)
        except OSError:
            pass


def recent_runs(app_id: str, n: int = 10) -> list:
    f = _sub("history") / (app_id + ".jsonl")
    if not f.exists():
        return []
    out = []
    for line in f.read_text(encoding="utf-8").splitlines()[::-1]:
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
