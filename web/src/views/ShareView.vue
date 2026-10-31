<template>
  <div class="sh-root">
    <!-- 顶部品牌行（简洁无导航，同旧 share.html） -->
    <header class="sh-brand">
      <span class="logo-mark"><img src="/favicon.svg" alt="Shade 壹匣" /></span>
      <b>Shade <i>壹匣</i></b>
      <span class="sep">·</span>
      <span class="sub">团队网盘分享</span>
    </header>

    <main class="sh-main">
      <!-- 链接无效 / 分享不可用 -->
      <div v-if="state === 'error'" class="card sh-state">
        <span class="ic" v-html="ic(errIcon, 44)"></span>
        <h2>{{ errMsg || '分享不可用' }}</h2>
        <p>{{ errDetail || '如有疑问请联系分享人重新生成链接。' }}</p>
      </div>

      <!-- 加载中 -->
      <div v-else-if="state === 'loading'" class="card sh-state">
        <span class="spin" style="margin: 0 auto"></span>
        <p>加载中…</p>
      </div>

      <!-- 提取码表单 -->
      <div v-else-if="state === 'password'" class="card sh-card">
        <div class="sh-owner">
          <span class="sh-av">{{ avatarChar }}</span>
          <div>
            <h1>
              <b>{{ info.creator }}</b> 的分享
            </h1>
            <div class="sh-meta">{{ expireText }}</div>
          </div>
        </div>
        <div class="pw-row">
          <input
            ref="pwInput"
            v-model="password"
            class="pw-in"
            maxlength="4"
            placeholder="请输入 4 位提取码"
            autocomplete="off"
            @keydown.enter.prevent="doVerify"
          />
          <button type="button" class="btn btn-primary" :disabled="verifying" @click="doVerify">
            {{ verifying ? '验证中…' : '提取文件' }}
          </button>
        </div>
        <div class="pw-err">{{ pwErr }}</div>
        <div class="pw-tip">提取码为 4 位字母或数字（不区分大小写），由分享人提供。</div>
      </div>

      <!-- 分享内容 -->
      <template v-else-if="state === 'content'">
        <div class="card sh-card sh-card-sm">
          <div class="sh-owner">
            <span class="sh-av">{{ avatarChar }}</span>
            <div>
              <h1>
                <b>{{ info.creator }}</b> 的分享
              </h1>
              <div class="sh-meta">{{ expireText }}</div>
            </div>
          </div>
        </div>

        <div class="card sh-list">
          <div class="sh-crumb">
            <a href="javascript:;" @click="openDir('/')">分享内容</a>
            <template v-for="seg in crumbs" :key="seg.rel">
              <em>/</em>
              <a v-if="seg.rel !== viewRel" href="javascript:;" @click="openDir(seg.rel)">{{
                seg.name
              }}</a>
              <b v-else>{{ seg.name }}</b>
            </template>
            <span class="cnt">共 {{ rows.length }} 项</span>
          </div>

          <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
          <div v-else-if="!rows.length" class="empty">
            <span class="ic" v-html="ic('folder', 44)"></span>
            <p>此文件夹为空</p>
          </div>
          <template v-else>
            <div
              v-for="it in rows"
              :key="it.rel"
              class="f-row"
              :title="it.name"
              @click="it.is_dir ? openDir(it.rel) : openFile(it)"
            >
              <span class="ft-ic" :style="{ width: '30px', height: '30px' }">
                <img
                  class="ft-img"
                  :src="fileIconUrl(it.name, it.is_dir)"
                  width="30"
                  height="30"
                  draggable="false"
                  alt=""
                  @error="onFtError"
                />
                <span class="ft-fb" hidden>
                  <span v-if="it.is_dir" class="ic" v-html="ic('folder', 30)"></span>
                  <span v-else class="ft-fb-lb" :style="ftLbStyle(it.name, 30)">{{
                    typeLabel(it.name)
                  }}</span>
                </span>
              </span>
              <div class="f-mid">
                <span class="f-nm">{{ it.name }}</span>
                <span class="f-sub">{{ rowSub(it) }}</span>
              </div>
              <button
                v-if="!it.is_dir"
                type="button"
                class="btn btn-sm f-dl"
                :disabled="downloading === it.rel"
                @click.stop="doDownload(it)"
              >
                {{ downloading === it.rel ? '下载中…' : '下载' }}
              </button>
            </div>
          </template>
        </div>
      </template>
    </main>

    <!-- 视频 Lightbox（overlay-motion 进出场：遮罩淡入 + 面板上浮/缩放，退出播完再移除 DOM） -->
    <div v-if="mediaRendered" class="mask ov-mask" :class="mediaPhase" @click.self="closeMedia">
      <div class="modal ov-panel" style="width: 920px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ media.name }}</span>
            <button type="button" class="m-x" @click="closeMedia">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>
          <video class="lb-media" :src="media.url" controls autoplay></video>
        </div>
      </div>
    </div>

    <!-- 图片全屏查看器（←→ 循环 / 滚轮缩放 / 拖拽平移 / 单击 适应↔原图 / Esc；overlay-motion 进出场） -->
    <div v-if="vwrRendered" class="vwr ov-mask" :class="vwrPhase">
      <div class="vwr-top">
        <span class="vwr-name">{{ vwrName }}</span>
        <span class="vwr-idx">{{ vwr.idx + 1 }} / {{ vwr.imgs.length }}</span>
        <span class="vwr-x" title="关闭（Esc）" @click="closeViewer">
          <span class="ic" v-html="ic('close', 16)"></span>
        </span>
      </div>
      <div ref="vwrStageEl" class="vwr-stage ov-panel" @wheel.prevent="onVwrWheel">
        <img
          ref="vwrImgEl"
          class="vwr-img"
          :class="{ free: vwr.mode === 'free', dragging: vwrDragging }"
          :src="vwrUrl"
          draggable="false"
          alt=""
          :style="vwrTransform"
          @load="onVwrLoad"
          @pointerdown="onVwrDown"
        />
      </div>
      <div class="vwr-bar">
        <button type="button" :disabled="vwr.imgs.length < 2" @click="vwrNav(-1)">
          <span class="ic" v-html="ic('chevron-left', 16)"></span>
        </button>
        <button type="button" @click="vwrToggleFit">
          {{ vwr.mode === 'fit' ? '原图' : '适应' }}
        </button>
        <button type="button" @click="doDownload(vwrCurrentItem)">
          <span class="ic" v-html="ic('download', 16)"></span>
        </button>
        <button type="button" :disabled="vwr.imgs.length < 2" @click="vwrNav(1)">
          <span class="ic" v-html="ic('chevron-right', 16)"></span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
