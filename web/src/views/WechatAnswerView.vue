<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">微信智能应答</h1>
      <span class="page-sub">按监听群聊组织规则，统一查看接入状态与配置完整性</span>
    </div>

    <!-- 接入状态：真实读取服务端状态，支持手动重连 -->
    <div class="card status-card" :class="{ warn: status && !status.configured }">
      <span class="status-icon" :class="{ warn: status && !status.configured }">
        <t-icon v-if="statusLoading" name="loading" />
        <t-icon v-else-if="statusError" name="error-circle" />
        <t-icon v-else-if="status && status.configured" name="check-circle-filled" />
        <t-icon v-else name="error-circle" />
      </span>
      <div class="status-main">
        <b>{{ statusTitle }}</b>
        <p class="status-desc">{{ statusDesc }}</p>
      </div>
      <div v-if="status && status.last_ready_at" class="status-meta num">
        最近就绪<br /><b>{{ formatTime(status.last_ready_at) }}</b>
      </div>
      <div class="status-actions">
        <t-button size="small" variant="outline" :loading="reconnecting" @click="onReconnect">
          手动重连
        </t-button>
      </div>
    </div>

    <div class="toolbar">
      <t-input
        v-model="keyword"
        placeholder="搜索群备注或群 ID"
        clearable
        style="max-width: 280px"
      />
      <span class="muted num">共 {{ filteredGroups.length }} 个监听群聊</span>
      <div class="spacer"></div>
      <t-button theme="primary" @click="openGroupDialog(null)">新增监听群聊</t-button>
    </div>

    <!-- 群聊列表 -->
    <div class="card">
      <t-loading :loading="groupsLoading" size="small">
        <div v-if="groupsError" class="state-box error">
          <b>群聊列表加载失败</b>
          <p>{{ groupsError }}</p>
          <t-button size="small" variant="outline" style="margin-top: 12px" @click="loadGroups">
            重新加载
          </t-button>
        </div>
        <table v-else-if="filteredGroups.length" class="group-table">
          <thead>
            <tr>
              <th>监听群聊</th>
              <th>归属班组</th>
              <th>监听规则</th>
              <th>最近消息</th>
              <th>管理操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="group in filteredGroups" :key="group.id">
              <td>
                <div class="group-name">
                  <b>{{ group.name }}</b>
                  <small class="num">{{ group.group_id }}</small>
                </div>
              </td>
              <td>
                <span v-if="group.team_name || group.team_id" class="dot-tag green">
                  <span class="dot"></span>{{ group.team_name || `班组 ${group.team_id}` }}
                </span>
                <span v-else class="dot-tag orange"><span class="dot"></span>待指定</span>
              </td>
              <td>
                <div class="rule-tags">
                  <span
                    v-for="rule in group.rules || []"
                    :key="rule.id"
                    class="tag"
                    :class="{ orange: !rule.complete }"
                  >
                    {{ ruleTypeName(rule.type) }}
                  </span>
                  <span v-if="!(group.rules || []).length" class="tag gray">尚未挂接规则</span>
                </div>
                <small class="muted num">
                  {{ enabledRuleCount(group) }} 条启用 · {{ (group.rules || []).length }} 条已挂接
                </small>
              </td>
              <td class="num">{{ formatTime(group.last_message_at) || '暂无消息' }}</td>
              <td>
                <div class="row-actions">
                  <t-button size="small" variant="text" @click="openRules(group)"
                    >管理规则</t-button
                  >
                  <t-button size="small" variant="text" @click="openGroupDialog(group)">
                    编辑
                  </t-button>
                  <t-button
                    size="small"
                    variant="text"
                    theme="danger"
                    @click="onDeleteGroup(group)"
                  >
                    删除
                  </t-button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!groupsLoading" class="state-box">
          <b>{{ groups.length ? '没有匹配的群聊' : '还没有监听群聊' }}</b>
          <p>
            {{
              groups.length
                ? '请调整搜索条件后重试。'
                : '先新增群聊，再为群聊加挂位置消息或输电班组运维规则。'
            }}
          </p>
        </div>
      </t-loading>
    </div>
    <div class="source-note">
      线路由「班组数据 → 运维线路」按班组统一提供；发送者名称优先使用员工 wxid 对应的壹匣昵称。
      Token 与 WebSocket 等接入配置在「管理-应用配置 · 微信智能应答」维护，此处只管理群与规则。
    </div>

    <!-- 新增/编辑群聊 -->
    <t-dialog
      v-model:visible="groupDialogVisible"
      :header="groupForm.id ? '编辑监听群聊' : '新增监听群聊'"
      :confirm-btn="{
        content: groupSaving ? '保存中…' : '保存群聊',
        theme: 'primary',
        loading: groupSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onSaveGroup"
      @close="groupDialogVisible = false"
    >
      <t-form layout="vertical">
        <t-form-item label="群备注" required>
          <t-input v-model="groupForm.name" placeholder="例如：检修一班运维群" />
        </t-form-item>
        <t-form-item label="群 ID" required>
          <t-input
            v-model="groupForm.groupId"
            :disabled="!!groupForm.id"
            placeholder="例如：demo-team@chatroom"
          />
          <div class="field-help">
            须以 @chatroom 结尾{{ groupForm.id ? '；创建后不可修改' : '' }}
          </div>
        </t-form-item>
        <t-form-item label="归属班组">
          <t-select
            v-model="groupForm.teamId"
            :options="teamOptions"
            placeholder="暂不指定"
            clearable
          />
          <div class="field-help">加挂输电班组运维规则前必须选择；线路匹配仅限本班。</div>
        </t-form-item>
      </t-form>
      <div v-if="groupFormError" class="field-error">{{ groupFormError }}</div>
    </t-dialog>

    <!-- 群规则抽屉 -->
    <t-drawer
      v-model:visible="rulesVisible"
      :header="`群规则管理 · ${currentGroup ? currentGroup.name : ''}`"
      :size="560"
      :footer="false"
      @close="rulesVisible = false"
    >
      <div class="group-summary">
        <div>
          <b>{{ currentGroup ? currentGroup.name : '' }}</b>
          <p class="muted">
            {{ currentGroup ? currentGroup.group_id : '' }} · 规则修改仅作用于本群
          </p>
        </div>
        <span
          class="dot-tag"
          :class="
            currentGroup && (currentGroup.team_name || currentGroup.team_id) ? 'green' : 'orange'
          "
        >
          <span class="dot"></span>
          {{
            currentGroup && (currentGroup.team_name || currentGroup.team_id)
              ? '班组已关联'
              : '输电规则待配班组'
          }}
        </span>
      </div>

      <div class="toolbar" style="margin: 12px 0">
        <h2 class="card-title" style="margin: 0">已挂规则</h2>
        <span class="muted">每群每类最多一条</span>
        <div class="spacer"></div>
        <t-button
          size="small"
          theme="primary"
          :disabled="rules.length >= 2"
          @click="openRuleEditor(null)"
        >
          {{ rules.length >= 2 ? '两类规则均已配置' : '添加规则' }}
        </t-button>
      </div>

      <t-loading :loading="rulesLoading" size="small">
        <div v-if="rulesError" class="state-box error">
          <b>规则加载失败</b>
          <p>{{ rulesError }}</p>
          <t-button size="small" variant="outline" style="margin-top: 12px" @click="loadRules">
            重新加载
          </t-button>
        </div>
        <template v-else>
          <div v-for="rule in rules" :key="rule.id" class="rule-card">
            <div class="rule-top">
              <div>
                <b>{{ rule.name }}</b>
                <p class="muted">{{ ruleTypeName(rule.type) }} · 独立规则实例</p>
              </div>
              <t-switch
                :model-value="!!rule.enabled"
                size="small"
                @change="(v) => onToggleRule(rule, v)"
              />
            </div>
            <div class="rule-meta">
              <span class="dot-tag" :class="rule.enabled ? 'green' : ''">
                <span class="dot"></span>{{ rule.enabled ? '运行中' : '已停用' }}
              </span>
              <span class="dot-tag" :class="rule.complete ? 'blue' : 'orange'">
                <span class="dot"></span>{{ rule.complete ? '工作流配置完整' : '有 Key 未配置' }}
              </span>
            </div>
            <div class="rule-actions">
              <t-button size="small" variant="text" @click="openRuleEditor(rule)"
                >编辑规则</t-button
              >
              <t-button size="small" variant="text" theme="danger" @click="onDeleteRule(rule)">
                删除
              </t-button>
            </div>
          </div>
          <div v-if="!rules.length" class="state-box">
            <b>尚未挂接监听规则</b>
            <p>可添加位置消息或输电班组运维；每类最多一条。</p>
          </div>
        </template>
      </t-loading>
    </t-drawer>

    <!-- 规则编辑抽屉 -->
    <t-drawer
      v-model:visible="editorVisible"
      :header="ruleForm.id ? '编辑规则' : '添加规则'"
      :size="560"
      :footer="false"
      @close="editorVisible = false"
    >
      <p class="editor-intro">
        规则仅作用于当前群聊。Dify 地址、user 与超时使用全平台共用配置；这里只维护本规则的独立工作流
        Key。敏感 Key 不回显完整值，留空表示保持不变。
      </p>

      <div class="form-section">
        <h4 class="card-title">基本信息</h4>
        <p class="hint">同群同类型规则唯一；停用后不再处理新消息，既有正式记录不受影响。</p>
        <t-form layout="vertical">
          <t-form-item label="规则名称" required>
            <t-input v-model="ruleForm.name" placeholder="如：输电班组运维" />
          </t-form-item>
          <t-form-item label="规则类型" required>
            <t-select v-model="ruleForm.type" :options="typeOptions" :disabled="!!ruleForm.id" />
          </t-form-item>
          <t-form-item label="启用状态">
            <div class="switch-line">
              <t-switch v-model="ruleForm.enabled" />
              <span class="muted">{{ ruleForm.enabled ? '启用此规则' : '停用此规则' }}</span>
            </div>
          </t-form-item>
        </t-form>
      </div>

      <!-- 输电班组运维：树障 / 交跨 / 提问与归档，Dify Key 分组维护 -->
      <template v-if="ruleForm.type === 'treeWatch'">
        <div class="form-section">
          <h4 class="card-title">树障提取与更正</h4>
          <p class="hint">WF1 负责新报送提取，WF2 负责引用记录后的字段更正。</p>
          <t-form layout="vertical">
            <t-form-item label="WF1 · 树障提取 Key" required>
              <t-input
                v-model="ruleForm.keys.tree_extract"
                type="password"
                :placeholder="keyPlaceholder('tree_extract')"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.tree_extract }">
                {{ keysConfigured.tree_extract ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
            <t-form-item label="WF2 · 树障更正 Key" required>
              <t-input
                v-model="ruleForm.keys.tree_correct"
                type="password"
                :placeholder="keyPlaceholder('tree_correct')"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.tree_correct }">
                {{ keysConfigured.tree_correct ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
          </t-form>
        </div>

        <div class="form-section">
          <h4 class="card-title">交跨排查 <span class="tag gray">可选能力</span></h4>
          <p class="hint">两项留空则本群不启用交跨；只填写提取 Key 时可捕获，但不支持引用更正。</p>
          <t-form layout="vertical">
            <t-form-item label="WF1c · 交跨提取 Key">
              <t-input
                v-model="ruleForm.keys.crossing_extract"
                type="password"
                :placeholder="keyPlaceholder('crossing_extract')"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.crossing_extract }">
                {{ keysConfigured.crossing_extract ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
            <t-form-item label="WF2c · 交跨更正 Key">
              <t-input
                v-model="ruleForm.keys.crossing_correct"
                type="password"
                :placeholder="keyPlaceholder('crossing_correct')"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.crossing_correct }">
                {{ keysConfigured.crossing_correct ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
          </t-form>
        </div>

        <div class="form-section">
          <h4 class="card-title">群内提问与聊天归档</h4>
          <p class="hint">每日未消费聊天总结和文档归档保留；提问与 Call Me 共用。</p>
          <t-form layout="vertical">
            <t-form-item label="群内提问">
              <div class="switch-line">
                <t-switch v-model="ruleForm.askEnabled" />
                <span class="muted">
                  {{ ruleForm.askEnabled ? '启用群内提问（引用消息提问）' : '不启用群内提问' }}
                </span>
              </div>
            </t-form-item>
            <t-form-item label="WF3 · 回答文件命名 Key" :required="ruleForm.askEnabled">
              <t-input
                v-model="ruleForm.keys.ask_name"
                type="password"
                :placeholder="keyPlaceholder('ask_name')"
                :disabled="!ruleForm.askEnabled"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.ask_name }">
                {{ keysConfigured.ask_name ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
            <t-form-item label="WF4 · 聊天总结 Key">
              <t-input
                v-model="ruleForm.keys.chat_digest"
                type="password"
                :placeholder="keyPlaceholder('chat_digest')"
              />
              <span class="secret-state" :class="{ empty: !keysConfigured.chat_digest }">
                {{ keysConfigured.chat_digest ? '已配置' : '未配置' }}
              </span>
            </t-form-item>
          </t-form>
        </div>
      </template>

      <!-- 位置消息 -->
      <div v-else class="form-section">
        <h4 class="card-title">位置消息工作流</h4>
        <p class="hint">收到群成员位置消息后，提取经纬度和发送者并调用此工作流。</p>
        <t-form layout="vertical">
          <t-form-item label="位置处理 Dify Workflow Key" required>
            <t-input
              v-model="ruleForm.keys.location"
              type="password"
              :placeholder="keyPlaceholder('location')"
            />
            <span class="secret-state" :class="{ empty: !keysConfigured.location }">
              {{ keysConfigured.location ? '已配置' : '未配置' }}
            </span>
          </t-form-item>
        </t-form>
      </div>

      <div v-if="ruleFormError" class="field-error">{{ ruleFormError }}</div>
      <div class="drawer-foot">
        <t-button variant="outline" @click="editorVisible = false">取消</t-button>
        <t-button theme="primary" :loading="ruleSaving" @click="onSaveRule">保存规则</t-button>
      </div>
    </t-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import {
  createWechatGroup,
  createWechatRule,
  deleteWechatGroup,
  deleteWechatRule,
  getWechatAnswerStatus,
  listTeams,
  listWechatGroups,
  listWechatRules,
  reconnectWechatAnswer,
  updateWechatGroup,
  updateWechatRule,
} from '../features/admin/api.js';

// 规则类型展示名（内部键 treeWatch/location 与服务端保持一致）
function ruleTypeName(type) {
  return { treeWatch: '输电班组运维', location: '位置消息' }[type] || type || '未知类型';
}

// formatTime 把 RFC3339 时间格式化为可读本地样式；空值返回空串。
function formatTime(value) {
  if (!value) return '';
  const text = String(value);
  return text
    .replace('T', ' ')
    .replace(/\.\d+.*$/, '')
    .replace(/Z$/, '');
}

// ---- 接入状态 ----
const status = ref(null);
const statusLoading = ref(false);
const statusError = ref('');
const reconnecting = ref(false);

const statusTitle = computed(() => {
  if (statusLoading.value && !status.value) return '正在读取接入状态…';
  if (statusError.value) return '接入状态读取失败';
  if (!status.value) return '接入状态未知';
  return status.value.configured ? 'WeChatPadPro 实时通道正常' : '尚未配置 WeChatPadPro 接入';
});
const statusDesc = computed(() => {
  if (statusError.value) return statusError.value;
  if (!status.value) return '请先登录超级管理员后查看。';
  const s = status.value;
  if (!s.configured)
    return '监听规则不会执行，请先在「管理-应用配置 · 微信智能应答」填写 Token 与连接地址';
  const parts = [
    s.token_configured ? 'Access Token 已配置' : 'Access Token 未配置',
    s.ws_connected ? 'WebSocket 已连接' : 'WebSocket 未连接',
    s.auto_reconnect ? '自动重连已启用' : '自动重连未启用',
  ];
  return parts.join(' · ');
});

async function loadStatus() {
  statusLoading.value = true;
  statusError.value = '';
  try {
    status.value = await getWechatAnswerStatus();
  } catch (err) {
    status.value = null;
    statusError.value = err.message || '接入状态读取失败';
  } finally {
    statusLoading.value = false;
  }
}

// 手动重连：真实调用重建连接接口，失败如实提示
async function onReconnect() {
  reconnecting.value = true;
  try {
    status.value = await reconnectWechatAnswer();
    MessagePlugin.success('已发起重连，实时通道状态已刷新');
    loadStatus();
  } catch (err) {
    MessagePlugin.error(err.message || '手动重连失败');
  } finally {
    reconnecting.value = false;
  }
}

// ---- 群聊列表 ----
const groups = ref([]);
const groupsLoading = ref(false);
const groupsError = ref('');
const keyword = ref('');

const filteredGroups = computed(() => {
  const kw = keyword.value.trim().toLowerCase();
  if (!kw) return groups.value;
  return groups.value.filter((g) =>
    `${g.name || ''}${g.group_id || ''}`.toLowerCase().includes(kw)
  );
});

function enabledRuleCount(group) {
  return (group.rules || []).filter((r) => r.enabled).length;
}

async function loadGroups() {
  groupsLoading.value = true;
  groupsError.value = '';
  try {
    groups.value = await listWechatGroups();
  } catch (err) {
    groups.value = [];
    groupsError.value = err.message || '群聊列表加载失败';
  } finally {
    groupsLoading.value = false;
  }
}

// ---- 归属班组选项 ----
const teams = ref([]);
const teamOptions = computed(() => [
  { label: '暂不指定', value: 0 },
  ...teams.value.map((t) => ({ label: t.name, value: t.id })),
]);

async function loadTeams() {
  try {
    teams.value = await listTeams();
  } catch (err) {
    // 班组字典加载失败不阻断群管理，仅提示
    MessagePlugin.warning(err.message || '班组列表加载失败，归属班组暂不可选');
  }
}

// ---- 群聊新增/编辑 ----
const groupDialogVisible = ref(false);
const groupSaving = ref(false);
const groupFormError = ref('');
const groupForm = reactive({ id: '', name: '', groupId: '', teamId: 0 });

function openGroupDialog(group) {
  groupForm.id = group ? String(group.id) : '';
  groupForm.name = group ? group.name : '';
  groupForm.groupId = group ? group.group_id : '';
  groupForm.teamId = group ? group.team_id || 0 : 0;
  groupFormError.value = '';
  groupDialogVisible.value = true;
}

async function onSaveGroup() {
  const name = groupForm.name.trim();
  const groupId = groupForm.groupId.trim();
  if (!name) {
    groupFormError.value = '请填写群备注';
    return;
  }
  if (!/^\S+@chatroom$/.test(groupId)) {
    groupFormError.value = '群 ID 须以 @chatroom 结尾，且不能包含空白字符';
    return;
  }
  groupFormError.value = '';
  groupSaving.value = true;
  try {
    if (groupForm.id) {
      await updateWechatGroup(groupForm.id, { name, teamId: groupForm.teamId || 0 });
      MessagePlugin.success('群聊信息已保存');
    } else {
      await createWechatGroup({ name, groupId, teamId: groupForm.teamId || 0 });
      MessagePlugin.success('监听群聊已新增，请继续加挂规则');
    }
    groupDialogVisible.value = false;
    await Promise.all([loadGroups(), loadStatus()]);
  } catch (err) {
    groupFormError.value = err.message || '保存失败';
  } finally {
    groupSaving.value = false;
  }
}

// 删除群聊：确认后真实删除，失败如实提示
function onDeleteGroup(group) {
  const confirm = DialogPlugin.confirm({
    header: '删除监听群聊',
    body: `确认删除「${group.name}」及其规则配置？正式业务记录不会删除。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      try {
        await deleteWechatGroup(group.id);
        MessagePlugin.success('群聊及其规则配置已删除');
        await loadGroups();
      } catch (err) {
        MessagePlugin.error(err.message || '删除失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 群规则 ----
const rulesVisible = ref(false);
const rulesLoading = ref(false);
const rulesError = ref('');
const rules = ref([]);
const currentGroup = ref(null);

async function openRules(group) {
  currentGroup.value = group;
  rulesVisible.value = true;
  await loadRules();
}

async function loadRules() {
  if (!currentGroup.value) return;
  rulesLoading.value = true;
  rulesError.value = '';
  try {
    rules.value = await listWechatRules(currentGroup.value.id);
  } catch (err) {
    rules.value = [];
    rulesError.value = err.message || '规则加载失败';
  } finally {
    rulesLoading.value = false;
  }
}

// 启停规则：只改启用状态，其余字段原样回传，Key 不提交（留空保持不变）
async function onToggleRule(rule, enabled) {
  try {
    await updateWechatRule(rule.id, {
      name: rule.name,
      enabled: !!enabled,
      askEnabled: rule.ask_enabled,
      keys: {},
    });
    rule.enabled = !!enabled;
    MessagePlugin.success(enabled ? '规则已启用' : '规则已停用');
    await loadGroups();
  } catch (err) {
    MessagePlugin.error(err.message || '规则状态更新失败');
  }
}

function onDeleteRule(rule) {
  const confirm = DialogPlugin.confirm({
    header: '删除规则',
    body: `确认删除规则「${rule.name}」？正式业务记录不会删除。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    cancelBtn: { content: '取消' },
    onConfirm: async () => {
      try {
        await deleteWechatRule(rule.id);
        MessagePlugin.success('规则已删除，正式业务记录保留');
        await Promise.all([loadRules(), loadGroups()]);
      } catch (err) {
        MessagePlugin.error(err.message || '删除失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 规则编辑 ----
const editorVisible = ref(false);
const ruleSaving = ref(false);
const ruleFormError = ref('');
const keysConfigured = reactive({
  tree_extract: false,
  tree_correct: false,
  crossing_extract: false,
  crossing_correct: false,
  ask_name: false,
  chat_digest: false,
  location: false,
});
const emptyKeys = () => ({
  tree_extract: '',
  tree_correct: '',
  crossing_extract: '',
  crossing_correct: '',
  ask_name: '',
  chat_digest: '',
  location: '',
});
const ruleForm = reactive({
  id: '',
  type: 'treeWatch',
  name: '',
  enabled: true,
  askEnabled: true,
  keys: emptyKeys(),
});

// 可选规则类型：同群同类型最多一条，已存在的类型不再提供
const typeOptions = computed(() => {
  const used = new Set((rules.value || []).map((r) => r.type));
  return [
    {
      label: '输电班组运维',
      value: 'treeWatch',
      disabled: used.has('treeWatch') && ruleForm.id === '',
    },
    { label: '位置消息', value: 'location', disabled: used.has('location') && ruleForm.id === '' },
  ];
});

function keyPlaceholder(field) {
  return keysConfigured[field] ? '已配置，输入以替换（留空保持不变）' : '未配置，请输入';
}

function openRuleEditor(rule) {
  ruleForm.id = rule ? String(rule.id) : '';
  ruleForm.type = rule
    ? rule.type
    : rules.value.some((r) => r.type === 'treeWatch')
      ? 'location'
      : 'treeWatch';
  ruleForm.name = rule ? rule.name : ruleTypeName(ruleForm.type);
  ruleForm.enabled = rule ? !!rule.enabled : true;
  ruleForm.askEnabled = rule ? rule.ask_enabled !== false : true;
  ruleForm.keys = emptyKeys();
  const configured = (rule && rule.keys_configured) || {};
  for (const field of Object.keys(keysConfigured)) {
    keysConfigured[field] = !!configured[field];
  }
  ruleFormError.value = '';
  editorVisible.value = true;
}

// 校验：必填项在「从未配置」时必须填写；已配置项留空表示保持不变
function validateRule() {
  if (!ruleForm.name.trim()) return '请填写规则名称';
  if (ruleForm.type === 'treeWatch' && currentGroup.value && !currentGroup.value.team_id) {
    return '输电班组运维要求先指定归属班组（请先编辑群聊）';
  }
  const need = (field, label) => {
    if (!keysConfigured[field] && !String(ruleForm.keys[field] || '').trim()) {
      return `请填写${label}`;
    }
    return '';
  };
  if (ruleForm.type === 'treeWatch') {
    return (
      need('tree_extract', 'WF1 · 树障提取 Key') ||
      need('tree_correct', 'WF2 · 树障更正 Key') ||
      (ruleForm.askEnabled ? need('ask_name', 'WF3 · 回答文件命名 Key') : '')
    );
  }
  return need('location', '位置处理 Dify Workflow Key');
}

async function onSaveRule() {
  const problem = validateRule();
  if (problem) {
    ruleFormError.value = problem;
    return;
  }
  if (
    !ruleForm.id &&
    (rules.value || []).some((r) => r.type === ruleForm.type && String(r.id) !== ruleForm.id)
  ) {
    ruleFormError.value = '本群已有同类型规则，不能重复添加';
    return;
  }
  ruleFormError.value = '';
  ruleSaving.value = true;
  try {
    const payload = {
      name: ruleForm.name.trim(),
      type: ruleForm.type,
      enabled: ruleForm.enabled,
      askEnabled: ruleForm.askEnabled,
      keys: ruleForm.keys,
    };
    if (ruleForm.id) {
      await updateWechatRule(ruleForm.id, payload);
      MessagePlugin.success('规则已保存');
    } else {
      await createWechatRule(currentGroup.value.id, payload);
      MessagePlugin.success('规则已保存并挂接到本群');
    }
    editorVisible.value = false;
    await Promise.all([loadRules(), loadGroups()]);
  } catch (err) {
    ruleFormError.value = err.message || '规则保存失败';
  } finally {
    ruleSaving.value = false;
  }
}

onMounted(() => {
  loadStatus();
  loadGroups();
  loadTeams();
});
</script>

<style scoped>
.status-card {
  display: flex;
  align-items: center;
  gap: 14px;
}
.status-card.warn {
  border-left: 3px solid #ff7d00;
}
.status-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: #e8ffea;
  color: #0b8a37;
  flex: none;
}
.status-icon.warn {
  background: #fff7e8;
  color: #d25f00;
}
.status-main {
  flex: 1;
  min-width: 0;
}
.status-desc {
  margin: 4px 0 0;
  color: #4e5969;
  font-size: 12px;
  line-height: 1.7;
}
.status-meta {
  color: #86909c;
  font-size: 11px;
  text-align: right;
  line-height: 1.7;
}
.status-meta b {
  color: #1d2129;
}
.status-actions {
  flex: none;
}
.group-table {
  width: 100%;
  border-collapse: collapse;
}
.group-table th {
  padding: 8px 10px;
  color: #86909c;
  font-size: 12px;
  font-weight: 600;
  text-align: left;
  border-bottom: 1px solid #e5e6eb;
}
.group-table td {
  padding: 12px 10px;
  border-bottom: 1px solid #e5e6eb;
  font-size: 13px;
  vertical-align: top;
}
.group-table tr:last-child td {
  border-bottom: none;
}
.group-name b {
  display: block;
}
.group-name small {
  color: #86909c;
  font-size: 11px;
}
.rule-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  background: #e8f0ff;
  color: #0e3da8;
  font-size: 11px;
}
.tag.orange {
  background: #fff7e8;
  color: #d25f00;
}
.tag.gray {
  background: #f7f8fa;
  color: #86909c;
}
.row-actions {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.state-box {
  padding: 32px 12px;
  text-align: center;
  color: #4e5969;
}
.state-box b {
  display: block;
  color: #1d2129;
  margin-bottom: 6px;
}
.state-box p {
  margin: 0;
  font-size: 12px;
  color: #86909c;
}
.state-box.error b {
  color: #f53f3f;
}
.source-note {
  margin-top: 10px;
  color: #86909c;
  font-size: 11px;
  line-height: 1.8;
}
.field-help {
  color: #86909c;
  font-size: 11px;
  line-height: 1.7;
}
.field-error {
  margin-top: 8px;
  padding: 8px 10px;
  background: #ffece8;
  color: #f53f3f;
  font-size: 12px;
  border-left: 3px solid #f53f3f;
}
.group-summary {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: #f7f8fa;
  border-radius: 8px;
}
.group-summary p {
  margin: 4px 0 0;
  font-size: 12px;
}
.rule-card {
  padding: 12px 14px;
  border: 1px solid #e5e6eb;
  border-radius: 8px;
  margin-bottom: 10px;
}
.rule-top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.rule-top > div:first-child {
  flex: 1;
  min-width: 0;
}
.rule-top > .t-switch {
  flex: none;
}
.rule-top p {
  margin: 2px 0 0;
  font-size: 11px;
}
.rule-meta {
  display: flex;
  gap: 8px;
  margin: 8px 0;
}
.rule-actions {
  display: flex;
  gap: 4px;
}
.editor-intro {
  margin: 0 0 12px;
  padding: 10px 12px;
  background: #e8f0ff;
  color: #4e5969;
  font-size: 12px;
  line-height: 1.8;
  border-left: 3px solid #0e3da8;
}
.form-section {
  padding: 14px;
  border: 1px solid #e5e6eb;
  border-radius: 8px;
  margin-bottom: 12px;
}
.form-section .hint {
  margin: 4px 0 10px;
  color: #86909c;
  font-size: 11px;
  line-height: 1.7;
}
.secret-state {
  margin-left: 10px;
  color: #0b8a37;
  font-size: 11px;
  white-space: nowrap;
}
.secret-state.empty {
  color: #d25f00;
}
.drawer-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  padding: 14px 0 4px;
}

/* 开关 + 说明文字同行：flex 垂直居中，不随基线错位 */
.switch-line {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 列表入场（对照设计稿 rise 口径：一次轻上浮后静止；reduce 降级） */
.group-table tbody tr,
.rule-card {
  animation: waRise 0.24s var(--tk-ease) both;
}
@keyframes waRise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .group-table tbody tr,
  .rule-card {
    animation: none !important;
  }
}
</style>
