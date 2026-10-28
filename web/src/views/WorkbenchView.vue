<template>
  <!-- 旧版工作台（server/public/index.html 同构）：紧凑身份行 + 四指标数据条 + 应用主任务区 + 通知辅助栏 + 安全提示条 -->
  <div class="wb-page">
    <!-- 紧凑身份行 -->
    <section class="user-card">
      <div class="uc-body">
        <div class="uc-main">
          <div class="uc-hello-row">
            <div class="uc-avatar">{{ avatarChar }}</div>
            <div class="uc-identity">
              <h2 class="uc-hello">{{ greeting }}，{{ displayName }}</h2>
              <div class="uc-meta">
                <span class="uc-role">
                  <span class="badge-outline">{{ roleText }}</span>
                  <span v-if="accountName" class="uname">账号：{{ accountName }}</span>
                </span>
              </div>
            </div>
          </div>
        </div>
        <div class="uc-date">{{ dateText }}</div>
      </div>
    </section>

    <!-- 四指标数据条（指标口径与小程序首页一致；无出工日志权限/未加载显示 —） -->
    <section class="card uc-stats" aria-label="出工日志数据概览">
      <div class="hstat" :class="statPrevPass.cls">
        <b>{{ statPrevPass.text }}</b><span>上月通过</span>
      </div>
      <div class="hstat" :class="statPrevNeed.cls">
        <b>{{ statPrevNeed.text }}</b><span>上月需补</span>
      </div>
      <div class="hstat" :class="statCurPass.cls">
        <b>{{ statCurPass.text }}</b><span>本月通过</span>
      </div>
      <div class="hstat" :class="statCurNeed.cls">
        <b>{{ statCurNeed.text }}</b><span>本月需补</span>
      </div>
    </section>

    <!-- 应用主任务区 + 通知辅助栏（窄屏按 DOM 顺序将通知移至应用区后） -->
    <div class="workspace-grid">
      <section class="app-section">
        <div class="section-title sec-app">
          <h3>应用中心</h3>
          <span class="more">{{ appCountText }}</span>
        </div>
        <section class="apps">
          <!-- 骨架卡片 -->
          <template v-if="appsLoading">
            <div class="card app-sk skeleton"></div>
            <div class="card app-sk skeleton"></div>
            <div class="card app-sk skeleton"></div>
          </template>
          <!-- 加载失败：真实错误信息 + 重新加载 -->
          <div v-else-if="appsError" class="empty">
            <LegacyIcon name="warn" :size="44" />
            <p>{{ appsError }}</p>
            <button class="btn btn-sm" type="button" @click="loadApps">重新加载</button>
          </div>
          <template v-else>
            <a
              v-for="app in apps"
              :key="app.code"
              class="card card-hover app-card"
              :href="app.route || 'javascript:void 0'"
              @click.prevent="enterApp(app)"
            >
              <div class="app-ic"><LegacyIcon :name="app.icon" :size="22" /></div>
              <div class="app-main">
                <h4>{{ app.name }}</h4>
                <p v-if="app.description">{{ app.description }}</p>
              </div>
              <div class="app-meta">
                <span v-for="t in app.tags" :key="t[0]" class="tag" :class="t[1]">{{ t[0] }}</span>
                <span class="app-go">进入应用<LegacyIcon name="arrow-right" :size="15" /></span>
              </div>
            </a>
            <!-- 空态占满整行 -->
            <div v-if="!apps.length" class="empty">
              <LegacyIcon name="app" :size="44" />
              <p>暂无可用应用，请联系管理员开通权限</p>
            </div>
            <!-- 虚线占位入口 -->
            <div class="card app-card app-soon">
              <div class="app-ic"><LegacyIcon name="ellipsis" :size="22" /></div>
              <h4>敬请期待</h4>
              <p>新应用接入中</p>
            </div>
          </template>
        </section>
      </section>

      <aside class="notice-side">
        <div class="section-title sec-msg">
          <h3>消息通知</h3>
          <span class="more" @click="openNoticeModal">
            <span v-if="noticeUnread > 0" class="nt-badge">{{ noticeUnread }} 条未读</span>
            <template v-else>{{ noticeBadgeText }}</template>
          </span>
        </div>
        <section class="card card-hover notice-card" @click="onNoticeCardClick">
          <div v-if="noticeLoading" class="loading"><span class="spin"></span>加载中</div>
          <div v-else-if="noticeError" class="empty">
            <LegacyIcon name="warn" :size="44" />
            <p>{{ noticeError }}</p>
            <button class="btn btn-sm" type="button" @click.stop="loadNotices">重新加载</button>
          </div>
          <template v-else>
            <div v-if="noticeTop.length">
              <div
                v-for="n in noticeTop.slice(0, 1)"
                :key="n.id"
                class="nt-item"
                :class="{ unread: !n.read }"
              >
                <span v-if="!n.read" class="nt-dot"></span>
                <span class="nt-body">
                  <span class="nt-title">{{ n.title }}</span>
                  <span class="nt-time">{{ fmtDate(n.createdAt) }}</span>
                </span>
              </div>
            </div>
            <div v-else class="empty">
              <LegacyIcon name="notification" :size="44" />
              <p>暂无通知</p>
            </div>
            <div class="notice-more" :hidden="!noticeExpanded">
              <div
                v-for="n in noticeRest"
                :key="n.id"
                class="nt-item"
                :class="{ unread: !n.read }"
              >
                <span v-if="!n.read" class="nt-dot"></span>
                <span class="nt-body">
                  <span class="nt-title">{{ n.title }}</span>
                  <span class="nt-time">{{ fmtDate(n.createdAt) }}</span>
                </span>
              </div>
            </div>
            <button
              v-if="noticeRest.length"
              class="notice-toggle"
              type="button"
              :aria-expanded="noticeExpanded ? 'true' : 'false'"
              @click.stop="noticeExpanded = !noticeExpanded"
            >
              {{ noticeExpanded ? '收起通知' : `展开其余 ${noticeRest.length} 条` }}
            </button>
          </template>
        </section>
      </aside>
    </div>

    <!-- 安全提示条 -->
    <section class="card safety">
      <span class="si"><LegacyIcon name="shield" :size="18" /></span>
      <span class="safety-tx"
        ><b>安全生产，预防为主。</b>上工前请确认劳保用品佩戴齐全，作业按规程执行。</span
      >
    </section>

    <!-- 通知列表弹层 -->
    <div class="mask" :class="{ show: noticeModal }" @click.self="noticeModal = false">
      <div class="modal">
        <div class="modal-hd">
          消息通知<button
            v-if="hasReadItem"
            class="btn btn-sm"
            type="button"
            id="noticeDelRead"
            @click="askDeleteRead"
          >
            删除已读 </button
          ><button
            v-if="noticeUnread > 0"
            class="btn btn-sm"
            type="button"
            id="noticeReadAll"
            @click="onReadAll"
          >
            全部已读 </button
          ><button class="x" type="button" title="关闭" @click="noticeModal = false">
            <LegacyIcon name="close" :size="16" />
          </button>
        </div>
        <div class="nm-list">
          <div v-if="modalLoading" class="loading"><span class="spin"></span>加载中</div>
          <div v-else-if="modalError" class="empty">
            <LegacyIcon name="warn" :size="44" />
            <p>{{ modalError }}</p>
          </div>
          <template v-else>
            <div v-if="!noticeItems.length" class="empty">
              <LegacyIcon name="notification" :size="44" />
              <p>暂无通知</p>
            </div>
            <div
              v-for="n in noticeItems"
              :key="n.id"
              class="nm-item"
              :class="{ unread: !n.read, open: openMap[n.id] }"
              @click="onModalItemClick(n)"
            >
              <div class="nm-head">
                <span v-if="!n.read" class="nt-dot"></span>
                <span class="nm-title">{{ n.title }}</span>
                <span class="nt-time">{{ fmtDate(n.createdAt) }}</span>
                <span class="nm-del" title="删除该通知" @click.stop="askDeleteNotice(n)">删除</span>
              </div>
              <!-- 内容默认两行折叠，点击展开全文（Markdown 渲染，失败回退转义纯文本） -->
              <div class="nm-content" v-html="noticeContentHtml(n)"></div>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 删除通知二次确认弹层（超管全局删除，其余角色仅从本人列表移除） -->
    <div class="mask" :class="{ show: delModal }">
      <div class="modal">
        <div class="modal-hd">
          <span>删除通知</span>
          <button class="x" type="button" title="关闭" @click="delModal = false">
            <LegacyIcon name="close" :size="16" />
          </button>
        </div>
        <div class="modal-bd">
          <div class="tip">{{ delTip }}</div>
        </div>
        <div class="modal-ft">
          <button class="btn" type="button" @click="delModal = false">取消</button>
          <button class="btn btn-danger" type="button" :disabled="delBusy" @click="confirmDelete">
            确认删除
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { currentUser, isAdmin, listApps } from '../api.js';
import { fetchStats } from '../features/worklog/api.js';
import { renderMd } from '../features/callme/md.js';
import { canonicalKey, metaForApp, routeForApp } from '../features/core/app-catalog.js';
import LegacyIcon from '../features/core/LegacyIcon.vue';

