<template>
  <div v-if="visible" class="palette-overlay" @mousedown.self="$emit('close')">
    <section class="palette" role="dialog" aria-modal="true" aria-label="工具搜索">
      <div class="palette-head">
        <strong>工具搜索</strong>
        <span class="muted">上下键选择 · Enter 确认 · Esc 关闭</span>
      </div>
      <label class="palette-search">
        <ShadeIcon name="search" :size="15" />
        <input
          ref="inputEl"
          v-model="query"
          type="text"
          placeholder="搜索工具、首页、执行历史或设置"
          @keydown.down.prevent="move(1)"
          @keydown.up.prevent="move(-1)"
          @keydown.enter.prevent="activate(selected)"
          @keydown.esc.prevent="$emit('close')"
        />
      </label>
      <div v-if="items.length" class="palette-list" role="listbox">
        <button
          v-for="(item, index) in items"
          :key="item.kind + ':' + item.pageID"
          class="palette-item"
          :class="{ active: index === selected }"
          role="option"
          :aria-selected="index === selected"
          @mouseenter="selected = index"
          @click="activate(index)"
        >
          <ShadeIcon :name="item.icon" :size="15" />
          <span class="palette-item-text">
            <span class="palette-title">{{ item.title }}</span>
            <span class="palette-desc">{{ item.desc }}</span>
          </span>
          <span v-if="item.kind === 'toggleFavorite'" class="palette-kind">收藏</span>
        </button>
      </div>
      <div v-else class="palette-empty">没有匹配的工具或页面</div>
    </section>
  </div>
</template>

<script setup>
import { computed, nextTick, ref, watch } from 'vue';
import ShadeIcon from './ShadeIcon.vue';
import { commandPaletteItems, filterCommandItems } from '../lib/navigation.mjs';

const props = defineProps({
  visible: { type: Boolean, default: false },
  favorites: { type: Array, default: () => [] },
});
const emit = defineEmits(['close', 'navigate', 'toggleFavorite']);

const query = ref('');
const selected = ref(0);
const inputEl = ref(null);

const items = computed(() =>
  filterCommandItems(commandPaletteItems({ favorites: props.favorites }), query.value)
);

function move(delta) {
  if (!items.value.length) return;
  selected.value = Math.min(items.value.length - 1, Math.max(0, selected.value + delta));
}

function activate(index) {
  const item = items.value[index];
  if (!item) return;
  if (item.kind === 'navigate') {
    emit('navigate', item.pageID);
    emit('close');
  } else {
    emit('toggleFavorite', item.pageID);
    selected.value = Math.min(index, Math.max(0, items.value.length - 1));
  }
}

watch(
  () => props.visible,
  async (visible) => {
    if (!visible) return;
    query.value = '';
    selected.value = 0;
    await nextTick();
    inputEl.value && inputEl.value.focus();
  }
);

watch(query, () => {
  selected.value = 0;
});
</script>
