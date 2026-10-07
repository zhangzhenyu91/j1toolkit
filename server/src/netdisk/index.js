// 团队网盘：OpenList 中转层（双端自定义前端 → 本模块 → 回环/内网的 OpenList，OpenList 不对公网暴露）
// 空间口径：my 个人空间 = {personalRoot}/{username}；public 公共区 = {publicRoot}（见 config.netdisk）
// 权限口径：公共区上传/新建文件夹全员可用，重命名/删除仅班管（team_admin）、超管（admin）；
//           分享：创建不限，停用/删除需本人或班管/超管，查看全部仅班管/超管
// 分享免登录访问：/public/* 挂在 auth 门控之前；提取码校验走 nd_share 表，验证通过发 2h 访问票据（JWT）
const express = require('express');
const crypto = require('crypto');
const jwt = require('jsonwebtoken');
const config = require('../config');
const { pool } = require('../db');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const tokenQuery = require('../utils/tokenQuery');
const { ok, fail } = require('../utils/resp');
const ol = require('./openlist');

const router = express.Router();

// 页面 <img>/<video> 预览与下载无法附带请求头：?token= 映射为 Authorization（统一鉴权口径）
router.use(tokenQuery);

// ================= 基础工具 =================

const canManagePublic = (user) => user.role === 'admin' || user.role === 'team_admin';

const { teamNameOf, spaceRootOf, normRel } = require('./space');

// 空间根解析：my 个人空间恒有；public 公共区按班组隔离（{publicRoot}/{班组名}），未分配班组无公共区
// 返回值：字符串=可用根；null=已响应错误（调用方直接 return）
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

const joinPath = (root, rel) => (rel === '/' ? root : `${root}${rel}`);
const baseName = (p) => p.split('/').filter(Boolean).pop() || '';
function dirName(p) {
  const parts = p.split('/').filter(Boolean);
  parts.pop();
  return parts.length ? `/${parts.join('/')}` : '/';
}

// 文件名校验：不允许斜杠/控制符/纯点/超长
function validName(name) {
  const n = String(name || '').trim();
  if (!n || n.length > 200) return null;
  if (/[/\\\r\n]/.test(n) || n === '.' || n === '..') return null;
  return n;
}

// OpenList 文件对象 → 前端条目
const mapItem = (o) => ({
  name: o.name,
  is_dir: !!o.is_dir,
  size: o.size || 0,
  modified: o.modified || '',
});

// 上游错误归一（仅透传可读文案；object not found 映射 40400）
function relayFail(res, err) {
  if (err && err.expose) {
    if (/not found/i.test(err.message || '')) {
      return fail(res, 404, 40400, '文件或目录不存在');
    }
    return fail(res, err.status || 502, err.code || 50201, err.message);
  }
  console.error('[网盘] OpenList 调用失败：', err && err.message);
  return fail(res, 502, 50201, '网盘服务异常，请稍后再试');
}

// ================= 分享公共访问（免登录，挂在 auth 之前） =================

async function loadShare(id) {
  const sid = String(id || '').trim().slice(0, 32);
  if (!/^[a-f0-9]{10,32}$/.test(sid)) return null;
  const [rows] = await pool.query('SELECT * FROM nd_share WHERE share_id = ?', [sid]);
  return rows[0] || null;
}

// 状态判定：停用 / 过期 / 生效（未通过时直接响应并返回 false）
function shareUsable(res, row) {
  if (!row) { fail(res, 404, 40400, '分享不存在或已删除'); return false; }
  if (row.status !== 1) { fail(res, 403, 40310, '分享已停用'); return false; }
  if (row.expire_at && new Date(row.expire_at).getTime() < Date.now()) {
    fail(res, 410, 41010, '分享已过期'); return false;
  }
  return true;
}

