// 旧 *.html 地址归一测试（node --test）：未知/多级 html 必须回工作台，不能原地重定向成循环。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  legacyRedirectTarget,
  normalizeLegacyPath,
} from './legacy-urls.js';

test('根级已知 *.html 映射到 SPA 路由并保留 query/hash', () => {
  assert.equal(normalizeLegacyPath('/worklog.html?date=2026-10-01'), '/worklog?date=2026-10-01');
  assert.equal(normalizeLegacyPath('/share.html#/s/abc'), '/share#/s/abc');
  assert.equal(normalizeLegacyPath('/worklog.html#dict'), '/teamdata');
});

test('未知根级 html 回工作台', () => {
  assert.equal(normalizeLegacyPath('/unknown.html'), '/');
  assert.equal(normalizeLegacyPath('/unknown.html?x=1'), '/?x=1');
});

test('未知多级 html 归一为工作台，绝不再返回 .html 自身', () => {
  for (const p of ['/a/b.html', '/deep/path/unknown.html', '/a/worklog.html']) {
    const out = normalizeLegacyPath(p);
    assert.notEqual(out, p);
    assert.equal(/\.html(?:[?#]|$)/i.test(out), false, out);
    assert.equal(out, '/');
  }
  assert.equal(normalizeLegacyPath('/a/b.html?x=1'), '/?x=1');
  assert.equal(normalizeLegacyPath('/a/b.html?x=1#frag'), '/?x=1#frag');
  assert.equal(normalizeLegacyPath('/a/b.html#frag'), '/#frag');
});

test('启动跳转对未知多级 html 也只回工作台一次', () => {
  assert.equal(legacyRedirectTarget({ pathname: '/a/b.html', search: '', hash: '' }), '/#/');
  assert.equal(
    legacyRedirectTarget({ pathname: '/a/b.html', search: '?x=1', hash: '#frag' }),
    '/#/?x=1#frag'
  );
});

test('启动跳转与路径归一使用同一根级文件名规则：/a/worklog.html 不去 /worklog', () => {
  assert.equal(legacyRedirectTarget({ pathname: '/a/worklog.html', search: '', hash: '' }), '/#/');
  assert.equal(
    legacyRedirectTarget({ pathname: '/a/worklog.html', search: '?date=2026-10-01', hash: '' }),
    '/#/?date=2026-10-01'
  );
  assert.equal(
    legacyRedirectTarget({ pathname: '/a/worklog.html', search: '', hash: '#dict' }),
    '/#/#dict'
  );
  assert.equal(normalizeLegacyPath('/a/worklog.html'), '/');
});
