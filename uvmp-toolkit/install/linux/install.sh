#!/usr/bin/env bash
# 内网工具箱【源码模式】安装脚本（银河麒麟 V10 SP1，amd64/arm64）
#
# 适用场景：无法运行 Electron 桌面客户端的机器（老旧 glibc/arm64 兼容问题等）——
# 安装 U 盘单监听 usb-agent systemd service（CLI 无头运行，无 GUI）。
# 有 GUI 的正常机器请直接用 Release 的 .deb 安装包。
#
# 用法：sudo bash install/linux/install.sh
set -euo pipefail

# ---- CRLF 自检（文件经 Windows 中转拷贝后行尾变 CRLF 的坑）----
if grep -q $'\r' "$0"; then
  echo "检测到本脚本为 CRLF 行尾，请先执行："
  echo "  sed -i 's/\r\$//' install/linux/*.sh install/linux/*.service"
  echo "然后再重跑安装。"
  exit 1
fi
[[ $EUID -eq 0 ]] || { echo "请用 sudo 运行：sudo bash install/linux/install.sh"; exit 1; }

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
APP_USER=${SUDO_USER:-}
if [[ -z "$APP_USER" ]]; then
  read -r -p "运行客户端的桌面用户名: " APP_USER
fi
id "$APP_USER" >/dev/null 2>&1 || { echo "用户不存在: $APP_USER"; exit 1; }
APP_HOME=$(getent passwd "$APP_USER" 2>/dev/null | cut -d: -f6 || true)
[[ -n "$APP_HOME" ]] || APP_HOME="/home/$APP_USER"
CONFIG_PATH="$PROJECT_DIR/toolkit/config.json"

echo "============================================================"
echo "内网工具箱 安装（源码 CLI 模式，无 GUI）"
echo "  项目目录: $PROJECT_DIR"
echo "============================================================"

# ---- 1. Python 检测 ----
PYTHON=""
for c in /opt/jdzh/python3.14/bin/python3 python3; do
  if command -v "$c" >/dev/null 2>&1; then PYTHON=$(command -v "$c"); break; fi
done
[[ -n "$PYTHON" ]] || { echo "未找到 python3，请先安装"; exit 1; }
echo "[1/4] Python: $PYTHON ($("$PYTHON" --version 2>&1))"

# 解释器标签（cp310 / cp37 …）与两级依赖清单。
# 离线轮按解释器 ABI 分：Pillow 13 只有 cp311+ 的二进制轮（本仓随包 cp315），
# 把 cp315 的 .so 解进 3.7 的 pylib 只会得到 ImportError（magic tag 不匹配）。
#
# 依赖分两级：
#   UVMP_DEP_CORE    核心依赖（同步/导出/登录），缺一条就整装失败；
#   UVMP_DEP_IMAGING 图片工具依赖（Pillow），可缺——缺了只警告「图片功能不可用」。
# 3.11–3.14 的机器若 offline_packages 里只有 cp315 的 Pillow 轮，ABI 不匹配装不上，
# 这时核心依赖是齐的，安装必须继续而不是 exit 1。
uvmp_init_python_compat() {
  PY_VER_TAG=$("$PYTHON" -c 'import sys; print("cp%d%d" % sys.version_info[:2])')
  local major minor
  major=$("$PYTHON" -c 'import sys; print(sys.version_info[0])')
  minor=$("$PYTHON" -c 'import sys; print(sys.version_info[1])')
  UVMP_DEP_CORE="requests, pypdf, websocket, gmssl.sm2, Cryptodome.Util.asn1"
  UVMP_DEP_IMAGING=""
  UVMP_IMAGING_SUPPORTED=0
  # Pillow 13 要求 3.11+：只有 3.11 及以上才可能装图片引擎
  if [ "$major" -gt 3 ] || { [ "$major" -eq 3 ] && [ "$minor" -ge 11 ]; }; then
    UVMP_DEP_IMAGING="PIL.Image, PIL.ImageOps"
    UVMP_IMAGING_SUPPORTED=1
  fi
}

# wheel 是否适用于当前解释器（$PY_VER_TAG）：
#   - 纯 Python 轮（none-any）与 abi3 轮：任何 3.x 都能装
#   - 其余带 -cp3x / -cp4x 的二进制轮：标签必须出现在文件名里
uvmp_wheel_fits_python() {
  local name
  name=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$name" in
    *none-any*|*abi3*) return 0 ;;
    *-cp3*|*-cp4*)
      case "$name" in
        *"$PY_VER_TAG"*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
  esac
  return 0
}

# 在 pylib/ 里逐个 import 给定清单（逗号分隔）。清单由调用方传入，不在这里写死。
uvmp_import_ok() {
  "$PYTHON" - "$PROJECT_DIR/pylib" "$1" <<'PYEOF'
import sys
sys.path.insert(0, sys.argv[1])
for name in [m.strip() for m in sys.argv[2].split(",") if m.strip()]:
    __import__(name)
PYEOF
}

