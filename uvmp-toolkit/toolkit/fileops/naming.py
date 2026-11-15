# -*- coding: utf-8 -*-
"""通用改名引擎：全量预检 + 同目录临时名两阶段提交，任何情况下都不覆盖已有文件。

被 batch_rename 插件（任意类型文件的批量改名）与图片查看器（单图改名 rename_one）
共用；只改名称，不移动位置、不改写内容，也不提供覆盖选项。

预检判定顺序（与设计稿一致）：非法名称 → 名称未变化 → 批内目标互撞 → 磁盘已有同名
→ 仅大小写变化 → 循环改名。非法名、未变化项与让位跳过项都不占统一序号，序号只在
可提交项之间连续（让位排除后按排除集合重算，直到不再新增排除项）。

提交以「组」为单位、分两阶段：把「目标名称正被同批其他项占用」「仅大小写变化」的项
聚成交付组，组内先全部改到同目录唯一临时名，再统一落到目标名——互换（a→b / b→a）与
链式改名因此不会互相覆盖。组内任何一步失败整组回滚到原名（绝不换「原名 (2)」兜底），
回滚失败写 job.log 并在结果里给出文件真实位置；取消只在组边界生效：已完成组保留新名，
未开始的组不动，未执行项标注 not_run。正常路径不留临时文件，回滚本身失败（原名被外部
占用、暂存文件被外部删除等）时会保留 `.uvmp-rename-*` 暂存文件并上报真实路径。
"""

import os
import re
import secrets
from pathlib import Path

from jobs import Cancelled

TEMP_PREFIX = ".uvmp-rename-"
INVALID_CHARS = '\\/:*?"<>|'
# Windows 设备保留名：不区分大小写，且「CON.txt」这类带扩展名同样不可用
RESERVED_NAMES = frozenset(
    ["CON", "PRN", "AUX", "NUL"]
    + ["COM%d" % i for i in range(1, 10)]
    + ["LPT%d" % i for i in range(1, 10)]
)
MAX_NAME_CHARS = 255
# 统一主名留空时的兜底主名（页面占位与设计稿示例一致）
SEQUENCE_FALLBACK_BASE = "文件"
CONFLICT_MODES = ("skip", "suffix")
MAX_SUFFIX_TRIES = 9999


# ---------------------------------------------------------------- 名称规则


def natural_key(name: str):
    """自然排序键：数字按数值比较（图2 在 图10 之前），大小写不敏感。"""
    parts = re.split(r"(\d+)", str(name).casefold())
    return tuple(
        (0, int(part)) if part.isdigit() else (1, part) for part in parts if part != ""
    )


def invalid_reason(name: str) -> str:
    """返回名称不可用的原因，合法返回空串。

    口径按 Windows 兼容实现（目标平台含 Windows，Linux 上同样不生成这些名称）：
    空名、首尾空白、结尾点号、路径分隔符与 ``\\/:*?"<>|``、控制字符、设备保留名、
    超长（255 字符）。
    """
    text = str(name or "")
    if not text.strip():
        return "名称为空"
    if text in (".", ".."):
        return "名称不能是 . 或 .."
    if text != text.strip():
        return "名称首尾不能有空白"
    if text.endswith("."):
        return "名称不能以点号结尾"
    for ch in text:
        if ch in INVALID_CHARS:
            return "名称不能包含 %s" % ch
        if ord(ch) < 32:
            return "名称不能包含控制字符"
    stem = text.split(".", 1)[0].strip().upper()
    if stem in RESERVED_NAMES:
        return "名称 %s 是系统保留名" % stem
    if len(text) > MAX_NAME_CHARS:
        return "名称超过 %d 个字符" % MAX_NAME_CHARS
    return ""


def split_name(name: str, keep_extension: bool = True):
    """拆成（主名, 扩展名）；keep_extension=False 时整名当主名，扩展名留空。"""
    if not keep_extension:
        return name, ""
    ext = Path(name).suffix
    if not ext:
        return name, ""
    return name[: len(name) - len(ext)], ext


