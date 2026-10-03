// 管理接口：员工管理 + 权限管理 + 班组管理（均需登录 + admin 超级管理员角色）
const express = require('express');
const bcrypt = require('bcryptjs');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const auth = require('../middleware/auth');
const requireAdmin = require('../middleware/requireAdmin');
const teamUtil = require('../utils/team');

const router = express.Router();
router.use(auth, requireAdmin);

const ROLES = ['admin', 'team_admin', 'user'];

// 员工变更后同步出工成员字典
function syncWorklogMember(userId) {
  require('../worklog/member-sync')
    .syncMemberForUser(userId)
    .catch((err) => console.error(`[出工日志] 成员同步失败（用户 ${userId}）：`, err.message));
}

// 校验班组 id 合法且启用；返回班组行或 null
async function validTeam(teamId) {
  const t = await teamUtil.getTeamById(teamId);
  return t && t.status === 1 ? t : null;
}

// GET /api/v1/admin/users 员工列表（keyword 模糊搜索账号/昵称；team_id 按班组筛选，0=未分组）
router.get('/users', async (req, res, next) => {
  try {
    const keyword = (req.query.keyword || '').trim();
    const wheres = [];
    const params = [];
    if (keyword) {
      wheres.push('(u.username LIKE ? OR u.nickname LIKE ?)');
      params.push(`%${keyword}%`, `%${keyword}%`);
    }
    if (req.query.team_id !== undefined && req.query.team_id !== '') {
      if (String(req.query.team_id) === '0') {
        wheres.push('u.team_id IS NULL');
      } else {
        wheres.push('u.team_id = ?');
        params.push(Number(req.query.team_id) || 0);
      }
    }
    const where = wheres.length ? `WHERE ${wheres.join(' AND ')}` : '';
    const [rows] = await pool.query(
      `SELECT u.id, u.username, u.nickname, u.wxid, u.team_id, t.name AS team, u.role, u.status, u.created_at
       FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id ${where} ORDER BY u.id LIMIT 500`,
      params
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/admin/users 新建员工账号（team_id 归属班组；role 默认 user）
router.post('/users', async (req, res, next) => {
  try {
    const { username, password, nickname } = req.body || {};
    const wxid = String((req.body && req.body.wxid) || '').trim();
    const teamId = req.body && req.body.team_id !== undefined && req.body.team_id !== null
      ? Number(req.body.team_id) : null;
    const role = (req.body && req.body.role) || 'user';
    if (!username || !/^[a-zA-Z0-9_]{2,64}$/.test(username)) {
      return fail(res, 400, 40010, '账号需为 2-64 位字母、数字或下划线');
    }
    if (!password || password.length < 6) return fail(res, 400, 40011, '密码至少 6 位');
    if (!ROLES.includes(role)) return fail(res, 400, 40013, '非法角色');
    if (role === 'team_admin' && !teamId) return fail(res, 400, 40026, '班组管理员必须有所属班组');
    if (teamId && !(await validTeam(teamId))) return fail(res, 400, 40015, '班组不存在或已停用');
    if (wxid.length > 128) return fail(res, 400, 40018, 'wxid 不能超过 128 字');

    const [dup] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [username]);
    if (dup.length) return fail(res, 409, 40901, '账号已存在');

    const hash = await bcrypt.hash(password, 10);
    const [r] = await pool.query(
      'INSERT INTO sys_user (username, password_hash, nickname, wxid, team_id, role) VALUES (?, ?, ?, ?, ?, ?)',
      [username, hash, nickname || '', wxid, teamId, role]
    );
    syncWorklogMember(r.insertId);
    return ok(res, { id: r.insertId }, '已创建');
  } catch (err) {
    return next(err);
  }
});

// PUT /api/v1/admin/users/:id 修改员工（昵称/wxid/班组/状态/角色/重置密码）
router.put('/users/:id', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    const { nickname, status, password } = req.body || {};
    const hasWxid = Object.prototype.hasOwnProperty.call(req.body || {}, 'wxid');
    const wxid = hasWxid ? String(req.body.wxid || '').trim() : '';
    const hasTeam = Object.prototype.hasOwnProperty.call(req.body || {}, 'team_id');
    const teamId = hasTeam && req.body.team_id !== null && req.body.team_id !== ''
      ? Number(req.body.team_id) : null;
    const role = req.body && req.body.role;

    // 自我保护：不能禁用或降级（改为非超管）自己的账号
    // 禁用判定与下方落库口径（status ? 1 : 0）一致：status 传了且为假值即视为禁用
    if (targetId === req.user.id && ((status !== undefined && !status) || (role !== undefined && role !== 'admin'))) {
      return fail(res, 400, 40012, '不能禁用或降级自己的账号');
    }

    const fields = [];
    const params = [];
    if (nickname !== undefined) { fields.push('nickname = ?'); params.push(nickname); }
    if (hasWxid) {
      if (wxid.length > 128) return fail(res, 400, 40018, 'wxid 不能超过 128 字');
      fields.push('wxid = ?'); params.push(wxid);
    }
    if (hasTeam) {
      if (teamId && !(await validTeam(teamId))) return fail(res, 400, 40015, '班组不存在或已停用');
      fields.push('team_id = ?'); params.push(teamId);
    }
    if (status !== undefined) { fields.push('status = ?'); params.push(status ? 1 : 0); }
    if (role !== undefined) {
      if (!ROLES.includes(role)) return fail(res, 400, 40013, '非法角色');
      fields.push('role = ?'); params.push(role);
    }
    // 班组管理员必须有班组（以更新后的 team_id 为准）
    if (role === 'team_admin' || (role === undefined && hasTeam)) {
      const [cur] = await pool.query('SELECT role, team_id FROM sys_user WHERE id = ?', [targetId]);
      if (!cur.length) return fail(res, 404, 40401, '用户不存在');
      const finalRole = role !== undefined ? role : cur[0].role;
      const finalTeam = hasTeam ? teamId : cur[0].team_id;
      if (finalRole === 'team_admin' && !finalTeam) {
        return fail(res, 400, 40026, '班组管理员必须有所属班组');
      }
    }
    if (password !== undefined) {
      if (password.length < 6) return fail(res, 400, 40011, '密码至少 6 位');
      fields.push('password_hash = ?');
      params.push(await bcrypt.hash(password, 10));
    }
    if (!fields.length) return fail(res, 400, 40014, '没有需要修改的内容');

    params.push(targetId);
    const [r] = await pool.query(`UPDATE sys_user SET ${fields.join(', ')} WHERE id = ?`, params);
    if (!r.affectedRows) return fail(res, 404, 40401, '用户不存在');
    if (nickname !== undefined || hasTeam || status !== undefined) syncWorklogMember(targetId);
    return ok(res, null, '已保存');
  } catch (err) {
    return next(err);
  }
});

// ===== 班组管理 =====

// GET /api/v1/admin/teams 班组列表（含成员数；含停用班组）
router.get('/teams', async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      `SELECT t.id, t.name, t.wxid, t.kvm_group_name, t.sort, t.status, t.created_at,
              (SELECT COUNT(*) FROM sys_user u WHERE u.team_id = t.id) AS user_count
       FROM sys_team t ORDER BY t.sort, t.id`
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/admin/teams 新增班组（wxid 可空：班组微信群 wxid，消息推送用）
router.post('/teams', async (req, res, next) => {
  try {
    const name = String((req.body && req.body.name) || '').trim().slice(0, 64);
    if (!name) return fail(res, 400, 40017, '请输入班组名称');
    const wxid = String((req.body && req.body.wxid) || '').trim();
    if (wxid.length > 128) return fail(res, 400, 40018, 'wxid 不能超过 128 字');
    const [maxRows] = await pool.query('SELECT COALESCE(MAX(sort), 0) AS maxSort FROM sys_team');
    try {
      const [r] = await pool.query(
        'INSERT INTO sys_team (name, wxid, sort) VALUES (?, ?, ?)',
        [name, wxid, maxRows[0].maxSort + 1]
      );
      return ok(res, { id: r.insertId }, '已创建');
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40902, `班组「${name}」已存在`);
      throw err;
    }
  } catch (err) {
    return next(err);
  }
});

// PUT /api/v1/admin/teams/:id 修改班组（改名/微信群 wxid/排序/停启用）；改名级联安全日记录与 docs 子目录
router.put('/teams/:id', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    const [exist] = await pool.query('SELECT id, name FROM sys_team WHERE id = ?', [targetId]);
    if (!exist.length) return fail(res, 404, 40405, '班组不存在');
    const oldName = exist[0].name;
    const { name, sort, status, wxid } = req.body || {};

    const fields = [];
    const params = [];
    if (name !== undefined) {
      const trimmed = String(name).trim().slice(0, 64);
      if (!trimmed) return fail(res, 400, 40017, '请输入班组名称');
      fields.push('name = ?'); params.push(trimmed);
    }
    if (sort !== undefined) { fields.push('sort = ?'); params.push(Number(sort) || 0); }
    if (status !== undefined) { fields.push('status = ?'); params.push(status ? 1 : 0); }
    // wxid 排在最后：上方 ER_DUP_ENTRY 报错文案假定 params[0] 为 name
    if (wxid !== undefined) {
      const trimmedWxid = String(wxid || '').trim();
      if (trimmedWxid.length > 128) return fail(res, 400, 40018, 'wxid 不能超过 128 字');
      fields.push('wxid = ?'); params.push(trimmedWxid);
    }
    if (!fields.length) return fail(res, 400, 40014, '没有需要修改的内容');

    try {
      await pool.query(`UPDATE sys_team SET ${fields.join(', ')} WHERE id = ?`, [...params, targetId]);
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40902, `班组「${params[0]}」已存在`);
      throw err;
    }

    // 改名级联：安全日记录 team 字段 + docs 子目录更名（worklog / 员工按 team_id 关联无感）
    const newName = name !== undefined ? String(name).trim() : oldName;
    if (newName !== oldName) {
      try {
        await require('../safeday/migrate').renameTeamFolder(oldName, newName);
      } catch (err) {
        console.error('[安全日] 班组改名级联失败（请手工调整 docs 目录）：', err.message);
      }
    }
    return ok(res, null, '已保存');
  } catch (err) {
    return next(err);
  }
});

