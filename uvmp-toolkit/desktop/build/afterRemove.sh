#!/bin/bash
# 卸载后清理：停用定时器与 unit 文件（用户配置 ~/.config/uvmp-toolkit/config.json 保留）
systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
systemctl stop uvmp-toolkit.service 2>/dev/null || true
rm -f /etc/systemd/system/uvmp-toolkit.service /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload 2>/dev/null || true
exit 0
