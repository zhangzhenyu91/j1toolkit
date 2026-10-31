// 班组数据 feature API：直连 Go 服务 /api/v1/teamdata（go/internal/apps/teamdata）。
// 响应契约 {code,message,data}；本文件统一解包，失败抛出带 code 的 Error（err.data 附业务明细）。
// 全部命名导出，便于主代理在公共 api.js 统一适配/替换；不修改公共装配文件。
// 鉴权与 web/src/api.js 同一 JWT（localStorage 优先、sessionStorage 回退的 'shade.token'）。
// 班组隔离对齐旧版 td()/teamBody() 口径：GET/DELETE 走 query 带 team_id，POST/PUT 走 body 带 team_id，
// 导入 FormData 带 team_id（后端 resolveReqTeam：超管可指定班组、班管固定本班）；0/空=不带，落默认班组。

import { normalizeList } from '../core/list-response.js';
import { handleUnauthorized } from '../core/session.js';
import { createClient } from '../core/request.js';

const BASE = '/api/v1/teamdata';

// teamId 当前生效班组（0=未指定，由后端落默认班组；班组管理员恒本班）。
let teamId = 0;

// setTeamId 设定后续请求的生效班组（超管班组切换器写入；0=不指定）。
export function setTeamId(id) {
  teamId = Number(id) || 0;
}

// getTeamId 读取当前生效班组。
export function getTeamId() {
  return teamId;
}

// withTeam GET/DELETE 路径拼 team_id（空值忽略）。
function withTeam(path) {
  return buildURL(path, teamId ? { team_id: teamId } : {});
}

// teamBody POST/PUT body 注入 team_id（0=不带）。
function teamBody(payload = {}) {
  return teamId ? { team_id: teamId, ...payload } : { ...payload };
}

// request 平台信封请求（共享请求层）：统一 Bearer、信封解包与 401 处理；
// buildURL/authHeaders 保留给下载与导入 FormData 等特化路径。
const client = createClient();
function authHeaders(extra = {}) {
  return client.authHeaders(extra);
}
function buildURL(path, params = {}) {
  return client.buildURL(path, params);
}
export function request(path, options = {}) {
  return client.request(path, options);
}

// saveBlob 触发浏览器保存。
export function saveBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// parseDisposition 解析 Content-Disposition 的文件名（优先 RFC5987 filename*）。
function parseDisposition(header, fallback) {
  if (!header) return fallback;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(header);
  if (star) {
    try {
      return decodeURIComponent(star[1]);
    } catch (e) {
      // 解码失败回退默认名
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header);
  return plain ? plain[1] : fallback;
}

// download 模板下载并直接保存。
export async function downloadTo(path, params = {}, fallbackName = 'download') {
  const res = await fetch(buildURL(path, { ...(teamId ? { team_id: teamId } : {}), ...params }), {
    headers: authHeaders(),
  });
  const type = res.headers.get('Content-Type') || '';
  if (type.includes('application/json')) {
    const body = await res.json().catch(() => ({
      code: 'http_error',
      message: `服务返回异常（HTTP ${res.status}）`,
    }));
    if (res.status === 401 || body.code === 40101) handleUnauthorized();
    if (body.code !== 0) {
      const err = new Error(body.message || '下载失败');
      err.code = body.code;
      err.data = body.data;
      throw err;
    }
    throw new Error('服务未返回文件内容');
  }
  if (res.status === 401) handleUnauthorized();
  if (!res.ok) throw new Error(`下载失败（HTTP ${res.status}）`);
  saveBlob(
    await res.blob(),
    parseDisposition(res.headers.get('Content-Disposition'), fallbackName)
  );
}

// uploadForm 上传 Excel（multipart，字段名 file；导入按表单优先口径带 team_id）。
async function uploadForm(path, file, fields = {}) {
  const form = new FormData();
  form.append('file', file);
  Object.entries(teamBody(fields)).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') form.append(k, String(v));
  });
  const res = await fetch(path, { method: 'POST', headers: authHeaders(), body: form });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (res.status === 401 || body.code === 40101) handleUnauthorized();
  if (body.code !== 0) {
    const err = new Error(body.message || '导入失败');
    err.code = body.code;
    err.data = body.data;
    throw err;
  }
  return body.data;
}

