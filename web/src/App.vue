<template>
  <!-- 登录页/分享页走无壳布局，在线文档满铺，其余页面为旧版左侧可折叠导航壳 -->
  <template v-if="isBare">
    <router-view />
    <!-- 免登录公开页（网盘分享）补全站备案页脚；登录页自放备案、在线文档满铺（旧 office.html data-beian="off"）不放 -->
    <footer v-if="route.meta.public" class="beian-foot">
      <a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener">晋ICP备2025063709号-2</a>
      <a
        href="https://beian.mps.gov.cn/#/query/webSearch?code=14118202000050"
        target="_blank"
        rel="noopener"
        ><img src="/public-security.png" alt="公安备案" />晋公网安备14118202000050号</a
      >
    </footer>
  </template>
  <div v-else class="app-shell">
    <!-- 窄屏抽屉开关（≤720px 显示；旧版 common.js topbar 同款） -->
    <button
      class="rail-mobile-toggle"
      type="button"
      aria-label="打开导航"
      :aria-expanded="drawerOpen ? 'true' : 'false'"
      aria-controls="shadeRail"
      @click="setDrawer(true)"
    >
      <LegacyIcon name="app" :size="18" />
      <span>导航</span>
    </button>
    <div class="rail-scrim" :hidden="!drawerOpen" @click="setDrawer(false)"></div>

    <!-- 左侧可折叠导航栏：展开 200px / 折叠 64px 图标态（偏好存 shade_nav_mini）/ 窄屏抽屉 -->
    <aside id="shadeRail" class="rail" @mouseover="onRailOver" @mouseout="onRailOut">
      <router-link class="rail-logo" to="/" data-tip="Shade 壹匣" aria-label="Shade 壹匣">
        <span class="logo-mark"><img src="/favicon.svg" alt="Shade 壹匣" /></span>
        <span class="rail-lw"><b>Shade 壹匣</b><span>班组数字化工具平台</span></span>
      </router-link>
      <button class="rail-fold" type="button" aria-label="收起 / 展开导航" @click="onFold">
        <LegacyIcon :name="navMini ? 'chevron-right' : 'chevron-left'" :size="13" />
      </button>

      <nav class="rail-nav" @focusin="onRailOver" @focusout="onRailOut">
        <!-- 已审核分组：组间按 APP_GROUPS 顺序，组内按后端 sort 稳定排序；空组不渲染 -->
        <div v-for="group in navGroups" :key="group.id" class="rail-group">
          <div class="rail-group-title">{{ group.label }}</div>
          <router-link
            v-for="item in group.items"
            :key="item.to"
            class="rail-item"
            :class="{ on: isNavActive(item) }"
            :to="item.to"
            :data-tip="item.label"
            :aria-label="item.label"
            @click="setDrawer(false)"
          >
            <LegacyIcon :name="item.icon || 'app'" :size="19" />
            <span>{{ item.label }}</span>
          </router-link>
        </div>
      </nav>

      <div class="rail-bottom">
        <!-- 用户卡：头像 + 昵称 + 角色徽章（与旧版 #miniAvatar/#topName/#railRole 同结构） -->
        <span class="rail-item rail-user" :data-tip="displayName" :aria-label="displayName">
          <span class="mini-avatar">{{ avatarChar }}</span>
          <span class="rail-uinfo">
            <b>{{ displayName }}</b>
            <i class="rail-role">{{ roleText }}</i>
          </span>
        </span>
        <button
          class="rail-item"
          type="button"
          data-tip="退出登录"
          aria-label="退出登录"
          :disabled="loggingOut"
          @click="onLogout"
        >
          <LegacyIcon name="logout" :size="19" />
          <span>{{ loggingOut ? '退出中…' : '退出登录' }}</span>
        </button>
      </div>
    </aside>

    <main class="page" ref="pageEl">
      <!-- 页入场：路由切换克制淡入落位静止（v-slot + path key：每次进入路由都重播，
           不只首次进入有效；查询串变化不重挂载，页内页签用 v-reanimate 重播；过渡定义见
           styles.css；reduced-motion 全局瞬时降级） -->
      <router-view v-slot="{ Component }">
        <Transition name="page-in" mode="out-in">
          <component :is="Component" :key="route.path" />
        </Transition>
      </router-view>
    </main>

    <!-- 全站备案页脚（旧 common.js 注入同文案链接）：壳外独立条、随内容区沉底，不占内容列宽度 -->
    <footer class="beian-foot">
      <a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener">晋ICP备2025063709号-2</a>
      <a
        href="https://beian.mps.gov.cn/#/query/webSearch?code=14118202000050"
        target="_blank"
        rel="noopener"
        ><img src="/public-security.png" alt="公安备案" />晋公网安备14118202000050号</a
      >
    </footer>

    <!-- 折叠态悬停气泡（贴栏右缘，纵向对齐图标中心） -->
    <div class="rail-tip" :hidden="!tipText" :style="{ top: tipTop }">{{ tipText }}</div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  currentUser,
  getProfile,
  isAdmin,
  isLoggedIn,
  listApps,
  logout as apiLogout,
} from './api.js';
import {
  ADMIN_NAV_ITEMS,
  APP_GROUPS,
  NAV_ITEMS,
  canonicalKey,
  groupForApp,
  metaForApp,
  routeForApp,
  sortBySort,
} from './features/core/app-catalog.js';
import LegacyIcon from './features/core/LegacyIcon.vue';

