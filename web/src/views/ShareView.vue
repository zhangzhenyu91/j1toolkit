<template>
  <div class="sh-page">
    <div class="sh-head">
      <div class="sh-brand">
        <div class="sh-mark">壹</div>
        <span>Shade 壹匣 · 文件分享</span>
      </div>
    </div>

    <div class="sh-card">
      <!-- 载入 / 错误态 -->
      <div v-if="state === 'loading'" class="sh-center muted">正在加载分享信息…</div>

      <div v-else-if="state === 'error'" class="sh-center">
        <div class="sh-err-ic">!</div>
        <h2 class="sh-title">{{ errMsg || '分享不可用' }}</h2>
        <p v-if="errDetail" class="muted">{{ errDetail }}</p>
      </div>

      <!-- 提取码校验 -->
      <template v-else-if="state === 'password'">
        <h2 class="sh-title">请输入提取码</h2>
        <p class="muted">
          {{ info.creator ? `「${info.creator}」` : '好友' }}向你分享了文件<template
            v-if="info.expire_at"
          >
            · 有效期至 {{ fmtSec(info.expire_at) }}</template
          >
        </p>
        <div class="sh-pw-row">
          <input
            ref="pwInput"
            v-model="password"
            class="sh-pw num"
            maxlength="4"
            placeholder="4 位提取码"
            autocomplete="off"
            @keydown.enter.prevent="doVerify"
          />
          <button
            type="button"
            class="sh-btn primary"
            :disabled="verifying || !password.trim()"
            @click="doVerify"
          >
            {{ verifying ? '校验中…' : '提取文件' }}
          </button>
        </div>
        <p v-if="pwErr" class="sh-err">{{ pwErr }}</p>
      </template>

      <!-- 分享内容 -->
      <template v-else-if="state === 'content'">
        <div class="sh-ctx">
          <div>
            <h2 class="sh-title" style="margin: 0">
              {{ info.creator ? `${info.creator} 的分享` : '分享内容' }}
            </h2>
            <p class="muted sh-meta">
              <template v-if="info.expire_at">有效期至 {{ fmtSec(info.expire_at) }}</template>
              <template v-else>永久有效</template>
            </p>
          </div>
          <div class="spacer"></div>
          <button type="button" class="sh-btn" :disabled="loading" @click="reload">刷新</button>
        </div>

        <!-- 面包屑：根 = 分享项集合 -->
        <div class="sh-crumb">
          <a href="javascript:;" @click="navTo('')">分享内容</a>
          <template v-for="seg in crumbs" :key="seg.rel">
            <span class="sh-sep">/</span>
            <a v-if="seg.rel !== viewRel" href="javascript:;" @click="navTo(seg.rel)">{{
              seg.name
            }}</a>
            <span v-else class="sh-crumb-cur">{{ seg.name }}</span>
          </template>
        </div>

        <div v-if="loading" class="sh-center muted">正在加载…</div>
        <table v-else class="sh-table">
          <thead>
            <tr>
              <th>名称</th>
              <th class="sh-col-size num">大小</th>
              <th class="sh-col-time num">修改时间</th>
              <th class="sh-col-op">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="it in rows" :key="it.rel">
              <td>
                <div class="sh-name" @click="it.is_dir && navTo(it.rel)">
                  <span class="sh-ic" :class="it.is_dir ? 'dir' : 'file'">{{
                    it.is_dir ? '夹' : '文'
                  }}</span>
                  <span class="sh-nm" :title="it.name">{{ it.name }}</span>
                </div>
              </td>
              <td class="sh-col-size num muted">{{ it.is_dir ? '—' : fmtSize(it.size) }}</td>
              <td class="sh-col-time num muted">{{ fmtSec(it.modified) }}</td>
              <td class="sh-col-op">
                <button v-if="it.is_dir" type="button" class="sh-link" @click="navTo(it.rel)">
                  进入
                </button>
                <button
                  v-else
                  type="button"
                  class="sh-link"
                  :disabled="downloading === it.rel"
                  @click="doDownload(it)"
                >
                  {{ downloading === it.rel ? '下载中…' : '下载' }}
                </button>
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="4" class="sh-empty muted">该目录为空</td>
            </tr>
          </tbody>
        </table>
      </template>
    </div>

    <p class="sh-foot muted">Shade 壹匣 · 检修一班</p>
  </div>
</template>

<script setup>
// 公开分享页（免登录）：/share/:id 对应 Go /api/v1/netdisk/public/share/:id 的
// 信息 / verify / list / download 四口。分享 id 兼容三种进入方式：
// 路由参数 /share/:id、查询 ?id= 或 ?s=、以及旧分享链接 hash 形态 #/s/<id>。
// 内容一律来自服务端，无演示数据；提取码票据（2h）留存在 sessionStorage。
import { computed, nextTick, onMounted, reactive, ref } from 'vue';
import { useRoute } from 'vue-router';
import {
  shareInfo,
  shareVerify,
  shareList,
  shareDownload,
  saveBlob,
  getTicket,
  clearTicket,
  normalizeShareId,
} from '../features/share/api.js';

