<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">交跨记录</h1>
      <span class="page-sub">输电线路交叉跨越台账的归档、核对与统计</span>
      <div class="spacer"></div>
      <t-select
        v-if="teamOptions.length > 1"
        v-model="teamId"
        :options="teamOptions"
        size="small"
        style="width: 150px"
        title="仅超级管理员可切换班组；其余角色固定为本班组"
        @change="onTeamChange"
      />
    </div>

    <div v-if="isDemo" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >数据为静态演示，未请求真实服务；切 live 模式（localStorage shade.apiMode=live）对接 Go
        API。</span
      >
    </div>

    <!-- 看板四格 + 管理待办 -->
    <div v-if="tab === 'records'" class="cx-dash">
      <div class="card dc">
        <span class="dc-n blue num">{{ dashVal(dash.total) }}</span>
        <span class="dc-t">记录总数</span>
      </div>
      <div class="card dc">
        <span class="dc-n num">{{ dashVal(dash.week_new) }}</span>
        <span class="dc-t">本周新增</span>
      </div>
      <div class="card dc">
        <span class="dc-n red num">{{ dashVal(dash.hazard) }}</span>
        <span class="dc-t">存在隐患</span>
      </div>
      <div class="card dc">
        <span class="dc-n num">{{ dashVal(dash.pending_lines) }}</span>
        <span class="dc-t">待确认线路</span>
      </div>
      <div v-if="isManager && (dash.staging_batches || dash.pending_conflicts)" class="dash-todo">
        <span>待办：</span>
        <a v-if="dash.staging_batches" @click="switchTab('staging')"
          >暂存 {{ dash.staging_batches }} 批</a
        >
        <a v-if="dash.pending_conflicts" @click="switchTab('conflicts')">
          冲突 {{ dash.pending_conflicts }} 条
        </a>
      </div>
    </div>

    <!-- 页签 -->
    <div class="subnav">
      <div class="subnav-item" :class="{ active: tab === 'records' }" @click="switchTab('records')">
        记录
      </div>
      <div
        v-if="isManager"
        class="subnav-item"
        :class="{ active: tab === 'staging' }"
        @click="switchTab('staging')"
      >
        暂存区
        <span v-if="dash.staging_batches" class="bdg num">{{ dash.staging_batches }}</span>
      </div>
      <div
        v-if="isManager"
        class="subnav-item"
        :class="{ active: tab === 'conflicts' }"
        @click="switchTab('conflicts')"
      >
        冲突
        <span v-if="dash.pending_conflicts" class="bdg num">{{ dash.pending_conflicts }}</span>
      </div>
      <div
        v-if="isManager"
        class="subnav-item"
        :class="{ active: tab === 'sync' }"
        @click="switchTab('sync')"
      >
        同步
      </div>
    </div>

    <!-- 记录页签 -->
    <template v-if="tab === 'records'">
      <div v-if="view === 'lines'" class="toolbar">
        <t-input
          v-model="lineSearch"
          class="rec-search"
          maxlength="32"
          clearable
          placeholder="搜索线路名称 / 电压等级"
        />
      </div>
      <div v-else class="toolbar">
        <t-button size="small" variant="outline" @click="backToLines">返回线路</t-button>
        <span class="ledger-title">{{ ledgerGroup ? ledgerGroup.title : '' }}</span>
        <span class="ledger-sub num">共 {{ ledgerGroup ? ledgerGroup.records.length : 0 }} 条</span>
      </div>

      <div v-if="recLoading" class="loading">加载中…</div>
      <div v-else-if="recError" class="empty">
        <p>{{ recError }}</p>
        <t-button size="small" variant="outline" @click="loadRecords()">重试</t-button>
      </div>

      <!-- 一级线路大卡 -->
      <div v-else-if="view === 'lines'" class="lc-grid">
        <div
          v-for="(g, idx) in visibleLineGroups"
          :key="g.key"
          class="card lc"
          :class="{ 'lc-clear': g.stats.hz === 0 }"
          :style="{ animationDelay: Math.min(idx * 0.05, 0.4).toFixed(2) + 's' }"
          title="点击进入线路台账"
          @click="openLedger(g.key)"
        >
          <div class="lc-main">
            <div class="lc-title">{{ g.title }}</div>
            <div class="lc-stats">
              <span :class="g.stats.hz > 0 ? 'lc-key' : 'lc-total'"
                >有隐患<b class="num">{{ g.stats.hz }}</b></span
              >
              <span class="lc-total num">共 {{ g.stats.total }} 条</span>
            </div>
          </div>
          <span class="lc-go">›</span>
        </div>
        <div v-if="!visibleLineGroups.length" class="empty">
          <p>
            {{
              lineSearch
                ? `未找到匹配「${lineSearch}」的线路`
                : '暂无运维线路，可先到「班组数据」维护'
            }}
          </p>
        </div>
      </div>

      <!-- 二级台账卡片流 -->
      <div v-else class="ledger-flow">
        <template v-if="ledgerGroup && ledgerGroup.records.length">
          <RecordCard
            v-for="r in ledgerGroup.records"
            :key="r.id"
            :record="r"
            :is-manager="isManager"
            @remove="removeRecord"
          />
        </template>
        <div v-else class="empty">
          <p>该线路下暂无记录</p>
        </div>
      </div>
    </template>

    <!-- 暂存区页签 -->
    <div v-else-if="tab === 'staging'" class="card">
      <StagingPanel
        :batches="stgBatches"
        :detail="stgDetail"
        :loading="stgLoading"
        :error="stgError"
        @reload="loadStaging"
        @open="openBatch"
        @back="stgDetail = null"
        @save-row="saveStagingRow"
        @import="importStaging"
        @discard="discardStaging"
      />
    </div>

    <!-- 冲突页签（管理） -->
    <div v-else-if="tab === 'conflicts'" class="card">
      <ConflictPanel
        :conflicts="cftList"
        :loading="cftLoading"
        :error="cftError"
        @reload="loadConflicts"
        @resolve="onResolveConflict"
      />
    </div>

    <!-- 同步页签（管理）：内网同步状态 + 知识库同步 -->
    <div v-else-if="tab === 'sync'">
      <SyncPanel :team-id="teamId" />
    </div>

    <!-- 导出 FAB -->
    <button class="fab" title="导出统计表" @click="openExport">
      <svg viewBox="0 0 20 20" width="20" height="20">
        <path
          d="M10 3v9m0 0l-3.2-3.2M10 12l3.2-3.2M4 15.5h12"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
        />
      </svg>
    </button>

    <ExportDialog
      v-model:visible="exportVisible"
      :lines="
        (rec.lines || []).map((l) => ({ voltage_level: l.voltage_level, line_name: l.line_name }))
      "
      :show-current="view === 'ledger'"
      :cur-line-name="view === 'ledger' && ledgerGroup ? ledgerGroup.lineName : ''"
      @download="doExport"
      @preview="doPreview"
    />
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { getApiMode } from '../api.js';
import * as api from '../features/crossing/api.js';
import RecordCard from '../features/crossing/RecordCard.vue';
import ExportDialog from '../features/crossing/ExportDialog.vue';
import StagingPanel from '../features/crossing/StagingPanel.vue';
import ConflictPanel from '../features/crossing/ConflictPanel.vue';
import SyncPanel from '../features/crossing/SyncPanel.vue';
import {
  fmtKv,
  lineStats,
  saveBlob,
  sortLineGroups,
  currentUserObj,
} from '../features/crossing/util.js';