# 明确报告图片功能不可用（不静默）：缺 Pillow 时安装继续，但要让人知道能力边界。
# 措辞按「是否已尝试解包」分支：解包过仍装不上 ≠ 包里没有该轮。
uvmp_report_imaging_unavailable() {
  echo "      警告：图片工具不可用（当前 Python $("$PYTHON" -c 'import sys; print("%d.%d" % sys.version_info[:2])')）"
  if [ "$UVMP_IMAGING_SUPPORTED" = "0" ]; then
    echo "            Pillow 13 要求 Python 3.11+，本机解释器更老：图片查看/批处理不启用。"
  elif [ -f "$PROJECT_DIR/pylib/.unpacked" ] \
       && grep -qi '^pillow-' "$PROJECT_DIR/pylib/.unpacked" 2>/dev/null; then
    echo "            已解包 offline_packages/ 里的 Pillow 轮，但当前解释器仍无法导入 PIL。"
    echo "            多半是 ABI 不匹配（随包只有 cp315）；需要图片功能请安装 Python 3.15。"
  else
    echo "            offline_packages/ 里没有与 $PY_VER_TAG 匹配的 Pillow 轮（随包只有 cp315）。"
    echo "            需要图片功能时请安装 Python 3.15 或自备对应版本的 Pillow 轮。"
  fi
  echo "            派车同步/轨迹导出/题库搜题/批量重命名等其余功能不受影响。"
}

