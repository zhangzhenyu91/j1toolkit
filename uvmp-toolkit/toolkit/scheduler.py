# -*- coding: utf-8 -*-
"""due-check 调度判定。

设计要点（见《内网工具箱开发指南.md》调度规范）：
OS 级定时器（systemd timer / Windows 计划任务）每分钟唤醒一次
`app.py daily-export --if-due`，本模块判定「已到配置时刻且今日未成功」才执行。
好处：GUI 改时间只写 config.json，不需要 root 改 unit 文件；
机器关机错过到点后，开机下一 tick 自动补跑。

分钟级 tick 下失败任务若每分钟重跑会高频打 SSO 登录（有锁定风险），
故加「距上次尝试 ≥10 分钟」尝试间隔闸（daily_export_attempt 戳），
失败重试节奏与原 10 分钟轮询一致（2026-10-09 起：轮询改每分钟，
解决 09:15 被 10 分钟量化到 09:20、与服务端取件撞车的问题）。
"""
import re
import time
from datetime import datetime

import state

STAMP_NAME = "daily_export"             # 每日派车单同步的成功戳
ATTEMPT_STAMP = "daily_export_attempt"  # 上次尝试戳（无论成败，到点放行前记录）
ATTEMPT_MIN_INTERVAL = 600              # 两次自动尝试的最小间隔（秒），与原 10 分钟轮询节奏一致


def parse_hhmm(s: str):
    """解析 'HH:MM'，非法输入回退 09:15"""
    m = re.match(r"^\s*(\d{1,2}):(\d{2})\s*$", s or "")
    if m:
        h, mi = int(m.group(1)), int(m.group(2))
        if 0 <= h <= 23 and 0 <= mi <= 59:
            return h, mi
    return 9, 15


def due_check(cfg: dict, now: datetime = None, driver: str = "os"):
    """返回 (是否应执行, 原因, 今日日期串)。
    driver 门闸：schedule.driver=os（系统定时器执行）/ app（客户端驻留执行）。
    客户端驻留模式下 OS 定时器的到点调用一律跳过（反之亦然），杜绝双跑。"""
    now = now or datetime.now()
    today = now.strftime("%Y-%m-%d")
    sch = cfg.get("schedule") or {}
    want = (sch.get("driver") or "os")
    if want != driver:
        return False, ("由客户端驻留调度" if want == "app" else "由系统定时器调度"), today
    if not sch.get("enabled", True):
        return False, "定时导出已停用", today
    stamp = state.get_stamp(STAMP_NAME) or {}
    if stamp.get("date") == today:
        return False, "今日已成功执行（%s）" % stamp.get("time", ""), today
    h, mi = parse_hhmm(sch.get("time", "09:15"))
    if (now.hour, now.minute) < (h, mi):
        return False, "未到设定时间 %02d:%02d" % (h, mi), today
    att = state.get_stamp(ATTEMPT_STAMP) or {}
    if att.get("date") == today:
        try:
            last = datetime.strptime(att.get("time", ""), "%Y-%m-%d %H:%M:%S")
        except ValueError:
            last = None
        if last and (now - last).total_seconds() < ATTEMPT_MIN_INTERVAL:
            return False, "距上次尝试不足 %d 分钟（%s）" % (
                ATTEMPT_MIN_INTERVAL // 60, att.get("time", "")), today
    return True, "已到设定时间 %02d:%02d" % (h, mi), today


def mark_success(today: str, extra: dict = None):
    data = {"date": today, "time": time.strftime("%Y-%m-%d %H:%M:%S")}
    data.update(extra or {})
    state.set_stamp(STAMP_NAME, data)


def mark_attempt(today: str):
    """到点放行后、执行前调用：拉开分钟级 tick 下的失败重试间隔。"""
    state.set_stamp(ATTEMPT_STAMP, {"date": today, "time": time.strftime("%Y-%m-%d %H:%M:%S")})


def next_run_text(cfg: dict, now: datetime = None) -> str:
    """GUI 状态卡显示用：下一次定时执行的时间描述"""
    sch = cfg.get("schedule") or {}
    if not sch.get("enabled", True):
        return "已停用"
    now = now or datetime.now()
    h, mi = parse_hhmm(sch.get("time", "09:15"))
    due, _, today = due_check(cfg, now)
    if due:
        return "随时（已到过点，等待定时器触发）"
    from datetime import timedelta
    target = now.replace(hour=h, minute=mi, second=0, microsecond=0)
    if target <= now:
        target += timedelta(days=1)
    return target.strftime("%Y-%m-%d %H:%M")
