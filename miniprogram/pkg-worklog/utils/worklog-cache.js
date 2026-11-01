// 出工日志预拉缓存 key：日期 + 视图 + 班组；切换任一维度后旧响应/旧日列表都不得落地。
function normalizeTeam(teamId) {
  return Number(teamId) || 0;
}

function dayCacheKey(dateStr, scope, teamId) {
  return `${dateStr || ''}|${scope || 'all'}|${normalizeTeam(teamId)}`;
}

function isCurrentDayKey(key, dateStr, scope, teamId) {
  if (!key) return false;
  return key === dayCacheKey(dateStr, scope, teamId);
}

// inflight 回收：只删除自己那一代 Promise，避免旧请求 finally 删掉切回同 key 后的新请求。
function releaseInflight(map, key, promise) {
  if (map && map[key] === promise) delete map[key];
}

module.exports = { dayCacheKey, isCurrentDayKey, releaseInflight };
