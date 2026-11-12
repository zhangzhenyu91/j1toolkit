// CI 冒烟：真实桌面窗口截图（desktopCapturer 整屏帧 + 按窗口位置裁剪）。
//
// 与 webContents.capturePage 的区别：capturePage 只有页面内容，没有系统窗口外框与圆角外的桌面；
// 本模块截真实屏幕像素，裁剪范围 = win.getBounds() 向外扩展 padding（DIP，默认 0）
// × 窗口所在显示器的 scaleFactor，因此四角能看到窗口圆角之外的桌面，
// 也能用 padding 把 CSD/thickFrame 的真实外框与阴影纳入截图，用于冒烟时检查原生窗口形状/尺寸。
//
// 隐私边界：只在 UVMP_CI_SMOKE 的自动化冒烟中调用。截图会包含桌面上的其他窗口与用户内容，
// 严禁在真实用户会话里采集；日志只输出 bounds/contentBounds/contentSize/DPI/标题栏按钮边界，
// 不输出窗口标题、页面内容与完整路径。
//
// 正确性约束（任何一步放宽都会静默截到错位置或错缩放）：
//   1) 窗口与 padding 后的截图范围都必须完整落在指定显示器内，跨屏/越界直接报错——
//      不夹取，夹取会截掉窗口边缘或外框阴影；
//   2) 屏幕源必须与指定显示器匹配，多屏不盲取 sources[0]；
//   3) 屏幕源像素尺寸必须恰好等于「显示器 DIP × scaleFactor」，否则裁剪会带上缩放，
//      违背「保存原始像素、不能二次 resize」；
//   4) 裁剪结果尺寸必须与计算值一致，否则报错，不保存错图。
'use strict';

const fs = require('fs');
const path = require('path');
const { pickSource } = require('./quizsearch/capture-source.cjs');

const LOG_PREFIX = '[window-capture]';
const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

function captureError(code, message, details) {
  const err = new Error(message);
  err.code = code;
  if (details) err.details = details;
  return err;
}

function requireSmokeMode() {
  if (!process.env.UVMP_CI_SMOKE) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_DISABLED',
      '真实桌面截图仅允许在 UVMP_CI_SMOKE 冒烟中调用（真实用户会话禁止采集屏幕）'
    );
  }
}

function readWindowBounds(win) {
  if (!win || typeof win.getBounds !== 'function') {
    throw captureError('ERR_WINDOW_CAPTURE_WINDOW', '未提供有效的 BrowserWindow');
  }
  if (typeof win.isDestroyed === 'function' && win.isDestroyed()) {
    throw captureError('ERR_WINDOW_CAPTURE_WINDOW', '窗口已销毁，无法截图');
  }
  if (typeof win.isMinimized === 'function' && win.isMinimized()) {
    throw captureError('ERR_WINDOW_CAPTURE_WINDOW', '窗口处于最小化，真实桌面截图会截到别的画面');
  }
  if (typeof win.isVisible === 'function' && !win.isVisible()) {
    throw captureError('ERR_WINDOW_CAPTURE_WINDOW', '窗口不可见，真实桌面截图会截到别的画面');
  }
  const raw = win.getBounds();
  const bounds = {
    x: Number(raw && raw.x),
    y: Number(raw && raw.y),
    width: Number(raw && raw.width),
    height: Number(raw && raw.height),
  };
  if (
    !Number.isFinite(bounds.x) ||
    !Number.isFinite(bounds.y) ||
    !Number.isFinite(bounds.width) ||
    !Number.isFinite(bounds.height) ||
    bounds.width <= 0 ||
    bounds.height <= 0
  ) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_BOUNDS',
      '窗口 getBounds() 返回非法尺寸：' + JSON.stringify(raw)
    );
  }
  return bounds;
}

function normalizePadding(padding) {
  if (padding === undefined || padding === null) return 0;
  const value = Number(padding);
  if (!Number.isFinite(value) || value < 0) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_PADDING',
      'padding 必须是非负有限数字（DIP）：' + String(padding)
    );
  }
  return value;
}

