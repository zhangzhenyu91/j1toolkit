<template>
  <div class="cm-wrap">
    <!-- 左：会话列表 -->
    <div class="col-side card cm-side">
      <div class="toolbar">
        <h2 class="card-title" style="margin: 0">会话</h2>
        <div class="spacer"></div>
        <t-button size="small" theme="primary" :disabled="creating" @click="onNewSession">
          新建会话
        </t-button>
      </div>
      <t-loading :loading="sessionsLoading" size="small">
        <div
          v-for="s in sessions"
          :key="s.id"
          class="cm-item"
          :class="{ on: s.id === activeId }"
          @click="openSession(s.id)"
        >
          <div class="cm-item-main">
            <div class="tt">{{ s.title || '新对话' }}</div>
            <div class="tm num">{{ s.timeText }}</div>
          </div>
          <div class="cm-item-acts">
            <button class="ia" title="重命名" @click.stop="renameSession(s)">改名</button>
            <button class="ia danger" title="删除" @click.stop="onDeleteSession(s)">删除</button>
          </div>
        </div>
        <div v-if="!sessions.length && !sessionsLoading" class="muted">
          {{ sessionsErr || '暂无对话，点击上方「新建会话」开始' }}
        </div>
      </t-loading>
    </div>

    <!-- 右：对话区 -->
    <div class="col-main card cm-main">
      <!-- 头部：标题 + 能力控件 -->
      <div class="cm-head">
        <div class="cm-title-box">
          <b class="cm-title">{{ activeTitle || 'Call Me' }}</b>
          <span class="cm-sub">AI 知识库问答</span>
        </div>
        <div class="cm-caps">
          <t-select
            v-model="modelId"
            size="small"
            class="cap-sel"
            :placeholder="caps ? '默认模型' : '能力加载中'"
            clearable
            @change="saveCapsPrefs"
          >
            <t-option
              v-for="m in (caps && caps.models) || []"
              :key="m.id"
              :value="m.id"
              :label="m.name || m.id"
            />
          </t-select>
          <t-select
            v-model="reasoningEffort"
            size="small"
            class="cap-sel"
            placeholder="思考强度：默认"
            clearable
            @change="saveCapsPrefs"
          >
            <t-option
              v-for="lv in effortOptions"
              :key="lv"
              :value="lv"
              :label="'思考强度：' + effortLabel(lv)"
            />
          </t-select>
          <t-tooltip v-if="caps && !caps.web_search" content="联网搜索能力未启用（服务端探测）">
            <span class="ws-wrap disabled">
              <span class="ws-label">联网</span>
              <t-switch v-model="webSearch" size="small" disabled />
            </span>
          </t-tooltip>
          <span v-else class="ws-wrap">
            <span class="ws-label">联网</span>
            <t-switch v-model="webSearch" size="small" @change="saveCapsPrefs" />
          </span>
        </div>
      </div>

      <!-- 消息流 -->
      <div ref="msgListEl" class="cm-body">
        <div v-if="!activeId" class="cm-welcome">
          <p class="w-title">向 AI 知识库提问</p>
          <p class="w-sub">支持文字、图片与文档附件（pdf / docx / xlsx / txt 等，单件 ≤50MB）</p>
        </div>
        <template v-else>
          <div v-if="hasMore && messages.length" class="load-earlier" @click="loadEarlier">
            {{ loadingEarlier ? '加载中…' : '加载更早消息' }}
          </div>

          <div v-for="(msg, mi) in messages" :key="mi" class="msg-row" :class="msg.role">
            <!-- 用户气泡 -->
            <template v-if="msg.role === 'user'">
              <div class="bubble user">
                <img v-if="msg.image" :src="msg.image" class="msg-img" alt="用户图片" />
                <div class="md-body" v-html="mdHtml(msg.content)"></div>
              </div>
            </template>

            <!-- AI 气泡 -->
            <template v-else>
              <div class="ai-col">
                <!-- 步骤树 / 思考过程 -->
                <div v-if="msg.steps && msg.steps.length" class="think-box">
                  <div class="think-head" @click="msg.thinkingExpanded = !msg.thinkingExpanded">
                    <span class="think-dot" :class="{ on: msg.streaming }"></span>
                    <span class="think-label">{{
                      msg.thinkLabel || (msg.streaming ? '正在思考…' : '思考过程')
                    }}</span>
                    <span
                      class="think-chev"
                      :class="{ open: msg.streaming || msg.thinkingExpanded }"
                    >
                      ›
                    </span>
                  </div>
                  <div v-show="msg.streaming || msg.thinkingExpanded" class="think-body">
                    <div
                      v-for="(step, si) in msg.steps"
                      :key="si"
                      class="step"
                      :class="step.status || ''"
                    >
                      <div class="step-rail">
                        <div class="step-icon">{{ step.kind === 'thought' ? '思' : '工' }}</div>
                        <div v-if="si < msg.steps.length - 1" class="step-line"></div>
                      </div>
                      <div class="step-main">
                        <div v-if="step.kind === 'tool'" class="step-title">
                          {{ step.title || '工具调用' }}
                        </div>
                        <div v-if="step.summary" class="step-summary">{{ step.summary }}</div>
                        <div v-if="step.kind === 'thought'" class="step-text">{{ step.text }}</div>
                      </div>
                    </div>
                  </div>
                </div>

                <!-- 回答气泡 -->
                <div class="bubble ai" :class="{ err: msg.error }">
                  <div
                    v-if="msg.streaming && !msg.content && !(msg.steps && msg.steps.length)"
                    class="typing"
                  >
                    <i></i><i></i><i></i><span class="typing-txt">正在思考…</span>
                  </div>
                  <div v-else class="md-body" v-html="mdHtml(msg.content)"></div>
                  <span v-if="msg.streaming && msg.content" class="caret"></span>
                </div>

                <!-- 参考来源 -->
                <div v-if="msg.references && msg.references.length" class="ref-box">
                  <div class="ref-title">参考来源</div>
                  <div v-for="(r, ri) in msg.references" :key="ri" class="ref-item">
                    {{ r.title || '未命名资料' }}
                  </div>
                </div>

                <!-- 操作条 -->
                <div v-if="!msg.streaming && !msg.error && msg.content" class="act-bar">
                  <button class="act" @click="copyText(msg.content)">复制</button>
                  <button v-if="msg.msgId" class="act danger" @click="onDeleteMsg(msg)">
                    删除
                  </button>
                  <span class="act-time num">{{ msg.timeText }}</span>
                </div>
              </div>
            </template>
          </div>
        </template>
      </div>

      <!-- 输入区 -->
      <div class="cm-input">
        <!-- 芯片行：图片 / 附件 -->
        <div v-if="pendingImage || attachments.length" class="chip-row">
          <div v-if="pendingImage" class="chip img-chip">
            <img :src="pendingImage" alt="待发送图片" />
            <button class="chip-x" @click="clearImage">×</button>
          </div>
          <div
            v-for="att in attachments"
            :key="att.uid"
            class="chip att-chip"
            :class="{ failed: att.status === 'failed' }"
          >
            <div class="a-main">
              <div class="a-name">{{ att.name }}</div>
              <div class="a-sub num">{{ attSubText(att) }}</div>
            </div>
            <button class="chip-x" title="移除附件" @click="removeAttachment(att)">×</button>
          </div>
        </div>

        <div class="input-row">
          <button
            class="tool-btn"
            title="图片（≤8MB）"
            :disabled="sending"
            @click="imageInput && imageInput.click()"
          >
            图片
          </button>
          <button
            class="tool-btn"
            title="文档附件（≤50MB）"
            :disabled="sending || !activeId"
            @click="attInput && attInput.click()"
          >
            附件
          </button>
          <textarea
            ref="inputEl"
            v-model="draft"
            class="cm-text"
            rows="1"
            placeholder="输入问题，Enter 发送 / Shift+Enter 换行"
            @input="autoGrow"
            @keydown="onKeydown"
          ></textarea>
          <t-button
            size="small"
            theme="primary"
            :disabled="!canSend"
            :loading="sending"
            @click="onSend"
          >
            发送
          </t-button>
        </div>

        <input
          ref="imageInput"
          type="file"
          accept=".jpg,.jpeg,.png,.gif,.webp"
          hidden
          @change="onPickImage"
        />
        <input ref="attInput" type="file" hidden multiple @change="onPickFiles" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import {
  createSession as createSessionApi,
  deleteMessage,
  deleteSession,
  getCapabilities,
  getSession,
  listAttachments,
  listSessions,
  streamChat,
  updateSession,
  uploadAttachment,
} from '../features/callme/api.js';
import { renderMd } from '../features/callme/md.js';

