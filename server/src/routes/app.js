// 应用路由：当前用户可见的应用列表（按权限过滤）
const express = require('express');
const { pool } = require('../db');
const { ok } = require('../utils/resp');
const auth = require('../middleware/auth');

const router = express.Router();

// GET /api/v1/app/list
router.get('/list', auth, async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      `SELECT a.app_key, a.name, a.icon, a.path, a.terminal, a.sort
       FROM sys_user_app ua
       JOIN sys_app a ON a.id = ua.app_id
       WHERE ua.user_id = ? AND a.status = 1
       ORDER BY a.sort, a.id`,
      [req.user.id]
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
