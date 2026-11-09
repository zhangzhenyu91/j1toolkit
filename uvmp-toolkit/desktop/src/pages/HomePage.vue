<template>
  <div class="home-page">
    <t-card class="home-quick">
      <div class="sec-title">
        常用与最近
        <span class="right">收藏优先</span>
      </div>
      <div v-for="item in quickItems" :key="item.id" class="quick-row">
        <span class="quick-icon"><ShadeIcon :name="item.icon" :size="15" /></span>
        <span class="quick-text">
          <span class="quick-name">{{ item.title }}</span>
          <span class="quick-desc">{{ item.desc }}</span>
        </span>
        <span class="quick-actions">
          <button class="quick-link" @click="toggleFavorite(item.id)">
            {{ favorites.includes(item.id) ? '取消收藏' : '收藏' }}
          </button>
          <button class="quick-link" @click="navigate(item.id)">打开</button>
        </span>
      </div>
      <div v-if="!quickItems.length" class="quick-empty">
        还没有收藏或最近打开的工具。进入任意工具后可点击页头“收藏”，下次从这里快速打开。
      </div>
    </t-card>

    <t-card class="home-hero-card">
      <div class="home-hero">
        <div class="home-hero-text">
          <h2>轨迹导出</h2>
          <p>{{ pcdDesc }}</p>
          <span class="home-hero-status">
            <span class="status-dot" :class="pcdStatusTheme" />
            {{ pcdStatus }}
          </span>
        </div>
        <t-button theme="primary" @click="navigate('pcd_export')">开始导出</t-button>
      </div>
    </t-card>

    <t-card v-if="runningTasks.length" class="home-running-card">
      <div class="sec-title">运行中的任务</div>
      <div v-for="task in runningTasks" :key="task.key" class="home-running-row">
        <div class="home-running-head">
          <span class="home-running-name">{{ taskTitle(task) }}</span>
          <span class="muted home-running-text">{{ task.text }}</span>
          <button class="quick-link" @click="navigate(task.appID)">查看</button>
        </div>
        <t-progress
          theme="plump"
          :percentage="task.pct"
          color="#0E3DA8"
          track-color="rgba(14,61,168,.10)"
        />
      </div>
    </t-card>

    <div class="home-tools-grid">
      <t-card class="home-tool-card">
        <h3>题库搜题</h3>
        <div class="tool-status">
          <span class="status-dot" :class="quizCount ? 'success' : 'default'" />
          {{ quizStatus }}
        </div>
        <p class="tool-desc">屏幕选词实时匹配本机题库</p>
        <button class="quick-link" @click="navigate('quiz_search')">进入题库</button>
      </t-card>
      <t-card class="home-tool-card">
        <h3>学堂刷课</h3>
        <div class="tool-status">
          <span class="status-dot" :class="courseConfigured ? 'success' : 'default'" />
          {{ courseStatus }}
        </div>
        <p class="tool-desc">受控浏览器自动学习国网学堂课程</p>
        <button class="quick-link" @click="navigate('course_autoplay')">打开学堂</button>
      </t-card>
      <t-card class="home-tool-card">
        <h3>机巡审核</h3>
        <div class="tool-status">
          <span class="status-dot" :class="defectConfigured ? 'success' : 'default'" />
          {{ defectStatus }}
        </div>
        <p class="tool-desc">自动登录受控浏览器后批量审核机巡缺陷识别任务</p>
        <button class="quick-link" @click="navigate('defect_review')">进入审核</button>
      </t-card>
    </div>

    <div class="home-bottom">
      <t-card class="home-dev">
        <button class="home-dev-toggle" @click="devExpanded = !devExpanded">
          <span>开发者工具：后台同步</span>
          <span class="chevron">{{ devExpanded ? '−' : '+' }}</span>
        </button>
        <div v-show="devExpanded">
          <p class="muted" style="margin: 8px 0 0">
            供维护人员使用；普通用户无需配置这些同步任务。
          </p>
          <div v-for="item in devItems" :key="item.id" class="home-dev-row">
            <span class="quick-icon"><ShadeIcon :name="item.icon" :size="15" /></span>
            <span class="quick-text">
              <span class="quick-name">{{ item.title }}</span>
              <span class="quick-desc">{{ item.desc }}</span>
            </span>
            <span class="muted">{{ devStatus(item.id) }}</span>
            <button class="quick-link" @click="navigate(item.id)">打开</button>
          </div>
        </div>
      </t-card>

      <t-card class="home-recent">
        <div class="sec-title">
          最近结果
          <span class="right">{{ recentResults.length }} 条</span>
        </div>
        <div v-if="!recentResults.length" class="recent-empty">
          还没有执行记录。运行任意任务后，结果会出现在这里。
        </div>
        <div
          v-for="entry in recentResults"
          :key="entry.appID + entry.rec.job_id"
          class="recent-item"
        >
          <div class="recent-title">
            <span class="status-dot" :class="historyStatusText(entry.rec).theme" />
            {{ entry.appName }}
          </div>
          <div class="recent-meta">
            {{ entry.rec.time }} · {{ formatHistorySummary(entry.appID, entry.rec.result) }}
          </div>
        </div>
      </t-card>
    </div>
  </div>
