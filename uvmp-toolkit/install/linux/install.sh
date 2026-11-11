#!/usr/bin/env bash
# 内网工具箱【源码模式】安装脚本（银河麒麟 V10 SP1，amd64/arm64）
#
# 适用场景：无法运行 Electron 桌面客户端的机器（老旧 glibc/arm64 兼容问题等）——
# 安装 U 盘单监听 usb-agent systemd service（CLI 无头运行，无 GUI）。
# 有 GUI 的正常机器请直接用 Release 的 .deb 安装包。
#
# 用法：sudo bash install/linux/install.sh
set -euo pipefail

# ---- CRLF 自检（文件经 Windows 中转拷贝后行尾变 CRLF 的坑）----
if grep -q $'\r' "$0"; then
  echo "检测到本脚本为 CRLF 行尾，请先执行："
  echo "  sed -i 's/\r\$//' install/linux/*.sh install/linux/*.service"
  echo "然后再重跑安装。"
  exit 1
fi
[[ $EUID -eq 0 ]] || { echo "请用 sudo 运行：sudo bash install/linux/install.sh"; exit 1; }

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
APP_USER=${SUDO_USER:-}
if [[ -z "$APP_USER" ]]; then
  read -r -p "运行客户端的桌面用户名: " APP_USER
fi
id "$APP_USER" >/dev/null 2>&1 || { echo "用户不存在: $APP_USER"; exit 1; }
APP_HOME=$(getent passwd "$APP_USER" 2>/dev/null | cut -d: -f6 || true)
[[ -n "$APP_HOME" ]] || APP_HOME="/home/$APP_USER"
CONFIG_PATH="$PROJECT_DIR/toolkit/config.json"

echo "============================================================"
echo "内网工具箱 安装（源码 CLI 模式，无 GUI）"
echo "  项目目录: $PROJECT_DIR"
echo "============================================================"

# ---- 1. Python 检测 ----
PYTHON=""
for c in /opt/jdzh/python3.14/bin/python3 python3; do
  if command -v "$c" >/dev/null 2>&1; then PYTHON=$(command -v "$c"); break; fi
done
[[ -n "$PYTHON" ]] || { echo "未找到 python3，请先安装"; exit 1; }
echo "[1/4] Python: $PYTHON ($("$PYTHON" --version 2>&1))"

