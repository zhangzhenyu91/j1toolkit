<template>
  <div class="cluster-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">
            本机节点
            <span class="right">{{ roleText }}</span>
          </div>
          <div class="cluster-status">
            <span class="status-dot" :class="serviceDot" />
            <span>{{ serviceText }}</span>
            <span class="muted" v-if="listenText">监听 {{ listenText }}</span>
            <span class="muted">版本 {{ identity.version || '未知' }}</span>
          </div>
          <div class="kv" style="margin-top: 10px">
            <span class="k">节点 ID</span>
            <span class="mono">{{ identity.node_id || '-' }}</span>
            <span class="k">集群标识</span>
            <span>{{ config.cluster_id || '未配置' }}</span>
            <span class="k">班组</span>
            <span>{{ teamText }}</span>
            <span class="k">本机地址</span>
            <span>{{ identity.ip || '-' }}</span>
            <span class="k">最近错误</span>
            <span :class="{ danger: service.last_error }">{{ service.last_error || '-' }}</span>
          </div>
          <div class="row" style="margin-top: 12px">
            <t-button size="small" variant="outline" @click="copyNodeId">复制节点 ID</t-button>
            <t-button size="small" variant="outline" :loading="loading" @click="loadStatus(true)"
              >刷新状态</t-button
            >
          </div>
        </t-card>

        <t-card>
          <div class="sec-title">集群配置</div>
          <p class="muted" style="margin: -4px 0 12px">
            保存后由后台服务在下一轮检查时生效（最多 15
            秒）；修改监听地址/端口或角色会重启节点服务。
          </p>
          <div class="form-grid">
            <label>启用集群</label>
            <div class="switch-field"><t-switch v-model="form.enabled" /></div>

            <label>角色</label>
            <t-radio-group v-model="form.role" variant="default-filled">
              <t-radio-button value="hub">Hub（协调节点，GLKVM 可选）</t-radio-button>
              <t-radio-button value="worker">Worker（经 Hub 中转）</t-radio-button>
            </t-radio-group>

            <label>集群标识</label>
            <t-input v-model="form.cluster_id" placeholder="例如 team-a，集群内所有节点一致" />

            <label>节点名称</label>
            <t-input v-model="form.node_name" placeholder="留空使用主机名" />

            <label>班组 ID</label>
            <t-input v-model="form.team_id" type="number" placeholder="0 表示未指定" />

            <label>班组名称</label>
            <t-input v-model="form.team_name" placeholder="用于显示与服务端绑定校验" />

            <label>监听地址</label>
            <t-input v-model="form.listen_host" placeholder="0.0.0.0" />

            <label>监听端口</label>
            <t-input v-model="form.listen_port" type="number" placeholder="17890" />

            <label>对外地址</label>
            <t-input
              v-model="form.advertise_host"
              placeholder="其他节点访问本机的 IP/主机名；留空自动探测"
            />

            <label>集群密钥</label>
            <div class="row grow">
              <t-input
                v-model="form.secret"
                type="password"
                class="grow"
                :placeholder="secretPlaceholder"
              />
              <t-button size="small" variant="outline" @click="generateSecret">生成</t-button>
              <t-button size="small" variant="outline" :disabled="!form.secret" @click="copySecret"
                >复制</t-button
              >
            </div>

            <label>入网审批</label>
            <div class="switch-field"><t-switch v-model="form.require_approval" /></div>

            <label>心跳间隔</label>
            <t-input-number
              v-model="form.heartbeat_sec"
              :min="3"
              :max="300"
              theme="normal"
              suffix="秒"
            />

            <label>离线判定</label>
            <t-input-number
              v-model="form.offline_sec"
              :min="3"
              :max="600"
              theme="normal"
              suffix="秒"
            />

            <template v-if="form.role === 'worker'">
              <label>Hub 地址</label>
              <t-textarea
                v-model="form.hubsText"
                :autosize="{ minRows: 2, maxRows: 5 }"
                placeholder="每行一个，例如 http://10.0.0.5:17890；可填其他班组的 Hub，由该 Hub 管理员审批"
              />
            </template>

            <template v-if="form.role === 'hub'">
              <label>Hub 对等地址</label>
              <t-textarea
                v-model="form.hubPeersText"
                :autosize="{ minRows: 2, maxRows: 5 }"
                placeholder="多 Hub 对等同步使用；单 Hub 可留空"
              />
            </template>
          </div>
          <div class="row" style="margin-top: 14px">
            <t-button theme="primary" :loading="saving" @click="saveConfig">保存配置</t-button>
            <span v-if="formError" class="form-error">{{ formError }}</span>
          </div>
        </t-card>
      </div>

      <div class="page-split-col">
        <t-card v-if="isHub">
          <div class="sec-title">
            待审批节点
            <span class="right">{{ pending.length }} 个</span>
          </div>
          <div v-if="!pending.length" class="muted">
            暂无待审批节点。Worker 启动并连接本机后会出现在这里。
          </div>
          <t-table
            v-else
            :data="pending"
            :columns="pendingCols"
            size="small"
            row-key="node_id"
            :max-height="260"
          >
            <template #team="{ row }">{{ row.team_text }}</template>
            <template #platform="{ row }">{{ row.platform_text }}</template>
            <template #last_seen="{ row }">{{ row.last_seen_text }}</template>
            <template #op="{ row }">
              <t-button size="small" theme="primary" variant="outline" @click="approve(row)"
                >批准</t-button
              >
              <t-button size="small" theme="danger" variant="text" @click="reject(row)"
                >拒绝</t-button
              >
            </template>
          </t-table>
        </t-card>

        <t-card>
          <div class="sec-title">
            {{ isHub ? '已入网节点' : '集群节点' }}
            <span class="right">{{ nodes.length }} 个</span>
          </div>
          <div v-if="!nodes.length" class="muted">
            {{ isHub ? '暂无已入网节点。' : '尚未从 Hub 获取到节点列表；入网审批通过后自动更新。' }}
          </div>
          <t-table
            v-else
            :data="nodes"
            :columns="nodeCols"
            size="small"
            row-key="node_id"
            :max-height="320"
          >
            <template #status="{ row }">
              <span class="status-line">
                <span class="status-dot" :class="row.online ? 'success' : 'warning'" />
                {{ row.online ? '在线' : '离线' }}
              </span>
            </template>
            <template #self="{ row }">
              <t-tag v-if="row.self" size="small" variant="light">本机</t-tag>
              <span v-else class="muted">{{ row.role === 'hub' ? 'Hub' : 'Worker' }}</span>
            </template>
            <template #team="{ row }">{{ row.team_text }}</template>
            <template #platform="{ row }">{{ row.platform_text }}</template>
            <template #last_seen="{ row }">{{ row.last_seen_text }}</template>
          </t-table>
        </t-card>

        <t-card v-if="pendingUpdate">
          <div class="sec-title">
            待安装更新
            <span class="right">{{ pendingUpdate.version || '未标注版本' }}</span>
          </div>
          <div class="kv">
            <span class="k">安装包</span>
            <span class="mono">{{ pendingUpdate.filename }}</span>
            <span class="k">平台/架构</span>
            <span>{{ pendingUpdate.platform }}/{{ pendingUpdate.arch }}</span>
            <span class="k">大小</span>
            <span>{{ formatSize(pendingUpdate.size) }}</span>
          </div>
          <div class="row" style="margin-top: 12px">
            <t-button theme="primary" :loading="openingUpdate" @click="openPendingUpdate"
              >打开安装包</t-button
            >
            <span class="muted">不会自动安装；打开后由系统安装器确认。</span>
          </div>
        </t-card>

        <t-card v-if="isHub">
          <div class="sec-title">
            节点更新
            <span class="right">{{ updatePackages.length }} 个安装包</span>
          </div>
          <p class="muted" style="margin: -4px 0 10px">
            Hub 登记各平台/架构的安装包，推送到匹配的已入网节点；节点只接收落盘并提示手动安装。
          </p>
          <div class="row">
            <t-button variant="outline" @click="pickPackage">选择安装包…</t-button>
            <span class="muted">{{ packageForm.filename || '未选择文件' }}</span>
          </div>
          <div v-if="packageForm.path" class="form-grid" style="margin-top: 10px">
            <label>平台</label>
            <t-select v-model="packageForm.platform" :options="platformOptions" />
            <label>架构</label>
            <t-select v-model="packageForm.arch" :options="archOptions" />
            <label>版本</label>
            <t-input v-model="packageForm.version" placeholder="例如 2.2.0" />
          </div>
          <div v-if="packageForm.path" class="row" style="margin-top: 10px">
            <t-button theme="primary" :loading="addingPackage" @click="addPackage"
              >登记安装包</t-button
            >
          </div>
          <t-table
            v-if="updatePackages.length"
            :data="updatePackages"
            :columns="packageCols"
            size="small"
            row-key="id"
            :max-height="180"
            style="margin-top: 10px"
          >
            <template #platform="{ row }">{{ row.platform }}/{{ row.arch }}</template>
            <template #size="{ row }">{{ formatSize(row.size) }}</template>
            <template #action="{ row }">
              <t-button
                size="small"
                theme="danger"
                variant="text"
                :loading="removingPackageId === row.id"
                @click="removePackage(row)"
                >移除</t-button
              >
            </template>
          </t-table>

          <div class="sec-title" style="margin-top: 16px">推送到节点</div>
          <div v-if="!pushNodes.length" class="muted">暂无其他已入网节点。</div>
          <div v-for="node in pushNodes" :key="node.node_id" class="update-node">
            <t-checkbox v-model="updateNodeSel[node.node_id]" :disabled="!node.online">
              {{ node.name || node.node_id }}（{{ node.platform_text }}）
            </t-checkbox>
            <span v-if="matchPackage(node)" class="muted">
              {{ matchPackage(node).filename }}
            </span>
            <span v-else class="danger">无匹配安装包</span>
          </div>
          <div class="row" style="margin-top: 10px">
            <t-button
              theme="primary"
              :disabled="!selectedUpdateCount"
              :loading="pushing"
              @click="pushUpdate"
              >推送更新</t-button
            >
            <span class="muted">节点端打开安装包后由系统安装器确认，不自动提权。</span>
          </div>

          <div v-if="updateJob" class="update-job">
            <div class="row">
              <span>任务状态：{{ updateJob.status }}</span>
              <t-button
                v-if="['pending', 'running'].includes(updateJob.status)"
                size="small"
                theme="danger"
                variant="text"
                @click="cancelUpdate"
                >取消</t-button
              >
            </div>
            <t-progress
              theme="plump"
              :percentage="updateProgress"
              :label="updateProgressText"
              color="#0E3DA8"
              track-color="rgba(14,61,168,.10)"
              style="margin-top: 8px"
            />
            <pre v-if="updateLogs.length" class="update-log">{{ updateLogs.join('\n') }}</pre>
            <div v-if="updateResult" class="muted" style="margin-top: 6px">
              成功 {{ updateResult.ok || 0 }} / 失败 {{ updateResult.failed || 0 }}
            </div>
          </div>
        </t-card>

        <t-card v-if="!isHub">
          <div class="sec-title">Hub 连接</div>
          <div class="kv">
            <span class="k">当前 Hub</span><span class="mono">{{ worker.hub || '-' }}</span>
            <span class="k">状态</span><span>{{ workerStatusText }}</span>
            <span class="k">最近成功</span><span>{{ fmtTime(worker.last_ok) }}</span>
            <span class="k">最近错误</span>
            <span :class="{ danger: worker.last_error }">{{ worker.last_error || '-' }}</span>
          </div>
          <p class="muted" style="margin: 10px 0 0">
            Worker 只主动连 Hub；未审批时显示等待批准，批准后自动获取节点列表。
          </p>
        </t-card>

        <t-card>
          <div class="sec-title">说明</div>
          <ul class="cluster-notes">
            <li>Hub 接 GLKVM U 盘，负责服务端指令与结果交换；Worker 无 U 盘，经 Hub 转发。</li>
            <li>一个节点只服务一个班组；树障/交跨绑定节点在服务端「班组数据 → 同步节点」设置。</li>
            <li>集群密钥所有节点必须一致；密钥只保存在本机 config.json（600）。</li>
            <li>
              节点列表来自本机服务/最近心跳，在线状态为快照；服务端只在下一次 U 盘同步时看到更新。
            </li>
          </ul>
        </t-card>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, pickFile } from '../api';

