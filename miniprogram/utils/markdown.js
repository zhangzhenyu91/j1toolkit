// 通知推送 Markdown 渲染配置：markdown-it 实例 + mp-html 排版样式（方案五色板）
// 与 pkg-callme/pages/chat/chat.js 的 MD_TAG_STYLE 同口径；breaks:true 兼容历史多行纯文本通知的换行
const MarkdownIt = require('markdown-it');

// html:false —— 不透传原始 HTML 标签，防止通知内容中的标签被原样注入
const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

const MD_TAG_STYLE = {
  h1: 'font-size:17px;font-weight:600;margin:10px 0 6px;color:#22314E',
  h2: 'font-size:16px;font-weight:600;margin:10px 0 6px;color:#22314E',
  h3: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#22314E',
  h4: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#22314E',
  p: 'margin:4px 0',
  ul: 'margin:4px 0 4px 1.2em;padding:0',
  ol: 'margin:4px 0 4px 1.2em;padding:0',
  li: 'margin:2px 0',
  table: 'border-collapse:collapse;margin:8px 0;font-size:13px;display:block;overflow-x:auto',
  th: 'border:1px solid #E8E0CD;background:#FDF6EF;padding:6px 10px;font-weight:600;text-align:left;white-space:nowrap;color:#22314E',
  td: 'border:1px solid #E8E0CD;padding:6px 10px',
  code: 'font-family:Menlo,Consolas,monospace;font-size:0.9em;color:#D85A12',
  pre: 'background:#F7F3EA;border:1px solid #E8E0CD;border-radius:8px;padding:10px;margin:8px 0;overflow-x:auto',
  blockquote: 'border-left:3px solid #F26D21;margin:6px 0;padding:2px 10px;color:#6B7690;background:#FDF6EF',
  a: 'color:#F26D21;text-decoration:underline',
  strong: 'font-weight:600',
  hr: 'border:none;border-top:1px solid #E8E0CD;margin:10px 0',
  img: 'display:block;max-width:100%;border-radius:8px;margin:6px 0',
};
const MD_CONTAINER_STYLE = 'font-size:14px;line-height:1.7;color:#22314E;word-break:break-word;';

// 渲染 Markdown 为 HTML（供 mp-html content 属性），失败回退转义纯文本
function renderMarkdown(text) {
  const src = String(text || '');
  try {
    return md.render(src);
  } catch (err) {
    return src.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
  }
}

module.exports = { renderMarkdown, MD_TAG_STYLE, MD_CONTAINER_STYLE };
