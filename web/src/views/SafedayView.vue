<template>
  <div>
    <!-- 未分配班组整页空态（非超管且无班组，屏十口径） -->
    <div v-if="noTeam" class="card sd-card-flat">
      <div class="sd-empty">
        <svg
          viewBox="0 0 24 24"
          width="44"
          height="44"
          fill="none"
          stroke="currentColor"
          stroke-width="1.7"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <circle cx="9" cy="8.5" r="3.2" />
          <path d="M3.8 19a5.2 5.2 0 0 1 10.4 0" />
          <path d="M15.5 5.6a3.2 3.2 0 0 1 0 5.8" />
          <path d="M17 14.2a5.2 5.2 0 0 1 3.3 4.8" />
        </svg>
        <p><b>未分配班组</b></p>
        <p class="sd-empty-sub">
          你的账号尚未加入任何班组，暂无可查看的数据。<br />请联系超级管理员分配班组后再使用。
        </p>
      </div>
    </div>

    <template v-else>
      <!-- 板块标题行：标题 + 说明 + 超管班组切换 -->
      <div class="sd-head">
        <h3>安全日活动记录</h3>
        <span class="more">上传活动文件 · AI 自动生成活动记录</span>
        <select
          v-if="teams.length"
          v-model="teamId"
          class="sd-team-sel"
          title="仅超级管理员可切换班组；其余角色固定为本班组"
          @change="switchTeam"
        >
          <option v-if="allowAll" value="all">全部班组</option>
          <option v-for="t in teams" :key="t.id" :value="String(t.id)">{{ t.name }}</option>
        </select>
      </div>

      <div v-if="demoMode" class="demo-banner">
        <strong>演示状态</strong>
        <span
          >记录列表为静态演示数据；生成/删除/存网盘不执行、不伪造成功。切换 live 适配层后对接 Go
          API。</span
        >
      </div>

      <div class="sd-body">
        <!-- 右窄栏：上传区 + 已选文件 + 生成参数 + 生成进度 -->
        <div class="sd-side">
          <div
            class="sd-drop"
            :class="{ over: dragOver }"
            tabindex="0"
            role="button"
            aria-label="选择或拖拽文件"
            @click="fileInput && fileInput.click()"
            @keydown.enter.prevent="fileInput && fileInput.click()"
            @keydown.space.prevent="fileInput && fileInput.click()"
            @dragenter.prevent="onDragEnter"
            @dragover.prevent
            @dragleave.prevent="onDragLeave"
            @drop.prevent="onDrop"
          >
            <div class="sd-drop-ic">
              <svg
                viewBox="0 0 24 24"
                width="28"
                height="28"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path d="M12 15.5V7.5M8.8 10.7L12 7.5l3.2 3.2" />
                <path
                  d="M6.8 15.9a4 4 0 0 1-1-7.9A5.5 5.5 0 0 1 16.5 6.4 4.6 4.6 0 0 1 17.2 15.9"
                />
              </svg>
            </div>
            <b>点击选择文件，或拖拽到此处<i>上传</i></b>
            <p>
              支持 pdf / doc / docx / ppt / pptx / xls / xlsx<br />单文件 ≤ 50MB，最多 10
              个；多文件自动合并（非 PDF 由服务端转换）
            </p>
          </div>
          <input
            ref="fileInput"
            type="file"
            hidden
            multiple
            accept=".pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx"
            @change="onFilesPicked"
          />

          <!-- 已选文件条（逐行紧凑，可单条移除；新条目一次上浮入场） -->
          <transition-group name="sd-ftag" tag="div" class="sd-ftags">
            <div v-for="(f, i) in files" :key="f.name" class="sd-ftag">
              <svg
                viewBox="0 0 24 24"
                width="15"
                height="15"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z" />
                <path d="M13.5 3v4.5H19" />
                <path d="M9 13h6M9 17h6" />
              </svg>
              <span class="sd-ftag-nm" :title="f.name">{{ f.name }}</span>
              <span class="sd-ftag-sz num">{{ fmtSize(f.size) }}</span>
              <button
                type="button"
                class="sd-ftag-x"
                :aria-label="'移除 ' + f.name"
                @click="files.splice(i, 1)"
              >
                ×
              </button>
            </div>
          </transition-group>

          <!-- 生成参数卡 -->
          <div class="card sd-gen">
            <label class="sd-lab" for="sdDate">活动日期</label>
            <!-- 整行可点开日期选择器（原生 input 仅点图标可开，补 showPicker 让文字区也能点开） -->
            <div class="sd-date-row" @click="openDatePicker">
              <input
                id="sdDate"
                ref="dateInput"
                v-model="dateISO"
                type="date"
                class="sd-input num"
              />
            </div>
            <button
              type="button"
              class="sd-btn sd-btn-block"
              :disabled="!files.length"
              @click="openGenMask"
            >
              生成记录
            </button>
          </div>

          <!-- 生成进度卡 -->
          <div class="card sd-prog" :class="{ idle: !track }">
            <template v-if="!track">
              <svg
                viewBox="0 0 24 24"
                width="26"
                height="26"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <circle cx="12" cy="12" r="8.5" />
                <path d="M12 7.5V12l3.5 2" />
              </svg>
              <span>提交生成后，这里将实时展示生成进度</span>
            </template>
            <template v-else>
              <div class="sd-prog-fh">
                <div class="sd-file-ic">
                  <svg
                    viewBox="0 0 24 24"
                    width="18"
                    height="18"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.7"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  >
                    <path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z" />
                    <path d="M13.5 3v4.5H19" />
                    <path d="M9 13h6M9 17h6" />
                  </svg>
                </div>
                <div>
                  <div class="sd-prog-fn">{{ track.name }}</div>
                  <div class="sd-prog-fs num">
                    {{ track.sourceCount || 1 }} 个源文件 · 提交于
                    {{ fmtDate(track.createdAt) || '—' }}
                  </div>
                </div>
              </div>
              <div class="sd-steps">
                <div v-for="s in stepRows" :key="s.title" class="sd-step" :class="s.state">
                  <span class="sd-step-ic">
                    <svg
                      v-if="s.state === 'done'"
                      viewBox="0 0 24 24"
                      width="11"
                      height="11"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="3.2"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    >
                      <path d="M5 12.5l4.5 4.5L19 7.5" />
                    </svg>
                    <svg
                      v-else-if="s.state === 'fail'"
                      viewBox="0 0 24 24"
                      width="10"
                      height="10"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="3.2"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    >
                      <path d="M6 6l12 12M18 6L6 18" />
                    </svg>
                    <i v-else-if="s.state === 'doing'"></i>
                  </span>
                  <div>
                    <div class="sd-step-t">{{ s.title }}</div>
                    <div class="sd-step-s">{{ s.sub }}</div>
                  </div>
                </div>
              </div>
              <div class="sd-pbar" :class="{ fail: track.status === 'failed' }">
                <i :style="{ width: progPct + '%' }"></i>
              </div>
              <div class="sd-pnum num" :class="{ fail: track.status === 'failed' }">
                <span>{{ track.status === 'failed' ? '生成失败' : '生成进度' }}</span>
                <b>{{ progPct }}%</b>
              </div>
            </template>
          </div>
        </div>

        <!-- 左侧主列：活动记录列表 -->
        <div class="card sd-tbl">
          <div class="sd-tbl-h">
            <b>活动记录列表</b>
            <button
              type="button"
              class="sd-btn sd-btn-sm sd-refresh"
              @click="refreshRecords(false)"
            >
              <svg
                viewBox="0 0 24 24"
                width="15"
                height="15"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
                :class="{ 'sd-rot': refreshing }"
              >
                <path d="M20 12a8 8 0 1 1-2.4-5.7" />
                <path d="M20 3.5v4h-4" />
              </svg>
              刷新
            </button>
            <span class="cnt num"
              >共 {{ lastRecords.length }} 条{{
                processingCount ? ` · ${processingCount} 条生成中` : ''
              }}</span
            >
          </div>

          <div v-if="recordsLoading && !loadedOnce" class="sd-loading">
            <span class="sd-spin"></span><span>正在加载记录…</span>
          </div>
          <template v-else>
            <!-- 表格滚动区：窄宽时横向滚动，不裁切操作列；细滚动条 + gutter 防内容偏移 -->
            <div class="sd-tbl-scroll">
              <table class="sd-rtable">
                <thead>
                  <tr>
                    <th>记录文件</th>
                    <th class="sd-col-date">活动日期</th>
                    <th v-if="teamId === 'all'" class="sd-col-team">班组</th>
                    <th class="sd-col-time">提交时间</th>
                    <th class="sd-col-state">状态</th>
                    <th class="sd-col-op">操作</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="rec in lastRecords"
                    :key="rec.id"
                    :class="{
                      'sd-row-prev': rec.status === 'done',
                      'sd-row-new': newIds.has(String(rec.id)),
                    }"
                    :title="rec.status === 'done' ? '点击预览' : ''"
                    @click="rec.status === 'done' && previewRecordAction(rec)"
                  >
                    <td>
                      <div class="sd-rn">{{ rec.fileName || '—' }}</div>
                      <div class="sd-rf">
                        <svg
                          viewBox="0 0 24 24"
                          width="12"
                          height="12"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="1.7"
                          stroke-linecap="round"
                          stroke-linejoin="round"
                        >
                          <path
                            d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"
                          />
                          <path d="M13.5 3v4.5H19" />
                          <path d="M9 13h6M9 17h6" />
                        </svg>
                        <span>{{ recSub(rec) }}</span>
                      </div>
                    </td>
                    <td class="sd-col-date sd-mut num">{{ rec.date || '—' }}</td>
                    <td v-if="teamId === 'all'" class="sd-col-team">
                      <span v-if="rec.team" class="sd-tag sd-tag-navy">{{ rec.team }}</span>
                      <span v-else class="sd-mut">—</span>
                    </td>
                    <td class="sd-col-time sd-mut num">{{ fmtDate(rec.createdAt) || '—' }}</td>
                    <td class="sd-col-state">
                      <span v-if="rec.status === 'done'" class="sd-tag sd-tag-orange">已生成</span>
                      <span
                        v-else-if="rec.status === 'failed'"
                        class="sd-tag sd-tag-red"
                        :title="rec.error || '生成失败'"
                        >生成失败</span
                      >
                      <span v-else class="sd-tag sd-tag-navy"
                        ><span class="sd-pulse-dot"></span>生成中</span
                      >
                    </td>
                    <td class="sd-col-op" @click.stop>
                      <div class="sd-op">
                        <button
                          type="button"
                          class="sd-opb"
                          title="下载记录文件"
                          :disabled="rec.status !== 'done'"
                          @click="downloadRecordAction(rec)"
                        >
                          <svg
                            viewBox="0 0 24 24"
                            width="15"
                            height="15"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.7"
                            stroke-linecap="round"
                            stroke-linejoin="round"
                          >
                            <path d="M12 3.5v11M7 10l5 5 5-5" />
                            <path d="M4.5 19.5h15" />
                          </svg>
                        </button>
                        <button
                          v-if="rec.status === 'done' && netdiskOk"
                          type="button"
                          class="sd-opb"
                          title="存网盘"
                          @click="openSaveNetdisk(rec)"
                        >
                          <svg
                            viewBox="0 0 24 24"
                            width="15"
                            height="15"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.7"
                            stroke-linecap="round"
                            stroke-linejoin="round"
                          >
                            <path
                              d="M17.8 19.5a4.3 4.3 0 0 0 .4-8.58A6 6 0 0 0 6.5 9.7 4.8 4.8 0 0 0 6.7 19.5z"
                            />
                          </svg>
                        </button>
                        <button
                          type="button"
                          class="sd-opb sd-opb-del"
                          title="删除记录"
                          @click="openDelMask(rec)"
                        >
                          <svg
                            viewBox="0 0 24 24"
                            width="15"
                            height="15"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.7"
                            stroke-linecap="round"
                            stroke-linejoin="round"
                          >
                            <path d="M4 7h16M10 11v6M14 11v6" />
                            <path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7" />
                            <path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7" />
                          </svg>
                        </button>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="!lastRecords.length" class="sd-empty">
              <svg
                viewBox="0 0 24 24"
                width="44"
                height="44"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z" />
                <path d="M13.5 3v4.5H19" />
                <path d="M12 11l2.4.9v1.8c0 1.5-1 2.5-2.4 3.2-1.4-.7-2.4-1.7-2.4-3.2v-1.8z" />
                <path d="M10.9 14.1l.8.8 1.4-1.5" />
              </svg>
              <p>暂无生成记录，上传活动文件开始生成</p>
            </div>
          </template>
        </div>
      </div>
    </template>

    <!-- ===== 弹层：生成表单（学习内容 + 参会信息，确认后开始生成；复杂多字段表单走右侧抽屉） ===== -->
    <Drawer
      :visible="genMask"
      title="生成活动记录"
      :width="560"
      @update:visible="(v) => (genMask = v)"
    >
      <p class="sd-dsub">确认学习内容与参会信息后开始生成；上级参加人员与记录人将记忆为下次默认</p>

      <label class="sd-lab" for="sdName">学习内容（记录名称）</label>
      <textarea
        id="sdName"
        ref="nameInput"
        v-model="form.name"
        class="sd-input sd-name-area"
        rows="5"
      ></textarea>

      <div class="sd-mode-row">
        <label class="sd-lab">参会人员</label>
        <span class="sd-mswitch">
          <button
            type="button"
            class="sd-ms-opt"
            :class="{ on: attendMode === 'member' }"
            @click="setAttendMode('member')"
          >
            名单选择
          </button>
          <button
            type="button"
            class="sd-ms-opt"
            :class="{ on: attendMode === 'custom' }"
            @click="setAttendMode('custom')"
          >
            自定义
          </button>
        </span>
      </div>

      <div class="sd-2col">
        <div>
          <label class="sd-lab">主持人</label>
          <select v-if="attendMode === 'member'" v-model="form.host" class="sd-input">
            <option v-for="m in members" :key="m" :value="m">{{ m }}</option>
          </select>
          <input
            v-else
            v-model="form.hostCustom"
            class="sd-input"
            type="text"
            autocomplete="off"
            maxlength="64"
            placeholder="请输入主持人"
          />
        </div>
        <div>
          <label class="sd-lab" for="sdRecorder">记录人</label>
          <select id="sdRecorder" v-model="form.recorder" class="sd-input">
            <option v-for="m in members" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>
      </div>

      <label class="sd-lab" for="sdSuperior">上级参加人员</label>
      <input
        id="sdSuperior"
        v-model="form.superior"
        class="sd-input"
        type="text"
        autocomplete="off"
      />

      <template v-if="attendMode === 'member'">
        <label class="sd-lab">本班组参加人员 <small>默认全员参加，点击名字改为缺席</small></label>
        <div class="sd-mchips">
          <button
            v-for="m in members"
            :key="'a' + m"
            type="button"
            class="sd-mchip"
            :class="{ on: attendSet.has(m) }"
            @click="toggleAttend(m)"
          >
            {{ m }}
          </button>
          <span v-if="!members.length" class="sd-chips-loading">成员加载中…</span>
        </div>
        <label class="sd-lab">缺席人员 <small>与参加人员互斥，点击名字改为参加</small></label>
        <div class="sd-mchips sd-absent">
          <button
            v-for="m in members"
            :key="'b' + m"
            type="button"
            class="sd-mchip"
            :class="{ on: !attendSet.has(m) }"
            @click="toggleAttend(m)"
          >
            {{ m }}
          </button>
        </div>
      </template>

      <template v-else>
        <label class="sd-lab" for="sdAttend"
          >本班组参加人员 <small>多人以空格或顿号分隔</small></label
        >
        <input
          id="sdAttend"
          v-model="form.attendeesCustom"
          class="sd-input"
          type="text"
          autocomplete="off"
          maxlength="512"
          placeholder="如：张三 李四"
        />
        <label class="sd-lab" for="sdAbsent"
          >缺席人员 <small>多人以空格或顿号分隔，无则留空</small></label
        >
        <input
          id="sdAbsent"
          v-model="form.absenteesCustom"
          class="sd-input"
          type="text"
          autocomplete="off"
          maxlength="512"
          placeholder="无"
        />
      </template>

      <label class="sd-lab" for="sdAbsentReason">缺席人员原因</label>
      <input
        id="sdAbsentReason"
        v-model="form.absentReason"
        class="sd-input"
        type="text"
        placeholder="无"
        autocomplete="off"
      />

      <div class="sd-actions">
        <button type="button" class="sd-btn" :disabled="submitting" @click="closeGenMask">
          取消
        </button>
        <button
          type="button"
          class="sd-btn sd-btn-primary"
          :disabled="submitting"
          @click="submitGenerate"
        >
          {{ submitting ? '正在生成…' : '确定，开始生成' }}
        </button>
      </div>
    </Drawer>

    <!-- ===== 弹层：删除确认 ===== -->
    <div v-if="delRendered" class="sd-mask ov-mask" :class="delPhase">
      <div class="sd-dialog ov-panel" role="dialog" aria-modal="true" aria-labelledby="sdDelTitle">
        <div class="sd-d-ic">
          <svg
            viewBox="0 0 24 24"
            width="22"
            height="22"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <path d="M4 7h16M10 11v6M14 11v6" />
            <path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7" />
            <path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7" />
          </svg>
        </div>
        <h3 id="sdDelTitle">删除这条记录？</h3>
        <p class="sd-dsub">
          记录「{{
            delTarget ? delTarget.name : ''
          }}」及已生成的记录文件将一并删除，该操作不可恢复。
        </p>
        <div class="sd-actions">
          <button type="button" class="sd-btn" :disabled="deleting" @click="delMask = false">
            取消
          </button>
          <button
            type="button"
            class="sd-btn sd-btn-danger"
            :disabled="deleting"
            @click="confirmDelete"
          >
            {{ deleting ? '删除中…' : '确认删除' }}
          </button>
        </div>
      </div>
    </div>

    <!-- ===== 弹层：存网盘目录选择（迁移旧页 ShadeNdPick 浏览式选目录） ===== -->
    <DirPicker
      v-model:visible="ndVisible"
      default-path="安全日记录"
      title="保存到网盘：选择目录"
      confirm-text="确定"
      @confirm="confirmSaveNetdisk"
    />
  </div>
