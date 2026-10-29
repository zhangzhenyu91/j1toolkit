#!/usr/bin/env bash
# Fyne 轨门禁（debian:buster 容器内执行，glibc 2.28 = 麒麟 V10 SP1 同代）：
# glibc/ldd 静态门禁 + GUI/CDP/OCR 真实冒烟
#
# 用法（容器内，工作目录 uvmp-toolkit/）：
#   bash packaging/fyne/gate_smoke.sh <staging-dir> [platform]
# staging-dir 缺省按 platform=dist-fyne/<platform>；platform 缺省按宿主判定
#
# 门禁内容：
#   1. 产物齐全：uvmp-toolkit、uvmp-core、uvmp-export、browser-runtime/electron、
#      browser-runtime/app/{package.json,main.cjs}、models/{det,rec}.onnx、native/libonnxruntime.*
#   2. glibc 符号：staging 内全部 ELF 最高 GLIBC_2.x 需求 ≤ GLIBC_2.28（静态无符号即通过）
#   3. ldd：动态链接产物的依赖全部可解析（含 native/ 与 Electron 运行库；not found 即失败）
#   4. GUI 冒烟：xvfb + UVMP_FYNE_SMOKE=gui（契约：首帧渲染成功自行退出 0，见 packaging/fyne/README.md）
#   5. CDP 冒烟：uvmp-export selftest（UVMP_BROWSER_BIN/APP 指向装配运行时、
#      UVMP_BROWSER_WORKER=--uvmp-browser-mode=export 双模式导出口径；
#      只开 data: 本地页零网络，导航/eval/截图/printToPDF 全链路）
#   6. OCR 冒烟：模型/库齐全 + ldd 通过（第 2、3 步已覆盖）+ UVMP_FYNE_SMOKE=ocr
#      契约调用（GUI 侧 ONNX 推理自检：加载 det/rec 模型对空白图跑通并退出 0；
#      需 GUI 以 -tags onnx 构建，见 build_fyne_core.* 的 UVMP_FYNE_TAGS）
set -euo pipefail
cd "$(dirname "$0")/../.."
fail() { echo "门禁失败: $*" >&2; exit 1; }

STAGING=${1:-}
PLATFORM=${2:-}
if [ -z "$PLATFORM" ]; then
  case "$(uname -s)" in
    Linux)  [ "$(uname -m)" = "aarch64" ] && PLATFORM=linux-arm64 || PLATFORM=linux-amd64 ;;
    *) fail "无法自动判定平台，请显式传入 linux-amd64|linux-arm64" ;;
  esac
