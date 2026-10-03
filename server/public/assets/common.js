/* ============================================================
   Shade 壹匣 · 网页版公共运行时（依赖 assets/icons.js 先行加载）
   全局对象 window.Shade，各页面代理按以下签名调用：

   Shade.api(path, opts)      → Promise<{code,data,message}>
     fetch 封装：baseUrl ''（同域）；自动带 Authorization: Bearer <shade_token>；
     opts.body 为普通对象时自动 JSON 序列化；HTTP 401 清本地登录态并跳 /login.html
     （/api/v1/auth/login 的 401 除外——账号密码错误原样返回，由上层以后端 message 抛出）；
     业务码 code!==0 时抛 Error(message)。
   Shade.apiRaw(path, opts)   → Promise<Response>：api() 的底层（token 注入 + fetch + 网络异常抛错
     + 401 清登录态跳登录页，auth/login 除外），返回原始 Response（不解析 JSON、不校验业务码），
     供二进制下载 / SSE 流式 / 需自处理响应信封的场景；AbortController 中止原样抛 AbortError
   Shade.saveBlob(res, fallbackName) → 二进制 Response 落地为下载文件：文件名先解析
     Content-Disposition（RFC5987 filename* 优先），缺省 fallbackName；临时 a 标签触发，延时回收 ObjectURL
   Shade.user()               → 本地缓存的用户对象（localStorage.shade_user），未登录为 null
   Shade.setAuth(token, user) → 写入登录态（shade_token / shade_user）
   Shade.requireAuth()        → 无 token 或 token 已过期直接清登录态跳 /login.html；有效返回 true
   Shade.isTokenExpired()     → 本地 JWT exp 判期（网页端 token 为 JWT_WEB_EXPIRES 短时效，默认 12h；
     本运行时每分钟巡检一次，到期自动清登录态回登录页，覆盖闲置挂机场景）
   Shade.$(s, c) / Shade.$$(s, c) → querySelector / querySelectorAll 数组（c 缺省 document）
   Shade.withTeam(path, teamId) → 班组隔离 query 助手：teamId 非空时为 path 追加 team_id
     （query 优先于 body，后端 resolveReqTeam 同口径）
   Shade.icon(name, size, color) → inline SVG 字符串（见 assets/icons.js）
   Shade.topbar(opts)         → 统一渲染顶部导航（插入 body 开头；需 icons.js 先加载）
     opts.active: 'index' | 'quiz' | 'callme' | 'worklog' | 'safeday' | 'kvm' | 'admin'（当前页，渲染为无链接激活态）
     结构：左侧 Logo（点击回 /index.html）+ 常驻导航（工作台 + /api/v1/app/list 按权限下发的应用，
           顺序同工作台宫格、无权限不显示，缓存 shade_apps 先渲染后校正，当前页高亮）；
           右侧「管理」链接（仅 role==='admin' 可见）
           + 分隔线 + 头像字 + 昵称（取 Shade.user() 缓存）+ 退出按钮
     渲染后页面可用 Shade.refreshUser 刷新 #miniAvatar / #topName（id 与各页既有逻辑兼容）
   Shade.refreshUser(onUser)  → Promise：GET /api/v1/user/profile 成功更新 #miniAvatar/#topName
     （元素存在才更新）并回调 onUser(data)；接口失败回退本地缓存 Shade.user() 同样更新并回调
     （无缓存时回调 null，顶栏保持 topbar() 渲染的缓存/默认文案）；页面额外刷新逻辑放回调
   Shade.toast(msg, type)     → 顶部轻提示，type: 'info' | 'success' | 'error'
   Shade.dropdown(select, opts) → 把原生 <select> 换为自绘下拉（按钮触发器 + 弹出列表，样式 theme.css .dd）；
     原 select 隐藏保留为数据源：选中回写 value 并派发 change 事件；脚本改选项/value/disabled 后调返回实例的 sync()；
     opts: { width, minWidth, height }；返回 { el, sync, close, destroy }——destroy 在 close 之上把弹出列表
     从 document.body 移除，容器 innerHTML 重建场景先 destroy 旧实例防孤儿弹层残留
   Shade.modal(html, opts)    → 通用弹层（可叠层，Esc / 点遮罩关闭；.mask/.modal/.m-scroll/.m-head/.m-x
     等样式由各页 CSS 提供）：opts: { width(px 数字), onClose }；返回 { mask, box, closed, close() }。
     Esc 关顶层监听在此统一注册一份；页面可给 Shade.escGuard 赋值优先拦截函数（返回 true 表示已消费
     本次 Esc，不再关弹层）——如 worklog 大图层需先于弹层响应 Esc
   Shade.mHead(title)         → 弹层标题行 HTML（标题 + 右上角 .m-x 关闭钮，Shade.modal 内自动绑定关闭）
   Shade.confirm(opts)        → 通用确认弹层（叠层）：opts: { title, content, confirmText, danger }；
     返回 Promise<boolean>，遮罩 / Esc / 取消均视为否（.m-head/.m-foot 等样式由各页 CSS 提供）
   Shade.moveTabInd(tab, ind, instant) → 页签滑动指示条跟随：tab 为当前激活页签元素（null 时收起
     指示条），ind 为指示条元素；instant=true 首帧/窗口缩放直接落位，避免从左侧滑入的残影
   Shade.renderNoTeam(el)     → 「未分配班组」整页空态提示卡：填充 el（占位容器，初始 hidden）并显示；
     页面其余区块的隐藏由页面自行处理
   Shade.roleText(role)       → 角色文案：admin→管理员 / team_admin→班组管理员 / 其他→普通用户
   Shade.APP_NAV              → 应用 → 网页版落地页映射（app_key 与 sys_app 种子一致）：顶栏导航与工作台
     宫格落地页共用的单一来源；无网页版的应用不收录
   Shade.setupTeamSel(opts)   → Promise<{getTeamId}|undefined>：超管班组切换器统一装配
     （role!=='admin' 直接返回 undefined）。opts: { select, dropdown, storageKey, allowAll, showEl, onChange }——
     GET /admin/teams 过滤 status===1 渲染 options（allowAll 时前置「全部班组」value=all 且默认 'all'），
     localStorage 回读校验持久化，dropdown.sync() 后显示 showEl（元素带 hidden 属性则 hidden=false，
     否则 style.display=''），change 持久化并回调 onChange(value)（allowAll 时 value 为字符串，否则数字 id）；
     班组列表拉取失败静默：不显示切换器，由后端落默认班组出数
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
  const APPS_KEY = 'shade_apps'; // 顶栏导航用应用清单缓存（/api/v1/app/list 按权限下发，顺序同工作台宫格）

  function clearAuth() {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(APPS_KEY);
  }

  // 解析 JWT 载荷（base64url），失败返回 null
  function tokenPayload(token) {
    try {
      var part = String(token || '').split('.')[1];
      if (!part) return null;
      var b64 = part.replace(/-/g, '+').replace(/_/g, '/');
      while (b64.length % 4) b64 += '=';
      return JSON.parse(atob(b64));
    } catch (e) { return null; }
  }

  // token 是否已过期（无 token 或解析不出 exp 一律视为已过期）
  // 网页端 token 由服务端按 JWT_WEB_EXPIRES 签短时效（默认 12h），到期即需重新登录
  function isTokenExpired() {
    var p = tokenPayload(localStorage.getItem(TOKEN_KEY));
    return !p || !p.exp || p.exp * 1000 <= Date.now();
  }

  // 登录态到期自动清除：每分钟巡检一次，过期即清本地登录态并回登录页（登录页自身不巡检）
  // 兜底 apiRaw 的 401 被动跳转——覆盖「开着页面无任何请求」的闲置场景（如 KVM 页长时间挂机）
  setInterval(function () {
    if (/\/login\.html$/.test(location.pathname)) return;
    if (!localStorage.getItem(TOKEN_KEY)) return;
    if (isTokenExpired()) {
      clearAuth();
      location.href = '/login.html';
    }
  }, 60000);

  // 原始请求：token 注入 + fetch + 网络异常抛错 + 401 清登录态跳登录页（/api/v1/auth/login 的 401 除外，原样返回）；
  // 返回原始 Response 由调用方自处理
  async function apiRaw(path, opts) {
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
      if (e && e.name === 'AbortError') throw e; // 调用方主动中止（如 SSE 超时 ctrl.abort()），原样抛出由调用方识别
      throw new Error('网络异常，无法连接服务器');
    }
    if (res.status === 401) {
      /* 登录接口自身的 401 即「账号或密码错误」：不清登录态、不跳登录页，
         原样返回 Response 由 api() 解析信封、以后端 message 抛出 */
      if (!/\/api\/v1\/auth\/login(\?|$)/.test(path)) {
        clearAuth();
        if (!/\/login\.html$/.test(location.pathname)) location.href = '/login.html';
        throw new Error('登录已过期，请重新登录');
      }
    }
    return res;
  }

  // 统一接口调用（apiRaw 之上解析 JSON 信封与业务码）
  async function api(path, opts) {
    const res = await apiRaw(path, opts);
    const json = await res.json().catch(function () { return null; });
    if (!json) throw new Error('服务响应异常（HTTP ' + res.status + '）');
    if (json.code !== 0) {
      const err = new Error(json.message || '请求失败（' + json.code + '）');
      err.code = json.code;
      err.data = json.data; // 业务明细随错误下发（如题库导入校验的逐行错误）
      throw err;
    }
    return json;
  }

  // 二进制 Response 落地为下载文件：文件名先解析 Content-Disposition（RFC5987 filename* 优先），缺省 fallbackName
  async function saveBlob(res, fallbackName) {
    const cd = res.headers.get('Content-Disposition') || '';
    let m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
    let name = '';
    if (m) { try { name = decodeURIComponent(m[1].replace(/"/g, '')); } catch (e) { } }
    if (!name) {
      m = /filename="?([^";]+)"?/i.exec(cd);
      name = m ? m[1] : '';
    }
    const blob = await res.blob();
    const a = document.createElement('a');
    const url = URL.createObjectURL(blob);
    a.href = url;
    a.download = name || fallbackName;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 4000);
  }

  // 本地缓存的用户对象
  function user() {
    try { return JSON.parse(localStorage.getItem(USER_KEY) || 'null'); } catch (e) { return null; }
  }

  // 角色文案：admin→管理员 / team_admin→班组管理员 / 其他→普通用户
  function roleText(role) {
    return role === 'admin' ? '管理员' : (role === 'team_admin' ? '班组管理员' : '普通用户');
  }

  // DOM 选择助手（$$ 返回数组）
  function $(s, c) { return (c || document).querySelector(s); }
  function $$(s, c) { return Array.prototype.slice.call((c || document).querySelectorAll(s)); }

  // 班组隔离 query 助手：teamId 非空时为 path 追加 team_id
  function withTeam(path, teamId) {
    if (!teamId) return path;
    return path + (path.indexOf('?') >= 0 ? '&' : '?') + 'team_id=' + teamId;
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

  // 写入登录态（换号登录时清掉顶栏应用清单缓存，避免串用户残留；接口回来后会重建）
  function setAuth(token, userObj) {
    localStorage.removeItem(APPS_KEY);
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(USER_KEY, JSON.stringify(userObj || {}));
  }

  // 退出登录：通知后端注销 token（失败不阻塞本地清理）；仅供顶栏退出按钮内部调用，不导出
  async function logout() {
    try { await api('/api/v1/auth/logout', { method: 'POST' }); } catch (e) { /* 忽略，本地照常清理 */ }
    clearAuth();
    location.href = '/login.html';
  }

  // 登录门控：无 token 或 token 已过期（网页端短时效）直接清登录态跳登录页
  function requireAuth() {
    if (!localStorage.getItem(TOKEN_KEY) || isTokenExpired()) {
      clearAuth();
      location.replace('/login.html');
      return false;
    }
    return true;
  }

  // 统一顶部导航：左侧 Logo + 常驻导航（工作台 + 按权限下发的应用，顺序同工作台宫格——同取 /api/v1/app/list，
  // 无权限的应用不下发即不显示；本地缓存 shade_apps 先行渲染避免闪烁，接口回来后校正）；
  // 右侧「管理」（仅 admin 可见）+ 用户区 + 退出
  var APP_NAV = { // 应用 → 网页版落地页（app_key 与 sys_app 种子一致；顶栏导航与工作台宫格落地页共用此单一来源，无网页版的应用不收录）
    'call-me': { key: 'callme', href: '/callme.html' },
    'quiz': { key: 'quiz', href: '/quiz.html' },
    'work-log': { key: 'worklog', href: '/worklog.html' },
    'safe-day': { key: 'safeday', href: '/safeday.html' },
    'kvm': { key: 'kvm', href: '/kvm.html' },
    'client': { key: 'client', href: '/client.html' },
  };
  function cachedApps() {
    try { return JSON.parse(localStorage.getItem(APPS_KEY) || 'null'); } catch (e) { return null; }
  }
  function renderLeftNav(active) {
    var items = [{ key: 'index', name: '工作台', href: '/index.html' }];
    (cachedApps() || []).forEach(function (a) {
      var nav = APP_NAV[a.app_key];
      if (nav && a.terminal !== 'mobile') items.push({ key: nav.key, name: a.name, href: nav.href });
    });
    return items.map(function (it) {
      return it.key === active
        ? '<span class="topbar-item on">' + esc(it.name) + '</span>'
        : '<a class="topbar-item" href="' + it.href + '">' + esc(it.name) + '</a>';
    }).join('');
  }
  function topbar(opts) {
    opts = opts || {};
    var active = opts.active || 'index';
    var u = user() || {};
    var name = u.nickname || u.username || '班组成员';
    var isAdmin = u.role === 'admin';
    var icon = window.Shade.icon;

    // 左侧常驻导航：当前页渲染为纯文本激活态（无链接），其余为可点链接
    var leftNav = renderLeftNav(active);

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
        '<nav class="topbar-nav" id="topNavApps">' + leftNav + '</nav>' +
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

    // 拉取最新应用清单校正左侧导航（权限 / 排序变化即时生效；失败保留缓存渲染）
    api('/api/v1/app/list').then(function (r) {
      var list = (r.data && r.data.list) || [];
      localStorage.setItem(APPS_KEY, JSON.stringify(list));
      var nav = document.getElementById('topNavApps');
      if (nav) nav.innerHTML = renderLeftNav(active);
    }).catch(function () { /* 保留缓存渲染 */ });
  }

  // 顶栏用户资料刷新：成功更新 #miniAvatar/#topName 并回调 onUser(data)；失败回退本地缓存同样更新并回调
  function refreshUser(onUser) {
    function paint(u) {
      if (!u) return;
      var name = u.nickname || u.username || '班组成员';
      var av = document.getElementById('miniAvatar');
      var nm = document.getElementById('topName');
      if (av) av.textContent = name.charAt(0);
      if (nm) nm.textContent = name;
    }
    return api('/api/v1/user/profile').then(function (r) {
      paint(r.data);
      if (onUser) onUser(r.data);
    }).catch(function () {
      var cached = user();
      paint(cached);
      if (onUser) onUser(cached);
    });
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
    const iconName = type === 'success' ? 'check-circle' : (type === 'error' ? 'warn' : 'info-circle');
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

  /* ================= 通用弹层（可叠层，Esc / 点遮罩关闭） ================= */
  /* 样式 .mask / .modal / .m-scroll / .m-head / .m-x 由各页 CSS 提供（worklog / quiz / callme 同套口径） */
  var modalStack = [];
  function mHead(title) {
    return '<div class="m-head"><span class="m-title">' + esc(title) + '</span>'
      + '<button class="m-x">' + window.Shade.icon('close', 14) + '</button></div>';
  }
  function modal(html, opts) {
    opts = opts || {};
    var mask = document.createElement('div');
    mask.className = 'mask';
    var box = document.createElement('div');
    box.className = 'modal';
    if (opts.width) box.style.width = opts.width + 'px';
    /* 内容包一层滚动容器：外壳 overflow:hidden 保圆角，滚动条内凹不贴角 */
    box.innerHTML = '<div class="m-scroll">' + html + '</div>';
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
    mask.addEventListener('mousedown', function (e) { if (e.target === mask) inst.close(); });
    modalStack.push(inst);
    $$('.m-x', box).forEach(function (b) { b.addEventListener('click', inst.close); });
    return inst;
  }
  /* Esc 关顶层弹层，全页面统一注册一份；页面可给 Shade.escGuard 赋值优先拦截函数
     （返回 true 表示已消费本次 Esc，不再关弹层）——如 worklog 大图层需先于弹层响应 Esc */
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    var guard = window.Shade && window.Shade.escGuard;
    if (typeof guard === 'function' && guard()) return;
    var top = modalStack[modalStack.length - 1];
    if (top) top.close();
  });

  /* 通用确认弹层（叠层）：opts: { title, content, confirmText, danger }；返回 Promise<boolean>，
     遮罩 / Esc / 取消均视为否；.m-head/.m-foot 样式由各页 CSS 提供 */
  function confirm(opts) {
    return new Promise(function (resolve) {
      var done = false;
      function finish(v) { if (!done) { done = true; resolve(v); } }
      var html = mHead(opts.title)
        + '<div style="font-size:13px;line-height:1.8">' + esc(opts.content) + '</div>'
        + '<div class="m-foot"><button class="btn btn-sm" data-a="no">取消</button>'
        + '<button class="btn btn-sm ' + (opts.danger ? 'btn-danger' : 'btn-accent') + '" data-a="yes">' + esc(opts.confirmText || '确定') + '</button></div>';
      var inst = modal(html, { width: 420, onClose: function () { finish(false); } });
      $('[data-a="no"]', inst.box).addEventListener('click', inst.close);
      $('[data-a="yes"]', inst.box).addEventListener('click', function () { finish(true); inst.close(); });
    });
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

  /* 页签滑动指示条跟随：tab 为当前激活页签元素（null 时收起指示条），ind 为指示条元素；
     instant=true 首帧/窗口缩放直接落位，避免从左侧滑入的残影 */
  function moveTabInd(tab, ind, instant) {
    if (!tab) { ind.style.width = '0'; return; }
    if (instant) {
      ind.style.transition = 'none';
      ind.style.left = tab.offsetLeft + 'px';
      ind.style.width = tab.offsetWidth + 'px';
      void ind.offsetHeight; // 强制回流后恢复过渡
      ind.style.transition = '';
      return;
    }
    ind.style.left = tab.offsetLeft + 'px';
    ind.style.width = tab.offsetWidth + 'px';
  }

  /* 「未分配班组」整页空态提示卡：填充 el（占位容器，初始 hidden）并显示；页面其余区块的隐藏由页面自行处理 */
  function renderNoTeam(el) {
    el.hidden = false;
    el.innerHTML = '<div class="empty">' + window.Shade.icon('usergroup', 44) +
      '<p><b>未分配班组</b></p>' +
      '<p style="font-size:12px">你的账号尚未加入任何班组，暂无可查看的数据。<br>请联系超级管理员分配班组后再使用。</p></div>';
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
  // 弹出列表常驻 document.body：容器 innerHTML 重建场景需先调返回实例的 destroy()（close 之上移除弹层节点），
  // 否则触发器已随容器销毁而弹层成孤儿残留。
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
    /* 销毁实例：close 之上把弹出列表从 body 移除（容器重建前先调，防 .dd-pop 孤儿残留） */
    function destroy() {
      close();
      pop.remove();
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
    return { el: wrap, sync: sync, close: close, destroy: destroy };
  }

  /* 超管班组切换器统一装配：role!=='admin' 直接返回 undefined；GET /admin/teams 过滤启用班组渲染 options
     （allowAll 时前置「全部班组」value=all 且默认 'all'），localStorage 回读校验持久化，dropdown.sync() 后显示
     showEl（元素带 hidden 属性则 hidden=false，否则 style.display=''）；change 持久化并回调 onChange(value)
     （allowAll 时 value 为字符串，否则为数字 id）。返回 { getTeamId } 供页面取初始生效值 */
  async function setupTeamSel(opts) {
    var me = user() || {};
    if (me.role !== 'admin') return;
    try {
      var r = await api('/api/v1/admin/teams');
      var list = ((r.data && r.data.list) || []).filter(function (t) { return t.status === 1; });
      if (!list.length && !opts.allowAll) return;
      var sel = opts.select;
      sel.innerHTML = (opts.allowAll ? '<option value="all">全部班组</option>' : '')
        + list.map(function (t) { return '<option value="' + t.id + '">' + esc(t.name) + '</option>'; }).join('');
      var val;
      if (opts.allowAll) {
        var savedAll = localStorage.getItem(opts.storageKey) || 'all';
        val = (savedAll === 'all' || list.some(function (t) { return String(t.id) === savedAll; })) ? savedAll : 'all';
      } else {
        var saved = Number(localStorage.getItem(opts.storageKey)) || 0;
        val = list.some(function (t) { return t.id === saved; }) ? saved : list[0].id;
      }
      sel.value = String(val);
      localStorage.setItem(opts.storageKey, String(val));
      opts.dropdown.sync();
      var showEl = opts.showEl;
      if (showEl) {
        if (showEl.hidden) showEl.hidden = false;
        else showEl.style.display = '';
      }
      sel.addEventListener('change', function () {
        val = opts.allowAll ? this.value : (Number(this.value) || 0);
        localStorage.setItem(opts.storageKey, String(this.value));
        if (opts.onChange) opts.onChange(val);
      });
      return { getTeamId: function () { return val; } };
    } catch (e) { /* 班组列表拉取失败：不显示切换器，由后端落默认班组出数 */ }
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
    apiRaw: apiRaw,
    saveBlob: saveBlob,
    user: user,
    roleText: roleText,
    setAuth: setAuth,
    requireAuth: requireAuth,
    isTokenExpired: isTokenExpired,
    topbar: topbar,
    refreshUser: refreshUser,
    toast: toast,
    reveal: reveal,
    esc: esc,
    fmtDate: fmtDate,
    dropdown: dropdown,
    modal: modal,
    mHead: mHead,
    confirm: confirm,
    moveTabInd: moveTabInd,
    renderNoTeam: renderNoTeam,
    setupTeamSel: setupTeamSel,
    withTeam: withTeam,
    APP_NAV: APP_NAV,
    $: $,
    $$: $$,
  });
})();
