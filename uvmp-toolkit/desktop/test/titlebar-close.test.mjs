import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const css = fs.readFileSync(new URL('../src/theme.css', import.meta.url), 'utf8');

test('关闭按钮红色悬停背景扩展到右缘，按钮组位置保持不变', () => {
  const group = css.match(/\.tb-right\s*\{([\s\S]*?)\}/);
  assert.ok(group);
  assert.match(group[1], /margin-right:\s*8px/);
  assert.match(css, /\.tb-btn\s*\{[\s\S]*?width:\s*44px/);

  const close = css.match(/\.tb-close\s*\{([\s\S]*?)\}/);
  assert.ok(close, '关闭按钮需要定位上下文');
  assert.match(close[1], /position:\s*relative/);

  const rule = css.match(/\.tb-close:hover::after,\s*\.tb-close:active::after\s*\{([\s\S]*?)\}/);
  assert.ok(rule, '悬停/按下时都要有右延背景');
  assert.match(rule[1], /content:\s*''/);
  assert.match(rule[1], /position:\s*absolute/);
  assert.match(rule[1], /left:\s*100%/);
  assert.match(rule[1], /width:\s*8px/);
  assert.match(rule[1], /background:\s*var\(--tk-red\)/);
  assert.match(rule[1], /pointer-events:\s*none/);
});
