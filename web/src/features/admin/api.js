// 管理端 API 模块：微信智能应答 / 应用日志 / 管理-应用配置。
//
// 所有后端调用集中封装在本文件，供三个管理视图直接引用，也导出给主代理挂到全局 api。
// 请求风格与 web/src/api.js 的 live 适配层一致：
//   - 响应契约 {code, message, data}，code === 0 为成功，否则抛出带 code 的 Error；
//   - 鉴权使用 sessionStorage 里的 JWT（键 shade.token），请求头 Authorization: Bearer <token>；
//   - HTTP 401 时清除本机会话标记并提示重新登录。
// 本模块只打真实后端（开发期经 vite 代理 /api → Go 服务），不含任何演示/兜底数据；
// 端点尚未实现或服务不可用时抛出明确错误，由视图如实提示，绝不伪造保存成功。
//
// 后端契约（挂载在 /api/v1 之下，均为超级管理员权限，对齐 go/internal/httpapi 的 isAdminPath）：
//   路径唯一入口 /api/v1/wechat-answer/*（go/internal/apps/wechatanswer 的统一管理入口）；
//   /api/v1/wechatanswer/* 是模块兼容挂载的旧路径前缀、POST /reconnect 是旧路径，前端一律不依赖。
//   GET    /api/v1/wechat-answer/status             → data:{status:{configured,token_configured,ws_connected,auto_reconnect,last_ready_at}}
//   POST   /api/v1/wechat-answer/status/reconnect   → data:{status:{...同上}}
//   GET    /api/v1/wechat-answer/groups             → data:{items:[...],groups:[...]}（items 与 groups 同内容；项含 {id,name,group_id,team_id,team_name,last_message_at,rules:[...]}）
//   POST   /api/v1/wechat-answer/groups             {name,group_id,team_id} → data:{group:{...}}
//   PUT    /api/v1/wechat-answer/groups/{id}        {name,team_id}          → data:{group:{...}}
//   DELETE /api/v1/wechat-answer/groups/{id}        → data:{id}
//   GET    /api/v1/wechat-answer/groups/{id}/rules  → data:{items:[rule],rules:[rule]}
//   POST   /api/v1/wechat-answer/groups/{id}/rules  {type,name,enabled,ask_enabled,keys} → data:{rule}
//   PUT    /api/v1/wechat-answer/rules/{id}         {name,enabled,ask_enabled,keys}     → data:{rule}
//   DELETE /api/v1/wechat-answer/rules/{id}         → data:{id}
//   GET    /api/v1/admin/teams                      → data:{list|teams|items:[{id,name,...}]}
//   GET    /api/v1/app/list                         → data:{apps:[{code,name,...}]}
//   GET    /api/v1/app-logs?...                     → data:{items,total,page,page_size}
//   GET    /api/v1/admin/config                     → data:{items:[{key,group,value,sensitive,updated_at}]}
//   PUT    /api/v1/admin/config                     {items:[{key,value}]} → data:{items:[...]}
//
// 规则类型 type：'treeWatch'（输电班组运维）/ 'location'（位置消息），每群每类最多一条。
// 规则响应 rule 形状（敏感 Key 只回配置状态，绝不回显完整值）：
//   {id, group_id, type, name, enabled, complete, ask_enabled,
//    keys_configured:{tree_extract,tree_correct,crossing_extract,crossing_correct,ask_name,chat_digest,location},
//    created_at, updated_at}
// 规则保存 keys 载荷（各项均可省略；提交空串/缺省 = 保持已存值不变，与管理配置敏感项同口径）：
//   {tree_extract, tree_correct, crossing_extract, crossing_correct, ask_name, chat_digest, location}

// 会话标记键与 web/src/api.js 保持一致（本文件不改 api.js，只读写同一存储）。
const TOKEN_KEY = 'shade.token';
const USER_KEY = 'shade.user';

// currentToken 读取当前 JWT（未登录返回空串）。
export function currentToken() {
  try {
    return sessionStorage.getItem(TOKEN_KEY) || '';
  } catch (e) {
    return '';
  }
}

