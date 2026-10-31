// 自定义导航栏：自适应状态栏高度
// 注意：右上角为微信胶囊按钮区域，禁止放置可点击内容，故本组件不提供右侧插槽
// 深浅色：非品牌页首跟随系统主题；品牌深蓝页首（frontColor=#ffffff，如首页）固定，与系统 dark 分开

const { THEME_NAV, readTheme } = require('../../utils/theme');

// 归一化为 6 位 hex 背景色；不合法时回退默认值（setNavigationBarColor 拒绝非 hex）
function toHex6(color, fallback) {
  const c = (color || '').trim();
  if (/^#[0-9a-fA-F]{6}$/.test(c)) return c;
  if (/^#[0-9a-fA-F]{3}$/.test(c)) {
    return `#${c[1]}${c[1]}${c[2]}${c[2]}${c[3]}${c[3]}`;
  }
  return fallback;
}

// frontColor 仅支持黑白，归一化为 #000000/#ffffff
function toFrontColor(color) {
  return (color || '').trim().toLowerCase() === '#ffffff' ? '#ffffff' : '#000000';
}

Component({
  properties: {
    title: { type: String, value: '' }, // 标题
    back: { type: Boolean, value: false }, // 是否显示返回箭头
    bg: { type: String, value: '#F5F7FA' }, // 背景色（品牌深蓝页首时原样使用）
    color: { type: String, value: '#1D2129' }, // 标题/箭头颜色（品牌深蓝页首时原样使用）
    frontColor: { type: String, value: '#000000' }, // 状态栏文字颜色（仅支持 #000000/#ffffff；#ffffff 声明品牌深蓝页首）
  },

  data: {
    statusBarHeight: 20,
    isRoot: false, // 当前页是页面栈栈底（分享卡片进入等场景）：返回箭头无效，改显首页图标
    isHome: false, // 当前页是首页：不显示首页图标
    isDark: false, // 品牌深蓝页首（frontColor=#ffffff）：取消发丝底线；与系统深色无关
    navBg: '#F5F7FA', // 解析后的页首背景（合法 hex）
    navColor: '#1D2129', // 解析后的标题/图标颜色
  },

  lifetimes: {
    attached() {
      const app = getApp();
      const pages = getCurrentPages();
      const route = pages.length ? pages[pages.length - 1].route : '';
      this.setData({
        statusBarHeight: (app && app.globalData.statusBarHeight) || 20,
        isRoot: pages.length <= 1,
        isHome: route === 'pages/home/home',
        isDark: toFrontColor(this.data.frontColor) === '#ffffff',
      });
      this._onThemeChange = () => this.applyTheme();
      this.applyTheme();
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
      // 回前台重读主题，覆盖后台切换系统浅/深色的情况
      this.applyTheme();
    },
  },

  methods: {
    // 按系统主题解析页首颜色，并用合法 hex 与黑白 frontColor 同步系统状态栏
    // （进入每个页面时重置，避免被登录页的白色设置残留）
    applyTheme() {
      const dark = readTheme() === 'dark';
      const theme = dark ? THEME_NAV.dark : THEME_NAV.light;
      let navBg;
      let navColor;
      let frontColor;
      if (this.data.isDark) {
        // 品牌深蓝页首：颜色原样固定，与系统 dark 分开
        navBg = toHex6(this.data.bg, THEME_NAV.light.bg);
        navColor = this.data.color;
        frontColor = '#ffffff';
      } else if (dark) {
        navBg = theme.bg;
        navColor = theme.color;
        frontColor = theme.frontColor;
      } else {
        navBg = toHex6(this.data.bg, theme.bg);
        navColor = this.data.color;
        frontColor = theme.frontColor;
      }
      this.setData({ navBg, navColor });
      wx.setNavigationBarColor({
        frontColor,
        backgroundColor: navBg,
      });
    },

    onBack() {
      // 栈底页（分享进入）无页可退，回首页；首页自身不响应
      if (this.data.isRoot) {
        if (!this.data.isHome) {
          wx.reLaunch({ url: '/pages/home/home' });
        }
        return;
      }
      if (this.data.back) {
        wx.navigateBack({ delta: 1 });
      }
    },
  },
});
