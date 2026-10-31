<template>
  <div class="logs-page">
    <div class="page-head">
      <h1 class="page-title">应用日志</h1>
      <span class="page-sub">
        服务端业务事件、外部调用、定时任务、安全与运行异常统一归档
        <span class="retention">默认永久保留 · 文件 + 数据库双副本</span>
      </span>
    </div>

    <div class="card retention-note">
      每条日志先同步写入文件，再由唯一 Importer 异步入库；数据库故障时日志仍可靠留存。
      列表默认展示近 7 天（仅为查询窗口），可按应用、类别、关键词与时间范围检索历史日志。
    </div>

    <!-- 持久层健康：待补录、DB/磁盘失败、最后入库与归档状态 -->
    <section
      class="card health-card"
      :class="`is-${healthSeverityClass}`"
      aria-label="日志持久层健康"
    >
      <div class="health-head">
        <b class="health-title">持久层健康</b>
        <span class="dot-tag" :class="healthTagClass">
          <span class="dot"></span>{{ healthLabel }}
        </span>
        <span v-if="health" class="health-meta num">
          最后入库 {{ formatLogTime(health.last_import) }}
        </span>
        <span class="spacer"></span>
        <t-button size="small" variant="text" :loading="healthLoading" @click="loadHealth">
          刷新健康
        </t-button>
      </div>
      <p v-if="healthError" class="health-error">{{ healthError }}</p>
      <template v-else-if="health">
        <dl class="health-grid num">
          <div>
            <dt>待补录</dt>
            <dd>{{ health.pending }} 条</dd>
          </div>
          <div>
            <dt>已写入 / 已入库</dt>
            <dd>{{ health.written }} / {{ health.imported }}</dd>
          </div>
          <div>
            <dt>磁盘失败 / 回退</dt>
            <dd>{{ health.disk_fallbacks }}</dd>
          </div>
          <div>
            <dt>写入 / 入库失败</dt>
            <dd>{{ health.write_errors }} / {{ health.insert_errors }}</dd>
          </div>
          <div>
            <dt>归档段</dt>
            <dd>
              {{ health.archived_segments }}
              <template v-if="health.archive_oldest"> · 最早 {{ health.archive_oldest }}</template>
            </dd>
          </div>
          <div>
            <dt>活跃段</dt>
            <dd>{{ health.active_segment || '—' }}（{{ formatBytes(health.segment_bytes) }}）</dd>
          </div>
          <div>
            <dt>数据库</dt>
            <dd>{{ health.db_connected ? '已接入' : '未接入（仅文件）' }}</dd>
          </div>
          <div>
            <dt>入库游标</dt>
            <dd>{{ health.cursor_segment || '—' }} @ {{ health.cursor_offset }}</dd>
          </div>
        </dl>
        <ul v-if="healthAlertItems.length" class="health-alerts">
          <li v-for="(alert, i) in healthAlertItems" :key="i" :class="alert.level">
            {{ alert.text }}
          </li>
        </ul>
        <p v-if="health.last_error" class="health-error">最近错误：{{ health.last_error }}</p>
      </template>
      <p v-else class="health-empty">健康统计未接入（本地 fixture 或依赖未配置）。</p>
    </section>

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

    <!-- 视图：默认业务与系统事件，避免访问日志淹没；可切换访问/全部 -->
    <div class="view-tabs" role="tablist" aria-label="日志视图">
      <button
        v-for="opt in LOG_VIEW_OPTIONS"
        :key="opt.value"
        type="button"
        role="tab"
        class="view-tab"
        :class="{ active: view === opt.value }"
        :aria-selected="view === opt.value"
        @click="switchView(opt.value)"
      >
        {{ opt.label }}
      </button>
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
        <t-select
          v-model="level"
          :options="LOG_LEVEL_OPTIONS"
          style="width: 110px"
          @change="reload"
        />
        <span class="field-label">类别</span>
        <t-select
          v-model="category"
          :options="LOG_CATEGORY_OPTIONS"
          style="width: 130px"
          @change="onCategoryChange"
        />
        <span class="field-label">时间</span>
        <t-select
          v-model="rangeHours"
          :options="LOG_RANGE_OPTIONS"
          style="width: 130px"
          @change="reload"
        />
        <span class="field-label">消息显示</span>
        <t-radio-group v-model="langMode" :options="LOG_LANG_OPTIONS" variant="default-filled" />
        <div class="spacer"></div>
        <t-button variant="outline" :loading="exporting" @click="onExport">导出 NDJSON</t-button>
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
        <template #occurred_at="{ row }">
          <span class="num">{{ formatLogTime(logEventTime(row)) }}</span>
        </template>
        <template #level="{ row }">
          <span class="dot-tag" :class="levelClass(row.level)">
            <span class="dot"></span>{{ levelName(row.level) }}
          </span>
        </template>
        <template #category="{ row }">
          {{ categoryName(row.category) }}
        </template>
        <template #app_code="{ row }">
          <span class="app-code num">{{ row.app_code || '—' }}</span>
        </template>
        <template #user="{ row }">
          <span class="user-cell num">{{ userLabel(row) }}</span>
        </template>
        <template #message="{ row }">
          <div class="msg-main" :class="{ raw: langMode === 'raw' }">{{ messageOf(row) }}</div>
          <div v-if="row.detail" class="msg-detail num">
            {{ row.detail }}<span v-if="row.detail_truncated">…</span>
          </div>
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
        <p>可放宽视图、级别、类别或时间范围后重试。</p>
      </div>

      <div class="result-meta num">
        <span>{{ totalText }}</span>
        <span v-if="refreshTime">刷新于 {{ refreshTime }}</span>
      </div>

      <div v-if="logs.length" class="load-more">
        <t-button
          v-if="hasMore"
          size="small"
          variant="outline"
          :loading="loadingMore"
          @click="loadMore"
        >
          加载更早日志
        </t-button>
        <span v-else class="load-end">已到最早一条</span>
      </div>
    </div>

    <!-- 日志详情：列表只有摘要，完整 detail/context_json 由单条接口返回 -->
    <t-drawer
      v-model:visible="detailVisible"
      header="日志详情"
      :size="520"
      :footer="false"
      @close="detailVisible = false"
    >
      <div v-if="detailLoading" class="state-box">
        <b>正在读取完整详情…</b>
        <p>列表仅展示截断摘要。</p>
      </div>
      <div v-else-if="detailError" class="state-box error">
        <b>详情读取失败</b>
        <p>{{ detailError }}</p>
      </div>
      <template v-else-if="detail">
        <dl class="detail-list">
          <dt>日志 ID</dt>
          <dd class="num">
            {{ detail.id }}<template v-if="detail.log_id"> · {{ detail.log_id }}</template>
          </dd>
          <dt>应用标识</dt>
          <dd class="num">{{ detail.app_code }}</dd>
          <dt>级别 / 类别</dt>
          <dd>{{ levelName(detail.level) }} · {{ categoryName(detail.category) }}</dd>
          <dt>事件标识</dt>
          <dd class="num">{{ detail.event || '—' }}</dd>
          <dt>事件时间</dt>
          <dd class="num">{{ formatLogTime(logEventTime(detail)) }}</dd>
          <dt>记录时间</dt>
          <dd class="num">{{ formatLogTime(detail.created_at) }}</dd>
          <dt>用户 / 班组</dt>
          <dd>{{ userLabel(detail) }}</dd>
          <dt>来源 / 访问</dt>
          <dd class="num">
            {{ detail.source || '—' }} · {{ detail.is_access ? '访问日志' : '非访问日志' }}
          </dd>
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
import {
  exportAppLogs,
  getAppLogDetail,
  getAppLogHealth,
  listAppLogs,
  listLogApps,
} from '../features/admin/api.js';
import {
  LOG_CATEGORY_OPTIONS,
  LOG_DEFAULT_RANGE_HOURS,
  LOG_LANG_OPTIONS,
  LOG_LEVEL_OPTIONS,
  LOG_RANGE_OPTIONS,
  LOG_VIEW_OPTIONS,
  categoryName,
  exportStartedMessage,
  exportWarning,
  formatLogTime,
  healthAlerts,
  healthSeverity,
  isTotalKnown,
  keysetFromItems,
  levelClass,
  levelName,
  logEventTime,
  logTotalText,
  mergeLogItems,
  rangeSince,
  syncCategoryForView,
  syncViewForCategory,
} from '../features/admin/logs.js';

