#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""构建门禁：CDP 冒烟（纯 Python data: 自检）。

经 uvmp-core 的 stdio JSON-RPC 调 `selftest:cdp`：由 toolkit/selftest.run_cdp 复用
official_track.CdpBrowser 驱动宿主公司指定浏览器，只打开 data:text/html 本地页（零网络），
验证导航 / eval / 新 tab / 截图 PNG / printToPDF，产物落隔离的临时状态目录。
不使用 Electron/Node/Go 任何 CDP 通道。

隔离：UVMP_TOOLKIT_CONFIG 指向仓库内 config.example.json（不读用户真实配置），
LOCALAPPDATA / XDG_DATA_HOME 指向临时目录（不写用户状态目录），结束后删除。

用法：
  python packaging/cdp_smoke.py --core dist/uvmp-core/uvmp-core.exe
  python packaging/cdp_smoke.py --core toolkit/app.py      # 源码核心（排障）
退出码：0 通过；1 失败；2 宿主无可用浏览器（明确报告后跳过，不静默当通过）。
"""

import argparse
import json
import os
import queue
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

PACKAGING_DIR = Path(__file__).resolve().parent
PROJECT_DIR = PACKAGING_DIR.parent
EXAMPLE_CONFIG = PROJECT_DIR / "toolkit" / "config.example.json"

EXPECTED_CHECKS = ("启动浏览器", "导航与 eval", "新 tab", "截图 PNG", "printToPDF")
PNG_MAGIC = b"\x89PNG\r\n\x1a\n"
PDF_MAGIC = b"%PDF"
CANCEL_GRACE = 10  # 超时后发 cancelJob，等待取消终态的宽限（秒）
SHUTDOWN_GRACE = 5  # 关闭 stdin 后等待核心自然退出的宽限（秒）


class SmokeError(RuntimeError):
    pass


class SmokeTimeout(SmokeError):
    pass


def _kill_process_tree(proc):
    """终止核心及其子进程树（浏览器）：Windows taskkill /T /F；POSIX 独立进程组
    killpg（Popen 以 start_new_session 启动，pgid 即核心 pid）。只发终止信号，
    等待回收由调用方完成，避免与输出读取线程互相阻塞。"""
    if proc is None:
        return
    try:
        if proc.poll() is not None:
            return
    except Exception:  # noqa: BLE001
        pass
    if os.name == "nt":
        try:
            subprocess.run(
                ["taskkill", "/T", "/F", "/PID", str(proc.pid)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=15,
            )
            return
        except Exception:  # noqa: BLE001
            pass
    else:
        try:
            pgid = os.getpgid(proc.pid)
            if pgid != os.getpgrp():  # 只杀独立进程组，绝不误伤本进程组
                os.killpg(pgid, signal.SIGKILL)
                return
        except Exception:  # noqa: BLE001
            pass
    try:
        proc.kill()
    except Exception:  # noqa: BLE001
        pass


def _send_rpc(proc, obj) -> bool:
    try:
        proc.stdin.write(json.dumps(obj) + "\n")
        proc.stdin.flush()
        return True
    except Exception:  # noqa: BLE001
        return False


def _await_terminal(events, logs, grace):
    """cancelJob 之后消费事件至 job.done 或宽限到期，返回 (job, 是否收到终态)"""
    deadline = time.monotonic() + max(0, grace)
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return None, False
        try:
            line = events.get(timeout=min(0.5, remaining))
        except queue.Empty:
            continue
        if line is None:  # 核心 stdout 已关闭
            return None, False
        try:
            obj = json.loads(line)
        except ValueError:
            continue
        if obj.get("event") == "job.log":
            logs.append(str(obj.get("line") or ""))
        elif obj.get("event") == "job.done":
            return obj.get("job") or {}, True


def _shutdown(proc, events, logs, threads):
    """收尾顺序：关 stdin 让核心退出 → 未退则终止核心及浏览器进程树 → 等输出读到
    EOF（子进程可能持有管道）→ 汇总剩余 job.log → 最后关闭 stdout/stderr。
    临时目录由 run_smoke 在全部清理完成后删除。"""
    try:
        proc.stdin.close()
    except Exception:  # noqa: BLE001
        pass
    try:
        proc.wait(timeout=SHUTDOWN_GRACE)
    except subprocess.TimeoutExpired:
        _kill_process_tree(proc)
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try:
                proc.kill()
            except Exception:  # noqa: BLE001
                pass
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
    for t in threads:
        t.join(timeout=5)
    while True:
        try:
            item = events.get_nowait()
        except queue.Empty:
            break
        if item is None:
            continue
        try:
            obj = json.loads(item)
        except ValueError:
            continue
        if obj.get("event") == "job.log":
            logs.append(str(obj.get("line") or ""))
    # 读线程仍阻塞（极端：核心退出但浏览器子进程仍持有管道）时不关闭流：
    # 从另一线程 close 正在读取的管道会卡住（这正是旧实现超时卡死的根因）。
    if not any(t.is_alive() for t in threads):
        for stream in (proc.stdout, proc.stderr):
            try:
                stream.close()
            except Exception:  # noqa: BLE001
                pass


def _print_tail(title, lines, limit=15):
    if not lines:
        return
    print("[cdp-smoke] %s 尾部：" % title)
    for line in lines[-limit:]:
        print("  " + line)


def _core_command(core: str) -> list:
    path = Path(core)
    if not path.exists():
        raise SmokeError("核心不存在: %s" % path)
    if path.suffix.lower() == ".py":
        return [sys.executable, str(path), "rpc"]
    return [str(path), "rpc"]


def _pump(stream, sink: queue.Queue):
    try:
        for line in iter(stream.readline, ""):
            sink.put(line.rstrip("\n"))
    finally:
        sink.put(None)


def _drain_stderr(stream, lines: list):
    try:
        for line in iter(stream.readline, ""):
            lines.append(line.rstrip("\n"))
            del lines[:-200]
    except Exception:  # noqa: BLE001
        pass


def _pick(obj: dict) -> str:
    """兼容不同终态负载：error 字段或日志尾部"""
    error = str(obj.get("error") or "")
    if not error:
        tail = obj.get("log_tail") or []
        error = " | ".join(str(x) for x in tail[-3:])
    return error or "任务失败（status=%s）" % obj.get("status")


def _verify_artifacts(result: dict) -> tuple:
    if result.get("ok") is not True:
        raise SmokeError("自检结果 ok 不为 true: %r" % (result.get("ok"),))
    checks = list(result.get("checks") or [])
    if tuple(checks) != EXPECTED_CHECKS:
        raise SmokeError("检查项不符：%r；期望 %r" % (checks, list(EXPECTED_CHECKS)))
    png = Path(str(result.get("png") or ""))
    pdf = Path(str(result.get("pdf") or ""))
    if not png.is_file():
        raise SmokeError("截图产物不存在: %s" % png)
    if not pdf.is_file():
        raise SmokeError("PDF 产物不存在: %s" % pdf)
    if png.read_bytes()[: len(PNG_MAGIC)] != PNG_MAGIC:
        raise SmokeError("截图产物不是 PNG: %s" % png)
    if pdf.read_bytes()[: len(PDF_MAGIC)] != PDF_MAGIC:
        raise SmokeError("printToPDF 产物不是 PDF: %s" % pdf)
    return png, pdf


def run_smoke(core: str, timeout: int) -> int:
    cmd = _core_command(core)
    if not EXAMPLE_CONFIG.is_file():
        raise SmokeError("缺少隔离配置模板: %s" % EXAMPLE_CONFIG)

    work = Path(tempfile.mkdtemp(prefix="uvmp-cdp-smoke-"))
    try:
        return _run_in_workspace(cmd, work, timeout)
    finally:
        shutil.rmtree(work, ignore_errors=True)


def _run_in_workspace(cmd: list, work: Path, timeout: int) -> int:
    env = dict(os.environ)
    env["UVMP_TOOLKIT_CONFIG"] = str(EXAMPLE_CONFIG)
    if os.name == "nt":
        env["LOCALAPPDATA"] = str(work)
        env["APPDATA"] = str(work)
    else:
        env["XDG_DATA_HOME"] = str(work)
        env["XDG_CONFIG_HOME"] = str(work)

    proc = subprocess.Popen(
        cmd,
        cwd=str(PROJECT_DIR),
        env=env,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
        bufsize=1,
        start_new_session=(os.name != "nt"),  # POSIX 独立进程组：超时可整树回收
    )
    events = queue.Queue()
    stderr_lines = []
    threads = [
        threading.Thread(
            target=_pump, args=(proc.stdout, events), daemon=True, name="cdp-smoke-out"
        ),
        threading.Thread(
            target=_drain_stderr, args=(proc.stderr, stderr_lines), daemon=True
        ),
    ]
    for t in threads:
        t.start()

    job = None
    job_id = ""
    logs = []
    timed_out = False
    deadline = time.monotonic() + max(1, timeout)
    try:
        if not _send_rpc(
            proc, {"id": 1, "method": "runJob", "params": {"app_id": "selftest:cdp"}}
        ):
            raise SmokeError("核心 stdin 已关闭，无法启动自检")
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                break
            try:
                line = events.get(timeout=min(0.5, remaining))
            except queue.Empty:
                continue
            if line is None:
                break
            try:
                obj = json.loads(line)
            except ValueError:
                continue
            if "event" in obj:
                if obj.get("event") == "job.log":
                    logs.append(str(obj.get("line") or ""))
                elif obj.get("event") == "job.done":
                    job = obj.get("job") or {}
                    break
            elif obj.get("id") == 1:
                if obj.get("ok"):
                    job_id = str((obj.get("result") or {}).get("job_id") or "")
                else:
                    raise SmokeError("核心拒绝自检: %s" % obj.get("error"))
        if timed_out:
            cancel_job = None
            if job_id:  # 先取消任务并消费取消终态，再终止进程树
                _send_rpc(
                    proc,
                    {
                        "id": 2,
                        "method": "cancelJob",
                        "params": {"job_id": job_id},
                    },
                )
                cancel_job, _done = _await_terminal(events, logs, CANCEL_GRACE)
            raise SmokeTimeout(
                "等待核心自检结束超时（%ss）%s"
                % (
                    timeout,
                    (
                        "；已发送 cancelJob（终态：%s）"
                        % ((cancel_job or {}).get("status") or "未收到")
                    )
                    if job_id
                    else "",
                )
            )
        if job is None:
            tail = " / ".join(stderr_lines[-3:])
            raise SmokeError(
                "核心未返回 job.done（退出码 %s）%s"
                % (proc.poll(), ("；stderr: " + tail) if tail else "")
            )
    finally:
        # 收尾：先取消/终止核心与浏览器进程树、等输出 EOF，最后才关管道；
        # 临时 profile 目录由 run_smoke 在 _shutdown 完成后删除。
        _shutdown(proc, events, logs, threads)
        if timed_out:  # 超时时保留已累积的日志与 stderr 尾部供排障
            _print_tail("job.log", logs)
            _print_tail("核心 stderr", stderr_lines)

    for line in logs:
        print("  " + line)
    if job.get("status") != "success":
        error = _pick(job)
        if any("未找到可用浏览器" in s for s in [error] + logs):
            print(
                "[cdp-smoke] 跳过 CDP 冒烟：宿主无可用浏览器（%s）。"
                "本门禁未验证 CDP 链路；请在内网机安装公司指定浏览器后重跑。" % error
            )
            return 2
        raise SmokeError(error)

    result = job.get("result") or {}
    png, pdf = _verify_artifacts(result)
    print(
        "[cdp-smoke] CDP 冒烟通过：浏览器 %s（检查 %d/%d）；截图 %d 字节；PDF %d 字节"
        % (
            result.get("browser") or "(未报告)",
            len(result.get("checks") or []),
            len(EXPECTED_CHECKS),
            png.stat().st_size,
            pdf.stat().st_size,
        )
    )
    return 0


def main(argv=None) -> int:
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    ap = argparse.ArgumentParser(description="构建门禁：纯 Python CDP 冒烟")
    ap.add_argument(
        "--core", required=True, help="uvmp-core 可执行文件（或源码 app.py）"
    )
    ap.add_argument("--timeout", type=int, default=240, help="超时秒数（默认 240）")
    args = ap.parse_args(argv)
    try:
        return run_smoke(args.core, args.timeout)
    except SmokeTimeout as e:
        print("[cdp-smoke] CDP 冒烟失败：%s" % e, file=sys.stderr)
        return 1
    except SmokeError as e:
        print("[cdp-smoke] CDP 冒烟失败：%s" % e, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
