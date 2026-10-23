// 交跨台账 WeKnora 知识库同步（快照语义，按线路一表 xlsx，内容哈希增量 + 删旧传新，机制同 treehazard/weknorasync.js）
// 目的：Call Me 问答可直接引用台账。同步对象与内网同步同班组口径（config.crossing.syncTeam）的全部记录
// （删除即物理删不再出现），按 line_name 一线路一表（空名归「未分组线路」）——复用 export.js buildWorkbook
// 单表口径出表（列序/隐藏记录ID 列与整表导出一致，组内排序沿用 sortRecords 口径，表内标题「{线路名}交跨统计表」），
// 文件名「{线路名}.xlsx」（safeFileName 净化非法字符），以 fileName=「交跨台账/{线路名}.xlsx」上传——
// 路径即文件夹，无需再调 folder 归类。
// 增量判定=内容哈希：每组表的记录集内容（sortRecords 排序后序列化）算 sha256 与本地状态文件比对，未变跳过
// 不请求 WeKnora；变了/新线路删旧传新（哈希不取 xlsx buffer——zip 条目时间戳随当前时间每 2 秒一变、
// 同数据 buffer 字节不稳，实测踩坑；docProps 时间戳仍固定仅为产物可比对）。
// 更新语义：文件上传同名 409，无原地更新；删除异步「正在删除的同名文件不计为重复」，删后即传不会 409；
// 仍 409 取响应已有 id 删除后重试一次。上传成功把 data.id 存进状态。
// 状态文件 server/data/crossing/weknora-sync-state.json（只写数据卷，tmp+rename 原子写，仿 safeday/store.js）；
// 缺失/损坏回退空状态=全量重传一轮自愈，进程重启丢状态=全量重传一轮，可接受；runSync 入口重入保护（isRunning），
// 状态读写只在同步流程内发生。创建带 channel='crossing-sync' 渠道标记，兜底清理只删自己传的，非本渠道文档不动。
// 触发两源：夜间 nightq（与 crossing-fetch 同时刻串行——本模块在 crossing/index.js 里于 sync.start()
// 之后注册，保证夜间取件回写完再同步知识库）/ 手动 POST /weknora-sync/push-now（班管/超管）。
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const config = require('../config');
const { pool } = require('../db');
const nightq = require('../utils/nightq');
const exporter = require('./export');
const weknora = require('../services/weknora');

const FOLDER_PATH = '交跨台账'; // 知识库文件夹名（代码常量；fileName 路径前缀即文件夹，无需预创建）
const CHANNEL = 'crossing-sync'; // 摄取渠道标记（列表过滤 source=，孤儿清理只删本渠道）
const UNGROUPED_LINE = '未分组线路'; // line_name 为空的记录归组名
const PAGE_SIZE = 100; // 列知识分页大小（batch-delete 单次上限 200，清理分批另切）
const LIST_MAX_PAGES = 10; // 列知识翻页兜底上限（防接口分页异常死循环）
const STATE_FILE = path.join(__dirname, '..', '..', 'data', 'crossing', 'weknora-sync-state.json');

function cfg() {
  return config.weknora;
}

// 同步班组：按配置班组名精确匹配启用班组（与 sync.js resolveTeam 同口径）
async function resolveTeam() {
  const [rows] = await pool.query('SELECT id, name FROM sys_team WHERE name = ? AND status = 1', [
    config.crossing.syncTeam,
  ]);
  if (!rows.length)
    throw new Error(`未找到同步班组「${config.crossing.syncTeam}」（未建或已停用）`);
  return rows[0];
}

// WeKnora 响应信封解包（{success, data, ...}）；success 显式为 false 时抛错
function unwrap(body, label) {
  if (!body || body.success === false) {
    throw new Error(
      `${label}失败：${(body && (body.message || body.error)) || 'WeKnora 响应异常'}`
    );
  }
  return body.data;
}

/* ===== 本地状态（哈希增量；tmp+rename 原子写，仿 safeday/store.js） ===== */

