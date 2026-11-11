# -*- coding: utf-8 -*-
"""需要人工准备会话的受控浏览器启动与状态检测（通用辅助，不处理具体业务）。

几处刻意的约束：
- 已连上 CDP 时只把启动网址放到“空白页/已同源的页签”，绝不 goto 用户正在看的页签
- 默认 profile 按 CDP 端口分目录（一应用/一实例一套 Cookie，避免多开互相踢登录）
- 自己拉起的浏览器在启动超时/启动网址失败时回收整棵进程树，不留孤儿 renderer
"""

import importlib.util
import json
import os
import signal
import subprocess
import time
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

import platform_utils
import state


def validate_cdp_url(value):
    parsed = urlsplit(value)
    if (
        parsed.scheme != "http"
        or parsed.hostname not in ("127.0.0.1", "localhost", "::1")
        or parsed.username
        or parsed.password
        or parsed.path not in ("", "/")
        or parsed.query
        or parsed.fragment
    ):
        raise RuntimeError("CDP 地址仅允许本机 HTTP 地址，例如 http://127.0.0.1:9222")
    try:
        port = parsed.port
    except ValueError:
        raise RuntimeError("CDP 地址端口无效")
    if not port:
        raise RuntimeError("CDP 地址必须明确指定端口")
    return value.rstrip("/"), port


def default_profile_dir():
    """默认浏览器用户目录（多应用共用时的基准目录，实际使用按端口再分目录）"""
    return state.state_dir() / "browser-automation" / "profile"


def _profile_for(opts):
    """解析实际用户目录：配置/调用方显式给的目录原样使用（业务的多实例目录也在此）；
    只有落到内置默认目录时才按 CDP 端口分目录，避免不同应用共享一套登录态。"""
    profile = Path(opts.get("profile_dir") or default_profile_dir()).expanduser()
    try:
        is_default = profile.resolve() == default_profile_dir().resolve()
    except OSError:
        is_default = str(profile) == str(default_profile_dir())
    if is_default:
        return profile.parent / ("port-%d" % int(opts["port"]))
    return profile


def settings(cfg):
    item = cfg.get("browser_automation") or {}
    cdp_url, port = validate_cdp_url(
        item.get("cdp_url") or "http://127.0.0.1:9222"
    )
    profile = Path(item.get("profile_dir") or default_profile_dir()).expanduser()
    timeout = int(item.get("step_timeout", 120))
    if timeout <= 0:
        raise RuntimeError("browser_automation.step_timeout 必须大于 0")
    start_url = (item.get("start_url") or "").strip()
    if start_url:
        parsed = urlsplit(start_url)
        if parsed.scheme not in ("http", "https") or not parsed.netloc:
            raise RuntimeError("启动网址必须是完整的 http/https 地址")
    return {
        "cdp_url": cdp_url,
        "port": port,
        "profile_dir": profile,
        "step_timeout": timeout,
        "start_url": start_url,
    }


def playwright_available():
    try:
        return importlib.util.find_spec("playwright") is not None
    except (ImportError, AttributeError, ValueError):
        return False


def _page_list(cdp_url, timeout=1):
    with urllib.request.urlopen(cdp_url + "/json/list", timeout=timeout) as response:
        data = json.loads(response.read().decode("utf-8"))
    if not isinstance(data, list):
        raise RuntimeError("浏览器调试接口返回格式异常")
    return data


def _same_target(page_url: str, start_url: str) -> bool:
    """页签是否已经在启动网址（同协议+主机+端口+路径即可，忽略查询串）"""
    try:
        a = urlsplit(page_url or "")
        b = urlsplit(start_url or "")
    except ValueError:
        return False
    if not a.scheme or a.scheme not in ("http", "https"):
        return False
    return (a.scheme, a.hostname, a.port, a.path.rstrip("/")) == (
        b.scheme,
        b.hostname,
        b.port,
        b.path.rstrip("/"),
    )


def _open_start_url(opts):
    """把启动网址放到受控浏览器：

    已打开同源页签 → 只前置，不重新导航（用户可能正在页面里填数据）；
    有空白页 → 复用空白页；
    否则 → 新建页签。
    绝不直接对 contexts[0].pages[0] 做 goto，那会覆盖用户当前页签。
    """
    if not opts["start_url"]:
        return
    from playwright.sync_api import sync_playwright

    with sync_playwright() as playwright:
        browser = playwright.chromium.connect_over_cdp(
            opts["cdp_url"], timeout=opts["step_timeout"] * 1000
        )
        contexts = list(browser.contexts or [])
        if not contexts:
            raise RuntimeError("受控浏览器没有可用于打开启动网址的上下文")
        context = contexts[0]
        pages = list(context.pages or [])
        for page in pages:
            try:
                url = page.url
            except Exception:  # noqa: BLE001
                continue
            if _same_target(url, opts["start_url"]):
                try:
                    page.bring_to_front()
                except Exception:  # noqa: BLE001
                    pass
                return
        target = None
        for page in pages:
            try:
                if (page.url or "").strip() in ("", "about:blank"):
                    target = page
                    break
            except Exception:  # noqa: BLE001
                continue
        if target is None:
            target = context.new_page()
        target.goto(
            opts["start_url"],
            wait_until="domcontentloaded",
            timeout=opts["step_timeout"] * 1000,
        )


