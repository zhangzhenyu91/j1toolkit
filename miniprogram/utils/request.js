// 统一请求封装：自动携带 JWT，401 时清理登录态并跳转登录页
// authRedirect=false 时 401 仅拒绝 Promise（用于启动自检等需自行处理跳转的场景）
const { BASE_URL } = require('../config');

function request({ url, method = 'GET', data, header = {}, timeout = 30000, authRedirect = true }) {
  return new Promise((resolve, reject) => {
    const token = wx.getStorageSync('token');
    wx.request({
      url: BASE_URL + url,
      method,
      data,
      timeout,
      header: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...header,
      },
      success(res) {
        const body = res.data || {};
        if (res.statusCode === 401) {
          wx.removeStorageSync('token');
          wx.removeStorageSync('userInfo');
          if (authRedirect) {
            wx.reLaunch({ url: '/pages/login/login' });
          }
          reject(new Error(body.message || '登录已过期，请重新登录'));
          return;
        }
        if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
          resolve(body.data);
          return;
        }
        const err = new Error(body.message || `请求失败（${res.statusCode}）`);
        err.statusCode = res.statusCode;
        reject(err);
      },
      fail() {
        reject(new Error('网络异常，请检查网络后重试'));
      },
    });
  });
}

module.exports = { request };
