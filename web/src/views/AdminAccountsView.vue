<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">账号与权限管理</h1>
      <span class="page-sub">班组 · 员工 · 应用启停排序 · 按人应用授权 · 仅超级管理员</span>
    </div>

    <div class="subnav">
      <div
        v-for="t in tabs"
        :key="t.key"
        class="subnav-item"
        :class="{ active: tab === t.key }"
        @click="switchTab(t.key)"
      >
        {{ t.label }}
      </div>
    </div>

    <!-- ===== 员工管理 ===== -->
    <template v-if="tab === 'users'">
      <div class="toolbar">
        <t-select
          v-model="userTeamFilter"
          :options="teamFilterOptions"
          style="width: 160px"
          @change="loadUsers"
        />
        <t-input
          v-model="userKeyword"
          placeholder="搜索账号或昵称"
          clearable
          style="max-width: 220px"
          @enter="loadUsers"
          @clear="loadUsers"
        />
        <span class="muted num">共 {{ users.length }} 名员工</span>
        <div class="spacer"></div>
        <t-button variant="outline" @click="loadUsers">刷新</t-button>
        <t-button theme="primary" @click="openUserDialog(null)">新建员工</t-button>
      </div>

      <div class="card">
        <t-loading :loading="usersLoading" size="small">
          <div v-if="usersError" class="state-box error">
            <b>员工列表加载失败</b>
            <p>{{ usersError }}</p>
            <t-button size="small" variant="outline" style="margin-top: 12px" @click="loadUsers">
              重新加载
            </t-button>
          </div>
          <table v-else-if="users.length" class="acct-table">
            <thead>
              <tr>
                <th>账号</th>
                <th>昵称</th>
                <th>wxid</th>
                <th>班组</th>
                <th>角色</th>
                <th>状态</th>
                <th>注册时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="num">
                  <b>{{ u.username }}</b>
                </td>
                <td>{{ u.nickname || '—' }}</td>
                <td class="num">{{ u.wxid || '—' }}</td>
                <td>
                  <span v-if="u.team" class="dot-tag green"
                    ><span class="dot"></span>{{ u.team }}</span
                  >
                  <span v-else class="muted">未分配</span>
                </td>
                <td>
                  <span v-if="u.role === 'admin'" class="role-badge admin">超级管理员</span>
                  <span v-else-if="u.role === 'team_admin'" class="role-badge outline"
                    >班组管理员</span
                  >
                  <span v-else class="role-badge gray">普通用户</span>
                </td>
                <td>
                  <span v-if="u.status === 1" class="dot-tag green"
                    ><span class="dot"></span>启用</span
                  >
                  <span v-else class="dot-tag red"><span class="dot"></span>停用</span>
                </td>
                <td class="num muted">{{ u.created_at || '—' }}</td>
                <td>
                  <div class="row-actions">
                    <t-button size="small" variant="text" @click="openUserDialog(u)">编辑</t-button>
                    <t-button size="small" variant="text" @click="openPasswordDialog(u)">
                      重置密码
                    </t-button>
                    <t-button
                      size="small"
                      variant="text"
                      :theme="u.status === 1 ? 'danger' : 'default'"
                      :disabled="isMe(u)"
                      :title="isMe(u) ? '不能停用自己的账号' : ''"
                      @click="onToggleUserStatus(u)"
                    >
                      {{ u.status === 1 ? '停用' : '启用' }}
                    </t-button>
                    <t-button
                      size="small"
                      variant="text"
                      theme="danger"
                      :disabled="isMe(u)"
                      :title="isMe(u) ? '不能删除自己的账号' : ''"
                      @click="onDeleteUser(u)"
                    >
                      删除
                    </t-button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-else-if="!usersLoading" class="state-box">
            <b>暂无员工</b>
            <p>点击右上角「新建员工」创建账号；未分配班组的员工显示在筛选「未分配」里。</p>
          </div>
        </t-loading>
      </div>
    </template>

    <!-- ===== 权限管理（按人应用授权） ===== -->
    <template v-else-if="tab === 'perms'">
      <div class="perm-layout">
        <aside class="card perm-side">
          <div class="toolbar" style="padding: 0 0 10px">
            <t-input
              v-model="permKeyword"
              placeholder="搜索账号或昵称"
              clearable
              style="width: 100%"
              @enter="loadUsers"
              @clear="loadUsers"
            />
          </div>
          <t-select
            v-model="userTeamFilter"
            :options="teamFilterOptions"
            style="width: 100%; margin-bottom: 10px"
            @change="loadUsers"
          />
          <t-loading :loading="usersLoading" size="small">
            <div class="perm-user-list">
              <div
                v-for="u in permUsers"
                :key="u.id"
                class="perm-user"
                :class="{ on: selectedUser && selectedUser.id === u.id }"
                @click="selectPermUser(u)"
              >
                <b>{{ u.nickname || u.username }}</b>
                <small class="muted num">{{ u.username }}</small>
              </div>
              <div v-if="!permUsers.length" class="muted" style="padding: 12px">
                {{ usersLoading ? '加载中…' : '没有匹配的员工' }}
              </div>
            </div>
          </t-loading>
        </aside>

        <section class="card perm-main">
          <template v-if="selectedUser">
            <div class="perm-head">
              <div>
                <b>{{ selectedUser.nickname || selectedUser.username }}</b>
                <span class="muted num" style="margin-left: 8px">{{ selectedUser.username }}</span>
                <div class="muted" style="font-size: 12px; margin-top: 2px">
                  勾选该员工可使用的应用；保存后立即生效（全量替换）。
                </div>
              </div>
              <div class="spacer"></div>
              <t-button
                theme="primary"
                :loading="permSaving"
                :disabled="permLoading"
                @click="onSaveUserApps"
              >
                保存授权
              </t-button>
            </div>
            <t-loading :loading="permLoading" size="small">
              <div v-if="permError" class="state-box error">
                <b>授权读取失败</b>
                <p>{{ permError }}</p>
                <t-button
                  size="small"
                  variant="outline"
                  style="margin-top: 12px"
                  @click="loadUserApps"
                >
                  重新加载
                </t-button>
              </div>
              <div v-else class="perm-apps">
                <label v-for="a in apps" :key="a.id" class="perm-app">
                  <t-checkbox :checked="permChecked[a.id]" @change="(v) => togglePerm(a.id, v)">
                    {{ a.name }}
                    <span class="muted num" style="margin-left: 6px">{{ a.app_key }}</span>
                  </t-checkbox>
                  <span v-if="a.status !== 1" class="dot-tag orange"
                    ><span class="dot"></span>已下架</span
                  >
                </label>
                <div v-if="!apps.length" class="muted">应用目录为空，请先在「应用管理」查看。</div>
              </div>
            </t-loading>
          </template>
          <div v-else class="state-box">
            <b>选择左侧员工</b>
            <p>按人查看与设置应用授权；授权以「应用管理」里的上架应用为准。</p>
          </div>
        </section>
      </div>
    </template>

    <!-- ===== 班组管理 ===== -->
    <template v-else-if="tab === 'teams'">
      <div class="toolbar">
        <span class="muted num">共 {{ teams.length }} 个班组</span>
        <div class="spacer"></div>
        <t-button variant="outline" @click="loadTeams">刷新</t-button>
        <t-button theme="primary" @click="openTeamDialog(null)">新增班组</t-button>
      </div>

      <div class="card">
        <t-loading :loading="teamsLoading" size="small">
          <div v-if="teamsError" class="state-box error">
            <b>班组列表加载失败</b>
            <p>{{ teamsError }}</p>
            <t-button size="small" variant="outline" style="margin-top: 12px" @click="loadTeams">
              重新加载
            </t-button>
          </div>
          <table v-else-if="teams.length" class="acct-table">
            <thead>
              <tr>
                <th>班组名称</th>
                <th>班组群 wxid</th>
                <th>排序</th>
                <th>成员数</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="t in teams" :key="t.id">
                <td>
                  <b>{{ t.name }}</b>
                </td>
                <td class="num">{{ t.wxid || '—' }}</td>
                <td class="num">{{ t.sort }}</td>
                <td class="num">{{ t.user_count }}</td>
                <td>
                  <span v-if="t.status === 1" class="dot-tag green"
                    ><span class="dot"></span>启用</span
                  >
                  <span v-else class="dot-tag red"><span class="dot"></span>停用</span>
                </td>
                <td>
                  <div class="row-actions">
                    <t-button size="small" variant="text" @click="openTeamDialog(t)">编辑</t-button>
                    <t-button size="small" variant="text" theme="danger" @click="onDeleteTeam(t)">
                      删除
                    </t-button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-else-if="!teamsLoading" class="state-box">
            <b>暂无班组</b>
            <p>点击右上角「新增班组」创建；班组下有员工或业务数据时只能停用、不能删除。</p>
          </div>
        </t-loading>
      </div>
    </template>

    <!-- ===== 应用管理（启停/排序） ===== -->
    <template v-else-if="tab === 'apps'">
      <div class="toolbar">
        <span class="muted num">共 {{ apps.length }} 个应用</span>
        <div class="spacer"></div>
        <t-button variant="outline" @click="loadApps">刷新</t-button>
      </div>

      <div class="card">
        <t-loading :loading="appsLoading" size="small">
          <div v-if="appsError" class="state-box error">
            <b>应用列表加载失败</b>
            <p>{{ appsError }}</p>
            <t-button size="small" variant="outline" style="margin-top: 12px" @click="loadApps">
              重新加载
            </t-button>
          </div>
          <table v-else-if="apps.length" class="acct-table">
            <thead>
              <tr>
                <th>应用</th>
                <th>标识</th>
                <th>终端</th>
                <th>排序</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in apps" :key="a.id">
                <td>
                  <b>{{ a.name }}</b>
                </td>
                <td class="num">{{ a.app_key }}</td>
                <td>{{ terminalName(a.terminal) }}</td>
                <td>
                  <t-input-number
                    v-model="appDraft[a.id].sort"
                    theme="column"
                    :min="0"
                    :step="1"
                    style="width: 110px"
                  />
                </td>
                <td>
                  <t-switch v-model="appDraft[a.id].status" :custom-value="[1, 0]" />
                </td>
                <td>
                  <div class="row-actions">
                    <t-button
                      size="small"
                      variant="text"
                      theme="primary"
                      :loading="appSavingId === a.id"
                      @click="onSaveApp(a)"
                    >
                      保存
                    </t-button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-else-if="!appsLoading" class="state-box">
            <b>应用目录为空</b>
            <p>应用由各业务模块建表种子登记；停用后所有端不再展示该应用入口。</p>
          </div>
        </t-loading>
      </div>
    </template>

    <!-- 新建/编辑员工 -->
    <t-dialog
      v-model:visible="userDialogVisible"
      :header="userForm.id ? '编辑员工' : '新建员工'"
      :confirm-btn="{
        content: userSaving ? '保存中…' : userForm.id ? '保存修改' : '创建账号',
        theme: 'primary',
        loading: userSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onSaveUser"
      @close="userDialogVisible = false"
    >
      <t-form layout="vertical">
        <t-form-item v-if="!userForm.id" label="登录账号" required>
          <t-input v-model="userForm.username" placeholder="2-64 位字母、数字或下划线" />
        </t-form-item>
        <t-form-item label="昵称 / 姓名">
          <t-input v-model="userForm.nickname" placeholder="选填" />
        </t-form-item>
        <t-form-item label="微信 wxid">
          <t-input v-model="userForm.wxid" placeholder="选填，消息微信推送用" />
        </t-form-item>
        <t-form-item label="所属班组">
          <t-select
            v-model="userForm.teamId"
            :options="teamOptions"
            placeholder="未分配"
            clearable
          />
          <div class="field-help">班组管理员必须有所属班组。</div>
        </t-form-item>
        <t-form-item label="角色">
          <t-select v-model="userForm.role" :options="roleOptions" :disabled="userForm.isSelf" />
          <div v-if="userForm.isSelf" class="field-help">不能降级自己的账号。</div>
        </t-form-item>
        <t-form-item v-if="!userForm.id" label="初始密码" required>
          <t-input v-model="userForm.password" type="password" placeholder="至少 6 位" />
        </t-form-item>
      </t-form>
      <div v-if="userFormError" class="field-error">{{ userFormError }}</div>
    </t-dialog>

    <!-- 重置密码 -->
    <t-dialog
      v-model:visible="passwordDialogVisible"
      :header="`重置密码 · ${passwordTarget ? passwordTarget.username : ''}`"
      :confirm-btn="{
        content: passwordSaving ? '提交中…' : '重置密码',
        theme: 'primary',
        loading: passwordSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onResetPassword"
      @close="passwordDialogVisible = false"
    >
      <t-form layout="vertical">
        <t-form-item label="新密码" required>
          <t-input v-model="passwordValue" type="password" placeholder="至少 6 位" />
        </t-form-item>
      </t-form>
      <div v-if="passwordError" class="field-error">{{ passwordError }}</div>
    </t-dialog>

    <!-- 新增/编辑班组 -->
    <t-dialog
      v-model:visible="teamDialogVisible"
      :header="teamForm.id ? '编辑班组' : '新增班组'"
      :confirm-btn="{
        content: teamSaving ? '保存中…' : teamForm.id ? '保存修改' : '创建班组',
        theme: 'primary',
        loading: teamSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onSaveTeam"
      @close="teamDialogVisible = false"
    >
      <t-form layout="vertical">
        <t-form-item label="班组名称" required>
          <t-input v-model="teamForm.name" placeholder="例如：检修一班" />
        </t-form-item>
        <t-form-item label="班组群 wxid">
          <t-input v-model="teamForm.wxid" placeholder="选填，班组群消息推送用" />
        </t-form-item>
        <t-form-item v-if="teamForm.id" label="排序">
          <t-input-number v-model="teamForm.sort" theme="column" :min="0" :step="1" />
          <div class="field-help">数值小的排在前面；首个启用班组即默认班组。</div>
        </t-form-item>
        <t-form-item label="状态">
          <t-switch v-model="teamForm.status" :custom-value="[1, 0]" />
          <div class="field-help">停用后不再出现在班组选择里，历史数据保留。</div>
        </t-form-item>
      </t-form>
      <div v-if="teamFormError" class="field-error">{{ teamFormError }}</div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import {
  createTeam,
  createUser,
  currentUser,
  deleteTeam,
  deleteUser,
  getUserApps,
  listApps,
  listTeams,
  listUsers,
  setUserApps,
  updateApp,
  updateTeam,
  updateUser,
} from '../features/accounts/api.js';