# ---------------------------------------------------------------- 规则解析


def _as_int(value, default: int) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _as_bool(value, default: bool = False) -> bool:
    """页面参数可能来自表单，字符串形式的假值要按假处理。"""
    if value is None:
        return default
    if isinstance(value, str):
        return value.strip().lower() not in ("", "0", "false", "no", "off")
    return bool(value)


def _read_rules(params: dict) -> dict:
    raw_seq = params.get("sequence") or {}
    sequence = None
    if isinstance(raw_seq, dict) and _as_bool(raw_seq.get("enabled")):
        sequence = {
            "base": str(raw_seq.get("base") or "").strip(),
            "start": _as_int(raw_seq.get("start"), 1),
            "step": _as_int(raw_seq.get("step"), 1) or 1,
            "digits": max(1, min(9, _as_int(raw_seq.get("digits"), 1))),
        }
    raw_replace = params.get("replace") or []
    if isinstance(raw_replace, dict):  # 单组规则也接受
        raw_replace = [raw_replace]
    rules = []
    for item in raw_replace:
        if not isinstance(item, dict):
            continue
        find = str(item.get("find") or "")
        if not find:
            continue
        rules.append(
            {
                "find": find,
                "replace": str(item.get("replace") or ""),
                "case_sensitive": _as_bool(item.get("case_sensitive")),
            }
        )
    conflict = str(params.get("conflict") or "skip").strip().lower()
    if conflict not in CONFLICT_MODES:
        conflict = "skip"
    return {
        "replace": rules,
        "prefix": str(params.get("prefix") or ""),
        "suffix": str(params.get("suffix") or ""),
        "keep_extension": _as_bool(params.get("keep_extension"), True),
        "conflict": conflict,
        "sequence": sequence,
    }


def _collect(params: dict) -> list:
    """收集待处理项：files 保持传入顺序，directory 只展开第一层并按自然序。"""
    items = []
    seen = set()
    for raw in params.get("files") or []:
        text = str(raw or "").strip()
        if not text:
            continue
        path = Path(os.path.abspath(os.path.expanduser(text)))
        key = os.path.normcase(str(path))
        if key in seen:  # 同一文件重复加入只保留一次（Windows 下大小写也算同一路径）
            continue
        seen.add(key)
        if not path.exists():
            items.append((path, "文件不存在"))
            continue
        if not path.is_file():
            items.append((path, "不是文件"))
            continue
        items.append((path, ""))
    if items:
        return items
    directory = str(params.get("directory") or "").strip()
    if not directory:
        return []
    base = Path(os.path.abspath(os.path.expanduser(directory)))
    if not base.is_dir():
        return [(base, "目录不存在")]
    names = sorted((p.name for p in base.iterdir() if p.is_file()), key=natural_key)
    return [(base / name, "") for name in names]


def _transform(stem: str, rules: dict) -> str:
    text = stem
    for rule in rules["replace"]:
        find = rule["find"]
        repl = rule["replace"]
        if rule["case_sensitive"]:
            text = text.replace(find, repl)
        else:
            text = re.sub(
                re.escape(find), lambda _m, r=repl: r, text, flags=re.IGNORECASE
            )
    return rules["prefix"] + text + rules["suffix"]


def _sequence_stem(sequence: dict, number: int) -> str:
    """统一主名 + 序号；主名留空时按「文件」兜底（与页面/设计示例一致）。"""
    text = "%0*d" % (sequence["digits"], number)
    base = sequence["base"] or SEQUENCE_FALLBACK_BASE
    return base + "_" + text


def _path_key(path) -> str:
    return os.path.normcase(os.path.abspath(str(path)))


def _same_file(a, b) -> bool:
    try:
        return os.path.samefile(str(a), str(b))
    except OSError:
        return False


def _claim_key(directory, name: str) -> tuple:
    """批内占用键：目录 + 名称（大小写不敏感），不同目录的同名目标互不相干。"""
    return (_path_key(directory), name.casefold())


def _target_key(row: dict) -> str:
    """行的目标路径键：只换名称、目录不变。"""
    return _path_key(Path(row["path"]).with_name(row["new_name"]))


