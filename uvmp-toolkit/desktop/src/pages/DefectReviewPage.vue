<template>
  <div class="defect-page">
    <t-card class="defect-overview">
      <div class="overview-head">
        <div>
          <div class="sec-title">机巡审核</div>
          <div class="muted">多实例总览 · 运行中 {{ runningCount }} / {{ instances.length }}</div>
        </div>
        <div class="overview-actions">
          <t-button variant="outline" @click="addInstance"> + 新建实例 </t-button>
          <t-button variant="outline" @click="toggleProgressWindow">
            {{ progressWindowOpen ? '关闭进度浮窗' : '进度浮窗' }}
          </t-button>
          <t-button
            theme="primary"
            :loading="startingAll"
            :disabled="!canStartAll"
            @click="startAllInstances"
          >
            全部启动
          </t-button>
          <t-button
            theme="danger"
            variant="outline"
            :loading="stoppingAll"
            :disabled="runningCount === 0"
            @click="stopAllInstances"
          >
            全部停止
          </t-button>
        </div>
      </div>
      <div class="instance-status-list">
        <button
          v-for="row in instanceBoard"
          :key="row.id"
          type="button"
          class="instance-status"
          :class="row.theme"
          @click="openInstance(row.id)"
        >
          <span class="status-dot" :class="row.theme" />
          <span class="instance-status-name">{{ row.name }}</span>
          <span class="instance-status-state">{{ row.stateLabel }}</span>
        </button>
      </div>
    </t-card>

    <div class="instance-grid">
      <t-card
        v-for="row in instanceBoard"
        :key="row.id"
        class="instance-card"
        :class="{ running: row.running, failed: row.failed }"
        role="button"
        tabindex="0"
        @click="openInstance(row.id)"
        @keydown.enter.self="openInstance(row.id)"
      >
        <div class="instance-card-head">
          <span class="status-dot" :class="row.theme" />
          <div class="instance-card-name" :title="row.name">{{ row.name }}</div>
          <span class="instance-state">{{ row.stateLabel }}</span>
        </div>
        <div class="instance-card-url muted" :title="row.url">
          {{ row.url || '未设置启动网址' }}
        </div>
        <t-progress
          theme="plump"
          :percentage="row.pct"
          :label="`${row.pct}%`"
          color="#0E3DA8"
          track-color="rgba(14,61,168,.10)"
        />
        <div class="instance-card-stage" :title="row.stage">{{ row.stage }}</div>
        <div class="instance-card-text muted">
          {{ row.text || (row.running ? '正在启动…' : '未开始') }}
        </div>
        <div v-if="!row.running && !row.starting && !row.canStart" class="instance-card-hint">
          参数不完整，点击卡片补全
        </div>
        <div class="instance-card-actions" @click.stop>
          <t-button
            size="small"
            :theme="row.running ? 'danger' : 'primary'"
            variant="outline"
            :loading="row.starting || row.stopping || row.ending"
            :disabled="
              row.starting || row.stopping || row.ending || (!row.running && !row.canStart)
            "
            @click="toggleInstance(row)"
          >
            {{
              row.starting
                ? '启动中'
                : row.stopping || row.ending
                  ? '停止中'
                  : row.running
                    ? '停止'
                    : '启动'
            }}
          </t-button>
          <t-button size="small" variant="text" @click="openInstance(row.id, 'params')">
            参数
          </t-button>
          <t-button size="small" variant="text" @click="openInstance(row.id, 'logs')">
            日志
          </t-button>
          <t-button
            size="small"
            variant="text"
            theme="danger"
            :disabled="
              instances.length <= 1 || row.running || row.starting || row.stopping || row.ending
            "
            @click="removeInstance(row.id)"
          >
            删除
          </t-button>
        </div>
      </t-card>
    </div>

    <div class="history-section">
      <HistoryCard ref="histCard" app-id="defect_review" :format-result="fmtResult" show-dir />
    </div>

    <t-dialog
      v-model:visible="editorVisible"
      :header="editorTitle"
      width="860px"
      top="6vh"
      :footer="false"
      @close="cancelEdit"
    >
      <div class="row" style="margin-bottom: 10px">
        <span class="muted">实例名称</span>
        <t-input v-model="form.instanceName" class="grow" placeholder="例如 胜西线 1" />
      </div>
      <t-tabs v-model:value="editorTab">
        <t-tab-panel value="params" label="参数设置">
          <div class="editor-body">
            <div class="editor-block">
              <div class="sec-title">审核浏览器</div>
              <div class="status-line">
                <span class="status-dot" :class="ready ? 'success' : 'warning'" />
                {{ ready ? '浏览器已就绪，可开始审核' : '浏览器未运行，开始审核会自动启动并登录' }}
              </div>
              <div class="row" style="margin-top: 10px">
                <span class="muted">启动网址</span>
                <t-input
                  v-model="form.startUrl"
                  class="grow"
                  placeholder="可只填站点根地址，例如 http://主机:端口"
                />
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">当前实例账号</span>
                <t-input v-model="form.username" class="grow" autocomplete="username" />
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">当前实例密码</span>
                <t-input
                  v-model="form.password"
                  class="grow"
                  type="password"
                  autocomplete="current-password"
                />
              </div>
              <p class="muted" style="margin: 8px 0 0">
                自动登录：脚本使用本机离线 OCR 识别验证码，登录后自动进入缺陷识别任务；
                掉登录/浏览器重启也会自动恢复。凭据仅保存在本机 config.json。
              </p>
              <div class="row" style="margin-top: 10px">
                <t-switch v-model="form.keepAwake" size="large" />
                <span class="muted">运行时防止关屏/休眠</span>
              </div>
            </div>

            <div class="editor-block">
              <div class="sec-title">审核参数</div>
              <div class="form-grid">
                <div class="row">
                  <span class="muted">任务状态</span>
                  <t-select v-model="form.taskStatus" :options="statusOptions" class="grow" />
                </div>
                <div class="row">
                  <span class="muted">作业性质</span>
                  <t-select v-model="form.missionType" :options="missionOptions" class="grow" />
                </div>
                <div class="row">
                  <span class="muted">电压等级</span>
                  <t-select v-model="form.voltageLevel" :options="voltageOptions" class="grow" />
                </div>
                <div class="row">
                  <span class="muted">线路名称</span>
                  <t-input v-model="form.lineName" class="grow" placeholder="留空=全部线路" />
                </div>
                <div class="row">
                  <span class="muted">开始日期</span>
                  <t-input v-model="form.startDate" class="grow" placeholder="YYYY-MM-DD" />
                </div>
                <div class="row">
                  <span class="muted">结束日期</span>
                  <t-input v-model="form.endDate" class="grow" placeholder="YYYY-MM-DD" />
                </div>
              </div>
              <div class="form-row-fixed" style="margin-top: 10px">
                <span class="muted">输出目录</span>
                <t-input v-model="form.outdir" class="grow" placeholder="留空自动生成运行目录" />
                <t-button variant="outline" @click="pickOutdir">选择目录</t-button>
              </div>
              <div class="form-row-fixed" style="margin-top: 8px">
                <span class="muted">任务跳过表</span>
                <t-input
                  v-model="form.skipTasksFile"
                  class="grow"
                  placeholder="一列 xlsx，首行标题，其余为任务名称；留空使用默认"
                />
                <t-button variant="outline" @click="pickSkipFile">选择文件</t-button>
              </div>
              <t-checkbox v-model="form.ignoreDefectBoxes" style="margin-top: 12px">
                无视缺陷框：不再判定是否有缺陷框，所有能加载的图片都点审核完成
              </t-checkbox>
              <t-checkbox v-model="form.savePhotos" style="margin-top: 8px">
                保存缺陷照片：按「线路名称/杆塔号」归档原图，并另存带缺陷框的「_标注」图片
              </t-checkbox>
            </div>

            <CollapsibleCard v-model:open="advancedOpen" title="高级设置（异常时调整）">
              <div class="row">
                <span class="muted">单步等待</span>
                <t-input-number
                  v-model="form.stepTimeout"
                  :min="10"
                  :max="900"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">动作超时</span>
                <t-input-number
                  v-model="form.actionTimeout"
                  :min="1"
                  :max="120"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">页面跳转超时</span>
                <t-input-number
                  v-model="form.navigationTimeout"
                  :min="5"
                  :max="300"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">登录结果超时</span>
                <t-input-number
                  v-model="form.loginResultTimeout"
                  :min="5"
                  :max="300"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">验证码识别超时</span>
                <t-input-number
                  v-model="form.captchaOcrTimeout"
                  :min="5"
                  :max="300"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">单张图片等待</span>
                <t-input-number
                  v-model="form.imageTimeout"
                  :min="1"
                  :max="300"
                  style="width: 120px"
                />
                <span class="muted">秒</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">加载失败立即重试</span>
                <t-input-number
                  v-model="form.imageRetries"
                  :min="0"
                  :max="10"
                  style="width: 120px"
                />
                <span class="muted">次</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">未审核数阈值</span>
                <t-input-number
                  v-model="form.maxUnreviewed"
                  :min="1"
                  :max="100000"
                  style="width: 120px"
                />
                <span class="muted">张，超过则跳过任务并记异常</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">卡死检测</span>
                <t-input-number
                  v-model="form.stallTimeout"
                  :min="30"
                  :max="3600"
                  style="width: 120px"
                />
                <span class="muted">秒无活动则重启浏览器</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">任务后刷新</span>
                <t-switch v-model="form.reloadAfterTask" size="large" />
                <span class="muted">释放页面内存，降低 OOM</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">慢操作阈值</span>
                <t-input-number
                  v-model="form.slowImageSeconds"
                  :min="5"
                  :max="300"
                  style="width: 120px"
                />
                <span class="muted">秒/张</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">连续慢操作</span>
                <t-input-number
                  v-model="form.slowImageCount"
                  :min="2"
                  :max="20"
                  style="width: 120px"
                />
                <span class="muted">次后重启当前实例</span>
              </div>
              <div class="row" style="margin-top: 8px">
                <span class="muted">立即重启未审核阈值</span>
                <t-input-number
                  v-model="form.slowRestartUnreviewed"
                  :min="0"
                  :max="100000"
                  style="width: 120px"
                />
                <span class="muted">张；超过则过慢时立即重启，0=仅任务边界重启</span>
              </div>
            </CollapsibleCard>
          </div>
        </t-tab-panel>
        <t-tab-panel value="logs" label="运行日志">
          <div class="editor-logs">
            <TaskResult
              :outcome="editorRuntime.outcome"
              :logs="editorRuntime.logs"
              default-expanded
            />
          </div>
        </t-tab-panel>
      </t-tabs>
      <div class="editor-footer">
        <t-button variant="text" theme="warning" @click="resetInstanceTask(editorId)">
          重置任务
        </t-button>
        <span class="muted">{{ editorRuntime.stage }}</span>
        <div class="grow" />
        <t-button variant="outline" @click="cancelEdit">取消</t-button>
        <t-button theme="primary" :loading="editorSaving" @click="saveEdit">保存</t-button>
      </div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, markRaw, onActivated, onMounted, onUnmounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, pickDirectory, pickFile, runJob } from '../api';
