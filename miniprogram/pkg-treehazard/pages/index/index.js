// 树障隐患记录：看板四格 + 线路筛选/批量/导出/阈值/同步工具行 + 按线路分组的记录卡片流
// 卡片精简口径（同出工日志卡片风格）：区段 + 状态点标 + 树种×数量/水平距离/垂直距离（≤阈值红显胶囊）三项，
// 点击卡片携记录进详情页（eventChannel 传递）；批量模式点击 = 勾选；班管/超管另有阈值设置与内网同步（删记录入口在详情页）。
// 班组口径：仅超管按出工日志主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；其余角色后端强制本班，不传
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';
import { pad } from '../../../utils/util';

const API_BASE = '/api/v1/treehazard';

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

// 记录 → 展示结构：日期点分文本 / 垂直距离超阈值标记 / 照片按 kind 分区（pz，供详情页直接渲染）
function mapRecord(r, threshold) {
  const photos = Array.isArray(r.photos) ? r.photos : [];
  const pz = { evidence: [], before: [], after: [] };
  for (const p of photos) {
    if (pz[p.kind]) pz[p.kind].push(p);
  }
  const vnum = r.vertical_num === null || r.vertical_num === undefined ? null : Number(r.vertical_num);
  return Object.assign({}, r, {
    foundAtText: fmtDot(r.found_at),
    handledAtText: fmtDot(r.handled_at),
    overThreshold: threshold !== null && vnum !== null && vnum <= threshold,
    pz,
  });
}

Page({
  data: {
    gate: false, // 门控（参照 quiz bank 页 gate 模式）
    canManage: false, // 班管/超管（阈值设置 / 内网同步入口可见）
    // 看板
    dash: { unhandled: 0, week_new: 0, over_threshold: 0, handled: 0, threshold: null },
    // 记录（threshold 供网格红字判断）
    threshold: null,
    groups: [], // [{key, title, records}]（后端已排序，筛选后渲染）
    lineOptions: [], // 筛选弹层选项（含条数，不随筛选变化）
    loading: true,
    // 线路筛选（''=全部）
    lineFilter: '',
    filterVisible: false,
    // 批量模式
    batchMode: false,
    selected: {}, // 记录 id → true
    selectedCount: 0,
    // 处理弹层（批量处理用；单条处理在详情页）
    handleVisible: false,
    handleIds: [],
    handleName: '',
    handleNote: '',
    handleDate: '', // YYYY-MM-DD（提交口径）
    handleDateText: '', // yyyy.MM.dd（显示口径）
    handling: false,
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
    // 导出
    exporting: false,
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
    this.stopSyncPoll();
  },

  // 从详情页返回（_fromDetail 在跳转前置位）：详情内可能已处理/删除记录或增删照片，重拉看板与列表
  onShow() {
    if (!this._fromDetail) return;
    this._fromDetail = false;
    this.loadDashboard();
    this.loadRecords();
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
      const cur = teams.find((t) => t.id === this._teamId)
        || teams.find((t) => t.id === Number(user.team_id))
        || teams[0];
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

  // 按当前筛选重建分组（后端已按线路顺序排好，直接渲染；当前筛选线路已不存在时回落全部）
  rebuild() {
    const threshold = this.data.threshold;
    let filter = this.data.lineFilter;
    if (filter && !(this._lines || []).some((l) => l.line_name === filter)) {
      filter = '';
      this.setData({ lineFilter: '' });
    }
    const groups = [];
    for (const l of this._lines || []) {
      if (filter && l.line_name !== filter) continue;
      groups.push({
        key: `L_${l.line_name}`,
        title: `${fmtKv(l.voltage_level)} ${l.line_name}`,
        records: (l.records || []).map((r) => mapRecord(r, threshold)),
      });
    }
    if (!filter && (this._unmatched || []).length) {
      groups.push({
        key: '_unmatched',
        title: '未匹配线路',
        records: this._unmatched.map((r) => mapRecord(r, threshold)),
      });
    }
    const lineOptions = (this._lines || []).map((l) => ({
      voltageText: fmtKv(l.voltage_level),
      line_name: l.line_name,
      count: (l.records || []).length,
    }));
    this.setData({ groups, lineOptions });
  },

  /* ==================== 线路筛选 ==================== */

  onOpenFilter() {
    this.setData({ filterVisible: true });
  },

  onFilterVisibleChange(e) {
    if (!e.detail.visible) this.setData({ filterVisible: false });
  },

  onPickLine(e) {
    const line = e.currentTarget.dataset.line || '';
    this.setData({ lineFilter: line, filterVisible: false });
    this.rebuild();
  },

  /* ==================== 批量模式 ==================== */

  onToggleBatch() {
    const on = !this.data.batchMode;
    this.setData({ batchMode: on, selected: {}, selectedCount: 0 });
  },

  // 卡片点击：批量模式 = 勾选（已处理禁选，口径同 rec-ck）；否则携记录对象进详情页（eventChannel 传递）
  onCardTap(e) {
    const { id, status, gi, ri } = e.currentTarget.dataset;
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
    const rec = this.data.groups[gi] && this.data.groups[gi].records[ri];
    if (!rec || Number(rec.id) !== Number(id)) return;
    this._fromDetail = true; // 返回时 onShow 重拉
    wx.navigateTo({
      url: '/pkg-treehazard/pages/detail/detail',
      success: (res) => {
        res.eventChannel.emit('record', rec);
        res.eventChannel.emit('ctx', { teamId: this._teamId });
      },
    });
  },

  onOpenBatchHandle() {
    const ids = Object.keys(this.data.selected).map(Number);
    if (!ids.length) {
      this.toast('请先勾选记录');
      return;
    }
    this.openHandle(ids);
  },

  /* ==================== 处理（批量；单条处理在详情页） ==================== */

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

  // 单选日历：点日期即选中并自动关闭（confirm-btn 已置空）
  onCalChange(e) {
    const value = e.detail.value;
    if (!value) return;
    const d = new Date(value);
    const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    this.setData({ calVisible: false, handleDate: date, handleDateText: fmtDot(date) });
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
      this.loadAll(); // 看板与网格红字均随阈值变化
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

  /* ==================== 导出统计表 ==================== */

  // 当前筛选为全部 → 全量导出；否则按 line 参数分线路导出（文件名口径同后端 exportFileName：{班组或线路}树木统计表.xlsx）
  onExport() {
    if (this.data.exporting) return;
    const line = this.data.lineFilter;
    const fileName = `${line || this._teamName || '树木统计表'}树木统计表.xlsx`;
    const q = [`line=${encodeURIComponent(line)}`];
    if (this._teamId) q.push(`team_id=${this._teamId}`);
    this.setData({ exporting: true });
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
