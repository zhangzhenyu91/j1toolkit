// 交跨统计表内网 U 盘同步编排（设备链路仿 worklog/dispatch-sync.js 与 treehazard/sync.js，diff/导出复用本模块纯函数）
// 同步对象：cfg().syncTeam 指定班组的全量记录 ↔ 被控机导出的「{班组名}交跨统计表.xlsx」（O 列记录ID 为行级比对锚点）
// 每日流程（北京时间，时区换算同 sgccclockin 固定 UTC+8 口径；可跨午夜）：
//   23:50 准备：锁定设备（devlock 维护锁）→ push snapshot 指令（crossing_sync_cmd.json，告知被控机导出内网表）
//         → 设备 /mount 把 U 盘挂载（共享）至被控机端（本地自排程，与 dispatch-sync 同型）
//   00:00 取件（挂 nightq 夜间队列，与其他夜间任务同点串行）：下载内网表（下载内部自动把 U 盘切回 KVM 侧）
//         → diff 比对回写（内网改动吸收：updates 覆盖 / news 插入 source='intranet'；双边都改 → 冲突留档待人工裁决；
//         裁决 keep=server 的行若内网旧表未收敛——行与已裁决快照全等 → 驳回旧值不回灌防反转，判待收敛触发下发）
//         → 无差异：重挂回共享态收尾；有差异（含内网无表）：删旧表 → push 最新表 + replace 指令（不等被控机取件确认）→ 重挂
//         → 解禁设备（finally 保证）→ 写 sync_log + 结果通知（超管 + 本班班组管理员）
// 另有手动触发（POST /sync/push-now）：完整轮次（准备 → 等指令窗口 CROSSING_SYNC_CMD_WINDOW_MIN 分钟 → 取件）
// 指令文件 crossing_sync_cmd.json：{ version:1, cycle_id, kind:'snapshot'|'replace', file:'{班组名}交跨统计表.xlsx', issued_at(+08:00) }
const FormData = require('form-data');
const axios = require('axios');
const config = require('../config');
const { pool } = require('../db');
const glkvm = require('../kvm/glkvm');
const devlock = require('../kvm/devlock');
const nightq = require('../utils/nightq');
const { nextDailyRunUtc, CN_OFFSET_MS } = require('../utils/cndate');
const exporter = require('./export');
const differ = require('./diff');

const LOCK_REASON = '交跨统计表同步中（预计 10 分钟内恢复）';
const RETRY_GAP_MS = 60 * 1000; // 设备调用失败重试间隔（两跳转发偶发抖动，共试 3 次）
const CMD_FILE = 'crossing_sync_cmd.json';
const KIND_NAME = { manual: '手动', idle: '空闲', night: '夜间' };

function cfg() {
  return config.crossing;
}

// 同步任务代登平台所用账号（留空回退管理员账号；须在 GLKVM 平台可见该设备组）
function kvmUser() {
  return cfg().syncKvmUser || config.admin.username;
}

// 同步班组：按配置班组名精确匹配启用班组
async function resolveTeam() {
  const [rows] = await pool.query('SELECT id, name FROM sys_team WHERE name = ? AND status = 1', [
    cfg().syncTeam,
  ]);
  if (!rows.length) throw new Error(`未找到同步班组「${cfg().syncTeam}」（未建或已停用）`);
  return rows[0];
}

// 目标设备：按配置 ddns 在同步账号可见设备中精确匹配；MAC 归一化校验（防设备更换后误操作他机）
async function findDevice() {
  const items = await glkvm.listDevices({}, kvmUser());
  const dev = items.find((d) => String(d.ddns || '') === cfg().syncDeviceDdns);
  if (!dev) {
    throw new Error(
      `未找到设备 ddns=${cfg().syncDeviceDdns}（账号 ${kvmUser()} 平台不可见或设备已下线）`
    );
  }
  const want = String(cfg().syncDeviceMac || '')
    .replace(/[^0-9a-f]/gi, '')
    .toLowerCase();
  if (want) {
    const got = String(dev.mac || '')
      .replace(/[^0-9a-f]/gi, '')
      .toLowerCase();
    if (got !== want) {
      throw new Error(
        `设备 ${cfg().syncDeviceDdns} MAC 校验不符（平台侧 ${dev.mac || '空'} ≠ 配置 ${cfg().syncDeviceMac}），已中止防误操作`
      );
    }
  }
  return dev;
}

