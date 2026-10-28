<template>
  <!-- 旧版 treehazard.html 口径：默认折叠一卡两行——标题行 区段 + 垂距双大字 + 状态点标 + 折叠开关（批量模式另有勾选框），
       次级行 树种×数量 / 水平距离 / 发现时间（found_at 空省略）；辅证照片条折叠态即展示（有图才渲染）；
       展开区紧凑信息行 + 已处理信息 + 处理前/后照片两区（可上传可删）+ 卡内操作。点卡（非交互区）= 折叠/展开，批量模式 = 勾选。 -->
  <div class="card rec" :class="{ done: isDone, fold: !expanded }" @click="onCardClick">
    <div class="rec-top">
      <span
        v-if="batchOn"
        class="rec-ck"
        :class="{ on: selected, dis: isDone }"
        :title="isDone ? '已处理记录不可选' : ''"
        @click.stop="onCheck"
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
      <button
        class="rec-fold"
        type="button"
        :title="expanded ? '折叠' : '展开'"
        @click.stop="toggle"
      >
        <svg viewBox="0 0 13 13" width="13" height="13">
          <path d="M2.5 5l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" />
        </svg>
      </button>
    </div>

    <div class="rec-subline">
      <span v-if="species" class="rec-sub">
        {{ species
        }}{{
          String(record.tree_count || '').trim() ? ' × ' + String(record.tree_count).trim() : ''
        }}
      </span>
      <span class="rec-dist">水平 {{ fmtM(record.horizontal) }}</span>
      <span v-if="foundAt" class="rec-dist">发现时间 {{ foundAt }}</span>
    </div>

    <!-- 辅证照片条：折叠态即展示（有图才渲染），点击缩略图开大图；删除权限 班管/超管 或 上传者本人 -->
    <div v-if="evidence.length" class="rec-evi">
      <div class="ph-zhead"><span>辅证照片</span></div>
      <div class="ph-grid">
        <figure v-for="p in evidence" :key="p.id" class="ph" @click="openViewer(evidence, p)">
          <img :src="p.url" alt="辅证照片" loading="lazy" />
          <button
            v-if="canDelPhoto(p)"
            class="ph-x"
            type="button"
            title="删除照片"
            @click.stop="$emit('remove-photo', record.id, p.id)"
          >
            <svg viewBox="0 0 10 10" width="10" height="10">
              <path
                d="M2 2l6 6M8 2l-6 6"
                fill="none"
                stroke="currentColor"
                stroke-width="1.6"
                stroke-linecap="round"
              />
            </svg>
          </button>
        </figure>
      </div>
    </div>

    <!-- 展开区（grid 0fr→1fr 折叠过渡，只藏展开块，摘要与辅证条常驻） -->
    <div class="rec-more">
      <div class="rec-more-in">
        <div v-if="infoItems.length" class="rf-row">
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
            <div class="ph-zhead">
              <span>{{ z.label }}</span>
              <button class="ph-add" type="button" title="上传照片" @click.stop="pickFiles(z.kind)">
                <svg viewBox="0 0 12 12" width="12" height="12">
                  <path
                    d="M6 2v8M2 6h8"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
            </div>
            <div class="ph-grid">
              <figure v-for="p in z.photos" :key="p.id" class="ph" @click="openViewer(z.photos, p)">
                <img :src="p.url" :alt="z.label" loading="lazy" />
                <button
                  v-if="canDelPhoto(p)"
                  class="ph-x"
                  type="button"
                  title="删除照片"
                  @click.stop="$emit('remove-photo', record.id, p.id)"
                >
                  <svg viewBox="0 0 10 10" width="10" height="10">
                    <path
                      d="M2 2l6 6M8 2l-6 6"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.6"
                      stroke-linecap="round"
                    />
                  </svg>
                </button>
              </figure>
              <div v-if="!z.photos.length" class="ph-none">暂无照片</div>
            </div>
          </div>
        </div>
        <div v-if="isManager || !isDone" class="rec-acts">
          <button
            v-if="isManager"
            class="btn btn-sm btn-danger"
            type="button"
            style="margin-right: auto"
            @click.stop="$emit('remove', record.id)"
          >
            删除
          </button>
          <button
            v-if="isManager"
            class="btn btn-sm"
            type="button"
            @click.stop="$emit('edit', record)"
          >
            编辑
          </button>
          <button
            v-if="!isDone"
            class="btn btn-sm btn-primary"
            type="button"
            @click.stop="$emit('handle', record.id)"
          >
            处理
          </button>
        </div>
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

