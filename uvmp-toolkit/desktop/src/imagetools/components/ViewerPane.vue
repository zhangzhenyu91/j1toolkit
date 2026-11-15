<template>
  <div class="it-viewer" :class="{ 'is-drawer-open': drawerOpen }">
    <div class="it-viewbar">
      <button type="button" class="it-tooltext it-toolopen" @click="$emit('open-images')">
        <ITIcon name="image" :size="15" />打开图片
      </button>
      <span class="it-vsep"></span>
      <button
        type="button"
        class="it-iconbtn it-vnudge"
        title="上一张（←）"
        aria-label="上一张"
        :disabled="!canPrev"
        @click="step(-1)"
      >
        <ITIcon name="prev" :size="16" />
      </button>
      <span class="it-counter" aria-live="polite">
        <b>{{ total ? index + 1 : 0 }}</b><span class="it-c-sep">/</span><span>{{ total }}</span>
      </span>
      <button
        type="button"
        class="it-iconbtn it-vnudge"
        title="下一张（→）"
        aria-label="下一张"
        :disabled="!canNext"
        @click="step(1)"
      >
        <ITIcon name="next" :size="16" />
      </button>
      <span class="it-vsep"></span>
      <button
        type="button"
        class="it-iconbtn"
        title="缩小"
        aria-label="缩小"
        :disabled="isMinZoom"
        @click="zoomStep(-1)"
      >
        <ITIcon name="zoom-out" :size="16" />
      </button>
      <div class="it-zoomwrap">
        <button
          type="button"
          class="it-zoom-pct"
          :aria-expanded="zoomMenu ? 'true' : 'false'"
          aria-haspopup="menu"
          @click="zoomMenu = !zoomMenu"
        >
          <span>{{ zoomText }}</span>
          <ITIcon name="chevron-down" :size="13" />
        </button>
        <div v-if="zoomMenu" class="it-popmenu" role="menu">
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom('fit')">
            适应窗口<span class="it-zval">{{ fitText }}</span>
          </button>
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom(0.25)">25%</button>
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom(0.5)">50%</button>
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom(1)">
            100%（实际尺寸）
          </button>
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom(2)">200%</button>
          <button type="button" class="it-zi" role="menuitem" @click="applyZoom(4)">400%</button>
          <div class="it-zimeta">滚轮缩放以鼠标位置为中心</div>
        </div>
      </div>
      <button
        type="button"
        class="it-iconbtn"
        title="放大"
        aria-label="放大"
        :disabled="isMaxZoom"
        @click="zoomStep(1)"
      >
        <ITIcon name="zoom-in" :size="16" />
      </button>
      <span class="it-vsep"></span>
      <button
        type="button"
        class="it-tooltext"
        :class="{ 'is-on': mode === 'fit' }"
        @click="applyZoom('fit')"
      >
        适应窗口
      </button>
      <button
        type="button"
        class="it-tooltext"
        :class="{ 'is-on': mode === 'actual' }"
        @click="applyZoom(1)"
      >
        实际尺寸
      </button>
      <div class="it-viewbar-right">
        <button
          type="button"
          class="it-iconbtn"
          :title="isFullscreen ? '退出全屏（Esc）' : '全屏沉浸预览'"
          :aria-label="isFullscreen ? '退出全屏' : '全屏'"
          @click="toggleFullscreen"
        >
          <ITIcon :name="isFullscreen ? 'exitFullscreen' : 'fullscreen'" :size="16" />
        </button>
        <button
          type="button"
          class="it-info-toggle"
          :aria-pressed="infoOpen ? 'true' : 'false'"
          @click="toggleInfo"
        >
          <ITIcon name="panel" :size="15" />信息栏
        </button>
      </div>
    </div>

    <div class="it-viewer-main" :class="{ 'is-narrow': narrow }">
      <div class="it-canvas-col">
        <div
          ref="canvasEl"
          class="it-canvas"
          :class="{ 'can-pan': pannable && !!file, 'is-dragging': dragging }"
          tabindex="0"
          role="application"
          :aria-label="file ? `图片画布：${file.name}` : '图片画布'"
          @wheel.prevent="onWheel"
          @pointerdown="onPointerDown"
          @pointermove="onPointerMove"
          @pointerup="onPointerUp"
          @pointercancel="onPointerUp"
          @dblclick="onDoubleClick"
          @keydown="onKeydown"
        >
          <img
            v-if="file && previewUrl && keepFrame"
            ref="photoEl"
            :key="previewUrl"
            class="it-photo"
            :class="{ 'is-hidden': state !== 'ready' }"
            :src="previewUrl"
            :alt="file.name"
            draggable="false"
            :style="photoStyle"
            @load="onPhotoLoad"
            @error="onPhotoError"
          />

          <div v-if="state === 'empty'" class="it-empty">
            <span class="it-e-ic"><ITIcon name="image" :size="26" stroke="1.4" /></span>
            <b>当前目录没有可查看的图片</b>
            <span>支持 JPEG / PNG / GIF / WebP / BMP / ICO / AVIF / TIFF</span>
            <span>也可从其他文件夹打开一张图片</span>
            <button type="button" class="it-empty-act" @click="$emit('open-images')">
              <ITIcon name="image" :size="14" />打开图片
            </button>
          </div>

          <div
            v-else-if="state === 'loading'"
            class="it-loading"
            :class="{ 'is-slow': slowLoading }"
          >
            <span class="it-spinner"></span>
            <b>正在读取图片</b>
            <span>{{ file ? file.name : '' }} · 大图解码中</span>
            <div v-if="slowLoading" class="it-load-bar"><i></i></div>
            <span class="it-load-note">超过 1 秒才出现进度，避免闪烁</span>
          </div>

          <div v-else-if="state === 'failed'" class="it-error">
            <span class="it-er-ic"><ITIcon name="error" :size="24" stroke="1.5" /></span>
            <b>无法显示这张图片</b>
            <span>文件可能已损坏，或使用了暂不支持的编码</span>
            <em>{{ errorCode || 'IMG_DECODE_FAILED' }} · {{ file ? file.name : '' }}</em>
            <div class="it-error-acts">
              <button type="button" class="it-btn is-ghost" @click="$emit('open-folder')">
                打开所在文件夹
              </button>
              <button type="button" class="it-btn is-ghost" :disabled="!canNext" @click="step(1)">
                下一张
              </button>
            </div>
          </div>

          <div v-else-if="state === 'gone'" class="it-error is-gone">
            <span class="it-er-ic"><ITIcon name="warn" :size="24" stroke="1.5" /></span>
            <b>文件已不在原位置</b>
            <span>可能被其他程序删除、移动或改名；画面保留最后一次成功读取的内容</span>
            <em>FILE_NOT_FOUND · {{ file ? file.name : '' }}</em>
            <div class="it-error-acts">
              <button type="button" class="it-btn is-ghost" @click="$emit('rescan')">
                重新扫描目录
              </button>
              <button type="button" class="it-btn is-ghost" :disabled="!canNext" @click="step(1)">
                下一张
              </button>
            </div>
          </div>

          <div v-if="pannable && state === 'ready'" class="it-canvas-hint">
            <ITIcon name="fit" :size="12" />放大后拖动查看 · 滚轮以鼠标为中心缩放
          </div>
          <div v-if="boundaryNote" class="it-zoom-boundary">{{ boundaryNote }}</div>
        </div>

        <div class="it-viewfoot">
          <span>
            第<b>{{ total ? index + 1 : 0 }}</b
            >张 / 共<b>{{ total }}</b
            >张 · 同目录自然排序 · 不递归子目录
          </span>
          <span class="it-viewkeys">
            <kbd>←</kbd><kbd>→</kbd>切图（不循环） <i class="it-fk-dot"></i>滚轮缩放<i
              class="it-fk-dot"
            ></i
            >拖动平移
          </span>
        </div>
      </div>

      <div v-if="drawerOpen" class="it-scrim" aria-hidden="true" @click="drawerOpen = false"></div>

      <aside
        v-if="infoOpen"
        class="it-inspector"
        :class="{ 'is-drawer': narrow }"
        :aria-hidden="false"
      >
        <slot name="inspector"></slot>
      </aside>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import ITIcon from './ITIcon.vue';
