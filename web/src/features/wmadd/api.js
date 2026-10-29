// 水印添加 feature API：对接 Go 路由 /api/v1/wmadd/*（go/internal/apps/wmadd），
// 响应契约 {code,message,data}（数字业务码），只发真实请求、不内置演示数据。
// 鉴权：Bearer JWT，取 localStorage 优先、sessionStorage 回退的 'shade.token'（与公共 api.js 同源）。
// 渲染只回图：不传 COS、不入库、不触发 Dify 验证（与出工日志水印链路解耦）。

import { handleUnauthorized, readToken } from '../core/session.js';

const BASE = '/api/v1/wmadd';

// request 统一请求：非 0 业务码抛 Error（带 err.code / err.data）。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  const tk = readToken();
  if (tk) headers.Authorization = `Bearer ${tk}`;
  if (options.body && !(options.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(BASE + path, { ...options, headers });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status})`,
  }));
  if (res.status === 401 || body.code === 40101) handleUnauthorized();
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    err.data = body.data;
    throw err;
  }
  return body.data;
}

// getGeo 按经纬度取当前「地点 + 天气」{location,weather}（高德）；未配置高德键或调用失败回空串字段。
export function getGeo(lng, lat) {
  return request(`/geo?lng=${encodeURIComponent(lng)}&lat=${encodeURIComponent(lat)}`);
}

// towersCache 杆塔坐标页内缓存（同旧页 towerRows「内存缓存一次」口径；仅缓存非空成功结果）。
let towersCache = null;

// listTowers 当前班组杆塔坐标 rows=[[电压等级,线路名称,杆塔号,经度,纬度],...]；
// 空数据抛「杆塔坐标数据为空」（调用方按此文案提示，不缓存、可重试）。
export function listTowers() {
  if (towersCache) return Promise.resolve(towersCache);
  return request('/towers').then((data) => {
    const rows = (data && data.rows) || [];
    if (!rows.length) throw new Error('杆塔坐标数据为空');
    towersCache = rows;
    return rows;
  });
}

// renderWatermark 服务端渲染水印回图 {image:dataURL}：
// payload = {image: 原图 base64 dataURL, wm: {content,time,weather,location,longitude,latitude,antiCode,orientation}}。
export function renderWatermark(payload) {
  return request('/render', { method: 'POST', body: JSON.stringify(payload || {}) });
}
