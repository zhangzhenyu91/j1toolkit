// 树障隐患记录：两级视图（view 状态机；视图切换整块 view-pane 淡入过渡）——
//   lines  线路视图：看板四格 + 搜索框（输入即过滤）+ 线路大卡片流（状态着色：要紧>0 红字、=0 灰字 / 未处理>0 品牌蓝 / 清零灰；
//           排序：要紧降序 > 未处理降序 > 电压降序 > 名称升序，未匹配卡排尾；stagger 入场）；
//   ledger 台账视图：返回行 + 线路标题（未处理/要紧小字）+ 记录卡片流（后端已按 未处理在前已处理在后>区段升序 排好）。
//           记录卡默认折叠一卡两行（区段+垂距双大字左对齐 / 树种×数量·水平距离·发现时间），每卡一枚折叠/展开开关（点卡同效），
//           辅证照片条折叠态即展示（有图才显示、只读、点图预览），展开出其他信息、处理信息、处理前/后照片区（可上传可删）
//           与 处理/编辑/删除 按钮（编辑与删除限班管/超管）——下一级详情页已取消，全部信息卡内承载；批量模式开关与批量处理条仅本视图可用（批量模式点击 = 勾选，已处理禁选）。
// 右下角全局悬浮导出 FAB：范围单选（全部线路 / 当前线路，线路视图为「选择线路」）+ 按线路分表开关（split=1，仅全部可勾）。
// 班管/超管另有手动新增（两级视图工具行）与阈值设置、内网同步（线路视图工具行，图标化）；手动编辑入口在卡片展开区——
// 新增/编辑共用同一表单弹层（发现 12 列 + 处理 3 列，线路名必填、校验失败不丢输入，成功后整表重拉）。
// 班组口径：仅超管按出工日志主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；其余角色后端强制本班，不传
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request, uploadFile } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';
import { pad } from '../../../utils/util';
import { iconColors, watchTheme, unwatchTheme } from '../../../utils/theme';

const API_BASE = '/api/v1/treehazard';

// 记录来源文案（treehazard_record.source：crane 直推 / staging 暂存区导入 / intranet 内网回写 / manual 手动录入）
const SOURCE_TEXT = {
  crane: '排查直推',
  staging: '暂存导入',
  intranet: '内网回写',
  manual: '手动录入',
};

// 空值展示占位
function dash(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  return s || '—';
}

// 'YYYY-MM-DD' → 'YYYY.MM.DD'（展示口径）
function fmtDot(s) {
  return s ? String(s).slice(0, 10).replace(/-/g, '.') : '';
}

// 电压等级幂等显示：纯数字（如 110）→ 拼 kV；已含 kV 后缀（110kV）→ 原样；空 → '—'
function fmtKv(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(\.\d+)?$/.test(s) ? `${s}kV` : s;
}

