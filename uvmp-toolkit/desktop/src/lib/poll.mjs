// 轮询失败计数：偶发一次网络/核心抖动不结束任务，连续失败到上限才停轮询并报错。
export function createFailureTracker(limit = 5) {
  const max = Number(limit) > 0 ? Math.floor(Number(limit)) : 1;
  let count = 0;
  let exhausted = false;
  return {
    limit: max,
    get count() {
      return count;
    },
    get exhausted() {
      return exhausted;
    },
    fail() {
      count += 1;
      if (count >= max) exhausted = true;
      return exhausted;
    },
    ok() {
      count = 0;
      return count;
    },
    reset() {
      count = 0;
      exhausted = false;
    },
  };
}
