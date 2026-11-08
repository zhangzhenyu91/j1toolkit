<template>
  <div class="pcd-page">
    <t-card>
      <div class="sec-title">选择单号</div>
      <div class="row">
        <span class="muted grow">{{ fileName || '未选择文件' }}</span>
        <t-button variant="outline" @click="pickXlsx">选择派车单表格…</t-button>
      </div>
      <div class="muted" style="margin-top: 10px">或直接粘贴派车单号：</div>
      <textarea
        class="paste-box"
        :value="codesText"
        rows="4"
        placeholder="也可以直接粘贴派车单号，每行一个（或用逗号/空格分隔）"
        @input="onPasteInput"
      ></textarea>
      <div class="row" style="margin-top: 8px">
        <t-button size="small" variant="outline" @click="parseNow">确认粘贴的单号</t-button>
        <span v-if="parsing" class="muted">正在解析…</span>
        <span v-if="codes.length" class="preview-count">共 {{ codes.length }} 个单号</span>
        <span v-if="previewSource" class="muted">来源：{{ previewSource }}</span>
      </div>
      <div v-if="previewEmpty" class="preview-empty">{{ previewEmpty }}</div>
      <div v-if="codes.length" class="codeview" style="margin-top: 8px; max-height: 180px">
        <div v-for="code in codes.slice(0, 100)" :key="code.seq">
          {{ code.seq }}. {{ code.code
          }}<span v-if="code.date_hint">　（{{ code.date_hint }}）</span>
        </div>
        <div v-if="codes.length > 100" class="muted">仅预览前 100 个</div>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">保存位置与导出内容</div>
      <div class="row">
        <span class="muted">保存位置</span>
        <t-input v-model="outdir" class="grow" placeholder="选择或输入导出文件的保存位置" />
        <t-button variant="outline" @click="browseOutdir">选择文件夹…</t-button>
      </div>
      <div class="row" style="margin-top: 10px">
        <t-checkbox v-model="opt.batchSubdir">按批次新建文件夹（避免覆盖）</t-checkbox>
        <t-checkbox v-model="opt.pcd">导出派车单（可打印文件）</t-checkbox>
        <t-checkbox v-model="opt.track">导出轨迹图和轨迹点明细</t-checkbox>
        <t-checkbox v-model="opt.calibrateNoon">自动校准行程结束时间</t-checkbox>
      </div>
      <div class="muted" style="margin-top: 8px">
        同批次重复导出会跳过已完成的单号；导出后提供合并文件、导出清单和未找到单号记录。
        勾选自动校准后，行程结束时间早于 12 点的轨迹按 1 小时逐次叠加校准，并写入行程清单。
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">开始导出</div>
      <div class="row">
        <t-button
          class="start-btn"
          theme="primary"
          :disabled="parsing || !codes.length || running || (clusterMode && !clusterSelectedCount)"
          @click="startRun"
        >
          {{ clusterMode ? '开始集群导出' : '开始导出' }}
        </t-button>
        <t-button v-if="running" theme="danger" @click="cancel">取消导出</t-button>
        <span v-if="!codes.length && !parsing" class="muted">
          请先选择派车单表格，或粘贴单号后确认
        </span>
        <span v-if="parsing" class="muted">解析完成前不能开始导出</span>
      </div>
      <div class="row" style="margin-top: 8px">
        <t-switch v-model="opt.keepAwake" size="large" :disabled="running" />
        <span class="muted">运行时防止关屏/休眠</span>
      </div>

      <div class="row" style="margin-top: 10px">
        <t-checkbox v-model="clusterMode" :disabled="!clusterEnabled"
          >使用集群导出（多节点分片）</t-checkbox
        >
        <span v-if="clusterMode" class="muted">
          {{ clusterReadyCount }} 个节点就绪；未勾选节点由发起节点兜底
        </span>
        <span v-else-if="!clusterEnabled" class="muted">集群服务未启用</span>
      </div>
      <div v-if="clusterMode" class="cluster-box">
        <div v-if="!clusterNodes.length" class="muted">
          集群没有可用节点，请先在「集群」页完成入网审批。
        </div>
        <t-table
          v-else
          :data="clusterNodes"
          :columns="clusterCols"
          size="small"
          row-key="node_id"
          :max-height="240"
        >
          <template #selected="{ row }">
            <t-checkbox v-model="clusterSel[row.node_id]" :disabled="!nodeReady(row)" />
          </template>
          <template #name="{ row }">
            <b>{{ row.name || row.node_id }}</b>
            <span class="muted" style="margin-left: 6px">{{
              row.role === 'hub' ? 'Hub' : 'Worker'
            }}</span>
            <div class="muted" style="font-size: 11px">
              {{ row.platform || '' }} / {{ row.arch || '' }}
            </div>
          </template>
          <template #status="{ row }">
            <span class="status-line">
              <span class="status-dot" :class="row.online ? 'success' : 'warning'" />
              {{ nodeStatusText(row) }}
            </span>
          </template>
          <template #query="{ row }">
            <t-input-number v-model="jobOf(row).query" :min="1" :max="16" theme="normal" />
          </template>
          <template #render="{ row }">
            <t-input-number v-model="jobOf(row).render" :min="1" :max="16" theme="normal" />
          </template>
        </t-table>
      </div>

      <div style="margin-top: 10px">
        <t-progress
          theme="plump"
          :percentage="progress.pct"
          :label="progress.text"
          color="#0E3DA8"
          track-color="rgba(14,61,168,.10)"
        />
      </div>
    </t-card>

    <TaskResult :outcome="outcome" :logs="logs" />

    <div class="max-only">
      <HistoryCard ref="histCard" app-id="pcd_export" :format-result="fmtResult" show-dir />
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onActivated, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, pickDirectory, pickFile, runJob } from '../api';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { createPreviewScheduler } from '../lib/preview-scheduler.mjs';
import { pcdOutcome, formatHistorySummary, friendlyTaskError } from '../lib/task-result.mjs';
import { restoreState } from '../lib/restore-bus.mjs';
import { restorePcdParams } from '../lib/restore-params.mjs';

