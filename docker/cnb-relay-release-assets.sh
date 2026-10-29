#!/usr/bin/env bash
# Shade 壹匣 · CNB Release 附件接力共用脚本（2026-10-29 自 .cnb.yml 内联脚本提取，双通道复用）：
#   1) tag_push 接力：正式 tag 回推 CNB 后，「上传 Release 附件」流水线调用，从 GitHub Release
#      下载客户端安装包 → 传 CNB Release 附件（预签名 URL 三段式上传）；
#   2) api_trigger 补传：Fyne 灰度 publish 三平台附件就位后，GitHub 侧 release.yml 的
#      fyne-cnb-relay job 以 CNB CLI（cnb build start-build --event api_trigger）触发，
#      对同一 GitHub Release 重新取件补传（overwrite 幂等），断言正式 3 件 + Fyne 3 件。
# 不重建镜像、不动 tag；tag 删除/移动一律禁止（发版纪律）。
#
# 入参（环境变量）：
#   RELAY_TAG           GitHub Release 的 tag 名；缺省取 CNB_BRANCH（tag_push 与 api_trigger
#                       传 --tag 时 CNB_BRANCH 均为 tag 名；release.yml 另经 --env 显式传入兜底）
#   RELAY_REQUIRE_FYNE  1=断言正式 3 件 + Fyne 3 件（api_trigger 补传通道：publish 后必有
#                       -fyne- 3 件，0 件或其他数量即失败）；缺省 0=双轨兼容断言（正式恒 3 件、
#                       Fyne 0 或 3 件，tag 常规发版/灰度 off·build 走 0 件分支）
# 依赖流水线内置 CNB_TOKEN（api_trigger 属可信事件，含 repo-release:rw）与 CNB_REPO_SLUG。
#
# 取件按 GitHub Release 资产清单过滤 exe/deb 逐个下载，不拼精确文件名：
#   资产名模板见 uvmp-toolkit/desktop/electron-builder.yml（uvmp-toolkit-Setup-<版本>.exe、
#   uvmp-toolkit_<版本>_{amd64,arm64}.deb），其中 <版本> 取 desktop/package.json 的 version，
#   与发版 tag 版本可脱钩（v1.10.0 的资产实名 1.9.6，按 tag 拼名下载 404——已踩坑）。
#   公开仓匿名列清单即可；若遇 GitHub API 限流，在密钥仓库 envs.yml 加 GITHUB_TOKEN 会自动带上。

TAG="${RELAY_TAG:-${CNB_BRANCH:-}}"
[ -n "$TAG" ] || { echo "RELAY_TAG 与 CNB_BRANCH 均为空，无法确定 GitHub Release tag"; exit 1; }
[ -n "${CNB_TOKEN:-}" ] || { echo "CNB_TOKEN 未注入"; exit 1; }
[ -n "${CNB_REPO_SLUG:-}" ] || { echo "CNB_REPO_SLUG 未注入"; exit 1; }
VERSION=$(echo "$TAG" | sed -E 's/^(release-)?v//')
echo "附件接力：GitHub Release ${TAG}（版本 ${VERSION}）→ CNB Release；Fyne 断言模式 RELAY_REQUIRE_FYNE=${RELAY_REQUIRE_FYNE:-0}"
command -v jq >/dev/null || { apt-get update -qq && apt-get install -y -qq jq; }
mkdir -p /tmp/arts && cd /tmp/arts
LIST_URL="https://api.github.com/repos/zhangzhenyu91/j1toolkit/releases/tags/${TAG}"
if [ -n "${GITHUB_TOKEN:-}" ]; then
  ASSETS=$(curl -sfL --connect-timeout 15 --max-time 60 -H "Authorization: Bearer ${GITHUB_TOKEN}" "$LIST_URL" \
    | jq -r '.assets[]?.name | select(test("\\.(exe|deb)$"))')
else
  ASSETS=$(curl -sfL --connect-timeout 15 --max-time 60 "$LIST_URL" | jq -r '.assets[]?.name | select(test("\\.(exe|deb)$"))')
fi
# API 限流/失败时改读公开发布页的资产清单，仍按真实文件名过滤，不回退到易错的按版本拼名
if [ -z "$ASSETS" ]; then
  echo "GitHub API 未取到附件清单，改读公开 expanded_assets"
  ASSETS=$(curl -sfL --connect-timeout 15 --max-time 60 \
    "https://github.com/zhangzhenyu91/j1toolkit/releases/expanded_assets/${TAG}" \
    | sed -nE 's#.*/releases/download/[^/]*/([^"/?]*[.](exe|deb))".*#\1#p' | sort -u)
fi
[ -n "$ASSETS" ] || { echo "GitHub Release ${TAG} 无 exe/deb 资产（API 与公开页面均未取到）"; exit 1; }
for f in $ASSETS; do
  downloaded=0
  for t in 1 2 3; do
    rm -f -- "$f"
    if curl -sfL --connect-timeout 20 --max-time 300 -o "$f" "https://github.com/zhangzhenyu91/j1toolkit/releases/download/${TAG}/${f}" && [ -s "$f" ]; then
      downloaded=1
      break
    fi
    echo "重试下载 $f（第 $t 次）"; sleep 10
  done
  [ "$downloaded" -eq 1 ] || { rm -f -- "$f"; echo "下载失败 $f"; exit 1; }
  echo "已下载 $f（$(du -h "$f" | cut -f1)）"