</template>

<script setup>
// 安全日活动记录（迁移 server/public/safeday.html）：多文件拖传 → multipart 生成 →
// 记录列表（下载/预览/存网盘/删除）。布局为右窄栏（上传/生成参数/进度卡）+ 左记录表。
// 生成进度卡四步时间线状态一律来自 /records 真实轮询（5s），不以定时器伪造云端步骤相位。
// 存网盘走 DirPicker 浏览式目录选择（迁移旧页 assets/ndpick.js）。
// 超管班组切换依赖 GET /api/v1/admin/teams，端点缺失时静默隐藏切换器。
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import DirPicker from '../features/safeday/DirPicker.vue';
import Drawer from '../components/Drawer.vue';
import { useOverlayMotion } from '../components/overlay-motion.js';
// 进出场动效样式（全局注入一次，类名 ov-* 自带命名空间）
import '../components/overlay-motion.css';
import {
  isDemo,
  formMeta as fetchFormMeta,
  generate,
  records as fetchRecords,
  downloadRecord,
  previewRecord,
  saveRecordToNetdisk,
  deleteRecord,
  probeNetdisk,
  listTeams,
  saveBlob,
  previewRoute,
} from '../features/safeday/api.js';

const router = useRouter();
const demoMode = isDemo();

const POLL_INTERVAL = 5000;
const MAX_FILES = 10;
const MAX_SIZE = 50 * 1024 * 1024;
const ALLOWED = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];

