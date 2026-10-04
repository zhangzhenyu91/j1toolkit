<template>
  <div class="app-shell">
    <TitleBar />
    <div class="main-row">
      <div class="nav-rail">
        <div class="brand">壹匣</div>
        <div class="nav-item" :class="{active: current === 'home'}" @click="current = 'home'">
          <div class="tile"><ShadeIcon name="app" :size="22" /></div><span>首页</span>
        </div>
        <div v-for="app in apps" :key="app.id" class="nav-item"
             :class="{active: current === app.id}" @click="current = app.id">
          <div class="tile"><ShadeIcon :name="navIcon(app.id)" :size="22" /></div>
          <span>{{ app.name }}</span>
        </div>
        <div class="nav-spacer"></div>
        <div class="nav-item" :class="{active: current === 'settings'}" @click="current = 'settings'">
          <div class="tile"><ShadeIcon name="setting" :size="22" /></div><span>设置</span>
        </div>
      </div>
      <div class="page">
        <t-alert v-if="coreError" theme="error" style="margin-bottom:14px">
          <template #title>Python 核心未运行，应用功能不可用</template>
          <template #default>
            <div style="white-space:pre-wrap; word-break:break-all">{{ coreError }}</div>
          </template>
          <template #operation>
            <t-button size="small" theme="primary" @click="restartCore">重启核心</t-button>
          </template>
        </t-alert>
        <!-- 核心冷启动期间整屏 splash（核心解包+握手要数秒，空白会被误认为卡死） -->
        <div v-if="!loaded" class="splash">
          <img class="splash-logo" :src="iconUrl" alt="" draggable="false">
          <div class="splash-name">Shade 壹匣 - 内网</div>
          <t-loading size="small" text="正在启动核心服务…" />
          <div class="muted">首次启动较慢属正常（核心解包运行环境）</div>
        </div>
        <!-- keep-alive：切页不销毁组件，任务进度/日志/表单状态全保留（已踩坑：v-if 会重置）；
             页面用 onActivated 做回显刷新 -->
        <keep-alive v-else>
          <component :is="pageComp" @goto="current = $event" />
        </keep-alive>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { call } from './api'
import TitleBar from './components/TitleBar.vue'
import ShadeIcon from './components/ShadeIcon.vue'
import iconUrl from './assets/icon.png'
import HomePage from './pages/HomePage.vue'
import DailySyncPage from './pages/DailySyncPage.vue'
import PcdExportPage from './pages/PcdExportPage.vue'
import QuizSearchPage from './pages/QuizSearchPage.vue'
import SettingsPage from './pages/SettingsPage.vue'

const PAGE_MAP = { home: HomePage, settings: SettingsPage,
                   daily_sync: DailySyncPage, pcd_export: PcdExportPage,
                   quiz_search: QuizSearchPage }
// 导航图标：壹匣线性图标库同名（见 components/ShadeIcon.vue）；未知应用回退宫格
const NAV_ICONS = { daily_sync: 'refresh', pcd_export: 'download', quiz_search: 'search' }
function navIcon(id) { return NAV_ICONS[id] || 'app' }

const apps = ref([])
const loaded = ref(false)
const current = ref('home')
const coreError = ref('')

const pageComp = computed(() => PAGE_MAP[current.value] || HomePage)

async function loadApps() {
  coreError.value = ''
  try {
    const list = await call('getApps')
    apps.value = [...list].sort((a, b) => (a.order - b.order))
  } catch (e) {
    coreError.value = String(e.message || e)
    try {
      const h = await window.core.health()
      if (h && h.command) coreError.value += '\n启动命令：' + h.command
      if (h && h.lastStderr && h.lastStderr.length) {
        coreError.value += '\n核心报错：\n' + h.lastStderr.slice(-15).join('\n')
      }
    } catch (_e) { /* 忽略 */ }
  } finally {
    loaded.value = true
  }
}

async function restartCore() {
  loaded.value = false
  await window.core.restartCore()
  setTimeout(loadApps, 1500)
}

onMounted(loadApps)
</script>
