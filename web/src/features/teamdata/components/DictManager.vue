<template>
  <div class="card">
    <div class="toolbar">
      <h2 class="card-title" style="margin: 0">{{ title }}</h2>
      <div class="spacer"></div>
      <t-button variant="outline" @click="reload">刷新</t-button>
      <t-button @click="openCreate">新增{{ label }}</t-button>
    </div>

    <div v-if="error" class="td-error">{{ error }}</div>

    <t-table
      :data="list"
      :columns="columns"
      :loading="loading"
      row-key="id"
      size="small"
      :bordered="false"
    >
      <template #name="{ row }">
        <span class="td-name">{{ row.name }}</span>
        <span v-if="kind === 'members' && row.user_id" class="muted">（已绑账号）</span>
      </template>

      <!-- 目的地：市外/驻地互斥标记 -->
      <template #flags="{ row }">
        <template v-if="kind === 'destinations'">
          <button
            type="button"
            class="td-flag"
            :class="{ on: row.out_of_city === 1 }"
            @click="toggleFlag(row, 'out_of_city')"
          >
            市外
          </button>
          <button
            type="button"
            class="td-flag"
            :class="{ on: row.stationed === 1 }"
            @click="toggleFlag(row, 'stationed')"
          >
            驻地
          </button>
        </template>
      </template>

      <!-- 成员排序 -->
      <template #sort="{ row }">
        <template v-if="kind === 'members'">
          <button type="button" class="td-move" aria-label="上移" @click="move(row, 'up')">
            ↑
          </button>
          <button type="button" class="td-move" aria-label="下移" @click="move(row, 'down')">
            ↓
          </button>
        </template>
        <span v-else class="num muted">{{ row.sort }}</span>
      </template>

      <!-- 状态 -->
      <template #status="{ row }">
        <button
          type="button"
          class="td-status"
          :class="{ on: row.status === 1 }"
          @click="toggleStatus(row)"
        >
          <span class="dot"></span>{{ row.status === 1 ? '启用' : '停用' }}
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
        <h3 class="card-title">{{ editing ? '编辑' : '新增' }}{{ label }}</h3>
        <div class="td-field">
          <span class="field-label">名称</span>
          <input
            v-model="editForm.name"
            type="text"
            class="td-input"
            :placeholder="`请输入${label}名称`"
          />
        </div>
        <div v-if="kind === 'destinations'" class="td-field">
          <span class="field-label">标记</span>
          <div class="td-flags">
            <button
              type="button"
              class="td-flag"
              :class="{ on: editForm.out_of_city === 1 }"
              @click="setEditFlag('out_of_city')"
            >
              市外
            </button>
            <button
              type="button"
              class="td-flag"
              :class="{ on: editForm.stationed === 1 }"
              @click="setEditFlag('stationed')"
            >
              驻地
            </button>
          </div>
          <div class="muted">市外与驻地只能标记其一（费用口径联动）</div>
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
import { createDict, deleteDict, listDict, moveMember, updateDict } from '../api.js';

// 班组字典管理（车牌 / 目的地 / 出工成员）：列表 + 新增/编辑/删除 + 启停 + 成员排序。
// kind=vehicles|destinations|members，走 /api/v1/teamdata/{kind}；失败如实提示。
const props = defineProps({
  kind: { type: String, required: true },
  title: { type: String, required: true },
  label: { type: String, required: true },
});

const list = ref([]);
const loading = ref(false);
const error = ref('');
const editOpen = ref(false);
const editing = ref(null);
const saving = ref(false);
const editError = ref('');
const editForm = reactive({ name: '', out_of_city: 0, stationed: 0 });

const columns = [
  { colKey: 'name', title: '名称', cell: 'name' },
  { colKey: 'flags', title: '标记', cell: 'flags', width: 140 },
  { colKey: 'sort', title: '排序', cell: 'sort', width: 100 },
  { colKey: 'status', title: '状态', cell: 'status', width: 100 },
  { colKey: 'ops', title: '操作', cell: 'ops', width: 130 },
];

