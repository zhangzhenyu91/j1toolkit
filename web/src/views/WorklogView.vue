<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">出工日志</h1>
      <span class="page-sub">月历验证状态色点，点击日期查看当日记录</span>
    </div>

    <!-- 顶栏工具条：班组切换（仅超管）+ 范围切换 + 商旅入口 + 新建 -->
    <div class="toolbar">
      <select
        v-if="isCrossAdmin"
        v-model="teamId"
        class="wl-team-sel num"
        title="仅超级管理员可切换班组；其余角色固定为本班组"
        @change="onTeamChange"
      >
        <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
      </select>
      <t-radio-group v-model="scope" variant="default-filled" @change="onScopeChange">
        <t-radio-button value="all">全部</t-radio-button>
        <t-radio-button value="mine">仅看我</t-radio-button>
      </t-radio-group>
      <div class="spacer"></div>
      <t-button variant="outline" @click="bindOpen = true">商旅账号</t-button>
      <t-button variant="outline" @click="opOpen = true">同步任务</t-button>
      <t-button v-if="isSheetAdmin" variant="outline" @click="pullOpen = true">从商旅拉取</t-button>
      <t-button v-if="isCrossAdmin" variant="outline" @click="openCross()">新建跨班日志</t-button>
      <t-button @click="openCreate()">新建日志</t-button>
    </div>

    <div class="two-col">
      <!-- 左：筛选月历 + 本月统计 -->
      <div class="col-side">
        <div class="card">
          <MonthCalendar
            v-model:month="month"
            v-model:selected="date"
            :status-map="dayStatus"
            @update:selected="loadLogs"
            @update:month="onMonthChange"
          />
          <div class="wl-stats num">
            本月通过 {{ stats.passed }}/{{ stats.total }}
            <span v-if="stats.needPhotos" class="wl-need">我缺照片 {{ stats.needPhotos }}</span>
          </div>
        </div>
      </div>

      <!-- 右：页签主列 -->
      <div class="col-main">
        <div class="subnav">
          <button
            v-for="t in visibleTabs"
            :key="t.key"
            type="button"
            class="subnav-item"
            :class="{ active: tab === t.key }"
            @click="switchTab(t.key)"
          >
            {{ t.label }}
          </button>
        </div>

        <!-- 记录 -->
        <template v-if="tab === 'rec'">
          <div class="wl-date-head num">{{ date }}</div>
          <div v-if="loading" class="muted">加载中…</div>
          <div v-else-if="!logs.length" class="card wl-empty muted">
            当日暂无记录，点右上「新建日志」开始
          </div>
          <template v-else>
            <LogCard
              v-for="e in logs"
              :key="e.id"
              :entry="e"
              :eff-team-id="effTeamId"
              :op-lock="opLocks[e.id] || null"
              @edit="openEdit"
              @remove="removeLog"
              @check="toggleCheck"
              @manage-photos="openPhotos"
              @open-photo="openLightbox"
              @clock-tap="openClock"
              @fee-tap="openFee"
              @sync-card="cardSync"
              @remark-open="openRemark"
            />
          </template>
        </template>

        <!-- 批量下载 -->
        <div v-else-if="tab === 'dl'" class="card">
          <div class="toolbar">
            <span class="field-label">起</span>
            <input v-model="dl.from" type="date" class="wl-range num" />
            <span class="field-label">止</span>
            <input v-model="dl.to" type="date" class="wl-range num" />
            <t-button size="small" variant="outline" @click="loadPhotos">查询</t-button>
            <div class="spacer"></div>
            <t-button size="small" variant="outline" :loading="dl.pdfing" @click="savePDF"
              >组合 PDF</t-button
            >
            <t-button
              size="small"
              variant="outline"
              :loading="dl.pdfSaving"
              @click="savePDFNetdisk"
            >
              PDF 存网盘
            </t-button>
            <t-button
              size="small"
              variant="outline"
              :disabled="!dl.selected.length"
              :loading="dl.zipping"
              @click="zipSelected"
            >
              打包 ZIP（{{ dl.selected.length }}）
            </t-button>
            <t-button
              size="small"
              :disabled="!dl.selected.length"
              :loading="dl.saving"
              @click="saveSelected"
            >
              下载选中（{{ dl.selected.length }}）
            </t-button>
          </div>
          <div v-if="dl.error" class="wl-error">{{ dl.error }}</div>
          <div v-if="!dlGroups.length" class="muted">该范围暂无照片</div>
          <div v-for="g in dlGroups" :key="g.month" class="wl-dl-group">
            <div class="wl-dl-month num">{{ g.month }}</div>
            <div class="wl-dl-grid">
              <label v-for="p in g.items" :key="p.id" class="wl-dl-item">
                <input v-model="dl.selected" type="checkbox" :value="p.id" />
                <img :src="p.url" loading="lazy" alt="照片" />
                <span class="num">{{ p.day }}日 · {{ (p.members || []).join('、') }}</span>
              </label>
            </div>
          </div>
        </div>

        <!-- 汇总前核验 -->
        <div v-else-if="tab === 'rep'" class="card">
          <div class="toolbar">
            <button type="button" class="wl-cal-nav" @click="shiftVerifyMonth(-1)">‹</button>
            <span class="num wl-rep-month">{{ rep.month }}</span>
            <button type="button" class="wl-cal-nav" @click="shiftVerifyMonth(1)">›</button>
            <div class="spacer"></div>
            <t-button size="small" variant="outline" @click="loadReport">刷新</t-button>
          </div>
          <div v-if="rep.error" class="wl-error">{{ rep.error }}</div>
          <div v-if="!rep.list.length" class="wl-pass-bar">本月记录全部通过核验</div>
          <template v-else>
            <div v-for="it in rep.list" :key="it.id" class="wl-rep-row">
              <div class="wl-rep-main">
                <span class="num wl-rep-date">{{ it.log_date }}</span>
                <span class="sub-text">{{ it.plate_no }}</span>
                <span class="muted">{{ (it.members || []).join('、') }}</span>
              </div>
              <div class="wl-rep-detail">
                <span class="dot-tag" :class="it.verify === 'failed' ? 'red' : 'orange'">
                  <span class="dot"></span>{{ it.verify === 'failed' ? '未通过' : '有备注' }}
                </span>
                <div v-for="(r, i) in it.reasons || []" :key="i" class="wl-rep-reason">{{ r }}</div>
                <div v-if="it.remark" class="wl-rep-reason">备注：{{ it.remark }}</div>
              </div>
            </div>
            <div class="wl-pass-bar">
              其余 {{ Math.max(rep.total - rep.list.length, 0) }} 条记录全部通过
            </div>
          </template>
        </div>

        <!-- 工作任务单 / 费用汇总 / 派车汇总 -->
        <div v-else-if="tab === 'ts' || tab === 'fs' || tab === 'ds'" class="card">
          <div class="wl-sheet-desc sub-text">
            {{
              tab === 'ts'
                ? '按日期范围生成出车卡片合并 Word 工作任务单（一卡一页，范围不超过 31 天）。'
                : tab === 'fs'
                  ? '按日期范围生成「人 × 日」费用矩阵 Word（一卡一行、一人一列、末尾合计）。'
                  : '按日期范围导出派车单号清单 Excel（派车单号 + 用车日期两列）。'
            }}
          </div>
          <div class="toolbar">
            <span class="field-label">起</span>
            <input v-model="sheet.from" type="date" class="wl-range num" />
            <span class="field-label">止</span>
            <input v-model="sheet.to" type="date" class="wl-range num" />
            <div class="spacer"></div>
            <t-button variant="outline" :loading="sheet.previewing" @click="previewSheet"
              >预览</t-button
            >
            <t-button variant="outline" :loading="sheet.netdisking" @click="saveSheetNetdisk(false)"
              >存网盘</t-button
            >
            <t-button :loading="sheet.loading" @click="saveSheet(false)">下载</t-button>
          </div>
          <div v-if="sheet.error" class="wl-error">{{ sheet.error }}</div>
        </div>

        <!-- 派车对齐 -->
        <div v-else class="card">
          <div class="wl-sheet-desc sub-text">
            导入派车系统导出表（.xlsx，可多份合并），按「日期＋用车人」对齐当天出车卡片，只读对齐不写库；
            更正走卡片「编辑」，表格有系统无的补建走「新建日志」。
          </div>
          <div class="toolbar">
            <input type="file" accept=".xlsx" multiple class="wl-range" @change="onAlignFiles" />
            <t-button :disabled="!align.files.length" :loading="align.loading" @click="runAlign">
              上传并对齐
            </t-button>
          </div>
          <div v-if="align.error" class="wl-error">{{ align.error }}</div>
          <template v-if="align.result">
            <div class="wl-align-sum">
              <span class="dot-tag green"
                ><span class="dot"></span>一致 {{ align.result.summary.consistent }}</span
              >
              <span class="dot-tag red"
                ><span class="dot"></span>车牌 {{ align.result.summary.plate }}</span
              >
              <span class="dot-tag red"
                ><span class="dot"></span>用车人 {{ align.result.summary.members }}</span
              >
              <span class="dot-tag red"
                ><span class="dot"></span>目的地 {{ align.result.summary.destination }}</span
              >
              <span class="dot-tag red"
                ><span class="dot"></span>派车单号 {{ align.result.summary.orderNo }}</span
              >
              <span class="dot-tag orange"
                ><span class="dot"></span>表有卡无 {{ align.result.summary.sheetOnly }}</span
              >
              <span class="dot-tag orange"
                ><span class="dot"></span>卡有表无 {{ align.result.summary.entryOnly }}</span
              >
              <span class="muted">
                导入 {{ align.result.summary.imported }} 行 / 跳过
                {{ align.result.summary.skipped }} 行 / 他班剔除
                {{ align.result.summary.outside }} 行
              </span>
            </div>
            <div v-for="(it, i) in align.result.items" :key="i" class="wl-rep-row">
              <div class="wl-rep-main">
                <span class="num wl-rep-date">{{ it.date }}</span>
                <span class="dot-tag" :class="it.kind === 'matched' ? 'red' : 'orange'">
                  <span class="dot"></span>
                  {{
                    it.kind === 'matched'
                      ? '有差异'
                      : it.kind === 'sheetOnly'
                        ? '表有卡无'
                        : '卡有表无'
                  }}
                </span>
              </div>
              <div class="wl-rep-detail">
                <div v-if="it.sheet" class="sub-text">
                  表格：{{ it.sheet.plate || '—' }} · {{ (it.sheet.members || []).join('、') }}
                  <span v-if="it.sheet.to"> → {{ it.sheet.to }}</span>
                  <span v-if="it.sheet.orderNo" class="num"> · {{ it.sheet.orderNo }}</span>
                </div>
                <div v-if="it.entry" class="sub-text">
                  卡片：{{ it.entry.plate || '未出车' }} ·
                  {{ ((it.entry.members || []).map((m) => m.name) || []).join('、') }}
                  <span v-if="it.entry.destination"> → {{ it.entry.destination }}</span>
                  <span v-if="it.entry.order_no" class="num"> · {{ it.entry.order_no }}</span>
                </div>
                <div v-if="it.diffs" class="wl-rep-reason">
                  <span v-if="it.diffs.plate">车牌不一致；</span>
                  <span v-if="it.diffs.missing && it.diffs.missing.length">
                    表格多出：{{ it.diffs.missing.join('、') }}；
                  </span>
                  <span v-if="it.diffs.extra && it.diffs.extra.length">
                    卡片多出：{{ it.diffs.extra.join('、') }}；
                  </span>
                  <span v-if="it.diffs.dest">目的地不一致；</span>
                  <span v-if="it.diffs.orderNo">派车单号不一致</span>
                </div>
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 弹窗 -->
    <LogFormDialog
      v-model:open="formOpen"
      :entry="editing"
      :meta="meta"
      :default-date="date"
      @saved="onSaved"
    />
    <CrossLogDialog
      v-model:open="crossOpen"
      :entry="crossEditing"
      :meta="crossMeta"
      :default-date="date"
      @saved="onSaved"
    />
    <PhotoManagerDialog
      v-model:open="photoOpen"
      :entry="photoEntry || { id: 0, members: [], photos: [] }"
      @changed="onSaved"
    />
    <PhotoLightbox
      v-model:open="lightboxOpen"
      v-model:index="lightboxIndex"
      :photos="lightboxPhotos"
      :entry-id="lightboxEntryId"
      @op-started="onOpStarted"
    />
    <ClockinDialog
      v-model:open="clockOpen"
      :entry="clockCtx.entry"
      :member-id="clockCtx.memberId"
      :name="clockCtx.name"
      :seq="clockCtx.seq"
      :action="clockCtx.action"
      :date="clockCtx.date"
      @saved="onClockSaved"
    />
    <FeeDialog
      v-model:open="feeOpen"
      :member-id="feeCtx.memberId"
      :name="feeCtx.name"
      :date="feeCtx.date"
      :fee-std="feeCtx.feeStd"
      @saved="onSaved"
    />
    <BindDialog v-model:open="bindOpen" @bound="onSaved" />
    <SyncPullDialog
      v-model:open="pullOpen"
      :team-warn="isCrossAdmin && Number(teamId) > 0 && Number(teamId) !== Number(user.team_id)"
      @done="onSaved"
    />
    <OpQueueDialog v-model:open="opOpen" @changed="noop" />

    <!-- 汇总前核验拦截弹层（仍要生成 force=1） -->
    <div v-if="force.open" class="wl-modal-mask" @click.self="force.open = false">
      <div class="wl-modal card">
        <h3 class="card-title">存在未通过项</h3>
        <div class="sub-text">
          所选范围内有 {{ force.failures.length }} 条记录存在未通过项，请处理后再操作。
        </div>
        <div v-for="(f, i) in force.failures" :key="i" class="wl-rep-row">
          <div class="wl-rep-main">
            <span class="num wl-rep-date">{{ f.log_date }}</span>
            <span class="sub-text">{{ f.plate_no }}</span>
            <span class="muted">{{ (f.members || []).join('、') }}</span>
          </div>
          <div class="wl-rep-detail">
            <div v-for="(r, j) in f.reasons || []" :key="j" class="wl-rep-reason">{{ r }}</div>
          </div>
        </div>
        <div class="wl-modal-foot">
          <t-button variant="outline" @click="force.open = false">取消</t-button>
          <t-button @click="forceContinue">仍要生成</t-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import LogCard from '../features/worklog/components/LogCard.vue';