const router = useRouter();

// ===== 身份行（资料取登录会话，App 启动时 GET /api/v1/user/profile 刷新） =====
const displayName = computed(() => {
  const u = currentUser();
  return (u && (u.nickname || u.username)) || '班组成员';
});
const accountName = computed(() => {
  const u = currentUser();
  return (u && u.username) || '';
});
const avatarChar = computed(() => (displayName.value || '班').charAt(0));
const roleText = computed(() => {
  const u = currentUser();
  const role = u && u.role;
  return role === 'admin' ? '管理员' : role === 'team_admin' ? '班组管理员' : '普通用户';
});

// 按时段问候（与旧版 greeting() 同口径）
function greetingText() {
  const h = new Date().getHours();
  if (h < 6) return '夜深了';
  if (h < 9) return '早上好';
  if (h < 12) return '上午好';
  if (h < 14) return '中午好';
  if (h < 18) return '下午好';
  return '晚上好';
}
const greeting = greetingText();

const dateText = (() => {
  const now = new Date();
  const week = '日一二三四五六'.charAt(now.getDay());
  return (
    now.getFullYear() + ' 年 ' + (now.getMonth() + 1) + ' 月 ' + now.getDate() + ' 日 · 星期' + week
  );
})();

// ===== 四指标数据条（/api/v1/worklog/stats 口径）=====
// 通过 = 班组当月验证通过卡片数/全部卡片数；需补 = 我当月「未上传水印照片」的卡片条数；
// 无出工日志权限不请求、加载中与失败静默保留 —（与旧版一致）。
const statPrevPass = ref({ text: '—', cls: '' });
const statPrevNeed = ref({ text: '—', cls: '' });
const statCurPass = ref({ text: '—', cls: '' });
const statCurNeed = ref({ text: '—', cls: '' });

