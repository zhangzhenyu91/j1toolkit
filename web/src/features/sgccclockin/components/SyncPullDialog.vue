<template>
  <div v-if="rendered" class="wl-modal-mask ov-mask" :class="phaseClass" @click.self="close">
    <div class="wl-modal card sg-pull-modal ov-panel">
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
        <input
          v-model="from"
          type="date"
          class="wl-range num"
          :disabled="running"
          @click="openPicker"
          @change="checkRange('from')"
        />
        <span class="field-label">止</span>
        <input
          v-model="to"
          type="date"
          class="wl-range num"
          :disabled="running"
          @click="openPicker"
          @change="checkRange('to')"
        />
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
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { fetchLogs } from '../../worklog/api.js';
import { useOverlayMotion } from '../../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../../components/overlay-motion.css';
import { fetchSyncProgress, syncPull } from '../api.js';
import { pollDelay, pollExpired } from '../../core/async.js';

// 从商旅拉取弹层（批量同步）：Go /sync/pull 为逐成员口径（member_id+date），
// 前端按「区段内有出车记录的（日期 × 用车人）」逐人发起，与 /sync/progress 聚合轮询并行推进进度。
const props = defineProps({
  open: { type: Boolean, default: false },
  teamWarn: { type: Boolean, default: false }, // 超管切换到非本班班组时提示口径差异
});
const emit = defineEmits(['update:open', 'done']);

// localDay 本地日历日（YYYY-MM-DD）：按本机时区拼年月日（toISOString().slice() 是 UTC 日，凌晨会退一天）。
function localDay(d = new Date()) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

const today = localDay();
const from = ref(today);
const to = ref(today);
const error = ref('');
const resultText = ref('');
const running = ref(false);
const progress = reactive({ total: 0, done: 0, failed: 0, skipped: 0 });
// disposed 离页标记：卸载后停止聚合轮询，且不再回写/toast 父视图。
let disposed = false;

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

// openPicker 点日期输入框任意位置即展开系统日期选择（不支持的浏览器保留原生行为）。
function openPicker(ev) {
  const el = ev && ev.target;
  try {
    if (el && el.showPicker) el.showPicker();
  } catch (e) {
    // 浏览器拒绝非手势触发时保留原生行为
  }
}

// checkRange 起止校验：from > to 时回退改动的那端并提示。
function checkRange(which) {
  if (!from.value || !to.value || from.value <= to.value) return;
  MessagePlugin.info('开始日期不能晚于结束日期');
  if (which === 'from') from.value = to.value;
  else to.value = from.value;
}

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

