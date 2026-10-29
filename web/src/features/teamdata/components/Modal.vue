<template>
  <div v-if="rendered" class="td-mask ov-mask" :class="phaseClass" @click.self="requestClose">
    <div
      class="td-modal ov-panel"
      :style="{ width: typeof width === 'number' ? width + 'px' : width }"
    >
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">{{ title }}</span>
          <button type="button" class="m-x" aria-label="关闭" @click="requestClose">
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
import { ref, watch } from 'vue';
import { icon } from '../helpers.js';
import { motionMs, useOverlayMotion } from '../../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../../components/overlay-motion.css';

// 旧版 Shade.modal 同构弹层壳：遮罩 + 560/420 宽卡片 + 标题行（含关闭）+ 内容 + 底部按钮行。
// 插槽内容用 .f-lab / .f-sel / .dck 表单件（teamdata.css 注入）；footer 插槽放 取消 / 保 存 类按钮。
// 进出场统一「遮罩淡入 + 内容上浮/缩放」200ms（overlay-motion），reduced-motion 自动降级。
const props = defineProps({
  title: { type: String, required: true },
  width: { type: [Number, String], default: 420 },
  // visible 受控显隐（可选）：传入后父级直接置 false 也能播退出动画（建议去掉 v-if 改用 :visible）；
  // 不传（旧 v-if 用法）挂载即打开，关闭点击播完退出动画再通知父级卸载。
  visible: { type: Boolean, default: undefined },
});
const emit = defineEmits(['close', 'update:visible']);

const open = ref(props.visible !== false);
watch(
  () => props.visible,
  (v) => {
    if (v !== undefined) open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);

// requestClose 请求关闭：本地先播退出动画，再按受控与否通知父级。
function requestClose() {
  if (!open.value) return;
  open.value = false;
  if (props.visible === undefined) {
    // 非受控（v-if 父级）：等退出动画播完再通知卸载，保证退出过渡可见
    window.setTimeout(() => emit('close'), motionMs());
  } else {
    emit('close');
    emit('update:visible', false);
  }
}
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
</style>
