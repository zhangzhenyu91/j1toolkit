<template>
  <div v-if="open" class="mask open" @click.self="close">
    <div class="modal wide">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title"
            >照片管理 · {{ entry.plate_no || '未出车' }}（{{ entry.log_date }}）</span
          >
          <button type="button" class="m-x" @click="close">
            <WlIcon name="close" :size="14" />
          </button>
        </div>

        <!-- 上传区：方式二选一（旧版 sheet-opt 行） -->
        <div class="dsec-h"><b>添加照片</b></div>
        <button type="button" class="sheet-opt" @click="pickImage">
          <span class="ic"><WlIcon name="camera" :size="19" /></span>
          <span>
            <b>选择照片并添加水印</b>
            <small>填「施工内容 + 拍摄时间」后由服务端渲染水印；留空则按原图上传后走识别</small>
          </span>
          <WlIcon name="chevron-right" :size="15" />
        </button>
        <button type="button" class="sheet-opt" @click="pickPlain">
          <span class="ic"><WlIcon name="image" :size="19" /></span>
          <span>
            <b>上传已带水印照片 / 原图</b>
            <small>作为非水印照片存档并同步商旅，免验证、不占每人一张</small>
          </span>
          <WlIcon name="chevron-right" :size="15" />
        </button>
        <input
          ref="fileInput"
          type="file"
          accept="image/jpeg,image/png"
          class="wl-hidden"
          @change="onPickFile"
        />

        <template v-if="upload.dataUrl">
          <img :src="upload.preview" class="wm-thumb" alt="待上传预览" />
          <label class="f-lab">所属人名（点亮即选中）</label>
          <div class="chips">
            <button
              v-for="n in memberNames"
              :key="n"
              type="button"
              class="chip"
              :class="{ on: upload.members.includes(n) }"
              @click="toggleUploadMember(n)"
            >
              {{ n }}
            </button>
          </div>
          <template v-if="!upload.plain">
            <label class="f-lab">水印字段（可空，留空走识别）</label>
            <div class="f-grid">
              <input v-model="upload.wm.content" type="text" class="f-sel" placeholder="施工内容" />
              <input
                v-model="upload.wm.time"
                type="text"
                class="f-sel"
                placeholder="拍摄时间 YYYY.MM.DD HH:mm"
              />
              <input
                v-model="upload.wm.weather"
                type="text"
                class="f-sel"
                placeholder="天气（选填）"
              />
              <input
                v-model="upload.wm.location"
                type="text"
                class="f-sel"
                placeholder="地点（选填）"
              />
            </div>
          </template>
          <div class="m-foot">
            <button type="button" class="btn btn-sm" @click="cancelUpload">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="!upload.dataUrl || uploading"
              @click="submitUpload"
            >
              {{ uploading ? '上传中…' : '上传照片' }}
            </button>
          </div>
        </template>

        <div v-if="error" class="wl-error">{{ error }}</div>

        <!-- 照片列表 -->
        <div class="dsec-h" style="margin-top: 16px">
          <b>已有照片（{{ (entry.photos || []).length }}）</b>
        </div>
        <div v-for="p in entry.photos || []" :key="p.id" class="pa-cmp-row">
          <img :src="p.url" class="wm-thumb big" loading="lazy" alt="照片" />
          <div class="photo-main">
            <div class="lb-row">
              <div class="k">验证情况</div>
              <div
                class="v"
                :class="statusClass(p) === 'green' ? 'good' : statusClass(p) === 'red' ? 'bad' : ''"
              >
                {{ statusText(p) }}<template v-if="p.is_watermark === 0">（非水印）</template>
                <template v-if="p.date_ok === 0">（日期不符）</template>
                <template v-if="p.dest_ok === 0">（地点不符）</template>
              </div>
            </div>

            <!-- 人名（点亮即改，即时保存） -->
            <div class="lb-row">
              <div class="k">人名（点击切换，即时保存）</div>
              <div class="chips">
                <button
                  v-for="n in memberNames"
                  :key="n"
                  type="button"
                  class="chip sm"
                  :class="{ on: (p.members || []).includes(n) }"
                  @click="togglePhotoMember(p, n)"
                >
                  {{ n }}
                </button>
              </div>
            </div>

            <!-- 水印识别信息（可修正） -->
            <template v-if="p.is_watermark !== 0">
              <label class="f-lab">水印识别信息（改后点「保存水印信息」重新核验）</label>
              <div class="f-grid">
                <input v-model="p.work_content" type="text" class="f-sel" placeholder="施工内容" />
                <input v-model="p.shot_time" type="text" class="f-sel" placeholder="拍摄时间" />
                <input v-model="p.weather" type="text" class="f-sel" placeholder="天气" />
                <input v-model="p.location" type="text" class="f-sel" placeholder="地点" />
                <input v-model="p.lng" type="text" class="f-sel num" placeholder="经度" />
                <input v-model="p.lat" type="text" class="f-sel num" placeholder="纬度" />
              </div>
            </template>

            <div class="lb-acts-row">
              <button
                v-if="p.verify_status === 'failed'"
                type="button"
                class="btn btn-sm"
                @click="reverify(p)"
              >
                <WlIcon name="refresh" :size="13" />重新验证
              </button>
              <button
                v-if="p.is_watermark !== 0 && p.verify_status !== 'pending'"
                type="button"
                class="btn btn-sm"
                @click="saveWM(p)"
              >
                <WlIcon name="check" :size="13" />保存水印信息
              </button>
              <button type="button" class="btn btn-sm" @click="downloadOne(p)">
                <WlIcon name="download" :size="13" />下载
              </button>
              <button type="button" class="btn btn-sm act-danger" @click="removePhoto(p)">
                <WlIcon name="delete" :size="13" />删除
              </button>
            </div>
          </div>
        </div>
        <div v-if="!(entry.photos || []).length" class="f-hint">暂无照片</div>

        <div class="m-foot">
          <button type="button" class="btn btn-sm" @click="close">关 闭</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue';
