#!/usr/bin/env bash
# 允许客户端桌面用户经 udisks2 挂载/卸载 KVM 虚拟 U 盘（锁屏/无活动会话也生效）。
#
# 两套规则同时写，按发行版实际支持的机制生效：
# - /etc/polkit-1/rules.d/50-uvmp-toolkit.rules        polkit >= 0.106 的 JS 规则引擎
# - /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla
#                                                      polkit 0.105（银河麒麟 V10 常见）的 .pkla；
#                                                      新版由 polkit-pkla-compat 读取，读不到则忽略
# 规则不生效时 udisksctl 报 NotAuthorizedCanObtain，agent 会把原因写进日志/心跳的 last_error。
# 用法：. polkit.sh && install_polkit_rule "$APP_USER"
install_polkit_rule() {
  local user="$1"
  if [ -z "$user" ] || [ "$user" = "root" ]; then
    return 0
  fi

  if [ -d /etc/polkit-1/rules.d ]; then
    cat > /etc/polkit-1/rules.d/50-uvmp-toolkit.rules <<EOF
polkit.addRule(function(action, subject) {
  if ((action.id.indexOf("org.freedesktop.udisks2.filesystem-mount") === 0 ||
       action.id.indexOf("org.freedesktop.udisks2.filesystem-unmount") === 0) &&
      subject.user == "$user") return polkit.Result.YES;
});
EOF
    chmod 644 /etc/polkit-1/rules.d/50-uvmp-toolkit.rules
  fi

  mkdir -p /etc/polkit-1/localauthority/50-local.d
  cat > /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla <<EOF
[uvmp-toolkit: udisks mount]
Identity=unix-user:$user
Action=org.freedesktop.udisks2.filesystem-mount;org.freedesktop.udisks2.filesystem-mount-system;org.freedesktop.udisks2.filesystem-unmount;org.freedesktop.udisks2.filesystem-unmount-others
ResultAny=yes
ResultInactive=yes
ResultActive=yes
EOF
  chmod 644 /etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla

  # 规则变更即时生效；老版本 polkit 对 .pkla 目录不一定有 inotify，重启一次兜底。
  systemctl try-restart polkit >/dev/null 2>&1 \
    || systemctl try-restart polkitd >/dev/null 2>&1 || true

  if command -v pkaction >/dev/null 2>&1; then
    echo "      polkit: $(pkaction --version 2>/dev/null | tail -1)（规则已写入 rules.d + 50-local.d）"
    echo "      校验授权: pkcheck --action-id org.freedesktop.udisks2.filesystem-mount --process \$\$"
  else
    echo "      警告：未找到 pkaction，无法确认 polkit 规则；udisks 挂载可能被拒绝"
  fi
}