def _unique_name(stem: str, ext: str, claimed: dict, directory: Path, src: Path):
    """在 ``<stem> (N)<ext>`` 里找一个批内与磁盘都空着的名称。"""
    for n in range(2, MAX_SUFFIX_TRIES + 1):
        candidate = "%s (%d)%s" % (stem, n, ext)
        if invalid_reason(candidate):
            return ""
        if _claim_key(directory, candidate) in claimed:
            continue
        target = directory / candidate
        if os.path.lexists(str(target)) and not _same_file(src, target):
            continue
        return candidate
    return ""


# ---------------------------------------------------------------- 预检


def build_plan(params: dict) -> dict:
    """全量预检：返回 rows 与 can/skip/same 计数、warnings；只读磁盘，不落任何改动。

    序号模式下让位判 skip 的项不占号：把排除项登记后重新分配候选名并重新判定，
    直到一轮不再新增排除项（与设计稿 ``buildPreview`` 的「重算到稳定」一致，
    上界取行数 + 2）。非序号模式与 ``suffix`` 策略名字不随位置变化，只算一轮。
    """
    params = params or {}
    rules = _read_rules(params)
    items = _collect(params)
    # 本批现有文件（含后续会被改名的项）：它们的原名不算「磁盘同名」，
    # 属于同批占用，最终由循环/互换与两阶段提交处理。
    sources = {
        _path_key(path): index
        for index, (path, missing) in enumerate(items)
        if not missing
    }
    excluded = {}
    rows = []
    for _ in range(len(items) + 2):
        rows = _assign_names(items, rules, sources, excluded)
        if not _exclude_conflicts(rows, rules, excluded):
            break
    _mark_two_phase(rows)
    can = sum(1 for r in rows if r["status"] == "ok")
    skip = sum(1 for r in rows if r["status"] == "skip")
    same = sum(1 for r in rows if r["status"] == "same")
    return {
        "rows": rows,
        "total": len(rows),
        "can": can,
        "skip": skip,
        "same": same,
        "warnings": _warnings(rows, can, skip, same),
    }


def _copy_row(row: dict) -> dict:
    """复制一行并深拷贝 flags，供排除项跨轮保留判定结果。"""
    out = dict(row)
    out["flags"] = list(row.get("flags") or [])
    return out


def _assign_names(items: list, rules: dict, sources: dict, excluded: dict) -> list:
    """一轮候选名分配：``excluded`` 命中的项沿用排除时的判定，不改名、不占序号。"""
    rows = []
    claimed = {}
    counter = rules["sequence"]["start"] if rules["sequence"] else 0
    for index, (path, missing) in enumerate(items):
        held = excluded.get(index)
        if held is not None:
            rows.append(_copy_row(held))
            continue
        row = {
            "path": str(path),
            "name": path.name,
            "new_name": "",
            "status": "",
            "reason": "",
            "flags": [],
        }
        rows.append(row)
        if missing:
            row["status"] = "skip"
            row["reason"] = missing
            continue

        name = path.name
        stem, ext = split_name(name, True)
        keep_ext = rules["keep_extension"]
        numbered = rules["sequence"] is not None
        if numbered:
            # 序号名同样要套前后缀，替换规则不作用于生成名
            new_stem = (
                rules["prefix"]
                + _sequence_stem(rules["sequence"], counter)
                + rules["suffix"]
            )
            counter += rules["sequence"]["step"]
        else:
            new_stem = _transform(stem, rules)
        candidate = new_stem + (ext if keep_ext else "")
        out_ext = ext if keep_ext else ""

        reason = invalid_reason(candidate)
        if reason:
            row["new_name"] = candidate
            row["status"] = "skip"
            row["reason"] = "名称非法：" + reason
            if numbered:  # 非法项不占序号
                counter -= rules["sequence"]["step"]
            continue
        if candidate == name:
            row["new_name"] = candidate
            row["status"] = "same"
            if numbered:  # 未变化项不占序号
                counter -= rules["sequence"]["step"]
            continue
        row["new_name"] = candidate
        if candidate.casefold() == name.casefold():
            row["flags"].append("case_only")

        target = path.with_name(candidate)
        in_batch = _claim_key(path.parent, candidate) in claimed
        held_by_batch = sources.get(_path_key(target)) not in (None, index)
        on_disk = (
            os.path.lexists(str(target))
            and not _same_file(path, target)
            and not held_by_batch
        )
        if not in_batch and not on_disk:
            row["status"] = "ok"
            claimed[_claim_key(path.parent, candidate)] = row
            continue
        if rules["conflict"] != "suffix":
            row["status"] = "skip"
            row["reason"] = "与批内其他项目标互撞" if in_batch else "目标名称已存在"
            if numbered:  # 跳过项不占序号：后续项仍按连续序号尝试
                counter -= rules["sequence"]["step"]
            continue
        found = _unique_name(
            candidate[: len(candidate) - len(out_ext)] if out_ext else candidate,
            out_ext,
            claimed,
            path.parent,
            path,
        )
        if not found:
            row["status"] = "skip"
            row["reason"] = "无法生成不冲突的新名称"
            if numbered:
                counter -= rules["sequence"]["step"]
            continue
        row["new_name"] = found
        row["status"] = "ok"
        claimed[_claim_key(path.parent, found)] = row

    return rows


