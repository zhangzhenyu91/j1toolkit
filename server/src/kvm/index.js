// KVM 远程管理子模块：设备列表实时代理自 GLKVM Cloud 平台（员工账号代登），
// 终端/远程控制经 /jump 签发带 sid 的跳转地址（平台 jump.html 种 Cookie），实现仅能通过壹匣登录；
// 文件分享转发点（files/push/download）经平台 /web/ 链路直达设备 fileshare 服务（见 开发指南.md 第十二节）
// 挂载：KVM_ENABLED=true 时由入口挂载（/api/v1/kvm）；GLKVM_* 配置见 .env.example
const express = require('express');
const axios = require('axios');
const FormData = require('form-data');
const multer = require('multer');
const config = require('../config');
const { pool } = require('../db');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const glkvm = require('./glkvm');

const router = express.Router();

// 推送上传内存接收（参考 safeday 同型用法），单文件上限 2GB、单次最多 20 个
const upload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 2 * 1024 * 1024 * 1024, files: 20 },
});

// 本模块全部接口需登录；权限按路由细分：
// 设备列表与文件转发 = kvm（网页端）或 file-transfer（小程序端）任一；带态跳转 = 仅 kvm
router.use(auth);

// 任一应用权限即放行（requireApp 的多 key 版，SQL 同 middleware/requireApp.js）
function requireAnyApp(appKeys) {
  return async (req, res, next) => {
    try {
      const marks = appKeys.map(() => '?').join(',');
      const [rows] = await pool.query(
        `SELECT a.id FROM sys_user_app ua
         JOIN sys_app a ON a.id = ua.app_id
         WHERE ua.user_id = ? AND a.app_key IN (${marks}) AND a.status = 1 LIMIT 1`,
        [req.user.id, ...appKeys]
      );
      if (!rows.length) {
        return fail(res, 403, 40301, '暂无该应用的使用权限，请联系管理员开通');
      }
      return next();
    } catch (err) {
      return next(err);
    }
  };
}

const KVM_OR_FT = ['kvm', 'file-transfer'];

// 设备列表（透传 q/status/groupId 过滤；以当前员工账号代登平台，可见范围随其平台权限）
router.get('/devices', requireAnyApp(KVM_OR_FT), async (req, res) => {
  try {
    const items = await glkvm.listDevices(req.query, req.user.username);
    return ok(res, { items });
  } catch (err) {
    // glkvm.js 抛出的均为组装好的对用户可读文案
    return fail(res, 502, 50201, err && err.message ? err.message : 'GLKVM 平台访问失败');
  }
});

// 跳转目标白名单（与平台前端路由同构）：终端为 SPA hash 路由，远程控制为平台 /web/ 代理路由
const JUMP_TARGETS = {
  term: (ddns) => `/#/rtty/${ddns}`,
  desk: (ddns) => `/web/${ddns}/https/${encodeURIComponent('127.0.0.1:443/')}`,
};

// 签发带态跳转地址：以当前员工账号取平台会话 sid，拼平台 jump.html 地址（sid 走 hash，不经服务器日志）
router.post('/jump', requireApp('kvm'), async (req, res) => {
  const body = req.body || {};
  const build = JUMP_TARGETS[String(body.to || '')];
  const ddns = String(body.ddns || '');
  if (!build) return fail(res, 400, 40001, '参数错误：to');
  // ddns 仅允许字母数字与 . _ -（禁止路径字符，防拼接注入）
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(ddns)) return fail(res, 400, 40001, '参数错误：ddns');
  try {
    const sid = await glkvm.getSessionToken(req.user.username);
    const url = `${config.kvm.url}/jump.html#sid=${encodeURIComponent(sid)}` +
      `&to=${encodeURIComponent(build(ddns))}`;
    return ok(res, { url });
  } catch (err) {
    return fail(res, 502, 50201, err && err.message ? err.message : 'GLKVM 平台访问失败');
  }
});

/* ===== 文件分享转发点：经平台 /web/ 链路直达设备 fileshare 服务 =====
   链路口径：壹匣后端代登取平台 sid → GET /web/{ddns}/https/127.0.0.1:443/ 换 rttysid
   → 带 rtty-http-sid Cookie 调设备子域名 /api/fileshare/*（平台无需反代设备 8901，
   8901→443 的桥接在设备 nginx extras 里，见 开发指南.md 第十二节） */

// 转发调用统一出口：错误文案对用户可读
function relayFail(res, err) {
  const upstream = err && err.response && err.response.data;
  const msg = (upstream && upstream.error) || (err && err.message) || '设备访问失败';
  const status = (err && err.status) || (err && err.response && err.response.status) || 502;
  return fail(res, status === 401 ? 502 : status, 50201, msg);
}

