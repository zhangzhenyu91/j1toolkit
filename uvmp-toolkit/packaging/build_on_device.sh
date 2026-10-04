#!/usr/bin/env bash
# 在麒麟（或其他 linux）设备上构建 uvmp-core 核心 —— 内网离线版
#
# 适用场景：GitHub Actions 额度耗尽 / 要在内网就地迭代核心。
# 麒麟 V10 SP1 系统 python3 为 3.7，本仓库业务代码保持 3.7 兼容（不 ctypes/无 missing_ok 等），
# PyInstaller 须用 ≤5.13（6.x 要求 python≥3.8）；bootloader 用 wheel 自带预编译版，无需 gcc。
#
# 准备（在能上网的机器上做一次，产物随仓库拷进内网）：
#   pip download pyinstaller==5.13.2 requests gmssl pypdf websocket-client \
#       -d wheels/ --only-binary :all:
#   （若该机器不是 linux py37，加 --platform manylinux2014_x86_64 --python-version 37
#    --implementation cp --abi cp37；arm64 麒麟换 manylinux2014_aarch64）
#
# 用法（仓库根目录）：bash packaging/build_on_device.sh [wheels目录，默认 ./wheels]
# 产物：dist/uvmp-core/（与 CI 的 core-linux-* 构件同构）
set -euo pipefail
cd "$(dirname "$0")/.."
WHEELS=${1:-./wheels}

PY=python3
command -v "$PY" >/dev/null || { echo "未找到 python3"; exit 1; }
"$PY" -c "import sys; print('python', sys.version.split()[0])"

echo "== 离线安装构建依赖（$WHEELS）=="
"$PY" -m pip install --no-index --find-links "$WHEELS" --user \
  pyinstaller==5.13.2 requests gmssl pypdf websocket-client
# pip --user 装的 pyinstaller 入口在 ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"

echo "== PyInstaller 构建（onedir，console）=="
"$PY" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import pypdf \
  toolkit/app.py

echo "== 装配发布目录 =="
cp config.ini dist/uvmp-core/config.ini
cp toolkit/config.example.json dist/uvmp-core/config.example.json
cp packaging/config.initial.json dist/uvmp-core/config.initial.json
cp packaging/run-daily-export.vbs dist/uvmp-core/run-daily-export.vbs
cp README.md dist/uvmp-core/README.md
cp -r install dist/uvmp-core/install
find dist/uvmp-core/install -name "*.sh" -o -name "*.timer" -o -name "*.service" | xargs -r chmod +x 2>/dev/null || true

echo "== 冒烟：selfcheck =="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping =="
echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc | grep -q '"ok": true'

echo "构建完成: dist/uvmp-core/（拷回 Windows 主机后用 build_local.ps1 -LinuxCoreAmd64 打 deb）"
