// 应用权限：校验当前用户是否拥有指定应用（app_key）的使用权限
// hasAppPermission 为统一判定入口（单 key 或 key 数组均可，数组按「任一命中」口径），
// 供默认中间件与路由层（auth/callme 等）复用，权限 SQL 只保留这一处
const { pool } = require('../db');
const { fail } = require('../utils/resp');

// 有任一启用应用（sys_app.status = 1）授权即为 true
async function hasAppPermission(userId, appKeyOrKeys) {
  const keys = (Array.isArray(appKeyOrKeys) ? appKeyOrKeys : [appKeyOrKeys])
    .map((k) => String(k || '').trim())
    .filter(Boolean);
  if (!keys.length) return false;
  const [rows] = await pool.query(
    `SELECT a.id FROM sys_user_app ua
     JOIN sys_app a ON a.id = ua.app_id
     WHERE ua.user_id = ? AND a.app_key IN (?) AND a.status = 1`,
    [userId, keys]
  );
  return rows.length > 0;
}

// 默认导出保持既有用法：requireApp(appKey) 返回 Express 中间件
module.exports = function requireApp(appKey) {
  return async (req, res, next) => {
    try {
      if (!(await hasAppPermission(req.user.id, appKey))) {
        return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
      }
      return next();
    } catch (err) {
      return next(err);
    }
  };
};
module.exports.hasAppPermission = hasAppPermission;
