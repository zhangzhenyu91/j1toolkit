<template>
  <div>
    <!-- 板块标题行（旧页 section-title 口径） -->
    <div class="kv-head">
      <h3>设备列表</h3>
      <span class="more">{{ devCnt }}</span>
    </div>

    <div v-if="isDemo" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >设备为静态演示数据，未请求真实服务；切 live 模式（localStorage shade.apiMode=live）对接 Go
        API。</span
      >
    </div>

    <!-- 筛选工具条 -->
    <div class="kv-bar">
      <input
        v-model="query"
        class="kv-input"
        placeholder="搜索名称 / IP / MAC"
        @input="debouncedLoad"
      />
      <div class="kv-ops-r">
        <select
          v-if="teamOptions.length > 1"
          v-model="teamId"
          class="kv-sel"
          title="仅超级管理员可切换班组；其余角色固定为本班组"
          @change="onTeamChange"
        >
          <option v-for="t in teamOptions" :key="t.value" :value="t.value">{{ t.label }}</option>
        </select>
        <button
          type="button"
          class="kv-btn"
          :disabled="manualLoading"
          @click="loadDevices('manual')"
        >
          <svg
            viewBox="0 0 24 24"
            width="15"
            height="15"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
            :class="{ 'kv-rot': manualLoading }"
          >
            <path d="M20 12a8 8 0 1 1-2.4-5.7" />
            <path d="M20 3.5v4h-4" />
          </svg>
          刷新
        </button>
      </div>
    </div>

    <!-- 班组设备回退提示（fallback=true 时显示，屏十四口径） -->
    <div v-if="fallback" class="kv-fb">
      <span class="ic">⚠</span>
      <p>
        本班组暂未接入设备，当前展示「{{
          fallbackTeam || '默认班组'
        }}」的设备；本班组设备组接入后自动切换、此提示消失。
      </p>
    </div>

    <!-- 设备卡片宫格 -->
    <section class="kv-grid">
      <template v-if="loading && !loaded">
        <div v-for="i in 3" :key="'sk' + i" class="card kv-sk"></div>
      </template>
      <div v-else-if="error && !loaded" class="kv-empty">
        <svg
          viewBox="0 0 24 24"
          width="44"
          height="44"
          fill="none"
          stroke="currentColor"
          stroke-width="1.7"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path
            d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"
          />
          <path d="M12 9.2v4" />
          <circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none" />
        </svg>
        <p>{{ error }}</p>
        <button type="button" class="kv-btn kv-btn-sm" @click="loadDevices('init')">
          重新加载
        </button>
      </div>
      <div v-else-if="!devices.length" class="kv-empty">
        <svg
          viewBox="0 0 24 24"
          width="44"
          height="44"
          fill="none"
          stroke="currentColor"
          stroke-width="1.7"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <rect x="3" y="4" width="18" height="12.5" rx="2" />
          <path d="M8.5 20.5h7M12 16.5v4" />
        </svg>
        <p>
          {{ query ? '暂无设备，试试更换搜索关键字' : '暂无设备，或联系管理员开通平台设备权限' }}
        </p>
      </div>
      <template v-else>
        <DeviceCard
          v-for="(d, i) in devices"
          :key="`${d.id}-${loadSeq}`"
          :device="d"
          :animate="animateCards"
          :index="i"
          @jump="doJump"
          @open-files="openFiles"
        />
      </template>
    </section>

    <!-- 文件互推面板 -->
    <FilePanel
      v-model:visible="fileVisible"
      :device="fileDevice"
      :team-id="teamId"
      @close="fileDevice = null"
      @toast="onToast"
    />
  </div>
</template>

<script setup>
// 远程控制（迁移 server/public/kvm.html，应用展示名统一「远程控制」）：设备卡片实时数据代理自 GLKVM Cloud 平台；
// 终端/远程控制经 /api/v1/kvm/jump 签发带态跳转地址；文件互推为后续新增能力（保留全套功能）。
// 加载模式：init 首屏（骨架+入场动画）；filter 筛选（重排+动画）；manual 手动刷新（保留卡片、按钮图标转圈）；auto 定时静默。
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { createGeneration } from '../features/core/async.js';
import { getApiMode } from '../api.js';
import { createPoller } from '../features/core/poller.js';
import * as api from '../features/kvm/api.js';
import DeviceCard from '../features/kvm/DeviceCard.vue';
import FilePanel from '../features/kvm/FilePanel.vue';
import { currentUserObj } from '../features/kvm/util.js';

const isDemo = computed(() => getApiMode() === 'demo');
const me = currentUserObj();

