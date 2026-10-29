<template>
  <!-- 选择杆塔坐标：居中确认弹层（overlay-motion 进出场）；Teleport 挂 body，保证叠在 Drawer 之上 -->
  <Teleport to="body">
    <div v-if="rendered" class="mask ov-mask" :class="phaseClass" @click.self="close">
      <div class="modal ov-panel">
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">选择杆塔坐标</span>
            <button type="button" class="m-x" @click="close">
              <LegacyIcon name="close" :size="14" />
            </button>
          </div>
          <div class="f-hint tw-tip">
            三级均可输入关键字快捷筛选；所选杆塔坐标按 ≤50m 随机波动后填入水印字段，填入后仍可手改
          </div>
          <label class="f-lab">电压等级</label>
          <ComboField
            :items="levelItems"
            :placeholder="levelPh"
            :disabled="levelDisabled"
            @pick="onLevelPick"
            @edit="onLevelEdit"
          />
          <label class="f-lab">线路名称</label>
          <ComboField
            :items="lineItems"
            :placeholder="linePh"
            :disabled="lineDisabled"
            @pick="onLinePick"
            @edit="onLineEdit"
          />
          <label class="f-lab">杆塔号</label>
          <ComboField
            :items="towerItems"
            :placeholder="towerPh"
            :disabled="towerDisabled"
            @pick="onTowerPick"
            @edit="onTowerEdit"
          />
          <div v-if="picked" class="f-hint tw-coord">
            {{ picked.no }} · 经度 {{ picked.lng.toFixed(6) }}　纬度 {{ picked.lat.toFixed(6) }}
          </div>
          <div class="m-foot">
            <button type="button" class="btn btn-sm" @click="close">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="!picked"
              @click="confirm"
            >
              确 定
            </button>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
// 选择杆塔坐标（电压等级 → 线路名称 → 杆塔号 级联，均可输入关键字筛选；移植 server/public/wmadd.html openTowerModal）：
// 数据 GET /api/v1/wmadd/towers 页内缓存一次（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]）；
// 上级选中填充下级，任一上级改选清空下级与已选杆塔（同小程序级联口径）。
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import LegacyIcon from '../core/LegacyIcon.vue';
import ComboField from './ComboField.vue';
import { useOverlayMotion } from '../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';
import { listTowers } from './api.js';

const props = defineProps({
  open: { type: Boolean, default: false },
});
const emit = defineEmits(['update:open', 'pick']);

const levelItems = ref([]);
const lineItems = ref([]);
const towerItems = ref([]);
const loading = ref(false);
const picked = ref(null); // {no, lng, lat}

const levelPh = computed(() => (loading.value ? '杆塔坐标加载中…' : '请选择电压等级'));
const levelDisabled = computed(() => loading.value);
const linePh = computed(() =>
  levelItems.value.length ? '输入关键字筛选或直接选择' : '请先选择电压等级'
);
const lineDisabled = computed(() => !levelItems.value.length || !levelValue.value);
const towerPh = computed(() =>
  lineItems.value.length ? '输入关键字筛选或直接选择' : '请先选择线路名称'
);
const towerDisabled = computed(() => !lineItems.value.length || !lineValue.value);

// 上级已选值（杆塔行过滤用；级联重置后清空）
const levelValue = ref('');
const lineValue = ref('');

let rows = [];

// 打开即加载（api.js 页内缓存一次；空数据/失败按旧页口径提示并关弹层）
watch(
  () => props.open,
  (open) => {
    if (!open) return;
    levelItems.value = [];
    lineItems.value = [];
    towerItems.value = [];
    levelValue.value = '';
    lineValue.value = '';
    picked.value = null;
    loading.value = true;
    listTowers()
      .then((list) => {
        rows = list;
        const levels = [];
        rows.forEach((r) => {
          if (levels.indexOf(r[0]) < 0) levels.push(r[0]);
        });
        levelItems.value = levels.map((n) => ({ label: n }));
        loading.value = false;
      })
      .catch((e) => {
        loading.value = false;
        emit('update:open', false);
        MessagePlugin.error(
          e && e.message === '杆塔坐标数据为空' ? e.message : '杆塔坐标加载失败，请稍后重试'
        );
      });
  }
);

function onLevelPick(it) {
  levelValue.value = it.label;
  const lines = [];
  rows.forEach((r) => {
    if (r[0] === it.label && lines.indexOf(r[1]) < 0) lines.push(r[1]);
  });
  lineItems.value = lines.map((n) => ({ label: n }));
  towerItems.value = [];
  lineValue.value = '';
  picked.value = null;
}
function onLevelEdit() {
  levelValue.value = '';
  lineItems.value = [];
  towerItems.value = [];
  lineValue.value = '';
  picked.value = null;
}
function onLinePick(it) {
  lineValue.value = it.label;
  const list = rows.filter((r) => r[0] === levelValue.value && r[1] === it.label);
  towerItems.value = list.map((r) => ({ label: r[2], no: r[2], lng: r[3], lat: r[4] }));
  picked.value = null;
}
function onLineEdit() {
  lineValue.value = '';
  towerItems.value = [];
  picked.value = null;
}
function onTowerPick(it) {
  picked.value = { no: it.no, lng: it.lng, lat: it.lat };
}
function onTowerEdit() {
  picked.value = null;
}

// open 显隐镜像：请求关闭本地先播退出动画（父级 v-model:open 跟随），关闭不瞬间消失。
const open = ref(props.open);
watch(
  () => props.open,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);

// onEsc Esc 关闭（document 捕获监听）；焦点在已展开的下拉里时先收下拉，不关弹层。
function onEsc(e) {
  if (e.key !== 'Escape') return;
  const combo = e.target && e.target.closest ? e.target.closest('.combo') : null;
  if (combo && combo.querySelector('.combo-list.open')) return;
  e.stopPropagation();
  close();
}
watch(rendered, (r) => {
  if (r) document.addEventListener('keydown', onEsc, true);
  else document.removeEventListener('keydown', onEsc, true);
});
onBeforeUnmount(() => document.removeEventListener('keydown', onEsc, true));

function close() {
  if (!open.value) return;
  open.value = false;
  emit('update:open', false);
}
function confirm() {
  if (!picked.value) return;
  close();
  emit('pick', picked.value);
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
  width: 460px;
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
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 14px 0 7px;
}
.f-hint {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.tw-tip {
  margin: 0 0 2px;
}
.tw-coord {
  font-variant-numeric: tabular-nums;
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
