/* ============================================================
   出工日志 WorkLogs · PC 端主应用
   分区：工具 / 通用组件 / API 封装 / 状态 / 导航与用户 /
        日志看板 / 日志模态 / 照片链路 / Lightbox /
        照片库 / 验证报告 / 数据管理 / 启动
   ============================================================ */
(function () {
'use strict';

/* ================= 工具 ================= */
var $ = function (s, c) { return (c || document).querySelector(s); };
var $$ = function (s, c) { return Array.prototype.slice.call((c || document).querySelectorAll(s)); };
var RM = matchMedia('(prefers-reduced-motion: reduce)').matches;

function pad(n) { return n < 10 ? '0' + n : '' + n; }
/* HTML 转义：所有接口数据上屏前必须过一遍 */
function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
  });
}
function fmtDate(d) { return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()); }
function parseDate(s) { var a = s.split('-'); return new Date(+a[0], +a[1] - 1, +a[2]); }
var DOW = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
/* 「M月D日 周X」 */
function fmtDateCN(s) { var d = parseDate(s); return (d.getMonth() + 1) + '月' + d.getDate() + '日 ' + DOW[d.getDay()]; }
function todayStr() { return fmtDate(new Date()); }
/* 「2026 年 8 月」 */
function fmtMonthCN(m) { var a = m.split('-'); return a[0] + ' 年 ' + (+a[1]) + ' 月'; }
/* 水印拍摄时间格式 YYYY.MM.DD HH:mm */
function fmtWmTime(d) {
  return d.getFullYear() + '.' + pad(d.getMonth() + 1) + '.' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
}
/* 默认范围：当天 ≤10 号 → 上月整月，否则本月 1 号 ~ 今天 */
function defaultRange() {
  var now = new Date();
  if (now.getDate() <= 10) {
    return {
      from: fmtDate(new Date(now.getFullYear(), now.getMonth() - 1, 1)),
      to: fmtDate(new Date(now.getFullYear(), now.getMonth(), 0))
    };
  }
  return { from: fmtDate(new Date(now.getFullYear(), now.getMonth(), 1)), to: fmtDate(now) };
}
function shake(el) { if (!el) return; el.classList.remove('shake'); void el.offsetWidth; el.classList.add('shake'); }

/* JS 生成内容所需的 inline SVG 片段 */
var I = {
  ok: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="m8 12.3 2.6 2.6L16 9.5"/></svg>',
  err: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5"/><path d="M12 16.4v.2"/></svg>',
  x: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg>',
  plus: '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
  check: '<svg viewBox="0 0 12 12" width="11" height="11"><path d="M2.2 6.4 4.8 8.9 9.8 3.4" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  warn: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5.5"/><path d="M12 16.4v.2"/></svg>',
  chevL: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 5.5 8 12l6.5 6.5"/></svg>',
  chevR: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 5.5 16 12l-6.5 6.5"/></svg>',
  wmImg: '<svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="4.5" width="17" height="15" rx="3"/><circle cx="9" cy="10" r="1.6"/><path d="M4 17.5 10 12l4 4 3-3 3.5 3.5"/><path d="M7 16.5h4" opacity=".6"/></svg>',
  magic: '<svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m15 4.5 4.5 4.5L8 20.5H3.5V16L15 4.5Z"/><path d="m13 7 4 4"/></svg>',
  empty: '<svg viewBox="0 0 120 90" width="110" height="82" fill="none" stroke="#D8D6CD" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><rect x="26" y="14" width="68" height="62" rx="8"/><path d="M44 14v-4a6 6 0 0 1 6-6h20a6 6 0 0 1 6 6v4"/><path d="M40 34h40M40 46h40M40 58h24"/><circle cx="92" cy="66" r="14" fill="#FBFBF9"/><path d="m86 66 4 4 8-8" stroke="#C6C4BB"/></svg>'
};

/* ================= 通用组件：Toast ================= */
function toast(msg, opts) {
  opts = opts || {};
  var t = document.createElement('div');
  t.className = 'toast' + (opts.type === 'err' ? ' err' : '');
  t.innerHTML = (opts.type === 'err' ? I.err : I.ok) + '<span></span>';
  t.querySelector('span').textContent = msg;
  $('#toastWrap').appendChild(t);
  if (!opts.sticky) {
    setTimeout(function () { untoast(t); }, opts.ms || 2000);
  }
  return t;
}
function untoast(t) {
  if (t && t.parentNode) {
    t.classList.add('bye');
    setTimeout(function () { t.remove(); }, 320);
  }
}

/* ================= 通用组件：模态框（动态、可叠层） ================= */
var modalStack = [];
function mHead(title) {
  return '<div class="m-head"><span class="m-title">' + esc(title) + '</span><button class="iconbtn m-x">' + I.x + '</button></div>';
}
function createModal(html, opts) {
  opts = opts || {};
  var mask = document.createElement('div');
  mask.className = 'mask';
  if (lbInst) mask.style.zIndex = '320'; // 照片预览（z-index 300）之上再叠模态
  var box = document.createElement('div');
  box.className = 'modal';
  if (opts.width) box.style.width = opts.width + 'px';
  box.innerHTML = html;
  mask.appendChild(box);
  document.body.appendChild(mask);
  requestAnimationFrame(function () { mask.classList.add('open'); });
  var inst = {
    mask: mask, box: box, closed: false,
    close: function () {
      if (inst.closed) return;
      inst.closed = true;
      var i = modalStack.indexOf(inst);
      if (i >= 0) modalStack.splice(i, 1);
      mask.classList.remove('open');
      setTimeout(function () { mask.remove(); }, 300);
      if (opts.onClose) opts.onClose();
    }
  };
  if (!opts.lock) {
    mask.addEventListener('mousedown', function (e) { if (e.target === mask) inst.close(); });
  }
  modalStack.push(inst);
  $$('.m-x', box).forEach(function (b) { b.addEventListener('click', inst.close); });
  return inst;
}

/* 确认框：危险操作为红色确认钮；resolve(true)=确认 */
function confirmDlg(o) {
  o = o || {};
  return new Promise(function (resolve) {
    var html = '<div class="m-head"><span class="m-title">' + esc(o.title || '操作确认') + '</span></div>'
      + '<div class="c-text">' + esc(o.text || '') + '</div>'
      + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
      + '<button class="btn-primary' + (o.danger ? ' danger' : '') + '" data-a="yes">' + esc(o.okText || '确定') + '</button></div>';
    var inst = createModal(html, { width: 400, onClose: function () { resolve(false); } });
    $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
    $('[data-a="yes"]', inst.box).addEventListener('click', function () { resolve(true); inst.close(); });
  });
}

/* ================= API 封装 ================= */
function goLogin() { window.location.href = 'login.html'; }

/* /api/wl 业务接口：信封 {code,message,data}；本地代理异常时为 {ok:false,error} */
async function api(path, opts) {
  opts = opts || {};
  var init = { method: opts.method || 'GET', headers: {}, credentials: 'same-origin' };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  var res;
  try { res = await fetch(path, init); } catch (e) { throw new Error('网络异常，请检查连接'); }
  if (res.status === 401) { goLogin(); throw new Error('登录已过期'); }
  var json = null;
  try { json = await res.json(); } catch (e) { /* 非 JSON 响应 */ }
  if (!json || json.code !== 0) {
    throw new Error((json && (json.message || json.error)) || ('请求失败（' + res.status + '）'));
  }
  return json.data;
}
var wl = {
  meta: function () { return api('api/wl/meta'); },
  logs: function (date, scope) { return api('api/wl/logs?date=' + date + '&scope=' + scope); },
  dayStatus: function (month, scope) { return api('api/wl/day-status?month=' + month + '&scope=' + scope); },
  createLog: function (b) { return api('api/wl/logs', { method: 'POST', body: b }); },
  updateLog: function (id, b) { return api('api/wl/logs/' + id, { method: 'PUT', body: b }); },
  delLog: function (id) { return api('api/wl/logs/' + id, { method: 'DELETE' }); },
  check: function (logId, mid) { return api('api/wl/logs/' + logId + '/members/' + mid + '/check', { method: 'PUT' }); },
  photos: function (from, to) { return api('api/wl/photos?from=' + from + '&to=' + to); },
  report: function (from, to, scope) { return api('api/wl/report?from=' + from + '&to=' + to + '&scope=' + scope); },
  geo: function (lng, lat) { return api('api/wl/geo?lng=' + encodeURIComponent(lng) + '&lat=' + encodeURIComponent(lat)); },
  uploadPhoto: function (logId, b) { return api('api/wl/logs/' + logId + '/photos', { method: 'POST', body: b }); },
  reverify: function (pid) { return api('api/wl/photos/' + pid + '/verify', { method: 'POST' }); },
  photoMembers: function (pid, names) { return api('api/wl/photos/' + pid + '/members', { method: 'PUT', body: { members: names } }); },
  delPhoto: function (pid) { return api('api/wl/photos/' + pid, { method: 'DELETE' }); },
  adminList: function (seg) { return api('api/wl/admin/' + seg); },
  adminAdd: function (seg, name) { return api('api/wl/admin/' + seg, { method: 'POST', body: { name: name } }); },
  adminPut: function (seg, id, b) { return api('api/wl/admin/' + seg + '/' + id, { method: 'PUT', body: b }); }
};

/* ================= 全局状态 ================= */
var state = {
  user: null,                                   // {id,username,nickname,role}
  meta: null, metaPromise: null,                // {vehicles,destinations,members}
  board: { date: todayStr(), scope: 'all', entries: [], timer: 0 },
  cal: { y: 0, m: 0 },                          // 日历弹层当前月（m 为 0 起）
  calCache: {},                                 // day-status 缓存：scope|YYYY-MM → map
  lib: { from: '', to: '', mine: false, list: [], sel: {}, loaded: false },
  rep: { from: '', to: '', mine: false },
  adm: { seg: 'vehicles', list: [], kw: '', loaded: false }
};

