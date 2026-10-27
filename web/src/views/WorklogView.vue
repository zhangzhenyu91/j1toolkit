<template>
  <div class="wl-page">
    <!-- 页头：kicker + 大标题 + 副标；右侧操作区（班组切换 / 当日统计 / 范围开关 / 各入口） -->
    <div class="wl-head">
      <div class="wl-title">
        <div class="wl-kick">WORKLOG · 班组出工记录</div>
        <h3>出工日志</h3>
        <span class="more">月历色点看验证状态，点击日期查看当日记录</span>
      </div>
      <div class="acts">
        <select
          v-if="isCrossAdmin"
          v-model="teamId"
          class="team-sel num"
          title="仅超级管理员可切换班组；其余角色固定为本班组"
          @change="onTeamChange"
        >
          <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
        </select>
        <span v-if="tab === 'rec'" class="photo-cnt num">{{ photoCntText }}</span>
        <div class="seg">
          <button type="button" :class="{ on: scope === 'all' }" @click="setScope('all')">
            全部
          </button>
          <button type="button" :class="{ on: scope === 'mine' }" @click="setScope('mine')">
            仅看我
          </button>
        </div>
        <button v-if="isSheetAdmin" type="button" class="btn" @click="pullOpen = true">
          从商旅拉取
        </button>
        <button v-if="isCrossAdmin" type="button" class="btn" @click="openCross()">
          新建跨班日志
        </button>
        <button type="button" class="btn btn-primary" @click="openCreate()">新建日志</button>
      </div>
    </div>

    <!-- 页签：下划线式，激活蓝字 + 2px 蓝条 -->
    <div class="wl-tabs">
      <button
        v-for="t in visibleTabs"
        :key="t.key"
        type="button"
        :class="{ on: tab === t.key }"
        @click="switchTab(t.key)"
      >
        {{ t.label }}
      </button>
    </div>

    <!-- 主体两栏：左筛选月历 / 右内容主列（撑满剩余高度，列内容各自内部滚动） -->
    <div class="wl-body" :class="{ 'rep-side': repSideOn }">
      <aside class="card cal">
        <MonthCalendar
          v-model:month="month"
          v-model:selected="date"
          :status-map="dayStatus"
          @update:selected="onSelectDate"
          @update:month="onMonthChange"
        />
        <div class="cal-stats num">
          本月通过 {{ stats.passed }}/{{ stats.total }}
          <span v-if="stats.needPhotos" class="cal-need">我缺照片 {{ stats.needPhotos }}</span>
        </div>
      </aside>

      <section class="wl-list">
        <!-- 记录：当日日志卡片流（日期导航由月历承担） -->
        <div class="pane" :class="{ on: tab === 'rec' }">
          <div class="card-flow" :class="{ switching: loading }">
            <div v-if="loadError && !logs.length" class="empty">
              <WlIcon name="warn" :size="44" />
              <p>{{ loadError }}</p>
              <button type="button" class="btn btn-sm" @click="retryLogs">重试</button>
            </div>
            <div v-else-if="!logs.length" class="empty">
              <WlIcon name="calendar" :size="44" />
              <p>当日暂无出工日志</p>
              <p class="empty-sub">
                {{
                  scope === 'mine' ? '可切换「全部」查看班组当日记录' : '切换月历日期查看历史记录'
                }}
              </p>
            </div>
            <template v-else>
              <LogCard
                v-for="(e, i) in logs"
                :key="e.id"
                :entry="e"
                :idx="i"
                :total="logs.length"
                :eff-team-id="effTeamId"
                :op-lock="opLocks[e.id] || null"
                :can-delete-cross="isCrossAdmin"
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
          </div>
        </div>

        <!-- 批量下载：日期范围 + 按月分组勾选 + 底部操作行 -->
        <div class="pane" :class="{ on: tab === 'dl' }">
          <div class="rep-bar">
            <input v-model="dl.from" type="date" class="f-sel rg-date num" />
            <span class="rep-sep">-</span>
            <input v-model="dl.to" type="date" class="f-sel rg-date num" />
            <button type="button" class="btn btn-sm" @click="loadPhotos">查询</button>
            <span class="dl-meta num">{{ dlMetaText }}</span>
          </div>
          <div class="pane-scroll">
            <div v-if="dl.error" class="wl-error">{{ dl.error }}</div>
            <div v-if="!dlGroups.length" class="empty">
              <p>该范围暂无照片</p>
            </div>
            <div v-for="g in dlGroups" :key="g.month" class="dl-group">
              <div class="dl-month num">{{ g.month }}</div>
              <div class="dl-grid">
                <figure
                  v-for="p in g.items"
                  :key="p.id"
                  class="dl-ph"
                  :class="{ sel: dl.selected.includes(p.id) }"
                  @click="toggleDlSelect(p.id)"
                >
                  <img :src="p.url" loading="lazy" alt="照片" />
                  <span class="dl-tick"><WlIcon name="check" :size="11" /></span>
                  <figcaption class="dl-day num">
                    {{ p.day }}日 · {{ (p.members || []).join('、') }}
                  </figcaption>
                </figure>
              </div>
            </div>
          </div>
          <div class="pane-foot">
            <span class="rep-mine" :class="{ on: dlAllOn }" @click="toggleDlAll">
              <span class="sw"></span>全选
            </span>
            <span class="dl-meta num">已选 {{ dl.selected.length }} 张</span>
            <span class="sp">
              <button type="button" class="btn" :disabled="dl.pdfing" @click="savePDF">
                组合 PDF
              </button>
              <button type="button" class="btn" :disabled="dl.pdfSaving" @click="savePDFNetdisk">
                PDF 存网盘
              </button>
              <button
                type="button"
                class="btn"
                :disabled="!dl.selected.length || dl.zipping"
                @click="zipSelected"
              >
                打包 ZIP
              </button>
              <button
                type="button"
                class="btn btn-primary"
                :disabled="!dl.selected.length || dl.saving"
                @click="saveSelected"
              >
                下载选中
              </button>
            </span>
          </div>
        </div>

        <!-- 汇总前核验：整月切换 + 未通过记录分组列表 + 底部绿色条（≥1600px 记录页签下常驻右栏） -->
        <div class="pane pane-rep" :class="{ on: tab === 'rep' }">
          <div class="rep-bar">
            <div class="cal-nav rep-nav">
              <button type="button" title="上一月" @click="shiftVerifyMonth(-1)">
                <WlIcon name="chevron-left" :size="12" />
              </button>
              <span class="rep-month num">{{ repMonthText }}</span>
              <button type="button" title="下一月" @click="shiftVerifyMonth(1)">
                <WlIcon name="chevron-right" :size="12" />
              </button>
            </div>
            <span class="rep-total num">{{ repTotalText }}</span>
          </div>
          <div class="pane-scroll">
            <div v-if="rep.error" class="wl-error">{{ rep.error }}</div>
            <template v-else-if="rep.list.length">
              <div v-for="g in repGroups" :key="g.date" class="rep-group">
                <div class="rep-date num">
                  {{ fmtDateCN(g.date) }}
                  <span class="cnt">{{ g.items.length }} 条未通过</span>
                </div>
                <div
                  v-for="it in g.items"
                  :key="it.id"
                  class="rep-row"
                  title="点击查看该日记录"
                  @click="gotoDate(it.log_date)"
                >
                  <div class="rep-top">
                    <span class="rep-plate num">{{ it.plate_no || '未出车' }}</span>
                    <span class="rep-members">{{ (it.members || []).join('、') }}</span>
                    <span class="rep-go"><WlIcon name="chevron-right" :size="14" /></span>
                  </div>
                  <div class="rep-chips">
                    <em v-for="(r, i) in it.reasons || []" :key="i" class="reason">{{ r }}</em>
                    <em v-if="!(it.reasons || []).length" class="reason">未通过</em>
                  </div>
                </div>
              </div>
              <div class="rep-okbar">
                <WlIcon name="check" :size="15" />
                <span
                  >其余 {{ repOkN }} 条记录全部通过，未在此列出（规则
                  a~g；未出车免验证；非水印照片不参与）</span
                >
              </div>
            </template>
            <div v-else class="empty">
              <WlIcon name="check" :size="44" />
              <p>当月记录全部通过</p>
              <p class="empty-sub">规则 a~g；未出车免验证；非水印照片不参与</p>
            </div>
          </div>
        </div>

        <!-- 工作任务单 / 费用汇总 / 派车汇总：日期范围 + 字段口径卡 + 底部操作行 -->
        <div class="pane" :class="{ on: tab === 'ts' || tab === 'fs' || tab === 'ds' }">
          <div v-if="tab === 'ds'" class="rep-bar pa-sync">
            <span
              class="rep-mine"
              :class="{ on: paSync }"
              role="switch"
              :aria-checked="paSync"
              tabindex="0"
              @click="togglePaSync"
              @keydown.enter.prevent="togglePaSync"
              @keydown.space.prevent="togglePaSync"
            >
              <span class="sw"></span><span class="tt">每日自动同步派车单</span>
            </span>
            <span class="hint">与「派车对齐」共用同一开关，按当前生效班组保存</span>
          </div>
          <div class="rep-bar">
            <input v-model="sheet.from" type="date" class="f-sel rg-date num" />
            <span class="rep-sep">-</span>
            <input v-model="sheet.to" type="date" class="f-sel rg-date num" />
            <span class="dl-meta">{{ sheetDesc }}</span>
          </div>
          <div class="pane-scroll">
            <div class="card dsec">
              <div class="dsec-h"><b>字段口径</b></div>
              <div class="f-hint dsec-body" v-html="sheetRule"></div>
            </div>
            <div v-if="sheet.error" class="wl-error">{{ sheet.error }}</div>
          </div>
          <div class="pane-foot">
            <span class="sp">
              <button
                type="button"
                class="btn"
                :disabled="sheet.previewing"
                @click="previewSheet(false)"
              >
                预览
              </button>
              <button
                type="button"
                class="btn"
                :disabled="sheet.netdisking"
                @click="saveSheetNetdisk(false)"
              >
                存网盘
              </button>
              <button
                type="button"
                class="btn btn-primary"
                :disabled="sheet.loading"
                @click="saveSheet(false)"
              >
                {{ tab === 'ds' ? '下载 Excel' : '下载 Word' }}
              </button>
            </span>
          </div>
        </div>

        <!-- 派车对齐：每日同步开关 + 导入 + 概览 + 不一致清单 -->
        <div class="pane" :class="{ on: tab === 'pa' }">
          <div class="rep-bar pa-sync">
            <span class="rep-mine" :class="{ on: paSync }" @click="togglePaSync">
              <span class="sw"></span><span class="tt">每日自动同步派车单</span>
            </span>
            <span class="hint"
              >开启后每日 09:20
              自动取回当日派车单，按本班成员匹配建出车卡片；用车人含多个班组人名的记录仅通知不建卡</span
            >
          </div>
          <div class="rep-bar">
            <button type="button" class="btn btn-sm" @click="pickAlignFiles">
              选择派车单文件…
            </button>
            <span class="pa-files">
              <span v-for="(f, i) in align.files" :key="f.name + i" class="pa-fchip">
                {{ f.name }}
                <span class="x" @click="align.files.splice(i, 1)">×</span>
              </span>
            </span>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="!align.files.length || align.loading"
              @click="runAlign"
            >
              开始对齐
            </button>
            <input
              ref="alignInput"
              type="file"
              accept=".xls,.xlsx"
              multiple
              class="wl-hidden"
              @change="onAlignFiles"
            />
          </div>
          <div class="pane-scroll">
            <div v-if="align.error" class="wl-error">{{ align.error }}</div>
            <template v-if="align.result">
              <div class="pa-sums">
                <div class="pa-sum ok">
                  <div class="n num">{{ align.result.summary.consistent }}</div>
                  <div class="t">一致</div>
                </div>
                <div class="pa-sum bad">
                  <div class="n num">{{ align.result.summary.plate }}</div>
                  <div class="t">车牌不一致</div>
                </div>
                <div class="pa-sum bad">
                  <div class="n num">{{ align.result.summary.members }}</div>
                  <div class="t">用车人不一致</div>
                </div>
                <div class="pa-sum bad">
                  <div class="n num">{{ align.result.summary.destination }}</div>
                  <div class="t">目的地不一致</div>
                </div>
                <div class="pa-sum bad">
                  <div class="n num">{{ align.result.summary.orderNo }}</div>
                  <div class="t">派车单号不一致</div>
                </div>
                <div class="pa-sum warn">
                  <div class="n num">{{ align.result.summary.sheetOnly }}</div>
                  <div class="t">表有卡无</div>
                </div>
                <div class="pa-sum warn">
                  <div class="n num">{{ align.result.summary.entryOnly }}</div>
                  <div class="t">卡有表无</div>
                </div>
                <div class="pa-sum mut">
                  <div class="n num">{{ align.result.summary.imported }}</div>
                  <div class="t">
                    导入行 / 跳过 {{ align.result.summary.skipped }} / 他班剔除
                    {{ align.result.summary.outside }}
                  </div>
                </div>
              </div>
              <div class="pa-list">
                <div v-for="(it, i) in align.result.items" :key="i" class="pa-row">
                  <span class="pa-date num">{{ it.date }}</span>
                  <span
                    class="pa-badge"
                    :class="
                      it.kind === 'matched'
                        ? 'mem'
                        : it.kind === 'sheetOnly'
                          ? 'none-sheet'
                          : 'none-sys'
                    "
                  >
                    {{
                      it.kind === 'matched'
                        ? '有差异'
                        : it.kind === 'sheetOnly'
                          ? '表有卡无'
                          : '卡有表无'
                    }}
                  </span>
                  <span class="pa-sumline">
                    <template v-if="it.sheet">
                      表格：<b>{{ it.sheet.plate || '—' }}</b>
                      {{ (it.sheet.members || []).join('、') }}
                      <template v-if="it.sheet.to"> → {{ it.sheet.to }}</template>
                      <template v-if="it.sheet.orderNo"> · {{ it.sheet.orderNo }}</template>
                    </template>
                    <span class="vs">/</span>
                    <template v-if="it.entry">
                      卡片：<b>{{ it.entry.plate || '未出车' }}</b>
                      {{ (it.entry.members || []).map((m) => m.name).join('、') }}
                      <template v-if="it.entry.destination"> → {{ it.entry.destination }}</template>
                      <template v-if="it.entry.order_no"> · {{ it.entry.order_no }}</template>
                    </template>
                  </span>
                  <span v-if="it.diffs" class="pa-diffs">
                    <span v-if="it.diffs.plate" class="hl-r">车牌不一致</span>
                    <span v-if="it.diffs.missing && it.diffs.missing.length" class="hl-r"
                      >表格多出：{{ it.diffs.missing.join('、') }}</span
                    >
                    <span v-if="it.diffs.extra && it.diffs.extra.length" class="hl-r"
                      >卡片多出：{{ it.diffs.extra.join('、') }}</span
                    >
                    <span v-if="it.diffs.dest" class="hl-r">目的地不一致</span>
                    <span v-if="it.diffs.orderNo" class="hl-r">派车单号不一致</span>
                  </span>
                </div>
              </div>
            </template>
          </div>
        </div>
      </section>
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
      :member-names="lightboxMembers"
      @op-started="onOpStarted"
      @changed="onSaved"
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
    <div v-if="force.open" class="mask open" @click.self="force.open = false">
      <div class="modal">
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">存在未通过项</span>
            <button type="button" class="m-x" @click="force.open = false">
              <WlIcon name="close" :size="14" />
            </button>
          </div>
          <div class="f-hint">
            所选范围内有 {{ force.failures.length }} 条记录存在未通过项，请处理后再操作。
          </div>
          <div v-for="(f, i) in force.failures" :key="i" class="rep-row">
            <div class="rep-top">
              <span class="rep-plate num">{{ f.log_date }}</span>
              <span class="rep-plate">{{ f.plate_no }}</span>
              <span class="rep-members">{{ (f.members || []).join('、') }}</span>
            </div>
            <div class="rep-chips">
              <em v-for="(r, j) in f.reasons || []" :key="j" class="reason">{{ r }}</em>
            </div>
          </div>
          <div class="m-foot">
            <button type="button" class="btn" @click="force.open = false">取消</button>
            <button type="button" class="btn btn-primary" @click="forceContinue">仍要生成</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import WlIcon from '../features/worklog/components/WlIcon.vue';
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
  fetchSyncSwitch,
  postZipAndSave,
  savePhotosPDFNetdisk,
  saveSheetNetdisk as saveSheetToNetdisk,
  setTeamId,
  toggleMemberCheck,
  updateSyncSwitch,
} from '../features/worklog/api.js';
import {
  fetchOpStatus,
  fetchSyncActive,
  fetchSyncProgress,
  syncPull,
} from '../features/sgccclockin/api.js';

