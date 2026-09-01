// 题库刷题路由：全部接口需登录 + quiz 应用权限；班组上下文 req.team 见 utils/team.js
// 双题库池：scope=team 班组池（本班成员可见）/ scope=all 全部池（仅超管维护，全员可见，team_id 为 NULL）
// 个人题库：用户从池中「添加」（quiz_user_bank 订阅），GET /banks 只回我的题库，与班组无关
// 错题本（quiz_wrong）/ 收藏（quiz_favorite）为个人口径，按题库过滤展示（题库主页 GET /banks/:id/home）
// 管理接口（[manage]）：requireQuizAdmin 粗筛角色，findManageBank 细粒度校验归属——
// 超管可管任意题库（含他班班组池与全部池），班组管理员仅本班班组池
const express = require('express');
const multer = require('multer');
const XLSX = require('xlsx');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const analyzer = require('./analyzer');
const { LETTERS } = require('./dify');

const router = express.Router();
router.use(auth, requireApp('quiz'));

// 班组上下文：解析生效班组（超管可用 ?team_id= 指定；其余角色固定本班，未分配 → null）
router.use(async (req, res, next) => {
  try {
    req.team = await teamUtil.resolveReqTeam(req);
    return next();
  } catch (err) {
    return next(err);
  }
});

// 题库管理权限粗筛：超管任意班组（req.team 由 ?team_id= 解析），班组管理员仅本班；
// 题库归属的细粒度校验由各接口内 findManageBank 完成
function requireQuizAdmin(req, res, next) {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
}

// 选项 JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 worklog members 同口径）
function parseOptions(raw) {
  if (!raw) return [];
  const arr = typeof raw === 'string' ? JSON.parse(raw) : raw;
  return Array.isArray(arr) ? arr : [];
}

// 取题库行（含池与归属班组）；可见性/管理权由下面两个助手判断
async function findBank(bankId) {
  const [rows] = await pool.query('SELECT id, team_id, scope FROM quiz_bank WHERE id = ?', [Number(bankId) || 0]);
  return rows[0] || null;
}

// 可见性：全部池全员可见；班组池仅本班（req.team 为生效班组，无班组用户只见全部池）
async function findVisibleBank(bankId, req) {
  const bank = await findBank(bankId);
  if (!bank) return null;
  if (bank.scope === 'all') return bank;
  if (req.team && bank.team_id === req.team.id) return bank;
  return null;
}

// 管理权：超管可管任意题库（含他班班组池与全部池）；班组管理员仅本班班组池
async function findManageBank(bankId, req) {
  const bank = await findBank(bankId);
  if (!bank) return null;
  if (req.user.role === 'admin') return bank;
  if (req.user.role === 'team_admin' && bank.scope === 'team' && req.team && bank.team_id === req.team.id) return bank;
  return null;
}

// 批量取题库的题目数与解析四状态统计（GET /banks、/banks/pool、/banks/manage、/banks/:id/home 共用），返回 { bankId: { questionCount, analysis } }
async function loadBankStats(ids) {
  if (!ids.length) return {};
  const [rows] = await pool.query(
    `SELECT bank_id, COUNT(*) AS total,
       SUM(CASE WHEN analysis_status = 'none' THEN 1 ELSE 0 END) AS none_cnt,
       SUM(CASE WHEN analysis_status = 'pending' THEN 1 ELSE 0 END) AS pending_cnt,
       SUM(CASE WHEN analysis_status = 'failed' THEN 1 ELSE 0 END) AS failed_cnt,
       SUM(CASE WHEN analysis_status = 'done' THEN 1 ELSE 0 END) AS done_cnt
     FROM quiz_question WHERE bank_id IN (?) AND status = 1 GROUP BY bank_id`,
    [ids]
  );
  const map = {};
  rows.forEach((s) => {
    map[s.bank_id] = {
      questionCount: Number(s.total) || 0,
      analysis: {
        none: Number(s.none_cnt) || 0,
        pending: Number(s.pending_cnt) || 0,
        failed: Number(s.failed_cnt) || 0,
        done: Number(s.done_cnt) || 0,
      },
    };
  });
  return map;
}

// 空统计（库内无启用题目时的默认值）
function emptyStats() {
  return { questionCount: 0, analysis: { none: 0, pending: 0, failed: 0, done: 0 } };
}

// 批量取当前用户每题最近一次作答（刷题记录恢复：qid → { right, answer }，answer 为归一后的原始字母）
async function loadMyLatestRecords(userId, qids) {
  if (!qids.length) return {};
  const [rows] = await pool.query(
    `SELECT r.question_id, r.is_right, r.user_answer
     FROM quiz_record r
     JOIN (SELECT question_id, MAX(id) AS mid FROM quiz_record
           WHERE user_id = ? AND question_id IN (?) GROUP BY question_id) t
       ON t.question_id = r.question_id AND t.mid = r.id`,
    [userId, qids]
  );
  const map = {};
  rows.forEach((r) => { map[r.question_id] = { right: r.is_right ? 1 : 0, answer: r.user_answer }; });
  return map;
}

// 题型归一：单选/单选题/single → single，多选/多选题/multiple → multiple，判断/判断题/judge → judge
// （trim、忽略大小写；导入与管理接口共用）
function normalizeType(v) {
  const t = String(v == null ? '' : v).trim().toLowerCase();
  if (['单选', '单选题', 'single'].includes(t)) return 'single';
  if (['多选', '多选题', 'multiple'].includes(t)) return 'multiple';
  if (['判断', '判断题', 'judge'].includes(t)) return 'judge';
  return null;
}