// ---- 班组列表（超管班组切换器数据源）----

// listTeams 班组列表（GET /admin/teams）：data 兼容直接数组与 list/teams/items 包装，统一归一为数组。
export function listTeams() {
  return request('/api/v1/admin/teams').then((data) => normalizeList(data));
}

// ---- 班组字典（车牌 / 目的地 / 出工成员）----

// listDict 字典列表（kind=vehicles|destinations|members）。
export function listDict(kind) {
  return request(withTeam(`${BASE}/${kind}`));
}

// createDict 新增字典项 {name}（sort 排尾）。
export function createDict(kind, payload) {
  return request(withTeam(`${BASE}/${kind}`), {
    method: 'POST',
    body: JSON.stringify(teamBody(payload)),
  });
}

// updateDict 修改字典项（name/sort/status；目的地另有 out_of_city/stationed 互斥标记）。
export function updateDict(kind, id, payload) {
  return request(withTeam(`${BASE}/${kind}/${id}`), {
    method: 'PUT',
    body: JSON.stringify(teamBody(payload)),
  });
}

// deleteDict 删除字典项（有日志引用 → 40901 改为停用）。
export function deleteDict(kind, id) {
  return request(withTeam(`${BASE}/${kind}/${id}`), { method: 'DELETE' });
}

// moveMember 成员排序（direction=up|down，与相邻行换位）。
export function moveMember(id, direction) {
  return request(withTeam(`${BASE}/members/${id}/move`), {
    method: 'PUT',
    body: JSON.stringify(teamBody({ direction })),
  });
}

// ---- 运维线路 team_line ----

// listOplines 运维线路列表（sort 序）。
export function listOplines() {
  return request(withTeam(`${BASE}/oplines`));
}

// createOpline 新增线路 {voltage_level, line_name}。
export function createOpline(payload) {
  return request(withTeam(`${BASE}/oplines`), {
    method: 'POST',
    body: JSON.stringify(teamBody(payload)),
  });
}

// updateOpline 修改线路（voltage_level/line_name 仅出现的字段）。
export function updateOpline(id, payload) {
  return request(withTeam(`${BASE}/oplines/${id}`), {
    method: 'PUT',
    body: JSON.stringify(teamBody(payload)),
  });
}

// deleteOpline 删除线路。
export function deleteOpline(id) {
  return request(withTeam(`${BASE}/oplines/${id}`), { method: 'DELETE' });
}

// moveOpline 线路排序（direction=up|down）。
export function moveOpline(id, direction) {
  return request(withTeam(`${BASE}/oplines/${id}/move`), {
    method: 'PUT',
    body: JSON.stringify(teamBody({ direction })),
  });
}

// downloadOplineTemplate 运维线路导入模板 xlsx。
export function downloadOplineTemplate() {
  return downloadTo(`${BASE}/oplines/template`, {}, '运维线路导入模板.xlsx');
}

// importOplines 导入运维线路 xlsx（全量替换本班组运维线路）。
export function importOplines(file) {
  return uploadForm(`${BASE}/oplines/import`, file);
}

// ---- 杆塔信息（worklog_tower）----

// listTowers 本班全部杆塔行（含 id 与 sort）。
export function listTowers() {
  return request(withTeam(`${BASE}/towers`));
}

// fetchLines 本班线路聚合 [{voltage_level, line_name, tower_count}]。
export function fetchLines() {
  return request(withTeam(`${BASE}/lines`));
}

// downloadTowerTemplate 杆塔坐标导入模板 xlsx。
export function downloadTowerTemplate() {
  return downloadTo(`${BASE}/towers/template`, {}, '杆塔坐标导入模板.xlsx');
}

// importTowers 导入杆塔坐标 xlsx（全量替换本班组坐标，按导入顺序定 sort）。
export function importTowers(file) {
  return uploadForm(`${BASE}/towers/import`, file);
}
