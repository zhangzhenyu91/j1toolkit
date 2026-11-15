<template>
  <div class="it-process" :data-state="state">
    <div class="it-work-tabs" role="tablist" aria-label="图片处理方式" @keydown="onTabKeydown">
      <button
        v-for="tab in OPERATION_TABS"
        :key="tab.key"
        :id="`it-tab-${tab.key}`"
        type="button"
        role="tab"
        class="it-work-tab"
        :class="{ 'is-on': operation === tab.key }"
        :aria-selected="operation === tab.key ? 'true' : 'false'"
        :tabindex="operation === tab.key ? 0 : -1"
        :aria-controls="`it-panel-${tab.key}`"
        @click="switchTab(tab.key)"
      >
        {{ OPERATION_LABELS[tab.key] }}
      </button>
    </div>

    <div class="it-shared-note">
      <ITIcon name="info" :size="13" />
      <span>
        <b>三个任务共用</b>：EXIF 方向在解码时一次性摆正，转换 / 压缩 /
        尺寸都基于摆正后的画面，输出不写旧方向标记；同一批
        <b>最多 2 张并行</b>，峰值内存约为单张解码的 2 倍，不随文件数增长。
      </span>
    </div>

    <div class="it-tool-grid">
      <section class="it-card it-file-card" aria-label="待处理文件">
        <div class="it-fc-head">
          <div>
            <div class="it-fc-title">
              待处理文件<span class="it-count">{{ entries.length }}</span>
            </div>
            <div class="it-fc-sub">
              可处理<b>{{ processableEntries.length }}</b> 项 · 跳过<b>{{
                skippedEntries.length
              }}</b>
              项 · 合计<b>{{ formatSize(totalSize) }}</b>
            </div>
          </div>
          <button type="button" class="it-btn is-ghost is-sm" :disabled="busy" @click="addFiles">
            <ITIcon name="plus" :size="14" />添加
          </button>
        </div>

        <ul class="it-file-list">
          <li v-for="entry in entries" :key="entry.path" class="it-file-row">
            <span class="it-f-thumb" :class="entry.processable ? 'is-ok' : 'is-skip'">
              {{ extLabel(entry.ext) }}
            </span>
            <div class="it-f-main">
              <div class="it-f-name" :title="entry.path">{{ entry.name }}</div>
              <div class="it-f-meta">
                {{ entry.format || extLabel(entry.ext)
                }}<template v-if="entry.width && entry.height">
                  · {{ entry.width }}×{{ entry.height }}</template
                >
                · {{ formatSize(entry.size) }}
              </div>
            </div>
            <span class="it-tag" :class="entry.processable ? 'is-success' : 'is-warn'">
              <i></i>{{ entry.processable ? '可处理' : '跳过' }}
            </span>
            <button
              type="button"
              class="it-iconbtn it-f-remove"
              :aria-label="`移除 ${entry.name}`"
              :disabled="busy"
              @click="removeEntry(entry.path)"
            >
              <ITIcon name="close" :size="14" />
            </button>
            <div v-if="!entry.processable" class="it-f-reason">{{ entry.reason }}</div>
          </li>
        </ul>
        <div v-if="!entries.length" class="it-f-empty">
          <ITIcon name="image" :size="20" stroke="1.5" />
          <b>还没有待处理文件</b>
          <span>点「添加」从本机选择图片；首版不提供拖放</span>
          <span>暂无可处理项时「开始处理」保持禁用，并说明原因</span>
        </div>

        <div v-if="bigImageCount" class="it-warn it-fc-warn">
          <ITIcon name="warn" :size="13" />
          <div>
            <b>大图内存提醒</b>
            <span>
              本批 {{ bigImageCount }} 项超过 800 万像素，单张解码峰值约 35–49 MB；工具最多 2
              张并行，峰值内存约为单张的 2 倍。
            </span>
          </div>
        </div>

        <div class="it-fc-foot">
          <b>批处理仅覆盖静态 JPEG / PNG / WebP / BMP / TIFF</b>；动画 GIF / WebP
          跳过，<b>不压首帧</b>。<br />
          {{ UNSUPPORTED_NOTE }}
        </div>
      </section>

      <div class="it-right-col">
        <section class="it-card it-config-card">
          <div
            v-for="tab in OPERATION_TABS"
            :id="`it-panel-${tab.key}`"
            :key="tab.key"
            class="it-panel"
            :class="{ 'is-on': operation === tab.key }"
            role="tabpanel"
            :aria-labelledby="`it-tab-${tab.key}`"
            :hidden="operation !== tab.key"
          >
            <div class="it-panel-head">
              <b>{{ OPERATION_LABELS[tab.key] }}</b>
              <span class="it-sub">{{ panelSub }}</span>
            </div>

            <div v-if="tab.key === 'convert'" class="it-panel-body">
              <div class="it-field">
                <div class="it-field-label">
                  目标格式<span class="it-hint">TIFF / BMP 会先解码再写入所选格式</span>
                </div>
                <div class="it-opt-row">
                  <button
                    v-for="fmt in outputFormatList"
                    :key="fmt.key"
                    type="button"
                    class="it-opt"
                    :class="{ 'is-on': convert.format === fmt.key }"
                    :aria-pressed="convert.format === fmt.key ? 'true' : 'false'"
                    @click="convert.format = fmt.key"
                  >
                    <b>{{ fmt.label }}</b>
                    <span>{{ fmt.desc }}</span>
                  </button>
                </div>
              </div>
              <div class="it-field" :class="{ 'is-muted': convert.format === 'png' }">
                <div class="it-field-label">
                  质量<span class="it-hint">{{
                    convert.format === 'png'
                      ? 'PNG 为无损，质量不生效'
                      : '有损压缩，建议 85–90；数值越低体积越小'
                  }}</span>
                </div>
                <div class="it-quality-row">
                  <input
                    v-model.number="convert.quality"
                    class="it-range"
                    type="range"
                    min="40"
                    max="100"
                    step="1"
                    aria-label="JPEG / WebP 质量"
                    :disabled="convert.format === 'png'"
                  />
                  <span class="it-quality-val">{{ convert.quality }}</span>
                </div>
              </div>
              <div class="it-field">
                <div class="it-field-label">
                  透明背景<span class="it-hint">源图含透明通道时生效</span>
                </div>
                <div class="it-chip-row">
                  <button
                    v-for="item in TRANSPARENT_OPTIONS"
                    :key="item.key"
                    type="button"
                    class="it-chip"
                    :class="{ 'is-on': convert.transparent === item.key }"
                    :aria-pressed="convert.transparent === item.key ? 'true' : 'false'"
                    @click="convert.transparent = item.key"
                  >
                    {{ item.label }}
                  </button>
                </div>
              </div>
              <div class="it-config-foot">{{ formatConflictPolicy() }}</div>
            </div>

            <div v-else-if="tab.key === 'compress'" class="it-panel-body">
              <div class="it-field">
                <div class="it-field-label">
                  质量档位<span class="it-hint">档位越高越清晰，体积也越大</span>
                </div>
                <div class="it-tier-row">
                  <button
                    v-for="level in COMPRESS_LEVELS"
                    :key="level.key"
                    type="button"
                    class="it-opt"
                    :class="{ 'is-on': compress.level === level.key }"
                    :aria-pressed="compress.level === level.key ? 'true' : 'false'"
                    @click="compress.level = level.key"
                  >
                    <b
                      >{{ level.label }}<span v-if="level.recommended" class="it-rec">推荐</span></b
                    >
                    <span>质量 {{ level.quality }} · {{ level.desc }}</span>
                  </button>
                </div>
              </div>
              <div class="it-field">
                <div class="it-field-label">
                  预计结果<span class="it-hint">按档位估算，实际以执行结果为准</span>
                </div>
                <div class="it-pv">
                  <div class="it-pv-head">
                    <span>文件</span><span>原始</span><span>预计</span><span>压缩率</span>
                  </div>
                  <div v-for="row in estimateRows" :key="row.path" class="it-pv-row">
                    <span class="it-pv-name" :title="row.name">{{ row.name }}</span>
                    <span>{{ formatSize(row.size) }}</span>
                    <span>{{ formatSize(row.after) }}</span>
                    <span>{{ row.rate }}</span>
                  </div>
                  <div v-if="!estimateRows.length" class="it-pv-empty">
                    暂无可处理项，无法估算输出体积
                  </div>
                  <div v-else class="it-pv-total">
                    <span>{{ estimateRows.length }} 项合计</span>
                    <span>{{ formatSize(estimate.before) }}</span>
                    <span>→ {{ formatSize(estimate.after) }}</span>
                  </div>
                </div>
              </div>
              <p class="it-foot-note">
                JPEG / WebP 按档位有损压缩；<b>PNG 仅做无损优化</b>，档位不影响估算与像素；BMP /
                TIFF 输出 JPEG。
              </p>
              <p class="it-foot-note">
                压缩率取决于画面细节与源图本身的质量，同一档位无法保证每张都达到固定体积；不提供「压到指定
                MB」。
              </p>
            </div>

            <div v-else class="it-panel-body">
              <div class="it-field">
                <div class="it-field-label">调整方式</div>
                <div class="it-seg-row">
                  <div class="it-seg" role="group" aria-label="尺寸调整方式">
                    <button
                      v-for="item in RESIZE_MODES"
                      :key="item.key"
                      type="button"
                      class="it-segbtn"
                      :class="{ 'is-on': resize.mode === item.key }"
                      :aria-pressed="resize.mode === item.key ? 'true' : 'false'"
                      @click="resize.mode = item.key"
                    >
                      {{ item.label }}
                    </button>
                  </div>
                </div>
              </div>

              <div v-if="resize.mode === 'pixels'" class="it-r-form">
                <label class="it-inline">
                  宽<input
                    v-model.number="resize.width"
                    class="it-input is-num"
                    type="number"
                    min="1"
                  />px
                </label>
                <span class="it-times">×</span>
                <label class="it-inline">
                  高<input
                    v-model.number="resize.height"
                    class="it-input is-num"
                    type="number"
                    min="1"
                  />px
                </label>
              </div>
              <div v-else-if="resize.mode === 'percent'" class="it-r-form">
                <label class="it-inline">
                  <input
                    v-model.number="resize.percent"
                    class="it-input is-num"
                    type="number"
                    min="1"
                    max="400"
                  />%
                </label>
                <span class="it-hint">按原图百分比缩放，超过 100% 按不放大规则跳过</span>
              </div>
              <div v-else class="it-r-form">
                <label class="it-inline">
                  长边<input
                    v-model.number="resize.long_edge"
                    class="it-input is-num"
                    type="number"
                    min="1"
                  />px
                </label>
                <span class="it-hint">短边按原比例计算</span>
              </div>

              <label class="it-check">
                <input v-model="resize.keepRatio" type="checkbox" />
                保持比例
              </label>
              <p class="it-foot-note">
                不放大（超出原尺寸时跳过）；输出的 JPEG / PNG / WebP 保持原格式，BMP / TIFF 输出
                JPEG。
              </p>
              <div class="it-size-preview">{{ resizePreview }}</div>
            </div>
          </div>
        </section>

        <div class="it-bottom-row">
          <section class="it-card it-mini-card it-output-card" aria-label="输出位置">
            <div class="it-mc-head"><b>输出位置</b><span class="it-sub">不覆盖原图</span></div>
            <div class="it-mc-body">
              <label class="it-out-row" :class="{ 'is-on': outputMode === 'default' }">
                <input
                  v-model="outputMode"
                  type="radio"
                  value="default"
                  :disabled="busy || state !== 'idle'"
                />
                <div class="it-out-main">
                  <b>原目录下的新子目录（processed_日期）</b>
                  <span class="it-mono">{{ defaultDir || '选择图片后自动确定' }}</span>
                </div>
              </label>
              <label class="it-out-row" :class="{ 'is-on': outputMode === 'custom' }">
                <input
                  v-model="outputMode"
                  type="radio"
                  value="custom"
                  :disabled="busy || state !== 'idle'"
                />
                <div class="it-out-main">
                  <b>自定义目录</b>
                  <span class="it-mono">{{ customDir || '未选择 · 需具备写入权限' }}</span>
                </div>
                <button
                  type="button"
                  class="it-btn is-ghost is-sm"
                  :disabled="busy"
                  @click.prevent="chooseDir"
                >
                  选择
                </button>
              </label>
              <div class="it-safety">同名文件自动追加 (2)，不提供原地覆盖；原图始终保持不变。</div>
            </div>
          </section>

          <section
            v-if="state === 'idle'"
            class="it-card it-mini-card it-action-card"
            aria-label="本次任务"
          >
            <div class="it-mc-head">
              <b>本次任务</b><span class="it-sub">{{ operationLabel }}</span>
            </div>
            <div class="it-mc-body">
              <div class="it-action-stats">
                <b>{{ processableEntries.length }}</b><span>项可处理</span>
                <i class="it-sep"></i>
                <span>{{ formatSize(estimate.before) }} → {{ formatSize(estimate.after) }}</span>
              </div>
              <div class="it-action-summary">{{ taskSummary }}</div>
              <div class="it-action-note" :class="{ 'is-blocked': !canStart }">
                <template v-if="!processableEntries.length">暂无可处理项</template>
                <template v-else>{{ actionReason }}</template>
              </div>
              <button
                type="button"
                class="it-btn is-primary is-full"
                :disabled="!canStart"
                @click="start"
              >
                开始处理
              </button>
              <p class="it-action-foot">全部在本机完成，图片不会上传到服务器。</p>
            </div>
          </section>

          <section
            v-else-if="state === 'running'"
            class="it-card it-mini-card it-run-card"
            aria-label="执行中"
          >
            <div class="it-mc-head">
              <b>正在处理</b>
              <span class="it-tag is-info">执行中</span>
              <button type="button" class="it-btn is-ghost is-sm it-cancel" @click="askCancel">
                <ITIcon name="cancel" :size="14" />取消
              </button>
            </div>
            <div class="it-mc-body">
              <div class="it-run-top">
                <b>{{ progressPercentValue }}%</b>
                <span>已处理 {{ progress.current }} / {{ progress.total }} 项</span>
              </div>
              <div class="it-bar"><i :style="{ width: progressPercentValue + '%' }"></i></div>
              <div class="it-run-current">
                当前文件<b>{{ progress.label || runningName || '准备中' }}</b>
              </div>
              <div class="it-run-chips">
                <span
                  >成功<b>{{ liveCounts.success }}</b></span
                >
                <span
                  >失败<b>{{ liveCounts.failed }}</b></span
                >
                <span
                  >跳过<b>{{ liveCounts.skipped }}</b></span
                >
              </div>
              <div class="it-warn">
                <ITIcon name="info" :size="13" />
                <div>
                  <b>取消不会回滚已完成的项</b>
                  <span
                    >已写入的输出保留，尚未开始的项不会写入任何文件；原图与索引路径不受影响。</span
                  >
                </div>
              </div>
            </div>
          </section>

          <section
            v-else
            class="it-card it-mini-card it-result-card"
            :data-tone="outcome.state"
            :aria-label="outcome.title"
          >
            <div class="it-mc-head">
              <b>{{ outcome.title }}</b>
              <span class="it-tag" :class="outcomeTagClass(outcome.tone)">
                <i></i>{{ outcome.tag }}
              </span>
              <button type="button" class="it-btn is-ghost is-sm it-log-btn" @click="openLog">
                查看日志
              </button>
            </div>
            <div class="it-mc-body">
              <div class="it-result-stats">
                <div class="it-rs">
                  <b>{{ counts.success }}</b><span>成功</span>
                </div>
                <div class="it-rs">
                  <b>{{ counts.failed }}</b><span>失败</span>
                </div>
                <div class="it-rs">
                  <b>{{ counts.skipped }}</b><span>跳过</span>
                </div>
                <div class="it-result-out">
                  <span>输出目录</span>
                  <b :title="resultOutputDirsText">{{ resultOutputDirsText }}</b>
                </div>
              </div>
              <div v-if="failedRows.length" class="it-warn is-danger">
                <ITIcon name="error" :size="13" />
                <div>
                  <b>{{ failedRows[0].name }} 失败：{{ failedRows[0].reason }}</b>
                  <span v-if="failedRows.length > 1"
                    >另有 {{ failedRows.length - 1 }} 项失败，可仅重试失败项。原图未受影响。</span
                  >
                  <span v-else>原图未受影响；可改选输出目录后仅重试这 1 项。</span>
                </div>
              </div>
              <div v-else-if="outcome.state === 'cancelled'" class="it-warn">
                <ITIcon name="info" :size="13" />
                <div>
                  <b>任务已取消</b>
                  <span
                    >已完成的
                    {{ counts.success }}
                    项输出保留在输出目录；未开始的项没有写入任何文件，原图未受影响。</span
                  >
                </div>
              </div>
              <div v-else-if="outcome.state === 'error'" class="it-warn is-danger">
                <ITIcon name="error" :size="13" />
                <div>
                  <b>任务已中断</b>
                  <span
                    >核心进程退出或连接中断，已完成的
                    {{ counts.success }} 项输出保留；可在「待处理」中重新开始。</span
                  >
                </div>
              </div>
              <div v-else class="it-done-line">
                原图 {{ counts.total - counts.skipped }} 项均未被改动；跳过
                {{ counts.skipped }} 项未写入任何文件。
              </div>
              <div class="it-result-actions">
                <button
                  type="button"
                  class="it-btn is-ghost"
                  :disabled="!firstOutputDir"
                  @click="openOutputDir"
                >
                  打开输出目录
                </button>
                <button
                  v-if="failedRows.length"
                  type="button"
                  class="it-btn is-ghost"
                  @click="retryFailed"
                >
                  重试失败项
                </button>
                <button type="button" class="it-btn is-text" @click="backToPending">
                  返回待处理
                </button>
              </div>
            </div>
          </section>
        </div>
      </div>
    </div>

    <div v-if="cancelOpen" class="it-overlay" @click.self="cancelOpen = false">
      <div class="it-dialog" role="dialog" aria-modal="true" aria-labelledby="it-cancel-title">
        <div class="it-d-head"><b id="it-cancel-title">取消本次处理？</b></div>
        <div class="it-d-body">
          <p class="it-d-note">
            已完成的 {{ liveCounts.success }} 项输出会保留在输出目录；尚未开始的
            {{ Math.max(0, progress.total - progress.current) }}
            项不会写入任何文件。原图与机巡照片索引不受影响。
          </p>
        </div>
        <div class="it-d-foot">
          <button type="button" class="it-btn is-ghost" @click="cancelOpen = false">
            继续处理
          </button>
          <button type="button" class="it-btn is-primary" @click="confirmCancel">取消任务</button>
        </div>
      </div>
    </div>

    <div v-if="logOpen" class="it-overlay is-right" @click.self="logOpen = false">
      <aside class="it-drawer" role="dialog" aria-modal="true" aria-labelledby="it-log-title">
        <div class="it-d-head">
          <b id="it-log-title">运行日志</b>
          <button type="button" class="it-iconbtn" aria-label="关闭日志" @click="logOpen = false">
            <ITIcon name="close" :size="16" />
          </button>
        </div>
        <div class="it-log-meta">
          <span>任务 {{ jobId || '—' }}</span>
          <span>{{ logRows.length }} 条</span>
        </div>
        <div class="it-log-filter">
          <div class="it-seg" role="group" aria-label="日志筛选">
            <button
              v-for="item in LOG_FILTERS"
              :key="item.key"
              type="button"
              class="it-segbtn"
              :class="{ 'is-on': logFilter === item.key }"
              @click="logFilter = item.key"
            >
              {{ item.label }}
            </button>
          </div>
        </div>
        <ul class="it-log-list">
          <li
            v-for="(line, i) in filteredLogs"
            :key="i"
            class="it-log-line"
            :class="'is-' + line.level"
          >
            <span class="it-lv" :class="'is-' + line.level">{{ line.badge }}</span>
            <span class="it-log-text">{{ line.text }}</span>
          </li>
          <li v-if="!filteredLogs.length" class="it-log-empty">这个筛选下没有记录</li>
        </ul>
        <div class="it-d-foot">
          <button type="button" class="it-btn is-ghost is-full" @click="logOpen = false">
            关闭
          </button>
        </div>
      </aside>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import ITIcon from './ITIcon.vue';
