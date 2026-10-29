<template>
  <div v-if="rendered" class="mask ov-mask" :class="phaseClass" @click.self="close">
    <div class="modal ov-panel">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">裁剪照片（{{ landscape ? '4:3' : '3:4' }}）</span>
          <button type="button" class="m-x" @click="close">
            <LegacyIcon name="close" :size="14" />
          </button>
        </div>
        <div class="f-hint crop-tip">拖动调整位置，滑块或滚轮缩放；水印按取景框范围渲染</div>
        <div
          ref="frameEl"
          class="crop-frame"
          :class="{ dragging }"
          :style="{ width: fw + 'px', height: fh + 'px' }"
          @pointerdown="onDown"
          @pointermove="onMove"
          @pointerup="onUp"
          @pointercancel="onUp"
          @wheel.prevent="onWheel"
        >
          <img class="crop-img" :src="url" :style="imgStyle" draggable="false" alt="待裁剪照片" />
        </div>
        <div class="crop-bar">
          <span class="crop-z-lab">缩放</span>
          <input
            class="crop-z"
            type="range"
            min="1"
            max="6"
            step="0.01"
            :value="z"
            @input="onSlider"
          />
          <span class="crop-pct">{{ Math.round(z * 100) }}%</span>
        </div>
        <div class="m-foot">
          <button type="button" class="btn btn-sm" @click="close">取消</button>
          <button type="button" class="btn btn-sm btn-primary" @click="confirm">确认裁剪</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 4:3 裁剪层（横拍锁 4:3 / 纵拍锁 3:4；移植 server/public/wmadd.html openCropModal）：
// 图片拖拽平移 + 滑块/滚轮缩放（cover 适配、锚点缩放），确认后可视区换算到图片像素
// 离屏 canvas 重绘裁出（长边 ≤2560，JPEG q0.92，黑底兜透明通道，同小程序口径）。
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import LegacyIcon from '../core/LegacyIcon.vue';
import { useOverlayMotion } from '../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';

const props = defineProps({
  open: { type: Boolean, default: false },
  // 位图（EXIF 方向已由 createImageBitmap 归一）：裁剪导出取像素用
  bmp: { type: Object, default: null },
  // 展示用 objectURL（父组件创建/释放）
  url: { type: String, default: '' },
});
const emit = defineEmits(['update:open', 'done']);

const frameEl = ref(null);
const landscape = ref(true);
const fw = ref(320);
const fh = ref(240);
const z = ref(1);
const dragging = ref(false);

// 取景框内图片位置/尺寸（拖拽/缩放高频更新，经 imgStyle 样式绑定落 DOM）
const x = ref(0);
const y = ref(0);
const bw = ref(0);
const bh = ref(0);
const k = ref(1); // cover 适配基准倍率
let drag = null;

const imgStyle = computed(() => ({
  width: bw.value * k.value * z.value + 'px',
  left: x.value + 'px',
  top: y.value + 'px',
}));

// 打开时按图片方向定取景框（横 4:3 / 纵 3:4）并复位缩放平移
watch(
  () => props.open,
  (open) => {
    if (!open || !props.bmp) return;
    const bmp = props.bmp;
    bw.value = bmp.width;
    bh.value = bmp.height;
    landscape.value = bw.value >= bh.value;
    const maxW = Math.max(200, Math.min(500, window.innerWidth * 0.94 - 60)); // 弹层宽 560（窄屏 94vw）扣内边距
    const maxH = Math.max(window.innerHeight - 360, 220); // 标题/提示/缩放条/按钮预留
    if (landscape.value) {
      fw.value = maxW;
      fh.value = (fw.value * 3) / 4;
    } else {
      fh.value = Math.min((maxW * 4) / 3, maxH);
      fw.value = (fh.value * 3) / 4;
    }
    k.value = Math.max(fw.value / bw.value, fh.value / bh.value);
    z.value = 1;
    x.value = (fw.value - bw.value * k.value) / 2;
    y.value = (fh.value - bh.value * k.value) / 2;
    dragging.value = false;
    drag = null;
  }
);

function clampPos() {
  const rw = bw.value * k.value * z.value;
  const rh = bh.value * k.value * z.value;
  x.value = Math.min(0, Math.max(fw.value - rw, x.value));
  y.value = Math.min(0, Math.max(fh.value - rh, y.value));
}

// setZoom 缩放保持锚点（缺省取景框中心）下的图片点不动
function setZoom(nz, ax, ay) {
  nz = Math.min(6, Math.max(1, nz));
  if (ax == null) {
    ax = fw.value / 2;
    ay = fh.value / 2;
  }
  const s0 = k.value * z.value;
  const s1 = k.value * nz;
  x.value = ax - ((ax - x.value) / s0) * s1;
  y.value = ay - ((ay - y.value) / s0) * s1;
  z.value = nz;
  clampPos();
}

