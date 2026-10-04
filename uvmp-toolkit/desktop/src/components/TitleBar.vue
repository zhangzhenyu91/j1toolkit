<template>
  <div class="titlebar" @dblclick="onToggleMax">
    <div class="tb-left">
      <img class="tb-logo" :src="iconUrl" alt="" draggable="false">
      <span class="tb-title">Shade 壹匣 - 内网</span>
      <span class="tb-ver">v{{ ver }}</span>
    </div>
    <div class="tb-right">
      <button class="tb-btn" title="最小化" @click="minimize"><ShadeIcon name="win-min" :size="16" /></button>
      <button class="tb-btn" :title="maximized ? '还原' : '最大化'" @click="onToggleMax">
        <ShadeIcon :name="maximized ? 'win-restore' : 'win-max'" :size="14" />
      </button>
      <button class="tb-btn tb-close" title="关闭" @click="close"><ShadeIcon name="close" :size="16" /></button>
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import ShadeIcon from './ShadeIcon.vue'
import iconUrl from '../assets/icon.png'

const maximized = ref(false)
const ver = ref('')
const minimize = () => window.core.winMinimize()
const onToggleMax = () => window.core.winToggleMax()
const close = () => window.core.winClose()

let off = null
onMounted(() => {
  off = window.core.onMaxChange((v) => { maximized.value = v })
  window.core.appVersion().then((v) => { ver.value = v || '' }).catch(() => {})
})
onUnmounted(() => { if (off) off() })
</script>
