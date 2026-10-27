// 账号与权限管理 API 模块：班组 / 员工 / 应用启停排序 / 按人应用授权。
//
// 所有后端调用集中封装在本文件，供 AdminAccountsView 直接引用。
// 请求风格与 web/src/api.js 的 live 适配层一致：
//   - 响应契约 {code, message, data}，code === 0 为成功，否则抛出带 code 的 Error；
//   - 鉴权使用 sessionStorage 里的 JWT（键 shade.token），请求头 Authorization: Bearer <token>；
//   - HTTP 401 时清除本机会话标记并提示重新登录。
// 本模块只打真实后端（开发期经 vite 代理 /api → Go 服务），不含任何演示/兜底数据；
// 操作失败时抛出服务端原文（如「不能删除最后一个超级管理员」），由视图如实提示，绝不伪造保存成功。
//
// 后端契约（挂载在 /api/v1 之下，均为超级管理员权限，对齐 go/internal/apps/adminapi）：
//   GET    /api/v1/admin/teams/full         → data:{list:[{id,name,wxid,kvm_group_name,sort,status,created_at,user_count}]}
//   POST   /api/v1/admin/teams              {name, wxid}                 → data:{id}
//   PUT    /api/v1/admin/teams/{id}         {name?, wxid?, sort?, status?} → data:null
//   DELETE /api/v1/admin/teams/{id}                                    → data:null（有数据引用 40903 改为停用）
//   GET    /api/v1/admin/users?keyword=&team_id= → data:{list:[{id,username,nickname,wxid,team_id,team,role,status,created_at}]}
//   POST   /api/v1/admin/users              {username,password,nickname?,wxid?,team_id?,role?} → data:{id}
//   PUT    /api/v1/admin/users/{id}         {nickname?,wxid?,team_id?,status?,role?,password?} → data:null
//   DELETE /api/v1/admin/users/{id}                                    → data:null（不能删自己/最后一个超管）
//   GET    /api/v1/admin/apps               → data:{list:[{id,app_key,name,icon,path,terminal,sort,status}]}
//   PUT    /api/v1/admin/apps/{id}          {status?, sort?}             → data:null
//   GET    /api/v1/admin/users/{id}/apps    → data:{app_ids:[...]}
//   PUT    /api/v1/admin/users/{id}/apps    {app_ids:[...]}              → data:{app_ids:[...]}（全量替换）
//
// 说明：GET /api/v1/admin/teams（窄列表 id/name）由 httpapi 基础层提供、班组管理需要的
// 成员数/状态/排序在 /admin/teams/full，本模块统一只打 full 口径。

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

// currentUser 当前登录用户资料（未登录返回 null；自我保护标识用）。
export function currentUser() {
  try {
    const raw = sessionStorage.getItem(USER_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch (e) {
    return null;
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

// request 原始请求封装：解析 {code,message,data} 契约，失败抛出带 code 的 Error。
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

// ---- 班组管理 ----

// listTeams 班组全量列表（含停用班组、成员数；按 sort,id 排序）。
export function listTeams() {
  return request('/api/v1/admin/teams/full').then((data) => data.list || []);
}

// createTeam 新增班组（wxid 可空：班组微信群 wxid）。
export function createTeam({ name, wxid }) {
  return request('/api/v1/admin/teams', {
    method: 'POST',
    body: JSON.stringify({ name, wxid: wxid || '' }),
  }).then((data) => data.id);
}

// updateTeam 修改班组（字段按需提交；status 1 启用 0 停用）。
export function updateTeam(id, patch) {
  return request(`/api/v1/admin/teams/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(patch),
  });
}

// deleteTeam 删除班组（班组下仍有员工或业务数据时服务端 40903 拒绝，提示改为停用）。
export function deleteTeam(id) {
  return request(`/api/v1/admin/teams/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// ---- 员工管理 ----

// listUsers 员工列表（keyword 模糊搜索账号/昵称；teamId 为 '' 不筛、0 筛未分组）。
export function listUsers({ keyword = '', teamId = '' } = {}) {
  const params = new URLSearchParams();
  if (keyword.trim()) params.set('keyword', keyword.trim());
  if (teamId !== '' && teamId !== null && teamId !== undefined)
    params.set('team_id', String(teamId));
  const qs = params.toString();
  return request(`/api/v1/admin/users${qs ? `?${qs}` : ''}`).then((data) => data.list || []);
}

// createUser 新建员工账号（role 缺省 user；team_admin 必须带 teamId）。
export function createUser({ username, password, nickname, wxid, teamId, role }) {
  return request('/api/v1/admin/users', {
    method: 'POST',
    body: JSON.stringify({
      username,
      password,
      nickname: nickname || '',
      wxid: wxid || '',
      team_id: teamId == null || teamId === '' ? null : Number(teamId),
      role: role || 'user',
    }),
  }).then((data) => data.id);
}

// updateUser 修改员工（patch 按需：nickname/wxid/team_id/status/role/password）。
export function updateUser(id, patch) {
  return request(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(patch),
  });
}

// deleteUser 删除员工（不能删自己 / 不能删最后一个超级管理员，服务端拒绝时抛原文）。
export function deleteUser(id) {
  return request(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// ---- 应用管理（启停/排序）----

// listApps 全部应用（含停用；按 sort,id 排序）。
export function listApps() {
  return request('/api/v1/admin/apps').then((data) => data.list || []);
}

// updateApp 应用启停/排序（patch 按需：status 1 上架 0 下架；sort 数值小的在前）。
export function updateApp(id, patch) {
  return request(`/api/v1/admin/apps/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(patch),
  });
}

// ---- 按人应用授权 ----

// getUserApps 某员工已授权应用 id 列表。
export function getUserApps(userId) {
  return request(`/api/v1/admin/users/${encodeURIComponent(userId)}/apps`).then(
    (data) => data.app_ids || []
  );
}

// setUserApps 全量替换员工授权（服务端过滤不存在的应用 id，返回实际生效清单）。
export function setUserApps(userId, appIds) {
  return request(`/api/v1/admin/users/${encodeURIComponent(userId)}/apps`, {
    method: 'PUT',
    body: JSON.stringify({ app_ids: appIds }),
  }).then((data) => data.app_ids || []);
}
