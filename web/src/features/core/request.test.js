// 共享请求层测试（node --test）：统一注入 token、解析 {code,message,data} / {ok,error} 信封、
// 401 清理广播；multipart 不覆盖 boundary；原始下载流不读 JSON；网络失败给出可读错误。
import assert from 'node:assert/strict';
import test from 'node:test';

import { createClient } from './request.js';

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function recordingClient({ body, status = 200, token = 'jwt-token' } = {}) {
  const calls = [];
  let unauthorized = 0;
  const client = createClient({
    base: '/api/v1/demo',
    token: () => token,
    onUnauthorized: () => {
      unauthorized += 1;
    },
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse(body, status);
    },
  });
  return { client, calls, unauthorized: () => unauthorized };
}

test('请求注入 Bearer token 与 JSON 头，返回信封 data', async () => {
  const { client, calls } = recordingClient({ body: { code: 0, message: '', data: { ok: 1 } } });
  const data = await client.request('/list', { method: 'POST', body: JSON.stringify({ a: 1 }) });
  assert.deepEqual(data, { ok: 1 });
  assert.equal(calls[0].url, '/api/v1/demo/list');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer jwt-token');
  assert.equal(calls[0].init.headers['Content-Type'], 'application/json');
});

test('GET / HEAD 无请求体不补 JSON Content-Type', async () => {
  const { client, calls } = recordingClient({ body: { code: 0, message: '', data: null } });
  await client.request('/list');
  assert.equal(calls[0].init.headers['Content-Type'], undefined);
  await client.request('/list', { method: 'HEAD' });
  assert.equal(calls[1].init.headers['Content-Type'], undefined);
  // 仅显式带请求体的 GET 才需要 JSON 头；非 GET/HEAD 无体仍按 JSON 处理。
  await client.request('/list', { method: 'GET', body: '{"a":1}' });
  assert.equal(calls[2].init.headers['Content-Type'], 'application/json');
  await client.request('/save', { method: 'POST' });
  assert.equal(calls[3].init.headers['Content-Type'], 'application/json');
});

test('multipart 请求交给运行时生成 boundary，不写 JSON Content-Type', async () => {
  const { client, calls } = recordingClient({ body: { code: 0, data: { id: 9 } } });
  const form = new FormData();
  form.append('file', new Blob(['x'], { type: 'image/jpeg' }), 'a.jpg');
  const data = await client.request('/upload', { method: 'POST', body: form });
  assert.deepEqual(data, { id: 9 });
  assert.equal(calls[0].init.headers['Content-Type'], undefined);
  assert.equal(calls[0].init.headers.Authorization, 'Bearer jwt-token');
});

test('data 为 null 时按 emptyData 回落（管理端解构不崩）', async () => {
  const calls = [];
  const client = createClient({
    emptyData: {},
    token: () => '',
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse({ code: 0, message: 'ok', data: null });
    },
  });
  assert.deepEqual(await client.request('/delete'), {});
});

test('业务码非 0：抛带 code/status/data 的错误', async () => {
  const { client } = recordingClient({
    body: { code: 40001, message: '参数错误', data: { field: 'line_name' } },
    status: 400,
  });
  await assert.rejects(client.request('/x'), (err) => {
    assert.equal(err.message, '参数错误');
    assert.equal(err.code, 40001);
    assert.equal(err.status, 400);
    assert.deepEqual(err.data, { field: 'line_name' });
    return true;
  });
});

test('HTTP 401 清会话并广播，且不把 data 当成功返回', async () => {
  let clears = 0;
  let broadcasts = 0;
  const client = createClient({
    token: () => '',
    onUnauthorized: () => {
      clears += 1;
    },
    broadcastUnauthorized: () => {
      broadcasts += 1;
    },
    fetchImpl: async () => jsonResponse({ code: 40101, message: '登录已过期' }, 401),
  });
  await assert.rejects(client.request('/x'), (err) => {
    assert.equal(err.code, 40101);
    assert.equal(err.sessionHandled, true);
    return true;
  });
  assert.equal(clears, 1);
  assert.equal(broadcasts, 1);
});

