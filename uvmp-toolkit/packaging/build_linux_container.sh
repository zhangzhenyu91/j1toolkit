#!/usr/bin/env bash
# linux amd64/arm64 核心构建：debian:buster 容器 + 内置 CPython 3.12（python-build-standalone）
#
# 设计要点（踩坑史见《内网工具箱开发指南.md》）：
# - 所有设备跑同一个内置 Python 3.12——不再有「buster 系统 Python 3.7 vs 新机 3.14」的差异坑
#   （3.7 没有 Path.unlink(missing_ok=)，曾在麒麟核心包上崩掉官方渲染器）
# - 常规依赖用纯 Python wheel；课程自动学习另收编 Playwright/greenlet 与对应架构 Node driver
# - PyInstaller 6 自带预编译 bootloader（含 aarch64）；buster glibc 2.28 覆盖麒麟 V10 SP1
# - pystandalone gnu 构建兼容 glibc 2.17+
# - buster 源已归档走 archive.debian.org；GitHub API 在 runner 上可达
# - 门禁在 build_unix.sh 内执行（Python unittest → 装配完整性 → selfcheck → rpc ping →
#   CDP 冒烟）；容器内通常没有宿主浏览器，CDP 冒烟以退出码 2 明确报告跳过，不静默当通过
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

# 内置 Python 3.12：固定 python-build-standalone 版本直连（GitHub API 匿名限流 60 次/时，CNB 共享出口易撞 403）；
# 直连失败回退 API 查最新 release（升级内置 Python 时改 PYVER/PYREL 两个变量即可）。
# 2026-11-08：github.com 的 releases/download 边缘整段 504（CI 与实机同时踩到），
# 故直连带重试，失败后改走 API 资产端点（release-assets.githubusercontent.com，绕开该边缘），
# 再退最新 release；固定版本与最新版本都失败才报错。
PYVER=3.12.15
PYREL=20261001
ASSET="cpython-${PYVER}%2B${PYREL}-${PYARCH}-unknown-linux-gnu-install_only_stripped.tar.gz"
mkdir -p /opt/py312
if ! curl -fSL --retry 3 --retry-delay 5 --connect-timeout 20 \
     "https://github.com/astral-sh/python-build-standalone/releases/download/${PYREL}/${ASSET}" \
     -o /tmp/py312.tar.gz; then
  rm -f /tmp/py312.tar.gz
python3 - "$PYARCH" "$PYREL" <<'PYEOF'
import json, sys, time, urllib.request

arch, rel_tag = sys.argv[1], sys.argv[2]
API = "https://api.github.com/repos/astral-sh/python-build-standalone"


def fetch(url, accept, out=None, attempts=3, timeout=180):
    last = None
    for i in range(attempts):
        try:
            req = urllib.request.Request(
                url, headers={"Accept": accept, "User-Agent": "uvmp-toolkit-build"}
            )
            with urllib.request.urlopen(req, timeout=timeout) as r:
                if out is None:
                    return json.loads(r.read().decode("utf-8"))
                total = 0
                with open(out, "wb") as f:
                    while True:
                        chunk = r.read(1 << 20)
                        if not chunk:
                            break
                        f.write(chunk)
                        total += len(chunk)
                return total
        except Exception as e:  # noqa: BLE001
            last = e
            print("下载失败（第 %d/%d 次）：%s" % (i + 1, attempts, e), flush=True)
            if i + 1 < attempts:
                time.sleep(5)
    raise last


def download(tag):
    meta = fetch(API + "/releases/tags/" + tag, "application/vnd.github+json")
    asset = None
    for a in meta.get("assets") or []:
        name = a.get("name") or ""
        if (
            name.startswith("cpython-3.12.")
            and ("-%s-unknown-linux-gnu-" % arch) in name
            and name.endswith("install_only_stripped.tar.gz")
        ):
            asset = a
            break
    if asset is None:
        raise RuntimeError("release %s 无 %s 的 CPython 3.12 资产" % (tag, arch))
    print("下载内置 Python（API 资产）:", asset["name"], flush=True)
    got = fetch(
        API + "/releases/assets/%d" % asset["id"],
        "application/octet-stream",
        out="/tmp/py312.tar.gz",
    )
    if asset.get("size") and got != asset["size"]:
        raise RuntimeError("资产大小不符：%d != %d" % (got, asset["size"]))


try:
    download(rel_tag)
except Exception as e:  # noqa: BLE001
    print("固定版本下载失败：%s；改查最新 release" % e, flush=True)
    download("latest")
PYEOF
fi
tar -C /opt/py312 --strip-components=1 -xzf /tmp/py312.tar.gz
/opt/py312/bin/python3 --version

PYTHON=/opt/py312/bin/python3 bash packaging/build_unix.sh