// clearSession 清除本机会话标记（401 时调用）。
function clearSession() {
  try {
    sessionStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(USER_KEY);
  } catch (e) {
    // 忽略
  }
}

// request 原生请求封装：解析 {code,message,data} 契约，失败抛出带 code 的 Error。
async function request(path, options = {}) {
  const { headers: optionHeaders, ...rest } = options;
  const headers = { 'Content-Type': 'application/json', ...(optionHeaders || {}) };
  const token = currentToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  let res;
  try {
    res = await fetch(path, { ...rest, headers });
  } catch (e) {
    const err = new Error('无法连接服务，请检查网络或稍后重试');
    err.code = 'network_error';
    throw err;
  }
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (res.status === 401) {
    clearSession();
    const err = new Error(body.message || '未登录或登录已过期，请重新登录');
    err.code = body.code || 'unauthorized';
    throw err;
  }
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    throw err;
  }
  return body.data == null ? {} : body.data;
}

// ---- 微信智能应答：接入状态 ----

// getWechatAnswerStatus 读取 WeChatPadPro 接入与连接状态。
export function getWechatAnswerStatus() {
  return request('/api/v1/wechat-answer/status').then((data) => data.status || data);
}

// reconnectWechatAnswer 手动重连实时通道（不改任何配置，仅重建连接；走 /status/reconnect 正式路径）。
export function reconnectWechatAnswer() {
  return request('/api/v1/wechat-answer/status/reconnect', { method: 'POST' }).then(
    (data) => data.status || data
  );
}

// ---- 微信智能应答：监听群聊 ----

// listWechatGroups 监听群聊列表（含各群已挂规则摘要）。
export function listWechatGroups() {
  return request('/api/v1/wechat-answer/groups').then((data) => data.groups || []);
}

// createWechatGroup 新增监听群聊；groupId 须以 @chatroom 结尾（服务端校验为准）。
export function createWechatGroup({ name, groupId, teamId }) {
  return request('/api/v1/wechat-answer/groups', {
    method: 'POST',
    body: JSON.stringify({ name, group_id: groupId, team_id: teamId || 0 }),
  }).then((data) => data.group || data);
}

