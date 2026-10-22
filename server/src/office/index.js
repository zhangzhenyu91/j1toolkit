// ONLYOFFICE Docs 在线编辑对接（社区版自托管；编辑器容器用户自装，与 Shade 同入 j1toolkit 容器网络按容器名互通）
// 链路：浏览器经 OO_PUBLIC_URL 加载编辑器 api.js 并渲染；OnlyOffice 容器经 OO_CALLBACK_BASE 向本服务
//   回源与保存回调；网盘来源的保存产物 URL 改写为 OO_INTERNAL_URL 拉回，经 OpenList 写回原路径（写回后
//   size/modified 变化 → 下次打开 document.key 自然滚动，无缓存脏读）
// 签发多源（/config body.src 判别，缺省 = 网盘文件）：
//   网盘（可编辑协同）：回源复用网盘 /api/v1/netdisk/preview/:token 票据路由（{ndp} 载荷同口径），保存回调走本模块 /callback/:ticket；
//   worklog-sheet：出工日志 工作任务单 / 费用汇总 / 派车汇总（按日期范围即时生成 docx/xlsx）；
//   safeday：安全日记录产物（本地落盘 docx）；worklog-remark：出工日志备注附件（COS 公网地址转发）；
//   treehazard：树障隐患统计表（按班组即时生成 xlsx）；
//   后四个 view-only 来源统一强制 mode='view'、editorConfig 不配 callbackUrl / customization 不配 forcesave（只读不回写），
//   回源走本模块 /raw/:ticket/:name?（免登录，票据即鉴权，载荷 {src:wls|sdr|wlr|thd} 分派）
// 安全口径：回源票据短时效绑定单来源单文件；回调须同时通过 OnlyOffice JWT（OO_JWT_SECRET）与本服务回调票据双校验；
//   回调应答为 OnlyOffice 约定格式 {error:0}（不走统一信封）
const express = require('express');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const jwt = require('jsonwebtoken');
const axios = require('axios');
const { Readable } = require('stream');
const config = require('../config');
const auth = require('../middleware/auth');
const { hasAppPermission } = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const ol = require('../netdisk/openlist');
const { spaceRootOf, normRel } = require('../netdisk/space');
const worklog = require('../worklog');
const tasksheet = require('../worklog/tasksheet');
const feesheet = require('../worklog/feesheet');
const dispatchsheet = require('../worklog/dispatchsheet');
const worklogCos = require('../worklog/cos');
const safeday = require('../safeday');
const safedayStore = require('../safeday/store');
const treehazard = require('../treehazard');
const thdExport = require('../treehazard/export');

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

// ===== 多源签发与回源共用（/config 三个 view-only 来源 + /raw） =====

// 日期口径照抄 worklog/index.js sheetRange（YYYY-MM-DD、from≤to、≤31 天）
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

// 回源 Content-Type 按扩展名给准（上游可能只回 octet-stream，OO 依赖它判格式）：网盘 PREVIEW_MIME 口径扩展 pdf
//（sheet 产物仅 docx/xlsx，安全日产物 docx，备注附件含 doc/docx/xls/xlsx/ppt/pptx/pdf 各族）
const RAW_MIME = {
  doc: 'application/msword',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xls: 'application/vnd.ms-excel',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  ppt: 'application/vnd.ms-powerpoint',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  pdf: 'application/pdf',
};

// 安全日记录产物目录（口径同 safeday/index.js：{SAFEDAY_DATA_DIR}/docs/）
const SAFEDAY_DOCS_DIR = path.join(config.safeday.dataDir, 'docs');

// 应用权限判定（口径同 middleware/requireApp.js 的 hasAppPermission）：/config 按 body.src 分派来源、
// 各来源门禁应用各异（netdisk / work-log / safe-day / tree-hazard），无法走路由级中间件，故在 handler 内判定

