# -*- coding: utf-8 -*-
"""Manifest 断点续传清单的线程安全与原子写测试（Task 2）。

覆盖：多线程并发 put 不丢记录、读取方始终能解析完整 JSON、替换失败向调用方报错、
Windows 短暂占用有限重试。全部使用临时目录，不触碰用户数据。
"""

import json
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

import vehicle_export as ve  # noqa: E402


class ManifestTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self.path = self.tmpdir / "manifest.json"

    def _read(self):
        return json.loads(self.path.read_text(encoding="utf-8"))

    def test_get与has读取最新值(self):
        m = ve.Manifest(self.path)
        self.assertFalse(m.has("pcd", "A"))
        self.assertEqual(m.get("pcd", "A"), "")
        self.assertEqual(m.get("pcd", "A", "缺省"), "缺省")
        m.put("pcd", "A", "01_派车单_A.pdf")
        self.assertTrue(m.has("pcd", "A"))
        self.assertEqual(m.get("pcd", "A"), "01_派车单_A.pdf")
        self.assertFalse(m.has("track", "A"))
        # 落盘内容与内存一致，重开清单可续传
        self.assertEqual(self._read()["pcd"]["A"], "01_派车单_A.pdf")
        again = ve.Manifest(self.path)
        self.assertTrue(again.has("pcd", "A"))
        self.assertEqual(again.get("pcd", "A"), "01_派车单_A.pdf")

    def test_并发put不丢记录且读取方始终读到完整json(self):
        m = ve.Manifest(self.path)
        m.put("track", "seed", "seed.pdf")
        writers, per_writer = 8, 25
        stop = threading.Event()
        read_errors = []
        reads = []

        def reader():
            while not stop.is_set():
                try:
                    text = self.path.read_text(encoding="utf-8")
                except FileNotFoundError:
                    continue
                except PermissionError:
                    # Windows 上原子替换的瞬间可能短暂无法打开；这不是内容损坏，
                    # 读取方重试即可（半截内容会表现为下面的 JSONDecodeError）
                    continue
                try:
                    json.loads(text)
                    reads.append(1)
                except Exception as e:  # noqa: BLE001
                    read_errors.append(e)
                time.sleep(0.001)  # 给原子替换留出时间片；读取仍是高频并发

        rd = threading.Thread(target=reader, name="manifest-reader")
        rd.start()

        def writer(w):
            for i in range(per_writer):
                m.put("pcd", "W%d-%d" % (w, i), "f%d-%d.pdf" % (w, i))

        threads = [
            threading.Thread(target=writer, args=(w,), name="manifest-w%d" % w)
            for w in range(writers)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        stop.set()
        rd.join()

        self.assertEqual(read_errors, [])
        self.assertGreater(len(reads), 0)
        data = self._read()
        self.assertEqual(len(data["pcd"]), writers * per_writer)
        self.assertEqual(len(data["track"]), 1)
        for w in range(writers):
            for i in range(per_writer):
                key = "W%d-%d" % (w, i)
                self.assertIn(key, data["pcd"])
                self.assertEqual(m.get("pcd", key), "f%d-%d.pdf" % (w, i))

    def test_替换失败向调用方报错而不是静默成功(self):
        m = ve.Manifest(self.path)
        attempts = []

        def boom(src, dst):
            attempts.append((src, dst))
            raise PermissionError(13, "文件被占用", str(dst))

        with mock.patch.object(ve.os, "replace", side_effect=boom):
            with self.assertRaises(PermissionError):
                m.put("pcd", "A", "a.pdf")

        # 有重试，但次数有限
        self.assertGreaterEqual(len(attempts), 2)
        self.assertLessEqual(len(attempts), ve.Manifest.WRITE_ATTEMPTS)
        # 失败不静默成功：内存记录回滚，磁盘无新文件，临时文件已清理
        self.assertFalse(m.has("pcd", "A"))
        self.assertEqual(m.get("pcd", "A"), "")
        self.assertFalse(self.path.exists())
        self.assertEqual(list(self.tmpdir.glob("*.tmp")), [])

    def test_短暂占用有限重试后成功(self):
        m = ve.Manifest(self.path)
        real_replace = ve.os.replace
        attempts = []

        def flaky(src, dst):
            attempts.append(1)
            if len(attempts) < 3:
                raise PermissionError(13, "文件被短暂占用", str(dst))
            return real_replace(src, dst)

        with mock.patch.object(ve.os, "replace", side_effect=flaky):
            m.put("track", "A", "a.pdf")

        self.assertEqual(len(attempts), 3)
        self.assertEqual(self._read()["track"]["A"], "a.pdf")
        self.assertEqual(list(self.tmpdir.glob("*.tmp")), [])

    def test_同进程两个实例临时文件名不冲突(self):
        m1 = ve.Manifest(self.path)
        m2 = ve.Manifest(self.path)
        sources = []
        real_replace = ve.os.replace

        def spy(src, dst):
            sources.append(Path(src).name)
            return real_replace(src, dst)

        with mock.patch.object(ve.os, "replace", side_effect=spy):
            m1.put("pcd", "A", "a.pdf")
            m2.put("pcd", "B", "b.pdf")

        self.assertEqual(len(sources), 2)
        self.assertNotEqual(sources[0], sources[1])


if __name__ == "__main__":
    unittest.main()
