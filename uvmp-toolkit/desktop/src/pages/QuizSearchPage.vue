<template>
  <div class="quiz-page">
    <div class="page-split">
      <div class="page-split-col">
        <t-card>
          <div class="sec-title">准备题库</div>
          <div class="row">
            <t-button variant="outline" :loading="importing" @click="importBank"
              >导入 Excel 题库…</t-button
            >
            <t-button variant="text" :loading="templating" @click="downloadTemplate"
              >下载导入模板</t-button
            >
          </div>
          <p class="muted" style="margin: 8px 0">
            支持 xls/xlsx（表头：题干、答案、选项 A～F），多个启用题库合并搜题。
          </p>
          <t-alert v-if="!banks.length" theme="info">
            尚未导入题库——搜题前请先导入至少一个题库。
          </t-alert>
          <t-alert v-if="bankNotice" theme="warning" style="margin-top: 8px">
            {{ bankNotice }}
          </t-alert>
          <div v-for="b in banks" :key="b.id" class="bankrow">
            <t-switch v-model="b.enabled" size="small" @change="(v) => setEnabled(b, v)" />
            <span class="bankname" :title="b.sourceFile">{{ b.name }}</span>
            <span class="muted banktime">{{ b.count }} 题 · {{ fmtTime(b.importedAt) }}</span>
            <t-button size="small" variant="text" @click="openRename(b)">重命名</t-button>
            <t-popconfirm content="确定删除该题库？" @confirm="removeBank(b)">
              <t-button size="small" variant="text" theme="danger">删除</t-button>
            </t-popconfirm>
          </div>
          <div v-if="banks.length" class="muted">启用题库合计 {{ questionCount }} 题参与搜题。</div>
        </t-card>

        <t-card>
          <div class="sec-title">开始搜题</div>
          <t-button
            class="quiz-start-btn"
            block
            theme="primary"
            :disabled="!questionCount && !scanning"
            @click="scanning ? stop() : start()"
          >
            {{ scanning ? '停止搜题' : '开始搜题' }}
          </t-button>
          <div class="quiz-status">{{ scanStatus }}</div>
          <p class="muted" style="margin: 8px 0 10px">
            开始后主窗口会隐藏，请拖动搜题区域分别框住题目。
          </p>
          <div class="row">
            <t-checkbox v-model="boxA" @change="(v) => setBox('A', v)">搜题区域 A</t-checkbox>
            <t-checkbox v-model="boxB" @change="(v) => setBox('B', v)">搜题区域 B</t-checkbox>
            <t-button size="small" variant="outline" @click="resetBoxes">重置区域位置</t-button>
          </div>
        </t-card>
      </div>

      <div class="page-split-col">
        <CollapsibleCard v-model:open="answerOpen" title="查看答案">
          <div class="row">
            <t-input
              v-model="testText"
              class="grow"
              placeholder="粘贴题干，自测题库并查看答案"
              @enter="testMatch"
            />
            <t-button variant="outline" @click="testMatch">题干自测</t-button>
          </div>
          <p class="muted" style="margin: 8px 0">
            实时答案显示在答案窗口；题干自测可提前确认题库是否可用。
          </p>
          <div v-if="testResult">
            <template v-if="testResult.matched">
              <span class="ansbadge">{{ testResult.question.answer }}</span>
              <span style="margin-left: 8px">{{ testResult.question.content }}</span>
              <div class="muted" style="margin-top: 4px">
                {{ testResult.question.bankName }} · 匹配度
                {{ Math.round(testResult.score * 100) }}%
              </div>
              <div v-if="testResult.question.analysis" class="muted">
                解析：{{ testResult.question.analysis }}
              </div>
            </template>
            <template v-else>
              <t-tag theme="warning" variant="light">未匹配到题目</t-tag>
              <span v-if="testResult.near" class="muted">
                最接近：{{ testResult.near.content.slice(0, 40) }}…</span
              >
            </template>
          </div>
        </CollapsibleCard>

        <CollapsibleCard v-model:open="advancedOpen" title="高级设置">
          <div class="optrow">
            <span class="muted">文字识别间隔（毫秒）</span>
            <t-slider
              v-model="opt.interval"
              :min="500"
              :max="3000"
              :step="100"
              class="grow"
              :tooltip-props="{ content: opt.interval + 'ms' }"
              @change-end="applyOptions"
            />
            <span class="optval">{{ opt.interval }} 毫秒</span>
          </div>
          <div class="optrow">
            <span class="muted">匹配阈值（越高越严格）</span>
            <t-slider
              v-model="opt.threshold"
              :min="0.3"
              :max="0.9"
              :step="0.05"
              class="grow"
              @change-end="applyOptions"
            />
            <span class="optval">{{ Number(opt.threshold).toFixed(2) }}</span>
          </div>
          <div class="optrow">
            <span class="muted">答案窗透明度</span>
            <t-slider
              v-model="opt.opacity"
              :min="0.3"
              :max="1"
              :step="0.05"
              class="grow"
              @change-end="applyOptions"
            />
            <span class="optval">{{ Math.round(opt.opacity * 100) }}%</span>
          </div>
        </CollapsibleCard>

        <CollapsibleCard v-model:open="diagOpen" title="技术诊断（供技术支持）">
          <p class="muted" style="margin-top: 0">{{ scanDiag }}</p>
          <t-alert v-if="opNotice" theme="warning" style="margin: 8px 0">{{ opNotice }}</t-alert>
          <t-alert v-if="engineError" theme="error" style="margin: 8px 0">
            OCR 引擎异常：{{ engineError }}
          </t-alert>
          <t-alert v-if="sessionType === 'wayland'" theme="error" style="margin: 8px 0">
            当前为 Wayland 会话，截屏不可用——请注销后在登录界面选择 X11 会话登录。
          </t-alert>
          <t-alert v-if="captureError" theme="warning" style="margin: 8px 0">{{
            captureError
          }}</t-alert>
          <div class="row">
            <t-button size="small" variant="outline" @click="debugCapture">保存题图</t-button>
            <t-button size="small" variant="outline" :loading="selfTesting" @click="runSelftest"
              >引擎自检</t-button
            >
            <span class="muted">{{ selfResult || engineText }}</span>
          </div>
        </CollapsibleCard>
      </div>
    </div>

    <t-dialog
      v-model:visible="renameDlg.show"
      header="重命名题库"
      :confirm-btn="'保存'"
      @confirm="doRename"
    >
      <t-input v-model="renameDlg.name" placeholder="题库名称" />
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, onActivated, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import { pickFile, qs, showItem } from '../api';
import CollapsibleCard from '../components/CollapsibleCard.vue';

