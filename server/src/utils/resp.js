// 统一响应结构：{ code, message, data }，code 为 0 表示成功
exports.ok = (res, data = null, message = 'ok') => res.json({ code: 0, message, data });

// data 可选：业务明细随错误下发（如生成前核验拦截的未通过记录清单，前端据以弹层展示）
exports.fail = (res, httpStatus, code, message, data) => {
  const body = { code, message };
  if (data !== undefined) body.data = data;
  return res.status(httpStatus).json(body);
};