def _exclude_conflicts(rows: list, rules: dict, excluded: dict) -> int:
    """让位判 skip 的项登记为排除项，返回本轮新增数量。

    只有「序号 + skip 策略」需要据此重算候选名；其余情况名字不随位置变化，
    重算也不会产出新结论，直接返回 0 结束外层循环。
    """
    before = len(excluded)
    _settle_held_targets(rows, rules, excluded)
    if rules["sequence"] is None or rules["conflict"] != "skip":
        return 0
    return len(excluded) - before


def _settle_held_targets(rows: list, rules: dict, excluded=None):
    """目标名恰好是同批某个最终不改名文件的原名：按磁盘同名处理（跳过或追加序号）。

    同一批里「目标被占用」分两种：占用者也会改名（循环/互换，两阶段提交解决），
    与占用者保留原名（未变化或已判跳过，必须在预检阶段就让位）。这里处理后者。

    收敛口径与「每轮全表扫描」的朴素做法一致：占用者按轮取快照（本轮新判跳过的行
    从下一轮起才算占用者），同一轮内按行号升序复核，批内占用表随复核实时更新。因此
    一条长度为 N 的让位链需要 N 轮；上界仍取行数 + 2，既保证链尾到链首全部传导，
    又保证有界终止（每轮至少改判一项，改判只发生一次）。

    实现上只复核受影响的行：进入时一次性建立「原名 → 行下标」「目标名 → 行下标」
    两张索引与批内占用表，之后每轮只处理目标名落在已判非可执行项原名上的行；某行
    让位或改名时把占用表与目标索引就地增删，并把目标落在它原名上的上游行排进下一轮。
    这样全表不再逐轮重建，数千项长链的耗时与行数同阶。

    传入 ``excluded`` 时把改判为 skip 的行登记进去，交给 ``build_plan`` 重算序号：
    让位项保持原名、不占号，后续可提交项仍拿连续编号。
    """
    total = len(rows)
    if not total:
        return
    suffix_mode = rules["conflict"] == "suffix"
    owners = {}  # 原名键 → 行下标：最终不改名的行
    targets = {}  # 目标名键 → 行下标列表：目标落在这个名字上的可执行行
    claimed = {}  # 批内占用键 → 行下标：可执行行已占的目标名

    for index, row in enumerate(rows):
        if row["status"] != "ok":
            owners[_path_key(row["path"])] = index
            continue
        claimed[_claim_key(Path(row["path"]).parent, row["new_name"])] = index
        targets.setdefault(_target_key(row), []).append(index)

    pending = sorted(
        index
        for index in range(total)
        if rows[index]["status"] == "ok" and _target_key(rows[index]) in owners
    )
    for _ in range(total + 2):
        if not pending:
            return
        current, next_round = pending, set()
        pending = []
        for index in current:
            row = rows[index]
            src = Path(row["path"])
            key = _target_key(row)
            holder = owners.get(key)
            if holder is None or holder == index:
                continue
            # 本行不再是可执行项（或目标名已换）：占用表与目标索引同步摘掉旧目标
            claimed.pop(_claim_key(src.parent, row["new_name"]), None)
            holders = targets.get(key)
            if holders is not None:
                holders.remove(index)
                if not holders:
                    del targets[key]
            if not suffix_mode:
                row["status"] = "skip"
                row["reason"] = "目标名称已存在"
                if excluded is not None:
                    excluded[index] = _copy_row(row)
            else:
                stem, ext = split_name(row["new_name"], True)
                found = _unique_name(stem, ext, claimed, src.parent, src)
                if not found:
                    row["status"] = "skip"
                    row["reason"] = "无法生成不冲突的新名称"
                    if excluded is not None:
                        excluded[index] = _copy_row(row)
                else:
                    row["new_name"] = found
                    claimed[_claim_key(src.parent, found)] = index
                    new_key = _target_key(row)
                    targets.setdefault(new_key, []).append(index)
                    if new_key in owners:  # 新目标又落到保留名上：下一轮复核
                        next_round.add(index)
            if row["status"] != "ok":
                owners[_path_key(src)] = index
                for upstream in targets.get(_path_key(src), ()):
                    next_round.add(upstream)
        pending = sorted(next_round)


