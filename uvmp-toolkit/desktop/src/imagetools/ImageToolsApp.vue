<template>
  <div class="it-shell">
    <TitleBar :api="api.windowApi" title="图片工具 · 内网工具箱" />
    <div class="it-app">
      <header class="it-appbar">
        <div class="it-brand">
          <span class="it-brand-mk"><ITIcon name="image" :size="17" /></span>
          <span class="it-brand-txt"><b>图片工具</b><em>内网工具箱</em></span>
        </div>
        <nav class="it-appnav" role="tablist" aria-label="图片工具导航">
          <button
            id="it-nav-viewer"
            type="button"
            role="tab"
            class="it-navtab"
            :class="{ 'is-on': page === 'viewer' }"
            :aria-selected="page === 'viewer' ? 'true' : 'false'"
            aria-controls="it-main"
            @click="page = 'viewer'"
          >
            图片查看
          </button>
          <button
            id="it-nav-process"
            type="button"
            role="tab"
            class="it-navtab"
            :class="{ 'is-on': page === 'process' }"
            :aria-selected="page === 'process' ? 'true' : 'false'"
            aria-controls="it-main"
            @click="page = 'process'"
          >
            图片处理
          </button>
        </nav>
        <div class="it-appbar-right">
          <button
            type="button"
            class="it-iconbtn"
            :class="{ 'is-on': page === 'settings' }"
            title="设置"
            aria-label="设置"
            @click="openSettings"
          >
            <ITIcon name="setting" :size="17" />
          </button>
        </div>
      </header>

      <ViewerPane
        v-show="page === 'viewer'"
        ref="viewerRef"
        class="it-pane"
        :file="current"
        :index="index"
        :total="files.length"
        :preview-url="previewUrl"
        :state="state"
        :error-code="errorCode"
        @step="loadAt"
        @open-images="openImages"
        @open-folder="openFolder"
        @rescan="rescan"
        @rename="startRename"
        @photo-load="onPhotoLoad"
        @photo-error="onPhotoError"
        @notify="toast"
      >
        <template #inspector>
          <RenamePanel
            v-if="renaming"
            :file="current"
            :phrases="phrases"
            :saving="renamingBusy"
            :error-code="renameErrorCode"
            :error-message="renameErrorMessage"
            @cancel="cancelRename"
            @submit="submitRename"
            @notify="toast"
          />
          <InfoPanel
            v-else-if="current"
            :file="current"
            :index="index"
            :total="files.length"
            :default-status="defaultStatus"
            @close="closeInfo"
            @open-folder="openFolder"
            @rename="startRename"
            @request-assoc="openAssoc"
            @notify="toast"
          />
          <div v-else class="it-ins-empty">
            <ITIcon name="image" :size="20" stroke="1.5" />
            <span>打开一张图片后，这里显示名称、类型、大小、尺寸、路径与修改时间。</span>
          </div>
        </template>
      </ViewerPane>

      <ProcessPane
        v-show="page === 'process'"
        ref="processRef"
        class="it-pane"
        :busy="processBusy"
        @notify="toast"
        @running="onProcessRunning"
      />

      <SettingsPane
        v-if="page === 'settings'"
        class="it-pane"
        :phrases="phrases"
        :default-status="defaultStatus"
        :tab="settingsTab"
        @tab-change="settingsTab = $event"
        @back="page = 'viewer'"
        @saved="onPhrasesSaved"
        @toast="toast"
        @notify="toast"
      />

      <div v-if="toastText" class="it-toast" role="status">{{ toastText }}</div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';
import ITIcon from './components/ITIcon.vue';
import TitleBar from '../components/TitleBar.vue';
import ViewerPane from './components/ViewerPane.vue';
import InfoPanel from './components/InfoPanel.vue';
import RenamePanel from './components/RenamePanel.vue';
import ProcessPane from './components/ProcessPane.vue';
import SettingsPane from './components/SettingsPane.vue';
import * as api from './lib/api.mjs';
import { basenameOf, extOf } from './lib/format.mjs';
import { DEFAULT_PHRASES, parseErrorCode, phrasesFromConfig } from './lib/rename.mjs';
import { createPreviewLoader, previewKey } from './lib/preview-cache.mjs';
import { hotkeyBlocked } from './lib/viewer.mjs';

