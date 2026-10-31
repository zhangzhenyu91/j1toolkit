<template>
  <div class="titlebar" :data-maximized="maximized ? '1' : '0'" @dblclick="toggleMax">
    <div class="tb-left">
      <img class="tb-logo" :src="iconUrl" alt="" draggable="false" />
      <span class="tb-title">Shade 壹匣 · 内网工具箱</span>
      <span class="tb-ver">v{{ ver }}</span>
    </div>
    <div class="tb-right">
      <button class="tb-btn" title="最小化" data-win="min" @click="minimize">
        <ShadeIcon name="win-min" :size="16" />
      </button>
      <button
        class="tb-btn"
        :title="maximized ? '还原' : '最大化'"
        data-win="max"
        @click="toggleMax"
      >
        <ShadeIcon :name="maximized ? 'win-restore' : 'win-max'" :size="14" />
      </button>
      <button class="tb-btn tb-close" title="关闭" data-win="close" @click="close">
        <ShadeIcon name="close" :size="16" />
      </button>
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue';
import ShadeIcon from './ShadeIcon.vue';
import iconUrl from '../assets/icon.png';

const maximized = ref(false);
const ver = ref('');
const minimize = () => window.core.winMinimize();
const toggleMax = () => window.core.winToggleMax();
const close = () => window.core.winClose();

let off = null;
onMounted(async () => {
  off = window.core.onMaxChange((v) => {
    maximized.value = !!v;
  });
  try {
    if (window.core.winIsMax) maximized.value = !!(await window.core.winIsMax());
  } catch (_e) {
    /* 旧 preload 没有该 IPC 时保留事件同步 */
  }
  window.core
    .appVersion()
    .then((v) => {
      ver.value = v || '';
    })
    .catch(() => {});
});
onUnmounted(() => {
  if (off) off();
});
</script>
