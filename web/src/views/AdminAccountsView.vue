<template>
  <div class="adm-page">
    <!-- 无权限提示（旧版 admin.html 口径；路由守卫之外的兜底） -->
    <div v-if="!isAdmin" class="card noperm page-entry">
      <div class="np-ic"><Icon name="lock-on" :size="30" /></div>
      <h3>无权限访问</h3>
      <p>管理页仅对管理员开放。<br />如需权限请联系班组管理员。</p>
      <t-button theme="primary" @click="goWorkbench">返回工作台</t-button>
    </div>

    <template v-else>
      <div class="page-head page-entry">
        <h1 class="page-title">账号与权限管理</h1>
        <span class="page-sub"
          >班组 · 员工 · 应用启停排序 · 按人应用授权 · 消息推送 · 仅超级管理员</span
        >
      </div>

      <!-- 分段导航（滑动指示条，同旧版 admin.html 页签） -->
      <div class="tabs page-entry">
        <button
          v-for="t in tabs"
          :key="t.key"
          :ref="(el) => setTabEl(t.key, el)"
          class="tab"
          :class="{ on: tab === t.key }"
          @click="switchTab(t.key)"
        >
          {{ t.label }}
        </button>
        <span class="tab-ind" :class="{ ready: indReady }" :style="indStyle"></span>
      </div>

      <div :key="tab" class="pane-in">
        <!-- ===== 员工管理 ===== -->
        <template v-if="tab === 'users'">
          <div class="toolbar">
            <t-select
              v-model="teamFilter"
              :options="teamFilterOptions"
              style="width: 170px"
              @change="loadUsers"
            />
            <t-input
              v-model="userKeyword"
              placeholder="搜索账号或昵称"
              clearable
              style="width: 200px"
              @enter="loadUsers"
              @clear="loadUsers"
            />
            <span class="cnt num">{{ userCountText }}</span>
            <span class="sp">
              <t-button size="small" variant="outline" @click="loadUsers">
                <Icon name="refresh" :size="14" />刷新
              </t-button>
              <t-button size="small" theme="primary" @click="openAddUser">
                <Icon name="add" :size="14" />新建员工
              </t-button>
            </span>
          </div>

          <div class="card tbl-card">
            <div class="tbl-wrap">
              <table class="tbl">
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
                  <tr v-if="usersLoading">
                    <td colspan="8">
                      <div class="loading"><span class="spin"></span>加载中</div>
                    </td>
                  </tr>
                  <tr v-else-if="usersError">
                    <td colspan="8">
                      <div class="empty">
                        <Icon name="warn" :size="44" />
                        <p>{{ usersError }}</p>
                      </div>
                    </td>
                  </tr>
                  <template v-else-if="users.length">
                    <!-- 选了具体班组 / 未分组 → 单组平铺；「全部班组」按班组分组（组标题行） -->
                    <template v-if="teamFilter !== ''">
                      <tr v-for="u in users" :key="u.id">
                        <td class="uname num">{{ u.username }}</td>
                        <td>
                          <span v-if="u.nickname">{{ u.nickname }}</span>
                          <span v-else class="mut">—</span>
                        </td>
                        <td class="num">
                          <span v-if="u.wxid">{{ u.wxid }}</span>
                          <span v-else class="mut">—</span>
                        </td>
                        <td>
                          <span v-if="u.team">{{ u.team }}</span>
                          <span v-else class="mut">未分配</span>
                        </td>
                        <td>
                          <span v-if="u.role === 'admin'" class="tag role-admin">超级管理员</span>
                          <span v-else-if="u.role === 'team_admin'" class="badge-outline"
                            >班组管理员</span
                          >
                          <span v-else class="tag tag-navy">普通用户</span>
                        </td>
                        <td>
                          <span v-if="u.status === 1" class="tag tag-orange">启用</span>
                          <span v-else class="tag tag-red">停用</span>
                        </td>
                        <td class="mut num">{{ fmtDate(u.created_at) }}</td>
                        <td>
                          <button class="act" @click="openEditUser(u)">
                            <Icon name="edit" :size="13" />编辑
                          </button>
                          <button class="act" @click="openPwd(u)">
                            <Icon name="key" :size="13" />重置密码
                          </button>
                          <button
                            class="act"
                            :class="{ danger: u.status === 1 }"
                            :disabled="isMe(u)"
                            :title="isMe(u) ? '不能停用自己的账号' : ''"
                            @click="openToggle(u)"
                          >
                            <Icon
                              :name="u.status === 1 ? 'lock-on' : 'check-circle'"
                              :size="13"
                            />{{ u.status === 1 ? '停用' : '启用' }}
                          </button>
                          <button
                            class="act danger"
                            :disabled="isMe(u)"
                            :title="isMe(u) ? '不能删除自己的账号' : ''"
                            @click="onDeleteUser(u)"
                          >
                            <Icon name="delete" :size="13" />删除
                          </button>
                        </td>
                      </tr>
                    </template>
                    <template v-else>
                      <template v-for="g in userGroups" :key="g.name || '未分组'">
                        <tr class="grp-row">
                          <td colspan="8">
                            {{ g.name || '未分组' }}
                            <span class="grp-cnt">{{ g.items.length }} 人</span>
                          </td>
                        </tr>
                        <tr v-for="u in g.items" :key="u.id">
                          <td class="uname num">{{ u.username }}</td>
                          <td>
                            <span v-if="u.nickname">{{ u.nickname }}</span>
                            <span v-else class="mut">—</span>
                          </td>
                          <td class="num">
                            <span v-if="u.wxid">{{ u.wxid }}</span>
                            <span v-else class="mut">—</span>
                          </td>
                          <td>
                            <span v-if="u.team">{{ u.team }}</span>
                            <span v-else class="mut">未分配</span>
                          </td>
                          <td>
                            <span v-if="u.role === 'admin'" class="tag role-admin">超级管理员</span>
                            <span v-else-if="u.role === 'team_admin'" class="badge-outline"
                              >班组管理员</span
                            >
                            <span v-else class="tag tag-navy">普通用户</span>
                          </td>
                          <td>
                            <span v-if="u.status === 1" class="tag tag-orange">启用</span>
                            <span v-else class="tag tag-red">停用</span>
                          </td>
                          <td class="mut num">{{ fmtDate(u.created_at) }}</td>
                          <td>
                            <button class="act" @click="openEditUser(u)">
                              <Icon name="edit" :size="13" />编辑
                            </button>
                            <button class="act" @click="openPwd(u)">
                              <Icon name="key" :size="13" />重置密码
                            </button>
                            <button
                              class="act"
                              :class="{ danger: u.status === 1 }"
                              :disabled="isMe(u)"
                              :title="isMe(u) ? '不能停用自己的账号' : ''"
                              @click="openToggle(u)"
                            >
                              <Icon
                                :name="u.status === 1 ? 'lock-on' : 'check-circle'"
                                :size="13"
                              />{{ u.status === 1 ? '停用' : '启用' }}
                            </button>
                            <button
                              class="act danger"
                              :disabled="isMe(u)"
                              :title="isMe(u) ? '不能删除自己的账号' : ''"
                              @click="onDeleteUser(u)"
                            >
                              <Icon name="delete" :size="13" />删除
                            </button>
                          </td>
                        </tr>
                      </template>
                    </template>
                  </template>
                  <tr v-else>
                    <td colspan="8">
                      <div class="empty">
                        <Icon name="usergroup" :size="44" />
                        <p>暂无员工，点击右上角「新建员工」创建</p>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <!-- ===== 权限管理（左应用矩阵 / 右员工列表，勾选即保存） ===== -->
        <template v-else-if="tab === 'perms'">
          <div class="perm-wrap">
            <div class="card pm-main">
              <div class="pm-head">
                <template v-if="selectedUser">
                  <span
                    >正在为
                    <b>{{ selectedUser.nickname || selectedUser.username }}</b> 配置应用权限</span
                  >
                </template>
                <span v-else>请先选择右侧员工</span>
                <span v-if="selectedUser" class="st">勾选即保存</span>
              </div>
              <div class="pm-apps">
                <div v-if="permLoading" class="loading"><span class="spin"></span>加载中</div>
                <div v-else-if="permError" class="empty">
                  <Icon name="warn" :size="44" />
                  <p>{{ permError }}</p>
                </div>
                <template v-else-if="selectedUser">
                  <div
                    v-for="a in apps"
                    :key="a.id"
                    class="app-item"
                    :class="{ on: !!checkedMap[a.id], 'off-app': a.status !== 1 }"
                    @click="togglePerm(a)"
                  >
                    <span class="cbx"><Icon name="check" :size="12" /></span>
                    <span class="ai-ic"><Icon :name="a.icon || 'app'" :size="20" /></span>
                    <span class="ai-txt">
                      <span class="ai-nm">{{ a.name }}</span>
                      <span class="ai-key">{{ a.app_key }}</span>
                    </span>
                    <span v-if="a.status !== 1" class="ai-state">已下架</span>
                  </div>
                  <div v-if="!apps.length" class="empty">
                    <Icon name="app" :size="44" />
                    <p>暂无应用，请先在 sys_app 表配置应用</p>
                  </div>
                </template>
                <div v-else class="empty">
                  <Icon name="usergroup" :size="44" />
                  <p>选择右侧员工后勾选应用，勾选即保存</p>
                </div>
              </div>
            </div>

            <aside class="card pm-side">
              <div class="pm-search">
                <div class="field">
                  <Icon name="search" :size="18" />
                  <input
                    v-model="permKeyword"
                    class="input"
                    placeholder="搜索账号 / 昵称"
                    autocomplete="off"
                  />
                </div>
                <t-select
                  v-model="permTeamFilter"
                  :options="teamFilterOptions"
                  style="width: 100%; margin-top: 10px"
                  @change="loadPermUsers"
                />
              </div>
              <div class="pm-list">
                <template v-if="filteredPermUsers.length">
                  <template v-if="permTeamFilter !== ''">
                    <button
                      v-for="u in filteredPermUsers"
                      :key="u.id"
                      class="pm-user"
                      :class="{ on: selectedUser && selectedUser.id === u.id }"
                      @click="selectPermUser(u)"
                    >
                      <span class="pm-ava">{{ (u.nickname || u.username || '?').charAt(0) }}</span>
                      <span class="pm-meta">
                        <span class="nm">{{ u.nickname || u.username }}</span>
                        <span class="un num">{{ u.username }}</span>
                      </span>
                    </button>
                  </template>
                  <template v-else>
                    <template v-for="g in permUserGroups" :key="g.name || '未分组'">
                      <div class="pm-grp">{{ g.name || '未分组' }} · {{ g.items.length }} 人</div>
                      <button
                        v-for="u in g.items"
                        :key="u.id"
                        class="pm-user"
                        :class="{ on: selectedUser && selectedUser.id === u.id }"
                        @click="selectPermUser(u)"
                      >
                        <span class="pm-ava">{{
                          (u.nickname || u.username || '?').charAt(0)
                        }}</span>
                        <span class="pm-meta">
                          <span class="nm">{{ u.nickname || u.username }}</span>
                          <span class="un num">{{ u.username }}</span>
                        </span>
                      </button>
                    </template>
                  </template>
                </template>
                <div v-else class="empty">
                  <Icon name="search" :size="44" />
                  <p>{{ permUsers.length ? '没有匹配的员工' : '暂无员工' }}</p>
                </div>
              </div>
            </aside>
          </div>
        </template>

        <!-- ===== 班组管理 ===== -->
        <template v-else-if="tab === 'teams'">
          <div class="toolbar">
            <span class="cnt num">{{ teamCountText }}</span>
            <span class="sp">
              <t-button size="small" variant="outline" @click="loadTeams">
                <Icon name="refresh" :size="14" />刷新
              </t-button>
              <t-button size="small" theme="primary" @click="openTeamModal(null)">
                <Icon name="add" :size="14" />新增班组
              </t-button>
            </span>
          </div>

          <div class="card tbl-card">
            <div class="tbl-wrap">
              <table class="tbl">
                <thead>
                  <tr>
                    <th>班组名称</th>
                    <th>微信群 wxid</th>
                    <th>成员数</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-if="teamsLoading">
                    <td colspan="5">
                      <div class="loading"><span class="spin"></span>加载中</div>
                    </td>
                  </tr>
                  <tr v-else-if="teamsError">
                    <td colspan="5">
                      <div class="empty">
                        <Icon name="warn" :size="44" />
                        <p>{{ teamsError }}</p>
                      </div>
                    </td>
                  </tr>
                  <template v-else>
                    <tr v-for="t in teams" :key="t.id">
                      <td class="uname">
                        {{ t.name
                        }}<span v-if="t.id === defaultTeamId" class="def-pill">默认班组</span>
                      </td>
                      <td class="num">
                        <span v-if="t.wxid">{{ t.wxid }}</span>
                        <span v-else class="mut">—</span>
                      </td>
                      <td class="num">{{ t.user_count || 0 }} 人</td>
                      <td>
                        <span v-if="t.status === 1" class="tag tag-orange">启用</span>
                        <span v-else class="tag tag-red">停用</span>
                      </td>
                      <td>
                        <button class="act" @click="openTeamModal(t)">
                          <Icon name="edit" :size="13" />编辑
                        </button>
                      </td>
                    </tr>
                    <tr v-if="!teams.length">
                      <td colspan="5">
                        <div class="empty">
                          <Icon name="usergroup" :size="44" />
                          <p>暂无班组，点击右上角「新增班组」创建</p>
                        </div>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <!-- ===== 应用管理（启停/排序） ===== -->
        <template v-else-if="tab === 'apps'">
          <div class="toolbar">
            <span class="cnt num">{{ appsCountText }}</span>
            <span class="sp">
              <t-button size="small" variant="outline" @click="loadApps">
                <Icon name="refresh" :size="14" />刷新
              </t-button>
            </span>
          </div>

          <div class="card tbl-card">
            <div class="tbl-wrap">
              <table class="tbl">
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
                  <tr v-if="appsLoading">
                    <td colspan="6">
                      <div class="loading"><span class="spin"></span>加载中</div>
                    </td>
                  </tr>
                  <tr v-else-if="appsError">
                    <td colspan="6">
                      <div class="empty">
                        <Icon name="warn" :size="44" />
                        <p>{{ appsError }}</p>
                      </div>
                    </td>
                  </tr>
                  <template v-else>
                    <tr v-for="a in apps" :key="a.id">
                      <td class="uname">
                        <span class="ai-inline">
                          <span class="ai-ic"><Icon :name="a.icon || 'app'" :size="18" /></span>
                          {{ a.name }}
                        </span>
                      </td>
                      <td class="mut num">{{ a.app_key }}</td>
                      <td>{{ terminalName(a.terminal) }}</td>
                      <td>
                        <input
                          v-model.number="appDraft[a.id].sort"
                          class="input cell-num"
                          type="number"
                          min="0"
                          step="1"
                        />
                      </td>
                      <td>
                        <button
                          type="button"
                          class="switch"
                          :class="{ on: appDraft[a.id].status === 1 }"
                          title="点击切换上架 / 下架"
                          @click="appDraft[a.id].status = appDraft[a.id].status === 1 ? 0 : 1"
                        ></button>
                      </td>
                      <td>
                        <button
                          class="act primary"
                          :disabled="appSavingId === a.id"
                          @click="onSaveApp(a)"
                        >
                          <Icon name="check" :size="13" />{{
                            appSavingId === a.id ? '保存中…' : '保存'
                          }}
                        </button>
                      </td>
                    </tr>
                    <tr v-if="!apps.length">
                      <td colspan="6">
                        <div class="empty">
                          <Icon name="app" :size="44" />
                          <p>应用目录为空，应用由各业务模块建表种子登记</p>
                        </div>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <!-- ===== 消息推送 ===== -->
        <template v-else-if="tab === 'push'">
          <div class="card push-card">
            <div class="f-row">
              <label>标题</label>
              <input
                v-model="pushTitle"
                class="input"
                maxlength="128"
                placeholder="请输入通知标题（128 字以内）"
                autocomplete="off"
              />
            </div>
            <div class="f-row">
              <label>内容 <i>支持 Markdown 语法，可插入图片</i></label>
              <div class="push-tools">
                <t-button size="small" variant="outline" @click="pickPushImage">
                  <Icon name="image" :size="14" />插入图片
                </t-button>
                <t-button size="small" variant="outline" @click="togglePushPreview">
                  {{ pushPreviewOn ? '继续编辑' : '预览' }}
                </t-button>
              </div>
              <textarea
                v-show="!pushPreviewOn"
                ref="pushContentEl"
                v-model="pushContent"
                class="input"
                placeholder="请输入通知内容，支持 Markdown：# 标题、**加粗**、- 列表、![图片描述](图片地址) 等"
              ></textarea>
              <div v-show="pushPreviewOn" class="push-preview" v-html="pushPreviewHtml"></div>
              <input
                ref="pushImgFileEl"
                type="file"
                accept="image/jpeg,image/png,image/gif,image/webp"
                style="display: none"
                @change="onPushImageChange"
              />
            </div>
            <div class="f-row">
              <label>推送对象 <i>至少选择一项</i></label>
              <div class="push-targets">
                <label v-for="r in pushRoles" :key="r.value" class="push-ck">
                  <input v-model="pushTargets" type="checkbox" :value="r.value" />{{ r.label }}
                </label>
              </div>
            </div>
            <div class="f-note">
              通知按角色推送，成员在工作台「消息通知」与小程序首页的通知卡可见，未读数以红色徽章提示。内容支持
              Markdown 排版，图片经「插入图片」上传后自动引用。
            </div>
            <div class="push-ft">
              <t-button theme="primary" :loading="pushSaving" @click="onPushSubmit">
                <Icon name="send" :size="14" />{{ pushSaving ? '推送中…' : '推送' }}
              </t-button>
            </div>
          </div>
        </template>
      </div>
    </template>

    <!-- ===== 新建员工弹层 ===== -->
    <t-dialog
      v-model:visible="addVisible"
      header="新建员工"
      width="440px"
      :confirm-btn="{
        content: addSaving ? '创建中…' : '创建',
        theme: 'primary',
        loading: addSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onAddSubmit"
      @close="addVisible = false"
    >
      <div class="f-row">
        <label>账号</label>
        <t-input
          v-model="addForm.username"
          class="f-input"
          placeholder="2-64 位字母、数字或下划线"
          autocomplete="off"
        />
      </div>
      <div class="f-row">
        <label>昵称 <i>选填</i></label>
        <t-input v-model="addForm.nickname" class="f-input" placeholder="选填" autocomplete="off" />
      </div>
      <div class="f-row">
        <label>wxid <i>选填</i></label>
        <t-input
          v-model="addForm.wxid"
          class="f-input"
          placeholder="微信 wxid，消息微信推送用，可空"
          autocomplete="off"
          :maxlength="128"
        />
      </div>
      <div class="f-row">
        <label>班组 <i>选填</i></label>
        <t-select
          v-model="addForm.teamId"
          :options="dialogTeamOptions"
          placeholder="未分配"
          clearable
          style="width: 100%"
        />
      </div>
      <div class="f-row">
        <label>角色</label>
        <t-select v-model="addForm.role" :options="roleOptions" style="width: 100%" />
      </div>
      <div class="f-row">
        <label>初始密码</label>
        <t-input
          v-model="addForm.password"
          class="f-input"
          type="password"
          placeholder="至少 6 位"
          autocomplete="new-password"
        />
      </div>
    </t-dialog>

    <!-- ===== 编辑员工弹层 ===== -->
    <t-dialog
      v-model:visible="editVisible"
      header="编辑员工"
      width="440px"
      :confirm-btn="{
        content: editSaving ? '保存中…' : '保存',
        theme: 'primary',
        loading: editSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onEditSubmit"
      @close="editVisible = false"
    >
      <div class="tip">账号：{{ editForm.username }}</div>
      <div class="f-row">
        <label>昵称</label>
        <t-input
          v-model="editForm.nickname"
          class="f-input"
          placeholder="选填"
          autocomplete="off"
        />
      </div>
      <div class="f-row">
        <label>wxid <i>选填</i></label>
        <t-input
          v-model="editForm.wxid"
          class="f-input"
          placeholder="微信 wxid，消息微信推送用，可空"
          autocomplete="off"
          :maxlength="128"
        />
      </div>
      <div class="f-row">
        <label>班组</label>
        <t-select
          v-model="editForm.teamId"
          :options="dialogTeamOptions"
          placeholder="未分配"
          clearable
          style="width: 100%"
        />
      </div>
      <div class="f-row">
        <label>角色 <i v-if="editForm.isSelf">不能降级自己的账号</i></label>
        <t-select
          v-model="editForm.role"
          :options="roleOptions"
          :disabled="editForm.isSelf"
          style="width: 100%"
        />
      </div>
    </t-dialog>

    <!-- ===== 重置密码弹层 ===== -->
    <t-dialog
      v-model:visible="pwdVisible"
      header="重置密码"
      width="440px"
      :confirm-btn="{
        content: pwdSaving ? '提交中…' : '重置',
        theme: 'primary',
        loading: pwdSaving,
      }"
      :cancel-btn="{ content: '取消' }"
      :close-on-overlay-click="false"
      @confirm="onPwdSubmit"
      @close="pwdVisible = false"
    >
      <div class="tip">
        重置密码：{{ pwdTarget ? pwdTarget.nickname || pwdTarget.username : '' }}
      </div>
      <div class="f-row">
        <label>新密码</label>
        <t-input
          v-model="pwdValue"
          class="f-input"
          type="password"
          placeholder="请输入新密码（至少 6 位）"
          autocomplete="new-password"
        />
      </div>
    </t-dialog>

    <!-- ===== 新增 / 编辑班组弹层（删除班组在弹层内，二次确认） ===== -->
    <t-dialog
      v-model:visible="teamVisible"
      :header="teamForm.id ? '编辑班组' : '新增班组'"
      width="440px"
      :close-on-overlay-click="false"
      @close="teamVisible = false"
    >
      <template #footer>
        <button v-if="teamForm.id" class="link-danger" @click="onTeamDelClick">删除班组</button>
        <span class="ft-spacer"></span>
        <t-button variant="outline" @click="teamVisible = false">取消</t-button>
        <t-button theme="primary" :loading="teamSaving" @click="onTeamSubmit">保存</t-button>
      </template>
      <div class="f-row">
        <label>班组名称</label>
        <t-input
          v-model="teamForm.name"
          class="f-input"
          placeholder="如：运维二班"
          autocomplete="off"
          :maxlength="64"
        />
      </div>
      <div class="f-row">
        <label>微信群 wxid <i>选填</i></label>
        <t-input
          v-model="teamForm.wxid"
          class="f-input"
          placeholder="班组微信群 wxid，消息推送用，可空"
          autocomplete="off"
          :maxlength="128"
        />
      </div>
      <div v-if="teamForm.id" class="f-row">
        <label>排序</label>
        <t-input-number v-model="teamForm.sort" theme="column" :min="0" :step="1" />
        <div class="f-note">数值小的排在前面；首个启用班组即默认班组。</div>
      </div>
      <div v-if="teamForm.id" class="f-row">
        <label>启用状态</label>
        <button
          type="button"
          class="switch"
          :class="{ on: teamForm.status === 1 }"
          title="点击切换启用 / 停用"
          @click="teamForm.status = teamForm.status === 1 ? 0 : 1"
        ></button>
      </div>
      <div class="f-note">
        KVM 设备组与班组同名，无需单独设置；班组改名后，GLKVM 平台侧设备组需同步改名，否则远程连接 /
        文件传输将回退显示默认班组设备。
      </div>
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
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
  noticePush,
  noticeUploadImage,
  setUserApps,
  updateApp,
  updateTeam,
  updateUser,
} from '../features/accounts/api.js';
import { renderMd } from '../features/callme/md.js';
import { createGeneration } from '../features/core/async.js';
import Icon from '../features/accounts/Icon.vue';

