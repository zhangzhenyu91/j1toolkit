#!/usr/bin/env bash
# linux amd64/arm64 核心构建：debian:buster 容器 + 内置 CPython（python-build-standalone）
#
# 设计要点（踩坑史见《内网工具箱开发指南.md》）：
# - 所有设备跑同一个内置 Python——不再有「buster 系统 Python 3.7 vs 新机 3.14」的差异坑
#   （3.7 没有 Path.unlink(missing_ok=)，曾在麒麟核心包上崩掉官方渲染器）
# - 版本与资产 SHA256 只有一处：packaging/python-runtime.env（当前 20261110 的 CPython 3.15.2）。
#   **不允许回退 latest**——回退会让同一个 tag 在不同时间构建出不同解释器，冻结产物无法复现。
#   下载后校验 SHA256，不过即中止。
# - 常规依赖用纯 Python/预编译 wheel（全部钉版本，见 packaging/requirements-core.txt）；
#   课程自动学习另收编 Playwright/greenlet 与对应架构 Node driver
# - PyInstaller 6 自带预编译 bootloader（含 aarch64）；buster glibc 2.28 覆盖麒麟 V10 SP1
# - pystandalone gnu 构建兼容 glibc 2.17+
# - buster 源已归档走 archive.debian.org
# - 门禁在 build_unix.sh 内执行（pip check → Python unittest → 装配完整性 → selfcheck →
#   rpc ping → CDP 冒烟）；容器内通常没有宿主浏览器，CDP 冒烟以退出码 2 明确报告跳过
# - 纯 Electron+Python 轨：不依赖任何外部 Go 工作进程
set -euo pipefail

ARCH=$(dpkg --print-architecture 2>/dev/null || echo amd64)
case "$ARCH" in
  amd64) PYARCH=x86_64 ;;
  arm64) PYARCH=aarch64 ;;
  *) echo "不支持的架构: $ARCH"; exit 1 ;;
esac

echo "deb http://archive.debian.org/debian buster main" > /etc/apt/sources.list
echo "deb http://archive.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list
apt-get -o Acquire::Check-Valid-Until=false update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ca-certificates curl xz-utils python3 binutils   # 系统 python3 只用于下载辅助脚本；binutils 提供 PyInstaller 必需的 objdump

# ---- 内置 CPython：版本与 SHA256 单一来源 ----
# shellcheck source=/dev/null
. packaging/python-runtime.env
ASSET=$(python_runtime_asset "$PYARCH")
PY_SHA256=$(python_runtime_sha "$PYARCH")
# 资产名里的 + 必须编码成 %2B
URL="https://github.com/astral-sh/python-build-standalone/releases/download/${PYREL}/${ASSET/+/%2B}"
echo "内置 Python：${PY_TAG}（${PYARCH}）sha256=${PY_SHA256}"
mkdir -p "/opt/py${PYVER%%.*}"
rm -f /tmp/py-runtime.tar.gz
ok=0
# GitHub releases/download 边缘偶发整段 504：只做同 URL 重试，不改查「最新 release」
for i in 1 2 3; do
  if curl -fSL --retry 3 --retry-delay 5 --connect-timeout 20 --max-time 600 "$URL" -o /tmp/py-runtime.tar.gz \
     && echo "${PY_SHA256}  /tmp/py-runtime.tar.gz" | sha256sum -c - ; then
    ok=1
    break
  fi
  echo "内置 Python 下载/校验失败（第 ${i}/3 次）：${URL}" >&2
  rm -f /tmp/py-runtime.tar.gz
  sleep 10
done
[ "$ok" = "1" ] || { echo "内置 Python ${PY_TAG} 获取失败，构建中止（不回退 latest）" >&2; exit 1; }
tar -C "/opt/py${PYVER%%.*}" --strip-components=1 -xzf /tmp/py-runtime.tar.gz
rm -f /tmp/py-runtime.tar.gz
PYTHON="/opt/py${PYVER%%.*}/bin/python3"
# 这里的 --version 同时是 glibc 门禁：buster（glibc 2.28）跑不起来新解释器会直接报
# GLIBC_2.xx not found —— 那就必须换更老的 3.1x 或换基线镜像，不要靠改门禁绕过
"$PYTHON" --version || {
  echo "内置 Python ${PYVER} 在本基线镜像上无法运行（多为 glibc 版本过低），构建中止" >&2
  ldd --version | head -1 >&2 || true
  exit 1
}
# 解释器版本必须与钉的版本完全一致（防止缓存/镜像替换导致漂移）
"$PYTHON" -c "import sys; assert '.'.join(map(str, sys.version_info[:3])) == '${PYVER}', sys.version"

PYTHON="$PYTHON" bash packaging/build_unix.sh
