// 出工日志 feature API：直连 Go 服务 /api/v1/worklog（go/internal/apps/worklog）。
// 响应契约 {code,message,data}；本文件统一解包，失败抛出带 code 的 Error（err.data 附业务明细）。
// 全部命名导出，便于主代理在公共 api.js 统一适配/替换；不修改公共装配文件。
// 鉴权与 web/src/api.js 同一 JWT（localStorage 优先、sessionStorage 回退的 'shade.token'）。

import { handleUnauthorized, readToken } from '../core/session.js';

const BASE = '/api/v1/worklog';

// teamId 超管班组切换的生效班组（0=不带 team_id，后端落默认班组）；
// 仅 /worklog/* 认该参数（query 优先于 body），sgcc 路由忽略之。
let teamId = 0;

// setTeamId 设置生效班组（超管切换器调用；非超管恒 0）。
export function setTeamId(id) {
  teamId = Number(id) || 0;
}

// getTeamId 当前生效班组 id。
export function getTeamId() {
  return teamId;
}

// teamPath 给路径追加 team_id 查询参数（空值忽略）。
function teamPath(path) {
  if (!teamId) return path;
  return path + (path.includes('?') ? '&' : '?') + 'team_id=' + teamId;
}

// currentToken 读取会话 JWT（与公共 api.js 同存储键）。
function currentToken() {
  return readToken();
}

// authHeaders 带 JWT 的请求头（下载/上传共用）。
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
  const res = await fetch(teamPath(path), {
    ...rest,
    headers: authHeaders({ 'Content-Type': 'application/json', ...(optionHeaders || {}) }),
  });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (res.status === 401 || body.code === 40101) {
    handleUnauthorized();
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

// saveBlob 触发浏览器保存（下载产物统一出口）。
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

// download 文件流下载（照片/组合 PDF/汇总表/模板共用）。
// 业务失败（含 40902 汇总前核验拦截）抛出带 code 与 err.data 的 Error。
export async function download(path, params = {}, fallbackName = 'download') {
  const res = await fetch(teamPath(buildURL(path, params)), { headers: authHeaders() });
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
  if (!res.ok) {
    throw new Error(`下载失败（HTTP ${res.status}）`);
  }
  const blob = await res.blob();
  return { blob, filename: parseDisposition(res.headers.get('Content-Disposition'), fallbackName) };
}

// downloadTo 下载并直接保存。
export async function downloadTo(path, params = {}, fallbackName = 'download') {
  const { blob, filename } = await download(path, params, fallbackName);
  saveBlob(blob, filename);
  return filename;
}

// uploadForm 上传（multipart，字段名 file）：备注附件 / 杆塔 Excel 导入共用。
async function uploadForm(path, file, fields = {}) {
  const form = new FormData();
  form.append('file', file);
  Object.entries(fields).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') form.append(k, String(v));
  });
  const res = await fetch(teamPath(path), { method: 'POST', headers: authHeaders(), body: form });
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

// ---- 卡片视图 ----

// fetchMeta 下拉/点亮数据源（车辆/目的地/成员）。
export function fetchMeta() {
  return request(`${BASE}/meta`);
}

// fetchLogs 某日卡片全量（scope=all|mine）。
export function fetchLogs({ date, scope } = {}) {
  return request(buildURL(`${BASE}/logs`, { date, scope }));
}

// fetchDayStatus 当月每日验证状态映射（passed/remark/failed）。
export function fetchDayStatus({ month, scope } = {}) {
  return request(buildURL(`${BASE}/day-status`, { month, scope }));
}

// fetchStats 当月统计：{total, passed, needPhotos}。
export function fetchStats({ month } = {}) {
  return request(buildURL(`${BASE}/stats`, { month }));
}

// fetchReport 汇总前核验：未通过记录 ∪ 有备注记录。
export function fetchReport({ from, to, scope } = {}) {
  return request(buildURL(`${BASE}/report`, { from, to, scope }));
}

// ---- 卡片 CRUD ----

