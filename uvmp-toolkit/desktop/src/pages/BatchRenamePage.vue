<template>
  <div class="rename-page" :class="{ 'is-preview': view === 'preview' }">
    <!-- ===================== 预览态 ===================== -->
    <template v-if="view === 'preview'">
      <div class="rename-source">
        <span class="src-icon"><ShadeIcon name="folder" :size="14" /></span>
        <span class="src-main">
          <span class="src-label">来源</span>
          <span class="src-path" :title="sourceText">{{ sourceText }}</span>
        </span>
        <span v-if="directory" class="src-flag">仅第一层</span>
        <span class="src-meta">{{ sourceMeta }}</span>
        <span class="src-acts">
          <t-button size="small" variant="outline" :disabled="running" @click="addFiles">
            添加文件
          </t-button>
          <t-button size="small" variant="outline" :disabled="running" @click="addFolder">
            选择文件夹
          </t-button>
          <button
            type="button"
            class="link-danger"
            :disabled="!hasSource || running"
            @click="clearList"
          >
            清空列表
          </button>
        </span>
      </div>

      <div v-if="restoreNotice" class="rename-restore">
        <ShadeIcon name="info-circle" :size="12" />
        <span>{{ restoreNotice }}</span>
        <button type="button" class="r-add" @click="restoreNotice = ''">知道了</button>
      </div>

      <div class="rename-work">
        <section class="rename-card rename-list">
          <div class="card-head">
            <b>文件与预览</b>
            <span class="card-sub">旧名 → 新名实时计算</span>
            <span class="right">{{ rulesSummary(rules) }}</span>
          </div>
          <div class="pv-head">
            <span>状态</span>
            <span>原名称</span>
            <span />
            <span>新名称</span>
            <span />
          </div>
          <div class="pv-body">
            <div v-if="!displayRows.length" class="empty-block">
              <span class="empty-icon"><ShadeIcon name="rename" :size="23" /></span>
              <b>还没有待重命名的文件</b>
              <p>
                添加任意类型文件，或选择一个文件夹（默认只读取第一层）。<br />
                列表与预览会即时计算。
              </p>
              <div class="empty-acts">
                <t-button size="small" variant="outline" @click="addFiles">添加文件</t-button>
                <t-button size="small" variant="outline" @click="addFolder">选择文件夹</t-button>
              </div>
            </div>
            <div
              v-for="(row, index) in displayRows"
              :key="row.key"
              class="pv-row"
              :class="{ 'is-conflict': row.conflict }"
            >
              <span>
                <span class="tag" :class="row.tag.theme"> <i />{{ row.tag.text }} </span>
              </span>
              <span class="pv-old">
                <span class="f-ico" :class="'kind-' + kindOf(row.name)">
                  <ShadeIcon :name="kindIcon(row.name)" :size="15" />
                </span>
                <span class="nm" :title="row.name">
                  <span class="nm-main">{{ splitName(row.name).main }}</span>
                  <span class="nm-ext">{{ splitName(row.name).ext }}</span>
                  <span class="nm-meta">{{ typeText(row.name) }}</span>
                </span>
              </span>
              <span class="pv-arrow"><ShadeIcon name="arrow-right" :size="13" /></span>
              <span class="pv-new">
                <span v-if="row.new_name" class="new-name" :class="row.tone" :title="row.new_name">
                  {{ splitName(row.new_name).main
                  }}<span class="nm-ext">{{ splitName(row.new_name).ext }}</span>
                </span>
                <span v-else class="new-name same">等待预检</span>
                <span v-if="row.reason" class="reason" :title="row.reason">{{ row.reason }}</span>
              </span>
              <button
                v-if="!directory"
                type="button"
                class="row-x"
                :aria-label="'从列表移除 ' + row.name"
                title="从列表移除"
                :disabled="running"
                @click="removeRow(row)"
              >
                <ShadeIcon name="close" :size="13" />
              </button>
            </div>
          </div>
          <div class="list-foot">
            <span
              >共 <b>{{ displayRows.length }}</b> 项</span
            >
            <span v-if="previewLoading" class="muted">正在预检…</span>
            <span v-else-if="previewError" class="reason bad">{{ previewError }}</span>
            <span class="right">只改名称，不移动位置</span>
          </div>
        </section>

        <aside class="rename-card rename-rules">
          <div class="card-head">
            <b>重命名规则</b>
            <span class="card-sub">只作用于主文件名</span>
          </div>
          <div class="rules-scroll">
            <section class="rsec">
              <div class="rsec-head">
                <span class="no">1</span>
                <b>查找替换</b>
                <button type="button" class="r-add" @click="addPair">+ 添加一组</button>
              </div>
              <div class="rsec-body">
                <div v-for="(pair, index) in rules.replace" :key="'pair-' + index" class="pair-row">
                  <t-input v-model="pair.find" size="small" placeholder="查找内容" />
                  <span class="to"><ShadeIcon name="arrow-right" :size="12" /></span>
                  <t-input v-model="pair.replace" size="small" placeholder="替换为（可留空）" />
                  <button
                    type="button"
                    class="icon-btn"
                    aria-label="删除这组替换"
                    title="删除这组"
                    @click="removePair(index)"
                  >
                    <ShadeIcon name="close" :size="12" />
                  </button>
                  <label class="pair-case">
                    <t-checkbox v-model="pair.case_sensitive" size="small" />
                    <span>区分大小写（组 {{ index + 1 }}）</span>
                  </label>
                </div>
                <p class="rsec-note">按组从上到下依次替换；查找内容为空的组不生效。</p>
              </div>
            </section>

            <section class="rsec">
              <div class="rsec-head">
                <span class="no">2</span>
                <b>前后缀</b>
                <span class="head-switch">
                  <span>保留扩展名</span>
                  <t-switch v-model="rules.keep_extension" size="small" />
                </span>
              </div>
              <div class="rsec-body two-col">
                <span>
                  <span class="fld-label">前缀</span>
                  <t-input v-model="rules.prefix" size="small" placeholder="例如 2026秋检_" />
                </span>
                <span>
                  <span class="fld-label">后缀</span>
                  <t-input v-model="rules.suffix" size="small" placeholder="例如 _终稿" />
                </span>
              </div>
              <div v-if="!rules.keep_extension" class="alert">
                <ShadeIcon name="warn" :size="12" />
                <span>关闭后新名不含扩展名，文件可能无法按类型打开；建议始终保留。</span>
              </div>
            </section>

            <section class="rsec">
              <div class="rsec-head">
                <span class="no">3</span>
                <b>统一主名 + 序号</b>
                <span class="head-switch">
                  <span>{{ rules.sequence.enabled ? '启用' : '关闭' }}</span>
                  <t-switch v-model="rules.sequence.enabled" size="small" />
                </span>
              </div>
              <div class="rsec-body">
                <span class="fld-label">统一主名</span>
                <t-input v-model="rules.sequence.base" size="small" placeholder="例如 现场巡检" />
                <div class="three-col seq-extra">
                  <span>
                    <span class="fld-label">起始</span>
                    <t-input-number
                      v-model="rules.sequence.start"
                      size="small"
                      :min="0"
                      :step="1"
                    />
                  </span>
                  <span>
                    <span class="fld-label">步长</span>
                    <t-input-number v-model="rules.sequence.step" size="small" :min="1" :step="1" />
                  </span>
                  <span>
                    <span class="fld-label">位数</span>
                    <t-input-number
                      v-model="rules.sequence.digits"
                      size="small"
                      :min="1"
                      :max="9"
                    />
                  </span>
                </div>
                <div class="seq-preview">
                  <span>示例</span>
                  <b>{{ exampleName }}</b>
                </div>
                <p class="rsec-note" :class="{ warn: sequenceWarn }">
                  {{ sequenceNote }}
                </p>
              </div>
            </section>

            <section class="rsec">
              <div class="rsec-head">
                <span class="no">4</span>
                <b>冲突处理</b>
                <span class="head-switch">默认不覆盖</span>
              </div>
              <div class="rsec-body">
                <div class="seg">
                  <button
                    type="button"
                    :class="{ on: rules.conflict === 'skip' }"
                    :aria-pressed="rules.conflict === 'skip'"
                    @click="rules.conflict = 'skip'"
                  >
                    跳过并报告
                  </button>
                  <button
                    type="button"
                    :class="{ on: rules.conflict === 'suffix' }"
                    :aria-pressed="rules.conflict === 'suffix'"
                    @click="rules.conflict = 'suffix'"
                  >
                    自动追加序号
                  </button>
                </div>
                <p class="rsec-note">
                  {{
                    rules.conflict === 'suffix'
                      ? '冲突项按「(2)」起自动避让；磁盘同名文件永不写入。'
                      : '冲突项保持原名，执行后逐条报告；磁盘同名文件永不写入。'
                  }}
                </p>
              </div>
            </section>
          </div>
          <div class="rules-foot">
            <ShadeIcon name="info-circle" :size="12" />
            只改名、不移动；不提供覆盖与撤销。
          </div>
        </aside>
      </div>

      <div v-if="counts.warnings.length" class="rename-alert">
        <ShadeIcon name="info-circle" :size="12" />
        <span>{{ counts.warnings.join('；') }}</span>
      </div>

      <div class="rename-runbar">
        <div class="run-sum">
          <span class="it ok"
            ><i />可重命名 <b>{{ counts.can }}</b></span
          >
          <span class="it skip"
            ><i />将跳过 <b>{{ counts.skip }}</b></span
          >
          <span class="it same"
            ><i />不修改 <b>{{ counts.same }}</b></span
          >
          <span class="sep" />
          <span class="run-tip">{{ conflictTip(rules) }}</span>
        </div>
        <div class="run-actions">
          <span class="revoke"><b>执行后不可撤销</b>冲突项保持原名</span>
          <t-button
            theme="primary"
            :disabled="counts.can === 0 || running"
            :title="counts.can === 0 ? '当前没有可重命名的文件' : '核对数量后执行'"
            @click="openConfirm"
          >
            执行重命名
            <span class="run-count">{{ counts.can }}</span>
          </t-button>
        </div>
      </div>
    </template>

    <!-- ===================== 进度态 ===================== -->
    <section v-else-if="view === 'progress'" class="rename-card progress-card">
      <div class="prog-head">
        <div>
          <h2>正在重命名</h2>
          <p>逐项提交，完成后统一回读目录状态。</p>
        </div>
        <t-button theme="danger" variant="outline" :loading="cancelling" @click="cancelRun">
          {{ cancelling ? '正在取消' : '取消' }}
        </t-button>
      </div>
      <div class="prog-main">
        <div class="prog-top">
          <span class="prog-pct">{{ progress.pct }}<span>%</span></span>
          <div class="prog-nums">
            <div class="n">
              <b>{{ progress.done }}</b><span>已处理</span>
            </div>
            <div class="n">
              <b>{{ progress.rest }}</b><span>待处理</span>
            </div>
            <div class="n ok">
              <b>{{ progress.planned }}</b><span>计划改名</span>
            </div>
            <div class="n skip">
              <b>{{ progress.preSkip }}</b><span>预检跳过</span>
            </div>
            <div class="n same">
              <b>{{ progress.same }}</b><span>未变化</span>
            </div>
          </div>
        </div>
        <t-progress
          theme="plump"
          :percentage="progress.pct"
          color="#0E3DA8"
          track-color="rgba(14,61,168,.10)"
        />
      </div>
      <div v-if="progress.label" class="prog-current">当前文件：{{ progress.label }}</div>
      <div class="prog-body">
        <div class="prog-log-head">
          执行日志<span>{{ logs.length }} 行</span>
        </div>
        <div class="logview">{{ logs.length ? logs.join('\n') : '正在准备…' }}</div>
        <p class="prog-note">
          两阶段提交：互换名称与同批目标占用先改临时名，再落到目标名，不会互相覆盖。
        </p>
      </div>
    </section>

    <!-- ===================== 结果态 ===================== -->
    <template v-else>
      <div class="res-head">
        <span class="res-ico" :class="{ warn: outcome.failed }">
          <ShadeIcon :name="outcome.failed ? 'warn' : 'check'" :size="20" />
        </span>
        <div>
          <h2>{{ outcome.failed ? '部分未完成' : '重命名完成' }}</h2>
          <p>成功与失败已分开记录，可展开日志交班核对。</p>
        </div>
      </div>

      <div class="res-tiles">
        <div class="tile ok">
          <b>{{ resultCounts.success }}</b><span>成功</span>
        </div>
        <div class="tile bad">
          <b>{{ resultCounts.failed }}</b><span>失败</span>
        </div>
        <div class="tile skip">
          <b>{{ resultCounts.skipped }}</b><span>跳过</span>
        </div>
        <div class="tile mute">
          <b>{{ resultCounts.unchanged }}</b><span>未变化</span>
        </div>
        <div class="tile mute">
          <b>{{ resultCounts.not_run }}</b><span>未执行</span>
        </div>
      </div>

      <TaskResult :outcome="outcome" :logs="logs" :job-id="finishedJobId" />

      <section class="rename-card res-card">
        <div class="res-toolbar">
          <div class="res-filters">
            <button
              v-for="option in RESULT_FILTERS"
              :key="option.id"
              type="button"
              class="fchip"
              :class="{ on: resultFilter === option.id }"
              :aria-pressed="resultFilter === option.id"
              @click="resultFilter = option.id"
            >
              {{ option.label }} <b>{{ option.count(resultCounts) }}</b>
            </button>
          </div>
          <span class="right muted">共 {{ resultRows.length }} 条</span>
        </div>
        <div class="res-head-row">
          <span>状态</span>
          <span>原名称</span>
          <span>目标名称 / 说明</span>
          <span />
        </div>
        <div class="res-body">
          <div v-if="!filteredResultRows.length" class="empty-block res-empty">
            <b>当前筛选下没有记录</b>
          </div>
          <div v-for="row in filteredResultRows" :key="row.key" class="res-row">
            <span>
              <span class="tag" :class="row.status.theme"><i />{{ row.status.text }}</span>
            </span>
            <span class="onm" :title="row.name">{{ row.name }}</span>
            <span>
              <span
                class="nnm"
                :class="{ ok: row.result === 'success', fail: row.result !== 'success' }"
                :title="row.new_name"
                >{{ row.result === 'success' ? row.new_name || row.name : row.name }}</span
              >
              <span v-if="row.message" class="rsn" :title="row.message">{{ row.message }}</span>
            </span>
            <span />
          </div>
        </div>
        <div class="res-foot">
          <span class="note">失败项保持原名，可关闭占用程序后重新执行。</span>
          <span class="acts">
            <t-button size="small" variant="outline" @click="backToPreview">返回预览</t-button>
            <t-button size="small" theme="primary" @click="finishResult">完成</t-button>
          </span>
        </div>
      </section>

      <HistoryCard ref="histCard" app-id="batch_rename" :format-result="formatBatchHistory" />
    </template>

    <!-- ===================== 执行前二次确认 ===================== -->
    <t-dialog
      v-model:visible="confirmVisible"
      header="执行重命名"
      :footer="false"
      width="460px"
      placement="center"
    >
      <div class="confirm-count">
        <b>{{ counts.can }}</b><span>个文件将被重命名</span>
      </div>
      <p class="confirm-sub">
        冲突项不会写入。冲突文件保持原名，不会覆盖、删除或改写任何已有文件。本操作不提供撤销。
      </p>
      <p class="confirm-sub">目标目录保持原位置：只改文件名，不移动文件。</p>
      <div class="confirm-acts">
        <t-button variant="outline" @click="confirmVisible = false">取消</t-button>
        <t-button theme="primary" @click="startRun">确认重命名 {{ counts.can }} 个</t-button>
      </div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, nextTick, onUnmounted, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, pickDirectory, pickFiles, runJob } from '../api';
