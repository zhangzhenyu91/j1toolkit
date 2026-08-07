// 首页 · 应用中心（小程序入口页：自动登录门控；「我的」为同页滑动面板，tab 切换左右滑动）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../utils/request';
import { greeting } from '../../utils/util';
import { shareAppMessage } from '../../utils/share';
import config from '../../config';

const WEEKDAYS = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'];

Page({
  data: {
    gate: false, // 门控是否已通过（未通过时显示启动加载页）
    tab: 'home', // 当前面板：home / me
    greeting: '',
    today: '',
    weekday: '',
    userInfo: {},
    avatarChar: '检',
    apps: [],
    appCount: 0, // 「我的」面板：可见应用数量
    appNames: [], // 「我的」面板：可见应用名称（我的权限弹窗）
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

  // 当前用户可见应用列表（首页面板宫格 + 「我的」面板数量/权限清单共用一次请求；
  // 宫格按 terminal 过滤：小程序只展示 双端/移动端 应用（PC 端应用仅网页端可见），
  // 数量/权限清单仍按完整列表统计）
  async loadApps() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/app/list' });
      const list = (data && data.list) || [];
      this.setData({
        apps: list.filter((item) => item.terminal !== 'pc'),
        appCount: list.length,
        appNames: list.map((item) => item.name),
      });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // tab 切换（swiper 左右滑动动画）
  onTabSwitch(e) {
    this.setData({ tab: e.detail.key });
  },

  // 进入应用（分包页面）；无小程序页面的应用（path 为空）
  // 统一提示前往 PC 端 Shade 壹匣，确认即复制网址
  onAppTap(e) {
    const { path, name } = e.currentTarget.dataset;
    if (!path) {
      Dialog.confirm({
        context: this,
        selector: '#t-dialog',
        title: name || '网页端应用',
        content: `请在 PC 端 Shade 壹匣 使用，网址：${config.BASE_URL}`,
        confirmBtn: '复制网址',
        cancelBtn: '知道了',
      }).then(() => {
        wx.setClipboardData({ data: config.BASE_URL });
      }).catch(() => {});
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

  /* ---------- 「我的」面板 ---------- */

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
      title: '关于 Shade 壹匣',
      content: '版本号：v1.4.1 \n 应用权限申请联系 zzy',
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

  onShareAppMessage() {
    return shareAppMessage(this);
  },
});