/* ================= 导航与当前用户 ================= */
function initNav() {
  var pages = { board: $('#page-board'), photos: $('#page-photos'), report: $('#page-report'), data: $('#page-data') };
  var underline = $('#navUnderline');
  function move(b) { underline.style.left = b.offsetLeft + 'px'; underline.style.width = b.offsetWidth + 'px'; }
  $$('.nav-btn').forEach(function (b) {
    b.addEventListener('click', function () {
      if (b.classList.contains('on')) return;
      $$('.nav-btn').forEach(function (x) { x.classList.remove('on'); });
      b.classList.add('on');
      move(b);
      Object.keys(pages).forEach(function (k) { pages[k].classList.remove('active'); });
      var pg = pages[b.dataset.page];
      void pg.offsetWidth; // 强制回流，重放进入动画
      pg.classList.add('active');
      clearPending();      // 切板块清除 pending 轮询
      placeAllSegs();      // 隐藏页中的分段开关药丸重定位
      onPageEnter(b.dataset.page);
    });
  });
  requestAnimationFrame(function () { move($('.nav-btn.on')); });
  window.addEventListener('resize', function () { move($('.nav-btn.on')); });
}
function onPageEnter(p) {
  if (p === 'photos' && !state.lib.loaded) loadLibrary();
  /* 报告每次进入都重查（与小程序口径一致）：看板侧打卡/重验等操作后切回来即为最新 */
  if (p === 'report') loadReport();
  if (p === 'data' && !state.adm.loaded) loadAdminSeg();
}
function renderUser() {
  var u = state.user;
  $('#userName').textContent = u.nickname || u.username;
  $('#userAvatar').textContent = (u.nickname || u.username || '·').charAt(0);
  if (u.role !== 'admin') $('#navData').style.display = 'none'; // 数据管理仅 admin 可见
  $('#quitBtn').addEventListener('click', function () {
    fetch('api/logout', { method: 'POST', credentials: 'same-origin' })
      .catch(function () { })
      .then(function () { goLogin(); });
  });
}

/* 药丸分段开关（沿用 B 稿滑动动效）；页面从隐藏变为可见时需重定位 */
var segPlacers = [];
function placeAllSegs() {
  segPlacers.forEach(function (fn) { fn(); });
}
function initSeg(sel, onChange) {
  var seg = $(sel);
  if (!seg) return;
  var pill = $('.pill', seg), btns = $$('button', seg);
  function place(b) { if (!b) return; pill.style.left = b.offsetLeft + 'px'; pill.style.width = b.offsetWidth + 'px'; }
  function placeOn() { place($('button.on', seg)); }
  segPlacers.push(placeOn);
  btns.forEach(function (b) {
    b.addEventListener('click', function () {
      if (b.classList.contains('on')) return;
      btns.forEach(function (x) { x.classList.remove('on'); });
      b.classList.add('on');
      place(b);
      if (onChange) onChange(b.dataset.v);
    });
  });
  requestAnimationFrame(placeOn);
  window.addEventListener('resize', placeOn);
}

/* ================= 板块一：日志看板 ================= */
function findEntry(eid) {
  var r = null;
  state.board.entries.forEach(function (e) { if (e.id === eid) r = e; });
  return r;
}

function setDateLabel() {
  var s = state.board.date;
  var cn = fmtDateCN(s).split(' ');
  $('#dMain').textContent = cn[0];
  $('#dSub').textContent = cn[1];
  $('#todayTag').style.display = (s === todayStr()) ? '' : 'none';
}

/* 加载当日日志；quiet=true 时保留现有内容（轮询/局部操作后刷新） */
async function loadLogs(opts) {
  opts = opts || {};
  clearPending();
  if (!opts.quiet) renderSkeleton();
  try {
    var data = await wl.logs(state.board.date, state.board.scope);
    state.board.entries = (data && data.list) || [];
    renderCards();
    renderStats();
    schedulePending();
    lbLiveSync(); // 联动已打开的照片预览（验证回写/人名修改/删除）
    if (!opts.poll) loadCalMonth(true); // 非轮询刷新时强刷日历着色（打卡/照片状态可能已变）
  } catch (e) {
    toast(e.message, { type: 'err' });
    if (!opts.quiet) state.board.entries = []; // 非静默加载失败时清空，避免残留其他日期卡片
    renderCards();
  }
}

function renderSkeleton() {
  var one = '<div class="log-card skel"><div class="sk-l" style="width:42%"></div>'
    + '<div class="sk-l" style="width:86%"></div>'
    + '<div class="sk-row"><i></i><i></i><i></i></div></div>';
  $('#cardFlow').innerHTML = one + one;
}

function emptyHTML(t, sub) {
  return '<div class="empty">' + I.empty + '<div class="empty-t">' + esc(t) + '</div>'
    + (sub ? '<div class="empty-s">' + esc(sub) + '</div>' : '') + '</div>';
}

/* 照片验证状态角标 */
function photoStatus(p) {
  if (p.verify_status === 'pending') return { cls: 'ing', txt: '验证中' };
  if (p.verify_status === 'passed') return { cls: 'pass', txt: '核验通过' };
  return { cls: 'fail', txt: '验证失败' };
}
/* 完成态逐项列「日期不符」「地点不符」 */
function photoMismatch(p) {
  var arr = [];
  if (p.date_ok === 0 || (p.date_ok == null && p.verify_status === 'date_mismatch')) arr.push('日期不符');
  if (p.dest_ok === 0 || (p.dest_ok == null && p.verify_status === 'dest_mismatch')) arr.push('地点不符');
  return arr;
}
function verifyText(p) {
  if (p.verify_status === 'pending') return '验证中';
  if (p.verify_status === 'passed') return '核验通过';
  var mis = photoMismatch(p);
  return '验证失败' + (mis.length ? '（' + mis.join('、') + '）' : '');
}

function photoHTML(e, p) {
  var st = photoStatus(p);
  var mis = photoMismatch(p);
  var html = '<figure class="photo" data-pid="' + p.id + '">'
    + '<img class="ph-img" src="' + esc(p.url) + '" loading="lazy" alt="水印照片">'
    + '<span class="ph-st ' + st.cls + '">' + st.txt + '</span>';
  if (mis.length) {
    html += '<span class="ph-miss">' + mis.map(function (m) { return '<span class="ph-mis">' + m + '</span>'; }).join('') + '</span>';
  }
  html += '<figcaption class="ph-info"><span>' + esc((p.members || []).join('、')) + '</span>'
    + '<span>' + esc(String(p.shot_time || '').slice(0, 16)) + '</span></figcaption></figure>';
  return html;
}

function cardHTML(e) {
  var noCar = e.vehicle_id == null;
  /* 验证角标：通过=绿 / 未通过=红 / 免验证=灰 */
  var badge = '';
  if (e.verify_passed === 'passed') badge = '<span class="vbadge pass">验证通过</span>';
  else if (e.verify_passed === 'failed') badge = '<span class="vbadge fail">未通过</span>';
  else if (e.verify_passed === 'exempt') badge = '<span class="vbadge gray">免验证</span>';

  var html = '<article class="log-card anim-in" data-eid="' + e.id + '">'
    + '<div class="lc-head">'
    + '<span class="plate' + (noCar ? ' nocar' : '') + '">' + esc(noCar ? '未出车' : e.plate_no) + '</span>'
    + '<span class="dest">' + esc(noCar ? '站内作业' : (e.destination_name || '—')) + '</span>'
    + badge
    + '<span class="lc-ops">'
    + '<button class="mini" data-act="edit">改派车</button>'
    + '<button class="mini danger" data-act="del">删除</button>'
    + '</span></div>';

  /* 未通过原因逐行 */
  if (e.verify_passed === 'failed' && e.verify_reasons && e.verify_reasons.length) {
    html += '<div class="fail-list">' + e.verify_reasons.map(function (r) {
      return '<div class="fail-row">' + I.warn + '<span>' + esc(r) + '</span></div>';
    }).join('') + '</div>';
  }

  /* 用车人 chips：点亮=已打卡 */
  if (e.members && e.members.length) {
    var ms = e.members.slice().sort(function (a, b) { return (a.sort || 0) - (b.sort || 0); });
    html += '<div class="chips">' + ms.map(function (m) {
      return '<button class="chip' + (m.checked ? ' on' : '') + '" data-mid="' + m.id + '"><i class="dot"></i>' + esc(m.name) + '</button>';
    }).join('') + '</div>';
  }

  /* 巡视内容块：空时显示占位 */
  html += '<div class="patrol" data-act="patrol" title="点击编辑巡视内容"><span class="tag">巡视内容</span>'
    + (e.patrol_content ? esc(e.patrol_content) : '<span class="patrol-ph">点击填写巡视内容</span>')
    + '</div>';

  /* 水印照片网格 + 添加入口 */
  var canAdd = !!(e.members && e.members.length);
  html += '<div class="photos">'
    + (e.photos || []).map(function (p) { return photoHTML(e, p); }).join('')
    + '<div class="add-photo' + (canAdd ? '' : ' dis') + '" data-act="add" title="' + (canAdd ? '' : '请先添加用车人') + '">'
    + I.plus + '添加照片</div>'
    + '</div></article>';
  return html;
}

function renderCards() {
  var flow = $('#cardFlow');
  var es = state.board.entries;
  if (!es.length) {
    flow.innerHTML = emptyHTML('当日暂无出工日志', '点击右上角「新建日志」开始记录');
    return;
  }
  flow.innerHTML = es.map(cardHTML).join('');
  /* 卡片入场：blur(6px)→0 stagger */
  $$('#cardFlow .anim-in').forEach(function (el, i) {
    el.classList.add('pre');
    setTimeout(function () { el.classList.add('in'); }, RM ? 0 : 60 + i * 80);
  });
}

