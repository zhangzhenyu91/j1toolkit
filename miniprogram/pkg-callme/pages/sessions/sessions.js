// Call Me · 会话列表页
// 进入方式：从首页进入时自动进入最近一次对话（无对话则自动创建）；
// 从对话页返回列表时不触发自动进入（onLoad 一次性标记实现）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { formatTime, parseDate } from '../../../utils/util';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    sessions: [],
    loading: true,
    creating: false,
  },

  onLoad() {
    // 页面实例化（即从首页进入）时标记一次自动进入；
    // 由对话页 navigateBack 返回时不会再次触发
    this._autoEnter = true;
  },

  onShow() {
    this.loadSessions();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 加载会话列表（对 WeKnora 响应结构做兼容：数组 / {list} / {sessions}）
  async loadSessions() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/callme/sessions' });
      const raw = Array.isArray(data) ? data : data.list || data.sessions || [];
      const sessions = raw
        .map((item) => ({
          id: item.id,
          title: item.title || '新对话', // 空标题 = 待 WeKnora 首轮后自动生成（显示兜底）
          ts: (parseDate(item.updated_at || item.created_at) || new Date(0)).getTime(),
          timeText: formatTime(item.updated_at || item.created_at),
        }))
        .sort((a, b) => b.ts - a.ts); // 最近更新在前
      this.setData({ sessions });
      if (this._autoEnter) {
        this._autoEnter = false;
        this.autoEnter(sessions);
      }
    } catch (err) {
      this._autoEnter = false;
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 自动进入最近一次对话；无对话时自动创建后进入
  autoEnter(sessions) {
    if (sessions.length) {
      const latest = sessions[0];
      wx.navigateTo({
        url: `/pkg-callme/pages/chat/chat?id=${latest.id}&title=${encodeURIComponent(
          latest.title || '新对话'
        )}`,
      });
      return;
    }
    this.createAndEnter('');
  },

  // 创建会话并进入（自动创建与手动新建共用）
  async createAndEnter(title) {
    if (this.data.creating) return;
    this.setData({ creating: true });
    wx.showLoading({ title: '正在创建对话…', mask: true });
    try {
      const data = await request({
        url: '/api/v1/callme/sessions',
        method: 'POST',
        data: { title },
      });
      wx.hideLoading();
      const id = data.id || (data.data && data.data.id);
      if (!id) {
        this.toast('创建失败：未返回会话 ID');
        return;
      }
      wx.navigateTo({
        url: `/pkg-callme/pages/chat/chat?id=${id}&title=${encodeURIComponent(title || '新对话')}`,
      });
    } catch (err) {
      wx.hideLoading();
      this.toast(err.message);
    } finally {
      this.setData({ creating: false });
    }
  },

  // 新建对话：输入标题 → 创建会话 → 进入对话页
  onCreate() {
    wx.showModal({
      title: '新建对话',
      editable: true,
      placeholderText: '请输入对话标题（留空则首轮对话后自动生成）',
      confirmText: '创建',
      success: (res) => {
        if (!res.confirm) return;
        const title = (res.content && res.content.trim()) || '';
        this.createAndEnter(title);
      },
    });
  },

  // 进入对话
  onOpen(e) {
    const { id, title } = e.currentTarget.dataset;
    wx.navigateTo({
      url: `/pkg-callme/pages/chat/chat?id=${id}&title=${encodeURIComponent(title || '新对话')}`,
    });
  },

  // 重命名会话（滑块动作 + 输入新名称）
  onRename(e) {
    const { id } = e.currentTarget.dataset;
    // 以 id 回查当前标题（dataset 里的 title 是渲染时快照，重命名后不更新）
    const target = this.data.sessions.find((s) => String(s.id) === String(id));
    const oldTitle = target ? target.title : '';
    wx.showModal({
      title: '重命名对话',
      editable: true,
      content: oldTitle,
      placeholderText: '请输入新的对话名称',
      confirmText: '保存',
      success: async (res) => {
        if (!res.confirm) return;
        const newTitle = (res.content || '').trim();
        if (!newTitle) {
          this.toast('名称不能为空');
          return;
        }
        if (newTitle === oldTitle) return;
        try {
          await request({
            url: `/api/v1/callme/sessions/${id}`,
            method: 'PUT',
            data: { title: newTitle },
          });
          // 整数组替换刷新（与删除同口径）：t-swipe-cell slot 内容对单字段路径 setData 不会重渲染
          const sessions = this.data.sessions.map((s) =>
            String(s.id) === String(id) ? { ...s, title: newTitle } : s
          );
          this.setData({ sessions });
          this.toast('已重命名');
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 删除会话（滑动删除 + 二次确认）
  onDelete(e) {
    const { id } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除对话',
      content: '删除后不可恢复，确定删除该对话吗？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({ url: `/api/v1/callme/sessions/${id}`, method: 'DELETE' });
          // 按 id 过滤移除（渲染 index 在异步请求期间可能错位）；整数组替换与重命名同口径
          const sessions = this.data.sessions.filter((s) => String(s.id) !== String(id));
          this.setData({ sessions });
          this.toast('已删除');
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'call-me', title: 'Call Me' });
  },
});
