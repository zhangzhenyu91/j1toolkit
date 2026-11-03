# -*- coding: utf-8 -*-
"""开发脚本测试：在客户端同款 Python 运行时里跑外部 .py，并把输出流回任务日志。

设计：
- 不把脚本 exec 在 RPC 进程里：子进程隔离崩溃/取消，冻结版用 uvmp-core 的
  `run-script` 子命令，源码版用当前 Python + toolkit/app.py；
- 环境与客户端一致：继承父进程 env，强制 UTF-8/无缓冲，并注入客户端配置路径；
- 输出逐行 job.log，取消时杀整个进程组（脚本可能再 spawn 子进程）。
"""

import os
import shlex
import signal
import subprocess
import sys
import time
from pathlib import Path

import config as _config
from jobs import Cancelled

APP_ID = "dev_script"
TOOLKIT_DIR = Path(__file__).resolve().parents[2]


def _split_args(raw) -> list:
    if raw is None:
        return []
    if isinstance(raw, str):
        text = raw.strip()
        return shlex.split(text) if text else []
    return [str(x) for x in raw]


def _command(path: str, args: list) -> list:
    path = str(Path(path).expanduser().resolve())
    if getattr(sys, "frozen", False):
        return [sys.executable, "run-script", path, "--"] + args
    return [sys.executable, str(TOOLKIT_DIR / "app.py"), "run-script", path, "--"] + args


def _popen_kwargs() -> dict:
    kwargs = {
        "stdout": subprocess.PIPE,
        "stderr": subprocess.STDOUT,
        "text": True,
        "bufsize": 1,
        "encoding": "utf-8",
        "errors": "replace",
    }
    if os.name == "nt":
        kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        kwargs["start_new_session"] = True
    return kwargs


def _kill(proc: subprocess.Popen):
    if proc is None or proc.poll() is not None:
        return
    try:
        if os.name == "nt":
            proc.terminate()
        else:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except Exception:  # noqa: BLE001
        try:
            proc.terminate()
        except Exception:  # noqa: BLE001
            pass


def run(job, params: dict) -> dict:
    params = params or {}
    path = str(params.get("path") or "").strip()
    if not path:
        raise RuntimeError("请选择要运行的 .py 脚本")
    p = Path(path).expanduser()
    if not p.is_file():
        raise RuntimeError("脚本不存在: %s" % p)
    if p.suffix.lower() != ".py":
        raise RuntimeError("只支持 .py 文件: %s" % p)
    cwd = str(params.get("cwd") or "").strip()
    if cwd and not Path(cwd).expanduser().is_dir():
        raise RuntimeError("工作目录不存在: %s" % cwd)
    args = _split_args(params.get("args"))
    timeout = params.get("timeout")
    try:
        timeout = int(timeout) if timeout not in (None, "") else 0
    except (TypeError, ValueError):
        timeout = 0

    env = os.environ.copy()
    env.setdefault("UVMP_TOOLKIT_CONFIG", str(_config.config_path()))
    env["PYTHONUNBUFFERED"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"
    env["UVMP_DEV_SCRIPT"] = "1"

    cmd = _command(str(p), args)
    job.log("执行脚本: %s" % p)
    job.log("解释器: %s" % sys.executable)
    if args:
        job.log("参数: %s" % " ".join(args))
    job.set_progress(0, 1, "运行脚本")
    started = time.time()
    proc = subprocess.Popen(
        cmd,
        cwd=str(Path(cwd).expanduser()) if cwd else str(p.parent),
        env=env,
        **_popen_kwargs(),
    )
    job.add_cancel_hook(lambda: _kill(proc))

    timed_out = [False]

    def _on_timeout():
        timed_out[0] = True
        _kill(proc)

    timer = None
    if timeout > 0:
        import threading

        timer = threading.Timer(timeout, _on_timeout)
        timer.daemon = True
        timer.start()
    try:
        assert proc.stdout is not None
        for line in proc.stdout:
            if job.cancelled:
                _kill(proc)
                raise Cancelled()
            job.log(line.rstrip("\r\n"))
        code = proc.wait(timeout=10)
    finally:
        if timer is not None:
            timer.cancel()
        if proc.poll() is None:
            _kill(proc)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                pass
        try:
            proc.stdout and proc.stdout.close()
        except Exception:  # noqa: BLE001
            pass

    duration = int((time.time() - started) * 1000)
    job.set_progress(1, 1, "完成")
    if job.cancelled:
        raise Cancelled()
    if timed_out[0]:
        raise RuntimeError("脚本运行超时（%d 秒）" % timeout)
    if code != 0:
        raise RuntimeError("脚本退出码 %d" % code)
    return {
        "path": str(p),
        "args": args,
        "exit_code": code,
        "duration_ms": duration,
        "cwd": cwd or str(p.parent),
    }
