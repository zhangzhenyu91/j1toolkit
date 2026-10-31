<template>
  <div>
    <!-- 线路表格（电压等级 / 线路名称 / 杆塔数，GET /lines）：坐标供出工日志加水印、商旅打卡带入定位 -->
    <div class="td-dsec">
      <div class="lt-bar">
        <span class="lt-note">杆塔坐标供出工日志加水印照片、商旅打卡带入定位使用</span>
        <span class="lt-btns">
          <button type="button" class="btn btn-sm" :disabled="tplBusy" @click="downloadTemplate">
            <span v-html="icon('download', 14)"></span>下载模板
          </button>
          <button type="button" class="btn btn-sm btn-primary" @click="pickImport">
            <span v-html="icon('upload', 14)"></span>导入 Excel
          </button>
        </span>
      </div>
      <div class="lt-wrap">
        <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
        <div v-else-if="error" class="lt-empty">
          {{ error }}　<a class="linkbtn" @click="reload">重试</a>
        </div>
        <div v-else-if="!lines.length" class="lt-empty">暂无杆塔坐标数据，可下载模板填写后导入</div>
        <table v-else class="lt">
          <thead>
            <tr>
              <th class="lc-v">电压等级</th>
              <th>线路名称</th>
              <th class="lc-c">杆塔数</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="l in lines" :key="`${l.voltage_level}|${l.line_name}`">
              <td class="lc-v">{{ fmtKv(l.voltage_level) }}</td>
              <td>{{ l.line_name || '—' }}</td>
              <td class="lc-c lc-n">{{ l.tower_count == null ? 0 : l.tower_count }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 杆塔坐标明细（导入序全量行；导入为全量替换本班组坐标） -->
    <div class="td-dsec" style="margin-top: 14px">
      <div class="dsec-h">
        <b>杆塔坐标明细</b>
        <span class="cnt">{{ loading ? '加载中…' : `${towers.length} 条` }}</span>
      </div>
      <div class="lt-wrap">
        <div v-if="loading" class="loading"><span class="spin"></span>加载中…</div>
        <div v-else-if="!towers.length" class="lt-empty">暂无杆塔坐标明细</div>
        <table v-else class="lt">
          <thead>
            <tr>
              <th class="lc-v">电压等级</th>
              <th>线路名称</th>
              <th class="lc-c">杆塔号</th>
              <th class="lc-n">经度</th>
              <th class="lc-n">纬度</th>
              <th class="lc-c">行序</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in towers" :key="t.id">
              <td class="lc-v">{{ fmtKv(t.voltage_level) }}</td>
              <td>{{ t.line_name || '—' }}</td>
              <td class="lc-c">{{ t.tower_no || '—' }}</td>
              <td class="lc-n">{{ t.lng }}</td>
              <td class="lc-n">{{ t.lat }}</td>
              <td class="lc-c lc-n">{{ t.sort }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="ol-foot">
        模板列：电压等级 / 线路名称 / 杆塔号 / 经度 / 纬度；导入为全量替换本班组坐标，行序即导入顺序
      </div>
    </div>

    <input
      ref="importInput"
      type="file"
      accept=".xlsx"
      class="td-hidden-input"
      @change="onImport"
    />

    <!-- 导入确认（全量替换本班组杆塔坐标数据）；受控 visible 保退出动画 -->
    <Modal :visible="impConfirm" title="导入杆塔坐标" @close="impConfirm = false">
      <div class="f-lab" style="color: var(--tk-ink); font-size: 13px; letter-spacing: 0">
        导入将全量替换本班组杆塔坐标数据，确定继续吗？
      </div>
      <template #footer>
        <button type="button" class="btn btn-sm" @click="impConfirm = false">取消</button>
        <button type="button" class="btn btn-sm btn-primary" @click="runImport">导入</button>
      </template>
    </Modal>

    <!-- 导入进行中（受控 visible；进行中不随关闭回调卸载，由 impBusy 落下收起） -->
    <Modal :visible="impBusy" title="导入杆塔坐标" :width="280" @close="() => {}">
      <div class="m-loading"><span class="spin"></span>正在导入…</div>
    </Modal>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { createGeneration } from '../../core/async.js';
import { downloadTowerTemplate, fetchLines, importTowers, listTowers } from '../api.js';
import { fmtKv, icon } from '../helpers.js';
import Modal from './Modal.vue';

// 杆塔信息段（对照旧版 teamdata.html）：主视图 = 线路聚合表（电压等级 / 线路名称 / 杆塔数，GET /lines）；
// 另保留 杆塔坐标明细（GET /towers 全量行）。模板下载 + Excel 导入全量替换（前端先校验 .xlsx 与 10MB）；
// 40304 → 通知整页门控。
const emit = defineEmits(['forbidden']);

const lines = ref([]);
const towers = ref([]);
const loading = ref(false);
const error = ref('');
const tplBusy = ref(false);
const importInput = ref(null);
const impConfirm = ref(false);
const impBusy = ref(false);
const impFile = ref(null);

// gateOnForbidden 40304 → 整页门控。
function gateOnForbidden(e) {
  if (e && e.code === 40304) {
    emit('forbidden');
    return true;
  }
  return false;
}

// reload 线路聚合 + 杆塔明细（契约 data 即数组；防御兼容 {list} 信封）。
// 请求代际防止连续导入/刷新时旧响应覆盖新列表。
const reloadGen = createGeneration();
async function reload() {
  const gen = reloadGen.next();
  loading.value = true;
  error.value = '';
  try {
    const [l, t] = await Promise.all([fetchLines(), listTowers()]);
    if (!reloadGen.isCurrent(gen)) return;
    lines.value = Array.isArray(l) ? l : (l && l.list) || [];
    towers.value = Array.isArray(t) ? t : (t && t.list) || [];
  } catch (e) {
    if (!reloadGen.isCurrent(gen)) return;
    if (gateOnForbidden(e)) return;
    error.value = e.message || '加载失败';
    lines.value = [];
    towers.value = [];
  } finally {
    if (reloadGen.isCurrent(gen)) loading.value = false;
  }
}

// downloadTemplate 杆塔坐标模板 xlsx。
async function downloadTemplate() {
  tplBusy.value = true;
  try {
    await downloadTowerTemplate();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '模板下载失败');
  } finally {
    tplBusy.value = false;
  }
}

function pickImport() {
  importInput.value && importInput.value.click();
}

// onImport 前端先校验扩展名与大小，再走「全量替换」二次确认；选完即清空以便重复选择同名文件。
function onImport(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  if (!/\.xlsx$/i.test(file.name)) {
    MessagePlugin.info('仅支持 .xlsx 文件，请使用模板填写');
    return;
  }
  if (file.size > 10 * 1024 * 1024) {
    MessagePlugin.info('文件大小应在 10MB 以内');
    return;
  }
  impFile.value = file;
  impConfirm.value = true;
}

// runImport 确认后 FormData 上传（全量替换），toast 后端 message 并刷新。
async function runImport() {
  const file = impFile.value;
  if (!file) return;
  impConfirm.value = false;
  impBusy.value = true;
  try {
    const data = await importTowers(file);
    const skipped = data && data.skipped;
    MessagePlugin.success(`导入完成${skipped ? `（跳过 ${skipped} 行）` : ''}`);
    await reload();
  } catch (e) {
    if (!gateOnForbidden(e)) MessagePlugin.error(e.message || '导入失败');
  } finally {
    impBusy.value = false;
    impFile.value = null;
  }
}

onMounted(reload);
</script>

<style scoped>
.td-hidden-input {
  display: none;
}
</style>
