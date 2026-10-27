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
    v-html="body"
  ></svg>
</template>

<script setup>
// 账号与权限管理页线性图标（inline SVG，无外部资源）。
// 图标名与 tdesign-miniprogram 同名（sys_app.icon 直接可用），内部路径移植自
// server/public/assets/icons.js（viewBox 0 0 24 24 / fill none / stroke-width 1.7 / 圆角端点），
// 未知名称回退 app，与旧版 Shade.icon 口径一致。
import { computed } from 'vue';

const props = defineProps({
  name: { type: String, default: 'app' },
  size: { type: [Number, String], default: 24 },
});

const ICONS = {
  robot:
    '<rect x="4" y="8" width="16" height="11.5" rx="2.5"/><path d="M12 4.5V8"/><circle cx="12" cy="3.8" r=".9" fill="currentColor" stroke="none"/><circle cx="9" cy="13" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="13" r="1" fill="currentColor" stroke="none"/><path d="M9.5 16.5h5"/>',
  'file-safety':
    '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M12 11l2.4.9v1.8c0 1.5-1 2.5-2.4 3.2-1.4-.7-2.4-1.7-2.4-3.2v-1.8z"/><path d="M10.9 14.1l.8.8 1.4-1.5"/>',
  task: '<path d="M15.5 4.5h1.3a2 2 0 0 1 2 2v12.3a2 2 0 0 1-2 2H7.2a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2h1.3"/><rect x="8.7" y="2.8" width="6.6" height="3.4" rx="1.2"/><path d="M9 12.5l2 2 4-4"/>',
  calendar:
    '<rect x="3.5" y="5" width="17" height="16" rx="2"/><path d="M16 3v4M8 3v4M3.5 10h17"/>',
  app: '<rect x="4" y="4" width="7" height="7" rx="1.8"/><rect x="13" y="4" width="7" height="7" rx="1.8"/><rect x="4" y="13" width="7" height="7" rx="1.8"/><rect x="13" y="13" width="7" height="7" rx="1.8"/>',
  add: '<path d="M12 5v14M5 12h14"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  delete:
    '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
  upload: '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  'check-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 12.3l2.5 2.5 4.7-4.8"/>',
  'error-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
  time: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3.5 2"/>',
  location:
    '<path d="M12 21s-6.5-5.3-6.5-10.2A6.5 6.5 0 0 1 12 4.3a6.5 6.5 0 0 1 6.5 6.5C18.5 15.7 12 21 12 21z"/><circle cx="12" cy="10.8" r="2.3"/>',
  image:
    '<rect x="3.5" y="5" width="17" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M21 15.5l-4.5-4.5L8 19.5"/>',
  user: '<circle cx="12" cy="8" r="3.6"/><path d="M5.6 19.4a6.4 6.4 0 0 1 12.8 0"/>',
  usergroup:
    '<circle cx="9" cy="8.5" r="3.2"/><path d="M3.8 19a5.2 5.2 0 0 1 10.4 0"/><path d="M15.5 5.6a3.2 3.2 0 0 1 0 5.8"/><path d="M17 14.2a5.2 5.2 0 0 1 3.3 4.8"/>',
  'lock-on':
    '<rect x="4.8" y="10.5" width="14.4" height="9.5" rx="2.5"/><path d="M8.2 10.5V8a3.8 3.8 0 0 1 7.6 0v2.5"/><circle cx="12" cy="15.2" r="1.1" fill="currentColor" stroke="none"/>',
  key: '<circle cx="8" cy="14.5" r="4"/><path d="M11 11.5L19 3.5"/><path d="M15.5 7l2.5 2.5M13 9.5l2 2"/>',
  book: '<path d="M12 7c-1.7-1.3-4-1.6-6.7-1.4v12.8c2.7-.2 5 .1 6.7 1.4 1.7-1.3 4-1.6 6.7-1.4V5.6C16 5.4 13.7 5.7 12 7z"/><path d="M12 7v12.8"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  'arrow-right': '<path d="M5 12h14M13 6l6 6-6 6"/>',
  send: '<path d="M21 3.5L3 10.5l7 2.5 2.5 7z"/><path d="M21 3.5L10 13"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  logout:
    '<path d="M14 4h4.5A1.5 1.5 0 0 1 20 5.5v13a1.5 1.5 0 0 1-1.5 1.5H14"/><path d="M3 12h11M10 8l4 4-4 4"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
  'file-logs':
    '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M8.7 12.2l1.6 1.6 3-3.2"/><path d="M8.7 17h6.6"/>',
  edit: '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
  setting:
    '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>',
  'info-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><circle cx="12" cy="8" r="1" fill="currentColor" stroke="none"/>',
  chat: '<path d="M20.5 4.5h-17A1.5 1.5 0 0 0 2 6v9a1.5 1.5 0 0 0 1.5 1.5H7l4 4 4-4h5.5A1.5 1.5 0 0 0 22 15V6a1.5 1.5 0 0 0-1.5-1.5z"/><circle cx="8" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="12" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="16" cy="10.5" r="1" fill="currentColor" stroke="none"/>',
  notification:
    '<path d="M18 9.2a6 6 0 1 0-12 0c0 5.8-2.2 6.8-2.2 6.8h16.4s-2.2-1-2.2-6.8z"/><path d="M9.8 19.5a2.2 2.2 0 0 0 4.4 0"/>',
  shield:
    '<path d="M12 3.2l6.8 2.5v5.2c0 4.5-2.9 7.7-6.8 9.4-3.9-1.7-6.8-4.9-6.8-9.4V5.7z"/><path d="M9 11.7l2.2 2.2 3.8-3.9"/>',
  cloud: '<path d="M17.8 19.5a4.3 4.3 0 0 0 .4-8.58A6 6 0 0 0 6.5 9.7 4.8 4.8 0 0 0 6.7 19.5z"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
  monitor: '<rect x="3" y="4" width="18" height="12.5" rx="2"/><path d="M8.5 20.5h7M12 16.5v4"/>',
  terminal:
    '<rect x="3" y="4.5" width="18" height="15" rx="2"/><path d="M7 9.5l3 3-3 3"/><path d="M12.5 15.5H17"/>',
  money:
    '<rect x="3" y="6.5" width="18" height="11" rx="2"/><circle cx="12" cy="12" r="2.4"/><path d="M6.3 9.7v.1M17.7 14.3v.1"/>',
  lightbulb:
    '<path d="M9.5 18.5h5M10.5 21h3"/><path d="M12 3.5a5.8 5.8 0 0 0-3.6 10.3c.8.7 1.4 1.6 1.6 2.7h4c.2-1.1.8-2 1.6-2.7A5.8 5.8 0 0 0 12 3.5z"/>',
  tools:
    '<path d="M14.6 6.2a4 4 0 0 0-5.5 5.2L4 16.5V20h3.5l5.1-5.1a4 4 0 0 0 5.2-5.5l-2.7 2.7-2.4-.6-.6-2.4z"/>',
  star: '<path d="M12 3.4l2.7 5.4 6 .9-4.3 4.2 1 6-5.4-2.8-5.4 2.8 1-6L3.3 9.7l6-.9z"/>',
  'view-module':
    '<rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>',
  'view-list':
    '<path d="M9.5 6h11M9.5 12h11M9.5 18h11"/><circle cx="4.5" cy="6" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="12" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="18" r="1.1" fill="currentColor" stroke="none"/>',
  compass: '<circle cx="12" cy="12" r="8.5"/><path d="M15.6 8.4l-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  folder:
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  'folder-open':
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2v1"/><path d="M3.5 7v10a2 2 0 0 0 2 2h11.5a2 2 0 0 0 1.86-1.26l2.28-5.7A1.5 1.5 0 0 0 19.75 10H7.44a2 2 0 0 0-1.86 1.26L3.5 16.8"/>',
  'folder-add':
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/><path d="M12 10.5v5M9.5 13h5"/>',
  link: '<path d="M10 14a5 5 0 0 0 7 .5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 10a5 5 0 0 0-7-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  share:
    '<circle cx="6" cy="12" r="2.5"/><circle cx="17.5" cy="5.5" r="2.5"/><circle cx="17.5" cy="18.5" r="2.5"/><path d="M8.2 10.8l7-4M8.2 13.2l7 4"/>',
  forest: '<path d="M12 3.5L6.8 10h2.7L4.8 16.5h4.7v4h5v-4h4.7L14.5 10h2.7z"/>',
  fork: '<circle cx="6" cy="5" r="2.5"/><circle cx="18" cy="5" r="2.5"/><circle cx="12" cy="19" r="2.5"/><path d="M6 7.5v1.8a4.2 4.2 0 0 0 4.2 4.2h3.6a4.2 4.2 0 0 0 4.2-4.2V7.5M12 13.5v3"/>',
  'chart-bar': '<path d="M4 4v15a1 1 0 0 0 1 1h15"/><path d="M8.5 16v-4.5M13 16V8.5M17.5 16v-7"/>',
  swap: '<path d="M4 7.5h11.5"/><path d="M13 4l3.5 3.5L13 11"/><path d="M20 16.5H8.5"/><path d="M11 13l-3.5 3.5L11 20"/>',
};

const body = computed(() => ICONS[props.name] || ICONS.app);
</script>
