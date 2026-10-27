<template>
  <div class="cm-root" :class="{ 'side-open': sideOpen }">
    <div class="cm">
      <!-- 右侧：会话列表 -->
      <aside class="cm-side">
        <div class="cm-side-head">
          <div class="cm-logo" v-html="icon('robot', 20)"></div>
          <div>
            <b>Call Me</b>
            <small>AI 知识库问答</small>
          </div>
        </div>
        <button class="cm-new" @click="onNewSession">
          <span v-html="icon('add', 15)"></span>
          <span>新建会话</span>
        </button>
        <div class="cm-list">
          <div class="cm-lab">最近会话</div>
          <template v-if="sessionsLoading && !sessions.length">
            <div class="sk-item">
              <span class="skeleton s-ic"></span>
              <div class="s-tx">
                <div class="skeleton s-l1"></div>
                <div class="skeleton s-l2"></div>
              </div>
            </div>
            <div class="sk-item">
              <span class="skeleton s-ic"></span>
              <div class="s-tx">
                <div class="skeleton s-l1"></div>
                <div class="skeleton s-l2"></div>
              </div>
            </div>
            <div class="sk-item">
              <span class="skeleton s-ic"></span>
              <div class="s-tx">
                <div class="skeleton s-l1"></div>
                <div class="skeleton s-l2"></div>
              </div>
            </div>
          </template>
          <div v-else-if="!sessions.length" class="list-err">
            <span v-html="icon('chat', 22)"></span>
            <p>{{ sessionsErr || '暂无对话，点击上方「新建会话」开始' }}</p>
          </div>
          <template v-else>
            <div
              v-for="s in sessions"
              :key="s.id"
              class="cm-item"
              :class="{ on: s.id === activeId }"
              @click="
                sideOpen = false;
                openSession(s.id);
              "
            >
              <span v-html="icon('chat', 16)"></span>
              <div class="cm-item-main">
                <div class="tt">{{ s.title || '新对话' }}</div>
                <div class="tm">{{ s.timeText }}</div>
              </div>
              <div class="cm-item-acts">
                <span class="ia" title="重命名" @click.stop="renameSession(s)">
                  <span v-html="icon('edit', 12)"></span>
                </span>
                <span class="ia danger" title="删除" @click.stop="onDeleteSession(s)">
                  <span v-html="icon('delete', 12)"></span>
                </span>
              </div>
            </div>
          </template>
        </div>
        <div class="cm-side-foot">
          <span class="mini-avatar">{{ userAvatarChar }}</span>
          <div>
            <div class="nm">{{ userName }}</div>
            <div class="rl">{{ userRoleText }}</div>
          </div>
        </div>
      </aside>
      <div class="cm-mask" @click="sideOpen = false"></div>

      <!-- 左侧：对话流 -->
      <main class="cm-main">
        <div class="cm-head">
          <button class="cm-toggle" title="会话列表" @click="sideOpen = true">
            <span v-html="icon('chat', 16)"></span>
          </button>
          <span class="cm-h-title">{{ activeTitle || 'Call Me' }}</span>
          <span class="src">
            <span class="pulse-dot"></span>
            <span>WeKnora 知识库</span>
          </span>
        </div>

        <div ref="msgListEl" class="cm-body">
          <div v-if="!activeId" class="cm-welcome">
            <div class="w-ic" v-html="icon('robot', 36)"></div>
            <b>Call Me · AI 知识库问答</b>
            <p>正在接入 AI 知识库…</p>
          </div>
          <div v-else class="msg-list">
            <div v-if="hasMore" class="load-earlier" @click="loadEarlier">
              {{ loadingEarlier ? '加载中…' : '加载更早消息' }}
            </div>

            <div
              v-for="(msg, mi) in messages"
              :key="mi"
              class="msg-row"
              :class="msg.role === 'user' ? 'right' : 'left'"
            >
              <!-- 用户气泡 -->
              <template v-if="msg.role === 'user'">
                <div class="u-bubble">
                  <img v-if="msg.image" class="u-img" :src="msg.image" alt="附件图片" />
                  <span v-html="escHTML(msg.content).replace(/\n/g, '<br>')"></span>
                </div>
              </template>

              <!-- AI 回答列：步骤树 / 回答 / 参考来源 / 操作条 -->
              <template v-else>
                <div class="ai-col">
                  <div v-if="msg.steps && msg.steps.length" class="think-box">
                    <div
                      class="think-head"
                      @click="!msg.streaming && (msg.thinkingExpanded = !msg.thinkingExpanded)"
                    >
                      <span class="think-dot" :class="{ on: msg.streaming }"></span>
                      <span class="think-label">
                        {{ msg.thinkLabel || (msg.streaming ? '正在思考…' : '思考过程') }}
                      </span>
                      <span
                        class="think-chev"
                        :style="{
                          transform: msg.streaming || msg.thinkingExpanded ? 'rotate(90deg)' : '',
                        }"
                      >
                        <span v-html="icon('chevron-right', 13)"></span>
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
                          <div class="step-icon" v-html="stepIcon(step)"></div>
                          <div v-if="si < msg.steps.length - 1" class="step-line"></div>
                        </div>
                        <div class="step-main">
                          <div v-if="step.kind === 'tool'" class="step-title">
                            {{ step.title || '工具调用' }}
                          </div>
                          <div v-if="step.summary" class="step-summary">{{ step.summary }}</div>
                          <div v-if="step.kind === 'thought'" class="step-text">
                            {{ step.text }}
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>

                  <div class="a-bubble" :class="{ err: msg.error }">
                    <template
                      v-if="msg.streaming && !msg.content && !(msg.steps && msg.steps.length)"
                    >
                      <span class="typing"><i></i><i></i><i></i></span>
                      <span class="typing-txt">正在思考…</span>
                    </template>
                    <template v-else>
                      <span v-html="mdHtml(msg.content)"></span>
                    </template>
                    <span v-if="msg.streaming && msg.content" class="caret"></span>
                  </div>

                  <div v-if="msg.references && msg.references.length" class="ref-box">
                    <div class="ref-title">参考来源</div>
                    <div v-for="(r, ri) in msg.references" :key="ri" class="ref-item">
                      {{ r.title || '未命名资料' }}
                    </div>
                  </div>

                  <div v-if="!msg.streaming && !msg.error && msg.content" class="msg-acts">
                    <span class="act-btn" title="复制回答" @click="copyText(msg.content)">
                      <span v-html="icon('copy', 14)"></span>
                    </span>
                    <span
                      v-if="msg.msgId"
                      class="act-btn"
                      title="删除问答"
                      @click="onDeleteMsg(msg)"
                    >
                      <span v-html="icon('delete', 14)"></span>
                    </span>
                    <span class="msg-time">{{ msg.timeText }}</span>
                  </div>
                </div>
              </template>
            </div>
          </div>
        </div>

        <!-- 输入区：chips 在卡上、提示在卡下 -->
        <div class="cm-input">
          <div v-if="pendingImage || attachments.length" class="cm-chip-row">
            <div v-if="pendingImage" class="img-chip">
              <img :src="pendingImage" alt="待发送图片" />
              <button class="chip-x" title="移除图片" @click="clearImage">
                <span v-html="icon('close', 10)"></span>
              </button>
            </div>
            <div
              v-for="att in attachments"
              :key="att.uid"
              class="att-chip"
              :class="{ failed: att.status === 'failed' }"
            >
              <div class="a-ic"><span v-html="icon('file', 16)"></span></div>
              <div class="a-main">
                <div class="a-name">{{ att.name }}</div>
                <div class="a-sub">{{ attSubText(att) }}</div>
              </div>
              <button class="chip-x" title="移除附件" @click="removeAttachment(att)">
                <span v-html="icon('close', 10)"></span>
              </button>
            </div>
          </div>

          <div class="cm-box">
            <textarea
              ref="inputEl"
              v-model="draft"
              class="cm-text"
              rows="3"
              maxlength="2000"
              placeholder="向 AI 知识库提问，Enter 发送 / Shift+Enter 换行"
              @input="autoGrow"
              @keydown="onKeydown"
            ></textarea>
            <div class="cm-toolbar">
              <button class="cm-tool" title="添加图片" @click="imageInput && imageInput.click()">
                <span v-html="icon('image', 19)"></span>
              </button>
              <button
                class="cm-tool"
                title="添加附件"
                :disabled="!activeId"
                @click="attInput && attInput.click()"
              >
                <span v-html="icon('attach', 19)"></span>
              </button>
              <div class="cm-tools-r">
                <button
                  v-if="caps && caps.web_search"
                  class="cm-ws"
                  :class="{ on: webSearch }"
                  title="联网搜索"
                  @click="toggleWs"
                >
                  <span v-html="icon('globe', 14)"></span>
                  <span class="ws-t">联网搜索</span>
                </button>
                <select
                  v-if="hasEffortLevels"
                  v-model="reasoningEffort"
                  class="cm-effort"
                  @change="saveCapsPrefs"
                >
                  <option value="">默认</option>
                  <option v-for="lv in effortOptions" :key="lv" :value="lv">
                    {{ effortLabel(lv) }}
                  </option>
                </select>
                <select
                  v-model="modelId"
                  class="cm-model"
                  :disabled="!caps || (caps.models || []).length <= 1"
                  @change="onModelChange"
                >
                  <option v-if="!caps || !(caps.models || []).length" value="">默认模型</option>
                  <option v-for="m in (caps && caps.models) || []" :key="m.id" :value="m.id">
                    {{ m.name || m.id }}
                  </option>
                </select>
                <button
                  class="cm-send"
                  :class="{ off: !canSend }"
                  title="发送"
                  :disabled="!canSend"
                  @click="onSend"
                >
                  <span v-html="icon('send', 17)"></span>
                </button>
              </div>
            </div>
          </div>
          <div class="cm-hint">回答由 AI 基于班组知识库生成，关键操作请以现行规程为准</div>

          <!-- 图片选择（本地读取为 base64 dataURL 上送，不经 COS） -->
          <input
            ref="imageInput"
            type="file"
            accept=".jpg,.jpeg,.png,.gif,.webp"
            hidden
            @change="onPickImage"
          />
          <!-- 附件选择（文档/音频等，≤50MB，走 /attachments 上传 + 解析状态轮询） -->
          <input ref="attInput" type="file" hidden multiple @change="onPickFiles" />
        </div>
      </main>
    </div>

    <!-- ================= 弹层：新建 / 重命名输入 & 删除确认（旧式 .modal-t/.modal-b/.modal-f） ================= -->
    <div v-if="overlay.visible" class="mask open" @click.self="closeOverlay(false)">
      <div class="modal">
        <div class="modal-t">{{ overlay.title }}</div>
        <div class="modal-b">
          <input
            v-if="overlay.prompt"
            ref="overlayInputEl"
            v-model="overlay.value"
            class="input"
            :placeholder="overlay.placeholder"
            @keydown.enter="closeOverlay(true)"
          />
          <template v-else>{{ overlay.content }}</template>
        </div>
        <div class="modal-f">
          <button class="btn btn-sm" @click="closeOverlay(false)">取消</button>
          <button
            class="btn btn-sm"
            :class="overlay.danger ? 'btn-danger' : 'btn-primary'"
            @click="closeOverlay(true)"
          >
            {{ overlay.okText }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
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
import { currentUser } from '../api.js';

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
      low: '较低',
      medium: '中等',
      high: '较高',
      xhigh: '超高',
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

// onNewSession 新建会话（旧式输入弹层：留空则首轮对话后自动命名）。
async function onNewSession() {
  if (sending.value) {
    MessagePlugin.warning('回答生成中，请稍候');
    return;
  }
  const name = await promptBox(
    '新建对话',
    '',
    '创建',
    '请输入对话标题（留空则首轮对话后自动生成）'
  );
  if (name === null) return;
  createSession(name.trim());
}

// renameSession 重命名会话（旧式输入弹层）。
async function renameSession(s) {
  const name = await promptBox('重命名对话', s.title || '', '保存', '请输入新的对话名称');
  if (name === null) return;
  const title = name.trim();
  if (!title) {
    MessagePlugin.error('名称不能为空');
    return;
  }
  try {
    await updateSession(s.id, title);
    s.title = title;
    if (s.id === activeId.value) activeTitle.value = title;
    MessagePlugin.success('已保存');
  } catch (err) {
    MessagePlugin.error(err.message || '重命名失败');
  }
}

// onDeleteSession 删除会话（旧式确认弹层）。
async function onDeleteSession(s) {
  const ok = await confirmBox(
    '删除会话？',
    `将删除「${s.title || '新对话'}」及其全部消息，不可恢复。`,
    '删除'
  );
  if (!ok) return;
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
// onDeleteMsg 问答成对删除（旧式确认弹层，同 request_id）。
async function onDeleteMsg(msg) {
  if (!msg.msgId) return;
  const ok = await confirmBox('删除该问答？', '将成对删除本轮提问与回答，不可恢复。', '删除');
  if (!ok) return;
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

/* ================= 旧版还原补充：图标 / 侧栏抽屉 / 旧式弹层 / 步骤图标 / 用户条 / 能力控件 ================= */

// 旧页线性图标（路径与 server/public/assets/icons.js 同源；viewBox 24，描边 1.7 圆角端点）。
const ICONS = {
  add: '<path d="M12 5v14M5 12h14"/>',
  attach:
    '<path d="M18.5 10.8l-6.9 6.9a4.6 4.6 0 0 1-6.5-6.5l7.4-7.4a3.2 3.2 0 0 1 4.5 4.5l-7.4 7.4a1.8 1.8 0 0 1-2.5-2.5l6.4-6.4"/>',
  chat: '<path d="M20.5 4.5h-17A1.5 1.5 0 0 0 2 6v9a1.5 1.5 0 0 0 1.5 1.5H7l4 4 4-4h5.5A1.5 1.5 0 0 0 22 15V6a1.5 1.5 0 0 0-1.5-1.5z"/><circle cx="8" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="12" cy="10.5" r="1" fill="currentColor" stroke="none"/><circle cx="16" cy="10.5" r="1" fill="currentColor" stroke="none"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  delete:
    '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
  edit: '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
  globe:
    '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17"/><path d="M12 3.5c2.4 2.2 3.7 5.1 3.7 8.5s-1.3 6.3-3.7 8.5c-2.4-2.2-3.7-5.1-3.7-8.5s1.3-6.3 3.7-8.5z"/>',
  image:
    '<rect x="3.5" y="5" width="17" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M21 15.5l-4.5-4.5L8 19.5"/>',
  lightbulb:
    '<path d="M9.5 18.5h5M10.5 21h3"/><path d="M12 3.5a5.8 5.8 0 0 0-3.6 10.3c.8.7 1.4 1.6 1.6 2.7h4c.2-1.1.8-2 1.6-2.7A5.8 5.8 0 0 0 12 3.5z"/>',
  robot:
    '<rect x="4" y="8" width="16" height="11.5" rx="2.5"/><path d="M12 4.5V8"/><circle cx="12" cy="3.8" r=".9" fill="currentColor" stroke="none"/><circle cx="9" cy="13" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="13" r="1" fill="currentColor" stroke="none"/><path d="M9.5 16.5h5"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  send: '<path d="M21 3.5L3 10.5l7 2.5 2.5 7z"/><path d="M21 3.5L10 13"/>',
  tools:
    '<path d="M14.6 6.2a4 4 0 0 0-5.5 5.2L4 16.5V20h3.5l5.1-5.1a4 4 0 0 0 5.2-5.5l-2.7 2.7-2.4-.6-.6-2.4z"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
};

// icon 生成 inline SVG 图标字符串（同旧页 Shade.icon）。
function icon(name, size) {
  const body = ICONS[name] || ICONS.chat;
  const s = size || 24;
  return (
    '<svg width="' +
    s +
    '" height="' +
    s +
    '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' +
    body +
    '</svg>'
  );
}

// stepIcon 步骤树节点图标：思考=灯泡，工具按工具名映射（同旧页 toolIcon）。
function stepIcon(step) {
  if (step.kind === 'thought') return icon('lightbulb', 14);
  const name = step.name || '';
  if (
    name === 'knowledge_search' ||
    name === 'search_knowledge' ||
    name === 'grep_chunks' ||
    name === 'web_search'
  ) {
    return icon('search', 14);
  }
  if (name === 'image_analysis') return icon('image', 14);
  return icon('tools', 14);
}

/* ================= 窄屏侧栏抽屉（≤860px 右侧滑出；宽屏无感） ================= */
const sideOpen = ref(false);

/* ================= 旧式弹层（新建/重命名输入 .modal-b 内输入框 + 删除确认） ================= */
const overlay = reactive({
  visible: false,
  prompt: false,
  title: '',
  content: '',
  placeholder: '',
  value: '',
  okText: '确定',
  danger: false,
  _resolve: null,
});
const overlayInputEl = ref(null);

// closeOverlay 关闭弹层：确认返回 true/false，输入返回文本，取消返回 null。
function closeOverlay(ok) {
  const resolve = overlay._resolve;
  const value = overlay.value;
  overlay._resolve = null;
  overlay.visible = false;
  if (!resolve) return;
  if (!ok) {
    resolve(null);
    return;
  }
  resolve(overlay.prompt ? value : true);
}

// promptBox 旧式输入弹层（同旧页 promptModal：取消返回 null）。
function promptBox(title, initial, okText, placeholder) {
  return new Promise((resolve) => {
    overlay.prompt = true;
    overlay.title = title;
    overlay.content = '';
    overlay.placeholder = placeholder || '';
    overlay.value = initial || '';
    overlay.okText = okText || '确定';
    overlay.danger = false;
    overlay._resolve = resolve;
    overlay.visible = true;
    nextTick(() => {
      const el = overlayInputEl.value;
      if (el) {
        el.focus();
        el.select();
      }
    });
  });
}

// confirmBox 旧式确认弹层（同旧页 confirmModal：危险按钮红底）。
function confirmBox(title, content, okText) {
  return new Promise((resolve) => {
    overlay.prompt = false;
    overlay.title = title;
    overlay.content = content;
    overlay.value = '';
    overlay.okText = okText || '确定';
    overlay.danger = true;
    overlay._resolve = resolve;
    overlay.visible = true;
  });
}

// onCmKey Esc 关闭弹层。
function onCmKey(e) {
  if (e.key === 'Escape' && overlay.visible) {
    e.preventDefault();
    closeOverlay(false);
  }
}

/* ================= 侧栏底部用户条（同旧页 renderUser 口径） ================= */
const me = currentUser() || {};
const userName = me.nickname || me.username || '班组成员';
const userAvatarChar = userName.charAt(0);
const userRoleText =
  me.role === 'admin' ? '管理员' : me.role === 'team_admin' ? '班组管理员' : '普通用户';

/* ================= 能力控件：思考强度档位显隐 / 联网开关 / 模型切换 ================= */
// hasEffortLevels 当前模型带思考强度档位时才显示强度下拉（同旧页 refreshEffortSel）。
const hasEffortLevels = computed(() => {
  const models = (caps.value && caps.value.models) || [];
  const m = models.find((x) => x.id === modelId.value);
  return !!(m && Array.isArray(m.thinking_levels) && m.thinking_levels.length);
});

// toggleWs 联网搜索胶囊开关（开=品牌蓝底白字，记忆于 localStorage）。
function toggleWs() {
  webSearch.value = !webSearch.value;
  saveCapsPrefs();
}

// onModelChange 模型切换：档位不在新模型清单即回退默认，并记忆选择。
function onModelChange() {
  const models = (caps.value && caps.value.models) || [];
  const m = models.find((x) => x.id === modelId.value);
  const levels = (m && Array.isArray(m.thinking_levels) && m.thinking_levels) || [];
  if (!levels.length || levels.indexOf(reasoningEffort.value) < 0) reasoningEffort.value = '';
  saveCapsPrefs();
}

onMounted(() => {
  window.addEventListener('keydown', onCmKey);
});

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onCmKey);
});
</script>

