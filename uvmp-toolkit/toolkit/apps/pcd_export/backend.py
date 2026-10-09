# -*- coding: utf-8 -*-
"""按派车单号清单按序导出派车单/轨迹 PDF。

编排（解析单号 → 逐号查找 → 派车单渲染 → 轨迹渲染 → 清单/未找到报告）放在本模块，
渲染与查询原语全部复用 vehicle_export.py / official_track.py，不重复实现。

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
import time
from datetime import datetime
from pathlib import Path

import config as _config
import platform_utils
import state
import uvmp
import vehicle_export as ve
import xlsx_util
from jobs import Cancelled

APP_ID = "pcd_export"


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
    out += ("trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF"
            % (len(objs) + 1, xref_pos)).encode()
    path.write_bytes(bytes(out))


def _clean_intermediate(pdf_path: Path):
    """ve.html_to_pdf 成功后会在 PDF 旁保留同名 .html 中间件（CLI 版留作排障用）；
    批次交付目录面向最终使用，予以删除保持整洁"""
    try:
        Path(pdf_path).with_suffix(".html").unlink()
    except OSError:
        pass


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

def run(job, params: dict) -> dict:
    cfg = _config.load()
    oe = cfg.get("order_export") or {}
    log = job.log

    codes = _resolve_codes(params)
    if not codes:
        raise RuntimeError("未解析到任何派车单号")
    export_pcd = bool(params.get("export_pcd", True))
    export_track = bool(params.get("export_track", True))
    # 自动校准行程结束时间：车载定位系统异常（结束时间早于12点）的行程按1小时逐次叠加至12点后
    calibrate_noon = bool(params.get("calibrate_noon", False))
    if not (export_pcd or export_track):
        raise RuntimeError("派车单 / 轨迹至少勾选一项")

    outdir = Path(params.get("outdir") or oe.get("default_outdir") or "./output").expanduser()
    if params.get("batch_subdir", oe.get("batch_subdir", True)):
        base = (params.get("batch_name") or "派车单").strip() or "派车单"
        outdir = outdir / ("%s_导出_%s" % (ve.safe_name(base),
                                           datetime.now().strftime("%Y%m%d_%H%M%S")))
    outdir.mkdir(parents=True, exist_ok=True)
    files_dir = outdir / "逐单"   # 逐单文件归子目录；批次根目录留给合并总 PDF/清单/诊断
    files_dir.mkdir(parents=True, exist_ok=True)
    log("共 %d 个派车单号；输出目录：%s" % (len(codes), outdir))
    manifest = ve.Manifest(outdir / "manifest.json")
    width = len(str(len(codes)))
    lookback = int(oe.get("lookback_days", 62))

    # ---- 登录与渲染器 ----
    client, auth = uvmp.make_client(cfg, log)
    vecfg = _config.ve_cfg(cfg)
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
    renderer = uvmp.start_renderer(client, auth, cfg, outdir, log)
    mock = bool((cfg.get("dev") or {}).get("mock"))
    # 取消即杀浏览器：CDP 在途等待（弹窗/瓦片/截图）立即报错退出，实现"点取消即刻中断"，
    # 而不是等当前单据渲完才被 check_cancel 发现
    if renderer is not None:
        job.add_cancel_hook(renderer.stop)
    # 内置模板已删除：官方渲染器是唯一产物路径；起不来必须报错引导，不得降级出仿制品
    if renderer is None and not mock:
        chosen = (_config.render_cfg(cfg).get("chrome") or "").strip()
        hint = ("所选浏览器（%s）可能不支持无头/CDP 调试模式，请到「设置」页换公司指定的专用浏览器；"
                "具体原因见上方日志" % chosen) if chosen else \
               "请到「设置」页选择公司指定浏览器后重试；具体原因见上方日志"
        raise RuntimeError("官方页面渲染器未启动——派车单/轨迹导出需要官方页面直出。" + hint)

    # ---- 阶段1：按号查找 ----
    found, not_found = [], []
    job.set_progress(0, len(codes), "按单号查询")
    for i, c in enumerate(codes, 1):
        job.check_cancel()
        job.set_progress(i - 1, len(codes), "查询 " + c["code"])
        order = ve.query_pcd_by_run_code(client, c["code"], c.get("date_hint") or "",
                                         lookback, log)
        if order:
            found.append({"seq": c["seq"], "code": c["code"], "order": order})
        else:
            not_found.append(c)
            log("  未找到：%s" % c["code"])
    job.set_progress(len(codes), len(codes), "查询完成")
    log("查询完成：找到 %d 单，未找到 %d 单" % (len(found), len(not_found)))

    rows = {f["seq"]: {"seq": f["seq"], "code": f["code"],
                       "vehicle": f["order"].get("vehicleNumber") or "",
                       "date": str(f["order"].get("planSendTimeStr")
                                   or f["order"].get("planSendTime") or "")[:10],
                       "pcd_file": "", "pcd_status": "未导出",
                       "track_file": "", "track_status": "未导出", "note": ""}
            for f in found}

    try:
        if export_pcd and found:
            _export_pcd(job, cfg, vecfg, client, renderer, found, outdir, files_dir,
                        chrome, electron_path, manifest, width, rows)
        if export_track and found:
            _export_track(job, cfg, vecfg, client, renderer, found, outdir, files_dir,
                          chrome, electron_path, manifest, width, rows, calibrate_noon)
    finally:
        if renderer:
            renderer.stop()

    # ---- 合并：全部导完后按序号顺序合成总 PDF（逐单文件保留在 逐单/；失败不毁批次）----
    ordered = sorted(rows.values(), key=lambda x: x["seq"])
    if export_pcd and found:
        ve.merge_pdfs([files_dir / r["pcd_file"] for r in ordered if r["pcd_file"]],
                      outdir / "_合并_派车单.pdf", log=log)
    if export_track and found:
        ve.merge_pdfs([files_dir / r["track_file"] for r in ordered if r["track_file"]],
                      outdir / "_合并_轨迹.pdf", log=log)

    # ---- 报告 ----
    _write_reports(outdir, rows, not_found, log)
    pcd_ok = sum(1 for r in rows.values() if r["pcd_status"] == "成功")
    pcd_fail = sum(1 for r in rows.values() if r["pcd_status"] == "失败")
    track_ok = sum(1 for r in rows.values() if r["track_status"] == "成功")
    track_fail = sum(1 for r in rows.values() if r["track_status"] == "失败")
    log("全部完成：派车单 成功 %d/失败 %d；轨迹 成功 %d/失败 %d；未找到 %d"
        % (pcd_ok, pcd_fail, track_ok, track_fail, len(not_found)))
    return {
        "dir": str(outdir), "total": len(codes), "found": len(found),
        "not_found": [c["code"] for c in not_found],
        "pcd_ok": pcd_ok, "pcd_fail": pcd_fail,
        "track_ok": track_ok, "track_fail": track_fail,
        "rows": sorted(rows.values(), key=lambda r: r["seq"]),
    }


# ---------------------------------------------------------------- 派车单

def _pcd_fname(seq: int, width: int, d: dict) -> str:
    return "%s_派车单_%s_%s_%s.pdf" % (
        str(seq).zfill(width), ve.safe_name(d.get("runCode")),
        ve.safe_name(d.get("vehicleNumber")),
        ve.safe_name(str(d.get("planSendTimeStr") or d.get("planSendTime") or "")[:10]))


def _export_pcd(job, cfg, vecfg, client, renderer, found, outdir, files_dir,
                chrome, electron_path, manifest, width, rows):
    log = job.log
    mock = bool((cfg.get("dev") or {}).get("mock"))
    todo = [f for f in found if not manifest.has("pcd", f["order"]["id"])]
    skipped = len(found) - len(todo)
    for f in found:
        if manifest.has("pcd", f["order"]["id"]):
            r = rows[f["seq"]]
            r["pcd_file"] = manifest.data.get("pcd", {}).get(f["order"]["id"], "")
            r["pcd_status"] = "成功"
            r["note"] = (r["note"] + "；" if r["note"] else "") + "派车单此前已导出"
    log("派车单：共 %d 单，已导出 %d 单，本次待导出 %d 单" % (len(found), skipped, len(todo)))
    if not todo:
        return

    done = 0
    if renderer is not None:
        # 逐单渲染（不批量：企业浏览器渲染慢/个别单数据异常时不再拖垮整批，也无需 pypdf 拆页）
        for i, f in enumerate(todo, 1):
            job.check_cancel()
            o = f["order"]
            try:
                details = ve.get_print_details(client, [o["id"]], 1)
                d = details[0] if details else o
                fname = _pcd_fname(f["seq"], width, d)
                renderer.render_pcd_pdf_one(d, files_dir / fname, tag="p%d" % f["seq"],
                                            diag_dir=outdir)
                manifest.put("pcd", o["id"], fname)
                r = rows[f["seq"]]
                r["pcd_file"], r["pcd_status"] = fname, "成功"
                done += 1
                job.set_progress(done, len(todo), "派车单 " + fname)
                log("[%d/%d] %s" % (done, len(todo), fname))
            except Exception as e:  # noqa: BLE001
                if job.cancelled:
                    raise Cancelled()   # 取消引发的浏览器销毁不算单据失败
                rows[f["seq"]]["pcd_status"] = "失败"
                log("  失败 %s: %s" % (f["code"], e))
            time.sleep(0.3)
        return

    # mock 联调：写占位 PDF（极简合法单页），只验证命名/顺序/续传管道；生产走不到这里
    if mock:
        for f in todo:
            job.check_cancel()
            fname = _pcd_fname(f["seq"], width, f["order"])
            _write_stub_pdf(files_dir / fname, "[mock] pcd %s" % f["code"])
            manifest.put("pcd", f["order"]["id"], fname)
            r = rows[f["seq"]]
            r["pcd_file"], r["pcd_status"] = fname, "成功"
            r["note"] = (r["note"] + "；" if r["note"] else "") + "mock 占位"
            done += 1
            job.set_progress(done, len(todo), "派车单 " + fname)
            log("[%d/%d] %s（mock 占位）" % (done, len(todo), fname))
        return


# ---------------------------------------------------------------- 轨迹

def _export_track(job, cfg, vecfg, client, renderer, found, outdir, files_dir,
                  chrome, electron_path, manifest, width, rows, calibrate_noon=False):
    log = job.log
    mock = bool((cfg.get("dev") or {}).get("mock"))
    todo = []
    for f in found:
        o = f["order"]
        begin = ve.clean_time(o.get("realSendTime"))
        end = ve.clean_time(o.get("realBackTime"))
        if manifest.has("track", o["id"]):
            r = rows[f["seq"]]
            r["track_file"] = manifest.data.get("track", {}).get(o["id"], "")
            r["track_status"] = "成功"
            r["note"] = (r["note"] + "；" if r["note"] else "") + "轨迹此前已导出"
        elif o.get("vehicleId") and begin and end:
            todo.append((f, begin, end))
        else:
            rows[f["seq"]]["track_status"] = "跳过（未完结或缺实际时间）"
    log("轨迹：待导出 %d 条（按派车单的实际出车/归队时间取轨迹）" % len(todo))
    done = 0
    noon_rows = []   # 行程结束时间在当日 12 点前的轨迹（出 _12点前结束行程.xlsx）

    for f, begin, end in todo:
        job.check_cancel()
        o = f["order"]
        label = "%s %s" % (o.get("vehicleNumber"), f["code"])
        try:
            points = ve.query_track_points(client, o["vehicleId"], begin, end)
            fname = "%s_轨迹_%s_%s_%s.pdf" % (
                str(f["seq"]).zfill(width), ve.safe_name(f["code"]),
                ve.safe_name(o.get("vehicleNumber")), ve.safe_name(begin[:10]))
            if renderer:
                import official_track as ot
                png = files_dir / (Path(fname).stem + ".png")
                seg = ve.query_track_segment(client, o["vehicleId"], begin, end)
                end_noon = ve.seg_end_before_noon(seg)
                pre_js = ""   # 「自动校准行程结束时间」：截图前注入弹窗的改写脚本（空 = 不改写）
                if end_noon:
                    noon_rows.append({"seq": f["seq"], "code": f["code"],
                                      "vehicle": o.get("vehicleNumber") or "", "end": end_noon})
                    if calibrate_noon:
                        cal = ve.calibrate_noon_end(seg)
                        if cal:
                            new_dt, old_dur, new_dur = cal
                            pre_js = ot.build_popup_rewrite_js(
                                datetime.strptime(end_noon[:19], "%Y-%m-%d %H:%M:%S"),
                                new_dt, old_dur, new_dur)
                            noon_rows[-1]["cal_end"] = new_dt.strftime("%Y-%m-%d %H:%M:%S")
                            noon_rows[-1]["cal_dur"] = ve.fmt_dur_cn(new_dur)
                            log("  [校准] %s 行程结束 %s → %s，行驶时间 → %s"
                                % (label, end_noon, noon_rows[-1]["cal_end"],
                                   noon_rows[-1]["cal_dur"]))
                if seg and seg.get("id"):
                    renderer.render_route_png(seg["id"], png, pre_shot_js=pre_js)   # 官方行程详情弹窗
                else:
                    renderer.render_pcd_png(o, png, pre_shot_js=pre_js)             # 回退按派车单弹窗
                ve.html_to_pdf(ot.PNG_WRAP_HTML.replace("$img_uri", png.as_uri()),
                               files_dir / fname, chrome, electron=electron_path)
                _clean_intermediate(files_dir / fname)
                if png.exists():
                    png.unlink()
            elif mock:
                # mock 联调：占位 PDF + 真实轨迹点 CSV（管道验证用；生产走不到这里）
                _write_stub_pdf(files_dir / fname, "[mock] track %s" % f["code"])
                r_note = "mock 占位"
            else:
                raise RuntimeError("官方页面渲染器未启动，轨迹无法导出"
                                   "（请到「设置」页选择公司指定浏览器后重试）")
            csv_name = "%s_轨迹点_%s.csv" % (str(f["seq"]).zfill(width),
                                             ve.safe_name(f["code"]))
            with open(files_dir / csv_name, "w", encoding="utf-8-sig") as fp:
                fp.write("时间,经度,纬度,速度,方向\n")
                for p in points:
                    t = p.get("locatetime") or p.get("time") or p.get("gpstime") or ""
                    if isinstance(t, (int, float)) and t > 1e12:
                        t = ve.fmt_epoch_ms(t)
                    fp.write("%s,%s,%s,%s,%s\n" % (t, p.get("dx", ""), p.get("dy", ""),
                                                   p.get("speed", ""), p.get("direction", "")))
            manifest.put("track", o["id"], fname)
            r = rows[f["seq"]]
            r["track_file"], r["track_status"] = fname, "成功"
            if not renderer and mock:
                r["note"] = (r["note"] + "；" if r["note"] else "") + "mock 占位"
            done += 1
            job.set_progress(done, len(todo), "轨迹 " + fname)
            log("[%d/%d] %s（%d 个轨迹点）" % (done, len(todo), fname, len(points)))
        except Exception as e:  # noqa: BLE001
            if job.cancelled:
                raise Cancelled()   # 取消引发的浏览器销毁不算单据失败
            rows[f["seq"]]["track_status"] = "失败"
            log("  失败 %s: %s" % (label, e))
    # 页码 = 该单在「轨迹成功序列」中的位次（_合并_轨迹.pdf 按 seq 升序、每单恰 1 页合并）；
    # 渲染失败的单没进合并 PDF，页码留空
    ok_seqs = sorted(r["seq"] for r in rows.values() if r["track_file"])
    ok_seq_set = set(ok_seqs)
    for nr in noon_rows:
        nr["page"] = bisect.bisect_right(ok_seqs, nr["seq"]) if nr["seq"] in ok_seq_set else None
    ve.write_noon_end_report(noon_rows, outdir / "_12点前结束行程.xlsx", log=log)


# ---------------------------------------------------------------- 报告

def _write_reports(outdir: Path, rows: dict, not_found: list, log):
    """_清单.csv（utf-8-sig，Excel 可直接打开）与 _未找到.txt"""
    lines = ["序号,派车单号,车牌,用车日期,派车单PDF,派车单状态,轨迹PDF,轨迹状态,备注"]
    for r in sorted(rows.values(), key=lambda x: x["seq"]):
        cells = [r["seq"], r["code"], r["vehicle"], r["date"],
                 r["pcd_file"], r["pcd_status"], r["track_file"], r["track_status"],
                 r["note"]]
        lines.append(",".join(str(c).replace(",", "，").replace("\n", " ") for c in cells))
    (outdir / "_清单.csv").write_text("\n".join(lines) + "\n", encoding="utf-8-sig")
    if not_found:
        (outdir / "_未找到.txt").write_text(
            "以下 %d 个派车单号未查询到：\n%s\n" % (
                len(not_found), "\n".join(c["code"] for c in not_found)),
            encoding="utf-8")
        log("未找到清单已写入 _未找到.txt（%d 个）" % len(not_found))
