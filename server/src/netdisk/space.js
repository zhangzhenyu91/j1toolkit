// 团队网盘空间根解析（共享口径）：index.js 路由层与 save.js / kvm 等内部模块共用
// my 个人空间 = {personalRoot}/{username}；public 公共区按班组隔离 = {publicRoot}/{班组名}
const config = require('../config');
const { pool } = require('../db');

// 班组名缓存（team_id → name；班组改名罕见，进程重启即刷新）
const teamNameCache = new Map();
async function teamNameOf(user) {
  if (!user.team_id) return '';
  if (teamNameCache.has(user.team_id)) return teamNameCache.get(user.team_id);
  const [rows] = await pool.query('SELECT name FROM sys_team WHERE id = ?', [user.team_id]);
  const name = (rows[0] && rows[0].name) || '';
  teamNameCache.set(user.team_id, name);
  return name;
}

// 空间根解析（纯函数版）：返回绝对路径；未分配班组的公共区返回空串，space 非法返回 null
async function spaceRootOf(user, space) {
  if (space === 'my') return `${config.netdisk.personalRoot}/${user.username}`;
  if (space === 'public') {
    const team = await teamNameOf(user);
    return team ? `${config.netdisk.publicRoot}/${team}` : '';
  }
  return null;
}

// 归一相对路径：反斜杠转正、去多余斜杠、拒绝 .. 与单点、限长；'/' 表示空间根
function normRel(input) {
  let p = String(input == null ? '/' : input).trim().replace(/\\/g, '/');
  if (!p || p === '/') return '/';
  if (!p.startsWith('/')) p = `/${p}`;
  const parts = p.split('/').filter(Boolean);
  if (parts.some((seg) => seg === '..' || seg === '.')) return null;
  const joined = `/${parts.join('/')}`;
  return joined.length > 400 ? null : joined;
}

module.exports = { teamNameOf, spaceRootOf, normRel };
