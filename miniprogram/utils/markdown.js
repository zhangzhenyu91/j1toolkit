// 通知推送 Markdown 渲染配置：markdown-it 实例 + mp-html 排版样式（包豪斯几何色板）
// MD_TAG_STYLE/MD_CONTAINER_STYLE 为全端唯一来源，pkg-callme 对话页同用（chat.js 仅覆盖容器字号，
// 并以 breaks:false 复用渲染）；breaks:true 兼容历史多行纯文本通知的换行
const MarkdownIt = require('markdown-it');

// html:false —— 不透传原始 HTML 标签，防止通知内容中的标签被原样注入
const mdBreaks = new MarkdownIt({ html: false, linkify: true, breaks: true });
// breaks:false（markdown-it 默认换行规则）：Call Me 对话页口径
const mdPlain = new MarkdownIt({ html: false, linkify: true, breaks: false });

const MD_TAG_STYLE = {
  h1: 'font-size:17px;font-weight:600;margin:10px 0 6px;color:#1D2129',
  h2: 'font-size:16px;font-weight:600;margin:10px 0 6px;color:#1D2129',
  h3: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#1D2129',
  h4: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#1D2129',
  p: 'margin:4px 0',
  ul: 'margin:4px 0 4px 1.2em;padding:0',
  ol: 'margin:4px 0 4px 1.2em;padding:0',
  li: 'margin:2px 0',
  table: 'border-collapse:collapse;margin:8px 0;font-size:13px;display:block;overflow-x:auto',
  th: 'border:1px solid #E5E6EB;background:#F5F7FA;padding:6px 10px;font-weight:600;text-align:left;white-space:nowrap;color:#1D2129',
  td: 'border:1px solid #E5E6EB;padding:6px 10px',
  code: 'font-family:Menlo,Consolas,monospace;font-size:0.9em;color:#0A3592',
  pre: 'background:#F5F7FA;border:1px solid #E5E6EB;border-radius:8px;padding:10px;margin:8px 0;overflow-x:auto',
  blockquote: 'border-left:3px solid #0E3DA8;margin:6px 0;padding:2px 10px;color:#4E5969;background:#F5F7FA',
  a: 'color:#0E3DA8;text-decoration:underline',
  strong: 'font-weight:600',
  hr: 'border:none;border-top:1px solid #E5E6EB;margin:10px 0',
  img: 'display:block;max-width:100%;border-radius:8px;margin:6px 0',
};
const MD_CONTAINER_STYLE = 'font-size:14px;line-height:1.7;color:#1D2129;word-break:break-word;';

// options.breaks：默认 true（兼容历史多行纯文本通知）；false 走 markdown-it 默认换行规则
function pickMd(options) {
  return options && options.breaks === false ? mdPlain : mdBreaks;
}

// 渲染 Markdown（失败时抛出，供需自定义回退策略的调用方使用，如 Call Me 对话页回退纯文本展示）
function renderMarkdownRaw(text, options) {
  return pickMd(options).render(String(text || ''));
}

// 渲染 Markdown 为 HTML（供 mp-html content 属性），失败回退转义纯文本
function renderMarkdown(text, options) {
  const src = String(text || '');
  try {
    return pickMd(options).render(src);
  } catch (err) {
    return src.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
  }
}

module.exports = { renderMarkdown, renderMarkdownRaw, MD_TAG_STYLE, MD_CONTAINER_STYLE };