import ShadeIcon from '../components/ShadeIcon.vue';
import TaskResult from '../components/TaskResult.vue';
import HistoryCard from '../components/HistoryCard.vue';
import { restoreState } from '../lib/restore-bus.mjs';
import { baseOutcome } from '../lib/task-result.mjs';
import {
  APP_ID,
  batchOutcome,
  buildRenameParams,
  conflictTip,
  createRules,
  formatBatchHistory,
  kindOf,
  mergePickedFiles,
  previewCounts,
  pathKey,
  progressPlan,
  removeFile,
  restoreBatchParams,
  resultRows as toResultRows,
  rowNameTone,
  rowTag,
  rulesSummary,
  sequenceExample,
  splitName,
  typeText,
} from '../lib/batch-rename.mjs';

const KIND_ICON = { image: 'image', doc: 'doc', sheet: 'sheet', text: 'text', other: 'file' };
const RESULT_FILTERS = [
  { id: 'all', label: '全部', count: (counts) => totalResultCount(counts) },
  { id: 'success', label: '成功', count: (counts) => counts.success },
  { id: 'failed', label: '失败', count: (counts) => counts.failed },
  { id: 'skipped', label: '跳过', count: (counts) => counts.skipped },
  { id: 'unchanged', label: '未变化', count: (counts) => counts.unchanged },
  { id: 'not_run', label: '未执行', count: (counts) => counts.not_run },
];

