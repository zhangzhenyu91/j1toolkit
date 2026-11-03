<template>
  <div>
    <div class="td-dsec">
      <div class="dsec-h">
        <b>运维线路</b>
        <span class="cnt">{{
          loading ? '加载中…' : error ? '加载失败' : `${list.length} 条`
        }}</span>
        <span class="lt-btns">
          <button type="button" class="btn btn-sm" :disabled="!canLink" @click="openLink">
            关联线路<template v-if="selectedCount">（{{ selectedCount }}）</template>
          </button>
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
            <input
              type="checkbox"
              class="ol-ck"
              :checked="!!selected[it.id]"
              :aria-label="`选择 ${it.line_name || ''}`"
              @change="toggleSelect(it)"
            />
            <span class="olv">{{ fmtKv(it.voltage_level) }}</span>
            <span class="dn">
              {{ it.line_name || '—' }}
              <span v-if="aliasText(it)" class="ol-alias" :title="aliasText(it)">
                已关联：{{ aliasText(it) }}
              </span>
            </span>
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

    <!-- 编辑弹层（电压等级 + 线路名称；均不变直接关闭）；受控 visible 保退出动画 -->
    <Modal :visible="editOpen" title="编辑运维线路" @close="editOpen = false">
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

    <!-- 删除二次确认；受控 visible 保退出动画，文案取快照防收起时闪空 -->
    <Modal :visible="!!delItem" title="删除运维线路" @close="delItem = null">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        删除后不可恢复，确定删除「{{ delLabel }}」吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="delItem = null">取消</button>
        <button type="button" class="btn btn-sm btn-danger" :disabled="deleting" @click="remove">
          删除
        </button>
      </template>
    </Modal>

    <!-- 关联线路（合并为一条统一线路）；受控 visible 保退出动画 -->
    <Modal :visible="linkOpen" title="关联线路" @close="closeLink">
      <label class="f-lab">已选线路</label>
      <div class="ol-chips">
        <span v-for="it in linkRows" :key="it.id" class="ol-chip">
          {{ it.line_name || '—' }}
        </span>
      </div>
      <label class="f-lab">统一线路名称</label>
      <input
        v-model="linkName"
        class="f-sel"
        maxlength="64"
        autocomplete="off"
        :placeholder="linkPreviewing ? '正在推导…' : '留空则由系统自动推导'"
        @input="linkNameTouched = true"
        @keydown.enter="submitLink"
      />
      <label class="f-lab">电压等级</label>
      <div class="ol-ro">{{ linkVolt ? fmtKv(linkVolt) : '—' }}</div>
      <div class="ol-tip">
        关联后这些线路合并为一条，历史树障/交跨记录统一改名，导出与同步按一条线路处理
      </div>
      <div v-if="linkError" class="ol-link-err">{{ linkError }}</div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="closeLink">取消</button>
        <button
          type="button"
          class="btn btn-sm btn-primary"
          :disabled="linking"
          @click="submitLink"
        >
          关 联
        </button>
      </template>
    </Modal>

    <!-- 导入确认（全量替换）；受控 visible 保退出动画 -->
    <Modal :visible="impConfirm" title="导入运维线路" @close="impConfirm = false">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        导入将全量替换本班组运维线路，确定继续吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="impConfirm = false">取消</button>
        <button type="button" class="btn btn-sm btn-primary" @click="runImport">导入</button>
      </template>
    </Modal>

    <!-- 导入进行中（受控 visible；进行中不随关闭回调卸载，由 impBusy 落下收起） -->
    <Modal :visible="impBusy" title="导入运维线路" :width="280" @close="() => {}">
      <div class="m-loading"><span class="spin"></span>正在导入…</div>
    </Modal>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, reactive, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { createGeneration } from '../../core/async.js';
