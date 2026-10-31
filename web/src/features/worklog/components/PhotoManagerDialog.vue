<template>
  <!-- 照片管理右侧抽屉（共享 Drawer）：固定标题行 + 独立滚动体 + 固定操作脚三段互不遮挡；
       遮罩点按不关（防误点丢输入），标题行关闭 / 取消 / Escape 同一收起链路播退出动画。 -->
  <Drawer :visible="open" :title="titleText" :width="720" @close="close">
    <div class="dwrap">
      <div class="dbody">
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
            <label class="f-lab">经纬度（选杆塔坐标自动带入并刷新天气、地点）</label>
            <div class="f-grid">
              <input
                v-model="upload.wm.lng"
                type="text"
                class="f-sel"
                placeholder="经度"
                @input="onCoordInput"
              />
              <input
                v-model="upload.wm.lat"
                type="text"
                class="f-sel"
                placeholder="纬度"
                @input="onCoordInput"
              />
              <button type="button" class="btn btn-sm" @click="toggleTower">
                <WlIcon name="location" :size="13" />
                {{ tower.picked ? `已选 ${tower.picked.no} · 重选` : '选择杆塔坐标' }}
              </button>
            </div>
            <div v-if="tower.open" class="tower-cascade">
              <div class="f-hint">
                依次选择电压等级 → 线路名称 → 杆塔号；所选坐标按 ≤50m 随机波动后填入，仍可手改
              </div>
              <select
                v-model="tower.level"
                class="f-sel"
                :disabled="tower.loading"
                @change="onTowerLevel"
              >
                <option value="">{{ tower.loading ? '杆塔坐标加载中…' : '请选择电压等级' }}</option>
                <option v-for="v in tower.levels" :key="v" :value="v">{{ v }}</option>
              </select>
              <select
                v-model="tower.line"
                class="f-sel"
                :disabled="!tower.level"
                @change="onTowerLine"
              >
                <option value="">请选择线路名称</option>
                <option v-for="n in tower.lines" :key="n" :value="n">{{ n }}</option>
              </select>
              <select
                v-model="tower.towerNo"
                class="f-sel"
                :disabled="!tower.line"
                @change="onTowerNo"
              >
                <option value="">请选择杆塔号</option>
                <option v-for="t in tower.towers" :key="t.no" :value="t.no">
                  {{ t.no }}（{{ t.lngText }}，{{ t.latText }}）
                </option>
              </select>
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

        <!-- 照片列表 -->
        <div class="dsec-h" style="margin-top: 16px">
          <b>已有照片（{{ (entry.photos || []).length }}）</b>
        </div>
        <div v-for="p in entry.photos || []" :key="p.id" class="pa-cmp-row">
          <img :src="p.url" class="wm-thumb big" loading="lazy" decoding="async" alt="照片" />
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
      </div>

      <div class="dfoot">
        <div v-if="error" class="wl-error">{{ error }}</div>
        <div class="foot-btns">
          <button type="button" class="btn btn-sm" @click="close">关 闭</button>
        </div>
      </div>
    </div>
  </Drawer>
</template>

<script setup>
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import { DialogPlugin } from 'tdesign-vue-next';
import Drawer from '../../../components/Drawer.vue';
import WlIcon from './WlIcon.vue';
import { deletePhotoConfirmText } from '../logic.js';
import {
  deletePhoto,
  downloadPhoto,
  fetchGeo,
  fetchTowers,
  getTeamId,
  reverifyPhoto,
  updatePhotoMembers,
  updatePhotoWM,
  uploadPhoto,
} from '../api.js';

// 照片管理弹层（共享 Drawer 右侧抽屉 + 旧版上传 sheet / 照片直改密度还原）：上传（水印 / 非水印二选一）/
// 人名调整 / 水印信息修正 / 重新验证 / 下载 / 删除。
// 成功后抛 changed 由父视图刷新卡片；本组件对 entry.photos 就地更新做即时反馈。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, required: true },
});
const emit = defineEmits(['update:open', 'changed']);

// titleText 抽屉标题（照片管理 · 派车车牌/未出车（日志日期））。
const titleText = computed(
  () => `照片管理 · ${props.entry?.plate_no || '未出车'}（${props.entry?.log_date}）`
);

const error = ref('');
const uploading = ref(false);
const fileInput = ref(null);
const upload = reactive({
  dataUrl: '',
  preview: '',
  members: [],
  plain: false,
  wm: { content: '', time: '', weather: '', location: '', lng: '', lat: '' },
});

