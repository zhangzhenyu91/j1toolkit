// 题库搜题：壳层主服务（Electron 主进程内运行）。
// 职责：扫描框/结果窗生命周期、截屏轮询、OCR 子进程编排、题库匹配、设置与题库持久化、IPC。
// 设计要点：
//   - 双扫描框（A/B）独立拖动/缩放/显隐，几何持久化；两框各自 OCR 各自匹配，结果窗分区展示；
//   - 截屏用 desktopCapturer 整屏帧按框裁剪（按所在显示器 scaleFactor 换算物理像素）；
//   - 逐帧哈希，画面未变跳过 OCR，弱机减负——哈希只在帧真正进入 OCR 后才记下；
//   - OCR 在 worker_threads 线程（ocr-worker.cjs），模型常驻内存，主进程不阻塞；
//   - 题库 JSON 落盘 userData/quiz-search/（banks-index.json + banks/<id>.json），不依赖 Excel 运行时；
//   - 所有 IPC 异常统一以「中文错误信息」回传，渲染层收到后重拉 getState 对齐权威状态。
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { BankStore } = require('./bank-store.cjs');
const {
  DEFAULT_SETTINGS,
  normalizeSettings,
  clampInt,
  clampNumber,
  BOX_MIN_W,
  BOX_MIN_H,
} = require('./settings.cjs');
const { pickSource } = require('./capture-source.cjs');
const match = require('./match.cjs');

// Electron 依赖延迟解析：测试注入 fake 时不必加载真实 Electron（主进程才能 require）。
function electronDeps() {
  return require('electron');
}

const MAX_THUMB = 4096; // 单屏缩略图长边上限（部分平台超额直接回空——已踩坑）
const DRAG_MAX_MS = 5 * 60 * 1000; // 拖拽兜底时长