import LogFormDialog from '../features/worklog/components/LogFormDialog.vue';
import CrossLogDialog from '../features/worklog/components/CrossLogDialog.vue';
import PhotoManagerDialog from '../features/worklog/components/PhotoManagerDialog.vue';
import PhotoLightbox from '../features/worklog/components/PhotoLightbox.vue';
import MonthCalendar from '../features/worklog/components/MonthCalendar.vue';
import ClockinDialog from '../features/sgccclockin/components/ClockinDialog.vue';
import FeeDialog from '../features/sgccclockin/components/FeeDialog.vue';
import BindDialog from '../features/sgccclockin/components/BindDialog.vue';
import SyncPullDialog from '../features/sgccclockin/components/SyncPullDialog.vue';
import OpQueueDialog from '../features/sgccclockin/components/OpQueueDialog.vue';
import {
  deleteLog,
  dispatchAlign,
  downloadFeeSheet,
  downloadDispatchSheet,
  downloadPhotosPDF,
  downloadPhoto,
  downloadTaskSheet,
  downloadRemarkFile,
  fetchCrossMeta,
  fetchDayStatus,
  fetchLogs,
  fetchMeta,
  fetchPhotos,
  fetchRemarkPreview,
  fetchReport,
  fetchSheetPreview,
  fetchStats,
  postZipAndSave,
  savePhotosPDFNetdisk,
  saveSheetNetdisk as saveSheetToNetdisk,
  setTeamId,
  toggleMemberCheck,
} from '../features/worklog/api.js';
import {
  fetchOpStatus,
  fetchSyncActive,
  fetchSyncProgress,
  syncPull,
} from '../features/sgccclockin/api.js';

