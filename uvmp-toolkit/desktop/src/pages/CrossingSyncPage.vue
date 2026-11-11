<template>
  <div class="sync-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">运行状态</div>
          <p v-if="!cfgOk" class="muted" style="margin: 0 0 8px">
            尚未选择内网统计表，请先在下方完成同步设置
          </p>
          <div class="normal-only">
            <div class="row">
              <span class="status-line">
                <span class="status-dot" :class="fileTone" />
                文件检查：{{ fileCheckText }}
              </span>
              <span class="status-line">
                <span class="status-dot" :class="st.usb_mount ? 'success' : 'warning'" />
                U 盘：{{ st.usb_mount || '未检测到（接入后将自动执行）' }}
              </span>
              <span class="status-line">
                <span class="status-dot" :class="agent.tone" />
                后台监听：{{ agent.text }}
              </span>
            </div>
          </div>
          <div class="max-only">
            <div class="kv">
              <span class="k">内网表</span><span>{{ st.local_path || '未配置' }}</span>
              <span class="k">文件检查</span><span>{{ fileCheckText }}</span>
              <span class="k">U 盘状态</span
              ><span>{{ st.usb_mount || '未检测到（接入后将自动执行）' }}</span>
              <span class="k">上次手动备份</span><span>{{ stampText }}</span>
              <span class="k">最近任务</span><span>{{ lastRunText }}</span>
            </div>
          </div>
        </t-card>

        <t-card>
          <div class="sec-title">同步设置</div>
          <div class="row">
            <span class="muted sync-label">内网表</span>
            <t-input
              v-model="edit.local_path"
              class="grow"
              placeholder="选择内网统计表，或输入文件位置"
            />
            <t-button variant="outline" @click="pick">选择文件</t-button>
          </div>
          <div class="row" style="margin-top: 10px">
            <span class="muted sync-label">U 盘中的文件名</span>
            <t-input
              v-model="edit.file_name"
              style="width: 240px"
              placeholder="检修一班交跨统计表.xlsx"
            />
          </div>
          <div class="row" style="margin-top: 10px">
            <t-button theme="default" :loading="saving" @click="save">保存设置</t-button>
            <span class="muted">同步由服务端 U 盘指令触发；替换内网表前会自动备份原文件。</span>
          </div>
          <p class="muted max-only" style="margin: 8px 0 0">
            U 盘接入后由 usb-agent 消费服务端指令：备份内网表，或用 U 盘中的新表替换内网表；
            替换前会自动备份原文件。
          </p>
        </t-card>
      </div>

      <div class="page-split-col">
        <t-card>
          <div class="sec-title">手动操作（调试）</div>
          <div class="row">
            <t-button
              :theme="syncButtonTheme('tick')"
              :variant="syncButtonVariant('tick')"
              :disabled="running"
              @click="startRun"
              >检查 U 盘指令</t-button
            >
            <t-button
              :theme="syncButtonTheme('nightly')"
              :variant="syncButtonVariant('nightly')"
              :disabled="running"
              @click="startNightly"
              >本地备份到 U 盘</t-button
            >
            <t-button v-if="running" theme="danger" variant="outline" @click="cancel"
              >取消执行</t-button
            >
          </div>
          <p class="muted max-only" style="margin: 8px 0 0">
            可按 U 盘中的任务同步，或直接备份内网表；完成后可查看同步结果。
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
          <HistoryCard ref="histCard" app-id="crossing_sync" :format-result="fmtResult" />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onActivated, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, pickFile, runJob } from '../api';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { syncOutcome, friendlyTaskError } from '../lib/task-result.mjs';
import { restoreState } from '../lib/restore-bus.mjs';
import { restoreSyncParams } from '../lib/restore-params.mjs';
import { agentStatusText } from '../lib/usb-agent.mjs';

const st = reactive({ local_path: '', local_exists: false, usb_mount: '' });
const agent = reactive({ text: '读取中…', tone: 'warning' });
const edit = reactive({ local_path: '', file_name: '' });
const stampText = ref('-');
const lastRunText = ref('-');
const cfgOk = ref(false);
const saving = ref(false);
const running = ref(false);
const logs = ref([]);
const progress = reactive({ pct: 0, text: '' });
const outcome = ref({ status: '尚未开始', summary: '', failed: false });

let currentJob = null;

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' };
const fileCheckText = computed(() => {
  if (!st.local_path) return '-';
  return st.local_exists ? '存在' : '不存在（执行将失败）';
});
const fileTone = computed(() => {
  if (!st.local_path) return 'default';
  return st.local_exists ? 'success' : 'danger';
});

const restoredAction = ref('tick');

