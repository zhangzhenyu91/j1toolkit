// 树障台账 WeKnora 知识库同步（快照语义，全量重生成 Markdown，按线路一组一文档，见 开发指南.md 19.7）
// 目的：Call Me 问答可直接引用台账。同步对象与内网同步同班组口径（cfg().syncTeam）的全部记录（含已处理行；
// 删除即物理删不再出现），按 line_name 分组一文档（空线路名归「未分组线路」），归入知识库「树木台账」文件夹。
// 文件夹由文档 folder_path 推导、平级可扩展（后续其他台账用各自 channel + 文件夹共存）；
// 创建带 channel='treehazard-sync' 渠道标记，孤儿清理只删自己传的，非本渠道文档不动。
// 触发两源：夜间 nightq（与 treehazard-fetch 同时刻串行——本模块在 treehazard/index.js 里于 sync.start()
// 之后注册，保证夜间取件回写完再同步知识库）/ 手动 POST /weknora-sync/push-now（班管/超管）。
const config = require('../config');
const { pool } = require('../db');
const nightq = require('../utils/nightq');
const { CN_OFFSET_MS } = require('../utils/cndate');
const exporter = require('./export');
const weknora = require('../services/weknora');

const FOLDER_PATH = '树木台账'; // 知识库文件夹名（代码常量；文件夹无需预创建）
const CHANNEL = 'treehazard-sync'; // 摄取渠道标记（列表过滤 source=，孤儿清理只删本渠道）
const UNGROUPED_LINE = '未分组线路'; // line_name 为空的记录归组名
const PAGE_SIZE = 100; // 列知识分页大小（batch-delete 单次上限 200，清理分批另切）
const LIST_MAX_PAGES = 10; // 列知识翻页兜底上限（防接口分页异常死循环）

function cfg() {
  return config.weknora;
}

// 同步班组：按配置班组名精确匹配启用班组（与 sync.js resolveTeam 同口径）
async function resolveTeam() {
  const [rows] = await pool.query('SELECT id, name FROM sys_team WHERE name = ? AND status = 1', [config.treehazard.syncTeam]);
  if (!rows.length) throw new Error(`未找到同步班组「${config.treehazard.syncTeam}」（未建或已停用）`);
  return rows[0];
}

// WeKnora 响应信封解包（{success, data, ...}）；success 显式为 false 时抛错
function unwrap(body, label) {
  if (!body || body.success === false) {
    throw new Error(`${label}失败：${(body && (body.message || body.error)) || 'WeKnora 响应异常'}`);
  }
  return body.data;
}

/* ===== Markdown 渲染（纯函数，快照全量重生成） ===== */

// 北京墙钟文本（cn 的 UTC 字段即北京墙钟，同 sync.js cnParts 口径）
function cnNowText() {
  const cn = new Date(Date.now() + CN_OFFSET_MS);
  const p = (n) => String(n).padStart(2, '0');
  return `${cn.getUTCFullYear()}-${p(cn.getUTCMonth() + 1)}-${p(cn.getUTCDate())} ${p(cn.getUTCHours())}:${p(cn.getUTCMinutes())}:${p(cn.getUTCSeconds())}`;
}

// 单条记录 Markdown 小节：区段/杆号标题 + 字段列表（空值字段跳过；处理状态恒带，处理信息有则带上）
function renderRecord(r, idx) {
  const title = String(r.section || '').trim() || '未填区段';
  const from = r.pole_from === null || r.pole_from === undefined ? '' : String(r.pole_from);
  const to = r.pole_to === null || r.pole_to === undefined ? '' : String(r.pole_to);
  let poles = '';
  if (from && to) poles = `（杆号 ${from}-${to}）`;
  else if (from) poles = `（杆号 ${from} 起）`;
  else if (to) poles = `（杆号 ${to} 止）`;
  const lines = [`## ${idx}. ${title}${poles}`, ''];
  const put = (label, v) => {
    const s = String(v ?? '').trim();
    if (s) lines.push(`- ${label}：${s}`);
  };
  put('位置备注', r.position_note);
  put('树种', r.tree_species);
  put('数量', r.tree_count);
  const ds = String(r.dist_small ?? '').trim();
  const db = String(r.dist_big ?? '').trim();
  if (ds || db) lines.push(`- 距小号侧（m）：${ds || '—'}，距大号侧（m）：${db || '—'}`);
  put('直径（cm）', r.diameter);
  const h = String(r.horizontal ?? '').trim();
  const v = String(r.vertical ?? '').trim();
  if (h || v) lines.push(`- 水平距离（m）：${h || '—'}，垂直距离：${v || '—'}`);
  const finder = String(r.finder_name || '').trim();
  const foundAt = String(r.found_at || '').trim();
  if (finder || foundAt) lines.push(`- 发现人：${finder || '—'}，发现时间：${foundAt || '—'}`);
  const handled = r.status === 'handled';
  const hs = [];
  const handler = String(r.handler_name || '').trim();
  const handledAt = String(r.handled_at || '').trim();
  const note = String(r.handle_note || '').trim();
  if (handler) hs.push(`处理人：${handler}`);
  if (handledAt) hs.push(`处理时间：${handledAt}`);
  if (note) hs.push(`处理情况：${note}`);
  lines.push(`- 处理状态：${handled ? '已处理' : '未处理'}${hs.length ? `（${hs.join('，')}）` : ''}`);
  return lines;
}

