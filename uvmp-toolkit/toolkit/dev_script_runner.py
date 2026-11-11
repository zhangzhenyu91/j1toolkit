# -*- coding: utf-8 -*-
"""开发脚本测试入口：用客户端同款 Python 运行时/模块路径执行任意 .py。

被 `app.py run-script`（冻结核心的子进程）调用；Electron 的“脚本测试”页通过
`dev_script` 应用后端 spawn 本入口，stdout/stderr 逐行回传到任务日志。

与普通 `python xxx.py` 的差异：
- 冻结版里 `sys.executable` 是 uvmp-core，能加载 PyInstaller 打包的 requests/gmssl 等；
- 默认把脚本所在目录与项目根加入 sys.path，脚本可直接 import uvmp/vehicle_export；
- 工作目录由 `--cwd` 指定（缺省为脚本所在目录）；后端每次都会显式传 `--cwd`，
  避免本入口把 Popen 的工作目录覆盖成脚本目录；
- 注入客户端配置路径与 UTF-8/无缓冲环境。

参数切分与脱敏（`split_args` / `mask_args`）实现放在本模块，后端与入口共用同一份：
- Windows 按 MSVCRT（CommandLineToArgvW）规则切分，反斜杠路径与引号原样保真；
- password/secret/token 等参数值一律打码，日志与任务 result 都走这里。
"""

import os
import re
import runpy
import shlex
import sys
import traceback
from pathlib import Path

MASK = "***"

# 敏感参数名片段（小写、去掉前导 - 与下划线后做包含匹配）
SENSITIVE_KEYS = (
    "password",
    "passwd",
    "pwd",
    "pass",
    "secret",
    "token",
    "apikey",
    "credential",
    "authorization",
    "cookie",
    "sso",
)

_KV_RE = re.compile(r"^(--?[^=]+)=(.*)$", re.S)
_QUERY_RE = re.compile(
    r"([?&;][^=&#;]*(?:%s)[^=&#;]*=)[^&#;]*" % "|".join(SENSITIVE_KEYS),
    re.I,
)


def _is_sensitive(name: str) -> bool:
    key = str(name or "").strip().lower().lstrip("-").replace("_", "").replace("-", "")
    return any(part in key for part in SENSITIVE_KEYS)


def mask_args(raw) -> list:
    """把参数列表里的敏感值替换为 ***；返回新列表，不修改入参。

    规则：`--password xxx` 打码下一个值；`--token=xxx` 打码等号右侧；
    其余参数里的 `?token=xxx`/`&secret=xxx` 查询串也打码。保守起见，敏感开关后
    紧跟的任何值（即使以 - 开头）都视为敏感值。
    """
    out = []
    hide_next = False
    for item in list(raw or []):
        text = str(item)
        if hide_next:
            out.append(MASK if text.strip() else text)
            hide_next = False
            continue
        match = _KV_RE.match(text)
        if match and _is_sensitive(match.group(1)):
            out.append("%s=%s" % (match.group(1), MASK))
            continue
        if text.startswith("-") and _is_sensitive(text):
            out.append(text)
            hide_next = True
            continue
        out.append(_QUERY_RE.sub(lambda m: m.group(1) + MASK, text))
    return out


def split_windows_command_line(text: str) -> list:
    """按 MSVCRT（CommandLineToArgvW）规则切分一段命令行文本。

    Python 子进程的 argv 由系统按该规则还原，所以按同一规则切分才能保真：
    反斜杠只在引号前有转义含义（2n 个 → n 个反斜杠 + 引号开合，2n+1 个 →
    n 个反斜杠 + 字面引号），引号内空白不切分。
    """
    args = []
    index = 0
    size = len(text)
    while index < size:
        while index < size and text[index] in " \t":
            index += 1
        if index >= size:
            break
        current = []
        in_quotes = False
        while index < size and (in_quotes or text[index] not in " \t"):
            char = text[index]
            if char == "\\":
                end = index
                while end < size and text[end] == "\\":
                    end += 1
                count = end - index
                if end < size and text[end] == '"':
                    current.append("\\" * (count // 2))
                    if count % 2:
                        current.append('"')
                        index = end + 1
                    else:
                        index = end  # 引号留给下一轮，按开合成对处理
                else:
                    current.append("\\" * count)
                    index = end
                continue
            if char == '"':
                in_quotes = not in_quotes
                index += 1
                continue
            current.append(char)
            index += 1
        args.append("".join(current))
    return args


def split_args(raw, windows=None) -> list:
    """把页面传来的参数（字符串或列表）切成 argv 列表。

    windows 缺省按当前平台判定；显式传 True/False 供测试固定规则。
    """
    if raw is None:
        return []
    if isinstance(raw, (list, tuple)):
        return [str(x) for x in raw]
    text = str(raw).strip()
    if not text:
        return []
    use_windows = os.name == "nt" if windows is None else bool(windows)
    if use_windows:
        return split_windows_command_line(text)
    return shlex.split(text)


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
    print("[run-script] 解释器: %s" % sys.executable, flush=True)
    print("[run-script] 脚本: %s" % p, flush=True)
    print("[run-script] 工作目录: %s" % workdir, flush=True)
    print(
        "[run-script] 参数: %s" % (" ".join(mask_args(args)) if args else "（无）"),
        flush=True,
    )
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