/* 今日概览：数字 count-up + 进度条 */
function countUp(el, target) {
  if (RM) { el.textContent = target; return; }
  var dur = 700, t0 = performance.now();
  (function tick(now) {
    var p = Math.min(1, (now - t0) / dur), e2 = 1 - Math.pow(1 - p, 3);
    el.textContent = Math.round(target * e2);
    if (p < 1) requestAnimationFrame(tick);
  })(t0);
}
function renderStats() {
  var cars = 0, ckT = 0, ckY = 0, phN = 0, psX = 0, psY = 0;
  state.board.entries.forEach(function (e) {
    if (e.vehicle_id != null) cars++;
    (e.members || []).forEach(function (m) { ckY++; if (m.checked) ckT++; });
    (e.photos || []).forEach(function (p) {
      phN++;
      if (p.verify_status !== 'pending') { psY++; if (p.verify_status === 'passed') psX++; }
    });
  });
  countUp($('#stCar'), cars);
  countUp($('#stCk'), ckT); $('#stCkY').textContent = '/' + ckY;
  countUp($('#stPh'), phN);
  countUp($('#stPs'), psX); $('#stPsY').textContent = '/' + psY;
  $('#pr1').style.width = ckY ? (ckT / ckY * 100) + '%' : '0';
  $('#pr2').style.width = psY ? (psX / psY * 100) + '%' : '0';
  $('#pr1t').textContent = ckT + '/' + ckY;
  $('#pr2t').textContent = psX + '/' + psY;
}

/* pending 轮询：有验证中照片时 3s 后自动重拉（poll 标记：不触发日历着色强刷） */
function clearPending() {
  if (state.board.timer) { clearTimeout(state.board.timer); state.board.timer = 0; }
}
function schedulePending() {
  clearPending();
  var has = state.board.entries.some(function (e) {
    return (e.photos || []).some(function (p) { return p.verify_status === 'pending'; });
  });
  if (has) {
    state.board.timer = setTimeout(function () { loadLogs({ quiet: true, poll: true }); }, 3000);
  }
}

/* 日期切换：卡片流平移滑出/滑入 */
function slideFlow(dir, mid) {
  var flow = $('#cardFlow');
  if (RM) { mid(); return; }
  flow.classList.add(dir > 0 ? 'out-left' : 'out-right');
  setTimeout(function () {
    mid();
    flow.classList.remove('out-left', 'out-right');
    flow.style.transition = 'none';
    flow.style.transform = 'translateX(' + (dir > 0 ? 40 : -40) + 'px)';
    flow.style.opacity = '0';
    void flow.offsetWidth;
    flow.style.transition = '';
    flow.style.transform = '';
    flow.style.opacity = '';
  }, 380);
}
/* 日历显示月份与看板日期对齐（跨月切换时日历随动） */
function syncCalMonth(d) {
  state.cal.y = d.getFullYear();
  state.cal.m = d.getMonth();
}
function shiftDay(delta) {
  var d = parseDate(state.board.date);
  d.setDate(d.getDate() + delta);
  var nd = fmtDate(d);
  slideFlow(delta, function () {
    state.board.date = nd;
    syncCalMonth(d);
    setDateLabel();
    loadLogs();
  });
}
function gotoDate(nd) {
  if (nd === state.board.date) return;
  var dir = nd > state.board.date ? 1 : -1;
  slideFlow(dir, function () {
    state.board.date = nd;
    syncCalMonth(parseDate(nd));
    setDateLabel();
    loadLogs();
  });
}

/* ---------- 左侧常驻日历卡 ---------- */
function loadCalMonth(force) {
  var ym = state.cal.y + '-' + pad(state.cal.m + 1);
  var key = state.board.scope + '|' + ym;
  renderCal(); // 先按现有缓存渲染（无缓存即无着色），数据回来后补着色
  if (!force && state.calCache[key] !== undefined) return;
  wl.dayStatus(ym, state.board.scope)
    .then(function (d) { state.calCache[key] = (d && d.map) || {}; renderCal(); })
    .catch(function () { /* 着色失败不阻塞选日，静默跳过 */ });
}
function shiftCalMonth(delta) {
  var m = state.cal.m + delta;
  state.cal.m = (m + 12) % 12;
  state.cal.y += (m < 0 ? -1 : (m > 11 ? 1 : 0));
  loadCalMonth();
}
function renderCal() {
  var y = state.cal.y, m = state.cal.m;
  var ym = y + '-' + pad(m + 1);
  var map = state.calCache[state.board.scope + '|' + ym] || {};
  var first = new Date(y, m, 1);
  var off = (first.getDay() + 6) % 7;            // 周一为一周开始
  var days = new Date(y, m + 1, 0).getDate();
  var prevDays = new Date(y, m, 0).getDate();
  var td = todayStr();

  $('#calYm').textContent = y + ' 年 ' + (m + 1) + ' 月';
  var html = '<div class="cal-grid">'
    + '<span class="dow">一</span><span class="dow">二</span><span class="dow">三</span><span class="dow">四</span>'
    + '<span class="dow">五</span><span class="dow">六</span><span class="dow">日</span>';
  var i, ds, st;
  for (i = off - 1; i >= 0; i--) html += '<span class="day dim">' + (prevDays - i) + '</span>';
  for (i = 1; i <= days; i++) {
    ds = ym + '-' + pad(i);
    st = map[ds];
    html += '<span class="day' + (ds === td ? ' today' : '') + (ds === state.board.date ? ' sel' : '') + '" data-d="' + ds + '">'
      + i + (st ? '<i class="dm ' + (st === 'passed' ? 'g' : 'r') + '"></i>' : '') + '</span>';
  }
  var tail = (7 - (off + days) % 7) % 7;
  for (i = 1; i <= tail; i++) html += '<span class="day dim">' + i + '</span>';
  html += '</div><div class="cal-legend"><span><i class="g"></i>全部通过</span><span><i class="r"></i>有未通过</span></div>';
  $('#calBody').innerHTML = html;
}
function initCalendar() {
  /* 初始月份对齐看板日期并先渲染；着色数据由 loadLogs 后的 loadCalMonth(true) 拉取 */
  syncCalMonth(parseDate(state.board.date));
  var n = new Date();
  $('#calToday').textContent = '今天 ' + (n.getMonth() + 1) + '/' + n.getDate();
  renderCal();
  $('#calCard').addEventListener('click', function (e) {
    var nav = e.target.closest('[data-cal]');
    if (nav) { shiftCalMonth(+nav.dataset.cal); return; }
    var day = e.target.closest('.day[data-d]');
    if (day) gotoDate(day.dataset.d);
  });
  $('#calToday').addEventListener('click', function (e) {
    e.stopPropagation();
    var t = todayStr();
    syncCalMonth(parseDate(t));
    loadCalMonth(true);
    if (state.board.date !== t) gotoDate(t);
  });
}

/* ---------- 看板事件绑定 ---------- */
function initBoard() {
  setDateLabel();
  $('#prevDay').addEventListener('click', function () { shiftDay(-1); });
  $('#nextDay').addEventListener('click', function () { shiftDay(1); });
  $('#backToday').addEventListener('click', function () {
    if (state.board.date === todayStr()) { toast('已是今天'); return; }
    gotoDate(todayStr());
  });
  $('#newLogBtn').addEventListener('click', function () { openLogModal(null); });
  $('#quickNew').addEventListener('click', function () { openLogModal(null); });
  $('#quickPhotos').addEventListener('click', function () { $('.nav-btn[data-page="photos"]').click(); });
  initCalendar();
  /* 全部 / 仅看我：切换后重拉并清空日历缓存 */
  initSeg('#segBoard', function (v) {
    state.board.scope = v;
    state.calCache = {};
    var f = $('#cardFlow');
    f.classList.remove('flash'); void f.offsetWidth; f.classList.add('flash');
    loadLogs();
  });

  /* 卡片流点击委派 */
  $('#cardFlow').addEventListener('click', function (ev) {
    var t = ev.target;
    /* 用车人打卡切换 */
    var chip = t.closest('.chip[data-mid]');
    if (chip) { onCheck(chip); return; }
    /* 卡片级操作 */
    var act = t.closest('[data-act]');
    if (act) {
      var card = act.closest('.log-card');
      var entry = findEntry(+card.dataset.eid);
      if (!entry) return;
      var a = act.dataset.act;
      if (a === 'edit') openLogModal(entry);
      else if (a === 'del') onDelLog(entry);
      else if (a === 'patrol') openPatrolModal(entry);
      else if (a === 'add') {
        if (act.classList.contains('dis')) { toast('请先在该日志中添加用车人'); return; }
        startAddPhoto(entry);
      }
      return;
    }
    /* 点击照片本体 = 预览（改人名/重新验证/删除在预览侧栏内操作） */
    var fig = t.closest('.photo[data-pid]');
    if (fig) {
      var cd = fig.closest('.log-card');
      var en = findEntry(+cd.dataset.eid);
      if (en) openPhotoLightbox(en, +fig.dataset.pid);
    }
  });
}

/* 用车人 chips：点亮切换打卡，失败回滚 */
async function onCheck(chip) {
  var card = chip.closest('.log-card');
  var eid = +card.dataset.eid, mid = +chip.dataset.mid;
  var now = chip.classList.toggle('on');
  try {
    var d = await wl.check(eid, mid);
    var entry = findEntry(eid);
    if (entry) {
      entry.members.forEach(function (m) { if (m.id === mid) m.checked = d.checked; });
    }
    renderStats();
    loadLogs({ quiet: true }); // 静默同步角标与未通过原因
  } catch (e) {
    chip.classList.toggle('on', !now);
    toast(e.message, { type: 'err' });
  }
}

/* 删除日志：确认框提示照片一并删除 */
function onDelLog(entry) {
  confirmDlg({
    title: '删除日志',
    text: '删除后该日志及其全部水印照片将一并删除，且不可恢复。是否继续？',
    danger: true, okText: '删除'
  }).then(function (ok) {
    if (!ok) return;
    wl.delLog(entry.id)
      .then(function () { toast('已删除'); loadLogs({ quiet: true }); })
      .catch(function (e) { toast(e.message, { type: 'err' }); });
  });
}

