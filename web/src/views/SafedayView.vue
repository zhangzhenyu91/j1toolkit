<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">安全日活动记录</h1>
      <span class="page-sub">学习文件上传 · 智能生成记录 · 预览与存网盘</span>
    </div>

    <div v-if="demoMode" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >记录列表为静态演示数据；生成/删除/存网盘不执行、不伪造成功。切换 live 适配层后对接 Go
        API。</span
      >
    </div>

    <!-- 超管班组切换器（端点缺失时静默隐藏，见 features/safeday/api.js 注释） -->
    <div v-if="teams.length" class="sd-teambar">
      <span class="field-label">班组视图</span>
      <t-select v-model="teamId" class="sd-team-sel" @change="switchTeam">
        <t-option v-if="allowAll" value="all" label="全部班组" />
        <t-option v-for="t in teams" :key="t.id" :value="String(t.id)" :label="t.name" />
      </t-select>
    </div>

    <!-- ===== 上传与生成 ===== -->
    <div class="card">
      <h2 class="card-title">上传学习文件</h2>
      <div
        class="sd-drop"
        :class="{ over: dragOver }"
        tabindex="0"
        role="button"
        @click="fileInput && fileInput.click()"
        @keydown.enter.prevent="fileInput && fileInput.click()"
        @dragenter.prevent="dragOver = true"
        @dragover.prevent
        @dragleave.prevent="dragOver = false"
        @drop.prevent="onDrop"
      >
        <div class="sd-drop-ic">＋</div>
        <div class="sub-text">
          点击或拖拽文件到此处（PDF / Word / PPT / Excel，单文件 ≤50MB，最多 10 个）
        </div>
      </div>
      <input
        ref="fileInput"
        type="file"
        multiple
        hidden
        accept=".pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx"
        @change="onFilesPicked"
      />

      <div v-if="files.length" class="sd-ftags">
        <div v-for="(f, i) in files" :key="f.name" class="sd-ftag">
          <span class="sd-ftag-nm" :title="f.name">{{ f.name }}</span>
          <span class="muted num">{{ fmtSize(f.size) }}</span>
          <button
            type="button"
            class="sd-ftag-x"
            :aria-label="'移除 ' + f.name"
            @click="files.splice(i, 1)"
          >
            ×
          </button>
        </div>
      </div>

      <div class="sd-genbar">
        <label class="field-label">活动日期</label>
        <input v-model="dateISO" type="date" class="sd-date num" />
        <div class="spacer"></div>
        <t-button theme="primary" :disabled="!files.length" @click="openGenMask"
          >生成活动记录</t-button
        >
      </div>
    </div>

    <!-- ===== 生成进度卡 ===== -->
    <div class="card sd-prog" :class="{ idle: !track }">
      <template v-if="!track">
        <span class="muted">提交生成后，这里将实时展示生成进度</span>
      </template>
      <template v-else>
        <div class="sd-prog-head">
          <div>
            <div class="row-title">{{ track.name }}</div>
            <div class="row-sub num">
              {{ track.sourceCount || 1 }} 个源文件 · 提交于 {{ fmtDate(track.createdAt) || '—' }}
            </div>
          </div>
        </div>
        <div class="sd-steps">
          <div v-for="(s, i) in stepRows" :key="s.title" class="sd-step" :class="s.state">
            <span class="sd-step-ic">{{
              s.state === 'done' ? '✓' : s.state === 'fail' ? '✕' : s.state === 'doing' ? '…' : ''
            }}</span>
            <div>
              <div class="sd-step-t">{{ s.title }}</div>
              <div class="sd-step-s">{{ s.sub }}</div>
            </div>
          </div>
        </div>
        <div class="sd-pbar" :class="{ fail: track.status === 'failed' }">
          <i
            :style="{
              width:
                track.status === 'processing' ? '50%' : track.status === 'done' ? '100%' : '0%',
            }"
          ></i>
        </div>
        <div class="sd-pnum num" :class="{ fail: track.status === 'failed' }">
          <span v-if="track.status === 'failed'">生成失败</span>
          <span v-else-if="track.status === 'done'">生成完成</span>
          <span v-else>云端生成中 · 已等待 {{ waitedText }}</span>
          <b v-if="track.status === 'processing'">状态每 {{ POLL_INTERVAL / 1000 }}s 自动刷新</b>
          <b v-else-if="track.status === 'done'">100%</b>
        </div>
      </template>
    </div>

    <!-- ===== 记录列表 ===== -->
    <div class="card">
      <div class="sd-tbl-head">
        <h2 class="card-title" style="margin: 0">活动记录</h2>
        <span class="muted"
          >共 {{ lastRecords.length }} 条{{
            processingCount ? ` · ${processingCount} 条生成中` : ''
          }}</span
        >
        <div class="spacer"></div>
        <t-button size="small" variant="outline" @click="refreshRecords(false)">刷新</t-button>
      </div>
      <t-loading :loading="recordsLoading" size="small">
        <table class="sd-table">
          <thead>
            <tr>
              <th>记录文件</th>
              <th class="sd-col-date num">活动日期</th>
              <th v-if="teamId === 'all'" class="sd-col-team">班组</th>
              <th class="sd-col-time num">生成时间</th>
              <th class="sd-col-state">状态</th>
              <th class="sd-col-op">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="rec in lastRecords"
              :key="rec.id"
              :class="{ prev: rec.status === 'done' }"
              :title="rec.status === 'done' ? '点击预览' : ''"
              @click="rec.status === 'done' && previewRecordAction(rec)"
            >
              <td>
                <div class="row-title">{{ rec.fileName || '—' }}</div>
                <div class="row-sub">{{ recSub(rec) }}</div>
              </td>
              <td class="sd-col-date num muted">{{ rec.date || '—' }}</td>
              <td v-if="teamId === 'all'" class="sd-col-team">
                <span v-if="rec.team" class="dot-tag blue"
                  ><span class="dot"></span>{{ rec.team }}</span
                >
                <span v-else class="muted">—</span>
              </td>
              <td class="sd-col-time num muted">{{ fmtDate(rec.createdAt) || '—' }}</td>
              <td class="sd-col-state">
                <span class="dot-tag" :class="statusColor(rec.status)">
                  <span class="dot"></span>{{ statusText(rec) }}
                </span>
              </td>
              <td class="sd-col-op" @click.stop>
                <t-button
                  size="small"
                  variant="text"
                  :disabled="rec.status !== 'done'"
                  @click="previewRecordAction(rec)"
                >
                  预览
                </t-button>
                <t-button
                  size="small"
                  variant="text"
                  :disabled="rec.status !== 'done'"
                  @click="downloadRecordAction(rec)"
                >
                  下载
                </t-button>
                <t-button
                  v-if="rec.status === 'done' && netdiskOk"
                  size="small"
                  variant="text"
                  @click="openSaveNetdisk(rec)"
                >
                  存网盘
                </t-button>
                <t-button size="small" variant="text" theme="danger" @click="openDelMask(rec)"
                  >删除</t-button
                >
              </td>
            </tr>
            <tr v-if="!recordsLoading && !lastRecords.length">
              <td :colspan="teamId === 'all' ? 6 : 5" class="sd-empty muted">暂无记录</td>
            </tr>
          </tbody>
        </table>
      </t-loading>
    </div>

    <!-- ===== 弹层：生成表单 ===== -->
    <div v-if="genMask" class="sd-mask">
      <div class="sd-dialog" role="dialog" aria-modal="true">
        <h3>生成活动记录</h3>

        <div class="sd-frow">
          <label class="field-label">学习内容名称</label>
          <t-input v-model="form.name" placeholder="如：《安规学习》、《事故通报》" />
        </div>

        <div class="sd-frow">
          <label class="field-label">参会人员</label>
          <div class="sd-mode">
            <button
              type="button"
              class="subnav-item"
              :class="{ active: attendMode === 'member' }"
              @click="setAttendMode('member')"
            >
              名单点亮
            </button>
            <button
              type="button"
              class="subnav-item"
              :class="{ active: attendMode === 'custom' }"
              @click="setAttendMode('custom')"
            >
              自定义输入
            </button>
          </div>
        </div>

        <template v-if="attendMode === 'member'">
          <div class="sd-frow">
            <label class="field-label">主持人</label>
            <t-select v-model="form.host">
              <t-option v-for="m in members" :key="m" :value="m" :label="m" />
            </t-select>
          </div>
          <div class="sd-frow">
            <label class="field-label">参加人员（点击切换参加/缺席）</label>
            <div class="sd-chips">
              <span
                v-for="m in members"
                :key="m"
                class="sd-chip"
                :class="{ on: attendSet.has(m) }"
                @click="toggleAttend(m)"
              >
                {{ m }}
              </span>
            </div>
            <div class="muted">缺席：{{ absentText || '无' }}</div>
          </div>
        </template>

        <template v-else>
          <div class="sd-frow">
            <label class="field-label">主持人</label>
            <t-input v-model="form.hostCustom" placeholder="主持人姓名" />
          </div>
          <div class="sd-frow">
            <label class="field-label">本班组参加人员（空格/顿号分隔）</label>
            <t-input v-model="form.attendeesCustom" placeholder="参加人员" />
          </div>
          <div class="sd-frow">
            <label class="field-label">缺席人员（空格/顿号分隔）</label>
            <t-input v-model="form.absenteesCustom" placeholder="缺席人员" />
          </div>
        </template>

        <div class="sd-frow">
          <label class="field-label">记录人</label>
          <t-select v-model="form.recorder">
            <t-option v-for="m in members" :key="m" :value="m" :label="m" />
          </t-select>
        </div>
        <div class="sd-frow">
          <label class="field-label">上级参加人员</label>
          <t-input v-model="form.superior" placeholder="无则留空" />
        </div>
        <div class="sd-frow">
          <label class="field-label">缺席原因</label>
          <t-input v-model="form.absentReason" placeholder="无则留空" />
        </div>

        <div class="sd-dfoot">
          <t-button size="small" variant="outline" :disabled="submitting" @click="closeGenMask"
            >取消</t-button
          >
          <t-button size="small" theme="primary" :loading="submitting" @click="submitGenerate">
            {{ submitting ? '正在生成…' : '确定，开始生成' }}
          </t-button>
        </div>
      </div>
    </div>

    <!-- ===== 弹层：存网盘（简化目录输入，旧页 ShadeNdPick 浏览式选目录未复刻） ===== -->
    <div v-if="ndMask" class="sd-mask">
      <div class="sd-dialog" role="dialog" aria-modal="true">
        <h3>保存到网盘</h3>
        <div class="sd-frow">
          <label class="field-label">目标空间</label>
          <div class="sd-mode">
            <button
              type="button"
              class="subnav-item"
              :class="{ active: ndForm.space === 'my' }"
              @click="ndForm.space = 'my'"
            >
              我的空间
            </button>
            <button
              type="button"
              class="subnav-item"
              :class="{ active: ndForm.space === 'public' }"
              @click="ndForm.space = 'public'"
            >
              公共区
            </button>
          </div>
        </div>
        <div class="sd-frow">
          <label class="field-label">目标目录（相对空间根）</label>
          <t-input v-model="ndForm.dir" placeholder="安全日记录" />
        </div>
        <div class="sd-dfoot">
          <t-button size="small" variant="outline" :disabled="ndBusy" @click="ndMask = false"
            >取消</t-button
          >
          <t-button size="small" theme="primary" :loading="ndBusy" @click="confirmSaveNetdisk"
            >保存</t-button
          >
        </div>
      </div>
    </div>

    <!-- ===== 弹层：删除确认 ===== -->
    <div v-if="delMask" class="sd-mask">
      <div class="sd-dialog" role="dialog" aria-modal="true">
        <h3>删除这条记录？</h3>
        <p class="sub-text">
          记录「{{
            delTarget ? delTarget.name : ''
          }}」及已生成的记录文件将一并删除，该操作不可恢复。
        </p>
        <div class="sd-dfoot">
          <t-button size="small" variant="outline" :disabled="deleting" @click="delMask = false"
            >取消</t-button
          >
          <t-button size="small" theme="danger" :loading="deleting" @click="confirmDelete"
            >确认删除</t-button
          >
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 安全日活动记录（迁移 server/public/safeday.html）：多文件上传 → multipart 生成 →
// 记录列表（下载/预览/存网盘/删除）。生成进度卡状态一律来自 /records 真实轮询（5s），
// 不以定时器伪造云端步骤。
// 未复刻（见交付说明缺口）：ShadeNdPick 浏览式目录选择（存网盘为路径输入简化版）；
// 超管班组切换依赖 GET /api/v1/admin/teams，Go 重写端暂无该端点（拉取失败静默隐藏）。
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import {
  isDemo,
  formMeta as fetchFormMeta,
  generate,
  records as fetchRecords,
  downloadRecord,
  previewRecord,
  saveRecordToNetdisk,
  deleteRecord,
  probeNetdisk,
  listTeams,
  saveBlob,
  previewRoute,
} from '../features/safeday/api.js';

