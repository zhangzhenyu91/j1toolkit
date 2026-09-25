// 安全日活动记录 · 小程序端（app_key safe-day；与网页端 safeday.html 逻辑一致：
// 上传活动文件（多文件由后端转换合并为单个 PDF）→ 弹层确认学习内容与参会信息
// （主持人/记录人/上级参加人员/参加缺席互斥点亮或自定义输入/缺席原因）→ 提交生成 → 进度卡跟踪 → 记录列表 5s 轮询至终态）
// 文档由后端套模板生成（Dify 仅回传三段文字），见 server/src/safeday/render.js
// 上传：wx.chooseMessageFile 从聊天选取；下载：wx.downloadFile 取回后 wx.openDocument 打开
// 接口信封 {ok,error,...}（非主平台 {code,message,data}），故不用 utils/request，本地封装 sdFetch
// 班组口径（屏八）：超管顶部切换器可选「全部班组」+ 各班组（records 带 team_id，全部=all）；
// 其余角色固定本班；非超管未分配班组 → 整页空态（屏十）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';
import { createTeamGate } from '../../../utils/teamgate';
import { pad, fmtSize, extOf } from '../../../utils/util';
import { utf8Buffer, concatBuffers } from '../../../utils/multipart';

const API_BASE = '/api/v1/safeday';
const POLL_INTERVAL = 5000; // 与网页端一致：存在生成中记录时按 5s 轮询 records
const MAX_FILES = 10; // 与服务端 multer 限制一致
const MAX_SIZE = 50 * 1024 * 1024; // 单文件 50MB，与服务端一致
const ALLOWED = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];

// 班组切换器 + 生效班组门控（storage safeday_team_id；withAll：超管可切「全部班组」，
// records 带 team_id=all|班组 id，「全部班组」混排视图在记录行显示班组徽章）
const teamGate = createTeamGate({ storageKey: 'safeday_team_id', withAll: true });

const baseOf = (name) => (name || '').replace(/\.[^.]+$/, '');
// 自定义人员输入规范化：空格/顿号/逗号/分号分隔统一为空格分隔（后端模板按空格分隔口径渲染）
const normNames = (str) =>
  (str || '').split(/[\s、，,；;]+/).filter((s) => s).join(' ');
