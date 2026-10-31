<template>
  <!-- 旧版 treehazard.html 骨架：页头 + 看板四格（仅记录页签）+ 工具行（管理）+ 下划线页签 + 页签内部滚动
       + 批量处理悬浮胶囊条 + 导出 FAB 弹层 -->
  <div class="th-wrap">
    <!-- 页头：kicker + 标题 + 副标；右侧班组切换（仅超管可见） -->
    <div class="th-head">
      <div class="section-title">
        <span class="th-kick">TREE HAZARD</span>
        <h3>树障隐患记录</h3>
        <span class="more">树木排查隐患的登记、处理与统计</span>
      </div>
      <div class="acts">
        <select
          v-if="teamOptions.length > 1"
          v-model="teamId"
          class="th-team num"
          title="仅超级管理员可切换班组；其余角色固定为本班组"
          @change="onTeamChange"
        >
          <option v-for="t in teamOptions" :key="t.id" :value="t.id">{{ t.name }}</option>
        </select>
      </div>
    </div>

    <div v-if="isDemo" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >数据为静态演示，未请求真实服务；切 live 模式（localStorage shade.apiMode=live）对接 Go
        API。</span
      >
    </div>

    <!-- 看板四格 + 管理待办入口（仅记录页签展示） -->
    <div v-show="tab === 'records'" class="th-dash">
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
        <svg viewBox="0 0 14 14" width="14" height="14">
          <path
            d="M7 1.5l6 11H1l6-11zM7 5.5v3.5M7 10.8v.9"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
        </svg>
        <span>待办：</span>
        <a v-if="dash.staging_batches" @click="switchTab('staging')"
          >暂存 {{ dash.staging_batches }} 批</a
        >
        <span v-if="dash.staging_batches && dash.pending_conflicts">·</span>
        <a v-if="dash.pending_conflicts" @click="switchTab('conflicts')"
          >冲突 {{ dash.pending_conflicts }} 条</a
        >
      </div>
    </div>

    <!-- 工具行（管理角色）：手动新增 / 阈值设置 / 同步至内网 / 同步知识库 + 同步状态小字 -->
    <div v-if="isManager" class="th-tools">
      <button class="btn btn-sm" type="button" @click="openCreate">手动新增</button>
      <button class="btn btn-sm" type="button" @click="openThreshold">阈值设置</button>
      <button class="btn btn-sm" type="button" :disabled="syncBusy" @click="pushSync">
        同步至内网
      </button>
      <button class="btn btn-sm" type="button" :disabled="wxSyncing" @click="pushWxSync">
        <svg viewBox="0 0 15 15" width="15" height="15">
          <path
            d="M3 2.5h6.5L12 5v7.5H3v-10zM4.8 7h5.4M4.8 9.2h5.4"
            fill="none"
            stroke="currentColor"
            stroke-width="1.2"
            stroke-linejoin="round"
          />
        </svg>
        {{ wxSyncing ? '同步中…' : '同步知识库' }}
      </button>
      <span v-if="syncHint" class="sync-hint">{{ syncHint }}</span>
    </div>

    <!-- 页签：记录 / 暂存区（管理）/ 冲突（管理） -->
    <div class="th-tabs">
      <button type="button" :class="{ on: tab === 'records' }" @click="switchTab('records')">
        记录
      </button>
      <button
        v-if="isManager"
        type="button"
        :class="{ on: tab === 'staging' }"
        @click="switchTab('staging')"
      >
        暂存区
        <span v-if="dash.staging_batches" class="bdg num">{{ dash.staging_batches }}</span>
      </button>
      <button
        v-if="isManager"
        type="button"
        :class="{ on: tab === 'conflicts' }"
        @click="switchTab('conflicts')"
      >
        冲突
        <span v-if="dash.pending_conflicts" class="bdg num">{{ dash.pending_conflicts }}</span>
      </button>
    </div>

    <!-- 页签主体（仅活动页签内容滚动）；pane 的 key 随页签变化强制重建，
         保证页签来回切换也真实重播 paneIn 与卡片上浮入场（同出工日志 sheet 页签口径） -->
    <div class="th-body">
      <div :key="'pane-records-' + tab" class="pane" :class="{ on: tab === 'records' }">
        <!-- 一级「线路视图」顶栏：搜索框（输入即过滤线路名/电压等级） -->
        <div v-if="view === 'lines'" class="rec-bar">
          <label class="rec-search">
            <svg viewBox="0 0 15 15" width="15" height="15">
              <circle
                cx="6.5"
                cy="6.5"
                r="4.2"
                fill="none"
                stroke="currentColor"
                stroke-width="1.4"
              />
              <path
                d="M9.8 9.8l3 3"
                fill="none"
                stroke="currentColor"
                stroke-width="1.4"
                stroke-linecap="round"
              />
            </svg>
            <input
              v-model="lineSearch"
              maxlength="32"
              autocomplete="off"
              placeholder="搜索线路名称 / 电压等级"
            />
          </label>
        </div>
        <!-- 二级「台账视图」顶栏：返回 + 线路标题与计数 + 批量处理开关 -->
        <div v-else class="rec-bar">
          <button class="btn btn-sm" type="button" @click="backToLines">返回线路</button>
          <span class="ledger-title">{{ ledgerGroup ? ledgerGroup.title : '' }}</span>
          <span class="ledger-sub num" v-html="ledgerSubHtml"></span>
          <span class="spring">
            <button class="btn btn-sm" type="button" @click="toggleBatch">
              {{ batchOn ? '退出批量' : '批量处理' }}
            </button>
          </span>
        </div>

        <div class="pane-scroll">
          <div v-if="recLoading" class="loading"><span class="spin"></span>加载中…</div>
          <div v-else-if="recError" class="empty">
            <svg viewBox="0 0 44 44" width="44" height="44">
              <path
                d="M22 6l18 32H4L22 6zM22 18v10M22 32v2"
                fill="none"
                stroke="currentColor"
                stroke-width="2.4"
                stroke-linejoin="round"
                stroke-linecap="round"
              />
            </svg>
            <p>{{ recError }}</p>
            <button class="btn btn-sm" type="button" @click="loadRecords()">重试</button>
          </div>

          <!-- 一级线路大卡 -->
          <div v-else-if="view === 'lines'" :key="'lines'" class="lc-grid view-in">
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
              <span class="lc-go">
                <svg viewBox="0 0 16 16" width="16" height="16">
                  <path
                    d="M6 3l5 5-5 5"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                </svg>
              </span>
            </div>
            <div v-if="!visibleLineGroups.length" class="empty">
              <svg viewBox="0 0 44 44" width="44" height="44">
                <circle
                  cx="19"
                  cy="19"
                  r="11"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2.4"
                />
                <path
                  d="M27 27l9 9"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2.4"
                  stroke-linecap="round"
                />
              </svg>
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
          <div v-else :key="'ledger'" class="ledger-flow view-in">
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
                @edit="openEdit"
                @remove="removeRecord"
                @remove-photo="removePhoto"
                @upload="uploadPhotos"
                @toggle-select="toggleSel"
              />
            </template>
            <div v-else class="empty">
              <svg viewBox="0 0 44 44" width="44" height="44">
                <path
                  d="M10 8h24v28H10V8zM15 15h14M15 21h14M15 27h9"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2.2"
                  stroke-linecap="round"
                />
              </svg>
              <p>该线路下暂无记录</p>
            </div>
          </div>
        </div>
      </div>

      <!-- 暂存区页签（管理） -->
      <div :key="'pane-staging-' + tab" class="pane" :class="{ on: tab === 'staging' }">
        <div class="pane-scroll">
          <StagingPanel
            :fields="TREE_FIELDS.bizFields"
            :date-fields="TREE_FIELDS.dateFields"
            :note-field="TREE_FIELDS.noteField"
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
      </div>

      <!-- 冲突页签（管理） -->
      <div :key="'pane-conflicts-' + tab" class="pane" :class="{ on: tab === 'conflicts' }">
        <div class="pane-scroll">
          <ConflictPanel
            :fields="TREE_FIELDS.bizFields"
            :numeric-keys="NUMERIC_KEYS_TREE"
            show-status
            :conflicts="cftList"
            :loading="cftLoading"
            :error="cftError"
            @reload="loadConflicts"
            @resolve="onResolveConflict"
          />
        </div>
      </div>
    </div>

    <!-- 批量处理底部悬浮条 -->
    <div v-if="batchOn" class="batch-bar">
      <span class="bc num">已选 {{ batchSelCount }} 条</span>
      <button
        class="btn btn-sm btn-primary"
        type="button"
        :disabled="!batchSelCount"
        @click="openHandle(batchSelectedIds)"
      >
        批量处理
      </button>
      <button class="btn btn-sm" type="button" @click="toggleBatch">退出</button>
    </div>

    <!-- 导出 FAB（右下角悬浮圆形品牌蓝；点击弹层选导出范围 / 按线路分表，再预览或下载） -->
    <button class="fab" type="button" title="导出统计表" @click="openExport">
      <svg viewBox="0 0 22 22" width="22" height="22">
        <path
          d="M11 3v10m0 0l-4-4m4 4l4-4M4 18h14"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    </button>

    <!-- 弹层 -->
    <HandleDialog v-model:visible="handleVisible" :ids="handleIds" @submit="submitHandle" />
    <RecordEditDialog
      v-model:visible="editVisible"
      :record="editRecord"
      :preset-line="presetLineName"
      @submit="submitEdit"
    />
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
      :confirm-btn="null"
      :cancel-btn="null"
    >
      <div class="th-form">
        <label class="f-lab">垂直距离阈值（m）</label>
        <input
          v-model="thresholdInput"
          class="f-sel"
          type="number"
          min="0"
          step="0.1"
          placeholder="留空表示不设阈值"
        />
        <div class="f-hint">
          未处理且垂直距离 ≤ 阈值的记录计入看板「超阈值」并红显；留空保存即清除阈值。
        </div>
        <div class="m-foot">
          <button
            class="btn btn-sm"
            type="button"
            :disabled="thresholdSaving"
            @click="thresholdVisible = false"
          >
            取消
          </button>
          <button
            class="btn btn-sm btn-primary"
            type="button"
            :disabled="thresholdSaving"
            @click="saveThreshold"
          >
            {{ thresholdSaving ? '保存中…' : '保 存' }}
          </button>
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
import RecordEditDialog from '../features/treehazard/RecordEditDialog.vue';
import ExportDialog from '../features/core/RecordExportDialog.vue';
import StagingPanel from '../features/core/RecordStagingPanel.vue';
import ConflictPanel from '../features/core/RecordConflictPanel.vue';
import { NUMERIC_KEYS_TREE, TREE_FIELDS } from '../features/core/record-fields.js';
import {
  fmtKv,
  fmtDate,
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
    teamOptions.value = list.map((t) => ({ id: t.id, name: t.name }));
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

function onTeamChange() {
  localStorage.setItem(TEAM_KEY, String(teamId.value));
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
    if (batchOn.value) pruneBatchSel();
  }
}

