<template>
  <!-- 手动新增/编辑统一弹层（新增 record=null 与编辑共用一套表单）：发现信息 13 项 + 处理信息 3 项；
       校验失败提示保留在弹层内且不丢输入；成功由父级 done() 关闭并真实刷新。 -->
  <t-dialog
    v-model:visible="visible"
    :header="record ? '编辑记录' : '新增记录'"
    :close-on-overlay-click="false"
    :confirm-btn="null"
    :cancel-btn="null"
  >
    <div class="ed-form">
      <div class="ed-sec">发现信息</div>
      <div class="ed-grid">
        <label class="ed-cell wide">
          <span class="f-lab">线路名称 <i class="req">*</i></span>
          <input v-model="form.line_name" class="f-sel" maxlength="64" placeholder="如：甲线" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">电压等级</span>
          <input v-model="form.voltage_level" class="f-sel" maxlength="16" placeholder="如：110" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">区段</span>
          <input v-model="form.section" class="f-sel" maxlength="32" placeholder="如：3#-4#" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">位置备注</span>
          <input v-model="form.position_note" class="f-sel" maxlength="32" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">树种</span>
          <input v-model="form.tree_species" class="f-sel" maxlength="32" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">数量</span>
          <input v-model="form.tree_count" class="f-sel" maxlength="16" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">距小号侧（m）</span>
          <input v-model="form.dist_small" class="f-sel" maxlength="16" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">距大号侧（m）</span>
          <input v-model="form.dist_big" class="f-sel" maxlength="16" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">直径（cm）</span>
          <input v-model="form.diameter" class="f-sel" maxlength="16" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">水平距离（m）</span>
          <input v-model="form.horizontal" class="f-sel" maxlength="16" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">垂直距离</span>
          <input v-model="form.vertical" class="f-sel" maxlength="16" placeholder="如：超高2.5" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">发现人</span>
          <input v-model="form.finder_name" class="f-sel" maxlength="32" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">发现时间</span>
          <input v-model="form.found_at" type="date" class="f-sel" @click="openPicker" />
        </label>
      </div>
      <div class="ed-sec">处理信息</div>
      <div class="ed-grid">
        <label class="ed-cell">
          <span class="f-lab">处理人</span>
          <input v-model="form.handler_name" class="f-sel" maxlength="32" />
        </label>
        <label class="ed-cell wide">
          <span class="f-lab">处理情况</span>
          <input v-model="form.handle_note" class="f-sel" maxlength="64" placeholder="如：削枝" />
        </label>
        <label class="ed-cell">
          <span class="f-lab">处理时间</span>
          <input v-model="form.handled_at" type="date" class="f-sel" @click="openPicker" />
        </label>
      </div>
      <div class="f-hint">留空处理时间视为未处理；垂直距离原文自动解析数值供阈值比对。</div>
      <div v-if="errText" class="f-err">{{ errText }}</div>
      <div class="m-foot">
        <button class="btn btn-sm" type="button" :disabled="submitting" @click="visible = false">
          取消
        </button>
        <button class="btn btn-sm btn-primary" type="button" :disabled="submitting" @click="onSubmit">
          {{ submitting ? '提交中…' : record ? '保存修改' : '确认新增' }}
        </button>
      </div>
    </div>
  </t-dialog>
</template>

<script setup>
import { reactive, ref, watch } from 'vue';

const props = defineProps({
  visible: { type: Boolean, default: false },
  // record 编辑对象；null = 新增。表单字段与后端 manual 业务列同名。
  record: { type: Object, default: null },
  // presetLine 新增时的线路名预填（当前台账线路）。
  presetLine: { type: String, default: '' },
});
const emit = defineEmits(['update:visible', 'submit']);

const visible = ref(props.visible);
const submitting = ref(false);
const errText = ref('');

const BLANK = {
  line_name: '',
  voltage_level: '',
  section: '',
  position_note: '',
  tree_species: '',
  tree_count: '',
  dist_small: '',
  dist_big: '',
  diameter: '',
  horizontal: '',
  vertical: '',
  finder_name: '',
  found_at: '',
  handler_name: '',
  handle_note: '',
  handled_at: '',
};
const form = reactive({ ...BLANK });

watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) {
      errText.value = '';
      submitting.value = false;
      const r = props.record;
      Object.keys(BLANK).forEach((k) => {
        form[k] = r && r[k] != null ? String(r[k]) : BLANK[k];
      });
      if (!r) form.line_name = props.presetLine || '';
    }
  }
);
watch(visible, (v) => emit('update:visible', v));

// openPicker 点日期文字区直接唤起原生日历（原生仅点图标弹出；不支持或非手势时静默回退）
function openPicker(e) {
  try {
    e.target.showPicker?.();
  } catch {
    /* 忽略：仍可点输入框右侧图标选择 */
  }
}

function onSubmit() {
  if (submitting.value) return;
  errText.value = '';
  const body = {};
  Object.keys(BLANK).forEach((k) => {
    body[k] = String(form[k] || '').trim();
  });
  if (!body.line_name) {
    errText.value = '线路名称不能为空';
    return;
  }
  submitting.value = true;
  emit('submit', {
    body,
    done: () => {
      submitting.value = false;
      visible.value = false;
    },
    fail: (msg) => {
      submitting.value = false;
      errText.value = msg || '保存失败，请重试';
    },
  });
}
</script>

<style scoped>
.ed-form {
  display: flex;
  flex-direction: column;
}
/* 分组小节标题（与旧版表单 f-lab 口径一致，加一档层级） */
.ed-sec {
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 1px;
  margin: 4px 0 2px;
}
.ed-sec:not(:first-of-type) {
  margin-top: 14px;
}
/* 两列表单（窄屏自动单列） */
.ed-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 14px;
}
@media (max-width: 640px) {
  .ed-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
.ed-cell {
  display: block;
  min-width: 0;
}
.ed-cell.wide {
  grid-column: 1 / -1;
}
.f-lab {
  display: block;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  margin: 12px 0 7px;
}
.f-lab .req {
  color: var(--tk-red);
  font-style: normal;
}
.f-sel {
  width: 100%;
  height: 40px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  outline: none;
  font-size: 13px;
  color: var(--tk-ink);
  font-family: inherit;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.f-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
}
.f-hint {
  margin-top: 12px;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  line-height: 1.7;
}
.f-err {
  margin-top: 8px;
  padding: 8px 12px;
  background: var(--tk-red-soft, #ffece8);
  color: var(--tk-red);
  font-size: 12px;
  letter-spacing: 0.5px;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

/* ===== 旧式按钮（同 HandleDialog 口径） ===== */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
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
  color: #fff;
  border-color: transparent;
  background: var(--tk-brand);
}
.btn-primary:hover {
  color: #fff;
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}
</style>
