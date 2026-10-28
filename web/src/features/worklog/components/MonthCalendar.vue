<template>
  <!-- 月历卡内容（旧版 aside.cal）：窄屏默认摘要可展开，常规完整月历 -->
  <div class="cal" :class="{ open: expanded }">
    <div class="cal-summary">
      <span class="num">{{ summaryText }}</span>
      <button
        type="button"
        :aria-expanded="expanded ? 'true' : 'false'"
        @click="expanded = !expanded"
      >
        {{ expanded ? '收起月历' : '展开月历' }}
      </button>
    </div>
    <div class="cal-collapse">
      <div class="cal-h">
        <b class="num">{{ ymText }}</b>
        <button type="button" class="cal-today" @click="goToday">今天</button>
        <div class="cal-nav">
          <button type="button" title="上一月" @click="shiftMonth(-1)">
            <WlIcon name="chevron-left" :size="12" />
          </button>
          <button type="button" title="下一月" @click="shiftMonth(1)">
            <WlIcon name="chevron-right" :size="12" />
          </button>
        </div>
      </div>
      <div class="cal-grid">
        <span v-for="w in weekDays" :key="w" class="cal-wd">{{ w }}</span>
        <span
          v-for="c in cells"
          :key="c.key"
          class="day"
          :class="c.cls"
          @click="c.date && select(c.date)"
        >
          {{ c.day || '' }}
          <span class="ds">
            <i v-if="c.dot" class="dt" :class="c.dot"></i>
          </span>
        </span>
      </div>
      <div class="cal-lg">
        <span><i class="dt dt-ok"></i>已验证</span>
        <span><i class="dt dt-warn"></i>有备注</span>
        <span><i class="dt dt-bad"></i>待核实</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import WlIcon from './WlIcon.vue';

// 月历（旧版 .cal 结构还原）：34px 圆形日格 + 状态色点，今天品牌蓝底白字、选中 2px 蓝环；
// 窄屏默认只显示日期摘要，手动展开完整月历（≤1024px 由本组件 media query 呈现摘要条）。
const props = defineProps({
  month: { type: String, required: true },
  selected: { type: String, default: '' },
  statusMap: { type: Object, default: () => ({}) },
});
const emit = defineEmits(['update:month', 'update:selected']);

const weekDays = ['一', '二', '三', '四', '五', '六', '日'];
const expanded = ref(false);

// today 今日（YYYY-MM-DD）。
const today = new Date().toISOString().slice(0, 10);

// ymText 年月标题。
const ymText = computed(() => {
  const [y, m] = props.month.split('-').map(Number);
  return `${y} 年 ${m} 月`;
});

// summaryText 窄屏摘要行。
const summaryText = computed(() =>
  props.selected ? props.selected.replace(/-/g, '.') : '选择日期'
);

// cells 月历格（周一起始；前后邻月灰字补齐）。
const cells = computed(() => {
  const [y, m] = props.month.split('-').map(Number);
  const first = new Date(y, m - 1, 1);
  const daysInMonth = new Date(y, m, 0).getDate();
  const prevDays = new Date(y, m - 1, 0).getDate();
  const lead = (first.getDay() + 6) % 7; // 周一起始
  const out = [];
  for (let i = lead - 1; i >= 0; i -= 1) {
    out.push({ key: `p${i}`, day: prevDays - i, date: '', cls: 'dim', dot: '' });
  }
  for (let d = 1; d <= daysInMonth; d += 1) {
    const date = `${props.month}-${String(d).padStart(2, '0')}`;
    const st = props.statusMap[date];
    const cls = [];
    if (date === today) cls.push('today');
    if (date === props.selected) cls.push('sel');
    const dot =
      st === 'passed' ? 'dt-ok' : st === 'failed' ? 'dt-bad' : st === 'remark' ? 'dt-warn' : '';
    out.push({ key: date, day: d, date, cls, dot });
  }
  const tail = (7 - ((lead + daysInMonth) % 7)) % 7;
  for (let i = 1; i <= tail; i += 1) {
    out.push({ key: `t${i}`, day: i, date: '', cls: 'dim', dot: '' });
  }
  return out;
});

