// 安全日活动记录 feature API：组件只调用本文件导出的方法。
//
// 两种适配器（与 src/api.js 同口径，模式由 getApiMode() 统一读取）：
// - demo：回填 features/safeday/demo.js 的静态演示数据（只读），写操作（生成/删除/存网盘）
//   一律明确拒绝，不发起网络请求、不伪造成功；
// - live：请求 Go HTTP API /api/v1/safeday（go/internal/apps/safeday）。
//
// 信封口径与主平台不同：本模块为 {ok:true,...} / {ok:false,error}（沿用旧 SafeDayLogs 服务），
// 生成接口为 multipart 上传，下载走 fetch + Bearer + Blob 落地（Go 端未开放 ?token= 直链）。

import { getApiMode } from '../../api.js';
import * as demo from './demo.js';

const BASE = '/api/v1/safeday';

// isDemo 当前是否演示适配层。
export function isDemo() {
  return getApiMode() !== 'live';
}

function authToken() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}

// request live 适配层公共请求：注入 Bearer，解析 {ok,error} 信封（非 ok 一律抛错）。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  const token = authToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(BASE + path, { ...options, headers });
  const body = await res.json().catch(() => null);
  if (!res.ok || !body || body.ok === false) {
    throw new Error((body && body.error) || `请求失败（HTTP ${res.status}）`);
  }
  return body;
}

const demoRead = (data) =>
  new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));

function demoWrite() {
  return Promise.reject(
    new Error(
      '演示模式不执行该写操作，请切换 live 适配层（localStorage.setItem("shade.apiMode","live")）'
    )
  );
}

