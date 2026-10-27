<template>
  <div class="card rec" :class="{ done: isDone, fold: !expanded }">
    <!-- 摘要行：批量勾选 + 区段 + 垂距 + 状态标签 + 折叠 -->
    <div class="rec-top">
      <span
        v-if="batchOn"
        class="rec-ck"
        :class="{ on: selected, dis: isDone }"
        :title="isDone ? '已处理记录不可选' : ''"
        @click="onCheck"
      >
        <svg v-if="selected" viewBox="0 0 12 12" width="12" height="12">
          <path d="M2 6l3 3 5-6" fill="none" stroke="currentColor" stroke-width="1.8" />
        </svg>
      </span>
      <b class="rec-sec">{{ dash(record.section) }}</b>
      <span class="rec-vert" :class="{ danger: isOver }" title="垂直距离">
        垂距 {{ fmtM(record.vertical) }}
      </span>
      <span v-if="isDone" class="rec-tag ok">已处理</span>
      <span v-else class="rec-tag un">未处理</span>
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

    <!-- 次级行：树种×数量 / 水平距离 / 发现时间（found_at 空值省略） -->
    <div class="rec-subline">
      <span v-if="species" class="rec-sub">
        {{ species }}{{ String(record.tree_count || '').trim() ? ' × ' + record.tree_count : '' }}
      </span>
      <span class="rec-dist">水平 {{ fmtM(record.horizontal) }}</span>
      <span v-if="foundAt" class="rec-dist">发现时间 {{ foundAt }}</span>
    </div>

    <!-- 辅证照片条：折叠态即展示（有图才渲染，只读） -->
    <div v-if="evidence.length" class="rec-evi">
      <div class="ph-zone">
        <span class="ph-lab">辅证照片</span>
        <div class="ph-list">
          <figure v-for="p in evidence" :key="p.id" class="ph" @click="openViewer(evidence, p)">
            <img :src="p.url" alt="辅证照片" loading="lazy" />
          </figure>
        </div>
      </div>
    </div>

    <!-- 展开区：紧凑信息行 + 处理信息 + 处理前/后照片区 + 操作 -->
    <div v-show="expanded" class="rec-more">
      <div class="rf-row" v-if="infoItems.length">
        <span v-for="it in infoItems" :key="it.key" class="rf"
          ><i>{{ it.label }}</i
          >{{ it.value }}</span
        >
      </div>
      <div v-if="isDone" class="rec-handled">
        <span
          >处理人 <b>{{ dash(record.handler_name) }}</b></span
        >
        <span
          >处理情况 <b>{{ dash(record.handle_note) }}</b></span
        >
        <span
          >处理时间 <b>{{ dash(record.handled_at) }}</b></span
        >
      </div>
      <div class="ph-zones">
        <div v-for="z in photoZones" :key="z.kind" class="ph-zone">
          <span class="ph-lab">{{ z.label }}</span>
          <div class="ph-list">
            <figure v-for="p in z.photos" :key="p.id" class="ph" @click="openViewer(z.photos, p)">
              <img :src="p.url" :alt="z.label" loading="lazy" />
              <button
                v-if="canDelPhoto(p)"
                class="ph-del"
                title="删除照片"
                @click.stop="$emit('remove-photo', record.id, p.id)"
              >
                ×
              </button>
            </figure>
            <button class="ph-add" title="上传照片" @click="pickFiles(z.kind)">
              <svg viewBox="0 0 16 16" width="16" height="16">
                <path d="M8 3v10M3 8h10" fill="none" stroke="currentColor" stroke-width="1.8" />
              </svg>
            </button>
          </div>
        </div>
      </div>
      <div v-if="isManager || !isDone" class="rec-acts">
        <t-button
          v-if="isManager"
          theme="danger"
          variant="outline"
          size="small"
          @click="$emit('remove', record.id)"
        >
          删除
        </t-button>
        <t-button v-if="!isDone" theme="primary" size="small" @click="$emit('handle', record.id)">
          处理
        </t-button>
      </div>
    </div>

    <!-- 照片选择（隐藏）与大图查看 -->
    <input ref="fileInput" type="file" accept="image/*" multiple hidden @change="onFiles" />
    <t-image-viewer
      v-model:visible="viewerVisible"
      :images="viewerImages"
      :default-index="viewerIndex"
    />
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import { COMPACT_FIELDS, dash, fmtM, overThreshold, readPhotoDataUrl } from './util.js';

