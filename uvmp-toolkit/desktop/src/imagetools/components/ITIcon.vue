<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="stroke"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
    v-html="inner"
  />
</template>

<script setup>
// 图片工具窗口自用的线性图标（同「政企蓝白」口径：fill none / 1.7 描边 / 圆角端点）。
// 与主客户端 ShadeIcon 同风格但不共享文件，避免两个窗口页面互相耦合。
import { computed } from 'vue';

const props = defineProps({
  name: { type: String, required: true },
  size: { type: [Number, String], default: 18 },
  stroke: { type: [Number, String], default: 1.7 },
});

const ICONS = {
  image:
    '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M4.5 17.5l4.6-4.4a1.6 1.6 0 0 1 2.2 0l2.2 2.1 1.6-1.5a1.6 1.6 0 0 1 2.2 0l2.2 2.1"/>',
  setting:
    '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>',
  prev: '<path d="M14.5 5.5L8 12l6.5 6.5"/>',
  next: '<path d="M9.5 5.5L16 12l-6.5 6.5"/>',
  'chevron-down': '<path d="M6 9.5l6 6 6-6"/>',
  'chevron-up': '<path d="M6 14.5l6-6 6 6"/>',
  'zoom-in': '<circle cx="11" cy="11" r="6.5"/><path d="M11 8.5v5M8.5 11h5M20.5 20.5L16 16"/>',
  'zoom-out': '<circle cx="11" cy="11" r="6.5"/><path d="M8.5 11h5M20.5 20.5L16 16"/>',
  fit: '<rect x="4" y="4.5" width="16" height="15" rx="2"/><path d="M8 9.5V8h2M16 9.5V8h-2M8 14.5V16h2M16 14.5V16h-2"/>',
  actual:
    '<rect x="4" y="5" width="16" height="14" rx="2"/><path d="M12 9v6M9.5 11.2L12 9l2.5 2.2"/>',
  fullscreen:
    '<path d="M9 4.5H5.5a1 1 0 0 0-1 1V9M15 4.5h3.5a1 1 0 0 1 1 1V9M9 19.5H5.5a1 1 0 0 1-1-1V15M15 19.5h3.5a1 1 0 0 0 1-1V15"/>',
  exitFullscreen:
    '<path d="M8.5 4v3.5a1 1 0 0 1-1 1H4M15.5 4v3.5a1 1 0 0 0 1 1H20M8.5 20v-3.5a1 1 0 0 0-1-1H4M15.5 20v-3.5a1 1 0 0 1 1-1H20"/>',
  panel:
    '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M15 4.5v15"/><path d="M6 9h6M6 12.5h6"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  pencil:
    '<path d="M15.5 4.5l4 4M4.5 19.5l1-4L16 5a2 2 0 0 1 2.8 0l.2.2a2 2 0 0 1 0 2.8L8.5 18.5z"/>',
  copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5V6.5a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2"/>',
  folder:
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  'folder-open':
    '<path d="M3.5 8.5V7a2 2 0 0 1 2-2h4l2 2h6.5a2 2 0 0 1 2 2v1"/><path d="M3.5 8.5h17l-2 9a2 2 0 0 1-2 1.6H6a2 2 0 0 1-2-1.6z"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 6.5"/>',
  'check-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 12.3l2.5 2.5 4.7-4.8"/>',
  error:
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
  info: '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><circle cx="12" cy="8" r="1" fill="currentColor" stroke="none"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  minus: '<path d="M5 12h14"/>',
  up: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  down: '<path d="M12 5v14M6 13l6 6 6-6"/>',
  trash:
    '<path d="M4.5 7h15M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/><path d="M6.5 7l1 12a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1l1-12"/><path d="M10 11v5M14 11v5"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  play: '<path d="M8 5l11 7-11 7z"/>',
  cancel: '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 8.5l7 7M15.5 8.5l-7 7"/>',
  scan: '<path d="M4 8.5V6a2 2 0 0 1 2-2h2.5M20 8.5V6a2 2 0 0 0-2-2h-2.5M4 15.5V18a2 2 0 0 0 2 2h2.5M20 15.5V18a2 2 0 0 1-2 2h-2.5"/><path d="M4 12h16"/>',
  external:
    '<path d="M14 4h6v6M20 4l-9 9"/><path d="M18 13v5a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h5"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
};

const inner = computed(() => ICONS[props.name] || ICONS.image);
</script>