</template>

<script setup>
import { computed, inject, onActivated, ref } from 'vue';
import ShadeIcon from '../components/ShadeIcon.vue';
import { call, qs } from '../api';
import { NAV_ITEMS, BUSINESS_IDS, navItem } from '../lib/navigation.mjs';
import { historyStatusText, mergeHistory } from '../lib/history.mjs';
import { formatHistorySummary } from '../lib/task-result.mjs';
import { taskStatus } from '../lib/task-status.mjs';

const shell = inject('shellState', null);
const favorites = computed(() => shell?.favorites || []);
const recent = computed(() => shell?.recent || []);
const navigate = (id) => shell?.navigate(id);
const toggleFavorite = (id) => shell?.toggleFavorite(id);
const runningTasks = computed(() =>
  Object.values(taskStatus.jobs).filter((task) => navItem(task.appID))
);

function taskTitle(task) {
  const title = navItem(task.appID)?.title || task.appID;
  return task.name ? `${title} · ${task.name}` : title;
}

const histories = ref({});
const config = ref({});
const quizCount = ref(0);
const devExpanded = ref(false);
const loaded = ref(false);

const pcdDesc = '按派车单号批量导出派车单与轨迹 PDF';
const pcdRecord = computed(() => (histories.value.pcd_export || [])[0] || null);
const pcdStatus = computed(() => {
  if (!loaded.value) return '正在读取状态…';
  const sso = (config.value.sso || {}).has_password ? '已配置派车系统账号' : '尚未配置派车系统账号';
  if (!pcdRecord.value) return `尚无执行记录 · ${sso}`;
  const status = historyStatusText(pcdRecord.value);
  return `上次${status.text} ${pcdRecord.value.time} · ${sso}`;
});
const pcdStatusTheme = computed(() => {
  if (!pcdRecord.value) return 'warning';
  return historyStatusText(pcdRecord.value).theme;
});

const quizStatus = computed(() => {
  if (!loaded.value) return '正在读取状态…';
  return quizCount.value > 0 ? `${quizCount.value} 题可用` : '尚未导入题库';
});
const defectConfigured = computed(() => Boolean((config.value.defect_review || {}).start_url));
const defectStatus = computed(() => {
  if (!loaded.value) return '正在读取状态…';
  const last = (histories.value.defect_review || [])[0];
  if (last) return `上次${historyStatusText(last).text} ${last.time}`;
  return defectConfigured.value ? '启动网址已配置' : '尚未打开审核浏览器';
});
const courseConfigured = computed(() => {
  const app = config.value.course_autoplay || {};
  return (
    (Array.isArray(app.instances) && app.instances.length > 0) ||
    Boolean((config.value.browser_automation || {}).start_url)
  );
});
const courseStatus = computed(() => {
  if (!loaded.value) return '正在读取状态…';
  const last = (histories.value.course_autoplay || [])[0];
  if (last) return `上次${historyStatusText(last).text} ${last.time}`;
  return courseConfigured.value ? '启动网址已配置' : '尚未打开课程浏览器';
});

const quickItems = computed(() => {
  const ids = [...favorites.value, ...recent.value];
  const unique = [...new Set(ids)].slice(0, BUSINESS_IDS.length);
  return unique.map(navItem).filter(Boolean);
});

const devItems = NAV_ITEMS.filter((item) => item.group === 'developer');

function devStatus(id) {
  const rec = (histories.value[id] || [])[0];
  if (!loaded.value) return '读取中…';
  if (!rec) return '尚无执行记录';
  return `${historyStatusText(rec).text} ${rec.time}`;
}

const recentResults = computed(() =>
  mergeHistory(histories.value)
    .slice(0, 6)
    .map((entry) => ({ ...entry, rec: entry.rec }))
);

async function reload() {
  const results = await Promise.allSettled([
    call('getConfig'),
    call('getHistory', { app_id: 'pcd_export' }),
    call('getHistory', { app_id: 'daily_sync' }),
    call('getHistory', { app_id: 'tree_sync' }),
    call('getHistory', { app_id: 'crossing_sync' }),
    call('getHistory', { app_id: 'course_autoplay' }),
    call('getHistory', { app_id: 'defect_review' }),
    call('getHistory', { app_id: 'quiz_search' }),
  ]);
  if (results[0].status === 'fulfilled') config.value = results[0].value || {};
  const ids = [
    'pcd_export',
    'daily_sync',
    'tree_sync',
    'crossing_sync',
    'course_autoplay',
    'defect_review',
    'quiz_search',
  ];
  const next = {};
  ids.forEach((id, index) => {
    const result = results[index + 1];
    next[id] = result.status === 'fulfilled' && Array.isArray(result.value) ? result.value : [];
  });
  histories.value = next;
  try {
    const state = await qs().getState();
    quizCount.value = state.questionCount || 0;
  } catch (_e) {
    quizCount.value = 0;
  }
  loaded.value = true;
}

onActivated(reload);
</script>
