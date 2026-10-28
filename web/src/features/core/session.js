// 登录会话存储共享助手：web/src/api.js 与各 features/*/api.js 共用的会话读写/清理单一口径，
// 避免各适配层 401 清理与 token 读取漂移。本文件不依赖 api.js（api.js 反向引用本文件），无循环依赖。
// 键名口径：现键 shade.token/shade.user + 旧 Node 页键 shade_token/shade_user；
// localStorage 持久化（关窗口/重开浏览器保持登录）+ sessionStorage 镜像；启动一次性迁移旧存储，现键优先。
// 服务端 JWT 有效期不变（JWT_WEB_EXPIRES 短时效）：本地只做持有与清除（到期/401/退出即清），绝不续期。
import { ref } from 'vue';

const TOKEN_KEY = 'shade.token';
const USER_KEY = 'shade.user';
const LEGACY_TOKEN_KEY = 'shade_token';
const LEGACY_USER_KEY = 'shade_user';

// UNAUTHORIZED_EVENT 会话失效（401）广播事件名；router.js 监听后跳登录页。
export const UNAUTHORIZED_EVENT = 'shade:unauthorized';

function lsGet(key) {
  try {
    return localStorage.getItem(key);
  } catch (e) {
    return null;
  }
}
function lsSet(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch (e) {
    // localStorage 不可用时忽略
  }
}
function lsRemove(key) {
  try {
    localStorage.removeItem(key);
  } catch (e) {
    // 忽略
  }
}
function ssGet(key) {
  try {
    return sessionStorage.getItem(key);
  } catch (e) {
    return null;
  }
}
function ssSet(key, value) {
  try {
    sessionStorage.setItem(key, value);
  } catch (e) {
    // 忽略
  }
}
function ssRemove(key) {
  try {
    sessionStorage.removeItem(key);
  } catch (e) {
    // 忽略
  }
}

// migrateSession 一次性迁移旧存储：现键 localStorage > 旧键 localStorage > 历史 sessionStorage，
// 取到后统一写回现键 localStorage 并镜像 sessionStorage；同时清掉旧键防双份漂移。
function migrateSession() {
  const token = lsGet(TOKEN_KEY) || lsGet(LEGACY_TOKEN_KEY) || ssGet(TOKEN_KEY) || '';
  const user = lsGet(USER_KEY) || lsGet(LEGACY_USER_KEY) || ssGet(USER_KEY) || '';
  if (token) {
    lsSet(TOKEN_KEY, token);
    ssSet(TOKEN_KEY, token);
  }
  if (user) {
    lsSet(USER_KEY, user);
    ssSet(USER_KEY, user);
  }
  lsRemove(LEGACY_TOKEN_KEY);
  lsRemove(LEGACY_USER_KEY);
}
migrateSession();

function readUserRaw() {
  return lsGet(USER_KEY) || ssGet(USER_KEY) || '';
}
function parseUser(raw) {
  try {
    return raw ? JSON.parse(raw) : null;
  } catch (e) {
    return null;
  }
}

// sessionUser 当前登录用户资料（响应式引用确保登录、刷新资料和退出后界面同步更新）。
const sessionUser = ref(parseUser(readUserRaw()));

// currentUser 当前登录用户资料（未登录返回 null）。
export function currentUser() {
  return sessionUser.value;
}

// readToken 当前 JWT（未登录返回空串；localStorage 优先、sessionStorage 镜像回退）。
export function readToken() {
  return lsGet(TOKEN_KEY) || ssGet(TOKEN_KEY) || '';
}

// saveSession 持久化登录态（token/user 为空则跳过对应项）：写 localStorage 并镜像 sessionStorage。
export function saveSession(token, user) {
  if (user) {
    const raw = JSON.stringify(user);
    lsSet(USER_KEY, raw);
    ssSet(USER_KEY, raw);
    sessionUser.value = user;
  }
  if (token) {
    lsSet(TOKEN_KEY, token);
    ssSet(TOKEN_KEY, token);
  }
}

// clearSession 清除本地会话标记（401/到期/退出等场景）：现键与旧键、双存储全清。
export function clearSession() {
  lsRemove(USER_KEY);
  lsRemove(TOKEN_KEY);
  lsRemove(LEGACY_USER_KEY);
  lsRemove(LEGACY_TOKEN_KEY);
  ssRemove(USER_KEY);
  ssRemove(TOKEN_KEY);
  sessionUser.value = null;
}

// broadcastUnauthorized 广播会话失效事件（router.js 监听后跳登录页）。
export function broadcastUnauthorized() {
  try {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
  } catch (e) {
    // 忽略（非浏览器环境）
  }
}

// handleUnauthorized 401 标准处理：清双存储会话标记 + 广播跳登录页。
// 各 feature 适配层的 401 分支统一走此口径，与公共 api.js 保持一致。
export function handleUnauthorized() {
  clearSession();
  broadcastUnauthorized();
}