// 公开分享页（免登录）：对照 server/public/share.html 还原——品牌行 / 提取码卡（头像 + 创建人 +
// 创建与有效期）/ 文件行列表（真实类型图标 + 名称 + 大小·时间子行）/ 面包屑（共 N 项）/
// 图片全屏查看器 + 视频 Lightbox。内容一律来自服务端，无演示数据；
// 提取码票据（2h）留存在 sessionStorage（features/share/api.js）。
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import {
  shareInfo,
  shareVerify,
  shareList,
  shareFileUrl,
  getTicket,
  clearTicket,
  normalizeShareId,
} from '../features/share/api.js';
import {
  extOf,
  typeLabel,
  ftLbStyle,
  fileIconUrl,
  onFtError,
} from '../features/netdisk/fileicon.js';
import { useOverlayMotion } from '../components/overlay-motion.js';
import { bodyScrollLock } from '../components/scroll-lock.js';
import { createGeneration } from '../features/core/async.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../components/overlay-motion.css';

const route = useRoute();

// shareGen 分享初始化 / 目录下钻 / 提取码校验的请求代际：同一组件内切换链接或快速下钻时，
// 旧响应不得覆盖新分享内容。
const shareGen = createGeneration();
// viewerLockRelease 全屏图片查看器的滚动锁票据（引用计数，卸载/关闭只释放自己）。
let viewerLockRelease = null;
let mediaLockRelease = null;
function acquireViewerLock() {
  if (!viewerLockRelease) viewerLockRelease = bodyScrollLock.acquire();
}
function releaseViewerLock() {
  if (viewerLockRelease) {
    viewerLockRelease();
    viewerLockRelease = null;
  }
}
function acquireMediaLock() {
  if (!mediaLockRelease) mediaLockRelease = bodyScrollLock.acquire();
}
function releaseMediaLock() {
  if (mediaLockRelease) {
    mediaLockRelease();
    mediaLockRelease = null;
  }
}

