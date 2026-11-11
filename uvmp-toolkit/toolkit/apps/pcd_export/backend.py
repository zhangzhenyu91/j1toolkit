# -*- coding: utf-8 -*-
"""按派车单号清单导出派车单/轨迹 PDF（旧 Python 编排 + 本地线程并发）。

编排（解析单号 → 并发查询 → 派车单渲染 → 轨迹渲染 → 清单/未找到报告）全部在本进程
完成，不再调用外部导出工作进程；查询与渲染原语复用 vehicle_export.py / official_track.py。
两级并发（parallel.py）：
  - 查询阶段 ThreadPoolExecutor(max_workers=query_workers)，每线程独立 UvmpClient/Session，
    共享 Auth 仅保留刷新锁；
  - 派车单/轨迹两个渲染阶段先后执行，各用 ThreadPoolExecutor(max_workers=render_workers)；
    每个工作项在独占 worker 槽内完成“详情/轨迹数据准备 → 官方渲染 → PDF（轨迹点 CSV
    先写同目录临时文件）”，worker 各自懒启动一个 OfficialTrackRenderer，目录隔离在
    state/browser-workers/<job>/。
主线程按输入顺序收集结果，并在 job.commit() 内完成最终 CSV 原子替换、manifest 落盘与
rows/进度/日志成功回调（与取消共用提交锁，避免取消后的成功回调或半提交）。

命名规则（《内网工具箱开发指南.md》定为规范）：
  <输出目录>/<批次名>_导出_YYYYMMDD_HHMMSS/
    _合并_派车单.pdf / _合并_轨迹.pdf   （全部导完后按序号顺序合并，不足 2 个不出）
    _清单.csv / _未找到.txt
    _12点前结束行程.xlsx               （轨迹导出时统计：行程结束时间在当日 12 点前；
                                        序号（合并轨迹PDF页码）/派车单号/车牌号/日期/行程结束时间）
    逐单/
      NNN_派车单_<runCode>_<车牌>_<用车日期>.pdf
      NNN_轨迹_<runCode>_<车牌>_<用车日期>.pdf      （仅已完结且有实际出/归车时间的单）
      NNN_轨迹点_<runCode>.csv
  NNN = 清单行序，按最大位数补零；批次目录内 manifest.json 支持重跑断点续传。

可选参数 calibrate_noon（前端「自动校准行程结束时间」勾选）：12 点前结束的行程按 1 小时
逐次叠加校准至 12 点后，轨迹弹窗截图前注入改写结束时间与行驶时间（official_track.
build_popup_rewrite_js），_12点前结束行程.xlsx 附校准后两列；轨迹点 CSV 与查询窗口不改。
"""

import base64
import bisect
import json
import os
import queue
import shutil
import sys
import tempfile
import threading
from datetime import datetime
from pathlib import Path

_APP_DIR = Path(__file__).resolve().parent
if str(_APP_DIR) not in sys.path:
    sys.path.insert(0, str(_APP_DIR))

import config as _config  # noqa: E402
import parallel  # noqa: E402
import platform_utils  # noqa: E402
import state  # noqa: E402
import uvmp  # noqa: E402
import vehicle_export as ve  # noqa: E402
import xlsx_util  # noqa: E402
from jobs import Cancelled  # noqa: E402

APP_ID = "pcd_export"


class _RendererUnavailable(RuntimeError):
    """渲染器启动失败：配置级错误，整批中止并给出排查引导"""


def _write_stub_pdf(path: Path, text: str):
    """mock 联调占位 PDF：手写最小合法单页 PDF（不依赖任何渲染器）。
    只用于 dev.mock 下验证命名/顺序/断点续传管道；生产永远走官方渲染器。"""
    stream = ("BT /F1 14 Tf 60 780 Td (%s) Tj ET" % text).encode("ascii", "replace")
    objs = [
        b"<</Type/Catalog/Pages 2 0 R>>",
        b"<</Type/Pages/Kids[3 0 R]/Count 1>>",
        b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]/Contents 4 0 R"
        b"/Resources<</Font<</F1 5 0 R>>>>>>",
        b"<</Length %d>>stream\n" % len(stream) + stream + b"\nendstream",
        b"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
    ]
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for i, body in enumerate(objs, 1):
        offsets.append(len(out))
        out += ("%d 0 obj\n" % i).encode() + body + b"\nendobj\n"
    xref_pos = len(out)
    out += ("xref\n0 %d\n" % (len(objs) + 1)).encode()
    out += b"0000000000 65535 f \n"
    for off in offsets:
        out += ("%010d 00000 n \n" % off).encode()
    out += (
        "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF"
        % (len(objs) + 1, xref_pos)
    ).encode()
    path.write_bytes(bytes(out))


def _clean_intermediate(pdf_path: Path):
    """ve.html_to_pdf 成功后会在 PDF 旁保留同名 .html 中间件（CLI 版留作排障用）；
    批次交付目录面向最终使用，予以删除保持整洁"""
    try:
        Path(pdf_path).with_suffix(".html").unlink()
    except OSError:
        pass


def _stage_existing_csv(final: Path) -> Path:
    """提交前把已存在的最终 CSV 原子改名为同目录唯一备份（rename 语义，保留
    大文件与元数据，不复制内容）。失败时原文件留在原位，并抛清晰错误。"""
    fd, name = tempfile.mkstemp(
        prefix=final.name + ".", suffix=".bak", dir=str(final.parent)
    )
    try:
        os.close(fd)
    except OSError as e:
        try:
            os.unlink(name)
        except OSError:
            pass
        raise RuntimeError("旧 CSV 备份占位创建失败：%s（%s）" % (name, e)) from e
    backup = Path(name)
    try:
        os.replace(str(final), str(backup))
    except OSError as e:
        if not final.exists() and backup.exists():
            # 极端：rename 已生效但调用报错。绝不删除 backup，保留可恢复现场。
            raise RuntimeError(
                "旧 CSV 暂存结果不确定：旧文件可能在备份 %s，原路径 %s 已不存在；"
                "请人工确认后再继续（%s）" % (backup, final, e)
            ) from e
        cleanup = ""
        try:
            backup.unlink()
        except OSError as ce:
            cleanup = "；备份占位清理失败，请手动删除 %s（%s）" % (backup, ce)
        raise RuntimeError(
            "旧 CSV 暂存失败（原文件保留在 %s）：%s%s" % (final, e, cleanup)
        ) from e
    return backup


