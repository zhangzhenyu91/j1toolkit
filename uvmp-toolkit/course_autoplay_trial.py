#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""培训班课程自动学习试验脚本（独立工具，不接入客户端）。

依赖：Python 3.9+、Playwright（python3 -m pip install playwright）。
使用系统 Chrome/公司浏览器，无需执行 playwright install，也不会自动登录。

首次启动受控浏览器：
  python3 course_autoplay_trial.py --launch
浏览器打开后，人工登录并进入培训班「课程设置」列表，再回到终端按 Enter。
后续在该浏览器中打开课程列表，直接运行：
  python3 course_autoplay_trial.py --scan-only   # 只扫描全部分页，不进入课程
  python3 course_autoplay_trial.py               # 处理可完成课程，末尾汇总未满 100% 课程

默认连接 http://127.0.0.1:9222。脚本只关闭自己打开的学习弹窗，
不关闭课程列表或浏览器。日志与失败截图默认写入 output/刷课试验_时间戳/；
安装目录不可写时，自动改用当前用户数据目录。浏览器探测不依赖工具箱源码模块。
"""

import argparse
import math
import os
import re
import shutil
import subprocess
import sys
import time
import traceback
from datetime import datetime
from pathlib import Path
from urllib.parse import parse_qs, urljoin, urlsplit

SCRIPT_DIR = Path(__file__).resolve().parent
TRAIN_DETAIL_PATH = "/learning-c/train/trainDetail"
COURSE_PAGE_API = "/learningc/trainclass/course/page"
WARE_LIST_API = "/learningc/course/ware-catalog-relation/listRes"
LAST_STUDY_API = "/learningc/course/study-record-info/lastStudyHour"
SAVE_STUDY_API = "/learningc/course/study-record-info/getPassWare"
_CANCEL_WAIT_SLICE_MS = 200


def _wait_for_timeout(page, timeout_ms, cancel_check=None):
    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        if cancel_check:
            cancel_check()
        remaining_ms = (deadline - time.monotonic()) * 1000.0
        if remaining_ms <= 0:
            return
        page.wait_for_timeout(min(_CANCEL_WAIT_SLICE_MS, remaining_ms))


def _wait_in_slices(wait, timeout_ms, cancel_check=None):
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError

    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        if cancel_check:
            cancel_check()
        remaining_ms = (deadline - time.monotonic()) * 1000.0
        if remaining_ms <= 0:
            raise PlaywrightTimeoutError("Timeout %sms exceeded." % timeout_ms)
        try:
            return wait(min(_CANCEL_WAIT_SLICE_MS, remaining_ms))
        except PlaywrightTimeoutError:
            if cancel_check:
                cancel_check()
            if time.monotonic() >= deadline:
                raise


def _wait_for_event(page, event_name, predicate, action, timeout_ms, cancel_check=None):
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError

    deadline = time.monotonic() + timeout_ms / 1000.0
    events = []

    def on_event(event):
        if not events and (predicate is None or predicate(event)):
            events.append(event)

    page.on(event_name, on_event)
    try:
        if cancel_check:
            cancel_check()
        action()
        while not events:
            if cancel_check:
                cancel_check()
            remaining_ms = (deadline - time.monotonic()) * 1000.0
            if remaining_ms <= 0:
                raise PlaywrightTimeoutError("Timeout %sms exceeded." % timeout_ms)
            page.wait_for_timeout(min(_CANCEL_WAIT_SLICE_MS, remaining_ms))
        return events[0]
    finally:
        page.remove_listener(event_name, on_event)


def _is_api(url, path):
    return urlsplit(url).path.endswith(path)


def is_train_detail_url(url):
    parsed = urlsplit(url)
    path = parsed.path.rstrip("/")
    fragment = parsed.fragment.split("?", 1)[0].rstrip("/")
    return path.endswith(TRAIN_DETAIL_PATH) or fragment.endswith("/train/trainDetail")


def safe_page_address(url):
    if url.startswith("about:"):
        return url.split("?", 1)[0]
    parsed = urlsplit(url)
    host = parsed.netloc.rsplit("@", 1)[-1]
    address = "%s://%s%s" % (parsed.scheme, host, parsed.path) if host else parsed.path
    if parsed.fragment:
        address += "#" + parsed.fragment.split("?", 1)[0]
    return address or "(空地址)"


def course_list_key(url):
    parsed = urlsplit(url)
    query = parse_qs(parsed.query)
    if "?" in parsed.fragment:
        query.update(parse_qs(parsed.fragment.split("?", 1)[1]))
    train_id = (query.get("trainid") or [""])[0].strip()
    if train_id:
        return "train:" + train_id
    return "page:" + safe_page_address(url)


def _api_value(payload, description):
    if not isinstance(payload, dict):
        raise RuntimeError("%s响应不是 JSON 对象" % description)
    if str(payload.get("resultCode")) != "200":
        value = payload.get("resultValue")
        message = value if isinstance(value, str) else "未知错误"
        raise RuntimeError("%s返回失败：%s" % (description, message[:200]))
    return payload.get("resultValue")


def parse_course_page(payload):
    value = _api_value(payload, "课程列表")
    if not isinstance(value, dict) or not isinstance(value.get("list"), list):
        raise RuntimeError("课程列表响应缺少 resultValue.list")
    try:
        total = int(value.get("total"))
    except (TypeError, ValueError):
        raise RuntimeError("课程列表响应缺少有效 total")
    for item in value["list"]:
        if not isinstance(item, dict) or not item.get("resourceid"):
            raise RuntimeError("课程列表存在缺少 resourceid 的记录")
    return value["list"], total


def flatten_wares(payload):
    value = _api_value(payload, "课件清单")
    if not isinstance(value, list):
        raise RuntimeError("课件清单 resultValue 不是数组")
    result = []

    def visit(nodes):
        for node in nodes:
            if not isinstance(node, dict):
                continue
            children = node.get("courseWareCatalogRelationDTOS")
            if children:
                visit(children)
            elif node.get("wareType") is not None:
                result.append(node)

    visit(value)
    if not result:
        raise RuntimeError("课件清单为空，无法学习")
    return result


def course_progress(item):
    value = item.get("studyRate")
    if value is None:
        return None
    try:
        progress = float(value)
    except (TypeError, ValueError):
        return None
    return progress if math.isfinite(progress) else None


def course_has_progress(item):
    return str(item.get("contenttype")) != "3" and item.get("hour") is not None


def course_is_complete(item):
    progress = course_progress(item)
    return course_has_progress(item) and progress is not None and progress >= 100


def course_block_reason(item):
    if str(item.get("courseStatus")) != "1":
        return "课程已下线或当前不可学习"
    if str(item.get("coursetype")) != "1":
        return "不是网页课程"
    if str(item.get("coursespace")) == "2":
        return "仅支持 APP 学习"
    return ""


def format_progress(item):
    progress = course_progress(item)
    return "未知" if progress is None else ("%g%%" % progress)


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
        raise RuntimeError("--cdp-url 仅允许本机 HTTP 地址，例如 http://127.0.0.1:9222")
    try:
        port = parsed.port
    except ValueError:
        raise RuntimeError("--cdp-url 端口无效")
    if not port:
        raise RuntimeError("--cdp-url 必须明确指定端口")
    return value, port


def runtime_root():
    source_output = SCRIPT_DIR / "output"
    writable_target = source_output if source_output.exists() else SCRIPT_DIR
    if os.access(writable_target, os.W_OK):
        return source_output
    if sys.platform.startswith("win"):
        base = Path(
            os.environ.get("LOCALAPPDATA", str(Path.home() / "AppData" / "Local"))
        )
    else:
        base = Path(os.environ.get("XDG_DATA_HOME", str(Path.home() / ".local" / "share")))
    return base / "uvmp-toolkit" / "course-autoplay"


def detect_browser(preferred=""):
    if preferred:
        executable = Path(preferred).expanduser()
        if executable.exists():
            return str(executable)
        raise RuntimeError("指定的浏览器不存在：%s" % executable)
    names = (
        "google-chrome",
        "google-chrome-stable",
        "chromium",
        "chromium-browser",
        "qaxbrowser",
        "browser360",
        "browser360-cn",
        "360chrome",
        "microsoft-edge",
        "microsoft-edge-stable",
        "chrome",
    )
    for name in names:
        executable = shutil.which(name)
        if executable:
            return executable
    candidates = [
        "/usr/bin/qaxbrowser",
        "/opt/qaxbrowser/qaxbrowser",
        "/opt/apps/qaxbrowser/qaxbrowser",
        "/usr/bin/browser360",
        "/opt/apps/com.360.browser-stable/files/browser360",
        "/opt/google/chrome/chrome",
    ]
    if sys.platform.startswith("win"):
        candidates.extend(
            [
                r"C:\Program Files\Google\Chrome\Application\chrome.exe",
                r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
                r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
                r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
            ]
        )
        local_appdata = os.environ.get("LOCALAPPDATA", "")
        if local_appdata:
            candidates.append(
                str(Path(local_appdata) / r"Google\Chrome\Application\chrome.exe")
            )
    for candidate in candidates:
        if Path(candidate).exists():
            return candidate
    raise RuntimeError("未找到 Chrome/Chromium/公司浏览器，请用 --browser-path 指定")


def launch_browser(args, port, log):
    executable = detect_browser(args.browser_path)
    profile = Path(args.profile_dir).expanduser().resolve()
    profile.mkdir(parents=True, exist_ok=True)
    command = [
        executable,
        "--remote-debugging-port=%d" % port,
        "--user-data-dir=%s" % profile,
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-dev-shm-usage",
        "about:blank",
    ]
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        command.insert(1, "--no-sandbox")
    log("启动受控浏览器：%s" % executable)
    log("使用独立浏览器配置目录：%s" % profile)
    subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


class ResponseMonitor(object):
    def __init__(self, page=None, cancel_check=None):
        self.page = page
        self.cancel_check = cancel_check
        self.responses = []

    def __call__(self, response):
        if any(
            _is_api(response.url, path)
            for path in (WARE_LIST_API, SAVE_STUDY_API, LAST_STUDY_API)
        ):
            self.responses.append(response)

    def _matches_page(self, response):
        if self.page is None:
            return True
        try:
            return response.request.frame.page == self.page
        except Exception:
            return False

    def wait_success(self, api_path, start, timeout_ms, description):
        deadline = time.monotonic() + timeout_ms / 1000.0
        while time.monotonic() < deadline:
            if self.cancel_check:
                self.cancel_check()
            for response in self.responses[start:]:
                if not self._matches_page(response) or not _is_api(response.url, api_path):
                    continue
                if response.status != 200:
                    raise RuntimeError("%s HTTP 状态为 %s" % (description, response.status))
                try:
                    payload = response.json()
                except Exception as exc:
                    raise RuntimeError("%s响应读取失败：%s" % (description, exc))
                _api_value(payload, description)
                return payload
            if self.page.is_closed():
                raise RuntimeError("等待%s时学习页已关闭" % description)
            _wait_for_timeout(self.page, 200, self.cancel_check)
        raise RuntimeError("等待%s超时（%.0f 秒）" % (description, timeout_ms / 1000.0))


class CourseRunner(object):
    def __init__(self, args, outdir, log_handler=None):
        self.args = args
        self.outdir = outdir
        self.timeout_ms = args.timeout * 1000
        self.log_handler = log_handler
        self.log_path = None if log_handler else outdir / "run.log"
        self.log_file = None if log_handler else self.log_path.open("a", encoding="utf-8")
        self.cancel_check = getattr(args, "cancel_check", None)
        self.progress_handler = getattr(args, "progress_handler", None)
        self.interrupt_exceptions = getattr(args, "interrupt_exceptions", ())
        self.diagnostic_prefix = getattr(args, "diagnostic_prefix", "failure")
        self.sync_playwright = None
        self.playwright = None
        self.browser = None
        self.context = None
        self.list_page = None
        self.active_popup = None
        self.context_response_monitor = None
        self.diagnostic_number = 0

    def log(self, message):
        if self.log_handler:
            self.log_handler(str(message))
            return
        line = "[%s] %s" % (datetime.now().strftime("%Y-%m-%d %H:%M:%S"), message)
        print(line, flush=True)
        self.log_file.write(line + "\n")
        self.log_file.flush()

    def check_cancel(self):
        if self.cancel_check:
            self.cancel_check()

    def report_progress(self, current, total, label):
        if self.progress_handler:
            self.progress_handler(current, total, label)

    def _wait_for_timeout(self, page, timeout_ms):
        return _wait_for_timeout(page, timeout_ms, self.check_cancel)

    def _wait_for_function(self, page, expression, **kwargs):
        timeout_ms = kwargs.pop("timeout", self.timeout_ms)
        return _wait_in_slices(
            lambda slice_ms: page.wait_for_function(expression, timeout=slice_ms, **kwargs),
            timeout_ms,
            self.check_cancel,
        )

    def _wait_for_load_state(self, page, state, **kwargs):
        timeout_ms = kwargs.pop("timeout", self.timeout_ms)
        return _wait_in_slices(
            lambda slice_ms: page.wait_for_load_state(state, timeout=slice_ms, **kwargs),
            timeout_ms,
            self.check_cancel,
        )

    def _wait_for_locator(self, locator, **kwargs):
        timeout_ms = kwargs.pop("timeout", self.timeout_ms)
        return _wait_in_slices(
            lambda slice_ms: locator.wait_for(timeout=slice_ms, **kwargs),
            timeout_ms,
            self.check_cancel,
        )

    def _expect_response(self, page, predicate, action):
        return _wait_for_event(
            page, "response", predicate, action, self.timeout_ms, self.check_cancel
        )

    def _expect_popup(self, page, action):
        return _wait_for_event(
            page, "popup", None, action, self.timeout_ms, self.check_cancel
        )

    def load_playwright(self):
        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            raise RuntimeError(
                "缺少 Playwright。请先为当前 Python 安装：python3 -m pip install playwright；"
                "内网离线环境请使用对应 Python 版本和 CPU 架构的 wheel 包"
            )
        self.sync_playwright = sync_playwright

    def connect(self, launched):
        self.playwright = self.sync_playwright().start()
        deadline = time.monotonic() + (self.args.timeout if launched else 0)
        attempt = 0
        while True:
            attempt += 1
            try:
                self.browser = self.playwright.chromium.connect_over_cdp(
                    self.args.cdp_url,
                    timeout=min(self.timeout_ms, 8000) if launched else self.timeout_ms,
                )
                self.log("已连接本机浏览器调试端口")
                return
            except Exception as exc:
                if not launched or time.monotonic() >= deadline:
                    raise RuntimeError(
                        "无法连接 %s。请先用 --launch 启动受控浏览器，"
                        "或确认浏览器已开启远程调试。原始错误：%s"
                        % (self.args.cdp_url, str(exc)[:300])
                    )
                if attempt == 1:
                    self.log("等待受控浏览器启动……")
                time.sleep(1)

    def find_list_page(self):
        pages = [page for context in self.browser.contexts for page in context.pages]

        def is_visible(page):
            try:
                return page.evaluate("() => document.visibilityState === 'visible'")
            except Exception:
                return False

        def choose(candidates):
            if len(candidates) == 1:
                return candidates[0]
            visible = [page for page in candidates if is_visible(page)]
            return visible[0] if len(visible) == 1 else None

        deadline = time.monotonic() + min(15.0, self.timeout_ms / 1000.0)
        candidates = []
        recognition = "地址"
        while True:
            visible_pages = [page for page in pages if is_visible(page)]
            search_pages = visible_pages or pages
            candidates = [page for page in search_pages if is_train_detail_url(page.url)]
            recognition = "地址"
            if not candidates:
                recognition = "页面内容"
                for page in search_pages:
                    try:
                        text = page.locator("body").inner_text(timeout=2000)
                    except Exception:
                        continue
                    if all(marker in text for marker in ("课程名称", "学习进度", "进入学习")):
                        candidates.append(page)
            selected = choose(candidates)
            if selected is not None or candidates or time.monotonic() >= deadline:
                break
            time.sleep(0.5)
        if selected is None:
            for page in pages:
                try:
                    title = page.title()
                except Exception:
                    title = "(无法读取标题)"
                self.log("受控标签页：%s | %s" % (title[:100], safe_page_address(page.url)))
            if candidates:
                raise RuntimeError("找到多个课程列表候选，请只将一个课程列表窗口置于前台")
            raise RuntimeError(
                "未找到课程设置列表（地址和页面内容均未匹配）。请在受控浏览器中"
                "把需要刷课的课程列表标签页置于前台，等待页面加载完成后重试"
            )
        self.list_page = selected
        self.context = selected.context
        self.list_page.set_default_timeout(self.timeout_ms)
        self.list_page.set_default_navigation_timeout(self.timeout_ms)
        self.list_page.bring_to_front()
        self.log("已通过%s识别并接管课程列表；运行期间请勿手动操作受控浏览器" % recognition)

    def _response_json(self, response, description):
        if response.status != 200:
            raise RuntimeError("%s HTTP 状态为 %s" % (description, response.status))
        try:
            return response.json()
        except Exception as exc:
            raise RuntimeError("%s响应不是有效 JSON：%s" % (description, exc))

    def _reload_list(self):
        tab = self.list_page.locator(".el-tabs__item:visible").filter(
            has_text=re.compile(r"^\s*课程设置\s*$")
        )
        try:
            if tab.count() and "is-active" not in (tab.first.get_attribute("class") or ""):
                response = self._expect_response(
                    self.list_page,
                    lambda r: _is_api(r.url, COURSE_PAGE_API),
                    tab.first.click,
                )
                return parse_course_page(self._response_json(response, "课程列表"))
            response = self._expect_response(
                self.list_page,
                lambda r: _is_api(r.url, COURSE_PAGE_API),
                lambda: self.list_page.reload(wait_until="commit"),
            )
            self._wait_for_load_state(
                self.list_page, "domcontentloaded", timeout=self.timeout_ms
            )
            return parse_course_page(self._response_json(response, "课程列表"))
        except Exception as exc:
            if self.interrupt_exceptions and isinstance(exc, self.interrupt_exceptions):
                raise
            self.save_diagnostic(self.list_page, "list")
            raise RuntimeError(
                "刷新课程列表失败。请确认已登录且位于「课程设置」页：%s" % exc
            )

    def reset_filters(self):
        self._reload_list()
        reset = self.list_page.locator("button:visible").filter(
            has_text=re.compile(r"^\s*重置\s*$")
        )
        try:
            self._wait_for_locator(reset.first, state="visible", timeout=self.timeout_ms)
        except Exception as exc:
            if self.interrupt_exceptions and isinstance(exc, self.interrupt_exceptions):
                raise
            raise RuntimeError("未找到课程设置区的「重置」按钮，无法保证扫描全部课程")
        response = self._expect_response(
            self.list_page,
            lambda r: _is_api(r.url, COURSE_PAGE_API),
            reset.first.click,
        )
        data = parse_course_page(self._response_json(response, "课程列表"))
        self.log("已清空课程筛选条件并回到第一页")
        return data

    def _wait_page_number(self, page_number):
        self._wait_for_function(
            self.list_page,
            """target => Array.from(document.querySelectorAll('.el-pagination')).some(pager => {
                if (pager.offsetParent === null) return false;
                const active = pager.querySelector('.el-pager li.active');
                return active && Number(active.textContent.trim()) === target;
            })""",
            arg=page_number,
            timeout=self.timeout_ms,
        )

    def _goto_page(self, page_number):
        jump = self.list_page.locator(
            ".el-pagination:visible .el-pagination__jump input"
        ).first
        try:
            self._wait_for_locator(jump, state="visible", timeout=self.timeout_ms)
        except Exception as exc:
            if self.interrupt_exceptions and isinstance(exc, self.interrupt_exceptions):
                raise
            raise RuntimeError("未找到分页器的页码输入框，无法跳回续刷页")

        def goto_page():
            jump.fill(str(page_number))
            jump.press("Enter")

        response = self._expect_response(
            self.list_page,
            lambda r: _is_api(r.url, COURSE_PAGE_API),
            goto_page,
        )
        data = parse_course_page(self._response_json(response, "课程列表"))
        self._wait_page_number(page_number)
        self.log("刷新后直接跳转到第 %d 页续刷" % page_number)
        return data

    def _next_page(self, page_number):
        button = self.list_page.locator(".el-pagination:visible .btn-next").first
        if not button.count() or button.is_disabled():
            raise RuntimeError("课程总数显示仍有下一页，但分页器下一页不可用")
        response = self._expect_response(
            self.list_page,
            lambda r: _is_api(r.url, COURSE_PAGE_API),
            button.click,
        )
        data = parse_course_page(self._response_json(response, "课程列表"))
        self._wait_page_number(page_number)
        return data

    def scan_courses(self, stop_first, first_page=None, skip_ids=None, start_page=1):
        if start_page < 1:
            raise RuntimeError("续刷页码必须大于 0")
        items, total = first_page if first_page is not None else self._reload_list()
        skip_ids = skip_ids or set()
        page_size = len(items)
        total_pages = (total + page_size - 1) // page_size if page_size else 0
        if start_page > max(1, total_pages):
            self.log("续刷页码超出当前总页数，改为从第 1 页扫描")
            start_page = 1
        if start_page > 1:
            new_items, new_total = self._goto_page(start_page)
            if new_total != total:
                raise RuntimeError("跳转续刷页期间课程总数发生变化，请重试")
            items = new_items
        page_number = start_page
        seen = (start_page - 1) * page_size
        studiable = 0
        completed = 0
        pending = []
        while True:
            self.check_cancel()
            self.log("扫描第 %d 页：本页 %d 门，列表总数 %d" % (page_number, len(items), total))
            for index, item in enumerate(items):
                if not course_has_progress(item):
                    continue
                studiable += 1
                if course_is_complete(item):
                    completed += 1
                    continue
                entry = {
                    "item": item,
                    "page": page_number,
                    "index": index,
                    "page_size": len(items),
                    "reason": course_block_reason(item),
                }
                pending.append(entry)
                if (
                    not entry["reason"]
                    and stop_first
                    and item["resourceid"] not in skip_ids
                ):
                    return {
                        "next": entry,
                        "pending": pending,
                        "total": total,
                        "seen": seen,
                        "studiable": studiable,
                        "completed": completed,
                    }
            seen += len(items)
            if seen >= total:
                if start_page > 1:
                    self.log("已到末页，开始从第 1 页查缺补漏")
                    start_page = 1
                    items, total = self._reload_list()
                    page_size = len(items)
                    page_number = 1
                    seen = 0
                    studiable = 0
                    completed = 0
                    pending = []
                    continue
                break
            if not items:
                raise RuntimeError("课程列表未到总数就出现空页，停止以避免漏课")
            if page_number >= 1000:
                raise RuntimeError("课程分页超过 1000 页，疑似分页异常")
            new_items, new_total = self._next_page(page_number + 1)
            if new_total != total:
                raise RuntimeError("翻页期间课程总数发生变化，请停止人工操作后重试")
            page_number += 1
            items = new_items
        return {
            "next": None,
            "pending": pending,
            "total": total,
            "seen": seen,
            "studiable": studiable,
            "completed": completed,
        }

    def report_scan(self, summary, skip_reasons=None):
        skip_reasons = skip_reasons or {}
        for entry in summary["pending"]:
            item = entry["item"]
            reason = entry["reason"]
            if item["resourceid"] in skip_reasons:
                detail = skip_reasons[item["resourceid"]]
                reason = "%s；%s" % (reason, detail) if reason else detail
            suffix = "；%s" % reason if reason else ""
            self.log(
                "未完成：第 %d 页《%s》当前 %s%s"
                % (entry["page"], item.get("coursename", "未命名课程"), format_progress(item), suffix)
            )
        self.log(
            "扫描汇总：列表 %d 门，纳入进度判断 %d 门，已 100%% %d 门，未完成 %d 门"
            % (
                summary["total"],
                summary["studiable"],
                summary["completed"],
                len(summary["pending"]),
            )
        )

    def _course_row(self, entry):
        expected = " ".join((entry["item"].get("coursename") or "").split())
        self._wait_for_function(
            self.list_page,
            """({expectedCount, expectedIndex, expectedName}) => {
                const tables = Array.from(document.querySelectorAll('.el-table')).filter(
                    table => table.offsetParent !== null
                );
                const table = tables.find(candidate => {
                    const header = candidate.querySelector('.el-table__header');
                    const text = header ? header.textContent : '';
                    return text.includes('课程名称') && text.includes('学习进度') && text.includes('进入学习');
                });
                if (!table) return false;
                const body = table.querySelector('.el-table__body-wrapper');
                if (!body) return false;
                const rows = body.querySelectorAll('tbody tr.el-table__row');
                if (rows.length !== expectedCount || !rows[expectedIndex]) return false;
                const actual = rows[expectedIndex].innerText.replace(/\\s+/g, ' ').trim();
                return !expectedName || actual.includes(expectedName);
            }""",
            arg={
                "expectedCount": entry["page_size"],
                "expectedIndex": entry["index"],
                "expectedName": expected,
            },
            timeout=self.timeout_ms,
        )
        tables = self.list_page.locator(".el-table:visible")
        table = None
        for index in range(tables.count()):
            candidate = tables.nth(index)
            header = candidate.locator(".el-table__header").first.inner_text()
            if "课程名称" in header and "学习进度" in header and "进入学习" in header:
                table = candidate
                break
        if table is None:
            raise RuntimeError("未找到包含课程名称/学习进度/进入学习的课程表格")
        rows = table.locator(".el-table__body-wrapper").first.locator(
            "tbody tr.el-table__row"
        )
        if rows.count() != entry["page_size"]:
            raise RuntimeError(
                "课程表格行数与接口不一致：DOM %d，接口 %d"
                % (rows.count(), entry["page_size"])
            )
        row = rows.nth(entry["index"])
        actual = " ".join(row.inner_text().split())
        if expected and expected not in actual:
            raise RuntimeError("课程行校验失败，已阻止误点其他课程")
        return row

    def _wait_frame_loaded(self, page, iframe, timeout_ms):
        src = iframe.get_attribute("src") or ""
        expected_url = urljoin(page.url, src) if src else ""
        deadline = time.monotonic() + timeout_ms / 1000.0
        frame = None
        while time.monotonic() < deadline:
            for candidate in page.frames:
                if candidate == page.main_frame:
                    continue
                if not expected_url or candidate.url == expected_url:
                    frame = candidate
                    break
            if frame is not None:
                break
            self._wait_for_timeout(page, 200)
        if frame is None:
            raise RuntimeError("课件预览 iframe 尚未建立")
        self._wait_for_load_state(frame, "domcontentloaded", timeout=timeout_ms)
        self._wait_for_load_state(frame, "load", timeout=timeout_ms)
        self._wait_for_function(
            frame,
            """() => {
                const body = document.body;
                if (!body) return false;
                const images = Array.from(document.images);
                if (images.some(img => img.complete && img.naturalWidth > 0)) return true;
                if (document.querySelector('canvas,embed,object')) return true;
                const text = body.innerText.trim();
                return images.length === 0 && text.length > 0 && !/^(加载中|loading)/i.test(text);
            }""",
            timeout=timeout_ms,
        )

    def _drag_video_to_end(self, page):
        self._wait_for_function(
            page,
            """() => Array.from(document.querySelectorAll('video')).some(video =>
                video.offsetParent !== null && video.readyState >= 1 &&
                Number.isFinite(video.duration) && video.duration > 0
            )""",
            timeout=self.timeout_ms,
        )
        video = page.locator("video:visible").first
        video.evaluate(
            """video => {
                video.__courseAutoplayEnded = Boolean(video.ended);
                if (!video.__courseAutoplayWatched) {
                    video.addEventListener('ended', () => {
                        video.__courseAutoplayEnded = true;
                    });
                    video.__courseAutoplayWatched = true;
                }
            }"""
        )
        slider = page.get_by_role("slider", name="进度小节", exact=True).first
        if not slider.count():
            slider = page.locator(".vjs-progress-control [role='slider']:visible").first
        self._wait_for_locator(slider, state="visible", timeout=self.timeout_ms)
        state = video.evaluate(
            "video => ({current: video.currentTime, duration: video.duration, ended: video.ended})"
        )
        duration = float(state["duration"])
        target = max(0.0, duration - min(1.0, max(0.05, duration * 0.001)))
        box = slider.bounding_box()
        if not box or box["width"] <= 2 or box["height"] <= 0:
            raise RuntimeError("视频进度条尺寸异常，无法拖动")
        start_ratio = min(0.99, max(0.0, float(state["current"]) / duration))
        start_x = box["x"] + max(1.0, min(box["width"] - 2.0, box["width"] * start_ratio))
        target_x = box["x"] + min(box["width"] - 1.0, box["width"] * target / duration)
        y = box["y"] + box["height"] / 2.0
        remaining = duration
        for attempt in range(1, 4):
            if video.evaluate("video => Boolean(video.ended || video.__courseAutoplayEnded)"):
                remaining = 0
                break
            page.mouse.move(start_x, y)
            page.mouse.down()
            page.mouse.move(target_x, y, steps=12)
            page.mouse.up()
            self._wait_for_timeout(page, 400)
            current = float(video.evaluate("video => video.currentTime"))
            remaining = duration - current
            threshold = max(2.0, min(6.0, duration * 0.002))
            if remaining <= threshold:
                self.log("视频已拖到末尾附近（剩余 %.1f 秒）" % max(0.0, remaining))
                break
            self.log("第 %d 次拖动未到达末尾，重试" % attempt)
        else:
            raise RuntimeError("连续 3 次拖动均未生效，课程可能禁止快进或播放器已改版")

        if not video.evaluate("video => Boolean(video.ended || video.__courseAutoplayEnded)"):
            try:
                video.evaluate("video => video.play()")
            except Exception:
                play = page.locator(".vjs-play-control:visible").first
                if not play.count():
                    raise RuntimeError("视频暂停且无法恢复播放")
                play.click()

    def _process_video(self, page, monitor, save_mark):
        from playwright.sync_api import TimeoutError as PlaywrightTimeoutError

        for wait_attempt in range(1, 4):
            self.check_cancel()
            self._drag_video_to_end(page)
            try:
                self._wait_for_function(
                    page,
                    """() => Array.from(document.querySelectorAll('video')).some(video =>
                        video.offsetParent !== null && Boolean(video.ended || video.__courseAutoplayEnded)
                    )""",
                    timeout=30000,
                )
                break
            except PlaywrightTimeoutError as exc:
                if wait_attempt == 3:
                    raise RuntimeError(
                        "连续 3 轮拖动后，视频均未在 30 秒内播放结束"
                    ) from exc
                self.log("等待视频结束超过 30 秒，重新拖动进度条")
        self.log("已收到视频真实 ended 事件，等待学习记录上报")
        monitor.wait_success(SAVE_STUDY_API, save_mark, self.timeout_ms, "学习记录上报")
        self._wait_for_timeout(page, 1000)

    def _process_other(self, page, monitor, save_mark, ware):
        ware_type = str(ware.get("wareType"))
        if ware_type == "1":
            self._wait_for_function(
                page,
                """() => Array.from(document.querySelectorAll('audio')).some(audio =>
                    audio.offsetParent !== null && audio.readyState >= 1
                )""",
                timeout=self.timeout_ms,
            )
        elif ware_type in ("2", "3"):
            selector = "#my_iframe" if ware_type == "3" else "iframe:visible"
            iframe = page.locator(selector).first
            self._wait_for_locator(iframe, state="visible", timeout=self.timeout_ms)
            self._wait_frame_loaded(page, iframe, self.timeout_ms)
        else:
            raise RuntimeError("暂不支持 wareType=%s 的课件加载流程" % ware_type)
        monitor.wait_success(SAVE_STUDY_API, save_mark, self.timeout_ms, "非视频学习记录上报")
        self._wait_for_timeout(page, 500)

    def _process_course(self, entry):
        item = entry["item"]
        row = self._course_row(entry)
        button = row.get_by_text("进入学习", exact=True).first
        if not button.count() or not button.is_visible():
            raise RuntimeError("课程行没有可用的「进入学习」入口")
        monitor = ResponseMonitor(cancel_check=self.check_cancel)
        self.context_response_monitor = monitor
        self.context.on("response", monitor)
        popup = self._expect_popup(self.list_page, button.click)
        self.active_popup = popup
        monitor.page = popup
        popup.set_default_timeout(self.timeout_ms)
        popup.set_default_navigation_timeout(self.timeout_ms)
        ware_payload = monitor.wait_success(
            WARE_LIST_API, 0, self.timeout_ms, "课件清单"
        )
        wares = flatten_wares(ware_payload)
        expected_id = item["resourceid"]
        for ware in wares:
            if ware.get("courseId") and ware["courseId"] != expected_id:
                raise RuntimeError("课件清单与当前课程不一致，已停止处理")
        self.log("学习页已打开，共 %d 个课件" % len(wares))
        if len(wares) > 1:
            self._wait_for_function(
                popup,
                "expected => document.querySelectorAll('.el-tree .nameClass').length === expected",
                arg=len(wares),
                timeout=self.timeout_ms,
            )
        tree_items = popup.locator(".el-tree .nameClass")
        if len(wares) > 1 and tree_items.count() != len(wares):
            raise RuntimeError(
                "课件目录数量与接口不一致：DOM %d，接口 %d"
                % (tree_items.count(), len(wares))
            )
        for index, ware in enumerate(wares):
            self.check_cancel()
            save_mark = len(monitor.responses)
            if index:
                tree_items.nth(index).click()
                if str(ware.get("wareType")) in ("1", "2"):
                    monitor.wait_success(
                        LAST_STUDY_API, save_mark, self.timeout_ms, "课件切换响应"
                    )
                self._wait_for_timeout(popup, 400)
            name = ware.get("wareName") or ware.get("resName") or "课件 %d" % (index + 1)
            self.log(
                "处理课件 %d/%d：《%s》，wareType=%s"
                % (index + 1, len(wares), name, ware.get("wareType"))
            )
            suffix = ware.get("suffixInfo")
            if not suffix:
                source_name = ware.get("resName") or ware.get("wareName") or ""
                suffix = Path(source_name).suffix
            is_mp4 = str(ware.get("wareType")) == "1" and str(
                suffix
            ).lower().lstrip(".") == "mp4"
            if is_mp4:
                self._process_video(popup, monitor, save_mark)
            else:
                self._process_other(popup, monitor, save_mark, ware)
            self.log("课件 %d/%d 页面流程完成" % (index + 1, len(wares)))

    def close_popup(self):
        monitor = self.context_response_monitor
        self.context_response_monitor = None
        if monitor is not None and self.context is not None:
            try:
                self.context.remove_listener("response", monitor)
            except Exception:
                pass
        popup = self.active_popup
        self.active_popup = None
        if popup is not None and not popup.is_closed():
            try:
                popup.close()
            except Exception as exc:
                self.log("关闭学习弹窗时出现警告：%s" % str(exc)[:200])

    def save_diagnostic(self, page, prefix):
        if page is None or page.is_closed():
            return
        self.diagnostic_number += 1
        path = self.outdir / (
            "%s_%02d_%s.png" % (self.diagnostic_prefix, self.diagnostic_number, prefix)
        )
        try:
            page.screenshot(path=str(path), full_page=False, timeout=10000)
            self.log("失败现场截图：%s" % path)
        except Exception as exc:
            self.log("失败截图保存失败：%s" % str(exc)[:200])

    def run(self, first_page, checkpoint=None):
        if checkpoint is not None and not isinstance(checkpoint, dict):
            raise RuntimeError("续刷检查点格式无效")
        checkpoint = {} if checkpoint is None else checkpoint
        checkpoint["version"] = 1
        attempts = checkpoint.setdefault("attempts", {})
        last_progress = checkpoint.setdefault("last_progress", {})
        last_errors = checkpoint.setdefault("last_errors", {})
        skipped_ids = set(checkpoint.get("skipped_ids", []))
        skip_reasons = checkpoint.setdefault("skip_reasons", {})
        processed = int(checkpoint.get("processed", 0))
        next_page_data = first_page
        resume_page = int(checkpoint.get("resume_page", 1))

        def skip_course(entry, reason):
            item = entry["item"]
            resource_id = item["resourceid"]
            skipped_ids.add(resource_id)
            skip_reasons[resource_id] = reason
            checkpoint["skipped_ids"] = sorted(skipped_ids)
            self.log(
                "跳过课程《%s》：%s；继续处理后续课程"
                % (item.get("coursename") or resource_id, reason)
            )

        self.report_progress(processed, 0, "准备扫描课程列表")
        while True:
            self.check_cancel()
            summary = self.scan_courses(
                stop_first=True,
                first_page=next_page_data,
                skip_ids=skipped_ids,
                start_page=resume_page,
            )
            next_page_data = None
            entry = summary["next"]
            if entry is None:
                if summary["pending"]:
                    self.log("========== 最终未完成课程 ==========")
                    self.report_scan(summary, skip_reasons)
                    self.log(
                        "本轮刷课结束：仍有 %d 门课程未达到 100%%，详见上方清单"
                        % len(summary["pending"])
                    )
                else:
                    self.report_scan(summary)
                    self.log("全部课程进度均为 100%，刷课完成")
                checkpoint["resume_page"] = 1
                checkpoint["processed"] = processed
                self.report_progress(summary["completed"], summary["total"], "本轮扫描完成")
                return summary
            resume_page = entry["page"]
            checkpoint["resume_page"] = resume_page
            item = entry["item"]
            resource_id = item["resourceid"]
            name = item.get("coursename") or resource_id
            progress = course_progress(item)
            attempt = attempts.get(resource_id, 0) + 1
            if attempt > self.args.max_attempts:
                reason = "连续 %d 次处理后仍为 %s" % (
                    self.args.max_attempts,
                    format_progress(item),
                )
                if resource_id in last_errors:
                    reason += "；最后错误：%s" % last_errors[resource_id]
                skip_course(entry, reason)
                continue
            attempts[resource_id] = attempt
            if resource_id in last_progress and progress is not None:
                if progress <= last_progress[resource_id]:
                    self.log("注意：《%s》上次处理后进度未提升" % name)
            if progress is not None:
                last_progress[resource_id] = progress
            self.log(
                "开始课程《%s》（第 %d 页，当前 %s，第 %d/%d 次尝试）"
                % (name, entry["page"], format_progress(item), attempt, self.args.max_attempts)
            )
            self.report_progress(processed, 0, "第 %d 页 · %s" % (entry["page"], name))
            try:
                self._process_course(entry)
            except Exception as exc:
                interrupt_error = (
                    exc
                    if self.interrupt_exceptions and isinstance(exc, self.interrupt_exceptions)
                    else None
                )
                if interrupt_error is None and self.cancel_check:
                    try:
                        self.cancel_check()
                    except self.interrupt_exceptions as cancel_exc:
                        interrupt_error = cancel_exc
                if interrupt_error is not None:
                    attempts[resource_id] = max(0, attempt - 1)
                    raise interrupt_error
                last_errors[resource_id] = str(exc)[:300]
                self.save_diagnostic(self.active_popup or self.list_page, "course")
                self.log("课程《%s》本次处理失败：%s" % (name, exc))
                self.log(traceback.format_exc())
                self.close_popup()
                if attempt >= self.args.max_attempts:
                    skip_course(
                        entry,
                        "连续 %d 次处理失败；最后错误：%s"
                        % (self.args.max_attempts, last_errors[resource_id]),
                    )
            else:
                self.close_popup()
                processed += 1
                checkpoint["processed"] = processed
                self.report_progress(processed, 0, "页面流程完成 · %s" % name)
                self._wait_for_timeout(self.list_page, 1500)

    def close(self):
        self.close_popup()
        if self.playwright is not None:
            try:
                self.playwright.stop()
            except Exception:
                pass
            self.playwright = None
        if self.log_file is not None and not self.log_file.closed:
            self.log_file.close()


def build_parser():
    parser = argparse.ArgumentParser(
        description="接管课程列表，处理未满 100% 的网页课程并汇总未完成项"
    )
    parser.add_argument("--scan-only", action="store_true", help="只扫描全部分页，不进入课程")
    parser.add_argument("--launch", action="store_true", help="启动独立受控浏览器并等待人工打开课程页")
    parser.add_argument("--cdp-url", default="http://127.0.0.1:9222", help="本机浏览器 CDP 地址")
    parser.add_argument("--browser-path", default="", help="浏览器可执行文件路径（仅 --launch）")
    parser.add_argument(
        "--profile-dir",
        default=str(runtime_root() / "course_browser_profile"),
        help="受控浏览器独立配置目录（仅 --launch）",
    )
    parser.add_argument("--out", default="", help="日志和截图输出目录")
    parser.add_argument("--timeout", type=int, default=120, help="单步等待超时秒数，默认 120")
    parser.add_argument("--max-attempts", type=int, default=3, help="单课尝试上限，达到后跳过，默认 3")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.timeout <= 0 or args.max_attempts <= 0:
        raise SystemExit("--timeout 和 --max-attempts 必须大于 0")
    try:
        _, port = validate_cdp_url(args.cdp_url)
    except RuntimeError as exc:
        raise SystemExit(str(exc))
    outdir = (
        Path(args.out).expanduser()
        if args.out
        else runtime_root() / ("刷课试验_%s" % datetime.now().strftime("%Y%m%d_%H%M%S"))
    )
    outdir.mkdir(parents=True, exist_ok=True)
    runner = CourseRunner(args, outdir)
    try:
        runner.log("刷课独立试验脚本启动；输出目录：%s" % outdir)
        runner.load_playwright()
        if args.launch:
            launch_browser(args, port, runner.log)
        runner.connect(launched=args.launch)
        if args.launch:
            try:
                input("请在浏览器中登录并打开培训班「课程设置」列表，然后按 Enter 继续……")
            except EOFError:
                raise RuntimeError("当前终端无法等待 Enter；请先人工打开课程页，再不带 --launch 运行")
        runner.find_list_page()
        first_page = runner.reset_filters()
        if args.scan_only:
            summary = runner.scan_courses(stop_first=False, first_page=first_page)
            runner.report_scan(summary)
            runner.log("只扫描模式结束，未进入任何课程")
            return 0
        runner.run(first_page)
        return 0
    except KeyboardInterrupt:
        runner.log("用户中止，已停止刷课")
        return 130
    except Exception as exc:
        runner.log("执行失败：%s" % exc)
        runner.log(traceback.format_exc())
        try:
            runner.save_diagnostic(runner.active_popup or runner.list_page, "error")
        except Exception:
            pass
        runner.log("请保留 run.log 和失败截图供内网排障")
        return 1
    finally:
        runner.close()


if __name__ == "__main__":
    sys.exit(main())