// 线路文档 Markdown：首行摘要（标题/班组/记录数/生成时间 北京时间）+ 逐条记录小节
function renderDoc({ lineName, teamName, voltage, records, generatedAt }) {
  const lines = [`# ${lineName}树木台账`, '', `- 班组：${teamName}`];
  const vol = String(voltage ?? '').trim();
  if (vol) lines.push(`- 电压等级：${vol}`);
  lines.push(`- 记录数：${records.length}`);
  lines.push(`- 生成时间：${generatedAt}（北京时间）`);
  records.forEach((r, i) => {
    lines.push('');
    lines.push(...renderRecord(r, i + 1));
  });
  return lines.join('\n');
}

// 按 line_name 归组（空名归 UNGROUPED_LINE），组序沿用排序后首现序（team_line 导入序 + 未匹配按线路名中文序）
function groupByLineName(records) {
  const groups = new Map();
  for (const r of records) {
    const name = String(r.line_name ?? '').trim() || UNGROUPED_LINE;
    if (!groups.has(name)) groups.set(name, []);
    groups.get(name).push(r);
  }
  return [...groups.entries()].map(([title, recs]) => ({ title, records: recs }));
}

// 载入同步班组全部记录并生成线路文档清单 [{title, content}]（title=线路名；空组不生成）
async function buildDocs() {
  const team = await resolveTeam();
  const [lineRows, [rows]] = await Promise.all([
    exporter.fetchLineOrder(pool, team.id),
    pool.query(
      `SELECT voltage_level, line_name, section, pole_from, pole_to, position_note,
         tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical,
         finder_name, DATE_FORMAT(found_at, '%Y-%m-%d') AS found_at,
         handler_name, handle_note, DATE_FORMAT(handled_at, '%Y-%m-%d') AS handled_at,
         status, created_at
       FROM treehazard_record WHERE team_id = ?`,
      [team.id]
    ),
  ]);
  const lineOrder = new Map(lineRows.map((l, i) => [l.line_name, i]));
  const sorted = exporter.sortRecords(rows, exporter.makeLineGroupComparator(lineOrder));
  const generatedAt = cnNowText();
  return groupByLineName(sorted).map((g) => ({
    title: g.title,
    content: renderDoc({
      lineName: g.title,
      teamName: team.name,
      voltage: g.records.map((r) => r.voltage_level).find((v) => String(v ?? '').trim()) || '',
      records: g.records,
      generatedAt,
    }),
  }));
}

/* ===== 同步流程 ===== */

// 拉「树木台账」文件夹内本渠道全部条目（分页兜底；非本渠道由 source 过滤天然排除）
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
async function deleteKnowledge(kbId, ids) {
  for (let i = 0; i < ids.length; i += 200) {
    const chunk = ids.slice(i, i + 200);
    unwrap(await weknora.batchDeleteKnowledge(kbId, chunk), '删知识');
  }
}

