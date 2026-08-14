// 题库刷题：AI 解析进程内队列（全局并发限 3）
// Set 防重复入队 + 3 个 worker 循环取题：取题置 pending，成功写 analysis + done，异常置 failed；
// 未配置 Dify 时入队静默跳过（返回 0），解析功能整体停用，刷题等其余功能不受影响
const { pool } = require('../db');
const dify = require('./dify');

const WORKERS = 3; // 全局并发上限（硬性要求）

const inQueue = new Set(); // 已入队/处理中的题目 id（防重复入队）
const queue = []; // 待处理题目 id
const waiters = []; // 休眠中的 worker 唤醒回调
let started = false;

function notify() {
  const wake = waiters.shift();
  if (wake) wake();
}

// 处理单题：任何异常都归为 failed（不抛出，worker 循环不中断）
async function processOne(id) {
  try {
    const [rows] = await pool.query(
      'SELECT id, type, content, options, answer FROM quiz_question WHERE id = ?',
      [id]
    );
    const q = rows[0];
    if (!q) return; // 题已被删除（如导入全量替换）
    await pool.query("UPDATE quiz_question SET analysis_status = 'pending' WHERE id = ?", [id]);
    // JSON 列防御解析（mysql2 可能返回字符串或已解析对象）
    const options = typeof q.options === 'string' ? JSON.parse(q.options) : q.options;
    const analysis = await dify.analyzeQuestion({
      id: q.id,
      type: q.type,
      content: q.content,
      options: Array.isArray(options) ? options : [],
      answer: q.answer,
    });
    await pool.query("UPDATE quiz_question SET analysis = ?, analysis_status = 'done' WHERE id = ?", [analysis, id]);
  } catch (err) {
    // Dify 的 4xx 响应体含具体原因（invalid_param 等），打出便于排查
    const detail = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
    console.error(`[题库刷题] 题目 ${id} AI 解析失败：`, detail);
    await pool.query("UPDATE quiz_question SET analysis_status = 'failed' WHERE id = ?", [id]).catch(() => {});
  } finally {
    inQueue.delete(id);
  }
}

async function workerLoop(idx) {
  for (;;) {
    const id = queue.shift();
    if (id === undefined) {
      await new Promise((resolve) => waiters.push(resolve));
      continue;
    }
    await processOne(id).catch((err) => console.error(`[题库刷题] 解析 worker${idx} 异常：`, err.message));
  }
}

function startWorkers() {
  if (started) return;
  started = true;
  for (let i = 0; i < WORKERS; i += 1) {
    workerLoop(i + 1).catch((err) => console.error('[题库刷题] 解析 worker 退出：', err.message));
  }
}

// 批量入队，返回实际入队数（重复/未配置 Dify 的跳过）
function enqueue(questionIds) {
  if (!dify.isConfigured()) return 0;
  startWorkers();
  let count = 0;
  for (const id of questionIds || []) {
    const qid = Number(id);
    if (!qid || inQueue.has(qid)) continue;
    inQueue.add(qid);
    queue.push(qid);
    count += 1;
  }
  for (let i = 0; i < count; i += 1) notify();
  return count;
}

// 按题库 + 解析状态批量入队（如导入后补 none、失败后重试 none/failed）
async function enqueueBank(bankId, statuses = ['none']) {
  if (!dify.isConfigured()) return 0;
  const [rows] = await pool.query(
    'SELECT id FROM quiz_question WHERE bank_id = ? AND status = 1 AND analysis_status IN (?)',
    [Number(bankId) || 0, statuses]
  );
  return enqueue(rows.map((r) => r.id));
}

module.exports = { enqueue, enqueueBank };