done
API="https://api.cnb.cool/${CNB_REPO_SLUG}"
AH="Authorization: Bearer ${CNB_TOKEN}"
# CNB Release 由同事件「服务端镜像」流水线的 git:release 创建，等它就绪（最多 10 分钟；
# 本流水线先跑完时自建兜底——撞车则由 git:release 复用/覆盖）
REL_ID=""
for i in $(seq 1 10); do
  REL_ID=$(curl -s -H "$AH" -H "Accept: application/vnd.cnb.api+json" "${API}/-/releases/tags/${TAG}" | jq -r '.id // empty')
  [ -n "$REL_ID" ] && break
  curl -s -X POST -H "$AH" -H "Accept: application/vnd.cnb.api+json" -H "Content-Type: application/json" \
    "${API}/-/releases" -d "{\"tag_name\":\"${TAG}\",\"name\":\"v${VERSION}\"}" > /dev/null
  REL_ID=$(curl -s -H "$AH" -H "Accept: application/vnd.cnb.api+json" "${API}/-/releases/tags/${TAG}" | jq -r '.id // empty')
  [ -n "$REL_ID" ] && break
  echo "等 CNB Release 就绪（$i）"; sleep 60
done
[ -n "$REL_ID" ] || { echo "CNB Release 未就绪"; exit 1; }
# 空 glob 不展开会把 "*.exe" 字面量留给 stat/curl，报错后仍可能以 0 退出（伪成功）——
# nullglob 让未匹配数组为空；上传/确认关联一律 curl -f，HTTP 错误显式判失败退出
shopt -s nullglob
ALL=( *.exe *.deb )
# 双轨断言（2026-10-28）：正式件恒 3 件（1 exe + 2 deb，附件名不含 -fyne-）；
# Fyne 灰度轨 publish 时另有 -fyne- 中缀 3 件（Setup exe + 麒麟 deb×2），off/build 时
# 0 件。off 默认仍恰 3 件、不破坏现有正式 3 件套；两轨分开计数，任一轨数量不对即失败。
# RELAY_REQUIRE_FYNE=1（api_trigger 补传通道，2026-10-29）：Fyne 件必须恰 3 件——该通道
# 仅在 publish 三平台附件就位后触发，数量不对说明接力时机不对或附件不全，宁失败不误传。
OFFICIAL=(); FYNE=()
for f in "${ALL[@]}"; do
  case "$f" in
    *-fyne-*) FYNE+=("$f") ;;
    *) OFFICIAL+=("$f") ;;
  esac
done
case "$TAG" in
  v2.0.0-rc.3|release-v2.0.0-rc.3)
    # 本次 RC 明确 Fyne-only：只允许 0 件旧 Electron + 3 件 Fyne 安装包。
    [ "${#OFFICIAL[@]}" -eq 0 ] || { echo "Fyne-only RC 不应包含旧 Electron 附件，实际 ${#OFFICIAL[@]} 件（${OFFICIAL[*]}）"; exit 1; }
    [ "${#FYNE[@]}" -eq 3 ] || { echo "Fyne-only RC 附件应为 3 件，实际 ${#FYNE[@]} 件（${FYNE[*]}）"; exit 1; }
    ;;
  *)
    [ "${#OFFICIAL[@]}" -eq 3 ] || { echo "正式客户端附件应为 3 件，实际 ${#OFFICIAL[@]} 件（${OFFICIAL[*]}）"; exit 1; }
    case "${RELAY_REQUIRE_FYNE:-0}" in
      1)
        [ "${#FYNE[@]}" -eq 3 ] || { echo "Fyne 灰度附件应为 3 件（RELAY_REQUIRE_FYNE=1），实际 ${#FYNE[@]} 件（${FYNE[*]}）"; exit 1; }
        ;;
      *)
        case "${#FYNE[@]}" in
          0) ;;  # fyne_track=off/build（默认发版走此分支）：无灰度附件
          3) ;;  # fyne_track=publish：-fyne- Setup exe + 麒麟 deb×2
          *) echo "Fyne 灰度附件应为 0 或 3 件，实际 ${#FYNE[@]} 件（${FYNE[*]}）"; exit 1 ;;
        esac
        ;;
    esac
    ;;
esac
FILES=( "${OFFICIAL[@]}" "${FYNE[@]}" )
echo "附件取件双轨计数：正式 ${#OFFICIAL[@]} 件 + Fyne ${#FYNE[@]} 件，合计 ${#FILES[@]} 件"
for f in "${FILES[@]}"; do
  SIZE=$(stat -c%s "$f")
  # 预签名三段式：取上传地址 → PUT 文件 → 确认关联（overwrite 允许重跑覆盖）
  UP=$(curl -s -X POST -H "$AH" -H "Accept: application/vnd.cnb.api+json" -H "Content-Type: application/json" \
    "${API}/-/releases/${REL_ID}/asset-upload-url" \
    -d "{\"asset_name\":\"$f\",\"size\":$SIZE,\"overwrite\":true}")
  UPLOAD_URL=$(echo "$UP" | jq -r '.upload_url // empty')
  VERIFY_URL=$(echo "$UP" | jq -r '.verify_url // empty')
  [ -n "$UPLOAD_URL" ] && [ -n "$VERIFY_URL" ] || { echo "取上传地址失败 $f：$UP"; exit 1; }
  curl -sf -X PUT -T "$f" "$UPLOAD_URL" -o /dev/null || { echo "上传失败 $f"; exit 1; }
  curl -sf -X POST -H "$AH" -H "Accept: application/vnd.cnb.api+json" "$VERIFY_URL" -o /dev/null || { echo "确认关联失败 $f"; exit 1; }
  echo "已传 $f"
done
echo "CNB Release 附件 ${#FILES[@]} 件就绪"
