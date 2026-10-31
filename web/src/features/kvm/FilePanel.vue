<template>
  <!-- 文件互推主面板（进出场由 overlay-motion 驱动：遮罩淡入 + 内容上浮/缩放，reduced-motion 降级） -->
  <div v-if="mainRendered" class="fs-mask ov-mask" :class="mainPhase" @mousedown.self="closeMain">
    <div class="fs-modal fs-modal-lg ov-panel" role="dialog" aria-modal="true">
      <div class="fs-scroll">
        <div class="fs-head">
          <span class="fs-title"
            >文件互推 · {{ device?.description || device?.ddns || '设备' }}</span
          >
          <button type="button" class="fs-x" aria-label="关闭" @click="closeMain">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>

        <!-- 挂载状态条 + 操作 -->
        <div class="fs-bar">
          <span class="dot-tag" :class="shared ? 'green' : ''">
            <span class="dot"></span>{{ shared ? '已共享给被控机' : '未共享' }}
          </span>
          <button type="button" class="fs-btn fs-btn-sm" :disabled="loading" @click="refresh">
            刷新
          </button>
          <button
            v-if="!shared"
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="mounting"
            @click="doMount"
          >
            挂载到被控机
          </button>
          <button
            v-else
            type="button"
            class="fs-btn fs-btn-sm fs-btn-on"
            :disabled="busy || takingBack"
            @click="takeBack"
          >
            {{ takingBack ? '正在取回…' : '取回 U 盘' }}
          </button>
          <div class="spacer"></div>
          <button
            type="button"
            class="fs-btn fs-btn-sm fs-btn-on"
            :disabled="busy"
            @click="pickUpload"
          >
            上传文件
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="busy || !selNames.length"
            @click="doDownload"
          >
            下载{{ selNames.length ? `（${selNames.length}）` : '' }}
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="busy || !selNames.length"
            @click="doSaveNetdisk"
          >
            存到网盘
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm fs-btn-danger"
            :disabled="busy || !selNames.length"
            @click="delConfirmVisible = true"
          >
            删除
          </button>
          <button type="button" class="fs-btn fs-btn-sm" :disabled="busy" @click="openPicker">
            从网盘拉取
          </button>
        </div>
        <div class="fs-hint">
          上传单文件 ≤2GB、单次最多 20 个；上传完成后自动挂载到被控机。共享中点「取回 U
          盘」断开共享后即可查看/管理盘内文件。
        </div>

        <!-- 后台传输任务（网盘 → 设备）：服务端执行，关页不中断；再次打开面板可见终态 -->
        <div v-if="pullTasks.length" class="fs-tasks">
          <div class="fs-tasks-h">
            <span>传输任务</span>
            <small>后台执行 · 可关闭页面</small>
          </div>
          <div v-for="t in pullTasks" :key="t.id" class="fs-task">
            <div class="fs-task-top">
              <span class="fs-task-name" :title="t.current || ''"
                >{{ t.total }} 个文件{{ t.current ? ` · ${t.current}` : '' }}</span
              >
              <span class="fs-task-st" :class="taskStateClass(t)">{{ taskStateText(t) }}</span>
              <button
                v-if="taskTerminal(t)"
                type="button"
                class="fs-task-del"
                :disabled="taskDeleting === t.id"
                title="删除任务记录"
                @click="removeTask(t)"
              >
                删除
              </button>
            </div>
            <div class="fs-task-bar"><i :style="{ width: taskPct(t) + '%' }"></i></div>
            <div class="fs-task-sub">{{ taskSubText(t) }}</div>
          </div>
        </div>

        <!-- 进度提示 -->
        <div v-if="progress" class="fs-progress">{{ progress }}</div>

        <!-- 文件列表 -->
        <div v-if="loading" class="fs-empty">加载中…</div>
        <div v-else-if="shared" class="fs-empty">
          已共享给被控机；点「取回 U 盘」断开共享后即可查看/管理盘内文件。
        </div>
        <div v-else-if="!files.length" class="fs-empty">盘内暂无文件</div>
        <table v-else class="fs-table">
          <thead>
            <tr>
              <th class="fs-cb">
                <input type="checkbox" :checked="allChecked" :disabled="busy" @change="toggleAll" />
              </th>
              <th>文件名</th>
              <th class="fs-sz">大小</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in files" :key="f.name">
              <td class="fs-cb">
                <input
                  type="checkbox"
                  :checked="!!sel[f.name]"
                  :disabled="busy"
                  @change="toggle(f.name)"
                />
              </td>
              <td class="mono">{{ f.name }}</td>
              <td class="fs-sz num">{{ fmtSize(f.size) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- 上传选择（隐藏） -->
  <input ref="upInput" type="file" multiple hidden @change="onUpload" />

  <!-- 从网盘拉取：目录浏览 + 文件多选（≤20） -->
  <div v-if="pickerRendered" class="fs-mask fs-mask-sub ov-mask" :class="pickerPhase">
    <div class="fs-modal ov-panel" role="dialog" aria-modal="true">
      <div class="fs-scroll">
        <div class="fs-head">
          <span class="fs-title">从网盘拉取</span>
          <button type="button" class="fs-x" aria-label="关闭" @click="pickerVisible = false">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="pk-bar">
          <select v-model="pickerSpace" class="fs-sel" @change="pickerCd('/')">
            <option value="my">我的空间</option>
            <option value="public">公共区</option>
          </select>
          <button
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="pickerPath === '/'"
            @click="pickerUp"
          >
            ‹ 上级
          </button>
          <span class="mono pk-path">{{ pickerPath }}</span>
          <div class="spacer"></div>
          <span class="pk-cnt">已选 {{ pickerSel.size }}/20</span>
        </div>
        <div v-if="pickerLoading" class="fs-empty">加载中…</div>
        <div v-else-if="pickerError" class="fs-empty">{{ pickerError }}</div>
        <div v-else-if="!pickerItems.length" class="fs-empty">该目录为空</div>
        <table v-else class="fs-table">
          <tbody>
            <tr v-for="it in pickerItems" :key="it.name" :class="{ 'pk-dir': it.is_dir }">
              <td class="fs-cb">
                <input
                  v-if="!it.is_dir"
                  type="checkbox"
                  :checked="
                    pickerSel.has(pickerPath === '/' ? it.name : pickerPath + '/' + it.name)
                  "
                  @change="togglePick(it)"
                />
              </td>
              <td class="mono" @click="it.is_dir ? pickerCd(joinName(it.name)) : togglePick(it)">
                {{ it.is_dir ? '📁 ' : '' }}{{ it.name }}
              </td>
              <td class="fs-sz num">{{ it.is_dir ? '' : fmtSize(it.size) }}</td>
            </tr>
          </tbody>
        </table>
        <div class="fs-actions">
          <button type="button" class="fs-btn fs-btn-sm" @click="pickerVisible = false">
            取消
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm fs-btn-on"
            :disabled="pulling || !pickerSel.size"
            @click="doPull"
          >
            {{ pulling ? '正在加入…' : `拉取到设备（${pickerSel.size}）` }}
          </button>
        </div>
      </div>
    </div>
  </div>

  <!-- 存到网盘：空间 + 目录（含目录合法性校验） -->
  <div v-if="saveRendered" class="fs-mask fs-mask-sub ov-mask" :class="savePhase">
    <div class="fs-modal fs-modal-sm ov-panel" role="dialog" aria-modal="true">
      <div class="fs-scroll">
        <div class="fs-head">
          <span class="fs-title">存到网盘</span>
          <button type="button" class="fs-x" aria-label="关闭" @click="saveVisible = false">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="pk-bar">
          <label class="fs-lab">空间</label>
          <select v-model="saveSpace" class="fs-sel">
            <option value="my">我的空间</option>
            <option value="public">公共区</option>
          </select>
          <label class="fs-lab">目录</label>
          <input v-model="saveDir" class="fs-in" type="text" placeholder="文件传输" />
        </div>
        <div class="fs-hint">
          将保存 {{ selNames.length }} 个文件到网盘「{{ saveDir || '文件传输' }}」目录。
        </div>
        <div class="fs-actions">
          <button
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="saving"
            @click="saveVisible = false"
          >
            取消
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm fs-btn-on"
            :disabled="saving"
            @click="doSaveConfirm"
          >
            {{ saving ? '保存中…' : '保 存' }}
          </button>
        </div>
      </div>
    </div>
  </div>

  <!-- 删除确认 -->
  <div v-if="delRendered" class="fs-mask fs-mask-sub ov-mask" :class="delPhase">
    <div class="fs-modal fs-modal-sm ov-panel" role="dialog" aria-modal="true">
      <div class="fs-scroll">
        <div class="fs-head">
          <span class="fs-title">删除选中文件？</span>
          <button type="button" class="fs-x" aria-label="关闭" @click="delConfirmVisible = false">
            <svg
              viewBox="0 0 24 24"
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="fs-hint">确定删除选中的 {{ selNames.length }} 个文件吗？该操作不可恢复。</div>
        <div class="fs-actions">
          <button
            type="button"
            class="fs-btn fs-btn-sm"
            :disabled="busy"
            @click="delConfirmVisible = false"
          >
            取消
          </button>
          <button
            type="button"
            class="fs-btn fs-btn-sm fs-btn-danger"
            :disabled="busy"
            @click="doDelete"
          >
            确认删除
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 设备文件互推（文件转发点 fileshare）：挂载/断开查看、上传推送（批量校验）、下载、删除、
// 存到网盘、从网盘拉取（目录浏览多选）。弹层沿用旧页 ndpick 弹层骨架（8px 圆角白卡 + 发丝边）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import * as api from './api.js';
import { fmtSize, saveBlob } from './util.js';
import { useOverlayMotion } from '../../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../../components/overlay-motion.css';

const props = defineProps({
  visible: { type: Boolean, default: false },
  device: { type: Object, default: null },
  teamId: { type: Number, default: 0 },
});
const emit = defineEmits(['update:visible', 'close', 'toast']);

const visible = ref(props.visible);
watch(
  () => props.visible,
  (v) => {
    visible.value = v;
    if (v) {
      refresh();
      loadTasks();
      startTaskPoll();
    } else {
      stopTaskPoll();
    }
  }
);
watch(visible, (v) => {
  emit('update:visible', v);
  if (!v) {
    stopTaskPoll();
    emit('close');
  }
});

// closeMain 关闭主面板（遮罩空白处 / 右上角关闭 / Esc 共用）
function closeMain() {
  visible.value = false;
}

// Esc 关闭最上层弹层（设计规范：弹层 Esc 关闭）
function onKeydown(e) {
  if (e.key !== 'Escape') return;
  if (delConfirmVisible.value) delConfirmVisible.value = false;
  else if (saveVisible.value) saveVisible.value = false;
  else if (pickerVisible.value) pickerVisible.value = false;
  else if (visible.value) closeMain();
}
onMounted(() => window.addEventListener('keydown', onKeydown));
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown);
  stopTaskPoll();
});