// pruneBatchSel 勾选集合剔除已不可选项（已处理 / 已删除），防底部悬浮条计数残留
function pruneBatchSel() {
  const valid = {};
  allGroups.value.forEach((g) =>
    (g.records || []).forEach((r) => {
      if (r.status !== 'handled') valid[r.id] = true;
    })
  );
  const next = {};
  Object.keys(batchSel.value).forEach((id) => {
    if (valid[id]) next[id] = true;
  });
  batchSel.value = next;
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
  return `${unh}${key} · 共 ${st.total} 条`;
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
  if (!ids.length) return;
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

// ---- 手动新增 / 编辑（统一弹层 RecordEditDialog；成功后真实刷新，失败弹层内提示且不丢输入） ----
const editVisible = ref(false);
const editRecord = ref(null);
// presetLineName 新增时预填当前台账线路（未匹配组无线路名，留空）
const presetLineName = computed(() =>
  view.value === 'ledger' && ledgerGroup.value && !ledgerGroup.value.unmatched
    ? ledgerGroup.value.lineName
    : ''
);

function openCreate() {
  editRecord.value = null;
  editVisible.value = true;
}

function openEdit(rec) {
  editRecord.value = rec;
  editVisible.value = true;
}

async function submitEdit(payload) {
  try {
    if (editRecord.value) {
      await api.updateRecord(editRecord.value.id, payload.body, teamId.value);
      payload.done?.();
      MessagePlugin.success('已保存');
    } else {
      await api.createRecord(payload.body, teamId.value);
      payload.done?.();
      MessagePlugin.success('已新增');
    }
    loadDashboard();
    loadRecords(true);
  } catch (e) {
    payload.fail?.(e.message || '保存失败');
    MessagePlugin.error(e.message || '保存失败');
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

// ---- 同步至内网 / 同步知识库（管理；旧版工具行口径） ----
const sync = ref(null); // {enabled,running,last,nextNight}
const syncBusy = ref(false);
const wxSyncing = ref(false);
const syncHint = computed(() => {
  const st = sync.value;
  if (!st) return '';
  if (st.running) return '同步中…';
  if (st.enabled === false) return '同步未启用';
  let text = '每日 24:00 自动同步';
  if (st.nextNight) text += ' · 下次 ' + fmtDate(st.nextNight, true);
  if (st.last)
    text +=
      ' · 最近 ' +
      fmtDate(st.last.created_at, true) +
      (st.last.result ? ' · ' + st.last.result : '');
  return text;
});

async function loadSyncStatus() {
  if (!isManager) return;
  try {
    sync.value = (await api.getSyncStatus(teamId.value)) || null;
  } catch (e) {
    sync.value = null;
  }
}
// pushSync 手动触发一轮内网同步：started 仅表示已发起，结果以后续状态与站内通知为准，不假成功。
async function pushSync() {
  if (syncBusy.value) return;
  syncBusy.value = true;
  try {
    await api.syncPushNow(teamId.value);
    MessagePlugin.success('已触发同步，结果见同步状态与站内通知');
    await loadSyncStatus();
  } catch (e) {
    MessagePlugin.error(e.message || '触发同步失败');
  } finally {
    syncBusy.value = false;
  }
}
// pushWxSync 知识库快照同步：errors 非空如实按部分失败提示，不假成功。
async function pushWxSync() {
  if (wxSyncing.value) return;
  wxSyncing.value = true;
  try {
    const d = (await api.weknoraPushNow(teamId.value)) || {};
    const errs = d.errors || [];
    if (errs.length) {
      if ((d.created || 0) + (d.updated || 0) + (d.deleted || 0) > 0) {
        MessagePlugin.warning(`已同步至知识库（失败 ${errs.length} 项）`);
      } else {
        MessagePlugin.error(errs[0]);
      }
    } else {
      MessagePlugin.success('已同步至知识库');
    }
  } catch (e) {
    MessagePlugin.error(e.message || '知识库同步失败');
  } finally {
    wxSyncing.value = false;
  }
}

function switchTab(t) {
  if (tab.value === t) return;
  tab.value = t;
  // 暂存区 / 冲突每次切回都重拉（可能被其他管理员改动）
  if (t === 'staging') loadStaging();
  if (t === 'conflicts') loadConflicts();
}

function loadAll() {
  loadDashboard();
  loadRecords();
  loadSyncStatus();
}

onMounted(() => {
  loadTeams().then(loadAll);
});
</script>

<style scoped>
/* ===== 整页骨架：页头/看板/工具行/页签固定，仅页签内容区内部滚动（同旧版 th-wrap 口径） ===== */
.th-wrap {
  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* 演示状态条：strong 不压折行（全局 flex 下会被挤窄换行） */
.demo-banner strong {
  flex: none;
  white-space: nowrap;
}

/* ===== 页头：kicker + 标题 + 副标；右侧班组切换 ===== */
.th-head {
  flex: none;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px 24px;
  flex-wrap: wrap;
}
.th-head .section-title {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0;
  margin: 0;
}
.th-kick {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--tk-brand);
  animation: th-fade 0.5s var(--tk-ease) 0.06s both;
}
.th-head h3 {
  margin: 6px 0 0;
  font-size: 24px;
  line-height: 1.2;
  font-weight: 700;
  letter-spacing: 1px;
  animation: th-fade 0.55s var(--tk-ease) 0.12s both;
}
.th-head .more {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  animation: th-fade 0.55s var(--tk-ease) 0.2s both;
}
.th-head .acts {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  animation: th-fade 0.55s var(--tk-ease) 0.28s both;
}
.th-team {
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  color: var(--tk-ink);
  font-size: 12.5px;
  font-family: inherit;
  outline: none;
}
.th-team:focus {
  border-color: var(--tk-brand);
}

/* ===== 看板四格卡（白卡 + tabular-nums；数字与标签基线同行） ===== */
.th-dash {
  flex: none;
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-top: 16px;
  animation: th-fade 0.55s var(--tk-ease) 0.3s both;
}
@media (max-width: 900px) {
  .th-dash {
    grid-template-columns: repeat(2, 1fr);
  }
}
.th-dash .dc {
  margin-top: 0; /* 全局 .card + .card 间距不进网格，防首卡被撑高、相邻卡下移不等高 */
  padding: 16px 20px 14px;
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.dc-n {
  font-size: 26px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
  color: var(--tk-ink);
}
.dc-n.blue {
  color: var(--tk-brand);
}
.dc-n.red {
  color: var(--tk-red);
}
.dc-n.green {
  color: var(--tk-green);
}
.dc-n.mut {
  font-size: 14px;
  color: var(--tk-ph);
  font-weight: 600;
  letter-spacing: 1px;
}
.dc-n.zero {
  color: var(--tk-ph);
} /* 要紧类计数为 0：不标红，弱化为辅助灰 */
.dc-t {
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
/* 班管/超管待办小字入口 */
.dash-todo {
  grid-column: 1 / -1;
  margin-top: -4px;
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.dash-todo > svg {
  width: 14px;
  height: 14px;
  color: var(--tk-yellow);
  flex: none;
}
.dash-todo a {
  font-weight: 700;
  cursor: pointer;
  color: var(--tk-brand);
}

/* ===== 工具行：阈值设置 / 同步至内网 / 同步知识库（管理角色可见） ===== */
.th-tools {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 16px;
  animation: th-fade 0.55s var(--tk-ease) 0.36s both;
}
.sync-hint {
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}

/* ===== 页签：下划线式（激活蓝字 + 2px 蓝条，角标红胶囊） ===== */
.th-tabs {
  flex: none;
  position: relative;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 14px;
  border-bottom: 1px solid var(--tk-line);
  animation: th-fade 0.55s var(--tk-ease) 0.4s both;
}
.th-tabs button {
  position: relative;
  padding: 9px 14px;
  border: none;
  background: transparent;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  font-weight: 600;
  font-family: inherit;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  transition: color 0.15s;
}
.th-tabs button:hover {
  color: var(--tk-ink);
}
.th-tabs button.on {
  color: var(--tk-brand);
}
.th-tabs button.on::after {
  content: '';
  position: absolute;
  left: 14px;
  right: 14px;
  bottom: -1px;
  height: 2px;
  background: var(--tk-brand);
  border-radius: 2px;
}
.bdg {
  min-width: 17px;
  height: 17px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--tk-red);
  color: var(--on-brand);
  font-size: 10.5px;
  font-weight: 700;
  line-height: 17px;
  text-align: center;
  font-variant-numeric: tabular-nums;
}

/* ===== 主体：三页签共用列表位，内容各自内部滚动 ===== */
.th-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  margin-top: 14px;
}
.th-body .pane {
  display: none;
  flex: 1;
  min-height: 0;
  flex-direction: column;
}
.th-body .pane.on {
  display: flex;
  animation: th-pane-in 0.34s var(--tk-ease);
}
.pane-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-bottom: 64px; /* 底部内容给右下角 FAB 留位 */
  scrollbar-width: thin;
  scrollbar-color: var(--dash) transparent;
}
.pane-scroll::-webkit-scrollbar {
  width: 5px;
}
.pane-scroll::-webkit-scrollbar-thumb {
  background: var(--dash);
  border-radius: 8px;
}
.pane-scroll::-webkit-scrollbar-thumb:hover {
  background: var(--ph);
}
.pane-scroll::-webkit-scrollbar-track {
  background: transparent;
}

/* ===== 记录页签顶栏（搜索框 / 台账标题计数 + 批量开关） ===== */
.rec-bar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.rec-bar .spring {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}
.rec-search {
  flex: 1;
  min-width: 220px;
  max-width: 420px;
  height: 36px;
  padding: 0 12px;
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--tk-fill);
  border-radius: 8px;
}
.rec-search svg {
  flex: none;
  width: 15px;
  height: 15px;
  color: var(--tk-ph);
}
.rec-search input {
  flex: 1;
  min-width: 0;
  border: none;
  background: transparent;
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  letter-spacing: 0.5px;
}
.ledger-title {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.ledger-sub {
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
.ledger-sub :deep(.ls-unh) {
  color: var(--tk-brand);
} /* 未处理>0：品牌蓝（与线路卡同口径） */
.ledger-sub :deep(.ls-key) {
  color: var(--tk-red);
} /* 要紧>0：危险红 */

/* ===== 一级「线路视图」：线路大卡片网格 ===== */
.lc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 14px;
}
.lc-grid .lc {
  margin-top: 0; /* 全局 .card + .card 间距不进网格，防相邻线路卡下移不等高 */
  padding: 18px 20px 16px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 12px;
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
  animation: th-rise 0.5s var(--tk-ease) both; /* 编排式上浮入场，逐卡时延链由行内 animation-delay 给出 */
}
.lc:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
.lc-main {
  flex: 1;
  min-width: 0;
}
.lc-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.lc-stats {
  margin-top: 10px;
  display: flex;
  align-items: baseline;
  gap: 14px;
  flex-wrap: wrap;
  font-size: 12px;
  letter-spacing: 0.5px;
}
.lc-stats b {
  margin-left: 3px;
  font-size: 17px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.lc-unh,
.lc-unh b {
  color: var(--tk-brand);
} /* 未处理：品牌蓝 */
.lc-key,
.lc-key b {
  color: var(--tk-red);
} /* 要紧：未处理且垂直距离 ≤ 阈值 */
.lc-total {
  color: var(--tk-ph);
}
.lc-clear .lc-unh,
.lc-clear .lc-unh b,
.lc-clear .lc-key,
.lc-clear .lc-key b {
  color: var(--tk-ph);
} /* 未处理与要紧均为 0：计数整体置灰 */
.lc-go {
  flex: none;
  display: flex;
  color: var(--tk-ph);
}

/* ===== 二级台账卡片流 ===== */
.ledger-flow {
  display: flex;
  flex-direction: column;
}
/* 视图切换 fade + slide 入场 */
.view-in {
  animation: th-rise 0.34s var(--tk-ease);
}

/* ===== 加载 / 空态 ===== */
.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: th-rot 1s linear infinite;
}
.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--tk-ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--tk-ph);
}
.empty svg {
  width: 44px;
  height: 44px;
  color: var(--tk-dash);
}
.empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
}

