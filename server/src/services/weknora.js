// WeKnora API 封装（对接细节见《开发指南》第六章）
// 认证方式：X-API-Key 请求头（不是 Authorization Bearer）；
// 另需 X-External-User-ID 标识终端用户（直接用本系统登录账号），会话按用户隔离
const axios = require('axios');
const FormData = require('form-data');
const config = require('../config');

const client = axios.create({
  baseURL: config.weknora.apiUrl,
  headers: { 'X-API-Key': config.weknora.apiKey, 'Content-Type': 'application/json' },
  timeout: 30000,
});

function ensureConfigured() {
  if (!config.weknora.apiKey || !config.weknora.agentId) {
    const err = new Error('WeKnora 未配置（WEKNORA_API_KEY / WEKNORA_AGENT_ID）');
    err.expose = true;
    throw err;
  }
}

// 外部用户 ID 头：WeKnora 侧终端用户标识，取本系统登录账号（如 zhangzhenyu91）
function extHeaders(username) {
  return { 'X-External-User-ID': username };
}

// 创建会话，返回 WeKnora 原始响应（data.id 即 session_id）
async function createSession(username, title) {
  ensureConfigured();
  const res = await client.post('/sessions', { title }, { headers: extHeaders(username) });
  return res.data;
}

// 会话列表 / 详情 / 改名 / 删除：透传 WeKnora 响应
async function listSessions(username) {
  ensureConfigured();
  const res = await client.get('/sessions', { headers: extHeaders(username) });
  return res.data;
}

async function getSession(username, id) {
  ensureConfigured();
  const res = await client.get(`/sessions/${id}`, { headers: extHeaders(username) });
  return res.data;
}

// 会话消息列表（该版本 WeKnora 的 GET /sessions/{id} 只返回元数据，
// 消息在独立接口 GET /messages/{session_id}/load，支持 before_time/limit 分页）
async function listMessages(username, id, { limit = 50, beforeTime } = {}) {
  ensureConfigured();
  const params = { limit };
  if (beforeTime) params.before_time = beforeTime;
  const res = await client.get(`/messages/${id}/load`, {
    headers: extHeaders(username),
    params,
  });
  return res.data;
}

async function updateSession(username, id, title) {
  ensureConfigured();
  const res = await client.put(`/sessions/${id}`, { title }, { headers: extHeaders(username) });
  return res.data;
}

async function deleteSession(username, id) {
  ensureConfigured();
  const res = await client.delete(`/sessions/${id}`, { headers: extHeaders(username) });
  return res.data;
}

// 删除单条消息（DELETE /messages/{session_id}/{id}）
async function deleteMessage(username, sessionId, messageId) {
  ensureConfigured();
  const res = await client.delete(`/messages/${sessionId}/${messageId}`, {
    headers: extHeaders(username),
  });
  return res.data;
}

// 上传会话附件（POST /sessions/{id}/attachments，multipart 字段 file + 表单字段 agent_id；
// WeKnora 异步解析，响应 {success, data}，data 为 {id, file_name, file_size, status, error_message}）
async function uploadAttachment(username, sessionId, { buffer, filename, mimetype }) {
  ensureConfigured();
  const fd = new FormData();
  fd.append('file', buffer, { filename, contentType: mimetype });
  fd.append('agent_id', config.weknora.agentId);
  const res = await client.post(`/sessions/${sessionId}/attachments`, fd, {
    headers: { ...fd.getHeaders(), ...extHeaders(username) },
    maxBodyLength: Infinity,
    maxContentLength: Infinity,
  });
  return res.data;
}

// 会话附件列表（GET /sessions/{id}/attachments，响应包裹 data）
async function listAttachments(username, sessionId) {
  ensureConfigured();
  const res = await client.get(`/sessions/${sessionId}/attachments`, {
    headers: extHeaders(username),
  });
  return res.data;
}

// 模型列表（GET /models；本实例这些租户级接口同样强制 X-External-User-ID，缺了会 401）
async function listModels(username) {
  ensureConfigured();
  const res = await client.get('/models', { headers: extHeaders(username) });
  return res.data;
}

// Agent 配置（GET /agents/{id}；web_search_enabled / web_search_provider_id 在 data.config 内）
async function getAgent(username) {
  ensureConfigured();
  const res = await client.get(`/agents/${config.weknora.agentId}`, {
    headers: extHeaders(username),
  });
  return res.data;
}

// 联网搜索提供商列表（GET /web-search-providers；is_default 标记默认提供商）
async function listWebSearchProviders(username) {
  ensureConfigured();
  const res = await client.get('/web-search-providers', { headers: extHeaders(username) });
  return res.data;
}