const router = useRouter();
const demoMode = isDemo();

const POLL_INTERVAL = 5000;
const MAX_FILES = 10;
const MAX_SIZE = 50 * 1024 * 1024;
const ALLOWED = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];

// ---- 工具 ----
function extOf(name) {
  return name.split('.').pop().toLowerCase();
}
function baseOf(name) {
  return name.replace(/\.[^.]+$/, '');
}
function toDots(iso) {
  return String(iso || '').replace(/-/g, '.');
}
function todayISO() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
function fmtSize(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return Math.round(n / 1024) + ' KB';
  return `${n || 0} B`;
}
function fmtDate(d) {
  if (!d) return '';
  const dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  if (Number.isNaN(dt.getTime())) return String(d);
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}
// 自定义人员输入规范化：空格/顿号/逗号/分号分隔统一为空格分隔（服务端模板按空格分隔渲染）
function normNames(str) {
  return String(str || '')
    .split(/[\s、，,；;]+/)
    .filter(Boolean)
    .join(' ');
}

// ---- 上传区 ----
const fileInput = ref(null);
const files = ref([]);
const dragOver = ref(false);
const dateISO = ref(todayISO());

function addFiles(fileList) {
  let badType = 0;
  let badSize = 0;
  const incoming = [];
  Array.from(fileList || []).forEach((f) => {
    if (!ALLOWED.includes(extOf(f.name))) {
      badType++;
      return;
    }
    if (f.size > MAX_SIZE) {
      badSize++;
      return;
    }
    if (files.value.some((x) => x.name === f.name)) return;
    if (incoming.some((x) => x.name === f.name)) return;
    incoming.push(f);
  });
  const merged = files.value.concat(incoming);
  if (merged.length > MAX_FILES) {
    MessagePlugin.error(`最多上传 ${MAX_FILES} 个文件`);
    return;
  }
  files.value = merged;
  if (badType > 0) MessagePlugin.info(`已忽略 ${badType} 个不支持的文件`);
  if (badSize > 0) MessagePlugin.info(`已忽略 ${badSize} 个超过 50MB 的文件`);
}

