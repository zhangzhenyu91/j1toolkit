<template>
  <!-- 手动新增 / 展开卡编辑统一表单：13 业务列（线路名称必填；巡视时间日期列）。
       区段按原文提交（带字母杆号不剔除），pole_from/pole_to 数值锚点由后端按区段解析（可空）。
       save 提交业务列白名单，cancel 放弃编辑。 -->
  <div class="mr-form">
    <div class="mr-grid">
      <label v-for="f in FORM_FIELDS" :key="f.key" class="mr-item" :class="{ full: f.full }">
        <span class="mr-lab">{{ f.label }}<i v-if="f.required" class="mr-req">*</i></span>
        <input
          v-if="f.type === 'date'"
          type="date"
          class="mr-in"
          :value="form[f.key]"
          @input="set(f.key, $event.target.value)"
        />
        <input
          v-else
          class="mr-in"
          :maxlength="f.max"
          :placeholder="f.ph || ''"
          :value="form[f.key]"
          @input="set(f.key, $event.target.value)"
        />
      </label>
    </div>
    <p v-if="err" class="mr-err">{{ err }}</p>
    <div class="mr-foot">
      <button class="btn btn-sm" type="button" :disabled="saving" @click="emit('cancel')">
        取消
      </button>
      <button class="btn btn-sm btn-primary" type="button" :disabled="saving" @click="submit">
        {{ saving ? '保存中…' : '保存' }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref, watch } from 'vue';

// FORM_FIELDS 表单列（key 与后端业务列一致；label/占位按台账口径）。
const FORM_FIELDS = [
  { key: 'voltage_level', label: '电压等级', max: 16, ph: '如 110' },
  { key: 'line_name', label: '线路名称', max: 64, required: true, ph: '如 甲线' },
  { key: 'section', label: '跨越区段', max: 32, ph: '如 15#-16#（杆号可带字母）' },
  { key: 'crossing_type', label: '跨越类型', max: 128, ph: '跨越物 / 交叉方式，如 10kV线路' },
  { key: 'dist_small', label: '距小号侧(m)', max: 16 },
  { key: 'dist_big', label: '距大号侧(m)', max: 16 },
  { key: 'cross_horizontal', label: '边线水平(m)', max: 16 },
  { key: 'cross_vertical', label: '垂直距离(m)', max: 16 },
  { key: 'cross_angle', label: '交叉角(°)', max: 16 },
  { key: 'has_hazard', label: '是否存在隐患', max: 16 },
  { key: 'finder_name', label: '发现人', max: 32 },
  { key: 'inspect_at', label: '巡视时间', type: 'date' },
  { key: 'remark', label: '备注', max: 255, full: true },
];

const props = defineProps({
  record: { type: Object, default: null }, // null=手动新增；Object=展开卡编辑初值
  saving: { type: Boolean, default: false },
});
const emit = defineEmits(['save', 'cancel']);

const form = reactive({});
const err = ref('');

// init 从记录初值装配表单（record 变化即重置；新增给全空）。
function init() {
  FORM_FIELDS.forEach((f) => {
    const raw = props.record ? props.record[f.key] : '';
    form[f.key] = raw == null ? '' : String(raw);
  });
  err.value = '';
}
init();
watch(
  () => props.record,
  () => init()
);

function set(key, val) {
  form[key] = val;
}

// submit 校验后提交业务列白名单（全部字段随表单提交，后端按列写入）。
function submit() {
  if (props.saving) return;
  const body = {};
  FORM_FIELDS.forEach((f) => {
    body[f.key] = String(form[f.key] == null ? '' : form[f.key]).trim();
  });
  if (!body.line_name) {
    err.value = '请填写线路名称';
    return;
  }
  err.value = '';
  emit('save', body);
}
</script>

<style scoped>
/* ===== 旧式按钮（theme.css .btn 口径，与本特性各组件同款自含） ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  font-family: inherit;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.btn:active:not([disabled]) {
  transform: translateY(1px);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary {
  color: var(--on-brand);
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

/* ===== 统一表单：两列网格 + 必填星标 + 底部操作行（8px 圆角输入，同暂存区编辑口径） ===== */
.mr-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px 14px;
}
.mr-item {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}
.mr-item.full {
  grid-column: 1 / -1;
}
.mr-lab {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
.mr-req {
  margin-left: 3px;
  font-style: normal;
  color: var(--tk-red);
}
.mr-in {
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 10px;
  font-size: 12.5px;
  color: var(--tk-ink);
  font-family: inherit;
  outline: none;
  background: var(--card);
  width: 100%;
  transition: border-color 0.15s;
}
.mr-in:focus {
  border-color: var(--tk-brand);
}
.mr-in[type='date'] {
  font-variant-numeric: tabular-nums;
}
.mr-err {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--tk-red);
  letter-spacing: 0.5px;
}
.mr-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;
}
@media (prefers-reduced-motion: reduce) {
  .btn,
  .mr-in {
    transition: none !important;
    transform: none !important;
  }
}
</style>