// 提取码失败限流：每 IP 每分享 10 分钟 10 次（内存口径，重启清零）
const pwFails = new Map();
function pwLimited(ip, sid) {
  const key = `${ip}|${sid}`;
  const now = Date.now();
  const rec = pwFails.get(key);
  if (pwFails.size > 5000) pwFails.clear(); // 防缓慢膨胀，清零即重置窗口
  if (rec && now < rec.resetAt && rec.count >= 10) return true;
  return false;
}
function pwFailIncr(ip, sid) {
  const key = `${ip}|${sid}`;
  const now = Date.now();
  const rec = pwFails.get(key);
  if (!rec || now >= rec.resetAt) pwFails.set(key, { count: 1, resetAt: now + 600000 });
  else rec.count += 1;
}

// 访问凭据：ticket（verify 签发的 2h JWT）或 password（逐次校验）
function shareAuth(req, row) {
  const tk = String((req.query && req.query.ticket) || (req.body && req.body.ticket) || '');
  if (tk) {
    try {
      const payload = jwt.verify(tk, config.jwt.secret);
      if (payload && payload.sid === row.share_id) return true;
    } catch (err) { /* 票据无效则回落提取码 */ }
  }
  const pw = String((req.query && req.query.password) || (req.body && req.body.password) || '');
  return pw !== '' && pw === row.password;
}

// 越界校验：rel 须为某个分享项本身或其子路径，返回 OpenList 绝对路径；不合法返回 null
function resolveShareAbs(row, rel) {
  let paths;
  try { paths = JSON.parse(row.paths); } catch (err) { paths = []; }
  if (!Array.isArray(paths)) return null;
  for (const p of paths) {
    if (rel === p || rel.startsWith(`${p}/`)) return joinPath(row.base_path, rel);
  }
  return null;
}

// 分享信息（不含内容，前端据此弹提取码框）
router.get('/public/share/:id', async (req, res) => {
  try {
    const row = await loadShare(req.params.id);
    if (!shareUsable(res, row)) return;
    return ok(res, {
      creator: row.creator,
      expire_at: row.expire_at,
      created_at: row.created_at,
    });
  } catch (err) { return relayFail(res, err); }
});

// 提取码验证：通过则发 2h 访问票据并返回首层内容
router.post('/public/share/:id/verify', async (req, res) => {
  try {
    const row = await loadShare(req.params.id);
    if (!shareUsable(res, row)) return;
    if (pwLimited(req.ip, row.share_id)) {
      return fail(res, 429, 42901, '尝试次数过多，请 10 分钟后再试');
    }
    const pw = String((req.body && req.body.password) || '');
    if (pw !== row.password) {
      pwFailIncr(req.ip, row.share_id);
      return fail(res, 403, 40311, '提取码错误');
    }
    pwFails.delete(`${req.ip}|${row.share_id}`);
    const ticket = jwt.sign({ sid: row.share_id }, config.jwt.secret, { expiresIn: '2h' });
    return ok(res, { ticket, items: await shareRootItems(row) });
  } catch (err) { return relayFail(res, err); }
});

// 首层内容：逐项取元信息（已被移动/删除的项跳过）
async function shareRootItems(row) {
  let paths;
  try { paths = JSON.parse(row.paths); } catch (err) { paths = []; }
  const items = [];
  for (const p of paths) {
    try {
      const meta = await ol.fsGet(joinPath(row.base_path, p));
      items.push({ ...mapItem(meta), rel: p });
    } catch (err) { /* 失效项跳过 */ }
  }
  return items;
}

// 目录浏览（分享项为文件夹时可下钻）
router.post('/public/share/:id/list', async (req, res) => {
  try {
    const row = await loadShare(req.params.id);
    if (!shareUsable(res, row)) return;
    if (!shareAuth(req, row)) return fail(res, 403, 40311, '提取码错误');
    const rel = normRel((req.body && req.body.path) || '/');
    if (rel === null) return fail(res, 400, 40001, '路径不合法');
    if (rel === '/') return ok(res, { path: '/', items: await shareRootItems(row) });
    const abs = resolveShareAbs(row, rel);
    if (!abs) return fail(res, 403, 40312, '路径超出分享范围');
    const data = await ol.fsList(abs);
    return ok(res, { path: rel, items: (data.content || []).map(mapItem) });
  } catch (err) { return relayFail(res, err); }
});

