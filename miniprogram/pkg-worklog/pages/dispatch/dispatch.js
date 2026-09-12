// 出工日志 · 派车对齐（超管 / 班组管理员；与网页端 worklog.html「派车对齐」页签同口径）：
// 顶部「每日自动同步派车单」开关（GET/PUT /dispatch/sync-switch，按生效班组；切换需确认，次日起按排程执行）
// wx.chooseMessageFile 从聊天选派车单（.xls/.xlsx，每个驾驶员一份、可多份合并）→ 手工拼装 multipart
// 一次提交 POST /worklog/dispatch/align（服务端只读对齐不写库）→ 概览 + 不一致清单 → 底部弹层逐条对照处理：
//   matched 改卡（PUT /logs/{id} 整卡更新，patrol_content 随条目带回避免误清）；
//   sheetOnly 按表格预填快速建卡（POST /logs，确认才写库，非自动建）；entryOnly 仅提示确认，不自动删卡；
//   表格车牌不在本班字典时可就地「添加进字典」（POST /admin/vehicles）
// 班组口径同 tasksheet 页：超管按主页切换器存下的 worklog_team_id 生效；班组管理员后端强制本班
// multipart 手工拼装原因同安全日：wx.uploadFile 单请求仅支持单文件且丢失中文原名，本页需多文件并保留原名
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { BASE_URL } from '../../../config';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const MAX_FILES = 20; // 与服务端 multer files 上限一致
const MAX_SIZE = 10 * 1024 * 1024; // 单文件 10MB，与服务端一致
const ALLOWED = ['xls', 'xlsx'];

