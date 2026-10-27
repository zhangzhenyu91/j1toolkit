<template>
  <div class="wl-cal">
    <div class="wl-cal-head">
      <button type="button" class="wl-cal-nav" aria-label="上个月" @click="shiftMonth(-1)">
        ‹
      </button>
      <span class="wl-cal-title num">{{ month }}</span>
      <button type="button" class="wl-cal-nav" aria-label="下个月" @click="shiftMonth(1)">›</button>
    </div>
    <div class="wl-cal-week">
      <span v-for="w in weekDays" :key="w">{{ w }}</span>
    </div>
    <div class="wl-cal-grid">
      <button
        v-for="cell in cells"
        :key="cell.key"
        type="button"
        class="wl-cal-day num"
        :class="{
          empty: !cell.day,
          selected: cell.date === selected,
          today: cell.date === today,
          [`st-${statusMap[cell.date] || ''}`]: true,
        }"
        :disabled="!cell.day"
        @click="cell.day && select(cell.date)"
      >
        <span class="wl-cal-day-no">{{ cell.day || '' }}</span>
        <span v-if="statusMap[cell.date]" class="wl-cal-dot"></span>
      </button>
    </div>
    <div class="wl-cal-legend">
      <span class="dot-tag green"><span class="dot"></span>已验证</span>
      <span class="dot-tag orange"><span class="dot"></span>有备注</span>
      <span class="dot-tag red"><span class="dot"></span>待核实</span>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';

// 月历组件：按 day-status 着色（绿=已验证 / 橙=有备注 / 红=待核实），点击日期选中。
// props：month=YYYY-MM、selected=YYYY-MM-DD、statusMap={日期:passed|remark|failed}。
const props = defineProps({
  month: { type: String, required: true },
  selected: { type: String, default: '' },
  statusMap: { type: Object, default: () => ({}) },
});
const emit = defineEmits(['update:month', 'update:selected']);

const weekDays = ['一', '二', '三', '四', '五', '六', '日'];

// today 今日日期（YYYY-MM-DD）。
const today = new Date().toISOString().slice(0, 10);

// cells 月历格子（周一起始；前后补空格）。
const cells = computed(() => {
  const [y, m] = props.month.split('-').map(Number);
  const first = new Date(y, m - 1, 1);
  const daysInMonth = new Date(y, m, 0).getDate();
  const lead = (first.getDay() + 6) % 7; // 周一起始
  const out = [];
  for (let i = 0; i < lead; i += 1) out.push({ key: `e${i}`, day: 0, date: '' });
  for (let d = 1; d <= daysInMonth; d += 1) {
    const date = `${props.month}-${String(d).padStart(2, '0')}`;
    out.push({ key: date, day: d, date });
  }
  while (out.length % 7 !== 0) out.push({ key: `t${out.length}`, day: 0, date: '' });
  return out;
});

// shiftMonth 月份平移（保持选中日同步到新月同日或月末）。
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

// select 选中日期。
function select(date) {
  emit('update:selected', date);
}
</script>

<style scoped>
.wl-cal {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.wl-cal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.wl-cal-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-ink);
}
.wl-cal-nav {
  width: 28px;
  height: 28px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-sub);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}
.wl-cal-nav:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
.wl-cal-week {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
  text-align: center;
  font-size: 12px;
  color: var(--tk-ph);
}
.wl-cal-grid {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
  gap: 4px;
}
.wl-cal-day {
  height: 34px;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--tk-ink);
  font-size: 13px;
  cursor: pointer;
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background 0.16s var(--tk-ease);
}
.wl-cal-day.empty {
  cursor: default;
}
.wl-cal-day:hover:not(.empty) {
  background: var(--tk-fill);
}
.wl-cal-day.selected {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-weight: 600;
}
.wl-cal-day.today .wl-cal-day-no {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.wl-cal-dot {
  position: absolute;
  bottom: 3px;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--tk-dash);
}
.wl-cal-day.st-passed .wl-cal-dot {
  background: var(--tk-green);
}
.wl-cal-day.st-remark .wl-cal-dot {
  background: var(--tk-yellow);
}
.wl-cal-day.st-failed .wl-cal-dot {
  background: var(--tk-red);
}
.wl-cal-legend {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
</style>