// 设备 fileshare 调用统一出口：每次调用换新代理会话，失败重试 2 次（共 3 次）、间隔 1 分钟
async function deviceCall(dev, label, fn) {
  let lastErr;
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      const ps = await glkvm.getProxySession(dev.id, kvmUser());
      return await fn(ps);
    } catch (err) {
      lastErr = err;
      console.error(`[交跨同步] ${label} 第 ${attempt}/3 次失败：`, err.message);
      if (attempt < 3) await new Promise((r) => setTimeout(r, RETRY_GAP_MS));
    }
  }
  throw lastErr;
}

// 挂载（共享）U 盘至被控机端
async function mountToHost(dev) {
  await deviceCall(dev, '挂载 U 盘至被控机', (ps) =>
    axios.post(`${ps.origin}/api/fileshare/mount`, null, {
      headers: { Cookie: ps.cookie },
      timeout: 60000,
    })
  );
}

// 下载 U 盘内文件；文件不存在返回 null（区别于链路故障抛错）
async function downloadFile(dev, name) {
  return deviceCall(dev, `下载 ${name}`, async (ps) => {
    const r = await axios.get(`${ps.origin}/api/fileshare/download/${encodeURIComponent(name)}`, {
      headers: { Cookie: ps.cookie },
      responseType: 'arraybuffer',
      timeout: 120000,
      validateStatus: (s) => s === 200 || s === 404,
    });
    if (r.status === 404) return null;
    return Buffer.from(r.data);
  });
}

// 列 U 盘内文件（设备 /list，{files:[{name,size}]}；共享中会先断开，重挂由调用方负责）
async function listFiles(dev) {
  return deviceCall(dev, '列 U 盘文件', async (ps) => {
    const r = await axios.get(`${ps.origin}/api/fileshare/list`, {
      headers: { Cookie: ps.cookie },
      timeout: 60000,
    });
    return (r.data && r.data.files) || [];
  });
}

// 删除 U 盘内文件（names 数组一次删多个）
async function deleteFiles(dev, names) {
  await deviceCall(dev, `删除 ${names.join('、')}`, (ps) =>
    axios.post(
      `${ps.origin}/api/fileshare/delete`,
      { names },
      {
        headers: { Cookie: ps.cookie },
        timeout: 60000,
      }
    )
  );
}

// 推送文件到 U 盘（设备 /push 内部自动断开共享 → 写入 → 保持非共享；挂载由 mountToHost 单独触发）
async function pushFile(dev, name, buf) {
  await deviceCall(dev, `推送 ${name}`, (ps) => {
    const fd = new FormData();
    fd.append('files', buf, { filename: name });
    return axios.post(`${ps.origin}/api/fileshare/push`, fd, {
      headers: { ...fd.getHeaders(), Cookie: ps.cookie },
      maxBodyLength: Infinity,
      maxContentLength: Infinity,
      timeout: 120000,
    });
  });
}

// 取件收尾清扫（口径：同步完不保留本操作产物）：删 U 盘根目录全部交跨表残留
// （任意「*交跨统计表.xlsx」——现名/旧名/夜间兜底名）与指令文件残留；仅根目录不递归，
// 不动派车单与用户经文件传输存放的文件。best-effort：失败仅记日志不阻断同步（下一轮收尾再清）。
async function cleanupUsb(dev) {
  try {
    const files = await listFiles(dev);
    const victims = files
      .map((f) => String((f && f.name) || ''))
      .filter((n) => n.endsWith('交跨统计表.xlsx') || n === CMD_FILE);
    if (victims.length) {
      await deleteFiles(dev, victims);
      console.log(`[交跨同步] 已清扫 U 盘残留：${victims.join('、')}`);
    }
  } catch (err) {
    console.error('[交跨同步] U 盘残留清扫失败（继续收尾）：', err.message);
  }
}