// 出工日志视图（旧版 worklog.html 结构还原）：页头 kicker/标题/操作区 + 下划线页签 +
// 左筛选月历 320px / 右记录主列（≥1600px 记录页签下核验常驻右栏），整页不滚动、面板内部滚动。
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
const loadError = ref('');
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

// repSideOn 记录页签下 ≥1600px 核验常驻右栏（由 media query 实现网格布局）。
const repSideOn = computed(() => tab.value === 'rec');

// photoCntText 页头当日统计小字。
const photoCntText = computed(() => {
  if (!logs.value.length) return '';
  const photoN = logs.value.reduce((n, e) => n + (e.photos || []).length, 0);
  return `共 ${logs.value.length} 条日志 · ${photoN} 张照片`;
});

// ---- 批量下载状态 ----
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

// dlGroups 按月分组。
const dlGroups = computed(() => {
  const byMonth = new Map();
  (dl.list || []).forEach((p) => {
    const m = p.month || String(p.log_date || '').slice(0, 7);
    if (!byMonth.has(m)) byMonth.set(m, []);
    byMonth.get(m).push(p);
  });
  return [...byMonth.entries()].map(([m, items]) => ({ month: m, items }));
});

// dlMetaText 范围内照片计数。
const dlMetaText = computed(() => `共 ${(dl.list || []).length} 张照片`);

