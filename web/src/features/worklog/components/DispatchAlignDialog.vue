<template>
  <!-- 派车对齐 · 逐条处理：左「派车单表格」只读；中间「对照卡片」常驻（异常卡/快速建卡，差异字段红字/红框）；
       右侧列当天其他卡片，点击展开就地修改（不替换中间对照卡）。
       matched 保存 PUT /logs/{id}（巡视内容随条目带回避免误清）；sheetOnly 中间为按表格预填的快速建卡；
       entryOnly 对照卡只读提示，不自动删卡（口径见《开发指南》7.6）。 -->
  <div v-if="rendered" class="al-mask ov-mask" :class="phaseClass" @click.self="requestClose">
    <div
      ref="panelRef"
      class="al-modal ov-panel"
      role="dialog"
      aria-modal="true"
      aria-label="派车对齐逐条处理"
      tabindex="-1"
    >
      <div class="al-scroll">
        <div class="m-head">
          <span class="m-title">派车对齐 · 逐条处理</span>
          <span class="al-prog">第 {{ index + 1 }} / {{ total }} 条 · {{ item?.date || '' }}</span>
          <button type="button" class="m-x" aria-label="关闭" @click="requestClose">
            <WlIcon name="close" :size="14" />
          </button>
        </div>

        <div class="al-cards">
          <!-- 左：派车单表格（只读） -->
          <section class="al-card">
            <header class="al-h">
              <span class="al-h-t">派车单表格</span>
              <span class="sub">只读</span>
              <span class="src">{{ sheet?.file || '' }}</span>
            </header>
            <div v-if="sheet" class="al-rows">
              <div class="al-r" :class="{ diff: diffs.orderNo }">
                <span class="k">派车单号</span>
                <span class="v num">{{ sheet.orderNo || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">用车日期</span>
                <span class="v num">{{ sheet.date }}</span>
              </div>
              <div class="al-r" :class="{ diff: diffs.plate }">
                <span class="k">车牌号码</span>
                <span class="v">{{ sheet.plate || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">驾驶员</span>
                <span class="v">{{ sheet.driver || '—' }}</span>
              </div>
              <div class="al-r" :class="{ diff: memberDiff }">
                <span class="k">用车人</span>
                <span class="v">{{ (sheet.members || []).join(' ') || '—' }}</span>
              </div>
              <div class="al-r" :class="{ diff: diffs.dest }">
                <span class="k">目的地</span>
                <span class="v">{{ sheet.to || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">用车事由</span>
                <span class="v">{{ sheet.reason || '—' }}</span>
              </div>
            </div>
            <div v-else class="al-empty">派车单中没有与这张卡片对应的记录</div>
          </section>

          <!-- 中：对照卡常驻（matched 可改 / sheetOnly 快速建卡 / entryOnly 只读），不被右侧操作替换 -->
          <section class="al-card">
            <header class="al-h">
              <span class="al-h-t">{{ isSheetOnly ? '快速建卡' : '对照卡片' }}</span>
              <span class="sub">
                {{
                  isSheetOnly
                    ? '按表格预填，确认后建卡'
                    : isEntryOnly
                      ? '只读提示'
                      : '可修改，保存即写回当日卡片'
                }}
              </span>
              <span v-if="entry" class="src">卡片 #{{ entry.id }}</span>
              <span class="al-h-badge" :class="isSheetOnly ? 'add' : 'bad'">
                {{ isSheetOnly ? '表有卡无' : isEntryOnly ? '卡有表无' : '有差异' }}
              </span>
            </header>

            <div v-if="isEntryOnly" class="al-rows">
              <div class="al-r">
                <span class="k">车牌号码</span>
                <span class="v">{{ entry?.plate || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">用车人</span>
                <span class="v">{{ memberNames(entry) }}</span>
              </div>
              <div class="al-r">
                <span class="k">目的地</span>
                <span class="v">{{ entry?.destination || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">派车单号</span>
                <span class="v num">{{ entry?.order_no || '—' }}</span>
              </div>
              <div class="al-r">
                <span class="k">巡视内容</span>
                <span class="v">{{ entry?.patrol_content || '—' }}</span>
              </div>
              <p class="al-tip">
                派车单中无此记录，可能导出不完整或本次出车未走派车申请；确认无误即可，本功能不自动删卡。
              </p>
            </div>

            <div v-else class="al-form">
              <div class="al-field">
                <label class="al-lab">
                  车牌号
                  <em v-if="isMatched && middleDiffs.plate" class="al-tag">表格 ≠ 卡片</em>
                </label>
                <select v-model.number="form.vehicle_id" class="a-sel" :class="{ warn: plateRed }">
                  <option :value="0">（请选择车牌）</option>
                  <option v-for="v in vehicleOptions" :key="v.id" :value="v.id">
                    {{ v.label }}
                  </option>
                </select>
                <div v-if="plateNote" class="a-note" :class="{ red: plateNoteRed }">
                  {{ plateNote }}
                  <button
                    v-if="showAddVeh"
                    type="button"
                    class="a-link"
                    :disabled="addingVeh"
                    @click="addVehicle"
                  >
                    {{ addingVeh ? '添加中…' : '添加进字典' }}
                  </button>
                </div>
              </div>

              <div class="al-field">
                <label class="al-lab">
                  用车人
                  <em v-if="isMatched && memberDiff" class="al-tag">用车人差异</em>
                </label>
                <div class="al-chips">
                  <button
                    v-for="c in chips"
                    :key="c.id"
                    type="button"
                    class="al-chip"
                    :class="{ on: c.on, extra: c.extra, add: c.add }"
                    @click="toggleMember(c.id)"
                  >
                    {{ c.name }}
                  </button>
                  <span v-if="!chips.length" class="a-note"
                    >本班暂无成员，请到「班组数据」维护</span
                  >
                </div>
                <div v-if="isMatched && memberDiff" class="a-note">
                  红框 = 卡片多出（点击取消）；蓝虚线 = 表格有而卡片缺（点击补上）
                </div>
                <div v-if="unknownNames.length" class="a-note red">
                  表格用车人「{{
                    unknownNames.join('、')
                  }}」不在本班成员字典，无法点亮（可在「班组数据 → 人员」中添加成员）
                </div>
              </div>

              <div class="al-field">
                <label class="al-lab">
                  目的地
                  <em v-if="isMatched && middleDiffs.dest" class="al-tag">表格 ≠ 卡片</em>
                </label>
                <select
                  v-model.number="form.destination_id"
                  class="a-sel"
                  :class="{ warn: isMatched && middleDiffs.dest }"
                >
                  <option :value="0">（暂不选）</option>
                  <option v-for="d in destinationOptions" :key="d.id" :value="d.id">
                    {{ d.label }}
                  </option>
                </select>
                <div v-if="destNote" class="a-note" :class="{ red: destNoteRed }">
                  {{ destNote }}
                </div>
              </div>

              <div class="al-field">
                <label class="al-lab">
                  派车单号
                  <em v-if="isMatched && middleDiffs.orderNo" class="al-tag">表格 ≠ 卡片</em>
                </label>
                <input
                  v-model="form.dispatch_order_no"
                  type="text"
                  class="a-sel"
                  :class="{ warn: isMatched && middleDiffs.orderNo }"
                  maxlength="64"
                  placeholder="请输入派车单号（可空）"
                  autocomplete="off"
                />
                <div v-if="orderNote" class="a-note">{{ orderNote }}</div>
              </div>

              <div class="al-field">
                <label class="al-lab">巡视内容</label>
                <div class="a-ro">
                  {{
                    isSheetOnly
                      ? '建卡后为空，可到「记录」页签补填'
                      : entry?.patrol_content || '（空）'
                  }}
                  <span v-if="!isSheetOnly" class="a-sub">不在对齐范围，只读展示</span>
                </div>
              </div>
            </div>
          </section>

          <!-- 右：当天其他卡片（点击展开查看/修改，不替换中间对照卡） -->
          <section class="al-card">
            <header class="al-h">
              <span class="al-h-t">当天其他卡片</span>
              <span class="sub">{{ item?.date }} · {{ otherCards.length }} 张</span>
              <span class="src">点击展开查看/修改</span>
            </header>

            <div v-if="otherCards.length" class="al-others">
              <article
                v-for="c in otherCards"
                :key="c.id"
                class="al-other"
                :class="{ open: openOtherId === c.id }"
              >
                <button type="button" class="al-other-head" @click="toggleOther(c)">
                  <span class="al-other-plate">{{ c.plate || '未出车' }}</span>
                  <span class="al-other-meta">
                    {{ memberNames(c) }}
                    <template v-if="c.destination"> → {{ c.destination }}</template>
                    <template v-if="c.order_no"> · {{ c.order_no }}</template>
                  </span>
                  <span class="al-other-id num">#{{ c.id }}</span>
                  <WlIcon
                    :name="openOtherId === c.id ? 'chevron-down' : 'chevron-right'"
                    :size="12"
                  />
                </button>

                <div v-if="openOtherId === c.id" class="al-other-body">
                  <div class="al-field">
                    <label class="al-lab">车牌号</label>
                    <select v-model.number="otherForms[c.id].vehicle_id" class="a-sel">
                      <option :value="0">（请选择车牌）</option>
                      <option v-for="v in vehicleOptions" :key="v.id" :value="v.id">
                        {{ v.label }}
                      </option>
                    </select>
                  </div>
                  <div class="al-field">
                    <label class="al-lab">用车人</label>
                    <div class="al-chips">
                      <button
                        v-for="m in chipsFor(otherForms[c.id])"
                        :key="m.id"
                        type="button"
                        class="al-chip"
                        :class="{ on: m.on }"
                        @click="toggleMemberIn(otherForms[c.id], m.id)"
                      >
                        {{ m.name }}
                      </button>
                    </div>
                  </div>
                  <div class="al-field">
                    <label class="al-lab">目的地</label>
                    <select v-model.number="otherForms[c.id].destination_id" class="a-sel">
                      <option :value="0">（暂不选）</option>
                      <option v-for="d in destinationOptions" :key="d.id" :value="d.id">
                        {{ d.label }}
                      </option>
                    </select>
                  </div>
                  <div class="al-field">
                    <label class="al-lab">派车单号</label>
                    <input
                      v-model="otherForms[c.id].dispatch_order_no"
                      type="text"
                      class="a-sel"
                      maxlength="64"
                      placeholder="请输入派车单号（可空）"
                      autocomplete="off"
                    />
                  </div>
                  <div v-if="otherError" class="a-note red">{{ otherError }}</div>
                  <div class="al-other-actions">
                    <span class="a-sub">不影响左侧对齐处理</span>
                    <button
                      type="button"
                      class="btn btn-sm btn-primary"
                      :disabled="savingOther"
                      @click="saveOther(c)"
                    >
                      {{ savingOther ? '保存中…' : '保存此卡' }}
                    </button>
                  </div>
                </div>
              </article>
            </div>
            <div v-else class="al-empty">当天没有其他卡片</div>
          </section>
        </div>
      </div>

      <!-- 固定操作脚：内容可滚动，保存/建卡/跳过始终可见 -->
      <div class="al-footwrap">
        <div v-if="info" class="al-info">{{ info }}</div>
        <div v-if="error" class="al-error">{{ error }}</div>
        <div class="al-foot">
          <button type="button" class="btn btn-sm" :disabled="index === 0" @click="emit('prev')">
            ‹ 上一条
          </button>
          <button type="button" class="a-skip" @click="emit('skip')">跳过本条</button>
          <span class="sp"></span>
          <template v-if="isEntryOnly">
            <button type="button" class="btn btn-sm btn-primary" @click="emit('skip')">
              {{ isLast ? '知道了' : '知道了，下一条 ›' }}
            </button>
          </template>
          <template v-else>
            <button type="button" class="btn btn-sm" :disabled="saving" @click="submit(false)">
              仅{{ isSheetOnly ? '建卡' : '保存' }}
            </button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="saving"
              @click="submit(true)"
            >
              {{
                saving
                  ? '处理中…'
                  : isSheetOnly
                    ? isLast
                      ? '建卡并收尾'
                      : '建卡并下一条 ›'
                    : isLast
                      ? '保存并收尾'
                      : '保存并下一条 ›'
              }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue';
import WlIcon from './WlIcon.vue';
import { useOverlayMotion } from '../../../components/overlay-motion.js';
import { useModalBehavior } from '../../../components/modal-behavior.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../../components/overlay-motion.css';
import { alignFormDefaults, entryFormDefaults, findDestination, findVehicle } from '../align.js';
import { createDictVehicle, createLog, updateLog } from '../api.js';

// 派车对齐对照弹层：左表格卡只读；中间对照卡常驻（匹配/建卡，差异红字/红框），
// 右侧列当天其他卡片，点击展开就地修改（不替换中间对照卡）。
// 保存/建卡复用既有接口；成功后由父视图标记已处理并按需跳到下一条。
const props = defineProps({
  open: { type: Boolean, default: false },
  item: { type: Object, default: null },
  index: { type: Number, default: 0 },
  total: { type: Number, default: 1 },
  meta: { type: Object, default: () => ({ vehicles: [], destinations: [], members: [] }) },
  dayCards: { type: Array, default: () => [] },
});
const emit = defineEmits(['update:open', 'prev', 'skip', 'saved', 'changed', 'meta-changed']);

const open = ref(props.open);
watch(
  () => props.open,
  (v) => {
    open.value = v;
  }
);
const { rendered, phaseClass } = useOverlayMotion(open);
const panelRef = ref(null);

// requestClose 请求关闭：本地先播退出动画，再通知父级收起。
function requestClose() {
  if (!open.value) return;
  open.value = false;
  emit('update:open', false);
}
useModalBehavior(rendered, { panelRef, onEscape: requestClose });

const saving = ref(false);
const savingOther = ref(false);
const addingVeh = ref(false);
const error = ref('');
const otherError = ref('');
const info = ref('');
const addedVehicles = ref([]);
// 中间对照卡/快速建卡表单常驻；右侧其他卡片各自缓存表单（key=卡片 id），点开时初始化。
const form = reactive({
  vehicle_id: 0,
  destination_id: 0,
  dispatch_order_no: '',
  member_ids: [],
});
const otherForms = reactive({});
const openOtherId = ref(0);

const sheet = computed(() => props.item?.sheet || null);
const entry = computed(() => props.item?.entry || null);
const diffs = computed(() => props.item?.diffs || {});
const isSheetOnly = computed(() => props.item?.kind === 'sheetOnly');
const isEntryOnly = computed(() => props.item?.kind === 'entryOnly');
const isMatched = computed(() => props.item?.kind === 'matched');
const isLast = computed(() => props.index + 1 >= props.total);
const middleDiffs = computed(() => (isMatched.value ? diffs.value : {}));
const memberDiff = computed(
  () => !!(middleDiffs.value.missing?.length || middleDiffs.value.extra?.length)
);

// 当天其他卡片：中间已常驻对照卡（或快速建卡），右侧只列其余卡片，保持后端顺序。
const dayCards = computed(() => props.dayCards || []);
const otherCards = computed(() =>
  dayCards.value.filter((c) => !entry.value || c.id !== entry.value.id)
);
const allCards = computed(() => {
  const list = [...dayCards.value];
  if (entry.value && !list.some((c) => c.id === entry.value.id)) list.push(entry.value);
  return list;
});

// resetForms 打开条目时初始化中间表单（对照卡/快速建卡按表格预填），并清空右侧其他卡片展开态。
function resetForms() {
  error.value = '';
  otherError.value = '';
  info.value = '';
  saving.value = false;
  savingOther.value = false;
  addingVeh.value = false;
  openOtherId.value = 0;
  addedVehicles.value = [];
  Object.keys(otherForms).forEach((k) => delete otherForms[k]);
  const d = alignFormDefaults(props.item, props.meta);
  form.vehicle_id = d.vehicle_id;
  form.destination_id = d.destination_id;
  form.dispatch_order_no = d.dispatch_order_no;
  form.member_ids = d.member_ids;
}

// open/item 变化时回填表单（差异字段按表格预填，其余保留卡片现值）。
watch(
  () => [props.open, props.item],
  () => {
    if (!props.open) return;
    resetForms();
  },
  { immediate: true }
);

// toggleOther 展开/收起右侧其他卡片；首次展开按其现值初始化表单。
function toggleOther(card) {
  if (!card) return;
  otherError.value = '';
  info.value = '';
  if (openOtherId.value === card.id) {
    openOtherId.value = 0;
    return;
  }
  if (!otherForms[card.id]) otherForms[card.id] = entryFormDefaults(card);
  openOtherId.value = card.id;
}

// chipsFor 指定表单的用车人点亮态（右侧其他卡片无差异标记）。
function chipsFor(f) {
  if (!f) return [];
  return (props.meta.members || []).map((m) => ({
    id: m.id,
    name: m.name,
    on: f.member_ids.includes(m.id),
  }));
}

// vehicleOptions 本班启用车牌 + 就地新增车牌 + 当天卡片现值兜底（字典停用时仍可辨认）。
const vehicleOptions = computed(() => {
  const list = (props.meta.vehicles || []).map((v) => ({ id: v.id, plate_no: v.plate_no }));
  const seen = new Set(list.map((v) => v.id));
  addedVehicles.value.forEach((v) => {
    if (!seen.has(v.id)) {
      list.push({ id: v.id, plate_no: v.plate_no });
      seen.add(v.id);
    }
  });
  allCards.value.forEach((c) => {
    if (c.vehicle_id && !seen.has(c.vehicle_id)) {
      list.push({ id: c.vehicle_id, plate_no: c.plate || `#${c.vehicle_id}`, off: true });
      seen.add(c.vehicle_id);
    }
  });
  return list.map((v) => ({ ...v, label: v.plate_no + (v.off ? '（已停用）' : '') }));
});

// destinationOptions 本班启用目的地 + 当天卡片现值兜底（字典停用时仍可辨认）。
const destinationOptions = computed(() => {
  const list = (props.meta.destinations || []).map((d) => ({ id: d.id, name: d.name }));
  const seen = new Set(list.map((d) => d.id));
  allCards.value.forEach((c) => {
    if (c.destination_id && !seen.has(c.destination_id)) {
      list.push({
        id: c.destination_id,
        name: c.destination || `#${c.destination_id}`,
        off: true,
      });
      seen.add(c.destination_id);
    }
  });
  return list.map((d) => ({ ...d, label: d.name + (d.off ? '（已停用）' : '') }));
});

const plateHit = computed(() => findVehicle(props.meta, sheet.value?.plate));
const destHit = computed(() => findDestination(props.meta, sheet.value?.to));
const showAddVeh = computed(
  () => (isSheetOnly.value || isMatched.value) && !!sheet.value?.plate && !plateHit.value
);
const plateRed = computed(() => {
  if (isSheetOnly.value) return !plateHit.value && !!sheet.value?.plate;
  if (isMatched.value) return !!middleDiffs.value.plate;
  return false;
});
const plateNote = computed(() => {
  if (!sheet.value) return '';
  if (isSheetOnly.value) {
    return plateHit.value ? '已按表格预填，可改选' : `表格车牌 ${sheet.value.plate} 不在本班字典`;
  }
  if (!isMatched.value || !middleDiffs.value.plate) return '';
  return plateHit.value
    ? `与表格不一致，已按表格预填为 ${sheet.value.plate}，确认后保存`
    : `表格车牌 ${sheet.value.plate} 不在本班字典，暂无法改派`;
});
const plateNoteRed = computed(
  () => showAddVeh.value || (isMatched.value && !!middleDiffs.value.plate && !plateHit.value)
);
const destNote = computed(() => {
  if (!sheet.value) return '';
  const sheetTo = sheet.value.to || '';
  if (isSheetOnly.value) {
    if (!sheetTo) return '';
    return destHit.value
      ? '已按表格模糊匹配预填，可改选'
      : `表格目的地「${sheetTo}」与本班字典无匹配项，请手工选择或暂不选`;
  }
  if (!isMatched.value || !middleDiffs.value.dest) return '';
  return destHit.value
    ? `与表格不一致，已按表格预填为 ${destHit.value.name}，确认后保存`
    : `表格目的地「${sheetTo}」与本班字典无匹配项，请手工选择`;
});
const destNoteRed = computed(() => {
  if (!sheet.value) return false;
  if (isSheetOnly.value) return !!sheet.value.to && !destHit.value;
  return isMatched.value && !!middleDiffs.value.dest && !destHit.value;
});
const orderNote = computed(() => {
  if (!sheet.value) return '';
  if (isSheetOnly.value) return sheet.value.orderNo ? '已按表格带入，可改' : '';
  return isMatched.value && middleDiffs.value.orderNo
    ? '与表格不一致，已按表格预填，确认后保存'
    : '';
});

const chips = computed(() => {
  const extra = new Set(isMatched.value ? middleDiffs.value.extra || [] : []);
  const missing = new Set(isMatched.value ? middleDiffs.value.missing || [] : []);
  return (props.meta.members || []).map((m) => ({
    id: m.id,
    name: m.name,
    on: form.member_ids.includes(m.id),
    extra: extra.has(m.name),
    add: missing.has(m.name),
  }));
});
const unknownNames = computed(() => {
  const known = new Set((props.meta.members || []).map((m) => m.name));
  const names = isSheetOnly.value
    ? sheet.value?.members || []
    : isMatched.value
      ? middleDiffs.value.missing || []
      : [];
  return [...new Set(names.filter((n) => !known.has(n)))];
});

// memberNames 卡片用车人姓名串（列表/只读行共用）。
function memberNames(card) {
  const names = (card?.members || []).map((m) => m.name).filter(Boolean);
  return names.length ? names.join('、') : '—';
}

// toggleMember 中间对照卡/快速建卡的用车人点亮切换（保存时全量替换）。
function toggleMember(id) {
  toggleMemberIn(form, id);
}

// toggleMemberIn 指定表单的用车人点亮切换（右侧其他卡片复用）。
function toggleMemberIn(f, id) {
  if (!f) return;
  const i = f.member_ids.indexOf(id);
  if (i >= 0) f.member_ids.splice(i, 1);
  else f.member_ids.push(id);
}

// addVehicle 表格车牌就地加入本班字典，并预填到中间表单（仅对照卡/快速建卡）。
async function addVehicle() {
  const plate = sheet.value?.plate;
  if (!plate || addingVeh.value) return;
  addingVeh.value = true;
  error.value = '';
  try {
    const r = await createDictVehicle({ name: plate });
    const id = Number(r?.id) || 0;
    if (id) {
      addedVehicles.value.push({ id, plate_no: plate });
      form.vehicle_id = id;
    }
    emit('meta-changed');
  } catch (e) {
    error.value = e.message || '添加车牌失败';
  } finally {
    addingVeh.value = false;
  }
}

// patchOf 保存成功后同步清单里的卡片值（重开/切换后读新值）。
function patchOf(f, orderNo) {
  const v = vehicleOptions.value.find((x) => x.id === f.vehicle_id);
  const d = destinationOptions.value.find((x) => x.id === f.destination_id);
  return {
    vehicle_id: f.vehicle_id,
    plate: v ? v.plate_no : '',
    destination_id: f.destination_id || 0,
    destination: d ? d.name : '',
    order_no: orderNo,
    members: (props.meta.members || [])
      .filter((m) => f.member_ids.includes(m.id))
      .map((m) => ({ member_id: m.id, name: m.name })),
  };
}

// saveOther 保存右侧展开的当天其他卡片（不动对齐条目的处理态；成功后收起该卡）。
// 与改卡同口径：整卡更新带回 patrol_content，被移出成员已有照片由后端 40006 拒绝。
async function saveOther(card) {
  const f = otherForms[card?.id];
  if (!card || !f || savingOther.value) return;
  if (!f.vehicle_id) {
    otherError.value = '请选择车牌号';
    return;
  }
  otherError.value = '';
  info.value = '';
  savingOther.value = true;
  try {
    const orderNo = String(f.dispatch_order_no || '').trim();
    await updateLog(card.id, {
      vehicle_id: f.vehicle_id,
      destination_id: f.destination_id || 0,
      dispatch_order_no: orderNo,
      patrol_content: card.patrol_content || '',
      member_ids: f.member_ids,
    });
    Object.assign(card, patchOf(f, orderNo));
    info.value = `已保存卡片 #${card.id}`;
    openOtherId.value = 0;
    emit('changed');
  } catch (e) {
    otherError.value = e.message || '保存失败';
  } finally {
    savingOther.value = false;
  }
}

// submit 保存中间对照卡更正（PUT 整卡）/ 快速建卡（POST）：失败留在弹层展示原话原因
// （如被移出成员已有水印照片 40006、同日期同人占用 40020）。
async function submit(advance) {
  if (isEntryOnly.value || saving.value) return;
  if (!form.vehicle_id) {
    error.value = '请选择车牌号（表格车牌不在字典时可先「添加进字典」）';
    return;
  }
  error.value = '';
  info.value = '';
  saving.value = true;
  try {
    const orderNo = String(form.dispatch_order_no || '').trim();
    if (isSheetOnly.value) {
      await createLog({
        log_date: props.item.date,
        patrol_content: '',
        vehicle_id: form.vehicle_id,
        destination_id: form.destination_id || 0,
        dispatch_order_no: orderNo,
        member_ids: form.member_ids,
      });
      emit('saved', { index: props.index, advance, patch: null });
    } else {
      await updateLog(entry.value.id, {
        vehicle_id: form.vehicle_id,
        destination_id: form.destination_id || 0,
        dispatch_order_no: orderNo,
        patrol_content: entry.value.patrol_content || '',
        member_ids: form.member_ids,
      });
      emit('saved', { index: props.index, advance, patch: patchOf(form, orderNo) });
    }
  } catch (e) {
    error.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
/* ===== 弹层骨架（宽版对照：1020px，窄屏两卡上下堆叠） ===== */
.al-mask {
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
.al-modal {
  width: 1240px;
  max-width: 96vw;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  background: var(--tk-card);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-hover);
  overflow: hidden;
  outline: none;
}
.al-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 20px 18px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--tk-dash) transparent;
}
.al-scroll::-webkit-scrollbar {
  width: 5px;
}
.al-scroll::-webkit-scrollbar-thumb {
  background: var(--tk-dash);
  border-radius: 8px;
}
.al-scroll::-webkit-scrollbar-track {
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
.al-prog {
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
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

/* ===== 左表格卡 / 中对照卡 / 右其他卡片（异常卡常驻中间） ===== */
.al-cards {
  display: grid;
  grid-template-columns: minmax(0, 0.9fr) minmax(0, 1fr) minmax(0, 0.9fr);
  gap: 14px;
  align-items: stretch;
}
.al-card {
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--tk-card);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
}
.al-h {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 14px;
  background: var(--tk-fill);
  border-bottom: 1px solid var(--tk-line);
}
.al-h-t {
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.al-h .sub {
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.al-h .src {
  margin-left: auto;
  max-width: 45%;
  font-size: 11px;
  color: var(--tk-ph);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.al-rows {
  padding: 4px 14px 8px;
}
.al-r {
  display: flex;
  gap: 12px;
  padding: 9px 0;
  border-bottom: 1px dashed var(--tk-hair);
  font-size: 12.5px;
  line-height: 1.6;
}
.al-r:last-child {
  border-bottom: 0;
}
.al-r .k {
  flex: none;
  width: 68px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
}
.al-r .v {
  flex: 1;
  min-width: 0;
  color: var(--tk-ink);
  word-break: break-all;
}
/* 差异字段：左色条 + 红字（警示条一族直角，不做大色块） */
.al-r.diff {
  box-shadow: inset 2px 0 0 var(--tk-red);
  padding-left: 9px;
}
.al-r.diff .v {
  color: var(--tk-red);
  font-weight: 700;
}
.al-empty {
  padding: 26px 16px;
  font-size: 12px;
  color: var(--tk-ph);
  text-align: center;
  line-height: 1.9;
}
.al-tip {
  margin: 10px 0 2px;
  padding: 8px 10px;
  background: var(--tk-blue-soft);
  font-size: 11.5px;
  color: var(--tk-sub);
  line-height: 1.8;
}

/* ===== 右侧可改表单 ===== */
.al-form {
  padding: 4px 14px 10px;
}
.al-field {
  padding: 9px 0;
  border-bottom: 1px dashed var(--tk-hair);
}
.al-field:last-child {
  border-bottom: 0;
}
.al-lab {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  margin-bottom: 6px;
}
.al-tag {
  font-style: normal;
  font-size: 10.5px;
  font-weight: 700;
  color: var(--tk-red);
  background: var(--tk-red-soft);
  border-radius: 4px;
  padding: 1px 6px;
}
.a-sel {
  width: 100%;
  height: 34px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card);
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.a-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--tk-brand-glow);
}
.a-sel.warn {
  border-color: var(--tk-red);
  background: var(--tk-red-soft);
}
.a-note {
  margin-top: 6px;
  font-size: 11px;
  color: var(--tk-ph);
  line-height: 1.7;
}
.a-note.red {
  color: var(--tk-red);
  font-weight: 700;
}
.a-link {
  margin-left: 6px;
  border: 0;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 11px;
  font-weight: 700;
  text-decoration: underline;
  font-family: inherit;
}
.a-link:disabled {
  color: var(--tk-ph);
  cursor: not-allowed;
}
.a-ro {
  font-size: 12px;
  color: var(--tk-ink);
  line-height: 1.8;
  word-break: break-all;
}
.a-sub {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  color: var(--tk-ph);
}

/* 用车人 chips：红框 = 卡片多出；蓝虚线 = 表格有而卡片缺 */
.al-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
}
.al-chip {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card);
  color: var(--tk-ink);
  font-size: 12px;
  padding: 4px 12px;
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.18s var(--tk-ease),
    background 0.18s var(--tk-ease),
    color 0.18s var(--tk-ease);
}
.al-chip.extra {
  border-color: var(--tk-red);
  color: var(--tk-red);
  font-weight: 700;
}
.al-chip.add {
  border-style: dashed;
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.al-chip.on {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: var(--tk-on-brand, #ffffff);
  font-weight: 700;
}
.al-chip.extra.on {
  border-color: var(--tk-red);
  background: var(--tk-red);
  color: var(--tk-on-brand, #ffffff);
}
.al-chip:hover:not(.on):not(.extra) {
  border-color: var(--tk-brand);
}

/* ===== 对照卡头部徽标 ===== */
.al-h-badge {
  flex: none;
  font-size: 10.5px;
  font-weight: 700;
  padding: 1px 7px;
  border-radius: 4px;
}
.al-h-badge.bad {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.al-h-badge.add {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}

/* ===== 右侧当天其他卡片（点击展开查看/修改） ===== */
.al-others {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px 12px;
}
.al-other {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
  transition: border-color 0.16s var(--tk-ease);
}
.al-other:hover {
  border-color: var(--tk-brand);
}
.al-other.open {
  border-color: var(--tk-brand);
}
.al-other-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  background: var(--tk-fill);
  text-align: left;
  font-size: 11.5px;
  color: var(--tk-sub);
  line-height: 1.5;
  cursor: pointer;
  font-family: inherit;
}
.al-other.open .al-other-head {
  background: var(--tk-blue-soft);
}
.al-other-plate {
  flex: none;
  color: var(--tk-ink);
  font-weight: 700;
  letter-spacing: 0.5px;
}
.al-other-meta {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.al-other-id {
  flex: none;
  color: var(--tk-ph);
}
.al-other-body {
  padding: 2px 12px 10px;
  border-top: 1px solid var(--tk-line);
}
.al-other-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 8px;
}
.al-other-actions .a-sub {
  margin-top: 0;
}

/* ===== 固定操作脚：错误 + 按钮行始终可见 ===== */
.al-footwrap {
  flex: none;
  padding: 10px 20px 14px;
  border-top: 1px solid var(--tk-line);
  background: var(--tk-card);
}
.al-error {
  margin-bottom: 8px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.al-info {
  margin-bottom: 8px;
  background: var(--tk-blue-soft);
  border-left: 3px solid var(--tk-brand);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-brand);
}
.al-foot {
  display: flex;
  align-items: center;
  gap: 10px;
}
.a-skip {
  border: 0;
  background: none;
  padding: 0;
  color: var(--tk-sub);
  font-size: 12px;
  text-decoration: underline dotted;
  font-family: inherit;
  cursor: pointer;
}
.a-skip:hover {
  color: var(--tk-brand);
}
.sp {
  flex: 1;
}

/* 按钮基线（弹层内局部样式，与页面 .btn 同口径） */
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

@media (max-width: 1180px) {
  .al-cards {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  }
  .al-cards > .al-card:last-child {
    grid-column: 1 / -1;
  }
}

@media (max-width: 820px) {
  .al-mask {
    padding: 12px;
  }
  .al-cards {
    grid-template-columns: 1fr;
  }
  .al-cards > .al-card:last-child {
    grid-column: auto;
  }
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
