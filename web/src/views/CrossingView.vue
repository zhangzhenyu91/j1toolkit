<template>
  <!-- 旧版 crossing.html 骨架：页头 + 看板四格（仅记录页签）+ 工具行（管理）+ 下划线页签 + 页签内部滚动
       + 导出 FAB 弹层 -->
  <div class="cx-wrap">
    <!-- 页头：kicker + 标题 + 副标；右侧班组切换（仅超管可见） -->
    <div class="cx-head">
      <div class="section-title">
        <span class="cx-kick">LINE CROSSING</span>
        <h3>交跨记录</h3>
        <span class="more">输电线路交叉跨越台账的归档、核对与统计</span>
      </div>
      <div class="acts">
        <select
          v-if="teamOptions.length > 1"
          v-model="teamId"
          class="cx-team num"
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
    <div v-show="tab === 'records'" class="cx-dash">
      <div class="card dc">
        <span class="dc-n blue num">{{ dashVal(dash.total) }}</span>
        <span class="dc-t">记录总数</span>
      </div>
      <div class="card dc">
        <span class="dc-n num">{{ dashVal(dash.week_new) }}</span>
        <span class="dc-t">本周新增</span>
      </div>
      <div class="card dc">
        <span class="dc-n num" :class="hazClass">{{ dashVal(dash.hazard) }}</span>
        <span class="dc-t">存在隐患</span>
      </div>
      <div class="card dc">
        <span class="dc-n num">{{ dashVal(dash.pending_lines) }}</span>
        <span class="dc-t">待确认线路</span>
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

    <!-- 工具行（管理角色）：同步至内网 / 同步知识库 + 同步状态小字 -->
    <div v-if="isManager" class="cx-tools">
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
    <div class="cx-tabs">
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

    <!-- 页签主体（仅活动页签内容滚动） -->
    <div class="cx-body">
      <div class="pane" :class="{ on: tab === 'records' }">
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
        <!-- 二级「台账视图」顶栏：返回 + 线路标题与计数 -->
        <div v-else class="rec-bar">
          <button class="btn btn-sm" type="button" @click="backToLines">返回线路</button>
          <span class="ledger-title">{{ ledgerGroup ? ledgerGroup.title : '' }}</span>
          <span class="ledger-sub num" v-html="ledgerSubHtml"></span>
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
              :class="{ 'lc-clear': g.stats.hz === 0 }"
              :style="{ animationDelay: Math.min(idx * 0.05, 0.4).toFixed(2) + 's' }"
              title="点击进入线路台账"
              @click="openLedger(g.key)"
            >
              <div class="lc-main">
                <div class="lc-title">{{ g.title }}</div>
                <div class="lc-stats">
                  <span :class="g.stats.hz > 0 ? 'lc-key' : 'lc-total'"
                    >存在隐患<b class="num">{{ g.stats.hz }}</b></span
                  >
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
                :is-manager="isManager"
                @remove="removeRecord"
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
      <div class="pane" :class="{ on: tab === 'staging' }">
        <div class="pane-scroll">
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
      </div>

      <!-- 冲突页签（管理） -->
      <div class="pane" :class="{ on: tab === 'conflicts' }">
        <div class="pane-scroll">
          <ConflictPanel
            :conflicts="cftList"
            :loading="cftLoading"
            :error="cftError"
            @reload="loadConflicts"
            @resolve="onResolveConflict"
          />
        </div>
      </div>
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
import {
  fmtKv,
  fmtDate,
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
// 隐患类计数仅 >0 才红显，=0 弱化为辅助灰
const hazClass = computed(() => (Number(dash.value.hazard) > 0 ? 'red' : 'zero'));

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
const ledgerSubHtml = computed(() => {
  if (!ledgerGroup.value) return '';
  const st = lineStats(ledgerGroup.value.records);
  return `<span class="${st.hz > 0 ? 'ls-key' : ''}">存在隐患 ${st.hz}</span> · 共 ${st.total} 条`;
});

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
  if (!window.confirm('删除后不可恢复，确定删除吗？')) return;
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
/* ===== 整页骨架：页头/看板/工具行/页签固定，仅页签内容区内部滚动（同旧版 cx-wrap 口径） ===== */
.cx-wrap {
  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* ===== 页头 ===== */
.cx-head {
  flex: none;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px 24px;
  flex-wrap: wrap;
}
.cx-head .section-title {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0;
  margin: 0;
}
.cx-kick {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--tk-brand);
  animation: cx-fade 0.5s var(--tk-ease) 0.06s both;
}
.cx-head h3 {
  margin: 6px 0 0;
  font-size: 24px;
  line-height: 1.2;
  font-weight: 700;
  letter-spacing: 1px;
  animation: cx-fade 0.55s var(--tk-ease) 0.12s both;
}
.cx-head .more {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  animation: cx-fade 0.55s var(--tk-ease) 0.2s both;
}
.cx-head .acts {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  animation: cx-fade 0.55s var(--tk-ease) 0.28s both;
}
.cx-team {
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  font-size: 12.5px;
  font-family: inherit;
  outline: none;
}
.cx-team:focus {
  border-color: var(--tk-brand);
}

/* ===== 看板四格卡 ===== */
.cx-dash {
  flex: none;
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-top: 16px;
  animation: cx-fade 0.55s var(--tk-ease) 0.3s both;
}
@media (max-width: 900px) {
  .cx-dash {
    grid-template-columns: repeat(2, 1fr);
  }
}
.dc {
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
.dc-n.zero {
  color: var(--tk-ph);
} /* 隐患类计数为 0：不标红，弱化为辅助灰 */
.dc-t {
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
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

/* ===== 工具行：同步至内网 / 同步知识库（管理角色可见） ===== */
.cx-tools {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 16px;
  animation: cx-fade 0.55s var(--tk-ease) 0.36s both;
}
.sync-hint {
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}

/* ===== 页签：下划线式（激活蓝字 + 2px 蓝条，角标红胶囊） ===== */
.cx-tabs {
  flex: none;
  position: relative;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 14px;
  border-bottom: 1px solid var(--tk-line);
  animation: cx-fade 0.55s var(--tk-ease) 0.4s both;
}
.cx-tabs button {
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
.cx-tabs button:hover {
  color: var(--tk-ink);
}
.cx-tabs button.on {
  color: var(--tk-brand);
}
.cx-tabs button.on::after {
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
  color: #fff;
  font-size: 10.5px;
  font-weight: 700;
  line-height: 17px;
  text-align: center;
  font-variant-numeric: tabular-nums;
}

/* ===== 主体：三页签共用列表位，内容各自内部滚动 ===== */
.cx-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  margin-top: 14px;
}
.cx-body .pane {
  display: none;
  flex: 1;
  min-height: 0;
  flex-direction: column;
}
.cx-body .pane.on {
  display: flex;
  animation: cx-pane-in 0.34s var(--tk-ease);
}
.pane-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-right: 10px;
  padding-bottom: 64px; /* 底部内容给右下角 FAB 留位 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.pane-scroll::-webkit-scrollbar {
  width: 5px;
}
.pane-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.pane-scroll::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.pane-scroll::-webkit-scrollbar-track {
  background: transparent;
}

/* ===== 记录页签顶栏 ===== */
.rec-bar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 12px;
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
.ledger-sub :deep(.ls-key) {
  color: var(--tk-red);
} /* 存在隐患>0：危险红 */

/* ===== 一级「线路视图」：线路大卡片网格 ===== */
.lc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 14px;
}
.lc {
  padding: 18px 20px 16px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 12px;
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
  animation: cx-rise 0.5s var(--tk-ease) both; /* 编排式上浮入场，逐卡时延链由行内 animation-delay 给出 */
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
.lc-key,
.lc-key b {
  color: var(--tk-red);
} /* 存在隐患：>0 红字 */
.lc-total {
  color: var(--tk-ph);
}
.lc-clear .lc-key,
.lc-clear .lc-key b {
  color: var(--tk-ph);
} /* 隐患为 0：计数整体置灰 */
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
  animation: cx-rise 0.34s var(--tk-ease);
}

/* ===== 加载 / 空态 ===== */
.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: cx-rot 1s linear infinite;
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
  background: #fff;
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
  color: #fff;
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: #fff;
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
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
  color: #fff;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 8px 20px rgba(14, 61, 168, 0.32);
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
}
.fab:hover {
  transform: translateY(-2px);
  box-shadow: 0 12px 26px rgba(14, 61, 168, 0.4);
}

/* ===== 入场关键帧 ===== */
@keyframes cx-fade {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
@keyframes cx-rise {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes cx-rot {
  to {
    transform: rotate(360deg);
  }
}
@keyframes cx-pane-in {
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
    animation-iteration-count: 1 !important;
    transition-duration: 0.001s !important;
  }
}
</style>