import * as api from '../lib/api.mjs';
import { formatSize } from '../lib/format.mjs';
import {
  COMPRESS_LEVELS,
  OPERATION_TABS,
  OUTPUT_FORMATS,
  RESIZE_MODES,
  TRANSPARENT_OPTIONS,
  UNSUPPORTED_NOTE,
  classifyFile,
  classifyRows,
  defaultOutputDir,
  describeAction,
  describeResize,
  describeTask,
  estimateOutput,
  formatConflictPolicy,
  outcomeTagClass,
  progressPercent,
  resolveOutcome,
  resolvePickedFiles,
} from '../lib/process.mjs';

const props = defineProps({
  busy: { type: Boolean, default: false },
});
const emit = defineEmits(['notify', 'running']);

const OPERATION_LABELS = { convert: '格式转换', compress: '压缩体积', resize: '尺寸修改' };
const outputFormatList = Object.values(OUTPUT_FORMATS);
const LOG_FILTERS = [
  { key: 'all', label: '全部' },
  { key: 'success', label: '成功' },
  { key: 'failed', label: '失败' },
  { key: 'skipped', label: '跳过' },
];

const operation = ref('convert');
const entries = ref([]);
const state = ref('idle');
const convert = reactive({ format: 'jpeg', quality: 88, transparent: 'keep' });
const compress = reactive({ level: 'balanced' });
const resize = reactive({
  mode: 'pixels',
  width: 1920,
  height: 1080,
  percent: 50,
  long_edge: 1920,
  keepRatio: true,
});
const outputMode = ref('default');
const customDir = ref('');
const jobId = ref('');
const progress = reactive({ current: 0, total: 0, label: '' });
const runningName = ref('');
const rows = ref([]);
const logLines = ref([]);
const logOpen = ref(false);
const logFilter = ref('all');
const cancelOpen = ref(false);
const liveCounts = reactive({ success: 0, failed: 0, skipped: 0 });
const jobStatus = ref('success'); // 结束态口径：success / failed / cancelled / error

