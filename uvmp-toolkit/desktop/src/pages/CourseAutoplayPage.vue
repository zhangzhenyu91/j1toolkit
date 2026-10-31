<template>
  <div class="course-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">课程浏览器</div>
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

const st = reactive({
  cdp_url: '',
  browser: '',
  connected: false,
  pages: 0,
  playwright: false,
  active_lists: 0,
});
const opt = reactive({ scanOnly: false, maxAttempts: 3, timeout: 120 });
const startUrl = ref('');
const opening = ref(false);
const running = ref(false);
const starting = ref(false);
const pausing = ref(false);
const logs = ref([]);
const progress = reactive({ pct: 0, text: '' });
const outcome = ref({ status: '尚未开始', summary: '', failed: false });
const histCard = ref(null);
const stageLabel = ref('未开始');
const advancedOpen = ref(false);
const techOpen = ref(false);

let currentJob = null;

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
const stageText = computed(() => '阶段：' + stageLabel.value);
const canStart = computed(() => ready.value && !running.value && !starting.value && !opening.value);

function countOf(v) {
  if (Array.isArray(v)) return v.length;
  return typeof v === 'number' ? v : 0;
}

function applyStatus(s) {
  st.cdp_url = s.cdp_url || '';
  st.browser = s.browser || '';
  st.connected = !!s.connected;
  st.pages = countOf(s.pages);
  st.playwright = !!s.playwright;
  st.active_lists = countOf(s.active_lists);
  if (!startUrl.value) startUrl.value = s.start_url || '';
}

async function loadStatus() {
  try {
    applyStatus((await call('getAppStatus', { app_id: 'course_autoplay' })) || {});
  } catch (_e) {
    /* 状态读取失败不打断页面 */
  }
}

async function openBrowser() {
  opening.value = true;
  try {
    await call('saveConfig', { browser_automation: { start_url: startUrl.value.trim() } });
    await call('launchAppBrowser', { app_id: 'course_autoplay' });
    await loadStatus();
    MessagePlugin.success('请在该浏览器登录、打开课程设置列表并置前台');
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    opening.value = false;
  }
}

function appendLog(line) {
  logs.value.push(line);
  if (logs.value.length > 1500) logs.value.splice(0, logs.value.length - 1500);
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
  if (starting.value || running.value) return;
  starting.value = true;
  try {
    const previousJob = currentJob;
    if (previousJob) await previousJob.finished;

    const scanOnly = !!opt.scanOnly;
    logs.value = [];
    outcome.value = { status: '执行中', summary: '正在学习课程…', failed: false };
    stageLabel.value = '准备启动';
    running.value = true;
    pausing.value = false;
    progress.pct = 0;
    progress.text = '';
    let job;
    job = runJob(
      'course_autoplay',
      {
        scan_only: scanOnly,
        timeout: Number(opt.timeout) || 120,
        max_attempts: Number(opt.maxAttempts) || 3,
      },
      {
        onLog: appendLog,
        onProgress: (p) => {
          progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          progress.text = p.total > 0 ? `${p.label || ''} ${p.current}/${p.total}` : p.label || '';
          stageLabel.value = `${p.label || '处理中'}（${p.current}/${p.total}）`;
        },
        onDone: (doneJob) => {
          if (currentJob !== job) return;
          currentJob = null;
          running.value = false;
          pausing.value = false;
          loadStatus();
          histCard.value && histCard.value.reload();
          const r = doneJob.result || {};
          outcome.value = courseOutcome(doneJob, { scanOnly });
          stageLabel.value = r.paused
            ? '已暂停（可继续学习）'
            : doneJob.status === 'success'
              ? (r.pending ?? 0) > 0
                ? '本轮结束（仍有未完成）'
                : scanOnly
                  ? '扫描完成'
                  : '学习完成'
              : outcome.value.status;
          if (doneJob.status === 'success' && !r.paused && (r.pending ?? 0) === 0 && !scanOnly) {
            DialogPlugin.confirm({
              header: '学习完成',
              body: `当前课程列表已全部达到 100%，共 ${r.total ?? 0} 门课程。`,
              confirmBtn: '知道了',
            });
          }
        },
        onError: (e) => {
          if (currentJob !== job) return;
          currentJob = null;
          running.value = false;
          pausing.value = false;
          const message = String(e.message || e);
          logs.value.push(message);
          outcome.value = {
            status: '启动失败',
            summary: friendlyTaskError(message),
            failed: true,
          };
          stageLabel.value = '启动失败';
        },
      }
    );
    currentJob = job;
  } catch (e) {
    running.value = false;
    pausing.value = false;
    const message = String(e.message || e);
    logs.value.push(message);
    outcome.value = { status: '启动失败', summary: friendlyTaskError(message), failed: true };
    stageLabel.value = '启动失败';
  } finally {
    starting.value = false;
  }
}

async function pause() {
  const job = currentJob;
  if (!job || pausing.value) return;
  running.value = false;
  pausing.value = true;
  stageLabel.value = '正在暂停';
  try {
    await job.cancel();
  } catch (e) {
    if (currentJob === job) {
      running.value = true;
      pausing.value = false;
      stageLabel.value = '暂停失败，任务仍在执行';
    }
    MessagePlugin.error(`暂停失败：${String(e.message || e)}`);
  }
}

function applyRestore(params) {
  const restored = restoreCourseParams(params || {});
  opt.scanOnly = restored.scanOnly;
  opt.timeout = restored.timeout;
  opt.maxAttempts = restored.maxAttempts;
  outcome.value = {
    status: '待执行',
    summary: '执行选项已回填，确认后点击开始学习。',
    failed: false,
  };
  stageLabel.value = '待执行';
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
onMounted(loadStatus);
onActivated(loadStatus);
</script>

<style scoped>
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
