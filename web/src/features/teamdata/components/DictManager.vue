<template>
  <div>
    <div class="td-dsec">
      <div class="dsec-h">
        <b>{{ label }}</b>
        <span class="cnt">{{
          loading ? '加载中…' : error ? '加载失败' : `${list.length} 条`
        }}</span>
      </div>

      <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
      <template v-else>
        <div v-if="error" class="dempty">
          {{ error }}　<a class="linkbtn" @click="reload">重试</a>
        </div>
        <template v-else>
          <div v-if="!list.length" class="dempty">暂无数据，可在下方添加</div>
          <div
            v-for="(it, idx) in list"
            :key="it.id"
            class="drow"
            :class="{ off: it.status !== 1 }"
          >
            <span class="dn">{{ it.name }}</span>
            <!-- 目的地费用口径标（互斥）：市外=伙食100/交通0；驻地=伙食40/交通0；默认市内 60/0 -->
            <span v-if="kind === 'destinations' && it.out_of_city === 1" class="dpill dpill-out">
              市外
            </span>
            <span v-if="kind === 'destinations' && it.stationed === 1" class="dpill dpill-sta">
              驻地
            </span>
            <!-- 人员来源标：user_id 非空=账号同步（绿），空=手动添加（灰） -->
            <span
              v-if="kind === 'members'"
              class="dpill"
              :class="it.user_id ? 'dpill-sync' : 'dpill-hand'"
            >
              {{ it.user_id ? '账号同步' : '手动添加' }}
            </span>
            <span v-if="it.status !== 1" class="doff-tip">已停用</span>
            <span class="dops">
              <button
                v-if="kind === 'members' && idx > 0"
                type="button"
                class="linkbtn"
                @click="move(it, 'up')"
              >
                上移
              </button>
              <button
                v-if="kind === 'members' && idx < list.length - 1"
                type="button"
                class="linkbtn"
                @click="move(it, 'down')"
              >
                下移
              </button>
              <button type="button" class="linkbtn" @click="openEdit(it)">改名</button>
              <button type="button" class="linkbtn" @click="toggleStatus(it)">
                {{ it.status === 1 ? '停用' : '启用' }}
              </button>
              <button type="button" class="linkbtn dgr" @click="confirmDel(it)">删除</button>
            </span>
          </div>
        </template>
      </template>

      <!-- 板块底部虚线添加行（输入名称回车 / 点按钮提交，sort 服务端自动追加） -->
      <div class="dadd">
        <input
          v-model="newName"
          class="dadd-in"
          :placeholder="ph"
          maxlength="64"
          autocomplete="off"
          @keydown.enter.prevent="add"
        />
        <button type="button" class="btn btn-sm btn-primary" :disabled="adding" @click="add">
          添加
        </button>
      </div>
    </div>

    <!-- 编辑弹层：改名；目的地段另有 市外 / 驻地 互斥勾选（费用口径标）；受控 visible 保退出动画 -->
    <Modal :visible="editOpen" :title="`编辑${label}`" @close="editOpen = false">
      <input
        ref="nameInput"
        v-model="editForm.name"
        class="f-sel"
        maxlength="64"
        autocomplete="off"
        @keydown.enter="save"
      />
      <template v-if="kind === 'destinations'">
        <label class="f-lab">费用口径标记（互斥，默认市内）</label>
        <label class="dck">
          <input
            type="checkbox"
            :checked="editForm.out_of_city === 1"
            @change="setEditFlag('out_of_city')"
          />
          市外（伙食 100 / 交通 0）
        </label>
        <label class="dck">
          <input
            type="checkbox"
            :checked="editForm.stationed === 1"
            @change="setEditFlag('stationed')"
          />
          驻地（伙食 40 / 交通 0）
        </label>
      </template>
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

    <!-- 删除二次确认（被引用时后端拒绝则 toast 后端文案）；受控 visible 保退出动画，名称取快照防收起时闪空 -->
    <Modal :visible="!!delItem" :title="`删除${label}`" @close="delItem = null">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        删除后不可恢复，确定删除「{{ delName }}」吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="delItem = null">取消</button>
        <button type="button" class="btn btn-sm btn-danger" :disabled="deleting" @click="remove">
          删除
        </button>
      </template>
    </Modal>
  </div>
</template>

