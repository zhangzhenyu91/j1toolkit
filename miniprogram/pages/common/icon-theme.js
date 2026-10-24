// 主包页面 t-icon 图标色共用（home / admin users / perms / teams）
// t-icon 的 color 属性会写成内联色，只接受具体颜色值，故按系统主题计算后绑定（不传 CSS 变量）；
// 取值与 app.wxss --app-brand / --app-text-* 的亮暗定义一致（与 components/tabbar 同套路）

const THEME_ICON = {
  light: {
    brand: '#0E3DA8', // 品牌蓝（--app-brand）
    sub: '#4E5969', // 次要文字（--app-text-secondary）
    weak: '#86909C', // 辅助文字（--app-text-tertiary）
    mute: 'rgba(29,33,41,.4)', // 弱化占位（与 tabbar 未激活项同值）
  },
  dark: {
    brand: '#78A6FF',
    sub: 'rgba(255,255,255,0.55)',
    weak: 'rgba(255,255,255,0.48)',
    mute: 'rgba(255,255,255,.4)',
  },
};

// 读取系统主题（getAppBaseInfo 提供 theme；旧基础库回退 getSystemInfoSync）
function readTheme() {
  try {
    let info = wx.getAppBaseInfo ? wx.getAppBaseInfo() : null;
    if ((!info || !info.theme) && wx.getSystemInfoSync) info = wx.getSystemInfoSync();
    return info && info.theme === 'dark' ? 'dark' : 'light';
  } catch (e) {
    return 'light';
  }
}

// 按系统主题解析图标计算色（theme 缺省时读系统主题）
function iconColors(theme) {
  return (theme || readTheme()) === 'dark' ? THEME_ICON.dark : THEME_ICON.light;
}

module.exports = { THEME_ICON, readTheme, iconColors };