const loading = ref(false);
const saving = ref(false);
const formError = ref('');
const config = ref({});
const status = ref({});
const configSecretSet = ref(false);
const updatePackages = ref([]);
const packageForm = reactive({
  path: '',
  filename: '',
  platform: 'linux',
  arch: 'amd64',
  version: '',
});
const addingPackage = ref(false);
const removingPackageId = ref('');
const pushing = ref(false);
const updateNodeSel = reactive({});
const updateJob = ref(null);
const updateLogs = ref([]);
const openingUpdate = ref(false);
const pendingUpdate = ref(null);
let updateTimer = null;
const platformOptions = [
  { label: 'Linux', value: 'linux' },
  { label: 'Windows', value: 'windows' },
  { label: 'macOS', value: 'darwin' },
];
const archOptions = [
  { label: 'amd64 / x64', value: 'amd64' },
  { label: 'arm64', value: 'arm64' },
];
const packageCols = [
  { colKey: 'filename', title: '安装包', ellipsis: true },
  { colKey: 'platform', title: '平台/架构', width: 120 },
  { colKey: 'version', title: '版本', width: 90 },
  { colKey: 'size', title: '大小', width: 90 },
  { colKey: 'action', title: '操作', width: 80 },
];

const form = reactive({
  enabled: false,
  role: 'worker',
  cluster_id: '',
  node_name: '',
  team_id: 0,
  team_name: '',
  listen_host: '0.0.0.0',
  listen_port: 17890,
  advertise_host: '',
  secret: '',
  require_approval: true,
  heartbeat_sec: 10,
  offline_sec: 30,
  hubsText: '',
  hubPeersText: '',
});