function onFilesPicked(e) {
  addFiles(e.target.files);
  e.target.value = '';
}
function onDrop(e) {
  dragOver.value = false;
  if (e.dataTransfer && e.dataTransfer.files) addFiles(e.dataTransfer.files);
}

// ---- 生成表单 ----
const genMask = ref(false);
const submitting = ref(false);
const attendMode = ref('member');
const members = ref([]);
const attendSet = ref(new Set());
const formMetaCache = ref(null);
const formMetaTeam = ref(null);

const form = reactive({
  name: '',
  host: '',
  hostCustom: '',
  recorder: '',
  superior: '',
  attendeesCustom: '',
  absenteesCustom: '',
  absentReason: '',
});

const absentText = computed(() => members.value.filter((m) => !attendSet.value.has(m)).join(' '));

function toggleAttend(m) {
  const s = new Set(attendSet.value);
  if (s.has(m)) s.delete(m);
  else s.add(m);
  attendSet.value = s;
}

function setAttendMode(mode) {
  if (attendMode.value === mode) return;
  if (mode === 'custom') {
    // 切到自定义时按当前名单选择预填主持人/参加/缺席
    form.hostCustom = form.host || '';
    form.attendeesCustom = members.value.filter((m) => attendSet.value.has(m)).join(' ');
    form.absenteesCustom = members.value.filter((m) => !attendSet.value.has(m)).join(' ');
  }
  attendMode.value = mode;
}