const props = defineProps({
  record: { type: Object, required: true },
  threshold: { type: Number, default: null },
  isManager: { type: Boolean, default: false },
  meId: { type: [Number, String], default: 0 },
  batchOn: { type: Boolean, default: false },
  selected: { type: Boolean, default: false },
});

const emit = defineEmits(['handle', 'remove', 'remove-photo', 'upload', 'toggle-select']);

const expanded = ref(false);

const isDone = computed(() => props.record.status === 'handled');
const isOver = computed(() => overThreshold(props.record, props.threshold));
const species = computed(() => String(props.record.tree_species || '').trim());
const foundAt = computed(() =>
  String(props.record.found_at == null ? '' : props.record.found_at).trim()
);
const evidence = computed(() => (props.record.photos || []).filter((p) => p.kind === 'evidence'));
const photoZones = computed(() => [
  {
    kind: 'before',
    label: '处理前',
    photos: (props.record.photos || []).filter((p) => p.kind === 'before'),
  },
  {
    kind: 'after',
    label: '处理后',
    photos: (props.record.photos || []).filter((p) => p.kind === 'after'),
  },
]);
const infoItems = computed(() =>
  COMPACT_FIELDS.map(([key, label]) => {
    const v = String(props.record[key] == null ? '' : props.record[key]).trim();
    return v ? { key, label, value: v } : null;
  }).filter(Boolean)
);

function canDelPhoto(p) {
  return props.isManager || Number(p.uploaded_by) === Number(props.meId);
}

function onCheck() {
  if (props.isDone) return;
  emit('toggle-select', props.record.id);
}

const fileInput = ref(null);
let uploadKind = 'before';

function pickFiles(kind) {
  uploadKind = kind;
  fileInput.value?.click();
}

async function onFiles(e) {
  const files = Array.from(e.target.files || []).filter((f) => /^image\//i.test(f.type || ''));
  e.target.value = '';
  if (!files.length) return;
  const urls = [];
  for (const f of files) {
    urls.push(await readPhotoDataUrl(f));
  }
  emit('upload', props.record.id, uploadKind, urls);
}

const viewerVisible = ref(false);
const viewerImages = ref([]);
const viewerIndex = ref(0);

function openViewer(list, p) {
  viewerImages.value = list.map((x) => x.url);
  viewerIndex.value = Math.max(
    0,
    list.findIndex((x) => x.id === p.id)
  );
  viewerVisible.value = true;
}
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
.rec-ck {
  width: 18px;
  height: 18px;
  flex: none;
  border: 1.5px solid var(--tk-line);
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  color: #fff;
}
.rec-ck.on {
  background: var(--tk-brand);
  border-color: var(--tk-brand);
}
.rec-ck.dis {
  background: var(--tk-fill);
  cursor: not-allowed;
}
.rec-sec {
  font-size: 15px;
}
.rec-vert {
  font-size: 12.5px;
  color: var(--tk-sub);
  font-variant-numeric: tabular-nums;
}
.rec-vert.danger {
  color: var(--tk-red);
  font-weight: 600;
}
.rec-tag {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
}
.rec-tag.ok {
  background: var(--tk-green-soft);
  color: #0b8a37;
}
.rec-tag.un {
  background: var(--tk-yellow-soft);
  color: #d25f00;
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
.rec-evi {
  margin-top: 10px;
}
.ph-zones {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.ph-zone {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--tk-fill);
}
.ph-lab {
  font-size: 12px;
  color: var(--tk-ph);
}
.ph-list {
  margin-top: 8px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.ph {
  position: relative;
  margin: 0;
  width: 96px;
  height: 72px;
  border-radius: 8px;
  overflow: hidden;
  cursor: zoom-in;
  border: 1px solid var(--tk-line);
}
.ph img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.ph-del {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 18px;
  height: 18px;
  border: none;
  border-radius: 50%;
  background: rgba(0, 0, 0, 0.55);
  color: #fff;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
}
.ph-add {
  width: 96px;
  height: 72px;
  border: 1px dashed var(--tk-dash);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ph);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.ph-add:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
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
.rec-handled {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px 18px;
  font-size: 12.5px;
  color: var(--tk-sub);
}
.rec-handled b {
  color: var(--tk-ink);
  font-weight: 600;
}
.rec-acts {
  margin-top: 12px;
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>
