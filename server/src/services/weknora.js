// WeKnora API 封装（对接细节见《WeKnora-API对接指南》）
// 认证方式为 X-API-Key 请求头（不是 Authorization Bearer）
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

// 创建会话，返回 WeKnora 原始响应（data.id 即 session_id）
async function createSession(title) {
  ensureConfigured();
  const res = await client.post('/sessions', { title });
  return res.data;
}

// 会话列表 / 详情 / 删除：直接透传 WeKnora 响应
async function listSessions() {
  ensureConfigured();
  const res = await client.get('/sessions');
  return res.data;
}

// 更新会话（标题）
async function updateSession(id, title) {
  ensureConfigured();
  const res = await client.put(`/sessions/${id}`, { title });
  return res.data;
}

async function getSession(id) {
  ensureConfigured();
  const res = await client.get(`/sessions/${id}`);
  return res.data;
}

async function deleteSession(id) {
  ensureConfigured();
  const res = await client.delete(`/sessions/${id}`);
  return res.data;
}

// Agent 对话（SSE 流式），返回 axios 流式响应，由调用方读取 res.data 流
async function agentChatStream(sessionId, { query, images }) {
  ensureConfigured();
  const payload = {
    query,
    agent_id: config.weknora.agentId,
    agent_enabled: true,
    web_search_enabled: false,
    enable_memory: true,
  };
  if (Array.isArray(images) && images.length) {
    payload.images = images; // 形如 [{ data: 'data:image/png;base64,...' }]
  }
  return client.post(`/agent-chat/${sessionId}`, payload, {
    responseType: 'stream',
    timeout: 120000,
  });
}

module.exports = { createSession, listSessions, getSession, updateSession, deleteSession, agentChatStream };
