// 交跨记录：两级视图（view 状态机；视图切换整块 view-pane 淡入过渡）——
//   lines  线路视图：看板四格 + 搜索框（输入即过滤）+ 线路大卡片流（状态着色：隐患>0 红 / 有记录品牌蓝 / 清零灰；
//           排序：隐患降序 > 电压降序 > 名称升序，未匹配卡排尾；stagger 入场）；
//   ledger 台账视图：返回行 + 线路标题（隐患小字）+ 记录卡片流（后端已按区段升序排好）。
//           记录卡默认折叠一卡两行（区段+垂距双大字左对齐 / 跨越类型·交叉角），每卡一枚折叠/展开开关（点卡同效），
//           展开出全字段（区段/类型/距小大号侧/边线水平/垂直距离/交叉角/隐患/发现人/巡视时间/来源/备注）与删除按钮
//           （班管/超管，t-dialog 确认）——无处理流程、无照片、无人工新增编辑。
// 右下角全局悬浮导出 FAB：范围单选（全部线路 / 当前线路，线路视图为「选择线路」）+ 按线路分表开关（split=1，仅全部可勾）。
// 班组口径：仅超管按出工日志主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；其余角色后端强制本班，不传
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/crossing';

// 记录来源文案（crossing_record.source：crane 直推 / staging 暂存区导入 / intranet 内网回写）
const SOURCE_TEXT = { crane: '排查直推', staging: '暂存导入', intranet: '内网回写' };

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

// 距离幂等 m 后缀：纯数字拼 m、已含单位或其他文本原样、空 → '—'
function fmtM(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(\.\d+)?$/.test(s) ? `${s}m` : s;
}

// 交叉角幂等 ° 后缀：纯数字拼 °、空 → '—'
function fmtDeg(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(\.\d+)?$/.test(s) ? `${s}°` : s;
}

// 记录 → 展示结构：巡视时间日期点分文本 / 垂直距离与交叉角展示文本 / 展开区全字段（moreFields，展开才渲染）
function mapRecord(r) {
  return Object.assign({}, r, {
    verticalText: fmtM(r.cross_vertical),
    angleText: fmtDeg(r.cross_angle),
    moreFields: [
      { label: '跨越区段', value: dash(r.section) },
      { label: '跨越类型', value: dash(r.crossing_type) },
      { label: '距小号侧', value: fmtM(r.dist_small) },
      { label: '距大号侧', value: fmtM(r.dist_big) },
      { label: '边线水平', value: fmtM(r.cross_horizontal) },
      { label: '垂直距离', value: fmtM(r.cross_vertical) },
      { label: '交叉角', value: fmtDeg(r.cross_angle) },
      { label: '是否存在隐患', value: dash(r.has_hazard) },
      { label: '发现人', value: dash(r.finder_name) },
      { label: '巡视时间', value: fmtDot(r.inspect_at) || '—' },
      { label: '来源', value: SOURCE_TEXT[r.source] || dash(r.source) },
      { label: '备注', value: dash(r.remark) },
    ],
  });
}

