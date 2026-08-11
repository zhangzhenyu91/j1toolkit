// JWT 鉴权中间件：校验 Authorization: Bearer <token>，并检查黑名单（退出登录的 token）
// 校验通过后实时查库装载完整用户行（昵称/角色/班组/状态），角色与班组调整即时生效
const jwt = require('jsonwebtoken');
const config = require('../config');
const { isBlacklisted } = require('../redis');
const { pool } = require('../db');
const { fail } = require('../utils/resp');

module.exports = async function auth(req, res, next) {
  const header = req.headers.authorization || '';
  const token = header.startsWith('Bearer ') ? header.slice(7) : null;
  if (!token) return fail(res, 401, 40101, '未登录或登录已过期');

  let payload;
  try {
    payload = jwt.verify(token, config.jwt.secret);
  } catch (err) {
    return fail(res, 401, 40101, '未登录或登录已过期');
  }

  if (await isBlacklisted(token)) {
    return fail(res, 401, 40102, '登录已失效，请重新登录');
  }

  try {
    const [rows] = await pool.query(
      'SELECT id, username, nickname, role, team_id, status FROM sys_user WHERE id = ?',
      [payload.uid]
    );
    const user = rows[0];
    if (!user || user.status !== 1) return fail(res, 401, 40103, '账号不存在或已被禁用');
    req.user = user;
  } catch (err) {
    return next(err);
  }
  req.token = token;
  req.tokenExp = payload.exp;
  return next();
};

