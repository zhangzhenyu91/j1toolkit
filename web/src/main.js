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

// 原生日期输入：点击输入框任意位置（含日期文字区域）即展开系统日期选择——旧页
// bindRangeInput 同口径（showPicker，不支持的浏览器/非用户手势时保留原生点击行为）；
// 委托到文档层，覆盖各页面与弹层的 input[type=date]，免得逐页绑定漏掉。
document.addEventListener('click', (e) => {
  const el = e.target && e.target.closest ? e.target.closest('input[type="date"]') : null;
  if (!el || typeof el.showPicker !== 'function') return;
  try {
    el.showPicker();
  } catch (err) {
    /* 浏览器拒绝（非用户手势等）时保留原生点击行为 */
  }
});

// 旧 Node 网页端 *.html 直链（如 /worklog.html?date=2026-10、/share.html#/s/<id>）
// 先归一为 SPA hash 路由（保留 query/hash）再挂载应用；非旧地址直接启动。
const legacyTarget = legacyRedirectTarget(window.location);
if (legacyTarget) {
  window.location.replace(legacyTarget);
} else {
  const app = createApp(App).use(TDesign).use(router);
  // v-reanimate：绑定值变化时强制重播元素上的 CSS 动画（页签/分组/管理页切换入场，
  // 不只首次进入有效）。用法：v-reanimate="tabKey"（值变化即重播）；
  // reduced-motion 由 legacy-theme.css 全局媒体查询统一降级，无需各页分支。
  app.directive('reanimate', {
    mounted(el) {
      restartAnimation(el);
    },
    updated(el, binding) {
      if (binding.value !== binding.oldValue) restartAnimation(el);
    },
  });
  app.mount('#app');
}

// restartAnimation 清空后强制回流再恢复，使 CSS animation 重新从头播放。
function restartAnimation(el) {
  if (!el || !el.style) return;
  el.style.animation = 'none';
  void el.offsetWidth;
  el.style.animation = '';
}
