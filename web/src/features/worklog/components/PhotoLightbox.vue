<template>
  <div v-if="open" class="wl-lb-mask" @click.self="close">
    <div class="wl-lb">
      <div class="wl-lb-imgbox">
        <img v-if="photo" :src="photo.url" class="wl-lb-img" :alt="photoAlt" />
        <button type="button" class="wl-lb-nav prev" :disabled="index <= 0" @click="step(-1)">
          ‹
        </button>
        <button
          type="button"
          class="wl-lb-nav next"
          :disabled="index >= photos.length - 1"
          @click="step(1)"
        >
          ›
        </button>
        <div class="wl-lb-count num">{{ index + 1 }} / {{ photos.length }}</div>
      </div>

      <div v-if="photo" class="wl-lb-info">
        <div class="wl-lb-tags">
          <span class="dot-tag" :class="statusClass">
            <span class="dot"></span>{{ statusText }}
          </span>
          <span v-if="photo.is_watermark === 0" class="dot-tag"
            ><span class="dot"></span>非水印</span
          >
          <span v-if="photo.date_ok === 0" class="dot-tag red"
            ><span class="dot"></span>日期不符</span
          >
          <span v-if="photo.dest_ok === 0" class="dot-tag red"
            ><span class="dot"></span>地点不符</span
          >
          <!-- 商旅同步角标：1 已同步 / 2 同步失败 -->
          <span v-if="photo.sgcc_synced === 1" class="dot-tag blue">
            <span class="dot"></span>已同步商旅
          </span>
          <span v-else-if="photo.sgcc_synced === 2" class="dot-tag red">
            <span class="dot"></span>同步商旅失败
          </span>
        </div>
        <div class="wl-lb-line">
          <span class="field-label">人名</span>{{ (photo.members || []).join('、') || '—' }}
        </div>
        <template v-if="photo.is_watermark !== 0">
          <div class="wl-lb-line">
            <span class="field-label">施工内容</span>{{ photo.work_content || '—' }}
          </div>
          <div class="wl-lb-line">
            <span class="field-label">拍摄时间</span
            ><span class="num">{{ photo.shot_time || '—' }}</span>
          </div>
          <div class="wl-lb-line">
            <span class="field-label">天气</span>{{ photo.weather || '—' }}
          </div>
          <div class="wl-lb-line">
            <span class="field-label">地点</span>{{ photo.location || '—' }}
          </div>
          <div v-if="photo.lng || photo.lat" class="wl-lb-line">
            <span class="field-label">坐标</span
            ><span class="num">{{ photo.lng }}, {{ photo.lat }}</span>
          </div>
        </template>
        <div class="wl-lb-acts">
          <t-button size="small" variant="outline" @click="download">下载</t-button>
          <t-button
            v-if="photo.sgcc_synced === 2"
            size="small"
            :loading="resyncing"
            @click="resync"
          >
            重试同步商旅
          </t-button>
        </div>
        <div v-if="error" class="wl-error">{{ error }}</div>
      </div>
    </div>
    <button type="button" class="wl-lb-close" @click="close">✕</button>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { downloadPhoto } from '../api.js';
import { resyncPhoto } from '../../sgccclockin/api.js';

// 照片大图预览：全量信息 + 商旅同步状态；同步失败可发起重试（任务化，op 队列可见）。
const props = defineProps({
  open: { type: Boolean, default: false },
  photos: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  entryId: { type: Number, default: 0 },
});
const emit = defineEmits(['update:open', 'update:index', 'op-started']);

const error = ref('');
const resyncing = ref(false);

const photo = computed(() => props.photos[props.index] || null);
const photoAlt = computed(() => ((photo.value && photo.value.members) || []).join('、') || '照片');

