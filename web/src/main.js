import { createApp } from 'vue';
import TDesign from 'tdesign-vue-next';
import 'tdesign-vue-next/es/style/index.css';
import App from './App.vue';
import router from './router.js';
import { legacyRedirectTarget } from './features/core/legacy-urls.js';
import './features/core/legacy-theme.css';
import './styles.css';

// 品牌图标（web/public/favicon.svg，与旧版同资源）：入口 index.html 不在本次改动范围，运行时补链
if (!document.querySelector('link[rel="icon"]')) {
  const link = document.createElement('link');
  link.rel = 'icon';
  link.type = 'image/svg+xml';
  link.href = '/favicon.svg';
  document.head.appendChild(link);
}

// 旧 Node 网页端 *.html 直链（如 /worklog.html?date=2026-10、/share.html#/s/<id>）
// 先归一为 SPA hash 路由（保留 query/hash）再挂载应用；非旧地址直接启动。
const legacyTarget = legacyRedirectTarget(window.location);
if (legacyTarget) {
  window.location.replace(legacyTarget);
} else {
  createApp(App).use(TDesign).use(router).mount('#app');
}