// 答案归一与校验：single 单字母；multiple ≥2 字母且在选项范围内（去空格转大写、按字母序去重）；
// judge 对/正确/√/T/true/A → 'A'，错/错误/×/F/false/B → 'B'。返回 { answer } 或 { error }
function normalizeAnswer(type, raw, optionCount) {
  const s = String(raw == null ? '' : raw).trim();
  if (type === 'judge') {
    const t = s.toLowerCase();
    if (['对', '正确', '√', 't', 'true', 'a'].includes(t)) return { answer: 'A' };
    if (['错', '错误', '×', 'f', 'false', 'b'].includes(t)) return { answer: 'B' };
    return { error: '判断题答案应为 对/错（或 A/B）' };
  }
  const uniq = [...new Set(s.toUpperCase().replace(/[^A-Z]/g, ''))].sort();
  if (!uniq.length) return { error: '答案不能为空' };
  const maxLetter = LETTERS[optionCount - 1] || 'A';
  if (uniq.some((c) => c > maxLetter)) return { error: `答案超出选项范围：识别到 ${optionCount} 个选项（A~${maxLetter}），请检查该行选项是否填写完整` };
  if (type === 'single' && uniq.length !== 1) return { error: '单选题答案应为 1 个字母' };
  if (type === 'multiple' && uniq.length < 2) return { error: '多选题答案应至少 2 个字母' };
  return { answer: uniq.join('') };
}

// 用户作答归一：转大写去非字母、去重、按字母序（多选集合相等即对，漏选判错）
function normalizeUserAnswer(raw) {
  return [...new Set(String(raw == null ? '' : raw).toUpperCase().replace(/[^A-Z]/g, ''))].sort().join('');
}

// 管理接口题目字段校验：返回 { type, content, options, answer, analysis } 或 { error }
function validateQuestionPayload(body) {
  const type = normalizeType(body.type);
  if (!type) return { error: '题型应为 single（单选）/ multiple（多选）/ judge（判断）' };
  const content = String(body.content || '').trim();
  if (!content) return { error: '题干不能为空' };
  let options;
  if (type === 'judge') {
    options = ['正确', '错误']; // 判断题无视选项，固定两项
  } else {
    options = (Array.isArray(body.options) ? body.options : [])
      .map((o) => String(o).trim())
      .filter(Boolean);
    if (options.length < 2) return { error: '选项至少 2 个' };
    if (options.length > 6) return { error: '选项最多 6 个' };
  }
  const a = normalizeAnswer(type, body.answer, options.length);
  if (a.error) return { error: a.error };
  return { type, content, options, answer: a.answer, analysis: String(body.analysis || '').trim() };
}

// GET /overview：当前用户答题总览（按人跨池统计：记录总数、正确率%、错题本题数）
router.get('/overview', async (req, res, next) => {
  try {
    const [rec] = await pool.query(
      'SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE user_id = ?',
      [req.user.id]
    );
    const total = Number(rec[0].total) || 0;
    const rights = Number(rec[0].rights) || 0;
    const [wr] = await pool.query('SELECT COUNT(*) AS cnt FROM quiz_wrong WHERE user_id = ?', [req.user.id]);
    return ok(res, {
      totalAnswered: total,
      rightRate: total ? Math.round((rights / total) * 100) : null,
      wrongCount: Number(wr[0].cnt) || 0,
    });
  } catch (err) {
    return next(err);
  }
});