// 页签（消息推送在「通知中心/推送」入口维护，此处只管账号与授权）
const tabs = [
  { key: 'users', label: '员工管理' },
  { key: 'perms', label: '权限管理' },
  { key: 'teams', label: '班组管理' },
  { key: 'apps', label: '应用管理' },
];
const tab = ref('users');

const roleOptions = [
  { label: '普通用户', value: 'user' },
  { label: '班组管理员', value: 'team_admin' },
  { label: '超级管理员', value: 'admin' },
];
const terminalName = (t) => ({ both: '双端', mobile: '仅小程序', pc: '仅网页端' })[t] || t || '—';

// 当前登录用户（自我保护标识；与服务端口径一致：不能停用/降级/删除自己）
const me = currentUser() || {};
const isMe = (u) => !!me.id && u.id === me.id;

// ---- 班组数据（筛选/弹层共用）----
const teams = ref([]);
const teamsLoading = ref(false);
const teamsError = ref('');

async function loadTeams() {
  teamsLoading.value = true;
  teamsError.value = '';
  try {
    teams.value = await listTeams();
  } catch (e) {
    teamsError.value = e.message || '班组列表加载失败';
  } finally {
    teamsLoading.value = false;
  }
}

const teamOptions = computed(() =>
  (teams.value || [])
    .filter((t) => t.status === 1)
    .map((t) => ({ label: `${t.name}${t.status === 1 ? '' : '（已停用）'}`, value: t.id }))
);
const teamFilterOptions = computed(() => [
  { label: '全部班组', value: '' },
  { label: '未分配', value: 0 },
  ...(teams.value || []).map((t) => ({
    label: t.status === 1 ? t.name : `${t.name}（已停用）`,
    value: t.id,
  })),
]);

