<template>
  <!-- 旧版处理弹层（单条 / 批量共用）：处理人 / 处理情况 + 快捷胶囊 / 处理时间 + 取消·确定处理 -->
  <t-dialog
    v-model:visible="visible"
    :header="ids.length > 1 ? `批量处理（${ids.length} 条）` : '处理隐患'"
    :close-on-overlay-click="false"
    :confirm-btn="null"
    :cancel-btn="null"
  >
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
        <button v-for="q in quickNotes" :key="q" class="chip" type="button" @click="handleNote = q">
          {{ q }}
        </button>
      </div>
      <label class="f-lab">处理时间</label>
      <input v-model="handledAt" type="date" class="f-sel" @click="openPicker" />
      <div class="m-foot">
        <button class="btn btn-sm" type="button" :disabled="submitting" @click="visible = false">
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
  </t-dialog>
</template>

<script setup>
import { ref, watch } from 'vue';
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
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
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