// 运行中必须真的锁住列表与输出目录：既看父组件传入的 busy，也看本组件运行态
const busy = computed(() => props.busy || state.value === 'running');

let unsubscribe = null;
// jobId 返回前到达的事件：先按到达顺序缓冲，任务号确定后回放，首批日志不丢
let earlyEvents = [];

/** 终态、核心退出与卸载共用：退订一次并清空缓冲，重复调用不会重复退订。 */
function stopJobEvents() {
  if (unsubscribe) unsubscribe();
  unsubscribe = null;
  earlyEvents = [];
}

/** 每次开始处理都重新订阅；先退掉上一轮，避免同一事件被处理两次或句柄泄漏。 */
function subscribeJobEvents() {
  stopJobEvents();
  unsubscribe = api.onEvent(onJobEvent);
}

/** 真实并发上限来自核心 image_tools.workers（1..2），日志不能写死 2。 */
function clampWorkers(value) {
  const count = Math.round(Number(value));
  if (!Number.isFinite(count)) return 2;
  return Math.min(2, Math.max(1, count));
}

/** 取当前配置的并发数；读不到配置时按核心默认 2 展示，不影响任务本身。 */
async function configuredWorkers() {
  try {
    const cfg = await api.getConfig();
    return clampWorkers(cfg && cfg.workers);
  } catch (e) {
    return clampWorkers(undefined);
  }
}

