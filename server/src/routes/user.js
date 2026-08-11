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
      `SELECT u.id, u.username, u.nickname, u.avatar, u.team_id, t.name AS team, u.role, u.openid, u.created_at
       FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id WHERE u.id = ?`,
      [req.user.id]
    );
    if (!rows.length) return fail(res, 404, 40401, '用户不存在');
    const u = rows[0];
    return ok(res, {
      id: u.id,
      username: u.username,
      nickname: u.nickname,
      avatar: u.avatar,
      team: u.team || '',
      team_id: u.team_id,
      role: u.role,
      wx_bound: !!u.openid, // 不返回 openid 本体，只给绑定状态
      created_at: u.created_at,
    });
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