// 文件下载（流式中转）
router.get('/public/share/:id/download', async (req, res) => {
  try {
    const row = await loadShare(req.params.id);
    if (!shareUsable(res, row)) return;
    if (!shareAuth(req, row)) return fail(res, 403, 40311, '提取码错误');
    const rel = normRel(req.query.path || '');
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const abs = resolveShareAbs(row, rel);
    if (!abs) return fail(res, 403, 40312, '路径超出分享范围');
    return await pipeDownload(res, abs, baseName(rel), 'attachment');
  } catch (err) { return relayFail(res, err); }
});

// ================= Office 在线预览回源（微软 view.officeapps.live.com 等第三方查看器） =================
// 机制：登录态调 /preview/sign 签 10 分钟单文件票据（JWT 绑定绝对路径）；微软服务器经 /preview/:token 免登录回源取流。
// 不直接把用户 JWT 贴给第三方；票据短时效、仅绑定单个文件。
const PREVIEW_MIME = {
  doc: 'application/msword',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xls: 'application/vnd.ms-excel',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  ppt: 'application/vnd.ms-powerpoint',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
};

router.get('/preview/:token/:name?', async (req, res) => {
  try {
    let payload;
    try {
      payload = jwt.verify(String(req.params.token || ''), config.jwt.secret);
    } catch (err) {
      return fail(res, 403, 40311, '预览票据无效或已过期');
    }
    const abs = payload && payload.ndp;
    if (typeof abs !== 'string' || !abs.startsWith('/')) {
      return fail(res, 403, 40311, '预览票据无效或已过期');
    }
    const name = baseName(abs);
    const ext = (name.split('.').pop() || '').toLowerCase();
    const r = await ol.downloadStream(abs);
    if (r.headers['content-length']) res.setHeader('Content-Length', r.headers['content-length']);
    // 内容类型按扩展名给准（上游可能只回 octet-stream，查看器依赖它判格式）
    res.setHeader('Content-Type', PREVIEW_MIME[ext] || r.headers['content-type'] || 'application/octet-stream');
    res.setHeader('Content-Disposition', `inline; filename*=UTF-8''${encodeURIComponent(name)}`);
    r.data.on('error', (serr) => {
      console.error(`[网盘] 预览回源上游断流（${abs}）：`, serr.message);
      res.destroy(serr);
    });
    return r.data.pipe(res);
  } catch (err) { return relayFail(res, err); }
});

// ================= 登录 + 应用权限门控（以下均需 netdisk 应用权限） =================
router.use(auth, requireApp('netdisk'));

// 流式下载共用：透传长度与类型，按 disposition 强制内联/下载；源流异常销毁响应
async function pipeDownload(res, abs, name, disposition) {
  const r = await ol.downloadStream(abs);
  if (r.headers['content-length']) res.setHeader('Content-Length', r.headers['content-length']);
  if (r.headers['content-type']) res.setHeader('Content-Type', r.headers['content-type']);
  res.setHeader('Content-Disposition',
    `${disposition}; filename*=UTF-8''${encodeURIComponent(name)}`);
  r.data.on('error', (serr) => {
    console.error(`[网盘] 下载上游断流（${abs}）：`, serr.message);
    res.destroy(serr);
  });
  return r.data.pipe(res);
}

// 空间列表（个人空间恒有；公共区按班组隔离，未分配班组不下发；manageable 仅控制公共区重命名/删除入口）
router.get('/spaces', async (req, res) => {
  try {
    const spaces = [{ key: 'my', name: '我的空间', manageable: true }];
    const team = await teamNameOf(req.user);
    if (team) {
      spaces.push({ key: 'public', name: '公共区', team, manageable: canManagePublic(req.user) });
    }
    return ok(res, { max_upload_mb: config.netdisk.maxUploadMb, spaces });
  } catch (err) { return relayFail(res, err); }
});