const operationLabel = computed(() => OPERATION_LABELS[operation.value] || '');
const panelSub = computed(() => {
  if (operation.value === 'convert') return '输出另存，不覆盖原图';
  if (operation.value === 'compress') return '按质量档位压缩，不承诺固定目标大小';
  return '等比缩放，不放大原图';
});

const processableEntries = computed(() => entries.value.filter((item) => item.processable));
const skippedEntries = computed(() => entries.value.filter((item) => !item.processable));
const totalSize = computed(() =>
  entries.value.reduce((sum, item) => sum + (Number(item.size) || 0), 0)
);
const bigImageCount = computed(
  () =>
    processableEntries.value.filter((item) => (item.width || 0) * (item.height || 0) > 8000000)
      .length
);
const defaultDir = computed(() => {
  const first = processableEntries.value[0] || entries.value[0];
  if (!first) return '';
  const at = Math.max(first.path.lastIndexOf('\\'), first.path.lastIndexOf('/'));
  if (at <= 0) return '';
  return defaultOutputDir(first.path.slice(0, at));
});

const taskSummary = computed(() =>
  describeTask({
    operation: operation.value,
    options: currentOptions(),
    processable: processableEntries.value.length,
    skipped: skippedEntries.value.length,
  })
);
const action = computed(() =>
  describeAction({
    processable: processableEntries.value.length,
    skipped: skippedEntries.value.length,
  })
);
const canStart = computed(() => action.value.canStart && !props.busy && state.value === 'idle');
const actionReason = computed(() => action.value.reason);