// 旧版 admin.html 还原：分段导航（滑动指示条）+ 员工（按班组分组）/ 权限（勾选即存）/
// 班组（默认班组徽章、删除在编辑弹层内）/ 应用（启停排序）/ 消息推送（Markdown + 配图）。
// 接口全部走 features/accounts/api.js 真实后端，失败原文如实提示，不伪造成功。

const router = useRouter();

// 页签（旧版页签顺序：员工 / 权限 / 班组 / 推送；「应用管理」为既有能力，插在推送前）
const tabs = [
  { key: 'users', label: '员工管理' },
  { key: 'perms', label: '权限管理' },
  { key: 'teams', label: '班组管理' },
  { key: 'apps', label: '应用管理' },
  { key: 'push', label: '消息推送' },
];
const tab = ref('users');

// ---- 滑动指示条（同旧版 Shade.moveTabInd：首帧直接落位，之后 left/width 过渡） ----
const tabEls = {};
const indStyle = ref({ left: '0px', width: '0px' });
const indReady = ref(false);

function setTabEl(key, el) {
  if (el) tabEls[key] = el;
}

function moveTabInd(instant) {
  const el = tabEls[tab.value];
  if (!el) return;
  if (instant) indReady.value = false;
  indStyle.value = { left: `${el.offsetLeft}px`, width: `${el.offsetWidth}px` };
  if (instant) {
    nextTick(() => {
      indReady.value = true;
    });
  }
}