// 出工日志视图：左筛选月历 + 右内容主列（记录 / 批量下载 / 汇总前核验 / 三个汇总表 / 派车对齐）。
// 数据一律走 features 真实 Go API，失败如实提示，不伪造成功；超管经 ?team_id= 切换生效班组。

const user = currentUser() || {};
const isCrossAdmin = user.role === 'admin';
const isSheetAdmin = user.role === 'admin' || user.role === 'team_admin';

const scope = ref('all'); // all | mine
const month = ref(new Date().toISOString().slice(0, 7));
const date = ref(new Date().toISOString().slice(0, 10));
const dayStatus = ref({});
const stats = ref({ total: 0, passed: 0, needPhotos: 0 });
const logs = ref([]);
const loading = ref(false);
const meta = ref({ vehicles: [], destinations: [], members: [] });

// 班组切换（仅超管）：localStorage.worklog_team_id 持久化，全部 /worklog/* 请求统一带 team_id。
const teamId = ref(0);
const teams = ref([]);
const effTeamId = computed(() =>
  isCrossAdmin ? Number(teamId.value) || 0 : Number(user.team_id) || 0
);

const tab = ref('rec');
const allTabs = [
  { key: 'rec', label: '记录', admin: false },
  { key: 'dl', label: '批量下载', admin: false },
  { key: 'rep', label: '汇总前核验', admin: false },
  { key: 'ts', label: '工作任务单', admin: true },
  { key: 'fs', label: '费用汇总', admin: true },
  { key: 'ds', label: '派车汇总', admin: true },
  { key: 'pa', label: '派车对齐', admin: true },
];
const visibleTabs = computed(() => allTabs.filter((t) => !t.admin || isSheetAdmin));

