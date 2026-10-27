<template>
  <div class="card wl-card">
    <!-- 任务锁（照片 op / 卡片级从商旅同步）：整卡遮罩进度 -->
    <div v-if="opLock" class="wl-op-lock">
      <div class="wl-op-box">
        <div class="wl-op-bar">
          <div class="wl-op-fill" :style="{ width: (opLock.pct || 0) + '%' }"></div>
        </div>
        <div class="wl-op-text">{{ opLock.text || '商旅同步中' }} {{ opLock.pct || 0 }}%</div>
      </div>
    </div>

    <!-- 卡头：车牌 + 目的地 + 状态标签 -->
    <div class="wl-card-head">
      <div class="wl-card-plate">
        <span class="wl-plate">{{ entry.plate_no || '未出车' }}</span>
        <span v-if="entry.destination_name" class="sub-text">→ {{ entry.destination_name }}</span>
        <span v-if="entry.cross_team" class="dot-tag blue"><span class="dot"></span>跨班</span>
      </div>
      <span class="dot-tag" :class="verifyClass"> <span class="dot"></span>{{ verifyText }} </span>
    </div>

    <!-- 巡视内容 -->
    <p v-if="entry.patrol_content" class="wl-patrol">{{ entry.patrol_content }}</p>

    <!-- 派车单号 -->
    <div v-if="entry.dispatch_order_no" class="wl-order">
      <span class="field-label">派车单号</span>
      <span class="num">{{ entry.dispatch_order_no }}</span>
    </div>

    <!-- 用车人：商旅打卡区（每用车人一行：状态 + 费用入口 + 开始/结束两枚 slot）；
         他班跨班卡或成员无商旅字段时回退打卡胶囊 -->
    <div v-if="entry.members && entry.members.length" class="wl-members-wrap">
      <span class="field-label">用车人</span>
      <div class="wl-mem-body">
        <template v-if="clockRow">
          <div v-for="row in sortedMembers" :key="row.id" class="ck-person">
            <div class="ck-head">
              <span class="ck-name">{{ row.name }}</span>
              <span v-if="memberStat(row).cls === 'ok'" class="ck-stat ok">{{
                memberStat(row).text
              }}</span>
              <span v-else-if="memberStat(row).cls === 'red'" class="ck-stat red">{{
                memberStat(row).text
              }}</span>
              <span v-else class="ck-stat">{{ memberStat(row).text }}</span>
              <button
                v-if="row.sgccBound !== false"
                type="button"
                class="ck-fee"
                title="修改当日费用信息（伙食补助 / 交通费）"
                @click="emit('fee-tap', { entry, member: row })"
              >
                费用
              </button>
            </div>
            <div class="ck-slots">
              <button
                v-for="seq in [1, 2]"
                :key="seq"
                type="button"
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
                  <span v-if="slotDone(row, seq)" class="ck-done-dot"></span>
                  {{ slotText(row, seq) }}
                </span>
                <span class="ck-t2">{{ slotSub(row, seq) }}</span>
                <span v-if="slotDone(row, seq) && isToday" class="ck-upd">更新</span>
              </button>
            </div>
          </div>
        </template>
        <template v-else>
          <button
            v-for="row in sortedMembers"
            :key="row.id"
            type="button"
            class="wl-member num"
            :class="{ checked: row.checked === 1 }"
            :title="row.checked === 1 ? '点击取消打卡' : '点击打卡'"
            @click="emit('check', { entry, row })"
          >
            <span class="wl-member-dot"></span>{{ row.name }}
          </button>
        </template>
      </div>
    </div>

    <!-- 照片缩略（点击看大图；商旅同步角标左上） -->
    <div v-if="entry.photos && entry.photos.length" class="wl-photos">
      <button
        v-for="(p, i) in entry.photos"
        :key="p.id"
        type="button"
        class="wl-photo"
        @click="emit('open-photo', { entry, index: i })"
      >
        <img :src="p.url" :alt="(p.members || []).join('、')" loading="lazy" />
        <span v-if="p.sgcc_synced === 1" class="wl-photo-sync" title="已同步至商旅费用照片"
          >商旅</span
        >
        <span
          v-else-if="p.sgcc_synced === 2"
          class="wl-photo-sync fail"
          title="同步商旅失败，可在大图中重试"
        >
          同步失败
        </span>
        <span class="wl-photo-tag" :class="photoClass(p)">{{ photoText(p) }}</span>
      </button>
    </div>

    <!-- 未通过原因（浅底 + 左色条，直角） -->
    <div v-if="entry.verify_reasons && entry.verify_reasons.length" class="wl-reasons">
      <div v-for="(r, i) in entry.verify_reasons" :key="i">{{ r }}</div>
    </div>

    <!-- 备注 + 附件（点击附件走预览 / 下载） -->
    <div v-if="entry.remark || (entry.remark_files && entry.remark_files.length)" class="wl-remark">
      <div v-if="entry.remark" class="sub-text">{{ entry.remark }}</div>
      <div v-if="entry.remark_files && entry.remark_files.length" class="wl-files">
        <button
          v-for="f in entry.remark_files"
          :key="f.cos_key"
          type="button"
          class="wl-file"
          @click="emit('remark-open', { entry, file: f })"
        >
          {{ f.name }}
        </button>
      </div>
    </div>

    <!-- 操作行 -->
    <div class="wl-card-foot">
      <button type="button" class="wl-act" @click="emit('manage-photos', entry)">
        照片（{{ (entry.photos || []).length }}）
      </button>
      <button
        v-if="clockRow"
        type="button"
        class="wl-act"
        title="仅拉取本卡用车人的当日打卡 / 费用，以商旅平台为准覆盖本地"
        @click="emit('sync-card', entry)"
      >
        从商旅同步
      </button>
      <button type="button" class="wl-act" @click="emit('edit', entry)">编辑</button>
      <button type="button" class="wl-act danger" @click="emit('remove', entry)">删除</button>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';

