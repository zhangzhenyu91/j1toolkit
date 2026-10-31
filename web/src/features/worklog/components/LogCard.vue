<template>
  <article class="card log-card anim-in" :class="{ in: shown }">
    <!-- 任务锁（照片 op / 卡片级从商旅同步）：整卡遮罩 + 居中进度块 -->
    <div v-if="opLock" class="card-lock">
      <div class="cl-box">
        <div class="cl-bar">
          <div class="cl-fill" :style="{ width: (opLock.pct || 0) + '%' }"></div>
        </div>
        <div class="cl-t">
          {{ opLock.text || '商旅同步中' }} {{ opLock.pct || 0 }}% · 暂不可操作
        </div>
      </div>
    </div>

    <!-- 卡头：车牌 20px 粗字 + 跨班签 + 日期序号 + 验证状态签 + 编辑入口 -->
    <div
      class="log-h"
      title="点击修改派车 / 用车人"
      role="button"
      tabindex="0"
      :aria-label="`修改派车 / 用车人：${entry.plate_no || '站内作业'}`"
      @click="emit('edit', entry)"
      @keydown.enter.prevent="onEditKey($event)"
      @keydown.space.prevent="onEditKey($event)"
    >
      <b class="num">{{ entry.plate_no || '站内作业' }}</b>
      <span v-if="entry.cross_team" class="tag-cross">跨班</span>
      <small class="num">{{ dateDot }} · 第 {{ idx + 1 }} / {{ total }} 条</small>
      <span class="tag" :class="verifyClass">{{ verifyText }}</span>
      <span class="he"><WlIcon name="edit" :size="14" /></span>
    </div>

    <!-- 未通过原因块（浅红底 + 左色条，直角） -->
    <div
      v-if="entry.verify_passed === 'failed' && entry.verify_reasons && entry.verify_reasons.length"
      class="fail-list"
    >
      <div class="fail-head"><WlIcon name="warn" :size="14" /><b>未通过原因</b></div>
      <div v-for="(r, i) in entry.verify_reasons" :key="i" class="fail-row">{{ r }}</div>
    </div>

    <!-- 卡片主体：左栏信息 / 右栏照片（照片侧为大侧 61.8%） -->
    <div class="log-body">
      <div class="log-side-l">
        <!-- 巡视内容块（点击进编辑） -->
        <div
          class="patrol"
          :class="{ none: !entry.patrol_content }"
          title="点击修改巡视内容"
          role="button"
          tabindex="0"
          @click="emit('edit', entry)"
          @keydown.enter.prevent="onEditKey($event)"
          @keydown.space.prevent="onEditKey($event)"
        >
          <span class="p-tag">巡视内容</span>
          <span class="p-edit"><WlIcon name="edit" :size="12" /></span>
          {{ entry.patrol_content || '未填写巡视内容' }}
        </div>

        <div class="log-rows">
          <!-- 派车车辆 -->
          <div class="lrow">
            <div class="li"><WlIcon name="car" :size="16" /></div>
            <div class="lk">派车车辆</div>
            <div class="lv">
              {{ noCar ? '未出车' : entry.plate_no }}
              <small>
                {{ noCar ? '站内作业，不参与验证' : '工程车辆'
                }}<template v-if="entry.dispatch_order_no">
                  · 单号 {{ entry.dispatch_order_no }}</template
                >
              </small>
            </div>
          </div>

          <!-- 用车人：商旅打卡区（每用车人一行）；他班跨班卡回退打卡胶囊 -->
          <div class="lrow" :class="{ 'lrow-ck': clockRow }">
            <div class="li"><WlIcon name="user" :size="16" /></div>
            <div class="lk">用车人</div>
            <div class="lv">
              <template v-if="clockRow">
                <div v-for="row in sortedMembers" :key="row.id" class="ck-person">
                  <div class="ck-head">
                    <span class="ck-name">{{ row.name }}</span>
                    <span class="ck-stat" :class="memberStat(row).cls">
                      <WlIcon v-if="memberStat(row).cls === 'red'" name="lock-on" :size="10" />
                      {{ memberStat(row).text }}
                    </span>
                    <span
                      v-if="row.sgccBound !== false"
                      class="ck-fee"
                      title="修改当日费用信息（伙食补助 / 交通费）"
                      @click="emit('fee-tap', { entry, member: row })"
                    >
                      <WlIcon name="money" :size="11" />费用
                    </span>
                  </div>
                  <div class="ck-slots">
                    <span
                      v-for="seq in [1, 2]"
                      :key="seq"
                      class="ck-slot"
                      :class="slotClass(row, seq)"
                      :title="slotTitle(row, seq)"
                      @click="
                        emit('clock-tap', {
                          entry,
                          member: row,
                          seq,
                          action: slotDone(row, seq) ? 'update' : 'mark',
                        })
                      "
                    >
                      <span class="ck-t1">
                        <WlIcon
                          v-if="!slotDone(row, seq) && slotClass(row, seq) === 'lock'"
                          name="lock-on"
                          :size="10"
                        />
                        <span v-if="slotDone(row, seq)" class="ck-done-dot"></span>
                        {{ slotText(row, seq) }}
                      </span>
                      <span class="ck-t2">{{ slotSub(row, seq) }}</span>
                      <span v-if="slotDone(row, seq) && isToday" class="ck-upd">
                        <WlIcon name="refresh" :size="9" />更新
                      </span>
                    </span>
                  </div>
                </div>
              </template>
              <template v-else>
                <span
                  v-for="row in sortedMembers"
                  :key="row.id"
                  class="mchip"
                  :class="{ on: row.checked === 1 }"
                  :title="row.checked === 1 ? '点击取消打卡' : '点击打卡'"
                  @click="emit('check', { entry, row })"
                >
                  <span class="m-dot"></span>{{ row.name }}
                </span>
                <span v-if="!sortedMembers.length" style="color: var(--tk-ph)">无</span>
                <small v-if="sortedMembers.length"
                  >已打卡 {{ checkedN }} / {{ sortedMembers.length }} 人</small
                >
              </template>
            </div>
          </div>

          <!-- 目的地（费用口径标记：市外 / 驻地互斥，默认市内不标注） -->
          <div class="lrow">
            <div class="li"><WlIcon name="location" :size="16" /></div>
            <div class="lk">目的地</div>
            <div class="lv">
              {{ noCar ? '—' : entry.destination_name || '未选择' }}
              <em v-if="!noCar && entry.destination_out_of_city === 1" class="dest-mark out"
                >市外</em
              >
              <em v-if="!noCar && entry.destination_stationed === 1" class="dest-mark sta">驻地</em>
            </div>
          </div>
        </div>

        <!-- 备注块：目的地下方；无文字且无附件不渲染，点击进编辑 -->
        <div
          v-if="entry.remark || (entry.remark_files && entry.remark_files.length)"
          class="rmk"
          title="点击修改备注"
          @click="emit('edit', entry)"
        >
          <span class="rk-tag">备注</span>
          <span class="rk-edit"><WlIcon name="edit" :size="12" /></span>
          <div v-if="entry.remark" class="rk-text">{{ entry.remark }}</div>
          <div v-if="mediaFiles.length" class="rk-files">
            <span
              v-for="(f, i) in mediaFiles"
              :key="f.cos_key"
              class="rk-thumb"
              title="点击预览"
              @click.stop="emit('remark-open', { entry, file: f, index: i })"
            >
              <img v-if="f.type === 'image'" :src="f.url" loading="lazy" alt="备注图片" />
              <span v-else class="rk-play"><WlIcon name="play" :size="20" /></span>
            </span>
          </div>
          <div v-if="docFiles.length" class="rk-docs">
            <span v-for="f in docFiles" :key="f.cos_key" class="rk-doc">
              <WlIcon name="file" :size="13" />
              <span
                class="fn"
                title="点击预览"
                @click.stop="emit('remark-open', { entry, file: f })"
                >{{ f.name }}</span
              >
              <span
                class="dl"
                title="下载"
                @click.stop="emit('remark-open', { entry, file: f, download: true })"
              >
                <WlIcon name="download" :size="13" />
              </span>
            </span>
          </div>
        </div>

        <!-- 左栏底部操作行：从商旅同步 / 备注 / 删除（文字链） -->
        <div class="log-acts">
          <button
            v-if="clockRow && !opLock"
            type="button"
            class="act-sync"
            title="仅拉取本卡用车人的当日打卡 / 费用 / 照片，以商旅平台为准覆盖本地"
            @click="emit('sync-card', entry)"
          >
            从商旅同步
          </button>
          <button type="button" class="act-note" @click="emit('edit', entry)">备注</button>
          <button
            v-if="!entry.cross_team || canDeleteCross"
            type="button"
            class="act-del"
            @click="emit('remove', entry)"
          >
            删除
          </button>
        </div>
      </div>

      <!-- 右栏：水印照片网格 -->
      <div class="log-side-r">
        <template v-if="entry.photos && entry.photos.length">
          <div class="ph-lab">照片 · {{ entry.photos.length }} 张（点击查看大图与核验信息）</div>
          <div class="photos">
            <figure
              v-for="(p, i) in entry.photos"
              :key="p.id"
              class="ph"
              @click="emit('open-photo', { entry, index: i })"
            >
              <img
                :src="p.url"
                loading="lazy"
                decoding="async"
                :alt="p.is_watermark === 0 ? '非水印照片' : '水印照片'"
              />
              <span
                v-if="p.is_watermark !== 0"
                class="ph-st"
                :class="[photoStatus(p).cls, { ed: p.verify_status !== 'pending' }]"
                :title="p.verify_status !== 'pending' ? '点击修正水印信息' : ''"
                @click.stop="p.verify_status !== 'pending' && emit('manage-photos', entry)"
              >
                {{ photoStatus(p).txt }}
              </span>
              <span v-if="p.sgcc_synced === 1" class="ph-sync" title="已同步至商旅费用照片"
                >商旅</span
              >
              <span
                v-else-if="p.sgcc_synced === 2"
                class="ph-sync fail"
                title="同步商旅失败，可在大图中重试"
                >同步失败</span
              >
              <span v-if="photoMismatch(p).length" class="ph-miss">
                <span v-for="m in photoMismatch(p)" :key="m" class="ph-mis">{{ m }}</span>
              </span>
              <figcaption class="ph-info">
                <span>{{ (p.members || []).join('、') }}</span>
                <span class="num">{{ String(p.shot_time || '').slice(0, 16) }}</span>
              </figcaption>
            </figure>
            <div
              v-if="!noCar"
              class="ph-add"
              title="上传照片"
              @click="emit('manage-photos', entry)"
            >
              <WlIcon name="add" :size="20" />
              <span>添加照片</span>
            </div>
          </div>
        </template>
        <div
          v-else-if="!noCar"
          class="photos-empty can-add"
          title="上传照片"
          @click="emit('manage-photos', entry)"
        >
          ＋ 添加照片
        </div>
        <div v-else class="photos-empty">暂无水印照片</div>
      </div>
    </div>
  </article>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import WlIcon from './WlIcon.vue';
