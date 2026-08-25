// 派车单每日自动同步：每日固定时点经 KVM 文件传输链路取回被控机导出的「yyyy-mm-dd-派车单.xlsx」，
// 按本班成员匹配自动建出车卡片。仅配置班组生效（WORKLOG_DISPATCH_SYNC_TEAM，目前仅检修一班，其他班组不开启）。
// 每日流程（北京时间，时区换算同 sgccclockin 固定 UTC+8 口径）：
//   09:25 准备：锁定设备（devlock 维护锁，远程连接/文件传输对该设备暂停）→ 设备 /mount 把 U 盘挂载（共享）至被控机端
//   09:30 被控机脚本导出派车单到 U 盘（scripts/export_dispatch_orders.py，被控机本机 crontab，不在本服务范围）
//   09:40 取件：下载当日文件（设备 fileshare 下载内部先 ensure_unshared，自动把 U 盘切回 KVM 侧）
//         → 删除 U 盘中该文件 → 解析建卡 → 解禁设备 → 结果通知（每日必发：超管 + 本班班组管理员）
// 建卡口径（用户指定）：A 列车牌（剔除「(蓝)/(绿)」等颜色后缀）、E 列用车人、J 列目的地；导出件仅含当日派车单无需读时间；
//   用车人串「含有」本班启用成员姓名（子串）的记录保留；同一人同日只挂一张卡，多条记录含同一人时由匹配班组人名最多的记录胜出；
//   车牌须本班启用字典精确命中、目的地按 destSame 模糊命中，未命中不建卡仅入通知（人工补字典后建卡）
const XLSX = require('xlsx');
const axios = require('axios');
const config = require('../config');
const { pool } = require('../db');
const glkvm = require('../kvm/glkvm');
const devlock = require('../kvm/devlock');
const { destSame, loadEntries } = require('./dispatch');

const LOCK_REASON = '每日派车单同步（预计 09:45 恢复）';
const CN_OFFSET_MS = 8 * 60 * 60 * 1000; // 北京时间固定偏移（UTC+8）
const RETRY_GAP_MS = 60 * 1000; // 设备调用失败重试间隔（两跳转发偶发抖动，共试 3 次）

function cfg() {
  return config.worklog.dispatchSync;
}

// 同步任务代登平台所用账号（留空回退管理员账号；须在 GLKVM 平台可见该设备组）
function kvmUser() {
  return cfg().kvmUser || config.admin.username;
}

// 北京日历日 YYYY-MM-DD（排程与文件名口径一致）
function todayCn() {
  return new Date(Date.now() + CN_OFFSET_MS).toISOString().slice(0, 10);
}

// 与 sgccclockin 同型：按北京时间 hh:mm 算下次触发 UTC 时间戳，今日已过顺延次日
function nextDailyRunUtc(hh, mm) {
  const now = Date.now();
  const cn = new Date(now + CN_OFFSET_MS); // 其 UTC 年月日即北京日历日
  let nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate(), hh, mm, 0) - CN_OFFSET_MS;
  if (nextUtc <= now) {
    nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate() + 1, hh, mm, 0) - CN_OFFSET_MS;
  }
  return { nextUtc, now };
}

// 生效班组：仅配置班组名（sys_team 启用中），其他班组不开启
async function resolveTeam() {
  const [rows] = await pool.query(
    'SELECT id, name FROM sys_team WHERE name = ? AND status = 1 LIMIT 1',
    [cfg().team]
  );
  if (!rows.length) throw new Error(`班组「${cfg().team}」不存在或已停用`);
  return rows[0];
}

// 目标设备：按配置 ddns 在同步账号可见设备中精确匹配；MAC 归一化校验（防设备更换后误操作他机）
async function findDevice() {
  const items = await glkvm.listDevices({}, kvmUser());
  const dev = items.find((d) => String(d.ddns || '') === cfg().deviceDdns);
  if (!dev) {
    throw new Error(`未找到设备 ddns=${cfg().deviceDdns}（账号 ${kvmUser()} 平台不可见或设备已下线）`);
  }
  const want = String(cfg().deviceMac || '').replace(/[^0-9a-f]/gi, '').toLowerCase();
  if (want) {
    const got = String(dev.mac || '').replace(/[^0-9a-f]/gi, '').toLowerCase();
    if (got !== want) {
      throw new Error(`设备 ${cfg().deviceDdns} MAC 校验不符（平台侧 ${dev.mac || '空'} ≠ 配置 ${cfg().deviceMac}），已中止防误操作`);
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
      console.error(`[派车单同步] ${label} 第 ${attempt}/3 次失败：`, err.message);
      if (attempt < 3) await new Promise((r) => setTimeout(r, RETRY_GAP_MS));
    }
  }
  throw lastErr;
}

