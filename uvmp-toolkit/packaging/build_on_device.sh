#!/usr/bin/env bash
# 在麒麟（或其他 linux）设备上构建 uvmp-core 核心 —— 内网离线版
#
# 适用场景：GitHub Actions 额度耗尽 / 要在内网就地迭代核心。
# 麒麟 V10 SP1 系统 python3 为 3.7 时仍可构建旧版兼容核心（课程自动学习不可用）；
# 完整课程能力需用 Python 3.12 构建，正式 CI 产物已统一内置 CPython 3.12。
# PyInstaller 5.13 仅用于 Python 3.7 旧兼容构建；完整构建用当前 PyInstaller 与 playwright==1.62.0。
#
# 准备（在能上网的机器上做一次，产物随仓库拷进内网）：
#   pip download pyinstaller==5.13.2 requests gmssl pypdf websocket-client \
#       -d wheels/ --only-binary :all:
#   完整构建另需下载 Python 3.12 对应平台的 pyinstaller、playwright==1.62.0 及传递依赖 wheel。
#
# 用法（仓库根目录）：bash packaging/build_on_device.sh [wheels目录，默认 ./wheels]
# 产物：dist/uvmp-core/（含 uvmp-export，与 CI 的 core-linux-* 构件同构）
# （内置 CDP 浏览器即随包 Electron 本体，不再装配独立 Chromium，无浏览器资产要求）
#
# Go 导出工作进程（离线内网，缺失即失败）：
#   - uvmp-export：需要系统 go 工具链，或 UVMP_GO_PREBUILT=/path/uvmp-export 指入已构建二进制
set -euo pipefail
cd "$(dirname "$0")/.."
WHEELS=${1:-./wheels}

PY=python3
command -v "$PY" >/dev/null || { echo "未找到 python3"; exit 1; }
"$PY" -c "import sys; print('python', sys.version.split()[0])"

if "$PY" -c 'import sys; raise SystemExit(sys.version_info < (3, 9))'; then
  BUILD_DEPS=(pyinstaller requests gmssl pypdf websocket-client playwright==1.62.0)
  COLLECT_ARGS=(--collect-all playwright --collect-all greenlet --hidden-import pyee --hidden-import typing_extensions)
else
  echo "提示：Python < 3.9，仅构建旧版兼容核心；课程自动学习不可用。完整构建请使用 Python 3.12"
  BUILD_DEPS=(pyinstaller==5.13.2 requests gmssl pypdf websocket-client)
  COLLECT_ARGS=()
fi

echo "== 离线安装构建依赖（$WHEELS）=="
"$PY" -m pip install --no-index --find-links "$WHEELS" --user "${BUILD_DEPS[@]}"
# pip --user 装的 pyinstaller 入口在 ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"

echo "== PyInstaller 构建（onedir，console）=="
"$PY" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --add-data "course_autoplay_trial.py:." \
  "${COLLECT_ARGS[@]}" \
  --hidden-import browser_automation \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import goexport \
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

echo "== Go 导出工作进程（uvmp-export；离线：go 工具链或 UVMP_GO_PREBUILT）=="
if [ -z "${UVMP_GO_PREBUILT:-}" ] && ! command -v go >/dev/null; then
  echo "错误: 内网离线构建需要 go 工具链，或以 UVMP_GO_PREBUILT=/path/uvmp-export 指入已构建二进制（缺失即失败）" >&2
  exit 1
fi
bash packaging/build_go_core.sh dist/uvmp-core/uvmp-export

echo "== 冒烟：selfcheck =="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping =="
echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc | grep -q '"ok": true'

echo "== 冒烟：uvmp-export 空导出清单协议（codes=[]，零网络访问）=="
printf '%s\n' '{"config":{},"params":{},"codes":[]}' \
  | timeout 30 ./dist/uvmp-core/uvmp-export run > /tmp/uvmp-export-smoke.log \
  || { code=$?; [ "$code" -eq 124 ] && { echo "uvmp-export 冒烟超时"; exit 1; }; echo "uvmp-export 退出码 $code（协议允许 error 终态）"; }
cat /tmp/uvmp-export-smoke.log
grep -q '"event"' /tmp/uvmp-export-smoke.log || { echo "uvmp-export 冒烟失败：stdout 无 NDJSON 事件"; exit 1; }

echo "构建完成: dist/uvmp-core/（含 uvmp-export；拷回 Windows 主机后用 build_local.ps1 -LinuxCoreAmd64 打 deb）"
