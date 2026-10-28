<template>
  <!-- 登录页/分享页走无壳布局，在线文档满铺，其余页面为旧版左侧可折叠导航壳 -->
  <router-view v-if="isBare" />
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
        <router-link
          v-for="item in navItems"
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
        <!-- 管理入口：仅超级管理员可见（旧版「管理」单入口，现为四个管理页分列） -->
        <router-link
          v-for="item in adminItems"
          :key="item.to"
          class="rail-item"
          :class="{ on: isNavActive(item) }"
          :to="item.to"
          :data-tip="item.label"
          :aria-label="item.label"
          @click="setDrawer(false)"
        >
          <LegacyIcon :name="item.icon || 'setting'" :size="19" />
          <span>{{ item.label }}</span>
        </router-link>
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

    <main class="page">
      <!-- 页入场：路由切换克制淡入落位静止（过渡定义见 styles.css；reduced-motion 全局瞬时降级） -->
      <Transition name="page-in" mode="out-in">
        <router-view />
      </Transition>
    </main>

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
  NAV_ITEMS,
  canonicalKey,
  metaForApp,
  routeForApp,
} from './features/core/app-catalog.js';
import LegacyIcon from './features/core/LegacyIcon.vue';

const route = useRoute();
const router = useRouter();
const loggingOut = ref(false);

// 侧导航：工作台 + 业务应用（按 /api/v1/app/list 权限下发校正）；管理入口仅超级管理员可见
const navItems = ref([{ to: '/', label: '工作台', icon: 'app' }, ...NAV_ITEMS]);
const adminItems = computed(() => (isAdmin() ? ADMIN_NAV_ITEMS : []));

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
  if (!isLoggedIn()) return;
  getProfile().catch((err) => {
    if (err && err.status === 401) return;
    MessagePlugin.error((err && err.message) || '用户资料刷新失败');
  });
  // 常驻导航按权限校正：/api/v1/app/list 下发什么显示什么（缓存文案先渲染避免闪烁，失败保留默认）
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
          to,
          label: (meta && meta.name) || a.name || code,
          icon: a.icon || (meta && meta.icon) || 'app',
        });
      });
      navItems.value = [{ to: '/', label: '工作台', icon: 'app' }, ...items];
    })
    .catch(() => {
      /* 应用清单拉取失败：保留本地目录渲染（落地路由为本页真实路由） */
    });
});

watch([isBare, navMini, drawerOpen], syncBodyClasses);
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize);
  window.removeEventListener('keydown', onKeydown);
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
