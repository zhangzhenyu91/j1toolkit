// 通知推送路由：全部接口需登录；可见性 = 角色命中 targets（角色数组子集）或 本人命中 user_ids（按人投放的用户 id 数组）
// POST /push 为超管推送入口；模块另导出 push() 供其他后端模块系统自动触发（createdBy 缺省 NULL=系统）
// push() 可选 wxUserIds / wxTeamIds / wxAtUserIds：写入站内通知后经 Dify 工作流追加微信外发（见 ./wxpush.js；wxAtUserIds 为群消息 @ 对象）
// 删除：超管 DELETE /:id 为全局删除；其余角色同接口按人删除（sys_notice_del，仅本人不可见）
const express = require('express');
const multer = require('multer');
const auth = require('../middleware/auth');
const requireAdmin = require('../middleware/requireAdmin');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const cos = require('../worklog/cos'); // 与 sgccclockin 同口径跨模块复用 COS 封装
const wxpush = require('./wxpush');

const router = express.Router();
router.use(auth);

// 可推送角色全集（与 sys_user.role 口径一致）
const ROLES = ['admin', 'team_admin', 'user'];

// 系统自动触发接口：写入一条通知并返回新通知 id；createdBy 缺省 NULL 表示系统。
// targets 为角色数组（可空），userIds 为按人投放的用户 id 数组（可空）；两者至少其一非空才有接收人
// wxUserIds / wxTeamIds 可选：同时向这些用户（个人 wxid）与班组（群 wxid）追加微信推送；失败仅记日志
// wxAtUserIds 可选：微信群发时 @ 这些用户（仅群消息生效，正文前自动拼「@昵称 」并传 at 输入）
async function push({ targets = [], userIds = null, title, content, createdBy = null, wxUserIds = null, wxTeamIds = null, wxAtUserIds = null }) {
  const roles = Array.isArray(targets) ? [...new Set(targets)] : [];
  const ids = Array.isArray(userIds)
    ? [...new Set(userIds.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0))]
    : [];
  const [r] = await pool.query(
    'INSERT INTO sys_notice (title, content, targets, user_ids, created_by) VALUES (?, ?, ?, ?, ?)',
    [title, content, JSON.stringify(roles), ids.length ? JSON.stringify(ids) : null, createdBy]
  );
  if ((Array.isArray(wxUserIds) && wxUserIds.length) || (Array.isArray(wxTeamIds) && wxTeamIds.length)) {
    try {
      await wxpush.sendTo({
        userIds: wxUserIds || [],
        teamIds: wxTeamIds || [],
        text: `${title}\n${content}`,
        atUserIds: wxAtUserIds || [],
        user: `notice-${r.insertId}`,
      });
    } catch (err) {
      console.error('[通知] 微信推送失败（站内通知已写入）：', err.message);
    }
  }
  return r.insertId;
}

// targets JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 quiz options 同口径）
function parseTargets(raw) {
  if (!raw) return [];
  const arr = typeof raw === 'string' ? JSON.parse(raw) : raw;
  return Array.isArray(arr) ? arr : [];
}

