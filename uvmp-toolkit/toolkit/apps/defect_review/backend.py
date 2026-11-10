# -*- coding: utf-8 -*-
"""缺陷识别自动审核：受控浏览器、筛选参数、跳过表与 Job 编排。"""

import importlib.util
import sys
import time
from pathlib import Path
from types import SimpleNamespace

import browser_automation
import config as _config
import jobs as _jobs
import state

APP_ID = "defect_review"
_ENGINE = None


def _engine():
    global _ENGINE
    if _ENGINE is None:
        if getattr(sys, "frozen", False):
            path = Path(sys._MEIPASS) / "defect_review_trial.py"
        else:
            path = Path(__file__).resolve().parents[3] / "defect_review_trial.py"
        spec = importlib.util.spec_from_file_location(
            "uvmp_defect_review_engine", path
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        _ENGINE = module
    return _ENGINE


def _app_cfg(cfg):
    item = cfg.get("defect_review") or {}
    return item if isinstance(item, dict) else {}


def _default_outdir(instance_id=""):
    suffix = time.strftime("%Y%m%d_%H%M%S")
    label = "".join(
        ch if (ch.isalnum() or ch in "._-") else "_" for ch in str(instance_id or "")
    ).strip("._")
    if label and label != "default":
        suffix += "-" + label[:40]
    return state.logs_dir() / "defect-review" / suffix


def _default_skip_path():
    return state.state_dir() / "defect-review" / "跳过任务.xlsx"


def _instance(params):
    instance = (params or {}).get("instance")
    return instance if isinstance(instance, dict) else {}


# 这些字段即使是空值也必须覆盖配置/实例里的旧值，不能按空值忽略：
# 作业性质/电压/线路空值代表「全部」；账号密码和启动网址必须按实例隔离，
# 实例留空时不能继承上一个实例的全局值。
_ALLOW_EMPTY_FILTERS = (
    "mission_type",
    "voltage_level",
    "line_name",
    "username",
    "password",
    "start_url",
)


def _merged(cfg, params):
    item = dict(_app_cfg(cfg))
    instance = _instance(params)
    for key, value in instance.items():
        if key != "params" and (value not in (None, "") or key in _ALLOW_EMPTY_FILTERS):
            item[key] = value
    for key, value in (instance.get("params") or {}).items():
        if value not in (None, "") or key in _ALLOW_EMPTY_FILTERS:
            item[key] = value
    for key, value in (params or {}).items():
        if key != "instance" and (
            value not in (None, "") or key in _ALLOW_EMPTY_FILTERS
        ):
            item[key] = value
    return item


def _browser_values(cfg, params):
    item = _merged(cfg, params)
    instance = _instance(params)
    opts = browser_automation.settings(cfg)
    profile_dir = str(instance.get("profile_dir") or "")
    if not profile_dir and instance.get("id") not in (None, "", "default"):
        profile_dir = str(
            state.state_dir()
            / "browser-instances"
            / ("%s-%s" % (APP_ID, instance.get("id")))
            / "profile"
        )
    if not profile_dir:
        profile_dir = str(item.get("profile_dir") or "")
    return {
        "cdp_url": str(item.get("cdp_url") or opts["cdp_url"]),
        "profile_dir": profile_dir or str(opts["profile_dir"]),
        "start_url": str(item.get("start_url") or ""),
    }


def status(cfg, params=None):
    browser = _browser_values(cfg, params)
    result = browser_automation.status(
        cfg,
        cdp_url=browser["cdp_url"],
        profile_dir=browser["profile_dir"],
        start_url=browser["start_url"],
    )
    item = _merged(cfg, params)
    result["start_url"] = browser["start_url"]
    result["outdir"] = str(item.get("outdir") or "")
    result["skip_tasks_file"] = str(item.get("skip_tasks_file") or "")
    result["ignore_defect_boxes"] = bool(item.get("ignore_defect_boxes", False))
    return result


def launch_browser(cfg, params=None):
    if not browser_automation.playwright_available():
        raise RuntimeError(
            "当前客户端未内置 Playwright，请安装完整客户端后再打开受控浏览器"
        )
    browser = _browser_values(cfg, params)
    return browser_automation.launch(
        cfg,
        start_url=browser["start_url"],
        cdp_url=browser["cdp_url"],
        profile_dir=browser["profile_dir"],
    )


def _normalize(cfg, params):
    merged = _merged(cfg, params)
    instance = _instance(params)
    engine = _engine()
    task_status = str(merged.get("task_status") or "").strip()
    if task_status not in engine.TASK_STATUS_OPTIONS:
        raise RuntimeError("任务状态无效：%s" % task_status)
    merged["task_status"] = task_status
    for key, label, options in (
        ("mission_type", "作业性质", engine.MISSION_TYPE_OPTIONS),
        ("voltage_level", "电压等级", engine.VOLTAGE_LEVEL_OPTIONS),
    ):
        value = str(merged.get(key) or "").strip()
        if value and value not in options:
            raise RuntimeError("%s无效：%s" % (label, value))
        merged[key] = value
    merged["line_name"] = str(merged.get("line_name") or "").strip()
    for key, label in (("start_date", "开始日期"), ("end_date", "结束日期")):
        value = str(merged.get(key) or "").strip()
        if not value:
            raise RuntimeError("%s不能为空" % label)
        merged[key] = value
    for key, label in (("start_date", "开始日期"), ("end_date", "结束日期")):
        try:
            time.strptime(merged[key], "%Y-%m-%d")
        except ValueError:
            raise RuntimeError("%s格式应为 YYYY-MM-DD" % label) from None
    if merged["start_date"] > merged["end_date"]:
        raise RuntimeError("开始日期不能晚于结束日期")
    for key, default, minimum, maximum in (
        ("step_timeout", 120, 10, 900),
        ("action_timeout", 5, 1, 120),
        ("navigation_timeout", 15, 5, 300),
        ("login_result_timeout", 20, 5, 300),
        ("captcha_ocr_timeout", 30, 5, 300),
        ("image_timeout", 10, 1, 300),
        ("image_retries", 0, 0, 10),
        ("max_unreviewed", getattr(engine, "DEFAULT_MAX_UNREVIEWED", 400), 1, 100000),
        ("unknown_timeout", getattr(engine, "DEFAULT_UNKNOWN_TIMEOUT", 120), 1, 900),
        ("slow_image_seconds", 15, 5, 300),
        ("slow_image_count", 3, 2, 20),
        ("slow_restart_unreviewed", 50, 0, 100000),
    ):
        try:
            value = int(merged.get(key, default))
        except (TypeError, ValueError):
            raise RuntimeError("%s必须为整数" % key) from None
        if value < minimum or value > maximum:
            raise RuntimeError("%s超出范围" % key)
        merged[key] = value
    merged["cdp_url"] = str(merged.get("cdp_url") or "").strip()
    merged["profile_dir"] = str(merged.get("profile_dir") or "").strip()
    outdir = str(merged.get("outdir") or "").strip()
    merged["outdir"] = (
        str(Path(outdir).expanduser())
        if outdir
        else str(_default_outdir(str(instance.get("id") or "")))
    )
    skip_path = str(merged.get("skip_tasks_file") or "").strip()
    merged["skip_tasks_file"] = (
        str(Path(skip_path).expanduser()) if skip_path else str(_default_skip_path())
    )
    merged["ignore_defect_boxes"] = bool(merged.get("ignore_defect_boxes", False))
    # 缺陷照片归档不提供开关：未勾选无视缺陷框时始终保存原图并标注。
    # 自动登录是唯一模式：账号密码和启动网址始终必填。
    merged["auto_login"] = True
    merged["username"] = str(merged.get("username") or "").strip()
    merged["password"] = str(merged.get("password") or "")
    if not merged["username"] or not merged["password"]:
        raise RuntimeError("请填写自动登录用户名和密码")
    if not str(merged.get("start_url") or "").strip():
        raise RuntimeError("请填写启动网址，自动登录和掉登录恢复都需要")
    merged["reload_after_task"] = bool(merged.get("reload_after_task", True))
    try:
        stall_timeout = int(merged.get("stall_timeout", 120))
    except (TypeError, ValueError):
        raise RuntimeError("卡死检测秒数必须为整数") from None
    if stall_timeout < 30 or stall_timeout > 3600:
        raise RuntimeError("卡死检测秒数必须在 30～3600 之间")
    merged["stall_timeout"] = stall_timeout
    return merged


def run(job, params: dict) -> dict:
    cfg = _config.load()
    values = _normalize(cfg, params)
    engine = _engine()
    settings = engine.ReviewSettings(
        task_status=values["task_status"],
        mission_type=values["mission_type"],
        voltage_level=values["voltage_level"],
        line_name=values["line_name"],
        start_date=values["start_date"],
        end_date=values["end_date"],
    )
    outdir = Path(values["outdir"])
    outdir.mkdir(parents=True, exist_ok=True)
    opts = browser_automation.settings(cfg)

    def progress(current, total, label):
        job.set_progress(current, total, label)

    browser = _browser_values(cfg, params)
    args = SimpleNamespace(
        cdp_url=values.get("cdp_url") or opts["cdp_url"],
        timeout=values["step_timeout"],
        action_timeout=values["action_timeout"],
        navigation_timeout=values["navigation_timeout"],
        login_result_timeout=values["login_result_timeout"],
        captcha_ocr_timeout=values["captcha_ocr_timeout"],
        image_timeout=values["image_timeout"],
        image_retries=values["image_retries"],
        max_unreviewed=values["max_unreviewed"],
        unknown_timeout=values["unknown_timeout"],
        start_url=browser["start_url"],
        profile_dir=browser["profile_dir"],
        browser_path=str((cfg.get("render") or {}).get("chrome") or ""),
        launch=False,
        auto_login=True,
        username=values["username"],
        password=values["password"],
        reload_after_task=values["reload_after_task"],
        stall_timeout=values["stall_timeout"],
        slow_image_seconds=values["slow_image_seconds"],
        slow_image_count=values["slow_image_count"],
        slow_restart_unreviewed=values["slow_restart_unreviewed"],
        window_bounds=values.get("window_bounds"),
        diagnose_button=False,
        skip_tasks_file=values["skip_tasks_file"],
        ignore_defect_boxes=values["ignore_defect_boxes"],
        save_photos=True,
        cancel_check=job.check_cancel,
        progress_handler=progress,
    )
    runner = engine.ReviewRunner(args, outdir, log_handler=job.log)
    # 走 runner.log：客户端任务日志与输出目录 run.log 保持同一份内容。
    runner.log("输出目录：%s" % outdir)
    runner.log("任务名称跳过表：%s" % values["skip_tasks_file"])
    if values["ignore_defect_boxes"]:
        runner.log("已勾选无视缺陷框：所有可加载图片都会审核")
    runner.log(
        "缺陷照片归档：按「线路名称/杆塔号」保存原图，"
        "系统缺陷框另存同名「_标注」图片（无开关，始终开启）"
    )
    runner.log(
        "自动登录：掉登录/页面崩溃/浏览器重启后自动恢复，"
        "并自动进入缺陷识别任务"
    )
    try:
        runner.load_playwright()
        current = browser_automation.status(
            cfg,
            cdp_url=browser["cdp_url"],
            profile_dir=browser["profile_dir"],
            start_url=browser["start_url"],
        )
        if not current.get("connected"):
            runner.log("受控浏览器未运行，自动启动并准备登录")
            try:
                browser_automation.launch(
                    cfg,
                    start_url=browser["start_url"],
                    cdp_url=browser["cdp_url"],
                    profile_dir=browser["profile_dir"],
                )
            except Exception as exc:
                # 启动网址导航超时不应阻断登录流程：浏览器可能已经起来，
                # connect 会附着现有页面，后续由引擎被动识别 /login。
                runner.log(
                    "自动启动浏览器/打开启动网址失败：%s；"
                    "尝试直接连接现有浏览器" % str(exc)[:200]
                )
        runner.connect(launched=True)
        result = runner.run(settings)
        result = dict(result or {})
        result["outdir"] = str(outdir)
        result["skip_tasks_file"] = values["skip_tasks_file"]
        return result
    except _jobs.Cancelled:
        runner.stop_requested = True
        return {"cancelled": True, "dir": str(outdir)}
    finally:
        runner.close()