// 应用子页签（真实应用目录 GET /api/v1/app/list）
const logApps = ref([]);
const appsError = ref('');

const appCode = ref('');
const level = ref('');
const category = ref('');
const keyword = ref('');
const view = ref('activity'); // 默认排除访问日志噪声
const rangeHours = ref(LOG_DEFAULT_RANGE_HOURS);
const langMode = ref('cn'); // cn=中文说明 / raw=原文
const logs = ref([]);
const total = ref(-1); // -1 表示后端未计数
const pageSize = ref(20);
const loading = ref(false);
const loadingMore = ref(false);
const hasMore = ref(false);
const loadError = ref('');
const refreshTime = ref('');
const exporting = ref(false);

const health = ref(null);
const healthLoading = ref(false);
const healthError = ref('');

const detailVisible = ref(false);
const detail = ref(null);
const detailLoading = ref(false);
const detailError = ref('');

// 请求序号：快速切换筛选/页签/详情时，旧响应不得覆盖新状态。
let listSeq = 0;
let healthSeq = 0;
let detailSeq = 0;

// tableKey 真实切换信号（视图/筛选）：变化时整表重挂载，行入场动画随之重放；
// 单纯「刷新」不换 key，避免整表闪动。
const tableKey = computed(() =>
  [view.value, appCode.value, level.value, category.value, rangeHours.value, langMode.value].join(
    '|'
  )
);

