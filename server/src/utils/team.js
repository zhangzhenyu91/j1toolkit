// 班组工具：班组列表、默认班组、请求级「生效班组」解析
// 生效班组口径：超管可用 query/body 的 team_id 指定任意启用班组（未指定时落自己班组，再无则默认班组）；
// 班组管理员 / 普通用户固定为本人 team_id（未分配 → null，子应用按空态处理）
const { pool } = require('../db');

// 全部班组（含停用），sort 小的在前 —— 管理接口用
async function listAllTeams() {
  const [rows] = await pool.query('SELECT id, name, kvm_group_name, sort, status FROM sys_team ORDER BY sort, id');
  return rows;
}

// 启用班组列表（下拉/切换器数据源）
async function listEnabledTeams() {
  const [rows] = await pool.query(
    'SELECT id, name, kvm_group_name, sort FROM sys_team WHERE status = 1 ORDER BY sort, id'
  );
  return rows;
}

// 默认班组 = sort 最小的启用班组（种子即检修一班；既有数据迁移与 KVM 设备回退都归它）
async function getDefaultTeam() {
  const [rows] = await pool.query(
    'SELECT id, name, kvm_group_name, sort FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1'
  );
  return rows[0] || null;
}

async function getTeamById(id) {
  const [rows] = await pool.query(
    'SELECT id, name, kvm_group_name, sort, status FROM sys_team WHERE id = ?',
    [Number(id) || 0]
  );
  return rows[0] || null;
}

// 解析请求的生效班组；user 为 auth 中间件装载的 req.user（含 role / team_id）
// wantedTeamId 仅超管生效（可传 'all' 表示全部班组——仅安全日记录列表等流水视图使用，调用方自行识别）
async function resolveTeam(user, wantedTeamId) {
  if (user.role === 'admin') {
    const wanted = Number(wantedTeamId);
    if (Number.isInteger(wanted) && wanted > 0) {
      const t = await getTeamById(wanted);
      if (t && t.status === 1) return t;
    }
    if (user.team_id) {
      const t = await getTeamById(user.team_id);
      if (t && t.status === 1) return t;
    }
    return getDefaultTeam();
  }
  if (!user.team_id) return null;
  const t = await getTeamById(user.team_id);
  if (!t || t.status !== 1) return null;
  return t;
}

// 从请求取指定期望班组（query 优先，其次 body），再按角色收敛
function resolveReqTeam(req) {
  const q = req.query && req.query.team_id;
  const b = req.body && req.body.team_id;
  return resolveTeam(req.user, q !== undefined ? q : b);
}

module.exports = { listAllTeams, listEnabledTeams, getDefaultTeam, getTeamById, resolveTeam, resolveReqTeam };