const isDemo = computed(() => getApiMode() === 'demo');
const me = currentUserObj();
const isManager = me.role === 'admin' || me.role === 'team_admin';

// ---- 班组切换（仅超管；数据源 /admin/teams，端点缺失时隐藏回退本班） ----
const teamId = ref(0);
const teamOptions = ref([]);
const TEAM_KEY = 'crossing_team_id';

async function loadTeams() {
  if (me.role !== 'admin') return;
  try {
    const list = await api.listTeams();
    teamOptions.value = list.map((t) => ({ label: t.name, value: t.id }));
    const saved = Number(localStorage.getItem(TEAM_KEY)) || 0;
    if (list.some((t) => t.id === saved)) teamId.value = saved;
    else if (list.length) {
      teamId.value = list[0].id;
      localStorage.setItem(TEAM_KEY, String(list[0].id));
    }
  } catch (e) {
    /* 班组列表拉取失败：不显示切换器，由后端落默认班组出数 */
  }
}

function onTeamChange(v) {
  localStorage.setItem(TEAM_KEY, String(v));
  loadAll();
}

// ---- 看板 / 记录 ----
const dash = ref({});
const rec = ref({ lines: [], unmatched: [] });
const recLoading = ref(false);
const recError = ref('');
const tab = ref('records');
const view = ref('lines');
const curLine = ref('');
const lineSearch = ref('');

function dashVal(v) {
  return v == null ? '—' : v;
}

async function loadDashboard() {
  try {
    dash.value = (await api.getDashboard(teamId.value)) || {};
  } catch (e) {
    dash.value = {};
    MessagePlugin.error(e.message || '看板读取失败');
  }
}

async function loadRecords(quiet) {
  if (!quiet) {
    recLoading.value = true;
  }
  recError.value = '';
  try {
    rec.value = (await api.getRecords(teamId.value)) || { lines: [], unmatched: [] };
  } catch (e) {
    rec.value = { lines: [], unmatched: [] };
    recError.value = e.message || '记录读取失败';
  } finally {
    recLoading.value = false;
  }
}

