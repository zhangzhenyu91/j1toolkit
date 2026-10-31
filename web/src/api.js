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

import * as demo from './demo-data.js';
import { createClient } from './features/core/request.js';
import {
  broadcastUnauthorized,
  clearSession,
  currentUser as sessionCurrentUser,
  readToken,
  saveSession,
  UNAUTHORIZED_EVENT,
} from './features/core/session.js';

// 读取当前适配器模式：显式 VITE_API_MODE 优先，避免历史 localStorage 覆盖演示环境；默认 live。
export function getApiMode() {
  if (import.meta.env && import.meta.env.VITE_API_MODE === 'demo') return 'demo';
  try {
    const m = localStorage.getItem('shade.apiMode');
    if (m === 'live' || m === 'demo') return m;
  } catch (e) {
    // localStorage 不可用时忽略
  }
  return 'live';
}

// ---- 会话标记与 JWT ----
// 会话读写/清理/旧键迁移/失效广播统一在 features/core/session.js（本文件与各 features/* 适配层
// 共用同一口径，避免 401 清理与 token 读取漂移；session.js 不引用本文件，无循环依赖）：
// localStorage 持久化保持登录 + sessionStorage 镜像，401/到期/退出清现键与旧键的双存储；
// 服务端 JWT 有效期不变（JWT_WEB_EXPIRES 短时效）：本地只做持有与清除，绝不续期。

// UNAUTHORIZED_EVENT 会话失效（401）广播事件名；router.js 监听后跳登录页（定义见 session.js）。
export { UNAUTHORIZED_EVENT };

// currentUser 当前登录用户资料；响应式引用确保登录、刷新资料和退出后界面同步更新。
export function currentUser() {
  return sessionCurrentUser();
}

// currentToken 读取当前 JWT（未登录返回空串；localStorage 优先，sessionStorage 镜像兜底）。
export function currentToken() {
  return readToken();
}

// tokenPayload 解析 JWT 载荷（base64url），失败返回 null（同旧 common.js 口径）。
function tokenPayload(token) {
  try {
    const part = String(token || '').split('.')[1];
    if (!part) return null;
    let b64 = part.replace(/-/g, '+').replace(/_/g, '/');
    while (b64.length % 4) b64 += '=';
    return JSON.parse(atob(b64));
  } catch (e) {
    return null;
  }
}

// isTokenExpired 本地 JWT 是否已过期（无 token 或解析不出 exp 一律视为已过期，同旧 common.js 口径；
// demo 适配器的占位 token 非 JWT，演示模式不做判期）。仅本地判读，不向服务端续期。
export function isTokenExpired() {
  const token = currentToken();
  if (!token) return true;
  if (getApiMode() === 'demo') return false;
  const p = tokenPayload(token);
  return !p || !p.exp || p.exp * 1000 <= Date.now();
}

// isLoggedIn 是否已登录（本地会话标记 + JWT 均在且未到期）。
export function isLoggedIn() {
  return !!currentUser() && !!currentToken() && !isTokenExpired();
}

// isAdmin 是否超级管理员（路由守卫与管理入口可见性用）。
export function isAdmin() {
  const u = currentUser();
  return !!u && u.role === 'admin';
}

// 登录态到期巡检（旧 common.js 同口径：每分钟一次）：JWT 到期即清本地登录态并广播跳登录页，
// 覆盖长时间挂机场景；不刷新、不延长服务端有效期。
if (typeof window !== 'undefined' && typeof setInterval === 'function') {
  setInterval(() => {
    if (currentToken() && isTokenExpired()) {
      clearSession();
      broadcastUnauthorized();
    }
  }, 60 * 1000);
}

// ---- live 适配器：对接 Go HTTP API ----
// 路由与 go/internal/httpapi 对齐；微信智能应答的群聊/规则复用 features/admin/api.js 的真实端点。
const liveClient = createClient();

const live = {
  request(path, options = {}) {
    return liveClient.request(path, options);
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
};

// ---- demo 适配器：静态演示数据（不请求任何服务）----
// 演示数据用 JSON round-trip 克隆：profile() 传入的是 Vue 响应式 Proxy（sessionUser），
// structuredClone 无法克隆 Proxy（浏览器抛 DataCloneError 且 Promise 不 settle），JSON 序列化可安全转为普通数据。
const delay = (data) =>
  new Promise((resolve) => setTimeout(() => resolve(JSON.parse(JSON.stringify(data))), 120));

const demoAdapter = {
  login: () =>
    delay({
      token: 'demo-token',
      user: { id: 1, username: 'demo', nickname: '演示账号', role: 'admin', team_id: 0 },
    }),

  logout: () => delay({ logged_out: true }),

  profile: () => delay(currentUser() || {}),

  listApps: () => delay(demo.demoApps),
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