// updateWechatGroup 编辑群备注/归属班组（群 ID 创建后不可改）。
export function updateWechatGroup(id, { name, teamId }) {
  return request(`/api/v1/wechat-answer/groups/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify({ name, team_id: teamId || 0 }),
  }).then((data) => data.group || data);
}

// deleteWechatGroup 删除监听群聊及其规则配置（正式业务记录由服务端保留）。
export function deleteWechatGroup(id) {
  return request(`/api/v1/wechat-answer/groups/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

// ---- 微信智能应答：群规则 ----

// listWechatRules 指定群的规则列表。
export function listWechatRules(groupId) {
  return request(`/api/v1/wechat-answer/groups/${encodeURIComponent(groupId)}/rules`).then(
    (data) => data.rules || []
  );
}

// ruleKeysPayload 把编辑表单里的 Key 载荷归一：空串一律不提交（留空保持不变）。
function ruleKeysPayload(keys) {
  const out = {};
  const src = keys || {};
  for (const field of [
    'tree_extract',
    'tree_correct',
    'crossing_extract',
    'crossing_correct',
    'ask_name',
    'chat_digest',
    'location',
  ]) {
    const v = String(src[field] == null ? '' : src[field]).trim();
    if (v) out[field] = v;
  }
  return out;
}

// ruleBody 组装规则请求体（type 新建时必带，编辑时不改类型）。
function ruleBody(payload) {
  const body = {
    name: String(payload.name || '').trim(),
    enabled: !!payload.enabled,
    keys: ruleKeysPayload(payload.keys),
  };
  if (payload.type) body.type = payload.type;
  if (payload.askEnabled != null) body.ask_enabled = !!payload.askEnabled;
  return body;
}

// createWechatRule 为群新建规则；同群同类型最多一条（服务端校验为准）。
export function createWechatRule(groupId, payload) {
  return request(`/api/v1/wechat-answer/groups/${encodeURIComponent(groupId)}/rules`, {
    method: 'POST',
    body: JSON.stringify(ruleBody(payload)),
  }).then((data) => data.rule || data);
}

// updateWechatRule 编辑规则（名称/启停/提问开关/Key；Key 留空保持不变）。
export function updateWechatRule(ruleId, payload) {
  return request(`/api/v1/wechat-answer/rules/${encodeURIComponent(ruleId)}`, {
    method: 'PUT',
    body: JSON.stringify(ruleBody(payload)),
  }).then((data) => data.rule || data);
}

// deleteWechatRule 删除规则（正式业务记录由服务端保留）。
export function deleteWechatRule(ruleId) {
  return request(`/api/v1/wechat-answer/rules/${encodeURIComponent(ruleId)}`, {
    method: 'DELETE',
  });
}

// ---- 班组字典（群「归属班组」下拉用）----

// listTeams 班组列表；兼容 data.list / data.teams / data.items 三种返回形状。
export function listTeams() {
  return request('/api/v1/admin/teams').then((data) => data.list || data.teams || data.items || []);
}

// ---- 应用日志 ----

// listLogApps 日志子页签的应用目录（GET /api/v1/app/list）。
export function listLogApps() {
  return request('/api/v1/app/list').then((data) => data.apps || []);
}

// listAppLogs 应用日志查询：应用/级别/类别/关键字/时间范围 + 分页。
// params: {appCode, level, category, keyword, since, until, page, pageSize}
//   since/until 为 Date 或 RFC3339 字符串；返回 {items,total,page,pageSize}。
export function listAppLogs({
  appCode = '',
  level = '',
  category = '',
  keyword = '',
  since = '',
  until = '',
  page = 1,
  pageSize = 20,
} = {}) {
  const q = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (appCode) q.set('app_code', appCode);
  if (level) q.set('level', level);
  if (category) q.set('category', category);
  if (keyword) q.set('keyword', keyword);
  if (since) q.set('since', since instanceof Date ? since.toISOString() : String(since));
  if (until) q.set('until', until instanceof Date ? until.toISOString() : String(until));
  return request(`/api/v1/app-logs?${q}`).then((data) => ({
    items: data.items || [],
    total: data.total || 0,
    page: data.page || page,
    pageSize: data.page_size || pageSize,
  }));
}

// ---- 管理-应用配置 ----

// getConfigs 配置清单（敏感值为脱敏形状）；按 group 归并成
// [{group, items:[{key,value,sensitive,updated_at}]}]，分组顺序按首次出现保持稳定。
export function getConfigs() {
  return request('/api/v1/admin/config').then((data) => {
    const items = data.items || [];
    const order = [];
    const byGroup = new Map();
    for (const it of items) {
      const group = it.group || '通用集成';
      if (!byGroup.has(group)) {
        byGroup.set(group, []);
        order.push(group);
      }
      byGroup.get(group).push(it);
    }
    return order.map((group) => ({ group, items: byGroup.get(group) }));
  });
}

// updateConfigs 批量更新配置；只提交填写了新值的项（敏感项留空 = 保持不变，不提交）。
// 返回更新后的分组形状（服务端仍脱敏）。
export function updateConfigs(items) {
  return request('/api/v1/admin/config', {
    method: 'PUT',
    body: JSON.stringify({ items }),
  }).then((data) => {
    const updated = data.items || [];
    const order = [];
    const byGroup = new Map();
    for (const it of updated) {
      const group = it.group || '通用集成';
      if (!byGroup.has(group)) {
        byGroup.set(group, []);
        order.push(group);
      }
      byGroup.get(group).push(it);
    }
    return order.map((group) => ({ group, items: byGroup.get(group) }));
  });
}

// ---- 汇总导出（主代理可整体挂到全局 api）----
export const adminApi = {
  currentToken,
  getWechatAnswerStatus,
  reconnectWechatAnswer,
  listWechatGroups,
  createWechatGroup,
  updateWechatGroup,
  deleteWechatGroup,
  listWechatRules,
  createWechatRule,
  updateWechatRule,
  deleteWechatRule,
  listTeams,
  listLogApps,
  listAppLogs,
  getConfigs,
  updateConfigs,
};

export default adminApi;