/* 照片操作实现（预览侧栏内调用）：改人名 / 重新验证 / 删除 */
function onReverify(btn, p) {
  if (btn.disabled) return;
  btn.disabled = true;
  wl.reverify(p.id)
    .then(function () { toast('已重新提交验证'); loadLogs({ quiet: true }); })
    .catch(function (e) { btn.disabled = false; toast(e.message, { type: 'err' }); });
}
function onDelPhoto(p) {
  confirmDlg({ title: '删除照片', text: '确定删除该水印照片？删除后不可恢复。', danger: true, okText: '删除' })
    .then(function (ok) {
      if (!ok) return;
      wl.delPhoto(p.id)
        .then(function () { toast('已删除'); loadLogs({ quiet: true }); })
        .catch(function (e) { toast(e.message, { type: 'err' }); });
    });
}

/* ================= 新建 / 改派车模态 ================= */
/* meta 懒加载：首次打开模态时拉取，数据管理变更后置空重拉 */
function ensureMeta() {
  if (state.meta) return Promise.resolve(state.meta);
  if (state.metaPromise) return state.metaPromise;
  state.metaPromise = wl.meta().then(function (d) {
    state.meta = d || { vehicles: [], destinations: [], members: [] };
    state.metaPromise = null;
    return state.meta;
  }).catch(function (e) {
    state.metaPromise = null;
    throw e;
  });
  return state.metaPromise;
}

/* 可搜索下拉组合框：input + 过滤浮层，选中值存 box.dataset.id */
function makeCombo(box, items, onPick) {
  var inp = $('input', box), opts = $('.opts', box);
  function current() {
    var r = null;
    items.forEach(function (it) { if (String(it.id) === String(box.dataset.id)) r = it; });
    return r;
  }
  function render(kw) {
    var list = items.filter(function (it) { return !kw || it.label.indexOf(kw) >= 0; });
    opts.innerHTML = list.length
      ? list.map(function (it) {
        return '<div class="opt' + (String(box.dataset.id) === String(it.id) ? ' sel' : '') + '" data-id="' + it.id + '">' + esc(it.label) + '</div>';
      }).join('')
      : '<div class="opt none">无匹配项</div>';
  }
  inp.addEventListener('focus', function () { if (inp.disabled) return; box.classList.add('open'); render(''); });
  inp.addEventListener('input', function () { delete box.dataset.id; box.classList.add('open'); render(inp.value.trim()); });
  inp.addEventListener('blur', function () {
    setTimeout(function () {
      box.classList.remove('open');
      var cur = current();
      inp.value = cur ? cur.label : ''; // 失焦回显已选项，未选则清空
      if (!cur) delete box.dataset.id;
    }, 160);
  });
  opts.addEventListener('mousedown', function (e) {
    var o = e.target.closest('.opt');
    if (!o || o.classList.contains('none')) return;
    box.dataset.id = o.dataset.id;
    inp.value = o.textContent;
    box.classList.remove('open');
    if (onPick) onPick(o.dataset.id);
  });
  return {
    set: function (id) {
      var it = null;
      items.forEach(function (x) { if (String(x.id) === String(id)) it = x; });
      if (it) { box.dataset.id = String(id); inp.value = it.label; }
    },
    clear: function () { delete box.dataset.id; inp.value = ''; },
    disable: function (f) {
      inp.disabled = f;
      if (f) { box.classList.remove('open'); this.clear(); }
    }
  };
}

/* 新建（entry=null）/ 改派车（entry 为日志对象）共用模态 */
function openLogModal(entry) {
  ensureMeta().then(function (meta) {
    var isEdit = !!entry;
    var carItems = (meta.vehicles || []).map(function (v) { return { id: v.id, label: v.plate_no }; });
    carItems.push({ id: -1, label: '未出车（站内作业）' }); // 固定项
    var destItems = (meta.destinations || []).map(function (d) { return { id: d.id, label: d.name }; });
    var members = (meta.members || []).slice().sort(function (a, b) { return (a.sort || 0) - (b.sort || 0); });

    var html = mHead(isEdit ? '改派车' : '新建日志')
      + '<div class="field"><label>日期</label><input class="text-input" value="' + state.board.date + '" readonly></div>'
      + '<div class="field"><label>派车情况</label><div class="select" id="mCar">'
      + '<input class="text-input" placeholder="搜索或选择车牌" autocomplete="off"><div class="opts"></div></div></div>'
      + '<div class="field"><label>目的地</label><div class="select" id="mDest">'
      + '<input class="text-input" placeholder="搜索或选择目的地" autocomplete="off"><div class="opts"></div></div></div>'
      + '<div class="field" id="mMemWrap"><label>用车人</label><div class="chips m-chips" id="mMemChips">'
      + members.map(function (m) { return '<button class="chip" type="button" data-mid="' + m.id + '"><i class="dot"></i>' + esc(m.name) + '</button>'; }).join('')
      + '</div></div>'
      + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
      + '<button class="btn-primary" data-a="yes">保存</button></div>';
    var inst = createModal(html, { width: 520 });

    var carBox = $('#mCar', inst.box), destBox = $('#mDest', inst.box), memWrap = $('#mMemWrap', inst.box);
    var noCar = false;
    var destCombo = makeCombo(destBox, destItems, null);
    makeCombo(carBox, carItems, function () {
      /* 选「未出车」：目的地禁用清空、用车人隐藏 */
      noCar = String(carBox.dataset.id) === '-1';
      destCombo.disable(noCar);
      memWrap.style.display = noCar ? 'none' : '';
      if (noCar) $$('.chip', memWrap).forEach(function (c) { c.classList.remove('on'); });
    });
    $$('.chip', memWrap).forEach(function (c) {
      c.addEventListener('click', function () { c.classList.toggle('on'); });
    });

    /* 改派车：预填当前值 */
    if (isEdit) {
      if (entry.vehicle_id == null) {
        carBox.dataset.id = '-1';
        $('input', carBox).value = '未出车（站内作业）';
        noCar = true;
        destCombo.disable(true);
        memWrap.style.display = 'none';
      } else {
        /* vehicles 列表不含已停用车辆时的兜底：直接按 id 预置显示车牌 */
        var hit = false;
        (meta.vehicles || []).forEach(function (v) { if (v.id === entry.vehicle_id) hit = true; });
        if (hit) { carBox.dataset.id = String(entry.vehicle_id); $('input', carBox).value = entry.plate_no || ''; }
        destCombo.set(entry.destination_id);
        var picked = {};
        (entry.members || []).forEach(function (m) { picked[m.member_id] = 1; });
        $$('.chip', memWrap).forEach(function (c) { c.classList.toggle('on', !!picked[+c.dataset.mid]); });
      }
    }

    var saveBtn = $('[data-a="yes"]', inst.box);
    $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
    saveBtn.addEventListener('click', function () {
      if (saveBtn.disabled) return; // 防连点
      if (carBox.dataset.id === undefined) { toast('请选择派车情况'); shake(carBox); return; }
      var body;
      if (String(carBox.dataset.id) === '-1') {
        body = { log_date: state.board.date, patrol_content: isEdit ? (entry.patrol_content || '') : '', vehicle_id: null, destination_id: null, member_ids: [] };
      } else {
        if (destBox.dataset.id === undefined) { toast('请选择目的地'); shake(destBox); return; }
        var mids = $$('.chip.on', memWrap).map(function (c) { return +c.dataset.mid; });
        if (!mids.length) { toast('请选择用车人'); shake(memWrap); return; }
        body = {
          log_date: state.board.date,
          patrol_content: isEdit ? (entry.patrol_content || '') : '',
          vehicle_id: +carBox.dataset.id,
          destination_id: +destBox.dataset.id,
          member_ids: mids
        };
      }
      var doSave = function () {
        saveBtn.disabled = true;
        saveBtn.textContent = '保存中…';
        var req = isEdit ? wl.updateLog(entry.id, body) : wl.createLog(body);
        req.then(function () {
          inst.close();
          toast('已保存');
          loadLogs({ quiet: true });
        }).catch(function (e) {
          saveBtn.disabled = false;
          saveBtn.textContent = '保存';
          toast(e.message, { type: 'err' });
        });
      };
      if (isEdit) {
        confirmDlg({ title: '确认修改', text: '请确保内网派车单同步修改！', danger: true, okText: '确认修改' })
          .then(function (ok) { if (ok) doSave(); });
      } else {
        doSave();
      }
    });
  }).catch(function (e) { toast(e.message, { type: 'err' }); });
}

/* ================= 巡视内容编辑模态 ================= */
var QUICK_WORDS = ['110kV', '220kV', 'Ⅰ', 'Ⅱ', '线巡视'];
function openPatrolModal(entry) {
  var html = mHead('巡视内容')
    + '<div class="field"><textarea class="text-input" id="pTa" maxlength="500" placeholder="填写本日巡视内容（≤500 字）"></textarea>'
    + '<div class="ta-count"><span id="pCnt">0</span>/500</div>'
    + '<div class="q-chips">' + QUICK_WORDS.map(function (w) { return '<button class="q-chip" data-w="' + esc(w) + '">' + esc(w) + '</button>'; }).join('') + '</div></div>'
    + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
    + '<button class="btn-primary" data-a="yes">保存</button></div>';
  var inst = createModal(html, { width: 520 });
  var ta = $('#pTa', inst.box), cnt = $('#pCnt', inst.box);
  ta.value = entry.patrol_content || '';
  cnt.textContent = ta.value.length;
  ta.addEventListener('input', function () { cnt.textContent = ta.value.length; });
  $$('.q-chip', inst.box).forEach(function (c) {
    c.addEventListener('click', function () {
      /* 光标处插入快捷词，超长截断 */
      var w = c.dataset.w;
      var s = ta.selectionStart != null ? ta.selectionStart : ta.value.length;
      var nv = (ta.value.slice(0, s) + w + ta.value.slice(s)).slice(0, 500);
      ta.value = nv;
      cnt.textContent = nv.length;
      ta.focus();
    });
  });
  $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
  var saveBtn = $('[data-a="yes"]', inst.box);
  saveBtn.addEventListener('click', function () {
    if (saveBtn.disabled) return;
    /* 全量替换：其余字段取该卡当前值带上 */
    var noCar = entry.vehicle_id == null;
    var body = {
      log_date: entry.log_date,
      patrol_content: ta.value.trim(),
      vehicle_id: entry.vehicle_id,
      destination_id: noCar ? null : entry.destination_id,
      member_ids: noCar ? [] : (entry.members || []).map(function (m) { return m.member_id; })
    };
    saveBtn.disabled = true;
    saveBtn.textContent = '保存中…';
    wl.updateLog(entry.id, body).then(function () {
      inst.close();
      toast('已保存');
      loadLogs({ quiet: true });
    }).catch(function (e) {
      saveBtn.disabled = false;
      saveBtn.textContent = '保存';
      toast(e.message, { type: 'err' });
    });
  });
}

