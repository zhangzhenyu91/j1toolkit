// 题库刷题：Dify「题目解析」工作流封装
// POST {DIFY_API_URL}/v1/workflows/run（blocking，/v1 由代码拼接）；
// inputs：type 题型（单选题/多选题/判断题）/ stem 题干 / options 选项汇总（多行文本，'A. 选项内容' 每行一个）/ answer 答案字母；
// 输出 outputs.analysis（解析文本）
const axios = require('axios');
const config = require('../config');
const { workflowRunUrl, createEnsureConfigured } = require('../utils/dify');

const ensureConfigured = createEnsureConfigured({
  apiUrl: config.dify.apiUrl,
  apiKey: config.quiz.difyKey,
  keyEnvName: 'DIFY_QUIZ_API_KEY',
});

// 是否已配置（未配置时 AI 解析整体停用，入队静默跳过，其余功能照常）
function isConfigured() {
  return Boolean(config.dify.apiUrl && config.quiz.difyKey);
}

const LETTERS = ['A', 'B', 'C', 'D', 'E', 'F'];
const TYPE_TEXT = { single: '单选题', multiple: '多选题', judge: '判断题' };

// 生成单题解析，成功返回非空解析文本；失败抛异常（由 analyzer 归 failed，不抛到路由）
async function analyzeQuestion({ id, type, content, options, answer }) {
  ensureConfigured();
  // 选项汇总成一个多行文本变量：'A. 选项内容\nB. 选项内容'
  const optionsText = (options || []).map((o, i) => `${LETTERS[i]}. ${o}`).join('\n');
  // 判断题答案附中文标注（A=正确 B=错误），便于工作流理解
  const answerText = type === 'judge' ? (answer === 'A' ? 'A（正确）' : 'B（错误）') : answer;
  const res = await axios.post(
    workflowRunUrl(config.dify.apiUrl),
    {
      inputs: {
        type: TYPE_TEXT[type] || type,
        stem: content,
        options: optionsText,
        answer: answerText,
      },
      response_mode: 'blocking',
      user: `quiz-q${id}`,
    },
    {
      headers: {
        Authorization: `Bearer ${config.quiz.difyKey}`,
        'Content-Type': 'application/json',
      },
      timeout: 90000,
    }
  );
  const outputs = (res.data && res.data.data && res.data.data.outputs) || {};
  let analysis = outputs.analysis;
  // 容错：analysis 缺失/为空时取 outputs 第一个非空字符串值
  if (typeof analysis !== 'string' || !analysis.trim()) {
    analysis = Object.values(outputs).find((v) => typeof v === 'string' && v.trim());
  }
  if (!analysis || !String(analysis).trim()) throw new Error('Dify 返回的解析为空');
  return String(analysis).trim();
}

module.exports = { analyzeQuestion, isConfigured, LETTERS };