import {
  ZOOM_MAX,
  ZOOM_MIN,
  canStep,
  clampOffset,
  clampZoom,
  fitScale,
  formatZoom,
  nextZoom,
  stepIndex,
  wheelZoom,
  zoomAroundOffset,
} from '../lib/viewer.mjs';

const props = defineProps({
  file: { type: Object, default: null },
  index: { type: Number, default: 0 },
  total: { type: Number, default: 0 },
  previewUrl: { type: String, default: '' },
  state: { type: String, default: 'empty' },
  errorCode: { type: String, default: '' },
});
const emit = defineEmits(['step', 'open-images', 'open-folder', 'rescan', 'notify']);

const canvasEl = ref(null);
const photoEl = ref(null);
const scale = ref(1);
const mode = ref('fit');
const offsetX = ref(0);
const offsetY = ref(0);
const boxWidth = ref(0);
const boxHeight = ref(0);
const imageWidth = ref(0);
const imageHeight = ref(0);
const zoomMenu = ref(false);
const infoOpen = ref(true);
const narrow = ref(false);
const drawerOpen = ref(true);
const isFullscreen = ref(false);
const dragging = ref(false);
const boundaryNote = ref('');
const loadTimer = ref(0);
const slowLoading = ref(false);

let dragStart = null;
let resizeObserver = null;
let boundaryTimer = 0;

