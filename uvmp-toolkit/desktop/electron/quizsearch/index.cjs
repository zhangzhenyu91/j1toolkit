// 题库搜题：壳层主服务（Electron 主进程内运行）。
// 职责：扫描框/结果窗生命周期、截屏轮询、OCR 子进程编排、题库匹配、设置与题库持久化、IPC。
// 设计要点：
//   - 双扫描框（A/B）独立拖动/缩放/显隐，几何持久化；两框各自 OCR 各自匹配，结果窗分区展示；
//   - 截屏用 desktopCapturer 整屏帧按框裁剪（按所在显示器 scaleFactor 换算物理像素）；
//   - 逐帧哈希，画面未变跳过 OCR，弱机减负；
//   - OCR 在 utilityProcess 子进程（ocr-worker.cjs），模型常驻内存，主进程不阻塞；
//   - 题库 JSON 落盘 userData/quiz-search/（banks-index.json + banks/<id>.json），不依赖 Excel 运行时。
'use strict';

const fs = require('fs');
const path = require('path');
const { app, BrowserWindow, desktopCapturer, screen, ipcMain, nativeImage } = require('electron');
const { BankStore } = require('./bank-store.cjs');
const match = require('./match.cjs');

const DEFAULT_SETTINGS = {
  interval: 1000,      // 扫描间隔 ms（500~3000）
  threshold: 0.6,      // 匹配命中阈值（0.3~0.9）
  opacity: 0.9,        // 结果窗不透明度
  result: { x: -1, y: -1, w: 380, h: 260 },   // 结果窗位置与尺寸（x/y=-1 = 默认右下角，用户拖动/缩放后落盘）
  boxes: {
    A: { x: 80, y: 100, w: 720, h: 180, visible: true },
    B: { x: 80, y: -1, w: 720, h: 180, visible: true },   // y=-1：初始化时贴屏幕底部
  },
};

function deepMerge(base, extra) {
  const out = JSON.parse(JSON.stringify(base));
  if (!extra || typeof extra !== 'object') return out;
  for (const [k, v] of Object.entries(extra)) {
    if (v && typeof v === 'object' && !Array.isArray(v) && out[k] && typeof out[k] === 'object') {
      out[k] = deepMerge(out[k], v);
    } else if (v !== undefined) {
      out[k] = v;
    }
  }
  return out;
}

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

class QuizSearchService {
  /**
   * @param opts.getMainWindow  取主窗口（托盘/页面向主窗口推事件用）
   * @param opts.onScanStateChange  搜题启停回调（main.cjs 刷新托盘菜单）
   */
  constructor(opts) {
    this.opts = opts || {};
    this.dataDir = path.join(app.getPath('userData'), 'quiz-search');
    fs.mkdirSync(this.dataDir, { recursive: true });
    this.settingsPath = path.join(this.dataDir, 'settings.json');
    this.settings = this._loadSettings();
    this.store = new BankStore(this.dataDir);

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
    this.ocrEpoch = 0;            // 停止/重启时作废旧帧结果
    this.bankIndex = [];
    this.indexDirty = true;
    this.boxState = { A: null, B: null };   // 各框最新匹配（结果窗数据源）
    this._lastPushed = '';
  }

  // ---------------------------------------------------------------- 设置

  _loadSettings() {
    let saved = null;
    try { saved = JSON.parse(fs.readFileSync(this.settingsPath, 'utf8')); } catch (_e) { /* 首次运行 */ }
    const s = deepMerge(DEFAULT_SETTINGS, saved);
    if (s.boxes.B.y < 0) {
      const wa = screen.getPrimaryDisplay().workArea;
      s.boxes.B.y = wa.y + wa.height - s.boxes.B.h - 120;
    }
    return s;
  }

  _saveSettings() {
    try { fs.writeFileSync(this.settingsPath, JSON.stringify(this.settings, null, 2), 'utf8'); } catch (_e) { /* 忽略 */ }
  }

  // ---------------------------------------------------------------- 模型/子进程

