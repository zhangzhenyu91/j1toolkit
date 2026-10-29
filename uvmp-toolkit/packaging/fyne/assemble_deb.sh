#!/usr/bin/env bash
# Fyne 轨 deb 装配（debian:buster 容器内执行，dpkg-deb 出包）：
# dist-fyne/<platform>/ 装配布局 → uvmp-toolkit-fyne-<version>_<arch>.deb + tar.gz 绿色包
#
# 用法（容器内，工作目录 uvmp-toolkit/）：
#   bash packaging/fyne/assemble_deb.sh <linux-amd64|linux-arm64> [<staging-dir>]
# staging-dir 缺省 dist-fyne/<platform>（应含：uvmp-toolkit、uvmp-core、uvmp-export、
# models/、native/，分别由 build_fyne_core.sh / assemble_onnx.sh 产出；browser-runtime/
# 为可选的历史 Electron 回退资产，仅显式装配时打包，默认不再内置）
#
# 环境：
#   UVMP_FYNE_VERSION  版本号（缺省 uvmp-toolkit/version.json 的 version；与发版 tag 对齐由 CI 传入）
#
# 产物命名（-fyne- 中缀避免覆盖 Electron 轨产物；保留 .deb 后缀与 CNB 取件口径一致）：
#   uvmp-toolkit-fyne-<version>_<arch>.deb
#   uvmp-toolkit-fyne-<version>-<arch>.tar.gz（绿色包，解压即用）
#
# 安装布局（deb → /opt/uvmp-toolkit-fyne/）：
#   uvmp-toolkit / uvmp-core / uvmp-export / uvmp-toolkit-fyne（launcher：LD_LIBRARY_PATH=native 后 exec uvmp-toolkit）
#   icon.png / config.initial.json / install/linux/uvmp-toolkit.{service,timer}（systemd 模板）
#   models/ / native/（ONNX）；browser-runtime/ 仅在显式装配了 Electron 回退时随包
#   /usr/share/applications/uvmp-toolkit-fyne.desktop
# OS 定时接管（功能 parity，与旧版同名升级替换而非双跑）：postinst 播种 config.initial.json 到
# ~/.config/uvmp-toolkit/config.json、状态镜像 ~/.local/share/uvmp-toolkit（与 Electron 轨/源码模式
# 共用，回退旧版可用），并按模板装 uvmp-toolkit.service/.timer（@RUN_CMD@→/opt/uvmp-toolkit-fyne/uvmp-core，
# 每分钟 due-check：daily-export / tree-sync / crossing-sync 五命令链）；同名单元（Electron 轨/源码模式
# 注册）检测后原位替换避免双跑。postrm 卸载只删单元、保留配置（afterRemove 保留配置契约）。
set -euo pipefail
cd "$(dirname "$0")/../.."
fail() { echo "错误: $*" >&2; exit 1; }

PLATFORM=${1:?用法: assemble_deb.sh <linux-amd64|linux-arm64> [<staging-dir>]}
STAGING=${2:-dist-fyne/$PLATFORM}
case "$PLATFORM" in
  linux-amd64) ARCH=amd64 ;;
  linux-arm64) ARCH=arm64 ;;
  *) fail "不支持的平台: $PLATFORM（可选 linux-amd64|linux-arm64）" ;;
esac
case "$STAGING" in
  /*|[A-Za-z]:*) ;;
  *) STAGING="$(pwd)/$STAGING" ;;
esac
command -v dpkg-deb >/dev/null || fail "缺少 dpkg-deb（在 debian:buster 容器内执行）"

VERSION=${UVMP_FYNE_VERSION:-}
if [ -z "$VERSION" ]; then
  # 版本与 desktop/package.json 解耦：读 uvmp-toolkit/version.json（装配版本单一来源）
  VERSION=$(sed -n 's/^[[:space:]]*"version": *"\([^"]*\)".*/\1/p' version.json | head -1)
fi
[ -n "$VERSION" ] || fail "无法确定版本号：设 UVMP_FYNE_VERSION 或确保 uvmp-toolkit/version.json 可读"
echo "装配版本: $VERSION（$PLATFORM）"

