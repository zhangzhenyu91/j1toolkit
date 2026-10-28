/* 精简 Markdown 渲染器（ESM 版，移植自 server/public/assets/md.js，无外部依赖）。
   覆盖：标题 / 加粗 / 斜体 / 行内代码 / 围栏代码块 / 有序无序列表（缩进嵌套）/
         引用 / 链接 / 图片 / 表格 / 分割线 / 删除线 / 换行。
   安全：解析前先 esc 全量转义，渲染结果不含原始 HTML；渲染失败由调用方回退纯文本。 */

// esc HTML 全量转义（防注入的唯一闸门）。
function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// renderMd 渲染 Markdown 为 HTML 字符串；失败返回 ''（调用方兜底纯文本）。
export function renderMd(text) {
  if (!text) return '';
  try {
    return mdRender(String(text));
  } catch (e) {
    return '';
  }
}

function mdRender(src) {
  // 1. 提取围栏代码块为占位符，内部内容不参与后续解析
  const blocks = [];
  src = src.replace(/```([^\n`]*)\r?\n?([\s\S]*?)(?:```|$)/g, (m, lang, code) => {
    blocks.push({ lang: (lang || '').trim(), code: code.replace(/\n$/, '') });
    return '\n\x00B' + (blocks.length - 1) + '\x00\n';
  });

  // 2. 全量 HTML 转义
  src = esc(src);

  // 3. 行内代码占位（先于加粗/斜体，避免反引号内符号被误解析）
  const spans = [];
  src = src.replace(/`([^`\n]+)`/g, (m, code) => {
    spans.push(code);
    return '\x00S' + (spans.length - 1) + '\x00';
  });

  // 4. 块级解析
  const lines = src.split('\n');
  const out = [];
  let i = 0;
  const isBlank = (l) => /^\s*$/.test(l);
  const blockHolder = (l) => {
    const m = l.match(/^\s*\x00B(\d+)\x00\s*$/);
    return m ? +m[1] : -1;
  };
  const isTableSep = (l) => {
    if (l.indexOf('-') < 0) return false;
    const cells = splitRow(l);
    return cells.length > 0 && cells.every((c) => /^:?-+:?$/.test(c));
  };
  const isTableStart = (idx) =>
    lines[idx].indexOf('|') >= 0 && idx + 1 < lines.length && isTableSep(lines[idx + 1]);
  const isBlockStart = (l) =>
    isBlank(l) ||
    blockHolder(l) >= 0 ||
    /^\s*#{1,6}\s+/.test(l) ||
    /^\s*&gt;\s?/.test(l) ||
    /^\s*(?:[-*+]|\d+[.)])\s+/.test(l) ||
    /^\s*([-*_])(\s*\1){2,}\s*$/.test(l);

  while (i < lines.length) {
    const line = lines[i];
    if (isBlank(line)) {
      i++;
      continue;
    }

    const bi = blockHolder(line);
    if (bi >= 0) {
      out.push(codeBlockHtml(blocks[bi]));
      i++;
      continue;
    }

    const h = line.match(/^\s*(#{1,6})\s+(.+)$/);
    if (h) {
      const lv = Math.min(h[1].length, 4);
      out.push('<h' + lv + '>' + mdInline(h[2]) + '</h' + lv + '>');
      i++;
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      out.push('<hr>');
      i++;
      continue;
    }

    if (isTableStart(i)) {
      const tbl = parseTable(lines, i);
      out.push(tbl.html);
      i = tbl.next;
      continue;
    }

    if (/^\s*&gt;\s?/.test(line)) {
      const q = [];
      while (i < lines.length && /^\s*&gt;\s?/.test(lines[i])) {
        q.push(lines[i].replace(/^\s*&gt;\s?/, ''));
        i++;
      }
      out.push('<blockquote>' + q.map(mdInline).join('<br>') + '</blockquote>');
      continue;
    }

    if (/^\s*(?:[-*+]|\d+[.)])\s+/.test(line)) {
      const lst = parseList(lines, i);
      out.push(lst.html);
      i = lst.next;
      continue;
    }

    // 普通段落：聚合到空行或其他块级起始为止
    const para = [];
    while (i < lines.length && !isBlockStart(lines[i]) && !isTableStart(i)) {
      para.push(lines[i]);
      i++;
    }
    out.push('<p>' + para.map(mdInline).join('<br>') + '</p>');
  }

  // 5. 还原行内代码占位
  return out.join('').replace(/\x00S(\d+)\x00/g, (m, k) => '<code>' + spans[+k] + '</code>');
}

// mdInline 行内语法（输入已转义）。
function mdInline(t) {
  // 图片 ![alt](url)：仅放行 http(s)
  t = t.replace(/!\[([^\]]*)\]\(([^)\s]+)[^)]*\)/g, (m, alt, url) => {
    const src = url.replace(/&quot;|&#39;/g, '');
    if (!/^https?:\/\//i.test(src)) return m;
    return '<img class="md-img" src="' + src + '" alt="' + (alt || '图片') + '">';
  });
  // 链接 [text](url)：仅放行 http(s)/mailto
  t = t.replace(/\[([^\]]*)\]\(([^)\s]+)[^)]*\)/g, (m, txt, url) => {
    let href = url.replace(/&quot;|&#39;/g, '');
    if (!/^(https?:\/\/|mailto:)/i.test(href)) href = '#';
    return (
      '<a href="' + href + '" target="_blank" rel="noopener noreferrer">' + (txt || href) + '</a>'
    );
  });
  t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  t = t.replace(/__([^_]+)__/g, '<strong>$1</strong>');
  t = t.replace(/\*([^*\n]+)\*/g, '<em>$1</em>');
  t = t.replace(/(^|[^A-Za-z0-9_])_([^_\n]+)_(?![A-Za-z0-9_])/g, '$1<em>$2</em>');
  t = t.replace(/~~([^~]+)~~/g, '<del>$1</del>');
  return t;
}

