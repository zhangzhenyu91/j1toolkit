#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""机巡审核人工操作抓取脚本：只记录，不自动登录、不导航、不点击、不提交。

用法（先由客户端/人工打开受控浏览器并停在登录页）：
  python3 defect_review_capture.py \
    --cdp-url http://127.0.0.1:9402 \
    --outdir /data/usershare/机巡审核测试/capture-20261111 \
    --seconds 600

然后人工完成「登录 → 任务 → 筛选 → 审核」，按 Enter 结束；无终端交互时
等待 --seconds 秒自动结束。

输出（全部在 --outdir 下）：
  trace.zip       Playwright 完整 trace，可用 show-trace 打开
  actions.jsonl   人工点击/输入/按键/滚动，含选择器、坐标、值
  network.jsonl   请求/响应（含 JSON/文本响应体，敏感头打码）
  console.log     页面 console 与运行日志
  shots/          每 --shot-interval 秒截图（默认 2 秒，最多 --max-shots 张）
  summary.json    运行统计与文件路径

本脚本只连接现有 CDP，不导入 toolkit、不依赖 defect_review_trial.py。
"""

import argparse
import json
import os
import re
import select
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit, parse_qsl, urlencode, urlunsplit

SCRIPT_VERSION = "2026-11-11.03"

_ACTION_LOGGER_JS = r"""
(() => {
  if (window.__uvmpCaptureInstalled) return;
  window.__uvmpCaptureInstalled = true;
  const clean = (value) => String(value == null ? "" : value)
    .replace(/\s+/g, " ").trim().slice(0, 300);
  const selector = (el) => {
    if (!el || el.nodeType !== 1) return "";
    let out = el.tagName.toLowerCase();
    if (el.id) return out + "#" + CSS.escape(el.id);
    if (el.classList && el.classList.length) {
      out += "." + Array.from(el.classList).slice(0, 5)
        .map((x) => CSS.escape(x)).join(".");
    }
    for (const attr of ["name", "placeholder", "data-pid", "type", "role"]) {
      const value = el.getAttribute && el.getAttribute(attr);
      if (value) out += "[" + attr + "=\"" + clean(value).slice(0, 60) + "\"]";
    }
    return out;
  };
  const valueOf = (el) => {
    if (!el) return "";
    if (String(el.type || "").toLowerCase() === "password") return "***";
    if ("value" in el) return clean(el.value);
    return clean(el.innerText);
  };
  const viewport = () => ({
    width: window.innerWidth,
    height: window.innerHeight,
    dpr: window.devicePixelRatio || 1,
    scale: window.visualViewport ? window.visualViewport.scale : 1
  });
  const emit = (kind, event, extra) => {
    const el = event.target;
    const data = Object.assign({
      kind: kind,
      t: Date.now(),
      url: location.href,
      viewport: viewport(),
      selector: selector(el),
      tag: el && el.tagName ? String(el.tagName).toLowerCase() : "",
      text: clean(el && el.innerText),
      value: valueOf(el),
    }, extra || {});
    if (el && String(el.type || "").toLowerCase() === "password") {
      data.key = "***";
      data.code = "***";
    }
    try { console.log("__UVMP_CAPTURE__" + JSON.stringify(data)); } catch (_err) {}
  };
  document.addEventListener("click", (e) => emit("click", e, {
    x: e.clientX, y: e.clientY, button: e.button, detail: e.detail
  }), true);
  document.addEventListener("dblclick", (e) => emit("dblclick", e, {
    x: e.clientX, y: e.clientY
  }), true);
  document.addEventListener("input", (e) => emit("input", e), true);
  document.addEventListener("change", (e) => emit("change", e), true);
  document.addEventListener("keydown", (e) => emit("keydown", e, {
    key: e.key, code: e.code, alt: e.altKey, ctrl: e.ctrlKey, shift: e.shiftKey
  }), true);
  document.addEventListener("submit", (e) => emit("submit", e), true);
  let lastResize = 0;
  window.addEventListener("resize", (e) => {
    const now = Date.now();
    if (now - lastResize < 250) return;
    lastResize = now;
    emit("resize", e, { viewport: viewport() });
  }, true);
  let lastScroll = 0;
  document.addEventListener("scroll", (e) => {
    const now = Date.now();
    if (now - lastScroll < 250) return;
    lastScroll = now;
    emit("scroll", e, {
      scrollX: window.scrollX, scrollY: window.scrollY,
      targetTop: e.target && e.target.scrollTop,
      targetLeft: e.target && e.target.scrollLeft
    });
  }, true);
})();
"""

_SECRET_QUERY = {
    "token",
    "access_token",
    "password",
    "passwd",
    "code",
    "ticket",
    "sig",
    "signature",
    "key",
    "secret",
    "authorization",
}
_SECRET_HEADERS = {
    "authorization",
    "proxy-authorization",
    "cookie",
    "set-cookie",
    "x-token",
    "x-auth-token",
}
_SECRET_BODY_KEYS = {
    "password",
    "passwd",
    "token",
    "access_token",
    "authorization",
    "secret",
}


def _redact_url(url: str) -> str:
    try:
        parts = urlsplit(str(url or ""))
        if not parts.query:
            return str(url or "")
        query = []
        for key, value in parse_qsl(parts.query, keep_blank_values=True):
            query.append((key, "***" if key.casefold() in _SECRET_QUERY else value))
        return urlunsplit(
            (parts.scheme, parts.netloc, parts.path, urlencode(query), parts.fragment)
        )
    except Exception:
        return str(url or "")


def _redact_text(text: str, limit: int = 65536) -> str:
    value = str(text or "")[:limit]
    try:
        value = re.sub(
            r"(?i)(\"(?:%s)\"\s*:\s*\")[^\"]*"
            % "|".join(re.escape(item) for item in _SECRET_BODY_KEYS),
            r"\1***",
            value,
        )
        value = re.sub(
            r"(?i)((?:token|access_token|password|passwd|ticket|sig|signature|"
            r"secret|authorization)=)[^&\s\"']+",
            r"\1***",
            value,
        )
    except Exception:
        pass
    return value


def _redact_headers(headers: dict) -> dict:
    result = {}
    for key, value in dict(headers or {}).items():
        if str(key).casefold() in _SECRET_HEADERS:
            result[key] = "***"
        else:
            result[key] = value
    return result


def _write_jsonl(handle, data: dict) -> None:
    try:
        handle.write(json.dumps(data, ensure_ascii=False) + "\n")
        handle.flush()
    except Exception:
        pass


def _cdp_port(cdp_url: str) -> int:
    try:
        parts = urlsplit(cdp_url)
        return int(parts.port or (443 if parts.scheme == "https" else 80))
    except Exception:
        return 0


def _cdp_ready(cdp_url: str, timeout: float = 1.5) -> bool:
    try:
        with urllib.request.urlopen(
            cdp_url.rstrip("/") + "/json/version", timeout=timeout
        ) as response:
            return response.status == 200
    except Exception:
        return False


def _find_browser(explicit: str) -> str:
    if explicit:
        if Path(explicit).is_file():
            return explicit
        found = shutil.which(explicit)
        if found:
            return found
        raise RuntimeError("浏览器不存在：%s" % explicit)
    for name in (
        "browser360ent-cn-stable",
        "browser360-cn-stable",
        "google-chrome",
        "google-chrome-stable",
        "chromium",
        "chromium-browser",
    ):
        found = shutil.which(name)
        if found:
            return found
    for path in (
        "/usr/bin/browser360ent-cn-stable",
        "/usr/bin/browser360-cn-stable",
        "/usr/bin/google-chrome",
        "/usr/bin/chromium",
    ):
        if Path(path).is_file():
            return path
    raise RuntimeError(
        "未找到浏览器；请用 --browser-path 指定，例如 "
        "/usr/bin/browser360ent-cn-stable"
    )


def _launch_browser(
    cdp_url: str,
    start_url: str,
    browser_path: str,
    profile_dir: str,
) -> None:
    executable = _find_browser(browser_path)
    port = _cdp_port(cdp_url)
    if not port:
        raise RuntimeError("无法从 --cdp-url 解析端口：%s" % cdp_url)
    profile = Path(profile_dir or "~/.uvmp-toolkit/capture-profile").expanduser()
    profile.mkdir(parents=True, exist_ok=True)
    command = [
        executable,
        "--remote-debugging-port=%d" % port,
        "--user-data-dir=%s" % profile.resolve(),
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-dev-shm-usage",
    ]
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        command.insert(1, "--no-sandbox")
    if start_url:
        command.append(start_url)
    print("启动浏览器：%s" % executable, flush=True)
    print("浏览器配置：%s" % profile, flush=True)
    print("调试端口：%s" % cdp_url, flush=True)
    subprocess.Popen(
        command,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )


def _wait_cdp(cdp_url: str, timeout: float) -> None:
    deadline = time.monotonic() + max(1.0, timeout)
    while time.monotonic() < deadline:
        if _cdp_ready(cdp_url):
            return
        time.sleep(0.5)
    raise RuntimeError(
        "浏览器调试端口未就绪：%s；请检查浏览器是否允许远程调试" % cdp_url
    )


def _wait_page(browser, url_contains: str, page_index: int, timeout: float):
    deadline = time.monotonic() + max(1.0, timeout)
    last_error = None
    while time.monotonic() < deadline:
        try:
            return _pick_page(browser, url_contains, page_index)
        except Exception as exc:
            last_error = exc
        time.sleep(0.5)
    raise RuntimeError(str(last_error or "没有找到可抓取页面"))


def _pick_page(browser, url_contains: str, page_index: int):
    pages = []
    for context in list(getattr(browser, "contexts", []) or []):
        pages.extend(list(context.pages))

    def visible(page):
        try:
            return bool(page.evaluate("() => document.visibilityState === 'visible'"))
        except Exception:
            return False

    candidates = []
    for page in pages:
        try:
            url = str(page.url or "")
        except Exception:
            continue
        if url.startswith(("devtools://", "chrome://", "edge://", "data:")):
            continue
        if url in ("", "about:blank"):
            continue
        if url_contains and url_contains not in url:
            continue
        candidates.append(page)
    if not candidates:
        titles = []
        for page in pages:
            try:
                titles.append(page.url)
            except Exception:
                titles.append("?")
        raise RuntimeError(
            "没有找到可抓取的页面（--url-contains=%r）；当前页面：%s"
            % (url_contains, "；".join(titles) or "无")
        )
    visible_candidates = [page for page in candidates if visible(page)]
    ordered = visible_candidates + [page for page in candidates if page not in visible_candidates]
    index = max(0, int(page_index or 0))
    if index >= len(ordered):
        index = 0
    return ordered[index]


def _start_capture(browser, page, outdir: Path, report: dict):
    context = page.context
    trace_path = outdir / "trace.zip"
    actions_path = outdir / "actions.jsonl"
    network_path = outdir / "network.jsonl"
    console_path = outdir / "console.log"
    shots_dir = outdir / "shots"
    actions = actions_path.open("a", encoding="utf-8")
    network = network_path.open("a", encoding="utf-8")
    console = console_path.open("a", encoding="utf-8")
    attached = set()
    trace_started = False
    counters = {"actions": 0, "requests": 0, "responses": 0, "console": 0}

    def write_console(kind: str, text: str) -> None:
        try:
            console.write(
                "[%s] [%s] %s\n"
                % (time.strftime("%Y-%m-%d %H:%M:%S"), kind, _redact_text(text))
            )
            console.flush()
        except Exception:
            pass

    def install(frame) -> None:
        try:
            frame.evaluate(_ACTION_LOGGER_JS)
        except Exception:
            pass

    def install_all(target_page) -> None:
        try:
            frames = list(target_page.frames)
        except Exception:
            return
        for frame in frames:
            install(frame)

    def on_frame_navigated(frame) -> None:
        write_console("navigation", frame.url)
        install(frame)

    def on_console(target_page, message) -> None:
        text = str(message.text or "")
        if text.startswith("__UVMP_CAPTURE__"):
            try:
                actions.write(
                    _redact_text(text[len("__UVMP_CAPTURE__") :]) + "\n"
                )
                actions.flush()
                counters["actions"] += 1
            except Exception:
                pass
            return
        counters["console"] += 1
        write_console("console:%s" % message.type, text)

    def attach_page(target_page) -> None:
        key = id(target_page)
        if key in attached:
            return
        attached.add(key)
        target_page.on(
            "console", lambda message: on_console(target_page, message)
        )
        target_page.on("framenavigated", on_frame_navigated)
        target_page.on("domcontentloaded", lambda: install_all(target_page))
        target_page.on("load", lambda: install_all(target_page))
        target_page.on(
            "pageerror", lambda exc: write_console("pageerror", str(exc))
        )
        target_page.on(
            "dialog",
            lambda dialog: write_console(
                "dialog", "%s %s" % (dialog.type, dialog.message)
            ),
        )
        target_page.on("frameattached", install)
        target_page.on(
            "websocket",
            lambda ws: (
                ws.on(
                    "framesent",
                    lambda payload: _write_jsonl(
                        network,
                        {
                            "type": "ws-sent",
                            "t": int(time.time() * 1000),
                            "url": _redact_url(ws.url),
                            "payload": _redact_text(payload, 32768),
                        },
                    ),
                ),
                ws.on(
                    "framereceived",
                    lambda payload: _write_jsonl(
                        network,
                        {
                            "type": "ws-received",
                            "t": int(time.time() * 1000),
                            "url": _redact_url(ws.url),
                            "payload": _redact_text(payload, 32768),
                        },
                    ),
                ),
            ),
        )
        install_all(target_page)
        if target_page is not page:
            write_console("page", "新页面 %s" % target_page.url)

    try:
        context.tracing.start(screenshots=True, snapshots=True, sources=True)
        trace_started = True
    except Exception as exc:
        report["trace_error"] = str(exc)[:300]
        write_console("trace-error", str(exc))

    def on_request(request) -> None:
        counters["requests"] += 1
        entry = {
            "type": "request",
            "t": int(time.time() * 1000),
            "method": request.method,
            "url": _redact_url(request.url),
            "resource_type": request.resource_type,
            "headers": {},
            "post_data": "",
        }
        try:
            entry["headers"] = _redact_headers(request.all_headers())
        except Exception:
            pass
        try:
            post_data = request.post_data
            if post_data:
                entry["post_data"] = _redact_text(post_data)
        except Exception:
            pass
        _write_jsonl(network, entry)

    def on_response(response) -> None:
        counters["responses"] += 1
        entry = {
            "type": "response",
            "t": int(time.time() * 1000),
            "status": response.status,
            "url": _redact_url(response.url),
            "resource_type": response.request.resource_type,
            "headers": {},
        }
        content_type = ""
        content_length = 0
        try:
            headers = response.all_headers()
            entry["headers"] = _redact_headers(headers)
            content_type = str(headers.get("content-type") or "").lower()
            content_length = int(headers.get("content-length") or 0)
        except Exception:
            pass
        if (
            any(
                marker in content_type
                for marker in (
                    "json",
                    "text",
                    "javascript",
                    "xml",
                    "x-www-form-urlencoded",
                )
            )
            and "event-stream" not in content_type
            and content_length <= 2_000_000
        ):
            try:
                body = response.text()
                entry["body"] = _redact_text(body)
            except Exception as exc:
                entry["body_error"] = str(exc)[:200]
        _write_jsonl(network, entry)

    def on_request_failed(request) -> None:
        _write_jsonl(
            network,
            {
                "type": "requestfailed",
                "t": int(time.time() * 1000),
                "method": request.method,
                "url": _redact_url(request.url),
                "failure": str(request.failure or ""),
            },
        )

    context.on("request", on_request)
    context.on("response", on_response)
    context.on("requestfailed", on_request_failed)
    try:
        context.on("page", attach_page)
    except Exception:
        pass
    attach_page(page)
    try:
        report["viewport"] = page.evaluate(
            """() => ({
                width: window.innerWidth,
                height: window.innerHeight,
                dpr: window.devicePixelRatio || 1,
                scale: window.visualViewport ? window.visualViewport.scale : 1,
            })"""
        )
    except Exception:
        report["viewport"] = {}

    report.update(
        {
            "trace_path": str(trace_path) if trace_started else "",
            "actions_path": str(actions_path),
            "network_path": str(network_path),
            "console_path": str(console_path),
            "shots_path": str(shots_dir),
            "trace_ok": trace_started,
        }
    )
    write_console(
        "capture",
        "开始抓取 %s（trace=%s）" % (page.url, trace_started),
    )
    return {
        "context": context if trace_started else None,
        "trace_path": trace_path,
        "actions": actions,
        "network": network,
        "console": console,
        "counters": counters,
    }


def _save_screenshot(page, shots_dir: Path, index: int) -> bool:
    try:
        shots_dir.mkdir(parents=True, exist_ok=True)
        page.screenshot(
            path=str(shots_dir / ("%05d.png" % index)),
            timeout=3000,
        )
        return True
    except Exception:
        return False


def _wait_until_stop(
    page, shots_dir: Path, seconds: float, shot_interval: float, max_shots: int
) -> int:
    print(
        "抓取中：请人工完成 登录 → 任务 → 筛选 → 审核；"
        "按 Enter 结束，或 %.0f 秒后自动结束" % max(1.0, seconds),
        flush=True,
    )
    deadline = time.monotonic() + max(1.0, float(seconds or 600))
    next_shot = 0.0
    shots = 0
    while time.monotonic() < deadline:
        now = time.monotonic()
        if now >= next_shot and shots < max(1, int(max_shots)):
            if _save_screenshot(page, shots_dir, shots):
                shots += 1
            next_shot = now + max(0.5, float(shot_interval or 2.0))
        try:
            if sys.stdin and sys.stdin.isatty():
                ready, _, _ = select.select([sys.stdin], [], [], 0.5)
                if ready:
                    sys.stdin.readline()
                    return shots
                continue
        except Exception:
            pass
        time.sleep(0.5)
    return shots


def _stop_capture(session, report: dict, shots: int) -> None:
    if not session:
        return
    context = session.get("context")
    trace_path = session.get("trace_path")
    if context is not None and trace_path is not None:
        try:
            context.tracing.stop(path=str(trace_path))
            report["trace_ok"] = True
        except Exception as exc:
            report["trace_error"] = str(exc)[:300]
            report["trace_ok"] = False
    for key in ("actions", "network", "console"):
        handle = session.get(key)
        try:
            if handle is not None:
                handle.close()
        except Exception:
            pass
    report["shots_count"] = shots
    counters = session.get("counters") or {}
    report.update(counters)


def _parse_args(argv=None):
    parser = argparse.ArgumentParser(
        description="机巡审核人工操作全量抓取（只记录，不自动操作）"
    )
    parser.add_argument(
        "--version",
        action="version",
        version="%(prog)s " + SCRIPT_VERSION,
    )
    parser.add_argument(
        "--cdp-url",
        default="http://127.0.0.1:9402",
        help="受控浏览器 CDP 地址（默认 http://127.0.0.1:9402）",
    )
    parser.add_argument(
        "--launch",
        action="store_true",
        help="CDP 端口未启动时，只打开浏览器到 --start-url，不做登录/点击",
    )
    parser.add_argument(
        "--start-url",
        default="",
        help="--launch 时浏览器起始地址，例如 http://25.39.77.173:31005",
    )
    parser.add_argument(
        "--browser-path",
        default="",
        help="浏览器可执行文件；留空自动查找 browser360/chrome/chromium",
    )
    parser.add_argument(
        "--profile-dir",
        default="",
        help="--launch 时浏览器配置目录；留空 ~/.uvmp-toolkit/capture-profile",
    )
    parser.add_argument(
        "--outdir",
        default="",
        help="输出目录；留空使用当前目录 capture-时间戳",
    )
    parser.add_argument(
        "--url-contains",
        default="",
        help="按 URL 片段选择页面；留空选第一个可见业务页面",
    )
    parser.add_argument(
        "--page-index",
        type=int,
        default=0,
        help="多个候选页面时的序号（默认 0）",
    )
    parser.add_argument(
        "--seconds",
        type=float,
        default=600.0,
        help="最长抓取秒数（默认 600）",
    )
    parser.add_argument(
        "--shot-interval",
        type=float,
        default=2.0,
        help="截图间隔秒数（默认 2）",
    )
    parser.add_argument(
        "--max-shots",
        type=int,
        default=500,
        help="最多截图张数（默认 500）",
    )
    parser.add_argument(
        "--no-trace",
        action="store_true",
        help="不启用 Playwright trace；仍记录 actions/network/console/shots",
    )
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = _parse_args(argv)
    print("抓取脚本版本 %s" % SCRIPT_VERSION, flush=True)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("缺少 Playwright；请用客户端自带 uvmp-core 运行", file=sys.stderr)
        return 2
    outdir = (
        Path(args.outdir).expanduser()
        if args.outdir
        else Path.cwd() / ("capture-" + time.strftime("%Y%m%d_%H%M%S"))
    )
    outdir.mkdir(parents=True, exist_ok=True)
    report = {
        "script_version": SCRIPT_VERSION,
        "cdp_url": args.cdp_url,
        "launch": bool(args.launch),
        "start_url": args.start_url,
        "browser_path": args.browser_path,
        "profile_dir": args.profile_dir,
        "outdir": str(outdir),
        "url_contains": args.url_contains,
        "page_index": args.page_index,
        "seconds": args.seconds,
        "actions": 0,
        "requests": 0,
        "responses": 0,
        "console": 0,
        "shots_count": 0,
        "trace_ok": False,
        "trace_error": "",
        "error": "",
    }
    pw = None
    browser = None
    session = None
    shots = 0
    try:
        if not _cdp_ready(args.cdp_url):
            if not args.launch:
                raise RuntimeError(
                    "受控浏览器未运行（%s）。加 --launch 可只打开浏览器；"
                    "或先在客户端启动实例浏览器后再运行本脚本" % args.cdp_url
                )
            _launch_browser(
                args.cdp_url,
                args.start_url,
                args.browser_path,
                args.profile_dir,
            )
            _wait_cdp(args.cdp_url, 30.0)
        pw = sync_playwright().start()
        browser = pw.chromium.connect_over_cdp(args.cdp_url)
        page = _wait_page(browser, args.url_contains, args.page_index, 30.0)
        print("已连接：%s" % args.cdp_url)
        print("抓取页面：%s" % page.url)
        print("输出目录：%s" % outdir)
        if not args.no_trace:
            session = _start_capture(browser, page, outdir, report)
        else:
            # no-trace 也保留 actions/network/console；复用捕获逻辑后立即停 trace。
            session = _start_capture(browser, page, outdir, report)
            context = session.get("context")
            if context is not None:
                try:
                    context.tracing.stop()
                except Exception:
                    pass
                session["context"] = None
                report["trace_ok"] = False
                report["trace_path"] = ""
        shots = _wait_until_stop(
            page,
            outdir / "shots",
            args.seconds,
            args.shot_interval,
            args.max_shots,
        )
        return 0
    except KeyboardInterrupt:
        print("收到中断，正在保存抓取结果……", flush=True)
        return 130
    except Exception as exc:
        report["error"] = str(exc)[:1000]
        print("抓取失败：%s" % exc, file=sys.stderr)
        return 1
    finally:
        _stop_capture(session, report, shots)
        report["finished_at"] = time.strftime("%Y-%m-%d %H:%M:%S")
        try:
            (outdir / "summary.json").write_text(
                json.dumps(report, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
        except Exception:
            pass
        # 只断开 CDP，不关闭用户正在操作的浏览器。
        try:
            if pw is not None:
                pw.stop()
        except Exception:
            pass
        print(
            "抓取结束：%s（actions=%s, requests=%s, responses=%s, shots=%s）"
            % (
                outdir,
                report.get("actions", 0),
                report.get("requests", 0),
                report.get("responses", 0),
                report.get("shots_count", 0),
            ),
            flush=True,
        )


if __name__ == "__main__":
    sys.exit(main())
