# -*- coding: utf-8 -*-
"""交跨统计表同步：经 KVM 虚拟 U 盘（卷标 GLKVM）与壹匣服务端交换交跨统计表 xlsx。

指令契约（与服务端定稿）：服务端挂载 U 盘后在根目录放 crossing_sync_cmd.json：
    {"version": 1, "cycle_id": "...", "kind": "snapshot|replace",
     "file": "检修一班交跨统计表.xlsx", "issued_at": "..."}
- snapshot：把内网表（crossing_sync.local_path 指向的 xlsx）复制到 U 盘根，文件名用指令里的 file
- replace：把 U 盘根的 file（服务端刚推入的新表）复制覆盖到 crossing_sync.local_path
            （先备份原文件为 <原名>.bak 再覆盖）
指令的唯一消费者是常驻 usb-agent：执行后先原子写 crossing_sync_result_<id>.json 再删指令；
失败/过期同样写 ok=false 结果；指令 JSON 读不出来（EACCES/EIO）保留指令下轮重试。
U 盘清理分工（防重复执行/零残留）：replace 消费完的新表随指令一并删除；
snapshot 上盘的表留给服务端取回后删除（客户端不删）——服务端取件收尾会连同
残留表（任意「*交跨统计表.xlsx」旧名等）与指令文件残留一并清扫；本地手动备份 snapshot 同此口径。
向前兼容：未知字段忽略；未知 kind 跳过留痕；指令 JSON 损坏跳过留痕。

触发方式：
- tick（页面「检查 U 盘指令」）：U 盘不在静默返回；发现服务端指令时只提示、不消费
  （返回 skipped=agent_owned），由 usb-agent 写结果并回执，避免服务端永远等不到结果
- nightly（页面「本地备份到 U 盘」，仅本地调试）：无服务端指令时做一次 snapshot
  （正式同步由服务端 U 盘指令触发，此入口不参与自动调度）
"""

import json
import os
import shutil
from pathlib import Path

import config as _config
import platform_utils
import scheduler
import state

APP_ID = "crossing_sync"
CMD_NAME = "crossing_sync_cmd.json"  # U 盘根目录的指令文件名（服务端契约，不可改）
STAMP_NIGHTLY = (
    "crossing_sync_nightly"  # 本地手动备份的成功戳（不共用 daily_export，指南调度规范）
)
DEFAULT_FILE_NAME = "检修一班交跨统计表.xlsx"
ALLOWED_KINDS = ("snapshot", "replace")


# ---------------------------------------------------------------- 工具


def _quiet_usb_log(msg: str):
    """快速轮询的 U 盘日志：只在异常时输出，U盘就绪/未找到等常规信息静默。"""
    if msg.startswith(("U盘已就绪", "U盘已挂载", "未找到卷标")):
        return
    print(msg, flush=True)


def has_pending_command(cfg: dict) -> bool:
    """定时 tick 快速判定：U盘不存在或没有指令文件时返回 False，不创建 job/日志/历史。
    权限等异常返回 True，交给完整 _tick 输出可读错误；手动执行不走本函数。"""
    usb = cfg.get("usb") or {}
    mount = platform_utils.find_usb_mount(usb.get("label", ""), _quiet_usb_log)
    if not mount:
        return False
    try:
        os.stat(os.path.join(mount, CMD_NAME))
    except FileNotFoundError:
        return False
    except OSError:
        return True
    return True


def _in_root(name: str, root: Path) -> bool:
    """文件名安全校验：禁止路径穿越，只允许落在 U 盘根目录（指令 file 字段来自服务端）"""
    name = (name or "").strip()
    if not name or name in (".", "..") or Path(name).name != name:
        return False
    try:
        return os.path.commonpath(
            [str(root.resolve()), str((root / name).resolve())]
        ) == str(root.resolve())
    except (OSError, ValueError):
        return False


def _perm_hint(usb_dir: Path) -> RuntimeError:
    """U 盘挂载点当前用户无权限时的可操作报错（挂载属主/uid + 处置建议）。"""
    try:
        st = usb_dir.stat()
    except OSError:
        return RuntimeError("U盘挂载点 %s 不可访问" % usb_dir)
    owner = str(st.st_uid)
    try:
        import pwd

        owner = pwd.getpwuid(st.st_uid).pw_name
    except (ImportError, KeyError):
        pass
    uid = os.geteuid() if hasattr(os, "geteuid") else -1
    return RuntimeError(
        "U盘挂载点 %s 当前用户无权限（uid=%d，属主=%s）。"
        "请确保 systemctl status uvmp-toolkit.timer 为 active（定时器以桌面用户运行），"
        "或在无同步任务时卸载后以桌面用户重新挂载 U 盘；"
        "详见《内网工具箱开发指南》U 盘权限一节。" % (usb_dir, uid, owner)
    )


