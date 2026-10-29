<template>
  <!-- 旧版 crossing.html 口径：默认折叠一卡两行——标题行 区段 + 交叉垂距双大字 + 有隐患点标 + 折叠开关，
       次级行 跨越类型 / 交叉角；展开区紧凑信息行（13 业务列，摘要行已占位不重复）+ 来源与推送信息行
       + 卡内编辑（统一表单 RecordForm 走 asDrawer 右侧抽屉，同手动新增）/ 删除。点卡（非交互区）= 折叠/展开。 -->
  <div class="card rec" :class="{ fold: !expanded }" @click="onCardClick">
    <div class="rec-top">
      <b class="rec-sec">{{ dash(record.section) }}</b>
      <span class="rec-vert" title="交叉垂直距离">垂距 {{ fmtM(record.cross_vertical) }}</span>
      <span v-if="isHazard" class="rec-tag hz" title="是否存在隐患">有隐患</span>
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
      <span v-if="crossingType" class="rec-sub">跨越 {{ crossingType }}</span>
      <span class="rec-dist">交叉角 {{ fmtDeg(record.cross_angle) }}</span>
    </div>

    <!-- 展开区（grid 0fr→1fr 折叠过渡） -->
    <div class="rec-more">
      <div class="rec-more-in">
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
        <!-- 操作行：编辑（右侧抽屉统一表单）/ 删除，居右；编辑态由表单自带保存/取消 -->
        <div v-if="isManager" class="rec-acts">
          <button class="btn btn-sm" type="button" @click.stop="startEdit">编辑</button>
          <button
            class="btn btn-sm btn-danger"
            type="button"
            @click.stop="$emit('remove', record.id)"
          >
            删除
          </button>
        </div>
        <!-- 编辑统一表单：RecordForm asDrawer 右侧抽屉（受控 visible 保退出动画），与手动新增同款 -->
        <RecordForm
          as-drawer
          v-model:visible="editing"
          drawer-title="编辑记录"
          :record="record"
          :saving="saving"
          @save="onFormSave"
          @cancel="editing = false"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import RecordForm from './RecordForm.vue';
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
const emit = defineEmits(['remove', 'save']);

// expanded 折叠状态（默认折叠，仅两行摘要）
const expanded = ref(false);
// editing 右侧抽屉编辑态（RecordForm asDrawer 受控显隐）；saving 保存中防重入
const editing = ref(false);
const saving = ref(false);

const isHazard = computed(() => hasHazard(props.record));
const crossingType = computed(() => String(props.record.crossing_type || '').trim());

// 紧凑信息行：13 业务列标签+值同行内联，摘要行已占位的字段不重复，空值字段不渲染
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

// 来源与推送信息行（空值跳过）
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

function toggle() {
  expanded.value = !expanded.value;
}

// startEdit 进入编辑（统一表单复用 RecordForm asDrawer 右侧抽屉，与手动新增同款）。
function startEdit() {
  if (!props.isManager || saving.value) return;
  editing.value = true;
}

// onFormSave 提交编辑：交上级调 API，done(ok) 回调复位保存态；失败保留表单便于修改重试。
function onFormSave(body) {
  if (saving.value) return;
  saving.value = true;
  emit('save', props.record.id, body, (ok) => {
    saving.value = false;
    if (ok !== false) editing.value = false;
  });
}

// onCardClick 点卡（非交互区）= 折叠/展开；拖拽选中文字不触发。
function onCardClick(e) {
  if (e.target.closest('button, a, input, label')) return;
  if (window.getSelection && window.getSelection().toString()) return;
  toggle();
}
</script>

<style scoped>
/* ===== 旧式按钮（theme.css .btn 口径） ===== */
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
.btn-primary,
.btn-danger {
  color: var(--on-brand);
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
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* ===== 记录卡（扁平完整交跨卡，无详情弹层分级） ===== */
.card.rec {
  margin-top: 0; /* 全局 .card + .card 间距不叠加：卡片流间距沿旧版 margin-bottom 14px 口径 */
  margin-bottom: 14px;
  padding: 15px 22px;
  position: relative;
  cursor: pointer;
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
}
.rec:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
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
/* 垂直距离与区段同级双大字（浏览主看这两项） */
.rec-vert {
  font-size: 19px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-ink);
  font-variant-numeric: tabular-nums;
}
/* 次级字段行：跨越类型 / 交叉角 */
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
/* 状态点标：圆点 + 文字（有隐患橙，同树障「待核验」一族口径） */
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
.rec-tag.hz {
  background: var(--tk-yellow-soft);
  color: var(--warn-text);
}
.rec-tag.hz::before {
  background: var(--tk-yellow);
}
/* 折叠/展开开关（标题行右端，每卡一枚） */
.rec-fold {
  flex: none;
  align-self: center;
  width: 26px;
  height: 26px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
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
}
/* 折叠：grid 0fr→1fr 高度过渡 + 淡入 */
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
/* 来源与推送信息行（浅灰块，与业务字段分隔） */
.rec-src {
  margin-top: 12px;
  padding: 9px 12px;
  border-radius: 8px;
  background: var(--tk-fill);
  display: flex;
  align-items: baseline;
  gap: 8px 14px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
.rec-src b {
  font-weight: 600;
  color: var(--tk-ink);
}
/* 卡内操作行：编辑 / 删除居右 */
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
