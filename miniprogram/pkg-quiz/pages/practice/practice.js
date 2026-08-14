// 题库刷题 · 刷题页（设计稿 design/quiz.html 屏 02 答题态 / 屏 03 解析态）
// 入参 { bankId, mode=seq|rand|wrong, title }；wrong 模式 bankId 可空（错题专项练习）
// 题序大纲：进入先拉 lite=1 全量大纲（seq 按 sort,id 升序 / rand 本 session 随机序 / wrong 按最近答错倒序），
// 题目内容按 20/批按需拉取（offset=批首大纲下标；答题模式不带答案，背题模式 withAnswer=1 带 answer/analysis）
// seq 断点续刷：wx.setStorageSync(`quiz_seq_${bankId}`, 大纲下标)，进入时恢复
// 双模式：答题（提交判分 + 解析态三态）/ 背题（直接标出正确答案，常显答案行 + AI 解析卡，不提交不写记录）；
// 答题卡弹层：按题型分区块题号导航（未答灰 / 答对绿 / 答错红 / 当前橙框），底部可清空做题记录（错题本保留）
// 切题：底部按钮 / 答题卡跳题 / 内容区左右滑动（左滑下一题、右滑上一题），统一走两段式滑动动画（旧内容滑出 → 换数据 → 对侧滑入）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
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
    viewMode: 'answer', // answer=答题 / recite=背题
    loading: true, // 首屏加载中
    emptyText: '', // 空态文案（非空即整页空态）
    // 顶部进度
    total: 0,
    posText: '0', // 当前题序（全局，1 起）
    pct: 0,
    // 当前题
    cur: null, // { id, type, typeText, content, options:[{letter,text,cls,st,stIcon}] }
    qAnim: '', // 切题动画类：'' / out-left / out-right / from-left / from-right
    submitted: false, // 当前题已提交（解析态；背题模式恒 true）
    canSubmit: false, // 已选至少一项
    multiple: false, // 当前题为多选（底部提示）
    // 解析态数据
    result: null, // { right, answerText, analysis, bannerSub }
    isFirst: true,
    isLast: false, // 已到大纲末尾
    intoView: '', // scroll-view 回顶锚点
    // 答题卡弹层
    sheetOpen: false,
    sheetGroups: [], // [{ type, typeText, count, items:[{idx,num,st,cur}] }]
    sheetAnswered: 0, // 已答数（本 session 提交口径）
    canReset: false, // 「清空做题记录」可点（答题模式 + 有 bankId + 已答数>0）
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

    // 内部状态：全量题序大纲 / 批次内容缓存（批首下标 → 题目数组）/ 逐题作答结果
    this._outline = []; // [{id,type}]
    this._cache = {};
    this._results = {}; // qid → { selected, right, answer, analysis, wrong }
    this._idx = 0; // 当前题的大纲下标
    this._sel = [];
    this._inflight = null; // 进行中的批次请求（防并发重拉）
    this._switching = false; // 切题动画进行中（期间忽略翻题与手势）
    this._touch = null; // 滑动手势起点

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
    this.initLoad();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 拉题（大纲 + 分批内容） ==================== */

  async initLoad() {
    try {
      await this.fetchOutline();
      if (!this._outline.length) {
        this.setData({
          loading: false,
          emptyText: this._mode === 'wrong' ? '暂无错题，继续保持' : '该题库暂无题目',
        });
        return;
      }
      // seq 断点恢复（大纲下标；题库缩水越界则兜底到末尾题）
      if (this._mode === 'seq' && this._bankId) {
        const saved = Number(wx.getStorageSync(`quiz_seq_${this._bankId}`)) || 0;
        this._idx = Math.min(Math.max(saved, 0), this._outline.length - 1);
      }
      await this.ensureBatch(this._idx, true);
      if (!this.getQuestion(this._idx)) {
        this.setData({ loading: false, emptyText: '题目加载失败，请返回重试' });
        return;
      }
      this.setData({ loading: false, total: this._outline.length });
      this.renderCurrent();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false, emptyText: '题目加载失败，请返回重试' });
    }
  },

  // 全量题序大纲（lite=1：仅 id + type，不分页）
  async fetchOutline() {
    let url = `${API_BASE}/practice/questions?mode=${this._mode}&lite=1`;
    if (this._bankId) url += `&bankId=${encodeURIComponent(this._bankId)}`;
    const data = await request({ url });
    this._outline = (data && data.list) || [];
  },

  // 下标所在批次内容（offset=批首下标；背题模式 withAnswer=1 带 answer/analysis）。silent=静默预拉
  async ensureBatch(idx, silent) {
    const start = Math.floor(idx / LIMIT) * LIMIT;
    if (this._cache[start]) return;
    // 有进行中的请求先等它落定（翻页与预拉可能撞批），再复查缓存
    if (this._inflight) {
      await this._inflight.catch(() => {});
      if (this._cache[start]) return;
    }
    if (!silent) wx.showLoading({ title: '加载中…', mask: true });
    let url = `${API_BASE}/practice/questions?mode=${this._mode}&offset=${start}&limit=${LIMIT}`;
    if (this._bankId) url += `&bankId=${encodeURIComponent(this._bankId)}`;
    if (this.data.viewMode === 'recite') url += '&withAnswer=1';
    this._inflight = request({ url });
    try {
      const data = await this._inflight;
      const raw = (data && data.list) || [];
      // 按大纲区间 id 对齐重排（对不上则按响应原序兜底）
      const map = {};
      raw.forEach((q) => { map[q.id] = q; });
      const seg = this._outline.slice(start, start + LIMIT).map((o) => map[o.id]).filter(Boolean);
      this._cache[start] = seg.length ? seg : raw;
    } finally {
      this._inflight = null;
      if (!silent) wx.hideLoading();
    }
  },

  // 取大纲下标对应题目（所在批次须已缓存）
  getQuestion(idx) {
    const start = Math.floor(idx / LIMIT) * LIMIT;
    return (this._cache[start] || [])[idx - start] || null;
  },

  /* ==================== 渲染当前题 ==================== */

  renderCurrent() {
    const q = this.getQuestion(this._idx);
    if (!q) return;
    const recite = this.data.viewMode === 'recite';
    const res = this._results[q.id];
    const type = q.type || 'single';
    const isMulti = type === 'multiple';
    const opts = (q.options || []).map((text, i) => {
      const letter = LETTERS[i] || String(i + 1);
      let cls = '';
      let st = '';
      let stIcon = '';
      if (recite) {
        // 背题：直接标出正确答案（绿底绿勾），其余普通
        if ((q.answer || '').includes(letter)) {
          cls = 'right'; st = '正确'; stIcon = 'check';
        }
      } else if (!res) {
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

    const total = this._outline.length;
    const isLast = this._idx >= total - 1;
    this.setData({
      cur: {
        id: q.id,
        type,
        typeText: TYPE_TEXT[type] || '单选题',
        content: q.content || '',
        options: opts,
      },
      multiple: isMulti && !recite && !res,
      submitted: recite || !!res, // 背题恒解析态版式（无 banner、无提交按钮）
      canSubmit: !recite && !!(this._sel || []).length,
      result: recite ? this.buildReciteResult(q) : (res ? this.buildResult(res) : null),
      total,
      posText: String(this._idx + 1),
      pct: total ? Math.min(100, Math.round(((this._idx + 1) / total) * 100)) : 0,
      isFirst: this._idx === 0,
      isLast,
      intoView: '',
    });
    // 回顶（两次赋值保证 scroll-into-view 重复触发）
    wx.nextTick(() => this.setData({ intoView: 'qtop' }));
    // seq 断点保存（大纲下标，0 起）
    if (this._mode === 'seq' && this._bankId) {
      wx.setStorageSync(`quiz_seq_${this._bankId}`, this._idx);
    }
    // 临近批尾静默预拉下一批
    const nstart = Math.floor(this._idx / LIMIT) * LIMIT + LIMIT;
    if (nstart < total && !this._cache[nstart]) {
      this.ensureBatch(nstart, true).catch(() => {});
    }
  },

  // 作答结果 → 解析态展示结构（答案行文案 + banner 副文案）
  buildResult(res) {
    const q = this.getQuestion(this._idx);
    const type = (q || {}).type;
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

  // 背题模式展示结构（答案/解析取自题目本身；analysis 为 null 时占位「解析生成中」）
  buildReciteResult(q) {
    let answerText = q.answer || '';
    if ((q.type || 'single') === 'judge') answerText = answerText === 'A' ? '正确' : '错误';
    return {
      right: true,
      answerText,
      analysis: q.analysis || '',
      bannerSub: '',
    };
  },

  /* ==================== 模式切换（答题 / 背题） ==================== */

  async onViewTap(e) {
    const v = e.currentTarget.dataset.v === 'recite' ? 'recite' : 'answer';
    if (v === this.data.viewMode) return;
    // 两种模式出参不同（背题带答案），批次缓存整体作废；先按新模式拉当前批，成功再切换
    const oldMode = this.data.viewMode;
    const oldCache = this._cache;
    this._cache = {};
    this.data.viewMode = v; // 直接改实例值让 ensureBatch 按新模式拼参（不触发渲染）
    try {
      await this.ensureBatch(this._idx, false);
    } catch (err) {
      this.data.viewMode = oldMode;
      this._cache = oldCache;
      this.toast(err.message);
      return;
    }
    this._sel = [];
    this.setData({ viewMode: v });
    this.renderCurrent();
  },

  /* ==================== 选项与提交（仅答题模式） ==================== */

  onOptTap(e) {
    if (this.data.submitted) return; // 解析态/背题不可改选
    const { letter } = e.currentTarget.dataset;
    const q = this.getQuestion(this._idx);
    const type = (q || {}).type;
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
    const q = this.getQuestion(this._idx);
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

  // 跳转到指定大纲下标（必要时先拉所在批次）；dir>0 左出右进 / dir<0 右出左进 / 缺省不播动画
  async goIndex(idx, dir) {
    if (this._jumping || this._switching) return; // 批次加载中 / 切题动画进行中忽略
    this._jumping = true;
    const anim = !!dir && !!this.data.cur; // 首屏等无当前题场景不播动画
    try {
      if (anim) {
        // 出场：旧内容沿切题方向滑出淡出（滑出终点不可见时再换数据，避免内容提前闪换）
        this._switching = true;
        this.setData({ qAnim: dir > 0 ? 'out-left' : 'out-right' });
        await new Promise((r) => setTimeout(r, 180));
      }
      await this.ensureBatch(idx, false);
    } catch (err) {
      this.toast(err.message);
      if (anim) this.paneIn(dir, true); // 拉取失败：原内容原路播回
      return;
    } finally {
      this._jumping = false;
    }
    if (!this.getQuestion(idx)) {
      this.toast('题目加载失败，请重试');
      if (anim) this.paneIn(dir, true);
      return;
    }
    this._idx = idx;
    this._sel = [];
    this.renderCurrent();
    if (anim) this.paneIn(dir, false);
  },

  // 切题入场：新内容从对侧滑入；back=true 为失败播回（从滑出侧回位）。
  // out-* 与 from-* 类名两两相异，直接换类即重启 CSS 动画，无需清空重放
  paneIn(dir, back) {
    const cls = back
      ? (dir > 0 ? 'from-left' : 'from-right')
      : (dir > 0 ? 'from-right' : 'from-left');
    this.setData({ qAnim: cls });
    setTimeout(() => { this._switching = false; }, 210); // 入场播完解锁
  },

  onPrev() {
    if (this._idx <= 0) return;
    this.goIndex(this._idx - 1, -1);
  },

  onNext() {
    if (this.data.isLast) return;
    this.goIndex(this._idx + 1, 1);
  },

  /* ==================== 左右滑动切题 ==================== */

  // 手势绑在题目内容区（.q-pane）而非整页 scroll-view，仅 touchend 一次判定，不做跟手拖拽
  onTouchStart(e) {
    const t = e.touches[0];
    this._touch = { x: t.clientX, y: t.clientY };
  },

  onTouchEnd(e) {
    if (!this._touch) return;
    const t = e.changedTouches[0];
    const dx = t.clientX - this._touch.x;
    const dy = t.clientY - this._touch.y;
    this._touch = null;
    // 批次加载中 / 切题动画进行中 / 答题卡弹层打开时忽略手势
    if (this._jumping || this._switching || this.data.sheetOpen) return;
    // 横向滑动：|dx| ≥ 50px 且明显横向（|dx| > 1.5|dy|），不干扰纵向滚动
    if (Math.abs(dx) < 50 || Math.abs(dx) <= Math.abs(dy) * 1.5) return;
    // 左滑下一题、右滑上一题（与按钮同口径，到顶/到底处理沿用 onPrev/onNext 现逻辑）
    if (dx < 0) this.onNext();
    else this.onPrev();
  },

  // 解析态/背题主按钮：末尾题=完成返回，否则下一题（wxml 事件绑定不支持动态表达式，统一入口内分支）
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

  /* ==================== 答题卡弹层 ==================== */

  // 打开弹层：按题型分区块构建题号 chips（背题模式仅导航，chips 全灰 + 当前橙框）
  onSheetOpen() {
    const recite = this.data.viewMode === 'recite';
    const groups = [];
    ['single', 'multiple', 'judge'].forEach((t) => {
      const items = [];
      this._outline.forEach((o, i) => {
        if ((o.type || 'single') !== t) return;
        let st = '';
        if (!recite) {
          const r = this._results[o.id];
          if (r) st = r.right ? 'right' : 'wrong';
        }
        items.push({ idx: i, num: i + 1, st, cur: i === this._idx });
      });
      if (items.length) groups.push({ type: t, typeText: TYPE_TEXT[t], count: items.length, items });
    });
    const answered = Object.keys(this._results).length;
    this.setData({
      sheetOpen: true,
      sheetGroups: groups,
      sheetAnswered: answered,
      canReset: !recite && !!this._bankId && answered > 0,
    });
  },

  onSheetVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.sheetOpen) this.setData({ sheetOpen: false });
  },

  // 点题号：跳转到该题（所在批次未缓存则先拉批）并关闭弹层；方向按目标与当前下标比较（大=左出右进）
  onSheetJump(e) {
    const idx = Number(e.currentTarget.dataset.idx);
    this.setData({ sheetOpen: false });
    if (Number.isNaN(idx) || idx === this._idx) return;
    this.goIndex(idx, idx > this._idx ? 1 : -1);
  },

  // 清空做题记录：仅清本题库练习记录与进度（错题本保留）→ 重拉大纲与第一批
  onResetRecords() {
    if (!this.data.canReset) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '清空做题记录？',
      content: '仅清空本题库的练习记录与进度，错题本保留。',
      confirmBtn: '确认清空',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({
          url: `${API_BASE}/practice/reset`,
          method: 'POST',
          data: { bankId: this._bankId },
        });
      } catch (err) {
        this.toast(err.message);
        return;
      }
      // 清空本地断点与已答状态（rand 大纲一并换新的 session 随机序）
      this._results = {};
      this._cache = {};
      this._sel = [];
      this._idx = 0;
      if (this._mode === 'seq') wx.removeStorageSync(`quiz_seq_${this._bankId}`);
      this.setData({ sheetOpen: false });
      try {
        await this.fetchOutline();
        if (!this._outline.length) {
          this.setData({ cur: null, total: 0, emptyText: '该题库暂无题目' });
          return;
        }
        await this.ensureBatch(0, false);
        this.setData({ total: this._outline.length });
        this.renderCurrent();
      } catch (err) {
        this.toast(err.message);
        return;
      }
      this.toast('已清空做题记录');
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库刷题' });
  },
});
