<template>
  <div class="history-page">
    <t-card>
      <div class="sec-title">筛选</div>
      <div class="history-filters">
        <div class="filter-field">
          <label>工具</label>
          <t-select v-model="filter.appID" :options="toolOptions" />
        </div>
        <div class="filter-field">
          <label>状态</label>
          <t-select v-model="filter.status" :options="statusOptions" />
        </div>
        <div class="filter-field">
          <label>关键词</label>
          <t-input v-model="filter.keyword" placeholder="搜索工具、任务号、时间或结果摘要" />
        </div>
        <div class="filter-field">
          <label>日期</label>
          <t-input v-model="filter.date" placeholder="可选，YYYY-MM-DD" />
        </div>
      </div>
      <div class="row" style="margin-top: 10px">
        <t-button size="small" variant="outline" @click="resetFilters">清除筛选</t-button>
        <t-button size="small" variant="outline" :loading="loading" @click="reload">刷新</t-button>
      </div>
    </t-card>

    <t-alert v-if="loadErrs.length" theme="error" style="margin-top: 12px">
      {{
        loadErrs.length === 6
          ? '六个工具的历史记录均读取失败：'
          : '部分工具的历史记录读取失败，其他工具结果仍可使用：'
      }}
      {{ loadErrs.join('；') }}
    </t-alert>

    <t-card style="margin-top: 12px">
      <div class="history-list-head">
        <div class="sec-title">执行记录</div>
        <span class="history-count">显示 {{ filtered.length }} / 共 {{ all.length }} 条</span>
        <t-button
          size="small"
          theme="danger"
          variant="outline"
          :loading="clearing"
          :disabled="!all.length || taskStatus.running > 0"
          :title="taskStatus.running > 0 ? '有任务运行中，暂不能清空历史' : ''"
          @click="confirmClearAll"
        >
          一键删除
        </t-button>
      </div>
      <div v-if="!filtered.length" class="history-empty">{{ emptyText }}</div>
      <div v-for="entry in filtered" :key="entry.appID + entry.rec.job_id" class="history-row">
        <div class="history-meta">
          <span class="history-app">{{ entry.appName }}</span>
          <span class="history-time">{{ entry.rec.time }}</span>
          <span class="history-status" :class="historyStatusText(entry.rec).theme">
            {{ historyStatusText(entry.rec).text }}
          </span>
        </div>
        <div class="history-summary" :title="entry.summary">{{ entry.summary }}</div>
        <div class="history-actions">
          <button class="history-link" @click="showLog(entry.rec.job_id)">查看日志</button>
          <button
            v-if="entry.rec.result && entry.rec.result.dir"
            class="history-link"
            @click="openDir(entry)"
          >
            打开目录
          </button>
          <button
            class="history-link"
            :disabled="!hasSnapshot(entry.rec.job_id)"
            :title="
              hasSnapshot(entry.rec.job_id) ? '回填该次任务参数' : '这条记录没有可用的参数快照'
            "
            @click="restore(entry)"
          >
            参数回填
          </button>
          <button class="history-link danger" @click="confirmDelete(entry)">删除</button>
        </div>
      </div>
    </t-card>

    <t-dialog
      v-model:visible="logVisible"
      header="排错日志（供技术支持）"
      width="720px"
      :footer="false"
    >
      <div class="logview" style="height: 52vh">{{ logText }}</div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, onActivated, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { call } from '../api';
import {
  HISTORY_STATUS_OPTIONS,
  HISTORY_TOOL_OPTIONS,
  filterHistory,
  mergeHistory,
  validHistoryDate,
  historyStatusText,
} from '../lib/history.mjs';
import { formatHistorySummary } from '../lib/task-result.mjs';
import { taskStatus } from '../lib/task-status.mjs';
import {
  hasJobParams,
  deleteAppJobParams,
  deleteJobParams,
  readJobParams,
} from '../lib/job-params.mjs';

const emit = defineEmits(['restore']);

const toolOptions = HISTORY_TOOL_OPTIONS.map((item) => ({
  label: item.label,
  value: item.id,
}));
const statusOptions = HISTORY_STATUS_OPTIONS.map((item) => ({
  label: item.label,
  value: item.id,
}));

const all = ref([]);
const loadErrs = ref([]);
const loading = ref(false);
const clearing = ref(false);
const filter = reactive({ appID: '', status: '', keyword: '', date: '' });
const logVisible = ref(false);
const logText = ref('');

const filtered = computed(() => filterHistory(all.value, filter));
const emptyText = computed(() => {
  if (loading.value && all.value.length === 0) return '正在读取六个工具的历史记录…';
  if (!validHistoryDate(filter.date)) {
    return '日期格式不正确，请使用 YYYY-MM-DD，或留空表示不限日期。';
  }
  if (all.value.length === 0 && loadErrs.value.length === 6) {
    return '历史记录加载失败，请检查客户端服务后点击“刷新”。';
  }
  if (all.value.length === 0) return '还没有执行记录。运行任意工具后，记录会出现在这里。';
  return '没有符合当前筛选条件的记录，可调整条件或点击“清除筛选”。';
});