/* ===== 指令文件与轮次 ===== */

// 北京墙钟文本工具（cn 的 UTC 字段即北京墙钟）
function cnParts() {
  const cn = new Date(Date.now() + CN_OFFSET_MS);
  const p = (n) => String(n).padStart(2, '0');
  return { cn, p };
}

// 同步轮次标识：yyyyMMdd-HHmm-xxxx（北京时间 + 4 位随机），指令文件与日志定位用
function newCycleId() {
  const { cn, p } = cnParts();
  const stamp = `${cn.getUTCFullYear()}${p(cn.getUTCMonth() + 1)}${p(cn.getUTCDate())}-${p(cn.getUTCHours())}${p(cn.getUTCMinutes())}`;
  return `${stamp}-${Math.random().toString(36).slice(2, 6)}`;
}

// 指令文件内容：snapshot 告知被控机「请导出内网表到 U 盘」；replace 告知「U 盘里的表已是最新，取用覆盖」
function cmdPayload(cycle, kind, fileName) {
  const { cn, p } = cnParts();
  const issuedAt = `${cn.getUTCFullYear()}-${p(cn.getUTCMonth() + 1)}-${p(cn.getUTCDate())}T${p(cn.getUTCHours())}:${p(cn.getUTCMinutes())}:${p(cn.getUTCSeconds())}+08:00`;
  return { version: 1, cycle_id: cycle, kind, file: fileName, issued_at: issuedAt };
}

// 准备：push snapshot 指令 → 挂载 U 盘至被控机端（设备锁由调用方加）
async function preparePhase(dev, team, cycle) {
  const cmd = Buffer.from(
    JSON.stringify(cmdPayload(cycle, 'snapshot', exporter.exportFileName(team.name))),
    'utf8'
  );
  await pushFile(dev, CMD_FILE, cmd);
  await mountToHost(dev);
  console.log(`[交跨同步] 已下发 snapshot 指令并挂载 U 盘（轮次 ${cycle}），等待被控机导出`);
}

/* ===== 比对回写与下发 ===== */

// 本班服务端记录全量（diff 与导出共用口径：13 业务列 + pole/同步点时间戳）
async function loadServerRecords(teamId) {
  const [rows] = await pool.query(
    `SELECT id, team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
       dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
       finder_name, DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark,
       synced_at, created_at, updated_at
     FROM crossing_record WHERE team_id = ?`,
    [teamId]
  );
  return rows;
}

// 差异应用（db 形参供自测注入 stub）：
// updates 内网值覆盖 13 业务列（+synced_at，WHERE 带 team_id 防越班）；
// news 以 source='intranet' 插入（crane 双 NULL，pole 从区段文本解析，synced_at=NOW）；
// conflicts 落 crossing_conflict 留档（人工二选一解决，本轮不改记录）
async function applyDiff(db, teamId, result) {
  for (const u of result.updates || []) {
    const f = u.fields;
    await db.query(
      `UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, crossing_type = ?,
         dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
         finder_name = ?, inspect_at = ?, remark = ?, synced_at = NOW()
       WHERE id = ? AND team_id = ?`,
      [
        f.voltage_level,
        f.line_name,
        f.section,
        f.crossing_type,
        f.dist_small,
        f.dist_big,
        f.cross_horizontal,
        f.cross_vertical,
        f.cross_angle,
        f.has_hazard,
        f.finder_name,
        f.inspect_at,
        f.remark,
        u.id,
        teamId,
      ]
    );
  }
  for (const row of result.news || []) {
    const f = differ.intranetFields(row);
    const [poleFrom, poleTo] = differ.parseSectionPoles(row.section);
    await db.query(
      `INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
         dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
         finder_name, inspect_at, remark, source, synced_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'intranet', NOW())`,
      [
        teamId,
        f.voltage_level,
        f.line_name,
        f.section,
        poleFrom,
        poleTo,
        f.crossing_type,
        f.dist_small,
        f.dist_big,
        f.cross_horizontal,
        f.cross_vertical,
        f.cross_angle,
        f.has_hazard,
        f.finder_name,
        f.inspect_at,
        f.remark,
      ]
    );
  }
  for (const c of result.conflicts || []) {
    await db.query(
      'INSERT INTO crossing_conflict (team_id, record_id, server_snapshot, intranet_snapshot) VALUES (?, ?, ?, ?)',
      [teamId, c.id, JSON.stringify(c.server), JSON.stringify(c.intranet)]
    );
  }
}