// 今日 'YYYY-MM-DD'
function todayStr() {
  const d = new Date();
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// 记录 → 展示结构：日期点分文本 / 垂直距离展示文本与超阈值标记 / 照片按 kind 分区（pz，供卡片展开区渲染）
// / 折叠态两行摘要之外的「其他信息」字段（moreFields，展开才渲染；原详情页字段并入卡片）
// 垂直距离展示文本（幂等 m 后缀：纯数字拼 m、已含单位或其他文本原样、空回退 vertical_num 再兜底 '—'）
function fmtVertical(r, vnum) {
  const s = String(r.vertical === null || r.vertical === undefined ? '' : r.vertical).trim();
  if (!s) return vnum === null ? '—' : `${vnum}m`;
  return /^\d+(\.\d+)?$/.test(s) ? `${s}m` : s;
}

function mapRecord(r, threshold) {
  const photos = Array.isArray(r.photos) ? r.photos : [];
  const pz = { evidence: [], before: [], after: [] };
  for (const p of photos) {
    if (pz[p.kind]) pz[p.kind].push(p);
  }
  const vnum =
    r.vertical_num === null || r.vertical_num === undefined ? null : Number(r.vertical_num);
  return Object.assign({}, r, {
    foundAtText: fmtDot(r.found_at),
    handledAtText: fmtDot(r.handled_at),
    verticalText: fmtVertical(r, vnum),
    overThreshold: threshold !== null && vnum !== null && vnum <= threshold,
    pz,
    moreFields: [
      { label: '位置说明', value: dash(r.position_note) },
      { label: '直径(cm)', value: dash(r.diameter) },
      { label: '距小号(m)', value: dash(r.dist_small) },
      { label: '距大号(m)', value: dash(r.dist_big) },
      { label: '发现人', value: dash(r.finder_name) },
      { label: '来源', value: SOURCE_TEXT[r.source] || dash(r.source) },
    ],
  });
}

// blankEditForm 手动新增/编辑表单初值（*_text 为日期显示口径，提交不带）
function blankEditForm() {
  return {
    line_name: '',
    voltage_level: '',
    section: '',
    position_note: '',
    tree_species: '',
    tree_count: '',
    dist_small: '',
    dist_big: '',
    diameter: '',
    horizontal: '',
    vertical: '',
    finder_name: '',
    found_at: '',
    found_at_text: '',
    handler_name: '',
    handle_note: '',
    handled_at: '',
    handled_at_text: '',
  };
}

Page({
  data: {
    ico: iconColors('light'), // t-icon 计算色（随系统深浅色刷新）
    gate: false, // 门控（参照 quiz bank 页 gate 模式）
    canManage: false, // 班管/超管（阈值设置 / 内网同步入口可见）
    view: 'lines', // 视图状态机：lines=线路卡片 / ledger=线路台账
    // 看板（仅线路视图显示）
    dash: { unhandled: 0, week_new: 0, over_threshold: 0, handled: 0, threshold: null },
    // 记录（threshold 供线路卡「要紧」数与网格红字判断）
    threshold: null,
    loading: true,
    // 线路视图：搜索与线路卡片
    searchKey: '',
    lineCards: [], // [{key, lineName, title, total, unhandled, urgent}]（urgent 为 null = 未设阈值，不显要紧行；key '_unmatched' 为未匹配组）
    // 台账视图：当前线路
    curKey: '',
    curTitle: '',
    curTotal: 0,
    curUnhandled: 0,
    curUrgent: null,
    curUnmatched: false,
    groups: [], // 当前线路单组 [{key, title, records}]（后端已排序，直接渲染）
    // 批量模式（仅台账视图）
    batchMode: false,
    selected: {}, // 记录 id → true
    selectedCount: 0,
    // 卡片折叠/展开（记录 id → true=展开；默认折叠一卡两行，详情页已取消）
    expanded: {},
    // 处理弹层（卡内单条与批量共用）
    handleVisible: false,
    handleIds: [],
    handleName: '',
    handleNote: '',
    handleDate: '', // YYYY-MM-DD（提交口径）
    handleDateText: '', // yyyy.MM.dd（显示口径）
    handling: false,
    // 手动新增/编辑弹层（统一表单：editId=0 为新增；校验失败不丢输入）
    editVisible: false,
    editId: 0,
    editForm: {},
    editSaving: false,
    // 处理时间日历（单选，点日期即选中并自动关闭）
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
    // 阈值弹层
    thVisible: false,
    thValue: '',
    thSaving: false,
    // 同步弹层
    syncVisible: false,
    syncStatus: null,
    syncRunning: false,
    syncText: '',
    syncLastText: '',
    // 导出（FAB 弹层）
    expVisible: false,
    expScope: 'all', // all=全部线路 / line=单条线路（台账视图默认当前线路；线路视图为「选择线路」）
    expLine: '', // 线路视图下「选择线路」的目标线路名
    expLineOptions: [], // 可选线路（未匹配组不可按线路导出，不入列）
    expSplit: false, // 按线路分表（URL 拼 split=1；仅范围为全部线路时可勾）
    exporting: false,
  },

  onShow() {
    this.setData({ ico: iconColors() });
    watchTheme(this);
  },

  onLoad() {
    // gate 兜底：本页由已门控的首页进入，此处仅保证登录态就绪
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

  async passGate() {
    if (this.data.gate) return;
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    this._role = user.role || 'user';
    this._userId = Number(user.id) || 0; // 当前用户 id（照片「仅上传者本人可删」判定用）
    const now = new Date();
    this.setData({
      gate: true,
      canManage: this._role === 'admin' || this._role === 'team_admin',
      // 处理时间可选区间：去年今日 ~ 三个月后（同出工日志 sheet 页口径；不设则组件默认今天起选不了历史日期）
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
    await this.initTeam(user);
    this.loadAll();
  },

  onUnload() {
    unwatchTheme(this);
    this.stopSyncPoll();
  },

  onPullDownRefresh() {
    if (!this.data.gate) {
      wx.stopPullDownRefresh();
      return;
    }
    Promise.all([this.loadDashboard(), this.loadRecords()]).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 生效班组与班组名（导出文件名用）：超管读切换器存下的 team_id 并经 /admin/teams 解析名称
  // （解析口径与后端 resolveTeam 一致：指定失效 → 自己班组 → 第一个启用班组）；其余角色取本人班组名
  async initTeam(user) {
    this._teamId = 0;
    this._teamName = user.team || '';
    if (this._role !== 'admin') return;
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0;
    try {
      const data = await request({ url: '/api/v1/admin/teams' });
      const teams = ((data && data.list) || []).filter((t) => t.status === 1);
      const cur =
        teams.find((t) => t.id === this._teamId) ||
        teams.find((t) => t.id === Number(user.team_id)) ||
        teams[0];
      if (cur) {
        this._teamId = cur.id;
        this._teamName = cur.name;
      }
    } catch (err) {
      // 班组名仅用于导出文件名，拉取失败不阻塞页面
    }
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST/PUT JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  /* ==================== 数据加载 ==================== */

  loadAll() {
    this.loadDashboard();
    this.loadRecords();
    if (this.data.canManage) this.loadSyncStatus();
  },

  async loadDashboard() {
    try {
      const data = await request({ url: `${API_BASE}/dashboard${this.teamQuery('?')}` });
      if (data) this.setData({ dash: data });
    } catch (err) {
      this.toast(err.message);
    }
  },

  async loadRecords() {
    try {
      const data = await request({ url: `${API_BASE}/records${this.teamQuery('?')}` });
      const d = data || {};
      this._lines = Array.isArray(d.lines) ? d.lines : [];
      this._unmatched = Array.isArray(d.unmatched) ? d.unmatched : [];
      this.setData({
        threshold: d.threshold === null || d.threshold === undefined ? null : Number(d.threshold),
        loading: false,
      });
      this.rebuild();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 组内统计：未处理数与「要紧」数（status=unhandled 且 vertical_num 非空且 ≤ 阈值；阈值为 null 时 urgent 返回 null，线路卡不显要紧行）
  lineStats(records) {
    const threshold = this.data.threshold;
    let unhandled = 0;
    let urgent = 0;
    for (const r of records) {
      if (r.status !== 'unhandled') continue;
      unhandled += 1;
      if (
        threshold !== null &&
        r.vertical_num !== null &&
        r.vertical_num !== undefined &&
        Number(r.vertical_num) <= threshold
      ) {
        urgent += 1;
      }
    }
    return { unhandled, urgent: threshold === null ? null : urgent };
  },

  // 重建两侧视图（后端已按线路顺序排好；线路卡按搜索词过滤，台账视图重建当前线路单组）
  rebuild() {
    this.rebuildLines();
    if (this.data.view === 'ledger') this.rebuildLedger();
  },

  // 线路卡片流：全部运维线路（含 0 条记录的线路）+ 未匹配组（有记录才成卡）；搜索词命中标题（电压+名称）即保留
  // 排序：要紧降序 > 未处理降序 > 电压等级降序（数值）> 线路名称升序；未匹配卡恒排尾
  // 状态着色（stateClass）：要紧>0 红 / 未处理>0 品牌蓝 / 均为 0 置灰；「要紧」计数本身仅 >0 红字，=0 灰字
  rebuildLines() {
    const kw = (this.data.searchKey || '').trim();
    const cards = [];
    for (const l of this._lines || []) {
      const records = l.records || [];
      const st = this.lineStats(records);
      const vm = String(
        l.voltage_level === null || l.voltage_level === undefined ? '' : l.voltage_level
      ).match(/\d+(\.\d+)?/);
      cards.push({
        key: `L_${l.line_name}`,
        lineName: l.line_name,
        title: `${fmtKv(l.voltage_level)} ${l.line_name}`,
        total: records.length,
        unhandled: st.unhandled,
        urgent: st.urgent, // 未设阈值时为 null（不显要紧行），排序按 0 计
        volt: vm ? parseFloat(vm[0]) : 0,
        stateClass: (st.urgent || 0) > 0 ? 's-bad' : st.unhandled > 0 ? 's-info' : 's-ok',
      });
    }
    cards.sort(
      (a, b) =>
        (b.urgent || 0) - (a.urgent || 0) ||
        b.unhandled - a.unhandled ||
        b.volt - a.volt ||
        String(a.lineName).localeCompare(String(b.lineName), 'zh-CN')
    );
    if ((this._unmatched || []).length) {
      const st = this.lineStats(this._unmatched);
      cards.push({
        key: '_unmatched',
        lineName: '',
        title: '未匹配线路',
        total: this._unmatched.length,
        unhandled: st.unhandled,
        urgent: st.urgent,
        volt: 0,
        stateClass: (st.urgent || 0) > 0 ? 's-bad' : st.unhandled > 0 ? 's-info' : 's-ok',
      });
    }
    this._cards = cards;
    this.setData({
      lineCards: kw ? cards.filter((c) => c.title.indexOf(kw) !== -1) : cards,
      expLineOptions: cards.filter((c) => c.key !== '_unmatched'),
    });
  },

  // 台账视图：当前线路单组记录卡片 + 标题行计数刷新（线路被同步/删除后记录可能清空，组空态兜底）
  rebuildLedger() {
    const threshold = this.data.threshold;
    let records;
    if (this.data.curUnmatched) {
      records = this._unmatched || [];
    } else {
      const line = (this._lines || []).find((l) => l.line_name === this._curLineName);
      records = (line && line.records) || [];
    }
    const st = this.lineStats(records);
    this.setData({
      groups: [
        {
          key: this.data.curKey,
          title: this.data.curTitle,
          records: records.map((r) => mapRecord(r, threshold)),
        },
      ],
      curTotal: records.length,
      curUnhandled: st.unhandled,
      curUrgent: st.urgent,
    });
  },

  /* ==================== 线路视图（搜索 / 线路卡） ==================== */

  onSearchInput(e) {
    this.setData({ searchKey: e.detail.value });
    this.rebuildLines();
  },

  onSearchClear() {
    this.setData({ searchKey: '' });
    this.rebuildLines();
  },

  // 点线路卡 → 台账视图（未匹配组 lineName 为空，单独标记；进入时重置批量模式并回滚到顶部）
  onLineCardTap(e) {
    const card = (this._cards || []).find((c) => c.key === e.currentTarget.dataset.key);
    if (!card) return;
    this._curLineName = card.lineName;
    this.setData({
      view: 'ledger',
      curKey: card.key,
      curTitle: card.title,
      curUnmatched: card.key === '_unmatched',
      batchMode: false,
      selected: {},
      selectedCount: 0,
    });
    this.rebuildLedger();
    wx.pageScrollTo({ scrollTop: 0 });
  },

  onBackLines() {
    this.setData({ view: 'lines', batchMode: false, selected: {}, selectedCount: 0 });
  },

  /* ==================== 批量模式（仅台账视图） ==================== */

  onToggleBatch() {
    const on = !this.data.batchMode;
    this.setData({ batchMode: on, selected: {}, selectedCount: 0 });
  },

  // 卡片点击：批量模式 = 勾选（已处理禁选，口径同 rec-ck）；否则就地折叠/展开该卡（下一级详情页已取消）
  onCardTap(e) {
    const { id, status } = e.currentTarget.dataset;
    if (this.data.batchMode) {
      if (status === 'handled') {
        this.toast('已处理记录无需选择');
        return;
      }
      const selected = Object.assign({}, this.data.selected);
      if (selected[id]) {
        delete selected[id];
      } else {
        selected[id] = true;
      }
      this.setData({ selected, selectedCount: Object.keys(selected).length });
      return;
    }
    this.toggleFold(id);
  },

  // 折叠/展开开关（每卡一枚，catchtap 防冒泡；点卡同效）：默认折叠一卡两行摘要
  onFoldToggle(e) {
    this.toggleFold(e.currentTarget.dataset.id);
  },

  toggleFold(id) {
    if (id === undefined || id === null || id === '') return;
    const expanded = Object.assign({}, this.data.expanded);
    if (expanded[id]) {
      delete expanded[id];
    } else {
      expanded[id] = true;
    }
    this.setData({ expanded });
  },

  // 卡内「处理」按钮（未处理卡展开区；catchtap 防冒泡）：复用处理弹层，单 id 走 /records/:id/handle
  onCardHandle(e) {
    const { id } = e.currentTarget.dataset;
    if (!id) return;
    this.openHandle([Number(id)]);
  },

  /* ==================== 手动新增 / 编辑（统一表单弹层，班管/超管） ==================== */

  // 打开新增表单：台账视图预填当前线路（未匹配组无线路名，留空由用户填写）
  onOpenCreate() {
    if (!this.data.canManage) return;
    const form = blankEditForm();
    if (this.data.view === 'ledger' && !this.data.curUnmatched)
      form.line_name = this._curLineName || '';
    this.setData({ editVisible: true, editId: 0, editForm: form, editSaving: false });
  },

  // 卡内「编辑」按钮（展开区；catchtap 防冒泡）：预填该卡当前值
  onCardEdit(e) {
    const { id } = e.currentTarget.dataset;
    if (!this.data.canManage || !id) return;
    const rec = this.findRecord(id);
    if (!rec) return;
    const form = blankEditForm();
    Object.keys(form).forEach((k) => {
      if (k.slice(-5) === '_text') return;
      form[k] = rec[k] === null || rec[k] === undefined ? '' : String(rec[k]);
    });
    form.found_at_text = fmtDot(form.found_at);
    form.handled_at_text = fmtDot(form.handled_at);
    this.setData({ editVisible: true, editId: Number(id), editForm: form, editSaving: false });
  },

  onCloseEdit() {
    if (this.data.editSaving) return;
    this.setData({ editVisible: false });
  },

  onEditVisibleChange(e) {
    if (!e.detail.visible && !this.data.editSaving) this.setData({ editVisible: false });
  },

  // 表单字段输入（data-key 对应业务列名）
  onEditField(e) {
    const { key } = e.currentTarget.dataset;
    const editForm = Object.assign({}, this.data.editForm);
    editForm[key] = e.detail.value;
    this.setData({ editForm });
  },

  // 发现/处理时间：复用单选日历（_calTarget 记录目标字段，与处理时间互不干扰）
  onEditDateTap(e) {
    const { key } = e.currentTarget.dataset;
    const cur = (this.data.editForm[key] || todayStr()).slice(0, 10);
    this._calTarget = key;
    this.setData({ calVisible: true, calValue: new Date(`${cur}T00:00:00`).getTime() });
  },

  // 提交：editId=0 走 POST /records，否则 PUT /records/:id；校验/保存失败弹层与输入保留，成功整表重拉
  async onSubmitEdit() {
    if (this.data.editSaving) return;
    const form = this.data.editForm || {};
    if (!String(form.line_name || '').trim()) {
      this.toast('线路名称不能为空');
      return;
    }
    this.setData({ editSaving: true });
    const body = {};
    Object.keys(blankEditForm()).forEach((k) => {
      if (k.slice(-5) === '_text') return;
      body[k] = String(form[k] || '').trim();
    });
    try {
      if (this.data.editId) {
        await request({
          url: `${API_BASE}/records/${this.data.editId}${this.teamQuery('?')}`,
          method: 'PUT',
          data: this.teamBody(body),
        });
        this.toast('已保存');
      } else {
        await request({
          url: `${API_BASE}/records${this.teamQuery('?')}`,
          method: 'POST',
          data: this.teamBody(body),
        });
        this.toast('已新增');
      }
      this.setData({ editVisible: false });
      this.loadAll();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ editSaving: false });
    }
  },

  /* ==================== 照片（辅证条折叠态常驻只读预览 / 展开区处理前后上传与长按删除） ==================== */

  // 按记录 id 取当前台账记录（照片、处理等卡内操作共用）
  findRecord(id) {
    for (const g of this.data.groups || []) {
      const rec = (g.records || []).find((r) => Number(r.id) === Number(id));
      if (rec) return rec;
    }
    return null;
  },

  // 点击缩略图预览（urls = 该区全部照片地址）
  onPhotoPreview(e) {
    const { id, kind, pi } = e.currentTarget.dataset;
    const rec = this.findRecord(id);
    if (!rec) return;
    const urls = ((rec.pz && rec.pz[kind]) || []).map((p) => p.url);
    if (!urls.length) return;
    wx.previewImage({ urls, current: urls[Number(pi) || 0] });
  },

  // 「+」格：wx.chooseMedia 多选原图（处理前/后照片大多不止一张）→ 逐张 multipart 上传
  onAddPhoto(e) {
    const { id, kind } = e.currentTarget.dataset;
    if (this._uploading || !id) return;
    wx.chooseMedia({
      count: 9,
      mediaType: ['image'],
      sizeType: ['original'],
      sourceType: ['album', 'camera'],
      success: (res) => {
        const files = res.tempFiles || [];
        if (files.length) this.uploadPhotos(Number(id), kind, files);
      },
    });
  },

  // 多选逐张上传（进度 n/m），完成后整表重拉（照片区随 loadAll 同步）
  async uploadPhotos(recordId, kind, files) {
    this._uploading = true;
    let ok = 0;
    let failed = 0;
    wx.showLoading({ title: `上传中 0/${files.length}`, mask: true });
    for (const f of files) {
      try {
        if (f.size && f.size > 50 * 1024 * 1024) throw new Error('照片大小应在 50MB 以内');
        await uploadFile({
          url: `${API_BASE}/records/${recordId}/photos?kind=${kind}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
          filePath: f.tempFilePath,
          name: 'image',
          data: this.teamBody({}),
          timeout: 120000,
        });
        ok += 1;
      } catch (err) {
        failed += 1;
      }
      wx.showLoading({ title: `上传中 ${ok + failed}/${files.length}`, mask: true });
    }
    wx.hideLoading();
    this._uploading = false;
    if (!ok && failed) this.toast('上传失败，请重试');
    else if (failed) this.toast(`上传完成：成功 ${ok} 张，失败 ${failed} 张`);
    else this.toast(`已上传 ${ok} 张`);
    this.loadAll();
  },

  // 长按缩略图删除照片（仅处理前/后区绑定；辅证区只读，与网页端对齐）：
  // 仅上传者本人（photo.uploaded_by === 当前用户 id）或班管/超管可删，不满足时长按静默无反应
  onPhotoLongPress(e) {
    const { id, pid, uploadedBy } = e.currentTarget.dataset;
    const mine = Number(uploadedBy) > 0 && Number(uploadedBy) === this._userId;
    if (!mine && !this.data.canManage) return; // 无权限：保持安静
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除照片',
      content: '删除这张照片？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({
            url: `${API_BASE}/records/${id}/photos/${pid}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          this.toast('已删除');
          this.loadAll();
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  // 删除记录（班管/超管，展开区按钮）：删记录与照片、写墓碑，成功后整表重拉
  onDeleteRecord(e) {
    const { id } = e.currentTarget.dataset;
    if (!this.data.canManage || !id) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除记录',
      content: '删除后不可恢复，记录内照片一并删除，确定删除吗？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({
            url: `${API_BASE}/records/${id}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          this.toast('已删除');
          this.loadAll();
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  onOpenBatchHandle() {
    const ids = Object.keys(this.data.selected).map(Number);
    if (!ids.length) {
      this.toast('请先勾选记录');
      return;
    }
    this.openHandle(ids);
  },

  /* ==================== 处理（卡内单条 / 批量共用弹层） ==================== */

  // 打开处理弹层：处理人预填当前用户昵称，处理时间默认今日
  openHandle(ids) {
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    const today = todayStr();
    this.setData({
      handleVisible: true,
      handleIds: ids,
      handleName: user.nickname || '',
      handleNote: '',
      handleDate: today,
      handleDateText: fmtDot(today),
    });
  },

  onCloseHandle() {
    if (this.data.handling) return;
    this.setData({ handleVisible: false });
  },

  onHandleVisibleChange(e) {
    if (!e.detail.visible && !this.data.handling) this.setData({ handleVisible: false });
  },

  onHandleName(e) {
    this.setData({ handleName: e.detail.value });
  },

  onHandleNote(e) {
    this.setData({ handleNote: e.detail.value });
  },

  // 快捷标签（削枝 / 砍伐）：点亮填入处理情况，再点取消；手工输入后标签自动失去选中
  onQuickTag(e) {
    const word = e.currentTarget.dataset.word;
    const checked = e.detail.checked;
    this.setData({ handleNote: checked ? word : '' });
  },

  // 处理时间：展开日历（单选）
  onOpenCal() {
    this.setData({
      calVisible: true,
      calValue: new Date(`${this.data.handleDate}T00:00:00`).getTime(),
    });
  },

  // 单选日历：点日期即选中并自动关闭（confirm-btn 已置空）；_calTarget 指向手动表单日期字段时写表单
  onCalChange(e) {
    const value = e.detail.value;
    if (!value) return;
    const d = new Date(value);
    const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    this.setData({ calVisible: false });
    if (this._calTarget) {
      const editForm = Object.assign({}, this.data.editForm);
      editForm[this._calTarget] = date;
      editForm[`${this._calTarget}_text`] = fmtDot(date);
      this._calTarget = '';
      this.setData({ editForm });
      return;
    }
    this.setData({ handleDate: date, handleDateText: fmtDot(date) });
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  // 确认处理：单条走 /records/:id/handle，批量走 /records/handle-batch；成功后退出批量模式并整页刷新
  async onConfirmHandle() {
    if (this.data.handling) return;
    const note = (this.data.handleNote || '').trim();
    if (!note) {
      this.toast('请填写处理情况');
      return;
    }
    const ids = this.data.handleIds;
    if (!ids.length) return;
    this.setData({ handling: true });
    try {
      const body = this.teamBody({
        handler_name: (this.data.handleName || '').trim(),
        handle_note: note,
        handled_at: this.data.handleDate,
      });
      if (ids.length === 1) {
        await request({ url: `${API_BASE}/records/${ids[0]}/handle`, method: 'POST', data: body });
        this.toast('已处理');
      } else {
        const data = await request({
          url: `${API_BASE}/records/handle-batch`,
          method: 'POST',
          data: Object.assign({}, body, { ids }),
        });
        this.toast(`已处理 ${data && data.updated !== undefined ? data.updated : ids.length} 条`);
      }
      this.setData({ handleVisible: false, batchMode: false, selected: {}, selectedCount: 0 });
      this.loadAll();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ handling: false });
    }
  },

  /* ==================== 阈值设置（班管/超管） ==================== */

  onOpenThreshold() {
    this.setData({
      thVisible: true,
      thValue: this.data.threshold === null ? '' : String(this.data.threshold),
    });
  },

  onCloseThreshold() {
    if (this.data.thSaving) return;
    this.setData({ thVisible: false });
  },

  onThVisibleChange(e) {
    if (!e.detail.visible && !this.data.thSaving) this.setData({ thVisible: false });
  },

  onThInput(e) {
    this.setData({ thValue: e.detail.value });
  },

  // 保存阈值：留空 = 清除（后端口径 vertical_threshold 可空）
  async onSaveThreshold() {
    if (this.data.thSaving) return;
    const raw = (this.data.thValue || '').trim();
    if (raw && (!/^\d+(\.\d+)?$/.test(raw) || Number(raw) < 0)) {
      this.toast('阈值应为非负数字');
      return;
    }
    this.setData({ thSaving: true });
    try {
      await request({
        url: `${API_BASE}/settings`,
        method: 'PUT',
        data: this.teamBody({ vertical_threshold: raw }),
      });
      this.toast(raw ? '阈值已保存' : '阈值已清除');
      this.setData({ thVisible: false });
      this.loadAll(); // 看板、线路卡「要紧」数与网格红字均随阈值变化
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ thSaving: false });
    }
  },

  /* ==================== 内网同步（班管/超管；后端为占位实现，字段缺失按未启用/空闲容错） ==================== */

  onOpenSync() {
    this.setData({ syncVisible: true });
    this.loadSyncStatus();
  },

  onCloseSync() {
    this.setData({ syncVisible: false });
  },

  onSyncVisibleChange(e) {
    if (!e.detail.visible) {
      this.setData({ syncVisible: false });
      this.stopSyncPoll();
    }
  },

  async loadSyncStatus() {
    try {
      const data = await request({ url: `${API_BASE}/sync/status${this.teamQuery('?')}` });
      const st = data || {};
      const running = st.running === true;
      const last = st.last || null;
      this.setData({
        syncStatus: st,
        syncRunning: running,
        syncText: running ? '同步中…' : st.enabled === false ? '同步模块未启用' : '空闲',
        syncLastText: last ? `${last.created_at || ''} ${last.result || ''}`.trim() : '',
      });
      // 弹层开着且仍在同步 → 保证轮询存活（中途关弹层会停轮询，重开时由此恢复）；空闲即停
      if (running && this.data.syncVisible) {
        this.startSyncPoll();
      } else {
        this.stopSyncPoll();
      }
    } catch (err) {
      this.setData({ syncText: '状态获取失败', syncRunning: false });
      this.stopSyncPoll();
    }
  },

  // 立即同步：push-now 成功后 1.5s 轮询直至 running=false（当前后端未启用时 503 原文 toast）
  async onSyncNow() {
    if (this.data.syncRunning || this._syncPushing) return;
    this._syncPushing = true;
    try {
      await request({ url: `${API_BASE}/sync/push-now`, method: 'POST', data: this.teamBody({}) });
      this.setData({ syncRunning: true, syncText: '同步中…' });
      this.startSyncPoll();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this._syncPushing = false;
    }
  },

  startSyncPoll() {
    this.stopSyncPoll();
    this._syncTimer = setInterval(async () => {
      const wasRunning = this.data.syncRunning;
      await this.loadSyncStatus();
      if (wasRunning && !this.data.syncRunning) {
        this.toast('同步完成');
        this.loadAll();
      }
    }, 1500);
  },

  stopSyncPoll() {
    if (this._syncTimer) {
      clearInterval(this._syncTimer);
      this._syncTimer = null;
    }
  },

  /* ==================== 导出统计表（FAB 弹层） ==================== */

  // 打开导出弹层：台账视图默认「当前线路」（未匹配组不可按线路导出，回落全部）；线路视图默认「全部线路」
  onOpenExport() {
    const line = this.data.view === 'ledger' && !this.data.curUnmatched ? this._curLineName : '';
    const options = this.data.expLineOptions;
    this.setData({
      expVisible: true,
      expScope: line ? 'line' : 'all',
      expLine: line || (options.length ? options[0].lineName : ''),
      expSplit: false,
    });
  },

  onCloseExport() {
    if (this.data.exporting) return;
    this.setData({ expVisible: false });
  },

  onExpVisibleChange(e) {
    if (!e.detail.visible && !this.data.exporting) this.setData({ expVisible: false });
  },

  // 范围单选：未匹配组台账无「当前线路」项可点；线路视图选「选择线路」时确保有默认线路
  onExpScopeTap(e) {
    const scope = e.currentTarget.dataset.scope;
    if (scope === this.data.expScope) return;
    if (scope === 'line' && this.data.view === 'ledger' && this.data.curUnmatched) return;
    const patch = { expScope: scope };
    if (scope === 'line' && !this.data.expLine && this.data.expLineOptions.length) {
      patch.expLine = this.data.expLineOptions[0].lineName;
    }
    this.setData(patch);
  },

  onExpLineTap(e) {
    this.setData({ expLine: e.currentTarget.dataset.line });
  },

  onExpSplit(e) {
    this.setData({ expSplit: !!e.detail.value });
  },

  // 确认导出：单线路带 line 参数；勾选分表且范围为全部时拼 split=1（后端并行开发中，未上线时忽略该参数）
  // 文件名口径同后端 exportFileName：{线路名或班组名}树木统计表.xlsx
  onConfirmExport() {
    if (this.data.exporting) return;
    const scopeAll = this.data.expScope === 'all';
    let line = '';
    if (!scopeAll) {
      line =
        this.data.view === 'ledger' && !this.data.curUnmatched
          ? this._curLineName
          : this.data.expLine;
      if (!line) {
        this.toast('请选择线路');
        return;
      }
    }
    const fileName = `${line || this._teamName || '树木统计表'}树木统计表.xlsx`;
    const q = [`line=${encodeURIComponent(line)}`];
    if (this._teamId) q.push(`team_id=${this._teamId}`);
    if (scopeAll && this.data.expSplit) q.push('split=1');
    this.setData({ exporting: true, expVisible: false });
    wx.showLoading({ title: '正在导出…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}${API_BASE}/export/${encodeURIComponent(fileName)}?${q.join('&')}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast('导出失败，请稍后重试');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'xlsx',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ exporting: false });
      },
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'tree-hazard', title: '树障隐患记录' });
  },
});