const page = ref('viewer');
const settingsTab = ref('quick'); // settings 视图当前页签：quick / assoc / formats
const processBusy = ref(false); // 处理任务是否在跑：由 ProcessPane 的 running 事件驱动
const files = ref([]);
const index = ref(0);
const previewUrl = ref('');
const state = ref('empty');
const errorCode = ref('');
const phrases = ref(DEFAULT_PHRASES.slice());
const defaultStatus = ref(null);
const renaming = ref(false);
const renamingBusy = ref(false);
const renameErrorCode = ref('');
const renameErrorMessage = ref('');
const toastText = ref('');

const viewerRef = ref(null);
const processRef = ref(null);

// 共享窗口外壳：TitleBar 经 api.mjs 的 windowApi 取窗口能力（不直接碰主客户端桥接）；
// 最大化状态由 TitleBar 自身订阅 onMaxChange，页面不再重复跟踪。

let toastTimer = 0;
let previewSeq = 0;
let unsubscribe = null;
// 外部删除时保留「同一张图」最后一次成功的画面；切到别的文件后不得沿用（否则会张冠李戴）
let lastPreview = '';
let lastPreviewPath = '';
const previewMax = ref(2560);
const previewLoader = createPreviewLoader({
  requestPreview: (path, maxSize) => api.preview(path, maxSize),
  getMaxSize: () => previewMax.value,
});
let openSeq = 0;
// 冷启动排障锚点：只存内存，供冒烟与现场排查读取（不写盘、不含图片内容）
const COLD_START_ATTEMPTS = 4;
const coldTrace = { attempts: 0, calls: 0, lastPaths: 0, lastError: '', reason: '' };

const current = computed(() => files.value[index.value] || null);

function toast(text) {
  toastText.value = String(text || '');
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => {
    toastText.value = '';
  }, 2600);
}

function dirOf(path) {
  const text = String(path || '');
  const at = Math.max(text.lastIndexOf('\\'), text.lastIndexOf('/'));
  return at > 0 ? text.slice(0, at) : '';
}

/** 打开图片：走壳层文件对话框（选中即授权），便携版也只用这一入口。 */
async function openImages() {
  try {
    const picked = await api.pickFiles();
    if (!picked || !picked.length) return;
    await openPaths(picked);
  } catch (e) {
    toast(api.cleanError(e) || '无法打开图片');
  }
}

// 冷启动时「核心还没起来」的重试统一放在主进程（electron/imagetools/index.cjs 的 _callBridge），
// 渲染层不再重复退避，否则两层叠加会把失败反馈拖到分钟级。这里只透传错误。

/** 首图先预览，目录随后补全；目录失败时仍保留选中的图片。 */
async function openPaths(list) {
  const paths = (Array.isArray(list) ? list : []).filter(
    (item) => typeof item === 'string' && item
  );
  if (!paths.length) return;
  const seq = ++openSeq;
  coldTrace.calls += 1;
  coldTrace.lastPaths = paths.length;
  coldTrace.lastError = '';
  const first = paths[0];
  const provisional = normalizeFile({ path: first, name: basenameOf(first), ext: extOf(first) });
  const previous = lastPreviewPath === first ? current.value : null;
  previewLoader.reset(previous);
  if (previous) previewLoader.rekey(previous, provisional);
  else {
    lastPreview = '';
    lastPreviewPath = '';
  }
  files.value = [provisional];
  index.value = 0;
  page.value = 'viewer';
  const firstLoad = loadAt(0);
  let listing = { directory: dirOf(first), files: [] };
  try {
    listing = await api.list(first);
  } catch (e) {
    listing = { directory: dirOf(first), files: [] };
  }
  await firstLoad;
  if (seq !== openSeq) return;

  const dirFiles = (listing && listing.files) || [];
  files.value = dirFiles.length
    ? dirFiles.map((file) => normalizeFile(file))
    : paths.map((path) => normalizeFile({ path, name: basenameOf(path), ext: extOf(path) }));
  let at = files.value.findIndex((file) => file.path === first);
  if (at < 0) at = files.value.findIndex((file) => file.name === basenameOf(first));
  let enriched = at >= 0 ? files.value[at] : null;
  if (!enriched) {
    enriched = provisional;
    files.value = [enriched, ...files.value];
    at = 0;
  }
  index.value = at;
  previewLoader.rekey(provisional, enriched);
  previewLoader.setCurrent(enriched);
  if (state.value === 'ready') previewLoader.schedule(files.value, index.value);
}