// dlAllOn 全选开关态。
const dlAllOn = computed(() => dl.list.length > 0 && dl.selected.length === dl.list.length);

// ---- 汇总前核验状态 ----
const rep = reactive({
  month: new Date().toISOString().slice(0, 7),
  list: [],
  total: 0,
  loaded: false,
  error: '',
});

// repList 仅未通过记录（旧版口径：有备注的不在此列出）。
const repList = computed(() => (rep.list || []).filter((x) => x.verify === 'failed'));

// repGroups 按 log_date 分组（保持接口顺序）。
const repGroups = computed(() => {
  const groups = [];
  let cur = null;
  repList.value.forEach((it) => {
    if (!cur || cur.date !== it.log_date) {
      cur = { date: it.log_date, items: [] };
      groups.push(cur);
    }
    cur.items.push(it);
  });
  return groups;
});

// repOkN 范围内其余全部通过的条数。
const repOkN = computed(() => Math.max(rep.total - repList.value.length, 0));

// repMonthText 核验月份标题。
const repMonthText = computed(() => {
  const [y, m] = rep.month.split('-').map(Number);
  return `${y} 年 ${m} 月`;
});

// repTotalText 核验合计。
const repTotalText = computed(() => (rep.loaded ? `合计 ${repList.value.length} 条未通过` : ''));

