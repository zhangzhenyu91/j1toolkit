// ONLYOFFICE Docs 在线编辑对接（社区版自托管；编辑器容器用户自装，与 Shade 同入 j1toolkit 容器网络按容器名互通）
// 链路：浏览器经 OO_PUBLIC_URL 加载编辑器 api.js 并渲染；OnlyOffice 容器经 OO_CALLBACK_BASE 向本服务
//   回源（复用网盘 /api/v1/netdisk/preview/:token 票据路由，{ndp} 载荷同口径）与保存回调（本模块 /callback/:ticket）；
//   保存产物 URL 改写为 OO_INTERNAL_URL 拉回，经 OpenList 写回原路径（写回后 size/modified 变化 →
//   下次打开 document.key 自然滚动，无缓存脏读）
// 安全口径：回源票据短时效绑定单文件；回调须同时通过 OnlyOffice JWT（OO_JWT_SECRET）与本服务回调票据双校验；
//   回调应答为 OnlyOffice 约定格式 {error:0}（不走统一信封）
const express = require('express');
const crypto = require('crypto');
const jwt = require('jsonwebtoken');
const axios = require('axios');
const config = require('../config');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const ol = require('../netdisk/openlist');
const { spaceRootOf, normRel } = require('../netdisk/space');

const router = express.Router();

// 格式白名单以 ONLYOFFICE 官方元数据仓 document-formats（onlyoffice-docs-formats.json，v10 口径，
// 2026-10-14 核）为准：可编辑 = 原生 edit + lossy-edit（ODF/txt/md/csv 等，保存回写同格式、有损）
// + MS 旧格式 auto-convert（doc/dot/xls/xlt/ppt/pot/pps：编辑会话自动转 OOXML，写回原路径后内容
// 实为 OOXML，Word/WPS 按内容嗅探可开，口径同 v1.3.0 的 doc/xls/ppt）+ pdf；
// 金山/苹果及其他 auto-convert 格式（hwp/wps/et/dps/numbers/key/pages/fod* 等）转换写回口径未实测，仅预览
const DOC_TYPES = {
  doc: 'word', docx: 'word', docm: 'word', dot: 'word', dotx: 'word', dotm: 'word',
  odt: 'word', ott: 'word', rtf: 'word', txt: 'word', md: 'word', html: 'word',
  epub: 'word', fb2: 'word',
  xls: 'cell', xlsx: 'cell', xlsm: 'cell', xlt: 'cell', xltx: 'cell', xltm: 'cell', xlsb: 'cell',
  ods: 'cell', ots: 'cell', csv: 'cell', tsv: 'cell',
  ppt: 'slide', pptx: 'slide', pptm: 'slide', pot: 'slide', potx: 'slide', potm: 'slide',
  pps: 'slide', ppsx: 'slide', ppsm: 'slide', odp: 'slide', otp: 'slide',
  pdf: 'pdf',
};
// 仅预览格式（不出编辑入口，view 模式强制只读；金山/苹果 auto-convert 与纯查看格式）
const VIEW_TYPES = {
  fodt: 'word', hml: 'word', htm: 'word', hwp: 'word', hwpx: 'word', mht: 'word', mhtml: 'word',
  pages: 'word', stw: 'word', sxw: 'word', wps: 'word', wpt: 'word', xml: 'word',
  et: 'cell', ett: 'cell', fods: 'cell', numbers: 'cell', sxc: 'cell',
  dps: 'slide', dpt: 'slide', fodp: 'slide', key: 'slide', odg: 'slide', sxi: 'slide',
  djvu: 'pdf', xps: 'pdf', oxps: 'pdf', ofd: 'pdf',
};

const baseName = (p) => p.split('/').filter(Boolean).pop() || '';
const joinPath = (root, rel) => (rel === '/' ? root : `${root}${rel}`);

const enabled = () => !!config.office.publicUrl;

