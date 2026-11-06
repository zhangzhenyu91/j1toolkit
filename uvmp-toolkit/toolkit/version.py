# -*- coding: utf-8 -*-
"""客户端版本号读取：环境变量优先，冻结包读可执行文件旁的 version.txt。

安装包（deb postinst / NSIS 构建）写入 version.txt；源码模式没有该文件时返回空串。
usb-agent 的指令结果与集群节点上报共用这一份口径。
"""

import os
import sys
from pathlib import Path


def read_version() -> str:
    """返回当前客户端版本；未知返回空串（调用方显示“未知”）。"""
    value = os.environ.get("UVMP_VERSION", "").strip()
    if value:
        return value
    try:
        if getattr(sys, "frozen", False):
            base = Path(sys.executable).resolve().parent
        else:
            base = Path(__file__).resolve().parent.parent
        path = base / "version.txt"
        if path.is_file():
            return path.read_text(encoding="utf-8").strip()
    except OSError:
        pass
    return ""