// ---- 工具 ----
function extOf(name) {
  return name.split('.').pop().toLowerCase();
}
function baseOf(name) {
  return name.replace(/\.[^.]+$/, '');
}
function toDots(iso) {
  return String(iso || '').replace(/-/g, '.');
}
function todayISO() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
function fmtSize(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return Math.round(n / 1024) + ' KB';
  return `${n || 0} B`;
}
function fmtDate(d) {
  if (!d) return '';
  const dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  if (Number.isNaN(dt.getTime())) return String(d);
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}
// 自定义人员输入规范化：空格/顿号/逗号/分号分隔统一为空格分隔（服务端模板按空格分隔渲染）
function normNames(str) {
  return String(str || '')
    .split(/[\s、，,；;]+/)
    .filter(Boolean)
    .join(' ');
}

// ---- 上传区（类型/大小/数量/去重批量校验） ----
const fileInput = ref(null);
const files = ref([]);
const dragOver = ref(false);
const dateISO = ref(todayISO());
const dateInput = ref(null);
let dragDepth = 0;

// 日期文字区点击也唤起原生选择器（input[type=date] 默认仅点图标可开；不支持 showPicker 的浏览器自动忽略）
function openDatePicker() {
  try {
    dateInput.value?.showPicker?.();
  } catch (e) {
    /* 已在打开或浏览器不支持时忽略 */
  }
}