const route = useRoute();
const router = useRouter();
const loggingOut = ref(false);

// 侧导航分组：工作台固定项 + 业务应用（按 /api/v1/app/list 权限下发校正）+ 管理入口（仅超级管理员）。
// 组间按 APP_GROUPS 顺序，组内一律 sortBySort 按后端 sort 稳定排序；空组不渲染，只显示实际可进入项。
function buildNavGroups(bizItems, adminItems) {
  const buckets = {};
  const push = (gid, item) => {
    if (!gid) return;
    (buckets[gid] || (buckets[gid] = [])).push(item);
  };
  // 工作台固定入口归「工作概览」
  push('overview', { to: '/', label: '工作台', icon: 'app' });
  sortBySort(bizItems).forEach((it) => push(groupForApp(it.code), it));
  // 管理入口（已审核归入「管理设置」；静态页签无后端 sort，sortBySort 保持既有相对顺序）
  sortBySort(adminItems).forEach((it) => push('admin', it));
  return APP_GROUPS.map((g) => ({ id: g.id, label: g.label, items: buckets[g.id] || [] })).filter(
    (g) => g.items.length
  );
}

const navGroups = ref(buildNavGroups(NAV_ITEMS, isAdmin() ? ADMIN_NAV_ITEMS : []));

// 无壳布局：登录页、公开分享页（bare）与 ONLYOFFICE 满铺承载页（full）
const isBare = computed(() => route.meta.bare === true || route.meta.full === true);

// 顶栏/用户卡：展示登录返回的资料（nickname 优先，回落账号）；
// currentUser 为响应式引用，登录、刷新资料与退出后界面同步更新（会话响应式修复保持不变）
const displayName = computed(() => {
  const u = currentUser();
  return (u && (u.nickname || u.username)) || '班组成员';
});
const avatarChar = computed(() => (displayName.value || '班').charAt(0));
const roleText = computed(() => {
  const u = currentUser();
  const role = u && u.role;
  return role === 'admin' ? '管理员' : role === 'team_admin' ? '班组管理员' : '普通用户';
});

// ---- 左栏折叠 / 窄屏抽屉（偏好存 localStorage shade_nav_mini，与旧版同键）----
const NAV_MINI_KEY = 'shade_nav_mini';
const navMini = ref(false);
const drawerOpen = ref(false);