<style scoped>
/* ===== 「政企蓝白」旧版 token（值与 server/public/assets/theme.css 一致），限定本组件 ===== */
.cm-root {
  --paper: #f5f7fa;
  --card: #ffffff;
  --navy: #1d2129;
  --orange: #0e3da8;
  --orange-soft: #e8f0ff;
  --on-brand: #ffffff;
  --inverse-bg: #1d2129;
  --inverse-text: #ffffff;
  --red: #f53f3f;
  --green: #00b42a;
  --yellow: #ff7d00;
  --warn-soft: #fff7e8;
  --warn-text: #d25f00;
  --ok-soft: #e8ffea;
  --ok-text: #0b8a37;
  --danger-soft: #ffece8;
  --danger-line: #ffd0c7;
  --line: #e5e6eb;
  --hair: rgba(29, 33, 41, 0.06);
  --sub: #4e5969;
  --ph: #86909c;
  --dash: #c9cdd4;
  --fill: #f7f8fa;
  --brand-glow: rgba(14, 61, 168, 0.12);
  --overlay: rgba(29, 33, 41, 0.45);
  --skeleton: rgba(29, 33, 41, 0.05);
  --ease: cubic-bezier(0.22, 1, 0.36, 1);
  --radius: 12px;
  --radius-sm: 8px;
  --hard: 0 2px 8px rgba(0, 0, 0, 0.04);
  --hard-s: 0 1px 4px rgba(0, 0, 0, 0.05);
  --shadow-card: 0 2px 8px rgba(0, 0, 0, 0.04);
  --shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.08);
  /* 整页骨架：覆盖父级内容区内边距，整页不滚动、仅卡片内部滚动（同旧页 body 锁定口径） */
  height: calc(100% + 40px);
  margin: -20px -24px;
  box-sizing: border-box;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  padding: 28px 40px 32px;
}

