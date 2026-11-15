# -*- coding: utf-8 -*-
"""图片引擎：查看、预览缓存、单图改名与批量格式转换/压缩/缩放。

约定：
- 目录列举、信息读取与浏览器原生静态预览只解析文件头（Image.open 惰性加载）；
  原生静态图的像素由 Chromium 解码并报告失败，Python 只在生成缓存、动画校验或批处理时解码。
- 预览优先直出原图（JPEG/PNG/GIF/WebP/BMP/ICO/AVIF 且尺寸不超过 max_size，
  或默认预览档下不超过 32MP 原生像素预算）；TIFF 与超预算静态图生成 PNG 缓存。
  缓存键含真实路径、mtime、size 与 max_size，原文件一改就换新缓存文件，绝不把旧图当新图返回。
- 动画 GIF/WebP 直接直出、不压成首帧；批处理一律整条跳过。
- 像素上限 1.5 亿、文件上限 200 MB；超限明确报错（IMG_TOO_LARGE），不做半成品预览。
- 批处理输出永不覆盖：先写同目录唯一临时文件，再以硬链接（或独占创建回退）落盘，
  同名自动追加 ` (2)`；单张失败不中止批次，取消时已写完的文件保留、未开始的不落盘。
"""

import concurrent.futures
import hashlib
import os
import re
import shutil
import tempfile
import threading
import time
from pathlib import Path

from PIL import Image, ImageMath, ImageOps

import state
from jobs import Cancelled

# 查看器开放格式（与设计稿的 8 种格式 / 10 个扩展名一致）
VIEW_EXTS = frozenset(
    {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".ico", ".avif", ".tif", ".tiff"}
)
# 明确不承诺的格式：只列进目录供界面提示「暂不支持」，不尝试解码
UNSUPPORTED_EXTS = frozenset(
    {".heic", ".heif", ".dng", ".cr2", ".nef", ".arw", ".psd", ".eps", ".pdf", ".svg"}
)
# 批处理输入：只接受静态图；GIF 与 ICO/AVIF 不参与
BATCH_INPUT_EXTS = frozenset(
    {".jpg", ".jpeg", ".png", ".webp", ".bmp", ".tif", ".tiff"}
)
# 浏览器可直接渲染的格式：这些格式在尺寸不超限时直出原图
NATIVE_MIME = {
    "JPEG": "image/jpeg",
    "PNG": "image/png",
    "GIF": "image/gif",
    "WEBP": "image/webp",
    "BMP": "image/bmp",
    "ICO": "image/x-icon",
    "AVIF": "image/avif",
}
FORMAT_MIME = dict(NATIVE_MIME, TIFF="image/tiff")
# 批处理输出格式：内部键 → (Pillow 格式名, 扩展名)
OUTPUT_FORMATS = {
    "jpeg": ("JPEG", ".jpg"),
    "png": ("PNG", ".png"),
    "webp": ("WEBP", ".webp"),
}
COMPRESS_LEVELS = {"high": 92, "balanced": 80, "small": 65}
DEFAULT_JPEG_QUALITY = 88
# 压缩目标格式：JPEG/WebP 按档位有损，PNG 只做无损优化（不传质量）。
# BMP/TIFF 没有可写出的有损同格式，按设计稿压缩页输出 JPEG。
COMPRESS_OUTPUT = {
    "JPEG": "jpeg",
    "PNG": "png",
    "WEBP": "webp",
    "BMP": "jpeg",
    "TIFF": "jpeg",
}
# 尺寸修改的目标格式：JPEG/PNG/WebP 保持原格式；BMP/TIFF 输出 JPEG（已批准设计）
RESIZE_OUTPUT = {
    "JPEG": "jpeg",
    "PNG": "png",
    "WEBP": "webp",
    "BMP": "jpeg",
    "TIFF": "jpeg",
}
# 各输出格式能直接写盘的模式；其余模式必须先显式转换，绝不靠 Pillow 隐式毁图
PNG_MODES = frozenset({"1", "L", "LA", "P", "RGB", "RGBA", "I;16", "I;16B", "I;16L"})
WEBP_MODES = frozenset({"RGB", "RGBA"})
JPEG_MODES = frozenset({"L", "RGB"})
# 透明区域填充（转 JPEG 或透明图压平）：保留透明 = 白底，浅灰用设计稿 #F7F8FA
TRANSPARENT_FILLS = {
    "keep": (255, 255, 255),
    "white": (255, 255, 255),
    "gray": (247, 248, 250),
}
MAX_PIXELS = 150_000_000
# 默认预览档下，这一像素预算内的原生格式直接交给 Chromium 解码并缩放；
# 超过后才做 Python 降采样缓存，避免常见照片重复解码 + 重编码。
NATIVE_PREVIEW_MAX_PIXELS = 32_000_000
MAX_BYTES = 200 * 1024 * 1024
PREVIEW_DEFAULT = 2560
# 单次 RPC 的 max_size 下限：渲染层需要缩略图，允许比配置更小
PREVIEW_MIN = 16
PREVIEW_MAX = 8192
# 配置项 image_tools.preview_max 的允许范围（与 config.IMAGE_PREVIEW_RANGE、文档一致）：
# 这是「默认预览档」，太小会让每张图都走缓存，太大又等于让大图原样直出撑爆渲染层
CONFIG_PREVIEW_MIN = 512
CONFIG_PREVIEW_MAX = 8192
# 并发上限固定 2：峰值内存约为单张解码的 2 倍，不随文件数增长
MAX_WORKERS = 2
DEFAULT_WORKERS = 2
# 动画预览逐帧校验的总像素预算（帧数 × 单帧像素）：超过后退化为首帧 + 末帧
ANIMATION_VALIDATE_PIXELS = 400_000_000
# 同名冲突最多尝试的序号，超过视为目录异常
CONFLICT_LIMIT = 1000
# 唯一临时文件前缀：批处理产物与缓存都用它，异常时按此清理
TEMP_PREFIX = ".~img-"
# 预览缓存算法版本：改变预览的像素语义（如 P 模式改为先展开 RGB 再缩放）时必须递增，
# 否则旧算法留下的缓存会被继续命中，用户看到的是偏色的旧图。
CACHE_VERSION = 3
_ILLEGAL_NAME = re.compile(r'[\\/:*?"<>|]')