/* ================= 图标（旧 assets/icons.js 同一套 SVG 路径） ================= */
const ICONS = {
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  'error-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  'chevron-left': '<path d="M14.5 6.5L9 12l5.5 5.5"/>',
  folder:
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  link: '<path d="M10 14a5 5 0 0 0 7 .5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 10a5 5 0 0 0-7-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
};
function ic(name, size) {
  const body = ICONS[name] || ICONS.folder;
  return `<svg width="${size || 24}" height="${size || 24}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
}

/* ================= 文件类型（旧 share.html 同一映射） ================= */
// 类型图标映射与渲染收敛到 features/netdisk/fileicon.js（真实类型 PNG + 文件夹线性图标/字母块回退），
// extOf / typeLabel / ftLbStyle / fileIconUrl / onFtError 自该模块引入
const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg'];
const VIDEO_EXTS = ['mp4', 'webm', 'mov', 'm4v'];
function isImg(name) {
  return IMG_EXTS.includes(extOf(name));
}
function isVideo(name) {
  return VIDEO_EXTS.includes(extOf(name));
}

/* ================= 工具 ================= */
function fmtSize(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}
function toDate(d) {
  if (!d) return null;
  let dt = new Date(d);
  if (Number.isNaN(dt.getTime())) dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  return Number.isNaN(dt.getTime()) ? null : dt;
}
function fmtDate(d, withTime) {
  const dt = toDate(d);
  if (!dt) return '—';
  const p = (n) => String(n).padStart(2, '0');
  let s = `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
  if (withTime) s += ` ${p(dt.getHours())}:${p(dt.getMinutes())}`;
  return s;
}

/* ================= 状态 ================= */
const state = ref('loading'); // loading / error / password / content
const errMsg = ref('');
const errDetail = ref('');
const errIcon = ref('error-circle');
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

const avatarChar = computed(() => String(info.creator || '友').charAt(0));
const expireText = computed(() => {
  if (!info.created_at && !info.expire_at) return '';
  return `创建于 ${fmtDate(info.created_at)} · 有效期至 ${
    info.expire_at ? fmtDate(info.expire_at, true) : '永久'
  }`;
});

// rows 当前层条目（附 rel；子层 rel = 当前目录 rel + 名称）。
const rows = computed(() =>
  (items.value || []).map((it) => {
    const rel = it.rel || (viewRel.value ? `${viewRel.value}/${it.name}` : it.name);
    return { ...it, rel };
  })
);
// crumbs 面包屑（按当前层 rel 拆段）。
const crumbs = computed(() => {
  const segs = [];
  if (!viewRel.value) return segs;
  let acc = '';
  viewRel.value
    .split('/')
    .filter(Boolean)
    .forEach((p) => {
      acc = acc ? `${acc}/${p}` : p;
      segs.push({ name: p, rel: acc });
    });
  return segs;
});
function rowSub(it) {
  return it.is_dir
    ? '文件夹'
    : fmtSize(it.size) + (it.modified ? ` · ${fmtDate(it.modified)}` : '');
}

// resolveShareId 兼容路由参数 / 查询 / 旧 hash 形态。
function resolveShareId() {
  const fromParams = normalizeShareId(route.params && route.params.id);
  if (fromParams) return fromParams;
  const fromQuery = normalizeShareId((route.query && (route.query.id || route.query.s)) || '');
  if (fromQuery) return fromQuery;
  const m = /#\/s\/([A-Za-z0-9]+)/.exec(location.hash || '');
  return m ? normalizeShareId(m[1]) : '';
}

/* ================= 载入与校验 ================= */
function showError(msg, detail, icon) {
  state.value = 'error';
  errMsg.value = msg || '分享不可用';
  errDetail.value = detail || '如有疑问请联系分享人重新生成链接。';
  errIcon.value = icon || 'error-circle';
}
function requirePassword(msg) {
  state.value = 'password';
  pwErr.value = msg || '';
  nextTick(() => pwInput.value && pwInput.value.focus());
}

async function loadRoot() {
  const gen = shareGen.next();
  const id = shareId.value;
  loading.value = true;
  try {
    const d = await shareList(id, '/');
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    items.value = (d && d.items) || [];
    viewRel.value = '';
    state.value = 'content';
  } catch (e) {
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    if (e.code === 40311 || e.status === 403) {
      clearTicket(id);
      requirePassword(e.code === 42901 ? e.message : '访问票据已过期，请重新输入提取码');
    } else if (e.code === 40310) {
      showError('该分享已被创建者停用');
    } else if (e.code === 41010) {
      showError('该分享已过期');
    } else if (e.code === 40400) {
      showError('分享不存在或已删除');
    } else {
      showError('分享内容加载失败', e.message || '');
    }
  } finally {
    if (shareGen.isCurrent(gen) && shareId.value === id) loading.value = false;
  }
}

