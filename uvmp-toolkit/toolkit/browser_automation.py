# -*- coding: utf-8 -*-
"""需要人工准备会话的受控浏览器启动与状态检测（通用辅助，不处理具体业务）。"""

import importlib.util
import json
import os
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


def settings(cfg):
    item = cfg.get("browser_automation") or {}
    cdp_url, port = validate_cdp_url(
        item.get("cdp_url") or "http://127.0.0.1:9222"
    )
    profile = Path(
        item.get("profile_dir")
        or (state.state_dir() / "browser-automation" / "profile")
    ).expanduser()
    timeout = int(item.get("step_timeout", 120))
    if timeout <= 0:
        raise RuntimeError("browser_automation.step_timeout 必须大于 0")
    return {
        "cdp_url": cdp_url,
        "port": port,
        "profile_dir": profile,
        "step_timeout": timeout,
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


def status(cfg):
    opts = settings(cfg)
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


def launch(cfg):
    opts = settings(cfg)
    if status(cfg)["connected"]:
        result = status(cfg)
        result["launched"] = False
        return result
    executable = platform_utils.detect_chrome(
        (cfg.get("render") or {}).get("chrome", "")
    )
    profile = opts["profile_dir"]
    profile.mkdir(parents=True, exist_ok=True)
    command = [
        executable,
        "--remote-debugging-port=%d" % opts["port"],
        "--user-data-dir=%s" % profile,
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-dev-shm-usage",
        "--disable-extensions",
        "--disable-sync",
        "about:blank",
    ]
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        command.insert(1, "--no-sandbox")
    kwargs = {"stdout": subprocess.DEVNULL, "stderr": subprocess.DEVNULL}
    if os.name == "nt":
        kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        kwargs["start_new_session"] = True
    subprocess.Popen(command, **kwargs)
    deadline = time.monotonic() + min(60, max(10, opts["step_timeout"]))
    while time.monotonic() < deadline:
        try:
            pages = _page_list(opts["cdp_url"])
            result = status(cfg)
            result["launched"] = True
            result["browser"] = executable
            result["pages"] = len([p for p in pages if p.get("type") == "page"])
            return result
        except Exception:
            time.sleep(0.5)
    raise RuntimeError("受控浏览器启动后仍未开放 CDP，请检查浏览器路径和桌面会话")