def _restore_staged_csv(backup: Path, final: Path):
    """把备份改回最终名。失败时绝不删除备份，保留可恢复现场并给出备份路径。"""
    try:
        os.replace(str(backup), str(final))
    except OSError as e:
        raise RuntimeError(
            "旧 CSV 恢复失败：备份保留在 %s，%s 未恢复；请手动恢复（%s）"
            % (backup, final, e)
        ) from e


def _discard_new_csv(final: Path):
    """原先无旧文件且 manifest 失败：删除刚写入的新 CSV；删除失败给出清晰错误。"""
    try:
        final.unlink()
    except FileNotFoundError:
        return
    except OSError as e:
        raise RuntimeError("清理新 CSV 失败（保留现场）：%s（%s）" % (final, e)) from e


def _discard_backup_csv(backup: Path, log):
    """提交成功后清理旧备份；失败不清空备份、不静默，日志提示手动删除。"""
    try:
        backup.unlink()
    except FileNotFoundError:
        return
    except OSError as e:
        log("  [提示] 旧 CSV 备份清理失败（可手动删除）：%s（%s）" % (backup, e))


def _atomic_write_text(path: Path, text: str, encoding: str):
    """原子写文本（清单/未找到）：同目录唯一临时文件 + os.replace 覆盖。
    失败删除临时文件、保留旧文件原样，绝不留下半截新件。"""
    fd, tmp_name = tempfile.mkstemp(
        prefix=path.name + ".", suffix=".tmp", dir=str(path.parent)
    )
    tmp = Path(tmp_name)
    persisted = False
    try:
        with os.fdopen(fd, "w", encoding=encoding) as fp:
            fp.write(text)
        os.replace(str(tmp), str(path))
        persisted = True
    finally:
        if not persisted:
            try:
                tmp.unlink()
            except OSError:
                pass


def _resume_hit(manifest, kind: str, key: str, files_dir: Path, log) -> str:
    """断点续传命中判定：manifest 有记录且逐单文件确实存在才算已导出。
    记录在而文件缺失（被清理/磁盘丢失/上次半提交）按未导出处理，重新导出后
    由 manifest.put 覆盖旧记录。"""
    fname = manifest.get(kind, key)
    if not fname:
        return ""
    if (files_dir / fname).is_file():
        return fname
    log("  [续传] 清单记录的文件缺失，重新导出：%s" % fname)
    return ""


def _acquire_renderer(worker, job, unavailable: threading.Event):
    """取本 worker 的渲染器。启动失败属配置级错误：置位整批中止标记后抛
    _RendererUnavailable，其余在途/排队工作项立即放弃，不再逐个重试启动浏览器。"""
    if unavailable.is_set():
        raise _RendererUnavailable()
    try:
        renderer = worker.renderer
    except parallel.RendererPoolClosed:
        if job.cancelled:
            raise Cancelled()
        unavailable.set()
        raise _RendererUnavailable()
    except Exception as e:  # noqa: BLE001
        if job.cancelled:
            raise Cancelled() from e
        unavailable.set()
        raise _RendererUnavailable() from e
    if renderer is None:
        unavailable.set()
        raise _RendererUnavailable()
    return renderer


def _write_track_points_csv_temp(points, files_dir: Path, csv_name: str) -> Path:
    """轨迹点 CSV 先写同目录唯一临时文件，不直接落最终名。

    内容生成复用 vehicle_export.write_track_points_csv（csv.writer 转义逗号/引号/换行，
    公式前导字符转文本），与 CLI 导出口径一致；主线程 on_result 在 job.commit() 内原子
    替换为最终 CSV 并写 manifest；写入异常自行清理临时文件，取消时由调用方/批次收尾
    统一删除。"""
    fd, tmp_name = tempfile.mkstemp(
        prefix=csv_name + ".", suffix=".tmp", dir=str(files_dir)
    )
    tmp = Path(tmp_name)
    try:
        os.close(fd)  # 先释放句柄：Windows 下不能对已打开的路径重新 open
    except OSError:
        pass
    try:
        ve.write_track_points_csv(tmp, points)
    except BaseException:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise
    return tmp


# ---------------------------------------------------------------- 输入解析


def save_upload(xlsx_b64: str, filename: str) -> Path:
    """前端 base64 上传的 xlsx 落盘到状态目录，返回路径"""
    stem = ve.safe_name(Path(filename or "清单.xlsx").stem) or "清单"
    d = state.state_dir() / "uploads"
    d.mkdir(parents=True, exist_ok=True)
    path = d / (stem + ".xlsx")
    path.write_bytes(base64.b64decode(xlsx_b64))
    return path


def preview(params: dict) -> dict:
    """解析单号来源（xlsx_b64 / codes_text / xlsx_path），返回清单预览"""
    codes = _resolve_codes(params)
    return {"total": len(codes), "codes": codes[:100], "truncated": len(codes) > 100}


def _resolve_codes(params: dict) -> list:
    if params.get("codes"):
        return [dict(c, seq=i + 1) for i, c in enumerate(params["codes"])]
    if params.get("xlsx_b64"):
        path = save_upload(params["xlsx_b64"], params.get("filename", ""))
        return xlsx_util.extract_run_codes(str(path))
    if params.get("xlsx_path"):
        return xlsx_util.extract_run_codes(params["xlsx_path"])
    if params.get("codes_text"):
        return xlsx_util.parse_codes_text(params["codes_text"])
    raise RuntimeError("未提供派车单号来源（xlsx 文件或粘贴文本）")


# ---------------------------------------------------------------- 导出主流程


