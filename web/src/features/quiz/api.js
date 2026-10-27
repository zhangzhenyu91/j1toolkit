// 题库刷题 feature API：对接 Go 路由 /api/v1/quiz/*（go/internal/apps/quiz），
// 响应契约 {code,message,data}（数字业务码），只发真实请求、不内置演示数据。
// 鉴权：Bearer JWT，取 api.js live 适配层写入的 sessionStorage 'shade.token'。

const BASE = '/api/v1/quiz';

// token 当前会话 JWT（未登录时留空，由服务端 401 拦截）。
function token() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}

// request 统一请求：非 0 业务码抛 Error（带 err.code / err.data）。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  const tk = token();
  if (tk) headers.Authorization = `Bearer ${tk}`;
  if (options.body && !(options.body instanceof FormData)) {
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
    err.data = body.data;
    throw err;
  }
  return body.data;
}

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
  const headers = {};
  const tk = token();
  if (tk) headers.Authorization = `Bearer ${tk}`;
  const res = await fetch(`${BASE}/banks/template`, { headers });
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