// ---- 常量（与后端白名单一致）----
const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'webp'];
const ATT_EXTS = [
  'pdf',
  'docx',
  'doc',
  'pptx',
  'ppt',
  'xlsx',
  'xls',
  'epub',
  'mhtml',
  'csv',
  'md',
  'markdown',
  'txt',
  'json',
  'html',
  'mp3',
  'wav',
  'm4a',
  'flac',
  'ogg',
  'aac',
  'bmp',
  'tiff',
  'tif',
  'mp4',
];
const ATT_MAX = 5;
const ATT_SIZE = 50 * 1024 * 1024;
const IMG_SIZE = 8 * 1024 * 1024;

// ---- 会话状态 ----
const sessions = ref([]);
const sessionsLoading = ref(false);
const sessionsErr = ref('');
const activeId = ref('');
const activeTitle = ref('');
const creating = ref(false);
const entered = ref(false);
const messages = ref([]);
const hasMore = ref(false);
const loadingEarlier = ref(false);
const sending = ref(false);

// ---- 输入区状态 ----
const draft = ref('');
const inputEl = ref(null);
const imageInput = ref(null);
const attInput = ref(null);
const msgListEl = ref(null);
const pendingImage = ref('');
const attachments = reactive([]); // {uid, attId, sessionId, name, size, status, progress, error, pollTimer}

// ---- 能力控件 ----
const caps = ref(null); // {models:[{id,name,supports_vision,thinking_levels}], web_search}
const modelId = ref('');
const reasoningEffort = ref('');
const webSearch = ref(false);

// effortOptions 当前模型的思考强度档位（无模型时给通用档）。
const effortOptions = computed(() => {
  const models = (caps.value && caps.value.models) || [];
  const m = models.find((x) => x.id === modelId.value);
  const levels =
    m && Array.isArray(m.thinking_levels) && m.thinking_levels.length ? m.thinking_levels : [];
  return levels.length ? levels : ['off', 'low', 'medium', 'high'];
});

// effortLabel 思考强度文案。
function effortLabel(lv) {
  return (
    {
      off: '关闭',
      auto: '自动',
      minimal: '极简',
      low: '低',
      medium: '中',
      high: '高',
      xhigh: '极高',
      max: '最高',
    }[lv] || lv
  );
}

// canSend 发送条件：有内容、非发送中、会话就绪、无上传中附件。
const canSend = computed(() => {
  if (!draft.value.trim() || sending.value || !activeId.value) return false;
  return !attachments.some((a) => a.status === 'uploading');
});

/* ================= 时间 / 文本工具 ================= */

function pad(n) {
  return n < 10 ? '0' + n : '' + n;
}

