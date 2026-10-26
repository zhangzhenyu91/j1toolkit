// 小程序入口
import { request } from './utils/request';
import { wxLoginCode } from './utils/util';
// 跨分包共享模块在主包显式登记引用：微信不允许分包间互相 require，共享代码只能放主包，
// 此处仅为向代码质量扫描声明用途（消除「主包未使用的 JS 文件」误报），模块均为工厂/纯函数，无副作用
import './utils/teamgate';
import './utils/wmphoto';
import './utils/tower';
import './utils/photo-url';
import './utils/multipart';

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

App({
  globalData: {
    statusBarHeight: 20, // 状态栏高度（供自定义导航栏使用）
    userInfo: null, // 当前登录用户信息
    ready: null, // 启动自检 Promise（自动登录门控，首页等待其结果）
    theme: 'light', // 系统主题（light/dark，深浅色基座取值；品牌深蓝页首不随其变化）
  },

  onLaunch() {
    try {
      const info = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
      this.globalData.statusBarHeight = info.statusBarHeight || 20;
    } catch (e) {
      // 获取失败时使用默认值
    }
    this.syncTheme();
    this.bindThemeChange();
    this.globalData.userInfo = wx.getStorageSync('userInfo') || null;
    this.globalData.ready = this.bootstrap();
  },

  // 回前台重读主题并恢复监听（onHide 已解除，后台期间的变更在此补齐）
  onShow() {
    this.syncTheme();
    this.bindThemeChange();
  },

  // 退后台解除主题监听（App 无 unload 生命周期，以 hide/show 管理监听）
  onHide() {
    this.unbindThemeChange();
  },

  // 读取系统主题写入 globalData
  syncTheme() {
    this.globalData.theme = readTheme();
  },

  // 监听系统主题切换（先解后绑，避免重复注册）
  bindThemeChange() {
    if (!this._onThemeChange) {
      this._onThemeChange = (res) => {
        this.globalData.theme = res && res.theme === 'dark' ? 'dark' : 'light';
      };
    }
    wx.offThemeChange(this._onThemeChange);
    wx.onThemeChange(this._onThemeChange);
  },

  // 解除主题监听
  unbindThemeChange() {
    if (this._onThemeChange) {
      wx.offThemeChange(this._onThemeChange);
    }
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