function onDown(e) {
  drag = { px: e.clientX, py: e.clientY, x: x.value, y: y.value };
  dragging.value = true;
  e.currentTarget.setPointerCapture(e.pointerId);
}
function onMove(e) {
  if (!drag) return;
  x.value = drag.x + e.clientX - drag.px;
  y.value = drag.y + e.clientY - drag.py;
  clampPos();
}
function onUp() {
  drag = null;
  dragging.value = false;
}
function onWheel(e) {
  const r = frameEl.value.getBoundingClientRect();
  setZoom(z.value * (e.deltaY < 0 ? 1.09 : 1 / 1.09), e.clientX - r.left, e.clientY - r.top);
}
function onSlider(e) {
  setZoom(parseFloat(e.target.value));
}

// open 显隐镜像：请求关闭本地先播退出动画（父级 v-model:open 跟随），关闭不瞬间消失。
const open = ref(props.open);
watch(
  () => props.open,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);

// onEsc Esc 关闭（document 捕获监听，焦点不在弹层内也能关）。
function onEsc(e) {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  close();
}
watch(rendered, (r) => {
  if (r) document.addEventListener('keydown', onEsc, true);
  else document.removeEventListener('keydown', onEsc, true);
});
onBeforeUnmount(() => document.removeEventListener('keydown', onEsc, true));

function close() {
  if (!open.value) return;
  open.value = false;
  emit('update:open', false);
}

// confirm 可视区换算到图片像素 → 离屏 canvas 重绘裁出（长边 ≤2560，JPEG q0.92）
function confirm() {
  const bmp = props.bmp;
  if (!bmp) return;
  const s = k.value * z.value;
  const sx = Math.max(0, -x.value / s);
  const sy = Math.max(0, -y.value / s);
  const sw = Math.min(bw.value - sx, fw.value / s);
  const sh = Math.min(bh.value - sy, fh.value / s);
  const ok = Math.min(1, 2560 / Math.max(sw, sh));
  const ow = Math.max(1, Math.round(sw * ok));
  const oh = Math.max(1, Math.round(sh * ok));
  const cv = document.createElement('canvas');
  cv.width = ow;
  cv.height = oh;
  const cx = cv.getContext('2d');
  cx.fillStyle = '#000000';
  cx.fillRect(0, 0, ow, oh);
  cx.drawImage(bmp, sx, sy, sw, sh, 0, 0, ow, oh);
  const out = cv.toDataURL('image/jpeg', 0.92);
  close();
  emit('done', out);
}
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

/* ===== 通用弹层骨架（旧版 .mask/.modal/.m-scroll/.m-head/.m-foot） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: var(--tk-overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: var(--tk-card, #ffffff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.m-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.m-scroll::-webkit-scrollbar {
  width: 5px;
}
.m-scroll::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.m-scroll::-webkit-scrollbar-track {
  background: transparent;
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
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card, #ffffff);
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
}
.m-x:hover {
  background: var(--tk-ink);
  color: var(--tk-on-brand, #ffffff);
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
.f-hint {
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.crop-tip {
  margin: 0 0 10px;
}

/* ===== 4:3 裁剪层：取景框锁 4:3（横）/ 3:4（纵），图片拖拽平移 + 滑块/滚轮缩放（cover 适配） ===== */
.crop-frame {
  position: relative;
  overflow: hidden;
  border-radius: 8px;
  background: var(--tk-ink);
  margin: 0 auto;
  touch-action: none;
  user-select: none;
  cursor: grab;
}
.crop-frame.dragging {
  cursor: grabbing;
}
.crop-img {
  position: absolute;
  max-width: none;
  -webkit-user-drag: none;
}
.crop-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}
.crop-bar .crop-z {
  flex: 1;
  accent-color: var(--tk-brand);
}
.crop-bar .crop-z-lab,
.crop-bar .crop-pct {
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  flex: none;
}
.crop-bar .crop-pct {
  width: 42px;
  text-align: right;
  font-variant-numeric: tabular-nums;
}

/* 按钮 */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 36px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--tk-card, #ffffff);
  color: var(--tk-ink);
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
  font-family: inherit;
}
.btn:hover:not([disabled]) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-sm {
  height: 32px;
  padding: 0 14px;
  font-size: 12px;
}
.btn-primary {
  color: var(--tk-on-brand, #ffffff);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover:not([disabled]) {
  color: var(--tk-on-brand, #ffffff);
  filter: brightness(0.92);
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