// timeTextOf 消息时间：当天 HH:mm，跨天 MM-DD HH:mm。
function timeTextOf(input) {
  const d = input ? new Date(input) : new Date();
  if (Number.isNaN(d.getTime())) return '';
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  if (sameDay) return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// relTime 会话列表相对时间。
function relTime(input) {
  const d = input ? new Date(input) : new Date();
  if (Number.isNaN(d.getTime())) return '';
  const diff = Date.now() - d.getTime();
  if (diff < 60 * 1000) return '刚刚';
  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / 60000)} 分钟前`;
  if (diff < 24 * 60 * 60 * 1000) return `${Math.floor(diff / 3600000)} 小时前`;
  return timeTextOf(input);
}

// escHTML 全量转义（markdown 渲染失败回退纯文本用）。
function escHTML(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// mdHtml 消息内容渲染（markdown，失败回退纯文本 + 换行）。
function mdHtml(text) {
  if (!text) return '';
  const html = renderMd(text);
  if (html) return html;
  return escHTML(text).replace(/\n/g, '<br>');
}

// fmtSize 文件大小文案。
function fmtSize(n) {
  if (!n && n !== 0) return '';
  if (n < 1024) return n + 'B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1).replace(/\.0$/, '') + 'KB';
  return (n / 1024 / 1024).toFixed(1).replace(/\.0$/, '') + 'MB';
}

// extOf 扩展名（小写、不含点）。
function extOf(name) {
  const m = /\.([a-z0-9]+)$/i.exec(name || '');
  return m ? m[1].toLowerCase() : '';
}

// copyText 复制文本。
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text || '');
    MessagePlugin.success('已复制');
  } catch (e) {
    MessagePlugin.error('复制失败，请手动选择文本复制');
  }
}

/* ================= 能力清单 ================= */

async function loadCaps() {
  try {
    caps.value = await getCapabilities();
    // 所选模型已下架时回落默认
    const models = (caps.value && caps.value.models) || [];
    if (modelId.value && !models.some((m) => m.id === modelId.value)) modelId.value = '';
    if (!caps.value.web_search) webSearch.value = false;
  } catch (err) {
    caps.value = null;
  }
}

function saveCapsPrefs() {
  try {
    localStorage.setItem('callme_model_id', modelId.value || '');
    localStorage.setItem('callme_reasoning_effort', reasoningEffort.value || '');
    localStorage.setItem('callme_web_search', webSearch.value ? '1' : '0');
  } catch (e) {
    // localStorage 不可用时忽略
  }
}

/* ================= 会话 ================= */

function findSession(id) {
  return sessions.value.find((s) => s.id === id) || null;
}

// loadSessions 会话列表（兼容数组 / {list} / {sessions}，最近更新在前）。
async function loadSessions(autoEnter) {
  sessionsLoading.value = true;
  sessionsErr.value = '';
  try {
    const data = await listSessions();
    const raw = Array.isArray(data) ? data : (data && (data.list || data.sessions)) || [];
    sessions.value = raw
      .map((item) => ({
        id: item.id,
        title: item.title,
        ts: new Date(item.updated_at || item.created_at || 0).getTime() || 0,
        timeText: relTime(item.updated_at || item.created_at),
      }))
      .sort((a, b) => b.ts - a.ts);
    if (autoEnter && !entered.value) {
      entered.value = true;
      if (sessions.value.length) openSession(sessions.value[0].id);
      else createSession('');
    }
  } catch (err) {
    sessionsErr.value = err.message || '会话列表加载失败';
    MessagePlugin.error(sessionsErr.value);
  } finally {
    sessionsLoading.value = false;
  }
}

// openSession 进入会话：加载历史消息。
function openSession(id) {
  if (sending.value) {
    MessagePlugin.warning('回答生成中，请稍候');
    return;
  }
  clearAttachments(); // 附件上传绑定会话 id，切换会话即清理待发送芯片
  const s = findSession(id);
  activeId.value = id;
  activeTitle.value = (s && s.title) || '新对话';
  messages.value = [];
  hasMore.value = false;
  loadHistory();
}

async function createSession(title) {
  if (creating.value) return;
  creating.value = true;
  try {
    const data = await createSessionApi(title);
    const id = data.id || (data.data && data.data.id);
    if (!id) {
      MessagePlugin.error('创建失败：未返回会话 ID');
      return;
    }
    sessions.value.unshift({
      id,
      title: title || '新对话',
      ts: Date.now(),
      timeText: '刚刚',
    });
    openSession(id);
  } catch (err) {
    MessagePlugin.error(err.message || '新建会话失败');
  } finally {
    creating.value = false;
  }
}

function onNewSession() {
  const dlg = DialogPlugin.confirm({
    header: '新建会话',
    body: '会话标题留空时首轮提问后自动命名。',
    confirmBtn: { content: '创建', theme: 'primary' },
    cancelBtn: { content: '取消' },
    onConfirm: () => {
      dlg.destroy();
      createSession('');
    },
    onCancel: () => dlg.destroy(),
    onClose: () => dlg.destroy(),
  });
}

function renameSession(s) {
  const dlg = DialogPlugin.confirm({
    header: '重命名会话',
    body: `将「${s.title || '新对话'}」改名为：`,
    confirmBtn: { content: '保存', theme: 'primary' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      dlg.destroy();
      // 简化交互：改名走浏览器 prompt（TDesign 无内联输入弹层，避免额外表单态）
      const title = (window.prompt('会话名称', s.title || '') || '').trim();
      if (!title) return;
      try {
        await updateSession(s.id, title);
        s.title = title;
        if (s.id === activeId.value) activeTitle.value = title;
        MessagePlugin.success('已保存');
      } catch (err) {
        MessagePlugin.error(err.message || '重命名失败');
      }
    },
    onCancel: () => dlg.destroy(),
    onClose: () => dlg.destroy(),
  });
}

async function onDeleteSession(s) {
  const dlg = DialogPlugin.confirm({
    header: '删除会话？',
    body: `将删除「${s.title || '新对话'}」及其全部消息，不可恢复。`,
    confirmBtn: { content: '删除', theme: 'danger' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      dlg.destroy();
      try {
        await deleteSession(s.id);
        sessions.value = sessions.value.filter((x) => x.id !== s.id);
        if (s.id === activeId.value) {
          activeId.value = '';
          activeTitle.value = '';
          messages.value = [];
        }
        MessagePlugin.success('已删除');
      } catch (err) {
        MessagePlugin.error(err.message || '删除失败');
      }
    },
    onCancel: () => dlg.destroy(),
    onClose: () => dlg.destroy(),
  });
}

/* ================= 历史消息 ================= */

// normalizeSteps Go 步骤树（type=thinking/tool/text）→ 展示节点（kind=thought/tool）。
function normalizeSteps(steps) {
  return (Array.isArray(steps) ? steps : []).map((s) => {
    const t = s.type || s.kind || '';
    if (t === 'tool' || t === 'tool_call') {
      return {
        kind: 'tool',
        title: s.title || '',
        summary: s.content || s.summary || '',
        status: s.success === false ? 'error' : 'done',
        name: s.name || '',
      };
    }
    return { kind: 'thought', text: s.content || s.text || '' };
  });
}

// summaryText 完结后的思考摘要：思考 N 轮 · 调用 N 次工具 · 耗时 X。
function fmtDuration(ms) {
  if (!ms || ms <= 0) return '';
  if (ms < 1000) return Math.round(ms) + 'ms';
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + 's';
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

function summaryText(steps, meta, durationMs) {
  const tools =
    meta && meta.toolCalls != null ? meta.toolCalls : steps.filter((s) => s.kind === 'tool').length;
  const rounds = steps.filter((s) => s.kind === 'thought').length;
  const parts = [];
  if (rounds) parts.push(`思考 ${rounds} 轮`);
  if (tools) parts.push(`调用 ${tools} 次工具`);
  const dur = fmtDuration(durationMs);
  if (dur) parts.push(`耗时 ${dur}`);
  return parts.join(' · ') || '思考过程';
}

// buildHistoryMsg 服务端历史消息 → 展示消息。
function buildHistoryMsg(m) {
  const role =
    m.role === 'user' ? 'user' : m.role === 'assistant' || m.role === 'ai' ? 'assistant' : null;
  const content = m.content || m.message || m.text || '';
  if (!role || !content) return null;
  const references = [];
  if (role === 'assistant' && Array.isArray(m.knowledge_references)) {
    const seen = {};
    m.knowledge_references.forEach((r) => {
      const rid = r.knowledge_id || r.id;
      if (!rid || seen[rid]) return;
      seen[rid] = true;
      references.push({
        id: rid,
        title: r.knowledge_title || r.knowledge_filename || '未命名资料',
      });
    });
  }
  const steps = role === 'assistant' ? normalizeSteps(m.steps) : [];
  const stepsMeta = m.steps_meta || null;
  return {
    msgId: m.id,
    role,
    content,
    steps,
    stepsMeta,
    thinkLabel: steps.length
      ? summaryText(steps, stepsMeta, (stepsMeta && stepsMeta.durationMs) || 0)
      : '',
    thinkingExpanded: false,
    references,
    streaming: false,
    error: false,
    timeText: timeTextOf(m.created_at),
    createdAt: m.created_at || '',
    image: '',
  };
}

async function loadHistory() {
  try {
    const data = await getSession(activeId.value, { limit: 50 });
    const raw = (data && (data.messages || data.message_list || data.history)) || [];
    messages.value = [];
    raw.forEach((m) => {
      const msg = buildHistoryMsg(m);
      if (msg) messages.value.push(msg);
    });
    hasMore.value = !!(data && data.has_more);
    if (!messages.value.length) {
      // 空会话提示由模板欢迎态兜底
    }
    scrollBottom(false);
  } catch (err) {
    MessagePlugin.error(err.message || '历史消息加载失败，可直接继续提问');
  }
}

async function loadEarlier() {
  if (loadingEarlier.value || !hasMore.value) return;
  const first = messages.value[0];
  if (!first || !first.createdAt) return;
  loadingEarlier.value = true;
  try {
    const data = await getSession(activeId.value, { limit: 50, beforeTime: first.createdAt });
    const raw = (data && data.messages) || [];
    const older = [];
    raw.forEach((m) => {
      const msg = buildHistoryMsg(m);
      if (msg) older.push(msg);
    });
    const prevHeight = msgListEl.value ? msgListEl.value.scrollHeight : 0;
    const prevTop = msgListEl.value ? msgListEl.value.scrollTop : 0;
    messages.value = older.concat(messages.value);
    hasMore.value = !!(data && data.has_more);
    await nextTick();
    if (msgListEl.value)
      msgListEl.value.scrollTop = prevTop + (msgListEl.value.scrollHeight - prevHeight);
  } catch (err) {
    MessagePlugin.error('更早消息加载失败，请稍后重试');
  } finally {
    loadingEarlier.value = false;
  }
}

// syncRecentIds 静默同步本轮新消息的服务端 msgId（仅尾部一两条）。
async function syncRecentIds() {
  try {
    const data = await getSession(activeId.value, {});
    const raw = (data && data.messages) || [];
    let assistant = null;
    let user = null;
    for (let i = raw.length - 1; i >= 0; i--) {
      const role = raw[i].role;
      if (!assistant && (role === 'assistant' || role === 'ai')) assistant = raw[i];
      else if (!user && role === 'user') {
        user = raw[i];
        break;
      }
    }
    const msgs = messages.value;
    for (let k = msgs.length - 1; k >= 0 && k >= msgs.length - 2; k--) {
      const m = msgs[k];
      if (m.role === 'assistant' && assistant) {
        if (!m.msgId) m.msgId = assistant.id;
        if (Array.isArray(assistant.steps) && assistant.steps.length) {
          m.steps = normalizeSteps(assistant.steps);
          m.stepsMeta = assistant.steps_meta || null;
          m.thinkLabel = summaryText(
            m.steps,
            m.stepsMeta,
            (m.stepsMeta && m.stepsMeta.durationMs) || 0
          );
        }
      }
      if (m.role === 'user' && user && !m.msgId) m.msgId = user.id;
    }
  } catch (e) {
    // 同步失败不影响使用（下次进入会话时历史消息自带 id）
  }
}

function scrollBottom(smooth) {
  nextTick(() => {
    const el = msgListEl.value;
    if (!el) return;
    if (smooth) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
    else el.scrollTop = el.scrollHeight;
  });
}

/* ================= 发送与 SSE 流式 ================= */

function onKeydown(e) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    onSend();
  }
}

function autoGrow() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = 'auto';
  el.style.height = Math.min(el.scrollHeight, 150) + 'px';
}

function onSend() {
  const query = draft.value.trim();
  if (!query || sending.value) return;
  if (!activeId.value) {
    MessagePlugin.warning('会话尚未就绪，请稍候');
    return;
  }
  if (attachments.some((a) => a.status === 'uploading')) {
    MessagePlugin.warning('附件上传中，请稍候');
    return;
  }

  const image = pendingImage.value;
  // 随本轮消息上送的附件：取已拿到服务端 id 且未失败的芯片
  const attIds = attachments.filter((a) => a.attId && a.status !== 'failed').map((a) => a.attId);

  messages.value.push({
    role: 'user',
    content: query,
    image: image || '',
    timeText: timeTextOf(),
    steps: [],
    references: [],
    streaming: false,
    error: false,
    msgId: '',
    createdAt: '',
    thinkLabel: '',
    thinkingExpanded: false,
    stepsMeta: null,
  });
  const aiMsg = reactive({
    role: 'assistant',
    content: '',
    steps: [],
    stepsMeta: null,
    thinkLabel: '',
    thinkingExpanded: false,
    streaming: true,
    error: false,
    timeText: '',
    references: [],
    msgId: '',
    createdAt: '',
    image: '',
  });
  messages.value.push(aiMsg);

  draft.value = '';
  autoGrow();
  clearImage();
  clearAttachments();
  sending.value = true;
  scrollBottom(true);

  const payload = { sessionId: activeId.value, query };
  if (image) payload.images = [{ data: image }];
  if (attIds.length) payload.attachmentIds = attIds;
  if (caps.value) payload.webSearch = !!webSearch.value; // 能力未知时不带该字段
  if (modelId.value) payload.modelId = modelId.value;
  if (reasoningEffort.value) payload.reasoningEffort = reasoningEffort.value;

  streamChat(
    payload,
    (evt) => onSseEvent(evt, aiMsg),
    (errorMessage) => finalizeAnswer(aiMsg, errorMessage)
  );
}

// onSseEvent 处理服务端归一化后的 SSE 事件。
function onSseEvent(evt, msg) {
  if (evt.type === 'answer' && evt.content) {
    msg.content += evt.content;
    scrollBottom(false);
  } else if (evt.type === 'thinking' && evt.content) {
    // 追加到未封闭的思考节点；否则开新节点（新一轮 ReAct 的思考）
    const last = msg.steps[msg.steps.length - 1];
    if (last && last.kind === 'thought' && !last._closed) {
      last.text += evt.content;
    } else {
      msg.steps.push({ kind: 'thought', text: evt.content, _closed: false });
    }
    msg.thinkLabel = '正在思考…';
  } else if (evt.type === 'preamble') {
    // 工具调用前的过渡语：从回答区挪入步骤树，作为完整的思考节点
    if (msg.content) {
      msg.steps.push({ kind: 'thought', text: msg.content, _closed: true });
      msg.content = '';
    }
  } else if (evt.type === 'tool_start') {
    // 同一次调用两帧（pending + hint）：重复帧更新既有节点
    let dup = -1;
    if (evt.id) {
      for (let di = msg.steps.length - 1; di >= 0; di--) {
        const st = msg.steps[di];
        if (st.kind === 'tool' && st.id === evt.id) {
          dup = di;
          break;
        }
      }
    }
    if (dup >= 0) {
      const st = msg.steps[dup];
      if (st.status === 'running') {
        st.title = evt.title || st.title;
        st.name = evt.name || st.name;
      }
      msg.thinkLabel = st.title;
    } else {
      msg.steps.forEach((s) => {
        if (s.kind === 'thought') s._closed = true;
      });
      msg.steps.push({
        kind: 'tool',
        id: evt.id || '',
        name: evt.name || '',
        title: evt.title || '正在调用工具…',
        summary: '',
        status: 'running',
        durationMs: 0,
      });
      msg.thinkLabel = evt.title || '正在调用工具…';
    }
  } else if (evt.type === 'tool_end') {
    // 按 tool_call_id 精确匹配；无匹配兜底最后一个 running 节点
    let ti = -1;
    if (evt.id) {
      for (let si = msg.steps.length - 1; si >= 0; si--) {
        const st = msg.steps[si];
        if (st.kind === 'tool' && st.id === evt.id) {
          ti = si;
          break;
        }
      }
    }
    if (ti < 0) {
      for (let sj = msg.steps.length - 1; sj >= 0; sj--) {
        const sf = msg.steps[sj];
        if (sf.kind === 'tool' && sf.status === 'running') {
          ti = sj;
          break;
        }
      }
    }
    if (ti >= 0) {
      const st = msg.steps[ti];
      st.title = evt.title || st.title;
      st.summary = evt.summary || '';
      st.status = evt.success === false ? 'error' : 'done';
      st.durationMs = evt.durationMs || 0;
      msg.thinkLabel = st.title;
    }
  } else if (evt.type === 'title' && evt.content) {
    // WeKnora 自动生成的会话标题：实时更新标题与列表
    activeTitle.value = evt.content;
    const titled = findSession(activeId.value);
    if (titled) titled.title = evt.content;
  } else if (evt.type === 'references' && evt.list && evt.list.length) {
    msg.references = evt.list;
  } else if (evt.type === 'error') {
    msg.content = evt.content || 'AI 服务返回错误';
    msg.error = true;
    msg.streaming = false;
    msg.thinkingExpanded = false;
    finishSteps(msg, true);
    msg.timeText = timeTextOf();
    sending.value = false;
    scrollBottom(false);
  } else if (evt.type === 'done') {
    msg.streaming = false;
    msg.thinkingExpanded = false;
    finishSteps(msg, false);
    msg.timeText = timeTextOf();
    sending.value = false;
    scrollBottom(false);
  }
}

// finishSteps 流收尾：残留 running 节点落终态 + 生成完结摘要。
function finishSteps(msg, isError) {
  msg.steps.forEach((s) => {
    if (s.kind === 'tool' && s.status === 'running') s.status = isError ? 'error' : 'done';
    if (s.kind === 'thought') s._closed = true;
  });
  if (msg.steps.length) {
    msg.thinkLabel = summaryText(msg.steps, msg.stepsMeta, 0);
  }
}

// finalizeAnswer 流结束收尾：无内容兜底提示；仅成功轮次同步 msgId。
function finalizeAnswer(msg, errorMessage) {
  if (msg._fin) {
    sending.value = false;
    return;
  }
  msg._fin = true;
  if (errorMessage && !msg.content) {
    msg.content = errorMessage;
    msg.error = true;
  } else if (!msg.content && !msg.error) {
    msg.content = '（未收到有效回答，请重试）';
  }
  const wasStreaming = msg.streaming;
  msg.streaming = false;
  msg.thinkingExpanded = false;
  if (wasStreaming) finishSteps(msg, !!errorMessage || msg.error);
  msg.timeText = msg.timeText || timeTextOf();
  sending.value = false;
  // 仅成功轮次同步 msgId：失败轮次消息可能未落库
  if (!errorMessage && !msg.error) syncRecentIds();
  scrollBottom(false);
}

// onDeleteMsg 问答成对删除（同 request_id）。
async function onDeleteMsg(msg) {
  if (!msg.msgId) return;
  const dlg = DialogPlugin.confirm({
    header: '删除该问答？',
    body: '将成对删除本轮提问与回答，不可恢复。',
    confirmBtn: { content: '删除', theme: 'danger' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      dlg.destroy();
      try {
        await deleteMessage(activeId.value, msg.msgId);
        // 本地移除本轮问答（同 request 的相邻 user/assistant）
        const idx = messages.value.indexOf(msg);
        const removeSet = new Set([idx]);
        if (idx > 0 && messages.value[idx - 1].role === 'user') removeSet.add(idx - 1);
        if (idx + 1 < messages.value.length && messages.value[idx + 1].role === 'assistant') {
          removeSet.add(idx + 1);
        }
        messages.value = messages.value.filter((_, i) => !removeSet.has(i));
        MessagePlugin.success('已删除');
      } catch (err) {
        MessagePlugin.error(err.message || '删除失败');
      }
    },
    onCancel: () => dlg.destroy(),
    onClose: () => dlg.destroy(),
  });
}

/* ================= 图片 / 附件 ================= */

function onPickImage(e) {
  const file = e.target.files && e.target.files[0];
  if (imageInput.value) imageInput.value.value = '';
  if (!file) return;
  if (IMG_EXTS.indexOf(extOf(file.name)) < 0) {
    MessagePlugin.error('仅支持 jpg / jpeg / png / gif / webp 图片');
    return;
  }
  if (file.size > IMG_SIZE) {
    MessagePlugin.error('图片不能超过 8MB，请压缩后再试');
    return;
  }
  const reader = new FileReader();
  reader.onload = () => {
    pendingImage.value = String(reader.result || '');
  };
  reader.onerror = () => MessagePlugin.error('图片读取失败');
  reader.readAsDataURL(file);
}

function clearImage() {
  pendingImage.value = '';
}

function onPickFiles(e) {
  const files = Array.prototype.slice.call(e.target.files || []);
  if (attInput.value) attInput.value.value = '';
  addAttachmentFiles(files);
}

// addAttachmentFiles 入队校验：超量 / 非白名单 / 超 50MB 均拦截。
function addAttachmentFiles(files) {
  if (!files.length) return;
  if (!activeId.value) {
    MessagePlugin.warning('会话尚未就绪，请稍候');
    return;
  }
  files.forEach((file) => {
    if (attachments.length >= ATT_MAX) {
      MessagePlugin.error(`最多添加 ${ATT_MAX} 个附件`);
      return;
    }
    if (ATT_EXTS.indexOf(extOf(file.name)) < 0) {
      MessagePlugin.error(`不支持的文件格式：${file.name}`);
      return;
    }
    if (file.size > ATT_SIZE) {
      MessagePlugin.error(`附件不能超过 50MB：${file.name}`);
      return;
    }
    const att = reactive({
      uid: Date.now() + Math.random(),
      attId: '',
      sessionId: activeId.value,
      name: file.name,
      size: file.size,
      status: 'uploading', // uploading / uploaded / processing / ready / failed
      progress: 0,
      error: '',
      pollTimer: 0,
    });
    attachments.push(att);
    doUpload(att, file);
  });
}

// doUpload XHR 上传（带进度），成功后进解析状态轮询。
async function doUpload(att, file) {
  try {
    const data = await uploadAttachment(att.sessionId, file, (p) => {
      if (att.status === 'uploading') att.progress = p;
    });
    if (!attachments.includes(att)) return; // 上传期间芯片已被移除
    att.attId = data.id;
    att.status = data.status || 'uploaded';
    att.progress = 100;
    // 落定态无需轮询，其余进 1s 轮询
    if (att.status !== 'ready' && att.status !== 'failed') startAttPoll(att.sessionId);
  } catch (err) {
    if (!attachments.includes(att)) return;
    attFail(att, err.message || '上传失败');
  }
}

function attFail(att, msg) {
  att.status = 'failed';
  att.error = msg;
  stopAttPollIfIdle(att.sessionId);
}

// attSubText 芯片状态行。
function attSubText(att) {
  if (att.status === 'uploading') return `${fmtSize(att.size)} · 上传 ${att.progress}%`;
  if (att.status === 'ready') return `${fmtSize(att.size)} · 已就绪`;
  if (att.status === 'failed') return att.error || '解析失败';
  return `${fmtSize(att.size)} · 解析中…`;
}

// startAttPoll 解析状态轮询：每会话单轮询器，tick 拉列表按 id 落位状态。
const attPolls = {};
function pendingAtts(sessionId) {
  return attachments.filter(
    (a) => a.sessionId === sessionId && a.attId && a.status !== 'ready' && a.status !== 'failed'
  );
}

function startAttPoll(sessionId) {
  if (attPolls[sessionId]) return;
  attPolls[sessionId] = setInterval(async () => {
    const pend = pendingAtts(sessionId);
    if (!pend.length) {
      stopAttPoll(sessionId);
      return;
    }
    try {
      const list = await listAttachments(sessionId);
      (list || []).forEach((doc) => {
        const att = attachments.find((a) => a.attId === doc.id);
        if (!att) return;
        att.status = doc.status || att.status;
        if (att.status === 'failed') att.error = doc.error_message || '解析失败';
      });
    } catch (e) {
      // 单次失败不中断轮询
    }
    if (!pendingAtts(sessionId).length) stopAttPoll(sessionId);
  }, 1000);
}

function stopAttPoll(sessionId) {
  if (attPolls[sessionId]) {
    clearInterval(attPolls[sessionId]);
    delete attPolls[sessionId];
  }
}

function stopAttPollIfIdle(sessionId) {
  if (!pendingAtts(sessionId).length) stopAttPoll(sessionId);
}

// removeAttachment 移除芯片：中止上传 / 该会话无待解析件即停轮询。
function removeAttachment(att) {
  const i = attachments.indexOf(att);
  if (i >= 0) attachments.splice(i, 1);
  stopAttPollIfIdle(att.sessionId);
}

function clearAttachments() {
  attachments.slice().forEach(removeAttachment);
}

/* ================= 生命周期 ================= */

onMounted(() => {
  try {
    modelId.value = localStorage.getItem('callme_model_id') || '';
    reasoningEffort.value = localStorage.getItem('callme_reasoning_effort') || '';
    webSearch.value = localStorage.getItem('callme_web_search') === '1';
  } catch (e) {
    // localStorage 不可用时用默认值
  }
  loadCaps();
  loadSessions(true);
});

onBeforeUnmount(() => {
  Object.keys(attPolls).forEach(stopAttPoll);
  clearAttachments();
});
</script>

<style scoped>
/* 双栏布局 */
.cm-wrap {
  display: grid;
  grid-template-columns: 280px 1fr;
  gap: 14px;
  height: calc(100vh - 150px);
  min-height: 480px;
}
.cm-side {
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.cm-main {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  padding: 0;
}

/* 会话列表 */
.cm-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 10px;
  border-radius: 8px;
  cursor: pointer;
  border: 1px solid transparent;
}
.cm-item:hover {
  background: var(--tk-fill);
}
.cm-item.on {
  background: var(--tk-blue-soft);
  border-color: var(--tk-brand);
}
.cm-item-main {
  flex: 1;
  min-width: 0;
}
.cm-item .tt {
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cm-item .tm {
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-ph);
}
.cm-item-acts {
  display: none;
  gap: 2px;
}
.cm-item:hover .cm-item-acts {
  display: inline-flex;
}
.ia {
  appearance: none;
  border: none;
  background: none;
  font-size: 11px;
  color: var(--tk-sub);
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 4px;
}
.ia:hover {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.ia.danger:hover {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}

/* 头部 */
.cm-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--tk-line);
  flex-wrap: wrap;
}
.cm-title-box {
  min-width: 0;
}
.cm-title {
  font-size: 15px;
}
.cm-sub {
  margin-left: 8px;
  font-size: 12px;
  color: var(--tk-ph);
}
.cm-caps {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.cap-sel {
  width: 150px;
}
.ws-wrap {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.ws-wrap.disabled {
  opacity: 0.6;
}
.ws-label {
  font-size: 12px;
  color: var(--tk-sub);
}

/* 消息流 */
.cm-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}
.cm-welcome {
  text-align: center;
  padding: 60px 20px;
}
.cm-welcome .w-title {
  font-size: 17px;
  font-weight: 600;
}
.cm-welcome .w-sub {
  margin-top: 8px;
  font-size: 13px;
  color: var(--tk-ph);
}
.load-earlier {
  text-align: center;
  margin-bottom: 14px;
  font-size: 12px;
  color: var(--tk-brand);
  cursor: pointer;
}
.msg-row {
  display: flex;
  margin-bottom: 16px;
}
.msg-row.user {
  justify-content: flex-end;
}
.msg-row.assistant {
  justify-content: flex-start;
}
.bubble {
  max-width: 78%;
  border-radius: 12px;
  padding: 10px 14px;
  font-size: 14px;
  line-height: 1.7;
}
.bubble.user {
  background: var(--tk-brand);
  color: #fff;
}
.bubble.user .md-body {
  color: #fff;
}
.bubble.ai {
  background: var(--tk-fill);
  border: 1px solid var(--tk-line);
}
.bubble.ai.err {
  background: var(--tk-red-soft);
  border-color: var(--tk-red);
}
.msg-img {
  max-width: 220px;
  max-height: 220px;
  border-radius: 8px;
  display: block;
  margin-bottom: 6px;
}
.ai-col {
  max-width: 78%;
  min-width: 0;
}

/* markdown 内容 */
.md-body :deep(p) {
  margin: 0 0 8px;
}
.md-body :deep(p:last-child) {
  margin-bottom: 0;
}
.md-body :deep(pre) {
  background: #1d2129;
  color: #f5f7fa;
  padding: 10px 12px;
  border-radius: 8px;
  overflow-x: auto;
  font-size: 12px;
}
.md-body :deep(code) {
  font-family: Consolas, Menlo, monospace;
  font-size: 12px;
}
.md-body :deep(table) {
  border-collapse: collapse;
  font-size: 12px;
}
.md-body :deep(th),
.md-body :deep(td) {
  border: 1px solid var(--tk-line);
  padding: 4px 8px;
}
.md-body :deep(blockquote) {
  margin: 6px 0;
  padding: 4px 10px;
  border-left: 3px solid var(--tk-dash);
  color: var(--tk-sub);
}
.md-body :deep(img) {
  max-width: 100%;
  border-radius: 8px;
}

/* 思考过程 */
.think-box {
  margin-bottom: 8px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
}
.think-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  cursor: pointer;
  font-size: 12px;
  color: var(--tk-sub);
}
.think-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--tk-dash);
}
.think-dot.on {
  background: var(--tk-brand);
}
.think-label {
  flex: 1;
}
.think-chev {
  transition: transform 0.2s var(--tk-ease);
}
.think-chev.open {
  transform: rotate(90deg);
}
.think-body {
  max-height: 260px;
  overflow-y: auto;
  padding: 4px 12px 10px;
  border-top: 1px solid var(--tk-line);
}
.step {
  display: flex;
  gap: 10px;
}
.step-rail {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.step-icon {
  width: 22px;
  height: 22px;
  border: 1px solid var(--tk-line);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  color: var(--tk-sub);
  flex-shrink: 0;
}
.step-line {
  width: 1px;
  flex: 1;
  background: var(--tk-line);
  min-height: 8px;
}
.step-main {
  padding-bottom: 10px;
  min-width: 0;
}
.step-title {
  font-size: 12px;
  font-weight: 600;
}
.step-summary {
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-ph);
}
.step-text {
  margin-top: 2px;
  font-size: 12px;
  color: var(--tk-sub);
  white-space: pre-wrap;
}
.step.error .step-icon {
  border-color: var(--tk-red);
  color: var(--tk-red);
}
.step.running .step-icon {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}

/* 打字光标 / 呼吸点 */
.typing {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.typing i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-dash);
  animation: cm-blink 1.2s infinite;
}
.typing i:nth-child(2) {
  animation-delay: 0.2s;
}
.typing i:nth-child(3) {
  animation-delay: 0.4s;
}
.typing-txt {
  font-size: 12px;
  color: var(--tk-ph);
}
@keyframes cm-blink {
  0%,
  80%,
  100% {
    opacity: 0.3;
  }
  40% {
    opacity: 1;
  }
}
.caret {
  display: inline-block;
  width: 2px;
  height: 14px;
  background: var(--tk-brand);
  margin-left: 2px;
  vertical-align: -2px;
  animation: cm-blink 1s infinite;
}

/* 参考来源 */
.ref-box {
  margin-top: 8px;
  border-left: 3px solid var(--tk-blue-soft);
  padding: 8px 12px;
  background: var(--tk-fill);
}
.ref-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
  margin-bottom: 4px;
}
.ref-item {
  font-size: 12px;
  color: var(--tk-sub);
  line-height: 1.8;
}

/* 操作条 */
.act-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
}
.act {
  appearance: none;
  border: none;
  background: none;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
  padding: 0;
}
.act:hover {
  color: var(--tk-brand);
}
.act.danger:hover {
  color: var(--tk-red);
}
.act-time {
  margin-left: auto;
  font-size: 11px;
  color: var(--tk-ph);
}

/* 输入区 */
.cm-input {
  border-top: 1px solid var(--tk-line);
  padding: 10px 12px;
}
.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}
.chip {
  display: flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 6px 8px;
  background: var(--tk-fill);
}
.chip.img-chip img {
  width: 48px;
  height: 48px;
  object-fit: cover;
  border-radius: 4px;
}
.chip-x {
  appearance: none;
  border: none;
  background: none;
  font-size: 14px;
  color: var(--tk-ph);
  cursor: pointer;
  line-height: 1;
}
.chip-x:hover {
  color: var(--tk-red);
}
.att-chip .a-main {
  min-width: 0;
}
.att-chip .a-name {
  font-size: 12px;
  font-weight: 600;
  max-width: 180px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.att-chip .a-sub {
  font-size: 11px;
  color: var(--tk-ph);
  margin-top: 2px;
}
.att-chip.failed {
  border-color: var(--tk-red);
  background: var(--tk-red-soft);
}
.att-chip.failed .a-sub {
  color: var(--tk-red);
}
.input-row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
.tool-btn {
  appearance: none;
  border: 1px solid var(--tk-line);
  background: #fff;
  border-radius: 8px;
  padding: 7px 12px;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
  white-space: nowrap;
}
.tool-btn:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.tool-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.cm-text {
  flex: 1;
  resize: none;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 9px 12px;
  font-size: 14px;
  font-family: inherit;
  line-height: 1.5;
  outline: none;
  min-height: 38px;
  max-height: 150px;
}
.cm-text:focus {
  border-color: var(--tk-brand);
}

@media (max-width: 960px) {
  .cm-wrap {
    grid-template-columns: 1fr;
    height: auto;
  }
  .cm-side {
    max-height: 220px;
  }
  .cm-main {
    min-height: 60vh;
  }
}
</style>
