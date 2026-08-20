// 消息通知列表：按当前用户角色过滤的通知流；点击行展开/收起全文，展开未读时自动标记已读
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { formatTime } from '../../../utils/util';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    items: [], // 通知列表（含 timeText / expanded 展示字段）
    unread: 0,
    loading: true,
  },

  onShow() {
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

  // 点击行：展开/收起全文；展开未读通知时上报已读（乐观更新，失败回滚）
  async onItemTap(e) {
    const id = e.currentTarget.dataset.id;
    const idx = this.data.items.findIndex((n) => n.id === id);
    if (idx < 0) return;
    const item = this.data.items[idx];
    const expanded = !item.expanded;
    this.setData({ [`items[${idx}].expanded`]: expanded });
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
