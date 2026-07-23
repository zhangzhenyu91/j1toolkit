// 首页 · 应用中心（小程序入口页：自动登录门控）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../utils/request';
import { greeting } from '../../utils/util';

const WEEKDAYS = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'];

Page({
  data: {
    gate: false, // 门控是否已通过（未通过时显示启动加载页）
    greeting: '',
    today: '',
    weekday: '',
    userInfo: {},
    avatarChar: '检',
    apps: [],
    loading: true,
  },

  onLoad() {
    const now = new Date();
    this.setData({
      greeting: greeting(),
      today: `${now.getMonth() + 1}月${now.getDate()}日`,
      weekday: WEEKDAYS[now.getDay()],
    });

    // 已有 token（如刚从登录页跳转来）直接放行；
    // 否则等待启动自检（token 校验 + 静默微信登录）结果
    if (wx.getStorageSync('token')) {
      this.passGate();
      return;
    }
    getApp().globalData.ready.then((authed) => {
      if (authed) {
        this.passGate();
        return;
      }
      wx.reLaunch({ url: '/pages/login/login' });
    });
  },

  passGate() {
    if (this.data.gate) return;
    this.setData({ gate: true });
    this.loadProfile();
    this.loadApps();
  },

  onShow() {
    if (this.data.gate) {
      this.loadProfile();
      this.loadApps();
    }
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 刷新用户信息（本地缓存优先，后台同步最新）
  async loadProfile() {
    const cached = wx.getStorageSync('userInfo');
    if (cached) this.applyUser(cached);
    try {
      const user = await request({ url: '/api/v1/user/profile' });
      getApp().applyUser(user); // 同步缓存与"可静默微信登录"标记
      this.applyUser(user);
    } catch (err) {
      // 静默失败：保留缓存展示
    }
  },

  applyUser(user) {
    this.setData({
      userInfo: user,
      avatarChar: (user.nickname || user.username || '检').slice(0, 1),
    });
  },

  // 当前用户可见应用列表
  async loadApps() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/app/list' });
      this.setData({ apps: (data && data.list) || [] });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 进入应用（分包页面）
  onAppTap(e) {
    const { path, name } = e.currentTarget.dataset;
    if (!path) {
      this.toast('应用页面接入中');
      return;
    }
    wx.navigateTo({
      url: path,
      fail: () => this.toast(`${name || '应用'}页面接入中`),
    });
  },

  onSoon() {
    this.toast('更多应用接入中，敬请期待');
  },
});
