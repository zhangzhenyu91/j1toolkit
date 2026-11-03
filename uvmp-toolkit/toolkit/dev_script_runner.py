# -*- coding: utf-8 -*-
"""开发脚本测试入口：用客户端同款 Python 运行时/模块路径执行任意 .py。

被 `app.py run-script`（冻结核心的子进程）调用；Electron 的“脚本测试”页通过
`dev_script` 应用后端 spawn 本入口，stdout/stderr 逐行回传到任务日志。

与普通 `python xxx.py` 的差异：
- 冻结版里 `sys.executable` 是 uvmp-core，能加载 PyInstaller 打包的 requests/gmssl 等；
- 默认把脚本所在目录与项目根加入 sys.path，脚本可直接 import uvmp/vehicle_export；
- 默认工作目录为脚本所在目录，并注入客户端配置路径与 UTF-8/无缓冲环境。
"""

import os
import runpy
import sys
import traceback
from pathlib import Path


def normalize_args(raw) -> list:
    args = list(raw or [])
    if args and args[0] == "--":
        args = args[1:]
    return [str(a) for a in args]


def run_cli(path: str, raw_args=None, cwd: str = "") -> int:
    p = Path(path or "").expanduser()
    if not p.is_file():
        print("[run-script] 脚本不存在: %s" % p, file=sys.stderr)
        return 2
    if p.suffix.lower() != ".py":
        print("[run-script] 只支持 .py 文件: %s" % p, file=sys.stderr)
        return 2
    p = p.resolve()
    args = normalize_args(raw_args)
    script_dir = str(p.parent)
    if script_dir not in sys.path:
        sys.path.insert(0, script_dir)
    workdir = Path(cwd).expanduser() if cwd else p.parent
    if not workdir.is_dir():
        print("[run-script] 工作目录不存在: %s" % workdir, file=sys.stderr)
        return 2
    os.chdir(str(workdir))
    os.environ.setdefault("UVMP_DEV_SCRIPT", "1")
    sys.argv = [str(p)] + args
    print("[run-script] 解释器: %s" % sys.executable)
    print("[run-script] 脚本: %s" % p)
    print("[run-script] 工作目录: %s" % workdir)
    print("[run-script] 参数: %s" % (" ".join(args) if args else "（无）"))
    try:
        runpy.run_path(str(p), run_name="__main__")
        return 0
    except SystemExit as e:
        code = e.code
        if code is None:
            return 0
        if isinstance(code, int):
            return code
        print(code, file=sys.stderr)
        return 1
    except Exception:  # noqa: BLE001
        traceback.print_exc()
        return 1
