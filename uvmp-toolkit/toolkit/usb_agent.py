# -*- coding: utf-8 -*-
"""U 盘单监听 agent：唯一负责“发现 U 盘 → 必要时挂载 → 消费服务端指令 → 写结果”。

设计要点（与《内网工具箱开发指南》U 盘一节一致）：
- 单实例：state/usb-agent.lock 抢占失败即退出；GUI/OS/手工检查共用一份逻辑。
- 事件源：Linux 内核 netlink uevent（block 子系统）+ 60s 兜底轮询；事件去抖 4s，
  60 秒内状态变化超过 2 次则暂停挂载 2 分钟，避免服务端切换共享时反复抢 udisks。
- 挂载状态机：先等桌面自动挂载 10s；仍未挂载才调用一次 udisksctl mount；
  失败按 10/30/120/300s 退避；每小时超过 12 次熔断 30 分钟；客户端从不主动卸载。
- 跨进程锁：state/usb.lock 覆盖扫描/挂载/指令执行/结果写入，GUI 手点与 agent 不并发。
- 指令：每应用 {app}_cmd.json；认领时原子改名隐藏文件；先写结果再删指令；
  崩在中间由 recover 补写 interrupted；结果写盘失败落本地 pending，下次挂载重试。
"""

import json
import logging
import os
import queue
import signal
import socket
import sys
import threading
import time
from collections import deque
from datetime import datetime
from logging.handlers import RotatingFileHandler
from pathlib import Path

import config as _config
import jobs as _jobs
import platform_utils
import state
import usb_protocol

LOG_MAX_BYTES = 1024 * 1024
LOG_BACKUPS = 5
STATUS_INTERVAL = 30 * 60
PENDING_PREFIX = "result_"


# ---------------------------------------------------------------- 跨进程锁


def _try_lock(path: Path, blocking: bool = False):
    """返回 fd 或 None；Windows 用 msvcrt，POSIX 用 flock。"""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(str(path), os.O_RDWR | os.O_CREAT, 0o600)
    try:
        if os.name == "nt":
            import msvcrt

            os.lseek(fd, 0, os.SEEK_SET)
            if os.fstat(fd).st_size == 0:
                os.write(fd, b"0")
                os.lseek(fd, 0, os.SEEK_SET)
            msvcrt.locking(fd, msvcrt.LK_LOCK if blocking else msvcrt.LK_NBLCK, 1)
        else:
            import fcntl

            flags = fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB)
            fcntl.flock(fd, flags)
        return fd
    except OSError:
        os.close(fd)
        return None


def _release_lock(fd):
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


# ---------------------------------------------------------------- 日志


def _setup_logging(file_name: str, verbose: bool = False, quiet: bool = False):
    logger = logging.getLogger("uvmp.usb_agent")
    logger.setLevel(logging.DEBUG if verbose else logging.INFO)
    logger.propagate = False
    for h in list(logger.handlers):
        logger.removeHandler(h)
        try:
            h.close()
        except Exception:  # noqa: BLE001
            pass
    fmt = logging.Formatter("[%(asctime)s] %(levelname)s %(message)s", "%Y-%m-%d %H:%M:%S")
    try:
        fh = RotatingFileHandler(
            str(state.logs_dir() / file_name),
            maxBytes=LOG_MAX_BYTES,
            backupCount=LOG_BACKUPS,
            encoding="utf-8",
        )
        fh.setFormatter(fmt)
        logger.addHandler(fh)
    except OSError:
        pass
    if verbose or (not quiet and sys.stdout is not None and sys.stdout.isatty()):
        sh = logging.StreamHandler(sys.stdout)
        sh.setFormatter(fmt)
        logger.addHandler(sh)
    if not logger.handlers:
        logger.addHandler(logging.NullHandler())
    return logger


def _iso(ts: float) -> str:
    return datetime.fromtimestamp(ts, usb_protocol.CST).isoformat(timespec="seconds")


def _arg_int(cfg: dict, key: str, default: int, minimum: int = 0) -> int:
    try:
        value = int((cfg or {}).get(key, default))
    except (TypeError, ValueError):
        return default
    return max(minimum, value)


