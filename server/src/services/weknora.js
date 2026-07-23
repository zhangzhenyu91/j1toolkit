// WeKnora API 封装（对接细节见《WeKnora-API对接指南》）
// 认证方式：X-API-Key 请求头（不是 Authorization Bearer）；
// 另需 X-External-User-ID 标识终端用户（直接用本系统登录账号），会话按用户隔离
const axios = require('axios');
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
async function listMessages(username, id, limit = 50) {
  ensureConfigured();
  const res = await client.get(`/messages/${id}/load`, {
    headers: extHeaders(username),
    params: { limit },
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

// Agent 对话（SSE 流式），返回 axios 流式响应，由调用方读取 res.data 流
async function agentChatStream(username, sessionId, { query, images }) {
  ensureConfigured();
  const payload = {
    query,
    agent_id: config.weknora.agentId,
    agent_enabled: true,
    web_search_enabled: false,
    enable_memory: true,
    channel: 'api',
  };
  if (Array.isArray(images) && images.length) {
    payload.images = images; // 形如 [{ data: 'data:image/png;base64,...' }]
  }
  return client.post(`/agent-chat/${sessionId}`, payload, {
    headers: extHeaders(username),
    responseType: 'stream',
    timeout: 120000,
  });
}

module.exports = {
  createSession,
  listSessions,
  getSession,
  listMessages,
  updateSession,
  deleteSession,
  agentChatStream,
};
