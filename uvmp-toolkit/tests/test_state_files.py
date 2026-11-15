# -*- coding: utf-8 -*-
"""state 落盘与清理安全测试。

覆盖审查问题：
- write_json 使用唯一临时名 + fsync，并发写不互踩、不留半截文件
- 状态目录/日志文件权限收紧（0700 / 0600），不依赖 umask
- clear_logs / prune_old_logs 豁免运行中任务日志与 usb-agent 常驻日志
- 历史（history）读写校验 app_id，禁止路径穿越
"""

import json
import os
import pathlib
import stat
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
for _p in (str(ROOT), str(ROOT / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import state  # noqa: E402


class StateTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {"LOCALAPPDATA": str(self.tmpdir), "XDG_DATA_HOME": str(self.tmpdir)},
        )
        self._env.start()
        self.addCleanup(self._env.stop)


class WriteJsonAtomicTest(StateTestCase):
    def test_唯一临时名且写入后fsync(self):
        path = self.tmpdir / "stamps" / "daily.json"
        seen = []

        def fake_replace(src, dst):
            seen.append(Path(src).name)
            os.rename(src, dst)

        with mock.patch.object(state.os, "replace", side_effect=fake_replace), (
            mock.patch.object(state.os, "fsync")
        ) as fsync:
            state.write_json(path, {"a": 1})
        self.assertGreaterEqual(fsync.call_count, 1)
        self.assertTrue(seen[0].endswith(".tmp"))
        self.assertNotEqual("daily.json.tmp", seen[0])
        self.assertEqual({"a": 1}, json.loads(path.read_text(encoding="utf-8")))
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_并发写不产生半截文件(self):
        path = self.tmpdir / "stamps" / "shared.json"
        errors = []

        def worker(i):
            try:
                state.write_json(path, {"i": i, "pad": "x" * 200})
            except Exception as e:  # noqa: BLE001
                errors.append(e)

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(16)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual([], errors)
        self.assertIn("i", json.loads(path.read_text(encoding="utf-8")))

    def test_Windows短暂占用时有限重试后成功(self):
        """Windows 上目标文件被短暂占住会报 PermissionError(13)，保存不能因此失败。"""
        path = self.tmpdir / "stamps" / "shared.json"
        real_replace = os.replace
        attempts = []

        def flaky(src, dst):
            attempts.append(1)
            if len(attempts) < 3:
                raise PermissionError(13, "Access is denied", str(dst))
            return real_replace(src, dst)

        with mock.patch.object(state.os, "replace", side_effect=flaky):
            state.write_json(path, {"ok": True})

        self.assertEqual(3, len(attempts))
        self.assertEqual({"ok": True}, json.loads(path.read_text(encoding="utf-8")))
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_占用不解除时报错且不留临时文件(self):
        """重试用尽仍被占用必须抛给调用方（不能静默当成功），临时文件清理干净。"""
        path = self.tmpdir / "stamps" / "shared.json"
        attempts = []

        def always_busy(src, dst):
            attempts.append(1)
            raise PermissionError(13, "Access is denied", str(dst))

        with mock.patch.object(state.os, "replace", side_effect=always_busy):
            with self.assertRaises(PermissionError):
                state.write_json(path, {"ok": True})

        self.assertEqual(state.REPLACE_ATTEMPTS, len(attempts))
        self.assertFalse(path.exists())
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_读取被短暂占用时重试而不是当作损坏(self):
        """读方与写方并发：替换瞬间 Windows 上可能短暂 PermissionError，
        重试后应读到完整内容；若直接放弃会误判成"没有历史"。"""
        path = self.tmpdir / "stamps" / "shared.json"
        state.write_json(path, {"ok": 1})
        real_read_text = Path.read_text
        calls = []

        def flaky_read_text(self, *args, **kwargs):
            calls.append(1)
            if len(calls) < 3:
                raise PermissionError(13, "Access is denied", str(self))
            return real_read_text(self, *args, **kwargs)

        with mock.patch.object(Path, "read_text", flaky_read_text):
            self.assertEqual({"ok": 1}, state.read_json(path, None))
        self.assertEqual(3, len(calls))


class PathLockTest(StateTestCase):
    """Windows 上并发替换同一目标会返回 WinError 5：写与读必须共用同一把路径锁。

    实测依据（临时目录探针，16 线程 × 200 轮）：只锁 os.replace 仍有 82 次失败；
    读方不持锁时写方仍失败；整段（临时名+fsync+replace）串行且读方同锁才归零。
    """

    def test_读与写共用同一把路径锁(self):
        """确定性证据：主线程持锁时，另一线程的 read_json / write_json 都必须阻塞。

        旧实现读路径不持锁，此断言必然失败（读会立刻返回）。"""
        path = self.tmpdir / "stamps" / "shared_lock.json"
        state.write_json(path, {"v": 1})
        done = []

        def reader():
            state.read_json(path, None)
            done.append("read")

        def writer():
            state.write_json(path, {"v": 2})
            done.append("write")

        with state._locked_path(path):
            threads = [threading.Thread(target=reader), threading.Thread(target=writer)]
            for t in threads:
                t.start()
            for t in threads:
                t.join(timeout=0.3)
            self.assertEqual([], done, "持锁期间读写都必须阻塞（说明共用同一把锁）")
            self.assertTrue(all(t.is_alive() for t in threads), "两个线程都应仍在等待")
        for t in threads:
            t.join(timeout=10)
        self.assertEqual(sorted(["read", "write"]), sorted(done))
        self.assertEqual({"v": 2}, json.loads(path.read_text(encoding="utf-8")))

    def test_并发写与读持续压测不产生错误或半截文件(self):
        """16 写线程各连续写 10 轮 + 2 读线程持续读，同时开始、只在线程结束后 join。

        逐轮 join 会把并发窗口压成每轮一次，抓不到 WinError 5；实测「去锁」对照：
        16 线程 × 10 轮连写时每一轮都出现 PermissionError，加锁后每一轮都是 0。
        """
        path = self.tmpdir / "stamps" / "shared_locked.json"
        state.write_json(path, {"seed": 1})
        errors = []
        stop = threading.Event()
        writers = 16
        per_writer = 10
        start = threading.Barrier(writers + 2 + 1, timeout=30)

        def writer(index):
            try:
                start.wait()
                for round_no in range(per_writer):
                    state.write_json(
                        path, {"i": index, "n": round_no, "pad": "x" * 200}
                    )
            except BaseException as e:  # noqa: BLE001
                errors.append(("writer", index, repr(e)))

        def reader():
            try:
                start.wait()
                while not stop.is_set():
                    state.read_json(path, None)
            except BaseException as e:  # noqa: BLE001
                errors.append(("reader", repr(e)))

        threads = [threading.Thread(target=writer, args=(i,)) for i in range(writers)]
        readers = [threading.Thread(target=reader) for _ in range(2)]
        for t in threads + readers:
            t.start()
        start.wait()
        for t in threads:
            t.join(timeout=120)
        stop.set()
        for t in readers:
            t.join(timeout=30)

        self.assertEqual(
            [], [t.name for t in threads + readers if t.is_alive()], "线程未结束"
        )
        self.assertEqual([], errors, "并发读写不得出现异常（含 WinError 5）")
        payload = json.loads(path.read_text(encoding="utf-8"))
        self.assertIn("i", payload, "最终 JSON 必须完整")
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_替换期间其他线程读不到半截内容(self):
        """验证替换的原子性：读方只会看到「旧完整内容」或「新完整内容」。

        这与加锁无关（`os.replace` 本身原子），加锁解决的是并发替换/读导致的 WinError 5；
        这条用例守住原子性，防止实现退回「先删除再改名」这类会出现空窗的两段式写法。
        """
        path = self.tmpdir / "stamps" / "atomic_window.json"
        state.write_json(path, {"v": "o" * 100})
        observed = []
        errors = []
        stop = threading.Event()
        ready = threading.Event()

        def reader():
            try:
                ready.set()
                while not stop.is_set():
                    value = state.read_json(path, None)
                    if value is not None:
                        observed.append(value)
            except BaseException as e:  # noqa: BLE001
                errors.append(repr(e))

        thread = threading.Thread(target=reader)
        thread.start()
        ready.wait(5)
        for i in range(50):
            state.write_json(path, {"v": "%02d" % i + "n" * 98})
        stop.set()
        thread.join(timeout=30)
        self.assertFalse(thread.is_alive())
        self.assertEqual([], errors)
        self.assertGreater(len(observed), 0, "读线程必须真的读到过内容")
        for item in observed:
            self.assertIsInstance(item, dict, "读到的必须是完整 JSON")
            self.assertEqual(100, len(item["v"]), "JSON 不能是半截")
            self.assertTrue(
                item["v"].startswith("o") or item["v"].endswith("n"), item["v"][:8]
            )

    def test_不同目标之间不互相串行(self):
        """锁按目标路径归并：两个不同文件必须能同时进入临界区（不是全局大锁）。

        用两个 Event 双向确认：先进入的一方等另一方也进入才继续；若锁是全局的，
        先进入者等不到对方，worker 会记录断言失败，主线程据此判红（不再依赖 barrier
        超时被 threading.excepthook 吞掉的旧写法）。
        """
        first = self.tmpdir / "stamps" / "a.json"
        second = self.tmpdir / "stamps" / "b.json"
        entered = {"first": threading.Event(), "second": threading.Event()}
        release = threading.Event()
        errors = []

        def worker(name, other):
            try:
                with state._locked_path(first if name == "first" else second):
                    entered[name].set()
                    if not entered[other].wait(2.0):
                        raise AssertionError("另一个目标被阻塞，说明锁不是按路径归并")
                    release.wait(5.0)
            except BaseException as e:  # noqa: BLE001
                errors.append((name, repr(e)))
                entered[name].set()

        threads = [
            threading.Thread(target=worker, args=("first", "second")),
            threading.Thread(target=worker, args=("second", "first")),
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        release.set()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual([], errors, "两个不同目标必须能同时进入临界区")
        self.assertTrue(entered["first"].is_set() and entered["second"].is_set())


class HistoryDeleteLockTest(StateTestCase):
    """历史删除必须与读/写共用同一把路径锁，且删不掉时不能报「已删 N 条」。"""

    def _seed(self, app_id="app_d", count=2):
        for i in range(count):
            state.append_history(app_id, {"job_id": "20260101000000-%02d" % i})
        return state._history_path(app_id)

    def test_删除在读写持锁时必须等待(self):
        """另一线程持锁时，删除线程不得提前完成。

        注：本条只证明「删除会先拿路径锁」（读历史那一步就持锁），并非 unlink 本身的
        证据——精确证据见 ``test_删除历史文件这一步在路径锁内``。
        """
        path = self._seed(count=1)
        done = []
        errors = []

        def deleter():
            try:
                done.append(state.delete_history_records("app_d", None))
            except BaseException as e:  # noqa: BLE001
                errors.append(repr(e))

        with state._locked_path(path):
            t = threading.Thread(target=deleter)
            t.start()
            t.join(timeout=0.3)
            self.assertEqual([], done, "持锁期间删除必须阻塞")
            self.assertTrue(t.is_alive(), "删除线程应仍在等待锁")
        t.join(timeout=15)
        self.assertEqual([], errors)
        self.assertEqual(1, done[0]["records"])
        self.assertEqual(0, done[0]["skipped"])
        self.assertFalse(path.exists(), "kept 为空时应删除文件本身")

    def test_删除历史文件这一步在路径锁内(self):
        """在「已读完历史、尚未 unlink」的窗口里，其他线程必须取不到同一把锁。

        只验证「删除会读历史」不够——读路径本身就持锁。这里把读挂起在 unlink 之前，
        再由主线程尝试取锁：若 unlink 不在锁内，主线程会立刻拿到锁并暴露缺口。
        """
        path = self._seed(count=1)
        read_done = threading.Event()
        release_read = threading.Event()
        outcome = {}
        real_read = state._read_text

        def hooked_read(p):
            text = real_read(p)
            if pathlib.Path(p) == path:
                read_done.set()
                release_read.wait(10)
            return text

        def deleter():
            try:
                outcome["out"] = state.delete_history_records("app_d", None)
            except BaseException as e:  # noqa: BLE001
                outcome["err"] = repr(e)

        with mock.patch.object(state, "_read_text", hooked_read):
            t = threading.Thread(target=deleter)
            t.start()
            self.assertTrue(read_done.wait(10), "删除线程应已读完历史")
            # 删除线程此刻正准备 unlink；若它持锁，主线程取锁必须超时
            lock = state._path_lock(path)
            acquired = lock.acquire(timeout=0.5)
            if acquired:
                lock.release()
            outcome["acquired"] = acquired
            release_read.set()
            t.join(15)

        self.assertFalse(t.is_alive(), "删除线程未结束")
        self.assertNotIn("err", outcome, outcome.get("err"))
        self.assertFalse(outcome["acquired"], "unlink 这一步必须在路径锁内")
        self.assertEqual(1, outcome["out"]["records"])
        self.assertFalse(path.exists())

    def test_历史文件不可删时保留文件且不计入已删(self):
        """Path.unlink 永久 PermissionError：磁盘保留、skipped=1、records=0。"""
        path = self._seed(count=2)
        before = path.read_text(encoding="utf-8")

        def always_busy(self, *a, **kw):
            raise PermissionError(13, "Access is denied", str(self))

        with mock.patch.object(Path, "unlink", always_busy):
            out = state.delete_history_records("app_d", None)
        self.assertTrue(path.exists(), "删不掉时文件必须保留")
        self.assertEqual(before, path.read_text(encoding="utf-8"), "内容不得被改写")
        self.assertEqual(0, out["records"], "没删掉就不能报已删条数")
        self.assertEqual(1, out["skipped"], "删除失败必须计入 skipped")
        self.assertEqual(0, out["files"])

    def test_保留分支重写失败时同样不计已删且保留原文件(self):
        """kept 非空时 _write_history_lines 失败：records=0、skipped=1、原文件不动。"""
        path = self._seed(count=2)
        before = path.read_text(encoding="utf-8")

        def boom(_path, _lines):
            raise PermissionError(13, "Access is denied", str(_path))

        with mock.patch.object(state, "_write_history_lines", boom):
            out = state.delete_history_records("app_d", ["20260101000000-00"])
        self.assertEqual(0, out["records"])
        self.assertEqual(1, out["skipped"])
        self.assertEqual(before, path.read_text(encoding="utf-8"))

    def test_历史文件读不出来时不得当成全部删除(self):
        """读历史失败（非 FileNotFound）：records=0、skipped=1，文件与内容原样保留。

        旧写法把读失败当成「没有记录」，随后 unlink 会把整份历史删掉。
        """
        path = self._seed(count=2)
        before = path.read_text(encoding="utf-8")

        def broken_read(_p):
            raise PermissionError(13, "Access is denied", str(_p))

        with mock.patch.object(state, "_read_text", broken_read):
            out = state.delete_history_records("app_d", None)
        self.assertTrue(path.exists(), "读不到就不能删")
        self.assertEqual(before, path.read_text(encoding="utf-8"))
        self.assertEqual(0, out["records"])
        self.assertEqual(1, out["skipped"])
        self.assertEqual(0, out["files"])

    def test_删除目标已消失时按完成处理而不是占用失败(self):
        """FileNotFoundError 表示已经没了：幂等完成，不计 skipped。"""
        path = self._seed(count=1)
        real = Path.unlink

        def gone(self, *a, **kw):
            real(self, *a, **kw)
            raise FileNotFoundError(2, "No such file", str(self))

        with mock.patch.object(Path, "unlink", gone):
            out = state.delete_history_records("app_d", ["20260101000000-00"])
        self.assertEqual(1, out["records"], "已消失视为删除完成")
        self.assertEqual(0, out["skipped"], "FileNotFoundError 不是占用失败")
        self.assertFalse(path.exists())
        again = state.delete_history_records("app_d", ["20260101000000-00"])
        self.assertEqual(0, again["records"])
        self.assertEqual(0, again["skipped"])

class PosixSemanticsTest(StateTestCase):
    """非 Windows 语义：POSIX 的 rename 会原子替换已存在的目标、且不因读句柄失败。

    这里用 patch 把进程语义切到「POSIX 分支」不现实，改为验证与平台无关的三条契约：
    ① 目标已存在时原子替换成功（不依赖 unlink）；② 替换后旧内容不再可见、无半截；
    ③ 重试装饰器在非 PermissionError 上不重试（POSIX 语义下 EACCES/ENOENT 行为不同）。
    """

    def test_目标已存在时原子替换仍成功(self):
        path = self.tmpdir / "stamps" / "replace_existing.json"
        state.write_json(path, {"v": 1})
        state.write_json(path, {"v": 2})
        self.assertEqual({"v": 2}, json.loads(path.read_text(encoding="utf-8")))
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )

    def test_重试只针对PermissionError且次数有界(self):
        path = self.tmpdir / "stamps" / "retry_scope.json"
        attempts = []

        def other_error(src, dst):
            attempts.append(1)
            raise OSError(28, "No space left on device", str(dst))

        with mock.patch.object(state.os, "replace", side_effect=other_error):
            with self.assertRaises(OSError):
                state.write_json(path, {"v": 1})
        self.assertEqual(1, len(attempts), "非 PermissionError 不得重试")
        self.assertEqual(
            [], [p.name for p in path.parent.iterdir() if p.name.endswith(".tmp")]
        )


class PermissionsTest(StateTestCase):
    @unittest.skipIf(sys.platform.startswith("win"), "Windows 无 Unix 权限位")
    def test_状态目录与日志文件权限收紧(self):
        state.set_stamp("perm_check", {"date": "2026-01-01"})
        mode = stat.S_IMODE(state.state_dir().stat().st_mode)
        self.assertEqual(0o700, mode)
        stamp = state.state_dir() / "stamps" / "perm_check.json"
        self.assertEqual(0o600, stat.S_IMODE(stamp.stat().st_mode))


class LogCleanupTest(StateTestCase):
    def test_运行中任务与agent日志豁免清理(self):
        logs = state.logs_dir()
        agent = logs / "usb-agent.log"
        agent.write_text("agent\n", encoding="utf-8")
        running = logs / "20260101000000-01.log"
        running.write_text("running\n", encoding="utf-8")
        done_log = logs / "20251231000000-01.log"
        done_log.write_text("done\n", encoding="utf-8")
        state.write_json(state.jobs_dir() / "20251231000000-01.json", {"id": "x"})

        state.register_active_log(running)
        try:
            result = state.clear_logs()
        finally:
            state.unregister_active_log(running)

        self.assertTrue(agent.exists(), "agent 常驻日志不能删")
        self.assertTrue(running.exists(), "运行中任务日志不能删")
        self.assertFalse(done_log.exists())
        self.assertGreaterEqual(result["removed"], 1)
        self.assertGreaterEqual(result["skipped"], 2)

    def test_无档案的新任务日志按运行中保护(self):
        fresh = state.logs_dir() / "20260101000000-02.log"
        fresh.write_text("x\n", encoding="utf-8")
        state.clear_logs()
        self.assertTrue(fresh.exists())

    def test_过期无人认领日志仍会被自洁(self):
        stale = state.logs_dir() / "20250101000000-09.log"
        stale.write_text("x\n", encoding="utf-8")
        old = time.time() - 40 * 86400
        os.utime(stale, (old, old))
        removed = state.prune_old_logs(30)
        self.assertGreaterEqual(removed, 1)
        self.assertFalse(stale.exists())

    def test_agent日志不参与过期自洁(self):
        agent = state.logs_dir() / "usb-agent.log"
        agent.write_text("x\n", encoding="utf-8")
        old = time.time() - 90 * 86400
        os.utime(agent, (old, old))
        state.prune_old_logs(30)
        self.assertTrue(agent.exists())


class HistoryAppIdTest(StateTestCase):
    def test_非法app_id拒绝写入与读取(self):
        for bad in ("../escape", "a/b", "", "a" * 65, "app.json"):
            with self.assertRaises(ValueError):
                state.append_history(bad, {"status": "success"})
            with self.assertRaises(ValueError):
                state.recent_runs(bad, 5)
        self.assertFalse((self.tmpdir / "escape.jsonl").exists())

    def test_合法app_id正常读写(self):
        state.append_history("defect_review", {"job_id": "20260101000000-01"})
        runs = state.recent_runs("defect_review", 5)
        self.assertEqual("20260101000000-01", runs[0]["job_id"])

    def test_并发追加不丢记录(self):
        def worker(i):
            state.append_history("app_x", {"job_id": "20260101000000-%02d" % i})

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)
        self.assertEqual(10, len(state.recent_runs("app_x", 50)))


class JobIdFormatTest(StateTestCase):
    def test_旧任务号仍视为合法(self):
        self.assertTrue(state.is_job_id("20260101000000-01"))
        self.assertTrue(state.is_job_id("20260101000000-99"))
        self.assertTrue(state.is_job_id("20260101000000-abc123"))

    def test_非法任务号拒绝(self):
        for bad in ("20260101000000", "../x", "20260101000000-01.log", "", "x" * 40):
            self.assertFalse(state.is_job_id(bad))

    def test_删除历史仍接受旧任务号(self):
        state.append_history(
            "app_y", {"job_id": "20260101000000-01", "title": "旧任务"}
        )
        out = state.delete_history_records("app_y", ["20260101000000-01"])
        self.assertEqual(1, out["records"])


if __name__ == "__main__":
    unittest.main()