// ---- 员工管理 ----
const users = ref([]);
const usersLoading = ref(false);
const usersError = ref('');
const userKeyword = ref('');
const userTeamFilter = ref('');

async function loadUsers() {
  usersLoading.value = true;
  usersError.value = '';
  try {
    users.value = await listUsers({ keyword: userKeyword.value, teamId: userTeamFilter.value });
  } catch (e) {
    usersError.value = e.message || '员工列表加载失败';
  } finally {
    usersLoading.value = false;
  }
}

// ---- 员工弹层 ----
const userDialogVisible = ref(false);
const userSaving = ref(false);
const userFormError = ref('');
const userForm = reactive({
  id: null,
  username: '',
  nickname: '',
  wxid: '',
  teamId: null,
  role: 'user',
  password: '',
  isSelf: false,
});

function openUserDialog(u) {
  userFormError.value = '';
  if (u) {
    userForm.id = u.id;
    userForm.username = u.username;
    userForm.nickname = u.nickname || '';
    userForm.wxid = u.wxid || '';
    userForm.teamId = u.team_id == null ? null : u.team_id;
    userForm.role = u.role || 'user';
    userForm.password = '';
    userForm.isSelf = isMe(u);
  } else {
    userForm.id = null;
    userForm.username = '';
    userForm.nickname = '';
    userForm.wxid = '';
    userForm.teamId = null;
    userForm.role = 'user';
    userForm.password = '';
    userForm.isSelf = false;
  }
  userDialogVisible.value = true;
}

