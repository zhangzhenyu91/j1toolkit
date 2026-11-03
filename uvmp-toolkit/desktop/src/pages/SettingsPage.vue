<template>
  <div>
    <label class="settings-search">
      <ShadeIcon name="search" :size="15" />
      <input v-model="search" type="text" placeholder="搜索设置：组名、字段、说明或关键词" />
    </label>
    <div v-if="!visibleCount" class="settings-empty">
      未找到匹配的设置，请修改关键词或清空搜索。
    </div>

    <CollapsibleCard
      v-if="visible('account')"
      title="账号 · 派车系统"
      :open="sectionOpen.account"
      @update:open="sectionOpen.account = $event"
    >
      <p class="muted" style="margin-top: 0">
        派车系统的登录账号密码。只保存在本机配置文件，不用于其他用途；密码留空表示不修改。
      </p>
      <div class="row" style="margin-top: 10px">
        <span class="muted">账号</span>
        <t-input v-model="sso.username" class="grow" placeholder="派车系统登录账号" />
      </div>
      <div class="row" style="margin-top: 8px">
        <span class="muted">密码</span>
        <t-input v-model="sso.password" type="password" class="grow" placeholder="留空则不修改" />
        <t-button theme="default" :loading="saving" @click="saveSso">保存账号</t-button>
      </div>
      <div class="muted" style="margin-top: 8px">{{ pwdHint }}</div>
    </CollapsibleCard>

    <CollapsibleCard
      v-if="visible('browser')"
      title="宿主浏览器"
      :open="sectionOpen.browser"
      @update:open="sectionOpen.browser = $event"
    >
      <p class="muted" style="margin-top: 0">
        用于生成派车单和轨迹 PDF。通常保持自动检测即可；无法识别时，手动选择公司浏览器。
      </p>
      <div class="row" style="margin-top: 10px">
        <t-input
          v-model="browserPath"
          class="grow"
          placeholder="留空 = 自动检测宿主浏览器；也可用内置 Electron 兜底"
        />
        <t-button variant="outline" @click="pickBrowser">浏览…</t-button>
        <t-button variant="outline" @click="autoDetect">自动检测</t-button>
        <t-button theme="default" :loading="saving" @click="saveBrowser">保存浏览器</t-button>
      </div>
      <div class="muted" style="margin-top: 8px">
        当前生效：{{ effectiveChrome || '自动检测 / 内置 Electron（不依赖宿主浏览器）' }}
      </div>
    </CollapsibleCard>

    <CollapsibleCard
      v-if="visible('export-background')"
      title="导出与后台"
      :open="sectionOpen['export-background']"
      @update:open="sectionOpen['export-background'] = $event"
    >
      <p class="muted" style="margin-top: 0">
        轨迹导出 Python 导出引擎的并发度（查询/渲染均
        1..16，超出由引擎钳制），对下次导出任务生效。32GB 内存推荐：8–10 核用 查询 8 / 渲染 6；12
        核以上用 12 / 8；上限均 16，16 路渲染需确认内存与 CPU 余量。
      </p>
      <div class="row" style="margin-top: 10px">
        <span class="muted">查询并发</span>
        <t-input-number
          v-model="workers.query"
          :min="1"
          :max="16"
          theme="column"
          style="width: 120px"
        />
        <span class="muted">渲染并发</span>
        <t-input-number
          v-model="workers.render"
          :min="1"
          :max="16"
          theme="column"
          style="width: 120px"
        />
        <t-button theme="default" :loading="saving" @click="saveWorkers">保存并发设置</t-button>
      </div>
      <div class="settings-divider" />
      <div class="row">
        <t-switch v-model="ui.closeToTray" size="large" />
        <span class="muted">关闭窗口时最小化到托盘（驻留后台；托盘图标右键「退出」才真退出）</span>
      </div>
      <div class="row" style="margin-top: 8px">
        <t-switch v-model="ui.autostart" size="large" />
        <span class="muted">开机自动启动（登录后驻留托盘）</span>
      </div>
      <div class="row" style="margin-top: 10px">
        <t-button theme="default" :loading="saving" @click="saveUi">保存后台设置</t-button>
      </div>
    </CollapsibleCard>

    <CollapsibleCard
      v-if="visible('shortcuts-about')"
      title="快捷键与关于"
      :open="sectionOpen['shortcuts-about']"
      @update:open="sectionOpen['shortcuts-about'] = $event"
    >
      <div class="kv">
        <span class="k">工具搜索</span><span>Ctrl+K</span> <span class="k">打开设置</span
        ><span>Ctrl+,</span> <span class="k">排错日志</span
        ><span>在任务页或执行历史中按需展开</span>
      </div>
      <div class="settings-divider" />
      <div class="about-block">
        <strong>Shade 壹匣 · 内网工具箱</strong>
        <div class="muted">
          版本：v{{ version || '开发构建' }}；Electron + Vue + Python 桌面客户端
        </div>
        <div class="muted">
          品牌位使用官方 Shade Logo；功能图标使用统一线性图标集。Noto Sans SC
          等第三方许可随安装包提供。
        </div>
      </div>
    </CollapsibleCard>

    <CollapsibleCard
      v-if="visible('developer-diagnostics')"
      title="开发诊断"
      :open="sectionOpen['developer-diagnostics']"
      @update:open="sectionOpen['developer-diagnostics'] = $event"
    >
      <div class="row">
        <t-button variant="outline" :loading="testing === 'render'" @click="runSelftest('render')">
          检查 PDF 渲染
        </t-button>
        <t-button variant="outline" :loading="testing === 'sso'" @click="runSelftest('sso')">
          检查派车系统登录
        </t-button>
        <t-button variant="outline" :loading="testing === 'cdp'" @click="runSelftest('cdp')">
          CDP 渲染自检
        </t-button>
        <span v-if="testing" class="muted">自检运行中…</span>
      </div>
      <div v-if="testResult" class="row" style="margin-top: 10px">
        <t-tag :theme="testResult.ok ? 'success' : 'danger'" variant="light-outline">
          {{ testResult.ok ? '通过' : '失败' }}
        </t-tag>
        <span class="muted">{{ testResult.text }}</span>
      </div>
      <div v-if="resultPaths.length" class="row" style="margin-top: 6px">
        <span class="muted">自检产物：</span>
        <span v-for="p in resultPaths" :key="p" class="muted">
          {{ p }}
          <t-button size="small" variant="text" @click="showItem(p)">打开</t-button>
        </span>
      </div>
      <div class="logview" style="margin-top: 10px; max-height: 160px">
        {{ testLogs.join('\n') }}
      </div>
      <div class="settings-divider" />
      <div class="row">
        <t-switch v-model="devMock" size="large" />
        <span class="muted"
          >开发模式：使用模拟派车数据（dev.mock；无派车系统网络时调试用，生产环境勿开）</span
        >
        <t-button variant="outline" @click="saveDev">保存开发设置</t-button>
      </div>
      <div class="settings-divider" />
      <div class="sec-sub">运行环境与路径</div>
      <div class="kv">
        <span class="k">配置文件</span><span>{{ info.config_path }}</span>
        <span class="k">状态目录</span>
        <span>
          {{ info.state_dir }}
          <t-button size="small" variant="text" @click="showItem(info.state_dir)">打开</t-button>
        </span>
        <span class="k">平台</span><span>{{ info.platform }} / {{ info.machine }}</span>
        <span class="k">生效浏览器</span><span>{{ effectiveChrome || '内置 Electron' }}</span>
      </div>
      <div class="sec-sub">日志占用与清理</div>
      <div class="kv">
        <span class="k">日志目录</span>
        <span>
          {{ logStats.dir || '-' }}
          <t-button v-if="logStats.dir" size="small" variant="text" @click="showItem(logStats.dir)"
            >打开目录</t-button
          >
        </span>
        <span class="k">占用</span
        ><span>{{ logStats.files }} 个文件 / {{ fmtBytes(logStats.bytes) }}</span>
      </div>
      <div class="row" style="margin-top: 10px">
        <t-button theme="danger" variant="outline" :loading="clearing" @click="clearLogs"
          >一键清除</t-button
        >
        <span class="muted">30 天前的任务日志在核心启动时自动清理</span>
      </div>
    </CollapsibleCard>
  </div>
