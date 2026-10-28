<template>
  <!-- 开合过渡同旧页 ShadeNdPick：遮罩淡入 0.3s + 弹体上浮缩放 0.35s -->
  <transition name="ndp">
    <div v-if="visible" class="ndp-mask" @mousedown.self="cancel">
      <div class="ndp-modal" role="dialog" aria-modal="true" aria-label="保存到网盘：选择目录">
        <div class="ndp-scroll">
          <div class="ndp-head">
            <span class="ndp-title">{{ title }}</span>
            <button type="button" class="ndp-x" aria-label="关闭" @click="cancel">
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

          <div v-if="spaces.length > 1" class="ndp-spaces">
            <button
              v-for="s in spaces"
              :key="s.key"
              type="button"
              class="ndp-space"
              :class="{ on: space === s.key }"
              @click="switchSpace(s.key)"
            >
              {{ s.name }}
            </button>
          </div>

          <div class="ndp-crumb">
            <template v-for="(seg, i) in crumbSegs" :key="seg.path">
              <em v-if="i > 0">/</em>
              <a
                v-if="i < crumbSegs.length - 1"
                role="button"
                tabindex="0"
                @click="goDir(seg.path)"
                @keydown.enter.prevent="goDir(seg.path)"
                >{{ seg.name }}</a
              >
              <b v-else>{{ seg.name }}</b>
            </template>
          </div>

          <div class="ndp-list">
            <div v-if="loading" class="ndp-state"><span class="ndp-spin"></span>加载中…</div>
            <div v-else-if="error" class="ndp-state">
              {{ error }}
              <button type="button" class="ndp-mini" @click="loadDirs(false)">重试</button>
            </div>
            <div v-else-if="!items.length" class="ndp-state">此目录下没有文件夹</div>
            <template v-else>
              <div
                v-for="it in items"
                :key="it.name"
                class="ndp-row"
                role="button"
                tabindex="0"
                @click="enterDir(it.name)"
                @keydown.enter.prevent="enterDir(it.name)"
              >
                <svg
                  viewBox="0 0 24 24"
                  width="20"
                  height="20"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.7"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                >
                  <path
                    d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"
                  />
                </svg>
                <span class="ndp-nm">{{ it.name }}</span>
              </div>
            </template>
          </div>

          <div v-if="mkdirOpen" class="ndp-mkdirrow">
            <input
              ref="mkdirInput"
              v-model="mkdirName"
              class="ndp-in"
              type="text"
              maxlength="200"
              placeholder="文件夹名称"
              autocomplete="off"
              @keydown.enter.prevent="submitMkdir"
            />
            <button
              type="button"
              class="ndp-mini ndp-mini-on"
              :disabled="mkdirBusy"
              @click="submitMkdir"
            >
              创建
            </button>
            <button type="button" class="ndp-mini" :disabled="mkdirBusy" @click="mkdirOpen = false">
              取消
            </button>
          </div>

          <div class="ndp-foot">
            <button type="button" class="ndp-mkdir" @click="openMkdir">
              <svg
                viewBox="0 0 24 24"
                width="14"
                height="14"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path
                  d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"
                />
                <path d="M12 10.5v5M9.5 13h5" />
              </svg>
              新建文件夹
            </button>
            <button type="button" class="ndp-btn" @click="cancel">取消</button>
            <button type="button" class="ndp-btn ndp-btn-on" @click="confirm">确定</button>
          </div>
        </div>
      </div>
    </div>
  </transition>
</template>

<script setup>
// 网盘目录选择弹层（迁移旧页 assets/ndpick.js）：空间分段 + 面包屑回跳 + 只列文件夹、
// 单击进入子目录（目标目录 = 当前浏览目录）+ 就地新建文件夹。取消（遮罩/Esc/X/取消钮）resolve 不选。
import { computed, nextTick, ref, watch } from 'vue';
import { netdiskSpaces, netdiskDirs, netdiskMkdir } from './api.js';

const props = defineProps({
  visible: { type: Boolean, default: false },
  // 打开时默认定位的「我的空间」内相对路径（如 '安全日记录'；不存在自动回退根目录）
  defaultPath: { type: String, default: '' },
  title: { type: String, default: '保存到网盘：选择目录' },
  confirmText: { type: String, default: '确定' },
});
const emit = defineEmits(['update:visible', 'confirm', 'cancel']);