class ImagingError(RuntimeError):
    """图片业务错误：code 供界面区分失败态，message 直接展示给用户。"""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class _Skip(Exception):
    """单张图片按规则跳过（动画、格式、不放大），携带可读原因。"""

    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------- 路径与文件信息


def _absolute(raw) -> Path:
    if not isinstance(raw, str) or not raw.strip():
        raise ImagingError("PARAM_INVALID", "路径不能为空")
    text = raw.strip()
    path = Path(text).expanduser()
    if not path.is_absolute():
        raise ImagingError("PARAM_INVALID", "路径必须是绝对路径：%s" % text)
    return Path(os.path.realpath(str(path)))


def _resolve_file(raw) -> Path:
    path = _absolute(raw)
    try:
        exists = path.exists()
    except OSError as e:
        raise ImagingError("FILE_NOT_FOUND", "路径无法访问：%s" % e) from e
    if not exists:
        raise ImagingError("FILE_NOT_FOUND", "文件不存在：%s" % path)
    if not path.is_file():
        raise ImagingError("PARAM_INVALID", "不是文件：%s" % path)
    return path


def _resolve_directory(raw) -> Path:
    path = _absolute(raw)
    if not path.exists():
        raise ImagingError("FILE_NOT_FOUND", "目录不存在：%s" % path)
    if not path.is_dir():
        raise ImagingError("PARAM_INVALID", "不是目录：%s" % path)
    return path


def _natural_key(name: str) -> list:
    """自然排序：图2 排在 图10 之前；数字段按数值比较，其余按小写文本。"""
    return [
        (0, int(part)) if part.isdigit() else (1, part.casefold())
        for part in re.split(r"(\d+)", name)
    ]


def _probe(path: Path) -> dict:
    """只读文件头取格式/尺寸/动画标记，不解码像素；EXIF 方向在此换算成显示尺寸。"""
    with Image.open(path) as img:
        fmt = str(img.format or "")
        width, height = img.size
        animated = fmt in ("GIF", "WEBP") and int(getattr(img, "n_frames", 1) or 1) > 1
        try:
            orientation = int(img.getexif().get(0x0112, 1) or 1)
        except Exception:  # noqa: BLE001
            orientation = 1
    if orientation in (5, 6, 7, 8):
        width, height = height, width
    return {
        "format": fmt,
        "width": int(width),
        "height": int(height),
        "animated": animated,
        "mime": FORMAT_MIME.get(fmt, ""),
    }


def _file_info(path: Path, probe=None) -> dict:
    st = path.stat()
    ext = path.suffix.lower()
    info = {
        "name": path.name,
        "path": str(path),
        "ext": ext,
        "size": int(st.st_size),
        "mtime": float(st.st_mtime),
        "mtime_text": time.strftime("%Y-%m-%d %H:%M", time.localtime(st.st_mtime)),
        "format": "",
        "supported": False,
        "animated": False,
        "width": 0,
        "height": 0,
        "mime": "",
        "too_large": False,
    }
    if ext in UNSUPPORTED_EXTS:
        info["too_large"] = False
        return info
    if ext not in VIEW_EXTS:
        return info
    if probe is None:
        try:
            probe = _probe(path)
        except Exception:  # noqa: BLE001
            return info
    info.update(probe)
    info["supported"] = True
    info["too_large"] = int(st.st_size) > MAX_BYTES or (
        probe["width"] * probe["height"] > MAX_PIXELS
    )
    return info


def list_images(raw) -> dict:
    """列目录（不递归）：传入文件时按所在目录列。只保留图片扩展名，自然排序。"""
    path = _absolute(raw)
    if not path.exists():
        raise ImagingError("FILE_NOT_FOUND", "目录不存在：%s" % path)
    directory = path.parent if path.is_file() else path
    if not directory.is_dir():
        raise ImagingError("PARAM_INVALID", "不是目录：%s" % directory)
    files = []
    try:
        entries = list(directory.iterdir())
    except OSError as e:
        raise ImagingError("FILE_NOT_FOUND", "目录无法读取：%s" % e) from e
    for entry in entries:
        if entry.name.startswith("."):
            continue
        ext = entry.suffix.lower()
        if ext not in VIEW_EXTS and ext not in UNSUPPORTED_EXTS:
            continue
        try:
            if not entry.is_file():
                continue
            files.append(_file_info(entry))
        except OSError:
            continue  # 列举过程中被外部删除/改名：跳过，不让整次列举失败
    files.sort(key=lambda item: _natural_key(item["name"]))
    return {"directory": str(directory), "files": files}


def inspect_image(raw) -> dict:
    path = _resolve_file(raw)
    ext = path.suffix.lower()
    if ext not in VIEW_EXTS:
        raise ImagingError(
            "IMG_UNSUPPORTED", "暂不支持的图片格式：%s" % (ext or path.name)
        )
    try:
        probe = _probe(path)
    except Exception as e:  # noqa: BLE001
        raise ImagingError(
            "IMG_DECODE_FAILED", "无法读取图片：%s（%s）" % (path.name, e)
        ) from e
    info = _file_info(path, probe)
    if not info["supported"]:
        raise ImagingError("IMG_DECODE_FAILED", "无法识别图片内容：%s" % path.name)
    return info