def _mark_two_phase(rows: list):
    """标记需要同目录临时名的项：目标被同批其他项占用、仅大小写变化、循环改名。"""
    ok = [r for r in rows if r["status"] == "ok"]
    if not ok:
        return
    sources = {_path_key(r["path"]): r for r in ok}
    for row in ok:
        src = Path(row["path"])
        target_key = _path_key(src.with_name(row["new_name"]))
        if target_key == _path_key(src):
            if "case_only" in row["flags"]:
                row["flags"].append("needs_temp")
            continue
        if target_key in sources:
            row["flags"].append("needs_temp")
    for row in ok:
        if "needs_temp" in row["flags"] and _in_cycle(row, sources):
            row["flags"].append("cycle")


def _in_cycle(row: dict, sources: dict) -> bool:
    """沿「目标名 → 该名称当前持有者」的链条回溯，回到自身即成环。"""
    src = Path(row["path"])
    if _path_key(src.with_name(row["new_name"])) == _path_key(src):
        return False
    current = row
    for _ in range(len(sources) + 1):
        target = Path(current["path"]).with_name(current["new_name"])
        nxt = sources.get(_path_key(target))
        if nxt is None:
            return False
        if nxt is row:
            return True
        current = nxt
    return True


def _warnings(rows: list, can: int, skip: int, same: int) -> list:
    lines = []
    if not rows:
        lines.append("未选择任何文件或目录")
        return lines
    if can:
        lines.append("%d 项可以改名，执行后不可撤销" % can)
    if skip:
        reasons = {}
        for row in rows:
            if row["status"] != "skip":
                continue
            reasons[row["reason"]] = reasons.get(row["reason"], 0) + 1
        detail = "；".join("%s %d 项" % (k, v) for k, v in reasons.items())
        lines.append("%d 项将被跳过：%s" % (skip, detail))
    if same:
        lines.append("%d 项名称未变化，不会执行" % same)
    if any("cycle" in row["flags"] for row in rows):
        lines.append("存在循环改名，将以同目录临时名两阶段提交")
    if any("case_only" in row["flags"] for row in rows):
        lines.append("存在仅大小写变化的名称，将以同目录临时名两阶段提交")
    return lines


# ---------------------------------------------------------------- 提交


def _temp_path(src: Path) -> Path:
    token = secrets.token_hex(4)
    for i in range(100):
        candidate = src.parent / ("%s%s-%d" % (TEMP_PREFIX, token, i))
        if not os.path.lexists(str(candidate)):
            return candidate
    raise RuntimeError("无法创建临时文件名：%s" % src.parent)


