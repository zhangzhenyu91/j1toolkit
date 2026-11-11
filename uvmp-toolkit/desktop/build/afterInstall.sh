#!/bin/bash
# 内网工具箱 deb 安装后：建初始配置 + 安装并启用 usb-agent systemd service
# 运行期统一使用桌面用户（User=@APP_USER@），不再以 root 跑核心或挂载 U 盘；
# 配置位置：桌面用户 ~/.config/uvmp-toolkit/config.json（600，属主=用户）。
# 注意：本文件整体替换 electron-builder 默认 after-install 模板，模板原有职责必须自理——
#   /usr/bin 命令软链、chrome-sandbox 权限、MIME/桌面数据库刷新，漏一条都会出现
#   「命令行找不到程序」「沙箱报错起不来」「菜单里短暂出现后消失」。
set -e
APP_DIR="/opt/uvmp-toolkit"
CORE_DIR="$APP_DIR/resources/core"
CORE="$CORE_DIR/uvmp-core"
# deb 控制字段 Package 名（= electron-builder.yml 的 deb.packageName），查版本必须用它——
# 曾按旧名 uvmp-toolkit 查询，dpkg 查不到 → version.txt 从来没写出来，客户端上报版本为空。
PKG_NAME="uvmp-toolkit-desktop"
EXE_NAME="uvmp-toolkit"

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

# 版本标记：核心/agent 写入状态与结果的 client.version（postinst 阶段 dpkg 已记录版本号）。
# 注意不能用 dpkg-query 的 Version 格式串：花括号会被 electron-builder 当宏展开并报错。
PKG_VERSION="$(dpkg -s "$PKG_NAME" 2>/dev/null | awk '/^Version:/{print $2; exit}')"
if [ -n "$PKG_VERSION" ]; then
  printf '%s\n' "$PKG_VERSION" > "$CORE_DIR/version.txt"
  chmod 644 "$CORE_DIR/version.txt" 2>/dev/null || true
else
  echo "警告：未取到 $PKG_NAME 的版本号，version.txt 未写入" >&2
fi

# /usr/bin 命令入口（默认模板职责）：优先 update-alternatives，回退直接软链
if type update-alternatives >/dev/null 2>&1; then
  if [ -L "/usr/bin/$EXE_NAME" ] && [ -e "/usr/bin/$EXE_NAME" ] \
     && [ "$(readlink "/usr/bin/$EXE_NAME")" != "/etc/alternatives/$EXE_NAME" ]; then
    rm -f "/usr/bin/$EXE_NAME"
  fi
  update-alternatives --install "/usr/bin/$EXE_NAME" "$EXE_NAME" "$APP_DIR/$EXE_NAME" 100 \
    || ln -sf "$APP_DIR/$EXE_NAME" "/usr/bin/$EXE_NAME"
else
  ln -sf "$APP_DIR/$EXE_NAME" "/usr/bin/$EXE_NAME"
fi

# Chromium 沙箱权限：内核支持 user namespace 时 chrome-sandbox 只需 0755；
# 不支持（麒麟/统信若干加固内核关掉了 unprivileged userns）必须 4755 root:root，
# 否则启动即 "SUID sandbox helper binary was found, but is not configured correctly"。
SANDBOX="$APP_DIR/chrome-sandbox"
if [ -f "$SANDBOX" ]; then
  if ! { [ -L /proc/self/ns/user ] && unshare --user true; } 2>/dev/null; then
    chown root:root "$SANDBOX" 2>/dev/null || true
    chmod 4755 "$SANDBOX" 2>/dev/null || true
  else
    chmod 0755 "$SANDBOX" 2>/dev/null || true
  fi
fi

# 允许桌面用户在无活动会话（锁屏/无人值守）时经 udisks 挂载虚拟 U 盘。
# 同时写 JS .rules 与旧版 .pkla：麒麟 V10 的 polkit 版本不一，单写一种可能被静默忽略。
if [ -f "$CORE_DIR/install/linux/polkit.sh" ]; then
  . "$CORE_DIR/install/linux/polkit.sh"
  install_polkit_rule "$APP_USER"
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

# 桌面/图标缓存刷新（UKUI、DDE 不刷新会继续显示占位图标或干脆不出现菜单项）
update-mime-database /usr/share/mime 2>/dev/null || true
update-desktop-database /usr/share/applications 2>/dev/null || true
gtk-update-icon-cache -q -t /usr/share/icons/hicolor 2>/dev/null || true
# 已登录的桌面会话立即重扫菜单（无 DBus/无该工具时静默跳过）
if command -v xdg-desktop-menu >/dev/null 2>&1; then
  xdg-desktop-menu forceupdate 2>/dev/null || true
fi

echo "Shade 壹匣 - 内网 已安装。请打开「Shade 壹匣 - 内网」→ 设置页填写 SSO 凭据；同步任务由服务端经 U 盘指令触发。"
exit 0
