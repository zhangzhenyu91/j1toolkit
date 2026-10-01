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

// ===== 登录口令接口限流（纯内存，防爆破）：同一 IP 在窗口期内失败达上限即拒绝，成功登录清零 =====
const LOGIN_FAIL_WINDOW_MS = 10 * 60 * 1000; // 限流统计窗口：10 分钟
const LOGIN_FAIL_MAX = 20; // 窗口期内允许的最大失败次数
const loginFailMap = new Map(); // ip -> { count: 失败次数, start: 窗口起点时间戳 }

// 是否已触发限流（只查询，不计数）
function isLoginLimited(ip) {
  const rec = loginFailMap.get(ip);
  return !!rec && Date.now() - rec.start < LOGIN_FAIL_WINDOW_MS && rec.count >= LOGIN_FAIL_MAX;
}

// 记录一次登录失败（窗口过期则重新起算）
function recordLoginFail(ip) {
  const now = Date.now();
  const rec = loginFailMap.get(ip);
  if (!rec || now - rec.start >= LOGIN_FAIL_WINDOW_MS) {
    loginFailMap.set(ip, { count: 1, start: now });
  } else {
    rec.count += 1;
  }
}

// 定期清理过期限流记录，避免 Map 无限增长（unref 不阻碍进程退出）
setInterval(() => {
  const now = Date.now();
  for (const [ip, rec] of loginFailMap) {
    if (now - rec.start >= LOGIN_FAIL_WINDOW_MS) loginFailMap.delete(ip);
  }
}, LOGIN_FAIL_WINDOW_MS).unref();

