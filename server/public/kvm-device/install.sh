#!/bin/sh
# ============================================================
# GL KVM 设备一键接入（Shade 壹匣集成）
#
# 作用：
#   1) 关闭设备 Web UI 登录认证（远程控制直达控制界面，不再二次登录）
#   2) 安装文件分享 API（push 上传 / list 列表 / download 下载 / status 状态）
#
# 用法（SSH 登录设备后执行一行）：
#   curl -fsSL https://toolkit.j1net.com/kvm-device/install.sh | sh
#
# 说明：
#   - 幂等：重复执行安全；改动前先备份 /etc/kvmd/override.yaml
#   - 载荷地址可用 KVM_INSTALL_BASE 覆盖（内网镜像/测试用）
#   - 回退见 开发指南.md 第十二节（还原 override.yaml 备份 + 停服务删文件）
# ============================================================
set -e

BASE="${KVM_INSTALL_BASE:-https://toolkit.j1net.com/kvm-device}"
OVERRIDE=/etc/kvmd/override.yaml
MARK='# yixia: disable webui auth'
NEED_KVMD_RESTART=0

info() { printf '[壹匣接入] %s\n' "$*"; }
fail() { printf '[壹匣接入] 失败：%s\n' "$*" >&2; exit 1; }

# 环境检查：仅适用于 GL KVM（PiKVM 固件）设备
[ -d /etc/kvmd ] || fail "未找到 /etc/kvmd，本脚本仅适用于 GL KVM 设备"
command -v curl >/dev/null 2>&1 || fail "设备缺少 curl"

fetch() {
    # fetch <文件名> <落地路径>；CA 校验失败时回退 -k（内网环境）
    curl -fsSL "$BASE/$1" -o "$2" 2>/dev/null \
        || curl -fsSLk "$BASE/$1" -o "$2" \
        || fail "下载 $1 失败（$BASE）"
}

# 1) 关闭 Web UI 登录认证
info "1/4 关闭 Web UI 登录认证"
touch "$OVERRIDE"
if grep -qF "$MARK" "$OVERRIDE"; then
    info "    已关闭，跳过"
else
    [ -f "${OVERRIDE}.bak-noauth" ] || cp "$OVERRIDE" "${OVERRIDE}.bak-noauth"
    printf '\n%s\nkvmd:\n    auth:\n        enabled: false\n' "$MARK" >> "$OVERRIDE"
    # 开机配置恢复目录存在时同步覆盖，防止重启被还原
    [ -d /userdata/backup_config ] && cp "$OVERRIDE" /userdata/backup_config/override.yaml
    NEED_KVMD_RESTART=1
    info "    已写入 $OVERRIDE（备份：${OVERRIDE}.bak-noauth）"
fi

# 2) 安装文件分享 API
info "2/4 安装文件分享 API（push/list/download/status）"
mkdir -p /etc/kvmd/user/fileshare /usr/share/kvmd/extras/fileshare
fetch fileshare.py            /etc/kvmd/user/fileshare/fileshare.py
fetch S99fileshare            /etc/init.d/S99fileshare
fetch nginx.ctx-server.conf   /usr/share/kvmd/extras/fileshare/nginx.ctx-server.conf
fetch manifest.yaml           /usr/share/kvmd/extras/fileshare/manifest.yaml
chmod +x /etc/init.d/S99fileshare
/usr/bin/python3 -m py_compile /etc/kvmd/user/fileshare/fileshare.py || fail "fileshare.py 语法校验失败"

# 3) 重启相关服务
info "3/4 重启服务"
if [ "$NEED_KVMD_RESTART" = 1 ]; then
    /etc/init.d/S98kvmd restart || fail "kvmd 重启失败"
    sleep 2
fi
/etc/init.d/S99fileshare restart || fail "fileshare 启动失败"
if nginx -p /etc/kvmd/nginx -c /etc/kvmd/nginx-kvmd.conf -t 2>/dev/null; then
    kill -HUP "$(cat /run/kvmd/nginx.pid)" 2>/dev/null || true
else
    fail "nginx 配置校验失败（未重载）"
fi

# 4) 自检
info "4/4 自检"
sleep 2
AUTH=$(curl -sk https://127.0.0.1/api/info | grep -o '"enabled": *false' | head -1)
[ -n "$AUTH" ] && info "    登录认证：已关闭" || fail "自检：认证未关闭（/api/info 未见 enabled:false）"
STATUS=$(curl -s http://127.0.0.1:8901/status) || fail "自检：fileshare 直连无响应"
printf '%s\n' "$STATUS" | grep -q '"ok": true' || fail "自检：fileshare 响应异常：$STATUS"
info "    fileshare 直连（:8901）：$STATUS"
VIA443=$(curl -sk https://127.0.0.1/api/fileshare/status)
printf '%s\n' "$VIA443" | grep -q '"ok": true' || fail "自检：/api/fileshare 响应异常：$VIA443"
info "    经 nginx（443 /api/fileshare/）：正常"

info "完成。文件分享 API："
info "    推送  curl -F \"files=@文件\" http://<设备IP>:8901/push"
info "    列表  curl http://<设备IP>:8901/list"
info "    下载  curl -OJ http://<设备IP>:8901/download/<URL编码文件名>"
