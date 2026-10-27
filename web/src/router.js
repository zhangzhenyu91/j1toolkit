import { createRouter, createWebHashHistory } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { UNAUTHORIZED_EVENT, currentUser, isAdmin, isLoggedIn } from './api.js';
import { normalizeLegacyPath } from './features/core/legacy-urls.js';

// hash 路由：静态托管无需服务端 fallback，整体挂到 Go 服务（WithSPA）下。
// 旧 Node 网页端 *.html 地址由 main.js 启动跳转与守卫双重归一（见 features/core/legacy-urls.js）。
const routes = [
  {
    path: '/login',
    name: 'login',
    meta: { bare: true, title: '登录' },
    component: () => import('./views/LoginView.vue'),
  },
  {
    path: '/',
    name: 'workbench',
    meta: { title: '工作台' },
    component: () => import('./views/WorkbenchView.vue'),
  },
  // ---- 业务应用（工作台 app list 落地页）----
  {
    path: '/worklog',
    name: 'worklog',
    meta: { title: '出工日志' },
    component: () => import('./views/WorklogView.vue'),
  },
  {
    path: '/teamdata',
    name: 'teamdata',
    meta: { title: '班组数据' },
    component: () => import('./views/TeamdataView.vue'),
  },
  {
    path: '/treehazard',
    name: 'treehazard',
    meta: { title: '树障隐患' },
    component: () => import('./views/TreehazardView.vue'),
  },
  {
    path: '/crossing',
    name: 'crossing',
    meta: { title: '交跨记录' },
    component: () => import('./views/CrossingView.vue'),
  },
  {
    path: '/netdisk',
    name: 'netdisk',
    meta: { title: '团队网盘' },
    component: () => import('./views/NetdiskView.vue'),
  },
  {
    // ONLYOFFICE 承载页：满铺（无顶栏/侧导航壳）
    path: '/office',
    name: 'office',
    meta: { title: '在线文档', full: true },
    component: () => import('./views/OfficeView.vue'),
  },
  {
    path: '/safeday',
    name: 'safeday',
    meta: { title: '安全日' },
    component: () => import('./views/SafedayView.vue'),
  },
  {
    path: '/quiz',
    name: 'quiz',
    meta: { title: '刷题练习' },
    component: () => import('./views/QuizView.vue'),
  },
  {
    path: '/callme',
    name: 'callme',
    meta: { title: 'Call Me' },
    component: () => import('./views/CallmeView.vue'),
  },
  {
    path: '/kvm',
    name: 'kvm',
    meta: { title: 'KVM 远程' },
    component: () => import('./views/KvmView.vue'),
  },
  // 文件传输复用 KVM 视图（旧入口地址保留）
  { path: '/file-transfer', redirect: '/kvm' },
  // ---- 管理类入口（仅超级管理员）----
  {
    path: '/wechat-answer',
    name: 'wechat-answer',
    meta: { title: '微信智能应答', adminOnly: true },
    component: () => import('./views/WechatAnswerView.vue'),
  },
  {
    path: '/app-logs',
    name: 'app-logs',
    meta: { title: '应用日志', adminOnly: true },
    component: () => import('./views/AppLogsView.vue'),
  },
  {
    path: '/admin/config',
    name: 'admin-config',
    meta: { title: '管理-应用配置', adminOnly: true },
    component: () => import('./views/AdminConfigView.vue'),
  },
  {
    // 管理员账号页：仅超级管理员
    path: '/admin/accounts',
    name: 'admin-accounts',
    meta: { title: '管理员账号', adminOnly: true },
    component: () => import('./views/AdminAccountsView.vue'),
  },
  ...shareRoutes(),
  { path: '/:pathMatch(.*)*', redirect: '/' },
];

// shareRoutes 网盘公开分享页（免登录独立页）：
// 旧链接形态 /share.html#/s/<share_id> 经 legacy-urls 跳转后落到 /s/:id。
function shareRoutes() {
  const component = () => import('./views/ShareView.vue');
  return [
    {
      path: '/share',
      name: 'share',
      meta: { bare: true, public: true, title: '文件分享' },
      component,
    },
    {
      // :id 与 ShareView 的 route.params.id 读取口径一致（旧链接 /share.html#/s/<id> 落此）
      path: '/s/:id',
      name: 'share-by-id',
      meta: { bare: true, public: true, title: '文件分享' },
      component,
    },
    {
      path: '/share/:id',
      name: 'share-with-id',
      meta: { bare: true, public: true, title: '文件分享' },
      component,
    },
  ];
}

const router = createRouter({
  // base 显式传 '/'：缺省时 vue-router 会把 pathname+search 拼进 hash 基址
  // （旧 *.html 直链会污染初始路由），始终以 '#/' 为唯一基址。
  history: createWebHashHistory('/'),
  routes,
});

// 登录守卫：未登录进登录页（保留回跳地址）；adminOnly 路由仅超级管理员可进；
// 旧 *.html 路径二次归一（兼容手输/直链）。分享页 public 免登录。
router.beforeEach((to) => {
  // 旧地址兜底：hash 里仍是 *.html 时归一为对应路由，query/hash 保留
  if (/\.html$/i.test(to.path)) {
    return { path: normalizeLegacyPath(to.path), query: to.query, hash: to.hash };
  }
  const open = to.meta.public === true || to.name === 'login';
  if (!open) {
    if (!isLoggedIn()) {
      return { name: 'login', query: { redirect: to.fullPath } };
    }
    if (to.meta.adminOnly && !isAdmin()) {
      return { name: 'workbench' };
    }
    return true;
  }
  // 已登录访问登录页 → 直接进工作台
  if (to.name === 'login' && isLoggedIn()) {
    return { name: 'workbench' };
  }
  return true;
});

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · Shade 壹匣` : 'Shade 壹匣';
});

// 会话失效（api.js 收到 401 清会话后广播）：真实提示并跳登录页保留回跳
window.addEventListener(UNAUTHORIZED_EVENT, () => {
  const cur = router.currentRoute.value;
  if (cur.name === 'login' || cur.meta.public) return;
  if (currentUser()) return; // 本地会话仍在（非本页触发的失效）不打断
  MessagePlugin.warning('登录状态已失效，请重新登录');
  router.push({ name: 'login', query: { redirect: cur.fullPath } });
});

export default router;