# ---------- 装配布局齐全性（缺失即失败，不出残缺包） ----------
for f in uvmp-toolkit uvmp-core uvmp-export; do
  [ -f "$STAGING/$f" ] || fail "装配布局缺 $f（先跑 packaging/fyne/build_fyne_core.sh）"
done
# 浏览器：默认宿主浏览器（360ent 等），不再随包内置 Electron；browser-runtime/ 为
# 可选历史回退资产，只有显式装配时才校验/打包（见 fetch_browser_runtime.sh）。
if [ -d "$STAGING/browser-runtime" ]; then
  [ -f "$STAGING/browser-runtime/app/main.cjs" ] && [ -f "$STAGING/browser-runtime/app/package.json" ] \
    || fail "browser-runtime/app/{package.json,main.cjs} 不完整（显式 Electron 回退装配不完整）"
fi
[ -f "$STAGING/models/det.onnx" ] && [ -f "$STAGING/models/rec.onnx" ] \
  || fail "装配布局缺 models（先跑 packaging/fyne/assemble_onnx.sh）"
ls "$STAGING/native/libonnxruntime.so."* >/dev/null 2>&1 || fail "装配布局缺 native/libonnxruntime.*（先跑 packaging/fyne/assemble_onnx.sh）"

# ---------- 暂存安装树 ----------
PKGID=uvmp-toolkit-fyne
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
APPDIR="$ROOT/opt/$PKGID"
mkdir -p "$APPDIR" "$ROOT/usr/share/applications" "$ROOT/DEBIAN"

cp -f "$STAGING/uvmp-toolkit" "$STAGING/uvmp-core" "$STAGING/uvmp-export" "$APPDIR/"
if [ -d "$STAGING/browser-runtime" ]; then
  cp -a "$STAGING/browser-runtime" "$APPDIR/browser-runtime"
fi
cp -a "$STAGING/models" "$APPDIR/models"
cp -a "$STAGING/native" "$APPDIR/native"
# 品牌图标用全仓统一真图标 packaging/icon.png（由 desktop/src/assets/icon.png 重编码；
# Windows 装配同一来源，不得回退默认图标）
ICON_SRC=packaging/icon.png
[ -f "$ICON_SRC" ] || fail "缺少品牌图标: $ICON_SRC（真正 Shade 图标，由 desktop/src/assets/icon.png 重编码）"
cp -f "$ICON_SRC" "$APPDIR/icon.png"
# OS 定时/配置素材入包（校验 deb 含全部脚本的依据）：初始配置 + systemd 模板（@RUN_CMD@ 等占位由 postinst 填）
cp -f packaging/config.initial.json "$APPDIR/config.initial.json"
mkdir -p "$APPDIR/install/linux"
cp -f install/linux/uvmp-toolkit.service install/linux/uvmp-toolkit.timer "$APPDIR/install/linux/"
# systemd 模板必须覆盖全部 5 条命令链（daily-export / tree-sync / crossing-sync，与 Windows 5 个计划任务同口径）
for c in "daily-export --if-due" "tree-sync --tick" "tree-sync --nightly --if-due" \
         "crossing-sync --tick" "crossing-sync --nightly --if-due"; do
  grep -qF "$c" "$APPDIR/install/linux/uvmp-toolkit.service" \
    || fail "systemd 模板缺命令链: $c"
done
chmod +x "$APPDIR/uvmp-toolkit" "$APPDIR/uvmp-core" "$APPDIR/uvmp-export"
if [ -d "$APPDIR/browser-runtime" ] && [ -f "$APPDIR/browser-runtime/electron" ]; then
  chmod +x "$APPDIR/browser-runtime/electron"
fi

# launcher：ONNX 库走 native/（soname 副本同目录），浏览器运行时与模型按相对布局发现
cat > "$APPDIR/$PKGID" <<'LAUNCH'
#!/bin/sh
# Fyne 轨客户端启动器：按安装相对布局注入 ONNX 运行时库路径后启动 GUI
DIR=$(cd "$(dirname "$0")" && pwd)
export LD_LIBRARY_PATH="$DIR/native${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
exec "$DIR/uvmp-toolkit" "$@"
LAUNCH
chmod +x "$APPDIR/$PKGID"

