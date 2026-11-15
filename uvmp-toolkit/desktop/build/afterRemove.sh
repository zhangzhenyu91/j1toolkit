#!/bin/bash
# 卸载后清理：停用 usb-agent 与旧定时器 unit（用户配置 ~/.config/uvmp-toolkit/config.json 保留）
# 与 afterInstall.sh 对称：同时撤掉 /usr/bin 命令链接并刷新桌面/图标缓存。
#
# 图片文件关联（File Association）的清理口径：
#   - 安装包只声明「本应用能打开哪些格式」（deb 的 MimeType / MIME XML），**不改默认打开方式**；
#     默认项是用户自己在图片工具「设置」里点一次后才由程序调 `xdg-mime default` 写入的。
#   - dpkg 会在**升级**时以 `postrm upgrade <旧版本>` 调用本脚本：那时新版本马上写回关联，
#     清掉只会让用户的默认项闪断（甚至被别的看图软件抢走）。所以只在 remove / purge /
#     disappear 时清理 MIME，upgrade / abort-* / failed-upgrade 一律不动。
#   - 真正卸载时：默认项仍指向本工具才撤掉，否则只删自己的登记行。
#   - 清理只改含有本工具条目的行；注释、无关行、不以分号结尾的值、共享行的其他程序段原样保留。
#   - mimeapps.list 可能是软链（用户把配置目录整体指向别处）：改真实文件，不把软链换成普通文件。
APP_DIR="/opt/uvmp-toolkit"
EXE_NAME="uvmp-toolkit"
# 与 electron-builder 生成的 .desktop 文件名一致（linux.executableName / desktopName）
DESKTOP_FILE="uvmp-toolkit.desktop"
# dpkg 传给 postrm 的第一个参数；手工执行（无参数）按卸载处理
UVMP_POSTRM_ACTION="${1:-}"

# 是否该清理 MIME 关联：只有真正移除软件包时才清。
uvmp_should_clean_mime() {
  case "$UVMP_POSTRM_ACTION" in
    # dpkg 在 remove / purge 时给的是 "remove" / "purge"；个别包管理器还会用 disappear
    remove | purge | disappear | "")
      return 0
      ;;
    *)
      # upgrade / failed-upgrade / abort-upgrade / abort-remove …：保留现状
      return 1
      ;;
  esac
}

# 从 mimeapps.list 删掉本工具的条目。
# 参数：目标文件；文件里没有本工具条目时一个字节都不改（返回 1 表示未改动）。
# 只重写「值里含本工具」的行，其余行按原样输出（含注释、空行、无分号结尾的值）。
uvmp_strip_mimeapps() {
  local file="$1" target tmp
  [ -f "$file" ] || return 1
  # 软链要改真实文件：直接 mv 会把链接替换成普通文件，用户的配置目录布局被悄悄改掉
  target=$(readlink -f "$file" 2>/dev/null || printf '%s' "$file")
  [ -f "$target" ] || return 1
  grep -qF "$DESKTOP_FILE" "$target" 2>/dev/null || return 1
  tmp=$(mktemp "${target}.uvmp-XXXXXX") || return 1
  awk -v tool="$DESKTOP_FILE" '
    {
      # 只处理「键=值」且值里真的含本工具的行；其余原样打印
      eq = index($0, "=")
      if (eq == 0 || index($0, tool) == 0 || substr($0, 1, 1) == "#") { print; next }
      key = substr($0, 1, eq - 1)
      value = substr($0, eq + 1)
      # 拆开分号分隔的列表，只丢弃本工具那一段；其他段保持原顺序与原文。
      # 原值是否以分号结尾也照原样保留（mimeapps.list 两种写法都合法）。
      trailing = (value ~ /;[[:space:]]*$/)
      n = split(value, parts, ";")
      kept = ""
      kept_any = 0
      for (i = 1; i <= n; i++) {
        item = parts[i]
        if (item == "") continue
        if (item == tool) continue
        kept = kept (kept_any ? ";" : "") item
        kept_any = 1
      }
      if (!kept_any) next          # 整行只指向本工具 → 整行删除
      if (trailing) kept = kept ";"
      print key "=" kept
    }
  ' "$target" > "$tmp" || { rm -f "$tmp"; return 1; }
  # 原子替换，保留原文件权限与属主（非 root 改动会失败，忽略即可）
  chown --reference="$target" "$tmp" 2>/dev/null || true
  chmod --reference="$target" "$tmp" 2>/dev/null || true
  mv -f "$tmp" "$target" || { rm -f "$tmp"; return 1; }
  return 0
}

systemctl disable --now uvmp-toolkit.timer 2>/dev/null || true
systemctl stop uvmp-toolkit.service 2>/dev/null || true
rm -f /etc/systemd/system/uvmp-toolkit.service /etc/systemd/system/uvmp-toolkit.timer
rm -f /etc/polkit-1/rules.d/50-uvmp-toolkit.rules
rm -f /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla
systemctl daemon-reload 2>/dev/null || true

if type update-alternatives >/dev/null 2>&1; then
  # 必须用 afterInstall 注册的那个路径（/opt 下的真实可执行文件）：写成 /usr/bin 链接会让
  # update-alternatives 认不出这条记录，/etc/alternatives 里留下指向已删程序的死链。
  update-alternatives --remove "$EXE_NAME" "$APP_DIR/$EXE_NAME" 2>/dev/null || true
fi
rm -f "/usr/bin/$EXE_NAME"

# 图片文件关联的条件清理：只撤「仍指向本工具」的默认项与自己的登记行，
# 用户已改指其他看图软件的选择一律保留（不去改 xdg-mime 默认值）。
# 升级时整段跳过（见文件头说明）。
if uvmp_should_clean_mime; then
  for f in "/etc/xdg/mimeapps.list" /usr/share/applications/mimeapps.list \
           "$HOME/.config/mimeapps.list" "$HOME/.local/share/applications/mimeapps.list"; do
    uvmp_strip_mimeapps "$f" || true
  done
  # 多用户机器：/home 下其他桌面用户也设过默认的话一并清（同样是条件清理）
  for f in /home/*/.config/mimeapps.list /home/*/.local/share/applications/mimeapps.list; do
    [ -f "$f" ] || continue
    uvmp_strip_mimeapps "$f" || true
  done
fi

update-mime-database /usr/share/mime 2>/dev/null || true
update-desktop-database /usr/share/applications 2>/dev/null || true
gtk-update-icon-cache -q -t /usr/share/icons/hicolor 2>/dev/null || true
if command -v xdg-desktop-menu >/dev/null 2>&1; then
  xdg-desktop-menu forceupdate 2>/dev/null || true
fi

if uvmp_should_clean_mime; then
  echo "Shade 壹匣 - 内网 已卸载（用户配置保留：~/.config/uvmp-toolkit/config.json）"
else
  echo "Shade 壹匣 - 内网 升级中（$UVMP_POSTRM_ACTION）：保留文件关联与系统配置"
fi
exit 0
