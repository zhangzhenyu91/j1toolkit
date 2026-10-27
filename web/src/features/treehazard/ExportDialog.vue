<template>
  <t-dialog
    v-model:visible="visible"
    header="导出统计表"
    :close-on-overlay-click="false"
    :confirm-btn="null"
    cancel-btn="取消"
  >
    <div class="ex-form">
      <label class="f-lab">导出范围</label>
      <t-radio-group v-model="range" variant="default-filled" :disabled="downloading || previewing">
        <t-radio-button
          v-if="showCurrent"
          value="line"
          :disabled="!curLineName"
          :title="curLineName ? '' : '未匹配线路聚合多条线路，不支持单线路导出'"
        >
          当前线路
        </t-radio-button>
        <t-radio-button value="all">全部线路</t-radio-button>
        <t-radio-button value="pick" :disabled="!lines.length">选择线路</t-radio-button>
      </t-radio-group>
      <t-select
        v-if="range === 'pick'"
        v-model="pickedLine"
        :options="lineOptions"
        placeholder="选择线路"
        filterable
      />
      <label class="f-lab">按线路分表</label>
      <t-checkbox v-model="split" :disabled="range !== 'all' || downloading || previewing">
        每条线路一张工作表（仅全部线路可用）
      </t-checkbox>
    </div>
    <template #footer>
      <t-button variant="outline" :disabled="downloading || previewing" @click="visible = false"
        >取消</t-button
      >
      <t-button variant="outline" :loading="previewing" @click="onPreview">在线预览</t-button>
      <t-button theme="primary" :loading="downloading" @click="onDownload">下载</t-button>
    </template>
  </t-dialog>
</template>

<script setup>
import { computed, ref, watch } from 'vue';

const props = defineProps({
  visible: { type: Boolean, default: false },
  lines: { type: Array, default: () => [] }, // [{voltage_level,line_name}]
  showCurrent: { type: Boolean, default: false }, // 台账视图（含当前线路项）
  curLineName: { type: String, default: '' }, // 当前线路名（'__unmatched__' 伪组传 ''）
});
const emit = defineEmits(['update:visible', 'download', 'preview']);

const visible = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) {
      range.value = props.showCurrent && props.curLineName ? 'line' : 'all';
      pickedLine.value = '';
      split.value = false;
      downloading.value = false;
      previewing.value = false;
    }
  }
);
watch(visible, (v) => emit('update:visible', v));

const range = ref('all');
const pickedLine = ref('');
const split = ref(false);
const downloading = ref(false);
const previewing = ref(false);

const lineOptions = computed(() =>
  props.lines.map((l) => ({
    label: `${l.voltage_level || ''}kV ${l.line_name}`.trim(),
    value: l.line_name,
  }))
);

function buildQuery() {
  if (range.value === 'line') {
    if (!props.curLineName) return null;
    return `?line=${encodeURIComponent(props.curLineName)}`;
  }
  if (range.value === 'pick') {
    if (!pickedLine.value) return null;
    return `?line=${encodeURIComponent(pickedLine.value)}`;
  }
  return split.value ? '?split=1' : '';
}

function onDownload() {
  const query = buildQuery();
  if (query === null) {
    emit('download', { error: '请选择线路' });
    return;
  }
  downloading.value = true;
  emit('download', {
    query,
    done: () => {
      downloading.value = false;
      visible.value = false;
    },
    fail: () => {
      downloading.value = false;
    },
  });
}

// onPreview 在线预览：由视图取 /export/preview 地址打开（OO 承载页或微软查看器）。
function onPreview() {
  const query = buildQuery();
  if (query === null) {
    emit('preview', { error: '请选择线路' });
    return;
  }
  previewing.value = true;
  emit('preview', {
    query,
    done: () => {
      previewing.value = false;
      visible.value = false;
    },
    fail: () => {
      previewing.value = false;
    },
  });
}
</script>

<style scoped>
.ex-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.f-lab {
  font-size: 13px;
  color: var(--tk-sub);
}
</style>
