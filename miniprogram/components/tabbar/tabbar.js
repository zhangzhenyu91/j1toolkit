// 底部标签栏组件
// t-icon 的 color 属性会写成内联色，只接受具体颜色值，故按系统主题计算后绑定（不传 CSS 变量）

// 图标颜色计算值（与 app.wxss --app-brand / 图标弱化色的亮暗取值一致）
const THEME_ICON = {
  light: { active: '#0E3DA8', inactive: 'rgba(29,33,41,.4)' },
  dark: { active: '#78A6FF', inactive: 'rgba(255,255,255,.4)' },
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

Component({
  // apply-shared：让 app.wxss 全局样式（.hover-scale 按压反馈）渗入组件，默认隔离下 hover-class 静默失效
  options: { styleIsolation: 'apply-shared' },

  properties: {
    active: { type: String, value: 'home' }, // 当前面板：home / me
  },

  data: {
    iconActiveColor: THEME_ICON.light.active, // 激活项图标颜色（计算值）
    iconInactiveColor: THEME_ICON.light.inactive, // 未激活项图标颜色（计算值）
  },

  lifetimes: {
    attached() {
      this._onThemeChange = (res) => this.applyTheme(res && res.theme);
      this.applyTheme(readTheme());
      wx.onThemeChange(this._onThemeChange);
    },
    detached() {
      // 组件卸载解除主题监听
      if (this._onThemeChange) {
        wx.offThemeChange(this._onThemeChange);
        this._onThemeChange = null;
      }
    },
  },

  pageLifetimes: {
    show() {
      this.applyTheme();
    },
  },

  methods: {
    // 按系统主题更新图标计算色
    applyTheme(theme) {
      const t = (theme || readTheme()) === 'dark' ? THEME_ICON.dark : THEME_ICON.light;
      this.setData({ iconActiveColor: t.active, iconInactiveColor: t.inactive });
    },

    go(e) {
      const { key } = e.currentTarget.dataset;
      if (key === this.data.active) return; // 已在当前面板
      this.triggerEvent('switch', { key }); // 由页面切换面板（左右滑动动画）
    },
  },
});