// ---- 班组切换（仅超管；数据源 /admin/teams，端点缺失时隐藏回退本班） ----
const teamId = ref(0);
const teamOptions = ref([]);
const TEAM_KEY = 'kvm_team_id';

async function loadTeams() {
  if (me.role !== 'admin') return;
  try {
    const list = await api.listTeams();
    teamOptions.value = list.map((t) => ({ label: t.name, value: t.id }));
    const saved = Number(localStorage.getItem(TEAM_KEY)) || 0;
    if (list.some((t) => t.id === saved)) teamId.value = saved;
    else if (list.length) {
      teamId.value = list[0].id;
      localStorage.setItem(TEAM_KEY, String(list[0].id));
    }
  } catch (e) {
    /* 班组列表拉取失败：不显示切换器，由后端落默认班组出数 */
  }
}

function onTeamChange() {
  try {
    localStorage.setItem(TEAM_KEY, String(teamId.value));
  } catch (e) {
    /* 忽略 */
  }
  loadDevices('filter');
}

// ---- 设备列表 ----
const devices = ref([]);
const fallback = ref(false);
const fallbackTeam = ref('');
const loading = ref(false);
const loaded = ref(false);
const manualLoading = ref(false);
const animateCards = ref(false);
// 重排序号：init/filter 时自增强制重建卡片 DOM，整卡重放入场动画（同旧页 innerHTML 重渲染口径）；
// manual/auto 不动序号，保留卡片、不重播
const loadSeq = ref(0);
// loadGen 设备列表请求代际：定时刷新/手动刷新/搜索重叠时丢弃旧响应。
const loadGen = createGeneration();
const error = ref('');
const query = ref('');
let qTimer = null;

const devCnt = computed(() =>
  loaded.value ? `共 ${devices.value.length} 台设备 · 每 30s 自动刷新` : '数据实时取自 GLKVM 平台'
);

function debouncedLoad() {
  clearTimeout(qTimer);
  qTimer = setTimeout(() => loadDevices('filter'), 400);
}

async function loadDevices(mode = 'filter') {
  // 请求代际：30s 自动刷新、手动刷新与搜索防抖可能重叠，只有最后一次请求可落地。
  const gen = loadGen.next();
  if (mode === 'init') {
    loading.value = true;
    loaded.value = false;
    animateCards.value = true;
  } else if (mode === 'filter') {
    loading.value = true;
    animateCards.value = true;
  } else if (mode === 'manual') {
    manualLoading.value = true;
    animateCards.value = false;
  }
  try {
    const data = await api.getDevices(teamId.value, query.value.trim());
    if (!loadGen.isCurrent(gen)) return;
    devices.value = (data && data.items) || [];
    fallback.value = !!(data && data.fallback);
    fallbackTeam.value = (data && data.fallback_team) || '';
    error.value = '';
    loaded.value = true;
    if (mode === 'init' || mode === 'filter') loadSeq.value++; // 重排后重放入场动画
  } catch (e) {
    if (!loadGen.isCurrent(gen)) return;
    if (mode === 'auto') return; // 静默刷新失败不打断页面
    if (loaded.value) MessagePlugin.error(e.message || '刷新失败');
    // 已有内容时仅提示；首屏失败渲染错误态
    else error.value = e.message || '设备列表加载失败';
  } finally {
    if (loadGen.isCurrent(gen)) {
      loading.value = false;
      manualLoading.value = false;
    }
  }
}

// ---- 带态跳转 ----
let jumping = false;
async function doJump(to, ddns) {
  if (jumping) return;
  jumping = true;
  try {
    const r = await api.jump(to, ddns, teamId.value);
    if (r && r.url) window.open(r.url, '_blank');
    else MessagePlugin.error('跳转地址为空');
  } catch (e) {
    MessagePlugin.error(e.message || '跳转失败');
  } finally {
    jumping = false;
  }
}

// ---- 文件互推面板 ----
const fileVisible = ref(false);
const fileDevice = ref(null);
function openFiles(d) {
  fileDevice.value = d;
  fileVisible.value = true;
}
function onToast({ type, msg }) {
  if (type === 'error') MessagePlugin.error(msg);
  else if (type === 'warning') MessagePlugin.warning(msg);
  else MessagePlugin.success(msg);
}

// 30s 定时静默刷新（失败不打断页面）：共享轮询器，离页 cancel 立即停止。
const autoPoller = createPoller({ intervalMs: 30000, baseMs: 30000, maxMs: 60000 });
let firstAutoTick = true;

