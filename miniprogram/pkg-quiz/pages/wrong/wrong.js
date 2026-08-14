// 题库刷题 · 错题本（设计稿 design/quiz.html 屏 04）
// 汇总卡（N 道错题 + 专项练习橙色主按钮）+ 错题卡列表（题型 tag / 错 N 次 / 来源题库 / 相对时间 / 移除）
// 规则：刷题答错自动收录；同一题连续答对 3 次自动移出，也可手动移除
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';

const TYPE_TEXT = { single: '单选', multiple: '多选', judge: '判断' };

// ISO 时间 → 相对时间（今天 / 昨天 / N 天前 / 超 30 天显示日期）
const relTime = (iso) => {
  const d = iso ? new Date(iso) : null;
  if (!d || Number.isNaN(d.getTime())) return '';
  const now = new Date();
  const day0 = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const day1 = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((day0 - day1) / 86400000);
  if (days <= 0) return '今天';
  if (days === 1) return '昨天';
  if (days <= 30) return `${days} 天前`;
  const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};

Page({
  data: {
    gate: false,
    list: [],
    bankCount: 0, // 覆盖题库数（汇总卡副行）
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
    this.setData({ gate: true });
    this.loadList(true);
  },

  onShow() {
    // 专项练习返回后刷新（连续答对移出 / 新错题会变化）
    if (this.data.gate && this._loaded) this.loadList(false);
  },

  onPullDownRefresh() {
    this.loadList(false).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  async loadList(isInitial) {
    if (isInitial) this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/wrongs` });
      const list = ((data && data.list) || []).map((w) => ({
        questionId: w.questionId,
        bankId: w.bankId,
        bankName: w.bankName || '',
        typeText: TYPE_TEXT[w.type] || '单选',
        typeCls: w.type === 'judge' ? 'tag-navy' : 'tag-orange',
        content: w.content || '',
        wrongCount: w.wrongCount || 0,
        rightStreak: w.rightStreak || 0,
        relTime: relTime(w.lastWrongAt),
      }));
      this._loaded = true;
      this.setData({
        list,
        bankCount: new Set(list.map((w) => String(w.bankId))).size,
        loading: false,
      });
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 开始错题专项练习（0 题时禁用）
  onPractice() {
    if (!this.data.list.length) return;
    wx.navigateTo({
      url: `/pkg-quiz/pages/practice/practice?mode=wrong&title=${encodeURIComponent('错题专项练习')}`,
    });
  },

  // 手动移除（Dialog 二次确认 → DELETE → 本地移除）
  onRemove(e) {
    const { id } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '移出错题本？',
      content: '该题将从错题本移除，之后答错会重新收录。',
      confirmBtn: '确认移除',
      cancelBtn: '取消',
    }).then(() => {
      request({
        url: `${API_BASE}/wrongs/${encodeURIComponent(id)}`,
        method: 'DELETE',
      }).then(() => {
        const list = this.data.list.filter((w) => String(w.questionId) !== String(id));
        this.setData({
          list,
          bankCount: new Set(list.map((w) => String(w.bankId))).size,
        });
        this.toast('已移出错题本');
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '错题本' });
  },
});
