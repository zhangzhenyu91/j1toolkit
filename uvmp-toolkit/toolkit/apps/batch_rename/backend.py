# -*- coding: utf-8 -*-
"""批量重命名：任意类型文件通用，只改名称、不移动位置、绝不覆盖。

流程与设计稿（design/桌面-批量重命名.html）一致：
- ``preview(params)``：全量预检并只读返回 rows/can/skip/same/warnings，前端只渲染；
- ``run(job, params)``：执行前重新预检（预览之后磁盘可能已变化），再逐项提交，
  逐行日志 + 进度 + 取消钩子，返回 success/failed/skipped/unchanged/not_run 与
  逐项结果，冲突项永不写入。

参数（前后端契约）：
    files            显式文件列表（任意类型，保持传入顺序）
    directory        目录（仅展开第一层文件，按自然序）；files 非空时忽略
    replace          [{find, replace, case_sensitive}] 查找替换（只作用于主名）
    prefix / suffix  主名前缀/后缀
    sequence         {enabled, base, start, step, digits} 统一主名 + 序号
                     （主名留空按「文件」兜底；序号名同样套 prefix/suffix）
    keep_extension   是否保留扩展名（默认 True）
    conflict         skip（默认，跳过并报告）| suffix（自动追加序号）

不提供覆盖选项，也没有撤销栈：确认弹层是唯一的防误入口（在界面侧实现）。
循环/互换/仅大小写变化按「提交组」执行：只在组边界响应取消，组内失败整组回滚。
"""

import os

from fileops import naming

APP_ID = "batch_rename"


def _output_dir(rows: list) -> str:
    """所有实际改名的项都在同一目录时返回该目录，供「打开产物目录」；否则留空。"""
    dirs = set()
    for row in rows:
        if row.get("status") != "ok" or row.get("result") not in (None, "success"):
            continue
        dirs.add(os.path.dirname(os.path.abspath(row["path"])))
    return dirs.pop() if len(dirs) == 1 else ""


def preview(params: dict) -> dict:
    """只读预检：非法名/未变化/互撞/磁盘同名/仅大小写/循环都在这里算完。"""
    built = naming.build_plan(params or {})
    built["output_dir"] = _output_dir(built["rows"])
    return built


def _summary_text(result: dict) -> str:
    parts = ["成功 %d" % result["success"], "失败 %d" % result["failed"]]
    if result["skipped"]:
        parts.append("跳过 %d" % result["skipped"])
    if result["unchanged"]:
        parts.append("未变化 %d" % result["unchanged"])
    if result["not_run"]:
        parts.append("未执行 %d" % result["not_run"])
    return "，".join(parts)


def run(job, params: dict) -> dict:
    params = params or {}
    built = naming.build_plan(params)
    rows = built["rows"]
    total = len(rows)
    job.log(
        "预检完成：共 %d 项，可改名 %d，跳过 %d，未变化 %d"
        % (total, built["can"], built["skip"], built["same"])
    )
    for warning in built["warnings"]:
        job.log(warning)
    conflicts = [r for r in rows if "互撞" in (r.get("reason") or "")]
    if conflicts:
        for row in conflicts:
            job.log(
                "冲突：%s 的目标 %s 与同批其他项重复" % (row["name"], row["new_name"])
            )
    on_disk = [r for r in rows if "已存在" in (r.get("reason") or "")]
    for row in on_disk:
        job.log(
            "已存在：%s 的目标 %s 在磁盘上已有同名文件，不覆盖"
            % (row["name"], row["new_name"])
        )
    illegal = [r for r in rows if (r.get("reason") or "").startswith("名称非法")]
    for row in illegal:
        job.log("非法：%s → %s（%s）" % (row["name"], row["new_name"], row["reason"]))

    outcome = naming.apply_plan(rows, job=job)
    for row in outcome["rows"]:
        if row["result"] == "skipped":
            job.log(
                "跳过：%s（%s）"
                % (row["name"], row.get("message") or row.get("reason", ""))
            )
        elif row["result"] == "unchanged":
            job.log("未变化：%s" % row["name"])

    result = {
        "success": outcome["success"],
        "failed": outcome["failed"],
        "skipped": outcome["skipped"],
        "unchanged": outcome["unchanged"],
        "not_run": outcome["not_run"],
        "cancelled": outcome["cancelled"],
        "rows": outcome["rows"],
        "output_dir": _output_dir(outcome["rows"]),
    }
    if outcome["cancelled"]:
        # 进度按「实际已处理单元数」报：成功与失败都已尝试过，不能只算成功
        handled = outcome["success"] + outcome["failed"]
        job.log(
            "任务已取消：已完成 %d 项、失败 %d 项、未执行 %d 项，进度停在 %d/%d"
            % (
                outcome["success"],
                outcome["failed"],
                outcome["not_run"],
                handled,
                total,
            )
        )
    else:
        job.log("重命名完成：" + _summary_text(result))
    return result