/* ===== 以下布局样式移植自 server/public/callme.html（旧版基准） ===== */
.cm {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: row-reverse;
  gap: 20px;
  width: 100%;
}
.cm-mask {
  display: none;
  position: fixed;
  inset: 0;
  z-index: 110;
  background: var(--overlay);
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.3s;
}

/* ===== 右侧会话栏（悬浮卡片：白底直角 + 2px 墨边 + 硬影），满宽贴边（右侧会话栏即页面最右） ===== */
.cm-side {
  width: 262px;
  flex: none;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  box-shadow: var(--shadow-card);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.cm-side-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 18px 14px;
}
.cm-logo {
  width: 36px;
  height: 36px;
  flex: none;
  border-radius: 8px;
  background: var(--orange);
  color: var(--on-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.cm-logo svg {
  width: 20px;
  height: 20px;
}
.cm-side-head b {
  font-size: 14.5px;
  letter-spacing: 1px;
}
.cm-side-head small {
  display: block;
  margin-top: 2px;
  font-size: 10.5px;
  color: var(--ph);
  letter-spacing: 1px;
}
.cm-new {
  margin: 0 14px 12px;
  height: 40px;
  border: 1px solid var(--line);
  background: var(--card);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  font-size: 13px;
  font-weight: 700;
  color: var(--navy);
  letter-spacing: 1px;
  cursor: pointer;
  transition:
    border-color 0.25s,
    color 0.25s,
    box-shadow 0.25s;
}
.cm-new svg {
  width: 15px;
  height: 15px;
}
.cm-new:hover {
  border-color: var(--orange);
  color: var(--orange);
  box-shadow: var(--hard-s);
}
.cm-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 0 10px 10px;
}
.cm-lab {
  padding: 6px 10px 8px;
  font-size: 11px;
  color: var(--ph);
  letter-spacing: 2px;
}
.cm-item {
  position: relative;
  display: flex;
  gap: 9px;
  padding: 10px 12px;
  margin-bottom: 2px;
  border-radius: 8px;
  border-left: 3px solid transparent;
  cursor: pointer;
  transition:
    background 0.25s,
    border-color 0.25s;
}
.cm-item:hover {
  background: var(--paper);
}
.cm-item.on {
  background: var(--orange-soft);
  border-left-color: var(--orange);
}
.cm-item > svg {
  width: 16px;
  height: 16px;
  flex: none;
  margin-top: 2px;
  color: var(--sub);
}
.cm-item.on > svg {
  color: var(--orange);
}
.cm-item-main {
  flex: 1;
  min-width: 0;
}
.cm-item .tt {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.5;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cm-item .tm {
  margin-top: 3px;
  font-size: 11px;
  color: var(--ph);
}
/* 悬停浮现的重命名 / 删除按钮 */
.cm-item-acts {
  position: absolute;
  right: 8px;
  top: 8px;
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.2s;
}
.cm-item:hover .cm-item-acts,
.cm-item.on .cm-item-acts {
  opacity: 1;
}
.ia {
  width: 22px;
  height: 22px;
  border-radius: 8px;
  background: var(--card);
  border: 1px solid var(--hair);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ph);
  transition:
    color 0.2s,
    border-color 0.2s;
}
.ia svg {
  width: 12px;
  height: 12px;
}
.ia:hover {
  color: var(--orange);
  border-color: var(--orange);
}
.ia.danger:hover {
  color: var(--red);
  border-color: var(--red);
}
.list-err {
  padding: 26px 14px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  color: var(--ph);
  text-align: center;
  line-height: 1.8;
}
.list-err svg {
  width: 22px;
  height: 22px;
  color: var(--dash);
}
/* 会话列表骨架 */
.sk-item {
  display: flex;
  gap: 9px;
  padding: 10px 12px;
  margin-bottom: 4px;
}
.sk-item .s-ic {
  width: 16px;
  height: 16px;
  flex: none;
  border-radius: 8px;
}
.sk-item .s-tx {
  flex: 1;
}
.sk-item .s-l1 {
  height: 12px;
  width: 78%;
  border-radius: 8px;
}
.sk-item .s-l2 {
  height: 9px;
  width: 42%;
  border-radius: 8px;
  margin-top: 7px;
}
/* 底部用户条 */
.cm-side-foot {
  border-top: 1px solid var(--hair);
  padding: 12px 16px;
  display: flex;
  align-items: center;
  gap: 10px;
}
.cm-side-foot .mini-avatar {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  font-size: 12px;
}
.cm-side-foot .nm {
  font-size: 12.5px;
  font-weight: 700;
}
.cm-side-foot .rl {
  font-size: 10.5px;
  color: var(--ph);
  margin-top: 1px;
}

/* ===== 对话区：无卡片，消息直接落在页面浅灰画布上（对齐官方前端）；
         聊天列限宽 960px 居中于对话区（auto 外边距吸收剩余宽度），两侧留白防超宽难读 ===== */
.cm-main {
  flex: 1;
  min-width: 0;
  max-width: 960px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.cm-head {
  height: 56px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 30px 0 22px;
}
.cm-toggle {
  display: none;
  width: 32px;
  height: 32px;
  flex: none;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--sub);
  align-items: center;
  justify-content: center;
  cursor: pointer;
}
.cm-toggle svg {
  width: 16px;
  height: 16px;
}
.cm-h-title {
  font-size: 14.5px;
  font-weight: 700;
  letter-spacing: 0.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cm-head .src {
  margin-left: auto;
  flex: none;
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 11.5px;
  color: var(--sub);
}
.pulse-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--orange);
} /* 在线状态静态圆点（原呼吸圈属氛围动效，已删） */

