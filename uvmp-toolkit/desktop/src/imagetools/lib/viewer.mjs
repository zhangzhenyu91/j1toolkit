// 查看器的纯交互逻辑：缩放锚点、适应窗口、切图边界与光标处插入。
// 组件只负责把 DOM 事件喂进来，数学与边界都在这里，便于静态测试。

export const ZOOM_MIN = 0.25;
export const ZOOM_MAX = 4;
export const ZOOM_STEPS = [0.25, 0.5, 1, 2, 4];
export const WHEEL_FACTOR = 1.12;

/** 手动缩放范围：25%–400%。 */
export function clampZoom(scale) {
  const value = Number(scale);
  if (!Number.isFinite(value) || value <= 0) return ZOOM_MIN;
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, value));
}

/** 适应窗口：按画布等比缩放到完整可见，不放大超过 1:1（超大图允许低于 25%）。 */
export function fitScale(width, height, boxWidth, boxHeight) {
  const w = Number(width);
  const h = Number(height);
  const bw = Number(boxWidth);
  const bh = Number(boxHeight);
  if (!(w > 0) || !(h > 0) || !(bw > 0) || !(bh > 0)) return 1;
  return Math.min(1, Math.max(0.02, Math.min(bw / w, bh / h)));
}

/**
 * 以指针为锚点缩放：缩放前后指针下的画面内容保持不动。
 * 坐标均为画布坐标系，offset 为画面中心相对画布中心的像素偏移。
 */
export function zoomAroundOffset({
  scale,
  offsetX,
  offsetY,
  nextScale,
  pointerX,
  pointerY,
  centerX,
  centerY,
}) {
  const from = clampZoom(scale);
  const to = clampZoom(nextScale);
  const dx = Number(pointerX) - Number(centerX);
  const dy = Number(pointerY) - Number(centerY);
  const ratio = to / from;
  return {
    scale: to,
    offsetX: dx - (dx - Number(offsetX)) * ratio,
    offsetY: dy - (dy - Number(offsetY)) * ratio,
  };
}

/** 滚轮缩放：向上滚放大、向下滚缩小，结果落在手动缩放范围内。 */
export function wheelZoom(scale, deltaY) {
  const factor = Number(deltaY) < 0 ? WHEEL_FACTOR : 1 / WHEEL_FACTOR;
  return clampZoom(Number(scale) * factor);
}

/** 按钮/菜单的下一档缩放（25 → 50 → 100 …），已到边界时原样返回。 */
export function nextZoom(scale, direction) {
  const current = clampZoom(scale);
  const steps = ZOOM_STEPS;
  if (direction < 0) {
    for (let i = steps.length - 1; i >= 0; i -= 1) {
      if (steps[i] < current - 1e-6) return steps[i];
    }
    return ZOOM_MIN;
  }
  for (let i = 0; i < steps.length; i += 1) {
    if (steps[i] > current + 1e-6) return steps[i];
  }
  return ZOOM_MAX;
}

/** 切图边界：首尾不循环，越界返回 -1（调用方据此禁用按钮/忽略按键）。 */
export function stepIndex(index, direction, total) {
  const count = Number(total) || 0;
  const current = Number(index);
  if (!(count > 0) || !Number.isFinite(current) || current < 0) return -1;
  const next = current + (Number(direction) < 0 ? -1 : 1);
  if (next < 0 || next >= count) return -1;
  return next;
}

export function canStep(index, direction, total) {
  return stepIndex(index, direction, total) >= 0;
}

/** 拖动边界：任何方向都至少留 64px 画面在画布内，避免把图拖丢。 */
export function clampOffset(offsetX, offsetY, scaledWidth, scaledHeight, boxWidth, boxHeight) {
  const margin = 64;
  const limitX = Math.max(0, (Number(scaledWidth) + Number(boxWidth)) / 2 - margin);
  const limitY = Math.max(0, (Number(scaledHeight) + Number(boxHeight)) / 2 - margin);
  const x = Number.isFinite(Number(offsetX)) ? Number(offsetX) : 0;
  const y = Number.isFinite(Number(offsetY)) ? Number(offsetY) : 0;
  return {
    offsetX: Math.min(limitX, Math.max(-limitX, x)),
    offsetY: Math.min(limitY, Math.max(-limitY, y)),
  };
}

/**
 * 在光标处插入短语（等价于 input 的 setRangeText(phrase, start, end, 'end')）：
 * 有选区时替换选区，插入后光标停在短语末尾。
 */
export function insertPhrase({ value, start, end, phrase }) {
  const text = String(value == null ? '' : value);
  const from = Math.min(Math.max(0, Number(start) || 0), text.length);
  const requestedEnd = Number.isFinite(Number(end)) ? Number(end) : from;
  const to = Math.min(Math.max(from, requestedEnd), text.length);
  const inserted = String(phrase == null ? '' : phrase);
  return {
    value: text.slice(0, from) + inserted + text.slice(to),
    caret: from + inserted.length,
  };
}

// 会吃掉按键的可编辑元素与弹层：命中时快捷键一律不生效
const EDITABLE_SELECTOR = 'input, textarea, select, [contenteditable="true"]';

/**
 * 全局快捷键（F2 / ← →）是否应当跳过。
 * 输入框、下拉、可编辑区、弹层（设置/日志/取消确认）与重命名面板内都不抢键。
 */
export function hotkeyBlocked({
  page = 'viewer',
  target = null,
  renaming = false,
  modalOpen = false,
} = {}) {
  if (page !== 'viewer') return true;
  if (modalOpen) return true;
  if (renaming) return true;
  const el =
    target && typeof target.closest === 'function' ? target.closest(EDITABLE_SELECTOR) : null;
  return !!el;
}

/** 缩放百分比文案。 */
export function formatZoom(scale) {
  return `${Math.round(Number(scale) * 100)}%`;
}