// 分级着色（与小程序首页同口径）：通过项按未通过数（total-passed）0 绿 / ≤3 橙 / >3 红；需补项同阈值
function statPassCls(passed, total) {
  const bad = Math.max(0, (Number(total) || 0) - (Number(passed) || 0));
  return bad === 0 ? 'good' : bad <= 3 ? 'warn' : 'bad';
}
function statNeedCls(n) {
  const c = Number(n) || 0;
  return c === 0 ? 'good' : c <= 3 ? 'warn' : 'bad';
}

async function loadStats(hasWorklog) {
  if (!hasWorklog) return;
  const now = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  const prev = new Date(now.getFullYear(), now.getMonth() - 1, 1);
  const prevMonth = prev.getFullYear() + '-' + pad(prev.getMonth() + 1);
  const curMonth = now.getFullYear() + '-' + pad(now.getMonth() + 1);
  try {
    const a = (await fetchStats({ month: prevMonth })) || {};
    statPrevPass.value = {
      text: (a.passed || 0) + '/' + (a.total || 0),
      cls: statPassCls(a.passed, a.total),
    };
    statPrevNeed.value = {
      text: String(a.needPhotos || 0),
      cls: statNeedCls(a.needPhotos),
    };
  } catch (e) {
    /* 失败静默：保留 — */
  }
  try {
    const b = (await fetchStats({ month: curMonth })) || {};
    statCurPass.value = {
      text: (b.passed || 0) + '/' + (b.total || 0),
      cls: statPassCls(b.passed, b.total),
    };
    statCurNeed.value = {
      text: String(b.needPhotos || 0),
      cls: statNeedCls(b.needPhotos),
    };
  } catch (e) {
    /* 失败静默：保留 — */
  }
}

// ===== 应用主任务区（GET /api/v1/app/list 按权限下发；网页端过滤 mobile-only） =====
const apps = ref([]);
const appsLoading = ref(true);
const appsError = ref('');
const appCountText = ref('加载中…');