.cm-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 30px 30px;
}
.msg-list {
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.chat-start {
  text-align: center;
  font-size: 12px;
  color: var(--ph);
  letter-spacing: 1px;
  padding: 6px 0;
}
.sk-bubble {
  width: min(460px, 70%);
  height: 96px;
  border-radius: 8px;
}

/* 欢迎/空态 */
.cm-welcome {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
  text-align: center;
  padding: 20px;
  color: var(--ph);
}
.cm-welcome .w-ic {
  width: 72px;
  height: 72px;
  border-radius: 8px;
  background: var(--orange-soft);
  color: var(--orange);
  display: flex;
  align-items: center;
  justify-content: center;
}
.cm-welcome .w-ic svg {
  width: 36px;
  height: 36px;
}
.cm-welcome b {
  font-size: 16px;
  color: var(--navy);
  letter-spacing: 1px;
}
.cm-welcome p {
  font-size: 12.5px;
  letter-spacing: 1px;
  line-height: 2;
}

/* 消息行 */
.msg-row {
  display: flex;
  animation: fadeUp 0.3s var(--ease) both;
}
.msg-row.right {
  justify-content: flex-end;
}
.msg-row.left {
  width: 100%;
}
.u-bubble {
  max-width: 72%;
  background: var(--orange);
  color: var(--on-brand);
  border-radius: 8px;
  padding: 12px 17px;
  font-size: 13.5px;
  line-height: 1.8;
  word-break: break-word;
}
.u-img {
  display: block;
  max-width: 240px;
  max-height: 240px;
  border-radius: 8px;
  margin-bottom: 8px;
}
.ai-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
/* AI 回答不用气泡（对齐官方前端）：纯文本流式内容，仅用户问题保留气泡 */
.a-bubble {
  font-size: 13.5px;
  line-height: 1.9;
  word-break: break-word;
  color: var(--navy);
}
.a-bubble.err {
  color: var(--red);
}

/* 步骤树（思考过程）：摘要行 + 可展开树 */
.think-box {
  margin-bottom: 6px;
}
.think-head {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
  font-size: 12px;
  color: var(--ph);
  cursor: pointer;
  user-select: none;
}
.think-dot {
  width: 7px;
  height: 7px;
  flex: none;
  border-radius: 50%;
  background: var(--dash);
}
.think-dot.on {
  background: var(--orange);
}
.think-chev {
  margin-left: 4px;
  display: flex;
  color: var(--ph);
  transition: transform 0.25s;
}
.think-chev svg {
  width: 13px;
  height: 13px;
}
.think-body {
  padding: 6px 0 2px;
  max-height: 240px;
  overflow-y: auto;
}

/* 步骤节点：左图标轨 + 连接线；running 态图标脉冲 + 标题呼吸 */
.step {
  display: flex;
  align-items: stretch;
}
.step-rail {
  width: 22px;
  flex: none;
  display: flex;
  flex-direction: column;
  align-items: center;
}
.step-icon {
  width: 22px;
  height: 22px;
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ph);
}
.step-icon svg {
  width: 14px;
  height: 14px;
}
.step.running .step-icon {
  color: var(--orange);
}
.step.error .step-icon {
  color: var(--red);
}
.step-line {
  width: 1px;
  flex: 1;
  min-height: 6px;
  background: var(--hair);
}
.step-main {
  flex: 1;
  min-width: 0;
  padding: 1px 0 12px 8px;
}
.step:last-child .step-main {
  padding-bottom: 2px;
}
.step-title {
  font-size: 12.5px;
  color: var(--navy);
  font-weight: 600;
  line-height: 1.5;
  word-break: break-word;
}
.step.running .step-title {
  color: var(--orange);
}
.step.error .step-title {
  color: var(--red);
}
.step-summary {
  margin-top: 2px;
  font-size: 12px;
  color: var(--ph);
  line-height: 1.6;
}
.step-text {
  font-size: 12px;
  color: var(--ph);
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

/* 引用来源 */
.ref-box {
  margin-top: 8px;
  background: var(--paper);
  border: 1px solid var(--hair);
  border-radius: 8px;
  padding: 9px 13px;
}
.ref-title {
  font-size: 11px;
  color: var(--ph);
  letter-spacing: 2px;
  margin-bottom: 4px;
}
.ref-item {
  font-size: 12px;
  color: var(--sub);
  line-height: 1.7;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 加载更早消息 */
.load-earlier {
  align-self: center;
  margin: 0 auto;
  padding: 5px 16px;
  font-size: 12px;
  color: var(--ph);
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  cursor: pointer;
  transition:
    color 0.2s,
    border-color 0.2s;
}
.load-earlier:hover {
  color: var(--orange);
  border-color: var(--orange);
}

/* 流式光标 / 呼吸点 */
.caret {
  display: inline-block;
  width: 8px;
  height: 15px;
  margin-left: 3px;
  border-radius: 8px;
  background: var(--orange);
  vertical-align: -2px;
  animation: blink 1s steps(2, start) infinite;
}
@keyframes blink {
  50% {
    opacity: 0;
  }
}
.typing {
  display: inline-flex;
  gap: 5px;
  align-items: center;
}
.typing i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--orange);
  animation: breath 1.2s ease-in-out infinite;
}
.typing i:nth-child(2) {
  animation-delay: 0.15s;
}
.typing i:nth-child(3) {
  animation-delay: 0.3s;
}
@keyframes breath {
  0%,
  100% {
    opacity: 0.25;
    transform: scale(0.8);
  }
  50% {
    opacity: 1;
    transform: scale(1.05);
  }
}
.typing-txt {
  margin-left: 9px;
  font-size: 12px;
  color: var(--ph);
  letter-spacing: 1px;
}