import CollapsibleCard from '../components/CollapsibleCard.vue';
import HistoryCard from '../components/HistoryCard.vue';
import TaskResult from '../components/TaskResult.vue';
import { friendlyTaskError } from '../lib/task-result.mjs';
import { makeInstance } from '../lib/instances.mjs';

const statusOptions = [
  '待人工审核',
  '审核完成',
  '审核驳回',
  '任务完成',
  '任务已终止',
  '工单终止',
].map((value) => ({ label: value, value }));
const missionOptions = [
  { label: '全部', value: '' },
  '仿线巡检',
  '精细化巡检',
  '线路交跨巡检',
  '应急巡检',
  '正射巡检',
  '红外巡检',
  '固定翼巡检',
  '通道巡检',
  '树障巡检',
  '特殊巡视',
  '联合巡视',
].map((value) => (typeof value === 'string' ? { label: value, value } : value));
const voltageOptions = [
  { label: '全部', value: '' },
  '交流110kV',
  '交流220kV',
  '交流35kV',
  '交流500kV',
  '交流1000kV',
  '±800kV地',
  '直流500kV',
  '直流660kV',
  '直流800kV',
  '直流1000kV',
].map((value) => (typeof value === 'string' ? { label: value, value } : value));

const form = reactive({
  instanceName: '',
  startUrl: '',
  taskStatus: '待人工审核',
  missionType: '',
  voltageLevel: '',
  lineName: '',
  startDate: '',
  endDate: '',
  outdir: '',
  skipTasksFile: '',
  ignoreDefectBoxes: false,
  savePhotos: true,
  keepAwake: false,
  username: '',
  password: '',
  stepTimeout: 120,
  actionTimeout: 5,
  navigationTimeout: 15,
  loginResultTimeout: 20,
  captchaOcrTimeout: 30,
  imageTimeout: 10,
  imageRetries: 0,
  maxUnreviewed: 400,
  stallTimeout: 120,
  reloadAfterTask: true,
  slowImageSeconds: 15,
  slowImageCount: 3,
  slowRestartUnreviewed: 50,
});
const st = reactive({ connected: false, playwright: false, pages: 0 });
const instances = ref([]);
const activeId = ref('');
const editorVisible = ref(false);
const editorId = ref('');
const editorTab = ref('params');
const editorSaving = ref(false);
const startingAll = ref(false);
const stoppingAll = ref(false);
const advancedOpen = ref(false);
const histCard = ref(null);
const runtimes = reactive({});
const progressWindowOpen = ref(false);
let offProgressWindowState = null;

