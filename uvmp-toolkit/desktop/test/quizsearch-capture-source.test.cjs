// 截屏源选择：多显示器下没有可靠匹配时不得盲取 sources[0]。
const test = require('node:test');
const assert = require('node:assert/strict');

const { pickSource } = require('../electron/quizsearch/capture-source.cjs');

const D1 = { id: 1, bounds: { x: 0, y: 0, width: 1920, height: 1080 } };
const D2 = { id: 2, bounds: { x: 1920, y: 0, width: 1920, height: 1080 } };

function src(displayId) {
  return { id: 'screen:' + displayId, display_id: displayId, thumbnail: { isEmpty: () => false } };
}

test('display_id 精确匹配时取对应屏幕', () => {
  const r = pickSource([src(2), src(1)], D2, 2);
  assert.equal(r.source.display_id, 2);
  assert.equal(r.reason, '');
});

test('数字/字符串 display_id 混用也可匹配', () => {
  assert.equal(pickSource([src('2')], D2, 2).source.display_id, '2');
  assert.equal(pickSource([src(2)], { id: '2' }, 2).source.display_id, 2);
});

test('多显示器且无匹配：不盲取 sources[0]，返回空并给出原因', () => {
  const r = pickSource([src(2)], D1, 2);
  assert.equal(r.source, null);
  assert.match(r.reason, /显示器/);
});

test('display_id 为空（平台不提供）且多显示器：同样不盲取', () => {
  const r = pickSource([src(''), src('')], D1, 2);
  assert.equal(r.source, null);
  assert.match(r.reason, /显示器/);
});

test('仅一个显示器一个源：允许唯一回退', () => {
  const r = pickSource([src('')], D1, 1);
  assert.equal(r.source.id, 'screen:');
});

test('多显示器下只有一个源且 display_id 为空：无法判定归属，仍拒绝', () => {
  const r = pickSource([src('')], D2, 2);
  assert.equal(r.source, null);
});

test('无源时返回空且不抛错', () => {
  assert.deepEqual(pickSource([], D1, 1), { source: null, reason: '未取到任何屏幕源' });
  assert.equal(pickSource(null, D1, 1).source, null);
});

test('多个源指向同一显示器：取第一个匹配项', () => {
  const a = src(1);
  const b = src(1);
  b.id = 'screen:1-dup';
  assert.equal(pickSource([a, b], D1, 2).source, a);
});
