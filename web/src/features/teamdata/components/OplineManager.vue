<template>
  <div class="card">
    <div class="toolbar">
      <h2 class="card-title" style="margin: 0">运维线路</h2>
      <div class="spacer"></div>
      <t-button variant="outline" @click="downloadTemplate">下载模板</t-button>
      <t-button variant="outline" @click="pickImport">导入 Excel</t-button>
      <input
        ref="importInput"
        type="file"
        accept=".xlsx"
        class="td-hidden-input"
        @change="onImport"
      />
      <t-button @click="openCreate">新增线路</t-button>
    </div>

    <div v-if="error" class="td-error">{{ error }}</div>
    <div v-if="importMsg" class="td-ok">{{ importMsg }}</div>

    <t-table
      :data="list"
      :columns="columns"
      :loading="loading"
      row-key="id"
      size="small"
      :bordered="false"
    >
      <template #sort="{ row }">
        <button type="button" class="td-move" aria-label="上移" @click="move(row, 'up')">↑</button>
        <button type="button" class="td-move" aria-label="下移" @click="move(row, 'down')">
          ↓
        </button>
      </template>
      <template #ops="{ row }">
        <button type="button" class="td-act" @click="openEdit(row)">编辑</button>
        <button type="button" class="td-act danger" @click="remove(row)">删除</button>
      </template>
    </t-table>

    <!-- 新增/编辑弹窗 -->
    <div v-if="editOpen" class="td-modal-mask" @click.self="editOpen = false">
      <div class="td-modal card">
        <h3 class="card-title">{{ editing ? '编辑' : '新增' }}线路</h3>
        <div class="td-field">
          <span class="field-label">电压等级</span>
          <input
            v-model="editForm.voltage_level"
            type="text"
            class="td-input"
            placeholder="如 110kV"
          />
        </div>
        <div class="td-field">
          <span class="field-label">线路名称</span>
          <input
            v-model="editForm.line_name"
            type="text"
            class="td-input"
            placeholder="请输入线路名称"
          />
        </div>
        <div v-if="editError" class="td-error">{{ editError }}</div>
        <div class="td-modal-foot">
          <t-button variant="outline" @click="editOpen = false">取消</t-button>
          <t-button :loading="saving" @click="save">保存</t-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import {
  createOpline,
  deleteOpline,
  downloadOplineTemplate,
  importOplines,
  listOplines,
  moveOpline,
  updateOpline,
} from '../api.js';

// 运维线路管理（team_line）：CRUD + 上移/下移排序 + 模板下载 + Excel 导入。
const list = ref([]);
const loading = ref(false);
const error = ref('');
const importMsg = ref('');
const editOpen = ref(false);
const editing = ref(null);
const saving = ref(false);
const editError = ref('');
const editForm = reactive({ voltage_level: '', line_name: '' });
const importInput = ref(null);

const columns = [
  { colKey: 'voltage_level', title: '电压等级', width: 140 },
  { colKey: 'line_name', title: '线路名称' },
  { colKey: 'sort', title: '排序', cell: 'sort', width: 100 },
  { colKey: 'ops', title: '操作', cell: 'ops', width: 130 },
];

// reload 列表加载。
async function reload() {
  loading.value = true;
  error.value = '';
  try {
    const data = await listOplines();
    list.value = data.list || [];
  } catch (e) {
    error.value = e.message || '加载失败';
    list.value = [];
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editing.value = null;
  editForm.voltage_level = '';
  editForm.line_name = '';
  editError.value = '';
  editOpen.value = true;
}

function openEdit(row) {
  editing.value = row;
  editForm.voltage_level = row.voltage_level || '';
  editForm.line_name = row.line_name || '';
  editError.value = '';
  editOpen.value = true;
}

// save 新增/编辑。
async function save() {
  editError.value = '';
  if (!editForm.voltage_level.trim() || !editForm.line_name.trim()) {
    editError.value = '请输入电压等级与线路名称';
    return;
  }
  saving.value = true;
  try {
    const payload = {
      voltage_level: editForm.voltage_level.trim(),
      line_name: editForm.line_name.trim(),
    };
    if (editing.value) await updateOpline(editing.value.id, payload);
    else await createOpline(payload);
    editOpen.value = false;
    MessagePlugin.success('已保存');
    reload();
  } catch (e) {
    editError.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}

// move 上移/下移排序。
async function move(row, direction) {
  try {
    await moveOpline(row.id, direction);
    reload();
  } catch (e) {
    MessagePlugin.error(e.message || '排序失败');
  }
}

// remove 删除。
function remove(row) {
  const dlg = DialogPlugin.confirm({
    header: '删除线路',
    body: `确定删除「${row.line_name}」？树障记录将落为未匹配。`,
    confirmBtn: '删除',
    onConfirm: async () => {
      try {
        await deleteOpline(row.id);
        MessagePlugin.success('已删除');
        reload();
      } catch (e) {
        MessagePlugin.error(e.message || '删除失败');
      } finally {
        dlg.destroy();
      }
    },
  });
}

// downloadTemplate 导入模板 xlsx。
async function downloadTemplate() {
  error.value = '';
  try {
    await downloadOplineTemplate();
  } catch (e) {
    error.value = e.message || '模板下载失败';
  }
}

function pickImport() {
  importMsg.value = '';
  importInput.value && importInput.value.click();
}

// onImport Excel 导入。
async function onImport(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  error.value = '';
  try {
    const data = await importOplines(file);
    importMsg.value =
      `已导入 ${data.imported} 条线路` + (data.skipped ? `，跳过 ${data.skipped} 条` : '');
    reload();
  } catch (e) {
    error.value = e.message || '导入失败';
  }
}

onMounted(reload);
</script>

<style scoped>
.td-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.td-ok {
  margin: 8px 0;
  background: var(--tk-green-soft);
  border-left: 3px solid var(--tk-green);
  padding: 8px 10px;
  font-size: 12px;
  color: #0b8a37;
}
.td-hidden-input {
  display: none;
}
.td-move {
  width: 24px;
  height: 24px;
  border: 1px solid var(--tk-line);
  border-radius: 4px;
  background: #fff;
  color: var(--tk-sub);
  cursor: pointer;
  margin-right: 4px;
}
.td-move:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
.td-act {
  border: none;
  background: none;
  padding: 0;
  margin-right: 12px;
  color: var(--tk-brand);
  font-size: 13px;
  cursor: pointer;
}
.td-act.danger {
  color: var(--tk-red);
}
.td-modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.td-modal {
  width: min(420px, calc(100vw - 32px));
}
.td-field {
  display: flex;
  gap: 10px;
  align-items: center;
  padding: 7px 0;
}
.td-field .field-label {
  width: 64px;
  flex-shrink: 0;
}
.td-input {
  flex: 1;
  min-width: 0;
  height: 32px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 0 10px;
  font-size: 13px;
  color: var(--tk-ink);
  background: var(--tk-fill);
}
.td-input:focus {
  outline: none;
  border-color: var(--tk-brand);
  background: #fff;
}
.td-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
