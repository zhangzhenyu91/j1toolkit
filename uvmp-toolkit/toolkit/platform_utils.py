# -*- coding: utf-8 -*-
"""平台相关工具：系统探测、Chrome 探测、U 盘（按卷标）发现/挂载、打开目录、常用目录建议。

U 盘逻辑移植自 export_dispatch_orders.py（卷标 GLKVM 的 KVM 虚拟 U 盘）：
- linux：lsblk 按卷标发现设备；未挂载则用 udisksctl 挂载（重试 3 次）
- windows：ctypes GetVolumeInformationW 按卷标扫盘符（尽力而为；找不到回退 fallback_dir）
"""

import os
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
CHROME_NAMES = [
    "google-chrome",
    "google-chrome-stable",
    "chromium",
    "chromium-browser",
    "qaxbrowser",  # 奇安信可信浏览器（麒麟软件商店常见）
    "browser360",
    "browser360-cn",
    "360chrome",  # 360 安全浏览器
    "microsoft-edge",
    "microsoft-edge-stable",
    "chrome",
]

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


def _desktop_user():
    """返回本机活动桌面用户名；无活动会话时回退镜像状态目录属主。"""
    uid = None
    try:
        sessions = subprocess.run(
            ["loginctl", "list-sessions", "--no-legend"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if sessions.returncode == 0:
            for line in sessions.stdout.splitlines():
                parts = line.split()
                if len(parts) < 3:
                    continue
                session_id, raw_uid = parts[0], parts[1]
                if not raw_uid.isdigit() or int(raw_uid) == 0:
                    continue
                info = subprocess.run(
                    [
                        "loginctl",
                        "show-session",
                        session_id,
                        "-p",
                        "Active",
                        "-p",
                        "State",
                        "-p",
                        "Remote",
                    ],
                    capture_output=True,
                    text=True,
                    timeout=5,
                )
                if info.returncode != 0:
                    continue
                props = dict(
                    item.split("=", 1)
                    for item in info.stdout.splitlines()
                    if "=" in item
                )
                if (
                    props.get("Active") == "yes"
                    and props.get("State") == "active"
                    and props.get("Remote") == "no"
                ):
                    uid = int(raw_uid)
                    break
    except Exception:
        pass
    if uid is None:
        mirror = os.environ.get("UVMP_TOOLKIT_MIRROR_STATE", "").strip()
        if mirror:
            try:
                uid = Path(mirror).stat().st_uid
            except OSError:
                pass
    if uid is None or uid == 0:
        return None
    try:
        import pwd

        return pwd.getpwuid(uid).pw_name
    except (ImportError, KeyError):
        return None


def _mount_linux_device(device, log):
    """root 触发时优先以桌面用户挂载，失败后回退 root 保证自动任务可用。"""
    cmd = ["udisksctl", "mount", "-b", device]
    if os.geteuid() == 0:
        user = _desktop_user()
        runuser = shutil.which("runuser")
        if user and runuser:
            log("以桌面用户 %s 挂载U盘" % user)
            r = subprocess.run(
                [runuser, "-u", user, "--"] + cmd,
                capture_output=True,
                text=True,
                timeout=30,
            )
            if r.returncode == 0:
                return r
            log(
                "桌面用户挂载失败，改用 root 挂载（桌面用户可能无权打开）: %s"
                % r.stderr.strip()
            )
        else:
            log("未识别桌面用户或 runuser，按 root 挂载（桌面用户可能无权打开）")
    return subprocess.run(cmd, capture_output=True, text=True, timeout=30)


def _find_usb_linux(label: str, log) -> str:
    """linux：确保卷标为 label 的设备已挂载，返回挂载点；失败返回 None"""
    label = (label or "").upper()

    def scan():
        try:
            r = subprocess.run(
                ["lsblk", "-J", "-o", "PATH,LABEL,MOUNTPOINT"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if r.returncode != 0:
                log("lsblk 执行失败: " + r.stderr.strip())
                return None
            import json as _json

            data = _json.loads(r.stdout)
        except Exception as e:
            log("查找U盘设备异常: %s" % e)
            return None
        for dev in data.get("blockdevices", []):
            for node in [dev] + (dev.get("children") or []):
                if (node.get("label") or "").upper() == label:
                    return node.get("path"), node.get("mountpoint")
        return None, None

    for attempt in range(1, 4):
        found = scan()
        if found is None:
            return None
        device, mountpoint = found
        if mountpoint:
            log("U盘已挂载: %s" % mountpoint)
            return mountpoint
        if not device:
            # 设备不存在时不再 3 次 × 5s 重试：设置页 getSystemInfo 会因此卡 10s，
            # 后续 tick 每分钟都会重新扫描，下一轮即可发现刚挂载的 U 盘。
            log("未找到卷标 %s 的U盘设备" % label)
            return None
        else:
            try:
                r = _mount_linux_device(device, log)
                if r.returncode != 0:
                    log("挂载失败 (%s): %s" % (device, r.stderr.strip()))
                time.sleep(1)
                found = scan()
                if found is None:
                    return None
                _device, actual = found
                if actual:
                    log("U盘挂载成功: %s" % actual)
                    return actual
                if r.returncode == 0:
                    log("挂载后未检测到挂载点")
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
            [
                "powershell",
                "-NoProfile",
                "-NonInteractive",
                "-Command",
                "(Get-Volume -FileSystemLabel '%s' -ErrorAction SilentlyContinue).DriveLetter"
                % label.replace("'", "''"),
            ],
            capture_output=True,
            text=True,
            timeout=20,
        )
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


def mountpoint_usable(path: str) -> tuple:
    """挂载点是否可读：返回 (ok, 原因)。

    断线残留挂载 stat 会 EIO/ENOTCONN，被别的用户（如 root）挂载的 vfat 会 EACCES；
    这两种都不能当作“U 盘没插”，也不该拿去跑指令。
    """
    if not path:
        return False, "挂载点为空"
    try:
        st = os.stat(path)
    except OSError as e:
        return False, "挂载点不可访问（%s，可能是残留挂载）" % (e.strerror or e)
    if not os.access(path, os.R_OK | os.X_OK):
        return False, "挂载点当前用户无读取权限（可能被 root 挂载）"
    return True, ""


def mountpoint_writable(path: str) -> tuple:
    """挂载点是否可写：返回 (ok, 原因)。

    只看 os.access 不够：U 盘异常拔插后内核会把文件系统按只读挂载，权限位仍是可写，
    但写结果文件会 EROFS（agent 会误以为执行成功/反复重试）。Linux 用 statvfs 的
    ST_RDONLY 标志识别。
    """
    if not path:
        return False, "挂载点为空"
    if not is_windows():
        try:
            st = os.statvfs(path)
            if getattr(os, "ST_RDONLY", 0) and (st.f_flag & os.ST_RDONLY):
                return False, "文件系统以只读方式挂载（常见于异常拔盘后，需重新挂载）"
        except OSError as e:
            return False, "读取挂载状态失败（%s）" % (e.strerror or e)
    if not os.access(path, os.W_OK):
        return False, "当前用户无写权限（可能被 root 挂载）"
    return True, ""


def scan_usb_mounts(label: str, log=print) -> tuple:
    """只发现、不挂载：返回 (device, mounts) 或 None。

    mounts 为 [(mountpoint, readonly, usable)]，按 lsblk 顺序，含不可读的残留挂载
    （调用方负责记录原因并跳过）。device 为设备路径（Windows 为空串），mounts 为空
    表示设备存在但未挂载。
    """
    label = (label or "").upper()
    if not label:
        return None
    if is_windows():
        root = _find_usb_windows(label, lambda _m: None)
        if not root:
            return None
        ok, _reason = mountpoint_usable(root)
        return ("", [(root, False, ok)])
    try:
        r = subprocess.run(
            ["lsblk", "-J", "-o", "PATH,LABEL,MOUNTPOINT,RO"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if r.returncode != 0:
            # 老版 util-linux 可能不认 RO 列；退回三列并假定可写。
            r = subprocess.run(
                ["lsblk", "-J", "-o", "PATH,LABEL,MOUNTPOINT"],
                capture_output=True,
                text=True,
                timeout=10,
            )
        if r.returncode != 0:
            log("lsblk 执行失败: " + r.stderr.strip())
            return None
        import json as _json

        data = _json.loads(r.stdout)
    except Exception as e:
        log("查找U盘设备异常: %s" % e)
        return None
    candidates = []
    for dev in data.get("blockdevices", []):
        for node in [dev] + (dev.get("children") or []):
            if (node.get("label") or "").upper() == label:
                candidates.append(node)

    def _readonly(node):
        value = node.get("ro")
        return value is True or str(value).lower() in ("1", "true", "yes")

    if not candidates:
        return None
    device = candidates[0].get("path") or ""
    mounts = []
    for node in candidates:
        point = node.get("mountpoint") or ""
        if point:
            ok, _reason = mountpoint_usable(point)
            mounts.append((point, _readonly(node), ok))
    return (device, mounts)


def scan_usb_device(label: str, log=print):
    """只发现、不挂载：返回 (device, mountpoint, readonly) 或 None。

    - mountpoint 为空 = 设备存在但未挂载（或已有挂载点不可读）；
    - Windows 无设备路径，返回 ("", 盘符根, False)；
    - 供页面状态与 usb-agent 的挂载状态机使用，不在此函数里重试/挂载。
    """
    found = scan_usb_mounts(label, log)
    if found is None:
        return None
    device, mounts = found
    for point, readonly, usable in mounts:
        if usable:
            return (device, point, readonly)
    return (device, "", mounts[0][1] if mounts else False)


def _mount_error_text(raw: str) -> str:
    """udisksctl 失败原因归类：把常见拒绝翻译成可操作的处置建议。"""
    text = (raw or "").strip() or "udisksctl 返回非零退出码"
    low = text.lower()
    if "not authorized" in low or "authentication" in low or "polkit" in low:
        return (
            text
            + "｜udisks 未授权：确认 /etc/polkit-1/rules.d/50-uvmp-toolkit.rules 与 "
            "/etc/polkit-1/localauthority/50-local.d/50-uvmp-toolkit.pkla 存在"
            "（重装客户端会重建）；以桌面用户执行 pkcheck --action-id "
            "org.freedesktop.udisks2.filesystem-mount --process $$ 应返回授权通过，"
            "再用 udisksctl mount -b <设备> 验证"
        )
    if "already mounted" in low or "already mounted on" in low:
        return text + "｜设备已挂载但挂载点不可读，检查 findmnt 与 mountpoint 属主"
    if "unknown filesystem" in low or "exfat" in low:
        return text + "｜系统可能缺少 exfat 支持（安装 exfat-fuse/exfatprogs 后重试）"
    if "no such device" in low or "not found" in low:
        return text + "｜设备刚被服务端切走（正常抖动，稍后重试）"
    return text


def mount_usb_device(device: str, log=print) -> tuple:
    """以当前桌面用户挂载一次（不做 root 回退、不在函数内重试）。

    重试由 usb-agent 的状态机负责（每插入周期共 3 次、间隔 5s）；单次超时 15s，失败立即返回，
    避免 udisks 卡住时把常驻 agent 一起拖死。
    返回 (ok, 原因)；原因已把 udisks 常见拒绝翻译成可操作提示。
    """
    if not device or is_windows():
        return False, "没有可挂载的设备路径"
    try:
        r = subprocess.run(
            ["udisksctl", "mount", "-b", device],
            capture_output=True,
            text=True,
            timeout=15,
        )
    except subprocess.TimeoutExpired:
        log("挂载超时: %s" % device)
        return False, "udisksctl 挂载超时（15s）"
    except Exception as e:  # noqa: BLE001
        log("挂载异常: %s" % e)
        return False, "udisksctl 调用异常：%s" % e
    if r.returncode != 0:
        reason = _mount_error_text(r.stderr or r.stdout)
        log("挂载失败 (%s): %s" % (device, reason))
        return False, reason
    log("U盘挂载成功: %s" % device)
    return True, ""


def sync_mount(path: str):
    """只同步指定挂载点所在文件系统（Linux `sync -f`），避免全局 sync 卡住其他盘。"""
    if not path or not is_linux():
        return
    try:
        if subprocess.run(["sync", "-f", path], capture_output=True, timeout=30).returncode == 0:
            return
    except Exception:  # noqa: BLE001
        pass
    try:
        subprocess.run(["sync"], capture_output=True, timeout=30)
    except Exception:  # noqa: BLE001
        pass


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
            subprocess.Popen(
                ["xdg-open", str(path)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
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
        "machine": os.uname().machine
        if hasattr(os, "uname")
        else os.environ.get("PROCESSOR_ARCHITECTURE", ""),
    }