// ---- 盘内文件状态 ----
const shared = ref(false);
const files = ref([]);
const loading = ref(false);
const mounting = ref(false);
const takingBack = ref(false);
const busy = ref(false);
const progress = ref('');
const sel = ref({});

const selNames = computed(() => Object.keys(sel.value).filter((k) => sel.value[k]));
const allChecked = computed(
  () => !!files.value.length && files.value.every((f) => sel.value[f.name])
);

async function refresh() {
  if (!props.device) return;
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

// takeBack 取回 U 盘：/files 在共享中会先断开共享并挂载回 KVM，再返回盘内文件
// （设备文件转发点口径，与小程序「取回 U 盘」一致）。
async function takeBack() {
  if (takingBack.value) return;
  if (!window.confirm('取回 U 盘会断开与被控机的共享，并挂载回 KVM。确定取回？')) return;
  takingBack.value = true;
  progress.value = '正在取回 U 盘…';
  try {
    const list = await api.fileList(props.device.id, props.teamId);
    shared.value = false;
    files.value = (list && list.files) || [];
    sel.value = {};
    emit('toast', { type: 'success', msg: '已取回 U 盘' });
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '取回 U 盘失败' });
  } finally {
    takingBack.value = false;
    progress.value = '';
  }
}

// ---- 上传（推送前批量校验：数量/单文件大小/文件名/重名，与 Go filePush 口径一致） ----
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
      } catch (err) {
        emit('toast', { type: 'warning', msg: `已上传但挂载失败：${err.message}` });
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

const delConfirmVisible = ref(false);
async function doDelete() {
  delConfirmVisible.value = false;
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

// ---- 从网盘拉取（目录浏览 + 文件多选） ----
const pickerVisible = ref(false);
// 四弹层进出场（overlay-motion）：渲染存在与阶段类由各自显隐源驱动，reduced-motion 自动降级
const { rendered: mainRendered, phaseClass: mainPhase } = useOverlayMotion(visible);
const { rendered: pickerRendered, phaseClass: pickerPhase } = useOverlayMotion(pickerVisible);
const { rendered: saveRendered, phaseClass: savePhase } = useOverlayMotion(saveVisible);
const { rendered: delRendered, phaseClass: delPhase } = useOverlayMotion(delConfirmVisible);
const pickerSpace = ref('my');
const pickerPath = ref('/');
const pickerItems = ref([]);
const pickerLoading = ref(false);
const pickerError = ref('');
const pickerSel = ref(new Set());
const pulling = ref(false);
// 后台传输任务：面板打开期间轮询；关掉页面后由服务端继续，回来重新打开即可看到终态。
const pullTasks = ref([]);
const taskStates = new Map();
const removedTaskIds = new Set();
const taskDeleting = ref('');
let taskTimer = null;

function taskTerminal(t) {
  return ['done', 'partial', 'failed'].includes(t.state);
}
async function removeTask(t) {
  if (!taskTerminal(t) || taskDeleting.value === t.id) return;
  taskDeleting.value = t.id;
  try {
    await api.deletePullTask(props.device.id, t.id, props.teamId);
    removedTaskIds.add(t.id);
    pullTasks.value = pullTasks.value.filter((x) => x.id !== t.id);
    taskStates.delete(t.id);
  } catch (e) {
    emit('toast', { type: 'error', msg: e.message || '删除任务记录失败' });
  } finally {
    taskDeleting.value = '';
  }
}

function taskStateText(t) {
  return (
    {
      queued: '排队中',
      running: '传输中',
      done: '已完成',
      partial: '部分失败',
      failed: '失败',
    }[t.state] || t.state
  );
}
function taskStateClass(t) {
  return (
    { queued: 'wait', running: 'run', done: 'ok', partial: 'warn', failed: 'bad' }[t.state] || ''
  );
}
function taskPct(t) {
  // 单文件按字节平滑推进；多文件按完成数推进（单调不回退，总字节随新文件开始增长会回退）。
  if (t.total === 1 && t.total_bytes > 0) {
    return Math.min(100, Math.round((t.done_bytes / t.total_bytes) * 100));
  }
  return t.total ? Math.min(100, Math.round(((t.done + t.failed) / t.total) * 100)) : 0;
}
function taskSubText(t) {
  const files = `${t.done}/${t.total} 个`;
  const bytes = t.total_bytes ? ` · ${fmtSize(t.done_bytes)} / ${fmtSize(t.total_bytes)}` : '';
  const bad = (t.items || []).find((it) => !it.ok);
  if (t.state === 'failed') {
    const reason = t.error || (bad ? `${bad.name}：${bad.error || '失败'}` : '');
    return `${files}${bytes}${reason ? ' · ' + reason : ''}`;
  }
  if (t.state === 'partial') {
    return `${files}${bytes}${bad ? ` · ${bad.name}：${bad.error || '失败'}` : ''}`;
  }
  if (t.state === 'done') return `${files}${bytes} · 已推送至设备`;
  return `${files}${bytes}${t.current ? ` · ${t.current}` : ''}`;
}

async function loadTasks() {
  if (!props.device) return;
  try {
    const d = await api.pullTasks(props.device.id, props.teamId);
    const list = ((d && d.items) || []).filter((t) => !removedTaskIds.has(t.id));
    let finished = false;
    for (const t of list) {
      const prev = taskStates.get(t.id);
      if (prev && prev !== t.state && ['done', 'partial', 'failed'].includes(t.state)) {
        finished = true;
      }
      taskStates.set(t.id, t.state);
    }
    pullTasks.value = list;
    // 任务完成后刷新一次盘内文件，让新推送的文件立即出现（轮询只在状态跃迁时触发）。
    if (finished) await refresh();
  } catch (e) {
    // 任务列表失败静默：不影响盘内文件浏览/上传等主流程
  }
}
function startTaskPoll() {
  if (taskTimer) return;
  taskTimer = setInterval(loadTasks, 2000);
}
function stopTaskPoll() {
  if (taskTimer) {
    clearInterval(taskTimer);
    taskTimer = null;
  }
}

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
    pickerVisible.value = false;
    emit('toast', {
      type: 'success',
      msg: (r && r.message) || '已加入后台传输任务，可关闭页面稍后查看',
    });
    await loadTasks();
    startTaskPoll();
  } catch (e) {
    // 参数/权限/设备握手失败才是同步错误；具体传输成败由任务列表展示
    emit('toast', { type: 'error', msg: e.message || '加入传输任务失败' });
  } finally {
    pulling.value = false;
  }
}
</script>