const editorInstance = computed(
  () => instances.value.find((item) => item.id === editorId.value) || null
);
const editorRuntime = computed(() => runtimeFor(editorId.value || activeId.value));
const editorTitle = computed(() =>
  editorInstance.value?.name ? `实例设置 · ${editorInstance.value.name}` : '实例设置'
);
const ready = computed(() => st.connected && st.playwright);

const instanceBoard = computed(() =>
  instances.value.map((item) => {
    const rt = runtimeFor(item.id);
    const status = rt.outcome?.status || '';
    let stateLabel = '未开始';
    let theme = 'default';
    if (rt.starting) {
      stateLabel = '启动中';
      theme = 'warning';
    } else if (rt.stopping || rt.ending) {
      stateLabel = '停止中';
      theme = 'warning';
    } else if (rt.running) {
      stateLabel = '运行中';
      theme = 'success';
    } else if (rt.outcome?.failed) {
      stateLabel = '失败';
      theme = 'danger';
    } else if (status === '完成') {
      stateLabel = '已完成';
      theme = 'success';
    } else if (status === '已停止' || status === '已重置') {
      stateLabel = status;
    }
    return {
      id: item.id,
      name: item.name || item.id,
      url: item.start_url || item.params?.start_url || '',
      pct: rt.progress?.pct || 0,
      text: rt.progress?.text || '',
      stage: rt.stage || '未开始',
      running: !!rt.running,
      starting: !!rt.starting,
      stopping: !!rt.stopping,
      ending: !!rt.ending,
      failed: !!rt.outcome?.failed,
      theme,
      stateLabel,
      canStart: instanceCanStart(item),
    };
  })
);
const runningCount = computed(() => instanceBoard.value.filter((row) => row.running).length);
const startableCount = computed(() => instanceBoard.value.filter((row) => row.canStart).length);
const canStartAll = computed(
  () => !!st.playwright && startableCount.value > 0 && !startingAll.value && !stoppingAll.value
);
function runtimeFor(id) {
  const key = id || 'default';
  if (!runtimes[key]) {
    runtimes[key] = {
      logs: [],
      progress: { pct: 0, text: '', current: 0, total: 0 },
      outcome: { status: '尚未开始', summary: '', failed: false },
      stage: '未开始',
      running: false,
      starting: false,
      stopping: false,
      ending: false,
      stopRequested: false,
      job: null,
    };
  }
  return runtimes[key];
}

