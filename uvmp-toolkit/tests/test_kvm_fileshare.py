# -*- coding: utf-8 -*-
"""GLKVM 设备端 fileshare.py：save_parts 原子写入与上传临时文件自愈。

设备上无 aiohttp，测试用 sys.modules 注入最小 aiohttp / aiohttp.web 占位后按路径加载
真实 fileshare.py；加载完随即移除占位，避免污染同进程其他测试的模块发现。
媒体目录指向临时目录，multipart 用 fake 流，不连设备。

运行：python3.15 -m unittest discover -s tests -p 'test_kvm_fileshare.py' -v
"""

import asyncio
import importlib.util
import os
import stat
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
FILESHARE = ROOT / "server" / "public" / "kvm-device" / "fileshare.py"

TMP_PREFIX = ".upload-"
TMP_SUFFIX = ".part"


def _install_aiohttp_stub():
    """仅在本机缺 aiohttp 时装占位，返回本次是否注入过。"""
    try:
        import aiohttp  # noqa: F401
    except ImportError:
        pass
    else:
        return False

    class _Router:
        def add_get(self, *a, **k):
            pass

        def add_post(self, *a, **k):
            pass

    class _Application:
        def __init__(self, *a, **k):
            self.router = _Router()

    web = types.ModuleType("aiohttp.web")
    web.json_response = lambda payload=None, **k: payload
    web.FileResponse = object
    web.Application = _Application
    web.run_app = lambda *a, **k: None

    aiohttp = types.ModuleType("aiohttp")
    aiohttp.ClientSession = object
    aiohttp.UnixConnector = object
    aiohttp.web = web

    sys.modules["aiohttp"] = aiohttp
    sys.modules["aiohttp.web"] = web
    return True


def _load_fileshare():
    stubbed = _install_aiohttp_stub()
    try:
        spec = importlib.util.spec_from_file_location("glkvm_fileshare", FILESHARE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module, stubbed
    finally:
        if stubbed:
            sys.modules.pop("aiohttp", None)
            sys.modules.pop("aiohttp.web", None)


fileshare, STUBBED = _load_fileshare()


class FakePart:
    """模拟 aiohttp multipart part：按 chunk 依次返回，末尾返回 b''。"""

    def __init__(self, filename, chunks, fail_at_read=None, on_read=None):
        self.filename = filename
        self._chunks = [bytes(c) for c in chunks]
        self._index = 0
        self._reads = 0
        self._fail_at_read = fail_at_read
        self._on_read = on_read

    async def read_chunk(self, n):
        self._reads += 1
        if self._on_read:
            self._on_read(self._reads)
        if self._fail_at_read is not None and self._reads == self._fail_at_read:
            raise ConnectionError("模拟上传中断")
        if self._index >= len(self._chunks):
            return b""
        chunk = self._chunks[self._index]
        self._index += 1
        return chunk


class FakeMultipart:
    def __init__(self, parts):
        self._parts = list(parts)

    async def next(self):
        return self._parts.pop(0) if self._parts else None


class FakeRequest:
    content_type = "multipart/form-data; boundary=fake"

    def __init__(self, parts):
        self._reader = FakeMultipart(parts)

    async def multipart(self):
        return self._reader


async def _noop():
    return None


async def _not_shared():
    return False


class FileshareTestBase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.media = self._tmp.name
        patcher = mock.patch.object(fileshare, "MEDIA_DIR", self.media)
        patcher.start()
        self.addCleanup(patcher.stop)

    def target(self, name):
        return os.path.join(self.media, name)

    def write(self, name, data):
        with open(self.target(name), "wb") as f:
            f.write(data)

    def read(self, name):
        with open(self.target(name), "rb") as f:
            return f.read()

    def entries(self):
        return sorted(os.listdir(self.media))


class SavePartsTest(FileshareTestBase):
    def test_multi_chunk_upload_writes_full_content_and_real_size(self):
        chunks = [b"abc", b"defgh", b"ij"]
        saved = asyncio.run(
            fileshare.save_parts(FakeRequest([FakePart("a.bin", chunks)])))

        self.assertEqual(saved, [{"name": "a.bin", "size": 10}])
        self.assertEqual(self.read("a.bin"), b"abcdefghij")
        self.assertEqual(self.entries(), ["a.bin"])

    def test_upload_failure_keeps_old_file_and_leaves_no_temp_file(self):
        self.write("a.bin", b"ORIGINAL")

        part = FakePart("a.bin", [b"x" * 10, b"y" * 10], fail_at_read=2)
        with self.assertRaises(ConnectionError):
            asyncio.run(fileshare.save_parts(FakeRequest([part])))

        self.assertEqual(self.read("a.bin"), b"ORIGINAL")
        self.assertEqual(self.entries(), ["a.bin"])

    def test_target_file_untouched_until_upload_succeeds(self):
        self.write("a.bin", b"ORIGINAL")

        seen = []

        def probe(_read_no):
            seen.append(self.read("a.bin"))

        asyncio.run(
            fileshare.save_parts(
                FakeRequest([FakePart("a.bin", [b"new1", b"new2"], on_read=probe)])))

        self.assertTrue(seen)
        self.assertEqual(seen, [b"ORIGINAL"] * len(seen))
        self.assertEqual(self.read("a.bin"), b"new1new2")

    def test_upload_stages_into_temp_file_inside_media_dir(self):
        staged = []

        def probe(_read_no):
            staged.append([n for n in self.entries() if n != "a.bin"])

        asyncio.run(
            fileshare.save_parts(
                FakeRequest([FakePart("a.bin", [b"new1", b"new2"], on_read=probe)])))

        self.assertTrue(any(extra for extra in staged), "写入期间应存在同目录临时文件")
        self.assertEqual(self.entries(), ["a.bin"])

    def test_flush_and_fsync_failure_keeps_old_file_and_cleans_temp(self):
        self.write("a.bin", b"ORIGINAL")

        with mock.patch("os.fsync", side_effect=OSError("模拟 fsync 失败")):
            with self.assertRaises(OSError):
                asyncio.run(
                    fileshare.save_parts(FakeRequest([FakePart("a.bin", [b"new1"])])))

        self.assertEqual(self.read("a.bin"), b"ORIGINAL")
        self.assertEqual(self.entries(), ["a.bin"])

    def test_replace_failure_keeps_old_file_and_cleans_temp(self):
        self.write("a.bin", b"ORIGINAL")

        with mock.patch("os.replace", side_effect=OSError("模拟替换失败")):
            with self.assertRaises(OSError):
                asyncio.run(
                    fileshare.save_parts(FakeRequest([FakePart("a.bin", [b"new1"])])))

        self.assertEqual(self.read("a.bin"), b"ORIGINAL")
        self.assertEqual(self.entries(), ["a.bin"])

    def test_replaced_file_mode_is_0644(self):
        modes = []
        real_chmod = os.chmod

        def spy(path, mode, *a, **k):
            modes.append(mode)
            return real_chmod(path, mode, *a, **k)

        with mock.patch("os.chmod", side_effect=spy):
            asyncio.run(
                fileshare.save_parts(FakeRequest([FakePart("a.bin", [b"new1"])])))

        self.assertEqual(modes, [0o644])
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(os.stat(self.target("a.bin")).st_mode), 0o644)

    def test_chmod_failure_keeps_old_file_and_cleans_temp(self):
        self.write("a.bin", b"ORIGINAL")

        with mock.patch("os.chmod", side_effect=OSError("模拟权限设置失败")):
            with self.assertRaises(OSError):
                asyncio.run(
                    fileshare.save_parts(FakeRequest([FakePart("a.bin", [b"new1"])])))

        self.assertEqual(self.read("a.bin"), b"ORIGINAL")
        self.assertEqual(self.entries(), ["a.bin"])


