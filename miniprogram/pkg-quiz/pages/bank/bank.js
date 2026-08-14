// 题库刷题 · 题库主页（参考考试宝科目页：练习入口与错题/收藏都跟着题库走，不再全局混排）
// 信息卡（题数/已练/正确率 + 进度条 + AI 解析状态，GET /banks/:id/home）+ 宫格四入口：
// 顺序练习（进度读本地断点 quiz_seq_{bankId}）/ 随机练习 → practice 页；
// 我的错题 → 错题本页（按本题库过滤）；我的收藏 → practice mode=fav 刷题；
// 底部「移出题库」= DELETE /banks/:id/join（保留练习记录与错题），返回列表页 onShow 自动刷新
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';

Page({
  data: {
    gate: false, // 门控（参照 pool 页 gate 模式）
    navTitle: '题库',
    bank: null, // home 接口数据 + 展示字段
    seqText: '', // 顺序练习进度文案
    loading: true,
    emptyText: '',
  },

  onLoad(options) {
    const opts = options || {};
    this._bankId = opts.bankId ? String(opts.bankId) : '';
    const title = opts.title ? decodeURIComponent(opts.title) : '';
    if (title) this.setData({ navTitle: title });
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
    if (!this._bankId) {
      this.setData({ gate: true, loading: false, emptyText: '缺少题库参数' });
      return;
    }
    this.setData({ gate: true });
    this.load(true);
  },

  onShow() {
    // 刷题/错题本返回后刷新（进度与错题/收藏数会变化）
    if (this.data.gate && this._loaded) this.load(false);
  },

  onPullDownRefresh() {
    if (!this.data.gate) {
      wx.stopPullDownRefresh();
      return;
    }
    this.load(false).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 数据加载 ==================== */

  async load(isInitial) {
    if (isInitial) this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/banks/${encodeURIComponent(this._bankId)}/home` });
      this._loaded = true;
      this.setData({ bank: this.mapHome(data || {}), loading: false, emptyText: '' });
      this.refreshSeqText();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false, emptyText: '题库加载失败，请返回重试' });
    }
  },

  // home 接口 → 展示结构（解析进度：analysis = { none, pending, failed, done } 计数）
  mapHome(h) {
    const an = h.analysis || {};
    const anDone = an.done || 0;
    const anPending = an.pending || 0;
    const anTotal = (an.none || 0) + anPending + (an.failed || 0) + anDone;
    const total = h.questionCount || 0;
    const answered = h.answeredCount || 0;
    return {
      id: h.id,
      name: h.name || '',
      description: h.description || '',
      scope: h.scope,
      teamName: h.teamName || '',
      questionCount: total,
      answeredCount: answered,
      rightRateText: h.rightRate === null || h.rightRate === undefined ? '—' : `${h.rightRate}%`,
      pct: total ? Math.round((answered / total) * 100) : 0,
      wrongCount: h.wrongCount || 0,
      favCount: h.favCount || 0,
      analyzing: anPending > 0,
      analyzingText: `AI 解析中 ${anDone}/${anTotal || total}`,
      // 解析就绪徽章：无排队且有已生成解析
      aiReady: anPending === 0 && anDone > 0,
    };
  },

  // 顺序练习进度文案（本地断点 quiz_seq_{bankId} 为大纲下标 0 起；无断点=未开始）
  refreshSeqText() {
    const total = this.data.bank ? this.data.bank.questionCount : 0;
    if (!total) {
      this.setData({ seqText: '暂无题目' });
      return;
    }
    const saved = Number(wx.getStorageSync(`quiz_seq_${this._bankId}`)) || 0;
    if (!saved) {
      this.setData({ seqText: `共 ${total} 题 · 未开始` });
      return;
    }
    this.setData({ seqText: `刷到 ${Math.min(saved + 1, total)}/${total}` });
  },

  /* ==================== 宫格入口 ==================== */

  onSeqTap() {
    this.goPractice('seq');
  },

  onRandTap() {
    this.goPractice('rand');
  },

  goPractice(mode) {
    const b = this.data.bank;
    if (!b) return;
    if (!b.questionCount) {
      this.toast('该题库暂无题目');
      return;
    }
    wx.navigateTo({
      url: `/pkg-quiz/pages/practice/practice?bankId=${this._bankId}&mode=${mode}&title=${encodeURIComponent(b.name)}`,
    });
  },

  // 我的错题：进按本题库过滤的错题本页（0 题提示）
  onWrongTap() {
    const b = this.data.bank;
    if (!b) return;
    if (!b.wrongCount) {
      this.toast('暂无错题，继续保持');
      return;
    }
    wx.navigateTo({
      url: `/pkg-quiz/pages/wrong/wrong?bankId=${this._bankId}&title=${encodeURIComponent(`${b.name}-错题`)}`,
    });
  },

  // 我的收藏：直接进收藏刷题（0 题提示）
  onFavTap() {
    const b = this.data.bank;
    if (!b) return;
    if (!b.favCount) {
      this.toast('暂无收藏，刷题时点星标收藏题目');
      return;
    }
    wx.navigateTo({
      url: `/pkg-quiz/pages/practice/practice?bankId=${this._bankId}&mode=fav&title=${encodeURIComponent(`${b.name}-收藏`)}`,
    });
  },

  /* ==================== 移出题库 ==================== */

  onLeave() {
    const b = this.data.bank;
    if (!b) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: `移出题库「${b.name}」？`,
      content: '移出后练习记录与错题保留，可随时从题库池重新添加。',
      confirmBtn: '确认移出',
      cancelBtn: '取消',
    }).then(() => {
      request({
        url: `${API_BASE}/banks/${encodeURIComponent(this._bankId)}/join`,
        method: 'DELETE',
      }).then(() => {
        this.toast('已移出我的题库');
        setTimeout(() => {
          wx.navigateBack({
            fail: () => wx.reLaunch({ url: '/pkg-quiz/pages/index/index' }),
          });
        }, 600);
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库刷题' });
  },
});
