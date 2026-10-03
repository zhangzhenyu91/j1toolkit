/* ============================================================
   精简 Markdown 渲染器（无外部依赖；网页端多页共用：callme.html / index.html / admin.html）
   覆盖：标题 / 加粗 / 斜体 / 行内代码 / 围栏代码块 / 有序无序列表（缩进嵌套）/
         引用 / 链接 / 图片 / 表格 / 分割线 / 删除线 / 换行
   安全：解析前先 Shade.esc 全量转义，渲染结果不含原始 HTML
   依赖：须先加载 /assets/common.js（Shade.esc）
   ============================================================ */
function renderMd(text) {
  if (!text) return '';
  try {
    return mdRender(String(text));
  } catch (e) {
    return ''; // 渲染失败回退纯文本展示（由调用方兜底）
  }
}

function mdRender(src) {
  // 1. 提取围栏代码块为占位符，内部内容不参与后续解析
  var blocks = [];
  src = src.replace(/```([^\n`]*)\r?\n?([\s\S]*?)(?:```|$)/g, function (m, lang, code) {
    blocks.push({ lang: (lang || '').trim(), code: code.replace(/\n$/, '') });
    return '\n\x00B' + (blocks.length - 1) + '\x00\n';
  });

  // 2. 全量 HTML 转义（防注入的唯一闸门）
  src = Shade.esc(src);

  // 3. 行内代码占位（先于加粗/斜体，避免反引号内符号被误解析）
  var spans = [];
  src = src.replace(/`([^`\n]+)`/g, function (m, code) {
    spans.push(code);
    return '\x00S' + (spans.length - 1) + '\x00';
  });

  // 4. 块级解析
  var lines = src.split('\n');
  var out = [];
  var i = 0;
  function isBlank(l) { return /^\s*$/.test(l); }
  function blockHolder(l) {
    var m = l.match(/^\s*\x00B(\d+)\x00\s*$/);
    return m ? +m[1] : -1;
  }
  function isTableStart(idx) {
    return lines[idx].indexOf('|') >= 0 && idx + 1 < lines.length && isTableSep(lines[idx + 1]);
  }
  function isBlockStart(l) {
    // 注意：此时文本已经 Shade.esc 转义，引用标记 > 以 &gt; 形式出现
    return isBlank(l) || blockHolder(l) >= 0 ||
      /^\s*#{1,6}\s+/.test(l) || /^\s*&gt;\s?/.test(l) ||
      /^\s*(?:[-*+]|\d+[.)])\s+/.test(l) ||
      /^\s*([-*_])(\s*\1){2,}\s*$/.test(l);
  }

  while (i < lines.length) {
    var line = lines[i];
    if (isBlank(line)) { i++; continue; }

    var bi = blockHolder(line);
    if (bi >= 0) { out.push(codeBlockHtml(blocks[bi])); i++; continue; }

    var h = line.match(/^\s*(#{1,6})\s+(.+)$/);
    if (h) {
      var lv = Math.min(h[1].length, 4);
      out.push('<h' + lv + '>' + mdInline(h[2]) + '</h' + lv + '>');
      i++;
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) { out.push('<hr>'); i++; continue; }

    if (isTableStart(i)) {
      var tbl = parseTable(lines, i);
      out.push(tbl.html);
      i = tbl.next;
      continue;
    }

    if (/^\s*&gt;\s?/.test(line)) {
      var q = [];
      while (i < lines.length && /^\s*&gt;\s?/.test(lines[i])) {
        q.push(lines[i].replace(/^\s*&gt;\s?/, ''));
        i++;
      }
      out.push('<blockquote>' + q.map(mdInline).join('<br>') + '</blockquote>');
      continue;
    }

    if (/^\s*(?:[-*+]|\d+[.)])\s+/.test(line)) {
      var lst = parseList(lines, i);
      out.push(lst.html);
      i = lst.next;
      continue;
    }

    // 普通段落：聚合到空行或其他块级起始为止
    var para = [];
    while (i < lines.length && !isBlockStart(lines[i]) && !isTableStart(i)) {
      para.push(lines[i]);
      i++;
    }
    out.push('<p>' + para.map(mdInline).join('<br>') + '</p>');
  }

  // 5. 还原行内代码占位
  return out.join('').replace(/\x00S(\d+)\x00/g, function (m, k) {
    return '<code>' + spans[+k] + '</code>';
  });
}

// 行内语法（输入已转义）
function mdInline(t) {
  // 图片 ![alt](url)：仅放行 http(s)；须先于链接处理，避免 [alt](url) 部分被链接正则吞掉
  t = t.replace(/!\[([^\]]*)\]\(([^)\s]+)[^)]*\)/g, function (m, alt, url) {
    var src = url.replace(/&quot;|&#39;/g, '');
    if (!/^https?:\/\//i.test(src)) return m; // 非 http(s) 图片降级为原文本
    return '<img class="md-img" src="' + src + '" alt="' + (alt || '图片') + '">';
  });
  // 链接 [text](url)：仅放行 http(s)/mailto，其余降级为 #
  t = t.replace(/\[([^\]]*)\]\(([^)\s]+)[^)]*\)/g, function (m, txt, url) {
    var href = url.replace(/&quot;|&#39;/g, '');
    if (!/^(https?:\/\/|mailto:)/i.test(href)) href = '#';
    return '<a href="' + href + '" target="_blank" rel="noopener noreferrer">' + (txt || href) + '</a>';
  });
  t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  t = t.replace(/__([^_]+)__/g, '<strong>$1</strong>');
  t = t.replace(/\*([^*\n]+)\*/g, '<em>$1</em>');
  t = t.replace(/(^|[^A-Za-z0-9_])_([^_\n]+)_(?![A-Za-z0-9_])/g, '$1<em>$2</em>');
  t = t.replace(/~~([^~]+)~~/g, '<del>$1</del>');
  return t;
}

