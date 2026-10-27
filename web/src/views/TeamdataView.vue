<template>
  <div class="td-page">
    <template v-if="!forbidden">
      <!-- 页头：领标 + 标题 + 副标；右侧班组切换（仅超管可见） -->
      <div class="td-head">
        <div class="section-title">
          <span class="td-kick">TEAM DATA</span>
          <h3>班组数据</h3>
          <span class="more">车牌号、目的地、人员、杆塔信息与运维线路维护</span>
        </div>
        <div v-if="isAdmin" class="acts">
          <select v-model.number="teamId" class="f-sel td-team-sel" @change="onTeamChange">
            <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
        </div>
      </div>

      <!-- 分段页签：车牌号 / 目的地 / 人员 / 杆塔信息 / 运维线路（下划线式，激活蓝字 + 2px 蓝条） -->
      <div class="td-tabs">
        <button
          v-for="t in tabs"
          :key="t.key"
          type="button"
          :class="{ on: tab === t.key }"
          @click="tab = t.key"
        >
          <span v-html="icon(t.icon, 15)"></span>{{ t.label }}
        </button>
      </div>

      <!-- 页签主体：前三段共用字典行卡，杆塔信息 / 运维线路各自独立板块；切段/换班组整块重挂载 -->
      <div class="td-body">
        <div :key="`${tab}-${teamKey}`" class="pane">
          <div class="pane-scroll">
            <DictManager
              v-if="tab === 'vehicles'"
              kind="vehicles"
              label="车牌号"
              ph="输入新车牌号"
              @forbidden="forbidden = true"
            />
            <DictManager
              v-else-if="tab === 'destinations'"
              kind="destinations"
              label="目的地"
              ph="输入新目的地名称"
              @forbidden="forbidden = true"
            />
            <DictManager
              v-else-if="tab === 'members'"
              kind="members"
              label="人员"
              ph="输入新成员姓名"
              @forbidden="forbidden = true"
            />
            <TowerManager v-else-if="tab === 'lines'" @forbidden="forbidden = true" />
            <OplineManager v-else @forbidden="forbidden = true" />
          </div>
        </div>
      </div>
    </template>

    <!-- 角色门控：非班管/超管整页提示（接口 40304 同口径兜底） -->
    <div v-else class="gate">
      <div class="gate-card">
        <span v-html="icon('lock-on', 44)"></span>
        <b>仅班管/超管可访问</b>
        <p>班组数据仅面向班组管理员与超级管理员开放，请联系管理员调整角色后再试。</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue';
import { currentUser } from '../api.js';
import DictManager from '../features/teamdata/components/DictManager.vue';
import OplineManager from '../features/teamdata/components/OplineManager.vue';
import TowerManager from '../features/teamdata/components/TowerManager.vue';
import { getTeamId, listTeams, setTeamId } from '../features/teamdata/api.js';
import { icon } from '../features/teamdata/helpers.js';
import '../features/teamdata/teamdata.css';

// 班组数据视图（对照 server/public/teamdata.html 还原）：车牌号 / 目的地 / 人员 / 杆塔信息 / 运维线路
// 五分段页签（商旅设备段归 features/sgccclockin，不在本视图）；数据走 features/teamdata/api.js 真实 Go API。
// 角色门控：非超管/班管整页提示，接口 40304 兜底；超管页头班组切换（localStorage.teamdata_team_id），
// 班组管理员固定本班。
const tabs = [
  { key: 'vehicles', label: '车牌号', icon: 'view-module' },
  { key: 'destinations', label: '目的地', icon: 'location' },
  { key: 'members', label: '人员', icon: 'user' },
  { key: 'lines', label: '杆塔信息', icon: 'compass' },
  { key: 'oplines', label: '运维线路', icon: 'view-list' },
];
const tab = ref('vehicles');
const forbidden = ref(false);
const teams = ref([]);
const teamId = ref(0);
const teamKey = ref(0); // 班组切换计数：驱动页签主体重挂载并重拉数据

const me = currentUser() || {};
const isAdmin = me.role === 'admin';

// onTeamChange 超管切班组：持久化 + 同步 API 层 + 整段数据重拉（组件随 :key 重挂载）。
function onTeamChange() {
  const id = Number(teamId.value) || 0;
  try {
    if (id) localStorage.setItem('teamdata_team_id', String(id));
    else localStorage.removeItem('teamdata_team_id');
  } catch (e) {
    // 忽略存储异常
  }
  setTeamId(id);
  teamKey.value += 1;
}

onMounted(async () => {
  // 角色门控：非班管/超管整页提示（渲染前判断，不发业务请求）
  if (me.role !== 'admin' && me.role !== 'team_admin') {
    forbidden.value = true;
    return;
  }
  if (!isAdmin) return; // 班管固定本班，无班组切换器
  // 超管班组切换器：数据源 GET /admin/teams，localStorage.teamdata_team_id 持久化
  try {
    teams.value = await listTeams();
  } catch (e) {
    teams.value = []; // 拉取失败保留默认班组（后端落默认），不阻断页面
    return;
  }
  let saved = 0;
  try {
    saved = Number(localStorage.getItem('teamdata_team_id')) || 0;
  } catch (e) {
    saved = 0;
  }
  const ids = teams.value.map((t) => Number(t.id));
  const init = ids.includes(saved) ? saved : getTeamId() || (ids[0] ?? 0);
  teamId.value = init;
  setTeamId(init);
});
</script>
