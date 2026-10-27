<template>
  <div v-if="open" class="lb open" @click.self="close">
    <button type="button" class="lb-x" title="关闭" @click="close">
      <WlIcon name="close" :size="17" />
    </button>
    <button
      v-if="many"
      type="button"
      class="lb-arrow l"
      :disabled="index <= 0"
      title="上一张"
      @click="step(-1)"
    >
      <WlIcon name="chevron-left" :size="18" />
    </button>
    <button
      v-if="many"
      type="button"
      class="lb-arrow r"
      :disabled="index >= photos.length - 1"
      title="下一张"
      @click="step(1)"
    >
      <WlIcon name="chevron-right" :size="18" />
    </button>

    <div v-if="photo" class="lb-main">
      <div class="lb-stage">
        <img class="lb-img show" :src="photo.url" :alt="photoAlt" />
        <div class="lb-count num">{{ index + 1 }} / {{ photos.length }}</div>
      </div>

      <aside class="lb-side" :class="{ 'wm-editing': editing }">
        <!-- 非水印：仅人名 + 说明 + 商旅同步 -->
        <template v-if="plain">
          <div class="lb-row">
            <div class="k">人名</div>
            <div class="v">{{ (photo.members || []).join('、') || '—' }}</div>
          </div>
          <div class="plain-note">非水印照片仅存档并同步商旅费用照片，不参与验证</div>
          <div v-if="syncRow" class="lb-row">
            <div class="k">商旅同步</div>
            <div class="v" v-html="syncRow"></div>
          </div>
        </template>

        <!-- 水印：核验字段行（手动修正时行内变输入框） -->
        <template v-else>
          <div class="lb-row">
            <div class="k">验证情况</div>
            <div class="v" :class="verifyCls">{{ verifyText }}</div>
          </div>
          <div class="lb-row">
            <div class="k">人名</div>
            <div class="v">{{ (photo.members || []).join('、') || '—' }}</div>
          </div>
          <div v-for="f in wmFields" :key="f.key" class="lb-row lb-edit-row">
            <div class="k">{{ f.label }}</div>
            <div class="v">{{ photo[f.key] || '—' }}</div>
            <input
              v-if="editing"
              v-model="wmForm[f.key]"
              class="lb-edit-field f-sel"
              :placeholder="f.ph"
            />
          </div>
          <div v-if="syncRow" class="lb-row">
            <div class="k">商旅同步</div>
            <div class="v" v-html="syncRow"></div>
          </div>
          <div v-if="!editing" class="acode">
            <WlIcon name="shield" :size="20" />
            <small>防伪码已随水印嵌印在照片画面中，放大照片即可核对 14 位防伪码</small>
          </div>
          <div v-if="editing" class="lb-edit-hint">
            改「施工内容 / 拍摄时间 / 地点」等识别错项后保存，服务端将重新核验。
          </div>
          <div v-if="editing" class="lb-edit-actions">
            <button type="button" class="btn btn-sm" @click="editing = false">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="wmSaving"
              @click="saveWM"
            >
              {{ wmSaving ? '保存中…' : '保存并重新核验' }}
            </button>
          </div>
        </template>

        <!-- 人名编辑（chips 点亮，保存即 PUT） -->
        <template v-if="editingMembers">
          <div class="lb-row">
            <div class="k">修改人名（点击点亮）</div>
            <div class="chips">
              <button
                v-for="n in memberNames"
                :key="n"
                type="button"
                class="chip"
                :class="{ on: memForm.includes(n) }"
                @click="toggleMem(n)"
              >
                {{ n }}
              </button>
            </div>
          </div>
          <div class="lb-edit-actions">
            <button type="button" class="btn btn-sm" @click="editingMembers = false">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="memSaving || !memForm.length"
              @click="saveMembers"
            >
              {{ memSaving ? '保存中…' : '保存人名' }}
            </button>
          </div>
        </template>

        <!-- 照片直改操作（同小程序） -->
        <div v-if="!editing && !editingMembers" class="lb-acts">
          <button
            v-if="photo.verify_status === 'failed'"
            type="button"
            class="btn btn-sm"
            :disabled="busy"
            @click="reverify"
          >
            <WlIcon name="refresh" :size="14" />重新验证
          </button>
          <button
            v-if="!plain && photo.verify_status !== 'pending'"
            type="button"
            class="btn btn-sm"
            @click="startEdit"
          >
            <WlIcon name="edit" :size="14" />手动修正
          </button>
          <button type="button" class="btn btn-sm" @click="download">
            <WlIcon name="download" :size="14" />下载照片
          </button>
          <button type="button" class="btn btn-sm" @click="startEditMembers">
            <WlIcon name="user" :size="14" />修改人名
          </button>
          <button type="button" class="btn btn-sm act-danger" :disabled="busy" @click="remove">
            <WlIcon name="delete" :size="14" />删除照片
          </button>
        </div>

        <div v-if="error" class="wl-error">{{ error }}</div>
      </aside>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import WlIcon from './WlIcon.vue';
