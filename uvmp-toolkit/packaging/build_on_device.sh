#!/usr/bin/env bash
# 在麒麟（或其他 linux）设备上构建 uvmp-core 核心 —— 内网离线版
#
# 适用场景：GitHub Actions 额度耗尽 / 要在内网就地迭代核心。
# 目标系统：银河麒麟 V10 SP1、统信 UOS 20（amd64/arm64）。PyInstaller 不能跨平台，
# 只能在目标架构的 linux 上构建。
#
# 推荐用 Python 3.11+（正式 CI 产物内置 CPython 3.15.2）；麒麟系统自带 python3 为 3.7 时
# 仍可构建旧版兼容核心（课程自动学习不可用），依赖版本按该解释器钉死，**不使用 latest**。
#
# 准备（在能上网的机器上做一次，产物随仓库拷进内网）：
#   现代路径（Python >= 3.11）：pip download -r packaging/requirements-core.txt -d wheels/ --only-binary :all:
#   旧兼容路径（Python < 3.11）：
#     pip download pyinstaller==5.13.2 requests==2.31.0 gmssl==3.2.2 pycryptodomex==3.24.2 \
#         pypdf==3.17.4 websocket-client==1.3.3 -d wheels/ --only-binary :all:
#
# 用法（仓库根目录）：bash packaging/build_on_device.sh [wheels目录，默认 ./wheels]
# 产物：dist/uvmp-core/（与 CI 的 core-linux-* 构件同构）
# 目录布局：PyInstaller 6（现代分支）onedir 把 --add-data 资产放 dist/uvmp-core/_internal/，
#       sys._MEIPASS 即该目录；PyInstaller 5（旧解释器分支）放在 exe 同目录——装配断言两种都认。
# 门禁：依赖自洽 → Python unittest（3.10+ 语法）→ 装配完整性（含两个 trial 引擎脚本）→ selfcheck →
#       rpc ping → CDP 冒烟（纯 Python data: 自检；缺宿主浏览器退出码 2 明确报告）。
#       不依赖任何外部 Go 工作进程。
# 不随包浏览器：CDP 冒烟复用宿主公司指定浏览器，只需 websocket-client。
set -euo pipefail
cd "$(dirname "$0")/.."
WHEELS=${1:-./wheels}
REQUIREMENTS=packaging/requirements-core.txt

PY=python3
command -v "$PY" >/dev/null || { echo "未找到 python3"; exit 1; }
"$PY" -c "import sys; print('python', sys.version.split()[0])"

# set -u 下未赋值数组取 ${#arr[@]} 会在旧 bash 报 unbound variable，先声明空数组
REQ_ARGS=()
# 分界必须与 requirements-core.txt 的实际最低版本一致：requests 2.34 / pypdf 7 / pyinstaller 6
# 要求 Python 3.10+，图片引擎 Pillow 13.0.0 要求 3.11+，取最高者 → 3.11。
# 分界写低了会让装不上依赖的解释器走现代路径，pip 找不到可用 wheel 后整锅失败；
# 写高了会把 3.11 机器误判成旧兼容分支，产出缺 imaging 的核心。
if "$PY" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))'; then
  REQ_ARGS=(-r "$REQUIREMENTS")
  COLLECT_ARGS=(--collect-all playwright --collect-all greenlet --collect-all PIL --hidden-import pyee --hidden-import typing_extensions)
  WITH_PLAYWRIGHT=1
  WITH_IMAGING=1
else
  echo "提示：Python < 3.11，仅构建旧版兼容核心；课程自动学习与图片工具不启用。完整构建请使用 Python 3.11+（内置 CPython 3.15.2）。"
  # 旧解释器用不了 requirements-core.txt（现代 requests/pypdf 要求 3.10+、Pillow 13 要求 3.11+），
  # 按 3.7 可用版本钉死；该分支不收编 Pillow，产出的核心不含图片工具 imaging。
  BUILD_DEPS=(pyinstaller==5.13.2 requests==2.31.0 gmssl==3.2.2 pycryptodomex==3.24.2 \
              pypdf==3.17.4 websocket-client==1.3.3)
  COLLECT_ARGS=()
  WITH_PLAYWRIGHT=0
  WITH_IMAGING=0
fi

# 资产定位：PyInstaller 6 落 _internal/，5 落 exe 同目录，两处任一命中即合格。
# 只认这两个精确位置——不用递归 find 兜底，否则「整包缺件」会被别处的同名文件掩盖。
core_payload_path() {
  local root="$1" rel="$2" base
  for base in "$root" "$root/_internal"; do
    if [ -f "$base/$rel" ]; then
      printf '%s\n' "$base/$rel"
      return 0
    fi
  done
  return 1
}

