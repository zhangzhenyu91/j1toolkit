<template>
  <!-- 旧版导出弹层（FAB 点击）：范围单选（台账视图「当前线路 / 全部线路」；线路视图「全部线路 / 选择线路」下拉即选中）
       +「按线路分表」勾选（仅全部线路范围可勾，其余禁用并灰显说明）→ 预览（/export/preview）/ 下载（xlsx + saveBlob） -->
  <t-dialog
    v-model:visible="visible"
    header="导出统计表"
    :close-on-overlay-click="false"
    :confirm-btn="null"
    :cancel-btn="null"
  >
    <div class="ex-form">
      <label class="f-lab">导出范围</label>
      <template v-if="showCurrent">
        <label
          class="ex-opt"
          :class="{ dis: !curLineName }"
          :title="curLineName ? '' : '未匹配线路聚合多条线路，不支持单线路导出'"
        >
          <input
            type="radio"
            name="exRange"
            value="line"
            :checked="range === 'line'"
            :disabled="!curLineName"
            @change="onRange('line')"
          />
          当前线路（{{ curTitle }}）
        </label>
        <label class="ex-opt">
          <input
            type="radio"
            name="exRange"
            value="all"
            :checked="range === 'all'"
            @change="onRange('all')"
          />
          全部线路
        </label>
      </template>
      <template v-else>
        <label class="ex-opt">
          <input
            type="radio"
            name="exRange"
            value="all"
            :checked="range === 'all'"
            @change="onRange('all')"
          />
          全部线路
        </label>
        <label class="ex-opt" :class="{ dis: !lines.length }">
          <input
            type="radio"
            name="exRange"
            value="pick"
            :checked="range === 'pick'"
            :disabled="!lines.length"
            @change="onRange('pick')"
          />
          选择线路
        </label>
        <select class="f-sel" :disabled="range !== 'pick'" @change="onPickLine">
          <option value="" disabled :selected="!pickedLine">请选择线路</option>
          <option
            v-for="l in lines"
            :key="l.line_name"
            :value="l.line_name"
            :selected="pickedLine === l.line_name"
          >
            {{ fmtKv(l.voltage_level) }} {{ l.line_name }}
          </option>
        </select>
      </template>

      <label class="f-lab">分表</label>
      <label class="ex-opt" :class="{ dis: range !== 'all' }">
        <input
          type="checkbox"
          :checked="split"
          :disabled="range !== 'all' || busy"
          @change="onSplit($event.target.checked)"
        />
        按线路分表（每条线路一个工作表，表名 = 电压等级 + 线路名称）
      </label>
      <div class="f-hint" :style="{ opacity: range !== 'all' ? '.55' : '' }">
        仅「全部线路」范围可分表；导出范围非全部线路时此项不可用。
      </div>

      <div class="m-foot">
        <button class="btn btn-sm" type="button" :disabled="busy" @click="onPreview">
          <svg viewBox="0 0 15 15" width="15" height="15">
            <path
              d="M3 1.5h6l3 3v9H3v-12zM9 1.5v3h3"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linejoin="round"
            />
          </svg>
          {{ previewing ? '预览中…' : '预览' }}
        </button>
        <button class="btn btn-sm btn-primary" type="button" :disabled="busy" @click="onDownload">
          <svg viewBox="0 0 15 15" width="15" height="15">
            <path
              d="M7.5 2v7m0 0L5 6.5M7.5 9L10 6.5M2.5 12.5h10"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          {{ downloading ? '导出中…' : '下载' }}
        </button>
      </div>
    </div>
  </t-dialog>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { fmtKv } from './util.js';

const props = defineProps({
  visible: { type: Boolean, default: false },
  lines: { type: Array, default: () => [] }, // [{voltage_level,line_name}]
  showCurrent: { type: Boolean, default: false }, // 台账视图（含当前线路项）
  curLineName: { type: String, default: '' }, // 当前线路名（'__unmatched__' 伪组传 ''）
});
const emit = defineEmits(['update:visible', 'download', 'preview']);

