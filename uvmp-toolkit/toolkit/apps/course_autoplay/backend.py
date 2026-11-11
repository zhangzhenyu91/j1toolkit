# -*- coding: utf-8 -*-
"""课程自动学习：受控浏览器、按培训班检查点、暂停恢复与 Job 编排。"""

import contextlib
import hashlib
import importlib.util
import json
import math
import os
import re
import sys
import threading
import time
import uuid
from pathlib import Path
from types import SimpleNamespace

import browser_automation
import config as _config
import jobs as _jobs
import state

APP_ID = "course_autoplay"
_ENGINE = None
# 断点的读-改-写既要跨线程也要跨进程串行：同进程用线程锁，跨进程用状态目录下的锁文件
_PERSIST_THREAD_LOCK = threading.Lock()


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
    if not isinstance(data, dict):
        return {}
    return {key: value for key, value in data.items() if isinstance(key, str)}


def _write_checkpoints(data):
    """写唯一临时文件再替换，避免多实例共用同一 .tmp 互相踩踏"""
    path = _checkpoint_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name("%s.%s.tmp" % (path.name, uuid.uuid4().hex))
    try:
        tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
        tmp.replace(path)
    except Exception:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise


def _acquire_file_lock(path):
    """跨进程排他锁，返回 fd；平台不支持时返回 None 并退化为不加锁"""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(str(path), os.O_RDWR | os.O_CREAT, 0o600)
    try:
        if os.name == "nt":
            import msvcrt

            os.lseek(fd, 0, os.SEEK_SET)
            if os.fstat(fd).st_size == 0:
                os.write(fd, b"0")
                os.lseek(fd, 0, os.SEEK_SET)
            msvcrt.locking(fd, msvcrt.LK_LOCK, 1)
        else:
            import fcntl

            fcntl.flock(fd, fcntl.LOCK_EX)
        return fd
    except OSError:
        os.close(fd)
        return None


def _release_file_lock(fd):
    if fd is None:
        return
    try:
        if os.name == "nt":
            import msvcrt

            os.lseek(fd, 0, os.SEEK_SET)
            msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
        else:
            import fcntl

            fcntl.flock(fd, fcntl.LOCK_UN)
    except OSError:
        pass
    try:
        os.close(fd)
    except OSError:
        pass


@contextlib.contextmanager
def _checkpoint_guard():
    """同进程串行化；锁文件不可用时退化为仅线程锁，写入本身仍是原子替换"""
    with _PERSIST_THREAD_LOCK:
        try:
            fd = _acquire_file_lock(_checkpoint_path().with_name("checkpoints.lock"))
        except OSError:
            fd = None
        try:
            yield
        finally:
            _release_file_lock(fd)


def _as_int(value, minimum, default):
    try:
        return max(minimum, int(value))
    except (TypeError, ValueError):
        return default


def _as_rate(value):
    try:
        rate = float(value)
    except (TypeError, ValueError):
        return None
    return rate if math.isfinite(rate) else None


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
        "last_error": "",
    }
    if not isinstance(raw, dict):
        return clean
    for key in ("attempts", "last_progress"):
        source = raw.get(key)
        if not isinstance(source, dict):
            continue
        target = {}
        for resource_id, value in source.items():
            if not isinstance(resource_id, str) or not resource_id:
                continue
            if key == "attempts":
                number = _as_int(value, 0, None)
            else:
                number = _as_rate(value)
            if number is not None:
                target[resource_id] = number
        clean[key] = target
    for key in ("last_errors", "skip_reasons"):
        source = raw.get(key)
        if not isinstance(source, dict):
            continue
        target = {}
        for resource_id, value in source.items():
            text = value.strip() if isinstance(value, str) else ""
            if text:
                target[str(resource_id)] = text
        clean[key] = target
    if isinstance(raw.get("skipped_ids"), list):
        clean["skipped_ids"] = [
            item.strip()
            for item in raw["skipped_ids"]
            if isinstance(item, str) and item.strip()
        ]
    clean["resume_page"] = _as_int(raw.get("resume_page"), 1, 1)
    clean["processed"] = _as_int(raw.get("processed"), 0, 0)
    clean["active"] = bool(raw.get("active"))
    last_error = raw.get("last_error")
    if isinstance(last_error, str):
        clean["last_error"] = last_error.strip()[:300]
    if isinstance(raw.get("last_result"), dict):
        clean["last_result"] = raw["last_result"]
    return clean


def _persist(list_key, checkpoint):
    """加锁读-改-写：只替换本实例条目，保留其他实例断点。

    返回失败原因；落盘失败不影响刷课本身，仅提示本次恢复可能不精确。
    """
    if not list_key or not isinstance(checkpoint, dict):
        return ""
    checkpoint["updated_at"] = time.strftime("%Y-%m-%d %H:%M:%S")
    try:
        with _checkpoint_guard():
            data = _load_checkpoints()
            data[list_key] = checkpoint
            _write_checkpoints(data)
        return ""
    except Exception as exc:
        return str(exc)[:200]


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
        "studiable": summary.get("studiable", 0),
        "completed": summary.get("completed", 0),
        "pending": len(pending),
        "skipped": len(skip_reasons),
        "pending_courses": pending_courses,
        "dir": str(state.logs_dir()),
    }


