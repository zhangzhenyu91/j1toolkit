// 树障隐患记录：内网树木统计表解析与行级差异比对（纯函数模块，供后续内网 U 盘同步模块复用）
// 内网表模板即 export.js 的导出件：R1 合并标题、R2 表头、Q 列隐藏记录ID；本模块不触库，输入输出均为 plain object
const XLSX = require('xlsx');
const { parseVerticalNum } = require('./export');

// 比对列集：发现 12 列 + 处理 3 列（与 treehazard_record / 内网表列序一致）
const DISCOVERY_COLS = [
  'voltage_level',
  'line_name',
  'section',
  'tree_species',
  'tree_count',
  'dist_small',
  'dist_big',
  'diameter',
  'horizontal',
  'vertical',
  'finder_name',
  'found_at',
];
const HANDLE_COLS = ['handler_name', 'handle_note', 'handled_at'];
// 数字类列：「3」与「3.0」视为相同（Number 等值比较，任一侧 NaN/空 时按字符串比较）
const NUMERIC_COLS = new Set([
  'voltage_level',
  'tree_count',
  'dist_small',
  'dist_big',
  'diameter',
  'horizontal',
  'vertical',
]);
// 日期列：统一归一为 yyyy-MM-dd 比较
const DATE_COLS = new Set(['found_at', 'handled_at']);

// 字符串归一（null/undefined → ''，trim）
function normStr(v) {
  return String(v ?? '').trim();
}

// 日期归一为 'yyyy-MM-dd'：兼容 Date 与 'yyyy.MM.dd' / 'yyyy-MM-dd' / 'yyyy/M/D' 文本；无法识别 → ''
function normDateStr(v) {
  if (v === null || v === undefined) return '';
  if (v instanceof Date) {
    if (Number.isNaN(v.getTime())) return '';
    const y = v.getFullYear();
    const m = String(v.getMonth() + 1).padStart(2, '0');
    const d = String(v.getDate()).padStart(2, '0');
    return `${y}-${m}-${d}`;
  }
  const s = String(v).trim();
  if (!s) return '';
  const m = /^(\d{4})[-./](\d{1,2})[-./](\d{1,2})/.exec(s);
  if (!m) return '';
  return `${m[1]}-${m[2].padStart(2, '0')}-${m[3].padStart(2, '0')}`;
}

// 单元格等值判定（按列类型走数字/日期/字符串口径）
function sameCell(col, a, b) {
  if (DATE_COLS.has(col)) return normDateStr(a) === normDateStr(b);
  const sa = normStr(a);
  const sb = normStr(b);
  if (sa === sb) return true;
  if (NUMERIC_COLS.has(col) && sa !== '' && sb !== '') {
    const na = Number(sa);
    const nb = Number(sb);
    if (!Number.isNaN(na) && !Number.isNaN(nb)) return na === nb;
  }
  return false;
}

// 区段文本（如「3#-4#」）尽力解析杆号区间 → [pole_from, pole_to]；解析不出 → [null, null]
//（暂存导入与内网 news 落库共用：暂存/内网行均不含 pole 列，按区段文本补便于按杆号排序）
function parseSectionPoles(section) {
  const m = /(\d+)\s*#?\s*[-—~～]\s*(\d+)\s*#?/.exec(String(section || ''));
  return m ? [Number(m[1]), Number(m[2])] : [null, null];
}