async function doVerify() {
  const pw = password.value.trim().toUpperCase();
  if (!/^[A-Z0-9]{4}$/.test(pw)) {
    pwErr.value = '请输入 4 位提取码（字母或数字）';
    return;
  }
  const gen = shareGen.next();
  const id = shareId.value;
  verifying.value = true;
  pwErr.value = '';
  try {
    const d = await shareVerify(id, pw);
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    state.value = 'content';
    items.value = (d && d.items) || [];
    viewRel.value = '';
  } catch (e) {
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    if (e.code === 40310) showError('该分享已被创建者停用');
    else if (e.code === 41010) showError('该分享已过期');
    else if (e.code === 40400) showError('分享不存在或已删除');
    else if (e.code === 42901)
      pwErr.value = e.message; // 尝试过多 原样提示
    else pwErr.value = e.message || '提取码错误';
  } finally {
    if (shareGen.isCurrent(gen) && shareId.value === id) verifying.value = false;
  }
}

// 目录下钻（path 为分享范围内相对路径，'/' 为首层）
async function openDir(rel) {
  if (state.value !== 'content') return;
  const gen = shareGen.next();
  const id = shareId.value;
  loading.value = true;
  try {
    const d = await shareList(id, rel || '/');
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    items.value = (d && d.items) || [];
    viewRel.value = rel === '/' ? '' : (d && d.path && d.path !== '/' ? d.path : rel) || rel || '';
  } catch (e) {
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    if (e.code === 40311 || e.status === 403) {
      clearTicket(id);
      requirePassword('访问票据已过期，请重新输入提取码');
    } else if (e.code === 40312) {
      showError(e.message || '路径超出分享范围');
    } else {
      showError('目录加载失败', e.message || '');
    }
  } finally {
    if (shareGen.isCurrent(gen) && shareId.value === id) loading.value = false;
  }
}

// 文件：图片走网页内全屏查看器，视频内联 Lightbox 播放，其余直接下载
function openFile(it) {
  if (isImg(it.name)) {
    openViewer(it);
    return;
  }
  if (isVideo(it.name)) {
    previewMedia(it);
    return;
  }
  doDownload(it);
}

