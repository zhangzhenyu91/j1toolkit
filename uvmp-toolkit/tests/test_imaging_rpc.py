# -*- coding: utf-8 -*-
"""图片 RPC 与配置测试：同步只读接口、批处理 Job、取消与配置校验。

样本在临时目录生成，状态目录与 config.json 都指向临时目录，不接触真实用户配置。
"""

import io
import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from PIL import Image  # noqa: E402

import config  # noqa: E402
import rpc  # noqa: E402
import state  # noqa: E402
from imaging import engine  # noqa: E402


class ImageRpcTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {
                "LOCALAPPDATA": str(self.tmpdir / "state"),
                "XDG_DATA_HOME": str(self.tmpdir / "state"),
                "UVMP_TOOLKIT_CONFIG": str(self.tmpdir / "config.json"),
            },
        )
        self._env.start()
        self.addCleanup(self._env.stop)
        self.server = rpc.RpcServer()
        self.events = io.StringIO()
        self.server._out = self.events
        self.images = self.tmpdir / "图片"
        self.images.mkdir()

    def make_image(
        self,
        name="照片.jpg",
        size=(80, 60),
        mode="RGB",
        color=(120, 30, 30),
        fmt="JPEG",
    ):
        path = self.images / name
        Image.new(mode, size, color).save(path, fmt)
        return path

    def make_animated_gif(self, name="动画.gif"):
        path = self.images / name
        frames = [Image.new("RGB", (20, 20), c) for c in ((1, 2, 3), (4, 5, 6))]
        frames[0].save(
            path, "GIF", save_all=True, append_images=frames[1:], duration=60
        )
        return path

    def wait_terminal(self, job, timeout=15.0):
        archive = state.jobs_dir() / (job.id + ".json")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if archive.exists() and job._log_fp.closed and job.finished_at:
                time.sleep(0.05)
                return
            time.sleep(0.02)
        self.fail("任务未在 %.0f 秒内收尾" % timeout)

    def read_events(self, buf):
        events = []
        for line in buf.getvalue().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                events.append(json.loads(line))
            except ValueError:
                continue
        return events


