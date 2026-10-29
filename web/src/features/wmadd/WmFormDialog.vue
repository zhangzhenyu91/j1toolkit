<template>
  <!-- 复杂信息录入：共享右侧抽屉壳 Drawer（遮罩淡入 + 面板进出场、Esc / 焦点圈定 / 内层滚动） -->
  <Drawer
    :visible="open"
    title="编辑水印信息"
    :width="560"
    :close-on-overlay-click="true"
    @update:visible="close"
  >
    <div class="wm-head">
      <img class="wm-thumb" :src="image" alt="待加水印照片" />
      <div>
        <div class="wm-names">服务端渲染水印</div>
        <div class="wm-names-tip">确认后仅返回图片，不上传、不存档</div>
      </div>
    </div>

    <label class="f-lab">施工内容</label>
    <div class="chips">
      <button
        v-for="t in QUICK_INPUTS"
        :key="t"
        type="button"
        class="chip chip-sm"
        @click="appendQuick(t)"
      >
        {{ t }}
      </button>
    </div>
    <textarea
      v-model="content"
      class="f-ta"
      maxlength="500"
      placeholder="请输入施工内容"
      style="min-height: 76px; margin-top: 8px"
    ></textarea>

    <label class="f-lab">拍摄时间</label>
    <input v-model="time" class="f-sel" maxlength="16" placeholder="YYYY.MM.DD HH:mm" />

    <label class="f-lab">天气<span v-if="updWeather" class="wm-upd">已更新</span></label>
    <input v-model="weather" class="f-sel" maxlength="64" placeholder="请输入天气" />

    <label class="f-lab">地点<span v-if="updLocation" class="wm-upd">已更新</span></label>
    <input v-model="location" class="f-sel" maxlength="250" placeholder="请输入地点" />

    <label class="f-lab">经纬度</label>
    <div class="wm-co">
      <input v-model="lng" class="f-sel" placeholder="经度" maxlength="16" @input="onCoordInput" />
      <input v-model="lat" class="f-sel" placeholder="纬度" maxlength="16" @input="onCoordInput" />
      <button type="button" class="btn btn-sm wm-tower-btn" @click="towerOpen = true">
        <template v-if="towerNo">
          <LegacyIcon name="check" :size="14" />
          <span class="tb-no">{{ towerNo }}</span
          ><span class="tb-re">重选</span>
        </template>
        <template v-else> <LegacyIcon name="location" :size="14" />选择杆塔坐标 </template>
      </button>
    </div>

    <label class="f-lab">防伪码</label>
    <div class="wm-code-row">
      <span class="wm-code num">{{ code }}</span>
      <button type="button" class="linkbtn" @click="code = genAntiCode()">换一换</button>
    </div>

    <div class="f-hint">确认后由服务端渲染水印并自动下载到本地</div>
    <div class="m-foot">
      <button type="button" class="btn btn-sm" @click="close">取消</button>
      <button type="button" class="btn btn-sm btn-primary" :disabled="saving" @click="submit">
        {{ saving ? '正在生成水印…' : '确认，生成水印照片' }}
      </button>
    </div>
  </Drawer>

  <TowerDialog v-model:open="towerOpen" @pick="onTowerPicked" />
</template>

