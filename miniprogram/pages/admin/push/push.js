// 消息推送（仅超管）：标题 + 内容 + 推送对象（角色多选），推送后按角色投递到对应用户通知中心
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    title: '',
    content: '',
    targets: [], // 推送对象：admin / team_admin / user 的非空子集
    pushing: false,
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onTitle(e) {
    this.setData({ title: e.detail.value });
  },

  onContent(e) {
    this.setData({ content: e.detail.value });
  },

  onTargets(e) {
    this.setData({ targets: e.detail.value });
  },

  async onSubmit() {
    if (this.data.pushing) return;
    const title = this.data.title.trim();
    const content = this.data.content.trim();
    if (!title) {
      this.toast('请填写通知标题');
      return;
    }
    if (!content) {
      this.toast('请填写通知内容');
      return;
    }
    if (this.data.targets.length === 0) {
      this.toast('请至少选择一个推送对象');
      return;
    }
    this.setData({ pushing: true });
    try {
      await request({
        url: '/api/v1/notice/push',
        method: 'POST',
        data: { title, content, targets: this.data.targets },
      });
      this.toast('推送成功');
      setTimeout(() => wx.navigateBack(), 600); // 稍候让 toast 可见再返回
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ pushing: false });
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { title: '消息推送' });
  },
});
