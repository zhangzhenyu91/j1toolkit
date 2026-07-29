// 安全日活动记录 · 引导页
// 本应用为网页端应用：展示访问地址并支持一键复制，引导用户在电脑浏览器使用（小程序内不直接打开）
const config = require('../../../config');

Page({
  data: {
    gate: false, // 登录态门控（首页宫格已做权限过滤，此处仅保证登录态就绪）
    url: config.SAFEDAY_WEB_URL,
  },

  onLoad() {
    if (wx.getStorageSync('token')) {
      this.setData({ gate: true });
      return;
    }
    getApp().globalData.ready.then((authed) => {
      if (authed) {
        this.setData({ gate: true });
        return;
      }
      wx.navigateBack({
        fail: () => wx.reLaunch({ url: '/pages/home/home' }),
      });
    });
  },

  // 复制访问地址（成功提示沿用系统「内容已复制」toast，见开发指南约定）
  onCopy() {
    wx.setClipboardData({ data: this.data.url });
  },
});