// 空间根解析（与 netdisk/index.js 同口径：字符串=可用根；null=已响应错误，调用方直接 return）
async function resolveRoot(req, res, space) {
  const root = await spaceRootOf(req.user, space);
  if (root === null) {
    fail(res, 400, 40001, '参数错误：space');
    return null;
  }
  if (root === '') {
    fail(res, 400, 40030, '未分配班组，无公共区');
    return null;
  }
  return root;
}

// 上游错误归一（与网盘同口径：仅透传可读文案）
function relayFail(res, err) {
  if (err && err.expose) {
    if (/not found/i.test(err.message || '')) {
      return fail(res, 404, 40400, '文件或目录不存在');
    }
    return fail(res, err.status || 502, err.code || 50201, err.message);
  }
  console.error('[OO] 调用失败：', err && err.message);
  return fail(res, 502, 50201, '在线编辑服务异常，请稍后再试');
}

// 功能开关（网盘页据此显隐「在线编辑」入口；未配置 OO_PUBLIC_URL 时网盘维持微软查看器口径）
router.get('/status', auth, requireApp('netdisk'), (req, res) => ok(res, { enabled: enabled() }));

// 编辑器配置签发（整个 config 以 OO_JWT_SECRET 签名，OnlyOffice 校验后方可打开）
router.post('/config', auth, requireApp('netdisk'), async (req, res) => {
  try {
    if (!enabled()) return fail(res, 503, 50301, '在线编辑未配置（OO_PUBLIC_URL）');
    if (!config.office.jwtSecret) return fail(res, 503, 50301, '在线编辑未配置（OO_JWT_SECRET）');
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const name = baseName(rel);
    const ext = (name.split('.').pop() || '').toLowerCase();
    const documentType = DOC_TYPES[ext] || VIEW_TYPES[ext];
    if (!documentType) return fail(res, 400, 40030, '该格式不支持在线打开');
    // 模式：view 仅预览 / edit 协同编辑；仅预览格式（VIEW_TYPES）强制 view 防构造参数越权
    const mode = (req.body && req.body.mode) === 'view' || !DOC_TYPES[ext] ? 'view' : 'edit';
    const abs = joinPath(root, rel);
    const meta = await ol.fsGet(abs); // 存在性前置校验（目录同判非法）
    if (!meta || meta.is_dir) return fail(res, 400, 40001, '路径不合法');
    // document.key：sha1(绝对路径|size|modified)，内容变即滚动（OnlyOffice 按 key 归并会话与缓存）
    const key = crypto.createHash('sha1')
      .update(`${abs}|${meta.size || 0}|${meta.modified || ''}`)
      .digest('hex');
    const cb = config.office.callbackBase;
    // 回源票据：载荷 {ndp} 与网盘 /preview/sign 同口径，直接复用其免登录回源路由
    const fileTicket = jwt.sign({ ndp: abs }, config.jwt.secret, { expiresIn: config.office.fileTicketTtl });
    // 回调票据：绑定文件绝对路径与签发人（覆盖最长编辑会话，默认 12h）
    const cbTicket = jwt.sign({ ooc: abs, uid: req.user.id }, config.jwt.secret,
      { expiresIn: config.office.callbackTicketTtl });
    const docCfg = {
      document: {
        fileType: ext,
        key,
        title: name,
        url: `${cb}/api/v1/netdisk/preview/${fileTicket}/${encodeURIComponent(name)}`,
        permissions: mode === 'edit'
          ? { edit: true, download: true, print: true, review: true, comment: true }
          : { edit: false, download: true, print: true, review: false, comment: false },
      },
      documentType,
      editorConfig: {
        callbackUrl: `${cb}/api/v1/office/callback/${cbTicket}`,
        lang: 'zh-CN',
        region: 'zh-CN',
        mode,
        user: { id: String(req.user.id), name: req.user.nickname || req.user.username },
        customization: {
          forcesave: mode === 'edit', // 保存按钮触发 status 6 回调即时写回（仅编辑模式有意义）
          close: { visible: true, text: '关闭文件' }, // 配合页面 events.onRequestClose 返回网盘
          unit: 'cm',
        },
      },
    };
    docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
    return ok(res, { public_url: config.office.publicUrl, config: docCfg });
  } catch (err) { return relayFail(res, err); }
});

