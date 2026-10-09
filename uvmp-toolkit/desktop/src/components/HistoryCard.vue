<template>
  <t-card>
    <div class="sec-title">
      最近执行记录
      <span class="right">
        <t-button size="small" theme="danger" variant="text"
                  :disabled="!history.length" @click="clearAll">清空</t-button>
      </span>
    </div>
    <t-table :data="history" :columns="histCols" size="small" row-key="job_id"
             :max-height="220">
      <template #op="{ row }">
        <t-button size="small" variant="outline" @click="showLog(row)">日志</t-button>
        <t-button v-if="showDir && row.dir" size="small" variant="outline"
                  @click="call('openPath', { path: row.dir })">打开目录</t-button>
        <t-button size="small" theme="danger" variant="text" @click="removeRow(row)">删除</t-button>
      </template>
    </t-table>
  </t-card>

  <t-dialog v-model:visible="logVisible" header="任务日志" width="80vw" :footer="false">
    <div class="logview" style="height:60vh">{{ logText }}</div>
  </t-dialog>
</template>

<script setup>
// 应用「最近执行记录」卡：展示/查看日志/单条删除/清空，各应用页面直接复用（新应用接入同享）。
// 数据链路：state/history/<app_id>.jsonl（getHistory）→ 删除走 deleteHistory（连带任务日志/档案）。
import { computed, onActivated, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { call } from '../api'

const props = defineProps({
  appId: { type: String, required: true },
  formatResult: { type: Function, default: null },   // (result) => 摘要文字
  showDir: { type: Boolean, default: false },        // 是否展示「打开目录」按钮（取 result.dir）
})

const STATUS_CN = { success: '成功', failed: '失败', cancelled: '已取消' }
const history = ref([])
const histCols = computed(() => [
  { colKey: 'time', title: '时间', width: 160 },
  { colKey: 'status', title: '状态', width: 90 },
  { colKey: 'summary', title: '结果', ellipsis: true },
  { colKey: 'op', title: '操作', width: props.showDir ? 200 : 140 },
])
const logVisible = ref(false)
const logText = ref('')

async function reload() {
  try {
    const rows = await call('getHistory', { app_id: props.appId })
    history.value = rows.map((r) => ({
      time: r.time, status: STATUS_CN[r.status] || r.status,
      summary: r.error || (props.formatResult ? props.formatResult(r.result) : ''),
      dir: (r.result || {}).dir || '',
      job_id: r.job_id,
    }))
  } catch (e) { /* 忽略 */ }
}
defineExpose({ reload })

async function showLog(row) {
  try {
    const r = await call('getJobLog', { job_id: row.job_id })
    logText.value = r.text || '（日志为空）'
    logVisible.value = true
  } catch (e) {
    MessagePlugin.error(String(e.message || e))
  }
}

function removeRow(row) {
  const dlg = DialogPlugin.confirm({
    header: '删除执行记录',
    body: `将删除 ${row.time} 的这条执行记录及其任务日志。确定继续？`,
    confirmBtn: '删除',
    theme: 'danger',
    onConfirm: async () => {
      dlg.hide()
      try {
        await call('deleteHistory', { app_id: props.appId, job_id: row.job_id })
        MessagePlugin.success('已删除')
        reload()
      } catch (e) {
        MessagePlugin.error(String(e.message || e))
      }
    },
  })
}

function clearAll() {
  const dlg = DialogPlugin.confirm({
    header: '清空执行记录',
    body: '将删除该应用全部执行记录及其任务日志。确定继续？',
    confirmBtn: '清空',
    theme: 'danger',
    onConfirm: async () => {
      dlg.hide()
      try {
        const r = await call('deleteHistory', { app_id: props.appId })
        let msg = `已清空 ${r.records} 条记录`
        if (r.skipped > 0) msg += `，${r.skipped} 个文件正在使用被跳过`
        MessagePlugin.success(msg)
        reload()
      } catch (e) {
        MessagePlugin.error(String(e.message || e))
      }
    },
  })
}

// keep-alive：子组件随页面激活触发，首次显示与每次切回都刷新
onActivated(reload)
</script>
