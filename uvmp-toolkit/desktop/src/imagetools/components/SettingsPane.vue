<template>
  <div class="it-settings">
    <div class="it-settings-inner">
      <div class="it-settings-head">
        <div>
          <h2>设置</h2>
          <p>图片工具独立窗口设置，不影响主客户端业务插件与登录态。</p>
        </div>
        <button type="button" class="it-btn is-ghost is-sm" @click="$emit('back')">
          <ITIcon name="prev" :size="14" />返回图片查看
        </button>
      </div>

      <div class="it-set-tabs" role="tablist" aria-label="设置分类" @keydown="onTabKeydown">
        <button
          v-for="tab in SET_TABS"
          :id="`it-set-tab-${tab.key}`"
          :key="tab.key"
          type="button"
          role="tab"
          class="it-set-tab"
          :class="{ 'is-on': tabKey === tab.key }"
          :aria-selected="tabKey === tab.key ? 'true' : 'false'"
          :tabindex="tabKey === tab.key ? 0 : -1"
          :aria-controls="`it-set-${tab.key}`"
          @click="selectTab(tab.key)"
        >
          {{ tab.label }}
        </button>
      </div>

      <div
        v-if="tabKey === 'quick'"
        id="it-set-quick"
        class="it-set-panel"
        role="tabpanel"
        aria-labelledby="it-set-tab-quick"
      >
        <div class="it-card">
          <div class="it-card-head">
            <div>
              <h3>快捷输入短语</h3>
              <p>重命名时点击短语即可插入，可增删改并调整顺序；保存后本机生效。</p>
            </div>
            <span class="it-count">{{ draft.length }} 条</span>
          </div>

          <div class="it-rule-strip">
            <ITIcon name="info" :size="15" class="it-rule-ic" />
            <div class="it-rule-copy">
              <b>插入，不是替换</b>
              <p>
                在文件名输入框的光标位置插入短语，插入后光标落在短语末尾；连续点击按点击顺序拼接，不会清空已输入内容，也不会自动改扩展名。
              </p>
            </div>
            <div class="it-insert-demo" aria-hidden="true">
              <span>现场</span><span class="it-caret"></span><span class="it-arrow">+</span>
              <span class="it-mini-chip">_已核验</span><span class="it-arrow">→</span>
              <span>现场_已核验</span><span class="it-caret"></span>
            </div>
          </div>

          <ul class="it-phrase-list">
            <li v-for="(phrase, index) in draft" :key="index" class="it-phrase-row">
              <span class="it-p-index">{{ index + 1 }}</span>
              <input
                v-model="draft[index]"
                class="it-input"
                type="text"
                :maxlength="MAX_PHRASE_LEN"
                :aria-label="`第 ${index + 1} 条快捷短语`"
                @input="dirty = true"
              />
              <button
                type="button"
                class="it-iconbtn"
                title="上移"
                aria-label="上移"
                :disabled="index === 0"
                @click="move(index, -1)"
              >
                <ITIcon name="up" :size="15" />
              </button>
              <button
                type="button"
                class="it-iconbtn"
                title="下移"
                aria-label="下移"
                :disabled="index === draft.length - 1"
                @click="move(index, 1)"
              >
                <ITIcon name="down" :size="15" />
              </button>
              <button
                type="button"
                class="it-iconbtn is-danger"
                title="删除"
                aria-label="删除"
                @click="remove(index)"
              >
                <ITIcon name="trash" :size="15" />
              </button>
            </li>
            <li v-if="!draft.length" class="it-phrase-empty">
              还没有短语：保存 0 条即为有效设置；需要时在下方输入后点「添加」。
            </li>
          </ul>

          <div class="it-phrase-add">
            <input
              v-model="newPhrase"
              class="it-input"
              type="text"
              :maxlength="MAX_PHRASE_LEN"
              placeholder="输入新短语，如 _已核验"
              aria-label="新短语"
              @keydown.enter.prevent="add"
            />
            <button
              type="button"
              class="it-btn is-ghost is-sm"
              :disabled="draft.length >= MAX_PHRASES"
              @click="add"
            >
              <ITIcon name="plus" :size="14" />添加
            </button>
          </div>
          <p v-if="error" class="it-inline-error" role="alert">{{ error }}</p>

          <div class="it-card-foot">
            <button type="button" class="it-btn is-ghost is-sm" @click="restoreDefaults">
              恢复默认
            </button>
            <span class="it-save-hint">{{ saveHint }}</span>
            <button
              type="button"
              class="it-btn is-primary is-sm"
              :disabled="saving || !dirty"
              @click="save"
            >
              {{ saving ? '保存中…' : '保存设置' }}
            </button>
          </div>

          <p class="it-note-line">
            短语顺序即重命名面板中的按钮顺序；删除短语不删除历史文件名。最多
            {{ MAX_PHRASES }} 条、单条 {{ MAX_PHRASE_LEN }} 字，允许中文、空格与下划线，不允许
            <code>\ / : * ? " &lt; &gt; |</code>
            等文件名非法字符。
          </p>
        </div>
      </div>

      <div
        v-else-if="tabKey === 'assoc'"
        id="it-set-assoc"
        class="it-set-panel"
        role="tabpanel"
        aria-labelledby="it-set-tab-assoc"
      >
        <div class="it-assoc-head">
          <div>
            <h3>文件关联与默认应用</h3>
            <p>
              让双击图片直接在此窗口打开。系统默认应用由操作系统掌控，工具只做声明与引导，不静默改写。
            </p>
          </div>
          <div class="it-seg">
            <button
              type="button"
              class="it-segbtn"
              :class="{ 'is-on': assocPlatform === 'win32' }"
              :aria-pressed="assocPlatform === 'win32' ? 'true' : 'false'"
              @click="assocPlatform = 'win32'"
            >
              Windows 10
            </button>
            <button
              type="button"
              class="it-segbtn"
              :class="{ 'is-on': assocPlatform === 'linux' }"
              :aria-pressed="assocPlatform === 'linux' ? 'true' : 'false'"
              @click="assocPlatform = 'linux'"
            >
              麒麟 / UOS
            </button>
          </div>
        </div>

        <div v-if="assocPlatform === 'win32'" class="it-card it-assoc-card">
          <div class="it-assoc-status">
            <div>
              <span class="it-tag" :class="windowsTagClass"> <i></i>{{ windowsTagText }} </span>
              <h4>{{ windowsHeadline }}</h4>
              <p>
                安装版会写入 10
                个扩展名的文件关联与「打开方式」条目，右键菜单可直接看到本工具；是否设为默认由你在系统设置中确认一次。
              </p>
              <ul v-if="windowsRows.length" class="it-assoc-list">
                <li v-for="row in windowsRows" :key="row.ext">
                  <b>{{ row.ext }}</b>
                  <span :class="'is-' + row.status">{{ row.label }}</span>
                </li>
              </ul>
            </div>
            <div class="it-assoc-actions">
              <button type="button" class="it-btn is-primary" @click="openDefaults">
                打开系统默认应用设置
              </button>
              <button type="button" class="it-btn is-ghost is-sm" @click="refreshDefaultStatus">
                <ITIcon name="refresh" :size="14" />重新检查
              </button>
              <p class="it-assoc-check">{{ windowsCheckText }}</p>
            </div>
          </div>

          <div class="it-steps">
            <div class="it-step">
              <span class="it-sn">1</span>
              <p>点击上方按钮，Windows 会打开<b>设置 → 应用 → 默认应用</b>。</p>
            </div>
            <div class="it-step">
              <span class="it-sn">2</span>
              <p>
                在「按文件类型指定默认应用」里，先为常用的<b>.jpg</b>、<b>.png</b>、<b>.webp</b>选择<b>图片工具</b>；其余
                .jpeg / .gif / .bmp / .ico / .avif / .tif / .tiff 按需设置。
              </p>
            </div>
            <div class="it-step">
              <span class="it-sn">3</span>
              <p>回到本页点上方「重新检查」，三项都指向本工具后状态变为「已设为默认」。</p>
            </div>
          </div>

          <div class="it-warn is-blue">
            <ITIcon name="info" :size="13" />
            <div>
              <b>Windows 10 限制</b>
              <span>
                UserChoice
                受系统保护，本工具不能静默抢占默认打开方式；安装与升级都不会反复弹窗或强制改写，最多在设置页提示一次。
              </span>
            </div>
          </div>

          <dl class="it-assoc-facts">
            <div>
              <dt>已登记扩展名</dt>
              <dd>10 个：.jpg .jpeg .png .gif .webp .bmp .ico .avif .tif .tiff（8 种格式）</dd>
            </div>
            <div>
              <dt>默认建议</dt>
              <dd>仅建议 .jpg / .png / .webp 设为默认；其余格式按需设置，不影响登记与右键打开</dd>
            </div>
            <div>
              <dt>接收方式</dt>
              <dd>安装版：双击文件 + 冷启动参数 + 第二实例转交；便携版：仅应用内「打开图片」</dd>
            </div>
            <div>
              <dt>卸载行为</dt>
              <dd>
                安装版卸载时移除本工具登记的关联，已设为默认的项由系统提示选择新应用；不改动用户照片
              </dd>
            </div>
          </dl>
        </div>

        <div v-else class="it-card it-assoc-card">
          <div class="it-assoc-status">
            <div>
              <span class="it-tag" :class="linuxIsDefault ? 'is-success' : 'is-info'">
                <i></i>{{ linuxIsDefault ? '已设为默认' : '可由用户主动设为默认' }}
              </span>
              <h4>已注册 desktop 条目「图片工具」，可写入 xdg-mime 默认项</h4>
              <p>
                麒麟 UKUI 与统信 DDE 都遵循 XDG MIME
                规范，但文件管理器缓存与「默认程序」面板可能各存一份，需要一次主动确认。
              </p>
            </div>
            <button
              type="button"
              class="it-btn is-ghost"
              :disabled="settingLinux"
              @click="setLinuxDefault"
            >
              {{ settingLinux ? '正在写入…' : '设为默认图片查看器' }}
            </button>
          </div>

          <div class="it-steps">
            <div class="it-step">
              <span class="it-sn">1</span>
              <p>
                点击上方按钮后，本工具才调用<b>xdg-mime default</b
                >写入<b>image/jpeg</b>、<b>image/png</b>、<b>image/webp</b>的默认项；不做开机自启、不后台轮询。
              </p>
            </div>
            <div class="it-step">
              <span class="it-sn">2</span>
              <p>
                若文件管理器仍用旧程序打开，注销重登或在其「默认程序」面板中再确认一次；两种桌面的入口名称不同。
              </p>
            </div>
            <div class="it-step">
              <span class="it-sn">3</span>
              <p>设为默认后，随时可在系统设置改回原查看器。</p>
            </div>
          </div>

          <p v-if="linuxResultText" class="it-assoc-check" role="status">{{ linuxResultText }}</p>

          <div class="it-warn is-blue">
            <ITIcon name="info" :size="13" />
            <div>
              <b>可控副作用</b>
              <span
                >仅影响当前登录用户的 MIME
                默认项，需要用户目录可写；写入失败会保留原值并提示原因，不重复尝试。</span
              >
            </div>
          </div>

          <dl class="it-assoc-facts">
            <div>
              <dt>已登记扩展名</dt>
              <dd>
                10 个：.jpg .jpeg .png .gif .webp .bmp .ico .avif .tif .tiff（MIME 随 desktop
                条目声明）
              </dd>
            </div>
            <div>
              <dt>默认建议</dt>
              <dd>仅建议 image/jpeg、image/png、image/webp 设为默认；其余格式按需设置</dd>
            </div>
            <div>
              <dt>接收方式</dt>
              <dd>
                安装版（deb）：文件管理器双击 +
                第二实例转交；便携版（tar.gz）：仅应用内打开，不注册关联
              </dd>
            </div>
            <div>
              <dt>桌面差异</dt>
              <dd>UKUI 与 DDE 的默认程序面板路径、缓存刷新时机不同，需在目标镜像实机复核</dd>
            </div>
          </dl>
        </div>

        <div class="it-card it-compare-card">
          <h3>安装版与便携版能力差异</h3>
          <table>
            <thead>
              <tr>
                <th>能力</th>
                <th>安装版（exe / deb）</th>
                <th>便携版（zip / tar.gz）</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>注册文件关联</td>
                <td class="is-yes">是，安装时写入</td>
                <td class="is-no">否</td>
              </tr>
              <tr>
                <td>双击图片直接打开</td>
                <td class="is-yes">是</td>
                <td class="is-no">否，需先启动工具</td>
              </tr>
              <tr>
                <td>设为系统默认应用</td>
                <td class="is-yes">可引导 / 用户主动设置</td>
                <td class="is-no">不提供入口</td>
              </tr>
              <tr>
                <td>卸载清理关联</td>
                <td class="is-yes">自动移除</td>
                <td class="is-no">删除目录即结束</td>
              </tr>
              <tr>
                <td>查看、重命名、图片处理</td>
                <td class="is-yes">全部可用</td>
                <td class="is-yes">全部可用</td>
              </tr>
            </tbody>
          </table>
          <p class="it-note-line">
            便携版适合无管理员权限的临时机器；文件关联能力只在安装版承诺，界面中也如实标注，不给便携版用户制造「设置失败」的困惑。
          </p>
        </div>
      </div>

      <div
        v-else
        id="it-set-formats"
        class="it-set-panel"
        role="tabpanel"
        aria-labelledby="it-set-tab-formats"
      >
        <div class="it-card it-formats-card">
          <h3>可查看格式</h3>
          <p>
            完整方案开放 8 种格式、10
            个扩展名，与文件关联登记一一对应；只有静态图参与批处理，动画图在批处理中明确跳过。
          </p>
          <div class="it-format-chips">
            <span v-for="item in formatChips" :key="item">{{ item }}</span>
          </div>
          <p class="it-subnote">
            不纳入：HEIC / HEIF、RAW、PSD、EPS、PDF、SVG
            栅格化，以上格式暂不承诺预览与批处理；本页不展示 EXIF
            摄影参数，只显示格式与尺寸等本机元数据。
          </p>
        </div>

        <div class="it-card it-formats-card">
          <h3>查看器边界</h3>
          <div class="it-insert-steps">
            <div>
              <i>1</i>只遍历当前文件所在目录、不递归子目录；按自然文件名排序，方向键到首尾后不循环。
            </div>
            <div>
              <i>2</i>缩放范围
              25%–400%，滚轮以鼠标位置为中心；放大后可拖动，双击画布在实际尺寸与适应窗口之间切换。
            </div>
            <div>
              <i>3</i>首版不做旋转、裁剪、标注、胶片栏与幻灯片播放；动画 GIF / WebP
              按默认循环播放，不提供暂停或逐帧控制。
            </div>
          </div>
          <p class="it-subnote">{{ FORMAT_BOUNDARY_NOTE }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue';
import ITIcon from './ITIcon.vue';
import * as api from '../lib/api.mjs';
import { DEFAULT_PHRASES, MAX_PHRASES, MAX_PHRASE_LEN, validatePhrases } from '../lib/rename.mjs';
import { FORMAT_BOUNDARY_NOTE } from '../lib/process.mjs';

const SET_TABS = [
  { key: 'quick', label: '快捷输入' },
  { key: 'assoc', label: '文件关联' },
  { key: 'formats', label: '查看能力' },
];

const props = defineProps({
  phrases: { type: Array, default: () => [] },
  defaultStatus: { type: Object, default: null },
  // 受控页签：外部（如信息栏「前往文件关联」）可直接指定进入哪一页
  tab: { type: String, default: 'quick' },
});
const emit = defineEmits(['back', 'notify', 'saved', 'tab-change', 'toast']);

const tabKey = ref(SET_TABS.some((item) => item.key === props.tab) ? props.tab : 'quick');

/** 切页签：同时广播给父组件，保证再次进入设置时停在同一页。 */
function selectTab(key) {
  if (!SET_TABS.some((item) => item.key === key)) return;
  tabKey.value = key;
  emit('tab-change', key);
}
const draft = ref([]);
const newPhrase = ref('');
const error = ref('');
const dirty = ref(false);
const saving = ref(false);
const saveHint = ref('已保存');
const assocPlatform = ref('win32');
const status = ref(null);
const settingLinux = ref(false);
const linuxResult = ref(null);

// 8 种格式 / 10 个扩展名，与文件关联登记一一对应
const FORMAT_CHIPS = [
  ['JPEG', '.jpg .jpeg'],
  ['PNG', '.png'],
  ['GIF', '.gif（含动画）'],
  ['WebP', '.webp（含动画）'],
  ['BMP', '.bmp'],
  ['ICO', '.ico'],
  ['AVIF', '.avif'],
  ['TIFF', '.tif .tiff（完整方案）'],
];
const formatChips = FORMAT_CHIPS.map(([name, exts]) => `${name} ${exts}`);

// 默认应用状态一律来自主进程真实查询（Windows 读 UserChoice、Linux 读 xdg-mime）。
// 查不到时只说「无法确定」，不把未知说成未设或已设——否则设置页会承诺做不到的事。
const STATUS_LABEL = {
  default: '本工具',
  other: '其它应用',
  unknown: '无法确定',
};
const windowsStatus = computed(() => {
  const value = status.value;
  if (!value) return 'unknown';
  if (value.supported === false) return 'unsupported';
  return String(value.status || 'unknown');
});
const windowsTagClass = computed(() => {
  const state = windowsStatus.value;
  if (state === 'default') return 'is-success';
  if (state === 'unsupported') return 'is-info';
  return 'is-warn';
});
const windowsTagText = computed(() => {
  switch (windowsStatus.value) {
    case 'default':
      return '已设为默认';
    case 'partial':
      return '部分设为默认';
    case 'other':
      return '尚未设为默认';
    case 'unsupported':
      return '当前系统不支持查询';
    default:
      return '默认应用状态无法确定';
  }
});
const windowsHeadline = computed(() => {
  const value = status.value;
  const installedKnown = value && typeof value.installed === 'boolean' ? value.installed : null;
  switch (windowsStatus.value) {
    case 'default':
      return '常用图片已由本工具打开';
    case 'partial':
      return '部分常用图片已由本工具打开，其余仍在其它应用';
    case 'other':
      return '已登记 10 个图片扩展名；常用的 .jpg / .png / .webp 仍由系统「照片」打开';
    case 'unsupported':
      return '当前系统不支持读取默认应用';
    default:
      return installedKnown === false
        ? '未检测到本工具的安装版登记（便携版不注册文件关联）'
        : '无法读取系统默认应用信息';
  }
});
const windowsCheckText = computed(() => {
  const value = status.value;
  if (!value) return '正在读取系统默认应用…';
  if (value.supported === false) return value.reason || '当前系统不支持查询默认应用';
  const rows = windowsRows.value;
  if (windowsStatus.value === 'unknown') {
    return value.reason || '无法读取注册表中的默认应用信息';
  }
  const own = rows.filter((row) => row.status === 'default').length;
  const other = rows.filter((row) => row.status === 'other').length;
  const unknown = rows.filter((row) => row.status === 'unknown').length;
  const parts = [`${own} 项指向本工具`, `${other} 项由其它应用持有`];
  if (unknown) parts.push(`${unknown} 项无法确定`);
  if (value.installed === false) parts.push('未检测到安装版登记');
  return parts.join(' · ');
});
const windowsRows = computed(() => {
  const value = status.value;
  if (!value || !Array.isArray(value.associations)) return [];
  return value.associations.map((row) => ({
    ext: row.ext,
    status: ['default', 'other', 'unknown'].includes(row.status) ? row.status : 'unknown',
    label: STATUS_LABEL[row.status] || STATUS_LABEL.unknown,
  }));
});
const linuxIsDefault = computed(() => {
  const value = status.value;
  if (!value || !value.supported) return false;
  const values = Object.values(value.isDefault || {});
  return values.length > 0 && values.every(Boolean);
});
const linuxResultText = computed(() => {
  const result = linuxResult.value;
  if (!result) return '';
  if (result.supported === false) return result.reason || '当前系统不支持写入 XDG 默认项';
  if (result.ok) return `已写入 ${(result.applied || []).length} 项 MIME 默认项`;
  const failed = result.failed || [];
  return `有 ${failed.length} 项写入失败，已保留原值：${failed.map((item) => item.mime).join('、')}`;
});

function syncDraft() {
  draft.value = Array.isArray(props.phrases) ? props.phrases.slice() : [];
  dirty.value = false;
  saveHint.value = `已保存 · ${draft.value.length} 条短语`;
}

function move(index, direction) {
  const next = index + direction;
  if (next < 0 || next >= draft.value.length) return;
  const list = draft.value.slice();
  const [item] = list.splice(index, 1);
  list.splice(next, 0, item);
  draft.value = list;
  dirty.value = true;
}

function remove(index) {
  draft.value = draft.value.filter((_, i) => i !== index);
  dirty.value = true;
  error.value = '';
}

function add() {
  const value = String(newPhrase.value || '').trim();
  const problem = validatePhrases(draft.value.concat([value]));
  if (problem) {
    error.value = problem;
    return;
  }
  draft.value = draft.value.concat([value]);
  newPhrase.value = '';
  dirty.value = true;
  error.value = '';
}

function restoreDefaults() {
  draft.value = DEFAULT_PHRASES.slice();
  dirty.value = true;
  error.value = '';
  saveHint.value = '已恢复默认，尚未保存';
}

async function save() {
  const problem = validatePhrases(draft.value);
  if (problem) {
    error.value = problem;
    return;
  }
  error.value = '';
  saving.value = true;
  try {
    const result = await api.saveQuickPhrases(draft.value.map((item) => String(item).trim()));
    draft.value = (result && result.quick_phrases) || draft.value;
    dirty.value = false;
    saveHint.value = `已保存 · ${draft.value.length} 条短语`;
    emit('saved', draft.value.slice());
    emit('notify', '快捷输入已保存');
  } catch (e) {
    error.value = api.cleanError(e) || '保存失败';
  } finally {
    saving.value = false;
  }
}

function onTabKeydown(event) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
  const index = SET_TABS.findIndex((item) => item.key === tabKey.value);
  const nextIndex =
    (index + (event.key === 'ArrowRight' ? 1 : -1) + SET_TABS.length) % SET_TABS.length;
  event.preventDefault();
  selectTab(SET_TABS[nextIndex].key);
  const button = document.getElementById(`it-set-tab-${SET_TABS[nextIndex].key}`);
  if (button) button.focus();
}

