#!/usr/bin/env bash
# 内置浏览器资产：下载 / SHA256 校验 / 装配（构建机执行；目标机不联网下载）
#
# 用法：
#   bash packaging/browser_assets.sh [<platform>] [<outdir>]
#   bash packaging/browser_assets.sh --record-sha <platform>   # 下载实测 SHA256 并写回清单（发版前校准）
# platform ∈ windows-amd64 | linux-amd64 | linux-arm64（缺省按宿主自动判定）
# outdir 缺省 dist-browser/browser（最终随 electron-builder 进 resources/browser/）
#
# 环境：
#   UVMP_BROWSER_ARCHIVE  本地归档包（zip/tar.gz）路径——跳过下载，仍按清单 SHA256 校验
#   UVMP_BROWSER_DIR      本地已解包目录——跳过下载与归档校验，直接装配（校验主可执行文件存在）
#   UVMP_BROWSER_CACHE    下载缓存目录（缺省 packaging/.browser-cache）
#
# 纪律（以 packaging/browser-assets.json 为准）：
#   - 归档 SHA256 未记录（空）→ 一律失败；发版前先 --record-sha 实测写回，或用 UVMP_BROWSER_DIR 通道
#   - 资产缺失（无 URL 且无本地通道）→ 失败，禁止编造地址（linux-arm64 即此状态）
#   - 装配产物：outdir 内为剥掉 zip 单一顶层目录后的全部内容，主可执行文件 chrome(.exe)
#     位于 outdir 根，另写 outdir/uvmp-browser.json 元数据（路径全 ASCII）
set -euo pipefail
cd "$(dirname "$0")/.."
MANIFEST=packaging/browser-assets.json
CACHE_DIR=${UVMP_BROWSER_CACHE:-packaging/.browser-cache}

fail() { echo "错误: $*" >&2; exit 1; }

# ---------- 参数 ----------
MODE=assemble
if [ "${1:-}" = "--record-sha" ]; then MODE=record; shift; fi
PLATFORM=${1:-}
OUTDIR=${2:-dist-browser/browser}

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

command -v python3 >/dev/null || fail "需要 python3 解析 $MANIFEST"

# ---------- 读清单 ----------
read_manifest() {
  python3 - "$PLATFORM" "$MANIFEST" <<'PY'
import json, sys
plat, path = sys.argv[1], sys.argv[2]
m = json.load(open(path, encoding="utf-8"))
a = m["assets"].get(plat)
if a is None:
    sys.exit("清单无平台条目: " + plat)
for v in (a.get("url", ""), a.get("archive_type", "zip"), a.get("archive_sha256", ""),
          a.get("executable", ""), m.get("product", {}).get("version", "")):
    print(v)
PY
}
MANIFEST_OUT=$(read_manifest) || fail "读取 $MANIFEST 失败"
URL=$(sed -n 1p <<<"$MANIFEST_OUT")
ARCHIVE_TYPE=$(sed -n 2p <<<"$MANIFEST_OUT")
SHA=$(sed -n 3p <<<"$MANIFEST_OUT")
EXE=$(sed -n 4p <<<"$MANIFEST_OUT")
VERSION=$(sed -n 5p <<<"$MANIFEST_OUT")
[ -n "$EXE" ] || fail "清单缺 executable 字段（$PLATFORM）"

sha256_of() { sha256sum "$1" | awk '{print $1}'; }

write_metadata() { # $1=输出目录 $2=来源说明 $3=实际 sha256
  python3 - "$1" "$PLATFORM" "$VERSION" "$3" "$2" "$EXE" <<'PY'
import json, sys
out, plat, ver, sha, source, exe = sys.argv[1:7]
meta = {"platform": plat, "version": ver, "archive_sha256": sha,
        "source": source, "executable": exe}
with open(out + "/uvmp-browser.json", "w", encoding="utf-8") as f:
    json.dump(meta, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY
}

same_dir() { # 同一目录判定（UVMP_BROWSER_DIR 指向输出目录本身 = 仅校验）
  [ "$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$1")" \
   = "$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$2")" ]
}

verify_exe() { [ -f "$1/$EXE" ] || fail "缺少主可执行文件: $1/$EXE"; }

copy_tree() { # $1=源目录 $2=输出目录
  rm -rf "$2"
  mkdir -p "$2"
  cp -a "$1/." "$2/"
}

