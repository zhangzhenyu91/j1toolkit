<template>
  <!-- 旧版线性图标（与 server/public/assets/icons.js 同一套 SVG 路径，见 features/core/icons.js）；
       name 与 tdesign-miniprogram 图标同名，sys_app.icon 可直用，未知名称回退 app -->
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    :stroke="color"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    v-html="body"
  ></svg>
</template>

<script setup>
import { computed } from 'vue';
import { iconInner } from './icons.js';

const props = defineProps({
  // 图标名（tdesign 同名，未知名称回退 app）
  name: { type: String, default: 'app' },
  // 边长像素（与旧 Shade.icon 一致）
  size: { type: [Number, String], default: 24 },
  // 描边颜色（默认跟随文字色）
  color: { type: String, default: 'currentColor' },
});

const body = computed(() => iconInner(props.name));
</script>