async function refreshDefaultStatus() {
  try {
    status.value = await api.getDefaultStatus();
  } catch (e) {
    emit('notify', api.cleanError(e) || '无法读取默认应用状态');
  }
}

async function openDefaults() {
  try {
    const result = await api.openDefaultSettings();
    if (result && result.supported === false) emit('notify', result.reason || '当前系统不支持');
    else emit('notify', '已打开系统默认应用设置');
  } catch (e) {
    emit('notify', api.cleanError(e) || '无法打开系统设置');
  }
}

async function setLinuxDefault() {
  settingLinux.value = true;
  try {
    linuxResult.value = await api.setDefaultLinux();
    await refreshDefaultStatus();
  } catch (e) {
    linuxResult.value = {
      ok: false,
      supported: true,
      failed: [{ mime: '—', error: api.cleanError(e) }],
    };
  } finally {
    settingLinux.value = false;
  }
}

watch(
  () => props.tab,
  (value) => {
    if (SET_TABS.some((item) => item.key === value) && value !== tabKey.value) tabKey.value = value;
  }
);

watch(
  () => props.phrases,
  () => syncDraft(),
  { immediate: true }
);

watch(
  () => props.defaultStatus,
  (value) => {
    if (value) status.value = value;
  },
  { immediate: true }
);

onMounted(() => {
  syncDraft();
  if (!status.value) refreshDefaultStatus();
});
</script>

<style scoped>
/* 逐扩展名的真实状态：只在本组件内生效，不引入新的窗口级样式 */
.it-assoc-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}
.it-assoc-list li {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 8px;
  border: 1px solid var(--it-line);
  border-radius: 4px;
  background: var(--it-fill);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.it-assoc-list b {
  font-weight: 600;
  color: var(--it-text);
}
.it-assoc-list .is-default {
  color: var(--it-green-ink);
}
.it-assoc-list .is-other {
  color: var(--it-orange-ink);
}
.it-assoc-list .is-unknown {
  color: var(--it-t3);
}
</style>