# ---------------------------------------------------------------- 预览


def _preview_size(value) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ImagingError("PARAM_INVALID", "max_size 必须是整数")
    if value < PREVIEW_MIN or value > PREVIEW_MAX:
        raise ImagingError(
            "PARAM_INVALID", "max_size 应在 %d..%d 之间" % (PREVIEW_MIN, PREVIEW_MAX)
        )
    return value


def _cache_dir() -> Path:
    path = state.state_dir() / "image-preview"
    path.mkdir(parents=True, exist_ok=True)
    return path


def _cache_path(path: Path, st, max_size: int) -> Path:
    """缓存文件路径：键含算法版本、真实路径、mtime、大小与 max_size。

    版本必须进键——预览算法一改（例如 P 模式改为先展开 RGB），旧图在视觉上就是错的，
    靠 mtime/size 区分不出来，只能整体换一代缓存。
    """
    key = "%d|%s|%d|%d|%d" % (
        CACHE_VERSION,
        path,
        st.st_mtime_ns,
        st.st_size,
        max_size,
    )
    digest = hashlib.sha256(key.encode("utf-8")).hexdigest()[:32]
    return _cache_dir() / (digest + ".png")


def _validate_decodable(path: Path):
    """真正解码一次，损坏/截断文件必须在这里暴露。

    动画只校验文件头会放过截断的后续帧（截断帧在渲染层表现为空白/卡死）；
    逐帧解码的成本用「帧数 × 单帧像素」上限约束：超预算时退化为首帧 + 末帧，
    仍能发现尾部截断，且不会为超长动画付出全量解码代价。
    """
    with Image.open(path) as img:
        img.load()
        frames = int(getattr(img, "n_frames", 1) or 1)
        if frames <= 1:
            return
        width, height = img.size
        full = frames * width * height <= ANIMATION_VALIDATE_PIXELS
        indexes = range(1, frames) if full else [frames - 1]
        for index in indexes:
            img.seek(index)
            img.load()


def preview_image(raw, max_size=None) -> dict:
    """返回原图或缓存预览路径；缓存键含真实路径、mtime、size 与 max_size。"""
    path = _resolve_file(raw)
    size_limit = _preview_size(PREVIEW_DEFAULT if max_size is None else max_size)
    try:
        st = path.stat()
    except OSError as e:
        raise ImagingError("FILE_NOT_FOUND", "文件不存在：%s" % e) from e
    if st.st_size > MAX_BYTES:
        raise ImagingError(
            "IMG_TOO_LARGE", "文件超过 200 MB，暂不支持预览：%s" % path.name
        )
    ext = path.suffix.lower()
    if ext not in VIEW_EXTS:
        raise ImagingError(
            "IMG_UNSUPPORTED", "暂不支持的图片格式：%s" % (ext or path.name)
        )
    try:
        probe = _probe(path)
    except Exception as e:  # noqa: BLE001
        raise ImagingError(
            "IMG_DECODE_FAILED", "无法读取图片：%s（%s）" % (path.name, e)
        ) from e
    if probe["width"] * probe["height"] > MAX_PIXELS:
        raise ImagingError(
            "IMG_TOO_LARGE", "图片超过 1.5 亿像素，暂不支持预览：%s" % path.name
        )

    native = NATIVE_MIME.get(probe["format"], "")
    if native and (
        probe["animated"]
        or max(probe["width"], probe["height"]) <= size_limit
        or (
            size_limit >= PREVIEW_DEFAULT
            and probe["width"] * probe["height"] <= NATIVE_PREVIEW_MAX_PIXELS
        )
    ):
        if probe["animated"] or probe["format"] in ("GIF", "WEBP"):
            # 截断动画可能只剩文件头中的一帧，不能仅凭 n_frames 跳过完整性校验。
            try:
                _validate_decodable(path)
            except Exception as e:  # noqa: BLE001
                raise ImagingError(
                    "IMG_DECODE_FAILED", "无法解码动画：%s（%s）" % (path.name, e)
                ) from e
            return {
                "path": str(path),
                "mime": native,
                "width": probe["width"],
                "height": probe["height"],
                "animated": probe["animated"],
                "cached": False,
            }
        return {
            "path": str(path),
            "mime": native,
            "width": probe["width"],
            "height": probe["height"],
            "animated": False,
            "cached": False,
        }

    target = _cache_path(path, st, size_limit)
    if not target.exists():
        _build_preview(path, target, size_limit)
    try:
        with Image.open(target) as img:
            width, height = img.size
    except Exception as e:  # noqa: BLE001
        raise ImagingError("IMG_DECODE_FAILED", "预览缓存读取失败：%s" % e) from e
    return {
        "path": str(target),
        "mime": "image/png",
        "width": int(width),
        "height": int(height),
        "animated": False,
        "cached": True,
    }


def _build_preview(path: Path, target: Path, size_limit: int):
    """生成 PNG 预览：EXIF 摆正后按长边缩到 size_limit。

    缩放在真实颜色空间里做：P/PA 先展开成 RGB/RGBA（直接对索引做 LANCZOS 会插出
    调色板里不存在的颜色），16 位整型按线性缩放转 8 位，浮点图按 0..1 映射到 8 位；
    写不进 PNG 的模式明确报错而不是整图变白。PNG 压缩不走这里，仍保持 P 原样。
    """
    try:
        with Image.open(path) as img:
            if img.format == "JPEG":
                img.draft(None, (size_limit, size_limit))
            img.load()
            oriented = ImageOps.exif_transpose(img)
            if oriented is None:
                oriented = img.copy()
    except Exception as e:  # noqa: BLE001
        raise ImagingError(
            "IMG_DECODE_FAILED", "无法解码图片：%s（%s）" % (path.name, e)
        ) from e
    prepared = _preview_mode(oriented)
    resized = _resize_to_fit(prepared, size_limit, size_limit)
    _write_cache(resized, target)


