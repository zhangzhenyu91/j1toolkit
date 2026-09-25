// 管理员中间件：要求当前用户为 admin 角色（本中间件总是跟在 auth 后使用）
// auth 中间件已实时查库装载 role/status 到 req.user（禁用账号在 auth 处即被拦截），此处直接读 req.user，不再重复查库
const { fail } = require('../utils/resp');

module.exports = function requireAdmin(req, res, next) {
  if (req.user.role !== 'admin') return fail(res, 403, 40304, '仅管理员可执行此操作');
  return next();
};