const route = useRoute();

const state = ref('loading'); // loading / error / password / content
const errMsg = ref('');
const errDetail = ref('');
const shareId = ref('');
const info = reactive({ creator: '', expire_at: '', created_at: '' });

const password = ref('');
const pwErr = ref('');
const verifying = ref(false);
const pwInput = ref(null);

const loading = ref(false);
const downloading = ref('');
const viewRel = ref(''); // 当前浏览的分享范围内相对路径（'' = 分享项首层）
const items = ref([]); // 当前层条目（含 rel）

// ---- 工具 ----
function fmtSize(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return Math.round(n / 1024) + ' KB';
  return `${n || 0} B`;
}
function fmtSec(d) {
  if (!d) return '—';
  const dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  if (Number.isNaN(dt.getTime())) return String(d);
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}

// baseName 路径末段。
function baseName(rel) {
  const parts = String(rel || '')
    .split('/')
    .filter(Boolean);
  return parts.length ? parts[parts.length - 1] : rel;
}

// rows 当前层条目（附 rel；子层 rel = 当前目录 rel + 名称）。
const rows = computed(() =>
  (items.value || []).map((it) => {
    const rel = it.rel || (viewRel.value ? `${viewRel.value}/${it.name}` : it.name);
    return { ...it, rel };
  })
);

// crumbs 面包屑（按当前层 rel 拆段；首层段取分享项名）。
const crumbs = computed(() => {
  const segs = [];
  if (!viewRel.value) return segs;
  const parts = viewRel.value.split('/').filter(Boolean);
  let acc = '';
  parts.forEach((p) => {
    acc = acc ? `${acc}/${p}` : p;
    segs.push({ name: p, rel: acc });
  });
  return segs;
});

// resolveShareId 兼容路由参数 / 查询 / 旧 hash 形态。
function resolveShareId() {
  const fromParams = normalizeShareId(route.params && route.params.id);
  if (fromParams) return fromParams;
  const fromQuery = normalizeShareId((route.query && (route.query.id || route.query.s)) || '');
  if (fromQuery) return fromQuery;
  const m = /#\/s\/([A-Za-z0-9]+)/.exec(location.hash || '');
  return m ? normalizeShareId(m[1]) : '';
}

// ---- 载入与校验 ----
function showError(msg, detail) {
  state.value = 'error';
  errMsg.value = msg || '分享不可用';
  errDetail.value = detail || '';
}

function requirePassword(msg) {
  state.value = 'password';
  pwErr.value = msg || '';
  nextTick(() => pwInput.value && pwInput.value.focus());
}

async function loadRoot() {
  loading.value = true;
  try {
    const d = await shareList(shareId.value, '/');
    items.value = (d && d.items) || [];
    viewRel.value = '';
    state.value = 'content';
  } catch (e) {
    if (e.code === 40311 || e.status === 403) {
      clearTicket(shareId.value);
      requirePassword(e.code === 42901 ? e.message : '请重新输入提取码');
    } else if (e.code === 41010 || e.code === 40310 || e.code === 40400) {
      showError(e.message);
    } else {
      showError('分享内容加载失败', e.message || '');
    }
  } finally {
    loading.value = false;
  }
}

async function doVerify() {
  const pw = password.value.trim();
  if (!pw) return;
  verifying.value = true;
  pwErr.value = '';
  try {
    const d = await shareVerify(shareId.value, pw);
    state.value = 'content';
    items.value = (d && d.items) || [];
    viewRel.value = '';
  } catch (e) {
    if (e.code === 42901) {
      showError(e.message, '请稍后再试');
    } else {
      pwErr.value = e.message || '提取码错误';
    }
  } finally {
    verifying.value = false;
  }
}

async function navTo(rel) {
  if (state.value !== 'content') return;
  if (!rel) {
    // 回首层：优先 list('/')，票据失效时回提取码框
    await loadRoot();
    return;
  }
  loading.value = true;
  try {
    const d = await shareList(shareId.value, rel);
    items.value = (d && d.items) || [];
    viewRel.value = (d && d.path && d.path !== '/' ? d.path : rel) || rel;
  } catch (e) {
    if (e.code === 40311 || e.status === 403) {
      clearTicket(shareId.value);
      requirePassword('请重新输入提取码');
    } else if (e.code === 40312) {
      pwErr.value = '';
      showError(e.message || '路径超出分享范围');
    } else {
      showError('目录加载失败', e.message || '');
    }
  } finally {
    loading.value = false;
  }
}

function reload() {
  navTo(viewRel.value);
}

async function doDownload(it) {
  downloading.value = it.rel;
  try {
    const res = await shareDownload(shareId.value, it.rel);
    await saveBlob(res, it.name);
  } catch (e) {
    if (e.code === 40311 || e.status === 403) {
      clearTicket(shareId.value);
      requirePassword('请重新输入提取码');
    } else {
      // 用原生 alert 太糙，就地展示在页脚下方不可靠——统一走标题错误态不合适，改为简单提示
      window.alert(e.message || '下载失败');
    }
  } finally {
    downloading.value = '';
  }
}

