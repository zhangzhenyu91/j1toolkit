<template>
  <div v-if="open" class="mask open" @click.self="close">
    <div class="modal">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">{{ entry ? '修改派车 / 用车人' : '新建日志' }}</span>
          <button type="button" class="m-x" @click="close">
            <WlIcon name="close" :size="14" />
          </button>
        </div>

        <!-- 日期（仅新建可改；点输入框任意位置展开系统日期选择） -->
        <div class="f-hint" style="margin: 0 0 2px">
          <template v-if="entry">{{ fmtDateCN(form.log_date) }}</template>
        </div>
        <template v-if="!entry">
          <label class="f-lab">日期</label>
          <input v-model="form.log_date" type="date" class="f-sel" @click="openPicker" />
        </template>

        <!-- 派车车辆（空 = 未出车） -->
        <label class="f-lab">派车车辆</label>
        <select v-model="form.vehicle_id" class="f-sel f-drop">
          <option :value="0">未出车</option>
          <option v-for="v in meta.vehicles" :key="v.id" :value="v.id">{{ v.plate_no }}</option>
        </select>

        <template v-if="form.vehicle_id !== 0">
          <label class="f-lab">目的地</label>
          <select v-model="form.destination_id" class="f-sel f-drop">
            <option :value="0">未选择</option>
            <option v-for="d in meta.destinations" :key="d.id" :value="d.id">
              {{ d.name }}{{ markSuffix(d) }}
            </option>
          </select>
          <div v-if="destMarkText" class="dest-mark-line">
            <em class="dest-mark" :class="destMarkCls">{{ destMarkText }}</em>
            <span class="f-hint" style="margin: 0">{{ destStdText }}</span>
          </div>

          <label class="f-lab">派车单号（可空）</label>
          <input
            v-model="form.dispatch_order_no"
            type="text"
            class="f-sel"
            maxlength="64"
            placeholder="请输入派车单号（可空）"
            autocomplete="off"
          />

          <label class="f-lab">用车人（点亮即为用车人）</label>
          <div class="chips">
            <button
              v-for="m in meta.members"
              :key="m.id"
              type="button"
              class="chip"
              :class="{ on: form.member_ids.includes(m.id) }"
              @click="toggleMember(m.id)"
            >
              {{ m.name }}
            </button>
            <span v-if="!meta.members.length" class="f-hint">暂无成员，请到「班组数据」维护</span>
          </div>
        </template>
        <div v-else class="f-hint">未出车为站内作业，不参与验证，不可选目的地与用车人</div>

        <!-- 巡视内容 -->
        <label class="f-lab">巡视内容（可空）</label>
        <textarea
          v-model="form.patrol_content"
          class="f-ta"
          rows="3"
          placeholder="如 110kVXX线巡视"
        ></textarea>

        <!-- 备注 + 附件（编辑模式才可改） -->
        <template v-if="entry">
          <label class="f-lab">备注（可空）</label>
          <textarea v-model="form.remark" class="f-ta" rows="2" placeholder="备注说明"></textarea>

          <label class="f-lab">备注附件</label>
          <div v-if="mediaFiles.length" class="att-grid">
            <span v-for="(f, i) in mediaFiles" :key="f.cos_key" class="att-cell">
              <span class="att-thumb">
                <img v-if="f.type === 'image'" :src="f.url" alt="附件" />
                <span v-else class="att-play"><WlIcon name="play" :size="22" /></span>
              </span>
              <button type="button" class="att-x" aria-label="移除附件" @click="removeFile(f)">
                <WlIcon name="close" :size="10" />
              </button>
            </span>
            <button type="button" class="att-add" :disabled="uploadingFile" @click="pickRemarkFile">
              <WlIcon :name="uploadingFile ? 'refresh' : 'add'" :size="17" />
              {{ uploadingFile ? '上传中' : '添加' }}
            </button>
          </div>
          <div v-if="docFiles.length" class="att-docs">
            <div v-for="f in docFiles" :key="f.cos_key" class="att-docrow">
              <WlIcon name="file" :size="13" />
              <span class="nm">{{ f.name }}</span>
              <button type="button" class="att-x" aria-label="移除附件" @click="removeFile(f)">
                <WlIcon name="close" :size="11" />
              </button>
            </div>
          </div>
          <button
            v-if="!mediaFiles.length"
            type="button"
            class="att-add"
            style="margin-top: 8px"
            :disabled="uploadingFile"
            @click="pickRemarkFile"
          >
            <WlIcon :name="uploadingFile ? 'refresh' : 'add'" :size="17" />
            {{ uploadingFile ? '上传中' : '添加附件' }}
          </button>
          <input ref="remarkInput" type="file" class="wl-hidden" @change="onRemarkFile" />
          <div class="f-hint">最多 9 个（图片/视频/Office 文档，单个 50MB 以内）</div>
        </template>

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
import { computed, reactive, ref, watch } from 'vue';
import WlIcon from './WlIcon.vue';
import { createLog, updateLog, uploadRemarkFile } from '../api.js';

