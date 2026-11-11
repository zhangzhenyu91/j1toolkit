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
# 取件按 GitHub Release 资产清单过滤 exe/deb/SHA256SUMS 逐个下载，不拼精确文件名：
#   资产名模板见 uvmp-toolkit/desktop/electron-builder.yml（uvmp-toolkit-Setup-<版本>.exe、
#   uvmp-toolkit_<版本>_{amd64,arm64}.deb），其中 <版本> 由发版 job 用 tag 版本注入
#   desktop/package.json（见 release.yml）；仍按 Release 实际资产名过滤，不按 tag 拼名下载。
#   SHA256SUMS 由 release-final / client-final 统一生成并校验后上传，本脚本下载后
#   **必须校验通过才继续上传**，并把它一并挂到 CNB Release（国内取件方据此验完整性）。
#   公开仓匿名列清单即可；若遇 GitHub API 限流，在密钥仓库 envs.yml 加 GITHUB_TOKEN 会自动带上。
#
# 下载路径不回退公共反代（ghfast.top / gh-proxy.com 之类）：第三方反代能读到全部发版流量、
# 可篡改二进制，且随时失效；只走 api.github.com 的资产签名地址与官方 releases/download。

TAG="${CNB_BRANCH:-}"
[ -n "$TAG" ] || { echo "CNB_BRANCH 为空，无法确定 GitHub Release tag"; exit 1; }
[ -n "${CNB_TOKEN:-}" ] || { echo "CNB_TOKEN 未注入"; exit 1; }
[ -n "${CNB_REPO_SLUG:-}" ] || { echo "CNB_REPO_SLUG 未注入"; exit 1; }
VERSION=$(echo "$TAG" | sed -E 's/^(release-)?v//')
echo "附件接力：GitHub Release ${TAG}（版本 ${VERSION}）→ CNB Release"
command -v jq >/dev/null || { apt-get update -qq && apt-get install -y -qq jq; }
mkdir -p /tmp/arts && cd /tmp/arts
LIST_URL="https://api.github.com/repos/zhangzhenyu91/j1toolkit/releases/tags/${TAG}"
RELEASE_JSON=""
if [ -n "${GITHUB_TOKEN:-}" ]; then
  RELEASE_JSON=$(curl -sfL --connect-timeout 15 --max-time 60 -H "Authorization: Bearer ${GITHUB_TOKEN}" "$LIST_URL") || true
else
  RELEASE_JSON=$(curl -sfL --connect-timeout 15 --max-time 60 "$LIST_URL") || true
fi
# 注意 SHA256SUMS 没有扩展名：写成 \.(exe|deb|SHA256SUMS)$ 会要求点号，
# 导致它永远匹配不上、后面的「必须 1 份 SHA256SUMS」断言必然失败（曾踩坑）
ASSETS=$(printf '%s' "$RELEASE_JSON" | jq -r '.assets[]?.name | select(test("\\.(exe|deb)$") or . == "SHA256SUMS")' 2>/dev/null || true)
# API 限流/失败时改读公开发布页的资产清单，仍按真实文件名过滤，不回退到易错的按版本拼名
if [ -z "$ASSETS" ]; then
  echo "GitHub API 未取到附件清单，改读公开 expanded_assets"
  ASSETS=$(curl -sfL --connect-timeout 15 --max-time 60 \
    "https://github.com/zhangzhenyu91/j1toolkit/releases/expanded_assets/${TAG}" \
    | sed -nE 's#.*/releases/download/[^/]*/([^"/?]*[.](exe|deb)|SHA256SUMS)".*#\1#p' | sort -u)
fi
[ -n "$ASSETS" ] || { echo "GitHub Release ${TAG} 无 exe/deb/SHA256SUMS 资产（API 与公开页面均未取到）"; exit 1; }
if [ -n "$RELEASE_JSON" ]; then
  printf '%s' "$RELEASE_JSON" > /tmp/arts/release.json