import {
  deletePhoto,
  downloadPhoto,
  reverifyPhoto,
  updatePhotoMembers,
  updatePhotoWM,
} from '../api.js';
import { resyncPhoto } from '../../sgccclockin/api.js';

// 照片大图（旧版 .lb 结构还原）：全屏深色舞台 + 右侧信息卡（核验字段行 / 防伪码块 /
// 操作列）；支持重新验证、手动修正（行内改水印识别信息）、下载、修改人名、删除、
// 商旅同步失败重试（任务化，op 队列可见）。
const props = defineProps({
  open: { type: Boolean, default: false },
  photos: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  entryId: { type: Number, default: 0 },
  memberNames: { type: Array, default: () => [] }, // 可选人名集（缺省取 photo.members）
});
const emit = defineEmits(['update:open', 'update:index', 'op-started', 'changed']);

const error = ref('');
const busy = ref(false);
const editing = ref(false);
const editingMembers = ref(false);
const wmSaving = ref(false);
const memSaving = ref(false);
const wmForm = reactive({});
const memForm = ref([]);

const photo = computed(() => props.photos[props.index] || null);
const photoAlt = computed(
  () => ((photo.value && photo.value.members) || []).join('、') || '照片大图'
);
const many = computed(() => props.photos.length > 1);
const plain = computed(() => photo.value && photo.value.is_watermark === 0);

// wmFields 手动修正字段（施工内容 / 拍摄时间 / 天气 / 地点 / 经度 / 纬度）。
const wmFields = [
  { key: 'work_content', label: '施工内容', ph: '如 110kVXX线巡视' },
  { key: 'shot_time', label: '拍摄时间', ph: 'YYYY.MM.DD HH:mm' },
  { key: 'weather', label: '天气', ph: '如 晴 28℃' },
  { key: 'location', label: '地点', ph: '须包含派车目的地' },
  { key: 'lng', label: '经度', ph: '选填' },
  { key: 'lat', label: '纬度', ph: '选填' },
];

// verifyCls/verifyText 验证情况行（绿=核验通过 / 红=失败（日期、地点不符））。
const verifyCls = computed(() => {
  const p = photo.value;
  if (!p) return '';
  if (p.verify_status === 'passed') return 'good';
  if (p.verify_status === 'pending') return '';
  return 'bad';
});
const verifyText = computed(() => {
  const p = photo.value;
  if (!p) return '';
  if (p.verify_status === 'pending') return '验证中';
  if (p.verify_status === 'passed') return '核验通过';
  const mis = [];
  if (p.date_ok === 0 || (p.date_ok == null && p.verify_status === 'date_mismatch'))
    mis.push('日期不符');
  if (p.dest_ok === 0 || (p.dest_ok == null && p.verify_status === 'dest_mismatch'))
    mis.push('地点不符');
  return '验证失败' + (mis.length ? '（' + mis.join('、') + '）' : '');
});

// syncRow 商旅同步行（HTML：已同步绿 / 失败红+重试钮 / 未同步）。
const syncRow = computed(() => {
  const p = photo.value;
  if (!p || p.sgcc_synced === undefined || p.sgcc_synced === null) return '';
  if (p.sgcc_synced === 1) return '<span class="sg-ok">已同步</span>';
  if (p.sgcc_synced === 2) {
    return '<span class="sg-bad">同步失败</span> <button type="button" class="linkbtn" data-resync="1">重试</button>';
  }
  return '未同步';
});

// 事件委托：syncRow 里的「重试」按钮。
watch(
  () => props.open,
  (val) => {
    if (val) {
      error.value = '';
      editing.value = false;
      editingMembers.value = false;
    }
  }
);

function onDocClick(ev) {
  const b = ev.target.closest && ev.target.closest('[data-resync]');
  if (b && props.open) resync();
}
document.addEventListener('click', onDocClick);
onBeforeUnmount(() => document.removeEventListener('click', onDocClick));

// step 前后翻页。
function step(d) {
  const next = props.index + d;
  if (next < 0 || next >= props.photos.length) return;
  editing.value = false;
  editingMembers.value = false;
  emit('update:index', next);
}