# 图片工具相关模块的 --hidden-import 参数（按 WITH_IMAGING 拼接）。
# 旧兼容分支（Python < 3.11）装不上 Pillow：imaging.* 若被硬编码进命令，
# PyInstaller 会去收编依赖 PIL 的模块而失败；fileops.naming 只依赖标准库，
# 任何分支都必须收编。
uvmp_init_imaging_args() {
  IMAGING_ARGS=()
  if [ "$WITH_IMAGING" = "1" ]; then
    IMAGING_ARGS=(--hidden-import imaging.engine --hidden-import imaging.service)
  fi
}

# 归档模块清单断言（独立函数，便于在测试里直接重放，不受可执行位等文件系统差异影响）。
# 参数：root exe with_playwright with_imaging
assert_core_modules() {
  local root="$1" exe="$2" with_playwright="$3" with_imaging="${4:-0}" mods listing mod
  listing=$("$PY" -m PyInstaller.utils.cliutils.archive_viewer -r -b "$root/$exe" 2>/dev/null) \
    || { echo "读取核心归档失败（pyi-archive_viewer）: $root/$exe" >&2; return 1; }
  # 纯 Python 包（websocket/pypdf/Cryptodome）进 PYZ 归档，不在磁盘生成同名目录——查归档清单
  mods="apps selftest websocket pypdf Cryptodome"
  if [ "$with_playwright" = "1" ]; then mods="$mods playwright greenlet"; fi
  # PIL 是图片工具（toolkit/imaging）的图像引擎，由独立的 with_imaging 控制（不挂在
  # with_playwright 上）：旧解释器分支两者都没有，现代构建两者都有；混在一起会让
  # 「缺 PIL」随 Playwright 的开关被一起跳过。selfcheck 的导入冒烟按同一口径核对
  # （缺 PIL 时跳过 imaging.* 并报告「图片功能未启用」），这里拦的是连带漏收。
  if [ "$with_imaging" = "1" ]; then mods="$mods PIL"; fi
  for mod in $mods; do
    printf '%s\n' "$listing" | grep -qxE "[[:space:]]*$mod" \
      || { echo "核心归档缺少模块 $mod（PyInstaller 漏收）" >&2; return 1; }
  done
  printf '%s\n' "$mods"
}

# 参数：root exe with_playwright with_imaging
#   with_playwright=1 / with_imaging=1 表示现代构建（Playwright 与 Pillow 各自独立开关）
assert_core_bundle() {
  local root="$1" exe="$2" with_playwright="$3" with_imaging="${4:-0}" rel f mods
  [ -x "$root/$exe" ] || { echo "核心缺少可执行文件 $root/$exe" >&2; exit 1; }
  for rel in apps/pcd_export/parallel.py apps/pcd_export/backend.py \
             install/linux/uvmp-toolkit.service install/linux/polkit.sh; do
    core_payload_path "$root" "$rel" >/dev/null \
      || { echo "核心缺少 ${rel}（根目录与 _internal/ 均无，PyInstaller 收编不完整）" >&2; exit 1; }
  done
  for f in config.ini config.example.json config.initial.json run-usb-agent.vbs \
           course_autoplay_trial.py defect_review_trial.py; do
    core_payload_path "$root" "$f" >/dev/null \
      || { echo "核心缺少 ${f}（根目录与 _internal/ 均无）" >&2; exit 1; }
  done
  mods=$(assert_core_modules "$root" "$exe" "$with_playwright" "$with_imaging") \
    || exit 1
  if [ "$with_playwright" = "1" ]; then
    find "$root" -type d -name playwright | grep -q . \
      || { echo "核心缺少 playwright 包数据目录（-collect-all 漏收）" >&2; exit 1; }
  fi
  echo "核心包完整性：OK（apps/parallel.py、trial 引擎、归档模块 $mods、config/install/vbs）"
}

echo "== 离线安装构建依赖（$WHEELS）=="
if [ "${#REQ_ARGS[@]}" -gt 0 ]; then
  "$PY" -m pip install --no-index --find-links "$WHEELS" --user "${REQ_ARGS[@]}"
else
  "$PY" -m pip install --no-index --find-links "$WHEELS" --user "${BUILD_DEPS[@]}"
fi
# pip --user 装的 pyinstaller 入口在 ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
"$PY" -m pip check || { echo "依赖冲突（pip check 失败），请检查 wheels 目录是否混入了不同 ABI 的同名包" >&2; exit 1; }

