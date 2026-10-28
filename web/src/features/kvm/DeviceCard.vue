<template>
  <div class="card dev" :class="{ 'fade-in': animate }" :style="animStyle">
    <div class="dv-in">
      <div class="dv-top">
        <span class="st" :class="statusClass"><i></i>{{ statusText }}</span>
        <span v-if="device.deviceGroupName" class="tag tag-navy">{{ device.deviceGroupName }}</span>
      </div>
      <div class="dv-name">{{ name }}</div>
      <div class="dv-ddns mono">{{ device.ddns || '' }}</div>
      <div class="dv-info">
        <div>
          <span>IP 地址</span><b class="mono">{{ device.ip || '—' }}</b>
        </div>
        <div>
          <span>MAC</span><b class="mono">{{ device.mac || '—' }}</b>
        </div>
        <div>
          <span>最近在线</span><b>{{ lastSeen }}</b>
        </div>
      </div>
      <!-- 卡底操作条（带态跳转平台；维护锁/离线置灰） -->
      <div class="dv-ops">
        <template v-if="device.maintaining">
          <span class="act off">
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4.5" width="18" height="15" rx="2" />
              <path d="M7 9.5l3 3-3 3" />
              <path d="M12.5 15.5H17" />
            </svg>
            终端
          </span>
          <span class="act off">
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4" width="18" height="12.5" rx="2" />
              <path d="M8.5 20.5h7M12 16.5v4" />
            </svg>
            远程控制
          </span>
          <a
            class="act files"
            role="button"
            tabindex="0"
            @click="$emit('open-files', device)"
            @keydown.enter.prevent="$emit('open-files', device)"
          >
            <svg
              viewBox="0 0 14 14"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linejoin="round"
            >
              <path d="M2 3.5h4l1 1.5h5v6H2z" />
            </svg>
            文件互推
          </a>
          <span class="hint">维护中</span>
        </template>
        <template v-else-if="device.status === 'online' && device.ddns">
          <a
            class="act"
            role="button"
            tabindex="0"
            @click="$emit('jump', 'term', device.ddns)"
            @keydown.enter.prevent="$emit('jump', 'term', device.ddns)"
          >
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4.5" width="18" height="15" rx="2" />
              <path d="M7 9.5l3 3-3 3" />
              <path d="M12.5 15.5H17" />
            </svg>
            终端
          </a>
          <a
            class="act"
            role="button"
            tabindex="0"
            @click="$emit('jump', 'desk', device.ddns)"
            @keydown.enter.prevent="$emit('jump', 'desk', device.ddns)"
          >
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4" width="18" height="12.5" rx="2" />
              <path d="M8.5 20.5h7M12 16.5v4" />
            </svg>
            远程控制
          </a>
          <a
            class="act files"
            role="button"
            tabindex="0"
            @click="$emit('open-files', device)"
            @keydown.enter.prevent="$emit('open-files', device)"
          >
            <svg
              viewBox="0 0 14 14"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linejoin="round"
            >
              <path d="M2 3.5h4l1 1.5h5v6H2z" />
            </svg>
            文件互推
          </a>
        </template>
        <template v-else>
          <span class="act off">
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4.5" width="18" height="15" rx="2" />
              <path d="M7 9.5l3 3-3 3" />
              <path d="M12.5 15.5H17" />
            </svg>
            终端
          </span>
          <span class="act off">
            <svg
              viewBox="0 0 24 24"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <rect x="3" y="4" width="18" height="12.5" rx="2" />
              <path d="M8.5 20.5h7M12 16.5v4" />
            </svg>
            远程控制
          </span>
          <a
            class="act files"
            role="button"
            tabindex="0"
            @click="$emit('open-files', device)"
            @keydown.enter.prevent="$emit('open-files', device)"
          >
            <svg
              viewBox="0 0 14 14"
              width="13"
              height="13"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linejoin="round"
            >
              <path d="M2 3.5h4l1 1.5h5v6H2z" />
            </svg>
            文件互推
          </a>
          <span class="hint">离线不可用</span>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