// 代码块 HTML（code 为原始文本，此处转义）
function codeBlockHtml(b) {
  return '<div class="md-code"><div class="md-code-h"><span>' +
    (b.lang ? Shade.esc(b.lang) : 'code') +
    '</span><span class="md-cp">复制</span></div>' +
    '<pre><code>' + Shade.esc(b.code) + '</code></pre></div>';
}

// 表格行切分：去掉首尾竖线后按 | 分列
function splitRow(l) {
  var t = l.trim().replace(/^\|/, '').replace(/\|$/, '');
  return t.split('|').map(function (c) { return c.trim(); });
}
function isTableSep(l) {
  if (l.indexOf('-') < 0) return false;
  var cells = splitRow(l);
  if (!cells.length) return false;
  return cells.every(function (c) { return /^:?-+:?$/.test(c); });
}
function alignOf(c) {
  var l = c.charAt(0) === ':', r = c.charAt(c.length - 1) === ':';
  if (l && r) return 'center';
  if (r) return 'right';
  if (l) return 'left';
  return '';
}
function parseTable(lines, start) {
  var head = splitRow(lines[start]);
  var aligns = splitRow(lines[start + 1]).map(alignOf);
  var i = start + 2;
  var rows = [];
  while (i < lines.length && !/^\s*$/.test(lines[i]) && lines[i].indexOf('|') >= 0) {
    rows.push(splitRow(lines[i]));
    i++;
  }
  var html = '<div class="md-table"><table><thead><tr>';
  head.forEach(function (c, k) {
    html += '<th' + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') + '>' + mdInline(c) + '</th>';
  });
  html += '</tr></thead><tbody>';
  rows.forEach(function (row) {
    html += '<tr>';
    for (var k = 0; k < head.length; k++) {
      html += '<td' + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') + '>' + mdInline(row[k] || '') + '</td>';
    }
    html += '</tr>';
  });
  html += '</tbody></table></div>';
  return { html: html, next: i };
}

// 列表：先收集 {indent, ordered, text}，再按缩进递归构建嵌套
function parseList(lines, start) {
  var items = [];
  var i = start;
  var re = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  while (i < lines.length) {
    var m = lines[i].match(re);
    if (!m) break;
    items.push({
      indent: m[1].replace(/\t/g, '  ').length,
      ordered: /^\d/.test(m[2]),
      text: m[3],
    });
    i++;
  }
  var html = '';
  var pos = 0;
  while (pos < items.length) {
    var r = buildList(items, pos, items[pos].indent);
    html += r.html;
    if (r.pos <= pos) break; // 防死循环
    pos = r.pos;
  }
  return { html: html, next: i };
}
function buildList(items, pos, indent) {
  var ordered = items[pos].ordered;
  var tag = ordered ? 'ol' : 'ul';
  var html = '<' + tag + '>';
  while (pos < items.length) {
    var it = items[pos];
    if (it.indent < indent) break;
    if (it.indent > indent) {
      // 子列表：并入上一项（无上一项则容错跳过）
      if (html.slice(-5) === '</li>') {
        html = html.slice(0, -5);
        var sub = buildList(items, pos, it.indent);
        html += sub.html + '</li>';
        pos = sub.pos;
      } else {
        pos++;
      }
      continue;
    }
    if (it.ordered !== ordered) break; // 同级类型变化：交由外层另起列表
    html += '<li>' + mdInline(it.text) + '</li>';
    pos++;
  }
  html += '</' + tag + '>';
  return { html: html, pos: pos };
}
