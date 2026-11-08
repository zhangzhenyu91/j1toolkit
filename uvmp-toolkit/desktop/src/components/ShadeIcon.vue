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
    v-html="inner"
  />
</template>

<script>
/* Shade 壹匣线性图标（inline SVG，无外部资源）。
   图标集移植自 j1toolkit server/public/assets/icons.js（同名同笔画：fill none /
   stroke-width 1.7 / 圆角端点），窗口控制三枚为同风格自绘。 */
const ICONS = {
  app: '<rect x="4" y="4" width="7" height="7" rx="1.8"/><rect x="13" y="4" width="7" height="7" rx="1.8"/><rect x="4" y="13" width="7" height="7" rx="1.8"/><rect x="13" y="13" width="7" height="7" rx="1.8"/>',
  home: '<path d="M4 10.5L12 4l8 6.5V20a1 1 0 0 1-1 1h-5v-6H10v6H5a1 1 0 0 1-1-1z"/>',
  history: '<path d="M4 5v4h4"/><path d="M4.6 9A8 8 0 1 1 4 12"/><path d="M12 7.5V12l3.2 2"/>',
  route:
    '<circle cx="5" cy="18" r="2"/><circle cx="19" cy="6" r="2"/><path d="M7 18h5a4 4 0 0 0 0-8H9a3 3 0 0 1 0-6h2"/>',
  course: '<rect x="3" y="4.5" width="18" height="12" rx="2"/><path d="M7 20.5h10M12 16.5v4"/>',
  defect: '<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M8 9h8M8 13h5M8 17h3"/><path d="M17 15l2 2 3-3"/>',
  forest: '<path d="M12 3l4 6h-2.5l4 6H6.5l4-6H8z"/><path d="M12 15v6"/>',
  fork: '<circle cx="6" cy="6" r="2"/><circle cx="18" cy="6" r="2"/><circle cx="12" cy="18" r="2"/><path d="M6 8v3a3 3 0 0 0 3 3h6a3 3 0 0 0 3-3V8M12 14v2"/>',
  star: '<path d="M12 3.6l2.6 5.3 5.9.8-4.3 4.1 1 5.8-5.2-2.8-5.2 2.8 1-5.8-4.3-4.1 5.9-.8z"/>',
  'star-filled':
    '<path d="M12 3.6l2.6 5.3 5.9.8-4.3 4.1 1 5.8-5.2-2.8-5.2 2.8 1-5.8-4.3-4.1 5.9-.8z" fill="currentColor"/>',
  folder:
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  save: '<path d="M5 4h11l3 3v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z"/><path d="M8 4v5h7V4M8 20v-6h8v6"/>',
  play: '<path d="M8 5l11 7-11 7z"/>',
  cancel: '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 8.5l7 7M15.5 8.5l-7 7"/>',
  pause: '<path d="M8.5 6v12M15.5 6v12"/>',
  upload: '<path d="M12 20.5v-11M7 13.5l5-5 5 5"/><path d="M4.5 4.5h15"/>',
  trash:
    '<path d="M4.5 7h15M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/><path d="M6.5 7l1 12a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1l1-12"/><path d="M10 11v5M14 11v5"/>',
  external:
    '<path d="M14 4h6v6M20 4l-9 9"/><path d="M18 13v5a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h5"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 6.5"/>',
  flask:
    '<path d="M9 3h6M10 3v5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 8V3"/><path d="M8 14h8"/>',
  globe:
    '<circle cx="12" cy="12" r="8.5"/><path d="M3.8 9h16.4M3.8 15h16.4M12 3.5c2 2.2 3 5.2 3 8.5s-1 6.3-3 8.5c-2-2.2-3-5.2-3-8.5s1-6.3 3-8.5z"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  setting:
    '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0 .34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  time: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3.5 2"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
  task: '<path d="M15.5 4.5h1.3a2 2 0 0 1 2 2v12.3a2 2 0 0 1-2 2H7.2a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2h1.3"/><rect x="8.7" y="2.8" width="6.6" height="3.4" rx="1.2"/><path d="M9 12.5l2 2 4-4"/>',
  calendar:
    '<rect x="3.5" y="5" width="17" height="16" rx="2"/><path d="M16 3v4M8 3v4M3.5 10h17"/>',
  location:
    '<path d="M12 21s-6.5-5.3-6.5-10.2A6.5 6.5 0 0 1 12 4.3a6.5 6.5 0 0 1 6.5 6.5C18.5 15.7 12 21 12 21z"/><circle cx="12" cy="10.8" r="2.3"/>',
  user: '<circle cx="12" cy="8" r="3.6"/><path d="M5.6 19.4a6.4 6.4 0 0 1 12.8 0"/>',
  key: '<circle cx="8" cy="14.5" r="4"/><path d="M11 11.5L19 3.5"/><path d="M15.5 7l2.5 2.5M13 9.5l2 2"/>',
  'check-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 12.3l2.5 2.5 4.7-4.8"/>',
  'error-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
  'info-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><circle cx="12" cy="8" r="1" fill="currentColor" stroke="none"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
  monitor: '<rect x="3" y="4" width="18" height="12.5" rx="2"/><path d="M8.5 20.5h7M12 16.5v4"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  /* 窗口控制（同风格自绘） */
  'win-min': '<path d="M6 12h12"/>',
  'win-max': '<rect x="5" y="5" width="14" height="14" rx="2"/>',
  'win-restore':
    '<rect x="8" y="8" width="11" height="11" rx="2"/><path d="M16 8V6.5A1.5 1.5 0 0 0 14.5 5H6.5A1.5 1.5 0 0 0 5 6.5v8A1.5 1.5 0 0 0 6.5 16H8"/>',
};

export default {
  name: 'ShadeIcon',
  props: { name: { type: String, required: true }, size: { type: [Number, String], default: 20 } },
  computed: {
    inner() {
      return ICONS[this.name] || ICONS['app'];
    },
  },
};
</script>
