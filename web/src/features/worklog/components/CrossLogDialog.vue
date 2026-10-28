<template>
  <div v-if="open" class="mask open" @click.self="close">
    <div class="modal">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">{{ entry ? '修改跨班派车 / 用车人' : '新建跨班日志' }}</span>
          <button type="button" class="m-x" @click="close">
            <WlIcon name="close" :size="14" />
          </button>
        </div>

        <!-- 日期（编辑时仅展示） -->
        <div class="f-hint" style="margin: 0 0 2px">
          <template v-if="entry">{{ fmtDateCN(form.log_date) }}</template>
        </div>
        <template v-if="!entry">
          <label class="f-lab">日期</label>
          <input v-model="form.log_date" type="date" class="f-sel" @click="openPicker" />
        </template>

        <label class="f-lab">归属班组</label>
        <select v-model="form.team_id" class="f-sel f-drop" :disabled="!!entry">
          <option :value="0" disabled>请选择</option>
          <option v-for="t in meta.teams" :key="t.id" :value="t.id">{{ t.name }}</option>
        </select>

        <label class="f-lab">派车车辆（留空 = 未出车）</label>
        <input
          v-model="form.plate_no"
          type="text"
          class="f-sel"
          list="cross-vehicles"
          placeholder="留空 = 未出车"
          autocomplete="off"
        />
        <datalist id="cross-vehicles">
          <option v-for="v in meta.vehicles" :key="v.name" :value="v.name">
            {{ (v.teams || []).join('、') }}
          </option>
        </datalist>

        <template v-if="form.plate_no">
          <label class="f-lab">目的地（可空）</label>
          <input
            v-model="form.destination"
            type="text"
            class="f-sel"
            list="cross-dests"
            placeholder="请输入目的地（可空）"
            autocomplete="off"
          />
          <datalist id="cross-dests">
            <option v-for="d in meta.destinations" :key="d.name" :value="d.name">
              {{ (d.teams || []).join('、') }}
            </option>
          </datalist>

          <label class="f-lab">派车单号（可空）</label>
          <input
            v-model="form.dispatch_order_no"
            type="text"
            class="f-sel"
            maxlength="64"
            placeholder="请输入派车单号（可空）"
            autocomplete="off"
          />

          <label class="f-lab">用车人（点亮即为用车人；同名多班见名称后小字）</label>
          <div class="chips">
            <button
              v-for="m in meta.members"
              :key="m.name"
              type="button"
              class="chip"
              :class="{ on: form.member_names.includes(m.name) }"
              @click="toggleMember(m.name)"
            >
              {{ m.name }}
              <small v-if="(m.teams || []).length > 1">{{ (m.teams || []).join('/') }}</small>
            </button>
            <span v-if="!meta.members.length" class="f-hint">加载中…</span>
          </div>
        </template>
        <div v-else class="f-hint">未出车为站内作业，不参与验证，不可选目的地与用车人</div>

        <label class="f-lab">巡视内容（可空）</label>
        <textarea
          v-model="form.patrol_content"
          class="f-ta"
          rows="3"
          placeholder="如 110kVXX线巡视"
        ></textarea>

        <div v-if="error" class="wl-error">{{ error }}</div>

        <div class="m-foot">
          <button type="button" class="btn btn-sm" @click="close">取消</button>
          <button type="button" class="btn btn-sm btn-primary" :disabled="saving" @click="submit">
            {{ saving ? '保存中…' : '保 存' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref, watch } from 'vue';
import WlIcon from './WlIcon.vue';
import { createCrossLog, updateCrossLog } from '../api.js';

// 跨班日志弹层（仅超管；旧版 .modal 弹层骨架 + f-lab/f-sel/chips 密度）：
// 归属班组 + 车牌/目的地/成员按名称精确命中（缺则服务端补建）。
// meta 来自 /logs/cross/meta；保存成功抛 saved 由父视图刷新。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, default: null },
  meta: {
    type: Object,
    default: () => ({ teams: [], vehicles: [], destinations: [], members: [] }),
  },
  defaultDate: { type: String, default: '' },
});
const emit = defineEmits(['update:open', 'saved']);

const saving = ref(false);
const error = ref('');
const form = reactive({
  log_date: '',
  team_id: 0,
  plate_no: '',
  destination: '',
  member_names: [],
  dispatch_order_no: '',
  patrol_content: '',
});

// fmtDateCN 日期中文。
function fmtDateCN(d) {
  const [y, m, dd] = String(d || '').split('-');
  return `${y} 年 ${Number(m)} 月 ${Number(dd)} 日`;
}