def _rename_no_replace(src, dst: Path):
    """同目录改名且绝不覆盖：目标已存在直接报错。

    Windows 的 os.rename 本身不覆盖；POSIX 的 rename 会静默替换目标，因此先建硬链接
    （目标已存在时 link 失败，天然不会覆盖）再删原名；文件系统不支持硬链接时退回
    “复查 + rename”。
    """
    if os.path.lexists(str(dst)):
        raise FileExistsError("目标已存在：%s" % dst)
    if os.name != "nt":
        try:
            os.link(str(src), str(dst), follow_symlinks=False)
        except FileExistsError:
            raise
        except OSError:
            pass  # 不支持硬链接（部分网络盘/特殊文件系统）时走下面的 rename
        else:
            try:
                os.unlink(str(src))
            except OSError:
                try:
                    os.unlink(str(dst))
                except OSError:
                    pass
                raise
            return
    if os.path.lexists(str(dst)):
        raise FileExistsError("目标已存在：%s" % dst)
    os.rename(str(src), str(dst))


def _rollback(path_from: Path, original: Path) -> tuple:
    """把文件放回原名，绝不换名兜底（不允许出现「原名 (2)」）。

    返回 (ok, 说明)；只在暂存文件已消失、原名确实被其他文件占用或系统拒绝时才失败，
    失败信息交给调用方决定如何记日志与标注结果——回滚绝不静默。
    """
    if not os.path.lexists(str(path_from)):
        # 暂存文件被外部删除：既没改回原名也不是「无事可做」，必须报失败
        return False, "暂存文件已被外部删除：%s" % path_from
    if _same_file(path_from, original):
        # 仅大小写改名等场景下两个名字本就指向同一文件（大小写不敏感的文件系统），
        # 这不是「原名被占用」，无需再改名。
        return True, ""
    try:
        _rename_no_replace(path_from, original)
        return True, ""
    except FileExistsError:
        return False, "原名已被占用：%s，文件仍在 %s" % (original, path_from)
    except OSError as exc:
        return False, "放回 %s 失败（%s），文件仍在 %s" % (
            original,
            _error_text(exc),
            path_from,
        )


def _rollback_group(pairs: list, job=None) -> dict:
    """整组回滚：多轮重试化解「谁先回谁」的链式占用，返回 {行下标: 失败说明}。

    回滚路径之间可能互为对方的原名（环/链），顺序不当会误判成占用；每轮只要有一项
    成功就再来一轮，最终仍失败的才是真占用（外部新建了同名文件等），记日志并上报。
    """
    pending = list(pairs)
    notes = {}
    for _ in range(len(pairs) + 1):
        if not pending:
            break
        retry = []
        for index, path_from, original in pending:
            ok, detail = _rollback(path_from, original)
            if ok:
                notes.pop(index, None)
            else:
                notes[index] = detail
                retry.append((index, path_from, original))
        if len(retry) == len(pending):
            break
        pending = retry
    for detail in notes.values():
        if job is not None:
            job.log("回滚异常：%s" % detail)
    return notes


def _error_text(exc: BaseException) -> str:
    text = exc.strerror if isinstance(exc, OSError) and exc.strerror else str(exc)
    return text or exc.__class__.__name__


def _apply_single(plan: list, index: int, job=None) -> tuple:
    """普通项：一步改名，失败只影响自己。"""
    row = plan[index]
    src = Path(row["path"])
    target = src.with_name(row["new_name"])
    try:
        _rename_no_replace(src, target)
    except OSError as exc:
        if job is not None:
            job.log(
                "失败：%s → %s（%s）" % (row["name"], row["new_name"], _error_text(exc))
            )
        return ("failed", _error_text(exc))
    if job is not None:
        job.log("成功：%s → %s" % (row["name"], row["new_name"]))
    return ("ok", "")


