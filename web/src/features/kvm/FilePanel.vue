<template>
  <t-dialog
    v-model:visible="visible"
    :header="`文件互推 · ${device?.description || device?.ddns || '设备'}`"
    width="720px"
    :close-on-overlay-click="false"
    :footer="false"
    @close="$emit('close')"
  >
    <!-- 挂载状态条 -->
    <div class="fs-bar">
      <span class="dot-tag" :class="shared ? 'green' : ''">
        <span class="dot"></span>{{ shared ? '已共享给被控机' : '未共享' }}
      </span>
      <t-button size="small" variant="outline" :loading="loading" @click="refresh">刷新</t-button>
      <t-button size="small" variant="outline" :loading="mounting" @click="doMount"
        >挂载到被控机</t-button
      >
      <div class="spacer"></div>
      <t-button size="small" theme="primary" :disabled="busy" @click="pickUpload"
        >上传文件</t-button
      >
      <t-button
        size="small"
        variant="outline"
        :disabled="busy || !selNames.length"
        @click="doDownload"
      >
        下载{{ selNames.length ? `（${selNames.length}）` : '' }}
      </t-button>
      <t-button
        size="small"
        variant="outline"
        :disabled="busy || !selNames.length"
        @click="doSaveNetdisk"
      >
        存到网盘
      </t-button>
      <t-button
        size="small"
        theme="danger"
        variant="outline"
        :disabled="busy || !selNames.length"
        @click="doDelete"
      >
        删除
      </t-button>
      <t-button size="small" variant="outline" :disabled="busy" @click="openPicker"
        >从网盘拉取</t-button
      >
    </div>
    <div class="muted fs-hint">
      上传单文件 ≤2GB、单次最多 20 个；上传完成后自动挂载到被控机。共享中断开才能查看/管理盘内文件。
    </div>

    <!-- 进度提示 -->
    <div v-if="progress" class="fs-progress">{{ progress }}</div>

    <!-- 文件列表 -->
    <div v-if="loading" class="fs-empty muted">加载中…</div>
    <div v-else-if="shared" class="fs-empty muted">
      已共享给被控机，如需查看/管理盘内文件请先刷新（会断开共享）。
    </div>
    <div v-else-if="!files.length" class="fs-empty muted">盘内暂无文件</div>
    <table v-else class="fs-table">
      <thead>
        <tr>
          <th>
            <input type="checkbox" :checked="allChecked" :disabled="busy" @change="toggleAll" />
          </th>
          <th>文件名</th>
          <th>大小</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="f in files" :key="f.name">
          <td>
            <input
              type="checkbox"
              :checked="!!sel[f.name]"
              :disabled="busy"
              @change="toggle(f.name)"
            />
          </td>
          <td class="mono">{{ f.name }}</td>
          <td class="num">{{ fmtSize(f.size) }}</td>
        </tr>
      </tbody>
    </table>

    <!-- 上传选择（隐藏） -->
    <input ref="upInput" type="file" multiple hidden @change="onUpload" />

    <!-- 从网盘拉取：轻量文件选择 -->
    <t-dialog
      v-model:visible="pickerVisible"
      header="从网盘拉取"
      width="560px"
      :close-on-overlay-click="false"
      :confirm-btn="{
        content: `拉取到设备（${pickerSel.size}）`,
        theme: 'primary',
        disabled: !pickerSel.size,
        loading: pulling,
      }"
      cancel-btn="取消"
      @confirm="doPull"
    >
      <div class="pk-bar">
        <t-select
          v-model="pickerSpace"
          :options="[
            { label: '我的空间', value: 'my' },
            { label: '公共区', value: 'public' },
          ]"
          size="small"
          style="width: 120px"
          @change="pickerCd('/')"
        />
        <t-button size="small" variant="outline" :disabled="pickerPath === '/'" @click="pickerUp">
          ‹ 上级
        </t-button>
        <span class="mono pk-path">{{ pickerPath }}</span>
        <div class="spacer"></div>
        <span class="muted">已选 {{ pickerSel.size }}/20</span>
      </div>
      <div v-if="pickerLoading" class="fs-empty muted">加载中…</div>
      <div v-else-if="pickerError" class="fs-empty">{{ pickerError }}</div>
      <div v-else-if="!pickerItems.length" class="fs-empty muted">该目录为空</div>
      <table v-else class="fs-table">
        <tbody>
          <tr v-for="it in pickerItems" :key="it.name" :class="{ 'pk-dir': it.is_dir }">
            <td style="width: 30px">
              <input
                v-if="!it.is_dir"
                type="checkbox"
                :checked="pickerSel.has(pickerPath === '/' ? it.name : pickerPath + '/' + it.name)"
                @change="togglePick(it)"
              />
            </td>
            <td class="mono" @click="it.is_dir ? pickerCd(joinName(it.name)) : togglePick(it)">
              {{ it.is_dir ? '📁 ' : '' }}{{ it.name }}
            </td>
            <td class="num">{{ it.is_dir ? '' : fmtSize(it.size) }}</td>
          </tr>
        </tbody>
      </table>
    </t-dialog>

    <!-- 存到网盘：空间 + 目录 -->
    <t-dialog
      v-model:visible="saveVisible"
      header="存到网盘"
      :close-on-overlay-click="false"
      :confirm-btn="{ content: '保 存', theme: 'primary', loading: saving }"
      cancel-btn="取消"
      @confirm="doSaveConfirm"
    >
      <div class="pk-bar">
        <label class="field-label">空间</label>
        <t-select
          v-model="saveSpace"
          :options="[
            { label: '我的空间', value: 'my' },
            { label: '公共区', value: 'public' },
          ]"
          size="small"
          style="width: 120px"
        />
        <label class="field-label">目录</label>
        <t-input v-model="saveDir" size="small" placeholder="文件传输" style="width: 180px" />
      </div>
      <div class="muted">
        将保存 {{ selNames.length }} 个文件到网盘「{{ saveDir || '文件传输' }}」目录。
      </div>
    </t-dialog>
  </t-dialog>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import * as api from './api.js';
