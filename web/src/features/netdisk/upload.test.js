// 网盘分片上传状态机测试（node --test）：重试、failed_retriable 从 0 重传、
// complete 超时后 status 长轮询、取消。
import assert from 'node:assert/strict';
import test from 'node:test';

import { uploadWithAdapter } from './upload.js';

const MB = 1024 * 1024;
const noSleep = async () => {};
const file = (size) => ({
  name: 'a.bin',
  size,
  slice: (start, end) => ({ start, end, size: end - start }),
});
const init = (extra = {}) => ({
  upload_id: 'abcd1234abcd1234',
  chunk_size: 4 * MB,
  total_chunks: 2,
  received: [],
  received_bytes: 0,
  ...extra,
});

test('正常上传：两片并发完成、进度与 storage_progress 收敛', async () => {
  const calls = [];
  const progress = [];
  const adapter = {
    uploadInit: async () => init(),
    uploadChunk: async ({ index }) => {
      calls.push(index);
      return {
        received_bytes: (index + 1) * 4 * MB,
        storage_progress: index === 0 ? 10 : 20,
        frontier: index + 1,
      };
    },
    uploadComplete: async () => ({ state: 'completed', storage_progress: 100 }),
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {
      throw new Error('不应 abort');
    },
  };
  const r = await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    onProgress: (p) => progress.push(p),
    sleep: noSleep,
  });
  assert.equal(r.uploadId, 'abcd1234abcd1234');
  assert.deepEqual(calls.sort(), [0, 1]);
  const last = progress.at(-1);
  assert.equal(last.received, 8 * MB);
  assert.equal(last.doneChunks, 2);
  assert.equal(last.storageProgress, 100);
  assert.equal(last.frontier, 2);
  assert.equal(last.merging, false);
});

test('failed_retriable：自动从第 0 片重传，upload_id 不变', async () => {
  const calls = [];
  let failedOnce = false;
  const adapter = {
    uploadInit: async () => init(),
    uploadChunk: async ({ index }) => {
      calls.push(index);
      if (!failedOnce && index === 1) {
        failedOnce = true;
        const err = new Error('resend from chunk 0');
        err.restart = true;
        throw err;
      }
      return { received_bytes: (index + 1) * 4 * MB };
    },
    uploadComplete: async () => ({ state: 'completed' }),
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {},
  };
  const r = await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    sleep: noSleep,
  });
  assert.equal(r.uploadId, 'abcd1234abcd1234');
  assert.equal(calls.filter((i) => i === 0).length, 2, '失败后应从 0 重传');
  assert.equal(calls.filter((i) => i === 1).length, 2);
});

test('complete 返回 failed_retriable：从第 0 片重传后完成', async () => {
  const calls = [];
  let completedOnce = false;
  const adapter = {
    uploadInit: async () => init(),
    uploadChunk: async ({ index }) => {
      calls.push(index);
      return { received_bytes: (index + 1) * 4 * MB };
    },
    uploadComplete: async () => {
      if (!completedOnce) {
        completedOnce = true;
        const err = new Error('网盘写入失败');
        err.data = { restart: true, state: 'failed_retriable' };
        throw err;
      }
      return { state: 'completed' };
    },
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {},
  };
  await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    sleep: noSleep,
  });
  assert.equal(calls.filter((i) => i === 0).length, 2);
  assert.equal(calls.filter((i) => i === 1).length, 2);
});

test('complete 网络失败：转 status 长轮询直到 completed', async () => {
  let polls = 0;
  const progress = [];
  const adapter = {
    uploadInit: async () => init(),
    uploadChunk: async ({ index }) => ({ received_bytes: (index + 1) * 4 * MB }),
    uploadComplete: async () => {
      throw new Error('504');
    },
    uploadStatus: async () => {
      polls += 1;
      return polls === 1
        ? { state: 'receiving', storage_progress: 50 }
        : { state: 'completed', storage_progress: 100 };
    },
    uploadAbort: async () => {},
  };
  const r = await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    onProgress: (p) => progress.push(p),
    sleep: noSleep,
  });
  assert.equal(r.uploadId, 'abcd1234abcd1234');
  assert.equal(polls, 2);
  assert.ok(progress.some((p) => p.merging === true));
  assert.equal(progress.at(-1).merging, false);
});

test('分片网络/5xx 重试：同一片重传后成功', async () => {
  const calls = [];
  let first = true;
  const adapter = {
    uploadInit: async () => init({ total_chunks: 1, chunk_size: 8 * MB }),
    uploadChunk: async ({ index }) => {
      calls.push(index);
      if (first) {
        first = false;
        const err = new Error('network');
        err.retry = true;
        throw err;
      }
      return { received_bytes: 8 * MB };
    },
    uploadComplete: async () => ({ state: 'completed' }),
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {},
  };
  await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    sleep: noSleep,
  });
  assert.deepEqual(calls, [0, 0]);
});

test('取消：中止会话并返回 aborted', async () => {
  let aborted = false;
  const adapter = {
    uploadInit: async () => init({ total_chunks: 1, chunk_size: 8 * MB }),
    uploadChunk: async () => ({ received_bytes: 8 * MB }),
    uploadComplete: async () => ({ state: 'completed' }),
    uploadStatus: async () => ({}),
    uploadAbort: async () => {
      aborted = true;
    },
  };
  const r = await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    isCancelled: () => true,
    sleep: noSleep,
  });
  assert.equal(r.aborted, true);
  assert.equal(aborted, true);
});
