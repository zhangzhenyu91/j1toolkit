<template>
  <div class="sync-wrap">
    <!-- 内网 U 盘同步 -->
    <div class="card sync-card">
      <div class="sync-h">
        <b>内网同步</b>
        <span class="sync-state" :class="intranetStateClass">{{ intranetStateText }}</span>
      </div>
      <div class="sync-meta">
        <template v-if="syncErr">{{ syncErr }}</template>
        <template v-else-if="sync">
          <template v-if="sync.running"
            >同步轮次进行中，结果将写入最近同步并推送站内通知。</template
          >
          <template v-else-if="sync.enabled === false">
            未配置同步设备（CROSSING_SYNC_DEVICE_DDNS），同步停用；配置后方可手动或夜间自动同步。
          </template>
          <template v-else>
            每日夜间自动取件同步
            <template v-if="sync.nextNight"> · 下次 {{ fmtDate(sync.nextNight) }}</template>
            <template v-if="sync.last">
              · 最近 {{ fmtDate(sync.last.created_at) }}（{{
                sync.last.kind === 'manual' ? '手动' : sync.last.kind
              }}）
              {{ sync.last.result }}
            </template>
            <template v-else> · 尚无同步记录</template>
          </template>
        </template>
        <template v-else>同步状态加载中…</template>
      </div>
      <div class="sync-acts">
        <t-button
          size="small"
          theme="primary"
          :disabled="syncLoading || (sync && sync.running)"
          :loading="syncLoading"
          @click="pushIntranet"
        >
          同步至内网
        </t-button>
        <t-button size="small" variant="outline" :disabled="syncLoading" @click="loadSync"
          >刷新状态</t-button
        >
      </div>
    </div>

    <!-- 知识库（WeKnora）同步 -->
    <div class="card sync-card">
      <div class="sync-h">
        <b>知识库同步</b>
        <span class="sync-state" :class="wkStateClass">{{ wkStateText }}</span>
      </div>
      <div class="sync-meta">
        按线路一表 xlsx 快照推送 WeKnora 知识库（内容哈希增量，变了删旧传新）。
        <template v-if="wkErr">当前：{{ wkErr }}</template>
        <template v-else-if="wkResult">
          上次结果：新增 {{ wkResult.created }} · 更新 {{ wkResult.updated }} · 删除
          {{ wkResult.deleted }} · 跳过 {{ wkResult.skipped }}
        </template>
      </div>
      <div v-if="wkResult && wkResult.errors && wkResult.errors.length" class="sync-errs">
        <div class="sync-errs-h">失败明细（{{ wkResult.errors.length }} 项）：</div>
        <div v-for="(e, i) in wkResult.errors" :key="i" class="sync-err">{{ e }}</div>
      </div>
      <div class="sync-acts">
        <t-button
          size="small"
          theme="primary"
          :disabled="wkLoading"
          :loading="wkLoading"
          @click="pushWeKnora"
        >
          同步知识库
        </t-button>
      </div>
    </div>
  </div>
</template>

<script setup>
// 同步面板（班管/超管）：内网 U 盘同步状态与手动触发 + 知识库快照同步。
// 未配置/无权限/同步中均按后端错误如实提示；同步进行中仅表示已发起，完成以状态与站内通知为准，不假成功。
import { computed, onMounted, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import * as api from './api.js';
import { fmtDate } from './util.js';

const props = defineProps({
  teamId: { type: Number, default: 0 },
});

const sync = ref(null); // {enabled,running,last,nextNight}
const syncErr = ref('');
const syncLoading = ref(false);
const wkResult = ref(null); // {ok,created,updated,deleted,skipped,errors}
const wkErr = ref('');
const wkLoading = ref(false);

const intranetStateText = computed(() => {
  if (syncErr.value) return '状态未知';
  if (!sync.value) return '加载中';
  if (sync.value.running) return '同步中…';
  if (sync.value.enabled === false) return '同步未启用';
  return '已启用';
});
const intranetStateClass = computed(() => {
  if (syncErr.value || !sync.value) return 'st-mut';
  if (sync.value.running) return 'st-run';
  if (sync.value.enabled === false) return 'st-off';
  return 'st-on';
});
const wkStateText = computed(() => {
  if (wkLoading.value) return '同步中…';
  if (wkErr.value) return '未配置 / 不可用';
  if (wkResult.value) return wkResult.value.ok ? '已同步' : '部分失败';
  return '待同步';
});
const wkStateClass = computed(() => {
  if (wkLoading.value) return 'st-run';
  if (wkErr.value) return 'st-off';
  if (wkResult.value) return wkResult.value.ok ? 'st-on' : 'st-warn';
  return 'st-mut';
});

async function loadSync() {
  syncErr.value = '';
  try {
    sync.value = (await api.getSyncStatus(props.teamId)) || null;
  } catch (e) {
    sync.value = null;
    syncErr.value = e.message || '同步状态读取失败';
  }
}

// pushIntranet 手动触发内网同步：started 仅表示已发起，结果以后续状态/通知为准。
async function pushIntranet() {
  if (syncLoading.value) return;
  syncLoading.value = true;
  try {
    await api.syncPushNow(props.teamId);
    MessagePlugin.success('同步已开始，结果见同步状态与站内通知');
    await loadSync();
  } catch (e) {
    // 未配置（50301）/同步中（42301）/权限不足：按后端文案显式提示，不假成功
    MessagePlugin.error(e.message || '触发同步失败');
  } finally {
    syncLoading.value = false;
  }
}

// pushWeKnora 知识库快照同步：errors 非空如实按部分失败提示。
async function pushWeKnora() {
  if (wkLoading.value) return;
  wkLoading.value = true;
  wkErr.value = '';
  try {
    const d = (await api.weknoraPushNow(props.teamId)) || {};
    wkResult.value = d;
    const errs = d.errors || [];
    if (!d.ok) {
      if (errs.length) MessagePlugin.warning(`同步完成，部分失败（${errs.length} 项）`);
      else MessagePlugin.warning('同步完成，部分失败');
    } else {
      MessagePlugin.success('已同步至知识库');
    }
  } catch (e) {
    wkResult.value = null;
    wkErr.value = e.message || '知识库同步失败';
    MessagePlugin.error(wkErr.value);
  } finally {
    wkLoading.value = false;
  }
}

watch(
  () => props.teamId,
  () => loadSync()
);
onMounted(loadSync);
</script>

<style scoped>
.sync-wrap {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.sync-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.sync-h {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sync-h b {
  font-size: 15px;
}
.sync-state {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
}
.st-on {
  color: #0b8a37;
  background: #e8ffea;
}
.st-warn {
  color: #d25f00;
  background: #fff7e8;
}
.st-run {
  color: var(--tk-brand);
  background: #e8f0ff;
}
.st-off {
  color: var(--tk-red);
  background: #ffece8;
}
.st-mut {
  color: var(--tk-ph);
  background: var(--tk-block, #f7f8fa);
}
.sync-meta {
  font-size: 12.5px;
  color: var(--tk-sub);
  line-height: 1.7;
}
.sync-errs {
  border-left: 3px solid var(--tk-red);
  background: #ffece8;
  padding: 8px 12px;
  font-size: 12.5px;
}
.sync-errs-h {
  font-weight: 600;
  color: var(--tk-red);
  margin-bottom: 4px;
}
.sync-err {
  color: var(--tk-ink);
  line-height: 1.7;
  word-break: break-all;
}
.sync-acts {
  display: flex;
  gap: 10px;
}
</style>
