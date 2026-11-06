# -*- coding: utf-8 -*-
"""U 盘单监听 agent：唯一负责“发现 U 盘 → 必要时挂载 → 消费服务端指令 → 写结果”。

设计要点（与《内网工具箱开发指南》U 盘一节一致）：
- 单实例：state/usb-agent.lock 抢占失败即退出；GUI/OS/手工检查共用一份逻辑。
  systemd 服务设 UVMP_USB_AGENT_WAIT_LOCK=1 时改为等待锁（GUI 拉起的实例退出后立即接管），
  避免“服务先退出、图形进程又没起来”导致整天无人消费指令。
- 事件源：Linux 内核 netlink uevent（block 子系统）+ 兜底轮询（未挂载/无设备 60s，
  已挂载 15s）；事件去抖 4s。**挂载只由“设备出现且未挂载”触发一次**：先等桌面自动
  挂载 10s，到点仍未挂才 udisksctl mount（同一插入周期共 3 次，间隔 5s）；失败后
  不再定时重试，等下一次拔出/插入（服务端每轮会重新共享）。轮询只做发现/消费，绝不
  反复调用 udisks（频繁 mount 会把系统拖卡）。
  每个挂载点先验可读可写：残留挂载（EIO）与 root 挂载（EACCES）只记录原因并跳过，
  不再被误判为“U 盘断开”；同一卷标有多个挂载点时优先选含指令文件、挂载时间最新的。
- 心跳：每轮检查落 state/usb-agent/heartbeat.json（无 U 盘也写），GUI/--once 据此
  区分“agent 没在跑”与“agent 在跑但 U 盘未挂载/不可读”。
- 跨进程锁：state/usb.lock 覆盖扫描/挂载/指令执行/结果写入，GUI 手点与 agent 不并发。
- 指令：每应用 {app}_cmd.json；认领时原子改名隐藏文件；先写结果再删指令；
  崩在中间由 recover 补写 interrupted；结果写盘失败落本地 pending，下次挂载重试。
  指令 JSON 读不出来属于 IO 错误（EACCES/EIO），保留指令下轮重试，不按损坏处理。
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
HEARTBEAT_NAME = "heartbeat.json"
WAIT_LOCK_ENV = "UVMP_USB_AGENT_WAIT_LOCK"
JOURNAL_ENV = "UVMP_AGENT_JOURNAL"
MOUNT_RETRY_COUNT = 3
MOUNT_RETRY_SLEEP = 5
# 挂载失败后的退避重试（秒）：30 → 60 → 120 → 300 封顶。
# 只限制“连续失败后重试”的频率，不是“一次失败就等拔插”——后者会把服务端
# 取件造成的短暂卸载变成永久不挂载（udisks 不会兜底自动挂载）。
MOUNT_RETRY_BASE = 30
MOUNT_RETRY_MAX = 300


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
    log_path = None
    log_error = None
    try:
        log_path = state.logs_dir() / file_name
        fh = RotatingFileHandler(
            str(log_path),
            maxBytes=LOG_MAX_BYTES,
            backupCount=LOG_BACKUPS,
            encoding="utf-8",
        )
        fh.setFormatter(fmt)
        logger.addHandler(fh)
    except OSError as e:
        # 权限/磁盘问题不能把 agent 变成“看不见”：落 stderr 进 journald，便于排障。
        log_error = e
    stream = None
    if verbose or (not quiet and sys.stdout is not None and sys.stdout.isatty()):
        stream = sys.stdout
    elif os.environ.get(JOURNAL_ENV, "") == "1":
        # systemd 服务显式要求：常规日志同时进 journald，日志文件丢失/被清也能排障
        stream = sys.stderr
    if stream is not None:
        sh = logging.StreamHandler(stream)
        sh.setFormatter(fmt)
        logger.addHandler(sh)
    if log_error is not None:
        if sys.stderr is not None and not any(
            isinstance(h, logging.StreamHandler) for h in logger.handlers
        ):
            sh = logging.StreamHandler(sys.stderr)
            sh.setFormatter(fmt)
            logger.addHandler(sh)
        logger.warning(
            "日志文件不可写（%s: %s），改用 stderr（journalctl -u uvmp-toolkit 可见）",
            log_path or "state/logs",
            log_error,
        )
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


def _read_version() -> str:
    """客户端版本：环境变量优先，冻结包读可执行文件旁的 version.txt（deb postinst 写入）。"""
    from version import read_version

    return read_version()


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
        self.heartbeat_path = self.base / HEARTBEAT_NAME
        self.last_problem = ""
        self.last_problem_at = 0
        self.last_tick_at = ""
        self.version = _read_version()
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
        self.mount_attempted_for = ""
        self.mount_retry_at = 0.0
        self.mount_fail_streak = 0
        self.last_mount_skip_log = 0.0
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
                self.mount_attempted_for = ""
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
        return _arg_int(self.agent_cfg, "poll_sec", 15, 5)

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

    def _note_problem(self, message: str):
        """记录挂载/权限类问题：写 last_error，同一条 10 分钟内只告警一次。"""
        self.last_error = message
        now = time.time()
        if message != self.last_problem or now - self.last_problem_at > 600:
            self.logger.warning("%s", message)
            self.last_problem = message
            self.last_problem_at = now

    def _write_heartbeat(self):
        """每轮检查落心跳：无 U 盘也写，GUI/--once 据此判断 agent 是否在跑。"""
        now = time.time()
        self.last_tick_at = _iso(now)
        if self.mount:
            name = "mounted"
        elif self.device:
            name = "device_unmounted"
        else:
            name = "no_device"
        data = {
            "version": 1,
            "pid": os.getpid(),
            "ts": self.last_tick_at,
            "agent_version": self.version,
            "label": self.label,
            "device": self.device,
            "mount": self.mount,
            "mounted_at": self.mounted_at,
            "readonly": bool(self.readonly),
            "state": name,
            "last_error": self.last_error,
            "last_command": self.last_command,
            "mount_attempted": bool(self.mount_attempted_for),
            "apps": sorted(self.apps),
            "log": str(state.logs_dir() / "usb-agent.log"),
        }
        try:
            state.write_json(self.heartbeat_path, data)
        except OSError as e:
            self.logger.warning("写 agent 心跳失败: %s", e)

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
                        action = "add" if b"ACTION=add" in data else "remove"
                        devname = ""
                        for field in data.split(b"\x00"):
                            if field.startswith(b"DEVNAME="):
                                devname = field.split(b"=", 1)[1].decode(
                                    "utf-8", "replace"
                                )
                                break
                        self.events.put(("udev", time.time(), action, devname))
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

    def _note_udev(self, ev):
        """目标设备的 add/remove 事件 = 新的插入周期，复位“本周期已尝试挂载”标记。

        服务端 collect 会瞬间断开再共享，4s 去抖后的扫描可能看不到中间空档；
        以 udev 事件为准，避免同一个标记把下一轮共享窗口也挡住。
        """
        if not ev or ev[0] != "udev" or len(ev) < 4:
            return
        action, devname = ev[2], ev[3]
        target = self.device.rsplit("/", 1)[-1] if self.device else ""
        if not target or not devname:
            return
        if devname != target and not (
            devname.startswith(target) and devname[len(target) :].isdigit()
        ):
            return
        if self.mount_attempted_for:
            self.logger.debug(
                "目标设备 %s %s，复位挂载尝试标记", action, devname
            )
            self.mount_attempted_for = ""
            self.mount_retry_at = 0.0
            self.mount_fail_streak = 0

    def _debounce(self):
        end = time.time() + self._debounce_sec()
        while time.time() < end and not self.stop.is_set():
            self.stop.wait(min(0.2, max(0.0, end - time.time())))
        while True:
            try:
                ev = self.events.get_nowait()
            except queue.Empty:
                break
            self._note_udev(ev)

    # ---- 主循环 ----

    def _acquire_instance_lock(self):
        """抢单实例锁。

        systemd 服务（UVMP_USB_AGENT_WAIT_LOCK=1）在锁被 GUI 拉起的实例占用时等待，
        而不是直接退出——否则服务会一直停着，GUI 一关就再没人消费指令。
        其他入口（GUI 补拉、--once）抢不到立即退出，避免多实例。
        """
        wait = os.environ.get(WAIT_LOCK_ENV, "") == "1"
        announced = False
        while not self.stop.is_set():
            fd = _try_lock(self.instance_lock_path)
            if fd is not None:
                return fd
            if not wait:
                return None
            if not announced:
                self.logger.info("已有 usb-agent 在运行，等待其退出后接管")
                announced = True
            self.stop.wait(5.0)
        return None

    def run(self) -> int:
        for sig in (signal.SIGTERM, signal.SIGINT):
            try:
                signal.signal(sig, self._on_signal)
            except (ValueError, OSError):
                pass
        fd = self._acquire_instance_lock()
        if fd is None:
            if not self.quiet:
                self.logger.info("已有 usb-agent 在运行，本实例退出")
            return 0
        try:
            self._prune_local(_arg_int(self.agent_cfg, "log_retention_days", 30, 1))
            self.logger.info(
                "usb-agent 启动（卷标=%s，poll=%ds，mounted_poll=%ds，pid=%d）",
                self.label,
                self._poll_sec(),
                self._mounted_poll_sec(),
                os.getpid(),
            )
            self._start_udev()
            self._ensure_cluster()
            while not self.stop.is_set():
                timeout = self._mounted_poll_sec() if self.mount else self._poll_sec()
                try:
                    ev = self.events.get(timeout=timeout)
                    if ev and ev[0] == "stop":
                        break
                    self._note_udev(ev)
                    self._debounce()
                except queue.Empty:
                    pass
                if self.stop.is_set():
                    break
                self._ensure_cluster()
                try:
                    self.tick()
                except Exception as e:  # noqa: BLE001
                    self.logger.exception("本轮检查异常: %s", e)
            self.logger.info("usb-agent 退出")
            return 0
        finally:
            self._stop_cluster()
            _release_lock(fd)

    def _ensure_cluster(self):
        """按 config.json 启停集群服务；失败只记日志，不影响 U 盘指令消费。"""
        try:
            from cluster.service import cluster_service

            cluster_service.set_runtime(self.jobs, lambda: self.apps)
            cluster_service.ensure(self.logger)
        except Exception as e:  # noqa: BLE001
            self.logger.exception("集群服务刷新失败: %s", e)

    def _stop_cluster(self):
        try:
            from cluster.service import cluster_service

            cluster_service.stop()
        except Exception as e:  # noqa: BLE001
            self.logger.exception("集群服务停止失败: %s", e)

    def _on_signal(self, signum, _frame):
        self.logger.info("收到信号 %s，准备退出", signum)
        self.stop.set()
        self.events.put(("stop", time.time()))

    def run_once(self) -> int:
        """手工检查一次（GUI「立即检查」/排障）：不抢单实例锁，只抢设备锁。"""
        self._reload_apps(force=True)
        self.tick(force_mount=True)
        if self.mount:
            print(
                "U盘已就绪: %s（卷标 %s，只读=%s）"
                % (self.mount, self.label, "是" if self.readonly else "否")
            )
        elif self.device:
            print(
                "已发现卷标 %s 的设备 %s，但未挂载（%s）"
                % (self.label, self.device, self.last_error or "挂载中/等待自动挂载")
            )
        else:
            print("未检测到卷标 %s 的U盘（设备不存在）" % self.label)
        print("agent 心跳: %s" % self.heartbeat_path)
        return 0

    # ---- 单轮检查 ----

    def tick(self, force_mount: bool = False):
        """检查一轮。

        挂载的唯一触发条件是“设备出现且未挂载”（每个插入周期一次）：先等桌面自动
        挂载 10s，仍未挂才 udisksctl；失败后本轮周期不再重试。force_mount=True
        （手工 --once/页面检查）会清除本周期标记，允许再试一次。
        """
        self._load_settings()
        self._reload_apps()
        lock = _try_lock(self.device_lock_path)
        if lock is None:
            self.logger.debug("U 盘操作锁被其他进程占用，跳过本轮")
            return
        try:
            if force_mount:
                self.mount_attempted_for = ""
                self.mount_retry_at = 0.0
                self.mount_fail_streak = 0
            self._tick_locked()
        finally:
            self._write_heartbeat()
            _release_lock(lock)

    @staticmethod
    def _mount_mtime(point: str) -> float:
        try:
            return Path(point).stat().st_mtime
        except OSError:
            return 0.0

    def _pick_mount(self, mounts):
        """同名卷标出现多个可用挂载点时的选择。

        优先含本端指令文件的挂载点，其次挂载目录最新的（udisks 每次新挂载建新目录，
        旧目录来自断线残留）。只读/不可读的挂载点在调用前已被过滤。
        """
        if len(mounts) == 1:
            return mounts[0][0], mounts[0][1]

        def has_cmd(point: str) -> bool:
            for app_id in self.apps:
                try:
                    if (Path(point) / usb_protocol.cmd_name(app_id)).is_file():
                        return True
                except OSError:
                    continue
            return False

        with_cmd = [m for m in mounts if has_cmd(m[0])]
        pool = with_cmd or mounts
        chosen = max(pool, key=lambda m: self._mount_mtime(m[0]))
        others = [m[0] for m in mounts if m[0] != chosen[0]]
        if others:
            self.logger.info(
                "同名卷标有 %d 个可用挂载点，选用 %s（忽略 %s）",
                len(mounts),
                chosen[0],
                "、".join(others),
            )
        return chosen[0], chosen[1]

    def _handle_mounted(self, mount: str, readonly: bool, now: float):
        # 挂载成功即复位“本周期已尝试”与退避：之后若挂载点被服务端取件断开
        # （设备仍留在被控机，udev 不会有 remove/add），必须允许下一轮重新挂载。
        self.mount_attempted_for = ""
        self.mount_retry_at = 0.0
        self.mount_fail_streak = 0
        if mount != self.mount:
            self.logger.info(
                "U盘已挂载: %s%s", mount, "（只读）" if readonly else ""
            )
            self.mount = mount
            self.readonly = readonly
            self.mounted_at = _iso(now)
            self.first_seen = 0
            self._write_status(force=True, agent_state="mounted")
        writable, why = platform_utils.mountpoint_writable(mount)
        if self.readonly or not writable:
            self._note_problem(
                "U盘不可写（设备只读=%s；%s），跳过指令执行"
                % (self.readonly, why or "未知原因")
            )
            return
        self.last_error = ""
        self._write_cluster_nodes(mount)
        self._flush_pending(mount)
        self._recover_running(mount)
        self._process_commands(mount)
        self._cleanup_results(mount)
        self._write_status(force=False)

    def _tick_locked(self):
        found = platform_utils.scan_usb_mounts(
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
            self.mount_attempted_for = ""
            self.mount_retry_at = 0.0
            self.mount_fail_streak = 0
            return

        device, mounts = found
        if device != self.device:
            self.logger.info("发现U盘设备: %s", device or "(windows)")
            self.device = device
            self.first_seen = now

        usable = [m for m in mounts if m[2]]
        if mounts and not usable:
            point = mounts[0][0]
            _ok, reason = platform_utils.mountpoint_usable(point)
            self._note_problem(
                "U盘挂载点不可用：%s（%s）。请卸载后以当前桌面用户重新挂载"
                "（root 挂载或断线残留挂载都会导致 agent 读不到指令）" % (point, reason)
            )
            self.mount = ""
            self.readonly = False
            self.mounted_at = ""
            return

        if usable:
            mount, readonly = self._pick_mount(usable)
            self._handle_mounted(mount, readonly, now)
            return

        if self.mount:
            self.logger.info("U盘已卸载（可能服务端正在切换共享）")
            self.mount = ""
            self.readonly = False
            self.mounted_at = ""
            # 服务端取件/切换共享会留下“设备在位但未挂载”的状态：按新周期处理，
            # 允许立即重挂；设备真拔出时由上面的 not found 分支复位。
            self.mount_attempted_for = ""
            self.mount_retry_at = 0.0
            self.mount_fail_streak = 0

        # 连续挂载失败按退避重试（30s→300s）；设备拔出或收到 udev 插拔会复位。
        if self.mount_attempted_for == device and now < self.mount_retry_at:
            if now - self.last_mount_skip_log >= 300:
                self.logger.info(
                    "U盘设备在位但未挂载，%d 秒后重试挂载（上次尝试失败）",
                    max(1, int(self.mount_retry_at - now)),
                )
                self.last_mount_skip_log = now
            return

        grace = _arg_int(self.agent_cfg, "auto_mount_grace_sec", 10, 0)
        if self.first_seen and now < self.first_seen + grace:
            # 宽限内每秒复查一次：桌面自动挂载出现则直接采用，设备消失则放弃本轮
            while time.time() < self.first_seen + grace and not self.stop.is_set():
                self.stop.wait(1.0)
                again = platform_utils.scan_usb_mounts(
                    self.label, lambda m: self.logger.debug("宽限内扫描: %s", m)
                )
                if not again:
                    return
                usable = [m for m in again[1] if m[2]]
                if usable:
                    mount, readonly = self._pick_mount(usable)
                    self._handle_mounted(mount, readonly, time.time())
                    return

        self.mount_attempted_for = device
        self.logger.info("U盘出现且未挂载，尝试挂载: %s", device or "(unknown)")
        mounted = self._attempt_mount(device)

        found = platform_utils.scan_usb_mounts(
            self.label, lambda m: self.logger.debug("挂载后扫描: %s", m)
        )
        if found:
            usable = [m for m in found[1] if m[2]]
            if usable:
                mount, readonly = self._pick_mount(usable)
                self._handle_mounted(mount, readonly, time.time())
                return
        if mounted:
            # 挂载命令成功但扫描仍未见可用挂载点：按失败退避，避免每轮反复 mount。
            self.mount_attempted_for = device
            self.mount_retry_at = time.time() + MOUNT_RETRY_BASE

    def _write_cluster_nodes(self, mount: str):
        """Hub 把已入网节点表写盘根，服务端下次会话读取；非 Hub/未启用时静默跳过。"""
        try:
            from cluster.service import cluster_service

            if cluster_service.write_nodes_file(mount):
                return
        except Exception as e:  # noqa: BLE001
            self.logger.warning("写入集群节点表失败: %s", e)

    def _attempt_mount(self, device: str) -> bool:
        """设备在位但未挂载：一次尝试 3 轮（间隔 5s），失败后按 30s→300s 退避重试。

        挂载成功立即复位“本周期已尝试”标记：挂载点被服务端取件断开、设备仍留在
        被控机时（日志里的“U盘已卸载”），必须允许下一轮重新挂载；否则标记会把
        后续所有尝试永久挡住，而 udisks 并不保证自动挂载兜底。
        """
        self.logger.info("U盘存在但未挂载，尝试挂载: %s", device or "(unknown)")
        reason = ""
        for attempt in range(MOUNT_RETRY_COUNT):
            ok, reason = platform_utils.mount_usb_device(device, self.logger.info)
            if ok:
                self.last_error = ""
                self.mount_attempted_for = ""
                self.mount_retry_at = 0.0
                self.mount_fail_streak = 0
                return True
            if attempt + 1 < MOUNT_RETRY_COUNT:
                time.sleep(MOUNT_RETRY_SLEEP)
        self.mount_fail_streak += 1
        delay = min(
            MOUNT_RETRY_MAX, MOUNT_RETRY_BASE * (2 ** (self.mount_fail_streak - 1))
        )
        self.mount_retry_at = time.time() + delay
        self.last_error = "挂载失败：%s；%d 秒后自动重试" % (reason, delay)
        self.logger.warning(self.last_error)
        return False

    # ---- 指令 ----

    def _process_commands(self, mount: str):
        root = Path(mount)
        for app_id in sorted(self.apps):
            meta, backend = self.apps[app_id]
            path = root / usb_protocol.cmd_name(app_id)
            try:
                if not path.is_file():
                    continue
            except PermissionError as e:
                self._note_problem(
                    "U盘挂载点无权限读取（EACCES，可能被 root 挂载）：%s" % e
                )
                return
            except OSError as e:
                self.logger.warning("检查指令文件失败（U盘可能已断开）: %s", e)
                return
            try:
                self._run_one(app_id, meta, backend, path, mount)
            except PermissionError as e:
                # 单个文件读不了不应吞掉其它应用的指令；保留文件，下轮重试。
                self._note_problem(
                    "U盘指令文件无权限读取（EACCES，可能被 root 挂载）：%s" % e
                )
                continue
            except OSError as e:
                self.logger.warning("执行指令时 U 盘断开（保留指令，下轮重试）: %s", e)
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
        from cluster.service import cluster_service

        route, reason = cluster_service.route_command(cmd)
        if route == "deny":
            res = usb_protocol.build_result(
                cmd,
                False,
                reason,
                data={"relayed": False},
                version=self.version,
                relayed_by=cluster_service.identity.node_id(),
            )
            finished = time.time()
            self.logger.warning("指令拒绝: id=%s %s", cmd["id"], reason)
            self._finish_result(
                mount,
                app_id,
                cmd["id"],
                res,
                running,
                started,
                finished,
                False,
                reason,
            )
            return
        if route == "remote":
            self.logger.info(
                "转发指令: app=%s action=%s id=%s target=%s",
                app_id,
                cmd["action"],
                cmd["id"],
                (cmd.get("target") or {}).get("node_id", ""),
            )
            res, artifacts = cluster_service.relay_command(cmd, mount)
            ok = bool(res.get("ok"))
            error = "" if ok else (res.get("error") or "目标节点执行失败")
            if ok and artifacts:
                try:
                    cluster_service.write_artifacts(mount, artifacts)
                except Exception as e:  # noqa: BLE001
                    res = usb_protocol.build_result(
                        cmd,
                        False,
                        "写入 U 盘产物失败: %s" % e,
                        data=res.get("data") or {},
                        version=self.version,
                        node=str(
                            (cmd.get("target") or {}).get("node_id") or ""
                        ),
                        relayed_by=cluster_service.identity.node_id(),
                    )
                    ok = False
                    error = res.get("error", "")
            if ok and str(cmd.get("action") or "") == "replace":
                cluster_service.finish_replace_source(mount, cmd)
            finished = time.time()
            self._finish_result(
                mount,
                app_id,
                cmd["id"],
                res,
                running,
                started,
                finished,
                ok,
                error,
            )
            return
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
            try:
                cmd = usb_protocol.normalize_command(
                    usb_protocol.read_json(p), app_hint=app_id
                )
            except (OSError, ValueError):
                cmd = None
            if cmd is not None and cmd.get("id") == cmd_id:
                try:
                    from cluster.service import cluster_service

                    route, _reason = cluster_service.route_command(cmd)
                    if route == "remote":
                        recovered = cluster_service.recover_remote(cmd)
                        if recovered:
                            res, artifacts = recovered
                            cluster_service.write_artifacts(mount, artifacts)
                            usb_protocol.write_json_atomic(result_path, res)
                            self._safe_unlink(p)
                            self._append_journal(
                                {
                                    "id": cmd_id,
                                    "app": app_id,
                                    "finished_at": usb_protocol.now_iso(),
                                    "ok": bool(res.get("ok")),
                                    "error": str(res.get("error") or ""),
                                    "result_written": True,
                                    "relayed": True,
                                }
                            )
                            self.logger.info(
                                "已取回转发结果: %s %s", app_id, cmd_id
                            )
                            continue
                except Exception as e:  # noqa: BLE001
                    self.logger.warning("取回转发结果失败（按中断处理）: %s", e)
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


def agent_running() -> bool:
    """单实例锁被占用 = 有 agent 在跑（跨进程、跨平台判定，不依赖心跳时间）。"""
    fd = _try_lock(state.state_dir() / "usb-agent.lock")
    if fd is None:
        return True
    _release_lock(fd)
    return False


def agent_status() -> dict:
    """GUI/排障用：agent 是否在跑 + 最近一次心跳（无 U 盘时也有）。"""
    hb_path = state.state_dir() / "usb-agent" / HEARTBEAT_NAME
    heartbeat = {}
    try:
        data = usb_protocol.read_json(hb_path, max_bytes=16384)
        if isinstance(data, dict):
            heartbeat = data
    except (OSError, usb_protocol.ProtocolError):
        heartbeat = {}
    return {
        "running": agent_running(),
        "heartbeat": heartbeat,
        "heartbeat_path": str(hb_path),
        "log_path": str(state.logs_dir() / "usb-agent.log"),
    }


def run(once: bool = False, verbose: bool = False, quiet: bool = False) -> int:
    agent = UsbAgent(verbose=verbose, quiet=quiet)
    if once:
        return agent.run_once()
    return agent.run()