// 批量下载状态
const today = new Date();
const monthStart = `${month.value}-01`;
const monthEnd = new Date(today.getFullYear(), today.getMonth() + 1, 0).toISOString().slice(0, 10);
const dl = reactive({
  from: monthStart,
  to: monthEnd,
  list: [],
  selected: [],
  saving: false,
  pdfing: false,
  pdfSaving: false,
  zipping: false,
  error: '',
});
// dl.groups 按月分组
const dlGroups = computed(() => {
  const byMonth = new Map();
  (dl.list || []).forEach((p) => {
    if (!byMonth.has(p.month)) byMonth.set(p.month, []);
    byMonth.get(p.month).push(p);
  });
  return [...byMonth.entries()].map(([m, items]) => ({ month: m, items }));
});

// 汇总前核验状态
const rep = reactive({
  month: new Date().toISOString().slice(0, 7),
  list: [],
  total: 0,
  error: '',
});

// 汇总表状态
const sheet = reactive({
  from: monthStart,
  to: monthEnd,
  loading: false,
  previewing: false,
  netdisking: false,
  error: '',
});
const force = reactive({ open: false, failures: [], kind: 'ts', action: 'download' });

// 派车对齐状态
const align = reactive({ files: [], loading: false, error: '', result: null });

// 弹窗状态
const formOpen = ref(false);
const editing = ref(null);
const crossOpen = ref(false);
const crossEditing = ref(null);
const crossMeta = ref({ teams: [], vehicles: [], destinations: [], members: [] });
const photoOpen = ref(false);
const photoEntry = ref(null);
const lightboxOpen = ref(false);
const lightboxIndex = ref(0);
const lightboxPhotos = ref([]);
const lightboxEntryId = ref(0);
const clockOpen = ref(false);
const clockCtx = reactive({ entry: null, memberId: 0, name: '', seq: 1, action: 'mark', date: '' });
const feeOpen = ref(false);
const feeCtx = reactive({ memberId: 0, name: '', date: '', feeStd: null });
const bindOpen = ref(false);
const pullOpen = ref(false);
const opOpen = ref(false);

// opLocks 卡片级任务锁（照片 op / 卡片级从商旅同步）：entryId → {pct, text}
const opLocks = reactive({});

function noop() {}

// ---- 加载 ----

// loadMonth 月历着色 + 本月统计。
async function loadMonth() {
  try {
    const [st, sp] = await Promise.all([
      fetchDayStatus({ month: month.value, scope: scope.value }),
      fetchStats({ month: month.value }),
    ]);
    dayStatus.value = st.map || {};
    stats.value = sp || { total: 0, passed: 0, needPhotos: 0 };
  } catch (e) {
    MessagePlugin.error(e.message || '月历加载失败');
  }
}

