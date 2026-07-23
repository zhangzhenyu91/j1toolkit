// 登录页：账号密码登录 + 微信登录
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../utils/request';
import { wxLoginCode } from '../../utils/util';

Page({
  data: {
    statusBarHeight: 20,
    username: '',
    password: '',
    loading: false,
  },

  onLoad() {
    const app = getApp();
    this.setData({ statusBarHeight: (app && app.globalData.statusBarHeight) || 20 });
  },

  onShow() {
    // 登录页为藏青横幅，状态栏文字用白色（其他页面由 navbar 组件重置为黑色）
    wx.setNavigationBarColor({ frontColor: '#ffffff', backgroundColor: '#22314E' });
    // 已登录则直接进入首页（涵盖直接打开登录页的场景）
    if (wx.getStorageSync('token')) {
      wx.reLaunch({ url: '/pages/home/home' });
    }
  },

  onUsername(e) {
    this.setData({ username: e.detail.value });
  },

  onPassword(e) {
    this.setData({ password: e.detail.value });
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 登录成功统一处理：保存 token 与用户信息（含"可静默微信登录"标记），进入首页
  afterLogin(data) {
    wx.setStorageSync('token', data.token);
    getApp().applyUser(data.user);
    wx.reLaunch({ url: '/pages/home/home' });
  },

  // 账号密码登录（携带 wx.login code：账号未绑微信时由后端自动绑定当前微信号）
  async onAccountLogin() {
    const { username, password, loading } = this.data;
    if (loading) return;
    if (!username.trim() || !password) {
      this.toast('请输入账号和密码');
      return;
    }
    this.setData({ loading: true });
    try {
      const wxCode = await wxLoginCode();
      const data = await request({
        url: '/api/v1/auth/login',
        method: 'POST',
        data: { username: username.trim(), password, wx_code: wxCode },
      });
      if (data.wx_bound) {
        wx.showToast({ title: '登录成功，已绑定微信号', icon: 'success', duration: 1200 });
        setTimeout(() => this.afterLogin(data), 1200);
      } else if (data.bind_message) {
        wx.showToast({ title: data.bind_message, icon: 'none', duration: 1500 });
        setTimeout(() => this.afterLogin(data), 1500);
      } else {
        this.afterLogin(data);
      }
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 微信一键登录：wx.login 得 code，后端换 openid
  onWxLogin() {
    wx.login({
      success: async (res) => {
        if (!res.code) {
          this.toast('微信登录失败：未获取到 code');
          return;
        }
        try {
          const data = await request({
            url: '/api/v1/auth/wx-login',
            method: 'POST',
            data: { code: res.code },
          });
          this.afterLogin(data);
        } catch (err) {
          this.toast(err.message);
        }
      },
      fail: () => this.toast('微信登录失败，请重试'),
    });
  },
});
