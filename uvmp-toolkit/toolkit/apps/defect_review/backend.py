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


def _default_outdir():
    return state.logs_dir() / "defect-review" / time.strftime("%Y%m%d_%H%M%S")


def _default_skip_path():
    return state.state_dir() / "defect-review" / "跳过任务.xlsx"


def _instance(params):
    instance = (params or {}).get("instance")
    return instance if isinstance(instance, dict) else {}


def _merged(cfg, params):
    item = dict(_app_cfg(cfg))
    instance = _instance(params)
    for key, value in instance.items():
        if key != "params" and value not in (None, ""):
            item[key] = value
    for key, value in (instance.get("params") or {}).items():
        if value not in (None, ""):
            item[key] = value
    for key, value in (params or {}).items():
        if key != "instance" and value not in (None, ""):
            item[key] = value
    return item


def _browser_values(cfg, params):
    item = _merged(cfg, params)
    opts = browser_automation.settings(cfg)
    return {
        "cdp_url": str(item.get("cdp_url") or opts["cdp_url"]),
        "profile_dir": str(item.get("profile_dir") or opts["profile_dir"]),
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
    engine = _engine()
    for key, label, options in (
        ("task_status", "任务状态", engine.TASK_STATUS_OPTIONS),
        ("mission_type", "作业性质", engine.MISSION_TYPE_OPTIONS),
        ("voltage_level", "电压等级", engine.VOLTAGE_LEVEL_OPTIONS),
    ):
        value = str(merged.get(key) or "").strip()
        if value not in options:
            raise RuntimeError("%s无效：%s" % (label, value))
        merged[key] = value
    for key, label in (("line_name", "线路名称"), ("start_date", "开始日期"), ("end_date", "结束日期")):
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
        ("image_timeout", 10, 1, 300),
        ("image_retries", 0, 0, 10),
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
    merged["outdir"] = str(Path(outdir).expanduser()) if outdir else str(_default_outdir())
    skip_path = str(merged.get("skip_tasks_file") or "").strip()
    merged["skip_tasks_file"] = (
        str(Path(skip_path).expanduser()) if skip_path else str(_default_skip_path())
    )
    merged["ignore_defect_boxes"] = bool(merged.get("ignore_defect_boxes", False))
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

    args = SimpleNamespace(
        cdp_url=values.get("cdp_url") or opts["cdp_url"],
        timeout=opts["step_timeout"],
        image_timeout=values["image_timeout"],
        image_retries=values["image_retries"],
        login_timeout=opts["step_timeout"],
        start_url="",
        launch=False,
        manual_task_page=True,
        diagnose_button=False,
        skip_tasks_file=values["skip_tasks_file"],
        ignore_defect_boxes=values["ignore_defect_boxes"],
        cancel_check=job.check_cancel,
        progress_handler=progress,
    )
    runner = engine.ReviewRunner(args, outdir, log_handler=job.log)
    job.log("输出目录：%s" % outdir)
    job.log("任务名称跳过表：%s" % values["skip_tasks_file"])
    if values["ignore_defect_boxes"]:
        job.log("已勾选无视缺陷框：所有可加载图片都会审核")
    try:
        runner.load_playwright()
        runner.connect(launched=False)
        result = runner.run(settings)
        result = dict(result or {})
        result["outdir"] = str(outdir)
        result["skip_tasks_file"] = values["skip_tasks_file"]
        return result
    except _jobs.Cancelled:
        return {"cancelled": True, "dir": str(outdir)}
    finally:
        runner.close()
