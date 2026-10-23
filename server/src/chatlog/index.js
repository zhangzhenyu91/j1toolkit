// 聊天日志归档：crane-answer treeWatch 兜底子能力每日把群兜底消息纪要 md 与当天文档推送到本端点，
// 写入 WeKnora 知识库「检修一班历史聊天记录」供 Call Me 问答引用（见 开发指南.md 二十一节）。
// 纯对接端点：无 sys_app 登记、无前端页面、无权限应用。
// 对接契约（与 crane-answer 侧约定）：POST /api/v1/chatlog/ingest，免登录凭请求头 X-Ingest-Token
// （未配置 → 503；不等 → 401；先比长度再恒定时间比较，防时序侧信道，同 treehazard /ingest 口径）。
// 一次调用只带一件：
//   kind=digest：{ date:'yyyy-MM-dd', group_name, kind, digest_md:'<markdown 正文>' }——存为「{群名}_{date}.md」
//   kind=file：{ date, group_name, kind, file:{ name, data_base64 } }——用原名，base64 纯内容无前缀，≤30MB，
//     扩展名须在文档类白名单内（pdf/doc/docx/ppt/pptx/xlsx/xls/epub/mhtml/csv/md/markdown/txt/json/html，
//     白名单外与无扩展名拒收 unsupported_file）
// 落库：知识库 ID 取 config.weknora.chatlogKbId；文件夹 /{yyyy}/{M.d}/ 两级（date=2026-10-23 → 年文件夹「2026」
// 下「10.23」，月.日单层不带前导零）；fileName 路径前缀即文件夹、按路径归一，无需预创建、并发重跑不产生
// 重复文件夹（同 weknorasync 口径）。幂等：同文件夹同名文档先删后传（重跑/补跑覆盖当天内容）；
// 仍 409 取响应已有 id 删除后重试一次，再冲突追加「-1」后缀（扩展名前）改名再传。
// 摄取渠道标记 channel='chatlog'，与树障/交跨既有渠道清理逻辑互不影响。
// WeKnora 未配置（WEKNORA_API_KEY 缺失）或知识库 ID 为空 → 503。
const express = require('express');
const crypto = require('crypto');
const config = require('../config');
const weknora = require('../services/weknora');
const { getFileExt } = require('../utils/file'); // 扩展名提取（不含点小写，同 callme 口径）

const router = express.Router();

const CHANNEL = 'chatlog'; // 摄取渠道标记（列表过滤 source=，树障/交跨渠道清理只认各自渠道，互不干扰）
const FILE_MAX_BYTES = 30 * 1024 * 1024; // 单文件上限（base64 纯内容解码后）
const PAGE_SIZE = 100; // 列知识分页大小
const LIST_MAX_PAGES = 10; // 列知识翻页兜底上限（防接口分页异常死循环）
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;
const B64_RE = /^[A-Za-z0-9+/]*={0,2}$/;
// 文档扩展名白名单（不含点小写比较）：WeKnora 知识库仅支持文档类摄取——
// 与 callme ATTACHMENT_EXTS 的文档类子集对齐（不含 mp3/wav/mp4 等音视频与 bmp/tiff 图片）
const FILE_EXTS = new Set([
  'pdf',
  'doc',
  'docx',
  'ppt',
  'pptx',
  'xlsx',
  'xls',
  'epub',
  'mhtml',
  'csv',
  'md',
  'markdown',
  'txt',
  'json',
  'html',
]);

// 直推响应（契约形状 {ok, code?, message?, data?}，与 callme /ingest-chat 同口径，不走统一 {code,message,data} 信封）
function ingestOk(res, data) {
  return res.json({ ok: true, data });
}

function ingestFail(res, httpStatus, code, message) {
  return res.status(httpStatus).json({ ok: false, code, message });
}

// WeKnora 响应信封解包（{success, data, ...}）；success 显式为 false 时抛错（同 weknorasync 口径）
function unwrap(body, label) {
  if (!body || body.success === false) {
    throw new Error(
      `${label}失败：${(body && (body.message || body.error)) || 'WeKnora 响应异常'}`
    );
  }
  return body.data;
}