// GET /api/v1/notice/list 当前用户可见的通知列表（倒序；read 0/1 + 推送人昵称 + 未读总数；本人已删（sys_notice_del）不出现）
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
       LEFT JOIN sys_notice_del d ON d.notice_id = n.id AND d.user_id = ?
       LEFT JOIN sys_user u ON u.id = n.created_by
       WHERE (JSON_CONTAINS(n.targets, JSON_QUOTE(?))
          OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON))) AND d.id IS NULL
       ORDER BY n.created_at DESC, n.id DESC
       LIMIT ${limit}`,
      [req.user.id, req.user.id, req.user.role, String(req.user.id)]
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
    // 未读总数：当前用户可见且未读（单独计数，不受 limit 翻页影响；本人已删不计）
    const [cntRows] = await pool.query(
      `SELECT COUNT(*) AS cnt
       FROM sys_notice n
       LEFT JOIN sys_notice_read r ON r.notice_id = n.id AND r.user_id = ?
       LEFT JOIN sys_notice_del d ON d.notice_id = n.id AND d.user_id = ?
       WHERE (JSON_CONTAINS(n.targets, JSON_QUOTE(?))
          OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON))) AND r.id IS NULL AND d.id IS NULL`,
      [req.user.id, req.user.id, req.user.role, String(req.user.id)]
    );
    return ok(res, { items, unread: cntRows[0].cnt });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/:id/read 标记单条已读（仅当前用户可见且未删的通知，不可见按不存在处理）
router.post('/:id/read', async (req, res, next) => {
  try {
    const noticeId = Number(req.params.id) || 0;
    const [rows] = await pool.query(
      `SELECT id FROM sys_notice n WHERE n.id = ?
       AND (JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))
       AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
      [noticeId, req.user.role, String(req.user.id), req.user.id]
    );
    if (!rows.length) return fail(res, 404, 40403, '通知不存在');
    await pool.query('INSERT IGNORE INTO sys_notice_read (notice_id, user_id) VALUES (?, ?)', [noticeId, req.user.id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/read-all 当前用户可见通知全部标记已读（本人已删跳过）
router.post('/read-all', async (req, res, next) => {
  try {
    await pool.query(
      `INSERT IGNORE INTO sys_notice_read (notice_id, user_id)
       SELECT n.id, ? FROM sys_notice n
       WHERE (JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))
         AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
      [req.user.id, req.user.role, String(req.user.id), req.user.id]
    );
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/del-read 一键删除已读：把本人已读且可见的通知全部按人删除（写 sys_notice_del，仅本人不可见；
// 超管同样仅按人删除——不做全局删除，避免清空其他成员未读通知）
router.post('/del-read', async (req, res, next) => {
  try {
    const [r] = await pool.query(
      `INSERT IGNORE INTO sys_notice_del (notice_id, user_id)
       SELECT n.id, ? FROM sys_notice n
       JOIN sys_notice_read rd ON rd.notice_id = n.id AND rd.user_id = ?
       WHERE (JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))
         AND NOT EXISTS (SELECT 1 FROM sys_notice_del d WHERE d.notice_id = n.id AND d.user_id = ?)`,
      [req.user.id, req.user.id, req.user.role, String(req.user.id), req.user.id]
    );
    return ok(res, { deleted: r.affectedRows }, `已删除 ${r.affectedRows} 条已读通知`);
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/notice/push 超管推送通知（targets 为 ROLES 非空子集；content 支持 Markdown 原文，图片用 ![描述](url) 引用）
// 微信外发仅系统自动触发的通知使用（后端模块经 push() 传 wxUserIds/wxTeamIds），手动推送不发微信
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

// POST /api/v1/notice/upload-image 超管上传通知配图（multipart 字段 file，仅 image/* ≤5MB），
// 存 COS notice/ 前缀，返回 { url } 供 Markdown 以 ![描述](url) 引用；COS 未配置时报错（ensureConfigured expose）
const IMG_MIME_EXT = { 'image/jpeg': '.jpg', 'image/png': '.png', 'image/gif': '.gif', 'image/webp': '.webp' };
// 类型不在 fileFilter 拦截（拒绝时 req.file 为空会误报「请选择图片」），统一在处理器按 mimetype 校验报错
const noticeImageUpload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 5 * 1024 * 1024, files: 1 },
});
router.post(
  '/upload-image',
  requireAdmin,
  (req, res, next) => {
    noticeImageUpload.single('file')(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40030, '图片大小应在 5MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      if (!req.file || !req.file.buffer || !req.file.buffer.length) {
        return fail(res, 400, 40030, '请选择要上传的图片');
      }
      const ext = IMG_MIME_EXT[req.file.mimetype];
      if (!ext) return fail(res, 400, 40030, '仅支持 JPG/PNG/GIF/WebP 图片');
      const now = new Date();
      const ym = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}`;
      const rand = Math.random().toString(36).slice(2, 8);
      const key = `notice/${ym}/${Date.now()}-${rand}${ext}`;
      await cos.putBuffer(key, req.file.buffer, req.file.mimetype);
      return ok(res, { url: cos.publicUrl(key) });
    } catch (err) {
      return next(err);
    }
  }
);

// DELETE /api/v1/notice/:id 删除通知：超管全局删除（连带清空已读与按人删除记录）；
// 其余角色仅对本人隐藏（写 sys_notice_del，INSERT IGNORE 幂等；本人不可见的通知按不存在处理，40403）
router.delete('/:id', async (req, res, next) => {
  try {
    const noticeId = Number(req.params.id) || 0;
    if (req.user.role === 'admin') {
      const [r] = await pool.query('DELETE FROM sys_notice WHERE id = ?', [noticeId]);
      if (!r.affectedRows) return fail(res, 404, 40403, '通知不存在');
      await pool.query('DELETE FROM sys_notice_read WHERE notice_id = ?', [noticeId]);
      await pool.query('DELETE FROM sys_notice_del WHERE notice_id = ?', [noticeId]);
      return ok(res, null, '删除成功');
    }
    const [rows] = await pool.query(
      `SELECT id FROM sys_notice n WHERE n.id = ?
       AND (JSON_CONTAINS(n.targets, JSON_QUOTE(?)) OR JSON_CONTAINS(n.user_ids, CAST(? AS JSON)))`,
      [noticeId, req.user.role, String(req.user.id)]
    );
    if (!rows.length) return fail(res, 404, 40403, '通知不存在');
    await pool.query('INSERT IGNORE INTO sys_notice_del (notice_id, user_id) VALUES (?, ?)', [noticeId, req.user.id]);
    return ok(res, null, '删除成功');
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
// 供其他后端模块系统自动触发通知（如 require('./notice').push({ targets, userIds, title, content })，createdBy 缺省为系统）
module.exports.push = push;