def _instance(params):
    instance = (params or {}).get("instance")
    return instance if isinstance(instance, dict) else {}


def _instance_slug(instance_id):
    """实例标识进目录名：保留安全字符，原值不同则追加摘要避免撞名"""
    raw = str(instance_id or "").strip() or "default"
    slug = re.sub(r"[^0-9A-Za-z_-]+", "-", raw).strip("-") or "default"
    if slug != raw:
        digest = hashlib.sha1(raw.encode("utf-8")).hexdigest()[:8]
        slug = "%s-%s" % (slug[:32], digest)
    return slug


def _browser_values(cfg, params):
    """未显式指定 profile_dir 时按实例隔离；不与其他应用共用登录态"""
    instance = _instance(params)
    opts = browser_automation.settings(cfg)
    instance_id = instance.get("id") or (params or {}).get("instance_id") or "default"
    profile_dir = str(instance.get("profile_dir") or "")
    if not profile_dir:
        profile_dir = str(
            state.state_dir()
            / "browser-instances"
            / ("%s-%s" % (APP_ID, _instance_slug(instance_id)))
            / "profile"
        )
    return {
        "cdp_url": str(instance.get("cdp_url") or opts["cdp_url"]),
        "profile_dir": profile_dir,
        "start_url": str(instance.get("start_url") or ""),
    }


def status(cfg, params=None):
    browser = _browser_values(cfg, params)
    result = browser_automation.status(
        cfg,
        cdp_url=browser["cdp_url"],
        profile_dir=browser["profile_dir"],
        start_url=browser["start_url"],
    )
    result["start_url"] = browser["start_url"]
    checkpoints = _load_checkpoints()
    result["active_lists"] = len(
        [cp for cp in checkpoints.values() if isinstance(cp, dict) and cp.get("active")]
    )
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


def run(job, params: dict) -> dict:
    cfg = _config.load()
    opts = browser_automation.settings(cfg)
    raw_instance = params.get("instance")
    instance = raw_instance if isinstance(raw_instance, dict) else {}
    instance_id = str(params.get("instance_id") or instance.get("id") or "default")
    # 连接前先拒绝非本机地址，避免把调试端口暴露到内网或误连他人浏览器
    cdp_url, _ = browser_automation.validate_cdp_url(
        str(instance.get("cdp_url") or opts["cdp_url"])
    )
    scan_only = bool(params.get("scan_only", False))
    timeout = int(params.get("timeout", opts["step_timeout"]))
    max_attempts = int(
        instance.get(
            "max_attempts",
            params.get(
                "max_attempts",
                (cfg.get("course_autoplay") or {}).get("max_attempts", 3),
            ),
        )
    )
    if timeout <= 0 or max_attempts <= 0:
        raise RuntimeError("单步超时和单课尝试次数必须大于 0")
    list_key = ""
    checkpoint = None
    paused = False
    persist_warned = False

    def save_checkpoint():
        nonlocal persist_warned
        message = _persist(list_key, checkpoint)
        if message and not persist_warned:
            persist_warned = True
            job.log(
                "断点保存失败（%s），学习继续；本次恢复可能不如预期精确" % message
            )

    def on_progress(current, total, label):
        if checkpoint is not None and list_key:
            save_checkpoint()
        job.set_progress(current, total, label)

    args = SimpleNamespace(
        cdp_url=cdp_url,
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
        nonlocal paused
        paused = True
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
            return _summary(summary, {}, "")

        list_key = "%s::%s" % (
            instance_id,
            engine.course_list_key(runner.list_page.url),
        )
        previous = _load_checkpoints().get(list_key)
        resumed = isinstance(previous, dict) and previous.get("active")
        checkpoint = _clean_checkpoint(previous if resumed else None)
        checkpoint["active"] = True
        checkpoint["last_error"] = ""
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
        save_checkpoint()
        summary = runner.run(first_page, checkpoint=checkpoint)
        result = _summary(summary, checkpoint, list_key)
        if result["total"] <= 0:
            raise RuntimeError(
                "课程列表为空、没有可学习的网页课程，无法判定学习完成，"
                "请确认已进入「课程设置」页"
            )
        if result["studiable"] <= 0:
            raise RuntimeError(
                "课程列表共 %d 门，但没有可学习的网页课程，不能判定为已完成"
                % result["total"]
            )
        checkpoint["last_result"] = result
        return result
    except _jobs.Cancelled:
        return paused_result()
    except Exception as exc:
        if job.cancelled:
            return paused_result()
        if checkpoint is not None:
            checkpoint["last_error"] = str(exc)[:300]
        raise
    finally:
        if checkpoint is not None and list_key:
            # 只有暂停才保留 active，正常结束或异常退出都必须允许下次重新开始
            checkpoint["active"] = paused
            save_checkpoint()
        runner.close()