// 挂载（共享）U 盘至被控机端
async function mountToHost(dev) {
  await deviceCall(dev, '挂载 U 盘至被控机', (ps) =>
    axios.post(`${ps.origin}/api/fileshare/mount`, null, {
      headers: { Cookie: ps.cookie }, timeout: 60000,
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

// 删除 U 盘内文件
async function deleteFile(dev, name) {
  await deviceCall(dev, `删除 ${name}`, (ps) =>
    axios.post(`${ps.origin}/api/fileshare/delete`, { names: [name] }, {
      headers: { Cookie: ps.cookie }, timeout: 60000,
    })
  );
}

/* ===== 解析与建卡计划（纯函数，不触库，便于本地自测） ===== */

// 必需列固定列位（被控机导出脚本生成口径，用户指定 A/E/J）：A=车牌号码 C=驾驶员 E=用车人 J=目的地 M=派车单号 N=派车单状态
const COL = { plate: 0, driver: 2, members: 4, to: 9, orderNo: 12, state: 13 };

// 车牌剔除颜色后缀：导出件形如「晋JBA773(蓝)」「晋JF56855(绿)」（全/半角括号均剔除）
function stripPlateColor(plate) {
  return String(plate || '').replace(/[（(]\s*(蓝|绿|黄|白)\s*[）)]/g, '').trim();
}

// 解析导出表 → { rows, dropped }；剔除「已取消」与剔除颜色后车牌为空的行（计数进通知）
function parseOrders(buffer) {
  const wb = XLSX.read(buffer, { type: 'buffer' });
  const rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
  if (!rows.length) throw new Error('导出表为空');
  const header = (rows[0] || []).map((c) => String(c).trim());
  if (header[COL.plate] !== '车牌号码' || header[COL.members] !== '用车人' || header[COL.to] !== '目的地') {
    throw new Error('导出表格式与预期不符（A 列应为车牌号码、E 列应为用车人、J 列应为目的地）');
  }
  const out = [];
  let dropped = 0;
  for (let i = 1; i < rows.length; i += 1) {
    const cells = (Array.isArray(rows[i]) ? rows[i] : []).map((c) => String(c).trim());
    if (cells.every((c) => !c)) continue; // 空行
    if (cells[COL.state] === '已取消') { dropped += 1; continue; }
    const plate = stripPlateColor(cells[COL.plate]);
    if (!plate) { dropped += 1; continue; }
    out.push({
      plate,
      driver: cells[COL.driver] || '',
      userText: cells[COL.members] || '', // 用车人原始串（空格分隔，偶有连写）
      to: cells[COL.to] || '',
      orderNo: cells[COL.orderNo] || '',
      state: cells[COL.state] || '',
    });
  }
  return { rows: out, dropped };
}

// 建卡计划（纯函数）：
// · 每条记录 matched = 本班启用成员中姓名被用车人串「含有」（子串）者；0 匹配计入 outside（非本班用车）
// · 候选按 matched 人数降序（并列按表内行序）贪心：同一人同日只挂一张卡（同日已有卡片成员经 assignedIds 预置），
//   多条记录含同一人时由匹配人数最多的记录胜出；一条记录仅挂上尚未分配的成员
// · 车牌须本班启用字典精确命中、目的地按 destSame 模糊命中（目的地为空不约束）；未命中不建卡、不消耗成员，记入 skippedDict
function planCards(rows, teamMembers, vehicles, destinations, assignedIds) {
  const assigned = new Set(assignedIds);
  const withMatch = rows.map((row, idx) => ({
    row,
    idx,
    matched: teamMembers.filter((m) => m.name && row.userText.includes(m.name)),
  }));
  const outside = withMatch.filter((c) => c.matched.length === 0).length;
  const candidates = withMatch
    .filter((c) => c.matched.length > 0)
    .sort((a, b) => b.matched.length - a.matched.length || a.idx - b.idx);

  const plans = [];
  const skippedDict = [];
  for (const c of candidates) {
    const vehicle = vehicles.find((v) => v.plate_no === c.row.plate) || null;
    const dest = c.row.to ? destinations.find((d) => destSame(d.name, c.row.to)) || null : null;
    if (!vehicle || (c.row.to && !dest)) {
      skippedDict.push({ ...c.row, reason: !vehicle ? '车牌未入字典' : '目的地未入字典' });
      continue;
    }
    const members = c.matched.filter((m) => !assigned.has(m.id));
    if (!members.length) continue; // 本记录匹配的成员均已挂卡
    members.forEach((m) => assigned.add(m.id));
    plans.push({ vehicle, destination: dest, members, row: c.row });
  }
  return { plans, skippedDict, outside };
}

/* ===== 库操作与通知 ===== */

// 解析 + 计划 + 落库建卡 → 汇总对象（created/skippedDict/outside/dropped/total）
async function importOrders(team, buffer, today) {
  const { rows, dropped } = parseOrders(buffer);
  const [members] = await pool.query(
    'SELECT id, name, sort FROM worklog_member WHERE status = 1 AND team_id = ? ORDER BY sort, id',
    [team.id]
  );
  const [vehicles] = await pool.query(
    'SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ?',
    [team.id]
  );
  const [destinations] = await pool.query(
    'SELECT id, name FROM worklog_destination WHERE status = 1 AND team_id = ?',
    [team.id]
  );
  // 同日已挂卡成员（未出车卡无成员天然不含），同日期同人唯一与 POST /logs 口径一致
  const entries = await loadEntries(team.id, today, today);
  const assignedIds = new Set();
  entries.forEach((e) => e.memberList.forEach((m) => assignedIds.add(m.member_id)));

  const { plans, skippedDict, outside } = planCards(rows, members, vehicles, destinations, assignedIds);

  // 系统建卡 created_by 取管理员账号（缺省回退首个超管）
  const [creator] = await pool.query(
    "SELECT id FROM sys_user WHERE username = ? OR role = 'admin' ORDER BY (username = ?) DESC, id LIMIT 1",
    [config.admin.username, config.admin.username]
  );
  if (!creator.length) throw new Error('未找到管理员账号作为建卡人');
  const createdBy = creator[0].id;

  const created = [];
  for (const p of plans) {
    const [r] = await pool.query(
      'INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, created_by) VALUES (?, ?, ?, ?, ?, ?)',
      [team.id, today, '', p.vehicle.id, p.destination ? p.destination.id : null, createdBy]
    );
    for (const m of p.members) {
      await pool.query(
        'INSERT INTO worklog_entry_member (entry_id, member_id, sort) VALUES (?, ?, ?)',
        [r.insertId, m.id, m.sort]
      );
    }
    created.push({
      id: r.insertId,
      plate: p.vehicle.plate_no,
      destination: p.destination ? p.destination.name : '',
      members: p.members.map((m) => m.name),
    });
  }
  return { created, skippedDict, outside, dropped, total: rows.length };
}

// 同步结果通知：超管 + 本班启用班组管理员（teamId 空时仅超管，如班组解析失败场景）；通知失败仅记日志
async function notify(title, lines, teamId) {
  try {
    const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
    let teamAdmins = [];
    if (teamId) {
      [teamAdmins] = await pool.query(
        "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id = ? AND status = 1",
        [teamId]
      );
    }
    const userIds = [...new Set([...admins.map((a) => a.id), ...teamAdmins.map((a) => a.id)])];
    if (!userIds.length) return;
    await require('../notice').push({ userIds, targets: [], title, content: lines.join('\n') }); // 与 sgccclockin 同型惰性加载
  } catch (err) {
    console.error('[派车单同步] 通知发送失败：', err.message);
  }
}

/* ===== 每日任务 ===== */

// 09:25 准备：锁定设备 → U 盘挂载至被控机；失败立即解禁并通知（取件任务仍按自身排程执行）
async function prepareJob() {
  let team = null;
  try {
    team = await resolveTeam();
    const dev = await findDevice();
    await devlock.lock(dev.ddns, LOCK_REASON);
    console.log(`[派车单同步] 设备 ${dev.ddns} 已锁定，正在挂载 U 盘至被控机 …`);
    await mountToHost(dev);
    console.log('[派车单同步] U 盘已挂载至被控机端，等待被控机导出');
  } catch (err) {
    console.error('[派车单同步] 准备任务失败：', err.message);
    await devlock.unlock(cfg().deviceDdns);
    await notify('派车单同步准备失败', [
      `每日准备任务（锁定设备并挂载 U 盘至被控机）失败：${err.message}`,
      '设备已解禁；今日取件任务仍将按排程尝试执行。',
    ], team && team.id);
  }
}

let fetching = false; // 取件重入守卫（定时与手动触发共用）

// 09:40 取件：下载 → 删除 U 盘文件 → 解析建卡 → 解禁（finally 保证）→ 每日结果通知；返回结果对象供手动触发回显
async function fetchJob() {
  if (fetching) return { ok: false, message: '已有取件任务在执行中，请稍后再试' };
  fetching = true;
  const today = todayCn();
  const fileName = `${today}-派车单.xlsx`;
  let team = null;
  try {
    team = await resolveTeam();
    const dev = await findDevice();
    const buf = await downloadFile(dev, fileName);
    if (!buf) {
      const lines = [
        `${today} 未在设备 U 盘中找到「${fileName}」。`,
        '可能为当日无派车单，或被控机导出脚本异常（排障见 scripts/README.md）。',
      ];
      await notify('派车单同步：今日无文件', lines, team.id);
      return { ok: true, message: lines.join('\n') };
    }
    console.log(`[派车单同步] 已下载 ${fileName}（${(buf.length / 1024).toFixed(1)} KB）`);
    try {
      await deleteFile(dev, fileName); // 按口径：下载完毕即删除 U 盘中派车单文件
    } catch (err) {
      console.error('[派车单同步] U 盘文件删除失败（文件已下载，继续建卡）：', err.message);
    }
    const r = await importOrders(team, buf, today);
    const lines = [
      `${today} 派车单同步完成：解析 ${r.total} 行（剔除 ${r.dropped} 行已取消/无车牌、${r.outside} 行非本班用车人），新建 ${r.created.length} 张出车卡片。`,
    ];
    r.created.forEach((c) =>
      lines.push(`· ${c.plate}${c.destination ? ` → ${c.destination}` : ''}：${c.members.join('、')}`)
    );
    if (r.skippedDict.length) {
      lines.push('以下记录因字典未命中未建卡，请将车牌/目的地先入字典后人工建卡：');
      r.skippedDict.forEach((s) =>
        lines.push(`· ${s.plate}${s.to ? ` → ${s.to}` : ''}（用车人：${s.userText}）：${s.reason}`)
      );
    }
    await notify('派车单同步完成', lines, team.id);
    console.log(`[派车单同步] ${today} 完成：新建 ${r.created.length} 张出车卡片`);
    return { ok: true, message: lines.join('\n') };
  } catch (err) {
    console.error('[派车单同步] 取件任务失败：', err.message);
    const lines = [
      `${today} 取件任务失败：${err.message}`,
      '设备已解禁。可人工在出工日志「派车对齐」导入导出件，或由字典管理员调 POST /api/v1/worklog/dispatch/sync-now 补跑。',
    ];
    await notify('派车单同步失败', lines, team && team.id);
    return { ok: false, message: lines.join('\n') };
  } finally {
    fetching = false;
    await devlock.unlock(cfg().deviceDdns); // 无论成败解禁（锁另有 2h TTL 兜底）
  }
}

/* ===== 排程（与 sgccclockin 同型：setTimeout 自排程 + unref，北京时间口径） ===== */

function scheduleDaily(timeStr, job, label) {
  const [hh, mm] = String(timeStr || '').split(':').map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(Number.isInteger(hh) ? hh : 9, Number.isInteger(mm) ? mm : 0);
  const timer = setTimeout(async () => {
    try {
      await job();
    } catch (err) {
      console.error(`[派车单同步] ${label}执行异常：`, err.message);
    }
    scheduleDaily(timeStr, job, label); // 排次日
  }, nextUtc - now);
  timer.unref(); // 不阻塞进程退出
  console.log(`[派车单同步] ${label}已排程：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`);
}

// 由 worklog 入口在满足条件时调用（WORKLOG_ENABLED 门控挂载 + KVM_ENABLED + WORKLOG_DISPATCH_SYNC_ENABLED）
function start() {
  if (!cfg().team || !cfg().deviceDdns) {
    console.error('[派车单同步] 未配置生效班组或设备 ddns（WORKLOG_DISPATCH_SYNC_TEAM / WORKLOG_DISPATCH_DEVICE_DDNS），同步停用');
    return;
  }
  scheduleDaily(cfg().prepareTime, prepareJob, '每日准备（锁定并挂载 U 盘）');
  scheduleDaily(cfg().fetchTime, fetchJob, '每日取件（下载建卡解禁）');
}

// 管理端手动触发（验证/补跑）：立即执行取件建卡，返回结果对象
async function runFetchNow() {
  return fetchJob();
}

module.exports = { start, runFetchNow };
