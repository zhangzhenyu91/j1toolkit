# -*- coding: utf-8 -*-
"""Go 导出工作进程适配（uvmp-export run）：轨迹导出并行引擎的进程协议桥。

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

密码只经 stdin 请求体传给工作进程，不进命令行；job 日志/进度标签/stderr 环形记录
统一脱敏（命中密码替换为 ******）。uvmp-export 缺失或执行失败一律直接报错，
不静默回退 Python 路径（对照路径见 backend.UVMP_EXPORT_ENGINE=python）。
"""

import json
import os
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


def _browser_names() -> tuple:
    if _is_windows():
        return (
            "chrome.exe",
            "chromium.exe",
            "chrome-win64/chrome.exe",
            "chrome-win32/chrome.exe",
        )
    return (
        "chrome",
        "chromium",
        "chromium-browser",
        "chrome-linux64/chrome",
        "chrome-linux/chrome",
    )


def derive_browser_bin() -> str:
    """内置浏览器路径推导（写入工作进程 UVMP_BROWSER_BIN）。

    显式环境变量 UVMP_BROWSER_BIN 优先（开发用）；冻结版在浏览器资源目录
    （uvmp-core 同目录 browser/ 与上级 resources/browser/）推导内置浏览器，
    生产默认不依赖宿主 Chrome。找不到返回空串，由工作进程按 render 配置处理。"""
    env = (os.environ.get("UVMP_BROWSER_BIN") or "").strip()
    if env:
        return env
    if not getattr(sys, "frozen", False):
        return ""  # 源码开发：走显式 env 或 render.chrome，不猜内置浏览器
    exe_dir = Path(sys.executable).resolve().parent
    for d in (exe_dir / "browser", exe_dir.parent / "browser"):
        for name in _browser_names():
            p = d / name
            if p.is_file():
                return str(p)
    return ""


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

    def log(msg):
        job.log(_scrub(str(msg), secrets))

    env = dict(os.environ)
    browser_bin = derive_browser_bin()
    if browser_bin and not (os.environ.get("UVMP_BROWSER_BIN") or "").strip():
        env["UVMP_BROWSER_BIN"] = browser_bin

    job.check_cancel()
    log("Go 导出工作进程：%s（并行导出）" % binary)
    try:
        proc = subprocess.Popen(
            [binary, "run"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            env=env,
            creationflags=subprocess.CREATE_NO_WINDOW if _is_windows() else 0,
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
            try:
                proc.terminate()
            except Exception:  # noqa: BLE001
                pass
            deadline = time.time() + 3
            while time.time() < deadline and proc.poll() is None:
                time.sleep(0.2)
            if proc.poll() is None:
                try:
                    proc.kill()
                except Exception:  # noqa: BLE001
                    pass

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
            try:
                proc.terminate()
            except Exception:  # noqa: BLE001
                pass
            try:
                proc.wait(timeout=5)
            except Exception:  # noqa: BLE001
                try:
                    proc.kill()
                except Exception:  # noqa: BLE001
                    pass