function toTime(v) {
  if (!v) return NaN;
  const t = new Date(v).getTime();
  return Number.isNaN(t) ? NaN : t;
}

// JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 crossing/index.js parseJson 同口径）
function parseJson(raw) {
  if (!raw) return null;
  if (typeof raw === 'string') {
    try {
      return JSON.parse(raw);
    } catch {
      return null;
    }
  }
  return raw;
}

// 下发判定（纯函数）：吸收了内网改动（更新/新增）、有已裁决驳回待收敛的行，
// 或服务端存在「同步点之后又改过 / 从未同步」的记录 → 需下发最新表
function decideAction(result, serverRecords) {
  const absorbed = (result.updates || []).length + (result.news || []).length;
  if (absorbed > 0) return { needPush: true, reason: `吸收内网改动 ${absorbed} 条` };
  const adjudicated = result.adjudicated || 0;
  // 裁决 keep=server 的行被 diff 拦下未回灌：须下发最新表让内网收敛（收敛后下一轮自然相等 skip）
  if (adjudicated > 0) return { needPush: true, reason: `裁决待收敛 ${adjudicated} 条` };
  const changed = (serverRecords || []).filter((r) => {
    const syncedAt = toTime(r.synced_at);
    if (Number.isNaN(syncedAt)) return true; // 从未同步
    const updatedAt = toTime(r.updated_at);
    return !Number.isNaN(updatedAt) && updatedAt > syncedAt;
  });
  if (changed.length) return { needPush: true, reason: `服务端变更 ${changed.length} 条` };
  return { needPush: false, reason: '无差异' };
}

// 下发后落同步点：全量记录 synced_at=NOW()；pending 冲突行除外（保人工裁决语义——
// 代价是冲突未决每轮仍判服务端变更触发下发，可接受）
async function markSyncedAfterPush(db, teamId) {
  await db.query(
    `UPDATE crossing_record SET synced_at = NOW()
     WHERE team_id = ? AND id NOT IN (
       SELECT record_id FROM crossing_conflict WHERE team_id = ? AND status = 'pending' AND record_id IS NOT NULL)`,
    [teamId, teamId]
  );
}

// 下发最新表：buildWorkbook 内部按单表口径排序（与人工导出 split=false 同口径：
// 电压降序>线路名升序>区段升序）→ push 表文件 + replace 指令 → 重挂回共享态（不等被控机取件确认）
async function pushLatest(dev, team, cycle, records) {
  const fileName = exporter.exportFileName(team.name);
  const wb = await exporter.buildWorkbook({ teamName: team.name, records });
  const buf = Buffer.from(await wb.xlsx.writeBuffer());
  await pushFile(dev, fileName, buf);
  const cmd = Buffer.from(JSON.stringify(cmdPayload(cycle, 'replace', fileName)), 'utf8');
  await pushFile(dev, CMD_FILE, cmd);
  await mountToHost(dev); // 重挂回共享态：被控机在窗口内可取最新表
  console.log(`[交跨同步] 已下发最新表（${records.length} 行）与 replace 指令（轮次 ${cycle}）`);
}