const extOf = (name) => {
  const i = (name || '').lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1).toLowerCase();
};
const fmtSize = (n) => {
  if (n >= 1048576) return `${(n / 1048576).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
};

// ---------- 手工拼装 multipart/form-data（与安全日 generate 同法；字节格式与网页端 FormData 一致） ----------

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

// 目的地模糊一致（与 server/src/worklog/dispatch.js destSame 同口径）：
// 表格常省略「市/县」等字样（如卡片「孝义市」表格写「吕梁市孝义」），去「中国/省/市/县/区」后互相包含即一致
function destSame(cardDest, sheetTo) {
  const card = String(cardDest || '').trim();
  const sheet = String(sheetTo || '').trim();
  if (!sheet) return true;
  if (!card) return false;
  if (card === sheet || card.includes(sheet) || sheet.includes(card)) return true;
  const nc = card.replace(/中国|省|市|县|区/g, '');
  const ns = sheet.replace(/中国|省|市|县|区/g, '');
  if (nc.length < 2 || ns.length < 2) return false;
  return nc.includes(ns) || ns.includes(nc);
}

// 清单条目 → 列表行视图模型（badges + 分段 sumline；cls: b=藏青加粗 / hl=橙 / hl-r=红 / vs=分隔灰）
function mapItem(it) {
  const badges = [];
  const segs = [];
  if (it.kind === 'matched') {
    if (it.diffs.plate) badges.push({ t: '车牌不一致', cls: 'plate' });
    if (it.diffs.dest) badges.push({ t: '目的地不一致', cls: 'dest' });
    if (it.diffs.missing.length || it.diffs.extra.length) badges.push({ t: '用车人差异', cls: 'mem' });
    segs.push({ t: it.sheet.members.join(' '), cls: 'b' }, { t: '｜', cls: 'vs' });
    const parts = [];
    if (it.diffs.plate) {
      parts.push([{ t: '表格 ', cls: '' }, { t: it.sheet.plate, cls: 'hl' }, { t: ' → ', cls: 'vs' }, { t: `系统 ${it.entry.plate}`, cls: '' }]);
    }
    if (it.diffs.dest) {
      parts.push([{ t: '目的地 表格 ', cls: '' }, { t: it.sheet.to || '—', cls: 'hl' }, { t: ' → ', cls: 'vs' }, { t: `系统 ${it.entry.destination || '未选'}`, cls: '' }]);
    }
    if (it.diffs.extra.length) parts.push([{ t: '卡片多 ', cls: '' }, { t: it.diffs.extra.join(' '), cls: 'hl-r' }]);
    if (it.diffs.missing.length) parts.push([{ t: '卡片少 ', cls: '' }, { t: it.diffs.missing.join(' '), cls: 'hl-r' }]);
    parts.forEach((p, pi) => {
      if (pi > 0) segs.push({ t: '，', cls: 'vs' });
      segs.push(...p);
    });
  } else if (it.kind === 'sheetOnly') {
    badges.push({ t: '表格有·系统无', cls: 'none-sys' });
    segs.push(
      { t: `${it.sheet.plate} · ${it.sheet.members.join(' ')}`, cls: 'b' },
      { t: '｜', cls: 'vs' },
      { t: '当天系统无对应出车卡片，可在弹层快速建卡', cls: '' }
    );
  } else {
    badges.push({ t: '系统有·表格无', cls: 'none-sheet' });
    segs.push(
      { t: `${it.entry.plate} · ${it.entry.members.map((m) => m.name).join(' ')}`, cls: 'b' },
      { t: '｜', cls: 'vs' },
      { t: '派车单中无此记录，仅提示确认', cls: '' }
    );
  }
  return {
    kind: it.kind,
    dateText: it.date.slice(5), // MM-DD
    badges,
    segs,
    actText: it.kind === 'entryOnly' ? '查看 ›' : '处理 ›',
    done: false,
  };
}

Page({
  data: {
    gate: false,
    // 每日自动同步派车单开关（按生效班组）
    syncEnabled: false,
    syncSwitchLoading: false, // 切换提交中（防连点）
    // 派车单文件（聊天选取）
    files: [], // [{name, sizeText, path}]
    uploading: false,
    // 对齐结果
    hasResult: false,
    sums: [], // 概览六宫格 [{n, t, cls}]
    hints: [], // skipped / outside 提示
    items: [], // 不一致清单行（mapItem 后结构）
    // ---------- 对照弹层 ----------
    dlgVisible: false,
    dlgKind: '', // matched / sheetOnly / entryOnly
    dlgIdx: 0,
    dlgProg: '', // 「第 x / N 条 · YYYY-MM-DD」
    dlgSheetFile: '', // 表格来源文件名
    dlgSheetRows: [], // 表格只读行 [{k, v, cls}]（cls: diff=橙 / diff-bad=红）
    dlgEntryRows: [], // entryOnly 卡片只读行
    dlgTip: '', // entryOnly 提示文案
    // 编辑区（matched 改卡 / sheetOnly 建卡共用）
    plateIdx: 0,
    plateRange: [], // picker 文案（可能含占位首项「（请选择车牌）」）
    plateWarn: false, // 车牌不一致高亮选择框
    plateNote: '',
    plateNoteRed: false,
    showAddVeh: false, // 表格车牌不在字典时显示「添加进字典」
    chips: [], // 用车人点亮 [{id, name, on, extra, add}]
    memNote: '', // 红点 / 绿虚框说明
    memUnknown: '', // 表格用车人不在成员字典提示
    destIdx: 0,
    destRange: [], // picker 文案（可能含占位首项）
    destWarn: false,
    destNote: '',
    destNoteRed: false,
    patrolText: '', // matched 巡视内容只读展示
    dlgSaving: false,
    isFirst: true,
    isLast: true,
  },

  async onLoad() {
    // 超管 / 班组管理员可访问（等启动自检完成再取角色；同 tasksheet 页口径）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    if (user.role !== 'admin' && user.role !== 'team_admin') {
      this.toast('仅管理员可访问');
      setTimeout(() => wx.navigateBack(), 1200);
      return;
    }
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0;
    this._items = []; // 对齐结果原始条目（弹层读写，含编辑后内存同步）
    this._meta = null; // 字典缓存（vehicles / destinations / members）
    this.setData({ gate: true });
    this.loadSyncSwitch();
    this.ensureMeta().catch(() => {}); // 预拉字典，失败在弹层打开时重试
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 生效班组 query / body 注入（仅超管 _teamId>0 时携带，其余角色后端强制本班；口径同主页）
  teamQuery() {
    return this._teamId ? `?team_id=${this._teamId}` : '';
  },

  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  /* ==================== 每日自动同步派车单开关（按生效班组；开启/关闭次日起按排程执行） ==================== */

  // 进入页面拉取当前开关状态
  async loadSyncSwitch() {
    try {
      const data = await request({ url: `/api/v1/worklog/dispatch/sync-switch${this.teamQuery()}` });
      this.setData({ syncEnabled: !!(data && data.enabled) });
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 切换开关：先确认再 PUT（t-switch 受控，未 setData 前视觉不变，取消/失败即保持原状态）
  onSyncSwitch(e) {
    if (this.data.syncSwitchLoading) return;
    const enabled = !!e.detail.value;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: enabled ? '开启每日自动同步' : '关闭每日自动同步',
      content: enabled
        ? '开启后每日 09:20 自动取回当日派车单，按本班成员匹配建出车卡片；用车人含多个班组人名的记录仅通知不建卡。次日起按排程执行，确定开启吗？'
        : '关闭后每日不再自动取回派车单建卡，已建卡片不受影响。次日起按排程执行，确定关闭吗？',
      confirmBtn: enabled ? '开启' : '关闭',
      cancelBtn: '取消',
    }).then(async () => {
      this.setData({ syncSwitchLoading: true });
      try {
        const data = await request({
          url: '/api/v1/worklog/dispatch/sync-switch',
          method: 'PUT',
          data: this.teamBody({ enabled }),
        });
        this.setData({ syncEnabled: !!(data && data.enabled), syncSwitchLoading: false });
        this.toast(enabled ? '已开启，次日起按排程执行' : '已关闭，次日起按排程执行');
      } catch (err) {
        this.setData({ syncEnabled: !enabled, syncSwitchLoading: false }); // 失败回退开关状态
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 字典（车牌 / 目的地 / 成员）：force 时重拉（车牌入字典后）
  async ensureMeta(force) {
    if (this._meta && !force) return this._meta;
    const data = await request({ url: `/api/v1/worklog/meta${this.teamQuery()}` });
    this._meta = {
      vehicles: (data && data.vehicles) || [],
      destinations: (data && data.destinations) || [],
      members: (data && data.members) || [],
    };
    return this._meta;
  },

  /* ==================== 派车单文件（从聊天选取）与校验 ==================== */

  onPickFiles() {
    if (this.data.uploading) return;
    const left = MAX_FILES - this.data.files.length;
    if (left <= 0) {
      this.toast(`最多导入 ${MAX_FILES} 个文件`);
      return;
    }
    wx.chooseMessageFile({
      count: left,
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
      incoming.push({ name: t.name, sizeText: fmtSize(t.size), path: t.path });
    });
    this.setData({ files: this.data.files.concat(incoming) });
    if (badType > 0) this.toast(`已忽略 ${badType} 个非 Excel 文件`);
    if (badSize > 0) this.toast(`已忽略 ${badSize} 个超过 10MB 的文件`);
  },

  onRemoveFile(e) {
    if (this.data.uploading) return;
    const files = [...this.data.files];
    files.splice(Number(e.currentTarget.dataset.index), 1);
    this.setData({ files });
  },

  /* ==================== 上传并对齐（multipart 字段 files 多文件一次提交；主平台信封 {code,message,data}） ==================== */

  uploadAlign() {
    const boundary = `----ShadeDispatch${Date.now()}`;
    const fsm = wx.getFileSystemManager();
    const parts = [];
    // 生效班组（仅超管携带；其余角色后端强制本班）
    if (this._teamId) {
      parts.push(utf8Buffer(`--${boundary}\r\nContent-Disposition: form-data; name="team_id"\r\n\r\n${this._teamId}\r\n`));
    }
    this.data.files.forEach((f) => {
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
        url: `${BASE_URL}/api/v1/worklog/dispatch/align`,
        method: 'POST',
        data: concatBuffers(parts),
        timeout: 120000,
        header: {
          'Content-Type': `multipart/form-data; boundary=${boundary}`,
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        success: (res) => {
          if (res.statusCode === 401) {
            wx.removeStorageSync('token');
            wx.removeStorageSync('userInfo');
            wx.reLaunch({ url: '/pages/login/login' });
            reject(new Error('登录已过期，请重新登录'));
            return;
          }
          const body = res.data || {};
          if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
            resolve({ data: body.data, message: body.message });
            return;
          }
          reject(new Error(body.message || `对齐失败（${res.statusCode}）`));
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  async onAlign() {
    if (!this.data.files.length || this.data.uploading) return;
    this.setData({ uploading: true });
    wx.showLoading({ title: '正在对齐…', mask: true });
    try {
      const r = await this.uploadAlign();
      this._items = (r.data && r.data.items) || [];
      this.applyResult((r.data && r.data.summary) || {});
      this.toast(r.message || '对齐完成');
    } catch (err) {
      this.toast(err.message);
    }
    wx.hideLoading();
    this.setData({ uploading: false });
  },

  // 概览六宫格 + 提示 + 清单行（同网页端 renderPA）
  applyResult(s) {
    this.setData({
      hasResult: true,
      sums: [
        { n: s.consistent || 0, t: '完全一致', cls: 'ok' },
        { n: s.plate || 0, t: '车牌不一致', cls: 'warn' },
        { n: s.destination || 0, t: '目的地不一致', cls: 'warn' },
        { n: s.members || 0, t: '用车人差异', cls: 'bad' },
        { n: s.sheetOnly || 0, t: '表格有·系统无', cls: 'mut' },
        { n: s.entryOnly || 0, t: '系统有·表格无', cls: 'mut' },
      ],
      hints: [
        s.skipped ? `另有 ${s.skipped} 行「预计用车时间」无法识别，已跳过。` : '',
        s.outside ? `已剔除 ${s.outside} 行用车人均非本班成员的记录（他班派车不参与对齐）。` : '',
      ].filter(Boolean),
      items: this._items.map(mapItem),
    });
  },

  /* ==================== 对照弹层（逐条处理） ==================== */

  onItemTap(e) {
    this.openDlg(Number(e.currentTarget.dataset.index));
  },

  // 翻页态（上一条置灰 / 下一条文案）
  dlgNav(i) {
    const total = this._items.length;
    return {
      dlgIdx: i,
      isFirst: i === 0,
      isLast: i + 1 >= total,
      dlgProg: `第 ${i + 1} / ${total} 条 · ${this._items[i].date}`,
    };
  },

  // 表格只读行（matched 按 diffs 高亮差异字段）
  sheetRowsOf(it) {
    const d = it.kind === 'matched' ? it.diffs : null;
    const s = it.sheet;
    return [
      { k: '派车单号', v: s.orderNo || '—', cls: '' },
      { k: '用车日期', v: s.date, cls: '' },
      { k: '车牌号码', v: s.plate, cls: d && d.plate ? 'diff' : '' },
      { k: '驾驶员', v: s.driver || '—', cls: '' },
      { k: '用车人', v: s.members.join(' ') || '—', cls: d && (d.missing.length || d.extra.length) ? 'diff-bad' : '' },
      { k: '目的地', v: s.to || '—', cls: d && d.dest ? 'diff' : '' },
      { k: '用车事由', v: s.reason || '—', cls: '' },
    ];
  },

  async openDlg(i) {
    const it = this._items[i];
    if (!it) return;
    if (it.kind === 'entryOnly') {
      // 提示态：仅展示数据，无可改项
      this.setData({
        ...this.dlgNav(i),
        dlgVisible: true,
        dlgKind: 'entryOnly',
        dlgSaving: false,
        dlgEntryRows: [
          { k: '车牌号', v: it.entry.plate },
          { k: '用车人', v: it.entry.members.map((m) => m.name).join(' ') },
          { k: '目的地', v: it.entry.destination || '—' },
        ],
        dlgTip: '派车单中没有与该卡片对应的记录。可能为派车单导出不完整或该次出车未走派车申请；确认无误即可，本功能不自动删卡。',
      });
      return;
    }

    let meta;
    try {
      meta = await this.ensureMeta();
    } catch (err) {
      this.toast(err.message);
      return;
    }
    const s = it.sheet;
    const base = {
      ...this.dlgNav(i),
      dlgVisible: true,
      dlgKind: it.kind,
      dlgSaving: false,
      dlgSheetFile: s.file,
      dlgSheetRows: this.sheetRowsOf(it),
    };

    if (it.kind === 'sheetOnly') {
      // 快速建卡：按表格预填车牌 / 用车人 / 目的地，确认后 POST /logs 建卡
      const plateHit = meta.vehicles.find((v) => v.plate_no === s.plate) || null;
      const destHit = s.to ? meta.destinations.find((v) => destSame(v.name, s.to)) || null : null;
      const unknown = s.members.filter((n) => !meta.members.some((m) => m.name === n));
      this._dlgIds = {
        plateIds: plateHit ? meta.vehicles.map((v) => v.id) : [null].concat(meta.vehicles.map((v) => v.id)),
        destIds: [null].concat(meta.destinations.map((v) => v.id)),
      };
      this.setData({
        ...base,
        plateIdx: plateHit ? meta.vehicles.findIndex((v) => v.id === plateHit.id) : 0,
        plateRange: plateHit ? meta.vehicles.map((v) => v.plate_no) : ['（请选择车牌）'].concat(meta.vehicles.map((v) => v.plate_no)),
        plateWarn: false,
        plateNote: plateHit ? '已按表格预填，可改选' : `表格车牌 ${s.plate} 不在本班字典`,
        plateNoteRed: !plateHit,
        showAddVeh: !plateHit,
        chips: meta.members.map((m) => ({ id: m.id, name: m.name, on: s.members.includes(m.name), extra: false, add: false })),
        memNote: '',
        memUnknown: unknown.length ? `表格用车人「${unknown.join(' ')}」不在本班成员字典，无法点亮（可在数据管理中添加成员）` : '',
        destIdx: destHit ? meta.destinations.findIndex((v) => v.id === destHit.id) + 1 : 0,
        destRange: ['（暂不选）'].concat(meta.destinations.map((v) => v.name)),
        destWarn: false,
        destNote: destHit ? '已按表格模糊匹配预填，可改选' : (s.to ? `表格目的地「${s.to}」与本班字典无匹配项，请手工选择或暂不选` : ''),
        destNoteRed: !destHit && !!s.to,
        patrolText: '',
      });
      return;
    }

    // matched：右栏为可改卡片（车牌不一致且表格车牌在字典内时按表格预填）
    const d = it.diffs;
    const entryMids = it.entry.members.map((m) => m.member_id);
    const plateInDict = meta.vehicles.some((v) => v.plate_no === s.plate);
    let plateIdx = meta.vehicles.findIndex((v) => v.id === it.entry.vehicle_id);
    if (d.plate && plateInDict) plateIdx = meta.vehicles.findIndex((v) => v.plate_no === s.plate);
    if (plateIdx < 0) plateIdx = 0;
    // 目的地：命中模糊匹配按表格预填；否则保留卡片当前值（无当前值给「（未选择）」占位，避免误写第一条字典）
    const destHit = d.dest && s.to ? meta.destinations.find((v) => destSame(v.name, s.to)) || null : null;
    const curDestIdx = meta.destinations.findIndex((v) => v.id === it.entry.destination_id);
    const hasDestHolder = !destHit && curDestIdx < 0;
    this._dlgIds = {
      plateIds: meta.vehicles.map((v) => v.id),
      destIds: hasDestHolder ? [null].concat(meta.destinations.map((v) => v.id)) : meta.destinations.map((v) => v.id),
    };
    const unknownMissing = d.missing.filter((n) => !meta.members.some((m) => m.name === n));
    this.setData({
      ...base,
      plateIdx,
      plateRange: meta.vehicles.map((v) => v.plate_no),
      plateWarn: d.plate,
      plateNote: d.plate
        ? (plateInDict ? `与表格不一致，已按表格预填为 ${s.plate}，确认后保存` : `表格车牌 ${s.plate} 不在本班字典，暂无法改派`)
        : '',
      plateNoteRed: d.plate && !plateInDict,
      showAddVeh: d.plate && !plateInDict,
      chips: meta.members.map((m) => ({
        id: m.id,
        name: m.name,
        on: entryMids.includes(m.id),
        extra: d.extra.includes(m.name), // 红点 = 卡片多出（点击取消）
        add: d.missing.includes(m.name), // 绿虚框 = 表格有而卡片缺（点击补上）
      })),
      memNote: (d.extra.length || d.missing.length) ? '红点 = 卡片多出（点击取消）；绿虚框 = 表格有而卡片缺（点击补上）' : '',
      memUnknown: unknownMissing.length ? `表格用车人「${unknownMissing.join(' ')}」不在本班成员字典，无法点亮（可在数据管理中添加成员）` : '',
      destIdx: destHit ? meta.destinations.findIndex((v) => v.id === destHit.id) : (hasDestHolder ? 0 : Math.max(0, curDestIdx)),
      destRange: hasDestHolder ? ['（未选择）'].concat(meta.destinations.map((v) => v.name)) : meta.destinations.map((v) => v.name),
      destWarn: d.dest,
      destNote: d.dest
        ? (destHit ? `与表格不一致，已按表格预填为 ${destHit.name}，确认后保存` : `表格目的地「${s.to || '—'}」与本班字典无匹配项，请手工选择`)
        : '',
      destNoteRed: d.dest && !destHit,
      patrolText: it.entry.patrol_content || '（空）',
    });
  },

  onDlgVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.dlgSaving) {
      this.setData({ dlgVisible: true }); // 保存中不允许遮罩关闭
      return;
    }
    if (this.data.dlgVisible) this.setData({ dlgVisible: false });
  },

  onPlateChange(e) {
    this.setData({ plateIdx: Number(e.detail.value) });
  },

  onDestChange(e) {
    this.setData({ destIdx: Number(e.detail.value) });
  },

  onChipTap(e) {
    const i = Number(e.currentTarget.dataset.index);
    this.setData({ [`chips[${i}].on`]: !this.data.chips[i].on });
  },

  onDlgPrev() {
    if (this.data.dlgSaving || this.data.isFirst) return;
    this.openDlg(this.data.dlgIdx - 1);
  },

  // 跳过 = 不处理直接跳下一条（末条则关闭）
  onDlgSkip() {
    if (this.data.dlgSaving) return;
    const i = this.data.dlgIdx;
    if (i + 1 < this._items.length) this.openDlg(i + 1);
    else this.setData({ dlgVisible: false });
  },

  // entryOnly「知道了」（不标记已处理，与网页端一致）
  onDlgNext() {
    this.onDlgSkip();
  },

  // 表格车牌加入本班字典（就地添加后重建弹层，新车牌即按表格预填）
  async onAddVeh() {
    const it = this._items[this.data.dlgIdx];
    if (!it || !it.sheet) return;
    try {
      await request({ url: '/api/v1/worklog/admin/vehicles', method: 'POST', data: this.teamBody({ name: it.sheet.plate }) });
      this.toast('已加入车牌字典');
      this._meta = null; // 字典已变更：重建弹层时重拉
      this.openDlg(this.data.dlgIdx);
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 保存更正：PUT 整卡更新（patrol_content 带回避免误清；被移出成员若有水印照片后端 40006 拒绝，toast 原话提示）
  async onDlgSave(e) {
    if (this.data.dlgSaving) return;
    const advance = e.currentTarget.dataset.advance === '1';
    const i = this.data.dlgIdx;
    const it = this._items[i];
    const body = {
      patrol_content: it.entry.patrol_content,
      vehicle_id: this._dlgIds.plateIds[this.data.plateIdx] || null,
      destination_id: this._dlgIds.destIds[this.data.destIdx] || null,
      member_ids: this.data.chips.filter((c) => c.on).map((c) => c.id),
    };
    this.setData({ dlgSaving: true });
    try {
      await request({ url: `/api/v1/worklog/logs/${it.entry.id}`, method: 'PUT', data: this.teamBody(body) });
      // 同步内存条目（重开弹层读新值）并标记已处理
      const meta = this._meta || { vehicles: [], destinations: [], members: [] };
      const hitV = meta.vehicles.find((v) => v.id === body.vehicle_id);
      const hitD = meta.destinations.find((v) => v.id === body.destination_id);
      it.entry.vehicle_id = body.vehicle_id;
      if (hitV) it.entry.plate = hitV.plate_no;
      it.entry.destination_id = body.destination_id;
      it.entry.destination = hitD ? hitD.name : it.entry.destination;
      it.entry.members = this.data.chips.filter((c) => c.on).map((c) => ({ member_id: c.id, name: c.name }));
      this.afterHandled(i, '已保存', advance);
    } catch (err) {
      this.setData({ dlgSaving: false });
      this.toast(err.message);
    }
  },

  // 表格有系统无：按弹层预填快速建卡（POST /logs，巡视内容留空待补）
  async onDlgCreate(e) {
    if (this.data.dlgSaving) return;
    const advance = e.currentTarget.dataset.advance === '1';
    const i = this.data.dlgIdx;
    const it = this._items[i];
    const vehicleId = this._dlgIds.plateIds[this.data.plateIdx] || null;
    if (!vehicleId) {
      this.toast('请选择车牌号（表格车牌不在字典时可先「添加进字典」）');
      return;
    }
    const body = {
      log_date: it.date,
      patrol_content: '',
      vehicle_id: vehicleId,
      destination_id: this._dlgIds.destIds[this.data.destIdx] || null,
      member_ids: this.data.chips.filter((c) => c.on).map((c) => c.id),
    };
    this.setData({ dlgSaving: true });
    try {
      await request({ url: '/api/v1/worklog/logs', method: 'POST', data: this.teamBody(body) });
      this.afterHandled(i, '已建卡', advance);
    } catch (err) {
      this.setData({ dlgSaving: false });
      this.toast(err.message);
    }
  },

  // 更正 / 建卡成功后的公共收尾：标已处理 + 清单行刷新 + 关闭 / 跳下一条
  afterHandled(i, text, advance) {
    this.setData({ [`items[${i}].done`]: true, dlgSaving: false, dlgVisible: false });
    this.toast(text);
    if (!advance) return;
    if (i + 1 < this._items.length) this.openDlg(i + 1);
    else this.toast('不一致条目已全部处理完');
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '派车对齐' });
  },
});
