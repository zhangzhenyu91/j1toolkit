<template>
  <div class="defect-page">
    <div class="instance-bar">
      <button
        v-for="item in instances"
        :key="item.id"
        class="instance-chip"
        :class="{ active: item.id === activeId, running: runtimes[item.id]?.running }"
        @click="switchInstance(item.id)"
      >
        {{ item.name }}
      </button>
      <t-button size="small" variant="outline" @click="addInstance"> + 新建实例 </t-button>
      <t-button
        size="small"
        variant="outline"
        :disabled="running || starting"
        @click="resetInstanceTask"
      >
        重置任务
      </t-button>
      <t-button
        size="small"
        theme="danger"
        variant="outline"
        :disabled="instances.length <= 1 || running || starting"
        @click="removeInstance(activeId)"
      >
        删除当前实例
      </t-button>
    </div>
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">审核浏览器</div>
          <div class="row" style="margin-bottom: 8px">
            <span class="muted">实例名称</span>
            <t-input v-model="form.instanceName" class="grow" placeholder="例如 胜西线 1" />
          </div>
          <div class="status-line">
            <span class="status-dot" :class="ready ? 'success' : 'warning'" />
            {{ ready ? '浏览器已就绪，可开始审核' : '请先打开浏览器并登录系统' }}
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted">启动网址</span>
            <t-input
              v-model="form.startUrl"
              class="grow"
              placeholder="可留空进入空白页，例如 http://…/IdentifyTasks"
            />
            <t-button variant="outline" :loading="opening" :disabled="running" @click="openBrowser">
              打开浏览器
            </t-button>
          </div>
          <p class="muted" style="margin: 8px 0 0">
            打开后请人工登录，进入「智能分析 → 智能识别 →
            缺陷识别任务」列表并保持该浏览器在前台，再点开始审核。
          </p>
        </t-card>

        <t-card>
          <div class="sec-title">审核参数</div>
          <div class="form-grid">
            <div class="row">
              <span class="muted">任务状态</span>
              <t-select v-model="form.taskStatus" :options="statusOptions" class="grow" />
            </div>
            <div class="row">
              <span class="muted">作业性质</span>
              <t-select v-model="form.missionType" :options="missionOptions" class="grow" />
            </div>
            <div class="row">
              <span class="muted">电压等级</span>
              <t-select v-model="form.voltageLevel" :options="voltageOptions" class="grow" />
            </div>
            <div class="row">
              <span class="muted">线路名称</span>
              <t-input v-model="form.lineName" class="grow" placeholder="留空=全部线路" />
            </div>
            <div class="row">
              <span class="muted">开始日期</span>
              <t-input v-model="form.startDate" class="grow" placeholder="YYYY-MM-DD" />
            </div>
            <div class="row">
              <span class="muted">结束日期</span>
              <t-input v-model="form.endDate" class="grow" placeholder="YYYY-MM-DD" />
            </div>
          </div>
          <div class="form-row-fixed" style="margin-top: 10px">
            <span class="muted">输出目录</span>
            <t-input v-model="form.outdir" class="grow" placeholder="留空自动生成运行目录" />
            <t-button variant="outline" @click="pickOutdir">选择目录</t-button>
          </div>
          <div class="form-row-fixed" style="margin-top: 8px">
            <span class="muted">任务跳过表</span>
            <t-input
              v-model="form.skipTasksFile"
              class="grow"
              placeholder="一列 xlsx，首行标题，其余为任务名称；留空使用默认"
            />
            <t-button variant="outline" @click="pickSkipFile">选择文件</t-button>
          </div>
          <t-checkbox v-model="form.ignoreDefectBoxes" style="margin-top: 12px">
            无视缺陷框：不再判定是否有缺陷框，所有能加载的图片都点审核完成
          </t-checkbox>
        </t-card>

        <CollapsibleCard v-model:open="advancedOpen" title="高级设置（异常时调整）">
          <div class="row">
            <span class="muted">单张图片等待</span>
            <t-input-number v-model="form.imageTimeout" :min="1" :max="300" style="width: 120px" />
            <span class="muted">秒</span>
          </div>
          <div class="row" style="margin-top: 8px">
            <span class="muted">加载失败立即重试</span>
            <t-input-number v-model="form.imageRetries" :min="0" :max="10" style="width: 120px" />
            <span class="muted">次</span>
          </div>
          <div class="row" style="margin-top: 8px">
            <span class="muted">未审核数阈值</span>
            <t-input-number
              v-model="form.maxUnreviewed"
              :min="1"
              :max="100000"
              style="width: 120px"
            />
            <span class="muted">张，超过则跳过任务并记异常</span>
          </div>
        </CollapsibleCard>
      </div>

      <div class="page-split-col">
        <t-card>
          <div class="sec-title">开始审核</div>
          <div class="row">
            <t-button
              class="defect-start-btn"
              theme="primary"
              :loading="starting"
              :disabled="!canStart"
              @click="startRun"
            >
              开始审核
            </t-button>
            <t-button
              v-if="running && !ending"
              theme="danger"
              variant="outline"
              :loading="stopping"
              :disabled="stopping"
              @click="stopRun"
            >
              停止审核
            </t-button>
            <span v-if="stopping || ending" class="muted">正在停止</span>
          </div>
          <div class="row" style="margin-top: 8px">
            <t-switch v-model="form.keepAwake" size="large" :disabled="running || starting" />
            <span class="muted">运行时防止关屏/休眠</span>
          </div>
          <div class="defect-stage">{{ stageText }}</div>
          <t-progress
            theme="plump"
            :percentage="progress.pct"
            :label="progress.text"
            color="#0E3DA8"
            track-color="rgba(14,61,168,.10)"
          />
        </t-card>

        <TaskResult :outcome="outcome" :logs="logs" />

        <HistoryCard ref="histCard" app-id="defect_review" :format-result="fmtResult" show-dir />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, markRaw, onActivated, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, pickDirectory, pickFile, runJob } from '../api';