class SyncRpcTest(ImageRpcTestCase):
    def test_imageList与imageInspect(self):
        a = self.make_image("甲.jpg", size=(90, 60))
        self.make_image(
            "乙.png", size=(30, 20), mode="RGBA", color=(0, 0, 0, 0), fmt="PNG"
        )
        (self.images / "说明.txt").write_text("忽略", encoding="utf-8")
        listed = self.server.m_imageList({"path": str(self.images)})
        self.assertEqual(str(self.images.resolve()), listed["directory"])
        self.assertEqual(
            ["乙.png", "甲.jpg"], sorted(f["name"] for f in listed["files"])
        )
        info = self.server.m_imageInspect({"path": str(a)})
        self.assertEqual("JPEG", info["format"])
        self.assertEqual((90, 60), (info["width"], info["height"]))
        self.assertEqual(str(a.resolve()), info["path"])

    def test_imagePreview默认与显式max_size(self):
        src = self.make_image("预览.jpg", size=(800, 600))
        out = self.server.m_imagePreview({"path": str(src)})
        self.assertEqual(str(src.resolve()), out["path"])
        self.assertEqual("image/jpeg", out["mime"])
        self.assertEqual((800, 600), (out["width"], out["height"]))
        small = self.server.m_imagePreview({"path": str(src), "max_size": 200})
        self.assertTrue(small["cached"])
        self.assertEqual(200, small["width"])
        with Image.open(small["path"]) as img:
            self.assertEqual((200, 150), img.size)

    def test_参数校验(self):
        src = self.make_image()
        for params in ({}, {"path": ""}, {"path": "相对.jpg"}):
            with self.assertRaises(RuntimeError) as ctx:
                self.server.m_imageList(params)
            self.assertIn("PARAM_INVALID", str(ctx.exception))
            with self.assertRaises(RuntimeError) as ctx:
                self.server.m_imageInspect(params)
            self.assertIn("PARAM_INVALID", str(ctx.exception))
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imagePreview({"path": str(src), "max_size": "大"})
        self.assertIn("PARAM_INVALID", str(ctx.exception))
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imagePreview({"path": str(src), "max_size": True})
        self.assertIn("PARAM_INVALID", str(ctx.exception))
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imageList({"path": str(self.images / "没有")})
        self.assertIn("FILE_NOT_FOUND", str(ctx.exception))
        missing = self.images / "不存在.jpg"
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imageInspect({"path": str(missing)})
        self.assertIn("FILE_NOT_FOUND", str(ctx.exception))
        corrupt = self.images / "坏.jpg"
        corrupt.write_bytes(b"broken")
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imagePreview({"path": str(corrupt)})
        self.assertIn("IMG_DECODE_FAILED", str(ctx.exception))

    def test_preview_max_手改配置越界时钳制(self):
        """手改 config.json 写进越界 preview_max：服务层钳到 512..8192，不能让大图原样直出。"""
        cfg_path = Path(os.environ["UVMP_TOOLKIT_CONFIG"])
        # 长边 9000：钳到 512 与 8192 都会触发缓存，能区分两个方向
        src = self.make_image("超宽.jpg", size=(9000, 200))
        for raw, expected in (
            (100000, engine.CONFIG_PREVIEW_MAX),
            (1, engine.CONFIG_PREVIEW_MIN),
        ):
            cfg_path.write_text(
                json.dumps({"image_tools": {"preview_max": raw}}), encoding="utf-8"
            )
            out = self.server.m_imagePreview({"path": str(src)})
            self.assertTrue(out["cached"], raw)
            self.assertEqual(expected, max(out["width"], out["height"]))
            # 未钳制时（8192 以上直接看原图）会直出，这里必须是缓存路径
            self.assertTrue(out["path"].startswith(str(state.state_dir())), raw)

    def test_preview_max_下限与配置校验一致为512(self):
        """配置钳制范围必须与 config/doc 一致为 512..8192；单次 RPC 的 max_size 仍可 16..8192。"""
        cfg_path = Path(os.environ["UVMP_TOOLKIT_CONFIG"])
        self.assertEqual(512, engine.CONFIG_PREVIEW_MIN)
        self.assertEqual(8192, engine.CONFIG_PREVIEW_MAX)
        self.assertEqual(
            config.IMAGE_PREVIEW_RANGE,
            (engine.CONFIG_PREVIEW_MIN, engine.CONFIG_PREVIEW_MAX),
        )
        # 显式 max_size 的下限仍是 16（渲染层要缩略图），与配置范围互不影响
        self.assertEqual(16, engine.PREVIEW_MIN)
        src = self.make_image("小图.jpg", size=(400, 300))
        cfg_path.write_text(
            json.dumps({"image_tools": {"preview_max": 1}}), encoding="utf-8"
        )
        out = self.server.m_imagePreview({"path": str(src)})
        # 若下限误钳成 16，400 会超过阈值而生成缓存图；钳到 512 才应直出原图
        self.assertFalse(out["cached"])
        self.assertEqual(str(src.resolve()), out["path"])
        self.assertEqual((400, 300), (out["width"], out["height"]))
        # 显式 max_size 不受配置钳制影响
        thumb = self.server.m_imagePreview({"path": str(src), "max_size": 200})
        self.assertTrue(thumb["cached"])
        self.assertEqual(200, thumb["width"])

    def test_preview_max_读取默认值也在范围内(self):
        self.assertTrue(
            engine.CONFIG_PREVIEW_MIN
            <= engine.PREVIEW_DEFAULT
            <= engine.CONFIG_PREVIEW_MAX
        )
        self.assertEqual(2560, engine.PREVIEW_DEFAULT)

    def test_preview_max_非法类型时回退默认(self):
        cfg_path = Path(os.environ["UVMP_TOOLKIT_CONFIG"])
        src = self.make_image("默认.jpg", size=(3000, 2000))
        for raw in ("很大", None, True):
            cfg_path.write_text(
                json.dumps({"image_tools": {"preview_max": raw}}), encoding="utf-8"
            )
            out = self.server.m_imagePreview({"path": str(src)})
            self.assertTrue(out["cached"], raw)
            self.assertEqual(engine.PREVIEW_DEFAULT, max(out["width"], out["height"]))

    def test_imageRename复用rename_one(self):
        src = self.make_image("原名.jpg")

        def fake_rename(path, new_base):
            target = Path(path).with_name(new_base + Path(path).suffix)
            Path(path).rename(target)
            return target

        with mock.patch.object(engine, "_rename_one", side_effect=fake_rename) as fn:
            out = self.server.m_imageRename({"path": str(src), "name": "改名"})
        fn.assert_called_once()
        self.assertEqual("改名", fn.call_args.args[1])
        self.assertEqual("改名.jpg", out["name"])
        self.assertTrue((self.images / "改名.jpg").exists())

    def test_imageRename拒绝非法名称(self):
        src = self.make_image()
        with mock.patch.object(engine, "_rename_one") as fn:
            with self.assertRaises(RuntimeError) as ctx:
                self.server.m_imageRename({"path": str(src), "name": "a/b"})
            self.assertIn("PARAM_INVALID", str(ctx.exception))
        fn.assert_not_called()
        with self.assertRaises(RuntimeError) as ctx:
            self.server.m_imageRename({"path": str(src), "name": ""})
        self.assertIn("PARAM_INVALID", str(ctx.exception))