// ---- 初始化 ----
onMounted(async () => {
  shareId.value = resolveShareId();
  if (!shareId.value) {
    showError('分享链接不完整', '请从分享者提供的完整链接进入');
    return;
  }
  try {
    const d = await shareInfo(shareId.value);
    info.creator = (d && d.creator) || '';
    info.expire_at = (d && d.expire_at) || '';
    info.created_at = (d && d.created_at) || '';
  } catch (e) {
    showError(e.message || '分享不存在或已删除', '');
    return;
  }
  if (getTicket(shareId.value)) {
    await loadRoot();
  } else {
    requirePassword('');
  }
});
</script>

<style scoped>
/* 公开分享页（bare 无壳布局）：独立自足样式，沿用全局 token（styles.css 注入） */
.sh-page {
  min-height: 100vh;
  background: var(--tk-bg, #f5f7fa);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 24px 16px 40px;
}
.sh-head {
  width: 100%;
  max-width: 860px;
  display: flex;
  align-items: center;
  margin-bottom: 16px;
}
.sh-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-ink, #1d2129);
}
.sh-mark {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: var(--tk-brand, #0e3da8);
  color: #fff;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
}
.sh-card {
  width: 100%;
  max-width: 860px;
  background: #fff;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-card, 0 2px 8px rgba(0, 0, 0, 0.04));
  padding: 28px;
}
.sh-title {
  margin: 0 0 8px;
  font-size: 18px;
  font-weight: 600;
  color: var(--tk-ink, #1d2129);
}
.sh-center {
  text-align: center;
  padding: 36px 0;
}
.sh-err-ic {
  width: 44px;
  height: 44px;
  margin: 0 auto 12px;
  border-radius: 50%;
  background: var(--tk-red-soft, #ffece8);
  color: var(--tk-red, #f53f3f);
  font-size: 22px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.sh-pw-row {
  display: flex;
  gap: 10px;
  margin-top: 16px;
}
.sh-pw {
  width: 180px;
  height: 38px;
  padding: 0 12px;
  border: 1px solid var(--tk-line, #e5e6eb);
  border-radius: 8px;
  font-size: 16px;
  letter-spacing: 4px;
  color: var(--tk-ink, #1d2129);
  outline: none;
}
.sh-pw:focus {
  border-color: var(--tk-brand, #0e3da8);
}
.sh-btn {
  height: 38px;
  padding: 0 18px;
  border-radius: 8px;
  border: 1px solid var(--tk-line, #e5e6eb);
  background: #fff;
  color: var(--tk-sub, #4e5969);
  font-size: 13px;
  cursor: pointer;
}
.sh-btn.primary {
  background: var(--tk-brand, #0e3da8);
  border-color: transparent;
  color: #fff;
  font-weight: 600;
}
.sh-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.sh-err {
  margin-top: 10px;
  color: var(--tk-red, #f53f3f);
  font-size: 13px;
}
.sh-ctx {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
}
.sh-ctx .spacer {
  flex: 1;
}
.sh-meta {
  margin: 4px 0 0;
  font-size: 12px;
}
.sh-crumb {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin: 12px 0;
  font-size: 13px;
}
.sh-crumb a {
  color: var(--tk-brand, #0e3da8);
  text-decoration: none;
}
.sh-sep {
  color: var(--tk-dash, #c9cdd4);
}
.sh-crumb-cur {
  color: var(--tk-ink, #1d2129);
  font-weight: 600;
}
.sh-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.sh-table th {
  text-align: left;
  font-weight: 600;
  color: var(--tk-sub, #4e5969);
  padding: 8px 10px;
  border-bottom: 1px solid var(--tk-line, #e5e6eb);
}
.sh-table td {
  padding: 10px;
  border-bottom: 1px solid var(--tk-line, #e5e6eb);
}
.sh-col-size {
  width: 90px;
}
.sh-col-time {
  width: 130px;
}
.sh-col-op {
  width: 90px;
}
.sh-name {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.sh-ic {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  flex-shrink: 0;
}
.sh-ic.dir {
  background: var(--tk-blue-soft, #e8f0ff);
  color: var(--tk-brand, #0e3da8);
}
.sh-ic.file {
  background: var(--tk-fill, #f7f8fa);
  color: var(--tk-sub, #4e5969);
}
.sh-nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sh-link {
  border: none;
  background: none;
  color: var(--tk-brand, #0e3da8);
  font-size: 13px;
  cursor: pointer;
  padding: 0;
}
.sh-link:disabled {
  color: var(--tk-ph, #86909c);
  cursor: not-allowed;
}
.sh-empty {
  text-align: center;
  padding: 28px 0;
}
.sh-foot {
  margin-top: 16px;
  font-size: 12px;
}
.muted {
  color: var(--tk-ph, #86909c);
}
.num {
  font-variant-numeric: tabular-nums;
}
</style>