// loadLogs 当日卡片全量。
async function loadLogs() {
  loading.value = true;
  try {
    const data = await fetchLogs({ date: date.value, scope: scope.value });
    logs.value = data.list || [];
  } catch (e) {
    MessagePlugin.error(e.message || '日志加载失败');
    logs.value = [];
  } finally {
    loading.value = false;
  }
}

// loadPhotos 批量下载照片清单。
async function loadPhotos() {
  dl.error = '';
  try {
    const data = await fetchPhotos({ from: dl.from, to: dl.to });
    dl.list = data.list || [];
    dl.selected = [];
  } catch (e) {
    dl.error = e.message || '照片清单加载失败';
    dl.list = [];
  }
}

// loadReport 汇总前核验清单（整月）。
async function loadReport() {
  rep.error = '';
  try {
    const [y, m] = rep.month.split('-').map(Number);
    const last = new Date(y, m, 0).getDate();
    const data = await fetchReport({
      from: `${rep.month}-01`,
      to: `${rep.month}-${String(last).padStart(2, '0')}`,
      scope: scope.value,
    });
    rep.list = data.list || [];
    rep.total = data.total || 0;
  } catch (e) {
    rep.error = e.message || '核验清单加载失败';
    rep.list = [];
  }
}

// loadTeams 超管班组切换数据源（跨班元数据含全部启用班组）。
async function loadTeams() {
  if (!isCrossAdmin) return;
  try {
    const data = await fetchCrossMeta();
    teams.value = data.teams || [];
  } catch (e) {
    MessagePlugin.error(e.message || '班组列表加载失败');
  }
}

// ---- 交互 ----

function onScopeChange() {
  loadMonth();
  loadLogs();
  if (tab.value === 'rep') loadReport();
}

function onMonthChange() {
  loadMonth();
}

function switchTab(key) {
  tab.value = key;
  if (key === 'dl' && !dl.list.length) loadPhotos();
  if (key === 'rep') loadReport();
}

// onTeamChange 超管切换班组：写入持久化并整页数据重载。
function onTeamChange() {
  setTeamId(Number(teamId.value) || 0);
  try {
    localStorage.setItem('worklog_team_id', String(teamId.value || 0));
  } catch (e) {
    // 存储不可用忽略
  }
  meta.value = { vehicles: [], destinations: [], members: [] };
  logs.value = [];
  dl.list = [];
  rep.list = [];
  Promise.all([fetchMeta().then((d) => (meta.value = d))]).catch(() => {});
  loadMonth();
  loadLogs();
}

// onSaved 保存后联动强刷日志与月历。
function onSaved() {
  loadLogs();
  loadMonth();
}

function openCreate() {
  editing.value = null;
  formOpen.value = true;
}

function openEdit(entry) {
  if (entry.cross_team === 1 && !isCrossAdmin) {
    MessagePlugin.warning('跨班日志仅超级管理员可修改派车');
    return;
  }
  editing.value = entry;
  formOpen.value = true;
}

async function openCross() {
  crossEditing.value = null;
  crossOpen.value = true;
  try {
    crossMeta.value = await fetchCrossMeta();
  } catch (e) {
    MessagePlugin.error(e.message || '跨班数据加载失败');
  }
}

// removeLog 删除卡片（确认后调 API）。
function removeLog(entry) {
  const dlg = DialogPlugin.confirm({
    header: '删除日志',
    body: `确定删除 ${entry.log_date}「${entry.plate_no || '未出车'}」的日志？照片将一并删除。`,
    confirmBtn: '删除',
    onConfirm: async () => {
      try {
        await deleteLog(entry.id);
        MessagePlugin.success('已删除');
        onSaved();
      } catch (e) {
        MessagePlugin.error(e.message || '删除失败');
      } finally {
        dlg.destroy();
      }
    },
  });
}

// toggleCheck 打卡切换。
async function toggleCheck({ entry, row }) {
  try {
    const data = await toggleMemberCheck(entry.id, row.id);
    row.checked = data.checked;
  } catch (e) {
    MessagePlugin.error(e.message || '打卡失败');
  }
}

function openPhotos(entry) {
  photoEntry.value = entry;
  photoOpen.value = true;
}

// openLightbox 照片大图预览。
function openLightbox({ entry, index }) {
  lightboxPhotos.value = entry.photos || [];
  lightboxIndex.value = index;
  lightboxEntryId.value = entry.id;
  lightboxOpen.value = true;
}

// openRemark 备注附件：Office 文档在线预览、图片/视频直接打开、其余走下载代理。
async function openRemark({ entry, file }) {
  try {
    if (file.type === 'doc') {
      const data = await fetchRemarkPreview(entry.id, file.cos_key);
      if (data && data.url) {
        window.open(data.url, '_blank', 'noopener');
        return;
      }
    }
    if (file.type === 'image' || file.type === 'video') {
      window.open(file.url, '_blank', 'noopener');
      return;
    }
    await downloadRemarkFile(entry.id, file.cos_key, file.name);
  } catch (e) {
    MessagePlugin.error(e.message || '附件预览失败');
  }
}

