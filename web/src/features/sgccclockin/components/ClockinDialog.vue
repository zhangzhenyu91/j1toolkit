<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card sg-ck-modal">
      <h3 class="card-title">{{ title }}</h3>
      <div class="sub-text">{{ note }}</div>

      <!-- 位置卡：地址输入 + 重新定位 / 选择杆塔 -->
      <div class="sg-ck-loc">
        <input
          v-model="position"
          maxlength="255"
          class="wl-range"
          placeholder="可手动输入地点，或重新定位 / 选择杆塔"
          @input="manualPos = true"
        />
        <div v-if="locSub" class="sg-ck-sub num">{{ locSub }}</div>
        <div class="sg-ck-acts">
          <button type="button" class="wl-act" :disabled="locating" @click="relocate">
            {{ locating ? '定位中…' : '重新定位' }}
          </button>
          <button type="button" class="wl-act" @click="towerOpen = !towerOpen">
            选择杆塔带入坐标
          </button>
        </div>
      </div>

      <!-- 杆塔选择（简单可搜列表） -->
      <div v-if="towerOpen" class="sg-ck-towers">
        <input v-model="towerQuery" type="text" class="wl-range" placeholder="搜索线路 / 杆塔号" />
        <div class="sg-ck-tower-list">
          <button
            v-for="(t, i) in filteredTowers"
            :key="i"
            type="button"
            class="sg-ck-tower-item"
            @click="pickTower(t)"
          >
            <span>{{ t[1] }} · {{ t[2] }}</span>
            <span class="muted num">{{ t[3] }}, {{ t[4] }}</span>
          </button>
          <div v-if="!filteredTowers.length" class="muted">无匹配杆塔</div>
        </div>
      </div>

      <div v-if="error" class="wl-error">{{ error }}</div>
      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">取消</t-button>
        <t-button :loading="saving" @click="confirm">确认打卡</t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { fetchGeo, fetchTowers } from '../../worklog/api.js';
import { postClockin } from '../api.js';

// 打卡确认弹层：位置优先级 = 同记录他人同 seq 打卡整套带入 → 浏览器定位（WGS84→GCJ02）+ 逆编码；
// 支持手输地址与杆塔坐标带入。提交 POST /sgcc/clockin（seq 1 首次 / 2 结束 / 0 更新地点）。
const props = defineProps({
  open: { type: Boolean, default: false },
  entry: { type: Object, default: null },
  memberId: { type: Number, default: 0 },
  name: { type: String, default: '' },
  seq: { type: Number, default: 1 }, // 1 开始 / 2 结束
  action: { type: String, default: 'mark' }, // mark 打卡 / update 更新地点
  date: { type: String, default: '' },
});
const emit = defineEmits(['update:open', 'saved']);

const position = ref('');
const error = ref('');
const saving = ref(false);
const locating = ref(false);
const manualPos = ref(false);
const locTag = ref('');
const towerOpen = ref(false);
const towerQuery = ref('');
const towers = ref([]);
const cur = reactive({ lng: '', lat: '', cityCode: '', cityName: '' });

const title = computed(() =>
  props.action === 'update'
    ? `更新打卡地点 · ${props.name}`
    : `${props.seq === 2 ? '结束' : '开始'}打卡 · ${props.name}`
);
const note = computed(() => {
  let base = `提交时使用 ${props.name} 绑定的机型；打卡即提交商旅并双写本地`;
  if (first.value)
    base = `本记录 ${first.value.name} 已于 ${fmtHm(first.value.time)} 打卡，地点已自动带入；${base}`;
  if (props.action === 'update') base = `仅修改打卡地点；${base}`;
  return base;
});
const locSub = computed(() => {
  if (cur.lng && cur.lat)
    return `${cur.lng}, ${cur.lat}${locTag.value ? ' · ' + locTag.value : ''}`;
  if (!locating.value) return '定位失败，可点「重新定位」或「选择杆塔带入坐标」';
  return '';
});

// first 同记录他人同 seq 已有位置的打卡（带入其整套定位）。
const first = computed(() => {
  const entry = props.entry;
  if (!entry || props.action === 'update') return null;
  const sorted = [...(entry.members || [])].sort((a, b) => (a.sort || 0) - (b.sort || 0));
  for (const m of sorted) {
    if (m.member_id === props.memberId) break; // 仅带入他人的打卡地点
    const c = (entry.clockinMap || {})[String(m.member_id)];
    if (c && c[props.seq] && c[props.seq].position) return { name: m.name, ...c[props.seq] };
  }
  return null;
});

