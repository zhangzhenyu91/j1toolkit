#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""机巡审核「登录 → 等 /home 加载 → 直达任务页/回退菜单 → 填筛选 → 查询」冒烟脚本。

只验证流程，不审核图片、不进入任务、不提交任何审核结果。适用于现场排查：
- 登录后是否能经菜单进入「智能分析 → 智能识别 → 缺陷识别任务」；
- 筛选条件是否真实填入并点击「查询」拿到列表响应；
- 可选 --simulate-404：先打开任务页深链接（不支持深链接的站点会 404），
  再验证脚本能点「返回首页」或回到启动网址、重新走菜单并继续筛选。

默认直接复用客户端已保存的第一个实例参数（启动网址、账号密码、CDP、profile、
筛选条件、超时）；命令行参数只在需要覆盖时传：
  python defect_review_nav_test.py --simulate-404

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
import sys
import time
import traceback
from pathlib import Path
from types import SimpleNamespace

SCRIPT_VERSION = "2026-11-10.05"


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
        "--no-launch",
        action="store_true",
        help="受控浏览器未运行时直接报错，不自动启动",
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
        reload_after_task=True,
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


def _write_report(path: Path, report: dict) -> None:
    try:
        path.write_text(
            json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8"
        )
    except Exception as exc:
        print("写入 %s 失败：%s" % (path, exc), file=sys.stderr)


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
        print("准备失败：%s" % exc, file=sys.stderr)
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
        "start_url": values["start_url"],
        "cdp_url": values["cdp_url"],
        "profile_dir": profile_dir,
        "task_status": values["task_status"],
        "mission_type": values["mission_type"],
        "voltage_level": values["voltage_level"],
        "line_name": values["line_name"],
        "start_date": values["start_date"],
        "end_date": values["end_date"],
        "simulate_404": bool(args.simulate_404),
        "task_list_url": "",
        "recovered_after_404": False,
        "records": 0,
        "total": 0,
        "page": 0,
        "pages": 0,
        "passed": False,
        "error": "",
    }
    runner = None
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
        runner.log("已连接浏览器，当前页面：%s" % runner.page.url)

        # open_task_list 内部完成：404/空白页回启动网址 → 登录 → 菜单进入任务列表。
        runner.open_task_list()
        report["task_list_url"] = runner.page.url
        runner.log("任务列表已就绪：%s" % runner.page.url)

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
                "task_list_url": runner.page.url,
                "records": len(records),
                "total": total,
                "page": current,
                "pages": pages,
                "passed": True,
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
        _write_report(report_path, report)
        runner.log(
            "NAV TEST PASS：筛选已填写并查询成功（第 %d/%d 页，本页 %d 条，总数 %d）"
            % (current, pages, len(records), total)
        )
        return 0
    except Exception as exc:
        report["error"] = str(exc)
        print(traceback.format_exc())
        if runner is not None:
            try:
                runner.save_diagnostic(runner.page, "nav_test", error=exc)
            except Exception:
                pass
        _write_report(report_path, report)
        print("NAV TEST FAIL：%s" % str(exc)[:500], file=sys.stderr)
        return 1
    finally:
        if runner is not None:
            try:
                runner.close()
            except Exception:
                pass


if __name__ == "__main__":
    sys.exit(main())
