<template>
  <div>
    <div class="td-dsec">
      <div class="dsec-h">
        <b>运维线路</b>
        <span class="cnt">{{
          loading ? '加载中…' : error ? '加载失败' : `${list.length} 条`
        }}</span>
        <span class="lt-btns">
          <button type="button" class="btn btn-sm" :disabled="tplBusy" @click="downloadTemplate">
            <span v-html="icon('download', 14)"></span>下载模板
          </button>
          <button type="button" class="btn btn-sm btn-primary" @click="pickImport">
            <span v-html="icon('upload', 14)"></span>导入 Excel
          </button>
        </span>
      </div>

      <!-- 顶部双输入即添加（回车 / 点按钮提交，sort 服务端自动追加） -->
      <div class="dadd">
        <input
          v-model="newVolt"
          class="dadd-in ol-v"
          placeholder="电压等级（如 110）"
          maxlength="32"
          autocomplete="off"
          @keydown.enter.prevent="add"
        />
        <input
          v-model="newName"
          class="dadd-in"
          placeholder="输入新线路名称"
          maxlength="64"
          autocomplete="off"
          @keydown.enter.prevent="add"
        />
        <button type="button" class="btn btn-sm btn-primary" :disabled="adding" @click="add">
          添加
        </button>
      </div>

      <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
      <template v-else>
        <div v-if="error" class="dempty">
          {{ error }}　<a class="linkbtn" @click="reload">重试</a>
        </div>
        <template v-else>
          <div v-if="!list.length" class="dempty">暂无运维线路，可在上方添加</div>
          <div v-for="(it, idx) in list" :key="it.id" class="drow">
            <span class="olv">{{ fmtKv(it.voltage_level) }}</span>
            <span class="dn">{{ it.line_name || '—' }}</span>
            <span class="dops">
              <button v-if="idx > 0" type="button" class="linkbtn" @click="move(it, 'up')">
                上移
              </button>
              <button
                v-if="idx < list.length - 1"
                type="button"
                class="linkbtn"
                @click="move(it, 'down')"
              >
                下移
              </button>
              <button type="button" class="linkbtn" @click="openEdit(it)">编辑</button>
              <button type="button" class="linkbtn dgr" @click="confirmDel(it)">删除</button>
            </span>
          </div>
        </template>
      </template>

      <div class="ol-foot">供树障隐患记录分组使用</div>
    </div>

    <input
      ref="importInput"
      type="file"
      accept=".xlsx"
      class="td-hidden-input"
      @change="onImport"
    />

    <!-- 编辑弹层（电压等级 + 线路名称；均不变直接关闭） -->
    <Modal v-if="editOpen" title="编辑运维线路" @close="editOpen = false">
      <label class="f-lab">电压等级</label>
      <input
        ref="voltInput"
        v-model="editForm.voltage_level"
        class="f-sel"
        maxlength="32"
        autocomplete="off"
        placeholder="如 110"
      />
      <label class="f-lab">线路名称</label>
      <input
        v-model="editForm.line_name"
        class="f-sel"
        maxlength="64"
        autocomplete="off"
        @keydown.enter="save"
      />
      <div v-if="editError" class="dempty" style="border-top: none; padding-left: 0">
        {{ editError }}
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="editOpen = false">取消</button>
        <button type="button" class="btn btn-sm btn-primary" :disabled="saving" @click="save">
          保 存
        </button>
      </template>
    </Modal>

    <!-- 删除二次确认 -->
    <Modal v-if="delItem" title="删除运维线路" @close="delItem = null">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        删除后不可恢复，确定删除「{{ fmtKv(delItem.voltage_level) }}
        {{ delItem.line_name || '' }}」吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="delItem = null">取消</button>
        <button type="button" class="btn btn-sm btn-danger" :disabled="deleting" @click="remove">
          删除
        </button>
      </template>
    </Modal>

    <!-- 导入确认（全量替换） -->
    <Modal v-if="impConfirm" title="导入运维线路" @close="impConfirm = false">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        导入将全量替换本班组运维线路，确定继续吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="impConfirm = false">取消</button>
        <button type="button" class="btn btn-sm btn-primary" @click="runImport">导入</button>
      </template>
    </Modal>

    <!-- 导入进行中 -->
    <Modal v-if="impBusy" title="导入运维线路" :width="280" @close="() => {}">
      <div class="m-loading"><span class="spin"></span>正在导入…</div>
    </Modal>
  </div>
</template>

<script setup>
import { nextTick, onMounted, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  createOpline,
  deleteOpline,
  downloadOplineTemplate,
  importOplines,
  listOplines,
  moveOpline,
  updateOpline,
} from '../api.js';
import { fmtKv, icon } from '../helpers.js';
import Modal from './Modal.vue';

// 运维线路段（team_line，对照旧版 teamdata.html）：顶部双输入即添加；行内 上移/下移·编辑·删除；
// 模板下载 + Excel 导入全量替换（前端先校验 .xlsx 与 10MB）；40304 → 通知整页门控。
const emit = defineEmits(['forbidden']);