fi
[ -n "$STAGING" ] || STAGING="dist-fyne/$PLATFORM"
case "$STAGING" in
  /*|[A-Za-z]:*) ;;
  *) STAGING="$(pwd)/$STAGING" ;;
esac
[ -d "$STAGING" ] || fail "缺暂存目录: $STAGING"

# ---------- 运行环境（buster 源已归档；装 binutils/ldd 与 GUI/浏览器运行库） ----------
if [ -f /etc/debian_version ] && [ "$(cut -d. -f1 /etc/debian_version)" = "10" ]; then
  echo "deb http://archive.debian.org/debian buster main" > /etc/apt/sources.list
  echo "deb http://archive.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list
fi
if command -v apt-get >/dev/null; then
  apt-get -o Acquire::Check-Valid-Until=false update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends -qq \
    binutils file xvfb xauth dbus-x11 x11-apps \
    libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 libcups2 libdrm2 \
    libgbm1 libgtk-3-0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libxkbcommon0 \
    libx11-6 libx11-xcb1 libxcb1 libxext6 libxrender1 libxi6 libxtst6 libxcursor1 \
    libxinerama1 libxxf86vm1 libgl1 libglu1-mesa libpangocairo-1.0-0 libpango-1.0-0 \
    libcairo2 libasound2 libexpat1 libglib2.0-0 libdbus-1-3 >/dev/null
else
  echo "提示: 非 apt 环境，跳过依赖安装（ldd/冒烟依赖本机已有运行库）"
fi

# ---------- 1. 产物齐全 ----------
for f in uvmp-toolkit uvmp-core uvmp-export browser-runtime/electron browser-runtime/app/main.cjs \
         browser-runtime/app/package.json models/det.onnx models/rec.onnx; do
  [ -f "$STAGING/$f" ] || fail "产物缺失: $f"
done
ls "$STAGING/native/libonnxruntime.so."* >/dev/null 2>&1 || fail "产物缺失: native/libonnxruntime.*"
echo "== 产物齐全检查通过 =="

# ---------- 2. glibc 符号（≤ GLIBC_2.28） ----------
is_elf() { [ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ]; }
max_glibc() {
  local f=$1 v=""
  if command -v objdump >/dev/null; then
    v=$(objdump -T "$f" 2>/dev/null | grep -ao 'GLIBC_2\.[0-9]*' | sort -Vu | tail -1 || true)
  fi
  if [ -z "$v" ]; then
    v=$(grep -ao 'GLIBC_2\.[0-9]*' "$f" | sort -Vu | tail -1 || true)
  fi
  echo "$v"
}
check_glibc() {
  local f=$1 max minor
  [ -f "$f" ] || fail "产物缺失: $f"
  if ! is_elf "$f"; then echo "  $f: 非 ELF，跳过"; return; fi
  max=$(max_glibc "$f")
  if [ -z "$max" ]; then echo "  $f: 无 glibc 符号（静态），通过"; return; fi
  minor=${max#GLIBC_2.}
  echo "  $f: 最高依赖 $max"
  [ -n "$minor" ] && [ "$minor" -le 28 ] || fail "glibc 超版本: $f 需 $max（门禁 ≤ GLIBC_2.28）"
}
echo "== glibc 符号门禁（≤ GLIBC_2.28，$(uname -m)）=="
check_glibc "$STAGING/uvmp-toolkit"
check_glibc "$STAGING/uvmp-core"
check_glibc "$STAGING/uvmp-export"
for f in "$STAGING"/native/libonnxruntime.so.*; do
  [ -f "$f" ] && check_glibc "$f"
done
# Electron 运行时全部 ELF
while IFS= read -r -d '' f; do
  check_glibc "$f"
done < <(find "$STAGING/browser-runtime" -type f \( -name 'electron' -o -name '*.so' -o -name '*.so.*' \) -print0)
echo "== glibc 符号门禁通过 =="

# ---------- 3. ldd（依赖全部可解析） ----------
echo "== ldd 门禁（LD_LIBRARY_PATH=native）=="
check_ldd() {
  local f=$1 out
  [ -f "$f" ] || return 0
  is_elf "$f" || return 0
  # 静态二进制 ldd 报 not a dynamic executable，属通过
  out=$(LD_LIBRARY_PATH="$STAGING/native${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ldd "$f" 2>&1 || true)
  if echo "$out" | grep -q "not a dynamic executable"; then
    echo "  $f: 静态，通过"
    return 0
  fi
  if echo "$out" | grep -q "not found"; then
    echo "$out" >&2
    fail "ldd 依赖缺失: $f"
  fi
  echo "  $f: 依赖可解析"
}
check_ldd "$STAGING/uvmp-toolkit"
check_ldd "$STAGING/uvmp-core"
check_ldd "$STAGING/uvmp-export"
for f in "$STAGING"/native/libonnxruntime.so.*; do
  [ -f "$f" ] && check_ldd "$f"
done
check_ldd "$STAGING/browser-runtime/electron"
while IFS= read -r -d '' f; do
  check_ldd "$f"
done < <(find "$STAGING/browser-runtime" -maxdepth 1 -type f -name '*.so*' -print0)
echo "== ldd 门禁通过 =="

# ---------- 4. GUI 冒烟（xvfb + UVMP_FYNE_SMOKE=gui） ----------
# Fyne 只创建末级缓存目录，容器的 ~/.cache 父目录须先存在；dbus-x11 提供主题监听所需启动器。
mkdir -p "${HOME:-/root}/.cache"
echo "== GUI 冒烟（UVMP_FYNE_SMOKE=gui，xvfb）=="
SMOKE_LOG=$(mktemp)
if command -v xvfb-run >/dev/null; then
  set +e
  LIBGL_ALWAYS_SOFTWARE=1 LD_LIBRARY_PATH="$STAGING/native${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
    UVMP_FYNE_SMOKE=gui timeout 180 xvfb-run -a "$STAGING/uvmp-toolkit" >"$SMOKE_LOG" 2>&1
  GUI_RC=$?
  set -e
else
  set +e
  LIBGL_ALWAYS_SOFTWARE=1 LD_LIBRARY_PATH="$STAGING/native${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
    UVMP_FYNE_SMOKE=gui timeout 180 "$STAGING/uvmp-toolkit" >"$SMOKE_LOG" 2>&1
  GUI_RC=$?
  set -e
fi
cat "$SMOKE_LOG" || true
[ "$GUI_RC" -eq 0 ] || fail "GUI 冒烟失败（exit=$GUI_RC）：契约 UVMP_FYNE_SMOKE=gui 须首帧渲染成功后退出 0（见 packaging/fyne/README.md）"
echo "== GUI 冒烟通过 =="

# ---------- 5. CDP 冒烟（uvmp-export selftest，data: 页零网络） ----------
echo "== CDP 冒烟（uvmp-export selftest，装配浏览器运行时）=="
SELFTEST_DIR=$(mktemp -d)
CDP_LOG=$(mktemp)
set +e
UVMP_BROWSER_BIN="$STAGING/browser-runtime/electron" \
UVMP_BROWSER_APP="$STAGING/browser-runtime/app" \
UVMP_BROWSER_WORKER="--uvmp-browser-mode=export" \
UVMP_SELFTEST_DIR="$SELFTEST_DIR" \
  timeout 300 xvfb-run -a "$STAGING/uvmp-export" selftest <<< '{"config":{}}' >"$CDP_LOG" 2>&1
CDP_RC=$?
set -e
cat "$CDP_LOG" || true
[ "$CDP_RC" -eq 0 ] || fail "CDP 冒烟失败（exit=$CDP_RC）：uvmp-export selftest 未通过（装配浏览器运行时链路）"
grep -q '"event"' "$CDP_LOG" || fail "CDP 冒烟失败：selftest stdout 无 NDJSON 事件"
echo "== CDP 冒烟通过 =="

# ---------- 6. OCR 冒烟（UVMP_FYNE_SMOKE=ocr 契约） ----------
echo "== OCR 冒烟（UVMP_FYNE_SMOKE=ocr：ONNX 模型加载 + 空白图推理自检）=="
OCR_LOG=$(mktemp)
set +e
LD_LIBRARY_PATH="$STAGING/native${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
UVMP_FYNE_SMOKE=ocr timeout 180 "$STAGING/uvmp-toolkit" >"$OCR_LOG" 2>&1
OCR_RC=$?
set -e
cat "$OCR_LOG" || true
[ "$OCR_RC" -eq 0 ] || fail "OCR 冒烟失败（exit=$OCR_RC）：契约 UVMP_FYNE_SMOKE=ocr 须加载 models/{det,rec}.onnx 跑通推理后退出 0（见 packaging/fyne/README.md）"
echo "== OCR 冒烟通过 =="

echo "Fyne 轨门禁全部通过（glibc/ldd + GUI/CDP/OCR）: $STAGING"