function totalResultCount(counts) {
  return counts.success + counts.failed + counts.skipped + counts.unchanged + counts.not_run;
}

function kindIcon(name) {
  return KIND_ICON[kindOf(name)] || 'file';
}

const view = ref('preview');
const files = ref([]);
const directory = ref('');
const rules = reactive(createRules());
const previewResult = ref(null);
const previewLoading = ref(false);
const previewError = ref('');
const logs = ref([]);
const running = ref(false);
const cancelling = ref(false);
const confirmVisible = ref(false);
const outcome = ref({ status: '尚未开始', summary: '', failed: false });
const resultData = ref(null);
const finishedJobId = ref('');
const resultFilter = ref('all');
// 进度只展示可确认的数字：后端进度事件只给 current/total 与当前文件名，
// 成功/失败要到终态才有，因此运行中显示「已处理 / 待处理 / 计划改名 / 预检跳过」。
const progress = reactive({
  pct: 0,
  done: 0,
  rest: 0,
  planned: 0,
  preSkip: 0,
  same: 0,
  label: '',
});
const restoreNotice = ref('');
const histCard = ref(null);

let job = null;
let previewTimer = null;
let previewToken = 0;
let totalRows = 0;

const hasSource = computed(() => files.value.length > 0 || directory.value !== '');
const counts = computed(() => previewCounts(previewResult.value));
// 进度分母用后端 rows 总数（含跳过与未变化），与 run() 的 job.set_progress(total=len(rows)) 对齐
const plan = computed(() => progressPlan(previewResult.value));
const sourceText = computed(() => {
  if (directory.value) return directory.value;
  if (!files.value.length) return '尚未选择文件';
  const dirs = new Set(files.value.map((path) => path.replace(/[\\/][^\\/]*$/, '')));
  return dirs.size === 1 ? [...dirs][0] : '混合文件（来自多个目录）';
});
const sourceMeta = computed(() => {
  if (directory.value) return `${files.value.length || displayRows.value.length} 个文件`;
  return `${files.value.length} 个文件`;
});
const displayRows = computed(() => {
  const rows =
    previewResult.value && Array.isArray(previewResult.value.rows)
      ? previewResult.value.rows
      : null;
  if (rows) {
    return rows.map((row, index) => {
      const tag = rowTag(row);
      return {
        key: `${row.path || row.name || ''}::${index}`,
        // 完整路径是移除/回看的唯一定位依据，不能只留文件名
        path: String(row.path || ''),
        name: String(row.name || ''),
        new_name: String(row.new_name || ''),
        reason: String(row.reason || ''),
        tag,
        tone: rowNameTone(row),
        conflict: tag.theme === 'danger',
      };
    });
  }
  return files.value.map((path, index) => ({
    key: `${path}::${index}`,
    path: String(path),
    name: path.split(/[\\/]/).pop() || path,
    new_name: '',
    reason: '',
    tag: { theme: 'default', text: '待预览' },
    tone: 'same',
    conflict: false,
  }));
});
const exampleName = computed(() => {
  const sample = displayRows.value[0]?.name || '';
  return sequenceExample(rules, sample);
});
// 统一主名为空时后端按「文件」兜底（fileops/naming.py），界面要提前说清，避免执行后才发现
const sequenceWarn = computed(() => rules.sequence.enabled && !rules.sequence.base.trim());
const sequenceNote = computed(() => {
  if (!rules.sequence.enabled) {
    return '关闭时保留原主名；开启后按可提交项顺序连续编号，扩展名不动。';
  }
  if (sequenceWarn.value) return '统一主名为空，将使用「文件」兜底；建议填写明确主名。';
  return '按可提交项连续编号；非法与未变化项不占序号。';
});
const resultRows = computed(() => toResultRows(resultData.value));
const resultCounts = computed(() => {
  const key = resultData.value || {};
  return {
    success: Number(key.success) || 0,
    failed: Number(key.failed) || 0,
    skipped: Number(key.skipped) || 0,
    unchanged: Number(key.unchanged) || 0,
    not_run: Number(key.not_run) || 0,
  };
});
const filteredResultRows = computed(() =>
  resultFilter.value === 'all'
    ? resultRows.value
    : resultRows.value.filter((row) => row.result === resultFilter.value)
);

