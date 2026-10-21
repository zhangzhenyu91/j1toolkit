// Call Me 应用路由：WeKnora 知识库对话代理（需登录 + call-me 应用权限）
// SSE 转发与解析细节见《开发指南》第六章；
// 所有调用携带 X-External-User-ID（传登录账号 username 本身），会话按用户隔离；
// 另有 POST /ingest-chat 群内提问直推（crane-answer 用，凭 X-Ingest-Token 免登录，挂在 auth 门控之前）
const express = require('express');
const multer = require('multer');
const config = require('../config');
const { pool } = require('../db');
const weknora = require('../services/weknora');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const { createSseParser } = require('../utils/sse');
const { getFileExt, fixLatin1Name } = require('../utils/file');
const agentSteps = require('../utils/agentsteps');

const router = express.Router();

// ===== 群内提问内部对接（crane-answer 直推，见《开发指南》第六章）=====
// 群成员 @ 监听账号提问未命中 @ 指令时，crane-answer 以该成员 wxid 对应的壹匣账号身份调 Call Me 提问，
// 回答后经 Dify 工作流命名写成 md 文件发回群。契约（与 crane-answer 侧约定）：
// body { wxid, query, session_title, web_search?, image?, file? }；
// 成功 { ok:true, data:{ session_id, session_title, answer, model_used:{id,name}|null } }；
// 失败 { ok:false, code, message, models? }，code：user_not_found（wxid 未绑定/多绑）/ no_permission /
// model_not_found（附 models:[{id,name}]）/ unsupported_file / file_too_large / upstream_error
// （业务失败统一 HTTP 200 + ok:false；对接密钥未配置 503、密钥不等 401）

// 图片校验：jpeg/png/gif/webp base64 dataURL（与 /chat images 同口径）
const INGEST_IMAGE_RE = /^data:image\/(jpeg|png|gif|webp);base64,([A-Za-z0-9+/=\s]*)$/;

// 群内提问直推响应（契约形状，与统一响应结构 {code,message,data} 不同）
function ingestOk(res, data) {
  return res.json({ ok: true, data });
}

function ingestFail(res, code, message, extra) {
  return res.json({ ok: false, code, message, ...(extra || {}) });
}

// 模型识别：query 中「（请）使用/调用/换成/切换到/切换成 + 模型名」候选词（规则见《开发指南》第六章）
const MODEL_HINT_RE = /(?:请)?(?:使用|调用|换成|切换到|切换成)\s*([A-Za-z0-9一-龥][A-Za-z0-9.\-_ 一-龥]{0,29})/u;

// 模型名规范化：小写并去掉空格/点/横线/下划线
function normModelKey(s) {
  return String(s || '').toLowerCase().replace(/[.\-_\s]+/g, '');
}

// 候选词与模型表匹配：按空格切 1–3 词前缀段、先长后短逐段与模型 id/name 规范化双向包含匹配；
// 一段多命中取规范化名最长者；全段无命中返回 null（调用方回 model_not_found）
function matchModelByHint(hint, models) {
  const words = String(hint || '').trim().split(/\s+/).filter(Boolean);
  for (let n = Math.min(words.length, 3); n >= 1; n -= 1) {
    const seg = normModelKey(words.slice(0, n).join(' '));
    if (!seg) continue;
    const hits = models.filter((m) => {
      const keys = [normModelKey(m.id), normModelKey(m.display_name || m.name)];
      return keys.some((k) => k && (k.includes(seg) || seg.includes(k)));
    });
    if (!hits.length) continue;
    // 多命中取规范化名最长者（显示名缺失时退化用 id）
    const best = hits
      .slice()
      .sort((a, b) => normModelKey(b.display_name || b.name || b.id).length
        - normModelKey(a.display_name || a.name || a.id).length)[0];
    // 思考强度取该模型 thinking_levels 最后一档（最高档）；空数组不传
    const levels = Array.isArray(best.capabilities && best.capabilities.thinking_levels)
      ? best.capabilities.thinking_levels
      : (Array.isArray(best.parameters && best.parameters.thinking_levels) ? best.parameters.thinking_levels : []);
    return {
      id: best.id,
      name: best.display_name || best.name,
      reasoningEffort: levels.length ? levels[levels.length - 1] : undefined,
    };
  }
  return null;
}

