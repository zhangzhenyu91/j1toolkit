<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">题库刷题</h1>
      <span class="page-sub">题库池 · 刷题练习 · 错题收藏 · 题库管理与 Excel 导入</span>
    </div>

    <!-- 页签：刷题练习 / 题库管理（管理角色可见） -->
    <div class="qz-tabs">
      <button class="qz-tab" :class="{ on: tab === 'practice' }" @click="tab = 'practice'">
        刷题练习
      </button>
      <button
        v-if="isManager"
        class="qz-tab"
        :class="{ on: tab === 'manage' }"
        @click="switchManageTab"
      >
        题库管理
      </button>
    </div>

    <!-- ================= 刷题练习 ================= -->
    <div v-show="tab === 'practice'" class="two-col">
      <!-- 左：我的题库 -->
      <div class="col-side card">
        <div class="toolbar">
          <h2 class="card-title" style="margin: 0">我的题库</h2>
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="openBankDialog(null)">新建</t-button>
          <t-button size="small" variant="outline" @click="openPool">题库池</t-button>
        </div>
        <t-loading :loading="banksLoading" size="small">
          <div
            v-for="b in banks"
            :key="b.id"
            class="list-row bank-row"
            :class="{ active: b.id === bankId }"
            @click="selectBank(b)"
          >
            <div class="row-main">
              <div class="row-title">
                {{ b.name }}
                <span class="tag" :class="b.scope === 'all' ? 'tag-blue' : 'tag-ink'">
                  {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
                </span>
              </div>
              <div class="row-sub num">
                {{ b.questionCount }} 题 · 已练 {{ b.answeredCount }}
                <template v-if="b.rightRate !== null && b.rightRate !== undefined">
                  · 正确率 {{ b.rightRate }}%
                </template>
              </div>
            </div>
          </div>
          <div v-if="!banks.length && !banksLoading" class="muted">
            还没有题库，点右上角「题库池」添加，或「新建」创建题库
          </div>
        </t-loading>
      </div>

      <!-- 右：题库主页 / 刷题 -->
      <div class="col-main card">
        <!-- 题库主页 -->
        <template v-if="!practice.started">
          <t-loading :loading="homeLoading" size="small">
            <template v-if="home">
              <div class="bh-head">
                <b class="bh-name">
                  {{ home.name }}
                  <span class="tag" :class="home.scope === 'all' ? 'tag-blue' : 'tag-ink'">
                    {{ home.scope === 'all' ? '全部池' : home.teamName || '班组池' }}
                  </span>
                  <span v-if="aiReady" class="badge-outline">AI 解析</span>
                </b>
                <p class="bh-desc">{{ home.description || '暂无简介' }}</p>
              </div>
              <div class="bh-stats num">
                <div class="cell">
                  <b>{{ home.questionCount }}</b
                  ><span>题目总数</span>
                </div>
                <div class="cell">
                  <b class="brand">{{ home.answeredCount }}</b
                  ><span>已练题数</span>
                </div>
                <div class="cell">
                  <b>{{
                    home.rightRate === null || home.rightRate === undefined
                      ? '—'
                      : home.rightRate + '%'
                  }}</b>
                  <span>正确率</span>
                </div>
                <div class="cell">
                  <b>{{ home.wrongCount }}</b
                  ><span>错题待攻克</span>
                </div>
              </div>
              <div class="bh-grid">
                <div
                  v-for="e in bankEntries"
                  :key="e.m"
                  class="bh-entry"
                  @click="startPractice(e.m)"
                >
                  <div class="tx">
                    <b>
                      {{ e.t }}
                      <span v-if="e.cnt" class="tag" :class="e.cntCls">{{ e.cnt }}</span>
                    </b>
                    <p>{{ e.d }}</p>
                  </div>
                  <span class="chev">›</span>
                </div>
              </div>
              <div v-if="aiPending" class="ai-notice">
                <span class="txt">AI 解析进行中，期间可正常刷题</span>
                <div class="progress slim"><i :style="{ width: aiPct + '%' }"></i></div>
                <b class="num">{{ aiDone }} / {{ aiTotal }}</b>
              </div>
            </template>
            <div v-else-if="!homeLoading" class="empty">
              <p>{{ homeErr || '还没有题库' }}</p>
              <p class="muted">点左栏「题库池」从池中添加题库，或直接「新建」</p>
            </div>
          </t-loading>
        </template>

        <!-- 刷题视图 -->
        <template v-else>
          <div class="pv-top">
            <span class="pv-back" @click="backToHome">‹ 题库主页</span>
            <b class="pv-title">{{ home ? home.name : '' }} · {{ modeText(practice.mode) }}</b>
            <span class="pv-vm">
              <i :class="{ on: practice.viewMode === 'answer' }" @click="switchView('answer')"
                >答题</i
              >
              <i :class="{ on: practice.viewMode === 'recite' }" @click="switchView('recite')"
                >背题</i
              >
            </span>
            <div class="pv-pg num">
              <div class="progress"><i :style="{ width: pct + '%' }"></i></div>
              <b>{{ practice.idx + 1 }} / {{ practice.outline.length }}</b>
            </div>
          </div>

          <div v-if="!curQ" class="empty" style="margin: 40px 0">
            <p>{{ emptyText }}</p>
            <t-button size="small" variant="outline" @click="backToHome">返回题库主页</t-button>
          </div>

          <template v-else>
            <div class="pv-col">
              <div class="pv-stem">
                <span class="tag" :class="curQ.type === 'judge' ? 'tag-blue' : 'tag-orange'">
                  {{ typeText(curQ.type) }}
                </span>
                <span class="stem-text">{{ curQ.content }}</span>
              </div>
              <div
                v-for="(opt, i) in displayOptions"
                :key="i"
                class="pv-opt"
                :class="[opt.cls, { lock: submitted, sq: curQ.type === 'multiple' }]"
                @click="onOption(opt.letter)"
              >
                <span class="ab">{{ opt.inner }}</span>
                <span class="tx">{{ opt.text }}</span>
              </div>

              <!-- 判分结果 -->
              <div v-if="!isRecite && curResult" class="r-block">
                <div class="r-line">
                  <div class="r-main num">
                    <span
                      >正确答案<b class="ok">{{ resultAnswerText }}</b></span
                    >
                    <span v-if="resultYourText">
                      您的选择<b :class="curResult.right ? 'ok' : 'bad'">{{ resultYourText }}</b>
                    </span>
                  </div>
                  <div v-if="curResult.stats && curResult.stats.myTimes" class="r-sub num">
                    本人作答 {{ curResult.stats.myTimes }} 次 · 正确率
                    {{ curResult.stats.myRightRate }}%
                  </div>
                  <div v-if="curResult.stats && curResult.stats.allTimes" class="r-sub num">
                    全员作答 {{ curResult.stats.allTimes }} 次 · 正确率
                    {{ curResult.stats.allRightRate }}%
                  </div>
                  <div v-if="wrongBanner" class="r-sub orange">{{ wrongBanner }}</div>
                </div>
              </div>

              <!-- 试题详解 -->
              <div v-if="submitted" class="pv-ana">
                <div class="hd">
                  试题详解
                  <button
                    class="pv-ana-edit"
                    title="修改解析（对全员生效）"
                    @click="openAnalysisEdit"
                  >
                    编辑
                  </button>
                </div>
                <div class="bd">{{ analysisText || '解析生成中，稍后可查看' }}</div>
                <div v-if="analysisText" class="ft">解析内容仅供参考</div>
              </div>
            </div>

            <div class="pv-foot">
              <button class="pv-chipbtn" :class="{ faved: curFav }" @click="toggleFav">收藏</button>
              <span class="pv-chipbtn num">
                <span class="qz-ok">✓</span>{{ counts.right }} <span class="qz-bad">✕</span
                >{{ counts.wrong }}
              </span>
              <button class="pv-chipbtn" @click="openSheet">
                答题卡 {{ counts.answered }}/{{ practice.outline.length }}
              </button>
              <button class="pv-chipbtn" @click="settingsVisible = true">设置</button>
              <span class="sp">
                <t-button
                  size="small"
                  variant="outline"
                  :disabled="practice.idx <= 0"
                  @click="go(practice.idx - 1)"
                >
                  上一题
                </t-button>
                <t-button
                  v-if="!submitted && curQ.type === 'multiple'"
                  size="small"
                  theme="primary"
                  :disabled="!practice.sel.length"
                  @click="submitCur"
                >
                  提交答案
                </t-button>
                <t-button v-else-if="submitted" size="small" theme="primary" @click="nextOrFinish">
                  {{ practice.idx >= practice.outline.length - 1 ? '完成' : '下一题' }}
                </t-button>
                <span v-else class="pv-hint">点选即判分</span>
              </span>
            </div>
          </template>
        </template>
      </div>
    </div>

    <!-- ================= 题库管理 ================= -->
    <div v-show="tab === 'manage'">
      <div class="card">
        <div class="toolbar">
          <h2 class="card-title" style="margin: 0">题库管理</h2>
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="downloadTemplate">下载导入模板</t-button>
          <t-button size="small" variant="outline" @click="openImport">导入 Excel</t-button>
          <t-button size="small" theme="primary" @click="openBankDialog(null, true)"
            >新建题库</t-button
          >
        </div>
        <t-loading :loading="mgLoading" size="small">
          <div class="qtable">
            <div class="tr hd">
              <div class="c-t">题库</div>
              <div class="c-q">简介</div>
              <div class="c-a">题目数 / 解析</div>
              <div class="c-s">创建时间</div>
              <div class="c-o">操作</div>
            </div>
            <div v-for="b in mgList" :key="b.id" class="tr">
              <div class="c-t">
                <b>{{ b.name }}</b>
                <span class="tag" :class="b.scope === 'all' ? 'tag-blue' : 'tag-ink'">
                  {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
                </span>
              </div>
              <div class="c-q">{{ b.description || '—' }}</div>
              <div class="c-a num">
                {{ b.questionCount }} 题
                <span class="an-stat">
                  待生成 {{ (b.analysis && b.analysis.none) || 0 }} · 生成中
                  {{ (b.analysis && b.analysis.pending) || 0 }} · 失败
                  {{ (b.analysis && b.analysis.failed) || 0 }} · 已生成
                  {{ (b.analysis && b.analysis.done) || 0 }}
                </span>
              </div>
              <div class="c-s num">{{ b.createdAt || '—' }}</div>
              <div class="c-o">
                <t-button size="small" variant="text" @click="openQuestions(b)">题目</t-button>
                <t-button size="small" variant="text" @click="openBankDialog(b, true)"
                  >编辑</t-button
                >
                <t-button size="small" variant="text" theme="danger" @click="onDeleteBank(b)">
                  删除
                </t-button>
              </div>
            </div>
            <div v-if="!mgList.length && !mgLoading" class="empty">暂无可管理的题库</div>
          </div>
        </t-loading>
      </div>

      <!-- 题目管理面板 -->
      <div v-if="mgBank" class="card">
        <div class="toolbar">
          <h2 class="card-title" style="margin: 0">「{{ mgBank.name }}」题目列表</h2>
          <div class="spacer"></div>
          <t-input
            v-model="mq.q"
            class="mq-search"
            size="small"
            placeholder="搜索题干"
            @change="onSearchQuestions"
          />
          <t-button size="small" variant="outline" @click="onAnalyzeRetry">重新生成解析</t-button>
          <t-button size="small" theme="primary" @click="openQuestionDialog(null)"
            >新增题目</t-button
          >
        </div>
        <t-loading :loading="mqLoading" size="small">
          <div class="qtable">
            <div class="tr hd">
              <div class="c-t">题型</div>
              <div class="c-q">题干</div>
              <div class="c-a">答案</div>
              <div class="c-s">解析状态</div>
              <div class="c-o">操作</div>
            </div>
            <div v-for="q in mq.list" :key="q.id" class="tr">
              <div class="c-t">{{ typeText(q.type) }}</div>
              <div class="c-q">{{ q.content }}</div>
              <div class="c-a">{{ answerText(q.type, q.answer) }}</div>
              <div class="c-s">
                <span class="dot-tag" :class="stTag(q.analysisStatus).cls">
                  <span class="dot"></span>{{ stTag(q.analysisStatus).text }}
                </span>
              </div>
              <div class="c-o">
                <t-button size="small" variant="text" @click="openQuestionDialog(q)">编辑</t-button>
                <t-button size="small" variant="text" theme="danger" @click="onDeleteQuestion(q)">
                  删除
                </t-button>
              </div>
            </div>
            <div v-if="!mq.list.length && !mqLoading" class="empty">暂无题目</div>
          </div>
          <div class="pager num">
            <t-button
              size="small"
              variant="outline"
              :disabled="mq.page <= 1"
              @click="loadMq(mq.page - 1)"
            >
              上一页
            </t-button>
            <span>{{ mq.page }} / {{ mqPages }} 页 · 共 {{ mq.total }} 题</span>
            <t-button
              size="small"
              variant="outline"
              :disabled="mq.page >= mqPages"
              @click="loadMq(mq.page + 1)"
            >
              下一页
            </t-button>
          </div>
        </t-loading>
      </div>
    </div>

    <!-- ================= 题库池弹层 ================= -->
    <t-dialog
      v-model:visible="poolVisible"
      header="题库池"
      :confirm-btn="null"
      :cancel-btn="{ content: '关闭' }"
      :width="640"
    >
      <t-loading :loading="poolLoading" size="small">
        <div v-for="b in pool" :key="b.id" class="list-row pool-row">
          <div class="row-main">
            <div class="row-title">
              {{ b.name }}
              <span class="tag" :class="b.scope === 'all' ? 'tag-blue' : 'tag-ink'">
                {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
              </span>
            </div>
            <div class="row-sub num">
              {{ b.description || '暂无简介' }} · {{ b.questionCount }} 题
            </div>
          </div>
          <t-button v-if="!b.added" size="small" theme="primary" @click="onJoin(b)">
            加入
          </t-button>
          <t-button v-else size="small" variant="outline" @click="onLeave(b)">退出</t-button>
        </div>
        <div v-if="!pool.length && !poolLoading" class="muted">题库池暂无可见题库</div>
      </t-loading>
    </t-dialog>

    <!-- ================= 题库编辑弹层 ================= -->
    <t-dialog
      v-model:visible="bankVisible"
      :header="bankForm.id ? '编辑题库' : '新建题库'"
      :confirm-btn="{ content: '保存', theme: 'primary' }"
      :cancel-btn="{ content: '取消' }"
      :width="520"
      @confirm="saveBank"
    >
      <t-form layout="vertical">
        <t-form-item label="题库名称" required>
          <t-input v-model="bankForm.name" :maxlength="100" placeholder="班组内唯一" />
        </t-form-item>
        <t-form-item label="题库简介">
          <t-textarea v-model="bankForm.description" :maxlength="255" placeholder="选填" />
        </t-form-item>
        <t-form-item label="题库池">
          <t-radio-group v-model="bankForm.scope">
            <t-radio value="team">班组池（本班可见）</t-radio>
            <t-radio value="all" :disabled="!isAdmin">全部池（全员可见，仅超管）</t-radio>
          </t-radio-group>
        </t-form-item>
      </t-form>
    </t-dialog>

    <!-- ================= 题目编辑弹层 ================= -->
    <t-dialog
      v-model:visible="questionVisible"
      :header="questionForm.id ? '编辑题目' : '新增题目'"
      :confirm-btn="{ content: '保存', theme: 'primary' }"
      :cancel-btn="{ content: '取消' }"
      :width="640"
      @confirm="saveQuestion"
    >
      <t-form layout="vertical">
        <t-form-item label="题型">
          <t-radio-group v-model="questionForm.type" @change="onTypeChange">
            <t-radio value="single">单选题</t-radio>
            <t-radio value="multiple">多选题</t-radio>
            <t-radio value="judge">判断题</t-radio>
          </t-radio-group>
        </t-form-item>
        <t-form-item label="题干" required>
          <t-textarea v-model="questionForm.content" :maxlength="2000" placeholder="填写题干" />
        </t-form-item>
        <t-form-item v-if="questionForm.type !== 'judge'" label="选项（至少 2 个）">
          <div v-for="(opt, i) in questionForm.options" :key="i" class="opt-row">
            <span class="opt-letter">{{ LETTERS[i] }}</span>
            <t-input
              v-model="questionForm.options[i]"
              :maxlength="500"
              :placeholder="'选项 ' + LETTERS[i]"
            />
            <t-button
              size="small"
              variant="text"
              theme="danger"
              :disabled="questionForm.options.length <= 2"
              @click="questionForm.options.splice(i, 1)"
            >
              删除
            </t-button>
          </div>
          <t-button
            size="small"
            variant="outline"
            :disabled="questionForm.options.length >= 6"
            @click="questionForm.options.push('')"
          >
            添加选项
          </t-button>
        </t-form-item>
        <t-form-item
          :label="questionForm.type === 'judge' ? '答案（正确 / 错误）' : '答案（可多选）'"
        >
          <t-radio-group
            v-if="questionForm.type === 'judge'"
            :value="judgeAnswer"
            @change="onJudgeAnswer"
          >
            <t-radio value="A">正确</t-radio>
            <t-radio value="B">错误</t-radio>
          </t-radio-group>
          <div v-else class="ans-row">
            <button
              v-for="(opt, i) in questionForm.options"
              :key="i"
              type="button"
              class="ans-chip"
              :class="{ on: questionForm.answer.indexOf(LETTERS[i]) >= 0 }"
              :disabled="!questionForm.options[i]"
              @click="toggleAnswerLetter(LETTERS[i])"
            >
              {{ LETTERS[i] }}
            </button>
          </div>
        </t-form-item>
        <t-form-item label="解析（选填，留空入库后由 AI 生成）">
          <t-textarea
            v-model="questionForm.analysis"
            :maxlength="2000"
            placeholder="填写本题解析"
          />
        </t-form-item>
      </t-form>
    </t-dialog>

    <!-- ================= Excel 导入弹层 ================= -->
    <t-dialog
      v-model:visible="importVisible"
      header="导入题目 Excel"
      :confirm-btn="null"
      :cancel-btn="{ content: '关闭' }"
      :width="560"
    >
      <div class="imp-err-tip">
        导入为全量替换该题库题目（连带清空答题记录 / 错题 / 收藏），仅支持 .xlsx，≤10MB；
        表头固定为「题型 | 题干 | 选项A~F | 答案 | 解析」，请先下载模板填写。
      </div>
      <input
        ref="importFileEl"
        type="file"
        accept=".xlsx"
        class="imp-file"
        @change="onImportFile"
      />
      <t-button
        size="small"
        variant="outline"
        :loading="importing"
        @click="importFileEl && importFileEl.click()"
      >
        选择文件并导入
      </t-button>
      <div v-if="importErrors.length" class="imp-errors">
        <div class="imp-errors-title">以下行校验未通过，请修正后重新导入：</div>
        <div v-for="e in importErrors" :key="e.row" class="imp-error-row num">
          第 {{ e.row }} 行：{{ e.message }}
        </div>
      </div>
    </t-dialog>

    <!-- ================= 刷题设置 ================= -->
    <t-dialog
      v-model:visible="settingsVisible"
      header="刷题设置"
      :confirm-btn="{ content: '完成', theme: 'primary' }"
      :cancel-btn="null"
      :width="480"
      @confirm="settingsVisible = false"
    >
      <div class="pv-set-row">
        <div class="tx">
          <b>选项乱序</b>
          <p>单选/多选题选项随机打乱展示（不影响答案判定）</p>
        </div>
        <t-switch v-model="practice.settings.shuffleOpt" @change="saveSettings" />
      </div>
      <div class="pv-set-row">
        <div class="tx">
          <b>答对自动下一题</b>
          <p>答对后 0.5 秒自动跳下一题；答错停留看解析</p>
        </div>
        <t-switch v-model="practice.settings.autoNext" @change="saveSettings" />
      </div>
    </t-dialog>

    <!-- ================= 答题卡 ================= -->
    <t-dialog
      v-model:visible="sheetVisible"
      :header="'答题卡（已答 ' + counts.answered + '/' + practice.outline.length + '）'"
      :confirm-btn="null"
      :cancel-btn="{ content: '关闭' }"
      :width="680"
    >
      <div class="pv-legend">
        <span><i></i>未作答</span><span><i class="g"></i>答对</span
        ><span><i class="r"></i>答错</span>
      </div>
      <template v-for="sec in sheetSections" :key="sec.type">
        <div class="f-label">
          {{ typeText(sec.type) }}<span class="muted">（{{ sec.items.length }} 题）</span>
        </div>
        <div class="pv-sheet-grid">
          <div
            v-for="it in sec.items"
            :key="it.i"
            class="pv-chip"
            :class="[it.st, { cur: it.i === practice.idx }]"
            @click="onSheetJump(it.i)"
          >
            {{ it.i + 1 }}
          </div>
        </div>
      </template>
      <div
        class="pv-clear"
        :class="{ disabled: isRecite || !counts.answered }"
        @click="onResetRecords"
      >
        清空做题记录（错题本保留）
      </div>
    </t-dialog>

    <!-- ================= 修改解析 ================= -->
    <t-dialog
      v-model:visible="anaVisible"
      header="修改解析"
      :confirm-btn="{ content: '保存', theme: 'primary' }"
      :cancel-btn="{ content: '取消' }"
      :width="560"
      @confirm="saveAnalysis"
    >
      <div class="imp-err-tip">解析对题库内全员可见，保存后即刻生效；留空则由 AI 重新生成</div>
      <t-textarea v-model="anaText" :maxlength="2000" placeholder="填写本题解析" />
    </t-dialog>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import {
  analyzeRetry,
  addFavorite,
  createBank,
  createQuestion,
  deleteBank,
  deleteQuestion,
  downloadTemplate as fetchTemplateBlob,
  getBankHome,
  getPracticeQuestions,
  importBank,
  joinBank,
  leaveBank,
  listBankPool,
  listManageBanks,
  listMyBanks,
  listQuestions,
  removeFavorite,
  resetPractice,
  savePracticeProgress,
  submitAnswer,
  updateAnalysis,
  updateBank,
  updateQuestion,
} from '../features/quiz/api.js';

// 选项字母表（与旧页一致，最多 10 位）。
const LETTERS = 'ABCDEFGHIJ';
// 刷题分批取题批大小（与旧页 P_LIMIT 同口径）。
const P_LIMIT = 20;

const user = currentUser() || {};
const role = user.role || 'user';
const isAdmin = role === 'admin';
const isManager = role === 'admin' || role === 'team_admin';

const tab = ref('practice');

// ---------- 左栏：我的题库 ----------
const banks = ref([]);
const banksLoading = ref(false);
const bankId = ref(0);

// ---------- 题库主页 ----------
const home = ref(null);
const homeErr = ref('');
const homeLoading = ref(false);

// ---------- 刷题会话 ----------
const practice = reactive({
  started: false,
  mode: 'seq', // seq / rand / wrong / fav
  viewMode: 'answer', // answer 答题 / recite 背题
  outline: [], // 大纲（lite 取题）
  cache: {}, // 批次缓存：start → [q|null]
  idx: 0,
  sel: [], // 当前选中字母
  results: {}, // qid → 判分结果
  favs: {}, // qid → 0/1
  myRight: {}, // qid → 0/1（历史恢复 + 本 session）
  shuffle: {}, // qid → 展示位 → 原选项下标
  settings: { shuffleOpt: true, autoNext: false },
  submitting: false,
});
const emptyText = ref('');
const autoTimer = ref(0);

// seq 断点上报（防抖 500ms，同值跳过；离开页面兜底补发）
let seqTimer = 0;
let seqSent = null;

// ---------- 题库池 / 弹层 ----------
const pool = ref([]);
const poolLoading = ref(false);
const poolVisible = ref(false);
const bankVisible = ref(false);
const bankForm = reactive({ id: 0, name: '', description: '', scope: 'team' });
const questionVisible = ref(false);
const questionForm = reactive({
  id: 0,
  type: 'single',
  content: '',
  options: ['', ''],
  answer: '',
  analysis: '',
});
const importVisible = ref(false);
const importFileEl = ref(null);
const importing = ref(false);
const importErrors = ref([]);
const settingsVisible = ref(false);
const sheetVisible = ref(false);
const anaVisible = ref(false);
const anaText = ref('');

// ---------- 题库管理 ----------
const mgLoading = ref(false);
const mgList = ref([]);
const mgBank = ref(null);
const mq = reactive({ list: [], total: 0, page: 1, size: 20, q: '' });
const mqLoading = ref(false);
let mqSearchTimer = 0;
let mqPollTimer = 0;

/* ================= 通用小工具 ================= */

// typeText 题型文案。
function typeText(t) {
  return { single: '单选题', multiple: '多选题', judge: '判断题' }[t] || '单选题';
}

// modeText 练习模式文案。
function modeText(m) {
  return { seq: '顺序练习', rand: '随机练习', wrong: '我的错题', fav: '我的收藏' }[m] || '顺序练习';
}

// answerText 答案展示（判断题 A=正确 B=错误）。
function answerText(type, answer) {
  if (!answer) return '—';
  if (type === 'judge') {
    return String(answer)
      .split('')
      .map((l) => ({ A: '正确', B: '错误' })[l] || l)
      .join(' / ');
  }
  return String(answer).split('').join(' ');
}

// stTag 解析状态标签。
function stTag(s) {
  return (
    {
      done: { text: '已生成', cls: 'green' },
      pending: { text: '生成中', cls: 'orange' },
      failed: { text: '生成失败', cls: 'red' },
    }[s] || { text: '待生成', cls: 'orange' }
  );
}

/* ================= 我的题库 / 题库主页 ================= */

async function loadBanks(quiet) {
  if (!quiet) banksLoading.value = true;
  try {
    const data = await listMyBanks();
    banks.value = data.list || [];
    // 当前选中库失效时回落到首个
    if (banks.value.length && !banks.value.some((b) => b.id === bankId.value)) {
      await selectBank(banks.value[0]);
    } else if (!banks.value.length) {
      bankId.value = 0;
      home.value = null;
    }
  } catch (err) {
    if (!quiet) MessagePlugin.error(err.message || '我的题库加载失败');
  } finally {
    banksLoading.value = false;
  }
}

async function selectBank(b) {
  bankId.value = b.id;
  practice.started = false;
  homeErr.value = '';
  await loadHome();
}

async function loadHome() {
  if (!bankId.value) return;
  homeLoading.value = true;
  try {
    home.value = await getBankHome(bankId.value);
  } catch (err) {
    home.value = null;
    homeErr.value = err.message || '题库加载失败';
  } finally {
    homeLoading.value = false;
  }
}

// aiReady / aiPending / aiPct AI 解析进度（analysis 四状态）。
const an = computed(() => (home.value && home.value.analysis) || {});
const aiDone = computed(() => (an.value.done || 0) + (an.value.failed || 0));
const aiTotal = computed(
  () =>
    (an.value.done || 0) + (an.value.failed || 0) + (an.value.pending || 0) + (an.value.none || 0)
);
const aiReady = computed(() => (an.value.pending || 0) === 0 && (an.value.done || 0) > 0);
const aiPending = computed(() => (an.value.pending || 0) > 0);
const aiPct = computed(() => {
  const y = aiTotal.value;
  return y ? Math.round((aiDone.value / y) * 100) : 0;
});

// bankEntries 题库主页四个入口。
const bankEntries = computed(() => {
  const h = home.value || {};
  const seqSaved = h.seqIdx === null || h.seqIdx === undefined ? null : Number(h.seqIdx);
  const seqText = !h.questionCount
    ? '暂无题目'
    : seqSaved === null
      ? `共 ${h.questionCount} 题 · 未开始`
      : `刷到 ${Math.min(seqSaved + 1, h.questionCount)}/${h.questionCount} · 断点续刷`;
  return [
    { m: 'seq', t: '顺序练习', d: seqText },
    {
      m: 'rand',
      t: '随机练习',
      d: h.questionCount ? '随机打乱 · 每次新题序' : '暂无题目',
    },
    {
      m: 'wrong',
      t: '我的错题',
      cnt: h.wrongCount,
      cntCls: 'tag-red',
      d: '答错自动收录 · 连对 3 次移出',
    },
    {
      m: 'fav',
      t: '我的收藏',
      cnt: h.favCount,
      cntCls: 'tag-orange',
      d: '刷题时点星标收藏题目',
    },
  ];
});

/* ================= 刷题：大纲 + 分批取题 ================= */

// fetchOutline 取大纲（lite=1；seq/rand 带 withRecord 恢复历史对错）。
async function fetchOutline() {
  const params = { mode: practice.mode, lite: 1, bankId: bankId.value };
  if (practice.mode === 'seq' || practice.mode === 'rand') params.withRecord = 1;
  const data = await getPracticeQuestions(params);
  practice.outline = data.list || [];
  practice.myRight = {};
  practice.outline.forEach((o) => {
    if (o.myRight !== undefined) practice.myRight[o.id] = o.myRight;
  });
  return typeof data.seqIdx === 'number' ? data.seqIdx : null;
}

// ensureBatch 按大纲区间分批取题并逐位对齐落位（缺位留 null，严禁压缩数组）。
async function ensureBatch(idx) {
  const start = Math.floor(idx / P_LIMIT) * P_LIMIT;
  if (practice.cache[start]) return;
  const params = {
    mode: practice.mode,
    offset: start,
    limit: P_LIMIT,
    bankId: bankId.value,
  };
  if (practice.viewMode === 'recite') params.withAnswer = 1;
  else if (practice.mode === 'seq' || practice.mode === 'rand') params.withRecord = 1;
  // rand 服务端每请求重排：按本批大纲 id 定点取题，防跨批串题
  if (practice.mode === 'rand') {
    params.ids = practice.outline
      .slice(start, start + P_LIMIT)
      .map((o) => o.id)
      .join(',');
  }
  const data = await getPracticeQuestions(params);
  const map = {};
  (data.list || []).forEach((q) => {
    map[q.id] = q;
  });
  practice.cache[start] = practice.outline
    .slice(start, start + P_LIMIT)
    .map((o) => map[o.id] || null);
  practice.cache[start].forEach((q) => {
    if (!q) return;
    practice.favs[q.id] = q.fav ? 1 : 0;
    // 历史作答恢复为解析态（本 session 已答过的保留）
    if (q.myAnswer && !practice.results[q.id]) {
      practice.results[q.id] = {
        selected: String(q.myAnswer).split(''),
        right: !!q.myRight,
        answer: q.answer || '',
        analysis: q.analysis || '',
        wrong: { inBook: false, rightStreak: 0, removed: false },
        stats: null,
      };
    }
  });
}

// curQ 当前题目。
const curQ = computed(() => {
  const start = Math.floor(practice.idx / P_LIMIT) * P_LIMIT;
  return (practice.cache[start] || [])[practice.idx - start] || null;
});
const isRecite = computed(() => practice.viewMode === 'recite');
const curResult = computed(() => (curQ.value ? practice.results[curQ.value.id] : null));
const submitted = computed(() => isRecite.value || !!curResult.value);
const curFav = computed(() => (curQ.value ? !!practice.favs[curQ.value.id] : false));
const pct = computed(() => {
  const total = practice.outline.length;
  return total ? Math.min(100, Math.round(((practice.idx + 1) / total) * 100)) : 0;
});

// counts 答题卡计数（已答/对/错）。
const counts = computed(() => {
  let answered = 0;
  let right = 0;
  let wrong = 0;
  Object.keys(practice.myRight).forEach((k) => {
    const v = practice.myRight[k];
    if (v === undefined) return;
    answered += 1;
    if (v) right += 1;
    else wrong += 1;
  });
  return { answered, right, wrong };
});

// displayOptions 展示选项（乱序映射 + 状态着色）。
const displayOptions = computed(() => {
  const q = curQ.value;
  if (!q) return [];
  const res = curResult.value;
  const recite = isRecite.value;
  let order = null;
  if (!recite && q.type !== 'judge') {
    if (practice.shuffle[q.id]) order = practice.shuffle[q.id];
    else if (practice.settings.shuffleOpt && !res) {
      order = practice.shuffle[q.id] = shuffleIdx((q.options || []).length);
    }
  }
  return (q.options || []).map((_, i) => {
    const origIdx = order ? order[i] : i;
    const letter = LETTERS[i] || String(i + 1);
    const origLetter = LETTERS[origIdx] || letter;
    const text = q.options[origIdx];
    let cls = '';
    let inner = letter;
    if (recite) {
      if ((q.answer || '').indexOf(origLetter) >= 0) {
        cls = 'right';
        inner = '✓';
      }
    } else if (!res) {
      if (practice.sel.indexOf(letter) >= 0) cls = 'sel';
    } else {
      const inAns = (res.answer || '').indexOf(origLetter) >= 0;
      const inSel = (res.selected || []).indexOf(letter) >= 0;
      if (inAns) {
        cls = 'right';
        inner = '✓';
      } else if (inSel) {
        cls = 'wrong';
        inner = '✕';
      }
    }
    return { letter, text, cls, inner };
  });
});

// resultAnswerText / resultYourText 判分展示（判断题映射中文 + 乱序字母还原）。
function mapAnswerText(answerTextIn) {
  const q = curQ.value;
  if (!q) return answerTextIn || '';
  let text = answerTextIn || '';
  const order = practice.shuffle[q.id];
  if (order && text) {
    text = text
      .split('')
      .map((ol) => {
        const di = order.indexOf(LETTERS.indexOf(ol));
        return di >= 0 ? LETTERS[di] : ol;
      })
      .sort()
      .join('');
  }
  if (q.type === 'judge') {
    const jm = { A: '正确', B: '错误' };
    text = text
      .split('')
      .map((l) => jm[l] || l)
      .join(' ');
  }
  return text;
}
const resultAnswerText = computed(() =>
  mapAnswerText((curResult.value && curResult.value.answer) || '')
);
const resultYourText = computed(() => {
  const res = curResult.value;
  if (!res) return '';
  return mapAnswerText((res.selected || []).slice().sort().join(''));
});
const wrongBanner = computed(() => {
  const res = curResult.value;
  if (!res) return '';
  const w = res.wrong || {};
  if (res.right) {
    if (w.removed) return '已移出错题本';
    if (w.inBook && w.rightStreak > 0)
      return `再答对 ${Math.max(3 - w.rightStreak, 0)} 次移出错题本`;
    return '';
  }
  return w.inBook !== false ? '已自动加入错题本' : '';
});
const analysisText = computed(() => {
  if (isRecite.value) return (curQ.value && curQ.value.analysis) || '';
  return (curResult.value && curResult.value.analysis) || (curQ.value && curQ.value.analysis) || '';
});

// shuffleIdx 生成 0..n-1 乱序映射。
function shuffleIdx(n) {
  const arr = Array.from({ length: n }, (_, i) => i);
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
  return arr;
}

/* ================= 刷题：进入 / 切题 / 作答 ================= */

// clearSession 清空本次练习会话（保留设置）。
function clearSession() {
  if (autoTimer.value) {
    clearTimeout(autoTimer.value);
    autoTimer.value = 0;
  }
  practice.cache = {};
  practice.results = {};
  practice.favs = {};
  practice.myRight = {};
  practice.shuffle = {};
  practice.sel = [];
  practice.idx = 0;
}

async function startPractice(mode) {
  if (!bankId.value) return;
  if (
    mode === 'wrong' &&
    !(home.value && home.value.questionCount) &&
    !(home.value && home.value.wrongCount)
  ) {
    // 空库直接走空态
  }
  clearSession();
  practice.mode = mode;
  try {
    const seqSaved = await fetchOutline();
    if (!practice.outline.length) {
      practice.started = true;
      emptyText.value =
        mode === 'wrong'
          ? '暂无错题，继续保持'
          : mode === 'fav'
            ? '暂无收藏题目，刷题时点星标收藏'
            : '该题库暂无题目';
      return;
    }
    emptyText.value = '';
    if (mode === 'seq') {
      practice.idx = Math.min(
        Math.max(seqSaved === null ? 0 : seqSaved, 0),
        practice.outline.length - 1
      );
      seqSent = practice.idx; // 首帧位置即服务端值，无需立即上报
    } else {
      seqSent = null;
    }
    await ensureBatch(practice.idx);
    if (!curQ.value) {
      MessagePlugin.error('题目加载失败，请重试');
      return;
    }
    practice.started = true;
  } catch (err) {
    MessagePlugin.error(err.message || '加载失败，请重试');
  }
}

function backToHome() {
  practice.started = false;
  flushSeq(); // 离开前兜底补发断点
  loadHome();
  loadBanks(true);
}

async function go(idx) {
  if (idx < 0 || idx >= practice.outline.length) return;
  if (autoTimer.value) {
    clearTimeout(autoTimer.value);
    autoTimer.value = 0;
  }
  try {
    await ensureBatch(idx);
  } catch (err) {
    MessagePlugin.error(err.message || '题目加载失败');
    return;
  }
  practice.idx = idx;
  practice.sel = [];
  syncSeq();
}

function onOption(letter) {
  if (isRecite.value || practice.submitting) return;
  const q = curQ.value;
  if (!q || practice.results[q.id]) return;
  if (q.type === 'multiple') {
    const i = practice.sel.indexOf(letter);
    practice.sel = i >= 0 ? practice.sel.filter((l) => l !== letter) : practice.sel.concat(letter);
    return;
  }
  // 单选/判断：点选即判分（短暂延时先呈现选中态）
  practice.sel = [letter];
  practice.submitting = true;
  setTimeout(() => {
    submitCur().finally(() => {
      practice.submitting = false;
    });
  }, 150);
}

async function submitCur() {
  const q = curQ.value;
  if (!q || practice.results[q.id] || isRecite.value) return;
  // 多选按字母序拼接；乱序题把展示字母映射回原始字母
  const order = practice.shuffle[q.id];
  const answer = practice.sel
    .map((l) => (order ? LETTERS[order[LETTERS.indexOf(l)]] : l))
    .sort()
    .join('');
  if (!answer) return;
  try {
    const data = await submitAnswer({ questionId: q.id, answer });
    practice.results[q.id] = {
      selected: practice.sel.slice(),
      right: !!data.right,
      answer: data.answer || '',
      analysis: data.analysis || '',
      wrong: data.wrong || {},
      stats: data.stats || null,
    };
    practice.myRight[q.id] = data.right ? 1 : 0;
    practice.sel = [];
    if (data.wrong && data.wrong.removed) MessagePlugin.success('已移出错题本');
    // 答对自动下一题（设置开启；答错停留看解析；末尾题不自动）
    if (practice.settings.autoNext && data.right && practice.idx < practice.outline.length - 1) {
      const qid = q.id;
      autoTimer.value = setTimeout(() => {
        autoTimer.value = 0;
        const cur = curQ.value;
        if (cur && cur.id === qid) go(practice.idx + 1);
      }, 500);
    }
  } catch (err) {
    MessagePlugin.error(err.message || '提交失败，请重试');
  }
}

function nextOrFinish() {
  if (practice.idx >= practice.outline.length - 1) {
    backToHome();
    return;
  }
  go(practice.idx + 1);
}

async function toggleFav() {
  const q = curQ.value;
  if (!q) return;
  const was = practice.favs[q.id] ? 1 : 0;
  practice.favs[q.id] = was ? 0 : 1; // 先本地后请求，失败回滚
  try {
    if (was) await removeFavorite(q.id);
    else await addFavorite(q.id);
    MessagePlugin.success(was ? '已取消收藏' : '已收藏');
  } catch (err) {
    practice.favs[q.id] = was;
    MessagePlugin.error(err.message || '操作失败');
  }
}

// switchView 答题/背题切换：两模式出参不同，批次缓存整体作废重拉。
async function switchView(v) {
  if (v === practice.viewMode) return;
  const old = practice.viewMode;
  const oldCache = practice.cache;
  practice.cache = {};
  practice.viewMode = v;
  try {
    await ensureBatch(practice.idx);
    practice.sel = [];
  } catch (err) {
    practice.viewMode = old;
    practice.cache = oldCache;
    MessagePlugin.error(err.message || '切换失败');
  }
}

/* ================= 刷题：断点 / 答题卡 / 设置 / 解析 ================= */

// syncSeq 顺序练习断点上报（防抖 500ms，同值跳过）。
function syncSeq() {
  if (practice.mode !== 'seq' || !bankId.value) return;
  if (seqTimer) clearTimeout(seqTimer);
  seqTimer = setTimeout(() => {
    seqTimer = 0;
    if (seqSent === practice.idx) return;
    seqSent = practice.idx;
    savePracticeProgress({ bankId: bankId.value, idx: practice.idx }).catch(() => {});
  }, 500);
}

// flushSeq 离开页面兜底补发断点（fire-and-forget）。
function flushSeq() {
  if (!seqTimer) return;
  clearTimeout(seqTimer);
  seqTimer = 0;
  if (practice.mode !== 'seq' || !bankId.value || seqSent === practice.idx) return;
  seqSent = practice.idx;
  savePracticeProgress({ bankId: bankId.value, idx: practice.idx }).catch(() => {});
}

// sheetSections 答题卡分题型分组。
const sheetSections = computed(() => {
  const out = [];
  ['single', 'multiple', 'judge'].forEach((t) => {
    const items = [];
    practice.outline.forEach((o, i) => {
      if ((o.type || 'single') !== t) return;
      let st = '';
      if (!isRecite.value) {
        const my = practice.myRight[o.id];
        if (my !== undefined) st = my ? 'ok' : 'bad';
      }
      items.push({ i, st });
    });
    if (items.length) out.push({ type: t, items });
  });
  return out;
});

function openSheet() {
  sheetVisible.value = true;
}

function onSheetJump(i) {
  sheetVisible.value = false;
  if (i !== practice.idx) go(i);
}

async function onResetRecords() {
  if (isRecite.value || !counts.value.answered) return;
  const confirmed = await confirmDanger(
    '清空做题记录？',
    '仅清空本题库的练习记录与进度，错题本保留。',
    '确认清空'
  );
  if (!confirmed) return;
  try {
    await resetPractice({ bankId: bankId.value });
  } catch (err) {
    MessagePlugin.error(err.message || '清空失败');
    return;
  }
  clearSession();
  seqSent = 0;
  sheetVisible.value = false;
  try {
    const seqSaved = await fetchOutline();
    if (!practice.outline.length) {
      emptyText.value = '该题库暂无题目';
      loadBanks(true);
      return;
    }
    if (practice.mode === 'seq') {
      practice.idx = Math.min(
        Math.max(seqSaved === null ? 0 : seqSaved, 0),
        practice.outline.length - 1
      );
      seqSent = practice.idx;
    }
    await ensureBatch(practice.idx);
  } catch (err) {
    MessagePlugin.error(err.message || '加载失败');
    return;
  }
  loadBanks(true);
  MessagePlugin.success('已清空做题记录');
}

// saveSettings 刷题设置持久化（localStorage）。
function saveSettings() {
  try {
    localStorage.setItem('quiz.practice.settings', JSON.stringify(practice.settings));
  } catch (e) {
    // localStorage 不可用时忽略
  }
}

// confirmDanger 危险操作确认（Promise 化 t-dialog）。
function confirmDanger(title, content, confirmText) {
  return new Promise((resolve) => {
    const dlg = DialogPlugin.confirm({
      header: title,
      body: content,
      confirmBtn: { content: confirmText, theme: 'danger' },
      cancelBtn: { content: '取消' },
      onConfirm: () => {
        dlg.destroy();
        resolve(true);
      },
      onCancel: () => {
        dlg.destroy();
        resolve(false);
      },
      onClose: () => {
        dlg.destroy();
        resolve(false);
      },
    });
  });
}

// openAnalysisEdit 修改解析（全员可改）。
function openAnalysisEdit() {
  const q = curQ.value;
  if (!q) return;
  anaText.value = analysisText.value || '';
  anaVisible.value = true;
}

async function saveAnalysis() {
  const q = curQ.value;
  if (!q) return;
  try {
    const data = await updateAnalysis({ questionId: q.id, analysis: anaText.value.trim() });
    const val = (data && data.analysis) || '';
    Object.keys(practice.cache).forEach((s) => {
      (practice.cache[s] || []).forEach((x) => {
        if (x && x.id === q.id) x.analysis = val || null;
      });
    });
    if (practice.results[q.id]) practice.results[q.id].analysis = val;
    MessagePlugin.success(val ? '解析已更新' : '已清空解析，将重新生成');
    anaVisible.value = false;
  } catch (err) {
    MessagePlugin.error(err.message || '保存失败');
  }
}

/* ================= 题库池 / 题库 CRUD ================= */

async function openPool() {
  poolVisible.value = true;
  poolLoading.value = true;
  try {
    const data = await listBankPool();
    pool.value = data.list || [];
  } catch (err) {
    MessagePlugin.error(err.message || '题库池加载失败');
  } finally {
    poolLoading.value = false;
  }
}

async function onJoin(b) {
  try {
    await joinBank(b.id);
    b.added = 1;
    MessagePlugin.success(`已加入「${b.name}」`);
    loadBanks(true);
  } catch (err) {
    MessagePlugin.error(err.message || '加入失败');
  }
}

async function onLeave(b) {
  try {
    await leaveBank(b.id);
    b.added = 0;
    MessagePlugin.success(`已退出「${b.name}」`);
    loadBanks(true);
  } catch (err) {
    MessagePlugin.error(err.message || '操作失败');
  }
}

// openBankDialog 题库编辑弹层（manage=true 来自管理页签）。
const bankFromManage = ref(false);
function openBankDialog(bank, fromManage = false) {
  bankFromManage.value = fromManage;
  bankForm.id = bank ? bank.id : 0;
  bankForm.name = bank ? bank.name : '';
  bankForm.description = bank ? bank.description || '' : '';
  bankForm.scope = bank ? bank.scope || 'team' : 'team';
  bankVisible.value = true;
}

async function saveBank() {
  const name = bankForm.name.trim();
  if (!name) {
    MessagePlugin.warning('请输入题库名称');
    return;
  }
  const payload = { name, description: bankForm.description.trim(), scope: bankForm.scope };
  try {
    if (bankForm.id) {
      await updateBank(bankForm.id, payload);
      MessagePlugin.success('题库已更新');
    } else {
      await createBank(payload);
      MessagePlugin.success('题库已创建');
    }
    bankVisible.value = false;
    loadBanks(true);
    if (bankFromManage.value) loadManage(true);
    if (bankForm.id === bankId.value) loadHome();
  } catch (err) {
    MessagePlugin.error(err.message || '保存失败');
  }
}

async function onDeleteBank(b) {
  const confirmed = await confirmDanger(
    `删除题库「${b.name}」？`,
    '将连带删除全部题目、答题记录、错题、收藏与订阅，不可恢复。',
    '确认删除'
  );
  if (!confirmed) return;
  try {
    await deleteBank(b.id);
    MessagePlugin.success('题库已删除');
    if (mgBank.value && mgBank.value.id === b.id) mgBank.value = null;
    loadManage(true);
    loadBanks(true);
  } catch (err) {
    MessagePlugin.error(err.message || '删除失败');
  }
}

/* ================= Excel 导入 / 模板 ================= */

function openImport() {
  if (!mgBank.value) {
    MessagePlugin.warning('请先在列表中点「题目」选择目标题库');
    return;
  }
  importErrors.value = [];
  importVisible.value = true;
}

function onImportFile(e) {
  const file = e.target.files && e.target.files[0];
  if (importFileEl.value) importFileEl.value.value = '';
  if (!file || !mgBank.value) return;
  runImport(file);
}

async function runImport(file) {
  importing.value = true;
  importErrors.value = [];
  try {
    const data = await importBank(mgBank.value.id, file);
    MessagePlugin.success(
      `已导入 ${data.imported} 道题目` + (data.queued ? `，${data.queued} 题进入 AI 解析队列` : '')
    );
    importVisible.value = false;
    loadManage(true);
    loadBanks(true);
  } catch (err) {
    if (err.code === 40031 && err.data && Array.isArray(err.data.errors)) {
      importErrors.value = err.data.errors;
    } else {
      MessagePlugin.error(err.message || '导入失败');
    }
  } finally {
    importing.value = false;
  }
}

async function downloadTemplate() {
  try {
    const blob = await fetchTemplateBlob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = '题库导入模板.xlsx';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (err) {
    MessagePlugin.error(err.message || '模板下载失败');
  }
}

/* ================= 题库管理 ================= */

async function loadManage(quiet) {
  if (!quiet) mgLoading.value = true;
  try {
    const data = await listManageBanks();
    mgList.value = data.list || [];
  } catch (err) {
    if (!quiet) MessagePlugin.error(err.message || '管理列表加载失败');
  } finally {
    mgLoading.value = false;
  }
}

function switchManageTab() {
  tab.value = 'manage';
  if (!mgList.value.length) loadManage();
}

// openQuestions 打开题目面板并加载第一页。
function openQuestions(b) {
  mgBank.value = b;
  mq.page = 1;
  mq.q = '';
  loadMq(1);
  startMqPoll();
}

const mqPages = computed(() => Math.max(1, Math.ceil(mq.total / mq.size)));

async function loadMq(page) {
  if (!mgBank.value) return;
  mqLoading.value = true;
  try {
    const data = await listQuestions(mgBank.value.id, { page, size: mq.size, q: mq.q.trim() });
    mq.list = data.list || [];
    mq.total = data.total || 0;
    mq.page = page;
  } catch (err) {
    MessagePlugin.error(err.message || '题目列表加载失败');
  } finally {
    mqLoading.value = false;
  }
}

function onSearchQuestions() {
  if (mqSearchTimer) clearTimeout(mqSearchTimer);
  mqSearchTimer = setTimeout(() => loadMq(1), 300);
}

// startMqPoll 解析生成中时轮询题目状态（无 pending 即停）。
function startMqPoll() {
  stopMqPoll();
  mqPollTimer = setInterval(() => {
    if (!mgBank.value || tab.value !== 'manage') return;
    const pending = mq.list.some((q) => q.analysisStatus === 'pending');
    if (pending) loadMq(mq.page);
    else if ((home.value && home.value.analysis && home.value.analysis.pending) || false) {
      // 保持静默：仅题目页内有 pending 才轮询
    }
  }, 3000);
}

function stopMqPoll() {
  if (mqPollTimer) {
    clearInterval(mqPollTimer);
    mqPollTimer = 0;
  }
}

async function onAnalyzeRetry() {
  if (!mgBank.value) return;
  try {
    const data = await analyzeRetry(mgBank.value.id);
    MessagePlugin.success(`已重新入队 ${data.queued} 题`);
    loadMq(mq.page);
    loadManage(true);
  } catch (err) {
    MessagePlugin.error(err.message || '操作失败');
  }
}

// openQuestionDialog 题目编辑弹层。
function openQuestionDialog(q) {
  questionForm.id = q ? q.id : 0;
  questionForm.type = q ? q.type : 'single';
  questionForm.content = q ? q.content : '';
  questionForm.options = q
    ? q.type === 'judge'
      ? ['正确', '错误']
      : (q.options || []).slice()
    : ['', ''];
  questionForm.answer = q ? q.answer || '' : '';
  questionForm.analysis = q && q.analysis ? q.analysis : '';
  questionVisible.value = true;
}

// judgeAnswer 判断题单选值。
const judgeAnswer = computed({
  get: () => (questionForm.answer === 'B' ? 'B' : 'A'),
  set: () => {},
});

function onJudgeAnswer(v) {
  questionForm.answer = v;
}

function onTypeChange(t) {
  if (t === 'judge') {
    questionForm.options = ['正确', '错误'];
    questionForm.answer = questionForm.answer ? questionForm.answer.slice(0, 1) : 'A';
  } else if (questionForm.options.length < 2 || questionForm.options.join('') === '正确错误') {
    questionForm.options = ['', ''];
    questionForm.answer = '';
  }
}

function toggleAnswerLetter(l) {
  const i = questionForm.answer.indexOf(l);
  if (questionForm.type === 'single') {
    questionForm.answer = l;
    return;
  }
  questionForm.answer =
    i >= 0
      ? questionForm.answer
          .split('')
          .filter((x) => x !== l)
          .sort()
          .join('')
      : (questionForm.answer + l).split('').sort().join('');
}

async function saveQuestion() {
  const content = questionForm.content.trim();
  if (!content) {
    MessagePlugin.warning('请填写题干');
    return;
  }
  let options = [];
  if (questionForm.type === 'judge') {
    options = ['正确', '错误'];
    if (!questionForm.answer) questionForm.answer = 'A';
  } else {
    options = questionForm.options.map((t) => t.trim()).filter(Boolean);
    if (options.length < 2) {
      MessagePlugin.warning('选项至少填写 2 个');
      return;
    }
    if (!questionForm.answer) {
      MessagePlugin.warning('请选择答案');
      return;
    }
  }
  const payload = {
    type: questionForm.type,
    content,
    options,
    answer: questionForm.answer.split('').sort().join(''),
    analysis: questionForm.analysis.trim(),
  };
  try {
    if (questionForm.id) {
      await updateQuestion(questionForm.id, payload);
      MessagePlugin.success('题目已更新');
    } else {
      await createQuestion(mgBank.value.id, payload);
      MessagePlugin.success('题目已添加');
    }
    questionVisible.value = false;
    loadMq(mq.page);
    loadManage(true);
  } catch (err) {
    MessagePlugin.error(err.message || '保存失败');
  }
}

async function onDeleteQuestion(q) {
  const confirmed = await confirmDanger(
    '删除该题目？',
    '将连带删除该题的答题记录、错题与收藏。',
    '确认删除'
  );
  if (!confirmed) return;
  try {
    await deleteQuestion(q.id);
    MessagePlugin.success('题目已删除');
    loadMq(mq.page);
    loadManage(true);
  } catch (err) {
    MessagePlugin.error(err.message || '删除失败');
  }
}

/* ================= 生命周期 ================= */

onMounted(() => {
  try {
    const raw = localStorage.getItem('quiz.practice.settings');
    if (raw) {
      const saved = JSON.parse(raw);
      if (saved && typeof saved === 'object') {
        practice.settings.shuffleOpt = saved.shuffleOpt !== false;
        practice.settings.autoNext = !!saved.autoNext;
      }
    }
  } catch (e) {
    // 配置损坏时用默认值
  }
  loadBanks();
});

onBeforeUnmount(() => {
  flushSeq();
  stopMqPoll();
  if (mqSearchTimer) clearTimeout(mqSearchTimer);
  if (autoTimer.value) clearTimeout(autoTimer.value);
});

// 页面隐藏/离开时补发未上报的断点（防抖窗口内退出兜底）
if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', flushSeq);
}
</script>

<style scoped>
/* 页签：文字 + 激活态（与出工日志 wl-tabs 同族） */
.qz-tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 14px;
  border-bottom: 1px solid var(--tk-line);
}
.qz-tab {
  appearance: none;
  border: none;
  background: none;
  padding: 10px 16px;
  font-size: 14px;
  font-weight: 600;
  color: var(--tk-sub);
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}
.qz-tab:hover {
  color: var(--tk-brand);
}
.qz-tab.on {
  color: var(--tk-brand);
  border-bottom-color: var(--tk-brand);
}

/* 题库行 */
.bank-row {
  cursor: pointer;
}
.bank-row .tag {
  margin-left: 6px;
}
.tag {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  vertical-align: 1px;
}
.tag-blue {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.tag-ink {
  background: var(--tk-fill);
  color: var(--tk-sub);
  border: 1px solid var(--tk-line);
}
.tag-orange {
  background: var(--tk-yellow-soft);
  color: #d25f00;
}
.tag-red {
  background: var(--tk-red-soft);
  color: var(--tk-red);
}
.badge-outline {
  display: inline-block;
  margin-left: 8px;
  padding: 0 8px;
  border: 1px solid var(--tk-brand);
  color: var(--tk-brand);
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
}

/* 题库主页 */
.bh-head {
  padding: 4px 2px 14px;
}
.bh-name {
  font-size: 16px;
}
.bh-desc {
  margin: 6px 0 0;
  color: var(--tk-sub);
  font-size: 13px;
}
.bh-stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
}
.bh-stats .cell {
  background: var(--tk-fill);
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  padding: 12px;
  text-align: center;
}
.bh-stats .cell b {
  display: block;
  font-size: 20px;
  font-weight: 700;
}
.bh-stats .cell b.brand {
  color: var(--tk-brand);
}
.bh-stats .cell span {
  font-size: 12px;
  color: var(--tk-ph);
}
.bh-grid {
  margin-top: 14px;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.bh-entry {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  cursor: pointer;
  transition:
    box-shadow 0.2s var(--tk-ease),
    border-color 0.2s var(--tk-ease);
}
.bh-entry:hover {
  border-color: var(--tk-brand);
  box-shadow: var(--tk-shadow-hover);
}
.bh-entry .tx {
  flex: 1;
  min-width: 0;
}
.bh-entry .tx b {
  font-size: 14px;
}
.bh-entry .tx p {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--tk-ph);
}
.bh-entry .chev {
  color: var(--tk-dash);
  font-size: 18px;
}
.ai-notice {
  margin-top: 14px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  font-size: 12px;
}
.ai-notice .txt {
  color: #d25f00;
}
.ai-notice .progress {
  flex: 1;
}

/* 进度条 */
.progress {
  height: 6px;
  background: var(--tk-fill);
  border: 1px solid var(--tk-line);
  border-radius: 4px;
  overflow: hidden;
}
.progress.slim {
  height: 5px;
}
.progress i {
  display: block;
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.25s var(--tk-ease);
}

/* 刷题视图 */
.pv-top {
  display: flex;
  align-items: center;
  gap: 12px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--tk-line);
}
.pv-back {
  color: var(--tk-brand);
  cursor: pointer;
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
}
.pv-title {
  font-size: 14px;
}
.pv-vm {
  display: inline-flex;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
}
.pv-vm i {
  font-style: normal;
  padding: 4px 12px;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
}
.pv-vm i.on {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
  font-weight: 600;
}
.pv-pg {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 180px;
}
.pv-pg .progress {
  flex: 1;
}
.pv-pg b {
  font-size: 12px;
  white-space: nowrap;
}

.pv-col {
  padding: 16px 2px;
}
.pv-stem {
  font-size: 15px;
  line-height: 1.7;
  margin-bottom: 14px;
}
.pv-stem .tag {
  margin-right: 8px;
}
.stem-text {
  white-space: pre-wrap;
}
.pv-opt {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  margin-bottom: 10px;
  cursor: pointer;
  transition:
    border-color 0.15s var(--tk-ease),
    background 0.15s var(--tk-ease);
}
.pv-opt:hover {
  border-color: var(--tk-brand);
}
.pv-opt.lock {
  cursor: default;
}
.pv-opt .ab {
  flex-shrink: 0;
  width: 26px;
  height: 26px;
  border: 1px solid var(--tk-line);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-sub);
}
.pv-opt.sq .ab {
  border-radius: 4px;
}
.pv-opt.sel {
  border-color: var(--tk-brand);
  background: var(--tk-blue-soft);
}
.pv-opt.sel .ab {
  border-color: var(--tk-brand);
  background: var(--tk-brand);
  color: #fff;
}
.pv-opt.right {
  border-color: var(--tk-green);
  background: var(--tk-green-soft);
}
.pv-opt.right .ab {
  border-color: var(--tk-green);
  background: var(--tk-green);
  color: #fff;
}
.pv-opt.wrong {
  border-color: var(--tk-red);
  background: var(--tk-red-soft);
}
.pv-opt.wrong .ab {
  border-color: var(--tk-red);
  background: var(--tk-red);
  color: #fff;
}
.pv-opt .tx {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  line-height: 1.6;
}