/* ================= 添加照片：人名点亮弹层（两条链路共用前置） ================= */
/* 本卡已被其他照片占用的人名（每人限一张） */
function usedNames(entry, excludePhoto) {
  var s = {};
  (entry.photos || []).forEach(function (p) {
    if (excludePhoto && p.id === excludePhoto.id) return;
    (p.members || []).forEach(function (n) { s[n] = 1; });
  });
  return s;
}
function memberNames(entry) {
  return (entry.members || []).slice()
    .sort(function (a, b) { return (a.sort || 0) - (b.sort || 0); })
    .map(function (m) { return m.name; });
}
/* 人名点亮弹层：resolve(选中名字数组)，取消 resolve(null) */
function pickMembers(o) {
  return new Promise(function (resolve) {
    var sel = {};
    (o.selected || []).forEach(function (n) { sel[n] = 1; });
    var chips = o.candidates.map(function (n) {
      var dis = o.disabled[n] && !sel[n];
      return '<button class="chip' + (sel[n] ? ' on' : '') + (dis ? ' off' : '') + '" data-n="' + esc(n) + '"' + (dis ? ' disabled' : '') + '><i class="dot"></i>' + esc(n) + '</button>';
    }).join('');
    var html = mHead(o.title || '点亮人名')
      + '<div class="m-hint">每人限一张照片，已被本卡其他照片占用的人名不可选</div>'
      + '<div class="chips m-chips">' + chips + '</div>'
      + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
      + '<button class="btn-primary" data-a="yes">确定</button></div>';
    var inst = createModal(html, { width: 440, onClose: function () { resolve(null); } });
    var okBtn = $('[data-a="yes"]', inst.box);
    function refresh() {
      var n = Object.keys(sel).length;
      okBtn.disabled = !n;
      okBtn.textContent = n ? ('确定（' + n + ' 人）') : '确定';
    }
    $$('.chip', inst.box).forEach(function (c) {
      c.addEventListener('click', function () {
        var n = c.dataset.n;
        if (sel[n]) { delete sel[n]; c.classList.remove('on'); }
        else { sel[n] = 1; c.classList.add('on'); }
        refresh();
      });
    });
    refresh();
    $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
    okBtn.addEventListener('click', function () { resolve(Object.keys(sel)); inst.close(); });
  });
}

/* 改人名：复用人名点亮弹层 */
function openPhotoMembersModal(entry, photo) {
  var cands = memberNames(entry);
  (photo.members || []).forEach(function (n) { if (cands.indexOf(n) < 0) cands.push(n); });
  pickMembers({ title: '修改人名', candidates: cands, disabled: usedNames(entry, photo), selected: photo.members || [] })
    .then(function (names) {
      if (!names) return;
      wl.photoMembers(photo.id, names)
        .then(function () { toast('已保存'); loadLogs({ quiet: true }); })
        .catch(function (e) { toast(e.message, { type: 'err' }); });
    });
}

/* 添加照片入口：人名点亮 → 上传方式二选一 → 选文件 */
function startAddPhoto(entry) {
  pickMembers({ title: '点亮人名', candidates: memberNames(entry), disabled: usedNames(entry, null), selected: [] })
    .then(function (names) {
      if (!names) return;
      openUploadChoice(entry, names);
    });
}
function openUploadChoice(entry, names) {
  var html = mHead('添加照片')
    + '<div class="opt-cards">'
    + '<button class="opt-card" data-a="raw">' + I.wmImg + '<b>上传水印照片</b><span>选择已经带水印的照片，直接上传</span></button>'
    + '<button class="opt-card" data-a="mk">' + I.magic + '<b>照片添加水印</b><span>选择普通照片，裁切为 4:3 / 3:4 并填写水印信息</span></button>'
    + '</div>';
  var inst = createModal(html, { width: 520 });
  function pickAndRun(fn) {
    inst.close();
    pickFile().then(function (f) {
      if (!f) return;
      if (!/^image\/(jpeg|png)$/.test(f.type)) { toast('仅支持 JPEG / PNG 图片', { type: 'err' }); return; }
      fn(entry, names, f);
    });
  }
  $('[data-a="raw"]', inst.box).addEventListener('click', function () { pickAndRun(directUpload); });
  $('[data-a="mk"]', inst.box).addEventListener('click', function () { pickAndRun(watermarkFlow); });
}
function pickFile() {
  return new Promise(function (resolve) {
    var inp = $('#filePicker');
    inp.value = '';
    inp.onchange = function () { resolve(inp.files[0] || null); };
    inp.click();
  });
}

/* ================= 图片读取与导出 ================= */
/* 读图并自动应用 EXIF 方向（createImageBitmap from-image；兜底 new Image） */
function loadImageOriented(file) {
  function viaTag() {
    return new Promise(function (res, rej) {
      var url = URL.createObjectURL(file);
      var im = new Image();
      im.onload = function () { URL.revokeObjectURL(url); res(im); };
      im.onerror = function () { URL.revokeObjectURL(url); rej(new Error('图片读取失败')); };
      im.src = url;
    });
  }
  if (window.createImageBitmap) {
    return createImageBitmap(file, { imageOrientation: 'from-image' })
      .catch(function () { return createImageBitmap(file); })
      .catch(viaTag);
  }
  return viaTag();
}
function imgW(img) { return img.width || img.naturalWidth; }
function imgH(img) { return img.height || img.naturalHeight; }
/* 源区域导出：长边 ≤2560，jpeg 0.92，返回 dataURL */
function exportImage(img, sx, sy, sw, sh) {
  var k = Math.min(1, 2560 / Math.max(sw, sh));
  var cw = Math.max(1, Math.round(sw * k)), ch = Math.max(1, Math.round(sh * k));
  var c = document.createElement('canvas');
  c.width = cw; c.height = ch;
  c.getContext('2d').drawImage(img, sx, sy, sw, sh, 0, 0, cw, ch);
  return c.toDataURL('image/jpeg', 0.92);
}
/* ①直接上传：>8MB 先压到长边 2560 / jpeg 0.92 */
function fileToDataURL(file) {
  if (file.size > 8 * 1024 * 1024) {
    return loadImageOriented(file).then(function (img) {
      return exportImage(img, 0, 0, imgW(img), imgH(img));
    });
  }
  return new Promise(function (res, rej) {
    var r = new FileReader();
    r.onload = function () { res(r.result); };
    r.onerror = function () { rej(new Error('图片读取失败')); };
    r.readAsDataURL(file);
  });
}

/* 链路上传①：直接上传已有水印照 */
function directUpload(entry, names, file) {
  var t = toast('正在上传…', { sticky: true });
  fileToDataURL(file).then(function (dataUrl) {
    return wl.uploadPhoto(entry.id, { image: dataUrl, members: names });
  }).then(function () {
    untoast(t);
    toast('已上传，验证中');
    loadLogs({ quiet: true });
  }).catch(function (e) {
    untoast(t);
    toast(e.message, { type: 'err' });
  });
}

/* 链路上传②：选图 → EXIF 定向 → 比例判定 →（裁剪）→ 水印表单 */
function watermarkFlow(entry, names, file) {
  loadImageOriented(file).then(function (img) {
    var w = imgW(img), h = imgH(img);
    var r = w / h;
    var ok43 = Math.abs(r - 4 / 3) <= 0.02, ok34 = Math.abs(r - 3 / 4) <= 0.02;
    if (ok43 || ok34) {
      /* 已是 4:3 / 3:4：免裁，直接规范化导出 */
      openWmForm(entry, names, exportImage(img, 0, 0, w, h));
    } else {
      /* 横图锁 4:3、纵图锁 3:4 */
      openCropper(img, w >= h ? 4 : 3, w >= h ? 3 : 4).then(function (dataUrl) {
        if (dataUrl) openWmForm(entry, names, dataUrl);
      });
    }
  }).catch(function (e) { toast(e.message, { type: 'err' }); });
}