// sheet 范围校验（口径照抄 worklog/index.js sheetRange）；不合法时已响应错误、返回 null
function checkSheetRange(res, from, to) {
  if (!DATE_RE.test(from) || !DATE_RE.test(to) || from > to) {
    fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD，且 from 不晚于 to');
    return null;
  }
  const days = (new Date(`${to}T00:00:00Z`) - new Date(`${from}T00:00:00Z`)) / 86400000 + 1;
  if (days > 31) {
    fail(res, 400, 40000, '日期范围不能超过 31 天');
    return null;
  }
  return { from, to };
}

// 费用汇总 / 派车汇总文件名：口径照抄 worklog/index.js feeFileName 与 /dispatch-sheet 下载路由（单日带单日期，跨天带范围）；
// 工作任务单文件名直接用 worklog 导出的 sheetFileName
function feeSheetFileName(from, to) {
  return from === to ? `费用汇总-${from}.docx` : `费用汇总-${from}至${to}.docx`;
}

function dispatchSheetFileName(from, to) {
  return from === to ? `派车汇总-${from}.xlsx` : `派车汇总-${from}至${to}.xlsx`;
}

function sheetFileNameOf(sheet, from, to) {
  if (sheet === 'task') return worklog.sheetFileName(from, to);
  if (sheet === 'fee') return feeSheetFileName(from, to);
  return dispatchSheetFileName(from, to);
}

// 多源签发共用的只读编辑器外壳（docCfg 结构与网盘分支一致，仅 document/editorConfig 差异）：
// 强制 mode='view'、permissions 只读、editorConfig 不配 callbackUrl、customization 不配 forcesave——只读不回写
function viewDocCfg({ documentType, ext, key, name, url, user }) {
  return {
    document: {
      fileType: ext,
      key,
      title: name,
      url,
      permissions: { edit: false, download: true, print: true, review: false, comment: false },
    },
    documentType,
    editorConfig: {
      lang: 'zh-CN',
      region: 'zh-CN',
      mode: 'view',
      user: { id: String(user.id), name: user.nickname || user.username },
      customization: {
        close: { visible: true, text: '关闭文件' }, // 配合页面 events.onRequestClose 返回来源页
        unit: 'cm',
        // 默认关拼写检查（同网盘分支口径：用户界面内开启后浏览器记忆优先）
        features: { spellcheck: false },
      },
    },
  };
}

// view-only 来源回源地址：票据即鉴权（TTL 同网盘分支用 OO_FILE_TICKET_TTL），URL 尾段带文件名供 OO 按扩展名识别格式
function rawUrl(payload, name) {
  const ticket = jwt.sign(payload, config.jwt.secret, { expiresIn: config.office.fileTicketTtl });
  return `${config.office.callbackBase}/api/v1/office/raw/${ticket}/${encodeURIComponent(name)}`;
}

// 安全日记录产物路径解析（口径同 safeday/index.js recordFilePath：优先 docs/{班组}/ 子目录，旧记录回退 docs/ 根目录）
function safedayFilePath(record) {
  const base = path.basename(record.fileName || '');
  if (record.team) {
    const nested = path.join(SAFEDAY_DOCS_DIR, record.team, base);
    if (fs.existsSync(nested)) return nested;
  }
  return path.join(SAFEDAY_DOCS_DIR, base);
}

// 功能开关（全局能力探测，无敏感数据，登录即可查；网盘/出工日志/安全日等各入口据此显隐「在线查看/编辑」）
router.get('/status', auth, (req, res) => ok(res, { enabled: enabled() }));