const banks = ref([]);
const questionCount = ref(0);
const scanning = ref(false);
const importing = ref(false);
const templating = ref(false);
const bankNotice = ref('');
const opNotice = ref('');
const selfTesting = ref(false);
const engineText = ref('检测中…');
const engineReady = ref(false);
const engineError = ref('');
const testText = ref('');
const testResult = ref(null);
const boxA = ref(true);
const boxB = ref(true);
const opt = reactive({ interval: 1000, threshold: 0.6, opacity: 0.9 });
const renameDlg = reactive({ show: false, id: '', name: '' });
const boxDebug = ref({ A: null, B: null });
const ocrMs = ref(0);
const captureError = ref('');
const sessionType = ref('');
const answerOpen = ref(false);
const advancedOpen = ref(false);
const diagOpen = ref(false);
const selfResult = ref('');

const scanStatus = computed(() => {
  if (scanning.value) return '搜题中（主窗口已隐藏，答案实时显示）';
  if (questionCount.value > 0) return `已停止（启用题库 ${questionCount.value} 题）`;
  return '已停止（尚未导入可用题库）';
});

const scanDiag = computed(() => {
  const parts = [`引擎：${engineStatusText.value}`];
  if (captureError.value) parts.push('截屏：' + captureError.value);
  else if (scanning.value) parts.push('截屏：正常');
  if (ocrMs.value > 0) parts.push(`OCR ${ocrMs.value}ms`);
  parts.push(`启用题库 ${questionCount.value} 题`);
  if (sessionType.value) parts.push('会话 ' + sessionType.value);
  for (const id of ['A', 'B']) {
    const debug = boxDebug.value[id];
    if (!debug) {
      parts.push(`区域 ${id} 尚未出帧`);
    } else if (debug.matched) {
      parts.push(`区域 ${id} 已命中`);
    } else if (debug.snippet) {
      parts.push(`区域 ${id} 未命中（识别：${debug.snippet}）`);
    } else {
      parts.push(`区域 ${id} 未命中（未识别到文字：区域内无字或截屏为空）`);
    }
  }
  return parts.join('；');
});