import { isActivationKey, isNoCar } from '../logic.js';
import { localDay } from '../date.js';

// 日志卡片（旧版 .log-card 双栏结构还原）：卡头 / 未通过原因 / 左信息栏（巡视内容、
// 派车车辆、用车人打卡区、目的地、备注）/ 右照片栏（3 列网格 + 状态角标）。
// 交互一律抛事件由父视图调 API 后刷新，本组件不发请求。
const props = defineProps({
  entry: { type: Object, required: true },
  effTeamId: { type: Number, default: 0 }, // 当前生效班组（判他班跨班卡）
  opLock: { type: Object, default: null }, // {pct, text} 任务锁（照片 op / 卡片级同步）
  idx: { type: Number, default: 0 }, // 当日序号（从 0 起）
  total: { type: Number, default: 1 }, // 当日条数
  canDeleteCross: { type: Boolean, default: false }, // 跨班卡是否可删（仅超管）
});
const emit = defineEmits([
  'edit',
  'remove',
  'check',
  'manage-photos',
  'open-photo',
  'clock-tap',
  'fee-tap',
  'sync-card',
  'remark-open',
]);

// shown 入场动画触发（同旧版 renderCards：40ms + i*70ms 错峰上浮；reduced-motion 立即落位；
// 静默刷新复用组件不重播，仅挂载时播一次）。
const shown = ref(false);
let showTimer = 0;
onMounted(() => {
  const rm =
    typeof window !== 'undefined' &&
    window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const delay = rm ? 0 : 40 + Math.min(props.idx, 6) * 70;
  showTimer = setTimeout(() => {
    shown.value = true;
  }, delay);
});
onBeforeUnmount(() => {
  if (showTimer) clearTimeout(showTimer);
});

