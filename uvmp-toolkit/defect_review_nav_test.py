#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""机巡审核「登录 → 等 /home 加载 → 直达任务页/回退菜单 → 填筛选 → 查询 →
点开第一个可审核任务」冒烟脚本。

默认只点开第一个可审核任务的弹层，随后立即关闭；不审核图片、不提交任何审核结果。
适用于现场排查：
- 登录后是否能经菜单进入「智能分析 → 智能识别 → 缺陷识别任务」；
- 筛选条件是否真实填入并点击「查询」拿到列表响应；
- 第一个可审核任务能否正常进入并加载弹层（--no-enter-task 可跳过）；
- 可选 --simulate-404：先打开任务页深链接（不支持深链接的站点会 404），
  再验证脚本能点「返回首页」或回到启动网址、重新走菜单并继续筛选。

默认直接复用客户端已保存的第一个实例参数（启动网址、账号密码、CDP、profile、
筛选条件、超时）；命令行参数只在需要覆盖时传：
  python defect_review_nav_test.py --simulate-404
  python defect_review_nav_test.py --simulate-nav-timeout

也可以指定实例或单独覆盖：
  python defect_review_nav_test.py --instance 胜西线1 --simulate-404
  python defect_review_nav_test.py --start-date 2026-11-01 --end-date 2026-11-02

