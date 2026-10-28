// 公开分享（免登录）feature API：组件只调用本文件导出的方法。
//
// 直连 Go HTTP API /api/v1/netdisk/public/share/{id}/*（go/internal/apps/netdisk/share.go），
// 信封 {code,message,data}；不挂演示适配层、不写死任何演示数据——分享内容一律来自服务端：
// - GET  /public/share/{id}/          → {creator, expire_at, created_at}（分享信息，不含内容）
// - POST /public/share/{id}/verify    → {ticket, items}（提取码校验，通过发 2h 访问票据）
// - POST /public/share/{id}/list      → {path, items}（目录下钻，须带 ticket 或 password）
// - GET  /public/share/{id}/download  → 文件流（?path=&ticket=，配合 saveBlob 落地）
//
// 鉴权：verify 签发的 ticket 为 2h JWT，存 sessionStorage（按分享 id 隔离），
// list 用 body.ticket、download 用 ?ticket=；票据过期/失效按 403 重新弹提取码框。

const BASE = '/api/v1/netdisk/public/share';

// ticketKey 分享票据的 sessionStorage 键（按分享 id 隔离）。
function ticketKey(shareId) {
  return `shade.shareTicket:${shareId}`;
}

// getTicket / setTicket / clearTicket 票据读写（sessionStorage 不可用时退化为内存 Map）。
const memTickets = new Map();

export function getTicket(shareId) {
  try {
    return sessionStorage.getItem(ticketKey(shareId)) || memTickets.get(shareId) || '';
  } catch (e) {
    return memTickets.get(shareId) || '';
  }
}

export function setTicket(shareId, ticket) {
  memTickets.set(shareId, ticket || '');
  try {
    if (ticket) sessionStorage.setItem(ticketKey(shareId), ticket);
    else sessionStorage.removeItem(ticketKey(shareId));
  } catch (e) {
    /* 内存 Map 兜底 */
  }
}

export function clearTicket(shareId) {
  setTicket(shareId, '');
}

// request 公共请求：解析 {code,message,data} 信封，非 0 抛错（err.code 为数字业务码）。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (!(options.body instanceof FormData) && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(BASE + path, { ...options, headers });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    err.status = res.status;
    throw err;
  }
  return body.data;
}

// shareInfo 分享信息（免登录、无票据）。
export function shareInfo(shareId) {
  return request(`/${encodeURIComponent(shareId)}/`);
}

// shareVerify 提取码校验 → {ticket, items}（成功后票据自动留存）。
export async function shareVerify(shareId, password) {
  const data = await request(`/${encodeURIComponent(shareId)}/verify`, {
    method: 'POST',
    body: JSON.stringify({ password: String(password || '') }),
  });
  if (data && data.ticket) setTicket(shareId, data.ticket);
  return data;
}

// shareList 目录下钻（path 为分享范围内相对路径，'/' 为首层）；票据失效抛 code=40311。
export function shareList(shareId, path, password) {
  const ticket = getTicket(shareId);
  return request(`/${encodeURIComponent(shareId)}/list`, {
    method: 'POST',
    body: JSON.stringify({ path: path || '/', ticket, password: password || '' }),
  });
}

// shareFileUrl 文件取流浏览器直链（?ticket= 票据鉴权，同旧 share.html 的 dlUrl）：
// 供 <a download>/<img>/<video> 直引与原生直链下载（不经内存 Blob，浏览器下载器出进度）。
export function shareFileUrl(shareId, path) {
  const ticket = getTicket(shareId);
  const qs = new URLSearchParams({ path: path || '' });
  if (ticket) qs.set('ticket', ticket);
  return `${BASE}/${encodeURIComponent(shareId)}/download?${qs.toString()}`;
}

// shareDownload 文件下载（返回 fetch Response，配合 saveBlob 落地）。
export async function shareDownload(shareId, path, password) {
  const ticket = getTicket(shareId);
  const qs = new URLSearchParams({ path: path || '' });
  if (ticket) qs.set('ticket', ticket);
  if (password) qs.set('password', password);
  const res = await fetch(`${BASE}/${encodeURIComponent(shareId)}/download?${qs.toString()}`);
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    const err = new Error((body && body.message) || `下载失败（HTTP ${res.status}）`);
    err.code = body && body.code;
    err.status = res.status;
    throw err;
  }
  return res;
}

// saveBlob 响应落地为本地文件：文件名优先 Content-Disposition，缺省 fallbackName。
export async function saveBlob(res, fallbackName) {
  const cd = res.headers.get('Content-Disposition') || '';
  const m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  let name = fallbackName || '下载文件';
  if (m) {
    try {
      name = decodeURIComponent(m[1]);
    } catch (e) {
      name = m[1];
    }
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 4000);
  return name;
}

// normalizeShareId 分享 id 校验（与 Go shareIDRe 同口径：10-32 位十六进制）。
export function normalizeShareId(raw) {
  const s = String(raw || '')
    .trim()
    .toLowerCase();
  return /^[a-f0-9]{10,32}$/.test(s) ? s : '';
}
