package render

import (
	"encoding/json"
	"fmt"
)

// 官方页面地址（INDEX_URL + "#home?T=<token>" 即页面自动登录入口）。
const IndexURL = "http://uvmp.sgcc.com.cn/uvmp-web/factoryLayout/index.html"

// 国密浏览器无头模式缺 window.crypto.random（页面 SM.js 的 SM2 加密依赖它取随机数，
// 缺失会连锁炸掉页面登录/初始化），用标准 getRandomValues 补齐；须在页面脚本之前注入。
const cryptoRandomPolyfillJS = `
(function(){
  try {
    var c = window.crypto;
    if (c && typeof c.random !== 'function') {
      c.random = function(len){
        var n = (typeof len === 'number' && len > 0 && len < 4096) ? len : 32;
        var b = new Uint8Array(n);
        c.getRandomValues(b);
        var s = '';
        for (var i = 0; i < n; i++) s += ('0' + b[i].toString(16)).slice(-2);
        return s;
      };
    }
  } catch (e) {}
})();
`

// injectAuthJS 模板：登录超时兜底，向页面注入 token/refreshToken/loginUser。
const injectAuthJSTpl = `
(function(token, refresh, loginUser){
  try {
    if (typeof cube === 'undefined') return 'ERR:无cube';
    cube.token = token;
    if (refresh) cube.refreshToken = refresh;
    cube.loginUser = loginUser;
    return 'ok';
  } catch(e) { return 'ERR:' + (e && (e.message || e)); }
})(%s, %s, %s)
`

// buildInjectAuthJS 组装凭证注入脚本（参数经 json.Marshal，令牌不进日志）。
func buildInjectAuthJS(token, refreshToken string, loginUser map[string]any) (string, error) {
	t, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	r, err := json.Marshal(refreshToken)
	if err != nil {
		return "", err
	}
	if loginUser == nil {
		loginUser = map[string]any{}
	}
	u, err := json.Marshal(loginUser)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(injectAuthJSTpl, t, r, u), nil
}

// openDialogJS 模板：调官方 cube.showDialog 打开轨迹弹窗（行程轨迹）。
const openDialogJSTpl = `
(function(templateName, params){
  try {
    cube.showDialog({
      isShowDialog: true, title: "行程轨迹", eleId: "trackPicture",
      submitFormOnConfirm: true,
      templateOptions: { name: templateName, params: params },
      width: (window.innerWidth-100)+"px",
      height: (window.innerHeight-100)+"px",
      top: "20px", bottom: "20px",
      isShowMaxBtn: false, isShowMinBtn: false,
      isShowConfirmBtn: false, isShowCloseBtn: false
    });
    return "ok";
  } catch(e) { return "ERR:" + (e && (e.message || e)); }
})(%s, %s)
`

// buildOpenDialogJS 组装打开轨迹弹窗脚本。
func buildOpenDialogJS(templateName string, params any) (string, error) {
	t, err := json.Marshal(templateName)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(openDialogJSTpl, t, p), nil
}

// 官方两种轨迹弹窗模板名：行程详情（routeId，字段最全）、按派车单（runData）。
const (
	TemplateRoute = "factoryLayout.monitor.realMonitor.track_picture"
	TemplatePCD   = "factoryLayout.monitor.trackQuery.pcd_track_picture"
	// TemplatePCDPrint 官方派车单批量打印弹窗。
	TemplatePCDPrint = "factoryLayout.running.pcdmanager.sx.sxpcdlistprint"
)

// openPCDPrintJS 模板：打开官方「派车单批量打印」弹窗（每单独立开窗）。
const openPCDPrintJSTpl = `
(function(items){
  try {
    cube.showDialog({
      isShowDialog: true, title: "派车单批量打印",
      submitFormOnConfirm: false, isClickEnterCloseDialog: false,
      templateOptions: {
        name: 'factoryLayout.running.pcdmanager.sx.sxpcdlistprint',
        type: "viewModel",
        params: { data: items, print: true }
      },
      width: (window.innerWidth-100)+"px",
      isWidthHeightAuto: true,
      isShowConfirmBtn: false, isShowMaxBtn: false, isShowMinBtn: false
    });
    return "ok";
  } catch(e) { return "ERR:" + (e && (e.message || e)); }
})(%s)
`

