// 旧 Node 网页端 *.html 地址 → Vue SPA 路由映射（迁移 server/public/*.html 落地页）。
// Go 侧 WithSPA 对未知路径回落 index.html，故 *.html 会先进 SPA，由本文件在启动时
// location.replace 到 hash 路由；query 与 hash 一律保留：
// - query 原样进入 hash 路由的 query（如 /office.html?space=... → /#/office?space=...）；
// - hash 已是 '#/...' 形态时（分享页 /share.html#/s/<id>）直接作为路由使用；
// - 旧工作台 worklog.html#dict 特例保留 → 跳班组数据（旧页同口径）。
// 未登记的 *.html 统一回工作台，不落到 404。

const LEGACY_ROUTES = {
  'index.html': '/',
  'login.html': '/login',
  'worklog.html': '/worklog',
  'teamdata.html': '/teamdata',
  'treehazard.html': '/treehazard',
  'crossing.html': '/crossing',
  'netdisk.html': '/netdisk',
  'office.html': '/office',
  'share.html': '/share',
  'callme.html': '/callme',
  'quiz.html': '/quiz',
  'safeday.html': '/safeday',
  'kvm.html': '/kvm',
  'admin.html': '/admin/config',
  // 工作水印网页版落地 /wmadd（迁移 server/public/wmadd.html）
  'wmadd.html': '/wmadd',
};

// legacyRouteFor 按文件名取 SPA 路由路径；未知 *.html 回工作台。
export function legacyRouteFor(file) {
  const key = String(file || '').toLowerCase();
  return Object.prototype.hasOwnProperty.call(LEGACY_ROUTES, key) ? LEGACY_ROUTES[key] : '/';
}

// normalizeLegacyPath 站内路径里的 *.html 归一为 SPA 路由路径（query/hash 原样保留）。
// 非 *.html 或非站内绝对路径原样返回。用于登录回跳、back 参数等二次跳转场景。
export function normalizeLegacyPath(p) {
  if (typeof p !== 'string' || p.charAt(0) !== '/') return p;
  const m = /^\/([^/?#]*)\.html([?#].*)?$/i.exec(p);
  if (!m) return p;
  const file = m[1].toLowerCase() + '.html';
  const rest = m[2] || '';
  if (file === 'worklog.html' && rest.indexOf('#dict') === 0) {
    // 旧工作台 #dict → 班组数据（旧页 location.replace('/teamdata.html') 同口径）
    return '/teamdata' + rest.slice('#dict'.length);
  }
  return legacyRouteFor(file) + rest;
}

// legacyRedirectTarget 启动时的旧地址跳转目标（'/' 起点的 hash 路由 URL）；非旧 *.html 返回 null。
export function legacyRedirectTarget(loc) {
  const pathname = (loc && loc.pathname) || '';
  const m = /\/([^/]+)\.html$/i.exec(pathname);
  if (!m) return null;
  const file = m[1].toLowerCase() + '.html';
  const search = (loc && loc.search) || '';
  const hash = (loc && loc.hash) || '';
  // hash 已是具体 SPA 路由（如 /share.html#/s/<id>）→ 去掉 .html 路径、保留 hash 路由与 query
  const hashRoute = hash.indexOf('#/') === 0 ? hash.slice(1) : '';
  const hashPath = hashRoute.split('?')[0].split('#')[0] || '/';
  if (hashPath !== '/') {
    return '/#' + hashRoute + (search && hashRoute.indexOf('?') < 0 ? search : '');
  }
  if (file === 'worklog.html' && hash === '#dict') {
    return '/#/teamdata' + search;
  }
  // 空路由 hash（''、'#/'——含 vue-router 启动时预置的 '#/'）不作为目标路由，按 *.html 映射、
  // query 保留；其余纯锚点 hash 原样附在目标后。
  const frag = hash && hash !== '#' && hashRoute === '' ? hash : '';
  return '/#' + legacyRouteFor(file) + search + frag;
}
