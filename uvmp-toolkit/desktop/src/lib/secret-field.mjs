// 密码类字段的脱敏读写：核心可能只回传掩码/空值 + has_password，
// 界面永远不回显掩码，留空表示「不修改」，由后端保留旧值。
const MASK = '******';

export function isMaskedSecret(value) {
  return String(value == null ? '' : value) === MASK;
}

/**
 * 从参数里读出密码字段状态。
 * 返回 { clear: 可直接复用的明文（旧版核心才会给）, hasStored: 是否已保存过密码 }
 */
export function readSecretField(params = {}) {
  const raw = params == null ? '' : params.password;
  const masked = isMaskedSecret(raw);
  const clear = typeof raw === 'string' && raw !== '' && raw !== MASK ? raw : '';
  const flag = !!(params && (params.has_password || params.password_set));
  // 掩码本身就是「已保存过密码」的证据，不需要额外标记
  return { clear, hasStored: masked || flag || clear !== '' };
}

/**
 * 解析要写回配置的密码：输入为空/掩码时保留旧值，都拿不到就交给后端「空值不改」。
 * 返回 { value: string, keepExisting: boolean }
 */
export function resolveSecret(input, storedClear = '') {
  const typed = typeof input === 'string' ? input : '';
  if (typed !== '' && typed !== MASK) return { value: typed, keepExisting: false };
  if (typeof storedClear === 'string' && storedClear !== '' && storedClear !== MASK) {
    return { value: storedClear, keepExisting: true };
  }
  return { value: '', keepExisting: true };
}

export function secretHint(hasStored) {
  return hasStored ? '已保存密码，留空则不修改' : '未保存密码，请填写';
}

/**
 * 保存实例参数时合并密码：新值优先；未填时沿用已存明文；
 * 已存的是掩码则丢弃——掩码不是密码，绝不能写回配置文件。
 */
export function carrySecret(existing = {}, next = {}) {
  const merged = { ...(existing || {}), ...(next || {}) };
  if (Object.prototype.hasOwnProperty.call(next || {}, 'password')) return merged;
  const old = (existing || {}).password;
  if (typeof old === 'string' && old !== '' && old !== MASK) {
    merged.password = old;
  } else {
    delete merged.password;
  }
  return merged;
}

/**
 * 启动参数里的密码占位符要整键删除：核心 masked_view 只回传空串 + has_password，
 * 掩码/空串都不是密码，带下去会覆盖配置里已存的真实值。
 * 保留 has_password 之类标记（核心用它判断「已配置」），只删 password 本身。
 */
export function stripSecretPlaceholders(params = {}) {
  const source = params && typeof params === 'object' ? params : {};
  const result = { ...source };
  const raw = result.password;
  if (raw == null || raw === '' || raw === MASK) delete result.password;
  return result;
}

export { MASK };