// 目录列表（个人/公共根目录自愈式创建）
router.post('/list', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    if (rel === null) return fail(res, 400, 40001, '路径不合法');
    if (rel === '/') await ol.ensureDir(root);
    const data = await ol.fsList(joinPath(root, rel));
    return ok(res, {
      space: req.body.space,
      path: rel,
      manageable: req.body.space === 'my' || canManagePublic(req.user),
      items: (data.content || []).map(mapItem),
    });
  } catch (err) { return relayFail(res, err); }
});

// 文件名搜索（范围限定当前空间根；需 OpenList 已建索引）
router.post('/search', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const keywords = String((req.body && req.body.keywords) || '').trim().slice(0, 100);
    if (!keywords) return fail(res, 400, 40001, '参数错误：keywords');
    const page = Math.max(1, parseInt((req.body && req.body.page) || '1', 10) || 1);
    const perPage = Math.min(100, Math.max(1, parseInt((req.body && req.body.per_page) || '30', 10) || 30));
    const data = await ol.fsSearch(root, keywords, page, perPage);
    const items = (data.content || []).map((o) => {
      const parent = String(o.parent || root);
      return { ...mapItem(o), parent: parent === root ? '/' : parent.slice(root.length) || '/' };
    });
    return ok(res, { items, total: data.total || 0, page, per_page: perPage });
  } catch (err) { return relayFail(res, err); }
});

// 新建文件夹（公共区全员可建）
router.post('/mkdir', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    const name = validName(req.body && req.body.name);
    if (rel === null || !name) return fail(res, 400, 40001, '参数错误：path/name');
    await ol.fsMkdir(`${joinPath(root, rel)}/${name}`);
    return ok(res, null, '已创建');
  } catch (err) { return relayFail(res, err); }
});

// 重命名（公共区仅班管/超管）
router.post('/rename', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    if (req.body.space === 'public' && !canManagePublic(req.user)) {
      return fail(res, 403, 40304, '公共区仅班管/超管可整理');
    }
    const rel = normRel(req.body && req.body.path);
    const name = validName(req.body && req.body.name);
    if (rel === null || rel === '/' || !name) return fail(res, 400, 40001, '参数错误：path/name');
    await ol.fsRename(joinPath(root, rel), name);
    return ok(res, null, '已重命名');
  } catch (err) { return relayFail(res, err); }
});

// 批量删除（公共区仅班管/超管；按目录分组逐批调上游）
router.post('/remove', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    if (req.body.space === 'public' && !canManagePublic(req.user)) {
      return fail(res, 403, 40304, '公共区仅班管/超管可整理');
    }
    const paths = (req.body && req.body.paths) || [];
    if (!Array.isArray(paths) || !paths.length || paths.length > 100) {
      return fail(res, 400, 40001, '参数错误：paths（1-100 条）');
    }
    const groups = new Map();
    for (const p of paths) {
      const rel = normRel(p);
      if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
      const dir = dirName(rel);
      if (!groups.has(dir)) groups.set(dir, []);
      groups.get(dir).push(baseName(rel));
    }
    for (const [dir, names] of groups) {
      await ol.fsRemove(joinPath(root, dir), names);
    }
    return ok(res, null, '已删除');
  } catch (err) { return relayFail(res, err); }
});