async function openGenMask() {
  if (teamId.value === 'all') {
    MessagePlugin.info('请先切换到具体班组再生成');
    return;
  }
  // 学习内容默认值：每个源文件各自带书名号（《文件1》、《文件2》）
  form.name = files.value.map((f) => `《${baseOf(f.name)}》`).join('、');
  attendMode.value = 'member';
  genMask.value = true;
  try {
    await loadFormMeta();
    const ms = members.value;
    form.host = ms[0] || '';
    form.recorder = formMetaCache.value.defaults.recorder || ms[0] || '';
    form.superior = formMetaCache.value.defaults.superior || '';
    form.absentReason = '';
    attendSet.value = new Set(ms); // 默认全员参加
  } catch (e) {
    MessagePlugin.error(e.message || '表单数据加载失败');
  }
}

function closeGenMask() {
  if (submitting.value) return;
  genMask.value = false;
}

async function loadFormMeta() {
  const key = teamId.value;
  if (formMetaCache.value && formMetaTeam.value === key) return formMetaCache.value;
  const d = await fetchFormMeta(key || undefined);
  formMetaCache.value = { members: d.members || [], defaults: d.defaults || {} };
  formMetaTeam.value = key;
  members.value = formMetaCache.value.members;
  return formMetaCache.value;
}