// 解析内网树木统计表（buffer → 行对象数组）：
// 首个 sheet → 定位「序号」表头行（兼容两行表头模板：R1 标题 R2 表头）→ 数据行按列序映射；
// record_id 读 Q 列（第 17 列），空/非法 → null；日期 'yyyy.MM.dd' → 'yyyy-MM-dd'
function parseIntranetSheet(buffer) {
  const wb = XLSX.read(buffer, { type: 'buffer' });
  const rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], {
    header: 1,
    raw: false,
    defval: '',
  });
  let headIdx = -1;
  for (let i = 0; i < rows.length; i += 1) {
    if (rows[i].some((c) => String(c).trim() === '序号')) {
      headIdx = i;
      break;
    }
  }
  if (headIdx === -1) return [];
  const out = [];
  for (let i = headIdx + 1; i < rows.length; i += 1) {
    const r = rows[i];
    // 整行业务列为空 → 跳过（尾部空行/装饰行）
    const anyBiz = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15].some(
      (j) => normStr(r[j]) !== ''
    );
    if (!anyBiz) continue;
    const rid = parseInt(normStr(r[16]), 10);
    out.push({
      row_no: normStr(r[0]),
      // 电压等级导出时拼了 kV 后缀，读回时剥掉与服务端原始值对齐
      voltage_level: normStr(r[1]).replace(/\s*kv$/i, ''),
      line_name: normStr(r[2]),
      section: normStr(r[3]),
      tree_species: normStr(r[4]),
      tree_count: normStr(r[5]),
      dist_small: normStr(r[6]),
      dist_big: normStr(r[7]),
      diameter: normStr(r[8]),
      horizontal: normStr(r[9]),
      vertical: normStr(r[10]),
      finder_name: normStr(r[11]),
      found_at: normDateStr(r[12]),
      handler_name: normStr(r[13]),
      handle_note: normStr(r[14]),
      handled_at: normDateStr(r[15]),
      record_id: Number.isInteger(rid) && rid > 0 ? rid : null,
    });
  }
  return out;
}

// 服务端记录快照（冲突留档与 intranet 快照同形：id + 15 业务列 + vertical_num + status）
function snapshotOf(record) {
  const snap = { id: Number(record.id) };
  for (const c of [...DISCOVERY_COLS, ...HANDLE_COLS]) {
    snap[c] = DATE_COLS.has(c) ? normDateStr(record[c]) : normStr(record[c]);
  }
  snap.vertical_num =
    record.vertical_num === null || record.vertical_num === undefined
      ? null
      : Number(record.vertical_num);
  snap.status = record.status || 'unhandled';
  return snap;
}

// 内网行 → 回写服务端字段集（15 业务列 + vertical_num + status）：
// handled_at 空 → status=unhandled 且清空处理三列（内网把已处理改回未处理的场景）
function intranetFields(row) {
  const fields = {};
  for (const c of DISCOVERY_COLS) {
    fields[c] = DATE_COLS.has(c) ? normDateStr(row[c]) || null : normStr(row[c]);
  }
  for (const c of HANDLE_COLS) {
    fields[c] = c === 'handled_at' ? normDateStr(row[c]) || null : normStr(row[c]);
  }
  fields.vertical_num = parseVerticalNum(row.vertical);
  if (fields.handled_at) {
    fields.status = 'handled';
  } else {
    fields.status = 'unhandled';
    fields.handler_name = '';
    fields.handle_note = '';
  }
  return fields;
}

// 无 ID 行业务键比对：line_name + section + tree_species + tree_count + found_at 全等视为疑似重复
function bizKeySame(serverRecord, row) {
  return ['line_name', 'section', 'tree_species', 'tree_count', 'found_at'].every((c) =>
    sameCell(c, serverRecord[c], row[c])
  );
}

function toTime(v) {
  if (!v) return NaN;
  const t = new Date(v).getTime();
  return Number.isNaN(t) ? NaN : t;
}

