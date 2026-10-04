#!/usr/bin/env bash
# 内网工具箱 卸载脚本（源码 CLI 模式）：停用并删除 systemd timer/service。
# 配置文件 config.json 默认保留；项目文件与 pylib 不删除。
# （deb 安装的卸载请用 sudo apt remove uvmp-toolkit）
set -euo pipefail
if grep -q $'\r' "$0"; then
  echo "检测到 CRLF 行尾，请先执行：sed -i 's/\r\$//' install/linux/*.sh"
  exit 1
fi
[[ $EUID -eq 0 ]] || { echo "请用 sudo 运行"; exit 1; }

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)

systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
systemctl stop uvmp-toolkit.service 2>/dev/null || true
rm -f /etc/systemd/system/uvmp-toolkit.timer /etc/systemd/system/uvmp-toolkit.service
systemctl daemon-reload
# 旧版（v1.x PyQt 时代）可能在桌面留过启动器，一并清理
rm -f /usr/share/applications/uvmp-toolkit.desktop
for d in /home/*/桌面 /home/*/Desktop; do
  rm -f "$d/uvmp-toolkit.desktop" 2>/dev/null || true
done

read -r -p "是否同时删除配置文件 $PROJECT_DIR/toolkit/config.json（含 SSO 凭据）？[y/N] " ANS
if [[ "${ANS:-N}" =~ ^[Yy]$ ]]; then
  rm -f "$PROJECT_DIR/toolkit/config.json"
  echo "配置文件已删除"
else
  echo "配置文件保留"
fi

echo "卸载完成。项目文件与 pylib 未删除；如需恢复旧版 playwright 定时导出："
echo "  sudo bash <j1toolkit scripts>/systemd/install.sh"