// ---- 商旅打卡 / 费用 / 同步 ----

// openClock 打卡确认弹层。
function openClock({ entry, member, seq, action }) {
  if (date.value !== new Date().toISOString().slice(0, 10)) {
    MessagePlugin.info('仅当日日期可打卡');
    return;
  }
  if (member.sgccBound === false) {
    MessagePlugin.info('该用车人未绑定商旅，请引导其本人点右上「商旅账号」绑定');
    return;
  }
  if (member.sgccTokenStatus === 0) {
    MessagePlugin.info('该用车人商旅登录已过期，请其本人点右上「商旅账号」重新登录');
    return;
  }
  clockCtx.entry = entry;
  clockCtx.memberId = member.member_id;
  clockCtx.name = member.name;
  clockCtx.seq = seq;
  clockCtx.action = action;
  clockCtx.date = entry.log_date;
  clockOpen.value = true;
}

// onClockSaved 打卡成功：刷新；首次「开始打卡」成功自动弹费用层（同小程序口径）。
function onClockSaved({ seq, action }) {
  onSaved();
  if (action === 'mark' && seq === 1) {
    const entry = clockCtx.entry;
    openFee({ entry, member: { member_id: clockCtx.memberId, name: clockCtx.name } });
  }
}

// openFee 费用信息弹层。
function openFee({ entry, member }) {
  feeCtx.memberId = member.member_id;
  feeCtx.name = member.name;
  feeCtx.date = entry.log_date;
  feeCtx.feeStd = entry.feeStd || null;
  feeOpen.value = true;
}

// cardSync 卡片级「从商旅同步」：逐（成员 × 当日）拉取并聚合 /sync/progress，本卡挂任务锁。
async function cardSync(entry) {
  if (opLocks[entry.id]) return;
  const pairs = (entry.members || [])
    .filter((m) => m.member_id && m.sgccBound !== false)
    .map((m) => ({ memberId: m.member_id, name: m.name }));
  if (!pairs.length) {
    MessagePlugin.info('本卡没有已绑定商旅的用车人');
    return;
  }
  opLocks[entry.id] = { pct: 0, text: '从商旅同步中' };
  const jobIds = [];
  let failed = 0;
  let skipped = 0;
  try {
    for (const p of pairs) {
      try {
        const d = await syncPull({ member_id: p.memberId, date: entry.log_date });
        if (d && d.jobId) jobIds.push(d.jobId);
      } catch (e) {
        if (e.code === 40020 || e.code === 40021) skipped += 1;
        else failed += 1;
      }
    }
    const pending = new Set(jobIds);
    let netFails = 0;
    while (pending.size) {
      for (const id of [...pending]) {
        try {
          const job = await fetchSyncProgress(id);
          netFails = 0;
          if (!job || job.finished) {
            pending.delete(id);
            if (job && Number(job.errors) > 0) failed += 1;
          } else if (job && job.total) {
            const doneCount = job.done || 0;
            if (opLocks[entry.id]) {
              opLocks[entry.id].pct = Math.min(99, Math.round((doneCount / job.total) * 100));
            }
          }
        } catch (e) {
          netFails += 1;
          if (e.code === 40400) pending.delete(id);
          else if (netFails > 20) pending.clear();
        }
      }
      if (pending.size) {
        // 多任务聚合进度：按完成任务数折算（任务锁可能被旁观者清理，安全写）
        if (opLocks[entry.id]) {
          opLocks[entry.id].pct = Math.min(
            99,
            Math.round(((jobIds.length - pending.size) / Math.max(jobIds.length, 1)) * 100)
          );
        }
        await new Promise((r) => setTimeout(r, 1500));
      }
    }
    if (failed && failed >= pairs.length) MessagePlugin.error('本卡同步失败，请稍后重试');
    else if (failed) MessagePlugin.error(`本卡已同步，${failed} 人失败（详见同步任务核查流水）`);
    else MessagePlugin.success('本卡已从商旅同步');
  } finally {
    delete opLocks[entry.id];
    onSaved();
  }
}

// onOpStarted 照片任务（大图重试同步）登记卡片任务锁并轮询收尾。
function onOpStarted({ opId, entryId }) {
  if (!opId) return;
  opLocks[entryId] = { pct: 0, text: '商旅重新同步中' };
  pollOp(entryId, opId);
}

// pollOp 发起者轮询 /op/status 至终态（Go PhotoOp 终态：step=done/failed）。
async function pollOp(entryId, opId) {
  let fails = 0;
  for (;;) {
    let st = null;
    try {
      st = await fetchOpStatus(opId);
      fails = 0;
    } catch (e) {
      fails += 1;
    }
    if (st && st.step === 'done') {
      MessagePlugin.success('同步成功');
      delete opLocks[entryId];
      onSaved();
      return;
    }
    if (st && (st.failed || st.step === 'failed')) {
      MessagePlugin.error(st.error || '商旅同步失败，请重试');
      delete opLocks[entryId];
      onSaved();
      return;
    }
    if (st) {
      opLocks[entryId] = {
        pct: Math.min(99, st.progress || 0),
        text: st.step === 'running' ? '商旅重新同步中' : st.step || '商旅重新同步中',
      };
    }
    if (fails > 20) {
      delete opLocks[entryId];
      onSaved();
      return;
    }
    await new Promise((r) => setTimeout(r, 1500));
  }
}

