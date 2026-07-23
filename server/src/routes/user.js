// 用户路由：当前登录用户信息
const express = require('express');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const auth = require('../middleware/auth');

const router = express.Router();

// GET /api/v1/user/profile
router.get('/profile', auth, async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      'SELECT id, username, nickname, avatar, team, role, openid, created_at FROM sys_user WHERE id = ?',
      [req.user.id]
    );
    if (!rows.length) return fail(res, 404, 40401, '用户不存在');
    const u = rows[0];
    return ok(res, {
      id: u.id,
      username: u.username,
      nickname: u.nickname,
      avatar: u.avatar,
      team: u.team,
      role: u.role,
      wx_bound: !!u.openid, // 不返回 openid 本体，只给绑定状态
      created_at: u.created_at,
    });
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