/* 判分结果（浅底 + 左色条直角） */
.r-block {
  margin: 6px 0 12px;
}
.r-line {
  background: var(--tk-fill);
  border-left: 3px solid var(--tk-brand);
  padding: 12px 14px;
}
.r-main {
  display: flex;
  gap: 24px;
  font-size: 13px;
}
.r-main b {
  margin-left: 6px;
}
.r-main b.ok {
  color: var(--tk-green);
}
.r-main b.bad {
  color: var(--tk-red);
}
.r-sub {
  margin-top: 6px;
  font-size: 12px;
  color: var(--tk-sub);
}
.r-sub.orange {
  color: #d25f00;
}

/* 试题详解 */
.pv-ana {
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  overflow: hidden;
}
.pv-ana .hd {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  background: var(--tk-fill);
  font-size: 13px;
  font-weight: 600;
}
.pv-ana-edit {
  margin-left: auto;
  appearance: none;
  border: 1px solid var(--tk-line);
  background: #fff;
  border-radius: 4px;
  padding: 2px 10px;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
}
.pv-ana-edit:hover {
  color: var(--tk-brand);
  border-color: var(--tk-brand);
}
.pv-ana .bd {
  padding: 12px 14px;
  font-size: 13px;
  line-height: 1.8;
  white-space: pre-wrap;
}
.pv-ana .ft {
  padding: 8px 14px;
  border-top: 1px dashed var(--tk-line);
  font-size: 11px;
  color: var(--tk-ph);
}

