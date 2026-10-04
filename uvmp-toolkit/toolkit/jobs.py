# -*- coding: utf-8 -*-
"""后台任务模型：所有耗时操作（导出/渲染）统一走 Job。

- Job 在线程中执行，前端按 1s 轮询进度与增量日志
- 协作式取消：工作单元之间调用 job.check_cancel()；长阻塞操作（CDP 渲染等）由业务
  用 job.add_cancel_hook() 注册"取消即销毁阻塞源"的钩子，实现即刻中断
- 结束后写任务档案（state/jobs/<id>.json）并追加应用历史（state/history/<app_id>.jsonl）
新应用接入时直接用 JobManager 包裹业务函数即可获得进度/日志/取消/历史能力。
"""
import threading
import time
import traceback
from collections import deque
from pathlib import Path

import state


class Cancelled(Exception):
    """job.check_cancel() 抛出，表示用户请求取消"""


class Job:
    def __init__(self, job_id: str, app_id: str, title: str):
        self.id = job_id
        self.app_id = app_id
        self.title = title
        self.status = "pending"           # pending/running/success/failed/cancelled
        self.progress = {"current": 0, "total": 0, "label": ""}
        self.created_at = time.strftime("%Y-%m-%d %H:%M:%S")
        self.finished_at = ""
        self.result = {}                  # 业务自定义摘要（产出文件、数量等）
        self.error = ""
        self.echo = False                 # CLI 模式置 True，日志同步打印到 stdout
        self.on_log = None                # GUI/RPC 桥接回调：fn(line)
        self.on_progress = None           # GUI/RPC 桥接回调：fn(current, total, label)
        self.on_done = None               # 终态回调：fn(job)，在状态落定后由 JobManager 调用
        self._logs = deque(maxlen=1000)   # (seq, line)
        self._seq = 0
        self._lock = threading.Lock()
        self._cancel = threading.Event()
        self._cancel_hooks = []           # 取消时立即调用的钩子（见 add_cancel_hook）
        self._log_fp = open(state.logs_dir() / (job_id + ".log"), "a", encoding="utf-8")

    # ---- 业务侧接口 ----
    def log(self, msg: str):
        line = "[%s] %s" % (time.strftime("%H:%M:%S"), msg)
        with self._lock:
            self._seq += 1
            self._logs.append((self._seq, line))
            try:
                self._log_fp.write(line + "\n")
                self._log_fp.flush()
            except Exception:
                pass
        if self.echo:
            print(line, flush=True)
        cb = self.on_log
        if cb:
            try:
                cb(line)
            except Exception:
                pass

    def set_progress(self, current: int, total: int, label: str = ""):
        with self._lock:
            self.progress = {"current": current, "total": total, "label": label}
        cb = self.on_progress
        if cb:
            try:
                cb(current, total, label)
            except Exception:
                pass

    @property
    def cancelled(self) -> bool:
        return self._cancel.is_set()

    def cancel(self):
        self._cancel.set()
        for fn in list(self._cancel_hooks):
            try:
                fn()
            except Exception:
                pass

    def add_cancel_hook(self, fn):
        """注册"取消即触发"的钩子：协作式 check_cancel 要等当前工作单元结束，
        而长阻塞操作（CDP 渲染等待等）需要即时中断——钩子负责销毁阻塞源
        （如杀掉渲染浏览器进程，在途 CDP 调用立即报错退出）。业务在 run() 里注册。"""
        self._cancel_hooks.append(fn)
        if self._cancel.is_set():   # 注册前已取消：立即补触发
            try:
                fn()
            except Exception:
                pass

    def check_cancel(self):
        if self._cancel.is_set():
            raise Cancelled()

    # ---- 查询侧接口 ----
    def logs_since(self, offset: int):
        """返回 (offset 之后的 [[seq, line], ...], 当前最大 seq)"""
        with self._lock:
            lines = [[s, t] for (s, t) in self._logs if s > offset]
            return lines, self._seq

    def to_dict(self) -> dict:
        return {
            "id": self.id, "app_id": self.app_id, "title": self.title,
            "status": self.status, "progress": dict(self.progress),
            "created_at": self.created_at, "finished_at": self.finished_at,
            "result": self.result, "error": self.error,
        }


class JobManager:
    def __init__(self):
        self._jobs = {}
        self._seq = 0
        self._lock = threading.Lock()

    def create(self, app_id: str, title: str) -> Job:
        with self._lock:
            self._seq += 1
            job_id = "%s-%02d" % (time.strftime("%Y%m%d%H%M%S"), self._seq % 100)
            job = Job(job_id, app_id, title)
            self._jobs[job_id] = job
            return job

    def get(self, job_id: str):
        return self._jobs.get(job_id)

    def list(self, app_id: str = "", limit: int = 20) -> list:
        jobs = sorted(self._jobs.values(), key=lambda j: j.id, reverse=True)
        if app_id:
            jobs = [j for j in jobs if j.app_id == app_id]
        return [j.to_dict() for j in jobs[:limit]]

    def _finalize(self, job: Job):
        job.finished_at = time.strftime("%Y-%m-%d %H:%M:%S")
        try:
            job._log_fp.close()
        except Exception:
            pass
        summary = job.to_dict()
        tail, _ = job.logs_since(max(0, job._seq - 50))
        summary["log_tail"] = [t for _, t in tail]
        state.write_json(state.jobs_dir() / (job.id + ".json"), summary)
        state.append_history(job.app_id, {
            "job_id": job.id, "title": job.title, "status": job.status,
            "time": job.finished_at, "result": job.result, "error": job.error,
        })

    def _runner(self, job: Job, fn):
        job.status = "running"
        try:
            result = fn(job)
            job.result = result or {}
            job.status = "cancelled" if job.cancelled else "success"
        except Cancelled:
            job.status = "cancelled"
            job.log("任务已取消")
        except Exception as e:  # noqa: BLE001
            job.status = "failed"
            job.error = str(e)
            job.log("任务失败: %s" % e)
            job.log(traceback.format_exc(limit=6))
        finally:
            self._finalize(job)
            cb = job.on_done
            if cb:
                try:
                    cb(job)
                except Exception:
                    pass

    def start(self, job: Job, fn):
        """后台线程执行（GUI 模式）"""
        t = threading.Thread(target=self._runner, args=(job, fn), daemon=True,
                             name="job-" + job.id)
        t.start()
        return job

    def run_sync(self, job: Job, fn, echo: bool = True):
        """当前线程执行（CLI/定时器模式），日志同步到 stdout"""
        job.echo = echo
        self._runner(job, fn)
        return job