import CollapsibleCard from '../components/CollapsibleCard.vue';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { friendlyTaskError } from '../lib/task-result.mjs';
import { makeInstance } from '../lib/instances.mjs';

const statusOptions = [
  '待人工审核',
  '审核完成',
  '审核驳回',
  '任务完成',
  '任务已终止',
  '工单终止',
].map((value) => ({ label: value, value }));
const missionOptions = [
  { label: '全部', value: '' },
  '仿线巡检',
  '精细化巡检',
  '线路交跨巡检',
  '应急巡检',
  '正射巡检',
  '红外巡检',
  '固定翼巡检',
  '通道巡检',
  '树障巡检',
  '特殊巡视',
  '联合巡视',
].map((value) => (typeof value === 'string' ? { label: value, value } : value));
const voltageOptions = [
  { label: '全部', value: '' },
  '交流110kV',
  '交流220kV',
  '交流35kV',
  '交流500kV',
  '交流1000kV',
  '±800kV地',
  '直流500kV',
  '直流660kV',
  '直流800kV',
  '直流1000kV',
].map((value) => (typeof value === 'string' ? { label: value, value } : value));

const form = reactive({
  instanceName: '',
  startUrl: '',
  taskStatus: '待人工审核',
  missionType: '',
  voltageLevel: '',
  lineName: '',
  startDate: '',
  endDate: '',
  outdir: '',
  skipTasksFile: '',
  ignoreDefectBoxes: false,
  keepAwake: false,
  imageTimeout: 10,
  imageRetries: 0,
  maxUnreviewed: 400,
});
const st = reactive({ connected: false, playwright: false, pages: 0 });
const instances = ref([]);
const activeId = ref('');
const opening = ref(false);
const advancedOpen = ref(false);
const histCard = ref(null);
const runtimes = reactive({});
const activeRuntime = computed(() => runtimeFor(activeId.value));
const logs = computed(() => activeRuntime.value.logs);
const progress = computed(() => activeRuntime.value.progress);
const outcome = computed(() => activeRuntime.value.outcome);
const running = computed(() => activeRuntime.value.running);
const starting = computed(() => activeRuntime.value.starting);
const stopping = computed(() => activeRuntime.value.stopping);
const stageText = computed(() => '阶段：' + activeRuntime.value.stage);

function runtimeFor(id) {
  const key = id || 'default';
  if (!runtimes[key]) {
    runtimes[key] = {
      logs: [],
      progress: { pct: 0, text: '' },
      outcome: { status: '尚未开始', summary: '', failed: false },
      stage: '未开始',
      running: false,
      starting: false,
      stopping: false,
      ending: false,
      job: null,
    };
  }
  return runtimes[key];
}

const ready = computed(() => st.connected && st.playwright);
const canStart = computed(
  () =>
    ready.value &&
    !running.value &&
    !starting.value &&
    !stopping.value &&
    !opening.value &&
    !!form.startDate.trim() &&
    !!form.endDate.trim()
);

function countOf(v) {
  if (Array.isArray(v)) return v.length;
  return typeof v === 'number' ? v : 0;
}