/* 回答操作条：复制 / 删除 + 时间 */
.msg-acts {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 7px;
  padding-left: 2px;
}
.act-btn {
  width: 26px;
  height: 26px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ph);
  cursor: pointer;
  transition:
    background 0.2s,
    color 0.2s;
}
.act-btn svg {
  width: 14px;
  height: 14px;
}
.act-btn:hover {
  background: var(--orange-soft);
  color: var(--orange);
}
.msg-time {
  margin-left: 4px;
  font-size: 11.5px;
  color: var(--ph);
}

/* ===== markdown 渲染样式（对齐小程序端 MD_TAG_STYLE 色板） ===== */
.a-bubble h1,
.a-bubble h2,
.a-bubble h3,
.a-bubble h4 {
  color: var(--navy);
  font-weight: 700;
  line-height: 1.5;
  margin: 10px 0 6px;
}
.a-bubble h1 {
  font-size: 17px;
}
.a-bubble h2 {
  font-size: 16px;
}
.a-bubble h3,
.a-bubble h4 {
  font-size: 15px;
  margin: 8px 0 4px;
}
.a-bubble p {
  margin: 4px 0;
}
.a-bubble ul,
.a-bubble ol {
  margin: 4px 0 4px 1.2em;
  padding: 0;
}
.a-bubble li {
  margin: 2px 0;
}
.a-bubble ul > li::marker {
  color: var(--orange);
}
.a-bubble ol > li::marker {
  color: var(--orange);
  font-weight: 700;
}
.a-bubble strong {
  font-weight: 700;
}
.a-bubble a {
  color: var(--orange);
  text-decoration: underline;
}
.a-bubble hr {
  border: none;
  border-top: 1px solid var(--hair);
  margin: 10px 0;
}
.a-bubble code {
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Courier New', monospace;
  font-size: 0.9em;
  color: var(--orange);
  background: var(--paper);
  padding: 1px 5px;
  border-radius: 8px;
}
.a-bubble blockquote {
  border-left: 3px solid var(--orange);
  margin: 6px 0;
  padding: 2px 10px;
  color: var(--sub);
  background: var(--paper);
  border-radius: 8px;
}
/* 表格 */
.md-table {
  display: block;
  overflow-x: auto;
  margin: 8px 0;
}
.md-table table {
  border-collapse: collapse;
  font-size: 13px;
}
.md-table th {
  border: 1px solid var(--hair);
  background: var(--paper);
  padding: 6px 10px;
  font-weight: 600;
  text-align: left;
  white-space: nowrap;
  color: var(--navy);
}
.md-table td {
  border: 1px solid var(--hair);
  padding: 6px 10px;
}
/* 代码块（墨黑深底 + 标题栏 + 复制；深底固定不随主题反转，浅字常亮） */
.md-code {
  margin: 10px 0;
  background: #1d2129;
  border-radius: 8px;
  overflow: hidden;
}
.md-code-h {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 14px;
  font-size: 11px;
  color: rgba(255, 255, 255, 0.55);
  background: rgba(0, 0, 0, 0.18);
  letter-spacing: 1px;
}
.md-code-h .md-cp {
  margin-left: auto;
  color: rgba(255, 255, 255, 0.45);
  cursor: pointer;
  letter-spacing: 0;
  transition: color 0.2s;
}
.md-code-h .md-cp:hover {
  color: #fff;
}
.md-code pre {
  margin: 0;
  padding: 12px 16px;
  overflow-x: auto;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.85;
  color: #e8edf6;
  white-space: pre-wrap;
  word-break: break-word;
}
.md-code pre code {
  background: none;
  color: inherit;
  padding: 0;
  font-size: inherit;
}