import WlIcon from './WlIcon.vue';
import {
  deletePhoto,
  downloadPhoto,
  reverifyPhoto,
  updatePhotoMembers,
  updatePhotoWM,
  uploadPhoto,
} from '../api.js';

// 照片管理弹层（旧版上传 sheet + 照片直改密度还原）：上传（水印 / 非水印二选一）/
// 人名调整 / 水印信息修正 / 重新验证 / 下载 / 删除。
// 成功后抛 changed 由父视图刷新卡片；本组件对 entry.photos 就地更新做即时反馈。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, required: true },
});
const emit = defineEmits(['update:open', 'changed']);

const error = ref('');
const uploading = ref(false);
const fileInput = ref(null);
const upload = reactive({
  dataUrl: '',
  preview: '',
  members: [],
  plain: false,
  wm: { content: '', time: '', weather: '', location: '' },
});

// memberNames 卡片用车人姓名集（照片人名只能从用车人中选）。
const memberNames = computed(() => (props.entry.members || []).map((m) => m.name));

// open 变化时重置上传区与错误。
watch(
  () => props.open,
  () => {
    error.value = '';
    uploading.value = false;
    cancelUpload();
  }
);

// pickImage / pickPlain 两种上传方式（决定 plain 标记后触发同一文件选择）。
function pickImage() {
  upload.plain = false;
  fileInput.value && fileInput.value.click();
}
function pickPlain() {
  upload.plain = true;
  fileInput.value && fileInput.value.click();
}

// cancelUpload 清空待上传区。
function cancelUpload() {
  upload.dataUrl = '';
  upload.preview = '';
  upload.members = [];
  upload.wm = { content: '', time: '', weather: '', location: '' };
}

// onPickFile 读取本地照片为 dataURL（jpeg/png）。
function onPickFile(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  const reader = new FileReader();
  reader.onload = () => {
    upload.dataUrl = String(reader.result || '');
    upload.preview = upload.dataUrl;
  };
  reader.readAsDataURL(file);
}

// toggleUploadMember 上传人名多选。
function toggleUploadMember(name) {
  const i = upload.members.indexOf(name);
  if (i >= 0) upload.members.splice(i, 1);
  else upload.members.push(name);
}

// togglePhotoMember 已有照片人名即时保存（PUT /photos/{id}/members）。
async function togglePhotoMember(photo, name) {
  error.value = '';
  const cur = photo.members || [];
  const next = cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name];
  if (!next.length) {
    error.value = '请选择照片所属人名';
    return;
  }
  try {
    const data = await updatePhotoMembers(photo.id, next);
    photo.members = data.members || next;
    emit('changed');
  } catch (e) {
    error.value = e.message || '人名调整失败';
  }
}

// submitUpload 上传照片（plain → 非水印；否则带 wm 时服务端渲染水印）。
async function submitUpload() {
  error.value = '';
  if (!upload.dataUrl) {
    error.value = '请先选择照片';
    return;
  }
  if (!upload.members.length) {
    error.value = '请选择照片所属人名';
    return;
  }
  uploading.value = true;
  try {
    const payload = {
      image: upload.dataUrl,
      members: upload.members,
      plain: upload.plain || undefined,
    };
    if (!upload.plain && (upload.wm.content || upload.wm.time)) {
      payload.wm = {
        content: upload.wm.content,
        time: upload.wm.time,
        weather: upload.wm.weather,
        location: upload.wm.location,
      };
    }
    await uploadPhoto(props.entry.id, payload);
    cancelUpload();
    emit('changed');
  } catch (e) {
    error.value = e.message || '上传失败';
  } finally {
    uploading.value = false;
  }
}

