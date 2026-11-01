// 可卸载终止的等待门：页面 onUnload 时 stop()，正在 sleep 的轮询立即 resolve，
// 避免 await 永久挂起（同步进度 / 每日核查 / 照片任务轮询共用）。
function createStopGate(setTimeoutFn, clearTimeoutFn) {
  const setT = setTimeoutFn || setTimeout;
  const clearT = clearTimeoutFn || clearTimeout;
  let stopped = false;
  const waits = new Set();

  return {
    get stopped() {
      return stopped;
    },
    sleep(ms) {
      if (stopped) return Promise.resolve();
      return new Promise((resolve) => {
        const entry = { resolve, timer: null };
        entry.timer = setT(() => {
          waits.delete(entry);
          resolve();
        }, ms);
        waits.add(entry);
      });
    },
    stop() {
      if (stopped) return;
      stopped = true;
      waits.forEach((entry) => {
        clearT(entry.timer);
        entry.resolve();
      });
      waits.clear();
    },
  };
}

module.exports = { createStopGate };