// 行级差异比对：
// serverRecords：本班服务端记录全量（含 15 业务列 + status + updated_at + synced_at）；
// intranetRows：parseIntranetSheet 产物；deletedIds：treehazard_deleted_id 墓碑 id 数组；
// adjudicated：已裁决驳回（resolution='server'）的冲突快照 [{ record_id, intranet }]（intranet 为已解析的 intranet_snapshot 对象）。
// 返回 { updates:[{id, fields}], news:[rows], conflicts:[{id, server, intranet}], skips, adjudicated }（后两者为计数）。
// 规则：有 record_id 且服务端存在——列值相同 → skip；不同且（从未同步 synced_at NULL 或 服务端同步后又改 updated_at>synced_at）→ 冲突；
// 否则仅内网改过 → update（内网值覆盖，handled_at 空 → 回退未处理并清处理列）；
// 但 update 前行与任一已裁决驳回快照全等 → adjudicated skip（裁决 keep=server 后内网旧表未收敛，驳回旧值不回灌防反转裁决，
// 由调用方判为待收敛触发下发；裁决后内网改出新值与快照不再全等，不在此列，按正常 update 走）。
// 有 record_id 服务端不存在——在墓碑 → skip；否则按无 ID 处理。同 record_id 多行：首行按上规则，其余按无 ID 处理。
// 无 record_id——业务键命中服务端 → 冲突（疑似重复，server 快照带上）；否则 → news。
function diffRecords(serverRecords, intranetRows, { deletedIds = [], adjudicated = [] } = {}) {
  const delSet = new Set((deletedIds || []).map(Number));
  const serverById = new Map((serverRecords || []).map((r) => [Number(r.id), r]));
  // 已裁决驳回快照按 record_id 分组（同一记录可能裁决过多轮，与任一快照全等即为旧表未收敛）
  const adjMap = new Map();
  for (const a of adjudicated || []) {
    const rid = Number(a && a.record_id);
    if (!Number.isInteger(rid) || rid <= 0 || !a.intranet) continue;
    if (!adjMap.has(rid)) adjMap.set(rid, []);
    adjMap.get(rid).push(a.intranet);
  }
  const updates = [];
  const news = [];
  const conflicts = [];
  let skips = 0;
  let adjudicatedSkips = 0;
  const seenIds = new Set();

  // 行与已裁决驳回快照全等判定（15 业务列，等值口径同 sameCell 列比较）
  const isAdjudicated = (rid, row) => {
    const snaps = adjMap.get(rid);
    return (
      !!snaps &&
      snaps.some((snap) =>
        [...DISCOVERY_COLS, ...HANDLE_COLS].every((c) => sameCell(c, snap[c], row[c]))
      )
    );
  };

  const handleNoId = (row) => {
    const hit = (serverRecords || []).find((s) => bizKeySame(s, row));
    if (hit) {
      conflicts.push({ id: Number(hit.id), server: snapshotOf(hit), intranet: row });
    } else {
      news.push(row);
    }
  };

  for (const row of intranetRows || []) {
    const rid = row && row.record_id ? Number(row.record_id) : null;
    if (rid && !seenIds.has(rid)) {
      seenIds.add(rid);
      const srv = serverById.get(rid);
      if (srv) {
        const changed = [...DISCOVERY_COLS, ...HANDLE_COLS].some(
          (c) => !sameCell(c, srv[c], row[c])
        );
        if (!changed) {
          skips += 1;
          continue;
        }
        const syncedAt = toTime(srv.synced_at);
        const updatedAt = toTime(srv.updated_at);
        // synced_at NULL = 从未同步（不一致即冲突）；updated_at>synced_at = 服务端在同步点之后又改过（双边都改 → 冲突）
        if (Number.isNaN(syncedAt) || (!Number.isNaN(updatedAt) && updatedAt > syncedAt)) {
          conflicts.push({ id: rid, server: snapshotOf(srv), intranet: row });
        } else if (isAdjudicated(rid, row)) {
          skips += 1; // 已裁决驳回的内网旧值：不回灌（防反转裁决），待下发收敛
          adjudicatedSkips += 1;
        } else {
          updates.push({ id: rid, fields: intranetFields(row) });
        }
        continue;
      }
      if (delSet.has(rid)) {
        skips += 1;
        continue;
      }
      handleNoId(row); // 服务端不存在且不在墓碑：按无 ID 处理
      continue;
    }
    handleNoId(row); // 无 record_id，或同 record_id 的重复行
  }
  return { updates, news, conflicts, skips, adjudicated: adjudicatedSkips };
}

module.exports = {
  DISCOVERY_COLS,
  HANDLE_COLS,
  normStr,
  normDateStr,
  parseSectionPoles,
  parseIntranetSheet,
  snapshotOf,
  intranetFields,
  diffRecords,
};