/* ===== 底部输入区：悬浮卡片，满宽落于右侧区域底部；chips 在卡上、提示在卡下 ===== */
.cm-input {
  flex: none;
  padding: 0 30px 12px;
}
.cm-chip-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 10px;
  margin-bottom: 10px;
}
.img-chip {
  position: relative;
  width: 56px;
  height: 56px;
}
.img-chip img {
  width: 56px;
  height: 56px;
  border-radius: 8px;
  object-fit: cover;
  border: 1px solid var(--hair);
  background: var(--card);
}
.chip-x {
  position: absolute;
  top: -7px;
  right: -7px;
  width: 20px;
  height: 20px;
  border: none;
  border-radius: 8px;
  background: var(--inverse-bg);
  color: var(--inverse-text);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
}
.chip-x svg {
  width: 10px;
  height: 10px;
}
/* 附件芯片（非图片：图标 + 文件名 + 大小/状态；失败标红，可点 × 移除重传） */
.att-chip {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 56px;
  max-width: 230px;
  padding: 0 12px;
  background: var(--card);
  border: 1px solid var(--hair);
  border-radius: 8px;
}
.att-chip .a-ic {
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: 8px;
  background: var(--orange-soft);
  color: var(--orange);
  display: flex;
  align-items: center;
  justify-content: center;
}
.att-chip .a-ic svg {
  width: 16px;
  height: 16px;
}
.att-chip .a-main {
  min-width: 0;
}
.att-chip .a-name {
  max-width: 136px;
  font-size: 12px;
  font-weight: 600;
  color: var(--navy);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.att-chip .a-sub {
  margin-top: 2px;
  max-width: 150px;
  font-size: 10.5px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.att-chip.failed {
  border-color: var(--red);
}
.att-chip.failed .a-ic {
  background: var(--danger-soft);
  color: var(--red);
}
.att-chip.failed .a-sub {
  color: var(--red);
}
.cm-box {
  display: flex;
  flex-direction: column;
  gap: 4px;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 8px;
  box-shadow: var(--shadow-card);
  transition:
    border-color 0.25s,
    box-shadow 0.25s;
}
.cm-box:focus-within {
  border-color: var(--orange);
  box-shadow: var(--hard-s);
}
/* 工具行：左组附件图标钮；右组（margin-left:auto 顶右）联网胶囊 + 思考强度/模型下拉 + 发送钮 */
.cm-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
}
.cm-tools-r {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.cm-tool {
  width: 34px;
  height: 34px;
  flex: none;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--ph);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    color 0.25s,
    background 0.25s;
}
.cm-tool svg {
  width: 19px;
  height: 19px;
}
.cm-tool:hover {
  color: var(--orange);
  background: var(--orange-soft);
}
.cm-text {
  border: none;
  outline: none;
  background: transparent;
  resize: none;
  font-family: inherit;
  font-size: 13.5px;
  line-height: 1.7;
  color: var(--navy);
  padding: 6px 2px;
  max-height: 150px;
}
.cm-text::placeholder {
  color: var(--ph);
}
.cm-send {
  width: 36px;
  height: 36px;
  flex: none;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--orange);
  color: var(--on-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  box-shadow: var(--hard-s);
  transition:
    transform 0.25s,
    opacity 0.25s,
    box-shadow 0.25s;
}
.cm-send svg {
  width: 17px;
  height: 17px;
}
.cm-send:hover {
  transform: translate(2px, 2px);
  box-shadow: var(--shadow-card);
}
.cm-send.off {
  opacity: 0.4;
  cursor: not-allowed;
  transform: none !important;
  box-shadow: none;
}
.cm-hint {
  margin: 9px 0 0;
  text-align: center;
  font-size: 11px;
  color: var(--ph);
  letter-spacing: 0.5px;
}
/* 联网搜索胶囊开关（开=品牌蓝底白字 / 关=灰）与思考强度/模型下拉（复用 common.js .dd 体系，工具行紧凑化） */
.cm-ws {
  flex: none;
  display: flex;
  align-items: center;
  gap: 5px;
  height: 34px;
  padding: 0 12px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--fill);
  color: var(--sub);
  font-size: 12px;
  letter-spacing: 0.5px;
  cursor: pointer;
  white-space: nowrap;
  transition:
    background 0.2s,
    color 0.2s,
    border-color 0.2s;
}
.cm-ws svg {
  width: 14px;
  height: 14px;
}
.cm-ws.on {
  background: var(--orange);
  border-color: var(--orange);
  color: var(--on-brand);
}
.cm-model,
.cm-effort {
  flex: none;
}
.cm-model .dd-btn,
.cm-effort .dd-btn {
  font-size: 12px;
  font-weight: 500;
  color: var(--sub);
  padding: 0 10px;
  gap: 6px;
}
.cm-model .dd-btn .dd-label {
  max-width: 140px;
}
.cm-model .dd-btn .dd-tri,
.cm-effort .dd-btn .dd-tri {
  color: var(--ph);
}

