// JWT 鉴权中间件：校验 Authorization: Bearer <token>，并检查黑名单（退出登录的 token）
const jwt = require('jsonwebtoken');
const config = require('../config');
const { isBlacklisted } = require('../redis');
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

  req.user = { id: payload.uid, username: payload.username };
  req.token = token;
  req.tokenExp = payload.exp;
  return next();
};