const estimate = computed(() =>
  estimateOutput(processableEntries.value, operation.value, currentOptions())
);
const estimateRows = computed(() => {
  const options = currentOptions();
  const list = processableEntries.value.slice(0, 6);
  return list.map((item) => {
    const single = estimateOutput([item], operation.value, options);
    const after = single.after || item.size;
    const rate = item.size ? Math.round(((after - item.size) / item.size) * 100) : 0;
    return {
      path: item.path,
      name: item.name,
      size: item.size,
      after,
      rate: `${rate > 0 ? '+' : ''}${rate}%`,
    };
  });
});

const resizePreview = computed(() =>
  describeResize(processableEntries.value[0], { ...currentOptions(), keepRatio: resize.keepRatio })
);

const progressPercentValue = computed(() => progressPercent(progress));
const counts = computed(() => classifyRows(rows.value));
const failedRows = computed(() => rows.value.filter((row) => row.status === 'failed'));
// 结束态名称一律由 resolveOutcome 决定：全失败、取消与中断各有自己的名目
const outcome = computed(() => resolveOutcome({ status: jobStatus.value, rows: rows.value }));
const resultOutputDirsText = computed(() => {
  const dirs = [];
  for (const row of rows.value) {
    if (!row || !row.output) continue;
    const at = Math.max(row.output.lastIndexOf('\\'), row.output.lastIndexOf('/'));
    if (at > 0) {
      const dir = row.output.slice(0, at);
      if (!dirs.includes(dir)) dirs.push(dir);
    }
  }
  return dirs.length ? dirs.join('；') : customDir.value || defaultDir.value || '—';
});
const firstOutputDir = computed(() => {
  const text = resultOutputDirsText.value;
  return text && text !== '—' ? text.split('；')[0] : '';
});