/* ===== 通用弹窗（新建/重命名输入用 promptModal（.modal-t/.modal-b/.modal-f）；删除确认走 Shade.confirm，
     .m-head/.m-foot 结构与 quiz / worklog 同口径；直角 + 2px 墨边 + 硬影，白底卡片样式直接落在 .modal 上） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--overlay);
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.3s;
}
.mask.open {
  opacity: 1;
  pointer-events: auto;
}
.mask .modal {
  width: 400px;
  max-width: 92vw;
  padding: 22px 24px 18px;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  box-shadow: var(--shadow-card);
  transform: translateY(16px) scale(0.98);
  transition: transform 0.35s var(--ease);
}
.mask.open .modal {
  transform: none;
}
.modal-t {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 1px;
}
.modal-b {
  margin: 14px 0 20px;
  font-size: 13px;
  color: var(--sub);
  line-height: 1.8;
}
.modal-b .input {
  height: 42px;
}
.modal-f {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
/* Shade.confirm 确认弹层结构（与 quiz / worklog 同口径） */
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 4px;
}
.m-title {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--navy);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--shadow-card);
  transition:
    background 0.15s,
    color 0.15s;
}
.m-x:hover {
  background: var(--fill);
  color: var(--orange);
}
.m-x svg {
  width: 14px;
  height: 14px;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* 细滚动条 */
.cm-list::-webkit-scrollbar,
.cm-body::-webkit-scrollbar,
.think-body::-webkit-scrollbar {
  width: 6px;
}
.cm-list::-webkit-scrollbar-thumb,
.cm-body::-webkit-scrollbar-thumb,
.think-body::-webkit-scrollbar-thumb {
  background: var(--dash);
  border-radius: 8px;
}
.cm-list::-webkit-scrollbar-track,
.cm-body::-webkit-scrollbar-track,
.think-body::-webkit-scrollbar-track {
  background: transparent;
}

/* 入页动效：左右两卡错峰上浮（仅宽屏；窄屏侧栏为抽屉、自身带 transform，不叠加避免冲突） */
@media (min-width: 861px) {
  .cm-side,
  .cm-main {
    animation: fadeUp 0.6s var(--ease) both;
  }
  /* 错峰自左向右：主区在左先落位，会话栏在右延后 0.1s */
  .cm-side {
    animation-delay: 0.1s;
  }
}

/* 窄屏：呼吸边距与卡片间距收紧、侧栏收窄 */
@media (max-width: 960px) {
  .cm-root {
    padding: 20px 24px 24px;
  }
  .cm {
    gap: 14px;
  }
  .cm-side {
    width: 240px;
  }
}
/* 更窄屏：侧栏收起为右侧抽屉（贴视口全高，right:0 贴视口右缘滑出，方角），对话区独占 .page */
@media (max-width: 860px) {
  .cm-root {
    padding: 14px 14px 18px;
  }
  .cm-side {
    position: fixed;
    right: 0;
    top: 0;
    bottom: 0;
    z-index: 120;
    width: 272px;
    border-radius: 8px;
    border-right: none;
    transform: translateX(105%);
    transition: transform 0.3s var(--ease);
    box-shadow: -8px 0 0 rgba(29, 33, 41, 0.9);
  }
  .cm-root.side-open .cm-side {
    transform: none;
  }
  .cm-mask {
    display: block;
  }
  .cm-root.side-open .cm-mask {
    opacity: 1;
    pointer-events: auto;
  }
  .cm-toggle {
    display: inline-flex;
  }
  .cm-head {
    padding: 0 14px;
  }
  .cm-body {
    padding: 8px 14px 22px;
  }
  .cm-input {
    padding: 0 14px 12px;
  }
  /* 窄屏输入区收紧：工具行间距收紧、联网胶囊只留图标，模型名下拉限宽 */
  .cm-toolbar,
  .cm-tools-r {
    gap: 6px;
  }
  .cm-ws {
    padding: 0 9px;
  }
  .cm-ws .ws-t {
    display: none;
  }
  .cm-model .dd-btn .dd-label {
    max-width: 88px;
  }
}

/* ===== theme.css 共享件（按钮 / 输入 / 骨架 / 头像 / 入场动效），旧页口径随本组件 scoped 注入 ===== */
.mini-avatar {
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: 8px;
  background: var(--orange-soft);
  color: var(--orange);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 700;
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--line);
  background: var(--card);
  color: var(--navy);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
  transition:
    border-color 0.15s var(--ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
  font-family: inherit;
}
.btn svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.btn:hover:not([disabled]) {
  border-color: var(--orange);
  color: var(--orange);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary,
.btn-danger {
  color: var(--on-brand);
  border-color: transparent;
}
.btn-primary {
  background: var(--orange);
}
.btn-primary:hover:not([disabled]) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-danger {
  background: var(--red);
}
.btn-danger:hover:not([disabled]) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

.input {
  width: 100%;
  height: 44px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  background: var(--card);
  padding: 0 14px;
  font-size: 14px;
  color: var(--navy);
  outline: none;
  font-family: inherit;
  transition:
    border-color 0.15s var(--ease),
    box-shadow 0.15s var(--ease);
}
.input::placeholder {
  color: var(--ph);
}
.input:focus {
  border-color: var(--orange);
  box-shadow: 0 0 0 3px var(--brand-glow);
}

.skeleton {
  position: relative;
  overflow: hidden;
  background: var(--skeleton);
  border-radius: 8px;
}
.skeleton::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(100deg, transparent 25%, rgba(255, 255, 255, 0.65), transparent 75%);
  transform: translateX(-100%);
  animation: shimmer 1.6s ease-in-out infinite;
}
@keyframes shimmer {
  to {
    transform: translateX(100%);
  }
}

@keyframes fadeUp {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* ===== 本组件补位：图标 span 包裹 / 原生下拉（旧版 .dd-btn 口径） ===== */
.cm-item > span {
  flex: none;
  margin-top: 2px;
  display: inline-flex;
  color: var(--sub);
}
.cm-item.on > span {
  color: var(--orange);
}
.cm-model,
.cm-effort {
  flex: none;
  height: 34px;
  max-width: 150px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--sub);
  font-size: 12px;
  padding: 0 10px;
  outline: none;
  font-family: inherit;
  cursor: pointer;
}
.cm-model:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.cm-tool[disabled] {
  opacity: 0.45;
  cursor: not-allowed;
}

/* 动效降级 */
@media (prefers-reduced-motion: reduce) {
  .cm-side,
  .cm-main,
  .msg-row,
  .skeleton::after {
    animation: none !important;
  }
}
</style>
