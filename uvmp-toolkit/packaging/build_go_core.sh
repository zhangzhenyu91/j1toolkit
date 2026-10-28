#!/usr/bin/env bash
# Go 导出工作进程（uvmp-export）构建：go test / go vet / go build
#
# 用法：bash packaging/build_go_core.sh [<输出文件路径>]
#   缺省输出 dist/uvmp-go/<target>/uvmp-export(.exe)；显式传路径时直接产出到该位置
# 环境：
#   UVMP_GO_TARGET    目标平台 windows-amd64|linux-amd64|linux-arm64（缺省=宿主自动判定）
#   UVMP_GO_PREBUILT  已构建 uvmp-export 二进制路径——直接采用（CI 宿主→容器、离线内网、
#                     上一版复用），不再要求 go 工具链
#
# 约定：
#   - 源码模块 uvmp-toolkit/core-go/（go.mod + cmd/uvmp-export），二进制名 uvmp-export(.exe)，
#     进程协议见《内网工具箱开发指南.md》（stdin 请求/stdout NDJSON，密码不进命令行与日志）
#   - CGO_ENABLED=0 静态构建：麒麟 V10 SP1（glibc 2.28）零 glibc 依赖；
#     若 core-go 引入 cgo 依赖，由 packaging/ci_smoke_gate.sh 的 glibc 门禁暴露
#   - go test / go vet 按【宿主】架构执行（跨平台无法执行目标架构测试——明确说明不冒充），
#     目标架构的可执行性由 CI 麒麟门禁（含 arm64 QEMU）实测
#   - 工具链缺失且无 UVMP_GO_PREBUILT → 明确失败，不静默跳过（产物必须齐全）
set -euo pipefail
cd "$(dirname "$0")/.."
CORE_GO_DIR=core-go

fail() { echo "错误: $*" >&2; exit 1; }

# ---------- 目标平台 ----------
TARGET=${UVMP_GO_TARGET:-}
if [ -z "$TARGET" ]; then
  case "$(uname -s)" in
    Linux)  [ "$(uname -m)" = "aarch64" ] && TARGET=linux-arm64 || TARGET=linux-amd64 ;;
    MINGW*|MSYS*|CYGWIN*) TARGET=windows-amd64 ;;
    *) fail "无法自动判定目标平台，请设 UVMP_GO_TARGET=windows-amd64|linux-amd64|linux-arm64" ;;
  esac
  echo "目标平台自动判定: $TARGET"
fi
case "$TARGET" in
  windows-amd64) GOOS=windows; GOARCH=amd64; BIN=uvmp-export.exe ;;
  linux-amd64)   GOOS=linux;   GOARCH=amd64; BIN=uvmp-export ;;
  linux-arm64)   GOOS=linux;   GOARCH=arm64; BIN=uvmp-export ;;
  *) fail "不支持的目标平台: $TARGET（可选 windows-amd64|linux-amd64|linux-arm64）" ;;
esac
OUT=${1:-dist/uvmp-go/$TARGET/$BIN}
case "$OUT" in
  /*|[A-Za-z]:*) ;;                       # 已是绝对路径
  *) OUT="$(pwd)/$OUT" ;;                 # 归一化为绝对路径（core-go 子目录构建时不受影响）
esac

# ---------- 通道：复用已构建二进制 ----------
if [ -n "${UVMP_GO_PREBUILT:-}" ]; then
  SRC=$UVMP_GO_PREBUILT
  [ -f "$SRC" ] || fail "UVMP_GO_PREBUILT 不存在: $SRC"
  if [ "$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$SRC")" \
     = "$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$OUT")" ]; then
    echo "UVMP_GO_PREBUILT 即输出文件，仅校验: $OUT"
  else
    mkdir -p "$(dirname "$OUT")"
    cp -f "$SRC" "$OUT"
    echo "采用预构建 uvmp-export: $SRC -> $OUT"
  fi
  [ -f "$OUT" ] || fail "预构建产物缺失: $OUT"
  exit 0
fi

# ---------- 源码与工具链检查（缺失即失败）----------
[ -f "$CORE_GO_DIR/go.mod" ] || fail "缺少 $CORE_GO_DIR/go.mod（Go 导出工作进程源码模块未就位）"
[ -d "$CORE_GO_DIR/cmd/uvmp-export" ] || fail "缺少 $CORE_GO_DIR/cmd/uvmp-export（导出工作进程入口）"
command -v go >/dev/null || fail "未找到 go 工具链：请安装 core-go/go.mod 声明版本的 Go，或以 UVMP_GO_PREBUILT 指入已构建的 $BIN"
command -v python3 >/dev/null || fail "需要 python3（路径归一化）"
go version

# ---------- 构建 ----------
HOST_TARGET=""
case "$(uname -s)" in
  Linux)  [ "$(uname -m)" = "aarch64" ] && HOST_TARGET=linux-arm64 || HOST_TARGET=linux-amd64 ;;
  MINGW*|MSYS*|CYGWIN*) HOST_TARGET=windows-amd64 ;;
esac
if [ "$TARGET" = "$HOST_TARGET" ]; then
  echo "== go vet / go test（宿主=$HOST_TARGET）=="
else
  echo "== go vet / go test（按宿主架构执行；跨平台不执行 $TARGET 目标架构测试，由 CI 麒麟门禁实测）=="
fi
mkdir -p "$(dirname "$OUT")"
(
  cd "$CORE_GO_DIR"
  go vet ./...
  go test ./...
  echo "== go build（$TARGET，CGO_ENABLED=0 静态）=="
  CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -o "$OUT" ./cmd/uvmp-export
)
[ -f "$OUT" ] || fail "构建产物缺失: $OUT"
chmod +x "$OUT" 2>/dev/null || true
echo "构建完成: $OUT"
