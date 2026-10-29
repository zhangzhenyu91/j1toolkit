<template>
  <!-- 旧版导出弹层（FAB 点击）：范围单选（台账视图「当前线路 / 全部线路」；线路视图「全部线路 / 选择线路」下拉即选中）
       +「按线路分表」勾选（仅全部线路范围可勾，其余禁用并灰显说明）→ 预览（/export/preview）/ 下载（xlsx + saveBlob）。
       自绘居中弹层壳（对照共享 Drawer/useOverlayMotion 口径）：遮罩淡入 + 面板上浮/缩放 200ms 进出场，
       Escape / 关闭按钮收起，点遮罩不关；打开期间锁页面滚动。 -->
  <Teleport to="body">
    <div v-if="rendered" class="ex-ctx" :class="phaseClass">
      <div class="ex-mask ov-mask"></div>
      <div class="ex-dialog ov-panel" role="dialog" aria-modal="true" aria-label="导出统计表">
        <div class="ex-head">
          <span class="ex-title">导出统计表</span>
          <button type="button" class="ex-x" aria-label="关闭" @click="close">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              aria-hidden="true"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="ex-body">
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
              <button
                class="btn btn-sm btn-primary"
                type="button"
                :disabled="busy"
                @click="onDownload"
              >
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
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useOverlayMotion } from '../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';
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

// 进出场编排（对照共享 Drawer/useOverlayMotion：遮罩淡入 + 面板上浮/缩放 200ms）。
const { rendered, phaseClass } = useOverlayMotion(visible);

// close 关闭（Escape / 关闭按钮）：置 false 播退出动画，父级经 update:visible 收起。
function close() {
  if (!visible.value) return;
  visible.value = false;
}

// 打开期间锁页面滚动 + Escape 收起（对齐 t-dialog preventScrollThrough / Esc 关闭口径）；关闭（含卸载）还原。
let savedOverflow = '';
let active = false;
watch(rendered, (r) => {
  if (r) {
    if (active) return;
    active = true;
    savedOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    document.addEventListener('keydown', onKeydown, true);
  } else if (active) {
    release();
  }
});

function release() {
  active = false;
  document.removeEventListener('keydown', onKeydown, true);
  document.body.style.overflow = savedOverflow;
}
onBeforeUnmount(release);

function onKeydown(e) {
  if (e.key === 'Escape') {
    e.stopPropagation();
    close();
  }
}

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
/* ===== 自绘居中弹层壳（对照共享 Drawer：遮罩 + 面板 + 标题行（含关闭）+ 滚动内容） ===== */
.ex-ctx {
  position: fixed;
  inset: 0;
  z-index: 320;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.ex-mask {
  position: absolute;
  inset: 0;
  background: var(--overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
}
.ex-dialog {
  position: relative;
  width: 480px;
  max-width: 94vw;
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-container, #fff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-card);
  overflow: hidden;
}
.ex-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 15px 18px 12px;
  border-bottom: 1px solid var(--tk-line);
}
.ex-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.ex-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--td-bg-color-container, #fff);
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
  transition:
    background 0.15s,
    color 0.15s;
}
.ex-x:hover {
  background: var(--tk-ink);
  color: var(--td-bg-color-container, #fff);
}
.ex-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 18px 16px;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.ex-body::-webkit-scrollbar {
  width: 5px;
}
.ex-body::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.ex-body::-webkit-scrollbar-track {
  background: transparent;
}
/* 深色滚动条（弹层随主题 token 翻转，滚动条色值单独按深色口径覆盖） */
@media (prefers-color-scheme: dark) {
  .ex-body {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .ex-body::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
}
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
