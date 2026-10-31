#!/usr/bin/env bash
# Shade 壹匣 · CNB Release 附件接力脚本（2026-10-29 自 .cnb.yml 内联脚本提取）：
#   正式 tag 回推 CNB 后，「上传 Release 附件」流水线调用，从 GitHub Release 下载
#   Electron+PyInstaller 客户端安装包 → 传 CNB Release 附件（预签名 URL 三段式上传）。
# 不重建镜像、不动 tag；tag 删除/移动一律禁止（发版纪律）。
#
# 入参（环境变量）：
#   CNB_BRANCH   GitHub Release 的 tag 名（tag_push 事件下即 tag 名）
# 依赖流水线内置 CNB_TOKEN（含 repo-release:rw）与 CNB_REPO_SLUG。
#
# 取件按 GitHub Release 资产清单过滤 exe/deb 逐个下载，不拼精确文件名：
#   资产名模板见 uvmp-toolkit/desktop/electron-builder.yml（uvmp-toolkit-Setup-<版本>.exe、
#   uvmp-toolkit_<版本>_{amd64,arm64}.deb），其中 <版本> 由 tag 发版 job 用 tag 版本注入
#   desktop/package.json（见 release.yml）；仍按 Release 实际资产名过滤，不按 tag 拼名下载。
#   公开仓匿名列清单即可；若遇 GitHub API 限流，在密钥仓库 envs.yml 加 GITHUB_TOKEN 会自动带上。

TAG="${CNB_BRANCH:-}"
[ -n "$TAG" ] || { echo "CNB_BRANCH 为空，无法确定 GitHub Release tag"; exit 1; }
[ -n "${CNB_TOKEN:-}" ] || { echo "CNB_TOKEN 未注入"; exit 1; }
[ -n "${CNB_REPO_SLUG:-}" ] || { echo "CNB_REPO_SLUG 未注入"; exit 1; }
VERSION=$(echo "$TAG" | sed -E 's/^(release-)?v//')
echo "附件接力：GitHub Release ${TAG}（版本 ${VERSION}）→ CNB Release"
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
# 正式三件口径（与 release.yml / desktop/electron-builder.yml 一致）：
#   uvmp-toolkit-Setup-<版本>.exe、uvmp-toolkit_<版本>_amd64.deb、uvmp-toolkit_<版本>_arm64.deb
# 任何未知命名或数量不符都判失败，宁失败不误传。
UNKNOWN=(); SETUP=(); DEB_AMD=(); DEB_ARM=()
for f in "${ALL[@]}"; do
  case "$f" in
    uvmp-toolkit-Setup-*.exe) SETUP+=("$f") ;;
    uvmp-toolkit_*_amd64.deb) DEB_AMD+=("$f") ;;
    uvmp-toolkit_*_arm64.deb) DEB_ARM+=("$f") ;;
    *) UNKNOWN+=("$f") ;;
  esac
done
[ "${#UNKNOWN[@]}" -eq 0 ] || { echo "不认识的客户端附件：${UNKNOWN[*]}"; exit 1; }
[ "${#SETUP[@]}" -eq 1 ] && [ "${#DEB_AMD[@]}" -eq 1 ] && [ "${#DEB_ARM[@]}" -eq 1 ] \
  || { echo "正式客户端应为 3 件：Setup exe ${#SETUP[@]} 件 / amd64 deb ${#DEB_AMD[@]} 件 / arm64 deb ${#DEB_ARM[@]} 件"; exit 1; }
FILES=( "${SETUP[@]}" "${DEB_AMD[@]}" "${DEB_ARM[@]}" )
echo "附件取件：正式三件（${FILES[*]}）"
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
