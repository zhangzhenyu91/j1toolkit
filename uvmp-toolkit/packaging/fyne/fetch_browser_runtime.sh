#!/usr/bin/env bash
# Fyne 轨浏览器运行时装配：按 packaging/fyne/assets/electron33-runtime.json 下载 /
# SHA256 校验 / 装配 Electron 33 运行时 + 独立浏览器运行时应用（构建机执行，目标机不联网）
#
# 用法：
#   bash packaging/fyne/fetch_browser_runtime.sh [<platform>] [<outdir>]
# platform ∈ windows-amd64 | linux-amd64 | linux-arm64（缺省按宿主自动判定）
# outdir 缺省 dist-fyne/<platform>/browser-runtime（随安装包进 browser-runtime/）
#
# 环境：
#   UVMP_BROWSER_ARCHIVE  本地 Electron 发行 zip——跳过下载，仍按清单 SHA256 校验
#   UVMP_BROWSER_DIR      本地已解包目录——跳过下载与归档校验，直接装配（校验主可执行文件存在）
#   UVMP_BROWSER_CACHE    下载缓存目录（缺省 packaging/.fyne-cache/electron）
#
# 纪律（以 packaging/fyne/assets/electron33-runtime.json 为准）：
#   - 归档 SHA256 与清单不匹配（或清单为空）→ 一律失败，禁止编造校验值
#   - 装配产物：主可执行 electron(.exe) 直落 outdir 根；独立浏览器运行时应用落 outdir/app/
#     （拷 uvmp-toolkit/browser-runtime/{package.json,main.cjs}，main=main.cjs 双模式：
#     --uvmp-browser-mode=export|course，见 browser-runtime/main.cjs 头注释）；
#     另写 outdir/uvmp-browser.json 元数据（路径全 ASCII）
set -euo pipefail
cd "$(dirname "$0")/../.."
MANIFEST=packaging/fyne/assets/electron33-runtime.json
CACHE_DIR=${UVMP_BROWSER_CACHE:-packaging/.fyne-cache/electron}

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
[ -n "$OUTDIR" ] || OUTDIR="dist-fyne/$PLATFORM/browser-runtime"
case "$OUTDIR" in
  /*|[A-Za-z]:*) ;;
  *) OUTDIR="$(pwd)/$OUTDIR" ;;
esac

command -v python3 >/dev/null || fail "需要 python3 解析 $MANIFEST 与解包校验"

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
          a.get("executable", ""), m.get("product", {}).get("version", ""),
          a.get("mirror_url", "")):
    print(v)
PY
}
MANIFEST_OUT=$(read_manifest) || fail "读取 $MANIFEST 失败"
URL=$(sed -n 1p <<<"$MANIFEST_OUT")
ARCHIVE_TYPE=$(sed -n 2p <<<"$MANIFEST_OUT")
SHA=$(sed -n 3p <<<"$MANIFEST_OUT")
EXE=$(sed -n 4p <<<"$MANIFEST_OUT")
VERSION=$(sed -n 5p <<<"$MANIFEST_OUT")
MIRROR_URL=$(sed -n 6p <<<"$MANIFEST_OUT")
[ -n "$EXE" ] || fail "清单缺 executable 字段（$PLATFORM）"
[ -n "$SHA" ] || fail "清单缺 archive_sha256（$PLATFORM）：未验资产禁止装配，先按官方 SHASUMS256 实取写回清单"

sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | awk '{print $1}'
  else python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$1"; fi
}

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

extract_zip() { # $1=zip $2=目标目录（剥掉单一顶层目录）
  python3 - "$1" "$2" <<'PY'
import os, sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(src) as z:
    names = [n for n in z.namelist() if n and not n.endswith("/")]
    tops = {n.split("/", 1)[0] for n in names}
    strip = ""
    if len(tops) == 1:
        top = next(iter(tops))
        # 只有「唯一顶层目录且其下还有内容」才剥壳（Electron 发行 zip 为根铺开，不剥）
        if any(("/" in n) for n in names):
            strip = top + "/"
    for n in names:
        rel = n[len(strip):] if (strip and n.startswith(strip)) else n
        if not rel:
            continue
        out = os.path.join(dst, rel.replace("/", os.sep))
        os.makedirs(os.path.dirname(out), exist_ok=True)
        with z.open(n) as fi, open(out, "wb") as fo:
            fo.write(fi.read())
print("解包完成（剥顶层 %r）: %s" % (strip or None, src))
PY
}

# ---------- 通道：本地已解包目录 ----------
if [ -n "${UVMP_BROWSER_DIR:-}" ]; then
  SRC_DIR=$UVMP_BROWSER_DIR
  [ -d "$SRC_DIR" ] || fail "UVMP_BROWSER_DIR 不存在: $SRC_DIR"
  [ -f "$SRC_DIR/$EXE" ] || fail "UVMP_BROWSER_DIR 缺主可执行 $EXE: $SRC_DIR"
  mkdir -p "$OUTDIR"
  cp -a "$SRC_DIR/." "$OUTDIR/"
  write_metadata "$OUTDIR" "UVMP_BROWSER_DIR=$SRC_DIR" "uvmp-browser-dir（未走归档校验）"
  echo "采用本地已解包浏览器运行时: $SRC_DIR -> $OUTDIR"
else
  # ---------- 取归档（本地包或下载） ----------
  mkdir -p "$CACHE_DIR"
  ARCHIVE=${UVMP_BROWSER_ARCHIVE:-}
  if [ -n "$ARCHIVE" ]; then
    [ -f "$ARCHIVE" ] || fail "UVMP_BROWSER_ARCHIVE 不存在: $ARCHIVE"
  else
    [ -n "$URL" ] || fail "清单缺 url（$PLATFORM）：未实测地址禁止装配"
    EXT=${ARCHIVE_TYPE:-zip}
    ARCHIVE="$CACHE_DIR/electron-$VERSION-$PLATFORM.$EXT"
    if [ ! -f "$ARCHIVE" ]; then
      echo "下载 Electron $VERSION（$PLATFORM）: $URL"
      if ! curl -fSL --connect-timeout 20 --max-time 600 -o "$ARCHIVE.part" "$URL"; then
        # 官方直链失败回退镜像（SHA256 仍按官方校验和强制，镜像只解决连通性）
        [ -n "$MIRROR_URL" ] || fail "下载失败: $URL（可改用 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR 本地通道）"
        rm -f "$ARCHIVE.part"
        echo "官方直链失败，回退镜像: $MIRROR_URL"
        curl -fSL --connect-timeout 20 --max-time 600 -o "$ARCHIVE.part" "$MIRROR_URL" \
          || fail "下载失败: $URL 与镜像 $MIRROR_URL（可改用 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR 本地通道）"
      fi
      mv -f "$ARCHIVE.part" "$ARCHIVE"
    fi
  fi

  # ---------- SHA256 校验（与清单必须一致） ----------
  ACTUAL=$(sha256_of "$ARCHIVE")
  [ "$ACTUAL" = "$SHA" ] || fail "SHA256 不匹配: $ARCHIVE（实际 $ACTUAL，清单 $SHA）——禁止装配未验资产"
  echo "SHA256 校验通过: $ACTUAL"

  # ---------- 装配 ----------
  rm -rf "$OUTDIR"
  mkdir -p "$OUTDIR"
  case "$ARCHIVE" in
    *.zip) extract_zip "$ARCHIVE" "$OUTDIR" ;;
    *.tar.gz|*.tgz) tar -xzf "$ARCHIVE" -C "$OUTDIR" --strip-components=0 ;;
    *) fail "不支持的归档类型: $ARCHIVE（清单 archive_type=${ARCHIVE_TYPE}）" ;;
  esac
  [ -f "$OUTDIR/$EXE" ] || fail "装配后缺主可执行 $EXE（归档结构与清单 executable 不符）"
  chmod +x "$OUTDIR/$EXE" || fail "无法恢复 Electron 可执行权限: $OUTDIR/$EXE"
  write_metadata "$OUTDIR" "$URL" "$ACTUAL"
  echo "浏览器运行时装配完成: $OUTDIR/$EXE"
fi

# ---------- 独立浏览器运行时应用（app/：main=main.cjs，export/course 双模式） ----------
RT_SRC=browser-runtime
[ -f "$RT_SRC/main.cjs" ] || fail "缺少独立浏览器运行时脚本: $RT_SRC/main.cjs"
[ -f "$RT_SRC/package.json" ] || fail "缺少独立浏览器运行时清单: $RT_SRC/package.json"
mkdir -p "$OUTDIR/app"
cp -f "$RT_SRC/main.cjs" "$OUTDIR/app/main.cjs"
cp -f "$RT_SRC/package.json" "$OUTDIR/app/package.json"
# app 入口契约：package.json main 须指向双模式主脚本 main.cjs（Electron 以应用目录方式加载）
python3 - "$OUTDIR/app/package.json" <<'PY'
import json, sys
pkg = json.load(open(sys.argv[1], encoding="utf-8"))
main = str(pkg.get("main") or "")
if main != "main.cjs":
    sys.exit("app/package.json main 应为 main.cjs（当前 %r）：app 入口须与 export/course 双模式主脚本匹配" % main)
PY
echo "独立浏览器运行时应用就位: $OUTDIR/app（main=main.cjs 双模式）"
echo "  导出口径: UVMP_BROWSER_APP=$OUTDIR/app + UVMP_BROWSER_WORKER=--uvmp-browser-mode=export（uvmp-export 渲染/自检）"
echo "  课程口径: internal/browser 以 --uvmp-browser-mode=course [--uvmp-start-url=…] 驱动同一 app"
