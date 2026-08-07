// GLKVM Cloud 平台 API 客户端：以「当前员工账号」代登录平台（用户名 = 壹匣登录账号，
// 密码 = 服务端统一配置的 GLKVM_PASSWORD），代理拉取该员工可见的设备列表
// 会话按员工缓存在内存中，过期或失效时自动重新登录；平台侧会话默认 24h（AuthSessionTTL）
// 信封约定：glkvm 接口 HTTP 恒 200，业务成败看 body.ok / body.code（见 glkvm-cloud dto）
const axios = require('axios');
const config = require('../config');

const client = axios.create({ timeout: 15000 });

// 内存缓存的平台会话：username → { token, expiresAt }
const sessions = new Map();
// 登录单例（按员工）：同一员工并发请求共享同一个登录 Promise，避免重复登录打爆平台
const loggingIn = new Map();

function ensureConfigured() {
  if (!config.kvm.url || !config.kvm.password) {
    const err = new Error('GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）');
    err.expose = true;
    throw err;
  }
}

// 以员工账号登录平台并缓存会话；员工平台账号请勿开启 2FA（代登录不支持二次验证）
async function login(username) {
  ensureConfigured();
  let body;
  try {
    const res = await client.post(`${config.kvm.url}/api/login`, {
      username,
      password: config.kvm.password,
    });
    body = res.data || {};
  } catch (err) {
    throw new Error(`GLKVM 平台连接失败：${err.message}`);
  }
  if (!body.ok || !body.data || !body.data.token) {
    throw new Error('KVM 平台登录失败：请确认你的账号已接入平台、未开启 2FA，且密码与统一密码一致');
  }
  // 平台会话默认 24h，提前 1h 主动过期重登
  sessions.set(username, { token: body.data.token, expiresAt: Date.now() + 23 * 3600 * 1000 });
  return body.data.token;
}

async function getToken(username) {
  const s = sessions.get(username);
  if (s && s.expiresAt > Date.now()) return s.token;
  if (!loggingIn.get(username)) {
    loggingIn.set(username, login(username).finally(() => loggingIn.delete(username)));
  }
  return loggingIn.get(username);
}

// 调 glkvm 接口；会话失效（AUTH_REQUIRED/AUTH_EXPIRED）时清缓存重登一次并重试
async function callGlkvm(path, params, username) {
  let token = await getToken(username);
  for (let retry = 0; retry < 2; retry += 1) {
    let body;
    try {
      const res = await client.get(`${config.kvm.url}${path}`, {
        params,
        headers: { Authorization: `Bearer ${token}` },
      });
      body = res.data || {};
    } catch (err) {
      throw new Error(`GLKVM 平台连接失败：${err.message}`);
    }
    if (body.ok) return body.data;
    if (body.code === 'AUTH_REQUIRED' || body.code === 'AUTH_EXPIRED') {
      sessions.delete(username);
      token = await getToken(username);
      continue;
    }
    throw new Error(`GLKVM 平台返回错误：${body.message || body.code || '未知错误'}`);
  }
  throw new Error('GLKVM 平台登录态刷新失败');
}

// 设备列表（不传 pageSize 平台即返回全部，见 glkvm ListDevices；status 仅接受 online/offline/disabled）
// 可见范围按员工平台账号权限（非 admin 仅见授权设备组）
async function listDevices(query, username) {
  const params = {};
  if (query.q) params.q = String(query.q).slice(0, 64);
  if (['online', 'offline', 'disabled'].includes(query.status)) params.status = query.status;
  if (/^\d+$/.test(String(query.groupId || ''))) params.groupId = query.groupId;
  const data = await callGlkvm('/api/devices', params, username);
  return (data && data.items) || [];
}

// 按平台设备 ID 取 ddns（/web/ 链路与设备子域名都以 ddns 寻址）
async function getDeviceDdns(deviceId, username) {
  const items = await listDevices({}, username);
  const dev = items.find((d) => Number(d.id) === Number(deviceId));
  if (!dev) throw Object.assign(new Error('设备不存在，或当前平台账号对其不可见'), { status: 404 });
  if (!dev.ddns) throw Object.assign(new Error('该设备缺少 ddns 标识'), { status: 400 });
  return dev.ddns;
}

// 经平台 /web/ 链路换设备子域名代理会话（rttysid），供转发点直连设备 fileshare 服务
// 返回 { origin, cookie }：origin 形如 https://<ddns>.kvm.j1net.com，cookie 为 rtty-http-sid
// 注意：/web/ 属平台 legacy authorized 组，只认 sid Cookie（不认 Bearer）
async function getProxySession(deviceId, username) {
  const ddns = await getDeviceDdns(deviceId, username);
  let sid = await getToken(username);
  for (let retry = 0; retry < 2; retry += 1) {
    let res;
    try {
      res = await client.get(
        `${config.kvm.url}/web/${encodeURIComponent(ddns)}/https/${encodeURIComponent('127.0.0.1:443/')}`,
        {
          headers: { Cookie: `sid=${sid}` },
          maxRedirects: 0,
          validateStatus: (s) => s < 500,
        }
      );
    } catch (err) {
      throw new Error(`GLKVM 平台连接失败：${err.message}`);
    }
    if (res.status === 401 || res.status === 403) {
      sessions.delete(username);
      sid = await getToken(username);
      continue;
    }
    const loc = res.headers.location || '';
    const m = loc.match(/^(https?:\/\/[^/]+)\/.*[?&]rttysid=([^&]+)/);
    if (res.status === 302 && m) {
      return { origin: m[1], cookie: `rtty-http-sid=${m[2]}` };
    }
    throw new Error(`平台未签发代理会话（HTTP ${res.status}），设备可能离线`);
  }
  throw new Error('GLKVM 平台登录态刷新失败');
}

module.exports = { listDevices, getSessionToken: getToken, getProxySession };
