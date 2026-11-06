<template>
  <div class="sync-panel">
    <div class="sync-head">
      <div>
        <div class="sync-title">同步节点</div>
        <div class="sync-sub">树障/交跨统计表由绑定的节点执行；节点列表来自最近一次 U 盘会话。</div>
      </div>
      <div class="sync-actions">
        <label class="sync-night">
          <input
            v-model="nightEnabled"
            type="checkbox"
            :disabled="savingNight"
            @change="saveNight"
          />
          夜间自动同步
        </label>
        <button type="button" class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button
          v-if="isAdmin && view.cache_empty"
          type="button"
          class="btn btn-sm btn-primary"
          :disabled="reading"
          @click="readNodes"
        >
          {{ reading ? '读取中…' : '读取节点' }}
        </button>
      </div>
    </div>

    <div class="sync-meta">
      班组 ID：{{ view.team?.id || teamId || '-' }} · 上次节点表：{{
        view.updated_at || '从未读取'
      }}
      <span v-if="view.cache_empty">（缓存为空）</span>
    </div>
    <div v-if="error" class="sync-error">{{ error }}</div>

    <table class="sync-table">
      <thead>
        <tr>
          <th>应用</th>
          <th>同步节点</th>
          <th>状态</th>
          <th>最近同步</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="app in apps" :key="app.app">
          <td class="app">{{ app.name }}</td>
          <td>
            <select
              v-model="selections[app.app]"
              class="f-sel sync-select"
              :disabled="savingApp === app.app"
            >
              <option value="">未绑定</option>
              <option v-for="node in nodes" :key="node.node_id" :value="node.node_id">
                {{ nodeLabel(node, app.app) }}
              </option>
            </select>
          </td>
          <td>
            <span v-if="!bindingOf(app.app).node_id" class="muted">未绑定</span>
            <span v-else>
              <span class="dot" :class="bindingOf(app.app).online ? 'on' : 'off'" />
              {{ bindingOf(app.app).node_name || bindingOf(app.app).node_id }}
              <span v-if="!bindingOf(app.app).ready" class="warn">
                {{ bindingOf(app.app).reason }}
              </span>
            </span>
          </td>
          <td class="muted">{{ stateText(app.app) }}</td>
          <td class="ops">
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="savingApp === app.app"
              @click="saveNode(app.app)"
            >
              保存
            </button>
            <button
              type="button"
              class="btn btn-sm"
              :disabled="!bindingOf(app.app).ready || stateRunning(app.app)"
              @click="push(app.app)"
            >
              立即同步
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <p class="sync-hint">
      {{
        nodes.length
          ? '节点选择只列出本班组节点；「未配置表路径/文件不存在」的节点保存后仍不可同步。'
          : isAdmin
            ? '当前班组还没有节点。确认 Hub 已启动并处理过 U 盘后，点「读取节点」。'
            : '当前班组还没有节点，请联系超管在 Hub 启动后读取节点。'
      }}
    </p>

    <div v-if="isAdmin" class="hub-block">
      <div class="sync-title hub-title">Hub 设备</div>
      <p class="sync-hint">
        hub_id 填 Hub 节点的节点 ID（该节点客户端「集群」页可复制）；未注册设备的 Hub
        回退该应用的旧单设备配置。
      </p>
      <table class="sync-table">
        <thead>
          <tr>
            <th>hub_id</th>
            <th>名称</th>
            <th>ddns</th>
            <th>MAC</th>
            <th>代登账号</th>
            <th>启用</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="hub in hubs" :key="hub.hub_id">
            <td class="mono">{{ hub.hub_id }}</td>
            <td>{{ hub.name || '-' }}</td>
            <td class="mono">{{ hub.ddns }}</td>
            <td class="mono">{{ hub.mac || '-' }}</td>
            <td>{{ hub.kvm_user || '-' }}</td>
            <td>{{ hub.enabled ? '是' : '否' }}</td>
            <td>
              <button type="button" class="btn btn-sm" @click="editHub(hub)">编辑</button>
            </td>
          </tr>
          <tr>
            <td><input v-model="hubForm.hub_id" class="f-sel" placeholder="Hub node_id" /></td>
            <td><input v-model="hubForm.name" class="f-sel" placeholder="名称" /></td>
            <td><input v-model="hubForm.ddns" class="f-sel" placeholder="设备 ddns" /></td>
            <td><input v-model="hubForm.mac" class="f-sel" placeholder="MAC（可空）" /></td>
            <td><input v-model="hubForm.kvm_user" class="f-sel" placeholder="代登账号" /></td>
            <td><input v-model="hubForm.enabled" type="checkbox" /></td>
            <td>
              <button
                type="button"
                class="btn btn-sm btn-primary"
                :disabled="savingHub"
                @click="saveHub"
              >
                保存
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  getTeamSync,
  getTeamSyncHubs,
  readClusterNodes,
  saveTeamSyncHub,
  saveTeamSyncNight,
  saveTeamSyncNode,
} from '../api.js';
import { getSyncStatus as getTreeStatus, syncPushNow as pushTree } from '../../treehazard/api.js';
import { getSyncStatus as getCrossStatus, syncPushNow as pushCross } from '../../crossing/api.js';