const logRows = computed(() => {
  const list = [];
  for (const line of logLines.value) list.push(line);
  for (const row of rows.value) {
    const level =
      row.status === 'success' ? 'success' : row.status === 'skipped' ? 'skipped' : 'failed';
    const badge = level === 'success' ? '成功' : level === 'skipped' ? '跳过' : '失败';
    let text = row.name || '';
    if (level === 'success') {
      const before = formatSize(row.size_before);
      const after = formatSize(row.size_after);
      text = `${text}  ${before} → ${after}`;
    } else if (row.reason) {
      text = `${text}  ${row.reason}`;
    }
    list.push({ level, badge, text });
  }
  return list;
});
const filteredLogs = computed(() =>
  logFilter.value === 'all'
    ? logRows.value
    : logRows.value.filter((line) => line.level === logFilter.value)
);

function extLabel(ext) {
  return (
    String(ext || '')
      .replace(/^\./, '')
      .toUpperCase() || 'IMG'
  );
}

function currentOptions() {
  if (operation.value === 'convert') {
    return {
      format: convert.format,
      quality: Number(convert.quality) || 88,
      transparent: convert.transparent,
    };
  }
  if (operation.value === 'compress') return { level: compress.level };
  const options = { mode: resize.mode };
  if (resize.mode === 'pixels') {
    options.keep_ratio = !!resize.keepRatio;
    if (resize.width) options.width = Number(resize.width);
    if (resize.height) options.height = Number(resize.height);
  } else if (resize.mode === 'percent') {
    options.percent = Number(resize.percent) || 50;
  } else {
    options.long_edge = Number(resize.long_edge) || 1920;
  }
  return options;
}

function switchTab(key) {
  if (busy.value) return;
  if (state.value !== 'idle' && state.value !== 'done' && state.value !== 'partial') return;
  operation.value = key;
}

function onTabKeydown(event) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
  const index = OPERATION_TABS.findIndex((item) => item.key === operation.value);
  const nextIndex =
    (index + (event.key === 'ArrowRight' ? 1 : -1) + OPERATION_TABS.length) % OPERATION_TABS.length;
  const next = OPERATION_TABS[nextIndex];
  event.preventDefault();
  switchTab(next.key);
  const button = document.getElementById(`it-tab-${next.key}`);
  if (button) button.focus();
}

function dirOf(path) {
  const text = String(path || '');
  const at = Math.max(text.lastIndexOf('\\'), text.lastIndexOf('/'));
  return at > 0 ? text.slice(0, at) : '';
}

async function addFiles() {
  if (busy.value) return;
  try {
    const picked = await api.pickFiles();
    if (!picked || !picked.length) return;
    await addPaths(picked);
  } catch (e) {
    emit('notify', api.cleanError(e) || '选择文件失败');
  }
}

/**
 * 把一批**已授权**路径加入待处理列表：对话框选中后与自动化冒烟共用同一条路径。
 * 授权仍由主进程在 runProcess 时逐项复核，这里不放开任何新目录。
 */