// 移动 / 复制（跨空间允许：目的侧按上传口径全员可写；移动会移除源位置，源在公共区时仅班管/超管）
async function moveCopy(req, res, op) {
  try {
    ol.ensureConfigured();
    const body = req.body || {};
    const srcRoot = await resolveRoot(req, res, body.src_space);
    if (!srcRoot) return;
    const dstRoot = await resolveRoot(req, res, body.dst_space);
    if (!dstRoot) return;
    if (op === 'move' && body.src_space === 'public' && !canManagePublic(req.user)) {
      return fail(res, 403, 40304, '公共区仅班管/超管可整理');
    }
    const srcDir = normRel(body.src_dir);
    const dstDir = normRel(body.dst_dir);
    if (srcDir === null || dstDir === null) return fail(res, 400, 40001, '路径不合法');
    const names = Array.isArray(body.names) ? body.names.map(validName) : [];
    if (!names.length || names.length > 100 || names.some((n) => !n)) {
      return fail(res, 400, 40001, '参数错误：names（1-100 条）');
    }
    if (body.src_space === body.dst_space && srcDir === dstDir) {
      return fail(res, 400, 40030, '源位置与目标位置相同');
    }
    // 目标目录自愈（个人空间根可能未建）
    await ol.ensureDir(joinPath(dstRoot, dstDir));
    const fn = op === 'move' ? ol.fsMove : ol.fsCopy;
    await fn(joinPath(srcRoot, srcDir), joinPath(dstRoot, dstDir), names);
    return ok(res, null, op === 'move' ? '已移动' : '已复制');
  } catch (err) { return relayFail(res, err); }
}

router.post('/move', (req, res) => moveCopy(req, res, 'move'));
router.post('/copy', (req, res) => moveCopy(req, res, 'copy'));

// ================= 压缩包预览 / 解压 =================

// 压缩包条目（含内层路径，递归映射 children 树）
const mapArchiveItem = (o, innerBase) => ({
  name: o.name,
  is_dir: !!o.is_dir,
  size: o.size || 0,
  modified: o.modified || '',
  rel: `${innerBase === '/' ? '' : innerBase}/${o.name}`,
  ...(Array.isArray(o.children) && o.children.length
    ? { children: o.children.map((c) => mapArchiveItem(c, `${innerBase === '/' ? '' : innerBase}/${o.name}`)) }
    : {}),
});

// 压缩包元信息（encrypted=true 时前端弹压缩密码框，archive_pass 随请求透传）
router.post('/archive/meta', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const meta = await ol.fsArchiveMeta(joinPath(root, rel), req.body && req.body.archive_pass);
    return ok(res, {
      encrypted: !!meta.encrypted,
      comment: meta.comment || '',
      content: (meta.content || []).map((o) => mapArchiveItem(o, '/')),
    });
  } catch (err) { return relayFail(res, err); }
});

// 压缩包内层目录平铺列表
router.post('/archive/list', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const inner = normRel((req.body && req.body.inner_path) || '/');
    if (inner === null) return fail(res, 400, 40001, '内层路径不合法');
    const data = await ol.fsArchiveList(joinPath(root, rel), inner, req.body && req.body.archive_pass);
    const items = (data.content || data || []).map((o) => mapArchiveItem(o, inner));
    return ok(res, { inner_path: inner, items });
  } catch (err) { return relayFail(res, err); }
});

// 解压（写目的目录，口径同上传：全员可用；公共区目的目录同理）
router.post('/archive/decompress', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    const dstDir = normRel(req.body && req.body.dst_dir);
    if (rel === null || rel === '/' || dstDir === null) {
      return fail(res, 400, 40001, '参数错误：path/dst_dir');
    }
    await ol.ensureDir(joinPath(root, dstDir));
    await ol.fsArchiveDecompress(dirName(joinPath(root, rel)), joinPath(root, dstDir), [baseName(rel)], {
      archivePass: req.body && req.body.archive_pass,
      putIntoNewDir: !req.body || req.body.put_into_new_dir !== false,
      overwrite: !!(req.body && req.body.overwrite),
    });
    return ok(res, null, '已解压');
  } catch (err) { return relayFail(res, err); }
});