// ---- 汇总表状态 ----
const sheet = reactive({
  from: monthStart,
  to: monthEnd,
  loading: false,
  previewing: false,
  netdisking: false,
  error: '',
});
const force = reactive({ open: false, failures: [], kind: 'ts', action: 'download' });

// sheetDesc / sheetRule 汇总表口径文案（字段口径卡）。
const sheetDesc = computed(() =>
  tab.value === 'ts'
    ? '范围内全部出车卡片，一卡一页合并为一个 Word（单次最多 31 天）'
    : tab.value === 'fs'
      ? '范围内出车卡片一卡一行、用车人一人一列，末尾合计（单次最多 31 天）'
      : '范围内已填派车单号的出车卡片一单一行：派车单号 + 用车日期（单次最多 31 天）'
);
const sheetRule = computed(() => {
  if (tab.value === 'ts') {
    return '线路杆塔号＝本卡水印照片的施工内容；工作负责人＝排序最前的用车人（排序在「班组数据 → 人员」维护）；<br />工作地点＝派车目的地；工作班成员＝用车人（空格间隔）；派车情况＝车牌号；未出车卡片不生成。';
  }
  if (tab.value === 'fs') {
    return '列＝范围内当过用车人的成员（按「班组数据 → 人员」点亮顺序排列，未出现者不列）；行＝每张出车卡片一行（日期升序）；<br />单元格＝（当日伙食补助＋交通费）×1＝计算值，非用车人列留空；费用数据来自商旅同步，有未通过核验的记录需先在「汇总前核验」处理后再生成。';
  }
  return '列＝派车单号、用车日期（＝卡片日志日期）；数据来自卡片「派车单号」字段——每日同步自动带入，<br />也可点卡片车牌「修改派车」手工补填；仅已填单号的出车卡片出数（同步前日期的历史卡片无此字段）。';
});

