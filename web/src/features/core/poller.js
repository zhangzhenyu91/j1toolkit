// 共享轮询器：取消（离页/切换会话）立即唤醒等待、连续失败指数退避、成功归零、
// 总时限到点抛 poll_timeout；任务自身致命错误（fatal=true）直接上抛不重试。
// 视图只负责「取一次状态并返回 done 标记」，不再各自复制 setTimeout 链与退避计算。
import { pollDelay, pollExpired } from './async.js';

export { pollDelay, pollExpired };

// createPoller 生成一个轮询实例（同一实例可 reset 后复用）。
export function createPoller({
  intervalMs = 1200,
  baseMs = 1000,
  maxMs = 10000,
  limitMs = 0,
  now = () => Date.now(),
  sleep = defaultSleep,
} = {}) {
  let startedAt = now();
  let failures = 0;
  let cancelled = false;
  let generation = 0;
  let wake = null;

  // defaultSleep 可被 cancel 立即唤醒的等待。
  function defaultSleep(ms, signal) {
    return new Promise((resolve) => {
      const timer = setTimeout(resolve, ms);
      signal.wake = () => {
        clearTimeout(timer);
        signal.wake = null;
        resolve();
      };
    });
  }

  function wait(ms) {
    if (cancelled) return Promise.resolve();
    return new Promise((resolve) => {
      const token = { wake: null };
      wake = token;
      Promise.resolve(sleep(ms, token)).then(() => {
        if (wake === token) wake = null;
        resolve();
      });
    });
  }

  function cancel() {
    if (cancelled) return;
    cancelled = true;
    generation += 1;
    if (wake && wake.wake) wake.wake();
  }

  function reset() {
    startedAt = now();
    failures = 0;
    cancelled = false;
    generation += 1;
    if (wake && wake.wake) wake.wake();
    wake = null;
  }

  function expired() {
    if (!limitMs || limitMs <= 0) return false;
    return now() - startedAt >= limitMs;
  }

  function timeoutError(timeoutMessage) {
    const err = new Error(timeoutMessage || '轮询超时，请稍后重试');
    err.code = 'poll_timeout';
    return err;
  }

  // poll 循环执行 task，直到 task 返回 {done:true}、取消或超时。
  // 普通异常按失败计数退避重试；err.fatal 直接上抛。
  async function poll(task, { onData, timeoutMessage } = {}) {
    const myGeneration = generation;
    for (;;) {
      if (cancelled || myGeneration !== generation) return { cancelled: true };
      if (expired()) throw timeoutError(timeoutMessage);
      let result;
      try {
        result = await task();
      } catch (e) {
        if (cancelled || myGeneration !== generation) return { cancelled: true };
        if (e && e.fatal) throw e;
        failures += 1;
        await wait(pollDelay(failures, { baseMs, maxMs }));
        continue;
      }
      if (cancelled || myGeneration !== generation) return { cancelled: true };
      failures = 0;
      if (onData) onData(result);
      if (result && result.done) return result;
      await wait(intervalMs);
    }
  }

  return {
    poll,
    cancel,
    reset,
    expired,
    get cancelled() {
      return cancelled;
    },
    get failures() {
      return failures;
    },
    get startedAt() {
      return startedAt;
    },
  };
}