// openPicker 点日期输入框任意位置即展开系统日期选择（不支持的浏览器保留原生行为）。
function openPicker(ev) {
  const el = ev && ev.target;
  try {
    if (el && el.showPicker) el.showPicker();
  } catch (e) {
    // 浏览器拒绝非手势触发时保留原生行为
  }
}

// open 变化时回填（编辑时成员名取 entry.members）。
watch(
  () => [props.open, props.entry],
  () => {
    if (!props.open) return;
    error.value = '';
    saving.value = false;
    const e = props.entry;
    form.log_date = e ? e.log_date : props.defaultDate || new Date().toISOString().slice(0, 10);
    form.team_id = e ? e.team_id : 0;
    form.plate_no = e ? e.plate_no || '' : '';
    form.destination = e ? e.destination_name || '' : '';
    form.member_names = e ? (e.members || []).map((m) => m.name) : [];
    form.dispatch_order_no = e ? e.dispatch_order_no || '' : '';
    form.patrol_content = e ? e.patrol_content || '' : '';
  },
  { immediate: true }
);

// toggleMember 成员名多选。
function toggleMember(name) {
  const i = form.member_names.indexOf(name);
  if (i >= 0) form.member_names.splice(i, 1);
  else form.member_names.push(name);
}

// close 关闭。
function close() {
  emit('update:open', false);
}

// submit 保存（新建 POST /logs/cross；编辑 PUT /logs/cross/{id}，不改日期）。
async function submit() {
  error.value = '';
  if (!form.log_date) {
    error.value = '请选择日期';
    return;
  }
  if (!form.team_id) {
    error.value = '请选择归属班组';
    return;
  }
  if (!form.plate_no && (form.destination || form.member_names.length > 0)) {
    error.value = '未出车时不可填写目的地与用车人';
    return;
  }
  saving.value = true;
  try {
    const payload = {
      team_id: form.team_id,
      plate_no: form.plate_no,
      destination: form.destination,
      member_names: form.member_names,
      dispatch_order_no: form.dispatch_order_no,
      patrol_content: form.patrol_content,
    };
    if (props.entry) await updateCrossLog(props.entry.id, payload);
    else await createCrossLog({ log_date: form.log_date, ...payload });
    emit('saved');
    close();
  } catch (e) {
    error.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

/* ===== 通用弹层骨架（旧版 .mask/.modal/.m-scroll/.m-head/.m-foot） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: rgba(29, 33, 41, 0.45);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 520px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.m-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.m-scroll::-webkit-scrollbar {
  width: 5px;
}
.m-scroll::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.m-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.m-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
}
.m-x:hover {
  background: var(--tk-ink);
  color: #fff;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 表单（f-lab / f-sel / f-ta / chips） ===== */
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-lab:first-of-type {
  margin-top: 2px;
}
.f-sel,
.f-ta {
  width: 100%;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
}
.f-sel {
  height: 36px;
  padding: 0 10px;
}
/* 原生下拉（select）：自绘箭头、悬停/聚焦着色，保持原生可展开 */
select.f-drop {
  padding: 0 30px 0 10px;
  background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='10' height='6' viewBox='0 0 10 6'><path d='M1 1l4 4 4-4' fill='none' stroke='%234E5969' stroke-width='1.5' stroke-linecap='round' stroke-linejoin='round'/></svg>");
  background-repeat: no-repeat;
  background-position: right 10px center;
  cursor: pointer;
  appearance: none;
  -webkit-appearance: none;
}
select.f-drop:hover {
  border-color: var(--tk-brand);
}
select.f-drop:disabled {
  cursor: not-allowed;
  opacity: 0.65;
}
.f-sel:focus,
.f-ta:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.f-ta {
  min-height: 76px;
  padding: 10px 12px;
  line-height: 1.7;
  resize: vertical;
}
.f-hint {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.chip {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  font-size: 12.5px;
  padding: 4px 13px;
  cursor: pointer;
  font-family: inherit;
}
.chip:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.chip.on {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: #fff;
  font-weight: 700;
}
/* 同名多班时名称后的来源班组小字 */
.chip small {
  margin-left: 4px;
  font-size: 10px;
  font-weight: 700;
  color: var(--tk-ph);
}
.chip.on small {
  color: #fff;
  opacity: 0.8;
}

/* 按钮 */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 36px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-ink);
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
  font-family: inherit;
}
.btn:hover:not([disabled]) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-sm {
  height: 32px;
  padding: 0 14px;
  font-size: 12px;
}
.btn-primary {
  color: #fff;
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover:not([disabled]) {
  color: #fff;
  filter: brightness(0.92);
}

.wl-error {
  margin-top: 12px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