// syncSpectators 旁观者挂条：/sync/active 的 ops → 对应卡片任务锁（他人/他端发起也可见）。
async function syncSpectators() {
  try {
    const data = await fetchSyncActive();
    const seen = {};
    ((data && data.ops) || []).forEach((o) => {
      if (!o.entry_id) return;
      seen[o.entry_id] = true;
      if (!opLocks[o.entry_id]) {
        opLocks[o.entry_id] = {
          pct: Math.min(99, o.progress || 0),
          text: o.step || '商旅同步中',
          spectator: true,
        };
      } else if (opLocks[o.entry_id].spectator) {
        opLocks[o.entry_id].pct = Math.min(99, o.progress || 0);
      }
    });
    Object.keys(opLocks).forEach((eid) => {
      // 发起者本地登记的任务锁不在此摘（由各自轮询收口）
      if (!seen[eid] && opLocks[eid] && opLocks[eid].spectator) delete opLocks[eid];
    });
  } catch (e) {
    // 探测失败忽略（旁观者锁为增强能力）
  }
}

// ---- 批量下载 ----

// saveSelected 批量下载选中照片（逐张 fetch+blob 带鉴权）。
async function saveSelected() {
  dl.saving = true;
  dl.error = '';
  try {
    const byId = new Map((dl.list || []).map((p) => [p.id, p]));
    for (const id of dl.selected) {
      const p = byId.get(id);
      await downloadPhoto(id, p ? `${p.log_date}-${id}.jpg` : `photo-${id}.jpg`);
    }
    MessagePlugin.success(`已下载 ${dl.selected.length} 张`);
  } catch (e) {
    dl.error = e.message || '下载失败';
  } finally {
    dl.saving = false;
  }
}

// zipSelected 选中照片打包 ZIP（POST /worklog/zip）。
async function zipSelected() {
  dl.zipping = true;
  dl.error = '';
  try {
    const byId = new Map((dl.list || []).map((p) => [p.id, p]));
    const photos = dl.selected.map((id) => {
      const p = byId.get(id);
      return { url: p ? p.url : '', name: p ? `${p.log_date}-${id}.jpg` : `photo-${id}.jpg` };
    });
    await postZipAndSave(photos, '水印照片.zip');
    MessagePlugin.success(`已打包 ${photos.length} 张照片`);
  } catch (e) {
    dl.error = e.message || '打包失败';
  } finally {
    dl.zipping = false;
  }
}

// savePDF 组合 PDF 下载。
async function savePDF() {
  dl.pdfing = true;
  dl.error = '';
  try {
    await downloadPhotosPDF({ from: dl.from, to: dl.to });
    MessagePlugin.success('组合 PDF 已下载');
  } catch (e) {
    dl.error = e.message || '生成失败';
  } finally {
    dl.pdfing = false;
  }
}

// savePDFNetdisk 组合 PDF 存网盘。
async function savePDFNetdisk() {
  dl.pdfSaving = true;
  dl.error = '';
  try {
    const data = await savePhotosPDFNetdisk({ from: dl.from, to: dl.to });
    MessagePlugin.success(data && data.path ? `已保存到网盘：${data.path}` : '已保存到网盘');
  } catch (e) {
    dl.error = e.message || '保存失败';
  } finally {
    dl.pdfSaving = false;
  }
}

// shiftVerifyMonth 核验月份平移。
function shiftVerifyMonth(delta) {
  const [y, m] = rep.month.split('-').map(Number);
  const d = new Date(y, m - 1 + delta, 1);
  rep.month = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
  loadReport();
}

// sheetKind 当前汇总表类型。
function sheetKind() {
  return tab.value === 'ts' ? 'task' : tab.value === 'fs' ? 'fee' : 'dispatch';
}

// saveSheet 汇总表下载（40902 拦截 → 弹层可 force）。
async function saveSheet(forceFlag) {
  sheet.loading = true;
  sheet.error = '';
  force.open = false;
  force.action = 'download';
  try {
    const args = { from: sheet.from, to: sheet.to, force: forceFlag };
    if (tab.value === 'ts') await downloadTaskSheet(args);
    else if (tab.value === 'fs') await downloadFeeSheet(args);
    else await downloadDispatchSheet({ from: sheet.from, to: sheet.to });
    MessagePlugin.success('已下载');
  } catch (e) {
    if (e.code === 40902 && e.data && e.data.failures) {
      force.failures = e.data.failures;
      force.kind = sheetKind();
      force.open = true;
    } else {
      sheet.error = e.message || '生成失败';
    }
  } finally {
    sheet.loading = false;
  }
}

