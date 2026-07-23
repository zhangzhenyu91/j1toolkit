// Call Me · 对话页：SSE 流式接收（对接细节见《WeKnora-API对接指南》第五节）
// 回答经 markdown-it 渲染为 HTML 后由 mp-html 展示（支持表格/标题/加粗/代码块等）；
// 思考内容流式期间展开、回答完毕自动折叠，可点击再展开
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { request } from '../../../utils/request';
import { arrayBufferToString, createSseParser } from '../../utils/sse';

const MarkdownIt = require('markdown-it');

// html:false —— 不透传原始 HTML 标签，防止 AI 输出中的标签被原样注入
const md = new MarkdownIt({ html: false, linkify: true });

// Markdown 排版样式（方案五色板；mp-html 的 tag-style 按标签名生效）
const MD_TAG_STYLE = {
  h1: 'font-size:17px;font-weight:600;margin:10px 0 6px;color:#22314E',
  h2: 'font-size:16px;font-weight:600;margin:10px 0 6px;color:#22314E',
  h3: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#22314E',
  h4: 'font-size:15px;font-weight:600;margin:8px 0 4px;color:#22314E',
  p: 'margin:4px 0',
  ul: 'margin:4px 0 4px 1.2em;padding:0',
  ol: 'margin:4px 0 4px 1.2em;padding:0',
  li: 'margin:2px 0',
  table: 'border-collapse:collapse;margin:8px 0;font-size:13px;display:block;overflow-x:auto',
  th: 'border:1px solid #E8E0CD;background:#FDF6EF;padding:6px 10px;font-weight:600;text-align:left;white-space:nowrap;color:#22314E',
  td: 'border:1px solid #E8E0CD;padding:6px 10px',
  code: 'font-family:Menlo,Consolas,monospace;font-size:0.9em;color:#D85A12',
  pre: 'background:#F7F3EA;border:1px solid #E8E0CD;border-radius:8px;padding:10px;margin:8px 0;overflow-x:auto',
  blockquote: 'border-left:3px solid #F26D21;margin:6px 0;padding:2px 10px;color:#6B7690;background:#FDF6EF',
  a: 'color:#F26D21;text-decoration:underline',
  strong: 'font-weight:600',
  hr: 'border:none;border-top:1px solid #E8E0CD;margin:10px 0',
};
const MD_CONTAINER_STYLE = 'font-size:14.5px;line-height:1.7;color:#22314E;word-break:break-word;';

