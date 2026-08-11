// 出工成员字典与账号同步：班组内所有账号的昵称默认启用为本班出工成员（只增不删）
// 事件触发（员工管理变更时调 syncMemberForUser）：新建 / 改昵称 / 调班组 / 停启用；
// 启动时 syncAllUserMembers 全量对齐一次（只补缺失，不改既有行的启停与姓名，避免覆盖管理端的停用决定）
const { pool } = require('../db');
const config = require('../config');

// 事件驱动同步：单个账号变更后对齐其出工成员记录（幂等）
async function syncMemberForUser(userId) {
  if (!config.worklog.enabled) return;
  const [users] = await pool.query('SELECT id, nickname, team_id, status FROM sys_user WHERE id = ?', [userId]);
  const user = users[0];
  if (!user) return;

  // 停用账号 / 未分配班组 / 昵称为空：同步成员全部停用（不删，保留历史引用）
  if (user.status !== 1 || !user.team_id || !user.nickname) {
    await pool.query('UPDATE worklog_member SET status = 0 WHERE user_id = ?', [user.id]);
    return;
  }
  // 调出本班：其他班组的同步成员停用
  await pool.query('UPDATE worklog_member SET status = 0 WHERE user_id = ? AND team_id <> ?', [
    user.id, user.team_id,
  ]);

  // 本班组同步成员：已存在则对齐姓名并启用
  const [rows] = await pool.query(
    'SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?',
    [user.id, user.team_id]
  );
  if (rows.length) {
    try {
      await pool.query('UPDATE worklog_member SET name = ?, status = 1 WHERE id = ?', [
        user.nickname, rows[0].id,
      ]);
    } catch (err) {
      // 目标姓名与本班既有成员重名：保留既有行，同步行停用让位（不强行合并引用）
      if (err.code === 'ER_DUP_ENTRY') {
        await pool.query('UPDATE worklog_member SET status = 0 WHERE id = ?', [rows[0].id]);
      } else {
        throw err;
      }
    }
    return;
  }

  // 本班已有同名成员（历史种子 / 手动添加）：认领为账号同步成员
  const [same] = await pool.query(
    'SELECT id FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL',
    [user.team_id, user.nickname]
  );
  if (same.length) {
    await pool.query('UPDATE worklog_member SET user_id = ?, status = 1 WHERE id = ?', [user.id, same[0].id]);
    return;
  }

  const [maxRows] = await pool.query(
    'SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?',
    [user.team_id]
  );
  await pool.query(
    'INSERT INTO worklog_member (team_id, user_id, name, sort, status) VALUES (?, ?, ?, ?, 1)',
    [user.team_id, user.id, user.nickname, maxRows[0].maxSort + 1]
  );
}

// 启动全量对齐：仅补缺（插入 / 认领），不改既有成员行的启停与姓名
async function syncAllUserMembers() {
  if (!config.worklog.enabled) return;
  const [users] = await pool.query(
    `SELECT id, nickname, team_id FROM sys_user WHERE status = 1 AND team_id IS NOT NULL AND nickname <> ''`
  );
  for (const u of users) {
    try {
      const [rows] = await pool.query(
        'SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?',
        [u.id, u.team_id]
      );
      if (rows.length) continue;
      const [same] = await pool.query(
        'SELECT id FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL',
        [u.team_id, u.nickname]
      );
      if (same.length) {
        await pool.query('UPDATE worklog_member SET user_id = ? WHERE id = ?', [u.id, same[0].id]);
        continue;
      }
      const [maxRows] = await pool.query(
        'SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?',
        [u.team_id]
      );
      await pool.query(
        'INSERT INTO worklog_member (team_id, user_id, name, sort, status) VALUES (?, ?, ?, ?, 1)',
        [u.team_id, u.id, u.nickname, maxRows[0].maxSort + 1]
      );
    } catch (err) {
      console.error(`[出工日志] 成员同步失败（用户 ${u.id}）：`, err.message);
    }
  }
}

module.exports = { syncMemberForUser, syncAllUserMembers };
