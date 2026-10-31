// 交跨记录展示与表单工具（与旧 server/public/crossing.html 口径一致）。
// 通用展示/日期/数字等值/下载与字段表统一在 features/core/record-fields.js，
// 本文件只保留交跨特有逻辑，避免与树障各维护一份。
import { CROSSING_FIELDS, dash, fmtDeg, fmtKv, fmtM, voltageNum } from '../core/record-fields.js';

export {
  dash,
  fmtKv,
  fmtM,
  fmtDeg,
  fmtDate,
  saveBlob,
  currentUserObj,
  NUMERIC_KEYS_CROSSING,
  CROSSING_FIELDS,
} from '../core/record-fields.js';

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

// BIZ_FIELDS 13 业务列：暂存区编辑共用（key 与后端列名一致；巡视时间日期列单独渲染）。
export const BIZ_FIELDS = CROSSING_FIELDS.bizFields;

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

// SOURCE_TEXT 来源展示文案（与共享字段表同源）。
export const SOURCE_TEXT = CROSSING_FIELDS.sourceText;
