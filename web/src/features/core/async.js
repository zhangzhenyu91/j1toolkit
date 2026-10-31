// 异步请求代际与轮询退避（各视图共用）：
// createGeneration 解决「切班组 / 切筛选 / 自动刷新」时旧响应覆盖新状态；
// pollDelay / pollExpired 给轮询类任务提供指数退避与总时限，避免固定频率空转和无限等待。

// createGeneration 单调递增请求代际；只有 isCurrent(next()) 的响应允许落地。
export function createGeneration() {
  let current = 0;
  return {
    next() {
      current += 1;
      return current;
    },
    current() {
      return current;
    },
    isCurrent(token) {
      return token === current;
    },
    invalidate() {
      current += 1;
    },
  };
}

// createResourceGates 按资源拆分请求代际（出工日志的 month/logs/report/photos/paSync 等）：
// 同一 tick 内 MonthCalendar 先 emit update:month 再 emit update:selected 时，两个监听各取自己的
// 票据，互不作废；同一资源的连续请求仍只有最后一次可落地。invalidateAll 用于切班组/范围。
export function createResourceGates(names = []) {
  const list = [...names];
  const generations = new Map(list.map((name) => [name, createGeneration()]));
  return {
    names: list.slice(),
    next(name) {
      const gen = generations.get(name);
      if (!gen) throw new Error(`未知资源代际：${name}`);
      return { name, token: gen.next() };
    },
    isCurrent(ticket) {
      const gen = ticket && generations.get(ticket.name);
      return !!gen && gen.isCurrent(ticket.token);
    },
    invalidate(name) {
      const gen = generations.get(name);
      if (gen) gen.invalidate();
    },
    invalidateAll() {
      generations.forEach((gen) => gen.invalidate());
    },
  };
}

// pollDelay 连续失败次数 → 下一次等待毫秒数（指数退避，默认 1s 起、10s 封顶）。
export function pollDelay(failures, { baseMs = 1000, maxMs = 10000 } = {}) {
  const n = Number(failures) > 0 ? Math.floor(Number(failures)) : 0;
  if (!n) return baseMs;
  return Math.min(maxMs, baseMs * 2 ** Math.min(n, 30));
}

// pollExpired 轮询总时限是否已到（limitMs<=0 表示不设时限）。
export function pollExpired(startedAt, now, limitMs) {
  if (!limitMs || limitMs <= 0) return false;
  return Number(now) - Number(startedAt) >= limitMs;
}