</template>

<script setup>
import { computed, onActivated, reactive, ref, watch } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call, pickFile, runJob, showItem } from '../api';
import ShadeIcon from '../components/ShadeIcon.vue';
import CollapsibleCard from '../components/CollapsibleCard.vue';
import { matchedSettingsIDs } from '../lib/navigation.mjs';

const search = ref('');
const searching = computed(() => search.value.trim() !== '');
const matched = computed(() => matchedSettingsIDs(search.value));
const visibleCount = computed(() => matched.value.length);
const visible = (id) => matched.value.includes(id);
const sectionOpen = reactive({
  account: true,
  browser: false,
  'export-background': false,
  'shortcuts-about': false,
  'developer-diagnostics': false,
});
let savedOpen = null;

watch(searching, (now, previous) => {
  if (now === previous) return;
  if (now) {
    savedOpen = { ...sectionOpen };
    for (const id of Object.keys(sectionOpen)) sectionOpen[id] = true;
  } else if (savedOpen) {
    Object.assign(sectionOpen, savedOpen);
    savedOpen = null;
  }
});

const sso = reactive({ username: '', password: '' });
const devMock = ref(false);
const saving = ref(false);
const pwdHint = ref('');
const info = reactive({});
const browserPath = ref('');
const effectiveChrome = ref('');
const testing = ref('');
const testLogs = ref([]);
const testResult = ref(null);
const resultPaths = ref([]);
const ui = reactive({ closeToTray: true, autostart: false });
const workers = reactive({ query: 8, render: 4 });
const logStats = reactive({ dir: '', files: 0, bytes: 0 });
const clearing = ref(false);
const version = ref('');

function fmtBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = n || 0;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

async function loadLogStats() {
  try {
    Object.assign(logStats, await call('getLogStats'));
  } catch (_e) {
    /* 忽略 */
  }
}