def _worker_counts(cfg: dict, params: dict = None):
    """查询/渲染并发数：默认 8/4，查询/渲染均钳 1..16（与设置页一致）。
    集群分片可用 params.query_workers/render_workers 覆盖本机配置。"""
    oe = cfg.get("order_export") or {}
    source = params or {}
    return (
        parallel.clamp_workers(
            source.get("query_workers", oe.get("query_workers")), 8, 1, 16
        ),
        parallel.clamp_workers(
            source.get("render_workers", oe.get("render_workers")), 4, 1, 16
        ),
    )


def _renderer_hint(cfg: dict) -> str:
    chosen = (_config.render_cfg(cfg).get("chrome") or "").strip()
    if chosen:
        return (
            "所选浏览器（%s）可能不支持无头/CDP 调试模式，请到「设置」页换公司指定的专用浏览器；"
            "具体原因见上方日志" % chosen
        )
    return "请到「设置」页选择公司指定浏览器后重试；具体原因见上方日志"


def _has_render_failures(rows: dict) -> bool:
    return any(
        r["pcd_status"] == "失败" or r["track_status"] == "失败" for r in rows.values()
    )


def _run_stage(items, handler, job, pool, workers, on_result):
    """渲染阶段调度：有渲染池时按 worker 槽位并行，mock 下无浏览器也走有界线程"""
    if pool is not None:
        return pool.stage(items, handler, job, on_result=on_result)
    return parallel.bounded_map(
        items, lambda item: handler(item, None), workers, job, on_result=on_result
    )


def _task_failure(f, exc, **extra):
    """单条失败结果（主线程据此标记行状态，不重建渲染器）"""
    result = {
        "ok": False,
        "seq": f["seq"],
        "code": f["code"],
        "error": str(exc),
    }
    result.update(extra)
    return result


def run(job, params: dict) -> dict:
    """导出主入口：解析单号后直接执行旧 Python 编排（查询与渲染两级本地线程并发）"""
    cfg = _config.load()
    codes = _resolve_codes(params)
    if not codes:
        raise RuntimeError("未解析到任何派车单号")
    if not (
        bool(params.get("export_pcd", True)) or bool(params.get("export_track", True))
    ):
        raise RuntimeError("派车单 / 轨迹至少勾选一项")
    return _run_python(job, cfg, params, codes)


