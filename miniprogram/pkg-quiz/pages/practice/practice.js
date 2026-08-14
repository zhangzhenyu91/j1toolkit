// 题库刷题 · 刷题页（设计稿 design/quiz.html 屏 02 答题态 / 屏 03 解析态）
// 入参 { bankId, mode=seq|rand|wrong, title }；wrong 模式 bankId 可空（错题专项练习）
// 分批拉题（offset 分页 limit 20，快用完自动续批；rand/wrong 每批 offset=0 拉新批并按已见 id 去重）；
// seq 模式断点续刷：wx.setStorageSync(`quiz_seq_${bankId}`, 全局题序)，进入时按批恢复
// 解析态：结果 banner + 选项三态（正确绿/错选红/漏选绿虚线）+ 答案行 + AI 解析卡（浅橙底）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';
const LIMIT = 20; // 每批拉题数（与后端约定一致）
const WRONG_CLEAR_STREAK = 3; // 错题连续答对移出错题本次数（与错题本规则说明一致）

const TYPE_TEXT = { single: '单选题', multiple: '多选题', judge: '判断题' };
const LETTERS = 'ABCDEFGH';

Page({
  data: {
    gate: false,
    navTitle: '刷题',
    mode: 'seq',
    loading: true, // 首屏加载中
    emptyText: '', // 空态文案（非空即整页空态）
    // 顶部进度
    total: 0,
    posText: '0', // 当前题序（全局，1 起）
    pct: 0,
    // 当前题
    cur: null, // { id, type, typeText, content, options:[{letter,text,cls,st,stIcon}] }
    submitted: false, // 当前题已提交（解析态）
    canSubmit: false, // 已选至少一项
    multiple: false, // 当前题为多选（底部提示）
    // 解析态数据
    result: null, // { right, answerText, analysis, bannerSub }
    isFirst: true,
    isLast: false, // 已到本批末尾且无更多题
    intoView: '', // scroll-view 回顶锚点
  },

  onLoad(options) {
    const opts = options || {};
    this._bankId = opts.bankId ? String(opts.bankId) : '';
    this._mode = ['seq', 'rand', 'wrong'].includes(opts.mode) ? opts.mode : 'seq';
    const title = opts.title ? decodeURIComponent(opts.title) : '';
    this.setData({
      mode: this._mode,
      navTitle: title || (this._mode === 'wrong' ? '错题专项练习' : '刷题'),
    });

    // 内部状态：已加载题目 / 逐题作答结果 / 去重表 / 续批标记
    this._qs = [];
    this._results = {}; // qid → { selected, right, answer, analysis, wrong }
    this._seen = {};
    this._idx = 0;
    this._base = 0; // 已加载首题的全局题序（seq 断点恢复用）
    this._noMore = false;
    this._loadingMore = false;
    this._restoreIdx = 0; // seq 断点：进入时恢复到的批内下标

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
    // seq 断点：按批对齐 base，批内下标恢复
    if (this._mode === 'seq' && this._bankId) {
      const saved = Number(wx.getStorageSync(`quiz_seq_${this._bankId}`)) || 0;
      if (saved > 0) {
        this._base = Math.floor(saved / LIMIT) * LIMIT;
        this._restoreIdx = saved - this._base;
      }
    }
    this.initLoad();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 拉题（分批 + 自动续批） ==================== */

  async initLoad() {
    const added = await this.loadMore().catch((err) => {
      this.toast(err.message);
      return -1;
    });
    if (added < 0) {
      this.setData({ loading: false, emptyText: '题目加载失败，请返回重试' });
      return;
    }
    if (!this._qs.length) {
      this.setData({
        loading: false,
        emptyText: this._mode === 'wrong' ? '暂无错题，继续保持' : '该题库暂无题目',
      });
      return;
    }
    // 断点恢复：越界（题库已缩水）则回到本批首题
    this._idx = Math.min(this._restoreIdx, this._qs.length - 1);
    this.setData({ loading: false, total: this._total });
    this.renderCurrent();
  },

  // 追加一批题目；返回新增条数（去重后）。rand/wrong 每批 offset=0 拉随机/当前批并去重
  async loadMore() {
    if (this._loadingMore || this._noMore) return 0;
    this._loadingMore = true;
    const offset = this._mode === 'seq' ? this._base + this._qs.length : 0;
    let url = `${API_BASE}/practice/questions?mode=${this._mode}&offset=${offset}&limit=${LIMIT}`;
    if (this._bankId) url += `&bankId=${encodeURIComponent(this._bankId)}`;
    try {
      const data = await request({ url });
      const raw = (data && data.list) || [];
      this._total = (data && data.total) || 0;
      let list = raw;
      if (this._mode !== 'seq') list = raw.filter((q) => !this._seen[q.id]);
      list.forEach((q) => { this._seen[q.id] = 1; });
      this._qs = this._qs.concat(list);
      // 本批原始不足 limit 或去重后无新题（rand/wrong 题池已刷完）即到底
      if (raw.length < LIMIT || !list.length) this._noMore = true;
      return list.length;
    } finally {
      this._loadingMore = false;
    }
  },

  /* ==================== 渲染当前题 ==================== */

  renderCurrent() {
    const q = this._qs[this._idx];
    if (!q) return;
    const res = this._results[q.id];
    const type = q.type || 'single';
    const isMulti = type === 'multiple';
    const opts = (q.options || []).map((text, i) => {
      const letter = LETTERS[i] || String(i + 1);
      let cls = '';
      let st = '';
      let stIcon = '';
      if (!res) {
        cls = (this._sel || []).includes(letter) ? 'sel' : '';
      } else {
        const inAns = (res.answer || '').includes(letter);
        const inSel = (res.selected || []).includes(letter);
        if (inAns && inSel) {
          cls = 'right'; st = '正确'; stIcon = 'check';
        } else if (inAns && !inSel) {
          // 多选漏选单独绿虚线；单选/判断直接绿色标出正确答案
          cls = isMulti ? 'miss' : 'right';
          st = isMulti ? '漏选' : '';
        } else if (!inAns && inSel) {
          cls = 'wrong'; st = '你的选择'; stIcon = 'close';
        }
      }
      return { letter, text, cls, st, stIcon };
    });

    const globalPos = this._base + this._idx + 1;
    const total = this._total || 0;
    const isLast = this._idx >= this._qs.length - 1 && this._noMore;
    this.setData({
      cur: {
        id: q.id,
        type,
        typeText: TYPE_TEXT[type] || '单选题',
        content: q.content || '',
        options: opts,
      },
      multiple: isMulti && !res,
      submitted: !!res,
      canSubmit: !!(this._sel || []).length,
      result: res ? this.buildResult(res) : null,
      total,
      posText: String(globalPos),
      pct: total ? Math.min(100, Math.round((globalPos / total) * 100)) : 0,
      isFirst: this._idx === 0,
      isLast,
      intoView: '',
    });
    // 回顶（两次赋值保证 scroll-into-view 重复触发）
    wx.nextTick(() => this.setData({ intoView: 'qtop' }));
    // seq 断点保存（全局题序，0 起）
    if (this._mode === 'seq' && this._bankId) {
      wx.setStorageSync(`quiz_seq_${this._bankId}`, this._base + this._idx);
    }
    // 快用完自动续批
    if (!this._noMore && this._qs.length - this._idx <= 3) {
      this.loadMore().then(() => {
        // 续批后重算总数与末尾态（拉到 0 道新题即到底）
        this.setData({
          total: this._total,
          isLast: this._idx >= this._qs.length - 1 && this._noMore,
        });
      }).catch(() => {});
    }
  },

  // 作答结果 → 解析态展示结构（答案行文案 + banner 副文案）
  buildResult(res) {
    const type = (this._qs[this._idx] || {}).type;
    let answerText = res.answer || '';
    if (type === 'judge') answerText = answerText === 'A' ? '正确' : '错误'; // judge 固定 A=正确 B=错误
    let bannerSub = '';
    const w = res.wrong || {};
    if (res.right) {
      if (w.removed) bannerSub = '已移出错题本';
      else if (w.inBook && w.rightStreak > 0) {
        bannerSub = `再答对 ${Math.max(WRONG_CLEAR_STREAK - w.rightStreak, 0)} 次移出错题本`;
      }
    } else if (w.inBook !== false) {
      bannerSub = '已自动加入错题本';
    }
    return {
      right: !!res.right,
      answerText,
      analysis: res.analysis || '',
      bannerSub,
    };
  },

  /* ==================== 选项与提交 ==================== */

  onOptTap(e) {
    if (this.data.submitted) return; // 解析态不可改选
    const { letter } = e.currentTarget.dataset;
    const type = (this._qs[this._idx] || {}).type;
    let sel = this._sel || [];
    if (type === 'multiple') {
      sel = sel.includes(letter) ? sel.filter((l) => l !== letter) : sel.concat(letter);
    } else {
      sel = [letter]; // 单选/判断：单选可改选
    }
    this._sel = sel;
    const opts = this.data.cur.options.map((o) => ({
      letter: o.letter,
      text: o.text,
      cls: sel.includes(o.letter) ? 'sel' : '',
      st: '',
      stIcon: '',
    }));
    this.setData({ 'cur.options': opts, canSubmit: sel.length > 0 });
  },

  async onSubmit() {
    if (this.data.submitted || !this.canAnswer()) return;
    const q = this._qs[this._idx];
    // 多选答案按字母序拼接（如 AC）
    const answer = [...(this._sel || [])].sort().join('');
    wx.showLoading({ title: '提交中…', mask: true });
    try {
      const data = await request({
        url: `${API_BASE}/practice/answer`,
        method: 'POST',
        data: { questionId: q.id, answer },
      });
      this._results[q.id] = {
        selected: [...(this._sel || [])],
        right: !!(data && data.right),
        answer: (data && data.answer) || '',
        analysis: (data && data.analysis) || '',
        wrong: (data && data.wrong) || {},
      };
      this._sel = [];
      this.renderCurrent();
      if (this._results[q.id].wrong.removed) this.toast('已移出错题本');
    } catch (err) {
      this.toast(err.message);
    } finally {
      wx.hideLoading();
    }
  },

  canAnswer() {
    return (this._sel || []).length > 0;
  },

  /* ==================== 翻题 ==================== */

  onPrev() {
    if (this._idx <= 0) return;
    this._idx -= 1;
    this._sel = [];
    this.renderCurrent();
  },

  async onNext() {
    if (this.data.isLast) return;
    // 已加载末尾：先续批再前进
    if (this._idx >= this._qs.length - 1) {
      if (this._noMore) return;
      wx.showLoading({ title: '加载中…', mask: true });
      const added = await this.loadMore().catch((err) => {
        this.toast(err.message);
        return -1;
      });
      wx.hideLoading();
      if (added > 0) this.setData({ total: this._total });
      if (added <= 0) {
        // 拉取失败或已无新题：停在本题并把末尾态上屏
        this.setData({ isLast: this._noMore });
        return;
      }
    }
    this._idx += 1;
    this._sel = [];
    this.renderCurrent();
  },

  // 解析态主按钮：末尾题=完成返回，否则下一题（wxml 事件绑定不支持动态表达式，统一入口内分支）
  onNextOrFinish() {
    if (this.data.isLast) {
      this.onFinish();
      return;
    }
    this.onNext();
  },

  // 本批完成：返回题库列表
  onFinish() {
    wx.navigateBack({
      fail: () => wx.reLaunch({ url: '/pkg-quiz/pages/index/index' }),
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库刷题' });
  },
});