function addFiles(fileList) {
  let badType = 0;
  let badSize = 0;
  const incoming = [];
  Array.from(fileList || []).forEach((f) => {
    if (!ALLOWED.includes(extOf(f.name))) {
      badType++;
      return;
    }
    if (f.size > MAX_SIZE) {
      badSize++;
      return;
    }
    if (files.value.some((x) => x.name === f.name)) return;
    if (incoming.some((x) => x.name === f.name)) return;
    incoming.push(f);
  });
  const merged = files.value.concat(incoming);
  if (merged.length > MAX_FILES) {
    MessagePlugin.error(`最多上传 ${MAX_FILES} 个文件`);
    return;
  }
  files.value = merged;
  if (badType > 0) MessagePlugin.info(`已忽略 ${badType} 个不支持的文件`);
  if (badSize > 0) MessagePlugin.info(`已忽略 ${badSize} 个超过 50MB 的文件`);
}

function onFilesPicked(e) {
  addFiles(e.target.files);
  e.target.value = '';
}
// 拖传交互（dragDepth 计数防止子元素进出闪烁）
function onDragEnter() {
  dragDepth++;
  dragOver.value = true;
}
function onDragLeave() {
  dragDepth--;
  if (dragDepth <= 0) {
    dragDepth = 0;
    dragOver.value = false;
  }
}
function onDrop(e) {
  dragDepth = 0;
  dragOver.value = false;
  if (e.dataTransfer && e.dataTransfer.files) addFiles(e.dataTransfer.files);
}

// ---- 生成表单 ----
const genMask = ref(false);
const submitting = ref(false);
const attendMode = ref('member');
const members = ref([]);
const attendSet = ref(new Set());
const formMetaCache = ref(null);
const formMetaTeam = ref(null);
const nameInput = ref(null);

const form = reactive({
  name: '',
  host: '',
  hostCustom: '',
  recorder: '',
  superior: '',
  attendeesCustom: '',
  absenteesCustom: '',
  absentReason: '',
});

function toggleAttend(m) {
  const s = new Set(attendSet.value);
  if (s.has(m)) s.delete(m);
  else s.add(m);
  attendSet.value = s;
}

// 参会人员模式切换（名单点亮 / 自定义输入）；切到自定义时按当前名单选择预填主持人/参加/缺席
function setAttendMode(mode) {
  if (attendMode.value === mode) return;
  if (mode === 'member') {
    form.attendeesCustom = '';
    form.absenteesCustom = '';
    form.hostCustom = '';
  } else {
    form.hostCustom = form.host || '';
    form.attendeesCustom = members.value.filter((m) => attendSet.value.has(m)).join(' ');
    form.absenteesCustom = members.value.filter((m) => !attendSet.value.has(m)).join(' ');
  }
  attendMode.value = mode;
}

async function loadFormMeta() {
  const key = teamId.value;
  if (formMetaCache.value && formMetaTeam.value === key) return formMetaCache.value;
  const d = await fetchFormMeta(key && key !== 'all' ? key : undefined);
  formMetaCache.value = { members: d.members || [], defaults: d.defaults || {} };
  formMetaTeam.value = key;
  members.value = formMetaCache.value.members;
  return formMetaCache.value;
}

async function openGenMask() {
  // 超管「全部班组」视图不可生成：记录须归属具体班组（屏十三口径）
  if (teamId.value === 'all') {
    MessagePlugin.info('请先切换到具体班组再生成');
    return;
  }
  // 学习内容默认值：每个源文件各自带书名号（《文件1》、《文件2》）
  form.name = files.value.map((f) => `《${baseOf(f.name)}》`).join('、');
  setAttendMode('member');
  attendMode.value = 'member';
  genMask.value = true;
  setTimeout(() => {
    nameInput.value?.focus();
    nameInput.value?.select();
  }, 260);
  // 先开层再异步填表单：成员未到时 chips 区显示加载占位
  try {
    await loadFormMeta();
    const ms = members.value;
    form.host = ms[0] || '';
    form.recorder = formMetaCache.value.defaults.recorder || ms[0] || '';
    form.superior = formMetaCache.value.defaults.superior || '';
    form.absentReason = '';
    attendSet.value = new Set(ms); // 默认全员参加
  } catch (e) {
    MessagePlugin.error(e.message || '表单数据加载失败');
  }
}

function closeGenMask() {
  if (submitting.value) return;
  genMask.value = false;
}

async function submitGenerate() {
  if (submitting.value) return;
  const name = form.name.trim();
  if (!name) {
    MessagePlugin.error('请输入记录名称');
    nameInput.value?.focus();
    return;
  }
  if (!files.value.length) {
    MessagePlugin.error('请先选择文件');
    return;
  }
  const custom = attendMode.value === 'custom';
  const hostVal = custom ? form.hostCustom.trim() : form.host || '';
  const attendeesVal = custom
    ? normNames(form.attendeesCustom)
    : members.value.filter((m) => attendSet.value.has(m)).join(' ');
  const absenteesVal = custom
    ? normNames(form.absenteesCustom)
    : members.value.filter((m) => !attendSet.value.has(m)).join(' ');
  if (custom && !attendeesVal) {
    MessagePlugin.error('请填写本班组参加人员');
    return;
  }

  const fd = new FormData();
  fd.append('name', name);
  fd.append('date', toDots(dateISO.value || todayISO()));
  if (teamId.value && teamId.value !== 'all') fd.append('team_id', teamId.value);
  fd.append('host', hostVal);
  fd.append('recorder', form.recorder || '');
  fd.append('superior', form.superior.trim());
  fd.append('attendees', attendeesVal);
  fd.append('absentees', absenteesVal);
  fd.append('absentReason', form.absentReason.trim());
  files.value.forEach((f) => fd.append('files', f, f.name));

  submitting.value = true;
  try {
    const data = await generate(fd);
    // 本地缓存同步新默认值（上级参加人员/记录人按班组记忆），下次打开免重拉
    if (formMetaCache.value) {
      formMetaCache.value.defaults.superior =
        form.superior.trim() || formMetaCache.value.defaults.superior;
      formMetaCache.value.defaults.recorder =
        form.recorder || formMetaCache.value.defaults.recorder;
    }
    genMask.value = false;
    files.value = [];
    MessagePlugin.success('已开始生成，请稍候…');
    if (data.record) startTrack(data.record);
    refreshRecords(false);
  } catch (e) {
    MessagePlugin.error(e.message || '生成请求失败');
  } finally {
    submitting.value = false;
  }
}