// 日志卡片：车牌/目的地/巡视内容/派车单号/商旅打卡区/照片缩略/备注/未通过原因。
// 交互一律抛事件由父视图调 API 后刷新，本组件不发请求。
const props = defineProps({
  entry: { type: Object, required: true },
  effTeamId: { type: Number, default: 0 }, // 当前生效班组（判他班跨班卡）
  opLock: { type: Object, default: null }, // {pct, text} 任务锁（照片 op / 卡片级同步）
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

// todayStr 今日（本地）YYYY-MM-DD。
const todayStr = new Date().toISOString().slice(0, 10);

// sortedMembers 用车人按 sort 排序。
const sortedMembers = computed(() =>
  [...(props.entry.members || [])].sort((a, b) => (a.sort || 0) - (b.sort || 0))
);

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

// verifyClass/verifyText 卡片核验状态标签（passed 绿 / failed 红 / exempt 灰）。
const verifyClass = computed(() => {
  const v = props.entry.verify_passed;
  if (v === 'passed') return 'green';
  if (v === 'failed') return 'red';
  return '';
});
const verifyText = computed(() => {
  const v = props.entry.verify_passed;
  if (v === 'passed') return '已验证';
  if (v === 'failed') return '待核实';
  return '免验证';
});

// photoClass/photoText 照片角标（验证状态 + 非水印标记）。
function photoClass(p) {
  if (p.is_watermark === 0) return 'plain';
  if (p.verify_status === 'passed') return 'ok';
  if (p.verify_status === 'failed' || p.verify_status === 'mismatch') return 'bad';
  return 'pending';
}
function photoText(p) {
  if (p.is_watermark === 0) return '非水印';
  return (
    { passed: '通过', mismatch: '不符', failed: '失败', pending: '识别中', skipped: '免验证' }[
      p.verify_status
    ] || '识别中'
  );
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

// memberStat 用车人状态小字（工时 / 还差结束打卡 / 未绑定商旅）。
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
  // 同 seq 已有他人打卡时提示带入定位
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
.wl-card {
  position: relative;
}
.wl-op-lock {
  position: absolute;
  inset: 0;
  background: rgba(255, 255, 255, 0.82);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 5;
  border-radius: 12px;
}
.wl-op-box {
  width: 70%;
  text-align: center;
}
.wl-op-bar {
  height: 6px;
  border-radius: 4px;
  background: var(--tk-fill);
  overflow: hidden;
}
.wl-op-fill {
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.3s;
}
.wl-op-text {
  margin-top: 6px;
  font-size: 12px;
  color: var(--tk-sub);
}
.wl-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.wl-card-plate {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.wl-plate {
  font-size: 15px;
  font-weight: 600;
  color: var(--tk-ink);
}
.wl-patrol {
  margin: 8px 0 0;
  color: var(--tk-sub);
  font-size: 13px;
  line-height: 1.6;
}
.wl-order {
  margin-top: 8px;
  display: flex;
  gap: 8px;
  font-size: 13px;
  color: var(--tk-ink);
}
.wl-members-wrap {
  margin-top: 10px;
  display: flex;
  gap: 8px;
  align-items: flex-start;
}
.wl-members-wrap > .field-label {
  width: 44px;
  flex-shrink: 0;
  padding-top: 4px;
}
.wl-mem-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
/* 用车人打卡胶囊（回退态） */
.wl-member {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid var(--tk-line);
  background: var(--tk-fill);
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
  align-self: flex-start;
}
.wl-member.checked {
  background: var(--tk-green-soft);
  border-color: transparent;
  color: #0b8a37;
}
.wl-member-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-dash);
}
.wl-member.checked .wl-member-dot {
  background: var(--tk-green);
}
/* 商旅打卡区（每用车人一行） */
.ck-person {
  border-bottom: 1px solid var(--tk-line);
  padding-bottom: 8px;
}
.ck-person:last-child {
  border-bottom: none;
  padding-bottom: 0;
}
.ck-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.ck-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--tk-ink);
}
.ck-stat {
  font-size: 11px;
  color: var(--tk-sub);
}
.ck-stat.ok {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  padding: 1px 8px;
  border-radius: 4px;
}
.ck-stat.red {
  color: var(--tk-red);
}
.ck-fee {
  margin-left: auto;
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  cursor: pointer;
}
.ck-slots {
  margin-top: 6px;
  display: flex;
  gap: 8px;
}
.ck-slot {
  position: relative;
  flex: 1;
  min-width: 0;
  text-align: left;
  padding: 6px 10px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
  cursor: pointer;
}
.ck-slot .ck-t1 {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-ink);
}
.ck-slot .ck-t2 {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-sub);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ck-slot.done {
  border-color: var(--tk-ink);
}
.ck-done-dot {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--tk-green);
  position: relative;
  flex-shrink: 0;
}
.ck-done-dot::after {
  content: '';
  position: absolute;
  left: 3px;
  top: 5px;
  width: 4px;
  height: 2px;
  border-left: 2px solid #fff;
  border-bottom: 2px solid #fff;
  transform: rotate(-45deg);
}
.ck-upd {
  position: absolute;
  top: 4px;
  right: 6px;
  font-size: 10px;
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
  padding: 0 5px;
  border-radius: 4px;
}
.ck-slot.todo {
  border-style: dashed;
  border-color: var(--tk-yellow);
  background: var(--tk-yellow-soft);
}
.ck-slot.todo .ck-t1 {
  color: var(--tk-yellow-ink, #d25f00);
}
.ck-slot.lock {
  background: var(--tk-fill);
  border-color: var(--tk-line);
  cursor: not-allowed;
}
.ck-slot.lock .ck-t1 {
  color: var(--tk-ph);
}
/* 照片缩略 */
.wl-photos {
  margin-top: 10px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.wl-photo {
  position: relative;
  width: 84px;
  height: 84px;
  padding: 0;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
  cursor: pointer;
  background: var(--tk-fill);
}
.wl-photo img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.wl-photo-tag {
  position: absolute;
  left: 0;
  bottom: 0;
  right: 0;
  font-size: 11px;
  text-align: center;
  padding: 1px 0;
  background: rgba(255, 255, 255, 0.92);
  color: var(--tk-sub);
}
.wl-photo-tag.ok {
  color: #0b8a37;
}
.wl-photo-tag.bad {
  color: var(--tk-red);
}
.wl-photo-tag.plain {
  color: var(--tk-ph);
}
.wl-photo-sync {
  position: absolute;
  left: 0;
  top: 0;
  font-size: 10px;
  padding: 1px 5px;
  background: var(--tk-brand);
  color: #fff;
}
.wl-photo-sync.fail {
  background: var(--tk-red);
}
.wl-reasons {
  margin-top: 10px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 8px 10px;
  font-size: 12px;
  color: var(--tk-red);
  line-height: 1.7;
}
.wl-remark {
  margin-top: 10px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  padding: 8px 10px;
  font-size: 12px;
}
.wl-files {
  margin-top: 4px;
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.wl-file {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 12px;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
}
.wl-card-foot {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--tk-line);
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
}
.wl-act {
  border: none;
  background: none;
  padding: 0;
  color: var(--tk-brand);
  font-size: 13px;
  cursor: pointer;
}
.wl-act.danger {
  color: var(--tk-red);
}
.wl-act:hover {
  opacity: 0.8;
}
</style>