// 编辑器配置签发（整个 config 以 OO_JWT_SECRET 签名，OnlyOffice 校验后方可打开）；
// body.src 判别来源：缺省走网盘文件（原逻辑）；worklog-sheet / safeday / worklog-remark 为 view-only 多源（实现见下方 sign*）
router.post('/config', auth, async (req, res) => {
  try {
    const src = String((req.body && req.body.src) || '');
    if (src === 'worklog-sheet') return await signWorklogSheet(req, res);
    if (src === 'safeday') return await signSafeday(req, res);
    if (src === 'worklog-remark') return await signWorklogRemark(req, res);
    if (src === 'treehazard') return await signTreehazard(req, res);
    if (src) return fail(res, 400, 40001, '参数错误：src');
    // 缺省来源 = 网盘文件：应用权限自路由级中间件挪入分支内判定（多源门禁应用各异），语义与 requireApp('netdisk') 一致
    if (!(await hasAppPermission(req.user.id, 'netdisk'))) {
      return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
    }
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
    // 回调票据：绑定文件绝对路径与签发人（覆盖最长编辑会话，默认 12h）；view 只读不回写，不签发也不下发
    const cbTicket = mode === 'edit'
      ? jwt.sign({ ooc: abs, uid: req.user.id }, config.jwt.secret, { expiresIn: config.office.callbackTicketTtl })
      : '';
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
        // 仅 edit 下发 callbackUrl（view 只读会话不产生只读写回）
        ...(mode === 'edit' ? { callbackUrl: `${cb}/api/v1/office/callback/${cbTicket}` } : {}),
        lang: 'zh-CN',
        region: 'zh-CN',
        mode,
        user: { id: String(req.user.id), name: req.user.nickname || req.user.username },
        customization: {
          forcesave: mode === 'edit', // 保存按钮触发 status 6 回调即时写回（仅编辑模式有意义）
          close: { visible: true, text: '关闭文件' }, // 配合页面 events.onRequestClose 返回网盘
          unit: 'cm',
          // OO 工具带维持原生完整两行（2026-10-14 评审撤回紧凑化）；
          // 默认关拼写检查（用户界面内开启后浏览器记忆优先）
          features: { spellcheck: false },
        },
      },
    };
    docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
    return ok(res, { public_url: config.office.publicUrl, config: docCfg });
  } catch (err) { return relayFail(res, err); }
});

// ===== /config 多源签发实现（均为 view-only：docCfg 结构照抄网盘分支，差异仅 document/editorConfig） =====

// src='worklog-sheet'：出工日志 工作任务单 / 费用汇总 / 派车汇总（body {sheet, from, to, team_id?, force?}）
async function signWorklogSheet(req, res) {
  if (!enabled()) return fail(res, 503, 50301, '在线编辑未配置（OO_PUBLIC_URL）');
  if (!config.office.jwtSecret) return fail(res, 503, 50301, '在线编辑未配置（OO_JWT_SECRET）');
  if (!(await hasAppPermission(req.user.id, 'work-log'))) {
    return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
  }
  const body = req.body || {};
  const sheet = String(body.sheet || '');
  if (!['task', 'fee', 'dispatch'].includes(sheet)) {
    return fail(res, 400, 40001, '参数错误：sheet（task/fee/dispatch）');
  }
  // 班组与权限复刻 worklog requireDictAdmin 语义：超管任意班组（team_id 指定）、班组管理员仅本班，其余 40304
  const team = await teamUtil.resolveTeam(req.user, body.team_id);
  if (req.user.role !== 'admin' && !(req.user.role === 'team_admin' && team && req.user.team_id === team.id)) {
    return fail(res, 403, 40304, '仅管理员可执行此操作');
  }
  if (!team) return fail(res, 400, 40010, '无可用班组');
  const range = checkSheetRange(res, String(body.from || ''), String(body.to || ''));
  if (!range) return;
  // 强制执行口径同 worklog /sheet/save-netdisk（body.force 为 true/1/'1'）
  const force = body.force === true || body.force === 1 || body.force === '1';
  // 生成前核验（文案/结构照抄 worklog failIfVerifyFailed）：task/fee 非 force 时范围内有未通过记录即 40901 拦截；
  // dispatch 不接核验（派车单号清单与照片核验无关）
  if (sheet !== 'dispatch' && !force) {
    const failures = await worklog.sheetVerifyFailures(team.id, range.from, range.to);
    if (failures) {
      return fail(res, 409, 40901, `所选范围内有 ${failures.length} 条记录存在未通过项，请处理后再操作`, { failures });
    }
  }
  // documentType 按 sheet 定：task/fee 为 docx 模板产物（word）、dispatch 为 xlsx（cell），均在 DOC_TYPES 内
  const documentType = sheet === 'dispatch' ? 'cell' : 'word';
  const ext = sheet === 'dispatch' ? 'xlsx' : 'docx';
  const name = sheetFileNameOf(sheet, range.from, range.to);
  // document.key 带瞬时量：同一范围重复签发不复用 OO 缓存（sheet 内容随库即时变化，防 OO 按 key 缓存脏读）
  const key = crypto.createHash('sha1')
    .update(`${sheet}|${team.id}|${range.from}|${range.to}|${force}|${Date.now()}`)
    .digest('hex');
  const url = rawUrl({ src: 'wls', sheet, team_id: team.id, from: range.from, to: range.to, force }, name);
  const docCfg = viewDocCfg({ documentType, ext, key, name, url, user: req.user });
  docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
  return ok(res, { public_url: config.office.publicUrl, config: docCfg });
}