const visible = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) {
      // 台账视图默认「当前线路」（未匹配伪组不可单线路导出时回退全部）；线路视图默认「全部线路」
      range.value = props.showCurrent && props.curLineName ? 'line' : 'all';
      pickedLine.value = '';
      split.value = false;
      downloading.value = false;
      previewing.value = false;
    }
  }
);
watch(visible, (v) => emit('update:visible', v));

const range = ref('all'); // all 全部线路 | line 当前线路 | pick 选择线路
const pickedLine = ref('');
const split = ref(false);
const downloading = ref(false);
const previewing = ref(false);
const busy = computed(() => downloading.value || previewing.value);

// curTitle 当前线路标题（电压等级 + 线路名，旧版「当前线路（…）」口径）
const curTitle = computed(() => {
  const l = (props.lines || []).find((x) => x.line_name === props.curLineName);
  return l ? `${fmtKv(l.voltage_level)} ${l.line_name}` : props.curLineName || '—';
});

function onRange(v) {
  if (v === 'line' && !props.curLineName && props.showCurrent) return;
  if (v === 'pick' && !props.lines.length) return;
  range.value = v;
  // 分表勾选随范围联动：非全部线路 → 摘勾（禁用由 :disabled 表达）
  if (v !== 'all') split.value = false;
}

function onPickLine(e) {
  pickedLine.value = e.target.value;
  if (pickedLine.value) {
    // 下拉选线即视为选中「选择线路」
    range.value = 'pick';
    split.value = false;
  }
}

function onSplit(checked) {
  if (range.value !== 'all') return;
  split.value = !!checked;
}

// buildQuery 拼导出 query：分线路 line=；全部线路 + 勾选分表 → split=1。
function buildQuery() {
  if (range.value === 'line') {
    if (!props.curLineName) return null;
    return `?line=${encodeURIComponent(props.curLineName)}`;
  }
  if (range.value === 'pick') {
    if (!pickedLine.value) return null;
    return `?line=${encodeURIComponent(pickedLine.value)}`;
  }
  return split.value ? '?split=1' : '';
}

function onDownload() {
  const query = buildQuery();
  if (query === null) {
    emit('download', { error: '请选择线路' });
    return;
  }
  downloading.value = true;
  emit('download', {
    query,
    done: () => {
      downloading.value = false;
      visible.value = false;
    },
    fail: () => {
      downloading.value = false;
    },
  });
}

// onPreview 在线预览：由视图取 /export/preview 地址打开（OO 承载页或微软查看器）。
function onPreview() {
  const query = buildQuery();
  if (query === null) {
    emit('preview', { error: '请选择线路' });
    return;
  }
  previewing.value = true;
  emit('preview', {
    query,
    done: () => {
      previewing.value = false;
      visible.value = false;
    },
    fail: () => {
      previewing.value = false;
    },
  });
}
</script>

<style scoped>
.ex-form {
  display: flex;
  flex-direction: column;
}
/* 弹层表单标签（旧版 f-lab 口径） */
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-lab:first-of-type {
  margin-top: 2px;
}
/* 范围单选 / 分表勾选行 */
.ex-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  font-size: 13px;
  color: var(--tk-ink);
  letter-spacing: 0.5px;
  cursor: pointer;
}
.ex-opt input {
  width: 15px;
  height: 15px;
  accent-color: var(--tk-brand);
  cursor: pointer;
  flex: none;
}
.ex-opt.dis {
  color: var(--tk-ph);
  cursor: not-allowed;
}
.ex-opt.dis input {
  cursor: not-allowed;
}
.f-sel {
  width: 100%;
  height: 40px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  margin-top: 2px;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.f-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
}
.f-sel:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.f-hint {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 旧式按钮 ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  font-family: inherit;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn:active:not([disabled]) {
  transform: translateY(1px);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}
</style>