// 设备卡片（迁移旧页 kvm.html renderCards）：状态点 + 名称 + ddns + 信息行 + 卡底操作条。
// connectedTime 为 unix 秒（平台 last_seen_at）；maintaining 维护锁期间（每日派车单同步）置灰。
import { computed } from 'vue';
import { fmtDate } from './util.js';

const props = defineProps({
  device: { type: Object, required: true },
  // 入场动画：仅 init/filter 重排时逐项 40ms 阶梯延迟；手动/定时刷新不重播
  animate: { type: Boolean, default: false },
  index: { type: Number, default: 0 },
});
defineEmits(['jump', 'open-files']);

const name = computed(
  () => props.device.description || props.device.ddns || `设备 #${props.device.id}`
);
const statusText = computed(() =>
  props.device.status === 'online' ? '在线' : props.device.status === 'disabled' ? '禁用' : '离线'
);
const statusClass = computed(() =>
  props.device.status === 'online'
    ? 'st-online'
    : props.device.status === 'disabled'
      ? 'st-disabled'
      : 'st-offline'
);
const lastSeen = computed(() =>
  props.device.status === 'online'
    ? '当前在线'
    : props.device.connectedTime
      ? fmtDate(props.device.connectedTime)
      : '—'
);
const animStyle = computed(() =>
  props.animate ? { animationDelay: `${Math.min(props.index * 40, 240)}ms` } : null
);
</script>

<style scoped>
.dev {
  box-sizing: border-box;
  /* 旧页 .card 自身无内边距（内层 .dv-in 承担）；中和 styles.css .card 自带 padding */
  padding: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  transition:
    box-shadow 0.18s var(--tk-ease),
    transform 0.18s var(--tk-ease);
}
/* 旧页 .card-hover 口径：hover 上浮 2px + 影加深（无缩放） */
.dev:hover {
  transform: translateY(-2px);
  box-shadow: var(--tk-shadow-hover);
}
.dv-in {
  flex: 1;
  display: flex;
  flex-direction: column;
  padding: 16px 22px 20px;
}
.dv-top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.dv-top .tag {
  margin-left: auto;
}
/* 状态点 + 文案 */
.st {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
}
.st i {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.st-online {
  color: var(--tk-green);
}
.st-online i {
  background: var(--tk-green);
  box-shadow: 0 0 0 3px rgba(0, 180, 42, 0.15);
}
.st-offline {
  color: var(--tk-ph);
}
.st-offline i {
  background: var(--tk-dash);
}
.st-disabled {
  color: var(--tk-red);
}
.st-disabled i {
  background: var(--tk-red);
}
.tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  white-space: nowrap;
}
.tag-navy {
  background: var(--tk-fill);
  color: var(--tk-sub);
}
.dv-name {
  margin-top: 12px;
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 0.3px;
  word-break: break-all;
}
.dv-ddns {
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--tk-ph);
  word-break: break-all;
}
.mono {
  font-family: ui-monospace, 'SFMono-Regular', Consolas, monospace;
}
/* 信息行：标签左、值右；占满剩余高度使卡底操作条贴底、相邻卡按钮行对齐 */
.dv-info {
  margin-top: 14px;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dv-info > div {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}
.dv-info span {
  flex: none;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.dv-info b {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-ink);
  word-break: break-all;
  text-align: right;
}
/* 卡底操作条（贴底对齐；窄卡可换行不溢出） */
.dv-ops {
  margin-top: 15px;
  padding-top: 13px;
  border-top: 1px solid var(--hair);
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 16px;
}
.act {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
  cursor: pointer;
  color: var(--tk-brand);
}
.act svg {
  width: 13px;
  height: 13px;
  flex: none;
}
.act.off {
  color: var(--tk-dash);
  cursor: not-allowed;
}
.act.files {
  margin-left: auto;
  color: var(--tk-sub);
}
.dv-ops .hint {
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 0.5px;
  white-space: nowrap;
}
/* 入场：逐项阶梯上浮一次后静止 */
.fade-in {
  animation: dv-fade-up 0.55s var(--tk-ease) both;
}
@keyframes dv-fade-up {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .fade-in {
    animation: none;
  }
  .dev {
    transition: none;
  }
  .dev:hover {
    transform: none;
    box-shadow: var(--tk-shadow-hover);
  }
}
</style>
