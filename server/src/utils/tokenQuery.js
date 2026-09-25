// 文件预览服务器回源拉取下载地址时无法附带请求头：
// 无 Authorization 头且 query 带 token 时，映射为 Authorization: Bearer 再走统一鉴权（出工日志/安全日记录等共用）
module.exports = function tokenQuery(req, res, next) {
  if (!req.headers.authorization && typeof req.query.token === 'string' && req.query.token) {
    req.headers.authorization = `Bearer ${req.query.token}`;
  }
  next();
};
