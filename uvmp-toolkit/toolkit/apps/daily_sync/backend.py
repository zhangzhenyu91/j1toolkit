# -*- coding: utf-8 -*-
"""每日派车单同步：导出当日（全部状态）派车单 → xlsx → 写入 U 盘（卷标 GLKVM）。

整合重写自 export_dispatch_orders.py：
- 原 playwright 页面链路 → vehicle_export.py 的纯接口链路（SSO 登录 + 签名请求）
- xlsx 列结构/文件名逐字保持（壹匣服务端解析契约）
- 「当日」口径保持：页面 filter 当日 + 客户端 is_today_order 二次过滤兜底
- U 盘发现/挂载/复制前复检保持旧行为；找不到 U 盘时可用 usb.fallback_dir 兜底
- 正式触发：服务端下发 daily_sync/export 指令，usb-agent 调用 handle_command；
  run()/CLI 仅作本地手动调试（手动执行仍可写 usb.fallback_dir）
- 日期严格校验为 YYYY-MM-DD（拼进文件名/filter 前），「当日」按北京时间（+08:00）判定；
  mock 模式直接拒绝生成正式 xlsx（假数据不得落进交付目录）
"""

import contextlib
import os
import re
import shutil
from datetime import datetime, timedelta, timezone
from pathlib import Path

import config as _config
import platform_utils
import scheduler
import state
import usb_agent
import usb_protocol
import uvmp
import vehicle_export as ve
import xlsx_util

APP_ID = "daily_sync"

# 「当日」口径固定按北京时间（服务端与 U 盘回执都用 +08:00），不随本机时区漂移：
# 被控机时区若为 UTC，凌晨 0~8 点的单会被算成前一天。
CST = timezone(timedelta(hours=8))
DAY_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")


def normalize_day(value, log=None) -> str:
    """校验并归一指令/参数里的日期：空 → 今天（北京时间）；非法 → RuntimeError。

    日期会拼进 xlsx 文件名与 filter 串，必须严格校验（只允许 YYYY-MM-DD 且真实存在
    的日历日），避免 `../` 之类把文件写到挂载点之外。
    """
    text = str(value or "").strip()
    if not text:
        return datetime.now(CST).strftime("%Y-%m-%d")
    if not DAY_RE.match(text):
        raise RuntimeError(
            "日期格式非法: %r（只接受 YYYY-MM-DD，例如 %s）"
            % (text, datetime.now(CST).strftime("%Y-%m-%d"))
        )
    try:
        datetime.strptime(text, "%Y-%m-%d")
    except ValueError:
        raise RuntimeError("日期不存在: %r（请检查月份/日期）" % text)
    return text


def is_today_order(item: dict, today: str) -> bool:
    """派车单是否属于当日：预计用车时间优先，创建时间兜底；字段缺失/不可解析时保留
    （移植自 export_dispatch_orders.py 的 _is_today_order，口径一致）

    时间戳一律按北京时间换算：fromtimestamp 默认跟随本机时区，被控机时区不是 +08:00
    时会把跨零点的单判错；超出平台可表示范围的时间戳按“不可解析”处理，不能抛异常
    打断整轮导出。
    """
    for field in ("planSendTime", "dispatchTime"):
        value = item.get(field)
        if not value:
            continue
        if isinstance(value, (int, float)):
            try:
                stamp = datetime.fromtimestamp(value / 1000, CST)
            except (OverflowError, OSError, ValueError):
                continue
            return stamp.strftime("%Y-%m-%d") == today
        try:
            return str(value)[:10] == today
        except (ValueError, TypeError):
            continue
    return True


def query_day_orders(client, day: str, log) -> list:
    """查询某日全部状态派车单（不附加 runState/vehicleState/keepTag 限制）"""
    flt = "planSendTime=%s 00:00&planSendTime2=%s 23:59:00" % (day, day)
    items = ve.query_pcd_all_filter(client, flt, page_size=200)
    log("接口返回 %d 条（页面口径）" % len(items))
    kept = [it for it in items if is_today_order(it, day)]
    if len(kept) != len(items):
        log("已按当日二次过滤: %d -> %d 条" % (len(items), len(kept)))
    return kept


