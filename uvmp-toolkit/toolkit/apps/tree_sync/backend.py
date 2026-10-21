# -*- coding: utf-8 -*-
"""树障统计表同步：经 KVM 虚拟 U 盘（卷标 GLKVM）与壹匣服务端交换树障统计表 xlsx。

指令契约（与服务端定稿）：服务端挂载 U 盘后在根目录放 tree_sync_cmd.json：
    {"version": 1, "cycle_id": "...", "kind": "snapshot|replace",
     "file": "检修一班树木统计表.xlsx", "issued_at": "..."}
- snapshot：把内网表（tree_sync.local_path 指向的 xlsx）复制到 U 盘根，文件名用指令里的 file
- replace：把 U 盘根的 file（服务端刚推入的新表）复制覆盖到 tree_sync.local_path
            （先备份原文件为 <原名>.bak 再覆盖）
执行成功后删除指令文件并 sync_fs() 落盘；失败保留指令文件（服务端有超时判定，防误判已执行）。
U 盘清理分工（防重复执行/零残留）：replace 消费完的新表随指令一并删除；
snapshot 上盘的表留给服务端取回后删除（客户端不删）——服务端取件收尾会连同
残留表（任意「*树木统计表.xlsx」旧名等）与指令文件残留一并清扫；夜间兜底 snapshot 同此口径。
向前兼容：未知字段忽略；未知 kind 跳过留痕；指令 JSON 损坏跳过留痕。

触发方式：
- tick（每分钟定时器唤醒）：U 盘不在静默返回；有指令按指令执行
- nightly（22:55 本地定时兜底）：有指令按 tick 逻辑走；无指令则做一次 snapshot
  （服务端 22:50 挂载 U 盘，客户端 22:55 兜底拷贝内网表上盘，文件名用 tree_sync.file_name）
"""
import json
import os
import shutil
from pathlib import Path

import config as _config
import platform_utils
import scheduler
import state

APP_ID = "tree_sync"
CMD_NAME = "tree_sync_cmd.json"          # U 盘根目录的指令文件名（服务端契约，不可改）
STAMP_NIGHTLY = "tree_sync_nightly"      # 夜间兜底的成功戳（不共用 daily_export，指南调度规范）
DEFAULT_FILE_NAME = "检修一班树木统计表.xlsx"
ALLOWED_KINDS = ("snapshot", "replace")


# ---------------------------------------------------------------- 工具

def _in_root(name: str, root: Path) -> bool:
    """文件名安全校验：禁止路径穿越，只允许落在 U 盘根目录（指令 file 字段来自服务端）"""
    name = (name or "").strip()
    if not name or name in (".", "..") or Path(name).name != name:
        return False
    try:
        return os.path.commonpath([str(root.resolve()), str((root / name).resolve())]) \
            == str(root.resolve())
    except (OSError, ValueError):
        return False


def _read_cmd(usb_dir: Path, log):
    """读 U 盘根目录指令文件：无→None；损坏→留痕返回 None；正常→dict"""
    cmd_file = usb_dir / CMD_NAME
    if not cmd_file.exists():
        return None
    try:
        cmd = json.loads(cmd_file.read_text(encoding="utf-8"))
    except Exception as e:
        log("指令文件 %s 解析失败，跳过（保留待人工处理）: %s" % (CMD_NAME, e))
        return None
    if not isinstance(cmd, dict):
        log("指令文件 %s 内容不是 JSON 对象，跳过" % CMD_NAME)
        return None
    return cmd


def _snapshot(local: Path, usb_dir: Path, file_name: str, log) -> dict:
    """内网表 → U 盘根（文件名 file_name）"""
    if not local.is_file():
        raise RuntimeError("内网表不存在: %s（请到树障统计表同步页检查 local_path 配置）" % local)
    dest = usb_dir / file_name
    shutil.copy2(str(local), str(dest))
    log("已拷贝内网表 → U盘: %s（%.1f KB）" % (dest, dest.stat().st_size / 1024))
    return {"action": "snapshot", "file": str(dest)}


def _replace(local: Path, usb_dir: Path, file_name: str, log) -> dict:
    """U 盘根的新表 → 覆盖内网表（先备份原文件为 <原名>.bak）；
    覆盖成功后删除 U 盘上的新表（已消费，后续不再使用，防重复执行）"""
    src = usb_dir / file_name
    if not src.is_file():
        raise RuntimeError("U盘上未找到新表: %s" % src)
    if local.exists():
        bak = local.with_name(local.name + ".bak")
        shutil.copy2(str(local), str(bak))
        log("原内网表已备份: %s" % bak)
    local.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(str(src), str(local))
    log("已用 U 盘新表覆盖内网表: %s（%.1f KB）" % (local, local.stat().st_size / 1024))
    src.unlink()
    log("U盘新表已删除（消费完成）: %s" % src)
    return {"action": "replace", "file": str(local), "src_deleted": file_name}


