<template>
  <div>
    <t-card>
      <div class="sec-title">状态<span class="right">{{ dueReason }}</span></div>
      <div class="kv">
        <span class="k">上次成功</span><span>{{ statusText.lastSuccess }}</span>
        <span class="k">上次产物</span><span>{{ statusText.lastFile }}</span>
        <span class="k">最近任务</span><span>{{ statusText.lastRun }}</span>
        <span class="k">下次定时</span><span>{{ statusText.nextRun }}</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">定时设置</div>
      <div class="row">
        <t-switch v-model="sch.enabled" size="large" />
        <span class="muted">启用每日定时导出</span>
        <t-input v-model="sch.time" style="width:110px" placeholder="09:15" />
        <t-button theme="primary" :loading="saving" @click="saveSchedule">保存</t-button>
        <span class="muted">下次执行：{{ statusText.nextRun }}</span>
      </div>
      <div class="muted" style="margin-top:8px">
        由系统定时器每分钟检查一次，到点自动执行；改时间即时生效，无需管理员权限；
        机器关机错过到点会在开机后自动补跑。
        <span v-if="driver === 'app'">当前为「客户端驻留执行」模式（客户端不运行则当天不执行），可在设置页切换。</span>
        <span v-else>当前为「系统定时器」模式（客户端不开也执行）。</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">U 盘（KVM 虚拟 U 盘）</div>
      <div class="row">
        <span class="muted">卷标</span>
        <t-input v-model="usbEdit.label" style="width:160px" placeholder="GLKVM" />
        <span class="muted">后备目录</span>
        <t-input v-model="usbEdit.fallback_dir" class="grow"
                 placeholder="U盘不可用时的落盘目录（留空则任务失败）" />
        <t-button theme="primary" :loading="savingUsb" @click="saveUsb">保存</t-button>
        <t-button variant="outline" @click="loadStatus">重新检测</t-button>
      </div>
      <div class="kv" style="margin-top:10px">
        <span class="k">挂载点</span><span>{{ usb.mount || '未检测到（执行时将尝试自动挂载）' }}</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">立即执行</div>
      <div class="row">
        <t-input v-model="runDate" style="width:160px" placeholder="留空 = 今天（YYYY-MM-DD）" />
        <t-button theme="primary" :disabled="running" @click="startRun">立即执行一次</t-button>
        <t-button v-if="running" theme="danger" @click="cancel">取消</t-button>
      </div>
      <t-alert v-if="runError" theme="error" style="margin-top:10px">
        启动失败：{{ runError }}
      </t-alert>
      <div style="margin-top:10px">
        <t-progress theme="plump" :percentage="progress.pct" :label="progress.text"
                    :color="'#0E3DA8'" track-color="rgba(14,61,168,.10)" />
      </div>
      <div ref="logEl" class="logview" style="margin-top:10px">{{ logs.join('\n') }}</div>
      <div class="row" style="margin-top:10px" v-if="doneInfo">
        <t-tag :theme="doneInfo.ok ? 'success' : 'danger'" variant="light-outline">
          {{ doneInfo.statusText }}
        </t-tag>
        <span class="muted">{{ doneInfo.text }}</span>
        <t-button v-if="doneInfo.dir" size="small" variant="outline"
                  @click="call('openPath', { path: doneInfo.dir })">打开目录</t-button>
      </div>
    </t-card>

    <HistoryCard ref="histCard" app-id="daily_sync" :format-result="fmtResult" />
  </div>
</template>