const emit = defineEmits(['handle', 'edit', 'remove', 'remove-photo', 'upload', 'toggle-select']);

// expanded 折叠状态（默认折叠，仅两行摘要 + 辅证条）
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
// 紧凑信息行：标签+值同行内联，空值字段不渲染
const infoItems = computed(() =>
  COMPACT_FIELDS.map(([key, label]) => {
    const v = String(props.record[key] == null ? '' : props.record[key]).trim();
    return v ? { key, label, value: v } : null;
  }).filter(Boolean)
);

function canDelPhoto(p) {
  return props.isManager || Number(p.uploaded_by) === Number(props.meId);
}

function toggle() {
  expanded.value = !expanded.value;
}

// onCardClick 点卡（非交互区）= 折叠/展开；批量模式点卡 = 勾选；拖拽选中文字不触发。
function onCardClick(e) {
  if (e.target.closest('button, a, input, label, .ph')) return;
  if (window.getSelection && window.getSelection().toString()) return;
  if (props.batchOn) {
    onCheck();
    return;
  }
  toggle();
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
/* ===== 旧式按钮（theme.css .btn 口径：8px 圆角 + 1px 发丝边） ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
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
.btn-primary,
.btn-danger {
  color: #fff;
  border-color: transparent;
}
.btn-primary {
  background: var(--tk-brand);
}
.btn-danger {
  background: var(--tk-red);
}
.btn-primary:hover,
.btn-danger:hover {
  color: #fff;
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* ===== 记录卡（扁平完整卡，无详情弹层分级） ===== */
.card.rec {
  margin-top: 0; /* 全局 .card + .card 间距不叠加：卡片流间距沿旧版 margin-bottom 14px 口径 */
  margin-bottom: 14px;
  padding: 15px 22px;
  position: relative;
  cursor: pointer;
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease),
    opacity 0.3s var(--tk-ease);
}
.rec:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
.rec.done {
  opacity: 0.62;
} /* 已处理卡整体降饱和 */
.rec-top {
  display: flex;
  align-items: baseline;
  gap: 12px 16px;
  flex-wrap: wrap;
}
.rec-sec {
  font-size: 19px;
  font-weight: 700;
  letter-spacing: 1px;
}
/* 垂直距离与区段同级双大字（浏览主看这两项）；≤ 阈值红显 */
.rec-vert {
  font-size: 19px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-ink);
  font-variant-numeric: tabular-nums;
}
.rec-vert.danger {
  color: var(--tk-red);
}
/* 次级字段行：树种×数量 / 水平距离 / 发现时间 */
.rec-subline {
  display: flex;
  align-items: baseline;
  gap: 14px;
  flex-wrap: wrap;
  margin-top: 6px;
}
.rec-sub {
  font-size: 13px;
  font-weight: 600;
  color: var(--tk-ink);
  letter-spacing: 0.5px;
}
.rec-dist {
  font-size: 13px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
/* 状态点标：圆点 + 文字（未处理橙 / 已处理绿） */
.rec-tag {
  margin-left: auto;
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 10px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.5px;
}
.rec-tag::before {
  content: '';
  width: 5px;
  height: 5px;
  border-radius: 50%;
}
.rec-tag.un {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.rec-tag.un::before {
  background: var(--tk-yellow);
}
.rec-tag.ok {
  background: var(--tk-green-soft);
  color: #0b8a37;
}
.rec-tag.ok::before {
  background: #0b8a37;
}
/* 批量勾选框（批量模式才渲染；已处理卡禁用态） */
.rec-ck {
  flex: none;
  width: 20px;
  height: 20px;
  border-radius: 6px;
  border: 1.5px solid var(--tk-dash);
  background: #fff;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: transparent;
  align-self: center;
  transition:
    border-color 0.15s,
    background 0.15s;
}
.rec-ck:hover {
  border-color: var(--tk-brand);
}
.rec-ck.on {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: #fff;
}
.rec-ck.dis {
  opacity: 0.35;
  cursor: not-allowed;
}
.rec-ck.dis:hover {
  border-color: var(--tk-dash);
}
/* 折叠/展开开关（标题行右端，每卡一枚） */
.rec-fold {
  flex: none;
  align-self: center;
  width: 26px;
  height: 26px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ph);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  transition:
    color 0.15s,
    border-color 0.15s;
}
.rec-fold:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
.rec-fold svg {
  width: 13px;
  height: 13px;
  transition: transform 0.2s var(--tk-ease);
}
.rec:not(.fold) .rec-fold svg {
  transform: rotate(180deg);
} /* 展开态旋 180° */
/* 折叠：grid 0fr→1fr 高度过渡 + 淡入（.rec-more-in 为折叠必需内层） */
.rec-more {
  display: grid;
  grid-template-rows: 1fr;
  opacity: 1;
  transition:
    grid-template-rows 0.28s var(--tk-ease),
    opacity 0.22s ease,
    margin-top 0.28s var(--tk-ease);
}
.rec.fold .rec-more {
  grid-template-rows: 0fr;
  opacity: 0;
}
.rec-more-in {
  min-height: 0;
  overflow: hidden;
}
/* 紧凑行式字段流（标签小灰字 + 值同行内联） */
.rf-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 18px;
  margin-top: 8px;
}
.rf {
  font-size: 12.5px;
  color: var(--tk-ink);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.rf i {
  font-style: normal;
  color: var(--tk-ph);
  margin-right: 4px;
}
/* 已处理信息行（绿点 + 处理人/处理情况/处理时间；浅底直角块一族口径，圆角 8px 同旧版） */
.rec-handled {
  margin-top: 12px;
  padding: 9px 12px;
  border-radius: 8px;
  background: var(--tk-green-soft);
  display: flex;
  align-items: center;
  gap: 8px 14px;
  flex-wrap: wrap;
  font-size: 12.5px;
  color: #0b8a37;
  letter-spacing: 0.5px;
}
.rec-handled::before {
  content: '';
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-green);
  flex: none;
}
.rec-handled b {
  font-weight: 700;
}
/* 辅证照片条（折叠态即展示） */
.rec-evi {
  margin-top: 10px;
}
/* 照片两区（处理前/后）横向分区，窄屏自动换列 */
.ph-zones {
  margin-top: 12px;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 12px;
}
.ph-zhead {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 1px;
}
.ph-add {
  margin-left: auto;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-brand);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    border-color 0.15s,
    background 0.15s;
}
.ph-add:hover {
  border-color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.ph-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(96px, 1fr));
  gap: 8px;
  margin-top: 8px;
}
.ph {
  position: relative;
  margin: 0;
  height: 88px;
  border-radius: 8px;
  overflow: hidden;
  cursor: zoom-in;
  background: var(--tk-fill);
}
.ph img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.ph-x {
  position: absolute;
  top: 4px;
  right: 4px;
  width: 20px;
  height: 20px;
  border-radius: 6px;
  border: none;
  background: rgba(29, 33, 41, 0.72);
  color: #fff;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transition: opacity 0.15s;
}
.ph:hover .ph-x {
  opacity: 1;
}
.ph-none {
  grid-column: 1 / -1;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  padding: 8px 0;
}
/* 卡内操作行：删除居左，编辑/处理居右 */
.rec-acts {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;
}
@media (prefers-reduced-motion: reduce) {
  .rec,
  .rec-more,
  .rec-fold svg {
    transition: none !important;
  }
}
</style>
