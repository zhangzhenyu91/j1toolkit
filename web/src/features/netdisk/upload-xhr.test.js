import assert from 'node:assert/strict';
import test from 'node:test';

import { putChunk } from './upload-xhr.js';

class FakeXHR {
  static instances = [];

  constructor() {
    this.upload = {};
    this.headers = {};
    this.status = 0;
    this.responseText = '';
    FakeXHR.instances.push(this);
  }

  open(method, url) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name, value) {
    this.headers[name] = value;
  }

  send(body) {
    this.body = body;
  }

  respond(status, body) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload();
  }
}

test('XHR 分片上传：发送字节进度、请求头与响应解析', async () => {
  FakeXHR.instances = [];
  const progress = [];
  const pending = putChunk({
    url: '/api/v1/netdisk/upload/chunk',
    headers: { Authorization: 'Bearer token', 'X-Chunk-Index': '0' },
    body: { size: 8 },
    timeout: 1234,
    onProgress: (loaded) => progress.push(loaded),
    createXHR: () => new FakeXHR(),
  });
  const xhr = FakeXHR.instances[0];
  assert.equal(xhr.method, 'PUT');
  assert.equal(xhr.url, '/api/v1/netdisk/upload/chunk');
  assert.equal(xhr.timeout, 1234);
  assert.equal(xhr.headers.Authorization, 'Bearer token');
  assert.equal(xhr.headers['X-Chunk-Index'], '0');

  xhr.upload.onprogress({ loaded: 3 });
  xhr.upload.onprogress({ loaded: 3 });
  xhr.upload.onprogress({ loaded: 8 });
  xhr.upload.onload();
  assert.deepEqual(progress, [3, 8]);

  xhr.respond(200, { code: 0, data: { state: 'receiving' } });
  const result = await pending;
  assert.equal(result.status, 200);
  assert.equal(result.body.code, 0);
  assert.equal(result.body.data.state, 'receiving');
});

test('XHR 网络错误：保留分片重试标记', async () => {
  FakeXHR.instances = [];
  const pending = putChunk({ body: { size: 8 }, createXHR: () => new FakeXHR() });
  FakeXHR.instances[0].onerror();
  await assert.rejects(pending, (err) => err.network === true && err.retry === true);
});

test('XHR 单片超时：保留分片重试标记', async () => {
  FakeXHR.instances = [];
  const pending = putChunk({ body: { size: 8 }, createXHR: () => new FakeXHR() });
  FakeXHR.instances[0].ontimeout();
  await assert.rejects(pending, (err) => err.network === true && err.retry === true);
});