function switchTab(key) {
  if (tab.value === key) return;
  tab.value = key;
  nextTick(() => moveTabInd(false));
}

function onResize() {
  moveTabInd(false);
}

// ---- 当前登录用户（自我保护：不能停用/降级/删除自己，与服务端同口径） ----
const me = currentUser() || {};
const isAdmin = computed(() => me.role === 'admin');
const isMe = (u) => !!me.id && u.id === me.id;

function goWorkbench() {
  router.push({ name: 'workbench' });
}

const roleOptions = [
  { label: '普通用户', value: 'user' },
  { label: '班组管理员', value: 'team_admin' },
  { label: '超级管理员', value: 'admin' },
];
const terminalName = (t) => ({ both: '双端', mobile: '仅小程序', pc: '仅网页端' })[t] || t || '—';

// 注册时间展示（同旧版 Shade.fmtDate：YYYY-MM-DD）
function fmtDate(d) {
  if (!d) return '—';
  const dt = new Date(d);
  if (isNaN(dt.getTime())) return '—';
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
}

// 按班组分组（组序同班组列表 sort 序，表外名兜底，未分组排最后；同旧版 groupByTeam）
function groupByTeam(list) {
  const byTeam = {};
  (list || []).forEach((u) => {
    const k = u.team || '';
    (byTeam[k] = byTeam[k] || []).push(u);
  });
  const order = [];
  teams.value.forEach((t) => {
    if (byTeam[t.name] && !order.includes(t.name)) order.push(t.name);
  });
  Object.keys(byTeam).forEach((k) => {
    if (k && !order.includes(k)) order.push(k);
  });
  if (byTeam['']) order.push('');
  return order.map((k) => ({ name: k, items: byTeam[k] }));
}

