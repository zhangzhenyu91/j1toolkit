# -*- coding: utf-8 -*-
"""图片引擎测试：格式矩阵、EXIF、缓存、动画、透明、大图、冲突与取消。

全部样本都在临时目录用 Pillow 现场生成，状态目录指向临时目录，不接触真实图片与业务数据。
"""

import builtins
import hashlib
import os
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TOOLKIT = ROOT / "toolkit"
for _p in (str(ROOT), str(TOOLKIT)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from PIL import Image, ImageChops, JpegImagePlugin  # noqa: E402

import state  # noqa: E402
import jobs  # noqa: E402
from imaging import engine  # noqa: E402
from jobs import Cancelled  # noqa: E402


def _gradient_palette_image(width: int, height: int) -> Image.Image:
    """32 色调色板海报：横向渐变 + 纵向渐变 + 棋盘块，缩放时最容易暴露索引插值伪色。"""
    palette = []
    for index in range(32):
        palette += [(index * 8) % 256, (255 - index * 7) % 256, (index * 13) % 256]
    img = Image.new("P", (width, height))
    img.putpalette(palette + [0] * (768 - len(palette)))
    pixels = img.load()
    for x in range(width):
        for y in range(height):
            stripe = (x * 31) // width
            band = (y * 7) // height
            block = 8 if (x // 16 + y // 16) % 2 else 0
            pixels[x, y] = (stripe + band + block) % 32
    return img


def _max_channel_diff(left: Image.Image, right: Image.Image) -> int:
    """两张同尺寸图的最大单通道差值（0..255）。"""
    diff = ImageChops.difference(left.convert("RGB"), right.convert("RGB"))
    return max(high for _low, high in diff.getextrema())


def _real_path(raw) -> str:
    """规范化真实路径：Windows 8.3 短名（如 RUNNER~1）与长名指向同一文件却字面不同。

    引擎对所有输入做 realpath，测试本地 Path 保留原拼写，CI 的 TEMP 恰好是短名形式；
    直接比字符串会把同一份成品判成两份，因此统一按真实路径比较。
    """
    return os.path.normcase(os.path.realpath(str(raw)))


def _same_file(left, right) -> bool:
    """按实体身份判断是否同一文件：规范化路径仍不可用（已被删等）时退化为路径比较。"""
    try:
        return os.path.samefile(str(left), str(right))
    except OSError:
        return _real_path(left) == _real_path(right)


def _wait_until(predicate, timeout: float = 10.0, interval: float = 0.005) -> bool:
    """有界条件等待：条件真正成立后才返回，替代固定 sleep；超时返回 False 交由断言暴露。"""
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() >= deadline:
            return False
        time.sleep(interval)
    return True


class FakeJob:
    """引擎只依赖 Job 的 log/set_progress/check_cancel。"""

    def __init__(self, cancel_after=None):
        self.logs = []
        self.progress = []
        self._cancel_after = cancel_after
        self._checks = 0

    def log(self, msg):
        self.logs.append(msg)

    def set_progress(self, current, total, label=""):
        self.progress.append((current, total, label))

    def check_cancel(self):
        self._checks += 1
        if self._cancel_after and self._checks >= self._cancel_after:
            raise Cancelled()


class ImagingTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmpdir = Path(self._tmp.name)
        self._env = mock.patch.dict(
            os.environ,
            {
                "LOCALAPPDATA": str(self.tmpdir / "state"),
                "XDG_DATA_HOME": str(self.tmpdir / "state"),
            },
        )
        self._env.start()
        self.addCleanup(self._env.stop)
        self.images = self.tmpdir / "图片"
        self.images.mkdir()

    def assertSameOutputs(self, disk_paths, row_outputs, msg=None):
        """结果 rows 与磁盘成品一一对应：先比规范化真实路径，再逐对核对文件实体。"""
        self.assertEqual(len(disk_paths), len(row_outputs), msg)
        self.assertEqual(
            sorted(_real_path(p) for p in disk_paths),
            sorted(_real_path(p) for p in row_outputs),
            msg,
        )
        ordered = sorted(row_outputs, key=_real_path)
        for left, right in zip(ordered, sorted(disk_paths, key=_real_path)):
            self.assertTrue(
                _same_file(left, right), "%s 与 %s 不是同一文件" % (left, right)
            )

    # ---------------- 样本 ----------------
    def make_jpeg(
        self, name="照片.jpg", size=(120, 80), color=(200, 30, 30), orientation=0
    ):
        path = self.images / name
        img = Image.new("RGB", size, color)
        if orientation:
            exif = Image.Exif()
            exif[0x0112] = orientation
            img.save(path, "JPEG", quality=92, exif=exif)
        else:
            img.save(path, "JPEG", quality=92)
        return path

    def make_png(self, name="透明.png", size=(64, 48)):
        path = self.images / name
        img = Image.new("RGBA", size, (10, 120, 220, 255))
        for x in range(size[0] // 2):
            for y in range(size[1] // 2):
                img.putpixel((x, y), (0, 0, 0, 0))
        img.save(path, "PNG")
        return path

    def make_webp(self, name="现场.webp", size=(80, 60)):
        path = self.images / name
        Image.new("RGB", size, (30, 160, 90)).save(path, "WEBP", lossless=True)
        return path

    def make_bmp(self, name="标识.bmp", size=(70, 50)):
        path = self.images / name
        Image.new("RGB", size, (90, 90, 200)).save(path, "BMP")
        return path

    def make_tiff(self, name="标识牌.tif", size=(96, 64), mode="RGB"):
        path = self.images / name
        color = (20, 140, 40) if mode == "RGB" else (0, 255, 0, 0)
        Image.new(mode, size, color).save(path, "TIFF")
        return path

    def make_animated_gif(self, name="班前会.gif", size=(40, 30)):
        path = self.images / name
        frames = [
            Image.new("RGB", size, (200, 0, 0)),
            Image.new("RGB", size, (0, 0, 200)),
        ]
        frames[0].save(
            path, "GIF", save_all=True, append_images=frames[1:], duration=80
        )
        return path

    def make_corrupt(self, name="损坏.jpg"):
        path = self.images / name
        path.write_bytes(b"not-an-image-at-all")
        return path


class ListAndInspectTest(ImagingTestCase):
    def test_格式矩阵与列表字段(self):
        self.make_jpeg()
        self.make_png()
        self.make_webp()
        self.make_bmp()
        self.make_tiff()
        self.make_animated_gif()
        ico = self.images / "图标.ico"
        Image.new("RGBA", (32, 32), (1, 2, 3, 255)).save(ico, "ICO")
        avif = self.images / "新格式.avif"
        Image.new("RGB", (40, 30), (5, 6, 7)).save(avif, "AVIF")

        out = engine.list_images(str(self.images))
        self.assertEqual(str(self.images.resolve()), out["directory"])
        by_name = {item["name"]: item for item in out["files"]}
        self.assertEqual(8, len(by_name))

        expected_formats = {
            "照片.jpg": "JPEG",
            "透明.png": "PNG",
            "现场.webp": "WEBP",
            "标识.bmp": "BMP",
            "标识牌.tif": "TIFF",
            "班前会.gif": "GIF",
            "图标.ico": "ICO",
            "新格式.avif": "AVIF",
        }
        for name, fmt in expected_formats.items():
            item = by_name[name]
            self.assertEqual(fmt, item["format"], name)
            self.assertTrue(item["supported"], name)
            self.assertTrue(item["path"].endswith(name))
            self.assertEqual(Path(name).suffix.lower(), item["ext"])
            self.assertGreater(item["size"], 0)
            self.assertRegex(item["mtime_text"], r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$")
            self.assertGreater(item["width"], 0)
            self.assertGreater(item["height"], 0)
        self.assertTrue(by_name["班前会.gif"]["animated"])
        self.assertFalse(by_name["透明.png"]["animated"])
        self.assertEqual("image/png", by_name["透明.png"]["mime"])

    def test_自然排序且不递归(self):
        for name in ("图10.jpg", "图2.jpg", "图1.jpg"):
            self.make_jpeg(name, size=(10, 10))
        sub = self.images / "子目录"
        sub.mkdir()
        Image.new("RGB", (10, 10)).save(sub / "子图.jpg", "JPEG")
        out = engine.list_images(str(self.images))
        self.assertEqual(
            ["图1.jpg", "图2.jpg", "图10.jpg"], [f["name"] for f in out["files"]]
        )
        # 传入单张图片时按所在目录列
        single = engine.list_images(str(self.images / "图1.jpg"))
        self.assertEqual(str(self.images.resolve()), single["directory"])
        self.assertEqual(3, len(single["files"]))

    def test_损坏文件标记不支持(self):
        bad = self.make_corrupt()
        out = engine.list_images(str(self.images))
        item = out["files"][0]
        self.assertFalse(item["supported"])
        self.assertFalse(item["animated"])
        self.assertEqual("", item["format"])
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.inspect_image(str(bad))
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)
        self.assertIn("损坏", str(ctx.exception))

    def test_inspect只读头部不加载像素(self):
        """截断到只保留 SOF 之前的头部：inspect 与原生预览只读头，像素失败由显示端报告。"""
        full = self.make_jpeg(size=(400, 300))
        raw = full.read_bytes()
        sof = min(
            pos for pos in (raw.find(b"\xff\xc0"), raw.find(b"\xff\xc2")) if pos > 0
        )
        cut = sof + 2 + int.from_bytes(raw[sof + 2 : sof + 4], "big")
        truncated = self.images / "半截.jpg"
        truncated.write_bytes(raw[:cut])
        info = engine.inspect_image(str(truncated))
        self.assertEqual(400, info["width"])
        self.assertEqual(300, info["height"])
        out = engine.preview_image(str(truncated), max_size=2560)
        self.assertEqual(str(truncated.resolve()), out["path"])
        self.assertFalse(out["cached"])

    def test_超过像素与体积上限(self):
        path = self.make_jpeg(size=(100, 80))
        with mock.patch.object(engine, "MAX_PIXELS", 1000):
            info = engine.inspect_image(str(path))
            self.assertTrue(info["too_large"])
            with self.assertRaises(engine.ImagingError) as ctx:
                engine.preview_image(str(path), max_size=2560)
            self.assertEqual("IMG_TOO_LARGE", ctx.exception.code)
        with mock.patch.object(engine, "MAX_BYTES", 10):
            with self.assertRaises(engine.ImagingError) as ctx:
                engine.preview_image(str(path), max_size=2560)
            self.assertEqual("IMG_TOO_LARGE", ctx.exception.code)

    def test_路径参数校验(self):
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.list_images("相对目录")
        self.assertEqual("PARAM_INVALID", ctx.exception.code)
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.list_images(str(self.tmpdir / "不存在"))
        self.assertEqual("FILE_NOT_FOUND", ctx.exception.code)


class PreviewTest(ImagingTestCase):
    def test_原生格式直出(self):
        path = self.make_png()
        out = engine.preview_image(str(path), max_size=2560)
        self.assertEqual(str(path.resolve()), out["path"])
        self.assertEqual("image/png", out["mime"])
        self.assertEqual((64, 48), (out["width"], out["height"]))
        self.assertFalse(out["animated"])
        self.assertFalse(out["cached"])
        self.assertEqual(out["path"], str(Path(out["path"])))

    def test_原生静态直出不重复加载像素(self):
        """Chromium 负责的静态原图，Python 不得再为校验完整解码一次。"""
        path = self.make_jpeg("单次解码.jpg", size=(320, 240))
        original_load = Image.Image.load
        loads = []

        def counted_load(img):
            loads.append(img.size)
            return original_load(img)

        with mock.patch.object(Image.Image, "load", counted_load):
            out = engine.preview_image(str(path), max_size=2560)
        self.assertEqual(str(path.resolve()), out["path"])
        self.assertEqual([], loads)

    def test_常见大图在原生像素预算内直接解码(self):
        """默认预览档下，不超过 32MP 的浏览器原生图不再多走一次 Python 缓存编码。"""
        path = self.make_jpeg("原生大图.jpg", size=(4000, 3000))
        original_load = Image.Image.load
        loads = []

        def counted_load(img):
            loads.append(img.size)
            return original_load(img)

        with mock.patch.object(Image.Image, "load", counted_load):
            out = engine.preview_image(str(path), max_size=2560)
        self.assertEqual(str(path.resolve()), out["path"])
        self.assertFalse(out["cached"])
        self.assertEqual([], loads)

    def test_超出原生像素预算仍生成缓存(self):
        path = self.make_jpeg("超预算原生图.jpg", size=(4000, 3000))
        with mock.patch.object(engine, "NATIVE_PREVIEW_MAX_PIXELS", 1000):
            out = engine.preview_image(str(path), max_size=2560)
        self.assertTrue(out["cached"])
        self.assertEqual(2560, out["width"])

    def test_EXIF方向_原生直出报告显示尺寸(self):
        path = self.make_jpeg(size=(120, 80), orientation=6)
        out = engine.preview_image(str(path), max_size=2560)
        self.assertEqual(str(path.resolve()), out["path"])
        self.assertEqual((80, 120), (out["width"], out["height"]))

    def test_EXIF方向_缩放预览已摆正(self):
        path = self.make_jpeg(size=(120, 80), orientation=6)
        out = engine.preview_image(str(path), max_size=100)
        self.assertTrue(out["cached"])
        self.assertEqual("image/png", out["mime"])
        self.assertEqual(100, max(out["width"], out["height"]))
        self.assertGreater(out["height"], out["width"])
        with Image.open(out["path"]) as img:
            self.assertEqual((out["width"], out["height"]), img.size)
            self.assertTrue(out["path"].startswith(str(state.state_dir())))

    def test_TIFF生成PNG缓存且保留透明(self):
        path = self.make_tiff("透明.tif", mode="RGBA")
        out = engine.preview_image(str(path), max_size=2560)
        self.assertTrue(out["cached"])
        self.assertEqual("image/png", out["mime"])
        with Image.open(out["path"]) as img:
            self.assertEqual("RGBA", img.mode)
            self.assertEqual(0, img.getpixel((5, 5))[3])

    def test_大图生成缓存且不超过max_size(self):
        path = self.make_jpeg("大图.jpg", size=(3000, 2000))
        out = engine.preview_image(str(path), max_size=1000)
        self.assertTrue(out["cached"])
        self.assertEqual(1000, out["width"])
        self.assertEqual(667, out["height"])
        with Image.open(out["path"]) as img:
            self.assertEqual((1000, 667), img.size)

    def test_JPEG预览先降采样解码再高质量缩放(self):
        """大图不能先解出全部像素再缩小，draft 必须先于 load，缩放使用递减归约。"""
        path = self.make_jpeg("降采样.jpg", size=(3000, 2000))
        original_draft = JpegImagePlugin.JpegImageFile.draft
        original_load = Image.Image.load
        original_resize = Image.Image.resize
        events = []
        resize_gaps = []

        def watched_draft(img, mode, size):
            events.append(("draft", img.size, size))
            return original_draft(img, mode, size)

        def watched_load(img):
            events.append(("load", img.size))
            return original_load(img)

        def watched_resize(img, size, resample=None, box=None, reducing_gap=None):
            resize_gaps.append(reducing_gap)
            return original_resize(img, size, resample, box, reducing_gap)

        with (
            mock.patch.object(JpegImagePlugin.JpegImageFile, "draft", watched_draft),
            mock.patch.object(Image.Image, "load", watched_load),
            mock.patch.object(Image.Image, "resize", watched_resize),
        ):
            out = engine.preview_image(str(path), max_size=1000)
        self.assertEqual(1000, out["width"])
        self.assertEqual(667, out["height"])
        self.assertTrue(any(event[0] == "draft" for event in events))
        first_load = next(index for index, event in enumerate(events) if event[0] == "load")
        self.assertTrue(any(event[0] == "draft" for event in events[:first_load]))
        self.assertLess(events[first_load][1][0] * events[first_load][1][1], 3000 * 2000)
        self.assertIn(2.0, resize_gaps)

    def test_预览缓存不做额外PNG优化(self):
        """临时预览只写一次常规 PNG，不为压缩率再跑 optimize 编码 pass。"""
        path = self.make_jpeg("快速缓存.jpg", size=(800, 600))
        original_save = Image.Image.save
        png_options = []

        def watched_save(img, fp, format=None, **params):
            if str(fp).endswith(".tmp") and format == "PNG":
                png_options.append(params)
            return original_save(img, fp, format, **params)

        with mock.patch.object(Image.Image, "save", watched_save):
            engine.preview_image(str(path), max_size=200)
        self.assertTrue(png_options)
        self.assertFalse(png_options[0].get("optimize", False))

    def test_缓存命中与失效(self):
        path = self.make_jpeg("缓存.jpg", size=(800, 600))
        first = engine.preview_image(str(path), max_size=200)
        second = engine.preview_image(str(path), max_size=200)
        self.assertEqual(first["path"], second["path"])
        self.assertTrue(Path(first["path"]).exists())

        # 内容与 mtime 变化后必须换新缓存，不能返回旧图
        with Image.open(path) as img:
            changed = img.resize((400, 300))
        changed.save(path, "JPEG", quality=90)
        later = time.time() + 5
        os.utime(path, (later, later))
        third = engine.preview_image(str(path), max_size=200)
        self.assertNotEqual(first["path"], third["path"])
        self.assertTrue(Path(third["path"]).exists())
        with Image.open(third["path"]) as img:
            self.assertEqual(200, img.width)

    def test_动画GIF直出并标记animated(self):
        path = self.make_animated_gif(size=(3000, 2000))
        out = engine.preview_image(str(path), max_size=100)
        # 动画不压成首帧：即使超过 max_size 也直出原图
        self.assertEqual(str(path.resolve()), out["path"])
        self.assertTrue(out["animated"])
        self.assertEqual("image/gif", out["mime"])

    def test_损坏文件预览报错(self):
        path = self.make_corrupt("损坏预览.jpg")
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.preview_image(str(path), max_size=2560)
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)

    def test_不承诺格式明确报不支持(self):
        heic = self.images / "宣传栏.heic"
        heic.write_bytes(b"\x00\x00\x00\x18ftypheic")
        for fn in (engine.inspect_image, engine.preview_image):
            with self.assertRaises(engine.ImagingError) as ctx:
                fn(str(heic))
            self.assertEqual("IMG_UNSUPPORTED", ctx.exception.code)
        listed = engine.list_images(str(self.images))
        item = listed["files"][0]
        self.assertFalse(item["supported"])
        self.assertEqual("", item["format"])

    def test_不同max_size使用不同缓存(self):
        path = self.make_jpeg("多档.jpg", size=(1200, 900))
        small = engine.preview_image(str(path), max_size=200)
        large = engine.preview_image(str(path), max_size=400)
        self.assertNotEqual(small["path"], large["path"])
        self.assertEqual(200, small["width"])
        self.assertEqual(400, large["width"])

    def test_预览16位与P模式不毁图(self):
        """缓存预览不能把 16 位整图拉白；P 模式要先展开成 RGB 再由 LANCZOS 缩放。"""
        i16 = self.images / "十六位预览.png"
        img = Image.new("I;16", (64, 48), 40000)
        img.save(i16, "PNG")
        out = engine.preview_image(str(i16), max_size=32)
        self.assertTrue(out["cached"])
        with Image.open(out["path"]) as cache:
            # 预览缓存统一 8 位灰度：既不是 16 位（渲染层不支持），也不是 RGB（白图）
            self.assertEqual("L", cache.mode)
            self.assertEqual(156, cache.getpixel((2, 2)))

        p = self.images / "索引预览.png"
        idx = Image.new("P", (64, 48), 1)
        idx.putpalette([0, 0, 0, 255, 0, 0] + [0] * 762)
        idx.save(p, "PNG")
        out = engine.preview_image(str(p), max_size=32)
        with Image.open(out["path"]) as cache:
            # 预览缩小时先展开调色板再 LANCZOS，避免索引插值伪色
            self.assertEqual("RGB", cache.mode)
            self.assertEqual((255, 0, 0), cache.getpixel((2, 2)))

    def test_预览不支持的色彩模式用模式错误码(self):
        """模式表达不了 ≠ 格式不支持：错误码必须是 IMG_MODE_UNSUPPORTED。"""
        path = self.images / "印刷色.tif"
        Image.new("LAB", (32, 24), (128, 128, 128)).save(path, "TIFF")
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.preview_image(str(path), max_size=32)
        self.assertEqual("IMG_MODE_UNSUPPORTED", ctx.exception.code)
        self.assertIn("LAB", str(ctx.exception))

    def test_预览浮点TIFF可显示且不报错(self):
        path = self.images / "浮点.tif"
        Image.new("F", (32, 24), 0.5).save(path, "TIFF")
        out = engine.preview_image(str(path), max_size=16)
        self.assertTrue(out["cached"])
        with Image.open(out["path"]) as cache:
            self.assertEqual("L", cache.mode)
            self.assertEqual(127, cache.getpixel((1, 1)))

    def test_旧版本缓存键不会被复用(self):
        """缓存键必须含算法版本：旧版本（P 模式直接插值）留下的缓存不能被继续命中。

        用当前版本的键算法伪造一份「颜色被插值污染」的旧缓存，再确认预览重新生成。
        """
        src = self.images / "版本缓.png"
        img = _gradient_palette_image(128, 96)
        img.save(src, "PNG")
        st = src.stat()
        size_limit = 64
        old_key = "%s|%d|%d|%d" % (
            src.resolve(),
            st.st_mtime_ns,
            st.st_size,
            size_limit,
        )
        old_digest = hashlib.sha256(old_key.encode("utf-8")).hexdigest()[:32]
        legacy = engine._cache_dir() / (old_digest + ".png")
        poisoned = img.resize((64, 48), Image.Resampling.LANCZOS)  # 旧算法：保留 P 索引
        poisoned.save(legacy, "PNG")
        self.assertTrue(legacy.exists())

        out = engine.preview_image(str(src), max_size=size_limit)
        self.assertTrue(out["cached"])
        self.assertNotEqual(str(legacy), out["path"], "旧版本键不能被当作当前缓存")
        reference = img.convert("RGB").resize((64, 48), Image.Resampling.LANCZOS)
        with Image.open(out["path"]) as cache:
            self.assertLessEqual(_max_channel_diff(cache, reference), 2)
        self.assertGreater(
            _max_channel_diff(Image.open(legacy), reference),
            8,
            "伪造的旧缓存应确有偏色",
        )

    def test_缓存键包含算法版本(self):
        src = self.make_jpeg("版本键.jpg", size=(300, 200))
        st = src.stat()
        current = engine._cache_path(src.resolve(), st, 128)
        with mock.patch.object(engine, "CACHE_VERSION", engine.CACHE_VERSION + 1):
            bumped = engine._cache_path(src.resolve(), st, 128)
        self.assertNotEqual(current, bumped, "版本变化必须换缓存文件")
        self.assertEqual(3, engine.CACHE_VERSION)

    def test_原生格式直出mime(self):
        cases = [
            (self.make_webp(), "image/webp"),
            (self.make_bmp(), "image/bmp"),
        ]
        ico = self.images / "图标.ico"
        Image.new("RGBA", (32, 32), (1, 2, 3, 255)).save(ico, "ICO")
        cases.append((ico, "image/x-icon"))
        for path, mime in cases:
            out = engine.preview_image(str(path), max_size=2560)
            self.assertEqual(str(path.resolve()), out["path"])
            self.assertEqual(mime, out["mime"])
            self.assertFalse(out["cached"])

    def test_动画预览逐帧校验(self):
        """动画直出也要真正解码：首帧与后续帧都过一遍，不能只信文件头。"""
        path = self.make_animated_gif("完整动画.gif", size=(60, 40))
        out = engine.preview_image(str(path), max_size=2560)
        self.assertTrue(out["animated"])
        # 把一个中间帧改成坏数据：逐帧校验必须发现（只解首帧的实现会漏过）
        raw = path.read_bytes()
        broken = self.images / "坏帧.gif"
        broken.write_bytes(raw[: len(raw) // 2])
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.preview_image(str(broken), max_size=2560)
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)

    def test_截断动画第一帧也必须报错(self):
        path = self.make_animated_gif("截断.gif", size=(60, 40))
        raw = path.read_bytes()
        broken = self.images / "截断尾部.gif"
        broken.write_bytes(raw[:-40])
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.preview_image(str(broken), max_size=2560)
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)

    def test_动画WebP同样逐帧校验(self):
        path = self.images / "动画.webp"
        frames = [Image.new("RGB", (48, 32), c) for c in ((10, 20, 30), (40, 50, 60))]
        frames[0].save(
            path, "WEBP", save_all=True, append_images=frames[1:], duration=60
        )
        out = engine.preview_image(str(path), max_size=2560)
        self.assertTrue(out["animated"])
        self.assertEqual("image/webp", out["mime"])

        broken = self.images / "截断.webp"
        broken.write_bytes(path.read_bytes()[: len(path.read_bytes()) // 2])
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.preview_image(str(broken), max_size=2560)
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)

    def test_动画超预算时降级为首帧加末帧仍能发现尾部截断(self):
        """帧数 × 单帧像素超预算时只解首帧与末帧：尾部截断仍必须报错。"""
        path = self.make_animated_gif("长动画.gif", size=(60, 40))
        with mock.patch.object(engine, "ANIMATION_VALIDATE_PIXELS", 1):
            valid = engine.preview_image(str(path), max_size=2560)
        self.assertTrue(valid["animated"])
        raw = path.read_bytes()
        broken = self.images / "长动画截断.gif"
        broken.write_bytes(raw[:-40])
        with mock.patch.object(engine, "ANIMATION_VALIDATE_PIXELS", 1):
            with self.assertRaises(engine.ImagingError) as ctx:
                engine.preview_image(str(broken), max_size=2560)
        self.assertEqual("IMG_DECODE_FAILED", ctx.exception.code)


class ProcessTest(ImagingTestCase):
    def _spec(self, operation, files, options=None, output_dir=""):
        return engine.normalize_process(
            operation, [str(p) for p in files], options or {}, output_dir
        )

    def test_转换_JPEG到PNG与WebP(self):
        src = self.make_jpeg()
        outdir = self.tmpdir / "输出"
        spec = self._spec("convert", [src], {"format": "png"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"])
        target = Path(result["rows"][0]["output"])
        self.assertEqual(outdir.resolve(), target.parent)
        self.assertEqual(".png", target.suffix)
        with Image.open(target) as img:
            self.assertEqual("PNG", img.format)
            self.assertEqual((120, 80), img.size)

        spec = self._spec(
            "convert", [src], {"format": "webp", "quality": 70}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"])
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual("WEBP", img.format)

    def test_转换_透明PNG填白与填灰与保留(self):
        src = self.make_png()
        outdir = self.tmpdir / "输出"

        spec = self._spec(
            "convert", [src], {"format": "jpeg", "transparent": "white"}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual("RGB", img.mode)
            self.assertEqual((255, 255, 255), img.getpixel((2, 2)))

        spec = self._spec(
            "convert", [src], {"format": "jpeg", "transparent": "gray"}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual((247, 248, 250), img.getpixel((2, 2)))

        spec = self._spec(
            "convert", [src], {"format": "png", "transparent": "keep"}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual("RGBA", img.mode)
            self.assertEqual(0, img.getpixel((2, 2))[3])

    def test_转换_EXIF方向摆正且不写旧方向(self):
        src = self.make_jpeg(size=(120, 80), orientation=6)
        outdir = self.tmpdir / "输出"
        spec = self._spec("convert", [src], {"format": "png"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        target = Path(result["rows"][0]["output"])
        with Image.open(target) as img:
            self.assertEqual((80, 120), img.size)
            self.assertEqual(0, img.getexif().get(0x0112, 0))

    def test_压缩_JPEG档位与PNG无损(self):
        # 细节丰富的图：不同档位体积应递减
        src = self.images / "细节.jpg"
        img = Image.effect_noise((320, 240), 80).convert("RGB")
        img.save(src, "JPEG", quality=95)
        outdir = self.tmpdir / "输出"
        sizes = {}
        for level in ("high", "balanced", "small"):
            spec = self._spec("compress", [src], {"level": level}, str(outdir))
            result = engine.process_files(FakeJob(), spec)
            self.assertEqual(1, result["success"])
            sizes[level] = Path(result["rows"][0]["output"]).stat().st_size
        self.assertGreater(sizes["high"], sizes["balanced"])
        self.assertGreater(sizes["balanced"], sizes["small"])

        png = self.make_png("无损.png", size=(80, 60))
        spec = self._spec("compress", [png], {"level": "small"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        target = Path(result["rows"][0]["output"])
        with Image.open(png) as before, Image.open(target) as after:
            self.assertEqual("PNG", after.format)
            self.assertEqual(before.mode, after.mode)
            self.assertEqual(before.tobytes(), after.tobytes())

    def test_压缩_WebP三档真的不同(self):
        """WebP 压缩不能落回统一默认质量：三档体积与质量参数都必须分开。"""
        src = self.images / "细节.webp"
        Image.effect_noise((320, 240), 80).convert("RGB").save(
            src, "WEBP", lossless=True
        )
        outdir = self.tmpdir / "输出"
        sizes = {}
        for level in ("high", "balanced", "small"):
            spec = self._spec("compress", [src], {"level": level}, str(outdir))
            self.assertEqual(engine.COMPRESS_LEVELS[level], spec["options"]["quality"])
            result = engine.process_files(FakeJob(), spec)
            self.assertEqual(1, result["success"], result)
            target = Path(result["rows"][0]["output"])
            with Image.open(target) as img:
                self.assertEqual("WEBP", img.format)
                sizes[level] = target.stat().st_size
        self.assertGreater(sizes["high"], sizes["balanced"])
        self.assertGreater(sizes["balanced"], sizes["small"])

    def test_压缩_WebP实际写入质量随档位(self):
        """直接核对写盘参数：WebP 必须带档位质量，不能是默认 88。"""
        src = self.make_webp("质量.webp", size=(60, 40))
        captured = {}
        real_save = Image.Image.save

        def spy(self, fp, fmt=None, **kw):
            if fmt == "WEBP":
                captured.setdefault("q", []).append(kw.get("quality"))
            return real_save(self, fp, fmt, **kw)

        outdir = self.tmpdir / "输出"
        with mock.patch.object(Image.Image, "save", spy):
            for level in ("high", "balanced", "small"):
                spec = self._spec("compress", [src], {"level": level}, str(outdir))
                engine.process_files(FakeJob(), spec)
        self.assertEqual([92, 80, 65], captured.get("q"))

    def test_压缩_PNG不传质量参数(self):
        """PNG 只做无损优化：写盘不能带 quality，否则等于有损重编码。"""
        src = self.make_png("无损参数.png", size=(40, 30))
        seen = []
        real_save = Image.Image.save

        def spy(self, fp, fmt=None, **kw):
            if fmt == "PNG":
                seen.append(dict(kw))
            return real_save(self, fp, fmt, **kw)

        with mock.patch.object(Image.Image, "save", spy):
            spec = self._spec(
                "compress", [src], {"level": "high"}, str(self.tmpdir / "输出")
            )
            engine.process_files(FakeJob(), spec)
        self.assertTrue(seen)
        for kw in seen:
            self.assertNotIn("quality", kw)

    def test_任务描述_PNG压缩写明无损优化(self):
        """PNG 压缩没有质量值：描述里不能编一个 92/80/65 出来。"""
        png = self.make_png("描述.png", size=(20, 20))
        jpg = self.make_jpeg("描述.jpg", size=(20, 20))

        png_spec = self._spec("compress", [png], {"level": "high"})
        text = engine._describe_spec(png_spec)
        self.assertIn("无损优化", text)
        self.assertNotIn("质量", text)
        self.assertNotIn("92", text)

        jpg_spec = self._spec("compress", [jpg], {"level": "high"})
        self.assertEqual("压缩体积 · 质量 92", engine._describe_spec(jpg_spec))

        mixed = self._spec("compress", [png, jpg], {"level": "small"})
        mixed_text = engine._describe_spec(mixed)
        self.assertIn("质量 65", mixed_text)
        self.assertIn("PNG 无损优化", mixed_text)

    def test_任务描述_转换与尺寸不含虚构质量(self):
        png = self.make_png("描述转.png", size=(20, 20))
        spec = self._spec("convert", [png], {"format": "png"})
        self.assertEqual("格式转换 PNG", engine._describe_spec(spec))
        to_jpeg = self._spec("convert", [png], {"format": "jpeg", "quality": 88})
        self.assertEqual("格式转换 JPEG · 质量 88", engine._describe_spec(to_jpeg))
        to_webp = self._spec("convert", [png], {"format": "webp", "quality": 70})
        self.assertEqual("格式转换 WEBP · 质量 70", engine._describe_spec(to_webp))
        resize = self._spec("resize", [png], {"mode": "percent", "percent": 50})
        self.assertEqual("尺寸修改 · percent", engine._describe_spec(resize))

    def test_转换到PNG日志不写质量值(self):
        """PNG 输出无损，日志里不能出现「质量 88」这种无意义（且误导）的数值。"""
        png = self.make_png("转日志.png", size=(20, 20))
        spec = self._spec(
            "convert", [png], {"format": "png"}, str(self.tmpdir / "输出")
        )
        job = FakeJob()
        engine.process_files(job, spec)
        self.assertTrue(job.logs)
        self.assertEqual("开始处理 1 项 · 格式转换 PNG · 并发 2", job.logs[0])
        self.assertNotIn("质量", job.logs[0])

    def test_任务日志首行使用无损描述(self):
        png = self.make_png("日志.png", size=(20, 20))
        spec = self._spec(
            "compress", [png], {"level": "balanced"}, str(self.tmpdir / "输出")
        )
        job = FakeJob()
        engine.process_files(job, spec)
        self.assertTrue(job.logs)
        self.assertIn("PNG 无损优化", job.logs[0])
        self.assertNotIn("质量", job.logs[0])

    def test_压缩_16位PNG保持位深(self):
        src = self.images / "十六位.png"
        img = Image.new("I;16", (32, 24), 1000)
        for x in range(16, 32):
            for y in range(12):
                img.putpixel((x, y), 60000)
        img.save(src, "PNG")
        spec = self._spec(
            "compress", [src], {"level": "balanced"}, str(self.tmpdir / "输出")
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        target = Path(result["rows"][0]["output"])
        with Image.open(src) as before, Image.open(target) as after:
            self.assertEqual("I;16", after.mode, "16 位 PNG 不能被压成 8 位")
            self.assertEqual(before.tobytes(), after.tobytes())
            self.assertNotEqual((255,), tuple({after.getpixel((20, 5))}))

    def test_转换_16位PNG转PNG与JPEG(self):
        src = self.images / "十六位转.png"
        img = Image.new("I;16", (16, 16), 300)
        img.save(src, "PNG")
        outdir = self.tmpdir / "输出"

        spec = self._spec("convert", [src], {"format": "png"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        with Image.open(result["rows"][0]["output"]) as after:
            self.assertEqual("I;16", after.mode)
            self.assertEqual(300, after.getpixel((3, 3)))

        spec = self._spec("convert", [src], {"format": "jpeg"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        with Image.open(result["rows"][0]["output"]) as after:
            self.assertEqual("RGB", after.mode)
            # 300/256 ≈ 1：转 8 位是按 16 位数值缩放，不能一律变白
            self.assertLess(max(after.getpixel((3, 3))), 10)

    def test_尺寸_16位PNG保持位深(self):
        src = self.images / "十六位缩.png"
        Image.new("I;16", (64, 48), 40000).save(src, "PNG")
        spec = self._spec(
            "resize",
            [src],
            {"mode": "long_edge", "long_edge": 32},
            str(self.tmpdir / "输出"),
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        with Image.open(result["rows"][0]["output"]) as after:
            self.assertEqual("I;16", after.mode)
            self.assertEqual((32, 24), after.size)
            self.assertAlmostEqual(40000, after.getpixel((5, 5)), delta=200)

    def test_压缩_P模式仍保持调色板(self):
        """PNG 压缩是无损优化：P 必须原样持有（只有预览与尺寸修改才展开 RGB）。"""
        src = self.images / "索引压缩.png"
        img = Image.new("P", (40, 30), 1)
        palette = [0, 0, 0, 255, 0, 0, 0, 0, 255] + [0] * 759
        img.putpalette(palette)
        img.save(src, "PNG")

        spec = self._spec(
            "compress", [src], {"level": "balanced"}, str(self.tmpdir / "输出")
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        with Image.open(result["rows"][0]["output"]) as after:
            self.assertEqual("P", after.mode)
            self.assertEqual(palette, after.getpalette())
            with Image.open(src) as before:
                self.assertEqual(before.tobytes(), after.tobytes())

    def test_尺寸_P模式先转RGB再缩放以保颜色正确(self):
        """直接对索引做 LANCZOS 会插值索引值，产生调色板里不存在的伪色。"""
        src = self.images / "渐变色板.png"
        img = _gradient_palette_image(128, 96)
        img.save(src, "PNG")

        spec = self._spec(
            "resize",
            [src],
            {"mode": "percent", "percent": 50},
            str(self.tmpdir / "输出"),
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        rows = {row["name"]: row for row in result["rows"]}
        target = Path(rows["渐变色板.png"]["output"])
        with Image.open(target) as after:
            # 允许 RGB/RGBA：颜色正确优先于保留索引
            self.assertIn(after.mode, ("RGB", "RGBA"))
            self.assertEqual((64, 48), after.size)
            actual = after.convert("RGB")
        reference = img.convert("RGB").resize((64, 48), Image.Resampling.LANCZOS)
        self.assertLessEqual(
            _max_channel_diff(actual, reference), 2, "缩放结果应贴合 RGB 参考"
        )
        # 反证：直接对 P 缩放确实会偏色，这个断言保证用例真的有区分力
        naive = img.resize((64, 48), Image.Resampling.LANCZOS).convert("RGB")
        self.assertGreater(_max_channel_diff(naive, reference), 8)

    def test_预览_P模式先转RGB再缩放(self):
        src = self.images / "渐变色板预览.png"
        img = _gradient_palette_image(128, 96)
        img.save(src, "PNG")
        out = engine.preview_image(str(src), max_size=64)
        self.assertTrue(out["cached"])
        with Image.open(out["path"]) as cache:
            self.assertEqual((64, 48), cache.size)
            actual = cache.convert("RGB")
        reference = img.convert("RGB").resize((64, 48), Image.Resampling.LANCZOS)
        self.assertLessEqual(
            _max_channel_diff(actual, reference), 2, "预览不能保留 P 索引缩放结果"
        )
        naive = img.resize((64, 48), Image.Resampling.LANCZOS).convert("RGB")
        self.assertGreater(
            _max_channel_diff(naive, reference), 8, "用例必须能区分朴素 P 缩放"
        )

    def test_不支持的色彩模式明确报错(self):
        """Pillow 写不出的模式（如 RGBA 的 PNG 之外的 I/F 组合）必须报业务错误而非静默毁图。"""
        src = self.images / "浮点.tif"
        Image.new("F", (8, 8), 0.5).save(src, "TIFF")
        spec = self._spec(
            "convert", [src], {"format": "png"}, str(self.tmpdir / "输出")
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["failed"])
        self.assertIn("色彩模式", result["rows"][0]["reason"])
        outdir = self.tmpdir / "输出"
        self.assertFalse(outdir.exists() and any(outdir.iterdir()))

    def test_尺寸_BMP与TIFF输出JPEG并填白(self):
        """已批准设计：尺寸修改时 BMP/TIFF 输出 JPEG，透明区域按白色填充。"""
        outdir = self.tmpdir / "输出"
        bmp = self.make_bmp("缩放.bmp", size=(200, 100))
        tif = self.make_tiff("缩放.tif", size=(200, 100), mode="RGBA")
        spec = self._spec(
            "resize", [bmp, tif], {"mode": "percent", "percent": 50}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(2, result["success"], result)
        for row in result["rows"]:
            target = Path(row["output"])
            self.assertEqual(".jpg", target.suffix)
            with Image.open(target) as img:
                self.assertEqual("JPEG", img.format)
                self.assertEqual((100, 50), img.size)
        with Image.open(Path(result["rows"][1]["output"])) as img:
            self.assertEqual((255, 255, 255), img.getpixel((2, 2)))

    def test_尺寸_WebP与PNG保持原格式(self):
        outdir = self.tmpdir / "输出"
        spec = self._spec(
            "resize",
            [
                self.make_webp("缩放.webp", size=(200, 100)),
                self.make_png("缩放.png", size=(200, 100)),
            ],
            {"mode": "percent", "percent": 50},
            str(outdir),
        )
        result = engine.process_files(FakeJob(), spec)
        formats = []
        for row in result["rows"]:
            with Image.open(row["output"]) as img:
                formats.append(img.format)
        self.assertEqual(["WEBP", "PNG"], formats)

    def test_压缩_BMP与TIFF输出JPEG(self):
        outdir = self.tmpdir / "输出"
        for src in (self.make_bmp(), self.make_tiff()):
            spec = self._spec("compress", [src], {"level": "balanced"}, str(outdir))
            result = engine.process_files(FakeJob(), spec)
            self.assertEqual(1, result["success"], result)
            with Image.open(result["rows"][0]["output"]) as img:
                self.assertEqual("JPEG", img.format)

    def test_尺寸_像素比例与百分比与长边(self):
        src = self.make_jpeg("原始.jpg", size=(800, 600))
        outdir = self.tmpdir / "输出"

        spec = self._spec(
            "resize",
            [src],
            {"mode": "pixels", "width": 400, "keep_ratio": True},
            str(outdir),
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual((400, 300), img.size)

        spec = self._spec(
            "resize", [src], {"mode": "percent", "percent": 50}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual((400, 300), img.size)

        spec = self._spec(
            "resize", [src], {"mode": "long_edge", "long_edge": 200}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual((200, 150), img.size)

    def test_尺寸_不放大时跳过(self):
        src = self.make_jpeg("小图.jpg", size=(100, 80))
        spec = self._spec(
            "resize",
            [src],
            {"mode": "long_edge", "long_edge": 500},
            str(self.tmpdir / "输出"),
        )
        job = FakeJob()
        result = engine.process_files(job, spec)
        self.assertEqual(0, result["success"])
        self.assertEqual(1, result["skipped"])
        self.assertIn("不放大", result["rows"][0]["reason"])
        self.assertFalse(Path(self.tmpdir / "输出").exists())

    def test_冲突自动追加序号且不覆盖(self):
        src = self.make_jpeg("冲突.jpg", size=(30, 20))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        (outdir / "冲突.png").write_bytes(b"old-1")
        (outdir / "冲突 (2).png").write_bytes(b"old-2")
        spec = self._spec("convert", [src], {"format": "png"}, str(outdir))
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"])
        self.assertEqual("冲突 (3).png", Path(result["rows"][0]["output"]).name)
        self.assertEqual(b"old-1", (outdir / "冲突.png").read_bytes())
        self.assertEqual(b"old-2", (outdir / "冲突 (2).png").read_bytes())
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_默认输出到原目录日期子目录(self):
        src = self.make_jpeg("默认.jpg", size=(30, 20))
        spec = self._spec("convert", [src], {"format": "png"})
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"])
        target = Path(result["rows"][0]["output"])
        self.assertEqual(src.parent.resolve(), target.parent.parent)
        self.assertRegex(target.parent.name, r"^processed_\d{8}$")
        self.assertEqual([str(target.parent)], result["output_dirs"])

    def test_动画与不支持格式跳过(self):
        gif = self.make_animated_gif()
        ico = self.images / "图标.ico"
        Image.new("RGBA", (32, 32), (1, 2, 3, 255)).save(ico, "ICO")
        static_gif = self.images / "静态.gif"
        Image.new("RGB", (20, 20), (1, 2, 3)).save(static_gif, "GIF")
        spec = self._spec(
            "convert",
            [gif, ico, static_gif],
            {"format": "jpeg"},
            str(self.tmpdir / "输出"),
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(3, result["skipped"])
        reasons = [row["reason"] for row in result["rows"]]
        self.assertIn("动画图", reasons[0])
        self.assertIn("不在批处理范围", reasons[1])
        self.assertIn("GIF", reasons[2])

    def test_单张损坏不影响其他文件(self):
        good = self.make_jpeg("好图.jpg", size=(40, 30))
        bad = self.make_corrupt("坏图.jpg")
        spec = self._spec(
            "convert", [bad, good], {"format": "png"}, str(self.tmpdir / "输出")
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"])
        self.assertEqual(1, result["failed"])
        self.assertEqual("failed", result["rows"][0]["status"])
        self.assertEqual("success", result["rows"][1]["status"])

    def test_CMYK_TIFF转sRGB(self):
        src = self.make_tiff("印刷.tif", mode="CMYK")
        spec = self._spec(
            "convert", [src], {"format": "jpeg"}, str(self.tmpdir / "输出")
        )
        result = engine.process_files(FakeJob(), spec)
        with Image.open(result["rows"][0]["output"]) as img:
            self.assertEqual("RGB", img.mode)

    def test_并发最多2张(self):
        files = [self.make_jpeg("并%d.jpg" % i, size=(40, 30)) for i in range(6)]
        real = engine._process_one
        lock = threading.Lock()
        active = {"now": 0, "peak": 0}

        def wrapped(*args, **kwargs):
            with lock:
                active["now"] += 1
                active["peak"] = max(active["peak"], active["now"])
            time.sleep(0.05)
            try:
                return real(*args, **kwargs)
            finally:
                with lock:
                    active["now"] -= 1

        spec = self._spec(
            "convert", files, {"format": "png"}, str(self.tmpdir / "输出")
        )
        with mock.patch.object(engine, "_process_one", side_effect=wrapped):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(6, result["success"])
        self.assertEqual(2, active["peak"])

    def test_取消保留已完成且未开始不写入(self):
        files = [self.make_jpeg("取消%d.jpg" % i, size=(40, 30)) for i in range(6)]
        outdir = self.tmpdir / "输出"
        spec = self._spec("convert", files, {"format": "png"}, str(outdir))
        job = FakeJob(cancel_after=2)
        result = engine.process_files(job, spec)
        self.assertTrue(result["cancelled"])
        outputs = [p for p in outdir.iterdir() if not p.name.startswith(".~img-")]
        self.assertGreaterEqual(len(outputs), 1)
        self.assertLessEqual(len(outputs), 2)
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )
        # 结果与磁盘一致：返回的成功项就对应输出目录里的成品（按真实路径与实体身份核对，
        # CI 的 TEMP 可能是 8.3 短名，与引擎 realpath 后的长名指向同一文件）
        success_rows = [row for row in result["rows"] if row["status"] == "success"]
        self.assertSameOutputs(outputs, [row["output"] for row in success_rows])
        self.assertEqual(len(success_rows), result["success"])

    def test_真实JobManager取消后结果保留已完成项且与磁盘一致(self):
        """取消走真实 JobManager：终态必须是 cancelled，result.rows 的成功项 == 磁盘成品。

        取消原先直接把 Cancelled 抛给 _runner，已收集的 rows 全部丢弃，界面按空结果
        显示「已完成 0 项」，而磁盘上其实已有成品。
        """
        manager = jobs.JobManager()
        files = [self.make_jpeg("取消保留%d.jpg" % i, size=(60, 40)) for i in range(6)]
        outdir = self.tmpdir / "取消输出"
        spec = self._spec("convert", files, {"format": "png"}, str(outdir))
        spec["workers"] = 1
        real = engine._process_one
        lock = threading.Lock()
        done = {"n": 0}

        def wrapped(job, spec_, path):
            row = real(job, spec_, path)
            with lock:
                done["n"] += 1
                if done["n"] == 2:
                    job.cancel()
            return row

        job = manager.create("image_tools", "图片工具 · 格式转换")
        with mock.patch.object(engine, "_process_one", side_effect=wrapped):
            manager.run_sync(job, lambda jb: engine.process_files(jb, spec), echo=False)

        self.assertEqual("cancelled", job.status)
        self.assertTrue(job.result.get("cancelled"))
        rows = job.result.get("rows") or []
        success_rows = [row for row in rows if row["status"] == "success"]
        # 已开始的项被等完并计入结果：并发 1 时正好是触发取消前的 2 项
        self.assertEqual(2, len(success_rows), rows)
        self.assertEqual(0, job.result.get("failed"), rows)
        self.assertEqual(2, job.result.get("success"))
        on_disk = sorted(
            str(p) for p in outdir.iterdir() if not p.name.startswith(".~img-")
        )
        self.assertSameOutputs(on_disk, [row["output"] for row in success_rows])
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )
        self.assertEqual([str(outdir.resolve())], job.result.get("output_dirs"))

    def test_取消收尾遇到非取消异常必须记录失败(self):
        """取消收尾时已结束的 future 抛非 Cancelled 异常：不能静默 continue。

        rows 空位上的这一项既没有写出文件、也不是被取消，必须写明确日志并记为
        failed，不能被「已取消」掩护成成功或凭空消失。
        """
        manager = jobs.JobManager()
        files = [self.make_jpeg("收尾%d.jpg" % i, size=(40, 30)) for i in range(3)]
        outdir = self.tmpdir / "收尾输出"
        spec = self._spec("convert", files, {"format": "png"}, str(outdir))
        spec["workers"] = 2
        real = engine._process_one
        waited = {"ok": False}

        def wrapped(job_, spec_, path):
            # 路径按实体身份匹配：CI 的 TEMP 是 8.3 短名，引擎侧已 realpath 成长名
            if _same_file(path, files[1]):
                # 有界条件等待：取消落定且主线程已消费完前一项，异常才确实落在收尾窗口
                waited["ok"] = _wait_until(
                    lambda: job_.cancelled and job_.progress["current"] >= 1
                )
                raise RuntimeError("收尾故障")
            row = real(job_, spec_, path)
            if _same_file(path, files[0]):
                job_.cancel()
            return row

        logs = []
        job = manager.create("image_tools", "图片工具 · 格式转换")
        job.on_log = logs.append
        with mock.patch.object(engine, "_process_one", side_effect=wrapped):
            manager.run_sync(job, lambda jb: engine.process_files(jb, spec), echo=False)

        self.assertEqual("cancelled", job.status)
        self.assertTrue(waited["ok"], "等待取消落定超时：异常没有落在收尾窗口")
        self.assertTrue(job.result.get("cancelled"))
        self.assertEqual(1, job.result.get("success"), job.result)
        self.assertEqual(1, job.result.get("failed"), job.result)
        failed_rows = [row for row in job.result["rows"] if row["status"] == "failed"]
        self.assertEqual(1, len(failed_rows), job.result["rows"])
        self.assertEqual(files[1].name, failed_rows[0]["name"])
        self.assertIn("收尾故障", failed_rows[0]["reason"])
        self.assertTrue(
            any(
                "失败" in line and failed_rows[0]["name"] in line and "收尾故障" in line
                for line in logs
            ),
            logs,
        )

    def test_取消收尾跳过已取消项且不改变普通结果(self):
        """已关闭（cancelled）的 future 仍按规则跳过，不算失败也不写日志。"""
        manager = jobs.JobManager()
        files = [self.make_jpeg("跳过%d.jpg" % i, size=(40, 30)) for i in range(4)]
        outdir = self.tmpdir / "跳过输出"
        spec = self._spec("convert", files, {"format": "png"}, str(outdir))
        spec["workers"] = 1
        real = engine._process_one
        done = {"n": 0}

        def wrapped(job_, spec_, path):
            row = real(job_, spec_, path)
            done["n"] += 1
            if done["n"] == 2:
                job_.cancel()
            return row

        logs = []
        job = manager.create("image_tools", "图片工具 · 格式转换")
        job.on_log = logs.append
        with mock.patch.object(engine, "_process_one", side_effect=wrapped):
            manager.run_sync(job, lambda jb: engine.process_files(jb, spec), echo=False)

        self.assertEqual("cancelled", job.status)
        self.assertEqual(2, job.result.get("success"), job.result)
        self.assertEqual(0, job.result.get("failed"), job.result)
        self.assertFalse(any("收尾时异常" in line for line in logs), logs)

    def test_并发配置为1时首行日志如实写明(self):
        files = [self.make_jpeg("单并发%d.jpg" % i, size=(30, 20)) for i in range(2)]
        spec = self._spec(
            "convert", files, {"format": "png"}, str(self.tmpdir / "单并发输出")
        )
        spec["workers"] = 1
        job = FakeJob()
        result = engine.process_files(job, spec)
        self.assertEqual(2, result["success"], result)
        self.assertEqual("开始处理 2 项 · 格式转换 PNG · 并发 1", job.logs[0])
        self.assertTrue(job.logs[-1].startswith("完成："), job.logs)

    def test_取消不把普通异常吞成取消(self):
        """取消只捕获 jobs.Cancelled：其他异常仍按失败落终态。"""
        manager = jobs.JobManager()
        files = [self.make_jpeg("异常%d.jpg" % i, size=(30, 20)) for i in range(3)]
        outdir = self.tmpdir / "异常输出"
        spec = self._spec("convert", files, {"format": "png"}, str(outdir))

        def boom(job, spec_, path):
            raise RuntimeError("引擎内部故障")

        job = manager.create("image_tools", "图片工具 · 格式转换")
        with mock.patch.object(engine, "_process_one", side_effect=boom):
            manager.run_sync(job, lambda jb: engine.process_files(jb, spec), echo=False)
        self.assertEqual("failed", job.status)
        self.assertIn("引擎内部故障", job.error)

    def test_处理参数校验(self):
        src = self.make_jpeg()
        with self.assertRaises(engine.ImagingError):
            engine.normalize_process("unknown", [str(src)], {}, "")
        with self.assertRaises(engine.ImagingError):
            engine.normalize_process("convert", [], {}, "")
        with self.assertRaises(engine.ImagingError):
            engine.normalize_process("convert", ["相对.jpg"], {}, "")
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.normalize_process("convert", [str(src)], {"format": "gif"}, "")
        self.assertEqual("PARAM_INVALID", ctx.exception.code)
        with self.assertRaises(engine.ImagingError):
            engine.normalize_process(
                "convert", [str(src)], {"format": "png", "quality": 0}, ""
            )
        with self.assertRaises(engine.ImagingError):
            engine.normalize_process("resize", [str(src)], {"mode": "pixels"}, "")


class RenameTest(ImagingTestCase):
    def test_调用rename_one并返回新文件信息(self):
        src = self.make_jpeg("旧名.jpg", size=(30, 20))
        target = self.images / "新名.jpg"

        def fake_rename(path, new_base):
            new_path = Path(path).with_name(new_base + Path(path).suffix)
            Path(path).rename(new_path)
            return new_path

        with mock.patch.object(engine, "_rename_one", side_effect=fake_rename) as fn:
            out = engine.rename_image(str(src), "新名")
        fn.assert_called_once()
        called_path, called_base = fn.call_args.args
        self.assertEqual(src.resolve(), Path(called_path))
        self.assertEqual("新名", called_base)
        self.assertEqual("新名.jpg", out["name"])
        self.assertTrue(target.exists())
        self.assertFalse(src.exists())

    def test_非法名称在调用前拒绝(self):
        src = self.make_jpeg()
        for bad in (
            "",
            "   ",
            "a/b",
            "a\\b",
            "a:b",
            "a*b",
            "a?b",
            'a"b',
            "a<b",
            "a>b",
            "a|b",
            "结尾.",
        ):
            with self.assertRaises(engine.ImagingError, msg=bad) as ctx:
                engine.rename_image(str(src), bad)
            self.assertEqual("PARAM_INVALID", ctx.exception.code)

    def test_同名冲突给出可读错误(self):
        src = self.make_jpeg()
        with mock.patch.object(
            engine, "_rename_one", side_effect=FileExistsError("exists")
        ):
            with self.assertRaises(engine.ImagingError) as ctx:
                engine.rename_image(str(src), "已存在")
        self.assertEqual("IMG_RENAME_CONFLICT", ctx.exception.code)
        self.assertIn("已存在", str(ctx.exception))

    def test_文件不存在(self):
        with self.assertRaises(engine.ImagingError) as ctx:
            engine.rename_image(str(self.images / "没有.jpg"), "新名")
        self.assertEqual("FILE_NOT_FOUND", ctx.exception.code)

    def test_真实改名引擎只改主名且拒绝同名(self):
        """接真实的 fileops.naming.rename_one：只改主名、扩展名保留、同名不覆盖。"""
        try:
            import fileops.naming  # noqa: F401
        except ImportError:
            self.skipTest("通用的通用改名引擎尚未合入")
        src = self.make_jpeg("现场照.jpg", size=(30, 20))
        occupied = self.images / "已占用.jpg"
        self.make_jpeg("已占用.jpg", size=(20, 20))
        occupied_bytes = occupied.read_bytes()

        out = engine.rename_image(str(src), "巡线")
        self.assertEqual("巡线.jpg", out["name"])
        self.assertTrue((self.images / "巡线.jpg").exists())
        self.assertFalse(src.exists())
        self.assertEqual(
            occupied_bytes, occupied.read_bytes(), "无关的同目录文件不能被改动"
        )

        with self.assertRaises(engine.ImagingError) as ctx:
            engine.rename_image(str(self.images / "巡线.jpg"), "已占用")
        self.assertEqual("IMG_RENAME_CONFLICT", ctx.exception.code)
        self.assertTrue((self.images / "巡线.jpg").exists(), "冲突时原文件不能被动过")

    def test_真实改名引擎支持仅大小写变化(self):
        try:
            import fileops.naming  # noqa: F401
        except ImportError:
            self.skipTest("通用改名引擎尚未合入")
        src = self.make_jpeg("Field.jpg", size=(30, 20))
        out = engine.rename_image(str(src), "field")
        self.assertEqual("field.jpg", out["name"])
        self.assertTrue((self.images / "field.jpg").exists())


class AtomicWriteTest(ImagingTestCase):
    def test_写入被拒绝时不留临时文件(self):
        src = self.make_jpeg(size=(30, 20))
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(self.tmpdir / "输出")
        )
        with mock.patch.object(
            engine, "_link_no_overwrite", side_effect=OSError("磁盘只读")
        ):
            job = FakeJob()
            result = engine.process_files(job, spec)
        self.assertEqual(1, result["failed"])
        outdir = self.tmpdir / "输出"
        leftovers = [p for p in outdir.iterdir() if p.name.startswith(".~img-")]
        self.assertEqual([], leftovers)

    def test_硬链接不可用时回退拷贝正常落盘(self):
        """跨盘/不支持硬链接的文件系统：回退独占创建也要写出完整文件。"""
        src = self.make_jpeg("回退.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        with mock.patch.object(
            engine.os, "link", side_effect=OSError(1, "不支持硬链接")
        ):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        target = Path(result["rows"][0]["output"])
        with Image.open(target) as img:
            self.assertEqual("PNG", img.format)
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_回退拷贝中途ENOSPC不得留下目标或临时文件(self):
        """硬链接回退到拷贝后磁盘写满：必须删掉半截 target，也不能留临时文件。"""
        src = self.make_jpeg("满盘.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        real_copy = engine.shutil.copyfileobj
        calls = {"n": 0}

        def failing_copy(source, dst, *args, **kwargs):
            calls["n"] += 1
            dst.write(b"partial")
            raise OSError(28, "No space left on device")

        self.assertIs(real_copy, engine.shutil.copyfileobj)  # 确认 patch 目标正确
        with (
            mock.patch.object(
                engine.os, "link", side_effect=OSError(1, "不支持硬链接")
            ),
            mock.patch.object(engine.shutil, "copyfileobj", failing_copy),
        ):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, calls["n"], "应走一次回退拷贝")
        self.assertEqual(1, result["failed"], result)
        self.assertIn("No space", result["rows"][0]["reason"])
        self.assertFalse(
            (outdir / "满盘.png").exists(), "ENOSPC 后不得留下半截目标文件"
        )
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_回退拷贝目标被抢占时不覆盖且换序号(self):
        """回退路径同样不能覆盖：目标已存在时独占创建失败，必须换下一个序号。"""
        src = self.make_jpeg("抢占.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        (outdir / "抢占.png").write_bytes(b"old")
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        with mock.patch.object(
            engine.os, "link", side_effect=OSError(1, "不支持硬链接")
        ):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        self.assertEqual("抢占 (2).png", Path(result["rows"][0]["output"]).name)
        self.assertEqual(b"old", (outdir / "抢占.png").read_bytes())

    def test_回退路径被抢占时不得删除别人的文件(self):
        """关键回归：无硬链接的文件系统（U 盘/FAT32）上，回退独占创建撞到同名文件时，
        旧实现会 `_unlink_quiet(target)` 删掉别人的文件，再「重试」写进去。"""
        src = self.make_jpeg("回退抢占.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        sentinel = b"occupant-must-survive"
        real_allocate = engine._allocate_target
        flag = {"first": True}

        def racing_allocate(directory, stem, ext):
            target = real_allocate(directory, stem, ext)
            if flag["first"]:
                flag["first"] = False
                target.write_bytes(sentinel)  # 选名之后、落盘之前被抢占
            return target

        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        with (
            mock.patch.object(
                engine.os, "link", side_effect=OSError(1, "不支持硬链接")
            ),
            mock.patch.object(engine, "_allocate_target", racing_allocate),
        ):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        self.assertEqual(
            sentinel,
            (outdir / "回退抢占.png").read_bytes(),
            "被抢占的目标原内容必须完整保留",
        )
        self.assertEqual("回退抢占 (2).png", Path(result["rows"][0]["output"]).name)
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_源文件消失时不留下0字节目标且不泄漏句柄(self):
        """回退路径里目标已建好、源却打不开：dst 必须被关闭，否则 Windows 上删不掉，
        会留下一个 0 字节的假产物，下次选名还把它当成已存在。"""
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        missing = self.tmpdir / "已消失.tmp"
        target = outdir / "残留.png"
        opened = []
        real_open = builtins.open

        def tracking_open(file, mode="r", *args, **kwargs):
            handle = real_open(file, mode, *args, **kwargs)
            opened.append(handle)
            return handle

        with (
            mock.patch.object(
                engine.os, "link", side_effect=OSError(1, "不支持硬链接")
            ),
            mock.patch.object(builtins, "open", tracking_open),
        ):
            with self.assertRaises(FileNotFoundError):
                engine._link_no_overwrite(str(missing), target)
        self.assertFalse(target.exists(), "源打开失败不得留下 0 字节目标文件")
        self.assertTrue(opened, "应尝试打开目标与源")
        self.assertTrue(
            all(handle.closed for handle in opened), "失败路径不能泄漏文件句柄"
        )

    def test_源消失时批处理报失败且输出目录干净(self):
        """同样的故障经批处理入口：必须报失败，输出目录不留产物（尤其不留 0 字节文件）。"""
        src = self.make_jpeg("待消失.jpg", size=(30, 20))
        outdir = self.tmpdir / "输出"
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        real_link = engine.os.link

        def link_then_vanish(source, target):
            os.unlink(source)  # 落盘前源被外部删掉
            return real_link(source, target)

        with mock.patch.object(engine.os, "link", side_effect=link_then_vanish):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["failed"], result)
        self.assertEqual(0, result["success"])
        leftovers = [p.name for p in outdir.iterdir()] if outdir.exists() else []
        self.assertEqual([], leftovers, "失败时输出目录不得残留空文件或临时文件")

    def test_目标已存在时绝不删除或覆盖它(self):
        """真实磁盘回归：选名后被别人抢占同名文件，占用者必须原封不动。

        旧实现把 FileExistsError 归到回退拷贝的 BaseException 分支，`_unlink_quiet`
        会删掉别人的文件，于是「重试」把占用名当空名写进去 —— 静默毁数据。
        """
        src = self.make_jpeg("占用.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        occupied = outdir / "占用.png"
        sentinel = b"someone-else-file"
        occupied.write_bytes(sentinel)
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        self.assertEqual(
            sentinel, occupied.read_bytes(), "占用目标的原内容必须一字不改"
        )
        self.assertEqual("占用 (2).png", Path(result["rows"][0]["output"]).name)
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_选名与落盘之间被抢占也不覆盖(self):
        """模拟选名后被外部进程写入同名文件：一次落盘失败必须换名，且不动别人的文件。"""
        src = self.make_jpeg("竞态.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        sentinel = b"raced-in"
        real_allocate = engine._allocate_target
        state = {"first": True}

        def racing_allocate(directory, stem, ext):
            target = real_allocate(directory, stem, ext)
            if state["first"]:
                state["first"] = False
                target.write_bytes(sentinel)  # 选名后、落盘前抢占
            return target

        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        with mock.patch.object(engine, "_allocate_target", racing_allocate):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        self.assertEqual(sentinel, (outdir / "竞态.png").read_bytes())
        self.assertEqual("竞态 (2).png", Path(result["rows"][0]["output"]).name)
        self.assertEqual(
            [], [p.name for p in outdir.iterdir() if p.name.startswith(".~img-")]
        )

    def test_落盘冲突不会被误报成功(self):
        """同名文件始终被占住（64 次都失败）时必须报失败，不能返回 success。"""
        src = self.make_jpeg("全占.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        sentinel = b"busy"
        (outdir / "全占.png").write_bytes(sentinel)

        def always_conflict(source, target):
            target.write_bytes(b"intruder")
            raise FileExistsError("busy")

        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        with mock.patch.object(engine, "_link_no_overwrite", always_conflict):
            result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["failed"], result)
        self.assertEqual(0, result["success"])
        self.assertIn("同名文件", result["rows"][0]["reason"])
        self.assertEqual(sentinel, (outdir / "全占.png").read_bytes())

    def test_选名用lexists以避开悬空链接(self):
        """不依赖符号链接权限的等价断言：`lexists` 为真但 `exists` 为假的候选名必须跳过。

        悬空链接在 exists 下不存在，若按 exists 选名就会选中它，写入时穿过链接落到
        它指向的位置（可能覆盖别人的文件）。
        """
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        real_lexists = engine.os.path.lexists

        def fake_lexists(raw):
            name = os.path.basename(str(raw))
            if name == "幽灵.png":
                return True  # 模拟悬空链接：lexists 真、exists 假
            return real_lexists(raw)

        self.assertFalse((outdir / "幽灵.png").exists())
        with mock.patch.object(engine.os.path, "lexists", fake_lexists):
            chosen = engine._allocate_target(outdir, "幽灵", ".png")
        self.assertEqual("幽灵 (2).png", chosen.name)

    def test_悬空符号链接不被当成空名(self):
        """悬空链接在 lexists 下存在、exists 下不存在：按空名选它就会写穿链接指向。"""
        src = self.make_jpeg("悬空.jpg", size=(40, 30))
        outdir = self.tmpdir / "输出"
        outdir.mkdir()
        link = outdir / "悬空.png"
        missing_target = self.tmpdir / "不存在的目标.png"
        try:
            os.symlink(str(missing_target), str(link))
        except (OSError, NotImplementedError):
            self.skipTest("当前环境不允许创建符号链接")
        spec = engine.normalize_process(
            "convert", [str(src)], {"format": "png"}, str(outdir)
        )
        result = engine.process_files(FakeJob(), spec)
        self.assertEqual(1, result["success"], result)
        self.assertEqual("悬空 (2).png", Path(result["rows"][0]["output"]).name)
        self.assertTrue(link.is_symlink(), "悬空链接不能被替换成真实文件")
        self.assertFalse(missing_target.exists(), "不得写穿链接指向的位置")


if __name__ == "__main__":
    unittest.main()
