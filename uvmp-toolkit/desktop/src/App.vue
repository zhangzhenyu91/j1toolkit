<template>
  <div class="app-shell" :class="{ 'is-maximized': maximized }">
    <TitleBar />
    <div class="main-row">
      <AppSidebar
        :current="current"
        :expanded-developer="expandedDeveloper"
        :favorites="favorites"
        @navigate="navigate"
        @toggle-developer="toggleDeveloper"
      />
      <div class="workspace">
        <div class="workspace-column">
          <PageHeader
            :title="meta.title"
            :desc="meta.desc"
            :show-favorite="Boolean(meta.business)"
            :favorite="favorites.includes(current)"
            :running-count="taskStatus.running"
            @toggle-favorite="toggleFavorite(current)"
          />
          <div class="page-scroll">
            <t-alert v-if="coreError" theme="error" style="margin-bottom: 14px">
              <template #title>Python 核心未运行，应用功能不可用</template>
              <template #default>
                <div style="white-space: pre-wrap; word-break: break-all">{{ coreError }}</div>
              </template>
              <template #operation>
                <t-button size="small" theme="primary" @click="restartCore">重启核心</t-button>
              </template>
            </t-alert>
            <div v-if="!loaded" class="splash">
              <img class="splash-logo" :src="iconUrl" alt="" draggable="false" />
              <div class="splash-name">Shade 壹匣 - 内网</div>
              <t-loading size="small" text="正在启动核心服务…" />
              <div class="muted">首次启动较慢属正常（核心解包运行环境）</div>
            </div>
            <!-- keep-alive：切页不销毁组件，任务进度/日志/表单状态全保留；
                 最大化只改壳层宽度，不重建页面。 -->
            <keep-alive v-else>
              <component
                :is="pageComp"
                @goto="navigate"
                @navigate="navigate"
                @restore="onRestore"
              />
            </keep-alive>
          </div>
        </div>
      </div>
    </div>
    <CommandPalette
      :visible="paletteVisible"
      :favorites="favorites"
      @close="paletteVisible = false"
      @navigate="onPaletteNavigate"
      @toggle-favorite="toggleFavorite"
    />
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, provide, reactive, ref } from 'vue';
import { abortAllJobs, call } from './api';
import TitleBar from './components/TitleBar.vue';
import AppSidebar from './components/AppSidebar.vue';
import PageHeader from './components/PageHeader.vue';
import CommandPalette from './components/CommandPalette.vue';
import HistoryPage from './pages/HistoryPage.vue';
import HomePage from './pages/HomePage.vue';
import DailySyncPage from './pages/DailySyncPage.vue';
import TreeSyncPage from './pages/TreeSyncPage.vue';
import CrossingSyncPage from './pages/CrossingSyncPage.vue';
import PcdExportPage from './pages/PcdExportPage.vue';
import QuizSearchPage from './pages/QuizSearchPage.vue';
import CourseAutoplayPage from './pages/CourseAutoplayPage.vue';
import DefectReviewPage from './pages/DefectReviewPage.vue';
import DevScriptPage from './pages/DevScriptPage.vue';
import ClusterPage from './pages/ClusterPage.vue';
import SettingsPage from './pages/SettingsPage.vue';
import iconUrl from './assets/icon.png';
import { navItem, toggleFavorite as toggleFavoriteID } from './lib/navigation.mjs';
import { taskStatus } from './lib/task-status.mjs';
import { requestRestore } from './lib/restore-bus.mjs';

const PAGE_MAP = {
  home: HomePage,
  history: HistoryPage,
  settings: SettingsPage,
  daily_sync: DailySyncPage,
  tree_sync: TreeSyncPage,
  crossing_sync: CrossingSyncPage,
  pcd_export: PcdExportPage,
  quiz_search: QuizSearchPage,
  course_autoplay: CourseAutoplayPage,
  defect_review: DefectReviewPage,
  dev_script: DevScriptPage,
  cluster: ClusterPage,
};

const FAVORITES_KEY = 'shell.favorites.v1';
const RECENT_KEY = 'shell.recent_tools.v1';
const DEV_KEY = 'shell.developer_expanded.v1';

function readJSON(key, fallback) {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(key) || 'null');
    return parsed == null ? fallback : parsed;
  } catch (_e) {
    return fallback;
  }
}

