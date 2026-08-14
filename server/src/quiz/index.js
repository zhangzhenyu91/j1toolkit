// 题库刷题路由：全部接口需登录 + quiz 应用权限 + 生效班组（req.team，见 utils/team.js）；
// 管理接口（[manage]）超管可管任意班组（?team_id= 指定），班组管理员仅本班（照 worklog requireDictAdmin 口径）
// req.team 为 null（未分配班组）时：只读接口返回空 list/零统计，写操作 400
const express = require('express');
const multer = require('multer');
const XLSX = require('xlsx');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const analyzer = require('./analyzer');

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

// 题库管理权限：超管任意班组（req.team 由 ?team_id= 解析），班组管理员仅本班
function requireQuizAdmin(req, res, next) {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
}

const LETTERS = ['A', 'B', 'C', 'D', 'E', 'F'];

// 选项 JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 worklog members 同口径）
function parseOptions(raw) {
  if (!raw) return [];
  const arr = typeof raw === 'string' ? JSON.parse(raw) : raw;
  return Array.isArray(arr) ? arr : [];
}

// 取当前生效班组下的题库（防跨班越权）
async function findTeamBank(bankId, teamId) {
  const [rows] = await pool.query('SELECT id, team_id FROM quiz_bank WHERE id = ?', [Number(bankId) || 0]);
  return rows[0] && rows[0].team_id === teamId ? rows[0] : null;
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
  if (uniq.some((c) => c > maxLetter)) return { error: `答案超出选项范围（A~${maxLetter}）` };
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

// GET /overview：当前用户答题总览（记录总数、正确率%、错题本题数）
router.get('/overview', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { totalAnswered: 0, rightRate: null, wrongCount: 0 });
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

// GET /banks：当前班组启用题库列表（含题目数/解析状态统计、当前用户答题进度）
router.get('/banks', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { list: [] });
    const [banks] = await pool.query(
      'SELECT id, name, description FROM quiz_bank WHERE team_id = ? AND status = 1 ORDER BY id',
      [req.team.id]
    );
    if (!banks.length) return ok(res, { list: [] });
    const ids = banks.map((b) => b.id);
    // 各库题目数 + 四状态解析统计
    const [qstats] = await pool.query(
      `SELECT bank_id, COUNT(*) AS total,
         SUM(CASE WHEN analysis_status = 'none' THEN 1 ELSE 0 END) AS none_cnt,
         SUM(CASE WHEN analysis_status = 'pending' THEN 1 ELSE 0 END) AS pending_cnt,
         SUM(CASE WHEN analysis_status = 'failed' THEN 1 ELSE 0 END) AS failed_cnt,
         SUM(CASE WHEN analysis_status = 'done' THEN 1 ELSE 0 END) AS done_cnt
       FROM quiz_question WHERE bank_id IN (?) AND status = 1 GROUP BY bank_id`,
      [ids]
    );
    // 当前用户各库答题统计（答过题数按不同题计，正确率按答题记录计）
    const [ustats] = await pool.query(
      `SELECT bank_id, COUNT(DISTINCT question_id) AS answered, COUNT(*) AS total, SUM(is_right) AS rights
       FROM quiz_record WHERE user_id = ? AND bank_id IN (?) GROUP BY bank_id`,
      [req.user.id, ids]
    );
    const qmap = {};
    qstats.forEach((s) => { qmap[s.bank_id] = s; });
    const umap = {};
    ustats.forEach((s) => { umap[s.bank_id] = s; });
    const list = banks.map((b) => {
      const qs = qmap[b.id] || {};
      const us = umap[b.id];
      const uTotal = us ? Number(us.total) : 0;
      return {
        id: b.id,
        name: b.name,
        description: b.description,
        questionCount: Number(qs.total) || 0,
        answeredCount: us ? Number(us.answered) : 0,
        rightRate: uTotal ? Math.round((Number(us.rights) / uTotal) * 100) : null,
        analysis: {
          none: Number(qs.none_cnt) || 0,
          pending: Number(qs.pending_cnt) || 0,
          failed: Number(qs.failed_cnt) || 0,
          done: Number(qs.done_cnt) || 0,
        },
      };
    });
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// POST /banks：新建题库 [manage]
router.post('/banks', requireQuizAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const name = String((req.body && req.body.name) || '').trim().slice(0, 100);
    const description = String((req.body && req.body.description) || '').trim().slice(0, 255);
    if (!name) return fail(res, 400, 40030, '请输入题库名称');
    try {
      const [r] = await pool.query(
        'INSERT INTO quiz_bank (team_id, name, description, created_by) VALUES (?, ?, ?, ?)',
        [req.team.id, name, description, req.user.id]
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

// PUT /banks/:id：修改题库名称/简介 [manage]
router.put('/banks/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const bank = await findTeamBank(req.params.id, req.team.id);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const name = String((req.body && req.body.name) || '').trim().slice(0, 100);
    const description = String((req.body && req.body.description) || '').trim().slice(0, 255);
    if (!name) return fail(res, 400, 40030, '请输入题库名称');
    try {
      await pool.query('UPDATE quiz_bank SET name = ?, description = ? WHERE id = ?', [name, description, bank.id]);
      return ok(res, null);
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${name}」已存在`);
      throw err;
    }
  } catch (err) {
    return next(err);
  }
});

// DELETE /banks/:id：删除题库（事务连带删题目/答题记录/错题） [manage]
router.delete('/banks/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const bank = await findTeamBank(req.params.id, req.team.id);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const conn = await pool.getConnection();
    try {
      await conn.beginTransaction();
      await conn.query('DELETE FROM quiz_record WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_wrong WHERE bank_id = ?', [bank.id]);
      await conn.query('DELETE FROM quiz_question WHERE bank_id = ?', [bank.id]);
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
      ['单选', '示例：安全带的正确挂扣方式是（ ）。', '高挂低用', '低挂高用', '平挂平用', '随意挂扣', '', '', 'A', '安全带应高挂低用，坠落时冲击力更小。'],
      ['多选', '示例：下列属于个人安全防护用品的有（ ）。', '安全帽', '安全带', '绝缘手套', '普通布鞋', '', '', 'ABC', '普通布鞋不属于安全防护用品。'],
      ['判断', '示例：雷雨天气可以进行户外登塔作业。', '', '', '', '', '', '', 'B', '雷雨天气禁止户外登塔作业。'],
    ]);
    ws['!cols'] = [{ wch: 8 }, { wch: 50 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 14 }, { wch: 8 }, { wch: 40 }];
    const tips = XLSX.utils.aoa_to_sheet([
      ['题库导入填写说明'],
      ['1. 只读取第一张 sheet，首行表头固定为：题型 | 题干 | 选项A~F | 答案 | 解析。'],
      ['2. 题型列支持：单选/单选题/single、多选/多选题/multiple、判断/判断题/judge（不区分大小写）。'],
      ['3. 单选/多选题选项A~F 至少填写 2 个，按序取非空列；判断题无需填写选项，固定为「正确/错误」。'],
      ['4. 答案列：单选填 1 个字母（如 A）；多选填 2 个及以上字母（如 ABC，顺序不限）；判断填 对/错（或 A/B）。'],
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
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（兼容 formData 携带 team_id）
      const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      if (!team) return fail(res, 400, 40030, '无可用班组');
      if (req.user.role === 'team_admin' && req.user.team_id !== team.id) {
        return fail(res, 403, 40304, '仅管理员可执行此操作');
      }
      const bank = await findTeamBank(req.params.id, team.id);
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
          error = '题型无法识别（应为 单选/多选/判断）';
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
        // 全量替换：先删该库旧答题记录/错题/题目，再分批插入（sort 按行序）
        await conn.query('DELETE FROM quiz_record WHERE bank_id = ?', [bank.id]);
        await conn.query('DELETE FROM quiz_wrong WHERE bank_id = ?', [bank.id]);
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
    if (!req.team) return ok(res, { total: 0, list: [] });
    const bank = await findTeamBank(req.params.id, req.team.id);
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
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const bank = await findTeamBank(req.params.id, req.team.id);
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
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const [rows] = await pool.query(
      'SELECT q.id FROM quiz_question q JOIN quiz_bank b ON b.id = q.bank_id WHERE q.id = ? AND b.team_id = ?',
      [Number(req.params.id) || 0, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
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

// DELETE /questions/:id：删除题目（连带删答题记录/错题） [manage]
router.delete('/questions/:id', requireQuizAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const [rows] = await pool.query(
      'SELECT q.id FROM quiz_question q JOIN quiz_bank b ON b.id = q.bank_id WHERE q.id = ? AND b.team_id = ?',
      [Number(req.params.id) || 0, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '题目不存在');
    await pool.query('DELETE FROM quiz_record WHERE question_id = ?', [rows[0].id]);
    await pool.query('DELETE FROM quiz_wrong WHERE question_id = ?', [rows[0].id]);
    await pool.query('DELETE FROM quiz_question WHERE id = ?', [rows[0].id]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// POST /banks/:id/analyze-retry：把该库解析状态为 none/failed 的题全部重新入 AI 队列 [manage]
router.post('/banks/:id/analyze-retry', requireQuizAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '无可用班组');
    const bank = await findTeamBank(req.params.id, req.team.id);
    if (!bank) return fail(res, 404, 40400, '题库不存在');
    const queued = await analyzer.enqueueBank(bank.id, ['none', 'failed']);
    return ok(res, { queued });
  } catch (err) {
    return next(err);
  }
});

// GET /practice/questions：刷题取题（严禁带答案与解析）
// mode=seq 顺序（按 sort,id 分页）；mode=rand 随机（ORDER BY RAND()，忽略 offset）；mode=wrong 错题本（最近答错倒序，bankId 可选过滤）
router.get('/practice/questions', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { total: 0, list: [] });
    const mode = ['seq', 'rand', 'wrong'].includes(req.query.mode) ? req.query.mode : 'seq';
    const limit = Math.min(50, Math.max(1, parseInt(req.query.limit, 10) || 20));
    const offset = Math.max(0, parseInt(req.query.offset, 10) || 0);
    const bankId = Number(req.query.bankId) || 0;
    if (mode !== 'wrong' && !bankId) return fail(res, 400, 40030, '请选择题库');
    if (bankId) {
      const bank = await findTeamBank(bankId, req.team.id);
      if (!bank) return fail(res, 404, 40400, '题库不存在');
    }
    let total = 0;
    let rows = [];
    if (mode === 'seq') {
      const [cnt] = await pool.query(
        'SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1',
        [bankId]
      );
      total = Number(cnt[0].total);
      [rows] = await pool.query(
        `SELECT id, type, content, options FROM quiz_question WHERE bank_id = ? AND status = 1
         ORDER BY sort, id LIMIT ${limit} OFFSET ${offset}`,
        [bankId]
      );
    } else if (mode === 'rand') {
      const [cnt] = await pool.query(
        'SELECT COUNT(*) AS total FROM quiz_question WHERE bank_id = ? AND status = 1',
        [bankId]
      );
      total = Number(cnt[0].total);
      [rows] = await pool.query(
        `SELECT id, type, content, options FROM quiz_question WHERE bank_id = ? AND status = 1
         ORDER BY RAND() LIMIT ${limit}`,
        [bankId]
      );
    } else {
      // 错题本：联表取题，仅当前班组题库（last_wrong_at 倒序分页）
      const where = bankId ? 'AND w.bank_id = ?' : '';
      const params = bankId ? [req.user.id, req.team.id, bankId] : [req.user.id, req.team.id];
      const [cnt] = await pool.query(
        `SELECT COUNT(*) AS total FROM quiz_wrong w
         JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = w.bank_id
         WHERE w.user_id = ? AND b.team_id = ? ${where}`,
        params
      );
      total = Number(cnt[0].total);
      [rows] = await pool.query(
        `SELECT q.id, q.type, q.content, q.options FROM quiz_wrong w
         JOIN quiz_question q ON q.id = w.question_id AND q.status = 1
         JOIN quiz_bank b ON b.id = w.bank_id
         WHERE w.user_id = ? AND b.team_id = ? ${where}
         ORDER BY w.last_wrong_at DESC, w.id DESC LIMIT ${limit} OFFSET ${offset}`,
        params
      );
    }
    // 出参严格不含 answer/analysis
    const list = rows.map((r) => ({ id: r.id, type: r.type, content: r.content, options: parseOptions(r.options) }));
    return ok(res, { total, list });
  } catch (err) {
    return next(err);
  }
});

// POST /practice/answer：提交作答判分（单选/判断精确等；多选集合相等即对，漏选判错）
// 写答题记录 + 维护错题本（答错 upsert；答对连对 +1，达 3 移出）
router.post('/practice/answer', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '未分配班组，请联系管理员分配');
    const questionId = Number(req.body && req.body.questionId);
    if (!questionId) return fail(res, 400, 40030, '参数不完整');
    const [rows] = await pool.query(
      `SELECT q.id, q.bank_id, q.answer, q.analysis FROM quiz_question q
       JOIN quiz_bank b ON b.id = q.bank_id
       WHERE q.id = ? AND q.status = 1 AND b.team_id = ?`,
      [questionId, req.team.id]
    );
    const q = rows[0];
    if (!q) return fail(res, 404, 40400, '题目不存在');
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
    return ok(res, { right: !!right, answer: q.answer, analysis: q.analysis || null, wrong });
  } catch (err) {
    return next(err);
  }
});

// GET /wrongs：当前用户错题本（按最近答错倒序，仅当前班组题库）
router.get('/wrongs', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { list: [] });
    const [rows] = await pool.query(
      `SELECT w.question_id, w.bank_id, b.name AS bank_name, q.type, q.content, q.options,
         w.wrong_count, w.right_streak, DATE_FORMAT(w.last_wrong_at, '%Y-%m-%d %H:%i:%s') AS last_wrong_at
       FROM quiz_wrong w
       JOIN quiz_question q ON q.id = w.question_id
       JOIN quiz_bank b ON b.id = w.bank_id
       WHERE w.user_id = ? AND b.team_id = ?
       ORDER BY w.last_wrong_at DESC, w.id DESC`,
      [req.user.id, req.team.id]
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

// DELETE /wrongs/:questionId：把某题移出当前用户错题本
router.delete('/wrongs/:questionId', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40030, '未分配班组，请联系管理员分配');
    await pool.query('DELETE FROM quiz_wrong WHERE user_id = ? AND question_id = ?', [
      req.user.id,
      Number(req.params.questionId) || 0,
    ]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
