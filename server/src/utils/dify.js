// Dify 公共逻辑：工作流地址拼接与配置缺失校验（各应用 dify 模块共用；传输层各自实现，不在此抽取）
// apiUrl（DIFY_API_URL）只填域名即可：/v1 由代码拼接，配置已带 /v1 也不会重复

// API 基地址：去尾斜杠（空值保持空串）
function apiBaseUrl(apiUrl) {
  return String(apiUrl || '').replace(/\/+$/, '');
}

// 工作流运行地址：{apiUrl}/v1/workflows/run
function workflowRunUrl(apiUrl) {
  const base = apiBaseUrl(apiUrl);
  return `${base}${base.endsWith('/v1') ? '' : '/v1'}/workflows/run`;
}

// 配置缺失校验工厂：keyEnvName 为各应用独立的 Dify key 环境变量名（如 DIFY_QUIZ_API_KEY）
function createEnsureConfigured({ apiUrl, apiKey, keyEnvName }) {
  return function ensureConfigured() {
    if (!apiUrl || !apiKey) {
      const err = new Error(`Dify 未配置（DIFY_API_URL / ${keyEnvName}）`);
      err.expose = true;
      throw err;
    }
  };
}

module.exports = { apiBaseUrl, workflowRunUrl, createEnsureConfigured };