function countOf(v) {
  if (Array.isArray(v)) return v.length;
  return typeof v === 'number' ? v : 0;
}

function payload() {
  return {
    start_url: form.startUrl.trim(),
    task_status: form.taskStatus,
    mission_type: form.missionType,
    voltage_level: form.voltageLevel,
    line_name: form.lineName.trim(),
    start_date: form.startDate.trim(),
    end_date: form.endDate.trim(),
    outdir: form.outdir.trim(),
    skip_tasks_file: form.skipTasksFile.trim(),
    ignore_defect_boxes: !!form.ignoreDefectBoxes,
    save_photos: !!form.savePhotos,
    keep_awake: !!form.keepAwake,
    auto_login: true,
    username: form.username.trim(),
    password: form.password,
    reload_after_task: !!form.reloadAfterTask,
    step_timeout: Number(form.stepTimeout) || 120,
    action_timeout: Number(form.actionTimeout) || 5,
    navigation_timeout: Number(form.navigationTimeout) || 15,
    login_result_timeout: Number(form.loginResultTimeout) || 20,
    captcha_ocr_timeout: Number(form.captchaOcrTimeout) || 30,
    stall_timeout: Number(form.stallTimeout) || 120,
    slow_image_seconds: Number(form.slowImageSeconds) || 15,
    slow_image_count: Number(form.slowImageCount) || 3,
    slow_restart_unreviewed: Number(form.slowRestartUnreviewed) || 0,
    image_timeout: Number(form.imageTimeout) || 10,
    image_retries: Number(form.imageRetries) || 0,
    max_unreviewed: Number(form.maxUnreviewed) || 400,
  };
}

function applyInstance(instance) {
  const data = instance || {};
  const params = data.params || {};
  form.instanceName = data.name || '实例 1';
  form.startUrl = data.start_url || params.start_url || '';
  form.taskStatus = params.task_status || '待人工审核';
  form.missionType = params.mission_type ?? '';
  form.voltageLevel = params.voltage_level ?? '';
  form.lineName = params.line_name || '';
  form.startDate = params.start_date || '';
  form.endDate = params.end_date || '';
  form.outdir = params.outdir || '';
  form.skipTasksFile = params.skip_tasks_file || '';
  form.ignoreDefectBoxes = !!params.ignore_defect_boxes;
  form.savePhotos = params.save_photos !== false;
  form.keepAwake = !!params.keep_awake;
  form.username = params.username || '';
  form.password = params.password || '';
  form.stepTimeout = Number(params.step_timeout) || 120;
  form.actionTimeout = Number(params.action_timeout) || 5;
  form.navigationTimeout = Number(params.navigation_timeout) || 15;
  form.loginResultTimeout = Number(params.login_result_timeout) || 20;
  form.captchaOcrTimeout = Number(params.captcha_ocr_timeout) || 30;
  form.imageTimeout = Number(params.image_timeout) || 10;
  form.imageRetries = Number(params.image_retries) || 0;
  form.maxUnreviewed = Number(params.max_unreviewed) || 400;
  form.stallTimeout = Number(params.stall_timeout) || 120;
  form.reloadAfterTask = params.reload_after_task !== false;
  form.slowImageSeconds = Number(params.slow_image_seconds) || 15;
  form.slowImageCount = Number(params.slow_image_count) || 3;
  form.slowRestartUnreviewed = Number(params.slow_restart_unreviewed) || 0;
}

function activeInstance() {
  return instances.value.find((item) => item.id === activeId.value) || null;
}

