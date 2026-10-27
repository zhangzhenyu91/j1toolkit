// API 适配层：组件只调用本文件导出的方法，不直接 fetch——对接 Go HTTP API 只改这里。
//
// 两种适配器：
// - live（默认，生产口径）：请求 Go HTTP API（go/internal/httpapi，响应契约 {code,message,data}，
//   开发期经 vite 代理 /api → 127.0.0.1:3000）；
// - demo：立即返回 src/demo-data.js 的静态演示数据（文件内明确标注 DEMO ONLY），
//   不发起任何网络请求，不连接真实服务。
//
// 切换方式（联调时）：localStorage.setItem('shade.apiMode', 'demo')，
// 或构建/启动时设置环境变量 VITE_API_MODE=demo。默认 live。

import { ref } from 'vue';
import * as demo from './demo-data.js';
import {
  createWechatRule,
  listWechatGroups,
  listWechatRules,
  updateWechatRule,
} from './features/admin/api.js';

// 读取当前适配器模式：localStorage 优先，其次 VITE_API_MODE，默认 live。
export function getApiMode() {
  try {
    const m = localStorage.getItem('shade.apiMode');
    if (m === 'live' || m === 'demo') return m;
  } catch (e) {
    // localStorage 不可用时忽略
  }
  return (import.meta.env && import.meta.env.VITE_API_MODE) === 'demo' ? 'demo' : 'live';
}

// setApiMode 切换适配器（仅影响本机浏览器，联调用）。
export function setApiMode(mode) {
  try {
    localStorage.setItem('shade.apiMode', mode === 'demo' ? 'demo' : 'live');
  } catch (e) {
    // 忽略
  }
}

// ---- 会话标记与 JWT（键名 features/* 各适配层同源依赖，勿改）----
const USER_KEY = 'shade.user';
const TOKEN_KEY = 'shade.token';

function readSessionUser() {
  try {
    const raw = sessionStorage.getItem(USER_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch (e) {
    return null;
  }
}

const sessionUser = ref(readSessionUser());

// currentUser 当前登录用户资料；响应式引用确保登录、刷新资料和退出后界面同步更新。
export function currentUser() {
  return sessionUser.value;
}

function currentToken() {
  try {
    return sessionStorage.getItem(TOKEN_KEY) || '';
  } catch (e) {
    return '';
  }
}

function saveSession(token, user) {
  try {
    if (user) sessionStorage.setItem(USER_KEY, JSON.stringify(user));
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
  } catch (e) {
    // 忽略
  }
  if (user) sessionUser.value = user;
}

// clearSession 仅清本地会话标记（401 处理等内部场景用）。
function clearSession() {
  try {
    sessionStorage.removeItem(USER_KEY);
    sessionStorage.removeItem(TOKEN_KEY);
  } catch (e) {
    // 忽略
  }
  sessionUser.value = null;
}

// isLoggedIn 是否已登录（本地会话标记 + JWT 均在）。
export function isLoggedIn() {
  return !!currentUser() && !!currentToken();
}

// isAdmin 是否超级管理员（路由守卫与管理入口可见性用）。
export function isAdmin() {
  const u = currentUser();
  return !!u && u.role === 'admin';
}

// UNAUTHORIZED_EVENT 会话失效（401）广播事件名；router.js 监听后跳登录页。
export const UNAUTHORIZED_EVENT = 'shade:unauthorized';

function broadcastUnauthorized() {
  try {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
  } catch (e) {
    // 忽略（非浏览器环境）
  }
}

// ---- live 适配器：对接 Go HTTP API ----
// 路由与 go/internal/httpapi 对齐；微信智能应答的群聊/规则复用 features/admin/api.js 的真实端点。
const live = {
  async request(path, options = {}) {
    const { headers: optionHeaders, silentUnauthorized, ...rest } = options;
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
      if (!silentUnauthorized) broadcastUnauthorized();
    }
    if (body.code !== 0) {
      const err = new Error(body.message || '请求失败');
      err.code = body.code;
      err.status = res.status;
      throw err;
    }
    return body.data;
  },

  // POST /api/v1/auth/login → data:{token,user:{id,username,nickname,role,team_id}}
  async login({ username, password }) {
    return this.request('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password, client: 'web' }),
      silentUnauthorized: true,
    });
  },

  // POST /api/v1/auth/logout（带 JWT；失败也继续清本地会话）
  async logout() {
    return this.request('/api/v1/auth/logout', {
      method: 'POST',
      silentUnauthorized: true,
    });
  },

  // GET /api/v1/user/profile → data:{id,username,nickname,avatar,team,team_id,role,wx_bound,created_at}
  // 与 Go httpapi/account.go 及旧 Node routes/user.js 同路径同形状（平铺资料，无 user 包装）。
  // 401 走标准处理：清会话并广播跳登录页（错误真实展示，不静默回落本地缓存）。
  async profile() {
    return this.request('/api/v1/user/profile');
  },

  // GET /api/v1/app/list
  async listApps() {
    const data = await this.request('/api/v1/app/list');
    return data.apps || [];
  },

  // 微信智能应答群聊/规则：复用 features/admin/api.js 的真实端点（/api/v1/wechat-answer/*），
  // 形状见该文件头注释；旧路径前缀 /wechatanswer 与 POST /reconnect 一律不用。
  async listChatGroups() {
    return listWechatGroups();
  },
  async listRules(groupId) {
    return listWechatRules(groupId);
  },
  // saveRule 保存规则：rule.id 存在走更新，否则按 rule.groupId（或 group_id）新建。
  async saveRule(rule) {
    const payload = {
      type: rule.type,
      name: rule.name,
      enabled: rule.enabled,
      askEnabled: rule.askEnabled != null ? rule.askEnabled : rule.ask_enabled,
      keys: rule.keys,
    };
    if (rule.id) return updateWechatRule(rule.id, payload);
    return createWechatRule(rule.groupId != null ? rule.groupId : rule.group_id, payload);
  },

  // GET /api/v1/app-logs?app_code=&level=&page=&page_size=
  async listAppLogs({ appCode = '', level = '', page = 1, pageSize = 10 } = {}) {
    const q = new URLSearchParams({
      page: String(page),
      page_size: String(pageSize),
    });
    if (appCode) q.set('app_code', appCode);
    if (level) q.set('level', level);
    const data = await this.request(`/api/v1/app-logs?${q}`);
    return {
      items: data.items || [],
      total: data.total || 0,
      page: data.page || page,
      pageSize: data.page_size || pageSize,
    };
  },

  // GET /api/v1/admin/config（服务端只回脱敏值，敏感项绝不回显完整值）
  async getConfigs() {
    const data = await this.request('/api/v1/admin/config');
    return data.items || [];
  },

  // PUT /api/v1/admin/config {items:[{key,value}]}（响应仍为脱敏值）
  async updateConfigs(items) {
    const data = await this.request('/api/v1/admin/config', {
      method: 'PUT',
      body: JSON.stringify({ items }),
    });
    return data.items || [];
  },
};