function normalizeFile(file) {
  return {
    name: file.name || basenameOf(file.path),
    path: file.path,
    ext: String(file.ext || extOf(file.path)).toLowerCase(),
    size: Number(file.size) || 0,
    mtime: Number(file.mtime) || 0,
    format: file.format || '',
    mime: file.mime || '',
    supported: file.supported !== false,
    animated: !!file.animated,
    width: Number(file.width) || 0,
    height: Number(file.height) || 0,
  };
}

async function loadAt(target) {
  const seq = ++previewSeq;
  const total = files.value.length;
  if (!(total > 0)) {
    state.value = 'empty';
    previewUrl.value = '';
    errorCode.value = '';
    return;
  }
  const next = Math.max(0, Math.min(total - 1, Number(target) || 0));
  index.value = next;
  renaming.value = false;
  renameErrorCode.value = '';
  renameErrorMessage.value = '';
  const file = files.value[next];
  if (!file) return;
  if (!file.supported) {
    state.value = 'failed';
    errorCode.value = 'IMG_UNSUPPORTED';
    previewUrl.value = lastPreviewPath === file.path ? lastPreview : '';
    return;
  }
  previewLoader.setCurrent(file);
  state.value = 'loading';
  errorCode.value = '';
  previewUrl.value = '';
  try {
    const result = await previewLoader.load(file);
    if (seq !== previewSeq) return;
    previewUrl.value = result && result.url ? result.url : '';
  } catch (e) {
    if (seq !== previewSeq) return;
    errorCode.value = parseErrorCode(e) || 'IMG_DECODE_FAILED';
    previewUrl.value = lastPreviewPath === file.path ? lastPreview : '';
    state.value = errorCode.value === 'FILE_NOT_FOUND' ? 'gone' : 'failed';
    toast(api.cleanError(e) || '无法读取这张图片');
  }
}

function onPhotoLoad(payload) {
  const file = current.value;
  const url = payload && typeof payload === 'object' ? payload.url : payload;
  const path = payload && typeof payload === 'object' ? payload.path : file && file.path;
  if (!file || !url || state.value !== 'loading' || path !== file.path || url !== previewUrl.value)
    return;
  lastPreview = previewUrl.value;
  lastPreviewPath = file.path;
  state.value = 'ready';
  document.title = `图片工具 — ${file.name}`;
  previewLoader.schedule(files.value, index.value);
}

function onPhotoError(payload) {
  const file = current.value;
  const url = payload && typeof payload === 'object' ? payload.url : payload;
  const path = payload && typeof payload === 'object' ? payload.path : file && file.path;
  if (!file || !url || path !== file.path || url !== previewUrl.value) return;
  invalidatePreview(file);
  state.value = 'failed';
  errorCode.value = 'IMG_DECODE_FAILED';
  previewUrl.value = lastPreviewPath === file.path ? lastPreview : '';
}

function invalidatePreview(file) {
  if (!file) return;
  previewLoader.invalidate(file);
  if (lastPreviewPath === file.path) {
    lastPreview = '';
    lastPreviewPath = '';
  }
}

async function rescan() {
  const file = current.value;
  if (!file) return;
  // 重扫使旧任务与其他 Blob 失效；仅保留当前图最后成功画面，旧结果不得写入新缓存
  openSeq += 1;
  previewSeq += 1;
  previewLoader.reset(file);
  if (lastPreviewPath !== file.path) {
    lastPreview = '';
    lastPreviewPath = '';
  }
  try {
    const listing = await api.list(file.path);
    const dirFiles = ((listing && listing.files) || []).map((item) => normalizeFile(item));
    if (!dirFiles.length) {
      invalidatePreview(file);
      files.value = [];
      index.value = 0;
      state.value = 'empty';
      previewUrl.value = '';
      return;
    }
    let at = dirFiles.findIndex((item) => item.path === file.path);
    if (at < 0 || previewKey(dirFiles[at]) !== previewKey(file)) invalidatePreview(file);
    files.value = dirFiles;
    if (at < 0) at = Math.min(index.value, dirFiles.length - 1);
    await loadAt(at);
  } catch (e) {
    toast(api.cleanError(e) || '重新扫描失败');
  }
}