function applyConfig(raw) {
  const app = raw || {};
  const list = Array.isArray(app.instances) ? app.instances : [];
  if (list.length) {
    instances.value = list;
  } else {
    instances.value = [
      {
        id: 'default',
        name: '实例 1',
        start_url: app.start_url || '',
        cdp_url: 'http://127.0.0.1:9400',
        profile_dir: '',
        params: app,
      },
    ];
  }
  activeId.value = instances.value[0].id;
  applyInstance(instances.value[0]);
}

async function loadStatus() {
  st.connected = false;
  try {
    const instance = activeInstance();
    const s =
      (await call('getAppStatus', {
        app_id: 'defect_review',
        instance_id: activeId.value,
        instance,
      })) || {};
    st.connected = !!s.connected;
    st.playwright = !!s.playwright;
    st.pages = countOf(s.pages);
    if (s.start_url && !form.startUrl) form.startUrl = s.start_url;
  } catch (_e) {
    /* 状态读取失败不阻断表单 */
  }
}

async function loadConfig() {
  try {
    const cfg = await call('getConfig');
    applyConfig((cfg || {}).defect_review || {});
  } catch (_e) {
    /* ignore */
  }
}

async function saveConfig() {
  const data = payload();
  await call('saveConfig', {
    defect_review: {
      ...data,
      instances: instances.value,
    },
  });
}

function openInstance(id, tab = 'params') {
  const instance = instances.value.find((item) => item.id === id);
  if (!instance) return;
  activeId.value = id;
  applyInstance(instance);
  editorId.value = id;
  editorTab.value = tab;
  editorVisible.value = true;
  loadStatus();
}

function cancelEdit() {
  const instance = instances.value.find((item) => item.id === editorId.value);
  if (instance) applyInstance(instance);
  editorVisible.value = false;
}

async function saveEdit() {
  const index = instances.value.findIndex((item) => item.id === editorId.value);
  if (index < 0) {
    editorVisible.value = false;
    return;
  }
  editorSaving.value = true;
  try {
    const data = payload();
    const name = form.instanceName.trim() || instances.value[index].name;
    instances.value[index] = {
      ...instances.value[index],
      name,
      start_url: data.start_url,
      params: data,
    };
    form.instanceName = name;
    activeId.value = editorId.value;
    await saveConfig();
    MessagePlugin.success('实例参数已保存');
    editorVisible.value = false;
  } catch (e) {
    MessagePlugin.error(`保存失败：${String(e.message || e)}`);
  } finally {
    editorSaving.value = false;
  }
}

async function addInstance() {
  const params = payload();
  const instance = makeInstance('defect_review', instances.value, undefined, 9400);
  instance.start_url = form.startUrl;
  instance.params = { ...params, start_url: form.startUrl };
  instances.value.push(instance);
  activeId.value = instance.id;
  applyInstance(instance);
  try {
    await saveConfig();
    openInstance(instance.id, 'params');
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function pickOutdir() {
  const path = await pickDirectory(form.outdir || undefined);
  if (path) form.outdir = path;
}

async function pickSkipFile() {
  const path = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }]);
  if (path) form.skipTasksFile = path;
}

function appendLog(rt, line) {
  rt.logs.push(line);
  if (rt.logs.length > 2000) rt.logs.splice(0, rt.logs.length - 2000);
}

function fmtResult(r) {
  if (!r) return '';
  if (r.cancelled) return '已停止';
  const photos = r.photos_saved
    ? `；缺陷照片 ${r.photos_saved}（标注 ${r.photos_marked ?? 0}）`
    : '';
  return `完成任务 ${r.completed ?? 0}；审核图片 ${r.images ?? 0}；转人工 ${r.deferred ?? 0}${photos}`;
}

function outcomeFromResult(job) {
  const r = job.result || {};
  if (r.cancelled) {
    return {
      status: '已停止',
      summary: `已停止，产物目录：${r.dir || r.outdir || ''}`,
      failed: false,
    };
  }
  const status = job.status === 'success' ? '完成' : job.status === 'cancelled' ? '已停止' : '失败';
  return {
    status,
    summary:
      `${fmtResult(r)}；输出目录：${r.outdir || r.dir || ''}` +
      (r.button_not_ready_images ? `；按钮未就绪 ${r.button_not_ready_images}` : ''),
    failed: job.status === 'failed',
    artifact: r.outdir || r.dir ? { path: r.outdir || r.dir, label: '打开输出目录' } : null,
  };
}

function instanceParams(instance, extra = {}) {
  return {
    ...((instance && instance.params) || {}),
    ...extra,
    instance_id: instance?.id || 'default',
    instance_name: instance?.name || form.instanceName,
    instance: instance ? { ...instance, params: { ...(instance.params || {}) } } : undefined,
  };
}

