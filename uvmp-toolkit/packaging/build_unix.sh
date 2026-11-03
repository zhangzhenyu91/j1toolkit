#!/usr/bin/env bash
# 内网工具箱核心装配（linux 容器内通用；windows 用 build_local.ps1 或 CI 内联步骤）：
#   Python 核心（PyInstaller）
# 用法：PYTHON=/path/to/python bash packaging/build_unix.sh
# 产出：dist/uvmp-core/（onedir：uvmp-core 可执行文件 + config 模板 + install/ + vbs 等）
# 门禁：Python unittest → PyInstaller 装配完整性（apps/parallel.py、playwright、websocket、
#       pypdf）→ selfcheck → rpc ping → CDP 冒烟（纯 Python data: 自检；容器无宿主浏览器时
#       以退出码 2 明确报告跳过，不静默当通过）。不依赖任何外部 Go 工作进程。
# 不随包浏览器：CDP 冒烟复用宿主公司指定浏览器（official_track.CdpBrowser），只需 websocket-client。
#
# 核心是【控制台】程序（stdio JSON-RPC 需要 stdout；GUI 由 Electron 承担）。
# 冻结后的目录约定（PyInstaller 扁平化顶层模块，__file__ 指向 _MEIPASS 根）：
#   - apps 源码 → _MEIPASS/apps（注册器按文件路径 importlib 加载，必须随包带源码；
#     pcd_export/parallel.py 也在其中，backend.py 运行时按路径 import）
#   - config.example.json → _MEIPASS 根（冻结时 TOOLKIT_DIR=_MEIPASS）
#   - config.ini 不打入包内，由本脚本放到 exe 旁（用户可改；config.py 冻结时优先读 exe 旁）
set -euo pipefail
cd "$(dirname "$0")/.."
PYTHON=${PYTHON:-python3}

assert_core_bundle() {
  local root="$1" exe="$2" with_playwright="$3" rel f mods listing mod
  [ -x "$root/$exe" ] || { echo "核心缺少可执行文件 $root/$exe" >&2; exit 1; }
  for rel in apps/pcd_export/parallel.py apps/pcd_export/backend.py \
             install/linux/uvmp-toolkit.service; do
    find "$root" -path "*/$rel" -type f | grep -q . \
      || { echo "核心缺少 $rel（PyInstaller 收编不完整）" >&2; exit 1; }
  done
  for f in config.ini config.example.json config.initial.json run-usb-agent.vbs; do
    [ -f "$root/$f" ] || { echo "核心缺少 $f" >&2; exit 1; }
  done
  # 纯 Python 包（websocket/pypdf）进 PYZ 归档，不在磁盘生成同名目录——查归档清单
  listing=$("$PYTHON" -m PyInstaller.utils.cliutils.archive_viewer -r -b "$root/$exe" 2>/dev/null) \
    || { echo "读取核心归档失败（pyi-archive_viewer）: $root/$exe" >&2; exit 1; }
  mods="apps selftest websocket pypdf"
  if [ "$with_playwright" = "1" ]; then mods="$mods playwright greenlet"; fi
  for mod in $mods; do
    printf '%s\n' "$listing" | grep -qxE "[[:space:]]*$mod" \
      || { echo "核心归档缺少模块 $mod（PyInstaller 漏收）" >&2; exit 1; }
  done
  if [ "$with_playwright" = "1" ]; then
    find "$root" -type d -name playwright | grep -q . \
      || { echo "核心缺少 playwright 包数据目录（-collect-all 漏收）" >&2; exit 1; }
  fi
  echo "核心包完整性：OK（apps/parallel.py、归档模块 $mods、config/install/vbs）"
}

echo "== 安装构建依赖 =="
"$PYTHON" -m pip install --no-cache-dir pyinstaller requests gmssl pypdf websocket-client playwright==1.62.0

# 测试使用 3.10 语法（括号化 with）；旧兼容解释器明确报告跳过而不是假装跑过
PY_OK=$("$PYTHON" -c 'import sys; print(1 if sys.version_info >= (3, 10) else 0)')
if [ "$PY_OK" = "1" ]; then
  echo "== 门禁：Python unittest =="
  "$PYTHON" -m unittest discover -s tests -v
  "$PYTHON" -m unittest discover -s packaging -p "test_*.py" -v
else
  echo "== 门禁：Python unittest 跳过（解释器 $("$PYTHON" -c 'import sys; print("%d.%d" % sys.version_info[:2])') < 3.10）；完整构建请用 Python 3.12 =="
fi

echo "== PyInstaller 构建（onedir，console）=="
# hidden-import 说明：apps 的 backend.py 由注册器运行时按路径加载，静态分析够不到，
# 其依赖（uvmp/vehicle_export/official_track/browser_automation 等）必须显式收编；
# 课程引擎按源码资产收编，Playwright 的 Python 包、greenlet 与 Node driver 一并收集；
# selftest 供 RPC selftest:cdp 使用，websocket 为 CdpBrowser 的传输层
"$PYTHON" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --add-data "course_autoplay_trial.py:." \
  --collect-all playwright --collect-all greenlet \
  --hidden-import pyee --hidden-import typing_extensions \
  --hidden-import browser_automation \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import usb_agent --hidden-import usb_protocol --hidden-import dev_script_runner \
  --hidden-import scheduler \
  --hidden-import selftest --hidden-import websocket --hidden-import pypdf \
  toolkit/app.py

echo "== 装配发布目录（含安装脚本与说明，产物开箱可装）=="
cp config.ini dist/uvmp-core/config.ini
cp toolkit/config.example.json dist/uvmp-core/config.example.json
cp packaging/config.initial.json dist/uvmp-core/config.initial.json
cp packaging/run-usb-agent.vbs dist/uvmp-core/run-usb-agent.vbs
cp README.md dist/uvmp-core/README.md
cp -r install dist/uvmp-core/install
find dist/uvmp-core/install -name "*.sh" -o -name "*.timer" -o -name "*.service" | xargs -r chmod +x 2>/dev/null || true

echo "== 校验：核心包完整性 =="
assert_core_bundle dist/uvmp-core uvmp-core 1

echo "== 冒烟：selfcheck（加载全部应用后端，暴露缺失的 hidden-import/DLL）=="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping（Electron 壳的调用通道）=="
echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc | grep -q '"ok": true'

echo "== 门禁：CDP 冒烟（纯 Python data: 自检）=="
cdp_rc=0
"$PYTHON" packaging/cdp_smoke.py --core dist/uvmp-core/uvmp-core --timeout 240 || cdp_rc=$?
case "$cdp_rc" in
  0) ;;
  2) echo "警告：CDP 冒烟已明确跳过（宿主无可用浏览器）；本次构建未验证 CDP 链路" >&2 ;;
  *) echo "CDP 冒烟失败（exit=$cdp_rc）" >&2; exit 1 ;;
esac

echo "构建完成: dist/uvmp-core/"
