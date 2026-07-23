// 管理员中间件：要求当前用户为 admin 角色（每次实时查库，角色变更即时生效）
const { pool } = require('../db');
const { fail } = require('../utils/resp');

module.exports = async function requireAdmin(req, res, next) {
  try {
    const [rows] = await pool.query('SELECT role, status FROM sys_user WHERE id = ?', [req.user.id]);
    const user = rows[0];
    if (!user || user.status !== 1) return fail(res, 403, 40303, '账号不可用');
    if (user.role !== 'admin') return fail(res, 403, 40304, '仅管理员可执行此操作');
    return next();
  } catch (err) {
    return next(err);
  }
};
