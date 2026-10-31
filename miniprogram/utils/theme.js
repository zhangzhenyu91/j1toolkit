// 小程序主题工具（单一来源）：系统深浅色读取、图标计算色与导航栏色。
// t-icon 的 color 属性会写成内联色，只接受具体色值，故按系统主题计算后绑定；
// 取值与 app.wxss --app-* 亮暗定义一致（品牌蓝 / 三级文字 / 弱化占位）。
// pages/common/icon-theme.js 仅作历史路径转发，页面与组件统一从本文件引入。

const THEME_ICON = {
  light: {
    brand: '#0E3DA8', // 品牌蓝（--app-brand）
    sub: '#4E5969', // 次要文字（--app-text-secondary）
    weak: '#86909C', // 辅助文字（--app-text-tertiary）
    mute: 'rgba(29,33,41,.4)', // 弱化占位（tabbar 未激活项）
    placeholder: '#C9CDD4', // 更弱占位（搜索清除、列表箭头、空态）
    active: '#0E3DA8',
    inactive: 'rgba(29,33,41,.4)',
  },
  dark: {
    brand: '#78A6FF',
    sub: 'rgba(255,255,255,0.55)',
    weak: 'rgba(255,255,255,0.48)',
    mute: 'rgba(255,255,255,.4)',
    placeholder: 'rgba(255,255,255,.4)',
    active: '#78A6FF',
    inactive: 'rgba(255,255,255,.4)',
  },
};

// 非品牌页首的计算值（wx.setNavigationBarColor 只接受合法 hex 背景与 #000000/#ffffff 前景）。
const THEME_NAV = {
  light: { bg: '#F5F7FA', color: '#1D2129', frontColor: '#000000' },
  dark: { bg: '#181818', color: 'rgba(255,255,255,0.9)', frontColor: '#ffffff' },
};

// readTheme 读取系统主题（getAppBaseInfo 提供 theme；旧基础库回退 getSystemInfoSync）；
// 已显式传入合法主题时直接返回（onThemeChange 回调值优先，避免重复读系统信息）。
function readTheme(preferred) {
  if (preferred === 'dark' || preferred === 'light') return preferred;
  try {
    let info = wx.getAppBaseInfo ? wx.getAppBaseInfo() : null;
    if ((!info || !info.theme) && wx.getSystemInfoSync) info = wx.getSystemInfoSync();
    return info && info.theme === 'dark' ? 'dark' : 'light';
  } catch (e) {
    return 'light';
  }
}

// iconColors 按系统主题解析图标计算色（theme 缺省时读系统主题）。
function iconColors(theme) {
  return readTheme(theme) === 'dark' ? THEME_ICON.dark : THEME_ICON.light;
}

// watchTheme 页面/组件共用系统主题监听：onThemeChange 时刷新 ico 计算色。
function watchTheme(target) {
  if (target._onThemeChange) return;
  target._onThemeChange = (res) => {
    target.setData({ ico: iconColors(res && res.theme) });
  };
  wx.onThemeChange(target._onThemeChange);
}

// unwatchTheme 解除监听（页面 onUnload / 组件 detached）。
function unwatchTheme(target) {
  if (!target._onThemeChange) return;
  wx.offThemeChange(target._onThemeChange);
  target._onThemeChange = null;
}

module.exports = {
  THEME_ICON,
  THEME_NAV,
  readTheme,
  iconColors,
  watchTheme,
  unwatchTheme,
};
