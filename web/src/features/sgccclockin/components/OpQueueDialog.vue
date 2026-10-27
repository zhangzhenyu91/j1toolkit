<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card sg-op-modal">
      <h3 class="card-title">同步任务</h3>
      <div class="sub-text">进行中的商旅同步 / 照片任务队列与核查流水</div>

      <!-- 同步任务（sync/pull 逐成员任务） -->
      <div class="sg-op-sec">
        <div class="sg-op-sec-head">同步任务</div>
        <div v-if="!jobs.length" class="muted">暂无进行中的同步任务</div>
        <div v-for="j in jobs" :key="j.id" class="sg-op-row">
          <span class="dot-tag" :class="j.finished ? 'green' : 'blue'">
            <span class="dot"></span>{{ j.finished ? '已完成' : '进行中' }}
          </span>
          <span class="sub-text"
            >{{ j.kind === 'pull' ? '手动拉取' : '每日核查' }} · {{ j.date }}</span
          >
          <span class="num sg-op-pct">{{ j.done }}/{{ j.total }}</span>
          <span v-if="j.errors" class="sg-op-err">失败 {{ j.errors }}</span>
        </div>
      </div>

      <!-- 照片任务（PhotoOp 队列） -->
      <div class="sg-op-sec">
        <div class="sg-op-sec-head">照片任务</div>
        <div v-if="!ops.length" class="muted">暂无进行中的照片任务</div>
        <div v-for="o in ops" :key="o.opId" class="sg-op-row">
          <span class="dot-tag" :class="o.failed ? 'red' : 'blue'">
            <span class="dot"></span>{{ opText(o.kind) }}
          </span>
          <span class="sub-text">卡片 #{{ o.entry_id }}</span>
          <span class="num sg-op-pct">{{ o.progress || 0 }}%</span>
          <span v-if="o.failed" class="sg-op-err">{{ o.error || '失败' }}</span>
          <span v-else-if="o.step" class="sub-text">{{ o.step }}</span>
        </div>
      </div>

      <!-- 核查流水 -->
      <div class="sg-op-sec">
        <div class="sg-op-sec-head">
          核查流水
          <span class="sg-op-head-acts">
            <button type="button" class="wl-act" @click="load">刷新</button>
            <button type="button" class="wl-act danger" @click="clearLogs">清理流水</button>
          </span>
        </div>
        <div v-if="!logs.length" class="muted">暂无核查流水</div>
        <div v-for="l in logs" :key="l.id" class="sg-op-log">
          <span class="num sg-op-log-date">{{ l.syncDate || l.sync_date }}</span>
          <span>{{ l.memberName || l.member_name || '—' }}</span>
          <span class="dot-tag" :class="logClass(l.result)">
            <span class="dot"></span>{{ logText(l.result) }}
          </span>
          <span class="sub-text">{{ l.type }}</span>
          <span v-if="l.detail" class="muted sg-op-log-detail">{{ l.detail }}</span>
        </div>
      </div>

      <div v-if="error" class="wl-error">{{ error }}</div>
      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">关闭</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onBeforeUnmount, ref, watch } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { clearSyncLogs, fetchSyncActive, fetchSyncLogs } from '../api.js';

// op 队列展示弹层：/sync/active（jobs + ops）+ /sync/logs 核查流水，打开期间 3s 轮询。
const props = defineProps({
  open: { type: Boolean, default: false },
});
const emit = defineEmits(['update:open', 'changed']);

const jobs = ref([]);
const ops = ref([]);
const logs = ref([]);
const error = ref('');
let timer = null;

// opText 照片任务类型文案。
function opText(kind) {
  return (
    {
      upload: '上传同步商旅中',
      delete: '解除商旅关联中',
      members: '人名同步商旅中',
      resync: '商旅重新同步中',
    }[kind] || '商旅同步中'
  );
}

// logClass/logText 核查流水结果标签。
function logClass(result) {
  return { ok: 'green', diff: 'blue', fail: 'red' }[result] || '';
}
function logText(result) {
  return { ok: '一致', diff: '已回写', fail: '失败' }[result] || result;
}

// load 拉任务队列与核查流水。
async function load() {
  error.value = '';
  try {
    const [act, lg] = await Promise.all([fetchSyncActive(), fetchSyncLogs()]);
    jobs.value = (act && act.jobs) || [];
    ops.value = (act && act.ops) || [];
    logs.value = (lg && lg.list) || [];
    emit('changed');
  } catch (e) {
    error.value = e.message || '加载失败';
  }
}

// open 变化：打开即轮询，关闭即停。
watch(
  () => props.open,
  (val) => {
    if (val) {
      load();
      timer = setInterval(load, 3000);
    } else if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }
);
onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});

// clearLogs 清理核查流水（确认后调 API）。
function clearLogs() {
  const dlg = DialogPlugin.confirm({
    header: '清理核查流水',
    body: '将清空本班组全部核查流水，确定清理？',
    confirmBtn: '清理',
    onConfirm: async () => {
      try {
        await clearSyncLogs();
        MessagePlugin.success('已清理');
        load();
      } catch (e) {
        error.value = e.message || '清理失败';
      } finally {
        dlg.destroy();
      }
    },
  });
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
.sg-op-modal {
  width: min(640px, calc(100vw - 32px));
}
.sg-op-sec {
  margin-top: 14px;
}
.sg-op-sec-head {
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-ink);
  margin-bottom: 8px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.sg-op-head-acts {
  display: flex;
  gap: 12px;
}
.sg-op-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 0;
  border-bottom: 1px solid var(--tk-line);
  font-size: 12px;
  flex-wrap: wrap;
}
.sg-op-pct {
  color: var(--tk-ink);
  font-weight: 600;
}
.sg-op-err {
  color: var(--tk-red);
  font-size: 12px;
}
.sg-op-log {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 0;
  border-bottom: 1px solid var(--tk-line);
  font-size: 12px;
  flex-wrap: wrap;
}
.sg-op-log-date {
  color: var(--tk-sub);
}
.sg-op-log-detail {
  flex-basis: 100%;
  padding-left: 2px;
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.wl-modal {
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.wl-act {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  cursor: pointer;
}
.wl-act.danger {
  color: var(--tk-red);
}
</style>