// ---------- 杆塔坐标级联（旧版「选择杆塔坐标」口径）----------
// 电压等级 → 线路名称 → 杆塔号 三级选择；选中后坐标按 ≤50m 随机波动填入经纬度，
// 并按填入坐标调 /geo（高德）覆盖刷新天气、地点；手动改经纬度 800ms 防抖同口径刷新。
const tower = reactive({
  open: false,
  loading: false,
  rows: null, // 全量行 [电压等级, 线路名称, 杆塔号, 经度, 纬度]
  levels: [],
  lines: [],
  towers: [], // 当前线路杆塔项 [{ no, lng, lat, lngText, latText }]
  level: '',
  line: '',
  towerNo: '',
  picked: null, // 已选杆塔 { level, line, no }
});
const towerCache = new Map(); // teamId -> rows（内存缓存，同旧版 state.towerRows 口径）
let geoTimer = 0;
let geoKey = ''; // 「上次取值坐标」记录（手动改经纬度防抖刷新去重用）

// jitterCoord 经纬度随机偏移：角度随机，半径 ≤maxMeters（杆塔带入取 50，同小程序）。
function jitterCoord(lng, lat, maxMeters) {
  const r = Math.random() * maxMeters;
  const a = Math.random() * Math.PI * 2;
  const dLat = (r * Math.sin(a)) / 111320;
  const cosLat = Math.cos((lat * Math.PI) / 180);
  const dLng = (r * Math.cos(a)) / (111320 * (Math.abs(cosLat) > 1e-6 ? cosLat : 1e-6));
  return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
}

// withDegSuffix 经纬度补方向后缀：只填数字时自动补 °E/°N（已带符号则原样，同小程序）。
function withDegSuffix(v, suffix) {
  const s = String(v || '').trim();
  if (!s) return '';
  return /[°NSEWnsew]/.test(s) ? s : `${s}${suffix}`;
}

// refreshGeo 经纬度变化统一调 /geo 覆盖刷新天气/地点；失败清空留空手填（同旧版口径）。
// 响应仅在表单经纬度未被再次改动时应用，避免旧响应覆盖新输入。
async function refreshGeo(lng, lat) {
  const nlng = parseFloat(lng);
  const nlat = parseFloat(lat);
  geoKey = `${nlng},${nlat}`;
  try {
    const r = await fetchGeo(nlng, nlat);
    if (parseFloat(upload.wm.lng) !== nlng || parseFloat(upload.wm.lat) !== nlat) return;
    upload.wm.weather = (r && r.weather) || '';
    upload.wm.location = (r && r.location) || '';
  } catch (e) {
    console.error('[照片管理] /geo 地点天气刷新失败（留空手填）：', e);
    if (parseFloat(upload.wm.lng) !== nlng || parseFloat(upload.wm.lat) !== nlat) return;
    upload.wm.weather = '';
    upload.wm.location = '';
  }
}

// onCoordInput 手动改经纬度：停顿 800ms 防抖后按新坐标刷新天气/地点；非法或与上次取值相同不请求。
function onCoordInput() {
  clearTimeout(geoTimer);
  geoTimer = setTimeout(() => {
    const lng = parseFloat(upload.wm.lng);
    const lat = parseFloat(upload.wm.lat);
    if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90)
      return;
    if (`${lng},${lat}` === geoKey) return;
    refreshGeo(lng, lat);
  }, 800);
}

// loadTowerRows 杆塔坐标全量：内存缓存优先（按生效班组隔离），无缓存请求服务端。
async function loadTowerRows() {
  const key = getTeamId() || 0;
  if (towerCache.has(key)) return towerCache.get(key);
  const data = await fetchTowers();
  const rows = Array.isArray(data) ? data : (data && data.rows) || [];
  if (!rows.length) throw new Error('杆塔坐标数据为空');
  towerCache.set(key, rows);
  return rows;
}

// toggleTower 开关级联面板；首次打开先加载数据（失败关面板提示，已选状态保留供重选）。
async function toggleTower() {
  if (tower.open) {
    tower.open = false;
    return;
  }
  tower.open = true;
  if (tower.rows) return;
  tower.loading = true;
  try {
    tower.rows = await loadTowerRows();
    tower.levels = [...new Set(tower.rows.map((r) => r[0]))];
  } catch (e) {
    tower.open = false;
    error.value = e.message === '杆塔坐标数据为空' ? e.message : '杆塔坐标加载失败，请稍后重试';
  } finally {
    tower.loading = false;
  }
}