def _preview_mode(img: Image.Image) -> Image.Image:
    """把任意来源模式转成可写进 PNG、且缩放语义正确的等价模式。

    - 16 位/32 位整型按 0..65535 → 0..255 线性缩放（保中间调）；浮点按 0..1 映射。
    - P 必须先转 RGB/RGBA：直接对索引做 LANCZOS 会插值索引值，插出调色板里并不存在
      的颜色组合（实测 32 色海报最大通道偏差 25/255）。预览不承诺保留调色板。
    - 其余 PNG 可写模式原样返回；表达不了的模式报业务错误。
    """
    mode = img.mode
    if mode in ("I;16", "I;16B", "I;16L", "I"):
        return _to_8bit_l(img)
    if mode == "F":
        # 浮点 TIFF 常见取值 0..1：先截断到范围内再映射到 0..255
        return ImageMath.lambda_eval(
            lambda args: args["convert"](
                args["min"](args["max"](args["a"], 0), 1) * 255, "L"
            ),
            a=img,
        )
    if mode in ("P", "PA"):
        return _palette_to_rgb(img)
    if mode in PNG_MODES:
        return img
    if mode == "CMYK":
        return img.convert("RGB")
    raise ImagingError("IMG_MODE_UNSUPPORTED", "暂不支持预览该色彩模式：%s" % mode)


def _palette_to_rgb(img: Image.Image) -> Image.Image:
    """P/PA 按透明度转 RGBA 或 RGB：缩放前展开调色板，避免索引插值伪色。"""
    if "transparency" in img.info:
        return img.convert("RGBA")
    if img.mode == "PA":
        return img.convert("RGBA")
    return img.convert("RGB")


def _resize_to_fit(img: Image.Image, max_width: int, max_height: int) -> Image.Image:
    width, height = img.size
    scale = min(max_width / float(width), max_height / float(height))
    if scale >= 1.0:
        return img
    size = (max(1, round(width * scale)), max(1, round(height * scale)))
    return img.resize(size, Image.Resampling.LANCZOS, reducing_gap=2.0)


def _write_cache(img: Image.Image, target: Path):
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=TEMP_PREFIX, suffix=".tmp", dir=str(target.parent)
    )
    os.close(fd)
    try:
        img.save(tmp_name, "PNG")
        os.replace(tmp_name, str(target))
    except BaseException:
        _unlink_quiet(tmp_name)
        raise


def _unlink_quiet(path):
    try:
        os.unlink(str(path))
    except OSError:
        pass


# ---------------------------------------------------------------- 单图改名


def _validate_base_name(name) -> str:
    if not isinstance(name, str):
        raise ImagingError("PARAM_INVALID", "主文件名必须是字符串")
    if not name or not name.strip():
        raise ImagingError("PARAM_INVALID", "主文件名不能为空")
    if name != name.strip():
        raise ImagingError("PARAM_INVALID", "主文件名不能以空格开头或结尾")
    if name.startswith(".") and name.strip(".") == "":
        raise ImagingError("PARAM_INVALID", "主文件名不能只有点号")
    if name.endswith("."):
        raise ImagingError("PARAM_INVALID", "主文件名不能以句点结尾")
    if _ILLEGAL_NAME.search(name):
        raise ImagingError("PARAM_INVALID", '主文件名不能包含 \\ / : * ? " < > |')
    return name


def _rename_one(path: Path, new_base: str):
    """默认改名实现：复用通用改名引擎（Task 1 的 fileops.naming.rename_one）。"""
    try:
        from fileops.naming import rename_one
    except ImportError as e:
        raise ImagingError(
            "IMG_RENAME_ENGINE", "改名引擎不可用（缺少 fileops.naming.rename_one）"
        ) from e
    return rename_one(path, new_base)


def rename_image(raw, name) -> dict:
    """只改主名、保留扩展名；同名冲突/只读/外部删除都给出可读错误，绝不覆盖。"""
    path = _resolve_file(raw)
    base = _validate_base_name(name)
    try:
        result = _rename_one(path, base)
    except ImagingError:
        raise
    except FileExistsError as e:
        raise ImagingError(
            "IMG_RENAME_CONFLICT",
            "同目录已有「%s」，未做任何修改" % (base + path.suffix),
        ) from e
    except FileNotFoundError as e:
        raise ImagingError("FILE_NOT_FOUND", "文件已被删除，无法改名") from e
    except PermissionError as e:
        raise ImagingError(
            "IMG_RENAME_DENIED", "文件只读或被其他程序占用，无法改名"
        ) from e
    except ValueError as e:
        # fileops.naming.rename_one 用 ValueError 报告非法名/非普通文件
        raise ImagingError("PARAM_INVALID", str(e)) from e
    except RuntimeError as e:
        raise ImagingError("IMG_RENAME_FAILED", "改名失败：%s" % e) from e
    except OSError as e:
        raise ImagingError("IMG_RENAME_FAILED", "改名失败：%s" % e) from e
    new_path = Path(os.path.realpath(str(result or path.parent / (base + path.suffix))))
    try:
        return _file_info(new_path)
    except OSError as e:
        raise ImagingError("IMG_RENAME_FAILED", "改名后未找到目标文件：%s" % e) from e


# ---------------------------------------------------------------- 批处理参数


def _positive_int(value, label: str, upper: int = 100000) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ImagingError("PARAM_INVALID", "%s 必须是整数" % label)
    if value <= 0 or value > upper:
        raise ImagingError("PARAM_INVALID", "%s 应在 1..%d 之间" % (label, upper))
    return value


