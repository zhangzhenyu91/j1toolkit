# -*- coding: utf-8 -*-
"""每日派车单同步：导出当日（全部状态）派车单 → xlsx → 写入 U 盘（卷标 GLKVM）。

整合重写自 export_dispatch_orders.py：
- 原 playwright 页面链路 → vehicle_export.py 的纯接口链路（SSO 登录 + 签名请求）
- xlsx 列结构/文件名逐字保持（壹匣服务端解析契约）
- 「当日」口径保持：页面 filter 当日 + 客户端 is_today_order 二次过滤兜底
- U 盘发现/挂载/复制前复检保持旧行为；找不到 U 盘时可用 usb.fallback_dir 兜底
"""
import os
import shutil
from datetime import datetime
from pathlib import Path

import config as _config
import platform_utils
import scheduler
import state
import uvmp
import vehicle_export as ve
import xlsx_util

APP_ID = "daily_sync"


def is_today_order(item: dict, today: str) -> bool:
    """派车单是否属于当日：预计用车时间优先，创建时间兜底；字段缺失/不可解析时保留
    （移植自 export_dispatch_orders.py 的 _is_today_order，口径一致）"""
    for field in ('planSendTime', 'dispatchTime'):
        value = item.get(field)
        if not value:
            continue
        try:
            if isinstance(value, (int, float)):
                return datetime.fromtimestamp(value / 1000).strftime("%Y-%m-%d") == today
            return str(value)[:10] == today
        except (ValueError, TypeError, OSError):
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


def run(job, params: dict) -> dict:
    cfg = _config.load()
    day = ((params or {}).get("date") or "").strip() or datetime.now().strftime("%Y-%m-%d")
    log = job.log
    log("开始导出派车单（日期：%s）" % day)

    job.set_progress(0, 4, "检查U盘")
    usb_dir, source = platform_utils.usb_writable_dir(cfg, log)
    if not usb_dir:
        raise RuntimeError("U盘不可用且未配置后备目录（可在设置页配置 usb.fallback_dir）")

    job.set_progress(1, 4, "登录派车系统")
    client, _auth = uvmp.make_client(cfg, log)

    job.set_progress(2, 4, "查询派车单")
    items = query_day_orders(client, day, log)
    log("共获取 %d 条当日派车单" % len(items))
    if not items:
        log("警告：未获取到任何数据（当日可能无派车）")
        scheduler.mark_success(day, {"count": 0, "file": ""})
        return {"date": day, "count": 0, "file": "", "dest": ""}

    job.set_progress(3, 4, "生成Excel")
    temp_dir = ((cfg.get("daily_export") or {}).get("temp_dir") or "").strip()
    temp_dir = temp_dir or str(Path.home() / "临时下载")   # 保持旧脚本习惯
    Path(temp_dir).mkdir(parents=True, exist_ok=True)
    fname = "%s-派车单.xlsx" % day
    temp_path = Path(temp_dir) / fname
    xlsx_util.write_dispatch_xlsx(items, str(temp_path), log)

    job.set_progress(4, 4, "写入交付目录")
    # 抓取耗时期间 U 盘可能断开，复制前重新检测（移植旧脚本行为）
    if not os.path.isdir(usb_dir):
        log("复制前检测到目标目录不可用，重新检测U盘 ...")
        usb_dir, source = platform_utils.usb_writable_dir(cfg, log)
        if not usb_dir:
            raise RuntimeError("写入前 U 盘不可用；文件保留在: %s" % temp_path)
    final_path = Path(usb_dir) / fname
    shutil.copy2(str(temp_path), str(final_path))
    platform_utils.sync_fs()
    size = final_path.stat().st_size
    log("导出完成! 文件已保存至: %s（%.1f KB，%d 条）" % (final_path, size / 1024, len(items)))

    scheduler.mark_success(day, {"count": len(items), "file": str(final_path)})
    return {"date": day, "count": len(items), "file": str(final_path),
            "dest": "U盘" if source == "usb" else "后备目录", "dir": usb_dir}


def status(cfg: dict) -> dict:
    sch = cfg.get("schedule") or {}
    usb = cfg.get("usb") or {}
    due, reason, _today = scheduler.due_check(cfg)
    return {
        "schedule": {"enabled": bool(sch.get("enabled", True)),
                     "time": sch.get("time", "09:15"),
                     "driver": sch.get("driver", "os"),
                     "next_run": scheduler.next_run_text(cfg)},
        "stamp": state.get_stamp(scheduler.STAMP_NAME),
        "last_run": state.last_run(APP_ID),
        "usb": {"label": usb.get("label", ""),
                "fallback_dir": usb.get("fallback_dir", ""),
                "mount": platform_utils.find_usb_mount(usb.get("label", ""), lambda m: None) or ""},
        "due": {"due": due, "reason": reason},
    }
