// 统一请求封装：自动携带 JWT，401 时清理登录态并跳转登录页
// authRedirect=false 时 401 仅拒绝 Promise（用于启动自检等需自行处理跳转的场景）
// withMessage=true 时 resolve 改为 { data, message }（调用方需要信封 message，如批量操作汇总文案）
const { BASE_URL } = require('../config');
const { createSingleFlight } = require('./singleflight');

// 并发 401 只跳转一次（多个请求同时过期时避免反复 reLaunch 覆盖登录页）
const redirectOnce = createSingleFlight();

function clearAuthStorage() {
  wx.removeStorageSync('token');
  wx.removeStorageSync('userInfo');
}

// 统一 401 收口：清登录态 + 单飞跳登录页；手写 wx.request 的页面（Call Me / Safeday 等）复用
function handleUnauthorized() {
  clearAuthStorage();
  redirectOnce.run(
    () =>
      new Promise((resolve) => {
        wx.reLaunch({ url: '/pages/login/login', complete: resolve, fail: resolve });
      })
  );
}

function request({
  url,
  method = 'GET',
  data,
  header = {},
  timeout = 30000,
  authRedirect = true,
  withMessage = false,
}) {
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
          if (authRedirect) handleUnauthorized();
          else clearAuthStorage();
          const err = new Error(body.message || '登录已过期，请重新登录');
          err.statusCode = 401; // 与非 2xx 分支口径一致（app.js 启动自检据此判定 token 失效走静默续期）
          reject(err);
          return;
        }
        if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
          resolve(withMessage ? { data: body.data, message: body.message || '' } : body.data);
          return;
        }
        const err = new Error(body.message || `请求失败（${res.statusCode}）`);
        err.statusCode = res.statusCode;
        err.code = body.code; // 业务错误码（如 40313 微信未绑定），供调用方区分场景
        reject(err);
      },
      fail() {
        reject(new Error('网络异常，请检查网络后重试'));
      },
    });
  });
}

// uploadFile 二进制上传：自动携带 JWT，按统一 {code,message,data} 信封处理。
function uploadFile({
  url,
  filePath,
  name = 'image',
  data = {},
  header = {},
  timeout = 120000,
  authRedirect = true,
}) {
  return new Promise((resolve, reject) => {
    const token = wx.getStorageSync('token');
    wx.uploadFile({
      url: BASE_URL + url,
      filePath,
      name,
      formData: data,
      timeout,
      header: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...header,
      },
      success(res) {
        let body = {};
        try {
          body = JSON.parse(res.data || '{}');
        } catch (e) {
          body = { message: '服务返回异常' };
        }
        if (res.statusCode === 401) {
          if (authRedirect) handleUnauthorized();
          else clearAuthStorage();
          const err = new Error(body.message || '登录已过期，请重新登录');
          err.statusCode = 401;
          reject(err);
          return;
        }
        if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
          resolve(body.data);
          return;
        }
        const err = new Error(body.message || `请求失败（${res.statusCode}）`);
        err.statusCode = res.statusCode;
        err.code = body.code;
        reject(err);
      },
      fail() {
        reject(new Error('网络异常，请检查网络后重试'));
      },
    });
  });
}

module.exports = { request, uploadFile, handleUnauthorized };