async function submitGenerate() {
  if (submitting.value) return;
  const name = form.name.trim();
  if (!name) {
    MessagePlugin.error('请输入记录名称');
    return;
  }
  if (!files.value.length) {
    MessagePlugin.error('请先选择文件');
    return;
  }
  const custom = attendMode.value === 'custom';
  const hostVal = custom ? form.hostCustom.trim() : form.host || '';
  const attendeesVal = custom
    ? normNames(form.attendeesCustom)
    : members.value.filter((m) => attendSet.value.has(m)).join(' ');
  const absenteesVal = custom
    ? normNames(form.absenteesCustom)
    : members.value.filter((m) => !attendSet.value.has(m)).join(' ');
  if (custom && !attendeesVal) {
    MessagePlugin.error('请填写本班组参加人员');
    return;
  }

  const fd = new FormData();
  fd.append('name', name);
  fd.append('date', toDots(dateISO.value || todayISO()));
  if (teamId.value && teamId.value !== 'all') fd.append('team_id', teamId.value);
  fd.append('host', hostVal);
  fd.append('recorder', form.recorder || '');
  fd.append('superior', form.superior.trim());
  fd.append('attendees', attendeesVal);
  fd.append('absentees', absenteesVal);
  fd.append('absentReason', form.absentReason.trim());
  files.value.forEach((f) => fd.append('files', f, f.name));

  submitting.value = true;
  try {
    const data = await generate(fd);
    // 本地缓存同步新默认值，下次打开免重拉
    if (formMetaCache.value) {
      if (form.superior.trim()) formMetaCache.value.defaults.superior = form.superior.trim();
      if (form.recorder) formMetaCache.value.defaults.recorder = form.recorder;
    }
    genMask.value = false;
    files.value = [];
    MessagePlugin.success('已开始生成，请稍候…');
    if (data.record) startTrack(data.record);
    refreshRecords(false);
  } catch (e) {
    MessagePlugin.error(e.message || '生成请求失败');
  } finally {
    submitting.value = false;
  }
}

// ---- 记录列表与轮询 ----
const lastRecords = ref([]);
const recordsLoading = ref(false);
const knownIds = ref(new Set());
const netdiskOk = ref(false);
const processingCount = computed(
  () => lastRecords.value.filter((r) => r.status === 'processing').length
);

let pollTimer = null;

function stopPolling() {
  if (pollTimer) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}
