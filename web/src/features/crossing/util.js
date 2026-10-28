// 交跨记录展示与表单工具（与旧 server/public/crossing.html 口径一致）。
import { currentUser } from '../core/session.js';

// dash 空值展示占位。
export function dash(v) {
  const s = String(v == null ? '' : v).trim();
  return s || '—';
}

// fmtKv 电压字段展示：纯数字补 kV，空值 '—'，其余原样。
export function fmtKv(v) {
  if (v === null || v === undefined) return '—';
  const s = String(v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + 'kV' : String(v);
}

// fmtM 距离展示幂等：纯数字拼 m 后缀（空 → —）。
export function fmtM(v) {
  const s = String(v == null ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + 'm' : s;
}

// fmtDeg 角度展示幂等：纯数字拼 ° 后缀（空 → —）。
export function fmtDeg(v) {
  const s = String(v == null ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + '°' : s;
}

// fmtDate 时间展示 'YYYY-MM-DD HH:mm'。
export function fmtDate(d, withTime = true) {
  const dt = d ? new Date(d) : new Date();
  if (Number.isNaN(dt.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  const day = `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
  return withTime ? `${day} ${p(dt.getHours())}:${p(dt.getMinutes())}` : day;
}

// hasHazard 是否存在隐患（空/'否'/'无' 视为无）。
export function hasHazard(r) {
  const s = String(r.has_hazard == null ? '' : r.has_hazard).trim();
  return !!s && s !== '否' && s !== '无';
}

// fmtField 字段展示格式化（电压补 kV、距离补 m、角度补 °）。
export function fmtField(key, v) {
  if (key === 'voltage_level') return fmtKv(v);
  if (
    key === 'dist_small' ||
    key === 'dist_big' ||
    key === 'cross_horizontal' ||
    key === 'cross_vertical'
  )
    return fmtM(v);
  if (key === 'cross_angle') return fmtDeg(v);
  return dash(v);
}

// voltageNum 电压等级数值提取（线路卡排序用；非数字回退 0）。
export function voltageNum(v) {
  const n = parseFloat(String(v == null ? '' : v).replace(/[^\d.]/g, ''));
  return Number.isNaN(n) ? 0 : n;
}

// lineStats 单线路计数：未处理数 → 交跨无处理流，改记 隐患数 / 总数。
export function lineStats(recs) {
  let hz = 0;
  recs.forEach((r) => {
    if (hasHazard(r)) hz++;
  });
  return { hz, total: recs.length };
}

// sortLineGroups 线路卡排序：隐患数降序 > 总数降序 > 电压等级数值降序 > 线路名称升序。
export function sortLineGroups(list) {
  const stats = {};
  list.forEach((g) => {
    stats[g.line_name] = lineStats(g.records || []);
  });
  return list.slice().sort((a, b) => {
    const sa = stats[a.line_name];
    const sb = stats[b.line_name];
    if (sb.hz !== sa.hz) return sb.hz - sa.hz;
    if (sb.total !== sa.total) return sb.total - sa.total;
    const dv = voltageNum(b.voltage_level) - voltageNum(a.voltage_level);
    if (dv) return dv;
    return String(a.line_name || '').localeCompare(String(b.line_name || ''), 'zh-CN');
  });
}

// saveBlob 二进制 Response 落地为下载文件（文件名先解析 Content-Disposition）。
export async function saveBlob(res, fallbackName) {
  const cd = res.headers.get('Content-Disposition') || '';
  let name = fallbackName;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (star) {
    try {
      name = decodeURIComponent(star[1]);
    } catch (e) {
      name = star[1];
    }
  } else {
    const plain = /filename="?([^";]+)"?/i.exec(cd);
    if (plain) name = plain[1];
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// currentUserObj 当前登录用户（与公共会话存储同源）。
export function currentUserObj() {
  return currentUser() || {};
}

// BIZ_FIELDS 12 业务列：暂存区编辑共用（key 与后端列名一致；inspect_at 日期列单独渲染）。
export const BIZ_FIELDS = [
  ['voltage_level', '电压等级'],
  ['line_name', '线路名称'],
  ['section', '区段'],
  ['crossing_type', '跨越类型'],
  ['dist_small', '距小号'],
  ['dist_big', '距大号'],
  ['cross_horizontal', '边线水平'],
  ['cross_vertical', '垂直距离'],
  ['cross_angle', '交叉角'],
  ['has_hazard', '是否存在隐患'],
  ['finder_name', '发现人'],
  ['inspect_at', '巡视时间'],
  ['remark', '备注'],
];

// COMPACT_FIELDS 卡片紧凑信息行（区段/垂距/类型/交叉角在摘要行，展示时跳过）。
export const COMPACT_FIELDS = [
  ['voltage_level', '电压等级'],
  ['line_name', '线路名称'],
  ['section', '跨越区段'],
  ['crossing_type', '跨越类型'],
  ['dist_small', '距小号(m)'],
  ['dist_big', '距大号(m)'],
  ['cross_horizontal', '边线水平(m)'],
  ['cross_vertical', '垂直距离(m)'],
  ['cross_angle', '交叉角(°)'],
  ['has_hazard', '是否存在隐患'],
  ['finder_name', '发现人'],
  ['inspect_at', '巡视时间'],
  ['remark', '备注'],
];

// SOURCE_TEXT 来源展示文案。
export const SOURCE_TEXT = {
  crane: '群捕获直推',
  staging: '暂存导入',
  intranet: '内网回写',
  manual: '手动录入',
};
