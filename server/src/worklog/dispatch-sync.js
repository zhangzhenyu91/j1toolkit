// 派车单每日自动同步：每日固定时点经 KVM 文件传输链路取回被控机导出的「yyyy-mm-dd-派车单.xlsx」，
// 按各开启班组的成员匹配自动建出车卡片。生效班组以 worklog_dispatch_sync_team 开关表为准
//（班组管理员/超管在「派车对齐」页按班组开关；env WORKLOG_DISPATCH_SYNC_TEAM 仅作首次启动种子）。
// 每日流程（北京时间，时区换算同 sgccclockin 固定 UTC+8 口径）：
//   09:10 准备：锁定设备（devlock 维护锁，远程连接/文件传输对该设备暂停）→ 设备 /mount 把 U 盘挂载（共享）至被控机端
//   09:15 被控机桌面客户端（内网工具箱，独立项目，不在本仓）导出派车单到 U 盘
//   09:20 取件：下载当日文件（设备 fileshare 下载内部先 ensure_unshared，自动把 U 盘切回 KVM 侧）
//         → 删除 U 盘中该文件 → 解析建卡（派车单号随卡写入 worklog_entry.dispatch_order_no，供「派车汇总」导出）
//         → 解禁设备 → 结果通知（每日必发：按班组分发，超管 + 对应班组管理员）
// 建卡口径（用户指定）：A 列车牌（剔除「(蓝)/(绿)」等颜色后缀）、E 列用车人、J 列目的地；
//   行日期按 F 列预计用车时间（T 列创建时间兜底）判定，非当日行清洗剔除（导出件可能混入之前日期的派车单）；
//   用车人串「含有」本班启用成员姓名（子串）的记录保留；同一人同日只挂一张卡，多条记录含同一人时由匹配班组人名最多的记录胜出；
//   用车人含多个班组人名（按全部启用班组成员字典判定，同名跨班多班都算）的记录仅通知不建卡（交由超管建跨班日志）；
//   用车人当日已有手动建卡（本班或别班/跨班卡）的记录不自动建卡/拆卡，改为与本班已有卡片比对（车牌/目的地/用车人），
//   差异附文字修改指引随通知发班组管理员与超管（全项一致仅计数）；
//   车牌须本班启用字典精确命中、目的地按 destSame 模糊命中，未命中不建卡仅入通知（人工补字典后建卡）
const XLSX = require('xlsx');
const axios = require('axios');
const config = require('../config');
const { pool } = require('../db');
const glkvm = require('../kvm/glkvm');
const devlock = require('../kvm/devlock');
const { destSame, loadEntries } = require('./dispatch');
const { todayCn, nextDailyRunUtc } = require('../utils/cndate');

const LOCK_REASON = '每日派车单同步（预计 09:45 恢复）';
const RETRY_GAP_MS = 60 * 1000; // 设备调用失败重试间隔（两跳转发偶发抖动，共试 3 次）

function cfg() {
  return config.worklog.dispatchSync;
}

// 同步任务代登平台所用账号（留空回退管理员账号；须在 GLKVM 平台可见该设备组）
function kvmUser() {
  return cfg().kvmUser || config.admin.username;
}

