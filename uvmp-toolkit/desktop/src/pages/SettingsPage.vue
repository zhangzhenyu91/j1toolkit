<template>
  <div>
    <t-card>
      <div class="sec-title">派车系统 SSO 凭据</div>
      <div class="muted" style="margin-bottom:10px">
        保存于本机配置文件（Linux 权限 600），不用于其他用途；密码留空则不修改。
      </div>
      <div class="row">
        <span class="muted">账号</span>
        <t-input v-model="sso.username" class="grow" placeholder="派车系统登录账号" />
        <span class="muted">密码</span>
        <t-input v-model="sso.password" type="password" class="grow" placeholder="留空则不修改" />
        <t-button theme="primary" :loading="saving" @click="saveSso">保存</t-button>
      </div>
      <div class="muted" style="margin-top:8px">{{ pwdHint }}</div>
    </t-card>

    <t-card>
      <div class="sec-title">浏览器设置</div>
      <div class="muted" style="margin-bottom:10px">
        派车单/轨迹 PDF 渲染与「官方页面直出」所用的浏览器。内网请选公司指定的专用浏览器
        （如奇安信可信浏览器、360 安全浏览器）；不设置时自动探测，且自带 Electron 渲染兜底。
      </div>
      <div class="row">
        <t-input v-model="browserPath" class="grow" placeholder="留空 = 自动探测（自带 Electron 兜底）" />
        <t-button variant="outline" @click="pickBrowser">浏览…</t-button>
        <t-button variant="outline" @click="autoDetect">自动检测</t-button>
        <t-button theme="primary" :loading="saving" @click="saveBrowser">保存</t-button>
      </div>
      <div class="muted" style="margin-top:8px">当前生效：{{ effectiveChrome || '未配置（自动探测/自带 Electron 兜底）' }}</div>
    </t-card>

    <t-card>
      <div class="sec-title">环境自检</div>
      <div class="row">
        <t-button variant="outline" :loading="testing === 'render'" @click="runSelftest('render')">
          PDF 渲染自检
        </t-button>
        <t-button variant="outline" :loading="testing === 'sso'" @click="runSelftest('sso')">
          SSO 登录测试
        </t-button>
        <span class="muted" v-if="testing">自检运行中…</span>
      </div>
      <div v-if="testResult" class="row" style="margin-top:10px">
        <t-tag :theme="testResult.ok ? 'success' : 'danger'" variant="light-outline">
          {{ testResult.ok ? '通过' : '失败' }}
        </t-tag>
        <span class="muted">{{ testResult.text }}</span>
      </div>
      <div class="logview" style="margin-top:10px; height:140px">{{ testLogs.join('\n') }}</div>
    </t-card>

    <t-card>
      <div class="sec-title">后台运行</div>
      <div class="row">
        <t-switch v-model="ui.closeToTray" size="large" />
        <span class="muted">关闭窗口时最小化到托盘（驻留后台；托盘图标右键「退出」才真退出）</span>
      </div>
      <div class="row" style="margin-top:8px">
        <t-switch v-model="ui.autostart" size="large" />
        <span class="muted">开机自动启动（登录后驻留托盘）</span>
      </div>
      <div class="row" style="margin-top:8px">
        <span class="muted">每日导出由谁执行：</span>
        <t-radio-group v-model="ui.driver">
          <t-radio value="os">系统定时器（默认，客户端不开也执行）</t-radio>
          <t-radio value="app">客户端驻留执行（需开机自启+托盘驻留）</t-radio>
        </t-radio-group>
      </div>
      <div class="muted" style="margin-top:6px">
        驻留执行更实时、状态直出客户端；代价是机器注销/未登录时当天不执行（系统定时器模式无此限制）。两种模式互斥，不会双跑。
      </div>
      <div class="row" style="margin-top:10px">
        <t-button theme="primary" :loading="saving" @click="saveUi">保存后台运行设置</t-button>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">高级</div>
      <div class="row">
        <t-switch v-model="devMock" size="large" />
        <span class="muted">开发模式：使用模拟派车数据（dev.mock；无派车系统网络时调试用，生产环境勿开）</span>
        <t-button variant="outline" @click="saveDev">保存高级设置</t-button>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">路径</div>
      <div class="kv">
        <span class="k">配置文件</span><span>{{ info.config_path }}</span>
        <span class="k">状态目录</span>
        <span>
          {{ info.state_dir }}
          <t-button size="small" variant="text" @click="showItem(info.state_dir)">打开</t-button>
        </span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">日志管理</div>
      <div class="kv">
        <span class="k">日志目录</span>
        <span>
          {{ logStats.dir || '-' }}
          <t-button v-if="logStats.dir" size="small" variant="text" @click="showItem(logStats.dir)">打开目录</t-button>
        </span>
        <span class="k">占用</span><span>{{ logStats.files }} 个文件 / {{ fmtBytes(logStats.bytes) }}</span>
      </div>
      <div class="row" style="margin-top:10px">
        <t-button theme="danger" variant="outline" :loading="clearing" @click="clearLogs">一键清除</t-button>
        <span class="muted">30 天前的任务日志在核心启动时自动清理</span>
      </div>
    </t-card>
  </div>