function payload() {
  return {
    start_url: form.startUrl.trim(),
    task_status: form.taskStatus,
    mission_type: form.missionType,
    voltage_level: form.voltageLevel,
    line_name: form.lineName.trim(),
    start_date: form.startDate.trim(),
    end_date: form.endDate.trim(),
    outdir: form.outdir.trim(),
    skip_tasks_file: form.skipTasksFile.trim(),
    ignore_defect_boxes: !!form.ignoreDefectBoxes,
    keep_awake: !!form.keepAwake,
    image_timeout: Number(form.imageTimeout) || 10,
    image_retries: Number(form.imageRetries) || 0,
    max_unreviewed: Number(form.maxUnreviewed) || 400,
  };
}

function applyInstance(instance) {
  const data = instance || {};
  const params = data.params || {};
  form.instanceName = data.name || '实例 1';
  form.startUrl = data.start_url || params.start_url || '';
  form.taskStatus = params.task_status || '待人工审核';
  form.missionType = params.mission_type ?? '';
  form.voltageLevel = params.voltage_level ?? '';
  form.lineName = params.line_name || '';
  form.startDate = params.start_date || '';
  form.endDate = params.end_date || '';
  form.outdir = params.outdir || '';
  form.skipTasksFile = params.skip_tasks_file || '';
  form.ignoreDefectBoxes = !!params.ignore_defect_boxes;
  form.keepAwake = !!params.keep_awake;
  form.imageTimeout = Number(params.image_timeout) || 10;
  form.imageRetries = Number(params.image_retries) || 0;
  form.maxUnreviewed = Number(params.max_unreviewed) || 400;
}

function captureInstance() {
  const index = instances.value.findIndex((item) => item.id === activeId.value);
  if (index < 0) return null;
  const data = payload();
  const updated = {
    ...instances.value[index],
    name: form.instanceName.trim() || instances.value[index].name,
    start_url: data.start_url,
    params: data,
  };
  instances.value[index] = updated;
  return updated;
}

function activeInstance() {
  return instances.value.find((item) => item.id === activeId.value) || null;
}

function applyConfig(raw) {
  const app = raw || {};
  const list = Array.isArray(app.instances) ? app.instances : [];
  if (list.length) {
    instances.value = list;
  } else {
    instances.value = [
      {
        id: 'default',
        name: '实例 1',
        start_url: app.start_url || '',
        cdp_url: 'http://127.0.0.1:9400',
        profile_dir: '',
        params: app,
      },
    ];
  }
  activeId.value = instances.value[0].id;
  applyInstance(instances.value[0]);
}

async function loadStatus() {
  try {
    const instance = activeInstance();
    const s =
      (await call('getAppStatus', {
        app_id: 'defect_review',
        instance_id: activeId.value,
        instance,
      })) || {};
    st.connected = !!s.connected;
    st.playwright = !!s.playwright;
    st.pages = countOf(s.pages);
    if (s.start_url && !form.startUrl) form.startUrl = s.start_url;
  } catch (_e) {
    /* 状态读取失败不阻断表单 */
  }
}

async function loadConfig() {
  try {
    const cfg = await call('getConfig');
    applyConfig((cfg || {}).defect_review || {});
  } catch (_e) {
    /* ignore */
  }
}

async function saveConfig() {
  captureInstance();
  const data = payload();
  await call('saveConfig', {
    defect_review: {
      ...data,
      instances: instances.value,
    },
  });
}

function switchInstance(id) {
  if (id === activeId.value) return;
  captureInstance();
  activeId.value = id;
  applyInstance(activeInstance());
  saveConfig().catch(() => {});
  loadStatus();
}

async function addInstance() {
  captureInstance();
  const params = payload();
  const instance = makeInstance('defect_review', instances.value, undefined, 9400);
  instance.start_url = form.startUrl;
  instance.params = { ...params, start_url: form.startUrl };
  instances.value.push(instance);
  activeId.value = instance.id;
  applyInstance(instance);
  try {
    await saveConfig();
    await loadStatus();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function openBrowser() {
  opening.value = true;
  try {
    await saveConfig();
    const instance = activeInstance();
    await call('launchAppBrowser', {
      app_id: 'defect_review',
      instance_id: activeId.value,
      instance,
    });
    await loadStatus();
    MessagePlugin.success('请登录系统并进入缺陷识别任务列表，再点开始审核');
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    opening.value = false;
  }
}

async function pickOutdir() {
  const path = await pickDirectory(form.outdir || undefined);
  if (path) form.outdir = path;
}

async function pickSkipFile() {
  const path = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }]);
  if (path) form.skipTasksFile = path;
}

