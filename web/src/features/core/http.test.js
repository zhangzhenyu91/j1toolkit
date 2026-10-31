// 请求头纯逻辑测试（node --test）：FormData 不得被覆盖为 application/json，
// 保证浏览器/运行时自行生成 multipart boundary；普通 JSON 请求保持既有头。
import assert from 'node:assert/strict';
import test from 'node:test';

import { applyBodyHeaders, isRawBody } from './http.js';

test('FormData 请求体不携带 JSON Content-Type', () => {
  const form = new FormData();
  form.append('image', new Blob(['x'], { type: 'image/jpeg' }), 'a.jpg');
  const headers = applyBodyHeaders({ 'Content-Type': 'application/json' }, form);
  assert.equal(headers['Content-Type'], undefined);
  assert.equal(headers['content-type'], undefined);
  assert.equal(isRawBody(form), true);
});

test('FormData 请求仍保留鉴权头', () => {
  const headers = applyBodyHeaders(
    { Authorization: 'Bearer t', 'Content-Type': 'application/json' },
    new FormData()
  );
  assert.equal(headers.Authorization, 'Bearer t');
  assert.equal(headers['Content-Type'], undefined);
});

test('JSON 字符串请求补 application/json', () => {
  const headers = applyBodyHeaders({}, JSON.stringify({ a: 1 }));
  assert.equal(headers['Content-Type'], 'application/json');
});

test('无请求体保持既有 JSON 默认头（非 GET/HEAD）', () => {
  const headers = applyBodyHeaders({}, undefined);
  assert.equal(headers['Content-Type'], 'application/json');
});

test('GET / HEAD 无请求体不补 JSON Content-Type，避免无谓预检', () => {
  assert.equal(applyBodyHeaders({}, undefined, 'GET')['Content-Type'], undefined);
  assert.equal(applyBodyHeaders({}, undefined, 'HEAD')['Content-Type'], undefined);
  assert.equal(applyBodyHeaders({}, null, 'get')['Content-Type'], undefined);
});

test('GET 带 JSON 请求体仍补 JSON（非无 body 场景）', () => {
  assert.equal(applyBodyHeaders({}, '{"a":1}', 'GET')['Content-Type'], 'application/json');
});

test('调用方显式 Content-Type 不被覆盖', () => {
  const headers = applyBodyHeaders({ 'Content-Type': 'text/plain' }, 'x');
  assert.equal(headers['Content-Type'], 'text/plain');
});

test('Blob / URLSearchParams 视为运行时自定请求体', () => {
  assert.equal(isRawBody(new Blob(['x'])), true);
  assert.equal(isRawBody(new URLSearchParams({ a: '1' })), true);
  const headers = applyBodyHeaders({ 'Content-Type': 'application/json' }, new Blob(['x']));
  assert.equal(headers['Content-Type'], undefined);
});