const engineStatusText = computed(() => {
  if (engineError.value) return '异常（' + engineError.value + '）';
  // 以主进程的 engineReady 为准：页面自己的自检结果只用来解释「为什么没就绪」
  if (engineReady.value) return '就绪';
  if (scanning.value) return '加载中';
  return '未启动';
});

function fmtTime(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

function applyState(s) {
  banks.value = s.banks || [];
  questionCount.value = s.questionCount || 0;
  scanning.value = !!s.scanning;
  engineError.value = s.engineError || '';
  engineReady.value = !!s.engineReady;
  if (s.boxDebug) boxDebug.value = s.boxDebug;
  if (s.ocrMs !== undefined) ocrMs.value = s.ocrMs;
  if (s.captureError !== undefined) captureError.value = s.captureError;
  if (s.sessionType !== undefined) sessionType.value = s.sessionType;
  if (s.bankNotice !== undefined) bankNotice.value = s.bankNotice || '';
  if (s.notice !== undefined) opNotice.value = s.notice || '';
  if (s.settings) {
    boxA.value = !!s.settings.boxes.A.visible;
    boxB.value = !!s.settings.boxes.B.visible;
    opt.interval = s.settings.interval;
    opt.threshold = s.settings.threshold;
    opt.opacity = s.settings.opacity;
  }
}

async function refresh() {
  try {
    applyState(await qs().getState());
  } catch (e) {
    MessagePlugin.error('搜题服务不可用：' + (e.message || e));
  }
}

async function loadEngine() {
  try {
    const t = await qs().selftest();
    engineText.value = t.ok
      ? `onnxruntime ${t.ortVersion} · 模型齐备`
      : `异常：${t.missing.length ? '缺模型 ' + t.missing.join(',') : 'onnxruntime 未加载'}`;
  } catch (e) {
    engineText.value = '自检失败：' + (e.message || e);
  }
}

async function runSelftest() {
  selfTesting.value = true;
  selfResult.value = '自检运行中…';
  try {
    const t = await qs().selftest();
    if (t.ok) {
      selfResult.value = '自检通过（模型齐备）';
    } else {
      selfResult.value = t.missing.length
        ? '自检未通过，缺少：' + t.missing.join(',')
        : '自检未通过（onnxruntime 未加载）';
      diagOpen.value = true;
    }
    await loadEngine();
  } catch (e) {
    selfResult.value = '自检失败：' + (e.message || e);
    diagOpen.value = true;
  } finally {
    selfTesting.value = false;
  }
}

watch([engineError, captureError, sessionType], ([engine, capture, session]) => {
  if (engine || capture || session === 'wayland') diagOpen.value = true;
});

async function downloadTemplate() {
  templating.value = true;
  try {
    // 题库解析/模板模块是纯 ESM（.mjs）：Vite dev 不做 CJS 转换，.cjs 里的
    // module.exports/require 会原样进浏览器直接报错，dev 下选择模板即失败。
    const { buildTemplateBlob, TEMPLATE_FILE_NAME } =
      await import('../../electron/quizsearch/template.mjs');
    const blob = await buildTemplateBlob();
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const saved = await qs().saveFile({ defaultName: TEMPLATE_FILE_NAME, bytes });
    if (saved) MessagePlugin.success('模板已保存：' + saved);
  } catch (e) {
    MessagePlugin.error('模板生成失败：' + (e.message || e));
  } finally {
    templating.value = false;
  }
}

async function importBank() {
  const file = await pickFile([{ name: 'Excel 题库', extensions: ['xlsx', 'xls'] }]);
  if (!file) return;
  importing.value = true;
  try {
    // 解析在渲染层完成（read-excel-file / 内置 .xls 读取器都由 Vite 打包）：
    // 主进程侧不引 Excel 解析器，安装包里才不会缺依赖；xls/xlsx 以文件内容判定。
    const { readWorkbookRows } = await import('../../electron/quizsearch/workbook.mjs');
    const bytes = await qs().readFile(file);
    const rows = await readWorkbookRows(new Uint8Array(bytes));
    const r = await qs().bankImport({ name: '', sourceFile: file, rows });
    MessagePlugin.success(
      `导入完成：${r.count} 题${r.dropped ? `（去重/无效 ${r.dropped} 行）` : ''}`
    );
  } catch (e) {
    MessagePlugin.error('导入失败：' + (e.message || e));
  } finally {
    importing.value = false;
    await refresh(); // 无论成败都以主进程状态为准
  }
}

// 所有写操作后都重拉 getState：主进程才是权威状态（IPC 失败时界面不留在乐观值上）
async function setEnabled(b, v) {
  try {
    applyState(await qs().bankSetEnabled(b.id, v));
  } catch (e) {
    MessagePlugin.error('启用状态切换失败：' + (e.message || e));
    await refresh();
  }
}

async function removeBank(b) {
  try {
    applyState(await qs().bankRemove(b.id));
    MessagePlugin.success('已删除「' + b.name + '」');
  } catch (e) {
    MessagePlugin.error('删除失败：' + (e.message || e));
    await refresh();
  }
}

function openRename(b) {
  renameDlg.id = b.id;
  renameDlg.name = b.name;
  renameDlg.show = true;
}

async function doRename() {
  try {
    applyState(await qs().bankRename(renameDlg.id, renameDlg.name));
    renameDlg.show = false;
  } catch (e) {
    MessagePlugin.error('重命名失败：' + (e.message || e));
    await refresh();
  }
}

async function testMatch() {
  if (!testText.value.trim()) return;
  try {
    testResult.value = await qs().testMatch(testText.value);
  } catch (e) {
    MessagePlugin.error('自测失败：' + (e.message || e));
  }
}

async function start() {
  try {
    applyState(await qs().start());
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
  }
}

async function stop() {
  try {
    applyState(await qs().stop());
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
    await refresh();
  }
}

async function setBox(box, v) {
  try {
    applyState(await qs().setBoxVisible(box, v));
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
    await refresh();
  }
}

async function resetBoxes() {
  try {
    applyState(await qs().resetBoxes());
    MessagePlugin.success('扫描框位置已重置');
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
    await refresh();
  }
}

async function applyOptions() {
  try {
    applyState(
      await qs().setOptions({
        interval: opt.interval,
        threshold: opt.threshold,
        opacity: opt.opacity,
      })
    );
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
    await refresh();
  }
}

async function debugCapture() {
  try {
    const r = await qs().debugCapture();
    if (r && r.file) {
      // 受控临时目录：不写用户的「下载」目录，位置在提示里明确告知
      MessagePlugin.success('截图已保存到临时目录：' + r.dir);
      showItem(r.file);
    }
  } catch (e) {
    MessagePlugin.warning(e.message || String(e));
  }
}

let off = null;
let pollTimer = null;
onMounted(async () => {
  await Promise.all([refresh(), loadEngine()]);
  // 常驻轮询：搜题中看实时诊断；停止后也要对齐（托盘「停止搜题」等外部入口改状态时这里才跟得上）
  pollTimer = setInterval(refresh, 2000);
  // 主进程事件只作为「有变化」的信号，界面一律回拉 getState 取权威值
  off = qs().onEvent((ev) => {
    if (ev.type === 'boxHidden' && (ev.box === 'A' || ev.box === 'B')) {
      if (ev.box === 'A') boxA.value = false;
      else boxB.value = false;
    }
    if (ev.type === 'engine') {
      engineReady.value = !!ev.ready;
      engineError.value = ev.ready ? '' : ev.error || '';
    }
    refresh();
  });
});
onActivated(() => {
  refresh();
  loadEngine();
});
onUnmounted(() => {
  if (off) off();
  if (pollTimer) clearInterval(pollTimer);
});
</script>

<style scoped>
.bankrow {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 0;
  border-bottom: 1px solid var(--tk-line);
}
.bankrow:last-of-type {
  border-bottom: none;
}
.bankname {
  font-weight: 600;
  color: var(--tk-ink);
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.banktime {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
}
.quiz-start-btn {
  margin-bottom: 10px;
}
.quiz-status {
  font-size: 13px;
  color: var(--tk-sub);
}
.ansbadge {
  display: inline-block;
  min-width: 28px;
  text-align: center;
  background: var(--tk-red-soft);
  color: var(--tk-red);
  font-weight: 700;
  border-radius: 4px;
  padding: 2px 8px;
  letter-spacing: 1px;
}
.optrow {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 8px;
}
.optrow .muted {
  flex: none;
  width: 150px;
}
.optval {
  flex: none;
  width: 70px;
  text-align: right;
  color: var(--tk-sub);
  font-variant-numeric: tabular-nums;
  font-size: 13px;
}
</style>