  // quizsearch 目录整体被 asarUnpack（模型/脚本都需真实文件路径）：
  // 打包后 __dirname 指向 asar 内，须显式换到 app.asar.unpacked（utilityProcess.fork 读不了 asar）
  moduleDir() {
    return app.isPackaged
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
    const { Worker } = require('worker_threads');
    let proc;
    try {
      proc = new Worker(path.join(this.moduleDir(), 'ocr-worker.cjs'), {});
    } catch (e) {
      this.workerError = 'OCR 线程创建失败：' + (e && (e.stack || e));
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
      return;
    }
    proc.on('message', (msg) => this._onWorkerMessage(msg));
    proc.on('error', (err) => {
      this.workerError = 'OCR 线程异常：' + String((err && (err.stack || err)) || err);
      this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
    });
    proc.on('exit', (code) => {
      if (this.worker === proc) {
        this.worker = null;
        this.workerReady = false;
        this.inFlight = { A: false, B: false };
        // 仅在没有任何先行错误时补兜底文案——别把 error 事件已给出的真实原因覆盖成 code（已踩坑）
        if (this.scanning && !this.workerError) {
          this.workerError = 'OCR 线程退出（code=' + code + '）';
          this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError });
        }
      }
    });
    this.worker = proc;
    proc.postMessage({ type: 'init', modelsDir: this.modelsDir() });
  }

  _killWorker() {
    if (this.worker) {
      const proc = this.worker;
      this.worker = null;
      this.workerReady = false;
      try { proc.terminate(); } catch (_e) { /* 已退出 */ }
    }
  }

  _onWorkerMessage(msg) {
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'ready') { this.workerReady = true; this._pushPageEvent({ type: 'engine', ready: true }); return; }
    if (msg.type === 'error') { this.workerError = msg.message || '引擎初始化失败'; this._pushPageEvent({ type: 'engine', ready: false, error: this.workerError }); return; }
    if (msg.type !== 'ocr-result') return;
    const epoch = (msg.id / 1000000) | 0;
    if (epoch !== this.ocrEpoch) return;         // 已停止/重启，丢弃旧帧
    const boxId = msg.id % 2 === 0 ? 'A' : 'B';  // id 低位奇偶承载框别（见 _queueOcr）
    this.inFlight[boxId] = false;
    if (!msg.ok) { this.workerError = msg.error || 'OCR 失败'; return; }
    this.lastOcrMs = msg.elapsedMs || 0;
    if (this.indexDirty) this._rebuildIndex();
    const hit = match.findBest(this.bankIndex, msg.text, this.settings.threshold);
    const prev = this.boxState[boxId];
    const next = hit && hit.question
      ? { matched: true, score: Math.round(hit.score * 100) / 100, q: hit.question, ocrText: msg.text.slice(0, 200) }
      : { matched: false, ocrText: msg.text.slice(0, 200) };
    this.boxState[boxId] = next;
    if (JSON.stringify(prev) !== JSON.stringify(next)) this._pushResult();
  }

  _queueOcr(boxId, bitmap, width, height) {
    if (!this.worker || !this.workerReady || this.inFlight[boxId]) return;
    this.inFlight[boxId] = true;
    const id = this.ocrEpoch * 1000000 + (this.ocrSeq++ % 500000) * 2 + (boxId === 'A' ? 0 : 1);
    const ab = bitmap.buffer.slice(bitmap.byteOffset, bitmap.byteOffset + bitmap.byteLength);
    this.worker.postMessage({ type: 'ocr', id, bitmap: ab, width, height }, [ab]);   // transfer 零拷贝
  }

  // ---------------------------------------------------------------- 题库与匹配

  _rebuildIndex() {
    this.bankIndex = match.buildIndex(this.store.allEnabledQuestions());
    this.indexDirty = false;
  }

  _bankState() {
    return { banks: this.store.list(), questionCount: this.store.allEnabledQuestions().length };
  }

  // ---------------------------------------------------------------- 扫描框窗口

  _overlayUrl(page, query) {
    if (!app.isPackaged && process.env.ELECTRON_DEV_URL) {
      return { url: process.env.ELECTRON_DEV_URL.replace(/\/$/, '') + '/' + page + query };
    }
    return { file: path.join(__dirname, '..', '..', 'dist', page), query };
  }

  _createBoxWin(boxId) {
    const b = this.settings.boxes[boxId];
    const win = new BrowserWindow({
      x: b.x, y: b.y, width: b.w, height: b.h,
      minWidth: 160, minHeight: 60,
      transparent: true, frame: false, alwaysOnTop: true, skipTaskbar: true,
      resizable: false, focusable: false, hasShadow: false, minimizable: false, maximizable: false,
      webPreferences: {
        preload: path.join(this.moduleDir(), 'preload-scanbox.cjs'),
        contextIsolation: true, nodeIntegration: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    const target = this._overlayUrl('scanbox.html', '?box=' + boxId);
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file, { query: { box: boxId } });
    win.on('closed', () => { this.boxWins[boxId] = null; });
    this.boxWins[boxId] = win;
    return win;
  }

  _syncBoxWindows() {
    for (const boxId of ['A', 'B']) {
      const want = this.scanning && this.settings.boxes[boxId].visible;
      const have = !!this.boxWins[boxId];
      if (want && !have) this._createBoxWin(boxId);
      if (!want && have) { this.boxWins[boxId].close(); this.boxWins[boxId] = null; }
    }
  }

  // ---------------------------------------------------------------- 结果窗

  _createResultWin() {
    const wa = screen.getPrimaryDisplay().workArea;
    const saved = this.settings.result || { x: -1, y: -1 };
    // 尺寸：用户拖过的持久化宽高（最小 260×140，不超出当前工作区）
    const w = Math.min(wa.width, Math.max(260, saved.w | 0 || 380));
    const h = Math.min(wa.height, Math.max(140, saved.h | 0 || 260));
    // 位置：用户拖过的持久化坐标（越出当前工作区则回退默认右下角）
    let x = wa.x + wa.width - w - 16;
    let y = wa.y + wa.height - h - 16;
    if (saved.x >= wa.x && saved.x <= wa.x + wa.width - 40
        && saved.y >= wa.y && saved.y <= wa.y + wa.height - 40) {
      x = saved.x;
      y = saved.y;
    }
    const win = new BrowserWindow({
      x, y, width: w, height: h,
      transparent: true, frame: false, alwaysOnTop: true, skipTaskbar: true,
      resizable: false, focusable: false, hasShadow: false, minimizable: false, maximizable: false,
      webPreferences: {
        preload: path.join(this.moduleDir(), 'preload-result.cjs'),
        contextIsolation: true, nodeIntegration: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    win.setOpacity(this.settings.opacity);
    const target = this._overlayUrl('result.html', '');
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file);
    win.on('closed', () => { this.resultWin = null; });
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
    if (!win || win.isDestroyed()) { this._endBoxDrag(); return; }
    if (Date.now() - d.startedAt > 5 * 60 * 1000) { this._endBoxDrag(); return; }   // 兜底防卡死
    const cursor = screen.getCursorScreenPoint();
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

  _endBoxDrag() {
    const d = this._boxDrag;
    if (!d) return;
    clearInterval(d.timer);
    this._boxDrag = null;
    const win = this.boxWins[d.boxId];
    if (win && !win.isDestroyed()) {
      const b = win.getBounds();
      this.settings.boxes[d.boxId] = Object.assign(this.settings.boxes[d.boxId], { x: b.x, y: b.y, w: b.width, h: b.height });
    }
    this._saveSettings();
  }

  // ---------------------------------------------------------------- 启停与轮询

  start() {
    if (this.scanning) return this.state();
    if (this.store.list().length === 0) throw new Error('请先导入题库');
    this.scanning = true;
    this.ocrEpoch++;
    this.lastHash = { A: 0, B: 0 };
    this.boxState = { A: null, B: null };
    this._lastPushed = '';
    this._forkWorker();
    this._syncBoxWindows();
    if (!this.resultWin) this._createResultWin();
    this.timer = setInterval(() => { this._tick().catch(() => {}); }, this.settings.interval);
    const main = this.opts.getMainWindow && this.opts.getMainWindow();
    if (main && !main.isDestroyed()) main.minimize();   // 同原软件：开搜即最小化主窗
    if (this.opts.onScanStateChange) this.opts.onScanStateChange(true);
    this._pushPageEvent({ type: 'scan', scanning: true });
    return this.state();
  }

  stop() {
    if (!this.scanning) return this.state();
    this.scanning = false;
    this.ocrEpoch++;
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
    this._killWorker();
    this._syncBoxWindows();
    if (this.resultWin && !this.resultWin.isDestroyed()) this.resultWin.close();
    this.resultWin = null;
    if (this.opts.onScanStateChange) this.opts.onScanStateChange(false);
    this._pushPageEvent({ type: 'scan', scanning: false });
    return this.state();
  }

  // 截取指定框当前覆盖的屏幕区域（shotCache：同帧同显示器复用整屏截图；传 null 则强制新抓）
  async _captureBox(boxId, shotCache) {
    const win = this.boxWins[boxId];
    if (!win || win.isDestroyed()) return null;
    const bounds = win.getBounds();   // DIP
    const display = screen.getDisplayMatching(bounds);
    const scale = display.scaleFactor;
    let physW = Math.round(display.size.width * scale);
    let physH = Math.round(display.size.height * scale);
    // 超大屏/高倍率下缩略图请求封顶（部分平台超额直接回空——已踩坑）；kx/ky 按实际尺寸自适应
    const MAX_THUMB = 4096;
    if (physW > MAX_THUMB || physH > MAX_THUMB) {
      const k = MAX_THUMB / Math.max(physW, physH);
      physW = Math.round(physW * k);
      physH = Math.round(physH * k);
    }
    if (shotCache && shotCache.has(display.id)) {
      // 已缓存
    } else {
      const sources = await desktopCapturer.getSources({ types: ['screen'], thumbnailSize: { width: physW, height: physH } });
      const src = sources.find((s) => String(s.display_id) === String(display.id)) || sources[0];
      if (!shotCache) shotCache = new Map();
      shotCache.set(display.id, src ? src.thumbnail : null);
    }
    const shot = shotCache.get(display.id);
    if (!shot || shot.isEmpty()) return null;
    // 缩略图实际尺寸可能与请求略有出入；换算「缩略图像素 / 物理像素」比例（物理像素 = DIP × scaleFactor）
    const tw = shot.getSize().width;
    const th = shot.getSize().height;
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
    const cropped = nativeImage.createFromBuffer(bitmap, { width: rw, height: rh });
    return { cropped, bitmap, width: rw, height: rh };
  }

  async _tick() {
    if (!this.scanning) return;
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
        if (!cap) {
          this.lastTickError = '框' + boxId + ' 未取到屏幕画面（截屏为空）';
          continue;
        }
        this.lastTickError = '';
        const hash = frameHash(cap.bitmap);
        if (hash === this.lastHash[boxId]) continue;   // 画面未变
        this.lastHash[boxId] = hash;
        this._queueOcr(boxId, cap.bitmap, cap.width, cap.height);
      } catch (e) {
        this.lastTickError = '框' + boxId + ' 截屏异常：' + String((e && e.message) || e);
      }
    }
  }

  // ---------------------------------------------------------------- 状态与事件

  state() {
    const boxDbg = {};
    for (const id of ['A', 'B']) {
      const st = this.boxState[id];
      boxDbg[id] = st
        ? { matched: !!st.matched, snippet: (st.ocrText || '').slice(0, 60) }
        : null;
    }
    return Object.assign({
      scanning: this.scanning,
      engineReady: this.workerReady,
      engineError: this.workerError,
      captureError: this.lastTickError || '',
      sessionType: process.env.XDG_SESSION_TYPE || '',   // wayland 截屏不可用（需 X11 会话）
      ocrMs: this.lastOcrMs,
      boxDebug: boxDbg,          // 各框最近 OCR 摘要（诊断用）
      settings: this.settings,
    }, this._bankState());
  }

  _pushPageEvent(payload) {
    const main = this.opts.getMainWindow && this.opts.getMainWindow();
    if (main && !main.isDestroyed()) main.webContents.send('quizsearch:event', payload);
  }

  selftest() {
    const dir = this.modelsDir();
    const files = ['det.onnx', 'rec.onnx', 'cls.onnx', 'ppocr_keys.txt'];
    const missing = files.filter((f) => !fs.existsSync(path.join(dir, f)));
    // utilityProcess fork 目标脚本须为真实文件路径（asarUnpack 落点），打包路径回归靠此项暴露
    const workerScript = path.join(this.moduleDir(), 'ocr-worker.cjs');
    if (!fs.existsSync(workerScript)) missing.push('ocr-worker.cjs');
    let ort = false;
    let ortVersion = '';
    try {
      const o = require('onnxruntime-node');
      ort = !!o;
      ortVersion = (o.env && o.env.versions && o.env.versions.node) || '';
    } catch (_e) { ort = false; }
    // CPU 指令集体检（linux x64）：onnxruntime 预编译内核在无 AVX2 的老 CPU 上会在推理时崩（SIGILL）
    let avx2 = null;
    if (process.platform === 'linux') {
      try { avx2 = /(^|\s)avx2(\s|$)/m.test(fs.readFileSync('/proc/cpuinfo', 'utf8')); } catch (_e) { avx2 = null; }
    }
    return { ok: missing.length === 0 && ort, modelsDir: dir, missing, ortLoaded: ort, ortVersion, avx2 };
  }

  // ---------------------------------------------------------------- IPC

  registerIpc() {
    ipcMain.handle('quizSearch:getState', () => this.state());
    ipcMain.handle('quizSearch:start', () => this.start());
    ipcMain.handle('quizSearch:stop', () => this.stop());
    ipcMain.handle('quizSearch:selftest', () => this.selftest());

    // 诊断：把扫描框当前所见画面存成 PNG（排查「截屏黑/空」「OCR 失败」「匹配未中」用）
    ipcMain.handle('quizSearch:debugCapture', async () => {
      const boxId = this.boxWins.A ? 'A' : (this.boxWins.B ? 'B' : null);
      if (!boxId) throw new Error('扫描框未开（先开始搜题）');
      const cap = await this._captureBox(boxId, null);
      if (!cap) throw new Error('截屏失败（未取到屏幕画面）');
      const file = path.join(app.getPath('downloads'), `题图调试_框${boxId}_${Date.now()}.png`);
      fs.writeFileSync(file, cap.cropped.toPNG());
      return file;
    });

    ipcMain.handle('quizSearch:setBoxVisible', (_e, boxId, visible) => {
      if (!this.settings.boxes[boxId]) return this.state();
      this.settings.boxes[boxId].visible = !!visible;
      this._saveSettings();
      this._syncBoxWindows();
      this._pushResult();   // 单/双框布局即时切换
      return this.state();
    });
    ipcMain.handle('quizSearch:resetBoxes', () => {
      const d = DEFAULT_SETTINGS.boxes;
      const wa = screen.getPrimaryDisplay().workArea;
      this.settings.boxes.A = Object.assign({}, d.A);
      this.settings.boxes.B = Object.assign({}, d.B, { y: wa.y + wa.height - d.B.h - 120 });
      this._saveSettings();
      for (const boxId of ['A', 'B']) {
        const win = this.boxWins[boxId];
        if (win && !win.isDestroyed()) {
          const b = this.settings.boxes[boxId];
          win.setBounds({ x: b.x, y: b.y, width: b.w, height: b.h });
        }
      }
      return this.state();
    });
    ipcMain.handle('quizSearch:setOptions', (_e, opts) => {
      if (opts.interval !== undefined) this.settings.interval = Math.min(3000, Math.max(500, opts.interval | 0));
      if (opts.threshold !== undefined) this.settings.threshold = Math.min(0.9, Math.max(0.3, Number(opts.threshold)));
      if (opts.opacity !== undefined) {
        this.settings.opacity = Math.min(1, Math.max(0.3, Number(opts.opacity)));
        if (this.resultWin && !this.resultWin.isDestroyed()) this.resultWin.setOpacity(this.settings.opacity);
      }
      this._saveSettings();
      if (this.scanning) {   // 间隔变更即时生效
        clearInterval(this.timer);
        this.timer = setInterval(() => { this._tick().catch(() => {}); }, this.settings.interval);
      }
      return this.state();
    });

    // 题库管理（rows 由渲染进程 SheetJS 解析后传入）
    ipcMain.handle('quizSearch:bankImport', (_e, payload) => {
      const r = this.store.importRows(payload.name, payload.sourceFile, payload.rows);
      this.indexDirty = true;
      return Object.assign({ ok: true }, r, this._bankState());
    });
    ipcMain.handle('quizSearch:bankRemove', (_e, id) => {
      this.store.remove(id);
      this.indexDirty = true;
      return this._bankState();
    });
    ipcMain.handle('quizSearch:bankRename', (_e, id, name) => {
      this.store.rename(id, name);
      return this._bankState();
    });
    ipcMain.handle('quizSearch:bankSetEnabled', (_e, id, enabled) => {
      this.store.setEnabled(id, enabled);
      this.indexDirty = true;
      return this._bankState();
    });
    ipcMain.handle('quizSearch:testMatch', (_e, text) => {
      if (this.indexDirty) this._rebuildIndex();
      const hit = match.findBest(this.bankIndex, text, this.settings.threshold);
      if (!hit) return { matched: false };
      return { matched: !!hit.question, score: hit.score, question: hit.question || null, near: hit.near || null, norm: hit.norm };
    });

    // 扫描框渲染进程 → 主进程：拖动/缩放/隐藏。
    // 位移由主进程轮询 OS 光标（screen.getCursorScreenPoint，Windows/Linux 均可靠）得出——
    // 渲染层 screenX/movementX 在悬浮窗上不可靠（合成输入恒为 0，已踩坑）；
    // 窗口跟手后光标始终留在窗内，pointerup 不依赖 pointer capture（Linux 断流，已踩坑）。
    ipcMain.on('quizsearch:boxDragStart', (_e, boxId, mode) => {
      const win = this.boxWins[boxId];
      if (!win || win.isDestroyed()) return;
      this._endBoxDrag();   // 防御：清掉可能残留的旧拖拽
      const b = win.getBounds();
      const cursor = screen.getCursorScreenPoint();
      this._boxDrag = {
        boxId,
        mode: mode === 'resize' ? 'resize' : 'move',
        cursor0: cursor,
        bounds0: { x: b.x, y: b.y, w: b.width, h: b.height },
        startedAt: Date.now(),
        timer: setInterval(() => this._pollBoxDrag(), 16),
      };
    });
    ipcMain.on('quizsearch:boxDragEnd', () => this._endBoxDrag());
    ipcMain.on('quizsearch:boxHide', (_e, boxId) => {
      if (!this.settings.boxes[boxId]) return;
      this.settings.boxes[boxId].visible = false;
      this._saveSettings();
      this._syncBoxWindows();
      this._pushResult();   // 单/双框布局即时切换
      this._pushPageEvent({ type: 'boxHidden', box: boxId });
    });
    // 结果窗拖动/缩放（用户可自由摆放与调整大小；轮询 OS 光标位移，dragEnd 时落盘，下次开搜恢复）
    ipcMain.on('quizsearch:resultDragStart', (_e, mode) => {
      if (!this.resultWin || this.resultWin.isDestroyed()) return;
      if (this._resultDrag && this._resultDrag.timer) clearInterval(this._resultDrag.timer);
      const b = this.resultWin.getBounds();
      this._resultDrag = {
        mode: mode === 'resize' ? 'resize' : 'move',
        cursor0: screen.getCursorScreenPoint(),
        bounds0: { x: b.x, y: b.y, w: b.width, h: b.height },
        startedAt: Date.now(),
        timer: setInterval(() => {
          const d = this._resultDrag;
          if (!d || !this.resultWin || this.resultWin.isDestroyed()) return;
          if (Date.now() - d.startedAt > 5 * 60 * 1000) { ipcMain.emit('quizsearch:resultDragEnd'); return; }
          const c = screen.getCursorScreenPoint();
          const dx = c.x - d.cursor0.x;
          const dy = c.y - d.cursor0.y;
          if (d.mode === 'move') {
            this.resultWin.setPosition(Math.round(d.bounds0.x + dx), Math.round(d.bounds0.y + dy));
          } else {
            let w = Math.max(260, Math.round(d.bounds0.w + dx));
            let h = Math.max(140, Math.round(d.bounds0.h + dy));
            if (w < d.bounds0.w) w += 3;
            if (h < d.bounds0.h) h += 3;
            // Windows 下 setSize 缩小只生效一次（透明窗老坑）；setBounds 可靠
            const cur = this.resultWin.getBounds();
            this.resultWin.setBounds({ x: cur.x, y: cur.y, width: w, height: h });
          }
        }, 16),
      };
    });
    ipcMain.on('quizsearch:resultDragEnd', () => {
      if (this._resultDrag && this._resultDrag.timer) clearInterval(this._resultDrag.timer);
      this._resultDrag = null;
      if (this.resultWin && !this.resultWin.isDestroyed()) {
        const b = this.resultWin.getBounds();
        this.settings.result = { x: b.x, y: b.y, w: b.width, h: b.height };
        this._saveSettings();
      }
    });
  }

  dispose() {
    if (this.timer) clearInterval(this.timer);
    this._killWorker();
    for (const boxId of ['A', 'B']) {
      if (this.boxWins[boxId] && !this.boxWins[boxId].isDestroyed()) this.boxWins[boxId].destroy();
    }
    if (this.resultWin && !this.resultWin.isDestroyed()) this.resultWin.destroy();
  }
}

module.exports = { QuizSearchService };
