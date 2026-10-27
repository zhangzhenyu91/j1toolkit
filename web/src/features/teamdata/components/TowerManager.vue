<template>
  <div>
    <div class="card">
      <div class="toolbar">
        <h2 class="card-title" style="margin: 0">杆塔信息</h2>
        <div class="spacer"></div>
        <t-button variant="outline" @click="downloadTemplate">下载模板</t-button>
        <t-button variant="outline" @click="pickImport">导入 Excel</t-button>
        <input
          ref="importInput"
          type="file"
          accept=".xlsx"
          class="td-hidden-input"
          @change="onImport"
        />
      </div>

      <div class="muted">
        模板列：电压等级 / 线路名称 / 杆塔号 / 经度 /
        纬度；导入为全量替换本班组坐标，行序即导入顺序。
      </div>
      <div v-if="error" class="td-error">{{ error }}</div>
      <div v-if="importMsg" class="td-ok">{{ importMsg }}</div>

      <t-table
        :data="towers"
        :columns="columns"
        :loading="loading"
        row-key="id"
        size="small"
        :bordered="false"
      >
        <template #lng="{ row }">
          <span class="num">{{ row.lng }}</span>
        </template>
        <template #lat="{ row }">
          <span class="num">{{ row.lat }}</span>
        </template>
      </t-table>
    </div>

    <!-- 线路聚合 -->
    <div class="card">
      <h2 class="card-title">线路汇总</h2>
      <div v-if="!lines.length" class="muted">暂无线路数据</div>
      <t-table
        v-else
        :data="lines"
        :columns="lineColumns"
        :loading="linesLoading"
        row-key="line_name"
        size="small"
        :bordered="false"
      >
        <template #tower_count="{ row }">
          <span class="num">{{ row.tower_count }}</span>
        </template>
      </t-table>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { downloadTowerTemplate, fetchLines, importTowers, listTowers } from '../api.js';

// 杆塔信息管理：坐标列表（导入序）+ 模板下载 + Excel 导入（全量替换）+ 线路聚合。
const towers = ref([]);
const lines = ref([]);
const loading = ref(false);
const linesLoading = ref(false);
const error = ref('');
const importMsg = ref('');
const importInput = ref(null);

const columns = [
  { colKey: 'voltage_level', title: '电压等级', width: 120 },
  { colKey: 'line_name', title: '线路名称' },
  { colKey: 'tower_no', title: '杆塔号', width: 120 },
  { colKey: 'lng', title: '经度', cell: 'lng', width: 130 },
  { colKey: 'lat', title: '纬度', cell: 'lat', width: 130 },
  { colKey: 'sort', title: '行序', width: 80 },
];
const lineColumns = [
  { colKey: 'voltage_level', title: '电压等级', width: 120 },
  { colKey: 'line_name', title: '线路名称' },
  { colKey: 'tower_count', title: '杆塔数', cell: 'tower_count', width: 100 },
];

// reload 杆塔列表 + 线路聚合。
async function reload() {
  loading.value = true;
  linesLoading.value = true;
  error.value = '';
  try {
    const [t, l] = await Promise.all([listTowers(), fetchLines()]);
    towers.value = t || [];
    lines.value = l || [];
  } catch (e) {
    error.value = e.message || '加载失败';
    towers.value = [];
    lines.value = [];
  } finally {
    loading.value = false;
    linesLoading.value = false;
  }
}

// downloadTemplate 杆塔坐标模板 xlsx。
async function downloadTemplate() {
  error.value = '';
  try {
    await downloadTowerTemplate();
  } catch (e) {
    error.value = e.message || '模板下载失败';
  }
}

function pickImport() {
  importMsg.value = '';
  importInput.value && importInput.value.click();
}

// onImport Excel 导入（全量替换）。
async function onImport(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  error.value = '';
  try {
    const data = await importTowers(file);
    importMsg.value =
      `已导入 ${data.imported} 条坐标` + (data.skipped ? `，跳过 ${data.skipped} 条` : '');
    reload();
  } catch (e) {
    error.value = e.message || '导入失败';
    MessagePlugin.error(e.message || '导入失败');
  }
}

onMounted(reload);
</script>

<style scoped>
.td-error {
  margin: 8px 0;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
}
.td-ok {
  margin: 8px 0;
  background: var(--tk-green-soft);
  border-left: 3px solid var(--tk-green);
  padding: 8px 10px;
  font-size: 12px;
  color: #0b8a37;
}
.td-hidden-input {
  display: none;
}
</style>