// saveWM 保存水印识别信息（服务端重算核验）。
async function saveWM(photo) {
  error.value = '';
  try {
    const data = await updatePhotoWM(photo.id, {
      workContent: photo.work_content,
      shotTime: photo.shot_time,
      weather: photo.weather,
      location: photo.location,
      lng: photo.lng,
      lat: photo.lat,
    });
    photo.verify_status = data.verify_status;
    photo.date_ok = data.date_ok;
    photo.dest_ok = data.dest_ok;
    emit('changed');
  } catch (e) {
    error.value = e.message || '保存失败';
  }
}

// reverify 重新验证（仅 failed）。
async function reverify(photo) {
  error.value = '';
  try {
    const data = await reverifyPhoto(photo.id);
    photo.verify_status = data.verify_status || 'pending';
    emit('changed');
  } catch (e) {
    error.value = e.message || '重新验证失败';
  }
}

// downloadOne 单张下载（fetch+blob 带鉴权）。
async function downloadOne(photo) {
  error.value = '';
  try {
    await downloadPhoto(photo.id, `photo-${photo.id}.jpg`);
  } catch (e) {
    error.value = e.message || '下载失败';
  }
}

// removePhoto 删除照片。
async function removePhoto(photo) {
  error.value = '';
  try {
    await deletePhoto(photo.id);
    emit('changed');
  } catch (e) {
    error.value = e.message || '删除失败';
  }
}

// statusClass/statusText 照片验证状态标签。
function statusClass(p) {
  return (
    { passed: 'green', mismatch: 'red', failed: 'red', pending: 'blue', skipped: '' }[
      p.verify_status
    ] || ''
  );
}
function statusText(p) {
  return (
    { passed: '已验证', mismatch: '不符', failed: '失败', pending: '识别中', skipped: '免验证' }[
      p.verify_status
    ] || p.verify_status
  );
}

// close 关闭弹层。
function close() {
  emit('update:open', false);
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
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.modal.wide {
  width: 720px;
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

/* ===== 说明小节标题（红方块 + 标题） ===== */
.dsec-h {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0 10px;
}
.dsec-h::before {
  content: '';
  width: 8px;
  height: 8px;
  flex: none;
  background: var(--tk-red);
}
.dsec-h b {
  font-size: 13.5px;
  letter-spacing: 1px;
}

/* ===== 上传方式二选一选项行（直角墨边 + 硬影，hover 下沉吃影） ===== */
.sheet-opt {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  text-align: left;
  font-family: inherit;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card, #ffffff);
  padding: 13px 14px;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
  cursor: pointer;
  transition:
    transform 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.sheet-opt + .sheet-opt {
  margin-top: 10px;
}
.sheet-opt:hover {
  transform: translate(2px, 2px);
  box-shadow: var(--tk-shadow-card);
}
.sheet-opt .ic {
  width: 38px;
  height: 38px;
  flex: none;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.sheet-opt b {
  display: block;
  font-size: 13.5px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.sheet-opt small {
  display: block;
  margin-top: 3px;
  font-size: 11px;
  color: var(--tk-ph);
  line-height: 1.6;
  letter-spacing: 0.5px;
}
.sheet-opt > svg {
  margin-left: auto;
  flex: none;
  color: var(--tk-ph);
}

/* ===== 表单（f-lab / f-sel / chips） ===== */
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-sel {
  height: 36px;
  width: 100%;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card, #ffffff);
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
.f-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 8px;
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
  background: var(--tk-card, #ffffff);
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
  color: var(--tk-on-brand, #ffffff);
  font-weight: 700;
}
.chip.sm {
  padding: 3px 10px;
  font-size: 11.5px;
}

/* ===== 照片行（缩略图 + 信息列） ===== */
.pa-cmp-row {
  display: flex;
  gap: 14px;
  padding: 12px 0;
  border-bottom: 1px dashed var(--tk-hair, rgba(29, 33, 41, 0.06));
}
.pa-cmp-row:last-child {
  border-bottom: none;
}
.wm-thumb {
  width: 72px;
  height: 54px;
  flex: none;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
}
.wm-thumb.big {
  width: 120px;
  height: 90px;
}
.photo-main {
  flex: 1;
  min-width: 0;
}
.lb-row {
  padding: 6px 0;
  border-bottom: 1px dashed var(--tk-hair, rgba(29, 33, 41, 0.06));
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
.lb-acts-row {
  margin-top: 10px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
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
  padding: 0 12px;
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
.act-danger {
  color: var(--tk-red);
}
.act-danger:hover:not([disabled]) {
  background: var(--tk-red-soft);
  border-color: var(--tk-red);
  color: var(--tk-red);
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
