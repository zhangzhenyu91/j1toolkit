<template>
  <div>
    <div v-if="rendered" class="mask ov-mask" :class="phaseClass" @click.self="close">
      <div class="modal ov-panel">
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">水印照片已生成</span>
            <button type="button" class="m-x" @click="close">
              <LegacyIcon name="close" :size="14" />
            </button>
          </div>
          <div class="wm-result">
            <img :src="image" alt="水印照片" title="点击全屏查看" @click="previewOpen = true" />
          </div>
          <div class="wm-result-hint">
            已自动下载到本地（若被浏览器拦截请点「下载图片」）；点击图片可全屏查看。照片仅返回本人，不上传、不存档。
          </div>
          <div class="m-foot">
            <button type="button" class="btn btn-sm" @click="close">再做一张</button>
            <button type="button" class="btn btn-sm btn-primary" @click="download">
              <LegacyIcon name="download" :size="14" />下载图片
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- 全屏预览层（对应小程序 wx.previewImage；点击任意处 / Esc 关闭；同口径 overlay-motion 进出场） -->
    <div v-if="pvRendered" class="pv ov-mask" :class="pvPhase" @click="previewOpen = false">
      <img class="ov-panel" :src="image" alt="水印照片预览" />
    </div>
  </div>
</template>

<script setup>
// 渲染结果弹层（移植 server/public/wmadd.html openResult / openPreview）：
// 打开即自动下载一次（对应小程序自动存相册，被浏览器拦截可手动「下载图片」），
// 点击图片进全屏预览（对应 wx.previewImage，点击任意处 / Esc 关闭）。
import { onBeforeUnmount, ref, watch } from 'vue';
import LegacyIcon from '../core/LegacyIcon.vue';
import { useOverlayMotion } from '../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';
import { downloadDataUrl, tsName } from './wm-utils.js';

const props = defineProps({
  open: { type: Boolean, default: false },
  // 渲染结果 JPEG dataURL
  image: { type: String, default: '' },
});
const emit = defineEmits(['update:open']);

const previewOpen = ref(false);
let fileName = '';

// open 显隐镜像：请求关闭本地先播退出动画（父级 v-model:open 跟随），关闭不瞬间消失。
const open = ref(props.open);
watch(
  () => props.open,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);
// 全屏预览层同口径进出场（遮罩淡入 + 图片上浮/缩放）
const { rendered: pvRendered, phaseClass: pvPhase } = useOverlayMotion(previewOpen);

// 打开即自动落地一次（等价小程序自动存相册），文件名 水印照片_时间戳.jpg
watch(
  () => props.open,
  (v) => {
    previewOpen.value = false;
    if (!v || !props.image) return;
    fileName = '水印照片_' + tsName(new Date()) + '.jpg';
    downloadDataUrl(props.image, fileName);
  }
);

// Esc 关闭：全屏预览在上层时只关预览（交其自身 onKey），否则关弹层（document 捕获监听）。
function onEsc(e) {
  if (e.key !== 'Escape') return;
  if (previewOpen.value) return;
  e.stopPropagation();
  close();
}
// Esc 关闭全屏预览
function onKey(e) {
  if (e.key === 'Escape') previewOpen.value = false;
}
watch(rendered, (r) => {
  if (r) document.addEventListener('keydown', onEsc, true);
  else document.removeEventListener('keydown', onEsc, true);
});
watch(previewOpen, (on) => {
  if (on) document.addEventListener('keydown', onKey);
  else document.removeEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEsc, true);
  document.removeEventListener('keydown', onKey);
});

function download() {
  if (!props.image) return;
  downloadDataUrl(props.image, fileName || '水印照片.jpg');
}
function close() {
  if (!open.value) return;
  open.value = false;
  emit('update:open', false);
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
  width: 640px;
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

/* ===== 渲染结果：图片预览（点击全屏）+ 提示条（直角浅底左色条一族） ===== */
.wm-result {
  text-align: center;
}
.wm-result img {
  max-width: 100%;
  max-height: 56vh;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  cursor: zoom-in;
  box-shadow: var(--tk-shadow-card);
}
.wm-result-hint {
  margin-top: 12px;
  padding: 10px 12px;
  border-left: 3px solid var(--tk-brand);
  background: var(--tk-blue-soft);
  font-size: 11.5px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  line-height: 1.7;
}

/* ===== 全屏预览层（对应小程序 wx.previewImage） ===== */
.pv {
  position: fixed;
  inset: 0;
  z-index: 400;
  padding: 36px;
  background: rgba(29, 33, 41, 0.88);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: zoom-out;
}
.pv img {
  max-width: 100%;
  max-height: 90vh;
  border-radius: 8px;
  box-shadow: var(--tk-shadow-hover);
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
