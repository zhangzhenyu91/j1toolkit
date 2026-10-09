<template>
  <div>
    <t-card>
      <div class="sec-title">① 派车单号清单</div>
      <div class="row">
        <t-button variant="outline" @click="pickXlsx">选择 xlsx 文件…</t-button>
        <span class="muted grow">{{ fileName || '未选择文件' }}</span>
        <span class="muted">或</span>
        <t-button variant="outline" @click="parseText">解析下方粘贴的单号</t-button>
      </div>
      <t-textarea v-model="codesText" style="margin-top:10px" :autosize="{ minRows: 3, maxRows: 6 }"
                  placeholder="也可以直接粘贴派车单号，每行一个（或用逗号/空格分隔）" />
      <div v-if="codes.length" style="margin-top:10px">
        <div class="row">
          <t-tag theme="primary" variant="light">共 {{ codes.length }} 个单号</t-tag>
          <span class="muted">来源：{{ batchName }}{{ codes.length > 100 ? '（仅预览前 100 个）' : '' }}</span>
        </div>
        <div class="codeview" style="height:110px; margin-top:8px">
          <div v-for="c in codes.slice(0, 100)" :key="c.seq">
            {{ c.seq }}. {{ c.code }}<span v-if="c.date_hint">　（{{ c.date_hint }}）</span>
          </div>
        </div>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">② 输出设置</div>
      <div class="row">
        <span class="muted">输出目录</span>
        <t-input v-model="outdir" class="grow" placeholder="选择或输入目录路径" />
        <t-button variant="outline" @click="browseOutdir">浏览…</t-button>
      </div>
      <div class="row" style="margin-top:10px">
        <t-checkbox v-model="opt.batchSubdir">建批次子目录（防覆盖）</t-checkbox>
        <t-checkbox v-model="opt.pcd">导出派车单 PDF</t-checkbox>
        <t-checkbox v-model="opt.track">导出轨迹 PDF + 轨迹点 CSV</t-checkbox>
        <t-checkbox v-model="opt.calibrateNoon">自动校准行程结束时间</t-checkbox>
      </div>
      <div class="muted" style="margin-top:8px">
        逐单文件在「逐单/」子目录：序号_派车单_单号_车牌_日期.pdf / 序号_轨迹_单号_车牌_日期.pdf / 序号_轨迹点_单号.csv；
        全部导出后按序号合并出根目录 _合并_派车单.pdf / _合并_轨迹.pdf；批次内附 _清单.csv、_12点前结束行程.xlsx 与 _未找到.txt；同批次重跑自动跳过已导出单据。
        勾选「自动校准行程结束时间」后：行程结束时间早于 12 点的轨迹（车载定位系统异常），按 1 小时逐次叠加校准至 12 点后
        （如 10:32→12:32），轨迹 PDF 弹窗内的结束时间与行驶时间同步改写，_12点前结束行程.xlsx 附「校准后结束时间/校准后行驶时长」两列。
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">③ 执行</div>
      <div class="row">
        <t-button theme="primary" :disabled="!codes.length || running" @click="startRun">开始导出</t-button>
        <t-button v-if="running" theme="danger" @click="cancel">取消</t-button>
        <span class="muted" v-if="!codes.length">请先选择 xlsx 文件或粘贴单号并解析</span>
      </div>
      <t-alert v-if="runError" theme="error" style="margin-top:10px">
        启动失败：{{ runError }}
      </t-alert>
      <div style="margin-top:10px">
        <t-progress theme="plump" :percentage="progress.pct" :label="progress.text"
                    :color="'#0E3DA8'" track-color="rgba(14,61,168,.10)" />
      </div>
      <div ref="logEl" class="logview" style="margin-top:10px">{{ logs.join('\n') }}</div>
    </t-card>

    <t-card>
      <div class="sec-title">最近执行记录</div>
      <t-table :data="history" :columns="histCols" size="small" row-key="job_id"
               :max-height="220">
        <template #op="{ row }">
          <t-button size="small" variant="outline" @click="showLog(row)">日志</t-button>
          <t-button v-if="row.dir" size="small" variant="outline"
                    @click="call('openPath', { path: row.dir })">打开目录</t-button>
        </template>
      </t-table>
    </t-card>

    <t-dialog v-model:visible="logVisible" header="任务日志" width="80vw" :footer="false">
      <div class="logview" style="height:60vh">{{ logText }}</div>
    </t-dialog>
  </div>
</template>

<script setup>
import { nextTick, onActivated, onMounted, reactive, ref } from 'vue'
// PcdExportPage 有意保留 onMounted（输出目录建议只在首次进页填充，切回不覆盖用户输入）；
// 最近执行记录走 onActivated（每次切回刷新），与派车单同步页一致
import { MessagePlugin } from 'tdesign-vue-next'
import { call, pickFile, pickDirectory, runJob } from '../api'

