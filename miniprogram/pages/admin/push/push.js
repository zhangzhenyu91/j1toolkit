// 消息推送（仅超管）：标题 + 内容（Markdown，可插入图片上传 COS 后引用）+ 推送对象（角色多选），推送后按角色投递到对应用户通知中心
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import { renderMarkdown, MD_TAG_STYLE, MD_CONTAINER_STYLE } from '../../../utils/markdown';

Page({
  data: {
    title: '',
    content: '',
    targets: [], // 推送对象：admin / team_admin / user 的非空子集
    preview: false, // 内容预览态（隐藏输入框，mp-html 渲染当前内容）
    previewHtml: '',
    uploading: false, // 图片上传中（防并发选择）
    pushing: false,
    mdTagStyle: MD_TAG_STYLE,
    mdContainerStyle: MD_CONTAINER_STYLE,
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

  // 插入图片：选图 → 上传 COS → 追加 ![图片](url) 到内容末尾（t-textarea 无光标 API）
  onInsertImage() {
    if (this.data.uploading) return;
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      success: (res) => {
        const file = res.tempFiles && res.tempFiles[0];
        if (!file || !file.tempFilePath) return;
        if (file.size > 5 * 1024 * 1024) {
          this.toast('图片大小应在 5MB 以内');
          return;
        }
        this.setData({ uploading: true });
        wx.showLoading({ title: '上传中…', mask: true });
        wx.uploadFile({
          url: `${BASE_URL}/api/v1/notice/upload-image`,
          filePath: file.tempFilePath,
          name: 'file',
          header: { Authorization: `Bearer ${wx.getStorageSync('token') || ''}` },
          success: (up) => {
            try {
              const body = JSON.parse(up.data || '{}');
              if (up.statusCode >= 200 && up.statusCode < 300 && body.code === 0 && body.data && body.data.url) {
                const joiner = this.data.content && !/\n$/.test(this.data.content) ? '\n' : '';
                this.setData({ content: `${this.data.content}${joiner}![图片](${body.data.url})\n` });
                this.toast('图片已插入');
              } else {
                this.toast(body.message || '图片上传失败');
              }
            } catch (e) {
              this.toast('图片上传失败');
            }
          },
          fail: () => this.toast('网络异常，请检查网络后重试'),
          complete: () => {
            this.setData({ uploading: false });
            wx.hideLoading();
          },
        });
      },
    });
  },

  // 预览/继续编辑切换：预览时以展示端同一渲染管线渲染当前内容
  onTogglePreview() {
    const preview = !this.data.preview;
    this.setData({
      preview,
      previewHtml: preview ? renderMarkdown(this.data.content) : '',
    });
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