async function onSaveUser() {
  userFormError.value = '';
  userSaving.value = true;
  try {
    if (userForm.id) {
      const patch = {
        nickname: userForm.nickname,
        wxid: userForm.wxid,
        team_id: userForm.teamId == null ? null : Number(userForm.teamId),
      };
      if (!userForm.isSelf) patch.role = userForm.role;
      await updateUser(userForm.id, patch);
      MessagePlugin.success('已保存');
    } else {
      await createUser({
        username: userForm.username,
        password: userForm.password,
        nickname: userForm.nickname,
        wxid: userForm.wxid,
        teamId: userForm.teamId == null ? null : Number(userForm.teamId),
        role: userForm.role,
      });
      MessagePlugin.success('已创建');
    }
    userDialogVisible.value = false;
    await loadUsers();
  } catch (e) {
    // 服务端原文如实展示（账号已存在 / 班组管理员必须有所属班组等），不伪成功
    userFormError.value = e.message || '保存失败';
  } finally {
    userSaving.value = false;
  }
}

// ---- 重置密码 ----
const passwordDialogVisible = ref(false);
const passwordSaving = ref(false);
const passwordError = ref('');
const passwordTarget = ref(null);
const passwordValue = ref('');

function openPasswordDialog(u) {
  passwordTarget.value = u;
  passwordValue.value = '';
  passwordError.value = '';
  passwordDialogVisible.value = true;
}