const spaces = ref([{ key: 'my', name: '我的空间' }]);
const space = ref('my');
// dir 内部带前导斜杠（'/' 根目录），与 /list、/mkdir 一致；confirm 时不带首尾斜杠
const dir = ref('/');
const items = ref([]);
const loading = ref(false);
const error = ref('');
const mkdirOpen = ref(false);
const mkdirName = ref('');
const mkdirBusy = ref(false);
const mkdirInput = ref(null);

const crumbSegs = computed(() => {
  const parts = dir.value.split('/').filter(Boolean);
  const segs = [
    { name: spaces.value.find((s) => s.key === space.value)?.name || '我的空间', path: '/' },
  ];
  let acc = '';
  parts.forEach((p) => {
    acc += '/' + p;
    segs.push({ name: p, path: acc });
  });
  return segs;
});

function relOf() {
  return dir.value.replace(/^\/+/, '');
}
function joinRel(d, name) {
  return (d === '/' ? '' : d) + '/' + name;
}

async function loadSpaces() {
  try {
    const list = await netdiskSpaces();
    if (list.length) spaces.value = list;
  } catch (e) {
    /* 探测失败：仅「我的空间」 */
  }
}

async function loadDirs(isInitial) {
  loading.value = true;
  error.value = '';
  try {
    items.value = await netdiskDirs(space.value, dir.value);
  } catch (e) {
    items.value = [];
    // 默认定位目录不存在（如从未保存过）：回退根目录重载
    if (isInitial && dir.value !== '/') {
      dir.value = '/';
      loading.value = false;
      return loadDirs(false);
    }
    error.value = e.message || '加载失败';
  } finally {
    loading.value = false;
  }
}

function goDir(path) {
  if (path === dir.value) return;
  dir.value = path;
  mkdirOpen.value = false;
  loadDirs(false);
}
function enterDir(name) {
  dir.value = joinRel(dir.value, name);
  mkdirOpen.value = false;
  loadDirs(false);
}
function switchSpace(key) {
  if (key === space.value) return;
  space.value = key;
  dir.value = '/';
  mkdirOpen.value = false;
  loadDirs(false);
}

function openMkdir() {
  mkdirOpen.value = true;
  mkdirName.value = '';
  nextTick(() => mkdirInput.value?.focus());
}
async function submitMkdir() {
  const name = mkdirName.value.trim();
  if (!name) return;
  mkdirBusy.value = true;
  try {
    await netdiskMkdir(space.value, dir.value, name);
    mkdirOpen.value = false;
    loadDirs(false);
  } catch (e) {
    error.value = e.message || '创建失败';
  } finally {
    mkdirBusy.value = false;
  }
}

function close() {
  emit('update:visible', false);
}
function cancel() {
  close();
  emit('cancel');
}
function confirm() {
  close();
  emit('confirm', { space: space.value, dir: relOf() });
}

// Esc 捕获阶段拦截并停传播：只关本弹层，不连带关页面既有弹层
function onKey(e) {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  cancel();
}
watch(
  () => props.visible,
  (v) => {
    if (!v) {
      document.removeEventListener('keydown', onKey, true);
      return;
    }
    space.value = 'my';
    const def = String(props.defaultPath || '')
      .trim()
      .replace(/^\/+|\/+$/g, '');
    dir.value = def ? '/' + def : '/';
    items.value = [];
    mkdirOpen.value = false;
    mkdirName.value = '';
    loadSpaces();
    loadDirs(true);
    document.addEventListener('keydown', onKey, true);
  }
);
</script>