// ---- 记录列表与轮询（有 processing 记录则 5s 轮询，全终态停止） ----
const lastRecords = ref([]);
const recordsLoading = ref(false);
const loadedOnce = ref(false);
const refreshing = ref(false);
const knownIds = ref(new Set());
const newIds = ref(new Set());
const netdiskOk = ref(false);
const processingCount = computed(
  () => lastRecords.value.filter((r) => r.status === 'processing').length
);

let pollTimer = null;

function stopPolling() {
  if (pollTimer) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}
function schedulePoll() {
  stopPolling();
  pollTimer = setTimeout(() => refreshRecords(false), POLL_INTERVAL);
}

function recSub(rec) {
  return rec.sources && rec.sources.length
    ? `《${rec.sources.join('、')}》`
    : `${rec.sourceCount || 1} 个源文件`;
}

async function refreshRecords(isInitial) {
  if (isInitial) recordsLoading.value = true;
  else refreshing.value = true;
  try {
    const data = await fetchRecords(teamId.value || undefined);
    const records = data.records || [];
    // 新记录播入场动画位：不在上一轮 knownIds 里的即新
    newIds.value = new Set(
      isInitial
        ? []
        : records.filter((r) => !knownIds.value.has(String(r.id))).map((r) => String(r.id))
    );
    knownIds.value = new Set(records.map((r) => String(r.id)));
    lastRecords.value = records;
    syncTrack(records);
    if (records.some((r) => r.status === 'processing')) schedulePoll();
    else stopPolling();
  } catch (e) {
    MessagePlugin.error(e.message || (isInitial ? '记录列表加载失败' : '记录刷新失败'));
    // 轮询中出错：仍按已知 processing 状态继续轮询，避免状态卡死
    if (!isInitial && lastRecords.value.some((r) => r.status === 'processing')) schedulePoll();
  } finally {
    recordsLoading.value = false;
    loadedOnce.value = true;
    refreshing.value = false;
  }
}

// ---- 生成进度卡（状态一律来自 /records 真实轮询，不以定时器伪造云端步骤相位） ----
const STEP_TEXTS = [
  ['上传完成', '文件已存至服务器'],
  ['内容解析', '提取活动主题与要点'],
  ['Dify 生成中', '正在按模板撰写活动记录…'],
  ['记录入库', '生成后自动加入记录列表'],
];
const track = ref(null); // {id,name,sourceCount,createdAt,status,error,notified}；status 与记录 status 同步

// stepStates / progPct 仅由真实状态映射：processing 稳态与旧页「Dify 生成中」相位同构，
// done 全勾、failed 停在 Dify 步；不再恢复旧页 parse→dify 的定时伪相位推进。
function stepStates(status) {
  if (status === 'done') return ['done', 'done', 'done', 'done'];
  if (status === 'failed') return ['done', 'done', 'fail', 'todo'];
  return ['done', 'done', 'doing', 'todo'];
}
const progPct = computed(() => {
  const t = track.value;
  if (!t) return 0;
  return t.status === 'done' ? 100 : 75;
});
const stepRows = computed(() => {
  const t = track.value;
  if (!t) return [];
  const states = stepStates(t.status);
  return STEP_TEXTS.map(([title, sub], i) => ({
    title,
    sub: states[i] === 'fail' ? t.error || '生成失败' : sub,
    state: states[i],
  }));
});

function startTrack(rec) {
  track.value = {
    id: String(rec.id),
    name: rec.name,
    sourceCount: rec.sourceCount,
    createdAt: rec.createdAt,
    status: rec.status || 'processing',
    error: '',
    notified: false,
  };
}

function syncTrack(records) {
  const t = track.value;
  if (t) {
    const rec = records.find((r) => String(r.id) === t.id);
    if (!rec) {
      track.value = null;
      return;
    } // 记录已被删除
    t.status = rec.status || 'processing';
    if (rec.status === 'done') {
      if (!t.notified) {
        t.notified = true;
        MessagePlugin.success('活动记录已生成，可在列表中下载');
      }
    } else if (rec.status === 'failed') {
      if (!t.notified) {
        t.notified = true;
        t.error = rec.error || '生成失败';
        MessagePlugin.error(t.error);
      }
    } else {
      t.error = '';
    }
    return;
  }
  // 页面刷新时若有生成中记录则接管跟踪（状态仍以轮询为准）
  const proc = records.find((r) => r.status === 'processing');
  if (proc) startTrack(proc);
}

// ---- 记录操作 ----
async function downloadRecordAction(rec) {
  try {
    const res = await downloadRecord(rec.id);
    await saveBlob(res, rec.fileName || '安全日活动记录.docx');
  } catch (e) {
    MessagePlugin.error(e.message || '下载失败');
  }
}

async function previewRecordAction(rec) {
  try {
    const data = await previewRecord(rec.id);
    if (!data.url) throw new Error('预览地址为空');
    const r = previewRoute(data.url);
    if (r && r.external) {
      window.open(r.external, '_blank');
    } else if (r) {
      router.push({ path: r.path, query: r.query });
    }
  } catch (e) {
    MessagePlugin.info((e.message || '预览失败') + '，可改用下载按钮');
  }
}

// 存网盘：弹目录选择（缺省定位 安全日记录/），确认后 POST save-netdisk
const ndVisible = ref(false);
const ndTarget = ref(null);

function openSaveNetdisk(rec) {
  ndTarget.value = rec;
  ndVisible.value = true;
}

async function confirmSaveNetdisk(sel) {
  if (!ndTarget.value || !sel) return;
  try {
    // dir 为相对空间根的目录（根目录为空串，与旧页 ShadeNdPick 口径一致）
    const data = await saveRecordToNetdisk(ndTarget.value.id, {
      dir: sel.dir,
      space: sel.space,
    });
    MessagePlugin.success(`已保存到网盘：${data.path || ''}`);
  } catch (e) {
    MessagePlugin.error(e.message || '保存失败');
  } finally {
    ndTarget.value = null;
  }
}

// 删除
const delMask = ref(false);
// 删除确认弹层进出场（overlay-motion）：生成表单为复杂多字段表单已走右侧抽屉（Drawer 自带进出场）
const { rendered: delRendered, phaseClass: delPhase } = useOverlayMotion(delMask);
const deleting = ref(false);
const delTarget = ref(null);

function openDelMask(rec) {
  delTarget.value = rec;
  delMask.value = true;
}