onMounted(() => {
  loadTeams().then(() => loadDevices('init'));
  autoPoller.poll(async () => {
    if (firstAutoTick) {
      firstAutoTick = false;
      return {};
    }
    await loadDevices('auto');
    return {};
  });
});
onUnmounted(() => {
  autoPoller.cancel();
  clearTimeout(qTimer);
});
</script>

<style scoped>
/* ===== 板块标题行 ===== */
.kv-head {
  display: flex;
  align-items: center;
  gap: 9px;
  margin: 6px 0 16px;
  animation: kv-enter 0.55s var(--tk-ease) both;
}
.kv-head h3 {
  margin: 0;
  font-size: 15.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
}
.kv-head .more {
  margin-left: auto;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}

/* ===== 筛选工具条 ===== */
.kv-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0 0 18px;
  flex-wrap: wrap;
  animation: kv-enter 0.55s var(--tk-ease) 0.06s both;
}
.kv-input {
  box-sizing: border-box;
  height: 38px;
  width: 210px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  padding: 0 12px;
  font-size: 13px;
  font-family: inherit;
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.kv-input::placeholder {
  color: var(--tk-ph);
}
.kv-input:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
/* 原生下拉：去系统外观换主题箭头，仍走原生弹层（可正常打开） */
.kv-sel {
  box-sizing: border-box;
  height: 38px;
  min-width: 132px;
  padding: 0 30px 0 12px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card)
    url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%2386909C' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M4 6l4 4 4-4'/%3E%3C/svg%3E")
    no-repeat right 9px center / 14px 14px;
  appearance: none;
  -webkit-appearance: none;
  font-size: 12.5px;
  font-family: inherit;
  color: var(--tk-ink);
  outline: none;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.kv-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
.kv-ops-r {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
}

/* ===== 按钮（旧页 .btn / .btn-sm 口径） ===== */
.kv-btn {
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 38px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13px;
  font-family: inherit;
  font-weight: 600;
  letter-spacing: 1px;
  line-height: 1;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    background 0.15s;
}
.kv-btn svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.kv-btn:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.kv-btn:disabled {
  opacity: 0.72;
}
.kv-btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}
/* 手动刷新期间按钮图标转圈（不重排卡片、不出骨架，避免闪现） */
.kv-btn svg.kv-rot {
  animation: kv-rot 1s linear infinite;
}
@keyframes kv-rot {
  to {
    transform: rotate(360deg);
  }
}

/* ===== 班组设备回退提示横幅（浅黄底 + 黄色描边，屏十四口径） ===== */
.kv-fb {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 18px;
  padding: 10px 14px;
  border-radius: 8px;
  border: 1.5px solid var(--tk-yellow);
  background: var(--tk-yellow-soft);
  color: var(--tk-ink);
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  line-height: 1.7;
}
.kv-fb .ic {
  flex: none;
  font-size: 14px;
}
.kv-fb p {
  margin: 0;
}

/* ===== 设备卡片宫格（同工作台应用宫格口径） ===== */
.kv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 18px;
}
/* 网格内中和 styles.css `.card + .card` 的相邻上边距：否则首卡独高、相邻卡不等高 */
.kv-grid > .card {
  margin-top: 0;
}

/* 宫格内空态/错误态占满整行（白卡 + 1px 发丝边 + 轻影）；骨架卡片 */
.kv-empty {
  grid-column: 1 / -1;
  background: var(--card);
  border: 1px solid var(--tk-line);
  border-radius: 12px;
  box-shadow: var(--tk-shadow-card);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--tk-ph);
}
.kv-empty svg {
  color: var(--tk-dash);
}
.kv-empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
}
.kv-sk {
  box-sizing: border-box;
  height: 224px;
  padding: 0;
  background: var(--tk-fill);
  position: relative;
  overflow: hidden;
}
.kv-sk::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(100deg, transparent 25%, rgba(255, 255, 255, 0.65), transparent 75%);
  transform: translateX(-100%);
  animation: kv-shimmer 1.6s ease-in-out infinite;
}
@keyframes kv-shimmer {
  to {
    transform: translateX(100%);
  }
}

@media (max-width: 640px) {
  .kv-input {
    width: 140px;
  }
}

/* 标题行/工具条一次性上浮入场（克制，落位静止） */
@keyframes kv-enter {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@media (prefers-reduced-motion: reduce) {
  .kv-head,
  .kv-bar {
    animation: none;
  }
  .kv-btn svg.kv-rot,
  .kv-sk::after {
    animation: none;
  }
}
</style>