// 生效班组：开关表 worklog_dispatch_sync_team 中仍启用的班组（在表即开启；无开启班组时任务跳过）
async function resolveTeams() {
  const [rows] = await pool.query(
    `SELECT t.id, t.name FROM worklog_dispatch_sync_team s
     JOIN sys_team t ON t.id = s.team_id AND t.status = 1 ORDER BY t.sort, t.id`
  );
  return rows;
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

// 必需列固定列位（被控机导出脚本生成口径，用户指定 A/E/J）：A=车牌号码 C=驾驶员 E=用车人 F=预计用车时间 J=目的地 M=派车单号 N=派车单状态 T=创建时间
const COL = { plate: 0, driver: 2, members: 4, planTime: 5, to: 9, orderNo: 12, state: 13, createTime: 19 };

// 车牌剔除颜色后缀：导出件形如「晋JBA773(蓝)」「晋JF56855(绿)」（全/半角括号均剔除）
function stripPlateColor(plate) {
  return String(plate || '').replace(/[（(]\s*(蓝|绿|黄|白)\s*[）)]/g, '').trim();
}

// 行日期（同被控机导出脚本 _is_today_order 口径）：F 列预计用车时间优先、T 列创建时间兜底，
// 取首个可解析的 yyyy-mm-dd；两列均缺失/不可解析返回空串（调用方按非当日剔除）
function rowDate(cells) {
  for (const idx of [COL.planTime, COL.createTime]) {
    const m = String(cells[idx] || '').match(/(\d{4})-(\d{1,2})-(\d{1,2})/);
    if (m) return `${m[1]}-${m[2].padStart(2, '0')}-${m[3].padStart(2, '0')}`;
  }
  return '';
}

// 解析导出表 → { rows, dropped, stale }；剔除「已取消」、日期非当日、剔除颜色后车牌为空的行（计数进通知）
function parseOrders(buffer, today) {
  const wb = XLSX.read(buffer, { type: 'buffer' });
  const rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
  if (!rows.length) throw new Error('导出表为空');
  const header = (rows[0] || []).map((c) => String(c).trim());
  if (header[COL.plate] !== '车牌号码' || header[COL.members] !== '用车人' || header[COL.to] !== '目的地') {
    throw new Error('导出表格式与预期不符（A 列应为车牌号码、E 列应为用车人、J 列应为目的地）');
  }
  const out = [];
  let dropped = 0;
  let stale = 0;
  for (let i = 1; i < rows.length; i += 1) {
    const cells = (Array.isArray(rows[i]) ? rows[i] : []).map((c) => String(c).trim());
    if (cells.every((c) => !c)) continue; // 空行
    if (cells[COL.state] === '已取消') { dropped += 1; continue; }
    if (rowDate(cells) !== today) { stale += 1; continue; } // 清洗非当日派车单
    const plate = stripPlateColor(cells[COL.plate]);
    if (!plate) { dropped += 1; continue; }
    out.push({
      plate,
      driver: cells[COL.driver] || '',
      userText: cells[COL.members] || '', // 用车人原始串（空格分隔，偶有连写）
      to: cells[COL.to] || '',
      orderNo: cells[COL.orderNo] || '', // 派车单号（随卡写入 dispatch_order_no，派车汇总导出用）
      state: cells[COL.state] || '',
    });
  }
  return { rows: out, dropped, stale };
}

// 建卡计划（纯函数）：
// · 每条记录 matched = 本班启用成员中姓名被用车人串「含有」（子串）者；0 匹配计入 outside（非本班用车）
// · 候选按 matched 人数降序（并列按表内行序）贪心：同一人同日只挂一张卡（assignedIds 预置占用，可用于排除已有卡成员），
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

// 派车单号写入卡片（「派车汇总」导出字段）：仅写派车单号这一信息字段，不动车牌/目的地/用车人比对三件套；
// 空单号不写（汇总无据）；同日补跑/重跑为同值覆盖，幂等
async function saveOrderNo(entryId, row) {
  if (!row.orderNo) return;
  await pool.query('UPDATE worklog_entry SET dispatch_order_no = ? WHERE id = ?', [row.orderNo, entryId]);
}

// 解析 + 行级班组判定 + 逐班组计划落库/已有卡比对 → { results, outside, otherTeam, dropped, stale, total }
// results 每项 { team, created, skippedDict, cross, mismatches, aligned }：
// cross 为涉及该班但含多班人名的记录（仅通知不建卡）；mismatches 为已有手动建卡且比对不一致的记录（附修改指引），aligned 为一致计数
async function importOrdersMulti(teams, buffer, today) {
  const { rows, dropped, stale } = parseOrders(buffer, today);
  // 全部启用班组的启用成员（跨班判定口径；含未开启班组，避免给本班建出缺人的卡）
  const [allMembers] = await pool.query(
    `SELECT m.id, m.name, m.sort, m.team_id, t.name AS team_name
     FROM worklog_member m JOIN sys_team t ON t.id = m.team_id AND t.status = 1
     WHERE m.status = 1 ORDER BY m.team_id, m.sort, m.id`
  );
  // 行级 involvedTeams：用车人串子串含有的成员所属班组集合（同名跨班则多班都算）
  const rowTeams = rows.map((row) => {
    const set = new Set();
    allMembers.forEach((m) => { if (m.name && row.userText.includes(m.name)) set.add(m.team_id); });
    return set;
  });
  const enabledIds = new Set(teams.map((t) => t.id));
  const crossIdx = []; // 含多个班组人名的行序：仅通知不创建
  const byTeam = new Map(); // 开启班组 team_id → 候选行
  let outside = 0; // 不含任何班组成员
  let otherTeam = 0; // 仅含未开启班组成员
  rows.forEach((row, i) => {
    const set = rowTeams[i];
    if (set.size > 1) { crossIdx.push(i); return; }
    if (set.size === 0) { outside += 1; return; }
    const tid = [...set][0];
    if (!enabledIds.has(tid)) { otherTeam += 1; return; }
    if (!byTeam.has(tid)) byTeam.set(tid, []);
    byTeam.get(tid).push(row);
  });

  // 当日全局已挂卡成员（含别班/跨班卡；同日期同人唯一为全局口径，防止同步为已有卡成员重复建卡）
  const [occupiedRows] = await pool.query(
    `SELECT em.member_id FROM worklog_entry_member em JOIN worklog_entry e ON e.id = em.entry_id WHERE e.log_date = ?`,
    [today]
  );
  const occupiedGlobal = new Set(occupiedRows.map((r) => r.member_id));

  // 系统建卡 created_by 取管理员账号（缺省回退首个超管）
  const [creator] = await pool.query(
    "SELECT id FROM sys_user WHERE username = ? OR role = 'admin' ORDER BY (username = ?) DESC, id LIMIT 1",
    [config.admin.username, config.admin.username]
  );
  if (!creator.length) throw new Error('未找到管理员账号作为建卡人');
  const createdBy = creator[0].id;

  const results = [];
  for (const team of teams) {
    const teamRows = byTeam.get(team.id) || [];
    const members = allMembers.filter((m) => m.team_id === team.id);
    const [vehicles] = await pool.query(
      'SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ?',
      [team.id]
    );
    const [destinations] = await pool.query(
      'SELECT id, name FROM worklog_destination WHERE status = 1 AND team_id = ?',
      [team.id]
    );
    // 本班当日已有出车卡片（未出车卡无车牌/用车人天然不含，同 loadEntries 口径）
    const entries = await loadEntries(team.id, today, today);
    const entryByMember = new Map();
    entries.forEach((e) => e.memberList.forEach((m) => entryByMember.set(m.member_id, e)));

    // 行分桶：用车人当日已有卡片（本班或别班/跨班）→ 比对桶（仅通知不改库）；无占用 → 自动建卡候选
    const freshRows = [];
    const overlaps = [];
    for (const row of teamRows) {
      const matched = members.filter((m) => m.name && row.userText.includes(m.name));
      const localCards = new Map();
      const foreign = [];
      matched.forEach((m) => {
        const e = entryByMember.get(m.id);
        if (e) { localCards.set(e.id, e); return; }
        if (occupiedGlobal.has(m.id)) foreign.push(m.name);
      });
      if (localCards.size || foreign.length) overlaps.push({ row, matched, cards: [...localCards.values()], foreign });
      else freshRows.push(row);
    }

    // planCards 的 outside 返回值在此不用（行级班组判定已在上方全局完成）；
    // assignedIds 传空集——占用成员所在行已全部进比对桶，建卡候选天然无占用（集合仅作本次运行内行间去重）
    const { plans, skippedDict } = planCards(freshRows, members, vehicles, destinations, new Set());

    const created = [];
    for (const p of plans) {
      const [r] = await pool.query(
        'INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, dispatch_order_no, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)',
        [team.id, today, '', p.vehicle.id, p.destination ? p.destination.id : null, p.row.orderNo || null, createdBy]
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

    // 已有卡比对（同派车对齐口径）：车牌精确、目的地 destSame 模糊（派车单为空不约束）、用车人集合；全项一致仅计数
    let aligned = 0;
    const mismatches = [];
    for (const o of overlaps) {
      // 派车单号回填已有卡片（多张卡时不确定归属不写；仅他班占用无本班卡的行不写）
      if (o.cards.length === 1) await saveOrderNo(o.cards[0].id, o.row);
      const issues = [];
      for (const card of o.cards) {
        // 一条派车单占用多张卡片时逐卡比对，前缀标注卡片便于定位
        const label = o.cards.length > 1 ? `卡片「${card.plate_no}」（${card.memberList.map((m) => m.name).join('、')}）：` : '';
        if (card.plate_no !== o.row.plate) {
          const inDict = vehicles.some((v) => v.plate_no === o.row.plate);
          issues.push(`${label}车牌不一致：卡片「${card.plate_no}」≠ 派车单「${o.row.plate}」→ 请在卡片「修改派车」改为 ${o.row.plate}${inDict ? '' : '（下拉无此车牌，请先在数据管理添加）'}`);
        }
        if (o.row.to) {
          const cardDest = card.destination_name || '';
          if (!cardDest) {
            issues.push(`${label}目的地未选（派车单「${o.row.to}」）→ 请补选目的地（无匹配项请先在数据管理添加）`);
          } else if (!destSame(cardDest, o.row.to)) {
            issues.push(`${label}目的地不一致：卡片「${cardDest}」≠ 派车单「${o.row.to}」→ 请核对修改`);
          }
        }
        const cardNames = new Set(card.memberList.map((m) => m.name));
        const missing = o.matched.filter((m) => !cardNames.has(m.name)).map((m) => m.name);
        const extra = card.memberList.filter((m) => !(m.name && o.row.userText.includes(m.name))).map((m) => m.name);
        if (missing.length) issues.push(`${label}用车人缺少：${missing.join('、')} → 请在卡片点亮补入（其当日若另有卡片请先调整，同一人同日只能挂一张卡）`);
        if (extra.length) issues.push(`${label}用车人多出：${extra.join('、')}（派车单无）→ 请确认是否取消点亮`);
      }
      if (o.cards.length > 1) issues.push(`用车人分散在 ${o.cards.length} 张卡片 → 请合并为一张（保留与派车单最一致者，其余删除）`);
      if (o.foreign.length) issues.push(`用车人 ${o.foreign.join('、')} 当日已在其他班组卡片（含跨班日志）→ 本班不再为其建卡，如需调整请联系超级管理员`);
      if (issues.length) mismatches.push({ row: o.row, issues });
      else aligned += 1;
    }

    const cross = crossIdx.filter((i) => rowTeams[i].has(team.id)).map((i) => rows[i]);
    results.push({ team, created, skippedDict, cross, mismatches, aligned });
  }
  return { results, outside, otherTeam, dropped, stale, total: rows.length };
}

// 同步结果通知：超管 + 指定班组启用班组管理员（teamIds 空时仅超管，如班组解析失败场景）；通知失败仅记日志
// 微信推送：发到 teamIds 对应班组群
async function notify(title, lines, teamIds) {
  try {
    const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
    let teamAdmins = [];
    const ids = (teamIds || []).filter(Boolean);
    if (ids.length) {
      [teamAdmins] = await pool.query(
        "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id IN (?) AND status = 1",
        [ids]
      );
    }
    const userIds = [...new Set([...admins.map((a) => a.id), ...teamAdmins.map((a) => a.id)])];
    if (!userIds.length) return;
    await require('../notice').push({ // 与 sgccclockin 同型惰性加载
      userIds,
      targets: [],
      title,
      content: lines.join('\n'),
      wxTeamIds: ids,
    });
  } catch (err) {
    console.error('[派车单同步] 通知发送失败：', err.message);
  }
}

/* ===== 每日任务 ===== */

// 09:10 准备：锁定设备 → U 盘挂载至被控机；失败立即解禁并通知（取件任务仍按自身排程执行）
async function prepareJob() {
  let teams = [];
  try {
    teams = await resolveTeams();
    if (!teams.length) {
      console.log('[派车单同步] 无开启班组，准备任务跳过');
      return;
    }
    const dev = await findDevice();
    await devlock.lock(dev.ddns, LOCK_REASON);
    console.log(`[派车单同步] 设备 ${dev.ddns} 已锁定，正在挂载 U 盘至被控机 …`);
    await mountToHost(dev);
    console.log('[派车单同步] U 盘已挂载至被控机端，等待被控机导出');
  } catch (err) {
    console.error('[派车单同步] 准备任务失败：', err.message);
    await devlock.unlock(cfg().deviceDdns);
    await notify('派车单同步准备失败', [
      `锁定设备 / 挂载 U 盘失败：${err.message}`,
      '设备已解禁，取件任务仍按排程执行。',
    ], teams.map((t) => t.id));
  }
}

let fetching = false; // 取件重入守卫（定时与手动触发共用）

// 09:20 取件：下载 → 删除 U 盘文件 → 解析建卡 → 解禁（finally 保证）→ 每日结果通知（按班组分发）；返回结果对象供手动触发回显
async function fetchJob() {
  if (fetching) return { ok: false, message: '已有取件任务在执行中，请稍后再试' };
  fetching = true;
  const today = todayCn();
  const fileName = `${today}-派车单.xlsx`;
  let teams = [];
  try {
    teams = await resolveTeams();
    if (!teams.length) {
      console.log('[派车单同步] 无开启班组，取件任务跳过');
      return { ok: true, message: '当前没有开启派车同步的班组（在「派车对齐」页按班组开启）' };
    }
    const dev = await findDevice();
    const buf = await downloadFile(dev, fileName);
    if (!buf) {
      const lines = [
        `未在设备 U 盘找到「${fileName}」。`,
        '可能当日无派车单，或被控机导出客户端（内网工具箱）异常。',
      ];
      await notify('派车单同步：今日无文件', lines, teams.map((t) => t.id));
      return { ok: true, message: lines.join('\n') };
    }
    console.log(`[派车单同步] 已下载 ${fileName}（${(buf.length / 1024).toFixed(1)} KB）`);
    try {
      await deleteFile(dev, fileName); // 按口径：下载完毕即删除 U 盘中派车单文件
    } catch (err) {
      console.error('[派车单同步] U 盘文件删除失败（文件已下载，继续建卡）：', err.message);
    }
    const r = await importOrdersMulti(teams, buf, today);
    const messages = [];
    for (const res of r.results) {
      const overlapCount = res.mismatches.length + res.aligned;
      const lines = [
        `${today} 解析 ${r.total} 行，新建 ${res.created.length} 张出车卡片`
        + `（剔除：非当日 ${r.stale} / 已取消或无车牌 ${r.dropped} / 非班组用车 ${r.outside} / 仅属未开启班组 ${r.otherTeam}）`
        + (overlapCount ? `；${overlapCount} 条用车人当日已有手动卡（不一致 ${res.mismatches.length} 条见下文）` : '')
        + '。',
      ];
      res.created.forEach((c) =>
        lines.push(`· ${c.plate}${c.destination ? ` → ${c.destination}` : ''}：${c.members.join('、')}`)
      );
      if (res.mismatches.length) {
        lines.push(`以下 ${res.mismatches.length} 条已有手动卡未改动，请核对：`);
        res.mismatches.forEach((a) => {
          lines.push(`· ${a.row.plate}${a.row.to ? ` → ${a.row.to}` : ''}（派车单用车人：${a.row.userText}）`);
          a.issues.forEach((it) => lines.push(`  ${it}`));
        });
      }
      if (res.skippedDict.length) {
        lines.push(`以下 ${res.skippedDict.length} 条字典未命中未建卡，请先将车牌/目的地入字典再人工建卡：`);
        res.skippedDict.forEach((s) =>
          lines.push(`· ${s.plate}${s.to ? ` → ${s.to}` : ''}（用车人：${s.userText}）：${s.reason}`)
        );
      }
      if (res.cross.length) {
        lines.push(`以下 ${res.cross.length} 条含跨班用车人，仅通知不建卡（需建卡请联系超管建跨班日志）：`);
        res.cross.forEach((s) =>
          lines.push(`· ${s.plate}${s.to ? ` → ${s.to}` : ''}（用车人：${s.userText}）`)
        );
      }
      await notify(`派车单同步完成（${res.team.name}）`, lines, [res.team.id]);
      messages.push(`【${res.team.name}】\n${lines.join('\n')}`);
      console.log(`[派车单同步] ${today} ${res.team.name} 完成：新建 ${res.created.length} 张出车卡片，已有卡比对不一致 ${res.mismatches.length} 条`);
    }
    return { ok: true, message: messages.join('\n\n') };
  } catch (err) {
    console.error('[派车单同步] 取件任务失败：', err.message);
    const lines = [
      `取件失败：${err.message}`,
      '设备已解禁，可在出工日志「派车对齐」人工导入导出件或手动补跑。',
    ];
    await notify('派车单同步失败', lines, teams.map((t) => t.id));
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
  if (!cfg().deviceDdns) {
    console.error('[派车单同步] 未配置设备 ddns（WORKLOG_DISPATCH_DEVICE_DDNS），同步停用');
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
