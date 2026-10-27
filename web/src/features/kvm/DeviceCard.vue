<template>
  <div class="card dev">
    <div class="dv-top">
      <span class="st" :class="statusClass"><i></i>{{ statusText }}</span>
      <span v-if="device.deviceGroupName" class="dv-tag">{{ device.deviceGroupName }}</span>
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
    <div class="dv-ops">
      <template v-if="device.maintaining">
        <span class="act off">终端</span>
        <span class="act off">远程控制</span>
        <span class="hint">维护中</span>
      </template>
      <template v-else-if="device.status === 'online' && device.ddns">
        <a class="act" @click="$emit('jump', 'term', device.ddns)">终端</a>
        <a class="act" @click="$emit('jump', 'desk', device.ddns)">远程控制</a>
      </template>
      <template v-else>
        <span class="act off">终端</span>
        <span class="act off">远程控制</span>
        <span class="hint">离线不可用</span>
      </template>
      <a class="act files" @click="$emit('open-files', device)">
        <svg viewBox="0 0 14 14" width="13" height="13">
          <path
            d="M2 3.5h4l1 1.5h5v6H2z"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linejoin="round"
          />
        </svg>
        文件互推
      </a>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { fmtDate } from './util.js';

const props = defineProps({
  device: { type: Object, required: true },
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
</script>

<style scoped>
.dev {
  display: flex;
  flex-direction: column;
  padding: 16px 20px 18px;
}
.dv-top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.dv-tag {
  margin-left: auto;
  font-size: 11.5px;
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.st {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  font-weight: 600;
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
.dv-name {
  margin-top: 12px;
  font-size: 15.5px;
  font-weight: 700;
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
.dv-info {
  margin-top: 14px;
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
}
.dv-info b {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-ink);
  word-break: break-all;
  text-align: right;
}
.dv-ops {
  margin-top: 15px;
  padding-top: 13px;
  border-top: 1px solid var(--tk-line);
  display: flex;
  align-items: center;
  gap: 16px;
}
.act {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12.5px;
  font-weight: 600;
  cursor: pointer;
  color: var(--tk-brand);
}
.act.off {
  color: var(--tk-dash);
  cursor: not-allowed;
}
.act.files {
  margin-left: auto;
  color: var(--tk-sub);
}
.hint {
  font-size: 11px;
  color: var(--tk-ph);
}
</style>
