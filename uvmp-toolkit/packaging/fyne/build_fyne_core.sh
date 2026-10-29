#!/usr/bin/env bash
# Fyne 轨核心构建（Linux amd64/arm64，debian:buster 容器内原生执行）：Go+Fyne CGO 构建
# uvmp-toolkit + Go 核心 CLI uvmp-core + Go 导出工作进程 uvmp-export，产出到 dist-fyne/<platform>/
#
# 用法（容器内，工作目录 uvmp-toolkit/）：
#   bash packaging/fyne/build_fyne_core.sh [<platform>]
# platform ∈ linux-amd64 | linux-arm64（缺省按宿主自动判定）；
# Windows 不走本脚本（CGO 工具链不同）：用 packaging/fyne/build_fyne_core.ps1（MSYS2 mingw-w64）
#
# 环境：
#   UVMP_FYNE_MOD     Fyne GUI 所在 Go 模块目录（缺省 core-go，与 uvmp-export 同模块；
#                     go.mod 的 fyne 依赖由 GUI 源码侧维护，本脚本不改 go.mod）
#   UVMP_FYNE_PKG     Fyne GUI 包路径（缺省 ./cmd/uvmp-toolkit，即 core-go/cmd/uvmp-toolkit）
#   UVMP_FYNE_BIN     产物二进制名（缺省 uvmp-toolkit，与 Electron 轨 executableName 同名）
#   UVMP_FYNE_TAGS    go build -tags 值（缺省 onnx：OCR 冒烟需要 ONNX 实现——
#                     internal/ocr/ort 置于 //go:build onnx 之后、yalue/onnxruntime_go 为 CGO 包，
#                     见 core-go/internal/ocr/ort/doc.go；显式设空则不带标签构建、OCR 冒烟不可用）
#   UVMP_FYNE_SKIP_EXPORT=1  只构建 GUI（uvmp-export 另行构建时）
#   UVMP_GO_PREBUILT  已构建 uvmp-export 路径，透传 packaging/build_go_core.sh
#   UVMP_GO_VERSION   Go 工具链版本（缺省取 UVMP_FYNE_MOD/go.mod 的 go 指令值）
#
# 依赖（脚本自装，buster 源已归档走 archive.debian.org）：
#   gcc / pkg-config / libgl1-mesa-dev / libglu1-mesa-dev / xorg-dev（Fyne CGO：GLFW+OpenGL+X11 头）
#   libwayland-dev / libxkbcommon-dev（新版 go-gl/glfw 同时编译 Wayland 支持，缺头文件即失败）
#   Go 工具链（缺失时按模块 go.mod 版本直连 go.dev 下载）
#
# 纪律：
#   - GUI 入口缺失即明确失败（点名 UVMP_FYNE_PKG 契约），不静默跳过、不出残缺产物
#   - CGO_ENABLED=1（Fyne 必须）且不做交叉编译（跨架构 CGO 需交叉 gcc，本产线一律原生构建；
#     arm64 在 ubuntu-24.04-arm + arm64v8/debian:buster 原生容器执行）
#   - go vet / go test 按宿主执行；不冒充跑过目标架构测试
#   - uvmp-export 保持 CGO_ENABLED=0（复用 packaging/build_go_core.sh）
set -euo pipefail
cd "$(dirname "$0")/../.."
fail() { echo "错误: $*" >&2; exit 1; }

# ---------- 目标平台 ----------
PLATFORM=${1:-}
if [ -z "$PLATFORM" ]; then
  case "$(uname -s)" in
    Linux)  [ "$(uname -m)" = "aarch64" ] && PLATFORM=linux-arm64 || PLATFORM=linux-amd64 ;;
    *) fail "无法自动判定平台，请显式传入 linux-amd64|linux-arm64（Windows 用 build_fyne_core.ps1）" ;;
  esac
  echo "平台自动判定: $PLATFORM"