function isMobileNav() {
  return window.matchMedia('(max-width: 720px)').matches;
}
function syncBodyClasses() {
  const body = document.body;
  body.classList.toggle('has-rail', !isBare.value);
  body.classList.toggle('nav-mini', !isBare.value && navMini.value);
  body.classList.toggle('nav-drawer-open', !isBare.value && drawerOpen.value);
}
function setDrawer(open) {
  if (!isMobileNav()) open = false;
  drawerOpen.value = open;
}
function onFold() {
  if (isMobileNav()) {
    setDrawer(false);
    return;
  }
  navMini.value = !navMini.value;
  try {
    localStorage.setItem(NAV_MINI_KEY, navMini.value ? '1' : '0');
  } catch (e) {
    // localStorage 不可用时忽略
  }
}
function onResize() {
  if (!isMobileNav()) setDrawer(false);
}

// 折叠态悬停气泡：单实例 + 栏上事件委托（仅图标态显示）
const tipText = ref('');
const tipTop = ref('0px');
function showTip(el) {
  if (!navMini.value || isMobileNav()) return;
  const text = el.getAttribute('data-tip');
  if (!text) return;
  const r = el.getBoundingClientRect();
  tipText.value = text;
  tipTop.value = Math.round(r.top + r.height / 2) + 'px';
}
function hideTip() {
  tipText.value = '';
}
function tipTarget(e) {
  const t = e.target && e.target.closest ? e.target.closest('[data-tip]') : null;
  return t || null;
}
function onRailOver(e) {
  const t = tipTarget(e);
  if (t) showTip(t);
}
function onRailOut(e) {
  const t = tipTarget(e);
  if (t && !(e.relatedTarget && t.contains(e.relatedTarget))) hideTip();
}

// Esc 关闭窄屏抽屉（与旧版 Shade.closeDrawer 同行为）
function onKeydown(e) {
  if (e.key === 'Escape') setDrawer(false);
}

// 激活态：精确匹配或前缀匹配（/admin/config 等子路由高亮所属入口）
function isNavActive(item) {
  const p = route.path;
  if (item.to === '/') return p === '/';
  return p === item.to || p.startsWith(item.to + '/');
}

// 登录态下启动刷新一次用户资料（GET /api/v1/user/profile）；
// 失败真实提示（401 已由 UNAUTHORIZED_EVENT 统一跳登录，不重复弹）。
onMounted(() => {
  try {
    navMini.value = localStorage.getItem(NAV_MINI_KEY) === '1';
  } catch (e) {
    // localStorage 不可用时忽略
  }
  syncBodyClasses();
  window.addEventListener('resize', onResize);
  window.addEventListener('keydown', onKeydown);
  window.addEventListener('scroll', onSbScroll, true);
  window.addEventListener('mousemove', onSbMove, true);
  window.addEventListener('mouseout', onSbOut);
  if (!isLoggedIn()) return;
  getProfile().catch((err) => {
    if (err && err.status === 401) return;
    MessagePlugin.error((err && err.message) || '用户资料刷新失败');
  });
  // 常驻导航按权限校正：/api/v1/app/list 下发什么显示什么（缓存文案先渲染避免闪烁，失败保留默认）；
  // 组内顺序按后端 sort 动态排序（后端已 ORDER BY sort,id，此处对合并/追加后的列表再稳定排序），
  // 分组归属与组间顺序见 app-catalog.js（APP_GROUPS / groupForApp），空组不渲染
  listApps()
    .then((list) => {
      const items = [];
      (list || []).forEach((a) => {
        if (a.terminal === 'mobile') return;
        const code = canonicalKey(a.code || a.app_key || '');
        const meta = metaForApp(code);
        if (meta && meta.adminOnly) return;
        const to = routeForApp(code);
        if (!to) return;
        // 展示名以目录为准（定稿展示名，如「安全日活动」「远程控制」），后端 name 仅兜底
        items.push({
          code,
          to,
          label: (meta && meta.name) || a.name || code,
          icon: a.icon || (meta && meta.icon) || 'app',
          sort: a.sort,
        });
      });
      navGroups.value = buildNavGroups(items, isAdmin() ? ADMIN_NAV_ITEMS : []);
    })
    .catch(() => {
      /* 应用清单拉取失败：保留本地目录渲染（落地路由为本页真实路由） */
    });
});