cat > "$ROOT/usr/share/applications/$PKGID.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Shade 壹匣 - 内网（Fyne 灰度）
Comment=派车同步 / 轨迹导出 / 题库搜题（Fyne 灰度轨）
Exec=/opt/$PKGID/$PKGID
Icon=/opt/$PKGID/icon.png
Terminal=false
Categories=Utility;
StartupWMClass=uvmp-toolkit
EOF

# ---------- control 与维护脚本 ----------
cat > "$ROOT/DEBIAN/control" <<EOF
Package: $PKGID
Version: $VERSION
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: 检修一班 <zhangzhenyu91@users.noreply.github.com>
Homepage: https://github.com/zhangzhenyu91/j1toolkit
Depends: libatomic1, libnss3, libgtk-3-0, libgbm1, libasound2, libgl1, libx11-6, libxext6, libxcursor1, libxrandr2, libxinerama1, libxi6, libxxf86vm1
Description: Shade 壹匣 - 内网客户端（Fyne 灰度轨）
 Go+Fyne 重构灰度安装包：Fyne GUI + Go 核心 CLI uvmp-core + 导出工作进程 uvmp-export
 + ONNX OCR 运行时；渲染/课程使用宿主机公司指定浏览器（360 企业浏览器 360ent 等，
 需另行安装），不再随包内置 Electron。
 与 Electron 轨安装包并行分发、互不覆盖（产物名带 -fyne- 中缀）。
EOF
cat > "$ROOT/DEBIAN/postinst" <<EOF
#!/bin/sh
set -e
APP_DIR=/opt/$PKGID
CORE=/opt/$PKGID/uvmp-core
chmod +x \$APP_DIR/\$PKGID \$APP_DIR/uvmp-toolkit \$APP_DIR/uvmp-core \$APP_DIR/uvmp-export 2>/dev/null || true
# 历史 Electron 回退资产仅在显式随包时恢复可执行位（默认不内置）
[ -f \$APP_DIR/browser-runtime/electron ] && chmod +x \$APP_DIR/browser-runtime/electron 2>/dev/null || true

# ---- 初始配置播种（配置/镜像目录与 Electron 轨/源码模式共用，回退旧版可用；已存在则保留）----
DESKTOP_USER="\${SUDO_USER:-}"
if [ -z "\$DESKTOP_USER" ]; then
  DESKTOP_USER=\$(getent passwd 1000 2>/dev/null | cut -d: -f1 || true)
fi
if [ -z "\$DESKTOP_USER" ]; then
  DESKTOP_USER=\$(ls /home 2>/dev/null | head -1 || true)
fi
USER_HOME=\$(getent passwd "\$DESKTOP_USER" 2>/dev/null | cut -d: -f6 || true)
if [ -n "\$USER_HOME" ] && [ -d "\$USER_HOME" ]; then
  CONF_DIR="\$USER_HOME/.config/uvmp-toolkit"
  CONF="\$CONF_DIR/config.json"
  if [ ! -f "\$CONF" ]; then
    mkdir -p "\$CONF_DIR"
    cp "\$APP_DIR/config.initial.json" "\$CONF"
    chown -R "\$DESKTOP_USER:\$DESKTOP_USER" "\$CONF_DIR"
    chmod 600 "\$CONF"
  fi
  # root 定时器的状态镜像目标=桌面用户状态目录（GUI 才能看到自动执行的记录/成功戳）
  MIRROR_STATE="\$USER_HOME/.local/share/uvmp-toolkit"
  mkdir -p "\$MIRROR_STATE"
  chown -R "\$DESKTOP_USER:\$DESKTOP_USER" "\$MIRROR_STATE" 2>/dev/null || true
else
  CONF=/etc/uvmp-toolkit/config.json
  mkdir -p /etc/uvmp-toolkit
  [ -f "\$CONF" ] || cp "\$APP_DIR/config.initial.json" "\$CONF"
  MIRROR_STATE=""
fi

# ---- systemd timer 接管（due-check 每分钟一拍）：同名单元升级替换，避免与旧安装双跑 ----
mkdir -p /etc/systemd/system
if [ -f /etc/systemd/system/uvmp-toolkit.service ] && ! grep -qF "\$CORE" /etc/systemd/system/uvmp-toolkit.service; then
  echo "检测到旧定时单元 uvmp-toolkit.service（Electron 轨/源码模式注册）→ 已同名替换为本安装（不双跑）"
