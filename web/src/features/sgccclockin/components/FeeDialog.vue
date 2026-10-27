<template>
  <div v-if="open" class="wl-modal-mask" @click.self="close">
    <div class="wl-modal card sg-fee-modal">
      <h3 class="card-title">当日费用 · {{ name }} · {{ date }}</h3>
      <div class="sub-text">保存即提交商旅</div>

      <div v-if="loading" class="muted sg-fee-load">费用信息加载中…</div>
      <div v-else-if="loadError" class="wl-error">{{ loadError }}</div>
      <template v-else>
        <!-- 当日适用标准（feeStd 随卡片装配）：一键填入 -->
        <div v-if="std" class="sg-fee-std">
          <span
            >当日适用标准：伙食 {{ std.foodFee }}/交通 {{ std.transitFee }}（{{ stdText }}）</span
          >
          <button type="button" class="wl-act" @click="fillStd">按标准填入</button>
        </div>

        <!-- 成本分配（只读） -->
        <div v-if="alloc" class="sg-fee-rows">
          <div class="sg-fee-row">
            <span class="sg-fee-k">成本分配</span>
            <span class="sg-fee-v">{{ alloc.label || '成本中心' }}</span>
          </div>
          <div v-if="alloc.costCenterName" class="sg-fee-row">
            <span class="sg-fee-k">成本中心</span>
            <span class="sg-fee-v">{{ alloc.costCenterName }}</span>
          </div>
          <div v-if="alloc.costCenterCode" class="sg-fee-row">
            <span class="sg-fee-k">成本中心编码</span>
            <span class="sg-fee-v num">{{ alloc.costCenterCode }}</span>
          </div>
        </div>

        <div class="sg-fee-rows">
          <div class="sg-fee-row">
            <span class="sg-fee-k">伙食补助</span>
            <input v-model="food" inputmode="decimal" class="wl-range num" placeholder="0" />
          </div>
          <div class="sg-fee-row">
            <span class="sg-fee-k">交通费</span>
            <input v-model="transit" inputmode="decimal" class="wl-range num" placeholder="0" />
          </div>
        </div>
      </template>

      <div v-if="error" class="wl-error">{{ error }}</div>
      <div class="wl-modal-foot">
        <t-button variant="outline" @click="close">稍后再填</t-button>
        <t-button :disabled="loading || !!loadError" :loading="saving" @click="save"
          >保存费用信息</t-button
        >
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { fetchFee, postFee } from '../api.js';

// 费用信息弹层：本地摘要优先，无值时取商旅模板补助组件（id=10）解析；
// 仅伙食补助/交通费可编辑，成本分配只读展示；保存即提交商旅（POST /sgcc/fee）。
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
.sg-fee-modal {
  width: min(460px, calc(100vw - 32px));
}
.sg-fee-load {
  padding: 18px 0;
  text-align: center;
}
.sg-fee-std {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  background: var(--tk-blue-soft);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-brand);
  margin-bottom: 10px;
}
.sg-fee-rows {
  margin-bottom: 6px;
}
.sg-fee-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 0;
}
.sg-fee-k {
  width: 90px;
  flex-shrink: 0;
  font-size: 13px;
  color: var(--tk-sub);
}
.sg-fee-v {
  font-size: 13px;
  color: var(--tk-ink);
}
.sg-fee-row .wl-range {
  flex: 1;
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
  width: min(560px, calc(100vw - 32px));
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
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
</style>