/* 裁剪模态：拖拽平移 + 滚轮/滑杆缩放；resolve(dataURL)，取消 resolve(null) */
function openCropper(img, rw, rh) {
  return new Promise(function (resolve) {
    var iw = imgW(img), ih = imgH(img);
    var fw, fh;
    if (rw >= rh) { fw = 520; fh = Math.round(520 * rh / rw); }
    else { fh = 480; fw = Math.round(480 * rw / rh); }
    var html = mHead('裁切照片（' + rw + ':' + rh + '）')
      + '<div class="crop-wrap" style="width:' + fw + 'px;height:' + fh + 'px"><canvas id="cropCv" width="' + fw + '" height="' + fh + '"></canvas></div>'
      + '<div class="crop-bar"><span>－</span><input type="range" id="cropZoom" min="100" max="500" value="100"><span>＋</span></div>'
      + '<div class="crop-tip">拖拽移动画面，滚轮或滑杆缩放</div>'
      + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
      + '<button class="btn-primary" data-a="yes">确定裁切</button></div>';
    var inst = createModal(html, { width: fw + 52, onClose: function () { resolve(null); } });
    var cv = $('#cropCv', inst.box), ctx = cv.getContext('2d');
    var base = Math.max(fw / iw, fh / ih); // 初始 cover
    var z = 1, scale = base, ox = (fw - iw * scale) / 2, oy = (fh - ih * scale) / 2;
    var zoomEl = $('#cropZoom', inst.box);
    function clamp() {
      ox = Math.min(0, Math.max(fw - iw * scale, ox));
      oy = Math.min(0, Math.max(fh - ih * scale, oy));
    }
    function draw() {
      ctx.fillStyle = '#0E0F12';
      ctx.fillRect(0, 0, fw, fh);
      ctx.drawImage(img, ox, oy, iw * scale, ih * scale);
    }
    function setZoom(nz) {
      nz = Math.min(5, Math.max(1, nz));
      /* 以画面中心为锚点缩放 */
      var cx = fw / 2, cy = fh / 2;
      var ix = (cx - ox) / scale, iy = (cy - oy) / scale;
      z = nz; scale = base * z;
      ox = cx - ix * scale; oy = cy - iy * scale;
      clamp(); draw();
      zoomEl.value = Math.round(z * 100);
    }
    clamp(); draw();
    var drag = null;
    cv.addEventListener('pointerdown', function (e) {
      drag = { x: e.clientX - ox, y: e.clientY - oy };
      cv.setPointerCapture(e.pointerId);
    });
    cv.addEventListener('pointermove', function (e) {
      if (!drag) return;
      ox = e.clientX - drag.x; oy = e.clientY - drag.y;
      clamp(); draw();
    });
    cv.addEventListener('pointerup', function () { drag = null; });
    cv.addEventListener('pointercancel', function () { drag = null; });
    cv.addEventListener('wheel', function (e) {
      e.preventDefault();
      setZoom(z * (e.deltaY < 0 ? 1.1 : 0.9));
    }, { passive: false });
    zoomEl.addEventListener('input', function () { setZoom(+zoomEl.value / 100); });
    $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
    $('[data-a="yes"]', inst.box).addEventListener('click', function () {
      var sx = -ox / scale, sy = -oy / scale, sw = fw / scale, sh = fh / scale;
      resolve(exportImage(img, sx, sy, sw, sh));
      inst.close();
    });
  });
}

/* ================= 水印字段表单模态 ================= */
var CODE_CH = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'; // 14 位防伪码字符集
function genCode() {
  var s = '';
  for (var i = 0; i < 14; i++) s += CODE_CH[Math.floor(Math.random() * CODE_CH.length)];
  return s;
}
function parseCoord(v) {
  var n = parseFloat(String(v == null ? '' : v));
  return isNaN(n) ? null : n;
}
/* 经纬度随机偏移 ≤400m */
function offsetCoord(lng, lat) {
  var ang = Math.random() * 2 * Math.PI;
  var dist = Math.random() * 400;
  var dLat = dist * Math.cos(ang) / 111320;
  var cosLat = Math.cos(lat * Math.PI / 180);
  var dLng = dist * Math.sin(ang) / (111320 * (cosLat || 1));
  return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
}
/* 预填拍摄时间：保留历史日期，时分随机 10:00-12:00 且不与原值相同 */
function randomTimeNear(shotTime, logDate) {
  var datePart = String(logDate || '').replace(/-/g, '.');
  var m = /^(\d{4}\.\d{2}\.\d{2}) (\d{2}):(\d{2})$/.exec(shotTime || '');
  if (m) datePart = m[1];
  var orig = m ? (m[2] + ':' + m[3]) : '';
  var t;
  do {
    t = pad(10 + Math.floor(Math.random() * 2)) + ':' + pad(Math.floor(Math.random() * 60));
  } while (t === orig);
  return datePart + ' ' + t;
}
/* 纯数字经纬度提交时补 °E / °N 后缀 */
function withSuffix(v, suf) { return /^\d+(\.\d+)?$/.test(v) ? v + suf : v; }

function openWmForm(entry, names, dataUrl) {
  /* 预填：本卡最近一张带施工内容的历史照片 */
  var hist = null;
  for (var i = entry.photos.length - 1; i >= 0; i--) {
    if (entry.photos[i].work_content) { hist = entry.photos[i]; break; }
  }
  var pre = { content: '', time: fmtWmTime(new Date()), weather: '', location: '', lng: '', lat: '' };
  if (hist) {
    pre.content = hist.work_content || '';
    pre.weather = hist.weather || '';
    pre.location = hist.location || '';
    var hlng = parseCoord(hist.lng), hlat = parseCoord(hist.lat);
    if (hlng !== null && hlat !== null) {
      var off = offsetCoord(hlng, hlat);
      pre.lng = off.lng; pre.lat = off.lat;
    }
    pre.time = randomTimeNear(hist.shot_time, entry.log_date);
  }

  var html = mHead('添加水印')
    + '<img class="wm-prev" src="' + dataUrl + '" alt="待上传照片">'
    + '<div class="field"><label>施工内容</label>'
    + '<textarea class="text-input" id="wContent" maxlength="500" placeholder="如：110kV 前界线Ⅰ线巡视"></textarea>'
    + '<div class="ta-count"><span id="wCnt">0</span>/500</div>'
    + '<div class="q-chips">' + QUICK_WORDS.map(function (w) { return '<button class="q-chip" data-w="' + esc(w) + '">' + esc(w) + '</button>'; }).join('') + '</div></div>'
    + '<div class="field"><label>拍摄时间</label><input class="text-input" id="wTime" placeholder="2026.08.02 10:24"></div>'
    + '<div class="field f-row">'
    + '<div><label>天气</label><input class="text-input" id="wWeather" placeholder="如：晴 33°C"></div>'
    + '<div><label>地点</label><input class="text-input" id="wLocation" placeholder="如：深圳市南山区前湾一路"></div></div>'
    + '<div class="field f-row">'
    + '<div><label>经度</label><input class="text-input" id="wLng" placeholder="如：113.891234"></div>'
    + '<div><label>纬度</label><input class="text-input" id="wLat" placeholder="如：22.534567"></div></div>'
    + '<div class="field"><label>防伪码（14 位，自动生成）</label>'
    + '<div class="code-row"><input class="text-input" id="wCode" readonly><button class="ghost" id="wCodeRe">换一个</button></div></div>'
    + '<div class="m-foot"><button class="ghost" data-a="no">取消</button>'
    + '<button class="btn-primary" data-a="yes">确认上传</button></div>';
  var inst = createModal(html, { width: 560 });

  var ta = $('#wContent', inst.box), cnt = $('#wCnt', inst.box);
  ta.value = pre.content; cnt.textContent = ta.value.length;
  ta.addEventListener('input', function () { cnt.textContent = ta.value.length; });
  $$('.q-chip', inst.box).forEach(function (c) {
    c.addEventListener('click', function () {
      var s = ta.selectionStart != null ? ta.selectionStart : ta.value.length;
      var nv = (ta.value.slice(0, s) + c.dataset.w + ta.value.slice(s)).slice(0, 500);
      ta.value = nv; cnt.textContent = nv.length; ta.focus();
    });
  });
  var timeEl = $('#wTime', inst.box), weatherEl = $('#wWeather', inst.box),
    locEl = $('#wLocation', inst.box), lngEl = $('#wLng', inst.box),
    latEl = $('#wLat', inst.box), codeEl = $('#wCode', inst.box);
  timeEl.value = pre.time;
  weatherEl.value = pre.weather;
  locEl.value = pre.location;
  lngEl.value = pre.lng;
  latEl.value = pre.lat;
  codeEl.value = genCode();
  $('#wCodeRe', inst.box).addEventListener('click', function () { codeEl.value = genCode(); });

  /* 无历史：尝试浏览器定位回填经纬度 + geo 接口回填天气/地点 */
  if (!hist && navigator.geolocation) {
    navigator.geolocation.getCurrentPosition(function (pos) {
      if (inst.closed) return;
      var lng = pos.coords.longitude, lat = pos.coords.latitude;
      if (!lngEl.value) lngEl.value = lng.toFixed(6);
      if (!latEl.value) latEl.value = lat.toFixed(6);
      wl.geo(lng, lat).then(function (d) {
        if (inst.closed || !d) return;
        if (d.weather && !weatherEl.value) weatherEl.value = d.weather;
        if (d.location && !locEl.value) locEl.value = d.location;
      }).catch(function () { /* 失败留空 */ });
    }, function () { }, { timeout: 5000 });
  }

  $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
  var okBtn = $('[data-a="yes"]', inst.box);
  okBtn.addEventListener('click', function () {
    if (okBtn.disabled) return;
    var time = timeEl.value.trim();
    if (!/^\d{4}\.\d{2}\.\d{2} \d{2}:\d{2}$/.test(time)) {
      toast('拍摄时间格式应为 2026.08.02 10:24');
      shake(timeEl);
      return;
    }
    var wm = {
      content: ta.value.trim().slice(0, 500),
      time: time,
      weather: weatherEl.value.trim(),
      location: locEl.value.trim(),
      longitude: withSuffix(lngEl.value.trim(), '°E'),
      latitude: withSuffix(latEl.value.trim(), '°N'),
      antiCode: codeEl.value,
      orientation: ''
    };
    okBtn.disabled = true;
    okBtn.textContent = '正在加水印上传…';
    wl.uploadPhoto(entry.id, { image: dataUrl, members: names, wm: wm }).then(function () {
      inst.close();
      toast('已上传，验证中');
      loadLogs({ quiet: true });
    }).catch(function (e) {
      okBtn.disabled = false;
      okBtn.textContent = '确认上传';
      toast(e.message, { type: 'err' });
    });
  });
}