// todayStr 今日（本地）YYYY-MM-DD。
const todayStr = localDay();

// dateDot 卡头日期（点分）。
const dateDot = computed(() => String(props.entry.log_date || '').replace(/-/g, '.'));

// noCar 未出车卡（站内作业）：vehicle_id=0 与历史 null 均按未出车。
const noCar = computed(() => isNoCar(props.entry.vehicle_id));

// onEditKey 键盘编辑：仅 Enter / Space 触发，防止普通按键误开弹层。
function onEditKey(e) {
  if (isActivationKey(e && e.key)) emit('edit', props.entry);
}

// sortedMembers 用车人按 sort 排序。
const sortedMembers = computed(() =>
  [...(props.entry.members || [])].sort((a, b) => (a.sort || 0) - (b.sort || 0))
);

// checkedN 已打卡人数（回退胶囊态副标）。
const checkedN = computed(() => sortedMembers.value.filter((m) => m.checked === 1).length);

// isToday 本卡是否当日（仅当日可打卡）。
const isToday = computed(() => props.entry.log_date === todayStr);

// crossOther 他班跨班卡（归属非当前生效班组）：不渲染商旅打卡区与同步入口。
const crossOther = computed(
  () =>
    !!props.entry.cross_team &&
    !!props.entry.team_id &&
    props.effTeamId > 0 &&
    props.entry.team_id !== props.effTeamId
);

// clockRow 是否渲染商旅打卡区（成员带 sgccBound 字段且非他班跨班卡）。
const clockRow = computed(
  () => !crossOther.value && (props.entry.members || []).some((m) => m.sgccBound !== undefined)
);

