<template>
  <header class="page-head">
    <div class="page-head-text">
      <h1>{{ title }}</h1>
      <p>{{ desc }}</p>
    </div>
    <div class="page-head-actions">
      <span v-if="runningCount > 0" class="running-badge">
        <span class="dot" />{{ runningCount }} 个任务运行中
      </span>
      <button
        v-if="showFavorite"
        class="fav-btn"
        :class="{ active: favorite }"
        :title="favorite ? '取消收藏' : '收藏'"
        @click="$emit('toggleFavorite')"
      >
        <ShadeIcon :name="favorite ? 'star-filled' : 'star'" :size="16" />
        <span>{{ favorite ? '已收藏' : '收藏' }}</span>
      </button>
    </div>
  </header>
</template>

<script setup>
import ShadeIcon from './ShadeIcon.vue';

defineProps({
  title: { type: String, default: '' },
  desc: { type: String, default: '' },
  showFavorite: { type: Boolean, default: false },
  favorite: { type: Boolean, default: false },
  runningCount: { type: Number, default: 0 },
});
defineEmits(['toggleFavorite']);
</script>