// padding 是 DIP：以窗口 bounds 四边为基准向外平移，宽高各增加 2 × padding。
function applyPadding(bounds, padding) {
  if (padding === 0) return { ...bounds };
  return {
    x: bounds.x - padding,
    y: bounds.y - padding,
    width: bounds.width + padding * 2,
    height: bounds.height + padding * 2,
  };
}

function resolveOutputPath(outputPath) {
  if (typeof outputPath !== 'string' || outputPath.trim() === '') {
    throw captureError('ERR_WINDOW_CAPTURE_OUTPUT', '必须显式指定截图输出文件 outputPath');
  }
  return path.resolve(outputPath);
}

function loadElectron() {
  let electron = null;
  try {
    electron = require('electron');
  } catch (_e) {
    electron = null;
  }
  if (!electron || typeof electron !== 'object' || !electron.screen || !electron.desktopCapturer) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_ELECTRON',
      '必须在 Electron 主进程调用，或在单元测试中注入 capturer/screen/display'
    );
  }
  return electron;
}

function normalizeDisplay(display) {
  if (!display || !display.bounds) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_DISPLAY',
      '未提供有效的显示器 display（需含 bounds/scaleFactor/id）'
    );
  }
  const raw = display.bounds;
  const bounds = {
    x: Number(raw.x),
    y: Number(raw.y),
    width: Number(raw.width),
    height: Number(raw.height),
  };
  if (
    !Number.isFinite(bounds.x) ||
    !Number.isFinite(bounds.y) ||
    !Number.isFinite(bounds.width) ||
    !Number.isFinite(bounds.height) ||
    bounds.width <= 0 ||
    bounds.height <= 0
  ) {
    throw captureError('ERR_WINDOW_CAPTURE_DISPLAY', '显示器 bounds 非法：' + JSON.stringify(raw));
  }
  const scaleFactor = Number(display.scaleFactor);
  if (!Number.isFinite(scaleFactor) || scaleFactor <= 0) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_DISPLAY',
      '显示器 scaleFactor 非法：' + String(display.scaleFactor)
    );
  }
  if (display.id === undefined || display.id === null || String(display.id) === '') {
    throw captureError('ERR_WINDOW_CAPTURE_DISPLAY', '显示器缺少 id，无法核对屏幕源');
  }
  const size = display.size || {};
  const displaySize = {
    width: Number(size.width) || bounds.width,
    height: Number(size.height) || bounds.height,
  };
  return { id: display.id, bounds, size: displaySize, scaleFactor };
}

function assertWindowInsideDisplay(rect, display, label = '窗口', extra = null) {
  const db = display.bounds;
  const overflow = {
    left: Math.max(0, db.x - rect.x),
    top: Math.max(0, db.y - rect.y),
    right: Math.max(0, rect.x + rect.width - (db.x + db.width)),
    bottom: Math.max(0, rect.y + rect.height - (db.y + db.height)),
  };
  const out = Object.keys(overflow).filter((key) => overflow[key] > 1e-6);
  if (out.length > 0) {
    const text = out.map((key) => `${key}=${overflow[key]}`).join(' ');
    throw captureError(
      'ERR_WINDOW_CAPTURE_OFFSCREEN',
      `${label}未完整落在指定显示器内（${text} DIP），拒绝截图以免裁掉窗口边缘`,
      extra ? { overflow, ...extra } : overflow
    );
  }
}

function resolveDisplays(opts, screenApi) {
  if (Array.isArray(opts.displays)) return opts.displays;
  if (screenApi && typeof screenApi.getAllDisplays === 'function') {
    try {
      const all = screenApi.getAllDisplays();
      return Array.isArray(all) ? all : null;
    } catch (_e) {
      return null;
    }
  }
  return null;
}

