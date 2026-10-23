// 交跨记录删除共用逻辑（DELETE /records/:id 与 crane-answer 直推 deletes 共用）：
// 事务删记录行 + 写 crossing_deleted_id 墓碑（交跨无辅证照片，无 COS 清理）
const { pool } = require('../db');

// 按记录 id 整条删除（调用方已校验记录归属）
async function deleteRecord(recordId, teamId) {
  const conn = await pool.getConnection();
  try {
    await conn.beginTransaction();
    await conn.query('DELETE FROM crossing_record WHERE id = ?', [recordId]);
    await conn.query('INSERT IGNORE INTO crossing_deleted_id (id, team_id) VALUES (?, ?)', [
      recordId,
      teamId,
    ]);
    await conn.commit();
  } catch (e) {
    await conn.rollback().catch(() => {});
    throw e;
  } finally {
    conn.release();
  }
}

module.exports = { deleteRecord };