const toDots = (iso) => (iso || '').replace(/-/g, '.'); // YYYY-MM-DD → YYYY.MM.DD（服务端要求的日期格式）
const todayISO = () => {
  const d = new Date();
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};
// ISO 时间 → 'YYYY-MM-DD HH:mm'（同网页端 Shade.fmtDate(d, true)）
const fmtCreated = (iso) => {
  const d = iso ? new Date(iso) : null;
  if (!d || Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

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
  behaviors: [teamGate],
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    // 班组切换器数据（isAdmin/noTeam/teamName/teamOptions/teamDropOpen/showTeamPill）由 teamgate behavior 提供；
    // 非超管未分配班组 → noTeam 整页空态（屏十），不发业务请求
    files: [], // 已选待上传文件 [{name, size, sizeText, path}]
    dateStr: '', // 活动日期 YYYY-MM-DD（picker 值；提交时转 YYYY.MM.DD）
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
    // 生成表单扩展字段（成员名单与默认值来自 GET /form-meta）
    members: [], // 班组成员名单（顺序=点亮按钮顺序，顺序1为默认主持人）
    hostIdx: 0, // 主持人选中下标
    recorderIdx: 0, // 记录人选中下标（默认上次选择）
    superior: '', // 上级参加人员（默认记忆值/任晓辉）
    attendFlags: [], // 参加标记（与 members 平行；false=缺席，两组 chips 互斥镜像）
    absentReason: '',
    // 参会人员自定义模式：member=名单点亮（主持人/参加/缺席取自成员名单）；
    // custom=自行输入（主持人、参加、缺席均为手工填写，适用于名单外人员或缺成员字典场景）
    attendMode: 'member',
    hostCustom: '', // 自定义模式主持人
    customAttendees: '', // 自定义模式参加人员（空格/顿号/逗号分隔均可，提交时规范化为空格）
    customAbsentees: '', // 自定义模式缺席人员
  },

  onLoad() {
    this.setData({ dateStr: todayISO() });

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
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    // 门控与生效班组确定由 teamgate behavior 完成（非超管未分配班组 → noTeam 空态，不再加载）
    if (!this.passTeamGate(user, () => this.refreshRecords(true))) {
      this.setData({ cntText: '' });
    }
  },

  // 超管主动切换班组后重拉记录列表（teamgate behavior 回调）；
  // 生成表单元数据按班组缓存，切班组后重拉
  onTeamSwitched() {
    this._formMeta = null;
    this.refreshRecords(false);
  },

  onShow() {
    // 切回页面时静默刷新一次（生成可能已完成）；轮询随 refresh 内部恢复
    if (this.data.gate && !this.data.noTeam && this._loaded) this.refreshRecords(false);
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
    if (this.data.noTeam) {
      wx.stopPullDownRefresh();
      return;
    }
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
    return this.sdFetch(`/records${this.teamQuery()}`)
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
      team: r.team || '', // 班组徽章（仅「全部班组」混排视图展示）
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

  /* ==================== 文件选择（从聊天选取）与校验（类型/大小/数量/去重；多文件由后端转换合并） ==================== */

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

  /* ==================== 活动日期（提交时转 YYYY.MM.DD，页面不再展示格式预览） ==================== */

  onDateChange(e) {
    this.setData({ dateStr: e.detail.value });
  },

  /* ==================== 记录名称确认弹层 ==================== */

  onGenTap() {
    if (!this.data.files.length || this.data.submitting) return;
    // 超管在「全部班组」视图下不可生成（生成须归属具体班组，同网页端口径）
    if (this._role === 'admin' && this._teamSel === 'all') {
      this.toast('请先切换到具体班组再生成');
      return;
    }
    // 学习内容默认值：每个源文件各自带书名号（《文件1》、《文件2》），不再整体括一对
    const names = this.data.files.map((f) => `《${baseOf(f.name)}》`).join('、');
    this.setData({
      genOpen: true,
      nameDraft: names,
      keyboardHeight: 0,
      // 每次打开回到名单点亮模式，自定义输入清空（名单数据异步拉取后覆盖）
      attendMode: 'member',
      hostCustom: '',
      customAttendees: '',
      customAbsentees: '',
    });
    // 打开弹层后异步拉表单元数据并填充（成员/默认值按当前生效班组）
    this.loadFormMeta()
      .then((meta) => {
        const { members, defaults } = meta;
        this.setData({
          members,
          hostIdx: 0, // 主持人默认班组成员顺序1
          recorderIdx: Math.max(0, members.indexOf(defaults.recorder)), // 记录人默认上次选择
          superior: defaults.superior || '任晓辉',
          attendFlags: members.map(() => true), // 默认全员参加
          absentReason: '',
        });
      })
      .catch((err) => this.toast(err.message || '表单数据加载失败'));
  },

  // 生成表单元数据：班组成员 + 记忆默认值（按当前生效班组缓存，切班组失效）
  loadFormMeta() {
    if (this._formMeta && this._formMetaTeam === this._teamSel) return Promise.resolve(this._formMeta);
    return this.sdFetch(`/form-meta${this.teamQuery()}`).then((data) => {
      this._formMeta = { members: (data && data.members) || [], defaults: (data && data.defaults) || {} };
      this._formMetaTeam = this._teamSel;
      return this._formMeta;
    });
  },

  onHostChange(e) {
    this.setData({ hostIdx: Number(e.detail.value) });
  },

  onRecorderChange(e) {
    this.setData({ recorderIdx: Number(e.detail.value) });
  },

  onSuperiorInput(e) {
    this.setData({ superior: e.detail.value });
  },

  onAbsentReasonInput(e) {
    this.setData({ absentReason: e.detail.value });
  },

  // 参加/缺席互斥：点名字即切换归属（两组 chips 镜像，无需分别维护）
  onToggleAttend(e) {
    const i = Number(e.currentTarget.dataset.idx);
    this.setData({ [`attendFlags[${i}]`]: !this.data.attendFlags[i] });
  },

  // 参会人员模式切换（名单点亮 / 自定义输入）；切到自定义时按当前名单选择预填，切回不清空手工输入
  onAttendModeTap(e) {
    const mode = e.currentTarget.dataset.mode;
    if (!mode || mode === this.data.attendMode) return;
    const patch = { attendMode: mode };
    if (mode === 'custom') {
      const { members, hostIdx, attendFlags } = this.data;
      patch.hostCustom = members[hostIdx] || '';
      patch.customAttendees = members.filter((_, i) => attendFlags[i]).join(' ');
      patch.customAbsentees = members.filter((_, i) => !attendFlags[i]).join(' ');
    }
    this.setData(patch);
  },

  onHostCustomInput(e) {
    this.setData({ hostCustom: e.detail.value });
  },

  onCustomAttendeesInput(e) {
    this.setData({ customAttendees: e.detail.value });
  },

  onCustomAbsenteesInput(e) {
    this.setData({ customAbsentees: e.detail.value });
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
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.setData({ genOpen: false, keyboardHeight: 0 });
  },

  // 提交中不允许遮罩关闭（同网页端 closeGenMask 拦截）
  onGenVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.submitting) {
      this.setData({ genOpen: true });
      return;
    }
    if (this.data.genOpen) {
      wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
      this.setData({ genOpen: false, keyboardHeight: 0 });
    }
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
    // 超管在「全部班组」视图下不可生成（生成须归属具体班组）
    if (this._role === 'admin' && this._teamSel === 'all') {
      this.toast('请先切换到具体班组再生成');
      return;
    }
    const date = toDots(this.data.dateStr || todayISO());
    // 生成表单字段（后端存进记录，回调渲染 docx 时使用）；
    // 自定义模式下主持人/参加/缺席取手工输入（分隔符规范化为空格），记录人仍取成员名单
    const { members } = this.data;
    const custom = this.data.attendMode === 'custom';
    const form = {
      host: custom ? (this.data.hostCustom || '').trim() : members[this.data.hostIdx] || '',
      recorder: members[this.data.recorderIdx] || '',
      superior: (this.data.superior || '').trim(),
      attendees: custom
        ? normNames(this.data.customAttendees)
        : members.filter((_, i) => this.data.attendFlags[i]).join(' '),
      absentees: custom
        ? normNames(this.data.customAbsentees)
        : members.filter((_, i) => !this.data.attendFlags[i]).join(' '),
      absentReason: (this.data.absentReason || '').trim(),
    };
    if (custom && !form.attendees) {
      this.toast('请填写本班组参加人员');
      return;
    }
    wx.hideKeyboard(); // 真正提交前收起 hold-keyboard 残留键盘（校验失败分支不收）
    this.setData({ submitting: true });
    this.uploadGenerate(name, date, files, form)
      .then((data) => {
        // 本地缓存同步新默认值，下次打开免重拉
        if (this._formMeta) {
          if (form.superior) this._formMeta.defaults.superior = form.superior;
          if (form.recorder) this._formMeta.defaults.recorder = form.recorder;
        }
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

  // multipart 上传（字段名 files + name/date + 生成表单字段；字节格式与网页端 FormData 一致，拼装说明见 utils/multipart.js）
  uploadGenerate(name, date, files, form) {
    const boundary = `----ShadeSafeday${Date.now()}`;
    const fsm = wx.getFileSystemManager();
    const parts = [];
    const pushField = (key, value) => {
      parts.push(utf8Buffer(`--${boundary}\r\nContent-Disposition: form-data; name="${key}"\r\n\r\n${value}\r\n`));
    };
    pushField('name', name);
    pushField('date', date);
    // 生成表单字段（主持人/记录人/上级参加人员/参加与缺席人员/缺席原因）
    pushField('host', form.host);
    pushField('recorder', form.recorder);
    pushField('superior', form.superior);
    pushField('attendees', form.attendees);
    pushField('absentees', form.absentees);
    pushField('absentReason', form.absentReason);
    // 生效班组（仅超管携带选中的 team_id；其余角色后端强制本班）
    if (this._role === 'admin' && this._teamSel && this._teamSel !== 'all') {
      pushField('team_id', String(this._teamSel));
    }
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
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${rec.fileName || '安全日活动记录.docx'}`,
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
          filePath: res.filePath,
          fileType: 'docx',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => this.setData({ openingId: '' }),
    });
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