def _group_staged(plan: list) -> list:
    """把 needs_temp 项聚成交付组：同目录内「目标名正是另一项原名」即相连。

    一个组是一个提交单元——组内先全部暂存、再全部落位，任何一步失败整组回滚。
    与普通项无关（普通项已在预检阶段让出名字）。
    """
    indexes = [
        i
        for i, row in enumerate(plan)
        if row.get("status") == "ok" and "needs_temp" in row["flags"]
    ]
    parent = {i: i for i in indexes}

    def find(x):
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a, b):
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[rb] = ra

    by_source = {}
    for i in indexes:
        src = Path(plan[i]["path"])
        by_source.setdefault(_path_key(src.parent), {})[_path_key(src)] = i
    for i in indexes:
        src = Path(plan[i]["path"])
        target = src.with_name(plan[i]["new_name"])
        holder = by_source.get(_path_key(src.parent), {}).get(_path_key(target))
        if holder is not None and holder != i:
            union(i, holder)

    groups = {}
    for i in indexes:  # 按原顺序归组，保持组的相对次序稳定
        groups.setdefault(find(i), []).append(i)
    return list(groups.values())


def _commit_group(plan: list, indexes: list, job=None) -> dict:
    """同目录临时名两阶段提交一个组；失败整组回滚，结果如实反映磁盘状态。

    返回 ``{行下标: ("ok"|"not_run"|"failed", 说明)}``。未进入暂存的项一个字节都没动
    （not_run），已暂存或已落位而后回滚成功的项也算未执行；回滚失败必须记 failed，
    并在说明里给出文件当前所在路径，保证结果清单与磁盘一致。
    """
    staged = []
    placed = {}
    failed_at = None
    error = ""
    for i in indexes:
        src = Path(plan[i]["path"])
        temp = _temp_path(src)
        try:
            _rename_no_replace(src, temp)
        except OSError as exc:
            failed_at, error = i, _error_text(exc)
            break
        staged.append((i, temp))

    if failed_at is None:
        for i, temp in staged:
            target = Path(plan[i]["path"]).with_name(plan[i]["new_name"])
            try:
                _rename_no_replace(temp, target)
            except OSError as exc:
                failed_at, error = i, _error_text(exc)
                break
            placed[i] = target

    if failed_at is None:
        results = {}
        for i in indexes:
            if job is not None:
                job.log("成功：%s → %s" % (plan[i]["name"], plan[i]["new_name"]))
            results[i] = ("ok", "")
        return results

    # 整组回滚：已落位的从目标名退回原名，仍在暂存名的直接放回原名；未暂存的项没动过
    staged_map = dict(staged)
    pairs = [
        (i, placed.get(i) or staged_map[i], Path(plan[i]["path"]))
        for i in indexes
        if i in staged_map
    ]
    notes = _rollback_group(pairs, job)
    if job is not None:
        job.log(
            "失败：%s → %s（%s），整组 %d 项已回滚"
            % (
                plan[failed_at]["name"],
                plan[failed_at]["new_name"],
                error,
                len(indexes),
            )
        )

    results = {}
    for i in indexes:
        detail = notes.get(i, "")
        if i == failed_at:
            message = "改名失败（%s），整组已回滚" % error
        elif i in staged_map:
            message = "同组 %s 失败，已回滚" % plan[failed_at]["name"]
        else:
            message = "同组 %s 失败，未执行" % plan[failed_at]["name"]
        if detail:
            message += "；" + detail
        # 回滚成功：未落地的项就等于没执行（not_run）；回滚失败：磁盘状态异常，必须报 failed
        kind = "failed" if (i == failed_at or detail) else "not_run"
        results[i] = (kind, message)
    return results


