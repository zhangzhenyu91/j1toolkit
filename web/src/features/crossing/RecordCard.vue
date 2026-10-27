<template>
  <div class="card rec" :class="{ fold: !expanded }">
    <!-- 摘要行：区段 + 交叉垂距 + 隐患标签 + 折叠 -->
    <div class="rec-top">
      <b class="rec-sec">{{ dash(record.section) }}</b>
      <span class="rec-vert" title="交叉垂直距离">垂距 {{ fmtM(record.cross_vertical) }}</span>
      <span v-if="isHazard" class="rec-tag hz" title="是否存在隐患">有隐患</span>
      <button class="rec-fold" :title="expanded ? '折叠' : '展开'" @click="expanded = !expanded">
        <svg
          viewBox="0 0 13 13"
          width="13"
          height="13"
          :style="{ transform: expanded ? 'rotate(180deg)' : '' }"
        >
          <path d="M2.5 5l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" />
        </svg>
      </button>
    </div>

    <!-- 次级行：跨越类型 / 交叉角 -->
    <div class="rec-subline">
      <span v-if="crossingType" class="rec-sub">跨越 {{ crossingType }}</span>
      <span class="rec-dist">交叉角 {{ fmtDeg(record.cross_angle) }}</span>
    </div>

    <!-- 展开区：紧凑信息行 + 来源信息行 + 操作 -->
    <div v-show="expanded" class="rec-more">
      <div v-if="infoItems.length" class="rf-row">
        <span v-for="it in infoItems" :key="it.key" class="rf"
          ><i>{{ it.label }}</i
          >{{ it.value }}</span
        >
      </div>
      <div v-if="srcItems.length" class="rec-src">
        <span v-for="it in srcItems" :key="it.label"
          >{{ it.label }} <b>{{ it.value }}</b></span
        >
      </div>
      <div v-if="isManager" class="rec-acts">
        <t-button theme="danger" variant="outline" size="small" @click="$emit('remove', record.id)">
          删除
        </t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import {
  COMPACT_FIELDS,
  SOURCE_TEXT,
  dash,
  fmtDeg,
  fmtField,
  fmtM,
  fmtDate,
  hasHazard,
} from './util.js';

const props = defineProps({
  record: { type: Object, required: true },
  isManager: { type: Boolean, default: false },
});
defineEmits(['remove']);

const expanded = ref(false);

const isHazard = computed(() => hasHazard(props.record));
const crossingType = computed(() => String(props.record.crossing_type || '').trim());

// 摘要行已占位的字段（区段/垂距/类型/交叉角）不重复展示
const infoItems = computed(() =>
  COMPACT_FIELDS.filter(
    (f) => !['section', 'cross_vertical', 'crossing_type', 'cross_angle'].includes(f[0])
  )
    .map(([key, label]) => {
      const raw = props.record[key];
      const v = String(raw == null ? '' : raw).trim();
      return v ? { key, label, value: fmtField(key, raw) } : null;
    })
    .filter(Boolean)
);

const srcItems = computed(() => {
  const out = [];
  const src = String(props.record.source || '').trim();
  if (src) out.push({ label: '来源', value: SOURCE_TEXT[src] || src });
  if (String(props.record.crane_session_id || '').trim())
    out.push({ label: '排查会话', value: props.record.crane_session_id });
  if (String(props.record.crane_record_id || '').trim())
    out.push({ label: '排查记录', value: props.record.crane_record_id });
  if (String(props.record.synced_at || '').trim())
    out.push({ label: '推送时间', value: fmtDate(props.record.synced_at) });
  if (String(props.record.created_at || '').trim())
    out.push({ label: '创建', value: fmtDate(props.record.created_at) });
  if (String(props.record.updated_at || '').trim())
    out.push({ label: '更新', value: fmtDate(props.record.updated_at) });
  return out;
});
</script>

<style scoped>
.rec {
  padding: 14px 16px;
}
.rec.fold .rec-more {
  display: none;
}
.rec-top {
  display: flex;
  align-items: center;
  gap: 10px;
}
.rec-sec {
  font-size: 15px;
}
.rec-vert {
  font-size: 12.5px;
  color: var(--tk-sub);
  font-variant-numeric: tabular-nums;
}
.rec-tag {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
}
.rec-tag.hz {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.rec-fold {
  margin-left: auto;
  border: none;
  background: none;
  color: var(--tk-ph);
  cursor: pointer;
  display: inline-flex;
  padding: 4px;
}
.rec-subline {
  margin-top: 6px;
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 12.5px;
  color: var(--tk-sub);
}
.rec-more {
  margin-top: 12px;
  border-top: 1px solid var(--tk-line);
  padding-top: 12px;
}
.rf-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 18px;
}
.rf {
  font-size: 12.5px;
  color: var(--tk-ink);
}
.rf i {
  font-style: normal;
  color: var(--tk-ph);
  margin-right: 6px;
}
.rec-src {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px 18px;
  font-size: 12px;
  color: var(--tk-ph);
}
.rec-src b {
  color: var(--tk-sub);
  font-weight: 600;
}
.rec-acts {
  margin-top: 12px;
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>
