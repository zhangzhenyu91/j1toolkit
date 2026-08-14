// 题库刷题 · 题库管理（超管 / 班组管理员；设计稿 design/quiz.html 屏 05）
// 新建题库 + 题库管理列表（GET /banks/manage；编辑/删除/解析状态三态：解析完成 / 解析中进度 / 失败重试）；
// Excel 批量导入已移至网页端（页底小字提示），小程序端仅保留建库与维护
// analysis.pending>0 时 5s 轮询 /banks/manage 刷新解析状态，全部终态后停轮询（同 safeday 轮询模式）
// 班组口径：超管按主页切换器存下的 quiz_team_id 生效（全部管理请求统一带 team_id：URL 用 teamQuery、body 用 teamBody）；
// 其余角色后端强制本班。题库范围 scope：team=班组池 / all=全部池（仅超管可建/改全部池，弹层「上传至」选择）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/quiz';
const POLL_INTERVAL = 5000; // 有解析中题库时按 5s 轮询（同 safeday 口径）

Page({
  data: {
    isManager: false, // 门控：仅 admin / team_admin 可见页面内容
    isAdmin: false, // 超管：弹层显示「上传至」选择（可建/改全部池）
    teamName: '', // 当前生效班组名（「上传至-班组池」选项展示）
    banks: [],
    loading: true,
    // 新建/编辑题库弹层
    bankOpen: false,
    bankMode: 'create', // create / edit
    bankId: 0, // 编辑目标
    bankName: '',
    bankDesc: '',
    bankScope: 'team', // 上传至：team=班组池 / all=全部池（仅超管可选可传）
    bankSaving: false,
    keyboardHeight: 0,
  },

  async onLoad() {
    // 超管 / 班组管理员可访问（等启动自检完成再取角色）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    if (user.role !== 'admin' && user.role !== 'team_admin') {
      this.toast('仅管理员可访问');
      setTimeout(() => wx.navigateBack(), 1200);
      return;
    }
    this._role = user.role;
    // 生效班组：取主页切换器存下的 team_id（仅超管真正生效；其余角色后端强制本班，带上也无妨）
    this._teamId = Number(wx.getStorageSync('quiz_team_id')) || 0;
    this.setData({
      isManager: true,
      isAdmin: user.role === 'admin',
      teamName: wx.getStorageSync('quiz_team_name') || '',
    });
    this.loadBanks(true);
  },

  onShow() {
    // 切回页面时静默刷新一次（并视解析状态恢复轮询）
    if (this.data.isManager && this._loaded) this.loadBanks(false);
  },

  onHide() {
    this.stopPolling();
  },

  onUnload() {
    this.stopPolling();
  },

  // 生效班组 query 片段（lead 为前导连接符；无选中班组则不携带）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST/PUT JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  /* ==================== 题库列表与解析状态轮询 ==================== */

  stopPolling() {
    if (this._pollTimer) {
      clearTimeout(this._pollTimer);
      this._pollTimer = null;
    }
  },

  schedulePoll() {
    this.stopPolling();
    this._pollTimer = setTimeout(() => this.loadBanks(false), POLL_INTERVAL);
  },

  async loadBanks(isInitial) {
    if (isInitial) this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/banks/manage${this.teamQuery('?')}` });
      const banks = ((data && data.list) || []).map((b) => this.mapBank(b));
      this._loaded = true;
      this.setData({ banks, loading: false });
      // 有解析中的题库则继续轮询，全部终态后停止
      if (banks.some((b) => b.analyzing)) this.schedulePoll();
      else this.stopPolling();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
      // 轮询中出错：仍按已知解析中状态继续轮询，避免状态卡死
      if (!isInitial && (this.data.banks || []).some((b) => b.analyzing)) this.schedulePoll();
    }
  },

  // 题库 → 展示结构（scope 范围标签；解析状态三态：解析完成 / 解析中 x/y / N 题失败 + 重试）
  mapBank(b) {
    const an = b.analysis || {};
    const anDone = an.done || 0;
    const anPending = an.pending || 0;
    const anFailed = an.failed || 0;
    const anTotal = (an.none || 0) + anPending + anFailed + anDone;
    return {
      id: b.id,
      name: b.name || '',
      description: b.description || '',
      scope: b.scope === 'all' ? 'all' : 'team',
      teamName: b.teamName || '',
      questionCount: b.questionCount || 0,
      analyzing: anPending > 0,
      anDone,
      anFailed,
      anTotal,
      anPct: anTotal ? Math.round((anDone / anTotal) * 100) : 0,
      // 解析完成：无排队无失败且有已生成解析
      anFinished: anPending === 0 && anFailed === 0 && anDone > 0,
    };
  },

  /* ==================== 新建 / 编辑题库弹层 ==================== */

  onOpenCreate() {
    this.setData({
      bankOpen: true,
      bankMode: 'create',
      bankId: 0,
      bankName: '',
      bankDesc: '',
      bankScope: 'team', // 新建默认班组池
      keyboardHeight: 0,
    });
  },

  onOpenEdit(e) {
    const { id, name, description, scope } = e.currentTarget.dataset;
    this.setData({
      bankOpen: true,
      bankMode: 'edit',
      bankId: id,
      bankName: name || '',
      bankDesc: description || '',
      bankScope: scope === 'all' ? 'all' : 'team', // 回填现范围
      keyboardHeight: 0,
    });
  },

  // 「上传至」选择（仅超管可见可点）
  onScopePick(e) {
    const scope = e.currentTarget.dataset.scope === 'all' ? 'all' : 'team';
    this.setData({ bankScope: scope });
  },

  onBankNameInput(e) {
    this.setData({ bankName: e.detail.value });
  },

  onBankDescInput(e) {
    this.setData({ bankDesc: e.detail.value });
  },

  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
  },

  onBankCancel() {
    if (this.data.bankSaving) return;
    this.setData({ bankOpen: false, keyboardHeight: 0 });
  },

  // 提交中不允许遮罩关闭（同 safeday 名称弹层口径）
  onBankVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.bankSaving) {
      this.setData({ bankOpen: true });
      return;
    }
    if (this.data.bankOpen) this.setData({ bankOpen: false, keyboardHeight: 0 });
  },

  async onBankSave() {
    if (this.data.bankSaving) return;
    const name = (this.data.bankName || '').trim();
    if (!name) {
      this.toast('请输入题库名称');
      return;
    }
    const description = (this.data.bankDesc || '').trim();
    const isEdit = this.data.bankMode === 'edit';
    // scope 仅超管可传（'all' 仅超管可建/改；team_admin 固定班组池，不传 scope）
    const payload = { name, description };
    if (this._role === 'admin') payload.scope = this.data.bankScope;
    this.setData({ bankSaving: true });
    try {
      await request({
        url: `${isEdit ? `${API_BASE}/banks/${this.data.bankId}` : `${API_BASE}/banks`}${this.teamQuery('?')}`,
        method: isEdit ? 'PUT' : 'POST',
        data: this.teamBody(payload),
      });
      this.setData({ bankOpen: false, keyboardHeight: 0 });
      this.toast(isEdit ? '已保存' : '已新建题库');
      this.loadBanks(false);
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ bankSaving: false });
    }
  },

  /* ==================== 删除题库 / 重试失败解析 ==================== */

  onDeleteBank(e) {
    const { id, name } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: `删除题库「${name || ''}」？`,
      content: '将同时清空该题库所有题目与全员答题记录，该操作不可恢复。',
      confirmBtn: '确认删除',
      cancelBtn: '取消',
    }).then(() => {
      // 删除请求无 body：team_id 走 query（超管缺 team_id 会落到默认班组匹配失败 404）
      request({
        url: `${API_BASE}/banks/${encodeURIComponent(id)}${this.teamQuery('?')}`,
        method: 'DELETE',
      }).then(() => {
        this.toast('题库已删除');
        this.loadBanks(false);
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onRetryAnalyze(e) {
    const { id, name } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '重试失败解析？',
      content: `将把题库「${name || ''}」中解析失败的题目重新排队生成解析。`,
      confirmBtn: '重试',
      cancelBtn: '取消',
    }).then(() => {
      request({
        url: `${API_BASE}/banks/${encodeURIComponent(id)}/analyze-retry${this.teamQuery('?')}`,
        method: 'POST',
        data: this.teamBody({}),
      }).then((data) => {
        this.toast(`已重新排队 ${(data && data.queued) || 0} 题`);
        this.loadBanks(false);
      }).catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'quiz', title: '题库管理' });
  },
});