def _run_python(job, cfg, params, codes) -> dict:
    """旧 Python 编排：并发查询 → 派车单渲染 → 轨迹渲染 → 清单/未找到报告"""
    oe = cfg.get("order_export") or {}
    log = job.log

    export_pcd = bool(params.get("export_pcd", True))
    export_track = bool(params.get("export_track", True))
    # 自动校准行程结束时间：车载定位系统异常（结束时间早于12点）的行程按1小时逐次叠加至12点后
    calibrate_noon = bool(params.get("calibrate_noon", False))
    mock = bool((cfg.get("dev") or {}).get("mock"))
    query_workers, render_workers = _worker_counts(cfg, params)

    cluster = bool(params.get("cluster"))
    outdir = Path(
        params.get("outdir") or oe.get("default_outdir") or "./output"
    ).expanduser()
    if not cluster and params.get("batch_subdir", oe.get("batch_subdir", True)):
        base = (params.get("batch_name") or "派车单").strip() or "派车单"
        outdir = outdir / (
            "%s_导出_%s"
            % (ve.safe_name(base), datetime.now().strftime("%Y%m%d_%H%M%S"))
        )
    outdir.mkdir(parents=True, exist_ok=True)
    files_dir = outdir / "逐单"  # 逐单文件归子目录；批次根目录留给合并总 PDF/清单/诊断
    files_dir.mkdir(parents=True, exist_ok=True)
    log("共 %d 个派车单号；输出目录：%s" % (len(codes), outdir))
    log(
        "并发：查询 %d 路，渲染 %d 路（Python 导出引擎）"
        % (query_workers, render_workers)
    )
    manifest = ve.Manifest(outdir / "manifest.json")
    try:
        cluster_width = int(params.get("cluster_width") or 0)
    except (TypeError, ValueError):
        cluster_width = 0
    width = cluster_width if cluster and cluster_width > 0 else len(str(len(codes)))
    lookback = int(oe.get("lookback_days", 62))

    # ---- 登录与渲染配置 ----
    clients, auth = uvmp.make_clients(cfg, log, max(query_workers, render_workers))
    client = clients[0]
    chrome = electron_path = ""
    if not mock:
        # 两条渲染通道分开（已踩坑：用户选了浏览器后连包版打印也走它，360 渲出空白 PDF）：
        #   PNG 包版打印（html_to_pdf）→ 自带 Electron 优先（不挑机器），无则用户所选/自动探测
        #   官方弹窗截图（CDP 驱动真实系统页面）→ 必须真实浏览器（用户所选优先）
        electron_path = platform_utils.electron_binary()
        chosen = (_config.render_cfg(cfg).get("chrome") or "").strip()
        if chosen and not Path(chosen).exists():
            raise RuntimeError("指定的浏览器不存在: %s（请到设置页重新选择）" % chosen)
        if chosen:
            chrome = chosen
            log("浏览器（官方直出/CDP）：用户指定 %s" % chrome)
        else:
            try:
                chrome = platform_utils.detect_chrome("")
                log("浏览器（官方直出/CDP）：自动探测 %s" % chrome)
            except Exception as e:  # noqa: BLE001
                chrome = ""
                log("浏览器（官方直出/CDP）：未找到（%s）" % e)
        if electron_path:
            log("PDF 包版打印：自带 Electron Chromium（%s）" % electron_path)
        elif chrome:
            log("PDF 包版打印：%s" % chrome)
        else:
            raise RuntimeError("无可用 PDF 渲染器（既无自带 Electron 也未找到浏览器）")

    # 渲染 worker 池：每槽独占浏览器/目录，按需懒启动；只注册一个取消钩子，
    # 取消即关闭全部浏览器（CDP 在途等待立即报错，不必等当前单据渲完）
    pool = None
    if not mock:
        work_root = (
            state.state_dir() / "browser-workers" / (ve.safe_name(job.id) or "job")
        )
        pool = parallel.RendererPool(
            render_workers,
            clients,
            lambda worker_client, work_dir: uvmp.start_renderer(
                worker_client, auth, cfg, work_dir, log
            ),
            work_root,
            log,
        )
        job.add_cancel_hook(pool.stop)

    # ---- 阶段1：按号查找（每线程独立客户端，主线程按输入顺序收结果）----
    found, not_found = [], []
    job.set_progress(0, len(codes), "按单号查询")
    lease = queue.Queue()
    for c in clients:
        lease.put(c)

    def query_one(c):
        query_client = lease.get()
        try:
            job.check_cancel()
            return ve.query_pcd_by_run_code(
                query_client, c["code"], c.get("date_hint") or "", lookback, log
            )
        finally:
            lease.put(query_client)

    def on_query(i, order):
        c = codes[i]
        if order:
            found.append({"seq": c["seq"], "code": c["code"], "order": order})
        else:
            not_found.append(c)
            log("  未找到：%s" % c["code"])
        job.set_progress(i + 1, len(codes), "查询 " + c["code"])

    parallel.bounded_map(codes, query_one, query_workers, job, on_result=on_query)
    job.set_progress(len(codes), len(codes), "查询完成")
    log("查询完成：找到 %d 单，未找到 %d 单" % (len(found), len(not_found)))

    rows = {
        f["seq"]: {
            "seq": f["seq"],
            "code": f["code"],
            "oid": f["order"].get("id") or "",
            "vehicle": f["order"].get("vehicleNumber") or "",
            "date": str(
                f["order"].get("planSendTimeStr")
                or f["order"].get("planSendTime")
                or ""
            )[:10],
            "pcd_file": "",
            "pcd_status": "未导出",
            "track_file": "",
            "track_status": "未导出",
            "note": "",
        }
        for f in found
    }

    keep_diag = False
    noon_rows = []
    try:
        if export_pcd and found:
            _export_pcd(
                job,
                found,
                files_dir,
                outdir,
                manifest,
                width,
                rows,
                pool,
                render_workers,
                mock,
            )
        if export_track and found:
            noon_rows = _export_track(
                job,
                found,
                files_dir,
                outdir,
                chrome,
                electron_path,
                manifest,
                width,
                rows,
                pool,
                render_workers,
                calibrate_noon,
                mock,
                client,
                cluster,
            )
    except _RendererUnavailable:
        keep_diag = True
        raise RuntimeError(
            "官方页面渲染器未启动——派车单/轨迹导出需要官方页面直出。"
            + _renderer_hint(cfg)
        )
    except BaseException:
        keep_diag = True
        raise
    finally:
        if pool is not None:
            pool.stop()
            # 成功清理临时 worker 目录；有失败/异常则保留现场供排障
            if not keep_diag and not _has_render_failures(rows):
                pool.cleanup()

    ordered = sorted(rows.values(), key=lambda x: x["seq"])
    pcd_ok = sum(1 for r in rows.values() if r["pcd_status"] == "成功")
    pcd_fail = sum(1 for r in rows.values() if r["pcd_status"] == "失败")
    track_ok = sum(1 for r in rows.values() if r["track_status"] == "成功")
    track_fail = sum(1 for r in rows.values() if r["track_status"] == "失败")
    if cluster:
        # 分片只产出逐单文件与明细；合并 PDF/清单/12点前表由发起节点统一生成。
        log(
            "分片完成：派车单 成功 %d/失败 %d；轨迹 成功 %d/失败 %d；未找到 %d"
            % (pcd_ok, pcd_fail, track_ok, track_fail, len(not_found))
        )
        return {
            "dir": str(outdir),
            "total": len(codes),
            "found": len(found),
            "not_found": [c["code"] for c in not_found],
            "pcd_ok": pcd_ok,
            "pcd_fail": pcd_fail,
            "track_ok": track_ok,
            "track_fail": track_fail,
            "rows": ordered,
            "noon_rows": noon_rows,
        }

    # ---- 合并：全部导完后按序号顺序合成总 PDF（逐单文件保留在 逐单/；失败不毁批次）----
    # 只收真实存在的文件：状态是成功但文件已丢失的，不进合并 PDF 也不进清单
    def _merge(key: str, caption: str, enabled: bool):
        if not enabled or not found:
            return
        inputs = []
        for r in ordered:
            name = r.get(key) or ""
            path = _existing(files_dir, name)
            if path is not None:
                inputs.append(path)
            elif name:
                _degrade_missing(r, key, name, log)
        ve.merge_pdfs(inputs, outdir / ("_合并_%s.pdf" % caption), log=log)

    _merge("pcd_file", "派车单", export_pcd)
    _merge("track_file", "轨迹", export_track)

    # ---- 报告 ----
    pcd_ok = sum(1 for r in rows.values() if r["pcd_status"] == "成功")
    pcd_fail = sum(1 for r in rows.values() if r["pcd_status"] == "失败")
    track_ok = sum(1 for r in rows.values() if r["track_status"] == "成功")
    track_fail = sum(1 for r in rows.values() if r["track_status"] == "失败")
    _write_reports(outdir, rows, not_found, log)
    log(
        "全部完成：派车单 成功 %d/失败 %d；轨迹 成功 %d/失败 %d；未找到 %d"
        % (pcd_ok, pcd_fail, track_ok, track_fail, len(not_found))
    )
    return {
        "dir": str(outdir),
        "total": len(codes),
        "found": len(found),
        "not_found": [c["code"] for c in not_found],
        "pcd_ok": pcd_ok,
        "pcd_fail": pcd_fail,
        "track_ok": track_ok,
        "track_fail": track_fail,
        "rows": sorted(rows.values(), key=lambda r: r["seq"]),
    }


# ---------------------------------------------------------------- 派车单


def _pcd_fname(seq: int, width: int, d: dict) -> str:
    return "%s_派车单_%s_%s_%s.pdf" % (
        str(seq).zfill(width),
        ve.safe_name(d.get("runCode")),
        ve.safe_name(d.get("vehicleNumber")),
        ve.safe_name(str(d.get("planSendTimeStr") or d.get("planSendTime") or "")[:10]),
    )