// Agent 对话（SSE 流式），返回 axios 流式响应，由调用方读取 res.data 流
// webSearch 开启本轮联网搜索；modelId 非空时覆盖本轮总结模型（summary_model_id）；
// attachmentIds 引用本会话已上传的附件（须先经 POST /sessions/{id}/attachments 上传）；
// reasoningEffort 为非空字符串时透传本轮思考强度（reasoning_effort）
async function agentChatStream(
  username,
  sessionId,
  { query, images, attachmentIds, webSearch, modelId, reasoningEffort }
) {
  ensureConfigured();
  const payload = {
    query,
    agent_id: config.weknora.agentId,
    agent_enabled: true,
    web_search_enabled: !!webSearch,
    channel: 'api',
  };
  if (Array.isArray(images) && images.length) {
    payload.images = images; // 形如 [{ data: 'data:image/png;base64,...' }]
  }
  if (Array.isArray(attachmentIds) && attachmentIds.length) {
    payload.attachment_ids = attachmentIds;
  }
  if (modelId) {
    payload.summary_model_id = modelId;
  }
  if (typeof reasoningEffort === 'string' && reasoningEffort.trim()) {
    payload.reasoning_effort = reasoningEffort;
  }
  return client.post(`/agent-chat/${sessionId}`, payload, {
    headers: extHeaders(username),
    responseType: 'stream',
    timeout: 0, // 流式不限总时长（axios 0 = 不超时）；卡死兜底在路由层上游静默看门狗（见指南第六章）
  });
}

// ===== 知识库内容管理（树障台账 xlsx 快照同步用，编排见 treehazard/weknorasync.js）=====
// 这组接口只需 API Key（不依赖 agentId）；本实例老接口强制 X-External-User-ID，统一取配置的 syncUser

function ensureKbConfigured() {
  if (!config.weknora.apiKey) {
    const err = new Error('WeKnora 未配置（WEKNORA_API_KEY）');
    err.expose = true;
    throw err;
  }
}

function kbHeaders() {
  return extHeaders(config.weknora.syncUser || 'admin');
}

// 列知识条目（GET /knowledge-bases/{kbId}/knowledge；params 支持 folder_path / folder_recursive /
// source / keyword / file_type / page / page_size 等过滤分页），透传 WeKnora 响应
async function listKnowledge(kbId, params = {}) {
  ensureKbConfigured();
  const res = await client.get(`/knowledge-bases/${kbId}/knowledge`, {
    headers: kbHeaders(),
    params,
  });
  return res.data;
}

// 上传知识库文件（POST /knowledge-bases/{kbId}/knowledge/file，multipart：file=文件 buffer、
// fileName=带目录路径的文件名（路径即文件夹，如「树木台账/检修一班树木统计表.xlsx」，无需再调 folder 归类）、
// title=展示标题、channel=摄取渠道标记；同名文件上传 409 无原地更新，由调用方按删旧传新处理）
async function uploadKnowledgeFile(kbId, { buffer, fileName, title, channel }) {
  ensureKbConfigured();
  const baseName = String(fileName).split('/').pop();
  const fd = new FormData();
  fd.append('file', buffer, {
    filename: title || baseName,
    contentType: /\.xlsx$/i.test(baseName)
      ? 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
      : 'application/octet-stream',
  });
  fd.append('fileName', fileName);
  if (title) fd.append('title', title);
  if (channel) fd.append('channel', channel);
  const res = await client.post(`/knowledge-bases/${kbId}/knowledge/file`, fd, {
    headers: { ...fd.getHeaders(), ...kbHeaders() },
    maxBodyLength: Infinity,
    maxContentLength: Infinity,
  });
  return res.data;
}

// 删单条知识（DELETE /knowledge/{knowledgeId}；删除异步——「正在删除的同名文件不计为重复」，
// 删后立即重传同名文件不会 409）
async function deleteKnowledge(knowledgeId) {
  ensureKbConfigured();
  const res = await client.delete(`/knowledge/${knowledgeId}`, { headers: kbHeaders() });
  return res.data;
}

// 批量删知识（POST /knowledge/batch-delete；knowledge_ids 单次 ≤200 条）
async function batchDeleteKnowledge(kbId, knowledgeIds) {
  ensureKbConfigured();
  const res = await client.post(
    '/knowledge/batch-delete',
    { kb_id: kbId, knowledge_ids: knowledgeIds },
    { headers: kbHeaders() }
  );
  return res.data;
}

module.exports = {
  createSession,
  listSessions,
  getSession,
  listMessages,
  updateSession,
  deleteSession,
  deleteMessage,
  uploadAttachment,
  listAttachments,
  listModels,
  getAgent,
  listWebSearchProviders,
  agentChatStream,
  listKnowledge,
  uploadKnowledgeFile,
  deleteKnowledge,
  batchDeleteKnowledge,
};
