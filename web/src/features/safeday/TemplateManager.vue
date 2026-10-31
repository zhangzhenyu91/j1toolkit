<template>
  <!-- 记录模板管理抽屉（仅超管挂载；服务端 /api/v1/admin/safeday 二次校验超管）。
       列表态：模板卡片（内置/自定义、默认、Key 配置状态、章节摘要）+ 设为默认 / 编辑 /
       替换 docx / 删除 / 下载样例；编辑态：名称、章节 key+title 增删与上下移、API Key
       密码输入（新建必填、编辑留空保留、只显示是否已配置）、上传/替换 docx。 -->
  <Drawer :visible="visible" title="记录模板管理" :width="640" @update:visible="onVisible">
    <template v-if="view === 'list'">
      <div class="tpl-hint">
        <p>
          模板决定生成记录的章节结构与 docx
          排版，生成时可在「记录模板」中选择；未选择时使用默认模板。
        </p>
        <div class="tpl-tools">
          <button
            type="button"
            class="tpl-btn"
            :disabled="sampleBusy || !!busyId"
            @click="downloadSample"
          >
            {{ sampleBusy ? '下载中…' : '下载样例' }}
          </button>
          <button type="button" class="tpl-btn" :disabled="!!busyId" @click="openCreate">
            新建模板
          </button>
        </div>
        <p v-if="createLimitError" class="tpl-note">{{ createLimitError }}</p>
      </div>

      <div v-if="demoMode" class="tpl-note">
        演示模式：模板为虚构样例，新建、保存、上传、设默认与删除均会被拒绝。
      </div>

      <div v-if="loading" class="tpl-state">
        <span class="tpl-spin"></span><span>正在加载模板…</span>
      </div>
      <div v-else-if="error" class="tpl-state tpl-state-err">
        <span>{{ error }}</span>
        <button type="button" class="tpl-link" @click="load">重试</button>
      </div>
      <div v-else-if="!templates.length" class="tpl-state">暂无模板，可新建自定义模板</div>
      <div v-else class="tpl-list">
        <div
          v-for="t in templates"
          :key="t.id"
          class="tpl-card"
          :class="{ 'is-default': t.isDefault }"
        >
          <div class="tpl-card-h">
            <b class="tpl-name">{{ t.name }}</b>
            <span class="tpl-tag" :class="t.mode === 'static' ? '' : 'tpl-tag-blue'">
              {{ t.mode === 'static' ? '内置' : '自定义' }}
            </span>
            <span v-if="t.isDefault" class="tpl-tag tpl-tag-green">默认</span>
            <span
              v-if="t.mode !== 'static'"
              class="tpl-tag"
              :class="t.hasApiKey ? 'tpl-tag-soft' : 'tpl-tag-warn'"
            >
              {{ t.hasApiKey ? 'Key 已配置' : 'Key 未配置' }}
            </span>
          </div>
          <div class="tpl-meta num">
            {{ (t.sections || []).length }} 个章节
            <template v-if="t.size"> · {{ fmtSize(t.size) }}</template>
            <template v-if="t.updatedAt"> · 更新于 {{ fmtTime(t.updatedAt) }}</template>
          </div>
          <div class="tpl-secs">{{ sectionLine(t) }}</div>
          <div class="tpl-ops">
            <button
              v-if="!t.isDefault"
              type="button"
              class="tpl-btn tpl-btn-sm"
              :disabled="!!busyId"
              @click="setDefault(t)"
            >
              设为默认
            </button>
            <template v-if="t.mode !== 'static'">
              <button
                type="button"
                class="tpl-btn tpl-btn-sm"
                :disabled="!!busyId"
                @click="openEdit(t)"
              >
                编辑
              </button>
              <button
                type="button"
                class="tpl-btn tpl-btn-sm"
                :disabled="!!busyId"
                @click="pickDocx(t)"
              >
                替换 docx
              </button>
              <button
                type="button"
                class="tpl-btn tpl-btn-sm tpl-btn-danger"
                :disabled="!!busyId"
                @click="askDelete(t)"
              >
                删除
              </button>
            </template>
            <span v-else class="tpl-lock">内置模板只读，仅可设为默认</span>
          </div>

          <div v-if="delTarget && delTarget.id === t.id" class="tpl-del">
            <span>删除后不可恢复；已生成记录仍按各自模板快照渲染。</span>
            <div class="tpl-del-ops">
              <button
                type="button"
                class="tpl-btn tpl-btn-sm"
                :disabled="!!busyId"
                @click="delTarget = null"
              >
                取消
              </button>
              <button
                type="button"
                class="tpl-btn tpl-btn-sm tpl-btn-danger"
                :disabled="!!busyId"
                @click="confirmDelete"
              >
                {{ busyId === t.id ? '删除中…' : '确认删除' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- 替换 docx 的隐藏文件选择器（列表行「替换 docx」共用） -->
      <input ref="docxInput" type="file" accept=".docx" hidden @change="onReplacePicked" />
    </template>

    <template v-else>
      <button type="button" class="tpl-back" @click="backToList">← 返回模板列表</button>
      <h4 class="tpl-edit-title">{{ editId ? '编辑模板' : '新建模板' }}</h4>

      <label class="tpl-lab" for="tplName">模板名称</label>
      <input
        id="tplName"
        v-model="form.name"
        class="tpl-input"
        type="text"
        maxlength="64"
        autocomplete="off"
        placeholder="如：检修一班安全日记录模板"
      />

      <div class="tpl-key-h">
        <label class="tpl-lab" for="tplKey">Dify API Key</label>
        <span class="tpl-tag" :class="editHasKey ? 'tpl-tag-soft' : 'tpl-tag-warn'">
          {{ editHasKey ? '已配置' : '未配置' }}
        </span>
      </div>
      <input
        id="tplKey"
        v-model="form.apiKey"
        class="tpl-input"
        type="password"
        autocomplete="new-password"
        :placeholder="apiKeyPlaceholder"
      />
      <p class="tpl-tip">
        仅保存加密密文，页面不回显；{{
          editId ? '留空表示保持原 Key 不变' : '新建模板必须填写，用于该模板的 Dify 工作流'
        }}。
      </p>

      <div class="tpl-sec-h">
        <label class="tpl-lab"
          >章节（1–{{ MAX_SECTIONS }} 段；key 对应 Dify 输出字段，顺序即文档章节顺序）</label
        >
        <button type="button" class="tpl-btn tpl-btn-sm" @click="addRow">添加章节</button>
      </div>
      <div class="tpl-rows">
        <div v-for="(row, i) in form.rows" :key="row.uid" class="tpl-row">
          <input
            v-model="row.key"
            class="tpl-input tpl-key-in num"
            type="text"
            maxlength="32"
            autocomplete="off"
            placeholder="key"
            :aria-label="`第 ${i + 1} 个章节标识`"
          />
          <input
            v-model="row.title"
            class="tpl-input"
            type="text"
            maxlength="64"
            autocomplete="off"
            placeholder="章节标题"
            :aria-label="`第 ${i + 1} 个章节标题`"
          />
          <div class="tpl-row-ops">
            <button
              type="button"
              class="tpl-move"
              :disabled="i === 0"
              :aria-label="`上移第 ${i + 1} 个章节`"
              title="上移"
              @click="moveRow(i, -1)"
            >
              ↑
            </button>
            <button
              type="button"
              class="tpl-move"
              :disabled="i === form.rows.length - 1"
              :aria-label="`下移第 ${i + 1} 个章节`"
              title="下移"
              @click="moveRow(i, 1)"
            >
              ↓
            </button>
            <button
              type="button"
              class="tpl-move tpl-move-del"
              :aria-label="`删除第 ${i + 1} 个章节`"
              title="删除章节"
              @click="removeRow(i)"
            >
              ×
            </button>
          </div>
        </div>
        <div v-if="!form.rows.length" class="tpl-state">尚未添加章节</div>
      </div>

      <label class="tpl-lab">模板 docx</label>
      <div class="tpl-file">
        <button type="button" class="tpl-btn tpl-btn-sm" @click="pickFormDocx">
          {{ form.docxFile ? '重新选择' : editId ? '上传/替换 docx' : '选择 docx' }}
        </button>
        <span class="tpl-file-nm">
          {{
            form.docxFile
              ? form.docxFile.name
              : editId
                ? editHasDocx
                  ? '已上传，留空保持不变'
                  : '未上传'
                : '未选择'
          }}
        </span>
      </div>
      <p class="tpl-tip">
        docx 须包含独占一行的 {@章节区} 占位符，生成时按上方章节顺序注入正文；文件 ≤{{
          MAX_DOCX_MB
        }}MB。
      </p>
      <input ref="fileInput" type="file" accept=".docx" hidden @change="onFormFilePicked" />

      <p v-if="formError" class="tpl-err">{{ formError }}</p>

      <div class="tpl-actions">
        <button type="button" class="tpl-btn" :disabled="saving" @click="backToList">取消</button>
        <button type="button" class="tpl-btn tpl-btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </div>
    </template>
  </Drawer>
</template>

<script setup>
// 记录模板管理（安全日）：列表 + 新建/编辑 + docx 上传/替换 + 设默认 + 删除 + 下载样例。
// 校验/排序/默认解析走 ./templates.js 纯逻辑（与后端 templates.go 同口径）；
// 写操作失败如实抛错（演示模式由 api 适配层拒绝），不本地伪造结果。
import { computed, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import Drawer from '../../components/Drawer.vue';
import {
  adminTemplates,
  createTemplate,
  downloadTemplateSample,
  isDemo,
  removeTemplate,
  replaceTemplateDocx,
  saveBlob,
  setDefaultTemplate,
  updateTemplate,
} from './api.js';
import {
  BUILTIN_SECTIONS,
  MAX_DOCX_MB,
  MAX_SECTIONS,
  addSectionRow,
  moveSection,
  removeSectionRow,
  templateCreateLimitError,
  templatePayload,
  validateDocxFile,
  validateTemplateForm,
} from './templates.js';

const props = defineProps({
  visible: { type: Boolean, default: false },
});
const emit = defineEmits(['update:visible', 'changed']);

const demoMode = isDemo();

const view = ref('list');
const templates = ref([]);
const loading = ref(false);
const error = ref('');
const busyId = ref('');
const sampleBusy = ref(false);

const editId = ref('');
const editHasKey = ref(false);
const editHasDocx = ref(false);
const saving = ref(false);
const formError = ref('');
const form = reactive({ name: '', apiKey: '', rows: [], docxFile: null });

const docxInput = ref(null);
const fileInput = ref(null);
const replaceTarget = ref(null);
const delTarget = ref(null);

// 抽屉会话序号：保存中关闭/重开抽屉后，旧会话的迟到结果一律丢弃，
// 不向新会话写提示、错误或表单状态（保存请求本身仍由服务端完成）。
let drawerSession = 0;

// 自定义模板 50 套上限：列表态提示并拦截新建（后端 Create 仍为最终裁判）。
const createLimitError = computed(() => templateCreateLimitError(templates.value));

const apiKeyPlaceholder = computed(() => {
  if (!editId.value) return '新建模板必填';
  return editHasKey.value ? '已配置，留空保持不变' : '未配置，请输入';
});

// 章节行 uid：仅用于 v-for 稳定 key（排序/删除时输入框不串行）。
let uidSeed = 0;
function nextUid() {
  uidSeed += 1;
  return `sec-${uidSeed}`;
}
function rowsOf(sections) {
  return (sections || []).map((s) => ({ uid: nextUid(), key: s.key, title: s.title }));
}

function onVisible(v) {
  emit('update:visible', v);
}

// ---- 列表 ----
async function load() {
  loading.value = true;
  error.value = '';
  try {
    const data = await adminTemplates();
    templates.value = data.templates;
  } catch (e) {
    error.value = e.message || '模板列表加载失败';
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.visible,
  (v) => {
    drawerSession += 1;
    if (!v) return;
    // 重开抽屉即进入新会话：作废旧会话的保存锁，清掉上次残留错误
    saving.value = false;
    formError.value = '';
    view.value = 'list';
    delTarget.value = null;
    load();
  }
);

function sectionLine(t) {
  const titles = (t.sections || []).map((s) => s.title).filter(Boolean);
  return titles.length ? titles.join(' · ') : '（未配置章节）';
}

function fmtSize(n) {
  const size = Number(n || 0);
  if (size >= 1048576) return (size / 1048576).toFixed(1) + ' MB';
  if (size >= 1024) return Math.round(size / 1024) + ' KB';
  return size + ' B';
}

function fmtTime(v) {
  const dt = new Date(v);
  if (Number.isNaN(dt.getTime())) return String(v);
  const p = (x) => String(x).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}

// ---- 行内操作 ----
async function setDefault(t) {
  if (busyId.value) return;
  busyId.value = t.id;
  try {
    await setDefaultTemplate(t.id);
    MessagePlugin.success(`已将「${t.name}」设为默认模板`);
    emit('changed');
    await load();
  } catch (e) {
    MessagePlugin.error(e.message || '设置默认模板失败');
  } finally {
    busyId.value = '';
  }
}

function pickDocx(t) {
  if (busyId.value) return;
  replaceTarget.value = t;
  docxInput.value?.click();
}

async function onReplacePicked(e) {
  const file = e.target.files && e.target.files[0];
  e.target.value = '';
  const target = replaceTarget.value;
  replaceTarget.value = null;
  if (!file || !target || busyId.value) return;
  const fileErr = validateDocxFile(file);
  if (fileErr) {
    MessagePlugin.error(fileErr);
    return;
  }
  busyId.value = target.id;
  try {
    const fd = new FormData();
    fd.append('file', file, file.name);
    await replaceTemplateDocx(target.id, fd);
    MessagePlugin.success(`「${target.name}」docx 已替换`);
    emit('changed');
    await load();
  } catch (err) {
    MessagePlugin.error(err.message || '替换 docx 失败');
  } finally {
    busyId.value = '';
  }
}

function askDelete(t) {
  if (busyId.value) return;
  delTarget.value = t;
}

async function confirmDelete() {
  const target = delTarget.value;
  if (!target || busyId.value) return;
  busyId.value = target.id;
  try {
    await removeTemplate(target.id);
    delTarget.value = null;
    MessagePlugin.success('模板已删除');
    emit('changed');
    await load();
  } catch (e) {
    MessagePlugin.error(e.message || '删除模板失败');
  } finally {
    busyId.value = '';
  }
}

// ---- 下载样例 ----
async function downloadSample() {
  if (sampleBusy.value || busyId.value) return;
  sampleBusy.value = true;
  try {
    const res = await downloadTemplateSample();
    await saveBlob(res, '安全日动态模板样例.docx');
    MessagePlugin.success('样例 docx 已下载');
  } catch (e) {
    MessagePlugin.error(e.message || '样例下载失败');
  } finally {
    sampleBusy.value = false;
  }
}

// ---- 新建 / 编辑 ----
function openCreate() {
  if (busyId.value) return;
  const limitErr = templateCreateLimitError(templates.value);
  if (limitErr) {
    MessagePlugin.warning(limitErr);
    return;
  }
  editId.value = '';
  editHasKey.value = false;
  editHasDocx.value = false;
  form.name = '';
  form.apiKey = '';
  form.docxFile = null;
  form.rows = rowsOf(BUILTIN_SECTIONS);
  formError.value = '';
  delTarget.value = null;
  view.value = 'edit';
}

function openEdit(t) {
  if (busyId.value) return;
  editId.value = t.id;
  editHasKey.value = !!t.hasApiKey;
  editHasDocx.value = !!t.sha256;
  form.name = t.name || '';
  form.apiKey = '';
  form.docxFile = null;
  form.rows = rowsOf(t.sections);
  formError.value = '';
  delTarget.value = null;
  view.value = 'edit';
}

function backToList() {
  if (saving.value) return;
  view.value = 'list';
  formError.value = '';
  delTarget.value = null;
}

function addRow() {
  const rows = addSectionRow(form.rows);
  rows[rows.length - 1] = { ...rows[rows.length - 1], uid: nextUid() };
  form.rows = rows;
}

function moveRow(index, delta) {
  form.rows = moveSection(form.rows, index, delta);
}

function removeRow(index) {
  form.rows = removeSectionRow(form.rows, index);
}

function pickFormDocx() {
  fileInput.value?.click();
}

function onFormFilePicked(e) {
  const file = e.target.files && e.target.files[0];
  e.target.value = '';
  if (!file) return;
  const fileErr = validateDocxFile(file);
  if (fileErr) {
    MessagePlugin.error(fileErr);
    return;
  }
  form.docxFile = file;
}

async function save() {
  if (saving.value) return;
  const session = drawerSession;
  const isNew = !editId.value;
  const checkErr = validateTemplateForm({
    name: form.name,
    rows: form.rows,
    apiKey: form.apiKey,
    isNew,
    docx: form.docxFile,
  });
  if (checkErr) {
    formError.value = checkErr;
    return;
  }
  formError.value = '';
  saving.value = true;
  try {
    const sections = templatePayload(form.rows);
    if (isNew) {
      const fd = new FormData();
      fd.append('name', form.name.trim());
      fd.append('sections', JSON.stringify(sections));
      fd.append('apiKey', form.apiKey.trim());
      fd.append('file', form.docxFile, form.docxFile.name);
      await createTemplate(fd);
      // 保存中抽屉被关闭/重开：丢弃旧会话的迟到结果，不写提示或表单状态
      if (session !== drawerSession) return;
      MessagePlugin.success('模板已创建');
    } else {
      const payload = { name: form.name.trim(), sections };
      const key = form.apiKey.trim();
      if (key) payload.apiKey = key; // 留空不提交，服务端保持原 Key
      await updateTemplate(editId.value, payload);
      if (session !== drawerSession) return;
      // 元数据已保存成功：即使下一步 docx 替换失败，新 Key 也已落库，状态须同步为已配置
      if (key) editHasKey.value = true;
      if (form.docxFile) {
        const fd = new FormData();
        fd.append('file', form.docxFile, form.docxFile.name);
        try {
          await replaceTemplateDocx(editId.value, fd);
          if (session !== drawerSession) return;
        } catch (e) {
          if (session !== drawerSession) return;
          // 元数据已保存：如实告知 docx 失败并停留在编辑态，便于重试替换
          emit('changed');
          await load();
          if (session !== drawerSession) return;
          formError.value = `模板信息已保存，但 docx 替换失败：${e.message || '未知错误'}`;
          return;
        }
      }
      if (session !== drawerSession) return;
      MessagePlugin.success('模板已保存');
    }
    if (session !== drawerSession) return;
    view.value = 'list';
    emit('changed');
    await load();
  } catch (e) {
    if (session === drawerSession) formError.value = e.message || '保存失败';
  } finally {
    if (session === drawerSession) saving.value = false;
  }
}
</script>

<style scoped>
/* 抽屉内容宽度：Drawer 自带 18px 内边距；章节行在窄面板内仍保持输入可用 */
.tpl-hint {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
}
.tpl-hint p {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--tk-sub);
}
.tpl-tools {
  display: flex;
  gap: 8px;
  flex: none;
}
.tpl-note {
  margin-bottom: 12px;
  padding: 9px 12px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  font-size: 12px;
  line-height: 1.6;
  color: var(--tk-sub);
}

/* 按钮：页面唯一主按钮仍在上传卡（生成记录），抽屉内仅保存为主按钮 */
.tpl-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 36px;
  padding: 0 14px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card);
  color: var(--tk-ink);
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  cursor: pointer;
  white-space: nowrap;
  transition:
    border-color 0.16s var(--tk-ease),
    color 0.16s,
    background 0.16s;
}
.tpl-btn:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.tpl-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.tpl-btn-sm {
  height: 30px;
  padding: 0 10px;
  font-size: 12px;
}
.tpl-btn-primary {
  height: 40px;
  padding: 0 22px;
  background: var(--tk-brand);
  border-color: transparent;
  color: var(--tk-on-brand);
  letter-spacing: 2px;
}
.tpl-btn-primary:hover:not(:disabled) {
  color: var(--tk-on-brand);
  filter: brightness(0.92);
}
.tpl-btn-danger {
  color: var(--tk-red);
  border-color: var(--tk-danger-line);
}
.tpl-btn-danger:hover:not(:disabled) {
  border-color: var(--tk-red);
  color: var(--tk-red);
  background: var(--tk-red-soft);
}

.tpl-link {
  border: none;
  background: none;
  color: var(--tk-brand);
  font-size: 12.5px;
  cursor: pointer;
  padding: 0;
}

/* 列表 */
.tpl-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.tpl-card {
  border: 1px solid var(--tk-line);
  border-radius: 12px;
  padding: 12px 14px;
  background: var(--tk-card);
}
.tpl-card.is-default {
  border-color: var(--tk-brand);
  box-shadow: inset 3px 0 0 var(--tk-brand);
}
.tpl-card-h {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.tpl-name {
  font-size: 13.5px;
  font-weight: 700;
  word-break: break-all;
}
.tpl-tag {
  display: inline-flex;
  align-items: center;
  border-radius: 4px;
  padding: 1px 7px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  background: var(--tk-fill);
  color: var(--tk-sub);
  white-space: nowrap;
}
.tpl-tag-blue {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.tpl-tag-green {
  background: var(--tk-green-soft);
  color: var(--tk-ok-text);
}
.tpl-tag-warn {
  background: var(--tk-yellow-soft);
  color: var(--tk-warn-text);
}
.tpl-tag-soft {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.tpl-meta {
  margin-top: 5px;
  font-size: 11px;
  color: var(--tk-ph);
}
.tpl-secs {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.65;
  color: var(--tk-sub);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.tpl-ops {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 10px;
}
.tpl-lock {
  font-size: 11.5px;
  color: var(--tk-ph);
}
.tpl-del {
  margin-top: 10px;
  padding: 9px 11px;
  border-left: 3px solid var(--tk-red);
  background: var(--tk-red-soft);
  font-size: 12px;
  line-height: 1.6;
  color: var(--tk-sub);
}
.tpl-del-ops {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

/* 状态 */
.tpl-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 9px;
  padding: 30px 8px;
  font-size: 12.5px;
  color: var(--tk-ph);
}
.tpl-state-err {
  color: var(--tk-red);
}
.tpl-spin {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: tpl-rot 1s linear infinite;
}
@keyframes tpl-rot {
  to {
    transform: rotate(360deg);
  }
}

/* 编辑态 */
.tpl-back {
  border: none;
  background: none;
  padding: 0;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--tk-brand);
  cursor: pointer;
}
.tpl-edit-title {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.tpl-lab {
  display: block;
  margin: 14px 0 7px;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-sub);
}
.tpl-key-h {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tpl-key-h .tpl-lab {
  margin-bottom: 7px;
}
.tpl-input {
  width: 100%;
  height: 40px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card);
  padding: 0 12px;
  font-size: 13.5px;
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.16s var(--tk-ease),
    box-shadow 0.16s var(--tk-ease);
}
.tpl-input::placeholder {
  color: var(--tk-ph);
}
.tpl-input:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--tk-brand-glow);
}
.tpl-tip {
  margin: 6px 0 0;
  font-size: 11.5px;
  line-height: 1.65;
  color: var(--tk-ph);
}
.tpl-sec-h {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 10px;
  margin-top: 16px;
}
.tpl-sec-h .tpl-lab {
  margin: 0 0 7px;
}
.tpl-rows {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.tpl-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tpl-key-in {
  width: 140px;
  flex: none;
}
.tpl-row .tpl-input:not(.tpl-key-in) {
  flex: 1;
  min-width: 0;
}
.tpl-row-ops {
  display: flex;
  gap: 4px;
  flex: none;
}
.tpl-move {
  width: 28px;
  height: 28px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-card);
  color: var(--tk-sub);
  font-size: 13px;
  line-height: 1;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    border-color 0.16s,
    color 0.16s,
    background 0.16s;
}
.tpl-move:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.tpl-move:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
.tpl-move-del:hover:not(:disabled) {
  border-color: var(--tk-red);
  color: var(--tk-red);
  background: var(--tk-red-soft);
}
.tpl-file {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 11px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-fill);
}
.tpl-file-nm {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--tk-sub);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tpl-err {
  margin: 12px 0 0;
  padding: 8px 11px;
  border-left: 3px solid var(--tk-red);
  background: var(--tk-red-soft);
  font-size: 12px;
  line-height: 1.6;
  color: var(--tk-red);
}
.tpl-actions {
  margin-top: 18px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

@media (prefers-reduced-motion: reduce) {
  .tpl-spin {
    animation: none;
  }
}
</style>
