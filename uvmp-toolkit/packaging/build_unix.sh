#!/usr/bin/env bash
# 内网工具箱核心装配（linux 容器内通用；windows 用 CI 内联步骤）：
#   Python 核心（PyInstaller）+ Go 导出工作进程（uvmp-export）
# 用法：PYTHON=/path/to/python bash packaging/build_unix.sh
# 产出：dist/uvmp-core/（onedir：uvmp-core 可执行文件 + uvmp-export + config 模板 + install/ + vbs 等）
# （内置 CDP 浏览器即随包 Electron 本体，不再装配独立 Chromium）
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
"$PYTHON" -m pip install --no-cache-dir pyinstaller requests gmssl pypdf websocket-client playwright==1.62.0

echo "== PyInstaller 构建（onedir，console）=="
# hidden-import 说明：apps 的 backend.py 由注册器运行时按路径加载，静态分析够不到，
# 其依赖（uvmp/vehicle_export/official_track/browser_automation 等）必须显式收编；
# 课程引擎按源码资产收编，Playwright 的 Python 包、greenlet 与 Node driver 一并收集
"$PYTHON" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --add-data "course_autoplay_trial.py:." \
  --collect-all playwright --collect-all greenlet \
  --hidden-import pyee --hidden-import typing_extensions \
  --hidden-import browser_automation \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import goexport \
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

echo "== Go 导出工作进程（uvmp-export）=="
# 输出直接落 dist/uvmp-core/（与 uvmp-core 同入 resources/core/）；
# 工具链缺失且无 UVMP_GO_PREBUILT 时明确失败，不静默跳过
bash packaging/build_go_core.sh dist/uvmp-core/uvmp-export

echo "== 冒烟：selfcheck（加载全部应用后端，暴露缺失的 hidden-import/DLL）=="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping（Electron 壳的调用通道）=="
echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc | grep -q '"ok": true'

echo "== 冒烟：uvmp-export 空导出清单协议（codes=[]，零网络访问）=="
printf '%s\n' '{"config":{},"params":{},"codes":[]}' \
  | timeout 30 ./dist/uvmp-core/uvmp-export run > /tmp/uvmp-export-smoke.log \
  || { code=$?; [ "$code" -eq 124 ] && { echo "uvmp-export 冒烟超时"; exit 1; }; echo "uvmp-export 退出码 $code（协议允许 error 终态）"; }
cat /tmp/uvmp-export-smoke.log
grep -q '"event"' /tmp/uvmp-export-smoke.log || { echo "uvmp-export 冒烟失败：stdout 无 NDJSON 事件"; exit 1; }
echo "构建完成: dist/uvmp-core/（含 uvmp-export）"
