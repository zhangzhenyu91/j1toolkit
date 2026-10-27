<template>
  <!-- 登录页/分享页走无壳布局，在线文档满铺，其余页面为顶栏 + 侧导航壳 -->
  <router-view v-if="isBare" />
  <div v-else class="app-shell">
    <header class="topbar">
      <div class="brand">
        <div class="brand-mark">壹</div>
        <span>Shade 壹匣 · 管理台</span>
      </div>
      <div class="spacer"></div>
      <span v-if="displayName" class="user">{{ displayName }}</span>
      <t-button size="small" variant="outline" :loading="loggingOut" @click="onLogout">
        退出
      </t-button>
    </header>
    <div class="main-row">
      <nav class="sidenav">
        <router-link to="/">
          <span class="nav-dot"></span>
          <span>工作台</span>
        </router-link>
        <router-link v-for="item in navItems" :key="item.to" :to="item.to">
          <span class="nav-dot"></span>
          <span>{{ item.label }}</span>
        </router-link>
      </nav>
      <main class="page">
        <router-view />
      </main>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { currentUser, getProfile, isAdmin, isLoggedIn, logout as apiLogout } from './api.js';
import { ADMIN_NAV_ITEMS, NAV_ITEMS } from './features/core/app-catalog.js';

const route = useRoute();
const router = useRouter();
const loggingOut = ref(false);

// 侧导航：业务应用固定条目 + 仅超级管理员可见的管理入口（微信智能应答/应用日志/管理配置）
const navItems = computed(() => (isAdmin() ? [...NAV_ITEMS, ...ADMIN_NAV_ITEMS] : NAV_ITEMS));

// 无壳布局：登录页、公开分享页（bare）与 ONLYOFFICE 满铺承载页（full）
const isBare = computed(() => route.meta.bare === true || route.meta.full === true);

// 顶栏用户：展示登录返回的资料（nickname 优先，回落账号）
const displayName = computed(() => {
  const u = currentUser();
  return (u && (u.nickname || u.username)) || '';
});

// 登录态下启动刷新一次用户资料（GET /api/v1/user/profile）；
// 失败真实提示（401 已由 UNAUTHORIZED_EVENT 统一跳登录，不重复弹）。
onMounted(() => {
  if (!isLoggedIn()) return;
  getProfile().catch((err) => {
    if (err && err.status === 401) return;
    MessagePlugin.error((err && err.message) || '用户资料刷新失败');
  });
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
