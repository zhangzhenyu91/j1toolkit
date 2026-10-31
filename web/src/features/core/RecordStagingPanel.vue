<template>
  <div>
    <!-- 批次列表：批次卡（群名 / 推送人 / 时间 / 条数 / 审核备注浅底左色条块） -->
    <template v-if="!detail">
      <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
      <div v-else-if="error" class="empty">
        <svg viewBox="0 0 44 44" width="44" height="44">
          <path
            d="M22 6l18 32H4L22 6zM22 18v10M22 32v2"
            fill="none"
            stroke="currentColor"
            stroke-width="2.4"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
        </svg>
        <p>{{ error }}</p>
        <button class="btn btn-sm" type="button" @click="reload">重试</button>
      </div>
      <div v-else-if="!batches.length" class="empty">
        <svg viewBox="0 0 44 44" width="44" height="44">
          <path
            d="M6 12h12l4 5h16v21H6V12z"
            fill="none"
            stroke="currentColor"
            stroke-width="2.4"
            stroke-linejoin="round"
          />
        </svg>
        <p>暂无待导入批次</p>
      </div>
      <div v-else>
        <div v-for="b in batches" :key="b.id" class="card stg-card" @click="openBatch(b.id)">
          <div class="stg-top">
            <b>{{ dash(b.group_name) }}</b>
            <span class="stg-meta">
              推送人 {{ dash(b.pusher_name) }} · {{ fmtDate(b.created_at) }} ·
              {{ b.record_count || 0 }} 条
            </span>
            <span class="stg-go">审核 ›</span>
          </div>
          <div v-if="String(b.review_note || '').trim()" class="stg-note">
            <b>审核备注：</b>{{ b.review_note }}
          </div>
        </div>
      </div>
    </template>

    <!-- 批次详情：顶栏（返回 / 标题计数 / 转入·废弃）+ 审核备注 + 按台账字段配置的 inline 编辑表格 -->
    <template v-else>
      <div class="stg-bar">
        <button class="btn btn-sm back" type="button" @click="backToList">
          <svg viewBox="0 0 14 14" width="14" height="14">
            <path
              d="M9 2L4 7l5 5"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          返回批次列表
        </button>
        <span class="stg-title">
          {{ dash(detail.batch.group_name) }}
          <small>
            推送人 {{ dash(detail.batch.pusher_name) }} · {{ fmtDate(detail.batch.created_at) }} ·
            剩余 {{ detail.records.length }} 条
          </small>
        </span>
        <span class="spring">
          <button class="btn btn-sm" type="button" :disabled="!selCount" @click="importSel">
            转入选中（{{ selCount }}）
          </button>
          <button
            class="btn btn-sm btn-primary"
            type="button"
            :disabled="!detail.records.length"
            @click="importAll"
          >
            全部转入
          </button>
          <button class="btn btn-sm btn-danger" type="button" @click="discard">废弃批次</button>
        </span>
      </div>
      <div
        v-if="String(detail.batch.review_note || '').trim()"
        class="stg-note"
        style="margin: 0 0 12px"
      >
        <b>审核备注：</b>{{ detail.batch.review_note }}
      </div>
      <div v-if="!detail.records.length" class="empty">
        <svg viewBox="0 0 44 44" width="44" height="44">
          <path
            d="M6 12h12l4 5h16v21H6V12z"
            fill="none"
            stroke="currentColor"
            stroke-width="2.4"
            stroke-linejoin="round"
          />
        </svg>
        <p>本批次记录已全部转入</p>
      </div>
      <div v-else class="stg-table">
        <table class="stg-tb">
          <thead>
            <tr>
              <th>
                <input class="stg-ck" type="checkbox" :checked="allChecked" @change="toggleAll" />
              </th>
              <th v-for="f in fields" :key="f[0]">{{ f[1] }}</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in detail.records" :key="r.id">
              <td>
                <input
                  class="stg-ck"
                  type="checkbox"
                  :checked="!!sel[r.id]"
                  @change="toggleRow(r.id)"
                />
              </td>
              <td v-for="f in fields" :key="f[0]">
                <input
                  v-if="dateFields.includes(f[0])"
                  type="date"
                  class="stg-in"
                  :value="r[f[0]] || ''"
                  @input="setField(r, f[0], $event.target.value)"
                  @click="openPicker"
                />
                <input
                  v-else
                  class="stg-in"
                  :class="wideCls(f[0])"
                  maxlength="64"
                  :value="r[f[0]] == null ? '' : String(r[f[0]])"
                  @input="setField(r, f[0], $event.target.value)"
                />
              </td>
              <td>
                <button
                  class="linkbtn"
                  type="button"
                  :disabled="savingId === r.id"
                  @click="saveRow(r)"
                >
                  {{ savingId === r.id ? '保存中…' : '保存' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import { dash, fmtDate } from './record-fields.js';

const props = defineProps({
  batches: { type: Array, default: () => [] },
  detail: { type: Object, default: null },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' },
  teamId: { type: Number, default: 0 },
  fields: { type: Array, required: true },
  dateFields: { type: Array, default: () => [] },
  noteField: { type: String, default: '' },
});
const emit = defineEmits(['reload', 'open', 'back', 'save-row', 'import', 'discard', 'toast']);

const sel = ref({});
const savingId = ref(0);

const selCount = computed(() => Object.values(sel.value).filter(Boolean).length);
const allChecked = computed(
  () => !!props.detail?.records?.length && props.detail.records.every((r) => sel.value[r.id])
);

function wideCls(key) {
  return { 'w-line': key === 'line_name', 'w-note': key === props.noteField };
}

function openBatch(id) {
  sel.value = {};
  emit('open', id);
}
function backToList() {
  sel.value = {};
  emit('back');
}
function reload() {
  emit('reload');
}
function toggleRow(id) {
  sel.value = { ...sel.value, [id]: !sel.value[id] };
}
function toggleAll() {
  const next = {};
  if (!allChecked.value) {
    (props.detail?.records || []).forEach((r) => {
      next[r.id] = true;
    });
  }
  sel.value = next;
}
// openPicker 点日期文字区直接唤起原生日历（原生仅点图标弹出；不支持或非手势时静默回退）
function openPicker(e) {
  try {
    e.target.showPicker?.();
  } catch {
    /* 忽略：仍可点输入框右侧图标选择 */
  }
}
function setField(row, key, val) {
  row[key] = val;
}
// saveRow 逐行保存：按传入字段表收集该行输入（白名单字段，日期列后端归一）
function saveRow(r) {
  if (savingId.value) return;
  savingId.value = r.id;
  const body = {};
  props.fields.forEach(([k]) => {
    body[k] = r[k] == null ? '' : String(r[k]);
  });
  emit('save-row', r.id, body, () => {
    savingId.value = 0;
  });
}
function importSel() {
  const ids = Object.keys(sel.value)
    .filter((k) => sel.value[k])
    .map(Number);
  emit('import', ids);
}
function importAll() {
  emit('import', null);
}
function discard() {
  emit('discard');
}
</script>

<style scoped>
/* ===== 加载 / 空态（theme.css 口径） ===== */
.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: th-rot 1s linear infinite;
}
@keyframes th-rot {
  to {
    transform: rotate(360deg);
  }
}
.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--tk-ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--tk-ph);
}
.empty svg {
  width: 44px;
  height: 44px;
  color: var(--tk-dash);
}
.empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
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
.linkbtn {
  border: none;
  background: transparent;
  color: var(--tk-brand);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 1px;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 8px;
  font-family: inherit;
  transition: background 0.2s;
}
.linkbtn:hover {
  background: var(--tk-blue-soft);
}
.linkbtn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}