// POST /api/v1/callme/ingest-chat 群内提问（免登录，凭 X-Ingest-Token；必须挂在 auth 门控之前，同 treehazard /ingest 模式）
router.post('/ingest-chat', async (req, res) => {
  // 对接密钥：未配置 503、不等 401
  const token = config.callme.ingestToken;
  if (!token) return res.status(503).json({ ok: false, code: 'ingest_token_missing', message: '未配置对接密钥' });
  if (String(req.get('X-Ingest-Token') || '') !== token) {
    return res.status(401).json({ ok: false, code: 'ingest_token_invalid', message: '对接密钥错误' });
  }

  const body = req.body || {};
  const wxid = typeof body.wxid === 'string' ? body.wxid.trim() : '';
  const query = typeof body.query === 'string' ? body.query.trim() : '';
  const sessionTitle = typeof body.session_title === 'string' ? body.session_title.trim() : '';
  if (!wxid || !query || !sessionTitle) {
    return ingestFail(res, 'param_invalid', 'wxid / query / session_title 均为必填');
  }

  // 图片：≤1 张（单字段天然 ≤1）、jpeg/png/gif/webp dataURL、解码后 ≤8MB
  let images;
  if (body.image) {
    const match = typeof body.image === 'string' && body.image.match(INGEST_IMAGE_RE);
    if (!match) return ingestFail(res, 'unsupported_file', '图片格式应为 jpeg/png/gif/webp（base64 dataURL）');
    if (Buffer.from(match[2], 'base64').length > 8 * 1024 * 1024) {
      return ingestFail(res, 'file_too_large', '图片大小应在 8MB 以内');
    }
    images = [{ data: body.image }];
  }

  // 文件：{name, data_base64} 单个 ≤50MB，扩展名白名单（含 mp4）；语音由 crane-answer 以 voice.wav 上送走 ASR
  let attachment;
  if (body.file) {
    const f = body.file;
    const name = f && typeof f.name === 'string' ? f.name : '';
    const b64 = f && typeof f.data_base64 === 'string' ? f.data_base64 : '';
    // 文件名须是合法 UTF-8（往返转换一致，拦截孤立代理字符）且不含控制字符（上游 ValidateInput 会拒非法字符）
    const nameOk = name
      && Buffer.from(name, 'utf8').toString('utf8') === name
      && !/[\u0000-\u001f]/.test(name);
    if (!nameOk || !b64 || !/^[A-Za-z0-9+/=\s]*$/.test(b64)) {
      return ingestFail(res, 'unsupported_file', '附件名称或内容无效');
    }
    const buffer = Buffer.from(b64, 'base64');
    if (!buffer.length) return ingestFail(res, 'unsupported_file', '附件内容为空');
    if (buffer.length > 50 * 1024 * 1024) return ingestFail(res, 'file_too_large', '附件大小应在 50MB 以内');
    const ext = getFileExt(name);
    if (!ATTACHMENT_EXTS.has(ext)) {
      return ingestFail(res, 'unsupported_file', `不支持的附件格式：${ext || '无扩展名'}`);
    }
    attachment = { buffer, name };
  }

  try {
    // wxid → 壹匣账号（0 命中或多绑 → user_not_found，多命中记日志便于排查）
    const [users] = await pool.query(
      'SELECT id, username FROM sys_user WHERE wxid = ? AND status = 1',
      [wxid]
    );
    if (!users.length) return ingestFail(res, 'user_not_found', '该 wxid 未绑定壹匣账号');
    if (users.length > 1) {
      console.error(`[Call Me 群内提问] wxid ${wxid} 绑定了多个启用账号：${users.map((u) => u.username).join('、')}`);
      return ingestFail(res, 'user_not_found', '该 wxid 绑定了多个壹匣账号');
    }
    const user = users[0];

    // Call Me 应用权限
    const [apps] = await pool.query(
      `SELECT a.id FROM sys_user_app ua
       JOIN sys_app a ON a.id = ua.app_id
       WHERE ua.user_id = ? AND a.app_key = 'call-me' AND a.status = 1`,
      [user.id]
    );
    if (!apps.length) return ingestFail(res, 'no_permission', '该用户未开通 Call Me 权限');

    // 会话按名找或建（同名多个取 updated_at 最新；创建显式传 title 防 WeKnora 自动改名）
    const sres = await weknora.listSessions(user.username);
    const sdata = sres.data !== undefined ? sres.data : sres;
    const slist = Array.isArray(sdata) ? sdata : sdata.list || sdata.sessions || [];
    const same = slist
      .filter((s) => s.title === sessionTitle)
      .sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0));
    let sessionId = same[0] && same[0].id;
    if (!sessionId) {
      const created = await weknora.createSession(user.username, sessionTitle);
      const cdata = (created && created.data) || created || {};
      sessionId = cdata.id;
    }
    if (!sessionId) return ingestFail(res, 'upstream_error', '会话创建失败');

    // 模型识别：抽到候选才查模型表；未抽到候选默认不传 model_id/reasoning_effort（走 agent 默认）
    let modelId;
    let reasoningEffort;
    let modelUsed = null;
    const hint = MODEL_HINT_RE.exec(query);
    if (hint) {
      const modelsRaw = await weknora.listModels(user.username);
      const modelsAll = Array.isArray(modelsRaw && modelsRaw.data) ? modelsRaw.data : [];
      // 仅 KnowledgeQA 模型可被指定/列出（与 capabilities 口径一致，避免列出不可调用模型）
      const models = modelsAll.filter((m) => m && m.type === 'KnowledgeQA');
      const hit = matchModelByHint(hint[1], models);
      if (!hit) {
        return ingestFail(res, 'model_not_found', '未识别到可用模型', {
          models: models.map((mm) => ({ id: mm.id, name: mm.display_name || mm.name })),
        });
      }
      modelId = hit.id;
      reasoningEffort = hit.reasoningEffort;
      modelUsed = { id: hit.id, name: hit.name };
    }

    // 有文件先预上传取附件 id（失败 → upstream_error），随对话发送
    let attachmentIds;
    if (attachment) {
      const up = await weknora.uploadAttachment(user.username, sessionId, {
        buffer: attachment.buffer,
        filename: attachment.name,
        mimetype: 'application/octet-stream',
      });
      const doc = (up && up.data) || {};
      if (!doc.id) return ingestFail(res, 'upstream_error', '附件上传失败');
      attachmentIds = [doc.id];
    }

    // 收流拿完整回答（与 /chat 共用 collectAnswer 同一套 SSE 归一化解析）
    const { answer } = await collectAnswer(user.username, sessionId, {
      query,
      images,
      attachmentIds,
      webSearch: typeof body.web_search === 'boolean' ? body.web_search : true,
      modelId,
      reasoningEffort,
    });

    return ingestOk(res, {
      session_id: sessionId,
      session_title: sessionTitle,
      answer,
      model_used: modelUsed,
    });
  } catch (err) {
    console.error('[Call Me 群内提问] 处理失败：', err.message);
    return ingestFail(res, 'upstream_error', err.expose ? err.message : 'AI 服务暂时不可用，请稍后再试');
  }
});