<style scoped>
/* ===== 弹层骨架（迁移旧页 .mask/.modal 弹层口径：8px 圆角白卡 + 发丝边 + 轻影；进出场由 overlay-motion 驱动） ===== */
.fs-mask {
  position: fixed;
  inset: 0;
  z-index: 200;
  padding: 24px;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
}
.fs-mask-sub {
  z-index: 210;
}
.fs-modal {
  box-sizing: border-box;
  background: var(--card);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
}
.fs-modal-lg {
  width: 720px;
}
.fs-modal-sm {
  width: 420px;
}
.fs-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  /* 预留滚动条槽位 + 细滚动条：长列表出滚动条时内容不偏移（同 teamdata 弹层口径） */
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--dash) transparent;
}
.fs-scroll::-webkit-scrollbar {
  width: 5px;
}
.fs-scroll::-webkit-scrollbar-thumb {
  background: var(--dash);
  border-radius: 8px;
}
.fs-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.fs-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.fs-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fs-x {
  box-sizing: border-box;
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    background 0.15s,
    color 0.15s;
}
.fs-x:hover {
  background: var(--inverse-bg);
  color: var(--inverse-text);
}

/* ===== 按钮 / 输入（紧凑） ===== */
.fs-btn {
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 34px;
  padding: 0 14px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 12.5px;
  font-family: inherit;
  font-weight: 600;
  letter-spacing: 0.5px;
  line-height: 1;
  cursor: pointer;
  white-space: nowrap;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    background 0.15s;
}
.fs-btn:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.fs-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.fs-btn-on {
  background: var(--tk-brand);
  border-color: transparent;
  color: var(--on-brand);
}
.fs-btn-on:hover:not(:disabled) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.fs-btn-danger {
  background: var(--tk-red);
  border-color: transparent;
  color: var(--on-brand);
}
.fs-btn-danger:hover:not(:disabled) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
/* 原生下拉：去系统外观换主题箭头，仍走原生弹层（可正常打开） */
.fs-sel {
  box-sizing: border-box;
  height: 34px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card)
    url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%2386909C' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M4 6l4 4 4-4'/%3E%3C/svg%3E")
    no-repeat right 9px center / 14px 14px;
  appearance: none;
  -webkit-appearance: none;
  padding: 0 28px 0 10px;
  font-size: 12.5px;
  font-family: inherit;
  color: var(--tk-ink);
  outline: none;
  cursor: pointer;
}
.fs-in {
  box-sizing: border-box;
  height: 34px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  padding: 0 10px;
  font-size: 12.5px;
  font-family: inherit;
  color: var(--tk-ink);
  outline: none;
}
.fs-sel:focus,
.fs-in:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
.fs-in {
  width: 180px;
}
.fs-lab {
  font-size: 12px;
  color: var(--tk-sub);
}