<script setup>
// 水印字段编辑弹层（恒「无历史」口径：定位带出 + 杆塔坐标，无所属人名；移植 server/public/wmadd.html openWmForm）：
// 拍摄时间取当前；浏览器定位 WGS84→GCJ02 带出经纬度并调 /wmadd/geo 预填地点/天气；
// 手动改经纬度 800ms 防抖刷新地点/天气；「选择杆塔坐标」≤50m 波动带入；防伪码自动生成可换一换；
// 确认后 POST /api/v1/wmadd/render 服务端渲染仅回图（网页端图片已 canvas 重绘，orientation 恒 'up'）。
import { onBeforeUnmount, onUnmounted, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import LegacyIcon from '../core/LegacyIcon.vue';
import Drawer from '../../components/Drawer.vue';
import TowerDialog from './TowerDialog.vue';
import { getGeo, renderWatermark } from './api.js';
import { fmtWmTime, genAntiCode, jitterCoord, withDegSuffix, wgs2gcj } from './wm-utils.js';

const props = defineProps({
  open: { type: Boolean, default: false },
  // 裁剪/归一后的原图 JPEG dataURL（上送渲染）
  image: { type: String, default: '' },
});
const emit = defineEmits(['update:open', 'rendered']);

// 快捷输入（同小程序）：点击追加到内容末尾
const QUICK_INPUTS = ['110kV', '220kV', 'Ⅰ', 'Ⅱ', '线巡视'];

const content = ref('');
const time = ref('');
const weather = ref('');
const location = ref('');
const lng = ref('');
const lat = ref('');
const code = ref('');
const updWeather = ref(false); // 已选杆塔坐标（天气/地点行显「已更新」标）
const updLocation = ref(false);
const towerNo = ref('');
const towerOpen = ref(false);
const saving = ref(false);

// 「上次取值坐标」记录（手动改经纬度防抖刷新去重用，同小程序 _wmGeoKey）
let geoKey = null;
let towerPicked = false; // 已选杆塔坐标（定位结果不再覆盖）
let userEdited = false; // 用户手填过经纬度（定位/geo 慢响应不得覆盖手填坐标）
let geoTimer = 0;

// guardEsc 杆塔弹层在上层时只关最上层：共享 Drawer 的 Esc 监听无法感知嵌套弹层，
// 于此（capture，注册早于 Drawer）拦截并代关，避免连带关掉抽屉；焦点在已展开下拉里时先收下拉。
function guardEsc(e) {
  if (e.key !== 'Escape' || !towerOpen.value) return;
  e.preventDefault();
  e.stopImmediatePropagation();
  const combo = e.target && e.target.closest ? e.target.closest('.combo') : null;
  if (combo && combo.querySelector('.combo-list.open')) {
    if (e.target.blur) e.target.blur();
    return;
  }
  towerOpen.value = false;
}
document.addEventListener('keydown', guardEsc, true);
onBeforeUnmount(() => document.removeEventListener('keydown', guardEsc, true));

// 打开即复位为「无历史」口径：拍摄时间取当前、防伪码新生成、浏览器定位预填
watch(
  () => props.open,
  (open) => {
    if (!open) return;
    content.value = '';
    weather.value = '';
    location.value = '';
    lng.value = '';
    lat.value = '';
    time.value = fmtWmTime(new Date());
    code.value = genAntiCode();
    updWeather.value = false;
    updLocation.value = false;
    towerNo.value = '';
    towerOpen.value = false;
    saving.value = false;
    geoKey = null;
    towerPicked = false;
    userEdited = false;
    clearTimeout(geoTimer);
    locateOnce();
  }
);

function appendQuick(t) {
  content.value = (content.value + t).slice(0, 500);
}

// refreshGeo 经纬度变化统一调 /geo 覆盖刷新地点/天气；
// 响应仅在弹层仍开且经纬度未再改动时应用（同小程序 refreshWmGeo）
function refreshGeo(gLng, gLat, tip) {
  geoKey = gLng + ',' + gLat;
  getGeo(gLng, gLat)
    .then((r) => {
      if (!props.open) return;
      if (parseFloat(lng.value) !== gLng || parseFloat(lat.value) !== gLat) return;
      weather.value = (r && r.weather) || '';
      location.value = (r && r.location) || '';
      if (tip) MessagePlugin.success(tip);
    })
    .catch(() => {
      if (!props.open) return;
      if (parseFloat(lng.value) !== gLng || parseFloat(lat.value) !== gLat) return;
      weather.value = ''; // 失败清空留空手填（同小程序口径）
      location.value = '';
    });
}

// scheduleGeo 手动改经纬度：停顿 800ms 防抖后按新坐标刷新地点/天气（同小程序 scheduleWmGeoRefresh）
function scheduleGeo() {
  clearTimeout(geoTimer);
  geoTimer = setTimeout(() => {
    const gLng = parseFloat(lng.value);
    const gLat = parseFloat(lat.value);
    if (
      !Number.isFinite(gLng) ||
      !Number.isFinite(gLat) ||
      Math.abs(gLng) > 180 ||
      Math.abs(gLat) > 90
    )
      return;
    if (gLng + ',' + gLat === geoKey) return;
    refreshGeo(gLng, gLat, '已按经纬度更新地点、天气');
  }, 800);
}
function onCoordInput() {
  userEdited = true;
  scheduleGeo();
}

// onTowerPicked 杆塔坐标带入：≤50m 随机波动后填入（同小程序），仍可手改
function onTowerPicked(t) {
  const jd = jitterCoord(t.lng, t.lat, 50);
  towerPicked = true;
  lng.value = jd.lng;
  lat.value = jd.lat;
  towerNo.value = t.no;
  updWeather.value = true;
  updLocation.value = true;
  refreshGeo(parseFloat(jd.lng), parseFloat(jd.lat), '已按杆塔坐标更新地点、天气');
}

// locateOnce 浏览器定位（WGS84→GCJ02）取经纬度并调 /geo 预填地点/天气；授权被拒或失败均留空手填（同小程序口径）
function locateOnce() {
  if (!navigator.geolocation) return;
  navigator.geolocation.getCurrentPosition(
    (pos) => {
      if (!props.open || towerPicked || userEdited) return;
      const g = wgs2gcj(pos.coords.longitude, pos.coords.latitude);
      const gLng = g[0].toFixed(6);
      const gLat = g[1].toFixed(6);
      lng.value = gLng;
      lat.value = gLat;
      geoKey = parseFloat(gLng) + ',' + parseFloat(gLat);
      getGeo(gLng, gLat)
        .then((r) => {
          if (!props.open || towerPicked || userEdited) return;
          if (!weather.value) weather.value = (r && r.weather) || '';
          if (!location.value) location.value = (r && r.location) || '';
        })
        .catch(() => {
          /* 反查失败留空手填 */
        });
    },
    () => {
      /* 授权被拒留空手填 */
    },
    { timeout: 10000 }
  );
}

// submit 服务端渲染仅回图（不上传、不存档、不验证），成功进结果弹层并自动下载
async function submit() {
  if (saving.value) return;
  saving.value = true;
  const wm = {
    content: content.value,
    time: time.value.trim(),
    weather: weather.value.trim(),
    location: location.value.trim(),
    longitude: withDegSuffix(lng.value, '°E'),
    latitude: withDegSuffix(lat.value, '°N'),
    antiCode: code.value,
    orientation: 'up', // 网页端图片已经 canvas 重绘，EXIF 方向归一
  };
  try {
    const r = await renderWatermark({ image: props.image, wm });
    const out = r && r.image;
    if (!out) throw new Error('水印生成失败');
    close();
    emit('rendered', out);
  } catch (e) {
    MessagePlugin.error((e && e.message) || '水印生成失败');
    saving.value = false;
  }
}

function close() {
  clearTimeout(geoTimer);
  emit('update:open', false);
}

// 卸载兜底清掉防抖定时器，避免卸载后发起地理反查
onUnmounted(() => {
  clearTimeout(geoTimer);
});
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

/* ===== 弹层底部按钮行（Drawer 内容区，随内容滚动） ===== */
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 弹层表单（与 worklog 同套口径） ===== */
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
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
}
.f-sel {
  height: 36px;
  padding: 0 10px;
}
.f-sel:focus,
.f-ta:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}
.f-ta {
  min-height: 110px;
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
/* 快捷输入 chips：8px 圆角小按钮，点击追加到内容末尾 */
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
.chip-sm {
  padding: 3px 10px;
  font-size: 11.5px;
}

/* ===== 水印字段编辑：缩略图头部；经纬度行；防伪码行；「已更新」签 ===== */
.wm-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 2px;
}
.wm-thumb {
  width: 72px;
  height: 54px;
  flex: none;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
}
.wm-names {
  font-size: 13.5px;
  font-weight: 700;
}
.wm-names-tip {
  margin-top: 3px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.wm-co {
  display: flex;
  align-items: center;
  gap: 8px;
}
.wm-co .f-sel {
  flex: 1;
  min-width: 0;
}
.wm-tower-btn {
  flex: none;
}
.wm-tower-btn .tb-no {
  font-weight: 700;
}
.wm-tower-btn .tb-re {
  color: var(--tk-ph);
  font-weight: 400;
  margin-left: 2px;
}
.wm-upd {
  margin-left: 8px;
  font-size: 10px;
  font-weight: 700;
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
  border-radius: 4px;
  padding: 1px 6px;
  letter-spacing: 1px;
}
.wm-code-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.wm-code {
  font-family: Consolas, monospace;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 2px;
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
