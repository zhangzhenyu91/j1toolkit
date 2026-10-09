#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""轨迹弹窗「行程结束时间 + 行驶时间」CDP 改写试验脚本（独立工具，不接入客户端）。

用途：验证「打开官方行程详情弹窗 → 截图前注入 JS 改写 DOM → 再截图」链路的可行性。
产出供人工核对：
  dump.json   —— 弹窗里全部「像时间/像时长」的 DOM 元素清单（含所在行上下文）；
  before.png  —— 改写前的弹窗截图（与生产导出口径一致的裁剪区域）；
  after.png   —— 改写后的弹窗截图。

改写口径（v3）：
- 在叶子元素级（textContent）操作，兼容官方页面把时间拆成多个文本节点的情况；
- 结束时间按「时间值」匹配：兼容 &nbsp;/空格、- 与 /、带不带秒，命中后按原格式写回；
- 行驶时间按「时长值」匹配（中文「X小时X分钟X秒」各形态，容差 ±90s 吸收页面取整），
  命中后按「H小时M分钟（S秒）」写回——新时长 = 校准后结束时间 − 行程开始时间。

用法（在内网机器上，试验包目录下）：
  python3 track_time_rewrite_trial.py <派车单号> [--date YYYY-MM-DD]
                                      [--end "YYYY-MM-DD HH:MM:SS"] [--out 目录]

  --end  指定改写成的结束时间；缺省按「1 小时逐次叠加至 12 点后」规则自动计算
         （行程结束时间不早于 12:00 时跳过改写，只出 dump 与截图）。
