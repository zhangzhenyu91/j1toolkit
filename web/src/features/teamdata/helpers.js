// 班组数据 feature 共用小工具：电压展示口径与页内线性图标（对齐 server/public/assets/icons.js）。

// fmtKv 电压字段展示：纯数字补 kV（如 110 → '110kV'），空值 '—'，其余（已带单位等）原样。
export function fmtKv(v) {
  if (v === null || v === undefined) return '—';
  const s = String(v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + 'kV' : String(v);
}

// 图标名 → SVG 内部元素（viewBox 0 0 24 24；风格 fill none / stroke-width 1.7 / 圆角端点）。
const ICONS = {
  'view-module':
    '<rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>',
  location:
    '<path d="M12 21s-6.5-5.3-6.5-10.2A6.5 6.5 0 0 1 12 4.3a6.5 6.5 0 0 1 6.5 6.5C18.5 15.7 12 21 12 21z"/><circle cx="12" cy="10.8" r="2.3"/>',
  user: '<circle cx="12" cy="8" r="3.6"/><path d="M5.6 19.4a6.4 6.4 0 0 1 12.8 0"/>',
  compass: '<circle cx="12" cy="12" r="8.5"/><path d="M15.6 8.4l-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  'view-list':
    '<path d="M9.5 6h11M9.5 12h11M9.5 18h11"/><circle cx="4.5" cy="6" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="12" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="18" r="1.1" fill="currentColor" stroke="none"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  upload: '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
  'lock-on':
    '<rect x="4.8" y="10.5" width="14.4" height="9.5" rx="2.5"/><path d="M8.2 10.5V8a3.8 3.8 0 0 1 7.6 0v2.5"/><circle cx="12" cy="15.2" r="1.1" fill="currentColor" stroke="none"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
};

// icon 生成 inline SVG 字符串（size 边长像素，默认 24；描边 currentColor）。
export function icon(name, size = 24) {
  const inner = ICONS[name] || '';
  return (
    `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
    `stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${inner}</svg>`
  );
}