// 包内文件下载（流式中转；inner 为包内路径）
router.get('/archive/download', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.query.space);
    if (!root) return;
    const rel = normRel(req.query.path || '');
    const inner = normRel(req.query.inner || '');
    if (rel === null || rel === '/' || inner === null || inner === '/') {
      return fail(res, 400, 40001, '参数错误：path/inner');
    }
    const r = await ol.archiveDownloadStream(joinPath(root, rel), inner, req.query.archive_pass);
    if (r.headers['content-length']) res.setHeader('Content-Length', r.headers['content-length']);
    if (r.headers['content-type']) res.setHeader('Content-Type', r.headers['content-type']);
    res.setHeader('Content-Disposition',
      `attachment; filename*=UTF-8''${encodeURIComponent(baseName(inner))}`);
    r.data.on('error', (serr) => {
      console.error(`[网盘] 包内下载上游断流（${rel} 内 ${inner}）：`, serr.message);
      res.destroy(serr);
    });
    return r.data.pipe(res);
  } catch (err) { return relayFail(res, err); }
});

// 下载/预览（disposition=inline 时内联，供 <img>/<video>/iframe 直引；默认 attachment）
router.get('/download', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.query.space);
    if (!root) return;
    const rel = normRel(req.query.path || '');
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const disposition = req.query.disposition === 'inline' ? 'inline' : 'attachment';
    return await pipeDownload(res, joinPath(root, rel), baseName(rel), disposition);
  } catch (err) { return relayFail(res, err); }
});

// Office 在线预览票据签发（仅 Office 格式；回源地址由前端拼 location.origin + /preview/<token>）
router.post('/preview/sign', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
    const name = baseName(rel);
    const ext = (name.split('.').pop() || '').toLowerCase();
    if (!PREVIEW_MIME[ext]) return fail(res, 400, 40030, '该格式不支持在线预览');
    await ol.fsGet(joinPath(root, rel)); // 存在性前置校验
    const token = jwt.sign({ ndp: joinPath(root, rel) }, config.jwt.secret, { expiresIn: '10m' });
    return ok(res, { token, expires_in: 600 });
  } catch (err) { return relayFail(res, err); }
});

// 分片上传 - 初始化（0 字节文件直接走 put 建成）
router.post('/upload/init', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rel = normRel(req.body && req.body.path);
    const name = validName(req.body && req.body.name);
    const size = parseInt((req.body && req.body.size) || '', 10);
    if (rel === null || !name || !Number.isFinite(size) || size < 0) {
      return fail(res, 400, 40001, '参数错误：path/name/size');
    }
    if (size > config.netdisk.maxUploadMb * 1024 * 1024) {
      return fail(res, 413, 41301, `单文件最大 ${config.netdisk.maxUploadMb} MB`);
    }
    await ol.ensureDir(joinPath(root, rel));
    const abs = `${joinPath(root, rel)}/${name}`;
    if (size === 0) {
      await ol.putEmpty(abs);
      return ok(res, { state: 'completed', instant: true });
    }
    const snap = await ol.mpInit(abs, size);
    return ok(res, snap);
  } catch (err) { return relayFail(res, err); }
});

// 分片上传 - 传一片（裸字节中转；429 窗口满 / 409 分片冲突均带 retry 标记由前端退避重传）
router.put('/upload/chunk',
  express.raw({ type: 'application/octet-stream', limit: '64mb' }),
  async (req, res) => {
    try {
      ol.ensureConfigured();
      const uploadId = String(req.headers['x-upload-id'] || '').trim();
      const idx = parseInt(req.headers['x-chunk-index'] || '', 10);
      if (!/^[0-9a-f-]{16,64}$/i.test(uploadId) || !Number.isInteger(idx) || idx < 0) {
        return fail(res, 400, 40001, '参数错误：X-Upload-Id / X-Chunk-Index');
      }
      if (!Buffer.isBuffer(req.body) || !req.body.length) {
        return fail(res, 400, 40001, '分片内容为空或 Content-Type 非 application/octet-stream');
      }
      const snap = await ol.mpChunk(uploadId, idx, req.body);
      return ok(res, snap);
    } catch (err) {
      if (err && err.upstreamCode === 429) {
        return fail(res, 429, 42901, '网盘接收窗口已满，请稍后重试', { retry: true });
      }
      if (err && err.upstreamCode === 409) {
        return fail(res, 409, 40900, '分片上传冲突，请重试', { retry: true });
      }
      return relayFail(res, err);
    }
  });