async function loadApps() {
  appsLoading.value = true;
  appsError.value = '';
  appCountText.value = '加载中…';
  try {
    const list = await listApps();
    const fullList = list || [];
    // 网页端应用区按 terminal 过滤：只展示 双端/PC端 应用（移动端应用仅小程序可见）
    const shown = fullList.filter((a) => a.terminal !== 'mobile');
    apps.value = shown.map((a) => {
      const code = canonicalKey(a.code || a.app_key || '');
      const meta = metaForApp(code);
      return {
        code,
        // 展示名以目录为准（定稿展示名），后端 name 仅在目录未登记时兜底（见 app-catalog.js）
        name: (meta && meta.name) || a.name || code,
        description: a.description || (meta && meta.desc) || '',
        icon: a.icon || (meta && meta.icon) || 'app',
        tags: (meta && meta.tags) || [],
        route: routeForApp(code),
      };
    });
    appCountText.value = '按权限自动下发 · 共 ' + shown.length + ' 个可用应用';
    // 出工日志指标独立拉取，失败静默（无权限保持 —）
    loadStats(fullList.some((item) => (item.app_key || item.code) === 'work-log'));
  } catch (err) {
    appsError.value = (err && err.message) || '应用列表加载失败';
    appCountText.value = '加载失败';
  } finally {
    appsLoading.value = false;
  }
}

function enterApp(app) {
  if (app.route) {
    router.push(app.route);
    return;
  }
  MessagePlugin.info('该应用暂未开通网页版，请使用小程序访问');
}

// ===== 消息通知（GET /api/v1/notice/list；已读 / 删除 / 全部已读走 /api/v1/notice/*）=====
const noticeTop = ref([]); // 工作台卡内最新 3 条
const noticeItems = ref([]); // 弹层全量列表（limit=100）
const noticeUnread = ref(0); // 未读数（接口下发，随已读操作本地递减）
const noticeExpanded = ref(false); // 工作台默认仅显示最新 1 条，余下 2 条按需展开
const noticeLoading = ref(true);
const noticeError = ref('');
const noticeModal = ref(false);
const modalLoading = ref(false);
const modalError = ref('');
const openMap = reactive({}); // 弹层条目展开态 id → bool
const noticeBadgeText = ref('查看全部');

const noticeRest = computed(() => noticeTop.value.slice(1, 3));
const hasReadItem = computed(() => noticeItems.value.some((n) => n.read));

// 通知端点不在公共 api.js 适配层（该文件不在本次改动范围），此处按同一 JWT 会话直连真实接口；
// 鉴权头与 api.js 同源（sessionStorage 'shade.token'），响应契约 {code,message,data} 同口径。
function noticeToken() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}
async function noticeRequest(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  const token = noticeToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  let res;
  try {
    res = await fetch(path, { ...options, headers });
  } catch (e) {
    const err = new Error('无法连接服务，请检查网络或稍后重试');
    err.code = 'network_error';
    throw err;
  }
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    err.status = res.status;
    throw err;
  }
  return body.data;
}