// start 发起批量拉取：逐（成员 × 日期）调 /sync/pull，与聚合轮询 /sync/progress 并行收尾。
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
    // 并发发起：全部（日期 × 用车人）一次性提交，由后端并发门限（SGCC_SYNC_CONCURRENCY，默认 4）
    // 决定真正同时执行的单元数；不再逐请求等待，避免 Web 侧把后端并发压成串行。
    // 发起与聚合轮询并行：任务登记一个即入轮询池，成员完成即计入进度条，
    // 避免先发完全部任务再轮询（后端任务已跑完）导致进度 0 直接跳到完成。
    const jobs = new Map(); // jobId → {done,failed,finished} 各任务最近快照
    const base = { done: 0, failed: 0, skipped: 0 }; // 发起阶段计数（跳过也计完成面）
    let firstMsg = ''; // 首条失败原因，收尾时展示
    // applyProgress 每轮按全部任务快照的绝对值 + 发起阶段计数重算进度：
    // job.done 含失败步，成功面须扣掉失败，失败不得重复计入完成；快照重算避免跨轮重复累计。
    function applyProgress() {
      let done = base.done;
      let failed = base.failed;
      for (const s of jobs.values()) {
        // finished 空态（任务不存在/过期按已完成收的既有口径）按 1 步完成计
        const steps = Math.max(s.done, s.failed, s.finished ? 1 : 0);
        done += Math.max(steps - s.failed, 0);
        failed += s.failed;
      }
      progress.done = done;
      progress.failed = failed;
      progress.skipped = base.skipped;
    }
    const pending = new Set(); // 已登记、未收尾的任务
    let issueDone = false;
    let netFails = 0;
    let aborted = ''; // 非空=提前停止轮询的原因，此时不报成功
    const startedAt = Date.now();
    // SYNC_POLL_LIMIT_MS 聚合轮询总时限（30 分钟；防止后台任务长期不落终态时无限等待）。
    const SYNC_POLL_LIMIT_MS = 30 * 60 * 1000;
    // 并发发起（未绑定 40020 / 登录过期 40021 计跳过，其余计失败，均不中断整体）
    const issue = (async () => {
      await Promise.all(
        pairs.map(async (p) => {
          try {
            const d = await syncPull({ member_id: p.memberId, date: p.date });
            if (d && d.jobId) pending.add(d.jobId);
            else {
              base.failed += 1;
              if (!firstMsg) firstMsg = '同步任务发起成功但未返回任务号';
              applyProgress();
            }
          } catch (e) {
            if (!firstMsg) firstMsg = e.message || '';
            if (e.code === 40020 || e.code === 40021) {
              base.skipped += 1;
              base.done += 1; // 计入完成面，进度条不空转
            } else {
              base.failed += 1;
            }
            applyProgress();
          }
        })
      );
      issueDone = true;
    })();
    // 聚合轮询：每轮读取各任务 done/errors 更新快照并重算进度，发起与收尾全程并行
    const poll = (async () => {
      while (!aborted && !disposed && (!issueDone || pending.size)) {
        if (pollExpired(startedAt, Date.now(), SYNC_POLL_LIMIT_MS)) {
          aborted = '进度查询总时长超过 30 分钟，已停止轮询';
          pending.clear();
          break;
        }
        for (const id of [...pending]) {
          try {
            const job = await fetchSyncProgress(id);
            netFails = 0;
            const failed = Math.max(Number(job && (job.errors ?? job.failed)) || 0, 0);
            const done = Math.max(Number(job && job.done) || 0, 0);
            const finished = !job || !!job.finished;
            jobs.set(id, { done, failed, finished });
            if (finished) pending.delete(id);
            if (job && job.failMsg && !firstMsg) firstMsg = job.failMsg;
          } catch (e) {
            netFails += 1;
            if (e.message && !firstMsg) firstMsg = e.message;
            // 任务不存在按已完成收（同口径）；连续网络失败兜底停止轮询，不悄悄报成功
            if (e.code === 40400) {
              const prev = jobs.get(id);
              jobs.set(
                id,
                prev ? { ...prev, finished: true } : { done: 1, failed: 0, finished: true }
              );
              pending.delete(id);
            } else if (netFails > 20) {
              aborted = '进度查询连续失败，已停止轮询';
              pending.clear();
            }
          }
        }
        applyProgress();
        if (!aborted && !disposed && (!issueDone || pending.size)) {
          await new Promise((r) => setTimeout(r, pollDelay(netFails, { baseMs: 1500, maxMs: 10000 })));
        }
      }
      if (disposed && !aborted) aborted = '页面已关闭，已停止轮询';
    })();
    await Promise.all([issue, poll]);
    if (disposed) return; // 离页后不再弹提示 / 回到父视图
    const success = Math.max(progress.total - progress.failed - progress.skipped, 0);
    const failNote = progress.failed && firstMsg ? `，首条失败：${firstMsg}` : '';
    if (aborted) {
      error.value = `${aborted}（${firstMsg || '网络异常'}）；进度 ${progress.done + progress.failed}/${progress.total}，未收尾任务可能仍在后台执行，请稍后在「同步任务」核查流水确认`;
    } else {
      resultText.value = `拉取完成：共 ${progress.total} 人日，成功 ${success}、失败 ${progress.failed}、跳过 ${progress.skipped}${failNote}（明细见「同步任务」核查流水）`;
      MessagePlugin.success('拉取完成');
    }
    emit('done');
  } catch (e) {
    error.value = e.message || '拉取失败';
  } finally {
    running.value = false;
  }
}

// open 显隐镜像：请求关闭本地先播退出动画（父级 v-model:open 跟随），关闭不瞬间消失。
const open = ref(props.open);
watch(
  () => props.open,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);

// onEsc Esc 关闭（document 捕获监听，焦点不在弹层内也能关）。
function onEsc(e) {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  close();
}
watch(rendered, (r) => {
  if (r) document.addEventListener('keydown', onEsc, true);
  else document.removeEventListener('keydown', onEsc, true);
});
onBeforeUnmount(() => {
  disposed = true;
  document.removeEventListener('keydown', onEsc, true);
});

function close() {
  if (!open.value) return;
  open.value = false;
  emit('update:open', false);
}
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

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
  background: var(--tk-overlay, rgba(29, 33, 41, 0.45));
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
/* 底部按钮（关闭 / 发起拉取）：显式锁定尺寸、padding 与 white-space——组件内 * 复位清掉按钮内边距、
   TDesign 按钮字号经 var() 回退为继承值，窄行内容易把文案挤出按钮边界；两枚按钮同口径 */
.wl-modal-foot .t-button {
  flex: none;
  height: 32px;
  padding: 0 16px;
  font-size: 14px;
  line-height: 22px;
  white-space: nowrap;
}
.wl-range {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: var(--tk-card, #ffffff);
}
/* ===== 旧版弹层骨架覆写（.mask/.modal/.m-head/.m-title/.m-x） ===== */
.wl-modal-mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: var(--tk-overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.wl-modal {
  background: var(--tk-card, #ffffff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 520px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden auto;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
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
  background: var(--tk-card, #ffffff);
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
  color: var(--tk-on-brand, #ffffff);
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