const codes = ref([]);
const batchName = ref('派车单');
const fileName = ref('');
const codesText = ref('');
const outdir = ref('');
const KEEP_AWAKE_KEY = 'pcd.keep_awake.v1';
const opt = reactive({
  batchSubdir: true,
  pcd: true,
  track: true,
  calibrateNoon: false,
  keepAwake: window.localStorage.getItem(KEEP_AWAKE_KEY) === '1',
});
watch(
  () => opt.keepAwake,
  (value) => {
    try {
      window.localStorage.setItem(KEEP_AWAKE_KEY, value ? '1' : '0');
    } catch (_e) {
      /* 本地存储不可用时不影响导出 */
    }
  }
);
const running = ref(false);
const logs = ref([]);
const progress = reactive({ pct: 0, text: '' });
const outcome = ref({ status: '尚未开始', summary: '', failed: false });
const parsing = ref(false);
const previewSource = ref('');
const previewEmpty = ref('');
const histCard = ref(null);

let currentJob = null;
const clusterMode = ref(false);
const clusterEnabled = ref(false);
const clusterNodes = ref([]);
const clusterSel = reactive({});
const clusterJobs = reactive({});
const clusterJobId = ref('');
let clusterTimer = null;

const clusterReadyCount = computed(
  () => clusterNodes.value.filter((node) => nodeReady(node)).length
);
const clusterSelectedCount = computed(
  () => clusterNodes.value.filter((node) => clusterSel[node.node_id] && nodeReady(node)).length
);
const clusterCols = [
  { colKey: 'selected', title: '', width: 48 },
  { colKey: 'name', title: '节点', width: 190 },
  { colKey: 'status', title: '状态', width: 190 },
  { colKey: 'query', title: '查询并发', width: 110 },
  { colKey: 'render', title: '渲染并发', width: 110 },
];