function currentParams() {
  return buildRenameParams({
    files: files.value,
    directory: directory.value,
    rules,
  });
}

function addPair() {
  rules.replace.push({ find: '', replace: '', case_sensitive: false });
}

function removePair(index) {
  rules.replace.splice(index, 1);
}

// 按路径定位：预检防抖期间列表可能已重排，用下标会删错行
// 按完整路径定位：防抖期间列表可能已重排，用下标会删错行；
// 也不能退回文件名——不同目录下的同名文件会一起被删掉。
function removeRow(row) {
  if (directory.value || !row) return;
  files.value = removeFile(files.value, row.path);
}

async function addFiles() {
  if (running.value) return;
  let picked = [];
  try {
    picked = (await pickFiles()) || [];
  } catch (e) {
    MessagePlugin.error(String((e && e.message) || e));
    return;
  }
  if (!picked.length) return;
  if (directory.value) {
    // 目录模式下列表来自磁盘，无法逐项混合：改为文件列表模式并明确告知
    directory.value = '';
    files.value = [];
    MessagePlugin.info('已切换到文件列表模式，目录筛选已取消');
  }
  const merged = mergePickedFiles(files.value, picked);
  files.value = merged.files;
  if (merged.duplicates > 0) {
    MessagePlugin.info(`已跳过 ${merged.duplicates} 个重复文件`);
  }
}

async function addFolder() {
  if (running.value) return;
  let picked = '';
  try {
    picked = await pickDirectory();
  } catch (e) {
    MessagePlugin.error(String((e && e.message) || e));
    return;
  }
  if (!picked) return;
  directory.value = picked;
  files.value = [];
  previewResult.value = null;
  schedulePreview();
}

function clearList() {
  files.value = [];
  directory.value = '';
  previewResult.value = null;
  previewError.value = '';
  restoreNotice.value = '';
}

function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => {
    previewTimer = null;
    loadPreview();
  }, 260);
}

async function loadPreview() {
  const params = currentParams();
  if (!params.files.length && !params.directory) {
    previewResult.value = null;
    previewError.value = '';
    previewLoading.value = false;
    return;
  }
  const token = ++previewToken;
  previewLoading.value = true;
  try {
    const result = await call('preview', { app_id: APP_ID, params });
    if (token !== previewToken) return;
    previewResult.value = result || null;
    previewError.value = '';
  } catch (e) {
    if (token !== previewToken) return;
    previewResult.value = null;
    previewError.value = `预检失败：${String((e && e.message) || e)}`;
  } finally {
    if (token === previewToken) previewLoading.value = false;
  }
}

function resetProgress(step) {
  progress.pct = 0;
  progress.done = 0;
  progress.rest = step.total;
  progress.planned = step.planned;
  progress.preSkip = step.skip;
  progress.same = step.same;
  progress.label = '';
}

// 确认弹层是唯一防误入口：先看数量再执行，页面不提供跳过确认的快捷通道
function openConfirm() {
  if (running.value || counts.value.can === 0) return;
  confirmVisible.value = true;
}