// ---- 线路视图 / 台账视图 ----
const allGroups = computed(() => {
  const list = (rec.value.lines || []).map((g) => ({
    key: g.line_name,
    line_name: g.line_name,
    voltage_level: g.voltage_level,
    title: `${fmtKv(g.voltage_level)} ${g.line_name}`,
    lineName: g.line_name,
    records: g.records || [],
    unmatched: false,
  }));
  const un = rec.value.unmatched || [];
  if (un.length) {
    list.push({
      key: '__unmatched__',
      line_name: '__unmatched__',
      voltage_level: '',
      title: '未匹配线路',
      lineName: '',
      records: un,
      unmatched: true,
    });
  }
  return list;
});

const visibleLineGroups = computed(() => {
  const kw = lineSearch.value.trim().toLowerCase();
  const sorted = sortLineGroups(allGroups.value.filter((g) => !g.unmatched));
  const groups = [...sorted, ...allGroups.value.filter((g) => g.unmatched)];
  return groups
    .map((g) => ({ ...g, stats: lineStats(g.records) }))
    .filter((g) => {
      if (!kw) return true;
      return (
        g.title.toLowerCase().includes(kw) ||
        String(g.lineName || '')
          .toLowerCase()
          .includes(kw) ||
        (g.unmatched && '未匹配线路'.includes(lineSearch.value.trim()))
      );
    });
});

const ledgerGroup = computed(() => allGroups.value.find((g) => g.key === curLine.value) || null);

function openLedger(key) {
  curLine.value = key;
  view.value = 'ledger';
}
function backToLines() {
  view.value = 'lines';
  curLine.value = '';
}

// ---- 记录删除 ----
async function removeRecord(id) {
  if (!window.confirm('删除后不可恢复，确定删除该交跨记录吗？')) return;
  try {
    await api.deleteRecord(id, teamId.value);
    MessagePlugin.success('已删除');
    loadDashboard();
    loadRecords(true);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  }
}

// ---- 导出 ----
const exportVisible = ref(false);
function openExport() {
  if (recLoading.value) {
    MessagePlugin.info('记录加载中，请稍候');
    return;
  }
  exportVisible.value = true;
}
async function doExport(payload) {
  if (payload.error) {
    MessagePlugin.info(payload.error);
    return;
  }
  try {
    const res = await api.exportDownload(payload.query, teamId.value);
    await saveBlob(res, '交跨统计表.xlsx');
    payload.done?.();
    MessagePlugin.success('已导出');
  } catch (e) {
    payload.fail?.();
    MessagePlugin.error(e.message || '导出失败');
  }
}

// doPreview 导出在线预览：取 /export/preview 地址后新窗口打开（OO 承载页或微软查看器）。
async function doPreview(payload) {
  if (payload.error) {
    MessagePlugin.info(payload.error);
    return;
  }
  try {
    const r = await api.exportPreview(payload.query, teamId.value);
    if (!r || !r.url) throw new Error('服务未返回预览地址');
    window.open(r.url, '_blank', 'noopener');
    payload.done?.();
  } catch (e) {
    payload.fail?.();
    MessagePlugin.error(e.message || '预览失败');
  }
}

// ---- 冲突裁决（管理） ----
const cftList = ref([]);
const cftLoading = ref(false);
const cftError = ref('');
async function loadConflicts() {
  cftLoading.value = true;
  cftError.value = '';
  try {
    cftList.value = (await api.getConflicts(teamId.value)) || [];
  } catch (e) {
    cftList.value = [];
    cftError.value = e.message || '冲突列表读取失败';
  } finally {
    cftLoading.value = false;
  }
}
// onResolveConflict 二选一裁决：keep=server 保留系统值 / intranet 采用内网值覆盖记录。
async function onResolveConflict(id, keep, done) {
  try {
    await api.resolveConflict(id, keep, teamId.value);
    MessagePlugin.success(keep === 'intranet' ? '已按内网值更新' : '已保留系统值');
    loadDashboard();
    if (keep === 'intranet') loadRecords(true); // 内网值已覆盖记录，卡片流静默刷新
    await loadConflicts();
  } catch (e) {
    MessagePlugin.error(e.message || '冲突裁决失败');
  } finally {
    done?.();
  }
}

