// 题库刷题 · 题库列表（app_key quiz；设计稿 design/quiz.html 屏 01）
// 统计卡（累计练习/正确率/错题待攻克）+ 错题本入口卡 + 我的题库列表（个人口径，GET /banks）+
// 题库池入口卡（二级页 pages/pool/pool，添加/移出后回本页 onShow 自动刷新）；
// 管理角色（admin/team_admin）另有「管理」入口与底部「新建题库」主按钮（进 manage 页）
// 班组口径（同 pkg-filetransfer）：超管顶部切换器切班组（storage quiz_team_id，banks 请求带 team_id）；
// 其余角色固定本班（后端强制）；非超管未分配班组 → 整页空态，不发业务请求
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';

Page({
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    // 班组切换器：超管可点 chip 下拉切换；其余角色为静态班组名标签
    isAdmin: false,
    noTeam: false, // 非超管且未分配班组：整页空态，不发业务请求
    teamName: '',
    teamOptions: [], // [{id, name, on}]
    teamDropOpen: false,
    // 统计卡
    overview: null, // {totalAnswered, rightRate|null, wrongCount}
    // 题库列表
    banks: [],
    loading: true,
    isManager: false, // admin / team_admin：显示管理入口与底部主按钮
  },

  onLoad() {
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
    this._role = user.role || 'user';
    this._teamId = 0; // 生效班组 id（仅超管经切换器指定；0=不带参数，后端落自己/默认班组）
    // 非超管且未分配班组：整页空态，不再发任何业务请求
    if (this._role !== 'admin' && !user.team) {
      this.setData({ gate: true, noTeam: true, loading: false });
      return;
    }
    this.setData({
      gate: true,
      isAdmin: this._role === 'admin',
      isManager: this._role === 'admin' || this._role === 'team_admin',
      teamName: user.team || '',
    });
    if (this._role === 'admin') {
      this.initTeams(user); // 超管先定生效班组，再拉数据
      return;
    }
    this.loadAll(true);
  },

  // ---------- 班组切换器（仅超管可切换，其余角色静态展示本班名） ----------

  // 超管：拉启用班组（/admin/teams 取 status=1）→ 生效班组（storage 优先 → 自己班组 → 第一个）→ 题库数据
  async initTeams(user) {
    let teams = [];
    try {
      const data = await request({ url: '/api/v1/admin/teams' });
      teams = ((data && data.list) || []).filter((t) => t.status === 1);
    } catch (err) {
      this.toast(err.message);
    }
    this._teams = teams;
    const saved = Number(wx.getStorageSync('quiz_team_id')) || 0;
    const cur = teams.find((t) => t.id === saved)
      || teams.find((t) => t.id === Number(user.team_id))
      || teams[0] || null;
    this.applyTeam(cur ? cur.id : 0, false);
    this.loadAll(true);
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 记录当前生效班组并刷新切换器展示；switching=true 表示用户主动切换，重拉题库数据
  applyTeam(id, switching) {
    this._teamId = id;
    if (id) wx.setStorageSync('quiz_team_id', id);
    const cur = ((this._teams || []).find((t) => t.id === id)) || null;
    // 生效班组名一并存下（manage 页「上传至」选项展示用）
    if (cur) wx.setStorageSync('quiz_team_name', cur.name);
    this.setData({
      teamName: cur ? cur.name : this.data.teamName,
      teamDropOpen: false,
      teamOptions: (this._teams || []).map((t) => ({ id: t.id, name: t.name, on: t.id === id })),
    });
    if (switching) this.loadAll(false);
  },

  onTeamChipTap() {
    if (!this.data.isAdmin || !(this._teams || []).length) return;
    this.setData({ teamDropOpen: !this.data.teamDropOpen });
  },

  onTeamDropClose() {
    if (this.data.teamDropOpen) this.setData({ teamDropOpen: false });
  },

  onTeamPick(e) {
    const id = Number(e.currentTarget.dataset.id);
    if (!id || id === this._teamId) {
      this.setData({ teamDropOpen: false });
      return;
    }
    this.applyTeam(id, true);
  },

  onShow() {
    // 切回页面时静默刷新一次（刷题/管理可能改动了进度与题库）
    if (this.data.gate && !this.data.noTeam && this._loaded) this.loadAll(false);
  },

  onPullDownRefresh() {
    if (!this.data.gate || this.data.noTeam) {
      wx.stopPullDownRefresh();
      return;
    }
    this.loadAll(false).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 数据加载 ==================== */

  // overview 为个人口径（不带 team_id）；banks（我的题库）按生效班组
  loadAll(isInitial) {
    if (isInitial) this.setData({ loading: true });
    return Promise.all([
      request({ url: `${API_BASE}/overview` }).catch((err) => {
        this.toast(err.message);
        return null;
      }),
      request({ url: `${API_BASE}/banks${this.teamQuery('?')}` }).catch((err) => {
        this.toast(err.message);
        return null;
      }),
    ]).then(([ov, bk]) => {
      this._loaded = true;
      this.setData({
        overview: ov || this.data.overview,
        banks: bk ? ((bk.list || []).map((b) => this.mapBank(b))) : this.data.banks,
        loading: false,
      });
    });
  },

  // 题库 → 展示结构（解析进度：analysis = { none, pending, failed, done } 计数）
  mapBank(b) {
    const an = b.analysis || {};
    const anDone = an.done || 0;
    const anPending = an.pending || 0;
    const anFailed = an.failed || 0;
    const anTotal = (an.none || 0) + anPending + anFailed + anDone;
    const total = b.questionCount || 0;
    const answered = b.answeredCount || 0;
    return {
      id: b.id,
      name: b.name || '',
      description: b.description || '',
      questionCount: total,
      answeredCount: answered,
      rightRate: b.rightRate, // null=尚未作答
      rightRateText: b.rightRate === null || b.rightRate === undefined ? '—' : `${b.rightRate}%`,
      notStarted: answered === 0,
      pct: total ? Math.round((answered / total) * 100) : 0,
      analyzing: anPending > 0,
      analyzingText: `AI 解析中 ${anDone}/${anTotal || total}`,
      // 解析就绪徽章：无排队且有已生成解析
      aiReady: anPending === 0 && anDone > 0,
    };
  },

  /* ==================== 交互 ==================== */

  // 点题库卡：选择练习模式后进入刷题页
  onBankTap(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.showActionSheet({
      itemList: ['顺序练习', '随机练习'],
      success: (res) => {
        const mode = res.tapIndex === 1 ? 'rand' : 'seq';
        wx.navigateTo({
          url: `/pkg-quiz/pages/practice/practice?bankId=${id}&mode=${mode}&title=${encodeURIComponent(name)}`,
        });
      },
    });
  },

  // 错题本入口卡：进错题本页
  onWrongTap() {
    wx.navigateTo({ url: '/pkg-quiz/pages/wrong/wrong' });
  },

  // 题库池入口卡：进题库池二级页（添加/移出后回本页 onShow 刷新我的题库）
  onPoolTap() {
    wx.navigateTo({ url: '/pkg-quiz/pages/pool/pool' });
  },

  // 错题专项练习（0 题时按钮禁用）
  onWrongPractice() {
    const ov = this.data.overview;
    if (!ov || !ov.wrongCount) return;
    wx.navigateTo({
      url: `/pkg-quiz/pages/practice/practice?mode=wrong&title=${encodeURIComponent('错题专项练习')}`,
    });
  },

  // 管理入口（板块标题右侧「管理」与底部主按钮同进 manage 页）
  onManageTap() {
    if (!this.data.isManager) return;
    wx.navigateTo({ url: '/pkg-quiz/pages/manage/manage' });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库刷题' });
  },
});
