<template>
  <div class="daily-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">运行状态</div>
          <div class="normal-only">
            <div class="row">
              <span class="status-line">
                <span class="status-dot success" />
                同步任务由服务端经 U 盘指令触发
              </span>
              <span class="muted">U 盘位置：{{ usb.mount || '未检测到（接入后自动挂载）' }}</span>
              <span class="status-line">
                <span class="status-dot" :class="agent.tone" />
                后台监听：{{ agent.text }}
              </span>
            </div>
          </div>
          <div class="max-only">
            <div class="kv">
              <span class="k">上次成功</span><span>{{ statusText.lastSuccess }}</span>
              <span class="k">上次产物</span><span>{{ statusText.lastFile }}</span>
              <span class="k">最近任务</span><span>{{ statusText.lastRun }}</span>
            </div>
          </div>
        </t-card>

        <t-card>
          <div class="sec-title">U 盘设置</div>
          <div class="row">
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
            服务端在每日排程时刻下发导出指令，usb-agent 收到后导出到 U
            盘并回写结果；客户端不再本地定时。
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
import { agentStatusText } from '../lib/usb-agent.mjs';

const statusText = reactive({ lastSuccess: '-', lastFile: '-', lastRun: '-' });
const agent = reactive({ text: '读取中…', tone: 'warning' });
const usb = reactive({});
const usbEdit = reactive({ label: '', fallback_dir: '' });
const savingUsb = ref(false);
const histCard = ref(null);
const runDate = ref('');
const running = ref(false);
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
    const [st, agentState] = await Promise.all([
      call('getAppStatus', { app_id: 'daily_sync' }),
      call('getUsbAgentStatus').catch(() => null),
    ]);
    Object.assign(agent, agentStatusText(agentState));
    const stamp = st.stamp || {};
    const last = st.last_run || {};
    statusText.lastSuccess = stamp.time
      ? stamp.time + (stamp.count != null && stamp.count !== '' ? `（${stamp.count} 条）` : '')
      : '从未执行';
    statusText.lastFile = stamp.file || '-';
    statusText.lastRun = last.time
      ? `${last.time}　${STATUS_CN[last.status] || last.status}${last.error ? '：' + last.error : ''}`
      : '-';
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