<script setup>
import { nextTick, onMounted, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { createDict, deleteDict, listDict, moveMember, updateDict } from '../api.js';
import Modal from './Modal.vue';

// 班组字典行卡（车牌号 / 目的地 / 人员，对照旧版 teamdata.html 字典段）：
// 行内 改名 / 停启用 / 删除，人员另有 上移 / 下移；底部虚线行即添加；目的地编辑弹层带互斥费用口径。
// kind=vehicles|destinations|members，走 /api/v1/teamdata/{kind}；40304（仅管理员）→ 通知整页门控。
const props = defineProps({
  kind: { type: String, required: true },
  label: { type: String, required: true },
  ph: { type: String, required: true },
});
const emit = defineEmits(['forbidden']);

const list = ref([]);
const loading = ref(false);
const error = ref('');
const newName = ref('');
const adding = ref(false);
const editOpen = ref(false);
const editing = ref(null);
const saving = ref(false);
const editError = ref('');
const editForm = reactive({ name: '', out_of_city: 0, stationed: 0 });
const nameInput = ref(null);
const delItem = ref(null);
const delName = ref(''); // 删除确认文案快照（退出动画期间保持可渲染）
const deleting = ref(false);

// gateOnForbidden 40304（仅管理员可执行）→ 通知视图切整页门控提示。
function gateOnForbidden(e) {
  if (e && e.code === 40304) {
    emit('forbidden');
    return true;
  }
  return false;
}

// reload 列表加载（失败如实展示 + 重试）。
async function reload() {
  loading.value = true;
  error.value = '';
  try {
    const data = await listDict(props.kind);
    list.value = data.list || [];
  } catch (e) {
    if (gateOnForbidden(e)) return;
    error.value = e.message || '加载失败';
    list.value = [];
  } finally {
    loading.value = false;
  }
}

// add 虚线行添加（sort 服务端自动追加）。
async function add() {
  const name = newName.value.trim();
  if (!name) {
    MessagePlugin.info(`请${props.ph}`);
    return;
  }
  adding.value = true;
  try {
    await createDict(props.kind, { name });
    newName.value = '';
    MessagePlugin.success('已添加');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '添加失败');
  } finally {
    adding.value = false;
  }
}

// openEdit 改名弹层（目的地段带费用口径勾选）。
async function openEdit(row) {
  editing.value = row;
  editForm.name = row.name || '';
  editForm.out_of_city = row.out_of_city === 1 ? 1 : 0;
  editForm.stationed = row.stationed === 1 ? 1 : 0;
  editError.value = '';
  editOpen.value = true;
  await nextTick();
  if (nameInput.value) {
    nameInput.value.focus();
    nameInput.value.select();
  }
}

// setEditFlag 市外 / 驻地互斥勾选（勾一个即清另一个；后端同口径兜底）。
function setEditFlag(key) {
  const next = editForm[key] === 1 ? 0 : 1;
  editForm[key] = next;
  if (next === 1) editForm[key === 'out_of_city' ? 'stationed' : 'out_of_city'] = 0;
}

// save 改名（名称与标记均不变直接关闭；目的地双字段恒带，取消勾选也要落库）。
async function save() {
  const name = editForm.name.trim();
  if (!name) {
    editError.value = '名称不能为空';
    return;
  }
  const isDest = props.kind === 'destinations';
  const ooc = isDest ? editForm.out_of_city : 0;
  const sta = isDest ? editForm.stationed : 0;
  const item = editing.value || {};
  if (
    name === item.name &&
    (!isDest || (ooc === (item.out_of_city || 0) && sta === (item.stationed || 0)))
  ) {
    editOpen.value = false;
    return;
  }
  saving.value = true;
  editError.value = '';
  try {
    const payload = { name };
    if (isDest) {
      payload.out_of_city = ooc;
      payload.stationed = sta;
    }
    await updateDict(props.kind, item.id, payload);
    editOpen.value = false;
    MessagePlugin.success('已保存');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) editError.value = e.message || '保存失败';
  } finally {
    saving.value = false;
  }
}

// toggleStatus 停用 / 启用切换（停用行灰显 + 「已停用」标记）。
async function toggleStatus(row) {
  try {
    await updateDict(props.kind, row.id, { status: row.status === 1 ? 0 : 1 });
    MessagePlugin.success(row.status === 1 ? '已停用' : '已启用');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '状态切换失败');
  }
}

// move 人员上移 / 下移（契约字段 direction:'up'|'down'）。
async function move(row, direction) {
  try {
    await moveMember(row.id, direction);
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '排序失败');
  }
}

function confirmDel(row) {
  delItem.value = row;
  delName.value = row.name || '';
}

// remove 删除（有引用 40901 → toast 后端文案提示改为停用）。
async function remove() {
  const row = delItem.value;
  if (!row) return;
  deleting.value = true;
  try {
    await deleteDict(props.kind, row.id);
    delItem.value = null;
    MessagePlugin.success('已删除');
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '删除失败');
  } finally {
    deleting.value = false;
  }
}

onMounted(reload);
</script>
