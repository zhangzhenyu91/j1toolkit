// 自定义导航栏：自适应状态栏高度
// 注意：右上角为微信胶囊按钮区域，禁止放置可点击内容，故本组件不提供右侧插槽
Component({
  properties: {
    title: { type: String, value: '' }, // 标题
    back: { type: Boolean, value: false }, // 是否显示返回箭头
    bg: { type: String, value: '#F4F1EA' }, // 背景色
    color: { type: String, value: '#22314E' }, // 标题/箭头颜色
    frontColor: { type: String, value: '#000000' }, // 状态栏文字颜色（仅支持 #000000/#ffffff）
  },

  data: {
    statusBarHeight: 20,
    isRoot: false, // 当前页是页面栈栈底（分享卡片进入等场景）：返回箭头无效，改显首页图标
    isHome: false, // 当前页是首页：不显示首页图标
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
      });
      // 同步系统状态栏文字颜色（进入每个页面时重置，避免被登录页的白色设置残留）
      wx.setNavigationBarColor({
        frontColor: this.data.frontColor,
        backgroundColor: this.data.bg,
      });
    },
  },

  methods: {
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