def _export_pcd(
    job,
    found,
    files_dir,
    outdir,
    manifest,
    width,
    rows,
    pool,
    render_workers,
    mock,
):
    log = job.log
    todo, skipped = [], 0
    for f in found:
        hit = _resume_hit(manifest, "pcd", f["order"]["id"], files_dir, log)
        if hit:
            r = rows[f["seq"]]
            r["pcd_file"] = hit
            r["pcd_status"] = "成功"
            r["note"] = (r["note"] + "；" if r["note"] else "") + "派车单此前已导出"
            skipped += 1
        else:
            todo.append(f)
    log(
        "派车单：共 %d 单，已导出 %d 单，本次待导出 %d 单"
        % (len(found), skipped, len(todo))
    )
    if not todo:
        return

    unavailable = threading.Event()  # 渲染器启动失败：整批快速中止

    def task(f, worker):
        # 每个工作项在自己的 worker 内完成“详情 → 官方渲染 → PDF”，不共享渲染器。
        # 异常按阶段隔离：只有官方渲染/打印失败才重建 worker；渲染器启动失败（配置级）
        # 直接上抛 _RendererUnavailable 整批中止；数据准备/单条渲染失败只算本条失败。
        o = f["order"]
        if mock:
            # mock 联调：写占位 PDF（极简合法单页），只验证命名/顺序/续传管道
            try:
                fname = _pcd_fname(f["seq"], width, o)
                _write_stub_pdf(files_dir / fname, "[mock] pcd %s" % f["code"])
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e
                return _task_failure(f, e)
        else:
            # 1) 先取渲染器：启动失败立即整批中止，不浪费逐单查询
            renderer = _acquire_renderer(worker, job, unavailable)
            # 2) 数据准备（查打印详情）：失败保留渲染器
            try:
                details = ve.get_print_details(worker.client, [o["id"]], 1)
                d = details[0] if details else o
                fname = _pcd_fname(f["seq"], width, d)
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e
                return _task_failure(f, e)
            # 3) 官方渲染/打印：只有这一步失败才重建 worker
            try:
                # 逐单渲染（不批量：企业浏览器渲染慢/个别单数据异常时不拖垮整批）
                renderer.render_pcd_pdf_one(
                    d, files_dir / fname, tag="p%d" % f["seq"], diag_dir=outdir
                )
            except parallel.RendererPoolClosed:
                if job.cancelled:
                    raise Cancelled()
                unavailable.set()
                raise _RendererUnavailable()
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e  # 取消引发的浏览器销毁不算单据失败
                worker.invalidate()  # 渲染器状态未知：下一项重建 worker
                return _task_failure(f, e)
        # 3) 只查取消并把结果交给主线程提交：manifest 与成功回调统一进 job.commit，
        #    避免 worker 先落盘、主线程随后才发现已取消的“半成功”
        job.check_cancel()
        return {
            "ok": True,
            "seq": f["seq"],
            "code": f["code"],
            "oid": o["id"],
            "fname": fname,
        }

    done = 0

    def on_result(_i, res):
        nonlocal done
        # 主线程提交：check_cancel → manifest.put → rows/进度/日志成功回调，
        # 与 cancel 共用提交锁；取消只能发生在整次提交之前或之后
        with job.commit():
            r = rows[res["seq"]]
            if not res["ok"]:
                r["pcd_status"] = "失败"
                log("  失败 %s: %s" % (res["code"], res["error"]))
                return
            if not (files_dir / res["fname"]).is_file():
                # 渲染返回了但产出不在：绝不写 manifest 当成功，下次重跑会重做
                r["pcd_status"] = "失败"
                log("  失败 %s: 渲染产出文件缺失（%s）" % (res["code"], res["fname"]))
                return
            try:
                manifest.put("pcd", res["oid"], res["fname"])
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e
                r["pcd_status"] = "失败"
                log("  失败 %s: %s" % (res["code"], e))
                return
            r["pcd_file"], r["pcd_status"] = res["fname"], "成功"
            if mock:
                r["note"] = (r["note"] + "；" if r["note"] else "") + "mock 占位"
            done += 1
            job.set_progress(done, len(todo), "派车单 " + res["fname"])
            log(
                "[%d/%d] %s%s"
                % (done, len(todo), res["fname"], "（mock 占位）" if mock else "")
            )

    _run_stage(todo, task, job, pool, render_workers, on_result)


# ---------------------------------------------------------------- 轨迹