function startRun() {
  if (running.value || counts.value.can === 0) return;
  confirmVisible.value = false;
  const params = currentParams();
  const total = plan.value.total;
  totalRows = total;
  logs.value = [];
  resultData.value = null;
  finishedJobId.value = '';
  resultFilter.value = 'all';
  restoreNotice.value = '';
  resetProgress(plan.value);
  view.value = 'progress';
  running.value = true;
  cancelling.value = false;
  outcome.value = {
    status: '执行中',
    summary: `正在处理 ${counts.value.can} 项（共 ${total} 项）`,
    failed: false,
  };

  job = runJob(APP_ID, params, {
    onLog: (line) => logs.value.push(String(line)),
    onProgress: (p) => {
      const current = Number(p?.current) || 0;
      // 后端逐项 set_progress(done, len(rows))；缺失时退回本次预览的行总数
      const processedTotal = Number(p?.total) || totalRows;
      progress.pct =
        processedTotal > 0 ? Math.min(100, Math.round((current * 100) / processedTotal)) : 0;
      progress.done = Math.min(current, processedTotal);
      progress.rest = Math.max(0, processedTotal - progress.done);
      if (p && p.label) progress.label = String(p.label);
    },
    onDone: (doneJob) => {
      if (!job) return;
      job = null;
      running.value = false;
      cancelling.value = false;
      resultData.value = (doneJob && doneJob.result) || {};
      finishedJobId.value = String((doneJob && doneJob.job_id) || '');
      outcome.value = batchOutcome(doneJob);
      progress.pct = 100;
      // 终态：全部行都已处理完，done 取本次后端行数（不是「计划改名」数）
      progress.done = totalRows || progress.planned;
      progress.rest = 0;
      view.value = 'result';
      // 结果区的历史卡随结果视图一起挂载，等它渲染完再刷新
      nextTick(() => histCard.value && histCard.value.reload());
    },
    onError: (err) => {
      if (!job) return;
      job = null;
      running.value = false;
      cancelling.value = false;
      const raw = String((err && err.message) || err || '任务启动失败');
      // 泛化摘要会让用户失去排查线索：真实错误同时进日志与结果卡
      logs.value.push(`启动失败：${raw}`);
      outcome.value = {
        status: '启动失败',
        summary: `启动失败：${raw}`,
        failed: true,
      };
      resultData.value = null;
      view.value = 'result';
    },
  });
}

async function cancelRun() {
  if (!job || cancelling.value) return;
  cancelling.value = true;
  try {
    await job.cancel();
  } catch (e) {
    cancelling.value = false;
    MessagePlugin.error(String((e && e.message) || e));
  }
}

// 返回预览：目录模式重读磁盘（文件名已变）；文件列表模式只保留没成功的项
// ——成功项的新路径已不可用，留在列表里只会预检成「文件不存在」。
function backToPreview() {
  view.value = 'preview';
  const data = resultData.value;
  resultData.value = null;
  if (directory.value) {
    previewResult.value = null;
    schedulePreview();
    return;
  }
  const done = new Set(
    (Array.isArray(data && data.rows) ? data.rows : [])
      .filter((row) => row && row.result === 'success')
      .map((row) => pathKey(row.path))
  );
  const dropped = files.value.filter((path) => done.has(pathKey(path))).length;
  files.value = files.value.filter((path) => !done.has(pathKey(path)));
  if (dropped) {
    MessagePlugin.info(`已从列表移除 ${dropped} 个改名成功的文件（旧路径已失效）`);
  }
  if (!files.value.length) previewResult.value = null;
  else schedulePreview();
}

function finishResult() {
  restoreNotice.value = '';
  // 「完成」回到干净的预览：目录模式重读第一层，文件列表模式清空（旧路径已失效）
  if (directory.value) {
    view.value = 'preview';
    previewResult.value = null;
    resultData.value = null;
    schedulePreview();
    return;
  }
  files.value = [];
  directory.value = '';
  previewResult.value = null;
  resultData.value = null;
  view.value = 'preview';
}

let syncingRules = false;

// 执行中回填会换掉正在跑的规则与列表，进度与取消入口也会被顶掉：直接拒绝并说明
function applyRestore(params) {
  if (running.value) {
    MessagePlugin.warning('任务执行中，无法回填参数；请等待完成或先在进度区取消。');
    return;
  }
  const restored = restoreBatchParams(params || {});
  syncingRules = true;
  files.value = restored.files;
  directory.value = restored.directory;
  Object.assign(rules, restored.rules);
  syncingRules = false;
  view.value = 'preview';
  previewResult.value = null;
  restoreNotice.value = restored.directory
    ? `已回填目录「${restored.directory}」与上次规则，核对预览后点击「执行重命名」；不会自动执行。`
    : `已回填 ${restored.files.length} 个文件与上次规则，核对预览后点击「执行重命名」；不会自动执行。`;
  outcome.value = {
    status: '待执行',
    summary: '已回填上次参数，核对预览后点击「执行重命名」；不会自动执行。',
    failed: false,
  };
  schedulePreview();
}

watch(
  () => restoreState.nonce,
  () => {
    if (restoreState.appID !== APP_ID) return;
    applyRestore(restoreState.params);
  },
  { immediate: true }
);

watch(
  [files, directory, rules],
  () => {
    if (syncingRules || view.value !== 'preview') return;
    schedulePreview();
  },
  { deep: true }
);

onUnmounted(() => {
  clearTimeout(previewTimer);
  previewTimer = null;
  previewToken += 1;
});
</script>