# 测试使用 3.10 语法（括号化 with）；旧兼容解释器明确报告跳过而不是假装跑过
PY_OK=$("$PY" -c 'import sys; print(1 if sys.version_info >= (3, 10) else 0)')
if [ "$PY_OK" = "1" ]; then
  echo "== 门禁：Python unittest（按文件分进程，避免容器测试进程累积内存被 OOM kill）=="
  for test_file in tests/test_*.py; do
    echo "-- $test_file"
    "$PY" -m unittest discover -s tests -p "$(basename "$test_file")" -v
  done
  for test_file in packaging/test_*.py; do
    echo "-- $test_file"
    "$PY" -m unittest discover -s packaging -p "$(basename "$test_file")" -v
  done
else
  echo "== 门禁：Python unittest 跳过（解释器 $("$PY" -c 'import sys; print("%d.%d" % sys.version_info[:2])') < 3.10）；完整构建请用 Python 3.11+ =="
fi

uvmp_init_imaging_args
echo "== PyInstaller 构建（onedir，console）=="
# Cryptodome 为 gmssl 的 SM2 依赖（ASN.1 编码），漏收即自动登录 ImportError；
# imaging.engine / imaging.service 由 rpc.py 方法内惰性 import、fileops.naming 由
# 按路径加载的 batch_rename backend import —— 三者对静态分析不可见，必须显式 hidden-import；
# 其中 imaging.* 依赖 Pillow，只在 WITH_IMAGING=1 时由 IMAGING_ARGS 拼进命令
"$PY" -m PyInstaller --name uvmp-core --onedir --clean --noconfirm \
  --add-data "toolkit/apps:apps" \
  --add-data "toolkit/config.example.json:." \
  --add-data "course_autoplay_trial.py:." \
  --add-data "defect_review_trial.py:." \
  "${COLLECT_ARGS[@]}" \
  --hidden-import Cryptodome --hidden-import Cryptodome.Util.asn1 \
  --hidden-import browser_automation \
  --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track \
  --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc \
  --hidden-import usb_agent --hidden-import usb_protocol --hidden-import dev_script_runner \
  --hidden-import cluster --hidden-import cluster.service \
  --hidden-import cluster.identity --hidden-import cluster.registry \
  --hidden-import cluster.protocol --hidden-import cluster.export --hidden-import version \
  --hidden-import scheduler \
  --hidden-import selftest --hidden-import websocket --hidden-import pypdf \
  "${IMAGING_ARGS[@]}" \
  --hidden-import fileops.naming \
  toolkit/app.py

echo "== 装配发布目录 =="
cp config.ini dist/uvmp-core/config.ini
cp toolkit/config.example.json dist/uvmp-core/config.example.json
cp packaging/config.initial.json dist/uvmp-core/config.initial.json
cp packaging/run-usb-agent.vbs dist/uvmp-core/run-usb-agent.vbs
cp README.md dist/uvmp-core/README.md
cp -r install dist/uvmp-core/install
find dist/uvmp-core/install \( -name "*.sh" -o -name "*.timer" -o -name "*.service" \) -exec chmod +x {} +

echo "== 校验：核心包完整性 =="
assert_core_bundle dist/uvmp-core uvmp-core "$WITH_PLAYWRIGHT" "$WITH_IMAGING"

echo "== 冒烟：selfcheck =="
./dist/uvmp-core/uvmp-core selfcheck

echo "== 冒烟：rpc ping =="
# 失败时打印真实响应与退出码：只做 grep 比较会把「空响应」报成无名失败，无从定位
ping_text=$(echo '{"id":1,"method":"ping"}' | ./dist/uvmp-core/uvmp-core rpc) && ping_code=0 || ping_code=$?
if [ "$ping_code" -ne 0 ] || ! printf '%s' "$ping_text" | grep -q '"ok": true'; then
  echo "rpc ping 原始响应（exit=$ping_code）：<$ping_text>" >&2
  exit 1
fi

echo "== 门禁：CDP 冒烟（纯 Python data: 自检）=="
cdp_rc=0
"$PY" packaging/cdp_smoke.py --core dist/uvmp-core/uvmp-core --timeout 240 || cdp_rc=$?
case "$cdp_rc" in
  0) ;;
  2) echo "警告：CDP 冒烟已明确跳过（宿主无可用浏览器）；本次构建未验证 CDP 链路" >&2 ;;
  *) echo "CDP 冒烟失败（exit=$cdp_rc）" >&2; exit 1 ;;
esac

echo "构建完成: dist/uvmp-core/（拷回 Windows 主机后用 build_local.ps1 -LinuxCoreAmd64 打 deb）"