/* ================= Lightbox（大图 + 信息侧栏 + 照片操作） ================= */
var lbInst = null; // {items:[{url,photo?}], idx, entryId}（entryId=0 表示照片库预览，无操作）
function openPhotoLightbox(entry, pid) {
  var items = (entry.photos || []).map(function (p) { return { url: p.url, photo: p }; });
  var idx = 0;
  (entry.photos || []).forEach(function (p, i) { if (p.id === pid) idx = i; });
  openLightbox(items, idx, entry.id);
}
function openLightbox(items, idx, entryId) {
  if (!items.length) return;
  lbInst = { items: items, idx: idx, entryId: entryId || 0 };
  renderLB();
  $('#lightbox').classList.add('open');
}
function closeLightbox() {
  lbInst = null;
  $('#lightbox').classList.remove('open');
}
function shiftLB(delta) {
  if (!lbInst) return;
  var n = lbInst.items.length;
  lbInst.idx = (lbInst.idx + delta + n) % n;
  renderLB();
}
function lbRow(k, v, cls) {
  return '<div class="lb-row"><div class="k">' + esc(k) + '</div><div class="v' + (cls ? ' ' + cls : '') + '">' + (v ? v : '—') + '</div></div>';
}
function renderLB() {
  var it = lbInst.items[lbInst.idx];
  var many = lbInst.items.length > 1;
  var p = it.photo;
  var html = '<button class="lb-x">' + I.x + '</button>'
    + (many ? '<button class="lb-arrow l">' + I.chevL + '</button><button class="lb-arrow r">' + I.chevR + '</button>' : '')
    + '<div class="lb-stage"><img class="lb-img" alt="照片预览"><div class="lb-count">' + (lbInst.idx + 1) + '/' + lbInst.items.length + '</div></div>';
  if (p) {
    var stCls = p.verify_status === 'passed' ? 'good' : (p.verify_status === 'pending' ? '' : 'bad');
    html += '<aside class="lb-side">'
      + lbRow('验证情况', esc(verifyText(p)), stCls)
      + lbRow('人名', esc((p.members || []).join('、')))
      + '<div class="lb-row"><div class="k">施工内容<button class="copy-btn" data-copy>复制</button></div><div class="v" id="lbWork">' + (p.work_content ? esc(p.work_content) : '—') + '</div></div>'
      + lbRow('拍摄时间', esc(p.shot_time))
      + lbRow('天气', esc(p.weather))
      + lbRow('地点', esc(p.location))
      + lbRow('经度', esc(p.lng))
      + lbRow('纬度', esc(p.lat))
      + '<div class="lb-ops">'
      + (p.verify_status === 'failed' ? '<button class="ghost" data-lb="reverify">重新验证</button>' : '')
      + '<button class="ghost" data-lb="members">改人名</button>'
      + '<button class="ghost danger" data-lb="del">删除</button>'
      + '</div></aside>';
  }
  var lb = $('#lightbox');
  lb.innerHTML = html;
  var img = $('.lb-img', lb);
  img.onload = function () { img.classList.add('show'); };
  img.src = it.url;
  $('.lb-x', lb).addEventListener('click', closeLightbox);
  if (many) {
    $('.lb-arrow.l', lb).addEventListener('click', function (e) { e.stopPropagation(); shiftLB(-1); });
    $('.lb-arrow.r', lb).addEventListener('click', function (e) { e.stopPropagation(); shiftLB(1); });
  }
  var copyBtn = $('[data-copy]', lb);
  if (copyBtn) {
    copyBtn.addEventListener('click', function (e) {
      e.stopPropagation();
      copyText($('#lbWork', lb).textContent);
    });
  }
  /* 侧栏照片操作（改人名 / 重新验证 / 删除） */
  $$('[data-lb]', lb).forEach(function (b) {
    b.addEventListener('click', function (e) { e.stopPropagation(); onLbOp(b); });
  });
}

/* 预览侧栏操作：成功后经 loadLogs → lbLiveSync 联动刷新（删除则自动关闭预览） */
function onLbOp(btn) {
  var it = lbInst && lbInst.items[lbInst.idx];
  var p = it && it.photo;
  if (!p) return;
  var entry = findEntry(lbInst.entryId);
  if (!entry) { closeLightbox(); return; }
  var op = btn.dataset.lb;
  if (op === 'members') openPhotoMembersModal(entry, p);
  else if (op === 'reverify') onReverify(btn, p);
  else if (op === 'del') onDelPhoto(p);
}

/* 看板数据每次刷新（含验证回写轮询）后联动预览：当前照片被删 → 关闭；状态/人名/施工内容变化 → 重渲染 */
function lbLiveSync() {
  if (!lbInst || !lbInst.entryId) return;
  var en = findEntry(lbInst.entryId);
  var cur = lbInst.items[lbInst.idx];
  var np = en && cur && cur.photo
    ? (en.photos || []).filter(function (x) { return x.id === cur.photo.id; })[0]
    : null;
  if (!np) { closeLightbox(); return; }
  if (np.verify_status !== cur.photo.verify_status
    || (np.members || []).join('、') !== (cur.photo.members || []).join('、')
    || np.work_content !== cur.photo.work_content) {
    var idx0 = lbInst.idx;
    lbInst.items = (en.photos || []).map(function (x) { return { url: x.url, photo: x }; });
    lbInst.idx = Math.min(idx0, lbInst.items.length - 1);
    renderLB();
  }
}
function copyText(t) {
  function fallback() {
    var ta = document.createElement('textarea');
    ta.value = t;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); toast('已复制'); }
    catch (e) { toast('复制失败', { type: 'err' }); }
    ta.remove();
  }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(t).then(function () { toast('已复制'); }).catch(fallback);
  } else {
    fallback();
  }
}
function initLightbox() {
  $('#lightbox').addEventListener('click', function (e) {
    /* 点遮罩空白处关闭（侧栏与按钮已 stopPropagation 或单独绑定） */
    if (e.target === this) closeLightbox();
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      if (lbInst) { closeLightbox(); return; }
      var top = modalStack[modalStack.length - 1];
      if (top) top.close();
      return;
    }
    if (lbInst && e.key === 'ArrowLeft') shiftLB(-1);
    if (lbInst && e.key === 'ArrowRight') shiftLB(1);
  });
}

/* ================= 板块二：照片库（批量下载） ================= */
function libFiltered() {
  if (!state.lib.mine) return state.lib.list;
  var nn = state.user.nickname;
  return state.lib.list.filter(function (p) { return (p.members || []).indexOf(nn) >= 0; });
}
function initLibrary() {
  var r = defaultRange();
  state.lib.from = r.from; state.lib.to = r.to;
  $('#libFrom').value = r.from;
  $('#libTo').value = r.to;
  $('#libFrom').addEventListener('change', function () { state.lib.from = this.value; loadLibrary(); });
  $('#libTo').addEventListener('change', function () { state.lib.to = this.value; loadLibrary(); });
  $('#libQuery').addEventListener('click', loadLibrary);
  $('#libMine').addEventListener('click', function () {
    var sw = $('.switch', this);
    sw.classList.toggle('on');
    state.lib.mine = sw.classList.contains('on');
    renderLibrary(); // 仅看我为前端过滤，无需重拉
  });
  $('#selAllBtn').addEventListener('click', function () {
    var list = libFiltered();
    var allSel = list.length && list.every(function (p) { return state.lib.sel[p.id]; });
    list.forEach(function (p) { state.lib.sel[p.id] = !allSel; });
    renderLibrary();
  });
  $('#dlBtn').addEventListener('click', downloadZip);
  $('#libBody').addEventListener('click', function (e) {
    var sc = e.target.closest('.sel-circle');
    if (sc) {
      e.stopPropagation();
      var fig = sc.closest('.lib');
      var pid = +fig.dataset.pid;
      state.lib.sel[pid] = !state.lib.sel[pid];
      fig.classList.toggle('sel', !!state.lib.sel[pid]);
      updateLibCount();
      return;
    }
    var f = e.target.closest('.lib[data-pid]');
    if (f) {
      var list = libFiltered();
      var items = list.map(function (p) { return { url: p.url }; });
      var idx = 0;
      list.forEach(function (p, i) { if (p.id === +f.dataset.pid) idx = i; });
      openLightbox(items, idx);
    }
  });
}
async function loadLibrary() {
  if (!state.lib.from || !state.lib.to) return;
  $('#libBar').style.display = 'none';
  $('#libBody').innerHTML = emptyHTML('加载中…', '');
  try {
    var d = await wl.photos(state.lib.from, state.lib.to);
    state.lib.list = (d && d.list) || [];
    state.lib.loaded = true;
    state.lib.sel = {};
    state.lib.list.forEach(function (p) { state.lib.sel[p.id] = true; }); // 默认全选
    renderLibrary();
  } catch (e) {
    $('#libBody').innerHTML = '';
    toast(e.message, { type: 'err' });
  }
}
function renderLibrary() {
  var list = libFiltered();
  $('#libTotal').textContent = '共 ' + list.length + ' 张照片';
  if (!list.length) {
    $('#libBody').innerHTML = emptyHTML('该范围内暂无照片', '');
    $('#libBar').style.display = 'none';
    return;
  }
  /* 按 month 分组（接口已按日期+上传序排好，保持顺序） */
  var groups = [], cur = null;
  list.forEach(function (p) {
    if (!cur || cur.month !== p.month) { cur = { month: p.month, items: [] }; groups.push(cur); }
    cur.items.push(p);
  });
  $('#libBody').innerHTML = groups.map(function (g) {
    return '<div class="lib-group"><div class="lib-date">' + esc(fmtMonthCN(g.month)) + ' <span class="cnt">' + g.items.length + ' 张</span></div>'
      + '<div class="ph-lib-grid">' + g.items.map(function (p) {
        return '<figure class="photo lib' + (state.lib.sel[p.id] ? ' sel' : '') + '" data-pid="' + p.id + '">'
          + '<img class="ph-img" src="' + esc(p.url) + '" loading="lazy" alt="水印照片">'
          + '<span class="sel-circle">' + I.check + '</span>'
          + '<span class="lib-day">' + p.day + ' 日</span>'
          + '</figure>';
      }).join('') + '</div></div>';
  }).join('');
  $('#libBar').style.display = '';
  updateLibCount();
}
function updateLibCount() {
  var list = libFiltered();
  var n = list.filter(function (p) { return state.lib.sel[p.id]; }).length;
  $('#libCount').textContent = '已选 ' + n + '/' + list.length;
  $('#selAllBtn').textContent = (n === list.length && list.length) ? '全不选' : '全选';
}
/* Content-Disposition 文件名解析（RFC5987 优先） */
function cdFilename(cd) {
  var m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (m) { try { return decodeURIComponent(m[1].replace(/"/g, '')); } catch (e) { } }
  m = /filename="?([^";]+)"?/i.exec(cd);
  return m ? m[1] : '';
}
async function downloadZip() {
  var btn = $('#dlBtn');
  if (btn.disabled) return;
  var list = libFiltered().filter(function (p) { return state.lib.sel[p.id]; });
  if (!list.length) { toast('请先选择照片'); return; }
  btn.disabled = true;
  var old = btn.innerHTML;
  btn.textContent = '打包下载中…';
  try {
    var res = await fetch('api/zip', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({
        photos: list.map(function (p) { return { url: p.url, name: p.log_date + '-' + p.id + '.jpg' }; })
      })
    });
    if (res.status === 401) { goLogin(); return; }
    if (!res.ok) {
      var ej = null;
      try { ej = await res.json(); } catch (e) { }
      throw new Error((ej && (ej.error || ej.message)) || ('打包下载失败（' + res.status + '）'));
    }
    var name = cdFilename(res.headers.get('Content-Disposition') || '') || '水印照片.zip';
    var blob = await res.blob();
    var a = document.createElement('a');
    var url = URL.createObjectURL(blob);
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 4000);
    toast('已开始下载 ' + list.length + ' 张照片');
  } catch (e) {
    toast(e.message, { type: 'err' });
  }
  btn.disabled = false;
  btn.innerHTML = old;
}

