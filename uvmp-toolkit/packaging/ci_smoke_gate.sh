#!/usr/bin/env bash
# 麒麟门禁：uvmp-export + 内置浏览器的 ldd/glibc 符号检查与真实可执行冒烟
#
# 在 debian:buster（glibc 2.28，麒麟 V10 SP1 同代）容器内执行；arm64 架构经 QEMU 用
# arm64v8/debian:buster 跑同一脚本（CI 已挂 QEMU，arm64 可真实执行）。
# 用法：bash packaging/ci_smoke_gate.sh <core-dir> <browser-dir>
#   例：bash packaging/ci_smoke_gate.sh dist-core/core dist-browser/browser
#
# 门禁内容：
#   1. 产物齐全：uvmp-export 与浏览器主可执行文件必须存在（缺失即构建失败）
#   2. glibc 符号：全部 ELF 的最高 GLIBC_2.x 需求 ≤ GLIBC_2.28（静态二进制无符号即通过）
#   3. ldd：动态库全部可解析（容器内补齐浏览器运行库后判定，not found 即失败）
#   4. 真实可执行冒烟：chrome --version 必须退出 0 且输出版本号；uvmp-export 以
#      空导出清单请求（codes=[]，零网络访问）跑 run，30 秒内退出且 stdout 输出 NDJSON 事件
set -euo pipefail

CORE_DIR=${1:?用法: ci_smoke_gate.sh <core-dir> <browser-dir>}
BROWSER_DIR=${2:?用法: ci_smoke_gate.sh <core-dir> <browser-dir>}

fail() { echo "门禁失败: $*" >&2; exit 1; }

# ---------- 运行环境（buster 源已归档；装 binutils/ldd 与浏览器运行库）----------
if [ -f /etc/debian_version ] && [ "$(cut -d. -f1 /etc/debian_version)" = "10" ]; then
  echo "deb http://archive.debian.org/debian buster main" > /etc/apt/sources.list
  echo "deb http://archive.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list
fi
if command -v apt-get >/dev/null; then
  apt-get -o Acquire::Check-Valid-Until=false update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends -qq \
    binutils file \
    libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 libcups2 libdrm2 \
    libgbm1 libgtk-3-0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libxkbcommon0 \
    libx11-6 libx11-xcb1 libxcb1 libxext6 libxrender1 libxi6 libxtst6 libxcursor1 \
    libpangocairo-1.0-0 libpango-1.0-0 libcairo2 libasound2 libexpat1 libglib2.0-0 \
    libdbus-1-3 >/dev/null
else
  echo "提示: 非 apt 环境，跳过依赖安装（ldd/冒烟依赖本机已有运行库）"
fi

is_elf() { [ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ]; }

max_glibc() { # ELF 最高 GLIBC_2.x 需求（objdump -T 精确；无 binutils 回退符号串扫描）
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
  if ! is_elf "$f"; then echo "  $f: 非 ELF，跳过 glibc 检查"; return; fi
  max=$(max_glibc "$f")
  if [ -z "$max" ]; then
    echo "  $f: 无 glibc 符号（静态链接），通过"
    return
  fi
  minor=${max#GLIBC_2.}
  echo "  $f: 最高依赖 $max"
  [ "$minor" -le 28 ] || fail "$f 依赖 $max > GLIBC_2.28（麒麟 V10 SP1 不可运行）"
}

check_ldd() {
  local f=$1 out
  if ! is_elf "$f"; then return; fi
  out=$(ldd "$f" 2>&1 || true)
  case "$out" in
    *"not a dynamic executable"*) echo "  $f: 静态链接，ldd 通过" ; return ;;
  esac
  if echo "$out" | grep -q "not found"; then
    echo "$out" >&2
    fail "$f 存在未解析动态库（ldd not found）"
  fi
  echo "  $f: ldd 全部可解析"
}

echo "== 1) 产物齐全 =="
EXPORT_BIN="$CORE_DIR/uvmp-export"
CHROME_BIN="$BROWSER_DIR/chrome"
[ -f "$EXPORT_BIN" ] || fail "缺失 $EXPORT_BIN（Go 导出工作进程未装配进核心目录）"
[ -f "$CHROME_BIN" ] || fail "缺失 $CHROME_BIN（内置浏览器未装配）"
[ -f "$BROWSER_DIR/uvmp-browser.json" ] || fail "缺失 $BROWSER_DIR/uvmp-browser.json（浏览器资产元数据）"
echo "  $EXPORT_BIN / $CHROME_BIN 就位"

echo "== 2) glibc 符号门禁（≤ GLIBC_2.28）=="
check_glibc "$EXPORT_BIN"
while IFS= read -r f; do
  check_glibc "$f"
done < <(find "$BROWSER_DIR" -type f | sort)

echo "== 3) ldd 解析门禁 =="
check_ldd "$EXPORT_BIN"
while IFS= read -r f; do
  check_ldd "$f"
done < <(find "$BROWSER_DIR" -type f | sort)

echo "== 4) 真实可执行冒烟 =="
chmod +x "$EXPORT_BIN" "$CHROME_BIN" 2>/dev/null || true
CHROME_VER=$("$CHROME_BIN" --version 2>&1) || fail "浏览器 --version 执行失败: $CHROME_VER"
echo "  $CHROME_BIN --version => $CHROME_VER"
echo "$CHROME_VER" | grep -Eq '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' || fail "浏览器 --version 未输出版本号"
SMOKE_LOG=$(mktemp)
trap 'rm -f "$SMOKE_LOG"' EXIT
# 空导出清单（codes=[]）零网络访问：只要求进程可执行、按协议吐 NDJSON 事件并在超时前退出
printf '%s\n' '{"config":{},"params":{},"codes":[]}' \
  | timeout 30 "$EXPORT_BIN" run >"$SMOKE_LOG" 2>"$SMOKE_LOG.err" \
  || { code=$?; [ "$code" -eq 124 ] && fail "uvmp-export 冒烟超时（30s）"; echo "  uvmp-export 退出码 $code（协议允许 error 终态）"; }
echo "---- uvmp-export stdout ----"; cat "$SMOKE_LOG"
echo "---- uvmp-export stderr ----"; cat "$SMOKE_LOG.err" 2>/dev/null || true
grep -q '"event"' "$SMOKE_LOG" || fail "uvmp-export 冒烟失败：stdout 无 NDJSON 事件"
echo "门禁全部通过"
