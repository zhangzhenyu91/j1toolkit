// 题库刷题 · 题库池（二级页；原题库列表页题库池板块迁入）
// GET /banks/pool（可见题库：全部池 + 本班组池；added 标记是否已加入我的题库）；
// 添加=POST join（防连点）/ 已加入→Dialog 确认移出=DELETE join（移出保留练习记录与错题）
// 登录门控同 index 页；班组口径：超管按 index 页切换器存下的 quiz_team_id 生效（请求带 team_id），
// 其余角色后端强制本班；非超管未分配班组 → 整页空态，不发业务请求
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';

Page({
  data: {
    gate: false, // 门控（参照 index 页 gate 模式）
    noTeam: false, // 非超管且未分配班组：整页空态，不发业务请求
    pool: [],
    loading: true,
  },

  onLoad() {
    // gate 兜底：本页由已门控的列表页进入，此处仅保证登录态就绪
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
    // 非超管且未分配班组：整页空态，不再发任何业务请求
    if (user.role !== 'admin' && !user.team) {
      this.setData({ gate: true, noTeam: true, loading: false });
      return;
    }
    // 生效班组：取题库列表页切换器存下的 team_id（仅超管真正生效；其余角色后端强制本班，带上也无妨）
    this._teamId = Number(wx.getStorageSync('quiz_team_id')) || 0;
    this.setData({ gate: true });
    this.loadPool(true);
  },

  onPullDownRefresh() {
    if (!this.data.gate || this.data.noTeam) {
      wx.stopPullDownRefresh();
      return;
    }
    this.loadPool(false).finally(() => wx.stopPullDownRefresh());
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 题库池列表 ==================== */

  async loadPool(isInitial) {
    if (isInitial) this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/banks/pool${this.teamQuery('?')}` });
      const pool = ((data && data.list) || []).map((b) => this.mapPool(b));
      this.setData({ pool, loading: false });
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 题库池项 → 展示结构（scope：all=全部池 / team=班组池；added 标记是否已加入我的题库）
  mapPool(b) {
    const an = b.analysis || {};
    const anDone = an.done || 0;
    const anPending = an.pending || 0;
    const anTotal = (an.none || 0) + anPending + (an.failed || 0) + anDone;
    return {
      id: b.id,
      name: b.name || '',
      scope: b.scope === 'all' ? 'all' : 'team',
      teamName: b.teamName || '班组池',
      questionCount: b.questionCount || 0,
      added: !!b.added,
      analyzing: anPending > 0,
      analyzingText: `AI 解析中 ${anDone}/${anTotal || b.questionCount || 0}`,
    };
  },

  /* ==================== 加入 / 移出我的题库 ==================== */

  // 「添加」：POST join 后刷新列表（个人行为，不带 team_id）
  onPoolJoin(e) {
    const { id } = e.currentTarget.dataset;
    if (this._joining) return; // 防连点
    this._joining = true;
    request({
      url: `${API_BASE}/banks/${encodeURIComponent(id)}/join`,
      method: 'POST',
    }).then(() => {
      this.toast('已加入我的题库');
      this.loadPool(false);
    }).catch((err) => this.toast(err.message))
      .finally(() => { this._joining = false; });
  },

  // 「已加入」：Dialog 确认（移出后练习记录与错题保留）→ DELETE join → 刷新
  onPoolLeave(e) {
    const { id, name } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: `移出题库「${name || ''}」？`,
      content: '移出后练习记录与错题保留，可随时从题库池重新添加。',
      confirmBtn: '确认移出',
      cancelBtn: '取消',
    }).then(() => {
      request({
        url: `${API_BASE}/banks/${encodeURIComponent(id)}/join`,
        method: 'DELETE',
      }).then(() => {
        this.toast('已移出我的题库');
        this.loadPool(false);
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库池' });
  },
});