// 分片上传 - 合并（阻塞到驱动落盘终态；被 CDN 掐断时前端用 status 轮询终态）
router.post('/upload/complete', async (req, res) => {
  try {
    ol.ensureConfigured();
    const uploadId = String((req.body && req.body.upload_id) || '').trim();
    if (!/^[0-9a-f-]{16,64}$/i.test(uploadId)) return fail(res, 400, 40001, '参数错误：upload_id');
    return ok(res, await ol.mpComplete(uploadId));
  } catch (err) { return relayFail(res, err); }
});

// 分片上传 - 会话查询（upload_id，或 space+path+name+size 找回断线会话）
router.get('/upload/status', async (req, res) => {
  try {
    ol.ensureConfigured();
    const uploadId = String(req.query.upload_id || '').trim();
    if (uploadId) {
      if (!/^[0-9a-f-]{16,64}$/i.test(uploadId)) return fail(res, 400, 40001, '参数错误：upload_id');
      return ok(res, await ol.mpStatus({ upload_id: uploadId }));
    }
    const root = await resolveRoot(req, res, req.query.space);
    if (!root) return;
    const rel = normRel(req.query.path || '');
    const name = validName(req.query.name);
    const size = parseInt(req.query.size || '', 10);
    if (rel === null || !name || !Number.isFinite(size) || size <= 0) {
      return fail(res, 400, 40001, '参数错误：path/name/size');
    }
    return ok(res, await ol.mpStatus({ path: `${joinPath(root, rel)}/${name}`, size: String(size) }));
  } catch (err) { return relayFail(res, err); }
});

// 分片上传 - 取消
router.post('/upload/abort', async (req, res) => {
  try {
    ol.ensureConfigured();
    const uploadId = String((req.body && req.body.upload_id) || '').trim();
    if (!/^[0-9a-f-]{16,64}$/i.test(uploadId)) return fail(res, 400, 40001, '参数错误：upload_id');
    return ok(res, await ol.mpAbort(uploadId));
  } catch (err) { return relayFail(res, err); }
});

// ================= 分享管理（登录态） =================

const SHARE_ID_RE = /^[a-f0-9]{10,32}$/;

// 状态派生：停用 > 过期 > 生效
function shareState(row) {
  if (row.status !== 1) return 'disabled';
  if (row.expire_at && new Date(row.expire_at).getTime() < Date.now()) return 'expired';
  return 'active';
}

const sharePaths = (row) => {
  try { const p = JSON.parse(row.paths); return Array.isArray(p) ? p : []; } catch (err) { return []; }
};

const mapShare = (row) => ({
  share_id: row.share_id,
  items: sharePaths(row).map((p) => baseName(p)),
  paths: sharePaths(row),
  password: row.password, // 列表已按本人/班管限定可见范围，提取码可下发用于找回
  creator: row.creator,
  mine: false, // 调用方填充
  expire_at: row.expire_at,
  state: shareState(row),
  created_at: row.created_at,
});

// 分享列表（scope=mine 默认；scope=all 仅班管/超管）
router.get('/shares', async (req, res) => {
  try {
    const scope = req.query.scope === 'all' ? 'all' : 'mine';
    if (scope === 'all' && !canManagePublic(req.user)) {
      return fail(res, 403, 40304, '仅班管/超管可查看全部分享');
    }
    const [rows] = scope === 'all'
      ? await pool.query('SELECT * FROM nd_share ORDER BY id DESC LIMIT 500')
      : await pool.query('SELECT * FROM nd_share WHERE user_id = ? ORDER BY id DESC LIMIT 500', [req.user.id]);
    return ok(res, rows.map((row) => ({ ...mapShare(row), mine: row.user_id === req.user.id })));
  } catch (err) { return relayFail(res, err); }
});

