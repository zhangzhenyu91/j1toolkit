// 题库刷题 · 题库列表（app_key quiz；设计稿 design/quiz.html 屏 01）
// 统计卡（累计练习/正确率/错题待攻克）+ 我的题库列表（个人口径，GET /banks）+
// 题库池入口卡（二级页 pages/pool/pool，添加/移出后回本页 onShow 自动刷新）；
// 点题库卡进题库主页（pages/bank/bank：顺序/随机练习与本题库错题/收藏/移出题库，参考考试宝科目页）；
// 管理角色（admin/team_admin）另有「管理」入口与底部「新建题库」主按钮（进 manage 页）
// 班组口径（同 pkg-filetransfer）：超管顶部切换器切班组（storage quiz_team_id，banks 请求带 team_id）；
// 其余角色固定本班（后端强制）；非超管未分配班组 → 整页空态，不发业务请求
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import { createTeamGate } from '../../../utils/teamgate';

const API_BASE = '/api/v1/quiz';

// 班组切换器 + 生效班组门控（storage quiz_team_id，banks 请求带 team_id；
// 生效班组名额外存 quiz_team_name，manage 页「上传至」选项展示用）
const teamGate = createTeamGate({ storageKey: 'quiz_team_id', teamNameKey: 'quiz_team_name' });

Page({
  behaviors: [teamGate],
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    // 班组切换器数据（isAdmin/noTeam/teamName/teamOptions/teamDropOpen）由 teamgate behavior 提供；
    // 非超管未分配班组 → noTeam 整页空态，不发业务请求
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
    // 门控与生效班组确定由 teamgate behavior 完成（非超管未分配班组 → noTeam 空态，不再加载）
    if (!this.passTeamGate(user, () => this.loadAll(true))) return;
    this.setData({ isManager: this._role === 'admin' || this._role === 'team_admin' });
  },

  // 超管主动切换班组后重拉题库数据（teamgate behavior 回调）
  onTeamSwitched() {
    this.loadAll(false);
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
      analyzingText: `解析中 ${anDone}/${anTotal || total}`,
      // 解析就绪徽章：无排队且有已生成解析
      anReady: anPending === 0 && anDone > 0,
    };
  },

  /* ==================== 交互 ==================== */

  // 点题库卡：进题库主页（练习入口与本题库错题/收藏/移出题库都在主页内，跟着题库走）
  onBankTap(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.navigateTo({
      url: `/pkg-quiz/pages/bank/bank?bankId=${id}&title=${encodeURIComponent(name)}`,
    });
  },

  // 题库池入口卡：进题库池二级页（添加/移出后回本页 onShow 刷新我的题库）
  onPoolTap() {
    wx.navigateTo({ url: '/pkg-quiz/pages/pool/pool' });
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
