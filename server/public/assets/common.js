/* ============================================================
   Shade 壹匣 · 网页版公共运行时（依赖 assets/icons.js 先行加载）
   全局对象 window.Shade，各页面代理按以下签名调用：

   Shade.api(path, opts)      → Promise<{code,data,message}>
     fetch 封装：baseUrl ''（同域）；自动带 Authorization: Bearer <shade_token>；
     opts.body 为普通对象时自动 JSON 序列化；HTTP 401 清本地登录态并跳 /login.html；
     业务码 code!==0 时抛 Error(message)。
   Shade.user()               → 本地缓存的用户对象（localStorage.shade_user），未登录为 null
   Shade.setAuth(token, user) → 写入登录态（shade_token / shade_user）
   Shade.logout()             → 调 POST /api/v1/auth/logout（失败也继续），清本地登录态跳 /login.html
   Shade.requireAuth()        → 无 token 直接跳 /login.html；有 token 返回 true
   Shade.icon(name, size, color) → inline SVG 字符串（见 assets/icons.js）
   Shade.topbar(opts)         → 统一渲染顶部导航（插入 body 开头；需 icons.js 先加载）
     opts.active: 'index' | 'callme' | 'worklog' | 'safeday' | 'kvm' | 'admin'（当前页，渲染为无链接激活态）
     结构：左侧 Logo（点击回 /index.html）+ 常驻导航（TOP_NAV：工作台与全部应用始终显示，当前页高亮）；
           右侧「管理」链接（仅 role==='admin' 可见）
           + 分隔线 + 头像字 + 昵称（取 Shade.user() 缓存）+ 退出按钮
     渲染后页面可用接口最新资料刷新 #miniAvatar / #topName / #navAdmin（id 与各页既有逻辑兼容）
   Shade.toast(msg, type)     → 顶部轻提示，type: 'info' | 'success' | 'error'
   Shade.dropdown(select, opts) → 把原生 <select> 换为自绘下拉（按钮触发器 + 弹出列表，样式 theme.css .dd）；
     原 select 隐藏保留为数据源：选中回写 value 并派发 change 事件；脚本改选项/value/disabled 后调返回实例的 sync()；
     opts: { width, minWidth, height }；返回 { el, sync, close }
   Shade.reveal()             → 给页面中未处理的 .rv 元素挂 IntersectionObserver，进入视口加 .in
   Shade.esc(html)            → HTML 转义，防注入
   Shade.fmtDate(d, withTime) → 'YYYY-MM-DD' 或 'YYYY-MM-DD HH:mm'；d 可为 Date/时间戳/字符串，缺省当前时间

   加载即自动执行：向 body 末尾注入备案页脚 <footer class="beian-foot">（工信部 + 公安备案两条链接，
   样式见 theme.css；body 为 flex 纵列，页脚随之沉底；已存在 .beian-foot 时跳过不重复注入；
   body 带 data-beian="off" 属性时整页跳过注入，由页面自放备案链接，如登录页置于表单块底部）
   ============================================================ */
