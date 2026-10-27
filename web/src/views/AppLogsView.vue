<template>
  <div>
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

      <!-- 日志表 -->
      <t-table
        v-if="!loadError"
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
import { onMounted, ref } from 'vue';
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
.retention {
  display: inline-block;
  margin-left: 10px;
  padding: 2px 8px;
  border-radius: 4px;
  background: #e8f0ff;
  color: #0e3da8;
  font-size: 11px;
  font-weight: 600;
}
.retention-note {
  padding: 10px 14px;
  color: #4e5969;
  font-size: 12px;
  line-height: 1.8;
  border-left: 3px solid #0e3da8;
  margin-bottom: 12px;
}
.apps-error {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: #fff7e8;
  color: #d25f00;
  font-size: 12px;
  border-left: 3px solid #ff7d00;
}
.result-meta {
  display: flex;
  justify-content: space-between;
  margin-top: 10px;
  color: #86909c;
  font-size: 11px;
}
.state-box {
  padding: 32px 12px;
  text-align: center;
  color: #4e5969;
}
.state-box b {
  display: block;
  color: #1d2129;
  margin-bottom: 6px;
}
.state-box p {
  margin: 0;
  font-size: 12px;
  color: #86909c;
}
.state-box.error b {
  color: #f53f3f;
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
  color: #86909c;
}
.detail-list dd {
  margin: 0;
  color: #1d2129;
  word-break: break-all;
}
.detail-block {
  margin-bottom: 12px;
}
.detail-block p {
  margin: 6px 0 0;
  font-size: 13px;
  line-height: 1.8;
  color: #1d2129;
}
.raw-block {
  margin: 6px 0 0;
  padding: 10px 12px;
  background: #f7f8fa;
  border-left: 3px solid #e5e6eb;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.7;
  color: #4e5969;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