// ---- 班组数据（筛选 / 弹层共用） ----
const teams = ref([]);
const teamsLoading = ref(false);
const teamsError = ref('');
// teamsGen / usersGen / permUsersGen 列表请求代际：快速切筛选/刷新时旧响应不得覆盖新结果。
const teamsGen = createGeneration();
const usersGen = createGeneration();
const permUsersGen = createGeneration();

async function loadTeams() {
  const gen = teamsGen.next();
  teamsLoading.value = true;
  teamsError.value = '';
  try {
    const data = await listTeams();
    if (!teamsGen.isCurrent(gen)) return;
    teams.value = data;
  } catch (e) {
    if (!teamsGen.isCurrent(gen)) return;
    teams.value = [];
    teamsError.value = e.message || '班组列表加载失败';
  } finally {
    if (teamsGen.isCurrent(gen)) teamsLoading.value = false;
  }
}

// 默认班组 = 首个启用班组（与后端 getDefaultTeam 同口径，KVM 设备回退归它）
const defaultTeamId = computed(() => {
  const t = teams.value.find((x) => x.status === 1);
  return t ? t.id : 0;
});

// 筛选下拉（全部班组 / 各班组 / 未分组，同旧版 renderTeamFilter 顺序）
const teamFilterOptions = computed(() => [
  { label: '全部班组', value: '' },
  ...teams.value.map((t) => ({
    label: t.status === 1 ? t.name : `${t.name}（已停用）`,
    value: t.id,
  })),
  { label: '未分组', value: 0 },
]);

// 新建/编辑弹层班组下拉（仅可选启用班组；原班组已停用/删除时保留显示，提交由后端校验；
// 「未分配」取 0 哨兵，提交时转 null——TDesign 下拉 null 值不稳）
const dialogTeamOptions = ref([]);

function buildDialogTeamOptions(selectedId) {
  const opts = [{ label: '未分配', value: 0 }];
  teams.value
    .filter((t) => t.status === 1)
    .forEach((t) => opts.push({ label: t.name, value: t.id }));
  if (selectedId && !opts.some((o) => o.value === selectedId)) {
    const t = teams.value.find((x) => x.id === selectedId);
    opts.push({ label: `${(t && t.name) || '原班组'}（已停用）`, value: selectedId });
  }
  dialogTeamOptions.value = opts;
}

// ---- 员工管理 ----
const users = ref([]);
const usersLoading = ref(false);
const usersError = ref('');
const userKeyword = ref('');
const teamFilter = ref('');

const userCountText = computed(() => {
  if (usersLoading.value) return '加载中…';
  if (usersError.value) return '加载失败';
  return `共 ${users.value.length} 名员工`;
});
const userGroups = computed(() => groupByTeam(users.value));

async function loadUsers() {
  const gen = usersGen.next();
  usersLoading.value = true;
  usersError.value = '';
  try {
    const data = await listUsers({ keyword: userKeyword.value, teamId: teamFilter.value });
    if (!usersGen.isCurrent(gen)) return;
    users.value = data;
  } catch (e) {
    if (!usersGen.isCurrent(gen)) return;
    users.value = [];
    usersError.value = e.message || '员工列表加载失败';
  } finally {
    if (usersGen.isCurrent(gen)) usersLoading.value = false;
  }
}

// ---- 新建员工 ----
const addVisible = ref(false);
const addSaving = ref(false);
const addForm = reactive({
  username: '',
  nickname: '',
  wxid: '',
  teamId: 0, // 0 = 未分配（TDesign 下拉 null 值不稳，用 0 哨兵，提交转 null）
  role: 'user',
  password: '',
});

function openAddUser() {
  addForm.username = '';
  addForm.nickname = '';
  addForm.wxid = '';
  addForm.teamId = 0;
  addForm.role = 'user';
  addForm.password = '';
  buildDialogTeamOptions(0);
  addVisible.value = true;
}

async function onAddSubmit() {
  const username = addForm.username.trim();
  const password = addForm.password;
  if (!username || !password) {
    MessagePlugin.info('请填写账号和初始密码');
    return;
  }
  const teamId = addForm.teamId ? Number(addForm.teamId) : null;
  if (addForm.role === 'team_admin' && !teamId) {
    MessagePlugin.info('班组管理员必须有所属班组');
    return;
  }
  addSaving.value = true;
  try {
    await createUser({
      username,
      password,
      nickname: addForm.nickname.trim(),
      wxid: addForm.wxid.trim(),
      teamId,
      role: addForm.role,
    });
    MessagePlugin.success('已创建');
    addVisible.value = false;
    loadTeams(); // 成员数变化
    loadUsers();
    loadPermUsers();
  } catch (e) {
    MessagePlugin.error(e.message || '创建失败'); // 服务端原文（账号已存在等）如实提示
  } finally {
    addSaving.value = false;
  }
}