/* ================= 板块三：验证报告 ================= */
function initReport() {
  var r = defaultRange();
  state.rep.from = r.from; state.rep.to = r.to;
  $('#repFrom').value = r.from;
  $('#repTo').value = r.to;
  $('#repFrom').addEventListener('change', function () { state.rep.from = this.value; });
  $('#repTo').addEventListener('change', function () { state.rep.to = this.value; });
  $('#repQuery').addEventListener('click', loadReport);
  /* 报告行点击：切回日志看板并跳到该日（同日或无该卡时仅切板块） */
  $('#repBody').addEventListener('click', function (e) {
    var row = e.target.closest('.rep-row[data-d]');
    if (!row) return;
    $('.nav-btn[data-page="board"]').click();
    gotoDate(row.dataset.d);
  });
  $('#repMine').addEventListener('click', function () {
    var sw = $('.switch', this);
    sw.classList.toggle('on');
    state.rep.mine = sw.classList.contains('on');
    loadReport(); // scope 传参，需重新查询
  });
}
async function loadReport() {
  if (!state.rep.from || !state.rep.to) { toast('请选择起止日期'); return; }
  $('#repBody').innerHTML = emptyHTML('加载中…', '');
  try {
    var d = await wl.report(state.rep.from, state.rep.to, state.rep.mine ? 'mine' : 'all');
    renderReport((d && d.list) || []);
  } catch (e) {
    $('#repBody').innerHTML = '';
    $('#repTotal').textContent = '';
    toast(e.message, { type: 'err' });
  }
}
function renderReport(list) {
  $('#repTotal').textContent = '合计 ' + list.length + ' 条';
  if (!list.length) {
    $('#repBody').innerHTML = emptyHTML('该范围内全部通过', '');
    return;
  }
  /* 按 log_date 分组（保持接口顺序） */
  var groups = [], cur = null;
  list.forEach(function (r) {
    if (!cur || cur.date !== r.log_date) { cur = { date: r.log_date, items: [] }; groups.push(cur); }
    cur.items.push(r);
  });
  $('#repBody').innerHTML = groups.map(function (g) {
    return '<div class="rep-group"><div class="rep-date">' + esc(fmtDateCN(g.date)) + ' <span class="cnt">' + g.items.length + ' 条未通过</span></div>'
      + g.items.map(function (r) {
        /* 行可点击：切回日志看板并跳到该记录所在日期（PC 屏幕大，无需定位到具体卡片） */
        return '<div class="rep-row" data-d="' + r.log_date + '" title="点击查看该日看板">'
          + '<span class="rep-plate">' + esc(r.plate_no || '未出车') + '</span>'
          + '<span class="rep-members">' + esc((r.members || []).join('、')) + '</span>'
          + '<span class="rep-chips">' + (r.reasons || []).map(function (x) { return '<em class="reason">' + esc(x) + '</em>'; }).join('') + '</span>'
          + '<span class="rep-go"><svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 5.5 16 12l-6.5 6.5"/></svg></span>'
          + '</div>';
      }).join('') + '</div>';
  }).join('');
}

/* ================= 板块四：数据管理（admin） ================= */
var ADM_PH = {
  vehicles: '输入新车牌号，回车快速添加',
  destinations: '输入新目的地，回车快速添加',
  members: '输入新成员姓名，回车快速添加'
};
function initAdmin() {
  initSeg('#segData', function (v) {
    state.adm.seg = v;
    state.adm.kw = '';
    $('#dataFilter').value = '';
    $('#dataInput').placeholder = ADM_PH[v];
    loadAdminSeg();
  });
  $('#dataFilter').addEventListener('input', function () {
    state.adm.kw = this.value.trim();
    renderAdmin();
  });
  $('#dataInput').addEventListener('keydown', function (e) { if (e.key === 'Enter') addDataRow(); });
  $('#dataAddBtn').addEventListener('click', addDataRow);
  $('#dataList').addEventListener('click', function (e) {
    var row = e.target.closest('.data-row');
    if (!row) return;
    var item = null;
    state.adm.list.forEach(function (it) { if (String(it.id) === row.dataset.id) item = it; });
    if (!item) return;
    if (e.target.closest('.act-toggle')) toggleDataRow(item);
    else if (e.target.closest('.act-edit')) startEditRow(row, item);
  });
}
function adminName(it) { return it.name || it.plate_no || ''; }
async function loadAdminSeg() {
  try {
    var d = await wl.adminList(state.adm.seg);
    state.adm.list = (d && d.list) || [];
    state.adm.loaded = true;
    renderAdmin();
  } catch (e) {
    toast(e.message, { type: 'err' });
  }
}
function renderAdmin() {
  var kw = state.adm.kw;
  var list = state.adm.list.filter(function (it) { return !kw || adminName(it).indexOf(kw) >= 0; });
  if (!list.length) {
    $('#dataList').innerHTML = '<li class="data-row"><span style="color:var(--sub);font-size:13px">暂无数据</span></li>';
    return;
  }
  $('#dataList').innerHTML = list.map(function (it) {
    var off = !it.status;
    return '<li class="data-row' + (off ? ' off' : '') + '" data-id="' + it.id + '">'
      + '<span class="d-name">' + esc(adminName(it)) + '</span>'
      + '<span class="st-badge ' + (off ? 'off' : 'on') + '">' + (off ? '已停用' : '启用中') + '</span>'
      + '<span class="d-ops"><button class="mini act-edit">编辑</button>'
      + '<button class="mini act-toggle">' + (off ? '启用' : '停用') + '</button></span></li>';
  }).join('');
}
/* 管理接口写操作后：优先用返回的 list，否则重拉；同时失效 meta 缓存 */
function afterAdminWrite(d) {
  state.meta = null;
  if (d && d.list) {
    state.adm.list = d.list;
    renderAdmin();
  } else {
    loadAdminSeg();
  }
}
function addDataRow() {
  var inp = $('#dataInput');
  var v = inp.value.trim();
  if (!v) { toast('请输入名称'); shake(inp); return; }
  var btn = $('#dataAddBtn');
  if (btn.disabled) return;
  btn.disabled = true;
  wl.adminAdd(state.adm.seg, v).then(function (d) {
    inp.value = '';
    toast('已添加');
    afterAdminWrite(d);
  }).catch(function (e) {
    toast(e.message, { type: 'err' });
  }).then(function () { btn.disabled = false; });
}
function toggleDataRow(item) {
  wl.adminPut(state.adm.seg, item.id, { status: item.status ? 0 : 1 })
    .then(function (d) {
      toast(item.status ? '已停用' : '已启用');
      afterAdminWrite(d);
    })
    .catch(function (e) { toast(e.message, { type: 'err' }); });
}
/* 行内编辑改名：Enter/blur 提交，Esc 取消 */
function startEditRow(row, item) {
  var nameEl = $('.d-name', row);
  if (!nameEl) return;
  var old = adminName(item);
  var inp = document.createElement('input');
  inp.className = 'text-input edit-inp';
  inp.value = old;
  nameEl.replaceWith(inp);
  inp.focus();
  inp.select();
  var finished = false;
  function done(commit) {
    if (finished) return;
    finished = true;
    var v = inp.value.trim() || old;
    var span = document.createElement('span');
    span.className = 'd-name';
    span.textContent = commit ? v : old;
    inp.replaceWith(span);
    if (commit && v !== old) {
      wl.adminPut(state.adm.seg, item.id, { name: v })
        .then(function (d) { toast('已更新'); afterAdminWrite(d); })
        .catch(function (e) { toast(e.message, { type: 'err' }); renderAdmin(); });
    }
  }
  inp.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') done(true);
    if (e.key === 'Escape') done(false);
  });
  inp.addEventListener('blur', function () { done(true); });
}

/* ================= 启动 ================= */
function init() {
  /* 首屏先拿当前用户：失败（含 401）跳登录页 */
  fetch('api/me', { credentials: 'same-origin' })
    .then(function (res) {
      if (res.status === 401 || !res.ok) { goLogin(); return null; }
      return res.json().catch(function () { return null; });
    })
    .then(function (j) {
      if (j === null) return;
      if (!j || !j.ok || !j.user) { goLogin(); return; }
      state.user = j.user;
      renderUser();
      initNav();
      initLightbox();
      initBoard();
      initLibrary();
      initReport();
      initAdmin();
      loadLogs();
      ensureMeta().catch(function () { /* meta 失败不阻塞看板，打开模态时重试 */ });
    })
    .catch(function () { goLogin(); });
}
init();

})();