function appendLog(rt, line) {
  rt.logs.push(line);
  if (rt.logs.length > 2000) rt.logs.splice(0, rt.logs.length - 2000);
}

function fmtResult(r) {
  if (!r) return '';
  if (r.cancelled) return '已停止';
  return `完成任务 ${r.completed ?? 0}；审核图片 ${r.images ?? 0}；转人工 ${r.deferred ?? 0}`;
}

function outcomeFromResult(job) {
  const r = job.result || {};
  if (r.cancelled) {
    return {
      status: '已停止',
      summary: `已停止，产物目录：${r.dir || r.outdir || ''}`,
      failed: false,
    };
  }
  const status = job.status === 'success' ? '完成' : job.status === 'cancelled' ? '已停止' : '失败';
  return {
    status,
    summary:
      `${fmtResult(r)}；输出目录：${r.outdir || r.dir || ''}` +
      (r.button_not_ready_images ? `；按钮未就绪 ${r.button_not_ready_images}` : ''),
    failed: job.status === 'failed',
    artifact: r.outdir || r.dir ? { path: r.outdir || r.dir, label: '打开输出目录' } : null,
  };
}

async function startRun() {
  const id = activeId.value;
  const rt = runtimeFor(id);
  if (rt.starting || rt.running || rt.stopping) return;
  rt.starting = true;
  try {
    await saveConfig();
    const instance = instances.value.find((item) => item.id === id) || activeInstance();
    const previous = rt.job;
    if (previous) await previous.finished;
    rt.logs = [];
    rt.outcome = { status: '执行中', summary: '正在审核…', failed: false };
    rt.stage = '准备启动';
    rt.running = true;
    rt.stopping = false;
    rt.ending = false;
    rt.progress.pct = 0;
    rt.progress.text = '';
    let job;
    job = runJob(
      'defect_review',
      {
        instance_id: id,
        instance_name: instance?.name || form.instanceName,
        instance,
        task_status: form.taskStatus,
        mission_type: form.missionType,
        voltage_level: form.voltageLevel,
        line_name: form.lineName.trim(),
        start_date: form.startDate.trim(),
        end_date: form.endDate.trim(),
        outdir: form.outdir.trim(),
        skip_tasks_file: form.skipTasksFile.trim(),
        ignore_defect_boxes: !!form.ignoreDefectBoxes,
        keep_awake: !!form.keepAwake,
        image_timeout: Number(form.imageTimeout) || 10,
        image_retries: Number(form.imageRetries) || 0,
        max_unreviewed: Number(form.maxUnreviewed) || 400,
      },
      {
        onLog: (line) => appendLog(rt, line),
        onProgress: (p) => {
          rt.progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          rt.progress.text =
            p.total > 0 ? `${p.label || ''} ${p.current}/${p.total}` : p.label || '';
          if (!rt.stopping) {
            rt.stage = `${p.label || '处理中'}（${p.current}/${p.total}）`;
          }
        },
        onDone: (doneJob) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.stopping = false;
          rt.ending = false;
          loadStatus();
          histCard.value && histCard.value.reload();
          rt.outcome = outcomeFromResult(doneJob);
          rt.stage = rt.outcome.status;
        },
        onError: (e) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.stopping = false;
          rt.ending = false;
          const message = String(e.message || e);
          rt.logs.push(message);
          rt.outcome = {
            status: '启动失败',
            summary: message.includes('该实例已有任务在运行')
              ? '该实例后台仍有任务在退出，可点「重置任务」后再开始，无需重启客户端'
              : friendlyTaskError(message),
            failed: true,
          };
          rt.stage = '启动失败';
        },
      }
    );
    rt.job = markRaw(job);
  } catch (e) {
    rt.running = false;
    rt.stopping = false;
    const message = String(e.message || e);
    rt.logs.push(message);
    rt.outcome = {
      status: '启动失败',
      summary: friendlyTaskError(message),
      failed: true,
    };
    rt.stage = '启动失败';
  } finally {
    rt.starting = false;
  }
}

async function stopRun() {
  const rt = activeRuntime.value;
  const job = rt.job;
  if (!job || rt.stopping) return;
  // running 保持到 job.done：此前“开始审核”不能重新亮起，
  // 避免旧任务还在退出时又启动新任务。
  rt.stopping = true;
  rt.ending = false;
  rt.stage = '正在停止';
  try {
    await job.cancel();
    if (rt.job === job) {
      // 取消请求已送达，但后端可能还在结束线程；不要提前释放实例，
      // 否则立即点“开始”会撞上“该实例已有任务在运行”。
      rt.stopping = false;
      rt.ending = true;
      rt.stage = '正在停止（等待后台退出）';
    }
  } catch (e) {
    if (rt.job === job) {
      rt.stopping = false;
      rt.ending = false;
      rt.stage = '停止失败，任务仍在执行';
    }
    MessagePlugin.error(`停止失败：${String(e.message || e)}`);
  }
}