// 保存回调（免登录：OnlyOffice 容器回调。双校验——OO JWT 载体的回调体 + 本服务回调票据绑定单文件）
router.post('/callback/:ticket', async (req, res) => {
  // OnlyOffice 回调 JWT：Authorization: Bearer 头与 body.token 两个携带位都认（v10 实测两者都发）；
  // 注意头位 token 的载荷会再包一层 {payload: 回调体}（ONLYOFFICE/server fillJwtForRequest 非 inBody 口径，
  // DocsCoServer.js sendServerRequest：auth 位 false、body 位 true），验签后须解包；v1.5.0 之前未解包导致
  // payload.status 取不到恒 401（「无法保存」病根，2026-10-14 定位）
  let body = null;
  let verifyErr = null;
  const unwrap = (d) => (d && typeof d === 'object' && d.payload && typeof d.payload === 'object' ? d.payload : d);
  const h = req.headers.authorization || '';
  if (config.office.jwtSecret && h.startsWith('Bearer ')) {
    try { body = unwrap(jwt.verify(h.slice(7), config.office.jwtSecret)); } catch (err) { verifyErr = err; }
  }
  if (!body && config.office.jwtSecret && req.body && typeof req.body.token === 'string') {
    try { body = unwrap(jwt.verify(String(req.body.token), config.office.jwtSecret)); } catch (err) { verifyErr = err; }
  }
  if (!body || typeof body.status !== 'number') {
    const hasBodyToken = !!(req.body && typeof req.body.token === 'string');
    const why = !h && !hasBodyToken ? '无 Authorization 头且 body 无 token'
      : h && !h.startsWith('Bearer ') ? `头格式非 Bearer：${h.slice(0, 12)}…`
        : verifyErr ? `验签异常：${verifyErr.message}`
          : `载荷缺数值型 status（keys=${body && typeof body === 'object' ? Object.keys(body).join(',') : typeof body}）`;
    console.error(`[OO] 回调 JWT 校验失败（${why}）；查 OO 容器 JWT_ENABLED/JWT_SECRET 与 Shade 的 OO_JWT_SECRET 是否同值`);
    return res.status(401).json({ error: 1, message: '回调签名校验失败' });
  }
  let ticket = null;
  try { ticket = jwt.verify(String(req.params.ticket || ''), config.jwt.secret); } catch (err) { /* 同上 */ }
  const abs = ticket && ticket.ooc;
  if (typeof abs !== 'string' || !abs.startsWith('/')) {
    console.error('[OO] 回调票据无效或已过期（会话超 OO_CALLBACK_TICKET_TTL，或服务重启后密钥变更）');
    return res.status(401).json({ error: 1, message: '回调票据无效或已过期' });
  }
  const st = body.status;
  // 2=会话结束保存；6=强制保存（customization.forcesave）：拉回产物写回网盘原路径
  if (st === 2 || st === 6) {
    try {
      const u = new URL(String(body.url || ''));
      const r = await axios({
        method: 'get',
        url: `${config.office.internalUrl}${u.pathname}${u.search}`,
        responseType: 'arraybuffer',
        timeout: 0, // 大文档拉回+写回两跳耗时不可预估
        maxContentLength: Infinity,
      });
      const buf = Buffer.from(r.data);
      if (!buf.length) throw new Error('回调产物为空');
      await ol.fsPut(abs, buf, buf.length);
      console.log(`[OO] 已写回网盘（status ${st}）：${abs}（${buf.length} B）`);
    } catch (err) {
      console.error(`[OO] 保存写回失败（${abs}）：`, err && err.message);
      return res.json({ error: 1, message: '写回网盘失败' });
    }
  } else if (st === 3 || st === 7) {
    console.error(`[OO] 文档损坏回调（status ${st}）：${abs}`);
  }
  // 1=编辑中 / 4=关闭无改动 / 其余：仅应答确认
  return res.json({ error: 0 });
});

module.exports = router;
