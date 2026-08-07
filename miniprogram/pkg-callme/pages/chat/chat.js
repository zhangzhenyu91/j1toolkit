// Call Me · 对话页：SSE 流式接收（对接细节见《开发指南》第六章）
// 回答经 markdown-it 渲染为 HTML 后由 mp-html 展示（支持表格/标题/加粗/代码块等）；
// 思考内容流式期间展开、回答完毕自动折叠，可点击再展开；
// 归一化事件：thinking / answer / preamble / title / references / done / error
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { request } from '../../../utils/request';
import { createUtf8Decoder, createSseParser } from '../../utils/sse';
import { shareAppMessage } from '../../../utils/share';

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

// 消息时间：当天显示 HH:mm，跨天显示 MM-DD HH:mm
function timeTextOf(input) {
  const d = input ? new Date(input) : new Date();
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
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
    this._keySeq = 0; // 消息本地唯一 key（wx:key 用，删除后不复用）
    this._stepSeq = 0; // 步骤树节点唯一序号（wx:key 用）
    this._loadingEarlier = false;
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
    if (this._scrollTimer) {
      clearTimeout(this._scrollTimer);
      this._scrollTimer = null;
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

  // ---------- 步骤树 ----------
  // 工具节点图标（与网页端同一套口径）
  toolIcon(name) {
    if (name === 'knowledge_search' || name === 'search_knowledge' || name === 'grep_chunks' || name === 'web_search') {
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
    const tools = meta && meta.tools != null ? meta.tools : steps.filter((s) => s.kind === 'tool').length;
    const rounds = meta && meta.rounds ? meta.rounds : steps.filter((s) => s.kind === 'thought').length;
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
      confirmColor: '#CF4444',
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
    const role = m.role === 'user' ? 'user' : m.role === 'assistant' || m.role === 'ai' ? 'assistant' : null;
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
        references.push({ id: rid, title: r.knowledge_title || r.knowledge_filename || '未命名资料' });
      }
    }
    const steps = role === 'assistant' && Array.isArray(m.steps) && m.steps.length
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
      const data = await request({ url: `/api/v1/callme/sessions/${this.data.sessionId}?limit=50` });
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
  async loadEarlier() {
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
        // 后端 JSON 上限 12mb，base64 约膨胀 1/3，原图限 8MB（与网页端一致）
        if (file.size && file.size > 8 * 1024 * 1024) {
          this.toast('图片不能超过 8MB，请压缩后再试');
          return;
        }
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
      sending: true,
      canSend: false,
    });
    this.scrollToBottom();

    const payload = { session_id: this.data.sessionId, query };
    if (pendingImage) payload.images = [{ data: pendingImage.data }];

    const token = wx.getStorageSync('token');
    const parser = createSseParser((evt) => this.onSseEvent(evt, answerIndex));
    const decoder = createUtf8Decoder();

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
      success: (res) => {
        // 冲刷解码器与解析器残余（错误响应非 SSE 格式，会被解析器自然忽略）
        parser.push(decoder.end());
        parser.end();
        // wx.request 的非 2xx 响应也走 success，必须在此判断（参数错误/鉴权过期/图片超限等）
        const code = res && res.statusCode;
        if (code === 401) {
          wx.removeStorageSync('token');
          wx.removeStorageSync('userInfo');
          wx.reLaunch({ url: '/pages/login/login' });
          this.finalizeAnswer(answerIndex, '登录已过期，请重新登录');
          return;
        }
        if (!code || code < 200 || code >= 300) {
          let body = res && res.data;
          if (typeof body === 'string') {
            try {
              body = JSON.parse(body);
            } catch (e) {
              body = null;
            }
          }
          this.finalizeAnswer(answerIndex, (body && body.message) || `请求失败（${code || '未知'}）`);
          return;
        }
        this.finalizeAnswer(answerIndex);
      },
      fail: () => {
        this.finalizeAnswer(answerIndex, '网络异常，请稍后重试');
      },
    });

    if (requestTask && requestTask.onChunkReceived) {
      requestTask.onChunkReceived((res) => {
        parser.push(decoder.push(res.data));
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
        this.setData(
          {
            [`${key}.steps[${dup}].title`]: evt.title || msg.steps[dup].title,
            [`${key}.steps[${dup}].name`]: evt.name || msg.steps[dup].name,
            [`${key}.steps[${dup}].icon`]: this.toolIcon(evt.name || msg.steps[dup].name),
            [`${key}.thinkLabel`]: evt.title || msg.steps[dup].title,
          },
          () => this.scrollStepEnd(index)
        );
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
      // 按 tool_call_id 定位节点，兜底取最后一个 running 节点
      let ti = -1;
      for (let i = msg.steps.length - 1; i >= 0; i -= 1) {
        const s = msg.steps[i];
        if (s.kind === 'tool' && ((evt.id && s.id === evt.id) || s.status === 'running')) {
          ti = i;
          break;
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
      const patch = {
        [`${key}.content`]: evt.content || 'AI 服务返回错误',
        [`${key}.error`]: true,
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: timeTextOf(),
        ...this.finishSteps(key, msg, true),
      };
      this.setData(patch);
      this.renderNow(index);
      this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
    } else if (evt.type === 'done') {
      // 回答完毕：步骤树自动折叠，记录完成时间与思考摘要
      const patch = {
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: timeTextOf(),
        ...this.finishSteps(key, msg, false),
      };
      this.setData(patch);
      this.renderNow(index);
      this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
    }
  },

  // 流结束收尾：无内容且非错误时给出兜底提示；仅成功轮次同步本轮消息 id
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
      const patch = {
        [`${key}.streaming`]: false,
        [`${key}.thinkingExpanded`]: false,
        [`${key}.timeText`]: msg.timeText || timeTextOf(),
      };
      // done/error 已收尾过步骤树（streaming 已置 false），这里只处理异常路径
      if (msg.streaming) Object.assign(patch, this.finishSteps(key, msg, !!errorMessage || msg.error));
      this.setData(patch);
      this.renderNow(index);
    }
    this.setData({ sending: false, canSend: !!this.data.inputValue.trim() });
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
