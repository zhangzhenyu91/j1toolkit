// 树障记录删除共用逻辑（DELETE /records/:id 与 crane-answer 直推 deletes 共用）：
// 删照片 COS 对象（失败仅记日志不阻断）→ 事务删照片表行 + 记录行 + 写 treehazard_deleted_id 墓碑
const { pool } = require('../db');
const cos = require('../worklog/cos');

// 按记录 id 整条删除（调用方已校验记录归属；COS 删除失败仅记日志，继续删表行与墓碑）
async function deleteRecord(recordId, teamId) {
  const [photos] = await pool.query('SELECT cos_key FROM treehazard_photo WHERE record_id = ?', [
    recordId,
  ]);
  for (const p of photos) {
    try {
      await cos.deleteObject(p.cos_key);
    } catch (e) {
      console.error('[树障] 删除 COS 对象失败（继续删记录）：', e.message);
    }
  }
  const conn = await pool.getConnection();
  try {
    await conn.beginTransaction();
    await conn.query('DELETE FROM treehazard_photo WHERE record_id = ?', [recordId]);
    await conn.query('DELETE FROM treehazard_record WHERE id = ?', [recordId]);
    await conn.query('INSERT IGNORE INTO treehazard_deleted_id (id, team_id) VALUES (?, ?)', [
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
