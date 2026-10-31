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
import { normalizeList } from '../core/list-response.js';
import { createClient } from '../core/request.js';
import { normalizeTemplateList } from './templates.js';

const BASE = '/api/v1/safeday';
// 模板管理端挂 /api/v1/admin/safeday（生产 httpapi 已按 /api/v1/admin/* 门控，模块内二次校验超管）。
const ADMIN_BASE = '/api/v1/admin/safeday';

// isDemo 当前是否演示适配层。
export function isDemo() {
  return getApiMode() !== 'live';
}

// 共享请求层：安全日信封为 {ok,error}，管理端与网盘探测走平台信封；
// multipart 由共享层按 body 类型归一请求头，下载保留 requestRaw 特化。
const client = createClient({ base: BASE, envelope: 'ok' });
const adminClient = createClient({ base: ADMIN_BASE, envelope: 'ok' });
const platformClient = createClient({ emptyData: {} });
const request = client.request;
const adminRequest = adminClient.request;

// adminDownload 管理端二进制下载：注入 Bearer 拉流，返回 fetch Response（配合 saveBlob 落地）。
async function adminDownload(path) {
  const res = await adminClient.requestRaw(path);
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error((body && (body.error || body.message)) || `下载失败（HTTP ${res.status}）`);
  }
  return res;
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
    const res = await client.requestRaw(`/records/${encodeURIComponent(id)}/download`);
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
    try {
      const res = await platformClient.requestRaw('/api/v1/netdisk/spaces');
      return res.ok;
    } catch (e) {
      return false;
    }
  },

  // GET /api/v1/admin/teams 超管班组列表（data 兼容直接数组与 list/teams/items 包装；
  // status===1 为启用，由视图侧过滤）。调用失败时视图静默隐藏切换器。
  listTeams: async () => {
    const data = await platformClient.request('/api/v1/admin/teams');
    return normalizeList(data);
  },

  // ---- 记录模板：普通列表 + 管理端 CRUD（管理端仅超管） ----

  // GET /templates → {templates:[{id,name,mode,sections,isDefault}], defaultId}（不含密钥）
  templateOptions: () => request('/templates'),

  // GET /api/v1/admin/safeday/templates → {templates:[Template], defaultId}（仅 hasApiKey）
  adminTemplates: () => adminRequest('/templates'),

  // POST /api/v1/admin/safeday/templates（multipart：name/sections JSON/apiKey/file）→ {template}
  createTemplate: (formData) => adminRequest('/templates', { method: 'POST', body: formData }),

  // PUT /api/v1/admin/safeday/templates/:id（JSON：name/sections/apiKey；空值保留）→ {template}
  updateTemplate: (id, payload) =>
    adminRequest(`/templates/${encodeURIComponent(id)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // POST /api/v1/admin/safeday/templates/:id/docx（multipart 文件：file）→ {template}
  replaceTemplateDocx: (id, formData) =>
    adminRequest(`/templates/${encodeURIComponent(id)}/docx`, { method: 'POST', body: formData }),

  // POST /api/v1/admin/safeday/templates/:id/default → {defaultId}
  setDefaultTemplate: (id) =>
    adminRequest(`/templates/${encodeURIComponent(id)}/default`, { method: 'POST' }),

  // DELETE /api/v1/admin/safeday/templates/:id → ok
  removeTemplate: (id) =>
    adminRequest(`/templates/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // GET /api/v1/admin/safeday/templates/sample（二进制，配合 saveBlob 落地）
  downloadTemplateSample: () => adminDownload('/templates/sample'),
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
  // 演示模板为虚构条目：只读回填，管理写操作一律拒绝（不伪造成功）
  templateOptions: () =>
    demoRead({
      templates: demo.demoTemplates.map((t) => ({
        id: t.id,
        name: t.name,
        mode: t.mode,
        sections: t.sections,
        isDefault: t.isDefault,
      })),
      defaultId: demo.demoTemplateDefaultId,
    }),
  adminTemplates: () =>
    demoRead({ templates: demo.demoTemplates, defaultId: demo.demoTemplateDefaultId }),
  createTemplate: demoWrite,
  updateTemplate: demoWrite,
  replaceTemplateDocx: demoWrite,
  setDefaultTemplate: demoWrite,
  removeTemplate: demoWrite,
  downloadTemplateSample: demoWrite,
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

// listTeams 超管班组列表（仅 admin 用；请求失败时抛错，视图静默降级）。
export function listTeams() {
  return adapter().listTeams();
}

// ---- 记录模板（普通选择 + 超管管理） ----

// templateOptions 生成表单可选模板（登录 + safe-day 权限；不含密钥）→ {templates,defaultId}。
export function templateOptions() {
  return adapter().templateOptions().then(normalizeTemplateList);
}

// adminTemplates 模板管理列表（仅超管）→ {templates:[Template], defaultId}，Template 只含 hasApiKey。
export function adminTemplates() {
  return adapter().adminTemplates().then(normalizeTemplateList);
}

// createTemplate 新建模板（multipart：name/sections/apiKey/file）→ {template}。
export function createTemplate(formData) {
  return adapter().createTemplate(formData);
}

// updateTemplate 编辑名称/章节/API Key（apiKey 空串表示保留原值）→ {template}。
export function updateTemplate(id, payload) {
  return adapter().updateTemplate(id, payload);
}

// replaceTemplateDocx 上传/替换模板 docx（multipart 文件字段 file）→ {template}。
export function replaceTemplateDocx(id, formData) {
  return adapter().replaceTemplateDocx(id, formData);
}

// setDefaultTemplate 设默认模板 → {defaultId}。
export function setDefaultTemplate(id) {
  return adapter().setDefaultTemplate(id);
}

// removeTemplate 删除自定义模板（内容文件保留，在途记录仍可渲染）。
export function removeTemplate(id) {
  return adapter().removeTemplate(id);
}

// downloadTemplateSample 下载动态模板样例 docx（返回 fetch Response，配合 saveBlob 落地）。
export function downloadTemplateSample() {
  return adapter().downloadTemplateSample();
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
  return platformClient.request(path, options);
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