// 群名/文档名净化非法字符（同 treehazard export.safeFileName 口径；含 / 防路径逃逸出当天文件夹）
function safeFileName(name) {
  const s = String(name ?? '')
    .replace(/[\\/:*?"<>|]/g, '_')
    .trim();
  return s.replace(/[\s\S]/g, (ch) => (ch.charCodeAt(0) < 32 ? '_' : ch)) || '未命名';
}

// date（yyyy-MM-dd）→ 文件夹两级名 ['yyyy', 'M.d']（月.日单层不带前导零，如 2026-10-23 → ['2026','10.23']）
function folderSegments(date) {
  const [y, m, d] = date.split('-').map(Number);
  return [String(y), `${m}.${d}`];
}

// 列表条目文件名（file_name/fileName/title 字段兼容取值；可能带 folder_path 前缀，取末段基名，同 weknorasync 口径）
function knowledgeBaseName(k) {
  const raw = String((k && (k.file_name || k.fileName || k.title)) ?? '').trim();
  return raw.slice(raw.lastIndexOf('/') + 1);
}

// 上传 409 响应里已有条目 id（data 形态未实测：单对象 / 数组 / {items|list} 列表均兼容，同 weknorasync 口径）
function conflictIds(data) {
  if (!data) return [];
  const arr = Array.isArray(data)
    ? data
    : Array.isArray(data.items)
      ? data.items
      : Array.isArray(data.list)
        ? data.list
        : [data];
  return arr
    .map((k) => (k && typeof k === 'object' ? (k.id ?? k.knowledge_id) : k))
    .filter((id) => id !== undefined && id !== null);
}

// 拉文件夹内全部条目（分页兜底；按 folder_path 精确匹配，子文件夹不递归）
async function listFolderKnowledge(kbId, folderPath) {
  const out = [];
  for (let page = 1; page <= LIST_MAX_PAGES; page += 1) {
    const body = await weknora.listKnowledge(kbId, {
      folder_path: folderPath,
      folder_recursive: 'false',
      page,
      page_size: PAGE_SIZE,
    });
    const data = unwrap(body, '列知识');
    const items = Array.isArray(data) ? data : (data && (data.items || data.list)) || [];
    out.push(...items);
    if (items.length < PAGE_SIZE) break;
  }
  return out;
}

// 同名追加序号后缀（插在扩展名前；无扩展名直接尾部追加）
function renameWithSuffix(name, n) {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? `${name.slice(0, dot)}-${n}${name.slice(dot)}` : `${name}-${n}`;
}

// 同文件夹同名文档先删后传（幂等重跑/补跑覆盖当天内容）；上传仍 409 → 取响应已有 id 删除后重试一次；
// 再冲突（并发极端场景）追加「-1」后缀改名再传。返回 { fileName, knowledgeId, replaced }
async function upsertFile(kbId, folderPath, fileName, buffer) {
  // 先在文件夹内查重（删除异步，「正在删除的同名文件不计为重复」，删后即传不会 409）
  const listed = await listFolderKnowledge(kbId, folderPath);
  const dupIds = [
    ...new Set(
      listed
        .filter((k) => knowledgeBaseName(k) === fileName)
        .map((k) => k && k.id)
        .filter((id) => id !== undefined && id !== null)
    ),
  ];
  for (const id of dupIds) unwrap(await weknora.deleteKnowledge(id), '删同名旧知识');

  const upload = async (name) => {
    const data = unwrap(
      await weknora.uploadKnowledgeFile(kbId, {
        buffer,
        fileName: `${folderPath}/${name}`,
        title: name,
        channel: CHANNEL,
      }),
      '上传知识文件'
    );
    return (data && (data.id ?? data.knowledge_id)) || null;
  };

  try {
    return { fileName, knowledgeId: await upload(fileName), replaced: dupIds.length };
  } catch (err) {
    const body = err && err.response && err.response.data;
    if (!body || err.response.status !== 409) throw err;
    const ids = conflictIds(body.data);
    if (!ids.length) throw err; // 409 但响应里拿不到已有 id，无法自愈
    for (const id of ids) unwrap(await weknora.deleteKnowledge(id), '删同名旧知识');
    try {
      return {
        fileName,
        knowledgeId: await upload(fileName),
        replaced: dupIds.length + ids.length,
      };
    } catch (err2) {
      const body2 = err2 && err2.response && err2.response.data;
      if (!body2 || err2.response.status !== 409) throw err2;
      const renamed = renameWithSuffix(fileName, 1);
      return {
        fileName: renamed,
        knowledgeId: await upload(renamed),
        replaced: dupIds.length + ids.length,
      };
    }
  }
}

// 入参校验：合法返回 { date, groupName, kind, fileName, buffer }；非法返回 { error:{ code, message, httpStatus } }
function parseBody(body) {
  const b = body || {};
  const bad = (message, code = 'param_invalid', httpStatus = 400) => ({
    error: { code, message, httpStatus },
  });

  const date = String(b.date ?? '').trim();
  if (!DATE_RE.test(date)) return bad('date 应为 yyyy-MM-dd');
  const [y, m, d] = date.split('-').map(Number);
  const dt = new Date(Date.UTC(y, m - 1, d));
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== m - 1 || dt.getUTCDate() !== d)
    return bad('date 不是有效日期');
  const groupName = String(b.group_name ?? '').trim();
  if (!groupName) return bad('group_name 不能为空');

  const kind = String(b.kind ?? '').trim();
  if (kind === 'digest') {
    const digestMd = typeof b.digest_md === 'string' ? b.digest_md : '';
    if (!digestMd.trim()) return bad('digest_md 不能为空');
    // 纪要：{群名}_{date}.md（群名净化非法字符）
    return {
      date,
      groupName,
      kind,
      fileName: `${safeFileName(groupName)}_${date}.md`,
      buffer: Buffer.from(digestMd, 'utf8'),
    };
  }
  if (kind === 'file') {
    const f = b.file;
    const name = f && typeof f.name === 'string' ? f.name.trim() : '';
    if (!name) return bad('file.name 不能为空');
    // 扩展名白名单（不含点小写；无扩展名同样拒收）；业务拒绝口径同 callme /ingest-chat：HTTP 200 + ok:false
    if (!FILE_EXTS.has(getFileExt(name))) {
      return bad('该文件类型暂不支持入知识库', 'unsupported_file', 200);
    }
    const b64 = f && typeof f.data_base64 === 'string' ? f.data_base64 : '';
    if (!b64.trim()) return bad('file.data_base64 不能为空');
    const compact = b64.replace(/\s+/g, '');
    if (!B64_RE.test(compact) || compact.length % 4 !== 0)
      return bad('file.data_base64 应为纯内容 base64（无前缀、去空白后长度须为 4 的倍数）');
    const buffer = Buffer.from(compact, 'base64');
    if (!buffer.length) return bad('file.data_base64 解码为空');
    if (buffer.length > FILE_MAX_BYTES)
      return bad(`单文件大小应在 30MB 以内（解码后 ${buffer.length} 字节）`, 'file_too_large');
    return { date, groupName, kind, fileName: safeFileName(name), buffer }; // 文档用原名（仅净化非法字符）
  }
  return bad('kind 应为 digest 或 file', 'unsupported_kind');
}

// POST /ingest（鉴权在路由处凭 X-Ingest-Token 完成，挂在 auth 门控之前）
router.post('/ingest', async (req, res) => {
  // 对接密钥：未配置 503、不等 401（先比长度再恒定时间比较，防时序侧信道，同 treehazard /ingest 口径）
  const token = config.chatlog.ingestToken;
  if (!token) return ingestFail(res, 503, 'ingest_token_missing', '未配置对接密钥');
  const provided = Buffer.from(String(req.get('X-Ingest-Token') || ''), 'utf8');
  const expected = Buffer.from(token, 'utf8');
  if (provided.length !== expected.length || !crypto.timingSafeEqual(provided, expected)) {
    return ingestFail(res, 401, 'ingest_token_invalid', '对接密钥错误');
  }
  // WeKnora 未配置（apiKey 缺失）或知识库 ID 为空 → 503
  const kbId = config.weknora.chatlogKbId;
  if (!config.weknora.apiKey || !kbId) {
    return ingestFail(
      res,
      503,
      'weknora_not_configured',
      '未配置 WeKnora（WEKNORA_API_KEY / WEKNORA_CHATLOG_KB_ID）'
    );
  }

  const parsed = parseBody(req.body);
  if (parsed.error) {
    return ingestFail(res, parsed.error.httpStatus, parsed.error.code, parsed.error.message);
  }

  const folderPath = folderSegments(parsed.date).join('/');
  try {
    const r = await upsertFile(kbId, folderPath, parsed.fileName, parsed.buffer);
    return ingestOk(res, {
      date: parsed.date,
      group_name: parsed.groupName,
      kind: parsed.kind,
      folder: folderPath,
      file_name: r.fileName,
      knowledge_id: r.knowledgeId,
      replaced: r.replaced,
    });
  } catch (err) {
    console.error('[聊天日志归档] 写入知识库失败：', err.message);
    return ingestFail(res, 502, 'upstream_error', `写入知识库失败：${err.message}`);
  }
});

module.exports = router;