依赖：同工具箱本体（SSO 凭据取自客户端 config.json 拷入的 toolkit/config.json；
浏览器取其中 render.chrome 或自动探测；dev.mock 开启时不可用）。
"""
import argparse
import json
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
for _p in (str(SCRIPT_DIR), str(SCRIPT_DIR / "toolkit")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

import config as _config          # noqa: E402
import uvmp                        # noqa: E402
import vehicle_export as ve        # noqa: E402
import official_track              # noqa: E402


def log(msg):
    print(msg, flush=True)


# ---------------------------------------------------------------- 弹窗 DOM 探查/改写 JS

SCRIPT_VERSION = "v3"

# 弹窗根：优先官方内容盒 trackPictureBox（与截图裁剪同一目标），取不到退回最后一个官方弹窗
_ROOT_JS = ("document.getElementById('trackPictureBox') || "
            "(function(){var m=document.querySelectorAll('.cube.modal.fade');"
            "return m.length ? m[m.length-1] : null;})()")

# 收集弹窗内全部「像时间」或「像时长」的叶子元素与输入框：标签/类名/所在行上下文/文本
DUMP_JS = """
(function(){
  var root = %s;
  if (!root) return 'ERR:无弹窗';
  var timeRe = /\\d{4}[-\\/年]\\d{1,2}[-\\/月]\\d{1,2}|\\d{1,2}:\\d{2}(:\\d{2})?/;
  var durRe = /时长|行驶|\\d+(?:\\.\\d+)?\\s*小时|\\d+\\s*分钟/;
  function labelOf(el){
    var cands = [];
    if (el.previousElementSibling) cands.push(el.previousElementSibling.textContent);
    if (el.parentElement && el.parentElement.previousElementSibling)
      cands.push(el.parentElement.previousElementSibling.textContent);
    var tr = el.closest('tr');
    if (tr && tr.cells && tr.cells.length > 1) cands.push(tr.cells[0].textContent);
    var box = el.closest('.form-group,.cube-form-item,.dataform-item,li');
    if (box) { var l = box.querySelector('label'); if (l) cands.push(l.textContent); }
    for (var i = 0; i < cands.length; i++) {
      var t = (cands[i] || '').trim();
      if (t && t !== (el.textContent || '').trim()) return t.slice(0, 30);
    }
    return '';
  }
  function ctxOf(el){
    var row = el.closest('tr') || el.parentElement;
    if (row && row.parentElement) row = row.parentElement;   // 再上一层，拿到「标签+值」整行
    return row ? row.textContent.replace(/\\s+/g, ' ').trim().slice(0, 120) : '';
  }
  var out = [];
  root.querySelectorAll('*').forEach(function(el){
    if (el.children.length === 0) {
      var t = (el.textContent || '').trim();
      if (t && timeRe.test(t)) {
        out.push({kind: 'time', tag: el.tagName, cls: String(el.className).slice(0, 60),
                  label: labelOf(el), ctx: ctxOf(el), text: t.slice(0, 90)});
      } else if (t && t.length <= 40 && durRe.test(t)) {
        out.push({kind: 'dur', tag: el.tagName, cls: String(el.className).slice(0, 60),
                  label: labelOf(el), ctx: ctxOf(el), text: t.slice(0, 90)});
      }
    }
    if ((el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && timeRe.test(el.value || '')) {
      out.push({kind: 'input', tag: el.tagName, cls: String(el.className).slice(0, 60),
                label: labelOf(el), ctx: ctxOf(el), text: String(el.value).slice(0, 90)});
    }
  });
  return JSON.stringify(out);
})()
""" % _ROOT_JS

# 改写：结束时间按值匹配（兼容格式差异）写回新时间；行驶时长按值匹配（±90s 容差）写回新时长。
# 占位符 __ROOT__/__OLDC__/__NEWC__/__OLDDUR__/__NEWDUR__ 由 Python 侧 str.replace 注入
# （不用 % 格式化：JS 里有取模运算，百分号转义易错）
REWRITE_JS = r"""
(function(){
  var root = __ROOT__;
  if (!root) return 'ERR:无弹窗';
  var oldC = __OLDC__, newC = __NEWC__, oldDur = __OLDDUR__, newDur = __NEWDUR__;
  var timeRe = /(\d{4})([-\/])(\d{1,2})\2(\d{1,2})([\s\u00a0T]+)(\d{1,2}):(\d{2})(?::(\d{2}))?/g;
  var durRe = /(\d+)\s*小时\s*(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+(?:\.\d+)?)(\s*)小时/g;
  function pad(v, w){ v = String(v); while (v.length < w) v = '0' + v; return v; }
  function eqTime(y, mo, d, h, mi, s, hasSec){
    if (y !== oldC[0] || mo !== oldC[1] || d !== oldC[2]) return false;
    if (h !== oldC[3] || mi !== oldC[4]) return false;
    return hasSec ? (s === oldC[5]) : true;   // 页面不显示秒时按分精度判定
  }
  function fmtDur(sec, withSec){
    var h = Math.floor(sec / 3600), m, tail = '';
    if (withSec) { m = Math.floor((sec % 3600) / 60); tail = (sec % 60) + '秒'; }
    else { m = Math.round((sec % 3600) / 60); if (m === 60) { h += 1; m = 0; } }
    return (h > 0 ? (h + '小时' + m + '分钟') : (m + '分钟')) + tail;
  }
  var total = 0, details = [], seen = [];
  function procText(s, kind){
    var out = s.replace(timeRe, function(m, y, sep, mo, d, gap, h, mi, ss){
      if (seen.length < 50 && seen.indexOf(m) < 0) seen.push(m);
      if (!eqTime(+y, +mo, +d, +h, +mi, +(ss || 0), ss !== undefined)) return m;
      var r = pad(newC[0], y.length) + sep + pad(newC[1], mo.length) + sep
            + pad(newC[2], d.length) + gap + pad(newC[3], h.length) + ':' + pad(newC[4], mi.length)
            + (ss !== undefined ? ':' + pad(newC[5], ss.length) : '');
      total++;
      if (details.length < 20) details.push({kind: kind, before: m, after: r});
      return r;
    });
    out = out.replace(durRe, function(m, h1, m1, s1, m2, s2, hd, gap){
      if (seen.length < 50 && seen.indexOf(m) < 0) seen.push(m);
      var sec, r;
      if (hd !== undefined) {
        // 小数小时形态（如「0.63 小时」）：按原小数位数与原有间隔写回，容差 = 半位精度 + 30s
        var dec = (hd.split('.')[1] || '').length;
        sec = Math.round(parseFloat(hd) * 3600);
        if (Math.abs(sec - oldDur) > 0.5 * Math.pow(10, -dec) * 3600 + 30) return m;
        r = (newDur / 3600).toFixed(dec) + gap + '小时';
      } else {
        sec = h1 !== undefined
          ? (+h1) * 3600 + (+m1) * 60 + (+(s1 || 0))
          : (+m2) * 60 + (+(s2 || 0));
        if (Math.abs(sec - oldDur) > 90) return m;   // 容差吸收页面取整（37分47秒≈38分钟）
        r = fmtDur(newDur, (s1 !== undefined || s2 !== undefined));
      }
      total++;
      if (details.length < 20) details.push({kind: kind + ':dur', before: m, after: r});
      return r;
    });
    return out;
  }
  // 叶子元素（无子元素）：整段 textContent 处理，兼容时间被拆成多个文本节点的情况
  root.querySelectorAll('*').forEach(function(el){
    if (el.children.length === 0 && el.tagName !== 'SCRIPT' && el.tagName !== 'STYLE') {
      var s = el.textContent;
      timeRe.lastIndex = 0; durRe.lastIndex = 0;
      if (s && (timeRe.test(s) || durRe.test(s))) {
        timeRe.lastIndex = 0; durRe.lastIndex = 0;
        var out = procText(s, 'text');
        if (out !== s) el.textContent = out;
      }
    }
  });
  root.querySelectorAll('input,textarea').forEach(function(el){
    var v = el.value || '';
    timeRe.lastIndex = 0; durRe.lastIndex = 0;
    if (v && (timeRe.test(v) || durRe.test(v))) {
      timeRe.lastIndex = 0; durRe.lastIndex = 0;
      var out = procText(v, 'input');
      if (out !== v) el.value = out;
    }
  });
  return JSON.stringify({replaced: total, details: details, seen: seen});
})()
"""


# ---------------------------------------------------------------- 时间校准

def calibrate_end(end_str: str):
    """结束时间早于 12:00 → 以 1 小时为单位叠加至 12 点后（10:32→12:32）；否则返回 None"""
    try:
        dt = datetime.strptime(end_str[:19], "%Y-%m-%d %H:%M:%S")
    except (ValueError, TypeError):
        return None
    if dt.hour >= 12:
        return None
    while dt.hour < 12:
        dt += timedelta(hours=1)
    return dt


def _comps(dt: datetime):
    return [dt.year, dt.month, dt.day, dt.hour, dt.minute, dt.second]


def _fmt_dur_cn(seconds: int) -> str:
    h, m = seconds // 3600, (seconds % 3600) // 60
    s = seconds % 60
    return ("%d小时%d分钟%d秒" % (h, m, s)) if h else ("%d分钟%d秒" % (m, s))


# ---------------------------------------------------------------- 主流程

def main():
    ap = argparse.ArgumentParser(description="轨迹弹窗「行程结束时间」CDP 改写试验")
    ap.add_argument("code", help="派车单号（该单应有已完结的实际出/归车时间）")
    ap.add_argument("--date", default="", help="用车日期提示 YYYY-MM-DD（加速按单号查找）")
    ap.add_argument("--end", default="", help="改写成的结束时间（缺省按 +1h 叠加规则自动算）")
    ap.add_argument("--out", default="", help="输出目录（默认 ./output/时间改写试验_时间戳）")
    args = ap.parse_args()
    log("track_time_rewrite_trial %s（结束时间+行驶时间 按值匹配改写）" % SCRIPT_VERSION)

    outdir = Path(args.out) if args.out else (
        SCRIPT_DIR / "output" / ("时间改写试验_%s" % datetime.now().strftime("%Y%m%d_%H%M%S")))
    outdir.mkdir(parents=True, exist_ok=True)
    log("输出目录：%s" % outdir)

    cfg = _config.load()
    if (cfg.get("dev") or {}).get("mock"):
        raise SystemExit("dev.mock 开启中：本试验需要真实官方页面，请关闭开发模式后重试")
    client, auth = uvmp.make_client(cfg, log)

    # 1) 找单 → 找行程段
    lookback = int((cfg.get("order_export") or {}).get("lookback_days", 62))
    order = ve.query_pcd_by_run_code(client, args.code.strip(), args.date.strip(), lookback, log)
    if not order:
        raise SystemExit("未找到派车单：%s" % args.code)
    begin = ve.clean_time(order.get("realSendTime"))
    end = ve.clean_time(order.get("realBackTime"))
    log("派车单：%s | 车牌 %s | 实际出车 %s | 实际归队 %s"
        % (args.code, order.get("vehicleNumber"), begin, end))
    if not (order.get("vehicleId") and begin and end):
        raise SystemExit("该单缺 vehicleId 或实际出/归队时间，无法定位行程段")
    seg = ve.query_track_segment(client, order["vehicleId"], begin, end)
    if not seg or not seg.get("id"):
        raise SystemExit("未查到行程段（无法打开行程详情弹窗），可换个单号再试")
    log("行程段：id=%s | 开始 %s | 结束 %s | 里程 %s"
        % (seg.get("id"), seg.get("starttime"), seg.get("endtime"), seg.get("miles")))

    seg_start = ve.clean_time(seg.get("starttime"))
    seg_end = ve.clean_time(seg.get("endtime"))
    try:
        start_dt = datetime.strptime(seg_start[:19], "%Y-%m-%d %H:%M:%S")
        old_dt = datetime.strptime(seg_end[:19], "%Y-%m-%d %H:%M:%S")
    except (ValueError, TypeError):
        raise SystemExit("行程段起止时间格式无法识别：%r ~ %r ——请把这行输出发给开发者"
                         % (seg_start, seg_end))
    if args.end.strip():
        raw = args.end.strip()
        new_dt = datetime.strptime(raw if len(raw) > 10 else seg_end[:10] + " " + raw,
                                   "%Y-%m-%d %H:%M:%S")
    else:
        new_dt = calibrate_end(seg_end)
    do_rewrite = new_dt is not None
    if do_rewrite:
        old_dur = int((old_dt - start_dt).total_seconds())
        new_dur = int((new_dt - start_dt).total_seconds())
        log("改写目标：结束时间 %s → %s；行驶时间 %s → %s"
            % (seg_end, new_dt.strftime("%Y-%m-%d %H:%M:%S"),
               _fmt_dur_cn(old_dur), _fmt_dur_cn(new_dur)))
    else:
        log("【注意】行程结束时间 %s 不早于 12:00，本次跳过改写，只出 dump 与 before.png" % seg_end)

    # 2) 起官方渲染器（与生产同路径），开行程详情弹窗
    renderer = uvmp.start_renderer(client, auth, cfg, outdir, log)
    if renderer is None:
        raise SystemExit("官方渲染器启动失败（浏览器配置见设置页/ config.ini [render]）")
    try:
        browser = renderer.browser
        R = official_track.OfficialTrackRenderer
        r = browser.eval(R.OPEN_DIALOG_JS % (json.dumps(R.TEMPLATE_ROUTE),
                                             json.dumps({"routeId": seg["id"]})), timeout=60)
        if r != "ok":
            raise SystemExit("打开官方轨迹弹窗失败: %s" % r)
        if not browser.wait_expr("!!document.querySelector('.cube.modal.fade .modal-body *')",
                                 timeout=renderer.wait_dialog_s):
            raise SystemExit("轨迹弹窗未出现")
        browser.wait_expr("!!document.querySelector('.cube.modal.fade canvas')",
                          timeout=min(40, renderer.wait_dialog_s))
        if renderer.min_wait_s > 0:
            time.sleep(renderer.min_wait_s)
        idle_ok = browser.wait_network_idle(quiet_s=renderer.idle_quiet_s,
                                            timeout=max(15, renderer.wait_tiles_s),
                                            url_substr="map.sgcc.com.cn", min_seen=2)
        log("地图加载检测：%s" % ("静默完成" if idle_ok else "兜底超时"))
        time.sleep(1.0)

        def dump_times(tag):
            raw = browser.eval(DUMP_JS)
            rows = json.loads(raw) if raw and not str(raw).startswith("ERR") else []
            if tag == "before":
                (outdir / "dump.json").write_text(
                    json.dumps(rows, ensure_ascii=False, indent=1), encoding="utf-8")
            log("弹窗时间/时长元素 %d 个（%s）：" % (len(rows), tag))
            for i, it in enumerate(rows, 1):
                log("  [%d] %s <%s> %s" % (i, it.get("kind"), it.get("tag"), it.get("text")))
                log("       上下文：%s" % (it.get("ctx") or "（无）"))
            return rows

        # 3) 探查：弹窗里全部时间/时长元素落 dump.json
        dump_times("before，明细同步写入 dump.json")

        # 4) 改写前截图
        rect = browser.eval(R.RECT_JS)
        if not rect:
            raise SystemExit("弹窗内容区未找到")
        box = json.loads(rect)
        clip = {"x": max(0, box["x"]), "y": max(0, box["y"]),
                "width": box["width"], "height": box["height"], "scale": 1}
        browser.screenshot_clip(clip, outdir / "before.png")
        log("改写前截图：%s" % (outdir / "before.png"))

        # 5) 注入改写 → 立即复验 → 改写后截图
        if do_rewrite:
            rw_js = (REWRITE_JS
                     .replace("__ROOT__", _ROOT_JS)
                     .replace("__OLDC__", json.dumps(_comps(old_dt)))
                     .replace("__NEWC__", json.dumps(_comps(new_dt)))
                     .replace("__OLDDUR__", str(old_dur))
                     .replace("__NEWDUR__", str(new_dur)))
            rw_raw = browser.eval(rw_js)
            rw = json.loads(rw_raw) if rw_raw and not str(rw_raw).startswith("ERR") else {}
            log("改写完成：命中 %d 处" % rw.get("replaced", 0))
            for d in rw.get("details", []):
                log("  [%s] %s → %s" % (d.get("kind"), d.get("before"), d.get("after")))
            if not rw.get("replaced"):
                log("未命中！页面上出现的时间/时长串为：%s" % "、".join(rw.get("seen", [])))
                log("请把 dump.json 与以上输出发给开发者调整匹配口径")
            else:
                log("改写后立即复验：")
                dump_times("after")
            time.sleep(0.5)   # 给重排一瞬
            browser.screenshot_clip(clip, outdir / "after.png")
            log("改写后截图：%s" % (outdir / "after.png"))

        browser.eval(R.CLOSE_DIALOG_JS)
    finally:
        renderer.stop()

    log("试验完成。请对比 before.png / after.png，并结合 dump.json 确认：")
    log("  1. 结束时间、行驶时间改写是否生效、位置是否正确；")
    log("  2. 地图上的时间标注是否为 canvas 绘制（canvas 内容 DOM 改不到）。")


if __name__ == "__main__":
    main()