// 取件与比对回写：下载内网表 → diff（带已裁决驳回快照防反转）→ 应用 → 收尾清盘 → 视差异决定收尾或下发最新表；
// 收尾顺序统一「先清扫、后下发」：除在途交付（新表 + replace 指令，客户端取走即删）外 U 盘零残留；
// 返回 { updates, news, conflicts, skips, adjudicated, pushed, noFile }（计数供摘要）
async function fetchPhase(dev, team, cycle) {
  const fileName = exporter.exportFileName(team.name);
  const serverRecords = await loadServerRecords(team.id);
  const [tomb] = await pool.query('SELECT id FROM crossing_deleted_id WHERE team_id = ?', [
    team.id,
  ]);
  const deletedIds = tomb.map((r) => r.id);

  const buf = await downloadFile(dev, fileName);
  if (!buf) {
    // 内网无表（首次同步/被控机未导出）：清残留后直接下发最新表（含空表），内网以服务端为准起步
    console.log(`[交跨同步] U 盘未找到「${fileName}」，直接下发最新表（轮次 ${cycle}）`);
    await cleanupUsb(dev);
    await pushLatest(dev, team, cycle, serverRecords);
    await markSyncedAfterPush(pool, team.id);
    return {
      updates: 0,
      news: 0,
      conflicts: 0,
      skips: 0,
      adjudicated: 0,
      pushed: true,
      noFile: true,
    };
  }
  console.log(`[交跨同步] 已下载 ${fileName}（${(buf.length / 1024).toFixed(1)} KB）`);
  const rows = differ.parseIntranetSheet(buf);
  // 已裁决驳回（keep=server）的冲突快照：裁决后内网旧表若未收敛（行与快照全等），驳回旧值不回灌防反转裁决（全部即可，量小）
  const [resolved] = await pool.query(
    `SELECT record_id, intranet_snapshot FROM crossing_conflict
     WHERE team_id = ? AND status = 'resolved' AND resolution = 'server' AND record_id IS NOT NULL`,
    [team.id]
  );
  const adjudicated = resolved
    .map((r) => ({ record_id: Number(r.record_id), intranet: parseJson(r.intranet_snapshot) }))
    .filter((a) => a.intranet);
  const result = differ.diffRecords(serverRecords, rows, { deletedIds, adjudicated });
  await applyDiff(pool, team.id, result);
  const action = decideAction(result, serverRecords);
  const counts = {
    updates: result.updates.length,
    news: result.news.length,
    conflicts: result.conflicts.length,
    skips: result.skips,
    adjudicated: result.adjudicated || 0,
    pushed: false,
    noFile: false,
  };
  if (!action.needPush) {
    // 无差异：消费完的表连同残留一并清盘（best-effort）后重挂回共享态收尾
    await cleanupUsb(dev);
    await mountToHost(dev);
    console.log(`[交跨同步] 无差异，已清盘并重挂回共享态（轮次 ${cycle}）`);
    return counts;
  }
  // 有差异：清盘（消费完的旧表 + 残留）→ 下发最新表 + replace 指令
  await cleanupUsb(dev);
  const fresh = await loadServerRecords(team.id); // 回写后的最新全量
  await pushLatest(dev, team, cycle, fresh);
  await markSyncedAfterPush(pool, team.id);
  counts.pushed = true;
  console.log(`[交跨同步] 轮次 ${cycle} 差异下发原因：${action.reason}`);
  return counts;
}

/* ===== 摘要 / 日志 / 通知 ===== */

// 结果摘要：'新增n 更新n 冲突n [裁决收敛n ]已下发' / '… 无差异'（裁决收敛段仅在 >0 时出现）；失败 '失败：原因'
function buildSummary(counts, err) {
  if (err) return `失败：${err.message || err}`;
  const parts = [`新增${counts.news}`, `更新${counts.updates}`, `冲突${counts.conflicts}`];
  if (counts.adjudicated) parts.push(`裁决收敛${counts.adjudicated}`);
  if (counts.pushed) parts.push(counts.noFile ? '内网无表·已下发' : '已下发');
  else parts.push('无差异');
  return parts.join(' ');
}