// GET /banks：我的题库（从池中添加的订阅，按添加时间升序；与班组无关，无班组用户同样可见已订阅的全部池题库）
router.get('/banks', async (req, res, next) => {
  try {
    const [banks] = await pool.query(
      `SELECT b.id, b.name, b.description, b.scope, t.name AS team_name
       FROM quiz_user_bank ub
       JOIN quiz_bank b ON b.id = ub.bank_id AND b.status = 1
       LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE ub.user_id = ?
       ORDER BY ub.created_at, ub.id`,
      [req.user.id]
    );
    if (!banks.length) return ok(res, { list: [] });
    const ids = banks.map((b) => b.id);
    const stats = await loadBankStats(ids);
    // 当前用户各库答题统计（答过题数按不同题计，正确率按答题记录计）
    const [ustats] = await pool.query(
      `SELECT bank_id, COUNT(DISTINCT question_id) AS answered, COUNT(*) AS total, SUM(is_right) AS rights
       FROM quiz_record WHERE user_id = ? AND bank_id IN (?) GROUP BY bank_id`,
      [req.user.id, ids]
    );
    const umap = {};
    ustats.forEach((s) => { umap[s.bank_id] = s; });
    const list = banks.map((b) => {
      const st = stats[b.id] || emptyStats();
      const us = umap[b.id];
      const uTotal = us ? Number(us.total) : 0;
      return {
        id: b.id,
        name: b.name,
        description: b.description,
        scope: b.scope,
        teamName: b.scope === 'team' ? b.team_name || null : null,
        questionCount: st.questionCount,
        answeredCount: us ? Number(us.answered) : 0,
        rightRate: uTotal ? Math.round((Number(us.rights) / uTotal) * 100) : null,
        analysis: st.analysis,
      };
    });
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// GET /banks/pool：可见题库池（全部池 + 本班班组池；全部池在前，其次班组池，再按 id；无班组用户只回全部池）
router.get('/banks/pool', async (req, res, next) => {
  try {
    const teamId = req.team ? req.team.id : 0; // team_id 为正数，0 不匹配任何班组池行
    const [banks] = await pool.query(
      `SELECT b.id, b.name, b.description, b.scope, t.name AS team_name
       FROM quiz_bank b
       LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE b.status = 1 AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))
       ORDER BY CASE WHEN b.scope = 'all' THEN 0 ELSE 1 END, b.id`,
      [teamId]
    );
    if (!banks.length) return ok(res, { list: [] });
    const ids = banks.map((b) => b.id);
    const stats = await loadBankStats(ids);
    // 是否已加入我的题库
    const [subs] = await pool.query(
      'SELECT bank_id FROM quiz_user_bank WHERE user_id = ? AND bank_id IN (?)',
      [req.user.id, ids]
    );
    const addedSet = new Set(subs.map((s) => s.bank_id));
    const list = banks.map((b) => {
      const st = stats[b.id] || emptyStats();
      return {
        id: b.id,
        name: b.name,
        description: b.description,
        scope: b.scope,
        teamName: b.scope === 'team' ? b.team_name || null : null,
        questionCount: st.questionCount,
        added: addedSet.has(b.id) ? 1 : 0,
        analysis: st.analysis,
      };
    });
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// GET /banks/:id/home：题库主页（小程序 bank 页）——题库信息 + 题数/解析状态 + 本人已练题数/正确率 + 本题库错题数/收藏数
router.get('/banks/:id/home', async (req, res, next) => {
  try {
    const bank = await findVisibleBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在或不可见');
    const [rows] = await pool.query(
      `SELECT b.name, b.description, b.scope, t.name AS team_name
       FROM quiz_bank b LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE b.id = ?`,
      [bank.id]
    );
    const b = rows[0];
    const st = (await loadBankStats([bank.id]))[bank.id] || emptyStats();
    // 本人本题库答题统计（已练题数按不同题计，正确率按答题记录计）
    const [us] = await pool.query(
      `SELECT COUNT(DISTINCT question_id) AS answered, COUNT(*) AS total, SUM(is_right) AS rights
       FROM quiz_record WHERE user_id = ? AND bank_id = ?`,
      [req.user.id, bank.id]
    );
    const uTotal = us[0] ? Number(us[0].total) || 0 : 0;
    // 错题/收藏均为个人口径，按本题库过滤（跟着题库走）
    const [wr] = await pool.query('SELECT COUNT(*) AS cnt FROM quiz_wrong WHERE user_id = ? AND bank_id = ?', [req.user.id, bank.id]);
    const [fv] = await pool.query('SELECT COUNT(*) AS cnt FROM quiz_favorite WHERE user_id = ? AND bank_id = ?', [req.user.id, bank.id]);
    return ok(res, {
      id: bank.id,
      name: b.name,
      description: b.description,
      scope: b.scope,
      teamName: b.scope === 'team' ? b.team_name || null : null,
      questionCount: st.questionCount,
      analysis: st.analysis,
      answeredCount: us[0] ? Number(us[0].answered) || 0 : 0,
      rightRate: uTotal ? Math.round((Number(us[0].rights) / uTotal) * 100) : null,
      wrongCount: Number(wr[0].cnt) || 0,
      favCount: Number(fv[0].cnt) || 0,
    });
  } catch (err) {
    return next(err);
  }
});

// GET /banks/manage：管理列表 [manage]——超管：全部池 + 生效班组班组池；班组管理员：本班班组池
router.get('/banks/manage', requireQuizAdmin, async (req, res, next) => {
  try {
    const teamId = req.team ? req.team.id : 0; // 超管无生效班组时只回全部池
    const where = req.user.role === 'admin'
      ? "(b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?))"
      : "(b.scope = 'team' AND b.team_id = ?)";
    const [banks] = await pool.query(
      `SELECT b.id, b.name, b.description, b.scope, t.name AS team_name,
         DATE_FORMAT(b.created_at, '%Y-%m-%d %H:%i:%s') AS created_at
       FROM quiz_bank b
       LEFT JOIN sys_team t ON b.scope = 'team' AND t.id = b.team_id
       WHERE ${where}
       ORDER BY b.id DESC`,
      [teamId]
    );
    if (!banks.length) return ok(res, { list: [] });
    const stats = await loadBankStats(banks.map((b) => b.id));
    const list = banks.map((b) => {
      const st = stats[b.id] || emptyStats();
      return {
        id: b.id,
        name: b.name,
        description: b.description,
        scope: b.scope,
        teamName: b.scope === 'team' ? b.team_name || null : null,
        questionCount: st.questionCount,
        analysis: st.analysis,
        createdAt: b.created_at,
      };
    });
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// POST /banks/:id/join：把池中题库添加进我的题库（重复添加幂等）
router.post('/banks/:id/join', async (req, res, next) => {
  try {
    const bank = await findVisibleBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在或不可见');
    await pool.query('INSERT IGNORE INTO quiz_user_bank (user_id, bank_id) VALUES (?, ?)', [req.user.id, bank.id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /banks/:id/join：从我的题库移出（仅删订阅；练习记录/错题保留，文案由前端提示）
router.delete('/banks/:id/join', async (req, res, next) => {
  try {
    await pool.query('DELETE FROM quiz_user_bank WHERE user_id = ? AND bank_id = ?', [
      req.user.id,
      Number(req.params.id) || 0,
    ]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /banks：新建题库 [manage]（scope 默认 team 班组池挂生效班组；scope=all 全部池仅超管，team_id 存 NULL）
router.post('/banks', requireQuizAdmin, async (req, res, next) => {
  try {
    const scope = req.body && req.body.scope === 'all' ? 'all' : 'team';
    if (scope === 'all' && req.user.role !== 'admin') {
      return fail(res, 403, 40304, '仅超管可上传至全部池');
    }
    let teamId = null;
    if (scope === 'team') {
      if (!req.team) return fail(res, 400, 40030, '无可用班组');
      teamId = req.team.id;
    }
    const name = String((req.body && req.body.name) || '').trim().slice(0, 100);
    const description = String((req.body && req.body.description) || '').trim().slice(0, 255);
    if (!name) return fail(res, 400, 40030, '请输入题库名称');
    if (scope === 'all') {
      // uk_team_name 唯一键对 NULL team_id 不生效，全部池重名走应用层校验
      const [dup] = await pool.query("SELECT id FROM quiz_bank WHERE scope = 'all' AND name = ? LIMIT 1", [name]);
      if (dup.length) return fail(res, 409, 40900, `「${name}」已存在`);
    }
    try {
      const [r] = await pool.query(
        'INSERT INTO quiz_bank (team_id, name, description, scope, created_by) VALUES (?, ?, ?, ?, ?)',
        [teamId, name, description, scope, req.user.id]
      );
      return ok(res, { id: r.insertId });
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${name}」已存在`);
      throw err;
    }
  } catch (err) {
    return next(err);
  }
});

// PUT /banks/:id：修改题库名称/简介 [manage]；body 带 scope 且与现值不同 → 仅超管可变更题库池
router.put('/banks/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    const bank = await findManageBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const name = String((req.body && req.body.name) || '').trim().slice(0, 100);
    const description = String((req.body && req.body.description) || '').trim().slice(0, 255);
    if (!name) return fail(res, 400, 40030, '请输入题库名称');
    let scope = bank.scope;
    let teamId = bank.team_id;
    if (req.body && req.body.scope !== undefined) {
      const wanted = req.body.scope === 'all' ? 'all' : 'team';
      if (wanted !== bank.scope) {
        if (req.user.role !== 'admin') return fail(res, 403, 40304, '仅超管可变更题库池');
        if (wanted === 'all') {
          scope = 'all';
          teamId = null; // 全部池不属任何班组
        } else {
          if (!req.team) return fail(res, 400, 40030, '无可用班组');
          scope = 'team';
          teamId = req.team.id; // 转入班组池：挂当前生效班组
        }
      }
    }
    if (scope === 'all') {
      // uk_team_name 唯一键对 NULL team_id 不生效，全部池重名走应用层校验
      const [dup] = await pool.query("SELECT id FROM quiz_bank WHERE scope = 'all' AND name = ? AND id <> ? LIMIT 1", [name, bank.id]);
      if (dup.length) return fail(res, 409, 40900, `「${name}」已存在`);
    }
    try {
      await pool.query('UPDATE quiz_bank SET name = ?, description = ?, scope = ?, team_id = ? WHERE id = ?', [name, description, scope, teamId, bank.id]);
      return ok(res, null);
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${name}」已存在`);
      throw err;
    }
  } catch (err) {
    return next(err);
  }
});

// DELETE /banks/:id：删除题库（事务连带删题目/答题记录/错题/收藏/个人订阅） [manage]
router.delete('/banks/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    const bank = await findManageBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const conn = await pool.getConnection();
    try {
      await conn.beginTransaction();
      await conn.query('DELETE FROM quiz_record WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_wrong WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_favorite WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_question WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_user_bank WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_bank WHERE id = ?', [bank.id]);
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// GET /banks/template：题库导入模板下载（xlsx；主表表头 + 3 行示例，第二张 sheet 放填写说明）
router.get('/banks/template', async (req, res, next) => {
  try {
    const ws = XLSX.utils.aoa_to_sheet([
      ['题型', '题干', '选项A', '选项B', '选项C', '选项D', '选项E', '选项F', '答案', '解析'],
      ['单选题', '示例：安全带的正确挂扣方式是（ ）。', '高挂低用', '低挂高用', '平挂平用', '随意挂扣', '', '', 'A', '安全带应高挂低用，坠落时冲击力更小。'],
      ['多选题', '示例：下列属于个人安全防护用品的有（ ）。', '安全帽', '安全带', '绝缘手套', '普通布鞋', '', '', 'ABC', '普通布鞋不属于安全防护用品。'],
      ['判断题', '示例：雷雨天气可以进行户外登塔作业。', '', '', '', '', '', '', 'B', '雷雨天气禁止户外登塔作业。'],
    ]);
    ws['!cols'] = [{ wch: 8 }, { wch: 50 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 8 }, { wch: 40 }];
    const tips = XLSX.utils.aoa_to_sheet([
      ['题库导入填写说明'],
      ['1. 只读取第一张 sheet，首行表头固定为：题型 | 题干 | 选项A~F | 答案 | 解析。'],
      ['2. 题型列填写：单选题、多选题、判断题（兼容 单选/多选/判断 及 single/multiple/judge，不区分大小写）。'],
      ['3. 单选/多选题选项A~F 至少填写 2 个，按序取非空列；判断题无需填写选项，固定为「正确/错误」。'],
      ['4. 答案列：单选填 1 个字母（如 A）；多选填 2 个及以上字母（如 ABC，顺序不限）；判断填 对/错（或 A/B）。答案引用 E/F 时，对应 选项E/选项F 列必须已填写内容。'],
      ['5. 解析列可留空，留空的题目入库后由 AI 自动生成解析（需配置 DIFY_QUIZ_API_KEY）。'],
      ['6. 导入为全量替换该题库题目，导入前请删除第一张 sheet 的 3 行示例，仅保留表头与正式题目。'],
    ]);
    tips['!cols'] = [{ wch: 100 }];
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, ws, '题目');
    XLSX.utils.book_append_sheet(wb, tips, '填写说明');
    const buf = XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' });
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="quiz-template.xlsx"; filename*=UTF-8''${encodeURIComponent('题库导入模板.xlsx')}`
    );
    return res.send(buf);
  } catch (err) {
    return next(err);
  }
});

// POST /banks/:id/import：导入题目 Excel（.xlsx，multipart 字段 file ≤10MB），全量替换该库题目（事务） [manage]
const quizUpload = multer({ storage: multer.memoryStorage(), limits: { fileSize: 10 * 1024 * 1024, files: 1 } });
router.post(
  '/banks/:id/import',
  requireQuizAdmin,
  (req, res, next) => {
    quizUpload.single('file')(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40030, '文件大小应在 10MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（兼容 formData 携带 team_id），
      // 归属校验统一走 findManageBank（超管任意题库，班组管理员仅本班班组池）
      req.team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      const bank = await findManageBank(req.params.id, req);
      if (!bank) return fail(res, 404, 40400, '题库不存在');
      if (!req.file || !req.file.buffer || !req.file.buffer.length) {
        return fail(res, 400, 40030, '请选择要上传的 Excel 文件');
      }
      const fname = Buffer.from(req.file.originalname || '', 'latin1').toString('utf8');
      if (!/\.xlsx$/i.test(fname)) return fail(res, 400, 40030, '仅支持 .xlsx 文件，请使用模板填写');

      let rows;
      try {
        const wb = XLSX.read(req.file.buffer, { type: 'buffer' });
        // 只读第一张 sheet
        rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
      } catch (e) {
        return fail(res, 400, 40030, 'Excel 解析失败，请使用模板文件填写');
      }

      // 逐行校验（跳过第 1 行表头；空行跳过）：有错整体失败，不入库
      const data = []; // [type, content, optionsJson, answer, analysis, analysisStatus]
      const errors = [];
      for (let i = 1; i < rows.length; i += 1) {
        const cells = (Array.isArray(rows[i]) ? rows[i] : []).map((c) => String(c).trim());
        if (cells.every((c) => !c)) continue;
        const rowNo = i + 1; // Excel 行号（1-based，含表头偏移）
        let error = '';
        const type = normalizeType(cells[0]);
        if (!type) {
          error = '题型无法识别（应为 单选题/多选题/判断题）';
        } else if (!cells[1]) {
          error = '题干不能为空';
        } else {
          // 判断题无视选项列固定两项；单选/多选取选项A~F 非空列按序组成数组
          const options = type === 'judge' ? ['正确', '错误'] : cells.slice(2, 8).filter(Boolean);
          if (type !== 'judge' && options.length < 2) {
            error = '选项至少填写 2 个';
          } else {
            const a = normalizeAnswer(type, cells[8], options.length);
            if (a.error) {
              error = a.error;
            } else {
              // 解析列非空 → 存值并置 done；为空 → none（导入完成后入 AI 队列）
              const analysis = cells[9] || '';
              data.push([type, cells[1], JSON.stringify(options), a.answer, analysis || null, analysis ? 'done' : 'none']);
            }
          }
        }
        if (error) {
          errors.push({ row: rowNo, message: error });
          if (errors.length >= 20) break; // 错误最多返回 20 条
        }
      }
      if (errors.length) {
        return res.status(400).json({
          code: 40031,
          message: `共 ${errors.length} 行校验未通过，请修正后重新导入`,
          data: { errors },
        });
      }
      if (!data.length) return fail(res, 400, 40030, '未识别到有效题目行，请按模板列填写');

      const conn = await pool.getConnection();
      try {
        await conn.beginTransaction();
        // 全量替换：先删该库旧答题记录/错题/收藏/题目，再分批插入（sort 按行序）
        await conn.query('DELETE FROM quiz_record WHERE bank_id = ?', [bank.id]);
        await conn.query('DELETE FROM quiz_wrong WHERE bank_id = ?', [bank.id]);
        await conn.query('DELETE FROM quiz_favorite WHERE bank_id = ?', [bank.id]);
        await conn.query('DELETE FROM quiz_question WHERE bank_id = ?', [bank.id]);
        for (let i = 0; i < data.length; i += 500) {
          const chunk = data.slice(i, i + 500).map((r, j) => [bank.id, ...r, i + j + 1]);
          await conn.query(
            'INSERT INTO quiz_question (bank_id, type, content, options, answer, analysis, analysis_status, sort) VALUES ?',
            [chunk]
          );
        }
        await conn.commit();
      } catch (e) {
        await conn.rollback().catch(() => {});
        throw e;
      } finally {
        conn.release();
      }

      // 提交后把无解析的题入 AI 队列（未配置 Dify 时静默跳过，queued=0）
      const queued = await analyzer.enqueueBank(bank.id, ['none']);
      return ok(res, { imported: data.length, queued }, `已导入 ${data.length} 道题目`);
    } catch (err) {
      return next(err);
    }
  }
);

// GET /banks/:id/questions：题目分页列表（q 模糊匹配题干） [manage]
router.get('/banks/:id/questions', requireQuizAdmin, async (req, res, next) => {
  try {
    const bank = await findManageBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const page = Math.max(1, parseInt(req.query.page, 10) || 1);
    const size = Math.min(100, Math.max(1, parseInt(req.query.size, 10) || 20));
    const q = String(req.query.q || '').trim();
    const where = q ? 'AND content LIKE ?' : '';
    const params = q ? [bank.id, `%${q}%`] : [bank.id];
    const [cnt] = await pool.query(`SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? ${where}`, params);
    // LIMIT/OFFSET 为上方钳制后的整数，直接拼接（mysql2 占位符不支持 LIMIT 场景的稳定行为）
    const [rows] = await pool.query(
      `SELECT id, type, content, options, answer, analysis, analysis_status,
         DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s') AS created_at
       FROM quiz_question WHERE bank_id = ? ${where}
       ORDER BY sort, id LIMIT ${size} OFFSET ${(page - 1) * size}`,
      params
    );
    const list = rows.map((r) => ({
      id: r.id,
      type: r.type,
      content: r.content,
      options: parseOptions(r.options),
      answer: r.answer,
      analysis: r.analysis,
      analysisStatus: r.analysis_status,
      createdAt: r.created_at,
    }));
    return ok(res, { total: Number(cnt[0].total), list });
  } catch (err) {
    return next(err);
  }
});

// POST /banks/:id/questions：新增题目（解析为空则入库后入 AI 队列） [manage]
router.post('/banks/:id/questions', requireQuizAdmin, async (req, res, next) => {
  try {
    const bank = await findManageBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const v = validateQuestionPayload(req.body || {});
    if (v.error) return fail(res, 400, 40030, v.error);
    const [maxRows] = await pool.query('SELECT COALESCE(MAX(sort), 0) AS m FROM quiz_question WHERE bank_id = ?', [bank.id]);
    const [r] = await pool.query(
      'INSERT INTO quiz_question (bank_id, type, content, options, answer, analysis, analysis_status, sort) VALUES (?, ?, ?, ?, ?, ?, ?, ?)',
      [bank.id, v.type, v.content, JSON.stringify(v.options), v.answer, v.analysis || null, v.analysis ? 'done' : 'none', Number(maxRows[0].m) + 1]
    );
    if (!v.analysis) analyzer.enqueue([r.insertId]);
    return ok(res, { id: r.insertId });
  } catch (err) {
    return next(err);
  }
});

// PUT /questions/:id：修改题目（解析为空 → 置 none 并入 AI 队列；非空 → 存值置 done） [manage]
router.put('/questions/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    const [rows] = await pool.query('SELECT id, bank_id FROM quiz_question WHERE id = ?', [Number(req.params.id) || 0]);
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
    const bank = await findManageBank(rows[0].bank_id, req);
    if (!bank) return fail(res, 404, 40400, '题目不存在');
    const v = validateQuestionPayload(req.body || {});
    if (v.error) return fail(res, 400, 40030, v.error);
    await pool.query(
      'UPDATE quiz_question SET type = ?, content = ?, options = ?, answer = ?, analysis = ?, analysis_status = ? WHERE id = ?',
      [v.type, v.content, JSON.stringify(v.options), v.answer, v.analysis || null, v.analysis ? 'done' : 'none', rows[0].id]
    );
    if (!v.analysis) analyzer.enqueue([rows[0].id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /questions/:id：删除题目（连带删答题记录/错题/收藏） [manage]
router.delete('/questions/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    const [rows] = await pool.query('SELECT id, bank_id FROM quiz_question WHERE id = ?', [Number(req.params.id) || 0]);
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
    const bank = await findManageBank(rows[0].bank_id, req);
    if (!bank) return fail(res, 404, 40400, '题目不存在');
    await pool.query('DELETE FROM quiz_record WHERE question_id = ?', [rows[0].id]);
    await pool.query('DELETE FROM quiz_wrong WHERE question_id = ?', [rows[0].id]);
    await pool.query('DELETE FROM quiz_favorite WHERE question_id = ?', [rows[0].id]);
    await pool.query('DELETE FROM quiz_question WHERE id = ?', [rows[0].id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /banks/:id/analyze-retry：把该库解析状态为 none/failed 的题全部重新入 AI 队列 [manage]
router.post('/banks/:id/analyze-retry', requireQuizAdmin, async (req, res, next) => {
  try {
    const bank = await findManageBank(req.params.id, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const queued = await analyzer.enqueueBank(bank.id, ['none', 'failed']);
    return ok(res, { queued });
  } catch (err) {
    return next(err);
  }
});

// GET /practice/questions：刷题取题（默认严禁带答案与解析；非 lite 出参含 fav 收藏标记）
// mode=seq 顺序（按 sort,id 分页）；mode=rand 随机（ORDER BY RAND()，忽略 offset）；
// mode=wrong 错题本（最近答错倒序）/ mode=fav 收藏（最近收藏倒序）——wrong/fav 的 bankId 均为可选过滤
// lite=1：答题卡大纲模式，忽略 offset/limit 返回全量有序列表，项仅含 { id, type }（withAnswer 同时传入时忽略）
// withAnswer=1：背题模式用，分页 list 项追加 answer 与 analysis（无解析为 null）；不传则出参不含答案/解析
// withRecord=1：作答记录恢复用（seq/rand 刷题重进恢复）——lite 项已答则追加 myRight（1 对 0 错）；
// 分页项已答则追加 myRight/myAnswer（本人最近作答）并放行 answer/analysis（本人已答过的题不再是秘密），未答题仍不带答案
// 可见性：bankId 必传时经 findVisibleBank 校验；wrong/fav 模式只回可见题库（全部池 + 本班班组池）的题
router.get('/practice/questions', async (req, res, next) => {
  try {
    const mode = ['seq', 'rand', 'wrong', 'fav'].includes(req.query.mode) ? req.query.mode : 'seq';
    const limit = Math.min(50, Math.max(1, parseInt(req.query.limit, 10) || 20));
    const offset = Math.max(0, parseInt(req.query.offset, 10) || 0);
    const bankId = Number(req.query.bankId) || 0;
    const lite = req.query.lite === '1';
    const withAnswer = !lite && req.query.withAnswer === '1'; // lite 时忽略 withAnswer
    const withRecord = req.query.withRecord === '1';
    if (mode !== 'wrong' && mode !== 'fav' && !bankId) return fail(res, 400, 40030, '请选择题库');
    if (bankId) {
      const bank = await findVisibleBank(bankId, req);
      if (!bank) return fail(res, 404, 40400, '题库不存在');
    }
    // lite 只取 id/type；withAnswer/withRecord 追加 answer/analysis（withRecord 仅对本人已答题放行出参）；rand 原有逻辑忽略 offset，非 lite 时仍需 LIMIT
    const cols = lite ? 'id, type' : `id, type, content, options${withAnswer || withRecord ? ', answer, analysis' : ''}`;
    const pageClause = lite ? '' : `LIMIT ${limit} OFFSET ${offset}`;
    let total = 0;
    let rows = [];
    if (mode === 'seq') {
      const [cnt] = await pool.query(
        'SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1',
        [bankId]
      );
      total = Number(cnt[0].total);
      [rows] = await pool.query(
        `SELECT ${cols} FROM quiz_question WHERE bank_id = ? AND status = 1
         ORDER BY sort, id ${pageClause}`,
        [bankId]
      );
    } else if (mode === 'rand') {
      const [cnt] = await pool.query(
        'SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1',
        [bankId]
      );
      total = Number(cnt[0].total);
      [rows] = await pool.query(
        `SELECT ${cols} FROM quiz_question WHERE bank_id = ? AND status = 1
         ORDER BY RAND() ${lite ? '' : `LIMIT ${limit}`}`,
        [bankId]
      );
    } else if (mode === 'fav') {
      // 收藏：联表取题，仅可见题库（同 wrong 口径），按最近收藏倒序分页
      const teamId = req.team ? req.team.id : 0;
      const where = bankId ? 'AND f.bank_id = ?' : '';
      const params = bankId ? [req.user.id, teamId, bankId] : [req.user.id, teamId];
      const [cnt] = await pool.query(
        `SELECT COUNT(*) AS total FROM quiz_favorite f
         JOIN quiz_question q ON q.id = f.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = f.bank_id
         WHERE f.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?)) ${where}`,
        params
      );
      total = Number(cnt[0].total);
      const qCols = cols.replace(/\b(id|type|content|options|answer|analysis)\b/g, 'q.$1');
      [rows] = await pool.query(
        `SELECT ${qCols} FROM quiz_favorite f
         JOIN quiz_question q ON q.id = f.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = f.bank_id
         WHERE f.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?)) ${where}
         ORDER BY f.id DESC ${pageClause}`,
        params
      );
    } else {
      // 错题本：联表取题，仅可见题库（全部池 + 本班班组池；teamId=0 时班组池部分自然为空，last_wrong_at 倒序分页）
      const teamId = req.team ? req.team.id : 0;
      const where = bankId ? 'AND w.bank_id = ?' : '';
      const params = bankId ? [req.user.id, teamId, bankId] : [req.user.id, teamId];
      const [cnt] = await pool.query(
        `SELECT COUNT(*) AS total FROM quiz_wrong w
         JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = w.bank_id
         WHERE w.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?)) ${where}`,
        params
      );
      total = Number(cnt[0].total);
      const qCols = cols.replace(/\b(id|type|content|options|answer|analysis)\b/g, 'q.$1');
      [rows] = await pool.query(
        `SELECT ${qCols} FROM quiz_wrong w
         JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = w.bank_id
         WHERE w.user_id = ? AND (b.scope = 'all' OR (b.scope = 'team' AND b.team_id = ?)) ${where}
         ORDER BY w.last_wrong_at DESC, w.id DESC ${pageClause}`,
        params
      );
    }
    // 本批题目的本人收藏标记（lite 大纲不带）
    let favSet = new Set();
    if (!lite && rows.length) {
      const [frows] = await pool.query(
        'SELECT question_id FROM quiz_favorite WHERE user_id = ? AND question_id IN (?)',
        [req.user.id, rows.map((r) => r.id)]
      );
      favSet = new Set(frows.map((f) => f.question_id));
    }
    // 本批/本大纲题目的本人最近作答（withRecord=1 记录恢复）
    const recMap = withRecord ? await loadMyLatestRecords(req.user.id, rows.map((r) => r.id)) : {};
    // 出参：lite 仅 id/type（withRecord 已答追加 myRight）；默认严格不含 answer/analysis；withAnswer 或 withRecord 命中已答才追加
    const list = rows.map((r) => {
      const rec = recMap[r.id];
      if (lite) {
        const item = { id: r.id, type: r.type };
        if (rec) item.myRight = rec.right;
        return item;
      }
      const item = { id: r.id, type: r.type, content: r.content, options: parseOptions(r.options), fav: favSet.has(r.id) ? 1 : 0 };
      if (rec) {
        item.myRight = rec.right;
        item.myAnswer = rec.answer;
      }
      if (withAnswer || rec) {
        item.answer = r.answer;
        item.analysis = r.analysis || null;
      }
      return item;
    });
    return ok(res, { total, list });
  } catch (err) {
    return next(err);
  }
});

// POST /practice/reset：清空当前用户在某题库的做题记录（body { bankId } 必传，findVisibleBank 校验）
// 只删 quiz_record 练习记录，错题本 quiz_wrong 保留
router.post('/practice/reset', async (req, res, next) => {
  try {
    const bankId = Number(req.body && req.body.bankId) || 0;
    if (!bankId) return fail(res, 400, 40030, '请选择题库');
    const bank = await findVisibleBank(bankId, req);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const [r] = await pool.query('DELETE FROM quiz_record WHERE user_id = ? AND bank_id = ?', [req.user.id, bankId]);
    return ok(res, { cleared: Number(r.affectedRows) || 0 }, '已清空做题记录');
  } catch (err) {
    return next(err);
  }
});

// PUT /practice/analysis：修改题目解析（全员可改——刷题中纠错/补充解析，保存后即刻对题库内全员生效）
// body { questionId, analysis }：解析为空 → 置 none 并入 AI 队列重新生成（与管理编辑同口径）；非空 → 存值置 done
router.put('/practice/analysis', async (req, res, next) => {
  try {
    const questionId = Number(req.body && req.body.questionId);
    if (!questionId) return fail(res, 400, 40030, '参数不完整');
    const [rows] = await pool.query('SELECT id, bank_id FROM quiz_question WHERE id = ? AND status = 1', [questionId]);
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
    /* 题目所属题库需对用户可见（同作答口径） */
    const bank = await findVisibleBank(rows[0].bank_id, req);
    if (!bank) return fail(res, 404, 40400, '题目不存在');
    const analysis = String((req.body && req.body.analysis) || '').trim().slice(0, 2000);
    if (analysis) {
      await pool.query("UPDATE quiz_question SET analysis = ?, analysis_status = 'done' WHERE id = ?", [analysis, questionId]);
    } else {
      await pool.query("UPDATE quiz_question SET analysis = NULL, analysis_status = 'none' WHERE id = ?", [questionId]);
      analyzer.enqueue([questionId]);
    }
    return ok(res, { analysis: analysis || null }, analysis ? '解析已更新' : '已清空解析，将重新生成');
  } catch (err) {
    return next(err);
  }
});

// POST /practice/answer：提交作答判分（单选/判断精确等；多选集合相等即对，漏选判错）
// 写答题记录 + 维护错题本（答错 upsert；答对连对 +1，达 3 移出）
router.post('/practice/answer', async (req, res, next) => {
  try {
    const questionId = Number(req.body && req.body.questionId);
    if (!questionId) return fail(res, 400, 40030, '参数不完整');
    const [rows] = await pool.query(
      'SELECT id, bank_id, answer, analysis FROM quiz_question WHERE id = ? AND status = 1',
      [questionId]
    );
    const q = rows[0];
    if (!q) return fail(res, 404, 40400, '题目不存在');
    // 题目所属题库需对用户可见（全部池全员可答；班组池仅本班，防跨班直答题）
    const bank = await findVisibleBank(q.bank_id, req);
    if (!bank) return fail(res, 404, 40400, '题目不存在');
    const userAnswer = normalizeUserAnswer(req.body && req.body.answer);
    if (!userAnswer) return fail(res, 400, 40030, '请作答后再提交');
    const right = userAnswer === q.answer ? 1 : 0;
    await pool.query(
      'INSERT INTO quiz_record (user_id, bank_id, question_id, is_right, user_answer) VALUES (?, ?, ?, ?, ?)',
      [req.user.id, q.bank_id, q.id, right, userAnswer]
    );
    let wrong;
    if (!right) {
      // 答错：upsert 错题本（累计答错次数 +1、连对清零、刷新最近答错时间）
      await pool.query(
        `INSERT INTO quiz_wrong (user_id, bank_id, question_id, wrong_count, right_streak, last_wrong_at)
         VALUES (?, ?, ?, 1, 0, NOW())
         ON DUPLICATE KEY UPDATE wrong_count = wrong_count + 1, right_streak = 0, last_wrong_at = NOW()`,
        [req.user.id, q.bank_id, q.id]
      );
      wrong = { inBook: true, rightStreak: 0, removed: false };
    } else {
      // 答对：在本子中则连对 +1，达 3 移出；不在本子无需处理
      const [wrows] = await pool.query(
        'SELECT id, right_streak FROM quiz_wrong WHERE user_id = ? AND question_id = ?',
        [req.user.id, q.id]
      );
      if (!wrows.length) {
        wrong = { inBook: false, rightStreak: 0, removed: false };
      } else {
        const streak = Number(wrows[0].right_streak) + 1;
        if (streak >= 3) {
          await pool.query('DELETE FROM quiz_wrong WHERE id = ?', [wrows[0].id]);
          wrong = { inBook: false, rightStreak: streak, removed: true };
        } else {
          await pool.query('UPDATE quiz_wrong SET right_streak = ? WHERE id = ?', [streak, wrows[0].id]);
          wrong = { inBook: true, rightStreak: streak, removed: false };
        }
      }
    }
    // 本题作答统计（含本次提交；本人/全员两个口径，刷题页解析态展示）
    const [allSt] = await pool.query(
      'SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE question_id = ?',
      [q.id]
    );
    const [mySt] = await pool.query(
      'SELECT COUNT(*) AS total, SUM(is_right) AS rights FROM quiz_record WHERE question_id = ? AND user_id = ?',
      [q.id, req.user.id]
    );
    const allTimes = Number(allSt[0].total) || 0;
    const myTimes = Number(mySt[0].total) || 0;
    const stats = {
      myTimes,
      myRightRate: myTimes ? Math.round((Number(mySt[0].rights) / myTimes) * 100) : null,
      allTimes,
      allRightRate: allTimes ? Math.round((Number(allSt[0].rights) / allTimes) * 100) : null,
    };
    return ok(res, { right: !!right, answer: q.answer, analysis: q.analysis || null, wrong, stats });
  } catch (err) {
    return next(err);
  }
});

// GET /wrongs：当前用户错题本（按人跨池，最近答错倒序，含来源题库；bankId 可选过滤，错题跟着题库走）
router.get('/wrongs', async (req, res, next) => {
  try {
    const bankId = Number(req.query.bankId) || 0;
    const where = bankId ? 'AND w.bank_id = ?' : '';
    const params = bankId ? [req.user.id, bankId] : [req.user.id];
    const [rows] = await pool.query(
      `SELECT w.question_id, w.bank_id, b.name AS bank_name, q.type, q.content, q.options,
         w.wrong_count, w.right_streak, DATE_FORMAT(w.last_wrong_at, '%Y-%m-%d %H:%i:%s') AS last_wrong_at
       FROM quiz_wrong w
       JOIN quiz_question q ON q.id = w.question_id
       JOIN quiz_bank b ON b.id = w.bank_id
       WHERE w.user_id = ? ${where}
       ORDER BY w.last_wrong_at DESC, w.id DESC`,
      params
    );
    const list = rows.map((r) => ({
      questionId: r.question_id,
      bankId: r.bank_id,
      bankName: r.bank_name,
      type: r.type,
      content: r.content,
      options: parseOptions(r.options),
      wrongCount: r.wrong_count,
      rightStreak: r.right_streak,
      lastWrongAt: r.last_wrong_at,
    }));
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// DELETE /wrongs/:questionId：把某题移出当前用户错题本（按人，与班组无关）
router.delete('/wrongs/:questionId', async (req, res, next) => {
  try {
    await pool.query('DELETE FROM quiz_wrong WHERE user_id = ? AND question_id = ?', [
      req.user.id,
      Number(req.params.questionId) || 0,
    ]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /favorites/:questionId：收藏某题（幂等；题目所属题库需对用户可见，bank_id 随题落库）
router.post('/favorites/:questionId', async (req, res, next) => {
  try {
    const [rows] = await pool.query(
      'SELECT id, bank_id FROM quiz_question WHERE id = ? AND status = 1',
      [Number(req.params.questionId) || 0]
    );
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
    const bank = await findVisibleBank(rows[0].bank_id, req);
    if (!bank) return fail(res, 404, 40400, '题目不存在');
    await pool.query(
      'INSERT IGNORE INTO quiz_favorite (user_id, bank_id, question_id) VALUES (?, ?, ?)',
      [req.user.id, rows[0].bank_id, rows[0].id]
    );
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /favorites/:questionId：取消收藏（按人，与班组无关）
router.delete('/favorites/:questionId', async (req, res, next) => {
  try {
    await pool.query('DELETE FROM quiz_favorite WHERE user_id = ? AND question_id = ?', [
      req.user.id,
      Number(req.params.questionId) || 0,
    ]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
