<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card">
      <h3 class="card-title">{{ entry ? '编辑跨班日志' : '新建跨班日志' }}</h3>

      <div class="wl-field">
        <span class="field-label">日期</span>
        <input v-if="!entry" v-model="form.log_date" type="date" class="wl-input" />
        <span v-else class="num">{{ form.log_date }}</span>
      </div>

      <div class="wl-field">
        <span class="field-label">归属班组</span>
        <select v-model="form.team_id" class="wl-input" :disabled="!!entry">
          <option :value="0" disabled>请选择</option>
          <option v-for="t in meta.teams" :key="t.id" :value="t.id">{{ t.name }}</option>
        </select>
      </div>

      <div class="wl-field">
        <span class="field-label">车牌号</span>
        <input
          v-model="form.plate_no"
          type="text"
          class="wl-input"
          list="cross-vehicles"
          placeholder="留空 = 未出车"
        />
        <datalist id="cross-vehicles">
          <option v-for="v in meta.vehicles" :key="v.name" :value="v.name">
            {{ (v.teams || []).join('、') }}
          </option>
        </datalist>
      </div>

      <div class="wl-field">
        <span class="field-label">目的地</span>
        <input
          v-model="form.destination"
          type="text"
          class="wl-input"
          list="cross-dests"
          placeholder="选填"
        />
        <datalist id="cross-dests">
          <option v-for="d in meta.destinations" :key="d.name" :value="d.name">
            {{ (d.teams || []).join('、') }}
          </option>
        </datalist>
      </div>

      <div class="wl-field">
        <span class="field-label">用车人</span>
        <div class="wl-chips">
          <button
            v-for="m in meta.members"
            :key="m.name"
            type="button"
            class="wl-chip"
            :class="{ on: form.member_names.includes(m.name) }"
            @click="toggleMember(m.name)"
          >
            {{ m.name }}
          </button>
          <span v-if="!meta.members.length" class="muted">加载中…</span>
        </div>
      </div>

      <div class="wl-field">
        <span class="field-label">派车单号</span>
        <input v-model="form.dispatch_order_no" type="text" class="wl-input" placeholder="选填" />
      </div>

      <div class="wl-field">
        <span class="field-label">巡视内容</span>
        <textarea
          v-model="form.patrol_content"
          class="wl-input wl-area"
          rows="3"
          placeholder="选填"
        ></textarea>
      </div>

      <div v-if="error" class="wl-error">{{ error }}</div>

      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">取消</t-button>
        <t-button :loading="saving" @click="submit">保存</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref, watch } from 'vue';
import { createCrossLog, updateCrossLog } from '../api.js';

// 跨班日志弹窗（仅超管）：归属班组 + 车牌/目的地/成员按名称精确命中（缺则服务端补建）。
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
.wl-modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.wl-modal {
  width: min(560px, calc(100vw - 32px));
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-field {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 7px 0;
}
.wl-field .field-label {
  width: 64px;
  flex-shrink: 0;
  padding-top: 7px;
}
.wl-input {
  flex: 1;
  min-width: 0;
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 10px;
  font-size: 13px;
  color: var(--tk-ink);
  background: var(--tk-fill);
}
.wl-area {
  height: auto;
  padding: 8px 10px;
  resize: vertical;
  line-height: 1.6;
}
.wl-input:focus {
  outline: none;
  border-color: var(--tk-brand);
  background: #fff;
}
.wl-chips {
  flex: 1;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.wl-chip {
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
}
.wl-chip.on {
  background: var(--tk-blue-soft);
  border-color: transparent;
  color: var(--tk-brand);
  font-weight: 600;
}
.wl-error {
  margin-top: 8px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