const identity = computed(() => status.value.identity || {});
const service = computed(() => status.value.service || {});
const worker = computed(() => status.value.worker || {});
const isHub = computed(() => {
  if (service.value.running) return service.value.role === 'hub';
  return form.role === 'hub';
});
const roleText = computed(() => {
  const role = service.value.role || form.role;
  return role === 'hub' ? 'Hub' : 'Worker';
});
const teamText = computed(() => {
  const id = Number(form.team_id) || 0;
  if (!id) return '未指定';
  return `${form.team_name || '未命名'}（ID ${id}）`;
});
const listenText = computed(() => {
  const host = service.value.listen_host || form.listen_host;
  const port = service.value.listen_port || form.listen_port;
  return host && port ? `${host}:${port}` : '';
});
const serviceText = computed(() => {
  if (service.value.disabled) return '未启用';
  return service.value.running ? '运行中' : '未运行';
});
const serviceDot = computed(() => {
  if (service.value.running) return 'success';
  if (service.value.last_error) return 'danger';
  return 'warning';
});
const secretPlaceholder = computed(() =>
  configSecretSet.value ? '已设置，留空不修改' : '至少 16 位，所有节点一致'
);
const workerStatusText = computed(() => {
  const value = worker.value.status || '';
  return { approved: '已入网', pending: '等待 Hub 批准', error: '连接失败' }[value] || '未连接';
});