function schedulePoll() {
  stopPolling();
  pollTimer = setTimeout(() => refreshRecords(false), POLL_INTERVAL);
}

function statusText(rec) {
  if (rec.status === 'done') return '已生成';
  if (rec.status === 'failed') return '生成失败';
  return '生成中';
}
function statusColor(st) {
  return st === 'done' ? 'orange' : st === 'failed' ? 'red' : 'blue';
}
function recSub(rec) {
  return rec.sources && rec.sources.length
    ? `《${rec.sources.join('、')}》`
    : `${rec.sourceCount || 1} 个源文件`;
}

async function refreshRecords(isInitial) {
  if (isInitial) recordsLoading.value = true;
  try {
    const data = await fetchRecords(teamId.value || undefined);
    const records = data.records || [];
    lastRecords.value = records;
    // 新记录入场动画位：knownIds 之外的即新（此处仅状态跟踪用）
    knownIds.value = new Set(records.map((r) => String(r.id)));
    syncTrack(records);
    if (records.some((r) => r.status === 'processing')) schedulePoll();
    else stopPolling();
  } catch (e) {
    if (isInitial) MessagePlugin.error(e.message || '记录列表加载失败');
    else MessagePlugin.error(e.message || '记录刷新失败');
    // 轮询中出错：仍按已知 processing 状态继续轮询，避免状态卡死
    if (!isInitial && lastRecords.value.some((r) => r.status === 'processing')) schedulePoll();
  } finally {
    recordsLoading.value = false;
  }
}

// ---- 生成进度卡（状态一律来自 /records 真实轮询，不以定时器伪造云端步骤） ----
const STEP_TEXTS = [
  ['提交生成', '学习文件已上传，生成请求已受理'],
  ['云端生成', 'Dify 工作流撰写活动记录，完成后回调入库'],
  ['记录入库', '完成后自动加入记录列表，可下载/预览'],
];
const track = ref(null); // {id,name,sourceCount,createdAt,status,error}；status 与记录 status 同步
const nowTick = ref(Date.now());
let nowTimer = null;

function stepStates(status) {
  if (status === 'done') return ['done', 'done', 'done'];
  if (status === 'failed') return ['done', 'fail', 'todo'];
  return ['done', 'doing', 'todo']; // processing（含未知状态）
}

const stepRows = computed(() => {
  const t = track.value;
  if (!t) return [];
  const states = stepStates(t.status);
  return STEP_TEXTS.map(([title, sub], i) => ({
    title,
    sub: states[i] === 'fail' ? t.error || '生成失败' : sub,
    state: states[i],
  }));
});

