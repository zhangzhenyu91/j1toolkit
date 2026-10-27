<template>
  <div>
    <div v-if="loading" class="loading">加载中…</div>
    <div v-else-if="error" class="empty">
      <p>{{ error }}</p>
      <t-button size="small" variant="outline" @click="emit('reload')">重试</t-button>
    </div>
    <div v-else-if="!conflicts.length" class="empty muted">暂无待解决冲突</div>
    <div v-else class="cft-list">
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
            </div>
          </div>
        </div>
        <div class="cft-acts">
          <t-button
            size="small"
            variant="outline"
            :disabled="resolving === c.id"
            @click="keep(c, 'server')"
          >
            以系统为准
          </t-button>
          <t-button
            size="small"
            theme="primary"
            :disabled="resolving === c.id"
            @click="keep(c, 'intranet')"
          >
            {{ resolving === c.id ? '提交中…' : '以内网为准' }}
          </t-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 冲突对照卡（班管/超管）：系统值 | 内网值两列对照、差异字段标红，二选一裁决。
// 13 业务列（交跨无处理状态行），key 与后端列名一致。
import { ref } from 'vue';
import { BIZ_FIELDS, dash, fmtDate, fmtField } from './util.js';

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
    'dist_small',
    'dist_big',
    'cross_horizontal',
    'cross_vertical',
    'cross_angle',
  ];
  if (numKeys.includes(key) && sa && sb) {
    const na = Number(sa);
    const nb = Number(sb);
    if (!Number.isNaN(na) && !Number.isNaN(nb)) return na === nb;
  }
  return false;
}

function cellVal(key, raw) {
  return fmtField(key, raw);
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
.cft-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.cft-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.cft-head b {
  font-size: 14.5px;
}
.cft-time {
  margin-left: auto;
  font-size: 12px;
  color: var(--tk-ph);
  font-variant-numeric: tabular-nums;
}
.cft-cmp {
  margin-top: 10px;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.cft-col {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
}
.cft-col-h {
  padding: 6px 10px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-sub);
  background: var(--tk-block, #f7f8fa);
}
.cft-rows {
  padding: 2px 10px;
}
.cft-r {
  display: flex;
  gap: 8px;
  padding: 5px 0;
  border-bottom: 1px solid var(--tk-line);
  font-size: 12.5px;
}
.cft-r:last-child {
  border-bottom: none;
}
.cft-r .k {
  flex: 0 0 72px;
  color: var(--tk-ph);
}
.cft-r .v {
  flex: 1;
  color: var(--tk-ink);
  word-break: break-all;
}
.cft-r .v.df {
  color: var(--tk-red);
  font-weight: 600;
}
.cft-acts {
  margin-top: 10px;
  display: flex;
  gap: 10px;
  justify-content: flex-end;
}
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
.empty.muted {
  color: var(--tk-ph);
}
</style>