const list = ref([]);
const loading = ref(false);
const error = ref('');
const newVolt = ref('');
const newName = ref('');
const adding = ref(false);
const tplBusy = ref(false);
const importInput = ref(null);
const impConfirm = ref(false);
const impBusy = ref(false);
const impFile = ref(null);
const editOpen = ref(false);
const editing = ref(null);
const saving = ref(false);
const editError = ref('');
const editForm = reactive({ voltage_level: '', line_name: '' });
const voltInput = ref(null);
const delItem = ref(null);
const deleting = ref(false);

// gateOnForbidden 40304 → 整页门控。
function gateOnForbidden(e) {
  if (e && e.code === 40304) {
    emit('forbidden');
    return true;
  }
  return false;
}

// reload 列表加载。
async function reload() {
  loading.value = true;
  error.value = '';
  try {
    const data = await listOplines();
    list.value = data.list || [];
  } catch (e) {
    if (gateOnForbidden(e)) return;
    error.value = e.message || '加载失败';
    list.value = [];
  } finally {
    loading.value = false;
  }
}

// add 电压等级 + 线路名称双输入添加（电压等级原样入库，显示侧 fmtKv 幂等拼 kV）。
async function add() {
  const volt = newVolt.value.trim();
  const name = newName.value.trim();
  if (!volt) {
    MessagePlugin.info('请输入电压等级');
    return;
  }
  if (!name) {
    MessagePlugin.info('请输入线路名称');
    return;
  }
  adding.value = true;
  try {
    await createOpline({ voltage_level: volt, line_name: name });
    newVolt.value = '';
    newName.value = '';
    MessagePlugin.success('已添加');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '添加失败');
  } finally {
    adding.value = false;
  }
}

// openEdit 编辑弹层（电压等级 + 线路名称）。
async function openEdit(row) {
  editing.value = row;
  editForm.voltage_level = row.voltage_level || '';
  editForm.line_name = row.line_name || '';
  editError.value = '';
  editOpen.value = true;
  await nextTick();
  if (voltInput.value) voltInput.value.focus();
}

// save 编辑保存（均不变直接关闭）。
async function save() {
  const volt = editForm.voltage_level.trim();
  const name = editForm.line_name.trim();
  if (!volt) {
    editError.value = '电压等级不能为空';
    return;
  }
  if (!name) {
    editError.value = '线路名称不能为空';
    return;
  }
  const item = editing.value || {};
  if (volt === (item.voltage_level || '') && name === (item.line_name || '')) {
    editOpen.value = false;
    return;
  }
  saving.value = true;
  editError.value = '';
  try {
    await updateOpline(item.id, { voltage_level: volt, line_name: name });
    editOpen.value = false;
    MessagePlugin.success('已保存');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) editError.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}

// move 上移 / 下移（契约字段 direction:'up'|'down'）。
async function move(row, direction) {
  try {
    await moveOpline(row.id, direction);
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '排序失败');
  }
}

function confirmDel(row) {
  delItem.value = row;
}

// remove 删除（被引用时后端拒绝则 toast 后端文案）。
async function remove() {
  const row = delItem.value;
  if (!row) return;
  deleting.value = true;
  try {
    await deleteOpline(row.id);
    delItem.value = null;
    MessagePlugin.success('已删除');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '删除失败');
  } finally {
    deleting.value = false;
  }
}

// downloadTemplate 导入模板 xlsx。
async function downloadTemplate() {
  tplBusy.value = true;
  try {
    await downloadOplineTemplate();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '模板下载失败');
  } finally {
    tplBusy.value = false;
  }
}

function pickImport() {
  importInput.value && importInput.value.click();
}

// onImport 前端先校验扩展名与大小，再走「全量替换」二次确认；选完即清空以便重复选择同名文件。
function onImport(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  if (!/\.xlsx$/i.test(file.name)) {
    MessagePlugin.info('仅支持 .xlsx 文件，请使用模板填写');
    return;
  }
  if (file.size > 10 * 1024 * 1024) {
    MessagePlugin.info('文件大小应在 10MB 以内');
    return;
  }
  impFile.value = file;
  impConfirm.value = true;
}

// runImport 确认后 FormData 上传（全量替换），toast 后端 message 并刷新本段。
async function runImport() {
  const file = impFile.value;
  if (!file) return;
  impConfirm.value = false;
  impBusy.value = true;
  try {
    const data = await importOplines(file);
    const skipped = data && data.skipped;
    MessagePlugin.success(`导入完成${skipped ? `（跳过 ${skipped} 行）` : ''}`);
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '导入失败');
  } finally {
    impBusy.value = false;
    impFile.value = null;
  }
}

onMounted(reload);
</script>

<style scoped>
.td-hidden-input {
  display: none;
}
</style>
