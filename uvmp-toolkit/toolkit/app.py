#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""内网工具箱入口。

用法：
  python3 app.py                  启动桌面客户端（默认；已运行则唤起窗口）
  python3 app.py daily-export [--if-due] [--date YYYY-MM-DD]
                                  每日派车单同步（定时器调用；--if-due 时做 due-check 判定）
  python3 app.py order-export --outdir 目录 (--xlsx 清单.xlsx | --codes "单号1,单号2")
                              [--type pcd|track|all] [--no-batch-dir]
                                  按派车单号按序导出（无头模式，供计划任务/调试）
  python3 app.py rpc                    # stdio JSON-RPC 服务（Electron 壳 spawn 调用）
退出码：0 成功/未到点；1 失败；2 GUI 依赖缺失。
"""

import argparse
import json
import sys
from pathlib import Path

TOOLKIT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TOOLKIT_DIR.parent
for _p in (str(TOOLKIT_DIR), str(PROJECT_DIR)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import config as _config  # noqa: E402
import jobs as _jobs  # noqa: E402
import scheduler  # noqa: E402
import state  # noqa: E402


def cmd_gui(cfg: dict) -> int:
    """桌面客户端已迁移到 Electron 版（desktop/，安装包见 Release）；
    本入口（uvmp-core）仅保留 CLI 与 RPC 服务能力"""
    print(
        "GUI 已迁移到 Electron 桌面客户端（见 Release 的安装包/桌面端源码 desktop/）。"
    )
    print(
        "本程序仅提供命令行与 RPC 能力：daily-export / order-export / selfcheck / rpc"
    )
    return 2


def _load_backend(app_id: str):
    from apps import scan_apps

    for app in scan_apps():
        if app["id"] == app_id and app.get("_backend"):
            return app["_backend"]
    raise SystemExit("应用不存在或后端加载失败: " + app_id)


def _run_headless(app_id: str, title: str, params: dict) -> int:
    backend = _load_backend(app_id)
    mgr = _jobs.JobManager()
    job = mgr.create(app_id, title)
    mgr.run_sync(job, lambda j: backend.run(j, params))
    print("-" * 50)
    print("任务状态：%s" % job.status)
    if job.result:
        print("结果：%s" % json.dumps(job.result, ensure_ascii=False, indent=1))
    return 0 if job.status == "success" else 1


def cmd_daily_export(cfg: dict, args) -> int:
    if args.if_due:
        due, reason, _today = scheduler.due_check(cfg)
        if not due:
            print("[daily-export] 跳过：%s" % reason)
            return 0
        scheduler.mark_attempt(_today)  # 分钟级 tick 下拉开失败重试间隔
    return _run_headless("daily_sync", "每日派车单同步", {"date": args.date or ""})


def cmd_tree_sync(cfg: dict, args) -> int:
    """树障统计表同步：--tick 每分钟轮询 U盘指令（不做 due_check，U盘不在秒退）；
    --nightly --if-due 夜间兜底（到点判定后执行，无指令也 snapshot 一次）。"""
    if args.nightly:
        if args.if_due:
            ts = cfg.get("tree_sync") or {}
            if not (ts.get("local_path") or "").strip():
                print("[tree-sync] 跳过：未配置内网表路径（tree_sync.local_path）")
                return 0
            sch = {
                "enabled": True,
                "time": ts.get("nightly_time", "23:55"),
                "driver": (cfg.get("schedule") or {}).get("driver", "os"),
            }
            due, reason, _today = scheduler.due_check(
                cfg, stamp_name="tree_sync_nightly", schedule=sch
            )
            if not due:
                print("[tree-sync] 跳过：%s" % reason)
                return 0
            scheduler.mark_attempt(_today, stamp_name="tree_sync_nightly")
        return _run_headless("tree_sync", "树障统计表同步", {"nightly": True})
    return _run_headless("tree_sync", "树障统计表同步", {"tick": True})


def cmd_crossing_sync(cfg: dict, args) -> int:
    """交跨统计表同步：--tick 每分钟轮询 U盘指令（不做 due_check，U盘不在秒退）；
    --nightly --if-due 夜间兜底（到点判定后执行，无指令也 snapshot 一次）。"""
    if args.nightly:
        if args.if_due:
            ts = cfg.get("crossing_sync") or {}
            if not (ts.get("local_path") or "").strip():
                print(
                    "[crossing-sync] 跳过：未配置内网表路径（crossing_sync.local_path）"
                )
                return 0
            sch = {
                "enabled": True,
                "time": ts.get("nightly_time", "23:55"),
                "driver": (cfg.get("schedule") or {}).get("driver", "os"),
            }
            due, reason, _today = scheduler.due_check(
                cfg, stamp_name="crossing_sync_nightly", schedule=sch
            )
            if not due:
                print("[crossing-sync] 跳过：%s" % reason)
                return 0
            scheduler.mark_attempt(_today, stamp_name="crossing_sync_nightly")
        return _run_headless("crossing_sync", "交跨统计表同步", {"nightly": True})
    return _run_headless("crossing_sync", "交跨统计表同步", {"tick": True})


def cmd_order_export(cfg: dict, args) -> int:
    params = {
        "outdir": args.outdir,
        "batch_subdir": not args.no_batch_dir,
        "batch_name": Path(args.xlsx).stem if args.xlsx else "派车单",
        "export_pcd": args.type in ("pcd", "all"),
        "export_track": args.type in ("track", "all"),
        "calibrate_noon": args.calibrate_noon,
    }
    if args.xlsx:
        params["xlsx_path"] = args.xlsx
    elif args.codes:
        params["codes_text"] = args.codes
    else:
        raise SystemExit("请用 --xlsx 或 --codes 提供派车单号来源")
    return _run_headless("pcd_export", "派车单·轨迹导出", params)


def cmd_selfcheck() -> int:
    """构建冒烟：加载全部应用后端（覆盖 import 链：platform_utils/uvmp/vehicle_export…）。
    PyInstaller 打包后跑一遍可暴露缺失的 hidden-import / DLL 收集问题。"""
    from apps import scan_apps

    apps = scan_apps()
    if not apps:
        print("selfcheck 失败：未发现任何应用")
        return 1
    ok = True
    for a in apps:
        if a.get("_backend"):
            print("  后端 OK: %s" % a["id"])
        else:
            print("  后端缺失: %s" % a["id"])
            ok = False
    import vehicle_export  # noqa: F401
    import platform_utils  # noqa: F401

    print("selfcheck %s" % ("通过" if ok else "失败"))
    return 0 if ok else 1


def main() -> int:
    # 控制台编码兜底：冻结成 console 程序后，在英文区机器（cp1252）上 print 中文会崩
    # （CI 已踩坑）；统一重配 UTF-8，非法字节替换而非抛异常
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass  # windowed 冻结版 stdout 是 None
    ap = argparse.ArgumentParser(prog="app.py", description="内网工具箱")
    sub = ap.add_subparsers(dest="cmd")
    sub.add_parser("gui", help="启动桌面客户端（默认）")
    sub.add_parser("selfcheck", help="自检：加载全部应用后端（构建冒烟用）")
    sub.add_parser("rpc", help="stdio JSON-RPC 服务（Electron 壳调用）")
    p1 = sub.add_parser("daily-export", help="每日派车单同步")
    p1.add_argument(
        "--if-due", action="store_true", help="先按配置时间做 due-check 判定"
    )
    p1.add_argument("--date", default="", help="导出日期 YYYY-MM-DD（默认今天）")
    p3 = sub.add_parser("tree-sync", help="树障统计表同步（U盘指令轮询 / 夜间兜底）")
    g3 = p3.add_mutually_exclusive_group()
    g3.add_argument(
        "--tick", action="store_true", help="轮询一次 U盘指令（默认；每分钟定时器用）"
    )
    g3.add_argument(
        "--nightly", action="store_true", help="夜间兜底：无指令也 snapshot 一次"
    )
    p3.add_argument(
        "--if-due",
        action="store_true",
        help="仅配合 --nightly：先按 tree_sync.nightly_time 做 due-check 判定",
    )
    p4 = sub.add_parser(
        "crossing-sync", help="交跨统计表同步（U盘指令轮询 / 夜间兜底）"
    )
    g4 = p4.add_mutually_exclusive_group()
    g4.add_argument(
        "--tick", action="store_true", help="轮询一次 U盘指令（默认；每分钟定时器用）"
    )
    g4.add_argument(
        "--nightly", action="store_true", help="夜间兜底：无指令也 snapshot 一次"
    )
    p4.add_argument(
        "--if-due",
        action="store_true",
        help="仅配合 --nightly：先按 crossing_sync.nightly_time 做 due-check 判定",
    )
    p2 = sub.add_parser("order-export", help="按派车单号按序导出")
    p2.add_argument("--xlsx", help="派车单号清单 xlsx 路径")
    p2.add_argument("--codes", help="直接给单号（逗号/空格分隔）")
    p2.add_argument("--outdir", required=True, help="输出目录")
    p2.add_argument("--type", choices=["pcd", "track", "all"], default="all")
    p2.add_argument(
        "--no-batch-dir", action="store_true", help="不建批次子目录，直接写入输出目录"
    )
    p2.add_argument(
        "--calibrate-noon",
        dest="calibrate_noon",
        action="store_true",
        help="自动校准行程结束时间：早于12点的行程按1小时逐次叠加至12点后，行驶时间同步增加",
    )
    args = ap.parse_args()

    cfg = _config.load()
    # 启动自洁：清理 30 天前的任务日志/档案（rpc/daily-export/order-export 各入口均经此处）
    try:
        state.prune_old_logs(30)
    except Exception:  # noqa: BLE001
        pass
    if args.cmd in (None, "gui"):
        return cmd_gui(cfg)
    if args.cmd == "selfcheck":
        return cmd_selfcheck()
    if args.cmd == "rpc":
        import rpc as _rpc

        _rpc.main()
        return 0
    if args.cmd == "daily-export":
        return cmd_daily_export(cfg, args)
    if args.cmd == "tree-sync":
        return cmd_tree_sync(cfg, args)
    if args.cmd == "crossing-sync":
        return cmd_crossing_sync(cfg, args)
    if args.cmd == "order-export":
        return cmd_order_export(cfg, args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