/* ===== 工具条 / 提示 ===== */
.fs-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.fs-hint {
  margin-bottom: 10px;
  font-size: 12px;
  color: var(--tk-ph);
  line-height: 1.7;
}
.fs-progress {
  margin-bottom: 8px;
  font-size: 12.5px;
  color: var(--tk-brand);
}
/* ===== 后台传输任务 ===== */
.fs-tasks {
  margin-bottom: 10px;
  padding: 10px 12px;
  border: 1px solid var(--hair);
  border-radius: 8px;
  background: var(--tk-fill);
}
.fs-tasks-h {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-ink);
}
.fs-tasks-h small {
  font-weight: 400;
  color: var(--tk-ph);
}
.fs-task {
  padding: 2px 0;
}
.fs-task + .fs-task {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--hair);
}
.fs-task-top {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.fs-task-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fs-task-st {
  flex: none;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}
.fs-task-st.run {
  color: var(--tk-brand);
  background: rgba(14, 61, 168, 0.08);
}
.fs-task-st.wait {
  color: var(--tk-sub);
  background: var(--hair);
}
.fs-task-st.ok {
  color: var(--tk-green);
  background: var(--tk-green-soft);
}
.fs-task-st.warn {
  color: var(--tk-warn-text);
  background: rgba(255, 125, 0, 0.12);
}
.fs-task-st.bad {
  color: var(--tk-red);
  background: var(--tk-red-soft);
}
.fs-task-del {
  flex: none;
  padding: 0 2px;
  border: 0;
  background: none;
  color: var(--tk-ph);
  font-size: 11px;
  cursor: pointer;
}
.fs-task-del:hover:not(:disabled) {
  color: var(--tk-red);
}
.fs-task-del:disabled {
  opacity: 0.5;
  cursor: default;
}
.fs-task-bar {
  height: 4px;
  margin: 6px 0 4px;
  border-radius: 4px;
  background: var(--hair);
  overflow: hidden;
}
.fs-task-bar i {
  display: block;
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.3s var(--tk-ease);
}
.fs-task-sub {
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--tk-ph);
  word-break: break-all;
}
/* 加载/空态占位等高（loading → 空态 → 列表 高度不跳变） */
.fs-empty {
  box-sizing: border-box;
  min-height: 132px;
  padding: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  font-size: 13px;
  color: var(--tk-ph);
}

/* ===== 文件表（紧凑行高） ===== */
.fs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
.fs-table th,
.fs-table td {
  padding: 7px 8px;
  border-bottom: 1px solid var(--hair);
  text-align: left;
}
.fs-table th {
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-weight: 600;
  font-size: 11.5px;
  letter-spacing: 1px;
}
.fs-cb {
  width: 30px;
}
.fs-sz {
  width: 90px;
  white-space: nowrap;
  color: var(--tk-sub);
}
.mono {
  font-family: ui-monospace, 'SFMono-Regular', Consolas, monospace;
  word-break: break-all;
}
.num {
  font-variant-numeric: tabular-nums;
}

/* ===== 网盘拉取面板 ===== */
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
.pk-cnt {
  font-size: 12px;
  color: var(--tk-ph);
}
.pk-dir td {
  cursor: pointer;
}
.spacer {
  flex: 1;
}
.fs-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
</style>