# 解包 → 校验核心 → 校验图片（可选）。
# 返回：0 依赖可用；1 核心依赖缺失（调用方应中止）。
uvmp_prepare_deps() {
  # 解包条件 = 核心依赖缺 **或** 图片依赖缺（任一缺失就要试着补）。
  # 只写「核心缺才解包」会踩两个坑：① 核心早已装好、只缺 Pillow 的升级场景永远补不上；
  # ② 随后报「没有匹配轮」——可轮子明明就在 offline_packages 里。
  local need_imaging=0
  if [ -n "$UVMP_DEP_IMAGING" ] && ! uvmp_import_ok "$UVMP_DEP_IMAGING" >/dev/null 2>&1; then
    need_imaging=1
  fi
  if ! uvmp_import_ok "$UVMP_DEP_CORE" >/dev/null 2>&1 || [ "$need_imaging" = "1" ]; then
    mkdir -p "$PROJECT_DIR/pylib"
    : >"$PROJECT_DIR/pylib/.unpacked"
    for f in "$PROJECT_DIR"/offline_packages/*.whl; do
      base=$(basename "$f")
      case "$base" in
        # 异构平台 wheel 解开会在 pylib 里互相覆盖（同名 .so/.pyd），只解当前架构的
        *win32*|*win_amd64*|*win_arm64*) continue ;;
        *macosx*) continue ;;
        *manylinux*aarch64*|*linux_aarch64*)
          [ "$ARCH" = "aarch64" ] || continue ;;
        *manylinux*x86_64*|*linux_x86_64*)
          [ "$ARCH" = "x86_64" ] || continue ;;
      esac
      # 解释器 ABI 不匹配的轮跳过（例如 3.11 机器用不上 cp315 的 Pillow）
      uvmp_wheel_fits_python "$base" || continue
      "$PYTHON" -m zipfile -e "$f" "$PROJECT_DIR/pylib/"
      printf '%s\n' "$base" >>"$PROJECT_DIR/pylib/.unpacked"
    done
    echo "      已解压 offline_packages -> pylib/"
  fi

  # 核心依赖缺失 = 真的装不起来，必须中止（调用方据此给出失败提示并退出）
  if ! uvmp_import_ok "$UVMP_DEP_CORE" >/dev/null 2>&1; then
    return 1
  fi

  # 图片依赖是可选能力：缺了只警告，绝不阻断安装
  if [ -n "$UVMP_DEP_IMAGING" ]; then
    if uvmp_import_ok "$UVMP_DEP_IMAGING" >/dev/null 2>&1; then
      echo "      图片依赖 OK（$UVMP_DEP_IMAGING）"
    else
      uvmp_report_imaging_unavailable
    fi
  else
    uvmp_report_imaging_unavailable
  fi
  return 0
}

# ---- 2. Python 依赖（免 pip：wheel 解压到 pylib/）----
# 探测必须做到 gmssl.sm2：gmssl 的 sm2.py 在模块顶层 `from Cryptodome.Util.asn1 import ...`，
# 只 import gmssl 能过、一到 SM2 加密就 ImportError（自动登录必挂）。pycryptodomex 的
# wheel 按平台分（win_amd64 / manylinux x86_64 / manylinux aarch64），故解压时按架构过滤。
# 同时按解释器 ABI 过滤：把 cp315 的 Pillow 解进 3.7 会让 import PIL 直接炸。
#
# 两级口径（见 uvmp_init_python_compat）：核心依赖缺了才中止；图片依赖（Pillow）缺了
# 只警告「图片功能不可用」——3.11–3.14 的机器装不上 cp315 的 Pillow 轮属于这种情况，
# 但派车同步/轨迹导出/题库搜题/批量重命名都还能用，不能因此整装失败。
echo "[2/4] 检查 Python 依赖 ..."
ARCH=$(uname -m)
uvmp_init_python_compat
echo "      解释器标签: $PY_VER_TAG"
uvmp_prepare_deps || {
  echo "      依赖校验失败，请检查 offline_packages/（核心依赖需含 pycryptodomex；$UVMP_DEP_CORE）"
  exit 1
}

# ---- 3. 配置文件 ----
echo "[3/4] 配置文件: $CONFIG_PATH"
if [[ -f "$CONFIG_PATH" ]]; then
  echo "      已存在，保留原配置"
else
  read -r -p "      SSO 账号（派车系统登录账号）: " SSO_USER
  read -r -s -p "      SSO 密码: " SSO_PASS; echo
  read -r -p "      U盘卷标 [GLKVM]: " USB_LABEL; USB_LABEL=${USB_LABEL:-GLKVM}
  SSO_USER="$SSO_USER" SSO_PASS="$SSO_PASS" USB_LABEL="$USB_LABEL" \
  CONFIG_PATH="$CONFIG_PATH" "$PYTHON" - <<'PYEOF'
import json, os
cfg = {
    "sso": {"username": os.environ["SSO_USER"], "password": os.environ["SSO_PASS"]},
    "usb": {"label": os.environ["USB_LABEL"], "fallback_dir": ""},
}
path = os.environ["CONFIG_PATH"]
with open(path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
print("      已写入 " + path)
PYEOF
  chown "$APP_USER":"$APP_USER" "$CONFIG_PATH" 2>/dev/null || true
fi

# ---- 4. systemd unit（usb-agent 常驻）----
echo "[4/4] 安装 systemd unit ..."
# agent 以桌面用户运行，状态直接写用户目录，不再需要 root→用户的镜像。
MIRROR_STATE=""
if [[ -n "$APP_HOME" && -d "$APP_HOME" ]]; then
  mkdir -p "$APP_HOME/.local/share/uvmp-toolkit"
  chown -R "$APP_USER":"$APP_USER" "$APP_HOME/.local/share/uvmp-toolkit" 2>/dev/null || true
fi
chown "$APP_USER":"$APP_USER" "$CONFIG_PATH" 2>/dev/null || true

# 允许桌面用户在无活动会话（锁屏/无人值守）时经 udisks 挂载虚拟 U 盘。
# 同时写 JS .rules 与旧版 .pkla：麒麟 V10 的 polkit 版本不一，单写一种可能被静默忽略。
if [[ -f "$SCRIPT_DIR/polkit.sh" ]]; then
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/polkit.sh"
  install_polkit_rule "$APP_USER"
fi

sed -e "s|@CONFIG_PATH@|$CONFIG_PATH|g" \
    -e "s|@RUN_CMD@|$PYTHON $PROJECT_DIR/toolkit/app.py|g" \
    -e "s|@APP_USER@|$APP_USER|g" -e "s|@APP_HOME@|$APP_HOME|g" \
    -e "s|@MIRROR_STATE@|$MIRROR_STATE|g" \
    "$SCRIPT_DIR/uvmp-toolkit.service" > /etc/systemd/system/uvmp-toolkit.service
# 旧版为每分钟 timer；升级时停用并移除，避免与新常驻 agent 双跑。
systemctl disable --now uvmp-toolkit.timer >/dev/null 2>&1 || true
rm -f /etc/systemd/system/uvmp-toolkit.timer
systemctl daemon-reload
systemctl enable uvmp-toolkit.service
systemctl restart uvmp-toolkit.service

# ---- 旧定时器迁移（避免双份导出写同名文件）----
if systemctl list-unit-files 2>/dev/null | grep -q "^export-dispatch-orders\.timer"; then
  echo "检测到旧版 export-dispatch-orders.timer（playwright 方案）"
  read -r -p "停用旧定时器并切换到本工具箱？[Y/n] " ANS
  if [[ "${ANS:-Y}" =~ ^[Yy]?$ ]]; then
    systemctl disable --now export-dispatch-orders.timer || true
    echo "旧定时器已停用（脚本本体保留，可手动回退）"
  else
    echo "保留旧定时器——注意：两套同时运行会重复导出同名文件！"
  fi
fi

echo "============================================================"
echo "安装完成！（源码 CLI 模式：U 盘单监听 agent）"
echo "  查看服务:     systemctl status uvmp-toolkit.service --no-pager"
echo "  手动检查一次: $PYTHON $PROJECT_DIR/toolkit/app.py usb-agent --once"
echo "  查看日志:     journalctl -u uvmp-toolkit.service"
echo "  GUI 客户端请使用 Release 的 .deb 安装包"
echo "============================================================"