// codeBlockHtml 代码块 HTML（code 为原始文本，此处转义；标题栏带「复制」按钮，点击由调用方事件委托处理）。
function codeBlockHtml(b) {
  return (
    '<div class="md-code"><div class="md-code-h"><span>' +
    (b.lang ? esc(b.lang) : 'code') +
    '</span><span class="md-cp">复制</span></div>' +
    '<pre><code>' +
    esc(b.code) +
    '</code></pre></div>'
  );
}

// splitRow 表格行切分：去掉首尾竖线后按 | 分列。
function splitRow(l) {
  const t = l.trim().replace(/^\|/, '').replace(/\|$/, '');
  return t.split('|').map((c) => c.trim());
}

// alignOf 列对齐。
function alignOf(c) {
  const l = c.charAt(0) === ':';
  const r = c.charAt(c.length - 1) === ':';
  if (l && r) return 'center';
  if (r) return 'right';
  if (l) return 'left';
  return '';
}

// parseTable 表格解析。
function parseTable(lines, start) {
  const head = splitRow(lines[start]);
  const aligns = splitRow(lines[start + 1]).map(alignOf);
  let i = start + 2;
  const rows = [];
  while (i < lines.length && !/^\s*$/.test(lines[i]) && lines[i].indexOf('|') >= 0) {
    rows.push(splitRow(lines[i]));
    i++;
  }
  let html = '<div class="md-table"><table><thead><tr>';
  head.forEach((c, k) => {
    html +=
      '<th' +
      (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') +
      '>' +
      mdInline(c) +
      '</th>';
  });
  html += '</tr></thead><tbody>';
  rows.forEach((row) => {
    html += '<tr>';
    for (let k = 0; k < head.length; k++) {
      html +=
        '<td' +
        (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') +
        '>' +
        mdInline(row[k] || '') +
        '</td>';
    }
    html += '</tr>';
  });
  html += '</tbody></table></div>';
  return { html, next: i };
}

// parseList 列表：收集 {indent, ordered, text} 后按缩进递归构建嵌套。
function parseList(lines, start) {
  const items = [];
  let i = start;
  const re = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  while (i < lines.length) {
    const m = lines[i].match(re);
    if (!m) break;
    items.push({
      indent: m[1].replace(/\t/g, '  ').length,
      ordered: /^\d/.test(m[2]),
      text: m[3],
    });
    i++;
  }
  let html = '';
  let pos = 0;
  while (pos < items.length) {
    const r = buildList(items, pos, items[pos].indent);
    html += r.html;
    if (r.pos <= pos) break;
    pos = r.pos;
  }
  return { html, next: i };
}

function buildList(items, pos, indent) {
  const ordered = items[pos].ordered;
  const tag = ordered ? 'ol' : 'ul';
  let html = '<' + tag + '>';
  while (pos < items.length) {
    const it = items[pos];
    if (it.indent < indent) break;
    if (it.indent > indent) {
      if (html.slice(-5) === '</li>') {
        html = html.slice(0, -5);
        const sub = buildList(items, pos, it.indent);
        html += sub.html + '</li>';
        pos = sub.pos;
      } else {
        pos++;
      }
      continue;
    }
    if (it.ordered !== ordered) break;
    html += '<li>' + mdInline(it.text) + '</li>';
    pos++;
  }
  html += '</' + tag + '>';
  return { html, pos };
}