// fmtHm 打卡时间取 HH:mm。
function fmtHm(v) {
  if (!v) return '';
  const m = /(\d{2}):(\d{2})/.exec(String(v));
  return m ? `${m[1]}:${m[2]}` : String(v);
}

// filteredTowers 杆塔搜索过滤（线路名 / 杆塔号）。
const filteredTowers = computed(() => {
  const q = towerQuery.value.trim();
  const list = towers.value || [];
  if (!q) return list.slice(0, 100);
  return list.filter((t) => `${t[1]}`.includes(q) || `${t[2]}`.includes(q)).slice(0, 100);
});

// open 变化：重置并按带入优先级初始化定位。
watch(
  () => props.open,
  async (val) => {
    if (!val) return;
    error.value = '';
    saving.value = false;
    manualPos.value = false;
    towerOpen.value = false;
    towerQuery.value = '';
    cur.lng = '';
    cur.lat = '';
    cur.cityCode = '';
    cur.cityName = '';
    const f = first.value;
    if (f) {
      position.value = f.position || '';
      cur.lng = f.lng || '';
      cur.lat = f.lat || '';
      cur.cityCode = f.cityCode || '';
      cur.cityName = f.cityName || '';
      locTag.value = '同记录已打卡人定位';
      locating.value = false;
    } else {
      position.value = '';
      locTag.value = '';
      locate(true);
    }
    if (!towers.value.length) {
      try {
        const data = await fetchTowers();
        towers.value = data.rows || data.list || [];
      } catch (e) {
        towers.value = [];
      }
    }
  }
);

// locate 浏览器定位（WGS84→GCJ02）+ 逆编码（needGeo=true 时填充地址串）。
function locate(needGeo) {
  if (!navigator.geolocation) {
    locating.value = false;
    return;
  }
  locating.value = true;
  navigator.geolocation.getCurrentPosition(
    (pos) => {
      const g = wgs2gcj(pos.coords.longitude, pos.coords.latitude);
      cur.lng = g[0].toFixed(6);
      cur.lat = g[1].toFixed(6);
      locating.value = false;
      geo(cur.lng, cur.lat, '当前定位', !needGeo);
    },
    () => {
      locating.value = false;
    },
    { timeout: 10000 }
  );
}

// geo 逆编码（/worklog/geo：location+weather；城市编码服务端未提供时留空由上游兜底）。
async function geo(lng, lat, tag, keepPosition) {
  try {
    const d = await fetchGeo(lng, lat);
    if (cur.lng !== lng || cur.lat !== lat) return; // 坐标已变，旧响应丢弃
    locTag.value = tag;
    if (!keepPosition && !manualPos.value && d.location) position.value = d.location;
  } catch (e) {
    // 逆编码失败：提交时由服务端兜底
  }
}

// relocate 重新定位（覆盖带入/手输地址）。
function relocate() {
  manualPos.value = false;
  locTag.value = '当前定位';
  position.value = '';
  cur.lng = '';
  cur.lat = '';
  locate(true);
}

// pickTower 杆塔坐标带入（不波动）+ 逆编码地址。
function pickTower(t) {
  manualPos.value = false;
  towerOpen.value = false;
  locTag.value = '杆塔坐标';
  cur.lng = Number(t[3]).toFixed(6);
  cur.lat = Number(t[4]).toFixed(6);
  locating.value = false;
  geo(cur.lng, cur.lat, '杆塔坐标', false);
}

// detailId 更新地点时上送的原打卡明细 id。
const detailId = computed(() => {
  const map =
    (props.entry && props.entry.clockinMap && props.entry.clockinMap[String(props.memberId)]) || {};
  return (map[props.seq] && map[props.seq].detailId) || '';
});