<style scoped>
.rename-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
/* 带边框的两条横条按 border-box 计算，min-height 才是最终高度 */
.rename-source,
.rename-runbar {
  box-sizing: border-box;
}
/* 来源条：与设计稿一致的浅边框白底条，起止信息不换行 */
.rename-source {
  display: flex;
  align-items: center;
  gap: 9px;
  min-height: 40px;
  padding: 6px 10px 6px 12px;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 10px;
  box-shadow: var(--tk-shadow-card);
}
.src-icon {
  width: 26px;
  height: 26px;
  border-radius: 8px;
  flex: none;
  background: var(--tk-blue-soft);
  color: var(--tk-brand-deep);
  display: grid;
  place-items: center;
}
.src-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex: 0 1 auto;
  max-width: 52%;
}
.src-label {
  font-size: 11px;
  color: var(--tk-ph);
  flex: none;
}
.src-path {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-sub);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}
.src-flag {
  flex: none;
  height: 20px;
  padding: 0 7px;
  border-radius: 4px;
  background: var(--tk-fill);
  color: var(--tk-ph);
  font-size: 10.5px;
  font-weight: 600;
  display: inline-flex;
  align-items: center;
}
.src-meta {
  font-size: 11px;
  color: var(--tk-ph);
  white-space: nowrap;
  flex: none;
  font-variant-numeric: tabular-nums;
}
.src-acts {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}
.link-danger {
  border: 0;
  background: transparent;
  color: var(--tk-red);
  font-size: 11.5px;
  font-weight: 500;
  padding: 3px 2px;
  border-radius: 4px;
  cursor: pointer;
}
.link-danger:hover:not(:disabled) {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.link-danger:disabled {
  color: var(--tk-dash);
  cursor: not-allowed;
}

/* ===== 预览态占满可视高度：来源条固定、列表与规则内部滚动、执行条固定 ===== */
.rename-page.is-preview {
  height: 100%;
  min-height: 0;
}
.rename-page.is-preview .rename-work {
  flex: 1;
  min-height: 0;
}
.rename-page.is-preview .rename-card {
  min-height: 0;
  max-height: 100%;
}
.rename-page.is-preview .pv-body {
  flex: 1;
  min-height: 0;
  max-height: none;
  overflow: auto;
}

/* 列表 + 规则两列；窄窗（960×620）规则列收窄，列表与原因仍完整 */
.rename-work {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 340px;
  gap: 12px;
  /* stretch：两列同高，列表与规则各自内部滚动，页面不整体滚动 */
  align-items: stretch;
}
.rename-card {
  min-width: 0;
  background: #fff;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-card);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.card-head {
  display: flex;
  align-items: baseline;
  gap: 9px;
  padding: 12px 15px 10px;
  border-bottom: 1px solid var(--tk-line);
}
.card-head b {
  font-size: 14px;
  font-weight: 700;
}
.card-sub {
  font-size: 10.5px;
  color: var(--tk-ph);
}
.card-head .right {
  margin-left: auto;
  font-size: 10.5px;
  color: var(--tk-ph);
}
.pv-head,
.pv-row {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr) 20px minmax(0, 1.12fr) 26px;
  gap: 10px;
  align-items: center;
}
.pv-head {
  padding: 7px 15px;
  background: var(--tk-fill);
  border-bottom: 1px solid var(--tk-line);
  font-size: 10px;
  font-weight: 600;
  color: var(--tk-ph);
  letter-spacing: 0.4px;
}
.pv-body {
  max-height: 46vh;
  overflow: auto;
}
.pv-row {
  padding: 8px 15px;
  border-bottom: 1px solid #f2f3f5;
}
.pv-row:hover {
  background: #fbfcff;
}
.pv-row.is-conflict:hover {
  background: #fffaf7;
}
.pv-old {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.f-ico {
  width: 27px;
  height: 27px;
  border-radius: 8px;
  flex: none;
  background: var(--tk-blue-soft);
  color: var(--tk-brand-deep);
  display: grid;
  place-items: center;
}
.f-ico.kind-doc,
.f-ico.kind-text {
  background: #eef2fb;
  color: #33518f;
}
.f-ico.kind-sheet {
  background: #eaf7ef;
  color: #1f7a46;
}
.nm {
  min-width: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
}
.nm-main,
.nm-ext {
  font-size: 13.5px;
  line-height: 1.4;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}
.nm-main {
  font-weight: 500;
  color: var(--tk-ink);
}
.nm-ext {
  color: var(--tk-ph);
  font-size: 12px;
  flex: none;
}
.nm-meta {
  flex-basis: 100%;
  margin-top: 1px;
  font-size: 10.5px;
  color: var(--tk-ph);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pv-arrow {
  color: var(--tk-dash);
  display: grid;
  place-items: center;
}
.pv-new {
  min-width: 0;
}
.new-name {
  display: block;
  font-size: 13.5px;
  line-height: 1.4;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.new-name.ok {
  color: var(--tk-brand);
}
.new-name.info {
  color: #2f63c9;
}
.new-name.bad {
  color: var(--tk-red);
}
.new-name.same {
  color: var(--tk-ph);
  font-weight: 500;
}
.reason {
  display: block;
  margin-top: 1px;
  font-size: 10.5px;
  line-height: 1.45;
  color: var(--tk-ph);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.reason.bad {
  color: var(--tk-red);
}
.row-x {
  width: 26px;
  height: 26px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--tk-ph);
  display: grid;
  place-items: center;
  cursor: pointer;
  opacity: 0;
}
.pv-row:hover .row-x,
.pv-row:focus-within .row-x {
  opacity: 1;
}
.row-x:hover:not(:disabled) {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.row-x:disabled {
  cursor: not-allowed;
  opacity: 0.3;
}
.tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 21px;
  padding: 0 7px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
  background: var(--tk-fill);
  color: var(--tk-ph);
}
.tag i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
  flex: none;
}
.tag.success {
  background: var(--tk-green-soft);
  color: #0b8a37;
}
.tag.danger {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.tag.warning {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.tag.primary {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.tag.default {
  background: var(--tk-fill);
  color: var(--tk-ph);
}
.list-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 15px;
  border-top: 1px solid var(--tk-line);
  font-size: 11px;
  color: var(--tk-ph);
  font-variant-numeric: tabular-nums;
}
.list-foot b {
  color: var(--tk-sub);
  font-weight: 600;
}
.list-foot .right {
  margin-left: auto;
}
.empty-block {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 32px 24px;
  text-align: center;
}
.empty-icon {
  width: 46px;
  height: 46px;
  border-radius: 12px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: grid;
  place-items: center;
}
.empty-block b {
  font-size: 13px;
}
.empty-block p {
  margin: 0;
  font-size: 11.5px;
  color: var(--tk-ph);
  line-height: 1.6;
}
.empty-acts {
  display: flex;
  gap: 8px;
  margin-top: 2px;
}

/* 规则面板 */
.rules-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 0 15px 10px;
}
.rsec {
  padding: 10px 0;
  border-bottom: 1px solid #f0f1f3;
}
.rsec:last-of-type {
  border-bottom: 0;
}
.rsec-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.rsec-head .no {
  width: 15px;
  height: 15px;
  border-radius: 4px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 9.5px;
  font-weight: 700;
  display: grid;
  place-items: center;
  flex: none;
}
.rsec-head b {
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-sub);
}
.rsec-head .hint {
  font-size: 10.5px;
  color: var(--tk-ph);
}
.r-add {
  margin-left: auto;
  height: 22px;
  padding: 0 7px;
  font-size: 10px;
  font-weight: 600;
  border-radius: 6px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-brand);
  cursor: pointer;
}
.r-add:hover {
  border-color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.rsec-body {
  margin-top: 7px;
}
.rsec-note {
  margin: 6px 0 0;
  font-size: 10.5px;
  line-height: 1.5;
  color: var(--tk-ph);
}
.rsec-note.warn {
  color: #d25f00;
}
.pair-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 14px minmax(0, 1fr) 22px;
  gap: 5px;
  align-items: center;
}
.pair-row + .pair-row {
  margin-top: 5px;
}
.pair-row .to {
  color: var(--tk-dash);
  display: grid;
  place-items: center;
}
.pair-row .icon-btn {
  width: 22px;
  height: 26px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--tk-ph);
  display: grid;
  place-items: center;
  cursor: pointer;
}
.pair-row .icon-btn:hover {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.pair-case {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 10.5px;
  color: var(--tk-ph);
}
.fld-label {
  display: block;
  font-size: 11px;
  color: var(--tk-ph);
  margin: 0 0 4px 1px;
}
.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
}
.three-col {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  gap: 6px;
}
.seq-extra {
  margin-top: 7px;
}
.head-switch {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 10.5px;
  color: var(--tk-ph);
}
.seq-preview {
  margin-top: 6px;
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 6px 8px;
  background: var(--tk-fill);
  border-radius: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  min-width: 0;
}
.seq-preview b {
  color: var(--tk-brand-deep);
  font-size: 10.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 警示块按规范直角 + 左色条 */
.alert {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 7px 9px;
  border-left: 3px solid var(--tk-yellow);
  background: var(--tk-yellow-soft);
  font-size: 11px;
  line-height: 1.55;
  color: var(--tk-sub);
  margin-top: 7px;
  border-radius: 0;
}
.rules-foot {
  padding: 8px 15px;
  border-top: 1px solid var(--tk-line);
  background: var(--tk-fill);
  font-size: 10.5px;
  color: var(--tk-ph);
  display: flex;
  align-items: center;
  gap: 5px;
}
.seg {
  display: flex;
  gap: 2px;
  padding: 2px;
  border-radius: 8px;
  background: #e5e6eb;
}
.seg button {
  flex: 1;
  height: 26px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 10.5px;
  color: var(--tk-sub);
  cursor: pointer;
}
.seg button:hover {
  background: rgba(255, 255, 255, 0.55);
}
.seg button.on {
  background: #fff;
  color: var(--tk-brand);
  font-weight: 600;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
}

/* 底部执行条：页面整体随壳层滚动，执行条吸底，窄窗下主按钮不必滚到底才可见 */
.rename-runbar {
  position: sticky;
  bottom: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  gap: 14px;
  min-height: 56px;
  padding: 9px 12px 9px 16px;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 12px;
  box-shadow: var(--tk-shadow-card);
}
.run-sum {
  display: flex;
  align-items: center;
  gap: 14px;
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  font-size: 11.5px;
  color: var(--tk-ph);
}
.run-sum .it {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
  flex: none;
}
.run-sum .it i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
.run-sum .it b {
  color: var(--tk-ink);
  font-size: 13px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.run-sum .it.ok i {
  background: var(--tk-green);
}
.run-sum .it.skip i {
  background: var(--tk-yellow);
}
.run-sum .it.same i {
  background: var(--tk-dash);
}
.run-sum .sep {
  width: 1px;
  height: 14px;
  flex: none;
  background: var(--tk-line);
}
.run-tip {
  font-size: 10.5px;
  color: var(--tk-ph);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.run-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
}
.run-actions .revoke {
  font-size: 9.5px;
  color: var(--tk-ph);
  line-height: 1.4;
  text-align: right;
}
.run-actions .revoke b {
  display: block;
  color: var(--tk-sub);
  font-size: 10px;
  font-weight: 600;
}
.run-count {
  margin-left: 6px;
  min-width: 19px;
  height: 19px;
  padding: 0 5px;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.2);
  font-size: 11px;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-variant-numeric: tabular-nums;
}
.rename-alert {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 8px 10px;
  border-left: 3px solid var(--tk-brand);
  background: var(--tk-blue-soft);
  font-size: 11px;
  line-height: 1.55;
  color: var(--tk-sub);
  border-radius: 0;
}
/* 回填提示：同一套「浅底 + 左色条」直角信息块，右侧给「知道了」关闭 */
.rename-restore {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-left: 3px solid var(--tk-brand);
  background: var(--tk-blue-soft);
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--tk-sub);
  border-radius: 0;
}
.rename-restore .r-add {
  margin-left: auto;
  flex: none;
}

