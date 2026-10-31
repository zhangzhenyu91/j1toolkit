// 树障 / 交跨共享字段与展示工具：两组台账的冲突对照、暂存区、导出和卡片使用同一套
// 空值/单位/日期/数字等值口径，字段表按台账参数化（不做第二套复制实现）。
// 冲突对照的「差异」判定与后端 diff 保持一致：日期已归一 yyyy-MM-dd；数字列 "3" 与 "3.0" 视为相同。
import { currentUser } from './session.js';

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

// fmtM 距离展示幂等：纯数字拼 m 后缀；已带单位或其他文本原样（空 → —）。
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

function pad2(n) {
  return String(n).padStart(2, '0');
}

// fmtDate 时间展示 'YYYY-MM-DD HH:mm'（withTime=false 仅日期）。
export function fmtDate(d, withTime = true) {
  const dt = d ? new Date(d) : new Date();
  if (Number.isNaN(dt.getTime())) return '';
  const day = `${dt.getFullYear()}-${pad2(dt.getMonth() + 1)}-${pad2(dt.getDate())}`;
  return withTime ? `${day} ${pad2(dt.getHours())}:${pad2(dt.getMinutes())}` : day;
}

// fmtDateCN 日期中文（X 年 X 月 X 日，格式不合法返回空串）。
export function fmtDateCN(d) {
  const [y, m, dd] = String(d || '').split('-');
  if (!y || !m || !dd) return '';
  return `${y} 年 ${Number(m)} 月 ${Number(dd)} 日`;
}

// todayStr 当日日期 'YYYY-MM-DD'（处理时间默认值）。
export function todayStr() {
  return fmtDate(new Date(), false);
}

// voltageNum 电压等级数值提取（线路卡排序用；非数字回退 0）。
export function voltageNum(v) {
  const n = parseFloat(String(v == null ? '' : v).replace(/[^\d.]/g, ''));
  return Number.isNaN(n) ? 0 : n;
}

// sameVal 单元格等值判定（数字列 "3" 与 "3.0" 相同，与后端 diff 口径一致）。
export function sameVal(key, a, b, numericKeys = []) {
  const sa = String(a == null ? '' : a).trim();
  const sb = String(b == null ? '' : b).trim();
  if (sa === sb) return true;
  if (numericKeys.includes(key) && sa && sb) {
    const na = Number(sa);
    const nb = Number(sb);
    if (!Number.isNaN(na) && !Number.isNaN(nb)) return na === nb;
  }
  return false;
}

// cellVal 单元格展示格式化：电压补 kV，其余空值占位。
export function cellVal(key, raw) {
  if (key === 'voltage_level') return fmtKv(raw);
  return dash(raw);
}

// conflictServerValue 系统当前值：冲突行平铺字段（LEFT JOIN 当前记录；记录已删时回退服务端快照）。
export function conflictServerValue(c, key) {
  let v = c[key];
  if ((v === null || v === undefined) && c.server_snapshot) v = c.server_snapshot[key];
  return v;
}

function intranetVal(c, key) {
  return c.intranet_snapshot ? c.intranet_snapshot[key] : undefined;
}

function statusVal(c) {
  return c.record_status || (c.server_snapshot && c.server_snapshot.status) || 'unhandled';
}

function intranetStatus(c) {
  return String(intranetVal(c, 'handled_at') || '').trim() ? 'handled' : 'unhandled';
}

function statusText(s) {
  return s === 'handled' ? '已处理' : '未处理';
}

// buildConflictRows 冲突卡对照行（系统值 | 内网值）：字段表 + 可选状态行；
// 返回 [{key,label,diff,server,intranet}]，server/intranet 为已完成格式化与占位的展示文本。
export function buildConflictRows(c, { fields, numericKeys = [], showStatus = false } = {}) {
  const rows = (fields || []).map(([key, label]) => {
    const sv = conflictServerValue(c, key);
    const iv = intranetVal(c, key);
    return {
      key,
      label,
      diff: !sameVal(key, sv, iv, numericKeys),
      server: cellVal(key, sv),
      intranet: cellVal(key, iv),
    };
  });
  if (showStatus) {
    const s = statusVal(c);
    const i = intranetStatus(c);
    rows.push({
      key: 'status',
      label: '状态',
      diff: s !== i,
      server: statusText(s),
      intranet: statusText(i),
    });
  }
  return rows;
}

// parseDispositionFilename 解析 Content-Disposition 文件名：优先 UTF-8 filename*，回退 filename。
export function parseDispositionFilename(header, fallbackName) {
  const cd = header || '';
  let name = fallbackName;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (star) {
    try {
      name = decodeURIComponent(star[1]);
    } catch (e) {
      name = star[1];
    }
    return name;
  }
  const plain = /filename="?([^";]+)"?/i.exec(cd);
  if (plain) name = plain[1];
  return name;
}

// saveBlob 二进制 Response 落地为下载文件（文件名先解析 Content-Disposition）。
export async function saveBlob(res, fallbackName) {
  const name = parseDispositionFilename(res.headers.get('Content-Disposition'), fallbackName);
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

// 字段表（key 与后端列名一致；发现/处理列与后端 BIZ_FIELDS 顺序一致）。
const TREE_BIZ_FIELDS = [
  ['voltage_level', '电压等级'],
  ['line_name', '线路名称'],
  ['section', '区段'],
  ['tree_species', '树种'],
  ['tree_count', '数量'],
  ['dist_small', '距小号'],
  ['dist_big', '距大号'],
  ['diameter', '直径'],
  ['horizontal', '水平'],
  ['vertical', '垂直'],
  ['finder_name', '发现人'],
  ['found_at', '发现时间'],
  ['handler_name', '处理人'],
  ['handle_note', '处理情况'],
  ['handled_at', '处理时间'],
];

const CROSSING_BIZ_FIELDS = [
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

const TREE_SOURCE_TEXT = {
  crane: '排查直推',
  staging: '暂存导入',
  intranet: '内网回写',
  manual: '手动录入',
};

const CROSSING_SOURCE_TEXT = {
  crane: '群捕获直推',
  staging: '暂存导入',
  intranet: '内网回写',
  manual: '手动录入',
};

// TREE_FIELDS 树障台账配置：15 业务列 + 状态行；日期列与长字段按后端列名。
export const TREE_FIELDS = {
  bizFields: TREE_BIZ_FIELDS,
  dateFields: ['found_at', 'handled_at'],
  noteField: 'handle_note',
  numericKeys: [
    'voltage_level',
    'tree_count',
    'dist_small',
    'dist_big',
    'diameter',
    'horizontal',
    'vertical',
  ],
  showStatus: true,
  sourceText: TREE_SOURCE_TEXT,
  exportName: '树木统计表.xlsx',
};

// CROSSING_FIELDS 交跨台账配置：13 业务列，无处理状态行。
export const CROSSING_FIELDS = {
  bizFields: CROSSING_BIZ_FIELDS,
  dateFields: ['inspect_at'],
  noteField: 'remark',
  numericKeys: [
    'voltage_level',
    'dist_small',
    'dist_big',
    'cross_horizontal',
    'cross_vertical',
    'cross_angle',
  ],
  showStatus: false,
  sourceText: CROSSING_SOURCE_TEXT,
  exportName: '交跨统计表.xlsx',
};

// 兼容既有 util.js 命名：数字化列键。
export const NUMERIC_KEYS_TREE = TREE_FIELDS.numericKeys;
export const NUMERIC_KEYS_CROSSING = CROSSING_FIELDS.numericKeys;
