<template>
  <div class="logs-page">
    <div class="page-head">
      <h1 class="page-title">应用日志</h1>
      <span class="page-sub">
        服务端业务事件、外部调用、定时任务与异常统一归档
        <span class="retention">仅保留近 7 天 · 过期自动清理</span>
      </span>
    </div>

    <div class="card retention-note">
      日志按应用与级别结构化记录，同时保留中文说明与技术原文；服务端固定保留近 7 天，
      过期条目由服务端自动清理，此处不提供恢复。
    </div>

    <!-- 应用子页签：真实应用目录，逐应用查询 -->
    <div class="subnav">
      <div class="subnav-item" :class="{ active: appCode === '' }" @click="switchApp('')">
        全部应用
      </div>
      <div
        v-for="app in logApps"
        :key="app.code"
        class="subnav-item"
        :class="{ active: appCode === app.code }"
        @click="switchApp(app.code)"
      >
        {{ app.name || app.code }}
      </div>
    </div>
    <div v-if="appsError" class="apps-error">
      应用目录加载失败：{{ appsError }}（仍可按「全部应用」查询日志）
    </div>

    <div class="card">
      <!-- 筛选工具条 -->
      <div class="toolbar">
        <t-input
          v-model="keyword"
          placeholder="搜索中文说明或原始日志"
          clearable
          style="max-width: 220px"
          @enter="reload"
        />
        <span class="field-label">级别</span>
        <t-select v-model="level" :options="levelOptions" style="width: 110px" @change="reload" />
        <span class="field-label">类别</span>
        <t-select
          v-model="category"
          :options="categoryOptions"
          style="width: 120px"
          @change="reload"
        />
        <span class="field-label">时间</span>
        <t-select
          v-model="rangeHours"
          :options="rangeOptions"
          style="width: 120px"
          @change="reload"
        />
        <span class="field-label">消息显示</span>
        <t-radio-group
          v-model="langMode"
          :options="langOptions"
          variant="default-filled"
          @change="reload"
        />
        <div class="spacer"></div>
        <t-button variant="outline" :loading="loading" @click="reload">刷新</t-button>
      </div>

      <!-- 日志表（:key 绑定筛选/页签真实切换：切换时整表重挂载，行入场动画真实重放） -->
      <t-table
        v-if="!loadError"
        :key="tableKey"
        :data="logs"
        :columns="columns"
        :loading="loading"
        row-key="id"
        size="small"
        :bordered="false"
      >
        <template #created_at="{ row }">
          <span class="num">{{ formatTime(row.created_at) }}</span>
        </template>
        <template #level="{ row }">
          <span class="dot-tag" :class="levelClass(row.level)">
            <span class="dot"></span>{{ levelName(row.level) }}
          </span>
        </template>
        <template #category="{ row }">
          {{ categoryName(row.category) }}
        </template>
        <template #message="{ row }">
          <span :class="{ raw: langMode === 'raw' }">{{ messageOf(row) }}</span>
        </template>
        <template #op="{ row }">
          <t-button size="small" variant="text" @click="openDetail(row)">详情</t-button>
        </template>
      </t-table>

      <!-- 加载失败与空态分开呈现，失败不误显示为「暂无日志」 -->
      <div v-if="loadError" class="state-box error">
        <b>日志读取失败</b>
        <p>{{ loadError }}</p>
        <t-button size="small" variant="outline" style="margin-top: 12px" @click="reload">
          重新加载
        </t-button>
      </div>
      <div v-else-if="!loading && !logs.length" class="state-box">
        <b>没有符合条件的日志</b>
        <p>可放宽级别、类别或时间范围后重试。</p>
      </div>

      <div class="result-meta num">
        <span>共 {{ total }} 条日志</span>
        <span v-if="refreshTime">刷新于 {{ refreshTime }}</span>
      </div>

      <div style="display: flex; justify-content: flex-end; margin-top: 12px">
        <t-pagination
          v-model="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-size-options="[10, 20, 50]"
          @change="reload"
        />
      </div>
    </div>

    <!-- 日志详情：完整中文说明、原文与结构化上下文 -->
    <t-drawer
      v-model:visible="detailVisible"
      header="日志详情"
      :size="520"
      :footer="false"
      @close="detailVisible = false"
    >
      <template v-if="detail">
        <dl class="detail-list">
          <dt>应用标识</dt>
          <dd class="num">{{ detail.app_code }}</dd>
          <dt>级别</dt>
          <dd>{{ levelName(detail.level) }}</dd>
          <dt>事件类别</dt>
          <dd>{{ categoryName(detail.category) }}</dd>
          <dt>事件标识</dt>
          <dd class="num">{{ detail.event || '—' }}</dd>
          <dt>完整时间</dt>
          <dd class="num">{{ formatTime(detail.created_at) }}</dd>
          <dt>链路 ID</dt>
          <dd class="num">{{ detail.trace_id || '—' }}</dd>
        </dl>
        <div class="detail-block">
          <h4 class="card-title">中文说明</h4>
          <p>{{ detail.message_cn || '（无中文说明）' }}</p>
        </div>
        <div class="detail-block">
          <h4 class="card-title">技术原文</h4>
          <pre class="raw-block">{{ detail.raw_message || detail.message || '（无）' }}</pre>
        </div>
        <div v-if="detail.context_json" class="detail-block">
          <h4 class="card-title">结构化上下文</h4>
          <pre class="raw-block">{{ detail.context_json }}</pre>
        </div>
        <div v-if="detail.detail" class="detail-block">
          <h4 class="card-title">细节</h4>
          <pre class="raw-block">{{ detail.detail }}</pre>
        </div>
      </template>
    </t-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { listAppLogs, listLogApps } from '../features/admin/api.js';

