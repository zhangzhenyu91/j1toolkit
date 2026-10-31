<template>
  <!-- 冲突对照卡（班管/超管）：系统值 | 内网值两列对照、差异字段标红，二选一裁决。
       字段表与状态行由台账配置传入（树障 15 列 + 状态行 / 交跨 13 列），不复制第二套渲染。 -->
  <div>
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
      <button class="btn btn-sm" type="button" @click="emit('reload')">重试</button>
    </div>
    <div v-else-if="!conflicts.length" class="empty">
      <svg viewBox="0 0 44 44" width="44" height="44">
        <path
          d="M8 22l10 10 18-20"
          fill="none"
          stroke="currentColor"
          stroke-width="2.6"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
      <p>暂无待解决冲突</p>
    </div>
    <div v-else>
      <div v-for="c in conflicts" :key="c.id" class="card cft">
        <div class="cft-head">
          <b>{{ dash(headerVal(c, 'line_name')) }} · {{ dash(headerVal(c, 'section')) }}</b>
          <span class="cft-time">{{ fmtDate(c.created_at) }}</span>
        </div>
        <div class="cft-cmp">
          <div class="cft-col">
            <div class="cft-col-h">系统值</div>
            <div class="cft-rows">
              <div v-for="row in rowsFor(c)" :key="row.key" class="cft-r">
                <span class="k">{{ row.label }}</span>
                <span class="v" :class="{ df: row.diff }">{{ row.server }}</span>
              </div>
            </div>
          </div>
          <div class="cft-col">
            <div class="cft-col-h">内网值</div>
            <div class="cft-rows">
              <div v-for="row in rowsFor(c)" :key="row.key" class="cft-r">
                <span class="k">{{ row.label }}</span>
                <span class="v" :class="{ df: row.diff }">{{ row.intranet }}</span>
              </div>
            </div>
          </div>
        </div>
        <div class="cft-acts">
          <button
            class="btn btn-sm"
            type="button"
            :disabled="resolving === c.id"
            @click="keep(c, 'server')"
          >
            以系统为准
          </button>
          <button
            class="btn btn-sm btn-primary"
            type="button"
            :disabled="resolving === c.id"
            @click="keep(c, 'intranet')"
          >
            {{ resolving === c.id ? '提交中…' : '以内网为准' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 冲突对照卡：字段表（BIZ_FIELDS）与状态行口径由 features/core/record-fields.js 统一，
// 树障/交跨只传字段配置，渲染逻辑只有这一份。
import { ref } from 'vue';
import { buildConflictRows, conflictServerValue, dash, fmtDate } from './record-fields.js';

const props = defineProps({
  conflicts: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' },
  fields: { type: Array, required: true },
  numericKeys: { type: Array, default: () => [] },
  showStatus: { type: Boolean, default: false },
});
const emit = defineEmits(['reload', 'resolve']);
const resolving = ref(0);

function headerVal(c, key) {
  return conflictServerValue(c, key);
}

function rowsFor(c) {
  return buildConflictRows(c, {
    fields: props.fields,
    numericKeys: props.numericKeys,
    showStatus: props.showStatus,
  });
}

// keep 提交裁决（keep=server 保留系统值 / intranet 采用内网值），结果由视图刷新列表。
function keep(c, side) {
  if (resolving.value) return;
  resolving.value = c.id;
  emit('resolve', c.id, side, () => {
    resolving.value = 0;
  });
}
</script>

<style scoped>
/* ===== 加载 / 空态 ===== */
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
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* ===== 冲突对照卡（系统值 | 内网值，差异字段标红） ===== */
.cft {
  margin-bottom: 16px;
  padding: 16px 22px;
}
.cft-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}
.cft-head b {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.cft-head .cft-time {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
.cft-cmp {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
  margin-top: 12px;
}
@media (max-width: 1000px) {
  .cft-cmp {
    grid-template-columns: 1fr;
  }
}
.cft-col {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
}
.cft-col-h {
  padding: 9px 14px;
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 1px;
  background: var(--tk-fill);
  border-bottom: 1px solid var(--tk-line);
}
.cft-rows {
  padding: 4px 14px 10px;
}
.cft-r {
  display: flex;
  gap: 10px;
  padding: 6px 0;
  border-bottom: 1px dashed var(--tk-line);
  font-size: 12.5px;
}
.cft-r:last-child {
  border-bottom: none;
}
.cft-r .k {
  flex: none;
  width: 84px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.cft-r .v {
  flex: 1;
  min-width: 0;
  font-weight: 600;
  word-break: break-all;
  font-variant-numeric: tabular-nums;
}
.cft-r .v.df {
  color: var(--tk-red);
  font-weight: 700;
}
.cft-acts {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;
}
</style>