function openFolder() {
  const file = current.value;
  if (!file) return;
  api.showItem(file.path).catch((e) => toast(api.cleanError(e) || '无法打开所在文件夹'));
}

function startRename() {
  if (!current.value) return;
  renaming.value = true;
  renameErrorCode.value = '';
  renameErrorMessage.value = '';
  nextTick(() => {
    const input = document.getElementById('it-name-input');
    if (input) input.focus();
  });
}

function cancelRename() {
  renaming.value = false;
  renameErrorCode.value = '';
  renameErrorMessage.value = '';
}

async function submitRename({ name }) {
  const file = current.value;
  if (!file) return;
  renamingBusy.value = true;
  renameErrorCode.value = '';
  renameErrorMessage.value = '';
  try {
    const info = await api.rename(file.path, name);
    const updated = normalizeFile(info || { path: file.path, name: `${name}${extOf(file.path)}` });
    const list = files.value.slice();
    // 改名只动一个文件：按原位置替换，保持当前序号与浏览位置
    list[index.value] = { ...file, ...updated };
    files.value = list;
    renaming.value = false;
    invalidatePreview(file);
    if (updated.path && updated.path !== file.path) invalidatePreview(updated);
    await loadAt(index.value);
    toast(`已重命名为 ${list[index.value].name}`);
  } catch (e) {
    renameErrorCode.value = parseErrorCode(e);
    renameErrorMessage.value = api.cleanError(e);
    // 失败保留输入内容：面板不自建状态，输入仍在上一次提交的值里
  } finally {
    renamingBusy.value = false;
  }
}

function closeInfo() {
  const viewer = viewerRef.value;
  if (viewer && typeof viewer.closeInfo === 'function') viewer.closeInfo();
}

function openSettings() {
  // 顶栏齿轮：从默认页签进入
  settingsTab.value = 'quick';
  page.value = 'settings';
}

function openAssoc() {
  // 信息栏「前往文件关联」：直接落在文件关联页签，不停在快捷输入
  settingsTab.value = 'assoc';
  page.value = 'settings';
}

/** 处理任务运行状态：驱动 ProcessPane 的 busy 与自身的按键封锁。 */
function onProcessRunning(running) {
  processBusy.value = !!running;
}

function onPhrasesSaved(list) {
  phrases.value = Array.isArray(list) ? list.slice() : phrases.value;
}

/** 页面上是否有打开的弹层/抽屉（取消确认、运行日志、完整路径等）。 */
function hasOpenModal() {
  return !!document.querySelector('[role="dialog"][aria-modal="true"]');
}

/**
 * 全局快捷键：F2 进入重命名、← → 切图。
 * 挂在 document 上，冷启动不点画布也能用；输入框、下拉、弹层与设置/处理页都不抢键。
 */
function onKeydown(event) {
  if (
    hotkeyBlocked({
      page: page.value,
      target: event.target,
      renaming: renaming.value,
      modalOpen: hasOpenModal(),
    })
  ) {
    return;
  }
  if (event.key === 'F2') {
    event.preventDefault();
    startRename();
    return;
  }
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
    const total = files.value.length;
    if (!total) return;
    const next = index.value + (event.key === 'ArrowRight' ? 1 : -1);
    // 首尾不循环：越界时不做任何事，也不 preventDefault 抢走焦点移动
    if (next < 0 || next >= total) return;
    event.preventDefault();
    loadAt(next);
  }
}

function onOpenEvent(payload) {
  if (!payload || !Array.isArray(payload.files) || !payload.files.length) return;
  openPaths(payload.files);
}

function onEvent(payload) {
  if (!payload) return;
  if (payload.event === 'open') {
    onOpenEvent(payload);
    return;
  }
  if (payload.event === 'core.exit') {
    if (processRef.value) processRef.value.onCoreExit();
  }
}

async function loadSettings() {
  try {
    const cfg = await api.getConfig();
    // 空数组是用户主动删光的合法状态，只有读不到配置才回落默认
    phrases.value = phrasesFromConfig(cfg && cfg.quick_phrases);
    previewMax.value = Number(cfg.preview_max) || 2560;
  } catch (e) {
    phrases.value = DEFAULT_PHRASES.slice();
  }
  try {
    defaultStatus.value = await api.getDefaultStatus();
  } catch (e) {
    defaultStatus.value = null;
  }
}

