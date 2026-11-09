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

test('cancelJob 请求卡住时仍能在兜底时间后收口', async () => {
  let cancelled = false;
  globalThis.window = {
    localStorage: fakeStorage(),
    core: {
      jobRunning: () => {},
      call: (method) => {
        if (method === 'runJob') return Promise.resolve({ job_id: 'job-1' });
        if (method === 'getJob') {
          return Promise.resolve({
            id: 'job-1',
            status: 'running',
            progress: { current: 0, total: 0, label: '' },
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
  await new Promise((resolve) => setTimeout(resolve, 2700));

  assert.equal(cancelled, true);
});