// onKey 左右方向键翻页、Esc 关闭。
function onKey(ev) {
  if (!props.open) return;
  if (ev.key === 'Escape') close();
  else if (ev.key === 'ArrowLeft') step(-1);
  else if (ev.key === 'ArrowRight') step(1);
}
window.addEventListener('keydown', onKey);
onBeforeUnmount(() => window.removeEventListener('keydown', onKey));

// startEdit 手动修正：回填表单进入行内编辑。
function startEdit() {
  const p = photo.value;
  if (!p) return;
  wmFields.forEach((f) => {
    wmForm[f.key] = p[f.key] == null ? '' : String(p[f.key]);
  });
  editing.value = true;
  editingMembers.value = false;
}

// saveWM 保存水印识别信息（服务端重算核验）。
async function saveWM() {
  if (!photo.value || wmSaving.value) return;
  error.value = '';
  wmSaving.value = true;
  try {
    const data = await updatePhotoWM(photo.value.id, {
      workContent: wmForm.work_content,
      shotTime: wmForm.shot_time,
      weather: wmForm.weather,
      location: wmForm.location,
      lng: wmForm.lng,
      lat: wmForm.lat,
    });
    Object.assign(photo.value, {
      work_content: wmForm.work_content,
      shot_time: wmForm.shot_time,
      weather: wmForm.weather,
      location: wmForm.location,
      lng: wmForm.lng,
      lat: wmForm.lat,
      verify_status: data.verify_status,
      date_ok: data.date_ok,
      dest_ok: data.dest_ok,
    });
    editing.value = false;
    MessagePlugin.success('已保存并重新核验');
    emit('changed');
  } catch (e) {
    error.value = e.message || '保存失败';
  } finally {
    wmSaving.value = false;
  }
}

// startEditMembers 修改人名（chips 点亮）。
function startEditMembers() {
  const p = photo.value;
  if (!p) return;
  memForm.value = [...(p.members || [])];
  editingMembers.value = true;
  editing.value = false;
}

function toggleMem(n) {
  const i = memForm.value.indexOf(n);
  if (i >= 0) memForm.value.splice(i, 1);
  else memForm.value.push(n);
}

// saveMembers 保存照片人名。
async function saveMembers() {
  if (!photo.value || memSaving.value) return;
  if (!memForm.value.length) {
    error.value = '请选择照片所属人名';
    return;
  }
  error.value = '';
  memSaving.value = true;
  try {
    const data = await updatePhotoMembers(photo.value.id, memForm.value);
    photo.value.members = data.members || memForm.value;
    editingMembers.value = false;
    MessagePlugin.success('人名已更新');
    emit('changed');
  } catch (e) {
    error.value = e.message || '人名调整失败';
  } finally {
    memSaving.value = false;
  }
}

// reverify 重新验证（仅 failed）。
async function reverify() {
  if (!photo.value || busy.value) return;
  error.value = '';
  busy.value = true;
  try {
    const data = await reverifyPhoto(photo.value.id);
    photo.value.verify_status = data.verify_status || 'pending';
    MessagePlugin.success('已发起重新验证');
    emit('changed');
  } catch (e) {
    error.value = e.message || '重新验证失败';
  } finally {
    busy.value = false;
  }
}

// download 单张下载。
async function download() {
  if (!photo.value) return;
  error.value = '';
  try {
    await downloadPhoto(photo.value.id, `photo-${photo.value.id}.jpg`);
  } catch (e) {
    error.value = e.message || '下载失败';
  }
}

// remove 删除照片。
async function remove() {
  if (!photo.value || busy.value) return;
  error.value = '';
  busy.value = true;
  try {
    await deletePhoto(photo.value.id);
    MessagePlugin.success('已删除');
    emit('changed');
    close();
  } catch (e) {
    error.value = e.message || '删除失败';
  } finally {
    busy.value = false;
  }
}