async function addPaths(paths) {
  const picked = (Array.isArray(paths) ? paths : []).filter(
    (item) => typeof item === 'string' && item
  );
  if (!picked.length) return;
  const directions = [];
  for (const path of picked) {
    const dir = dirOf(path);
    if (dir && !directions.includes(dir)) directions.push(dir);
  }
  const known = new Map();
  const listed = new Set();
  // 原始目录 → 核心返回的规范目录（inspect 因解码失败报错时，用它做精确存在性兜底）
  const knownDirs = new Map();
  for (const dir of directions) {
    try {
      const listing = await api.list(dir);
      for (const file of (listing && listing.files) || []) known.set(file.path, file);
      listed.add(dir);
      const canonicalDir = listing && listing.directory;
      if (canonicalDir) knownDirs.set(dir, canonicalDir);
    } catch (e) {
      // 单个目录列举失败不影响其余选择：缺元数据的文件按路径兜底展示
    }
  }
  // 身份统一：传入路径可能是 Windows 8.3 短名，核心列举给的是长名。
  // 用核心的规范路径（api.inspect，主进程仍校验目录授权）做键比对，
  // 否则短名会被误判成「已不在原位置」而整批跳过（v2.4.3 CI：列表 0 行）。
  const resolved = await resolvePickedFiles({
    picked,
    known,
    listedDirs: listed,
    knownDirs,
    dirOf,
    inspect: (p) => api.inspect(p).then((info) => (info && info.path) || ''),
    existing: new Set(entries.value.map((item) => item.path)),
  });
  for (const { path, info, input } of resolved.entries) {
    const name = info.name || input.split(/[\\/]/).pop() || path;
    const ext = String(info.ext || name.slice(name.lastIndexOf('.')) || '').toLowerCase();
    const verdict = classifyFile({ ...info, ext, supported: info.supported !== false });
    entries.value.push({
      path,
      name,
      ext,
      format: info.format || '',
      size: Number(info.size) || 0,
      width: Number(info.width) || 0,
      height: Number(info.height) || 0,
      animated: !!info.animated,
      processable: verdict.processable,
      reason: verdict.reason,
    });
  }
  if (state.value !== 'idle') state.value = 'idle';
  summarizeLive();
  if (resolved.missing.length) {
    emit('notify', `${resolved.missing.length} 个文件已不在原位置，已跳过`);
  }
}

function removeEntry(path) {
  if (busy.value) return;
  entries.value = entries.value.filter((item) => item.path !== path);
}

async function chooseDir() {
  if (busy.value) return;
  try {
    const picked = await api.pickDirectory(customDir.value || '');
    if (!picked || !picked.directory) return;
    customDir.value = picked.directory;
    outputMode.value = 'custom';
  } catch (e) {
    emit('notify', api.cleanError(e) || '选择输出目录失败');
  }
}

function summarizeLive() {
  const done = counts.value;
  if (state.value === 'running') return;
  liveCounts.success = done.success;
  liveCounts.failed = done.failed;
  liveCounts.skipped = done.skipped;
}

function logLine(text) {
  logLines.value.push({ level: 'info', badge: '信息', text: String(text || '') });
}

function onJobEvent(payload) {
  if (!payload) return;
  // 任务号还没回来（runProcess 尚未返回）：先按到达顺序缓冲，确定后原序回放
  if (!jobId.value) {
    if (String(payload.event || '').startsWith('job.')) earlyEvents.push(payload);
    return;
  }
  handleJobEvent(payload);
}

function handleJobEvent(payload) {
  if (!payload || payload.job_id !== jobId.value) return;
  if (payload.event === 'job.progress') {
    progress.current = Number(payload.current) || 0;
    progress.total = Number(payload.total) || progress.total;
    progress.label = String(payload.label || '');
    runningName.value = progress.label;
    return;
  }
  if (payload.event === 'job.log') {
    logLine(payload.line);
    const text = String(payload.line || '');
    if (text.includes('成功 ')) liveCounts.success += 1;
    else if (text.includes('失败 ')) liveCounts.failed += 1;
    else if (text.includes('跳过 ')) liveCounts.skipped += 1;
    return;
  }
  if (payload.event === 'job.done') {
    finishJob(payload.job);
  }
}

function finishJob(job) {
  const data = job || {};
  // 迟到的终态（上一轮任务）不得覆盖当前任务的结果与口径
  if (data.id && jobId.value && String(data.id) !== jobId.value) return;
  const result = data.result || {};
  rows.value = Array.isArray(result.rows) ? result.rows : [];
  jobStatus.value = String(data.status || 'success');
  if (jobStatus.value === 'cancelled') {
    logLine('任务已取消：已完成的输出保留，未开始的项未写入任何文件');
  } else if (data.error) {
    logLine(`任务失败：${data.error}`);
  }
  // 终态一律由 resolveOutcome 定名：全失败、取消与中断都不能落到成功名目
  state.value = outcome.value.state;
  progress.current = progress.total = rows.value.length || progress.total;
  summarizeLive();
  stopJobEvents();
  pollStop();
}

/** 事件偶发丢失时的兜底：结束态靠 getJob 收口。 */
let pollTimer = 0;
function pollStop() {
  window.clearTimeout(pollTimer);
  pollTimer = 0;
}
function startPoll() {
  pollStop();
  const tick = async () => {
    if (!jobId.value || state.value !== 'running') return;
    try {
      const job = await api.getJob(jobId.value);
      if (job && ['success', 'failed', 'cancelled'].includes(job.status)) {
        finishJob(job);
        return;
      }
      if (job && job.progress) {
        progress.current = Number(job.progress.current) || progress.current;
        progress.total = Number(job.progress.total) || progress.total;
        progress.label = String(job.progress.label || progress.label);
      }
    } catch (e) {
      // 核心退出/任务已被清理：交给「core.exit」提示收口
    }
    pollTimer = window.setTimeout(tick, 700);
  };
  pollTimer = window.setTimeout(tick, 700);
}