// ---- 派车对齐状态 ----
const align = reactive({ files: [], loading: false, error: '', result: null });
const alignInput = ref(null);
const paSync = ref(false);

// ---- 弹窗状态 ----
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
const lightboxMembers = ref([]);
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

// fmtDateCN 日期中文（X 月 X 日）。
function fmtDateCN(d) {
  const [y, m, dd] = String(d || '').split('-');
  return `${y} 年 ${Number(m)} 月 ${Number(dd)} 日`;
}

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
  loadError.value = '';
  try {
    const data = await fetchLogs({ date: date.value, scope: scope.value });
    logs.value = data.list || [];
  } catch (e) {
    loadError.value = e.message || '日志加载失败';
    logs.value = [];
  } finally {
    loading.value = false;
  }
}

// retryLogs 错误态重试。
function retryLogs() {
  loadLogs();
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
    rep.loaded = true;
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

// loadPaSync 派车单每日同步开关（派车对齐页签）。
async function loadPaSync() {
  if (!isSheetAdmin) return;
  try {
    const data = await fetchSyncSwitch();
    paSync.value = !!(data && data.enabled);
  } catch (e) {
    // 开关读取失败忽略（对齐主功能不受影响）
  }
}

// ---- 交互 ----

function setScope(v) {
  if (scope.value === v) return;
  scope.value = v;
  onScopeChange();
}

function onScopeChange() {
  loadMonth();
  loadLogs();
  if (tab.value === 'rep' || repSideOn.value) loadReport();
}

function onMonthChange() {
  loadMonth();
}

// onSelectDate 月历选日：重拉当日卡片。
function onSelectDate() {
  loadLogs();
}

// gotoDate 跳到指定日期（核验行点击）：切记录页签并选中该日。
function gotoDate(d) {
  date.value = d;
  month.value = d.slice(0, 7);
  tab.value = 'rec';
  loadLogs();
  loadMonth();
}

function switchTab(key) {
  tab.value = key;
  if (key === 'dl' && !dl.list.length) loadPhotos();
  if (key === 'rep') loadReport();
  if (key === 'pa' || key === 'ds') loadPaSync();
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
  loadPaSync();
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

// openEdit 编辑卡片：跨班卡走跨班表单（仅超管），本班卡走普通编辑。
async function openEdit(entry) {
  if (entry.cross_team === 1 || entry.cross_team === true) {
    if (!isCrossAdmin) {
      MessagePlugin.warning('跨班日志仅超级管理员可修改派车');
      return;
    }
    crossEditing.value = entry;
    crossOpen.value = true;
    try {
      crossMeta.value = await fetchCrossMeta();
    } catch (e) {
      MessagePlugin.error(e.message || '跨班数据加载失败');
    }
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
  lightboxMembers.value = (entry.members || []).map((m) => m.name);
  lightboxOpen.value = true;
}

// openRemark 备注附件：Office 文档在线预览、图片/视频直接打开、其余走下载代理。
async function openRemark({ entry, file, download: wantDl }) {
  try {
    if (wantDl) {
      await downloadRemarkFile(entry.id, file.cos_key, file.name);
      return;
    }
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
  try {
    for (const p of pairs) {
      try {
        const d = await syncPull({ member_id: p.memberId, date: entry.log_date });
        if (d && d.jobId) jobIds.push(d.jobId);
      } catch (e) {
        if (e.code === 40020 || e.code === 40021) continue;
        failed += 1;
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

// toggleDlSelect 勾选切换。
function toggleDlSelect(id) {
  const i = dl.selected.indexOf(id);
  if (i >= 0) dl.selected.splice(i, 1);
  else dl.selected.push(id);
}

// toggleDlAll 全选 / 全不选。
function toggleDlAll() {
  dl.selected = dlAllOn.value ? [] : (dl.list || []).map((p) => p.id);
}

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

// zipSelected 选中照片打包 ZIP（POST /worklog/zip，按日期-照片id 排序命名）。
async function zipSelected() {
  dl.zipping = true;
  dl.error = '';
  try {
    const byId = new Map((dl.list || []).map((p) => [p.id, p]));
    const picked = dl.selected
      .map((id) => byId.get(id))
      .filter(Boolean)
      .sort((a, b) => (a.log_date < b.log_date ? -1 : a.log_date > b.log_date ? 1 : a.id - b.id));
    const photos = picked.map((p) => ({ url: p.url, name: `${p.log_date}-${p.id}.jpg` }));
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

// shiftVerifyMonth 核验月份平移（不看未来月）。
function shiftVerifyMonth(delta) {
  const [y, m] = rep.month.split('-').map(Number);
  const d = new Date(y, m - 1 + delta, 1);
  const nm = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
  if (nm > new Date().toISOString().slice(0, 7)) return;
  rep.month = nm;
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

function pickAlignFiles() {
  alignInput.value && alignInput.value.click();
}

function onAlignFiles(ev) {
  const picked = Array.from((ev.target && ev.target.files) || []);
  ev.target.value = '';
  picked.forEach((f) => {
    if (!align.files.some((x) => x.name === f.name && x.size === f.size)) align.files.push(f);
  });
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

// togglePaSync 每日自动同步派车单开关（按生效班组）。
async function togglePaSync() {
  const next = !paSync.value;
  paSync.value = next;
  try {
    await updateSyncSwitch({ enabled: next });
    MessagePlugin.success(next ? '已开启每日自动同步派车单' : '已关闭每日自动同步派车单');
  } catch (e) {
    paSync.value = !next;
    MessagePlugin.error(e.message || '开关保存失败');
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
  loadReport(); // ≥1600px 核验右栏常驻，进页即载
  // 旁观者任务锁：进入页面即探测，之后每 15s 空闲探测 /sync/active
  syncSpectators();
  spectatorTimer = setInterval(syncSpectators, 15000);
});

onBeforeUnmount(() => {
  if (spectatorTimer) clearInterval(spectatorTimer);
});
</script>

<style scoped>
/* ===== 整页填充：页头/页签固定，主体两栏内部滚动（旧版整页不滚动口径） ===== */
.wl-page {
  margin: -20px -24px;
  height: calc(100% + 40px);
  padding: 16px 24px 14px;
  display: flex;
  flex-direction: column;
  min-height: 0;
  box-sizing: border-box;
}

/* ===== 页头：kicker + 大标题 + 副标；右侧操作区 ===== */
.wl-head {
  flex: none;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px 24px;
  flex-wrap: wrap;
}
.wl-title {
  display: block;
}
.wl-kick {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--tk-brand);
}
.wl-title h3 {
  margin: 4px 0 0;
  font-size: 22px;
  line-height: 1.2;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.wl-title .more {
  display: block;
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
.acts {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
/* 班组切换下拉 */
.team-sel {
  height: 34px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 10px;
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.5px;
  color: var(--tk-ink);
  background: #fff;
  max-width: 160px;
}
/* 当日统计小字（「共 N 条日志 · M 张照片」） */
.photo-cnt {
  font-size: 13px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
/* 全部 / 仅看我 分段开关：灰槽分段控件，激活白底蓝字带轻影 */
.seg {
  display: inline-flex;
  background: var(--tk-line);
  border-radius: 8px;
  padding: 2px;
}
.seg button {
  border: none;
  background: transparent;
  height: 30px;
  padding: 0 16px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 1px;
  cursor: pointer;
  transition:
    background 0.15s,
    color 0.15s;
}
.seg button.on {
  background: #fff;
  color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
/* 按钮（旧版 .btn / .btn-primary） */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 36px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-ink);
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover:not([disabled]) {
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
.btn-primary:hover:not([disabled]) {
  color: #fff;
  filter: brightness(0.92);
}
.btn-sm {
  height: 30px;
  padding: 0 12px;
  font-size: 12px;
}

/* ===== 页签：下划线式，激活蓝字 + 2px 蓝条 ===== */
.wl-tabs {
  position: relative;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 16px;
  border-bottom: 1px solid var(--tk-line);
  flex: none;
}
.wl-tabs button {
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
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  font-family: inherit;
  transition: color 0.15s;
}
.wl-tabs button:hover {
  color: var(--tk-ink);
}
.wl-tabs button.on {
  color: var(--tk-brand);
}
.wl-tabs button::after {
  content: '';
  position: absolute;
  left: 14px;
  right: 14px;
  bottom: -1px;
  height: 2px;
  background: var(--tk-brand);
  border-radius: 2px;
  opacity: 0;
  transition: opacity 0.18s var(--tk-ease);
}
.wl-tabs button.on::after {
  opacity: 1;
}

/* ===== 主体两栏：左筛选月历 320px / 右内容主列 ===== */
.wl-body {
  display: flex;
  flex-direction: row;
  gap: 18px;
  margin-top: 16px;
  flex: 1;
  min-height: 0;
}
.cal {
  width: 320px;
  flex: none;
  padding: 14px 16px 12px;
  align-self: flex-start;
  max-height: 100%;
  overflow-y: auto;
}
/* 本月统计小字（月历图例下方） */
.cal-stats {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid rgba(29, 33, 41, 0.06);
  font-size: 11.5px;
  color: var(--tk-sub);
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  letter-spacing: 0.5px;
}
.cal-need {
  color: #d25f00;
}

/* ===== 右侧列表位：各页签共用，切换时内容各自内部滚动 ===== */
.wl-list {
  flex: 1;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.wl-list .pane {
  display: none;
  flex: 1;
  min-height: 0;
}
.wl-list .pane.on {
  display: flex;
  flex-direction: column;
  animation: paneIn 0.34s var(--tk-ease);
}
.pane > .rep-bar {
  flex: none;
  margin-bottom: 12px;
}
.pane-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-right: 10px; /* 给行卡右偏影让位 */
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.pane-scroll::-webkit-scrollbar {
  width: 5px;
}
.pane-scroll::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.pane-scroll::-webkit-scrollbar-thumb:hover {
  background: var(--tk-ph);
}
.pane-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.pane-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding-top: 14px;
}
.pane-foot .sp {
  margin-left: auto;
  display: flex;
  gap: 10px;
}

/* 卡片流：页面唯一滚动区（切日加载时旧内容微透明，期间禁点） */
.card-flow {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  scrollbar-gutter: stable;
  transition: opacity 0.18s ease;
  padding-right: 10px;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.card-flow.switching {
  opacity: 0.45;
  pointer-events: none;
}
.card-flow::-webkit-scrollbar {
  width: 5px;
}
.card-flow::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.card-flow::-webkit-scrollbar-track {
  background: transparent;
}

/* 空态 / 错误态 */
.empty {
  padding: 48px 0;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
  letter-spacing: 1px;
}
.empty svg {
  color: var(--tk-dash);
}
.empty p {
  margin: 10px 0 0;
}
.empty-sub {
  font-size: 11.5px !important;
}
.empty .btn {
  margin-top: 14px;
}

/* ===== 宽屏（≥1600px）：记录页签下核验常驻右栏（上月历 / 下核验列表） ===== */
@media (min-width: 1600px) {
  .wl-body.rep-side {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 400px;
    grid-template-rows: auto minmax(0, 1fr);
  }
  .wl-body.rep-side > .cal {
    grid-column: 2;
    grid-row: 1;
    width: 100%;
    max-height: none;
  }
  .wl-body.rep-side .wl-list {
    display: contents;
  }
  .wl-body.rep-side .pane:first-child {
    grid-column: 1;
    grid-row: 1 / 3;
    min-height: 0;
    display: flex;
    flex-direction: column;
    animation: none;
  }
  .wl-body.rep-side .pane-rep {
    grid-column: 2;
    grid-row: 2;
    display: flex;
    flex-direction: column;
    min-height: 0;
    animation: none;
    background: #fff;
    border-radius: 12px;
    box-shadow: var(--tk-shadow-card);
    padding: 14px 16px;
    margin-top: 16px;
  }
}

/* ===== 批量下载页签 ===== */
.rg-date {
  width: 148px;
  height: 36px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  padding: 0 10px;
  font-size: 13px;
  color: var(--tk-ink);
  cursor: pointer;
  font-family: inherit;
}
.rep-sep {
  color: var(--tk-ph);
  font-size: 12px;
}
.dl-meta {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.pane-foot .dl-meta {
  margin-left: 0;
}
.dl-group {
  margin-bottom: 6px;
}
.dl-month {
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 1px;
  margin: 14px 0 8px;
}
.dl-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(168px, 1fr));
  gap: 10px;
}
.dl-ph {
  position: relative;
  aspect-ratio: 4 / 3;
  margin: 0;
  border-radius: 8px;
  overflow: hidden;
  cursor: pointer;
  border: 1px solid var(--tk-line);
  background: var(--tk-bg);
  transition:
    border-color 0.2s,
    box-shadow 0.2s,
    opacity 0.2s;
}
.dl-ph img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
  transition: opacity 0.2s;
}
.dl-ph.sel {
  border-color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.dl-ph:not(.sel) img {
  opacity: 0.45;
}
.dl-tick {
  position: absolute;
  top: 5px;
  right: 5px;
  width: 18px;
  height: 18px;
  border-radius: 8px;
  background: #fff;
  border: 1px solid var(--tk-line);
  color: transparent;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    background 0.2s,
    border-color 0.2s,
    color 0.2s;
}
.dl-ph.sel .dl-tick {
  background: var(--tk-brand);
  border-color: var(--tk-brand);
  color: #fff;
}
.dl-day {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  padding: 3px 6px 4px;
  text-align: center;
  font-size: 9.5px;
  font-weight: 600;
  color: #fff;
  letter-spacing: 0.8px;
  background: rgba(29, 33, 41, 0.74);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

/* ===== 汇总前核验 ===== */
.rep-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.rep-nav {
  margin-left: 0;
}
.rep-month {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-ink);
  min-width: 84px;
  text-align: center;
}
.rep-total {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
/* 自绘开关（白底墨边槽 + 墨方块滑钮，开态蓝槽白钮） */
.rep-mine {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
  user-select: none;
  letter-spacing: 1px;
}
.rep-mine .sw {
  width: 32px;
  height: 18px;
  border-radius: 8px;
  background: #fff;
  border: 1px solid var(--tk-line);
  position: relative;
  transition: background 0.2s;
  flex: none;
}
.rep-mine .sw::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 10px;
  height: 10px;
  border-radius: 8px;
  background: var(--tk-ink);
  transition: left 0.2s var(--tk-ease);
}
.rep-mine.on {
  color: var(--tk-brand);
}
.rep-mine.on .sw {
  background: var(--tk-brand);
}
.rep-mine.on .sw::after {
  left: 16px;
  background: #fff;
}
.rep-date {
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 1px;
  margin: 14px 0 8px;
}
.rep-date .cnt {
  margin-left: 8px;
  font-size: 11px;
  font-weight: 400;
  color: var(--tk-red);
}
/* 核验行：行卡（上行 车牌 + 用车人 + 跳转箭头，下行不通过 chips） */
.rep-row {
  display: block;
  padding: 12px 16px;
  border: 1px solid var(--tk-line);
  background: #fff;
  border-radius: 8px;
  margin-bottom: 10px;
  cursor: pointer;
  transition: box-shadow 0.15s var(--tk-ease);
}
.rep-row:hover {
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.rep-top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.rep-plate {
  flex: none;
  font-size: 13.5px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.rep-members {
  font-size: 12px;
  color: var(--tk-sub);
  line-height: 1.6;
}
.rep-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 9px;
}
.reason {
  font-style: normal;
  font-size: 11px;
  font-weight: 700;
  color: var(--tk-red);
  background: var(--tk-red-soft);
  border-radius: 8px;
  padding: 3px 9px;
  letter-spacing: 0.5px;
  line-height: 1.55;
}
.rep-go {
  margin-left: auto;
  flex: none;
  color: var(--tk-ph);
  display: flex;
}
.rep-row:hover .rep-go {
  color: var(--tk-brand);
}
/* 底部绿色通过条 */
.rep-okbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  padding: 12px 14px;
  border-radius: 8px;
  background: var(--tk-green-soft);
  border: 1.5px solid var(--tk-green);
  border-left: 6px solid var(--tk-green);
  color: var(--tk-green);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.rep-okbar svg {
  flex: none;
}

/* ===== 说明卡小节（各 sheet 页签「字段口径」） ===== */
.dsec {
  margin-bottom: 14px;
  overflow: hidden;
}
.dsec-h {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 0 10px;
}
.dsec-h::before {
  content: '';
  width: 8px;
  height: 8px;
  flex: none;
  background: var(--tk-red);
}
.dsec-h b {
  font-size: 13.5px;
  letter-spacing: 1px;
}
.dsec-body {
  margin: 0;
  padding: 0;
  line-height: 2;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}

/* ===== 派车对齐页签 ===== */
.pa-sync {
  padding: 8px 12px;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
}
.pa-sync .tt {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 1px;
}
.pa-sync .hint {
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.pa-files {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.pa-fchip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 9px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 11.5px;
  font-weight: 700;
}
.pa-fchip .x {
  cursor: pointer;
  opacity: 0.65;
}
.pa-fchip .x:hover {
  opacity: 1;
}
.pa-sums {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.pa-sum {
  flex: 1;
  min-width: 110px;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 10px 14px;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.pa-sum .n {
  font-size: 20px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.pa-sum .t {
  font-size: 11px;
  color: var(--tk-sub);
  margin-top: 2px;
  letter-spacing: 1px;
}
.pa-sum.ok .n {
  color: var(--tk-green);
}
.pa-sum.warn .n {
  color: var(--tk-brand);
}
.pa-sum.bad .n {
  color: var(--tk-red);
}
.pa-sum.mut .n {
  color: var(--tk-ph);
}
.pa-list {
  margin-top: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.pa-row {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 12px 16px;
  flex-wrap: wrap;
  transition: box-shadow 0.15s var(--tk-ease);
}
.pa-row:hover {
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.pa-date {
  flex: none;
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
.pa-badge {
  flex: none;
  font-size: 11px;
  font-weight: 700;
  padding: 2px 9px;
  border-radius: 8px;
  border: 1.5px solid var(--tk-line);
  letter-spacing: 0.5px;
}
.pa-badge.mem {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.pa-badge.none-sys {
  background: var(--tk-bg);
  color: var(--tk-ph);
}
.pa-badge.none-sheet {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.pa-sumline {
  flex: 1;
  min-width: 200px;
  font-size: 12.5px;
  color: var(--tk-sub);
  line-height: 1.7;
}
.pa-sumline b {
  color: var(--tk-ink);
  font-weight: 700;
}
.pa-sumline .vs {
  color: var(--tk-ph);
  margin: 0 6px;
}
.pa-diffs {
  flex-basis: 100%;
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 11.5px;
}
.pa-diffs .hl-r {
  color: var(--tk-red);
  font-weight: 700;
}

/* ===== 通用弹层（核验拦截；其余弹层在各自组件内） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: rgba(29, 33, 41, 0.45);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.m-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.m-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
}
.m-x:hover {
  background: var(--tk-ink);
  color: #fff;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
.f-hint {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}

/* 错误提示（浅红底 + 左色条，直角） */
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-hidden {
  display: none;
}

/* 入场 / 切换动效 */
@keyframes paneIn {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* ===== 响应式：窄屏上下堆叠（月历摘要展开由 MonthCalendar 自理） ===== */
@media (max-width: 1024px) {
  .wl-body {
    flex-direction: column;
    overflow-y: auto;
  }
  .cal {
    width: 100%;
    max-height: none;
    overflow-y: visible;
  }
  .wl-title h3 {
    font-size: 30px;
  }
  .wl-list {
    min-height: 420px;
  }
}
@media (max-width: 640px) {
  .wl-title h3 {
    font-size: 24px;
    letter-spacing: 0.5px;
  }
  .wl-title .more {
    display: none;
  }
}

@media (prefers-reduced-motion: reduce) {
  .wl-list .pane.on,
  .wl-tabs button::after {
    animation: none;
    transition: none;
  }
}
</style>
