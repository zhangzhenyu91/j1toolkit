<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card sg-pull-modal">
      <div class="m-head">
        <h3 class="m-title">从商旅拉取</h3>
        <button type="button" class="m-x" aria-label="关闭" @click="close">×</button>
      </div>
      <div class="sub-text">
        选择区段后，按「日期 × 用车人」逐人拉取打卡/费用数据，以商旅平台为准覆盖本地。
      </div>
      <div v-if="teamWarn" class="sg-pull-warn">
        商旅同步按登录者所属班组收敛（不随班组切换），跨班组拉取可能被拒绝。
      </div>

      <div class="sg-pull-range">
        <span class="field-label">起</span>
        <input v-model="from" type="date" class="wl-range num" :disabled="running" />
        <span class="field-label">止</span>
        <input v-model="to" type="date" class="wl-range num" :disabled="running" />
      </div>

      <div v-if="running" class="sg-pull-progress">
        <div class="sg-pull-bar">
          <div class="sg-pull-fill" :style="{ width: pct + '%' }"></div>
        </div>
        <div class="sub-text num">
          进度 {{ progress.done + progress.failed }}/{{ progress.total }}
          <span v-if="progress.skipped">（跳过 {{ progress.skipped }}）</span>
          <span v-if="progress.failed">（失败 {{ progress.failed }}）</span>
        </div>
      </div>

      <div v-if="error" class="wl-error">{{ error }}</div>
      <div v-if="resultText" class="sg-pull-result">{{ resultText }}</div>

      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">关闭</t-button>
        <t-button :loading="running" @click="start">发起拉取</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { fetchLogs } from '../../worklog/api.js';
import { fetchSyncProgress, syncPull } from '../api.js';

// 从商旅拉取弹层（批量同步）：Go /sync/pull 为逐成员口径（member_id+date），
// 前端按「区段内有出车记录的（日期 × 用车人）」逐人发起并聚合 /sync/progress 进度。
const props = defineProps({
  open: { type: Boolean, default: false },
  teamWarn: { type: Boolean, default: false }, // 超管切换到非本班班组时提示口径差异
});
const emit = defineEmits(['update:open', 'done']);

const today = new Date().toISOString().slice(0, 10);
const from = ref(today);
const to = ref(today);
const error = ref('');
const resultText = ref('');
const running = ref(false);
const progress = reactive({ total: 0, done: 0, failed: 0, skipped: 0 });

const pct = computed(() =>
  progress.total
    ? Math.min(100, Math.round(((progress.done + progress.failed) / progress.total) * 100))
    : 0
);

watch(
  () => props.open,
  (val) => {
    if (!val) return;
    error.value = '';
    resultText.value = '';
  }
);

// collectPairs 区段内逐日拉卡片清单，收集（日期 × 用车人）去重对。
async function collectPairs() {
  const pairs = new Map(); // `${date}:${memberId}` → {date, memberId, name}
  const [y1, m1, d1] = from.value.split('-').map(Number);
  const [y2, m2, d2] = to.value.split('-').map(Number);
  const start = new Date(y1, m1 - 1, d1);
  const end = new Date(y2, m2 - 1, d2);
  for (let t = new Date(start); t <= end; t.setDate(t.getDate() + 1)) {
    const date = `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`;
    const data = await fetchLogs({ date, scope: 'all' });
    (data.list || []).forEach((e) => {
      (e.members || []).forEach((m) => {
        if (!m.member_id) return;
        const key = `${date}:${m.member_id}`;
        if (!pairs.has(key)) pairs.set(key, { date, memberId: m.member_id, name: m.name });
      });
    });
  }
  return [...pairs.values()];
}

// start 发起批量拉取：逐（成员 × 日期）调 /sync/pull，再聚合轮询 /sync/progress 收尾。
async function start() {
  if (running.value) return;
  error.value = '';
  resultText.value = '';
  if (!from.value || !to.value) {
    error.value = '请选择拉取区段';
    return;
  }
  if (from.value > to.value) {
    error.value = '开始日期不能晚于结束日期';
    return;
  }
  const days = Math.round((new Date(to.value) - new Date(from.value)) / 86400000) + 1;
  if (days > 31) {
    error.value = '拉取区段不超过 31 天';
    return;
  }
  running.value = true;
  progress.total = 0;
  progress.done = 0;
  progress.failed = 0;
  progress.skipped = 0;
  try {
    const pairs = await collectPairs();
    progress.total = pairs.length;
    if (!pairs.length) {
      resultText.value = '所选区段没有出车记录，没有可拉取的用车人';
      running.value = false;
      return;
    }
    // 逐人发起（未绑定 40020 / 登录过期 40021 计跳过，其余计失败，均不中断整体）
    const jobIds = [];
    for (const p of pairs) {
      try {
        const d = await syncPull({ member_id: p.memberId, date: p.date });
        if (d && d.jobId) jobIds.push(d.jobId);
      } catch (e) {
        if (e.code === 40020 || e.code === 40021) {
          progress.skipped += 1;
          progress.done += 1; // 计入完成面，进度条不空转
        } else {
          progress.failed += 1;
        }
      }
    }
    // 聚合轮询任务进度直至全部 finished
    const pending = new Set(jobIds);
    let netFails = 0;
    while (pending.size) {
      for (const id of [...pending]) {
        try {
          const job = await fetchSyncProgress(id);
          netFails = 0;
          if (!job || job.finished) {
            pending.delete(id);
            progress.done += 1;
            if (job && Number(job.errors) > 0) progress.failed += 1;
          }
        } catch (e) {
          netFails += 1;
          // 任务不存在按已完成收（同口径）；连续网络失败兜底退出
          if (e.code === 40400) {
            pending.delete(id);
            progress.done += 1;
          } else if (netFails > 20) {
            pending.clear();
          }
        }
      }
      if (pending.size) await new Promise((r) => setTimeout(r, 1500));
    }
    resultText.value = `拉取完成：共 ${progress.total} 人日，成功 ${Math.max(progress.total - progress.failed - progress.skipped, 0)}、失败 ${progress.failed}、跳过 ${progress.skipped}（明细见「同步任务」核查流水）`;
    MessagePlugin.success('拉取完成');
    emit('done');
  } catch (e) {
    error.value = e.message || '拉取失败';
  } finally {
    running.value = false;
  }
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
.sg-pull-modal {
  width: min(520px, calc(100vw - 32px));
}
.sg-pull-warn {
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  padding: 8px 10px;
  font-size: 12px;
  color: #d25f00;
  margin: 8px 0;
}
.sg-pull-range {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 12px 0;
  flex-wrap: wrap;
}
.sg-pull-progress {
  margin: 10px 0;
}
.sg-pull-bar {
  height: 6px;
  border-radius: 4px;
  background: var(--tk-fill);
  overflow: hidden;
  margin-bottom: 6px;
}
.sg-pull-fill {
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.2s;
}
.sg-pull-result {
  background: var(--tk-green-soft);
  border-left: 6px solid var(--tk-green);
  padding: 10px 12px;
  font-size: 13px;
  color: #0b8a37;
  margin-top: 10px;
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
.wl-range {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
/* ===== 旧版弹层骨架覆写（.mask/.modal/.m-head/.m-title/.m-x） ===== */
.wl-modal-mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: rgba(29, 33, 41, 0.45);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.wl-modal {
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 520px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden auto;
  padding: 14px 18px 16px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.m-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  margin: 0;
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  line-height: 1;
  padding: 0;
}
.m-x:hover {
  background: var(--tk-ink);
  color: #fff;
}
</style>
