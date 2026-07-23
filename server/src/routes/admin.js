// 管理接口：员工管理 + 权限管理（均需登录 + admin 角色）
const express = require('express');
const bcrypt = require('bcryptjs');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const auth = require('../middleware/auth');
const requireAdmin = require('../middleware/requireAdmin');

const router = express.Router();
router.use(auth, requireAdmin);

// GET /api/v1/admin/users 员工列表（keyword 模糊搜索账号/昵称）
router.get('/users', async (req, res, next) => {
  try {
    const keyword = (req.query.keyword || '').trim();
    let where = '';
    const params = [];
    if (keyword) {
      where = 'WHERE username LIKE ? OR nickname LIKE ?';
      params.push(`%${keyword}%`, `%${keyword}%`);
    }
    const [rows] = await pool.query(
      `SELECT id, username, nickname, team, role, status, created_at
       FROM sys_user ${where} ORDER BY id LIMIT 200`,
      params
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// POST /api/v1/admin/users 新建员工账号
router.post('/users', async (req, res, next) => {
  try {
    const { username, password, nickname, team } = req.body || {};
    if (!username || !/^[a-zA-Z0-9_]{2,64}$/.test(username)) {
      return fail(res, 400, 40010, '账号需为 2-64 位字母、数字或下划线');
    }
    if (!password || password.length < 6) return fail(res, 400, 40011, '密码至少 6 位');

    const [dup] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [username]);
    if (dup.length) return fail(res, 409, 40901, '账号已存在');

    const hash = await bcrypt.hash(password, 10);
    const [r] = await pool.query(
      'INSERT INTO sys_user (username, password_hash, nickname, team) VALUES (?, ?, ?, ?)',
      [username, hash, nickname || '', team || '']
    );
    return ok(res, { id: r.insertId }, '已创建');
  } catch (err) {
    return next(err);
  }
});

// PUT /api/v1/admin/users/:id 修改员工（昵称/班组/状态/角色/重置密码）
router.put('/users/:id', async (req, res, next) => {
  try {
    const targetId = Number(req.params.id);
    const { nickname, team, status, role, password } = req.body || {};

    // 自我保护：不能禁用或降级自己的账号
    if (targetId === req.user.id && (status === 0 || status === '0' || role === 'user')) {
      return fail(res, 400, 40012, '不能禁用或降级自己的账号');
    }

    const fields = [];
    const params = [];
    if (nickname !== undefined) { fields.push('nickname = ?'); params.push(nickname); }
    if (team !== undefined) { fields.push('team = ?'); params.push(team); }
    if (status !== undefined) { fields.push('status = ?'); params.push(status ? 1 : 0); }
    if (role !== undefined) {
      if (!['admin', 'user'].includes(role)) return fail(res, 400, 40013, '非法角色');
      fields.push('role = ?');
      params.push(role);
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
    return ok(res, null, '已保存');
  } catch (err) {
    return next(err);
  }
});

// GET /api/v1/admin/apps 全部应用列表
router.get('/apps', async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      'SELECT id, app_key, name, icon, path, sort, status FROM sys_app ORDER BY sort, id'
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// GET /api/v1/admin/users/:id/apps 某员工的已授权应用
router.get('/users/:id/apps', async (req, res, next) => {
  try {
    const [rows] = await pool.query('SELECT app_id FROM sys_user_app WHERE user_id = ?', [
      Number(req.params.id),
    ]);
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
    if (!ids) return fail(res, 400, 40015, 'app_ids 需为数组');
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