// reload 列表加载。
async function reload() {
  loading.value = true;
  error.value = '';
  try {
    const data = await listDict(props.kind);
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
  editForm.name = '';
  editForm.out_of_city = 0;
  editForm.stationed = 0;
  editError.value = '';
  editOpen.value = true;
}

function openEdit(row) {
  editing.value = row;
  editForm.name = row.name || '';
  editForm.out_of_city = row.out_of_city === 1 ? 1 : 0;
  editForm.stationed = row.stationed === 1 ? 1 : 0;
  editError.value = '';
  editOpen.value = true;
}

// setEditFlag 市外/驻地互斥标记。
function setEditFlag(key) {
  const next = editForm[key] === 1 ? 0 : 1;
  editForm[key] = next;
  if (next === 1) editForm[key === 'out_of_city' ? 'stationed' : 'out_of_city'] = 0;
}

// toggleFlag 列表内即时切换标记（PUT）。
async function toggleFlag(row, key) {
  const next = row[key] === 1 ? 0 : 1;
  try {
    await updateDict(props.kind, row.id, { [key]: next });
    if (next === 1) row[key === 'out_of_city' ? 'stationed' : 'out_of_city'] = 0;
    row[key] = next;
  } catch (e) {
    MessagePlugin.error(e.message || '标记失败');
  }
}

// save 新增/编辑。
async function save() {
  editError.value = '';
  if (!editForm.name.trim()) {
    editError.value = `请输入${props.label}名称`;
    return;
  }
  saving.value = true;
  try {
    const payload = { name: editForm.name.trim() };
    if (props.kind === 'destinations') {
      payload.out_of_city = editForm.out_of_city;
      payload.stationed = editForm.stationed;
    }
    if (editing.value) await updateDict(props.kind, editing.value.id, payload);
    else await createDict(props.kind, payload);
    editOpen.value = false;
    MessagePlugin.success('已保存');
    reload();
  } catch (e) {
    editError.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}

// toggleStatus 启用/停用切换。
async function toggleStatus(row) {
  try {
    await updateDict(props.kind, row.id, { status: row.status === 1 ? 0 : 1 });
    row.status = row.status === 1 ? 0 : 1;
  } catch (e) {
    MessagePlugin.error(e.message || '状态切换失败');
  }
}

// move 成员上移/下移。
async function move(row, direction) {
  try {
    await moveMember(row.id, direction);
    reload();
  } catch (e) {
    MessagePlugin.error(e.message || '排序失败');
  }
}

// remove 删除（有引用 40901 → 提示改为停用）。
function remove(row) {
  const dlg = DialogPlugin.confirm({
    header: `删除${props.label}`,
    body: `确定删除「${row.name}」？`,
    confirmBtn: '删除',
    onConfirm: async () => {
      try {
        await deleteDict(props.kind, row.id);
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
.td-name {
  font-size: 13px;
  color: var(--tk-ink);
}
.td-flag {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  margin-right: 6px;
  border-radius: 4px;
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
}
.td-flag.on {
  background: var(--tk-blue-soft);
  border-color: transparent;
  color: var(--tk-brand);
  font-weight: 600;
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
.td-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: none;
  background: none;
  padding: 0;
  font-size: 12px;
  color: var(--tk-ph);
  cursor: pointer;
}
.td-status .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-dash);
}
.td-status.on {
  color: #0b8a37;
}
.td-status.on .dot {
  background: var(--tk-green);
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
  max-height: calc(100vh - 64px);
  overflow: auto;
}
.td-field {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 7px 0;
}
.td-field .field-label {
  width: 48px;
  flex-shrink: 0;
  padding-top: 7px;
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
.td-flags {
  flex: 1;
}
.td-modal-foot {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