fi
case "$PLATFORM" in
  linux-amd64) GOOS=linux; GOARCH=amd64; GUI_BIN=uvmp-toolkit; EXPORT_BIN=uvmp-export; CORE_BIN=uvmp-core ;;
  linux-arm64) GOOS=linux; GOARCH=arm64; GUI_BIN=uvmp-toolkit; EXPORT_BIN=uvmp-export; CORE_BIN=uvmp-core ;;
  windows-amd64) fail "windows-amd64 请用 packaging/fyne/build_fyne_core.ps1（MSYS2 mingw-w64 CGO）" ;;
  *) fail "不支持的平台: $PLATFORM（可选 linux-amd64|linux-arm64）" ;;
esac

MOD=${UVMP_FYNE_MOD:-core-go}
PKG=${UVMP_FYNE_PKG:-./cmd/uvmp-toolkit}
BIN_NAME=${UVMP_FYNE_BIN:-$GUI_BIN}
TAGS=${UVMP_FYNE_TAGS-onnx}
OUTDIR="$(pwd)/dist-fyne/$PLATFORM"
mkdir -p "$OUTDIR"

# ---------- Fyne GUI 源码契约（缺失即失败） ----------
[ -f "$MOD/go.mod" ] || fail "缺少 $MOD/go.mod（Fyne GUI 所在 Go 模块未就位；UVMP_FYNE_MOD 可改指）"
if [ ! -d "$MOD/${PKG#./}" ]; then
  fail "缺少 Fyne GUI 入口 $MOD/${PKG#./}（契约：UVMP_FYNE_PKG 缺省 ./cmd/uvmp-toolkit，可改指；GUI 源码与 go.mod fyne 依赖由 GUI 侧落位后本脚本才可构建）"
fi
# onnx 构建标签工具链预检：ORT 实现（internal/ocr/ort）在 //go:build onnx 之后、依赖
# yalue/onnxruntime_go（CGO 包），缺失时 go build 报错难读，这里点名补依赖的出处
case ",$TAGS," in
  *,onnx,*)
    grep -q 'github.com/yalue/onnxruntime_go' "$MOD/go.mod" \
      || fail "构建标签含 onnx 但 $MOD/go.mod 未含 github.com/yalue/onnxruntime_go（启用步骤见 core-go/internal/ocr/ort/doc.go；或设 UVMP_FYNE_TAGS 关闭 onnx）"
    ;;
esac

# ---------- 系统依赖（仅 apt 环境自装；容器 debian:buster 为主场景） ----------
if [ -f /etc/debian_version ] && [ "$(cut -d. -f1 /etc/debian_version)" = "10" ]; then
  echo "deb http://archive.debian.org/debian buster main" > /etc/apt/sources.list
  echo "deb http://archive.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list
fi
if command -v apt-get >/dev/null; then
  apt-get -o Acquire::Check-Valid-Until=false update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends -qq \
    gcc pkg-config make ca-certificates curl xz-utils python3 \
    libgl1-mesa-dev libglu1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev >/dev/null
else
  echo "提示: 非 apt 环境，假定 gcc/pkg-config/python3/X11·GL/Wayland 开发头已就位"
fi
command -v gcc >/dev/null || fail "缺少 C 编译器 gcc（Fyne CGO 必需）"
command -v pkg-config >/dev/null || fail "缺少 pkg-config（Fyne CGO 探测 GL/X11/Wayland 需要）"
pkg-config --exists gl x11 || fail "pkg-config 探测 gl/x11 失败：缺 libgl1-mesa-dev / xorg-dev"
pkg-config --exists wayland-client xkbcommon || fail "pkg-config 探测 Wayland 失败：缺 libwayland-dev / libxkbcommon-dev"

