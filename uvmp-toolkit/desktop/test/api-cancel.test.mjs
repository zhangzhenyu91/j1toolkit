import test from 'node:test';
import assert from 'node:assert/strict';

function fakeStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
  };
}

test('cancelJob 请求卡住时不提前释放实例，终态到达后收口', async () => {
  let cancelled = false;
  let getJobCalls = 0;
  globalThis.window = {
    localStorage: fakeStorage(),
    core: {
      jobRunning: () => {},
      call: (method) => {
        if (method === 'runJob') return Promise.resolve({ job_id: 'job-1' });
        if (method === 'getJob') {
          getJobCalls += 1;
          return Promise.resolve({
            id: 'job-1',
            status: 'cancelled',
            progress: { current: 0, total: 0, label: '' },
            result: { cancelled: true },
          });
        }
        if (method === 'cancelJob') return new Promise(() => {});
        return Promise.resolve({});
      },
      onEvent: () => () => {},
    },
  };

  const { runJob } = await import('../src/api.js');
  const job = runJob('defect_review', { instance_id: 'default' }, {
    onDone: (doneJob) => {
      cancelled = doneJob.status === 'cancelled';
    },
  });
  await job.jobId;
  job.cancel();
  await new Promise((resolve) => setTimeout(resolve, 200));
  assert.equal(cancelled, false);
  await new Promise((resolve) => setTimeout(resolve, 1400));

  assert.equal(cancelled, true);
});