def _finish(usb_dir: Path, log):
    """执行成功：删除指令文件并落盘（服务端取回时据指令消失判定已执行）"""
    try:
        (usb_dir / CMD_NAME).unlink()
        log("指令文件已删除: %s" % CMD_NAME)
    except FileNotFoundError:
        pass
    platform_utils.sync_fs()


# ---------------------------------------------------------------- 业务

def _tick(job, cfg, params: dict) -> dict:
    """轮询一次：找 U 盘 → 有指令按 kind 执行；无指令时 nightly 兜底 snapshot"""
    log = job.log
    ts = cfg.get("tree_sync") or {}
    local_path = (ts.get("local_path") or "").strip()
    nightly = bool((params or {}).get("nightly"))

    job.set_progress(0, 2, "检测U盘")
    usb = cfg.get("usb") or {}
    mount = platform_utils.find_usb_mount(usb.get("label", ""), log)
    if not mount:
        # U 盘不在属常态（服务端未挂载），静默返回不算错误
        log("U盘未挂载，本次跳过")
        return {"skipped": "usb_absent", "nightly": nightly}
    usb_dir = Path(mount)

    job.set_progress(1, 2, "检查指令")
    cmd = _read_cmd(usb_dir, log)
    if cmd is None:
        if not nightly:
            log("无指令文件（%s），本次跳过" % CMD_NAME)
            return {"skipped": "no_command"}
        # 夜间兜底：无指令也执行一次 snapshot，文件名用配置
        if not local_path:
            raise RuntimeError("未配置内网表路径（tree_sync.local_path）：请到树障统计表同步页选择内网 xlsx 文件")
        file_name = (ts.get("file_name") or "").strip() or DEFAULT_FILE_NAME
        log("夜间兜底：U盘无指令，执行 snapshot（%s）" % file_name)
        result = _snapshot(Path(local_path), usb_dir, file_name, log)
        result["nightly"] = True
        platform_utils.sync_fs()
        job.set_progress(2, 2, "完成")
        return result

    kind = str(cmd.get("kind") or "")
    cycle_id = str(cmd.get("cycle_id") or "")
    file_name = str(cmd.get("file") or "")
    log("收到指令: kind=%s cycle_id=%s file=%s" % (kind, cycle_id, file_name))
    if kind not in ALLOWED_KINDS:
        # 未知 kind：跳过留痕，不删指令（疑似新版服务端指令，等客户端升级处理）
        log("未知指令 kind=%r，跳过（cycle_id=%s，指令保留）" % (kind, cycle_id))
        return {"skipped": "unknown_kind", "kind": kind, "cycle_id": cycle_id}
    if not _in_root(file_name, usb_dir):
        log("指令 file 字段非法: %r，跳过（指令保留）" % file_name)
        return {"skipped": "bad_file", "cycle_id": cycle_id}
    if not local_path:
        # 有指令要执行但配置缺失：失败留痕，指令保留（配置补齐后下一轮 tick 自动补执行）
        raise RuntimeError("未配置内网表路径（tree_sync.local_path）：请到树障统计表同步页选择内网 xlsx 文件")

    job.set_progress(2, 2, "执行指令")
    local = Path(local_path)
    if kind == "snapshot":
        result = _snapshot(local, usb_dir, file_name, log)
    else:
        result = _replace(local, usb_dir, file_name, log)
    _finish(usb_dir, log)
    result["cycle_id"] = cycle_id
    result["kind"] = kind
    log("指令执行完成: cycle_id=%s kind=%s" % (cycle_id, kind))
    return result


# ---------------------------------------------------------------- 契约入口

def run(job, params: dict) -> dict:
    cfg = _config.load()
    params = params or {}
    # 缺省（GUI「立即执行一次」）= 执行一次 tick；nightly 由定时器 --nightly 传入
    if not params.get("nightly"):
        params = dict(params)
        params.setdefault("tick", True)
    result = _tick(job, cfg, params)
    if params.get("nightly"):
        # 夜间兜底成功（含 U盘不在的跳过）：写成功戳，今日不再重试；
        # 真失败（抛异常）不写戳，由尝试间隔闸控制重试节奏
        from datetime import datetime
        scheduler.mark_success(datetime.now().strftime("%Y-%m-%d"),
                               {"cycle_id": result.get("cycle_id", ""),
                                "kind": result.get("kind", "")},
                               stamp_name=STAMP_NIGHTLY)
    return result


def status(cfg: dict) -> dict:
    ts = cfg.get("tree_sync") or {}
    usb = cfg.get("usb") or {}
    local_path = (ts.get("local_path") or "").strip()
    return {
        "tree_sync": {"local_path": local_path,
                      "local_exists": bool(local_path) and Path(local_path).is_file(),
                      "file_name": ts.get("file_name", DEFAULT_FILE_NAME),
                      "nightly_time": ts.get("nightly_time", "22:55")},
        "stamp": state.get_stamp(STAMP_NIGHTLY),
        "last_run": state.last_run(APP_ID),
        "usb": {"label": usb.get("label", ""),
                "mount": platform_utils.find_usb_mount(usb.get("label", ""), lambda m: None) or ""},
    }