// confirm 确认打卡（POST /sgcc/clockin）。
async function confirm() {
  if (saving.value) return;
  if (!cur.lng || !cur.lat) {
    error.value = '请先完成定位（重新定位或选择杆塔带入坐标）';
    return;
  }
  saving.value = true;
  error.value = '';
  try {
    await postClockin({
      member_id: props.memberId,
      date: props.date,
      seq: props.action === 'update' ? 0 : props.seq,
      position: position.value,
      longitude: cur.lng,
      latitude: cur.lat,
      city_code: cur.cityCode,
      city_name: cur.cityName,
      detail_id: props.action === 'update' ? detailId.value : '',
    });
    MessagePlugin.success('打卡成功');
    emit('saved', { seq: props.seq, action: props.action });
    close();
  } catch (e) {
    error.value = e.message || '打卡失败';
    if (e.code === 40037) {
      // 已打过：关层刷新，数据以服务端为准
      emit('saved', { seq: props.seq, action: props.action });
      close();
    }
  } finally {
    saving.value = false;
  }
}

// wgs2gcj WGS84 → GCJ02（浏览器定位纠偏，同水印弹层口径）。
function wgs2gcj(lng, lat) {
  const a = 6378245.0;
  const ee = 0.00669342162296594323;
  const pi = Math.PI;
  const outOfChina = lng < 72.004 || lng > 137.8347 || lat < 0.8293 || lat > 55.8271;
  if (outOfChina) return [lng, lat];
  let dLat = transformLat(lng - 105.0, lat - 35.0);
  let dLng = transformLng(lng - 105.0, lat - 35.0);
  const radLat = (lat / 180.0) * pi;
  let magic = Math.sin(radLat);
  magic = 1 - ee * magic * magic;
  const sqrtMagic = Math.sqrt(magic);
  dLat = (dLat * 180.0) / (((a * (1 - ee)) / (magic * sqrtMagic)) * pi);
  dLng = (dLng * 180.0) / ((a / sqrtMagic) * Math.cos(radLat) * pi);
  return [lng + dLng, lat + dLat];
}
function transformLat(x, y) {
  let ret = -100.0 + 2.0 * x + 3.0 * y + 0.2 * y * y + 0.1 * x * y + 0.2 * Math.sqrt(Math.abs(x));
  ret += ((20.0 * Math.sin(6.0 * x * Math.PI) + 20.0 * Math.sin(2.0 * x * Math.PI)) * 2.0) / 3.0;
  ret += ((20.0 * Math.sin(y * Math.PI) + 40.0 * Math.sin((y / 3.0) * Math.PI)) * 2.0) / 3.0;
  ret +=
    ((160.0 * Math.sin((y / 12.0) * Math.PI) + 320.0 * Math.sin((y * Math.PI) / 30.0)) * 2.0) / 3.0;
  return ret;
}
function transformLng(x, y) {
  let ret = 300.0 + x + 2.0 * y + 0.1 * x * x + 0.1 * x * y + 0.1 * Math.sqrt(Math.abs(x));
  ret += ((20.0 * Math.sin(6.0 * x * Math.PI) + 20.0 * Math.sin(2.0 * x * Math.PI)) * 2.0) / 3.0;
  ret += ((20.0 * Math.sin(x * Math.PI) + 40.0 * Math.sin((x / 3.0) * Math.PI)) * 2.0) / 3.0;
  ret +=
    ((150.0 * Math.sin((x / 12.0) * Math.PI) + 300.0 * Math.sin((x / 30.0) * Math.PI)) * 2.0) / 3.0;
  return ret;
}

function close() {
  emit('update:open', false);
}
</script>

<style scoped>
.sg-ck-modal {
  width: min(520px, calc(100vw - 32px));
}
.sg-ck-loc {
  background: var(--tk-fill);
  padding: 10px;
  margin: 12px 0;
}
.sg-ck-sub {
  margin-top: 6px;
  font-size: 12px;
  color: var(--tk-sub);
}
.sg-ck-acts {
  margin-top: 8px;
  display: flex;
  gap: 14px;
}
.sg-ck-towers {
  margin: 8px 0;
}
.sg-ck-tower-list {
  max-height: 220px;
  overflow: auto;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  margin-top: 6px;
}
.sg-ck-tower-item {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  width: 100%;
  padding: 7px 10px;
  border: none;
  border-bottom: 1px solid var(--tk-line);
  background: #fff;
  font-size: 12px;
  color: var(--tk-ink);
  cursor: pointer;
}
.sg-ck-tower-item:hover {
  background: var(--tk-blue-soft);
}
.wl-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
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
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.wl-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.wl-act {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  cursor: pointer;
}
.wl-range {
  width: 100%;
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
</style>
