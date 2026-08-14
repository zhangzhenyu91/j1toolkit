// 题库刷题 · 刷题页（交互参考考试宝：一题一页，swiper 跟手左右滑动翻页；单选/判断点选即判分）
// 入参 { bankId, mode=seq|rand|wrong|fav, title }；wrong/fav 模式 bankId 可空（页面入口均已带 bankId）
// 题序大纲：进入先拉 lite=1 全量大纲（seq 按 sort,id 升序 / rand 本 session 随机序 / wrong 按最近答错倒序 / fav 按最近收藏倒序），
// 题目内容按 20/批按需拉取（offset=批首大纲下标；答题模式不带答案，背题模式 withAnswer=1 带 answer/analysis；批次带 fav 收藏标记）
// seq 断点续刷：wx.setStorageSync(`quiz_seq_${bankId}`, 大纲下标)，进入时恢复
// 翻页：swiper 三窗格窗口化渲染（仅渲染 当前题±1），bindanimationfinish 后窗口平移到新当前题并无感复位中间格；
// 答题卡跳题直接重建窗口（无动画）；相邻题批次静默预拉，翻页不卡
// 双模式：答题（单选/判断点选即判分、多选底部提交；解析态=正确答案/您的选择 + 本人/全员作答统计 + AI 解析卡）/
// 背题（直接标出正确答案，常显答案行 + AI 解析卡，不提交不写记录）
// 底栏（考试宝式）：收藏星标切换 / 本 session 对错计数 / 已做题（点开答题卡）/ 主按钮（多选提交、解析态下一题）
// 答题卡弹层：按题型分区块题号导航（未答灰 / 答对绿 / 答错红 / 当前橙框）+ 图例行，底部可清空做题记录（错题本保留）
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
    // swiper 窗口（当前题 ±1，边界处缩减；项结构见 buildItem）
    win: [],
    swiperCurrent: 0,
    // 底栏镜像（随窗口重建/作答/收藏同步）
    curFav: 0, // 当前题已收藏
    submitted: false, // 当前题已提交（解析态；背题模式恒 true）
    canSubmit: false, // 已选至少一项
    showSubmit: false, // 多选未提交：底部「提交答案」
    showNext: false, // 已提交/背题：底部「下一题/完成」
    isLast: false, // 已到大纲末尾
    // 本 session 作答计数（底栏与答题卡）
    answeredCount: 0,
    rightCount: 0,
    wrongCount: 0,
    // 答题卡弹层
    sheetOpen: false,
    sheetGroups: [], // [{ type, typeText, count, items:[{idx,num,st,cur}] }]
    canReset: false, // 「清空做题记录」可点（答题模式 + 有 bankId + 已答数>0）
  },

  onLoad(options) {
    const opts = options || {};
    this._bankId = opts.bankId ? String(opts.bankId) : '';
    this._mode = ['seq', 'rand', 'wrong', 'fav'].includes(opts.mode) ? opts.mode : 'seq';
    const title = opts.title ? decodeURIComponent(opts.title) : '';
    this.setData({
      mode: this._mode,
      navTitle: title || (this._mode === 'wrong' ? '错题专项练习' : this._mode === 'fav' ? '收藏练习' : '刷题'),
    });

    // 内部状态：全量题序大纲 / 批次内容缓存（批首下标 → 题目数组）/ 逐题作答结果 / 收藏标记
    this._outline = []; // [{id,type}]
    this._cache = {};
    this._results = {}; // qid → { selected, right, answer, analysis, wrong, stats }
    this._favs = {}; // qid → 1/0
    this._idx = 0; // 当前题的大纲下标
    this._sel = [];
    this._inflight = null; // 进行中的批次请求（防并发重拉）
    this._committing = false; // 翻页/跳题提交中（期间忽略手势落定）
    this._submitting = false; // 判分请求进行中（点选即判分防连点）
    this._favToggling = false; // 收藏切换进行中（防连点）

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
          emptyText: this._mode === 'wrong'
            ? '暂无错题，继续保持'
            : this._mode === 'fav'
              ? '暂无收藏题目，刷题时点星标收藏'
              : '该题库暂无题目',
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
      this.rebuildWindow();
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
      // 收藏标记入本地表（收藏切换先改本地，失败回滚）
      this._cache[start].forEach((q) => { this._favs[q.id] = q.fav ? 1 : 0; });
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

  /* ==================== 窗口化渲染（当前题 ±1 三窗格） ==================== */

  // 大纲下标 → swiper 窗格展示结构（批次未拉到时为 empty 占位格）
  buildItem(idx) {
    const q = this.getQuestion(idx);
    if (!q) return { idx, empty: true };
    const recite = this.data.viewMode === 'recite';
    const res = this._results[q.id];
    const type = q.type || 'single';
    const opts = (q.options || []).map((text, i) => {
      const letter = LETTERS[i] || String(i + 1);
      let cls = '';
      let icon = '';
      if (recite) {
        // 背题：直接标出正确答案（绿底绿勾），其余普通
        if ((q.answer || '').includes(letter)) {
          cls = 'right'; icon = 'check';
        }
      } else if (!res) {
        // 选中态只属当前题（_sel 随翻题清空）
        cls = idx === this._idx && (this._sel || []).includes(letter) ? 'sel' : '';
      } else {
        // 解析态（考试宝式）：正确项统一绿勾，错选项红叉，其余保持普通
        const inAns = (res.answer || '').includes(letter);
        const inSel = (res.selected || []).includes(letter);
        if (inAns) {
          cls = 'right'; icon = 'check';
        } else if (inSel) {
          cls = 'wrong'; icon = 'close';
        }
      }
      return { letter, text, cls, icon };
    });

    return {
      idx,
      id: q.id,
      empty: false,
      type,
      typeText: TYPE_TEXT[type] || '单选题',
      content: q.content || '',
      options: opts,
      submitted: recite || !!res, // 背题恒解析态版式（无统计、无提交按钮）
      result: recite ? this.buildReciteResult(q) : (res ? this.buildResult(res, q) : null),
      fav: this._favs[q.id] ? 1 : 0,
    };
  },

  // 作答结果 → 解析态展示结构（答案行 + 本人/全员作答统计 + 错题本动态文案）
  buildResult(res, q) {
    const type = (q || {}).type;
    let answerText = res.answer || '';
    let yourText = (res.selected || []).join('');
    if (type === 'judge') {
      // judge 固定 A=正确 B=错误
      const jm = { A: '正确', B: '错误' };
      answerText = jm[answerText] || answerText;
      yourText = jm[yourText] || yourText;
    }
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
    const st = res.stats || {};
    return {
      right: !!res.right,
      answerText,
      yourText,
      analysis: res.analysis || '',
      bannerSub,
      myStatsText: st.myTimes ? `本人作答 ${st.myTimes} 次 · 正确率 ${st.myRightRate}%` : '',
      allStatsText: st.allTimes ? `全员作答 ${st.allTimes} 次 · 正确率 ${st.allRightRate}%` : '',
    };
  },

  // 背题模式展示结构（答案/解析取自题目本身；analysis 为 null 时占位「解析生成中」）
  buildReciteResult(q) {
    let answerText = q.answer || '';
    if ((q.type || 'single') === 'judge') answerText = answerText === 'A' ? '正确' : '错误';
    return {
      right: true,
      answerText,
      yourText: '',
      analysis: q.analysis || '',
      bannerSub: '',
      myStatsText: '',
      allStatsText: '',
    };
  },

  // 以 _idx 为中心重建三窗格窗口并复位 swiper 到当前格（中间格内容与用户所见一致，无感切换）
  rebuildWindow() {
    const total = this._outline.length;
    const win = [];
    for (let i = this._idx - 1; i <= this._idx + 1; i += 1) {
      if (i < 0 || i >= total) continue;
      win.push(this.buildItem(i));
    }
    const cur = win.findIndex((w) => w.idx === this._idx);
    this.setData({
      win,
      swiperCurrent: Math.max(cur, 0),
      total,
      posText: String(this._idx + 1),
      pct: total ? Math.min(100, Math.round(((this._idx + 1) / total) * 100)) : 0,
      isLast: this._idx >= total - 1,
    });
    this.refreshBar();
    // seq 断点保存（大纲下标，0 起）
    if (this._mode === 'seq' && this._bankId) {
      wx.setStorageSync(`quiz_seq_${this._bankId}`, this._idx);
    }
    // 相邻题所在批次静默预拉（跟手翻页不卡）
    [this._idx - 1, this._idx + 1].forEach((i) => {
      if (i < 0 || i >= total) return;
      const start = Math.floor(i / LIMIT) * LIMIT;
      if (!this._cache[start]) this.ensureBatch(i, true).catch(() => {});
    });
  },

  // 仅重渲当前格（作答选择/提交判分/收藏切换后）
  refreshCurrentItem() {
    const pos = this.data.win.findIndex((w) => w.idx === this._idx);
    if (pos < 0) return;
    this.setData({ [`win[${pos}]`]: this.buildItem(this._idx) });
    this.refreshBar();
  },

  // 底栏镜像同步（收藏态 / 提交与下一题主按钮 / 已选可提交）
  refreshBar() {
    const item = this.data.win.find((w) => w.idx === this._idx);
    const recite = this.data.viewMode === 'recite';
    const submitted = item ? item.submitted : false;
    const isMulti = item ? item.type === 'multiple' : false;
    this.setData({
      curFav: item ? item.fav : 0,
      submitted,
      canSubmit: !recite && !!(this._sel || []).length,
      showSubmit: !recite && !submitted && isMulti,
      showNext: !!submitted,
    });
  },

  /* ==================== swiper 跟手翻页 ==================== */

  // 翻页落定（仅响应用户手势；程序化复位 source 为空直接忽略）。
  // 此时用户所见即目标格内容：平移窗口并把 current 收回中间格，内容一致故无感
  onSwiperFinish(e) {
    if (!e.detail || e.detail.source !== 'touch') return;
    if (this._committing) return;
    const item = this.data.win[e.detail.current];
    if (!item || item.idx === this._idx) return; // 拖拽回弹未换题
    this.commitIndex(item.idx);
  },

  // 切换到新当前题（必要时先拉批次）；答题卡跳题同走此路（无动画直切）
  async commitIndex(idx) {
    if (this._committing) return;
    this._committing = true;
    try {
      await this.ensureBatch(idx, false);
    } catch (err) {
      this.toast(err.message);
      this.rebuildWindow(); // 拉取失败：复位回当前题
      this._committing = false;
      return;
    }
    if (!this.getQuestion(idx)) {
      this.toast('题目加载失败，请重试');
      this.rebuildWindow();
      this._committing = false;
      return;
    }
    this._idx = idx;
    this._sel = [];
    this.rebuildWindow();
    this._committing = false;
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
    this.rebuildWindow();
  },

  /* ==================== 选项与提交（仅答题模式） ==================== */

  onOptTap(e) {
    if (this.data.submitted || this._submitting) return; // 解析态/背题/判分中不可改选
    const { letter } = e.currentTarget.dataset;
    const q = this.getQuestion(this._idx);
    if (!q) return;
    const type = q.type || 'single';
    if (type === 'multiple') {
      // 多选：勾选切换，底部「提交答案」判分
      const sel = (this._sel || []).includes(letter)
        ? this._sel.filter((l) => l !== letter)
        : (this._sel || []).concat(letter);
      this._sel = sel;
      this.refreshCurrentItem();
      return;
    }
    // 单选/判断：点选即判分（考试宝交互；短暂延时先呈现选中态再出结果）
    this._sel = [letter];
    this.refreshCurrentItem();
    this._submitting = true;
    setTimeout(() => {
      this.onSubmit().finally(() => { this._submitting = false; });
    }, 150);
  },

  async onSubmit() {
    if (this.data.submitted || !this.canAnswer()) return;
    const q = this.getQuestion(this._idx);
    if (!q) return;
    // 多选答案按字母序拼接（如 AC）
    const answer = [...(this._sel || [])].sort().join('');
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
        stats: (data && data.stats) || null,
      };
      this._sel = [];
      this.refreshCounts();
      this.refreshCurrentItem();
      if (this._results[q.id].wrong.removed) this.toast('已移出错题本');
    } catch (err) {
      this.toast(err.message);
    }
  },

  canAnswer() {
    return (this._sel || []).length > 0;
  },

  // 本 session 作答计数（底栏对错数与「已做题」）
  refreshCounts() {
    const rs = Object.keys(this._results).map((k) => this._results[k]);
    const right = rs.filter((r) => r.right).length;
    this.setData({
      answeredCount: rs.length,
      rightCount: right,
      wrongCount: rs.length - right,
    });
  },

  /* ==================== 收藏（星标切换，先本地后请求，失败回滚） ==================== */

  async onFavTap() {
    if (this._favToggling) return;
    const q = this.getQuestion(this._idx);
    if (!q) return;
    const was = this._favs[q.id] ? 1 : 0;
    this._favToggling = true;
    this._favs[q.id] = was ? 0 : 1;
    this.refreshCurrentItem();
    try {
      await request({
        url: `${API_BASE}/favorites/${encodeURIComponent(q.id)}`,
        method: was ? 'DELETE' : 'POST',
      });
      this.toast(was ? '已取消收藏' : '已收藏');
    } catch (err) {
      this._favs[q.id] = was;
      this.refreshCurrentItem();
      this.toast(err.message);
    } finally {
      this._favToggling = false;
    }
  },

  /* ==================== 翻题与完成 ==================== */

  // 解析态/背题主按钮：末尾题=完成返回，否则下一题（等价于左滑一题）
  onNextOrFinish() {
    if (this.data.isLast) {
      this.onFinish();
      return;
    }
    this.commitIndex(this._idx + 1);
  },

  // 本批完成：返回题库主页
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
    this.setData({
      sheetOpen: true,
      sheetGroups: groups,
      canReset: !recite && !!this._bankId && this.data.answeredCount > 0,
    });
  },

  onSheetVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.sheetOpen) this.setData({ sheetOpen: false });
  },

  // 点题号：跳转到该题（所在批次未缓存则先拉批）并关闭弹层
  onSheetJump(e) {
    const idx = Number(e.currentTarget.dataset.idx);
    this.setData({ sheetOpen: false });
    if (Number.isNaN(idx) || idx === this._idx) return;
    this.commitIndex(idx);
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
      this._favs = {};
      this._sel = [];
      this._idx = 0;
      if (this._mode === 'seq') wx.removeStorageSync(`quiz_seq_${this._bankId}`);
      this.setData({ sheetOpen: false });
      try {
        await this.fetchOutline();
        if (!this._outline.length) {
          this.setData({ win: [], total: 0, emptyText: '该题库暂无题目' });
          this.refreshCounts();
          return;
        }
        await this.ensureBatch(0, false);
        this.refreshCounts();
        this.rebuildWindow();
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