// 轻量帧哈希（抽样 FNV-1a）：画面未变时跳过 OCR
function frameHash(buf) {
  const stride = Math.max(16, Math.floor(buf.length / 4096));
  let h = 0x811c9dc5;
  for (let i = 0; i < buf.length; i += stride) {
    h ^= buf[i];
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// OCR 任务 id：高位帧纪元、低位奇偶承载框别（见 _queueOcr）
function encodeOcrId(epoch, seq, boxId) {
  return epoch * 1000000 + (seq % 500000) * 2 + (boxId === 'A' ? 0 : 1);
}
function decodeOcrId(id) {
  const epoch = Math.floor(id / 1000000);
  return { epoch, boxId: id % 2 === 0 ? 'A' : 'B' };
}

class QuizSearchService {
  /**
   * @param opts.getMainWindow  取主窗口（托盘/页面向主窗口推事件用）
   * @param opts.onScanStateChange  搜题启停回调（main.cjs 刷新托盘菜单）
   * @param opts.workArea  覆盖主显示器工作区（测试注入用，缺省现取）
   */
  constructor(opts) {
    this.opts = opts || {};
    this.el = this.opts.deps || electronDeps(); // Electron 模块面（测试可注入 fake）
    this.dataDir = this.opts.dataDir || path.join(this.el.app.getPath('userData'), 'quiz-search');
    fs.mkdirSync(this.dataDir, { recursive: true });
    this.settingsPath = path.join(this.dataDir, 'settings.json');
    this.store = new BankStore(this.dataDir);
    this.settings = this._loadSettings();

    this.scanning = false;
    this.timer = null;
    this.boxWins = { A: null, B: null };
    this.resultWin = null;
    this.worker = null;
    this.workerReady = false;
    this.workerError = '';
    this.ocrSeq = 0;
    this.inFlight = { A: false, B: false };
    this.lastHash = { A: 0, B: 0 };
    this.lastOcrMs = 0;
    this.lastTickError = '';
    this.notice = ''; // 设置保存失败等一次性提示（页面技术诊断区展示）
    this._tickBusy = false; // _tick 忙门闩：上一拍截屏未完不重入
    this.ocrEpoch = 0; // 停止/重启时作废旧帧结果
    this.bankIndex = [];
    this.indexDirty = true;
    this.boxState = { A: null, B: null }; // 各框最新匹配（结果窗数据源）
    this._lastPushed = '';
    this._boxDrag = null;
    this._resultDrag = null;
  }

  // ---------------------------------------------------------------- 设置

  _loadSettings() {
    let saved = null;
    try {
      saved = JSON.parse(fs.readFileSync(this.settingsPath, 'utf8'));
    } catch (_e) {
      /* 首次运行或文件损坏：按默认值起步 */
    }
    return normalizeSettings(saved, {
      workArea: this._workArea(),
      // 框/结果窗可能停在上次的副屏上：宽高按各自所在显示器钳制，不被主屏尺寸截断
      workAreaFor: (rect) => this._workAreaFor(rect),
    });
  }

  _workArea() {
    if (this.opts.workArea) return this.opts.workArea;
    return this.el.screen.getPrimaryDisplay().workArea;
  }

  // 多显示器：按窗口**实际所在**显示器的工作区取值（拖拽/缩放/落盘钳制都走这里）。
  // 用主屏工作区钳制会让副屏上的框被主屏尺寸截断，副屏在主屏上方时负坐标还会被拽回。
  _workAreaFor(bounds) {
    if (!bounds || !this.el.screen.getDisplayMatching) return this._workArea();
    // 脏配置里可能缺宽高/是字符串：先规整成合法矩形，别把 NaN 递给 Electron
    const rect = {
      x: Number(bounds.x) || 0,
      y: Number(bounds.y) || 0,
      width: Math.max(1, Number(bounds.width) || 1),
      height: Math.max(1, Number(bounds.height) || 1),
    };
    let display = null;
    try {
      display = this.el.screen.getDisplayMatching(rect);
    } catch (_e) {
      display = null;
    }
    return (display && display.workArea) || this._workArea();
  }

  _saveSettings() {
    const tmp = this.settingsPath + '.tmp';
    try {
      fs.writeFileSync(tmp, JSON.stringify(this.settings, null, 2), 'utf8');
      fs.renameSync(tmp, this.settingsPath);
    } catch (e) {
      this.notice = '设置保存失败：' + String((e && e.message) || e);
      try {
        fs.unlinkSync(tmp);
      } catch (_e) {
        /* 临时文件已不在 */
      }
    }
  }

  // ---------------------------------------------------------------- 模型/子进程

  // quizsearch 目录整体被 asarUnpack（模型/脚本都需真实文件路径）：
  // 打包后 __dirname 指向 asar 内，须显式换到 app.asar.unpacked（worker 读不了 asar）
  moduleDir() {
    return this.el.app.isPackaged
      ? path.join(process.resourcesPath, 'app.asar.unpacked', 'electron', 'quizsearch')
      : __dirname;
  }

  modelsDir() {
    return path.join(this.moduleDir(), 'models');
  }

  _forkWorker() {
    if (this.worker) return;
    this.workerReady = false;
    this.workerError = '';
    // worker_threads：不拉起新进程（个别麒麟环境 utilityProcess spawn 即死——已踩坑），
    // 主进程能跑它就能跑；NAPI 版 onnxruntime-node 在线程环境可用。
    const Worker = this.el.Worker || require('worker_threads').Worker;
    let proc;
    try {
      proc = new Worker(path.join(this.moduleDir(), 'ocr-worker.cjs'), {});
    } catch (e) {
      this.workerError = 'OCR 线程创建失败：' + (e && (e.stack || e));
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      return;
    }
    proc.on('message', (msg) => {
      if (this.worker !== proc) return; // 已被 kill/重启：旧线程消息一律丢弃
      this._onWorkerMessage(msg);
    });
    proc.on('error', (err) => {
      if (this.worker !== proc) return;
      this.workerError = 'OCR 线程异常：' + String((err && (err.stack || err)) || err);
      this.inFlight = { A: false, B: false };
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
    });
    proc.on('exit', (code) => {
      if (this.worker !== proc) return;
      this.worker = null;
      this.workerReady = false;
      this.inFlight = { A: false, B: false }; // 线程没了，挂起的帧永远不会回：不清理会永久卡死该框
      // 仅在没有任何先行错误时补兜底文案——别把 error 事件已给出的真实原因覆盖成 code（已踩坑）
      if (this.scanning && !this.workerError) {
        this.workerError = 'OCR 线程退出（code=' + code + '）';
        this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      }
    });
    this.worker = proc;
    proc.postMessage({ type: 'init', modelsDir: this.modelsDir() });
  }

  _killWorker() {
    const proc = this.worker;
    this.worker = null;
    this.workerReady = false;
    this.inFlight = { A: false, B: false };
    if (proc) {
      try {
        proc.terminate();
      } catch (_e) {
        /* 已退出 */
      }
    }
  }

  _onWorkerMessage(msg) {
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'ready') {
      this.workerReady = true;
      this.workerError = '';
      this._pushPageEvent({ type: 'engine', ready: true });
      return;
    }
    if (msg.type === 'error') {
      this.workerError = msg.message || '引擎初始化失败';
      this.workerReady = false;
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      return;
    }
    if (msg.type !== 'ocr-result') return;
    const { epoch, boxId } = decodeOcrId(Number(msg.id) || 0);
    if (epoch !== this.ocrEpoch) return; // 已停止/重启，丢弃旧帧
    this.inFlight[boxId] = false;
    if (!msg.ok) {
      this.workerError = msg.error || 'OCR 失败';
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      return;
    }
    this.workerError = '';
    this.lastOcrMs = msg.elapsedMs || 0;
    if (this.indexDirty) this._rebuildIndex();
    const hit = match.findBest(this.bankIndex, msg.text, this.settings.threshold);
    const prev = this.boxState[boxId];
    const next =
      hit && hit.question
        ? {
            matched: true,
            score: Math.round(hit.score * 100) / 100,
            q: hit.question,
            ocrText: msg.text.slice(0, 200),
          }
        : { matched: false, ocrText: msg.text.slice(0, 200) };
    this.boxState[boxId] = next;
    if (JSON.stringify(prev) !== JSON.stringify(next)) this._pushResult();
  }

  // 真正投递出去才算「已入队」：返回是否成功
  _queueOcr(boxId, bitmap, width, height) {
    if (!this.worker || !this.workerReady || this.inFlight[boxId]) return false;
    const id = encodeOcrId(this.ocrEpoch, this.ocrSeq++, boxId);
    const ab = bitmap.buffer.slice(bitmap.byteOffset, bitmap.byteOffset + bitmap.byteLength);
    this.inFlight[boxId] = true;
    try {
      this.worker.postMessage({ type: 'ocr', id, bitmap: ab, width, height }, [ab]); // transfer 零拷贝
      return true;
    } catch (e) {
      this.inFlight[boxId] = false;
      this.workerError = 'OCR 投递失败：' + String((e && e.message) || e);
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      return false;
    }
  }

  // ---------------------------------------------------------------- 题库与匹配

  _rebuildIndex() {
    this.bankIndex = match.buildIndex(this.store.allEnabledQuestions());
    this.indexDirty = false;
  }

  _bankState() {
    // bankNotice 只承载题库侧自愈提示（索引损坏/题库文件损坏），页面放在题库卡片里；
    // 其它一次性提示走 state().notice，展示在技术诊断区，避免串到题库告警上。
    // 题数取索引元数据求和：state() 每 2 秒被轮询一次，不能每次都把全部题库文件读一遍。
    const banks = this.store.list();
    let questionCount = 0;
    for (const b of banks) if (b.enabled) questionCount += b.count || 0;
    return { banks, questionCount, bankNotice: this.store.notice };
  }

  // ---------------------------------------------------------------- 扫描框窗口

  _overlayUrl(page, query) {
    if (!this.el.app.isPackaged && process.env.ELECTRON_DEV_URL) {
      return { url: process.env.ELECTRON_DEV_URL.replace(/\/$/, '') + '/' + page + query };
    }
    return { file: path.join(__dirname, '..', '..', 'dist', page), query };
  }

  _createBoxWin(boxId) {
    const b = this.settings.boxes[boxId];
    const win = new this.el.BrowserWindow({
      x: b.x,
      y: b.y,
      width: b.w,
      height: b.h,
      minWidth: 160,
      minHeight: 60,
      transparent: true,
      frame: false,
      alwaysOnTop: true,
      skipTaskbar: true,
      resizable: false,
      focusable: false,
      hasShadow: false,
      minimizable: false,
      maximizable: false,
      show: false,
      webPreferences: {
        preload: path.join(this.moduleDir(), 'preload-scanbox.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    const target = this._overlayUrl('scanbox.html', '?box=' + boxId);
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file, { query: { box: boxId } });
    // 首帧加载完再显示：避免先露出半张空白透明框（页面脚本尚未接管指针事件）
    win.webContents.once('did-finish-load', () => {
      if (this.boxWins[boxId] !== win || win.isDestroyed()) return;
      win.showInactive();
    });
    // 只清理自己：本实例已被替换时不要把新窗置空（重启/重建竞态，已踩坑）
    win.on('closed', () => {
      if (this.boxWins[boxId] === win) {
        this.boxWins[boxId] = null;
        if (this._boxDrag && this._boxDrag.boxId === boxId) this._endBoxDrag(true);
      }
    });
    this.boxWins[boxId] = win;
    return win;
  }

  _syncBoxWindows() {
    for (const boxId of ['A', 'B']) {
      const want = this.scanning && !!this.settings.boxes[boxId].visible;
      const win = this.boxWins[boxId];
      const have = !!win && !win.isDestroyed();
      if (want && !have) this._createBoxWin(boxId);
      if (!want && have) {
        this.boxWins[boxId] = null; // 先摘引用再关：closed 回调据此判断是否仍属本实例
        win.close();
      }
    }
  }

  // ---------------------------------------------------------------- 结果窗

  _createResultWin() {
    const saved = this.settings.result;
    // 位置：用户拖过的持久化坐标（越出所在工作区则回退默认右下角）。
    // 上次拖到副屏的结果窗按其保存的矩形找所在显示器，否则会被主屏边界判越界拽回主屏；
    // x/y 仍为 -1 表示「从未拖过」，按主屏默认右下角摆放。
    const neverMoved = saved.x === -1 && saved.y === -1;
    const wa = neverMoved
      ? this._workArea()
      : this._workAreaFor({ x: saved.x, y: saved.y, width: saved.w, height: saved.h });
    // 尺寸：用户拖过的持久化宽高（最小 260×140，不超出所在工作区）——settings 已钳制，这里再兜一次
    const w = Math.min(wa.width, Math.max(260, saved.w));
    const h = Math.min(wa.height, Math.max(140, saved.h));
    // 位置：用户拖过的持久化坐标（越出所在工作区则回退默认右下角）
    let x = wa.x + wa.width - w - 16;
    let y = wa.y + wa.height - h - 16;
    if (
      saved.x >= wa.x &&
      saved.x <= wa.x + wa.width - 40 &&
      saved.y >= wa.y &&
      saved.y <= wa.y + wa.height - 40
    ) {
      x = saved.x;
      y = saved.y;
    }
    const win = new this.el.BrowserWindow({
      x,
      y,
      width: w,
      height: h,
      transparent: true,
      frame: false,
      alwaysOnTop: true,
      skipTaskbar: true,
      resizable: false,
      focusable: false,
      hasShadow: false,
      minimizable: false,
      maximizable: false,
      show: false,
      webPreferences: {
        preload: path.join(this.moduleDir(), 'preload-result.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    win.setOpacity(this.settings.opacity);
    const target = this._overlayUrl('result.html', '');
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file);
    // 首帧：显示 + 重放权威状态（窗口在结果产生后才加载完时不会丢首条答案）
    win.webContents.once('did-finish-load', () => {
      if (this.resultWin !== win || win.isDestroyed()) return;
      win.showInactive();
      this._lastPushed = '';
      this._pushResult();
    });
    win.on('closed', () => {
      if (this.resultWin !== win) return;
      this.resultWin = null;
      this._endResultDrag();
    });
    this.resultWin = win;
    return win;
  }

  _pushResult() {
    if (!this.resultWin || this.resultWin.isDestroyed()) return;
    const payload = {
      boxes: this.boxState,
      ocrMs: this.lastOcrMs,
      visible: { A: !!this.settings.boxes.A.visible, B: !!this.settings.boxes.B.visible },
    };
    const key = JSON.stringify([payload.boxes, payload.visible]);
    if (key === this._lastPushed) return;
    this._lastPushed = key;
    this.resultWin.webContents.send('quizsearch:state', payload);
  }

  // 框拖拽轮询：按 OS 光标与起点的差值应用 移动/缩放（缩放收缩留 3px 余量让光标不越窗界）
  _pollBoxDrag() {
    const d = this._boxDrag;
    if (!d) return;
    const win = this.boxWins[d.boxId];
    if (!win || win.isDestroyed()) {
      this._endBoxDrag(true);
      return;
    }
    if (Date.now() - d.startedAt > DRAG_MAX_MS) {
      this._endBoxDrag(true);
      return;
    } // 兜底防卡死
    const cursor = this.el.screen.getCursorScreenPoint();
    const dx = cursor.x - d.cursor0.x;
    const dy = cursor.y - d.cursor0.y;
    if (d.mode === 'move') {
      win.setPosition(Math.round(d.bounds0.x + dx), Math.round(d.bounds0.y + dy));
    } else {
      let w = Math.max(160, Math.round(d.bounds0.w + dx));
      let h = Math.max(60, Math.round(d.bounds0.h + dy));
      if (w < d.bounds0.w) w += 3;
      if (h < d.bounds0.h) h += 3;
      // Windows 下 setSize 缩小只生效一次、之后静默忽略（Electron 透明窗老坑）；setBounds 可靠
      const cur = win.getBounds();
      win.setBounds({ x: cur.x, y: cur.y, width: w, height: h });
    }
  }

  _endBoxDrag(discard) {
    const d = this._boxDrag;
    if (!d) return;
    clearInterval(d.timer);
    this._boxDrag = null;
    if (discard) return; // 窗口已没了：没有可信几何可落盘
    const win = this.boxWins[d.boxId];
    if (win && !win.isDestroyed()) {
      const b = win.getBounds();
      const wa = this._workAreaFor(b); // 所在显示器，不是主屏
      // 这里只做数值钳制，不走 normalizeSettings：后者只在 y 精确等于 -1 时才当「贴底」
      // 哨兵，多显示器下把框拖到主屏上方（y 为负）的坐标必须原样保留。
      this.settings.boxes[d.boxId] = {
        x: clampInt(b.x, -100000, 100000, this.settings.boxes[d.boxId].x),
        y: clampInt(b.y, -100000, 100000, this.settings.boxes[d.boxId].y),
        w: clampInt(
          b.width,
          BOX_MIN_W,
          Math.max(BOX_MIN_W, wa.width),
          this.settings.boxes[d.boxId].w
        ),
        h: clampInt(
          b.height,
          BOX_MIN_H,
          Math.max(BOX_MIN_H, wa.height),
          this.settings.boxes[d.boxId].h
        ),
        visible: this.settings.boxes[d.boxId].visible,
      };
    }
    this._saveSettings();
  }

  _endResultDrag() {
    const d = this._resultDrag;
    if (!d) return;
    clearInterval(d.timer);
    this._resultDrag = null;
    if (this.resultWin && !this.resultWin.isDestroyed()) {
      const b = this.resultWin.getBounds();
      const wa = this._workAreaFor(b); // 结果窗可能被拖到副屏：按其所在显示器钳制
      this.settings.result = normalizeSettings(
        { result: { x: b.x, y: b.y, w: b.width, h: b.height } },
        { workArea: wa }
      ).result;
    }
    this._saveSettings();
  }

  // ---------------------------------------------------------------- 启停与轮询

  start() {
    if (this.scanning) return this.state();
    if (this.store.list().length === 0) throw new Error('请先导入题库');
    if (this.store.allEnabledQuestions().length === 0) throw new Error('请先启用至少一个题库');
    this.scanning = true;
    this.ocrEpoch++;
    this.ocrSeq = 0;
    this.inFlight = { A: false, B: false };
    this.lastHash = { A: 0, B: 0 };
    this.boxState = { A: null, B: null };
    this.lastTickError = '';
    this._lastPushed = '';
    this._forkWorker();
    this._syncBoxWindows();
    if (!this.resultWin || this.resultWin.isDestroyed()) this._createResultWin();
    this.timer = setInterval(() => {
      this._tick().catch(() => {});
    }, this.settings.interval);
    const main = this.opts.getMainWindow && this.opts.getMainWindow();
    if (main && !main.isDestroyed()) main.minimize(); // 同原软件：开搜即最小化主窗
    if (this.opts.onScanStateChange) this.opts.onScanStateChange(true);
    this._pushPageEvent({ type: 'scan', scanning: true });
    return this.state();
  }

  stop() {
    if (!this.scanning) return this.state();
    this.scanning = false;
    this.ocrEpoch++;
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
    this._tickBusy = false;
    this._killWorker();
    this._endBoxDrag(true); // 结束中的拖拽不应在关窗后再落盘
    this._syncBoxWindows();
    if (this.resultWin && !this.resultWin.isDestroyed()) {
      const win = this.resultWin;
      this.resultWin = null;
      win.close();
    }
    this.resultWin = null;
    if (this.opts.onScanStateChange) this.opts.onScanStateChange(false);
    this._pushPageEvent({ type: 'scan', scanning: false });
    return this.state();
  }

  // 截取指定框当前覆盖的屏幕区域（shotCache：同帧同显示器复用整屏截图；传 null 则强制新抓）
  async _captureBox(boxId, shotCache) {
    const win = this.boxWins[boxId];
    if (!win || win.isDestroyed()) return null;
    const bounds = win.getBounds(); // DIP
    const display = this.el.screen.getDisplayMatching(bounds);
    const scale = display.scaleFactor;
    let physW = Math.round(display.size.width * scale);
    let physH = Math.round(display.size.height * scale);
    // 超大屏/高倍率下缩略图请求封顶（部分平台超额直接回空——已踩坑）；kx/ky 按实际尺寸自适应
    if (physW > MAX_THUMB || physH > MAX_THUMB) {
      const k = MAX_THUMB / Math.max(physW, physH);
      physW = Math.round(physW * k);
      physH = Math.round(physH * k);
    }
    const cache = shotCache || new Map();
    if (!cache.has(display.id)) {
      const sources = await this.el.desktopCapturer.getSources({
        types: ['screen'],
        thumbnailSize: { width: physW, height: physH },
      });
      const displays = this.el.screen.getAllDisplays();
      const picked = pickSource(sources, display, displays.length);
      if (!picked.source) {
        cache.set(display.id, null);
        this.lastTickError = '框' + boxId + ' 截屏失败：' + picked.reason;
        return null;
      }
      cache.set(display.id, picked.source.thumbnail || null);
    }
    const shot = cache.get(display.id);
    if (!shot || shot.isEmpty()) return null;
    // 缩略图实际尺寸可能与请求略有出入；换算「缩略图像素 / 物理像素」比例（物理像素 = DIP × scaleFactor）
    const tw = shot.getSize().width;
    const th = shot.getSize().height;
    if (!tw || !th) return null;
    const kx = tw / (display.size.width * scale);
    const ky = th / (display.size.height * scale);
    const rx = Math.max(0, Math.round((bounds.x - display.bounds.x) * scale * kx));
    const ry = Math.max(0, Math.round((bounds.y - display.bounds.y) * scale * ky));
    const rw = Math.min(tw - rx, Math.round(bounds.width * scale * kx));
    const rh = Math.min(th - ry, Math.round(bounds.height * scale * ky));
    if (rw < 16 || rh < 16) return null;
    // 不用 crop.getBitmap()：裁剪图的缓冲不一定是稠密 w×h×4（实测约为 3 倍——行距继承原图，已踩坑），
    // 整屏缩略图的 getBitmap 是稠密的，按行手工裁出稠密 BGRA
    const full = shot.getBitmap();
    const stride = tw * 4;
    const bitmap = Buffer.allocUnsafe(rw * rh * 4);
    for (let y = 0; y < rh; y++) {
      const srcStart = (ry + y) * stride + rx * 4;
      full.copy(bitmap, y * rw * 4, srcStart, srcStart + rw * 4);
    }
    const cropped = this.el.nativeImage.createFromBuffer(bitmap, { width: rw, height: rh });
    return { cropped, bitmap, width: rw, height: rh };
  }

  async _tick() {
    // 忙门闩：截屏（desktopCapturer.getSources）在弱机/大屏上可能超过扫描间隔，
    // 未完的拍不重入，防并发抓帧互相拖慢甚至取到空画面
    if (!this.scanning || this._tickBusy) return;
    this._tickBusy = true;
    try {
      const active = ['A', 'B'].filter((id) => {
        const win = this.boxWins[id];
        return this.settings.boxes[id].visible && win && !win.isDestroyed();
      });
      if (active.length === 0) return;
      // 每个涉及显示器每帧只抓一次
      const shotCache = new Map();
      for (const boxId of active) {
        try {
          const cap = await this._captureBox(boxId, shotCache);
          if (!this.scanning) return; // 截屏期间被停止：不再投递
          if (!cap) {
            if (!this.lastTickError)
              this.lastTickError = '框' + boxId + ' 未取到屏幕画面（截屏为空）';
            continue;
          }
          const hash = frameHash(cap.bitmap);
          if (hash === this.lastHash[boxId]) continue; // 画面未变
          // 哈希只在真正投递成功后记录：引擎未就绪/该框上一帧仍在识别时不吞掉这一帧，
          // 否则画面停住后再也不会有新帧进来（hash 已写、后续每拍都被判「未变」）
          if (this._queueOcr(boxId, cap.bitmap, cap.width, cap.height)) {
            this.lastHash[boxId] = hash;
            this.lastTickError = '';
          }
        } catch (e) {
          this.lastTickError = '框' + boxId + ' 截屏异常：' + String((e && e.message) || e);
        }
      }
    } finally {
      this._tickBusy = false;
    }
  }

  // ---------------------------------------------------------------- 状态与事件

  state() {
    const boxDbg = {};
    for (const id of ['A', 'B']) {
      const st = this.boxState[id];
      boxDbg[id] = st ? { matched: !!st.matched, snippet: (st.ocrText || '').slice(0, 60) } : null;
    }
    return Object.assign(
      {
        scanning: this.scanning,
        engineReady: this.workerReady,
        engineError: this.workerError,
        captureError: this.lastTickError || '',
        sessionType: process.env.XDG_SESSION_TYPE || '', // wayland 截屏不可用（需 X11 会话）
        ocrMs: this.lastOcrMs,
        boxDebug: boxDbg, // 各框最近 OCR 摘要（诊断用）
        notice: this.notice, // 设置保存失败等一次性提示（技术诊断区展示）
        settings: this.settings,
      },
      this._bankState()
    );
  }

  _pushPageEvent(payload) {
    const main = this.opts.getMainWindow && this.opts.getMainWindow();
    if (main && !main.isDestroyed()) main.webContents.send('quizsearch:event', payload);
  }

  selftest() {
    const dir = this.modelsDir();
    const files = ['det.onnx', 'rec.onnx', 'cls.onnx', 'ppocr_keys.txt'];
    const missing = files.filter((f) => !fs.existsSync(path.join(dir, f)));
    // worker 目标脚本须为真实文件路径（asarUnpack 落点），打包路径回归靠此项暴露
    const workerScript = path.join(this.moduleDir(), 'ocr-worker.cjs');
    if (!fs.existsSync(workerScript)) missing.push('ocr-worker.cjs');
    let ort = false;
    let ortVersion = '';
    try {
      const o = require('onnxruntime-node');
      ort = !!o;
      ortVersion = (o.env && o.env.versions && o.env.versions.node) || '';
    } catch (_e) {
      ort = false;
    }
    // CPU 指令集体检（linux x64）：onnxruntime 预编译内核在无 AVX2 的老 CPU 上会在推理时崩（SIGILL）
    let avx2 = null;
    if (process.platform === 'linux') {
      try {
        avx2 = /(^|\s)avx2(\s|$)/m.test(fs.readFileSync('/proc/cpuinfo', 'utf8'));
      } catch (_e) {
        avx2 = null;
      }
    }
    return {
      ok: missing.length === 0 && ort,
      modelsDir: dir,
      missing,
      ortLoaded: ort,
      ortVersion,
      avx2,
    };
  }

  // 诊断截图目录：程序自管的临时目录（不写用户「下载」目录，不污染用户可见位置）
  debugDir() {
    return path.join(os.tmpdir(), 'shade-quiz-debug');
  }

  // ---------------------------------------------------------------- IPC

  // 统一包装：同步/异步异常都转成带前缀的中文错误回传渲染层（IPC 直接抛栈对用户无意义）
  _handle(channel, fn) {
    this.el.ipcMain.handle(channel, async (e, ...args) => {
      try {
        return await fn(e, ...args);
      } catch (err) {
        const msg = String((err && err.message) || err || '未知错误');
        throw new Error(msg);
      }
    });
  }

  registerIpc() {
    this._handle('quizSearch:getState', () => this.state());
    this._handle('quizSearch:start', () => this.start());
    this._handle('quizSearch:stop', () => this.stop());
    this._handle('quizSearch:selftest', () => this.selftest());

    // 诊断：把扫描框当前所见画面存成 PNG（排查「截屏黑/空」「OCR 失败」「匹配未中」用）
    this._handle('quizSearch:debugCapture', async () => {
      const boxId = this.boxWins.A ? 'A' : this.boxWins.B ? 'B' : null;
      if (!boxId) throw new Error('扫描框未开（先开始搜题）');
      const cap = await this._captureBox(boxId, null);
      if (!cap) throw new Error('截屏失败（未取到屏幕画面）');
      const dir = this.debugDir();
      fs.mkdirSync(dir, { recursive: true });
      const file = path.join(dir, `题图调试_框${boxId}_${Date.now()}.png`);
      fs.writeFileSync(file, cap.cropped.toPNG());
      // 提示由渲染层用 MessagePlugin 直接展示（返回体里带 dir），不占用 state().notice
      return { file, dir, notice: '诊断截图已保存到临时目录：' + dir };
    });

    this._handle('quizSearch:setBoxVisible', (_e, boxId, visible) => {
      if (!this.settings.boxes[boxId]) throw new Error('未知扫描框：' + String(boxId));
      this.settings.boxes[boxId].visible = !!visible;
      this._saveSettings();
      this._syncBoxWindows();
      this._pushResult(); // 单/双框布局即时切换
      return this.state();
    });
    this._handle('quizSearch:resetBoxes', () => {
      for (const id of ['A', 'B']) {
        // 每个框按其当前所在显示器计算默认落点：副屏上的框不该被重置回主屏。
        // 以零原点工作区取默认几何（相对工作区左上角），再平移到该工作区原点。
        const win = this.boxWins[id];
        const live = win && !win.isDestroyed() ? win.getBounds() : null;
        const wa = this._workAreaFor(live);
        const b = normalizeSettings(null, {
          workArea: { x: 0, y: 0, width: wa.width, height: wa.height },
        }).boxes[id];
        const x = wa.x + b.x;
        const y = wa.y + b.y;
        this.settings.boxes[id] = Object.assign({}, b, {
          x,
          y,
          visible: this.settings.boxes[id].visible,
        });
        if (live) win.setBounds({ x, y, width: b.w, height: b.h });
      }
      this._saveSettings();
      return this.state();
    });
    this._handle('quizSearch:setOptions', (_e, opts) => {
      const o = opts && typeof opts === 'object' ? opts : {};
      // 非法值保留当前设置，不跳回默认（滑块拖出脏数据不该把间隔重置）
      if (o.interval !== undefined)
        this.settings.interval = clampInt(o.interval, 500, 3000, this.settings.interval);
      if (o.threshold !== undefined)
        this.settings.threshold = clampNumber(o.threshold, 0.3, 0.9, this.settings.threshold);
      if (o.opacity !== undefined) {
        this.settings.opacity = clampNumber(o.opacity, 0.3, 1, this.settings.opacity);
        if (this.resultWin && !this.resultWin.isDestroyed())
          this.resultWin.setOpacity(this.settings.opacity);
      }
      this._saveSettings();
      if (this.scanning) {
        // 间隔变更即时生效
        if (this.timer) clearInterval(this.timer);
        this.timer = setInterval(() => {
          this._tick().catch(() => {});
        }, this.settings.interval);
      }
      return this.state();
    });

    // 题库导入：rows 由渲染层解析（xlsx 走 read-excel-file，老 .xls 走内置读取器，
    // 两者都由 Vite 打进渲染层包；主进程侧不引 Excel 解析器，安装包才不缺依赖）。
    this._handle('quizSearch:bankImport', (_e, payload) => {
      const p = payload && typeof payload === 'object' ? payload : {};
      if (!Array.isArray(p.rows)) throw new Error('导入数据缺失（应由页面解析后传入题目行）');
      const r = this.store.importRows(p.name, p.sourceFile, p.rows);
      this.indexDirty = true;
      return Object.assign({ ok: true }, r, this._bankState());
    });
    this._handle('quizSearch:bankRemove', (_e, id) => {
      this.store.remove(id);
      this.indexDirty = true;
      return this._bankState();
    });
    this._handle('quizSearch:bankRename', (_e, id, name) => {
      this.store.rename(id, name);
      return this._bankState();
    });
    this._handle('quizSearch:bankSetEnabled', (_e, id, enabled) => {
      this.store.setEnabled(id, enabled);
      this.indexDirty = true;
      return this._bankState();
    });
    this._handle('quizSearch:testMatch', (_e, text) => {
      if (this.indexDirty) this._rebuildIndex();
      const hit = match.findBest(this.bankIndex, text, this.settings.threshold);
      if (!hit) return { matched: false };
      return {
        matched: !!hit.question,
        score: hit.score,
        question: hit.question || null,
        near: hit.near || null,
        norm: hit.norm,
      };
    });
    // 扫描框渲染进程 → 主进程：拖动/缩放/隐藏。
    // 位移由主进程轮询 OS 光标（screen.getCursorScreenPoint，Windows/Linux 均可靠）得出——
    // 渲染层 screenX/movementX 在悬浮窗上不可靠（合成输入恒为 0，已踩坑）；
    // 窗口跟手后光标始终留在窗内，pointerup 不依赖 pointer capture（Linux 断流，已踩坑）。
    this.el.ipcMain.on('quizsearch:boxDragStart', (_e, boxId, mode) => {
      const win = this.boxWins[boxId];
      if (!win || win.isDestroyed()) return;
      this._endBoxDrag(); // 防御：清掉可能残留的旧拖拽（同时落盘其几何）
      const b = win.getBounds();
      const cursor = this.el.screen.getCursorScreenPoint();
      this._boxDrag = {
        boxId,
        mode: mode === 'resize' ? 'resize' : 'move',
        cursor0: cursor,
        bounds0: { x: b.x, y: b.y, w: b.width, h: b.height },
        startedAt: Date.now(),
        timer: setInterval(() => this._pollBoxDrag(), 16),
      };
    });
    this.el.ipcMain.on('quizsearch:boxDragEnd', () => this._endBoxDrag());
    this.el.ipcMain.on('quizsearch:boxHide', (_e, boxId) => {
      if (!this.settings.boxes[boxId]) return;
      this._endBoxDrag(true); // 隐藏中的拖拽不应把几何写回
      this.settings.boxes[boxId].visible = false;
      this._saveSettings();
      this._syncBoxWindows();
      this._pushResult(); // 单/双框布局即时切换
      this._pushPageEvent({ type: 'boxHidden', box: boxId });
    });
    // 结果窗拖动/缩放（用户可自由摆放与调整大小；轮询 OS 光标位移，dragEnd 时落盘，下次开搜恢复）
    this.el.ipcMain.on('quizsearch:resultDragStart', (_e, mode) => {
      if (!this.resultWin || this.resultWin.isDestroyed()) return;
      this._endResultDrag(); // 防御：清掉可能残留的旧拖拽
      const b = this.resultWin.getBounds();
      this._resultDrag = {
        mode: mode === 'resize' ? 'resize' : 'move',
        cursor0: this.el.screen.getCursorScreenPoint(),
        bounds0: { x: b.x, y: b.y, w: b.width, h: b.height },
        startedAt: Date.now(),
        timer: setInterval(() => this._pollResultDrag(), 16),
      };
    });
    this.el.ipcMain.on('quizsearch:resultDragEnd', () => this._endResultDrag());
  }

  _pollResultDrag() {
    const d = this._resultDrag;
    if (!d) return;
    if (!this.resultWin || this.resultWin.isDestroyed()) {
      this._endResultDrag();
      return;
    }
    if (Date.now() - d.startedAt > DRAG_MAX_MS) {
      this._endResultDrag();
      return;
    }
    const c = this.el.screen.getCursorScreenPoint();
    const dx = c.x - d.cursor0.x;
    const dy = c.y - d.cursor0.y;
    if (d.mode === 'move') {
      this.resultWin.setPosition(Math.round(d.bounds0.x + dx), Math.round(d.bounds0.y + dy));
      return;
    }
    let w = Math.max(260, Math.round(d.bounds0.w + dx));
    let h = Math.max(140, Math.round(d.bounds0.h + dy));
    if (w < d.bounds0.w) w += 3;
    if (h < d.bounds0.h) h += 3;
    // Windows 下 setSize 缩小只生效一次（透明窗老坑）；setBounds 可靠
    const cur = this.resultWin.getBounds();
    this.resultWin.setBounds({ x: cur.x, y: cur.y, width: w, height: h });
  }

  dispose() {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.scanning = false;
    if (this._boxDrag) clearInterval(this._boxDrag.timer);
    if (this._resultDrag) clearInterval(this._resultDrag.timer);
    this._boxDrag = null;
    this._resultDrag = null;
    this._killWorker();
    for (const boxId of ['A', 'B']) {
      const win = this.boxWins[boxId];
      this.boxWins[boxId] = null;
      if (win && !win.isDestroyed()) win.destroy();
    }
    if (this.resultWin && !this.resultWin.isDestroyed()) this.resultWin.destroy();
    this.resultWin = null;
  }
}

module.exports = { QuizSearchService, DEFAULT_SETTINGS, frameHash, encodeOcrId, decodeOcrId };
