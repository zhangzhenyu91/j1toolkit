<template>
  <!-- 旧版处理弹层（单条 / 批量共用）：处理人 / 处理情况 + 快捷胶囊 / 处理时间 + 取消·确定处理。
       自绘居中弹层壳（对照共享 Drawer/useOverlayMotion 口径）：遮罩淡入 + 面板上浮/缩放 200ms 进出场，
       Escape / 关闭按钮收起，点遮罩不关（防误点丢输入）；打开期间锁页面滚动。 -->
  <Teleport to="body">
    <div v-if="rendered" class="hd-ctx" :class="phaseClass">
      <div class="hd-mask ov-mask"></div>
      <div
        ref="panelRef"
        class="hd-dialog ov-panel"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        :aria-label="ids.length > 1 ? `批量处理（${ids.length} 条）` : '处理隐患'"
      >
        <div class="hd-head">
          <span class="hd-title">{{
            ids.length > 1 ? `批量处理（${ids.length} 条）` : '处理隐患'
          }}</span>
          <button type="button" class="hd-x" aria-label="关闭" @click="close">
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
        <div class="hd-body">
          <div class="hd-form">
            <label class="f-lab">处理人</label>
            <input v-model="handlerName" class="f-sel" maxlength="32" autocomplete="off" />
            <label class="f-lab">处理情况</label>
            <textarea
              v-model="handleNote"
              class="f-ta"
              maxlength="64"
              placeholder="填写处理情况，如：削枝"
            ></textarea>
            <div class="chips">
              <button
                v-for="q in quickNotes"
                :key="q"
                class="chip"
                type="button"
                @click="handleNote = q"
              >
                {{ q }}
              </button>
            </div>
            <label class="f-lab">处理时间</label>
            <input v-model="handledAt" type="date" class="f-sel" @click="openPicker" />
            <div class="m-foot">
              <button class="btn btn-sm" type="button" :disabled="submitting" @click="close">
                取消
              </button>
              <button
                class="btn btn-sm btn-primary"
                type="button"
                :disabled="submitting"
                @click="onConfirm"
              >
                {{ submitting ? '提交中…' : '确定处理' }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { ref, watch } from 'vue';
import { useOverlayMotion } from '../../components/overlay-motion.js';
import { useModalBehavior } from '../../components/modal-behavior.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';
import { currentUserObj, todayStr } from './util.js';

const props = defineProps({
  visible: { type: Boolean, default: false },
  ids: { type: Array, default: () => [] },
});
const emit = defineEmits(['update:visible', 'submit']);

const visible = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) {
      const u = currentUserObj();
      handlerName.value = u.nickname || u.username || '';
      handleNote.value = '';
      handledAt.value = todayStr();
      submitting.value = false;
    }
  }
);
watch(visible, (v) => emit('update:visible', v));

// 进出场编排（对照共享 Drawer/useOverlayMotion：遮罩淡入 + 面板上浮/缩放 200ms）。
const { rendered, phaseClass } = useOverlayMotion(visible);

// close 关闭（Escape / 关闭按钮 / 取消）：置 false 播退出动画，父级经 update:visible 收起。
function close() {
  if (!visible.value) return;
  visible.value = false;
}

// 打开期间锁页面滚动 + 圈定焦点 + Escape 收起（共享弹层行为；与 Drawer 混用不误解锁）。
const panelRef = ref(null);
useModalBehavior(rendered, { panelRef, onEscape: close });

const quickNotes = ['削枝', '砍伐'];
const handlerName = ref('');
const handleNote = ref('');
const handledAt = ref(todayStr());
const submitting = ref(false);

// openPicker 点日期文字区直接唤起原生日历（原生仅点图标弹出；不支持或非手势时静默回退）
function openPicker(e) {
  try {
    e.target.showPicker?.();
  } catch {
    /* 忽略：仍可点输入框右侧图标选择 */
  }
}

async function onConfirm() {
  const note = handleNote.value.trim();
  if (!note) {
    emit('submit', { error: '处理情况不能为空' });
    return;
  }
  if (submitting.value) return;
  submitting.value = true;
  emit('submit', {
    ids: props.ids,
    body: {
      handler_name: handlerName.value.trim(),
      handle_note: note,
      handled_at: handledAt.value || todayStr(),
    },
    done: () => {
      submitting.value = false;
      visible.value = false;
    },
    fail: () => {
      submitting.value = false;
    },
  });
}
</script>

<style scoped>
/* ===== 自绘居中弹层壳（对照共享 Drawer：遮罩 + 面板 + 标题行（含关闭）+ 滚动内容） ===== */
.hd-ctx {
  position: fixed;
  inset: 0;
  z-index: 320;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.hd-mask {
  position: absolute;
  inset: 0;
  background: var(--overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
}
.hd-dialog {
  position: relative;
  width: 480px;
  max-width: 94vw;
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-container, #fff);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-card);
  overflow: hidden;
}
.hd-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 15px 18px 12px;
  border-bottom: 1px solid var(--tk-line);
}
.hd-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.hd-x {
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
.hd-x:hover {
  background: var(--tk-ink);
  color: var(--td-bg-color-container, #fff);
}
.hd-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 18px 16px;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.hd-body::-webkit-scrollbar {
  width: 5px;
}
.hd-body::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.hd-body::-webkit-scrollbar-track {
  background: transparent;
}
/* 深色滚动条（弹层随主题 token 翻转，滚动条色值单独按深色口径覆盖） */
@media (prefers-color-scheme: dark) {
  .hd-body {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .hd-body::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
}
.hd-form {
  display: flex;
  flex-direction: column;
}
/* 弹层表单（旧版 f-lab / f-sel / f-ta / chips 口径） */
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-lab:first-of-type {
  margin-top: 2px;
}
.f-sel,
.f-ta {
  width: 100%;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.f-sel {
  height: 40px;
  padding: 0 10px;
}
.f-ta {
  min-height: 96px;
  padding: 10px 12px;
  line-height: 1.7;
  resize: vertical;
}
.f-sel:focus,
.f-ta:focus {
  border-color: var(--tk-brand);
  box-shadow: var(--tk-shadow-card);
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}
.chip {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  color: var(--tk-ink);
  font-size: 12.5px;
  padding: 4px 13px;
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.2s,
    color 0.2s,
    background 0.2s;
}
.chip:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 旧式按钮 ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  font-family: inherit;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn:active:not([disabled]) {
  transform: translateY(1px);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}
</style>
