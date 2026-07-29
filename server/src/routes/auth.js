// 鉴权路由：账号密码登录 / 微信登录 / 退出登录
const express = require('express');
const bcrypt = require('bcryptjs');
const jwt = require('jsonwebtoken');
const axios = require('axios');
const config = require('../config');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const auth = require('../middleware/auth');
const { blacklistToken } = require('../redis');

const router = express.Router();

// 签发 JWT，并计算有效期秒数（供前端展示/续期判断）
function sign(user) {
  const token = jwt.sign({ uid: user.id, username: user.username }, config.jwt.secret, {
    expiresIn: config.jwt.expiresIn,
  });
  const decoded = jwt.decode(token);
  return { token, expires_in: decoded.exp - decoded.iat };
}

function publicUser(u) {
  return {
    id: u.id,
    username: u.username,
    nickname: u.nickname,
    avatar: u.avatar,
    team: u.team,
    role: u.role,
    wx_bound: !!u.openid, // 是否已绑定微信（前端据此允许静默微信登录）
  };
}

// 用微信 code 换取 openid/unionid（失败抛错，调用方自行映射为响应）
async function code2openid(code) {
  const { data } = await axios.get('https://api.weixin.qq.com/sns/jscode2session', {
    params: {
      appid: config.wx.appid,
      secret: config.wx.secret,
      js_code: code,
      grant_type: 'authorization_code',
    },
    timeout: 10000,
  });
  if (data.errcode) {
    throw new Error(data.errmsg || `微信接口错误（${data.errcode}）`);
  }
  return data; // { openid, unionid?, session_key }
}

// POST /api/v1/auth/login 账号密码登录
// 可选携带 wx_code：账号尚未绑定微信时，自动将当前微信号绑定到本账号
router.post('/login', async (req, res, next) => {
  try {
    const { username, password, wx_code: wxCode } = req.body || {};
    if (!username || !password) return fail(res, 400, 40001, '请输入账号和密码');

    const [rows] = await pool.query('SELECT * FROM sys_user WHERE username = ? AND status = 1', [username]);
    const user = rows[0];
    if (!user || !user.password_hash) return fail(res, 401, 40111, '账号或密码错误');

    const matched = await bcrypt.compare(password, user.password_hash);
    if (!matched) return fail(res, 401, 40111, '账号或密码错误');

    // 首次账号密码登录时自动绑定当前微信号（仅当账号未绑定 openid）
    let wxBound = false;
    let bindMessage = '';
    if (!user.openid && wxCode && config.wx.appid && config.wx.secret) {
      try {
        const wxData = await code2openid(wxCode);
        // openid 若已绑定在其他账号（历史上微信登录产生的独立账号）则转移到本账号：
        // 密码验证 + 微信验证双重通过，转移不产生越权
        await pool.query('UPDATE sys_user SET openid = NULL, unionid = NULL WHERE openid = ? AND id <> ?', [
          wxData.openid,
          user.id,
        ]);
        await pool.query('UPDATE sys_user SET openid = ?, unionid = COALESCE(unionid, ?) WHERE id = ?', [
          wxData.openid,
          wxData.unionid || null,
          user.id,
        ]);
        wxBound = true;
        console.log(`[绑定] 当前微信号已绑定到账号 ${user.username}(id=${user.id})`);
      } catch (err) {
        // 绑定失败不影响本次登录，仅记录并告知前端
        bindMessage = '微信号绑定失败，可稍后在登录时重试';
        console.warn(`[绑定] 账号 ${user.username} 绑定微信失败：${err.message}`);
      }
    }

    return ok(res, {
      ...sign(user),
      user: publicUser(user),
      wx_bound: wxBound,
      ...(bindMessage ? { bind_message: bindMessage } : {}),
    });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/auth/app-login 应用登录校验（供外部网页应用调用，如 SafeDayLogs 安全日活动记录平台）：
// 一次完成「账号密码 + 指定应用权限」校验；不签发本平台 JWT，会话由调用方自行管理
router.post('/app-login', async (req, res, next) => {
  try {
    const { username, password, app_key: appKey } = req.body || {};
    if (!username || !password || !appKey) return fail(res, 400, 40001, '请输入账号和密码');

    const [rows] = await pool.query('SELECT * FROM sys_user WHERE username = ? AND status = 1', [username]);
    const user = rows[0];
    if (!user || !user.password_hash) return fail(res, 401, 40111, '账号或密码错误');

    const matched = await bcrypt.compare(password, user.password_hash);
    if (!matched) return fail(res, 401, 40111, '账号或密码错误');

    // 应用权限校验（与 requireApp 中间件同一口径）
    const [permRows] = await pool.query(
      `SELECT a.id FROM sys_user_app ua
       JOIN sys_app a ON a.id = ua.app_id
       WHERE ua.user_id = ? AND a.app_key = ? AND a.status = 1`,
      [user.id, appKey]
    );
    if (!permRows.length) return fail(res, 403, 40301, '暂无该应用的使用权限');

    return ok(res, { user: publicUser(user) });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/auth/wx-login 微信登录：小程序 wx.login 得 code，后端向微信换 openid
// 仅允许已绑定账号的微信号登录；未绑定时返回 40313，提示先账号密码登录一次（登录过程自动完成绑定）
router.post('/wx-login', async (req, res, next) => {
  try {
    const { code } = req.body || {};
    if (!code) return fail(res, 400, 40002, '缺少微信 code');
    if (!config.wx.appid || !config.wx.secret) {
      return fail(res, 503, 50301, '微信登录未配置（WX_APPID / WX_SECRET）');
    }

    let wxData;
    try {
      wxData = await code2openid(code);
    } catch (err) {
      return fail(res, 400, 40012, `微信登录失败：${err.message}`);
    }
    const { openid } = wxData;

    const [rows] = await pool.query('SELECT * FROM sys_user WHERE openid = ?', [openid]);
    const user = rows[0];
    if (!user) {
      // 微信号未绑定任何账号：不再自动创建独立账号，引导用户先用账号密码登录完成绑定
      return fail(res, 403, 40313, '该微信号尚未绑定账号，请先使用账号密码登录一次后再使用微信登录');
    }
    if (user.status !== 1) return fail(res, 403, 40302, '账号已被禁用，请联系管理员');

    return ok(res, { ...sign(user), user: publicUser(user) });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/auth/logout 退出登录：当前 token 加入黑名单
router.post('/logout', auth, async (req, res) => {
  const ttl = (req.tokenExp || 0) - Math.floor(Date.now() / 1000);
  await blacklistToken(req.token, ttl > 0 ? ttl : 60);
  return ok(res, null, '已退出登录');
});

module.exports = router;