// src='safeday'：安全日记录产物（body {id}；本地落盘 docx，view-only）
async function signSafeday(req, res) {
  if (!enabled()) return fail(res, 503, 50301, '在线编辑未配置（OO_PUBLIC_URL）');
  if (!config.office.jwtSecret) return fail(res, 503, 50301, '在线编辑未配置（OO_JWT_SECRET）');
  if (!(await hasAppPermission(req.user.id, 'safe-day'))) {
    return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
  }
  const id = String((req.body && req.body.id) || '');
  // 归属校验在 getAccessibleRecordFile 内（超管任意班组、其余仅本班；记录不存在/无权限/产物未落盘同判 null）
  const got = await safeday.getAccessibleRecordFile(req.user, id);
  if (!got) return fail(res, 404, 40400, '记录不存在或无权查看');
  const { record, filePath, fileName } = got;
  const st = fs.statSync(filePath);
  // document.key：sha1(记录 id|size|mtime)，产物重渲染内容变即滚动
  const key = crypto.createHash('sha1').update(`${record.id}|${st.size}|${st.mtimeMs}`).digest('hex');
  const ext = (fileName.split('.').pop() || '').toLowerCase();
  const url = rawUrl({ src: 'sdr', id: String(id) }, fileName);
  const docCfg = viewDocCfg({ documentType: 'word', ext, key, name: fileName, url, user: req.user });
  docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
  return ok(res, { public_url: config.office.publicUrl, config: docCfg });
}

// src='worklog-remark'：出工日志备注附件（body {id, key, team_id?}；COS 公网地址经 /raw 转发，view-only）
async function signWorklogRemark(req, res) {
  if (!enabled()) return fail(res, 503, 50301, '在线编辑未配置（OO_PUBLIC_URL）');
  if (!config.office.jwtSecret) return fail(res, 503, 50301, '在线编辑未配置（OO_JWT_SECRET）');
  if (!(await hasAppPermission(req.user.id, 'work-log'))) {
    return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
  }
  const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id);
  if (!team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
  const { entry, file } = await worklog.findRemarkFile(
    Number((req.body || {}).id), String((req.body || {}).key || ''), team.id
  );
  if (!entry) return fail(res, 404, 40400, '日志不存在');
  if (!file) return fail(res, 404, 40400, '附件不存在');
  const ext = (file.name.split('.').pop() || '').toLowerCase();
  const documentType = DOC_TYPES[ext] || VIEW_TYPES[ext];
  if (!documentType) return fail(res, 400, 40030, '该格式不支持在线打开');
  // document.key：附件不可变（删旧传新即换 cos_key），sha1(cos_key) 即内容指纹
  const key = crypto.createHash('sha1').update(String(file.cos_key)).digest('hex');
  const url = rawUrl({ src: 'wlr', key: file.cos_key, name: file.name }, file.name);
  const docCfg = viewDocCfg({ documentType, ext, key, name: file.name, url, user: req.user });
  docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
  return ok(res, { public_url: config.office.publicUrl, config: docCfg });
}

