import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const cssPath = new URL('../src/imagetools/animations.css', import.meta.url);
const css = fs.existsSync(cssPath) ? fs.readFileSync(cssPath, 'utf8') : '';
const viewer = fs.readFileSync(
  new URL('../src/imagetools/components/ViewerPane.vue', import.meta.url),
  'utf8'
);
const main = fs.readFileSync(new URL('../src/imagetools/main.js', import.meta.url), 'utf8');

test('独立窗口有克制的页签、信息栏、弹层与图片过渡', () => {
  assert.match(main, /import '\.\/animations\.css'/);
  for (const selector of [
    '.it-shell',
    '.it-pane',
    '.it-inspector',
    '.it-overlay',
    '.it-dialog',
    '.it-drawer',
    '.it-photo',
  ]) {
    assert.match(
      css,
      new RegExp(selector.replace('.', '\\.') + '\\s*\\{[^}]*animation:[^;}]+', 's'),
      selector
    );
  }
  for (const name of [
    'it-shell-in',
    'it-pane-in',
    'it-aside-in',
    'it-fade-in',
    'it-dialog-in',
    'it-drawer-in',
    'it-photo-in',
  ]) {
    assert.match(css, new RegExp('@keyframes\\s+' + name + '\\b'), name);
  }
  const durations = [...css.matchAll(/animation:\s*[\w-]+\s+([\d.]+)s/g)].map((m) => Number(m[1]));
  assert.ok(durations.length >= 7);
  assert.ok(durations.every((value) => value >= 0.12 && value <= 0.24));
  assert.match(css, /var\(--it-ease\)|cubic-bezier\(\.22,1,\.36,1\)/);
});

test('滚轮和拖动不使用延迟动画，减少动态效果偏好覆盖所有新增动效', () => {
  assert.match(css, /\.it-canvas\.is-dragging\s+\.it-photo\s*\{[^}]*animation:\s*none/);
  assert.match(css, /\.it-canvas\.is-dragging\s+\.it-photo\s*\{[^}]*transition:\s*none/);
  const reduced = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/);
  assert.ok(reduced);
  assert.match(reduced[1], /animation:\s*none !important/);
  assert.match(reduced[1], /transition:\s*none !important/);
});

test('图片路径作为切换键，确保淡入只在图片变化时发生', () => {
  assert.match(viewer, /<img[\s\S]*?:key="previewUrl"/);
});