</template>

<script setup>
import { onActivated, reactive, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { call, pickFile, runJob, showItem } from '../api'

const sso = reactive({ username: '', password: '' })
const devMock = ref(false)
const saving = ref(false)
const pwdHint = ref('')
const info = reactive({})
const browserPath = ref('')
const effectiveChrome = ref('')
const testing = ref('')
const testLogs = ref([])
const testResult = ref(null)
const ui = reactive({ closeToTray: true, autostart: false, driver: 'os' })
const logStats = reactive({ dir: '', files: 0, bytes: 0 })
const clearing = ref(false)

function fmtBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB']
  let v = n || 0
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1 }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

async function loadLogStats() {
  try {
    Object.assign(logStats, await call('getLogStats'))
  } catch (e) { /* 忽略 */ }
}

function clearLogs() {
  const dlg = DialogPlugin.confirm({
    header: '一键清除日志',
    body: '将删除全部任务日志与任务档案（不影响执行记录）；正在运行任务的日志会被跳过。确定继续？',
    confirmBtn: '清除',
    theme: 'warning',
    onConfirm: async () => {
      dlg.hide()
      clearing.value = true
      try {
        const r = await call('clearLogs')
        let msg = `已清除 ${r.removed} 个日志文件`
        if (r.skipped > 0) msg += `，${r.skipped} 个正在使用被跳过`
        MessagePlugin.success(msg)
        loadLogStats()
      } catch (e) {
        MessagePlugin.error(String(e.message || e))
      } finally {
        clearing.value = false
      }
    },
  })
}

async function load() {
  try {
    const cfg = await call('getConfig')
    sso.username = (cfg.sso || {}).username || ''
    devMock.value = !!(cfg.dev || {}).mock
    browserPath.value = (cfg.render || {}).chrome || ''
    ui.closeToTray = (cfg.ui || {}).close_to_tray !== false
    ui.autostart = !!(cfg.ui || {}).autostart
    ui.driver = ((cfg.schedule || {}).driver) || 'os'
    pwdHint.value = (cfg.sso || {}).has_password
      ? `密码：已配置（来源 ${cfg.sso.password_source || '未知'}）`
      : '密码：未配置，导出将无法登录'
    Object.assign(info, await call('getSystemInfo'))
    effectiveChrome.value = info.chrome || ''
  } catch (e) { /* 忽略 */ }
}

async function saveSso() {
  saving.value = true
  try {
    await call('saveConfig', { sso: { username: sso.username.trim(), password: sso.password } })
    sso.password = ''
    MessagePlugin.success('已保存')
    load()
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  } finally {
    saving.value = false
  }
}

async function pickBrowser() {
  const p = await pickFile([{ name: '可执行文件', extensions: ['*'] }])
  if (p) browserPath.value = p
}

async function autoDetect() {
  try {
    const r = await call('getSystemInfo')
    if (r.chrome) {
      browserPath.value = r.chrome
      effectiveChrome.value = r.chrome
      MessagePlugin.success('检测到：' + r.chrome)
    } else {
      MessagePlugin.warning('未自动探测到浏览器，请手动选择（或留空用自带 Electron 兜底）')
    }
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  }
}

async function saveBrowser() {
  saving.value = true
  try {
    await call('saveConfig', { render: { chrome: browserPath.value.trim() } })
    MessagePlugin.success('已保存')
    load()
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  } finally {
    saving.value = false
  }
}

function runSelftest(kind) {
  testing.value = kind
  testLogs.value = []
  testResult.value = null
  runJob('selftest:' + kind, {}, {
    onLog: (l) => testLogs.value.push(l),
    onDone: (job) => {
      testing.value = ''
      const ok = job.status === 'success'
      let text = job.error || ''
      if (ok && kind === 'render') {
        const lanes = (job.result || {}).lanes || []
        text = lanes.map((l) => `${l.lane === 'electron' ? '自带Electron' : '浏览器'}：`
          + `${l.ok ? 'OK' : '失败'}`).join('；')
      } else if (ok && kind === 'sso') {
        text = (job.result || {}).mock ? '登录成功（模拟数据模式）' : 'SSO 登录成功，凭证已就绪'
      }
      testResult.value = { ok, text }
    },
    onError: (e) => {
      testing.value = ''
      testResult.value = { ok: false, text: String(e.message || e) }
    },
  })
}

async function saveUi() {
  saving.value = true
  try {
    await call('saveConfig', {
      ui: { close_to_tray: ui.closeToTray, autostart: ui.autostart },
      schedule: { driver: ui.driver },
    })
    window.core.uiChanged({ close_to_tray: ui.closeToTray, autostart: ui.autostart })
    MessagePlugin.success('已保存')
    load()
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  } finally {
    saving.value = false
  }
}

async function saveDev() {
  await call('saveConfig', { dev: { mock: devMock.value } })
  MessagePlugin.success('已保存')
}

// keep-alive：首次显示与每次切回都触发
onActivated(() => { load(); loadLogStats() })
</script>
