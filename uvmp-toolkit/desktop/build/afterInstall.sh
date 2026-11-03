#!/bin/bash
# 内网工具箱 deb 安装后：建初始配置 + 安装并启用 usb-agent systemd service
# 运行期统一使用桌面用户（User=@APP_USER@），不再以 root 跑核心或挂载 U 盘；
# 配置位置：桌面用户 ~/.config/uvmp-toolkit/config.json（600，属主=用户）。
set -e
APP_DIR="/opt/uvmp-toolkit"
CORE_DIR="$APP_DIR/resources/core"
CORE="$CORE_DIR/uvmp-core"

DESKTOP_USER="${SUDO_USER:-$(getent passwd 1000 2>/dev/null | cut -d: -f1 || true)}"
if [ -z "$DESKTOP_USER" ]; then
  DESKTOP_USER=$(ls /home 2>/dev/null | head -1 || true)
fi
USER_HOME=$(getent passwd "$DESKTOP_USER" 2>/dev/null | cut -d: -f6 || true)
APP_USER="${DESKTOP_USER:-root}"
APP_HOME="${USER_HOME:-/root}"

if [ -n "$USER_HOME" ] && [ -d "$USER_HOME" ]; then
  CONF_DIR="$USER_HOME/.config/uvmp-toolkit"
  CONF="$CONF_DIR/config.json"
  if [ ! -f "$CONF" ]; then
    mkdir -p "$CONF_DIR"
    cp "$CORE_DIR/config.initial.json" "$CONF"
    chown -R "$DESKTOP_USER:$DESKTOP_USER" "$CONF_DIR"
    chmod 600 "$CONF"
  fi
  # 定时器已以桌面用户运行，状态直接写用户目录，不再需要 root→用户的镜像。
  MIRROR_STATE=""
  mkdir -p "$USER_HOME/.local/share/uvmp-toolkit"
  chown -R "$DESKTOP_USER:$DESKTOP_USER" "$USER_HOME/.local/share/uvmp-toolkit" 2>/dev/null || true
else
  CONF="/etc/uvmp-toolkit/config.json"
  mkdir -p /etc/uvmp-toolkit
  [ -f "$CONF" ] || cp "$CORE_DIR/config.initial.json" "$CONF"
  MIRROR_STATE=""
fi

# 允许桌面用户在无活动会话（锁屏/无人值守）时经 udisks 挂载虚拟 U 盘。
if [ "$APP_USER" != "root" ] && [ -d /etc/polkit-1/rules.d ]; then
  cat > /etc/polkit-1/rules.d/50-uvmp-toolkit.rules <<EOF
polkit.addRule(function(action, subject) {
  if ((action.id.indexOf("org.freedesktop.udisks2.filesystem-mount") === 0 ||
       action.id.indexOf("org.freedesktop.udisks2.filesystem-unmount") === 0) &&
      subject.user == "$APP_USER") return polkit.Result.YES;
});
EOF
  chmod 644 /etc/polkit-1/rules.d/50-uvmp-toolkit.rules
fi

sed -e "s|@CONFIG_PATH@|$CONF|g" -e "s|@RUN_CMD@|$CORE|g" \
    -e "s|@APP_USER@|$APP_USER|g" -e "s|@APP_HOME@|$APP_HOME|g" \
    -e "s|@MIRROR_STATE@|$MIRROR_STATE|g" \
    "$CORE_DIR/install/linux/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
# 旧版为每分钟 timer；升级时停用并移除，避免与新常驻 agent 双跑。
systemctl disable --now uvmp-toolkit.timer >/dev/null 2>&1 || true
rm -f /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload || true
systemctl enable uvmp-toolkit.service || true
systemctl restart uvmp-toolkit.service || true

# 图标缓存刷新（UKUI 等桌面不刷新可能仍显示旧/占位图标）
gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true

echo "Shade 壹匣 - 内网 已安装。请打开「Shade 壹匣 - 内网」→ 设置页填写 SSO 凭据；同步任务由服务端经 U 盘指令触发。"
exit 0