fi
sed -e "s|@CONFIG_PATH@|\$CONF|g" -e "s|@RUN_CMD@|\$CORE|g" \\
    -e "s|@MIRROR_STATE@|\$MIRROR_STATE|g" \\
    "\$APP_DIR/install/linux/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
cp "\$APP_DIR/install/linux/uvmp-toolkit.timer" /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload || true
systemctl enable --now uvmp-toolkit.timer || true

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
exit 0
EOF
cat > "$ROOT/DEBIAN/postrm" <<EOF
#!/bin/sh
set -e
# 卸载清理：停用并删除 systemd 单元；用户配置 ~/.config/uvmp-toolkit/config.json 与状态镜像
# ~/.local/share/uvmp-toolkit 保留（afterRemove 保留配置契约，回退旧版可用）。升级（upgrade）不动单元。
case "\$1" in
  remove|purge)
    systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
    systemctl stop uvmp-toolkit.service 2>/dev/null || true
    rm -f /etc/systemd/system/uvmp-toolkit.service /etc/systemd/system/uvmp-toolkit.timer
    systemctl daemon-reload 2>/dev/null || true
    ;;
esac
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
exit 0
EOF
chmod 755 "$ROOT/DEBIAN/postinst" "$ROOT/DEBIAN/postrm"

# ---------- 出包 ----------
OUT_DEB="dist-fyne/$PLATFORM/${PKGID}-${VERSION}_${ARCH}.deb"
OUT_TGZ="dist-fyne/$PLATFORM/${PKGID}-${VERSION}-${ARCH}.tar.gz"
mkdir -p "dist-fyne/$PLATFORM"
dpkg-deb --root-owner-group --build "$ROOT" "$OUT_DEB"
dpkg-deb --info "$OUT_DEB" >/dev/null
dpkg-deb --contents "$OUT_DEB" >/dev/null

# ---------- 校验 deb 含全部脚本（配置播种 + systemd 模板 + 维护脚本，缺一即失败） ----------
DEB_LIST=$(dpkg-deb --contents "$OUT_DEB")
for f in "./opt/$PKGID/config.initial.json" \
         "./opt/$PKGID/install/linux/uvmp-toolkit.service" \
         "./opt/$PKGID/install/linux/uvmp-toolkit.timer"; do
  echo "$DEB_LIST" | grep -qF "$f" || fail "deb 缺脚本: $f"
done
POSTINST=$(dpkg-deb --info "$OUT_DEB" postinst)
POSTRM=$(dpkg-deb --info "$OUT_DEB" postrm)
echo "$POSTINST" | grep -qF "s|@RUN_CMD@|\$CORE|g" || fail "deb postinst 缺 @RUN_CMD@ 替换（应指向 /opt/$PKGID/uvmp-core）"
echo "$POSTINST" | grep -qF "CORE=/opt/$PKGID/uvmp-core" || fail "deb postinst RUN_CMD 未指向 /opt/$PKGID/uvmp-core"
echo "$POSTINST" | grep -q "config.initial.json" || fail "deb postinst 缺配置播种逻辑"
echo "$POSTINST" | grep -q "uvmp-toolkit.timer" || fail "deb postinst 缺 timer 接管逻辑"
echo "$POSTRM" | grep -q "uvmp-toolkit.timer" || fail "deb postrm 缺 timer 清理逻辑"
echo "$POSTRM" | grep -q "config.json" || fail "deb postrm 缺保留配置契约注释/逻辑"
echo "== deb 脚本齐全性校验通过（config.initial.json + service/timer 模板 + postinst/postrm）=="

# 绿色包：安装树 opt/$PKGID/ 原样打 tar.gz（解压即用，launcher 同目录）
STAGE_TGZ=$(mktemp -d)
mkdir -p "$STAGE_TGZ/$PKGID"
cp -a "$APPDIR/." "$STAGE_TGZ/$PKGID/"
tar -czf "$OUT_TGZ" -C "$STAGE_TGZ" "$PKGID"
rm -rf "$STAGE_TGZ"

echo "deb 装配完成:"
echo "  $OUT_DEB"
echo "  $OUT_TGZ"