// mediaFiles/docFiles 备注附件（图片/视频缩略、文档文件条）。
const mediaFiles = computed(() => (props.entry.remark_files || []).filter((f) => f.type !== 'doc'));
const docFiles = computed(() => (props.entry.remark_files || []).filter((f) => f.type === 'doc'));

// verifyClass/verifyText 卡片核验状态签（通过浅绿 / 未通过浅红 / 免验证灰）。
const verifyClass = computed(() => {
  const v = props.entry.verify_passed;
  if (v === 'passed') return 'tag-ok';
  if (v === 'failed') return 'tag-red';
  return 'tag-navy';
});
const verifyText = computed(() => {
  const v = props.entry.verify_passed;
  if (v === 'passed') return '验证通过';
  if (v === 'failed') return '未通过';
  return '免验证';
});

// photoStatus 照片验证角标（pending=验证中 / passed=核验通过 / 其余=验证失败）。
function photoStatus(p) {
  if (p.verify_status === 'pending') return { cls: 'ing', txt: '验证中' };
  if (p.verify_status === 'passed') return { cls: 'pass', txt: '核验通过' };
  return { cls: 'fail', txt: '验证失败' };
}

// photoMismatch 完成态逐项列「日期不符」「地点不符」。
function photoMismatch(p) {
  const arr = [];
  if (p.date_ok === 0 || (p.date_ok == null && p.verify_status === 'date_mismatch'))
    arr.push('日期不符');
  if (p.dest_ok === 0 || (p.dest_ok == null && p.verify_status === 'dest_mismatch'))
    arr.push('地点不符');
  return arr;
}

// ckOf 取某人打卡记录（clockinMap[memberId][seq]，seq 1 开始 / 2 结束）。
function ckOf(row, seq) {
  const map = (props.entry.clockinMap || {})[String(row.member_id)] || {};
  return map[seq] || null;
}

// fmtHm 打卡时间取 HH:mm。
function fmtHm(v) {
  if (!v) return '';
  const m = /(\d{2}):(\d{2})/.exec(String(v));
  return m ? `${m[1]}:${m[2]}` : '';
}

// fmtWorkHours 工时展示：纯数字补 h 后缀。
function fmtWorkHours(w) {
  const s = String(w || '').trim();
  if (!s) return '';
  return /^\d+(\.\d+)?$/.test(s) ? s + 'h' : s;
}

// shortPosition 打卡地点短文案：去「中国」前缀与省/地级段。
function shortPosition(p) {
  let s = String(p || '')
    .replace(/^中国/, '')
    .replace(/^[^省]{1,12}省/, '');
  const m = /^[^市]{1,12}市(.+)$/.exec(s);
  if (m && /[市区县]/.test(m[1])) s = m[1];
  return s;
}

// memberStat 用车人状态小字（工时小签 / 还差结束打卡 / 未绑定商旅 / 登录过期）。
function memberStat(row) {
  const s1 = ckOf(row, 1);
  const s2 = ckOf(row, 2);
  if (row.sgccBound === false) return { cls: 'red', text: '未绑定商旅' };
  if (row.sgccBound === true && row.sgccTokenStatus === 0)
    return { cls: 'red', text: '商旅登录已过期' };
  if (s1 && s2) {
    const wh = fmtWorkHours(s2.workHours || s1.workHours);
    return { cls: 'ok', text: wh ? `工时 ${wh}` : '已完成两次打卡' };
  }
  if (s1) return { cls: '', text: '还差结束打卡' };
  return { cls: '', text: '未打卡' };
}

// slotDone 该 seq 是否已打卡。
function slotDone(row, seq) {
  const c = ckOf(row, seq);
  return !!(c && fmtHm(c.time));
}

// slotClass 单枚打卡 slot 三态（done / todo / lock）。
function slotClass(row, seq) {
  if (slotDone(row, seq)) return 'done';
  if (!isToday.value) return 'lock';
  if (row.sgccBound === false || row.sgccTokenStatus === 0) return 'lock';
  return 'todo';
}

// slotText slot 主文案。
function slotText(row, seq) {
  const lab = seq === 2 ? '结束' : '开始';
  const c = ckOf(row, seq);
  if (slotDone(row, seq)) return `${lab} ${fmtHm(c.time)}`;
  return `${lab}打卡`;
}

// slotSub slot 副行（地点短文案 / 引导文案 / 点击打卡）。
function slotSub(row, seq) {
  const c = ckOf(row, seq);
  if (c && fmtHm(c.time)) return shortPosition(c.position) || '地点未知';
  if (!isToday.value) return '仅当日可打卡';
  if (row.sgccBound === false) return '引导本人至「我的」页绑定';
  if (row.sgccTokenStatus === 0) return '登录已过期，请本人重新登录';
  const ck = props.entry.clockinMap || {};
  const bringIn = Object.keys(ck).some((k) => ck[k] && ck[k][seq] && ck[k][seq].position);
  return bringIn ? '点击打卡 · 带入同记录定位' : '点击打卡';
}