def _terminate_tree(proc):
    """回收浏览器整棵进程树：Chrome 会 fork 出 zygote/renderer，只 terminate 主进程
    会留下孤儿进程继续占用 profile 与调试端口，下次启动直接失败。"""
    if proc is None:
        return
    try:
        if proc.poll() is not None:
            return
    except Exception:  # noqa: BLE001
        pass
    if os.name == "nt":
        # Windows 没有进程组信号：taskkill /T 按父子关系杀整棵树
        try:
            subprocess.run(
                ["taskkill", "/F", "/T", "/PID", str(proc.pid)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=10,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
        except Exception:  # noqa: BLE001
            pass
    else:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        except Exception:  # noqa: BLE001
            pass
    try:
        proc.wait(timeout=5)
        return
    except Exception:  # noqa: BLE001
        pass
    if os.name == "nt":
        try:
            proc.kill()
        except Exception:  # noqa: BLE001
            pass
        return
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
    except Exception:  # noqa: BLE001
        try:
            proc.kill()
        except Exception:  # noqa: BLE001
            pass


def status(cfg, cdp_url=None, profile_dir=None, start_url=None):
    opts = settings(cfg)
    if cdp_url:
        opts["cdp_url"], opts["port"] = validate_cdp_url(str(cdp_url))
    if profile_dir:
        opts["profile_dir"] = Path(str(profile_dir)).expanduser()
    if start_url is not None:
        text = str(start_url or "").strip()
        if text:
            parsed = urlsplit(text)
            if parsed.scheme not in ("http", "https") or not parsed.netloc:
                raise RuntimeError("启动网址必须是完整的 http/https 地址")
        opts["start_url"] = text
    opts["profile_dir"] = _profile_for(opts)
    try:
        browser = platform_utils.detect_chrome(
            (cfg.get("render") or {}).get("chrome", "")
        )
    except Exception:
        browser = ""
    result = {
        "cdp_url": opts["cdp_url"],
        "browser": browser,
        "profile_dir": str(opts["profile_dir"]),
        "start_url": opts["start_url"],
        "connected": False,
        "pages": 0,
        "playwright": playwright_available(),
    }
    try:
        pages = _page_list(opts["cdp_url"])
    except Exception:
        return result
    result["connected"] = True
    result["pages"] = len([p for p in pages if p.get("type") == "page"])
    return result


def _browser_command(executable, opts):
    command = [
        executable,
        "--remote-debugging-port=%d" % opts["port"],
        "--user-data-dir=%s" % opts["profile_dir"],
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-dev-shm-usage",
        "--disable-extensions",
        "--disable-sync",
        "about:blank",
    ]
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        command.insert(1, "--no-sandbox")
    return command


def launch(cfg, start_url=None, cdp_url=None, profile_dir=None):
    opts = settings(cfg)
    if cdp_url:
        opts["cdp_url"], opts["port"] = validate_cdp_url(str(cdp_url))
    if profile_dir:
        opts["profile_dir"] = Path(str(profile_dir)).expanduser()
    if start_url is not None:
        text = str(start_url or "").strip()
        if text:
            parsed = urlsplit(text)
            if parsed.scheme not in ("http", "https") or not parsed.netloc:
                raise RuntimeError("启动网址必须是完整的 http/https 地址")
        opts["start_url"] = text
    opts["profile_dir"] = _profile_for(opts)
    if status(
        cfg,
        cdp_url=opts["cdp_url"],
        profile_dir=opts["profile_dir"],
        start_url=opts["start_url"],
    )["connected"]:
        _open_start_url(opts)
        result = status(
            cfg,
            cdp_url=opts["cdp_url"],
            profile_dir=opts["profile_dir"],
            start_url=opts["start_url"],
        )
        result["launched"] = False
        return result
    executable = platform_utils.detect_chrome(
        (cfg.get("render") or {}).get("chrome", "")
    )
    profile = opts["profile_dir"]
    profile.mkdir(parents=True, exist_ok=True)
    command = _browser_command(executable, opts)
    kwargs = {"stdout": subprocess.DEVNULL, "stderr": subprocess.DEVNULL}
    if os.name == "nt":
        kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        # 独立会话 = 独立进程组，回收时 killpg 一次带走整棵进程树
        kwargs["start_new_session"] = True
    proc = subprocess.Popen(command, **kwargs)
    deadline = time.monotonic() + min(60, max(10, opts["step_timeout"]))
    pages = None
    while time.monotonic() < deadline:
        try:
            pages = _page_list(opts["cdp_url"])
            break
        except Exception:
            time.sleep(0.5)
    if pages is None:
        # 自己拉起的浏览器没起来（路径不对/桌面会话缺失）：整棵树回收后再报错
        _terminate_tree(proc)
        raise RuntimeError("受控浏览器启动后仍未开放 CDP，请检查浏览器路径和桌面会话")
    try:
        _open_start_url(opts)
    except Exception:
        _terminate_tree(proc)
        raise
    result = status(
        cfg,
        cdp_url=opts["cdp_url"],
        profile_dir=opts["profile_dir"],
        start_url=opts["start_url"],
    )
    result["launched"] = True
    result["browser"] = executable
    result["pages"] = len([p for p in pages if p.get("type") == "page"])
    return result
