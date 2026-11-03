# -*- coding: utf-8 -*-
"""运行戳（stamp）小工具。

同步任务已改为服务端 U 盘指令驱动（见 usb_agent.py / 开发指南 U 盘监听一节），
客户端不再做 due-check 或本地定时；本模块只保留“手动执行成功后写成功戳”的能力，
供页面状态卡展示最近一次手动导出/备份时间。
"""

import time

import state

STAMP_NAME = "daily_export"  # 每日派车单同步的成功戳


def mark_success(today: str, extra: dict = None, stamp_name: str = None):
    """记录一次成功（date/time + 业务摘要），供状态卡展示。"""
    data = {"date": today, "time": time.strftime("%Y-%m-%d %H:%M:%S")}
    data.update(extra or {})
    state.set_stamp(stamp_name or STAMP_NAME, data)
