// Workbench 可访问性结构测试（node --test）：通知卡与通知条不得把 role=button 套在含按钮的父级上，
// 展开/查看全部必须使用原生 button，避免 Enter/Space 吞掉内层按钮默认 click。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';

const src = fs.readFileSync(path.join(import.meta.dirname, '../views/WorkbenchView.vue'), 'utf8');

function openingTag(fragment) {
  const at = src.indexOf(fragment);
  assert.ok(at >= 0, `未找到片段：${fragment}`);
  const start = src.lastIndexOf('<', at);
  const end = src.indexOf('>', at);
  return src.slice(start, end + 1);
}

test('通知卡不再承担 role=button / tabindex，避免内层按钮嵌套交互', () => {
  const tag = openingTag('class="card card-hover notice-card"');
  assert.doesNotMatch(tag, /role="button"/);
  assert.doesNotMatch(tag, /tabindex="0"/);
  assert.doesNotMatch(tag, /@keydown/);
});

test('通知条父级不是 button 角色，展开由原生标题按钮承担', () => {
  const tag = openingTag('class="nm-item"');
  assert.doesNotMatch(tag, /role="button"/);
  assert.doesNotMatch(tag, /tabindex="0"/);
  assert.doesNotMatch(tag, /@keydown/);
  assert.match(src, /<button[^>]*class="[^"]*nm-title-btn"/);
});

test('通知标题按钮保持 .nm-title 的 13.5px，不继承父级 16px', () => {
  const at = src.indexOf('.nm-title-btn {');
  assert.ok(at >= 0, '未找到 .nm-title-btn 样式');
  const block = src.slice(at, src.indexOf('}', at));
  assert.match(block, /font-size:\s*13\.5px/);
  assert.doesNotMatch(block, /font-size:\s*inherit/);
});

test('「查看全部 / 展开其余」使用原生 button，而非 role=button 的 span', () => {
  assert.match(src, /<button[^>]*class="more"/);
  assert.doesNotMatch(src, /<span[^>]*class="more"[^>]*role="button"/);
});