// 计算物理像素裁剪矩形：左右/上下分别取整再相减，保证两边都对齐像素边界。
function computeCropRect(bounds, display, sourceWidth, sourceHeight) {
  const scale = display.scaleFactor;
  const left = Math.round((bounds.x - display.bounds.x) * scale);
  const top = Math.round((bounds.y - display.bounds.y) * scale);
  const right = Math.round((bounds.x + bounds.width - display.bounds.x) * scale);
  const bottom = Math.round((bounds.y + bounds.height - display.bounds.y) * scale);
  const rect = { x: left, y: top, width: right - left, height: bottom - top };
  if (rect.width <= 0 || rect.height <= 0) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_BOUNDS',
      '窗口按 scaleFactor 换算后尺寸为 0：' + JSON.stringify(rect)
    );
  }
  if (left < 0 || top < 0 || right > sourceWidth || bottom > sourceHeight) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_OFFSCREEN',
      `窗口裁剪矩形超出屏幕帧（rect=${JSON.stringify(rect)}, source=${sourceWidth}x${sourceHeight}）`,
      { rect, sourceWidth, sourceHeight }
    );
  }
  return rect;
}

function readContentSize(win, fallback) {
  try {
    if (typeof win.getContentSize === 'function') {
      const size = win.getContentSize();
      if (Array.isArray(size)) {
        const width = Number(size[0]);
        const height = Number(size[1]);
        if (Number.isFinite(width) && Number.isFinite(height)) return { width, height };
      }
    }
  } catch (_e) {
    // 内容尺寸只用于日志；读不到时退回窗口 DIP 尺寸
  }
  return { width: fallback.width, height: fallback.height };
}

// 内容区 DIP 边界只用于日志核对 CSD 标题栏/边框预留，读取失败不影响截图。
function readContentBounds(win) {
  if (!win || typeof win.getContentBounds !== 'function') return null;
  try {
    const raw = win.getContentBounds();
    if (!raw) return null;
    const bounds = {
      x: Number(raw.x),
      y: Number(raw.y),
      width: Number(raw.width),
      height: Number(raw.height),
    };
    if (
      !Number.isFinite(bounds.x) ||
      !Number.isFinite(bounds.y) ||
      !Number.isFinite(bounds.width) ||
      !Number.isFinite(bounds.height) ||
      bounds.width <= 0 ||
      bounds.height <= 0
    ) {
      return null;
    }
    return bounds;
  } catch (_e) {
    return null;
  }
}

function formatContentBounds(contentBounds) {
  if (!contentBounds) return '未提供';
  return `${contentBounds.width}x${contentBounds.height} @ (${contentBounds.x},${contentBounds.y})`;
}

function round2(value) {
  return Math.round(Number(value) * 100) / 100;
}

// 标题栏按钮 DOM 边界：主代理用 executeJavaScript 采集后作为普通对象传入。
// 兼容 { titlebarButtons: [...] }、{ buttons: [...] } 与直接数组；每项支持
// { action/label/name, rect/bounds } 或扁平的 { x, y, width, height }。
function formatDomBounds(dom) {
  let list = null;
  if (Array.isArray(dom)) list = dom;
  else if (dom && Array.isArray(dom.titlebarButtons)) list = dom.titlebarButtons;
  else if (dom && Array.isArray(dom.buttons)) list = dom.buttons;
  if (!list || list.length === 0) return '未提供';
  return list
    .map((button, index) => {
      const rawLabel =
        (button && (button.action || button.label || button.name)) || `按钮${index + 1}`;
      // 日志单行输出：去掉换行/控制字符，限制长度，避免日志注入
      const label = String(rawLabel)
        .replace(/[\r\n\t]+/g, ' ')
        .slice(0, 24);
      const rect = button && (button.rect || button.bounds || button);
      const values = rect
        ? [rect.x, rect.y, rect.width, rect.height].map((value) => Number(value))
        : [];
      if (values.length !== 4 || !values.every(Number.isFinite)) {
        return `${label}(边界无效)`;
      }
      return `${label}(${values.map(round2).join(',')})`;
    })
    .join(' ');
}

