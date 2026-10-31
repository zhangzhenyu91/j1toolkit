// 题库刷题 feature API：对接 Go 路由 /api/v1/quiz/*（go/internal/apps/quiz），
// 响应契约 {code,message,data}（数字业务码），只发真实请求、不内置演示数据。
// 鉴权：Bearer JWT，取 localStorage 优先、sessionStorage 回退的 'shade.token'（与公共 api.js 同源）。

import { normalizeList } from '../core/list-response.js';
import { createClient } from '../core/request.js';

const BASE = '/api/v1/quiz';

// manageTeamId 超管班组切换的生效班组（0=不指定，由后端落默认班组）。
// 管理口径请求（管理列表/题库 CRUD/导入/解析重试/题库池/订阅/题目 CRUD）统一带 team_id；
// 个人口径（我的题库/题库主页/刷题/错题/收藏/模板下载）不带，见 withTeam。
let manageTeamId = 0;

// setManageTeam 设置管理口径生效班组（超管班组切换器调用）。
export function setManageTeam(id) {
  manageTeamId = Number(id) || 0;
}

// withTeam 管理口径路径拼接 team_id（与旧页 qz() 同口径：管理/题库池/订阅/题目 CRUD 带，
// 我的题库/题库主页/刷题/错题/收藏/模板下载为个人口径不带）。
function withTeam(path, options = {}) {
  if (!manageTeamId) return path;
  const method = (options.method || 'GET').toUpperCase();
  const personal =
    /^\/(overview|practice\/|wrongs|favorites)/.test(path) ||
    /^\/banks\/[^/]+\/home/.test(path) ||
    /^\/banks\/template/.test(path) ||
    (path === '/banks' && method === 'GET');
  if (personal) return path;
  const isManage =
    method !== 'GET' ||
    /^\/banks\/manage|^\/banks\/pool|^\/banks\/[^/]+\/(import|analyze-retry|join|questions)|^\/questions\//.test(
      path
    );
  if (!isManage) return path;
  return path + (path.indexOf('?') >= 0 ? '&' : '?') + 'team_id=' + manageTeamId;
}

// listTeams 超管班组列表（GET /api/v1/admin/teams）：data 兼容直接数组与 list/teams/items
// 包装，统一归一为数组返回（启用班组过滤由调用方处理）。
export async function listTeams() {
  const data = await platformClient.request('/api/v1/admin/teams');
  return normalizeList(data);
}

// request 共享请求层：管理口径路径注入 team_id；信封解包与 401 统一处理。
const client = createClient();
const platformClient = createClient();
function request(path, options = {}) {
  return client.request(BASE + withTeam(path, options), options);
}
const requestRaw = client.requestRaw;

// jsonBody JSON 请求体封装。
function jsonBody(payload) {
  return { method: 'POST', body: JSON.stringify(payload || {}) };
}

// ---- 总览 / 题库 ----

// getOverview 当前用户答题总览 {totalAnswered,rightRate,wrongCount}。
export function getOverview() {
  return request('/overview');
}

// listMyBanks 我的题库（订阅）{list:[...]}。
export function listMyBanks() {
  return request('/banks');
}

// listBankPool 可见题库池（含 added 标记）。
export function listBankPool() {
  return request('/banks/pool');
}

// listManageBanks 管理列表（管理角色）。
export function listManageBanks() {
  return request('/banks/manage');
}

// getBankHome 题库主页（统计 + 断点 + 错题/收藏数）。
export function getBankHome(id) {
  return request(`/banks/${id}/home`);
}

// joinBank 加入我的题库（幂等）。
export function joinBank(id) {
  return request(`/banks/${id}/join`, { method: 'POST' });
}

// leaveBank 退出我的题库。
export function leaveBank(id) {
  return request(`/banks/${id}/join`, { method: 'DELETE' });
}

// createBank 新建题库 {name,description,scope} → {id}。
export function createBank(payload) {
  return request('/banks', jsonBody(payload));
}

// updateBank 修改题库（scope 变更仅超管）。
export function updateBank(id, payload) {
  return request(`/banks/${id}`, { method: 'PUT', body: JSON.stringify(payload || {}) });
}