// 原生直链下载：临时 <a download> 触发浏览器保存，不经内存 Blob 中转（大文件即点即下，
// 浏览器下载器出进度）；name 仅作回退，缺省/为空时取响应 Content-Disposition 文件名
function triggerDownload(url, name) {
  const a = document.createElement('a');
  a.href = url;
  a.download = name || '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}
// 下载：先 Range 预检判 JSON 信封（ticket 失效 40311 回引导重验），通过再走原生直链落地，不再整读内存 Blob。
// 取舍：后端 pipeDownload 不支持 Range 会回 200 全流，预检取到响应头即取消 body，开销可忽略；
// 预检通过到直跳之间票据恰好失效等极小概率场景会存下 JSON 文件而非回引导，属接受范围
async function doDownload(it) {
  if (!it) return;
  downloading.value = it.rel;
  try {
    const url = shareFileUrl(shareId.value, it.rel);
    const res = await fetch(url, { headers: { Range: 'bytes=0-0' } });
    const ct = res.headers.get('Content-Type') || '';
    if (!res.ok || /json/i.test(ct)) {
      const j = await res.json().catch(() => null);
      if (j && j.code === 40311) {
        clearTicket(shareId.value);
        requirePassword('访问票据已过期，请重新输入提取码');
        return;
      }
      if (j && j.code === 40310) {
        showError('该分享已被创建者停用');
        return;
      }
      if (j && j.code === 41010) {
        showError('该分享已过期');
        return;
      }
      if (j && j.code === 40400) {
        showError('分享不存在或已删除');
        return;
      }
      throw new Error((j && j.message) || `HTTP ${res.status}`);
    }
    if (res.body) res.body.cancel().catch(() => {});
    triggerDownload(url, it.name);
  } catch (e) {
    window.alert(`下载失败：${e.message}`);
  } finally {
    downloading.value = '';
  }
}

/* ================= 视频 Lightbox / 图片全屏查看器（同旧 dlUrl 直引） ================= */
const media = reactive({ open: false, name: '', url: '' });
// 弹层进出场（overlay-motion）：渲染存在与阶段类由显隐源驱动，reduced-motion 自动降级。
const mediaOpen = computed(() => media.open);
const { rendered: mediaRendered, phaseClass: mediaPhase } = useOverlayMotion(mediaOpen);
function previewMedia(it) {
  media.url = shareFileUrl(shareId.value, it.rel);
  media.name = it.name;
  media.open = true;
  acquireMediaLock();
}
function closeMedia() {
  media.open = false;
  media.url = '';
  releaseMediaLock();
}

const vwr = reactive({ open: false, imgs: [], idx: 0, mode: 'fit', scale: 1, tx: 0, ty: 0 });
// 查看器同口径进出场（遮罩淡入 + 内容上浮/缩放，退出播完再移除 DOM）。
const vwrOpen = computed(() => vwr.open);
const { rendered: vwrRendered, phaseClass: vwrPhase } = useOverlayMotion(vwrOpen);
const vwrStageEl = ref(null);
const vwrImgEl = ref(null);
const vwrDragging = ref(false);
const vwrCurrentItem = computed(() => (vwr.imgs[vwr.idx] ? vwr.imgs[vwr.idx].item : null));
const vwrName = computed(() => (vwrCurrentItem.value ? vwrCurrentItem.value.name : ''));
const vwrUrl = computed(() => (vwr.imgs[vwr.idx] ? vwr.imgs[vwr.idx].url : ''));
const vwrTransform = computed(
  () => `transform: translate(-50%, -50%) translate(${vwr.tx}px, ${vwr.ty}px) scale(${vwr.scale})`
);

async function openViewer(it) {
  const imgs = rows.value.filter((x) => !x.is_dir && isImg(x.name));
  const idx = imgs.findIndex((x) => x.rel === it.rel);
  if (idx < 0) return;
  vwr.imgs = imgs.map((x) => ({ item: x, url: shareFileUrl(shareId.value, x.rel) }));
  vwr.idx = idx;
  vwr.open = true;
  vwr.mode = 'fit';
  vwr.scale = 1;
  vwr.tx = 0;
  vwr.ty = 0;
  acquireViewerLock();
  window.removeEventListener('keydown', onVwrKey, true);
  window.removeEventListener('resize', onVwrResize);
  window.addEventListener('keydown', onVwrKey, true);
  window.addEventListener('resize', onVwrResize);
  await ensureVwrUrl();
}
async function ensureVwrUrl() {
  const cur = vwr.imgs[vwr.idx];
  if (cur && !cur.url) cur.url = shareFileUrl(shareId.value, cur.item.rel);
}
function vwrShow() {
  vwr.mode = 'fit';
  vwr.tx = 0;
  vwr.ty = 0;
  vwr.scale = 1;
  ensureVwrUrl();
  nextTick(() => {
    const img = vwrImgEl.value;
    if (img && img.complete && img.naturalWidth) vwrFit();
  });
}
function vwrNav(d) {
  if (vwr.imgs.length < 2) return;
  vwr.idx = (vwr.idx + d + vwr.imgs.length) % vwr.imgs.length;
  vwrShow();
}
function vwrFit() {
  const img = vwrImgEl.value;
  const stage = vwrStageEl.value;
  if (!img || !stage) return;
  const natW = img.naturalWidth;
  const natH = img.naturalHeight;
  if (!natW || !natH) return;
  const r = stage.getBoundingClientRect();
  vwr.scale = Math.min(8, Math.max(0.1, Math.min(r.width / natW, r.height / natH)));
  vwr.tx = 0;
  vwr.ty = 0;
  vwr.mode = 'fit';
}
function vwrToggleFit() {
  if (vwr.mode === 'fit') {
    vwr.scale = 1;
    vwr.tx = 0;
    vwr.ty = 0;
    vwr.mode = 'free';
  } else vwrFit();
}
function onVwrLoad() {
  if (vwr.mode === 'fit') vwrFit();
}
function onVwrResize() {
  if (vwr.open && vwr.mode === 'fit') vwrFit();
}
function onVwrWheel(e) {
  const ns = Math.min(8, Math.max(0.1, vwr.scale * (e.deltaY < 0 ? 1.15 : 1 / 1.15)));
  if (ns === vwr.scale) return;
  const stage = vwrStageEl.value;
  const r = stage.getBoundingClientRect();
  const px = e.clientX - r.left - r.width / 2;
  const py = e.clientY - r.top - r.height / 2;
  vwr.tx = px - (px - vwr.tx) * (ns / vwr.scale);
  vwr.ty = py - (py - vwr.ty) * (ns / vwr.scale);
  vwr.scale = ns;
  vwr.mode = 'free';
}
let vwrDrag = null;
function onVwrDown(e) {
  if (e.button !== 0) return;
  e.preventDefault();
  vwrDrag = { x: e.clientX, y: e.clientY, moved: false };
  vwrImgEl.value.setPointerCapture(e.pointerId);
  const onMove = (ev) => {
    if (!vwrDrag) return;
    const dx = ev.clientX - vwrDrag.x;
    const dy = ev.clientY - vwrDrag.y;
    if (!vwrDrag.moved && Math.abs(dx) < 4 && Math.abs(dy) < 4) return;
    vwrDrag.moved = true;
    if (vwr.mode !== 'free') return;
    vwrDragging.value = true;
    vwr.tx += dx;
    vwr.ty += dy;
    vwrDrag.x = ev.clientX;
    vwrDrag.y = ev.clientY;
  };
  const onUp = () => {
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', onUp);
    vwrDragging.value = false;
    const wasDrag = vwrDrag && vwrDrag.moved;
    vwrDrag = null;
    if (!wasDrag) vwrToggleFit();
  };
  document.addEventListener('pointermove', onMove);
  document.addEventListener('pointerup', onUp);
  document.addEventListener('pointercancel', onUp);
}
function onVwrKey(e) {
  if (!vwr.open) return;
  if (e.key === 'Escape') {
    e.stopPropagation();
    e.preventDefault();
    closeViewer();
  } else if (e.key === 'ArrowLeft') {
    e.stopPropagation();
    vwrNav(-1);
  } else if (e.key === 'ArrowRight') {
    e.stopPropagation();
    vwrNav(1);
  }
}
function closeViewer() {
  vwr.open = false;
  releaseViewerLock();
  window.removeEventListener('keydown', onVwrKey, true);
  window.removeEventListener('resize', onVwrResize);
}

/* ================= 初始化 ================= */
async function initShare() {
  const gen = shareGen.next();
  state.value = 'loading';
  items.value = [];
  viewRel.value = '';
  shareId.value = resolveShareId();
  if (!shareId.value) {
    showError('链接无效', '分享链接格式不正确，请核对后重新打开。', 'link');
    return;
  }
  const id = shareId.value;
  try {
    const d = await shareInfo(id);
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    info.creator = (d && d.creator) || '';
    info.expire_at = (d && d.expire_at) || '';
    info.created_at = (d && d.created_at) || '';
  } catch (e) {
    if (!shareGen.isCurrent(gen) || shareId.value !== id) return;
    if (e.code === 40310) showError('该分享已被创建者停用');
    else if (e.code === 41010) showError('该分享已过期');
    else if (e.code === 40400) showError('分享不存在或已删除');
    else showError(e.message || '分享不存在或已删除');
    return;
  }
  if (getTicket(id)) {
    await loadRoot();
  } else {
    requirePassword('');
  }
}

// 同一组件内切换分享链接时也必须重新初始化，不能保留上一个链接的状态。
watch(() => route.fullPath, initShare, { immediate: true });

onBeforeUnmount(() => {
  shareGen.invalidate();
  releaseViewerLock();
  releaseMediaLock();
  window.removeEventListener('keydown', onVwrKey, true);
  window.removeEventListener('resize', onVwrResize);
});
</script>

<style scoped>
/* 公开分享页样式：对照 server/public/share.html 移植（旧 theme.css 变量映射到 --tk-* token） */
.sh-root {
  --card: #fff;
  --navy: var(--tk-ink);
  --orange: var(--tk-brand);
  --orange-soft: var(--tk-blue-soft);
  --red: var(--tk-red);
  --line: var(--tk-line);
  --sub: var(--tk-sub);
  --ph: var(--tk-ph);
  --dash: var(--tk-dash);
  --fill: var(--tk-fill);
  --paper: var(--tk-bg);
  --on-brand: #fff;
  --inverse-text: #fff;
  --shadow-card: var(--tk-shadow-card);
  --shadow-hover: var(--tk-shadow-hover);
  --ease: var(--tk-ease);
  --radius-sm: 8px;
  --overlay: rgba(29, 33, 41, 0.45);
  --brand-glow: rgba(14, 61, 168, 0.12);
  --danger-soft: var(--tk-red-soft);

  min-height: 100vh;
  background: var(--paper);
  display: flex;
  flex-direction: column;
}
.sh-root :deep(*) {
  box-sizing: border-box;
}

/* 顶部品牌行（简洁无导航） */
.sh-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 60px;
  background: var(--card);
  border-bottom: 1px solid var(--line);
  padding: 0 24px;
  flex: none;
}
.sh-brand .logo-mark {
  width: 30px;
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.sh-brand .logo-mark img {
  width: 100%;
  height: 100%;
  display: block;
}
.sh-brand b {
  font-size: 15px;
  letter-spacing: 1px;
}
.sh-brand b i {
  font-style: normal;
  color: var(--orange);
}
.sh-brand .sep {
  color: var(--dash);
}
.sh-brand .sub {
  font-size: 13px;
  color: var(--ph);
  letter-spacing: 1px;
}

.sh-main {
  flex: 1;
  width: 100%;
  max-width: 880px;
  margin: 0 auto;
  padding: 32px 20px 56px;
}

/* 卡片（白卡 12px + 轻阴影，同全局口径） */
.card {
  background: var(--card);
  border-radius: 12px;
  box-shadow: var(--shadow-card);
}

/* 分享信息卡 / 提取码表单 */
.sh-card {
  padding: 28px 30px;
}
.sh-card-sm {
  padding: 18px 22px;
}
.sh-owner {
  display: flex;
  align-items: center;
  gap: 13px;
}
.sh-av {
  width: 42px;
  height: 42px;
  flex: none;
  border-radius: 50%;
  background: var(--orange);
  color: var(--on-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  font-weight: 700;
}
.sh-owner h1 {
  margin: 0;
  font-size: 17px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.sh-owner h1 b {
  color: var(--orange);
}
.sh-meta {
  margin-top: 3px;
  font-size: 11.5px;
  color: var(--ph);
  letter-spacing: 0.5px;
}
.pw-row {
  margin-top: 20px;
  display: flex;
  gap: 10px;
}
.pw-in {
  flex: 1;
  min-width: 0;
  height: 44px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  padding: 0 14px;
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 8px;
  color: var(--navy);
  text-transform: uppercase;
  outline: none;
  font-variant-numeric: tabular-nums;
  font-family: inherit;
  transition:
    border-color 0.15s var(--ease),
    box-shadow 0.15s var(--ease);
}
.pw-in::placeholder {
  color: var(--ph);
  font-weight: 400;
  letter-spacing: 2px;
  font-size: 13px;
}
.pw-in:focus {
  border-color: var(--orange);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
.pw-row .btn {
  height: 44px;
  flex: none;
}
.pw-err {
  margin-top: 10px;
  font-size: 12px;
  color: var(--red);
  letter-spacing: 0.5px;
  min-height: 16px;
}
.pw-tip {
  margin-top: 4px;
  font-size: 12px;
  color: var(--ph);
  line-height: 1.7;
}

/* 文件列表卡 */
.sh-list {
  margin-top: 18px;
  overflow: hidden;
}
.sh-crumb {
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
  padding: 13px 18px;
  border-bottom: 1px solid var(--line);
  font-size: 13px;
}
.sh-crumb a {
  color: var(--orange);
  font-weight: 600;
  cursor: pointer;
  text-decoration: none;
}
.sh-crumb a:hover {
  filter: brightness(0.85);
}
.sh-crumb em {
  font-style: normal;
  color: var(--ph);
}
.sh-crumb b {
  font-weight: 600;
  word-break: break-all;
}
.sh-crumb .cnt {
  margin-left: auto;
  font-size: 11px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
.f-row {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 9px 18px;
  cursor: pointer;
}
.f-row + .f-row {
  border-top: 1px solid var(--fill);
}
.f-row:hover {
  background: var(--fill);
}
/* 真实文件类型图标（无底块直落）；img 404 由 @error 回退 */
.ft-ic {
  position: relative;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.ft-ic .ft-img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: contain;
}
.ft-ic .ft-fb {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
}
.ft-ic .ft-fb[hidden] {
  display: none;
}
.ft-fb-lb {
  width: 100%;
  height: 100%;
  border-radius: 22%;
  background: var(--orange-soft);
  color: var(--orange);
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.ft-fb .ic {
  color: var(--orange);
  display: inline-flex;
}
.f-mid {
  flex: 1;
  min-width: 0;
}
.f-nm {
  display: block;
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.f-sub {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
.f-dl {
  flex: none;
}

/* 空态/错误卡 */
.sh-state {
  padding: 52px 26px;
  text-align: center;
  color: var(--ph);
}
.sh-state .ic {
  width: 44px;
  height: 44px;
  color: var(--dash);
  margin: 0 auto;
  display: inline-flex;
}
.sh-state h2 {
  margin: 14px 0 0;
  font-size: 16px;
  font-weight: 700;
  color: var(--navy);
  letter-spacing: 1px;
}
.sh-state p {
  margin: 8px 0 0;
  font-size: 12.5px;
  line-height: 1.8;
}

/* 通用按钮（旧 theme.css 同口径） */
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
  font-family: inherit;
  transition:
    border-color 0.15s var(--ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover {
  border-color: var(--orange);
  color: var(--orange);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--orange);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* 空态 / 加载 */
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--ph);
}
.empty .ic {
  color: var(--dash);
  display: inline-flex;
}
.empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
}
.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--line);
  border-top-color: var(--orange);
  animation: rot 1s linear infinite;
}
@keyframes rot {
  to {
    transform: rotate(360deg);
  }
}
.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}

/* ===== 通用弹层（视频 Lightbox；进出场由 overlay-motion 驱动） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: var(--overlay);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--shadow-card);
}
.m-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.m-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
}
.m-x:hover {
  background: var(--navy);
  color: var(--inverse-text);
}
.lb-media {
  display: block;
  margin: 0 auto;
  max-width: 100%;
  max-height: 68vh;
  border-radius: 8px;
  background: var(--fill);
}

/* ===== 图片全屏查看器（深色底为查看器惯例；进出场由 overlay-motion 驱动） ===== */
.vwr {
  position: fixed;
  inset: 0;
  z-index: 2000;
  background: rgba(0, 0, 0, 0.88);
  display: flex;
  flex-direction: column;
}
.vwr-top {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  color: #fff;
}
.vwr-name {
  font-size: 13.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vwr-idx {
  flex: none;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.6);
  font-variant-numeric: tabular-nums;
}
.vwr-x {
  margin-left: auto;
  flex: none;
  width: 34px;
  height: 34px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: rgba(255, 255, 255, 0.8);
  cursor: pointer;
  transition: background 0.15s;
}
.vwr-x:hover {
  background: rgba(255, 255, 255, 0.12);
  color: #fff;
}
.vwr-stage {
  flex: 1;
  min-height: 0;
  position: relative;
  overflow: hidden;
  margin: 0 4vw 4vh;
}
.vwr-img {
  position: absolute;
  left: 50%;
  top: 50%;
  transform-origin: center;
  user-select: none;
  cursor: zoom-in;
  transition: transform 0.15s var(--ease);
}
.vwr-img.free {
  cursor: grab;
}
.vwr-img.free.dragging {
  cursor: grabbing;
  transition: none;
}
.vwr-bar {
  flex: none;
  display: flex;
  justify-content: center;
  gap: 10px;
  padding: 4px 0 18px;
}
.vwr-bar button {
  height: 36px;
  min-width: 36px;
  padding: 0 12px;
  border-radius: 8px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  background: rgba(255, 255, 255, 0.08);
  color: #fff;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s;
}
.vwr-bar button:hover {
  background: rgba(255, 255, 255, 0.18);
}
.vwr-bar button[disabled] {
  opacity: 0.4;
  cursor: default;
}

/* 系统深色：仅切换语义变量（值取 server/public/assets/theme.css 深色 token，不简单反色）；
   浅色结构与圆角不变 */
@media (prefers-color-scheme: dark) {
  .sh-root {
    --paper: #14171d;
    --card: #1d222b;
    --fill: #252b35;
    --navy: #e5e6eb;
    --sub: #a9b1bd;
    --ph: #8a94a3;
    --dash: #626d7d;
    --line: #343c49;
    --orange: #78a6ff;
    --orange-soft: #1b2d4d;
    --on-brand: #101820;
    --inverse-text: #1d222b;
    --red: #ff7a73;
    --danger-soft: #3a1d1d;
    --overlay: rgba(0, 0, 0, 0.62);
    --brand-glow: rgba(120, 166, 255, 0.18);
    --shadow-card: 0 2px 8px rgba(0, 0, 0, 0.24);
    --shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.32);
  }
  .m-scroll {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .m-scroll::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
}

/* 动效降级（同旧 theme.css 全局口径，scoped 内自持）：减少动态偏好下关闭动画与延迟 */
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-iteration-count: 1 !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