async function confirmDelete() {
  if (!delTarget.value || deleting.value) return;
  deleting.value = true;
  try {
    const data = await deleteRecord(delTarget.value.id);
    delMask.value = false;
    // 服务端可能返回 warning（记录已删但文件删除失败）
    if (data.warning) MessagePlugin.info(data.warning);
    else MessagePlugin.success('记录已删除');
    refreshRecords(false);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  } finally {
    deleting.value = false;
  }
}

// 删除确认不绑定遮罩点击关闭，防误点丢上下文；Esc 可关闭（请求中除外）。
// 生成表单抽屉与目录选择弹层自带 Esc 拦截，这里主要兜删除确认。
function onEsc(e) {
  if (e.key !== 'Escape') return;
  if (ndVisible.value) return; // 目录选择弹层自行拦截 Esc
  if (genMask.value) closeGenMask(); // 生成表单抽屉自行拦截 Esc，此行仅兜底
  if (delMask.value && !deleting.value) delMask.value = false;
}

// ---- 超管班组切换（端点缺失时静默隐藏） ----
const teams = ref([]);
const teamId = ref('');
const allowAll = ref(true);
const noTeam = ref(false);

function switchTeam() {
  formMetaCache.value = null;
  formMetaTeam.value = null;
  stopPolling();
  track.value = null;
  knownIds.value = new Set();
  newIds.value = new Set();
  lastRecords.value = [];
  loadedOnce.value = false;
  try {
    localStorage.setItem('safeday_team_id', teamId.value);
  } catch (e) {
    /* 忽略 */
  }
  refreshRecords(true);
}

// ---- 初始化 ----
onMounted(async () => {
  const me = currentUser() || {};
  // 班组隔离门控：非超管未分配班组 → 整页空态（屏十口径）
  if (me.role !== 'admin' && !me.team_id) {
    noTeam.value = true;
    return;
  }
  if (me.role === 'admin') {
    try {
      const list = await listTeams();
      teams.value = (list || []).filter((t) => t.status === 1 || t.status === undefined);
      if (teams.value.length) {
        let saved = 'all';
        try {
          saved = localStorage.getItem('safeday_team_id') || 'all';
        } catch (e) {
          /* 忽略 */
        }
        teamId.value = saved;
      }
    } catch (e) {
      // 端点缺失/失败：不显示切换器，后端按默认班组出数
      teams.value = [];
    }
  }
  probeNetdisk()
    .then((ok) => {
      netdiskOk.value = !!ok;
    })
    .catch(() => {
      netdiskOk.value = false;
    });
  refreshRecords(true);
  document.addEventListener('keydown', onEsc);
});

onBeforeUnmount(() => {
  stopPolling();
  document.removeEventListener('keydown', onEsc);
});
</script>

<style scoped>
/* ===== 板块标题行（旧页 section-title 口径） ===== */
.sd-head {
  display: flex;
  align-items: center;
  gap: 9px;
  margin: 6px 0 16px;
}
.sd-head h3 {
  font-size: 15.5px;
  font-weight: 600;
  letter-spacing: 0.5px;
  margin: 0;
}
.sd-head .more {
  margin-left: auto;
  font-size: 12px;
  color: var(--tk-ph);
  letter-spacing: 1px;
}
/* 原生下拉（班组切换/主持人/记录人）：自绘箭头 + 指针，保持原生可点开 */
.sd-team-sel,
select.sd-input {
  appearance: none;
  -webkit-appearance: none;
  padding-right: 34px;
  cursor: pointer;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%2386909c' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
}
.sd-team-sel {
  height: 40px;
  min-width: 132px;
  padding-left: 12px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background-color: var(--card);
  font-size: 12.5px;
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.sd-team-sel:hover {
  border-color: var(--tk-brand);
}
.sd-team-sel:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--brand-glow);
}

