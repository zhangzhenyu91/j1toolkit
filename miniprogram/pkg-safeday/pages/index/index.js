// 安全日活动记录 · 小程序端（app_key safe-day；与网页端 safeday.html 逻辑一致：
// 上传活动文件 → 确认记录名称 → 提交生成 → 进度卡跟踪 → 记录列表 5s 轮询至终态）
// 上传：wx.chooseMessageFile 从聊天选取；下载：wx.downloadFile 取回后 wx.openDocument 打开
// 接口信封 {ok,error,...}（非主平台 {code,message,data}），故不用 utils/request，本地封装 sdFetch
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/safeday';
const POLL_INTERVAL = 5000; // 与网页端一致：存在生成中记录时按 5s 轮询 records
const MAX_FILES = 10; // 与服务端 multer 限制一致
const MAX_SIZE = 50 * 1024 * 1024; // 单文件 50MB，与服务端一致
const ALLOWED = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];

const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
const extOf = (name) => {
  const i = (name || '').lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1).toLowerCase();
};
const baseOf = (name) => (name || '').replace(/\.[^.]+$/, '');
const toDots = (iso) => (iso || '').replace(/-/g, '.'); // YYYY-MM-DD → YYYY.MM.DD（服务端要求的日期格式）
const todayISO = () => {
  const d = new Date();
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};
const fmtSize = (n) => {
  if (n >= 1048576) return `${(n / 1048576).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
};
// ISO 时间 → 'YYYY-MM-DD HH:mm'（同网页端 Shade.fmtDate(d, true)）
const fmtCreated = (iso) => {
  const d = iso ? new Date(iso) : null;
  if (!d || Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

// ---------- 手工拼装 multipart/form-data ----------
// wx.uploadFile 单请求仅支持单文件，且 multipart 文件名只能是临时路径 basename
// （中文原名会丢失，sources 展示与扩展名校验都依赖原名），安全日 generate 需一次提交
// 多文件并保留原名，故按网页端 FormData 的字节格式自行拼装（UTF-8 编码文件名，
// 服务端 multer 按 latin1 接收后转回 UTF-8，与浏览器行为一致）

// 字符串 → UTF-8 字节 ArrayBuffer（含 surrogate pair 处理）
function utf8Buffer(str) {
  const bytes = [];
  for (let i = 0; i < str.length; i += 1) {
    let code = str.charCodeAt(i);
    if (code < 0x80) {
      bytes.push(code);
    } else if (code < 0x800) {
      bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code >= 0xd800 && code <= 0xdbff) {
      const lo = str.charCodeAt(i + 1);
      i += 1;
      code = 0x10000 + (((code & 0x3ff) << 10) | (lo & 0x3ff));
      bytes.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f)
      );
    } else {
      bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    }
  }
  return new Uint8Array(bytes).buffer;
}

function concatBuffers(buffers) {
  let total = 0;
  buffers.forEach((b) => { total += b.byteLength; });
  const out = new Uint8Array(total);
  let offset = 0;
  buffers.forEach((b) => {
    out.set(new Uint8Array(b), offset);
    offset += b.byteLength;
  });
  return out.buffer;
}

// ---------- 生成进度卡（阶段与文案同网页端） ----------
const STEP_TEXTS = [
  ['上传完成', '文件已存至服务器'],
  ['内容解析', '提取活动主题与要点'],
  ['Dify 生成中', '正在按模板撰写活动记录…'],
  ['记录入库', '生成后自动加入记录列表'],
];
const stepStates = (phase) => {
  if (phase === 'parse') return ['done', 'doing', 'todo', 'todo'];
  if (phase === 'dify') return ['done', 'done', 'doing', 'todo'];
  if (phase === 'done') return ['done', 'done', 'done', 'done'];
  return ['done', 'done', 'fail', 'todo']; // failed
};
const phasePct = (phase) => ({ parse: 45, dify: 75, done: 100 }[phase] || 75);

Page({
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    files: [], // 已选待上传文件 [{name, size, sizeText, path}]
    dateStr: '', // 活动日期 YYYY-MM-DD（picker 值）
    dateDots: '', // 提交格式 YYYY.MM.DD
    // 生成进度卡
    track: null, // {id,name,sourceCount,createdText,phase,error}
    steps: [], // [{t,s,state}] state: done/doing/todo/fail
    pct: 0,
    // 记录列表
    records: [], // 展示用记录（mapRecord 后的结构）
    cntText: '加载中…',
    loading: true,
    openingId: '', // 正在下载打开的记录 id
    // 记录名称确认弹层
    genOpen: false,
    nameDraft: '',
    submitting: false,
    keyboardHeight: 0,
  },

  onLoad() {
    const today = todayISO();
    this.setData({ dateStr: today, dateDots: toDots(today) });

    // gate 兜底：首页宫格已做权限过滤，此处仅保证登录态就绪后再加载
    if (wx.getStorageSync('token')) {
      this.passGate();
      return;
    }
    getApp().globalData.ready.then((authed) => {
      if (authed) {
        this.passGate();
        return;
      }
      wx.navigateBack({
        fail: () => wx.reLaunch({ url: '/pages/home/home' }),
      });
    });
  },

  passGate() {
    if (this.data.gate) return;
    this.setData({ gate: true });
    this.refreshRecords(true);
  },

  onShow() {
    // 切回页面时静默刷新一次（生成可能已完成）；轮询随 refresh 内部恢复
    if (this.data.gate && this._loaded) this.refreshRecords(false);
  },

  onHide() {
    this.stopPolling();
  },

  onUnload() {
    this.stopPolling();
    if (this._phaseTimer) {
      clearTimeout(this._phaseTimer);
      this._phaseTimer = null;
    }
  },

  onPullDownRefresh() {
    this.refreshRecords(false).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onExpired() {
    wx.removeStorageSync('token');
    wx.removeStorageSync('userInfo');
    wx.reLaunch({ url: '/pages/login/login' });
  },

  // safeday 接口封装：信封 {ok,error}，手动带 token，401 清登录态跳登录页
  sdFetch(path, opts = {}) {
    const token = wx.getStorageSync('token');
    return new Promise((resolve, reject) => {
      wx.request({
        url: `${BASE_URL}${API_BASE}${path}`,
        method: opts.method || 'GET',
        timeout: opts.timeout || 30000,
        header: {
          Accept: 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        success: (res) => {
          if (res.statusCode === 401) {
            this.onExpired();
            reject(new Error('登录已过期，请重新登录'));
            return;
          }
          const data = res.data || {};
          if (res.statusCode >= 200 && res.statusCode < 300 && data.ok !== false) {
            resolve(data);
            return;
          }
          reject(new Error(data.error || `请求失败（${res.statusCode}）`));
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  /* ==================== 记录列表与轮询（同网页端：有 processing 则 5s 轮询，全终态停止） ==================== */

  stopPolling() {
    if (this._pollTimer) {
      clearTimeout(this._pollTimer);
      this._pollTimer = null;
    }
  },

  schedulePoll() {
    this.stopPolling();
    this._pollTimer = setTimeout(() => this.refreshRecords(false), POLL_INTERVAL);
  },

  refreshRecords(isInitial) {
    return this.sdFetch('/records')
      .then((data) => {
        const records = data.records || [];
        this._records = records; // 原始记录（下载取 fileName 用）
        this._loaded = true;
        this.renderRecords(records);
        this.syncTrack(records);
        if (records.some((r) => r.status === 'processing')) this.schedulePoll();
        else this.stopPolling();
      })
      .catch((err) => {
        if (isInitial) this.setData({ loading: false, cntText: '加载失败' });
        this.toast(err.message || '记录列表加载失败');
        // 轮询中出错：仍按已知 processing 状态继续轮询，避免状态卡死
        if (!isInitial && (this._records || []).some((r) => r.status === 'processing')) {
          this.schedulePoll();
        }
      });
  },

  // 记录 → 展示结构（主标题 = 生成的记录文件名；副行 = 上传源文件名，历史记录回退「N 个源文件」）
  mapRecord(r) {
    const done = r.status === 'done';
    return {
      id: String(r.id),
      name: r.name || '',
      fileName: r.fileName || '—',
      sub: (r.sources && r.sources.length) ? `《${r.sources.join('、')}》` : `${r.sourceCount || 1} 个源文件`,
      date: r.date || '—',
      createdText: fmtCreated(r.createdAt) || '—',
      status: r.status,
      statusText: done ? '已生成' : r.status === 'failed' ? '生成失败' : '生成中',
      done,
      failed: r.status === 'failed',
      error: r.error || '',
    };
  },

  renderRecords(records) {
    const processing = records.filter((r) => r.status === 'processing').length;
    this.setData({
      records: records.map((r) => this.mapRecord(r)),
      loading: false,
      cntText: `共 ${records.length} 条${processing ? ` · ${processing} 条生成中` : ''}`,
    });
  },

  /* ==================== 生成进度卡 ==================== */

  renderProg() {
    const { track } = this.data;
    if (!track) {
      this.setData({ steps: [], pct: 0 });
      return;
    }
    const states = stepStates(track.phase);
    this.setData({
      steps: STEP_TEXTS.map((s, i) => ({ t: s[0], s: s[1], state: states[i] })),
      pct: phasePct(track.phase),
    });
  },

  // 提交成功后开始跟踪新记录（先「内容解析」短暂展示，再进入「Dify 生成中」）
  startTrack(rec) {
    this.setData({
      track: {
        id: String(rec.id),
        name: rec.name,
        sourceCount: rec.sourceCount,
        createdText: fmtCreated(rec.createdAt),
        phase: 'parse',
        error: '',
      },
    });
    this.renderProg();
    if (this._phaseTimer) clearTimeout(this._phaseTimer);
    this._phaseTimer = setTimeout(() => {
      const { track } = this.data;
      if (track && track.phase === 'parse') {
        this.setData({ 'track.phase': 'dify' });
        this.renderProg();
      }
    }, 1500);
  },

  // 每次轮询后同步跟踪状态；页面重进时若有生成中记录则接管跟踪
  syncTrack(records) {
    const { track } = this.data;
    if (track) {
      const rec = records.find((r) => String(r.id) === track.id);
      if (!rec) {
        this.setData({ track: null }); // 记录已被删除
        this.renderProg();
        return;
      }
      if (rec.status === 'done' && track.phase !== 'done') {
        this.setData({ 'track.phase': 'done' });
        this.renderProg();
        this.toast('活动记录已生成，点击记录即可打开');
      } else if (rec.status === 'failed' && track.phase !== 'failed') {
        this.setData({ 'track.phase': 'failed', 'track.error': rec.error || '生成失败' });
        this.renderProg();
        this.toast(rec.error || '生成失败');
      }
      return;
    }
    const processing = records.find((r) => r.status === 'processing');
    if (processing) {
      this.setData({
        track: {
          id: String(processing.id),
          name: processing.name,
          sourceCount: processing.sourceCount,
          createdText: fmtCreated(processing.createdAt),
          phase: 'dify',
          error: '',
        },
      });
      this.renderProg();
    }
  },

  /* ==================== 文件选择（从聊天选取）与校验（类型/大小/数量/多文件纯 PDF/去重） ==================== */

  onPickFiles() {
    wx.chooseMessageFile({
      count: MAX_FILES,
      type: 'file',
      extension: ALLOWED,
      success: (res) => this.addFiles(res.tempFiles || []),
    });
  },

  addFiles(tempFiles) {
    let badType = 0;
    let badSize = 0;
    const incoming = [];
    tempFiles.forEach((t) => {
      if (!ALLOWED.includes(extOf(t.name))) { badType += 1; return; }
      if (t.size > MAX_SIZE) { badSize += 1; return; }
      if (this.data.files.some((x) => x.name === t.name)) return;
      if (incoming.some((x) => x.name === t.name)) return;
      incoming.push({ name: t.name, size: t.size, sizeText: fmtSize(t.size), path: t.path });
    });
    const merged = this.data.files.concat(incoming);
    if (merged.length > MAX_FILES) {
      this.toast(`最多上传 ${MAX_FILES} 个文件`);
      return;
    }
    // 多文件时须全部为 PDF，否则整批阻止（与服务端规则一致）
    if (merged.length >= 2 && merged.some((f) => extOf(f.name) !== 'pdf')) {
      this.toast('多文件合并仅支持 PDF，请先转换为 PDF 或逐个生成');
      return;
    }
    this.setData({ files: merged });
    if (badType > 0) this.toast(`已忽略 ${badType} 个不支持的文件`);
    if (badSize > 0) this.toast(`已忽略 ${badSize} 个超过 50MB 的文件`);
  },

  onRemoveFile(e) {
    if (this.data.submitting) return;
    const files = [...this.data.files];
    files.splice(e.currentTarget.dataset.index, 1);
    this.setData({ files });
  },

  /* ==================== 活动日期（提交格式 YYYY.MM.DD） ==================== */

  onDateChange(e) {
    const dateStr = e.detail.value;
    this.setData({ dateStr, dateDots: toDots(dateStr) });
  },

  /* ==================== 记录名称确认弹层 ==================== */

  onGenTap() {
    if (!this.data.files.length || this.data.submitting) return;
    const names = this.data.files.map((f) => baseOf(f.name)).join('、');
    this.setData({ genOpen: true, nameDraft: `《${names}》`, keyboardHeight: 0 });
  },

  onNameInput(e) {
    this.setData({ nameDraft: e.detail.value });
  },

  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
  },

  onGenCancel() {
    if (this.data.submitting) return;
    this.setData({ genOpen: false, keyboardHeight: 0 });
  },

  // 提交中不允许遮罩关闭（同网页端 closeGenMask 拦截）
  onGenVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.submitting) {
      this.setData({ genOpen: true });
      return;
    }
    if (this.data.genOpen) this.setData({ genOpen: false, keyboardHeight: 0 });
  },

  /* ==================== 提交生成 ==================== */

  onGenConfirm() {
    if (this.data.submitting) return;
    const name = (this.data.nameDraft || '').trim();
    if (!name) {
      this.toast('请输入记录名称');
      return;
    }
    const { files } = this.data;
    if (!files.length) {
      this.toast('请先选择文件');
      return;
    }
    // 提交前再校验一次多文件 PDF 规则
    if (files.length >= 2 && files.some((f) => extOf(f.name) !== 'pdf')) {
      this.toast('多文件合并仅支持 PDF，请先转换为 PDF 或逐个生成');
      return;
    }
    const date = this.data.dateDots || toDots(todayISO());
    this.setData({ submitting: true });
    this.uploadGenerate(name, date, files)
      .then((data) => {
        this.setData({ genOpen: false, keyboardHeight: 0, files: [] });
        this.toast('已开始生成，请稍候…');
        if (data.record) this.startTrack(data.record);
        // 立即刷新列表；新记录为 processing，refresh 内部会恢复轮询
        this.refreshRecords(false);
      })
      .catch((err) => {
        this.toast(err.message || '生成请求失败');
      })
      .finally(() => this.setData({ submitting: false }));
  },

  // multipart 上传（字段名 files + name/date；字节格式与网页端 FormData 一致，见文件头注释）
  uploadGenerate(name, date, files) {
    const boundary = `----ShadeSafeday${Date.now()}`;
    const fsm = wx.getFileSystemManager();
    const parts = [];
    const pushField = (key, value) => {
      parts.push(utf8Buffer(`--${boundary}\r\nContent-Disposition: form-data; name="${key}"\r\n\r\n${value}\r\n`));
    };
    pushField('name', name);
    pushField('date', date);
    files.forEach((f) => {
      parts.push(utf8Buffer(
        `--${boundary}\r\nContent-Disposition: form-data; name="files"; filename="${f.name}"\r\n` +
        'Content-Type: application/octet-stream\r\n\r\n'
      ));
      parts.push(fsm.readFileSync(f.path));
      parts.push(utf8Buffer('\r\n'));
    });
    parts.push(utf8Buffer(`--${boundary}--\r\n`));

    const token = wx.getStorageSync('token');
    return new Promise((resolve, reject) => {
      wx.request({
        url: `${BASE_URL}${API_BASE}/generate`,
        method: 'POST',
        data: concatBuffers(parts),
        timeout: 120000,
        header: {
          'Content-Type': `multipart/form-data; boundary=${boundary}`,
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        success: (res) => {
          if (res.statusCode === 401) {
            this.onExpired();
            reject(new Error('登录已过期，请重新登录'));
            return;
          }
          const data = res.data || {};
          if (res.statusCode >= 200 && res.statusCode < 300 && data.ok !== false) {
            resolve(data);
            return;
          }
          reject(new Error(data.error || `生成请求失败（${res.statusCode}）`));
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  /* ==================== 打开记录（下载 → wx.openDocument） ==================== */

  onOpenRecord(e) {
    const id = e.currentTarget.dataset.id;
    const rec = (this._records || []).find((r) => String(r.id) === String(id));
    if (!rec || rec.status !== 'done' || this.data.openingId) return;
    this.setData({ openingId: String(id) });
    wx.downloadFile({
      url: `${BASE_URL}${API_BASE}/records/${encodeURIComponent(id)}/download`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      success: (res) => {
        if (res.statusCode === 401) {
          this.onExpired();
          return;
        }
        if (res.statusCode !== 200) {
          this.toast(`下载失败（${res.statusCode}）`);
          return;
        }
        wx.openDocument({
          filePath: this.renameTemp(res.tempFilePath, rec.fileName || '安全日活动记录.docx'),
          fileType: 'docx',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => this.setData({ openingId: '' }),
    });
  },

  // downloadFile 的临时路径名是随机的，重命名为真实文件名再打开（同文件传输 renameTemp）
  renameTemp(tempPath, name) {
    const fsm = wx.getFileSystemManager();
    const target = `${wx.env.USER_DATA_PATH}/${name}`;
    try { fsm.unlinkSync(target); } catch (err) { /* 目标不存在则忽略 */ }
    try {
      fsm.renameSync(tempPath, target);
      return target;
    } catch (err) {
      return tempPath; // 重命名失败退回临时路径
    }
  },

  /* ==================== 删除记录 ==================== */

  onDeleteRecord(e) {
    const { id, name } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除这条记录？',
      content: `记录「${name || ''}」及已生成的记录文件将一并删除，该操作不可恢复。`,
      confirmBtn: '确认删除',
      cancelBtn: '取消',
    }).then(() => {
      this.sdFetch(`/records/${encodeURIComponent(id)}`, { method: 'DELETE' })
        .then((data) => {
          // 服务端可能返回 warning（记录已删但文件删除失败）
          this.toast(data.warning || '记录已删除');
          this.refreshRecords(false);
        })
        .catch((err) => this.toast(err.message || '删除失败'));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'safe-day', title: '安全日活动记录' });
  },
});
