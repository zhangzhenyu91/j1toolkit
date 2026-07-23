// 小程序入口
import { request } from './utils/request';
import { wxLoginCode } from './utils/util';

App({
  globalData: {
    statusBarHeight: 20, // 状态栏高度（供自定义导航栏使用）
    userInfo: null, // 当前登录用户信息
    ready: null, // 启动自检 Promise（自动登录门控，首页等待其结果）
  },

  onLaunch() {
    try {
      const info = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
      this.globalData.statusBarHeight = info.statusBarHeight || 20;
    } catch (e) {
      // 获取失败时使用默认值
    }
    this.globalData.userInfo = wx.getStorageSync('userInfo') || null;
    this.globalData.ready = this.bootstrap();
  },

  // 启动自检（自动登录）：
  // 1. 有本地 token → 调 /user/profile 校验，有效直接进入；
  // 2. token 失效且设备已绑定微信 → 静默 wx.login 换发新 token，无需用户操作；
  // 3. 其余情况 → 返回 false，由首页引导至登录页。
  async bootstrap() {
    const token = wx.getStorageSync('token');
    if (token) {
      try {
        const user = await request({ url: '/api/v1/user/profile', authRedirect: false });
        this.applyUser(user);
        return true;
      } catch (err) {
        // 网络/服务异常：放行使用本地缓存（请求层会各自报错提示）
        if (err.statusCode !== 401) return true;
        // 401：token 已失效，继续尝试静默登录
      }
    }

    if (wx.getStorageSync('canSilentWx')) {
      try {
        const code = await wxLoginCode();
        if (!code) return false;
        const data = await request({
          url: '/api/v1/auth/wx-login',
          method: 'POST',
          data: { code },
          authRedirect: false,
        });
        wx.setStorageSync('token', data.token);
        this.applyUser(data.user);
        return true;
      } catch (err) {
        return false;
      }
    }
    return false;
  },

  // 统一写入用户信息与"可静默微信登录"标记
  applyUser(user) {
    wx.setStorageSync('userInfo', user);
    this.globalData.userInfo = user;
    wx.setStorageSync('canSilentWx', user && user.wx_bound ? '1' : '');
  },
});