在客户端「开发者工具 → 脚本测试」中运行时，客户端会注入
UVMP_TOOLKIT_CONFIG，脚本可直接读到当前 config.json；源码环境则读取
uvmp-toolkit/toolkit/config.json。脚本默认优先加载同目录的
defect_review_trial.py，否则加载客户端 _MEIPASS 内的引擎；要验证新引擎，
请把本脚本和修复后的 defect_review_trial.py 放同一目录，或用 --engine-path。
脚本只连接本机 CDP，不导出 Cookie/token，不访问外网。
"""

import argparse
import importlib.util
import json
import os
import re
import sys
import time
import traceback
from pathlib import Path
from types import SimpleNamespace

SCRIPT_VERSION = "2026-11-11.01"


def _load_engine(path_hint: str = ""):
    candidates = []
    if path_hint:
        hinted = Path(path_hint).expanduser()
        candidates.append(
            hinted / "defect_review_trial.py" if hinted.is_dir() else hinted
        )
    candidates.append(Path(__file__).resolve().with_name("defect_review_trial.py"))
    meipass = getattr(sys, "_MEIPASS", "")
    if meipass:
        candidates.append(Path(meipass) / "defect_review_trial.py")
    for candidate in candidates:
        if not candidate.is_file():
            continue
        spec = importlib.util.spec_from_file_location(
            "_defect_review_nav_engine", candidate
        )
        if spec is None or spec.loader is None:
            continue
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module
    raise RuntimeError(
        "未找到 defect_review_trial.py；请用 --engine-path 指定引擎路径"
    )


def _read_client_config(engine):
    """读取客户端 config.json；返回 (配置, 来源说明, 配置路径)。"""
    try:
        cfg = engine._client_config() or {}
        if isinstance(cfg, dict) and cfg:
            return cfg, "客户端脚本环境", os.environ.get("UVMP_TOOLKIT_CONFIG", "")
    except Exception:
        pass
    try:
        import toolkit.config as toolkit_config

        data = toolkit_config.load()
        if isinstance(data, dict) and data:
            return data, "toolkit.config", str(toolkit_config.config_path())
    except Exception:
        pass
    candidates = []
    env = os.environ.get("UVMP_TOOLKIT_CONFIG", "").strip()
    if env:
        candidates.append(Path(env))
    candidates.append(Path.home() / ".config" / "uvmp-toolkit" / "config.json")
    appdata = os.environ.get("APPDATA", "").strip()
    if appdata:
        candidates.append(Path(appdata) / "uvmp-toolkit" / "config.json")
    candidates.append(Path(__file__).resolve().parent / "config.json")
    candidates.append(
        Path(__file__).resolve().parents[1] / "toolkit" / "config.json"
    )
    for path in candidates:
        try:
            if not path.is_file():
                continue
            data = json.loads(path.read_text(encoding="utf-8"))
            if isinstance(data, dict) and data:
                return data, "配置文件", str(path)
        except Exception:
            continue
    return {}, "", ""


def _select_instance(instances, selector):
    if not instances:
        return None, 0
    text = str(selector or "").strip()
    if not text:
        return instances[0], 0
    for index, item in enumerate(instances):
        if not isinstance(item, dict):
            continue
        if text in (str(item.get("id") or ""), str(item.get("name") or "")):
            return item, index
    if text.isdigit():
        index = int(text) - 1
        if 0 <= index < len(instances):
            return instances[index], index
    names = [
        "%s(%s)" % (item.get("name") or "", item.get("id") or "")
        for item in instances
        if isinstance(item, dict)
    ]
    raise RuntimeError("找不到实例 %r；可用实例：%s" % (selector, "、".join(names)))


def _resolve_values(args, cfg):
    app = cfg.get("defect_review") if isinstance(cfg, dict) else {}
    if not isinstance(app, dict):
        app = {}
    instances = app.get("instances") if isinstance(app.get("instances"), list) else []
    instance, index = _select_instance(instances, args.instance)
    params = instance.get("params") if isinstance(instance, dict) else {}
    if not isinstance(params, dict):
        params = {}
    browser_cfg = cfg.get("browser_automation") if isinstance(cfg, dict) else {}
    if not isinstance(browser_cfg, dict):
        browser_cfg = {}
    render_cfg = cfg.get("render") if isinstance(cfg, dict) else {}
    if not isinstance(render_cfg, dict):
        render_cfg = {}

    def param(key, default=None):
        # 选中实例时只读该实例的 params，不回退到顶层（顶层是最后保存的实例，
        # 可能是另一个账号，混用会把测试登到别人账号上）。
        if isinstance(instance, dict):
            if key in params:
                value = params.get(key)
                return "" if value is None else value
            return default
        if key in app:
            value = app.get(key)
            return "" if value is None else value
        return default

    def instance_value(key, default=""):
        if isinstance(instance, dict):
            value = instance.get(key)
            if value is not None and str(value).strip():
                return str(value)
        return default

    start_url = (
        args.start_url
        or instance_value("start_url")
        or str(param("start_url", "") or "")
    )
    username = (
        args.username
        if args.username is not None
        else os.environ.get("UVMP_DEFECT_USERNAME", "")
        or str(param("username", "") or "")
    )
    password = (
        args.password
        if args.password is not None
        else os.environ.get("UVMP_DEFECT_PASSWORD", "")
        or str(param("password", "") or "")
    )
    cdp_url = (
        args.cdp_url
        or instance_value("cdp_url")
        or str(param("cdp_url", "") or "")
        or str(browser_cfg.get("cdp_url") or "")
        or "http://127.0.0.1:9222"
    )
    profile_dir = (
        args.profile_dir
        or instance_value("profile_dir")
        or str(param("profile_dir", "") or "")
        or str(browser_cfg.get("profile_dir") or "")
    )
    browser_path = args.browser_path or str(render_cfg.get("chrome") or "")
    task_status = (
        args.task_status
        if args.task_status is not None
        else str(param("task_status", "待人工审核") or "待人工审核")
    )
    mission_type = (
        args.mission_type
        if args.mission_type is not None
        else str(param("mission_type", "") or "")
    )
    voltage_level = (
        args.voltage_level
        if args.voltage_level is not None
        else str(param("voltage_level", "") or "")
    )
    line_name = (
        args.line_name
        if args.line_name is not None
        else str(param("line_name", "") or "")
    )
    start_date = args.start_date or str(param("start_date", "") or "")
    end_date = args.end_date or str(param("end_date", "") or "")
    timeout = args.timeout
    if timeout is None:
        try:
            timeout = float(param("step_timeout", 120) or 120)
        except (TypeError, ValueError):
            timeout = 120.0
    missing = []
    if not start_url:
        missing.append("启动网址")
    if not username:
        missing.append("账号")
    if not password:
        missing.append("密码")
    if not start_date:
        missing.append("开始日期")
    if not end_date:
        missing.append("结束日期")
    if getattr(args, "manual", False):
        # 手动模式只要求能打开站点；账号密码和日期由人现场输入。
        missing = [item for item in missing if item == "启动网址"]
    if missing:
        raise RuntimeError(
            "实例参数缺少%s；请先在客户端配置实例，或用命令行参数补全"
            % "、".join(missing)
        )
    return {
        "instance": instance,
        "instance_index": index,
        "instance_name": str((instance or {}).get("name") or "实例 1"),
        "instance_id": str((instance or {}).get("id") or "default"),
        "start_url": start_url,
        "username": username,
        "password": password,
        "cdp_url": cdp_url,
        "profile_dir": profile_dir,
        "browser_path": browser_path,
        "task_status": task_status,
        "mission_type": mission_type,
        "voltage_level": voltage_level,
        "line_name": line_name,
        "start_date": start_date,
        "end_date": end_date,
        "timeout": timeout,
    }


def _parse_args(argv=None):
    parser = argparse.ArgumentParser(
        description="机巡审核登录/菜单/筛选/查询冒烟（默认复用第一个实例参数，不执行审核）"
    )
    parser.add_argument(
        "--version",
        action="version",
        version="%(prog)s " + SCRIPT_VERSION,
    )
    parser.add_argument(
        "--engine-path", default="", help="defect_review_trial.py 或其所在目录"
    )
    parser.add_argument(
        "--instance", default="", help="实例 id、名称或序号（默认第一个实例）"
    )
    parser.add_argument("--start-url", default="", help="覆盖实例启动网址")
    parser.add_argument(
        "--username", default=None, help="覆盖实例账号（默认读实例参数/环境变量）"
    )
    parser.add_argument(
        "--password", default=None, help="覆盖实例密码（默认读实例参数/环境变量）"
    )
    parser.add_argument("--cdp-url", default="", help="覆盖实例 CDP 地址")
    parser.add_argument("--profile-dir", default="", help="覆盖实例浏览器 profile")
    parser.add_argument("--browser-path", default="", help="覆盖浏览器可执行文件")
    parser.add_argument("--task-status", default=None, help="覆盖任务状态")
    parser.add_argument("--mission-type", default=None, help="覆盖作业性质")
    parser.add_argument("--voltage-level", default=None, help="覆盖电压等级")
    parser.add_argument("--line-name", default=None, help="覆盖线路名称")
    parser.add_argument("--start-date", default="", help="覆盖开始日期 YYYY-MM-DD")
    parser.add_argument("--end-date", default="", help="覆盖结束日期 YYYY-MM-DD")
    parser.add_argument(
        "--outdir",
        default="",
        help="输出目录；留空使用用户主目录下的 nav-test-时间戳",
    )
    parser.add_argument(
        "--simulate-404",
        action="store_true",
        help="先打开任务页深链接（站点不支持时会 404），再验证自动返回首页/回启动网址并走菜单",
    )
    parser.add_argument(
        "--simulate-nav-timeout",
        action="store_true",
        help="任务列表已就绪后调用直达逻辑，验证任务页已加载时不再等待导航超时",
    )
    parser.add_argument(
        "--no-launch",
        action="store_true",
        help="受控浏览器未运行时直接报错，不自动启动",
    )
    parser.add_argument(
        "--trace",
        action="store_true",
        help="开启 Playwright trace + 网络/控制台/页面操作日志",
    )
    parser.add_argument(
        "--manual",
        action="store_true",
        help="手动模式：打开启动网址后由人操作，抓取登录到审核的全过程",
    )
    parser.add_argument(
        "--seconds",
        type=float,
        default=600.0,
        help="手动模式最长抓取秒数（默认 600）",
    )
    parser.add_argument(
        "--no-enter-task",
        action="store_true",
        help="查询后不进入任务；默认点开第一个可审核任务并立即关闭（不审核、不提交）",
    )
    parser.add_argument(
        "--timeout", type=float, default=None, help="覆盖单步等待秒数"
    )
    return parser.parse_args(argv)


def _runner_args(values, profile_dir: str):
    return SimpleNamespace(
        timeout=values["timeout"],
        action_timeout=5,
        navigation_timeout=15,
        login_result_timeout=20,
        captcha_ocr_timeout=30,
        image_timeout=10,
        image_retries=0,
        max_unreviewed=400,
        unknown_timeout=120,
        start_url=values["start_url"],
        cdp_url=values["cdp_url"],
        profile_dir=profile_dir,
        browser_path=values["browser_path"],
        launch=True,
        auto_login=True,
        username=values["username"],
        password=values["password"],
        reload_after_tasks=10,
        reload_after_images=200,
        stall_timeout=120,
        slow_image_seconds=15,
        slow_image_count=3,
        slow_restart_unreviewed=50,
        window_bounds=None,
        diagnose_button=False,
        skip_tasks_file="",
        ignore_defect_boxes=False,
        cancel_check=None,
        progress_handler=None,
        task_retries=1,
    )


def _redact_trace_text(text: str) -> str:
    """抓取日志里屏蔽常见查询参数中的令牌/密码值，避免误传敏感信息。"""
    value = re.sub(
        r"(?i)((?:token|access_token|password|passwd|code|ticket|sig|signature|"
        r"key|secret|authorization)=)[^&\s\"']+",
        r"\1***",
        str(text),
    )
    # URL 里可能带 user:pass@host，异常文本里也会原样带出。
    return re.sub(r"(?i)(\bhttps?://)[^/\s:@]+:[^/\s@]+@", r"\1***@", value)


_ACTION_LOGGER_JS = r"""
(() => {
  if (window.__uvmpTraceInstalled) return;
  window.__uvmpTraceInstalled = true;
  const clean = (value) => String(value == null ? "" : value)
    .replace(/\s+/g, " ").trim().slice(0, 160);
  const selector = (el) => {
    if (!el || el.nodeType !== 1) return "";
    let out = el.tagName.toLowerCase();
    if (el.id) return out + "#" + CSS.escape(el.id);
    if (el.classList && el.classList.length) {
      out += "." + Array.from(el.classList).slice(0, 4).map((x) => CSS.escape(x)).join(".");
    }
    for (const attr of ["name", "placeholder", "data-pid", "type"]) {
      const value = el.getAttribute && el.getAttribute(attr);
      if (value) out += "[" + attr + "=\"" + clean(value).slice(0, 40) + "\"]";
    }
    return out;
  };
  const valueOf = (el) => {
    if (!el) return "";
    if (String(el.type || "").toLowerCase() === "password") return "***";
    if ("value" in el) return clean(el.value);
    return clean(el.innerText);
  };
  const emit = (kind, event, extra) => {
    const el = event.target;
    const data = Object.assign({
      kind: kind,
      t: Date.now(),
      url: location.href,
      selector: selector(el),
      tag: el && el.tagName ? String(el.tagName).toLowerCase() : "",
      text: clean(el && el.innerText),
      value: valueOf(el),
    }, extra || {});
    if (el && String(el.type || "").toLowerCase() === "password") {
      data.key = "***";
      data.code = "***";
    }
    try { console.log("__UVMP_TRACE__" + JSON.stringify(data)); } catch (_err) {}
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
  let lastScroll = 0;
  document.addEventListener("scroll", (e) => {
    const now = Date.now();
    if (now - lastScroll < 300) return;
    lastScroll = now;
    emit("scroll", e, {
      scrollX: window.scrollX, scrollY: window.scrollY,
      targetTop: e.target && e.target.scrollTop,
      targetLeft: e.target && e.target.scrollLeft
    });
  }, true);
})();
"""


def _wait_manual_stop(seconds: float, page=None, shots_dir: Path = None) -> int:
    """手动操作等待：回车结束；无 TTY 时按 --seconds 超时结束；定期截图。"""
    wait_seconds = max(1.0, float(seconds or 600))
    print(
        "手动抓取模式：请在浏览器中完成 登录 → 任务 → 筛选 → 审核；"
        "按 Enter 结束，或 %.0f 秒后自动结束" % wait_seconds,
        flush=True,
    )
    deadline = time.monotonic() + wait_seconds
    next_shot = 0.0
    shots = 0
    while time.monotonic() < deadline:
        now = time.monotonic()
        if page is not None and shots_dir is not None and now >= next_shot and shots < 200:
            try:
                shots_dir.mkdir(parents=True, exist_ok=True)
                page.screenshot(
                    path=str(shots_dir / ("%04d.png" % shots)),
                    timeout=3000,
                )
                shots += 1
            except Exception:
                pass
            next_shot = now + 3.0
        try:
            if sys.stdin and sys.stdin.isatty():
                import select

                ready, _, _ = select.select([sys.stdin], [], [], 1)
                if ready:
                    sys.stdin.readline()
                    return shots
                continue
        except Exception:
            pass
        time.sleep(1)
    return shots


def _start_trace_session(runner, outdir: Path, report: dict):
    """开启 Playwright trace + 页面事件/手动操作日志；trace 不可用时仍保留事件。"""
    context = runner.page.context
    trace_path = outdir / "trace.zip"
    events_path = outdir / "events.log"
    actions_path = outdir / "actions.jsonl"
    shots_dir = outdir / "shots"
    events = events_path.open("a", encoding="utf-8")
    actions = actions_path.open("a", encoding="utf-8")
    attached = set()
    trace_started = False

    def write_event(line: str) -> None:
        try:
            events.write(_redact_trace_text(line.rstrip()) + "\n")
            events.flush()
        except Exception:
            pass

    def on_console(page, message) -> None:
        text = str(message.text or "")
        if text.startswith("__UVMP_TRACE__"):
            try:
                actions.write(
                    _redact_trace_text(text[len("__UVMP_TRACE__") :]) + "\n"
                )
                actions.flush()
            except Exception:
                pass
            return
        write_event("[console:%s] %s" % (message.type, text))

    def install(frame) -> None:
        try:
            frame.evaluate(_ACTION_LOGGER_JS)
        except Exception:
            pass

    def install_all(page) -> None:
        try:
            frames = list(page.frames)
        except Exception:
            return
        for frame in frames:
            install(frame)

    def on_frame_navigated(frame) -> None:
        write_event("[framenavigated] %s" % frame.url)
        # 整页登录/跳转后旧 document 的事件监听会丢失，导航后重新注入。
        install(frame)

    def attach_page(page) -> None:
        key = id(page)
        if key in attached:
            return
        attached.add(key)
        page.on("console", lambda message: on_console(page, message))
        page.on("framenavigated", on_frame_navigated)
        page.on("domcontentloaded", lambda: install_all(page))
        page.on("load", lambda: install_all(page))
        page.on("pageerror", lambda exc: write_event("[pageerror] %s" % exc))
        page.on(
            "dialog",
            lambda dialog: write_event(
                "[dialog] %s %s" % (dialog.type, dialog.message)
            ),
        )
        page.on("frameattached", install)
        install_all(page)
        if page is not runner.page:
            write_event("[page] 新页面 %s" % page.url)

    try:
        context.tracing.start(screenshots=True, snapshots=True, sources=True)
        trace_started = True
    except Exception as exc:
        report["trace_error"] = _redact_trace_text(str(exc))[:300]
        write_event("[trace-error] %s" % str(exc)[:300])

    context.on("request", lambda req: write_event(
        "[request] %s %s %s" % (req.method, req.resource_type, req.url)
    ))
    context.on("response", lambda res: write_event(
        "[response] %s %s" % (res.status, res.url)
    ))
    context.on("requestfailed", lambda req: write_event(
        "[requestfailed] %s %s" % (req.url, req.failure)
    ))
    try:
        context.on("page", attach_page)
    except Exception:
        pass
    attach_page(runner.page)

    report["trace_path"] = str(trace_path) if trace_started else ""
    report["events_path"] = str(events_path)
    report["actions_path"] = str(actions_path)
    report["shots_path"] = str(shots_dir)
    runner.log(
        "已开启完整抓取：trace=%s events=%s actions=%s"
        % (trace_path if trace_started else "不可用", events_path, actions_path)
    )
    return {
        "context": context if trace_started else None,
        "events": events,
        "actions": actions,
        "trace_path": trace_path,
    }


def _stop_trace_session(session, report: dict) -> None:
    if not session:
        return
    context = session.get("context")
    trace_path = session.get("trace_path")
    if context is not None and trace_path is not None:
        try:
            context.tracing.stop(path=str(trace_path))
        except Exception as exc:
            report["trace_error"] = _redact_trace_text(str(exc))[:300]
    for key in ("events", "actions"):
        handle = session.get(key)
        try:
            if handle is not None:
                handle.close()
        except Exception:
            pass


def _write_report(path: Path, report: dict) -> None:
    """先写同目录临时文件再替换，避免中断时留下半截 JSON。"""
    temp_path = path.with_name("%s.%d.tmp" % (path.name, os.getpid()))
    try:
        temp_path.write_text(
            json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8"
        )
        os.replace(temp_path, path)
    except Exception as exc:
        print(
            "写入 %s 失败：%s" % (path, _redact_trace_text(str(exc))),
            file=sys.stderr,
        )
    finally:
        try:
            if temp_path.exists():
                temp_path.unlink()
        except OSError:
            pass


def main(argv=None) -> int:
    args = _parse_args(argv)
    try:
        engine = _load_engine(args.engine_path)
        engine_version = str(getattr(engine, "SCRIPT_VERSION", ""))
        if engine_version and engine_version < "2026-11-10.02":
            raise RuntimeError(
                "当前加载的是旧引擎 %s（%s），仍会直接打开任务页深链接并 404；"
                "请把修复后的 defect_review_trial.py 与本脚本放同一目录，"
                "或用 --engine-path 指向修复后的引擎"
                % (engine_version, getattr(engine, "__file__", ""))
            )
        cfg, config_source, config_path = _read_client_config(engine)
        values = _resolve_values(args, cfg)
    except Exception as exc:
        print("准备失败：%s" % _redact_trace_text(str(exc)), file=sys.stderr)
        return 2
    outdir = (
        Path(args.outdir).expanduser()
        if args.outdir
        else Path.home() / ("nav-test-" + time.strftime("%Y%m%d_%H%M%S"))
    )
    outdir.mkdir(parents=True, exist_ok=True)
    report_path = outdir / "nav-test.json"
    profile_dir = values["profile_dir"] or str(
        Path.home() / ".uvmp-toolkit" / "nav-test" / "profile"
    )
    report = {
        "script_version": SCRIPT_VERSION,
        "engine_version": engine_version,
        "engine_path": str(getattr(engine, "__file__", "")),
        "config_source": config_source,
        "config_path": config_path,
        "instance_id": values["instance_id"],
        "instance_name": values["instance_name"],
        "start_url": _redact_trace_text(values["start_url"]),
        "cdp_url": values["cdp_url"],
        "profile_dir": profile_dir,
        "task_status": values["task_status"],
        "mission_type": values["mission_type"],
        "voltage_level": values["voltage_level"],
        "line_name": values["line_name"],
        "start_date": values["start_date"],
        "end_date": values["end_date"],
        "simulate_404": bool(args.simulate_404),
        "simulate_nav_timeout": bool(args.simulate_nav_timeout),
        "manual": bool(args.manual),
        "trace": bool(args.trace or args.manual),
        "trace_path": "",
        "events_path": "",
        "actions_path": "",
        "shots_path": "",
        "shots_count": 0,
        "nav_timeout_visible": False,
        "nav_timeout_elapsed": 0.0,
        "task_list_url": "",
        "recovered_after_404": False,
        "records": 0,
        "total": 0,
        "page": 0,
        "pages": 0,
        "passed": False,
        "entered_task_guid": "",
        "entered_task_name": "",
        "task_dialog_opened": False,
        "task_dialog_closed": False,
        "scan_summary": {},
        "error": "",
    }
    runner = None
    trace_session = None
    try:
        settings = engine.ReviewSettings(
            task_status=values["task_status"],
            mission_type=values["mission_type"],
            voltage_level=values["voltage_level"],
            line_name=values["line_name"],
            start_date=values["start_date"],
            end_date=values["end_date"],
        )
        runner_args = _runner_args(values, profile_dir)
        runner = engine.ReviewRunner(
            runner_args,
            outdir,
            log_handler=lambda line: print(line, flush=True),
        )
        runner.log("导航筛选冒烟开始（脚本 %s）" % SCRIPT_VERSION)
        runner.log(
            "引擎：%s（%s）" % (report["engine_path"], engine_version or "未知版本")
        )
        runner.log(
            "实例：%s（%s）；配置来源：%s"
            % (values["instance_name"], values["instance_id"], config_source or "命令行")
        )
        runner.log("输出目录：%s" % outdir)
        runner.load_playwright()
        if not engine.cdp_reachable(values["cdp_url"]):
            if args.no_launch:
                raise RuntimeError(
                    "受控浏览器未运行且指定了 --no-launch：%s"
                    % values["cdp_url"]
                )
            port = engine._cdp_port(values["cdp_url"]) or 9222
            runner.log("受控浏览器未运行，启动：%s" % values["cdp_url"])
            engine.launch_browser(runner_args, port, runner.log)
        runner.connect(launched=True)
        runner.log("已连接浏览器，当前页面：%s" % _redact_trace_text(runner.page.url))

        if args.trace or args.manual:
            trace_session = _start_trace_session(runner, outdir, report)
        if args.manual:
            runner._open_start_url()
            report["shots_count"] = _wait_manual_stop(
                args.seconds, runner.page, outdir / "shots"
            )
            report["passed"] = True
            _write_report(report_path, report)
            runner.log("手动抓取结束：%s" % report.get("trace_path", ""))
            return 0

        # open_task_list 内部完成：404/空白页回启动网址 → 登录 → 菜单进入任务列表。
        runner.open_task_list()
        report["task_list_url"] = _redact_trace_text(runner.page.url)
        runner.log("任务列表已就绪：%s" % _redact_trace_text(runner.page.url))

        if args.simulate_nav_timeout:
            runner.log("模拟任务页已加载后的直达调用")
            if not runner._task_list_visible():
                raise RuntimeError(
                    "任务列表未就绪，无法模拟直达；请先确认页面已加载"
                )
            started = time.monotonic()
            ok = bool(runner._goto_task_page_direct())
            elapsed = time.monotonic() - started
            report["nav_timeout_visible"] = ok
            report["nav_timeout_elapsed"] = round(elapsed, 3)
            runner.log(
                "任务页已加载，直达逻辑返回 %s，耗时 %.2fs" % (ok, elapsed)
            )
            if not ok:
                raise RuntimeError("任务页已加载，但直达逻辑仍判失败")
            if elapsed > 2.0:
                raise RuntimeError(
                    "任务页已加载，但直达逻辑仍等待 %.1fs 导航" % elapsed
                )

        if args.simulate_404:
            origin = engine._safe_origin(values["start_url"])
            if not origin:
                raise RuntimeError(
                    "无法从启动网址解析同源地址：%s" % values["start_url"]
                )
            target = origin + engine.TASK_PAGE_PATH
            runner.log("模拟任务页深链接：%s" % target)
            runner.page.goto(target, wait_until="commit")
            runner.page.wait_for_timeout(500)
            runner.open_task_list()
            report["recovered_after_404"] = bool(runner._task_list_visible())
            runner.log(
                "404 深链接后已恢复任务列表：%s" % report["recovered_after_404"]
            )
            if not report["recovered_after_404"]:
                raise RuntimeError("404 深链接后未能恢复缺陷识别任务列表")

        runner.apply_filters(settings)
        runner.log("筛选已填写，开始点击查询")
        records, total, current, pages = runner.query_tasks(settings)
        report.update(
            {
                "task_list_url": _redact_trace_text(runner.page.url),
                "records": len(records),
                "total": total,
                "page": current,
                "pages": pages,
            }
        )
        shot = outdir / "nav_test_ok.png"
        try:
            runner.page.screenshot(
                path=str(shot), full_page=True, timeout=10000
            )
            runner.log("成功截图：%s" % shot)
        except Exception as exc:
            runner.log("成功截图保存失败：%s" % str(exc)[:200])

        if not args.no_enter_task:
            runner.log("查询成功，进入第一个可审核任务")
            candidates, scan_summary = runner.scan_task_list(
                settings, stop_at_first=True
            )
            report["scan_summary"] = scan_summary
            if not candidates:
                raise RuntimeError(
                    "查询成功但没有可进入的任务（无可审核状态或「进入」按钮）"
                )
            entry = candidates[0]
            task_name = str(
                (entry.get("item") or {}).get("taskName") or ""
            ).strip()
            report["entered_task_guid"] = str(entry.get("guid") or "")
            report["entered_task_name"] = task_name
            runner.log(
                "尝试进入任务《%s》GUID=%s"
                % (task_name, report["entered_task_guid"])
            )
            runner.enter_task(entry)
            dialog = runner._wait_review_dialog()
            runner._wait_task_dialog_loaded(dialog, task_name)
            report["task_dialog_opened"] = True
            runner.log("任务弹层已打开：%s" % task_name)
            task_shot = outdir / "nav_test_task_open.png"
            try:
                runner.page.screenshot(
                    path=str(task_shot), full_page=False, timeout=10000
                )
                runner.log("任务弹层截图：%s" % task_shot)
            except Exception as exc:
                runner.log("任务弹层截图失败：%s" % str(exc)[:200])
            try:
                closed = runner.close_task(dialog)
                report["task_dialog_closed"] = bool(closed)
                runner.log(
                    "已关闭任务弹层（未审核、未提交）：%s" % bool(closed)
                )
            except Exception as exc:
                runner.log("关闭任务弹层失败（继续 PASS）：%s" % str(exc)[:200])

        report["passed"] = True
        _write_report(report_path, report)
        suffix = ""
        if report.get("task_dialog_opened"):
            suffix = "；已打开任务《%s》" % report.get("entered_task_name", "")
        runner.log(
            "NAV TEST PASS：筛选已填写并查询成功（第 %d/%d 页，本页 %d 条，总数 %d）%s"
            % (current, pages, len(records), total, suffix)
        )
        return 0
    except Exception as exc:
        report["error"] = _redact_trace_text(str(exc))
        print(_redact_trace_text(traceback.format_exc()))
        if runner is not None:
            try:
                runner.save_diagnostic(runner.page, "nav_test", error=exc)
            except Exception:
                pass
        _write_report(report_path, report)
        print("NAV TEST FAIL：%s" % _redact_trace_text(str(exc))[:500], file=sys.stderr)
        return 1
    finally:
        _stop_trace_session(trace_session, report)
        if runner is not None:
            try:
                runner.close()
            except Exception:
                pass


if __name__ == "__main__":
    sys.exit(main())