/* 底部操作条 */
.pv-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-top: 12px;
  border-top: 1px solid var(--tk-line);
}
.pv-chipbtn {
  appearance: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px solid var(--tk-line);
  background: #fff;
  border-radius: 8px;
  padding: 5px 12px;
  font-size: 12px;
  color: var(--tk-sub);
  cursor: pointer;
}
.pv-chipbtn:hover {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.pv-chipbtn.faved {
  border-color: var(--tk-yellow);
  color: #d25f00;
  background: var(--tk-yellow-soft);
}
.qz-ok {
  color: var(--tk-green);
  font-weight: 700;
}
.qz-bad {
  color: var(--tk-red);
  font-weight: 700;
  margin-left: 4px;
}
.pv-foot .sp {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 10px;
}
.pv-hint {
  font-size: 12px;
  color: var(--tk-ph);
}

.empty {
  padding: 40px 0;
  text-align: center;
  color: var(--tk-sub);
}
.empty p {
  margin: 6px 0;
}

/* 管理表 */
.qtable {
  overflow-x: auto;
}
.qtable .tr {
  display: grid;
  grid-template-columns: 1.2fr 1.6fr 1.6fr 1fr 1fr;
  gap: 10px;
  align-items: center;
  padding: 10px 8px;
  border-bottom: 1px solid var(--tk-line);
  font-size: 13px;
}
.qtable .tr.hd {
  color: var(--tk-ph);
  font-size: 12px;
  font-weight: 600;
}
.qtable .c-q {
  color: var(--tk-sub);
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.qtable .c-s {
  color: var(--tk-ph);
  font-size: 12px;
}
.qtable .c-o {
  display: flex;
  gap: 2px;
  white-space: nowrap;
}
.an-stat {
  display: block;
  margin-top: 2px;
  color: var(--tk-ph);
  font-size: 11px;
}
.pager {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  padding: 12px 4px 0;
  font-size: 12px;
  color: var(--tk-sub);
}
.mq-search {
  width: 220px;
}

/* 题库池弹层 */
.pool-row {
  cursor: default;
}

/* 表单小件 */
.opt-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  width: 100%;
}
.opt-letter {
  flex-shrink: 0;
  width: 22px;
  height: 22px;
  border: 1px solid var(--tk-line);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 600;
  color: var(--tk-sub);
}
.ans-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.ans-chip {
  appearance: none;
  width: 34px;
  height: 34px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  background: #fff;
  font-size: 13px;
  font-weight: 600;
  color: var(--tk-sub);
  cursor: pointer;
}
.ans-chip.on {
  background: var(--tk-brand);
  border-color: var(--tk-brand);
  color: #fff;
}
.ans-chip:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

/* 导入弹层 */
.imp-err-tip {
  background: var(--tk-yellow-soft);
  border-left: 3px solid var(--tk-yellow);
  padding: 10px 12px;
  font-size: 12px;
  color: #d25f00;
  margin-bottom: 12px;
}
.imp-file {
  display: none;
}
.imp-errors {
  margin-top: 12px;
  background: var(--tk-red-soft);
  border-left: 3px solid var(--tk-red);
  padding: 10px 12px;
}
.imp-errors-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--tk-red);
  margin-bottom: 6px;
}
.imp-error-row {
  font-size: 12px;
  color: var(--tk-sub);
  line-height: 1.8;
}

