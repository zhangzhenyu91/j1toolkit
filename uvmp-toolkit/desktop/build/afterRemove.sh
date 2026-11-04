#!/bin/bash
# 卸载后清理：停用 usb-agent 与旧定时器 unit（用户配置 ~/.config/uvmp-toolkit/config.json 保留）
systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
systemctl stop uvmp-toolkit.service 2>/dev/null || true
rm -f /etc/systemd/system/uvmp-toolkit.service /etc/systemd/system/uvmp-toolkit.timer
rm -f /etc/polkit-1/rules.d/50-uvmp-toolkit.rules
rm -f /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla
systemctl daemon-reload 2>/dev/null || true
exit 0