const props = defineProps({
  teamId: { type: Number, default: 0 },
  isAdmin: { type: Boolean, default: false },
});

const DEFAULT_APPS = [
  { app: 'tree_sync', name: '树障统计表' },
  { app: 'crossing_sync', name: '交跨统计表' },
];

const view = ref({});
const loading = ref(false);
const reading = ref(false);
const savingNight = ref(false);
const savingApp = ref('');
const savingHub = ref(false);
const error = ref('');
const nightEnabled = ref(false);
const selections = reactive({});
const states = reactive({});
const hubs = ref([]);
const hubForm = reactive({
  hub_id: '',
  name: '',
  ddns: '',
  mac: '',
  kvm_user: '',
  enabled: true,
});

const apps = computed(() =>
  Array.isArray(view.value.apps) && view.value.apps.length ? view.value.apps : DEFAULT_APPS
);
const nodes = computed(() => view.value.nodes || []);

function bindingOf(app) {
  return (view.value.bindings || {})[app] || {};
}

function nodeLabel(node, app) {
  const table = (node.sync_tables || {})[app] || {};
  const teamID = Number(view.value.team?.id) || Number(props.teamId) || 0;
  const parts = [node.name || node.node_id, node.online ? '在线' : '离线'];
  if (teamID && node.team_id !== teamID) {
    parts.push('班组不一致');
  } else if (!table.configured) {
    parts.push('未配置表路径');
  } else if (!table.exists) {
    parts.push('表文件不存在');
  }
  return parts.join(' · ');
}

function stateRunning(app) {
  return !!states[app]?.running;
}

function stateText(app) {
  const state = states[app];
  if (!state) return '—';
  if (state.running) return '同步中…';
  if (state.enabled === false) {
    return state.reason ? `未启用：${state.reason}` : '未启用';
  }
  if (state.last?.result) {
    return `${state.last.created_at || ''} ${state.last.result}`;
  }
  return state.node ? `节点：${state.node.name || state.node.id}` : '待同步';
}

async function loadStates() {
  const [tree, cross] = await Promise.allSettled([
    getTreeStatus(props.teamId),
    getCrossStatus(props.teamId),
  ]);
  states.tree_sync = tree.status === 'fulfilled' ? tree.value : null;
  states.crossing_sync = cross.status === 'fulfilled' ? cross.value : null;
}

async function load() {
  loading.value = true;
  error.value = '';
  try {
    view.value = (await getTeamSync()) || {};
    nightEnabled.value = !!view.value.night_enabled;
    for (const app of apps.value) {
      selections[app.app] = bindingOf(app.app).node_id || '';
    }
    if (props.isAdmin) await loadHubs();
    await loadStates();
  } catch (e) {
    error.value = e.message || '读取同步节点失败';
  } finally {
    loading.value = false;
  }
}

async function loadHubs() {
  try {
    const result = await getTeamSyncHubs();
    hubs.value = result?.hubs || [];
  } catch (_e) {
    hubs.value = [];
  }
}

function editHub(hub) {
  Object.assign(hubForm, {
    hub_id: hub.hub_id || '',
    name: hub.name || '',
    ddns: hub.ddns || '',
    mac: hub.mac || '',
    kvm_user: hub.kvm_user || '',
    enabled: hub.enabled !== false,
  });
}