def _quality(value, default: int) -> int:
    if value is None:
        return default
    return _positive_int(value, "质量", 100)


def _output_dir(raw) -> str:
    if raw is None or raw == "":
        return ""
    if not isinstance(raw, str):
        raise ImagingError("PARAM_INVALID", "输出目录必须是字符串")
    path = _absolute(raw)
    if path.exists() and not path.is_dir():
        raise ImagingError("PARAM_INVALID", "输出路径不是目录：%s" % path)
    return str(path)


def normalize_process(operation, files, options=None, output_dir="") -> dict:
    """校验并归一化批处理参数；只在调用期读参数，不创建目录、不碰文件。"""
    if operation not in ("convert", "compress", "resize"):
        raise ImagingError("PARAM_INVALID", "不支持的处理类型：%s" % operation)
    if not isinstance(files, list) or not files:
        raise ImagingError("PARAM_INVALID", "请先选择要处理的图片")
    paths = []
    for item in files:
        if not isinstance(item, str):
            raise ImagingError("PARAM_INVALID", "文件列表必须由路径字符串组成")
        paths.append(_absolute(item))
    if options is None:
        options = {}
    if not isinstance(options, dict):
        raise ImagingError("PARAM_INVALID", "options 必须是对象")

    if operation == "convert":
        fmt = options.get("format")
        if fmt not in OUTPUT_FORMATS:
            raise ImagingError("PARAM_INVALID", "目标格式只支持 JPEG / PNG / WebP")
        normalized = {
            "format": fmt,
            "quality": _quality(options.get("quality"), DEFAULT_JPEG_QUALITY),
            "transparent": options.get("transparent") or "keep",
        }
        if normalized["transparent"] not in TRANSPARENT_FILLS:
            raise ImagingError(
                "PARAM_INVALID", "透明背景只支持 保留透明 / 填充白色 / 填充浅灰"
            )
    elif operation == "compress":
        level = options.get("level") or "balanced"
        if level not in COMPRESS_LEVELS:
            raise ImagingError("PARAM_INVALID", "压缩档位只支持 高质量 / 均衡 / 小体积")
        normalized = {"level": level, "quality": COMPRESS_LEVELS[level]}
    else:
        mode = options.get("mode")
        if mode not in ("pixels", "percent", "long_edge"):
            raise ImagingError(
                "PARAM_INVALID", "尺寸调整方式只支持 像素 / 百分比 / 长边"
            )
        normalized = {"mode": mode}
        if mode == "pixels":
            keep_ratio = options.get("keep_ratio", True)
            if not isinstance(keep_ratio, bool):
                raise ImagingError("PARAM_INVALID", "保持比例必须是布尔值")
            width = options.get("width")
            height = options.get("height")
            if width is None and height is None:
                raise ImagingError("PARAM_INVALID", "请输入目标宽或高")
            normalized["keep_ratio"] = keep_ratio
            if width is not None:
                normalized["width"] = _positive_int(width, "目标宽度")
            if height is not None:
                normalized["height"] = _positive_int(height, "目标高度")
            if not keep_ratio and width is None:
                raise ImagingError(
                    "PARAM_INVALID", "不保持比例时必须同时给出宽度和高度"
                )
            if not keep_ratio and height is None:
                raise ImagingError(
                    "PARAM_INVALID", "不保持比例时必须同时给出宽度和高度"
                )
        elif mode == "percent":
            normalized["percent"] = _positive_int(
                options.get("percent"), "缩放百分比", 400
            )
        else:
            normalized["long_edge"] = _positive_int(
                options.get("long_edge"), "长边像素"
            )

    return {
        "operation": operation,
        "files": paths,
        "options": normalized,
        "output_dir": _output_dir(output_dir),
        "workers": DEFAULT_WORKERS,
    }


# ---------------------------------------------------------------- 批处理执行


def _row(
    path: Path,
    status: str,
    output: str = "",
    reason: str = "",
    before: int = 0,
    after: int = 0,
):
    return {
        "path": str(path),
        "name": path.name,
        "status": status,
        "output": output,
        "reason": reason,
        "size_before": int(before),
        "size_after": int(after),
    }


def _plan_output(spec: dict, probe: dict) -> dict:
    """决定目标格式、扩展名、质量与目标尺寸；需要跳过时抛 _Skip。

    压缩：JPEG/WebP 按档位有损，PNG 只做无损优化（quality=None），BMP/TIFF 输出 JPEG。
    尺寸：JPEG/PNG/WebP 保持原格式，BMP/TIFF 输出 JPEG（已批准设计）。
    转换：按用户选定格式，质量只用显式参数。
    """
    operation = spec["operation"]
    options = spec["options"]
    source = probe["format"]
    if operation == "convert":
        key = options["format"]
        quality = options.get("quality")
    elif operation == "compress":
        if source not in COMPRESS_OUTPUT:
            raise _Skip("%s 不在批处理范围" % source)
        key = COMPRESS_OUTPUT[source]
        # PNG 无损优化不传质量；JPEG/WebP 按档位落质量
        quality = None if key == "png" else options.get("quality")
    else:
        if source not in RESIZE_OUTPUT:
            raise _Skip("%s 不在批处理范围" % source)
        key = RESIZE_OUTPUT[source]
        quality = DEFAULT_JPEG_QUALITY
    fmt_name, ext = OUTPUT_FORMATS[key]
    plan = {
        "key": key,
        "format": fmt_name,
        "ext": ext,
        "quality": quality,
        "size": None,
    }
    if operation != "resize":
        return plan

    width, height = probe["width"], probe["height"]
    mode = options["mode"]
    if mode == "percent":
        percent = options["percent"]
        if percent >= 100:
            raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
        plan["size"] = (
            max(1, round(width * percent / 100.0)),
            max(1, round(height * percent / 100.0)),
        )
        return plan
    if mode == "long_edge":
        long_edge = options["long_edge"]
        if long_edge >= max(width, height):
            raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
        scale = long_edge / float(max(width, height))
        plan["size"] = (max(1, round(width * scale)), max(1, round(height * scale)))
        return plan

    target_w = options.get("width")
    target_h = options.get("height")
    if options["keep_ratio"]:
        if target_w is not None and target_h is not None:
            scale = min(target_w / float(width), target_h / float(height))
            if scale >= 1.0:
                raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
            plan["size"] = (max(1, round(width * scale)), max(1, round(height * scale)))
            return plan
        if target_w is not None:
            if target_w >= width:
                raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
            plan["size"] = (target_w, max(1, round(height * target_w / float(width))))
            return plan
        if target_h >= height:
            raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
        plan["size"] = (max(1, round(width * target_h / float(height))), target_h)
        return plan
    if target_w >= width or target_h >= height:
        raise _Skip("目标尺寸不小于原图，已按不放大规则跳过")
    plan["size"] = (target_w, target_h)
    return plan