// saveSheetNetdisk 汇总表存网盘（核验拦截同下载口径）。
async function saveSheetNetdisk(forceFlag) {
  sheet.netdisking = true;
  sheet.error = '';
  force.open = false;
  force.action = 'netdisk';
  try {
    const data = await saveSheetToNetdisk({
      sheet: sheetKind(),
      from: sheet.from,
      to: sheet.to,
      force: forceFlag,
    });
    MessagePlugin.success(data && data.path ? `已保存到网盘：${data.path}` : '已保存到网盘');
  } catch (e) {
    if (e.code === 40902 && e.data && e.data.failures) {
      force.failures = e.data.failures;
      force.kind = sheetKind();
      force.open = true;
    } else {
      sheet.error = e.message || '保存失败';
    }
  } finally {
    sheet.netdisking = false;
  }
}

// forceContinue 核验拦截后「仍要生成」。
function forceContinue() {
  force.open = false;
  if (force.action === 'netdisk') saveSheetNetdisk(true);
  else if (force.action === 'preview') previewSheet(true);
  else saveSheet(true);
}

// previewSheet 汇总表在线预览（OO 承载页 / 微软查看器）。
async function previewSheet(forceFlag) {
  sheet.previewing = true;
  sheet.error = '';
  force.open = false;
  force.action = 'preview';
  try {
    const data = await fetchSheetPreview({
      sheet: sheetKind(),
      from: sheet.from,
      to: sheet.to,
      force: forceFlag,
    });
    if (data && data.url) {
      window.open(data.url, '_blank', 'noopener');
    } else {
      sheet.error = '预览地址获取失败';
    }
  } catch (e) {
    if (e.code === 40902 && e.data && e.data.failures) {
      force.failures = e.data.failures;
      force.kind = sheetKind();
      force.open = true;
    } else {
      sheet.error = e.message || '预览失败';
    }
  } finally {
    sheet.previewing = false;
  }
}

// ---- 派车对齐 ----

function onAlignFiles(ev) {
  align.files = Array.from((ev.target && ev.target.files) || []);
  align.error = '';
  align.result = null;
}

// runAlign 上传并对齐（只读对齐不写库）。
async function runAlign() {
  if (!align.files.length) return;
  align.loading = true;
  align.error = '';
  align.result = null;
  try {
    align.result = await dispatchAlign(align.files);
  } catch (e) {
    align.error = e.message || '导入失败';
  } finally {
    align.loading = false;
  }
}

// ---- 启动 ----

let spectatorTimer = null;

onMounted(async () => {
  if (isCrossAdmin) {
    // 超管：恢复上次班组选择并加载班组列表
    try {
      const saved = Number(localStorage.getItem('worklog_team_id')) || 0;
      if (saved) {
        teamId.value = saved;
        setTeamId(saved);
      }
    } catch (e) {
      // 存储不可用忽略
    }
    loadTeams();
  }
  try {
    meta.value = await fetchMeta();
  } catch (e) {
    MessagePlugin.error(e.message || '数据源加载失败');
  }
  loadMonth();
  loadLogs();
  // 旁观者任务锁：进入页面即探测，之后每 15s 空闲探测 /sync/active
  syncSpectators();
  spectatorTimer = setInterval(syncSpectators, 15000);
});

onBeforeUnmount(() => {
  if (spectatorTimer) clearInterval(spectatorTimer);
});
</script>

<style scoped>
.wl-team-sel {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
  max-width: 160px;
}
.wl-stats {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--tk-line);
  font-size: 13px;
  color: var(--tk-sub);
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.wl-need {
  color: #d25f00;
}
.wl-date-head {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 10px;
  color: var(--tk-ink);
}
.wl-empty {
  text-align: center;
  padding: 32px 0;
}
.wl-range {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-dl-group {
  margin-top: 12px;
}
.wl-dl-month {
  font-size: 13px;
  font-weight: 600;
  color: var(--tk-sub);
  margin-bottom: 8px;
}
.wl-dl-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 10px;
}
.wl-dl-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--tk-sub);
}
.wl-dl-item img {
  width: 100%;
  height: 90px;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
}
.wl-cal-nav {
  width: 28px;
  height: 28px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-sub);
  font-size: 16px;
  cursor: pointer;
}
.wl-rep-month {
  font-size: 14px;
  font-weight: 600;
}
.wl-rep-row {
  display: flex;
  gap: 14px;
  padding: 10px 0;
  border-bottom: 1px solid var(--tk-line);
  flex-wrap: wrap;
}
.wl-rep-main {
  display: flex;
  gap: 10px;
  align-items: baseline;
  min-width: 220px;
}
.wl-rep-date {
  font-size: 13px;
  font-weight: 600;
  color: var(--tk-ink);
}
.wl-rep-detail {
  flex: 1;
  min-width: 0;
}
.wl-rep-reason {
  font-size: 12px;
  color: var(--tk-red);
  line-height: 1.7;
}
.wl-pass-bar {
  margin-top: 12px;
  background: var(--tk-green-soft);
  border-left: 6px solid var(--tk-green);
  padding: 10px 12px;
  font-size: 13px;
  color: #0b8a37;
}
.wl-sheet-desc {
  margin-bottom: 12px;
}
.wl-align-sum {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
  margin: 10px 0;
}
.wl-modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.wl-modal {
  width: min(560px, calc(100vw - 32px));
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