import {
  createOpline,
  deleteOpline,
  downloadOplineTemplate,
  importOplines,
  linkOplines,
  listOplines,
  moveOpline,
  previewOplineLink,
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
const delLabel = ref(''); // 删除确认文案快照（退出动画期间保持可渲染）
const deleting = ref(false);

// 关联线路：行选择（key=线路 id，value=true）+ 弹层状态
const selected = reactive({});
const linkOpen = ref(false);
const linkRows = ref([]); // 打开弹层时的已选行快照（chips 与提交共用）
const linkName = ref('');
const linkNameTouched = ref(false); // 用户改过统一名称后，预览响应不再覆盖
const linkVolt = ref('');
const linkError = ref('');
const linkPreviewing = ref(false);
const linking = ref(false);
// previewSeq 预览请求代际：关闭/重开弹层后丢弃旧响应，避免覆盖手填名称。
let previewSeq = 0;

const selectedRows = computed(() => list.value.filter((it) => selected[it.id]));
const selectedCount = computed(() => selectedRows.value.length);
const canLink = computed(() => selectedCount.value >= 2);

// pruneSelection 列表刷新后剔除已不存在的选择（关联 / 删除 / 导入后原线路可能已消失）。
function pruneSelection() {
  const ids = new Set(list.value.map((it) => it.id));
  Object.keys(selected).forEach((key) => {
    if (!ids.has(Number(key))) delete selected[key];
  });
}

// gateOnForbidden 40304 → 整页门控。
function gateOnForbidden(e) {
  if (e && e.code === 40304) {
    emit('forbidden');
    return true;
  }
  return false;
}

// reload 列表加载；请求代际防止连续操作时旧列表覆盖新状态。
const reloadGen = createGeneration();
async function reload() {
  const gen = reloadGen.next();
  loading.value = true;
  error.value = '';
  try {
    const data = await listOplines();
    if (!reloadGen.isCurrent(gen)) return;
    list.value = data.list || [];
    pruneSelection();
  } catch (e) {
    if (!reloadGen.isCurrent(gen)) return;
    if (gateOnForbidden(e)) return;
    error.value = e.message || '加载失败';
    list.value = [];
    pruneSelection();
  } finally {
    if (reloadGen.isCurrent(gen)) loading.value = false;
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
  delLabel.value = `${fmtKv(row.voltage_level)} ${row.line_name || ''}`;
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

// toggleSelect 勾选 / 取消勾选一行（reactive 对象增删自动触发渲染）。
function toggleSelect(row) {
  if (selected[row.id]) delete selected[row.id];
  else selected[row.id] = true;
}

// aliasText 已并入本线路的原线路名（「A、B」）；无别名返回空串，不占位。
function aliasText(row) {
  const aliases = Array.isArray(row.aliases) ? row.aliases.filter(Boolean) : [];
  return aliases.join('、');
}

// commonVolt 已选线路的共同电压等级（不一致返回空串，preview 成功后再以接口值为准）。
function commonVolt(rows) {
  const set = new Set(rows.map((it) => it.voltage_level || '').filter(Boolean));
  return set.size === 1 ? [...set][0] : '';
}

// openLink 打开关联弹层：先展示快照与本地电压，再用 preview（无副作用）自动填统一名称。
function openLink() {
  if (!canLink.value) return;
  linkRows.value = selectedRows.value.slice();
  linkName.value = '';
  linkNameTouched.value = false;
  linkVolt.value = commonVolt(linkRows.value);
  linkError.value = '';
  linkOpen.value = true;
  runPreview(linkRows.value.map((it) => it.id));
}

// runPreview 预览自动推导的统一名称；失败仅提示，名称仍可手填后提交。
async function runPreview(ids) {
  const seq = ++previewSeq;
  linkPreviewing.value = true;
  linkError.value = '';
  try {
    const data = await previewOplineLink(ids);
    if (seq !== previewSeq) return;
    if (data && data.line_name && !linkNameTouched.value) linkName.value = data.line_name;
    if (data && data.voltage_level) linkVolt.value = data.voltage_level;
  } catch (e) {
    if (seq !== previewSeq) return;
    if (gateOnForbidden(e)) {
      linkOpen.value = false;
      return;
    }
    linkError.value = e.message || '统一名称推导失败，可手动填写';
  } finally {
    if (seq === previewSeq) linkPreviewing.value = false;
  }
}

// closeLink 关闭弹层并作废在途预览（避免旧响应回填已关闭的弹层）。
function closeLink() {
  previewSeq += 1;
  linkPreviewing.value = false;
  linkOpen.value = false;
}

// submitLink 提交关联：line_name 留空由后端推导；成功后清空选择并刷新（原线路已被合并删除）。
async function submitLink() {
  if (linking.value) return;
  const ids = linkRows.value.map((it) => it.id);
  if (ids.length < 2) {
    linkError.value = '请至少选择两条线路';
    return;
  }
  linking.value = true;
  linkError.value = '';
  try {
    const data = await linkOplines(ids, linkName.value);
    const records = data && data.records_updated;
    const name = (data && data.line_name) || linkName.value.trim() || '统一线路';
    MessagePlugin.success(`已关联为「${name}」${records ? `，更新 ${records} 条记录` : ''}`);
    closeLink();
    Object.keys(selected).forEach((key) => delete selected[key]);
    await reload();
  } catch (e) {
    if (gateOnForbidden(e)) {
      closeLink();
      return;
    }
    linkError.value = e.message || '关联失败';
  } finally {
    linking.value = false;
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
