#!/usr/bin/env bash
# linux amd64/arm64 核心构建：debian:buster 容器 + 内置 CPython 3.12（python-build-standalone）
#
# 设计要点（踩坑史见《内网工具箱开发指南.md》）：
# - 所有设备跑同一个内置 Python 3.12——不再有「buster 系统 Python 3.7 vs 新机 3.14」的差异坑
#   （3.7 没有 Path.unlink(missing_ok=)，曾在麒麟核心包上崩掉官方渲染器）
# - 核心依赖全是纯 Python wheel（无 PyQt5/Qt），PyInstaller 6 自带预编译 bootloader（含 aarch64）
# - buster 基线 glibc 2.28 覆盖麒麟 V10 SP1；pystandalone gnu 构建兼容 glibc 2.17+
# - buster 源已归档走 archive.debian.org；GitHub API 在 runner 上可达
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

# 内置 Python 3.12：固定 python-build-standalone 版本直连（GitHub API 匿名限流 60 次/时，CNB 共享出口易撞 403）；
# 直连失败回退 API 查最新 release（升级内置 Python 时改 PYVER/PYREL 两个变量即可）
PYVER=3.12.15
PYREL=20261001
mkdir -p /opt/py312
if ! curl -fSL "https://github.com/astral-sh/python-build-standalone/releases/download/${PYREL}/cpython-${PYVER}%2B${PYREL}-${PYARCH}-unknown-linux-gnu-install_only_stripped.tar.gz" \
     -o /tmp/py312.tar.gz; then
python3 - "$PYARCH" <<'PYEOF'
import json, sys, urllib.request
arch = sys.argv[1]
rel = json.load(urllib.request.urlopen(
    "https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest"))
want = None
for a in rel["assets"]:
    n = a["name"]
    if (n.startswith("cpython-3.12.") and ("-%s-unknown-linux-gnu-" % arch) in n
            and n.endswith("install_only_stripped.tar.gz")):
        want = a["browser_download_url"]
        break
assert want, "未找到 %s 的 cpython-3.12 资产" % arch
print("下载内置 Python:", want)
urllib.request.urlretrieve(want, "/tmp/py312.tar.gz")
PYEOF
fi
tar -C /opt/py312 --strip-components=1 -xzf /tmp/py312.tar.gz
/opt/py312/bin/python3 --version

PYTHON=/opt/py312/bin/python3 bash packaging/build_unix.sh
