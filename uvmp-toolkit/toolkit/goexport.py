# -*- coding: utf-8 -*-
"""Go 导出工作进程适配（uvmp-export run / uvmp-export selftest）：轨迹导出并行引擎的进程协议桥。

进程协议（固定）：
  启动  uvmp-export run
  stdin 首行请求  {"config":<config.load dict>,"params":<pcd_export 参数>,
                   "codes":[{seq,code,date_hint}]}
        之后可收  {"command":"cancel"}
  stdout 仅 NDJSON 事件：
        {"event":"log","line":...}
        {"event":"progress","current":n,"total":n,"label":...}
        成功 {"event":"done","result":{...}}（原 backend.run 返回形状）
        失败 {"event":"error","error":"..."}

内置 CDP 浏览器自检走同一协议（uvmp-export selftest，见 run_selftest）：
  stdin 首行请求  {"config":<config.load dict>}，成功 done.result = {ok,png,pdf,...}。

密码只经 stdin 请求体传给工作进程，不进命令行；job 日志/进度标签/stderr 环形记录
统一脱敏（命中密码替换为 ******）。uvmp-export 缺失或执行失败一律直接报错，
不静默回退 Python 路径（对照路径见 backend.UVMP_EXPORT_ENGINE=python）。
"""

import json
import os
import signal
import subprocess
import sys
import threading
import time
from collections import deque
from pathlib import Path

from jobs import Cancelled

TOOLKIT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TOOLKIT_DIR.parent

CANCEL_GRACE = 8.0  # 取消指令发出后等进程自退的宽限期，超时强制终止
STDERR_RING = 50  # stderr 脱敏环形记录条数（失败排障用）
_SECRET_MASK = "******"


def _is_windows() -> bool:
    return sys.platform.startswith("win")


def _kill_tree(proc) -> None:
    """强杀 uvmp-export 进程树（防 Electron/Chromium 孤儿占调试端口）：
    Windows 走 taskkill /PID <pid> /T /F；POSIX 杀整个进程组
    （Popen 已 start_new_session，pgid == 子进程 pid）。"""
    if proc.poll() is not None:
        return
    if _is_windows():
        try:
            subprocess.run(
                ["taskkill", "/PID", str(proc.pid), "/T", "/F"],
                capture_output=True,
                timeout=10,
            )
        except Exception:  # noqa: BLE001 taskkill 缺失/超时退化为单杀
            try:
                proc.kill()
            except Exception:  # noqa: BLE001
                pass
        return
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except Exception:  # noqa: BLE001 进程组已散则退化为单杀
        try:
            proc.kill()
        except Exception:  # noqa: BLE001
            pass


def _terminate_tree(proc) -> None:
    """温和终止后强杀进程树：优雅取消（stdin cancel 指令）优先，这里是其后的兜底。"""
    try:
        proc.terminate()
    except Exception:  # noqa: BLE001
        pass
    deadline = time.time() + 3
    while time.time() < deadline and proc.poll() is None:
        time.sleep(0.2)
    if proc.poll() is None:
        _kill_tree(proc)


def _exe_names() -> tuple:
    return ("uvmp-export.exe", "uvmp-export") if _is_windows() else ("uvmp-export",)


def find_binary() -> str:
    """定位 uvmp-export 工作进程，返回可执行文件路径；找不到抛 RuntimeError。

    优先级：环境变量 UVMP_EXPORT_BIN（显式指定）→ 冻结版与 uvmp-core 同目录 →
    源码树 core-go 构建产物（core-go/、core-go/bin/、项目根）。"""
    env = (os.environ.get("UVMP_EXPORT_BIN") or "").strip()
    if env:
        if Path(env).exists():
            return env
        raise RuntimeError("UVMP_EXPORT_BIN 指定的导出工作进程不存在: %s" % env)
    names = _exe_names()
    if getattr(sys, "frozen", False):
        dirs = [Path(sys.executable).resolve().parent]
    else:
        dirs = [
            PROJECT_DIR / "core-go",
            PROJECT_DIR / "core-go" / "bin",
            PROJECT_DIR,
        ]
    for d in dirs:
        for n in names:
            p = d / n
            if p.is_file():
                return str(p)
    raise RuntimeError(
        "未找到 Go 导出工作进程 uvmp-export（已查：%s）；"
        "请安装完整版客户端或设置环境变量 UVMP_EXPORT_BIN 指向该程序"
        % "、".join(str(d) for d in dirs)
    )


def _electron_names() -> tuple:
    return ("electron.exe", "electron") if _is_windows() else ("electron",)


