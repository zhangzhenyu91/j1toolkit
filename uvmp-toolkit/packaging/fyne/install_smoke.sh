#!/usr/bin/env bash
# Fyne 轨安装冒烟（debian:buster 容器内执行）：dpkg 实装 → 布局/权限校验 → 卸载清理校验
#
# 用法（容器内，工作目录 uvmp-toolkit/）：
#   bash packaging/fyne/install_smoke.sh <deb 路径>
#
# 门禁内容：
#   1. deb 元数据合法（dpkg-deb --info/--contents 可解析；Version/Architecture 正确）
#   2. dpkg -i 实装成功（含 postinst；预置假旧单元验证同名替换接管）
#   3. 安装布局校验：/opt/uvmp-toolkit-fyne/{uvmp-toolkit-fyne launcher,uvmp-toolkit,uvmp-core,uvmp-export,
#      config.initial.json,install/linux/uvmp-toolkit.{service,timer},
#      browser-runtime/electron,browser-runtime/app/main.cjs,models/det.onnx,native/libonnxruntime.*} + 桌面项
#   4. OS 定时接管校验：systemd 单元已装且 @RUN_CMD@→/opt/uvmp-toolkit-fyne/uvmp-core（5 条命令链齐全），
#      同名旧单元被替换（升级替换不双跑）；配置已播种（config.initial.json）
#   5. launcher 实跑冒烟：UVMP_FYNE_SMOKE=gui 经 launcher 启动成功退出 0（xvfb）
#   6. dpkg -r 卸载后文件树/桌面项/systemd 单元清理干净，用户配置保留（afterRemove 保留配置）
set -euo pipefail
cd "$(dirname "$0")/../.."
fail() { echo "安装冒烟失败: $*" >&2; exit 1; }