// 写同步日志 + 结果通知（超管 + 本班班组管理员；失败标题「交跨统计表同步失败」）；通知/日志失败仅记日志
async function logAndNotify(team, kind, { ok: success, text }) {
  try {
    await pool.query('INSERT INTO crossing_sync_log (team_id, kind, result) VALUES (?, ?, ?)', [
      team.id,
      kind,
      text,
    ]);
  } catch (err) {
    console.error('[交跨同步] 同步日志写入失败：', err.message);
  }
  try {
    await require('../notice').push({
      // 与 dispatch-sync 同型惰性加载
      targets: ['admin', 'team_admin'],
      title: success ? `交跨统计表同步完成（${team.name}）` : '交跨统计表同步失败',
      content: `方式：${KIND_NAME[kind] || kind}\n${text}`,
      wxTeamIds: [team.id],
    });
  } catch (err) {
    console.error('[交跨同步] 结果通知失败：', err.message);
  }
}

/* ===== 轮次编排 ===== */

let running = false; // 同步重入守卫（夜间/手动共用）

// 完整同步轮次（手动用）：锁 → 准备 → 等指令窗口 → 取件比对回写/下发 → 解锁（finally 保证）→ 日志通知；
// 全 catch 不抛；返回 { ok, message }
async function runCycle(kind) {
  if (running) return { ok: false, message: '同步进行中，请稍后再试' };
  if (!cfg().syncDeviceDdns) return { ok: false, message: '未配置同步设备' };
  running = true;
  const cycle = newCycleId();
  let team = null;
  try {
    team = await resolveTeam();
    const dev = await findDevice();
    await devlock.lock(dev.ddns, LOCK_REASON);
    try {
      await preparePhase(dev, team, cycle);
      console.log(`[交跨同步] 等待指令窗口 ${cfg().syncCmdWindowMin} 分钟（轮次 ${cycle}）…`);
      await new Promise((r) => setTimeout(r, cfg().syncCmdWindowMin * 60 * 1000));
      const counts = await fetchPhase(dev, team, cycle);
      const text = buildSummary(counts);
      console.log(`[交跨同步] 轮次 ${cycle} 完成：${text}`);
      await logAndNotify(team, kind, { ok: true, text });
      return { ok: true, message: text };
    } finally {
      await devlock.unlock(dev.ddns); // 无论成败解禁（锁另有 2h TTL 兜底）
    }
  } catch (err) {
    console.error(`[交跨同步] 轮次 ${cycle} 失败：`, err.message);
    const text = buildSummary(null, err);
    if (team) await logAndNotify(team, kind, { ok: false, text });
    return { ok: false, message: text };
  } finally {
    running = false;
  }
}

let nightCtx = null; // 夜间轮次上下文 { cycle, dev, team }（23:50 准备存，次日 00:00 取件用）

// 23:50 夜间准备：锁 + 准备并存上下文；失败立即解禁并通知（取件任务仍按自身排程执行——同 dispatch-sync 口径）
async function nightPrepareJob() {
  let team = null;
  try {
    team = await resolveTeam();
    const dev = await findDevice();
    await devlock.lock(dev.ddns, LOCK_REASON);
    const cycle = newCycleId();
    try {
      await preparePhase(dev, team, cycle);
      nightCtx = { cycle, dev, team };
    } catch (err) {
      await devlock.unlock(dev.ddns);
      throw err;
    }
  } catch (err) {
    console.error('[交跨同步] 夜间准备失败：', err.message);
    if (team)
      await logAndNotify(team, 'night', { ok: false, text: `失败：准备阶段 ${err.message}` });
  }
}