Page({
  data: {
    gate: false, // 门控（参照树障页 gate 模式）
    canManage: false, // 班管/超管（删记录入口可见）
    view: 'lines', // 视图状态机：lines=线路卡片 / ledger=线路台账
    // 看板（仅线路视图显示）：记录总数 / 本周新增 / 存在隐患数 / 待确认线路数
    dash: { total: 0, week_new: 0, hazard: 0, pending_lines: 0 },
    loading: true,
    // 线路视图：搜索与线路卡片
    searchKey: '',
    lineCards: [], // [{key, lineName, title, total, hazard}]（key '_unmatched' 为未匹配组）
    // 台账视图：当前线路
    curKey: '',
    curTitle: '',
    curTotal: 0,
    curHazard: 0,
    curUnmatched: false,
    groups: [], // 当前线路单组 [{key, title, records}]（后端已排序，直接渲染）
    // 卡片折叠/展开（记录 id → true=展开；默认折叠一卡两行）
    expanded: {},
    // 导出（FAB 弹层）
    expVisible: false,
    expScope: 'all', // all=全部线路 / line=单条线路（台账视图默认当前线路；线路视图为「选择线路」）
    expLine: '', // 线路视图下「选择线路」的目标线路名
    expLineOptions: [], // 可选线路（未匹配组不可按线路导出，不入列）
    expSplit: false, // 按线路分表（URL 拼 split=1；仅范围为全部线路时可勾）
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
    this.setData({
      gate: true,
      canManage: this._role === 'admin' || this._role === 'team_admin',
    });
    await this.initTeam(user);
    this.loadAll();
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
      this.setData({ loading: false });
      this.rebuild();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 组内统计：存在隐患数（has_hazard 非空）
  lineStats(records) {
    let hazard = 0;
    for (const r of records) {
      if (String(r.has_hazard === null || r.has_hazard === undefined ? '' : r.has_hazard).trim()) {
        hazard += 1;
      }
    }
    return { hazard };
  },

  // 重建两侧视图（后端已按线路顺序排好；线路卡按搜索词过滤，台账视图重建当前线路单组）
  rebuild() {
    this.rebuildLines();
    if (this.data.view === 'ledger') this.rebuildLedger();
  },

  // 线路卡片流：全部运维线路（含 0 条记录的线路）+ 未匹配组（有记录才成卡）；搜索词命中标题（电压+名称）即保留
  // 排序：隐患降序 > 电压等级降序（数值）> 线路名称升序；未匹配卡恒排尾
  // 状态着色（stateClass）：隐患>0 红 / 有记录品牌蓝 / 0 记录置灰；「隐患」计数本身仅 >0 红字，=0 灰字
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
        hazard: st.hazard,
        volt: vm ? parseFloat(vm[0]) : 0,
        stateClass: st.hazard > 0 ? 's-bad' : records.length > 0 ? 's-info' : 's-ok',
      });
    }
    cards.sort(
      (a, b) =>
        b.hazard - a.hazard ||
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
        hazard: st.hazard,
        volt: 0,
        stateClass: st.hazard > 0 ? 's-bad' : 's-info',
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
          records: records.map((r) => mapRecord(r)),
        },
      ],
      curTotal: records.length,
      curHazard: st.hazard,
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

  // 点线路卡 → 台账视图（未匹配组 lineName 为空，单独标记；进入时回滚到顶部）
  onLineCardTap(e) {
    const card = (this._cards || []).find((c) => c.key === e.currentTarget.dataset.key);
    if (!card) return;
    this._curLineName = card.lineName;
    this.setData({
      view: 'ledger',
      curKey: card.key,
      curTitle: card.title,
      curUnmatched: card.key === '_unmatched',
    });
    this.rebuildLedger();
    wx.pageScrollTo({ scrollTop: 0 });
  },

  onBackLines() {
    this.setData({ view: 'lines' });
  },

  /* ==================== 记录卡（折叠/展开 / 删除） ==================== */

  // 卡片点击：就地折叠/展开该卡
  onCardTap(e) {
    this.toggleFold(e.currentTarget.dataset.id);
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

  // 删除记录（班管/超管，展开区按钮）：删行写墓碑，成功后整表重拉
  onDeleteRecord(e) {
    const { id } = e.currentTarget.dataset;
    if (!this.data.canManage || !id) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除记录',
      content: '删除后不可恢复，确定删除吗？',
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

  // 确认导出：单线路带 line 参数；勾选分表且范围为全部时拼 split=1
  // 文件名口径同后端 exportFileName：{线路名或班组名}交跨统计表.xlsx
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
    const fileName = `${line || this._teamName || '交跨统计表'}交跨统计表.xlsx`;
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
    return shareAppMessage(this, { app: 'crossing', title: '交跨记录' });
  },
});