// src='treehazard'：树障隐患统计表（body {line?, team_id?}；按班组即时生成 xlsx，view-only）
async function signTreehazard(req, res) {
  if (!enabled()) return fail(res, 503, 50301, '在线编辑未配置（OO_PUBLIC_URL）');
  if (!config.office.jwtSecret) return fail(res, 503, 50301, '在线编辑未配置（OO_JWT_SECRET）');
  if (!(await hasAppPermission(req.user.id, 'tree-hazard'))) {
    return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
  }
  const body = req.body || {};
  const team = await teamUtil.resolveTeam(req.user, body.team_id);
  if (!team) return fail(res, 400, 40010, '无可用班组');
  const line = String(body.line || '').trim();
  const split = body.split === '1' || body.split === 1 || body.split === true; // 按线路分 sheet（office.html query 全透传入 body）
  const name = thdExport.exportFileName(team.name, line || undefined);
  // document.key 带瞬时量：同一班组重复签发不复用 OO 缓存（内容随库即时变化，防 OO 按 key 缓存脏读）
  const key = crypto.createHash('sha1').update(`treehazard|${team.id}|${line}|${split ? 1 : 0}|${Date.now()}`).digest('hex');
  const url = rawUrl({ src: 'thd', team_id: team.id, line, split }, name);
  const docCfg = viewDocCfg({ documentType: 'cell', ext: 'xlsx', key, name, url, user: req.user });
  docCfg.token = jwt.sign(docCfg, config.office.jwtSecret);
  return ok(res, { public_url: config.office.publicUrl, config: docCfg });
}

// ===== view-only 多源回源（免登录：OnlyOffice 容器凭票据取流；与 /callback 并列，挂位与口径参照网盘 /preview/:token/:name?） =====
// 票据即鉴权：短时效 JWT 绑定单来源单文件（TTL = OO_FILE_TICKET_TTL）；验签失败或载荷非本路由票据一律 401 纯文本
//（OO 不消费 {code,message,data} 信封）；URL 尾段 :name 仅供 OO 按扩展名识别格式，实际取数全按票据载荷
router.get('/raw/:ticket/:name?', async (req, res) => {
  let ticket = null;
  try { ticket = jwt.verify(String(req.params.ticket || ''), config.jwt.secret); } catch (err) { /* 按无效票据处理 */ }
  const src = (ticket && typeof ticket === 'object') ? String(ticket.src || '') : '';
  try {
    if (src === 'wls') return await rawWorklogSheet(ticket, res);
    if (src === 'sdr') return await rawSafeday(ticket, res);
    if (src === 'wlr') return await rawWorklogRemark(ticket, res);
    if (src === 'thd') return await rawTreehazard(ticket, res);
    return res.status(401).type('text').send('回源票据无效或已过期');
  } catch (err) {
    console.error(`[OO] 回源失败（src=${src || '无效票据'}）：`, err && err.message);
    if (!res.headersSent) return res.status(500).type('text').send('回源失败，请稍后再试');
    return res.destroy(err);
  }
});

