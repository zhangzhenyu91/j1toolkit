<template>
  <div>
    <t-card>
      <div class="sec-title">① 题库管理
        <span class="right">
          <t-button size="small" variant="text" @click="downloadTemplate">下载模板</t-button>
          <t-button size="small" variant="outline" :loading="importing" @click="importBank">导入 Excel 题库…</t-button>
        </span>
      </div>
      <div class="muted" style="margin-bottom:10px">
        支持 xls/xlsx，表头：题干、答案、选项A~F（可选：题型、解析）——点「下载模板」获取带示例的空白表。导入后题库保存在本机（各启用库合并搜题），不再依赖原 Excel 文件。
      </div>
      <t-alert v-if="!banks.length" theme="info" style="margin-bottom:10px">
        尚未导入题库——搜题前请先导入至少一个题库。
      </t-alert>
      <div v-for="b in banks" :key="b.id" class="bankrow">
        <t-switch v-model="b.enabled" size="small" @change="(v) => setEnabled(b, v)" />
        <span class="bankname" :title="b.sourceFile">{{ b.name }}</span>
        <t-tag size="small" variant="light-outline">{{ b.count }} 题</t-tag>
        <span class="muted banktime">{{ fmtTime(b.importedAt) }}</span>
        <t-button size="small" variant="text" @click="openRename(b)">重命名</t-button>
        <t-popconfirm content="确定删除该题库？" @confirm="removeBank(b)">
          <t-button size="small" variant="text" theme="danger">删除</t-button>
        </t-popconfirm>
      </div>
      <div class="muted" v-if="banks.length">启用题库合计 {{ questionCount }} 题参与搜题。</div>
    </t-card>

    <t-card>
      <div class="sec-title">② 搜题自测</div>
      <div class="row">
        <t-input v-model="testText" class="grow" placeholder="粘贴一段题干文字，验证题库是否可用" @enter="testMatch" />
        <t-button variant="outline" @click="testMatch">测试匹配</t-button>
      </div>
      <div v-if="testResult" style="margin-top:10px">
        <template v-if="testResult.matched">
          <span class="ansbadge">{{ testResult.question.answer }}</span>
          <span style="margin-left:8px">{{ testResult.question.content }}</span>
          <div class="muted" style="margin-top:4px">
            {{ testResult.question.bankName }} · 匹配度 {{ Math.round(testResult.score * 100) }}%
          </div>
        </template>
        <template v-else>
          <t-tag theme="warning" variant="light">未匹配到题目</t-tag>
          <span v-if="testResult.near" class="muted"> 最接近：{{ testResult.near.content.slice(0, 40) }}…</span>
        </template>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">③ 搜题控制
        <span class="right">
          <t-button v-if="!scanning" theme="primary" :disabled="!questionCount" @click="start">开始搜题</t-button>
          <t-button v-else theme="danger" @click="stop">停止搜题</t-button>
        </span>
      </div>
      <t-alert v-if="engineError" theme="error" style="margin-bottom:10px">OCR 引擎异常：{{ engineError }}</t-alert>
      <div class="kv">
        <span class="k">OCR 引擎</span>
        <span>{{ engineText }}</span>
        <span class="k">状态</span>
        <span>{{ scanning ? '搜题中（主窗口已最小化，框选区域对准题目）' : '未启动' }}</span>
      </div>
      <div class="row" style="margin-top:10px">
        <t-checkbox v-model="boxA" @change="(v) => setBox('A', v)">扫描框 A</t-checkbox>
        <t-checkbox v-model="boxB" @change="(v) => setBox('B', v)">扫描框 B</t-checkbox>
        <t-button size="small" variant="outline" @click="resetBoxes">重置框位置</t-button>
        <span class="muted">框可拖动/右下角拖拽缩放；页面滚动时两框分别罩住顶部与底部题目</span>
      </div>
      <div class="optrow">
        <span class="muted">扫描间隔</span>
        <t-slider v-model="opt.interval" :min="500" :max="3000" :step="100" class="grow"
                  :tooltip-props="{ content: opt.interval + 'ms' }" @change-end="applyOptions" />
        <span class="optval">{{ opt.interval }}ms</span>
      </div>
      <div class="optrow">
        <span class="muted">匹配阈值</span>
        <t-slider v-model="opt.threshold" :min="0.3" :max="0.9" :step="0.05" class="grow" @change-end="applyOptions" />
        <span class="optval">{{ Math.round(opt.threshold * 100) }}%</span>
      </div>
      <div class="optrow">
        <span class="muted">结果窗不透明度</span>
        <t-slider v-model="opt.opacity" :min="0.3" :max="1" :step="0.05" class="grow" @change-end="applyOptions" />
        <span class="optval">{{ Math.round(opt.opacity * 100) }}%</span>
      </div>
      <template v-if="scanning">
        <div class="sec-sub">运行诊断</div>
        <t-alert v-if="sessionType === 'wayland'" theme="error" style="margin-bottom:8px">
          当前为 Wayland 会话，截屏不可用——请注销后在登录界面选择 X11 会话登录。
        </t-alert>
        <t-alert v-if="captureError" theme="warning" style="margin-bottom:8px">{{ captureError }}</t-alert>
        <div class="diag" v-for="id in ['A', 'B']" :key="id">
          <span class="boxtag">框{{ id }}</span>
          <template v-if="boxDebug[id]">
            <t-tag v-if="boxDebug[id].matched" theme="success" size="small" variant="light">已命中</t-tag>
            <t-tag v-else theme="warning" size="small" variant="light">未匹配</t-tag>
            <span class="muted diagtxt">{{ boxDebug[id].snippet ? '识别：' + boxDebug[id].snippet : '未识别到文字（框内无字或截屏为空）' }}</span>
          </template>
          <span v-else class="muted diagtxt">尚未出帧</span>
        </div>
        <div class="row" style="margin-top:8px">
          <t-button size="small" variant="outline" @click="debugCapture">保存框内截图…</t-button>
          <span class="muted">截屏为空时用它确认框实际所见；最近一帧 OCR {{ ocrMs || '-' }}ms</span>
        </div>
      </template>
    </t-card>

    <t-dialog v-model:visible="renameDlg.show" header="重命名题库" :confirm-btn="'保存'"
              @confirm="doRename">
      <t-input v-model="renameDlg.name" placeholder="题库名称" />
    </t-dialog>
  </div>