def _export_track(
    job,
    found,
    files_dir,
    outdir,
    chrome,
    electron_path,
    manifest,
    width,
    rows,
    pool,
    render_workers,
    calibrate_noon,
    mock,
    base_client,
    cluster: bool = False,
):
    log = job.log
    todo = []
    for f in found:
        o = f["order"]
        begin = ve.clean_time(o.get("realSendTime"))
        end = ve.clean_time(o.get("realBackTime"))
        hit = _resume_hit(manifest, "track", o["id"], files_dir, log)
        if hit:
            r = rows[f["seq"]]
            r["track_file"] = hit
            r["track_status"] = "成功"
            r["note"] = (r["note"] + "；" if r["note"] else "") + "轨迹此前已导出"
        elif o.get("vehicleId") and begin and end:
            todo.append((f, begin, end))
        else:
            rows[f["seq"]]["track_status"] = "跳过（未完结或缺实际时间）"
    log("轨迹：待导出 %d 条（按派车单的实际出车/归队时间取轨迹）" % len(todo))
    done = 0
    # 行程结束时间在当日 12 点前的轨迹（出 _12点前结束行程.xlsx）；历史记录从断点索引
    # 读回，重跑时「已导出」的单也保留在清单里
    # 只按序号认领会把上一批的记录写进本批（固定目录/关掉批次子目录时序号必然重合），
    # 因此必须「序号 + 派车单号」同时匹配才算本批历史
    found_codes = {f["seq"]: f["code"] for f in found}
    noon_by_seq = (
        {}
        if cluster
        else {
            seq: nr
            for seq, nr in _load_noon_index(outdir, log).items()
            if found_codes.get(seq) == nr.get("code")
        }
    )
    unavailable = threading.Event()  # 渲染器启动失败：整批快速中止
    temp_files = set()  # 未提交的轨迹点 CSV 临时文件：取消/异常时统一清理
    temp_lock = threading.Lock()

    def _register_temp(path):
        with temp_lock:
            temp_files.add(str(path))

    def _discard_temp(path):
        with temp_lock:
            temp_files.discard(str(path))
        try:
            os.unlink(path)
        except OSError:
            pass

    def task(item, worker):
        f, begin, end = item
        o = f["order"]
        label = "%s %s" % (o.get("vehicleNumber"), f["code"])
        client = worker.client if worker is not None else base_client
        noon = None
        renderer = None
        if not mock:
            # 渲染器启动失败属于配置级：置位中止标记后整批快速中止
            renderer = _acquire_renderer(worker, job, unavailable)

        # 1) 数据准备（轨迹点/行程段/校准）：失败保留渲染器
        seg = None
        pre_js = ""
        png = None
        try:
            # strict=True：响应形状不认识直接报错，不能当成"没有轨迹点"继续出图并记成功
            points = ve.query_track_points(
                client, o["vehicleId"], begin, end, strict=True
            )
            if not points:
                raise RuntimeError(
                    "轨迹点为空（该时段无定位数据），不生成轨迹 PDF/CSV 也不记入清单"
                )
            fname = "%s_轨迹_%s_%s_%s.pdf" % (
                str(f["seq"]).zfill(width),
                ve.safe_name(f["code"]),
                ve.safe_name(o.get("vehicleNumber")),
                ve.safe_name(begin[:10]),
            )
            if renderer is not None:
                import official_track as ot

                # 临时 PNG 落在本 worker 目录，避免多渲染线程共享中间文件
                png = worker.work_dir / (Path(fname).stem + ".png")
                seg = ve.query_track_segment(client, o["vehicleId"], begin, end)
                end_noon = ve.seg_end_before_noon(seg)
                if end_noon:
                    noon = {
                        "seq": f["seq"],
                        "code": f["code"],
                        "vehicle": o.get("vehicleNumber") or "",
                        "end": end_noon,
                        "label": label,
                    }
                    if calibrate_noon:
                        cal = ve.calibrate_noon_end(seg)
                        if cal:
                            new_dt, old_dur, new_dur = cal
                            pre_js = ot.build_popup_rewrite_js(
                                datetime.strptime(end_noon[:19], "%Y-%m-%d %H:%M:%S"),
                                new_dt,
                                old_dur,
                                new_dur,
                            )
                            noon["cal_end"] = new_dt.strftime("%Y-%m-%d %H:%M:%S")
                            noon["cal_dur"] = ve.fmt_dur_cn(new_dur)
        except Exception as e:  # noqa: BLE001
            if job.cancelled:
                raise Cancelled() from e
            return _task_failure(f, e, label=label, noon=noon)

        # 2) 官方渲染/打印：只有这一步失败才重建 worker
        if renderer is not None:
            import official_track as ot

            try:
                if seg and seg.get("id"):
                    renderer.render_route_png(
                        seg["id"], png, pre_shot_js=pre_js
                    )  # 官方行程详情弹窗
                else:
                    renderer.render_pcd_png(
                        o, png, pre_shot_js=pre_js
                    )  # 回退按派车单弹窗
                ve.html_to_pdf(
                    ot.PNG_WRAP_HTML.replace("$img_uri", png.as_uri()),
                    files_dir / fname,
                    chrome,
                    electron=electron_path,
                    job=job,
                )
                _clean_intermediate(files_dir / fname)
                try:
                    if png.exists():
                        png.unlink()
                except OSError:
                    pass
            except parallel.RendererPoolClosed:
                if job.cancelled:
                    raise Cancelled()
                raise _RendererUnavailable()
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e  # 取消引发的浏览器销毁不算单据失败
                worker.invalidate()  # 渲染器状态未知：下一项重建 worker
                return _task_failure(f, e, label=label, noon=noon)
        else:
            # mock 联调：占位 PDF + 真实轨迹点 CSV（管道验证用；生产走不到这里）
            try:
                _write_stub_pdf(files_dir / fname, "[mock] track %s" % f["code"])
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled() from e
                return _task_failure(f, e, label=label, noon=noon)

        # 3) 轨迹点 CSV 先写同目录唯一临时文件；最终替换、manifest 与成功回调
        #    统一由主线程 on_result 在 job.commit 内完成，避免“写盘后才发现取消”
        job.check_cancel()
        tmp_path = None
        try:
            csv_name = "%s_轨迹点_%s.csv" % (
                str(f["seq"]).zfill(width),
                ve.safe_name(f["code"]),
            )
            tmp_path = _write_track_points_csv_temp(points, files_dir, csv_name)
            _register_temp(tmp_path)
            job.check_cancel()  # 写入期间取消：立即放弃，临时文件交收尾清理
        except Exception as e:  # noqa: BLE001
            if tmp_path is not None:
                _discard_temp(tmp_path)
            if job.cancelled:
                raise Cancelled() from e
            return _task_failure(f, e, label=label, noon=noon)

        return {
            "ok": True,
            "seq": f["seq"],
            "code": f["code"],
            "id": o["id"],
            "label": label,
            "fname": fname,
            "points": len(points),
            "noon": noon,
            "csv_name": csv_name,
            "csv_tmp": str(tmp_path),
        }

    def on_result(_i, res):
        nonlocal done
        tmp_path = Path(res["csv_tmp"]) if res.get("csv_tmp") else None
        r = rows[res["seq"]]
        try:
            # 主线程提交：再次 check_cancel → 原子替换最终 CSV → manifest →
            # rows/进度/日志成功回调，全程持提交锁；取消只能线性化在整次提交之前
            with job.commit():
                job.check_cancel()
                nr = res.get("noon")
                if nr:
                    # 页码在阶段结束后统一按 seq 计算；失败单的页码留空但记录保留（与原行为一致）
                    noon_by_seq[nr["seq"]] = nr
                    if nr.get("cal_end"):
                        log(
                            "  [校准] %s 行程结束 %s → %s，行驶时间 → %s"
                            % (nr["label"], nr["end"], nr["cal_end"], nr["cal_dur"])
                        )
                if not res["ok"]:
                    r["track_status"] = "失败"
                    log("  失败 %s: %s" % (res["label"], res["error"]))
                    return
                final = files_dir / res["csv_name"]
                backup = None
                if final.exists():  # 重跑/上次失败遗留：先改名暂存旧文件
                    backup = _stage_existing_csv(final)
                try:
                    os.replace(str(tmp_path), str(final))
                except Exception:
                    # 替换失败：旧文件已在 backup（若有），恢复后原样抛出
                    if backup is not None:
                        _restore_staged_csv(backup, final)
                        backup = None
                    raise
                if not (files_dir / res["fname"]).is_file():
                    # 轨迹 PDF 没落盘：不记成功（清掉刚换上的 CSV 的新内容）
                    if backup is not None:
                        _restore_staged_csv(backup, final)
                        backup = None
                    else:
                        _discard_new_csv(final)
                    r["track_status"] = "失败"
                    log(
                        "  失败 %s: 渲染产出文件缺失（%s）"
                        % (res["label"], res["fname"])
                    )
                    return
                try:
                    manifest.put("track", res["id"], res["fname"])
                except Exception:
                    # 清单失败：原先有旧文件必须恢复；原先没有才删除刚写入的新文件
                    if backup is not None:
                        _restore_staged_csv(backup, final)
                        backup = None
                    else:
                        _discard_new_csv(final)
                    raise
                if backup is not None:  # 提交成功：清理旧备份
                    _discard_backup_csv(backup, log)
                r["track_file"], r["track_status"] = res["fname"], "成功"
                if mock:
                    r["note"] = (r["note"] + "；" if r["note"] else "") + "mock 占位"
                done += 1
                job.set_progress(done, len(todo), "轨迹 " + res["fname"])
                log(
                    "[%d/%d] %s（%d 个轨迹点）"
                    % (done, len(todo), res["fname"], res["points"])
                )
        except Cancelled:
            raise  # 取消不降级为单条失败；commit 语义与临时文件清理保持不变
        except Exception as e:  # noqa: BLE001
            r["track_status"] = "失败"
            log("  失败 %s: %s" % (res["label"], e))
        finally:
            if tmp_path is not None:
                _discard_temp(tmp_path)

    try:
        _run_stage(todo, task, job, pool, render_workers, on_result)
    finally:
        # 取消/异常时可能有已写出但未提交的临时 CSV（结果未送达 on_result）
        with temp_lock:
            leftovers = list(temp_files)
            temp_files.clear()
        for name in leftovers:
            try:
                os.unlink(name)
            except OSError:
                pass
    noon_rows = sorted(noon_by_seq.values(), key=lambda nr: nr["seq"])
    if not cluster:
        # 页码 = 该单在「轨迹成功序列」中的位次（_合并_轨迹.pdf 按 seq 升序、每单恰 1 页合并）；
        # 渲染失败的单没进合并 PDF，页码留空
        ok_seqs = sorted(r["seq"] for r in rows.values() if r["track_file"])
        ok_seq_set = set(ok_seqs)
        for nr in noon_rows:
            nr["page"] = (
                bisect.bisect_right(ok_seqs, nr["seq"])
                if nr["seq"] in ok_seq_set
                else None
            )
        # 先落断点索引再出报表：报表写入失败时重跑仍能凭索引补出完整清单
        _save_noon_index(outdir, noon_rows, log)
        ve.write_noon_end_report(noon_rows, outdir / "_12点前结束行程.xlsx", log=log)
    return noon_rows