/* 设置行 */
.pv-set-row {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--tk-line);
}
.pv-set-row:last-child {
  border-bottom: none;
}
.pv-set-row .tx {
  flex: 1;
}
.pv-set-row .tx b {
  font-size: 14px;
}
.pv-set-row .tx p {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--tk-ph);
}

/* 答题卡 */
.pv-legend {
  display: flex;
  gap: 18px;
  font-size: 12px;
  color: var(--tk-sub);
}
.pv-legend i {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--tk-dash);
  margin-right: 5px;
}
.pv-legend i.g {
  background: var(--tk-green);
}
.pv-legend i.r {
  background: var(--tk-red);
}
.f-label {
  margin: 14px 0 8px;
  font-size: 13px;
  font-weight: 600;
}
.f-label .muted {
  font-weight: 400;
  margin-left: 4px;
}
.pv-sheet-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.pv-chip {
  width: 36px;
  height: 36px;
  border: 1px solid var(--tk-line);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  cursor: pointer;
  color: var(--tk-sub);
}
.pv-chip.ok {
  background: var(--tk-green-soft);
  border-color: var(--tk-green);
  color: var(--tk-green);
}
.pv-chip.bad {
  background: var(--tk-red-soft);
  border-color: var(--tk-red);
  color: var(--tk-red);
}
.pv-chip.cur {
  border-color: var(--tk-brand);
  color: var(--tk-brand);
  font-weight: 700;
}
.pv-clear {
  margin-top: 18px;
  text-align: center;
  padding: 10px;
  border: 1px dashed var(--tk-line);
  color: var(--tk-red);
  font-size: 13px;
  cursor: pointer;
}
.pv-clear.disabled {
  color: var(--tk-dash);
  cursor: not-allowed;
}
</style>
