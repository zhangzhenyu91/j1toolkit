#!/bin/bash
# 卸载后清理：停用 usb-agent 与旧定时器 unit（用户配置 ~/.config/uvmp-toolkit/config.json 保留）
# 与 afterInstall.sh 对称：同时撤掉 /usr/bin 命令链接并刷新桌面/图标缓存。
APP_DIR="/opt/uvmp-toolkit"
EXE_NAME="uvmp-toolkit"

systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
systemctl stop uvmp-toolkit.service 2>/dev/null || true
rm -f /etc/systemd/system/uvmp-toolkit.service /etc/systemd/system/uvmp-toolkit.timer
rm -f /etc/polkit-1/rules.d/50-uvmp-toolkit.rules
rm -f /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla
systemctl daemon-reload 2>/dev/null || true

if type update-alternatives >/dev/null 2>&1; then
  # 必须用 afterInstall 注册的那个路径（/opt 下的真实可执行文件）：写成 /usr/bin 链接会让
  # update-alternatives 认不出这条记录，/etc/alternatives 里留下指向已删程序的死链。
  update-alternatives --remove "$EXE_NAME" "$APP_DIR/$EXE_NAME" 2>/dev/null || true
fi
rm -f "/usr/bin/$EXE_NAME"

update-mime-database /usr/share/mime 2>/dev/null || true
update-desktop-database /usr/share/applications 2>/dev/null || true
gtk-update-icon-cache -q -t /usr/share/icons/hicolor 2>/dev/null || true
if command -v xdg-desktop-menu >/dev/null 2>&1; then
  xdg-desktop-menu forceupdate 2>/dev/null || true
fi

echo "Shade 壹匣 - 内网 已卸载（用户配置保留：~/.config/uvmp-toolkit/config.json）"
exit 0