// ---- 编辑员工（昵称 / wxid / 班组 / 角色） ----
const editVisible = ref(false);
const editSaving = ref(false);
const editForm = reactive({
  id: null,
  username: '',
  nickname: '',
  wxid: '',
  teamId: 0, // 0 = 未分配，提交转 null
  role: 'user',
  isSelf: false,
});

function openEditUser(u) {
  editForm.id = u.id;
  editForm.username = u.username;
  editForm.nickname = u.nickname || '';
  editForm.wxid = u.wxid || '';
  editForm.teamId = u.team_id == null ? 0 : u.team_id;
  editForm.role = u.role === 'admin' ? 'admin' : u.role === 'team_admin' ? 'team_admin' : 'user';
  editForm.isSelf = isMe(u); // 编辑自己时角色置灰（不能降级自己的账号）
  buildDialogTeamOptions(editForm.teamId);
  editVisible.value = true;
}

async function onEditSubmit() {
  const teamId = editForm.teamId ? Number(editForm.teamId) : null;
  if (editForm.role === 'team_admin' && !teamId && !editForm.isSelf) {
    MessagePlugin.info('班组管理员必须有所属班组');
    return;
  }
  editSaving.value = true;
  try {
    const patch = {
      nickname: editForm.nickname.trim(),
      wxid: editForm.wxid.trim(),
      team_id: teamId, // null = 未分配
    };
    if (!editForm.isSelf) patch.role = editForm.role;
    await updateUser(editForm.id, patch);
    MessagePlugin.success('已保存');
    editVisible.value = false;
    loadTeams(); // 成员数变化
    loadUsers();
    loadPermUsers();
  } catch (e) {
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    editSaving.value = false;
  }
}

// ---- 重置密码 ----
const pwdVisible = ref(false);
const pwdSaving = ref(false);
const pwdTarget = ref(null);
const pwdValue = ref('');

function openPwd(u) {
  pwdTarget.value = u;
  pwdValue.value = '';
  pwdVisible.value = true;
}

async function onPwdSubmit() {
  const password = pwdValue.value.trim();
  if (password.length < 6) {
    MessagePlugin.info('密码至少 6 位');
    return;
  }
  pwdSaving.value = true;
  try {
    await updateUser(pwdTarget.value.id, { password });
    MessagePlugin.success('密码已重置');
    pwdVisible.value = false;
  } catch (e) {
    MessagePlugin.error(e.message || '重置失败');
  } finally {
    pwdSaving.value = false;
  }
}