// 创建分享（paths 相对当前空间根，逐项校验存在性；提取码 4 位字母数字）
router.post('/shares', async (req, res) => {
  try {
    ol.ensureConfigured();
    const root = await resolveRoot(req, res, req.body && req.body.space);
    if (!root) return;
    const rawPaths = (req.body && req.body.paths) || [];
    if (!Array.isArray(rawPaths) || !rawPaths.length || rawPaths.length > 20) {
      return fail(res, 400, 40001, '参数错误：paths（1-20 条）');
    }
    const password = String((req.body && req.body.password) || '').trim().toUpperCase();
    if (!/^[A-Z0-9]{4}$/.test(password)) {
      return fail(res, 400, 40001, '提取码须为 4 位字母或数字');
    }
    const days = parseInt((req.body && req.body.expire_days) || '0', 10) || 0;
    if (days < 0 || days > 365) return fail(res, 400, 40001, '参数错误：expire_days');
    const rels = [];
    for (const p of rawPaths) {
      const rel = normRel(p);
      if (rel === null || rel === '/') return fail(res, 400, 40001, '路径不合法');
      rels.push(rel);
    }
    // 逐项校验存在（防止已移动/删除的文件进入分享）
    await Promise.all(rels.map((rel) => ol.fsGet(joinPath(root, rel))));
    const expireAt = days > 0 ? new Date(Date.now() + days * 86400000) : null;
    // share_id 冲突重试（唯一键兜底）
    for (let attempt = 0; attempt < 3; attempt += 1) {
      const shareId = crypto.randomBytes(5).toString('hex');
      try {
        await pool.query(
          `INSERT INTO nd_share (share_id, user_id, creator, base_path, paths, password, expire_at, status)
           VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
          [shareId, req.user.id, req.user.nickname || req.user.username, root,
            JSON.stringify(rels), password, expireAt]
        );
        return ok(res, { share_id: shareId, expire_at: expireAt }, '已创建分享');
      } catch (err) {
        if (err && err.code === 'ER_DUP_ENTRY' && attempt < 2) continue;
        throw err;
      }
    }
    return fail(res, 500, 50000, '分享标识生成失败，请重试');
  } catch (err) { return relayFail(res, err); }
});

// 停用 / 启用 / 删除共用装载与权限校验（本人或班管/超管）
async function loadOwnedShare(req, res) {
  const sid = String(req.params.id || '').trim();
  if (!SHARE_ID_RE.test(sid)) { fail(res, 400, 40001, '参数错误：id'); return null; }
  const [rows] = await pool.query('SELECT * FROM nd_share WHERE share_id = ?', [sid]);
  const row = rows[0];
  if (!row) { fail(res, 404, 40400, '分享不存在'); return null; }
  if (row.user_id !== req.user.id && !canManagePublic(req.user)) {
    fail(res, 403, 40304, '仅创建人或班管/超管可操作'); return null;
  }
  return row;
}

router.post('/shares/:id/disable', async (req, res) => {
  try {
    const row = await loadOwnedShare(req, res);
    if (!row) return;
    await pool.query('UPDATE nd_share SET status = 0 WHERE id = ?', [row.id]);
    return ok(res, null, '已停用');
  } catch (err) { return relayFail(res, err); }
});

router.post('/shares/:id/enable', async (req, res) => {
  try {
    const row = await loadOwnedShare(req, res);
    if (!row) return;
    if (shareState(row) === 'expired') {
      return fail(res, 400, 40030, '分享已过期，请重新创建');
    }
    await pool.query('UPDATE nd_share SET status = 1 WHERE id = ?', [row.id]);
    return ok(res, null, '已重新启用');
  } catch (err) { return relayFail(res, err); }
});

router.delete('/shares/:id', async (req, res) => {
  try {
    const row = await loadOwnedShare(req, res);
    if (!row) return;
    await pool.query('DELETE FROM nd_share WHERE id = ?', [row.id]);
    return ok(res, null, '已删除');
  } catch (err) { return relayFail(res, err); }
});

module.exports = router;