const codes = ref([])
const batchName = ref('派车单')
const fileName = ref('')
const codesText = ref('')
const outdir = ref('')
const opt = reactive({ batchSubdir: true, pcd: true, track: true, calibrateNoon: false })
const running = ref(false)
const logs = ref([])
const logEl = ref(null)
const progress = reactive({ pct: 0, text: '' })
const runError = ref('')
const history = ref([])
const histCols = [
  { colKey: 'time', title: '时间', width: 160 },
  { colKey: 'status', title: '状态', width: 90 },
  { colKey: 'summary', title: '结果', ellipsis: true },
  { colKey: 'op', title: '操作', width: 160 },
]
const logVisible = ref(false)
const logText = ref('')

async function showLog(row) {
  try {
    const r = await call('getJobLog', { job_id: row.job_id })
    logText.value = r.text || '（日志为空）'
    logVisible.value = true
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  }
}

let currentJob = null

function applyPreview(r, sourceName) {
  codes.value = r.codes || []
  batchName.value = sourceName
  if (!codes.value.length) MessagePlugin.warning('未解析到有效单号')
}

async function pickXlsx() {
  const path = await pickFile([{ name: 'Excel 文件', extensions: ['xlsx'] }])
  if (!path) return
  try {
    const r = await call('preview', { app_id: 'pcd_export', params: { xlsx_path: path } })
    fileName.value = path.split(/[\\/]/).pop()
    codesText.value = ''
    applyPreview(r, fileName.value.replace(/\.xlsx$/i, ''))
  } catch (e) {
    MessagePlugin.error('解析失败：' + (e.message || e))
  }
}

async function parseText() {
  const text = codesText.value.trim()
  if (!text) { MessagePlugin.warning('请先粘贴派车单号'); return }
  const r = await call('preview', { app_id: 'pcd_export', params: { codes_text: text } })
  fileName.value = '（粘贴文本）'
  applyPreview(r, '粘贴文本')
}

async function browseOutdir() {
  const dir = await pickDirectory(outdir.value || undefined)
  if (dir) outdir.value = dir
}

function appendLog(line) {
  logs.value.push(line)
  if (logs.value.length > 1500) logs.value.splice(0, logs.value.length - 1500)
  nextTick(() => { if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight })
}

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' }

function fmtResult(r) {
  if (!r) return ''
  const parts = [`共 ${r.total || 0} 单`,
                 `派车单 成功 ${r.pcd_ok || 0}/失败 ${r.pcd_fail || 0}`,
                 `轨迹 成功 ${r.track_ok || 0}/失败 ${r.track_fail || 0}`]
  if ((r.not_found || []).length) parts.push(`未找到 ${r.not_found.length}`)
  return parts.join('；')
}

async function loadHistory() {
  try {
    const rows = await call('getHistory', { app_id: 'pcd_export' })
    history.value = rows.map((r) => ({
      time: r.time, status: STATUS_CN[r.status] || r.status,
      summary: r.error || fmtResult(r.result),
      dir: (r.result || {}).dir || '',
      job_id: r.job_id,
    }))
  } catch (e) { /* 忽略 */ }
}

async function startRun() {
  if (!outdir.value.trim()) { MessagePlugin.warning('请填写输出目录'); return }
  if (!opt.pcd && !opt.track) { MessagePlugin.warning('派车单 / 轨迹至少勾选一项'); return }
  // 凭据预检：没配 SSO 直接提示，避免任务秒失败后界面看似「没反应」
  try {
    const cfg = await call('getConfig')
    if (!(cfg.sso || {}).has_password && !(cfg.dev || {}).mock) {
      MessagePlugin.warning('尚未配置 SSO 账号密码——请先到「设置」页填写（或开启开发模式试用）')
      return
    }
  } catch (e) { /* 忽略，照常下发 */ }
  logs.value = []
  runError.value = ''
  running.value = true
  progress.pct = 0; progress.text = ''
  try {
    currentJob = runJob('pcd_export', {
      codes: codes.value,
      outdir: outdir.value.trim(),
      batch_name: batchName.value,
      batch_subdir: opt.batchSubdir,
      export_pcd: opt.pcd,
      export_track: opt.track,
      calibrate_noon: opt.calibrateNoon,
    }, {
      onLog: appendLog,
      onProgress: (p) => {
        progress.pct = p.total > 0 ? Math.round(p.current * 100 / p.total) : 0
        progress.text = `${p.label || ''} ${p.current}/${p.total}`
      },
      onDone: () => {
        running.value = false
        loadHistory()
        // 任务完成后清空单号/日志等旧数据（结果已落入底部最近执行记录），
        // 避免下次导入新表时界面还留着上一批的数据造成混淆
        codes.value = []
        codesText.value = ''
        fileName.value = ''
        batchName.value = '派车单'
        logs.value = []
        progress.pct = 0; progress.text = ''
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

onActivated(loadHistory)

onMounted(async () => {
  try {
    const info = await call('getSystemInfo')
    const dirs = info.suggested_outdirs || []
    if (dirs.length) outdir.value = dirs[0]
  } catch (e) { /* 忽略 */ }
})
</script>
