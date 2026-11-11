# -*- coding: utf-8 -*-
"""stdio JSON-RPC 服务：Electron 壳经 spawn 管道调用 Python 核心。

协议（每行一个 JSON）：
  请求  {"id": 1, "method": "getConfig", "params": {...}}
  响应  {"id": 1, "ok": true, "result": ...} / {"id": 1, "ok": false, "error": "..."}
  事件（无 id，服务端主动推）{"event": "job.log"|"job.progress"|"job.done", "job_id": "...", ...}

为什么是 stdio 而不是 HTTP：免端口/令牌管理，父子进程天然 1:1 生命周期，
Electron 主进程 spawn 即用；核心仍是纯标准库（PyInstaller 好打包）。

stdout 是协议独占通道：服务启动时保存真实 stdout 供 _send 使用，并把 sys.stdout
重定向到 stderr——业务/第三方库的 print 只会进 stderr，不会夹杂在协议行里让对端
JSON.parse 失败。
"""

import json
import os
import re
import sys
import threading
import time
from pathlib import Path

import config as _config
import jobs as _jobs
import platform_utils
import state

# getJobLog 单次返回上限（字符）：日志动辄几十 MB，整份回传会把 RPC 管道塞住
JOB_LOG_MAX_CHARS = 200000
# 任务档案白名单的默认窗口（天）：与 state.prune_old_logs(30) 的保留期一致。
# 不能用「最近 N 份」这种数量上限——30 天内跑得多的用户，靠后的旧记录会打不开。
ARCHIVE_WINDOW_DAYS = 30


