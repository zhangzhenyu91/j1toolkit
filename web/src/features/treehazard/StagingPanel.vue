<template>
  <div>
    <!-- 批次列表 -->
    <template v-if="!detail">
      <div v-if="loading" class="loading">加载中…</div>
      <div v-else-if="error" class="empty">
        <p>{{ error }}</p>
        <t-button size="small" variant="outline" @click="reload">重试</t-button>
      </div>
      <div v-else-if="!batches.length" class="empty muted">暂无待导入批次</div>
      <div v-else class="stg-list">
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

    <!-- 批次详情 -->
    <template v-else>
      <div class="stg-bar">
        <t-button size="small" variant="outline" @click="backToList">‹ 返回批次列表</t-button>
        <span class="stg-title">
          {{ dash(detail.batch.group_name) }}
          <small>
            推送人 {{ dash(detail.batch.pusher_name) }} · {{ fmtDate(detail.batch.created_at) }} ·
            剩余 {{ detail.records.length }} 条
          </small>
        </span>
        <span class="spring">
          <t-button size="small" variant="outline" :disabled="!selCount" @click="importSel">
            转入选中（{{ selCount }}）
          </t-button>
          <t-button
            size="small"
            theme="primary"
            :disabled="!detail.records.length"
            @click="importAll"
          >
            全部转入
          </t-button>
          <t-button size="small" theme="danger" variant="outline" @click="discard"
            >废弃批次</t-button
          >
        </span>
      </div>
      <div v-if="String(detail.batch.review_note || '').trim()" class="stg-note">
        <b>审核备注：</b>{{ detail.batch.review_note }}
      </div>
      <div v-if="!detail.records.length" class="empty muted">本批次记录已全部转入</div>
      <div v-else class="stg-table">
        <table class="stg-tb">
          <thead>
            <tr>
              <th><input type="checkbox" :checked="allChecked" @change="toggleAll" /></th>
              <th v-for="f in BIZ_FIELDS" :key="f[0]">{{ f[1] }}</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in detail.records" :key="r.id">
              <td>
                <input type="checkbox" :checked="!!sel[r.id]" @change="toggleRow(r.id)" />
              </td>
              <td v-for="f in BIZ_FIELDS" :key="f[0]">
                <input
                  v-if="f[0] === 'found_at' || f[0] === 'handled_at'"
                  type="date"
                  class="stg-in"
                  :value="r[f[0]] || ''"
                  @input="setField(r, f[0], $event.target.value)"
                />
                <input
                  v-else
                  class="stg-in"
                  :class="{ 'w-line': f[0] === 'line_name', 'w-note': f[0] === 'handle_note' }"
                  maxlength="64"
                  :value="r[f[0]] == null ? '' : String(r[f[0]])"
                  @input="setField(r, f[0], $event.target.value)"
                />
              </td>
              <td>
                <t-button
                  size="small"
                  variant="text"
                  theme="primary"
                  :loading="savingId === r.id"
                  @click="saveRow(r)"
                >
                  保存
                </t-button>
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
import { BIZ_FIELDS, dash, fmtDate } from './util.js';

const props = defineProps({
  batches: { type: Array, default: () => [] },
  detail: { type: Object, default: null },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' },
  teamId: { type: Number, default: 0 },
});
const emit = defineEmits(['reload', 'open', 'back', 'save-row', 'import', 'discard', 'toast']);

const sel = ref({});
const savingId = ref(0);

const selCount = computed(() => Object.values(sel.value).filter(Boolean).length);
const allChecked = computed(
  () => !!props.detail?.records?.length && props.detail.records.every((r) => sel.value[r.id])
);

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
function setField(row, key, val) {
  row[key] = val;
}
function saveRow(r) {
  savingId.value = r.id;
  const body = {};
  BIZ_FIELDS.forEach(([k]) => {
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
.loading {
  padding: 30px;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
}
.empty {
  padding: 36px;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
}
.stg-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.stg-card {
  cursor: pointer;
}
.stg-top {
  display: flex;
  align-items: baseline;
  gap: 12px;
}
.stg-meta {
  color: var(--tk-ph);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.stg-go {
  margin-left: auto;
  color: var(--tk-brand);
  font-size: 12.5px;
}
.stg-note {
  margin-top: 8px;
  padding: 8px 10px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  font-size: 12.5px;
  color: var(--tk-sub);
}
.stg-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.stg-title {
  font-size: 15px;
  font-weight: 600;
}
.stg-title small {
  display: block;
  font-size: 12px;
  font-weight: 400;
  color: var(--tk-ph);
  margin-top: 2px;
}
.spring {
  margin-left: auto;
  display: flex;
  gap: 8px;
}
.stg-table {
  overflow: auto;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
}
.stg-tb {
  border-collapse: collapse;
  font-size: 12.5px;
  white-space: nowrap;
}
.stg-tb th,
.stg-tb td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--tk-line);
  text-align: left;
}
.stg-tb th {
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-weight: 600;
  position: sticky;
  top: 0;
}
.stg-in {
  height: 28px;
  padding: 0 6px;
  border: 1px solid var(--tk-line);
  border-radius: 4px;
  font-size: 12.5px;
  width: 84px;
}
.stg-in.w-line {
  width: 130px;
}
.stg-in.w-note {
  width: 160px;
}
</style>