function nodeReady(node) {
  if (!node || !node.online) return false;
  if (node.export_ready && !node.export_ready.ready) return false;
  return true;
}

function nodeStatusText(node) {
  if (!node.online) return '离线';
  if (node.export_ready && !node.export_ready.ready) {
    return node.export_ready.reason || '未就绪';
  }
  return '就绪';
}

function jobOf(node) {
  if (!clusterJobs[node.node_id]) {
    const ready = node.export_ready || {};
    clusterJobs[node.node_id] = {
      query: Number(ready.query_workers) || 8,
      render: Number(ready.render_workers) || 4,
    };
  }
  return clusterJobs[node.node_id];
}

async function loadCluster() {
  try {
    const st = (await call('getClusterStatus')) || {};
    clusterEnabled.value = !!(st.service && st.service.running);
    const map = new Map();
    for (const node of st.nodes || []) {
      if (node && node.node_id) map.set(node.node_id, node);
    }
    if (st.identity && st.identity.node_id && !map.has(st.identity.node_id)) {
      map.set(st.identity.node_id, st.identity);
    }
    clusterNodes.value = Array.from(map.values());
    for (const node of clusterNodes.value) {
      if (clusterSel[node.node_id] === undefined) {
        clusterSel[node.node_id] = nodeReady(node);
      }
      jobOf(node);
    }
  } catch (_e) {
    clusterEnabled.value = false;
    clusterNodes.value = [];
  }
}

function startClusterPoll() {
  stopClusterPoll();
  clusterTimer = setInterval(pollCluster, 1000);
  pollCluster();
}

function stopClusterPoll() {
  if (clusterTimer) {
    clearInterval(clusterTimer);
    clusterTimer = null;
  }
}

async function pollCluster() {
  if (!clusterJobId.value) return;
  try {
    const st = (await call('clusterGetExport', { job_id: clusterJobId.value })) || {};
    if (Array.isArray(st.logs)) logs.value = st.logs;
    const job = st.job || {};
    const p = job.progress || {};
    progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
    progress.text = `集群导出 ${p.current || 0}/${p.total || 0}`;
    if (['success', 'failed', 'cancelled'].includes(job.status)) {
      stopClusterPoll();
      running.value = false;
      clusterJobId.value = '';
      outcome.value = pcdOutcome(job);
      if (histCard.value) histCard.value.reload();
    }
  } catch (_e) {
    /* 轮询失败不结束任务；服务重启后管理员可重跑 */
  }
}

async function startClusterRun() {
  const nodes = clusterNodes.value
    .filter((node) => clusterSel[node.node_id] && nodeReady(node))
    .map((node) => {
      const item = jobOf(node);
      return {
        node_id: node.node_id,
        query_workers: Number(item.query) || 8,
        render_workers: Number(item.render) || 4,
      };
    });
  if (!nodes.length) {
    MessagePlugin.warning('请至少勾选一个就绪节点');
    return;
  }
  logs.value = [];
  outcome.value = {
    status: '执行中',
    summary: `集群导出：${nodes.length} 个节点参与`,
    failed: false,
  };
  progress.pct = 0;
  progress.text = '';
  running.value = true;
  try {
    const result = await call('clusterStartExport', {
      params: {
        codes: codes.value,
        outdir: outdir.value.trim(),
        batch_name: batchName.value,
        batch_subdir: opt.batchSubdir,
        export_pcd: opt.pcd,
        export_track: opt.track,
        calibrate_noon: opt.calibrateNoon,
        keep_awake: !!opt.keepAwake,
        nodes,
      },
    });
    clusterJobId.value = result.job_id;
    startClusterPoll();
  } catch (e) {
    running.value = false;
    const message = String(e.message || e);
    logs.value.push(message);
    outcome.value = {
      status: '启动失败',
      summary: friendlyTaskError(message),
      failed: true,
    };
  }
}