// resync 商旅照片重新同步（任务化：opId 交父视图挂卡片任务锁）。
async function resync() {
  if (!photo.value) return;
  error.value = '';
  try {
    const data = await resyncPhoto(photo.value.id);
    MessagePlugin.success('已发起重新同步');
    emit('op-started', { opId: data && data.opId, entryId: props.entryId, kind: 'resync' });
  } catch (e) {
    error.value = e.message || '重试失败';
  }
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
/* ===== 大图遮罩层（Lightbox）：深色舞台 + 右侧信息卡 ===== */
.lb {
  position: fixed;
  inset: 0;
  z-index: 300;
  padding: 36px;
  background: rgba(29, 33, 41, 0.85);
  backdrop-filter: blur(6px);
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 18px;
}
.lb-main {
  display: flex;
  align-items: flex-start;
  gap: 18px;
  max-width: 100%;
}
.lb-stage {
  position: relative;
  max-width: min(68vw, 960px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.lb-img {
  max-width: 100%;
  max-height: 84vh;
  border-radius: 8px;
  border: 2px solid var(--tk-bg);
  box-shadow: var(--tk-shadow-card);
}
.lb-count {
  position: absolute;
  bottom: -30px;
  left: 50%;
  transform: translateX(-50%);
  color: rgba(255, 255, 255, 0.8);
  font-size: 12px;
  letter-spacing: 2px;
}
/* 遮罩上的方形白底墨边钮（关闭 / 左右切换），hover 下沉吃影 */
.lb-x,
.lb-arrow {
  position: absolute;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
  transition:
    transform 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.lb-x:hover,
.lb-arrow:hover {
  transform: translate(2px, 2px);
  box-shadow: var(--tk-shadow-card);
}
.lb-x {
  top: 18px;
  right: 18px;
  width: 38px;
  height: 38px;
}
.lb-arrow {
  top: 50%;
  translate: 0 -50%;
  width: 42px;
  height: 42px;
  z-index: 2;
}
.lb-arrow.l {
  left: 14px;
}
.lb-arrow.r {
  right: 14px;
}
.lb-arrow:disabled {
  opacity: 0.35;
  pointer-events: none;
}
/* 信息卡 */
.lb-side {
  width: 290px;
  flex: none;
  max-height: 84vh;
  overflow-y: auto;
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 18px 20px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.04);
}
.lb-side.wm-editing {
  border-color: var(--tk-brand);
}
.lb-row {
  padding: 8px 0;
  border-bottom: 1px dashed rgba(29, 33, 41, 0.06);
}
.lb-row:last-child {
  border-bottom: none;
}
.lb-row .k {
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin-bottom: 3px;
}
.lb-row .v {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.6;
  word-break: break-all;
}
.lb-row .v.good {
  color: var(--tk-green);
}
.lb-row .v.bad {
  color: var(--tk-red);
}
/* 手动修正：行内字段替换展示值 */
.lb-side.wm-editing .lb-edit-row > .v {
  display: none;
}
.lb-edit-field {
  display: block;
  height: 34px;
  margin-top: 4px;
  font-weight: 500;
  letter-spacing: 0;
}
.f-sel {
  width: 100%;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  padding: 0 10px;
}
.f-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.lb-edit-hint {
  color: var(--tk-ph);
  font-size: 11px;
  line-height: 1.7;
}
.lb-edit-actions {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}
.lb-edit-actions .btn {
  flex: 1;
  min-width: 0;
}
/* 防伪码提示 */
.acode {
  margin-top: 12px;
  border: 2px dashed var(--tk-dash);
  border-radius: 8px;
  padding: 12px 14px;
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--tk-bg);
}
.acode > svg {
  flex: none;
  color: var(--tk-brand);
}
.acode small {
  font-size: 10.5px;
  color: var(--tk-ph);
  line-height: 1.7;
  letter-spacing: 0.5px;
}
/* 非水印说明 */
.plain-note {
  margin-top: 10px;
  padding: 9px 12px;
  border-radius: 8px;
  background: var(--tk-bg);
  border: 1.5px solid var(--tk-line);
  font-size: 11.5px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  line-height: 1.7;
}
/* 操作列 */
.lb-acts {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--tk-line);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
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
  padding: 0 12px;
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
.lb-acts .btn {
  width: 100%;
}
.act-danger {
  color: var(--tk-red);
}
.act-danger:hover:not([disabled]) {
  background: var(--tk-red-soft);
  border-color: var(--tk-red);
  color: var(--tk-red);
}
/* 商旅同步行 / 失败行 */
.sg-ok {
  color: var(--tk-green);
  font-weight: 700;
}
.sg-bad {
  color: var(--tk-red);
  font-weight: 700;
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
}
.linkbtn:hover {
  background: var(--tk-blue-soft);
}
/* 人名 chips（点亮蓝底白字） */
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 6px;
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
.wl-error {
  margin-top: 10px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}

@media (max-width: 760px) {
  .lb {
    padding: 18px;
    overflow-y: auto;
    align-items: flex-start;
  }
  .lb-main {
    flex-direction: column;
    align-items: center;
  }
  .lb-side {
    width: 100%;
    max-height: none;
  }
}
</style>
