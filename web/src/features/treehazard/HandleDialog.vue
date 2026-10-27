<template>
  <t-dialog
    v-model:visible="visible"
    :header="ids.length > 1 ? `批量处理（${ids.length} 条）` : '处理隐患'"
    :close-on-overlay-click="false"
    :confirm-btn="{ content: '确定处理', theme: 'primary', loading: submitting }"
    cancel-btn="取消"
    @confirm="onConfirm"
  >
    <div class="hd-form">
      <label class="f-lab">处理人</label>
      <t-input v-model="handlerName" :maxlength="32" placeholder="处理人" />
      <label class="f-lab">处理情况</label>
      <t-textarea v-model="handleNote" :maxlength="64" placeholder="填写处理情况，如：削枝" />
      <div class="chips">
        <button v-for="q in quickNotes" :key="q" class="chip" type="button" @click="handleNote = q">
          {{ q }}
        </button>
      </div>
      <label class="f-lab">处理时间</label>
      <t-date-picker v-model="handledAt" format="YYYY-MM-DD" allow-input clearable />
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
  gap: 8px;
}
.f-lab {
  font-size: 13px;
  color: var(--tk-sub);
  margin-top: 4px;
}
.chips {
  display: flex;
  gap: 8px;
}
.chip {
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
  border-radius: 4px;
  padding: 2px 10px;
  cursor: pointer;
}
.chip:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
</style>