# ---- 2. Python 依赖（免 pip：wheel 解压到 pylib/）----
# 探测必须做到 gmssl.sm2：gmssl 的 sm2.py 在模块顶层 `from Cryptodome.Util.asn1 import ...`，
# 只 import gmssl 能过、一到 SM2 加密就 ImportError（自动登录必挂）。pycryptodomex 的
# wheel 按平台分（win_amd64 / manylinux x86_64 / manylinux aarch64），故解压时按架构过滤。
echo "[2/4] 检查 Python 依赖 ..."
ARCH=$(uname -m)
dep_ok() {
  "$PYTHON" -c "import sys; sys.path.insert(0,'$PROJECT_DIR/pylib'); import requests, pypdf, websocket, gmssl.sm2; from Cryptodome.Util.asn1 import DerSequence; print('      依赖 OK（requests/pypdf/websocket-client/gmssl.sm2/Cryptodome）')"
}
if ! dep_ok >/dev/null 2>&1; then
  mkdir -p "$PROJECT_DIR/pylib"
  for f in "$PROJECT_DIR"/offline_packages/*.whl; do
    base=$(basename "$f")
    case "$base" in
      # 异构平台 wheel 解开会在 pylib 里互相覆盖（同名 .so/.pyd），只解当前架构的
      *win32*|*win_amd64*|*win_arm64*) continue ;;
      *macosx*) continue ;;
      *manylinux*aarch64*|*linux_aarch64*)
        [ "$ARCH" = "aarch64" ] || continue ;;
      *manylinux*x86_64*|*linux_x86_64*)
        [ "$ARCH" = "x86_64" ] || continue ;;
    esac
    "$PYTHON" -m zipfile -e "$f" "$PROJECT_DIR/pylib/"
  done
  echo "      已解压 offline_packages -> pylib/"
fi
dep_ok || { echo "      依赖校验失败，请检查 offline_packages/（需含 pycryptodomex）"; exit 1; }

# ---- 3. 配置文件 ----
echo "[3/4] 配置文件: $CONFIG_PATH"
if [[ -f "$CONFIG_PATH" ]]; then
  echo "      已存在，保留原配置"
else
  read -r -p "      SSO 账号（派车系统登录账号）: " SSO_USER
  read -r -s -p "      SSO 密码: " SSO_PASS; echo
  read -r -p "      U盘卷标 [GLKVM]: " USB_LABEL; USB_LABEL=${USB_LABEL:-GLKVM}
  SSO_USER="$SSO_USER" SSO_PASS="$SSO_PASS" USB_LABEL="$USB_LABEL" \
  CONFIG_PATH="$CONFIG_PATH" "$PYTHON" - <<'PYEOF'
import json, os
cfg = {
    "sso": {"username": os.environ["SSO_USER"], "password": os.environ["SSO_PASS"]},
    "usb": {"label": os.environ["USB_LABEL"], "fallback_dir": ""},
}
path = os.environ["CONFIG_PATH"]
with open(path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
print("      已写入 " + path)
PYEOF
  chown "$APP_USER":"$APP_USER" "$CONFIG_PATH" 2>/dev/null || true
fi

# ---- 4. systemd unit（usb-agent 常驻）----
echo "[4/4] 安装 systemd unit ..."
# agent 以桌面用户运行，状态直接写用户目录，不再需要 root→用户的镜像。
MIRROR_STATE=""
if [[ -n "$APP_HOME" && -d "$APP_HOME" ]]; then
  mkdir -p "$APP_HOME/.local/share/uvmp-toolkit"
  chown -R "$APP_USER":"$APP_USER" "$APP_HOME/.local/share/uvmp-toolkit" 2>/dev/null || true
fi
chown "$APP_USER":"$APP_USER" "$CONFIG_PATH" 2>/dev/null || true

# 允许桌面用户在无活动会话（锁屏/无人值守）时经 udisks 挂载虚拟 U 盘。
# 同时写 JS .rules 与旧版 .pkla：麒麟 V10 的 polkit 版本不一，单写一种可能被静默忽略。
if [[ -f "$SCRIPT_DIR/polkit.sh" ]]; then
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/polkit.sh"
  install_polkit_rule "$APP_USER"
fi

sed -e "s|@CONFIG_PATH@|$CONFIG_PATH|g" \
    -e "s|@RUN_CMD@|$PYTHON $PROJECT_DIR/toolkit/app.py|g" \
    -e "s|@APP_USER@|$APP_USER|g" -e "s|@APP_HOME@|$APP_HOME|g" \
    -e "s|@MIRROR_STATE@|$MIRROR_STATE|g" \
    "$SCRIPT_DIR/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
# 旧版为每分钟 timer；升级时停用并移除，避免与新常驻 agent 双跑。
systemctl disable --now uvmp-toolkit.timer >/dev/null 2>&1 || true
rm -f /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload
systemctl enable uvmp-toolkit.service
systemctl restart uvmp-toolkit.service

# ---- 旧定时器迁移（避免双份导出写同名文件）----
if systemctl list-unit-files 2>/dev/null | grep -q "^export-dispatch-orders\.timer"; then
  echo "检测到旧版 export-dispatch-orders.timer（playwright 方案）"
  read -r -p "停用旧定时器并切换到本工具箱？[Y/n] " ANS
  if [[ "${ANS:-Y}" =~ ^[Yy]?$ ]]; then
    systemctl disable --now export-dispatch-orders.timer || true
    echo "旧定时器已停用（脚本本体保留，可手动回退）"
  else
    echo "保留旧定时器——注意：两套同时运行会重复导出同名文件！"
  fi
fi

echo "============================================================"
echo "安装完成！（源码 CLI 模式：U 盘单监听 agent）"
echo "  查看服务:     systemctl status uvmp-toolkit.service --no-pager"
echo "  手动检查一次: $PYTHON $PROJECT_DIR/toolkit/app.py usb-agent --once"
echo "  查看日志:     journalctl -u uvmp-toolkit.service"
echo "  GUI 客户端请使用 Release 的 .deb 安装包"
echo "============================================================"