const healthAlertItems = computed(() => healthAlerts(health.value));
const healthSeverityClass = computed(() => healthSeverity(health.value));
const healthLabel = computed(() => {
  if (healthSeverityClass.value === 'error') return '需处理';
  if (healthSeverityClass.value === 'warn') return '需关注';
  return '正常';
});
const healthTagClass = computed(() => {
  if (healthSeverityClass.value === 'error') return 'red';
  if (healthSeverityClass.value === 'warn') return 'orange';
  return 'green';
});
const totalText = computed(() => logTotalText(total.value, logs.value.length, hasMore.value));

const columns = [
  { colKey: 'occurred_at', title: '事件时间', width: 160, cell: 'occurred_at' },
  { colKey: 'level', title: '级别', width: 84, cell: 'level' },
  { colKey: 'category', title: '类别', width: 96, cell: 'category' },
  { colKey: 'app_code', title: '应用', width: 110, cell: 'app_code' },
  { colKey: 'user', title: '用户 / 班组', width: 150, cell: 'user' },
  { colKey: 'message', title: '消息', cell: 'message' },
  { colKey: 'trace_id', title: '链路 ID', width: 120 },
  { colKey: 'op', title: '操作', width: 64, cell: 'op' },
];

function formatBytes(value) {
  const n = Number(value) || 0;
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// messageOf 中文说明优先回退原文；原文模式展示技术原文。
function messageOf(row) {
  if (langMode.value === 'raw') return row.raw_message || row.message || '';
  return row.message_cn || row.message || '';
}

// userLabel 用户/班组归属；匿名与未归属班组明确标注。
function userLabel(row) {
  const user = row && row.user_id ? `用户 #${row.user_id}` : '匿名';
  const team = row && row.team_id ? `班组 #${row.team_id}` : '未归属班组';
  return `${user} · ${team}`;
}

// currentFilters 当前筛选（时间范围为查询窗口；导出与列表同口径）。
function currentFilters() {
  return {
    view: view.value,
    appCode: appCode.value,
    level: level.value,
    category: category.value,
    keyword: keyword.value,
    since: rangeSince(rangeHours.value),
    pageSize: pageSize.value,
  };
}

async function reload() {
  const seq = ++listSeq;
  loading.value = true;
  loadingMore.value = false;
  loadError.value = '';
  try {
    const data = await listAppLogs({ ...currentFilters(), page: 1 });
    if (seq !== listSeq) return; // 已被更新的筛选覆盖
    logs.value = data.items;
    total.value = data.total;
    hasMore.value =
      data.items.length >= pageSize.value &&
      (!isTotalKnown(data.total) || data.items.length < data.total);
    refreshTime.value = formatLogTime(new Date());
  } catch (err) {
    if (seq !== listSeq) return;
    logs.value = [];
    total.value = -1;
    hasMore.value = false;
    loadError.value = err.message || '日志加载失败';
    MessagePlugin.error(err.message || '日志加载失败');
  } finally {
    if (seq === listSeq) loading.value = false;
  }
}

// loadMore 键集向前加载：以当前最后一条的 occurred_at/id 为锚点，避免深 OFFSET。
async function loadMore() {
  if (loading.value || loadingMore.value) return;
  const cursor = keysetFromItems(logs.value);
  if (!cursor) return;
  const seq = listSeq;
  loadingMore.value = true;
  try {
    const data = await listAppLogs({
      ...currentFilters(),
      beforeTime: cursor.beforeTime,
      beforeId: cursor.beforeId,
    });
    if (seq !== listSeq) return;
    logs.value = mergeLogItems(logs.value, data.items);
    if (isTotalKnown(data.total)) total.value = data.total;
    hasMore.value =
      data.items.length >= pageSize.value &&
      (!isTotalKnown(total.value) || logs.value.length < total.value);
  } catch (err) {
    if (seq !== listSeq) return;
    MessagePlugin.error(err.message || '加载更早日志失败');
  } finally {
    if (seq === listSeq) loadingMore.value = false;
  }
}

function switchView(next) {
  if (view.value === next) return;
  view.value = next;
  // 切换视图时清掉互斥类别（access 视图不带非 access 类别；activity 不带 access 类别）。
  category.value = syncCategoryForView(next, category.value);
  reload();
}

// onCategoryChange 视图/类别联动：access 类别切访问视图，access 视图选非 access 类别切回 activity。
function onCategoryChange(value) {
  view.value = syncViewForCategory(view.value, value);
  reload();
}

function switchApp(code) {
  if (appCode.value === code) return;
  appCode.value = code;
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

async function loadHealth() {
  const seq = ++healthSeq;
  healthLoading.value = true;
  healthError.value = '';
  try {
    const data = await getAppLogHealth();
    if (seq !== healthSeq) return;
    health.value = data;
  } catch (err) {
    if (seq !== healthSeq) return;
    health.value = null;
    healthError.value = err.message || '健康信息加载失败';
  } finally {
    if (seq === healthSeq) healthLoading.value = false;
  }
}

async function openDetail(row) {
  const seq = ++detailSeq;
  detail.value = row; // 先用列表摘要占位，随后替换为完整详情
  detailError.value = '';
  detailLoading.value = true;
  detailVisible.value = true;
  try {
    const data = await getAppLogDetail(row.id);
    if (seq !== detailSeq) return;
    detail.value = { ...row, ...data };
  } catch (err) {
    if (seq !== detailSeq) return;
    detailError.value = err.message || '详情读取失败';
  } finally {
    if (seq === detailSeq) detailLoading.value = false;
  }
}

// onExport 先申请一次性 ticket，再让浏览器原生流式下载 NDJSON（不读入 JS 内存）；
// 磁盘回退过 DB 直写时明确提示文件副本可能不完整；提示只说明“已发起”，不宣称已保存完整。
async function onExport() {
  const healthWarning = exportWarning(health.value);
  if (healthWarning) MessagePlugin.warning(healthWarning);
  exporting.value = true;
  try {
    const ticket = await exportAppLogs(currentFilters());
    const ticketWarning = exportWarning(null, ticket);
    if (ticketWarning && ticketWarning !== healthWarning) {
      MessagePlugin.warning(ticketWarning);
    }
    MessagePlugin.info(exportStartedMessage(ticket.filename));
  } catch (err) {
    MessagePlugin.error(err.message || '导出失败');
  } finally {
    exporting.value = false;
  }
}

onMounted(() => {
  loadApps();
  loadHealth();
  reload();
});
</script>

<style scoped>
/* ===== 本页骨架：覆盖 .page 内边距，页底色随全局 token 翻转 ===== */
.logs-page {
  margin: -28px -40px -48px;
  padding: 28px 40px 48px;
  min-height: calc(100% + 76px);
  background: var(--tk-bg);
  color: var(--tk-ink);
  box-sizing: border-box;
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
  border-radius: 0;
  margin-bottom: 12px;
}

/* 健康摘要：浅底 + 左色条一族一律直角；正常态保持白卡 */
.health-card {
  padding: 12px 14px;
  margin-bottom: 12px;
  border-left: 3px solid var(--tk-green);
  border-radius: 0;
}
.health-card.is-warn {
  background: var(--tk-yellow-soft);
  border-left-color: var(--tk-yellow);
}
.health-card.is-error {
  background: var(--tk-red-soft);
  border-left-color: var(--tk-red);
}
.health-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.health-title {
  font-size: 13px;
  color: var(--tk-ink);
}
.health-meta {
  color: var(--tk-ph);
  font-size: 11px;
}
.health-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 8px 16px;
  margin: 10px 0 0;
}
.health-grid div {
  min-width: 0;
}
.health-grid dt {
  color: var(--tk-ph);
  font-size: 11px;
}
.health-grid dd {
  margin: 2px 0 0;
  color: var(--tk-ink);
  font-size: 12px;
  word-break: break-all;
}
.health-alerts {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--tk-warn-text);
  font-size: 12px;
  line-height: 1.8;
}
.health-alerts .error {
  color: var(--tk-red);
}
.health-error {
  margin: 8px 0 0;
  color: var(--tk-red);
  font-size: 12px;
}
.health-empty {
  margin: 8px 0 0;
  color: var(--tk-ph);
  font-size: 12px;
}

