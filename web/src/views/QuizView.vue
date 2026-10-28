<template>
  <div class="qz-root">
    <!-- 页签：刷题练习 / 题库管理（文字 + 滑动指示条；题库管理仅管理角色可见） -->
    <div ref="tabsEl" class="qz-tabs fade-up">
      <button :class="{ on: tab === 'practice' }" @click="goTab('practice')">刷题练习</button>
      <button v-if="isManager" :class="{ on: tab === 'manage' }" @click="goTab('manage')">
        题库管理
      </button>
      <span class="qz-tab-ind" :style="tabIndStyle"></span>
    </div>

    <!-- ================= 刷题练习：右卡我的题库 + 左卡题库主页 / 刷题页 ================= -->
    <div v-show="tab === 'practice'" class="qz-wrap fade-up">
      <!-- 右卡：我的题库 -->
      <aside class="card qz-side">
        <div class="side-head">
          <b>我的题库</b>
          <button class="mini-btn" title="题库池" @click="openPool">
            <span v-html="icon('compass', 15)"></span>
          </button>
        </div>
        <div class="side-scroll">
          <template v-if="banksLoading && !banks.length">
            <div class="skeleton sk-item"></div>
            <div class="skeleton sk-item"></div>
            <div class="skeleton sk-item"></div>
          </template>
          <div v-else-if="!banks.length" class="empty">
            <span v-html="icon('book', 40)"></span>
            <p>还没有题库</p>
            <p style="font-size: 11px">点右上角罗盘图标，从题库池添加</p>
          </div>
          <template v-else>
            <div
              v-for="b in banks"
              :key="b.id"
              class="side-item"
              :class="{ on: b.id === bankId }"
              @click="selectBank(b)"
            >
              <span v-html="icon('book', 17)"></span>
              <div class="tx">
                <b>{{ b.name }}</b>
                <span>
                  <span class="tag" :class="b.scope === 'all' ? 'tag-orange' : 'tag-navy'">
                    {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
                  </span>
                  {{ b.questionCount }} 题 ·
                  {{ b.answeredCount ? '已练 ' + b.answeredCount : '未开始' }}
                </span>
                <div v-if="b.questionCount" class="pg">
                  <div class="progress"><i :style="{ width: bankPct(b) + '%' }"></i></div>
                  <span>{{ b.answeredCount }}/{{ b.questionCount }}</span>
                </div>
              </div>
            </div>
          </template>
        </div>
      </aside>

      <!-- 左卡：题库主页 / 刷题页 -->
      <section class="card qz-main">
        <!-- 题库主页（统计行 + 四宫格入口） -->
        <div v-if="!practice.started" class="pane home-pane">
          <template v-if="homeLoading && !home">
            <div class="skeleton" style="height: 120px; border-radius: 8px"></div>
            <div class="skeleton" style="height: 210px; border-radius: 8px; margin-top: 14px"></div>
          </template>
          <div v-else-if="!bankId || !home" class="empty" style="margin: auto">
            <span v-html="icon(homeErr ? 'warn' : 'book', 44)"></span>
            <p>{{ homeErr || '还没有题库' }}</p>
            <p v-if="!homeErr" style="font-size: 11px">点右卡右上角罗盘图标，从题库池添加题库</p>
          </div>
          <template v-else>
            <div class="mg-head">
              <div class="tx">
                <b>
                  {{ home.name }}
                  <span class="tag" :class="home.scope === 'all' ? 'tag-orange' : 'tag-navy'">
                    {{ home.scope === 'all' ? '全部池' : home.teamName || '班组池' }}
                  </span>
                  <span v-if="aiReady" class="badge-outline">AI 解析</span>
                </b>
                <p>{{ home.description || '' }}</p>
              </div>
            </div>
            <div class="bh-stats">
              <div class="cell">
                <b>{{ home.questionCount }}</b>
                <span>题目总数</span>
              </div>
              <div class="cell">
                <b
                  ><em>{{ home.answeredCount }}</em></b
                >
                <span>已练题数</span>
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
                <b>{{ home.wrongCount }}</b>
                <span>错题待攻克</span>
              </div>
            </div>
            <div class="bh-grid">
              <div v-for="e in bankEntries" :key="e.m" class="bh-entry" @click="startPractice(e.m)">
                <div class="ib"><span v-html="icon(entryIcons[e.m], 20)"></span></div>
                <div class="tx">
                  <b>
                    {{ e.t }}
                    <span v-if="e.cnt" class="tag" :class="e.cntCls">{{ e.cnt }}</span>
                  </b>
                  <p>{{ e.d }}</p>
                </div>
                <span v-html="icon('chevron-right', 16)"></span>
              </div>
            </div>
            <div v-if="aiPending" class="ai-notice">
              <span v-html="icon('lightbulb', 15)"></span>
              <span class="txt">AI 解析进行中，期间可正常刷题</span>
              <div class="progress"><i :style="{ width: aiBarPct + '%' }"></i></div>
              <b>{{ aiDoneCnt }} / {{ aiTodoCnt }}</b>
            </div>
          </template>
        </div>

        <!-- 刷题页（左卡接管） -->
        <div v-else class="pane pv-pane">
          <div class="pv-top">
            <span class="pv-back" @click="backToHome">
              <span v-html="icon('chevron-left', 15)"></span>题库主页
            </span>
            <b class="pv-title">{{ home ? home.name : '' }} · {{ modeText(practice.mode) }}</b>
            <span class="pv-vm">
              <i :class="{ on: practice.viewMode === 'answer' }" @click="switchView('answer')"
                >答题</i
              >
              <i :class="{ on: practice.viewMode === 'recite' }" @click="switchView('recite')"
                >背题</i
              >
            </span>
            <div class="pv-pg">
              <div class="progress"><i :style="{ width: pct + '%' }"></i></div>
              <b>{{ practice.idx + 1 }} / {{ practice.outline.length }}</b>
            </div>
          </div>

          <template v-if="!curQ">
            <div v-if="emptyText" class="empty" style="margin: auto">
              <span v-html="icon('book', 44)"></span>
              <p>{{ emptyText }}</p>
              <button class="btn btn-sm" @click="backToHome">返回题库主页</button>
            </div>
            <div v-else class="loading" style="margin: auto">
              <span class="spin"></span>题目加载中…
            </div>
          </template>

          <template v-else>
            <div ref="pvColEl" class="pv-col">
              <div :key="curQ.id" class="pv-col-in">
                <div class="pv-stem">
                  <span class="tag" :class="curQ.type === 'judge' ? 'tag-navy' : 'tag-orange'">
                    {{ typeText(curQ.type) }}
                  </span>
                  {{ curQ.content }}
                </div>
                <div
                  v-for="(opt, i) in displayOptions"
                  :key="i"
                  class="pv-opt"
                  :class="[opt.cls, { lock: submitted, sq: curQ.type === 'multiple' }]"
                  @click="onOption(opt.letter)"
                >
                  <span class="ab" v-html="optInnerHtml(opt)"></span>
                  <span class="tx">{{ opt.text }}</span>
                </div>
              </div>

              <!-- 判分结果（正确答案/您的选择 + 本人/全员统计 + 错题本动态） -->
              <div v-if="!isRecite && curResult" class="r-line">
                <div class="r-main">
                  <span
                    >正确答案<b class="ok">{{ resultAnswerText }}</b></span
                  >
                  <span v-if="resultYourText">
                    您的选择<b :class="curResult.right ? 'ok' : 'bad'">{{ resultYourText }}</b>
                  </span>
                </div>
                <div v-if="curResult.stats && curResult.stats.myTimes" class="r-sub">
                  本人作答 {{ curResult.stats.myTimes }} 次 · 正确率
                  {{ curResult.stats.myRightRate }}%
                </div>
                <div v-if="curResult.stats && curResult.stats.allTimes" class="r-sub">
                  全员作答 {{ curResult.stats.allTimes }} 次 · 正确率
                  {{ curResult.stats.allRightRate }}%
                </div>
                <div v-if="wrongBanner" class="r-sub orange">{{ wrongBanner }}</div>
              </div>

              <!-- 试题详解 -->
              <div v-if="submitted" class="pv-ana">
                <div class="hd">
                  <span v-html="icon('lightbulb', 15)"></span>试题详解
                  <button
                    class="pv-ana-edit"
                    title="修改解析（对全员生效）"
                    @click="openAnalysisEdit"
                  >
                    <span v-html="icon('edit', 14)"></span>
                  </button>
                </div>
                <div class="bd">{{ analysisText || '解析生成中，稍后可查看' }}</div>
                <div v-if="analysisText" class="ft">解析内容仅供参考</div>
              </div>
            </div>

            <!-- 底栏：收藏 / 对错计数 / 答题卡 / 设置 + 主按钮 -->
            <div class="pv-foot">
              <button class="pv-chipbtn" :class="{ faved: curFav }" @click="toggleFav">
                <span v-html="icon('star', 13)"></span>收藏
              </button>
              <span class="pv-chipbtn">
                <span class="qz-ok" v-html="icon('check', 12)"></span>{{ counts.right }}
                <span class="qz-bad" v-html="icon('close', 12)"></span>{{ counts.wrong }}
              </span>
              <button class="pv-chipbtn" @click="openSheet">
                <span v-html="icon('view-module', 13)"></span>答题卡 {{ counts.answered }}/{{
                  practice.outline.length
                }}
              </button>
              <button class="pv-chipbtn" @click="settingsVisible = true">
                <span v-html="icon('setting', 13)"></span>设置
              </button>
              <span class="sp">
                <button
                  class="btn btn-sm"
                  :disabled="practice.idx <= 0"
                  @click="go(practice.idx - 1)"
                >
                  上一题
                </button>
                <button
                  v-if="!submitted && curQ.type === 'multiple'"
                  class="btn btn-sm btn-primary"
                  :disabled="!practice.sel.length"
                  @click="submitCur"
                >
                  提交答案
                </button>
                <button v-else-if="submitted" class="btn btn-sm btn-primary" @click="nextOrFinish">
                  {{ practice.idx >= practice.outline.length - 1 ? '完成' : '下一题' }}
                </button>
                <span v-else class="pv-hint">点选即判分 · ← → 方向键切题</span>
              </span>
            </div>
          </template>
        </div>
      </section>
    </div>

    <!-- ================= 题库管理：右卡题库列表 + 左卡题目表格 / 导入 / AI 解析 ================= -->
    <div v-show="tab === 'manage'" class="qz-wrap fade-up">
      <!-- 右卡：题库管理（新建 / 导入；超管另见班组切换） -->
      <aside class="card qz-side">
        <div class="side-head">
          <b>题库管理</b>
          <button class="mini-btn" title="新建题库" @click="openBankDialog(null, true)">
            <span v-html="icon('add', 15)"></span>
          </button>
          <button class="mini-btn" title="导入 Excel" @click="openImport">
            <span v-html="icon('upload', 15)"></span>
          </button>
        </div>
        <div v-if="isAdmin && teams.length" class="side-team">
          <select
            v-model.number="teamId"
            title="仅超级管理员可切换班组；其余角色固定为本班组"
            @change="onTeamChange"
          >
            <option v-for="t in teams" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
        </div>
        <div class="side-scroll">
          <template v-if="mgLoading && !mgList.length">
            <div class="skeleton sk-item"></div>
            <div class="skeleton sk-item"></div>
            <div class="skeleton sk-item"></div>
          </template>
          <div v-else-if="!mgList.length" class="empty">
            <span v-html="icon('book', 40)"></span>
            <p>暂无题库</p>
            <p style="font-size: 11px">点击右上角 + 新建题库，Excel 一键导入</p>
          </div>
          <template v-else>
            <div
              v-for="b in mgList"
              :key="b.id"
              class="side-item"
              :class="{ on: mgBank && b.id === mgBank.id }"
              @click="openQuestions(b)"
            >
              <span v-html="icon('book', 17)"></span>
              <div class="tx">
                <b>{{ b.name }}</b>
                <span>
                  <span class="tag" :class="b.scope === 'all' ? 'tag-orange' : 'tag-navy'">
                    {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
                  </span>
                  {{ mgMeta(b) }}
                </span>
              </div>
              <span class="ops">
                <button title="编辑题库" @click.stop="openBankDialog(b, true)">
                  <span v-html="icon('edit', 14)"></span>
                </button>
                <button title="删除题库" @click.stop="onDeleteBank(b)">
                  <span v-html="icon('delete', 14)"></span>
                </button>
              </span>
            </div>
          </template>
        </div>
      </aside>

      <!-- 左卡：头部按钮组 + AI 解析通知条 + 搜索 + 题目表格 + 分页 -->
      <section class="card qz-main">
        <div class="pane">
          <div class="mg-head">
            <div v-if="!mgBank" class="tx">
              <b>暂无题库</b>
              <p>点击右卡右上角 + 新建题库</p>
            </div>
            <div v-else class="tx">
              <b>
                {{ mgBank.name }}
                <span class="tag" :class="mgBank.scope === 'all' ? 'tag-orange' : 'tag-navy'">
                  {{ mgBank.scope === 'all' ? '全部池' : mgBank.teamName || '班组池' }}
                </span>
                <span class="badge-outline">AI 解析</span>
              </b>
              <p>{{ mgSubText(mgBank) }}</p>
            </div>
            <button class="btn btn-sm" @click="downloadTemplate">
              <span v-html="icon('download', 14)"></span>下载模板
            </button>
            <button class="btn btn-sm btn-accent" @click="openImport">
              <span v-html="icon('upload', 14)"></span>导入 Excel
            </button>
            <button
              class="btn btn-sm"
              :disabled="
                !mgBank ||
                bankAi(mgBank).pending !== 0 ||
                bankAi(mgBank).failed + bankAi(mgBank).none === 0
              "
              @click="onAnalyzeRetry"
            >
              <span v-html="icon('refresh', 14)"></span>
              {{ bankAi(mgBank).failed > 0 ? '重试失败解析' : '继续解析' }}
            </button>
            <button
              class="btn btn-sm btn-primary"
              :disabled="!mgBank"
              @click="openQuestionDialog(null)"
            >
              <span v-html="icon('add', 14)"></span>新增题目
            </button>
          </div>

          <div v-if="mgBank && bankAi(mgBank).pending" class="ai-notice">
            <span v-html="icon('lightbulb', 15)"></span>
            <span class="txt">「{{ mgBank.name }}」AI 解析进行中，期间可正常刷题</span>
            <div class="progress"><i :style="{ width: bankAi(mgBank).pct + '%' }"></i></div>
            <b>{{ bankAi(mgBank).x }} / {{ bankAi(mgBank).y }}</b>
          </div>

          <div class="mg-tools">
            <div class="mg-search">
              <span v-html="icon('search', 15)"></span>
              <input
                v-model="mq.q"
                class="input"
                placeholder="搜索题干"
                maxlength="50"
                @input="onSearchQuestions"
              />
            </div>
            <span class="mg-total">共 {{ mq.total }} 题</span>
          </div>

          <div class="qtable">
            <div class="tr hd">
              <span class="c-t">题型</span>
              <span class="c-q">题干</span>
              <span class="c-a">答案</span>
              <span class="c-s">解析状态</span>
              <span class="c-o">操作</span>
            </div>
            <template v-if="mqLoading && !mq.list.length">
              <div class="loading"><span class="spin"></span>题目加载中…</div>
            </template>
            <div v-else-if="!mgBank" class="empty">
              <span v-html="icon('book', 44)"></span>
              <p>暂无题库</p>
            </div>
            <div v-else-if="!mq.list.length" class="empty">
              <span v-html="icon('search', 44)"></span>
              <p>{{ mq.q ? '未搜到相关题目' : '暂无题目，点击右上角导入或新增' }}</p>
            </div>
            <template v-else>
              <div v-for="q in mq.list" :key="q.id" class="tr">
                <span class="tag c-t" :class="typeMeta(q.type).cls">{{
                  typeMeta(q.type).tag
                }}</span>
                <span class="c-q" :title="q.content">{{ q.content }}</span>
                <span class="c-a">{{ answerText(q.type, q.answer) }}</span>
                <span class="c-s">
                  <span
                    class="tag"
                    :class="[
                      'tag-' + stTag(q.analysisStatus).cls,
                      { 'tag-retry': q.analysisStatus === 'failed' },
                    ]"
                    :title="q.analysisStatus === 'failed' ? '点击重试解析' : ''"
                    @click="q.analysisStatus === 'failed' && onAnalyzeRetry()"
                  >
                    {{ stTag(q.analysisStatus).text }}
                  </span>
                </span>
                <span class="c-o">
                  <button title="编辑" @click="openQuestionDialog(q)">
                    <span v-html="icon('edit', 15)"></span>
                  </button>
                  <button title="删除" @click="onDeleteQuestion(q)">
                    <span v-html="icon('delete', 15)"></span>
                  </button>
                </span>
              </div>
            </template>
          </div>

          <div v-if="mqPages > 1" class="pager">
            <button :disabled="mq.page <= 1" @click="loadMq(mq.page - 1)">‹</button>
            <template v-for="(p, pi) in pagerNums" :key="pi">
              <button v-if="p === '…'" class="pg-dots" disabled>…</button>
              <button v-else :class="{ on: p === mq.page }" @click="loadMq(p)">{{ p }}</button>
            </template>
            <button :disabled="mq.page >= mqPages" @click="loadMq(mq.page + 1)">›</button>
          </div>
        </div>
      </section>
    </div>

    <!-- ================= 弹层：题库池 ================= -->
    <Transition name="qz-fade">
      <div v-if="poolVisible" class="mask" @click.self="poolVisible = false">
        <div class="modal" style="width: 520px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">题库池</div>
              <button class="m-x" @click="poolVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="imp-err-tip">
              从班组池 / 全部池把题库添加进「我的题库」，移出保留练习记录与错题
            </div>
            <template v-if="poolLoading">
              <div class="loading"><span class="spin"></span>加载中…</div>
            </template>
            <div v-else-if="!pool.length" class="empty">
              <span v-html="icon('book', 40)"></span>
              <p>暂无可添加的题库</p>
            </div>
            <template v-else>
              <div v-for="b in pool" :key="b.id" class="imp-err pool-row">
                <div class="pool-main">
                  <b>
                    {{ b.name }}
                    <span class="tag" :class="b.scope === 'all' ? 'tag-orange' : 'tag-navy'">
                      {{ b.scope === 'all' ? '全部池' : b.teamName || '班组池' }}
                    </span>
                  </b>
                  <p>{{ b.questionCount }} 题{{ b.description ? ' · ' + b.description : '' }}</p>
                </div>
                <button v-if="!b.added" class="btn btn-sm btn-accent" @click="onJoin(b)">
                  添加
                </button>
                <button v-else class="btn btn-sm" @click="onLeave(b)">移出</button>
              </div>
            </template>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：题库新增 / 编辑 ================= -->
    <Transition name="qz-fade">
      <div v-if="bankVisible" class="mask" @click.self="bankVisible = false">
        <div class="modal" style="width: 460px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">{{ bankForm.id ? '编辑题库' : '新建题库' }}</div>
              <button class="m-x" @click="bankVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="f-label">题库名称</div>
            <input
              v-model="bankForm.name"
              class="input"
              maxlength="50"
              placeholder="如：电力安全工作规程"
            />
            <div class="f-label">题库描述</div>
            <textarea
              v-model="bankForm.description"
              class="input"
              placeholder="选填：题库范围或说明"
            ></textarea>
            <template v-if="isAdmin">
              <div class="f-label">上传至</div>
              <div class="type-seg">
                <button :class="{ on: bankForm.scope === 'team' }" @click="bankForm.scope = 'team'">
                  班组池（本班组）
                </button>
                <button :class="{ on: bankForm.scope === 'all' }" @click="bankForm.scope = 'all'">
                  全部池（所有用户可拉取）
                </button>
              </div>
            </template>
            <div class="m-foot">
              <button class="btn btn-sm" @click="bankVisible = false">取消</button>
              <button class="btn btn-sm btn-primary" @click="saveBank">保存</button>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：题目新增 / 编辑 ================= -->
    <Transition name="qz-fade">
      <div v-if="questionVisible" class="mask" @click.self="questionVisible = false">
        <div class="modal" style="width: 620px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">{{ questionForm.id ? '编辑题目' : '新增题目' }}</div>
              <button class="m-x" @click="questionVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="f-label">题型</div>
            <div class="type-seg">
              <button
                :class="{ on: questionForm.type === 'single' }"
                @click="
                  questionForm.type = 'single';
                  onTypeChange('single');
                "
              >
                单选题
              </button>
              <button
                :class="{ on: questionForm.type === 'multiple' }"
                @click="
                  questionForm.type = 'multiple';
                  onTypeChange('multiple');
                "
              >
                多选题
              </button>
              <button
                :class="{ on: questionForm.type === 'judge' }"
                @click="
                  questionForm.type = 'judge';
                  onTypeChange('judge');
                "
              >
                判断题
              </button>
            </div>
            <div class="f-label">题干</div>
            <textarea
              v-model="questionForm.content"
              class="input"
              placeholder="请输入题干"
            ></textarea>
            <div class="f-label">选项</div>
            <template v-if="questionForm.type === 'judge'">
              <div class="opt-edit">
                <span class="ab">A</span>
                <input class="input" value="正确" disabled />
              </div>
              <div class="opt-edit">
                <span class="ab">B</span>
                <input class="input" value="错误" disabled />
              </div>
            </template>
            <template v-else>
              <div v-for="(opt, i) in questionForm.options" :key="i" class="opt-edit">
                <span class="ab">{{ LETTERS[i] }}</span>
                <input
                  v-model="questionForm.options[i]"
                  class="input"
                  :placeholder="'选项 ' + LETTERS[i]"
                  maxlength="200"
                />
                <button
                  class="mini-btn qm-del"
                  title="删除选项"
                  :disabled="questionForm.options.length <= 2"
                  @click="questionForm.options.splice(i, 1)"
                >
                  <span v-html="icon('delete', 14)"></span>
                </button>
              </div>
              <button
                class="btn btn-sm"
                style="margin-top: 10px"
                :disabled="questionForm.options.length >= 6"
                @click="questionForm.options.push('')"
              >
                <span v-html="icon('add', 14)"></span>添加选项
              </button>
            </template>
            <div class="f-label">答案</div>
            <div class="ans-chips">
              <template v-if="questionForm.type === 'judge'">
                <button
                  class="a-chip"
                  :class="{ on: judgeAnswer === 'A' }"
                  @click="onJudgeAnswer('A')"
                >
                  正确
                </button>
                <button
                  class="a-chip"
                  :class="{ on: judgeAnswer === 'B' }"
                  @click="onJudgeAnswer('B')"
                >
                  错误
                </button>
              </template>
              <template v-else>
                <button
                  v-for="(opt, i) in questionForm.options"
                  :key="i"
                  class="a-chip"
                  :class="{ on: questionForm.answer.indexOf(LETTERS[i]) >= 0 }"
                  :disabled="!questionForm.options[i]"
                  @click="toggleAnswerLetter(LETTERS[i])"
                >
                  {{ LETTERS[i] }}
                </button>
              </template>
            </div>
            <div class="f-tip">
              {{
                questionForm.type === 'judge'
                  ? '判断题答案：A=正确，B=错误'
                  : questionForm.type === 'multiple'
                    ? '多选题可点选多个选项字母'
                    : '单选题点选一个选项字母'
              }}
            </div>
            <div class="f-label">解析</div>
            <textarea
              v-model="questionForm.analysis"
              class="input"
              placeholder="留空则由 AI 自动生成"
            ></textarea>
            <div class="m-foot">
              <button class="btn btn-sm" @click="questionVisible = false">取消</button>
              <button class="btn btn-sm btn-primary" @click="saveQuestion">保存</button>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：导入校验未通过 ================= -->
    <Transition name="qz-fade">
      <div v-if="importErrors.length" class="mask" @click.self="importErrors = []">
        <div class="modal" style="width: 480px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">导入校验未通过</div>
              <button class="m-x" @click="importErrors = []">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="imp-err-tip warn">
              <span v-html="icon('warn', 14)"></span>
              <span>以下行校验未通过，请修正后重新导入</span>
            </div>
            <div v-for="e in importErrors" :key="e.row" class="imp-err">
              <b>第 {{ e.row }} 行</b>
              <span>{{ e.message }}</span>
            </div>
            <div class="m-foot">
              <button class="btn btn-sm btn-primary" @click="importErrors = []">知道了</button>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：刷题设置 ================= -->
    <Transition name="qz-fade">
      <div v-if="settingsVisible" class="mask" @click.self="settingsVisible = false">
        <div class="modal" style="width: 440px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">刷题设置</div>
              <button class="m-x" @click="settingsVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="pv-set-row">
              <div class="tx">
                <b>答对自动下一题</b>
                <p>答错停留看解析，末尾题不自动</p>
              </div>
              <span
                class="pv-sw"
                :class="{ on: practice.settings.autoNext }"
                @click="
                  practice.settings.autoNext = !practice.settings.autoNext;
                  saveSettings();
                "
              >
                <i class="dot"></i>
              </span>
            </div>
            <div class="pv-set-row">
              <div class="tx">
                <b>选项乱序</b>
                <p>仅答题模式单选/多选未答题生效，已答题保留作答时显示</p>
              </div>
              <span
                class="pv-sw"
                :class="{ on: practice.settings.shuffleOpt }"
                @click="toggleShuffleOpt"
              >
                <i class="dot"></i>
              </span>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：答题卡 ================= -->
    <Transition name="qz-fade">
      <div v-if="sheetVisible" class="mask" @click.self="sheetVisible = false">
        <div class="modal" style="width: 680px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">
                答题卡（已答 {{ counts.answered }}/{{ practice.outline.length }}）
              </div>
              <button class="m-x" @click="sheetVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="pv-legend">
              <span><i></i>未作答</span><span><i class="g"></i>答对</span
              ><span><i class="r"></i>答错</span>
            </div>
            <template v-for="sec in sheetSections" :key="sec.type">
              <div class="f-label">
                {{ typeText(sec.type) }}
                <span style="color: var(--ph); font-weight: 600"
                  >（{{ sec.items.length }} 题）</span
                >
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
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：修改解析 ================= -->
    <Transition name="qz-fade">
      <div v-if="anaVisible" class="mask" @click.self="anaVisible = false">
        <div class="modal" style="width: 560px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">修改解析</div>
              <button class="m-x" @click="anaVisible = false">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="imp-err-tip">
              解析对题库内全员可见，保存后即刻生效；留空则由 AI 重新生成
            </div>
            <textarea
              v-model="anaText"
              class="input pv-ana-text"
              maxlength="2000"
              placeholder="填写本题解析"
            ></textarea>
            <div class="m-foot">
              <button class="btn btn-sm" @click="anaVisible = false">取消</button>
              <button class="btn btn-sm btn-primary" @click="saveAnalysis">保存</button>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- ================= 弹层：通用确认（旧式 Shade.confirm 口径） ================= -->
    <Transition name="qz-fade">
      <div v-if="confirmDlg.visible" class="mask" @click.self="closeConfirm(false)">
        <div class="modal" style="width: 400px">
          <div class="m-scroll">
            <div class="m-head">
              <div class="m-title">{{ confirmDlg.title }}</div>
              <button class="m-x" @click="closeConfirm(false)">
                <span v-html="icon('close', 14)"></span>
              </button>
            </div>
            <div class="imp-err-tip">{{ confirmDlg.content }}</div>
            <div class="m-foot">
              <button class="btn btn-sm" @click="closeConfirm(false)">取消</button>
              <button class="btn btn-sm btn-danger" @click="closeConfirm(true)">
                {{ confirmDlg.confirmText }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>

    <!-- 题库 Excel 导入：隐藏文件选择（仅 .xlsx ≤10MB，前端先校验） -->
    <input ref="importFileEl" type="file" accept=".xlsx" hidden @change="onImportFile" />
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
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
  listTeams,
  removeFavorite,
  resetPractice,
  savePracticeProgress,
  setManageTeam,
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
// pvColEl 答题区滚动容器（切题 / 答题·背题切换后复位到顶，同旧页 pvRender 的 col.scrollTop = 0 口径）
const pvColEl = ref(null);
function resetPvScroll() {
  nextTick(() => {
    if (pvColEl.value) pvColEl.value.scrollTop = 0;
  });
}
watch(() => practice.idx, resetPvScroll);
watch(() => practice.viewMode, resetPvScroll);

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

// stTag 解析状态标签（已解析绿 / 解析中橙 / 失败红可重试 / 未解析墨，同旧页）。
function stTag(s) {
  return (
    {
      done: { text: '已解析', cls: 'green' },
      pending: { text: '解析中…', cls: 'orange' },
      failed: { text: '失败 · 重试', cls: 'red' },
    }[s] || { text: '未解析', cls: 'navy' }
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
  // 用户所选即展示字母（旧页不回映射乱序），仅判断题翻成中文
  let text = (res.selected || []).slice().sort().join('');
  if (curQ.value && curQ.value.type === 'judge') {
    const jm = { A: '正确', B: '错误' };
    text = text
      .split('')
      .map((l) => jm[l] || l)
      .join(' ');
  }
  return text;
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

// confirmDlg 旧式确认弹层状态（.mask/.modal，Esc / 点遮罩视为取消）。
const confirmDlg = reactive({
  visible: false,
  title: '',
  content: '',
  confirmText: '确认',
  _resolve: null,
});

// closeConfirm 关闭确认弹层并回传结果。
function closeConfirm(ok) {
  confirmDlg.visible = false;
  const resolve = confirmDlg._resolve;
  confirmDlg._resolve = null;
  if (resolve) resolve(!!ok);
}

// confirmDanger 危险操作确认（旧式弹层，同旧页 Shade.confirm 口径）。
function confirmDanger(title, content, confirmText) {
  return new Promise((resolve) => {
    confirmDlg.title = title;
    confirmDlg.content = content;
    confirmDlg.confirmText = confirmText || '确认';
    confirmDlg._resolve = resolve;
    confirmDlg.visible = true;
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

// openImport 拉起 Excel 文件选择（校验失败弹「导入校验未通过」，同旧页）。
function openImport() {
  if (!mgBank.value) {
    MessagePlugin.warning('请先在右侧列表选择目标题库');
    return;
  }
  importErrors.value = [];
  if (importFileEl.value) importFileEl.value.click();
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

// loadManage 管理侧题库列表（未选中或已失效时自动落第一个题库，同旧页 enterManage）。
async function loadManage(quiet) {
  if (!quiet) mgLoading.value = true;
  try {
    const data = await listManageBanks();
    mgList.value = data.list || [];
    if (!mgBank.value || !mgList.value.some((b) => b.id === mgBank.value.id)) {
      if (mgList.value.length) {
        openQuestions(mgList.value[0]);
      } else {
        mgBank.value = null;
        mq.list = [];
        mq.total = 0;
        mq.page = 1;
      }
    }
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

/* ================= 旧版还原补充：图标 / 页签指示条 / 班组切换 / 快捷键 / 展示辅助 ================= */

// 旧页线性图标（路径与 server/public/assets/icons.js 同源；viewBox 24，描边 1.7 圆角端点）。
const ICONS = {
  add: '<path d="M12 5v14M5 12h14"/>',
  book: '<path d="M12 7c-1.7-1.3-4-1.6-6.7-1.4v12.8c2.7-.2 5 .1 6.7 1.4 1.7-1.3 4-1.6 6.7-1.4V5.6C16 5.4 13.7 5.7 12 7z"/><path d="M12 7v12.8"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  'chevron-left': '<path d="M14.5 6.5L9 12l5.5 5.5"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  'close-circle': '<circle cx="12" cy="12" r="8.5"/><path d="M9 9l6 6M15 9l-6 6"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  compass: '<circle cx="12" cy="12" r="8.5"/><path d="M15.6 8.4l-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  delete:
    '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  edit: '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
  lightbulb:
    '<path d="M9.5 18.5h5M10.5 21h3"/><path d="M12 3.5a5.8 5.8 0 0 0-3.6 10.3c.8.7 1.4 1.6 1.6 2.7h4c.2-1.1.8-2 1.6-2.7A5.8 5.8 0 0 0 12 3.5z"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  setting:
    '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>',
  star: '<path d="M12 3.4l2.7 5.4 6 .9-4.3 4.2 1 6-5.4-2.8-5.4 2.8 1-6L3.3 9.7l6-.9z"/>',
  swap: '<path d="M4 7.5h11.5"/><path d="M13 4l3.5 3.5L13 11"/><path d="M20 16.5H8.5"/><path d="M11 13l-3.5 3.5L11 20"/>',
  upload: '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
  'view-list':
    '<path d="M9.5 6h11M9.5 12h11M9.5 18h11"/><circle cx="4.5" cy="6" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="12" r="1.1" fill="currentColor" stroke="none"/><circle cx="4.5" cy="18" r="1.1" fill="currentColor" stroke="none"/>',
  'view-module':
    '<rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
};

// icon 生成 inline SVG 图标字符串（同旧页 Shade.icon）。
function icon(name, size) {
  const body = ICONS[name] || ICONS.book;
  const s = size || 24;
  return (
    '<svg width="' +
    s +
    '" height="' +
    s +
    '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' +
    body +
    '</svg>'
  );
}

// entryIcons 题库主页四入口图标（顺序/随机/错题/收藏）。
const entryIcons = { seq: 'view-list', rand: 'swap', wrong: 'close-circle', fav: 'star' };

// optInnerHtml 选项字母圈内容：解析态正确项绿勾、错选项红叉，其余显字母。
function optInnerHtml(opt) {
  if (opt.inner === '✓') return icon('check', 13);
  if (opt.inner === '✕') return icon('close', 13);
  return opt.inner;
}

// typeMeta 管理表题型标签（单/多选橙签、判断墨签，同旧页 TYPE_META）。
function typeMeta(t) {
  return (
    {
      single: { tag: '单选', cls: 'tag-orange' },
      multiple: { tag: '多选', cls: 'tag-orange' },
      judge: { tag: '判断', cls: 'tag-navy' },
    }[t] || { tag: '单选', cls: 'tag-orange' }
  );
}

// bankAi 题库 AI 解析进度（x=已解析，y=已解析+解析中，同旧页 aiProgress）。
function bankAi(b) {
  const a = (b && b.analysis) || {};
  const x = a.done || 0;
  const y = (a.done || 0) + (a.pending || 0);
  return {
    x,
    y,
    pending: a.pending || 0,
    failed: a.failed || 0,
    none: a.none || 0,
    pct: y ? Math.round((x / y) * 100) : 0,
  };
}

// mgMeta 管理侧题库列表项摘要（解析进度口径同旧页 bankMetaManage）。
function mgMeta(b) {
  const a = bankAi(b);
  let t = `${b.questionCount} 题 · `;
  if (a.pending > 0) t += `解析中 ${a.x}/${a.y}`;
  else if (a.failed > 0) t += `${a.failed} 题解析失败`;
  else if (a.none > 0) t += `${a.none} 题待解析`;
  else t += '解析完成';
  return t;
}

// mgSubText 管理头部摘要行（同旧页 renderMgHead sub 文案）。
function mgSubText(b) {
  const a = bankAi(b);
  return (
    `${b.questionCount} 题 · 已解析 ${a.x} · 解析中 ${a.pending} · 失败 ${a.failed}` +
    (b.description ? ' · ' + b.description : '')
  );
}

// bankPct 我的题库练习进度百分比。
function bankPct(b) {
  return b.questionCount ? Math.round(((b.answeredCount || 0) / b.questionCount) * 100) : 0;
}

// 首页 AI 进度条口径（已解析 / 已解析+解析中）。
const aiDoneCnt = computed(() => an.value.done || 0);
const aiTodoCnt = computed(() => (an.value.done || 0) + (an.value.pending || 0));
const aiBarPct = computed(() =>
  aiTodoCnt.value ? Math.round((aiDoneCnt.value / aiTodoCnt.value) * 100) : 0
);

// pagerNums 管理分页页码（≤7 全列，否则首尾 + 当前 ±1 + 省略号，同旧页 renderPager）。
const pagerNums = computed(() => {
  const pages = mqPages.value;
  const cur = mq.page;
  if (pages <= 7) return Array.from({ length: pages }, (_, i) => i + 1);
  const nums = [1];
  const lo = Math.max(2, cur - 1);
  const hi = Math.min(pages - 1, cur + 1);
  if (lo > 2) nums.push('…');
  for (let j = lo; j <= hi; j++) nums.push(j);
  if (hi < pages - 1) nums.push('…');
  nums.push(pages);
  return nums;
});

/* ================= 页签：滑动指示条 ================= */
const tabsEl = ref(null);
const tabIndStyle = ref({ left: '0px', width: '0px' });

// moveTabInd 指示条跟随当前页签（同旧页 moveQzTabInd）。
function moveTabInd() {
  const wrap = tabsEl.value;
  if (!wrap) return;
  const btn = wrap.querySelector('button.on');
  tabIndStyle.value = btn
    ? { left: btn.offsetLeft + 'px', width: btn.offsetWidth + 'px' }
    : { left: '0px', width: '0px' };
}

// goTab 页签切换（管理页签首次切入加载管理数据，同旧页 switchTab）。
function goTab(p) {
  if (p === 'manage') {
    switchManageTab();
  } else {
    tab.value = 'practice';
  }
  nextTick(moveTabInd);
}

// toggleShuffleOpt 选项乱序开关：关闭时清掉未答题的乱序映射（已答题保留作答时显示）。
function toggleShuffleOpt() {
  practice.settings.shuffleOpt = !practice.settings.shuffleOpt;
  saveSettings();
  if (!practice.settings.shuffleOpt) {
    Object.keys(practice.shuffle).forEach((qid) => {
      if (!practice.results[qid]) delete practice.shuffle[qid];
    });
    practice.sel = [];
  }
}

/* ================= 超管班组切换（管理口径请求统一带 team_id，同旧页 setupTeamSel） ================= */
const teams = ref([]);
const teamId = ref(0);

async function loadTeams() {
  if (!isAdmin) return;
  try {
    const data = await listTeams();
    const raw = Array.isArray(data) ? data : (data && (data.list || data.teams)) || [];
    teams.value = raw.filter((t) => t && t.id);
    let saved = 0;
    try {
      saved = Number(localStorage.getItem('quiz_team_id') || 0);
    } catch (e) {
      // localStorage 不可用时用默认班组
    }
    teamId.value = teams.value.some((t) => t.id === saved)
      ? saved
      : teams.value.length
        ? teams.value[0].id
        : 0;
    setManageTeam(teamId.value);
    if (tab.value === 'manage') loadManage(true);
  } catch (err) {
    teams.value = [];
  }
}

function onTeamChange() {
  setManageTeam(teamId.value);
  try {
    localStorage.setItem('quiz_team_id', String(teamId.value || 0));
  } catch (e) {
    // localStorage 不可用时忽略
  }
  mgBank.value = null;
  loadManage(true);
}

/* ================= 快捷键：← → 切题 / Esc 关弹层 ================= */
function anyModalOpen() {
  return (
    confirmDlg.visible ||
    bankVisible.value ||
    questionVisible.value ||
    importErrors.value.length > 0 ||
    settingsVisible.value ||
    sheetVisible.value ||
    anaVisible.value ||
    poolVisible.value
  );
}

function closeTopModal() {
  if (confirmDlg.visible) closeConfirm(false);
  else if (poolVisible.value) poolVisible.value = false;
  else if (questionVisible.value) questionVisible.value = false;
  else if (bankVisible.value) bankVisible.value = false;
  else if (importErrors.value.length) importErrors.value = [];
  else if (anaVisible.value) anaVisible.value = false;
  else if (sheetVisible.value) sheetVisible.value = false;
  else if (settingsVisible.value) settingsVisible.value = false;
}

function onQzKey(e) {
  if (e.key === 'Escape' && anyModalOpen()) {
    e.preventDefault();
    closeTopModal();
    return;
  }
  if (tab.value !== 'practice' || !practice.started || anyModalOpen()) return;
  const tag = (e.target && e.target.tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  if (e.key === 'ArrowLeft') {
    e.preventDefault();
    go(practice.idx - 1);
  } else if (e.key === 'ArrowRight') {
    e.preventDefault();
    go(practice.idx + 1);
  }
}

onMounted(() => {
  loadTeams();
  moveTabInd();
  window.addEventListener('resize', moveTabInd);
  window.addEventListener('keydown', onQzKey);
});

onBeforeUnmount(() => {
  window.removeEventListener('resize', moveTabInd);
  window.removeEventListener('keydown', onQzKey);
});
</script>

<style scoped>
/* ===== 「政企蓝白」旧版 token（值与 server/public/assets/theme.css 一致），限定本组件 ===== */
.qz-root {
  --paper: #f5f7fa;
  --card: #ffffff;
  --navy: #1d2129;
  --orange: #0e3da8;
  --orange-soft: #e8f0ff;
  --on-brand: #ffffff;
  --inverse-bg: #1d2129;
  --inverse-text: #ffffff;
  --red: #f53f3f;
  --green: #00b42a;
  --yellow: #ff7d00;
  --warn-soft: #fff7e8;
  --warn-text: #d25f00;
  --ok-soft: #e8ffea;
  --ok-text: #0b8a37;
  --danger-soft: #ffece8;
  --danger-line: #ffd0c7;
  --line: #e5e6eb;
  --hair: rgba(29, 33, 41, 0.06);
  --sub: #4e5969;
  --ph: #86909c;
  --dash: #c9cdd4;
  --fill: #f7f8fa;
  --brand-glow: rgba(14, 61, 168, 0.12);
  --overlay: rgba(29, 33, 41, 0.45);
  --skeleton: rgba(29, 33, 41, 0.05);
  --ease: cubic-bezier(0.22, 1, 0.36, 1);
  --radius: 12px;
  --radius-sm: 8px;
  --hard: 0 2px 8px rgba(0, 0, 0, 0.04);
  --hard-s: 0 1px 4px rgba(0, 0, 0, 0.05);
  --shadow-card: 0 2px 8px rgba(0, 0, 0, 0.04);
  --shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.08);
  /* 整页骨架：精确覆盖父级 .page 内边距（28px 40px 48px，见 styles.css），整页不滚动、仅卡片内部滚动（同旧页 body 锁定口径） */
  height: calc(100% + 76px);
  margin: -28px -40px -48px;
  box-sizing: border-box;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  padding: 22px 40px 30px;
}
/* 尺寸口径补齐：旧 theme.css 全局 border-box，本组件 scoped 还原（按钮/输入/Select 高度对齐依赖此项） */
.qz-root *,
.qz-root *::before,
.qz-root *::after {
  box-sizing: border-box;
}

/* ===== 以下布局样式移植自 server/public/quiz.html（旧版基准） ===== */
.qz-wrap {
  flex: 1;
  min-height: 0;
  width: 100%;
  display: flex;
  flex-direction: row-reverse;
  align-items: stretch;
  gap: 20px;
  margin-top: 16px;
}
/* 两卡等高对齐：全局 styles.css 的 .card + .card 上边距会把 row-reverse 下的左卡（第二个 .card 兄弟）压矮、顶边下沉，此处归零 */
.qz-wrap > .qz-main {
  margin-top: 0;
}
.qz-wrap[hidden] {
  display: none !important;
} /* author display 规则优先于 UA [hidden]，显式兜底 */

/* 答题对错用色：业务绿 var(--green)（浅绿底 --ok-soft 同 worklog 口径；解析状态「已解析」标签用） */
.tag-green {
  background: var(--ok-soft);
  color: var(--green);
}

/* 细滚动条：半透明墨，全页滚动区统一口径（同 worklog）；gutter 常驻，滚动条出现/消失时内容不左右跳动 */
.side-scroll,
.qtable,
.pv-col,
.home-pane {
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
  scrollbar-gutter: stable both-edges;
}
.side-scroll::-webkit-scrollbar,
.qtable::-webkit-scrollbar,
.pv-col::-webkit-scrollbar,
.home-pane::-webkit-scrollbar,
.m-scroll::-webkit-scrollbar {
  width: 5px;
}
.side-scroll::-webkit-scrollbar-thumb,
.qtable::-webkit-scrollbar-thumb,
.pv-col::-webkit-scrollbar-thumb,
.home-pane::-webkit-scrollbar-thumb,
.m-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.side-scroll::-webkit-scrollbar-thumb:hover,
.qtable::-webkit-scrollbar-thumb:hover,
.pv-col::-webkit-scrollbar-thumb:hover,
.home-pane::-webkit-scrollbar-thumb:hover,
.m-scroll::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.side-scroll::-webkit-scrollbar-track,
.qtable::-webkit-scrollbar-track,
.pv-col::-webkit-scrollbar-track,
.home-pane::-webkit-scrollbar-track,
.m-scroll::-webkit-scrollbar-track {
  background: transparent;
}
/* 题库主页：内容高于卡片时卡内滚动（与答题区/题库列表同口径，不撑破卡片） */
.home-pane {
  overflow-y: auto;
}

/* ===== 右卡：题库列表 ===== */
.qz-side {
  width: 300px;
  flex: none;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 18px 16px;
}
.qz-side [hidden] {
  display: none !important;
} /* author display 规则优先于 UA [hidden]，显式兜底 */
.side-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
}
.side-head b {
  flex: 1;
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.mini-btn {
  width: 30px;
  height: 30px;
  flex: none;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--sub);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition:
    border-color 0.25s,
    color 0.25s;
}
.mini-btn:hover {
  border-color: var(--orange);
  color: var(--orange);
}
.mini-btn svg {
  width: 15px;
  height: 15px;
}
.side-team {
  flex: none;
  margin-top: 12px;
}
.side-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  margin-top: 12px;
  padding-right: 2px;
}
.side-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 12px;
  border: 1px solid transparent;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.2s;
}
.side-item + .side-item {
  margin-top: 8px;
}
.side-item:hover {
  background: var(--paper);
}
.side-item.on {
  background: var(--orange-soft);
}
.side-item.on .tx b {
  color: var(--orange);
}
.side-item > svg {
  flex: none;
  color: var(--sub);
}
.side-item.on > svg {
  color: var(--orange);
}
/* 空态成功图标（theme 的 .empty svg 置灰，此处覆盖为业务绿） */
.ic-ok svg {
  color: var(--green);
}
.side-item .tx {
  flex: 1;
  min-width: 0;
}
.side-item .tx b {
  display: block;
  font-size: 13px;
  font-weight: 700;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.side-item .tx span {
  display: block;
  margin-top: 2px;
  font-size: 10.5px;
  color: var(--ph);
}
/* 题库项的编辑/删除小图标 */
.side-item .ops {
  display: flex;
  gap: 2px;
  flex: none;
}
.side-item .ops button {
  border: none;
  background: none;
  padding: 5px;
  color: var(--ph);
  cursor: pointer;
  border-radius: 8px;
  display: flex;
  transition:
    color 0.2s,
    background 0.2s;
}
.side-item .ops button:hover {
  color: var(--orange);
  background: var(--card);
}
.side-item .ops svg {
  width: 14px;
  height: 14px;
}
.side-scroll .empty {
  padding: 36px 8px;
}
.sk-item {
  height: 54px;
  border-radius: 8px;
}
.sk-item + .sk-item {
  margin-top: 8px;
}

/* ===== 左卡：题库管理 ===== */
.qz-main {
  flex: 1;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 20px 24px;
}
.qz-main [hidden] {
  display: none !important;
}
.pane {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  animation: paneIn 0.34s var(--ease);
}
/* 入场时延链：页签条 / 练习区 / 管理区依次落位（fadeUp .55s + delay，总落位 ≤0.9s） */
.qz-tabs.fade-up {
  animation-delay: 0.05s;
}
.qz-wrap.fade-up {
  animation-delay: 0.12s;
}

.progress {
  height: 6px;
  border-radius: 8px;
  background: var(--hair);
  overflow: hidden;
}
.progress i {
  display: block;
  height: 100%;
  border-radius: 8px;
  background: var(--orange);
  transition: width 0.3s;
}

.mg-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.mg-head .tx {
  flex: 1;
  min-width: 220px;
}
.mg-head .tx b {
  font-size: 16px;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 8px;
}
.mg-head .tx p {
  margin-top: 3px;
  font-size: 11.5px;
  color: var(--sub);
}
/* AI 解析进行中通知条（5s 轮询，归零停；浅蓝底品牌蓝字） */
.ai-notice {
  flex: none;
  margin-top: 14px;
  padding: 10px 14px;
  border-radius: 8px;
  background: var(--orange-soft);
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 9px;
  font-size: 12px;
  font-weight: 600;
  color: var(--orange);
}
.ai-notice > svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.ai-notice .txt {
  flex: 1;
  min-width: 0;
}
.ai-notice .progress {
  width: 180px;
  flex: none;
}
.ai-notice b {
  white-space: nowrap;
}
.mg-tools {
  flex: none;
  margin-top: 14px;
  display: flex;
  align-items: center;
  gap: 10px;
}
.mg-search {
  position: relative;
  width: 260px;
}
.mg-search > svg {
  position: absolute;
  left: 12px;
  top: 50%;
  transform: translateY(-50%);
  width: 15px;
  height: 15px;
  color: var(--ph);
  pointer-events: none;
}
.mg-search .input {
  height: 36px;
  padding-left: 36px;
  font-size: 12.5px;
}
.mg-total {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--ph);
  letter-spacing: 1px;
}
/* 题目表格：行内 flex，表头 sticky， tbody 区独立滚动 */
.qtable {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  margin-top: 12px;
  border: 1px solid var(--hair);
  border-radius: 8px;
}
.qtable .tr {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 14px;
  font-size: 12.5px;
  background: var(--card);
}
.qtable .tr + .tr {
  border-top: 1px solid var(--hair);
}
.qtable .tr.hd {
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--paper);
  font-size: 11.5px;
  font-weight: 700;
  color: var(--sub);
  letter-spacing: 1px;
}
.qtable .c-t {
  width: 52px;
  flex: none;
  justify-content: center;
}
.qtable .c-q {
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-weight: 600;
}
.qtable .c-a {
  width: 64px;
  flex: none;
  font-weight: 700;
}
.qtable .c-s {
  width: 92px;
  flex: none;
}
.qtable .c-o {
  width: 64px;
  flex: none;
  display: flex;
  gap: 10px;
}
.qtable .c-o button {
  border: none;
  background: none;
  padding: 3px;
  color: var(--ph);
  cursor: pointer;
  display: flex;
  border-radius: 8px;
  transition:
    color 0.2s,
    background 0.2s;
}
.qtable .c-o button:hover {
  color: var(--orange);
  background: var(--paper);
}
.qtable .c-o svg {
  width: 15px;
  height: 15px;
}
.tag-retry {
  cursor: pointer;
}
.qtable .empty {
  padding: 44px 20px;
}
/* 分页器 */
.pager {
  flex: none;
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
  gap: 6px;
}
.pager button {
  min-width: 28px;
  height: 28px;
  padding: 0 6px;
  border-radius: 8px;
  border: 1px solid var(--line);
  background: var(--card);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11.5px;
  color: var(--sub);
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.2s,
    color 0.2s;
}
.pager button:hover:not([disabled]) {
  border-color: var(--orange);
  color: var(--orange);
}
.pager button.on {
  background: var(--orange);
  border-color: var(--orange);
  color: var(--on-brand);
  font-weight: 700;
}
.pager button[disabled] {
  opacity: 0.45;
  cursor: not-allowed;
}
.pager .pg-dots {
  min-width: 20px;
  border: none;
  background: none;
  cursor: default;
  color: var(--ph);
}

/* ===== 通用弹层（题库 / 题目编辑；同 worklog 结构：直角外壳 + 2px 墨边 + 硬影、滚动内凹 .m-scroll） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 320;
  padding: 24px;
  background: var(--overlay);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.modal {
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  width: 560px;
  max-width: 94vw;
  max-height: 86vh;
  overflow: hidden;
  padding: 6px;
  display: flex;
  flex-direction: column;
  box-shadow: var(--shadow-card);
}
/* 弹层开合过渡（Vue Transition 驱动，还原旧 Shade.modal 的进出场：遮罩淡入淡出 + 弹窗轻微上浮） */
.qz-fade-enter-active {
  transition: opacity 0.28s var(--ease);
}
.qz-fade-leave-active {
  transition: opacity 0.22s ease;
}
.qz-fade-enter-from,
.qz-fade-leave-to {
  opacity: 0;
}
.qz-fade-enter-active .modal {
  animation: qzModalIn 0.32s var(--ease);
}
.qz-fade-leave-active .modal {
  animation: qzModalOut 0.2s ease forwards;
}
@keyframes qzModalIn {
  from {
    opacity: 0;
    transform: translateY(16px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes qzModalOut {
  from {
    opacity: 1;
    transform: none;
  }
  to {
    opacity: 0;
    transform: translateY(10px) scale(0.98);
  }
}
.m-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 8px;
  padding: 14px 18px 16px;
  scrollbar-gutter: stable both-edges;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 4px;
}
.m-title {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.m-x {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--navy);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: var(--shadow-card);
  transition:
    background 0.15s,
    color 0.15s;
}
.m-x:hover {
  background: var(--fill);
  color: var(--orange);
}
.m-x svg {
  width: 14px;
  height: 14px;
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
/* 弹层表单 */
.f-label {
  margin: 14px 0 8px;
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.imp-err-tip {
  margin-top: 12px;
  font-size: 12.5px;
  color: var(--sub);
}
/* 导入校验失败提示条：⚠ 红三角 + 红字（与 worklog 未通过原因块同口径） */
.imp-err-tip.warn {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--red);
  font-weight: 700;
}
.imp-err-tip.warn svg {
  flex: none;
  color: var(--red);
}
.imp-err {
  display: flex;
  gap: 10px;
  padding: 9px 2px;
  border-bottom: 1px solid var(--line);
  font-size: 12.5px;
}
.imp-err b {
  flex: none;
  color: var(--red);
}
.imp-err span {
  color: var(--navy);
  line-height: 1.6;
}
.f-tip {
  margin-top: 6px;
  font-size: 11px;
  color: var(--ph);
  letter-spacing: 0.5px;
}
.type-seg {
  display: inline-flex;
  gap: 2px;
  padding: 3px;
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 8px;
}
.type-seg button {
  border: none;
  background: transparent;
  height: 30px;
  padding: 0 16px;
  border-radius: 8px;
  font-size: 12.5px;
  color: var(--sub);
  letter-spacing: 1px;
  cursor: pointer;
  font-family: inherit;
  transition:
    background 0.25s,
    color 0.25s;
}
.type-seg button:hover {
  color: var(--navy);
}
.type-seg button.on {
  background: var(--orange);
  color: var(--on-brand);
  font-weight: 600;
}
.opt-edit {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.opt-edit .ab {
  width: 26px;
  height: 26px;
  flex: none;
  border-radius: 50%;
  border: 1.5px solid var(--dash);
  color: var(--sub);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12.5px;
  font-weight: 700;
}
.opt-edit .input {
  height: 38px;
  flex: 1;
}
.ans-chips {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.a-chip {
  min-width: 38px;
  height: 32px;
  padding: 0 13px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--sub);
  font-size: 12.5px;
  font-weight: 700;
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.2s,
    background 0.2s,
    color 0.2s;
}
.a-chip:hover {
  border-color: var(--orange);
  color: var(--orange);
}
.a-chip.on {
  border-color: var(--orange);
  background: var(--orange-soft);
  color: var(--orange);
}

/* ===== 页签：刷题练习 / 题库管理（与出工日志 wl-tabs 同族：文字 + 滑动指示条） ===== */
.qz-tabs {
  position: relative;
  flex: none;
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--hair);
}
.qz-tabs button {
  padding: 11px 20px;
  border: none;
  background: none;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 14px;
  font-weight: 600;
  color: var(--sub);
  letter-spacing: 1.5px;
  transition: color 0.25s;
  font-family: inherit;
}
.qz-tabs button:hover {
  color: var(--navy);
}
.qz-tabs button.on {
  color: var(--orange);
  font-weight: 700;
}
.qz-tabs button svg {
  width: 15px;
  height: 15px;
}
.qz-tab-ind {
  position: absolute;
  bottom: -1px;
  left: 0;
  width: 0;
  height: 3px;
  border-radius: 8px;
  background: var(--orange);
  transition:
    left 0.3s var(--ease),
    width 0.3s var(--ease);
}

/* ===== 刷题练习 · 右卡我的题库（个人口径进度） ===== */
.qz-wrap .side-item .pg {
  margin-top: 7px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.qz-wrap .side-item .pg .progress {
  flex: 1;
  height: 5px;
}
.qz-wrap .side-item .pg > span {
  font-size: 10px;
  color: var(--ph);
  white-space: nowrap;
}

/* ===== 刷题练习 · 左卡题库主页（统计行 + 四宫格入口） ===== */
.bh-stats {
  display: flex;
  padding: 15px 0;
  border: 1px solid var(--hair);
  border-radius: 8px;
}
.bh-stats .cell {
  flex: 1;
  text-align: center;
  position: relative;
}
.bh-stats .cell + .cell::before {
  content: '';
  position: absolute;
  left: 0;
  top: 6px;
  bottom: 6px;
  width: 1px;
  background: var(--hair);
}
.bh-stats b {
  font-size: 20px;
  font-weight: 700;
}
.bh-stats b em {
  font-style: normal;
  color: var(--orange);
}
.bh-stats span {
  display: block;
  margin-top: 4px;
  font-size: 10.5px;
  color: var(--sub);
  letter-spacing: 1px;
}
.bh-grid {
  margin-top: 14px;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.bh-entry {
  border: 1px solid var(--hair);
  border-radius: 8px;
  padding: 13px 14px;
  display: flex;
  align-items: center;
  gap: 11px;
  cursor: pointer;
  transition: border-color 0.2s;
}
.bh-entry:hover {
  border-color: var(--orange);
}
.bh-entry .ib {
  width: 40px;
  height: 40px;
  flex: none;
  border-radius: 8px;
  background: var(--orange-soft);
  color: var(--orange);
  display: flex;
  align-items: center;
  justify-content: center;
}
.bh-entry .tx {
  flex: 1;
  min-width: 0;
}
.bh-entry .tx b {
  font-size: 13.5px;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 6px;
}
.bh-entry .tx p {
  margin-top: 3px;
  font-size: 11px;
  color: var(--sub);
}
.bh-entry > svg {
  flex: none;
  color: var(--ph);
}

/* ===== 刷题页（左卡接管）：顶行 返回 + 答题/背题 + 进度 + x/total ===== */
.pv-pane {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.pv-pane[hidden] {
  display: none !important;
} /* author display 规则优先于 UA [hidden]，显式兜底 */
.pv-top {
  flex: none;
  display: flex;
  align-items: center;
  gap: 14px;
}
.pv-back {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--navy);
  cursor: pointer;
}
.pv-back:hover {
  color: var(--orange);
}
.pv-title {
  font-size: 13.5px;
  font-weight: 700;
  white-space: nowrap;
}
.pv-vm {
  flex: none;
  display: inline-flex;
  background: var(--hair);
  border-radius: 8px;
  padding: 2px;
}
.pv-vm i {
  font-style: normal;
  font-size: 11.5px;
  font-weight: 700;
  padding: 3px 12px;
  border-radius: 8px;
  color: var(--sub);
  cursor: pointer;
}
.pv-vm i.on {
  background: var(--orange);
  color: var(--on-brand);
}
.pv-pg {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 12px;
}
.pv-pg .progress {
  flex: 1;
}
.pv-pg b {
  font-size: 12.5px;
  font-weight: 700;
  white-space: nowrap;
}
.pv-col {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.pv-col::-webkit-scrollbar {
  width: 5px;
}
.pv-col::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.pv-col-in {
  max-width: 840px;
  padding: 16px 2px 4px;
  /* 翻页 / 答题卡跳转：题目块轻微上浮淡入（约 220ms 克制止渡，由题目 key 重挂载触发） */
  animation: qzTurn 0.22s var(--ease);
}
@keyframes qzTurn {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
/* 题干：无卡片，与选项同一平面（tag 起于 15px；选项字母圈对齐内容首字 69px 口径不变） */
.pv-stem {
  padding: 2px 15px 14px;
  font-size: 15.5px;
  font-weight: 700;
  line-height: 1.8;
}
.pv-stem .tag {
  margin-right: 7px;
  vertical-align: 2px;
}
/* 选项：无卡片底，仅字母圈 + 文字；左边界对齐题干内容首字（题干卡内边距 15px + 题型 tag ≈47px + 间距 7px） */
.pv-opt {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 7px 15px 7px 69px;
  font-size: 14px;
  line-height: 1.7;
  cursor: pointer;
  border-radius: 8px;
}
.pv-opt .ab {
  width: 26px;
  height: 26px;
  flex: none;
  border-radius: 50%;
  border: 1.5px solid var(--dash);
  color: var(--sub);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12.5px;
  font-weight: 700;
  transition:
    border-color 0.2s,
    background 0.2s,
    color 0.2s;
}
.pv-opt.sq .ab {
  border-radius: 8px;
}
.pv-opt:not(.lock):hover .ab {
  border-color: var(--orange);
  color: var(--orange);
}
.pv-opt.sel .ab {
  border-color: var(--orange);
  background: var(--orange);
  color: var(--on-brand);
}
.pv-opt.right .ab {
  border-color: var(--green);
  background: var(--green);
  color: var(--on-brand);
}
.pv-opt.wrong .ab {
  border-color: var(--red);
  background: var(--red);
  color: var(--on-brand);
}
.pv-opt .tx {
  min-width: 0;
}
.pv-opt.lock {
  cursor: default;
}
/* 解析态（正确答案/您的选择 + 统计 + 错题本动态）与试题详解卡：占满整个题目卡宽度（与顶栏/底栏对齐，突破上方 840px 题列） */
.r-line {
  margin: 14px 0 0;
  padding: 12px 15px;
  background: var(--card);
  border: 1px solid var(--hair);
  border-radius: 8px;
}
.r-main {
  display: flex;
  gap: 22px;
  font-size: 13px;
  font-weight: 700;
}
.r-main b {
  margin-left: 6px;
}
.r-main b.ok {
  color: var(--green);
}
.r-main b.bad {
  color: var(--red);
}
.r-sub {
  margin-top: 6px;
  font-size: 11px;
  color: var(--sub);
}
.r-sub.orange {
  color: var(--orange);
  font-weight: 700;
}
.pv-ana {
  margin: 14px 0 0;
  padding: 13px 15px;
  background: var(--orange-soft);
  border-radius: 8px;
}
.pv-ana .hd {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--orange);
  font-size: 13.5px;
  font-weight: 700;
  letter-spacing: 1px;
}
.pv-ana .bd {
  margin-top: 9px;
  font-size: 12.8px;
  line-height: 1.8;
  color: var(--sub);
}
.pv-ana .ft {
  margin-top: 9px;
  font-size: 10.5px;
  color: var(--ph);
  letter-spacing: 0.5px;
}
/* 解析编辑入口（全员可改：刷题中纠错/补充，保存后对全员生效） */
.pv-ana-edit {
  margin-left: auto;
  border: none;
  background: none;
  padding: 3px;
  border-radius: 8px;
  color: var(--ph);
  cursor: pointer;
  display: flex;
  transition:
    color 0.2s,
    background 0.2s;
}
.pv-ana-edit:hover {
  color: var(--orange);
  background: var(--card);
}
.pv-ana-edit svg {
  width: 14px;
  height: 14px;
}
.pv-ana-text {
  width: 100%;
  min-height: 150px;
  resize: vertical;
  line-height: 1.7;
}
/* 底栏：收藏 / 对错计数 / 答题卡 / 设置 + 主按钮 */
.pv-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding-top: 14px;
  border-top: 1px solid var(--hair);
}
.pv-chipbtn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  flex: none;
  white-space: nowrap;
  background: var(--paper);
  border: 1px solid var(--hair);
  border-radius: 8px;
  padding: 5px 12px;
  font-size: 11.5px;
  color: var(--sub);
  font-weight: 600;
  cursor: pointer;
  transition:
    color 0.2s,
    border-color 0.2s;
  font-family: inherit;
}
.pv-chipbtn:hover {
  color: var(--orange);
  border-color: var(--orange);
}
.pv-chipbtn.faved {
  color: var(--orange);
  border-color: var(--orange);
}
.pv-chipbtn svg {
  width: 13px;
  height: 13px;
}
.pv-foot .sp {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}
.pv-foot .sp .btn {
  flex: none;
}
.pv-hint {
  min-width: 0;
  font-size: 11px;
  color: var(--ph);
  letter-spacing: 1px;
}
.qz-ok {
  color: var(--green);
}
.qz-bad {
  color: var(--red);
}
/* 答题卡弹层（题型分区块 chips + 图例 + 底部清空做题记录） */
.pv-sheet-grid {
  display: grid;
  grid-template-columns: repeat(10, 1fr);
  gap: 8px;
}
.pv-chip {
  height: 34px;
  border-radius: 8px;
  background: var(--hair);
  color: var(--sub);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12.5px;
  font-weight: 700;
  cursor: pointer;
  transition: box-shadow 0.15s;
}
.pv-chip:hover {
  box-shadow: inset 0 0 0 2px var(--orange);
}
.pv-chip.ok {
  background: var(--green);
  color: var(--on-brand);
}
.pv-chip.bad {
  background: var(--red);
  color: var(--on-brand);
}
.pv-chip.cur {
  box-shadow: inset 0 0 0 2px var(--orange);
  color: var(--navy);
  background: var(--card);
}
.pv-legend {
  display: flex;
  gap: 16px;
  margin: 2px 0 4px;
  font-size: 11px;
  color: var(--sub);
}
.pv-legend i {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 8px;
  background: var(--hair);
  margin-right: 5px;
}
.pv-legend i.g {
  background: var(--green);
}
.pv-legend i.r {
  background: var(--red);
}
.pv-clear {
  text-align: center;
  font-size: 12px;
  font-weight: 700;
  color: var(--red);
  cursor: pointer;
  margin-top: 14px;
}
.pv-clear.disabled {
  color: var(--ph);
  cursor: not-allowed;
}
/* 刷题设置（答对自动下一题 / 选项乱序；本地 storage quiz_settings 持久化） */
.pv-set-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 2px;
}
.pv-set-row + .pv-set-row {
  border-top: 1px solid var(--hair);
}
.pv-set-row .tx {
  flex: 1;
  min-width: 0;
}
.pv-set-row .tx b {
  font-size: 13.5px;
  font-weight: 700;
}
.pv-set-row .tx p {
  margin-top: 3px;
  font-size: 11px;
  color: var(--sub);
}
.pv-sw {
  width: 40px;
  height: 22px;
  flex: none;
  border-radius: 8px;
  background: var(--dash);
  position: relative;
  cursor: pointer;
  transition: background 0.25s;
}
.pv-sw .dot {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 18px;
  height: 18px;
  border-radius: 8px;
  background: var(--card);
  transition: left 0.25s;
  box-shadow: var(--shadow-card);
}
.pv-sw.on {
  background: var(--orange);
}
.pv-sw.on .dot {
  left: 20px;
}

/* 窄屏回退：两卡收窄 */
@media (max-width: 900px) {
  .page {
    padding: 16px;
  }
  .qz-side {
    width: 240px;
  }
  .qz-main {
    padding: 16px;
  }
}

/* ===== theme.css 共享件（按钮 / 输入 / 标签 / 徽章 / 空态 / 骨架 / 加载 / 入场动效），
   与 server/public/assets/theme.css 旧页口径一致，随本组件 scoped 注入 ===== */
.badge-outline {
  display: inline-block;
  flex: none;
  white-space: nowrap;
  background: var(--orange-soft);
  color: var(--orange);
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
  letter-spacing: 0.5px;
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  height: 40px;
  padding: 0 20px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--line);
  background: var(--card);
  color: var(--navy);
  font-size: 13.5px;
  font-weight: 600;
  letter-spacing: 1px;
  white-space: nowrap;
  cursor: pointer;
  transition:
    border-color 0.15s var(--ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
  font-family: inherit;
}
.btn svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.btn:hover:not([disabled]) {
  border-color: var(--orange);
  color: var(--orange);
}
.btn:active:not([disabled]) {
  transform: translateY(1px);
}
.btn[disabled] {
  opacity: 0.55;
  cursor: not-allowed;
}
.btn-primary,
.btn-accent,
.btn-danger {
  color: var(--on-brand);
  border-color: transparent;
}
.btn-primary {
  background: var(--orange);
}
.btn-primary:hover:not([disabled]) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-accent,
.btn-danger {
  background: var(--red);
}
.btn-accent:hover:not([disabled]),
.btn-danger:hover:not([disabled]) {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-sm {
  height: 34px;
  padding: 0 14px;
  font-size: 12.5px;
}

.input {
  width: 100%;
  height: 44px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  background: var(--card);
  padding: 0 14px;
  font-size: 14px;
  color: var(--navy);
  outline: none;
  font-family: inherit;
  transition:
    border-color 0.15s var(--ease),
    box-shadow 0.15s var(--ease);
}
.input::placeholder {
  color: var(--ph);
}
.input:focus {
  border-color: var(--orange);
  box-shadow: 0 0 0 3px var(--brand-glow);
}
textarea.input {
  height: auto;
  min-height: 96px;
  padding: 10px 14px;
  resize: vertical;
  line-height: 1.7;
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
.tag-orange {
  background: var(--orange-soft);
  color: var(--orange);
}
.tag-navy {
  background: var(--fill);
  color: var(--sub);
}
.tag-red {
  background: var(--danger-soft);
  color: var(--red);
}

.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--ph);
}
.empty svg {
  width: 44px;
  height: 44px;
  color: var(--dash);
}
.empty p {
  margin: 0;
  font-size: 13px;
  letter-spacing: 1px;
}

.skeleton {
  position: relative;
  overflow: hidden;
  background: var(--skeleton);
  border-radius: 8px;
}
.skeleton::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(100deg, transparent 25%, rgba(255, 255, 255, 0.65), transparent 75%);
  transform: translateX(-100%);
  animation: shimmer 1.6s ease-in-out infinite;
}
@keyframes shimmer {
  to {
    transform: translateX(100%);
  }
}

.spin {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: 2.5px solid var(--line);
  border-top-color: var(--orange);
  animation: rot 1s linear infinite;
}
@keyframes rot {
  to {
    transform: rotate(360deg);
  }
}
.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 0;
  color: var(--ph);
  font-size: 12.5px;
  letter-spacing: 1px;
}

@keyframes fadeUp {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.fade-up {
  animation: fadeUp 0.55s var(--ease) both;
}
@keyframes paneIn {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* ===== 本组件补位：题库池行 / 题目选项删除钮 / 窄屏 ===== */
.pool-row {
  align-items: center;
  padding: 11px 2px;
}
.pool-row .pool-main {
  flex: 1;
  min-width: 0;
}
.pool-row .pool-main b {
  font-size: 13px;
  color: var(--navy);
  display: flex;
  align-items: center;
  gap: 6px;
}
.pool-row .pool-main p {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--sub);
}
.qm-del[disabled] {
  opacity: 0.4;
  cursor: not-allowed;
}
.side-team select {
  width: 100%;
  height: 36px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--card);
  color: var(--navy);
  font-size: 12.5px;
  padding: 0 30px 0 10px;
  outline: none;
  font-family: inherit;
  appearance: none;
  -webkit-appearance: none;
  /* 自绘下拉箭头：收起态美观，点击仍走原生下拉（可正常展开选择） */
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%234E5969' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 10px center;
  cursor: pointer;
}
.side-team select:hover {
  border-color: var(--orange);
}
.side-team select:focus {
  border-color: var(--orange);
}

/* 动效降级：与旧 theme.css 同口径（关动画与延迟、过渡时长趋零），覆盖翻页淡入/弹层开合/骨架微光等 */
@media (prefers-reduced-motion: reduce) {
  .qz-root *,
  .qz-root *::before,
  .qz-root *::after {
    animation-duration: 0.001s !important;
    animation-iteration-count: 1 !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
  .fade-up,
  .pane,
  .pv-col-in,
  .skeleton::after,
  .spin {
    animation: none !important;
  }
}

/* ===== 图标 span 包裹补位（旧 CSS 以直接子级 svg 定位/着色的规则） ===== */
.side-item > span {
  flex: none;
  display: inline-flex;
  color: var(--sub);
}
.side-item.on > span {
  color: var(--orange);
}
.mg-search > span {
  position: absolute;
  left: 12px;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  color: var(--ph);
  pointer-events: none;
}
.mg-search > span svg {
  width: 15px;
  height: 15px;
}
.ai-notice > span:not(.txt) {
  width: 15px;
  height: 15px;
  flex: none;
  display: inline-flex;
}
.ai-notice > span:not(.txt) svg {
  width: 15px;
  height: 15px;
}
.bh-entry > span {
  flex: none;
  display: inline-flex;
  color: var(--ph);
}
.pv-ana .hd > span {
  flex: none;
  display: inline-flex;
}
</style>
