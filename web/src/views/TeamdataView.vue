<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">班组数据</h1>
      <span class="page-sub">班组字典、运维线路与杆塔坐标维护（仅管理员）</span>
    </div>

    <div class="subnav">
      <button
        v-for="t in tabs"
        :key="t.key"
        type="button"
        class="subnav-item"
        :class="{ active: tab === t.key }"
        @click="tab = t.key"
      >
        {{ t.label }}
      </button>
    </div>

    <DictManager v-if="tab === 'vehicles'" kind="vehicles" title="车牌号" label="车牌" />
    <DictManager
      v-else-if="tab === 'destinations'"
      kind="destinations"
      title="目的地"
      label="目的地"
    />
    <DictManager v-else-if="tab === 'members'" kind="members" title="出工成员" label="成员" />
    <OplineManager v-else-if="tab === 'oplines'" />
    <TowerManager v-else />
  </div>
</template>

<script setup>
import { ref } from 'vue';
import DictManager from '../features/teamdata/components/DictManager.vue';
import OplineManager from '../features/teamdata/components/OplineManager.vue';
import TowerManager from '../features/teamdata/components/TowerManager.vue';

// 班组数据视图：车牌 / 目的地 / 出工成员 三字典 + 运维线路 + 杆塔信息（含线路汇总）。
// 数据走 features/teamdata/api.js 真实 Go API；管理门控由服务端 enforce（超管/班组管理员）。
const tabs = [
  { key: 'vehicles', label: '车牌号' },
  { key: 'destinations', label: '目的地' },
  { key: 'members', label: '出工成员' },
  { key: 'oplines', label: '运维线路' },
  { key: 'towers', label: '杆塔信息' },
];
const tab = ref('vehicles');
</script>