/* ===== 批次卡列表 ===== */
.stg-card {
  margin-bottom: 14px;
  padding: 16px 22px;
  cursor: pointer;
  transition:
    transform 0.18s var(--tk-ease),
    box-shadow 0.18s var(--tk-ease);
}
.stg-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
.stg-top {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.stg-top b {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.stg-meta {
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
.stg-go {
  margin-left: auto;
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-brand);
}
/* 审核备注：浅底 + 左色条警示块（直角，同「政企蓝白」警示信息块族） */
.stg-note {
  margin-top: 10px;
  padding: 9px 12px;
  background: var(--tk-yellow-soft);
  border-left: 4px solid var(--tk-yellow);
  font-size: 12.5px;
  color: var(--tk-sub);
  line-height: 1.7;
  letter-spacing: 0.5px;
}
.stg-note b {
  color: var(--tk-ink);
}

/* ===== 批次详情顶栏 ===== */
.stg-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.stg-bar .back {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.stg-title {
  font-size: 14px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.stg-title small {
  margin-left: 8px;
  font-size: 11.5px;
  color: var(--tk-ph);
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}
.spring {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

/* ===== 批次详情表格：按台账字段配置的 inline 输入，横向滚动 ===== */
.stg-table {
  overflow-x: auto;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  scrollbar-width: thin;
}
.stg-tb {
  border-collapse: collapse;
  min-width: 1680px;
  width: 100%;
}
.stg-tb th,
.stg-tb td {
  padding: 7px 8px;
  border-bottom: 1px solid var(--tk-line);
  font-size: 12px;
  text-align: left;
  white-space: nowrap;
}
.stg-tb th {
  position: sticky;
  top: 0;
  background: var(--tk-fill);
  font-size: 11px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  z-index: 1;
}
.stg-tb tr:last-child td {
  border-bottom: none;
}
.stg-in {
  width: 92px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 6px;
  padding: 0 8px;
  font-size: 12px;
  color: var(--tk-ink);
  font-family: inherit;
  outline: none;
  background: var(--card);
  transition: border-color 0.15s;
}
.stg-in:focus {
  border-color: var(--tk-brand);
}
.stg-in.w-line {
  width: 130px;
}
.stg-in.w-note {
  width: 120px;
}
.stg-in[type='date'] {
  width: 128px;
}
.stg-ck {
  width: 16px;
  height: 16px;
  accent-color: var(--tk-brand);
  cursor: pointer;
}

/* 列表↔批次详情切换：v-if 重建即重放的克制上浮（每次进入/返回都真实触发；reduced-motion 全局降级关闭） */
.stg-card,
.stg-bar,
.stg-table {
  animation: stg-swap-in 0.26s var(--tk-ease);
}
@keyframes stg-swap-in {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
</style>
