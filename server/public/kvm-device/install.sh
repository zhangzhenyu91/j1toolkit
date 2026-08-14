#!/bin/sh
# ============================================================
# GL KVM 设备一键接入（Shade 壹匣集成）
#
# 作用：
#   1) 关闭设备 Web UI 登录认证（远程控制直达控制界面，不再二次登录）
#   2) 安装文件分享 API（push 上传 / list 列表 / download 下载 / status 状态）
#   3) 注入壹匣主题 CSS（j1-theme.css，覆盖 Web UI 为安全橙风格）
#   4) 嵌入 Call Me 浮窗（LangBot，bot.j1net.com，无需令牌）
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

# 2b) 注入壹匣主题（覆盖 Web UI 为安全橙风格）
# 原理见 j1-theme.css 头部注释：CSS 变量 !important 覆盖，不改 JS
info "    注入壹匣主题 CSS"
mkdir -p /etc/kvmd/user/theme
fetch j1-theme.css /etc/kvmd/user/theme/j1-theme.css
INDEX=/usr/share/kvmd/glweb/index.html
if [ -f "$INDEX" ]; then
    if grep -qF 'j1-theme.css' "$INDEX"; then
        info "    index.html 已引用主题，跳过"
    else
        [ -f "${INDEX}.bak-j1theme" ] || cp "$INDEX" "${INDEX}.bak-j1theme"
        sed -i 's|</head>|<link rel="stylesheet" href="/j1-theme.css" /></head>|' "$INDEX" \
            || fail "index.html 注入主题引用失败"
        # 同步更新预压缩副本，避免旧 .gz 被优先返回
        [ -f "${INDEX}.gz" ] && gzip -c "$INDEX" > "${INDEX}.gz"
        info "    已向 index.html 注入主题引用（备份：${INDEX}.bak-j1theme）"
    fi
else
    info "    警告：未找到 $INDEX，仅落地 CSS（固件布局变更时需人工检查）"
fi

# 2b-2) 补丁前端主题常量：antd token 与品牌图标的蓝紫色是构建时内联进 JS/CSS 的，
# CSS 变量覆盖不到，需直接替换构建产物中的色值（键名带 hash，固件升级后重跑即可）
info "    补丁前端主题色常量（蓝紫 → 安全橙）"
ASSETS=/usr/share/kvmd/glweb/assets
COLOR_PAIRS="5271EC:F26D21 A8B8F5:F98A4B 26367A:D95E15 CBD4F9:F8C9A6 DCE2FC:FDEEE2 \
4660C9:F26D21 384CA0:F98A4B 909FDE:F8C9A6 1C2650:22314E 859BF5:F98A4B \
E04C7E:CF4444 FFF0F2:FBEDEA 00C8B5:3FA66A E6FFF8:EAF5EF \
F3F3F4:F4F1EA EEEEF0:F8F5EC E0E1E5:E3DCCB C44671:CF4444 37262C:3A2426"
SED_RULES=""
ALT=""
for pair in $COLOR_PAIRS; do
    src=${pair%%:*}; dst=${pair##*:}
    up=$(printf '%s' "$src" | tr 'a-f' 'A-F'); lo=$(printf '%s' "$src" | tr 'A-F' 'a-f')
    SED_RULES="$SED_RULES s|#$up|#$dst|g; s|#$lo|#$dst|g; s|%23$up|%23$dst|g; s|%23$lo|%23$dst|g;"
    ALT="${ALT:+$ALT|}$up"
done
if [ -d "$ASSETS" ]; then
    find "$ASSETS" \( -name '*.js' -o -name '*.css' -o -name '*.svg' \) | while read -r f; do
        grep -qiE "(#|%23)($ALT)" "$f" || continue
        [ -f "${f}.bak-j1theme" ] || cp "$f" "${f}.bak-j1theme"
        sed -i "$SED_RULES" "$f"
        [ -f "${f}.gz" ] && gzip -c "$f" > "${f}.gz"
        info "    已补丁：$(basename "$f")"
    done
fi

# 2c) 嵌入 Call Me 浮窗（LangBot）：shadow DOM 自带样式，无需令牌
# 必须带 defer：脚本在 <head> 内，同步执行时 document.body 尚未解析，浮窗初始化会报错
WIDGET_TAG='<script defer data-title="Call Me" src="https://bot.j1net.com/api/v1/embed/703eb087-cb29-49b1-b0f4-4955d744db88/widget.js"></script>'
if [ -f "$INDEX" ]; then
    if grep -qF 'bot.j1net.com/api/v1/embed' "$INDEX"; then
        info "    浮窗脚本已注入，跳过"
    else
        # 旧版 WeKnora 浮窗（含缺 defer 的形态）先移除，再注入 LangBot
        grep -qF 'weknora-widget.js' "$INDEX" && \
            sed -i 's|<script defer src="https://know.j1net.com/weknora-widget.js"[^>]*></script>||; s|<script src="https://know.j1net.com/weknora-widget.js"[^>]*></script>||' "$INDEX"
        sed -i "s|</head>|$WIDGET_TAG</head>|" "$INDEX" \
            || fail "index.html 注入浮窗脚本失败"
        [ -f "${INDEX}.gz" ] && gzip -c "$INDEX" > "${INDEX}.gz"
        info "    已向 index.html 注入 Call Me 浮窗"
    fi
fi

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
THEME=$(curl -sk https://127.0.0.1/j1-theme.css | grep -c 'F26D21')
[ "$THEME" -gt 0 ] && info "    壹匣主题 CSS（443 /j1-theme.css）：正常" || fail "自检：/j1-theme.css 未取到主题内容"
curl -sk https://127.0.0.1/ | grep -qF 'j1-theme.css' \
    && info "    首页主题引用：已注入" || info "    警告：首页未见主题引用（如刚升级固件请重跑本脚本）"
curl -sk https://127.0.0.1/ | grep -qF 'bot.j1net.com/api/v1/embed' \
    && info "    Call Me 浮窗：已注入" || info "    警告：首页未见浮窗脚本（如刚升级固件请重跑本脚本）"

info "完成。文件分享 API："
info "    推送  curl -F \"files=@文件\" http://<设备IP>:8901/push"
info "    列表  curl http://<设备IP>:8901/list"
info "    下载  curl -OJ http://<设备IP>:8901/download/<URL编码文件名>"
