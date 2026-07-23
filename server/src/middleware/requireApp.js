// 应用权限中间件：校验当前用户是否拥有指定应用（app_key）的使用权限
const { pool } = require('../db');
const { fail } = require('../utils/resp');

module.exports = function requireApp(appKey) {
  return async (req, res, next) => {
    try {
      const [rows] = await pool.query(
        `SELECT a.id FROM sys_user_app ua
         JOIN sys_app a ON a.id = ua.app_id
         WHERE ua.user_id = ? AND a.app_key = ? AND a.status = 1`,
        [req.user.id, appKey]
      );
      if (!rows.length) {
        return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
      }
      return next();
    } catch (err) {
      return next(err);
    }
  };
};
