<template>
  <t-card class="task-result" :class="{ failed: Boolean(outcome.failed) }">
    <div class="result-head">
      <span class="status-line">
        <span class="status-dot" :class="theme" />
        <strong>{{ outcome.status || '尚未开始' }}</strong>
      </span>
      <t-button
        v-if="outcome.artifact && outcome.artifact.path"
        size="small"
        variant="outline"
        @click="openArtifact"
      >
        {{ outcome.artifact.label || '打开产物目录' }}
      </t-button>
    </div>
    <p v-if="outcome.summary" class="result-summary">{{ outcome.summary }}</p>
    <button class="log-toggle" :class="{ open: expanded }" @click="expanded = !expanded">
      <span>排错日志（供技术支持）</span>
      <span class="chevron">{{ expanded ? '−' : '+' }}</span>
    </button>
    <div v-show="expanded" ref="logEl" class="logview" data-log-expanded="1">
      {{ logs.length ? logs.join('\n') : '尚无日志' }}
    </div>
  </t-card>
</template>

<script setup>
import { computed, nextTick, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { call } from '../api';
import { shouldExpandLog, statusTheme } from '../lib/task-result.mjs';

const props = defineProps({
  outcome: { type: Object, default: () => ({}) },
  logs: { type: Array, default: () => [] },
  defaultExpanded: { type: Boolean, default: false },
  // 有任务号时一并带给核心：核心按该次档案登记的产物目录判定，旧档案也能精确打开；
  // 页面拿不到任务号的场景（如壳层自检）留空，退化为只传路径的老行为。
  jobId: { type: String, default: '' },
});

const expanded = ref(props.defaultExpanded);
const logEl = ref(null);
const theme = computed(() => statusTheme(props.outcome.status));

watch(
  () => props.outcome,
  (outcome) => {
    if (props.defaultExpanded || shouldExpandLog(outcome)) {
      expanded.value = true;
      nextTick(() => {
        if (logEl.value && logEl.value.scrollIntoView) {
          logEl.value.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }
      });
    } else {
      expanded.value = false;
    }
  },
  { deep: true }
);

async function openArtifact() {
  const path = props.outcome?.artifact?.path;
  if (!path) return;
  const params = { path };
  if (props.jobId) params.job_id = props.jobId;
  try {
    await call('openPath', params);
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}
</script>