Page({
  data: {
    sessionId: '',
    title: 'Call Me',
    // { role, content, html?, thinking?, thinkingExpanded?, image?, streaming?, error? }
    messages: [],
    inputValue: '',
    pendingImage: null, // { path: 本地预览路径, data: base64 dataURL }
    sending: false,
    canSend: false,
    toView: '',
    keyboardHeight: 0, // 键盘弹起高度（px），用于输入栏上移至键盘正上方
    mdTagStyle: MD_TAG_STYLE,
    mdContainerStyle: MD_CONTAINER_STYLE,
  },

  onLoad(options) {
    const { id, title } = options || {};
    if (!id) {
      this.toast('缺少会话 ID');
      setTimeout(() => wx.navigateBack({ delta: 1 }), 800);
      return;
    }
    this.setData({
      sessionId: id,
      title: title ? decodeURIComponent(title) : 'Call Me',
    });
    this.loadHistory();
  },

  onUnload() {
    if (this._renderTimer) {
      clearTimeout(this._renderTimer);
      this._renderTimer = null;
    }
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  scrollToBottom() {
    this.setData({ toView: '' }, () => {
      this.setData({ toView: 'msg-bottom' });
    });
  },

  // ---------- Markdown 渲染 ----------
  // 渲染失败返回空串，前端回退为纯文本展示
  renderMd(text) {
    if (!text) return '';
    try {
      return md.render(text);
    } catch (e) {
      return '';
    }
  },

  // 流式期间节流渲染（150ms），避免每个分片都 setData
  scheduleRender(index) {
    if (this._renderTimer) return;
    this._renderTimer = setTimeout(() => {
      this._renderTimer = null;
      const msg = this.data.messages[index];
      if (msg) this.setData({ [`messages[${index}].html`]: this.renderMd(msg.content) });
    }, 150);
  },

  // 立即渲染（回答完成/出错/收尾时调用）
  renderNow(index) {
    if (this._renderTimer) {
      clearTimeout(this._renderTimer);
      this._renderTimer = null;
    }
    const msg = this.data.messages[index];
    if (msg) this.setData({ [`messages[${index}].html`]: this.renderMd(msg.content) });
  },

  // 展开/收起思考过程
  toggleThinking(e) {
    const index = e.currentTarget.dataset.index;
    const msg = this.data.messages[index];
    if (msg && !msg.streaming) {
      this.setData({ [`messages[${index}].thinkingExpanded`]: !msg.thinkingExpanded });
    }
  },

  // ---------- 历史消息 ----------
  // 对 WeKnora 会话详情结构做兼容处理，实际字段以云端返回为准
  async loadHistory() {
    try {
      const data = await request({ url: `/api/v1/callme/sessions/${this.data.sessionId}` });
      const raw = data.messages || data.message_list || data.history || [];
      const messages = [];
      for (const m of raw) {
        const role = m.role === 'user' ? 'user' : m.role === 'assistant' || m.role === 'ai' ? 'assistant' : null;
        const content = m.content || m.message || '';
        if (!role || !content) continue;
        messages.push({
          role,
          content,
          html: role === 'assistant' ? this.renderMd(content) : '',
        });
      }
      if (messages.length) {
        this.setData({ messages });
        this.scrollToBottom();
      }
    } catch (err) {
      // 历史加载失败不阻断对话
    }
  },

  onInput(e) {
    this.setData({
      inputValue: e.detail.value,
      canSend: !!e.detail.value.trim() && !this.data.sending,
    });
  },

  // 键盘高度变化：输入栏整体上移，底边与键盘顶齐平（页面不被顶起，标题栏保留原位；
  // 输入栏自身的白色内边距即作为与键盘间的留白），弹起时滚动到最新消息
  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
    if (h > 0) this.scrollToBottom();
  },

  // 选择图片（base64 上送，无需 COS，见指南第六节）
  chooseImage() {
    if (this.data.sending) return;
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      success: (res) => {
        const file = res.tempFiles[0];
        const path = file.tempFilePath;
        wx.getFileSystemManager().readFile({
          filePath: path,
          encoding: 'base64',
          success: (r) => {
            const ext = (path.split('.').pop() || 'jpeg').toLowerCase();
            const mime = ext === 'png' ? 'png' : 'jpeg';
            this.setData({
              pendingImage: { path, data: `data:image/${mime};base64,${r.data}` },
            });
          },
          fail: () => this.toast('图片读取失败'),
        });
      },
    });
  },

  removeImage() {
    this.setData({ pendingImage: null });
  },

  previewImage(e) {
    wx.previewImage({ urls: [e.currentTarget.dataset.src] });
  },

  // ---------- 发送与 SSE 流式接收 ----------
  onSend() {
    const query = this.data.inputValue.trim();
    if (!query || this.data.sending) return;

    const { pendingImage } = this.data;
    const messages = this.data.messages.concat([
      { role: 'user', content: query, image: pendingImage ? pendingImage.path : '' },
      {
        role: 'assistant',
        content: '',
        html: '',
        thinking: '',
        thinkingExpanded: false,
        streaming: true,
        error: false,
      },
    ]);
    const answerIndex = messages.length - 1;

    this.setData({
      messages,
      inputValue: '',
      pendingImage: null,
      sending: true,
      canSend: false,
    });
    this.scrollToBottom();

    const payload = { session_id: this.data.sessionId, query };
    if (pendingImage) payload.images = [{ data: pendingImage.data }];

    const token = wx.getStorageSync('token');
    const parser = createSseParser((evt) => this.onSseEvent(evt, answerIndex));

    const requestTask = wx.request({
      url: `${BASE_URL}/api/v1/callme/chat`,
      method: 'POST',
      header: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      data: payload,
      enableChunked: true, // 关键：启用分块传输（见指南 5.1）
      responseType: 'text',
      timeout: 120000,
      success: () => {
        parser.end();
        this.finalizeAnswer(answerIndex);
      },
      fail: () => {
        this.finalizeAnswer(answerIndex, '网络异常，请稍后重试');
      },
    });

    if (requestTask && requestTask.onChunkReceived) {
      requestTask.onChunkReceived((res) => {
        parser.push(arrayBufferToString(res.data));
        this.scrollToBottom();
      });
    }
  },

  // 处理服务端归一化后的 SSE 事件：{ type: 'thinking|answer|done|error', content }
  onSseEvent(evt, index) {
    const key = `messages[${index}]`;
    const msg = this.data.messages[index];
    if (!msg) return;

    if (evt.type === 'answer' && evt.content) {
      this.setData({ [`${key}.content`]: msg.content + evt.content });
      this.scheduleRender(index);
    } else if (evt.type === 'thinking' && evt.content) {
      this.setData({ [`${key}.thinking`]: (msg.thinking || '') + evt.content });
    } else if (evt.type === 'error') {
      this.setData({
        [`${key}.content`]: evt.content || 'AI 服务返回错误',
        [`${key}.error`]: true,
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
      });
      this.renderNow(index);
      this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
    } else if (evt.type === 'done') {
      // 回答完毕：思考内容自动折叠
      this.setData({ [`${key}.streaming`]: false, [`${key}.thinkingExpanded`]: false });
      this.renderNow(index);
      this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
    }
  },

  // 流结束收尾：无内容且非错误时给出兜底提示
  finalizeAnswer(index, errorMessage) {
    const key = `messages[${index}]`;
    const msg = this.data.messages[index];
    if (msg) {
      if (errorMessage && !msg.content) {
        this.setData({
          [`${key}.content`]: errorMessage,
          [`${key}.error`]: true,
        });
      } else if (!msg.content && !msg.error) {
        this.setData({ [`${key}.content`]: '（未收到有效回答，请重试）' });
      }
      this.setData({ [`${key}.streaming`]: false, [`${key}.thinkingExpanded`]: false });
      this.renderNow(index);
    }
    this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
    this.scrollToBottom();
  },
});
