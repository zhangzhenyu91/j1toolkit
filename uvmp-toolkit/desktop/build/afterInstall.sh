#!/bin/bash
# 内网工具箱 deb 安装后：建初始配置（定时默认停用）+ 安装并启用 systemd timer（due-check）
# 配置位置：桌面用户 ~/.config/uvmp-toolkit/config.json（600，属主=用户）；
# root 定时器经 Environment=UVMP_TOOLKIT_CONFIG 读同一份（root 可读 600 用户文件）。
set -e
APP_DIR="/opt/uvmp-toolkit"
CORE_DIR="$APP_DIR/resources/core"
CORE="$CORE_DIR/uvmp-core"

DESKTOP_USER="${SUDO_USER:-$(getent passwd 1000 2>/dev/null | cut -d: -f1 || true)}"
if [ -z "$DESKTOP_USER" ]; then
  DESKTOP_USER=$(ls /home 2>/dev/null | head -1 || true)
fi
USER_HOME=$(getent passwd "$DESKTOP_USER" 2>/dev/null | cut -d: -f6 || true)

if [ -n "$USER_HOME" ] && [ -d "$USER_HOME" ]; then
  CONF_DIR="$USER_HOME/.config/uvmp-toolkit"
  CONF="$CONF_DIR/config.json"
  if [ ! -f "$CONF" ]; then
    mkdir -p "$CONF_DIR"
    cp "$CORE_DIR/config.initial.json" "$CONF"
    chown -R "$DESKTOP_USER:$DESKTOP_USER" "$CONF_DIR"
    chmod 600 "$CONF"
  fi
  # root 定时器的状态镜像目标=桌面用户状态目录（GUI 才能看到自动执行的记录/成功戳）；
  # 预建并归用户所有，root 镜像写入的文件由 state.py 置 666 保用户可写
  MIRROR_STATE="$USER_HOME/.local/share/uvmp-toolkit"
  mkdir -p "$MIRROR_STATE"
  chown -R "$DESKTOP_USER:$DESKTOP_USER" "$MIRROR_STATE" 2>/dev/null || true
else
  CONF="/etc/uvmp-toolkit/config.json"
  mkdir -p /etc/uvmp-toolkit
  [ -f "$CONF" ] || cp "$CORE_DIR/config.initial.json" "$CONF"
  MIRROR_STATE=""
fi

sed -e "s|@CONFIG_PATH@|$CONF|g" -e "s|@RUN_CMD@|$CORE|g" \
    -e "s|@MIRROR_STATE@|$MIRROR_STATE|g" \
    "$CORE_DIR/install/linux/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
cp "$CORE_DIR/install/linux/uvmp-toolkit.timer" /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload || true
systemctl enable --now uvmp-toolkit.timer || true

# 图标缓存刷新（UKUI 等桌面不刷新可能仍显示旧/占位图标）
gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true

echo "Shade 壹匣 - 内网 已安装。请打开「Shade 壹匣 - 内网」→ 设置页填写 SSO 凭据，再到「每日派车单同步」页启用定时。"
exit 0
