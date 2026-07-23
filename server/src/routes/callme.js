// Call Me 应用路由：WeKnora 知识库对话代理（需登录 + call-me 应用权限）
// SSE 转发与解析细节见《WeKnora-API对接指南》第四、六节；
// 所有调用携带 X-External-User-ID（user_{本系统用户id}），会话按用户隔离
const express = require('express');
const weknora = require('../services/weknora');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const { createSseParser } = require('../utils/sse');

const router = express.Router();
router.use(auth, requireApp('call-me'));

// 统一处理 WeKnora 调用错误（避免把内部细节抛给客户端）
function weknoraError(res, err, next) {
  if (err.expose) return fail(res, 503, 50302, err.message);
  if (err.response) {
    const status = err.response.status;
    if (status === 401) return fail(res, 502, 50201, 'WeKnora 认证失败，请检查 WEKNORA_API_KEY');
    if (status === 404) return fail(res, 404, 40402, '会话不存在或已被删除');
  }
  if (err.code === 'ECONNABORTED') return fail(res, 504, 50401, 'WeKnora 响应超时，请稍后再试');
  return next(err);
}

// POST /api/v1/callme/sessions 新建会话
router.post('/sessions', async (req, res, next) => {
  try {
    const title = (req.body && req.body.title) || '新对话';
    const data = await weknora.createSession(req.user.username, title);
    return ok(res, data.data || data);
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// GET /api/v1/callme/sessions 会话列表（透传 WeKnora，按当前用户隔离）
router.get('/sessions', async (req, res, next) => {
  try {
    const data = await weknora.listSessions(req.user.username);
    return ok(res, data.data !== undefined ? data.data : data);
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// GET /api/v1/callme/sessions/:id 会话详情 + 历史消息
// 该版本 WeKnora 的 GET /sessions/{id} 只返回元数据，消息在 GET /messages/{id}/load，
// 这里合并后统一返回（前端读 data.messages，按创建时间升序）
router.get('/sessions/:id', async (req, res, next) => {
  try {
    const [detail, messagesRaw] = await Promise.all([
      weknora.getSession(req.user.username, req.params.id),
      weknora.listMessages(req.user.username, req.params.id),
    ]);
    const session = detail.data !== undefined ? detail.data : detail;
    const mdata = messagesRaw.data !== undefined ? messagesRaw.data : messagesRaw;
    const messages = (Array.isArray(mdata) ? mdata : mdata.list || mdata.messages || [])
      .slice()
      .sort((a, b) => new Date(a.created_at || 0) - new Date(b.created_at || 0));
    return ok(res, { ...session, messages });
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// PUT /api/v1/callme/sessions/:id 修改会话名称
router.put('/sessions/:id', async (req, res, next) => {
  try {
    const title = ((req.body && req.body.title) || '').trim();
    if (!title) return fail(res, 400, 40016, '会话名称不能为空');
    const data = await weknora.updateSession(req.user.username, req.params.id, title);
    return ok(res, data.data !== undefined ? data.data : data, '已保存');
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// DELETE /api/v1/callme/sessions/:id 删除会话
router.delete('/sessions/:id', async (req, res, next) => {
  try {
    await weknora.deleteSession(req.user.username, req.params.id);
    return ok(res, null, '已删除');
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// POST /api/v1/callme/chat 发送消息（SSE 流式转发）
// 入参：{ session_id, query, images? }；出参：text/event-stream
// 事件格式（已归一化）：data: {"type":"thinking|answer|done|error","content":"...","done":bool}
router.post('/chat', async (req, res) => {
  const { session_id: sessionId, query, images } = req.body || {};
  if (!sessionId || !query) {
    return fail(res, 400, 40003, '缺少 session_id 或 query');
  }

  // SSE 响应头（X-Accel-Buffering 关闭 Nginx 缓冲，见指南第七节）
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache, no-transform');
  res.setHeader('Connection', 'keep-alive');
  res.setHeader('X-Accel-Buffering', 'no');
  res.flushHeaders();

  const send = (obj) => {
    if (!res.writableEnded) res.write(`data: ${JSON.stringify(obj)}\n\n`);
  };
  // 心跳：防止代理/网关在长生成期间断连（客户端解析器会忽略非 data 行）
  const heartbeat = setInterval(() => {
    if (!res.writableEnded) res.write(': ping\n\n');
  }, 15000);
  const finish = () => {
    clearInterval(heartbeat);
    if (!res.writableEnded) res.end();
  };

  let upstream = null;
  // 客户端中途断开时销毁上游流
  req.on('close', () => {
    if (upstream && !res.writableEnded) upstream.destroy();
  });

  try {
    const wres = await weknora.agentChatStream(req.user.username, sessionId, { query, images });
    upstream = wres.data;

    const parser = createSseParser((data) => {
      // 只上送小程序端需要的事件类型，保持端侧简单
      switch (data.response_type) {
        case 'answer':
          send({ type: 'answer', content: data.content || '', done: !!data.done });
          break;
        case 'thinking':
          send({ type: 'thinking', content: data.content || '' });
          break;
        case 'error':
          send({ type: 'error', content: data.content || 'AI 服务返回错误' });
          break;
        default:
          break; // agent_query / tool_call / tool_result / references 暂不上送
      }
    });

    upstream.on('data', (chunk) => parser.push(chunk.toString('utf8')));
    upstream.on('end', () => {
      parser.end();
      send({ type: 'done' });
      finish();
    });
    upstream.on('error', () => {
      send({ type: 'error', content: 'AI 服务连接中断' });
      finish();
    });
  } catch (err) {
    if (err.expose) {
      send({ type: 'error', content: err.message });
    } else if (err.response && err.response.status === 401) {
      send({ type: 'error', content: 'WeKnora 认证失败，请检查 WEKNORA_API_KEY' });
    } else if (err.response && err.response.status === 404) {
      send({ type: 'error', content: '会话不存在或已被删除' });
    } else {
      send({ type: 'error', content: 'AI 服务暂时不可用，请稍后再试' });
    }
    finish();
  }
  return undefined;
});

module.exports = router;
