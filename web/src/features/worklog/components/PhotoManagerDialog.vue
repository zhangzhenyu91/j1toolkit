<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card">
      <h3 class="card-title">
        照片管理 · {{ entry.plate_no || '未出车' }}（{{ entry.log_date }}）
      </h3>

      <!-- 上传区 -->
      <div class="wl-upload">
        <div class="wl-field">
          <span class="field-label">选择照片</span>
          <input
            type="file"
            accept="image/jpeg,image/png"
            class="wl-file-input"
            @change="onPickFile"
          />
        </div>
        <img v-if="upload.preview" :src="upload.preview" class="wl-preview" alt="待上传预览" />
        <div class="wl-field">
          <span class="field-label">所属人名</span>
          <div class="wl-chips">
            <button
              v-for="n in memberNames"
              :key="n"
              type="button"
              class="wl-chip"
              :class="{ on: upload.members.includes(n) }"
              @click="toggleUploadMember(n)"
            >
              {{ n }}
            </button>
          </div>
        </div>
        <div class="wl-field">
          <span class="field-label">水印</span>
          <div class="wl-wm">
            <label class="wl-plain">
              <input v-model="upload.plain" type="checkbox" />
              非水印照片（免验证、不占每人一张）
            </label>
            <template v-if="!upload.plain">
              <div class="wl-wm-grid">
                <input
                  v-model="upload.wm.content"
                  type="text"
                  class="wl-input"
                  placeholder="施工内容"
                />
                <input
                  v-model="upload.wm.time"
                  type="text"
                  class="wl-input"
                  placeholder="拍摄时间 YYYY.MM.DD HH:mm"
                />
                <input
                  v-model="upload.wm.weather"
                  type="text"
                  class="wl-input"
                  placeholder="天气（选填）"
                />
                <input
                  v-model="upload.wm.location"
                  type="text"
                  class="wl-input"
                  placeholder="地点（选填）"
                />
              </div>
              <div class="muted">
                填「施工内容 + 拍摄时间」后由服务端渲染水印；留空则按原图上传后走识别
              </div>
            </template>
          </div>
        </div>
        <div class="wl-modal-foot">
          <t-button
            size="small"
            :disabled="!upload.dataUrl"
            :loading="uploading"
            @click="submitUpload"
          >
            上传照片
          </t-button>
        </div>
      </div>

      <div v-if="error" class="wl-error">{{ error }}</div>

      <!-- 照片列表 -->
      <div v-for="p in entry.photos || []" :key="p.id" class="wl-photo-row">
        <img :src="p.url" class="wl-thumb" loading="lazy" alt="照片" />
        <div class="wl-photo-main">
          <div class="wl-photo-tags">
            <span class="dot-tag" :class="statusClass(p)">
              <span class="dot"></span>{{ statusText(p) }}
            </span>
            <span v-if="p.is_watermark === 0" class="dot-tag"><span class="dot"></span>非水印</span>
            <span v-if="p.date_ok === 0" class="dot-tag red"
              ><span class="dot"></span>日期不符</span
            >
            <span v-if="p.dest_ok === 0" class="dot-tag red"
              ><span class="dot"></span>地点不符</span
            >
          </div>

          <!-- 人名 -->
          <div class="wl-row-line">
            <span class="field-label">人名</span>
            <div class="wl-chips">
              <button
                v-for="n in memberNames"
                :key="n"
                type="button"
                class="wl-chip"
                :class="{ on: (p.members || []).includes(n) }"
                @click="togglePhotoMember(p, n)"
              >
                {{ n }}
              </button>
            </div>
          </div>

          <!-- 水印识别信息（可修正） -->
          <div v-if="p.is_watermark !== 0" class="wl-wm-grid">
            <input v-model="p.work_content" type="text" class="wl-input" placeholder="施工内容" />
            <input v-model="p.shot_time" type="text" class="wl-input" placeholder="拍摄时间" />
            <input v-model="p.weather" type="text" class="wl-input" placeholder="天气" />
            <input v-model="p.location" type="text" class="wl-input" placeholder="地点" />
            <input v-model="p.lng" type="text" class="wl-input num" placeholder="经度" />
            <input v-model="p.lat" type="text" class="wl-input num" placeholder="纬度" />
          </div>

          <div class="wl-photo-foot">
            <button
              v-if="p.verify_status === 'failed'"
              type="button"
              class="wl-act"
              @click="reverify(p)"
            >
              重新验证
            </button>
            <button
              v-if="p.is_watermark !== 0 && p.verify_status !== 'pending'"
              type="button"
              class="wl-act"
              @click="saveWM(p)"
            >
              保存水印信息
            </button>
            <button type="button" class="wl-act" @click="downloadOne(p)">下载</button>
            <button type="button" class="wl-act danger" @click="removePhoto(p)">删除</button>
          </div>
        </div>
      </div>
      <div v-if="!(entry.photos || []).length" class="muted">暂无照片</div>

      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">关闭</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue';
import {
  deletePhoto,
  downloadPhoto,
  reverifyPhoto,
  updatePhotoMembers,
  updatePhotoWM,
  uploadPhoto,
} from '../api.js';

// 照片管理弹窗：上传（base64→POST）/ 人名调整 / 水印信息修正 / 重新验证 / 下载 / 删除。
// 成功后抛 changed 由父视图刷新卡片；本组件对 entry.photos 就地更新做即时反馈。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, required: true },
});
const emit = defineEmits(['update:open', 'changed']);

const error = ref('');
const uploading = ref(false);
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
    upload.dataUrl = '';
    upload.preview = '';
    upload.members = [];
    upload.plain = false;
    upload.wm = { content: '', time: '', weather: '', location: '' };
  }
);

// onPickFile 读取本地照片为 dataURL（jpeg/png）。
function onPickFile(ev) {
  const file = ev.target.files && ev.target.files[0];
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
    upload.dataUrl = '';
    upload.preview = '';
    upload.members = [];
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

// close 关闭弹窗。
function close() {
  emit('update:open', false);
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
  width: min(720px, calc(100vw - 32px));
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-upload {
  background: var(--tk-fill);
  padding: 12px;
  margin-bottom: 12px;
}
.wl-field {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 6px 0;
}
.wl-field .field-label {
  width: 64px;
  flex-shrink: 0;
  padding-top: 5px;
}
.wl-file-input {
  flex: 1;
  font-size: 12px;
  color: var(--tk-sub);
}
.wl-preview {
  width: 96px;
  height: 96px;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
}
.wl-chips {
  flex: 1;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.wl-chip {
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid var(--tk-line);
  background: #fff;
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
.wl-wm {
  flex: 1;
}
.wl-plain {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--tk-sub);
}
.wl-wm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 6px;
  margin-top: 6px;
}
.wl-input {
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 12px;
  color: var(--tk-ink);
  background: #fff;
}
.wl-input:focus {
  outline: none;
  border-color: var(--tk-brand);
}
.wl-photo-row {
  display: flex;
  gap: 12px;
  padding: 12px 0;
  border-bottom: 1px solid var(--tk-line);
}
.wl-thumb {
  width: 96px;
  height: 96px;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  flex-shrink: 0;
}
.wl-photo-main {
  flex: 1;
  min-width: 0;
}
.wl-photo-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.wl-row-line {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  margin-top: 8px;
}
.wl-row-line .field-label {
  width: 40px;
  flex-shrink: 0;
  padding-top: 5px;
}
.wl-photo-foot {
  margin-top: 8px;
  display: flex;
  gap: 14px;
}
.wl-act {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  cursor: pointer;
}
.wl-act.danger {
  color: var(--tk-red);
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.wl-modal-foot {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