class ProcessRpcTest(ImageRpcTestCase):
    def test_批处理Job成功失败跳过汇总(self):
        good = self.make_image("好图.jpg", size=(40, 30))
        bad = self.images / "坏图.jpg"
        bad.write_bytes(b"broken")
        gif = self.make_animated_gif()
        outdir = self.tmpdir / "输出"
        out = self.server.m_imageProcess(
            {
                "operation": "convert",
                "files": [str(good), str(bad), str(gif)],
                "options": {"format": "png"},
                "output_dir": str(outdir),
            }
        )
        job = self.server.manager.get(out["job_id"])
        self.assertIsNotNone(job)
        self.assertEqual("image_tools", job.app_id)
        self.wait_terminal(job)
        self.assertEqual("success", job.status)
        result = job.result
        self.assertEqual(1, result["success"])
        self.assertEqual(1, result["failed"])
        self.assertEqual(1, result["skipped"])
        self.assertEqual("convert", result["operation"])
        outputs = [p for p in outdir.iterdir() if not p.name.startswith(".~img-")]
        self.assertEqual(1, len(outputs))
        with Image.open(outputs[0]) as img:
            self.assertEqual("PNG", img.format)

        events = self.read_events(self.events)
        kinds = {e.get("event") for e in events}
        self.assertIn("job.log", kinds)
        self.assertIn("job.progress", kinds)
        self.assertIn("job.done", kinds)
        done = [e for e in events if e.get("event") == "job.done"]
        self.assertEqual(job.id, done[-1]["job_id"])
        self.assertEqual("success", done[-1]["job"]["status"])

    def test_取消后任务为cancelled且未全部写入(self):
        files = [self.make_image("取消%d.jpg" % i, size=(30, 20)) for i in range(6)]
        outdir = self.tmpdir / "输出"
        real = engine._process_one

        def slow(*args, **kwargs):
            time.sleep(0.25)
            return real(*args, **kwargs)

        with mock.patch.object(engine, "_process_one", side_effect=slow):
            out = self.server.m_imageProcess(
                {
                    "operation": "convert",
                    "files": [str(p) for p in files],
                    "options": {"format": "png"},
                    "output_dir": str(outdir),
                }
            )
            job = self.server.manager.get(out["job_id"])
            # 等第一批真正落盘后再取消：已完成的必须保留，未开始的不落盘
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if outdir.exists() and any(
                    not p.name.startswith(".~img-") for p in outdir.iterdir()
                ):
                    break
                time.sleep(0.02)
            self.server.m_cancelJob({"job_id": job.id})
            self.wait_terminal(job)
        self.assertEqual("cancelled", job.status)
        outputs = [p for p in outdir.iterdir() if not p.name.startswith(".~img-")]
        self.assertGreaterEqual(len(outputs), 1)
        self.assertLess(len(outputs), len(files))

    def test_已有任务运行时不重复建任务(self):
        files = [self.make_image("占位%d.jpg" % i, size=(30, 20)) for i in range(4)]
        real = engine._process_one

        def slow(*args, **kwargs):
            time.sleep(0.2)
            return real(*args, **kwargs)

        payload = {
            "operation": "convert",
            "files": [str(p) for p in files],
            "options": {"format": "png"},
            "output_dir": str(self.tmpdir / "输出"),
        }
        with mock.patch.object(engine, "_process_one", side_effect=slow):
            out = self.server.m_imageProcess(payload)
            job = self.server.manager.get(out["job_id"])
            with self.assertRaises(RuntimeError) as ctx:
                self.server.m_imageProcess(payload)
            self.assertIn("已有图片处理任务", str(ctx.exception))
            self.server.m_cancelJob({"job_id": job.id})
            self.wait_terminal(job)
        # 任务收尾后可再建新任务
        again = self.server.m_imageProcess(dict(payload, files=[str(files[0])]))
        self.server.m_cancelJob({"job_id": again["job_id"]})
        self.wait_terminal(self.server.manager.get(again["job_id"]))

    def test_参数非法时不创建任务(self):
        src = self.make_image()
        cases = [
            {"operation": "rotate", "files": [str(src)], "options": {}},
            {"operation": "convert", "files": [], "options": {}},
            {"operation": "convert", "files": "x", "options": {}},
            {"operation": "convert", "files": [str(src)], "options": {"format": "gif"}},
            {
                "operation": "convert",
                "files": [str(src)],
                "options": {},
                "output_dir": "相对",
            },
            {"operation": "resize", "files": [str(src)], "options": {"mode": "pixels"}},
        ]
        for payload in cases:
            with self.assertRaises(RuntimeError, msg=payload) as ctx:
                self.server.m_imageProcess(payload)
            self.assertIn("PARAM_INVALID", str(ctx.exception))
        self.assertEqual(0, len(self.server.manager._jobs))