# 16/32 位整型灰度 → 8 位：0..65535 线性缩放到 0..255。
# 不能用 `point(lambda)`：Pillow 13 会用 ImagePointTransform 探针调用该函数做
# scale/offset 推断，`int(...)` 直接抛 TypeError；显式 65536 项 LUT 既确定又快。
_INT_TO_L_LUT = [min(255, value // 256) for value in range(65536)]


def _to_8bit_l(img: Image.Image) -> Image.Image:
    """16 位/32 位整型灰度按 0..65535 → 0..255 线性缩放转 8 位。

    直接 convert("L") 会把 >255 的样本一律截成 255（整图变白），丢失画面内容。
    """
    if img.mode in ("I;16", "I;16B", "I;16L"):
        ints = img.convert("I")
    elif img.mode == "I":
        ints = img
    else:
        raise ImagingError("IMG_MODE_UNSUPPORTED", "无法转 8 位：%s" % img.mode)
    return ints.point(_INT_TO_L_LUT, "L")


def _prepare(
    img: Image.Image,
    fmt_name: str,
    transparent: str,
    resampling: bool = False,
) -> Image.Image:
    """EXIF 摆正 → 按目标格式显式转换色彩模式 → JPEG 按透明背景压平。

    原则：能在目标格式里原样写出的模式一律不动（压缩时 P 保持 P、I;16 保持 16 位），
    只有目标格式表达不了的模式才显式换算；换算不了的模式报业务错误，绝不静默毁图。

    `resampling=True`（尺寸修改）时，索引图必须先展开成 RGB/RGBA 再缩放：对 P 直接
    做 LANCZOS 会插值索引值，插出调色板里不存在的颜色组合（伪色）。尺寸修改不承诺
    保留调色板，颜色正确优先；PNG 压缩不受影响（`resampling=False`）。
    """
    oriented = ImageOps.exif_transpose(img)
    if oriented is None:
        oriented = img.copy()
    mode = oriented.mode
    if fmt_name == "PNG":
        if mode in ("P", "PA") and resampling:
            return _palette_to_rgb(oriented)
        if mode in PNG_MODES:
            return oriented
        raise ImagingError("IMG_MODE_UNSUPPORTED", "色彩模式 %s 不能输出为 PNG" % mode)
    if fmt_name == "WEBP":
        if mode in WEBP_MODES:
            return oriented
        if mode in ("P", "1"):
            return oriented.convert("RGBA" if mode == "P" else "L")
        if mode in ("L", "LA"):
            return oriented.convert("RGBA" if mode == "LA" else "RGB")
        if mode in ("I;16", "I;16B", "I;16L", "I"):
            return _to_8bit_l(oriented).convert("RGB")
        raise ImagingError("IMG_MODE_UNSUPPORTED", "色彩模式 %s 不能输出为 WebP" % mode)
    # JPEG：不支持透明与高位深，透明按背景压平，16 位按 0..65535 缩放
    if mode in ("I;16", "I;16B", "I;16L", "I"):
        return _to_8bit_l(oriented).convert("RGB")
    if mode == "F":
        raise ImagingError(
            "IMG_MODE_UNSUPPORTED", "色彩模式 F（浮点）不能输出为 JPEG/WebP"
        )
    if mode == "CMYK":
        oriented = oriented.convert("RGB")
    elif mode == "P":
        oriented = oriented.convert("RGBA")
    elif mode == "1":
        oriented = oriented.convert("L")
    elif mode not in ("RGB", "RGBA", "L", "LA"):
        raise ImagingError("IMG_MODE_UNSUPPORTED", "色彩模式 %s 不能输出为 JPEG" % mode)
    if oriented.mode in ("RGBA", "LA"):
        fill = TRANSPARENT_FILLS.get(transparent, (255, 255, 255))
        background = Image.new("RGB", oriented.size, fill)
        if oriented.mode == "LA":
            background.paste(oriented.convert("RGB"), mask=oriented.getchannel("A"))
        else:
            background.paste(oriented, mask=oriented.getchannel("A"))
        oriented = background
    if oriented.mode not in JPEG_MODES:
        oriented = oriented.convert("RGB")
    return oriented


def _allocate_target(directory: Path, stem: str, ext: str) -> Path:
    """选一个尚未被占用的目标名。

    用 `lexists` 而不是 `exists`：悬空符号链接在 exists 下为 False，会让我们选中它，
    随后写入就穿过链接落到它指向的位置（甚至覆盖别人的文件）。
    """
    for index in range(1, CONFLICT_LIMIT + 1):
        name = "%s%s" % (stem, ext) if index == 1 else "%s (%d)%s" % (stem, index, ext)
        candidate = directory / name
        if not os.path.lexists(str(candidate)):
            return candidate
    raise ImagingError("IMG_OUTPUT_CONFLICT", "同名文件过多，无法另存：%s" % stem)


def _write_atomic(img: Image.Image, target: Path, fmt_name: str, quality):
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=TEMP_PREFIX, suffix=".tmp", dir=str(target.parent)
    )
    os.close(fd)
    try:
        if fmt_name == "JPEG":
            img.save(
                tmp_name,
                "JPEG",
                quality=int(quality or DEFAULT_JPEG_QUALITY),
                optimize=True,
            )
        elif fmt_name == "WEBP":
            img.save(
                tmp_name,
                "WEBP",
                quality=int(quality or DEFAULT_JPEG_QUALITY),
                method=4,
            )
        else:
            img.save(tmp_name, "PNG", optimize=True)
        _link_no_overwrite(tmp_name, target)
    finally:
        _unlink_quiet(tmp_name)


def _link_no_overwrite(source: str, target: Path):
    """先硬链接落盘（目标存在即失败，绝不覆盖）；文件系统不支持时退回独占创建。

    两条约束缺一不可：
    - 目标已存在必须是「失败上抛」，调用方换下一个序号；绝不能删掉占用者再重试，
      那会把别人的文件当空名覆盖（U 盘/无硬链接文件系统上真实会发生）。
    - 只有本次确实创建成功的文件才允许清理：回退拷贝中途失败（ENOSPC 等）要删掉
      半截目标，而 `open(..., "xb")` 撞名时文件是别人建的，一个字节都不能碰。
    """
    try:
        os.link(source, str(target))
        return
    except FileExistsError:
        raise  # 目标存在：交给 _save_output 换名，绝不清理
    except OSError:
        pass  # 文件系统不支持硬链接（FAT32/exFAT/部分网络盘）：走回退

    try:
        dst = open(str(target), "xb")
    except FileExistsError:
        raise  # 撞名但文件不是我们建的：不清理，直接上抛
    try:
        # dst 在前：源打不开时 dst 也会被 with 关闭，不会泄漏句柄或留下 0 字节目标
        with dst, open(source, "rb") as src:
            shutil.copyfileobj(src, dst)
            dst.flush()
            os.fsync(dst.fileno())
    except BaseException:
        _unlink_quiet(target)  # 目标本次由我们创建（可能是 0 字节或半截）：删掉
        raise


def _save_output(img: Image.Image, directory: Path, stem: str, plan: dict) -> Path:
    ext = plan["ext"]
    for _ in range(64):  # 选名与落盘之间可能有竞争：目标被抢占就换下一个序号
        target = _allocate_target(directory, stem, ext)
        try:
            _write_atomic(img, target, plan["format"], plan["quality"])
            return target
        except FileExistsError:
            continue
    raise ImagingError("IMG_OUTPUT_CONFLICT", "输出目录同名文件持续被占用，无法另存")


def _process_one(job, spec: dict, path: Path) -> dict:
    job.check_cancel()
    try:
        st = path.stat()
    except OSError as e:
        return _row(path, "failed", reason="文件已不存在：%s" % e)
    size_before = int(st.st_size)
    if size_before > MAX_BYTES:
        return _row(path, "failed", reason="文件超过 200 MB 上限", before=size_before)
    ext = path.suffix.lower()
    if ext not in VIEW_EXTS:
        return _row(
            path,
            "skipped",
            reason="%s 不在批处理范围" % (ext.lstrip(".").upper() or "该格式"),
            before=size_before,
        )
    if ext not in BATCH_INPUT_EXTS and ext != ".gif":
        return _row(
            path,
            "skipped",
            reason="%s 不在批处理范围" % ext.lstrip(".").upper(),
            before=size_before,
        )
    try:
        probe = _probe(path)
    except Exception as e:  # noqa: BLE001
        return _row(path, "failed", reason="无法解码：%s" % e, before=size_before)
    if probe["animated"]:
        return _row(
            path, "skipped", reason="动画图：整条跳过，不压成首帧", before=size_before
        )
    if ext == ".gif":
        return _row(
            path, "skipped", reason="静态 GIF 不在首版批处理范围", before=size_before
        )
    if probe["width"] * probe["height"] > MAX_PIXELS:
        return _row(path, "failed", reason="像素超过 1.5 亿上限", before=size_before)
    transparent = spec["options"].get("transparent") or "keep"
    try:
        plan = _plan_output(spec, probe)
    except _Skip as skip:
        return _row(path, "skipped", reason=skip.reason, before=size_before)
    try:
        with Image.open(path) as raw:
            img = _prepare(
                raw,
                plan["format"],
                transparent,
                resampling=plan["size"] is not None,
            )
        if plan["size"]:
            img = img.resize(plan["size"], Image.Resampling.LANCZOS)
        directory = (
            Path(spec["output_dir"])
            if spec["output_dir"]
            else path.parent / time.strftime("processed_%Y%m%d")
        )
        target = _save_output(img, directory, path.stem, plan)
    except ImagingError as e:
        return _row(path, "failed", reason=str(e), before=size_before)
    except Exception as e:  # noqa: BLE001
        return _row(path, "failed", reason="处理失败：%s" % e, before=size_before)
    try:
        size_after = int(target.stat().st_size)
    except OSError:
        size_after = 0
    return _row(
        path, "success", output=str(target), before=size_before, after=size_after
    )


def _log_row(job, row: dict):
    if row["status"] == "success":
        job.log(
            "成功 %s（%s → %s）"
            % (
                row["name"],
                _human_size(row["size_before"]),
                _human_size(row["size_after"]),
            )
        )
    elif row["status"] == "skipped":
        job.log("跳过 %s：%s" % (row["name"], row["reason"]))
    else:
        job.log("失败 %s：%s" % (row["name"], row["reason"]))


def _human_size(size: int) -> str:
    if size >= 1024 * 1024:
        return "%.1f MB" % (size / 1024.0 / 1024.0)
    if size >= 1024:
        return "%.0f KB" % (size / 1024.0)
    return "%d B" % size


def process_files(job, spec: dict) -> dict:
    """线程池执行整批：最多 2 张并行，单张失败不中止。

    取消（jobs.Cancelled）不再把已收集的结果丢掉：先关闭未开始的 future、等已开始的项
    把当前文件写完，再把实际完成的项汇总成部分结果返回（带 ``cancelled: True``）。
    JobManager 仍按 job.cancelled 落 cancelled 状态，不会变成 success；其他异常照常上抛。
    """
    files = spec["files"]
    total = len(files)
    workers = spec.get("workers") or DEFAULT_WORKERS
    workers = max(1, min(int(workers), MAX_WORKERS))
    rows = [None] * total
    done = 0
    lock = threading.Lock()
    job.log("开始处理 %d 项 · %s · 并发 %d" % (total, _describe_spec(spec), workers))
    job.set_progress(0, total, "准备处理")
    pool = concurrent.futures.ThreadPoolExecutor(
        max_workers=workers, thread_name_prefix="image-tools"
    )
    cancelled = False
    try:
        futures = {
            pool.submit(_process_one, job, spec, path): index
            for index, path in enumerate(files)
        }
        try:
            for future in concurrent.futures.as_completed(futures):
                row = future.result()
                rows[futures[future]] = row
                with lock:
                    done += 1
                job.set_progress(done, total, row["name"])
                _log_row(job, row)
                job.check_cancel()
        except Cancelled:
            cancelled = True
            # 关闭未开始的 future；已开始的项不再打断，等它把当前文件写完
            pool.shutdown(wait=True, cancel_futures=True)
            _collect_finished(job, spec, futures, rows)
    except BaseException:
        pool.shutdown(wait=True, cancel_futures=True)
        raise
    finally:
        pool.shutdown(wait=False)

    success = sum(1 for row in rows if row and row["status"] == "success")
    failed = sum(1 for row in rows if row and row["status"] == "failed")
    skipped = sum(1 for row in rows if row and row["status"] == "skipped")
    output_dirs = sorted(
        {str(Path(row["output"]).parent) for row in rows if row and row["output"]}
    )
    if cancelled:
        job.log(
            "已取消：已完成 %d 项（成功 %d）" % (sum(1 for r in rows if r), success)
        )
    else:
        job.log("完成：成功 %d · 失败 %d · 跳过 %d" % (success, failed, skipped))
    return {
        "operation": spec["operation"],
        "success": success,
        "failed": failed,
        "skipped": skipped,
        "total": total,
        "rows": [row for row in rows if row],
        "output_dirs": output_dirs,
        "cancelled": cancelled,
    }


def _collect_finished(job, spec: dict, futures: dict, rows: list):
    """取消后补齐「已结束」的项：只填空位，不重复记录已收集的行。

    已关闭/被取消的 future 按规则跳过；已结束却抛非 Cancelled 的 BaseException 那一项
    不能静默跳过——它既没写出文件也不是被取消，必须写明确日志并落 failed，不能冒充
    成功。取消收尾本身已是终态，这里记录后继续收完其余项，让部分结果如实回给界面；
    非取消的普通执行路径（process_files 主循环）仍按原样上抛，不受影响。
    """
    for future, index in futures.items():
        if rows[index] is not None or future.cancelled():
            continue
        try:
            row = future.result()
        except Cancelled:
            continue  # 取消项没写出文件，不进结果
        except BaseException as e:  # noqa: BLE001
            path = Path(spec["files"][index])
            rows[index] = _row(path, "failed", reason="收尾时异常：%s" % e)
            job.log("失败 %s：收尾时异常 %s" % (path.name, e))
            continue
        if row:
            rows[index] = row


def _describe_spec(spec: dict) -> str:
    """任务标题/日志用的处理描述。

    PNG 压缩没有质量参数（只做无损优化），不能把档位数字写进去；
    同批含 PNG 与有损格式时同时说明两者。
    """
    operation = spec["operation"]
    options = spec["options"]
    if operation == "convert":
        fmt = options["format"].upper()
        if fmt == "PNG":
            # PNG 输出无损，质量参数对它没有意义，别在日志里写一个不生效的数值
            return "格式转换 PNG"
        return "格式转换 %s · 质量 %s" % (fmt, options["quality"])
    if operation == "compress":
        targets = _compress_targets(spec)
        lossy = targets & {"JPEG", "WEBP"}
        if "PNG" in targets and not lossy:
            return "压缩体积 · PNG 无损优化"
        if "PNG" in targets:
            return "压缩体积 · 质量 %s · PNG 无损优化" % options["quality"]
        return "压缩体积 · 质量 %s" % options["quality"]
    return "尺寸修改 · %s" % options["mode"]


def _compress_targets(spec: dict) -> set:
    """按源格式推出本批压缩会产出的目标格式集合（用于描述与估算）。"""
    targets = set()
    for path in spec["files"]:
        try:
            with Image.open(path) as img:
                source = str(img.format or "")
        except Exception:  # noqa: BLE001
            continue
        key = COMPRESS_OUTPUT.get(source)
        if key:
            targets.add(OUTPUT_FORMATS[key][0])
    return targets