import { fmtSize, saveBlob } from './util.js';

const props = defineProps({
  visible: { type: Boolean, default: false },
  device: { type: Object, required: true },
  teamId: { type: Number, default: 0 },
});
const emit = defineEmits(['update:visible', 'close', 'toast']);

const visible = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) refresh();
  }
);
watch(visible, (v) => emit('update:visible', v));

// ---- 盘内文件状态 ----
const shared = ref(false);
const files = ref([]);
const loading = ref(false);
const mounting = ref(false);
const busy = ref(false);
const progress = ref('');
const sel = ref({});

const selNames = computed(() => Object.keys(sel.value).filter((k) => sel.value[k]));
const allChecked = computed(
  () => !!files.value.length && files.value.every((f) => sel.value[f.name])
);

async function refresh() {
  loading.value = true;
  progress.value = '';
  try {
    const st = await api.fileStatus(props.device.id, props.teamId);
    shared.value = !!(st && st.shared);
    if (shared.value) {
      files.value = (st && st.files) || [];
    } else {
      const list = await api.fileList(props.device.id, props.teamId);
      files.value = (list && list.files) || [];
    }
    sel.value = {};
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '盘内文件读取失败' });
  } finally {
    loading.value = false;
  }
}

function toggle(name) {
  sel.value = { ...sel.value, [name]: !sel.value[name] };
}
function toggleAll() {
  const next = {};
  if (!allChecked.value) files.value.forEach((f) => (next[f.name] = true));
  sel.value = next;
}

async function doMount() {
  mounting.value = true;
  try {
    await api.mount(props.device.id, props.teamId);
    emit('toast', { type: 'success', msg: '已挂载到被控机' });
    shared.value = true;
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '挂载失败' });
  } finally {
    mounting.value = false;
  }
}

// ---- 上传（推送前校验：数量/单文件大小/文件名，与 Go filePush 口径一致） ----
const upInput = ref(null);
function pickUpload() {
  upInput.value?.click();
}