/* ===== 布局：右窄栏上传/参数/进度 + 左侧记录表 ===== */
.sd-body {
  display: flex;
  flex-direction: row-reverse;
  gap: 20px;
  align-items: flex-start;
}
.sd-side {
  width: 340px;
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
/* 中和 styles.css `.card + .card` 相邻上边距：间距统一交给 flex gap，防首卡被撑高、相邻卡下移不等高 */
.sd-side > .card {
  margin-top: 0;
}
/* 卡片作外壳时零内边距（旧页 .card 无全局 padding，内边距由内部块自带） */
.sd-card-flat {
  padding: 0;
}
.sd-ftags:empty {
  display: none;
}

/* ===== 拖传上传区 ===== */
.sd-drop {
  border: 1.5px dashed var(--tk-dash);
  border-radius: 12px;
  background: var(--card);
  padding: 28px 22px;
  text-align: center;
  cursor: pointer;
  transition:
    border-color 0.3s,
    background 0.3s,
    box-shadow 0.3s;
}
.sd-drop:hover,
.sd-drop.over {
  border-color: var(--tk-brand);
  background: var(--tk-bg);
}
.sd-drop.over {
  box-shadow: inset 0 0 0 3px var(--brand-glow);
}
.sd-drop-ic {
  width: 56px;
  height: 56px;
  margin: 0 auto 12px;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.sd-drop b {
  display: block;
  font-size: 14px;
  font-weight: 700;
}
.sd-drop b i {
  font-style: normal;
  color: var(--tk-brand);
}
.sd-drop p {
  margin: 8px 0 0;
  font-size: 11.5px;
  color: var(--tk-ph);
  line-height: 1.8;
}

/* ===== 已选文件条 ===== */
.sd-ftags {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sd-ftag {
  display: flex;
  align-items: center;
  gap: 9px;
  background: var(--card);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 8px 10px;
}
.sd-ftag svg {
  flex: none;
  color: var(--tk-brand);
}
.sd-ftag-nm {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sd-ftag-sz {
  flex: none;
  font-size: 11px;
  color: var(--tk-ph);
}
.sd-ftag-x {
  flex: none;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--tk-ph);
  cursor: pointer;
  font-size: 15px;
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    color 0.2s,
    background 0.2s;
}
.sd-ftag-x:hover {
  color: var(--tk-red);
  background: var(--tk-red-soft);
}

/* ===== 生成参数卡 ===== */
.sd-gen {
  padding: 18px 20px;
}
.sd-lab {
  display: block;
  margin: 14px 0 7px;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--tk-sub);
}
.sd-lab small {
  font-weight: 400;
  color: var(--tk-ph);
  letter-spacing: 0;
}
.sd-gen > .sd-lab {
  margin-top: 0;
  margin-bottom: 8px;
}
.sd-date-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sd-input {
  width: 100%;
  height: 44px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--card);
  padding: 0 14px;
  font-size: 14px;
  color: var(--tk-ink);
  outline: none;
  transition:
    border-color 0.15s var(--tk-ease),
    box-shadow 0.15s var(--tk-ease);
}
.sd-input::placeholder {
  color: var(--tk-ph);
}
.sd-input:focus {
  border-color: var(--tk-brand);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
.sd-date-row .sd-input {
  /* 与「生成记录」块按钮同高对齐 */
  height: 44px;
  flex: 1;
  min-width: 0;
  color-scheme: light dark;
}
textarea.sd-name-area {
  min-height: 140px;
  padding: 10px 14px;
  resize: vertical;
  line-height: 1.7;
  font-family: inherit;
}

/* ===== 按钮（旧页 .btn 口径：8px 圆角 + 1px 发丝边） ===== */
.sd-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  color: var(--tk-ink);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.sd-btn svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.sd-btn:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.sd-btn:active:not(:disabled) {
  transform: translateY(1px);
}
.sd-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.sd-btn-block {
  width: 100%;
  height: 44px;
  margin-top: 14px;
  font-size: 14px;
  font-weight: 700;
  letter-spacing: 2px;
  background: var(--tk-brand);
  border-color: transparent;
  color: var(--on-brand);
}
.sd-btn-block:hover:not(:disabled) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.sd-btn-sm {
  height: 30px;
  padding: 0 12px;
  font-size: 12px;
}
.sd-btn-primary {
  background: var(--tk-brand);
  border-color: transparent;
  color: var(--on-brand);
}
.sd-btn-primary:hover:not(:disabled) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.sd-btn-danger {
  background: var(--tk-red);
  border-color: transparent;
  color: var(--on-brand);
}
.sd-btn-danger:hover:not(:disabled) {
  color: var(--on-brand);
  filter: brightness(0.92);
}

/* ===== 生成进度卡 ===== */
.sd-prog {
  padding: 18px 20px;
}
.sd-prog.idle {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--tk-ph);
  font-size: 12.5px;
  letter-spacing: 0.5px;
}
.sd-prog.idle svg {
  flex: none;
  color: var(--tk-dash);
}
.sd-prog-fh {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sd-file-ic {
  width: 34px;
  height: 34px;
  flex: none;
  border-radius: 8px;
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  display: flex;
  align-items: center;
  justify-content: center;
}
.sd-prog-fn {
  font-size: 13px;
  font-weight: 700;
  word-break: break-all;
}
.sd-prog-fs {
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-ph);
}
.sd-steps {
  margin-top: 16px;
}
.sd-step {
  position: relative;
  display: flex;
  gap: 11px;
  padding-bottom: 15px;
}
.sd-step:last-child {
  padding-bottom: 0;
}
.sd-step::before {
  content: '';
  position: absolute;
  left: 9.5px;
  top: 22px;
  bottom: 0;
  width: 2px;
  background: var(--hair);
}
.sd-step:last-child::before {
  display: none;
}
.sd-step-ic {
  position: relative;
  z-index: 1;
  width: 20px;
  height: 20px;
  flex: none;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--card);
}
.sd-step.done .sd-step-ic {
  background: var(--tk-brand);
  color: var(--on-brand);
}
.sd-step.done::before {
  background: var(--tk-brand);
}
.sd-step.doing .sd-step-ic {
  border: 2px solid var(--tk-brand);
}
.sd-step.doing .sd-step-ic i {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--tk-brand);
  animation: sd-breath 1.2s ease-in-out infinite;
}
.sd-step.todo .sd-step-ic {
  border: 2px solid var(--tk-dash);
}
.sd-step.fail .sd-step-ic {
  background: var(--tk-red);
  color: var(--on-brand);
}
.sd-step-t {
  font-size: 12.5px;
  font-weight: 700;
}
.sd-step.todo .sd-step-t {
  color: var(--tk-ph);
}
.sd-step.fail .sd-step-t {
  color: var(--tk-red);
}
.sd-step-s {
  margin-top: 2px;
  font-size: 11px;
  color: var(--tk-ph);
  line-height: 1.6;
}
@keyframes sd-breath {
  0%,
  100% {
    transform: scale(0.7);
    opacity: 0.55;
  }
  50% {
    transform: scale(1);
    opacity: 1;
  }
}
/* 流光进度条 */
.sd-pbar {
  height: 6px;
  margin-top: 14px;
  border-radius: 8px;
  background: var(--hair);
  overflow: hidden;
}
.sd-pbar i {
  position: relative;
  display: block;
  height: 100%;
  border-radius: 8px;
  background: linear-gradient(90deg, var(--tk-brand), #4d7cf0);
  overflow: hidden;
  transition: width 0.4s var(--tk-ease);
}
.sd-pbar i::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(100deg, transparent 25%, rgba(255, 255, 255, 0.55), transparent 75%);
  transform: translateX(-100%);
  animation: sd-shimmer 1.8s ease-in-out infinite;
}
.sd-pbar.fail i {
  background: linear-gradient(90deg, var(--tk-red), #e46067);
}
.sd-pbar.fail i::after {
  display: none;
}
@keyframes sd-shimmer {
  to {
    transform: translateX(100%);
  }
}
.sd-pnum {
  margin-top: 8px;
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--tk-sub);
}
.sd-pnum b {
  color: var(--tk-brand);
}
.sd-pnum.fail b {
  color: var(--tk-red);
}