// 读状态文件（缺失/损坏回退空状态——下一轮全量重传自愈）
function loadState() {
  try {
    const st = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
    if (st && st.version === 2 && st.lines && typeof st.lines === 'object') return st;
  } catch (e) {
    /* 缺失或损坏回退空状态 */
  }
  // version=2：知识库表去首行标题（skipTitle）口径；旧版状态（带标题旧件）作废，首轮全量重传替换
  return { version: 2, lines: {} };
}

// 原子写状态文件（先写临时文件再 rename，防崩溃截断读到半成品）
function saveState(state) {
  fs.mkdirSync(path.dirname(STATE_FILE), { recursive: true });
  const tmp = `${STATE_FILE}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, `${JSON.stringify(state, null, 2)}\n`, 'utf8');
  fs.renameSync(tmp, STATE_FILE);
}

/* ===== xlsx 产物 ===== */

// 内容哈希：对 sortRecords 排序后的记录集序列化算 sha256（排序口径与表内行序一致，DB 行序无关）；
// 不哈希 xlsx buffer——zip 条目时间戳随当前时间每 2 秒一变，同数据 buffer 字节不稳（实测踩坑），
// docProps 时间戳已固定仅为产物可比对。记录内容不变 ⇒ 表内容不变；导出格式升级后需删状态文件强制全量重传
function recordsHash(records) {
  const canonical = exporter.sortRecords(records, exporter.exportGroupComparator);
  return crypto.createHash('sha256').update(JSON.stringify(canonical)).digest('hex');
}

// 载入同步班组全部记录，按 line_name 一线路一表 [{ lineName, fileName, buffer, hash }]（空名归 UNGROUPED_LINE；
// buildWorkbook 内部按 sortRecords 口径排序，单表列序/隐藏记录ID 列与整表导出一致；文件名 safeFileName 净化 + .xlsx）
async function buildLineBooks(team) {
  const [rows] = await pool.query(
    `SELECT id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
       dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
       finder_name, DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark, created_at
     FROM crossing_record WHERE team_id = ?`,
    [team.id]
  );
  const groups = new Map();
  for (const r of rows) {
    const name = String(r.line_name ?? '').trim() || UNGROUPED_LINE;
    if (!groups.has(name)) groups.set(name, []);
    groups.get(name).push(r);
  }
  const books = [];
  for (const [lineName, records] of groups) {
    const wb = await exporter.buildWorkbook({ teamName: lineName, records, skipTitle: true }); // 知识库表去首行标题（其余导出保留）
    wb.created = new Date(0); // 固定 docProps 时间戳：否则 exceljs 每次写当前时间，同数据产物字节不可比对
    wb.modified = new Date(0);
    const buffer = Buffer.from(await wb.xlsx.writeBuffer());
    books.push({
      lineName,
      fileName: `${exporter.safeFileName(lineName)}.xlsx`,
      buffer,
      hash: recordsHash(records),
    });
  }
  return books;
}

/* ===== 同步流程 ===== */

// 拉「交跨台账」文件夹内本渠道全部条目（分页兜底；非本渠道由 source 过滤天然排除）
async function listFolderKnowledge(kbId) {
  const out = [];
  for (let page = 1; page <= LIST_MAX_PAGES; page += 1) {
    const body = await weknora.listKnowledge(kbId, {
      folder_path: FOLDER_PATH,
      folder_recursive: 'false',
      source: CHANNEL,
      page,
      page_size: PAGE_SIZE,
    });
    const data = unwrap(body, '列知识');
    const items = Array.isArray(data) ? data : (data && (data.items || data.list)) || [];
    out.push(...items);
    if (items.length < PAGE_SIZE) break;
  }
  return out;
}

// 批量删（≤200/次分批）；失败抛错由调用方记 errors
async function batchDelete(kbId, ids) {
  for (let i = 0; i < ids.length; i += 200) {
    const chunk = ids.slice(i, i + 200);
    unwrap(await weknora.batchDeleteKnowledge(kbId, chunk), '删知识');
  }
}

// 列表条目文件名（file_name/fileName/title 字段兼容取值；可能带 folder_path 前缀，取末段基名）
function knowledgeBaseName(k) {
  const raw = String((k && (k.file_name || k.fileName || k.title)) ?? '').trim();
  return raw.slice(raw.lastIndexOf('/') + 1);
}

// 上传 409 响应里已有条目 id（data 形态未实测：单对象 / 数组 / {items|list} 列表均兼容）
function conflictIds(data) {
  if (!data) return [];
  const arr = Array.isArray(data)
    ? data
    : Array.isArray(data.items)
      ? data.items
      : Array.isArray(data.list)
        ? data.list
        : [data];
  return arr
    .map((k) => (k && typeof k === 'object' ? (k.id ?? k.knowledge_id) : k))
    .filter((id) => id !== undefined && id !== null);
}

// 上传线路表（同名 409 → 取响应已有 id 删除后重试一次，再失败抛错由调用方记 errors）；
// 返回 { conflictDeleted, knowledgeId }——conflictDeleted=409 兜底删除的旧件数，knowledgeId=上传响应 data.id（缺省 null）
async function uploadXlsx(kbId, book) {
  const payload = {
    buffer: book.buffer,
    fileName: `${FOLDER_PATH}/${book.fileName}`,
    title: book.fileName,
    channel: CHANNEL,
  };
  const doUpload = async () => {
    const data = unwrap(await weknora.uploadKnowledgeFile(kbId, payload), '上传知识文件');
    return (data && (data.id ?? data.knowledge_id)) || null;
  };
  try {
    return { conflictDeleted: 0, knowledgeId: await doUpload() };
  } catch (err) {
    const body = err && err.response && err.response.data;
    if (!body || err.response.status !== 409) throw err;
    const ids = conflictIds(body.data);
    if (!ids.length) throw err; // 409 但响应里拿不到已有 id，无法自愈
    for (const id of ids) {
      unwrap(await weknora.deleteKnowledge(id), '删同名旧知识');
    }
    return { conflictDeleted: ids.length, knowledgeId: await doUpload() };
  }
}

// 同步主流程：逐线路哈希比对 → 未变跳过 / 变了删旧传新（新线路直接传）→ 已下线路删条目清状态项 →
// 末尾兜底清理本渠道孤儿（状态丢失/旧版整表残留）；单步失败记入 errors 不中断整批。
// 返回 { ok, created, updated, deleted, skipped, errors[] }
async function syncToWeKnora() {
  const kbId = cfg().crossingKbId;
  const errors = [];
  let created = 0;
  let updated = 0;
  let deleted = 0;
  let skipped = 0;
  const team = await resolveTeam();
  const books = await buildLineBooks(team);
  const state = loadState();
  const currentLines = new Set(books.map((b) => b.lineName)); // 本次有记录的线路（已下线路判定）
  const currentNames = new Set(books.map((b) => b.fileName)); // 本次文件名集合（兜底清理保留判定）
  const removedIds = new Set(); // 本轮已单删的条目 id（防异步删除残留被兜底误再删）
  for (const book of books) {
    const st = state.lines[book.lineName];
    if (st && st.hash === book.hash) {
      skipped += 1; // 哈希未变：不请求 WeKnora
      continue;
    }
    // 删旧传新（新线路无旧件直接传）；删除失败 best-effort 继续走上传（409 兜底可自愈）
    let deleteErrMsg = '';
    if (st && st.knowledgeId) {
      try {
        unwrap(await weknora.deleteKnowledge(st.knowledgeId), '删旧知识');
        removedIds.add(st.knowledgeId);
        deleted += 1;
      } catch (err) {
        deleteErrMsg = `删除旧「${book.fileName}」失败：${err.message}`;
        console.warn('[交跨知识库同步]', deleteErrMsg);
      }
    }
    try {
      const { conflictDeleted, knowledgeId } = await uploadXlsx(kbId, book);
      deleted += conflictDeleted;
      state.lines[book.lineName] = {
        hash: book.hash,
        knowledgeId: knowledgeId || (st && st.knowledgeId) || null,
        fileName: book.fileName,
        syncedAt: new Date().toISOString(),
      };
      if (st) updated += 1;
      else created += 1;
    } catch (err) {
      errors.push(
        `${deleteErrMsg ? `${deleteErrMsg}；` : ''}上传「${book.fileName}」失败：${err.message}`
      );
    }
  }
  // 已下线路清理：状态里有、本次无记录的线路 → 删条目 + 清状态项（删除失败保留下轮重试）
  for (const [lineName, st] of Object.entries(state.lines)) {
    if (currentLines.has(lineName)) continue;
    if (st.knowledgeId) {
      try {
        unwrap(await weknora.deleteKnowledge(st.knowledgeId), '删已下线知识');
        removedIds.add(st.knowledgeId);
        deleted += 1;
      } catch (err) {
        errors.push(`清理已下线路「${lineName}」失败：${err.message}`);
        continue; // 状态项保留，下轮重试
      }
    }
    delete state.lines[lineName];
  }
  // 兜底清理：本渠道在册但既不属本次线路文件名、也不在状态 knowledgeId 集里的条目
  // （状态丢失/旧版整表「{班组名}交跨统计表.xlsx」残留）→ batch-delete
  try {
    const listed = await listFolderKnowledge(kbId);
    const stateIds = new Set(
      Object.values(state.lines)
        .map((s) => s.knowledgeId)
        .filter((id) => id !== undefined && id !== null)
    );
    const orphans = [];
    const seen = new Set();
    for (const k of listed) {
      const id = k && k.id;
      if (id === undefined || id === null || seen.has(id) || removedIds.has(id)) continue;
      seen.add(id);
      if (currentNames.has(knowledgeBaseName(k)) || stateIds.has(id)) continue;
      orphans.push(id);
    }
    if (orphans.length) {
      await batchDelete(kbId, orphans);
      deleted += orphans.length;
    }
  } catch (err) {
    errors.push(`清理孤儿条目失败：${err.message}`);
  }
  saveState(state);
  return { ok: errors.length === 0, created, updated, deleted, skipped, errors };
}

/* ===== 触发与排程 ===== */

let running = false; // 手动触发重入守卫（夜间 nightq 同刻串行本身不重入）

// 手动/夜间共用一轮同步：全 catch 不抛，返回 { ok, created, updated, deleted, skipped, errors[] }
async function runSync() {
  if (running)
    return {
      ok: false,
      created: 0,
      updated: 0,
      deleted: 0,
      skipped: 0,
      errors: ['同步进行中，请稍后再试'],
    };
  running = true;
  try {
    return await syncToWeKnora();
  } catch (err) {
    console.error('[交跨知识库同步] 同步失败：', err.message);
    return { ok: false, created: 0, updated: 0, deleted: 0, skipped: 0, errors: [err.message] };
  } finally {
    running = false;
  }
}

function isRunning() {
  return running;
}

// 夜间任务（nightq 调用）：结果仅记日志
async function nightJob() {
  const r = await runSync();
  const summary = `新建 ${r.created} / 更新 ${r.updated} / 跳过 ${r.skipped} / 清理 ${r.deleted}${r.errors.length ? `，失败 ${r.errors.length} 项` : ''}`;
  if (r.ok) console.log(`[交跨知识库同步] 夜间完成：${summary}`);
  else console.error(`[交跨知识库同步] 夜间完成（有失败）：${summary}`, r.errors);
}

// 由 crossing 入口在 sync.start() 之后调用（同时刻任务按登记序串行：先内网取件回写，再同步知识库）；
// 未配置知识库 ID 或 WeKnora API Key 时停用不排程
function start() {
  if (!cfg().crossingKbId || !cfg().apiKey) {
    console.error(
      '[交跨知识库同步] 未配置 WeKnora（WEKNORA_CROSSING_KB_ID / WEKNORA_API_KEY），知识库同步停用'
    );
    return;
  }
  nightq.register('crossing-weknora-sync', config.crossing.syncFetchTime, nightJob);
  console.log(
    `[交跨知识库同步] 已启用：每日 ${config.crossing.syncFetchTime}（北京时间，随内网取件同刻串行）按线路同步至知识库「${FOLDER_PATH}」文件夹，另支持手动 POST /weknora-sync/push-now 即时同步`
  );
}

module.exports = {
  start,
  runSync,
  isRunning,
};