</template>

<script setup>
import { onActivated, onMounted, onUnmounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { pickFile, qs, showItem } from '../api'

const banks = ref([])
const questionCount = ref(0)
const scanning = ref(false)
const importing = ref(false)
const engineText = ref('检测中…')
const engineError = ref('')
const testText = ref('')
const testResult = ref(null)
const boxA = ref(true)
const boxB = ref(true)
const opt = reactive({ interval: 1000, threshold: 0.6, opacity: 0.9 })
const renameDlg = reactive({ show: false, id: '', name: '' })
const boxDebug = ref({ A: null, B: null })
const ocrMs = ref(0)
const captureError = ref('')
const sessionType = ref('')

function fmtTime(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function applyState(s) {
  banks.value = s.banks || []
  questionCount.value = s.questionCount || 0
  scanning.value = !!s.scanning
  engineError.value = s.engineError || ''
  if (s.boxDebug) boxDebug.value = s.boxDebug
  if (s.ocrMs !== undefined) ocrMs.value = s.ocrMs
  if (s.captureError !== undefined) captureError.value = s.captureError
  if (s.sessionType !== undefined) sessionType.value = s.sessionType
  if (s.settings) {
    boxA.value = !!s.settings.boxes.A.visible
    boxB.value = !!s.settings.boxes.B.visible
    opt.interval = s.settings.interval
    opt.threshold = s.settings.threshold
    opt.opacity = s.settings.opacity
  }
}

async function refresh() {
  try {
    applyState(await qs().getState())
  } catch (e) {
    MessagePlugin.error('搜题服务不可用：' + (e.message || e))
  }
  try {
    const t = await qs().selftest()
    engineText.value = t.ok
      ? `onnxruntime ${t.ortVersion} · 模型齐备`
      : `异常：${t.missing.length ? '缺模型 ' + t.missing.join(',') : 'onnxruntime 未加载'}`
  } catch (e) {
    engineText.value = '自检失败：' + (e.message || e)
  }
}

async function downloadTemplate() {
  try {
    const XLSX = await import('xlsx')
    // 三行示例分别覆盖 单选/多选/判断；与导入器（bank-store）字段口径一致，闭环可导
    const rows = [
      ['题干', '答案', '选项A', '选项B', '选项C', '选项D', '选项E', '选项F', '题型', '解析'],
      ['示例单选：我国安全生产工作的方针是？', 'A', '安全第一、预防为主、综合治理', '质量第一', '效率优先', '效益优先', '', '', '', '2014 年安全生产法修订确立'],
      ['示例多选：下列属于个人防护用品的有？', 'ABD', '安全帽', '防护手套', '普通眼镜', '防护鞋', '', '', '多选', ''],
      ['示例判断：安全生产，人人有责。', 'A', '正确', '错误', '', '', '', '', '判断', '判断题 A=正确 B=错误；选项列可留空'],
    ]
    const ws = XLSX.utils.aoa_to_sheet(rows)
    ws['!cols'] = [{ wch: 40 }, { wch: 6 }, { wch: 22 }, { wch: 22 }, { wch: 22 }, { wch: 22 }, { wch: 12 }, { wch: 12 }, { wch: 8 }, { wch: 30 }]
    const wb = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(wb, ws, '题库模板')
    const bytes = XLSX.write(wb, { type: 'array', bookType: 'xlsx' })
    const saved = await qs().saveFile({ defaultName: '题库模板.xlsx', bytes })
    if (saved) MessagePlugin.success('模板已保存：' + saved)
  } catch (e) {
    MessagePlugin.error('模板生成失败：' + (e.message || e))
  }
}

async function importBank() {
  const file = await pickFile([{ name: 'Excel 题库', extensions: ['xlsx', 'xls'] }])
  if (!file) return
  importing.value = true
  try {
    const XLSX = await import('xlsx')   // 按需加载：题库解析仅导入时用到，不拖慢主包
    const buf = await qs().readFile(file)
    const wb = XLSX.read(new Uint8Array(buf), { type: 'array' })
    // 合并全部 sheet：首个 sheet 保留表头行，后续 sheet 丢弃重复表头
    const rows = []
    wb.SheetNames.forEach((name, i) => {
      const rs = XLSX.utils.sheet_to_json(wb.Sheets[name], { header: 1, defval: '' })
      if (!rs.length) return
      if (i > 0) {
        const head = rs[0].map((c) => String(c).trim())
        if (head.includes('题干') || head.includes('答案')) rs.shift()
      }
      rows.push(...rs)
    })
    const r = await qs().bankImport({ name: '', sourceFile: file, rows })
    MessagePlugin.success(`导入完成：${r.count} 题${r.dropped ? `（去重/无效 ${r.dropped} 行）` : ''}`)
    banks.value = r.banks
    questionCount.value = r.questionCount
  } catch (e) {
    MessagePlugin.error('导入失败：' + (e.message || e))
  } finally {
    importing.value = false
  }
}

async function setEnabled(b, v) {
  const s = await qs().bankSetEnabled(b.id, v)
  banks.value = s.banks
  questionCount.value = s.questionCount
}

async function removeBank(b) {
  const s = await qs().bankRemove(b.id)
  banks.value = s.banks
  questionCount.value = s.questionCount
  MessagePlugin.success('已删除「' + b.name + '」')
}

function openRename(b) {
  renameDlg.id = b.id
  renameDlg.name = b.name
  renameDlg.show = true
}

async function doRename() {
  const s = await qs().bankRename(renameDlg.id, renameDlg.name)
  banks.value = s.banks
  renameDlg.show = false
}

async function testMatch() {
  if (!testText.value.trim()) return
  testResult.value = await qs().testMatch(testText.value)
}

async function start() {
  try {
    applyState(await qs().start())
  } catch (e) {
    MessagePlugin.warning(e.message || String(e))
  }
}

async function stop() {
  applyState(await qs().stop())
}

async function setBox(box, v) {
  applyState(await qs().setBoxVisible(box, v))
}

async function resetBoxes() {
  applyState(await qs().resetBoxes())
  MessagePlugin.success('扫描框位置已重置')
}

async function applyOptions() {
  applyState(await qs().setOptions({ interval: opt.interval, threshold: opt.threshold, opacity: opt.opacity }))
}

async function debugCapture() {
  try {
    const file = await qs().debugCapture()
    if (file) {
      MessagePlugin.success('截图已保存：' + file)
      showItem(file)
    }
  } catch (e) {
    MessagePlugin.warning(e.message || String(e))
  }
}

let off = null
let pollTimer = null
onMounted(() => {
  refresh()
  // 扫描期间每 2s 拉一次状态喂运行诊断区
  pollTimer = setInterval(() => { if (scanning.value) refresh() }, 2000)
  // 托盘「停止搜题」/框上「×」等壳层动作回同步页面状态
  off = qs().onEvent((ev) => {
    if (ev.type === 'scan') scanning.value = ev.scanning
    if (ev.type === 'boxHidden' && (ev.box === 'A' || ev.box === 'B')) {
      if (ev.box === 'A') boxA.value = false
      else boxB.value = false
    }
    if (ev.type === 'engine') engineError.value = ev.ready ? '' : (ev.error || '')
  })
})
onActivated(refresh)
onUnmounted(() => {
  if (off) off()
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
.bankrow {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 0; border-bottom: 1px solid #E5E6EB;
}
.bankrow:last-of-type { border-bottom: none; }
.bankname { font-weight: 600; color: #1D2129; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.banktime { font-variant-numeric: tabular-nums; }
.ansbadge {
  display: inline-block; min-width: 28px; text-align: center;
  background: #FFECE8; color: #F53F3F; font-weight: 700;
  border-radius: 4px; padding: 2px 8px; letter-spacing: 1px;
}
.optrow { display: flex; align-items: center; gap: 10px; margin-top: 8px; }
.optrow .muted { flex: none; width: 96px; }
.optval { flex: none; width: 52px; text-align: right; color: #4E5969; font-variant-numeric: tabular-nums; font-size: 13px; }
.sec-sub { font-size: 13px; font-weight: 600; color: #1D2129; margin-top: 14px; margin-bottom: 6px; }
.diag { display: flex; align-items: center; gap: 8px; padding: 3px 0; }
.diag .boxtag { flex: none; font-size: 11px; color: #0E3DA8; background: #E8F0FF; border-radius: 4px; padding: 1px 6px; }
.diagtxt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