// 应用子页签（真实应用目录 GET /api/v1/app/list）
const logApps = ref([]);
const appsError = ref('');

const appCode = ref('');
const level = ref('');
const category = ref('');
const keyword = ref('');
const rangeHours = ref(168); // 默认近 7 天，与服务端保留期一致
const langMode = ref('cn'); // cn=中文说明 / raw=原文
const logs = ref([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const loading = ref(false);
const loadError = ref('');
const refreshTime = ref('');

const detailVisible = ref(false);
const detail = ref(null);

// tableKey 真实切换信号（页签/筛选/页码）：变化时整表重挂载，行入场动画随之重放；
// 单纯「刷新」不换 key，避免整表闪动
const tableKey = computed(() =>
  [
    appCode.value,
    level.value,
    category.value,
    rangeHours.value,
    langMode.value,
    page.value,
    pageSize.value,
  ].join('|')
);

const levelOptions = [
  { label: '全部级别', value: '' },
  { label: '信息', value: 'info' },
  { label: '警告', value: 'warn' },
  { label: '错误', value: 'error' },
  { label: '调试', value: 'debug' },
];
const categoryOptions = [
  { label: '全部类别', value: '' },
  { label: '业务事件', value: 'business' },
  { label: '外部调用', value: 'external' },
  { label: '定时任务', value: 'scheduler' },
  { label: '配置变更', value: 'config' },
  { label: '微信消息', value: 'message' },
];
const rangeOptions = [
  { label: '近 24 小时', value: 24 },
  { label: '近 3 天', value: 72 },
  { label: '近 7 天', value: 168 },
];
const langOptions = [
  { label: '中文说明', value: 'cn' },
  { label: '原始日志', value: 'raw' },
];
const columns = [
  { colKey: 'created_at', title: '时间', width: 160, cell: 'created_at' },
  { colKey: 'level', title: '级别', width: 90, cell: 'level' },
  { colKey: 'category', title: '类别', width: 100, cell: 'category' },
  { colKey: 'message', title: '消息', cell: 'message' },
  { colKey: 'trace_id', title: '链路 ID', width: 130 },
  { colKey: 'op', title: '操作', width: 70, cell: 'op' },
];

function levelClass(levelName) {
  return { error: 'red', warn: 'orange', info: 'blue', debug: '' }[levelName] || '';
}
function levelName(levelValue) {
  return { info: '信息', warn: '警告', error: '错误', debug: '调试' }[levelValue] || levelValue;
}
function categoryName(categoryValue) {
  return (
    {
      business: '业务事件',
      external: '外部调用',
      scheduler: '定时任务',
      config: '配置变更',
      message: '微信消息',
    }[categoryValue] ||
    categoryValue ||
    '—'
  );
}

// formatTime 把 RFC3339 时间格式化为可读本地样式。
function formatTime(value) {
  if (!value) return '—';
  return String(value)
    .replace('T', ' ')
    .replace(/\.\d+.*$/, '')
    .replace(/Z$/, '');
}

// messageOf 中文说明优先回退原文；原文模式展示技术原文。
function messageOf(row) {
  if (langMode.value === 'raw') return row.raw_message || row.message || '';
  return row.message_cn || row.message || '';
}

function openDetail(row) {
  detail.value = row;
  detailVisible.value = true;
}

function switchApp(code) {
  appCode.value = code;
  page.value = 1;
  reload();
}

async function loadApps() {
  try {
    logApps.value = [
      { code: 'system', name: '系统公共' },
      ...(await listLogApps()),
      { code: 'sgcc-clockin', name: '商旅打卡' },
      { code: 'office', name: 'ONLYOFFICE 文档服务' },
      { code: 'chat-log', name: '聊天归档' },
    ];
    appsError.value = '';
  } catch (err) {
    logApps.value = [];
    appsError.value = err.message || '应用目录加载失败';
  }
}

async function reload() {
  loading.value = true;
  loadError.value = '';
  try {
    const since = rangeHours.value > 0 ? new Date(Date.now() - rangeHours.value * 3600 * 1000) : '';
    const data = await listAppLogs({
      appCode: appCode.value,
      level: level.value,
      category: category.value,
      keyword: keyword.value.trim(),
      since,
      page: page.value,
      pageSize: pageSize.value,
    });
    logs.value = data.items;
    total.value = data.total;
    refreshTime.value = formatTime(new Date().toISOString());
  } catch (err) {
    logs.value = [];
    total.value = 0;
    loadError.value = err.message || '日志加载失败';
    MessagePlugin.error(err.message || '日志加载失败');
  } finally {
    loading.value = false;
  }
}

onMounted(() => {
  loadApps();
  reload();
});
</script>

<style scoped>
/* ===== 本页补充 token（与 server/public/assets/theme.css 同名同值）：说明文字状态色 ===== */
.logs-page {
  --ok-text: #0b8a37;
  --warn-text: #d25f00;
  /* 整页骨架：覆盖 .page 内边距，页底色随本页 token 翻转 */
  margin: -28px -40px -48px;
  padding: 28px 40px 48px;
  min-height: calc(100% + 76px);
  background: var(--tk-bg);
  color: var(--tk-ink);
  box-sizing: border-box;
}

/* 深色：按 server/public/assets/theme.css 深色 token 在本页重映射（不简单反色）；
   全局 styles.css 未带深色覆盖，故限定本页作用域，不动全局样式文件 */
@media (prefers-color-scheme: dark) {
  .logs-page {
    color-scheme: dark;
    --tk-brand: #78a6ff;
    --tk-brand-deep: #78a6ff;
    --tk-ink: #e5e6eb;
    --tk-bg: #14171d;
    --tk-fill: #252b35;
    --tk-line: #343c49;
    --tk-sub: #a9b1bd;
    --tk-ph: #8a94a3;
    --tk-dash: #626d7d;
    --tk-blue-soft: #1b2d4d;
    --tk-red: #ff7a73;
    --tk-red-soft: #3a1d1d;
    --tk-green: #4fd37e;
    --tk-green-soft: #153322;
    --tk-yellow: #f5a94a;
    --tk-yellow-soft: #382b16;
    --tk-shadow-card: 0 2px 8px rgba(0, 0, 0, 0.24);
    --tk-shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.32);
    --ok-text: #82d89b;
    --warn-text: #f2b56d;
    /* TDesign 页内控件（表格/输入/分页等，随 DOM 继承）同步翻转 */
    --td-brand-color: #78a6ff;
    --td-brand-color-focus: rgba(120, 166, 255, 0.18);
    --td-bg-color-page: #14171d;
    --td-bg-color-container: #1d222b;
    --td-bg-color-secondarycontainer: #252b35;
    --td-bg-color-containerhover: #252b35;
    --td-text-color-primary: #e5e6eb;
    --td-text-color-secondary: #a9b1bd;
    --td-text-color-placeholder: #8a94a3;
    --td-component-border: #343c49;
    --td-error-color: #ff7a73;
    --td-success-color: #4fd37e;
    --td-warning-color: #f5a94a;
  }
  /* 全局 .card / .subnav-item 浅色硬值在本页改走可翻转 token */
  .card,
  .subnav-item {
    background: var(--td-bg-color-container);
  }
  /* 表格滚动区深色细滚动条 */
  :deep(.t-table__content) {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  :deep(.t-table__content)::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
  :deep(.t-table__content)::-webkit-scrollbar-thumb:hover {
    background: rgba(255, 255, 255, 0.34);
  }
  .raw-block {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .raw-block::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
}

/* 全局 .card / .subnav-item 白底硬值在本页改走可翻转 token（浅色观感不变） */
.card,
.subnav-item {
  background: var(--td-bg-color-container);
}

/* 表格/原文滚动区：细滚动条 + 稳定占位，出现滚动条时内容不偏移 */
:deep(.t-table__content),
.raw-block {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
:deep(.t-table__content)::-webkit-scrollbar,
.raw-block::-webkit-scrollbar {
  width: 5px;
  height: 5px;
}
:deep(.t-table__content)::-webkit-scrollbar-thumb,
.raw-block::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
:deep(.t-table__content)::-webkit-scrollbar-thumb:hover,
.raw-block::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
:deep(.t-table__content)::-webkit-scrollbar-track,
.raw-block::-webkit-scrollbar-track {
  background: transparent;
}

.retention {
  display: inline-block;
  margin-left: 10px;
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 11px;
  font-weight: 600;
}
.retention-note {
  padding: 10px 14px;
  color: var(--tk-sub);
  font-size: 12px;
  line-height: 1.8;
  border-left: 3px solid var(--tk-brand);
  margin-bottom: 12px;
}
.apps-error {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: var(--tk-yellow-soft);
  color: var(--warn-text);
  font-size: 12px;
  border-left: 3px solid var(--tk-yellow);
}
.result-meta {
  display: flex;
  justify-content: space-between;
  margin-top: 10px;
  color: var(--tk-ph);
  font-size: 11px;
}
.state-box {
  padding: 32px 12px;
  text-align: center;
  color: var(--tk-sub);
}
.state-box b {
  display: block;
  color: var(--tk-ink);
  margin-bottom: 6px;
}
.state-box p {
  margin: 0;
  font-size: 12px;
  color: var(--tk-ph);
}
.state-box.error b {
  color: var(--tk-red);
}
.raw {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
.detail-list {
  display: grid;
  grid-template-columns: 88px 1fr;
  gap: 6px 12px;
  margin: 0 0 14px;
  font-size: 12px;
}
.detail-list dt {
  color: var(--tk-ph);
}
.detail-list dd {
  margin: 0;
  color: var(--tk-ink);
  word-break: break-all;
}
.detail-block {
  margin-bottom: 12px;
}
.detail-block p {
  margin: 6px 0 0;
  font-size: 13px;
  line-height: 1.8;
  color: var(--tk-ink);
}
.raw-block {
  margin: 6px 0 0;
  padding: 10px 12px;
  background: var(--tk-fill);
  border-left: 3px solid var(--tk-line);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.7;
  color: var(--tk-sub);
  white-space: pre-wrap;
  word-break: break-all;
  overflow-x: auto;
}

/* 日志行入场（对照设计稿 .log-row：一次上浮后静止；整表随真实切换重挂载重放；reduce 降级） */
:deep(.t-table__body tr) {
  animation: logRise 0.24s var(--tk-ease) both;
}
@keyframes logRise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  :deep(.t-table__body tr) {
    animation: none !important;
  }
}
</style>