// ---- 暂存区 ----
const stgBatches = ref([]);
const stgDetail = ref(null);
const stgLoading = ref(false);
const stgError = ref('');
async function loadStaging() {
  stgDetail.value = null;
  stgLoading.value = true;
  stgError.value = '';
  try {
    stgBatches.value = (await api.stagingBatches(teamId.value)) || [];
  } catch (e) {
    stgBatches.value = [];
    stgError.value = e.message || '暂存批次读取失败';
  } finally {
    stgLoading.value = false;
  }
}
async function openBatch(id, silent) {
  try {
    stgDetail.value = (await api.stagingBatch(id, teamId.value)) || null;
    if (!stgDetail.value) loadStaging();
  } catch (e) {
    stgDetail.value = null;
    if (silent) {
      loadStaging();
      return;
    }
    MessagePlugin.error(e.message || '批次读取失败');
  }
}
async function saveStagingRow(id, body, done) {
  try {
    await api.updateStagingRecord(id, body, teamId.value);
    MessagePlugin.success('已保存');
  } catch (e) {
    MessagePlugin.error(e.message || '暂存记录更新失败');
  } finally {
    done?.();
  }
}
async function importStaging(recordIds) {
  const d = stgDetail.value;
  if (!d || !d.batch) return;
  try {
    const r = await api.importBatch(d.batch.id, recordIds, teamId.value);
    MessagePlugin.success(`已转入 ${(r && r.imported) || 0} 条`);
    loadDashboard();
    loadRecords(true);
    openBatch(d.batch.id, true);
  } catch (e) {
    MessagePlugin.error(e.message || '导入失败');
  }
}
async function discardStaging() {
  const d = stgDetail.value;
  if (!d || !d.batch) return;
  if (
    !window.confirm(
      `废弃后该批次及全部暂存记录将被删除且不可恢复，确定废弃「${d.batch.group_name || '本批次'}」吗？`
    )
  )
    return;
  try {
    await api.discardBatch(d.batch.id, teamId.value);
    MessagePlugin.success('已废弃');
    stgDetail.value = null;
    loadDashboard();
    loadStaging();
  } catch (e) {
    MessagePlugin.error(e.message || '批次删除失败');
  }
}

function switchTab(t) {
  tab.value = t;
  if (t === 'staging' && !stgBatches.value.length && !stgLoading.value) loadStaging();
  if (t === 'conflicts' && !cftLoading.value) loadConflicts();
}

function loadAll() {
  loadDashboard();
  loadRecords();
}

onMounted(() => {
  loadTeams().then(loadAll);
});
</script>

<style scoped>
.spacer {
  flex: 1;
}
.cx-dash {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
  gap: 12px;
  margin-bottom: 14px;
  align-items: stretch;
}
.dc {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.dc-n {
  font-size: 24px;
  font-weight: 700;
  color: var(--tk-ink);
}
.dc-n.blue {
  color: var(--tk-brand);
}
.dc-n.red {
  color: var(--tk-red);
}
.dc-t {
  font-size: 12.5px;
  color: var(--tk-sub);
}
.dash-todo {
  grid-column: 1 / -1;
  font-size: 12.5px;
  color: var(--tk-sub);
  display: flex;
  gap: 6px;
  align-items: center;
}
.dash-todo a {
  color: var(--tk-brand);
  cursor: pointer;
}
.bdg {
  margin-left: 6px;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--tk-red);
  color: #fff;
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.rec-search {
  width: 260px;
}
.ledger-title {
  font-size: 15px;
  font-weight: 600;
}
.ledger-sub {
  font-size: 12.5px;
  color: var(--tk-sub);
}
.lc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 14px;
}
.lc {
  display: flex;
  align-items: center;
  gap: 12px;
  cursor: pointer;
  animation: lc-in 0.5s var(--tk-ease) both;
}
.lc:hover {
  box-shadow: var(--tk-shadow-hover);
}
.lc-title {
  font-size: 15px;
  font-weight: 600;
}
.lc-stats {
  margin-top: 8px;
  display: flex;
  gap: 12px;
  font-size: 12px;
  color: var(--tk-sub);
}
.lc-stats b {
  margin-left: 4px;
  font-size: 15px;
}
.lc-key b {
  color: var(--tk-red);
}
.lc-total {
  color: var(--tk-ph);
}
.lc-clear .lc-key b {
  color: var(--tk-ph);
}
.lc-go {
  margin-left: auto;
  color: var(--tk-dash);
  font-size: 18px;
}
@keyframes lc-in {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.ledger-flow {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.loading {
  padding: 30px;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
}
.empty {
  padding: 36px;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
}
.fab {
  position: fixed;
  right: 28px;
  bottom: 28px;
  width: 52px;
  height: 52px;
  border: none;
  border-radius: 50%;
  background: var(--tk-brand);
  color: #fff;
  cursor: pointer;
  box-shadow: var(--tk-shadow-hover);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 20;
}
.fab:hover {
  background: var(--tk-brand-deep);
}
</style>