// deleteBank 删除题库（连带删题目/记录/错题/收藏/订阅）。
export function deleteBank(id) {
  return request(`/banks/${id}`, { method: 'DELETE' });
}

// importBank Excel 导入（multipart 字段 file，全量替换）→ {imported,queued}；
// 逐行校验失败抛 code=40031，err.data.errors=[{row,message}]。
export function importBank(id, file) {
  const fd = new FormData();
  fd.append('file', file, file.name);
  return request(`/banks/${id}/import`, { method: 'POST', body: fd });
}

// downloadTemplate 下载导入模板 xlsx（带 JWT 拉流后前端触发保存）。
export async function downloadTemplate() {
  const res = await requestRaw(`${BASE}/banks/template`);
  if (!res.ok) {
    let msg = `模板下载失败（HTTP ${res.status}）`;
    try {
      const body = await res.json();
      if (body && body.message) msg = body.message;
    } catch (e) {
      // 非 JSON 响应用默认文案
    }
    throw new Error(msg);
  }
  return res.blob();
}

// ---- 题目管理 ----

// listQuestions 题目分页 {total,list}（q 模糊匹配题干）。
export function listQuestions(bankId, { page = 1, size = 20, q = '' } = {}) {
  const qs = new URLSearchParams({ page: String(page), size: String(size) });
  if (q) qs.set('q', q);
  return request(`/banks/${bankId}/questions?${qs}`);
}

// createQuestion 新增题目 → {id}。
export function createQuestion(bankId, payload) {
  return request(`/banks/${bankId}/questions`, jsonBody(payload));
}

// updateQuestion 修改题目。
export function updateQuestion(id, payload) {
  return request(`/questions/${id}`, { method: 'PUT', body: JSON.stringify(payload || {}) });
}

// deleteQuestion 删除题目（连带删记录/错题/收藏）。
export function deleteQuestion(id) {
  return request(`/questions/${id}`, { method: 'DELETE' });
}

// analyzeRetry none/failed 解析重新入 AI 队列 → {queued}。
export function analyzeRetry(bankId) {
  return request(`/banks/${bankId}/analyze-retry`, { method: 'POST' });
}

// ---- 刷题 ----

// getPracticeQuestions 取题：mode=seq/rand/wrong/fav；lite=1 大纲；
// withAnswer=1 背题；withRecord=1 记录恢复；rand 定点取题传 ids。
export function getPracticeQuestions(params = {}) {
  const qs = new URLSearchParams();
  ['mode', 'bankId', 'limit', 'offset', 'lite', 'withAnswer', 'withRecord', 'ids'].forEach((k) => {
    const v = params[k];
    if (v !== undefined && v !== null && v !== '') qs.set(k, String(v));
  });
  return request(`/practice/questions?${qs}`);
}

// savePracticeProgress 顺序练习断点上报 {bankId,idx}。
export function savePracticeProgress(payload) {
  return request('/practice/progress', jsonBody(payload));
}

// resetPractice 清空做题记录（错题本保留）→ {cleared}。
export function resetPractice(payload) {
  return request('/practice/reset', jsonBody(payload));
}

// updateAnalysis 修改题目解析（全员可改；留空由 AI 重新生成）→ {analysis}。
export function updateAnalysis(payload) {
  return request('/practice/analysis', { method: 'PUT', body: JSON.stringify(payload || {}) });
}

// submitAnswer 提交作答判分 → {right,answer,analysis,wrong,stats}。
export function submitAnswer(payload) {
  return request('/practice/answer', jsonBody(payload));
}

// ---- 错题本 / 收藏 ----

// listWrongs 错题本（可按 bankId 过滤）{list:[...]}。
export function listWrongs(bankId) {
  const qs = bankId ? `?bankId=${encodeURIComponent(bankId)}` : '';
  return request(`/wrongs${qs}`);
}

// removeWrong 移出错题本（按人）。
export function removeWrong(questionId) {
  return request(`/wrongs/${questionId}`, { method: 'DELETE' });
}

// addFavorite 收藏（幂等）。
export function addFavorite(questionId) {
  return request(`/favorites/${questionId}`, { method: 'POST' });
}

// removeFavorite 取消收藏（按人）。
export function removeFavorite(questionId) {
  return request(`/favorites/${questionId}`, { method: 'DELETE' });
}
