# -*- coding: utf-8 -*-
"""平台相关工具：系统探测、Chrome 探测、U 盘（按卷标）发现/挂载、打开目录、常用目录建议。

U 盘逻辑移植自 export_dispatch_orders.py（卷标 GLKVM 的 KVM 虚拟 U 盘）：
- linux：lsblk 按卷标发现设备；未挂载则用 udisksctl 挂载（重试 3 次）
- windows：ctypes GetVolumeInformationW 按卷标扫盘符（尽力而为；找不到回退 fallback_dir）
"""
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path


def is_windows() -> bool:
    return sys.platform.startswith("win")


def is_linux() -> bool:
    return sys.platform.startswith("linux")


# ---------------------------------------------------------------- Chrome 探测

# Chrome/Chromium 及麒麟常见专用浏览器（按序探测；公司指定浏览器找不到时让用户在设置页手选）
CHROME_NAMES = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
                "qaxbrowser",            # 奇安信可信浏览器（麒麟软件商店常见）
                "browser360", "browser360-cn", "360chrome",   # 360 安全浏览器
                "microsoft-edge", "microsoft-edge-stable", "chrome"]

# 不在 PATH 时的已知绝对路径（麒麟软件商店/厂商默认安装位置）
LINUX_BROWSER_PATHS = [
    "/usr/bin/qaxbrowser",
    "/opt/qaxbrowser/qaxbrowser",
    "/opt/apps/qaxbrowser/qaxbrowser",
    "/usr/bin/browser360",
    "/opt/apps/com.360.browser-stable/files/browser360",
]

WINDOWS_CHROME_PATHS = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
]


def detect_chrome(preferred: str = "") -> str:
    """返回可用的浏览器路径（用于 PDF 渲染与官方页面直出）；找不到抛 RuntimeError。
    preferred 非空 = 用户在设置页/配置里指定的公司浏览器，优先且必须存在。"""
    if preferred:
        if Path(preferred).exists():
            return preferred
        raise RuntimeError("指定的浏览器不存在: %s（请在设置页重新选择）" % preferred)
    for name in CHROME_NAMES:
        found = shutil.which(name)
        if found:
            return found
    if is_windows():
        for p in WINDOWS_CHROME_PATHS:
            if Path(p).exists():
                return p
        local = os.environ.get("LOCALAPPDATA", "")
        if local:
            p = Path(local) / r"Google\Chrome\Application\chrome.exe"
            if p.exists():
                return str(p)
    else:
        for p in LINUX_BROWSER_PATHS:
            if Path(p).exists():
                return p
    raise RuntimeError("未找到浏览器：请在设置页手动选择（公司指定浏览器）")


def electron_binary() -> str:
    """Electron 应用本体路径（Electron 壳 spawn 核心时经 UVMP_ELECTRON 注入）。
    有它就能用应用自带的 Chromium 做离线 PDF 渲染，不再依赖系统 Chrome。"""
    env = os.environ.get("UVMP_ELECTRON", "")
    return env if env and Path(env).exists() else ""


def detect_pdf_renderer(render_cfg: dict, log=print) -> dict:
    """PDF 渲染器选择（优先级）：
    1. 用户在设置页手选的浏览器（公司指定浏览器，官方直出也需要它）
    2. 自带 Electron Chromium（离线兜底）
    3. 自动探测系统浏览器
    都没有则抛错。"""
    chosen = ((render_cfg or {}).get("chrome") or "").strip()
    if chosen:
        if not Path(chosen).exists():
            raise RuntimeError("指定的浏览器不存在: %s（请到设置页重新选择）" % chosen)
        log("PDF 渲染器：用户指定浏览器（%s）" % chosen)
        return {"kind": "chrome", "path": chosen}
    eb = electron_binary()
    if eb:
        log("PDF 渲染器：应用自带 Electron Chromium（%s）" % eb)
        return {"kind": "electron", "path": eb}
    chrome = detect_chrome("")
    log("PDF 渲染器：自动探测系统浏览器（%s）" % chrome)
    return {"kind": "chrome", "path": chrome}


# ---------------------------------------------------------------- U 盘（按卷标）

