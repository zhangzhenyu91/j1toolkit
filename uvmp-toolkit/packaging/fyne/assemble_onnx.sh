#!/usr/bin/env bash
# Fyne 轨 OCR 运行时装配：按 packaging/fyne/assets/onnx-assets.json 校验 SHA256 并装入
# onnxruntime 库（native/）与 PP-OCRv4 模型（models/）（构建机执行，目标机不联网）
#
# 用法：
#   bash packaging/fyne/assemble_onnx.sh [<platform>] [<outdir>]
# platform ∈ windows-amd64 | linux-amd64 | linux-arm64（缺省按宿主自动判定）
# outdir 缺省 dist-fyne/<platform>（装入 <outdir>/native 与 <outdir>/models）
#
# 环境：
#   UVMP_ORT_DIR          onnxruntime-node 的 napi-v3 目录
#                         （缺省 desktop/node_modules/onnxruntime-node/bin/napi-v3，npm ci 后就位）
#   UVMP_ORT_ARCHIVE      onnxruntime-node-<ver>.tgz——UVMP_ORT_DIR 缺失时从 tgz 解出 napi-v3
#   UVMP_ORT_MODELS_DIR   模型源目录（缺省 desktop/electron/quizsearch/models，随仓）
#
# 纪律（以 packaging/fyne/assets/onnx-assets.json 为准）：
#   - 每个文件 SHA256 必须与清单一致（清单为空视为未验资产），不匹配即失败，禁止编造
#   - Windows 只装 onnxruntime.dll（CPU EP，DirectML.dll 不随包）；Linux 装
#     libonnxruntime.so.1.20.1 + 同内容 libonnxruntime.so.1 副本（soname 兼容）
#   - glibc 门禁（≤ GLIBC_2.28）在 packaging/fyne/gate_smoke.sh 统一执行
set -euo pipefail
cd "$(dirname "$0")/../.."
MANIFEST=packaging/fyne/assets/onnx-assets.json

fail() { echo "错误: $*" >&2; exit 1; }

# ---------- 参数 ----------
PLATFORM=${1:-}
OUTDIR=${2:-}
if [ -z "$PLATFORM" ]; then
  case "$(uname -s)" in
    Linux)  [ "$(uname -m)" = "aarch64" ] && PLATFORM=linux-arm64 || PLATFORM=linux-amd64 ;;
    MINGW*|MSYS*|CYGWIN*) PLATFORM=windows-amd64 ;;
    *) fail "无法自动判定平台，请显式传入 windows-amd64|linux-amd64|linux-arm64" ;;
  esac
  echo "平台自动判定: $PLATFORM"
fi
case "$PLATFORM" in
  windows-amd64|linux-amd64|linux-arm64) ;;
  *) fail "不支持的平台: $PLATFORM（可选 windows-amd64|linux-amd64|linux-arm64）" ;;
esac
[ -n "$OUTDIR" ] || OUTDIR="dist-fyne/$PLATFORM"
case "$OUTDIR" in
  /*|[A-Za-z]:*) ;;
  *) OUTDIR="$(pwd)/$OUTDIR" ;;
esac

command -v python3 >/dev/null || fail "需要 python3 解析 $MANIFEST 与校验"

# ---------- 库来源定位 ----------
ORT_DIR=${UVMP_ORT_DIR:-desktop/node_modules/onnxruntime-node/bin/napi-v3}
if [ ! -d "$ORT_DIR" ] && [ -n "${UVMP_ORT_ARCHIVE:-}" ]; then
  [ -f "$UVMP_ORT_ARCHIVE" ] || fail "UVMP_ORT_ARCHIVE 不存在: $UVMP_ORT_ARCHIVE"
  TMP_ORT=$(mktemp -d)
  trap 'rm -rf "$TMP_ORT"' EXIT
  python3 - "$UVMP_ORT_ARCHIVE" "$TMP_ORT" <<'PY'
import os, sys, tarfile
src, dst = sys.argv[1], sys.argv[2]
with tarfile.open(src) as t:
    for m in t.getmembers():
        # 只解 napi-v3 预编译库，剥掉 package/ 前缀
        name = m.name.replace("\\", "/")
        if "/bin/napi-v3/" not in name or m.isdir():
            continue
        rel = name.split("/bin/napi-v3/", 1)[1]
        out = os.path.join(dst, "napi-v3", rel.replace("/", os.sep))
        os.makedirs(os.path.dirname(out), exist_ok=True)
        with t.extractfile(m) as fi, open(out, "wb") as fo:
            fo.write(fi.read())
print("从 tgz 解出 napi-v3:", src)
PY
  ORT_DIR="$TMP_ORT/napi-v3"
fi
[ -d "$ORT_DIR" ] || fail "缺少 onnxruntime 库目录: $ORT_DIR（npm ci 后就位，或用 UVMP_ORT_DIR/UVMP_ORT_ARCHIVE 指入）"
MODELS_DIR=${UVMP_ORT_MODELS_DIR:-desktop/electron/quizsearch/models}
[ -d "$MODELS_DIR" ] || fail "缺少模型目录: $MODELS_DIR"

sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | awk '{print $1}'
  else python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$1"; fi
}

verify_and_copy() { # $1=源文件 $2=期望 sha256 $3=目标文件
  local src=$1 want=$2 dst=$3 actual
  [ -f "$src" ] || fail "装配源缺失: $src"
  actual=$(sha256_of "$src")
  [ "$actual" = "$want" ] || fail "SHA256 不匹配: $src（实际 $actual，清单 $want）——禁止装配未验资产"
  mkdir -p "$(dirname "$dst")"
  cp -f "$src" "$dst"
  echo "  装入 $dst（$actual）"
}

# ---------- 按清单装配（python 读清单，bash 执行校验与拷贝） ----------
python3 - "$PLATFORM" "$MANIFEST" <<'PY' > /tmp/uvmp-fyne-onnx-plan.$$
import json, sys
plat, path = sys.argv[1], sys.argv[2]
m = json.load(open(path, encoding="utf-8"))
for e in m.get("libraries", {}).get(plat, []):
    if e.get("required", True):
        print("lib\t%s\t%s\t%s" % (e["source"], e["sha256"], e["dest"]))
for e in m.get("models", []):
    print("model\t%s\t%s\t%s" % (e["source"], e["sha256"], e["dest"]))
PY

COUNT=0
while IFS=$'\t' read -r kind src want dst; do
  [ -n "${kind:-}" ] || continue
  case "$kind" in
    lib)   base="$ORT_DIR/$src" ;;
    model) base="$MODELS_DIR/$src" ;;
    *) fail "清单条目类型未知: $kind" ;;
  esac
  verify_and_copy "$base" "$want" "$OUTDIR/$dst"
  COUNT=$((COUNT + 1))
done < /tmp/uvmp-fyne-onnx-plan.$$
rm -f /tmp/uvmp-fyne-onnx-plan.$$
[ "$COUNT" -gt 0 ] || fail "清单无可装配条目（$PLATFORM）"
echo "OCR 运行时装配完成：$COUNT 个文件 -> $OUTDIR（native/ + models/）"