/* 进度态 */
.progress-card {
  padding-bottom: 16px;
}
.prog-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 18px 20px 0;
}
.prog-head h2 {
  font-size: 18px;
  font-weight: 700;
  margin: 0;
}
.prog-head p {
  margin: 3px 0 0;
  font-size: 11.5px;
  color: var(--tk-ph);
}
.prog-head .t-button {
  margin-left: auto;
  flex: none;
}
.prog-main {
  padding: 16px 20px 14px;
}
.prog-top {
  display: flex;
  align-items: flex-end;
  gap: 12px;
}
.prog-pct {
  font-size: 34px;
  line-height: 1;
  font-weight: 700;
  color: var(--tk-brand-deep);
  letter-spacing: -0.5px;
  font-variant-numeric: tabular-nums;
}
.prog-pct span {
  font-size: 15px;
  font-weight: 600;
  color: var(--tk-ph);
  margin-left: 1px;
}
.prog-nums {
  margin-left: auto;
  display: flex;
  gap: 16px;
  padding-bottom: 2px;
}
.prog-nums .n {
  text-align: right;
}
.prog-nums b {
  display: block;
  font-size: 16px;
  font-weight: 700;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
}
.prog-nums span {
  font-size: 10.5px;
  color: var(--tk-ph);
}
.prog-nums .ok b {
  color: #0b8a37;
}
.prog-nums .bad b {
  color: var(--tk-red);
}
.prog-nums .skip b {
  color: #d25f00;
}
.prog-nums .same b {
  color: var(--tk-ph);
}
.prog-current {
  margin: 0 20px;
  padding: 10px 12px;
  background: var(--tk-fill);
  border-radius: 8px;
  font-size: 12px;
  color: var(--tk-sub);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.prog-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 20px 0;
}
.prog-log-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 10px;
  font-weight: 600;
  color: var(--tk-ph);
  letter-spacing: 0.4px;
}
.prog-log-head span {
  margin-left: auto;
  font-weight: 400;
  letter-spacing: 0;
  font-variant-numeric: tabular-nums;
}
.prog-body .logview {
  height: 200px;
  max-height: 32vh;
}
.prog-note {
  margin: 0;
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 8px 10px;
  border-left: 3px solid var(--tk-brand);
  background: var(--tk-blue-soft);
  font-size: 11px;
  line-height: 1.55;
  color: var(--tk-sub);
  border-radius: 0;
}

