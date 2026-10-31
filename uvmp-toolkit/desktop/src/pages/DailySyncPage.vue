<template>
  <div class="daily-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">运行状态</div>
          <div class="normal-only">
            <div class="row">
              <span class="status-line">
                <span class="status-dot" :class="sch.enabled ? 'warning' : 'default'" />
                {{ dueReason || (sch.enabled ? '等待下次自动导出' : '未启用每天自动导出') }}
              </span>
              <span class="muted">U 盘位置：{{ usb.mount || '未检测到（导出时将自动查找）' }}</span>
            </div>
          </div>
          <div class="max-only">
            <div class="kv">
              <span class="k">上次成功</span><span>{{ statusText.lastSuccess }}</span>
              <span class="k">上次产物</span><span>{{ statusText.lastFile }}</span>
              <span class="k">最近任务</span><span>{{ statusText.lastRun }}</span>
              <span class="k">下次自动导出</span><span>{{ statusText.nextRun }}</span>
            </div>
          </div>
        </t-card>

        <t-card>
          <div class="sec-title">定时与 U 盘设置</div>
          <div class="row">
            <t-checkbox v-model="sch.enabled">每天自动导出派车单</t-checkbox>
            <span class="muted">导出时间</span>
            <t-input v-model="sch.time" style="width: 110px" placeholder="09:15" />
            <t-button theme="default" :loading="saving" @click="saveSchedule">保存设置</t-button>
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted">U 盘名称</span>
            <t-input v-model="usbEdit.label" style="width: 150px" placeholder="GLKVM" />
            <t-button theme="default" :loading="savingUsb" @click="saveUsb">保存设置</t-button>
            <t-button variant="outline" @click="loadStatus">重新查找 U 盘</t-button>
          </div>
          <div class="row" style="margin-top: 8px">
            <t-input
              v-model="usbEdit.fallback_dir"
              class="grow"
              placeholder="U 盘不可用时的备用保存位置（留空则导出失败）"
            />
          </div>
          <p class="muted max-only" style="margin: 8px 0 0">
            到点自动执行，关机错过会在开机后自动补跑；派车单默认保存到 U
            盘，找不到时可保存到备用位置。
            <span v-if="driver === 'app'">当前为「客户端驻留执行」模式，可在设置页切换。</span>
            <span v-else>当前为「系统定时器」模式，客户端不开也执行。</span>
          </p>
        </t-card>
      </div>

      <div class="page-split-col">
        <t-card>
          <div class="sec-title">立即执行一次</div>
          <div class="row">
            <t-input v-model="runDate" style="width: 220px" placeholder="导出日期（留空为今天）" />
            <t-button theme="primary" :disabled="running" @click="startRun">立即执行一次</t-button>
            <t-button v-if="running" theme="danger" variant="outline" @click="cancel"
              >取消执行</t-button
            >
          </div>
          <p class="muted max-only" style="margin: 8px 0 0">
            选择要导出的日期，留空则导出今天；完成后可直接打开保存位置。
          </p>
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
          <HistoryCard ref="histCard" app-id="daily_sync" :format-result="fmtResult" />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onActivated, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, runJob } from '../api';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { dailyOutcome, friendlyTaskError } from '../lib/task-result.mjs';
import { restoreState } from '../lib/restore-bus.mjs';
import { restoreDailyParams } from '../lib/restore-params.mjs';

const sch = reactive({ enabled: true, time: '09:15' });
const statusText = reactive({ lastSuccess: '-', lastFile: '-', lastRun: '-', nextRun: '-' });
const usb = reactive({});
const usbEdit = reactive({ label: '', fallback_dir: '' });
const savingUsb = ref(false);
const dueReason = ref('');
const driver = ref('os');
const histCard = ref(null);
const runDate = ref('');
const running = ref(false);
const saving = ref(false);
const logs = ref([]);
const progress = reactive({ pct: 0, text: '' });
const outcome = ref({ status: '尚未开始', summary: '', failed: false });

let currentJob = null;