const canPrev = computed(() => !!props.file && canStep(props.index, -1, props.total));
const canNext = computed(() => !!props.file && canStep(props.index, 1, props.total));
const zoomText = computed(() => formatZoom(scale.value));
const fitText = computed(() =>
  formatZoom(fitScale(imageWidth.value, imageHeight.value, boxWidth.value, boxHeight.value))
);
const isMinZoom = computed(() => scale.value <= ZOOM_MIN + 1e-6);
const isMaxZoom = computed(() => scale.value >= ZOOM_MAX - 1e-6);
// 外部删除/解码失败时保留最后一次成功的画面，只在其上叠加状态说明
// 外部删除/解码失败时保留最后一次成功的画面：只在仍有该图画面时保留，
// 失败且没有画面（例如切到一张坏图）就只显示失败状态，避免张冠李戴
const keepFrame = computed(() =>
  props.state === 'failed' ? !!props.previewUrl : ['loading', 'ready', 'gone'].includes(props.state)
);
const pannable = computed(() => {
  if (!imageWidth.value) return false;
  return (
    imageWidth.value * scale.value > boxWidth.value + 2 ||
    imageHeight.value * scale.value > boxHeight.value + 2
  );
});
const photoStyle = computed(() => ({
  width: `${Math.max(1, imageWidth.value * scale.value)}px`,
  height: `${Math.max(1, imageHeight.value * scale.value)}px`,
  transform: `translate(-50%, -50%) translate(${offsetX.value}px, ${offsetY.value}px)`,
}));

function measure() {
  const el = canvasEl.value;
  if (!el) return;
  boxWidth.value = el.clientWidth;
  boxHeight.value = el.clientHeight;
}

function resetView() {
  offsetX.value = 0;
  offsetY.value = 0;
  mode.value = 'fit';
  scale.value = fitScale(imageWidth.value, imageHeight.value, boxWidth.value, boxHeight.value);
}

function setScale(next, anchor) {
  const clamped = clampZoom(next);
  if (!anchor) {
    scale.value = clamped;
    return;
  }
  const box = canvasEl.value;
  if (!box) {
    scale.value = clamped;
    return;
  }
  const rect = box.getBoundingClientRect();
  const result = zoomAroundOffset({
    scale: scale.value,
    offsetX: offsetX.value,
    offsetY: offsetY.value,
    nextScale: clamped,
    pointerX: anchor.x - rect.left,
    pointerY: anchor.y - rect.top,
    centerX: rect.width / 2,
    centerY: rect.height / 2,
  });
  scale.value = result.scale;
  offsetX.value = result.offsetX;
  offsetY.value = result.offsetY;
}

function applyZoom(target) {
  zoomMenu.value = false;
  if (target === 'fit') {
    resetView();
    return;
  }
  mode.value = Math.abs(Number(target) - 1) < 1e-6 ? 'actual' : 'manual';
  setScale(Number(target));
}

function zoomStep(direction) {
  mode.value = 'manual';
  setScale(nextZoom(scale.value, direction));
}

function noteBoundary(text) {
  boundaryNote.value = text;
  window.clearTimeout(boundaryTimer);
  boundaryTimer = window.setTimeout(() => {
    boundaryNote.value = '';
  }, 2400);
}

function onWheel(event) {
  if (!props.file) return;
  const before = scale.value;
  const next = wheelZoom(before, event.deltaY);
  if (next === before) {
    noteBoundary(
      next >= ZOOM_MAX
        ? '已达最大 400% · 放大按钮禁用 · 滚轮再放大无变化'
        : '已达最小 25% · 缩小按钮禁用 · 滚轮再缩小无变化'
    );
    return;
  }
  mode.value = 'manual';
  setScale(next, { x: event.clientX, y: event.clientY });
}

function onPointerDown(event) {
  if (!props.file || !pannable.value || event.button !== 0) return;
  dragging.value = true;
  dragStart = {
    x: event.clientX,
    y: event.clientY,
    offsetX: offsetX.value,
    offsetY: offsetY.value,
    pointerId: event.pointerId,
  };
  if (canvasEl.value && canvasEl.value.setPointerCapture) {
    try {
      canvasEl.value.setPointerCapture(event.pointerId);
    } catch (e) {
      // 某些平台对已释放的指针会抛错：忽略，拖动逻辑仍可用
    }
  }
}

