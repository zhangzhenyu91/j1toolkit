#!/usr/bin/env bash
# 内网工具箱 Python 核心 PyInstaller 构建（linux 容器内通用；windows 用 CI 内联步骤）
# 用法：PYTHON=/path/to/python bash packaging/build_unix.sh
# 产出：dist/uvmp-core/（onedir：uvmp-core 可执行文件 + config 模板 + install/ + vbs 等）
#
# 核心是【控制台】程序（stdio JSON-RPC 需要 stdout；GUI 由 Electron 承担）。
# 冻结后的目录约定（PyInstaller 扁平化顶层模块，__file__ 指向 _MEIPASS 根）：
#   - apps 源码 → _MEIPASS/apps（注册器按文件路径 importlib 加载，必须随包带源码）
#   - config.example.json → _MEIPASS 根（冻结时 TOOLKIT_DIR=_MEIPASS）
#   - config.ini 不打入包内，由本脚本放到 exe 旁（用户可改；config.py 冻结时优先读 exe 旁）
set -euo pipefail
cd "$(dirname "$0")/.."
PYTHON=${PYTHON:-python3}

echo "== 安装构建依赖 =="
"$PYTHON" -m pip install --no-cache-dir pyinstaller requests gmssl pypdf websocket-client

echo "== PyInstaller 构建（onedir，console）=="
# hidden-import 说明：apps 的 backend.py 由注册器运行时按路径加载，静态分析够不到，
# 其依赖（uvmp/vehicle_export/official_track/xlsx_util/devmock/rpc）必须显式收编
"$PYTHON" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import pypdf \
  toolkit/app.py

echo "== 装配发布目录（含安装脚本与说明，产物开箱可装）=="
cp config.ini dist/uvmp-core/config.ini
cp toolkit/config.example.json dist/uvmp-core/config.example.json
cp packaging/config.initial.json dist/uvmp-core/config.initial.json
cp packaging/run-daily-export.vbs dist/uvmp-core/run-daily-export.vbs
cp README.md dist/uvmp-core/README.md
cp -r install dist/uvmp-core/install
find dist/uvmp-core/install -name "*.sh" -o -name "*.timer" -o -name "*.service" | xargs -r chmod +x 2>/dev/null || true

echo "== 冒烟：selfcheck（加载全部应用后端，暴露缺失的 hidden-import/DLL）=="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping（Electron 壳的调用通道）=="
echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc | grep -q '"ok": true'
echo "构建完成: dist/uvmp-core/"