router.use(auth, requireApp('call-me'));

// 会话附件上传：单文件 ≤50MB，内存暂存后转发 WeKnora（同 safeday 口径）
const attachmentUpload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 50 * 1024 * 1024, files: 1 },
});

// 附件扩展名白名单（不含点、小写比较；与 WeKnora 临时文档支持的格式对齐；
// mp4：WeKnora 侧视频解析能力待验证，随白名单放开）
const ATTACHMENT_EXTS = new Set([
  'pdf', 'docx', 'doc', 'pptx', 'ppt', 'xlsx', 'xls', 'epub', 'mhtml',
  'csv', 'md', 'markdown', 'txt', 'json', 'html',
  'mp3', 'wav', 'm4a', 'flac', 'ogg', 'aac',
  'bmp', 'tiff', 'tif',
  'mp4',
]);

// 思考强度固定档位（仅当 /chat 显式提供 reasoning_effort 时校验，缺省由模型/会话默认决定）
const REASONING_EFFORTS = new Set(['off', 'auto', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']);

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
    // 标题留空（默认）时 WeKnora 会在首轮对话后自动生成会话标题（session_title 事件，官方口径）；
    // 用户显式命名过的标题则不再被自动覆盖
    const title = (req.body && req.body.title) || '';
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
        // supports_vision / thinking_levels 字段位置随 WeKnora 版本在 capabilities / parameters 之间变动，两处兼容
        supports_vision: (m.capabilities && m.capabilities.supports_vision === true)
          || (m.parameters && m.parameters.supports_vision === true),
        thinking_levels: Array.isArray(m.capabilities && m.capabilities.thinking_levels)
          ? m.capabilities.thinking_levels
          : (Array.isArray(m.parameters && m.parameters.thinking_levels) ? m.parameters.thinking_levels : []),
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

// 收流拿完整回答（POST /chat 与 POST /ingest-chat 共用同一 SSE 归一化解析，见《开发指南》第六章）：
// 发起 agentChatStream 后把 WeKnora 事件归一化经 onEvent 实时转发、拼接最终回答（工具调用前
// 乐观流出的过渡语 preamble 不计入最终回答），300s 无字节静默看门狗判上游卡死。
// 返回 { answer, requestId }；失败抛错——err.emitted === true 表示 error 事件已由 onEvent 下发，
// 调用方只收尾不要重复下发；signal（AbortSignal）中止时销毁上游并抛出已静默错误（客户端断连场景）。
async function collectAnswer(username, sessionId, {
  query, images, attachmentIds, webSearch, modelId, reasoningEffort,
  onEvent = () => {}, signal,
}) {
  const wres = await weknora.agentChatStream(username, sessionId, {
    query,
    images,
    attachmentIds,
    webSearch,
    modelId,
    reasoningEffort,
  });
  const upstream = wres.data;
  return new Promise((resolve, reject) => {
    let settled = false;
    let silenceTimer = null;
    // 最终回答拼接（增量 answer 帧；preamble 段重置不计）与本轮 request_id（帧顶层或 data 内携带）
    let answer = '';
    let requestId = '';
    // 是否已向端侧流出过回答内容（用于判定 tool_call 前的 answer 是过渡语）
    let answerForwarded = false;
    // 回答级 error（无 tool_call_id）标记：流末尾照常发 done 保持端侧行为，随后按失败抛错
    let answerFailed = false;
    // tool_call_id → { name, args }：tool_result 到达时生成完成态标题/摘要需要入参
    const toolCalls = new Map();

    const settle = (fn, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(silenceTimer);
      if (signal) signal.removeEventListener('abort', onAbort);
      fn(value);
    };
    // 已下发 error 事件的失败（调用方只收尾，不重复下发）
    const failEmitted = (content) => {
      const err = new Error(content);
      err.emitted = true;
      settle(reject, err);
    };
    const onAbort = () => {
      upstream.destroy();
      const err = new Error('客户端已断开');
      err.emitted = true;
      settle(reject, err);
    };
    if (signal) {
      if (signal.aborted) return onAbort();
      signal.addEventListener('abort', onAbort);
    }

    // 上游静默看门狗：正常生成 token 持续流出，300s 无任何字节判定 WeKnora 卡死；
    // 不设总时长上限，长生成不被掐断
    const armSilence = () => {
      clearTimeout(silenceTimer);
      silenceTimer = setTimeout(() => {
        onEvent({ type: 'error', content: 'AI 服务响应超时' });
        upstream.destroy();
        failEmitted('AI 服务响应超时');
      }, 300000);
    };

    // 工具结束（tool_result 或带 tool_call_id 的 error 帧）归一化为 tool_end
    const emitToolEnd = (frame) => {
      const meta = frame.data || {};
      const id = meta.tool_call_id || '';
      const name = meta.tool_name || '';
      const startInfo = toolCalls.get(id) || {};
      toolCalls.delete(id);
      const success = frame.response_type !== 'error' && meta.success !== false;
      onEvent({
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
      // 只上送端侧需要的事件类型，保持端侧简单
      switch (data.response_type) {
        case 'answer':
          if (data.content) answerForwarded = true;
          answer += data.content || '';
          onEvent({ type: 'answer', content: data.content || '', done: !!data.done });
          break;
        case 'thinking':
          onEvent({ type: 'thinking', content: data.content || '' });
          break;
        case 'tool_call': {
          // WeKnora 会把工具调用前的过渡语以 answer 事件乐观流出，且不发出显式撤回信号
          // （官方约定：tool_call 事件本身即"之前不是最终回答"的标记，见 engine.go）。
          // 通知端侧把已流出的回答并入思考区，再开出工具步骤节点；过渡语不计入最终回答。
          if (answerForwarded) {
            onEvent({ type: 'preamble' });
            answerForwarded = false;
            answer = '';
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
          onEvent({ type: 'tool_start', id, name, title: agentSteps.pendingTitle(name), update: !!prev });
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
          if (list.length) onEvent({ type: 'references', list });
          break;
        }
        case 'session_title':
          // WeKnora 自动生成的会话标题，端侧据此实时改名
          if (data.content) onEvent({ type: 'title', content: data.content });
          break;
        case 'error':
          // 工具执行失败也以 error 帧下发（带 tool_call_id），并非整流终态：
          // 归一化为失败的 tool_end 节点；只有无 tool_call_id 的 error 才是回答级错误
          if (data.data && data.data.tool_call_id) {
            emitToolEnd(data);
          } else {
            answerFailed = true;
            onEvent({ type: 'error', content: data.content || 'AI 服务返回错误' });
          }
          break;
        default:
          break; // agent_query / complete 等暂不上送
      }
      const rid = data.request_id || (data.data && data.data.request_id);
      if (rid) requestId = String(rid);
    });

    upstream.on('data', (chunk) => {
      armSilence();
      parser.push(chunk.toString('utf8'));
    });
    upstream.on('end', () => {
      parser.end();
      onEvent({ type: 'done' });
      if (answerFailed) {
        const err = new Error('AI 服务返回错误');
        err.emitted = true;
        settle(reject, err);
      } else {
        settle(resolve, { answer, requestId });
      }
    });
    upstream.on('error', () => {
      onEvent({ type: 'error', content: 'AI 服务连接中断' });
      failEmitted('AI 服务连接中断');
    });
    armSilence();
    return undefined;
  });
}

// POST /api/v1/callme/chat 发送消息（SSE 流式转发）
// 入参：{ session_id, query, images?, attachment_ids?, web_search?, model_id?, reasoning_effort? }；出参：text/event-stream
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
    reasoning_effort: reasoningEffort,
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
  // 思考强度：显式提供时须为固定档位之一，缺省由模型/会话默认决定
  if (reasoningEffort !== undefined && !REASONING_EFFORTS.has(reasoningEffort)) {
    return fail(res, 400, 40049, 'reasoning_effort 参数无效');
  }
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
  const finish = () => {
    clearInterval(heartbeat);
    if (!res.writableEnded) res.end();
  };

  // 客户端断连检测必须挂在 res 上：req 的 close 在请求体收完时即触发（那时 upstream 尚未
  // 建立，判断恒为空操作），只有 res 的 close 才是真正的断连信号。断连后经 AbortSignal 通知
  // collectAnswer 销毁上游流，避免 WeKnora 继续空转生成。
  const ac = new AbortController();
  res.on('close', () => ac.abort());
  // 断连后的迟滞写入会触发 EPIPE/ERR_STREAM_DESTROYED，吞掉防止未处理异常
  res.on('error', () => {});

  try {
    // 收流与事件归一化在 collectAnswer（与 /ingest-chat 共用同一解析器），本路由只负责 SSE 输出
    await collectAnswer(req.user.username, sessionId, {
      query,
      images,
      attachmentIds,
      webSearch,
      modelId: summaryModelId,
      reasoningEffort,
      onEvent: send,
      signal: ac.signal,
    });
  } catch (err) {
    // err.emitted：error 事件已随流下发（静默超时/连接中断/回答级 error），只收尾不重复下发
    if (!err.emitted) {
      if (err.expose) {
        send({ type: 'error', content: err.message });
      } else if (err.response && err.response.status === 401) {
        send({ type: 'error', content: 'WeKnora 认证失败，请检查 WEKNORA_API_KEY' });
      } else if (err.response && err.response.status === 404) {
        send({ type: 'error', content: '会话不存在或已被删除' });
      } else {
        send({ type: 'error', content: 'AI 服务暂时不可用，请稍后再试' });
      }
    }
  }
  return finish();
});

module.exports = router;
