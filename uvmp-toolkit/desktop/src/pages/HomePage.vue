<template>
  <div>
    <t-card>
      <div class="sec-title">每日派车单同步
        <span class="right">
          <t-button size="small" theme="primary" @click="$emit('goto', 'daily_sync')">前往</t-button>
        </span>
      </div>
      <div class="kv">
        <span class="k">上次成功</span><span>{{ syncStatus.lastSuccess }}</span>
        <span class="k">下次定时</span><span>{{ syncStatus.nextRun }}</span>
        <span class="k">U盘</span><span>{{ syncStatus.usb }}</span>
      </div>
    </t-card>

    <t-card>
      <div class="sec-title">运行环境</div>
      <t-alert v-if="sysError" theme="warning" style="margin-bottom:10px">
        核心调用失败：{{ sysError }}——请检查 Python 核心是否正常运行（见页面顶部提示）
      </t-alert>
      <div v-if="!sys.platform && !sysError" class="muted">环境信息加载中…</div>
      <div class="kv" v-else>
        <span class="k">平台</span><span>{{ sys.platform }} / {{ sys.machine }}</span>
        <span class="k">Python 核心</span><span>{{ sys.python }}</span>
        <span class="k">浏览器</span><span>{{ sys.chrome || '未找到（PDF 渲染不可用，请在 config.ini [render] chrome= 指定）' }}</span>
        <span class="k">配置文件</span><span>{{ sys.config_path }}</span>
        <span class="k">状态目录</span><span>{{ sys.state_dir }}</span>
      </div>
    </t-card>
    <t-card>
      <div class="sec-title">壹匣小程序</div>
      <img class="mp-promo" :src="mpPromo" alt="微信搜一搜 Shade 壹匣" draggable="false">
    </t-card>
  </div>
</template>

<script setup>
import { onActivated, reactive, ref } from 'vue'
import { call } from '../api'
import mpPromo from '../assets/mp-promo.jpg'

const sys = reactive({})
const sysError = ref('')
const syncStatus = reactive({ lastSuccess: '-', nextRun: '-', usb: '-' })

// keep-alive 下 onActivated 首次显示与每次切回都触发（替代 onMounted 做数据刷新）
async function reload() {
  try {
    Object.assign(sys, await call('getSystemInfo'))
  } catch (e) {
    sysError.value = String(e.message || e)
  }
  try {
    const st = await call('getAppStatus', { app_id: 'daily_sync' })
    const stamp = st.stamp || {}
    const usb = st.usb || {}
    syncStatus.lastSuccess = stamp.time
      ? stamp.time + (stamp.count != null ? `（${stamp.count} 条）` : '') : '从未执行'
    syncStatus.nextRun = (st.schedule || {}).next_run || '-'
    syncStatus.usb = usb.mount || `未检测到（卷标 ${usb.label || ''}）`
  } catch (e) { /* 忽略 */ }
}
onActivated(reload)
</script>

<style scoped>
/* 小程序宣传图：卡片内居中限宽。原图四角是烘焙成黑色的圆角（原图 2172px 宽、半径约 90-100px），
   显示宽 ≤560px 时折算约 26px——border-radius 需给到 28px 才能完整盖住烘焙黑角（8px 盖不住），
   被裁掉的四角为纯白内容，无信息损失 */
.mp-promo { display: block; width: 100%; max-width: 560px; margin: 0 auto; border-radius: 28px; }
</style>