/* ===== 旧式按钮 ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  font-family: inherit;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn:active:not([disabled]) {
  transform: translateY(1px);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* ===== 批量处理底部悬浮条（胶囊） ===== */
.batch-bar {
  position: fixed;
  left: 50%;
  bottom: 56px;
  transform: translateX(-50%);
  z-index: 120;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 10px 12px 10px 20px;
  background: var(--card);
  border: 1px solid var(--tk-line);
  border-radius: 999px;
  box-shadow: var(--tk-shadow-hover);
  animation: th-fade 0.3s var(--tk-ease) both;
}
.batch-bar .bc {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}

/* ===== 导出 FAB（右下角悬浮圆形品牌蓝） ===== */
.fab {
  position: fixed;
  right: 28px;
  bottom: 28px;
  z-index: 110;
  width: 52px;
  height: 52px;
  border: none;
  border-radius: 50%;
  background: var(--tk-brand);
  color: var(--on-brand);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
}
.fab:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}

/* ===== 阈值设置弹层表单 ===== */
.th-form {
  display: flex;
  flex-direction: column;
}
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-lab:first-of-type {
  margin-top: 2px;
}
.f-sel {
  width: 100%;
  height: 40px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.f-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
}
.f-hint {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 入场关键帧 ===== */
@keyframes th-fade {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
@keyframes th-rise {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes th-rot {
  to {
    transform: rotate(360deg);
  }
}
@keyframes th-pane-in {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* ===== 动效降级 ===== */
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important; /* 时延链同步清零，避免降级后仍延迟浮现 */
    animation-iteration-count: 1 !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