function fmtResult(r) {
  if (!r) return '';
  if (!r.count) return '当日无派车数据';
  return `${r.count} 条 → ${r.file || ''}${r.dest ? '（' + r.dest + '）' : ''}`;
}

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' };

async function loadStatus() {
  try {
    const st = await call('getAppStatus', { app_id: 'daily_sync' });
    const stamp = st.stamp || {};
    const last = st.last_run || {};
    statusText.lastSuccess = stamp.time
      ? stamp.time + (stamp.count != null && stamp.count !== '' ? `（${stamp.count} 条）` : '')
      : '从未执行';
    statusText.lastFile = stamp.file || '-';
    statusText.lastRun = last.time
      ? `${last.time}　${STATUS_CN[last.status] || last.status}${last.error ? '：' + last.error : ''}`
      : '-';
    statusText.nextRun = (st.schedule || {}).next_run || '-';
    dueReason.value = (st.due || {}).reason || '';
    driver.value = (st.schedule || {}).driver || 'os';
    sch.enabled = !!(st.schedule || {}).enabled;
    sch.time = (st.schedule || {}).time || '09:15';
    Object.assign(usb, st.usb || {});
    usbEdit.label = usb.label || '';
    usbEdit.fallback_dir = usb.fallback_dir || '';
  } catch (_e) {
    /* 状态读取失败不打断页面 */
  }
}

async function saveUsb() {
  savingUsb.value = true;
  try {
    await call('saveConfig', {
      usb: {
        label: usbEdit.label.trim() || 'GLKVM',
        fallback_dir: usbEdit.fallback_dir.trim(),
      },
    });
    MessagePlugin.success('已保存');
    loadStatus();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    savingUsb.value = false;
  }
}

async function saveSchedule() {
  if (!/^\d{1,2}:\d{2}$/.test(sch.time)) {
    MessagePlugin.warning('时间格式应为 HH:MM，如 09:15');
    return;
  }
  saving.value = true;
  try {
    await call('saveConfig', { schedule: { enabled: sch.enabled, time: sch.time } });
    MessagePlugin.success('已保存，到点会自动执行');
    loadStatus();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

function appendLog(line) {
  logs.value.push(line);
  if (logs.value.length > 1500) logs.value.splice(0, logs.value.length - 1500);
}

async function startRun() {
  try {
    const cfg = await call('getConfig');
    if (!(cfg.sso || {}).has_password && !(cfg.dev || {}).mock) {
      MessagePlugin.warning('尚未配置派车系统账号——请先到「设置」页填写（或开启开发模式试用）');
      return;
    }
  } catch (_e) {
    /* 预检失败照常下发 */
  }
  logs.value = [];
  outcome.value = { status: '执行中', summary: '正在导出派车单，请稍候…', failed: false };
  running.value = true;
  progress.pct = 0;
  progress.text = '';
  try {
    currentJob = runJob(
      'daily_sync',
      { date: runDate.value.trim() },
      {
        onLog: appendLog,
        onProgress: (p) => {
          progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          progress.text = `${p.label || ''} ${p.current}/${p.total}`;
        },
        onDone: (job) => {
          running.value = false;
          outcome.value = dailyOutcome(job);
          loadStatus();
          histCard.value && histCard.value.reload();
        },
        onError: (e) => {
          running.value = false;
          const message = String(e.message || e);
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
  if (currentJob) currentJob.cancel();
}

function applyRestore(params) {
  const restored = restoreDailyParams(params || {});
  runDate.value = restored.date;
  outcome.value = {
    status: '待执行',
    summary: '执行日期已回填，确认后点击立即执行。',
    failed: false,
  };
}

watch(
  () => restoreState.nonce,
  () => {
    if (restoreState.appID !== 'daily_sync') return;
    applyRestore(restoreState.params);
  },
  { immediate: true }
);

// keep-alive：首次显示与每次切回都触发（任务组件常驻，状态不丢）；
// 最近执行记录由 HistoryCard 自行在激活时刷新
onActivated(loadStatus);
</script>