/* ===== 记录表格 ===== */
.sd-tbl {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  padding: 0; /* 外壳零内边距（同旧页 .card）：表头/单元格自带 22px 边距，防内容区被挤窄裁切 */
}
/* 表格滚动区：超宽时横向滚动不裁切操作列；细滚动条 + gutter 防滚动条出现把内容挤偏 */
.sd-tbl-scroll {
  overflow-x: auto;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: var(--dash) transparent;
}
.sd-tbl-scroll::-webkit-scrollbar {
  height: 5px;
}
.sd-tbl-scroll::-webkit-scrollbar-thumb {
  background: var(--dash);
  border-radius: 8px;
}
.sd-tbl-scroll::-webkit-scrollbar-thumb:hover {
  background: var(--ph);
}
.sd-tbl-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.sd-tbl-h {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 16px 22px;
  border-bottom: 1px solid var(--hair);
}
.sd-tbl-h b {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.5px;
}
.sd-tbl-h .sd-refresh {
  margin-left: 6px;
}
.sd-tbl-h .cnt {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--tk-ph);
  letter-spacing: 1px;
  white-space: nowrap;
}
.sd-refresh svg.sd-rot {
  animation: sd-rot 1s linear infinite;
}
@keyframes sd-rot {
  to {
    transform: rotate(360deg);
  }
}
.sd-rtable {
  width: 100%;
  border-collapse: collapse;
}
.sd-rtable th {
  padding: 11px 22px;
  background: var(--tk-bg);
  border-bottom: 1px solid var(--hair);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--tk-ph);
  text-align: left;
  letter-spacing: 1px;
  white-space: nowrap;
}
.sd-rtable td {
  padding: 14px 22px;
  border-bottom: 1px solid var(--hair);
  font-size: 13px;
  vertical-align: middle;
}
.sd-rtable tbody tr:last-child td {
  border-bottom: none;
}
.sd-rtable tbody tr {
  transition: background 0.2s;
}
.sd-rtable tbody tr:hover {
  background: var(--tk-bg);
}
/* 已生成行整行可点击预览：指针 + hover 浅蓝高亮 */
.sd-rtable tbody tr.sd-row-prev {
  cursor: pointer;
}
.sd-rtable tbody tr.sd-row-prev:hover {
  background: var(--tk-blue-soft);
}
.sd-rtable tr.sd-row-new {
  animation: sd-fade-up 0.5s var(--tk-ease) both;
}
@keyframes sd-fade-up {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.sd-rn {
  font-weight: 700; /* 长文件名自然按 CJK 断行；超宽时由 .sd-tbl-scroll 横向滚动兜底，不裁切 */
}
.sd-rf {
  margin-top: 3px;
  font-size: 11px;
  color: var(--tk-ph);
  display: flex;
  align-items: center;
  gap: 5px;
}
.sd-rf svg {
  flex: none;
}
.sd-mut {
  color: var(--tk-sub);
  white-space: nowrap;
}

/* 状态标签（4px 小胶囊）与静态圆点 */
.sd-tag {
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
.sd-tag-orange {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.sd-tag-navy {
  background: var(--tk-fill);
  color: var(--tk-sub);
}
.sd-tag-red {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.sd-pulse-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--tk-sub);
}

/* 操作列图标按钮 */
.sd-op {
  display: flex;
  gap: 8px;
}
.sd-opb {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: var(--card);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--tk-sub);
  cursor: pointer;
  transition:
    border-color 0.25s,
    color 0.25s,
    background 0.25s;
}
.sd-opb:hover:not(:disabled) {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.sd-opb-del:hover:not(:disabled) {
  border-color: var(--tk-red);
  color: var(--tk-red);
  background: var(--tk-red-soft);
}
.sd-opb:disabled {
  opacity: 0.38;
  cursor: not-allowed;
}

/* 加载 / 空态 */
.sd-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--tk-ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}
.sd-spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--tk-line);
  border-top-color: var(--tk-brand);
  animation: sd-rot 1s linear infinite;
}
.sd-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--tk-ph);
}
.sd-empty svg {
  color: var(--tk-dash);
}
.sd-empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
}
.sd-empty-sub {
  font-size: 12px;
  text-align: center;
  line-height: 1.8;
}

/* 弹层（生成表单走右侧抽屉；删除确认居中）骨架：进出场由 overlay-motion 驱动 */
.sd-mask {
  position: fixed;
  inset: 0;
  z-index: 100;
  padding: 20px;
  background: var(--overlay);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
/* 文件条新行入场（旧页 .ftag fadeUp 0.4s） */
.sd-ftag-enter-active {
  animation: sd-fade-up 0.4s var(--tk-ease) both;
}
.sd-dialog {
  width: 420px;
  max-width: 100%;
  max-height: 88vh;
  overflow-x: hidden;
  overflow-y: auto;
  background: var(--card);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  box-shadow: var(--tk-shadow-card);
  padding: 26px 26px 22px;
  /* 滚动条出现时不挤偏内容（both-edges 保持居中；不支持时退化为单侧 gutter） */
  scrollbar-gutter: stable;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: var(--dash) transparent;
}
.sd-dialog::-webkit-scrollbar {
  width: 5px;
}
.sd-dialog::-webkit-scrollbar-thumb {
  background: var(--dash);
  border-radius: 8px;
}
.sd-dialog::-webkit-scrollbar-thumb:hover {
  background: var(--ph);
}
.sd-dialog::-webkit-scrollbar-track {
  background: transparent;
}
.sd-dialog h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.sd-dsub {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--tk-sub);
  line-height: 1.7;
  word-break: break-all;
}
.sd-2col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.sd-mode-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.sd-mswitch {
  display: flex;
  padding: 3px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: var(--tk-bg);
}
.sd-ms-opt {
  padding: 3px 14px;
  border: none;
  border-radius: 8px;
  background: none;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--tk-sub);
  cursor: pointer;
  transition:
    background 0.15s,
    color 0.15s;
}
.sd-ms-opt.on {
  background: var(--tk-brand);
  color: var(--on-brand);
}
/* 成员点亮 chips：参加组点亮=蓝实底；缺席组点亮=红实底；两组互斥镜像 */
.sd-mchips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.sd-mchip {
  padding: 5px 13px;
  border-radius: 8px;
  border: 1.5px solid var(--tk-dash);
  background: var(--card);
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
  cursor: pointer;
  transition:
    border-color 0.15s,
    background 0.15s,
    color 0.15s;
  user-select: none;
}
.sd-mchip.on {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: var(--on-brand);
}
.sd-mchips.sd-absent .sd-mchip.on {
  border-color: var(--tk-red);
  background: var(--tk-red);
}
.sd-chips-loading {
  font-size: 12px;
  color: var(--tk-ph);
}
.sd-d-ic {
  width: 44px;
  height: 44px;
  margin-bottom: 12px;
  border-radius: 8px;
  background: var(--tk-red-soft);
  color: var(--tk-red);
  display: flex;
  align-items: center;
  justify-content: center;
}
.sd-actions {
  margin-top: 18px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

/* ===== 入场编排：页头 + 主区一次性上浮/淡入后静止（时延链 ≤0.9s） ===== */
.sd-head h3 {
  animation: sd-fade 0.55s var(--tk-ease) 0.06s both;
}
.sd-head .more {
  animation: sd-fade 0.55s var(--tk-ease) 0.12s both;
}
.sd-side {
  animation: sd-rise 0.55s var(--tk-ease) 0.2s both;
}
.sd-tbl {
  animation: sd-rise 0.55s var(--tk-ease) 0.3s both;
}
@keyframes sd-fade {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes sd-rise {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* 中等宽度收紧单元格水平边距，保证操作列完整可见；宽屏保持旧页 22px 口径 */
@media (max-width: 1500px) {
  .sd-rtable th,
  .sd-rtable td {
    padding-left: 12px;
    padding-right: 12px;
  }
  .sd-tbl-h {
    padding-left: 12px;
    padding-right: 12px;
  }
}

@media (max-width: 960px) {
  .sd-body {
    flex-direction: column;
  }
  .sd-side {
    width: 100%;
  }
  .sd-tbl {
    width: 100%;
  }
  .sd-tbl-scroll {
    overflow-x: auto;
  }
  .sd-rtable {
    min-width: 640px;
  }
}

/* 动效降级 */
@media (prefers-reduced-motion: reduce) {
  .sd-head h3,
  .sd-head .more,
  .sd-side,
  .sd-tbl,
  .sd-rtable tr.sd-row-new,
  .sd-ftag-enter-active {
    animation: none;
  }
  .sd-step.doing .sd-step-ic i,
  .sd-pbar i::after,
  .sd-refresh svg.sd-rot,
  .sd-spin {
    animation: none;
  }
}
</style>