def _refuse_mock(cfg: dict = None, mock: bool = False):
    """mock 模式拒写正式 xlsx：假数据一旦落进派车单文件，会被当成真实单据使用。"""
    if mock or (cfg and (cfg.get("dev") or {}).get("mock")):
        raise RuntimeError(
            "当前为 mock 模式（dev.mock=true），拒绝生成正式派车单 xlsx："
            "假数据不得写入交付目录/派车单文件。请关闭 dev.mock 后再导出，"
            "或在开发机上用脚本直接校验 mock 数据。"
        )


def _write_temp_xlsx(items, temp_dir: str, fname: str, log) -> Path:
    """生成临时 xlsx：先以 0600 建文件再写，避免默认 umask 让单据对同机其他用户可读。"""
    temp_path = Path(temp_dir) / fname
    fd = os.open(str(temp_path), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.close(fd)
    try:
        os.chmod(str(temp_path), 0o600)
    except OSError:
        pass
    xlsx_util.write_dispatch_xlsx(items, str(temp_path), log)
    return temp_path


@contextlib.contextmanager
def _usb_lock(agent_owned: bool, hint: Path = None):
    """U 盘操作锁（state/usb.lock）的临界区边界。

    agent_owned=True：指令模式已由 usb-agent 持锁，这里不能再抢——同进程二次 flock
    必然失败，会把 agent 自己的指令流程报成「锁被占用」。
    hint：写盘阶段抢不到锁时附上已生成的临时文件，否则用户不知道单据还在不在。
    """
    if agent_owned:
        yield
        return
    lock = usb_agent.device_lock()
    if lock is None:
        msg = "U 盘操作锁被占用（后台 usb-agent 正在扫描/挂载/执行指令），请稍后重试"
        if hint is not None:
            msg = "写入前 U 盘操作锁被占用；文件保留在: %s" % hint
        raise RuntimeError(msg)
    try:
        yield
    finally:
        usb_agent.release_device_lock(lock)


def _write_target(
    cfg: dict, log, usb_dir: str, source: str, agent_owned: bool, temp_path: Path
):
    """复制前复检交付目录（锁内调用）。

    抓取耗时期间 U 盘可能断开：手动模式重新扫描（含后备目录），指令模式不重新挂载，
    失败直接抛给 agent 写结果（服务端可重试），避免与 GLKVM 共享切换抢 udisks。
    """
    if os.path.isdir(usb_dir):
        return usb_dir, source
    if agent_owned:
        raise RuntimeError("写入前 U 盘不可用；文件保留在: %s" % temp_path)
    log("复制前检测到目标目录不可用，重新检测U盘 ...")
    found, found_source = platform_utils.usb_writable_dir(cfg, log)
    if not found:
        raise RuntimeError("写入前 U 盘不可用；文件保留在: %s" % temp_path)
    return found, found_source


def _export(job, cfg: dict, day: str, dest_dir: str = "", mark: bool = True) -> dict:
    """导出指定日期派车单到指定目录。

    dest_dir 非空 = usb-agent 传入的 U 盘挂载点（指令模式，不再自行扫描/挂载）；
    为空 = GUI/CLI 手动执行，沿用 usb_writable_dir 的 U 盘发现与后备目录逻辑。

    手动入口与常驻 agent 共用 state/usb.lock，但只包住真正碰 U 盘的两段（发现挂载点、
    复制+落盘）；登录、查询、生成 xlsx 全程不持锁——手动入口若全程持锁，agent 的指令
    要等整轮查询结束才能执行，服务端会先等到超时。指令模式已由 agent 持锁，不得重入。
    """
    log = job.log
    day = normalize_day(day)
    log("开始导出派车单（日期：%s）" % day)
    agent_owned = bool(dest_dir)

    job.set_progress(0, 4, "检查U盘")
    if agent_owned:
        usb_dir, source = dest_dir, "usb"
        if not os.path.isdir(usb_dir):
            raise RuntimeError("U盘目录不可用: %s" % usb_dir)
    else:
        with _usb_lock(agent_owned=False):
            usb_dir, source = platform_utils.usb_writable_dir(cfg, log)
        if not usb_dir:
            raise RuntimeError(
                "U盘不可用且未配置后备目录（可在设置页配置 usb.fallback_dir）"
            )
    _refuse_mock(cfg)

    job.set_progress(1, 4, "登录派车系统")
    client, _auth = uvmp.make_client(cfg, log)
    if getattr(client, "is_mock", False):
        # 配置可能只在校验之后才被改成 mock（或客户端自身带标记）：再兜一层。
        _refuse_mock(mock=True)

    job.set_progress(2, 4, "查询派车单")
    # 网络往返 + 分页抓取可能很慢，必须在锁外完成。
    items = query_day_orders(client, day, log)
    log("共获取 %d 条当日派车单" % len(items))
    if not items:
        log("警告：未获取到任何数据（当日可能无派车）")
        if mark:
            scheduler.mark_success(day, {"count": 0, "file": ""})
        return {"date": day, "count": 0, "file": "", "dest": ""}

    job.set_progress(3, 4, "生成Excel")
    temp_dir = ((cfg.get("daily_export") or {}).get("temp_dir") or "").strip()
    temp_dir = temp_dir or str(Path.home() / "临时下载")  # 保持旧脚本习惯
    Path(temp_dir).mkdir(parents=True, exist_ok=True)
    fname = "%s-派车单.xlsx" % day
    temp_path = _write_temp_xlsx(items, temp_dir, fname, log)

    job.set_progress(4, 4, "写入交付目录")
    # 只有这一段（复检挂载点 → 复制 → 改权限 → sync）真正碰 U 盘，锁到此为止。
    with _usb_lock(agent_owned, hint=temp_path):
        usb_dir, source = _write_target(
            cfg, log, usb_dir, source, agent_owned, temp_path
        )
        final_path = Path(usb_dir) / fname
        if not usb_protocol.inside_root(final_path, usb_dir):
            raise RuntimeError("目标文件不在挂载点内，已拒绝写入: %s" % final_path)
        shutil.copy2(str(temp_path), str(final_path))
        try:
            # copy2 会连同临时文件的 0600 一起搬过来；交付文件要让服务端/其他账户可读。
            os.chmod(str(final_path), 0o644)
        except OSError:
            pass
        platform_utils.sync_mount(str(usb_dir))
    size = final_path.stat().st_size
    try:
        temp_path.unlink()
    except OSError as e:
        log("临时文件清理失败（不影响交付）: %s: %s" % (temp_path, e))
    log(
        "导出完成! 文件已保存至: %s（%.1f KB，%d 条）"
        % (final_path, size / 1024, len(items))
    )

    if mark:
        scheduler.mark_success(day, {"count": len(items), "file": str(final_path)})
    result = {
        "date": day,
        "count": len(items),
        "file": str(final_path),
        "dest": "U盘" if source == "usb" else "后备目录",
    }
    if not dest_dir:
        # 手动入口（GUI/CLI）用 dir 打开产物目录；agent 结果写盘前会被收敛掉路径。
        result["dir"] = usb_dir
    return result


def handle_command(job, cmd: dict, mount: str) -> dict:
    """usb-agent 指令入口：服务端下发 daily_sync/export，导出到 agent 已挂载的 U 盘。"""
    cfg = _config.load()
    action = str(cmd.get("action") or cmd.get("kind") or "")
    if action not in ("export", "snapshot"):
        raise RuntimeError("未知指令 action: %r" % action)
    params = cmd.get("params") if isinstance(cmd.get("params"), dict) else {}
    return _export(job, cfg, params.get("date"), dest_dir=mount, mark=False)


def run(job, params: dict) -> dict:
    cfg = _config.load()
    return _export(job, cfg, (params or {}).get("date"))


def status(cfg: dict) -> dict:
    usb = cfg.get("usb") or {}
    found = platform_utils.scan_usb_device(usb.get("label", ""), lambda m: None)
    return {
        "stamp": state.get_stamp(scheduler.STAMP_NAME),
        "last_run": state.last_run(APP_ID),
        "usb": {
            "label": usb.get("label", ""),
            "fallback_dir": usb.get("fallback_dir", ""),
            "mount": (found[1] if found else "") or "",
        },
    }
