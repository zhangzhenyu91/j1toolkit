# -*- coding: utf-8 -*-
"""应用注册器：扫描 apps/*/app.json，按需加载 backend.py。

新增应用 = 在 apps/ 下新建一个目录，放入：
  app.json   声明（id/名称/徽标/简介/排序，详见《内网工具箱开发指南.md》插件规范）
  backend.py 契约函数：run(job, params) -> dict；可选 status(cfg) / preview(params) /
                      launch_browser(cfg)（需要人工准备会话的受控浏览器应用）
无需改动任何既有文件。

注意：backend.py 禁止 import 任何 GUI 依赖（PyQt5 等）——CLI/定时器无头环境没有 GUI 依赖；
界面由 Electron 桌面端（desktop/src/pages）实现，旧 PyQt page.py 机制已废弃。
"""

import importlib.util
import json
from pathlib import Path

APPS_DIR = Path(__file__).resolve().parent


def _load_module(mod_name: str, path: Path):
    spec = importlib.util.spec_from_file_location(mod_name, str(path))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def scan_apps() -> list:
    apps = []
    for d in sorted(APPS_DIR.iterdir()):
        meta_file = d / "app.json"
        if not d.is_dir() or not meta_file.exists():
            continue
        try:
            meta = json.loads(meta_file.read_text(encoding="utf-8"))
        except Exception as e:
            print("[apps] %s app.json 解析失败: %s" % (d.name, e))
            continue
        meta.setdefault("id", d.name)
        meta.setdefault("name", d.name)
        meta.setdefault("badge", meta["name"][:2])
        meta.setdefault("desc", "")
        meta.setdefault("order", 99)
        meta["_dir"] = str(d)
        backend = d / "backend.py"
        if backend.exists():
            try:
                meta["_backend"] = _load_module("toolkit_app_" + d.name, backend)
            except Exception as e:
                print("[apps] %s backend.py 加载失败: %s" % (d.name, e))
        apps.append(meta)
    apps.sort(key=lambda a: (a.get("order", 99), a.get("name", "")))
    return apps
