<template>
  <aside class="sidebar">
    <label class="sidebar-search">
      <ShadeIcon name="search" :size="14" />
      <input v-model="query" type="search" placeholder="搜索工具" aria-label="搜索工具" />
    </label>
    <div class="sidebar-scroll">
      <template v-for="group in visibleGroups" :key="group.id">
        <button
          v-if="group.id === 'developer'"
          class="nav-group-caption nav-group-toggle"
          :title="
            searching ? '搜索时自动展开' : expandedDeveloper ? '收起开发者工具' : '展开开发者工具'
          "
          @click="!searching && $emit('toggleDeveloper')"
        >
          <span>开发者工具{{ searching ? '' : `（${expandedDeveloper ? '收起' : '展开'}）` }}</span>
          <span class="group-arrow">{{ searching || expandedDeveloper ? '⌄' : '›' }}</span>
        </button>
        <div v-else class="nav-group-caption">{{ group.caption }}</div>
        <button
          v-for="item in group.items"
          :key="item.id"
          class="nav-item"
          :class="{ active: current === item.id }"
          :aria-current="current === item.id ? 'page' : undefined"
          @click="$emit('navigate', item.id)"
        >
          <ShadeIcon :name="item.icon" :size="15" />
          <span class="nav-label">{{ item.title }}</span>
          <span v-if="favorites.includes(item.id)" class="nav-fav" title="已收藏">★</span>
        </button>
      </template>
      <div v-if="query.trim() && !visibleGroups.length" class="nav-empty">没有匹配的工具</div>
    </div>
    <div class="sidebar-bottom">
      <button
        class="nav-item"
        :class="{ active: current === 'settings' }"
        :aria-current="current === 'settings' ? 'page' : undefined"
        @click="$emit('navigate', 'settings')"
      >
        <ShadeIcon name="setting" :size="15" />
        <span class="nav-label">设置</span>
      </button>
    </div>
  </aside>
</template>

<script setup>
import { computed, ref } from 'vue';
import ShadeIcon from './ShadeIcon.vue';
import { navGroups } from '../lib/navigation.mjs';

const props = defineProps({
  current: { type: String, default: 'home' },
  expandedDeveloper: { type: Boolean, default: false },
  favorites: { type: Array, default: () => [] },
});
defineEmits(['navigate', 'toggleDeveloper']);

const query = ref('');
const searching = computed(() => query.value.trim() !== '');
const visibleGroups = computed(() => {
  const all = navGroups({ expandedDeveloper: true, query: query.value });
  if (!searching.value) {
    return all.map((group) =>
      group.id === 'developer'
        ? { ...group, items: props.expandedDeveloper ? group.items : [] }
        : group
    );
  }
  const developerMatches = all.some((group) => group.id === 'developer' && group.items.length > 0);
  return developerMatches ? all : all.filter((group) => group.id !== 'developer');
});
</script>