// validPushName 文件名校验：非空、≤255 字符、不含路径分隔符/非法字符（与 Go sanitizeName 口径一致）。
function validPushName(name) {
  const n = String(name || '').trim();
  if (!n) return '文件名不能为空';
  if ([...n].length > 255) return `「${n.slice(0, 24)}…」文件名过长（最多 255 字符）`;
  if (/[\\/:*?"<>|\r\n]/.test(n)) return `「${n}」文件名含非法字符（\\ / : * ? " < > |）`;
  return '';
}

async function onUpload(e) {
  const list = Array.from(e.target.files || []);
  e.target.value = '';
  if (!list.length) return;
  if (list.length > 20) {
    emit('toast', { type: 'error', msg: '文件数量超出限制（最多 20 个）' });
    return;
  }
  const over = list.find((f) => f.size > 2 * 1024 * 1024 * 1024);
  if (over) {
    emit('toast', { type: 'error', msg: `「${over.name}」超出单文件 2GB 上限` });
    return;
  }
  for (const f of list) {
    const bad = validPushName(f.name);
    if (bad) {
      emit('toast', { type: 'error', msg: bad });
      return;
    }
  }
  const dup = list.map((f) => f.name).filter((n, i, arr) => arr.indexOf(n) !== i);
  if (dup.length) {
    emit('toast', { type: 'error', msg: `存在重名文件：${dup[0]}，请先重命名再推送` });
    return;
  }
  busy.value = true;
  progress.value = `正在上传 0/${list.length}…`;
  try {
    const r = await api.pushFiles(props.device.id, list, props.teamId);
    if (!r.ok) {
      emit('toast', { type: 'error', msg: `上传失败：${r.firstErr || '请重试'}` });
    } else {
      if (r.fail) {
        emit('toast', {
          type: 'warning',
          msg: `上传完成：成功 ${r.ok} 个，失败 ${r.fail} 个（${r.firstErr}）`,
        });
      } else {
        emit('toast', { type: 'success', msg: `已上传 ${r.ok} 个文件` });
      }
      // 推送完成后挂载到被控机（与小程序口径一致）
      try {
        await api.mount(props.device.id, props.teamId);
        shared.value = true;
      } catch (e) {
        emit('toast', { type: 'warning', msg: `已上传但挂载失败：${e.message}` });
      }
      await refresh();
    }
  } finally {
    busy.value = false;
    progress.value = '';
  }
}

// ---- 下载 / 删除 ----
async function doDownload() {
  busy.value = true;
  try {
    for (const name of selNames.value) {
      const res = await api.downloadFile(props.device.id, name, props.teamId);
      await saveBlob(res, name);
    }
    emit('toast', { type: 'success', msg: `已下载 ${selNames.value.length} 个文件` });
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '下载失败' });
  } finally {
    busy.value = false;
  }
}

async function doDelete() {
  if (!window.confirm(`确定删除选中的 ${selNames.value.length} 个文件吗？`)) return;
  busy.value = true;
  try {
    await api.deleteFiles(props.device.id, selNames.value, props.teamId);
    emit('toast', { type: 'success', msg: '已删除' });
    await refresh();
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '删除失败' });
  } finally {
    busy.value = false;
  }
}

// ---- 存到网盘 ----
const saveVisible = ref(false);
const saveSpace = ref('my');
const saveDir = ref('文件传输');
const saving = ref(false);
function doSaveNetdisk() {
  saveVisible.value = true;
}

// validNetdiskDir 目标目录校验（与 Go normRel 同口径：拒 ./..、限长 400）。
function validNetdiskDir(dir) {
  const p = String(dir || '').trim();
  if (!p) return '';
  const joined = '/' + p.replace(/\\/g, '/').split('/').filter(Boolean).join('/');
  if (
    p
      .replace(/\\/g, '/')
      .split('/')
      .some((seg) => seg === '..' || seg === '.')
  ) {
    return '目标目录不能包含 . 或 ..';
  }
  if (joined.length > 400) return '目标目录路径过长（最多 400 字符）';
  return '';
}

async function doSaveConfirm() {
  const dirErr = validNetdiskDir(saveDir.value);
  if (dirErr) {
    emit('toast', { type: 'error', msg: dirErr });
    return;
  }
  for (const name of selNames.value) {
    const bad = validPushName(name);
    if (bad) {
      emit('toast', { type: 'error', msg: bad });
      return;
    }
  }
  saving.value = true;
  try {
    let ok = 0;
    let fail = 0;
    let firstErr = '';
    for (const name of selNames.value) {
      try {
        await api.saveToNetdisk(
          props.device.id,
          name,
          saveSpace.value,
          saveDir.value || '文件传输',
          props.teamId
        );
        ok++;
      } catch (e) {
        fail++;
        if (!firstErr) firstErr = e.message || String(e);
      }
    }
    if (!ok) emit('toast', { type: 'error', msg: `保存失败：${firstErr}` });
    else if (fail)
      emit('toast', { type: 'warning', msg: `成功 ${ok} 个，失败 ${fail} 个（${firstErr}）` });
    else emit('toast', { type: 'success', msg: `已保存 ${ok} 个文件到网盘` });
    if (ok && !fail) saveVisible.value = false;
  } finally {
    saving.value = false;
  }
}

// ---- 从网盘拉取（轻量文件选择） ----
const pickerVisible = ref(false);
const pickerSpace = ref('my');
const pickerPath = ref('/');
const pickerItems = ref([]);
const pickerLoading = ref(false);
const pickerError = ref('');
const pickerSel = ref(new Set());
const pulling = ref(false);

function openPicker() {
  pickerSel.value = new Set();
  pickerPath.value = '/';
  pickerVisible.value = true;
  pickerCd('/');
}
function joinName(name) {
  return pickerPath.value === '/' ? '/' + name : pickerPath.value + '/' + name;
}
function pickerUp() {
  const parts = pickerPath.value.split('/').filter(Boolean);
  parts.pop();
  pickerCd(parts.length ? '/' + parts.join('/') : '/');
}
async function pickerCd(path) {
  pickerPath.value = path;
  pickerLoading.value = true;
  pickerError.value = '';
  try {
    const data = await api.netdiskList(pickerSpace.value, path);
    pickerItems.value = (data && data.items) || [];
  } catch (e) {
    pickerItems.value = [];
    pickerError.value = e.message || '网盘目录读取失败';
  } finally {
    pickerLoading.value = false;
  }
}
function togglePick(it) {
  if (it.is_dir) {
    emit('toast', { type: 'warning', msg: '文件夹不可直接拉取，请进入目录后选择文件' });
    return;
  }
  const full = joinName(it.name);
  if (!full || full === '/') return;
  // 路径越界校验（与 Go normRel 同口径）：拒 . .. 段与超长
  const segs = full.split('/').filter(Boolean);
  if (segs.some((s) => s === '..' || s === '.')) {
    emit('toast', { type: 'error', msg: '路径不合法' });
    return;
  }
  if (full.length > 400) {
    emit('toast', { type: 'error', msg: '路径过长（最多 400 字符）' });
    return;
  }
  const next = new Set(pickerSel.value);
  if (next.has(full)) next.delete(full);
  else if (next.size < 20) next.add(full);
  else {
    emit('toast', { type: 'warning', msg: '单次最多拉取 20 个文件' });
    return;
  }
  pickerSel.value = next;
}
async function doPull() {
  const paths = [...pickerSel.value];
  if (!paths.length) return;
  if (paths.length > 20) {
    emit('toast', { type: 'error', msg: '单次最多拉取 20 个文件' });
    return;
  }
  pulling.value = true;
  try {
    const r = await api.pullFromNetdisk(props.device.id, pickerSpace.value, paths, props.teamId);
    const items = (r && r.items) || [];
    const failed = items.filter((it) => !it.ok);
    if (!items.length) {
      emit('toast', { type: 'success', msg: (r && r.message) || '已从网盘拉取到设备' });
    } else if (!failed.length) {
      emit('toast', {
        type: 'success',
        msg: (r && r.message) || `已推送 ${items.length} 个文件到设备`,
      });
    } else if (failed.length === items.length) {
      emit('toast', {
        type: 'error',
        msg: `推送全部失败（共 ${failed.length} 个）：${failed[0].name}（${failed[0].error || '未知错误'}）`,
      });
    } else {
      emit('toast', {
        type: 'warning',
        msg:
          (r && r.message) ||
          `成功 ${items.length - failed.length} 个，失败 ${failed.length} 个：${failed[0].name}（${failed[0].error || '未知错误'}）`,
      });
    }
    pickerVisible.value = false;
    await refresh();
  } catch (e) {
    // 全部失败时服务端 502 才会走这里；部分失败 200 带 items 已在上方汇总
    emit('toast', { type: 'error', msg: e.message || '拉取失败' });
  } finally {
    pulling.value = false;
  }
}
</script>

<style scoped>
.fs-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.fs-hint {
  margin-bottom: 10px;
}
.fs-progress {
  margin-bottom: 8px;
  font-size: 12.5px;
  color: var(--tk-brand);
}
.fs-empty {
  padding: 28px;
  text-align: center;
  font-size: 13px;
}
.fs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
.fs-table th,
.fs-table td {
  padding: 7px 8px;
  border-bottom: 1px solid var(--tk-line);
  text-align: left;
}
.fs-table th {
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-weight: 600;
}
.mono {
  font-family: ui-monospace, 'SFMono-Regular', Consolas, monospace;
  word-break: break-all;
}
.pk-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.pk-path {
  font-size: 12px;
  color: var(--tk-sub);
}
.pk-dir td {
  cursor: pointer;
}
.spacer {
  flex: 1;
}
</style>