function baseName(path) {
  return (
    String(path || '')
      .split(/[\\/]/)
      .pop() || ''
  );
}

function appendLog(line) {
  logs.value.push(line);
  if (logs.value.length > 1500) logs.value.splice(0, logs.value.length - 1500);
  nextTick(() => {
    const el = document.querySelector('.pcd-page .task-result .logview');
    if (el) el.scrollTop = el.scrollHeight;
  });
}

const scheduler = createPreviewScheduler({
  delay: 400,
  request: (params) => call('preview', { app_id: 'pcd_export', params }),
  onState: (state) => {
    parsing.value = state.parsing;
  },
  onPreview: (result, meta) => {
    codes.value = result.codes || [];
    previewEmpty.value = '';
    if (meta.source === 'file') {
      batchName.value = meta.batchName || '派车单';
      previewSource.value = fileName.value;
    } else {
      batchName.value = '粘贴文本';
      previewSource.value = '粘贴文本';
    }
  },
  onEmpty: (meta) => {
    codes.value = [];
    previewSource.value = meta.source === 'file' ? fileName.value : '粘贴文本';
    previewEmpty.value =
      '未解析到有效单号，请检查表头是否为“派车单号 / 用车日期 / 记录ID”，或粘贴内容是否包含单号。';
    MessagePlugin.warning('未解析到有效单号');
  },
  onError: (err) => {
    codes.value = [];
    previewEmpty.value = '解析失败：' + String(err.message || err);
    MessagePlugin.error('解析失败：' + String(err.message || err));
  },
});

function onPasteInput(event) {
  codesText.value = event.target.value;
  codes.value = [];
  previewSource.value = '';
  previewEmpty.value = codesText.value.trim() ? '正在解析…' : '';
  scheduler.setText(codesText.value);
}

function parseNow() {
  if (!scheduler.parseNow()) {
    MessagePlugin.warning('请先粘贴派车单号');
  }
}

async function pickXlsx() {
  const path = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }]);
  if (!path) return;
  fileName.value = baseName(path);
  codesText.value = '';
  codes.value = [];
  previewEmpty.value = '正在解析…';
  previewSource.value = fileName.value;
  scheduler.cancel();
  scheduler.previewFile(
    { xlsx_path: path },
    { source: 'file', batchName: fileName.value.replace(/\.xlsx$/i, '') }
  );
}

async function browseOutdir() {
  const dir = await pickDirectory(outdir.value || undefined);
  if (dir) outdir.value = dir;
}

function fmtResult(result) {
  return formatHistorySummary('pcd_export', result);
}

async function startRun() {
  if (parsing.value || !codes.value.length) return;
  if (!outdir.value.trim()) {
    MessagePlugin.warning('请填写输出目录');
    return;
  }
  if (!opt.pcd && !opt.track) {
    MessagePlugin.warning('派车单 / 轨迹至少勾选一项');
    return;
  }
  if (clusterMode.value) {
    await startClusterRun();
    return;
  }
  try {
    const cfg = await call('getConfig');
    if (!(cfg.sso || {}).has_password && !(cfg.dev || {}).mock) {
      MessagePlugin.warning('尚未配置 SSO 账号密码——请先到「设置」页填写（或开启开发模式试用）');
      return;
    }
  } catch (_e) {
    /* 预检失败照常下发 */
  }
  logs.value = [];
  outcome.value = { status: '执行中', summary: '正在导出派车单与轨迹…', failed: false };
  progress.pct = 0;
  progress.text = '';
  running.value = true;
  try {
    currentJob = runJob(
      'pcd_export',
      {
        codes: codes.value,
        outdir: outdir.value.trim(),
        batch_name: batchName.value,
        batch_subdir: opt.batchSubdir,
        export_pcd: opt.pcd,
        export_track: opt.track,
        calibrate_noon: opt.calibrateNoon,
        keep_awake: !!opt.keepAwake,
      },
      {
        onLog: appendLog,
        onProgress: (p) => {
          progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          progress.text = `${p.label || ''} ${p.current}/${p.total}`;
        },
        onDone: (job) => {
          running.value = false;
          outcome.value = pcdOutcome(job);
          histCard.value && histCard.value.reload();
        },
        onError: (err) => {
          running.value = false;
          const message = String(err.message || err);
          logs.value.push(message);
          outcome.value = {
            status: '启动失败',
            summary: friendlyTaskError(message),
            failed: true,
          };
        },
      }
    );
  } catch (e) {
    running.value = false;
    const message = String(e.message || e);
    logs.value.push(message);
    outcome.value = { status: '启动失败', summary: friendlyTaskError(message), failed: true };
  }
}

