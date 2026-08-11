// 安全日活动记录：班组迁移与改名级联（records.json 班组字段 + docs 班组子目录）
// 启动时由 db.js 调用 migrateSafedayTeams；班组改名时由 routes/admin.js 调用 renameTeamFolder
const fs = require('fs');
const path = require('path');
const config = require('../config');
const { pool } = require('../db');
const store = require('./store');

const DATA_DIR = path.resolve(config.safeday.dataDir);
const DOCS_DIR = path.join(DATA_DIR, 'docs');

// 默认班组名（首个启用班组；无可启用班组时回退任意首个）
async function defaultTeamName() {
  const [rows] = await pool.query('SELECT name FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1');
  if (rows.length) return rows[0].name;
  const [any] = await pool.query('SELECT name FROM sys_team ORDER BY sort, id LIMIT 1');
  return any.length ? any[0].name : '';
}

// 安全地移动文件（目标已存在则跳过，源保留）
function moveFile(from, to) {
  try {
    if (fs.existsSync(from) && !fs.existsSync(to)) {
      fs.mkdirSync(path.dirname(to), { recursive: true });
      fs.renameSync(from, to);
      return true;
    }
  } catch (err) {
    console.error(`[安全日] 文件迁移失败 ${from} → ${to}：`, err.message);
  }
  return false;
}

// 启动迁移：旧记录回填默认班组名；docs 根目录下记录对应的旧产物移入班组子目录
async function migrateSafedayTeams() {
  const teamName = await defaultTeamName();
  if (!teamName) return;
  if (store.backfillTeam(teamName)) {
    console.log(`[初始化] 安全日旧记录已回填班组「${teamName}」`);
  }
  let moved = 0;
  for (const r of store.list()) {
    if (!r.fileName || !r.team) continue;
    const flat = path.join(DOCS_DIR, path.basename(r.fileName));
    const nested = path.join(DOCS_DIR, r.team, path.basename(r.fileName));
    if (moveFile(flat, nested)) moved++;
  }
  if (moved) console.log(`[初始化] 安全日旧产物已迁入班组子目录 ${moved} 个`);
}

// 班组改名级联：records.json team 字段改写 + docs/{旧名} → docs/{新名}
async function renameTeamFolder(oldName, newName) {
  const count = store.renameTeam(oldName, newName);
  const oldDir = path.join(DOCS_DIR, oldName);
  const newDir = path.join(DOCS_DIR, newName);
  if (fs.existsSync(oldDir)) {
    if (!fs.existsSync(newDir)) {
      fs.renameSync(oldDir, newDir);
    } else {
      // 目标目录已存在（罕见）：逐个移入
      for (const f of fs.readdirSync(oldDir)) {
        moveFile(path.join(oldDir, f), path.join(newDir, f));
      }
    }
  }
  if (count) console.log(`[安全日] 班组「${oldName}」改名「${newName}」：已级联 ${count} 条记录与 docs 子目录`);
}

module.exports = { migrateSafedayTeams, renameTeamFolder };