/**
 * 截取窗口在真实桌面上的完整像素（含圆角外桌面）并保存 PNG。
 *
 * @param {object} opts
 * @param {object} opts.win BrowserWindow（用到 getBounds/getContentSize/getContentBounds/isDestroyed/isMinimized/isVisible）
 * @param {object} [opts.display] screen.getDisplayMatching(win.getBounds()) 的结果；缺省用 screen 解析
 * @param {string} opts.outputPath 显式输出 PNG 路径（父目录不存在会创建）
 * @param {number} [opts.padding=0] 向外扩展截图的 DIP 像素，用于纳入 CSD/thickFrame 外框与阴影；
 *   扩展后必须完整落在目标显示器内，默认 0（与旧行为一致）
 * @param {object} [opts.dom] 主代理另行采集的 DOM 信息（仅读取标题栏按钮边界打印，原样返回）
 * @param {object} [opts.capturer] desktopCapturer；测试可注入 { getSources }
 * @param {object} [opts.screen] screen；测试可注入 { getDisplayMatching, getAllDisplays }
 * @param {object[]} [opts.displays] 全部显示器（用于单屏回退判定；缺省取 screen.getAllDisplays()）
 * @param {(line: string) => void} [opts.log] 日志输出，默认 console.log
 * @returns {Promise<{outputPath: string, width: number, height: number, bounds: object,
 *   scaleFactor: number, dom: object|null, captureBounds?: object}>} width/height 为截图像素尺寸，
 *   bounds 为窗口 DIP 尺寸；padding > 0 时额外返回扩展后的 captureBounds（DIP）
 */