def derive_electron() -> str:
    """随包 Electron 本体推导（写入工作进程 UVMP_BROWSER_BIN），推不出返回空串
    （调用方不得回退宿主浏览器）。

    显式 UVMP_ELECTRON（Electron 壳 spawn 核心时注入，冻结版即应用本体）→
    源码树 desktop/node_modules/electron/dist 运行时。"""
    bin_path = (os.environ.get("UVMP_ELECTRON") or "").strip()
    if not (bin_path and Path(bin_path).exists()):
        bin_path = ""
        if not getattr(sys, "frozen", False):
            dist = PROJECT_DIR / "desktop" / "node_modules" / "electron" / "dist"
            for name in _electron_names():
                p = dist / name
                if p.is_file():
                    bin_path = str(p)
                    break
    return bin_path


# CDP 渲染 Worker 专用参数（main.cjs 识别后 require cdp-worker.cjs）：打包版 Electron
# 忽略直接 JS 路径参数，故不用脚本路径、统一走该参数（冻结版与源码版同款）。
CDP_WORKER_FLAG = "--uvmp-cdp-worker"


def derive_browser_env() -> dict:
    """推导要注入工作进程的浏览器环境变量（显式 UVMP_BROWSER_BIN/WORKER/APP 优先）。

    随包 Electron 可推导 → 写 UVMP_BROWSER_BIN=本体 + UVMP_BROWSER_WORKER=
    --uvmp-cdp-worker（main.cjs 的 CDP Worker 专用入口参数）；源码 Electron 另设
    UVMP_BROWSER_APP=uvmp-toolkit/desktop（源码 Electron 需应用目录才会加载
    main.cjs），冻结版本体已绑定 app.asar、APP 为空。都推不出就不注入，由工作进程
    按 render 配置明确失败——内置逻辑不强制宿主浏览器回退。"""
    bin_explicit = (os.environ.get("UVMP_BROWSER_BIN") or "").strip()
    worker_explicit = (os.environ.get("UVMP_BROWSER_WORKER") or "").strip()
    app_explicit = (os.environ.get("UVMP_BROWSER_APP") or "").strip()
    elec = derive_electron()
    out = {}
    use_electron_worker = False
    if bin_explicit:
        out["UVMP_BROWSER_BIN"] = bin_explicit  # 显式 BIN 不再注入派生 worker
    elif elec:
        out["UVMP_BROWSER_BIN"] = elec
        use_electron_worker = True
    if worker_explicit:
        out["UVMP_BROWSER_WORKER"] = worker_explicit
    elif use_electron_worker:
        out["UVMP_BROWSER_WORKER"] = CDP_WORKER_FLAG
    worker = out.get("UVMP_BROWSER_WORKER", "")
    if app_explicit:
        out["UVMP_BROWSER_APP"] = app_explicit
    elif worker == CDP_WORKER_FLAG and not getattr(sys, "frozen", False):
        # APP 只配 flag 入口（走 main.cjs worker 分支）；显式 worker 为脚本路径的
        # 直接脚本方式不加应用目录（加了会被当成应用路径，worker 脚本失效）
        out["UVMP_BROWSER_APP"] = str(PROJECT_DIR / "desktop")
    return out


def _secrets(cfg: dict) -> list:
    """配置中可能出现的密码明文（config.json sso 与 config.ini [auth]），供日志脱敏"""
    out = []
    for pw in (
        (cfg.get("sso") or {}).get("password") or "",
        ((cfg.get("_ini") or {}).get("auth") or {}).get("password") or "",
    ):
        if pw and pw not in out:
            out.append(pw)
    return out


def _scrub(text: str, secrets: list) -> str:
    for pw in secrets:
        if pw:
            text = text.replace(pw, _SECRET_MASK)
    return text


def run(cfg: dict, params: dict, codes: list, job) -> dict:
    """按协议拉起 uvmp-export run，把 NDJSON 事件桥接到 job。

    cfg 为 config.load() 全量配置（含 SSO 凭据，仅经 stdin 传递）；params 为
    pcd_export 参数原样透传；codes 为已解析清单 [{seq,code,date_hint}]。
    成功返回 done.result（与 Python 路径 backend.run 返回形状一致）；
    失败抛 RuntimeError，用户取消抛 jobs.Cancelled。"""
    binary = find_binary()
    secrets = _secrets(cfg)
    request = {
        "config": cfg,
        "params": params,
        "codes": [
            {
                "seq": c.get("seq"),
                "code": c.get("code"),
                "date_hint": c.get("date_hint") or "",
            }
            for c in codes
        ],
    }
    env = dict(os.environ)
    env.update(derive_browser_env())  # 显式 UVMP_BROWSER_BIN/WORKER/APP 已优先，不覆盖
    return _run_protocol(
        binary,
        ["run"],
        request,
        secrets,
        job,
        env,
        "Go 导出工作进程：%s（并行导出）" % binary,
    )


