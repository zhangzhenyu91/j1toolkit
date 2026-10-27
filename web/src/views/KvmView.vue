<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">远程连接计算机</h1>
      <span class="page-sub">设备实时数据代理自 GLKVM 平台 · 终端/远程控制带态跳转 · 文件互推</span>
      <div class="spacer"></div>
      <t-select
        v-if="teamOptions.length > 1"
        v-model="teamId"
        :options="teamOptions"
        size="small"
        style="width: 150px"
        title="仅超级管理员可切换班组；其余角色固定为本班组"
        @change="onTeamChange"
      />
    </div>

    <div v-if="isDemo" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >设备为静态演示数据，未请求真实服务；切 live 模式（localStorage shade.apiMode=live）对接 Go
        API。</span
      >
    </div>

    <!-- 筛选工具条 -->
    <div class="toolbar">
      <t-input
        v-model="query"
        class="dev-search"
        clearable
        placeholder="搜索名称 / IP / MAC"
        @change="debouncedLoad"
      />
      <div class="spacer"></div>
      <t-button size="small" variant="outline" :loading="loading" @click="loadDevices"
        >刷新</t-button
      >
    </div>

    <!-- 班组设备回退提示 -->
    <div v-if="fallback" class="fb-banner">
      <span>⚠</span>
      <p>
        本班组暂未接入设备，当前展示「{{
          fallbackTeam || '默认班组'
        }}」的设备；本班组设备组接入后自动切换、此提示消失。
      </p>
    </div>

    <div class="muted dev-cnt">共 {{ devices.length }} 台设备 · 每 30s 自动刷新</div>

    <!-- 设备卡片宫格 -->
    <div v-if="loading && !loaded" class="dev-grid">
      <div v-for="i in 3" :key="i" class="card dev-sk"></div>
    </div>
    <div v-else-if="error && !loaded" class="empty">
      <p>{{ error }}</p>
      <t-button size="small" variant="outline" @click="loadDevices">重新加载</t-button>
    </div>
    <div v-else-if="!devices.length" class="empty">
      <p>{{ query ? '暂无设备，试试更换搜索关键字' : '暂无设备，或联系管理员开通平台设备权限' }}</p>
    </div>
    <div v-else class="dev-grid">
      <DeviceCard
        v-for="d in devices"
        :key="d.id"
        :device="d"
        @jump="doJump"
        @open-files="openFiles"
      />
    </div>

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
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { getApiMode } from '../api.js';
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

function onTeamChange(v) {
  localStorage.setItem(TEAM_KEY, String(v));
  loadDevices();
}

// ---- 设备列表 ----
const devices = ref([]);
const fallback = ref(false);
const fallbackTeam = ref('');
const loading = ref(false);
const loaded = ref(false);
const error = ref('');
const query = ref('');
let qTimer = null;

function debouncedLoad() {
  clearTimeout(qTimer);
  qTimer = setTimeout(() => loadDevices(), 400);
}

async function loadDevices() {
  loading.value = true;
  try {
    const data = await api.getDevices(teamId.value, query.value.trim());
    devices.value = (data && data.items) || [];
    fallback.value = !!(data && data.fallback);
    fallbackTeam.value = (data && data.fallback_team) || '';
    error.value = '';
    loaded.value = true;
  } catch (e) {
    error.value = e.message || '设备列表加载失败';
    if (!loaded.value) devices.value = [];
  } finally {
    loading.value = false;
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

// 30s 定时静默刷新（失败不打断页面）
let timer = null;

onMounted(() => {
  loadTeams().then(loadDevices);
  timer = setInterval(() => {
    if (!document.hidden) loadDevices();
  }, 30000);
});
onUnmounted(() => {
  clearInterval(timer);
  clearTimeout(qTimer);
});
</script>

<style scoped>
.spacer {
  flex: 1;
}
.dev-search {
  width: 240px;
}
.fb-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 14px;
  padding: 10px 14px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  font-size: 12.5px;
  color: var(--tk-sub);
}
.fb-banner p {
  margin: 0;
}
.dev-cnt {
  margin-bottom: 10px;
}
.dev-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 14px;
}
.dev-sk {
  height: 224px;
  background: linear-gradient(90deg, var(--tk-fill) 25%, #fff 50%, var(--tk-fill) 75%);
  background-size: 400% 100%;
  animation: sk 1.2s ease infinite;
}
@keyframes sk {
  from {
    background-position: 100% 0;
  }
  to {
    background-position: 0 0;
  }
}
.empty {
  padding: 36px;
  text-align: center;
  color: var(--tk-ph);
  font-size: 13px;
}
</style>