function hasSnapshot(jobID) {
  try {
    return hasJobParams(window.localStorage, jobID);
  } catch (_e) {
    return false;
  }
}

async function reload() {
  // 激活时与手动刷新可能重叠：并发重入会让 loading 反复翻转、结果互相覆盖
  if (loading.value) return;
  loading.value = true;
  loadErrs.value = [];
  try {
    const tasks = HISTORY_TOOL_OPTIONS.filter((item) => item.id);
    const results = await Promise.allSettled(
      tasks.map((item) => call('getHistory', { app_id: item.id }))
    );
    const records = {};
    const errors = [];
    results.forEach((result, index) => {
      const task = tasks[index];
      if (result.status === 'fulfilled' && Array.isArray(result.value)) {
        records[task.id] = result.value;
      } else {
        records[task.id] = [];
        errors.push(`${task.label}：${result.reason?.message || result.reason || '读取失败'}`);
      }
    });
    all.value = mergeHistory(records).map((entry) => ({
      ...entry,
      summary: formatHistorySummary(entry.appID, entry.rec.result || {}),
    }));
    loadErrs.value = errors;
  } catch (e) {
    loadErrs.value = [`读取执行历史失败：${String((e && e.message) || e)}`];
  } finally {
    loading.value = false;
  }
}

function resetFilters() {
  filter.appID = '';
  filter.status = '';
  filter.keyword = '';
  filter.date = '';
}

async function showLog(jobID) {
  try {
    const result = await call('getJobLog', { job_id: jobID });
    logText.value = result.text || '（日志为空）';
    logVisible.value = true;
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

// 带上 job_id：核心据此回查该次任务档案登记的产物目录，
// 超出档案保留期或历史目录被挪动时也能精确打开这条记录的目录。
async function openDir(entry) {
  const dir = (entry.rec.result || {}).dir;
  if (!dir) return;
  try {
    await call('openPath', { path: dir, job_id: entry.rec.job_id });
  } catch (e) {
    MessagePlugin.error(String(e.message || e));
  }
}

function restore(entry) {
  let snapshot = null;
  try {
    snapshot = readJobParams(window.localStorage, entry.rec.job_id);
  } catch (_e) {
    snapshot = null;
  }
  if (!snapshot) {
    MessagePlugin.warning('这条记录没有可用的参数快照，不会根据旧历史推测参数。');
    return;
  }
  emit('restore', { appID: snapshot.appID, params: snapshot.params });
}

function confirmDelete(entry) {
  const dialog = DialogPlugin.confirm({
    header: '删除执行记录',
    body: `将删除${entry.appName}在 ${entry.rec.time} 的这条执行记录及其任务日志、参数快照。确定继续？`,
    confirmBtn: '删除',
    theme: 'danger',
    onConfirm: async () => {
      dialog.hide();
      try {
        await call('deleteHistory', { app_id: entry.appID, job_id: entry.rec.job_id });
        try {
          deleteJobParams(window.localStorage, entry.rec.job_id);
        } catch (_e) {
          /* 本地快照清理失败不影响记录删除 */
        }
        MessagePlugin.success('已删除');
        reload();
      } catch (e) {
        MessagePlugin.error(String(e.message || e));
      }
    },
  });
}

function confirmClearAll() {
  if (!all.value.length || clearing.value || taskStatus.running > 0) return;
  const dialog = DialogPlugin.confirm({
    header: '清空执行历史',
    body: `将删除全部 ${all.value.length} 条执行记录及对应任务日志、任务档案、参数快照，不可恢复。确定继续？`,
    confirmBtn: '全部删除',
    theme: 'danger',
    onConfirm: async () => {
      dialog.hide();
      clearing.value = true;
      const appIDs = HISTORY_TOOL_OPTIONS.filter((item) => item.id).map((item) => item.id);
      const results = await Promise.allSettled(
        appIDs.map((appID) => call('deleteHistory', { app_id: appID }))
      );
      for (const appID of appIDs) {
        try {
          deleteAppJobParams(window.localStorage, appID);
        } catch (_e) {
          /* 本地快照清理失败不影响记录删除 */
        }
      }
      all.value = [];
      logVisible.value = false;
      clearing.value = false;
      await reload();
      const failed = results.filter((result) => result.status === 'rejected').length;
      if (failed) {
        MessagePlugin.error(`${failed} 个工具的历史记录删除失败，请重试`);
      } else {
        MessagePlugin.success('已清空全部执行历史');
      }
    },
  });
}

onActivated(reload);
</script>
