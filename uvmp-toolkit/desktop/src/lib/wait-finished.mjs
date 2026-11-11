// 等待上一个任务的 finished：核心退出/事件丢失时 finished 可能永不到达，
// 必须有超时兜底，否则启动按钮会永久停在「启动中」。
export function waitFinished(job, timeoutMs = 5000) {
  const finished = job && job.finished;
  if (!finished || typeof finished.then !== 'function') return Promise.resolve(true);
  const limit = Number(timeoutMs) > 0 ? Number(timeoutMs) : 5000;
  return new Promise((resolve) => {
    let settled = false;
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      resolve(false);
    }, limit);
    finished.then(
      () => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve(true);
      },
      () => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve(true);
      }
    );
  });
}
