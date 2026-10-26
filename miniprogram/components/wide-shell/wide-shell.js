import { request } from '../../utils/request';

const SHORT_NAMES = {
  'call-me': 'Call Me',
  netdisk: '网盘',
  'work-log': '日志',
  'tree-hazard': '树障',
  crossing: '交跨',
  'file-transfer': '传输',
  quiz: '刷题',
  'wm-add': '水印',
  'safe-day': '安全日',
  'team-data': '数据',
};

Component({
  options: { styleIsolation: 'apply-shared' },

  properties: {
    active: { type: String, value: 'home' },
    apps: { type: Array, value: [] },
    appsReady: { type: Boolean, value: false },
    userInfo: { type: Object, value: null },
  },

  observers: {
    'apps, appsReady'() {
      this.applyApps();
    },
    userInfo() {
      this.applyUser();
    },
  },

  data: {
    collapsed: false,
    statusBarHeight: 20,
    navApps: [],
    profile: {},
    avatarChar: '壹',
  },

  lifetimes: {
    attached() {
      const app = getApp();
      this.setData({
        collapsed: !!wx.getStorageSync('wide_nav_collapsed'),
        statusBarHeight: (app && app.globalData.statusBarHeight) || 20,
      });
      this.applyApps();
      this.applyUser();
    },
  },

  pageLifetimes: {
    show() {
      this.applyUser();
      if (!this.data.appsReady && !this.data.navApps.length) this.applyApps();
    },
  },

  methods: {
    decorateApps(list) {
      return (list || [])
        .filter((item) => item.terminal !== 'pc')
        .map((item) => ({
          ...item,
          shortName: SHORT_NAMES[item.app_key] || item.name.slice(0, 4),
        }));
    },

    applyApps() {
      if (this.data.appsReady) {
        this.setData({ navApps: this.decorateApps(this.data.apps) });
        return;
      }
      const app = getApp();
      const cached = (app && app.globalData.apps) || wx.getStorageSync('wide_apps') || [];
      this.setData({ navApps: this.decorateApps(cached) });
      if (!cached.length) this.loadApps();
    },

    async loadApps() {
      if (this._appsLoading || this._appsLoaded) return;
      this._appsLoading = true;
      try {
        const data = await request({ url: '/api/v1/app/list' });
        const list = this.decorateApps((data && data.list) || []);
        getApp().globalData.apps = list;
        wx.setStorageSync('wide_apps', list);
        this._appsLoaded = true;
        if (!this.data.appsReady) this.setData({ navApps: list });
      } catch (err) {
        // 导航请求失败时保留缓存，业务页仍按原权限与错误口径工作。
      } finally {
        this._appsLoading = false;
      }
    },

    applyUser() {
      const app = getApp();
      const prop = this.data.userInfo;
      const profile =
        (prop && (prop.nickname || prop.username) && prop) ||
        (app && app.globalData.userInfo) ||
        wx.getStorageSync('userInfo') ||
        {};
      this.setData({
        profile,
        avatarChar: (profile.nickname || profile.username || '壹').slice(0, 1),
      });
    },

    toggleCollapse() {
      const collapsed = !this.data.collapsed;
      wx.setStorageSync('wide_nav_collapsed', collapsed ? '1' : '');
      this.setData({ collapsed });
    },

    goHome() {
      const pages = getCurrentPages();
      const current = pages[pages.length - 1];
      if (current && current.route === 'pages/home/home') {
        current.onTabSwitch({ detail: { key: 'home' } });
        return;
      }
      wx.reLaunch({ url: '/pages/home/home' });
    },

    goMe() {
      const pages = getCurrentPages();
      const current = pages[pages.length - 1];
      if (current && current.route === 'pages/home/home') {
        current.onTabSwitch({ detail: { key: 'me' } });
        return;
      }
      wx.reLaunch({ url: '/pages/home/home?tab=me' });
    },

    goApp(e) {
      const { path, key } = e.currentTarget.dataset;
      if (!path || key === this.data.active) return;
      const pages = getCurrentPages();
      const fromHome = pages.length && pages[pages.length - 1].route === 'pages/home/home';
      const method = fromHome ? 'navigateTo' : 'redirectTo';
      wx[method]({
        url: path,
        fail: () => wx.showToast({ title: '页面接入中', icon: 'none' }),
      });
    },
  },
});