function browserWindowBounds(count) {
  if (!count) return [];
  const availLeft = Number(window.screen?.availLeft) || 0;
  const availTop = Number(window.screen?.availTop) || 0;
  const availWidth = Number(window.screen?.availWidth) || Number(window.innerWidth) || 1280;
  const availHeight = Number(window.screen?.availHeight) || Number(window.innerHeight) || 720;
  let cols;
  let rows;
  if (count <= 3) {
    cols = count;
    rows = 1;
  } else if (count === 4) {
    cols = 2;
    rows = 2;
  } else {
    cols = Math.ceil(Math.sqrt(count));
    rows = Math.ceil(count / cols);
  }
  const gap = 6;
  const width = Math.max(320, Math.floor((availWidth - gap * (cols - 1)) / cols));
  const height = Math.max(240, Math.floor((availHeight - gap * (rows - 1)) / rows));
  return Array.from({ length: count }, (_unused, index) => ({
    left: availLeft + (index % cols) * (width + gap),
    top: availTop + Math.floor(index / cols) * (height + gap),
    width,
    height,
  }));
}

function instanceCanStart(instance) {
  if (!instance) return false;
  const rt = runtimeFor(instance.id);
  if (rt.starting || rt.running || rt.stopping || rt.ending) return false;
  const params = instance.params || {};
  if (!params.start_date || !params.end_date) return false;
  const startUrl = params.start_url || instance.start_url || '';
  return !!(startUrl && params.username && params.password);
}

async function startInstance(id, { bounds = null } = {}) {
  const rt = runtimeFor(id);
  if (rt.starting || rt.running || rt.stopping || rt.ending) return null;
  const instance = instances.value.find((item) => item.id === id);
  if (!instance) throw new Error('实例不存在');
  if (!instanceCanStart(instance)) {
    throw new Error('实例参数不完整，请先打开参数设置补全网址、账号密码和日期');
  }
  rt.starting = true;
  rt.stopRequested = false;
  try {
    const previous = rt.job;
    if (previous) await previous.finished;
    rt.logs = [];
    rt.outcome = { status: '执行中', summary: '正在审核…', failed: false };
    rt.stage = '准备启动';
    rt.progress.pct = 0;
    rt.progress.text = '';
    rt.progress.current = 0;
    rt.progress.total = 0;
    rt.running = true;
    rt.stopping = false;
    rt.ending = false;
    let job;
    job = runJob(
      'defect_review',
      instanceParams(instance, bounds ? { window_bounds: bounds } : {}),
      {
        onLog: (line) => appendLog(rt, line),
        onProgress: (p) => {
          rt.progress.current = Number(p.current) || 0;
          rt.progress.total = Number(p.total) || 0;
          rt.progress.pct = p.total > 0 ? Math.round((p.current * 100) / p.total) : 0;
          rt.progress.text =
            p.total > 0 ? `${p.label || ''} ${p.current}/${p.total}` : p.label || '';
          if (!rt.stopping) {
            rt.stage = `${p.label || '处理中'}（${p.current}/${p.total}）`;
          }
        },
        onDone: (doneJob) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.stopping = false;
          rt.ending = false;
          rt.stopRequested = false;
          if (doneJob.status === 'success') rt.progress.pct = 100;
          rt.outcome = outcomeFromResult(doneJob);
          rt.stage = rt.outcome.status;
          if (id === activeId.value) loadStatus();
          histCard.value && histCard.value.reload();
        },
        onError: (e) => {
          if (rt.job !== job) return;
          rt.job = null;
          rt.running = false;
          rt.stopping = false;
          rt.ending = false;
          rt.stopRequested = false;
          const message = String(e.message || e);
          rt.logs.push(message);
          rt.outcome = {
            status: '启动失败',
            summary: message.includes('该实例已有任务在运行')
              ? '该实例后台仍有任务在退出，可点「重置任务」后再开始，无需重启客户端'
              : friendlyTaskError(message),
            failed: true,
          };
          rt.stage = '启动失败';
          if (id === activeId.value) loadStatus();
        },
      }
    );
    rt.job = markRaw(job);
    if (rt.stopRequested) {
      rt.stopRequested = false;
      await stopInstance(id, { force: true });
    }
    return job;
  } catch (e) {
    rt.running = false;
    rt.stopping = false;
    rt.ending = false;
    rt.stopRequested = false;
    const message = String(e.message || e);
    rt.logs.push(message);
    rt.outcome = {
      status: '启动失败',
      summary: message.includes('该实例已有任务在运行')
        ? '该实例后台仍有任务在退出，可点「重置任务」后再开始，无需重启客户端'
        : friendlyTaskError(message),
      failed: true,
    };
    rt.stage = '启动失败';
    return null;
  } finally {
    rt.starting = false;
  }
}

async function stopInstance(id, { force = false } = {}) {
  const rt = runtimeFor(id);
  const job = rt.job;
  if (!job) {
    if (rt.starting) {
      rt.stopRequested = true;
      rt.stopping = true;
      rt.stage = '正在停止';
      return true;
    }
    return false;
  }
  if (rt.stopping && !force) return false;
  rt.stopping = true;
  rt.ending = false;
  rt.stage = '正在停止';
  try {
    await job.cancel();
    if (rt.job === job) {
      rt.stopping = false;
      rt.ending = true;
      rt.stage = '正在停止（等待后台退出）';
    }
    return true;
  } catch (e) {
    if (rt.job === job) {
      rt.stopping = false;
      rt.ending = false;
      rt.stage = '停止失败，任务仍在执行';
    }
    MessagePlugin.error(`停止失败：${String(e.message || e)}`);
    return false;
  }
}

