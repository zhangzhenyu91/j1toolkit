// 题库搜题：设置 schema（纯 Node，无 Electron 依赖，可 node 直测）。
// 磁盘上的 settings.json 可能被手工改坏、被旧版本写脏，或写入超范围的数字；
// 一律按 schema 钳制后再用——否则一个 interval=0 就能把主进程打满，
// 一个负数宽高会让异常窗口在截屏换算里崩掉。
'use strict';

// B 框「贴底」哨兵：只有精确等于 -1 才是约定值。多显示器下用户可能真的把框拖到
// 主屏上方（y 为负），任何其它负数都是真实坐标，不得改写成贴底。
const BOTTOM_SENTINEL = -1;

const DEFAULT_SETTINGS = {
  interval: 1000, // 扫描间隔 ms（500~3000）
  threshold: 0.6, // 匹配命中阈值（0.3~0.9）
  opacity: 0.9, // 结果窗不透明度（0.3~1）
  result: { x: -1, y: -1, w: 380, h: 260 }, // 结果窗位置与尺寸（x/y=-1 = 默认右下角）
  boxes: {
    A: { x: 80, y: 100, w: 720, h: 180, visible: true },
    B: { x: 80, y: BOTTOM_SENTINEL, w: 720, h: 180, visible: true }, // 哨兵：初始化时贴屏幕底部
  },
};

const BOX_MIN_W = 160;
const BOX_MIN_H = 60;
const RESULT_MIN_W = 260;
const RESULT_MIN_H = 140;

// 只接受「真正的数字」：null / '' / true / 数组都不算（Number(null) 会得到 0，直接放行会把坐标写成 0）
function toFiniteNumber(value) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value === 'string' && value.trim() !== '') {
    const n = Number(value);
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

function clampNumber(value, min, max, fallback) {
  const n = toFiniteNumber(value);
  if (n === null) return fallback;
  return Math.min(max, Math.max(min, n));
}

function clampInt(value, min, max, fallback) {
  const n = toFiniteNumber(value);
  if (n === null) return fallback;
  return Math.round(Math.min(max, Math.max(min, n)));
}

function normalizeBox(raw, fallback, workArea) {
  const src = raw && typeof raw === 'object' ? raw : {};
  return {
    x: clampInt(src.x, -100000, 100000, fallback.x),
    y: clampInt(src.y, -100000, 100000, fallback.y),
    w: clampInt(src.w, BOX_MIN_W, Math.max(BOX_MIN_W, workArea.width), fallback.w),
    h: clampInt(src.h, BOX_MIN_H, Math.max(BOX_MIN_H, workArea.height), fallback.h),
    visible: src.visible === undefined ? fallback.visible : !!src.visible,
  };
}

/**
 * 合并磁盘配置与默认值并钳制。
 * @param saved   已解析的 settings.json（可能为 null / 非对象）
 * @param opts.workArea 当前主显示器工作区（默认 B 框贴底计算用）
 * @param opts.workAreaFor 可选 (rect) => 工作区：多显示器下按几何所在显示器取，
 *                         缺省一律用 workArea（纯函数层不认识 Electron，由调用方注入）
 */
function normalizeSettings(saved, opts) {
  const workArea = (opts && opts.workArea) || { x: 0, y: 0, width: 1920, height: 1080 };
  const src = saved && typeof saved === 'object' && !Array.isArray(saved) ? saved : {};
  const savedBoxes = src.boxes && typeof src.boxes === 'object' ? src.boxes : {};

  // 每个窗口按自己所在显示器的工作区钳制：副屏比主屏宽/高时不该被主屏尺寸截断
  const areaFor = (raw, fallback) => {
    if (typeof (opts && opts.workAreaFor) !== 'function') return fallback;
    const r = raw && typeof raw === 'object' ? raw : {};
    const rect = {
      x: toFiniteNumber(r.x),
      y: toFiniteNumber(r.y),
      width: toFiniteNumber(r.w),
      height: toFiniteNumber(r.h),
    };
    if (rect.x === null || rect.y === null) return fallback; // 没有可信坐标：按主屏兜底
    const wa = opts.workAreaFor(rect);
    return wa && wa.width > 0 && wa.height > 0 ? wa : fallback;
  };

  const boxes = {
    A: normalizeBox(savedBoxes.A, DEFAULT_SETTINGS.boxes.A, areaFor(savedBoxes.A, workArea)),
    B: normalizeBox(savedBoxes.B, DEFAULT_SETTINGS.boxes.B, areaFor(savedBoxes.B, workArea)),
  };
  // B 框贴底哨兵：仅「未写过」（undefined）或「精确等于 -1」时按当前工作区贴底；
  // 其它负值（多显示器下把框拖到主屏上方、或副屏在主屏上方）是真实坐标，原样保留。
  const savedB = savedBoxes.B && typeof savedBoxes.B === 'object' ? savedBoxes.B : {};
  const bRawY = toFiniteNumber(savedB.y);
  if (bRawY === null || bRawY === BOTTOM_SENTINEL) {
    const wa = areaFor(savedB, workArea);
    boxes.B.y = wa.y + wa.height - boxes.B.h - 120;
  }

  const savedResult = src.result && typeof src.result === 'object' ? src.result : {};
  const resultWa = areaFor(savedResult, workArea);
  const result = {
    x: clampInt(savedResult.x, -100000, 100000, DEFAULT_SETTINGS.result.x),
    y: clampInt(savedResult.y, -100000, 100000, DEFAULT_SETTINGS.result.y),
    w: clampInt(
      savedResult.w,
      RESULT_MIN_W,
      Math.max(RESULT_MIN_W, resultWa.width),
      DEFAULT_SETTINGS.result.w
    ),
    h: clampInt(
      savedResult.h,
      RESULT_MIN_H,
      Math.max(RESULT_MIN_H, resultWa.height),
      DEFAULT_SETTINGS.result.h
    ),
  };

  return {
    interval: clampInt(src.interval, 500, 3000, DEFAULT_SETTINGS.interval),
    threshold: clampNumber(src.threshold, 0.3, 0.9, DEFAULT_SETTINGS.threshold),
    opacity: clampNumber(src.opacity, 0.3, 1, DEFAULT_SETTINGS.opacity),
    result,
    boxes,
  };
}

module.exports = {
  DEFAULT_SETTINGS,
  normalizeSettings,
  clampInt,
  clampNumber,
  BOX_MIN_W,
  BOX_MIN_H,
  RESULT_MIN_W,
  RESULT_MIN_H,
  BOTTOM_SENTINEL,
};
