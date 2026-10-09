// 「保存到网盘」共享通道：供出工日志/安全日记录/文件传输等模块把服务端侧文件写入网盘
// 落点为「我的空间/<dir>/<文件名>」（个人空间，不搅公共区；目录自动创建）：
// dir 为「我的空间」内相对路径（可多级，如 出工日志/2026-10），缺省由各调用方传固定目录
// 权限：写网盘须持 netdisk 应用权限（无权限抛 40301）
const config = require('../config');
const { pool } = require('../db');
const ol = require('./openlist');

// 各调用方的缺省归档目录（个人空间下；仅作默认值文档，调用方也可让用户自选多级相对路径）
const DIRS = new Set(['出工日志', '安全日记录', '文件传输']);

async function assertNetdiskAccess(userId) {
  const [rows] = await pool.query(
    `SELECT a.id FROM sys_user_app ua
     JOIN sys_app a ON a.id = ua.app_id
     WHERE ua.user_id = ? AND a.app_key = 'netdisk' AND a.status = 1`,
    [userId]
  );
  if (!rows.length) {
    const err = new Error('未开通团队网盘应用，请联系管理员开通后再保存');
    err.expose = true;
    err.status = 403;
    err.code = 40301;
    throw err;
  }
}

// 文件名净化：去路径分隔符与非法字符（与 worklog zip 同口径防路径穿越）
function sanitizeName(name) {
  const n = String(name || '').replace(/[/\\:*?"<>|\r\n]/g, '_').trim().slice(0, 200);
  if (!n || n === '.' || n === '..') return null;
  return n;
}

// 「我的空间」内相对目录净化：去首尾空白与斜杠后按 / 分段，每段走 sanitizeName 同款净化，
// 任一段为空 / . / .. 判非法；深度 ≤6、总长 ≤200
// 合法返回净化后相对路径（不含首尾斜杠；根目录为空串 ''），非法返回 null
function sanitizeRelDir(dir) {
  const s = String(dir == null ? '' : dir).trim().replace(/^\/+|\/+$/g, '');
  if (!s) return '';
  const segs = s.split('/');
  if (segs.length > 6) return null;
  const clean = [];
  for (const seg of segs) {
    const c = sanitizeName(seg);
    if (!c) return null;
    clean.push(c);
  }
  const joined = clean.join('/');
  return joined.length > 200 ? null : joined;
}

/**
 * 保存到网盘（我的空间/<dir>/<name>；dir 为我的空间内相对路径，'' 表示根目录）
 * @param {object} user  req.user
 * @param {object} opts  { dir: 目标相对路径（多级，如 出工日志/2026-10；各调用方缺省见 DIRS）,
 *                         name: 文件名, body: Buffer|Readable, size?: 字节数 }
 * @returns {Promise<string>} 网盘内展示路径（我的空间/<dir>/<name>）
 */
async function saveToNetdisk(user, { dir, name, body, size }) {
  ol.ensureConfigured();
  await assertNetdiskAccess(user.id);
  const rel = sanitizeRelDir(dir);
  if (rel === null) {
    const err = new Error('参数错误：dir');
    err.expose = true;
    err.status = 400;
    err.code = 40001;
    throw err;
  }
  const clean = sanitizeName(name);
  if (!clean) {
    const err = new Error('文件名不合法');
    err.expose = true;
    err.status = 400;
    err.code = 40001;
    throw err;
  }
  const root = `${config.netdisk.personalRoot}/${user.username}`;
  const target = rel ? `${root}/${rel}` : root;
  await ol.ensureDir(target); // ensureDir 逐级创建，支持多级
  await ol.fsPut(`${target}/${clean}`, body, size);
  return rel ? `我的空间/${rel}/${clean}` : `我的空间/${clean}`;
}

module.exports = { saveToNetdisk, assertNetdiskAccess, sanitizeRelDir, DIRS };