// createLog 新建卡片（一卡片一派车；vehicle_id 空=未出车）。
export function createLog(payload) {
  return request(`${BASE}/logs`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateLog 修改卡片（用车人全量替换；remark/remark_files 仅显式携带才更新）。
export function updateLog(id, payload) {
  return request(`${BASE}/logs/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// deleteLog 删除卡片（跨班卡仅超管可删）。
export function deleteLog(id) {
  return request(`${BASE}/logs/${id}`, { method: 'DELETE' });
}

// uploadRemarkFile 上传单个备注附件（multipart 字段 file，≤50MB；白名单 image/video/doc）。
// 返回 {name,url,cos_key,type,size}，由前端经 updateLog 的 remark_files 挂入卡片。
export function uploadRemarkFile(entryId, file) {
  return uploadForm(`${BASE}/logs/${entryId}/remark-files`, file);
}

// toggleMemberCheck 打卡切换（返回 {checked}）。
export function toggleMemberCheck(entryId, memberRowId) {
  return request(`${BASE}/logs/${entryId}/members/${memberRowId}/check`, { method: 'PUT' });
}

// ---- 跨班日志（仅超管）----

// fetchCrossMeta 跨班表单数据源：{teams, vehicles, destinations, members}。
export function fetchCrossMeta() {
  return request(`${BASE}/logs/cross/meta`);
}

// createCrossLog 新建跨班日志（字典按名称精确命中，缺则补建）。
export function createCrossLog(payload) {
  return request(`${BASE}/logs/cross`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateCrossLog 修改跨班日志派车（不改日期）。
export function updateCrossLog(id, payload) {
  return request(`${BASE}/logs/cross/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// ---- 照片 ----

// uploadPhoto 上传照片（image 为 base64 dataURL；wm 可选服务端渲染水印；plain=true 非水印）。
export function uploadPhoto(entryId, payload) {
  return request(`${BASE}/logs/${entryId}/photos`, {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

// reverifyPhoto 重新验证（仅 failed 可重验）。
export function reverifyPhoto(photoId) {
  return request(`${BASE}/photos/${photoId}/verify`, { method: 'POST' });
}

// updatePhotoWM 修正水印识别信息（仅水印照片、非识别中可改）。
export function updatePhotoWM(photoId, payload) {
  return request(`${BASE}/photos/${photoId}/wm`, { method: 'PUT', body: JSON.stringify(payload) });
}

// updatePhotoMembers 调整照片所属人名。
export function updatePhotoMembers(photoId, members) {
  return request(`${BASE}/photos/${photoId}/members`, {
    method: 'PUT',
    body: JSON.stringify({ members }),
  });
}

// deletePhoto 删除照片。
export function deletePhoto(photoId) {
  return request(`${BASE}/photos/${photoId}`, { method: 'DELETE' });
}

// fetchPhotos 日期范围内全部照片（批量下载用）。
export function fetchPhotos({ from, to } = {}) {
  return request(buildURL(`${BASE}/photos`, { from, to }));
}

// downloadPhoto 单张照片下载（302 → fetch+blob，带鉴权）。
export function downloadPhoto(photoId, fallbackName = 'photo.jpg') {
  return downloadTo(`${BASE}/photos/${photoId}/download`, {}, fallbackName);
}

// downloadPhotosPDF 范围照片组合 PDF。
export function downloadPhotosPDF({ from, to } = {}) {
  return downloadTo(`${BASE}/photos.pdf`, { from, to }, 'photos.pdf');
}

// ---- 组合 PDF 后台任务（真实进度）----

// createPhotosPDFJob 发起组合 PDF 后台任务，返回 {job_id,total}；
// 无照片抛 40402（与同步口径一致，不建任务）。进度按实际下载/处理照片推进。
export function createPhotosPDFJob({ from, to } = {}) {
  return request(`${BASE}/photos.pdf/jobs`, {
    method: 'POST',
    body: JSON.stringify({ from, to }),
  });
}

// fetchPhotosPDFJob 查询任务进度，返回 {id,status,current,total,error}
// （status: running 进行中 / done 完成 / failed 失败，error 仅失败非空）。
export function fetchPhotosPDFJob(jobId) {
  return request(`${BASE}/photos.pdf/jobs/${jobId}`);
}

// downloadPhotosPDFJob 任务完成后取缓存 PDF 并保存（GET /photos.pdf?job_id=...）；
// 进行中抛 40909，任务失败抛其错误码（无照片 40402）。
export function downloadPhotosPDFJob(jobId, fallbackName = 'photos.pdf') {
  return downloadTo(`${BASE}/photos.pdf`, { job_id: jobId }, fallbackName);
}

// fetchGeo 逆地址 + 天气（水印预填）。
export function fetchGeo(lng, lat) {
  return request(buildURL(`${BASE}/geo`, { lng, lat }));
}

// ---- 汇总表（班管/超管）----

// downloadTaskSheet 工作任务单 docx（生成前核验；force=true 强执）。
export function downloadTaskSheet({ from, to, force } = {}) {
  return downloadTo(
    `${BASE}/task-sheet`,
    { from, to, force: force ? 1 : undefined },
    '工作任务单.docx'
  );
}

// downloadFeeSheet 费用汇总 docx（生成前核验；force=true 强执）。
export function downloadFeeSheet({ from, to, force } = {}) {
  return downloadTo(
    `${BASE}/fee-sheet`,
    { from, to, force: force ? 1 : undefined },
    '费用汇总.docx'
  );
}

// downloadDispatchSheet 派车汇总 xlsx（不核验）。
export function downloadDispatchSheet({ from, to } = {}) {
  return downloadTo(`${BASE}/dispatch-sheet`, { from, to }, '派车汇总.xlsx');
}

// ---- 杆塔 / 派车单同步 ----

// fetchTowers 当前班组杆塔坐标全量（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]）。
export function fetchTowers() {
  return request(`${BASE}/towers`);
}

// downloadTowerTemplate 杆塔坐标导入模板 xlsx。
export function downloadTowerTemplate() {
  return downloadTo(`${BASE}/towers/template`, {}, 'tower-template.xlsx');
}

// importTowers 导入杆塔坐标 xlsx（全量替换本班组坐标）。
export function importTowers(file) {
  return uploadForm(`${BASE}/towers/import`, file);
}

// fetchSyncSwitch 派车单每日同步开关。
export function fetchSyncSwitch() {
  return request(`${BASE}/dispatch/sync-switch`);
}

// updateSyncSwitch 开关每日同步。
export function updateSyncSwitch(payload) {
  return request(`${BASE}/dispatch/sync-switch`, { method: 'PUT', body: JSON.stringify(payload) });
}

// dispatchSyncNow 立即同步派车单。
export function dispatchSyncNow() {
  return request(`${BASE}/dispatch/sync-now`, { method: 'POST' });
}

// ---- 字典管理（班管/超管；与班组数据同表，管理界面在 TeamdataView）----

// listDictVehicles 车牌字典列表。
export function listDictVehicles() {
  return request(`${BASE}/admin/vehicles`);
}

// createDictVehicle 新增车牌。
export function createDictVehicle(payload) {
  return request(`${BASE}/admin/vehicles`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateDictVehicle 修改车牌。
export function updateDictVehicle(id, payload) {
  return request(`${BASE}/admin/vehicles/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// deleteDictVehicle 删除车牌。
export function deleteDictVehicle(id) {
  return request(`${BASE}/admin/vehicles/${id}`, { method: 'DELETE' });
}

// listDictDestinations 目的地字典列表。
export function listDictDestinations() {
  return request(`${BASE}/admin/destinations`);
}

// createDictDestination 新增目的地。
export function createDictDestination(payload) {
  return request(`${BASE}/admin/destinations`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateDictDestination 修改目的地。
export function updateDictDestination(id, payload) {
  return request(`${BASE}/admin/destinations/${id}`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  });
}

// deleteDictDestination 删除目的地。
export function deleteDictDestination(id) {
  return request(`${BASE}/admin/destinations/${id}`, { method: 'DELETE' });
}

// listDictMembers 出工成员字典列表。
export function listDictMembers() {
  return request(`${BASE}/admin/members`);
}

// createDictMember 新增成员。
export function createDictMember(payload) {
  return request(`${BASE}/admin/members`, { method: 'POST', body: JSON.stringify(payload) });
}

// updateDictMember 修改成员。
export function updateDictMember(id, payload) {
  return request(`${BASE}/admin/members/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// deleteDictMember 删除成员。
export function deleteDictMember(id) {
  return request(`${BASE}/admin/members/${id}`, { method: 'DELETE' });
}

// moveDictMember 成员排序（direction=up|down）。
export function moveDictMember(id, direction) {
  return request(`${BASE}/admin/members/${id}/move`, {
    method: 'PUT',
    body: JSON.stringify({ direction }),
  });
}

// ---- 批量下载打包（ZIP）----

// postZip 打包照片为 zip（POST /zip 出二进制，不走 {code,data} 信封）。
// photos: [{url, name}]，仅本仓 COS 地址可打包；部分失败清单由服务端写入 zip。
export async function postZip(photos, fallbackName = '水印照片.zip') {
  const res = await fetch(teamPath(`${BASE}/zip`), {
    method: 'POST',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ photos }),
  });
  const type = res.headers.get('Content-Type') || '';
  if (type.includes('application/json')) {
    const body = await res.json().catch(() => ({
      code: 'http_error',
      message: `服务返回异常（HTTP ${res.status}）`,
    }));
    if (res.status === 401 || body.code === 40101) handleUnauthorized();
    if (body.code !== 0) {
      const err = new Error(body.message || '打包下载失败');
      err.code = body.code;
      err.data = body.data;
      throw err;
    }
    throw new Error('服务未返回文件内容');
  }
  if (res.status === 401) handleUnauthorized();
  if (!res.ok) {
    throw new Error(`打包下载失败（HTTP ${res.status}）`);
  }
  const blob = await res.blob();
  return { blob, filename: parseDisposition(res.headers.get('Content-Disposition'), fallbackName) };
}

// postZipAndSave 打包并直接保存。
export async function postZipAndSave(photos, fallbackName = '水印照片.zip') {
  const { blob, filename } = await postZip(photos, fallbackName);
  saveBlob(blob, filename);
  return filename;
}

// ---- 存网盘 ----

// savePhotosPDFNetdisk 组合 PDF 保存到网盘（space: my 我的空间（缺省）/ public 公共区；dir 缺省 出工日志）。
export function savePhotosPDFNetdisk({ from, to, dir, space } = {}) {
  return request(`${BASE}/photos-pdf/save-netdisk`, {
    method: 'POST',
    body: JSON.stringify({ from, to, dir, space }),
  });
}

// saveSheetNetdisk 任务单/费用汇总/派车汇总生成并保存到网盘
// （sheet: task/fee/dispatch；生成前核验口径与下载一致，force=true 强执）。
export function saveSheetNetdisk({ sheet, from, to, force, dir, space } = {}) {
  return request(`${BASE}/sheet/save-netdisk`, {
    method: 'POST',
    body: JSON.stringify({ sheet, from, to, force: force ? 1 : undefined, dir, space }),
  });
}

// ---- 汇总表在线预览 ----

// fetchSheetPreview 拼在线预览地址（OO 承载页 / 微软查看器），返回 {url}。
export function fetchSheetPreview({ sheet, from, to, force } = {}) {
  return request(
    buildURL(`${BASE}/${sheet}-sheet/preview`, { from, to, force: force ? 1 : undefined })
  );
}

// ---- 备注附件预览 / 下载 ----

// fetchRemarkPreview 备注附件在线预览地址（仅 Office 文档），返回 {url}。
export function fetchRemarkPreview(entryId, key) {
  return request(buildURL(`${BASE}/logs/${entryId}/remark-preview`, { key }));
}

// downloadRemarkFile 备注附件下载代理（COS 跨域无 CORS，经本接口回源）。
export function downloadRemarkFile(entryId, key, fallbackName = 'attachment') {
  return downloadTo(buildURL(`${BASE}/logs/${entryId}/remark-download`, { key }), {}, fallbackName);
}

// ---- 派车单对齐 ----

// dispatchAlign 导入派车单 Excel（multipart 字段 files，≤20 个）按「日期＋用车人」对齐当天出车卡片。
// 返回 {summary, items}（只读对齐不写库；旧版 .xls 服务端如实报错）。
export async function dispatchAlign(files) {
  const form = new FormData();
  files.forEach((f) => form.append('files', f));
  const res = await fetch(teamPath(`${BASE}/dispatch/align`), {
    method: 'POST',
    headers: authHeaders(),
    body: form,
  });
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
