// api.runJob 终态收口：getJob 丢失 / 连续失败 / core.exit / 启动失败都必须走到同一条收口，
// 解绑监听、清轮询计时器、归还 jobRunning 计数与壳层任务状态，finished 必然兑现。
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';

// 每个用例结束统一收口在跑任务，避免上一例的轮询计时器把下一例的计数改掉
afterEach(async () => {
  const { abortAllJobs } = await import('../src/api.js');
  abortAllJobs('用例结束');
  await new Promise((resolve) => setTimeout(resolve, 20));
});

function fakeStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
  };
}

function makeEnv({ getJob, runJob = () => Promise.resolve({ job_id: 'job-1' }) } = {}) {
  const listeners = new Set();
  const state = {
    jobRunning: 0,
    getJobCalls: 0,
    emit(ev) {
      for (const listener of [...listeners]) listener(ev);
    },
  };
  globalThis.window = {
    localStorage: fakeStorage(),
    core: {
      jobRunning: (delta) => {
        state.jobRunning += delta;
      },
      call: (method) => {
        if (method === 'runJob') return runJob();
        if (method === 'getJob') {
          state.getJobCalls += 1;
          return getJob(state.getJobCalls);
        }
        if (method === 'cancelJob') return Promise.resolve({});
        return Promise.resolve({});
      },
      onEvent: (cb) => {
        listeners.add(cb);
        return () => listeners.delete(cb);
      },
    },
  };
  state.listenerCount = () => listeners.size;
  return state;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const WATCHDOG_WAIT = 2000;

test('getJob 返回 null 视为任务丢失，终态失败并释放监听与计数', async () => {
  const env = makeEnv({ getJob: () => Promise.resolve(null) });
  const { runJob } = await import('../src/api.js');
  const { taskStatus } = await import('../src/lib/task-status.mjs');
  const errors = [];
  let doneCalled = false;
  const job = runJob(
    'pcd_export',
    { instance_id: 'default' },
    {
      onError: (err) => errors.push(String(err.message || err)),
      onDone: () => {
        doneCalled = true;
      },
    }
  );
  await job.jobId;
  assert.equal(env.listenerCount(), 1);
  // 空值要连续两次才判定任务丢失（避免把偶发空响应当失败），轮询间隔 1.2s
  const finishedValue = await Promise.race([
    job.finished.then(() => 'settled'),
    sleep(4000).then(() => 'timeout'),
  ]);
  assert.equal(finishedValue, 'settled');
  assert.equal(doneCalled, false);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /丢失|核心/);
  assert.equal(env.listenerCount(), 0);
  assert.equal(env.jobRunning, 0);
  assert.equal(taskStatus.running, 0);
  assert.deepEqual(Object.keys(taskStatus.jobs), []);
});

test('getJob 连续失败到上限后停止轮询并给出终态失败', async () => {
  const env = makeEnv({
    getJob: () => Promise.reject(new Error('核心进程未运行')),
  });
  const { runJob } = await import('../src/api.js');
  const errors = [];
  const job = runJob(
    'daily_sync',
    {},
    { onError: (err) => errors.push(String(err.message || err)) }
  );
  await job.jobId;
  const finishedValue = await Promise.race([
    job.finished.then(() => 'settled'),
    sleep(9000).then(() => 'timeout'),
  ]);
  assert.equal(finishedValue, 'settled');
  assert.equal(errors.length, 1);
  assert.match(errors[0], /状态/);
  const callsAtSettle = env.getJobCalls;
  assert.ok(callsAtSettle >= 2);
  await sleep(WATCHDOG_WAIT);
  assert.equal(env.getJobCalls, callsAtSettle, '终态后不得继续轮询');
  assert.equal(env.listenerCount(), 0);
  assert.equal(env.jobRunning, 0);
});

test('core.exit 让所有在跑任务统一收口，互不影响各自回调', async () => {
  const env = makeEnv({
    runJob: () => Promise.resolve({ job_id: `job-${Math.random().toString(36).slice(2, 7)}` }),
    getJob: () => Promise.resolve({ status: 'running', progress: { current: 0, total: 0 } }),
  });
  env.coreEmit = env.emit;
  const { runJob } = await import('../src/api.js');
  const hits = [];
  const first = runJob(
    'pcd_export',
    { instance_id: 'a' },
    {
      onError: (err) => hits.push('a:' + String(err.message || err)),
    }
  );
  const second = runJob(
    'defect_review',
    { instance_id: 'b' },
    {
      onError: (err) => hits.push('b:' + String(err.message || err)),
    }
  );
  await Promise.all([first.jobId, second.jobId]);
  assert.equal(env.listenerCount(), 2);
  env.emit({ event: 'core.exit', code: 9 });
  assert.equal(hits.length, 2);
  assert.match(hits[0], /核心.*退出/);
  await Promise.all([first.finished, second.finished]);
  assert.equal(env.listenerCount(), 0);
  assert.equal(env.jobRunning, 0);
});

test('runJob 启动失败同样走收口，finished 兑现且不重复上报', async () => {
  const env = makeEnv({ runJob: () => Promise.reject(new Error('核心进程未运行')) });
  const { runJob } = await import('../src/api.js');
  const errors = [];
  const job = runJob(
    'tree_sync',
    {},
    { onError: (err) => errors.push(String(err.message || err)) }
  );
  await assert.rejects(() => job.jobId, /核心进程未运行/);
  await job.finished;
  assert.deepEqual(errors, ['核心进程未运行']);
  assert.equal(env.listenerCount(), 0);
  assert.equal(env.jobRunning, 0);
});

test('abortAllJobs 用于核心重启前兜底：在跑任务统一失败并停止轮询', async () => {
  const env = makeEnv({ getJob: () => Promise.resolve({ status: 'running' }) });
  const { abortAllJobs, runJob } = await import('../src/api.js');
  const { taskStatus } = await import('../src/lib/task-status.mjs');
  const errors = [];
  const job = runJob(
    'course_autoplay',
    {},
    { onError: (err) => errors.push(String(err.message || err)) }
  );
  await job.jobId;
  abortAllJobs('核心已重启，任务已中断');
  await job.finished;
  assert.deepEqual(errors, ['核心已重启，任务已中断']);
  const calls = env.getJobCalls;
  await sleep(WATCHDOG_WAIT);
  assert.equal(env.getJobCalls, calls);
  assert.equal(env.listenerCount(), 0);
  assert.equal(env.jobRunning, 0);
  assert.equal(taskStatus.running, 0);
});
