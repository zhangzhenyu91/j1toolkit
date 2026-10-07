// 「保存到网盘」共享通道：供出工日志/安全日记录/文件传输等模块把服务端侧文件写入网盘
// 落点统一为「我的空间/<来源目录>/<文件名>」（个人空间，不搅公共区；目录自动创建）
// 权限：写网盘须持 netdisk 应用权限（无权限抛 40301）
const config = require('../config');
const { pool } = require('../db');
const ol = require('./openlist');

// 各调用方的归档子目录（个人空间下）；净名单层目录名
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

/**
 * 保存到网盘（我的空间/<dir>/<name>）
 * @param {object} user  req.user
 * @param {object} opts  { dir: 来源目录（DIRS 之一）, name: 文件名, body: Buffer|Readable, size?: 字节数 }
 * @returns {Promise<string>} 网盘内展示路径（我的空间/<dir>/<name>）
 */
async function saveToNetdisk(user, { dir, name, body, size }) {
  ol.ensureConfigured();
  await assertNetdiskAccess(user.id);
  if (!DIRS.has(dir)) {
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
  await ol.ensureDir(`${root}/${dir}`);
  await ol.fsPut(`${root}/${dir}/${clean}`, body, size);
  return `我的空间/${dir}/${clean}`;
}

module.exports = { saveToNetdisk, assertNetdiskAccess };
