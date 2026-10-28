<template>
  <div v-if="open" class="mask open" @click.self="close">
    <div class="modal fee-modal">
      <div class="m-scroll">
        <div class="m-head">
          <span class="m-title">当日费用 · {{ name }} · {{ fmtDateCN(date) }}</span>
          <button type="button" class="m-x" @click="close">
            <WlIcon name="close" :size="14" />
          </button>
        </div>
        <div class="f-hint" style="margin: 0 0 6px">保存即提交商旅</div>

        <div v-if="loading" class="fee-load">费用信息加载中…</div>
        <div v-else-if="loadError" class="wl-error">{{ loadError }}</div>
        <template v-else>
          <!-- 当日适用标准行（浅蓝信息条底 + 右侧「按标准填入」链接钮） -->
          <div v-if="std" class="fee-std">
            <span
              >当日适用标准：伙食 {{ std.foodFee }}/交通 {{ std.transitFee }}（{{ stdText }}）</span
            >
            <button type="button" @click="fillStd">按标准填入</button>
          </div>

          <!-- 成本分配（只读） -->
          <div v-if="alloc" class="fee-rows">
            <div class="fee-row">
              <span class="fee-k">成本分配</span>
              <span class="fee-v">{{ alloc.label || '成本中心' }}</span>
            </div>
            <div v-if="alloc.costCenterName" class="fee-row">
              <span class="fee-k">成本中心</span>
              <span class="fee-v">{{ alloc.costCenterName }}</span>
            </div>
            <div v-if="alloc.costCenterCode" class="fee-row">
              <span class="fee-k">成本中心编码</span>
              <span class="fee-v num">{{ alloc.costCenterCode }}</span>
            </div>
          </div>

          <div class="fee-rows">
            <div class="fee-row">
              <span class="fee-k">伙食补助</span>
              <input
                v-model="food"
                inputmode="decimal"
                class="f-sel fee-input num"
                placeholder="0"
              />
            </div>
            <div class="fee-row">
              <span class="fee-k">交通费</span>
              <input
                v-model="transit"
                inputmode="decimal"
                class="f-sel fee-input num"
                placeholder="0"
              />
            </div>
          </div>
        </template>

        <div v-if="error" class="wl-error">{{ error }}</div>
        <div class="m-foot">
          <button type="button" class="btn btn-sm" @click="close">稍后再填</button>
          <button
            type="button"
            class="btn btn-sm btn-primary"
            :disabled="loading || !!loadError || saving"
            @click="save"
          >
            {{ saving ? '保存中…' : '保存费用信息' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import WlIcon from '../../worklog/components/WlIcon.vue';
import { fetchFee, postFee } from '../api.js';

// 费用信息弹层（旧版 .modal 弹层骨架 + .fee-std/.fee-row 行卡密度）：本地摘要优先，
// 无值时取商旅模板补助组件（id=10）解析；仅伙食补助/交通费可编辑，成本分配只读展示；
// 保存即提交商旅（POST /sgcc/fee）。
const props = defineProps({
  open: { type: Boolean, default: false },
  memberId: { type: Number, default: 0 },
  name: { type: String, default: '' },
  date: { type: String, default: '' },
  feeStd: { type: Object, default: null }, // {foodFee, transitFee, scope}
});
const emit = defineEmits(['update:open', 'saved']);

const loading = ref(false);
const loadError = ref('');
const error = ref('');
const saving = ref(false);
const food = ref('');
const transit = ref('');
const alloc = ref(null);

// std 当日适用标准（feeStd 缺失/字段不全时不渲染标准行）。
const std = computed(() =>
  props.feeStd && typeof props.feeStd.foodFee === 'number' ? props.feeStd : null
);
const stdText = computed(() => {
  if (!std.value) return '';
  return std.value.scope === 1 ? '市外' : std.value.scope === 2 ? '驻地' : '市内';
});

// fmtDateCN 日期中文（X 年 X 月 X 日，同旧版费用弹层标题）。
function fmtDateCN(d) {
  const [y, m, dd] = String(d || '').split('-');
  return `${y} 年 ${Number(m)} 月 ${Number(dd)} 日`;
}

// num 数字归一（非法值回空串）。
function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? String(n) : '';
}

// open 变化时拉取费用数据源（本地摘要 + 商旅模板）。
watch(
  () => props.open,
  async (val) => {
    if (!val) return;
    error.value = '';
    loadError.value = '';
    loading.value = true;
    alloc.value = null;
    food.value = '';
    transit.value = '';
    try {
      const data = await fetchFee({ member_id: props.memberId, date: props.date });
      const local = data.local || null;
      // 补助明细组件（id=10）：value 为 JSON 字符串 {foodFee, arrive}
      let tplFood = '';
      let tplArrive = '';
      const comps = (data.clockTemplate && data.clockTemplate.dtComponentList) || [];
      for (const c of comps) {
        if (c.id === 10 && c.value) {
          try {
            const v = JSON.parse(c.value);
            tplFood = v.foodFee;
            tplArrive = v.arrive;
          } catch (e) {
            // 模板值异常按无值处理
          }
          break;
        }
      }
      alloc.value = data.costAlloc || null;
      food.value = local ? num(local.food_fee) : num(tplFood) || '0';
      transit.value = local ? num(local.transit_fee) : num(tplArrive) || '0';
    } catch (e) {
      loadError.value = e.message || '费用信息加载失败，请关闭后重试';
    } finally {
      loading.value = false;
    }
  }
);

// fillStd 按当日适用标准一键填入。
function fillStd() {
  if (!std.value) return;
  food.value = String(std.value.foodFee);
  transit.value = String(std.value.transitFee);
}

// save 保存费用（远端先行，成功后静默刷新由父视图负责）。
async function save() {
  if (saving.value) return;
  saving.value = true;
  error.value = '';
  try {
    await postFee({
      member_id: props.memberId,
      date: props.date,
      foodFee: Number(food.value) || 0,
      transitFee: Number(transit.value) || 0,
    });
    MessagePlugin.success('保存成功');
    emit('saved');
    close();
  } catch (e) {
    error.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}

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
  overflow-x: hidden;
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

/* ===== 费用信息（旧版 .fee-std / .fee-row / .fee-k / .fee-v） ===== */
.fee-load {
  padding: 30px 0;
  text-align: center;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.fee-std {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--tk-blue-soft);
  border-radius: 8px;
  padding: 8px 12px;
  margin-bottom: 6px;
  font-size: 11.5px;
  color: var(--tk-sub);
}
.fee-std span {
  flex: 1;
  min-width: 0;
}
.fee-std button {
  flex: none;
  border: none;
  background: none;
  padding: 2px 4px;
  font-size: 11.5px;
  font-weight: 700;
  color: var(--tk-brand);
  cursor: pointer;
  font-family: inherit;
}
.fee-std button:hover {
  text-decoration: underline;
}
.fee-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px dashed var(--tk-hair, rgba(29, 33, 41, 0.06));
}
.fee-row:last-child {
  border-bottom: none;
}
.fee-k {
  font-size: 13px;
  font-weight: 700;
  color: var(--tk-ink);
}
.fee-v {
  font-size: 13px;
  color: var(--tk-sub);
  text-align: right;
  word-break: break-all;
}
.f-sel {
  height: 36px;
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
.fee-input {
  width: 150px;
  text-align: right;
}
.f-hint {
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
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