// buildOpenPCDPrintJS 组装打开派车单打印弹窗脚本（items 为逐单清单）。
func buildOpenPCDPrintJS(items []map[string]any) (string, error) {
	if items == nil {
		items = []map[string]any{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(openPCDPrintJSTpl, b), nil
}

// extractPrintDomJS 提取打印区 DOM（head 样式 + 打印区 outerHTML）。
const extractPrintDomJS = `
(function(){
  var area = document.getElementById('sxpcdmanageprint-area');
  if (!area) return null;
  var head = Array.from(document.head.querySelectorAll('style,link[rel=stylesheet]'))
                  .map(function(e){ return e.outerHTML; }).join('');
  return JSON.stringify({head: head, body: area.outerHTML});
})()
`

// probeJS 页面环境探测（登录卡点定位）。
const probeJS = `
(function(){
  var o = {cube: typeof cube, ko: typeof ko, require_: typeof require,
           win_require: typeof window.require, readyState: document.readyState,
           crypto_random: (window.crypto ? typeof window.crypto.random : 'no-crypto'),
           has_token: (typeof cube!=='undefined' ? !!cube.token : null),
           has_loginUser: (typeof cube!=='undefined' ? !!cube.loginUser : null)};
  try {
    if (typeof CUBE !== 'undefined' && CUBE.self) o.cube_msg = typeof CUBE.self.msg;
    if (typeof ko !== 'undefined' && ko.components)
      o.print_tpl_registered = ko.components.isRegistered(
        'factoryLayout.running.pcdmanager.sx.sxpcdlistprint');
  } catch(e) { o.probe_err = String(e); }
  return JSON.stringify(o);
})()
`

// stylesReadyJS 文档/样式表就绪检测（包装页替代固定 sleep）。
const stylesReadyJS = `
(function(){
  if (document.readyState !== 'complete') return false;
  var links = document.querySelectorAll('link[rel=stylesheet]');
  for (var i = 0; i < links.length; i++) {
    try { if (!links[i].sheet) return false; } catch (e) {}
  }
  return true;
})()
`

// tplReadyJS 打印组件注册或加载器就绪检测（企业浏览器 requirejs 兜底曾报
// "(require || window.require) is not a function"，故先等就绪再开弹窗）。
const tplReadyJS = `
(typeof ko!=='undefined' && ko.components && ko.components.isRegistered(
  'factoryLayout.running.pcdmanager.sx.sxpcdlistprint'))
  || typeof window.require==='function' || typeof require==='function'
`

// section0ReadyJS 打印区首节渲染完成检测。
const section0ReadyJS = `document.querySelectorAll('#sxpcdmanageprint-area .Section0').length >= 1`

// printAreaStateJS 弹窗状态探测（Section0 超时时的诊断）。
const printAreaStateJS = `
(function(){return JSON.stringify({
  modal: document.querySelectorAll('.cube.modal.fade').length,
  area: !!document.getElementById('sxpcdmanageprint-area'),
  s0: document.querySelectorAll('#sxpcdmanageprint-area .Section0').length})})()
`

// rectJS 轨迹弹窗内容区矩形（截图裁剪框）。
const rectJS = `
(function(){
  var el = document.getElementById('trackPictureBox');
  if (!el) el = document.querySelector('.cube.modal.fade .modal-body');
  if (!el) return null;
  return JSON.stringify(el.getBoundingClientRect());
})()
`

// closeDialogJS 关闭弹窗（派发 mousedown 到 .close；无按钮则移除弹窗与遮罩）。
const closeDialogJS = `
(function(){
  var closed = 0;
  document.querySelectorAll('.cube.modal.fade .close').forEach(function(b){
    b.dispatchEvent(new MouseEvent('mousedown', {bubbles:true})); closed++;
  });
  if (!closed) document.querySelectorAll('.cube.modal.fade').forEach(function(e){ e.remove(); });
  document.querySelectorAll('.modal-backdrop, .cube-modal-backdrop').forEach(function(e){ e.remove(); });
  return closed;
})()
`

// dialogBodyReadyJS 轨迹弹窗内容出现检测。
const dialogBodyReadyJS = `!!document.querySelector('.cube.modal.fade .modal-body *')`

// dialogCanvasReadyJS 轨迹地图 canvas 出现检测（无轨迹点时可能一直没有，容忍）。
const dialogCanvasReadyJS = `!!document.querySelector('.cube.modal.fade canvas')`

// 弹窗时间改写（自动校准行程结束时间用）。已在 track_time_rewrite_trial.py 经内网实测验证。
// 匹配按「值」而非死字符串：兼容 &nbsp;、- 与 / 分隔、有无秒、文本节点拆分；时长兼容
// 「X小时X分钟(X秒)」与小数小时，±90s 容差吸收页面取整。占位符 __OLDC__/__NEWC__
// （[Y,M,D,h,m,s] JSON 数组）与 __OLDDUR__/__NEWDUR__（秒）由 BuildPopupRewriteJS 注入
// （不用 Sprintf：JS 里有取模运算，百分号转义易错）。
const popupRewriteJSTpl = `
(function(){
  var root = document.getElementById('trackPictureBox');
  if (!root) { var m = document.querySelectorAll('.cube.modal.fade'); root = m.length ? m[m.length-1] : null; }
  if (!root) return 'ERR:无弹窗';
  var oldC = __OLDC__, newC = __NEWC__, oldDur = __OLDDUR__, newDur = __NEWDUR__;
  var timeRe = /(\d{4})([-\/])(\d{1,2})\2(\d{1,2})([\s\u00a0T]+)(\d{1,2}):(\d{2})(?::(\d{2}))?/g;
  var durRe = /(\d+)\s*小时\s*(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+)\s*分钟\s*(?:(\d+)\s*秒)?|(\d+(?:\.\d+)?)(\s*)小时/g;
  function pad(v, w){ v = String(v); while (v.length < w) v = '0' + v; return v; }
  function eqTime(y, mo, d, h, mi, s, hasSec){
    if (y !== oldC[0] || mo !== oldC[1] || d !== oldC[2]) return false;
    if (h !== oldC[3] || mi !== oldC[4]) return false;
    return hasSec ? (s === oldC[5]) : true;
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
        var dec = (hd.split('.')[1] || '').length;
        sec = Math.round(parseFloat(hd) * 3600);
        if (Math.abs(sec - oldDur) > 0.5 * Math.pow(10, -dec) * 3600 + 30) return m;
        r = (newDur / 3600).toFixed(dec) + gap + '小时';
      } else {
        sec = h1 !== undefined
          ? (+h1) * 3600 + (+m1) * 60 + (+(s1 || 0))
          : (+m2) * 60 + (+(s2 || 0));
        if (Math.abs(sec - oldDur) > 90) return m;
        r = fmtDur(newDur, (s1 !== undefined || s2 !== undefined));
      }
      total++;
      if (details.length < 20) details.push({kind: kind + ':dur', before: m, after: r});
      return r;
    });
    return out;
  }
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
`

// documentWriteJS 在新标签页写入完整 HTML（包装页/打印页渲染用）。
func buildDocumentWriteJS(fullHTML string) (string, error) {
	b, err := json.Marshal(fullHTML)
	if err != nil {
		return "", err
	}
	return "document.write(" + string(b) + "); document.close(); 'ok'", nil
}

// buildPrintFullHTML 拼出打印区的完整 HTML（head 样式 + body 打印区）。
func buildPrintFullHTML(domHead, domBody string) string {
	return "<!DOCTYPE html><html><head><meta charset='utf-8'>" + domHead + "</head><body>" + domBody + "</body></html>"
}

// trimForDiag 诊断输出截断（与 Python [:n] 口径一致，按字符）。
func trimForDiag(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
