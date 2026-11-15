<template>
  <div class="titlebar" :data-maximized="maximized ? '1' : '0'" @dblclick="toggleMax">
    <div class="tb-left">
      <img class="tb-logo" :src="iconUrl" alt="" draggable="false" />
      <span class="tb-title">{{ title }}</span>
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
import { computed, onMounted, onUnmounted, ref } from 'vue';
import ShadeIcon from './ShadeIcon.vue';
import iconUrl from '../assets/icon.png';

// 主窗口与独立图片窗口共用同一枚 48px 顶栏：窗口能力由调用方注入（图片窗口是 window.imageTools），
// 未注入时回落到主客户端的 core 桥接，主窗口行为保持不变。
const props = defineProps({
  api: { type: Object, default: null },
  title: { type: String, default: 'Shade 壹匣 · 内网工具箱' },
});

const api = computed(() => props.api || window.core);
const maximized = ref(false);
const ver = ref('');
const minimize = () => api.value.winMinimize();
const toggleMax = () => api.value.winToggleMax();
const close = () => api.value.winClose();

let off = null;
onMounted(async () => {
  off = api.value.onMaxChange((v) => {
    maximized.value = !!v;
  });
  try {
    if (api.value.winIsMax) maximized.value = !!(await api.value.winIsMax());
  } catch (_e) {
    /* 旧 preload 没有该 IPC 时保留事件同步 */
  }
  Promise.resolve(api.value.appVersion())
    .then((v) => {
      ver.value = v || '';
    })
    .catch(() => {});
});
onUnmounted(() => {
  if (off) off();
});
</script>
