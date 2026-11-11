<template>
  <div class="course-page">
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
      <t-button size="small" variant="outline" @click="addInstance">
        + 新建实例
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
          <div class="sec-title">课程浏览器</div>
          <div class="row" style="margin-bottom: 8px">
            <span class="muted">实例名称</span>
            <t-input v-model="instanceName" class="grow" placeholder="例如 培训班 A" />
          </div>
          <div class="normal-only">
            <span class="status-line">
              <span class="status-dot" :class="compactTone" />
              {{ compactText }}
            </span>
          </div>
          <div class="max-only">
            <div class="kv">
              <span class="k">浏览器状态</span
              ><span>{{ st.connected ? '浏览器已就绪' : '浏览器未就绪' }}</span>
              <span class="k">学习状态</span
              ><span>{{ ready ? '可开始学习' : '暂不可开始学习' }}</span>
              <span class="k">已打开页面</span
              ><span>{{ st.connected ? st.pages + ' 个页面' : '-' }}</span>
              <span class="k">可继续学习</span><span>{{ st.active_lists }} 个培训班</span>
            </div>
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted">课程网址</span>
            <t-input v-model="startUrl" class="grow" placeholder="输入要打开的课程页面网址" />
            <t-button variant="outline" :loading="opening" :disabled="running" @click="openBrowser"
              >打开课程浏览器</t-button
            >
          </div>
          <p class="muted" style="margin: 8px 0 0">
            打开后请登录学堂，进入课程设置列表，并保持课程浏览器在前台。
          </p>
        </t-card>

        <t-card>
          <div class="sec-title">学习选项</div>
          <t-checkbox v-model="opt.scanOnly">只查看课程清单，不进入课程</t-checkbox>
        </t-card>

        <CollapsibleCard v-model:open="advancedOpen" title="高级设置（异常时调整）">
          <div class="row">
            <span class="muted">每门课程最多重试</span>
            <t-input-number v-model="opt.maxAttempts" :min="1" :max="10" style="width: 110px" />
            <span class="muted">次</span>
          </div>
          <div class="row" style="margin-top: 8px">
            <span class="muted">页面无响应等待时间</span>
            <t-input-number
              v-model="opt.timeout"
              :min="10"
              :max="600"
              :step="10"
              style="width: 110px"
            />
            <span class="muted">秒</span>
          </div>
        </CollapsibleCard>
      </div>

      <div class="page-split-col">
        <t-card>
          <div class="sec-title">开始学习</div>
          <div class="row">
            <t-button
              class="course-start-btn"
              theme="primary"
              :loading="starting"
              :disabled="!canStart"
              @click="startRun"
              >开始学习</t-button
            >
            <t-button v-if="running" theme="danger" variant="outline" @click="pause"
              >暂停学习</t-button
            >
            <span v-if="pausing" class="muted">正在暂停</span>
          </div>
          <div class="row" style="margin-top: 8px">
            <t-switch v-model="opt.keepAwake" size="large" :disabled="running || starting" />
            <span class="muted">运行时防止关屏/休眠</span>
          </div>
          <div class="course-stage">{{ stageText }}</div>
          <p v-if="!ready" class="muted" style="margin: 6px 0 0">{{ startHintText }}</p>
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

        <t-card class="max-only">
          <div class="sec-title">学习进度</div>
          <div class="muted">暂无学习进度；开始学习后会按培训班和课程列表保存。</div>
        </t-card>

        <div class="max-only">
          <HistoryCard
            ref="histCard"
            app-id="course_autoplay"
            :format-result="fmtResult"
            show-dir
          />
        </div>

        <div class="max-only">
          <CollapsibleCard v-model:open="techOpen" title="技术详情（供排错）">
            <div class="kv">
              <span class="k">连接状态</span><span>{{ st.connected ? '已连接' : '未连接' }}</span>
              <span class="k">连接地址</span><span>{{ st.cdp_url || '未启动' }}</span>
              <span class="k">浏览器</span><span>{{ st.browser || '-' }}</span>
              <span class="k">自动操作</span><span>{{ st.playwright ? '可用' : '不可用' }}</span>
            </div>
          </CollapsibleCard>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onActivated, onMounted, reactive, ref, watch } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, runJob } from '../api';
import CollapsibleCard from '../components/CollapsibleCard.vue';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { courseOutcome, friendlyTaskError } from '../lib/task-result.mjs';
import { restoreState } from '../lib/restore-bus.mjs';
import { restoreCourseParams } from '../lib/restore-params.mjs';
import { makeInstance } from '../lib/instances.mjs';
import { waitFinished } from '../lib/wait-finished.mjs';