<style scoped>
.ndp-mask {
  position: fixed;
  inset: 0;
  z-index: 400;
  padding: 24px;
  background: rgba(29, 33, 41, 0.45);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
/* 开合过渡（旧页 ndpick.js：遮罩 opacity .3s + 弹体 translateY(16px) scale(.98) .35s） */
.ndp-enter-active,
.ndp-leave-active {
  transition: opacity 0.3s;
}
.ndp-enter-from,
.ndp-leave-to {
  opacity: 0;
}
.ndp-modal {
  background: var(--tk-bg, #fff);
  background: #fff;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  width: 520px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--tk-shadow-card);
  transform: none;
  transition: transform 0.35s var(--tk-ease);
}
.ndp-enter-from .ndp-modal,
.ndp-leave-to .ndp-modal {
  transform: translateY(16px) scale(0.98);
}
.ndp-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  /* 滚动条出现时不挤偏内容（同旧页 ndpick.js） */
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.ndp-scroll::-webkit-scrollbar {
  width: 5px;
}
.ndp-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.ndp-scroll::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.ndp-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.ndp-head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.ndp-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ndp-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--tk-shadow-card);
  transition:
    background 0.15s,
    color 0.15s;
}
.ndp-x:hover {
  background: var(--tk-ink);
  color: #fff;
}
.ndp-spaces {
  display: flex;
  gap: 4px;
  margin-top: 12px;
  padding: 3px;
  background: var(--tk-fill);
  border-radius: 8px;
}
.ndp-space {
  flex: 1;
  height: 30px;
  border: none;
  border-radius: 6px;
  background: none;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-sub);
  cursor: pointer;
  transition:
    background 0.15s,
    color 0.15s;
}
.ndp-space.on {
  background: #fff;
  color: var(--tk-brand);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.06);
}
.ndp-crumb {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  min-width: 0;
  flex-wrap: wrap;
  margin-top: 12px;
}
.ndp-crumb a {
  color: var(--tk-brand);
  font-weight: 600;
  cursor: pointer;
}
.ndp-crumb em {
  font-style: normal;
  color: var(--tk-ph);
}
.ndp-crumb b {
  font-weight: 600;
  word-break: break-all;
}
.ndp-list {
  margin-top: 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  min-height: 180px;
  max-height: 300px;
  overflow-y: auto;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.ndp-list::-webkit-scrollbar {
  width: 5px;
}
.ndp-list::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.ndp-list::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.ndp-list::-webkit-scrollbar-track {
  background: transparent;
}
.ndp-row {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 8px 12px;
  font-size: 12.5px;
  cursor: pointer;
}
.ndp-row:hover {
  background: var(--tk-fill);
}
.ndp-row svg {
  flex: none;
  color: var(--tk-ink);
}
.ndp-nm {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ndp-state {
  padding: 30px 20px;
  text-align: center;
  font-size: 12.5px;
  color: var(--tk-sub);
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
}
.ndp-spin {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  border: 2px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: ndp-rot 1s linear infinite;
}
@keyframes ndp-rot {
  to {
    transform: rotate(360deg);
  }
}
.ndp-mkdirrow {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.ndp-in {
  flex: 1;
  min-width: 0;
  height: 34px;
  padding: 0 12px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  font-size: 12.5px;
  color: var(--tk-ink);
  outline: none;
}
.ndp-in:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px rgba(14, 61, 168, 0.12);
}
.ndp-mini {
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}
.ndp-mini:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.ndp-mini-on {
  background: var(--tk-brand);
  border-color: transparent;
  color: #fff;
}
.ndp-mini-on:hover {
  color: #fff;
  filter: brightness(0.92);
}
.ndp-foot {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 10px;
  margin-top: 18px;
}
.ndp-mkdir {
  margin-right: auto;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: none;
  background: none;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-brand);
  cursor: pointer;
}
.ndp-mkdir:hover {
  filter: brightness(0.85);
}
.ndp-btn {
  height: 34px;
  padding: 0 16px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  color: var(--tk-ink);
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
}
.ndp-btn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.ndp-btn-on {
  background: var(--tk-brand);
  border-color: transparent;
  color: #fff;
}
.ndp-btn-on:hover {
  color: #fff;
  filter: brightness(0.92);
}
@media (prefers-reduced-motion: reduce) {
  .ndp-spin {
    animation: none;
  }
  .ndp-enter-active,
  .ndp-leave-active,
  .ndp-modal {
    transition: none;
  }
  .ndp-enter-from .ndp-modal,
  .ndp-leave-to .ndp-modal {
    transform: none;
  }
}
</style>
