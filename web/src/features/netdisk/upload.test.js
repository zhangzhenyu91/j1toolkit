// 网盘分片上传状态机测试（node --test）：重试、failed_retriable 从 0 重传、
// 普通直传 complete 超时后 status 长轮询、暂存收齐即 accepted、取消、
// 暂存 accepted 后的网盘写入终态跟随。
import assert from 'node:assert/strict';
import test from 'node:test';

import { followStagedUpload, uploadWithAdapter } from './upload.js';

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

test('断点续传：已收分片计入进度，只补传缺失片', async () => {
  const calls = [];
  const progress = [];
  const adapter = {
    uploadInit: async () => init({ received: [[0, 0]], received_bytes: 4 * MB }),
    uploadChunk: async ({ index }) => {
      calls.push(index);
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
    onProgress: (p) => progress.push(p),
    sleep: noSleep,
  });
  assert.deepEqual(calls, [1]);
  assert.equal(progress[0].doneChunks, 1);
  assert.equal(progress[0].received, 4 * MB);
  assert.equal(progress.at(-1).doneChunks, 2);
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

test('普通直传 complete 返回 uploading：继续轮询 status 到 completed', async () => {
  let polls = 0;
  const progress = [];
  const adapter = {
    uploadInit: async () => init(),
    uploadChunk: async ({ index }) => ({ received_bytes: (index + 1) * 4 * MB }),
    uploadComplete: async () => ({
      state: 'uploading',
      received_bytes: 8 * MB,
      storage_progress: 10,
    }),
    uploadStatus: async () => {
      polls += 1;
      return polls === 1
        ? { state: 'uploading', received_bytes: 8 * MB, storage_progress: 40 }
        : { state: 'completed', received_bytes: 8 * MB, storage_progress: 100 };
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
  assert.ok(progress.some((p) => p.merging && p.storageProgress === 40));
  assert.equal(progress.at(-1).merging, false);
  assert.equal(progress.at(-1).storageProgress, 100);
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

test('服务端暂存并发：按 init.chunk_concurrency 限制片内并发', async () => {
  let active = 0;
  let peak = 0;
  const adapter = {
    uploadInit: async () => init({ chunk_concurrency: 1, total_chunks: 3, chunk_size: 4 * MB }),
    uploadChunk: async ({ index }) => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((resolve) => setTimeout(resolve, 5));
      active -= 1;
      return { received_bytes: (index + 1) * 4 * MB };
    },
    uploadComplete: async () => ({ state: 'completed' }),
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {},
  };
  await uploadWithAdapter({
    file: file(12 * MB),
    space: 'my',
    path: '',
    adapter,
    sleep: noSleep,
  });
  assert.equal(peak, 1);
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

test('服务端暂存：收齐分片即 accepted，不等待 OpenList complete/status', async () => {
  let completeCalls = 0;
  let statusCalls = 0;
  const adapter = {
    uploadInit: async () => init({ staged: true, state: 'staging' }),
    uploadChunk: async ({ index }) => ({
      staged: true,
      state: 'uploading',
      received_bytes: 8 * MB,
      received: [[0, 1]],
      storage_progress: index === 0 ? 0 : 10,
    }),
    uploadComplete: async () => {
      completeCalls += 1;
      return { state: 'completed' };
    },
    uploadStatus: async () => {
      statusCalls += 1;
      return { state: 'completed' };
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
  assert.equal(r.accepted, true);
  assert.equal(r.staged, true);
  assert.equal(r.uploadId, 'abcd1234abcd1234');
  assert.equal(completeCalls, 0);
  assert.equal(statusCalls, 0);
});

test('字节级进度：多片并发按增量平滑累计且不因响应重复计数', async () => {
  const progress = [];
  const adapter = {
    uploadInit: async () => init({ staged: true, state: 'staging' }),
    uploadChunk: async ({ blob, onProgress }) => {
      onProgress(blob.size / 2);
      await Promise.resolve();
      onProgress(blob.size);
      return {};
    },
    uploadComplete: async () => ({ state: 'completed' }),
    uploadStatus: async () => {
      throw new Error('不应轮询');
    },
    uploadAbort: async () => {},
  };
  const result = await uploadWithAdapter({
    file: file(8 * MB),
    space: 'my',
    path: '',
    adapter,
    onProgress: (p) => progress.push(p),
    sleep: noSleep,
  });
  const received = progress.map((p) => p.received);
  assert.equal(result.accepted, true);
  assert.ok(received.includes(2 * MB));
  assert.ok(received.includes(4 * MB));
  assert.ok(received.includes(6 * MB));
  assert.ok(received.every((value, index) => index === 0 || value >= received[index - 1]));
  assert.equal(Math.max(...received), 8 * MB);
  assert.equal(progress.at(-1).received, 8 * MB);
});

test('字节级进度：同片重试保持高点，不因重新发送而倒退', async () => {
  const progress = [];
  let attempt = 0;
  const adapter = {
    uploadInit: async () =>
      init({ staged: true, state: 'staging', total_chunks: 1, chunk_size: 8 * MB }),
    uploadChunk: async ({ blob, onProgress }) => {
      attempt += 1;
      if (attempt === 1) {
        onProgress(4 * MB);
        const err = new Error('network');
        err.retry = true;
        throw err;
      }
      onProgress(2 * MB);
      onProgress(6 * MB);
      onProgress(blob.size);
      return {};
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
    onProgress: (p) => progress.push(p.received),
    sleep: noSleep,
  });
  assert.ok(progress.every((value, index) => index === 0 || value >= progress[index - 1]));
  assert.ok(progress.includes(4 * MB));
  assert.ok(progress.includes(6 * MB));
  assert.equal(progress.at(-1), 8 * MB);
});

// 假时钟：sleep 推进 now，测试跟随轮询无需真实等待
const fakeClock = () => {
  let t = 0;
  return {
    now: () => t,
    sleep: async (ms) => {
      t += ms || 0;
    },
  };
};

test('followStagedUpload：轮询到 completed 并回报中间快照', async () => {
  const clock = fakeClock();
  const snaps = [];
  const seq = [
    { state: 'uploading', storage_progress: 30 },
    { state: 'uploading', storage_progress: 70 },
    { state: 'completed', storage_progress: 100 },
  ];
  let polls = 0;
  const adapter = {
    uploadStatus: async ({ uploadId }) => {
      assert.equal(uploadId, 'staged01');
      return seq[Math.min(polls++, seq.length - 1)];
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'staged01',
    onSnap: (s) => snaps.push(s),
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.completed, true);
  assert.equal(polls, 3);
  assert.deepEqual(
    snaps.map((s) => s.state),
    ['uploading', 'uploading', 'completed']
  );
  assert.equal(snaps[1].storage_progress, 70);
});

test('followStagedUpload：failed_permanent 返回失败原因', async () => {
  const clock = fakeClock();
  const adapter = {
    uploadStatus: async () => ({
      state: 'failed_permanent',
      storage_progress: 20,
      error: '网盘空间不足',
    }),
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.failed, '网盘空间不足');
  assert.equal(r.completed, undefined);
});

test('followStagedUpload：aborted 也按失败返回', async () => {
  const clock = fakeClock();
  const adapter = { uploadStatus: async () => ({ state: 'aborted' }) };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.failed, '网盘写入失败');
});

test('followStagedUpload：中途一次网络错误后仍能完成', async () => {
  const clock = fakeClock();
  let polls = 0;
  const adapter = {
    uploadStatus: async () => {
      polls += 1;
      if (polls === 1) throw new Error('502');
      return { state: 'completed', storage_progress: 100 };
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.completed, true);
  assert.equal(polls, 2);
});

test('followStagedUpload：会话 404 返回 missing 并停止轮询', async () => {
  const clock = fakeClock();
  let polls = 0;
  const adapter = {
    uploadStatus: async () => {
      polls += 1;
      const err = new Error('会话不存在');
      err.code = 404;
      throw err;
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.missing, true);
  assert.equal(polls, 1);
});

test('followStagedUpload：not found 文案同样判定 missing', async () => {
  const clock = fakeClock();
  const adapter = {
    uploadStatus: async () => {
      throw new Error('upload session not found');
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.missing, true);
});

test('followStagedUpload：服务端 40400「不存在」同样判定 missing', async () => {
  const clock = fakeClock();
  const adapter = {
    uploadStatus: async () => {
      const err = new Error('文件或目录不存在');
      err.code = 40400;
      err.status = 404;
      throw err;
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.missing, true);
});

test('followStagedUpload：isCancelled 立即退出且不再请求', async () => {
  const clock = fakeClock();
  let polls = 0;
  const adapter = {
    uploadStatus: async () => {
      polls += 1;
      return { state: 'uploading', storage_progress: 10 };
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    isCancelled: () => true,
    sleep: clock.sleep,
    now: clock.now,
  });
  assert.equal(r.cancelled, true);
  assert.equal(polls, 0);
});

test('followStagedUpload：超过 maxMs 返回 timeout', async () => {
  const clock = fakeClock();
  let polls = 0;
  const adapter = {
    uploadStatus: async () => {
      polls += 1;
      return { state: 'uploading', storage_progress: 50 };
    },
  };
  const r = await followStagedUpload({
    adapter,
    uploadId: 'x',
    sleep: clock.sleep,
    pollMs: 1000,
    maxMs: 2500,
    now: clock.now,
  });
  assert.equal(r.timeout, true);
  assert.equal(polls, 2); // 0s/1s/2s 三次 sleep 后 3s ≥ 2.5s 跳出，前两轮已请求
});