// onTowerLevel / onTowerLine 选中上级后清空下级并出下级选项。
function onTowerLevel() {
  tower.line = '';
  tower.towerNo = '';
  tower.towers = [];
  tower.lines = [
    ...new Set((tower.rows || []).filter((r) => r[0] === tower.level).map((r) => r[1])),
  ];
}
function onTowerLine() {
  tower.towerNo = '';
  tower.towers = (tower.rows || [])
    .filter((r) => r[0] === tower.level && r[1] === tower.line)
    .map((r) => ({
      no: r[2],
      lng: r[3],
      lat: r[4],
      lngText: Number(r[3]).toFixed(6),
      latText: Number(r[4]).toFixed(6),
    }));
}

// onTowerNo 选中杆塔：坐标 ≤50m 随机波动填入经纬度，并按填入坐标刷新天气、地点。
function onTowerNo() {
  const t = tower.towers.find((x) => String(x.no) === String(tower.towerNo));
  if (!t) return;
  const jd = jitterCoord(Number(t.lng), Number(t.lat), 50);
  tower.picked = { level: tower.level, line: tower.line, no: t.no };
  upload.wm.lng = jd.lng;
  upload.wm.lat = jd.lat;
  tower.open = false;
  refreshGeo(jd.lng, jd.lat);
}

// resetTower 重置级联选择态（rows 缓存保留，供下次直接打开）。
function resetTower() {
  clearTimeout(geoTimer);
  tower.open = false;
  tower.level = '';
  tower.line = '';
  tower.towerNo = '';
  tower.levels = [];
  tower.lines = [];
  tower.towers = [];
  tower.picked = null;
}

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

// cancelUpload 清空待上传区与杆塔选择态。
function cancelUpload() {
  if (upload.preview) URL.revokeObjectURL(upload.preview);
  upload.dataUrl = '';
  upload.preview = '';
  upload.members = [];
  upload.wm = { content: '', time: '', weather: '', location: '', lng: '', lat: '' };
  resetTower();
}

// onPickFile 保留原始 File 二进制上传，本地仅用 objectURL 预览。
function onPickFile(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  if (!/^image\/(jpeg|png)$/i.test(file.type || '')) {
    error.value = '照片格式应为 jpeg/png';
    return;
  }
  if (file.size > 50 * 1024 * 1024) {
    error.value = '照片大小应在 50MB 以内';
    return;
  }
  if (upload.preview) URL.revokeObjectURL(upload.preview);
  upload.dataUrl = file;
  upload.preview = URL.createObjectURL(file);
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
    const wm = {
      content: upload.wm.content,
      time: upload.wm.time,
      weather: upload.wm.weather,
      location: upload.wm.location,
      longitude: withDegSuffix(upload.wm.lng, '°E'),
      latitude: withDegSuffix(upload.wm.lat, '°N'),
    };
    if (!upload.plain && Object.values(wm).some((v) => String(v || '').trim())) {
      payload.wm = wm;
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

// removePhoto 删除照片（二次确认：避免误点直接删掉水印照片与核验记录）。
function removePhoto(photo) {
  error.value = '';
  const confirm = DialogPlugin.confirm({
    header: '删除照片',
    body: deletePhotoConfirmText(photo),
    confirmBtn: { content: '确认删除', theme: 'danger' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      try {
        await deletePhoto(photo.id);
        emit('changed');
      } catch (e) {
        error.value = e.message || '删除失败';
      } finally {
        confirm.destroy();
      }
    },
  });
}

// 组件卸载：清掉地理防抖与待上传预览 objectURL，避免离页后内存与请求残留。
onBeforeUnmount(() => {
  clearTimeout(geoTimer);
  if (upload.preview) URL.revokeObjectURL(upload.preview);
  upload.preview = '';
});

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

/* ===== 抽屉内三段骨架：独立滚动体 + 固定操作脚（固定标题行由共享 Drawer 承担，互不遮挡） ===== */
.dwrap {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.dbody {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.dbody::-webkit-scrollbar {
  width: 5px;
}
.dbody::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.dbody::-webkit-scrollbar-track {
  background: transparent;
}
.dfoot {
  flex: none;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--tk-line);
  background: var(--tk-card, #ffffff);
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.foot-btns {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
/* 上传区就地操作行（取消 / 上传照片），留在滚动体内随表单走 */
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
/* ===== 杆塔坐标级联（选择杆塔坐标） ===== */
.tower-cascade {
  margin-top: 10px;
  display: grid;
  gap: 8px;
}
.tower-cascade .f-hint {
  margin-top: 0;
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
  margin-top: 0;
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