const live = {
  // GET /form-meta → {members, defaults:{superior,recorder}}
  formMeta: (teamId) =>
    request(`/form-meta${teamId ? `?team_id=${encodeURIComponent(teamId)}` : ''}`),

  // POST /generate（multipart：name/date/host/recorder/superior/attendees/absentees/absentReason/files）→ {record}
  generate: (formData) => request('/generate', { method: 'POST', body: formData }),

  // GET /records → {records:[Record]}
  records: (teamId) => request(`/records${teamId ? `?team_id=${encodeURIComponent(teamId)}` : ''}`),

  // GET /records/:id/download（二进制，配合 saveBlob 落地）
  download: async (id) => {
    const headers = {};
    const token = authToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    const res = await fetch(`${BASE}/records/${encodeURIComponent(id)}/download`, { headers });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error((body && body.error) || `下载失败（HTTP ${res.status}）`);
    }
    return res;
  },

  // POST /records/:id/preview → {url}
  preview: (id) => request(`/records/${encodeURIComponent(id)}/preview`),

  // POST /records/:id/save-netdisk {dir, space} → {path}
  saveNetdisk: (id, { dir, space }) =>
    request(`/records/${encodeURIComponent(id)}/save-netdisk`, {
      method: 'POST',
      body: JSON.stringify({ dir, space }),
    }),

  // DELETE /records/:id → 可带 warning（记录已删但文件删除失败）
  remove: (id) => request(`/records/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // GET /api/v1/netdisk/spaces 网盘可用性探测（403 未开通 / 503 未配置 → 不可用）
  probeNetdisk: async () => {
    const headers = {};
    const token = authToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    const res = await fetch('/api/v1/netdisk/spaces', { headers });
    return res.ok;
  },

  // GET /api/v1/admin/teams 超管班组列表（data.list，status===1 为启用）。
  // 注意：Go 重写端暂无该端点（见交付说明），调用失败时视图静默隐藏切换器。
  listTeams: async () => {
    const headers = {};
    const token = authToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    const res = await fetch('/api/v1/admin/teams', { headers });
    const body = await res.json().catch(() => null);
    if (!res.ok || !body || body.code !== 0) {
      throw new Error((body && body.message) || `班组列表加载失败（HTTP ${res.status}）`);
    }
    return (body.data && body.data.list) || [];
  },
};

const demoAdapter = {
  formMeta: () => demoRead(demo.demoFormMeta),
  generate: demoWrite,
  records: () => demoRead({ records: demo.demoRecords }),
  download: demoWrite,
  // 演示预览：拒绝（不指向不存在的演示文件，避免伪成功）
  preview: demoWrite,
  saveNetdisk: demoWrite,
  remove: demoWrite,
  probeNetdisk: () => Promise.resolve(false),
  listTeams: () => Promise.resolve([]),
};

function adapter() {
  return getApiMode() === 'live' ? live : demoAdapter;
}

// ---- 对组件暴露的统一 API ----

// formMeta 生成表单数据源（班组成员名单 + 按班组记忆的默认值）。teamId 仅超管切换班组时传。
export function formMeta(teamId) {
  return adapter().formMeta(teamId);
}

// generate 提交生成（multipart FormData，字段见 live.generate 注释）→ {record}。
export function generate(formData) {
  return adapter().generate(formData);
}

// records 记录列表 → {records}（processing 记录存在时由视图 5s 轮询）。
export function records(teamId) {
  return adapter().records(teamId);
}

// downloadRecord 记录文件下载（返回 fetch Response，配合 saveBlob 落地）。
export function downloadRecord(id) {
  return adapter().download(id);
}

// previewRecord 记录在线预览 → {url}（站内 OO 承载页 / 站外微软查看器）。
export function previewRecord(id) {
  return adapter().preview(id);
}

// saveRecordToNetdisk 保存到网盘（dir 目标目录相对路径，space 'my'|'public'）→ {path}。
export function saveRecordToNetdisk(id, params) {
  return adapter().saveNetdisk(id, params);
}

// deleteRecord 删除记录（连带删除已生成文件；响应可能带 warning）。
export function deleteRecord(id) {
  return adapter().remove(id);
}

// probeNetdisk 网盘可用性探测（不可用时「存网盘」入口不渲染）。
export function probeNetdisk() {
  return adapter().probeNetdisk();
}

// listTeams 超管班组列表（仅 admin 用；端点缺失/失败时抛错，视图静默降级）。
export function listTeams() {
  return adapter().listTeams();
}

// saveBlob 响应落地为本地文件：文件名优先 Content-Disposition，缺省 fallbackName。
export async function saveBlob(res, fallbackName) {
  const cd = res.headers.get('Content-Disposition') || '';
  const m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  let name = fallbackName || '安全日活动记录.docx';
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

// previewRoute 预览地址归一：站内地址（如 /office.html?src=safeday…，Go 端生成的旧页路径）
// 转为 Vue 路由跳转（/office.html → /office）；站外（跨域预签名等）原样返回 external。
export function previewRoute(url) {
  if (!url) return null;
  if (/^https?:\/\//i.test(url)) {
    try {
      if (new URL(url, location.origin).origin !== location.origin) return { external: url };
    } catch (e) {
      return { external: url };
    }
  }
  const u = new URL(url, location.origin);
  let path = u.pathname.replace(/\.html$/, '');
  if (!path.startsWith('/')) path = '/' + path;
  const query = {};
  u.searchParams.forEach((v, k) => {
    query[k] = v;
  });
  return { path, query };
}

// ---- 网盘目录浏览（存网盘目标目录选择器，对应旧页 assets/ndpick.js 的接口口径） ----
// 平台信封 {code,message,data}（与 /api/v1/netdisk/* 主平台接口一致，不同于本模块 {ok,error}）。

// platformRequest 平台信封请求：注入 Bearer，code!==0 一律抛错。
async function platformRequest(path, options = {}) {
  if (isDemo()) {
    throw new Error('演示模式不提供网盘目录浏览，请切换 live 适配层');
  }
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  const token = authToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(path, { ...options, headers });
  const body = await res.json().catch(() => null);
  if (!res.ok || !body || body.code !== 0) {
    throw new Error((body && body.message) || `请求失败（HTTP ${res.status}）`);
  }
  return body.data || {};
}

// netdiskSpaces 空间分段数据源 → [{key,name}]（仅 my/public；未下发则空数组，分段隐藏）。
export function netdiskSpaces() {
  return platformRequest('/api/v1/netdisk/spaces').then((d) =>
    (d.spaces || [])
      .filter((s) => s && (s.key === 'my' || s.key === 'public'))
      .map((s) => ({ key: s.key, name: s.name || (s.key === 'public' ? '公共区' : '我的空间') }))
  );
}

// netdiskDirs 目录列表（仅文件夹，按中文名排序）：path 为 '/'（根）或 '/相对路径'。
export function netdiskDirs(space, path) {
  return platformRequest('/api/v1/netdisk/list', {
    method: 'POST',
    body: JSON.stringify({ space, path }),
  }).then((d) =>
    (d.items || [])
      .filter((x) => x && x.is_dir)
      .sort((a, b) => String(a.name).localeCompare(String(b.name), 'zh'))
  );
}

// netdiskMkdir 就地新建文件夹（path 当前浏览目录，name 文件夹名）。
export function netdiskMkdir(space, path, name) {
  return platformRequest('/api/v1/netdisk/mkdir', {
    method: 'POST',
    body: JSON.stringify({ space, path, name }),
  });
}
