// 共享弹层行为：初始焦点、Tab 圈定、Escape 收起与焦点归还，滚动锁走 bodyScrollLock 引用计数/代际。
// 适配 `v-if=rendered` 的自绘弹层（Drawer / 导出弹层 / 处理弹层 / 工作台通知弹层）：
// 传入 rendered 与面板 ref，弹层出现时保存触发元素、锁滚动并把焦点移入面板，关闭后逐项还原。
import { nextTick, onBeforeUnmount, watch } from 'vue';
import { bodyScrollLock } from './scroll-lock.js';

// FOCUSABLE 面板内可聚焦元素选择器（禁用项与 tabindex=-1 不参与圈定）。
export const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// 弹层栈：叠层打开时只有最上层响应 Escape / Tab（stopPropagation 不会阻止同节点其他监听）。
const modalStack = [];
let modalSeq = 0;

// resolveTabTarget 决定 Tab 焦点目标：焦点在面板外/首尾时拉回，面板无可聚焦元素回面板本身。
// 返回 'first' | 'last' | 'panel' | null（null = 浏览器默认行为）。
export function resolveTabTarget({ list, current, panel, shiftKey } = {}) {
  const items = list || [];
  if (!items.length) return 'panel';
  const first = items[0];
  const last = items[items.length - 1];
  if (!current || !items.includes(current)) return shiftKey ? 'last' : 'first';
  if (shiftKey && (current === first || current === panel)) return 'last';
  if (!shiftKey && current === last) return 'first';
  return null;
}

// shouldRestoreFocus 关闭弹层时是否归还焦点：只有自身为栈顶或已无更高层才归还，
// 下层弹层先关不得抢回上层弹层的焦点。
export function shouldRestoreFocus(wasTop, remaining) {
  return !!wasTop || Number(remaining) === 0;
}

export function useModalBehavior(rendered, { panelRef, onEscape, lock = true } = {}) {
  const id = ++modalSeq;
  let savedFocus = null;
  let releaseLock = null;
  let active = false;

  function release() {
    if (!active) return;
    active = false;
    const wasTop = modalStack[modalStack.length - 1] === id;
    const index = modalStack.indexOf(id);
    if (index >= 0) modalStack.splice(index, 1);
    document.removeEventListener('keydown', onKeydown, true);
    if (releaseLock) {
      releaseLock();
      releaseLock = null;
    }
    if (
      shouldRestoreFocus(wasTop, modalStack.length) &&
      savedFocus &&
      document.contains(savedFocus)
    ) {
      savedFocus.focus({ preventScroll: true });
    }
    savedFocus = null;
  }

  watch(rendered, (r) => {
    if (r) {
      if (active) return;
      active = true;
      modalStack.push(id);
      savedFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      if (lock) releaseLock = bodyScrollLock.acquire();
      document.addEventListener('keydown', onKeydown, true);
      nextTick(() => panelRef.value?.focus({ preventScroll: true }));
    } else if (active) {
      release();
    }
  });

  onBeforeUnmount(release);

  function onKeydown(e) {
    if (!active || modalStack[modalStack.length - 1] !== id) return;
    if (e.key === 'Escape' && onEscape) {
      e.stopPropagation();
      onEscape();
      return;
    }
    if (e.key !== 'Tab' || !panelRef.value) return;
    const list = Array.from(panelRef.value.querySelectorAll(FOCUSABLE));
    const target = resolveTabTarget({
      list,
      current: document.activeElement,
      panel: panelRef.value,
      shiftKey: e.shiftKey,
    });
    if (!target) return;
    e.preventDefault();
    if (target === 'panel') panelRef.value.focus({ preventScroll: true });
    else if (target === 'first') list[0].focus();
    else list[list.length - 1].focus();
  }

  return { release };
}
