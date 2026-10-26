<template>
  <div>
    <t-card>
      <div class="sec-title">受控浏览器</div>
      <div class="kv">
        <span class="k">CDP 地址</span><span>{{ st.cdp_url || '未启动' }}</span>
        <span class="k">浏览器</span><span>{{ st.browser || '-' }}</span>
        <span class="k">连接 / 页面</span
        ><span
          >{{ st.connected ? '已连接' : '未连接' }} /
          {{ st.connected ? st.pages + ' 个页' : '-' }}</span
        >
        <span class="k">Playwright</span><span>{{ st.playwright ? '可用' : '不可用' }}</span>
        <span class="k">可恢复培训班</span><span>{{ st.active_lists }} 个</span>
      </div>
      <div class="row" style="margin-top: 10px">
        <span class="muted">启动网址</span>
        <t-input v-model="startUrl" class="grow" placeholder="输入打开浏览器后自动访问的 http/https 网址" />
      </div>
      <div class="row" style="margin-top: 10px">
        <t-button variant="outline" :loading="opening" :disabled="running" @click="openBrowser"
          >打开浏览器</t-button
        >
        <span class="muted">启动或复用受控浏览器；请在该浏览器登录、打开课程设置列表并置前台</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">执行选项</div>
      <div class="row">
        <t-checkbox v-model="opt.scanOnly">仅扫描（不进入课程）</t-checkbox>
        <span class="muted">单课尝试次数</span>
        <t-input-number v-model="opt.maxAttempts" :min="1" :max="10" style="width: 110px" />
        <span class="muted">单步超时（秒）</span>
        <t-input-number
          v-model="opt.timeout"
          :min="10"
          :max="600"
          :step="10"
          style="width: 110px"
        />
      </div>
      <div class="row" style="margin-top: 10px">
        <t-button theme="primary" :loading="starting" :disabled="!canStart" @click="startRun"
          >开始学习</t-button
        >
        <t-button v-if="running" theme="danger" @click="pause">暂停学习</t-button>
        <span v-if="pausing" class="muted">正在暂停</span>
        <span class="muted" v-if="!st.connected || !st.playwright">请先打开受控浏览器</span>
      </div>
      <t-alert v-if="runError" theme="error" style="margin-top: 10px">
        启动失败：{{ runError }}
      </t-alert>
      <t-alert v-if="doneInfo" :theme="doneInfo.theme" style="margin-top: 10px">
        {{ doneInfo.text }}
      </t-alert>
      <div style="margin-top: 10px">
        <t-progress
          theme="plump"
          :percentage="progress.pct"
          :label="progress.text"
          :color="'#0E3DA8'"
          track-color="rgba(14,61,168,.10)"
        />
      </div>
      <div ref="logEl" class="logview" style="margin-top: 10px">{{ logs.join('\n') }}</div>
    </t-card>

    <HistoryCard ref="histCard" app-id="course_autoplay" :format-result="fmtResult" show-dir />
  </div>
</template>

<script setup>
import { computed, nextTick, onActivated, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, runJob } from '../api';
import HistoryCard from '../components/HistoryCard.vue';

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
const logEl = ref(null);
const progress = reactive({ pct: 0, text: '' });
const runError = ref('');
const doneInfo = ref(null);
const histCard = ref(null);

let currentJob = null;

const canStart = computed(
  () =>
    st.connected &&
    st.playwright &&
    !running.value &&
    !starting.value &&
    !opening.value
);

// pages / active_lists 后端可能给数字或数组，统一按数量展示
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
  startUrl.value = s.start_url || '';
}

async function loadStatus() {
  try {
    const s = await call('getAppStatus', { app_id: 'course_autoplay' });
    applyStatus(s || {});
  } catch (e) {
    /* MessagePlugin 由调用处统一处理 */
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
  nextTick(() => {
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight;
  });
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
    runError.value = '';
    doneInfo.value = null;
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
        },
        onDone: (doneJob) => {
          if (currentJob !== job) return;
          currentJob = null;
          running.value = false;
          pausing.value = false;
          loadStatus();
          histCard.value && histCard.value.reload();
          const r = doneJob.result || {};
          // 任务结束保留最终日志（不清空），结果以提示条 + 历史记录收口
          if (r.paused) {
            // 用户暂停：浏览器/登录态保留，可续刷，不是失败
            doneInfo.value = {
              theme: 'info',
              text: `已暂停：浏览器和登录态保留，可切换列表后再次开始学习。${countText(r)}`,
            };
          } else if (doneJob.status === 'success') {
            const pending = r.pending ?? 0;
            doneInfo.value =
              pending > 0
                ? { theme: 'warning', text: `仍有未完成：${countText(r)}` }
                : {
                    theme: 'success',
                    text: `${scanOnly ? '扫描完成' : '全部学习完成 100%'}：${countText(r)}`,
                  };
            if (pending === 0 && !scanOnly) {
              DialogPlugin.confirm({
                header: '学习完成',
                body: `当前课程列表已全部达到 100%，共 ${r.total ?? 0} 门课程。`,
                confirmBtn: '知道了',
              });
            }
          } else if (doneJob.status === 'cancelled') {
            doneInfo.value = { theme: 'info', text: `已取消：${countText(r)}` };
          } else {
            doneInfo.value = {
              theme: 'error',
              text: `执行失败：${doneJob.error || countText(r)}`,
            };
          }
        },
        onError: (e) => {
          if (currentJob !== job) return;
          currentJob = null;
          running.value = false;
          pausing.value = false;
          runError.value = String(e.message || e);
        },
      }
    );
    currentJob = job;
  } catch (e) {
    running.value = false;
    pausing.value = false;
    runError.value = String(e.message || e);
  } finally {
    starting.value = false;
  }
}

async function pause() {
  const job = currentJob;
  if (!job || pausing.value) return;
  running.value = false;
  pausing.value = true;
  try {
    await job.cancel();
  } catch (e) {
    if (currentJob === job) {
      running.value = true;
      pausing.value = false;
    }
    MessagePlugin.error(`暂停失败：${String(e.message || e)}`);
  }
}

// keep-alive：首次显示与每次切回都自动刷新受控浏览器状态
onMounted(loadStatus);
onActivated(loadStatus);
</script>