// 新建/编辑日志弹层（旧版 .modal 弹层骨架 + f-lab/f-sel/chips 密度）：
// 新建 = 日期 + 派车/目的地/派车单号/用车人 + 巡视内容；编辑另可改备注与附件。
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

// mediaFiles/docFiles 附件分组。
const mediaFiles = computed(() => (form.remark_files || []).filter((f) => f.type !== 'doc'));
const docFiles = computed(() => (form.remark_files || []).filter((f) => f.type === 'doc'));

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

// markSuffix 目的地选项费用口径后缀（市外 / 驻地互斥，默认市内不标注）。
function markSuffix(d) {
  if (d.out_of_city === 1) return '（市外）';
  if (d.stationed === 1) return '（驻地）';
  return '';
}

// destMarkText / destMarkCls / destStdText 当前选中目的地的费用口径标记与标准提示。
const destMarkText = computed(() => {
  const d = (props.meta.destinations || []).find((x) => x.id === form.destination_id);
  if (!d) return '';
  if (d.out_of_city === 1) return '市外';
  if (d.stationed === 1) return '驻地';
  return '';
});
const destMarkCls = computed(() => (destMarkText.value === '市外' ? 'out' : 'sta'));
const destStdText = computed(() => {
  if (destMarkText.value === '市外') return '费用口径：伙食 100 / 交通 0';
  if (destMarkText.value === '驻地') return '费用口径：伙食 40 / 交通 0';
  return '费用口径：市内（伙食 60 / 交通 0）';
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

// removeFile 移除附件（本地移除，保存时 PUT 落库）。
function removeFile(f) {
  const i = form.remark_files.indexOf(f);
  if (i >= 0) form.remark_files.splice(i, 1);
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

// close 关闭弹层。
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
  background: var(--tk-overlay, rgba(29, 33, 41, 0.45));
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: var(--tk-card, #ffffff);
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
  background: var(--tk-card, #ffffff);
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
}
.m-x:hover {
  background: var(--tk-ink);
  color: var(--tk-on-brand, #ffffff);
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 表单（旧版 f-lab / f-sel / f-ta） ===== */
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
  background: var(--tk-card, #ffffff);
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.f-sel {
  height: 36px;
  padding: 0 10px;
}
/* 原生下拉（select）：自绘箭头、悬停/聚焦着色，保持原生可展开 */
select.f-drop {
  padding: 0 30px 0 10px;
  background-color: #fff;
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

/* 用车人 chips（点亮蓝底白字） */
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.chip {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card, #ffffff);
  color: var(--tk-ink);
  font-size: 12.5px;
  padding: 4px 13px;
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.2s,
    color 0.2s,
    background 0.2s;
}
.chip:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.chip.on {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: var(--tk-on-brand, #ffffff);
  font-weight: 700;
}
.chip:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

/* 备注附件：缩略图 × 移除 + 虚线添加钮；文档文件条 */
.att-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 8px;
}
.att-cell {
  position: relative;
  width: 56px;
  height: 56px;
}
.att-thumb {
  width: 56px;
  height: 56px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  overflow: hidden;
  background: var(--tk-ink);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--tk-on-brand, #ffffff);
}
.att-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.att-x {
  position: absolute;
  top: -6px;
  right: -6px;
  width: 18px;
  height: 18px;
  border-radius: 8px;
  background: var(--tk-ink);
  color: var(--tk-on-brand, #ffffff);
  border: none;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
}
.att-add {
  width: 56px;
  height: 56px;
  border-radius: 8px;
  border: 2px dashed var(--tk-dash);
  background: transparent;
  color: var(--tk-ph);
  cursor: pointer;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 3px;
  font-size: 10px;
  letter-spacing: 0;
  font-family: inherit;
  transition:
    border-color 0.2s,
    color 0.2s;
}
.att-add:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.att-docs {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 8px;
}
.att-docrow {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 6px 10px;
  border: 1.5px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-bg);
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-ink);
}
.att-docrow > svg {
  flex: none;
  color: var(--tk-ph);
}
.att-docrow .nm {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.att-docrow .att-x {
  position: static;
  width: 20px;
  height: 20px;
  background: transparent;
  color: var(--tk-ph);
}
.att-docrow .att-x:hover {
  color: var(--tk-red);
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
  background: var(--tk-card, #ffffff);
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
  color: var(--tk-on-brand, #ffffff);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover:not([disabled]) {
  color: var(--tk-on-brand, #ffffff);
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
.wl-hidden {
  display: none;
}

/* ===== 目的地费用口径标记（市外 / 驻地互斥；同班组数据字典 dpill 口径） ===== */
.dest-mark-line {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.dest-mark {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 10.5px;
  font-weight: 700;
  font-style: normal;
  letter-spacing: 1px;
  line-height: 1.5;
}
.dest-mark.out {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.dest-mark.sta {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
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