/* 视图切换：分段控件 8px 圆角，激活浅蓝底 */
.view-tabs {
  display: flex;
  gap: 8px;
  margin: 0 0 12px;
  flex-wrap: wrap;
}
.view-tab {
  padding: 5px 14px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--td-bg-color-container);
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
  transition:
    background 0.18s var(--tk-ease),
    color 0.18s var(--tk-ease),
    border-color 0.18s var(--tk-ease);
}
.view-tab:hover {
  background: var(--tk-fill);
}
.view-tab.active {
  background: var(--tk-blue-soft);
  border-color: var(--tk-brand);
  color: var(--tk-brand);
  font-weight: 600;
}

.apps-error {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: var(--tk-yellow-soft);
  color: var(--tk-warn-text);
  font-size: 12px;
  border-left: 3px solid var(--tk-yellow);
  border-radius: 0;
}

.app-code {
  color: var(--tk-sub);
  font-size: 11px;
}
.user-cell {
  color: var(--tk-sub);
  font-size: 11px;
  white-space: nowrap;
}
.msg-main {
  color: var(--tk-ink);
}
.msg-main.raw {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
.msg-detail {
  margin-top: 2px;
  color: var(--tk-ph);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.result-meta {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-top: 10px;
  color: var(--tk-ph);
  font-size: 11px;
  flex-wrap: wrap;
}
.load-more {
  display: flex;
  justify-content: center;
  align-items: center;
  margin-top: 12px;
}
.load-end {
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
  border-radius: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.7;
  color: var(--tk-sub);
  white-space: pre-wrap;
  word-break: break-all;
  overflow-x: auto;
}

/* 表格/原文滚动区：细滚动条 + 稳定占位，出现滚动条时内容不偏移 */
:deep(.t-table__content),
.raw-block {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-scrollbar-thumb) transparent;
}
:deep(.t-table__content)::-webkit-scrollbar,
.raw-block::-webkit-scrollbar {
  width: 5px;
  height: 5px;
}
:deep(.t-table__content)::-webkit-scrollbar-thumb,
.raw-block::-webkit-scrollbar-thumb {
  background: var(--tk-scrollbar-thumb);
  border-radius: 8px;
}
:deep(.t-table__content)::-webkit-scrollbar-thumb:hover,
.raw-block::-webkit-scrollbar-thumb:hover {
  background: var(--tk-scrollbar-thumb-hover);
}
:deep(.t-table__content)::-webkit-scrollbar-track,
.raw-block::-webkit-scrollbar-track {
  background: transparent;
}

/* 表格/卡片在深色下走全局 token（styles.css 已统一覆盖，本页不重复定义深色变量） */
.card {
  background: var(--td-bg-color-container);
}

/* 日志行入场（一次上浮后静止；整表随真实切换重挂载重放；reduce 降级） */
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
