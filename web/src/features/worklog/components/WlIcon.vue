<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    v-html="body"
  ></svg>
</template>

<script setup>
import { computed } from 'vue';

// 线性图标：路径与旧版 server/public/assets/icons.js 同源（viewBox 24、stroke 1.7、圆角端点），
// 另收录 worklog.html 内联的 车辆 / 播放 / 文档 / 相机 等补充图标。
const props = defineProps({
  name: { type: String, required: true },
  size: { type: Number, default: 16 },
});

const ICONS = {
  add: '<path d="M12 5v14M5 12h14"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  delete:
    '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
  upload: '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  time: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3.5 2"/>',
  location:
    '<path d="M12 21s-6.5-5.3-6.5-10.2A6.5 6.5 0 0 1 12 4.3a6.5 6.5 0 0 1 6.5 6.5C18.5 15.7 12 21 12 21z"/><circle cx="12" cy="10.8" r="2.3"/>',
  user: '<circle cx="12" cy="8" r="3.6"/><path d="M5.6 19.4a6.4 6.4 0 0 1 12.8 0"/>',
  'lock-on':
    '<rect x="4.8" y="10.5" width="14.4" height="9.5" rx="2.5"/><path d="M8.2 10.5V8a3.8 3.8 0 0 1 7.6 0v2.5"/><circle cx="12" cy="15.2" r="1.1" fill="currentColor" stroke="none"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  'chevron-left': '<path d="M14.5 6.5L9 12l5.5 5.5"/>',
  'chevron-down': '<path d="M6.5 9.5l5.5 5.5 5.5-5.5"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8.5z"/><path d="M13.5 3v5.5H19"/>',
  edit: '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
  shield:
    '<path d="M12 3.2l6.8 2.5v5.2c0 4.5-2.9 7.7-6.8 9.4-3.9-1.7-6.8-4.9-6.8-9.4V5.7z"/><path d="M9 11.7l2.2 2.2 3.8-3.9"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
  money:
    '<rect x="3" y="6.5" width="18" height="11" rx="2"/><circle cx="12" cy="12" r="2.4"/><path d="M6.3 9.7v.1M17.7 14.3v.1"/>',
  car: '<path d="M4.5 15.5l1.6-5A2 2 0 0 1 8 9h8a2 2 0 0 1 1.9 1.5l1.6 5"/><rect x="3.5" y="15.5" width="17" height="4" rx="1.5"/><circle cx="7.5" cy="19.5" r="1.4"/><circle cx="16.5" cy="19.5" r="1.4"/>',
  play: '<path d="M10 8.5l6 3.5-6 3.5z" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="10"/>',
  camera:
    '<path d="M4 8.5h2.6l1.7-2.6h7.4l1.7 2.6H20a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 20 20.5H4A1.5 1.5 0 0 1 2.5 19v-9A1.5 1.5 0 0 1 4 8.5z"/><circle cx="12" cy="13.8" r="3.6"/>',
  image:
    '<rect x="3.5" y="5" width="17" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M21 15.5l-4.5-4.5L8 19.5"/>',
};

const body = computed(() => ICONS[props.name] || ICONS.image);
</script>
