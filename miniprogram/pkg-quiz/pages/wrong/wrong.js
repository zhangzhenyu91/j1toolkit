// 题库刷题 · 错题本（功能口径见《开发指南》第十四节；按题库过滤，跟着题库走）
// 入参 { bankId, title }：自题库主页（pages/bank/bank）「我的错题」进入，只展示本题库错题；
// 汇总卡（N 道错题 + 专项练习橙色主按钮，练本题库错题）+ 错题按题库分组渲染
// （组头：题库名 + 组内题数 +「练这组」→ practice mode=wrong&bankId=X；组内错题卡：题型 tag / 错 N 次 / 相对时间 / 移除）
// 规则：刷题答错自动收录；同一题连续答对 3 次自动移出，也可手动移除
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { parseDate, pad } from '../../../utils/util';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';

const TYPE_TEXT = { single: '单选', multiple: '多选', judge: '判断' };

// 后端时间串（ISO 或 'yyyy-MM-dd HH:mm:ss'）→ 相对时间（今天 / 昨天 / N 天前 / 超 30 天显示日期）
const relTime = (iso) => {
  const d = parseDate(iso);
  if (!d) return '';
  const now = new Date();
  const day0 = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const day1 = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((day0 - day1) / 86400000);
  if (days <= 0) return '今天';
  if (days === 1) return '昨天';
  if (days <= 30) return `${days} 天前`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};

Page({
  data: {
    gate: false,
    navTitle: '错题本',
    scoped: false, // 按题库过滤（自题库主页进入时 true）
    groups: [], // 按题库分组 [{bankId, bankName, items:[...]}]
    total: 0, // 错题总数（汇总卡）
    loading: true,
  },

  onLoad(options) {
    // 生效题库（自题库主页进入时携带；错题跟着题库走）
    const opts = options || {};
    this._bankId = opts.bankId ? String(opts.bankId) : '';
    const title = opts.title ? decodeURIComponent(opts.title) : '';
    if (title) this.setData({ navTitle: title, scoped: true });
    else if (this._bankId) this.setData({ scoped: true });
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
      // bankId 可选过滤（题库主页进入时只回本题库错题）
      let url = `${API_BASE}/wrongs`;
      if (this._bankId) url += `?bankId=${encodeURIComponent(this._bankId)}`;
      const data = await request({ url });
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
      // 按题库分组（保持接口返回顺序：组按首题出现先后，组内按最近答错排序）
      const groups = [];
      const gmap = {};
      list.forEach((w) => {
        const key = String(w.bankId);
        if (!gmap[key]) {
          gmap[key] = { bankId: w.bankId, bankName: w.bankName || '未命名题库', items: [] };
          groups.push(gmap[key]);
        }
        gmap[key].items.push(w);
      });
      this._loaded = true;
      this.setData({
        groups,
        total: list.length,
        loading: false,
      });
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 开始错题专项练习（带 bankId 时只练本题库错题，0 题时禁用）
  onPractice() {
    if (!this.data.total) return;
    let url = `/pkg-quiz/pages/practice/practice?mode=wrong&title=${encodeURIComponent(this.data.navTitle || '错题专项练习')}`;
    if (this._bankId) url += `&bankId=${encodeURIComponent(this._bankId)}`;
    wx.navigateTo({ url });
  },

  // 组头「练这组」：只练该题库下的错题
  onGroupPractice(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.navigateTo({
      url: `/pkg-quiz/pages/practice/practice?mode=wrong&bankId=${encodeURIComponent(id)}&title=${encodeURIComponent(`${name}-错题`)}`,
    });
  },

  // 手动移除（Dialog 二次确认 → DELETE → 本地移除；组内清空则整组消失）
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
        const groups = [];
        this.data.groups.forEach((g) => {
          const items = g.items.filter((w) => String(w.questionId) !== String(id));
          if (items.length) groups.push({ bankId: g.bankId, bankName: g.bankName, items });
        });
        this.setData({ groups, total: Math.max(this.data.total - 1, 0) });
        this.toast('已移出错题本');
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '错题本' });
  },
});
