// WeKnora Agent 步骤树格式化：实时 SSE 事件与历史消息（agent_steps）共用，
// 产出「展示就绪」的步骤节点，小程序/Web 两端只做渲染、不含格式化逻辑。
// 文案口径对齐官方前端（frontend AgentStreamDisplay.vue + zh-CN 语言包）。

// 工具名 → 展示名（进行中）
// 全量对齐官方前端 TOOL_NAME_KEYS（AgentStreamDisplay.vue + zh-CN 语言包）；
// search_conversations / search_memory / write_skill_file / edit_skill_file 官方聊天流也无
// 中文名（走英文兜底），此处按「全部显示中文」要求自补
const TOOL_LABEL = {
  knowledge_search: '知识库检索',
  search_knowledge: '知识库检索',
  grep_chunks: '搜索关键词',
  web_search: '网络搜索',
  web_fetch: '网页抓取',
  get_document_info: '获取文档信息',
  get_document_content: '获取文档内容',
  list_documents: '浏览文档列表',
  read_document: '阅读文档',
  get_related_documents: '查找相关文档',
  list_knowledge_chunks: '查看知识分块',
  wiki_search: '搜索 Wiki',
  wiki_read_page: '阅读 Wiki 页面',
  wiki_flag_issue: '标记 Wiki 问题',
  wiki_write_page: '创建/覆盖 Wiki',
  wiki_replace_text: '局部替换 Wiki',
  wiki_rename_page: '重命名 Wiki',
  wiki_delete_page: '删除 Wiki',
  wiki_read_issue: '查看 Wiki 问题',
  wiki_update_issue: '更新 Wiki 问题',
  wiki_read_source_doc: '精读源文档',
  todo_write: '计划管理',
  thinking: '思考',
  ask_user: '向用户提问',
  image_analysis: '查看图片内容',
  attachment_parsing: '解析附件',
  query_understand: '理解问题',
  query_knowledge_graph: '知识图谱查询',
  knowledge_graph_extract: '知识图谱抽取',
  read_file: '读取文件',
  list_sandbox_files: '列出沙箱文件',
  read_sandbox_file: '读取沙箱文件',
  write_sandbox_file: '写入沙箱文件',
  edit_sandbox_file: '编辑沙箱文件',
  shell_exec: '执行沙箱命令',
  read_skill: '读取技能',
  execute_skill_script: '执行技能脚本',
  write_skill_file: '写入技能文件',
  edit_skill_file: '编辑技能文件',
  data_analysis: '数据分析',
  data_schema: '数据结构',
  database_query: '数据库查询',
  find_operations: '查找操作',
  run_operation: '执行操作',
  try_agent: '试跑智能体',
  search_conversations: '回顾历史对话',
  search_memory: '搜索记忆',
  open_page: '打开页面',
  read_page: '查看当前页面',
  select_tab: '切换标签',
  fill_page: '填写页面',
  search_weknora_docs: '搜索产品文档',
  read_weknora_doc: '阅读产品文档',
  local_browser: '本机浏览器',
  discover_mcp_tools: '发现 MCP 工具',
  call_mcp_tool: 'MCP 工具',
};

// 工具名 → 完成态描述（官方 toolStatus 文案；专属失败文案为「{描述}失败」）
const TOOL_DONE_LABEL = {
  knowledge_search: '检索知识库',
  search_knowledge: '检索知识库',
  grep_chunks: '搜索关键词',
  web_search: '网络搜索',
  get_document_info: '获取文档信息',
  list_documents: '获取文档信息', // 官方 Web 端复用 getDocInfo 文案
  read_document: '查看文档',
  get_document_content: '查看文档',
  wiki_read_source_doc: '查看文档',
  todo_write: '更新任务列表',
  thinking: '完成思考',
  image_analysis: '已查看图片内容',
  attachment_parsing: '已解析附件',
  query_understand: '已完成问题理解',
};

// 完成态直显中文名的工具（官方 getToolDescription 口径：成功直显、失败仍按「调用 {中文名} 失败」）
const DONE_DIRECT = new Set([
  'shell_exec',
  'execute_skill_script',
  'read_skill',
  'list_sandbox_files',
  'read_file',
  'read_sandbox_file',
  'write_sandbox_file',
  'edit_sandbox_file',
  'wiki_search',
  'wiki_read_page',
]);

// 进行中标题的特殊文案（官方 toolStatus 里带进行时的三个）
const PENDING_TITLE = {
  image_analysis: '正在查看图片内容…',
  attachment_parsing: '正在解析附件…',
  query_understand: '正在理解问题…',
  shell_exec: '正在执行沙箱命令…',
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
  const direct = DONE_DIRECT.has(name);
  const label = TOOL_DONE_LABEL[name] || (direct ? toolLabel(name) : `调用 ${toolLabel(name)}`);
  // 失败口径（官方）：专属完成态文案直接 +「失败」（如 检索知识库失败）；直显族与兜底均为「调用 {中文名} 失败」
  if (!success)
    return !direct && TOOL_DONE_LABEL[name] ? `${label}失败` : `调用 ${toolLabel(name)} 失败`;
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
  pendingTitle,
  doneTitle,
  toolSummary,
  buildSteps,
};
