// WeKnora Agent 步骤树格式化：实时 SSE 事件与历史消息（agent_steps）共用，
// 产出「展示就绪」的步骤节点，小程序/Web 两端只做渲染、不含格式化逻辑。
// 文案口径对齐官方前端（frontend AgentStreamDisplay.vue + zh-CN 语言包）。

// 工具名 → 展示名（进行中）
const TOOL_LABEL = {
  knowledge_search: '知识库检索',
  search_knowledge: '知识库检索',
  grep_chunks: '搜索关键词',
  web_search: '网络搜索',
  web_fetch: '网页抓取',
  get_document_info: '获取文档信息',
  get_document_content: '获取文档内容',
  list_knowledge_chunks: '查看知识分块',
  todo_write: '计划管理',
  thinking: '思考',
  image_analysis: '查看图片内容',
  attachment_parsing: '解析附件',
  query_understand: '理解问题',
};

// 工具名 → 完成态描述（官方 toolStatus 文案）
const TOOL_DONE_LABEL = {
  knowledge_search: '检索知识库',
  search_knowledge: '检索知识库',
  grep_chunks: '搜索关键词',
  web_search: '网络搜索',
  get_document_info: '获取文档信息',
  get_document_content: '查看文档',
  list_knowledge_chunks: '查看知识分块',
  todo_write: '更新任务列表',
  thinking: '完成思考',
  image_analysis: '已查看图片内容',
  attachment_parsing: '已解析附件',
  query_understand: '已完成问题理解',
};

// 进行中标题的特殊文案（官方 toolStatus 里带进行时的三个）
const PENDING_TITLE = {
  image_analysis: '正在查看图片内容…',
  attachment_parsing: '正在解析附件…',
  query_understand: '正在理解问题…',
};

// 标题里允许附带「查询词」的搜索类工具
const QUERY_TOOLS = new Set([
  'knowledge_search',
  'search_knowledge',
  'wiki_search',
  'web_search',
  'grep_chunks',
]);

function toolLabel(name) {
  return TOOL_LABEL[name] || name || '工具';
}

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

// 进行中标题：正在调用 知识库检索…
function pendingTitle(name) {
  return PENDING_TITLE[name] || `正在调用 ${toolLabel(name)}…`;
}

// 从工具参数/结果数据中提取查询词（官方：搜索类工具标题后缀「query」）
function extractQuery(name, args, data) {
  const src = args && Object.keys(args).length ? args : data || {};
  if (!src) return '';
  if (name === 'grep_chunks') {
    let pats = [];
    if (Array.isArray(src.queries)) pats = src.queries;
    else if (Array.isArray(src.patterns)) pats = src.patterns;
    else if (src.query) pats = [src.query];
    else if (src.pattern) pats = [src.pattern];
    pats = pats.filter((p) => typeof p === 'string' && p);
    if (!pats.length) return '';
    return pats.slice(0, 2).join('、') + (pats.length > 2 ? ` +${pats.length - 2}` : '');
  }
  const q = src.query;
  if (Array.isArray(q)) return q.filter((s) => typeof s === 'string' && s).join('，');
  return typeof q === 'string' ? q : '';
}

// 完成态标题：检索知识库：「带电」/ 搜索关键词：「带电」/ 调用 xxx 失败
function doneTitle(name, args, data, success) {
  const label = TOOL_DONE_LABEL[name] || `调用 ${toolLabel(name)}`;
  if (!success) return `${label}失败`;
  if (QUERY_TOOLS.has(name)) {
    const q = extractQuery(name, args, data);
    if (q) return `${label}：「${q}」`;
  }
  return label;
}

// 结果摘要行（官方 agentStream.search 文案）：找到 30 个匹配片段，来自 24 个文档
function toolSummary(name, data) {
  if (!data || typeof data !== 'object') return '';
  const displayType = data.display_type || '';
  if (name === 'grep_chunks' || displayType === 'grep_results') {
    const chunks = num(data.total_matches);
    const docs = num(data.document_count) || num(data.result_count);
    if (!chunks) return '未找到匹配的内容';
    return `找到 ${chunks} 个匹配片段，来自 ${docs} 个文档`;
  }
  if (name === 'web_search') {
    const n = Array.isArray(data.results) ? data.results.length : num(data.count);
    return n ? `找到 ${n} 条网页` : '未找到匹配的内容';
  }
  if (name === 'knowledge_search' || name === 'search_knowledge') {
    const n = Array.isArray(data.results) ? data.results.length : num(data.count);
    if (!n) return '未找到匹配的内容';
    const kbCount = data.kb_counts ? Object.keys(data.kb_counts).length : 0;
    return kbCount ? `找到 ${n} 个结果，来自 ${kbCount} 个文件` : `找到 ${n} 个结果`;
  }
  if (displayType === 'knowledge_chunks_list') {
    const fetched = num(data.fetched_chunks);
    const total = num(data.total_chunks);
    if (total) return `已加载 ${fetched} / ${total} 个分块`;
  }
  return '';
}

// 历史消息 → 展示就绪步骤树（assistant 消息的 agent_steps）：
// 节点 { kind:'thought', text } | { kind:'tool', name, title, summary, status, durationMs }
function buildSteps(agentSteps) {
  const steps = [];
  let toolCount = 0;
  if (!Array.isArray(agentSteps)) return { steps, meta: { rounds: 0, tools: 0 } };
  for (const s of agentSteps) {
    if (!s || typeof s !== 'object') continue;
    if (s.reasoning_content) steps.push({ kind: 'thought', text: String(s.reasoning_content) });
    if (s.thought) steps.push({ kind: 'thought', text: String(s.thought) });
    if (!Array.isArray(s.tool_calls)) continue;
    for (const tc of s.tool_calls) {
      const name = (tc && tc.name) || '';
      const result = tc && tc.result;
      const success = !result || result.success !== false;
      const data = (result && result.data) || null;
      toolCount += 1;
      steps.push({
        kind: 'tool',
        name,
        status: success ? 'done' : 'error',
        title: doneTitle(name, tc.args, data, success),
        summary: success
          ? toolSummary(name, data)
          : String((result && result.error) || '').slice(0, 120),
        durationMs: num(tc.duration),
      });
    }
  }
  return { steps, meta: { rounds: agentSteps.length, tools: toolCount } };
}

module.exports = {
  toolLabel,
  pendingTitle,
  doneTitle,
  toolSummary,
  extractQuery,
  buildSteps,
};
