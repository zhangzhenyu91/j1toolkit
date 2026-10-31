// Call Me 流控制纯逻辑：SSE 已给出 done/error 终态后不得再按分块重臂 watchdog，
// 避免完成后的超时 abort 覆盖终态；卸载时 abort 当前 wx.request 并吞掉平台异常。

const TERMINAL_EVENTS = ['done', 'error', 'success', 'fail'];

function createChunkGate() {
  let finished = false;
  return {
    markEvent(type) {
      if (TERMINAL_EVENTS.indexOf(type) >= 0) finished = true;
    },
    canArm() {
      return !finished;
    },
    isFinished() {
      return finished;
    },
  };
}

function abortTask(task) {
  if (!task || typeof task.abort !== 'function') return false;
  try {
    task.abort();
    return true;
  } catch (e) {
    return false;
  }
}

module.exports = { createChunkGate, abortTask };