async function toggleInstance(row) {
  if (row.starting || row.stopping || row.ending) return;
  if (row.running) {
    await stopInstance(row.id);
    return;
  }
  if (!st.playwright) {
    MessagePlugin.error('当前客户端未内置 Playwright，无法启动审核');
    return;
  }
  try {
    await startInstance(row.id);
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function startAllInstances() {
  if (startingAll.value) return;
  if (!st.playwright) {
    MessagePlugin.error('当前客户端未内置 Playwright，无法启动审核');
    return;
  }
  const startable = instances.value.filter(instanceCanStart);
  if (!startable.length) {
    MessagePlugin.warning('没有可启动的实例，请先补全各实例的网址、账号密码和日期');
    return;
  }
  startingAll.value = true;
  let started = 0;
  try {
    const boundsList = browserWindowBounds(startable.length);
    for (const [index, instance] of startable.entries()) {
      try {
        const job = await startInstance(instance.id, {
          bounds: boundsList[index],
        });
        if (job) started += 1;
      } catch (e) {
        MessagePlugin.error(`实例「${instance.name}」启动失败：${String(e.message || e)}`);
      }
    }
    const skipped = instances.value.length - startable.length;
    const message =
      `已启动 ${started} 个实例` + (skipped ? `，${skipped} 个未启动（参数不完整或已在运行）` : '');
    if (started) MessagePlugin.success(message);
    else MessagePlugin.warning('没有实例成功启动，请查看各实例日志');
  } finally {
    startingAll.value = false;
  }
}

async function stopAllInstances() {
  if (stoppingAll.value) return;
  const targets = instances.value.filter((item) => {
    const rt = runtimeFor(item.id);
    return rt.running || rt.starting;
  });
  if (!targets.length) {
    MessagePlugin.info('当前没有运行中的实例');
    return;
  }
  const dialog = DialogPlugin.confirm({
    header: '全部停止',
    body: `将停止 ${targets.length} 个实例的审核任务；已经提交的图片审核不会回滚。确定继续？`,
    theme: 'warning',
    confirmBtn: '全部停止',
    onConfirm: async () => {
      dialog.hide();
      stoppingAll.value = true;
      try {
        for (const item of targets) await stopInstance(item.id);
      } finally {
        stoppingAll.value = false;
      }
    },
  });
}

async function resetInstanceTask(id = editorId.value || activeId.value) {
  const targetId = id || activeId.value;
  const rt = runtimeFor(targetId);
  const instance = instances.value.find((item) => item.id === targetId);
  let active;
  try {
    active = await call('getActiveJob', {
      app_id: 'defect_review',
      instance_id: targetId,
    });
  } catch (e) {
    MessagePlugin.error(`读取实例任务失败：${String(e.message || e)}`);
    return;
  }
  if (!active || !active.id) {
    MessagePlugin.info('该实例当前没有后台任务');
    rt.job = null;
    rt.running = false;
    rt.stopping = false;
    rt.ending = false;
    rt.stopRequested = false;
    rt.stage = '实例空闲';
    return;
  }
  const dialog = DialogPlugin.confirm({
    header: '重置实例任务',
    body: `将取消${instance ? `「${instance.name}」` : '该实例'}的后台任务 ${
      active.id
    }（${active.status}），不影响其他实例。确定继续？`,
    theme: 'warning',
    confirmBtn: '重置',
    onConfirm: async () => {
      dialog.hide();
      rt.stage = '正在重置实例任务';
      try {
        await call('cancelJob', { job_id: active.id });
        for (let attempt = 0; attempt < 40; attempt += 1) {
          await new Promise((resolve) => setTimeout(resolve, 500));
          const job = await call('getJob', { job_id: active.id });
          if (!job || ['success', 'failed', 'cancelled'].includes(job.status)) {
            rt.job = null;
            rt.running = false;
            rt.stopping = false;
            rt.ending = false;
            rt.stopRequested = false;
            rt.progress.pct = 0;
            rt.progress.text = '';
            rt.progress.current = 0;
            rt.progress.total = 0;
            rt.stage = '实例已重置';
            rt.outcome = {
              status: '已重置',
              summary: '已取消该实例后台残留任务，不影响其他实例',
              failed: false,
            };
            MessagePlugin.success('实例任务已重置');
            loadStatus();
            histCard.value && histCard.value.reload();
            return;
          }
        }
        MessagePlugin.warning('后台任务仍未退出，请稍后再次重置或检查该实例浏览器');
      } catch (e) {
        MessagePlugin.error(`重置任务失败：${String(e.message || e)}`);
      }
    },
  });
}

function removeInstance(id) {
  if (instances.value.length <= 1) {
    MessagePlugin.warning('至少保留一个实例');
    return;
  }
  const target = instances.value.find((item) => item.id === id);
  if (!target) return;
  const rt = runtimeFor(id);
  if (rt.running || rt.starting || rt.stopping || rt.ending) {
    MessagePlugin.warning('实例正在运行，请先停止再删除');
    return;
  }
  const dialog = DialogPlugin.confirm({
    header: '删除实例',
    body: `确定删除「${target.name}」？不会关闭已经打开的浏览器，也不会删除登录目录。`,
    theme: 'warning',
    confirmBtn: '删除',
    onConfirm: async () => {
      dialog.hide();
      const index = instances.value.findIndex((item) => item.id === id);
      instances.value.splice(index, 1);
      delete runtimes[id];
      if (editorId.value === id) {
        editorId.value = '';
        editorVisible.value = false;
      }
      if (activeId.value === id) {
        const next = instances.value[Math.max(0, index - 1)] || instances.value[0];
        activeId.value = next.id;
        applyInstance(next);
      }
      try {
        await saveConfig();
        await loadStatus();
      } catch (e) {
        MessagePlugin.error(String(e.message || e));
      }
    },
  });
}

async function refreshProgressWindowState() {
  const api = window.core && window.core.defectProgress;
  if (!api || !api.getState) return;
  try {
    const state = await api.getState();
    progressWindowOpen.value = !!(state && state.open);
  } catch (_e) {
    /* 浮窗状态读取失败不阻断页面 */
  }
}

async function toggleProgressWindow() {
  const api = window.core && window.core.defectProgress;
  if (!api) {
    MessagePlugin.error('当前客户端版本不支持进度浮窗');
    return;
  }
  try {
    const state = progressWindowOpen.value ? await api.close() : await api.open();
    progressWindowOpen.value = !!(state && state.open);
  } catch (e) {
    MessagePlugin.error(`进度浮窗操作失败：${String(e.message || e)}`);
  }
}

onMounted(async () => {
  const api = window.core && window.core.defectProgress;
  if (api && api.onState) {
    offProgressWindowState = api.onState((open) => {
      progressWindowOpen.value = !!open;
    });
  }
  await loadConfig();
  await loadStatus();
  await refreshProgressWindowState();
});
onActivated(() => {
  loadStatus();
  refreshProgressWindowState();
});
onUnmounted(() => {
  if (offProgressWindowState) offProgressWindowState();
  offProgressWindowState = null;
});
</script>

<style scoped>
.overview-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.overview-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.instance-status-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
.instance-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 999px;
  background: transparent;
  color: var(--tk-sub);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
}
.instance-status:hover {
  border-color: var(--tk-brand);
}
.instance-status-name {
  color: var(--tk-ink);
  font-weight: 500;
}
.instance-status.success {
  border-color: rgba(0, 180, 42, 0.4);
}
.instance-status.warning {
  border-color: rgba(255, 125, 0, 0.4);
}
.instance-status.danger {
  border-color: rgba(245, 63, 63, 0.4);
}
.instance-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-top: 12px;
}
.instance-grid .instance-card {
  margin-top: 0;
}
.instance-card {
  height: 100%;
  cursor: pointer;
  border: 1px solid transparent;
  transition:
    box-shadow 0.18s ease,
    border-color 0.18s ease;
}
.instance-card :deep(.t-card__body) {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.instance-card:hover {
  box-shadow: 0 8px 20px rgba(0, 0, 0, 0.08);
}
.instance-card.running {
  border-color: rgba(0, 180, 42, 0.35);
}
.instance-card.failed {
  border-color: rgba(245, 63, 63, 0.35);
}
.instance-card-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.instance-card-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--tk-ink);
  font-size: 14px;
  font-weight: 600;
}
.instance-state {
  flex: 0 0 auto;
  color: var(--tk-sub);
  font-size: 12px;
}
.instance-card-url {
  margin: 6px 0 8px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.instance-card-stage {
  margin-top: 6px;
  overflow: hidden;
  color: var(--tk-ink);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.instance-card-text {
  min-height: 17px;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.instance-card-hint {
  margin-top: 4px;
  color: var(--tk-yellow);
  font-size: 12px;
}
.instance-card-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-top: auto;
  padding-top: 8px;
  border-top: 1px solid var(--tk-line);
}
.history-section {
  margin-top: 16px;
}
.instance-card-actions .t-button:first-child {
  margin-right: auto;
}
.editor-body {
  max-height: 58vh;
  overflow-y: auto;
  padding-right: 6px;
}
.editor-block + .editor-block {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--tk-line);
}
.editor-logs {
  max-height: 58vh;
  overflow-y: auto;
}
.editor-logs :deep(.logview) {
  max-height: 50vh;
}
.editor-footer {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--tk-line);
}
.editor-footer .grow {
  flex: 1;
}
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 18px;
}
.form-grid .row {
  margin: 0;
}
.form-grid .row > .muted {
  flex: 0 0 80px;
  white-space: nowrap;
}
.form-row-fixed {
  display: grid;
  grid-template-columns: 80px minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
}
@media (max-width: 1100px) {
  .instance-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 700px) {
  .instance-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
