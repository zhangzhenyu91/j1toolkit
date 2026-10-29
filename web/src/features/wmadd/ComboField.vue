<template>
  <div class="combo">
    <input
      class="f-sel combo-in"
      type="text"
      autocomplete="off"
      spellcheck="false"
      :value="text"
      :placeholder="placeholder"
      :disabled="disabled"
      @focus="onFocus"
      @input="onInput"
      @blur="onBlur"
      @keydown="onKeydown"
    />
    <span class="combo-chev" @mousedown.prevent="onChev">
      <svg
        viewBox="0 0 24 24"
        width="13"
        height="13"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M5.5 9.5 12 16l6.5-6.5" />
      </svg>
    </span>
    <div class="combo-list" :class="{ open: isOpen }">
      <div class="combo-list-in">
        <div v-for="(it, i) in filtered" :key="i" class="combo-opt" @mousedown.prevent="pick(it)">
          {{ it.label }}
        </div>
        <div v-if="!filtered.length" class="combo-none">无匹配项</div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 可筛选下拉（移植 server/public/wmadd.html makeCombo，杆塔坐标级联用）：
// 输入关键字实时过滤（子串匹配）；点击输入框/箭头展开，箭头恢复完整列表（同小程序）；
// 选中回填并触发 pick；输入与已选不一致时触发 edit 一次（级联清空下级）。
import { computed, ref, watch } from 'vue';

const props = defineProps({
  // 选项 [{label, ...}]（杆塔项另带 no/lng/lat）；引用变化即视为重置（同旧页 setItems）
  items: { type: Array, default: () => [] },
  placeholder: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
});
const emit = defineEmits(['pick', 'edit']);

const text = ref('');
const cur = ref(null); // 已选项 label（输入与之不一致即视为改选）
const isOpen = ref(false);
const showAll = ref(false); // 展开时是否恢复完整列表（已选值再展开 / 箭头点击）

const filtered = computed(() => {
  if (showAll.value) return props.items;
  const k = text.value.trim();
  if (!k) return props.items;
  return props.items.filter((it) => String(it.label).indexOf(k) !== -1);
});

// 选项集变化 = 上级级联重置（同旧页 setItems：清空输入与已选、收起）
watch(
  () => props.items,
  () => {
    cur.value = null;
    text.value = '';
    isOpen.value = false;
    showAll.value = false;
  }
);

function close() {
  isOpen.value = false;
}
function pick(it) {
  if (!it) return;
  cur.value = it.label;
  text.value = it.label;
  close();
  emit('pick', it);
}
function onFocus() {
  if (props.disabled) return;
  // 展开：当前值即已选值时恢复完整列表，否则按当前值过滤（同旧页）
  showAll.value = cur.value !== null && text.value === cur.value;
  isOpen.value = true;
}
function onInput(e) {
  const v = e.target.value;
  text.value = v;
  showAll.value = false;
  if (v === cur.value) return;
  if (cur.value !== null) {
    cur.value = null;
    emit('edit');
  }
  isOpen.value = true;
}
function onBlur() {
  setTimeout(close, 120);
}
function onKeydown(e) {
  if (e.key === 'Escape') {
    // 下拉展开时只收下拉（stopPropagation 不透传给外层弹层的 Esc 关闭），否则保持原失焦收起
    if (isOpen.value) e.stopPropagation();
    close();
    e.target.blur();
    return;
  }
  if (e.key === 'Enter' && isOpen.value && filtered.value.length) {
    pick(filtered.value[0]); // 回车快捷选中首个匹配项
  }
}
function onChev() {
  if (props.disabled) return;
  if (isOpen.value) {
    close();
    return;
  }
  showAll.value = true; // 箭头点击恢复完整列表（同小程序）
  isOpen.value = true;
}
</script>

<style scoped>
/* ===== 可筛选下拉：选项列表内联展开（同小程序），随弹层滚动不被裁剪 ===== */
.combo {
  position: relative;
}
.combo .f-sel {
  padding-right: 30px;
}
.combo-chev {
  position: absolute;
  right: 9px;
  top: 18px;
  transform: translateY(-50%);
  display: flex;
  color: var(--tk-ph);
  cursor: pointer;
}
.combo-list {
  display: grid;
  grid-template-rows: 0fr;
  overflow: hidden;
  margin-top: 0;
  opacity: 0;
  transition:
    grid-template-rows 0.2s var(--tk-ease),
    opacity 0.2s ease,
    margin-top 0.2s var(--tk-ease);
}
.combo-list.open {
  grid-template-rows: 1fr;
  opacity: 1;
  margin-top: 4px;
}
.combo-list-in {
  min-height: 0;
  max-height: 208px;
  overflow-y: auto;
  background: var(--tk-card, #ffffff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-hover);
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.combo-list-in::-webkit-scrollbar {
  width: 5px;
}
.combo-list-in::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.combo-list-in::-webkit-scrollbar-track {
  background: transparent;
}
.combo-opt {
  padding: 8px 12px;
  font-size: 12.5px;
  cursor: pointer;
  word-break: break-all;
}
.combo-opt:hover {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.combo-none {
  padding: 10px 12px;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}

@media (prefers-reduced-motion: reduce) {
  .combo-list {
    transition: none;
  }
}
</style>