// 盘内文件列表（设备 /list：共享中则先断开）
router.get('/devices/:id/files', requireAnyApp(KVM_OR_FT), async (req, res) => {
  try {
    const ps = await glkvm.getProxySession(req.params.id, req.user.username);
    const r = await axios.get(`${ps.origin}/api/fileshare/list`, {
      headers: { Cookie: ps.cookie }, timeout: 60000,
    });
    return ok(res, r.data);
  } catch (err) {
    return relayFail(res, err);
  }
});

// 推送文件到盘（设备 /push：断开 → 写入 → 保持非共享；挂载由 /mount 单独触发）
router.post('/devices/:id/push', requireAnyApp(KVM_OR_FT), upload.array('files', 20), async (req, res) => {
  const files = req.files || [];
  if (!files.length) return fail(res, 400, 40001, '请至少上传一个文件（字段名 files）');
  try {
    const ps = await glkvm.getProxySession(req.params.id, req.user.username);
    const fd = new FormData();
    for (const f of files) {
      // 文件名口径：小程序 wx.uploadFile 发的是临时路径名，真实文件名在表单字段 filename
      // （单文件场景）；其余走 multer originalname（latin1 → UTF-8，与 safeday 同口径）
      const name = (files.length === 1 && req.body && req.body.filename)
        ? String(req.body.filename).slice(0, 255)
        : Buffer.from(f.originalname, 'latin1').toString('utf8');
      fd.append('files', f.buffer, { filename: name });
    }
    const r = await axios.post(`${ps.origin}/api/fileshare/push`, fd, {
      headers: { ...fd.getHeaders(), Cookie: ps.cookie },
      maxBodyLength: Infinity,
      maxContentLength: Infinity,
      timeout: 0, // 大文件经两跳转发耗时不可预估，不限超时
    });
    return ok(res, r.data);
  } catch (err) {
    return relayFail(res, err);
  }
});

// 下载盘内文件（设备 /download/<name>，流式回传）
router.get('/devices/:id/download', requireAnyApp(KVM_OR_FT), async (req, res) => {
  const name = String(req.query.name || '');
  if (!name) return fail(res, 400, 40001, '参数错误：name');
  try {
    const ps = await glkvm.getProxySession(req.params.id, req.user.username);
    const r = await axios.get(
      `${ps.origin}/api/fileshare/download/${encodeURIComponent(name)}`,
      { headers: { Cookie: ps.cookie }, responseType: 'stream', timeout: 0 }
    );
    res.setHeader('Content-Disposition',
      r.headers['content-disposition'] || `attachment; filename*=UTF-8''${encodeURIComponent(name)}`);
    if (r.headers['content-length']) res.setHeader('Content-Length', r.headers['content-length']);
    return r.data.pipe(res);
  } catch (err) {
    return relayFail(res, err);
  }
});

// 挂载（共享）到被控机（设备 /mount；推送全部完成后再调一次）
router.post('/devices/:id/mount', requireAnyApp(KVM_OR_FT), async (req, res) => {
  try {
    const ps = await glkvm.getProxySession(req.params.id, req.user.username);
    const r = await axios.post(`${ps.origin}/api/fileshare/mount`, null, {
      headers: { Cookie: ps.cookie }, timeout: 60000,
    });
    return ok(res, r.data);
  } catch (err) {
    return relayFail(res, err);
  }
});

// 删除盘内文件（设备 /delete：JSON {"names": [...]}，共享中自动断开）
router.post('/devices/:id/delete', requireAnyApp(KVM_OR_FT), async (req, res) => {
  const names = req.body && req.body.names;
  if (!Array.isArray(names) || !names.length) {
    return fail(res, 400, 40001, '参数错误：names（文件名数组）');
  }
  try {
    const ps = await glkvm.getProxySession(req.params.id, req.user.username);
    const r = await axios.post(`${ps.origin}/api/fileshare/delete`,
      { names: names.map((n) => String(n).slice(0, 255)) },
      { headers: { Cookie: ps.cookie }, timeout: 60000 });
    return ok(res, r.data);
  } catch (err) {
    return relayFail(res, err);
  }
});

// multer 错误（超限等）统一返回可读文案（与 safeday 同口径）
// eslint-disable-next-line no-unused-vars
router.use((err, req, res, next) => {
  if (err instanceof multer.MulterError) {
    let message = `文件上传失败：${err.message}`;
    if (err.code === 'LIMIT_FILE_SIZE') message = '文件大小超出限制（单文件最大 2GB）';
    else if (err.code === 'LIMIT_FILE_COUNT') message = '文件数量超出限制（最多 20 个）';
    return fail(res, 400, 40001, message);
  }
  return next(err);
});

module.exports = router;