class RpcServer:
    def __init__(self):
        self.manager = _jobs.JobManager()
        self._write_lock = threading.Lock()
        self._backends = None
        # 协议通道独占 stdout：构造时留住真实 stdout，serve() 启动时再把 sys.stdout
        # 指向 stderr（业务代码/第三方库的 print 只污染 stderr，不破坏 JSON 行）
        self._out = sys.stdout

    def _claim_stdout(self):
        if sys.stderr is not None and sys.stdout is not sys.stderr:
            sys.stdout = sys.stderr

    # ---------------- 底层 ----------------
    def _send(self, obj: dict):
        # ensure_ascii=True：CJK 转 \uXXXX，跨控制台编码 100% 安全（对端 JSON.parse 还原）
        with self._write_lock:
            self._out.write(json.dumps(obj, ensure_ascii=True) + "\n")
            self._out.flush()

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

    def m_getSystemInfo(self, params):
        info = platform_utils.platform_summary()
        try:
            info["chrome"] = platform_utils.detect_chrome(
                _config.render_cfg(_config.load()).get("chrome", "")
            )
        except Exception:
            info["chrome"] = ""
        info["state_dir"] = str(state.state_dir())
        info["config_path"] = str(_config.config_path())
        # 目录建议会探测 U 盘/桌面，按需返回；设置页首次加载不再被 U 盘检测拖慢。
        if (params or {}).get("with_outdirs"):
            info["suggested_outdirs"] = platform_utils.suggested_outdirs(_config.load())
        return info

    def m_getDevScriptEnv(self, _params):
        """脚本测试页：显示实际运行脚本的解释器/打包状态，避免与客户端环境不一致。"""
        import sys

        return {
            "python": sys.executable,
            "version": sys.version.split()[0],
            "frozen": bool(getattr(sys, "frozen", False)),
            "config_path": str(_config.config_path()),
            "state_dir": str(state.state_dir()),
            "encoding": sys.getdefaultencoding(),
        }

    def m_getConfig(self, _params):
        return _config.masked_view(_config.load())

    def m_saveConfig(self, params):
        # 密码留空/掩码 = 不修改（sso 与其他应用的口径统一在 config.save 里处理）
        updates = dict(params or {})
        try:
            saved = _config.save(updates)
        except _config.ConfigValidationError as e:
            raise RuntimeError(str(e))
        cfg = _config.deep_merge(_config.load(), saved)
        return _config.masked_view(cfg)

    def m_getApps(self, _params):
        return [
            {
                "id": a["id"],
                "name": a.get("name"),
                "badge": a.get("badge"),
                "desc": a.get("desc"),
                "order": a.get("order", 99),
                "has_status": hasattr(a.get("_backend"), "status"),
                "has_preview": hasattr(a.get("_backend"), "preview"),
                "has_browser": hasattr(a.get("_backend"), "launch_browser"),
            }
            for a in self._apps().values()
        ]

    def m_getAppStatus(self, params):
        app = self._apps().get((params or {}).get("app_id", ""))
        backend = (app or {}).get("_backend")
        if not backend or not hasattr(backend, "status"):
            return {}
        cfg = _config.load()
        try:
            return backend.status(cfg, params or {})
        except TypeError:
            return backend.status(cfg)

    def m_getUsbAgentStatus(self, _params):
        """U 盘后台监听状态：是否在跑 + 最近心跳（无 U 盘时也有心跳，便于区分
        “服务没在跑”与“服务在跑但 U 盘未挂载/不可读”）。"""
        import usb_agent

        return usb_agent.agent_status()

    def m_getClusterStatus(self, _params):
        """集群节点状态：本机身份、服务状态、待审批/已入网节点、Worker 连接。

        集群服务在 usb-agent 常驻进程内；RPC 子进程经本机管理接口读取，
        读不到时回退到 state/cluster 下的状态文件。
        """
        import cluster.service as cluster_service

        return cluster_service.local_status(_config.load())

    def m_getClusterNodes(self, _params):
        data = self.m_getClusterStatus({})
        return {
            "nodes": data.get("nodes") or [],
            "pending": data.get("pending") or [],
        }

    def _cluster_approval(self, action: str, params: dict):
        node_id = str((params or {}).get("node_id") or "").strip()
        if not re.match(r"^[A-Za-z0-9._:-]{1,64}$", node_id):
            raise RuntimeError("节点 ID 非法")
        import cluster.service as cluster_service

        return cluster_service.local_admin(action, node_id)

    def m_approveClusterNode(self, params):
        return self._cluster_approval("approve", params)

    def m_rejectClusterNode(self, params):
        return self._cluster_approval("reject", params)

    def m_clusterStartExport(self, params):
        payload = (params or {}).get("params") or {}
        if not isinstance(payload, dict):
            raise RuntimeError("导出参数非法")
        import cluster.service as cluster_service

        return cluster_service.local_export("start", payload)

    def m_clusterGetExport(self, params):
        job_id = str((params or {}).get("job_id") or "").strip()
        if not re.match(r"^[A-Za-z0-9._:-]{1,64}$", job_id):
            raise RuntimeError("job_id 非法")
        import cluster.service as cluster_service

        return cluster_service.local_export("status", {"job_id": job_id})

    def m_clusterCancelExport(self, params):
        job_id = str((params or {}).get("job_id") or "").strip()
        if not re.match(r"^[A-Za-z0-9._:-]{1,64}$", job_id):
            raise RuntimeError("job_id 非法")
        import cluster.service as cluster_service

        return cluster_service.local_export("cancel", {"job_id": job_id})

    @staticmethod
    def _update_payload(params):
        if isinstance(params, dict) and isinstance(params.get("params"), dict):
            return params["params"]
        return params if isinstance(params, dict) else {}

    def m_clusterListUpdatePackages(self, _params):
        import cluster.service as cluster_service

        return cluster_service.local_update("packages")

    def m_clusterAddUpdatePackage(self, params):
        import cluster.service as cluster_service

        return cluster_service.local_update(
            "add_package", self._update_payload(params)
        )

    def m_clusterRemoveUpdatePackage(self, params):
        import cluster.service as cluster_service

        return cluster_service.local_update(
            "remove_package", self._update_payload(params)
        )

    def m_clusterPushUpdate(self, params):
        import cluster.service as cluster_service

        return cluster_service.local_update("push", self._update_payload(params))

    def m_clusterGetUpdate(self, params):
        job_id = str(self._update_payload(params).get("job_id") or "").strip()
        if not re.match(r"^[A-Za-z0-9._:-]{1,64}$", job_id):
            raise RuntimeError("job_id 非法")
        import cluster.service as cluster_service

        return cluster_service.local_update("status", {"job_id": job_id})

    def m_clusterCancelUpdate(self, params):
        job_id = str(self._update_payload(params).get("job_id") or "").strip()
        if not re.match(r"^[A-Za-z0-9._:-]{1,64}$", job_id):
            raise RuntimeError("job_id 非法")
        import cluster.service as cluster_service

        return cluster_service.local_update("cancel", {"job_id": job_id})

    def m_clusterGetPendingUpdate(self, _params):
        import cluster.service as cluster_service

        return cluster_service.local_update("pending")

    def m_clusterOpenPendingUpdate(self, _params):
        import cluster.service as cluster_service

        return cluster_service.local_update("open")

    def m_launchAppBrowser(self, params):
        app = self._apps().get((params or {}).get("app_id", ""))
        backend = (app or {}).get("_backend")
        if not backend or not hasattr(backend, "launch_browser"):
            raise RuntimeError("该应用不支持受控浏览器")
        cfg = _config.load()
        try:
            return backend.launch_browser(cfg, params or {})
        except TypeError:
            return backend.launch_browser(cfg)

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
        job_params = (params or {}).get("params") or {}
        instance_id = str(job_params.get("instance_id") or "")
        if self.manager.active(app_id, instance_id):
            raise RuntimeError("该实例已有任务在运行")
        title = app.get("name", app_id)
        instance_name = str(job_params.get("instance_name") or "").strip()
        if instance_name:
            title += " · " + instance_name
        job = self.manager.create(app_id, title)
        job.instance_id = instance_id
        job.keep_awake = bool(job_params.get("keep_awake"))
        job.on_log = lambda line, jid=job.id: self._emit(
            "job.log", job_id=jid, line=line
        )
        job.on_progress = lambda c, t, l, jid=job.id: self._emit(
            "job.progress", job_id=jid, current=c, total=t, label=l
        )
        job.on_done = lambda jb: self._emit("job.done", job_id=jb.id, job=jb.to_dict())
        self.manager.start(job, lambda jb: backend.run(jb, job_params))
        return {"job_id": job.id}

    def _run_selftest(self, kind: str, params: dict):
        """设置页环境自检（渲染 / SSO 登录），复用 jobs 事件模型"""
        import selftest as _st

        fn = {"render": _st.run_render, "sso": _st.run_sso, "cdp": _st.run_cdp}.get(
            kind
        )
        if not fn:
            raise RuntimeError("未知自检类型: " + kind)
        job = self.manager.create("selftest", "环境自检·" + kind)
        job.on_log = lambda line, jid=job.id: self._emit(
            "job.log", job_id=jid, line=line
        )
        job.on_progress = lambda c, t, l, jid=job.id: self._emit(
            "job.progress", job_id=jid, current=c, total=t, label=l
        )
        job.on_done = lambda jb: self._emit("job.done", job_id=jb.id, job=jb.to_dict())
        self.manager.start(job, lambda jb: fn(jb, params.get("params") or {}))
        return {"job_id": job.id}

    def m_cancelJob(self, params):
        job = self.manager.get((params or {}).get("job_id", ""))
        if not job:
            raise RuntimeError("任务不存在")
        job.cancel()
        return {"ok": True}

    def m_getJob(self, params):
        job = self.manager.get((params or {}).get("job_id", ""))
        return job.to_dict() if job else None

    def m_getActiveJob(self, params):
        params = params or {}
        job = self.manager.active(
            str(params.get("app_id") or ""),
            str(params.get("instance_id") or ""),
        )
        return job.to_dict() if job else None

    def m_getHistory(self, params):
        app_id = str((params or {}).get("app_id", "") or "")
        if not state.is_app_id(app_id):
            return []  # 非法 app_id 当“没有历史”，不把内部校验错误抛给界面
        return state.recent_runs(app_id, 10)

    @staticmethod
    def _archive_files(job_id: str = ""):
        """待检查的任务档案（生成器，按 mtime 从新到旧）。

        - 带 job_id：只产出这一份档案，按任务精确定位（不受保留窗口限制）
        - 不带 job_id：产出 ARCHIVE_WINDOW_DAYS 天内修改过的档案，窗口与 30 天保留期
          一致；不再用「最近 200 份」这种数量上限——排序靠后的旧记录同样要能打开
        """
        if job_id and not state.is_job_id(job_id):
            job_id = ""  # 非法任务号按“未指定”处理，不参与档案读取
        try:
            if job_id:
                yield state.jobs_dir() / (job_id + ".json")
                return
            cutoff = time.time() - ARCHIVE_WINDOW_DAYS * 86400
            stamped = []
            for p in state.jobs_dir().glob("*.json"):
                try:
                    mtime = p.stat().st_mtime
                except OSError:
                    continue
                if mtime < cutoff:
                    continue
                stamped.append((mtime, p))
            stamped.sort(key=lambda item: item[0], reverse=True)
        except OSError:
            return
        for _mtime, path in stamped:
            yield path

    @staticmethod
    def _archive_output_dirs(path: Path) -> list:
        """单份任务档案里登记的输出目录（dir/outdir/output_dir）"""
        roots = []
        data = state.read_json(path, None)
        if not isinstance(data, dict):
            return roots
        result = data.get("result")
        if not isinstance(result, dict):
            return roots
        for key in ("dir", "outdir", "output_dir"):
            value = result.get(key)
            if isinstance(value, str) and value.strip():
                roots.append(value)
        return roots

    @staticmethod
    def _cluster_package_paths() -> list:
        """集群已登记的更新安装包（设置页“打开安装包”用）"""
        paths = []
        data = state.read_json(
            state.state_dir() / "cluster" / "update_packages.json", None
        )
        for item in (data or {}).get("packages") or []:
            if isinstance(item, dict) and item.get("path"):
                paths.append(str(item["path"]))
        return paths

    @staticmethod
    def _configured_open_roots() -> list:
        """配置里明确登记的业务目录（U 盘后备目录、同步表目录、输出目录、临时目录）：
        同步结果卡与导出结果卡上的“打开目录”按钮指向这些位置。"""
        try:
            cfg = _config.load()
        except Exception:  # noqa: BLE001
            return []
        roots = []
        usb = cfg.get("usb") or {}
        if usb.get("fallback_dir"):
            roots.append(str(usb["fallback_dir"]))
        for section in ("tree_sync", "crossing_sync"):
            local = str((cfg.get(section) or {}).get("local_path") or "")
            if local:
                roots.append(str(Path(local).expanduser().parent))
        daily = cfg.get("daily_export") or {}
        if daily.get("temp_dir"):
            roots.append(str(daily["temp_dir"]))
        order = cfg.get("order_export") or {}
        if order.get("default_outdir"):
            roots.append(str(order["default_outdir"]))
        review = cfg.get("defect_review") or {}
        if review.get("outdir"):
            roots.append(str(review["outdir"]))
        return roots

    @staticmethod
    def _is_removable_mount(real: Path) -> bool:
        """U 盘交付目录（卷标由用户在配置里指定）。

        Windows 的挂载点是盘符根目录（`X:\\`，路径里没有卷标），只放行盘符根本身——
        打开盘符在资源管理器里是无害的，而 `X:\\任意.exe` 仍会被拒绝；
        Linux/macOS 要求路径位于系统可移动介质根目录下且包含配置的卷标。
        卷标按大小写不敏感比较：用户配置常写 GLKVM，而 udisks 可能挂成 glkvm
        （vfat/exfat 卷标本身不区分大小写），严格比较会让整块盘打不开。"""
        if os.name == "nt":
            drive = str(real.drive or "")
            if not drive:
                return False
            return str(real).rstrip("\\/").upper() == drive.rstrip(":").upper() + ":"
        try:
            label = str((_config.load().get("usb") or {}).get("label") or "").strip()
        except Exception:  # noqa: BLE001
            return False
        if not label:
            return False
        wanted = label.casefold()
        if not any(part.casefold() == wanted for part in real.parts):
            return False
        media_roots = ("/media", "/run/media", "/mnt", "/Volumes")
        text = str(real)
        return any(text == r or text.startswith(r + "/") for r in media_roots)

    def _allowed_open_path(self, raw: str, job_id: str = "") -> Path:
        """openPath 只允许打开客户端自己的产物/业务目录。

        渲染进程一旦被注入，任意路径打开就等于把整机文件系统交出去（Windows 上
        os.startfile 还会直接执行 .exe）。白名单 = 状态目录 + 任务档案登记的输出目录
        + 配置里登记的业务目录 + 集群安装包 + 卷标匹配的 U 盘挂载点；realpath 解析后
        做前缀判断，符号链接也逃不出去。

        job_id 由前端从历史记录带过来：直接读该次任务的档案，30 天保留期之外也准确定位。"""
        if not raw:
            raise RuntimeError("路径不能为空")
        target = Path(str(raw)).expanduser()
        try:
            if not target.exists():
                raise RuntimeError("路径不存在")
            real = Path(os.path.realpath(str(target)))
        except OSError as e:
            raise RuntimeError("路径无法访问：%s" % e) from e
        packages = []
        for extra in self._cluster_package_paths():
            try:
                packages.append(Path(os.path.realpath(str(Path(extra).expanduser()))))
            except OSError:
                continue
        if real in packages:
            return target  # 安装包显式登记过：允许直接打开（交给系统安装器）
        if self._under(real, [state.state_dir()]):
            return target
        if self._under(real, self._configured_open_roots()):
            return target
        # 档案逐个懒读、命中即返回：窗口内档案再多也只在必要时才继续读下一个
        for archive in self._archive_files(job_id):
            if self._under(real, self._archive_output_dirs(archive)):
                return target
        if self._is_removable_mount(real):
            return target
        raise RuntimeError("只允许打开客户端产物目录、同步目录或日志目录")

    @staticmethod
    def _under(real: Path, roots) -> bool:
        """real 是否等于某个 root，或位于其下层（realpath 已解析，符号链接逃不出去）"""
        for root in roots:
            try:
                real_root = Path(os.path.realpath(str(Path(root).expanduser())))
            except OSError:
                continue
            if real == real_root or real_root in real.parents:
                return True
        return False

    def m_openPath(self, params):
        p = (params or {}).get("path", "")
        job_id = str((params or {}).get("job_id") or "").strip()
        target = self._allowed_open_path(p, job_id)
        return {"ok": platform_utils.open_path(str(target))}

    def m_getJobLog(self, params):
        """读任务完整日志（state/logs/<job_id>.log）；job_id 校验防路径穿越，
        超过 JOB_LOG_MAX_CHARS 只回尾部并置 truncated，避免把 RPC 管道塞满。"""
        job_id = (params or {}).get("job_id", "")
        if not state.is_job_id(job_id):
            raise RuntimeError("参数错误")
        p = state.logs_dir() / (job_id + ".log")
        if not p.exists():
            raise RuntimeError("日志不存在或已被清除")
        size = p.stat().st_size
        with open(p, "rb") as fp:
            if size > JOB_LOG_MAX_CHARS * 4:  # 先按字节粗裁，再按字符解码
                fp.seek(size - JOB_LOG_MAX_CHARS * 4)
            text = fp.read().decode("utf-8", errors="replace")
        truncated = size > JOB_LOG_MAX_CHARS or len(text) > JOB_LOG_MAX_CHARS
        if len(text) > JOB_LOG_MAX_CHARS:
            text = text[-JOB_LOG_MAX_CHARS:]
        return {"text": text, "truncated": truncated}

    def m_clearLogs(self, _params):
        """一键清除全部任务日志与任务档案（占用中的文件跳过并计数）"""
        return state.clear_logs()

    def m_deleteHistory(self, params):
        """删除应用执行记录：传 job_id 删单条，不传清空该应用（连带任务日志/档案）"""
        app_id = (params or {}).get("app_id", "")
        job_id = (params or {}).get("job_id", "") or None
        if job_id and not state.is_job_id(job_id):
            raise RuntimeError("参数错误")
        try:
            return state.delete_history_records(app_id, [job_id] if job_id else None)
        except ValueError as e:
            raise RuntimeError(str(e))

    def m_getLogStats(self, _params):
        """日志目录统计（logs/ + jobs/）：文件数与总字节"""
        files = 0
        total = 0
        for d in (state.logs_dir(), state.jobs_dir()):
            for f in d.iterdir():
                if f.is_file():
                    files += 1
                    total += f.stat().st_size
        return {"dir": str(state.logs_dir()), "files": files, "bytes": total}

    # ---------------- 主循环 ----------------
    def serve(self):
        self._claim_stdout()
        methods = {
            name[2:]: getattr(self, name) for name in dir(self) if name.startswith("m_")
        }
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