def _read_cmd(usb_dir: Path, log):
    """读 U 盘根目录指令文件：无→None；损坏→留痕返回 None；正常→dict"""
    cmd_file = usb_dir / CMD_NAME
    try:
        if not cmd_file.exists():
            return None
        cmd = json.loads(cmd_file.read_text(encoding="utf-8"))
    except PermissionError as e:
        raise _perm_hint(usb_dir) from e
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
        raise RuntimeError(
            "内网表不存在: %s（请到交跨同步页检查 local_path 配置）" % local
        )
    dest = usb_dir / file_name
    try:
        shutil.copyfile(str(local), str(dest))
        try:
            shutil.copystat(str(local), str(dest))
        except OSError:
            pass
        size = dest.stat().st_size
    except PermissionError as e:
        raise _perm_hint(usb_dir) from e
    log("已拷贝内网表 → U盘: %s（%.1f KB）" % (dest, size / 1024))
    return {"action": "snapshot", "file": str(dest)}


def _local_perm_hint(local: Path, err: OSError | None = None) -> RuntimeError:
    """内网表写入失败：区分被占用/只读/目录无权限，避免误报 U 盘权限。"""
    detail = ("：%s" % err) if err else ""
    return RuntimeError(
        "覆盖内网表失败：%s（文件被 Excel/WPS 占用、只读，或所在目录无写权限）%s；"
        "请关闭该表后重试，指令会保留并在下一轮自动补执行。" % (local, detail)
    )


def _replace(local: Path, usb_dir: Path, file_name: str, log) -> dict:
    """U 盘根的新表 → 覆盖内网表（先备份原文件为 <原名>.bak）；
    覆盖成功后删除 U 盘上的新表（已消费，后续不再使用，防重复执行）"""
    src = usb_dir / file_name
    try:
        if not src.is_file():
            raise RuntimeError("U盘上未找到新表: %s" % src)
        if src.resolve() == local.resolve():
            raise RuntimeError(
                "内网表路径不能指向 U 盘下发文件: %s（请在同步页重新选择内网 xlsx）"
                % local
            )
    except PermissionError as e:
        raise _perm_hint(usb_dir) from e
    except OSError:
        pass

    # 备份是兜底，不是覆盖前置条件：旧 .bak 只读/目录不可建时继续覆盖。
    if local.exists():
        bak = local.with_name(local.name + ".bak")
        try:
            if bak.exists():
                try:
                    bak.chmod(0o600)
                except OSError:
                    pass
                bak.unlink()
            # 只拷内容：内网表可能位于不支持 chmod/utime 的挂载（SMB/FUSE/FAT），
            # copy2 的 copystat 会抛 EPERM；元数据失败不应阻断覆盖。
            shutil.copyfile(str(local), str(bak))
            try:
                shutil.copystat(str(local), str(bak))
            except OSError:
                pass
            log("原内网表已备份: %s" % bak)
        except OSError as e:
            log("原内网表备份失败（继续覆盖）: %s" % e)

    try:
        local.parent.mkdir(parents=True, exist_ok=True)
        # 先写同目录临时文件再原子替换：Linux 下目标即使只读/被查看也可替换；
        # Windows 下目标被 Excel/WPS 占用时仍会失败，转成可读提示。
        tmp = local.with_name(local.name + ".sync-tmp")
        try:
            tmp.unlink()
        except OSError:
            pass
        try:
            shutil.copyfile(str(src), str(tmp))
            try:
                shutil.copystat(str(src), str(tmp))
            except OSError:
                pass
            try:
                os.replace(str(tmp), str(local))
            except PermissionError as e:
                # Windows 只读目标：清只读后重试一次（被占用时仍失败）。
                try:
                    os.chmod(str(local), 0o600)
                    os.replace(str(tmp), str(local))
                except OSError:
                    raise _local_perm_hint(local, e) from e
        except PermissionError as e:
            # 目录不允许新建临时文件时，退回直接覆盖已有文件。
            try:
                shutil.copyfile(str(src), str(local))
            except OSError as fallback:
                raise _local_perm_hint(local, fallback) from e
        except OSError as e:
            raise _local_perm_hint(local, e) from e
        finally:
            try:
                tmp.unlink()
            except OSError:
                pass
        size = local.stat().st_size
    except OSError as e:
        raise _local_perm_hint(local, e) from e

    try:
        src.unlink()
    except OSError as e:
        # 本地已覆盖成功；U 盘表删不掉不阻断本次结果，服务端下一轮收尾会清扫。
        log("U盘新表删除失败（内网覆盖已完成）: %s: %s" % (src, e))
    log("已用 U 盘新表覆盖内网表: %s（%.1f KB）" % (local, size / 1024))
    return {"action": "replace", "file": str(local), "src_deleted": file_name}


# ---------------------------------------------------------------- 业务