// 路由切换回到内容区顶部：.page 为内部滚动容器，vue-router scrollBehavior 不作用于它；
// 仅路径切换复位（查询串变化保留滚动位置）
const pageEl = ref(null);
watch(
  () => route.path,
  () => {
    if (pageEl.value) pageEl.value.scrollTop = 0;
  }
);

// ---- 滚动条自动隐藏（styles.css .tk-sb-on 配合）：滚动中/悬停滚动条时显现，停止约 1s 后隐藏 ----
// scroll 不冒泡，捕获阶段收全部滚动容器（原生窗口与各 overflow 容器）；滚动条无独立事件，
// 靠 mousemove 判定指针是否停在滚动条条带（右/底约 16px）上，悬停期间保持显现、离开后再计时。
const SB_HIDE_MS = 1000;
const sbHideTimers = new WeakMap();
function sbShow(el) {
  if (!el || !el.classList) return;
  el.classList.add('tk-sb-on');
  const t = sbHideTimers.get(el);
  if (t) {
    clearTimeout(t);
    sbHideTimers.delete(el);
  }
}
function sbHideLater(el) {
  if (!el || !el.classList) return;
  const t = sbHideTimers.get(el);
  if (t) clearTimeout(t);
  sbHideTimers.set(
    el,
    setTimeout(() => {
      el.classList.remove('tk-sb-on');
      sbHideTimers.delete(el);
    }, SB_HIDE_MS)
  );
}
function sbTarget(node) {
  if (node === document) return document.documentElement;
  return node && node.nodeType === 1 ? node : null;
}
function sbOnBar(el, e) {
  const vScroll = el.scrollHeight > el.clientHeight;
  const hScroll = el.scrollWidth > el.clientWidth;
  if (!vScroll && !hScroll) return false;
  const r = el.getBoundingClientRect();
  return (vScroll && e.clientX >= r.right - 16) || (hScroll && e.clientY >= r.bottom - 16);
}
function onSbScroll(e) {
  const el = sbTarget(e.target);
  if (!el) return;
  sbShow(el);
  sbHideLater(el);
}
let sbHoverEl = null;
function onSbMove(e) {
  const el = sbTarget(e.target);
  if (el && sbOnBar(el, e)) {
    sbShow(el);
    sbHoverEl = el;
    return;
  }
  if (sbHoverEl) {
    sbHideLater(sbHoverEl);
    sbHoverEl = null;
  }
}
function onSbOut(e) {
  // 指针移出窗口（无落点元素）时对悬停中的滚动条补计时，避免停在条带上离窗后不再隐藏
  if (e && e.relatedTarget) return;
  if (sbHoverEl) {
    sbHideLater(sbHoverEl);
    sbHoverEl = null;
  }
}

watch([isBare, navMini, drawerOpen], syncBodyClasses);
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize);
  window.removeEventListener('keydown', onKeydown);
  window.removeEventListener('scroll', onSbScroll, true);
  window.removeEventListener('mousemove', onSbMove, true);
  window.removeEventListener('mouseout', onSbOut);
  document.body.classList.remove('has-rail', 'nav-mini', 'nav-drawer-open');
});

// 退出：调服务端注销（失败也清本地会话），再回登录页
async function onLogout() {
  loggingOut.value = true;
  try {
    await apiLogout();
  } finally {
    loggingOut.value = false;
  }
  router.push('/login');
}
</script>