// waitedText 生成中已等待时长（按真实时间，随 nowTick 每秒刷新）。
const waitedText = computed(() => {
  const t = track.value;
  if (!t || !t.createdAt) return '—';
  const start = new Date(String(t.createdAt).replace(' ', 'T').replace(/\//g, '-')).getTime();
  if (Number.isNaN(start)) return '—';
  const sec = Math.max(0, Math.floor((nowTick.value - start) / 1000));
  if (sec < 60) return `${sec} 秒`;
  return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`;
});

function startNowTicker() {
  stopNowTicker();
  nowTimer = setInterval(() => {
    nowTick.value = Date.now();
  }, 1000);
}
function stopNowTicker() {
  if (nowTimer) {
    clearInterval(nowTimer);
    nowTimer = null;
  }
}

function startTrack(rec) {
  track.value = {
    id: String(rec.id),
    name: rec.name,
    sourceCount: rec.sourceCount,
    createdAt: rec.createdAt,
    status: rec.status || 'processing',
    error: '',
    notified: false,
  };
  nowTick.value = Date.now();
  startNowTicker();
}

function syncTrack(records) {
  const t = track.value;
  if (t) {
    const rec = records.find((r) => String(r.id) === t.id);
    if (!rec) {
      track.value = null;
      stopNowTicker();
      return;
    } // 记录已被删除
    t.status = rec.status || 'processing';
    if (rec.status === 'done') {
      if (!t.notified) {
        t.notified = true;
        stopNowTicker();
        MessagePlugin.success('活动记录已生成，可在列表中下载');
      }
    } else if (rec.status === 'failed') {
      if (!t.notified) {
        t.notified = true;
        t.error = rec.error || '生成失败';
        stopNowTicker();
        MessagePlugin.error(t.error);
      }
    } else {
      t.error = '';
      startNowTicker();
    }
    return;
  }
  // 页面刷新时若有生成中记录则接管跟踪（状态仍以轮询为准）
  const proc = records.find((r) => r.status === 'processing');
  if (proc) {
    startTrack(proc);
  }
}

// ---- 记录操作 ----
async function downloadRecordAction(rec) {
  try {
    const res = await downloadRecord(rec.id);
    await saveBlob(res, rec.fileName || '安全日活动记录.docx');
  } catch (e) {
    MessagePlugin.error(e.message || '下载失败');
  }
}

async function previewRecordAction(rec) {
  try {
    const data = await previewRecord(rec.id);
    if (!data.url) throw new Error('预览地址为空');
    const r = previewRoute(data.url);
    if (r && r.external) {
      window.open(r.external, '_blank');
    } else if (r) {
      router.push({ path: r.path, query: r.query });
    }
  } catch (e) {
    MessagePlugin.info((e.message || '预览失败') + '，可改用下载按钮');
  }
}

// 存网盘（简化目录输入）
const ndMask = ref(false);
const ndBusy = ref(false);
const ndTarget = ref(null);
const ndForm = reactive({ space: 'my', dir: '安全日记录' });

function openSaveNetdisk(rec) {
  ndTarget.value = rec;
  ndForm.space = 'my';
  ndForm.dir = '安全日记录';
  ndMask.value = true;
}

async function confirmSaveNetdisk() {
  if (!ndTarget.value) return;
  ndBusy.value = true;
  try {
    const data = await saveRecordToNetdisk(ndTarget.value.id, {
      dir: ndForm.dir.trim() || '安全日记录',
      space: ndForm.space,
    });
    MessagePlugin.success(`已保存到网盘：${data.path || ''}`);
    ndMask.value = false;
  } catch (e) {
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    ndBusy.value = false;
  }
}

// 删除
const delMask = ref(false);
const deleting = ref(false);
const delTarget = ref(null);

function openDelMask(rec) {
  delTarget.value = rec;
  delMask.value = true;
}

async function confirmDelete() {
  if (!delTarget.value || deleting.value) return;
  deleting.value = true;
  try {
    const data = await deleteRecord(delTarget.value.id);
    delMask.value = false;
    // 服务端可能返回 warning（记录已删但文件删除失败）
    if (data.warning) MessagePlugin.info(data.warning);
    else MessagePlugin.success('记录已删除');
    refreshRecords(false);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  } finally {
    deleting.value = false;
  }
}

// ---- 超管班组切换（端点缺失时静默隐藏） ----
const teams = ref([]);
const teamId = ref('');
const allowAll = ref(true);

async function switchTeam() {
  formMetaCache.value = null;
  stopPolling();
  stopNowTicker();
  track.value = null;
  lastRecords.value = [];
  try {
    localStorage.setItem('safeday_team_id', teamId.value);
  } catch (e) {
    /* 忽略 */
  }
  refreshRecords(true);
}

// ---- 初始化 ----
onMounted(async () => {
  const me = currentUser() || {};
  if (me.role === 'admin') {
    try {
      const list = await listTeams();
      teams.value = (list || []).filter((t) => t.status === 1 || t.status === undefined);
      if (teams.value.length) {
        let saved = 'all';
        try {
          saved = localStorage.getItem('safeday_team_id') || 'all';
        } catch (e) {
          /* 忽略 */
        }
        teamId.value = saved;
      }
    } catch (e) {
      // 端点缺失/失败：不显示切换器，后端按默认班组出数
      teams.value = [];
    }
  }
  probeNetdisk()
    .then((ok) => {
      netdiskOk.value = !!ok;
    })
    .catch(() => {
      netdiskOk.value = false;
    });
  refreshRecords(true);
});

onBeforeUnmount(() => {
  stopPolling();
  stopNowTicker();
});
</script>

<style scoped>
/* 安全日页局部样式：沿用全局 token 与轻量白卡 */
.sd-teambar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.sd-team-sel {
  width: 180px;
}
.sd-drop {
  border: 1px dashed var(--tk-dash);
  border-radius: 8px;
  padding: 28px 16px;
  text-align: center;
  cursor: pointer;
  transition: all 0.16s var(--tk-ease);
}
.sd-drop:hover,
.sd-drop.over {
  border-color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.sd-drop-ic {
  width: 40px;
  height: 40px;
  margin: 0 auto 8px;
  border-radius: 50%;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 22px;
  line-height: 40px;
}
.sd-ftags {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
.sd-ftag {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  border-radius: 4px;
  background: var(--tk-fill);
  font-size: 12px;
}
.sd-ftag-nm {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sd-ftag-x {
  border: none;
  background: none;
  color: var(--tk-ph);
  font-size: 15px;
  cursor: pointer;
  padding: 0 2px;
}
.sd-ftag-x:hover {
  color: var(--tk-red);
}
.sd-genbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 14px;
}
.sd-genbar .spacer {
  flex: 1;
}
.sd-date {
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  font-size: 13px;
  color: var(--tk-ink);
  background: #fff;
}
.sd-prog.idle {
  color: var(--tk-ph);
  text-align: center;
}
.sd-prog-head {
  margin-bottom: 12px;
}
.sd-steps {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
}
.sd-step {
  display: flex;
  gap: 8px;
  opacity: 0.45;
}
.sd-step.doing,
.sd-step.done {
  opacity: 1;
}
.sd-step.fail {
  opacity: 1;
}
.sd-step-ic {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--tk-fill);
  color: var(--tk-brand);
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.sd-step.done .sd-step-ic {
  background: var(--tk-green-soft);
  color: #0b8a37;
}
.sd-step.doing .sd-step-ic {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.sd-step.fail .sd-step-ic {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.sd-step-t {
  font-size: 13px;
  font-weight: 600;
}
.sd-step-s {
  font-size: 12px;
  color: var(--tk-ph);
}
.sd-pbar {
  height: 4px;
  margin: 14px 0 8px;
  border-radius: 2px;
  background: var(--tk-fill);
  overflow: hidden;
}
.sd-pbar i {
  display: block;
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.4s var(--tk-ease);
}
.sd-pbar.fail i {
  background: var(--tk-red);
}
.sd-pnum {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: var(--tk-sub);
}
.sd-pnum.fail {
  color: var(--tk-red);
}
.sd-tbl-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.sd-tbl-head .spacer {
  flex: 1;
}
.sd-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.sd-table th {
  text-align: left;
  font-weight: 600;
  color: var(--tk-sub);
  padding: 8px 10px;
  border-bottom: 1px solid var(--tk-line);
}
.sd-table td {
  padding: 10px;
  border-bottom: 1px solid var(--tk-line);
}
.sd-table tr.prev {
  cursor: pointer;
}
.sd-table tr.prev:hover td {
  background: var(--tk-fill);
}
.sd-col-date {
  width: 100px;
}
.sd-col-team {
  width: 110px;
}
.sd-col-time {
  width: 130px;
}
.sd-col-state {
  width: 90px;
}
.sd-col-op {
  width: 180px;
}
.sd-empty {
  text-align: center;
  padding: 32px 0;
}
/* 弹层 */
.sd-mask {
  position: fixed;
  inset: 0;
  z-index: 30;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
}
.sd-dialog {
  width: 440px;
  max-width: 92vw;
  max-height: 84vh;
  overflow: auto;
  background: #fff;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-hover);
  padding: 20px;
}
.sd-dialog h3 {
  margin: 0 0 14px;
  font-size: 16px;
}
.sd-frow {
  margin-bottom: 12px;
}
.sd-frow .field-label {
  display: block;
  margin-bottom: 6px;
}
.sd-mode {
  display: flex;
  gap: 8px;
}
.sd-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.sd-chip {
  padding: 4px 12px;
  border-radius: 4px;
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
  user-select: none;
}
.sd-chip.on {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.sd-dfoot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 16px;
}
</style>