const st = reactive({
  cdp_url: '',
  browser: '',
  connected: false,
  pages: 0,
  playwright: false,
  active_lists: 0,
});
const opt = reactive({ scanOnly: false, maxAttempts: 3, timeout: 120, keepAwake: false });
const startUrl = ref('');
const instanceName = ref('');
const instances = ref([]);
const activeId = ref('');
const opening = ref(false);
const histCard = ref(null);
const advancedOpen = ref(false);
const techOpen = ref(false);
const runtimes = reactive({});
const activeRuntime = computed(() => runtimeFor(activeId.value));
const running = computed(() => activeRuntime.value.running);
const starting = computed(() => activeRuntime.value.starting);
const pausing = computed(() => activeRuntime.value.pausing);
const logs = computed(() => activeRuntime.value.logs);
const progress = computed(() => activeRuntime.value.progress);
const outcome = computed(() => activeRuntime.value.outcome);
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
      pausing: false,
      job: null,
    };
  }
  return runtimes[key];
}

const ready = computed(() => st.connected && st.playwright);
const compactTone = computed(() => (ready.value ? 'success' : 'warning'));
const compactText = computed(() => {
  if (ready.value) {
    return `浏览器已就绪 · 可开始学习 · ${st.pages} 个页面 · 可继续 ${st.active_lists} 个培训班`;
  }
  if (st.connected) return '浏览器已连接，但自动操作不可用，请重新打开课程浏览器';
  return '浏览器未就绪 · 请先打开课程浏览器';
});
const startHintText = computed(() => {
  if (ready.value) return '';
  if (st.connected) return '浏览器已连接，但自动操作不可用，请重新打开课程浏览器';
  return '请先打开课程浏览器';
});
const canStart = computed(() => ready.value && !running.value && !starting.value && !opening.value);

function countOf(v) {
  if (Array.isArray(v)) return v.length;
  return typeof v === 'number' ? v : 0;
}

function applyStatus(s, keepStart = false) {
  st.cdp_url = s.cdp_url || '';
  st.browser = s.browser || '';
  st.connected = !!s.connected;
  st.pages = countOf(s.pages);
  st.playwright = !!s.playwright;
  st.active_lists = countOf(s.active_lists);
  if (!keepStart && !startUrl.value) startUrl.value = s.start_url || '';
}

function payload() {
  return {
    start_url: startUrl.value.trim(),
    scan_only: !!opt.scanOnly,
    max_attempts: Number(opt.maxAttempts) || 3,
    timeout: Number(opt.timeout) || 120,
    keep_awake: !!opt.keepAwake,
  };
}

function applyInstance(instance) {
  const data = instance || {};
  const params = data.params || {};
  instanceName.value = data.name || '实例 1';
  startUrl.value = data.start_url || params.start_url || '';
  opt.scanOnly = !!params.scan_only;
  opt.maxAttempts = Number(params.max_attempts) || 3;
  opt.timeout = Number(params.timeout) || 120;
  opt.keepAwake = !!params.keep_awake;
}

function captureInstance() {
  const index = instances.value.findIndex((item) => item.id === activeId.value);
  if (index < 0) return null;
  const data = payload();
  const updated = {
    ...instances.value[index],
    name: instanceName.value.trim() || instances.value[index].name,
    start_url: data.start_url,
    params: data,
  };
  instances.value[index] = updated;
  return updated;
}

function activeInstance() {
  return instances.value.find((item) => item.id === activeId.value) || null;
}

async function loadConfig() {
  try {
    const cfg = await call('getConfig');
    const app = (cfg || {}).course_autoplay || {};
    const list = Array.isArray(app.instances) ? app.instances : [];
    if (list.length) {
      instances.value = list;
    } else {
      instances.value = [
        {
          id: 'default',
          name: '实例 1',
          start_url: (cfg.browser_automation || {}).start_url || '',
          cdp_url: '',
          profile_dir: '',
          params: {
            max_attempts: app.max_attempts || 3,
            scan_only: false,
            timeout: 120,
            start_url: (cfg.browser_automation || {}).start_url || '',
          },
        },
      ];
    }
    activeId.value = instances.value[0].id;
    applyInstance(instances.value[0]);
  } catch (_e) {
    /* ignore */
  }
}

async function saveConfig() {
  captureInstance();
  await call('saveConfig', {
    course_autoplay: {
      max_attempts: Number(opt.maxAttempts) || 3,
      instances: instances.value,
    },
  });
}

async function loadStatus() {
  try {
    const instance = activeInstance();
    applyStatus(
      (await call('getAppStatus', {
        app_id: 'course_autoplay',
        instance_id: activeId.value,
        instance,
      })) || {},
      true
    );
  } catch (_e) {
    /* 状态读取失败不打断页面 */
  }
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
  const data = payload();
  const instance = makeInstance('course_autoplay', instances.value, undefined, 9300);
  instance.start_url = startUrl.value;
  instance.params = { ...data, start_url: startUrl.value };
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
    await call('launchAppBrowser', {
      app_id: 'course_autoplay',
      instance_id: activeId.value,
      instance: activeInstance(),
    });
    await loadStatus();
    MessagePlugin.success('请在该浏览器登录、打开课程设置列表并置前台');
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    opening.value = false;
  }
}