// 同步主流程（快照语义）：list 建 title→id 映射 → 每个线路文档原地更新或新建并归入文件夹 →
// 本渠道存在但本次不再生成的文档（线路已无记录）批量清理；单文档失败记入 errors 不中断整批。
// 返回 { ok, created, updated, deleted, errors[] }
async function syncToWeKnora() {
  const kbId = cfg().treehazardKbId;
  const errors = [];
  let created = 0;
  let updated = 0;
  let deleted = 0;
  const docs = await buildDocs();
  // 现有条目 title→id 映射（同名多条留首条，多余条目走孤儿清理）
  const existing = new Map();
  const listed = await listFolderKnowledge(kbId);
  for (const k of listed) {
    const id = k && k.id;
    if (id === undefined || id === null) continue;
    if (!existing.has(k.title)) existing.set(k.title, id);
  }
  const touchedIds = new Set(); // 本次创建或尝试更新的条目 id（更新失败的保留下轮再更，不清理）
  for (const doc of docs) {
    const id = existing.get(doc.title);
    if (id !== undefined) {
      touchedIds.add(id);
      try {
        unwrap(await weknora.updateManualKnowledge(id, { title: doc.title, content: doc.content, status: 'publish' }), '更新知识');
        updated += 1;
      } catch (err) {
        errors.push(`更新「${doc.title}」失败：${err.message}`);
      }
      continue;
    }
    try {
      const knowledge = unwrap(
        await weknora.createManualKnowledge(kbId, { title: doc.title, content: doc.content, status: 'publish', channel: CHANNEL }),
        '建知识'
      );
      const newId = knowledge && knowledge.id;
      if (newId === undefined || newId === null) throw new Error('响应缺少条目 id');
      touchedIds.add(newId);
      try {
        unwrap(await weknora.moveKnowledgeToFolder(kbId, [newId], FOLDER_PATH), '归入文件夹');
      } catch (err) {
        // 归类失败回滚删除新建条目，防残留文件夹外成重复（下一轮重试重建）
        await deleteKnowledge(kbId, [newId]).catch(() => {});
        throw new Error(`归入文件夹失败：${err.message}`);
      }
      created += 1;
    } catch (err) {
      errors.push(`新建「${doc.title}」失败：${err.message}`);
    }
  }
  // 孤儿清理：本渠道在册但本次未触达的条目（线路已无记录 / 同名冗余）
  const orphans = [];
  const seen = new Set();
  for (const k of listed) {
    const id = k && k.id;
    if (id === undefined || id === null || touchedIds.has(id) || seen.has(id)) continue;
    seen.add(id);
    orphans.push(id);
  }
  if (orphans.length) {
    try {
      await deleteKnowledge(kbId, orphans);
      deleted += orphans.length;
    } catch (err) {
      errors.push(`清理孤儿条目失败：${err.message}`);
    }
  }
  return { ok: errors.length === 0, created, updated, deleted, errors };
}

/* ===== 触发与排程 ===== */

let running = false; // 手动触发重入守卫（夜间 nightq 同刻串行本身不重入）

// 手动/夜间共用一轮同步：全 catch 不抛，返回 { ok, created, updated, deleted, errors[] }
async function runSync() {
  if (running) return { ok: false, created: 0, updated: 0, deleted: 0, errors: ['同步进行中，请稍后再试'] };
  running = true;
  try {
    return await syncToWeKnora();
  } catch (err) {
    console.error('[树障知识库同步] 同步失败：', err.message);
    return { ok: false, created: 0, updated: 0, deleted: 0, errors: [err.message] };
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
  const summary = `新建 ${r.created} / 更新 ${r.updated} / 清理 ${r.deleted}${r.errors.length ? `，失败 ${r.errors.length} 项` : ''}`;
  if (r.ok) console.log(`[树障知识库同步] 夜间完成：${summary}`);
  else console.error(`[树障知识库同步] 夜间完成（有失败）：${summary}`, r.errors);
}

// 由 treehazard 入口在 sync.start() 之后调用（同时刻任务按登记序串行：先内网取件回写，再同步知识库）；
// 未配置知识库 ID 或 WeKnora API Key 时停用不排程
function start() {
  if (!cfg().treehazardKbId || !cfg().apiKey) {
    console.error('[树障知识库同步] 未配置 WeKnora（WEKNORA_TREEHAZARD_KB_ID / WEKNORA_API_KEY），知识库同步停用');
    return;
  }
  nightq.register('treehazard-weknora-sync', config.treehazard.syncFetchTime, nightJob);
  console.log(`[树障知识库同步] 已启用：每日 ${config.treehazard.syncFetchTime}（北京时间，随内网取件同刻串行）同步至知识库「${FOLDER_PATH}」文件夹，另支持手动 POST /weknora-sync/push-now 即时同步`);
}

module.exports = {
  start,
  runSync,
  isRunning,
  // 渲染/归组纯函数导出供自测演练
  renderDoc,
  renderRecord,
  groupByLineName,
};
