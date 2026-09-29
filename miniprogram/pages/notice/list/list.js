// 消息通知列表：按当前用户角色过滤的通知流；点击行展开/收起全文，展开未读时自动标记已读
// 内容为 Markdown（含图片）：折叠态显示原文摘要，展开时按需渲染经 mp-html 展示；删除全员可用（超管全局删除，其余角色仅从本人列表移除）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { formatTime } from '../../../utils/util';
import { shareAppMessage } from '../../../utils/share';
import { renderMarkdown, MD_TAG_STYLE, MD_CONTAINER_STYLE } from '../../../utils/markdown';

Page({
  data: {
    items: [], // 通知列表（含 timeText / expanded / html 展示字段）
    unread: 0,
    loading: true,
    isAdmin: false, // 超管删除为全局删除（确认文案区分）；删除入口全员可见
    mdTagStyle: MD_TAG_STYLE,
    mdContainerStyle: MD_CONTAINER_STYLE,
  },

  onShow() {
    const userInfo = wx.getStorageSync('userInfo');
    this.setData({ isAdmin: Boolean(userInfo && userInfo.role === 'admin') });
    this.load();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  async load() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/notice/list?limit=100' });
      const items = ((data && data.items) || []).map((n) => ({
        ...n,
        timeText: formatTime(n.createdAt),
        expanded: false,
      }));
      this.setData({ items, unread: (data && data.unread) || 0 });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 点击行：展开/收起全文；展开时按需渲染 Markdown（html 字段缓存），未读通知展开即上报已读（乐观更新，失败回滚）
  async onItemTap(e) {
    const id = e.currentTarget.dataset.id;
    const idx = this.data.items.findIndex((n) => n.id === id);
    if (idx < 0) return;
    const item = this.data.items[idx];
    const expanded = !item.expanded;
    const patch = { [`items[${idx}].expanded`]: expanded };
    if (expanded && !item.html) patch[`items[${idx}].html`] = renderMarkdown(item.content);
    this.setData(patch);
    if (!expanded || item.read) return;
    this.setData({
      [`items[${idx}].read`]: 1,
      unread: Math.max(0, this.data.unread - 1),
    });
    try {
      await request({ url: `/api/v1/notice/${id}/read`, method: 'POST' });
    } catch (err) {
      this.setData({
        [`items[${idx}].read`]: 0,
        unread: this.data.unread + 1,
      });
      this.toast(err.message);
    }
  },

  // 删除通知（全员可见入口；catchtap 不触发展开/收起；二次确认后删除，同步未读数；
  // 超管全局删除所有成员不可见，其余角色仅从本人列表移除——确认文案按角色区分）
  onDelete(e) {
    const id = e.currentTarget.dataset.id;
    const item = this.data.items.find((n) => n.id === id);
    if (!item) return;
    wx.showModal({
      title: '删除通知',
      content: this.data.isAdmin
        ? `确定删除通知「${item.title}」吗？删除后所有成员均不可见，且不可恢复。`
        : `确定删除通知「${item.title}」吗？删除后仅从您的通知列表移除，不影响其他成员。`,
      confirmText: '删除',
      confirmColor: '#F53F3F',
      success: async (res) => {
        if (!res.confirm) return;
        try {
          await request({ url: `/api/v1/notice/${id}`, method: 'DELETE' });
          this.setData({
            items: this.data.items.filter((n) => n.id !== id),
            unread: item.read ? this.data.unread : Math.max(0, this.data.unread - 1),
          });
          this.toast('已删除');
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 全部已读（仅有未读时显示入口）
  async onReadAll() {
    if (this.data.unread <= 0) return;
    try {
      await request({ url: '/api/v1/notice/read-all', method: 'POST' });
      this.setData({
        items: this.data.items.map((n) => ({ ...n, read: 1 })),
        unread: 0,
      });
      this.toast('已全部标记为已读');
    } catch (err) {
      this.toast(err.message);
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { title: '消息通知' });
  },
});