function appendLog(rt, line) {
  rt.logs.push(line);
  if (rt.logs.length > 1500) rt.logs.splice(0, rt.logs.length - 1500);
}

function countText(r) {
  return `总数 ${r.total ?? 0}；完成 ${r.completed ?? 0}；未完成 ${r.pending ?? 0}；跳过 ${r.skipped ?? 0}`;
}

function fmtResult(r) {
  if (!r) return '';
  if (r.paused) return `已处理 ${r.processed ?? 0} 门次；可续刷`;
  return countText(r);
}

async function startRun() {
  const id = activeId.value;
  const rt = runtimeFor(id);
  if (rt.starting || rt.running) return;
  rt.starting = true;
  try {
    await saveConfig();
    const instance = instances.value.find((item) => item.id === id) || activeInstance();
    const previousJob = rt.job;
    // 等上一个任务退出再启动；finished 可能因核心退出/事件丢失永不到达，超时后放弃等待
    if (previousJob && !(await waitFinished(previousJob, 5000))) {
      rt.logs.push('上一个任务未确认退出，已放弃等待并继续启动');
      if (rt.job === previousJob) rt.job = null;
    }

    const scanOnly = !!opt.scanOnly;
    rt.logs = [];
    rt.outcome = { status: '执行中', summary: '正在学习课程…', failed: false };
    rt.stage = '准备启动';
    rt.running = true;
    rt.pausing = false;
    rt.progress.pct = 0;
    rt.progress.text = '';
    let job;
    job = runJob(
      'course_autoplay',
      {
        instance_id: id,
        instance_name: instance?.name || instanceName.value,
        instance,
        scan_only: scanOnly,
        timeout: Number(opt.timeout) || 120,
        max_attempts: Number(opt.maxAttempts) || 3,
        keep_awake: !!opt.keepAwake,
      },
      {
        onLog: (line) => appendLog(rt, line),
        onProgress: (p) => {
          rt.progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          rt.progress.text =
            p.total > 0 ? `${p.label || ''} ${p.current}/${p.total}` : p.label || '';
          rt.stage = `${p.label || '处理中'}（${p.current}/${p.total}）`;
        },
        onDone: (doneJob) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.pausing = false;
          loadStatus();
          histCard.value && histCard.value.reload();
          const r = doneJob.result || {};
          rt.outcome = courseOutcome(doneJob, { scanOnly });
          rt.stage = r.paused
            ? '已暂停（可继续学习）'
            : doneJob.status === 'success'
              ? (r.pending ?? 0) > 0
                ? '本轮结束（仍有未完成）'
                : scanOnly
                  ? '扫描完成'
                  : '学习完成'
              : rt.outcome.status;
          if (doneJob.status === 'success' && !r.paused && (r.pending ?? 0) === 0 && !scanOnly) {
            DialogPlugin.confirm({
              header: '学习完成',
              body: `当前课程列表已全部达到 100%，共 ${r.total ?? 0} 门课程。`,
              confirmBtn: '知道了',
            });
          }
        },
        onError: (e) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.pausing = false;
          const message = String(e.message || e);
          rt.logs.push(message);
          rt.outcome = {
            status: '启动失败',
            summary: friendlyTaskError(message),
            failed: true,
          };
          rt.stage = '启动失败';
        },
      }
    );
    rt.job = job;
  } catch (e) {
    rt.running = false;
    rt.pausing = false;
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

async function pause() {
  const rt = activeRuntime.value;
  const job = rt.job;
  if (!job || rt.pausing) return;
  rt.running = false;
  rt.pausing = true;
  rt.stage = '正在暂停';
  try {
    await job.cancel();
  } catch (e) {
    if (rt.job === job) {
      rt.running = true;
      rt.pausing = false;
      rt.stage = '暂停失败，任务仍在执行';
    }
    MessagePlugin.error(`暂停失败：${String(e.message || e)}`);
  }
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
    MessagePlugin.warning('实例正在运行，请先暂停再删除');
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

function applyRestore(params) {
  const restored = restoreCourseParams(params || {});
  opt.scanOnly = restored.scanOnly;
  opt.timeout = restored.timeout;
  opt.maxAttempts = restored.maxAttempts;
  const rt = activeRuntime.value;
  rt.outcome = {
    status: '待执行',
    summary: '执行选项已回填，确认后点击开始学习。',
    failed: false,
  };
  rt.stage = '待执行';
}

watch(
  () => restoreState.nonce,
  () => {
    if (restoreState.appID !== 'course_autoplay') return;
    applyRestore(restoreState.params);
  },
  { immediate: true }
);

// keep-alive：首次显示与每次切回都自动刷新受控浏览器状态
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
.course-start-btn {
  margin-right: 4px;
}
.course-stage {
  margin-top: 8px;
  font-size: 13px;
  color: var(--tk-sub);
  font-variant-numeric: tabular-nums;
}
</style>