function fmtDate(d) {
  const dt = d ? new Date(d) : new Date();
  if (isNaN(dt.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  return (
    dt.getFullYear() +
    '-' +
    p(dt.getMonth() + 1) +
    '-' +
    p(dt.getDate()) +
    ' ' +
    p(dt.getHours()) +
    ':' +
    p(dt.getMinutes())
  );
}

function escHtml(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (ch) => {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
  });
}

// 通知内容：Markdown 渲染，失败回退转义纯文本 + 换行（与旧版 noticeContentHtml 同口径）
function noticeContentHtml(n) {
  return renderMd(n.content) || escHtml(n.content).replace(/\n/g, '<br>');
}

// 拉取通知卡数据（失败在卡内展示真实错误，不阻断工作台）
async function loadNotices() {
  noticeLoading.value = true;
  noticeError.value = '';
  try {
    const data = await noticeRequest('/api/v1/notice/list?limit=3');
    noticeTop.value = (data && data.items) || [];
    noticeUnread.value = (data && data.unread) || 0;
    noticeBadgeText.value = '查看全部';
    if (!noticeRest.value.length) noticeExpanded.value = false;
  } catch (e) {
    noticeTop.value = [];
    noticeExpanded.value = false;
    noticeError.value = (e && e.message) || '通知加载失败';
    noticeBadgeText.value = '查看全部';
  } finally {
    noticeLoading.value = false;
  }
}

function onNoticeCardClick() {
  if (noticeError.value) return; // 错误态只响应「重新加载」
  openNoticeModal();
}

// 打开通知列表弹层：拉全量（limit=100）
async function openNoticeModal() {
  noticeModal.value = true;
  modalLoading.value = true;
  modalError.value = '';
  try {
    const data = await noticeRequest('/api/v1/notice/list?limit=100');
    noticeItems.value = (data && data.items) || [];
    noticeUnread.value = (data && data.unread) || 0;
    // 弹层与工作台卡同步标题/未读态
    noticeTop.value = noticeItems.value.slice(0, 3);
  } catch (e) {
    modalError.value = (e && e.message) || '通知加载失败';
  } finally {
    modalLoading.value = false;
  }
}

// 本地置已读：未读数递减、刷新徽章 / 工作台卡 / 弹层行
function markNoticeReadLocal(id) {
  const n = noticeItems.value.find((x) => x.id === id);
  if (n && !n.read) n.read = 1;
  const t = noticeTop.value.find((x) => x.id === id);
  if (t && !t.read) t.read = 1;
  noticeUnread.value = Math.max(0, noticeUnread.value - 1);
}

// 弹层条目点击：展开 / 收起全文；未读条目上报已读后本地置已读
async function onModalItemClick(n) {
  openMap[n.id] = !openMap[n.id];
  if (n.read) return;
  try {
    await noticeRequest(`/api/v1/notice/${n.id}/read`, { method: 'POST' });
    markNoticeReadLocal(n.id);
  } catch (e) {
    /* 已读上报失败保留未读态，不伪造成功 */
  }
}

// ---------- 删除通知（超管全局删除，其余角色仅从本人列表移除；二次确认） ----------
// delId 哨兵 -1 = 一键删除已读（按人删除，超管也不做全局删除）
const delId = ref(0);
const delTip = ref('');
const delModal = ref(false);
const delBusy = ref(false);

function askDeleteNotice(n) {
  delId.value = n.id;
  delTip.value = isAdmin()
    ? '确定删除通知「' + n.title + '」吗？删除后所有成员均不可见，且不可恢复。'
    : '确定删除通知「' + n.title + '」吗？删除后仅从您的通知列表移除，不影响其他成员。';
  delModal.value = true;
}
function askDeleteRead() {
  delId.value = -1;
  delTip.value = '确定删除全部已读通知吗？删除后仅从您的通知列表移除，不影响其他成员。';
  delModal.value = true;
}

async function confirmDelete() {
  if (!delId.value || delBusy.value) return;
  delBusy.value = true;
  try {
    if (delId.value === -1) {
      const data = await noticeRequest('/api/v1/notice/del-read', { method: 'POST' });
      noticeItems.value = noticeItems.value.filter((x) => !x.read);
      noticeTop.value = noticeTop.value.filter((x) => !x.read);
      delModal.value = false;
      MessagePlugin.success('已删除 ' + ((data && data.deleted) || 0) + ' 条已读通知');
    } else {
      await noticeRequest(`/api/v1/notice/${delId.value}`, { method: 'DELETE' });
      const wasUnread = noticeItems.value.some((x) => x.id === delId.value && !x.read);
      noticeItems.value = noticeItems.value.filter((x) => x.id !== delId.value);
      noticeTop.value = noticeTop.value.filter((x) => x.id !== delId.value);
      if (wasUnread) noticeUnread.value = Math.max(0, noticeUnread.value - 1);
      delModal.value = false;
      MessagePlugin.success('已删除');
    }
  } catch (e) {
    MessagePlugin.error((e && e.message) || '删除失败');
  } finally {
    delBusy.value = false;
    delId.value = 0;
  }
}

// 全部已读（有未读时显示）
async function onReadAll() {
  try {
    await noticeRequest('/api/v1/notice/read-all', { method: 'POST' });
    noticeUnread.value = 0;
    noticeItems.value.forEach((n) => {
      n.read = 1;
    });
    noticeTop.value.forEach((n) => {
      n.read = 1;
    });
  } catch (e) {
    MessagePlugin.error((e && e.message) || '操作失败');
  }
}

onMounted(() => {
  loadApps();
  loadNotices();
});
</script>