def _find_usb_linux(label: str, log) -> str:
    """linux：确保卷标为 label 的设备已挂载，返回挂载点；失败返回 None"""
    label = (label or "").upper()
    try:
        r = subprocess.run(["lsblk", "-J", "-o", "PATH,LABEL,MOUNTPOINT"],
                           capture_output=True, text=True, timeout=10)
        if r.returncode != 0:
            log("lsblk 执行失败: " + r.stderr.strip())
            return None
        import json as _json
        data = _json.loads(r.stdout)
    except Exception as e:
        log("查找U盘设备异常: %s" % e)
        return None

    def scan():
        for dev in data.get("blockdevices", []):
            for node in [dev] + (dev.get("children") or []):
                if (node.get("label") or "").upper() == label:
                    return node.get("path"), node.get("mountpoint")
        return None, None

    device, mountpoint = scan()
    for attempt in range(1, 4):
        if mountpoint:
            log("U盘已挂载: %s" % mountpoint)
            return mountpoint
        if not device:
            log("未找到卷标 %s 的U盘设备 (第 %d/3 次)" % (label, attempt))
        else:
            try:
                r = subprocess.run(["udisksctl", "mount", "-b", device],
                                   capture_output=True, text=True, timeout=30)
                if r.returncode == 0:
                    m = re.search(r"\bat\s+(.+?)\.?\s*$", r.stdout.strip())
                    actual = m.group(1) if m else ""
                    time.sleep(1)
                    if actual and os.path.ismount(actual):
                        log("U盘挂载成功: %s" % actual)
                        return actual
                    log("挂载后未检测到挂载点: %s" % actual)
                else:
                    log("挂载失败 (%s): %s" % (device, r.stderr.strip()))
            except subprocess.TimeoutExpired:
                log("挂载超时")
            except Exception as e:
                log("挂载异常: %s" % e)
        if attempt < 3:
            time.sleep(5)
    return None


def _find_usb_windows(label: str, log) -> str:
    """windows：PowerShell Get-Volume 按卷标找盘符，返回 'X:\\'；找不到返回 None。
    （不用 ctypes 调 Win32 API：PyInstaller/conda 环境下 _ctypes 的 libffi DLL 依赖脆弱，
    PowerShell 子进程零原生依赖，Win10+ 自带）"""
    label = (label or "").strip()
    if not label:
        return None
    try:
        r = subprocess.run(
            ["powershell", "-NoProfile", "-NonInteractive", "-Command",
             "(Get-Volume -FileSystemLabel '%s' -ErrorAction SilentlyContinue).DriveLetter"
             % label.replace("'", "''")],
            capture_output=True, text=True, timeout=20)
        letter = r.stdout.strip().splitlines()
        if r.returncode == 0 and letter and letter[0].strip():
            root = letter[0].strip() + ":\\"
            if Path(root).exists():
                log("U盘已就绪: %s（卷标 %s）" % (root, label))
                return root
    except Exception as e:
        log("U盘检测异常: %s" % e)
    log("未找到卷标 %s 的U盘" % label)
    return None


def find_usb_mount(label: str, log=print) -> str:
    """按卷标找 U 盘挂载点/盘符；找不到返回 None"""
    if not label:
        return None
    if is_windows():
        return _find_usb_windows(label, log)
    return _find_usb_linux(label, log)


def usb_writable_dir(cfg: dict, log=print):
    """确定交付目录：优先 U 盘挂载点，其次 fallback_dir。
    返回 (dir_path 或 None, 来源标记 'usb'/'fallback'/'none')"""
    usb = cfg.get("usb") or {}
    mount = find_usb_mount(usb.get("label", ""), log)
    if mount:
        return mount, "usb"
    fb = (usb.get("fallback_dir") or "").strip()
    if fb:
        Path(fb).mkdir(parents=True, exist_ok=True)
        log("U盘不可用，改用后备目录: %s" % fb)
        return fb, "fallback"
    return None, "none"


def sync_fs():
    """linux 落盘；windows 无对应操作"""
    if is_linux():
        try:
            subprocess.run(["sync"], capture_output=True, timeout=30)
        except Exception:
            pass


# ---------------------------------------------------------------- 其他

def open_path(path: str):
    """用系统文件管理器打开目录/文件（GUI「打开目录」按钮）"""
    try:
        if is_windows():
            os.startfile(str(path))  # noqa: S606 桌面应用语义
        elif sys.platform == "darwin":
            subprocess.Popen(["open", str(path)])
        else:
            subprocess.Popen(["xdg-open", str(path)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return True
    except Exception:
        return False


def home_subdir(*names: str) -> str:
    """在家目录下找第一个存在的候选子目录（中文/英文桌面目录差异）"""
    home = Path.home()
    for n in names:
        p = home / n
        if p.is_dir():
            return str(p)
    return str(home)


def suggested_outdirs(cfg: dict) -> list:
    """GUI 输出目录建议列表：U盘 > 桌面 > 文档 > 项目 output"""
    out = []
    mount = find_usb_mount((cfg.get("usb") or {}).get("label", ""), lambda m: None)
    if mount:
        out.append(mount)
    out.append(home_subdir("桌面", "Desktop"))
    out.append(home_subdir("文档", "Documents"))
    default = ((cfg.get("order_export") or {}).get("default_outdir") or "").strip()
    if getattr(sys, "frozen", False):
        base = Path(sys.executable).resolve().parent
    else:
        base = Path(__file__).resolve().parent.parent
    out.append(default or str(base / "output"))
    seen, ret = set(), []
    for d in out:
        if d and d not in seen:
            seen.add(d)
            ret.append(d)
    return ret


def platform_summary() -> dict:
    return {
        "platform": sys.platform,
        "python": sys.version.split()[0],
        "machine": os.uname().machine if hasattr(os, "uname") else os.environ.get("PROCESSOR_ARCHITECTURE", ""),
    }
