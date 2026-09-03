// 出工成员字典与账号同步：班组内所有账号的昵称默认启用为本班出工成员（只增不删）
// 事件触发（员工管理变更时调 syncMemberForUser）：新建 / 改昵称 / 调班组 / 停启用；
// 调班组 = 他班同步成员行直接迁移到新班（member_id 不变，历史卡片/打卡/照片人名引用全保持，原班不留停用残留；
// 仅新班已有同名成员撞 uk_team_member 时回退「旧行停用让位 + 认领/新建」），商旅账号绑定随人迁移（token 跟人走）；
// 启动时 syncAllUserMembers 全量对齐一次（只补缺失，不改既有行的启停与姓名，避免覆盖管理端的停用决定）
const { pool } = require('../db');
const config = require('../config');

// 商旅账号跟随调班：绑定行迁到本人当前班组与当前成员行（token 跟人走，调班无需重绑）。
// 正常迁移 member_id 不变（仅 team_id 跟进）；重名回退路径传 oldMemberId，member_id 一并改指新行。
// ER_DUP_ENTRY（新成员行已被管理员代绑占 uk_member）：先删本人指向旧成员/他班的行，占坑代绑行归并本人
async function syncSgccAccountForUser(user, memberId, oldMemberId = null) {
  if (!config.sgcc || !config.sgcc.enabled || !memberId) return;
  const oldId = oldMemberId || memberId;
  try {
    await pool.query(
      'UPDATE worklog_sgcc_account SET team_id = ?, member_id = ? WHERE user_id = ? OR member_id = ?',
      [user.team_id, memberId, user.id, oldId]
    );
  } catch (err) {
    if (err.code !== 'ER_DUP_ENTRY') throw err;
    await pool.query(
      'DELETE FROM worklog_sgcc_account WHERE (user_id = ? OR member_id = ?) AND member_id <> ?',
      [user.id, oldId, memberId]
    );
    await pool.query(
      'UPDATE worklog_sgcc_account SET user_id = ?, team_id = ? WHERE member_id = ? AND user_id IS NULL',
      [user.id, user.team_id, memberId]
    );
  }
}

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
  // 调出本班：其他班组的同步成员先停用（被直接迁移的行随后由迁移语句换班并重新启用；
  // 历史调班残留的跨班重复行保持停用收敛）
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
      await syncSgccAccountForUser(user, rows[0].id);
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

  // 调入本班：他班同步成员行直接迁移（member_id 不变，历史引用全保持；sort 排到本班末尾，同新建行口径）
  const [otherRows] = await pool.query(
    'SELECT id FROM worklog_member WHERE user_id = ? AND team_id <> ? ORDER BY id DESC',
    [user.id, user.team_id]
  );
  if (otherRows.length) {
    try {
      const [maxRows] = await pool.query(
        'SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?',
        [user.team_id]
      );
      await pool.query('UPDATE worklog_member SET team_id = ?, name = ?, sort = ?, status = 1 WHERE id = ?', [
        user.team_id, user.nickname, maxRows[0].maxSort + 1, otherRows[0].id,
      ]);
      await syncSgccAccountForUser(user, otherRows[0].id);
      return;
    } catch (err) {
      if (err.code !== 'ER_DUP_ENTRY') throw err;
      // 新班已有同名成员：迁移撞 uk_team_member，回退「旧行停用让位 + 下方认领/新建」（member_id 变更，账号改指新行）
      await pool.query('UPDATE worklog_member SET status = 0 WHERE id = ?', [otherRows[0].id]);
    }
  }
  const oldMemberId = otherRows.length ? otherRows[0].id : null;

  // 本班已有同名成员（历史种子 / 手动添加）：认领为账号同步成员
  const [same] = await pool.query(
    'SELECT id FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL',
    [user.team_id, user.nickname]
  );
  if (same.length) {
    await pool.query('UPDATE worklog_member SET user_id = ?, status = 1 WHERE id = ?', [user.id, same[0].id]);
    await syncSgccAccountForUser(user, same[0].id, oldMemberId);
    return;
  }

  const [maxRows] = await pool.query(
    'SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?',
    [user.team_id]
  );
  const [ins] = await pool.query(
    'INSERT INTO worklog_member (team_id, user_id, name, sort, status) VALUES (?, ?, ?, ?, 1)',
    [user.team_id, user.id, user.nickname, maxRows[0].maxSort + 1]
  );
  await syncSgccAccountForUser(user, ins.insertId, oldMemberId);
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
