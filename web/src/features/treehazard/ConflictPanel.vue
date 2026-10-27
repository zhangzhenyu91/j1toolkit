<template>
  <!-- 冲突对照卡（班管/超管）：系统值 | 内网值两列对照、差异字段标红，二选一裁决（旧版 cft 卡口径） -->
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
          <b>{{ dash(serverVal(c, 'line_name')) }} · {{ dash(serverVal(c, 'section')) }}</b>
          <span class="cft-time">{{ fmtDate(c.created_at) }}</span>
        </div>
        <div class="cft-cmp">
          <div class="cft-col">
            <div class="cft-col-h">系统值</div>
            <div class="cft-rows">
              <div v-for="f in BIZ_FIELDS" :key="f[0]" class="cft-r">
                <span class="k">{{ f[1] }}</span>
                <span
                  class="v"
                  :class="{ df: !sameVal(f[0], serverVal(c, f[0]), intranetVal(c, f[0])) }"
                >
                  {{ cellVal(f[0], serverVal(c, f[0])) }}
                </span>
              </div>
              <div class="cft-r">
                <span class="k">状态</span>
                <span class="v" :class="{ df: statusVal(c) !== intranetStatus(c) }">
                  {{ statusText(statusVal(c)) }}
                </span>
              </div>
            </div>
          </div>
          <div class="cft-col">
            <div class="cft-col-h">内网值</div>
            <div class="cft-rows">
              <div v-for="f in BIZ_FIELDS" :key="f[0]" class="cft-r">
                <span class="k">{{ f[1] }}</span>
                <span
                  class="v"
                  :class="{ df: !sameVal(f[0], serverVal(c, f[0]), intranetVal(c, f[0])) }"
                >
                  {{ cellVal(f[0], intranetVal(c, f[0])) }}
                </span>
              </div>
              <div class="cft-r">
                <span class="k">状态</span>
                <span class="v" :class="{ df: statusVal(c) !== intranetStatus(c) }">
                  {{ statusText(intranetStatus(c)) }}
                </span>
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
// 冲突对照卡（班管/超管）：系统值 | 内网值两列对照、差异字段标红，二选一裁决。
// 15 业务列 + 状态行（状态按 record_status / 快照 status，内网侧由 handled_at 推导，同后端 intranetFields 口径）。
import { ref } from 'vue';
import { BIZ_FIELDS, dash, fmtDate, fmtKv } from './util.js';

const props = defineProps({
  conflicts: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' },
});
const emit = defineEmits(['reload', 'resolve']);
const resolving = ref(0);

// serverVal 系统当前值：冲突行平铺字段（LEFT JOIN 当前记录；记录已删时回退服务端快照）。
function serverVal(c, key) {
  let v = c[key];
  if ((v === null || v === undefined) && c.server_snapshot) v = c.server_snapshot[key];
  return v;
}

// intranetVal 内网侧值：取 intranet_snapshot。
function intranetVal(c, key) {
  return c.intranet_snapshot ? c.intranet_snapshot[key] : undefined;
}

// sameVal 单元格等值判定（日期已归一 yyyy-MM-dd；数字类「3」与「3.0」视为相同，与后端 diff 口径一致）。
function sameVal(key, a, b) {
  const sa = String(a == null ? '' : a).trim();
  const sb = String(b == null ? '' : b).trim();
  if (sa === sb) return true;
  const numKeys = [
    'voltage_level',
    'tree_count',
    'dist_small',
    'dist_big',
    'diameter',
    'horizontal',
    'vertical',
  ];
  if (numKeys.includes(key) && sa && sb) {
    const na = Number(sa);
    const nb = Number(sb);
    if (!Number.isNaN(na) && !Number.isNaN(nb)) return na === nb;
  }
  return false;
}

function cellVal(key, raw) {
  if (key === 'voltage_level') return fmtKv(raw);
  return dash(raw);
}

// statusVal 系统侧状态：record_status 优先，记录已删回退服务端快照 status。
function statusVal(c) {
  return c.record_status || (c.server_snapshot && c.server_snapshot.status) || 'unhandled';
}

// intranetStatus 内网侧状态：handled_at 非空 → 已处理（同后端 intranetFields 口径）。
function intranetStatus(c) {
  return String(intranetVal(c, 'handled_at') || '').trim() ? 'handled' : 'unhandled';
}

function statusText(s) {
  return s === 'handled' ? '已处理' : '未处理';
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
.btn-primary {
  color: #fff;
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: #fff;
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
