// 共享请求层：统一注入 Bearer token、按请求体归一 Content-Type（multipart 由运行时代生成 boundary）、
// 解析 {code,message,data} / {ok,error} 信封；401/40101 总是清会话，按需广播跳登录。
// multipart 上传、SSE 流式响应与二进制下载仍由各 feature 使用 requestRaw / 自建 fetch 保留特化。
import { applyBodyHeaders } from './http.js';
import {
  broadcastUnauthorized as broadcastSessionExpired,
  clearSession,
  readToken,
} from './session.js';

const NETWORK_MESSAGE = '无法连接服务，请检查网络或稍后重试';

// buildURL 拼接查询参数：空值（undefined/null/空串）忽略；已有查询串用 & 续接。
export function buildURL(path, params = {}) {
  const q = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  });
  const qs = q.toString();
  if (!qs) return path;
  return path + (path.includes('?') ? '&' : '?') + qs;
}

// authHeaders 带 JWT 的请求头（extra 可覆盖 Content-Type 等）。
export function authHeaders(token, extra = {}) {
  return { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(extra || {}) };
}

// createClient 生成一个请求客户端：
// - base：统一路径前缀；token/onUnauthorized 可注入便于测试；
// - envelope：'code' 解析平台信封 {code,message,data}，'ok' 解析安全日信封 {ok,error}；
// - silentUnauthorized：仅跳过广播；401 仍总是清本地会话（登录页密码错误不跳登录）。
// - sessionManaged=false：公开页（分享）不触碰登录会话，也不标记 sessionHandled。
export function createClient({
  base = '',
  token = readToken,
  onUnauthorized = clearSession,
  broadcastUnauthorized = broadcastSessionExpired,
  sessionManaged = true,
  envelope = 'code',
  unauthorizedCodes = [40101],
  networkMessage = NETWORK_MESSAGE,
  emptyData = null,
  fetchImpl = (...args) => fetch(...args),
} = {}) {
  function headersFor(optionHeaders, body, method) {
    return applyBodyHeaders(authHeaders(token(), optionHeaders), body, method);
  }

  function unauthorizedError(body, res) {
    const err = new Error((body && body.message) || '未登录或登录已过期，请重新登录');
    err.code = (body && body.code) || res.status || 401;
    err.status = res.status || 401;
    err.sessionHandled = sessionManaged; // 会话已清理，调用方不再重复跳转；公开页不伪造已处理
    return err;
  }

  async function request(path, options = {}) {
    const { headers: optionHeaders, silentUnauthorized, withMessage, ...rest } = options;
    const url = base + path;
    let res;
    try {
      res = await fetchImpl(url, {
        ...rest,
        headers: headersFor(optionHeaders, rest.body, rest.method || 'GET'),
      });
    } catch (e) {
      const err = new Error(networkMessage);
      err.code = 'network_error';
      throw err;
    }
    const body = await res.json().catch(() => null);
    const code = body && body.code;
    if (res.status === 401 || unauthorizedCodes.includes(code)) {
      onUnauthorized(); // 401 总是清本地会话
      if (!silentUnauthorized) broadcastUnauthorized();
      throw unauthorizedError(body, res);
    }
    if (envelope === 'ok') {
      if (!res.ok || !body || body.ok === false) {
        throw new Error((body && (body.error || body.message)) || `请求失败（HTTP ${res.status}）`);
      }
      return body;
    }
    if (!body || code !== 0) {
      const err = new Error((body && body.message) || `服务返回异常（HTTP ${res.status}）`);
      err.code = body && body.code != null ? body.code : 'http_error';
      err.status = res.status;
      if (body && body.data !== undefined) err.data = body.data;
      throw err;
    }
    if (withMessage) return { ...(body.data || {}), message: body.message || '' };
    return body.data === undefined || body.data === null ? emptyData : body.data;
  }

  // requestRaw 二进制/流式请求：不读 JSON、不解析信封；401 统一清会话后抛错。
  // 2xx 判断与错误文案由调用方按下载/预览场景决定。
  async function requestRaw(path, options = {}) {
    const { headers: optionHeaders, silentUnauthorized, ...rest } = options;
    let res;
    try {
      res = await fetchImpl(base + path, {
        ...rest,
        headers: headersFor(optionHeaders, rest.body, rest.method || 'GET'),
      });
    } catch (e) {
      const err = new Error(networkMessage);
      err.code = 'network_error';
      throw err;
    }
    if (res.status === 401) {
      onUnauthorized(); // 401 总是清本地会话
      if (!silentUnauthorized) broadcastUnauthorized();
      throw unauthorizedError(null, res);
    }
    return res;
  }

  return {
    request,
    requestRaw,
    buildURL: (path, params) => buildURL(base + path, params),
    authHeaders: (extra) => authHeaders(token(), extra),
  };
}
