# -*- coding: utf-8 -*-
"""图片 RPC 服务层：只做参数校验、配置读取与 Job 装配，像素处理全在 engine。

同步调用只处理「列目录 / 读信息 / 预览小图 / 单图改名」这类小数据；格式转换、压缩与
尺寸修改走 JobManager 的 image_tools 任务，事件经 jobs 的 on_log/on_progress/on_done
桥接给独立图片窗口，取消复用现有 cancelJob。
"""

import config

from . import engine

APP_ID = "image_tools"
OPERATION_TITLES = {
    "convert": "图片工具 · 格式转换",
    "compress": "图片工具 · 压缩体积",
    "resize": "图片工具 · 尺寸修改",
}


def list_images(params: dict) -> dict:
    return engine.list_images((params or {}).get("path"))


def inspect_image(params: dict) -> dict:
    return engine.inspect_image((params or {}).get("path"))


def preview_image(params: dict) -> dict:
    params = params or {}
    max_size = params.get("max_size")
    if max_size is None:
        max_size = _configured_preview_max()
    return engine.preview_image(params.get("path"), max_size=max_size)


def _configured_preview_max() -> int:
    """读配置里的默认预览长边；手改 config.json 写坏的越界/非整数值按规则兜底。

    配置项本身在保存时已校验，但用户可能直接编辑文件：越界钳到 512..8192
    （`engine.CONFIG_PREVIEW_MIN..MAX`，与 `config.IMAGE_PREVIEW_RANGE`、文档一致），
    类型不对回退默认 2560——否则一个 `preview_max: 1` 会让所有图都去生成缓存，
    而 `preview_max: 100000` 又会让 4 万像素大图原样直出撑爆渲染层。
    """
    section = config.load().get("image_tools") or {}
    value = section.get("preview_max", engine.PREVIEW_DEFAULT)
    if isinstance(value, bool) or not isinstance(value, int):
        return engine.PREVIEW_DEFAULT
    return max(engine.CONFIG_PREVIEW_MIN, min(engine.CONFIG_PREVIEW_MAX, value))


def rename_image(params: dict) -> dict:
    params = params or {}
    return engine.rename_image(params.get("path"), params.get("name"))


def process_spec(params: dict) -> dict:
    """把 RPC 参数归一化成 engine 可直接执行的批处理描述（含并发上限）。"""
    params = params or {}
    spec = engine.normalize_process(
        params.get("operation"),
        params.get("files"),
        params.get("options"),
        params.get("output_dir") or "",
    )
    section = config.load().get("image_tools") or {}
    workers = section.get("workers", engine.DEFAULT_WORKERS)
    if isinstance(workers, bool) or not isinstance(workers, int) or workers < 1:
        workers = engine.DEFAULT_WORKERS
    spec["workers"] = min(workers, engine.MAX_WORKERS)
    return spec


def title(spec: dict) -> str:
    return OPERATION_TITLES.get(spec["operation"], "图片工具")


def run_process(job, spec: dict) -> dict:
    return engine.process_files(job, spec)
