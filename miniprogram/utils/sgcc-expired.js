// 商旅过期名单：GET /api/v1/sgcc/expired 返回 { list: ["姓名", ...] }（纯字符串数组），
// 兼容旧对象项 { memberName/mobile/name }；统一生成展示名与当日指纹。
function normalizeExpiredNames(list) {
  if (!Array.isArray(list)) return [];
  const names = [];
  list.forEach((item) => {
    let name = '';
    if (typeof item === 'string') name = item;
    else if (item && typeof item === 'object') name = item.memberName || item.mobile || item.name || '';
    name = String(name || '').trim();
    if (name && !names.includes(name)) names.push(name);
  });
  return names;
}

// 指纹：日期 + 排序后的名单（同日同名单不重复弹；名单变化或跨天再弹）
function expiredFingerprint(day, list) {
  const names = normalizeExpiredNames(list);
  if (!names.length) return '';
  return `${day}:${names.slice().sort().join(',')}`;
}

module.exports = { normalizeExpiredNames, expiredFingerprint };
