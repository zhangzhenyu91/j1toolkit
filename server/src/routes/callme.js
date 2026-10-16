// Call Me 应用路由：WeKnora 知识库对话代理（需登录 + call-me 应用权限）
// SSE 转发与解析细节见《开发指南》第六章；
// 所有调用携带 X-External-User-ID（传登录账号 username 本身），会话按用户隔离
const express = require('express');
const multer = require('multer');
const weknora = require('../services/weknora');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const { createSseParser } = require('../utils/sse');
const { getFileExt, fixLatin1Name } = require('../utils/file');
const agentSteps = require('../utils/agentsteps');

const router = express.Router();
router.use(auth, requireApp('call-me'));

// 会话附件上传：单文件 ≤50MB，内存暂存后转发 WeKnora（同 safeday 口径）
const attachmentUpload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 50 * 1024 * 1024, files: 1 },
});

// 附件扩展名白名单（不含点、小写比较；与 WeKnora 临时文档支持的格式对齐）
const ATTACHMENT_EXTS = new Set([
  'pdf', 'docx', 'doc', 'pptx', 'ppt', 'xlsx', 'xls', 'epub', 'mhtml',
  'csv', 'md', 'markdown', 'txt', 'json', 'html',
  'mp3', 'wav', 'm4a', 'flac', 'ogg', 'aac',
  'bmp', 'tiff', 'tif',
]);

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
// 这里合并后统一返回（前端读 data.messages，按创建时间升序）；
// 支持 limit / before_time 分页参数，has_more 提示是否还有更早消息
router.get('/sessions/:id', async (req, res, next) => {
  try {
    const limit = Math.min(Math.max(parseInt(req.query.limit, 10) || 50, 1), 200);
    const beforeTime = req.query.before_time || undefined;
    const [detail, messagesRaw] = await Promise.all([
      weknora.getSession(req.user.username, req.params.id),
      weknora.listMessages(req.user.username, req.params.id, { limit, beforeTime }),
    ]);
    const session = detail.data !== undefined ? detail.data : detail;
    const mdata = messagesRaw.data !== undefined ? messagesRaw.data : messagesRaw;
    const list = Array.isArray(mdata) ? mdata : mdata.list || mdata.messages || [];
    const messages = list
      .slice()
      .sort((a, b) => new Date(a.created_at || 0) - new Date(b.created_at || 0))
      .map((m) => {
        // assistant 消息附带展示就绪的步骤树（思考/工具调用）与统计，两端直接渲染
        if (
          (m.role === 'assistant' || m.role === 'ai') &&
          Array.isArray(m.agent_steps) &&
          m.agent_steps.length
        ) {
          const { steps, meta } = agentSteps.buildSteps(m.agent_steps);
          return {
            ...m,
            steps,
            steps_meta: { ...meta, durationMs: Number(m.agent_duration_ms) || 0 },
          };
        }
        return m;
      });
    return ok(res, { ...session, messages, has_more: list.length >= limit });
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

// DELETE /api/v1/callme/sessions/:id/messages/:msgId 删除消息（问答成对删除：
// 一问一答在 WeKnora 共享 request_id，连同配对消息一起删除）
router.delete('/sessions/:id/messages/:msgId', async (req, res, next) => {
  try {
    const sessionId = req.params.id;
    const msgId = req.params.msgId;

    // 找出该消息所在问答对的全部消息 id
    let ids = [msgId];
    try {
      const data = await weknora.listMessages(req.user.username, sessionId);
      const mdata = data.data !== undefined ? data.data : data;
      const list = Array.isArray(mdata) ? mdata : mdata.list || mdata.messages || [];
      const target = list.find((m) => m.id === msgId);
      if (target && target.request_id) {
        const pairIds = list.filter((m) => m.request_id === target.request_id).map((m) => m.id);
        ids = [...new Set([...pairIds, msgId])];
      }
    } catch (e) {
      // 列表获取失败时退化为仅删除本条
    }

    for (const id of ids) {
      await weknora.deleteMessage(req.user.username, sessionId, id);
    }
    return ok(res, { deleted: ids.length }, '已删除');
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// POST /api/v1/callme/sessions/:id/attachments 上传会话附件（multipart 字段 file，单文件 ≤50MB）
// 转发 WeKnora 临时文档接口（异步解析，返回 status 供端侧轮询）
router.post(
  '/sessions/:id/attachments',
  // multer 错误（超限等）转成业务响应，避免落入全局 500（与 worklog 同口径）
  (req, res, next) => {
    attachmentUpload.single('file')(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40027, '附件大小应在 50MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      const file = req.file;
      if (!file) return fail(res, 400, 40029, '请选择要上传的附件');
      // multer 默认按 latin1 解析文件名（1.x/2.x 同），中文名需转回 UTF-8
      const filename = fixLatin1Name(file.originalname);
      const ext = getFileExt(filename);
      if (!ATTACHMENT_EXTS.has(ext)) {
        return fail(res, 400, 40028, `不支持的附件格式：${ext || '无扩展名'}`);
      }
      const data = await weknora.uploadAttachment(req.user.username, req.params.id, {
        buffer: file.buffer,
        filename,
        mimetype: file.mimetype,
      });
      const doc = (data && data.data) || {};
      return ok(res, { id: doc.id, name: doc.file_name, size: doc.file_size, status: doc.status });
    } catch (err) {
      return weknoraError(res, err, next);
    }
  }
);

// GET /api/v1/callme/sessions/:id/attachments 会话附件列表
// （字段子集映射 file_name/file_size → name/size；WeKnora 响应包裹 data）
router.get('/sessions/:id/attachments', async (req, res, next) => {
  try {
    const data = await weknora.listAttachments(req.user.username, req.params.id);
    const list = Array.isArray(data && data.data) ? data.data : [];
    return ok(res, list.map((d) => ({
      id: d.id,
      name: d.file_name,
      size: d.file_size,
      status: d.status,
      error_message: d.error_message,
    })));
  } catch (err) {
    return weknoraError(res, err, next);
  }
});

// GET /api/v1/callme/capabilities 端侧能力探测：可选模型列表 + 联网搜索是否可用
// WeKnora 调用失败时降级为空能力（不报错），端侧据此隐藏对应按钮
router.get('/capabilities', async (req, res) => {
  const capabilities = { models: [], web_search: false };
  try {
    const [modelsRaw, agentRaw, providersRaw] = await Promise.all([
      weknora.listModels(req.user.username),
      weknora.getAgent(req.user.username),
      weknora.listWebSearchProviders(req.user.username),
    ]);
    const models = Array.isArray(modelsRaw && modelsRaw.data) ? modelsRaw.data : [];
    capabilities.models = models
      .filter((m) => m.type === 'KnowledgeQA')
      .map((m) => ({
        id: m.id,
        name: m.display_name || m.name,
        // supports_vision 字段位置随 WeKnora 版本在 capabilities / parameters 之间变动，两处兼容
        supports_vision: (m.capabilities && m.capabilities.supports_vision === true)
          || (m.parameters && m.parameters.supports_vision === true),
      }));
    const agent = (agentRaw && agentRaw.data) || {};
    const agentCfg = agent.config || {};
    const agentWebSearch = agent.web_search_enabled === true || agentCfg.web_search_enabled === true;
    const providerId = agent.web_search_provider_id || agentCfg.web_search_provider_id || '';
    const providers = Array.isArray(providersRaw && providersRaw.data) ? providersRaw.data : [];
    capabilities.web_search = agentWebSearch
      && (!!providerId || providers.some((p) => p.is_default === true));
  } catch (err) {
    // 探测失败降级为空能力，不影响对话主流程
  }
  return ok(res, capabilities);
});

// POST /api/v1/callme/chat 发送消息（SSE 流式转发）
// 入参：{ session_id, query, images?, attachment_ids?, web_search?, model_id? }；出参：text/event-stream
// 事件格式（已归一化）：data: {"type":"thinking|answer|preamble|tool_start|tool_end|title|references|done|error", ...}
// thinking：思考增量；preamble：工具调用前的过渡语需从回答区挪入思考区（对齐官方 UI 语义）；
// tool_start/tool_end：步骤树工具节点（展示就绪：title/summary 由 utils/agentsteps 生成）；
// title：自动生成的会话标题；references：引用来源列表 [{id, title}]
router.post('/chat', async (req, res) => {
  const {
    session_id: sessionId,
    query,
    images,
    attachment_ids: attachmentIds,
    web_search: webSearch,
    model_id: modelId,
  } = req.body || {};
  if (!sessionId || !query) {
    return fail(res, 400, 40003, '缺少 session_id 或 query');
  }
  // 附件引用：数组 ≤5、元素非空字符串（须先经 /sessions/:id/attachments 上传）
  if (attachmentIds !== undefined
    && (!Array.isArray(attachmentIds) || attachmentIds.length > 5
      || attachmentIds.some((id) => typeof id !== 'string' || !id.trim()))) {
    return fail(res, 400, 40043, 'attachment_ids 参数无效（最多 5 个附件）');
  }
  if (webSearch !== undefined && typeof webSearch !== 'boolean') {
    return fail(res, 400, 40047, 'web_search 参数无效');
  }
  // 模型覆盖：非空字符串才生效，空串/缺省走会话默认模型
  if (modelId !== undefined && modelId !== null && typeof modelId !== 'string') {
    return fail(res, 400, 40048, 'model_id 参数无效');
  }
  const summaryModelId = typeof modelId === 'string' ? modelId.trim() : '';
  // 图片：≤1 张、须 data:image/(jpeg|png|gif|webp);base64, 头、解码后 ≤8MB
  if (images !== undefined) {
    if (!Array.isArray(images) || images.length > 1) {
      return fail(res, 400, 40044, '图片最多 1 张');
    }
    if (images.length) {
      const item = images[0];
      const dataUrl = (item && typeof item === 'object') ? item.data : item;
      const match = typeof dataUrl === 'string'
        && dataUrl.match(/^data:image\/(jpeg|png|gif|webp);base64,([A-Za-z0-9+/=\s]*)$/);
      if (!match) {
        return fail(res, 400, 40045, '图片格式应为 jpeg/png/gif/webp（base64 dataURL）');
      }
      if (Buffer.from(match[2], 'base64').length > 8 * 1024 * 1024) {
        return fail(res, 400, 40046, '图片大小应在 8MB 以内');
      }
    }
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
  let silenceTimer = null;
  const finish = () => {
    clearInterval(heartbeat);
    clearTimeout(silenceTimer);
    if (!res.writableEnded) res.end();
  };

  let upstream = null;
  // 上游静默看门狗：正常生成 token 持续流出，300s 无任何字节判定 WeKnora 卡死；
  // 不设总时长上限，长生成不被掐断
  const armSilence = () => {
    clearTimeout(silenceTimer);
    silenceTimer = setTimeout(() => {
      send({ type: 'error', content: 'AI 服务响应超时' });
      finish();
      if (upstream) upstream.destroy();
    }, 300000);
  };
  // 客户端断连检测必须挂在 res 上：req 的 close 在请求体收完时即触发（那时 upstream 尚未
  // 建立，判断恒为空操作），只有 res 的 close 才是真正的断连信号。断连后清理心跳并销毁
  // 上游流，避免 WeKnora 继续空转生成。
  res.on('close', () => {
    clearInterval(heartbeat);
    clearTimeout(silenceTimer);
    if (upstream) upstream.destroy();
  });
  // 断连后的迟滞写入会触发 EPIPE/ERR_STREAM_DESTROYED，吞掉防止未处理异常
  res.on('error', () => {});

  try {
    const wres = await weknora.agentChatStream(req.user.username, sessionId, {
      query,
      images,
      attachmentIds,
      webSearch,
      modelId: summaryModelId,
    });
    upstream = wres.data;
    armSilence();

    // 是否已向端侧流出过回答内容（用于判定 tool_call 前的 answer 是过渡语）
    let answerForwarded = false;
    // tool_call_id → { name, args }：tool_result 到达时生成完成态标题/摘要需要入参
    const toolCalls = new Map();

    // 工具结束（tool_result 或带 tool_call_id 的 error 帧）归一化为 tool_end
    const emitToolEnd = (frame) => {
      const meta = frame.data || {};
      const id = meta.tool_call_id || '';
      const name = meta.tool_name || '';
      const startInfo = toolCalls.get(id) || {};
      toolCalls.delete(id);
      const success = frame.response_type !== 'error' && meta.success !== false;
      send({
        type: 'tool_end',
        id,
        name,
        success,
        title: agentSteps.doneTitle(name, startInfo.args, meta, success),
        summary: success
          ? agentSteps.toolSummary(name, meta)
          : String(meta.error || frame.content || '').slice(0, 120),
        durationMs: Number(meta.duration_ms) || 0,
      });
    };

    const parser = createSseParser((data) => {
      // 只上送小程序端需要的事件类型，保持端侧简单
      switch (data.response_type) {
        case 'answer':
          if (data.content) answerForwarded = true;
          send({ type: 'answer', content: data.content || '', done: !!data.done });
          break;
        case 'thinking':
          send({ type: 'thinking', content: data.content || '' });
          break;
        case 'tool_call': {
          // WeKnora 会把工具调用前的过渡语以 answer 事件乐观流出，且不发出显式撤回信号
          // （官方约定：tool_call 事件本身即"之前不是最终回答"的标记，见 engine.go）。
          // 通知端侧把已流出的回答并入思考区，再开出工具步骤节点。
          if (answerForwarded) {
            send({ type: 'preamble' });
            answerForwarded = false;
          }
          // 同一次调用会发两帧 tool_call：LLM 决策时的 pending 帧（无 arguments）+ 执行前的
          // hint 帧（带 arguments，见 think.go/act.go），tool_call_id 相同。已注册过则合并
          // 信息并标记 update，端侧更新既有节点而不是重复开节点（否则残留"正在调用"）。
          const meta = data.data || {};
          const id = meta.tool_call_id || '';
          const prev = id ? toolCalls.get(id) : null;
          const hasArgs = meta.arguments && Object.keys(meta.arguments).length;
          const args = hasArgs ? meta.arguments : (prev && prev.args) || {};
          const name = meta.tool_name || (prev && prev.name) || '';
          if (id) toolCalls.set(id, { name, args });
          send({ type: 'tool_start', id, name, title: agentSteps.pendingTitle(name), update: !!prev });
          break;
        }
        case 'tool_result':
          emitToolEnd(data);
          break;
        case 'references': {
          // 引用来源：按知识条目去重后只上送展示所需字段
          const refs = Array.isArray(data.knowledge_references) ? data.knowledge_references : [];
          const seen = new Set();
          const list = [];
          for (const r of refs) {
            const id = r.knowledge_id || r.id;
            if (!id || seen.has(id)) continue;
            seen.add(id);
            list.push({ id, title: r.knowledge_title || r.knowledge_filename || '未命名资料' });
          }
          if (list.length) send({ type: 'references', list });
          break;
        }
        case 'session_title':
          // WeKnora 自动生成的会话标题，端侧据此实时改名
          if (data.content) send({ type: 'title', content: data.content });
          break;
        case 'error':
          // 工具执行失败也以 error 帧下发（带 tool_call_id），并非整流终态：
          // 归一化为失败的 tool_end 节点；只有无 tool_call_id 的 error 才是回答级错误
          if (data.data && data.data.tool_call_id) {
            emitToolEnd(data);
          } else {
            send({ type: 'error', content: data.content || 'AI 服务返回错误' });
          }
          break;
        default:
          break; // agent_query / complete 等暂不上送
      }
    });

    upstream.on('data', (chunk) => {
      armSilence();
      parser.push(chunk.toString('utf8'));
    });
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