DEB=${1:?用法: install_smoke.sh <deb 路径>}
case "$DEB" in
  /*|[A-Za-z]:*) ;;
  *) DEB="$(pwd)/$DEB" ;;
esac
[ -f "$DEB" ] || fail "deb 不存在: $DEB"
command -v dpkg-deb >/dev/null || fail "缺少 dpkg-deb（在 debian:buster 容器内执行）"
command -v dpkg >/dev/null || fail "缺少 dpkg"

PKGID=uvmp-toolkit-fyne
APPDIR=/opt/$PKGID

# ---------- 1. deb 元数据 ----------
INFO=$(dpkg-deb --info "$DEB") || fail "dpkg-deb --info 失败"
echo "$INFO"
dpkg-deb --contents "$DEB" >/dev/null || fail "dpkg-deb --contents 失败"
echo "$INFO" | grep -q "^ Package: $PKGID$" || fail "control Package 不符（应为 $PKGID）"
echo "$INFO" | grep -q "^ Architecture: \(amd64\|arm64\)$" || fail "control Architecture 不符"

# ---------- 2. 实装（预置假旧单元验证同名替换接管） ----------
# 容器内补齐 GUI/浏览器运行库（与 gate_smoke 同套），保证 launcher 冒烟真实可跑
if [ -f /etc/debian_version ] && [ "$(cut -d. -f1 /etc/debian_version)" = "10" ]; then
  echo "deb http://archive.debian.org/debian buster main" > /etc/apt/sources.list
  echo "deb http://archive.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list
fi
if command -v apt-get >/dev/null; then
  apt-get -o Acquire::Check-Valid-Until=false update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends -qq \
    xvfb xauth dbus-x11 x11-apps libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 libcups2 libdrm2 \
    libgbm1 libgtk-3-0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libxkbcommon0 \
    libx11-6 libx11-xcb1 libxcb1 libxext6 libxrender1 libxi6 libxtst6 libxcursor1 \
    libxinerama1 libxxf86vm1 libgl1 libglu1-mesa libpangocairo-1.0-0 libpango-1.0-0 \
    libcairo2 libasound2 libexpat1 libglib2.0-0 libdbus-1-3 >/dev/null
fi
# 预置假「旧安装」单元（指向 Electron 轨路径）：postinst 应检测并同名替换（升级接管，不双跑）
mkdir -p /etc/systemd/system
printf '%s\n' '[Unit]' 'Description=fake-old-unit' '[Service]' \
  'ExecStart=/opt/uvmp-toolkit/resources/core/uvmp-core daily-export --if-due' \
  > /etc/systemd/system/uvmp-toolkit.service
DpkgLog=$(mktemp)
if command -v apt-get >/dev/null; then
  # 本地 deb 也经 apt 安装，libatomic1 等声明依赖须由包管理器一并解析；dpkg -i 不负责补依赖。
  DEBIAN_FRONTEND=noninteractive apt-get install -y "$DEB" | tee "$DpkgLog" || fail "apt-get install 失败"
else
  dpkg -i "$DEB" | tee "$DpkgLog" || fail "dpkg -i 失败"
fi
grep -q "已同名替换" "$DpkgLog" || fail "postinst 未检测/报告旧单元同名替换"

# ---------- 3. 安装布局校验 ----------
for f in "$APPDIR/$PKGID" "$APPDIR/uvmp-toolkit" "$APPDIR/uvmp-core" "$APPDIR/uvmp-export" \
         "$APPDIR/config.initial.json" \
         "$APPDIR/install/linux/uvmp-toolkit.service" "$APPDIR/install/linux/uvmp-toolkit.timer" \
         "$APPDIR/browser-runtime/electron" "$APPDIR/browser-runtime/app/main.cjs" \
         "$APPDIR/browser-runtime/app/package.json" \
         "$APPDIR/models/det.onnx" "$APPDIR/models/rec.onnx" "$APPDIR/icon.png" \
         "/usr/share/applications/$PKGID.desktop"; do
  [ -e "$f" ] || fail "安装布局缺: $f"
done
ls "$APPDIR/native/libonnxruntime.so."* >/dev/null 2>&1 || fail "安装布局缺 native/libonnxruntime.*"
[ -x "$APPDIR/$PKGID" ] || fail "launcher 无执行位: $APPDIR/$PKGID"
[ -x "$APPDIR/uvmp-toolkit" ] || fail "uvmp-toolkit 无执行位"
[ -x "$APPDIR/uvmp-core" ] || fail "uvmp-core 无执行位"
echo "== 安装布局校验通过 =="

# ---------- 4. OS 定时接管校验（systemd 单元 + 配置播种） ----------
UNIT=/etc/systemd/system/uvmp-toolkit.service
[ -f "$UNIT" ] || fail "systemd 单元未安装: $UNIT"
[ -f /etc/systemd/system/uvmp-toolkit.timer ] || fail "systemd timer 未安装"
if grep -q "fake-old-unit" "$UNIT"; then fail "旧单元内容残留（未被同名替换）"; fi
# 5 条命令链齐全（与 Windows 5 个计划任务同口径），@RUN_CMD@ 指向 /opt/uvmp-toolkit-fyne/uvmp-core
for c in "daily-export --if-due" "tree-sync --tick" "tree-sync --nightly --if-due" \
         "crossing-sync --tick" "crossing-sync --nightly --if-due"; do
  grep -qF "ExecStart=/opt/$PKGID/uvmp-core $c" "$UNIT" || fail "单元缺命令链: $c"
done
grep -q "UVMP_TOOLKIT_CONFIG=" "$UNIT" || fail "单元缺 UVMP_TOOLKIT_CONFIG（配置共用路径）"
# 配置播种：容器无桌面用户时兜底 /etc/uvmp-toolkit/config.json（有桌面用户时为 ~/.config/uvmp-toolkit/config.json）
CONF=/etc/uvmp-toolkit/config.json
[ -f "$CONF" ] || fail "配置未播种: $CONF"
grep -q '"username"' "$CONF" || fail "播种配置内容不符 config.initial.json"
echo "== OS 定时接管校验通过（同名替换 + 5 命令链 + 配置播种） =="

# ---------- 5. launcher 实跑冒烟（经安装态 launcher 启动 GUI 冒烟） ----------
mkdir -p "${HOME:-/root}/.cache"
LOG=$(mktemp)
set +e
LIBGL_ALWAYS_SOFTWARE=1 UVMP_FYNE_SMOKE=gui timeout 180 xvfb-run -a "$APPDIR/$PKGID" >"$LOG" 2>&1
RC=$?
set -e
cat "$LOG" || true
[ "$RC" -eq 0 ] || fail "launcher GUI 冒烟失败（exit=$RC）"
echo "== launcher GUI 冒烟通过 =="

# ---------- 6. 卸载清理校验 ----------
dpkg -r "$PKGID" || fail "dpkg -r 失败"
[ ! -e "$APPDIR" ] || fail "卸载后残留: $APPDIR"
[ ! -e "/usr/share/applications/$PKGID.desktop" ] || fail "卸载后残留桌面项"
[ ! -e /etc/systemd/system/uvmp-toolkit.service ] || fail "卸载后残留 systemd 单元"
[ ! -e /etc/systemd/system/uvmp-toolkit.timer ] || fail "卸载后残留 systemd timer"
[ -f "$CONF" ] || fail "卸载后配置丢失（afterRemove 保留配置契约，应保留: $CONF）"
echo "== 卸载清理校验通过（配置保留） =="

echo "Fyne 轨安装冒烟全部通过: $DEB"
