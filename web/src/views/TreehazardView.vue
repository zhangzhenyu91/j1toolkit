<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">树障隐患记录</h1>
      <span class="page-sub">树木排查隐患的登记、处理与统计</span>
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
    <div v-if="tab === 'records'" class="th-dash">
      <div class="card dc">
        <span class="dc-n blue num">{{ dashVal(dash.unhandled) }}</span>
        <span class="dc-t">未处理</span>
      </div>
      <div class="card dc">
        <span class="dc-n num">{{ dashVal(dash.week_new) }}</span>
        <span class="dc-t">本周新增</span>
      </div>
      <div class="card dc">
        <span class="dc-n num" :class="overClass">{{ overText }}</span>
        <span class="dc-t">{{ overLabel }}</span>
      </div>
      <div class="card dc">
        <span class="dc-n green num">{{ dashVal(dash.handled) }}</span>
        <span class="dc-t">已处理</span>
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

    <!-- 工具行（管理角色）：阈值设置 -->
    <div v-if="isManager" class="toolbar">
      <t-button size="small" variant="outline" @click="openThreshold">阈值设置</t-button>
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
        <span class="ledger-sub" v-html="ledgerSubHtml"></span>
        <div class="spacer"></div>
        <t-button size="small" variant="outline" @click="toggleBatch">
          {{ batchOn ? '退出批量' : '批量处理' }}
        </t-button>
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
          :class="{ 'lc-clear': g.stats.unh === 0 && g.stats.key === 0 }"
          :style="{ animationDelay: Math.min(idx * 0.05, 0.4).toFixed(2) + 's' }"
          title="点击进入线路台账"
          @click="openLedger(g.key)"
        >
          <div class="lc-main">
            <div class="lc-title">{{ g.title }}</div>
            <div class="lc-stats">
              <span class="lc-unh"
                >未处理<b class="num">{{ g.stats.unh }}</b></span
              >
              <span
                v-if="rec.threshold !== null && rec.threshold !== undefined"
                :class="g.stats.key > 0 ? 'lc-key' : 'lc-total'"
                title="未处理且垂直距离≤阈值"
              >
                要紧<b class="num">{{ g.stats.key }}</b>
              </span>
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
            :threshold="thresholdNum"
            :is-manager="isManager"
            :me-id="meId"
            :batch-on="batchOn"
            :selected="!!batchSel[r.id]"
            @handle="openHandle([r.id])"
            @remove="removeRecord"
            @remove-photo="removePhoto"
            @upload="uploadPhotos"
            @toggle-select="toggleSel"
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
        :team-id="teamId"
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

    <!-- 批量处理底部悬浮条 -->
    <div v-if="batchOn" class="batch-bar">
      <span class="num">已选 {{ batchSelCount }} 条</span>
      <t-button
        size="small"
        theme="primary"
        :disabled="!batchSelCount"
        @click="openHandle(batchSelectedIds)"
      >
        批量处理
      </t-button>
      <t-button size="small" variant="outline" @click="toggleBatch">退出</t-button>
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

    <!-- 弹层 -->
    <HandleDialog v-model:visible="handleVisible" :ids="handleIds" @submit="submitHandle" />
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

    <!-- 阈值设置 -->
    <t-dialog
      v-model:visible="thresholdVisible"
      header="阈值设置"
      :close-on-overlay-click="false"
      :confirm-btn="{ content: '保 存', theme: 'primary', loading: thresholdSaving }"
      cancel-btn="取消"
      @confirm="saveThreshold"
    >
      <div class="th-form">
        <label class="f-lab">垂直距离阈值（m）</label>
        <t-input v-model="thresholdInput" type="number" placeholder="留空表示不设阈值" />
        <div class="muted">
          未处理且垂直距离 ≤ 阈值的记录计入看板「超阈值」并红显；留空保存即清除阈值。
        </div>
      </div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { getApiMode } from '../api.js';
