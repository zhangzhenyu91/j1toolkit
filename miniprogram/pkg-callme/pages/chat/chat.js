// Call Me · 对话页：SSE 流式接收（对接细节见《开发指南》第六章）
// 回答经 markdown-it 渲染为 HTML 后由 mp-html 展示（支持表格/标题/加粗/代码块等）；
// 思考内容流式期间展开、回答完毕自动折叠，可点击再展开；
// 归一化事件：thinking / answer / preamble / tool_start / tool_end / title / references / done / error
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { request, handleUnauthorized } from '../../../utils/request';
import { createSseStream, createIdleWatchdog, isSuccessfulStatus } from '../../utils/sse';
import { createChunkGate, abortTask } from '../../utils/stream-control';
import { shareAppMessage } from '../../../utils/share';
import { buildMultipartBody } from '../../../utils/multipart';
import { createRequestGuard } from '../../../utils/generation';
import {
  ATTACH_EXTS,
  isAllowedAttachment,
  isAudioAttachment,
} from '../../../utils/attach';

// Markdown 排版样式：tag 样式与渲染实例统一用 utils/markdown.js 那一份（「政企蓝白」色板，mp-html 按标签名生效）；
// 经 getMarkdownStyle 按系统主题取 tag/容器排版样式（含深色正文色），容器样式本地覆盖——
// 聊天气泡字号略大（14.5px），其余与共享口径一致；渲染安全策略不变（markdown-it html:false + breaks:false）
const { renderMarkdownRaw, getMarkdownStyle } = require('../../../utils/markdown');
const { parseDate, pad, fmtSize, extOf } = require('../../../utils/util');

// 对话页排版样式绑定值：共享口径 + 容器 14.5px 字号覆盖（theme 缺省读系统主题）
function chatMdStyle(theme) {
  const style = getMarkdownStyle(theme);
  return {
    mdTagStyle: style.mdTagStyle,
    mdContainerStyle: style.mdContainerStyle.replace('font-size:14px', 'font-size:14.5px'),
  };
}

// 附件上限（白名单见 utils/attach.js，与后端 attachmentExts 对齐；wx.chooseMessageFile 过滤 + 端侧复核）
const ATTACH_MAX = 5;
const ATTACH_MAX_SIZE = 50 * 1024 * 1024;

// 思考强度档位中文映射（胶囊与选项文案用；未知档位原样显示）
const EFFORT_LABELS = {
  off: '关闭',
  auto: '自动',
  minimal: '极简',
  low: '较低',
  medium: '中等',
  high: '较高',
  xhigh: '超高',
  max: '最高',
};
function effortLabel(effort) {
  if (!effort) return '默认';
  return EFFORT_LABELS[effort] || effort;
}

