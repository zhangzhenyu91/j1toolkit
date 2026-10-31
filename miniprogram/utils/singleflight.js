// 单飞执行器：并发多个 401 时只清理登录态/跳转一次；执行结束（成功或失败）后解除标记，
// 下次会话再次 401 仍可触发。
function createSingleFlight() {
  let running = false;
  return {
    run(fn) {
      if (running) return false;
      running = true;
      let result;
      try {
        result = fn();
      } catch (err) {
        running = false;
        throw err;
      }
      if (result && typeof result.then === 'function') {
        const done = () => {
          running = false;
        };
        result.then(done, done);
      } else {
        running = false;
      }
      return true;
    },
    running() {
      return running;
    },
  };
}

module.exports = { createSingleFlight };