async function start(files) {
  const list = Array.isArray(files) ? files : processableEntries.value.map((item) => item.path);
  if (!list.length) return;
  const options = currentOptions();
  if (
    operation.value === 'resize' &&
    options.mode === 'pixels' &&
    !options.width &&
    !options.height
  ) {
    emit('notify', '请输入目标宽或高');
    return;
  }
  rows.value = [];
  logLines.value = [];
  progress.current = 0;
  progress.total = list.length;
  progress.label = '';
  runningName.value = '';
  liveCounts.success = 0;
  liveCounts.failed = 0;
  liveCounts.skipped = 0;
  jobStatus.value = 'success';
  jobId.value = '';
  state.value = 'running';
  emit('running', true);
  // 先订阅、后启动：runProcess 返回 jobId 之前的首批日志先入缓冲，不会丢
  try {
    subscribeJobEvents();
    const workers = await configuredWorkers();
    const payload = { operation: operation.value, files: list, options };
    const outDir = outputMode.value === 'custom' ? customDir.value : '';
    if (outDir) payload.output_dir = outDir;
    const res = await api.runProcess(payload);
    jobId.value = String((res && res.job_id) || '');
    if (!jobId.value) throw new Error('核心未返回任务号');
    logLine(`${operationLabel.value} 开始 · ${list.length} 项 · 并发 ${workers}`);
    // 任务号确定后按到达顺序回放首批事件；核心已退出的这一轮不再回放
    const pending = earlyEvents;
    earlyEvents = [];
    if (state.value === 'running') {
      for (const event of pending) handleJobEvent(event);
      if (state.value === 'running') startPoll();
    }
  } catch (e) {
    // 启动就被拒（未授权路径 / 核心未启动等）：明确报失败，不留在待处理里假装没事
    stopJobEvents();
    rows.value = [];
    jobStatus.value = 'failed';
    state.value = 'failed';
    logLine(`任务启动失败：${api.cleanError(e)}`);
    emit('running', false);
    emit('notify', api.cleanError(e) || '任务启动失败');
  }
}

function retryFailed() {
  if (busy.value) return;
  const list = failedRows.value.map((row) => row.path).filter(Boolean);
  if (!list.length) return;
  start(list);
}

function backToPending() {
  state.value = 'idle';
  jobStatus.value = 'success';
  rows.value = [];
  logLines.value = [];
  jobId.value = '';
  progress.current = 0;
  progress.total = 0;
  progress.label = '';
  summarizeLive();
}

function askCancel() {
  cancelOpen.value = true;
}

async function confirmCancel() {
  cancelOpen.value = false;
  if (!jobId.value) return;
  try {
    await api.cancelJob(jobId.value);
    logLine('已请求取消任务');
  } catch (e) {
    emit('notify', api.cleanError(e) || '取消失败');
  }
}

function openLog() {
  logFilter.value = 'all';
  logOpen.value = true;
}

function openOutputDir() {
  if (!firstOutputDir.value) return;
  api
    .showItem(firstOutputDir.value)
    .catch((e) => emit('notify', api.cleanError(e) || '无法打开输出目录'));
}

function onCoreExit() {
  if (state.value !== 'running') return;
  jobStatus.value = 'error';
  state.value = 'error';
  progress.current = Math.min(progress.current, progress.total || progress.current);
  logLine('核心进程已退出，任务已中断');
  pollStop();
  stopJobEvents();
  emit('notify', '核心进程已退出，本次任务已中断');
  emit('running', false);
}

/** 取消弹层 / 日志抽屉的 Esc 关闭：就地拦下，避免穿透到 document 级快捷键。 */
function onOverlayKeydown(event) {
  if (event.key !== 'Escape') return;
  if (!cancelOpen.value && !logOpen.value) return;
  event.preventDefault();
  event.stopPropagation();
  if (cancelOpen.value) cancelOpen.value = false;
  else logOpen.value = false;
  // 焦点归还触发按钮，避免焦点留在已消失的弹层里
  nextTick(() => {
    const trigger = document.querySelector(
      cancelOpen.value ? '.it-run-card .it-cancel' : '.it-result-card .it-log-btn'
    );
    if (trigger && typeof trigger.focus === 'function') trigger.focus();
  });
}

defineExpose({
  addFiles,
  addPaths,
  onCoreExit,
  state,
  jobStatus,
  busy,
  // 真实任务号：冒烟用它去核心查 getJob，核对结果 rows 与磁盘成品一致
  currentJobId: () => jobId.value,
  // 仅供真实 GUI 冒烟：喂一份任务终态，走与 job.done 完全相同的渲染路径
  simulateJobDone: (job) => {
    jobId.value = String((job && job.id) || jobId.value || '');
    finishJob(job);
  },
  // 仅供真实 GUI 冒烟：确定性进入运行态并触发核心退出（与 core.exit 事件同一处理函数）
  simulateInterrupted: () => {
    jobId.value = 'smoke-interrupted';
    jobStatus.value = 'success';
    state.value = 'running';
    onCoreExit();
  },
});

onMounted(() => {
  document.addEventListener('keydown', onOverlayKeydown);
});

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onOverlayKeydown);
  pollStop();
  stopJobEvents();
});

watch(state, (value) => {
  if (value !== 'running') emit('running', false);
});
</script>