class StaleTempCleanupTest(FileshareTestBase):
    def make_stale(self, name=TMP_PREFIX + "abc123" + TMP_SUFFIX):
        self.write(name, b"half-uploaded")
        return name

    def test_list_files_skips_upload_temp_files(self):
        self.write("keep.bin", b"ok")
        self.make_stale()

        self.assertEqual([f["name"] for f in fileshare.list_files()], ["keep.bin"])

    def test_status_hides_upload_temp_files(self):
        self.write("keep.bin", b"ok")
        self.make_stale()

        with mock.patch.object(fileshare, "msd_connected", _not_shared):
            payload = asyncio.run(fileshare.status(None))

        self.assertEqual([f["name"] for f in payload["files"]], ["keep.bin"])

    def test_push_cleans_stale_temp_files(self):
        stale = self.make_stale()
        self.write("keep.bin", b"ok")

        with mock.patch.object(fileshare, "ensure_unshared", _noop), \
                mock.patch("os.sync", create=True):
            payload = asyncio.run(
                fileshare.push(FakeRequest([FakePart("new.bin", [b"new1"])])))

        self.assertTrue(payload["ok"])
        self.assertFalse(os.path.exists(self.target(stale)))
        self.assertEqual(self.read("new.bin"), b"new1")
        self.assertEqual(self.entries(), ["keep.bin", "new.bin"])

    def test_startup_cleans_stale_temp_files(self):
        stale = self.make_stale()
        self.write("keep.bin", b"ok")

        class DummyApp:
            def __init__(self, *a, **k):
                self.router = mock.Mock()

        # 本机若已装 aiohttp，真实 run_app 会监听端口：一律替换为桩
        with mock.patch.object(fileshare.web, "Application", DummyApp), \
                mock.patch.object(fileshare.web, "run_app"), \
                mock.patch("logging.basicConfig"):  # 不污染同进程其他测试的日志配置
            with self.assertLogs("fileshare", level="INFO") as logs:
                fileshare.main()

        self.assertFalse(os.path.exists(self.target(stale)))
        self.assertEqual(self.entries(), ["keep.bin"])
        self.assertTrue(any("stale upload temp" in line for line in logs.output))

    @unittest.skipUnless(STUBBED, "本机已安装 aiohttp，无桩可检")
    def test_aiohttp_stub_is_not_left_in_sys_modules(self):
        self.assertNotIn("aiohttp", sys.modules)
        self.assertNotIn("aiohttp.web", sys.modules)


if __name__ == "__main__":
    unittest.main()