test('silentUnauthorized 仍清会话、只跳过广播（登录页密码错误场景）', async () => {
  let clears = 0;
  let broadcasts = 0;
  const client = createClient({
    token: () => '',
    onUnauthorized: () => {
      clears += 1;
    },
    broadcastUnauthorized: () => {
      broadcasts += 1;
    },
    fetchImpl: async () => jsonResponse({ code: 40101, message: '账号或密码错误' }, 401),
  });
  await assert.rejects(client.request('/x', { silentUnauthorized: true }));
  assert.equal(clears, 1, '静默仅表示不广播，会话标记仍必须清理');
  assert.equal(broadcasts, 0);
});

test('requestRaw 401 同样清会话并按需广播', async () => {
  let clears = 0;
  let broadcasts = 0;
  const client = createClient({
    token: () => 't',
    onUnauthorized: () => {
      clears += 1;
    },
    broadcastUnauthorized: () => {
      broadcasts += 1;
    },
    fetchImpl: async () => new Response('', { status: 401 }),
  });
  await assert.rejects(client.requestRaw('/download'));
  assert.equal(clears, 1);
  assert.equal(broadcasts, 1);
});

test('sessionManaged=false 的公开客户端不标记 sessionHandled', async () => {
  const client = createClient({
    token: () => '',
    sessionManaged: false,
    onUnauthorized: () => {},
    broadcastUnauthorized: () => {},
    fetchImpl: async () => jsonResponse({ code: 40101, message: '票据失效' }, 401),
  });
  await assert.rejects(client.request('/public'), (err) => {
    assert.equal(err.sessionHandled, false);
    return true;
  });
});

test('{ok:false,error} 信封（安全日）按 error 文案抛错，成功返回整个 body', async () => {
  const calls = [];
  const client = createClient({
    envelope: 'ok',
    token: () => '',
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse({ ok: true, records: [] });
    },
  });
  assert.deepEqual(await client.request('/records'), { ok: true, records: [] });
  const failing = createClient({
    envelope: 'ok',
    token: () => '',
    fetchImpl: async () => jsonResponse({ ok: false, error: '生成失败' }, 500),
  });
  await assert.rejects(failing.request('/x'), /生成失败/);
});

test('网络异常归一为 network_error，不泄露底层异常', async () => {
  const client = createClient({
    token: () => '',
    fetchImpl: async () => {
      throw new Error('ECONNREFUSED 127.0.0.1');
    },
  });
  await assert.rejects(client.request('/x'), (err) => {
    assert.equal(err.code, 'network_error');
    assert.match(err.message, /无法连接服务/);
    assert.doesNotMatch(err.message, /ECONNREFUSED/);
    return true;
  });
});

test('requestRaw 原样返回 Response，401 清会话后抛错', async () => {
  const calls = [];
  let unauthorized = 0;
  const client = createClient({
    token: () => 't',
    onUnauthorized: () => {
      unauthorized += 1;
    },
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return new Response('binary', { status: 200 });
    },
  });
  const res = await client.requestRaw('/download');
  assert.equal(await res.text(), 'binary');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer t');

  const unauthorizedClient = createClient({
    token: () => 't',
    onUnauthorized: () => {
      unauthorized += 1;
    },
    fetchImpl: async () => new Response('{"code":40101}', { status: 401 }),
  });
  await assert.rejects(unauthorizedClient.requestRaw('/download'), (err) => {
    assert.equal(err.sessionHandled, true);
    return true;
  });
  assert.equal(unauthorized, 1);
});

test('withMessage 附加服务端汇总文案（KVM 操作提示）', async () => {
  const { client } = recordingClient({
    body: { code: 0, message: '已推送 2 个文件', data: { pushed: 2 } },
  });
  const data = await client.request('/push', { method: 'POST', body: '{}', withMessage: true });
  assert.deepEqual(data, { pushed: 2, message: '已推送 2 个文件' });
});

test('buildURL 忽略空值并用 & 续接已有查询串', async () => {
  const client = createClient({ fetchImpl: async () => jsonResponse({ code: 0, data: {} }) });
  assert.equal(client.buildURL('/x', { a: 1, b: '', c: null }), '/x?a=1');
  assert.equal(client.buildURL('/x?z=9', { a: 1 }), '/x?z=9&a=1');
});