<script setup>
import { nextTick, onActivated, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { call, runJob } from '../api'
import HistoryCard from '../components/HistoryCard.vue'

const sch = reactive({ enabled: true, time: '09:15' })
const statusText = reactive({ lastSuccess: '-', lastFile: '-', lastRun: '-', nextRun: '-' })
const usb = reactive({})
const usbEdit = reactive({ label: '', fallback_dir: '' })
const savingUsb = ref(false)
const dueReason = ref('')
const driver = ref('os')
const histCard = ref(null)
const runDate = ref('')
const running = ref(false)
const saving = ref(false)
const logs = ref([])
const logEl = ref(null)
const progress = reactive({ pct: 0, text: '' })
const doneInfo = ref(null)
const runError = ref('')

let currentJob = null

function fmtResult(r) {
  if (!r) return ''
  if (!r.count) return '当日无派车数据'
  return `${r.count} 条 → ${r.file || ''}${r.dest ? '（' + r.dest + '）' : ''}`
}

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' }

async function loadStatus() {
  try {
    const st = await call('getAppStatus', { app_id: 'daily_sync' })
    const stamp = st.stamp || {}
    const last = st.last_run || {}
    statusText.lastSuccess = stamp.time
      ? stamp.time + (stamp.count != null && stamp.count !== '' ? `（${stamp.count} 条）` : '') : '从未执行'
    statusText.lastFile = stamp.file || '-'
    statusText.lastRun = last.time ? `${last.time}　${STATUS_CN[last.status] || last.status}${last.error ? '：' + last.error : ''}` : '-'
    statusText.nextRun = (st.schedule || {}).next_run || '-'
    dueReason.value = (st.due || {}).reason || ''
    driver.value = (st.schedule || {}).driver || 'os'
    sch.enabled = !!(st.schedule || {}).enabled
    sch.time = (st.schedule || {}).time || '09:15'
    Object.assign(usb, st.usb || {})
    usbEdit.label = usb.label || ''
    usbEdit.fallback_dir = usb.fallback_dir || ''
  } catch (e) { /* MessagePlugin 由调用处统一处理 */ }
}

async function saveUsb() {
  savingUsb.value = true
  try {
    await call('saveConfig', { usb: {
      label: usbEdit.label.trim() || 'GLKVM',
      fallback_dir: usbEdit.fallback_dir.trim(),
    } })
    MessagePlugin.success('已保存')
    loadStatus()
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  } finally {
    savingUsb.value = false
  }
}

async function saveSchedule() {
  if (!/^\d{1,2}:\d{2}$/.test(sch.time)) {
    MessagePlugin.warning('时间格式应为 HH:MM，如 09:15')
    return
  }
  saving.value = true
  try {
    await call('saveConfig', { schedule: { enabled: sch.enabled, time: sch.time } })
    MessagePlugin.success('已保存，定时器将在下一检查周期生效')
    loadStatus()
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  } finally {
    saving.value = false
  }
}

function appendLog(line) {
  logs.value.push(line)
  if (logs.value.length > 1500) logs.value.splice(0, logs.value.length - 1500)
  nextTick(() => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight })
}

async function startRun() {
  // 凭据预检：没配 SSO 直接提示（避免任务秒失败、界面看似没反应）
  try {
    const cfg = await call('getConfig')
    if (!(cfg.sso || {}).has_password && !(cfg.dev || {}).mock) {
      MessagePlugin.warning('尚未配置 SSO 账号密码——请先到「设置」页填写（或开启开发模式试用）')
      return
    }
  } catch (e) { /* 忽略，照常下发 */ }
  logs.value = []
  doneInfo.value = null
  runError.value = ''
  running.value = true
  progress.pct = 0; progress.text = ''
  try {
    currentJob = runJob('daily_sync', { date: runDate.value.trim() }, {
      onLog: appendLog,
      onProgress: (p) => {
        progress.pct = p.total > 0 ? Math.round(p.current * 100 / p.total) : 0
        progress.text = `${p.label || ''} ${p.current}/${p.total}`
      },
      onDone: (job) => {
        running.value = false
        const ok = job.status === 'success'
        doneInfo.value = {
          ok,
          statusText: STATUS_CN[job.status] || job.status,
          text: job.error || fmtResult(job.result),
          dir: (job.result || {}).dir || '',
        }
        loadStatus(); histCard.value && histCard.value.reload()
      },
      onError: (e) => {
        running.value = false
        runError.value = String(e.message || e)
      },
    })
  } catch (e) {
    running.value = false
    runError.value = String(e.message || e)
  }
}

function cancel() {
  if (currentJob) currentJob.cancel()
}

// keep-alive：首次显示与每次切回都触发（任务组件常驻，状态不丢）；
// 最近执行记录由 HistoryCard 自行在激活时刷新
onActivated(loadStatus)
</script>
