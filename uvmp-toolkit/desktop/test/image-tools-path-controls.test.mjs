import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { elidePath } from '../src/imagetools/lib/format.mjs';

const app = fs.readFileSync(
  new URL('../src/imagetools/ImageToolsApp.vue', import.meta.url),
  'utf8'
);
const pane = fs.readFileSync(
  new URL('../src/imagetools/components/ProcessPane.vue', import.meta.url),
  'utf8'
);
const css = fs.readFileSync(new URL('../src/imagetools/imagetools.css', import.meta.url), 'utf8');

test('冷启动重试常量已定义且输出目录单选不在模板中误用 .value', () => {
  assert.match(app, /const COLD_START_ATTEMPTS\s*=\s*\d+/);
  assert.doesNotMatch(pane, /:disabled="[^"]*state\.value/);
  const disabled = [...pane.matchAll(/:disabled="([^"]+)"/g)].map((item) => item[1]);
  assert.ok(disabled.includes("busy || state !== 'idle'"));
});

test('路径省略工具仍保留，供重命名预览使用', () => {
  const full = 'D:\\it-gui-8zQ1QT\\样本\\06-坏图.jpg';
  assert.equal(elidePath(full, 18), '…\\样本\\06-坏图.jpg');
  assert.equal(elidePath(full, 11), '…\\06-坏图.jpg');
  assert.equal(
    elidePath('D:\\资料\\秋检\\宣传素材\\秋检现场-03.jpg', 18),
    '…\\宣传素材\\秋检现场-03.jpg'
  );
  // 信息栏不再省略，但共享函数不能因此被删：重命名预览仍调用它
  const rename = fs.readFileSync(
    new URL('../src/imagetools/components/RenamePanel.vue', import.meta.url),
    'utf8'
  );
  assert.match(rename, /elidePath/);
});

test('信息栏位置直接展示完整 file.path，不再使用省略串', () => {
  const panel = fs.readFileSync(
    new URL('../src/imagetools/components/InfoPanel.vue', import.meta.url),
    'utf8'
  );
  assert.match(panel, /\{\{\s*file\.path\s*\}\}/, '位置行应直接渲染完整 file.path');
  assert.doesNotMatch(panel, /elidedPath/, '信息栏不应再计算省略串');
  assert.doesNotMatch(panel, /按目录分段省略/, '旧「按目录分段省略」文案应替换');
  assert.match(panel, /class="it-path-text"/);
  // 保留完整路径弹层、复制与打开所在文件夹
  assert.match(panel, /class="it-pathpop"/);
  assert.match(panel, /copyPath/);
  assert.match(panel, /open-folder/);
});

test('路径按钮为 LTR 正常顺序、可换行且高度随内容增长', () => {
  const rule = css.match(/\.it-pathbtn\s*\{([\s\S]*?)\}/);
  assert.ok(rule, '缺少 .it-pathbtn 规则');
  const body = rule[1];
  assert.match(body, /direction:\s*ltr/, '路径改回正常 LTR 阅读顺序');
  assert.doesNotMatch(body, /direction:\s*rtl/);
  assert.doesNotMatch(body, /text-overflow:\s*ellipsis/, '不再单行省略');
  assert.match(body, /white-space:\s*normal/, '允许换行');
  assert.doesNotMatch(body, /white-space:\s*nowrap/);
  // 旧实现是固定 26px；新方案改为略增的最小高度 + 内容撑高
  assert.doesNotMatch(body, /(^|[;\s])height:\s*26px/, '不能继续固定 26px 高度');
  const minHeight = /min-height:\s*(\d+)px/.exec(body);
  assert.ok(minHeight, '应给出最小高度');
  assert.ok(Number(minHeight[1]) >= 28, '最小高度应略高于旧的 26px');
  // 连续字符（无分隔符的长目录名）也必须能断行，不得横向溢出
  const textRule = (css.match(/\.it-pathbtn\s+\.it-path-text\s*\{([^}]*)\}/) || [])[1] || '';
  const combined = `${body} ${textRule}`;
  assert.match(combined, /word-break:\s*break-all|overflow-wrap:\s*anywhere/, '长串字符需要可断行');
  assert.match(textRule, /direction:\s*ltr/);
  assert.match(textRule, /unicode-bidi:\s*isolate/);
});

test('完整路径弹层回到文档流，不再用固定 top 覆盖多行路径', () => {
  const rule = css.match(/\.it-pathpop\s*\{([\s\S]*?)\}/);
  assert.ok(rule, '缺少 .it-pathpop 规则');
  assert.doesNotMatch(rule[1], /top:\s*32px/, '固定 top 会压住撑高后的路径');
  assert.match(rule[1], /position:\s*(static|relative)/, '弹层应随内容下移');
});