function splitLines(value) {
  return String(value || '')
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function fmtTime(value) {
  if (!value) return '-';
  return String(value).replace('T', ' ').replace('+08:00', '').slice(0, 19);
}

function normPlatform(value) {
  const text = String(value || '')
    .trim()
    .toLowerCase();
  if (text.startsWith('win')) return 'windows';
  if (text.includes('linux')) return 'linux';
  if (['darwin', 'macos', 'mac'].includes(text)) return 'darwin';
  return text;
}

function normArch(value) {
  const text = String(value || '')
    .trim()
    .toLowerCase();
  if (['x86_64', 'amd64', 'x64', 'x86-64', 'i686', 'i386'].includes(text)) return 'amd64';
  if (['arm64', 'aarch64'].includes(text)) return 'arm64';
  return text;
}

function matchPackage(node) {
  if (!node) return null;
  const platform = normPlatform(node.platform);
  const arch = normArch(node.arch);
  return (
    updatePackages.value
      .slice()
      .reverse()
      .find((item) => item.platform === platform && item.arch === arch) || null
  );
}

function formatSize(value) {
  const size = Number(value) || 0;
  if (!size) return '-';
  if (size >= 1024 * 1024 * 1024) return (size / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
  if (size >= 1024 * 1024) return (size / (1024 * 1024)).toFixed(1) + ' MB';
  if (size >= 1024) return (size / 1024).toFixed(1) + ' KB';
  return size + ' B';
}

async function loadPending() {
  try {
    const data = await call('clusterGetPendingUpdate');
    pendingUpdate.value = (data && data.pending) || null;
  } catch (_e) {
    /* 服务未运行/无更新时静默 */
  }
}

async function loadUpdatePackages() {
  try {
    const data = await call('clusterListUpdatePackages');
    updatePackages.value = (data && data.packages) || [];
    for (const node of pushNodes.value) {
      if (updateNodeSel[node.node_id] === undefined) {
        updateNodeSel[node.node_id] = !!(node.online && matchPackage(node));
      }
    }
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function pickPackage() {
  const path = await pickFile([
    {
      name: '安装包',
      extensions: ['deb', 'rpm', 'AppImage', 'exe', 'msi', 'pkg', 'zip'],
    },
  ]);
  if (!path) return;
  const filename = String(path).split(/[\\/]/).pop() || '';
  const lower = filename.toLowerCase();
  packageForm.path = path;
  packageForm.filename = filename;
  packageForm.platform =
    lower.includes('win') || lower.endsWith('.exe') || lower.endsWith('.msi')
      ? 'windows'
      : lower.includes('linux') ||
          lower.endsWith('.deb') ||
          lower.endsWith('.rpm') ||
          lower.includes('appimage')
        ? 'linux'
        : normPlatform(identity.value.platform);
  packageForm.arch = /arm64|aarch64/.test(lower)
    ? 'arm64'
    : /amd64|x86_64|x64/.test(lower)
      ? 'amd64'
      : normArch(identity.value.arch);
  const match = filename.match(/(\d+\.\d+\.\d+)/);
  packageForm.version = match ? match[1] : '';
}

function removePackage(row) {
  if (!row || !row.id) return;
  DialogPlugin.confirm({
    header: '移除安装包登记',
    body: `确定从 Hub 登记表移除「${row.filename}」？不会删除源文件。`,
    theme: 'warning',
    confirmBtn: '移除',
    onConfirm: async () => {
      removingPackageId.value = row.id;
      try {
        await call('clusterRemoveUpdatePackage', { id: row.id });
        MessagePlugin.success('安装包登记已移除');
        await loadUpdatePackages();
      } catch (e) {
        MessagePlugin.error(String(e.message || e));
      } finally {
        removingPackageId.value = '';
      }
    },
  });
}

async function addPackage() {
  if (!packageForm.path) {
    MessagePlugin.warning('请先选择安装包');
    return;
  }
  addingPackage.value = true;
  try {
    await call('clusterAddUpdatePackage', {
      path: packageForm.path,
      platform: packageForm.platform,
      arch: packageForm.arch,
      version: packageForm.version,
    });
    MessagePlugin.success('安装包已登记');
    await loadUpdatePackages();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    addingPackage.value = false;
  }
}

function startUpdatePoll() {
  stopUpdatePoll();
  updateTimer = setInterval(pollUpdate, 1000);
  pollUpdate();
}

function stopUpdatePoll() {
  if (updateTimer) {
    clearInterval(updateTimer);
    updateTimer = null;
  }
}

async function pollUpdate() {
  const jobId = updateJob.value && updateJob.value.id;
  if (!jobId) return;
  try {
    const data = await call('clusterGetUpdate', { job_id: jobId });
    if (data && data.job) updateJob.value = data.job;
    updateLogs.value = (data && data.logs) || [];
    if (['success', 'failed', 'cancelled'].includes((data.job || {}).status)) {
      stopUpdatePoll();
      loadPending();
    }
  } catch (_e) {
    /* 轮询失败不结束任务 */
  }
}

async function pushUpdate() {
  const nodeIds = pushNodes.value
    .filter((node) => updateNodeSel[node.node_id] && node.online && matchPackage(node))
    .map((node) => node.node_id);
  if (!nodeIds.length) {
    MessagePlugin.warning('请选择有匹配安装包的在线节点');
    return;
  }
  pushing.value = true;
  try {
    const data = await call('clusterPushUpdate', { nodes: nodeIds });
    updateJob.value = {
      id: data.job_id,
      status: 'running',
      progress: { current: 0, total: data.total || nodeIds.length },
      result: null,
    };
    updateLogs.value = [];
    startUpdatePoll();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    pushing.value = false;
  }
}

async function cancelUpdate() {
  const jobId = updateJob.value && updateJob.value.id;
  if (!jobId) return;
  try {
    await call('clusterCancelUpdate', { job_id: jobId });
    stopUpdatePoll();
    updateJob.value = { ...(updateJob.value || {}), status: 'cancelled' };
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function openPendingUpdate() {
  openingUpdate.value = true;
  try {
    const data = await call('clusterGetPendingUpdate');
    const path = data && data.pending && data.pending.path;
    if (!path) throw new Error('待安装包路径不存在');
    await call('openPath', { path });
    MessagePlugin.success('已打开安装包，请在系统安装器中确认');
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    openingUpdate.value = false;
  }
}

function withDisplay(row) {
  return {
    ...row,
    platform_text: `${row.platform || '-'}/${row.arch || '-'}`,
    team_text:
      [row.team_name, row.team_id ? `ID ${row.team_id}` : ''].filter(Boolean).join(' / ') || '-',
    last_seen_text: fmtTime(row.last_seen),
  };
}

const nodes = computed(() =>
  (status.value.nodes || []).map((row) =>
    withDisplay({
      ...row,
      self: row.self || row.node_id === identity.value.node_id,
    })
  )
);
const pending = computed(() => (status.value.pending || []).map(withDisplay));
const pushNodes = computed(() => nodes.value.filter((node) => !node.self));
const selectedUpdateCount = computed(
  () =>
    pushNodes.value.filter(
      (node) => updateNodeSel[node.node_id] && node.online && matchPackage(node)
    ).length
);
const updateProgress = computed(() => {
  const p = (updateJob.value && updateJob.value.progress) || {};
  const total = Number(p.total) || 0;
  return total > 0 ? Math.round((Number(p.current || 0) * 100) / total) : 0;
});
const updateProgressText = computed(() => {
  const p = (updateJob.value && updateJob.value.progress) || {};
  return `节点更新 ${Number(p.current || 0)}/${Number(p.total || 0)}`;
});
const updateResult = computed(() => (updateJob.value && updateJob.value.result) || null);

const pendingCols = [
  { colKey: 'name', title: '名称', width: 130 },
  { colKey: 'node_id', title: '节点 ID', width: 150, ellipsis: true },
  { colKey: 'team', title: '班组', width: 120 },
  { colKey: 'ip', title: 'IP', width: 110 },
  { colKey: 'platform', title: '平台/架构', width: 120 },
  { colKey: 'version', title: '版本', width: 80 },
  { colKey: 'last_seen', title: '最近心跳', width: 150 },
  { colKey: 'op', title: '操作', width: 130 },
];

const nodeCols = [
  { colKey: 'name', title: '名称', width: 130 },
  { colKey: 'status', title: '状态', width: 80 },
  { colKey: 'self', title: '角色', width: 80 },
  { colKey: 'node_id', title: '节点 ID', width: 150, ellipsis: true },
  { colKey: 'team', title: '班组', width: 120 },
  { colKey: 'ip', title: 'IP', width: 110 },
  { colKey: 'platform', title: '平台/架构', width: 120 },
  { colKey: 'version', title: '版本', width: 80 },
  { colKey: 'last_seen', title: '最近心跳', width: 150 },
];

function applyConfig(cfg) {
  const c = (cfg || {}).cluster || {};
  form.enabled = !!c.enabled;
  form.role = c.role === 'hub' ? 'hub' : 'worker';
  form.cluster_id = c.cluster_id || '';
  form.node_name = c.node_name || '';
  form.team_id = Number(c.team_id) || 0;
  form.team_name = c.team_name || '';
  form.listen_host = c.listen_host || '0.0.0.0';
  form.listen_port = Number(c.listen_port) || 17890;
  form.advertise_host = c.advertise_host || '';
  form.secret = '';
  form.require_approval = c.require_approval !== false;
  form.heartbeat_sec = Number(c.heartbeat_sec) || 10;
  form.offline_sec = Number(c.offline_sec) || 30;
  form.hubsText = (c.hubs || []).join('\n');
  form.hubPeersText = (c.hub_peers || []).join('\n');
  configSecretSet.value = !!c.secret_set;
}

async function loadConfig() {
  try {
    const cfg = await call('getConfig');
    config.value = (cfg || {}).cluster || {};
    applyConfig(cfg);
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function loadStatus(manual = false) {
  if (manual) loading.value = true;
  try {
    status.value = (await call('getClusterStatus')) || {};
  } catch (_e) {
    /* 轮询失败不打断页面；服务状态卡会显示最近错误 */
  } finally {
    if (manual) loading.value = false;
  }
  loadPending();
}

async function loadAll() {
  await Promise.all([loadConfig(), loadStatus()]);
  if (isHub.value) loadUpdatePackages();
}

function validate() {
  if (!form.enabled) return '';
  if (!String(form.cluster_id || '').trim()) return '启用集群需要填写集群标识';
  if (!form.secret && !configSecretSet.value) return '启用集群需要填写至少 16 位集群密钥';
  if (form.secret && form.secret.length < 16) return '集群密钥至少 16 位';
  if (form.role === 'worker' && !splitLines(form.hubsText).length)
    return 'Worker 至少填写一个 Hub 地址';
  return '';
}

async function saveConfig() {
  formError.value = validate();
  if (formError.value) return;
  saving.value = true;
  try {
    const cluster = {
      enabled: !!form.enabled,
      role: form.role,
      cluster_id: String(form.cluster_id || '').trim(),
      node_name: String(form.node_name || '').trim(),
      team_id: Number(form.team_id) || 0,
      team_name: String(form.team_name || '').trim(),
      listen_host: String(form.listen_host || '0.0.0.0').trim() || '0.0.0.0',
      listen_port: Number(form.listen_port) || 17890,
      advertise_host: String(form.advertise_host || '').trim(),
      require_approval: !!form.require_approval,
      heartbeat_sec: Number(form.heartbeat_sec) || 10,
      offline_sec: Number(form.offline_sec) || 30,
      hubs: splitLines(form.hubsText),
      hub_peers: splitLines(form.hubPeersText),
    };
    if (form.secret) cluster.secret = form.secret;
    const saved = await call('saveConfig', { cluster });
    config.value = (saved || {}).cluster || {};
    applyConfig(saved);
    MessagePlugin.success('已保存，后台服务将在 15 秒内生效');
    setTimeout(loadStatus, 2000);
    setTimeout(loadStatus, 16000);
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

function generateSecret() {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  form.secret = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  formError.value = '';
}

async function copyText(text, label) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch (_e) {
    const el = document.createElement('textarea');
    el.value = text;
    document.body.appendChild(el);
    el.select();
    try {
      document.execCommand('copy');
    } finally {
      el.remove();
    }
  }
  MessagePlugin.success(label + '已复制');
}

function copyNodeId() {
  copyText(identity.value.node_id || '', '节点 ID');
}

function copySecret() {
  copyText(form.secret || '', '集群密钥');
}

async function approve(row) {
  try {
    status.value = (await call('approveClusterNode', { node_id: row.node_id })) || {};
    MessagePlugin.success('已批准 ' + (row.name || row.node_id));
    loadStatus();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function reject(row) {
  try {
    status.value = (await call('rejectClusterNode', { node_id: row.node_id })) || {};
    MessagePlugin.success('已拒绝 ' + (row.name || row.node_id));
    loadStatus();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

let timer = null;

function startPolling() {
  stopPolling();
  timer = setInterval(loadStatus, 5000);
}

function stopPolling() {
  if (timer) {
    clearInterval(timer);
    timer = null;
  }
}

onMounted(async () => {
  await loadAll();
  startPolling();
});

onActivated(async () => {
  await loadStatus();
  if (isHub.value) loadUpdatePackages();
  startPolling();
});

onDeactivated(() => {
  stopPolling();
  stopUpdatePoll();
});
onUnmounted(() => {
  stopPolling();
  stopUpdatePoll();
});
</script>

<style scoped>
.cluster-status {
  display: flex;
  align-items: center;
  gap: 9px;
  font-size: 14px;
  flex-wrap: wrap;
}
.form-grid {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 10px 14px;
  align-items: center;
}
.form-grid > label {
  font-size: 13px;
  color: var(--td-text-color-secondary);
}
.form-grid .row {
  gap: 8px;
}
.switch-field {
  display: flex;
  align-items: center;
  justify-self: start;
}
.mono {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  word-break: break-all;
}
.danger {
  color: var(--tk-red);
}
.form-error {
  color: var(--tk-red);
  font-size: 12px;
}
.update-node {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 6px 0;
  border-bottom: 1px solid var(--tk-line);
}
.update-job {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--tk-line);
}
.update-log {
  max-height: 180px;
  overflow: auto;
  margin: 8px 0 0;
  padding: 8px 10px;
  background: var(--tk-fill);
  border: 1px solid var(--tk-line);
  border-radius: 6px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
.cluster-notes {
  margin: 0;
  padding-left: 18px;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.8;
}
@media (max-width: 760px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