function syncButtonTheme(action) {
  return restoredAction.value === action ? 'primary' : 'default';
}

function syncButtonVariant(action) {
  return restoredAction.value === action ? 'base' : 'outline';
}

function fmtResult(r) {
  if (!r) return '';
  if (r.skipped === 'usb_absent') return 'U盘未挂载，跳过';
  if (r.skipped === 'no_command') return 'U 盘无同步指令，跳过';
  if (r.skipped === 'unknown_kind') return `未知指令 ${r.kind || r.action || ''}（已跳过）`;
  if (r.skipped === 'bad_file') return '指令 file 字段非法（已跳过）';
  if (r.skipped === 'agent_owned') return '服务端指令由后台 usb-agent 消费（手动入口不执行）';
  const act =
    { snapshot: '内网表 → U盘', replace: 'U盘新表 → 内网' }[r.kind || r.action] ||
    r.kind ||
    r.action ||
    '';
  return `${act}${r.nightly ? '（夜间兜底）' : ''}${r.cycle_id ? ' cycle=' + r.cycle_id : ''} → ${r.file || ''}`;
}

async function loadStatus() {
  try {
    const [s, agentState] = await Promise.all([
      call('getAppStatus', { app_id: 'crossing_sync' }),
      call('getUsbAgentStatus').catch(() => null),
    ]);
    Object.assign(agent, agentStatusText(agentState));
    const ts = s.crossing_sync || {};
    st.local_path = ts.local_path || '';
    st.local_exists = !!ts.local_exists;
    st.usb_mount = (s.usb || {}).mount || '';
    edit.local_path = ts.local_path || '';
    edit.file_name = ts.file_name || '检修一班交跨统计表.xlsx';
    cfgOk.value = !!st.local_path;
    stampText.value = s.stamp && s.stamp.time ? s.stamp.time : '从未执行';
    const last = s.last_run || {};
    lastRunText.value = last.time
      ? `${last.time}　${STATUS_CN[last.status] || last.status}${last.error ? '：' + last.error : ''}`
      : '-';
  } catch (_e) {
    /* 状态读取失败不打断页面 */
  }
}

async function pick() {
  const p = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }]);
  if (p) edit.local_path = p;
}

async function save() {
  if (!edit.local_path.trim()) {
    MessagePlugin.warning('请先选择内网统计表');
    return;
  }
  saving.value = true;
  try {
    await call('saveConfig', {
      crossing_sync: {
        local_path: edit.local_path.trim(),
        file_name: edit.file_name.trim() || '检修一班交跨统计表.xlsx',
      },
    });
    MessagePlugin.success('已保存');
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

function start(params, beginText) {
  // 双击/回车重复触发会连开两个任务，后端会报「该实例已有任务在运行」
  if (running.value) return;
  if (!cfgOk.value) {
    MessagePlugin.warning('尚未配置内网表路径——请先在上方「同步设置」选择文件并保存');
    return;
  }
  logs.value = [];
  outcome.value = { status: '执行中', summary: beginText, failed: false };
  running.value = true;
  progress.pct = 0;
  progress.text = '';
  try {
    currentJob = runJob('crossing_sync', params, {
      onLog: appendLog,
      onProgress: (p) => {
        progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
        progress.text = `${p.label || ''} ${p.current}/${p.total}`;
      },
      onDone: (job) => {
        running.value = false;
        outcome.value = syncOutcome(job, {
          usbMount: st.usb_mount,
          localPath: st.local_path,
        });
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
    });
  } catch (e) {
    running.value = false;
    const message = String(e.message || e);
    logs.value.push(message);
    outcome.value = { status: '启动失败', summary: friendlyTaskError(message), failed: true };
  }
}

function startRun() {
  start({ tick: true }, '正在检查 U 盘中的服务端指令…');
}
function startNightly() {
  start({ nightly: true }, '正在备份内网表到 U 盘…');
}
function cancel() {
  if (currentJob) currentJob.cancel();
}

const histCard = ref(null);

function applyRestore(params) {
  const restored = restoreSyncParams(params || {});
  restoredAction.value = restored.action;
  outcome.value = {
    status: '待执行',
    summary:
      restored.action === 'nightly'
        ? '已回填：夜间兜底，确认后点击对应执行按钮。'
        : '已回填：U 盘指令检查，确认后点击对应执行按钮。',
    failed: false,
  };
}

watch(
  () => restoreState.nonce,
  () => {
    if (restoreState.appID !== 'crossing_sync') return;
    applyRestore(restoreState.params);
  },
  { immediate: true }
);

// keep-alive：首次显示与每次切回都触发；最近执行记录由 HistoryCard 自行在激活时刷新
onActivated(loadStatus);
</script>