# ---------------------------------------------------------------- 报告


def _noon_index_path(outdir: Path) -> Path:
    return Path(outdir) / "_12点前结束行程索引.json"


def _load_noon_index(outdir: Path, log) -> dict:
    """读断点索引：{seq: noon 记录}。索引缺失/损坏时返回空表（不阻断导出，
    只是重跑不再能补出历史 12 点前记录）。

    调用方必须再用记录自带的 code 与本批同序号的派车单号比对后才能认领：
    索引只按序号落盘，跨批次（固定目录）时序号会重合。"""
    path = _noon_index_path(outdir)
    if not path.is_file():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        return {int(k): v for k, v in (data or {}).items() if isinstance(v, dict)}
    except (OSError, ValueError, TypeError) as e:
        log("  [续传] 12 点前结束行程索引不可用，本次重建：%s（%s）" % (path, e))
        return {}


def _save_noon_index(outdir: Path, noon_rows: list, log):
    """原子写断点索引（只收带派车单号的记录，无号记录下次无法被认领）。
    失败只提示不中止——索引是续跑增强，报表才是交付物。"""
    path = _noon_index_path(outdir)
    data = {str(nr["seq"]): nr for nr in noon_rows if nr.get("code")}
    text = json.dumps(data, ensure_ascii=False, indent=1)
    try:
        _atomic_write_text(path, text, "utf-8")
    except OSError as e:
        log(
            "  [提示] 12 点前结束行程索引写入失败（重跑将无法补出历史记录）：%s（%s）"
            % (path, e)
        )


def _existing(files_dir: Path, name: str):
    """合并/汇总输入：只认真实存在的文件，返回路径或 None（不修改调用方的行字典）"""
    if not name:
        return None
    path = Path(files_dir) / name
    return path if path.is_file() else None


def _degrade_missing(row: dict, key: str, name: str, log):
    """把「状态成功但文件缺失」的行降级为失败并提示重跑，避免清单里出现无文件的成功项。"""
    row[key] = ""
    row[key.replace("_file", "_status")] = "失败"
    note = "文件缺失，请重跑补出"
    row["note"] = (row["note"] + "；" if row["note"] else "") + note
    log("  [缺件] %s 缺失，已标记失败（重跑会重新导出）" % name)


