// 检修一班杆塔坐标数据：全量行 [电压等级, 线路名称, 杆塔号, 经度, 纬度]，读盘一次缓存进内存
// 数据文件由根目录《检修一班杆塔坐标.xlsx》离线转换为 server/assets/worklog/tower-coords.json（随仓分发）
// 出工日志 /towers 与水印添加 /towers 共用本模块（同一份缓存）
const fs = require('fs');
const path = require('path');

let cache = null;

function getTowers() {
  if (!cache) {
    const file = path.join(__dirname, '../../assets/worklog/tower-coords.json');
    cache = JSON.parse(fs.readFileSync(file, 'utf8'));
  }
  return cache;
}

module.exports = { getTowers };