function readArray(key) {
  const value = readJSON(key, []);
  return Array.isArray(value) ? value : [];
}

const current = ref('home');
const loaded = ref(false);
const coreError = ref('');
const paletteVisible = ref(false);
const maximized = ref(false);
const expandedDeveloper = ref(window.localStorage.getItem(DEV_KEY) === '1');
const favorites = ref(readArray(FAVORITES_KEY));
const recent = ref(readArray(RECENT_KEY));
const meta = computed(() => navItem(current.value) || navItem('home'));
const pageComp = computed(() => PAGE_MAP[current.value] || HomePage);

provide(
  'shellState',
  reactive({
    current,
    maximized,
    favorites,
    expandedDeveloper,
    navigate,
    toggleFavorite,
    recent,
  })
);

let offMax = null;
let offCoreExit = null;

function navigate(id) {
  if (!PAGE_MAP[id]) return;
  current.value = id;
  const item = navItem(id);
  if (item && item.business) {
    recent.value = [id, ...recent.value.filter((entry) => entry !== id)].slice(0, 6);
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(recent.value));
  }
  paletteVisible.value = false;
}

function toggleFavorite(id) {
  favorites.value = toggleFavoriteID(favorites.value, id);
  window.localStorage.setItem(FAVORITES_KEY, JSON.stringify(favorites.value));
}

function toggleDeveloper() {
  expandedDeveloper.value = !expandedDeveloper.value;
  window.localStorage.setItem(DEV_KEY, expandedDeveloper.value ? '1' : '0');
}

function onPaletteNavigate(id) {
  navigate(id);
}

function onRestore({ appID, params }) {
  if (!appID || !params) return;
  requestRestore(appID, params);
  navigate(appID);
}

async function loadCore() {
  coreError.value = '';
  try {
    await call('getApps');
  } catch (e) {
    coreError.value = String(e.message || e);
    try {
      const h = await window.core.health();
      if (h && h.command) coreError.value += '\n启动命令：' + h.command;
      if (h && h.lastStderr && h.lastStderr.length) {
        coreError.value += '\n核心报错：\n' + h.lastStderr.slice(-15).join('\n');
      }
    } catch (_e) {
      /* 忽略 */
    }
  } finally {
    loaded.value = true;
  }
}

async function restartCore() {
  // 重启核心会杀掉所有在跑任务，先统一收口：keep-alive 页面不会收到任何终态事件，
  // 不主动清理就会永久停在「启动中 / 执行中」，按钮再也点不动。
  abortAllJobs('核心已重启，任务已中断');
  loaded.value = false;
  try {
    await window.core.restartCore();
  } catch (e) {
    coreError.value = String(e.message || e);
  }
  setTimeout(loadCore, 1500);
}

function onKeydown(event) {
  const modifier = event.ctrlKey || event.metaKey;
  if (modifier && event.key.toLowerCase() === 'k') {
    event.preventDefault();
    paletteVisible.value = !paletteVisible.value;
  } else if (modifier && event.key === ',') {
    event.preventDefault();
    paletteVisible.value = false;
    navigate('settings');
  } else if (event.key === 'Escape' && paletteVisible.value) {
    paletteVisible.value = false;
  }
}

onMounted(async () => {
  offMax = window.core.onMaxChange((value) => {
    maximized.value = !!value;
  });
  // 核心意外退出：在跑任务由 api.js 统一收口，这里只把原因亮到页头，
  // 否则用户只看到任务消失、没有任何解释。
  offCoreExit = window.core.onEvent((ev) => {
    if (!ev || ev.event !== 'core.exit') return;
    abortAllJobs(`核心进程已退出（code ${ev.code ?? '未知'}），任务已中断`);
    coreError.value = `核心进程已退出（code ${ev.code ?? '未知'}），请点击「重启核心」恢复。`;
  });
  try {
    if (window.core.winIsMax) maximized.value = !!(await window.core.winIsMax());
  } catch (_e) {
    /* 忽略 */
  }
  window.addEventListener('keydown', onKeydown);
  loadCore();
});

onUnmounted(() => {
  if (offMax) offMax();
  if (offCoreExit) offCoreExit();
  offCoreExit = null;
  window.removeEventListener('keydown', onKeydown);
});
</script>