function onPointerMove(event) {
  if (!dragging.value || !dragStart) return;
  const scaledWidth = imageWidth.value * scale.value;
  const scaledHeight = imageHeight.value * scale.value;
  const bounded = clampOffset(
    dragStart.offsetX + (event.clientX - dragStart.x),
    dragStart.offsetY + (event.clientY - dragStart.y),
    scaledWidth,
    scaledHeight,
    boxWidth.value,
    boxHeight.value
  );
  offsetX.value = bounded.offsetX;
  offsetY.value = bounded.offsetY;
}

function onPointerUp() {
  if (!dragging.value) return;
  dragging.value = false;
  dragStart = null;
}

function onDoubleClick() {
  if (!props.file) return;
  if (mode.value === 'actual') resetView();
  else applyZoom(1);
}

function step(direction) {
  const target = stepIndex(props.index, direction, props.total);
  if (target < 0) return;
  emit('step', target);
}

// 切图（← →）与重命名（F2）由 App 的 document 级快捷键统一接管：
// 冷启动不必先点画布，且输入框/弹层/设置页的排除规则只在一处维护。
function onKeydown(event) {
  if (event.key === '+' || event.key === '=') {
    event.preventDefault();
    zoomStep(1);
    return;
  }
  if (event.key === '-' || event.key === '_') {
    event.preventDefault();
    zoomStep(-1);
    return;
  }
  if (event.key === '0') {
    event.preventDefault();
    resetView();
    return;
  }
  if (event.key === 'Escape' && drawerOpen.value && narrow.value && infoOpen.value) {
    drawerOpen.value = false;
  }
}

async function toggleFullscreen() {
  try {
    if (!document.fullscreenElement) {
      await document.documentElement.requestFullscreen();
    } else {
      await document.exitFullscreen();
    }
  } catch (e) {
    emit('notify', '当前系统不允许全屏预览');
  }
}

function onFullscreenChange() {
  isFullscreen.value = !!document.fullscreenElement;
  // 全屏切换后画布尺寸变了：适应窗口模式按新尺寸重算，手动缩放保持
  nextTick(() => {
    measure();
    if (mode.value === 'fit') resetView();
  });
}

function toggleInfo() {
  infoOpen.value = !infoOpen.value;
  if (infoOpen.value) drawerOpen.value = true;
}

function closeInfo() {
  infoOpen.value = false;
}

function onPhotoLoad() {
  const img = photoEl.value;
  if (!img) return;
  window.clearTimeout(loadTimer.value);
  slowLoading.value = false;
  imageWidth.value = img.naturalWidth || 0;
  imageHeight.value = img.naturalHeight || 0;
  measure();
  resetView();
}

function onPhotoError() {
  emit('photo-error');
}

function onResize() {
  const previousWidth = boxWidth.value;
  measure();
  if (narrow.value !== window.innerWidth <= 960) narrow.value = window.innerWidth <= 960;
  if (mode.value === 'fit' || previousWidth !== boxWidth.value) {
    if (infoOpen.value && !narrow.value) {
      // 宽窗口信息栏占 300px：画布宽度变化后重新适应
      resetView();
    } else if (mode.value === 'fit') {
      resetView();
    }
  }
}

watch(
  () => props.file && props.file.path,
  () => {
    offsetX.value = 0;
    offsetY.value = 0;
    mode.value = 'fit';
    imageWidth.value = 0;
    imageHeight.value = 0;
    scale.value = 1;
    boundaryNote.value = '';
    window.clearTimeout(loadTimer.value);
    slowLoading.value = false;
  }
);

watch(
  () => props.state,
  (state) => {
    if (state === 'loading') {
      window.clearTimeout(loadTimer.value);
      loadTimer.value = window.setTimeout(() => {
        // 超过 1 秒才出现进度提示，避免小图闪烁
        if (props.state === 'loading') slowLoading.value = true;
      }, 1000);
    } else {
      window.clearTimeout(loadTimer.value);
      slowLoading.value = false;
    }
  }
);

onMounted(() => {
  measure();
  narrow.value = window.innerWidth <= 960;
  document.addEventListener('fullscreenchange', onFullscreenChange);
  window.addEventListener('resize', onResize);
  if (typeof ResizeObserver === 'function' && canvasEl.value) {
    resizeObserver = new ResizeObserver(() => {
      const wasFit = mode.value === 'fit';
      measure();
      if (wasFit) resetView();
    });
    resizeObserver.observe(canvasEl.value);
  }
});

onBeforeUnmount(() => {
  document.removeEventListener('fullscreenchange', onFullscreenChange);
  window.removeEventListener('resize', onResize);
  window.clearTimeout(loadTimer.value);
  window.clearTimeout(boundaryTimer);
  if (resizeObserver) resizeObserver.disconnect();
});

defineExpose({
  focusCanvas: () => canvasEl.value && canvasEl.value.focus(),
  toggleInfo,
  closeInfo,
});
</script>