class ImageConfigTest(ImageRpcTestCase):
    def test_默认配置(self):
        cfg = self.server.m_getConfig({})
        section = cfg["image_tools"]
        self.assertEqual(4, len(section["quick_phrases"]))
        self.assertEqual(2560, section["preview_max"])
        self.assertEqual(2, section["workers"])
        self.assertEqual(
            ["_已核验", "_2026秋检", "_宣传素材", "_终稿"], section["quick_phrases"]
        )
        self.assertEqual(config.DEFAULTS["image_tools"], section)

    def test_保存快捷短语与上限配置(self):
        out = self.server.m_saveConfig(
            {
                "image_tools": {
                    "quick_phrases": ["_已核验", "现场 记录", "_终稿"],
                    "preview_max": 1920,
                    "workers": 1,
                }
            }
        )
        self.assertEqual(
            ["_已核验", "现场 记录", "_终稿"], out["image_tools"]["quick_phrases"]
        )
        self.assertEqual(1920, out["image_tools"]["preview_max"])
        self.assertEqual(1, out["image_tools"]["workers"])
        raw = json.loads(Path(os.environ["UVMP_TOOLKIT_CONFIG"]).read_text("utf-8"))
        self.assertEqual(1920, raw["image_tools"]["preview_max"])

    def test_类型不符被拒绝(self):
        cases = [
            {"image_tools": {"quick_phrases": "_已核验"}},
            {"image_tools": {"quick_phrases": ["_甲", 1]}},
            {"image_tools": {"preview_max": "2560"}},
            {"image_tools": {"workers": True}},
        ]
        for updates in cases:
            with self.assertRaises(RuntimeError, msg=updates):
                self.server.m_saveConfig(updates)

    def test_快捷短语语义校验(self):
        cases = [
            ["_%d" % i for i in range(13)],  # 超过 12 条
            ["长" * 41],  # 超过 40 字
            ["a/b"],  # 文件名非法字符
            [""],  # 空短语
            ["   "],  # 全空白
            ["结尾 "],  # 以空格结尾
        ]
        for phrases in cases:
            with self.assertRaises(RuntimeError, msg=phrases) as ctx:
                self.server.m_saveConfig({"image_tools": {"quick_phrases": phrases}})
            self.assertIn("image_tools.quick_phrases", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
