<template>
  <div class="it-rename">
    <div class="it-ins-head">
      <b>重命名</b>
      <button
        type="button"
        class="it-iconbtn it-ins-x"
        aria-label="退出重命名"
        @click="$emit('cancel')"
      >
        <ITIcon name="close" :size="15" />
      </button>
    </div>

    <div class="it-ins-body">
      <p class="it-rename-intro">
        只编辑<b>主文件名</b>，扩展名<b>{{ extLabel }}</b
        >固定保留；快捷短语插入到当前光标位置，不替换整个文件名。
      </p>

      <label class="it-edit-label" for="it-name-input">文件名</label>
      <div class="it-name-edit">
        <input
          id="it-name-input"
          ref="inputEl"
          v-model="baseName"
          class="it-input"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :maxlength="120"
          aria-describedby="it-rename-hint"
          @keydown.enter.prevent="submit"
          @keydown.esc.prevent="$emit('cancel')"
          @keyup="syncCaret"
          @click="syncCaret"
          @select="syncCaret"
        />
        <span class="it-ext-fixed">{{ extLabel }}</span>
      </div>
      <p id="it-rename-hint" class="it-hint">{{ hintText }}</p>

      <div class="it-quick-head">
        <span>快捷输入</span>
        <em>点击插入光标处</em>
      </div>
      <div class="it-qchips">
        <button
          v-for="phrase in phrases"
          :key="phrase"
          type="button"
          class="it-qchip"
          @mousedown.prevent="insertAtCaret(phrase)"
        >
          {{ phrase }}
        </button>
        <span v-if="!phrases.length" class="it-qempty">设置中还没有快捷短语</span>
      </div>
      <div class="it-insert-status">{{ insertStatus }}</div>

      <div v-if="feedback" class="it-fb" :class="'is-' + feedback.tone" role="alert">
        <ITIcon :name="feedback.tone === 'warn' ? 'warn' : 'error'" :size="15" />
        <div>
          <div class="it-fb-t">{{ feedback.title }}</div>
          <div class="it-fb-b">{{ feedback.body }}</div>
        </div>
      </div>

      <div class="it-rename-actions">
        <button type="button" class="it-btn is-ghost" @click="$emit('cancel')">取消</button>
        <button type="button" class="it-btn is-primary" :disabled="saving" @click="submit">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </div>

      <div class="it-meta-compact">
        <dl class="it-meta">
          <div class="it-mrow">
            <dt>位置</dt>
            <dd>{{ elidedPath }}</dd>
          </div>
          <div class="it-mrow">
            <dt>修改时间</dt>
            <dd class="it-num">{{ mtimeText }}</dd>
          </div>
        </dl>
        <p class="it-hint">改名后仅更新文件名，不改变文件内容、修改时间与目录内其他文件。</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, ref, watch } from 'vue';
import ITIcon from './ITIcon.vue';
import { elidePath, formatMtime } from '../lib/format.mjs';
import {
  baseNameOf,
  extNameOf,
  isSameBaseName,
  renameFeedback,
  validateBaseName,
} from '../lib/rename.mjs';
import { insertPhrase } from '../lib/viewer.mjs';

const props = defineProps({
  file: { type: Object, required: true },
  phrases: { type: Array, default: () => [] },
  saving: { type: Boolean, default: false },
  errorCode: { type: String, default: '' },
  errorMessage: { type: String, default: '' },
});
const emit = defineEmits(['cancel', 'submit', 'notify']);

const inputEl = ref(null);
const baseName = ref(baseNameOf(props.file.name));
const caret = ref(0);
const localError = ref(null);

const extLabel = computed(() => extNameOf(props.file.name) || '.');
const elidedPath = computed(() => elidePath(props.file.path, 30));
const mtimeText = computed(() => formatMtime(props.file.mtime));

// 主名没改就不提交：不发改名 RPC、不动文件，只在输入框下方就地说明
const unchanged = computed(() => isSameBaseName(props.file.name, baseName.value));
const hintText = computed(() =>
  unchanged.value
    ? '名称未变化：保存不会改动文件。Enter 保存 · Esc 取消'
    : 'Enter 保存 · Esc 取消 · 扩展名不参与编辑'
);

const feedback = computed(() => {
  if (localError.value) return localError.value;
  if (props.errorCode || props.errorMessage) {
    const mapped = renameFeedback(props.errorCode, props.errorMessage);
    const tone =
      props.errorCode === 'FILE_NOT_FOUND' || props.errorCode === 'IMG_RENAME_DENIED'
        ? 'warn'
        : 'error';
    return { title: mapped.title, body: mapped.body, tone };
  }
  return null;
});

const insertStatus = computed(() => {
  if (!props.phrases.length) return '可在设置 → 快捷输入里添加短语';
  return `光标位于第 ${caret.value} 个字符后，短语将插入此处。`;
});

function syncCaret() {
  const el = inputEl.value;
  if (!el) return;
  caret.value = typeof el.selectionStart === 'number' ? el.selectionStart : caret.value;
}

/** 快捷短语插入光标处（有选区则替换选区），插入后光标落在短语末尾。 */
function insertAtCaret(phrase) {
  const el = inputEl.value;
  const start = el && typeof el.selectionStart === 'number' ? el.selectionStart : caret.value;
  const end = el && typeof el.selectionEnd === 'number' ? el.selectionEnd : start;
  const result = insertPhrase({ value: baseName.value, start, end, phrase });
  baseName.value = result.value;
  caret.value = result.caret;
  localError.value = null;
  nextTick(() => {
    const node = inputEl.value;
    if (!node) return;
    node.focus();
    node.setSelectionRange(result.caret, result.caret);
  });
}

function submit() {
  const error = validateBaseName(baseName.value);
  if (error) {
    localError.value = { title: error.title, body: error.body, tone: 'warn' };
    return;
  }
  // 主名与当前文件一致：不发 RPC，避免一次无意义的改名往返与「已重命名」成功提示
  if (isSameBaseName(props.file.name, baseName.value)) {
    localError.value = null;
    return;
  }
  localError.value = null;
  emit('submit', { name: baseName.value.trim() });
}

watch(
  () => props.file.path,
  () => {
    baseName.value = baseNameOf(props.file.name);
    caret.value = baseName.value.length;
    localError.value = null;
  }
);

watch(
  () => baseName.value,
  () => {
    if (localError.value) localError.value = null;
  }
);

onMounted(() => {
  nextTick(() => {
    const node = inputEl.value;
    if (!node) return;
    node.focus();
    const at = baseNameOf(props.file.name).length;
    node.setSelectionRange(at, at);
    caret.value = at;
  });
});

defineExpose({ focus: () => inputEl.value && inputEl.value.focus() });
</script>