// ---- 启用 / 停用（二次确认，同旧版 cfmMask 文案） ----
function openToggle(u) {
  const disabling = u.status === 1;
  const name = u.nickname || u.username;
  const confirm = DialogPlugin.confirm({
    header: disabling ? '停用账号' : '启用账号',
    body: `确定${disabling ? '停用' : '启用'}「${name}」吗？${
      disabling ? '停用后该账号将无法登录。' : ''
    }`,
    confirmBtn: { content: disabling ? '停用' : '启用', theme: disabling ? 'danger' : 'primary' },
    async onConfirm() {
      try {
        await updateUser(u.id, { status: disabling ? 0 : 1 });
        MessagePlugin.success(disabling ? '已停用' : '已启用');
        loadUsers();
      } catch (e) {
        MessagePlugin.error(e.message || '操作失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 删除员工 ----
function onDeleteUser(u) {
  const confirm = DialogPlugin.confirm({
    header: '删除员工',
    body: `确定删除员工「${u.username}」吗？删除后不可恢复；不能删除自己或最后一个超级管理员。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    async onConfirm() {
      try {
        await deleteUser(u.id);
        MessagePlugin.success('已删除');
        loadTeams();
        loadUsers();
        loadPermUsers();
      } catch (e) {
        MessagePlugin.error(e.message || '删除失败');
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 权限管理（左应用矩阵 / 右员工列表；勾选即全量保存） ----
const permUsers = ref([]);
const permTeamFilter = ref('');
const permKeyword = ref('');
const selectedUser = ref(null);
const permLoading = ref(false);
const permError = ref('');
const checkedMap = reactive({});
let savingPerm = false;
let permDirty = false; // 保存期间又有新勾选

const filteredPermUsers = computed(() => {
  const kw = permKeyword.value.trim().toLowerCase();
  return (permUsers.value || []).filter((u) => {
    if (!kw) return true;
    return (
      (u.username || '').toLowerCase().includes(kw) || (u.nickname || '').toLowerCase().includes(kw)
    );
  });
});
const permUserGroups = computed(() => groupByTeam(filteredPermUsers.value));

async function loadPermUsers() {
  const gen = permUsersGen.next();
  try {
    const data = await listUsers({ teamId: permTeamFilter.value });
    if (!permUsersGen.isCurrent(gen)) return;
    permUsers.value = data;
  } catch (e) {
    if (!permUsersGen.isCurrent(gen)) return;
    permUsers.value = [];
  }
}

function selectPermUser(u) {
  if (selectedUser.value && selectedUser.value.id === u.id) return;
  selectedUser.value = u;
  permDirty = false; // 切换用户清 dirty：上个用户保存期间的勾选不再补存，防止写错账号
  permBaseline = []; // 新用户授权未加载前，失败回滚不得沿用上一用户基线
  Object.keys(checkedMap).forEach((k) => delete checkedMap[k]);
  loadUserApps();
}

async function loadUserApps() {
  const uid = selectedUser.value.id;
  permLoading.value = true;
  permError.value = '';
  try {
    const ids = await getUserApps(uid);
    if (!selectedUser.value || selectedUser.value.id !== uid) return; // 过期响应丢弃
    setCheckedIds(ids || []);
    permBaseline = (ids || []).map(Number);
  } catch (e) {
    if (!selectedUser.value || selectedUser.value.id !== uid) return;
    permBaseline = [];
    permError.value = e.message || '授权信息加载失败';
  } finally {
    if (selectedUser.value && selectedUser.value.id === uid) permLoading.value = false;
  }
}

// permBaseline 最近一次服务端确认的授权（保存失败回滚用，按当前选中用户）。
let permBaseline = [];
function setCheckedIds(ids) {
  Object.keys(checkedMap).forEach((k) => delete checkedMap[k]);
  (ids || []).forEach((id) => {
    checkedMap[id] = true;
  });
}

function togglePerm(a) {
  if (!selectedUser.value) return;
  checkedMap[a.id] = !checkedMap[a.id];
  savePerms();
}

async function savePerms() {
  if (savingPerm) {
    permDirty = true;
    return;
  }
  savingPerm = true;
  const uid = selectedUser.value.id; // 快照本次保存用户：保存期间切换用户时补存不串号
  const ids = Object.keys(checkedMap)
    .filter((k) => checkedMap[k])
    .map(Number);
  try {
    await setUserApps(uid, ids);
    if (selectedUser.value && selectedUser.value.id === uid) permBaseline = ids.slice();
    MessagePlugin.success('授权已保存');
  } catch (e) {
    // 失败回滚到最近一次服务端确认的勾选，避免界面显示“已授权”而服务端未落库。
    if (selectedUser.value && selectedUser.value.id === uid) {
      setCheckedIds(permBaseline);
      permDirty = false;
    }
    MessagePlugin.error(e.message || '授权保存失败');
    return;
  } finally {
    savingPerm = false;
  }
  // 保存期间产生的勾选补一次保存；仍属原用户才补（切换用户时 dirty 已清，双保险防误写他人权限）
  if (permDirty && selectedUser.value && selectedUser.value.id === uid) {
    permDirty = false;
    savePerms();
  }
}

// ---- 班组弹层（新增/编辑；删除在弹层内 + 二次确认） ----
const teamVisible = ref(false);
const teamSaving = ref(false);
const teamForm = reactive({ id: null, name: '', wxid: '', sort: 0, status: 1 });

function openTeamModal(t) {
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
  teamVisible.value = true;
}

async function onTeamSubmit() {
  const name = teamForm.name.trim();
  if (!name) {
    MessagePlugin.info('请输入班组名称');
    return;
  }
  teamSaving.value = true;
  try {
    if (teamForm.id) {
      await updateTeam(teamForm.id, {
        name,
        wxid: teamForm.wxid.trim(),
        sort: Number(teamForm.sort) || 0,
        status: teamForm.status === 1 ? 1 : 0,
      });
      MessagePlugin.success('已保存');
    } else {
      await createTeam({ name, wxid: teamForm.wxid.trim() });
      MessagePlugin.success('已创建');
    }
    teamVisible.value = false;
    loadTeams();
    loadUsers(); // 班改名 / 停启用后员工列表同步
    loadPermUsers();
  } catch (e) {
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    teamSaving.value = false;
  }
}

function onTeamDelClick() {
  if (!teamForm.id) return;
  const editingTeam = teams.value.find((x) => x.id === teamForm.id) || { name: teamForm.name };
  const confirm = DialogPlugin.confirm({
    header: '删除班组',
    body: `确定删除班组「${editingTeam.name}」吗？删除后不可恢复；班组下仍有员工或业务数据时无法删除，可改为停用。`,
    confirmBtn: { content: '确认删除', theme: 'danger' },
    async onConfirm() {
      try {
        await deleteTeam(teamForm.id);
        MessagePlugin.success('已删除');
        teamVisible.value = false;
        loadTeams();
        loadUsers();
        loadPermUsers();
      } catch (e) {
        MessagePlugin.error(e.message || '删除失败'); // 40903 等冲突文案由后端给出
      } finally {
        confirm.destroy();
      }
    },
  });
}

// ---- 应用管理（启停/排序） ----
const apps = ref([]);
const appsLoading = ref(false);
const appsError = ref('');
const appSavingId = ref(null);
const appDraft = reactive({});

const appsCountText = computed(() => {
  if (appsLoading.value) return '加载中…';
  if (appsError.value) return '加载失败';
  return `共 ${apps.value.length} 个应用`;
});

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
    apps.value = [];
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
    // 保存失败回滚草稿到列表当前值，避免界面停留在未落库的启停/排序。
    appDraft[a.id] = { sort: a.sort, status: a.status };
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    appSavingId.value = null;
  }
}

// ---- 消息推送（Markdown + 配图；三类对象勾选制） ----
const pushTitle = ref('');
const pushContent = ref('');
const pushTargets = ref([]);
const pushSaving = ref(false);
const pushPreviewOn = ref(false);
const pushContentEl = ref(null);
const pushImgFileEl = ref(null);

const pushRoles = [
  { label: '超级管理员', value: 'admin' },
  { label: '班组管理员', value: 'team_admin' },
  { label: '普通成员', value: 'user' },
];

const pushPreviewHtml = computed(
  () => renderMd(pushContent.value) || '<span style="color: var(--tk-ph)">暂无内容</span>'
);

function pickPushImage() {
  if (pushImgFileEl.value) pushImgFileEl.value.click();
}

async function onPushImageChange(e) {
  const input = e.target;
  const file = input.files && input.files[0];
  input.value = ''; // 置空以允许重复选择同一文件
  if (!file) return;
  if (file.size > 50 * 1024 * 1024) {
    MessagePlugin.info('图片大小应在 50MB 以内');
    return;
  }
  MessagePlugin.info('图片上传中…');
  try {
    const url = await noticeUploadImage(file);
    if (!url) throw new Error('上传失败，未返回图片地址');
    insertAtCursor(`\n![图片](${url})\n`);
    MessagePlugin.success('图片已插入');
  } catch (err) {
    MessagePlugin.error(err.message || '上传失败');
  }
}

// 在 textarea 光标处插入文本（取不到光标时追加到末尾）
function insertAtCursor(text) {
  const ta = pushContentEl.value;
  if (!ta || typeof ta.selectionStart !== 'number') {
    pushContent.value += text;
    return;
  }
  const s = ta.selectionStart;
  const e2 = ta.selectionEnd;
  pushContent.value = pushContent.value.slice(0, s) + text + pushContent.value.slice(e2);
  nextTick(() => {
    ta.selectionStart = ta.selectionEnd = s + text.length;
    ta.focus();
  });
}

function togglePushPreview() {
  pushPreviewOn.value = !pushPreviewOn.value;
}

async function onPushSubmit() {
  const title = pushTitle.value.trim();
  const content = pushContent.value.trim();
  const targets = pushTargets.value.slice();
  if (!title || !content) {
    MessagePlugin.info('请填写标题和内容');
    return;
  }
  if (!targets.length) {
    MessagePlugin.info('请至少选择一个推送对象');
    return;
  }
  pushSaving.value = true;
  try {
    await noticePush({ title, content, targets });
    MessagePlugin.success('推送成功');
    pushTitle.value = '';
    pushContent.value = '';
    pushTargets.value = [];
    pushPreviewOn.value = false;
  } catch (e) {
    MessagePlugin.error(e.message || '推送失败');
  } finally {
    pushSaving.value = false;
  }
}

onMounted(() => {
  if (!isAdmin.value) return;
  loadTeams();
  loadUsers();
  loadPermUsers();
  loadApps();
  nextTick(() => moveTabInd(true));
  window.addEventListener('resize', onResize);
});

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize);
});
</script>

<style scoped>
/* ===== 本页补充 token（与 server/public/assets/theme.css 同名同值）：实心按钮文字等 ===== */
.adm-page {
  --on-brand: #ffffff;
  /* 整页骨架：覆盖 .page 内边距，页底色随本页 token 翻转 */
  margin: -28px -40px -48px;
  padding: 28px 40px 48px;
  min-height: calc(100% + 76px);
  background: var(--tk-bg);
  color: var(--tk-ink);
  box-sizing: border-box;
}

/* 深色：按 server/public/assets/theme.css 深色 token 在本页重映射（不简单反色）；
   全局 styles.css 未带深色覆盖，故限定本页作用域，不动全局样式文件 */
@media (prefers-color-scheme: dark) {
  .adm-page {
    color-scheme: dark;
    --tk-brand: #78a6ff;
    --tk-brand-deep: #78a6ff;
    --tk-ink: #e5e6eb;
    --tk-bg: #14171d;
    --tk-fill: #252b35;
    --tk-line: #343c49;
    --tk-sub: #a9b1bd;
    --tk-ph: #8a94a3;
    --tk-dash: #626d7d;
    --tk-blue-soft: #1b2d4d;
    --tk-red: #ff7a73;
    --tk-red-soft: #3a1d1d;
    --tk-green: #4fd37e;
    --tk-green-soft: #153322;
    --tk-yellow: #f5a94a;
    --tk-yellow-soft: #382b16;
    --tk-shadow-card: 0 2px 8px rgba(0, 0, 0, 0.24);
    --tk-shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.32);
    --on-brand: #101820;
    /* TDesign 页内控件（输入/选择等，随 DOM 继承）同步翻转 */
    --td-brand-color: #78a6ff;
    --td-brand-color-focus: rgba(120, 166, 255, 0.18);
    --td-bg-color-page: #14171d;
    --td-bg-color-container: #1d222b;
    --td-bg-color-secondarycontainer: #252b35;
    --td-text-color-primary: #e5e6eb;
    --td-text-color-secondary: #a9b1bd;
    --td-text-color-placeholder: #8a94a3;
    --td-component-border: #343c49;
    --td-error-color: #ff7a73;
    --td-success-color: #4fd37e;
    --td-warning-color: #f5a94a;
  }
  /* 全局 .card 白底硬值在本页改走可翻转 token */
  .card {
    background: var(--td-bg-color-container);
  }
  /* 表格/列表滚动区深色细滚动条 */
  .tbl-wrap,
  .pm-list,
  .pm-apps {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .tbl-wrap::-webkit-scrollbar-thumb,
  .pm-list::-webkit-scrollbar-thumb,
  .pm-apps::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
  .tbl-wrap::-webkit-scrollbar-thumb:hover,
  .pm-list::-webkit-scrollbar-thumb:hover,
  .pm-apps::-webkit-scrollbar-thumb:hover {
    background: rgba(255, 255, 255, 0.34);
  }
}

/* 全局 .card 白底硬值在本页改走可翻转 token（浅色观感不变） */
.card {
  background: var(--td-bg-color-container);
}

/* 页内原生输入基线（员工搜索 / 排序数字格；旧版 .input 口径：发丝边 + 8px 圆角 + token 底） */
.input {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--td-bg-color-container);
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.input::placeholder {
  color: var(--tk-ph);
}
.input:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--td-brand-color-focus);
}

/* ===== 页签（滑动指示条，同旧版 admin.html） ===== */
.tabs {
  position: relative;
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--tk-line);
}
.tab {
  padding: 11px 20px;
  border: none;
  background: none;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 1.5px;
  transition: color 0.25s;
}
.tab:hover {
  color: var(--tk-brand);
}
.tab.on {
  color: var(--tk-brand);
}
.tab-ind {
  position: absolute;
  bottom: -1px;
  left: 0;
  width: 0;
  height: 3px;
  border-radius: 8px;
  background: var(--tk-brand);
}
.tab-ind.ready {
  transition:
    left 0.3s var(--tk-ease),
    width 0.3s var(--tk-ease);
}

/* 入场动效（一次编排式上浮；reduce 时关闭）。
   行级入场随页签切换（.pane-in :key 重挂载）真实重放，不做循环/假动画 */
.page-entry {
  animation: admFadeUp 0.55s var(--tk-ease) both;
}
.pane-in {
  animation: admFadeUp 0.34s var(--tk-ease) both;
}
.tbl tbody tr {
  animation: admRise 0.3s var(--tk-ease) both;
}
.tbl tbody tr:nth-child(2) {
  animation-delay: 0.04s;
}
.tbl tbody tr:nth-child(3) {
  animation-delay: 0.08s;
}
.tbl tbody tr:nth-child(4) {
  animation-delay: 0.12s;
}
.tbl tbody tr:nth-child(5) {
  animation-delay: 0.16s;
}
.tbl tbody tr:nth-child(6) {
  animation-delay: 0.2s;
}
.tbl tbody tr:nth-child(n + 7) {
  animation-delay: 0.24s;
}
@keyframes admFadeUp {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes admRise {
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
  .page-entry,
  .pane-in,
  .tbl tbody tr {
    animation: none !important;
  }
  .tab-ind,
  .tab-ind.ready {
    transition: none !important;
  }
}

/* ===== 工具条 ===== */
.toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 20px 0 14px;
}
.toolbar .cnt {
  font-size: 12.5px;
  color: var(--tk-dash);
  letter-spacing: 1px;
}
.toolbar .sp {
  margin-left: auto;
  display: flex;
  gap: 10px;
}

/* ===== 表格（同旧版 .tbl 密度：13px/16px、表头浅底 12px 加粗） ===== */
.tbl-card {
  width: 100%;
  overflow: hidden;
  padding: 0;
}
/* 表格/列表滚动区：细滚动条 + 稳定占位，出现滚动条时内容不偏移、不被挤偏 */
.tbl-wrap,
.pm-list,
.pm-apps {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.tbl-wrap::-webkit-scrollbar,
.pm-list::-webkit-scrollbar,
.pm-apps::-webkit-scrollbar {
  width: 5px;
  height: 5px;
}
.tbl-wrap::-webkit-scrollbar-thumb,
.pm-list::-webkit-scrollbar-thumb,
.pm-apps::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.tbl-wrap::-webkit-scrollbar-thumb:hover,
.pm-list::-webkit-scrollbar-thumb:hover,
.pm-apps::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.tbl-wrap::-webkit-scrollbar-track,
.pm-list::-webkit-scrollbar-track,
.pm-apps::-webkit-scrollbar-track {
  background: transparent;
}
.tbl-wrap {
  overflow-x: auto;
}
table.tbl {
  width: 100%;
  border-collapse: collapse;
  min-width: 760px;
}
.tbl th,
.tbl td {
  padding: 13px 16px;
  text-align: left;
  font-size: 13px;
  border-bottom: 1px solid var(--tk-line);
  white-space: nowrap;
}
.tbl th {
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 1.5px;
  background: var(--tk-fill);
}
.tbl tbody tr:last-child td {
  border-bottom: none;
}
.tbl tbody tr {
  transition: background 0.2s;
}
.tbl tbody tr:hover {
  background: var(--tk-fill);
}
.tbl .uname {
  font-weight: 700;
}
.tbl .mut {
  color: var(--tk-ph);
}

/* 员工表按班组分组（「全部班组」视图）：组标题行 = 蓝条 + 班组名 + 人数 */
.grp-row td {
  padding: 9px 16px;
  background: var(--tk-fill);
  font-size: 12px;
  font-weight: 700;
  color: var(--tk-brand);
  letter-spacing: 1px;
}
.grp-row td::before {
  content: '';
  display: inline-block;
  width: 4px;
  height: 12px;
  margin-right: 8px;
  border-radius: 8px;
  background: var(--tk-brand);
  vertical-align: -1px;
}
.grp-row .grp-cnt {
  margin-left: 8px;
  font-weight: 500;
  color: var(--tk-ph);
}

/* 行内操作按钮（同旧版 .act） */
.act {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 5px 9px;
  border: none;
  border-radius: 8px;
  background: none;
  cursor: pointer;
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
  transition:
    color 0.2s,
    background 0.2s;
}
.act:hover {
  color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.act.danger:hover {
  color: var(--tk-red);
  background: var(--tk-red-soft);
}
.act.primary {
  color: var(--tk-brand);
}
.act[disabled] {
  opacity: 0.45;
  cursor: not-allowed;
}
.act[disabled]:hover {
  color: var(--tk-sub);
  background: none;
}

/* ===== 标签（4px 小胶囊；同旧版 .tag / .badge-outline） ===== */
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
.tag-orange {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.tag-navy {
  background: var(--tk-fill);
  color: var(--tk-sub);
}
.tag-red {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.role-admin {
  background: var(--tk-brand);
  color: var(--on-brand);
}
.badge-outline {
  display: inline-block;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
  letter-spacing: 0.5px;
}

/* 默认班组徽章（首个启用班组） */
.def-pill {
  display: inline-block;
  margin-left: 6px;
  border-radius: 8px;
  padding: 1px 7px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.5px;
  vertical-align: 1px;
}

/* 启用状态开关（班组编辑弹层 / 应用启停，同旧版 .switch；表格单元格内垂直居中不压基线） */
.switch {
  position: relative;
  display: inline-block;
  width: 46px;
  height: 26px;
  border: none;
  border-radius: 8px;
  background: var(--tk-dash);
  cursor: pointer;
  transition: background 0.25s;
  padding: 0;
  vertical-align: middle;
}
.switch::after {
  content: '';
  position: absolute;
  top: 3px;
  left: 3px;
  width: 20px;
  height: 20px;
  border-radius: 8px;
  background: #fff;
  box-shadow: var(--tk-shadow-card);
  transition: left 0.25s;
}
.switch.on {
  background: var(--tk-brand);
}
.switch.on::after {
  left: 23px;
}

/* ===== 权限管理：左应用矩阵 / 右员工列表 ===== */
.perm-wrap {
  display: grid;
  grid-template-columns: 1fr 300px;
  gap: 18px;
  align-items: start;
  margin-top: 20px;
}
.pm-side {
  overflow: hidden;
  padding: 0;
}
.pm-search {
  padding: 14px 14px 10px;
  border-bottom: 1px solid var(--tk-line);
}
.pm-search .field {
  position: relative;
}
.pm-search .field > svg {
  position: absolute;
  left: 15px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--tk-ph);
  pointer-events: none;
}
.pm-search .field .input {
  height: 40px;
  padding-left: 44px;
}
.pm-list {
  max-height: 520px;
  overflow-y: auto;
  padding: 8px;
}
.pm-user {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 10px;
  border: none;
  border-radius: 8px;
  background: none;
  cursor: pointer;
  text-align: left;
  transition: background 0.2s;
}
.pm-user:hover {
  background: var(--tk-fill);
}
.pm-user.on {
  background: var(--tk-blue-soft);
}
.pm-ava {
  width: 36px;
  height: 36px;
  flex: none;
  border-radius: 8px;
  background: var(--tk-brand);
  color: var(--on-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 600;
}
.pm-meta {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.pm-user .nm {
  font-size: 13.5px;
  font-weight: 700;
  color: var(--tk-ink);
}
.pm-user .un {
  font-size: 11.5px;
  color: var(--tk-ph);
  margin-top: 1px;
}
/* 权限管理左栏分组标签（蓝条口径同员工表组标题） */
.pm-grp {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 10px 4px;
  font-size: 11px;
  font-weight: 700;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.pm-grp::before {
  content: '';
  width: 3px;
  height: 10px;
  border-radius: 8px;
  background: var(--tk-brand);
}
.pm-main {
  overflow: hidden;
  padding: 0;
}
.pm-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 15px 20px;
  border-bottom: 1px solid var(--tk-line);
  font-size: 13px;
  color: var(--tk-sub);
  letter-spacing: 0.5px;
}
.pm-head b {
  color: var(--tk-ink);
}
.pm-head .st {
  margin-left: auto;
  font-size: 12px;
  color: var(--tk-ph);
}
.pm-apps {
  padding: 10px 14px 16px;
  max-height: 560px;
  overflow-y: auto;
}
.app-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 12px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.2s;
}
.app-item:hover {
  background: var(--tk-fill);
}
.app-item .cbx {
  width: 19px;
  height: 19px;
  flex: none;
  border-radius: 8px;
  border: 1.5px solid var(--tk-line);
  background: var(--td-bg-color-container);
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    background 0.2s,
    border-color 0.2s;
}
.app-item .cbx svg {
  color: var(--on-brand);
  opacity: 0;
  transition: opacity 0.15s;
}
.app-item.on .cbx {
  background: var(--tk-brand);
  border-color: var(--tk-brand);
}
.app-item.on .cbx svg {
  opacity: 1;
}
.app-item .ai-ic {
  width: 38px;
  height: 38px;
  flex: none;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.app-item .ai-txt {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.app-item .ai-nm {
  font-size: 13.5px;
  font-weight: 700;
}
.app-item .ai-key {
  font-size: 11.5px;
  color: var(--tk-ph);
  margin-top: 1px;
}
.app-item.off-app {
  opacity: 0.45;
}
.app-item .ai-state {
  margin-left: auto;
  font-size: 11px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
.ai-inline {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.ai-inline .ai-ic {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

/* 应用表排序输入 */
.cell-num {
  width: 90px;
  height: 32px;
  padding: 0 10px;
  font-variant-numeric: tabular-nums;
}

/* ===== 弹层表单（旧版 .f-row / .tip / .f-note 口径） ===== */
.tip {
  font-size: 13px;
  color: var(--tk-sub);
  line-height: 1.8;
  margin-bottom: 14px;
}
.f-row {
  margin-bottom: 14px;
}
.f-row label {
  display: block;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--tk-sub);
  letter-spacing: 1px;
  margin-bottom: 7px;
}
.f-row label i {
  font-style: normal;
  color: var(--tk-ph);
  font-weight: 500;
}
.f-input {
  height: 42px;
}
.f-note {
  font-size: 11.5px;
  color: var(--tk-ph);
  line-height: 1.8;
  margin: 4px 0 8px;
}
.link-danger {
  margin-right: auto;
  border: none;
  background: none;
  cursor: pointer;
  padding: 4px 0;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--tk-red);
  letter-spacing: 1px;
}
.link-danger:hover {
  text-decoration: underline;
}
.ft-spacer {
  flex: 1;
}

/* ===== 消息推送（旧版 .push-card 口径） ===== */
.push-card {
  margin-top: 20px;
  padding: 22px 24px 16px;
  max-width: 640px;
}
.push-card .input {
  width: 100%;
  height: 44px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--td-bg-color-container);
  padding: 0 14px;
  font-size: 14px;
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.push-card .input::placeholder {
  color: var(--tk-ph);
}
.push-card .input:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--td-brand-color-focus);
}
.push-card textarea.input {
  height: auto;
  min-height: 140px;
  padding: 10px 14px;
  resize: vertical;
  line-height: 1.7;
}
.push-targets {
  display: flex;
  align-items: center;
  gap: 22px;
  flex-wrap: wrap;
  min-height: 42px;
}
.push-targets .push-ck {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 0;
  font-size: 13.5px;
  font-weight: 400;
  cursor: pointer;
}
.push-ck input {
  width: 16px;
  height: 16px;
  margin: 0;
  accent-color: var(--tk-brand);
  cursor: pointer;
}
.push-tools {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
.push-ft {
  display: flex;
  justify-content: flex-end;
}
/* Markdown 预览区（对齐通知弹层排版） */
.push-preview {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 10px 14px;
  min-height: 96px;
  font-size: 13.5px;
  line-height: 1.8;
  color: var(--tk-ink);
  background: var(--tk-fill);
  word-break: break-word;
}
.push-preview :deep(h1),
.push-preview :deep(h2),
.push-preview :deep(h3),
.push-preview :deep(h4) {
  font-size: 15px;
  font-weight: 700;
  margin: 10px 0 6px;
}
.push-preview :deep(p) {
  margin: 4px 0;
}
.push-preview :deep(ul),
.push-preview :deep(ol) {
  margin: 4px 0 4px 1.2em;
  padding: 0;
}
.push-preview :deep(li) {
  margin: 2px 0;
}
.push-preview :deep(code) {
  font-family: Menlo, Consolas, monospace;
  font-size: 0.92em;
  color: var(--tk-brand);
}
.push-preview :deep(pre) {
  background: var(--tk-bg);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 10px;
  margin: 8px 0;
  overflow-x: auto;
}
.push-preview :deep(blockquote) {
  border-left: 3px solid var(--tk-brand);
  margin: 6px 0;
  padding: 2px 10px;
  color: var(--tk-sub);
  background: var(--tk-bg);
}
.push-preview :deep(a) {
  color: var(--tk-brand);
}
.push-preview :deep(hr) {
  border: none;
  border-top: 1px solid var(--tk-line);
  margin: 10px 0;
}
.push-preview :deep(table) {
  border-collapse: collapse;
  margin: 8px 0;
  font-size: 13px;
}
.push-preview :deep(th),
.push-preview :deep(td) {
  border: 1px solid var(--tk-line);
  padding: 5px 9px;
}
.push-preview :deep(th) {
  background: var(--tk-bg);
}
.push-preview :deep(img) {
  display: block;
  max-width: 100%;
  border-radius: 8px;
  margin: 6px 0;
}

/* ===== 空态 / 加载（同旧版 .empty / .loading / .spin） ===== */
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--tk-ph);
}
.empty svg {
  color: var(--tk-dash);
}
.empty p {
  font-size: 13px;
  letter-spacing: 1px;
}
.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--tk-ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}
.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: admRot 1s linear infinite;
}
@keyframes admRot {
  to {
    transform: rotate(360deg);
  }
}

/* ===== 无权限 ===== */
.noperm {
  max-width: 460px;
  margin: 90px auto 0;
  text-align: center;
  padding: 46px 30px;
}
.noperm .np-ic {
  width: 64px;
  height: 64px;
  margin: 0 auto 18px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.noperm h3 {
  font-size: 17px;
  letter-spacing: 1px;
  margin: 0 0 10px;
}
.noperm p {
  margin: 10px 0 22px;
  font-size: 13px;
  color: var(--tk-sub);
  line-height: 1.8;
}

/* 窄屏：权限页单栏堆叠，员工选择列仍居上（同旧版 ≤860px 口径） */
@media (max-width: 860px) {
  .perm-wrap {
    grid-template-columns: 1fr;
  }
  .pm-side {
    order: -1;
  }
  .pm-list,
  .pm-apps {
    max-height: none;
  }
}
</style>
