// 机巡页照片失败可见性：缺陷照片下载/拒绝保存/补存/标注日志不能被技术日志过滤吞掉，
// 结果摘要必须在「一张都没存下来」时也给出失败提示。
// 这里从 SFC 源码抽出纯函数实际执行，避免只断言字符串存在却跑错逻辑。
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parse } from '@vue/compiler-sfc';

const PAGE = 'pages/DefectReviewPage.vue';

function readPage() {
  return readFileSync(fileURLToPath(new URL(`../src/${PAGE}`, import.meta.url)), 'utf8');
}

function pageScript() {
  // Git 在 Windows 默认可能把源码转换成 CRLF；函数抽取统一按 LF 处理，
  // 否则 definition() 找不到闭合位置，整组用例会误报失败。
  const { descriptor } = parse(readPage().replace(/\r\n?/g, '\n'), { filename: PAGE });
  return descriptor.scriptSetup?.content || '';
}

function definition(script, header) {
  const start = script.indexOf(header);
  assert.ok(start >= 0, `页面缺少 ${header}`);
  const end = script.indexOf(header.startsWith('function ') ? '\n}\n' : ';\n', start);
  assert.ok(end >= 0, `${header} 未闭合`);
  return script.slice(start, header.startsWith('function ') ? end + 3 : end + 2);
}

function loadHelpers() {
  const script = pageScript();
  const code = [
    definition(script, 'const USER_LOG_RE'),
    definition(script, 'const TECHNICAL_LOG_RE'),
    definition(script, 'const PHOTO_LOG_RE'),
    definition(script, 'function shouldShowUserLog'),
    definition(script, 'function hasPhotoCount'),
    definition(script, 'function fmtPhotoResult'),
    definition(script, 'function fmtResult'),
  ].join('\n');
  return new Function(`${code}\nreturn { shouldShowUserLog, fmtResult };`)();
}

test('照片失败/拒绝保存/补存/标注日志带 HTTP 也进大屏摘要', () => {
  const { shouldShowUserLog } = loadHelpers();
  const visible = [
    '缺陷照片原图下载失败：1号塔.jpg（HTTP 401）',
    '缺陷照片只加载到缩略图，已拒绝保存：1号塔.jpg（原图接口：HTTP 500）',
    '缺陷照片保存失败：1号塔.jpg（写入失败：EACCES）',
    '缺陷照片标注失败：1号塔.jpg',
    '缺陷照片标注未画出任何缺陷框：1号塔.jpg（缺陷框 2 个，丢弃 2 个）',
    '缺陷照片标注图已保存：D:/out/线路/1号塔/1号塔_标注.jpg（画框 2 个）',
    '缺陷照片原图已保存：D:/out/线路/1号塔/1号塔.jpg',
    '缺陷照片补存成功：1号塔.jpg',
    '原图接口失败，改用页面已加载原图：1号塔.jpg（HTTP 404）',
  ];
  for (const line of visible) {
    assert.equal(shouldShowUserLog(line), true, `被过滤：${line}`);
  }
});

test('原有技术日志过滤不被破坏', () => {
  const { shouldShowUserLog } = loadHelpers();
  const hidden = [
    '下拉失败：Timeout 30000ms exceeded',
    '接口响应 HTTP 500',
    '审核图片 3/10：等待高亮',
    '页面视口 1280x800',
    'pid 1234 已退出',
    '诊断：locator 未找到元素',
  ];
  for (const line of hidden) {
    assert.equal(shouldShowUserLog(line), false, `技术日志未过滤：${line}`);
  }
  // 不含用户关键词的行照旧不进摘要
  assert.equal(shouldShowUserLog('心跳正常'), false);
});

test('大屏摘要走统一判定，不退回旧的组合过滤条件', () => {
  const source = readPage();
  assert.match(source, /if \(!shouldShowUserLog\(line\)\) return;/);
  assert.doesNotMatch(
    source,
    /if \(!USER_LOG_RE\.test\(line\) \|\| TECHNICAL_LOG_RE\.test\(line\)\) return;/
  );
});

test('结果摘要：归档与标注全为 0 但存在失败时仍给出照片提示', () => {
  const { fmtResult } = loadHelpers();
  assert.match(
    fmtResult({ photos_saved: 0, photos_marked: 0, photo_failures: 1 }),
    /缺陷照片失败 1 张/
  );
  assert.match(
    fmtResult({ photos_saved: 0, photos_marked: 0, photo_download_failures: 3 }),
    /缺陷照片原图下载失败 3 张/
  );
  assert.match(
    fmtResult({ photos_saved: 0, photos_marked: 0, photo_mark_failures: 2 }),
    /缺陷照片标注失败 2 张/
  );
});

test('结果摘要：分别显示原图下载失败与标注失败，不再合并成一句', () => {
  const { fmtResult } = loadHelpers();
  const summary = fmtResult({
    photos_saved: 4,
    photos_marked: 3,
    photo_download_failures: 2,
    photo_mark_failures: 1,
  });
  assert.match(summary, /缺陷照片 4 张（标注 3 张）/);
  assert.match(summary, /缺陷照片原图下载失败 2 张/);
  assert.match(summary, /缺陷照片标注失败 1 张/);
  assert.doesNotMatch(summary, /缺陷照片失败/);
});

test('结果摘要：兼容旧 photo_failures，新字段为 0 时不重复计数', () => {
  const { fmtResult } = loadHelpers();
  const legacy = fmtResult({ photos_saved: 2, photos_marked: 2, photo_failures: 1 });
  assert.match(legacy, /缺陷照片 2 张（标注 2 张）/);
  assert.match(legacy, /缺陷照片失败 1 张/);
  const split = fmtResult({
    photos_saved: 2,
    photos_marked: 2,
    photo_failures: 1,
    photo_download_failures: 1,
  });
  assert.match(split, /缺陷照片原图下载失败 1 张/);
  assert.doesNotMatch(split, /缺陷照片失败 1 张/);
});

test('结果摘要：没有照片计数时不出现照片段，已停止仍是已停止', () => {
  const { fmtResult } = loadHelpers();
  assert.equal(
    fmtResult({ completed: 3, images: 30, deferred: 0 }),
    '完成任务 3；审核图片 30；转人工 0'
  );
  assert.equal(fmtResult({ cancelled: true, photos_saved: 1 }), '已停止');
});

test('无视缺陷框勾选文案写明不归档照片、不写缺陷汇总', () => {
  const source = readPage();
  assert.match(source, /无视缺陷框[^<]*不归档照片[^<]*不写缺陷汇总/);
});