def handle_command(job, cmd: dict, mount: str) -> dict:
    """usb-agent 指令入口：执行一条服务端指令。

    与旧 _tick 的区别：U 盘挂载点由 agent 传入，不再自行扫描/挂载；失败直接抛异常，
    agent 统一写 {app}_result_<id>.json 并删除指令文件。
    """
    cfg = _config.load()
    ts = cfg.get("crossing_sync") or {}
    local_path = (ts.get("local_path") or "").strip()
    if not local_path:
        raise RuntimeError("未配置内网表路径（crossing_sync.local_path）")
    action = str(cmd.get("action") or cmd.get("kind") or "")
    if action not in ALLOWED_KINDS:
        raise RuntimeError("未知指令 action: %r" % action)
    params = cmd.get("params") if isinstance(cmd.get("params"), dict) else {}
    file_name = str(
        params.get("file")
        or cmd.get("file")
        or ts.get("file_name")
        or DEFAULT_FILE_NAME
    )
    usb_dir = Path(mount)
    if not _in_root(file_name, usb_dir):
        raise RuntimeError("指令 file 字段非法: %r" % file_name)
    job.log(
        "收到指令: action=%s id=%s file=%s"
        % (action, cmd.get("id", ""), file_name)
    )
    job.set_progress(0, 2, "执行指令")
    local = Path(local_path)
    if action == "snapshot":
        result = _snapshot(local, usb_dir, file_name, job.log)
    else:
        result = _replace(local, usb_dir, file_name, job.log)
    platform_utils.sync_mount(str(usb_dir))
    job.set_progress(2, 2, "完成")
    result["cycle_id"] = cmd.get("id", "")
    result["action"] = action
    job.log("指令执行完成: id=%s action=%s" % (cmd.get("id", ""), action))
    return result


def _tick(job, cfg, params: dict) -> dict:
    """检查一次：找 U 盘 → 有服务端指令只提示（由 agent 消费）；nightly 且无指令时本地备份 snapshot"""
    log = job.log
    ts = cfg.get("crossing_sync") or {}
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
    if cmd is not None:
        # 服务端指令的唯一消费者是常驻 usb-agent（写结果文件、按 id 回执、过期判定）。
        # 手动入口若也执行并删除指令，服务端将永远等不到结果文件——这里只提示，不消费。
        cycle_id = str(cmd.get("cycle_id") or cmd.get("id") or "")
        log(
            "U 盘存在服务端指令（cycle_id=%s），由后台 usb-agent 消费；"
            "手动入口不执行、不删除。可用 usb-agent --once 调试。" % cycle_id
        )
        return {"skipped": "agent_owned", "cycle_id": cycle_id}
    if not nightly:
        log("无指令文件（%s），本次跳过" % CMD_NAME)
        return {"skipped": "no_command"}
    # 本地手动备份：无指令也执行一次 snapshot，文件名用配置
    if not local_path:
        raise RuntimeError(
            "未配置内网表路径（crossing_sync.local_path）：请到交跨同步页选择内网 xlsx 文件"
        )
    file_name = (ts.get("file_name") or "").strip() or DEFAULT_FILE_NAME
    log("本地手动备份：U盘无指令，执行 snapshot（%s）" % file_name)
    result = _snapshot(Path(local_path), usb_dir, file_name, log)
    result["nightly"] = True
    platform_utils.sync_fs()
    job.set_progress(2, 2, "完成")
    return result


# ---------------------------------------------------------------- 契约入口


def run(job, params: dict) -> dict:
    cfg = _config.load()
    params = params or {}
    # 缺省（GUI「立即执行一次」）= 执行一次 tick；nightly 由页面「本地备份到 U 盘」传入
    if not params.get("nightly"):
        params = dict(params)
        params.setdefault("tick", True)
    result = _tick(job, cfg, params)
    if params.get("nightly"):
        # 本地手动备份成功（含 U盘不在的跳过）：写成功戳，今日不再重试；
        # 真失败（抛异常）不写戳，页面可重试
        from datetime import datetime

        scheduler.mark_success(
            datetime.now().strftime("%Y-%m-%d"),
            {"cycle_id": result.get("cycle_id", ""), "kind": result.get("kind", "")},
            stamp_name=STAMP_NIGHTLY,
        )
    return result


def status(cfg: dict) -> dict:
    ts = cfg.get("crossing_sync") or {}
    usb = cfg.get("usb") or {}
    local_path = (ts.get("local_path") or "").strip()
    found = platform_utils.scan_usb_device(usb.get("label", ""), lambda m: None)
    return {
        "crossing_sync": {
            "local_path": local_path,
            "local_exists": bool(local_path) and Path(local_path).is_file(),
            "file_name": ts.get("file_name", DEFAULT_FILE_NAME),
        },
        "stamp": state.get_stamp(STAMP_NIGHTLY),
        "last_run": state.last_run(APP_ID),
        "usb": {
            "label": usb.get("label", ""),
            "mount": (found[1] if found else "") or "",
        },
    }
