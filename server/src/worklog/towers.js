// 杆塔坐标：按班组存 worklog_tower 表，内存按 team_id 缓存（首次访问读库）
// 返回形状与既有前端约定保持 { rows: [...] }，行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]
// 经纬度按数字返回（库内 VARCHAR 存储，前端按数字做 toFixed/波动偏移运算，字符串会崩）
const { pool } = require('../db');

const cache = new Map(); // teamId -> { rows }

async function getTowers(teamId) {
  const key = Number(teamId) || 0;
  if (!cache.has(key)) {
    const [rows] = await pool.query(
      'SELECT voltage_level, line_name, tower_no, lng, lat FROM worklog_tower WHERE team_id = ? ORDER BY sort, id',
      [key]
    );
    cache.set(key, {
      rows: rows.map((r) => [r.voltage_level, r.line_name, r.tower_no, Number(r.lng), Number(r.lat)]),
    });
  }
  return cache.get(key);
}

function invalidate(teamId) {
  cache.delete(Number(teamId) || 0);
}

module.exports = { getTowers, invalidate };