async function onResetPassword() {
  passwordError.value = '';
  passwordSaving.value = true;
  try {
    await updateUser(passwordTarget.value.id, { password: passwordValue.value });
    MessagePlugin.success('已重置密码');
    passwordDialogVisible.value = false;
  } catch (e) {
    passwordError.value = e.message || '重置失败';
  } finally {
    passwordSaving.value = false;
  }
}

// ---- 启停 / 删除员工 ----
function onToggleUserStatus(u) {
  const disabling = u.status === 1;
  const confirm = DialogPlugin.confirm({
    header: disabling ? '停用账号' : '启用账号',
    body: disabling
      ? `确定停用账号「${u.username}」吗？停用后该账号将无法登录。`
      : `确定启用账号「${u.username}」吗？`,
    confirmBtn: { content: disabling ? '停用' : '启用', theme: disabling ? 'danger' : 'primary' },
    async onConfirm() {
      try {
        await updateUser(u.id, { status: disabling ? 0 : 1 });
        MessagePlugin.success(disabling ? '已停用' : '已启用');
        await loadUsers();
      } catch (e) {
        MessagePlugin.error(e.message || '操作失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

function onDeleteUser(u) {
  const confirm = DialogPlugin.confirm({
    header: '删除员工',
    body: `确定删除员工「${u.username}」吗？删除后不可恢复；不能删除自己或最后一个超级管理员。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    async onConfirm() {
      try {
        await deleteUser(u.id);
        MessagePlugin.success('已删除');
        await loadUsers();
      } catch (e) {
        MessagePlugin.error(e.message || '删除失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 权限管理（按人应用授权）----
const permKeyword = ref('');
const selectedUser = ref(null);
const permLoading = ref(false);
const permSaving = ref(false);
const permError = ref('');
const permChecked = reactive({});

const permUsers = computed(() => {
  const kw = permKeyword.value.trim().toLowerCase();
  return (users.value || []).filter((u) => {
    if (userTeamFilter.value !== '' && userTeamFilter.value !== null) {
      const want = Number(userTeamFilter.value);
      if (want === 0 ? u.team_id != null : Number(u.team_id) !== want) return false;
    }
    if (!kw) return true;
    return (
      (u.username || '').toLowerCase().includes(kw) || (u.nickname || '').toLowerCase().includes(kw)
    );
  });
});

function selectPermUser(u) {
  selectedUser.value = u;
  loadUserApps();
}

async function loadUserApps() {
  if (!selectedUser.value) return;
  permLoading.value = true;
  permError.value = '';
  try {
    const ids = await getUserApps(selectedUser.value.id);
    Object.keys(permChecked).forEach((k) => delete permChecked[k]);
    (ids || []).forEach((id) => {
      permChecked[id] = true;
    });
  } catch (e) {
    permError.value = e.message || '授权读取失败';
  } finally {
    permLoading.value = false;
  }
}

function togglePerm(appId, checked) {
  permChecked[appId] = !!checked;
}

async function onSaveUserApps() {
  if (!selectedUser.value) return;
  permSaving.value = true;
  try {
    const ids = Object.keys(permChecked)
      .filter((k) => permChecked[k])
      .map(Number);
    const saved = await setUserApps(selectedUser.value.id, ids);
    MessagePlugin.success(`已保存 ${saved.length} 项授权`);
  } catch (e) {
    MessagePlugin.error(e.message || '授权保存失败');
  } finally {
    permSaving.value = false;
  }
}

// ---- 班组弹层 ----
const teamDialogVisible = ref(false);
const teamSaving = ref(false);
const teamFormError = ref('');
const teamForm = reactive({ id: null, name: '', wxid: '', sort: 0, status: 1 });

function openTeamDialog(t) {
  teamFormError.value = '';
  if (t) {
    teamForm.id = t.id;
    teamForm.name = t.name;
    teamForm.wxid = t.wxid || '';
    teamForm.sort = t.sort || 0;
    teamForm.status = t.status;
  } else {
    teamForm.id = null;
    teamForm.name = '';
    teamForm.wxid = '';
    teamForm.sort = 0;
    teamForm.status = 1;
  }
  teamDialogVisible.value = true;
}

async function onSaveTeam() {
  teamFormError.value = '';
  teamSaving.value = true;
  try {
    if (teamForm.id) {
      await updateTeam(teamForm.id, {
        name: teamForm.name,
        wxid: teamForm.wxid,
        sort: Number(teamForm.sort) || 0,
        status: teamForm.status,
      });
      MessagePlugin.success('已保存');
    } else {
      await createTeam({ name: teamForm.name, wxid: teamForm.wxid });
      MessagePlugin.success('已创建');
    }
    teamDialogVisible.value = false;
    await Promise.all([loadTeams(), loadUsers()]);
  } catch (e) {
    teamFormError.value = e.message || '保存失败';
  } finally {
    teamSaving.value = false;
  }
}

function onDeleteTeam(t) {
  const confirm = DialogPlugin.confirm({
    header: '删除班组',
    body: `确定删除班组「${t.name}」吗？删除后不可恢复；班组下仍有员工或业务数据时无法删除，可改为停用。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    async onConfirm() {
      try {
        await deleteTeam(t.id);
        MessagePlugin.success('已删除');
        await Promise.all([loadTeams(), loadUsers()]);
      } catch (e) {
        // 40903 等服务端拒绝原文（含引用数量）如实展示
        MessagePlugin.error(e.message || '删除失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 应用管理（启停/排序）----
const apps = ref([]);
const appsLoading = ref(false);
const appsError = ref('');
const appSavingId = ref(null);
const appDraft = reactive({});

async function loadApps() {
  appsLoading.value = true;
  appsError.value = '';
  try {
    apps.value = await listApps();
    Object.keys(appDraft).forEach((k) => delete appDraft[k]);
    (apps.value || []).forEach((a) => {
      appDraft[a.id] = { sort: a.sort, status: a.status };
    });
  } catch (e) {
    appsError.value = e.message || '应用列表加载失败';
  } finally {
    appsLoading.value = false;
  }
}

async function onSaveApp(a) {
  const draft = appDraft[a.id] || {};
  appSavingId.value = a.id;
  try {
    await updateApp(a.id, { status: Number(draft.status) ? 1 : 0, sort: Number(draft.sort) || 0 });
    MessagePlugin.success('已保存');
    await loadApps();
  } catch (e) {
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    appSavingId.value = null;
  }
}

// ---- 页签切换（数据按需加载）----
async function switchTab(key) {
  tab.value = key;
  if (key === 'perms' && !selectedUser.value && users.value.length) {
    // 切到权限页时保持未选中，等用户点选
  }
}

onMounted(async () => {
  await loadTeams();
  await loadUsers();
  await loadApps();
});
</script>

<style scoped>
.role-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  white-space: nowrap;
}
.role-badge.admin {
  background: #0e3da8;
  color: #fff;
}
.role-badge.outline {
  border: 1px solid #0e3da8;
  color: #0e3da8;
}
.role-badge.gray {
  background: #f2f3f5;
  color: #4e5969;
}

.acct-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.acct-table th {
  padding: 8px 10px;
  text-align: left;
  color: #86909c;
  font-weight: 500;
  border-bottom: 1px solid #e5e6eb;
  white-space: nowrap;
}
.acct-table td {
  padding: 10px;
  border-bottom: 1px solid #f2f3f5;
  vertical-align: middle;
}
.acct-table tr:last-child td {
  border-bottom: none;
}
.row-actions {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
}

.perm-layout {
  display: grid;
  grid-template-columns: 280px 1fr;
  gap: 14px;
  align-items: start;
}
.perm-user-list {
  max-height: 560px;
  overflow: auto;
}
.perm-user {
  padding: 9px 10px;
  border-radius: 8px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.perm-user:hover {
  background: #f2f3f5;
}
.perm-user.on {
  background: #e8f0ff;
}
.perm-head {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 12px;
}
.perm-apps {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 8px;
}
.perm-app {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid #e5e6eb;
  border-radius: 8px;
}

.field-help {
  margin-top: 4px;
  font-size: 12px;
  color: #86909c;
  line-height: 1.6;
}
.field-error {
  margin-top: 10px;
  padding: 8px 10px;
  background: #ffece8;
  color: #cb2634;
  font-size: 12.5px;
  line-height: 1.7;
}
</style>