/* 结果态 */
.res-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.res-ico {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  background: var(--tk-green-soft);
  color: #0b8a37;
  display: grid;
  place-items: center;
  flex: none;
}
.res-ico.warn {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.res-head h2 {
  font-size: 18px;
  font-weight: 700;
  margin: 0;
}
.res-head p {
  margin: 2px 0 0;
  font-size: 11.5px;
  color: var(--tk-ph);
}
.res-tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(108px, 1fr));
  gap: 8px;
}
.tile {
  background: #fff;
  border-radius: 10px;
  box-shadow: var(--tk-shadow-card);
  padding: 9px 12px;
  border-left: 3px solid var(--tk-line);
  display: flex;
  align-items: baseline;
  gap: 7px;
}
.tile b {
  font-size: 20px;
  font-weight: 700;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
}
.tile span {
  font-size: 11px;
  color: var(--tk-ph);
}
.tile.ok {
  border-left-color: var(--tk-green);
}
.tile.ok b {
  color: #0b8a37;
}
.tile.bad {
  border-left-color: var(--tk-red);
}
.tile.bad b {
  color: var(--tk-red);
}
.tile.skip {
  border-left-color: var(--tk-yellow);
}
.tile.skip b {
  color: #d25f00;
}
.tile.mute b {
  color: var(--tk-ph);
}
.res-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 14px;
  border-bottom: 1px solid var(--tk-line);
}
.res-toolbar .right {
  margin-left: auto;
}
.res-filters {
  display: flex;
  gap: 3px;
  flex-wrap: wrap;
}
.fchip {
  height: 26px;
  padding: 0 9px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--tk-sub);
  font-size: 10.5px;
  cursor: pointer;
}
.fchip:hover {
  background: var(--tk-fill);
}
.fchip.on {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-weight: 600;
}
.fchip b {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.res-head-row,
.res-row {
  display: grid;
  grid-template-columns: 80px minmax(0, 1fr) minmax(0, 1.1fr) 26px;
  gap: 10px;
  align-items: center;
}
.res-head-row {
  padding: 7px 14px;
  background: var(--tk-fill);
  border-bottom: 1px solid var(--tk-line);
  font-size: 10px;
  font-weight: 600;
  color: var(--tk-ph);
  letter-spacing: 0.4px;
}
.res-body {
  max-height: 34vh;
  overflow: auto;
}
.res-row {
  padding: 8px 14px;
  border-bottom: 1px solid #f2f3f5;
}
.res-row .onm,
.res-row .nnm {
  font-size: 11.5px;
  color: var(--tk-sub);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.res-row .onm {
  color: var(--tk-ph);
}
.res-row .nnm {
  display: block;
  font-weight: 500;
  color: var(--tk-ink);
}
.res-row .nnm.ok {
  color: var(--tk-brand);
  font-weight: 600;
}
.res-row .nnm.fail {
  color: var(--tk-ph);
  text-decoration: line-through;
  text-decoration-color: #f3b7b7;
}
.res-row .rsn {
  display: block;
  margin-top: 1px;
  font-size: 10.5px;
  color: var(--tk-ph);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.res-empty {
  padding: 24px;
  color: var(--tk-ph);
}
.res-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-top: 1px solid var(--tk-line);
}
.res-foot .note {
  font-size: 10.5px;
  color: var(--tk-ph);
}
.res-foot .acts {
  margin-left: auto;
  display: flex;
  gap: 8px;
}
.confirm-count {
  display: flex;
  align-items: baseline;
  gap: 7px;
  margin: 2px 0 6px;
}
.confirm-count b {
  font-size: 28px;
  font-weight: 700;
  color: var(--tk-brand);
  line-height: 1.1;
  font-variant-numeric: tabular-nums;
}
.confirm-count span {
  font-size: 11.5px;
  color: var(--tk-sub);
}
.confirm-sub {
  margin: 0 0 6px;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--tk-sub);
}
.confirm-acts {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 14px;
}

/* 960×620（主窗口最小尺寸）下规则列收窄，列表与原因仍完整可见；
   默认 1160×780 窗口保持 340px 规则列，因此断点取两者之间。 */
@media (max-width: 1060px) {
  .rename-work {
    grid-template-columns: minmax(0, 1fr) 268px;
    gap: 9px;
  }
  .pv-head,
  .pv-row {
    grid-template-columns: 70px minmax(0, 1fr) 15px minmax(0, 1fr) 22px;
    gap: 7px;
    padding-left: 10px;
    padding-right: 10px;
  }
  .nm-meta {
    display: none;
  }
  .nm-main,
  .new-name {
    font-size: 12.5px;
  }
  .res-head-row,
  .res-row {
    grid-template-columns: 70px minmax(0, 1fr) minmax(0, 1fr) 22px;
    gap: 7px;
    padding-left: 10px;
    padding-right: 10px;
  }
  .res-foot .note,
  .run-tip,
  .run-actions .revoke {
    display: none;
  }
  .rules-scroll {
    padding: 0 12px 10px;
  }
  .card-head {
    padding: 9px 12px 7px;
  }
}

/* 减少动态：关闭入场与过渡（页面自带动画全部在这里统一降级） */
@media (prefers-reduced-motion: reduce) {
  .rename-page *,
  .rename-page *::before,
  .rename-page *::after {
    animation: none !important;
    transition: none !important;
  }
}
</style>