def _write_reports(outdir: Path, rows: dict, not_found: list, log):
    """_清单.csv（utf-8-sig，Excel 可直接打开）与 _未找到.txt（两者均原子替换）"""
    lines = ["序号,派车单号,车牌,用车日期,派车单PDF,派车单状态,轨迹PDF,轨迹状态,备注"]
    for r in sorted(rows.values(), key=lambda x: x["seq"]):
        cells = [
            r["seq"],
            r["code"],
            r["vehicle"],
            r["date"],
            r["pcd_file"],
            r["pcd_status"],
            r["track_file"],
            r["track_status"],
            r["note"],
        ]
        lines.append(
            ",".join(str(c).replace(",", "，").replace("\n", " ") for c in cells)
        )
    _atomic_write_text(outdir / "_清单.csv", "\n".join(lines) + "\n", "utf-8-sig")
    if not_found:
        _atomic_write_text(
            outdir / "_未找到.txt",
            "以下 %d 个派车单号未查询到：\n%s\n"
            % (len(not_found), "\n".join(c["code"] for c in not_found)),
            "utf-8",
        )
        log("未找到清单已写入 _未找到.txt（%d 个）" % len(not_found))


# ---------------------------------------------------------------- 集群导出


def run_cluster(job, params: dict) -> dict:
    """集群分片执行：codes 由发起节点切好（全局 seq），本函数只导出分片、不合并/不出报告。"""
    cfg = _config.load()
    codes = params.get("codes") or []
    if not codes:
        raise RuntimeError("集群分片没有派车单号")
    if not (
        bool(params.get("export_pcd", True)) or bool(params.get("export_track", True))
    ):
        raise RuntimeError("派车单 / 轨迹至少勾选一项")
    merged = dict(params)
    merged["cluster"] = True
    return _run_python(job, cfg, merged, codes)


def finalize_cluster(
    outdir,
    batch_name: str,
    batch_subdir: bool,
    started_at: str,
    rows: list,
    not_found: list,
    noon_rows: list,
    files,
    export_track: bool = True,
    log=print,
) -> dict:
    """发起节点汇总：复制逐单文件、合并 PDF、按全局 seq 重算 12 点前页码、写清单/manifest。"""
    base = Path(outdir).expanduser()
    if batch_subdir:
        stamp = started_at or datetime.now().strftime("%Y%m%d_%H%M%S")
        final = base / (
            "%s_导出_%s" % (ve.safe_name(batch_name or "派车单"), stamp)
        )
    else:
        final = base
    final.mkdir(parents=True, exist_ok=True)
    files_dir = final / "逐单"
    files_dir.mkdir(parents=True, exist_ok=True)
    for item in files or []:
        source = Path(item)
        try:
            if source.is_file():
                shutil.copy2(str(source), str(files_dir / source.name))
        except OSError as e:
            log("复制分片文件失败 %s: %s" % (source, e))

    rows_by_seq = {int(r["seq"]): r for r in rows or []}
    ordered = sorted(rows_by_seq.values(), key=lambda r: r["seq"])
    # 复制/合并/manifest 前核实文件确实落到批次目录：分片上传遗漏、复制失败或磁盘
    # 丢失的条目一律标记失败（不写 manifest），发起节点重跑该批次即可补出缺件
    for row in ordered:
        for key in ("pcd_file", "track_file"):
            name = row.get(key) or ""
            if name and _existing(files_dir, name) is None:
                _degrade_missing(row, key, name, log)
    missing = [
        r for r in ordered if r["pcd_status"] == "失败" or r["track_status"] == "失败"
    ]
    if missing:
        log(
            "  分片缺件 %d 条：%s（已标记失败，补齐后重跑可补出）"
            % (
                len(missing),
                "、".join(str(r.get("code") or r["seq"]) for r in missing[:5])
                + ("…" if len(missing) > 5 else ""),
            )
        )
    if ordered:
        ve.merge_pdfs(
            [files_dir / r["pcd_file"] for r in ordered if r.get("pcd_file")],
            final / "_合并_派车单.pdf",
            log=log,
        )
        ve.merge_pdfs(
            [files_dir / r["track_file"] for r in ordered if r.get("track_file")],
            final / "_合并_轨迹.pdf",
            log=log,
        )

    ok_seqs = sorted(r["seq"] for r in ordered if r.get("track_file"))
    ok_seq_set = set(ok_seqs)
    for nr in noon_rows or []:
        nr["page"] = (
            bisect.bisect_right(ok_seqs, nr["seq"])
            if nr.get("seq") in ok_seq_set
            else None
        )
    if export_track:
        ve.write_noon_end_report(
            noon_rows or [], final / "_12点前结束行程.xlsx", log=log
        )

    _write_reports(final, rows_by_seq, not_found or [], log)
    manifest = ve.Manifest(final / "manifest.json")
    for row in ordered:
        oid = str(row.get("oid") or row.get("code") or "")
        if not oid:
            continue
        if row.get("pcd_file"):
            manifest.put("pcd", oid, row["pcd_file"])
        if row.get("track_file"):
            manifest.put("track", oid, row["track_file"])

    pcd_ok = sum(1 for r in ordered if r.get("pcd_status") == "成功")
    pcd_fail = sum(1 for r in ordered if r.get("pcd_status") == "失败")
    track_ok = sum(1 for r in ordered if r.get("track_status") == "成功")
    track_fail = sum(1 for r in ordered if r.get("track_status") == "失败")
    log(
        "集群汇总完成：派车单 成功 %d/失败 %d；轨迹 成功 %d/失败 %d；未找到 %d"
        % (pcd_ok, pcd_fail, track_ok, track_fail, len(not_found or []))
    )
    return {
        "dir": str(final),
        "total": len(ordered) + len(not_found or []),
        "found": len(ordered),
        "not_found": [c.get("code", "") for c in (not_found or [])],
        "pcd_ok": pcd_ok,
        "pcd_fail": pcd_fail,
        "track_ok": track_ok,
        "track_fail": track_fail,
        "rows": ordered,
    }