// DELETE /api/v1/admin/teams/:id 删除班组（有任何数据引用则拒绝，提示改为停用）
router.delete('/teams/:id', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    const [exist] = await pool.query('SELECT id, name FROM sys_team WHERE id = ?', [targetId]);
    if (!exist.length) return fail(res, 404, 40405, '班组不存在');
    const teamName = exist[0].name;

    const [userCnt] = await pool.query('SELECT COUNT(*) AS cnt FROM sys_user WHERE team_id = ?', [targetId]);
    if (userCnt[0].cnt) return fail(res, 409, 40903, `该班组下仍有 ${userCnt[0].cnt} 名员工，请改为停用`);
    const tables = ['worklog_entry', 'worklog_vehicle', 'worklog_destination', 'worklog_member', 'worklog_tower'];
    for (const table of tables) {
      const [rows] = await pool.query(`SELECT COUNT(*) AS cnt FROM ${table} WHERE team_id = ?`, [targetId]);
      if (rows[0].cnt) return fail(res, 409, 40903, '该班组已有出工日志数据，请改为停用');
    }
    // 派车单同步开关表（team_id 为主键）只是开关配置行、非业务数据：随班组删除级联清除
    await pool.query('DELETE FROM worklog_dispatch_sync_team WHERE team_id = ?', [targetId]);
    const [quizRows] = await pool.query('SELECT COUNT(*) AS cnt FROM quiz_bank WHERE team_id = ?', [targetId]);
    if (quizRows[0].cnt) return fail(res, 409, 40903, '该班组已有题库数据，请改为停用');
    const sgccTables = ['worklog_sgcc_account', 'worklog_clockin', 'worklog_fee', 'worklog_sync_log'];
    for (const table of sgccTables) {
      const [rows] = await pool.query(`SELECT COUNT(*) AS cnt FROM ${table} WHERE team_id = ?`, [targetId]);
      if (rows[0].cnt) return fail(res, 409, 40903, '该班组已有商旅打卡数据，请改为停用');
    }
    const store = require('../safeday/store');
    if (store.list().some((r) => r.team === teamName)) {
      return fail(res, 409, 40903, '该班组已有安全日活动记录，请改为停用');
    }
    await pool.query('DELETE FROM sys_team WHERE id = ?', [targetId]);
    return ok(res, null, '已删除');
  } catch (err) {
    return next(err);
  }
});