function cancel() {
  if (clusterJobId.value) {
    const jobId = clusterJobId.value;
    stopClusterPoll();
    running.value = false;
    clusterJobId.value = '';
    outcome.value = {
      status: '已取消',
      summary: '已请求取消集群导出；各节点会结束当前分片',
      failed: false,
    };
    call('clusterCancelExport', { job_id: jobId }).catch((e) => {
      MessagePlugin.error(String(e.message || e));
    });
    return;
  }
  if (currentJob) currentJob.cancel();
}

function applyRestore(params) {
  const restored = restorePcdParams(params || {});
  outdir.value = restored.outdir;
  batchName.value = restored.batchName;
  Object.assign(opt, restored.options);
  if (restored.codes.length) {
    codes.value = restored.codes;
    previewSource.value = '历史参数';
    previewEmpty.value = '';
  } else if (restored.codesText.trim()) {
    codesText.value = restored.codesText;
    scheduler.setText(codesText.value);
  } else if (restored.xlsxPath) {
    fileName.value = baseName(restored.xlsxPath);
    scheduler.previewFile(
      { xlsx_path: restored.xlsxPath },
      { source: 'file', batchName: fileName.value.replace(/\.xlsx$/i, '') }
    );
  }
  outcome.value = {
    status: '待执行',
    summary: restored.codes.length
      ? `已回填 ${restored.codes.length} 个单号，确认参数后点击开始导出。`
      : '参数已恢复，尚未再次执行。',
    failed: false,
  };
}

watch(
  () => restoreState.nonce,
  () => {
    if (restoreState.appID !== 'pcd_export') return;
    applyRestore(restoreState.params);
  },
  { immediate: true }
);

watch(clusterMode, (enabled) => {
  if (enabled && !clusterNodes.value.length) loadCluster();
});

onMounted(async () => {
  loadCluster();
  try {
    const info = await call('getSystemInfo', { with_outdirs: true });
    const dirs = info.suggested_outdirs || [];
    if (dirs.length && !outdir.value) outdir.value = dirs[0];
  } catch (_e) {
    /* 忽略 */
  }
});

onUnmounted(stopClusterPoll);

onActivated(() => {
  loadCluster();
  if (!outdir.value) {
    call('getSystemInfo', { with_outdirs: true })
      .then((info) => {
        const dirs = info.suggested_outdirs || [];
        if (dirs.length && !outdir.value) outdir.value = dirs[0];
      })
      .catch(() => {});
  }
});
</script>

<style scoped>
.paste-box {
  display: block;
  width: 100%;
  box-sizing: border-box;
  margin-top: 6px;
  padding: 9px 11px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  font: inherit;
  font-size: 13px;
  line-height: 1.6;
  resize: vertical;
  outline: none;
}
.paste-box:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px rgba(14, 61, 168, 0.12);
}
.cluster-box {
  margin-top: 10px;
}
.preview-count {
  color: var(--tk-brand);
  font-size: 12px;
  font-weight: 600;
}
.preview-empty {
  margin-top: 8px;
  padding: 9px 11px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  color: var(--tk-yellow);
  font-size: 12.5px;
}
</style>
