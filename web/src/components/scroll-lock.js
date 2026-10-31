// 共享页面滚动锁：引用计数 + 代际管理 document.body.style.overflow。
// 多个弹层/抽屉可同时持有；任一持有者关闭只释放自己的票据，最后一位释放才恢复原值，
// 避免「A 未激活/先关时把 B 仍需要的滚动锁提前解除」。
// 代际：每次从 0 加锁进入新一代；上一代遗留的释放闭包（旧弹层延迟卸载/重复调用）
// 不得扣减或误解锁新一代持有者。

// createScrollLock 创建一把锁（传入目标元素便于测试；默认取 document.body）。
export function createScrollLock(target) {
  let count = 0;
  let generation = 0;
  let saved = null;
  function body() {
    if (target) return target;
    if (typeof document !== 'undefined') return document.body;
    return null;
  }
  return {
    acquire() {
      const el = body();
      count += 1;
      if (count === 1) {
        generation += 1;
        if (el) {
          saved = el.style.overflow;
          el.style.overflow = 'hidden';
        }
      }
      const ticketGeneration = generation;
      let released = false;
      return () => {
        if (released) return;
        if (ticketGeneration !== generation) return; // 上一代票据：不得影响新一代
        released = true;
        count = Math.max(0, count - 1);
        if (count === 0) {
          const node = body();
          if (node && saved != null) node.style.overflow = saved;
          saved = null;
          generation += 1;
        }
      };
    },
    get count() {
      return count;
    },
    get generation() {
      return generation;
    },
  };
}

// bodyScrollLock 全应用共享唯一实例（Drawer / 全屏查看器 / 分享页 / 导出与处理弹层共用同一计数）。
export const bodyScrollLock = createScrollLock();