fi
for f in $ASSETS; do
  # 优先用 Asset API 的 302 签名地址：GitHub releases/download 边缘对国内经常整段 504，
  # 但不影响 api.github.com -> release-assets.githubusercontent.com 这条链路。
  API_ASSET=""
  if [ -s /tmp/arts/release.json ]; then
    API_ASSET=$(jq -r --arg n "$f" '.assets[]? | select(.name == $n) | .url' /tmp/arts/release.json 2>/dev/null | head -1)
  fi
  URLS=()
  [ -n "$API_ASSET" ] && URLS+=("$API_ASSET")
  URLS+=("https://github.com/zhangzhenyu91/j1toolkit/releases/download/${TAG}/${f}")
  downloaded=0
  for url in "${URLS[@]}"; do
    for t in 1 2; do
      rm -f -- "$f"
      if [[ "$url" == https://api.github.com/* && -n "${GITHUB_TOKEN:-}" ]]; then
        curl -sfL --retry 2 --retry-delay 5 --retry-all-errors --connect-timeout 20 --max-time 600 \
          -H "Accept: application/octet-stream" -H "Authorization: Bearer ${GITHUB_TOKEN}" \
          -o "$f" "$url" || true
      else
        curl -sfL --retry 2 --retry-delay 5 --retry-all-errors --connect-timeout 20 --max-time 600 \
          -H "Accept: application/octet-stream" -o "$f" "$url" || true
      fi
      if [ -s "$f" ]; then
        downloaded=1
        break 2
      fi
      echo "下载失败，重试 ${f}（${url} 第 ${t} 次）"
      sleep 10
    done
  done
  [ "$downloaded" -eq 1 ] || { rm -f -- "$f"; echo "下载失败 $f"; exit 1; }
  echo "已下载 $f（$(du -h "$f" | cut -f1)）"
done
# 空 glob 不展开会把 "*.exe" 字面量留给 stat/curl，报错后仍可能以 0 退出（伪成功）——
# nullglob 让未匹配数组为空；上传/确认关联一律 curl -f，HTTP 错误显式判失败退出
shopt -s nullglob
ALL=( *.exe *.deb SHA256SUMS )
# 正式三件口径（与 release.yml / desktop/electron-builder.yml 一致）：
#   uvmp-toolkit-Setup-<版本>.exe、uvmp-toolkit_<版本>_amd64.deb、uvmp-toolkit_<版本>_arm64.deb
# 另有 1 份 SHA256SUMS。任何未知命名或数量不符都判失败，宁失败不误传。
UNKNOWN=(); SETUP=(); DEB_AMD=(); DEB_ARM=(); SUMS=()
for f in "${ALL[@]}"; do
  case "$f" in
    SHA256SUMS) SUMS+=("$f") ;;
    uvmp-toolkit-Setup-*.exe) SETUP+=("$f") ;;
    uvmp-toolkit_*_amd64.deb) DEB_AMD+=("$f") ;;
    uvmp-toolkit_*_arm64.deb) DEB_ARM+=("$f") ;;
    *) UNKNOWN+=("$f") ;;
  esac
done
[ "${#UNKNOWN[@]}" -eq 0 ] || { echo "不认识的客户端附件：${UNKNOWN[*]}"; exit 1; }
[ "${#SETUP[@]}" -eq 1 ] && [ "${#DEB_AMD[@]}" -eq 1 ] && [ "${#DEB_ARM[@]}" -eq 1 ] && [ "${#SUMS[@]}" -eq 1 ] \
  || { echo "正式客户端应为 3 件安装包 + 1 份 SHA256SUMS：Setup exe ${#SETUP[@]} 件 / amd64 deb ${#DEB_AMD[@]} 件 / arm64 deb ${#DEB_ARM[@]} 件 / SHA256SUMS ${#SUMS[@]} 份"; exit 1; }
# 校验和断言：三件安装包必须与 SHA256SUMS 逐条对上，否则不上传（宁可失败不传错件）
grep -Ev 'SHA256SUMS$' SHA256SUMS > /tmp/arts/SHA256SUMS.check
[ "$(wc -l < /tmp/arts/SHA256SUMS.check)" -eq 3 ] \
  || { echo "SHA256SUMS 条目数不为 3（$(wc -l < /tmp/arts/SHA256SUMS.check)）"; exit 1; }
sha256sum -c /tmp/arts/SHA256SUMS.check || { echo "附件与 SHA256SUMS 不符，中止上传"; exit 1; }
echo "SHA256SUMS 校验通过（3 件）"
FILES=( "${SETUP[@]}" "${DEB_AMD[@]}" "${DEB_ARM[@]}" "${SUMS[@]}" )
echo "附件取件：正式三件 + SHA256SUMS（${FILES[*]}）"
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
echo "CNB Release 附件就绪：三件安装包 + SHA256SUMS"
