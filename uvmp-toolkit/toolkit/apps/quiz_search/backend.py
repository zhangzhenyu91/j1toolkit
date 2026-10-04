# -*- coding: utf-8 -*-
"""题库搜题：桌面壳实时功能（截屏/OCR/悬浮窗均在 Electron 层完成，见 desktop/electron/quizsearch/）。

本模块仅为应用注册与自检占位：Python 核心按规范不得引入原生依赖（开发指南 §8.1），
OCR 推理走壳层 onnxruntime-node，故此处无任何业务逻辑，亦无 CLI 任务。
"""

APP_ID = "quiz_search"


def run(job, params: dict) -> dict:
    job.log("题库搜题为桌面壳实时功能，请在客户端「题库搜题」页面操作，无 CLI 任务。")
    return {"message": "shell_only"}


def status(cfg: dict) -> dict:
    return {"shell_only": True}