# ---------- Go 工具链（缺失即按模块 go.mod 版本下载） ----------
if ! command -v go >/dev/null; then
  GOVER=${UVMP_GO_VERSION:-}
  if [ -z "$GOVER" ]; then
    GOVER=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$MOD/go.mod" | head -1)
  fi
  [ -n "$GOVER" ] || fail "无法确定 Go 版本（$MOD/go.mod 无 go 指令）；可设 UVMP_GO_VERSION"
  mkdir -p /opt/go
  curl -fSL "https://go.dev/dl/go${GOVER}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz \
    || fail "Go 工具链下载失败（go${GOVER}.linux-${GOARCH}）"
  tar -C /opt/go --strip-components=1 -xzf /tmp/go.tar.gz
  export PATH=/opt/go/bin:$PATH
fi
go version

# ---------- CGO 交叉守卫（本产线一律原生构建） ----------
HOST_ARCH=$(go env GOHOSTARCH)
HOST_OS=$(go env GOHOSTOS)
if [ "$HOST_OS" != "$GOOS" ] || [ "$HOST_ARCH" != "$GOARCH" ]; then
  fail "Fyne CGO 不做交叉编译（宿主 $HOST_OS/$HOST_ARCH ≠ 目标 $GOOS/$GOARCH）：请在目标架构原生构建机/容器执行（arm64 用 ubuntu-24.04-arm + arm64v8/debian:buster）"
fi

# ---------- 构建 Fyne GUI（CGO，原生；默认带 onnx 标签启用 ONNX 推理实现） ----------
echo "== go vet / go test（宿主 $HOST_OS/$HOST_ARCH）=="
(
  cd "$MOD"
  go vet ./...
  go test ./...
  echo "== go build（$PLATFORM，CGO_ENABLED=1，tags=${TAGS:-无}）$PKG =="
  if [ -n "$TAGS" ]; then
    CGO_ENABLED=1 go build -trimpath -tags "$TAGS" -o "$OUTDIR/$BIN_NAME" "$PKG"
  else
    CGO_ENABLED=1 go build -trimpath -o "$OUTDIR/$BIN_NAME" "$PKG"
  fi
) || fail "Fyne GUI 构建失败"
[ -f "$OUTDIR/$BIN_NAME" ] || fail "构建产物缺失: $OUTDIR/$BIN_NAME"
chmod +x "$OUTDIR/$BIN_NAME" 2>/dev/null || true
echo "Fyne GUI 构建完成: $OUTDIR/$BIN_NAME"

# ---------- 构建 uvmp-core（Go 核心 CLI/RPC，CGO_ENABLED=0，随包装入供命令行/排障） ----------
[ -d "$MOD/cmd/uvmp-core" ] || fail "缺少 $MOD/cmd/uvmp-core（Go 核心 CLI 入口）"
(
  cd "$MOD"
  CGO_ENABLED=0 go build -trimpath -o "$OUTDIR/$CORE_BIN" ./cmd/uvmp-core
) || fail "uvmp-core 构建失败"
[ -f "$OUTDIR/$CORE_BIN" ] || fail "uvmp-core 构建产物缺失: $OUTDIR/$CORE_BIN"
chmod +x "$OUTDIR/$CORE_BIN" 2>/dev/null || true
echo "uvmp-core 构建完成: $OUTDIR/$CORE_BIN"

# ---------- 构建 uvmp-export（复用既有产线，CGO_ENABLED=0） ----------
if [ "${UVMP_FYNE_SKIP_EXPORT:-}" = "1" ]; then
  echo "跳过 uvmp-export 构建（UVMP_FYNE_SKIP_EXPORT=1）"
else
  UVMP_GO_TARGET=$PLATFORM bash packaging/build_go_core.sh "$OUTDIR/$EXPORT_BIN"
  [ -f "$OUTDIR/$EXPORT_BIN" ] || fail "uvmp-export 构建产物缺失: $OUTDIR/$EXPORT_BIN"
  echo "uvmp-export 构建完成: $OUTDIR/$EXPORT_BIN"
fi

echo "Fyne 轨核心构建完成: $OUTDIR"
