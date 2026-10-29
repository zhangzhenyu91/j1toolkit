// 遮罩淡入 + 内容上浮/缩放 进入/退出编排（Modal / Drawer 共用，配合 overlay-motion.css）。
import { computed, onBeforeUnmount, ref, watch } from 'vue';

// OVERLAY_MOTION_MS 进入/退出动画时长（设计口径 120–240ms，统一取 200ms）。
export const OVERLAY_MOTION_MS = 200;

// motionMs 当前实际动画时长：prefers-reduced-motion 时降级为 0，只留一拍收起。
export function motionMs() {
  const reduce =
    typeof window !== 'undefined' &&
    !!window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  return reduce ? 0 : OVERLAY_MOTION_MS;
}

// useOverlayMotion 以显隐源驱动 DOM 存在（rendered）与动画阶段（phaseClass：ov-enter / ov-exit / ''）。
// 阶段类播完即摘除，静止时面板不残留 transform，内容滚动不会触发缩放跳动。
export function useOverlayMotion(visible) {
  const rendered = ref(false);
  const phase = ref('');
  const phaseClass = computed(() => (phase.value ? 'ov-' + phase.value : ''));
  let timer = 0;

  watch(
    visible,
    (v) => {
      window.clearTimeout(timer);
      if (v) {
        rendered.value = true;
        phase.value = 'enter';
        timer = window.setTimeout(() => (phase.value = ''), motionMs());
      } else if (rendered.value) {
        phase.value = 'exit';
        timer = window.setTimeout(() => {
          rendered.value = false;
          phase.value = '';
        }, motionMs());
      }
    },
    { immediate: true }
  );

  onBeforeUnmount(() => window.clearTimeout(timer));
  return { rendered, phaseClass };
}
