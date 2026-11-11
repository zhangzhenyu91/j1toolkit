// 启动防双击：按钮 disabled 挡不住回车/连点，启动流程含 await 预检，
// 这里做同步占位，流程结束（含抛错）自动释放。
export function createSingleFlight() {
  let pending = null;
  return {
    get busy() {
      return pending !== null;
    },
    async run(task) {
      if (pending) return undefined;
      pending = Promise.resolve().then(task);
      try {
        return await pending;
      } finally {
        pending = null;
      }
    },
  };
}