def apply_plan(rows: list, job=None) -> dict:
    """按预检结果执行：普通项逐项提交，needs_temp 项按组两阶段提交。

    组是提交单元：只在组边界 check_cancel，组内不打断；组内任何失败整组回滚，
    回滚绝不改名兜底（不产生「原名 (2)」），失败如实写日志与结果。

    普通项先于组执行（先把被占用的原名腾出来）。入参应为 ``build_plan`` 的结果——
    预检已把「目标名是另一项原名」的项标成 needs_temp 并归组，保证链式改名不会
    因为顺序问题误报磁盘同名。
    """
    plan = [dict(row) for row in rows]
    total = len(plan)
    verdicts = [None] * total
    for i, row in enumerate(plan):
        if row.get("status") == "skip":
            verdicts[i] = ("skipped", row.get("reason", ""))
        elif row.get("status") == "same":
            verdicts[i] = ("unchanged", "")
        elif row.get("status") != "ok":
            verdicts[i] = ("not_run", "预检未通过")

    direct_units = [
        [i]
        for i, row in enumerate(plan)
        if row.get("status") == "ok" and "needs_temp" not in row["flags"]
    ]
    # 普通项必须排在暂存组之前：它们要先把被占用的原名腾出来。
    ordered = direct_units + _group_staged(plan)

    cancelled = False
    done = 0

    def report(label):
        nonlocal done
        done += 1
        if job is not None:
            job.set_progress(done, total, label)

    for unit in ordered:
        try:
            if job is not None:
                job.check_cancel()  # 只在单元（组）边界响应取消
            if "needs_temp" not in plan[unit[0]]["flags"]:
                verdicts[unit[0]] = _apply_single(plan, unit[0], job)
                report(plan[unit[0]]["name"])
                continue
            unit_result = _commit_group(plan, unit, job)
        except Cancelled:
            cancelled = True
            break
        for i in unit:
            verdicts[i] = unit_result[i]
        for i in unit:
            report(plan[i]["name"])

    for i in range(total):
        if verdicts[i] is None:
            verdicts[i] = ("not_run", "已取消" if cancelled else "未执行")

    counts = {"success": 0, "failed": 0, "skipped": 0, "unchanged": 0, "not_run": 0}
    key_of = {
        "ok": "success",
        "failed": "failed",
        "skipped": "skipped",
        "unchanged": "unchanged",
        "not_run": "not_run",
    }
    out_rows = []
    for row, verdict in zip(plan, verdicts):
        kind, message = verdict
        out = dict(row)
        out["flags"] = list(row.get("flags") or [])
        out["result"] = key_of[kind]
        counts[key_of[kind]] += 1
        if message:
            out["message"] = message
        out_rows.append(out)

    if job is not None:
        # 正常跑完：跳过/未变化项也算处理过，进度按总数拉满；
        # 取消：停在已完成的提交项，不把未执行项算进去。
        job.set_progress(
            total if not cancelled else done, total, "已取消" if cancelled else "完成"
        )
    outcome = dict(counts)
    outcome["cancelled"] = cancelled
    outcome["rows"] = out_rows
    return outcome


# ---------------------------------------------------------------- 单图改名


def rename_one(path, new_base: str) -> Path:
    """图片查看器单图改名：只改主名、保留原扩展名、目标已存在时报错且不覆盖。

    传入带扩展名的新名（如 ``b.png``）不会叠成 ``b.png.png``；仅大小写变化走同目录
    临时名，兼容 Windows 的大小写不敏感文件系统。
    """
    src = Path(path)
    if not src.exists():
        raise FileNotFoundError("文件不存在：%s" % src)
    if not src.is_file():
        raise ValueError("不是文件：%s" % src)
    ext = src.suffix
    base = str(new_base or "").strip()
    if ext and base.lower().endswith(ext.lower()):
        base = base[: len(base) - len(ext)].strip()
    reason = invalid_reason(base)
    if reason:
        raise ValueError("名称非法：" + reason)
    target_name = base + ext
    reason = invalid_reason(target_name)
    if reason:
        raise ValueError("名称非法：" + reason)
    if target_name == src.name:
        return src
    target = src.with_name(target_name)
    case_only = target_name.casefold() == src.name.casefold()
    if not case_only and os.path.lexists(str(target)):
        raise FileExistsError("目标已存在：%s" % target)
    if not case_only:
        _rename_no_replace(src, target)
        return target
    temp = _temp_path(src)
    _rename_no_replace(src, temp)
    try:
        _rename_no_replace(temp, target)
    except OSError:
        _rollback(temp, src)
        raise
    return target