# ---------- --record-sha：下载/取归档 → 实测 SHA256 写回清单 ----------
if [ "$MODE" = "record" ]; then
  ARCHIVE=${UVMP_BROWSER_ARCHIVE:-}
  if [ -z "$ARCHIVE" ]; then
    [ -n "$URL" ] || fail "$PLATFORM 清单无 URL 且未设 UVMP_BROWSER_ARCHIVE，无法记录 SHA256"
    mkdir -p "$CACHE_DIR"
    ARCHIVE="$CACHE_DIR/$(basename "$URL")"
    if [ ! -f "$ARCHIVE" ]; then
      echo "下载 $URL ..."
      curl -fSL --retry 3 -o "$ARCHIVE" "$URL" || fail "下载失败: $URL"
    fi
  fi
  [ -f "$ARCHIVE" ] || fail "归档不存在: $ARCHIVE"
  NEW_SHA=$(sha256_of "$ARCHIVE")
  python3 - "$PLATFORM" "$MANIFEST" "$NEW_SHA" <<'PY'
import json, sys
plat, path, sha = sys.argv[1:4]
m = json.load(open(path, encoding="utf-8"))
m["assets"][plat]["archive_sha256"] = sha
with open(path, "w", encoding="utf-8") as f:
    json.dump(m, f, ensure_ascii=False, indent=2)
    f.write("\n")
print("已写回 %s: archive_sha256=%s" % (plat, sha))
PY
  exit 0
fi

# ---------- 装配 ----------
# 通道 1：本地已解包目录（信任构建者；来源即输出目录时仅校验）
if [ -n "${UVMP_BROWSER_DIR:-}" ]; then
  SRC_DIR=$UVMP_BROWSER_DIR
  [ -d "$SRC_DIR" ] || fail "UVMP_BROWSER_DIR 不是目录: $SRC_DIR"
  verify_exe "$SRC_DIR"
  if same_dir "$SRC_DIR" "$OUTDIR"; then
    echo "UVMP_BROWSER_DIR 即输出目录，仅校验: $OUTDIR/$EXE"
  else
    echo "按本地目录装配: $SRC_DIR -> $OUTDIR"
    copy_tree "$SRC_DIR" "$OUTDIR"
    verify_exe "$OUTDIR"
  fi
  echo "浏览器资产装配完成（目录通道）: $OUTDIR/$EXE"
  exit 0
fi

# 通道 2/3：本地归档或按 URL 下载（均须清单 SHA256 已记录；未验资产在下载前即失败）
[ -n "$SHA" ] || fail "$PLATFORM 的 archive_sha256 未记录（未验资产不得装配）：发版前先执行 bash packaging/browser_assets.sh --record-sha $PLATFORM 实测写回，或用 UVMP_BROWSER_DIR 通道"
ARCHIVE=${UVMP_BROWSER_ARCHIVE:-}
if [ -n "$ARCHIVE" ]; then
  [ -f "$ARCHIVE" ] || fail "UVMP_BROWSER_ARCHIVE 不存在: $ARCHIVE"
else
  [ -n "$URL" ] || fail "$PLATFORM 清单无 URL 且未设 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR（资产缺失即构建失败，见 $MANIFEST status 说明）"
  mkdir -p "$CACHE_DIR"
  ARCHIVE="$CACHE_DIR/$(basename "$URL")"
  if [ -f "$ARCHIVE" ]; then
    echo "命中下载缓存: $ARCHIVE"
  else
    echo "下载 $URL ..."
    curl -fSL --retry 3 -o "$ARCHIVE" "$URL" || fail "下载失败: $URL"
  fi
fi

ACTUAL=$(sha256_of "$ARCHIVE")
[ "$ACTUAL" = "$SHA" ] || fail "SHA256 不符: 清单=$SHA 实测=$ACTUAL（$ARCHIVE）"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
case "$ARCHIVE_TYPE" in
  zip)    command -v unzip >/dev/null || fail "需要 unzip 解包 $ARCHIVE"; unzip -q -o "$ARCHIVE" -d "$TMP" ;;
  tar.gz) tar -xzf "$ARCHIVE" -C "$TMP" ;;
  *) fail "不支持的 archive_type: $ARCHIVE_TYPE" ;;
esac
# 单一顶层目录剥壳（chrome-linux64/、chrome-win64/ 等），内容直落装配根
shopt -s nullglob
ENTRIES=("$TMP"/*)
shopt -u nullglob
SRC="$TMP"
[ "${#ENTRIES[@]}" -eq 1 ] && [ -d "${ENTRIES[0]}" ] && SRC="${ENTRIES[0]}"
verify_exe "$SRC"
copy_tree "$SRC" "$OUTDIR"
verify_exe "$OUTDIR"
chmod +x "$OUTDIR/$EXE" 2>/dev/null || true
[ -f "$OUTDIR/chrome-sandbox" ] && chmod +x "$OUTDIR/chrome-sandbox" 2>/dev/null || true
write_metadata "$OUTDIR" "$ARCHIVE" "$ACTUAL"
echo "浏览器资产装配完成（$PLATFORM $VERSION sha256=$ACTUAL）: $OUTDIR/$EXE"
