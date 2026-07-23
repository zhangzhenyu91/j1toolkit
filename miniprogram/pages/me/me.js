// 我的
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../utils/request';

Page({
  data: {
    userInfo: {},
    avatarChar: '检',
    appCount: 0,
    appNames: [],
  },

  onShow() {
    if (!wx.getStorageSync('token')) {
      wx.reLaunch({ url: '/pages/login/login' });
      return;
    }
    const cached = wx.getStorageSync('userInfo');
    if (cached) this.applyUser(cached);
    this.loadData();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  applyUser(user) {
    this.setData({
      userInfo: user,
      avatarChar: (user.nickname || user.username || '检').slice(0, 1),
    });
  },

  // 用户信息与可见应用数量
  async loadData() {
    try {
      const user = await request({ url: '/api/v1/user/profile' });
      getApp().applyUser(user);
      this.applyUser(user);
    } catch (err) {
      // 静默失败：保留缓存展示
    }
    try {
      const data = await request({ url: '/api/v1/app/list' });
      const list = (data && data.list) || [];
      this.setData({
        appCount: list.length,
        appNames: list.map((item) => item.name),
      });
    } catch (err) {
      // 静默失败：数量保持上次值
    }
  },

  // 我的权限：列出可见应用
  onPerms() {
    const content = this.data.appNames.length
      ? this.data.appNames.join('、')
      : '暂无可用应用，请联系管理员开通权限';
    Dialog.alert({
      context: this,
      selector: '#t-dialog',
      title: '我的权限',
      content,
      confirmBtn: '知道了',
    });
  },

  onAbout() {
    Dialog.alert({
      context: this,
      selector: '#t-dialog',
      title: '关于工具箱',
      content: '检修一班工具箱 v1.0.0，班组数字化工具平台。',
      confirmBtn: '知道了',
    });
  },

  // 管理功能入口（仅管理员可见）
  goUsers() {
    wx.navigateTo({ url: '/pages/admin/users/users' });
  },

  goPerms() {
    wx.navigateTo({ url: '/pages/admin/perms/perms' });
  },

  // 退出登录：确认后调用后端使 token 失效，清理本地登录态
  onLogout() {
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '退出登录',
      content: '确定要退出当前账号吗？',
      confirmBtn: '退出',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({ url: '/api/v1/auth/logout', method: 'POST' });
      } catch (err) {
        // 后端不可达也允许本地退出
      }
      wx.removeStorageSync('token');
      wx.removeStorageSync('userInfo');
      getApp().globalData.userInfo = null;
      wx.reLaunch({ url: '/pages/login/login' });
    }).catch(() => {});
  },
});