// 冷启动补取：主进程可能在页面订阅「open」之前就推了首帧（事件丢失），
// 也可能稍后才登记待开文件。独立调度 + 有限次重试，拿到即停。
function bootstrapPendingFiles(attempt = 0) {
  coldTrace.attempts = attempt + 1;
  if (attempt >= COLD_START_ATTEMPTS) {
    coldTrace.reason = 'exhausted';
    return;
  }
  if (files.value.length) {
    coldTrace.reason = 'already-open';
    return;
  }
  if (!api.available()) {
    // 预加载尚未就绪时也要继续退避重试，不能就此放弃
    coldTrace.reason = 'no-bridge';
    schedulePendingRetry(attempt + 1);
    return;
  }
  coldTrace.reason = 'pending';
  Promise.resolve()
    .then(() => api.ready())
    .then((pending) => {
      const list = pending && Array.isArray(pending.files) ? pending.files : [];
      if (!list.length) {
        coldTrace.reason = 'empty-payload';
        return;
      }
      if (files.value.length) return;
      coldTrace.reason = 'opening';
      return openPaths(list);
    })
    .catch((e) => {
      // 旧 preload 无该通道时保持事件路径；其他错误记下来供排障
      coldTrace.lastError = api.cleanError(e);
    })
    .finally(() => {
      // 没拿到就退避再试：首帧事件可能恰好落在订阅之前
      if (!files.value.length) schedulePendingRetry(attempt + 1);
    });
}

function schedulePendingRetry(attempt) {
  if (attempt >= COLD_START_ATTEMPTS) return;
  window.setTimeout(() => bootstrapPendingFiles(attempt), 250 * attempt);
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown);
  try {
    unsubscribe = api.onEvent(onEvent);
  } catch (_e) {
    // 订阅失败不影响补取：下面仍会用 ready() 兜住待开文件
  }
  // 真实 GUI 冒烟的唯一接缝：用一批**已授权**路径直接填处理列表，跳过系统文件对话框。
  // 路径授权仍由主进程在 runProcess 时逐项复核，这里不放开任何新目录，也不影响正常交互。
  if (typeof window !== 'undefined') {
    window.__imageToolsSmoke = {
      addPaths: (paths) =>
        processRef.value ? processRef.value.addPaths(paths) : Promise.resolve(),
      // 任务终态 / 核心退出的驱动入口：与真实事件走同一处理函数
      simulateJobDone: (job) =>
        processRef.value ? processRef.value.simulateJobDone(job) : undefined,
      coreExit: () => (processRef.value ? processRef.value.onCoreExit() : undefined),
      simulateInterrupted: () =>
        processRef.value ? processRef.value.simulateInterrupted() : undefined,
      // 真实任务号：冒烟据它调桥接的 getJob 核对真实取消结果
      currentJobId: () => (processRef.value ? processRef.value.currentJobId() : ''),
      // 预览缓存的只读探针：确认键含 mtime/size、且外部改动后不再命中
      previewCacheSize: () => previewLoader.stats().cacheSize,
      previewReady: () => state.value === 'ready',
      previewStats: () => ({ ...previewLoader.stats() }),
      previewWhenIdle: () => previewLoader.whenIdle(),
      // 用给定 {path, mtime, size} 查缓存是否命中：证明键确实含 mtime/size
      previewHas: (file) => previewLoader.has(file),
      currentFile: () => (current.value ? { ...current.value } : null),
      coldTrace: () => ({ ...coldTrace }),
      rescan,
    };
  }
  // 先启动补取（不 await）：任何后续初始化抛错都不会丢掉待开文件
  bootstrapPendingFiles();
  loadSettings().catch(() => {});
});

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown);
  window.clearTimeout(toastTimer);
  openSeq += 1;
  previewSeq += 1;
  previewLoader.dispose();
  if (unsubscribe) unsubscribe();
  if (typeof window !== 'undefined') delete window.__imageToolsSmoke;
});

// 供静态检查与调试使用：页面只有查看/处理/设置三个视图，没有别的页签
defineExpose({ page, files, loadAt });
</script>