// slotTitle slot 悬停完整地址。
function slotTitle(row, seq) {
  const c = ckOf(row, seq);
  return c && c.position ? c.position : '';
}
</script>

<style scoped>
/* ===== 盒模型基线（对齐旧版 theme.css * reset） ===== */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

/* ===== 卡片壳（12px 圆角轻阴影；卡顶色带无样式） ===== */
/* 中和 styles.css `.card + .card` 相邻上边距（同 KvmView 口径）：卡间距统一由 margin-bottom 掌管 */
.card.log-card {
  position: relative;
  overflow: hidden;
  margin-top: 0;
  margin-bottom: 18px;
  padding: 0;
  border-radius: 12px;
}

/* ===== 卡片入场（旧版 anim-in：短距上浮 + 错峰；JS 挂载后加 .in） ===== */
.anim-in {
  opacity: 0;
  transform: translateY(16px);
  transition:
    opacity 0.5s ease,
    transform 0.5s var(--tk-ease);
}
.anim-in.in {
  opacity: 1;
  transform: none;
}

/* ===== 任务锁遮罩 ===== */
.card-lock {
  position: absolute;
  inset: 0;
  z-index: 5;
  background: var(--tk-lock, rgba(255, 255, 255, 0.82));
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: inherit;
  cursor: not-allowed;
}
.cl-box {
  width: 240px;
  background: var(--tk-card, #ffffff);
  border: 1px solid var(--tk-line);
  border-radius: 12px;
  padding: 16px 18px;
  box-shadow: var(--tk-shadow-hover);
}
.cl-bar {
  height: 8px;
  border-radius: 999px;
  background: var(--tk-fill);
  overflow: hidden;
}
.cl-fill {
  height: 100%;
  border-radius: 999px;
  background: var(--tk-brand);
  transition: width 0.4s ease;
}
.cl-t {
  margin-top: 8px;
  font-size: 11px;
  font-weight: 600;
  color: var(--tk-brand);
  letter-spacing: 0.5px;
  text-align: center;
}

/* ===== 卡头（可点击进编辑）：车牌 20px 粗字 + 日期序号 + 状态签 + 编辑图标 ===== */
.log-h {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px 22px 14px;
  border-bottom: 1px solid var(--tk-line);
  cursor: pointer;
  transition: background 0.2s;
}
.log-h:hover {
  background: var(--tk-fill);
}
.log-h b {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 1px;
  font-variant-numeric: tabular-nums;
}
.log-h small {
  font-size: 12px;
  font-weight: 400;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  font-variant-numeric: tabular-nums;
}
/* 状态小胶囊：通过浅绿底深绿字 / 未通过浅红底红字 / 免验证灰 */
.log-h .tag {
  margin-left: auto;
  flex: none;
  padding: 3px 10px;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.5px;
  line-height: 1.4;
}
.log-h .tag::before {
  content: '';
  width: 5px;
  height: 5px;
  border-radius: 50%;
}
.tag-ok {
  background: var(--tk-green-soft);
  color: #0b8a37;
}
.tag-ok::before {
  background: #0b8a37;
}
.tag-red {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.tag-red::before {
  background: var(--tk-red);
}
.tag-navy {
  background: var(--tk-fill);
  color: var(--tk-ph);
}
.tag-navy::before {
  background: var(--tk-ph);
}
.log-h .he {
  flex: none;
  width: 26px;
  height: 26px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--tk-ph);
  transition:
    color 0.2s,
    background 0.2s;
}
.log-h:hover .he {
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
/* 跨班日志签（浅蓝底蓝字小胶囊） */
.tag-cross {
  flex: none;
  display: inline-flex;
  align-items: center;
  background: var(--tk-blue-soft);
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--tk-brand);
  letter-spacing: 1px;
  line-height: 1.4;
  white-space: nowrap;
}

/* ===== 未通过原因块（浅红底 + 4px 红左边条，直角） ===== */
.fail-list {
  margin: 12px 22px 0;
  padding: 10px 14px;
  border-radius: 0;
  background: var(--tk-red-soft);
  border-left: 4px solid var(--tk-red);
}
.fail-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 5px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-red);
  letter-spacing: 0.5px;
}
.fail-head svg {
  flex: none;
}
.fail-row {
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
  line-height: 1.7;
  padding: 1.5px 0;
}

