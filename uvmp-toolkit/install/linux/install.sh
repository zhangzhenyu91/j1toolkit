#!/usr/bin/env bash
# 内网工具箱【源码模式】安装脚本（银河麒麟 V10 SP1，amd64/arm64）
#
# 适用场景：无法运行 Electron 桌面客户端的机器（老旧 glibc/arm64 兼容问题等）——
# 仅安装每日派车单同步的 systemd timer（CLI 无头运行，无 GUI）。
# 有 GUI 的正常机器请直接用 Release 的 .deb 安装包。
#
# 用法：sudo bash install/linux/install.sh
set -euo pipefail

# ---- CRLF 自检（文件经 Windows 中转拷贝后行尾变 CRLF 的坑）----
if grep -q $'\r' "$0"; then
  echo "检测到本脚本为 CRLF 行尾，请先执行："
  echo "  sed -i 's/\r\$//' install/linux/*.sh install/linux/*.timer install/linux/*.service"
  echo "然后再重跑安装。"
  exit 1
fi
[[ $EUID -eq 0 ]] || { echo "请用 sudo 运行：sudo bash install/linux/install.sh"; exit 1; }

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
APP_USER=${SUDO_USER:-}
if [[ -z "$APP_USER" ]]; then
  APP_USER=$(stat -c %U "$PROJECT_DIR" 2>/dev/null || echo "root")
fi
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
echo "[2/4] 检查 Python 依赖 ..."
if ! "$PYTHON" -c "import sys; sys.path.insert(0,'$PROJECT_DIR/pylib'); import requests, gmssl" >/dev/null 2>&1; then
  mkdir -p "$PROJECT_DIR/pylib"
  for f in "$PROJECT_DIR"/offline_packages/*.whl; do
    "$PYTHON" -m zipfile -e "$f" "$PROJECT_DIR/pylib/"
  done
  echo "      已解压 offline_packages -> pylib/"
fi
"$PYTHON" -c "import sys; sys.path.insert(0,'$PROJECT_DIR/pylib'); import requests, gmssl; print('      依赖 OK（requests/gmssl）')" \
  || { echo "      依赖校验失败，请检查 offline_packages/"; exit 1; }

# ---- 3. 配置文件 ----
echo "[3/4] 配置文件: $CONFIG_PATH"
if [[ -f "$CONFIG_PATH" ]]; then
  echo "      已存在，保留原配置"
else
  read -r -p "      SSO 账号（派车系统登录账号）: " SSO_USER
  read -r -s -p "      SSO 密码: " SSO_PASS; echo
  read -r -p "      每日导出时间 [09:15]: " SCH_TIME; SCH_TIME=${SCH_TIME:-09:15}
  read -r -p "      U盘卷标 [GLKVM]: " USB_LABEL; USB_LABEL=${USB_LABEL:-GLKVM}
  SSO_USER="$SSO_USER" SSO_PASS="$SSO_PASS" SCH_TIME="$SCH_TIME" USB_LABEL="$USB_LABEL" \
  CONFIG_PATH="$CONFIG_PATH" "$PYTHON" - <<'PYEOF'
import json, os
cfg = {
    "sso": {"username": os.environ["SSO_USER"], "password": os.environ["SSO_PASS"]},
    "schedule": {"enabled": True, "time": os.environ["SCH_TIME"]},
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

# ---- 4. systemd unit（timer 每 10 分钟 due-check）----
echo "[4/4] 安装 systemd unit ..."
# root 定时器的状态镜像目标=安装用户状态目录（GUI/手动查看才能看到自动执行的记录）
APP_HOME=$(getent passwd "$APP_USER" 2>/dev/null | cut -d: -f6 || echo "")
MIRROR_STATE=""
if [[ -n "$APP_HOME" && -d "$APP_HOME" ]]; then
  MIRROR_STATE="$APP_HOME/.local/share/uvmp-toolkit"
  mkdir -p "$MIRROR_STATE"
  chown -R "$APP_USER":"$APP_USER" "$MIRROR_STATE" 2>/dev/null || true
fi
sed -e "s|@CONFIG_PATH@|$CONFIG_PATH|g" \
    -e "s|@RUN_CMD@|$PYTHON $PROJECT_DIR/toolkit/app.py|g" \
    -e "s|@MIRROR_STATE@|$MIRROR_STATE|g" \
    "$SCRIPT_DIR/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
cp "$SCRIPT_DIR/uvmp-toolkit.timer" /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload
systemctl enable --now uvmp-toolkit.timer

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
echo "安装完成！（源码 CLI 模式，仅每日定时导出）"
echo "  查看定时器:   systemctl list-timers uvmp-toolkit.timer"
echo "  手动执行一次: systemctl start uvmp-toolkit.service"
echo "  查看日志:     journalctl -u uvmp-toolkit.service"
echo "  GUI 客户端请使用 Release 的 .deb 安装包"
echo "============================================================"