def run_selftest(cfg: dict, job, out_dir) -> dict:
    """按协议拉起 uvmp-export selftest（内置 CDP 浏览器自检）：只打开 data: 本地页，
    验证导航 / eval / 新 tab / 截图 PNG / printToPDF，真实产物写 out_dir
    （经 UVMP_SELFTEST_DIR 传给工作进程，Python 侧传 state.logs_dir()）。
    成功返回 done.result {ok,png,pdf,...}；无浏览器/失败抛 RuntimeError，
    用户取消抛 jobs.Cancelled。"""
    binary = find_binary()
    secrets = _secrets(cfg)
    env = dict(os.environ)
    env.update(derive_browser_env())
    env["UVMP_SELFTEST_DIR"] = str(out_dir)  # 真实产物目录（Go 侧写 PNG/PDF）
    return _run_protocol(
        binary,
        ["selftest"],
        {"config": cfg},
        secrets,
        job,
        env,
        "内置 CDP 自检工作进程：%s" % binary,
    )


def _run_protocol(
    binary: str, argv: list, request: dict, secrets: list, job, env: dict, title: str
) -> dict:
    """run / run_selftest 共用的进程协议：Popen 拉起工作进程 → stdin 首行请求 →
    stdout NDJSON 事件（log/progress/done/error）桥接到 job → 成功返回 done.result。
    取消（stdin cancel 指令 + 宽限期后整树强杀）、日志脱敏、进程树收尾语义不变。"""

    def log(msg):
        job.log(_scrub(str(msg), secrets))

    job.check_cancel()
    log(title)
    try:
        proc = subprocess.Popen(
            [binary] + argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            env=env,
            creationflags=subprocess.CREATE_NO_WINDOW if _is_windows() else 0,
            # POSIX 独立进程组（pgid == pid）：取消兜底可 killpg 整树杀掉 Electron 孤儿；
            # Windows 走 taskkill /T 按父子关系清树。
            start_new_session=not _is_windows(),
        )
    except OSError as e:
        raise RuntimeError("Go 导出工作进程启动失败: %s" % e)

    stderr_ring = deque(maxlen=STDERR_RING)

    def _read_stderr():
        try:
            for line in proc.stderr:
                line = line.rstrip("\r\n")
                if line:
                    stderr_ring.append(_scrub(line, secrets))
        except Exception:  # noqa: BLE001 进程收尾时管道关闭，忽略
            pass

    threading.Thread(target=_read_stderr, daemon=True, name="goexport-stderr").start()

    cancel_sent = threading.Event()

    def _cancel():
        """取消钩子：发 {"command":"cancel"}，宽限期后进程仍未退出即强制终止"""
        if cancel_sent.is_set():
            return
        cancel_sent.set()
        try:
            if proc.stdin and not proc.stdin.closed:
                proc.stdin.write(
                    json.dumps({"command": "cancel"}, ensure_ascii=True) + "\n"
                )
                proc.stdin.flush()
        except Exception:  # noqa: BLE001 管道已断则直接走终止
            pass

        def _terminate_later():
            deadline = time.time() + CANCEL_GRACE
            while time.time() < deadline and proc.poll() is None:
                time.sleep(0.2)
            if proc.poll() is not None:
                return
            _terminate_tree(proc)  # 温和终止超时 → 整树强杀（防 Electron 孤儿）

        threading.Thread(
            target=_terminate_later, daemon=True, name="goexport-cancel"
        ).start()

    job.add_cancel_hook(_cancel)

    result = None
    error = None
    try:
        try:
            # 首行请求：配置含密码，只经 stdin 传递，不进命令行、不写日志
            proc.stdin.write(json.dumps(request, ensure_ascii=True) + "\n")
            proc.stdin.flush()
        except (OSError, ValueError):
            pass  # 进程已退出：走下方"异常退出"分支统一报错
        for line in proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue  # 协议规定 stdout 仅 NDJSON 事件，杂音忽略
            et = ev.get("event")
            if et == "log":
                log(ev.get("line") or "")
            elif et == "progress":
                job.set_progress(
                    int(ev.get("current") or 0),
                    int(ev.get("total") or 0),
                    _scrub(str(ev.get("label") or ""), secrets),
                )
            elif et == "done":
                result = ev.get("result") or {}
                break
            elif et == "error":
                error = _scrub(str(ev.get("error") or "导出失败"), secrets)
                break
        if result is not None:
            return result
        if job.cancelled or cancel_sent.is_set():
            raise Cancelled()
        if error is not None:
            raise RuntimeError(error)
        try:  # stdout 已 EOF 但进程退出码可能尚未收割，稍候再报
            proc.wait(timeout=5)
        except Exception:  # noqa: BLE001
            pass
        tail = "；".join(list(stderr_ring)[-5:])
        raise RuntimeError(
            "Go 导出工作进程异常退出（exit=%s）%s"
            % (proc.poll(), ("：stderr " + tail) if tail else "")
        )
    finally:
        try:
            if proc.stdin and not proc.stdin.closed:
                proc.stdin.close()
        except Exception:  # noqa: BLE001
            pass
        if proc.poll() is None:
            _terminate_tree(proc)  # 收尾兜底同样整树杀，防 Electron 孤儿