// 签发 JWT，并计算有效期秒数（供前端展示/续期判断）
// expiresIn 可覆盖默认时效：网页端登录（client='web'）用 JWT_WEB_EXPIRES 短时效
function sign(user, expiresIn) {
  const token = jwt.sign({ uid: user.id, username: user.username }, config.jwt.secret, {
    expiresIn: expiresIn || config.jwt.expiresIn,
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
    team: u.team_name || '', // 班组名（sys_team 关联；未分配为空串）
    team_id: u.team_id || null,
    role: u.role,
    wx_bound: !!u.openid, // 是否已绑定微信（前端据此允许静默微信登录）
  };
}

// 登录类查询统一口径：联出班组名
const USER_SELECT =
  'SELECT u.*, t.name AS team_name FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id';

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
    const ip = req.ip || '';
    if (isLoginLimited(ip)) return fail(res, 429, 42901, '尝试过于频繁，请稍后再试');
    const { username, password, wx_code: wxCode, client } = req.body || {};
    if (!username || !password) return fail(res, 400, 40001, '请输入账号和密码');

    const [rows] = await pool.query(`${USER_SELECT} WHERE u.username = ? AND u.status = 1`, [username]);
    const user = rows[0];
    if (!user || !user.password_hash) {
      recordLoginFail(ip);
      return fail(res, 401, 40111, '账号或密码错误');
    }

    const matched = await bcrypt.compare(password, user.password_hash);
    if (!matched) {
      recordLoginFail(ip);
      return fail(res, 401, 40111, '账号或密码错误');
    }

    // 首次账号密码登录时自动绑定当前微信号（仅当账号未绑定 openid）
    let wxBound = false;
    let bindMessage = '';
    if (!user.openid && wxCode && config.wx.appid && config.wx.secret) {
      try {
        const wxData = await code2openid(wxCode);
        // openid 若已绑定在其他账号（历史上微信登录产生的独立账号）则转移到本账号：
        // 密码验证 + 微信验证双重通过，转移不产生越权；
        // 两条 UPDATE 包在同一事务：避免「先清他号、后绑本号」中途失败导致该 openid 无人持有
        const conn = await pool.getConnection();
        try {
          await conn.beginTransaction();
          await conn.query('UPDATE sys_user SET openid = NULL, unionid = NULL WHERE openid = ? AND id <> ?', [
            wxData.openid,
            user.id,
          ]);
          await conn.query('UPDATE sys_user SET openid = ?, unionid = COALESCE(unionid, ?) WHERE id = ?', [
            wxData.openid,
            wxData.unionid || null,
            user.id,
          ]);
          await conn.commit();
        } catch (err) {
          await conn.rollback().catch(() => {});
          throw err;
        } finally {
          conn.release();
        }
        wxBound = true;
        user.openid = wxData.openid; // 内存同步绑定结果：下方 publicUser 的 wx_bound 即时为真
        console.log(`[绑定] 当前微信号已绑定到账号 ${user.username}(id=${user.id})`);
      } catch (err) {
        // 绑定失败不影响本次登录，仅记录并告知前端
        bindMessage = '微信号绑定失败，可稍后在登录时重试';
        console.warn(`[绑定] 账号 ${user.username} 绑定微信失败：${err.message}`);
      }
    }

    loginFailMap.delete(ip); // 登录成功：清零该 IP 的失败计数
    // 网页端（client='web'）签发短时效 token：到期服务端即拒绝，前端同步清除登录态
    return ok(res, {
      ...sign(user, client === 'web' ? config.jwt.webExpiresIn : undefined),
      user: publicUser(user),
      wx_bound: wxBound,
      ...(bindMessage ? { bind_message: bindMessage } : {}),
    });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/auth/app-login 应用登录校验（供外部应用复用本平台账号体系做登录校验）：
// 一次完成「账号密码 + 指定应用权限」校验；不签发本平台 JWT，会话由调用方自行管理
router.post('/app-login', async (req, res, next) => {
  try {
    const ip = req.ip || '';
    if (isLoginLimited(ip)) return fail(res, 429, 42901, '尝试过于频繁，请稍后再试');
    const { username, password, app_key: appKey } = req.body || {};
    if (!username || !password || !appKey) return fail(res, 400, 40001, '请输入账号和密码');

    const [rows] = await pool.query(`${USER_SELECT} WHERE u.username = ? AND u.status = 1`, [username]);
    const user = rows[0];
    if (!user || !user.password_hash) {
      recordLoginFail(ip);
      return fail(res, 401, 40111, '账号或密码错误');
    }

    const matched = await bcrypt.compare(password, user.password_hash);
    if (!matched) {
      recordLoginFail(ip);
      return fail(res, 401, 40111, '账号或密码错误');
    }

    // 应用权限校验（与 requireApp 中间件同一口径）
    const [permRows] = await pool.query(
      `SELECT a.id FROM sys_user_app ua
       JOIN sys_app a ON a.id = ua.app_id
       WHERE ua.user_id = ? AND a.app_key = ? AND a.status = 1`,
      [user.id, appKey]
    );
    if (!permRows.length) return fail(res, 403, 40301, '暂无该应用的使用权限');

    loginFailMap.delete(ip); // 校验成功：清零该 IP 的失败计数
    return ok(res, { user: publicUser(user) });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/auth/wx-login 微信登录：小程序 wx.login 得 code，后端向微信换 openid
// 仅允许已绑定账号的微信号登录；未绑定时返回 40313，提示先账号密码登录一次（登录过程自动完成绑定）
router.post('/wx-login', async (req, res, next) => {
  try {
    const ip = req.ip || '';
    if (isLoginLimited(ip)) return fail(res, 429, 42901, '尝试过于频繁，请稍后再试');
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

    const [rows] = await pool.query(`${USER_SELECT} WHERE u.openid = ?`, [openid]);
    const user = rows[0];
    if (!user) {
      // 微信号未绑定任何账号：不再自动创建独立账号，引导用户先用账号密码登录完成绑定
      recordLoginFail(ip);
      return fail(res, 403, 40313, '该微信号尚未绑定账号，请先使用账号密码登录一次后再使用微信登录');
    }
    if (user.status !== 1) {
      recordLoginFail(ip);
      return fail(res, 403, 40302, '账号已被禁用，请联系管理员');
    }

    loginFailMap.delete(ip); // 登录成功：清零该 IP 的失败计数
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
