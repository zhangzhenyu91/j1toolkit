// 统一响应结构：{ code, message, data }，code 为 0 表示成功
exports.ok = (res, data = null, message = 'ok') => res.json({ code: 0, message, data });

exports.fail = (res, httpStatus, code, message) =>
  res.status(httpStatus).json({ code, message });