// GET /api/v1/admin/apps 全部应用列表
router.get('/apps', async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      'SELECT id, app_key, name, icon, path, terminal, sort, status FROM sys_app ORDER BY sort, id'
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// GET /api/v1/admin/users/:id/apps 某员工的已授权应用
router.get('/users/:id/apps', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    const [u] = await pool.query('SELECT id FROM sys_user WHERE id = ?', [targetId]);
    if (!u.length) return fail(res, 404, 40401, '用户不存在');
    const [rows] = await pool.query('SELECT app_id FROM sys_user_app WHERE user_id = ?', [targetId]);
    return ok(res, { app_ids: rows.map((r) => r.app_id) });
  } catch (err) {
    return next(err);
  }
});

// PUT /api/v1/admin/users/:id/apps 设置员工授权（全量替换，事务）
router.put('/users/:id/apps', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    let ids = Array.isArray(req.body && req.body.app_ids) ? req.body.app_ids : null;
    if (!ids) return fail(res, 400, 40025, 'app_ids 需为数组');
    ids = [...new Set(ids.map(Number).filter((n) => Number.isInteger(n) && n > 0))];

    const [u] = await pool.query('SELECT id FROM sys_user WHERE id = ?', [targetId]);
    if (!u.length) return fail(res, 404, 40401, '用户不存在');

    // 过滤掉不存在的应用 id，防止脏数据
    if (ids.length) {
      const [valid] = await pool.query('SELECT id FROM sys_app WHERE id IN (?)', [ids]);
      ids = valid.map((v) => v.id);
    }

    const conn = await pool.getConnection();
    try {
      await conn.beginTransaction();
      await conn.query('DELETE FROM sys_user_app WHERE user_id = ?', [targetId]);
      if (ids.length) {
        await conn.query('INSERT IGNORE INTO sys_user_app (user_id, app_id) VALUES ?', [
          ids.map((id) => [targetId, id]),
        ]);
      }
      await conn.commit();
    } catch (err) {
      await conn.rollback().catch(() => {});
      throw err;
    } finally {
      conn.release();
    }
    return ok(res, { app_ids: ids }, '已保存');
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
