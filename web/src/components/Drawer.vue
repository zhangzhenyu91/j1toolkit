<template>
  <!-- 共享右侧抽屉壳（对照微信智能应答规则管理抽屉）：遮罩 + 右侧面板 + 标题行（含关闭）+ 滚动内容。
       遮罩淡入 + 内容上浮/缩放进出场（overlay-motion）；Escape / 关闭按钮 / 可选点遮罩关闭；
       打开期间锁定页面滚动并圈定焦点到面板，关闭后归还焦点；滚动只发生在内层 .tk-drawer-scroll
       （不带 transform），内容滚动不触发缩放跳动。 -->
  <Teleport to="body">
    <div v-if="rendered" class="tk-drawer" :class="phaseClass">
      <div class="tk-drawer-mask ov-mask" @click="onMaskClick"></div>
      <div
        ref="panelRef"
        class="tk-drawer-panel ov-panel"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        tabindex="-1"
        :style="panelStyle"
      >
        <div class="tk-drawer-head">
          <span class="tk-drawer-title">{{ title }}</span>
          <button type="button" class="tk-drawer-x" aria-label="关闭" @click="requestClose">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              aria-hidden="true"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="tk-drawer-scroll">
          <slot></slot>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useOverlayMotion } from './overlay-motion.js';
import { useModalBehavior } from './modal-behavior.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import './overlay-motion.css';

const props = defineProps({
  // visible 受控显隐（v-model:visible）：父级置 false 也播放退出动画，组件保持挂载等动画完成后再收起 DOM。
  visible: { type: Boolean, default: false },
  title: { type: String, default: '' },
  width: { type: [Number, String], default: 560 },
  // closeOnOverlayClick 点遮罩是否关闭：表单类默认不关，防误点丢输入。
  closeOnOverlayClick: { type: Boolean, default: false },
});
const emit = defineEmits(['update:visible', 'close']);

const open = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);

const panelRef = ref(null);
const panelStyle = computed(() => ({
  width: typeof props.width === 'number' ? props.width + 'px' : props.width,
}));

// requestClose 请求关闭：本地先播退出动画，同时通知父级（v-model:visible / close）。
function requestClose() {
  if (!open.value) return;
  open.value = false;
  emit('update:visible', false);
  emit('close');
}

function onMaskClick() {
  if (props.closeOnOverlayClick) requestClose();
}

// 打开：锁页面滚动、圈定焦点、监听 Escape / Tab；关闭（含卸载）：逐项还原（共享弹层行为）。
useModalBehavior(rendered, { panelRef, onEscape: requestClose });
</script>

<style scoped>
.tk-drawer {
  position: fixed;
  inset: 0;
  z-index: 320;
}
.tk-drawer-mask {
  position: absolute;
  inset: 0;
  background: var(--overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
}
.tk-drawer-panel {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  max-width: 92vw;
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-container, #fff);
  border-left: 1px solid var(--tk-line);
  box-shadow: var(--tk-shadow-hover, 0 8px 20px rgba(0, 0, 0, 0.08));
  outline: none;
}
.tk-drawer-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 15px 18px 12px;
  border-bottom: 1px solid var(--tk-line);
}
.tk-drawer-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.tk-drawer-x {
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
.tk-drawer-x:hover {
  background: var(--tk-ink);
  color: var(--td-bg-color-container, #fff);
}
.tk-drawer-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 14px 18px 18px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.tk-drawer-scroll::-webkit-scrollbar {
  width: 5px;
}
.tk-drawer-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.tk-drawer-scroll::-webkit-scrollbar-track {
  background: transparent;
}
/* 深色滚动条（弹层随主题 token 翻转，滚动条色值单独按深色口径覆盖） */
@media (prefers-color-scheme: dark) {
  .tk-drawer-scroll {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .tk-drawer-scroll::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
  .tk-drawer-scroll::-webkit-scrollbar-thumb:hover {
    background: rgba(255, 255, 255, 0.34);
  }
}
</style>
