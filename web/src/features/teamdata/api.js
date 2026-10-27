// 班组数据 feature API：直连 Go 服务 /api/v1/teamdata（go/internal/apps/teamdata）。
// 响应契约 {code,message,data}；本文件统一解包，失败抛出带 code 的 Error（err.data 附业务明细）。
// 全部命名导出，便于主代理在公共 api.js 统一适配/替换；不修改公共装配文件。
// 鉴权与 web/src/api.js 同一 JWT（sessionStorage 'shade.token'）。

const BASE = '/api/v1/teamdata';

// currentToken 读取会话 JWT（与公共 api.js 同存储键）。
function currentToken() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}

// authHeaders 带 JWT 的请求头。
function authHeaders(extra = {}) {
  const token = currentToken();
  return { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...extra };
}

// buildURL 拼接路径与查询参数（空值忽略）。
function buildURL(path, params = {}) {
  const q = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  });
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

// request JSON 请求（GET/POST/PUT/DELETE 共用），返回解包后的 data。
export async function request(path, options = {}) {
  const { headers: optionHeaders, ...rest } = options;
  const res = await fetch(path, {
    ...rest,
    headers: authHeaders({ 'Content-Type': 'application/json', ...(optionHeaders || {}) }),
  });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (res.status === 401 || body.code === 40101) {
    const err = new Error(body.message || '未登录或登录已过期');
    err.code = 40101;
    throw err;
  }
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    err.data = body.data;
    throw err;
  }
  return body.data;
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
  const res = await fetch(buildURL(path, params), { headers: authHeaders() });
  const type = res.headers.get('Content-Type') || '';
  if (type.includes('application/json')) {
    const body = await res.json().catch(() => ({
      code: 'http_error',
      message: `服务返回异常（HTTP ${res.status}）`,
    }));
    if (body.code !== 0) {
      const err = new Error(body.message || '下载失败');
      err.code = body.code;
      err.data = body.data;
      throw err;
    }
    throw new Error('服务未返回文件内容');
  }
  if (!res.ok) throw new Error(`下载失败（HTTP ${res.status}）`);
  saveBlob(
    await res.blob(),
    parseDisposition(res.headers.get('Content-Disposition'), fallbackName)
  );
}

// uploadForm 上传 Excel（multipart，字段名 file）。
async function uploadForm(path, file, fields = {}) {
  const form = new FormData();
  form.append('file', file);
  Object.entries(fields).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') form.append(k, String(v));
  });
  const res = await fetch(path, { method: 'POST', headers: authHeaders(), body: form });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (body.code !== 0) {
    const err = new Error(body.message || '导入失败');
    err.code = body.code;
    err.data = body.data;
    throw err;
  }
  return body.data;
}

// ---- 班组字典（车牌 / 目的地 / 出工成员）----

// listDict 字典列表（kind=vehicles|destinations|members）。
export function listDict(kind) {
  return request(`${BASE}/${kind}`);
}

// createDict 新增字典项 {name}（sort 排尾）。
export function createDict(kind, payload) {
  return request(`${BASE}/${kind}`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateDict 修改字典项（name/sort/status；目的地另有 out_of_city/stationed 互斥标记）。
export function updateDict(kind, id, payload) {
  return request(`${BASE}/${kind}/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// deleteDict 删除字典项（有日志引用 → 40901 改为停用）。
export function deleteDict(kind, id) {
  return request(`${BASE}/${kind}/${id}`, { method: 'DELETE' });
}

// moveMember 成员排序（direction=up|down，与相邻行换位）。
export function moveMember(id, direction) {
  return request(`${BASE}/members/${id}/move`, {
    method: 'PUT',
    body: JSON.stringify({ direction }),
  });
}

// ---- 运维线路 team_line ----

// listOplines 运维线路列表（sort 序）。
export function listOplines() {
  return request(`${BASE}/oplines`);
}

// createOpline 新增线路 {voltage_level, line_name}。
export function createOpline(payload) {
  return request(`${BASE}/oplines`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateOpline 修改线路（voltage_level/line_name 仅出现的字段）。
export function updateOpline(id, payload) {
  return request(`${BASE}/oplines/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// deleteOpline 删除线路。
export function deleteOpline(id) {
  return request(`${BASE}/oplines/${id}`, { method: 'DELETE' });
}

// moveOpline 线路排序（direction=up|down）。
export function moveOpline(id, direction) {
  return request(`${BASE}/oplines/${id}/move`, {
    method: 'PUT',
    body: JSON.stringify({ direction }),
  });
}

// downloadOplineTemplate 运维线路导入模板 xlsx。
export function downloadOplineTemplate() {
  return downloadTo(`${BASE}/oplines/template`, {}, 'opline-template.xlsx');
}

// importOplines 导入运维线路 xlsx。
export function importOplines(file) {
  return uploadForm(`${BASE}/oplines/import`, file);
}

// ---- 杆塔信息（worklog_tower）----

// listTowers 本班全部杆塔行（含 id 与 sort）。
export function listTowers() {
  return request(`${BASE}/towers`);
}

// fetchLines 本班线路聚合 [{voltage_level, line_name, tower_count}]。
export function fetchLines() {
  return request(`${BASE}/lines`);
}

// downloadTowerTemplate 杆塔坐标导入模板 xlsx。
export function downloadTowerTemplate() {
  return downloadTo(`${BASE}/towers/template`, {}, 'tower-template.xlsx');
}

// importTowers 导入杆塔坐标 xlsx（全量替换本班组坐标，按导入顺序定 sort）。
export function importTowers(file) {
  return uploadForm(`${BASE}/towers/import`, file);
}