function clearLogs() {
  const dialog = DialogPlugin.confirm({
    header: '一键清除日志',
    body: '将删除全部任务日志与任务档案（不影响执行记录）；正在运行任务的日志会被跳过。确定继续？',
    confirmBtn: '清除',
    theme: 'warning',
    onConfirm: async () => {
      dialog.hide();
      clearing.value = true;
      try {
        const result = await call('clearLogs');
        let message = `已清除 ${result.removed} 个日志文件`;
        if (result.skipped > 0) message += `，${result.skipped} 个正在使用被跳过`;
        MessagePlugin.success(message);
        loadLogStats();
      } catch (e) {
        MessagePlugin.error(String(e.message || e));
      } finally {
        clearing.value = false;
      }
    },
  });
}

async function load() {
  try {
    const cfg = await call('getConfig');
    sso.username = (cfg.sso || {}).username || '';
    devMock.value = !!(cfg.dev || {}).mock;
    browserPath.value = (cfg.render || {}).chrome || '';
    ui.closeToTray = (cfg.ui || {}).close_to_tray !== false;
    ui.autostart = !!(cfg.ui || {}).autostart;

    workers.query = (cfg.order_export || {}).query_workers || 8;
    workers.render = (cfg.order_export || {}).render_workers || 4;
    pwdHint.value = (cfg.sso || {}).has_password
      ? `密码：已配置（来源 ${cfg.sso.password_source || '未知'}）`
      : '密码：未配置，导出将无法登录';
    Object.assign(info, await call('getSystemInfo'));
    effectiveChrome.value = browserPath.value.trim() ? info.chrome || browserPath.value.trim() : '';
  } catch (_e) {
    /* 忽略 */
  }
  try {
    version.value = (await window.core.appVersion()) || '';
  } catch (_e) {
    /* 旧 preload 无版本通道时留空 */
  }
}

async function saveSso() {
  saving.value = true;
  try {
    await call('saveConfig', { sso: { username: sso.username.trim(), password: sso.password } });
    sso.password = '';
    MessagePlugin.success('已保存');
    load();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

async function pickBrowser() {
  const path = await pickFile([{ name: '可执行文件', extensions: ['*'] }]);
  if (path) browserPath.value = path;
}

async function autoDetect() {
  try {
    const result = await call('getSystemInfo');
    if (result.chrome) {
      browserPath.value = result.chrome;
      effectiveChrome.value = result.chrome;
      MessagePlugin.success('检测到：' + result.chrome);
    } else {
      MessagePlugin.warning('未自动探测到浏览器，请手动选择（或留空用自带 Electron 兜底）');
    }
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

async function saveBrowser() {
  saving.value = true;
  try {
    await call('saveConfig', { render: { chrome: browserPath.value.trim() } });
    MessagePlugin.success('已保存');
    load();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

async function saveWorkers() {
  saving.value = true;
  try {
    await call('saveConfig', {
      order_export: {
        query_workers: Number(workers.query) || 8,
        render_workers: Number(workers.render) || 4,
      },
    });
    MessagePlugin.success('已保存');
    load();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

function runSelftest(kind) {
  testing.value = kind;
  testLogs.value = [];
  testResult.value = null;
  resultPaths.value = [];
  runJob(
    'selftest:' + kind,
    {},
    {
      onLog: (line) => testLogs.value.push(line),
      onDone: (job) => {
        testing.value = '';
        const ok = job.status === 'success';
        let text = job.error || '';
        if (ok && kind === 'render') {
          const lanes = (job.result || {}).lanes || [];
          text = lanes
            .map(
              (lane) =>
                `${lane.lane === 'electron' ? '自带Electron' : '浏览器'}：` +
                `${lane.ok ? 'OK' : '失败'}`
            )
            .join('；');
        } else if (ok && kind === 'sso') {
          text = (job.result || {}).mock ? '登录成功（模拟数据模式）' : 'SSO 登录成功，凭证已就绪';
        } else if (ok && kind === 'cdp') {
          const result = job.result || {};
          text = `CDP 渲染：导航/eval/新tab/截图/printToPDF 全部通过（${result.browser || ''}）`;
          resultPaths.value = [result.png, result.pdf].filter(Boolean);
        }
        testResult.value = { ok, text };
      },
      onError: (e) => {
        testing.value = '';
        testResult.value = { ok: false, text: String(e.message || e) };
      },
    }
  );
}

async function saveUi() {
  saving.value = true;
  try {
    await call('saveConfig', {
      ui: { close_to_tray: ui.closeToTray, autostart: ui.autostart },
    });
    window.core.uiChanged({ close_to_tray: ui.closeToTray, autostart: ui.autostart });
    MessagePlugin.success('已保存');
    load();
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  } finally {
    saving.value = false;
  }
}

async function saveDev() {
  await call('saveConfig', { dev: { mock: devMock.value } });
  MessagePlugin.success('已保存');
}

onActivated(() => {
  load();
  loadLogStats();
});
</script>

<style scoped>
.settings-divider {
  height: 1px;
  background: var(--tk-hair);
  margin: 12px 0;
}
.about-block {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: 13px;
}
</style>