async function captureWindowDesktop(opts = {}) {
  requireSmokeMode();
  const { win, outputPath, dom = null, log = console.log } = opts;
  const padding = normalizePadding(opts.padding);
  const bounds = readWindowBounds(win);
  const resolvedOutput = resolveOutputPath(outputPath);

  let electron = null;
  const getElectron = () => {
    if (!electron) electron = loadElectron();
    return electron;
  };
  const screenApi = opts.screen || (opts.display ? null : getElectron().screen);
  const capturer = opts.capturer || getElectron().desktopCapturer;

  let display = opts.display;
  if (!display) {
    if (!screenApi || typeof screenApi.getDisplayMatching !== 'function') {
      throw captureError(
        'ERR_WINDOW_CAPTURE_DISPLAY',
        '缺少 display，且没有可用的 screen.getDisplayMatching'
      );
    }
    display = screenApi.getDisplayMatching(bounds);
  }
  const normalized = normalizeDisplay(display);
  assertWindowInsideDisplay(bounds, normalized);
  const captureBounds = applyPadding(bounds, padding);
  if (padding > 0) {
    assertWindowInsideDisplay(captureBounds, normalized, `padding=${padding} DIP 后的截图范围`, {
      padding,
      bounds: { ...bounds },
      captureBounds: { ...captureBounds },
    });
  }

  // 屏幕源必须按显示器 DIP × 缩放请求，拿到的像素尺寸再核对一次 1:1。
  const expectedWidth = Math.round(normalized.size.width * normalized.scaleFactor);
  const expectedHeight = Math.round(normalized.size.height * normalized.scaleFactor);
  if (expectedWidth <= 0 || expectedHeight <= 0) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_DISPLAY',
      `显示器物理尺寸非法：${expectedWidth}x${expectedHeight}`
    );
  }

  let sources;
  try {
    sources = await capturer.getSources({
      types: ['screen'],
      thumbnailSize: { width: expectedWidth, height: expectedHeight },
    });
  } catch (e) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_SOURCE',
      'desktopCapturer.getSources 失败：' + (e && (e.message || e))
    );
  }
  const sourceList = Array.isArray(sources) ? sources : [];
  const displays = resolveDisplays(opts, screenApi);
  const picked = pickSource(sourceList, normalized, displays ? displays.length : 0);
  if (!picked.source) {
    throw captureError('ERR_WINDOW_CAPTURE_SOURCE', picked.reason || '未能匹配到屏幕源');
  }

  const thumbnail = picked.source.thumbnail;
  if (!thumbnail || typeof thumbnail.getSize !== 'function') {
    throw captureError('ERR_WINDOW_CAPTURE_SOURCE', '屏幕源没有 thumbnail');
  }
  if (typeof thumbnail.isEmpty === 'function' && thumbnail.isEmpty()) {
    throw captureError('ERR_WINDOW_CAPTURE_SOURCE', '屏幕源 thumbnail 为空');
  }
  const thumbSize = thumbnail.getSize();
  const sourceWidth = Number(thumbSize && thumbSize.width);
  const sourceHeight = Number(thumbSize && thumbSize.height);
  if (sourceWidth !== expectedWidth || sourceHeight !== expectedHeight) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_SOURCE_SIZE',
      `屏幕源尺寸与显示器物理尺寸不一致（期望 ${expectedWidth}x${expectedHeight}，实际 ${sourceWidth}x${sourceHeight}）；` +
        '继续裁剪会得到缩放图，已拒绝（可换一块屏幕或降低缩放）',
      { expectedWidth, expectedHeight, sourceWidth, sourceHeight }
    );
  }

  const rect = computeCropRect(captureBounds, normalized, sourceWidth, sourceHeight);
  let cropped;
  try {
    cropped = thumbnail.crop(rect);
  } catch (e) {
    throw captureError('ERR_WINDOW_CAPTURE_CROP', '裁剪屏幕帧失败：' + (e && (e.message || e)));
  }
  if (!cropped || (typeof cropped.isEmpty === 'function' && cropped.isEmpty())) {
    throw captureError('ERR_WINDOW_CAPTURE_CROP', '裁剪结果为空');
  }
  const croppedSize = cropped.getSize();
  const cropWidth = Number(croppedSize && croppedSize.width);
  const cropHeight = Number(croppedSize && croppedSize.height);
  if (cropWidth !== rect.width || cropHeight !== rect.height) {
    throw captureError(
      'ERR_WINDOW_CAPTURE_CROP',
      `裁剪结果尺寸不符（期望 ${rect.width}x${rect.height}，实际 ${cropWidth}x${cropHeight}）`,
      { rect, cropWidth, cropHeight }
    );
  }

  let png;
  try {
    png = cropped.toPNG();
  } catch (e) {
    throw captureError('ERR_WINDOW_CAPTURE_PNG', 'PNG 编码失败：' + (e && (e.message || e)));
  }
  if (
    !Buffer.isBuffer(png) ||
    png.length < PNG_SIGNATURE.length ||
    !png.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE)
  ) {
    throw captureError('ERR_WINDOW_CAPTURE_PNG', '裁剪结果不是有效 PNG');
  }

  try {
    fs.mkdirSync(path.dirname(resolvedOutput), { recursive: true });
    fs.writeFileSync(resolvedOutput, png);
  } catch (e) {
    const err = captureError(
      'ERR_WINDOW_CAPTURE_WRITE',
      '写入截图失败：' + (e && (e.message || e))
    );
    err.cause = e;
    throw err;
  }

  const content = readContentSize(win, bounds);
  const contentBounds = readContentBounds(win);
  const dpi = Math.round(normalized.scaleFactor * 96);
  log(
    `${LOG_PREFIX} 窗口 DIP ${bounds.width}x${bounds.height} @ (${bounds.x},${bounds.y})｜` +
      (padding > 0
        ? `padding ${padding} DIP → 截图范围 ${captureBounds.width}x${captureBounds.height} ` +
          `@ (${captureBounds.x},${captureBounds.y})｜`
        : '') +
      `内容 ${content.width}x${content.height}｜内容区 ${formatContentBounds(contentBounds)}｜` +
      `DPI ${dpi}（scale ${normalized.scaleFactor}）｜` +
      `截图 ${rect.width}x${rect.height} px｜标题栏按钮 ${formatDomBounds(dom)}｜` +
      `已保存 ${path.basename(resolvedOutput)}`
  );

  const result = {
    outputPath: resolvedOutput,
    width: rect.width,
    height: rect.height,
    bounds: { ...bounds },
    scaleFactor: normalized.scaleFactor,
    dom,
  };
  if (padding > 0) result.captureBounds = { ...captureBounds };
  return result;
}

module.exports = { captureWindowDesktop };