// ---- demo 适配器：静态演示数据（不请求任何服务）----
const delay = (data) =>
  new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));

const demoAdapter = {
  login: () =>
    delay({
      token: 'demo-token',
      user: { id: 1, username: 'demo', nickname: '演示账号', role: 'admin', team_id: 0 },
    }),

  logout: () => delay({ logged_out: true }),

  profile: () => delay(currentUser() || {}),

  listApps: () => delay(demo.demoApps),

  listChatGroups: () => delay(demo.demoChatGroups),

  listRules: (groupCode) => delay(demo.demoRules[groupCode] || []),

  saveRule: (rule) => delay({ ...rule, id: rule.id || `r-demo-${Date.now()}` }),

  listAppLogs: ({ appCode = '', level = '', page = 1, pageSize = 10 } = {}) => {
    let items = demo.demoLogs;
    if (appCode) items = items.filter((it) => it.app_code === appCode);
    if (level) items = items.filter((it) => it.level === level);
    const total = items.length;
    const start = (page - 1) * pageSize;
    return delay({
      items: items.slice(start, start + pageSize),
      total,
      page,
      pageSize,
    });
  },

  getConfigs: () => delay(demo.demoConfigGroups),

  // 演示保存：仅回显脱敏形状，不做任何持久化
  updateConfigs: (items) =>
    delay(
      items.map((it) => ({
        ...it,
        value: it.sensitive ? '演示****已更新' : it.value,
      }))
    ),
};

function adapter() {
  return getApiMode() === 'demo' ? demoAdapter : live;
}

// ---- 对组件暴露的统一 API（形状稳定，适配器可整体替换）----

// login 登录：成功保存 Go 返回的用户资料与 JWT；失败把服务端 message 原样抛给页面。
export async function login(payload) {
  const data = await adapter().login(payload);
  saveSession(data.token, data.user);
  return data;
}

// getProfile 拉取当前用户资料并刷新本地会话缓存（GET /api/v1/user/profile，平铺形状）。
// 失败如实抛错（服务端 message 原样），不回落缓存伪造成功；401 已由 live.request 清会话并广播跳登录。
export async function getProfile() {
  const data = await adapter().profile();
  const user = (data && data.user) || data || null;
  if (!user || (user.id == null && !user.username)) {
    const err = new Error('用户资料返回数据不完整');
    err.code = 'bad_response';
    throw err;
  }
  saveSession('', user);
  return user;
}

// logout 退出登录：先请求服务端注销（失败忽略），再清本地会话标记与 JWT。
export async function logout() {
  try {
    await adapter().logout();
  } catch (e) {
    // 注销失败也继续清本地会话
  }
  clearSession();
}

// listApps 工作台应用列表。
export function listApps() {
  return adapter().listApps();
}

// listChatGroups 微信智能应答：监听群列表（项含 {id,group_id,name,enabled,team_id,rule_count,...}）。
export function listChatGroups() {
  return adapter().listChatGroups();
}

// listRules 微信智能应答：指定群（listChatGroups 项的 id）的规则列表。
export function listRules(groupId) {
  return adapter().listRules(groupId);
}

// saveRule 微信智能应答：保存规则（有 rule.id 走更新，否则按 rule.groupId 新建；keys 留空保持不变）。
export function saveRule(rule) {
  return adapter().saveRule(rule);
}

// listAppLogs 应用日志：筛选 + 分页。
export function listAppLogs(params) {
  return adapter().listAppLogs(params);
}

// getConfigs 管理配置：分组列表（敏感项脱敏值）。
export function getConfigs() {
  return adapter().getConfigs();
}

// updateConfigs 管理配置：批量更新（响应仍脱敏）。
export function updateConfigs(items) {
  return adapter().updateConfigs(items);
}
