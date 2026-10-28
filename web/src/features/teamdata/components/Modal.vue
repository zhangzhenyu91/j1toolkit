<template>
  <div class="td-mask" @click.self="$emit('close')">
    <div class="td-modal" :style="{ width: typeof width === 'number' ? width + 'px' : width }">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">{{ title }}</span>
          <button type="button" class="m-x" aria-label="关闭" @click="$emit('close')">
            <span v-html="icon('close', 14)"></span>
          </button>
        </div>
        <slot></slot>
        <div v-if="$slots.footer" class="m-foot">
          <slot name="footer"></slot>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { icon } from '../helpers.js';

// 旧版 Shade.modal 同构弹层壳：遮罩 + 560/420 宽卡片 + 标题行（含关闭）+ 内容 + 底部按钮行。
// 插槽内容用 .f-lab / .f-sel / .dck 表单件（teamdata.css 注入）；footer 插槽放 取消 / 保 存 类按钮。
defineProps({
  title: { type: String, required: true },
  width: { type: [Number, String], default: 420 },
});
defineEmits(['close']);
</script>

<style scoped>
.td-mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: var(--overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
  animation: tdMaskIn 0.3s var(--tk-ease) both;
}
.td-modal {
  background: var(--td-bg-color-container, #fff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
  animation: tdModalIn 0.35s var(--tk-ease) both;
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
.m-scroll::-webkit-scrollbar {
  width: 5px;
}
.m-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.m-scroll::-webkit-scrollbar-track {
  background: transparent;
}
/* 深色滚动条（弹层随 .td-page token 翻转，滚动条色值单独按深色口径覆盖） */
@media (prefers-color-scheme: dark) {
  .m-scroll {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .m-scroll::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
  .m-scroll::-webkit-scrollbar-thumb:hover {
    background: rgba(255, 255, 255, 0.34);
  }
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
  background: var(--td-bg-color-container, #fff);
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
  transition:
    background 0.15s,
    color 0.15s;
}
.m-x:hover {
  background: var(--tk-ink);
  color: var(--td-bg-color-container, #fff);
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
@keyframes tdMaskIn {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
@keyframes tdModalIn {
  from {
    transform: translateY(16px) scale(0.98);
  }
  to {
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .td-mask,
  .td-modal {
    animation-duration: 0.001s !important;
  }
}
</style>