import * as api from '../features/treehazard/api.js';
import RecordCard from '../features/treehazard/RecordCard.vue';
import HandleDialog from '../features/treehazard/HandleDialog.vue';
import ExportDialog from '../features/treehazard/ExportDialog.vue';
import StagingPanel from '../features/treehazard/StagingPanel.vue';
import ConflictPanel from '../features/treehazard/ConflictPanel.vue';
import SyncPanel from '../features/treehazard/SyncPanel.vue';
import {
  fmtKv,
  lineStats,
  saveBlob,
  sortLineGroups,
  currentUserObj,
} from '../features/treehazard/util.js';

const isDemo = computed(() => getApiMode() === 'demo');
const me = currentUserObj();
const meId = me.id || 0;
const isManager = me.role === 'admin' || me.role === 'team_admin';

// ---- 班组切换（仅超管；数据源 /admin/teams，端点缺失时隐藏回退本班） ----
const teamId = ref(0);
const teamOptions = ref([]);
const TEAM_KEY = 'treehazard_team_id';

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
const rec = ref({ threshold: null, lines: [], unmatched: [] });
const recLoading = ref(false);
const recError = ref('');
const tab = ref('records');
const view = ref('lines');
const curLine = ref('');
const lineSearch = ref('');

function dashVal(v) {
  return v == null ? '—' : v;
}
const thresholdNum = computed(() =>
  rec.value.threshold === null || rec.value.threshold === undefined
    ? null
    : Number(rec.value.threshold)
);
const overText = computed(() =>
  dash.value.threshold === null || dash.value.threshold === undefined
    ? '未设阈值'
    : dashVal(dash.value.over_threshold)
);
const overLabel = computed(() =>
  dash.value.threshold === null || dash.value.threshold === undefined
    ? '垂直距离≤阈值未处理'
    : `垂直距离≤${dash.value.threshold}m 未处理`
);
const overClass = computed(() => {
  if (dash.value.threshold === null || dash.value.threshold === undefined) return 'mut';
  return Number(dash.value.over_threshold) > 0 ? 'red' : 'zero';
});

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
    rec.value = (await api.getRecords(teamId.value)) || {
      threshold: null,
      lines: [],
      unmatched: [],
    };
  } catch (e) {
    rec.value = { threshold: null, lines: [], unmatched: [] };
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
  const sorted = sortLineGroups(
    allGroups.value.filter((g) => !g.unmatched),
    thresholdNum.value
  );
  const groups = [...sorted, ...allGroups.value.filter((g) => g.unmatched)];
  return groups
    .map((g) => ({ ...g, stats: lineStats(g.records, thresholdNum.value) }))
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
const ledgerSubHtml = computed(() => {
  if (!ledgerGroup.value) return '';
  const st = lineStats(ledgerGroup.value.records, thresholdNum.value);
  const unh = `<span class="${st.unh > 0 ? 'ls-unh' : ''}">未处理 ${st.unh}</span>`;
  const key =
    thresholdNum.value === null
      ? ''
      : ` · <span class="${st.key > 0 ? 'ls-key' : ''}">要紧 ${st.key}</span>`;
  return `${unh}${key} · <span class="num">共 ${st.total} 条</span>`;
});

function openLedger(key) {
  curLine.value = key;
  view.value = 'ledger';
}
function backToLines() {
  if (batchOn.value) toggleBatch();
  view.value = 'lines';
  curLine.value = '';
}

// ---- 批量处理 ----
const batchOn = ref(false);
const batchSel = ref({});
const batchSelCount = computed(() => Object.values(batchSel.value).filter(Boolean).length);
const batchSelectedIds = computed(() =>
  Object.keys(batchSel.value)
    .filter((k) => batchSel.value[k])
    .map(Number)
);
function toggleBatch() {
  batchOn.value = !batchOn.value;
  batchSel.value = {};
}
function toggleSel(id) {
  batchSel.value = { ...batchSel.value, [id]: !batchSel.value[id] };
}

// ---- 处理（单条/批量） ----
const handleVisible = ref(false);
const handleIds = ref([]);
function openHandle(ids) {
  handleIds.value = ids;
  handleVisible.value = true;
}
async function submitHandle(payload) {
  if (payload.error) {
    MessagePlugin.info(payload.error);
    return;
  }
  try {
    if (payload.ids.length > 1) {
      await api.handleBatch(payload.ids, payload.body, teamId.value);
      toggleBatch();
    } else {
      await api.handleRecord(payload.ids[0], payload.body, teamId.value);
    }
    payload.done?.();
    MessagePlugin.success('已处理');
    loadDashboard();
    loadRecords(true);
  } catch (e) {
    payload.fail?.();
    MessagePlugin.error(e.message || '处理失败');
  }
}

// ---- 记录删除 / 照片 ----
async function removeRecord(id) {
  if (!window.confirm('删除后不可恢复，该记录的照片一并删除，确定删除吗？')) return;
  try {
    await api.deleteRecord(id, teamId.value);
    MessagePlugin.success('已删除');
    loadDashboard();
    loadRecords(true);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  }
}

async function uploadPhotos(recordId, kind, dataUrls) {
  let ok = 0;
  let fail = 0;
  let firstErr = '';
  for (const url of dataUrls) {
    try {
      await api.uploadPhoto(recordId, kind, url, teamId.value);
      ok++;
    } catch (e) {
      fail++;
      if (!firstErr) firstErr = e.message || String(e);
    }
  }
  if (!ok) MessagePlugin.error(firstErr ? `上传失败：${firstErr}` : '上传失败，请重试');
  else if (fail) MessagePlugin.warning(`上传完成：成功 ${ok} 张，失败 ${fail} 张（${firstErr}）`);
  else MessagePlugin.success(`已上传 ${ok} 张`);
  loadRecords(true);
}

async function removePhoto(recordId, photoId) {
  if (!window.confirm('确定删除这张照片吗？')) return;
  try {
    await api.deletePhoto(recordId, photoId, teamId.value);
    MessagePlugin.success('已删除');
    loadRecords(true);
  } catch (e) {
    MessagePlugin.error(e.message || '照片删除失败');
  }
}

// ---- 阈值设置 ----
const thresholdVisible = ref(false);
const thresholdInput = ref('');
const thresholdSaving = ref(false);
async function openThreshold() {
  thresholdInput.value = '';
  try {
    const s = await api.getSettings(teamId.value);
    if (s && s.vertical_threshold !== null && s.vertical_threshold !== undefined) {
      thresholdInput.value = String(s.vertical_threshold);
    }
  } catch (e) {
    /* 设置读取失败沿用看板值 */
    if (dash.value.threshold != null) thresholdInput.value = String(dash.value.threshold);
  }
  thresholdVisible.value = true;
}
async function saveThreshold() {
  const raw = String(thresholdInput.value || '').trim();
  if (raw !== '' && (!Number.isFinite(Number(raw)) || Number(raw) < 0)) {
    MessagePlugin.info('阈值应为非负数字');
    return;
  }
  thresholdSaving.value = true;
  try {
    await api.putSettings(raw === '' ? null : Number(raw), teamId.value);
    thresholdVisible.value = false;
    MessagePlugin.success(raw === '' ? '已清除阈值' : '已保存阈值');
    loadDashboard();
    loadRecords(true);
  } catch (e) {
    MessagePlugin.error(e.message || '设置保存失败');
  } finally {
    thresholdSaving.value = false;
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
    await saveBlob(res, '树木统计表.xlsx');
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
    openBatch(d.batch.id, true); // 批次行空后服务端置 imported，详情 404 时静默回列表
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
.th-dash {
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
.dc-n.green {
  color: #0b8a37;
}
.dc-n.red {
  color: var(--tk-red);
}
.dc-n.zero {
  color: var(--tk-ph);
}
.dc-n.mut {
  color: var(--tk-ph);
  font-size: 16px;
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
.ledger-sub :deep(.ls-unh) {
  color: var(--tk-brand);
  font-weight: 600;
}
.ledger-sub :deep(.ls-key) {
  color: var(--tk-red);
  font-weight: 600;
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
.lc-unh b {
  color: var(--tk-brand);
}
.lc-key b {
  color: var(--tk-red);
}
.lc-total {
  color: var(--tk-ph);
}
.lc-clear .lc-unh b {
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
.batch-bar {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-hover);
  padding: 10px 16px;
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
  z-index: 30;
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
.th-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.f-lab {
  font-size: 13px;
  color: var(--tk-sub);
}
</style>
