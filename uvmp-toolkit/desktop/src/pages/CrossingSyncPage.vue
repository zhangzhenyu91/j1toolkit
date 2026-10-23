<template>
  <div>
    <t-card>
      <div class="sec-title">
        状态<span class="right">{{ cfgOk ? '' : '未配置内网表路径，请先完成下方配置' }}</span>
      </div>
      <div class="kv">
        <span class="k">内网表</span><span>{{ st.local_path || '未配置' }}</span>
        <span class="k">文件检查</span
        ><span>{{
          st.local_path ? (st.local_exists ? '存在' : '不存在（执行将失败）') : '-'
        }}</span>
        <span class="k">U盘挂载</span
        ><span>{{ st.usb_mount || '未检测到（服务端挂载后自动执行）' }}</span>
        <span class="k">上次兜底</span><span>{{ stampText }}</span> <span class="k">最近任务</span
        ><span>{{ lastRunText }}</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">同步设置</div>
      <div class="row">
        <span class="muted">内网表路径</span>
        <t-input
          v-model="edit.local_path"
          class="grow"
          placeholder="内网交跨统计表 xlsx 完整路径"
        />
        <t-button variant="outline" @click="pick">选择文件</t-button>
      </div>
      <div class="row" style="margin-top: 10px">
        <span class="muted">上盘文件名</span>
        <t-input
          v-model="edit.file_name"
          style="width: 260px"
          placeholder="检修一班交跨统计表.xlsx"
        />
        <span class="muted">夜间兜底</span>
        <t-input v-model="edit.nightly_time" style="width: 110px" placeholder="23:55" />
        <t-button theme="primary" :loading="saving" @click="save">保存</t-button>
      </div>
      <div class="muted" style="margin-top: 8px">
        服务端挂载 U 盘并下发指令后，客户端每分钟轮询执行：snapshot=内网表拷贝上盘，
        replace=U盘新表覆盖内网表（原文件自动备份为 .bak）；夜间兜底时刻无指令也拷贝内网表上盘。
        00:00 后的服务端指令次晨首轮轮询补执行。
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">立即执行</div>
      <div class="row">
        <t-button theme="primary" :disabled="running" @click="startRun"
          >立即执行一次（按 U 盘指令）</t-button
        >
        <t-button variant="outline" :disabled="running" @click="startNightly"
          >执行一次夜间兜底</t-button
        >
        <t-button v-if="running" theme="danger" @click="cancel">取消</t-button>
      </div>
      <t-alert v-if="runError" theme="error" style="margin-top: 10px">
        启动失败：{{ runError }}
      </t-alert>
      <div ref="logEl" class="logview" style="margin-top: 10px">{{ logs.join('\n') }}</div>
      <div class="row" style="margin-top: 10px" v-if="doneInfo">
        <t-tag :theme="doneInfo.ok ? 'success' : 'danger'" variant="light-outline">
          {{ doneInfo.statusText }}
        </t-tag>
        <span class="muted">{{ doneInfo.text }}</span>
      </div>
    </t-card>

    <HistoryCard ref="histCard" app-id="crossing_sync" :format-result="fmtResult" />
  </div>
</template>

<script setup>
import { nextTick, onActivated, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call, pickFile, runJob } from '../api';
import HistoryCard from '../components/HistoryCard.vue';

const st = reactive({ local_path: '', local_exists: false, usb_mount: '' });
const edit = reactive({ local_path: '', file_name: '', nightly_time: '' });
const stampText = ref('-');
const lastRunText = ref('-');
const cfgOk = ref(false);
const saving = ref(false);
const running = ref(false);
const logs = ref([]);
const logEl = ref(null);
const doneInfo = ref(null);
const runError = ref('');

let currentJob = null;

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' };

function fmtResult(r) {
  if (!r) return '';
  if (r.skipped === 'usb_absent') return 'U盘未挂载，跳过';
  if (r.skipped === 'no_command') return 'U盘无指令，跳过';
  if (r.skipped === 'unknown_kind') return `未知指令 kind=${r.kind || ''}（已跳过）`;
  if (r.skipped === 'bad_file') return '指令 file 字段非法（已跳过）';
  const act =
    { snapshot: '内网表 → U盘', replace: 'U盘新表 → 内网' }[r.kind || r.action] ||
    r.kind ||
    r.action ||
    '';
  return `${act}${r.nightly ? '（夜间兜底）' : ''}${r.cycle_id ? ' cycle=' + r.cycle_id : ''} → ${r.file || ''}`;
}

async function loadStatus() {
  try {
    const s = await call('getAppStatus', { app_id: 'crossing_sync' });
    const ts = s.crossing_sync || {};
    st.local_path = ts.local_path || '';
    st.local_exists = !!ts.local_exists;
    st.usb_mount = (s.usb || {}).mount || '';
    edit.local_path = ts.local_path || '';
    edit.file_name = ts.file_name || '检修一班交跨统计表.xlsx';
    edit.nightly_time = ts.nightly_time || '23:55';
    cfgOk.value = !!st.local_path;
    stampText.value = s.stamp && s.stamp.time ? s.stamp.time : '从未执行';
    const last = s.last_run || {};
    lastRunText.value = last.time
      ? `${last.time}　${STATUS_CN[last.status] || last.status}${last.error ? '：' + last.error : ''}`
      : '-';
  } catch (e) {
    /* MessagePlugin 由调用处统一处理 */
  }
}

async function pick() {
  const p = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }]);
  if (p) edit.local_path = p;
}

async function save() {
  if (!edit.local_path.trim()) {
    MessagePlugin.warning('请先选择内网表 xlsx 文件');
    return;
  }
  if (!/^\d{1,2}:\d{2}$/.test(edit.nightly_time.trim())) {
    MessagePlugin.warning('夜间兜底时间格式应为 HH:MM，如 23:55');
    return;
  }
  saving.value = true;
  try {
    await call('saveConfig', {
      crossing_sync: {
        local_path: edit.local_path.trim(),
        file_name: edit.file_name.trim() || '检修一班交跨统计表.xlsx',
        nightly_time: edit.nightly_time.trim(),
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
  nextTick(() => {
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight;
  });
}

function start(params) {
  if (!cfgOk.value) {
    MessagePlugin.warning('尚未配置内网表路径——请先在上方「同步设置」选择文件并保存');
    return;
  }
  logs.value = [];
  doneInfo.value = null;
  runError.value = '';
  running.value = true;
  try {
    currentJob = runJob('crossing_sync', params, {
      onLog: appendLog,
      onDone: (job) => {
        running.value = false;
        doneInfo.value = {
          ok: job.status === 'success',
          statusText: STATUS_CN[job.status] || job.status,
          text: job.error || fmtResult(job.result),
        };
        loadStatus();
        histCard.value && histCard.value.reload();
      },
      onError: (e) => {
        running.value = false;
        runError.value = String(e.message || e);
      },
    });
  } catch (e) {
    running.value = false;
    runError.value = String(e.message || e);
  }
}

function startRun() {
  start({ tick: true });
}
function startNightly() {
  start({ nightly: true });
}
function cancel() {
  if (currentJob) currentJob.cancel();
}

const histCard = ref(null);

// keep-alive：首次显示与每次切回都触发；最近执行记录由 HistoryCard 自行在激活时刷新
onActivated(loadStatus);
</script>