def _backoff_list(cfg: dict) -> list:
    raw = (cfg or {}).get("mount_backoff_sec")
    if not isinstance(raw, list):
        return [10, 30, 120, 300]
    out = []
    for item in raw:
        try:
            n = int(item)
        except (TypeError, ValueError):
            continue
        if n > 0:
            out.append(n)
    return out or [10, 30, 120, 300]


# ---------------------------------------------------------------- agent


class UsbAgent:
    def __init__(self, verbose: bool = False, quiet: bool = False):
        self.verbose = verbose
        self.quiet = quiet
        self.logger = _setup_logging("usb-agent.log", verbose, quiet)
        self.stop = threading.Event()
        self.events = queue.Queue()
        self.base = state.state_dir() / "usb-agent"
        self.base.mkdir(parents=True, exist_ok=True)
        self.pending_dir = self.base / "pending"
        self.pending_dir.mkdir(parents=True, exist_ok=True)
        self.instance_lock_path = state.state_dir() / "usb-agent.lock"
        self.device_lock_path = state.state_dir() / "usb.lock"
        self.processed_path = self.base / "processed.json"
        self.journal_path = self.base / "journal.jsonl"
        self.version = os.environ.get("UVMP_VERSION", "")
        self.jobs = _jobs.JobManager()
        self.apps = {}
        self.apps_loaded_at = 0
        self.label = ""
        self.agent_cfg = {}
        self.device = ""
        self.mount = ""
        self.readonly = False
        self.mounted_at = ""
        self.first_seen = 0
        self.next_mount_at = 0
        self.backoff_idx = 0
        self.mount_attempts = deque(maxlen=100)
        self.paused_until = 0
        self.unstable_until = 0
        self.state_changes = deque(maxlen=32)
        self.last_status_write = 0
        self.last_status_error = 0
        self.last_command = None
        self.last_error = ""
        self.processed = self._load_processed()
        self._load_settings()
        self._reload_apps(force=True)

    # ---- 配置/应用 ----

    def _load_settings(self):
        cfg = _config.load()
        usb = cfg.get("usb") or {}
        label = str(usb.get("label") or "GLKVM").strip() or "GLKVM"
        if label != self.label:
            if self.label:
                self.logger.info("U 盘卷标配置变更: %s → %s", self.label, label)
                self.mount = ""
                self.device = ""
                self.first_seen = 0
                self.backoff_idx = 0
                self.next_mount_at = 0
            self.label = label
        self.agent_cfg = usb.get("agent") or {}

    def _reload_apps(self, force: bool = False):
        now = time.time()
        if not force and now - self.apps_loaded_at < 600:
            return
        from apps import scan_apps

        apps = {}
        for meta in scan_apps():
            backend = meta.get("_backend")
            if backend is not None and hasattr(backend, "handle_command"):
                apps[meta["id"]] = (meta, backend)
        if set(apps) != set(self.apps):
            self.logger.info("可执行 U 盘指令的应用: %s", ", ".join(sorted(apps)) or "无")
        self.apps = apps
        self.apps_loaded_at = now

    def _poll_sec(self) -> int:
        return _arg_int(self.agent_cfg, "poll_sec", 60, 5)

    def _mounted_poll_sec(self) -> int:
        return _arg_int(self.agent_cfg, "mounted_poll_sec", 15, 3)

    def _debounce_sec(self) -> int:
        return _arg_int(self.agent_cfg, "debounce_sec", 4, 0)

    # ---- 本地状态 ----

    def _load_processed(self) -> set:
        try:
            data = usb_protocol.read_json(self.processed_path, max_bytes=65536)
            return set(str(x) for x in (data.get("ids") or []))
        except Exception:  # noqa: BLE001
            return set()

    def _mark_processed(self, cmd_id: str):
        if cmd_id in self.processed:
            return
        self.processed.add(cmd_id)
        while len(self.processed) > 200:
            self.processed.pop()
        try:
            state.write_json(self.processed_path, {"ids": list(self.processed)})
        except OSError as e:
            self.logger.warning("写 processed 失败: %s", e)

    def _append_journal(self, record: dict):
        try:
            with open(self.journal_path, "a", encoding="utf-8") as f:
                f.write(json.dumps(record, ensure_ascii=False) + "\n")
        except OSError as e:
            self.logger.warning("写 journal 失败: %s", e)

    def _prune_local(self, retention_days: int):
        try:
            state.prune_old_logs(retention_days)
        except Exception:  # noqa: BLE001
            pass
        cutoff = time.time() - retention_days * 86400
        try:
            for p in self.pending_dir.glob("*.json"):
                try:
                    if p.stat().st_mtime < cutoff:
                        p.unlink()
                except OSError:
                    pass
        except OSError:
            pass
        try:
            if self.journal_path.exists():
                lines = self.journal_path.read_text(encoding="utf-8").splitlines()
                keep = []
                for line in lines:
                    try:
                        rec = json.loads(line)
                    except ValueError:
                        continue
                    stamp = rec.get("finished_at") or rec.get("claimed_at") or ""
                    dt = usb_protocol.parse_iso(stamp)
                    if dt is not None and dt.timestamp() < cutoff:
                        continue
                    keep.append(line)
                keep = keep[-1000:]
                text = "\n".join(keep) + ("\n" if keep else "")
                if text != self.journal_path.read_text(encoding="utf-8"):
                    self.journal_path.write_text(text, encoding="utf-8")
        except OSError:
            pass

    # ---- 事件源 ----

    def _start_udev(self):
        if not sys.platform.startswith("linux"):
            return
        t = threading.Thread(target=self._udev_loop, daemon=True, name="usb-udev")
        t.start()

    def _udev_loop(self):
        proto = getattr(socket, "NETLINK_KOBJECT_UEVENT", 15)
        failures = 0
        while not self.stop.is_set() and failures < 3:
            sock = None
            try:
                sock = socket.socket(socket.AF_NETLINK, socket.SOCK_DGRAM, proto)
                sock.bind((0, 1))
                sock.settimeout(1.0)
                self.logger.info("已订阅内核 U 盘插拔事件")
                failures = 0
                while not self.stop.is_set():
                    try:
                        data, _ = sock.recvfrom(4096)
                    except socket.timeout:
                        continue
                    if b"SUBSYSTEM=block" in data and (
                        b"ACTION=add" in data or b"ACTION=remove" in data
                    ):
                        self.events.put(("udev", time.time()))
            except OSError as e:
                failures += 1
                self.logger.warning("订阅 udev 失败（第 %d 次，将回退轮询）: %s", failures, e)
                self.stop.wait(5.0)
            finally:
                if sock is not None:
                    try:
                        sock.close()
                    except OSError:
                        pass

    def _note_event(self):
        now = time.time()
        self.state_changes.append(now)
        window = _arg_int(self.agent_cfg, "unstable_window_sec", 60, 10)
        while self.state_changes and now - self.state_changes[0] > window:
            self.state_changes.popleft()
        if len(self.state_changes) > 2 and now >= self.unstable_until:
            pause = _arg_int(self.agent_cfg, "unstable_pause_sec", 120, 10)
            self.unstable_until = now + pause
            self.logger.warning(
                "U 盘 %d 秒内状态变化 %d 次，暂停挂载 %d 秒（避免与共享切换抢 udisks）",
                window,
                len(self.state_changes),
                pause,
            )

    def _debounce(self):
        end = time.time() + self._debounce_sec()
        while time.time() < end and not self.stop.is_set():
            self.stop.wait(min(0.2, max(0.0, end - time.time())))
        while True:
            try:
                self.events.get_nowait()
            except queue.Empty:
                break

    # ---- 主循环 ----

    def run(self) -> int:
        fd = _try_lock(self.instance_lock_path)
        if fd is None:
            self.logger.info("已有 usb-agent 在运行，本实例退出")
            return 0
        try:
            self._prune_local(_arg_int(self.agent_cfg, "log_retention_days", 30, 1))
            self.logger.info(
                "usb-agent 启动（卷标=%s，poll=%ds，mounted_poll=%ds）",
                self.label,
                self._poll_sec(),
                self._mounted_poll_sec(),
            )
            for sig in (signal.SIGTERM, signal.SIGINT):
                try:
                    signal.signal(sig, self._on_signal)
                except (ValueError, OSError):
                    pass
            self._start_udev()
            while not self.stop.is_set():
                timeout = self._mounted_poll_sec() if self.mount else self._poll_sec()
                try:
                    ev = self.events.get(timeout=timeout)
                    if ev and ev[0] == "stop":
                        break
                    self._note_event()
                    self._debounce()
                except queue.Empty:
                    pass
                if self.stop.is_set():
                    break
                try:
                    self.tick()
                except Exception as e:  # noqa: BLE001
                    self.logger.exception("本轮检查异常: %s", e)
            self.logger.info("usb-agent 退出")
            return 0
        finally:
            _release_lock(fd)

    def _on_signal(self, signum, _frame):
        self.logger.info("收到信号 %s，准备退出", signum)
        self.stop.set()
        self.events.put(("stop", time.time()))

    def run_once(self) -> int:
        """手工检查一次（GUI「立即检查」/排障）：不抢单实例锁，只抢设备锁。"""
        self._reload_apps(force=True)
        self.tick()
        if self.mount:
            print("U盘已就绪: %s（卷标 %s）" % (self.mount, self.label))
        else:
            print("未检测到卷标 %s 的U盘（或等待自动挂载中）" % self.label)
        return 0

    # ---- 单轮检查 ----

    def tick(self):
        self._load_settings()
        self._reload_apps()
        lock = _try_lock(self.device_lock_path)
        if lock is None:
            self.logger.debug("U 盘操作锁被其他进程占用，跳过本轮")
            return
        try:
            self._tick_locked()
        finally:
            _release_lock(lock)

    def _tick_locked(self):
        found = platform_utils.scan_usb_device(
            self.label, lambda m: self.logger.debug("扫描U盘: %s", m)
        )
        now = time.time()
        if not found:
            if self.mount:
                self.logger.info("U盘已移除: %s", self.mount)
                self.mount = ""
                self.device = ""
                self.readonly = False
                self.first_seen = 0
                self.mounted_at = ""
            return

        device, mount, readonly = found
        if device != self.device:
            self.logger.info("发现U盘设备: %s", device or "(windows)")
            self.device = device
            self.first_seen = now
            self.backoff_idx = 0
            self.next_mount_at = 0

        if mount:
            if mount != self.mount:
                self.logger.info(
                    "U盘已挂载: %s%s", mount, "（只读）" if readonly else ""
                )
                self.mount = mount
                self.readonly = readonly
                self.mounted_at = _iso(now)
                self.first_seen = 0
                self.backoff_idx = 0
                self.next_mount_at = 0
                self._write_status(force=True, agent_state="mounted")
            self._flush_pending(mount)
            if self.readonly:
                self.logger.warning("U盘为只读挂载，跳过指令执行（请检查文件系统）")
                return
            self._recover_running(mount)
            self._process_commands(mount)
            self._cleanup_results(mount)
            self._write_status(force=False)
            return

        if self.mount:
            self.logger.info("U盘已卸载（可能服务端正在切换共享）")
            self.mount = ""
            self.readonly = False
            self.mounted_at = ""

        grace = _arg_int(self.agent_cfg, "auto_mount_grace_sec", 10, 0)
        if self.first_seen and now < self.first_seen + grace:
            return
        if now < self.paused_until or now < self.unstable_until:
            return
        if now < self.next_mount_at:
            return
        self._attempt_mount(device)

        found = platform_utils.scan_usb_device(
            self.label, lambda m: self.logger.debug("挂载后扫描: %s", m)
        )
        if found and found[1]:
            self.mount = found[1]
            self.readonly = found[2]
            self.mounted_at = _iso(time.time())
            self._flush_pending(self.mount)
            if not self.readonly:
                self._recover_running(self.mount)
                self._process_commands(self.mount)
                self._cleanup_results(self.mount)
            self._write_status(force=True, agent_state="mounted")

    def _attempt_mount(self, device: str):
        now = time.time()
        self.mount_attempts = deque(
            (t for t in self.mount_attempts if now - t < 3600), maxlen=100
        )
        max_attempts = _arg_int(self.agent_cfg, "max_mount_attempts_per_hour", 12, 1)
        if len(self.mount_attempts) >= max_attempts:
            pause = _arg_int(self.agent_cfg, "circuit_pause_sec", 1800, 60)
            self.paused_until = now + pause
            self.last_error = "挂载尝试超过 %d 次/小时，熔断 %d 秒" % (max_attempts, pause)
            self.logger.error(self.last_error)
            return
        self.mount_attempts.append(now)
        self.logger.info("U盘存在但未挂载，尝试挂载: %s", device or "(unknown)")
        if platform_utils.mount_usb_device(device, self.logger.info):
            self.backoff_idx = 0
            self.next_mount_at = 0
            self.last_error = ""
            return
        backoff = _backoff_list(self.agent_cfg)
        idx = min(self.backoff_idx, len(backoff) - 1)
        delay = backoff[idx]
        self.backoff_idx = min(idx + 1, len(backoff) - 1)
        self.next_mount_at = now + delay
        self.last_error = "挂载失败，%d 秒后重试" % delay
        self.logger.warning(self.last_error)

    # ---- 指令 ----

    def _process_commands(self, mount: str):
        root = Path(mount)
        for app_id in sorted(self.apps):
            meta, backend = self.apps[app_id]
            path = root / usb_protocol.cmd_name(app_id)
            try:
                if not path.is_file():
                    continue
            except OSError as e:
                self.logger.warning("检查指令文件失败（U盘可能已断开）: %s", e)
                return
            try:
                self._run_one(app_id, meta, backend, path, mount)
            except OSError as e:
                self.logger.warning("执行指令时 U 盘断开: %s", e)
                return
            except Exception as e:  # noqa: BLE001
                self.logger.exception("执行指令异常 (%s): %s", app_id, e)

    def _run_one(self, app_id: str, meta: dict, backend, path: Path, mount: str):
        raw = None
        try:
            raw = usb_protocol.read_json(path)
            cmd = usb_protocol.normalize_command(raw, app_hint=app_id)
        except usb_protocol.ProtocolError as e:
            cmd_id = ""
            if isinstance(raw, dict):
                cmd_id = str(raw.get("id") or raw.get("cycle_id") or "")
            if not cmd_id:
                cmd_id = "invalid-%d" % int(time.time())
            res = usb_protocol.build_result(
                {"id": cmd_id, "app": app_id},
                False,
                "指令格式非法: %s" % e,
                data={"invalid": True, "file": path.name},
                version=self.version,
            )
            self.logger.warning("指令格式非法（%s）: %s", path.name, e)
            self._finish_result(
                mount, app_id, cmd_id, res, None, ok=False, error=res.get("error", "")
            )
            self._safe_unlink(path)
            return

        if cmd["app"] != app_id:
            res = usb_protocol.build_result(
                cmd,
                False,
                "指令 app=%r 与文件名 app=%r 不一致" % (cmd["app"], app_id),
                version=self.version,
            )
            self.logger.warning("指令 app 不一致: %s", path.name)
            self._finish_result(
                mount, app_id, cmd["id"], res, None, ok=False, error=res.get("error", "")
            )
            self._safe_unlink(path)
            return

        if usb_protocol.is_expired(cmd):
            res = usb_protocol.build_result(
                cmd,
                False,
                "指令已过期（expires_at=%s）" % cmd.get("expires_at", ""),
                data={"expired": True},
                version=self.version,
            )
            self.logger.info("指令已过期，跳过: %s", cmd["id"])
            self._finish_result(
                mount, app_id, cmd["id"], res, None, ok=False, error=res.get("error", "")
            )
            self._safe_unlink(path)
            return

        result_path = Path(mount) / usb_protocol.result_name(app_id, cmd["id"])
        if cmd["id"] in self.processed and result_path.exists():
            self.logger.info("指令已处理过，删除重复文件: %s", cmd["id"])
            self._safe_unlink(path)
            return

        running = Path(mount) / usb_protocol.running_name(app_id, cmd["id"])
        try:
            os.replace(str(path), str(running))
        except OSError as e:
            self.logger.warning("认领指令失败（可能已消失）: %s", e)
            return

        started = time.time()
        self.logger.info(
            "执行指令: app=%s action=%s id=%s", app_id, cmd["action"], cmd["id"]
        )
        job = self.jobs.create(app_id, "U盘指令·%s" % meta.get("name", app_id))
        job.echo = False
        try:
            self.jobs.run_sync(
                job, lambda j: backend.handle_command(j, cmd, mount), echo=False
            )
        except Exception:  # JobManager 已记录，不让单条指令打断 agent
            pass
        finished = time.time()
        ok = job.status == "success"
        error = "" if ok else (job.error or "任务%s" % job.status)
        res = usb_protocol.build_result(
            cmd,
            ok,
            error,
            data=job.result or {},
            started_at=_iso(started),
            finished_at=_iso(finished),
            duration_ms=int((finished - started) * 1000),
            version=self.version,
        )
        self._finish_result(mount, app_id, cmd["id"], res, running, started, finished, ok, error)

    def _finish_result(
        self,
        mount: str,
        app_id: str,
        cmd_id: str,
        res: dict,
        running: Path | None,
        started: float = 0,
        finished: float = 0,
        ok: bool = False,
        error: str = "",
    ):
        written = False
        try:
            usb_protocol.write_json_atomic(
                Path(mount) / usb_protocol.result_name(app_id, cmd_id), res
            )
            written = True
        except Exception as e:  # noqa: BLE001
            self.logger.error("写结果失败（转本地 pending，下次挂载重试）: %s", e)
            self._save_pending(app_id, cmd_id, res, running.name if running else "")
        if written:
            self._mark_processed(cmd_id)
            if running is not None:
                self._safe_unlink(running)
            try:
                platform_utils.sync_mount(mount)
            except Exception:  # noqa: BLE001
                pass
        self.last_command = {
            "id": cmd_id,
            "app": app_id,
            "ok": bool(ok),
            "finished_at": res.get("finished_at", ""),
            "error": error,
        }
        self.last_error = "" if ok else error
        self._append_journal(
            {
                "id": cmd_id,
                "app": app_id,
                "claimed_at": _iso(started) if started else "",
                "finished_at": _iso(finished) if finished else usb_protocol.now_iso(),
                "ok": bool(ok),
                "duration_ms": res.get("duration_ms", 0),
                "error": error,
                "result_written": written,
            }
        )
        self.logger.info(
            "指令完成: app=%s id=%s ok=%s%s",
            app_id,
            cmd_id,
            ok,
            (" error=" + error) if error else "",
        )
        if written:
            self._write_status(force=True, agent_state="idle")

    def _save_pending(self, app_id: str, cmd_id: str, res: dict, running_name: str):
        name = PENDING_PREFIX + usb_protocol.sanitize_id(cmd_id) + ".json"
        try:
            usb_protocol.write_json_atomic(
                self.pending_dir / name,
                {
                    "app": app_id,
                    "id": cmd_id,
                    "running": running_name,
                    "result": res,
                },
            )
        except OSError as e:
            self.logger.error("保存本地 pending 结果失败: %s", e)

    def _flush_pending(self, mount: str):
        try:
            files = sorted(self.pending_dir.glob("*.json"))
        except OSError:
            return
        for p in files:
            try:
                data = usb_protocol.read_json(p, max_bytes=65536)
                app_id = str(data.get("app") or "")
                cmd_id = str(data.get("id") or "")
                res = data.get("result") or {}
                if not app_id or not cmd_id or not isinstance(res, dict):
                    p.unlink()
                    continue
                usb_protocol.write_json_atomic(
                    Path(mount) / usb_protocol.result_name(app_id, cmd_id), res
                )
                running = str(data.get("running") or "")
                if running:
                    self._safe_unlink(Path(mount) / running)
                p.unlink()
                self.logger.info("已补写 pending 结果: %s %s", app_id, cmd_id)
            except Exception as e:  # noqa: BLE001
                self.logger.warning("补写 pending 结果失败（保留待下轮）: %s", e)

    def _recover_running(self, mount: str):
        root = Path(mount)
        try:
            entries = list(root.iterdir())
        except OSError:
            return
        for p in entries:
            parsed = usb_protocol.parse_running_name(p.name)
            if not parsed:
                continue
            app_id, cmd_id = parsed
            result_path = root / usb_protocol.result_name(app_id, cmd_id)
            if result_path.exists():
                self._safe_unlink(p)
                continue
            res = usb_protocol.build_result(
                {"id": cmd_id, "app": app_id},
                False,
                "客户端在写入结果前中断（指令可能已执行，请服务端按超时/结果缺失处理）",
                data={"interrupted": True},
                version=self.version,
            )
            try:
                usb_protocol.write_json_atomic(result_path, res)
                self._safe_unlink(p)
                self._append_journal(
                    {
                        "id": cmd_id,
                        "app": app_id,
                        "finished_at": usb_protocol.now_iso(),
                        "ok": False,
                        "error": "interrupted",
                        "result_written": True,
                    }
                )
                self.logger.warning("补写中断结果: %s %s", app_id, cmd_id)
            except Exception as e:  # noqa: BLE001
                self.logger.warning("补写中断结果失败: %s", e)

    def _cleanup_results(self, mount: str):
        days = _arg_int(self.agent_cfg, "result_retention_days", 7, 1)
        cutoff = time.time() - days * 86400
        root = Path(mount)
        try:
            for p in root.glob("*_result_*.json"):
                try:
                    if p.stat().st_mtime < cutoff:
                        p.unlink()
                        self.logger.info("清理过期结果文件: %s", p.name)
                except OSError:
                    pass
        except OSError:
            return
        try:
            for p in root.glob(".*.tmp"):
                try:
                    if p.stat().st_mtime < time.time() - 86400:
                        p.unlink()
                except OSError:
                    pass
        except OSError:
            pass

    # ---- 状态 ----

    def _write_status(self, force: bool = False, agent_state: str = "idle"):
        if not self.mount:
            return
        now = time.time()
        if not force and now - self.last_status_write < STATUS_INTERVAL:
            return
        pending = []
        for app_id in self.apps:
            try:
                if (Path(self.mount) / usb_protocol.cmd_name(app_id)).is_file():
                    pending.append(app_id)
            except OSError:
                pass
        info = usb_protocol.client_info(self.version)
        status = {
            "version": 1,
            "agent_version": self.version,
            "host": info.get("host", ""),
            "user": info.get("user", ""),
            "last_seen": usb_protocol.now_iso(),
            "state": agent_state,
            "usb": {
                "label": self.label,
                "mount": self.mount,
                "mounted_at": self.mounted_at,
                "readonly": bool(self.readonly),
            },
            "pending": pending,
            "last_command": self.last_command,
            "last_error": self.last_error,
        }
        try:
            usb_protocol.write_status(self.mount, status)
            self.last_status_write = now
        except Exception as e:  # noqa: BLE001
            if now - self.last_status_error > 600:
                self.logger.warning("写 U 盘状态失败: %s", e)
                self.last_status_error = now

    def _safe_unlink(self, path):
        try:
            Path(path).unlink()
        except FileNotFoundError:
            pass
        except OSError as e:
            self.logger.warning("删除文件失败 %s: %s", path, e)


def run(once: bool = False, verbose: bool = False, quiet: bool = False) -> int:
    agent = UsbAgent(verbose=verbose, quiet=quiet)
    if once:
        return agent.run_once()
    return agent.run()