async function resetInstanceTask() {
  const id = activeId.value;
  const rt = runtimeFor(id);
  let active;
  try {
    active = await call('getActiveJob', {
      app_id: 'defect_review',
      instance_id: id,
    });
  } catch (e) {
    MessagePlugin.error(`读取实例任务失败：${String(e.message || e)}`);
    return;
  }
  if (!active || !active.id) {
    MessagePlugin.info('该实例当前没有后台任务');
    rt.job = null;
    rt.running = false;
    rt.stopping = false;
    rt.ending = false;
    rt.stage = '实例空闲';
    return;
  }
  const dialog = DialogPlugin.confirm({
    header: '重置实例任务',
    body: `将取消该实例的后台任务 ${active.id}（${active.status}），不影响其他实例。确定继续？`,
    theme: 'warning',
    confirmBtn: '重置',
    onConfirm: async () => {
      dialog.hide();
      rt.stage = '正在重置实例任务';
      try {
        await call('cancelJob', { job_id: active.id });
        for (let attempt = 0; attempt < 40; attempt += 1) {
          await new Promise((resolve) => setTimeout(resolve, 500));
          const job = await call('getJob', { job_id: active.id });
          if (!job || ['success', 'failed', 'cancelled'].includes(job.status)) {
            rt.job = null;
            rt.running = false;
            rt.stopping = false;
            rt.ending = false;
            rt.progress.pct = 0;
            rt.progress.text = '';
            rt.stage = '实例已重置';
            rt.outcome = {
              status: '已重置',
              summary: '已取消该实例后台残留任务，不影响其他实例',
              failed: false,
            };
            MessagePlugin.success('实例任务已重置');
            loadStatus();
            histCard.value && histCard.value.reload();
            return;
          }
        }
        MessagePlugin.warning('后台任务仍未退出，请稍后再次重置或检查该实例浏览器');
      } catch (e) {
        MessagePlugin.error(`重置任务失败：${String(e.message || e)}`);
      }
    },
  });
}

function removeInstance(id) {
  if (instances.value.length <= 1) {
    MessagePlugin.warning('至少保留一个实例');
    return;
  }
  const target = instances.value.find((item) => item.id === id);
  if (!target) return;
  const rt = runtimeFor(id);
  if (rt.running || rt.starting) {
    MessagePlugin.warning('实例正在运行，请先停止再删除');
    return;
  }
  const dialog = DialogPlugin.confirm({
    header: '删除实例',
    body: `确定删除「${target.name}」？不会关闭已经打开的浏览器，也不会删除登录目录。`,
    theme: 'warning',
    confirmBtn: '删除',
    onConfirm: async () => {
      dialog.hide();
      const index = instances.value.findIndex((item) => item.id === id);
      instances.value.splice(index, 1);
      delete runtimes[id];
      if (activeId.value === id) {
        const next = instances.value[Math.max(0, index - 1)] || instances.value[0];
        activeId.value = next.id;
        applyInstance(next);
      }
      await saveConfig();
      await loadStatus();
    },
  });
}

onMounted(async () => {
  await loadConfig();
  await loadStatus();
});
onActivated(loadStatus);
</script>

<style scoped>
.instance-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.instance-chip {
  border: 1px solid var(--tk-border, #e5e6eb);
  background: #fff;
  border-radius: 999px;
  box-sizing: border-box;
  height: 24px;
  padding: 0 12px;
  font-size: 12px;
  line-height: 22px;
  color: var(--tk-sub);
  cursor: pointer;
}
.instance-chip.running {
  box-shadow: inset 0 0 0 1px #00b42a;
}
.instance-chip.active {
  border-color: #0e3da8;
  background: #e8f0ff;
  color: #0e3da8;
  font-weight: 600;
}
.instance-chip:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.defect-start-btn {
  margin-right: 4px;
}
.defect-stage {
  margin: 8px 0;
  font-size: 13px;
  color: var(--tk-sub);
  font-variant-numeric: tabular-nums;
}
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 18px;
}
.form-grid .row {
  margin: 0;
}
.form-grid .row > .muted {
  flex: 0 0 80px;
  white-space: nowrap;
}
.form-row-fixed {
  display: grid;
  grid-template-columns: 80px minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
}
@media (max-width: 900px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