// shiftMonth 月份平移（选中日同步到新月同日或月末）。
function shiftMonth(delta) {
  const [y, m] = props.month.split('-').map(Number);
  const d = new Date(y, m - 1 + delta, 1);
  const ym = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
  emit('update:month', ym);
  const day = props.selected ? props.selected.slice(8, 10) : '01';
  const maxDay = new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate();
  const want = Math.min(Number(day), maxDay);
  emit('update:selected', `${ym}-${String(want).padStart(2, '0')}`);
}

// goToday 回到今天所在月并选中今天。
function goToday() {
  emit('update:month', today.slice(0, 7));
  emit('update:selected', today);
}

// select 选中日期。
function select(date) {
  emit('update:selected', date);
}
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

.cal {
  padding: 0;
}
.cal-summary {
  display: none;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.cal-summary > span {
  color: var(--tk-ink);
  font-size: 12px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.cal-summary button {
  flex: none;
  padding: 5px 8px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card, #ffffff);
  color: var(--tk-brand);
  font-size: 11px;
  cursor: pointer;
}
.cal-h {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.cal-h b {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 1px;
  font-variant-numeric: tabular-nums;
}
/* 今天 / 上下月切换：同高 24px 方钮，行内垂直居中对齐 */
.cal-today {
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--tk-line);
  background: var(--tk-card, #ffffff);
  color: var(--tk-brand);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.5px;
  cursor: pointer;
  padding: 0 9px;
  border-radius: 6px;
  transition:
    border-color 0.15s,
    background 0.15s;
}
.cal-today:hover {
  border-color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.cal-nav {
  margin-left: auto;
  display: flex;
  gap: 6px;
  align-items: center;
}
.cal-nav button {
  width: 24px;
  height: 24px;
  flex: none;
  border: 1px solid var(--tk-line);
  border-radius: 6px;
  background: var(--tk-card, #ffffff);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--tk-sub);
  cursor: pointer;
  padding: 0;
  transition:
    color 0.15s,
    border-color 0.15s;
}
.cal-nav button:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
.cal-grid {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  gap: 2px;
  justify-items: center;
  overflow: hidden; /* 兜底：任何宽度下不出横向滚动 */
}
.cal-wd {
  text-align: center;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--tk-dash);
  padding: 4px 0 8px;
  letter-spacing: 2px;
}
.day {
  position: relative;
  width: 34px;
  max-width: 100%; /* 窄容器随列宽收缩，保持 7 列均匀且不溢出 */
  aspect-ratio: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  font-size: 12.5px;
  font-weight: 600;
  cursor: pointer;
  border: 2px solid transparent;
  font-variant-numeric: tabular-nums;
  transition:
    background 0.15s,
    border-color 0.15s,
    color 0.15s;
}
.day:hover {
  background: rgba(29, 33, 41, 0.05);
}
.day.dim {
  color: var(--tk-dash);
  font-weight: 400;
  cursor: default;
}
.day.dim:hover {
  background: transparent;
}
.day .ds {
  display: flex;
  gap: 2px;
  height: 4px;
  margin-top: 2px;
}
.dt {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  display: inline-block;
}
.dt-ok {
  background: var(--tk-green);
}
.dt-bad {
  background: var(--tk-red);
}
.dt-warn {
  background: var(--tk-yellow);
}
.day.today {
  background: var(--tk-brand);
  color: var(--tk-on-brand, #ffffff);
  font-weight: 700;
}
.day.today .dt {
  background: var(--tk-on-brand, #ffffff);
  opacity: 0.85;
}
.day.sel {
  border-color: var(--tk-brand);
}
.cal-lg {
  display: flex;
  gap: 12px;
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--tk-hair, rgba(29, 33, 41, 0.06));
  font-size: 10.5px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 1px;
}
.cal-lg span {
  display: flex;
  align-items: center;
  gap: 6px;
}
.cal-lg .dt {
  width: 7px;
  height: 7px;
}

/* 窄屏（≤1024px）：默认摘要，展开后才占完整月历高度 */
@media (max-width: 1024px) {
  .cal-summary {
    display: flex;
  }
  .cal-collapse {
    display: none;
    margin-top: 12px;
  }
  .cal.open .cal-collapse {
    display: block;
    animation: calIn 0.28s var(--tk-ease);
  }
}
@keyframes calIn {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  /* 动效降级（同旧版 theme.css）：展开月历不播放入场 */
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
  .cal.open .cal-collapse {
    animation: none !important;
  }
}
</style>