// 消息时间：当天显示 HH:mm，跨天显示 MM-DD HH:mm
function timeTextOf(input) {
  const d = input ? parseDate(input) : new Date();
  if (!d) return '';
  const sameDay = d.toDateString() === new Date().toDateString();
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return sameDay ? hm : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`;
}

Page({
  data: {
    sessionId: '',
    title: 'Call Me',
    // { key, msgId?, role, content, html?, steps?, stepsMeta?, thinkLabel?, thinkingExpanded?,
    //   references?, image?, streaming?, error?, timeText?, createdAt?, copied? }
    // steps：步骤树节点（后端已展示就绪，端侧只渲染）
    //   { sid, kind:'thought', icon, text } | { sid, kind:'tool', icon, id, name, title, summary, status, durationMs }
    messages: [],
    hasMore: false, // 是否还有更早的历史消息（before_time 分页）
    inputValue: '',
    pendingImage: null, // { path: 本地预览与 multipart 原文件路径 }
    // 附件芯片：{ key, id, name, sizeText, icon, status: uploading|parsing|ready|failed, progress, error? }
    attachments: [],
    webSearchSupported: false, // 后端支持联网搜索时才显示开关胶囊
    webSearchOn: false,
    models: [], // 可选模型 [{ id, name, supports_vision, thinking_levels? }]
    modelId: '', // 空 = 服务端默认模型（发送时不传 model_id）
    modelName: '',
    thinkingLevels: [], // 当前模型的思考强度档位清单（空 = 不支持，隐藏思考胶囊）
    reasoningEffort: '', // 思考强度档位（空 = 默认，发送时不传 reasoning_effort）
    reasoningLabel: '默认', // 思考胶囊档位文案（思考·{档位}）
    // 选项弹层（思考强度/模型选择共用；wx.showActionSheet 限 6 项，思考档位连默认共 7 项放不下会静默失败）
    sheetShow: false,
    sheetTitle: '',
    sheetOptions: [], // [{ label, value }]
    sheetTarget: '', // 'effort' | 'model'
    sheetValue: '',
    sending: false,
    canSend: false,
    toView: '',
    keyboardHeight: 0, // 键盘弹起高度（px），用于输入栏上移至键盘正上方
    ...chatMdStyle('light'), // 标签/容器排版样式计算值（随系统主题刷新，不重建已渲染 HTML）
  },

  onLoad(options) {
    const { id, title } = options || {};
    if (!id) {
      this.toast('缺少会话 ID');
      setTimeout(
        () =>
          wx.navigateBack({
            delta: 1,
            // 页面栈仅 1 页（分享卡片首屏进入）时 navigateBack 静默失败，兜底回会话列表
            fail: () => wx.reLaunch({ url: '/pkg-callme/pages/sessions/sessions' }),
          }),
        800
      );
      return;
    }
    this._keySeq = 0; // 消息本地唯一 key（wx:key 用，删除后不复用）
    this._stepSeq = 0; // 步骤树节点唯一序号（wx:key 用）
    this._attSeq = 0; // 附件芯片本地唯一 key（wx:key 用）
    this._pollTimers = {}; // 附件解析轮询计时器（按芯片 key 管理）
    this._chatGuard = createRequestGuard(); // 本轮发送代际：旧 success/fail/SSE 不得停新 watchDog 或收尾新回答
    this._loadingEarlier = false;
    this.setData({
      sessionId: id,
      title: title ? decodeURIComponent(title) : '新对话',
    });
    // 系统主题变化时刷新排版样式绑定值（不重建已渲染 HTML）
    this._onThemeChange = (res) => this.setData(chatMdStyle(res && res.theme));
    this.setData(chatMdStyle());
    wx.onThemeChange(this._onThemeChange);
    this.loadHistory();
    this.loadCapabilities();
  },

  onShow() {
    // 回前台同步排版样式（主题可能在后台切换）
    this.setData(chatMdStyle());
  },

  onUnload() {
    this._unloaded = true;
    if (this._onThemeChange) {
      wx.offThemeChange(this._onThemeChange);
      this._onThemeChange = null;
    }
    if (this._renderTimer) {
      clearTimeout(this._renderTimer);
      this._renderTimer = null;
    }
    if (this._scrollTimer) {
      clearTimeout(this._scrollTimer);
      this._scrollTimer = null;
    }
    if (this._chatWatchdog) {
      this._chatWatchdog.stop();
      this._chatWatchdog = null;
    }
    // 卸载时中止当前流式请求，避免后台连接继续回调/占用网络。
    abortTask(this._chatRequestTask);
    this._chatRequestTask = null;
    // 附件解析轮询计时清掉，避免页面销毁后仍在 setData
    Object.keys(this._pollTimers || {}).forEach((k) => clearTimeout(this._pollTimers[k]));
    this._pollTimers = {};
    if (this._chatGuard) this._chatGuard.invalidate();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  scrollToBottom() {
    this.setData({ toView: '' }, () => {
      this.setData({ toView: 'msg-bottom' });
    });
  },

  // 流式期间节流滚动（300ms），避免每个分片都触发 scroll-into-view
  scheduleScroll() {
    if (this._scrollTimer) return;
    this._scrollTimer = setTimeout(() => {
      this._scrollTimer = null;
      this.scrollToBottom();
    }, 300);
  },

  // 步骤树限高盒滚动跟随到底（与网页端 scrollTop=scrollHeight 等效：
  // scroll-view 不能用 overflow 滚动，只能靠 scroll-into-view 驱动）
  scrollStepEnd(index) {
    const msg = this.data.messages[index];
    if (!msg || !msg.streaming) return;
    const key = `messages[${index}].stepToView`;
    this.setData({ [key]: '' }, () => {
      this.setData({ [key]: `se-${msg.key}` });
    });
  },

  // ---------- Markdown 渲染 ----------
  // 复用公共渲染实例（breaks:false 对话页口径）；渲染失败返回空串，前端回退为纯文本展示
  renderMd(text) {
    if (!text) return '';
    try {
      return renderMarkdownRaw(text, { breaks: false });
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

  // ---------- 步骤树 ----------
  // 工具节点图标（与网页端同一套口径）
  toolIcon(name) {
    if (
      name === 'knowledge_search' ||
      name === 'search_knowledge' ||
      name === 'grep_chunks' ||
      name === 'web_search'
    ) {
      return 'search';
    }
    if (name === 'image_analysis') return 'image';
    return 'tools';
  },

  // 服务端步骤补本地渲染字段（sid / icon）
  prepareSteps(steps) {
    return steps.map((s) => {
      this._stepSeq += 1;
      return {
        ...s,
        sid: `s${this._stepSeq}`,
        icon: s.kind === 'thought' ? 'lightbulb' : this.toolIcon(s.name),
      };
    });
  },

  // 耗时文案（官方格式）：<1s 显示 ms，<60s 显示 s，否则 Xm Ys
  fmtDuration(ms) {
    if (!ms || ms <= 0) return '';
    if (ms < 1000) return `${Math.round(ms)}ms`;
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  },

  // 完结后的思考摘要（官方口径）：思考 N 轮 · 调用 N 次工具 · 耗时 X
  summaryText(steps, meta, durationMs) {
    const tools =
      meta && meta.tools != null ? meta.tools : steps.filter((s) => s.kind === 'tool').length;
    const rounds =
      meta && meta.rounds ? meta.rounds : steps.filter((s) => s.kind === 'thought').length;
    const parts = [];
    if (rounds) parts.push(`思考 ${rounds} 轮`);
    if (tools) parts.push(`调用 ${tools} 次工具`);
    const dur = this.fmtDuration(durationMs);
    if (dur) parts.push(`耗时 ${dur}`);
    return parts.join(' · ') || '思考过程';
  },

  // 流收尾：残留 running 节点落终态 + 生成完结摘要（done/error/finalize 共用）
  finishSteps(key, msg, isError) {
    const patch = {};
    msg.steps.forEach((s, i) => {
      if (s.kind === 'tool' && s.status === 'running') {
        patch[`${key}.steps[${i}].status`] = isError ? 'error' : 'done';
      }
    });
    if (msg.steps.length) {
      const durationMs = msg._thinkStart ? Date.now() - msg._thinkStart : 0;
      patch[`${key}.thinkLabel`] = this.summaryText(msg.steps, msg.stepsMeta, durationMs);
    }
    return patch;
  },

  // 展开/收起思考过程
  toggleThinking(e) {
    const index = e.currentTarget.dataset.index;
    const msg = this.data.messages[index];
    if (msg && !msg.streaming) {
      this.setData({ [`messages[${index}].thinkingExpanded`]: !msg.thinkingExpanded });
    }
  },

  // 复制回答内容到剪贴板（图标短暂变为"已复制"）
  onCopy(e) {
    const index = e.currentTarget.dataset.index;
    const msg = this.data.messages[index];
    if (!msg || !msg.content) return;
    wx.setClipboardData({
      data: msg.content,
      success: () => {
        const key = `messages[${index}].copied`;
        this.setData({ [key]: true });
        setTimeout(() => this.setData({ [key]: false }), 1500);
      },
    });
  },

  // 删除本条消息（与复制同行；问答成对删除，确认后调后端接口）
  onDelete(e) {
    if (this.data.sending) return;
    const index = e.currentTarget.dataset.index;
    const msg = this.data.messages[index];
    if (!msg) return;
    if (!msg.msgId) {
      this.toast('消息同步中，请稍后再试');
      return;
    }
    wx.showModal({
      title: '删除问答',
      content: '确定删除这条问答吗？对应的问题和回答将一并删除，且不可恢复。',
      confirmText: '删除',
      confirmColor: '#F53F3F',
      success: async (res) => {
        if (!res.confirm) return;
        try {
          await request({
            url: `/api/v1/callme/sessions/${this.data.sessionId}/messages/${msg.msgId}`,
            method: 'DELETE',
          });
          // 本地同步移除该回答及其上方最近的一条问题
          const messages = this.data.messages.slice();
          let qIndex = -1;
          for (let i = index - 1; i >= 0; i -= 1) {
            if (messages[i].role === 'user') {
              qIndex = i;
              break;
            }
          }
          messages.splice(index, 1);
          if (qIndex >= 0) messages.splice(qIndex, 1);
          this.setData({ messages });
          this.toast('已删除');
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 静默同步本轮新消息的服务端 msgId（历史消息已带 id；仅更新尾部一两条，不重绘列表）
  async syncRecentIds() {
    try {
      const data = await request({ url: `/api/v1/callme/sessions/${this.data.sessionId}` });
      const raw = data.messages || [];
      // 服务端已按时间升序，从尾部找最近的一问一答
      let assistant = null;
      let user = null;
      for (let i = raw.length - 1; i >= 0; i -= 1) {
        const role = raw[i].role;
        if (!assistant && (role === 'assistant' || role === 'ai')) {
          assistant = raw[i];
        } else if (!user && role === 'user') {
          user = raw[i];
          break;
        }
      }
      const updates = {};
      const messages = this.data.messages;
      for (let i = messages.length - 1; i >= 0 && i >= messages.length - 2; i -= 1) {
        const m = messages[i];
        if (m.role === 'assistant' && assistant) {
          if (!m.msgId) updates[`messages[${i}].msgId`] = assistant.id;
          // 服务端持久化的步骤树与统计（轮次/耗时）更精确，静默校正本地实时构建的版本
          if (Array.isArray(assistant.steps) && assistant.steps.length) {
            updates[`messages[${i}].steps`] = this.prepareSteps(assistant.steps);
            updates[`messages[${i}].stepsMeta`] = assistant.steps_meta || null;
            updates[`messages[${i}].thinkLabel`] = this.summaryText(
              assistant.steps,
              assistant.steps_meta,
              (assistant.steps_meta && assistant.steps_meta.durationMs) || 0
            );
          }
        }
        if (m.role === 'user' && user && !m.msgId) {
          updates[`messages[${i}].msgId`] = user.id;
        }
      }
      if (Object.keys(updates).length) this.setData(updates);
    } catch (err) {
      // 同步失败不影响使用（下次进入会话时历史消息自带 id）
    }
  },

  // ---------- 历史消息 ----------
  // 消息取自 GET /sessions/:id（后端合并 WeKnora /messages/:id/load 后按时间升序返回）。
  // assistant 消息的步骤树由后端从 agent_steps 构建为展示就绪的 steps/steps_meta，端侧只渲染。
  buildHistoryMessage(m) {
    const role =
      m.role === 'user' ? 'user' : m.role === 'assistant' || m.role === 'ai' ? 'assistant' : null;
    const content = m.content || m.message || m.text || '';
    if (!role || !content) return null;
    const references = [];
    if (role === 'assistant' && Array.isArray(m.knowledge_references)) {
      // 引用来源：按知识条目去重（与流式 references 事件同构）
      const seen = new Set();
      for (const r of m.knowledge_references) {
        const rid = r.knowledge_id || r.id;
        if (!rid || seen.has(rid)) continue;
        seen.add(rid);
        references.push({
          id: rid,
          title: r.knowledge_title || r.knowledge_filename || '未命名资料',
        });
      }
    }
    const steps =
      role === 'assistant' && Array.isArray(m.steps) && m.steps.length
        ? this.prepareSteps(m.steps)
        : [];
    const stepsMeta = m.steps_meta || null;
    this._keySeq += 1;
    return {
      key: `k${this._keySeq}`,
      msgId: m.id,
      role,
      content,
      html: role === 'assistant' ? this.renderMd(content) : '',
      steps,
      stepsMeta,
      thinkLabel: steps.length
        ? this.summaryText(steps, stepsMeta, (stepsMeta && stepsMeta.durationMs) || 0)
        : '',
      thinkingExpanded: false,
      references,
      timeText: timeTextOf(m.created_at),
      createdAt: m.created_at || '',
    };
  },

  async loadHistory() {
    try {
      const data = await request({
        url: `/api/v1/callme/sessions/${this.data.sessionId}?limit=50`,
      });
      const raw = data.messages || data.message_list || data.history || [];
      const messages = [];
      for (const m of raw) {
        const msg = this.buildHistoryMessage(m);
        if (msg) messages.push(msg);
      }
      this.setData({ messages, hasMore: !!data.has_more });
      if (messages.length) this.scrollToBottom();
    } catch (err) {
      // 历史加载失败不阻断对话
    }
  },

  // 加载更早消息（before_time 分页；加载后滚动位置锚定在原首条）
  // 流式期间直接返回：此时往列表头部插旧消息，SSE 仍按发送时的下标回写会错位到别的气泡
  async loadEarlier() {
    if (this.data.sending) return;
    if (this._loadingEarlier || !this.data.hasMore) return;
    const first = this.data.messages[0];
    if (!first || !first.createdAt) return;
    this._loadingEarlier = true;
    try {
      const data = await request({
        url: `/api/v1/callme/sessions/${this.data.sessionId}?limit=50&before_time=${encodeURIComponent(
          first.createdAt
        )}`,
      });
      const raw = data.messages || [];
      const older = [];
      for (const m of raw) {
        const msg = this.buildHistoryMessage(m);
        if (msg) older.push(msg);
      }
      const anchor = `m-${first.key}`;
      this.setData({ messages: older.concat(this.data.messages), hasMore: !!data.has_more }, () => {
        this.setData({ toView: '' }, () => this.setData({ toView: anchor }));
      });
    } catch (err) {
      this.toast('更早消息加载失败，请稍后重试');
    } finally {
      this._loadingEarlier = false;
    }
  },

  onInput(e) {
    this.setData({ inputValue: e.detail.value }, () => this.refreshCanSend());
  },

  // 发送可用性：有内容、非发送中、无上传中的附件（解析中不阻塞）
  refreshCanSend() {
    const uploading = this.data.attachments.some((a) => a.status === 'uploading');
    this.setData({ canSend: !!this.data.inputValue.trim() && !this.data.sending && !uploading });
  },

  // 键盘高度变化：输入栏整体上移，底边与键盘顶齐平（页面不被顶起，标题栏保留原位；
  // 输入栏自身的内边距即作为与键盘间的留白），弹起时滚动到最新消息
  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
    if (h > 0) this.scrollToBottom();
  },

  // 「+」钮：添加图片 / 添加附件（分别走现有 chooseImage / chooseFile 通道）
  onAddTap() {
    if (this.data.sending) return;
    wx.showActionSheet({
      itemList: ['添加图片', '添加附件'],
      success: (res) => {
        if (res.tapIndex === 0) this.chooseImage();
        else if (res.tapIndex === 1) this.chooseFile();
      },
    });
  },

  // 选择图片（保留原文件，multipart 二进制上送，统一 50MB 上限）
  chooseImage() {
    if (this.data.sending) return;
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      success: (res) => {
        const file = res.tempFiles[0];
        if (file.size && file.size > 50 * 1024 * 1024) {
          this.toast('图片不能超过 50MB，请压缩后再试');
          return;
        }
        this.setData({ pendingImage: { path: file.tempFilePath } });
      },
    });
  },

  removeImage() {
    this.setData({ pendingImage: null });
  },

  previewImage(e) {
    wx.previewImage({ urls: [e.currentTarget.dataset.src] });
  },

  // ---------- 附件 ----------
  // 选择附件（wx.chooseMessageFile 从聊天文件选取，白名单过滤 + 端侧复核，同 worklog 先例）
  chooseFile() {
    if (this.data.sending) return;
    const left = ATTACH_MAX - this.data.attachments.length;
    if (left <= 0) {
      this.toast(`附件最多 ${ATTACH_MAX} 个`);
      return;
    }
    wx.chooseMessageFile({
      count: left,
      type: 'file',
      extension: ATTACH_EXTS,
      success: (res) => {
        (res.tempFiles || []).slice(0, left).forEach((t) => {
          if (!isAllowedAttachment(t.name)) {
            this.toast(`不支持的文件类型：${t.name}`);
            return;
          }
          if (t.size > ATTACH_MAX_SIZE) {
            this.toast('单个附件不超过 50MB');
            return;
          }
          this.addAttachment({ name: t.name, size: t.size, path: t.path });
        });
      },
    });
  },

  // 附件入列并启动上传 → 成功后转入解析轮询
  addAttachment({ name, size, path }) {
    this._attSeq += 1;
    const key = `a${this._attSeq}`;
    const chip = {
      key,
      id: '',
      name,
      sizeText: fmtSize(size || 0),
      icon: isAudioAttachment(name) ? 'music' : 'attach',
      status: 'uploading',
      progress: 0,
    };
    this.setData({ attachments: this.data.attachments.concat([chip]) }, () =>
      this.refreshCanSend()
    );
    this.uploadAttachment(key, path, name)
      .then((data) => {
        if (this._unloaded) return;
        const idx = this.data.attachments.findIndex((a) => a.key === key);
        if (idx < 0) return;
        if (!data || !data.id) {
          this.setData({ [`attachments[${idx}].status`]: 'failed' }, () => this.refreshCanSend());
          this.toast('上传响应异常，请重试');
          return;
        }
        this.setData(
          {
            [`attachments[${idx}].id`]: data.id,
            [`attachments[${idx}].status`]: 'parsing',
            [`attachments[${idx}].progress`]: 100,
          },
          () => this.refreshCanSend()
        );
        this.pollAttachment(key, data.id);
      })
      .catch((err) => {
        if (this._unloaded) return;
        const idx = this.data.attachments.findIndex((a) => a.key === key);
        if (idx < 0) return;
        this.setData({ [`attachments[${idx}].status`]: 'failed' }, () => this.refreshCanSend());
        this.toast(err.message);
      });
  },

  // 单个附件上传（wx.uploadFile 手动带 Authorization；onProgressUpdate 更新芯片进度）
  // uploadFile 的 multipart 文件名只能是临时路径 basename，原始文件名走表单字段 name，由后端读取
  uploadAttachment(key, filePath, name) {
    const token = wx.getStorageSync('token');
    return new Promise((resolve, reject) => {
      const task = wx.uploadFile({
        url: `${BASE_URL}/api/v1/callme/sessions/${this.data.sessionId}/attachments`,
        filePath,
        name: 'file',
        header: token ? { Authorization: `Bearer ${token}` } : {},
        // uploadFile 以临时路径 basename 为 multipart 文件名，原始文件名走表单字段（同 worklog 先例）
        formData: { name },
        success: (res) => {
          let body = {};
          try {
            body = JSON.parse(res.data || '{}');
          } catch (e) {
            // 非 JSON 响应按失败处理
          }
          if (
            res.statusCode >= 200 &&
            res.statusCode < 300 &&
            (body.code === 0 || body.success === true)
          ) {
            resolve(body.data || {});
            return;
          }
          reject(new Error(body.message || `附件上传失败（${res.statusCode}）`));
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
      task.onProgressUpdate((r) => {
        const idx = this.data.attachments.findIndex((a) => a.key === key);
        if (idx >= 0) this.setData({ [`attachments[${idx}].progress`]: r.progress });
      });
    });
  },

  // 附件解析状态轮询：上传成功后每 1s 拉列表直至 ready / failed（芯片被移除或页面卸载即终止）
  pollAttachment(key, id) {
    this._pollTimers[key] = setTimeout(async () => {
      delete this._pollTimers[key];
      if (this._unloaded) return;
      try {
        const list = await request({
          url: `/api/v1/callme/sessions/${this.data.sessionId}/attachments`,
        });
        if (this._unloaded) return;
        const found = (list || []).find((a) => a.id === id);
        const idx = this.data.attachments.findIndex((a) => a.key === key);
        if (idx < 0) return;
        if (found && (found.status === 'ready' || found.status === 'failed')) {
          this.setData({
            [`attachments[${idx}].status`]: found.status,
            [`attachments[${idx}].error`]: found.error_message || '',
          });
          return;
        }
      } catch (err) {
        // 拉取失败不视为附件失败，继续轮询
      }
      this.pollAttachment(key, id);
    }, 1000);
  },

  // 移除附件芯片（仅本地移除；解析失败标红的芯片也经此清理）
  removeAttachment(e) {
    const { key } = e.currentTarget.dataset;
    if (this._pollTimers[key]) {
      clearTimeout(this._pollTimers[key]);
      delete this._pollTimers[key];
    }
    this.setData({ attachments: this.data.attachments.filter((a) => a.key !== key) }, () =>
      this.refreshCanSend()
    );
  },

  // ---------- 模型与联网搜索 ----------
  // 能力查询：可选模型清单 + 联网搜索支持与否，本地记忆上次选择；失败不阻断对话（胶囊保持隐藏）
  async loadCapabilities() {
    try {
      const data = await request({ url: '/api/v1/callme/capabilities' });
      const models = Array.isArray(data && data.models) ? data.models : [];
      const savedId = wx.getStorageSync('callme_model_id');
      const saved = models.find((m) => m.id === savedId);
      this.setData({
        models,
        modelId: saved ? saved.id : '',
        modelName: saved ? saved.name : '',
        webSearchSupported: !!(data && data.web_search),
        webSearchOn: !!(data && data.web_search) && !!wx.getStorageSync('callme_web_search'),
      });
      this.applyThinkingLevels();
    } catch (err) {
      // 查询失败：胶囊隐藏，发送不带扩展参数
    }
  },

  toggleWebSearch() {
    if (!this.data.webSearchSupported) return;
    const on = !this.data.webSearchOn;
    this.setData({ webSearchOn: on });
    wx.setStorageSync('callme_web_search', on);
  },

  // 当前模型思考强度清单：刷新思考胶囊显隐，并校验本地记忆档位（切模型后不在新清单则回退默认）
  applyThinkingLevels() {
    const cur = this.data.models.find((m) => m.id === this.data.modelId);
    const levels = cur && Array.isArray(cur.thinking_levels) ? cur.thinking_levels : [];
    const saved = wx.getStorageSync('callme_reasoning_effort') || '';
    const effort = levels.includes(saved) ? saved : '';
    this.setData({
      thinkingLevels: levels,
      reasoningEffort: effort,
      reasoningLabel: effortLabel(effort),
    });
  },

  // 思考强度选择：默认（不传）+ 当前模型支持的档位（中文映射文案）
  // 自绘底部弹层（与模型选择共用）：wx.showActionSheet 上限 6 项，「默认 + 6 档」共 7 项会静默失败（点按无反应）
  chooseReasoning() {
    const levels = this.data.thinkingLevels;
    if (!levels.length) return;
    this.setData({
      sheetShow: true,
      sheetTitle: '思考强度',
      sheetTarget: 'effort',
      sheetValue: this.data.reasoningEffort,
      sheetOptions: [{ label: '默认（跟随模型）', value: '' }].concat(
        levels.map((l) => ({ label: effortLabel(l), value: l }))
      ),
    });
  },

  // 切换模型（仅 1 个可用模型时为禁用态）
  chooseModel() {
    const { models } = this.data;
    if (!models || models.length <= 1) return;
    this.setData({
      sheetShow: true,
      sheetTitle: '选择模型',
      sheetTarget: 'model',
      sheetValue: this.data.modelId,
      sheetOptions: models.map((m) => ({ label: m.name, value: m.id })),
    });
  },

  // 选项弹层：点选生效并关闭
  onSheetPick(e) {
    const { value } = e.currentTarget.dataset;
    if (this.data.sheetTarget === 'effort') {
      this.setData({ reasoningEffort: value, reasoningLabel: effortLabel(value) });
      wx.setStorageSync('callme_reasoning_effort', value);
    } else if (this.data.sheetTarget === 'model') {
      const m = this.data.models.find((x) => x.id === value);
      if (m) {
        this.setData({ modelId: m.id, modelName: m.name });
        wx.setStorageSync('callme_model_id', m.id);
        this.applyThinkingLevels();
      }
    }
    this.setData({ sheetShow: false });
  },

  closeSheet() {
    this.setData({ sheetShow: false });
  },

  noop() {},

  // ---------- 发送与 SSE 流式接收 ----------
  onSend() {
    const query = this.data.inputValue.trim();
    if (!query || this.data.sending) return;
    // 上传中禁止发送（解析中不阻塞：uploaded/processing/ready 均随本条上送，对齐 WeKnora 官方口径）
    if (this.data.attachments.some((a) => a.status === 'uploading')) {
      this.toast('附件上传中，请稍候');
      return;
    }

    const { pendingImage } = this.data;
    const sendIds = this.data.attachments
      .filter((a) => a.id && a.status !== 'failed')
      .map((a) => a.id);
    const userKey = `k${(this._keySeq += 1)}`;
    const answerKey = `k${(this._keySeq += 1)}`;
    const messages = this.data.messages.concat([
      {
        key: userKey,
        role: 'user',
        content: query,
        image: pendingImage ? pendingImage.path : '',
        timeText: timeTextOf(),
      },
      {
        key: answerKey,
        role: 'assistant',
        content: '',
        html: '',
        steps: [],
        stepsMeta: null,
        thinkLabel: '',
        thinkingExpanded: false,
        references: [],
        streaming: true,
        error: false,
      },
    ]);
    const answerIndex = messages.length - 1;

    this.setData({
      messages,
      inputValue: '',
      pendingImage: null,
      // 随本条发送的附件出列；仅失败的芯片保留待用户移除重传
      attachments: this.data.attachments.filter((a) => a.status === 'failed'),
      sending: true,
      canSend: false,
    });
    this.scrollToBottom();

    const payload = { session_id: this.data.sessionId, query };
    if (sendIds.length) payload.attachment_ids = sendIds;
    if (this.data.webSearchOn) payload.web_search = true; // 未开启不传
    if (this.data.modelId) payload.model_id = this.data.modelId; // 未选不传（服务端默认模型）
    if (this.data.reasoningEffort) payload.reasoning_effort = this.data.reasoningEffort; // 非默认档位才传

    const token = wx.getStorageSync('token');
    const ticket = this._chatGuard.begin('chat');
    const chunkGate = createChunkGate();
    const stream = createSseStream((evt) => {
      // 旧请求的迟到事件不得写入新回答
      if (!this._chatGuard.isCurrent(ticket)) return;
      // done/error（及 success/fail）后关闭 gate：迟到的分块不得再重臂 watchdog。
      chunkGate.markEvent(evt && evt.type);
      this.onSseEvent(evt, answerIndex);
    });
    let requestTask = null;
    // 静默看门狗：不限总时长；服务端 15s 心跳即活动证据，120s 无任何真实分块判定连接死亡
    const watchdog = createIdleWatchdog({
      timeoutMs: 120000,
      onTimeout: () => {
        if (requestTask) requestTask.abort();
      },
    });
    this._chatWatchdog = watchdog;
    const header = { ...(token ? { Authorization: `Bearer ${token}` } : {}) };
    let requestData = payload;
    if (pendingImage) {
      // 带图必须走 wx.request 手拼 multipart：wx.uploadFile 不回调 onChunkReceived，
      // 流式回答会被憋到 success 才整段解析；手拼还能保留字段值与标准边界格式。
      let imageData;
      try {
        imageData = wx.getFileSystemManager().readFileSync(pendingImage.path);
      } catch (err) {
        watchdog.stop();
        this._chatWatchdog = null;
        this.finalizeAnswer(answerIndex, '图片读取失败，请重新选择');
        return;
      }
      const boundary = `----ShadeCallMe${Date.now()}`;
      try {
        requestData = buildMultipartBody({
          boundary,
          fields: Object.keys(payload).map((key) => [
            key,
            typeof payload[key] === 'string' ? payload[key] : JSON.stringify(payload[key]),
          ]),
          files: [
            {
              field: 'image',
              name: `photo.${extOf(pendingImage.path) || 'jpg'}`,
              type: 'application/octet-stream',
              data: imageData,
            },
          ],
        });
      } catch (err) {
        // 手拼同步异常同样要收尾，否则 sending 永久为 true
        watchdog.stop();
        this._chatWatchdog = null;
        this.finalizeAnswer(answerIndex, '图片读取失败，请重新选择');
        return;
      } finally {
        // wx.request 的 data 只接受单个 ArrayBuffer，不能直接传 part 列表；此处构建最终请求体后
        // 立即释放源文件引用，避免长期同时持有 2×50MB（峰值仍为请求体 + 读取源两份）。
        imageData = null;
      }
      header['Content-Type'] = `multipart/form-data; boundary=${boundary}`;
    } else {
      header['Content-Type'] = 'application/json';
    }
    const options = {
      url: `${BASE_URL}/api/v1/callme/chat`,
      method: 'POST',
      header,
      data: requestData,
      enableChunked: true,
      responseType: 'text',
      timeout: 3600000,
      success: (res) => {
        // 旧请求的 success 不得停掉新请求 watchdog / 收尾新回答
        if (!this._chatGuard.isCurrent(ticket)) return;
        chunkGate.markEvent('success');
        if (this._chatRequestTask === requestTask) this._chatRequestTask = null;
        watchdog.stop();
        if (this._chatWatchdog === watchdog) this._chatWatchdog = null;
        const code = res && res.statusCode;
        if (code === 401) {
          handleUnauthorized();
          this.finalizeAnswer(answerIndex, '登录已过期，请重新登录');
          return;
        }
        if (!isSuccessfulStatus(code)) {
          let body = res && res.data;
          if (typeof body === 'string') {
            try {
              body = JSON.parse(body);
            } catch (e) {
              body = null;
            }
          }
          this.finalizeAnswer(
            answerIndex,
            (body && body.message) || `请求失败（${code || '未知'}）`
          );
          return;
        }
        // 仅成功响应才把完整 res.data 交给 SSE 回退（错误 JSON 不喂解析器）；
        // 未收到任何分块时由 fallback 解析完整响应体，已收分块则忽略避免重复。
        stream.fallback(res && res.data);
        stream.finish();
        this.finalizeAnswer(answerIndex);
      },
      fail: () => {
        if (!this._chatGuard.isCurrent(ticket)) return;
        chunkGate.markEvent('fail');
        if (this._chatRequestTask === requestTask) this._chatRequestTask = null;
        watchdog.stop();
        if (this._chatWatchdog === watchdog) this._chatWatchdog = null;
        this.finalizeAnswer(
          answerIndex,
          watchdog.hasFired() ? '响应超时，请稍后重试' : '网络异常，请稍后重试'
        );
      },
    };
    requestTask = wx.request(options);
    this._chatRequestTask = requestTask;
    watchdog.arm();
    if (requestTask && requestTask.onChunkReceived) {
      requestTask.onChunkReceived((res) => {
        if (!this._chatGuard.isCurrent(ticket)) return;
        const data = res && res.data;
        if (!data || !data.byteLength) return; // 空分块（心跳占位）不重臂
        stream.chunk(data);
        if (chunkGate.canArm()) watchdog.arm(); // done/error 后的迟到分块不得重臂
        this.scheduleScroll();
      });
    }
  },

  // 处理服务端归一化后的 SSE 事件：
  // { type: 'thinking|answer|preamble|tool_start|tool_end|title|references|done|error', ... }
  onSseEvent(evt, index) {
    const key = `messages[${index}]`;
    const msg = this.data.messages[index];
    if (!msg) return;

    if (evt.type === 'answer' && evt.content) {
      this.setData({ [`${key}.content`]: msg.content + evt.content });
      this.scheduleRender(index);
    } else if (evt.type === 'thinking' && evt.content) {
      // 追加到未封闭的思考节点；否则开新节点（新一轮 ReAct 的思考）
      if (!msg._thinkStart) msg._thinkStart = Date.now();
      const openIdx = msg._thoughtSi != null ? msg._thoughtSi : -1;
      if (openIdx >= 0 && msg.steps[openIdx] && msg.steps[openIdx].kind === 'thought') {
        this.setData(
          {
            [`${key}.steps[${openIdx}].text`]: msg.steps[openIdx].text + evt.content,
            [`${key}.thinkLabel`]: '正在思考…',
          },
          () => this.scrollStepEnd(index)
        );
      } else {
        this._stepSeq += 1;
        msg._thoughtSi = msg.steps.length;
        this.setData(
          {
            [`${key}.steps`]: msg.steps.concat([
              { sid: `s${this._stepSeq}`, kind: 'thought', icon: 'lightbulb', text: evt.content },
            ]),
            [`${key}.thinkLabel`]: '正在思考…',
          },
          () => this.scrollStepEnd(index)
        );
      }
    } else if (evt.type === 'preamble') {
      // 工具调用前的过渡语：从回答区挪入步骤树，作为完整的思考节点（对齐官方 UI 语义）
      if (msg.content) {
        if (!msg._thinkStart) msg._thinkStart = Date.now();
        this._stepSeq += 1;
        msg._thoughtSi = -1; // 过渡语节点已完整，后续思考另起节点
        this.setData(
          {
            [`${key}.steps`]: msg.steps.concat([
              { sid: `s${this._stepSeq}`, kind: 'thought', icon: 'lightbulb', text: msg.content },
            ]),
            [`${key}.content`]: '',
            [`${key}.html`]: '',
          },
          () => this.scrollStepEnd(index)
        );
      }
    } else if (evt.type === 'tool_start') {
      if (!msg._thinkStart) msg._thinkStart = Date.now();
      msg._thoughtSi = -1; // 封闭当前思考节点
      // WeKnora 对同一次调用发两帧（pending + hint），重复帧更新既有节点
      let dup = -1;
      if (evt.id) {
        for (let i = msg.steps.length - 1; i >= 0; i -= 1) {
          const s = msg.steps[i];
          if (s.kind === 'tool' && s.id === evt.id) {
            dup = i;
            break;
          }
        }
      }
      if (dup >= 0) {
        // 仅进行中节点回写标题：完成态节点不被迟到的重复帧改回「正在调用…」文案
        if (msg.steps[dup].status === 'running') {
          this.setData(
            {
              [`${key}.steps[${dup}].title`]: evt.title || msg.steps[dup].title,
              [`${key}.steps[${dup}].name`]: evt.name || msg.steps[dup].name,
              [`${key}.steps[${dup}].icon`]: this.toolIcon(evt.name || msg.steps[dup].name),
              [`${key}.thinkLabel`]: evt.title || msg.steps[dup].title,
            },
            () => this.scrollStepEnd(index)
          );
        }
      } else {
        this._stepSeq += 1;
        const node = {
          sid: `s${this._stepSeq}`,
          kind: 'tool',
          id: evt.id || '',
          name: evt.name || '',
          icon: this.toolIcon(evt.name),
          title: evt.title || '正在调用 工具…',
          summary: '',
          status: 'running',
          durationMs: 0,
        };
        this.setData(
          {
            [`${key}.steps`]: msg.steps.concat([node]),
            [`${key}.thinkLabel`]: node.title,
          },
          () => this.scrollStepEnd(index)
        );
      }
    } else if (evt.type === 'tool_end') {
      // 按 tool_call_id 精确匹配定位节点（同轮可并行多个同名调用、帧交错，必须精确优先，
      // 否则 running 兜底会让后开的并行节点抢答别人的完成事件）；仅无匹配时兜底取最后一个 running 节点
      let ti = -1;
      if (evt.id) {
        for (let i = msg.steps.length - 1; i >= 0; i -= 1) {
          const s = msg.steps[i];
          if (s.kind === 'tool' && s.id === evt.id) {
            ti = i;
            break;
          }
        }
      }
      if (ti < 0) {
        for (let j = msg.steps.length - 1; j >= 0; j -= 1) {
          const s2 = msg.steps[j];
          if (s2.kind === 'tool' && s2.status === 'running') {
            ti = j;
            break;
          }
        }
      }
      if (ti >= 0) {
        this.setData(
          {
            [`${key}.steps[${ti}].title`]: evt.title || msg.steps[ti].title,
            [`${key}.steps[${ti}].summary`]: evt.summary || '',
            [`${key}.steps[${ti}].status`]: evt.success === false ? 'error' : 'done',
            [`${key}.steps[${ti}].durationMs`]: evt.durationMs || 0,
            [`${key}.thinkLabel`]: evt.title || msg.steps[ti].title,
          },
          () => this.scrollStepEnd(index)
        );
      }
    } else if (evt.type === 'title' && evt.content) {
      // WeKnora 自动生成的会话标题，实时更新导航栏
      this.setData({ title: evt.content });
    } else if (evt.type === 'references' && Array.isArray(evt.list) && evt.list.length) {
      this.setData({ [`${key}.references`]: evt.list });
    } else if (evt.type === 'error') {
      this.stopChatWatchdog(); // 流已给出终态：停看门狗，避免稍后 abort 覆盖错误文案
      const patch = {
        [`${key}.content`]: evt.content || '服务返回错误',
        [`${key}.error`]: true,
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: timeTextOf(),
        ...this.finishSteps(key, msg, true),
      };
      this.setData(patch);
      this.renderNow(index);
      this.setData({ sending: false });
      this.refreshCanSend();
    } else if (evt.type === 'done') {
      this.stopChatWatchdog();
      // 回答完毕：步骤树自动折叠，记录完成时间与思考摘要
      const patch = {
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: timeTextOf(),
        ...this.finishSteps(key, msg, false),
      };
      this.setData(patch);
      this.renderNow(index);
      this.setData({ sending: false });
      this.refreshCanSend();
    }
  },

  // 流已收尾（done/error/success/fail）即停看门狗，避免完成后再 abort 覆盖终态
  stopChatWatchdog() {
    if (this._chatWatchdog) {
      this._chatWatchdog.stop();
      this._chatWatchdog = null;
    }
  },

  // 流结束收尾：无内容且非错误时给出兜底提示；仅成功轮次同步本轮消息 id
  finalizeAnswer(index, errorMessage) {
    this.stopChatWatchdog();
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
      const patch = {
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: msg.timeText || timeTextOf(),
      };
      // done/error 已收尾过步骤树（streaming 已置 false），这里只处理异常路径
      if (msg.streaming)
        Object.assign(patch, this.finishSteps(key, msg, !!errorMessage || msg.error));
      this.setData(patch);
      this.renderNow(index);
    }
    this.setData({ sending: false });
    this.refreshCanSend();
    // 失败轮次消息可能未落库，同步会把上一轮的 id 错配到本轮（误删风险），故仅成功时同步
    if (!errorMessage && msg && !msg.error) {
      this.syncRecentIds();
    }
    this.scrollToBottom();
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'call-me', title: 'Call Me' });
  },
});
