<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card">
      <h3 class="card-title">{{ entry ? '编辑日志' : '新建日志' }}</h3>

      <!-- 日期（仅新建可改） -->
      <div class="wl-field">
        <span class="field-label">日期</span>
        <input v-if="!entry" v-model="form.log_date" type="date" class="wl-input" />
        <span v-else class="num">{{ form.log_date }}</span>
      </div>

      <!-- 车牌（空 = 未出车） -->
      <div class="wl-field">
        <span class="field-label">车牌号</span>
        <select v-model="form.vehicle_id" class="wl-input">
          <option :value="0">未出车</option>
          <option v-for="v in meta.vehicles" :key="v.id" :value="v.id">{{ v.plate_no }}</option>
        </select>
      </div>

      <!-- 目的地 -->
      <div class="wl-field">
        <span class="field-label">目的地</span>
        <select v-model="form.destination_id" class="wl-input" :disabled="form.vehicle_id === 0">
          <option :value="0">无</option>
          <option v-for="d in meta.destinations" :key="d.id" :value="d.id">{{ d.name }}</option>
        </select>
      </div>

      <!-- 派车单号 -->
      <div class="wl-field">
        <span class="field-label">派车单号</span>
        <input
          v-model="form.dispatch_order_no"
          type="text"
          class="wl-input"
          placeholder="选填"
          :disabled="form.vehicle_id === 0"
        />
      </div>

      <!-- 巡视内容 -->
      <div class="wl-field">
        <span class="field-label">巡视内容</span>
        <textarea
          v-model="form.patrol_content"
          class="wl-input wl-area"
          rows="3"
          placeholder="选填"
        ></textarea>
      </div>

      <!-- 用车人（多选） -->
      <div class="wl-field">
        <span class="field-label">用车人</span>
        <div class="wl-chips" :class="{ disabled: form.vehicle_id === 0 }">
          <button
            v-for="m in meta.members"
            :key="m.id"
            type="button"
            class="wl-chip"
            :class="{ on: form.member_ids.includes(m.id) }"
            :disabled="form.vehicle_id === 0"
            @click="toggleMember(m.id)"
          >
            {{ m.name }}
          </button>
          <span v-if="!meta.members.length" class="muted">暂无成员，请到「班组数据」维护</span>
        </div>
      </div>

      <!-- 备注（编辑模式才可改） -->
      <div v-if="entry" class="wl-field">
        <span class="field-label">备注</span>
        <textarea
          v-model="form.remark"
          class="wl-input wl-area"
          rows="2"
          placeholder="选填"
        ></textarea>
      </div>
      <div v-if="entry" class="wl-field">
        <span class="field-label">备注附件</span>
        <div class="wl-chips">
          <span v-for="(f, i) in form.remark_files" :key="f.cos_key" class="wl-file-chip">
            {{ f.name }}
            <button
              type="button"
              class="wl-file-x"
              aria-label="移除附件"
              @click="form.remark_files.splice(i, 1)"
            >
              ×
            </button>
          </span>
          <button type="button" class="wl-chip" :disabled="uploadingFile" @click="pickRemarkFile">
            {{ uploadingFile ? '上传中…' : '+ 上传附件' }}
          </button>
          <input ref="remarkInput" type="file" class="wl-hidden-input" @change="onRemarkFile" />
        </div>
        <div class="muted">最多 9 个（图片/视频/Office 文档，单个 50MB 以内）</div>
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
import { createLog, updateLog, uploadRemarkFile } from '../api.js';

// 新建/编辑日志弹窗：车辆/目的地/用车人来自 /meta；备注与备注附件仅编辑模式可改。
// 保存成功抛 saved，由父视图刷新日志列表与月历。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, default: null },
  meta: { type: Object, default: () => ({ vehicles: [], destinations: [], members: [] }) },
  defaultDate: { type: String, default: '' },
});
const emit = defineEmits(['update:open', 'saved']);

const saving = ref(false);
const error = ref('');

const form = reactive({
  log_date: '',
  vehicle_id: 0,
  destination_id: 0,
  dispatch_order_no: '',
  patrol_content: '',
  member_ids: [],
  remark: '',
  remark_files: [],
});

// open/entry 变化时回填表单（新建取 defaultDate）。
watch(
  () => [props.open, props.entry],
  () => {
    if (!props.open) return;
    error.value = '';
    saving.value = false;
    const e = props.entry;
    form.log_date = e ? e.log_date : props.defaultDate || new Date().toISOString().slice(0, 10);
    form.vehicle_id = e ? e.vehicle_id || 0 : 0;
    form.destination_id = e ? e.destination_id || 0 : 0;
    form.dispatch_order_no = e ? e.dispatch_order_no || '' : '';
    form.patrol_content = e ? e.patrol_content || '' : '';
    form.member_ids = e ? (e.members || []).map((m) => m.member_id) : [];
    form.remark = e ? e.remark || '' : '';
    form.remark_files = e ? (e.remark_files || []).map((f) => ({ ...f })) : [];
  },
  { immediate: true }
);

// toggleMember 用车人多选切换。
function toggleMember(id) {
  const i = form.member_ids.indexOf(id);
  if (i >= 0) form.member_ids.splice(i, 1);
  else form.member_ids.push(id);
}

const remarkInput = ref(null);
const uploadingFile = ref(false);

// pickRemarkFile 触发附件选择。
function pickRemarkFile() {
  remarkInput.value && remarkInput.value.click();
}

// onRemarkFile 上传备注附件（成功后挂入 form.remark_files，保存时 PUT 落库）。
async function onRemarkFile(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  if (form.remark_files.length >= 9) {
    error.value = '备注附件最多 9 个';
    return;
  }
  error.value = '';
  uploadingFile.value = true;
  try {
    const item = await uploadRemarkFile(props.entry.id, file);
    form.remark_files.push(item);
  } catch (e) {
    error.value = e.message || '附件上传失败';
  } finally {
    uploadingFile.value = false;
  }
}

// close 关闭弹窗。
function close() {
  emit('update:open', false);
}

// submit 保存（新建 POST /logs；编辑 PUT /logs/{id}）。
async function submit() {
  error.value = '';
  if (!form.log_date) {
    error.value = '请选择日期';
    return;
  }
  if (form.vehicle_id === 0 && (form.destination_id !== 0 || form.member_ids.length > 0)) {
    error.value = '未出车时不可填写目的地与用车人';
    return;
  }
  saving.value = true;
  try {
    const payload = {
      vehicle_id: form.vehicle_id || 0,
      destination_id: form.vehicle_id ? form.destination_id || 0 : 0,
      dispatch_order_no: form.dispatch_order_no,
      patrol_content: form.patrol_content,
      member_ids: form.vehicle_id ? form.member_ids : [],
    };
    if (props.entry) {
      await updateLog(props.entry.id, {
        ...payload,
        remark: form.remark,
        remark_files: form.remark_files,
      });
    } else {
      await createLog({ log_date: form.log_date, ...payload });
    }
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
  width: min(520px, calc(100vw - 32px));
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
.wl-input:disabled {
  color: var(--tk-ph);
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
.wl-chip:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.wl-file-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border-radius: 4px;
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
}
.wl-file-x {
  border: none;
  background: none;
  color: var(--tk-ph);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0;
}
.wl-hidden-input {
  display: none;
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