// 夜间取件（nightq 调用）：取回内网表比对回写/下发 → 解锁（finally 保证）→ 日志通知；
// nightCtx 丢失（进程重启/准备失败）时重建轮次照跑取件兜底（设备未锁时 unlock 为空操作无害）
async function nightFetchJob() {
  if (running) {
    console.log('[交跨同步] 已有同步在执行，夜间取件跳过');
    return;
  }
  running = true;
  let team = null;
  try {
    let ctx = nightCtx;
    nightCtx = null;
    if (!ctx) {
      team = await resolveTeam();
      ctx = { cycle: newCycleId(), dev: await findDevice(), team };
      console.log(`[交跨同步] 夜间上下文缺失，按轮次 ${ctx.cycle} 重建（准备阶段可能未执行）`);
    } else {
      team = ctx.team;
    }
    try {
      const counts = await fetchPhase(ctx.dev, ctx.team, ctx.cycle);
      const text = buildSummary(counts);
      console.log(`[交跨同步] 夜间轮次 ${ctx.cycle} 完成：${text}`);
      await logAndNotify(team, 'night', { ok: true, text });
    } finally {
      await devlock.unlock(ctx.dev.ddns);
    }
  } catch (err) {
    console.error('[交跨同步] 夜间取件失败：', err.message);
    if (team) await logAndNotify(team, 'night', { ok: false, text: buildSummary(null, err) });
  } finally {
    running = false;
  }
}

/* ===== 触发与排程 ===== */

// 本地每日自排程（同 dispatch-sync：setTimeout 自排程 + unref，北京时间口径）；仅用于夜间准备，
// 取件挂 nightq 按时刻分组串行
function scheduleDaily(timeStr, job, label) {
  const [hh, mm] = String(timeStr || '')
    .split(':')
    .map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(
    Number.isInteger(hh) ? hh : 23,
    Number.isInteger(mm) ? mm : 50
  );
  const timer = setTimeout(async () => {
    try {
      await job();
    } catch (err) {
      console.error(`[交跨同步] ${label}执行异常：`, err.message);
    }
    scheduleDaily(timeStr, job, label); // 排次日
  }, nextUtc - now);
  timer.unref(); // 不阻塞进程退出
  console.log(
    `[交跨同步] ${label}已排程：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`
  );
}

// 由 crossing 入口调用（未配置设备 ddns 时停用不排程）
function start() {
  if (!cfg().syncDeviceDdns) {
    console.error('[交跨同步] 未配置同步设备 ddns（CROSSING_SYNC_DEVICE_DDNS），内网 U 盘同步停用');
    return;
  }
  scheduleDaily(cfg().syncPrepareTime, nightPrepareJob, '每日准备（锁定并下发 snapshot 指令）');
  nightq.register('crossing-fetch', cfg().syncFetchTime, nightFetchJob);
  console.log(
    `[交跨同步] 已启用：每日 ${cfg().syncPrepareTime} 准备 / ${cfg().syncFetchTime} 取件（北京时间），另支持手动 POST /sync/push-now 即时同步`
  );
}

// 同步状态：enabled（已配置设备）、running、last（本班最近一条 sync_log）、nextNight（下次夜间取件时间，
// 北京时间 ISO 字符串；未启用为 null）
async function status() {
  const enabled = Boolean(cfg().syncDeviceDdns);
  let last = null;
  if (enabled) {
    try {
      const [rows] = await pool.query(
        `SELECT l.kind, l.result, l.created_at FROM crossing_sync_log l
         JOIN sys_team t ON t.id = l.team_id WHERE t.name = ? ORDER BY l.id DESC LIMIT 1`,
        [cfg().syncTeam]
      );
      if (rows.length) last = rows[0];
    } catch (err) {
      console.error('[交跨同步] 状态查询失败：', err.message);
    }
  }
  // 下次夜间取件时间（按 syncFetchTime 排程口径换算，转北京时间 ISO 字符串）
  let nextNight = null;
  if (enabled) {
    const [hh, mm] = String(cfg().syncFetchTime || '')
      .split(':')
      .map((s) => parseInt(s, 10));
    const { nextUtc } = nextDailyRunUtc(
      Number.isInteger(hh) ? hh : 0,
      Number.isInteger(mm) ? mm : 0
    );
    nextNight = new Date(nextUtc + CN_OFFSET_MS).toISOString().replace('Z', '+08:00');
  }
  return { enabled, running, last, nextNight };
}

function isRunning() {
  return running;
}

module.exports = {
  start,
  runCycle,
  status,
  isRunning,
  // 纯逻辑/库操作导出供自测 stub 演练
  decideAction,
  buildSummary,
  applyDiff,
  markSyncedAfterPush,
};