async function saveHub() {
  savingHub.value = true;
  error.value = '';
  try {
    const result = await saveTeamSyncHub({
      hub_id: hubForm.hub_id.trim(),
      name: hubForm.name.trim(),
      ddns: hubForm.ddns.trim(),
      mac: hubForm.mac.trim(),
      kvm_user: hubForm.kvm_user.trim(),
      enabled: !!hubForm.enabled,
    });
    hubs.value = result?.hubs || [];
    MessagePlugin.success('Hub 设备已保存');
  } catch (e) {
    error.value = e.message || '保存 Hub 失败';
    MessagePlugin.error(error.value);
  } finally {
    savingHub.value = false;
  }
}

async function saveNode(app) {
  savingApp.value = app;
  error.value = '';
  try {
    view.value = (await saveTeamSyncNode(app, selections[app] || '')) || {};
    nightEnabled.value = !!view.value.night_enabled;
    for (const item of apps.value) {
      selections[item.app] = bindingOf(item.app).node_id || '';
    }
    await loadStates();
    MessagePlugin.success('同步节点已保存');
  } catch (e) {
    error.value = e.message || '保存失败';
    MessagePlugin.error(error.value);
  } finally {
    savingApp.value = '';
  }
}

async function saveNight() {
  savingNight.value = true;
  error.value = '';
  try {
    view.value = (await saveTeamSyncNight(nightEnabled.value)) || {};
    nightEnabled.value = !!view.value.night_enabled;
    MessagePlugin.success(nightEnabled.value ? '已开启夜间同步' : '已关闭夜间同步');
  } catch (e) {
    nightEnabled.value = !!view.value.night_enabled;
    error.value = e.message || '保存夜间开关失败';
    MessagePlugin.error(error.value);
  } finally {
    savingNight.value = false;
  }
}

async function readNodes() {
  reading.value = true;
  error.value = '';
  try {
    const result = await readClusterNodes();
    MessagePlugin.success(`已读取 ${result?.nodes ?? 0} 个节点`);
    await load();
  } catch (e) {
    error.value = e.message || '读取节点失败';
    MessagePlugin.error(error.value);
  } finally {
    reading.value = false;
  }
}

async function push(app) {
  try {
    if (app === 'tree_sync') await pushTree(props.teamId);
    else await pushCross(props.teamId);
    MessagePlugin.success('已触发同步，结果见状态与站内通知');
    setTimeout(loadStates, 1200);
  } catch (e) {
    MessagePlugin.error(e.message || '触发同步失败');
  }
}

watch(
  () => props.teamId,
  () => load()
);
onMounted(load);
</script>

<style scoped>
.sync-panel {
  padding: 4px 2px 18px;
}
.sync-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: flex-start;
}
.sync-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--tk-ink);
  letter-spacing: 0.3px;
}
.sync-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--tk-sub);
}
.sync-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.sync-night {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--tk-sub);
  white-space: nowrap;
}
.sync-meta {
  margin-top: 8px;
  font-size: 12px;
  color: var(--tk-ph);
  font-variant-numeric: tabular-nums;
}
.sync-error {
  margin-top: 8px;
  color: var(--tk-red);
  font-size: 12px;
}
.sync-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 12px;
  font-size: 13px;
}
.sync-table th {
  text-align: left;
  font-weight: 500;
  font-size: 12px;
  color: var(--tk-sub);
  padding: 8px 10px;
  border-bottom: 1px solid var(--tk-line);
}
.sync-table td {
  padding: 10px;
  border-bottom: 1px solid var(--tk-line);
  vertical-align: middle;
}
.sync-table .app {
  font-weight: 600;
  white-space: nowrap;
}
.sync-select {
  width: 100%;
  max-width: 360px;
}
.sync-table .ops {
  white-space: nowrap;
}
.sync-table .ops .btn + .btn {
  margin-left: 6px;
}
.dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  margin-right: 5px;
  background: var(--tk-ph);
}
.dot.on {
  background: var(--tk-green);
}
.dot.off {
  background: var(--tk-yellow);
}
.warn {
  display: block;
  margin-top: 3px;
  color: var(--tk-yellow);
  font-size: 12px;
}
.muted {
  color: var(--tk-ph);
}
.sync-hint {
  margin: 12px 0 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--tk-ph);
}
.hub-block {
  margin-top: 22px;
  padding-top: 16px;
  border-top: 1px solid var(--tk-line);
}
.hub-title {
  font-size: 13px;
}
.mono {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  word-break: break-all;
}
</style>
