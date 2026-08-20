// 通知推送路由：全部接口需登录；可见性 = 角色命中 targets（角色数组子集）或 本人命中 user_ids（按人投放的用户 id 数组）
// POST /push 为超管推送入口；模块另导出 push() 供其他后端模块系统自动触发（createdBy 缺省 NULL=系统）
const express = require('express');
const auth = require('../middleware/auth');
const requireAdmin = require('../middleware/requireAdmin');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');

const router = express.Router();
router.use(auth);

// 可推送角色全集（与 sys_user.role 口径一致）
const ROLES = ['admin', 'team_admin', 'user'];

// 系统自动触发接口：写入一条通知并返回新通知 id；createdBy 缺省 NULL 表示系统。
// targets 为角色数组（可空），userIds 为按人投放的用户 id 数组（可空）；两者至少其一非空才有接收人
async function push({ targets = [], userIds = null, title, content, createdBy = null }) {
  const roles = Array.isArray(targets) ? [...new Set(targets)] : [];
  const ids = Array.isArray(userIds)
    ? [...new Set(userIds.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0))]
    : [];
  const [r] = await pool.query(
    'INSERT INTO sys_notice (title, content, targets, user_ids, created_by) VALUES (?, ?, ?, ?, ?)',
    [title, content, JSON.stringify(roles), ids.length ? JSON.stringify(ids) : null, createdBy]
  );
  return r.insertId;
}

// targets JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 quiz options 同口径）
function parseTargets(raw) {
  if (!raw) return [];
  const arr = typeof raw === 'string' ? JSON.parse(raw) : raw;
  return Array.isArray(arr) ? arr : [];
}

// GET /api/v1/notice/list 当前用户可见的通知列表（倒序；read 0/1 + 推送人昵称 + 未读总数）
router.get('/list', async (req, res, next) => {
  try {
    const limit = Math.min(Math.max(parseInt(req.query.limit, 10) || 50, 1), 200);
    const [rows] = await pool.query(
      `SELECT n.id, n.title, n.content, n.targets,
              DATE_FORMAT(n.created_at, '%Y-%m-%d %H:%i:%s') AS created_at,
              u.nickname AS created_by_name,
              IF(r.id IS NULL, 0, 1) AS \`read\`
       FROM sys_notice n
       LEFT JOIN sys_notice_read r ON r.notice_id = n.id AND r.user_id = ?
       LEFT JOIN sys_user u ON u.id = n.created_by
       WHERE JSON_CONTAINS(n.targets, JSON_QUOTE(?))
          OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON))
       ORDER BY n.created_at DESC, n.id DESC
       LIMIT ${limit}`,
      [req.user.id, req.user.role, String(req.user.id)]
    );
    const items = rows.map((n) => ({
      id: n.id,
      title: n.title,
      content: n.content,
      targets: parseTargets(n.targets),
      createdByName: n.created_by_name || '系统',
      createdAt: n.created_at,
      read: n.read ? 1 : 0,
    }));
    // 未读总数：当前用户可见且未读（单独计数，不受 limit 翻页影响）
    const [cntRows] = await pool.query(
      `SELECT COUNT(*) AS cnt
       FROM sys_notice n
       LEFT JOIN sys_notice_read r ON r.notice_id = n.id AND r.user_id = ?
       WHERE (JSON_CONTAINS(n.targets, JSON_QUOTE(?))
          OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON))) AND r.id IS NULL`,
      [req.user.id, req.user.role, String(req.user.id)]
    );
    return ok(res, { items, unread: cntRows[0].cnt });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/:id/read 标记单条已读（仅当前用户可见的通知，不可见按不存在处理）
router.post('/:id/read', async (req, res, next) => {
  try {
    const noticeId = Number(req.params.id) || 0;
    const [rows] = await pool.query(
      `SELECT id FROM sys_notice n WHERE n.id = ?
       AND (JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))`,
      [noticeId, req.user.role, String(req.user.id)]
    );
    if (!rows.length) return fail(res, 404, 40403, '通知不存在');
    await pool.query('INSERT IGNORE INTO sys_notice_read (notice_id, user_id) VALUES (?, ?)', [noticeId, req.user.id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/read-all 当前用户可见通知全部标记已读
router.post('/read-all', async (req, res, next) => {
  try {
    await pool.query(
      `INSERT IGNORE INTO sys_notice_read (notice_id, user_id)
       SELECT n.id, ? FROM sys_notice n
       WHERE JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON))`,
      [req.user.id, req.user.role, String(req.user.id)]
    );
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/push 超管推送通知（targets 为 ROLES 非空子集）
router.post('/push', requireAdmin, async (req, res, next) => {
  try {
    const title = String((req.body && req.body.title) || '').trim();
    const content = String((req.body && req.body.content) || '').trim();
    const targets = req.body && req.body.targets;
    if (!title || !content) return fail(res, 400, 40030, '标题/内容不能为空');
    if (title.length > 128) return fail(res, 400, 40030, '标题不能超过 128 字');
    const valid = Array.isArray(targets) && targets.length > 0
      && targets.every((t) => typeof t === 'string' && ROLES.includes(t));
    if (!valid) return fail(res, 400, 40031, '推送对象不合法');
    const id = await push({ targets: [...new Set(targets)], title, content, createdBy: req.user.id });
    return ok(res, { id }, '推送成功');
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
// 供其他后端模块系统自动触发通知（如 require('./notice').push({ targets, userIds, title, content })，createdBy 缺省为系统）
module.exports.push = push;
