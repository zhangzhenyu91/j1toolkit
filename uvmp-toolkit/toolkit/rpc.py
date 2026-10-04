# -*- coding: utf-8 -*-
"""stdio JSON-RPC 服务：Electron 壳经 spawn 管道调用 Python 核心。

协议（每行一个 JSON）：
  请求  {"id": 1, "method": "getConfig", "params": {...}}
  响应  {"id": 1, "ok": true, "result": ...} / {"id": 1, "ok": false, "error": "..."}
  事件（无 id，服务端主动推）{"event": "job.log"|"job.progress"|"job.done", "job_id": "...", ...}

为什么是 stdio 而不是 HTTP：免端口/令牌管理，父子进程天然 1:1 生命周期，
Electron 主进程 spawn 即用；核心仍是纯标准库（PyInstaller 好打包）。
"""
import json
import sys
import threading

import config as _config
import jobs as _jobs
import platform_utils
import scheduler
import state


class RpcServer:
    def __init__(self):
        self.manager = _jobs.JobManager()
        self._write_lock = threading.Lock()
        self._backends = None

    # ---------------- 底层 ----------------
    def _send(self, obj: dict):
        # ensure_ascii=True：CJK 转 \uXXXX，跨控制台编码 100% 安全（对端 JSON.parse 还原）
        with self._write_lock:
            sys.stdout.write(json.dumps(obj, ensure_ascii=True) + "\n")
            sys.stdout.flush()

    def _emit(self, event: str, **payload):
        obj = {"event": event}
        obj.update(payload)
        self._send(obj)

    def _apps(self):
        if self._backends is None:
            from apps import scan_apps
            self._backends = {a["id"]: a for a in scan_apps()}
        return self._backends

    # ---------------- 方法表 ----------------
    def m_ping(self, _params):
        return {"core": "uvmp-toolkit", "jobs": len(self.manager._jobs)}

    def m_getSystemInfo(self, _params):
        info = platform_utils.platform_summary()
        try:
            info["chrome"] = platform_utils.detect_chrome(
                _config.render_cfg(_config.load()).get("chrome", ""))
        except Exception:
            info["chrome"] = ""
        info["state_dir"] = str(state.state_dir())
        info["config_path"] = str(_config.config_path())
        info["suggested_outdirs"] = platform_utils.suggested_outdirs(_config.load())
        return info

    def m_getConfig(self, _params):
        return _config.masked_view(_config.load())

    def m_saveConfig(self, params):
        # 密码留空 = 不修改
        updates = dict(params or {})
        if "sso" in updates and not (updates["sso"] or {}).get("password"):
            updates["sso"] = {k: v for k, v in updates["sso"].items() if k != "password"}
        saved = _config.save(updates)
        cfg = _config.deep_merge(_config.load(), saved)
        return _config.masked_view(cfg)

    def m_getApps(self, _params):
        return [{"id": a["id"], "name": a.get("name"), "badge": a.get("badge"),
                 "desc": a.get("desc"), "order": a.get("order", 99),
                 "has_status": hasattr(a.get("_backend"), "status"),
                 "has_preview": hasattr(a.get("_backend"), "preview")}
                for a in self._apps().values()]

    def m_getAppStatus(self, params):
        app = self._apps().get((params or {}).get("app_id", ""))
        backend = (app or {}).get("_backend")
        if not backend or not hasattr(backend, "status"):
            return {}
        return backend.status(_config.load())

    def m_preview(self, params):
        app = self._apps().get((params or {}).get("app_id", ""))
        backend = (app or {}).get("_backend")
        if not backend or not hasattr(backend, "preview"):
            raise RuntimeError("该应用不支持预览")
        return backend.preview(params.get("params") or {})

    def m_runJob(self, params):
        app_id = (params or {}).get("app_id", "")
        if app_id.startswith("selftest:"):
            return self._run_selftest(app_id.split(":", 1)[1], params)
        app = self._apps().get(app_id)
        backend = (app or {}).get("_backend")
        if not backend:
            raise RuntimeError("应用不存在: " + app_id)
        if any(j.app_id == app_id and j.status in ("pending", "running")
               for j in self.manager._jobs.values()):
            raise RuntimeError("该应用已有任务在运行")
        job = self.manager.create(app_id, app.get("name", app_id))
        job.on_log = lambda line, jid=job.id: self._emit("job.log", job_id=jid, line=line)
        job.on_progress = lambda c, t, l, jid=job.id: self._emit(
            "job.progress", job_id=jid, current=c, total=t, label=l)
        job.on_done = lambda jb: self._emit("job.done", job_id=jb.id, job=jb.to_dict())
        self.manager.start(job, lambda jb: backend.run(jb, params.get("params") or {}))
        return {"job_id": job.id}

    def _run_selftest(self, kind: str, params: dict):
        """设置页环境自检（渲染 / SSO 登录），复用 jobs 事件模型"""
        import selftest as _st
        fn = {"render": _st.run_render, "sso": _st.run_sso}.get(kind)
        if not fn:
            raise RuntimeError("未知自检类型: " + kind)
        job = self.manager.create("selftest", "环境自检·" + kind)
        job.on_log = lambda line, jid=job.id: self._emit("job.log", job_id=jid, line=line)
        job.on_progress = lambda c, t, l, jid=job.id: self._emit(
            "job.progress", job_id=jid, current=c, total=t, label=l)
        job.on_done = lambda jb: self._emit("job.done", job_id=jb.id, job=jb.to_dict())
        self.manager.start(job, lambda jb: fn(jb, params.get("params") or {}))
        return {"job_id": job.id}

    def m_maybeRunDaily(self, _params):
        """客户端驻留调度入口：客户端每分钟轮询调用；到点且 schedule.driver=app 才启动。
        与 OS 定时器经 scheduler.due_check 的 driver 门闸互斥，不会双跑。"""
        cfg = _config.load()
        due, reason, _today = scheduler.due_check(cfg, driver="app")
        if not due:
            return {"started": False, "reason": reason}
        try:
            r = self.m_runJob({"app_id": "daily_sync", "params": {"date": ""}})
        except RuntimeError as e:
            return {"started": False, "reason": str(e)}
        return {"started": True, "reason": reason, "job_id": r.get("job_id")}

    def m_cancelJob(self, params):
        job = self.manager.get((params or {}).get("job_id", ""))
        if not job:
            raise RuntimeError("任务不存在")
        job.cancel()
        return {"ok": True}

    def m_getJob(self, params):
        job = self.manager.get((params or {}).get("job_id", ""))
        return job.to_dict() if job else None

    def m_getHistory(self, params):
        return state.recent_runs((params or {}).get("app_id", ""), 10)

    def m_openPath(self, params):
        p = (params or {}).get("path", "")
        from pathlib import Path
        if not p or not Path(p).exists():
            raise RuntimeError("路径不存在")
        return {"ok": platform_utils.open_path(p)}

    # ---------------- 主循环 ----------------
    def serve(self):
        methods = {name[2:]: getattr(self, name) for name in dir(self)
                   if name.startswith("m_")}
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue
            req_id = None
            try:
                req = json.loads(line)
                req_id = req.get("id")
                method = req.get("method", "")
                fn = methods.get(method)
                if not fn:
                    raise RuntimeError("未知方法: " + str(method))
                result = fn(req.get("params") or {})
                if req_id is not None:
                    self._send({"id": req_id, "ok": True, "result": result})
            except Exception as e:  # noqa: BLE001
                if req_id is not None:
                    self._send({"id": req_id, "ok": False, "error": str(e)})
        # stdin 关闭（如管道一次性喂请求）后，等运行中的任务线程把事件推完再退出
        for t in threading.enumerate():
            if t.name.startswith("job-") and t.is_alive():
                t.join()


def main():
    RpcServer().serve()