/* ===== 卡片主体：左栏信息 / 右栏照片（grid 38.2%/1fr，两栏宽度全卡一致） ===== */
.log-body {
  display: grid;
  grid-template-columns: minmax(250px, 38.2%) minmax(0, 1fr);
  align-items: stretch;
}
.log-side-l {
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.log-side-r {
  min-width: 0;
  border-left: 1px solid var(--tk-line);
  display: flex;
  flex-direction: column;
}
.log-rows {
  padding: 6px 22px;
}
.lrow {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 0;
  border-bottom: 1px dashed var(--tk-hair, rgba(29, 33, 41, 0.06));
}
.lrow:last-child {
  border-bottom: none;
}
.lrow .li {
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
/* 图标块内容居中：svg 作块级 flex 子项，避免行内基线间隙把图标顶偏 */
.lrow .li svg {
  display: block;
}
.lrow .lk {
  width: 56px;
  flex: none;
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 1px;
}
.lrow .lv {
  font-size: 13.5px;
  font-weight: 600;
}
.lrow .lv small {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-ph);
  font-weight: 400;
}

/* 用车人打卡胶囊（跨班回退展示）：已打卡绿实底白字 */
.mchip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  margin: 2px 6px 2px 0;
  padding: 2px 10px;
  border: 1px solid var(--tk-dash);
  border-radius: 999px;
  font-size: 12px;
  font-weight: 400;
  color: var(--tk-sub);
  cursor: pointer;
  user-select: none;
  transition:
    border-color 0.2s,
    color 0.2s,
    background 0.2s;
}
.mchip:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.mchip.on {
  border-color: var(--tk-green);
  color: var(--tk-on-brand, #ffffff);
  background: var(--tk-green);
  font-weight: 600;
}
.m-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-dash);
}
.mchip.on .m-dot {
  background: var(--tk-card, #ffffff);
}

/* ===== 巡视内容块（浅蓝底圆角块，点击进编辑） ===== */
.patrol {
  margin: 12px 22px 0;
  padding: 10px 14px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  font-size: 12.5px;
  color: var(--tk-ink);
  line-height: 1.7;
  cursor: pointer;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  transition:
    box-shadow 0.15s var(--tk-ease),
    background 0.2s;
}
.patrol:hover {
  box-shadow: var(--tk-shadow-hover);
}
.patrol .p-tag {
  display: inline-block;
  margin-right: 8px;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--tk-on-brand, #ffffff);
  background: var(--tk-brand);
  border-radius: 4px;
  padding: 1px 6px;
  letter-spacing: 1px;
}
.patrol .p-edit {
  float: right;
  color: var(--tk-ph);
  display: flex;
  align-items: center;
}
.patrol:hover .p-edit {
  color: var(--tk-brand);
}
.patrol.none {
  color: var(--tk-ph);
}

/* ===== 备注块（浅橙底 + 4px 橙左边条，直角；点击进编辑） ===== */
.rmk {
  margin: 12px 22px 0;
  padding: 10px 12px;
  border-left: 4px solid var(--tk-yellow);
  border-radius: 0;
  background: var(--tk-yellow-soft);
  cursor: pointer;
  transition: box-shadow 0.15s var(--tk-ease);
}
.rmk:hover {
  box-shadow: var(--tk-shadow-card);
}
.rmk .rk-tag {
  display: inline-block;
  margin-right: 8px;
  font-size: 10.5px;
  font-weight: 600;
  color: #d25f00;
  background: rgba(255, 125, 0, 0.12);
  border-radius: 4px;
  padding: 1px 6px;
  letter-spacing: 1px;
}
.rmk .rk-edit {
  float: right;
  color: var(--tk-sub);
  display: flex;
  align-items: center;
}
.rmk:hover .rk-edit {
  color: var(--tk-ink);
}
.rmk .rk-text {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-sub);
  line-height: 1.7;
  word-break: break-all;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
}
/* 附件：图片 / 视频缩略图（视频深色底 + 播放标） */
.rmk .rk-files {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}
.rk-thumb {
  position: relative;
  width: 52px;
  height: 52px;
  border-radius: 8px;
  overflow: hidden;
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  cursor: zoom-in;
  display: block;
}
.rk-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.rk-thumb .rk-play {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  background: rgba(29, 33, 41, 0.38);
}
/* 附件：文档文件条（下划线品牌蓝文件名 + 行尾下载钮） */
.rmk .rk-docs {
  display: flex;
  flex-direction: column;
  gap: 5px;
  margin-top: 8px;
}
.rk-doc {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  min-width: 0;
}
.rk-doc > svg {
  flex: none;
  color: var(--tk-ph);
}
.rk-doc .fn {
  color: var(--tk-brand);
  font-weight: 600;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rk-doc .dl {
  flex: none;
  margin-left: auto;
  color: var(--tk-ph);
  cursor: pointer;
  display: flex;
  align-items: center;
  padding: 3px;
  border-radius: 8px;
  transition:
    color 0.2s,
    background 0.2s;
}
.rk-doc .dl:hover {
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
}

/* ===== 左栏底部操作行（沉底文字链） ===== */
.log-acts {
  display: flex;
  justify-content: flex-end;
  gap: 16px;
  margin-top: auto;
  padding: 8px 22px 14px;
}
.act-sync,
.act-note,
.act-del {
  border: none;
  background: transparent;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.5px;
  cursor: pointer;
  padding: 2px 0;
  border-radius: 4px;
}
.act-sync:hover,
.act-note:hover,
.act-del:hover {
  text-decoration: underline;
  text-underline-offset: 3px;
}
.act-sync {
  color: var(--tk-brand);
}
.act-note {
  color: var(--tk-ink);
}
.act-del {
  color: var(--tk-red);
}

/* ===== 商旅打卡区（每用车人一行：头行 + 开始/结束两枚 slot） ===== */
.lrow-ck {
  align-items: flex-start;
}
/* 图标块/「用车人」标签与打卡区首行（.ck-head）共用同一中心线：
   .ck-head 定高 30px、图标块 30px、标签行高 30px，三者垂直中心重合；
   水平方向图标块/标签列宽与其余信息行一致（30px 图标块 + 12px 间距 + 56px 标签列） */
.lrow-ck .li,
.lrow-ck .lk {
  margin-top: 0;
}
.lrow-ck .lk {
  line-height: 30px;
}
.lrow-ck .lv {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 400;
}
.ck-person + .ck-person {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px dashed var(--tk-line);
}
.ck-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  min-height: 30px;
  margin-bottom: 6px;
}
.ck-name {
  font-size: 13px;
  font-weight: 700;
  color: var(--tk-ink);
  white-space: nowrap;
}
.ck-stat {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 10.5px;
  color: var(--tk-ph);
  white-space: nowrap;
}
.ck-stat.ok {
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
  border-radius: 4px;
  padding: 1px 6px;
  font-weight: 600;
}
.ck-stat.red {
  color: var(--tk-red);
}
.ck-slots {
  display: flex;
  gap: 8px;
}
/* slot 三态：done=白底墨边 + 绿圆白勾 / todo=橙虚线浅橙底 / lock=灰底灰边锁 */
.ck-slot {
  position: relative;
  flex: 1;
  min-width: 0;
  border-radius: 6px;
  padding: 6px 9px;
  cursor: pointer;
  user-select: none;
}
.ck-t1 {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11.5px;
  font-weight: 700;
  color: var(--tk-ink);
}
.ck-t1 svg {
  flex: none;
}
.ck-t2 {
  display: block;
  margin-top: 2px;
  font-size: 10px;
  line-height: 1.5;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--tk-sub);
}
.ck-slot.done {
  border: 1px solid var(--tk-line);
  background: var(--tk-card, #ffffff);
}
.ck-slot.done .ck-t1 {
  padding-right: 30px; /* 避开右上角「更新」小标 */
}
.ck-done-dot {
  width: 13px;
  height: 13px;
  flex: none;
  border-radius: 50%;
  background: var(--tk-green);
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.ck-done-dot::before {
  content: '✓';
  color: var(--tk-on-brand, #ffffff);
  font-size: 9px;
  font-weight: 700;
  line-height: 1;
}
.ck-upd {
  position: absolute;
  right: 6px;
  top: 6px;
  font-size: 9.5px;
  font-weight: 700;
  color: var(--tk-brand);
  display: inline-flex;
  align-items: center;
  gap: 1px;
}
.ck-slot.todo {
  border: 1.5px dashed var(--tk-yellow);
  background: var(--tk-yellow-soft);
}
.ck-slot.todo .ck-t1::before {
  content: '';
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--tk-yellow);
}
.ck-slot.todo .ck-t2 {
  color: #d25f00;
}
.ck-slot.lock {
  border: 1.5px solid var(--tk-line);
  background: var(--tk-fill);
  cursor: not-allowed;
}
.ck-slot.lock .ck-t1 {
  color: var(--tk-ph);
}
.ck-slot.lock .ck-t2 {
  color: var(--tk-dash);
  white-space: normal;
}
/* 费用入口（头行最右端描边小签） */
.ck-fee {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex: none;
  border: 1px solid var(--tk-brand);
  border-radius: 6px;
  padding: 2px 10px;
  font-size: 10.5px;
  font-weight: 700;
  color: var(--tk-brand);
  cursor: pointer;
  user-select: none;
  transition: background 0.2s;
}
.ck-fee:hover {
  background: var(--tk-blue-soft);
}

/* ===== 右栏照片网格（3 列，行高 1fr 均分吃满剩余高度） ===== */
.ph-lab {
  padding: 14px 22px 10px;
  font-size: 11px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 2px;
}
.photos {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  grid-auto-rows: 1fr;
  gap: 10px;
  padding: 0 22px 20px;
  flex: 1;
}
.ph {
  position: relative;
  min-height: 148px;
  margin: 0;
  border-radius: 8px;
  overflow: hidden;
  cursor: pointer;
  background: var(--tk-fill);
  transition:
    opacity 0.32s var(--tk-ease) var(--pd, 0s),
    transform 0.32s var(--tk-ease) var(--pd, 0s),
    box-shadow 0.18s var(--tk-ease) 0s;
}
/* 照片逐张短距上浮，前五项错峰；静默/切日重渲染复用组件时不重播 */
.anim-in:not(.in) .ph {
  opacity: 0;
  transform: translateY(14px);
}
.anim-in.in .ph:nth-child(2) {
  --pd: 0.05s;
}
.anim-in.in .ph:nth-child(3) {
  --pd: 0.1s;
}
.anim-in.in .ph:nth-child(4) {
  --pd: 0.15s;
}
.anim-in.in .ph:nth-child(5) {
  --pd: 0.2s;
}
.anim-in.in .ph:nth-child(n + 6) {
  --pd: 0.25s;
}
.ph:hover {
  box-shadow: var(--tk-shadow-hover);
}
.ph img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
/* 验证状态角标：右上小胶囊（绿=核验通过 / 蓝=验证中 / 红=验证失败，白字 + 白圆点） */
.ph-st {
  position: absolute;
  top: 6px;
  right: 6px;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 7px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.5px;
  color: #fff;
}
.ph-st::before {
  content: '';
  flex: none;
  display: block;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--tk-card, #ffffff);
}
.ph-st.pass {
  background: rgba(0, 180, 42, 0.92);
}
.ph-st.ing {
  background: rgba(14, 61, 168, 0.92);
}
.ph-st.fail {
  background: rgba(245, 63, 63, 0.92);
}
.ph-st.ed {
  cursor: pointer;
  outline: 1.5px dashed rgba(255, 255, 255, 0.8);
  outline-offset: -2px;
}
/* 「日期/地点不符」chips 列（让位状态角标） */
.ph-miss {
  position: absolute;
  top: 28px;
  right: 6px;
  z-index: 2;
  display: flex;
  flex-direction: column;
  gap: 3px;
  align-items: flex-end;
}
.ph-mis {
  border-radius: 4px;
  padding: 1px 6px;
  font-size: 9.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  color: var(--tk-on-brand, #ffffff);
  background: var(--tk-red);
}
/* 照片底部信息条：墨色半透明平涂 */
.ph-info {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 2;
  padding: 4px 8px;
  display: flex;
  justify-content: space-between;
  gap: 6px;
  font-size: 9.5px;
  font-weight: 600;
  color: #fff;
  letter-spacing: 0.8px;
  background: rgba(29, 33, 41, 0.74);
}
/* 商旅同步签（左上直角小签） */
.ph-sync {
  position: absolute;
  top: 6px;
  left: 6px;
  z-index: 2;
  border-radius: 8px;
  padding: 2px 7px;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-on-brand, #ffffff);
  background: var(--tk-brand);
  border: 1.5px solid var(--tk-line);
}
.ph-sync.fail {
  background: var(--tk-red);
}
/* 添加照片入口（网格尾部虚线块 / 空态虚线框） */
.ph-add {
  min-height: 148px;
  border: 2px dashed var(--tk-dash);
  border-radius: 8px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  transition:
    border-color 0.2s,
    color 0.2s,
    background 0.2s;
}
.ph-add:hover,
.photos-empty.can-add:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.photos-empty {
  flex: 1;
  min-height: 140px;
  margin: 14px 22px 20px;
  border: 2px dashed var(--tk-dash);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.photos-empty.can-add {
  cursor: pointer;
  transition:
    border-color 0.2s,
    color 0.2s,
    background 0.2s;
}

/* ===== 响应式：月历占侧栏后卡片容不下双栏时上下排列 ===== */
@media (min-width: 641px) and (max-width: 1200px) {
  .log-body {
    grid-template-columns: minmax(0, 1fr);
  }
  .log-side-l {
    width: 100%;
  }
  .log-side-r {
    border-left: none;
    border-top: 1px solid var(--tk-line);
    margin-top: 12px;
  }
}
@media (max-width: 640px) {
  .photos {
    grid-template-columns: repeat(2, 1fr);
  }
  .log-body {
    grid-template-columns: minmax(0, 1fr);
  }
  .log-side-l {
    width: 100%;
    min-width: 0;
  }
  .log-side-r {
    border-left: none;
    border-top: 1px solid var(--tk-line);
    margin-top: 12px;
  }
}

/* ===== 目的地费用口径标记（市外 / 驻地互斥；同班组数据字典 dpill 口径） ===== */
.dest-mark {
  display: inline-flex;
  align-items: center;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 10.5px;
  font-weight: 700;
  font-style: normal;
  letter-spacing: 1px;
  line-height: 1.5;
  vertical-align: 1px;
}
.dest-mark.out {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.dest-mark.sta {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}

@media (prefers-reduced-motion: reduce) {
  /* 动效降级（同旧版 theme.css）：入场与照片错峰瞬时完成 */
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
  .anim-in,
  .anim-in:not(.in) .ph {
    opacity: 1;
    transform: none;
    transition: none;
  }
}
</style>