(function () {
  const TOKEN_KEY = 'shade_token';
  const USER_KEY = 'shade_user';

  function clearAuth() {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
  }

  // 统一接口调用
  async function api(path, opts) {
    opts = opts || {};
    const headers = Object.assign({}, opts.headers);
    const token = localStorage.getItem(TOKEN_KEY);
    if (token) headers['Authorization'] = 'Bearer ' + token;

    let body = opts.body;
    if (body && typeof body === 'object' && !(body instanceof FormData) && !(body instanceof Blob)) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(body);
    }

    let res;
    try {
      res = await fetch(path, Object.assign({}, opts, { headers: headers, body: body }));
    } catch (e) {
      throw new Error('网络异常，无法连接服务器');
    }
    if (res.status === 401) {
      clearAuth();
      if (!/\/login\.html$/.test(location.pathname)) location.href = '/login.html';
      throw new Error('登录已过期，请重新登录');
    }
    const json = await res.json().catch(function () { return null; });
    if (!json) throw new Error('服务响应异常（HTTP ' + res.status + '）');
    if (json.code !== 0) throw new Error(json.message || '请求失败（' + json.code + '）');
    return json;
  }

  // 本地缓存的用户对象
  function user() {
    try { return JSON.parse(localStorage.getItem(USER_KEY) || 'null'); } catch (e) { return null; }
  }

  // 统一备案页脚：注入为 body 末尾 flex item（样式 theme.css .beian-foot）；
  // body 带 data-beian="off" 时跳过（页面自行放置），已存在 .beian-foot 时跳过
  function beianFooter() {
    if (document.body && document.body.getAttribute('data-beian') === 'off') return;
    if (document.querySelector('.beian-foot')) return;
    var foot = document.createElement('footer');
    foot.className = 'beian-foot';
    foot.innerHTML =
      '<a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener">晋ICP备2025063709号-2</a>' +
      '<a href="https://beian.mps.gov.cn/#/query/webSearch?code=14118202000050" target="_blank" rel="noopener"><img src="/public-security.png" alt="公安备案">晋公网安备14118202000050号</a>';
    document.body.appendChild(foot);
  }
  if (document.body) beianFooter();
  else document.addEventListener('DOMContentLoaded', beianFooter);

  // 写入登录态
  function setAuth(token, userObj) {
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(USER_KEY, JSON.stringify(userObj || {}));
  }

  // 退出登录：通知后端注销 token（失败不阻塞本地清理）
  async function logout() {
    try { await api('/api/v1/auth/logout', { method: 'POST' }); } catch (e) { /* 忽略，本地照常清理 */ }
    clearAuth();
    location.href = '/login.html';
  }

  // 登录门控：无 token 直接跳登录页
  function requireAuth() {
    if (!localStorage.getItem(TOKEN_KEY)) {
      location.replace('/login.html');
      return false;
    }
    return true;
  }

  // 统一顶部导航：左侧 Logo + 常驻导航（工作台与全部应用始终显示，当前页高亮）；
  // 右侧「管理」（仅 admin 可见）+ 用户区 + 退出
  var TOP_NAV = [
    { key: 'index', name: '工作台', href: '/index.html' },
    { key: 'callme', name: 'Call Me', href: '/callme.html' },
    { key: 'worklog', name: '出工日志', href: '/worklog.html' },
    { key: 'safeday', name: '安全日活动', href: '/safeday.html' },
    { key: 'kvm', name: '远程连接', href: '/kvm.html' },
  ];
  function topbar(opts) {
    opts = opts || {};
    var active = opts.active || 'index';
    var u = user() || {};
    var name = u.nickname || u.username || '班组成员';
    var isAdmin = u.role === 'admin';
    var icon = window.Shade.icon;

    // 左侧常驻导航：当前页渲染为纯文本激活态（无链接），其余为可点链接
    var leftNav = TOP_NAV.map(function (it) {
      return it.key === active
        ? '<span class="topbar-item on">' + it.name + '</span>'
        : '<a class="topbar-item" href="' + it.href + '">' + it.name + '</a>';
    }).join('');

    // 右侧「管理」：仅 admin 可见；管理页时渲染为激活态
    var adminInner = icon('setting', 14) + '管理';
    var adminNav = active === 'admin'
      ? '<span class="topbar-item on" id="navAdmin"' + (isAdmin ? '' : ' hidden') + '>' + adminInner + '</span>'
      : '<a class="topbar-item" id="navAdmin" href="/admin.html"' + (isAdmin ? '' : ' hidden') + '>' + adminInner + '</a>';

    document.body.insertAdjacentHTML('afterbegin',
      '<header class="topbar"><div class="topbar-in">' +
        '<a class="topbar-logo" href="/index.html">' +
          '<span class="logo-mark"><img src="/favicon.svg" alt="Shade 壹匣"></span>' +
          '<b>Shade <i>壹匣</i></b>' +
        '</a>' +
        '<nav class="topbar-nav">' + leftNav + '</nav>' +
        '<div class="topbar-right">' +
          '<nav class="topbar-nav">' + adminNav + '</nav>' +
          '<div class="topbar-user">' +
            '<span class="mini-avatar" id="miniAvatar">' + esc(name.charAt(0)) + '</span>' +
            '<span class="nm" id="topName">' + esc(name) + '</span>' +
          '</div>' +
          '<button class="btn btn-sm" id="btnLogout">' + icon('logout', 15) + '退出</button>' +
        '</div>' +
      '</div></header>');
    document.getElementById('btnLogout').addEventListener('click', function () { logout(); });
  }

  // 顶部轻提示
  function toast(msg, type) {
    type = type || 'info';
    let wrap = document.querySelector('.toast-wrap');
    if (!wrap) {
      wrap = document.createElement('div');
      wrap.className = 'toast-wrap';
      document.body.appendChild(wrap);
    }
    const iconName = type === 'success' ? 'check-circle' : (type === 'error' ? 'error-circle' : 'info-circle');
    const el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.innerHTML = window.Shade.icon(iconName, 16) + '<span></span>';
    el.querySelector('span').textContent = msg;
    wrap.appendChild(el);
    setTimeout(function () {
      el.classList.add('out');
      setTimeout(function () { el.remove(); }, 320);
    }, 2600);
  }

  // 滚动 reveal：.rv 进入视口时加 .in（可重复调用，自动跳过已观察元素）
  let rvObserver = null;
  function reveal() {
    const els = document.querySelectorAll('.rv:not([data-rv-obs])');
    if (!('IntersectionObserver' in window)) {
      els.forEach(function (el) { el.classList.add('in'); });
      return;
    }
    if (!rvObserver) {
      rvObserver = new IntersectionObserver(function (entries) {
        entries.forEach(function (en) {
          if (en.isIntersecting) {
            en.target.classList.add('in');
            rvObserver.unobserve(en.target);
          }
        });
      }, { threshold: 0.08 });
    }
    els.forEach(function (el) {
      el.setAttribute('data-rv-obs', '1');
      rvObserver.observe(el);
    });
  }

  // HTML 转义
  function esc(html) {
    return String(html == null ? '' : html).replace(/[&<>"']/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
    });
  }

  // 轻量自绘下拉：把原生 <select> 替换为按钮触发器 + 弹出列表（暖纸主题，样式见 theme.css .dd）；
  // 原 select 隐藏保留为数据源——选中回写 select.value 并派发 change 事件，既有监听与取值逻辑不变；
  // 脚本改动选项 / value / disabled 后需调返回实例的 sync() 重绘（打开弹层时也会自动 sync）。
  // opts: { width, minWidth, height }（工具行传 width:'auto'；表单场景默认宽 100% 高 40px）
  function dropdown(select, opts) {
    opts = opts || {};
    var wrap = document.createElement('div');
    wrap.className = 'dd';
    if (opts.width) wrap.style.width = opts.width;
    if (opts.minWidth) wrap.style.minWidth = opts.minWidth;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'dd-btn';
    if (opts.height) btn.style.height = opts.height + 'px';
    btn.innerHTML = '<span class="dd-label"></span>' +
      '<svg class="dd-tri" width="10" height="6" viewBox="0 0 10 6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M1 1l4 4 4-4"/></svg>';
    var pop = document.createElement('div');
    pop.className = 'dd-pop';
    select.style.display = 'none'; // 原生控件隐藏，保留为数据源
    select.parentNode.insertBefore(wrap, select.nextSibling);
    wrap.appendChild(btn);

    // 弹出列表挂 document.body（fixed 定位）：页头/卡片/弹层的层叠上下文或 overflow 会遮挡裁切内嵌弹出层
    function place() {
      var r = btn.getBoundingClientRect();
      pop.style.minWidth = r.width + 'px';
      pop.style.left = r.left + 'px';
      pop.style.top = (r.bottom + 6) + 'px';
      // 底部空间不足则向上展开；右侧防溢出
      var h = pop.offsetHeight;
      if (r.bottom + 6 + h > window.innerHeight && r.top - 6 - h > 0) {
        pop.style.top = (r.top - 6 - h) + 'px';
      }
      var w = pop.offsetWidth;
      if (r.left + w > window.innerWidth - 8) {
        pop.style.left = Math.max(8, window.innerWidth - 8 - w) + 'px';
      }
    }

    function sync() {
      btn.disabled = !!select.disabled;
      var cur = select.options[select.selectedIndex];
      btn.querySelector('.dd-label').textContent = cur ? cur.text : '';
      pop.innerHTML = Array.prototype.map.call(select.options, function (o, i) {
        return '<button type="button" class="dd-opt' + (i === select.selectedIndex ? ' on' : '') + '" data-i="' + i + '">' +
          '<span>' + esc(o.text) + '</span>' +
          '<svg class="dd-ck" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>' +
        '</button>';
      }).join('');
    }
    function onDocDown(e) { if (!wrap.contains(e.target) && !pop.contains(e.target)) close(); }
    function onKey(e) {
      if (e.key === 'Escape') { e.stopPropagation(); close(); } // 捕获阶段拦截，避免连带关闭页面弹层
    }
    function onScroll(e) { if (!pop.contains(e.target)) close(); } // 触发器随内容滚动会漂移，直接收起
    function close() {
      wrap.classList.remove('open');
      pop.classList.remove('open');
      document.removeEventListener('mousedown', onDocDown, true);
      document.removeEventListener('keydown', onKey, true);
      document.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', close);
    }
    btn.addEventListener('click', function () {
      if (btn.disabled) return;
      if (wrap.classList.contains('open')) { close(); return; }
      sync();
      wrap.classList.add('open');
      document.body.appendChild(pop); // 幂等：已在 body 下则为移动
      pop.classList.add('open');
      place();
      document.addEventListener('mousedown', onDocDown, true);
      document.addEventListener('keydown', onKey, true);
      document.addEventListener('scroll', onScroll, true);
      window.addEventListener('resize', close);
    });
    pop.addEventListener('mousedown', function (e) {
      var optBtn = e.target.closest('.dd-opt');
      if (!optBtn) return;
      e.preventDefault();
      select.selectedIndex = Number(optBtn.dataset.i);
      close();
      sync();
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    sync();
    return { el: wrap, sync: sync, close: close };
  }

  // 日期格式化
  function fmtDate(d, withTime) {
    const dt = d ? new Date(d) : new Date();
    if (isNaN(dt.getTime())) return '';
    const p = function (n) { return String(n).padStart(2, '0'); };
    let s = dt.getFullYear() + '-' + p(dt.getMonth() + 1) + '-' + p(dt.getDate());
    if (withTime) s += ' ' + p(dt.getHours()) + ':' + p(dt.getMinutes());
    return s;
  }

  window.Shade = Object.assign(window.Shade || {}, {
    api: api,
    user: user,
    setAuth: setAuth,
    logout: logout,
    requireAuth: requireAuth,
    topbar: topbar,
    toast: toast,
    reveal: reveal,
    esc: esc,
    fmtDate: fmtDate,
    dropdown: dropdown,
  });
})();
