# -*- coding: utf-8 -*-
"""课程自动学习：受控浏览器、按培训班检查点、暂停恢复与 Job 编排。"""

import importlib.util
import sys
import time
from pathlib import Path
from types import SimpleNamespace

import browser_automation
import config as _config
import jobs as _jobs
import state

APP_ID = "course_autoplay"
_ENGINE = None


def _engine():
    global _ENGINE
    if _ENGINE is None:
        if getattr(sys, "frozen", False):
            path = Path(sys._MEIPASS) / "course_autoplay_trial.py"
        else:
            path = Path(__file__).resolve().parents[3] / "course_autoplay_trial.py"
        spec = importlib.util.spec_from_file_location(
            "uvmp_course_autoplay_engine", path
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        _ENGINE = module
    return _ENGINE


def _checkpoint_path():
    return state.state_dir() / "course-autoplay" / "checkpoints.json"


def _load_checkpoints():
    data = state.read_json(_checkpoint_path(), {})
    return data if isinstance(data, dict) else {}


def _clean_checkpoint(raw):
    clean = {
        "version": 1,
        "active": False,
        "resume_page": 1,
        "processed": 0,
        "attempts": {},
        "last_progress": {},
        "last_errors": {},
        "skipped_ids": [],
        "skip_reasons": {},
    }
    if not isinstance(raw, dict):
        return clean
    for key in ("attempts", "last_progress", "last_errors", "skip_reasons"):
        if isinstance(raw.get(key), dict):
            clean[key] = raw[key]
    if isinstance(raw.get("skipped_ids"), list):
        clean["skipped_ids"] = [x for x in raw["skipped_ids"] if isinstance(x, str)]
    for key in ("resume_page", "processed"):
        try:
            clean[key] = max(
                0 if key == "processed" else 1, int(raw.get(key, clean[key]))
            )
        except (TypeError, ValueError):
            pass
    clean["active"] = bool(raw.get("active"))
    if isinstance(raw.get("last_result"), dict):
        clean["last_result"] = raw["last_result"]
    return clean


def _persist(checkpoints, list_key, checkpoint):
    checkpoint["updated_at"] = time.strftime("%Y-%m-%d %H:%M:%S")
    checkpoints[list_key] = checkpoint
    state.write_json(_checkpoint_path(), checkpoints)


def _summary(summary, checkpoint, list_key):
    pending = summary.get("pending") or []
    skip_reasons = checkpoint.get("skip_reasons") or {}
    pending_courses = []
    for entry in pending[:100]:
        item = entry["item"]
        resource_id = item["resourceid"]
        pending_courses.append(
            {
                "name": item.get("coursename") or resource_id,
                "page": entry["page"],
                "progress": _engine().course_progress(item),
                "reason": entry.get("reason") or skip_reasons.get(resource_id, ""),
            }
        )
    return {
        "paused": False,
        "list_key": list_key,
        "total": summary.get("total", 0),
        "completed": summary.get("completed", 0),
        "pending": len(pending),
        "skipped": len(skip_reasons),
        "pending_courses": pending_courses,
        "dir": str(state.logs_dir()),
    }


def status(cfg):
    result = browser_automation.status(cfg)
    checkpoints = _load_checkpoints()
    result["active_lists"] = len(
        [cp for cp in checkpoints.values() if isinstance(cp, dict) and cp.get("active")]
    )
    return result


def launch_browser(cfg):
    if not browser_automation.playwright_available():
        raise RuntimeError(
            "当前客户端未内置 Playwright，请安装完整客户端后再打开受控浏览器"
        )
    return browser_automation.launch(cfg)


def run(job, params: dict) -> dict:
    cfg = _config.load()
    opts = browser_automation.settings(cfg)
    scan_only = bool(params.get("scan_only", False))
    timeout = int(params.get("timeout", opts["step_timeout"]))
    max_attempts = int(
        params.get(
            "max_attempts", (cfg.get("course_autoplay") or {}).get("max_attempts", 3)
        )
    )
    if timeout <= 0 or max_attempts <= 0:
        raise RuntimeError("单步超时和单课尝试次数必须大于 0")
    checkpoints = _load_checkpoints()
    list_key = ""
    checkpoint = None
    finished = False

    def on_progress(current, total, label):
        if checkpoint is not None and list_key:
            _persist(checkpoints, list_key, checkpoint)
        job.set_progress(current, total, label)

    args = SimpleNamespace(
        cdp_url=opts["cdp_url"],
        timeout=timeout,
        max_attempts=max_attempts,
        cancel_check=job.check_cancel,
        progress_handler=on_progress,
        interrupt_exceptions=(_jobs.Cancelled,),
        diagnostic_prefix=job.id + "_failure",
    )
    engine = _engine()
    runner = engine.CourseRunner(args, state.logs_dir(), log_handler=job.log)

    def paused_result():
        job.log("学习已暂停：受控浏览器和登录状态已保留，可切换课程列表后再次开始学习")
        return {
            "paused": True,
            "list_key": list_key,
            "resume_page": (checkpoint or {}).get("resume_page", 0),
            "processed": (checkpoint or {}).get("processed", 0),
            "dir": str(state.logs_dir()),
        }

    try:
        runner.load_playwright()
        runner.connect(launched=False)
        runner.find_list_page()
        first_page = runner.reset_filters()
        if scan_only:
            summary = runner.scan_courses(stop_first=False)
            runner.report_scan(summary)
            job.log("只扫描模式结束，未进入任何课程")
            finished = True
            return _summary(summary, {}, "")

        list_key = engine.course_list_key(runner.list_page.url)
        previous = checkpoints.get(list_key)
        resumed = isinstance(previous, dict) and previous.get("active")
        checkpoint = _clean_checkpoint(previous if resumed else None)
        checkpoint["active"] = True
        if resumed:
            job.log(
                "恢复当前培训班：第 %d 页，已处理 %d 门次，跳过 %d 门"
                % (
                    checkpoint["resume_page"],
                    checkpoint["processed"],
                    len(checkpoint["skipped_ids"]),
                )
            )
        else:
            job.log("当前培训班开始新一轮学习")
        _persist(checkpoints, list_key, checkpoint)
        summary = runner.run(first_page, checkpoint=checkpoint)
        result = _summary(summary, checkpoint, list_key)
        checkpoint["last_result"] = result
        finished = True
        return result
    except _jobs.Cancelled:
        return paused_result()
    except Exception:
        if job.cancelled:
            return paused_result()
        raise
    finally:
        if checkpoint is not None and list_key:
            checkpoint["active"] = not finished
            _persist(checkpoints, list_key, checkpoint)
        runner.close()