// statusClass/statusText 验证状态标签。
const statusClass = computed(() => {
  const p = photo.value;
  if (!p) return '';
  return (
    { passed: 'green', mismatch: 'red', failed: 'red', pending: 'blue', skipped: '' }[
      p.verify_status
    ] || ''
  );
});
const statusText = computed(() => {
  const p = photo.value;
  if (!p) return '';
  return (
    { passed: '已验证', mismatch: '不符', failed: '失败', pending: '识别中', skipped: '免验证' }[
      p.verify_status
    ] || p.verify_status
  );
});

// step 前后翻页。
function step(d) {
  const next = props.index + d;
  if (next < 0 || next >= props.photos.length) return;
  emit('update:index', next);
}

// onKey 左右方向键翻页、Esc 关闭。
function onKey(ev) {
  if (!props.open) return;
  if (ev.key === 'Escape') close();
  else if (ev.key === 'ArrowLeft') step(-1);
  else if (ev.key === 'ArrowRight') step(1);
}
window.addEventListener('keydown', onKey);
onBeforeUnmount(() => window.removeEventListener('keydown', onKey));

// open 变化清错误。
watch(
  () => props.open,
  (val) => {
    if (val) error.value = '';
  }
);

// download 单张下载。
async function download() {
  if (!photo.value) return;
  error.value = '';
  try {
    await downloadPhoto(photo.value.id, `photo-${photo.value.id}.jpg`);
  } catch (e) {
    error.value = e.message || '下载失败';
  }
}

// resync 商旅照片重新同步（任务化：opId 交父视图挂卡片任务锁）。
async function resync() {
  if (!photo.value || resyncing.value) return;
  error.value = '';
  resyncing.value = true;
  try {
    const data = await resyncPhoto(photo.value.id);
    MessagePlugin.success('已发起重新同步');
    emit('op-started', { opId: data && data.opId, entryId: props.entryId, kind: 'resync' });
  } catch (e) {
    error.value = e.message || '重试失败';
  } finally {
    resyncing.value = false;
  }
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
.wl-lb-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.72);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1200;
}
.wl-lb {
  display: flex;
  gap: 16px;
  width: min(1080px, calc(100vw - 48px));
  max-height: calc(100vh - 64px);
  background: var(--tk-card, #fff);
  border-radius: 12px;
  padding: 14px;
  overflow: auto;
}
.wl-lb-imgbox {
  position: relative;
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--tk-fill);
  border-radius: 8px;
}
.wl-lb-img {
  max-width: 100%;
  max-height: calc(100vh - 120px);
  object-fit: contain;
}
.wl-lb-nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  width: 36px;
  height: 36px;
  border: none;
  border-radius: 50%;
  background: rgba(29, 33, 41, 0.55);
  color: #fff;
  font-size: 20px;
  cursor: pointer;
}
.wl-lb-nav:disabled {
  opacity: 0.3;
  cursor: default;
}
.wl-lb-nav.prev {
  left: 10px;
}
.wl-lb-nav.next {
  right: 10px;
}
.wl-lb-count {
  position: absolute;
  right: 10px;
  bottom: 8px;
  background: rgba(29, 33, 41, 0.55);
  color: #fff;
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
}
.wl-lb-info {
  width: min(320px, 34vw);
  flex-shrink: 0;
}
.wl-lb-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.wl-lb-line {
  display: flex;
  gap: 8px;
  font-size: 13px;
  color: var(--tk-ink);
  padding: 4px 0;
}
.wl-lb-line .field-label {
  width: 62px;
  flex-shrink: 0;
}
.wl-lb-acts {
  margin-top: 12px;
  display: flex;
  gap: 10px;
}
.wl-lb-close {
  position: fixed;
  top: 18px;
  right: 22px;
  width: 34px;
  height: 34px;
  border: none;
  border-radius: 50%;
  background: rgba(29, 33, 41, 0.55);
  color: #fff;
  font-size: 15px;
  cursor: pointer;
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
@media (max-width: 760px) {
  .wl-lb {
    flex-direction: column;
  }
  .wl-lb-info {
    width: 100%;
  }
}
</style>
