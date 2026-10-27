// Call Me feature API：对接 Go 路由 /api/v1/callme/*（go/internal/apps/callme），
// 含会话 CRUD、消息成对删除、附件上传/列表、能力清单与 POST /chat SSE 流式问答。
// 响应契约 {code,message,data}（数字业务码）；只发真实请求、不内置演示数据。

const BASE = '/api/v1/callme';

// token 当前会话 JWT（api.js live 适配层写入 sessionStorage 'shade.token'）。
function token() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}

// request 统一请求：非 0 业务码抛 Error（带 err.code）。
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
    throw err;
  }
  return body.data;
}

// jsonBody JSON 请求体封装。
function jsonBody(payload) {
  return { method: 'POST', body: JSON.stringify(payload || {}) };
}

/* ================= 会话 ================= */

// createSession 新建会话（title 留空首轮后自动命名）。
export function createSession(title = '') {
  return request('/sessions', jsonBody({ title }));
}

// listSessions 会话列表（按登录账号隔离）。
export function listSessions() {
  return request('/sessions');
}

// getSession 会话详情 + 历史消息（升序）；limit 默认 50，before_time 加载更早。
export function getSession(id, { limit = 50, beforeTime = '' } = {}) {
  const qs = new URLSearchParams({ limit: String(limit) });
  if (beforeTime) qs.set('before_time', beforeTime);
  return request(`/sessions/${encodeURIComponent(id)}?${qs}`);
}

// updateSession 会话改名。
export function updateSession(id, title) {
  return request(`/sessions/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify({ title }),
  });
}

// deleteSession 删除会话。
export function deleteSession(id) {
  return request(`/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// deleteMessage 问答成对删除（同 request_id）→ {deleted}。
export function deleteMessage(sessionId, msgId) {
  return request(
    `/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(msgId)}`,
    { method: 'DELETE' }
  );
}

/* ================= 附件 ================= */

// uploadAttachment 上传会话附件（multipart 字段 file，≤50MB）→ {id,name,size,status}。
// onProgress(0-100) 由 XHR upload 事件驱动（fetch 无上传进度）。
export function uploadAttachment(sessionId, file, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    fd.append('file', file, file.name);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `${BASE}/sessions/${encodeURIComponent(sessionId)}/attachments`);
    const tk = token();
    if (tk) xhr.setRequestHeader('Authorization', `Bearer ${tk}`);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && typeof onProgress === 'function') {
        onProgress(Math.min(99, Math.round((e.loaded / e.total) * 100)));
      }
    };
    xhr.onload = () => {
      let json = null;
      try {
        json = JSON.parse(xhr.responseText);
      } catch (e) {
        // 非 JSON 响应走默认文案
      }
      if (json && json.code === 0 && json.data) {
        resolve(json.data);
      } else {
        const err = new Error((json && json.message) || `上传失败（HTTP ${xhr.status}）`);
        err.code = json ? json.code : 'http_error';
        reject(err);
      }
    };
    xhr.onerror = () => reject(new Error('网络异常，上传失败'));
    xhr.onabort = () => reject(new Error('上传已取消'));
    xhr.send(fd);
  });
}

// listAttachments 会话附件列表 [{id,name,size,status,error_message}]。
export function listAttachments(sessionId) {
  return request(`/sessions/${encodeURIComponent(sessionId)}/attachments`);
}

/* ================= 能力 ================= */

// getCapabilities 模型列表 + 联网搜索能力 {models:[{id,name,supports_vision,thinking_levels}],web_search}。
export function getCapabilities() {
  return request('/capabilities');
}

/* ================= SSE 流式问答 ================= */

// createSseParser SSE 解析器（兼容 data: 后无空格、事件以 \n\n 分隔；忽略 : ping 心跳）。
function createSseParser(onEvent) {
  let buf = '';
  function handleEventStr(eventStr) {
    eventStr.split('\n').forEach((line) => {
      if (!line.startsWith('data:')) return;
      let jsonStr = line.slice(5);
      if (jsonStr.startsWith(' ')) jsonStr = jsonStr.slice(1);
      if (!jsonStr) return;
      try {
        onEvent(JSON.parse(jsonStr));
      } catch (e) {
        // 非 JSON 行忽略
      }
    });
  }
  return {
    push(chunkStr) {
      buf += chunkStr;
      const parts = buf.split('\n\n');
      buf = parts[parts.length - 1];
      parts.slice(0, -1).forEach(handleEventStr);
    },
    end() {
      if (buf.trim() !== '') {
        const rest = buf;
        buf = '';
        handleEventStr(rest);
      }
    },
  };
}

// streamChat 发起 SSE 流式问答：
// payload = {sessionId, query, images?, attachmentIds?, webSearch?, modelId?, reasoningEffort?}
// onEvent(evt) 接收归一化事件 {type:'answer'|'thinking'|'preamble'|'tool_start'|'tool_end'|
//   'title'|'references'|'error'|'done', ...}；onClose(errorMessage?) 流收尾（errorMessage 非空表示失败）。
// 返回 {abort()} 供调用方中止；120s 无任何字节判定连接死亡（服务端 15s 心跳即活动证据）。
export function streamChat(payload, onEvent, onClose) {
  const ctrl = new AbortController();
  let timedOut = false;
  let watchdog = 0;
  let finished = false;

  function armWatchdog() {
    clearTimeout(watchdog);
    watchdog = setTimeout(() => {
      timedOut = true;
      ctrl.abort();
    }, 120000);
  }

  function finish(errorMessage) {
    if (finished) return;
    finished = true;
    clearTimeout(watchdog);
    if (typeof onClose === 'function') onClose(errorMessage || '');
  }

  const body = {
    session_id: payload.sessionId,
    query: payload.query,
  };
  if (payload.images && payload.images.length) body.images = payload.images;
  if (payload.attachmentIds && payload.attachmentIds.length) {
    body.attachment_ids = payload.attachmentIds;
  }
  if (payload.webSearch !== undefined && payload.webSearch !== null) {
    body.web_search = !!payload.webSearch;
  }
  if (payload.modelId) body.model_id = payload.modelId;
  if (payload.reasoningEffort) body.reasoning_effort = payload.reasoningEffort;

  (async () => {
    armWatchdog();
    try {
      const headers = { 'Content-Type': 'application/json' };
      const tk = token();
      if (tk) headers.Authorization = `Bearer ${tk}`;
      const res = await fetch(`${BASE}/chat`, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        signal: ctrl.signal,
      });
      if (!res.ok || !res.body) {
        let msg = `AI 服务暂时不可用（HTTP ${res.status}）`;
        try {
          const j = await res.json();
          if (j && j.message) msg = j.message;
        } catch (e) {
          // 非 JSON 响应用默认文案
        }
        throw new Error(msg);
      }
      const parser = createSseParser(onEvent);
      const reader = res.body.getReader();
      const decoder = new TextDecoder('utf-8');
      for (;;) {
        const r = await reader.read();
        if (r.done) break;
        armWatchdog();
        parser.push(decoder.decode(r.value, { stream: true }));
      }
      parser.push(decoder.decode());
      parser.end();
      finish('');
    } catch (err) {
      if (err && err.name === 'AbortError') {
        finish(timedOut ? '响应超时，请稍后重试' : '');
      } else {
        finish((err && err.message) || '网络异常，请稍后重试');
      }
    }
  })();

  return {
    abort() {
      ctrl.abort();
      finish('');
    },
  };
}
