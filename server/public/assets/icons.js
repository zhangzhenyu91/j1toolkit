/* ============================================================
   Shade 壹匣 · 网页版线性图标库（无外部资源，inline SVG）
   用法：Shade.icon(name, size, color) → SVG 字符串，可 innerHTML 注入
     name  与 tdesign-miniprogram 同名（sys_app.icon 直接可用）
     size  边长像素，默认 24；color 描边色，默认 currentColor
   风格：fill none / stroke-width 1.7 / 圆角端点，与 style-5 一致
   ============================================================ */
(function () {
  // 图标名 → SVG 内部元素（viewBox 统一 0 0 24 24）
  const ICONS = {
    // 品牌 Logo（工具箱）
    'logo': '<rect x="3.5" y="7.5" width="17" height="12.5" rx="2.5"/><path d="M9 7.5V5.6A1.6 1.6 0 0 1 10.6 4h2.8A1.6 1.6 0 0 1 15 5.6v1.9"/><path d="M3.5 12h17"/><path d="M10.8 12v1.6h2.4V12"/>',
    'robot': '<rect x="4" y="8" width="16" height="11.5" rx="2.5"/><path d="M12 4.5V8"/><circle cx="12" cy="3.8" r=".9" fill="currentColor" stroke="none"/><circle cx="9" cy="13" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="13" r="1" fill="currentColor" stroke="none"/><path d="M9.5 16.5h5"/>',
    'file-safety': '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M12 11l2.4.9v1.8c0 1.5-1 2.5-2.4 3.2-1.4-.7-2.4-1.7-2.4-3.2v-1.8z"/><path d="M10.9 14.1l.8.8 1.4-1.5"/>',
    'task': '<path d="M15.5 4.5h1.3a2 2 0 0 1 2 2v12.3a2 2 0 0 1-2 2H7.2a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2h1.3"/><rect x="8.7" y="2.8" width="6.6" height="3.4" rx="1.2"/><path d="M9 12.5l2 2 4-4"/>',
    'calendar': '<rect x="3.5" y="5" width="17" height="16" rx="2"/><path d="M16 3v4M8 3v4M3.5 10h17"/>',
    'app': '<rect x="4" y="4" width="7" height="7" rx="1.8"/><rect x="13" y="4" width="7" height="7" rx="1.8"/><rect x="4" y="13" width="7" height="7" rx="1.8"/><rect x="13" y="13" width="7" height="7" rx="1.8"/>',
    'add': '<path d="M12 5v14M5 12h14"/>',
    'search': '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
    'bell': '<path d="M6.2 16.2v-4.9a5.8 5.8 0 0 1 11.6 0v4.9l1.6 2.6H4.6z"/><path d="M10.2 20.6a2 2 0 0 0 3.6 0"/>',
    'close': '<path d="M6 6l12 12M18 6L6 18"/>',
    'download': '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
    'delete': '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
    'upload': '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
    'check': '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
    'check-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 12.3l2.5 2.5 4.7-4.8"/>',
    'error-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
    'time': '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3.5 2"/>',
    'location': '<path d="M12 21s-6.5-5.3-6.5-10.2A6.5 6.5 0 0 1 12 4.3a6.5 6.5 0 0 1 6.5 6.5C18.5 15.7 12 21 12 21z"/><circle cx="12" cy="10.8" r="2.3"/>',
    'image': '<rect x="3.5" y="5" width="17" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M21 15.5l-4.5-4.5L8 19.5"/>',
    'folder': '<path d="M3.5 6.5A2 2 0 0 1 5.5 4.5h4l2 2.5h7A2 2 0 0 1 20.5 9v8.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
    'user': '<circle cx="12" cy="8" r="3.6"/><path d="M5.6 19.4a6.4 6.4 0 0 1 12.8 0"/>',
    'usergroup': '<circle cx="9" cy="8.5" r="3.2"/><path d="M3.8 19a5.2 5.2 0 0 1 10.4 0"/><path d="M15.5 5.6a3.2 3.2 0 0 1 0 5.8"/><path d="M17 14.2a5.2 5.2 0 0 1 3.3 4.8"/>',
    'lock-on': '<rect x="4.8" y="10.5" width="14.4" height="9.5" rx="2.5"/><path d="M8.2 10.5V8a3.8 3.8 0 0 1 7.6 0v2.5"/><circle cx="12" cy="15.2" r="1.1" fill="currentColor" stroke="none"/>',
    'key': '<circle cx="8" cy="14.5" r="4"/><path d="M11 11.5L19 3.5"/><path d="M15.5 7l2.5 2.5M13 9.5l2 2"/>',
    'book': '<path d="M12 7c-1.7-1.3-4-1.6-6.7-1.4v12.8c2.7-.2 5 .1 6.7 1.4 1.7-1.3 4-1.6 6.7-1.4V5.6C16 5.4 13.7 5.7 12 7z"/><path d="M12 7v12.8"/>',
    'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
    'arrow-left': '<path d="M19 12H5M11 6l-6 6 6 6"/>',
    'arrow-right': '<path d="M5 12h14M13 6l6 6-6 6"/>',
    'send': '<path d="M21 3.5L3 10.5l7 2.5 2.5 7z"/><path d="M21 3.5L10 13"/>',
    'attach': '<path d="M20 11.5l-8.2 8.2a5 5 0 0 1-7-7l8.2-8.2a3.4 3.4 0 0 1 4.8 4.8l-8.2 8.2a1.8 1.8 0 0 1-2.5-2.5l7.5-7.5"/>',
    'copy': '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
    'refresh': '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
    'logout': '<path d="M14 4h4.5A1.5 1.5 0 0 1 20 5.5v13a1.5 1.5 0 0 1-1.5 1.5H14"/><path d="M3 12h11M10 8l4 4-4 4"/>',
    'home': '<path d="M4 11l8-7 8 7"/><path d="M6 9.5V20h12V9.5"/><path d="M10 20v-6h4v6"/>',
    'file': '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
    'edit': '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
    'setting': '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>',
    'info-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><circle cx="12" cy="8" r="1" fill="currentColor" stroke="none"/>',
    'link': '<path d="M10 14a5 5 0 0 0 7.1 0l2.1-2.1a5 5 0 0 0-7.1-7.1L11 5.9"/><path d="M14 10a5 5 0 0 0-7.1 0l-2.1 2.1a5 5 0 0 0 7.1 7.1l1.1-1.1"/>',
    'qrcode': '<rect x="3.5" y="3.5" width="6.5" height="6.5" rx="1"/><rect x="14" y="3.5" width="6.5" height="6.5" rx="1"/><rect x="3.5" y="14" width="6.5" height="6.5" rx="1"/><path d="M14 14h2.8v2.8H14zM17.5 17.5h3v3h-3zM14 18.8h1.6v1.7H14zM19 14h1.5v1.5H19z" fill="currentColor" stroke="none"/>',
    'view': '<path d="M2.5 12S6 5.8 12 5.8 21.5 12 21.5 12 18 18.2 12 18.2 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.8"/>',
    'thumb-up': '<path d="M7 21V10.5"/><path d="M7 11H4.5A1.5 1.5 0 0 0 3 12.5v7A1.5 1.5 0 0 0 4.5 21H7"/><path d="M7 10.5l4-7.5a2 2 0 0 1 3.7 1l-.6 3.5h4.9a2 2 0 0 1 2 2.4l-1.2 7a2 2 0 0 1-2 1.6H7"/>',
    'chat': '<path d="M20.5 4.5h-17A1.5 1.5 0 0 0 2 6v9a1.5 1.5 0 0 0 1.5 1.5H7l4 4 4-4h5.5A1.5 1.5 0 0 0 22 15V6a1.5 1.5 0 0 0-1.5-1.5z"/><circle cx="8" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="12" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="16" cy="10.5" r="1" fill="currentColor" stroke="none"/>',
    'shield': '<path d="M12 3.2l6.8 2.5v5.2c0 4.5-2.9 7.7-6.8 9.4-3.9-1.7-6.8-4.9-6.8-9.4V5.7z"/><path d="M9 11.7l2.2 2.2 3.8-3.9"/>',
    'ellipsis': '<circle cx="6.3" cy="12" r="1.2" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="1.2" fill="currentColor" stroke="none"/><circle cx="17.7" cy="12" r="1.2" fill="currentColor" stroke="none"/>',
    /* 外部应用入口用到的补充图标 */
    'cloud': '<path d="M17.8 19.5a4.3 4.3 0 0 0 .4-8.58A6 6 0 0 0 6.5 9.7 4.8 4.8 0 0 0 6.7 19.5z"/>',
    'warn': '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
    'monitor': '<rect x="3" y="4" width="18" height="12.5" rx="2"/><path d="M8.5 20.5h7M12 16.5v4"/>',
  };

  /**
   * 生成 inline SVG 图标字符串
   * @param {string} name 图标名（tdesign 同名，未知名称回退 app）
   * @param {number} [size=24] 边长像素
   * @param {string} [color='currentColor'] 描边颜色
   * @returns {string} SVG 字符串
   */
  function icon(name, size, color) {
    const body = ICONS[name] || ICONS['app'];
    const s = size || 24;
    const c = color || 'currentColor';
    return '<svg width="' + s + '" height="' + s + '" viewBox="0 0 24 24" fill="none" stroke="' + c +
      '" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' + body + '</svg>';
  }

  window.Shade = Object.assign(window.Shade || {}, { icon: icon, ICONS: ICONS });
})();