// wls 回源：出工日志 sheet 按载荷即时生成（build 调用签名照抄 worklog /task-sheet 等下载路由）；
// 生成前核验在签发侧已完成（force 已固化进载荷），回源按票据直出；即时生成的 buffer 一次性 res.end
async function rawWorklogSheet(ticket, res) {
  const team = await teamUtil.getTeamById(ticket.team_id);
  if (!team || team.status !== 1) return res.status(404).type('text').send('班组不存在或已停用');
  let result = null;
  if (ticket.sheet === 'task') result = await tasksheet.build(team, ticket.from, ticket.to);
  else if (ticket.sheet === 'fee') result = await feesheet.build(team, ticket.from, ticket.to);
  else if (ticket.sheet === 'dispatch') result = await dispatchsheet.build(team.id, ticket.from, ticket.to);
  else return res.status(401).type('text').send('回源票据无效或已过期');
  if (!result) return res.status(404).type('text').send('该日期范围没有出车记录，无可生成的内容');
  const buf = Buffer.isBuffer(result) ? result : result.buffer;
  const name = sheetFileNameOf(ticket.sheet, ticket.from, ticket.to);
  const ext = ticket.sheet === 'dispatch' ? 'xlsx' : 'docx';
  res.setHeader('Content-Type', RAW_MIME[ext]);
  res.setHeader('Content-Length', buf.length);
  res.setHeader('Content-Disposition', `inline; filename*=UTF-8''${encodeURIComponent(name)}`);
  return res.end(buf);
}

// sdr 回源：安全日记录产物（回源无用户态，按 id 直查记录取文件路径，fs 流式返回；
// 归属校验在签发侧 getAccessibleRecordFile 已完成，票据即授权凭证）
async function rawSafeday(ticket, res) {
  const record = safedayStore.get(String(ticket.id || ''));
  if (!record) return res.status(404).type('text').send('记录不存在');
  const filePath = safedayFilePath(record);
  const name = path.basename(record.fileName || '');
  let st = null;
  try { st = fs.statSync(filePath); } catch (err) { /* 按不存在处理 */ }
  if (!st || !st.isFile()) return res.status(404).type('text').send('文件不存在，可能已被清理');
  const ext = (name.split('.').pop() || '').toLowerCase();
  res.setHeader('Content-Type', RAW_MIME[ext] || 'application/octet-stream');
  res.setHeader('Content-Length', st.size);
  res.setHeader('Content-Disposition', `inline; filename*=UTF-8''${encodeURIComponent(name)}`);
  const stream = fs.createReadStream(filePath);
  stream.on('error', (serr) => {
    console.error(`[OO] 安全日产物回源断流（${filePath}）：`, serr.message);
    res.destroy(serr);
  });
  return stream.pipe(res);
}

// wlr 回源：出工日志备注附件（COS 公共读，流式转发口径照抄 worklog /logs/:id/remark-download：Content-Length 透传）
async function rawWorklogRemark(ticket, res) {
  const key = String(ticket.key || '');
  const name = String(ticket.name || '') || '附件';
  const resp = await fetch(worklogCos.publicUrl(key), { signal: AbortSignal.timeout(60000) });
  if (!resp.ok) return res.status(502).type('text').send(`附件回源失败（HTTP ${resp.status}）`);
  const ext = (name.split('.').pop() || '').toLowerCase();
  res.setHeader('Content-Type', RAW_MIME[ext] || resp.headers.get('content-type') || 'application/octet-stream');
  const len = Number(resp.headers.get('content-length') || 0);
  if (len) res.setHeader('Content-Length', len);
  res.setHeader('Content-Disposition', `inline; filename*=UTF-8''${encodeURIComponent(name)}`);
  return Readable.fromWeb(resp.body).pipe(res);
}

// thd 回源：树障统计表按载荷即时生成（复用 treehazard 的 buildTeamExport，与 /export 下载同口径；buffer 一次性 res.end）
async function rawTreehazard(ticket, res) {
  const team = await teamUtil.getTeamById(ticket.team_id);
  if (!team || team.status !== 1) return res.status(404).type('text').send('班组不存在或已停用');
  const { buf, fileName } = await treehazard.buildTeamExport(team, String(ticket.line || '').trim(), !!ticket.split);
  res.setHeader('Content-Type', RAW_MIME.xlsx);
  res.setHeader('Content-Length', buf.length);
  res.setHeader('Content-Disposition', `inline; filename*=UTF-8''${encodeURIComponent(fileName)}`);
  return res.end(buf);
}

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
