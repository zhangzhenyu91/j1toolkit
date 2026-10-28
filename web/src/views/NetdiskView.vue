<template>
  <div class="nd-root">
    <div v-if="demoMode" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >列表为静态演示数据；上传/分享/删除等写操作不执行、不伪造成功。切换 live 适配层后对接 Go
        API。</span
      >
    </div>

    <div class="nd-wrap">
      <div class="nd-head fade-up">
        <h2>团队网盘</h2>
        <p>文件经 Shade 中转存取，支持分片上传与外链分享</p>
      </div>

      <div class="nd-tabs fade-up" style="animation-delay: 0.08s">
        <button type="button" :class="{ on: view === 'files' }" @click="switchView('files')">
          全部文件
        </button>
        <button type="button" :class="{ on: view === 'shares' }" @click="switchView('shares')">
          分享管理
        </button>
      </div>

      <!-- ================= 全部文件视图 ================= -->
      <section
        v-show="view === 'files'"
        class="nd-view"
        :class="{ 'view-in': viewAnim === 'files' }"
        @dragenter.prevent="onViewDragEnter"
        @dragover.prevent="onViewDragOver"
        @dragleave.prevent="onViewDragLeave"
        @drop.prevent="onDropFiles"
      >
        <div class="nd-main">
          <div class="nd-files-col">
            <div class="nd-toolbar fade-up" style="animation-delay: 0.14s">
              <!-- 空间分段（灰槽 + 滑动白 pill） -->
              <div v-if="spaceKeys.length > 1" ref="spaceSegEl" class="seg">
                <i class="seg-pill" :style="segPillStyle"></i>
                <button
                  v-for="k in spaceKeys"
                  :key="k"
                  type="button"
                  :class="{ on: space === k }"
                  @click="switchSpace(k)"
                >
                  {{ spaceLabel(k) }}
                </button>
              </div>

              <!-- 选中操作区：有选中项时淡入（移动/删除按 manageable 显隐） -->
              <span class="selbar" :class="{ show: selectedItems.length }">
                <button type="button" class="btn btn-sm" @click="downloadItems(selectedItems)">
                  <span class="ic" v-html="ic('download', 14)"></span>下载
                </button>
                <button type="button" class="btn btn-sm" @click="openShareModal(selectedItems)">
                  <span class="ic" v-html="ic('link', 14)"></span>分享
                </button>
                <button
                  v-if="manageable"
                  type="button"
                  class="btn btn-sm"
                  @click="moveCopyItems('move', selectedItems)"
                >
                  <span class="ic" v-html="ic('swap', 14)"></span>移动
                </button>
                <button
                  type="button"
                  class="btn btn-sm"
                  @click="moveCopyItems('copy', selectedItems)"
                >
                  <span class="ic" v-html="ic('copy', 14)"></span>复制
                </button>
                <button
                  v-if="manageable"
                  type="button"
                  class="btn btn-sm btn-del"
                  @click="removeItems(selectedItems)"
                >
                  <span class="ic" v-html="ic('delete', 14)"></span>删除
                </button>
                <button type="button" class="btn btn-sm" @click="clearSel">
                  <span class="ic" v-html="ic('close', 14)"></span>取消选择
                </button>
              </span>

              <div class="tb-right" :class="{ gone: selectedItems.length }">
                <button v-if="!searchActive" type="button" class="btn btn-sm" @click="openMkdir">
                  <span class="ic" v-html="ic('folder-add', 15)"></span>新建文件夹
                </button>
                <button
                  v-if="!searchActive"
                  type="button"
                  class="btn btn-sm"
                  @click="pickDirUpload"
                >
                  <span class="ic" v-html="ic('folder-add', 15)"></span>上传文件夹
                </button>
                <button
                  v-if="!searchActive"
                  type="button"
                  class="btn btn-sm btn-primary"
                  @click="pickUpload"
                >
                  <span class="ic" v-html="ic('upload', 15)"></span>上传文件
                </button>
              </div>
            </div>

            <div class="pathbar fade-up" style="animation-delay: 0.2s">
              <div
                class="crumb"
                @dragover="onCrumbDragOver"
                @dragleave="onCrumbDragLeave"
                @drop.prevent="onCrumbDrop"
              >
                <template v-if="searchActive">
                  <a href="javascript:;" @click="exitSearch">{{ spaceLabel(space) }}</a>
                  <em>/</em>
                  <b>搜索“{{ searchKw }}”</b>
                </template>
                <template v-else>
                  <a href="javascript:;" data-path="" @click="navTo('')">{{ spaceLabel(space) }}</a>
                  <template v-for="(seg, i) in crumbSegs" :key="seg.path">
                    <em>/</em>
                    <a
                      v-if="i < crumbSegs.length - 1"
                      href="javascript:;"
                      :data-path="seg.path"
                      @click="navTo(seg.path)"
                      >{{ seg.name }}</a
                    >
                    <b v-else>{{ seg.name }}</b>
                  </template>
                </template>
              </div>
              <div class="searchw">
                <span class="ic" v-html="ic('search', 14)"></span>
                <input
                  v-model="searchKw"
                  placeholder="搜索当前空间文件名"
                  maxlength="100"
                  @keydown.enter.prevent="doSearch(searchKw)"
                />
                <span
                  v-if="searchKw || searchActive"
                  class="sw-x"
                  title="清空搜索"
                  @click="exitSearch"
                >
                  <span class="ic" v-html="ic('close', 9)"></span>
                </span>
              </div>
              <!-- 待粘贴提示条：移动/复制进入待粘贴状态后显示，切换目录/空间不丢 -->
              <div v-if="pasteClip" class="paste-bar">
                <span
                  >待粘贴：{{ pasteClip.op === 'move' ? '移动' : '复制' }}
                  {{ pasteClip.items.length }} 项，进入目标目录后右键粘贴</span
                >
                <span class="lk" @click="cancelPaste">取消</span>
              </div>
            </div>

            <div
              ref="gridwrapEl"
              class="gridwrap fade-up"
              :class="{ dragover: dragOver }"
              style="animation-delay: 0.26s"
              @pointerdown="onGridPointerDown"
              @contextmenu.prevent="openBlankMenu($event)"
            >
              <div class="grid" :class="{ 'g-out': fading }">
                <!-- 骨架屏（首载/空目录切换） -->
                <template v-if="loading && !items.length">
                  <div v-for="i in 8" :key="'sk' + i" class="sk-cell">
                    <span class="skeleton sk-ic"></span>
                    <span class="skeleton sk-ln"></span>
                  </div>
                </template>

                <!-- 错误态 -->
                <div v-else-if="loadError" class="nd-state card">
                  <span class="ic" v-html="ic('error-circle', 40)"></span>
                  <div style="margin-top: 10px">{{ loadError }}</div>
                  <button type="button" class="btn btn-sm" @click="reloadCurrent(true)">
                    重试
                  </button>
                </div>

                <!-- 空态 -->
                <div v-else-if="!items.length && !currentDirTasks.length" class="empty">
                  <span class="ic" v-html="ic('folder', 44)"></span>
                  <p>{{ searchActive ? `未找到匹配「${searchKw}」的文件` : '此文件夹为空' }}</p>
                  <p v-if="!searchActive" class="nd-ehint">
                    点击右上角「上传文件」，或直接把文件拖进来
                  </p>
                </div>

                <!-- 文件格 -->
                <template v-else>
                  <div
                    v-for="(it, i) in items"
                    :key="it._rel"
                    class="g-cell"
                    :class="{ sel: sel.has(it._rel), 'cell-in': animate }"
                    :style="animate ? { animationDelay: Math.min(i * 36, 400) + 'ms' } : null"
                    :data-rel="it._rel"
                    :data-idx="i"
                    :draggable="manageable ? 'true' : 'false'"
                    :title="it.name"
                    @click.stop="onCellClick(it, $event)"
                    @dblclick.stop="openItem(it)"
                    @contextmenu.prevent.stop="openItemMenu(it, $event)"
                    @dragstart="onCellDragStart($event, it)"
                    @dragend="onCellDragEnd"
                  >
                    <div class="g-icon">
                      <img
                        v-if="gridImgSrc(it) && !thumbFailed.has(it._rel)"
                        class="g-thumb"
                        :src="gridImgSrc(it)"
                        loading="lazy"
                        alt=""
                        @error="onThumbFail(it._rel)"
                      />
                      <video
                        v-else-if="gridVideoSrc(it)"
                        class="g-thumb"
                        :src="gridVideoSrc(it)"
                        preload="metadata"
                        muted
                        playsinline
                      ></video>
                      <span v-else class="ft-ic" :style="icBox(48)">
                        <img
                          class="ft-img"
                          :src="fileIconUrl(it.name, it.is_dir)"
                          width="48"
                          height="48"
                          draggable="false"
                          alt=""
                          @error="onFtError"
                        />
                        <span class="ft-fb" hidden>
                          <span v-if="it.is_dir" class="ic" v-html="ic('folder', 48)"></span>
                          <span v-else class="ft-fb-lb" :style="ftLbStyle(it.name, 48)">{{
                            typeLabel(it.name)
                          }}</span>
                        </span>
                      </span>
                    </div>
                    <span class="g-name">{{ it.name }}</span>
                    <span v-if="subText(it)" class="g-sub" :title="subText(it)">{{
                      subText(it)
                    }}</span>
                  </div>

                  <!-- 当前目录内联上传格 -->
                  <div
                    v-for="t in currentDirTasks"
                    :key="'up-' + t.id"
                    class="g-cell g-up"
                    :class="{ err: t.state === 'error', gone: t.fading }"
                  >
                    <span class="g-x" title="取消上传" @click="cancelUpload(t)">
                      <span class="ic" v-html="ic('close', 11)"></span>
                    </span>
                    <span class="g-tag tag" :class="upTagClass(t)">
                      <i></i>{{ upTagText(t) }}
                    </span>
                    <div class="g-icon">
                      <span class="ft-ic" :style="icBox(48)">
                        <img
                          class="ft-img"
                          :src="fileIconUrl(t.name, false)"
                          width="48"
                          height="48"
                          draggable="false"
                          alt=""
                          @error="onFtError"
                        />
                        <span class="ft-fb" hidden>
                          <span class="ft-fb-lb" :style="ftLbStyle(t.name, 48)">{{
                            typeLabel(t.name)
                          }}</span>
                        </span>
                      </span>
                    </div>
                    <span class="g-name">{{ t.disp || t.name }}</span>
                    <div class="g-prog">
                      <i :style="{ width: (t.state === 'error' ? 100 : pctOf(t)) + '%' }"></i>
                    </div>
                    <span class="g-sub">{{ upSubText(t) }}</span>
                  </div>
                </template>
              </div>
            </div>

            <div class="statusbar">
              <span
                >共 <b>{{ items.length }}</b> 项</span
              >
              <template v-if="selectedItems.length">
                <span
                  >已选择 <b>{{ selectedItems.length }}</b> 项</span
                >
                <span
                  >合计 <b>{{ fmtSize(selTotalSize) }}</b></span
                >
              </template>
            </div>
          </div>

          <!-- 右侧常驻详情面板（空选占位 / 多选汇总 / 单选详情） -->
          <aside class="nd-info">
            <div v-if="!selectedItems.length" class="ni-empty">未选择条目</div>
            <div v-else-if="selectedItems.length > 1" class="ni-multi">
              <b>已选 {{ selectedItems.length }} 项</b>
              <span>合计 {{ fmtSize(selTotalSize) }}</span>
            </div>
            <template v-else>
              <div class="ni-head">
                <div class="g-icon">
                  <img
                    v-if="gridImgSrc(infoItem) && !thumbFailed.has(infoItem._rel)"
                    class="g-thumb"
                    :src="gridImgSrc(infoItem)"
                    alt=""
                    @error="onThumbFail(infoItem._rel)"
                  />
                  <video
                    v-else-if="gridVideoSrc(infoItem)"
                    class="g-thumb"
                    :src="gridVideoSrc(infoItem)"
                    preload="metadata"
                    muted
                    playsinline
                  ></video>
                  <span v-else class="ft-ic" :style="icBox(48)">
                    <img
                      class="ft-img"
                      :src="fileIconUrl(infoItem.name, infoItem.is_dir)"
                      width="48"
                      height="48"
                      draggable="false"
                      alt=""
                      @error="onFtError"
                    />
                    <span class="ft-fb" hidden>
                      <span v-if="infoItem.is_dir" class="ic" v-html="ic('folder', 48)"></span>
                      <span v-else class="ft-fb-lb" :style="ftLbStyle(infoItem.name, 48)">{{
                        typeLabel(infoItem.name)
                      }}</span>
                    </span>
                  </span>
                </div>
                <div class="ni-name">{{ infoItem.name }}</div>
              </div>
              <dl class="ni-fields">
                <div class="ni-row">
                  <dt>类型</dt>
                  <dd>{{ infoItem.is_dir ? '文件夹' : typeLabel(infoItem.name) + ' 文件' }}</dd>
                </div>
                <div class="ni-row">
                  <dt>大小</dt>
                  <dd>{{ dirSizeText(infoItem) }}</dd>
                </div>
                <div class="ni-row">
                  <dt>修改时间</dt>
                  <dd>{{ fmtSec(infoItem.modified) }}</dd>
                </div>
                <div class="ni-row">
                  <dt>所在路径</dt>
                  <dd>{{ infoLoc }}</dd>
                </div>
              </dl>
            </template>
          </aside>
        </div>
      </section>

      <!-- ================= 分享管理视图 ================= -->
      <section
        v-show="view === 'shares'"
        class="nd-view"
        :class="{ 'view-in': viewAnim === 'shares' }"
      >
        <div v-if="canManageRole" class="notice">
          <span class="ic" v-html="ic('info-circle', 14)"></span>
          <span>班管 / 超管视角：可查看并管理班内全部分享链接；普通成员仅见「我创建的」。</span>
          <span class="seg">
            <button
              type="button"
              :class="{ on: shareScope === 'mine' }"
              @click="switchShareScope('mine')"
            >
              我创建的
            </button>
            <button
              type="button"
              :class="{ on: shareScope === 'all' }"
              @click="switchShareScope('all')"
            >
              全部
            </button>
          </span>
        </div>
        <div class="card tbl-card">
          <div v-if="sharesLoading" class="loading"><span class="spin"></span>加载中…</div>
          <div v-else-if="sharesError" class="nd-state">
            <span class="ic" v-html="ic('error-circle', 40)"></span>
            <div style="margin-top: 10px">{{ sharesError }}</div>
            <button type="button" class="btn btn-sm" @click="loadShares">重试</button>
          </div>
          <div v-else-if="!shares.length" class="empty">
            <span class="ic" v-html="ic('link', 44)"></span>
            <p>暂无分享链接</p>
            <p class="nd-ehint">在「全部文件」中选中文件后，从右键菜单或选中操作区发起分享</p>
          </div>
          <table v-else class="rtable">
            <thead>
              <tr>
                <th>分享内容</th>
                <th style="width: 92px">提取码</th>
                <th style="width: 120px">有效期至</th>
                <th v-if="canManageRole" style="width: 110px">创建人</th>
                <th style="width: 120px">创建时间</th>
                <th style="width: 92px">状态</th>
                <th style="width: 196px">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(s, idx) in shares"
                :key="s.share_id"
                class="row-in"
                :style="{ animationDelay: Math.min(idx * 36, 400) + 'ms' }"
              >
                <td>
                  <div class="nm-cell">
                    <span class="ft-ic" :style="icBox(30)">
                      <img
                        class="ft-img"
                        :src="fileIconUrl(shareFirst(s), !shareFirstIsFile(s))"
                        width="30"
                        height="30"
                        draggable="false"
                        alt=""
                        @error="onFtError"
                      />
                      <span class="ft-fb" hidden>
                        <span
                          v-if="!shareFirstIsFile(s)"
                          class="ic"
                          v-html="ic('folder', 30)"
                        ></span>
                        <span v-else class="ft-fb-lb" :style="ftLbStyle(shareFirst(s), 30)">{{
                          typeLabel(shareFirst(s))
                        }}</span>
                      </span>
                    </span>
                    <div class="nm-wrap">
                      <span class="nm" :title="shareName(s)">{{ shareName(s) }}</span>
                      <span v-if="(s.items || []).length > 1" class="nm-sub"
                        >共 {{ (s.items || []).length }} 项</span
                      >
                    </div>
                  </div>
                </td>
                <td class="code">{{ s.password || '—' }}</td>
                <td class="t2 num">{{ s.expire_at ? fmtDate(s.expire_at) : '永久' }}</td>
                <td v-if="canManageRole" class="t2">{{ s.creator || '' }}</td>
                <td class="t2 num">{{ fmtDate(s.created_at) }}</td>
                <td>
                  <span class="tag" :class="shareStateCls(s.state)">
                    <i></i>{{ shareStateText(s.state) }}
                  </span>
                </td>
                <td>
                  <div class="ops">
                    <span v-if="s.state === 'active'" class="lk" @click="copyShareLink(s)"
                      >复制链接</span
                    >
                    <span v-else class="lk gray dis">复制链接</span>
                    <span
                      v-if="s.state === 'active'"
                      class="lk gray"
                      @click="setShareEnabled(s, false)"
                      >停用</span
                    >
                    <span v-if="s.state === 'disabled'" class="lk" @click="setShareEnabled(s, true)"
                      >重新启用</span
                    >
                    <span class="lk red" @click="removeShare(s)">{{
                      s.state === 'expired' ? '删除记录' : '删除'
                    }}</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 全局上传任务面板（有任务即浮现） -->
    <div v-if="uploads.length" class="upanel">
      <div class="up-head">
        <b>上传任务</b>
        <span>{{ upSummary }}</span>
      </div>
      <div class="up-list">
        <div v-for="t in uploads" :key="t.id" class="up-row" :class="{ err: t.state === 'error' }">
          <span class="ft-ic" :style="icBox(28)">
            <img
              class="ft-img"
              :src="fileIconUrl(t.name, false)"
              width="28"
              height="28"
              draggable="false"
              alt=""
              @error="onFtError"
            />
            <span class="ft-fb" hidden>
              <span class="ft-fb-lb" :style="ftLbStyle(t.name, 28)">{{ typeLabel(t.name) }}</span>
            </span>
          </span>
          <div class="up-main">
            <span class="up-name" :title="t.disp || t.name">{{ t.disp || t.name }}</span>
            <div v-if="t.state !== 'pending'" class="uprog">
              <i :style="{ width: (t.state === 'error' ? 100 : pctOf(t)) + '%' }"></i>
            </div>
            <span class="up-sub">{{ upSubText(t) }}</span>
          </div>
          <div class="up-ops">
            <span v-if="t.state === 'error'" class="up-lk" @click="retryUpload(t)">重试</span>
            <span
              class="up-ic"
              :title="t.state === 'uploading' || t.state === 'pending' ? '取消' : '移除'"
              @click="dismissUpload(t)"
            >
              <span class="ic" v-html="ic('close', 13)"></span>
            </span>
          </div>
        </div>
      </div>
    </div>

    <input ref="fileInput" type="file" multiple hidden @change="onFilesPicked" />
    <input ref="dirInput" type="file" hidden webkitdirectory @change="onDirPicked" />

    <!-- ================= 右键菜单 ================= -->
    <div
      class="ctxmenu"
      :class="{ open: ctx.open }"
      :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }"
      @click.stop
    >
      <template v-for="(en, i) in ctx.entries" :key="i">
        <div v-if="en === '-'" class="ctx-sep"></div>
        <div
          v-else
          class="ctx"
          :class="{ danger: en.danger, dis: en.disabled }"
          @click="runCtx(en)"
        >
          <span class="ic" v-html="ic(en.icon, 15)"></span>
          <span>{{ en.label }}</span>
        </div>
      </template>
    </div>

    <!-- ================= 弹层：单输入（新建/重命名/压缩密码） ================= -->
    <div class="mask" :class="{ open: promptDlg.open }" @click.self="closePrompt(null)">
      <div class="modal" style="width: 420px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ promptDlg.title }}</span>
            <button type="button" class="m-x" @click="closePrompt(null)">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>
          <input
            ref="promptInputEl"
            v-model="promptDlg.value"
            class="input"
            :type="promptDlg.password ? 'password' : 'text'"
            maxlength="200"
            :placeholder="promptDlg.placeholder"
            @keydown.enter.prevent="confirmPrompt"
          />
          <div class="m-foot">
            <button type="button" class="btn btn-sm" @click="closePrompt(null)">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="promptDlg.busy"
              @click="confirmPrompt"
            >
              {{ promptDlg.okText }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：删除确认 ================= -->
    <div class="mask" :class="{ open: delDlg.open }" @click.self="delDlg.open = false">
      <div class="modal" style="width: 420px" @click.stop>
        <div class="m-scroll">
          <div class="m-head"><span class="m-title">删除确认</span></div>
          <p class="m-note" style="margin-top: 0">
            删除后不可恢复。确定删除选中的 {{ delDlg.targets.length }} 项吗？
          </p>
          <div class="m-foot">
            <button type="button" class="btn btn-sm" @click="delDlg.open = false">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-danger"
              :disabled="delDlg.busy"
              @click="confirmDelete"
            >
              删除
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：创建分享 / 分享已创建 ================= -->
    <div class="mask" :class="{ open: shareDlg.open }" @click.self="shareDlg.open = false">
      <div class="modal" style="width: 460px" @click.stop>
        <div class="m-scroll">
          <template v-if="!shareDlg.result">
            <div class="m-head">
              <span class="m-title">创建分享</span>
              <button type="button" class="m-x" @click="shareDlg.open = false">
                <span class="ic" v-html="ic('close', 14)"></span>
              </button>
            </div>
            <div class="m-files">
              <div v-for="it in shareDlg.items" :key="it._rel" class="mf-rw">
                <span class="ft-ic" :style="icBox(24)">
                  <img
                    class="ft-img"
                    :src="fileIconUrl(it.name, it.is_dir)"
                    width="24"
                    height="24"
                    draggable="false"
                    alt=""
                    @error="onFtError"
                  />
                  <span class="ft-fb" hidden>
                    <span v-if="it.is_dir" class="ic" v-html="ic('folder', 24)"></span>
                    <span v-else class="ft-fb-lb" :style="ftLbStyle(it.name, 24)">{{
                      typeLabel(it.name)
                    }}</span>
                  </span>
                </span>
                <span class="mf-nm">{{ it.name }}</span>
                <span class="mf-sz">{{ it.is_dir ? '文件夹' : fmtSize(it.size) }}</span>
              </div>
            </div>
            <div class="kv">
              <label>有效期</label>
              <div class="radios">
                <span
                  v-for="d in [7, 30, 0]"
                  :key="d"
                  class="rd"
                  :class="{ on: shareDlg.expireDays === d }"
                  @click="shareDlg.expireDays = d"
                  ><i></i>{{ d === 0 ? '永久' : d + ' 天' }}</span
                >
              </div>
            </div>
            <div class="kv">
              <label>提取码（自动生成，可刷新）</label>
              <div class="codebox">
                <span class="cd">{{ shareDlg.code }}</span>
                <span class="rf" title="刷新提取码" @click="shareDlg.code = randShareCode()">
                  <span class="ic" v-html="ic('refresh', 15)"></span>
                </span>
              </div>
            </div>
            <div class="m-note">
              外部人员凭链接 + 提取码访问；链接经 Shade 中转，对方无需登录、无需安装。
            </div>
            <div class="m-foot">
              <button type="button" class="btn btn-sm" @click="shareDlg.open = false">取消</button>
              <button
                type="button"
                class="btn btn-sm btn-primary"
                :disabled="shareDlg.busy"
                @click="createShare"
              >
                {{ shareDlg.busy ? '生成中…' : '生成分享链接' }}
              </button>
            </div>
          </template>
          <template v-else>
            <div class="m-head">
              <span class="m-title">分享已创建</span>
              <button type="button" class="m-x" @click="shareDlg.open = false">
                <span class="ic" v-html="ic('close', 14)"></span>
              </button>
            </div>
            <div class="m-note" style="margin-top: 0">
              链接与提取码请一并发送给访问人；有效期至
              {{ shareDlg.result.expire_at ? fmtDate(shareDlg.result.expire_at, true) : '永久' }}。
            </div>
            <div class="kv">
              <label>分享链接</label>
              <div class="rs-link">{{ shareDlg.result.link }}</div>
            </div>
            <div class="kv">
              <label>提取码</label>
              <div class="codebox">
                <span class="cd">{{ shareDlg.result.code }}</span>
              </div>
            </div>
            <div class="m-foot">
              <button type="button" class="btn btn-sm" @click="shareDlg.open = false">关闭</button>
              <button type="button" class="btn btn-sm btn-primary" @click="copyShareResult">
                一键复制
              </button>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：批量重命名（替换文本 / 添加文本 / 格式化） ================= -->
    <div class="mask" :class="{ open: batchDlg.open }" @click.self="batchDlg.open = false">
      <div class="modal" style="width: 560px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">批量重命名（{{ batchDlg.items.length }} 项）</span>
            <button type="button" class="m-x" @click="batchDlg.open = false">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>
          <div class="seg">
            <button
              v-for="m in BATCH_MODES"
              :key="m[0]"
              type="button"
              :class="{ on: batchDlg.mode === m[0] }"
              @click="batchDlg.mode = m[0]"
            >
              {{ m[1] }}
            </button>
          </div>
          <div class="br-form">
            <template v-if="batchDlg.mode === 'replace'">
              <input v-model="batchDlg.find" class="input" maxlength="200" placeholder="查找" />
              <input
                v-model="batchDlg.repl"
                class="input"
                maxlength="200"
                placeholder="替换为（可为空）"
              />
            </template>
            <template v-else-if="batchDlg.mode === 'add'">
              <input
                v-model="batchDlg.affix"
                class="input"
                maxlength="100"
                placeholder="要添加的文本"
              />
              <div class="seg">
                <button
                  type="button"
                  :class="{ on: batchDlg.pos === 'before' }"
                  @click="batchDlg.pos = 'before'"
                >
                  加在开头
                </button>
                <button
                  type="button"
                  :class="{ on: batchDlg.pos === 'after' }"
                  @click="batchDlg.pos = 'after'"
                >
                  加在结尾
                </button>
              </div>
            </template>
            <template v-else>
              <input
                v-model="batchDlg.fmt"
                class="input"
                maxlength="100"
                placeholder="名称格式，如：巡视记录-"
              />
              <div class="seg">
                <button
                  type="button"
                  :class="{ on: batchDlg.numPos === 'before' }"
                  @click="batchDlg.numPos = 'before'"
                >
                  编号在前
                </button>
                <button
                  type="button"
                  :class="{ on: batchDlg.numPos === 'after' }"
                  @click="batchDlg.numPos = 'after'"
                >
                  编号在后
                </button>
              </div>
              <span class="br-label">起始</span>
              <input
                v-model.number="batchDlg.start"
                class="input br-num"
                type="number"
                min="0"
                step="1"
              />
              <span class="br-label">位数</span>
              <input
                v-model.number="batchDlg.pad"
                class="input br-num"
                type="number"
                min="1"
                max="8"
                step="1"
              />
            </template>
          </div>
          <div class="br-list">
            <div
              v-for="r in batchPreview"
              :key="r.path"
              class="br-row"
              :class="{ same: r.same, bad: r.bad }"
            >
              <span class="o">{{ r.old }}</span>
              <span class="a">→</span>
              <span class="n">{{ r.name || '（空）' }}</span>
              <span v-if="r.bad" class="why">{{ r.why }}</span>
            </div>
          </div>
          <div class="m-foot">
            <span class="br-hint" :class="{ err: batchHasBad }">{{
              batchHasBad ? '存在不可用的新名称，请调整' : '扩展名保留，仅重命名主名'
            }}</span>
            <button type="button" class="btn btn-sm" @click="batchDlg.open = false">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="batchDlg.busy || batchHasBad"
              @click="confirmBatchRename"
            >
              {{ batchDlg.busy ? '处理中…' : '应用' }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：压缩包预览 / 解压 ================= -->
    <div class="mask" :class="{ open: arcDlg.open }" @click.self="arcDlg.open = false">
      <div class="modal" style="width: 720px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ arcDlg.item ? arcDlg.item.name : '' }}</span>
            <button type="button" class="m-x" @click="arcDlg.open = false">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>

          <!-- 加密/读取失败：密码行 -->
          <template v-if="arcDlg.stage === 'pw'">
            <div class="m-note" style="margin-top: 0">
              {{
                arcDlg.knownEncrypted
                  ? '该压缩包已加密，请输入压缩密码后浏览。'
                  : '压缩包读取失败或已加密，若设有压缩密码请输入后重试。'
              }}
            </div>
            <div class="ar-pw">
              <input
                v-model="arcDlg.pass"
                class="input"
                type="password"
                placeholder="压缩密码"
                autocomplete="off"
                @keydown.enter.prevent="submitArchivePass"
              />
              <button type="button" class="btn btn-primary" @click="submitArchivePass">确定</button>
            </div>
            <div v-if="arcDlg.error" class="ar-err">{{ arcDlg.error }}</div>
          </template>

          <!-- 包内浏览 -->
          <template v-else>
            <div v-if="arcDlg.comment" class="ar-cmt">{{ arcDlg.comment }}</div>
            <div class="crumb ar-crumb">
              <a href="javascript:;" @click="arcNavTo('/')">{{
                arcDlg.item ? arcDlg.item.name : ''
              }}</a>
              <template v-for="seg in arcCrumbs" :key="seg.rel">
                <em>/</em>
                <a v-if="seg.rel !== arcDlg.inner" href="javascript:;" @click="arcNavTo(seg.rel)">{{
                  seg.name
                }}</a>
                <b v-else>{{ seg.name }}</b>
              </template>
            </div>
            <div class="ar-list">
              <div v-if="arcDlg.loading" class="loading"><span class="spin"></span>加载中…</div>
              <div v-else-if="arcDlg.error" class="nd-state">
                {{ arcDlg.error }}
                <button type="button" class="btn btn-sm" style="margin-left: 10px" @click="arcLoad">
                  重试
                </button>
              </div>
              <div v-else-if="!arcDlg.items.length" class="empty ar-empty">
                <span class="ic" v-html="ic('folder', 40)"></span>
                <p>此目录为空</p>
              </div>
              <template v-else>
                <div
                  v-for="x in arcDlg.items"
                  :key="x.rel || x.name"
                  class="ar-row"
                  :title="x.name"
                  @click="x.is_dir ? arcNavTo(x.rel) : downloadArchiveItem(x)"
                >
                  <span class="ft-ic" :style="icBox(30)">
                    <img
                      class="ft-img"
                      :src="fileIconUrl(x.name, x.is_dir)"
                      width="30"
                      height="30"
                      draggable="false"
                      alt=""
                      @error="onFtError"
                    />
                    <span class="ft-fb" hidden>
                      <span v-if="x.is_dir" class="ic" v-html="ic('folder', 30)"></span>
                      <span v-else class="ft-fb-lb" :style="ftLbStyle(x.name, 30)">{{
                        typeLabel(x.name)
                      }}</span>
                    </span>
                  </span>
                  <div class="ar-mid">
                    <span class="ar-nm">{{ x.name }}</span>
                    <span class="ar-sub">{{
                      x.is_dir
                        ? '文件夹'
                        : fmtSize(x.size) + (x.modified ? ' · ' + fmtDate(x.modified) : '')
                    }}</span>
                  </div>
                  <button
                    v-if="!x.is_dir"
                    type="button"
                    class="btn btn-sm"
                    @click.stop="downloadArchiveItem(x)"
                  >
                    下载
                  </button>
                </div>
              </template>
            </div>
          </template>

          <div v-if="arcDlg.stage !== 'pw'" class="m-foot">
            <button type="button" class="btn btn-sm" @click="arcDlg.open = false">关闭</button>
            <button type="button" class="btn btn-sm btn-primary" @click="decompressArchive">
              解压到…
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：目标位置选择（解压到…） ================= -->
    <div class="mask" :class="{ open: pickDlg.open }" @click.self="closePick(null)">
      <div class="modal" style="width: 520px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ pickDlg.title }}</span>
            <button type="button" class="m-x" @click="closePick(null)">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>
          <div v-if="!pickDlg.fixSpace" class="seg" data-pick-seg>
            <i class="seg-pill" :style="pickPillStyle"></i>
            <button
              v-for="k in spaceKeys"
              :key="k"
              type="button"
              :class="{ on: pickDlg.space === k }"
              @click="switchPickSpace(k)"
            >
              {{ spaceLabel(k) }}
            </button>
          </div>
          <div class="crumb pk-crumb">
            <a href="javascript:;" @click="pickNavTo('')">{{ spaceLabel(pickDlg.space) }}</a>
            <template v-for="seg in pickCrumbs" :key="seg.path">
              <em>/</em>
              <a v-if="seg.path !== pickDlg.dir" href="javascript:;" @click="pickNavTo(seg.path)">{{
                seg.name
              }}</a>
              <b v-else>{{ seg.name }}</b>
            </template>
          </div>
          <div class="pk-list">
            <div v-if="pickDlg.loading" class="loading"><span class="spin"></span>加载中…</div>
            <div v-else-if="pickDlg.error" class="nd-state">
              {{ pickDlg.error }}
              <button
                type="button"
                class="btn btn-sm"
                style="margin-left: 10px"
                @click="pickLoadDirs"
              >
                重试
              </button>
            </div>
            <div v-else-if="!pickDlg.dirs.length" class="empty pk-empty">
              <span class="ic" v-html="ic('folder', 40)"></span>
              <p>此目录下没有文件夹</p>
            </div>
            <template v-else>
              <div
                v-for="d in pickDlg.dirs"
                :key="d.name"
                class="pk-row"
                :class="{ on: pickDlg.selName === d.name }"
                @click="togglePickSel(d.name)"
                @dblclick="pickEnter(d.name)"
              >
                <span class="ft-ic" :style="icBox(24)">
                  <img
                    class="ft-img"
                    :src="fileIconUrl(d.name, true)"
                    width="24"
                    height="24"
                    draggable="false"
                    alt=""
                    @error="onFtError"
                  />
                  <span class="ft-fb" hidden>
                    <span class="ic" v-html="ic('folder', 24)"></span>
                  </span>
                </span>
                <span class="pk-nm">{{ d.name }}</span>
              </div>
            </template>
          </div>
          <label v-if="pickDlg.withNewDir" class="ck-row">
            <input v-model="pickDlg.putIntoNewDir" type="checkbox" />
            解压到以压缩包命名的新目录
          </label>
          <div class="m-foot">
            <span class="lk pk-mkdir" @click="pickMkdir">
              <span class="ic" v-html="ic('folder-add', 14)"></span>新建文件夹
            </span>
            <button type="button" class="btn btn-sm" @click="closePick(null)">取消</button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="pickDlg.busy"
              @click="confirmPick"
            >
              {{ pickDlg.busy ? pickDlg.busyText : pickDlg.confirmText }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：Office 打开方式（协同编辑 / 仅预览） ================= -->
    <div class="mask" :class="{ open: ooDlg.open }" @click.self="ooDlg.open = false">
      <div class="modal" style="width: 300px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ ooDlg.item ? ooDlg.item.name : '' }}</span>
          </div>
          <div style="padding: 4px 2px 2px; display: flex; flex-direction: column; gap: 10px">
            <button type="button" class="btn btn-primary" @click="openOfficePage('edit')">
              协同编辑（可多人同时）
            </button>
            <button type="button" class="btn" @click="openOfficePage('view')">仅预览</button>
          </div>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：视频预览（Lightbox） ================= -->
    <div class="mask" :class="{ open: mediaDlg.open }" @click.self="closeMedia">
      <div class="modal" style="width: 920px" @click.stop>
        <div class="m-scroll">
          <div class="m-head">
            <span class="m-title">{{ mediaDlg.name }}</span>
            <button type="button" class="m-x" @click="closeMedia">
              <span class="ic" v-html="ic('close', 14)"></span>
            </button>
          </div>
          <video class="lb-media" :src="mediaDlg.url" controls autoplay></video>
        </div>
      </div>
    </div>

    <!-- ================= 图片全屏查看器 ================= -->
    <div v-if="vwr.open" class="vwr">
      <div class="vwr-top">
        <span class="vwr-name">{{ vwrName }}</span>
        <span class="vwr-idx">{{ vwr.idx + 1 }} / {{ vwr.imgs.length }}</span>
        <span class="vwr-x" title="关闭（Esc）" @click="closeViewer">
          <span class="ic" v-html="ic('close', 16)"></span>
        </span>
      </div>
      <div ref="vwrStageEl" class="vwr-stage" @wheel.prevent="onVwrWheel">
        <img
          ref="vwrImgEl"
          class="vwr-img"
          :class="{ free: vwr.mode === 'free', dragging: vwrDragging }"
          :src="vwrUrl"
          draggable="false"
          alt=""
          :style="vwrTransform"
          @load="onVwrLoad"
          @pointerdown="onVwrDown"
        />
      </div>
      <div class="vwr-bar">
        <button type="button" :disabled="vwr.imgs.length < 2" @click="vwrNav(-1)">
          <span class="ic" v-html="ic('chevron-left', 16)"></span>
        </button>
        <button type="button" @click="vwrToggleFit">
          {{ vwr.mode === 'fit' ? '原图' : '适应' }}
        </button>
        <button type="button" @click="downloadItems([vwrCurrentItem])">
          <span class="ic" v-html="ic('download', 16)"></span>
        </button>
        <button type="button" :disabled="vwr.imgs.length < 2" @click="vwrNav(1)">
          <span class="ic" v-html="ic('chevron-right', 16)"></span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
// 团队网盘（对照 server/public/netdisk.html 还原）：图标网格 + 右侧常驻详情面板 + 状态栏，
// 空间分段 / 面包屑 / 搜索 / 右键菜单 / 待粘贴移动复制 / 分享管理表格 / 上传任务面板（分片上传），
// 弹层沿用旧版 Shade.modal 口径（单输入、创建分享、批量重命名三模式、压缩包浏览解压、目标位置选择）。
// 功能保留：分片上传（断点续传/退避重传）、创建分享与管理、图片缩略图与全屏查看器、目录大小统计、
// 批量重命名、压缩包（元信息/包内浏览/包内下载/解压到…）、视频 Lightbox、拖入文件上传。
// 媒体取流走 fetch + Bearer + Blob（Go 端仅认 Authorization 头，无 ?token= 直链），不伪造接口。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
import { currentUser } from '../api.js';
import {
  isDemo,
  listSpaces,
  listDir,
  search,
  mkdir,
  rename as renameApi,
  removeItems as removeApi,
  listShares,
  createShare as createShareApi,
  disableShare,
  enableShare,
  deleteShare,
  saveBlob,
  shareLinkUrl,
  randShareCode,
  itemRel,
  uploadFile,
  moveItems,
  copyItems,
  renameBatch,
  dirSize,
  archiveMeta,
  archiveList,
  archiveDecompress,
  archiveDownload,
  mediaUrl,
  thumbUrl,
  dirZipUrl,
  isArchiveName,
  isImageName,
} from '../features/netdisk/api.js';
import {
  extOf,
  typeLabel,
  ftLbStyle,
  fileIconUrl,
  onFtError,
} from '../features/netdisk/fileicon.js';
import { loadLastLoc, saveLastLoc } from '../features/netdisk/lastloc.js';

const router = useRouter();
const demoMode = isDemo();

/* ================= 图标（旧 assets/icons.js 同一套 SVG 路径，内联免依赖） ================= */
const ICONS = {
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  download: '<path d="M12 3.5v11M7 10l5 5 5-5"/><path d="M4.5 19.5h15"/>',
  delete:
    '<path d="M4 7h16M10 11v6M14 11v6"/><path d="M6 7l1 12.2a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7"/><path d="M9 7V4.8A1.8 1.8 0 0 1 10.8 3h2.4A1.8 1.8 0 0 1 15 4.8V7"/>',
  upload: '<path d="M12 15V4M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
  'error-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v5"/><circle cx="12" cy="16.2" r="1" fill="currentColor" stroke="none"/>',
  'info-circle':
    '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><circle cx="12" cy="8" r="1" fill="currentColor" stroke="none"/>',
  book: '<path d="M12 7c-1.7-1.3-4-1.6-6.7-1.4v12.8c2.7-.2 5 .1 6.7 1.4 1.7-1.3 4-1.6 6.7-1.4V5.6C16 5.4 13.7 5.7 12 7z"/><path d="M12 7v12.8"/>',
  'chevron-right': '<path d="M9.5 6.5l5.5 5.5-5.5 5.5"/>',
  'chevron-left': '<path d="M14.5 6.5L9 12l5.5 5.5"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 3.5v4h-4"/>',
  file: '<path d="M13.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5z"/><path d="M13.5 3v4.5H19"/><path d="M9 13h6M9 17h6"/>',
  edit: '<path d="M16.8 3.8l3.4 3.4L8 19.4l-4.6 1.2L4.6 16z"/><path d="M14.5 6.1l3.4 3.4"/>',
  warn: '<path d="M10.3 4.1L2.4 17.6a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 4.1a2 2 0 0 0-3.4 0z"/><path d="M12 9.2v4"/><circle cx="12" cy="16.8" r="1" fill="currentColor" stroke="none"/>',
  swap: '<path d="M4 7.5h11.5"/><path d="M13 4l3.5 3.5L13 11"/><path d="M20 16.5H8.5"/><path d="M11 13l-3.5 3.5L11 20"/>',
  folder:
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  'folder-open':
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2v1"/><path d="M3.5 7v10a2 2 0 0 0 2 2h11.5a2 2 0 0 0 1.86-1.26l2.28-5.7A1.5 1.5 0 0 0 19.75 10H7.44a2 2 0 0 0-1.86 1.26L3.5 16.8"/>',
  'folder-add':
    '<path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/><path d="M12 10.5v5M9.5 13h5"/>',
  link: '<path d="M10 14a5 5 0 0 0 7 .5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 10a5 5 0 0 0-7-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
};
function ic(name, size) {
  const body = ICONS[name] || ICONS.file;
  return `<svg width="${size || 24}" height="${size || 24}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
}
function icBox(size) {
  return { width: `${size}px`, height: `${size}px` };
}

/* ================= 文件类型（旧 netdisk.html 同一映射与回退口径） ================= */
// 类型图标映射与渲染收敛到 features/netdisk/fileicon.js（真实类型 PNG + 文件夹线性图标/字母块回退），
// extOf / typeLabel / ftLbStyle / fileIconUrl / onFtError 自该模块引入
const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg'];
const VIDEO_EXTS = ['mp4', 'webm', 'mov', 'm4v'];
// ONLYOFFICE 格式族（与后端白名单同口径）：edit=可编辑（双击弹「仅预览/协同编辑」）、view=仅预览
const OO_EDIT_EXTS =
  'doc docx docm dot dotx dotm odt ott rtf txt md html epub fb2 xls xlsx xlsm xlt xltx xltm xlsb ods ots csv tsv ppt pptx pptm pot potx potm pps ppsx ppsm odp otp pdf'.split(
    ' '
  );
const OO_VIEW_EXTS =
  'fodt hml htm hwp hwpx mht mhtml pages stw sxw wps wpt xml et ett fods numbers sxc dps dpt fodp key odg sxi djvu xps oxps ofd'.split(
    ' '
  );
function isImg(name) {
  return IMG_EXTS.includes(extOf(name));
}
function isVideo(name) {
  return VIDEO_EXTS.includes(extOf(name));
}
function isArchive(name) {
  return isArchiveName(name);
}
function ooKind(name) {
  const e = extOf(name);
  return OO_EDIT_EXTS.includes(e) ? 'edit' : OO_VIEW_EXTS.includes(e) ? 'view' : '';
}

/* ================= 工具 ================= */
function fmtSize(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}
function toDate(d) {
  if (!d) return null;
  let dt = new Date(d);
  if (Number.isNaN(dt.getTime())) dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  return Number.isNaN(dt.getTime()) ? null : dt;
}
function fmtDate(d, withTime) {
  const dt = toDate(d);
  if (!dt) return '';
  const p = (n) => String(n).padStart(2, '0');
  let s = `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
  if (withTime) s += ` ${p(dt.getHours())}:${p(dt.getMinutes())}`;
  return s;
}
function fmtSec(d) {
  const dt = toDate(d);
  if (!dt) return '—';
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}
function joinRel(dir, name) {
  return dir ? `${dir}/${name}` : name;
}
function parentOf(rel) {
  const p = String(rel || '')
    .split('/')
    .filter(Boolean);
  p.pop();
  return p.join('/');
}
function dirPart(p) {
  const i = String(p || '').lastIndexOf('/');
  return i < 0 ? '' : p.slice(0, i);
}
function basePart(p) {
  const i = String(p || '').lastIndexOf('/');
  return i < 0 ? p : p.slice(i + 1);
}
function sortItems(list) {
  return (list || []).slice().sort((a, b) => {
    if (!!a.is_dir !== !!b.is_dir) return a.is_dir ? -1 : 1;
    return String(a.name).localeCompare(String(b.name), 'zh');
  });
}
function copyText(text, okMsg) {
  const done = () => MessagePlugin.success(okMsg || '已复制');
  const fallback = () => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand('copy');
      done();
    } catch (e) {
      MessagePlugin.error('复制失败，请手动复制');
    }
    ta.remove();
  };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done, fallback);
  } else fallback();
}

/* ================= 页面状态 ================= */
const view = ref('files'); // files 全部文件 / shares 分享管理
const viewAnim = ref('files');
const spaces = reactive({}); // key → {key,name,team,manageable}
const spaceKeys = computed(() => ['my', 'public'].filter((k) => spaces[k]));
const space = ref('my');
const maxMb = ref(0);
const path = ref(''); // 相对空间根目录路径（''=根）
const items = ref([]);
const loading = ref(false);
const loadError = ref('');
const manageable = ref(true); // 当前空间重命名/删除权限（以 /list 下发为准）
const searchKw = ref('');
const searchActive = ref(false);
const searchTotal = ref(0);
const fading = ref(false); // 目录/空间/搜索切换旧内容淡出
const animate = ref(false); // 格子编排式上浮入场

const sel = reactive(new Set()); // 选中集合（条目相对路径）
let anchorIdx = -1; // Shift 连选锚点（当前渲染序）

const me = currentUser() || {};
const canManageRole = me.role === 'admin' || me.role === 'team_admin';

function spaceLabel(k) {
  const sp = spaces[k] || { name: '' };
  return k === 'public' && sp.team ? `${sp.name} · ${sp.team}` : sp.name;
}

const crumbSegs = computed(() => {
  const segs = [];
  if (!path.value) return segs;
  let acc = '';
  path.value
    .split('/')
    .filter(Boolean)
    .forEach((p) => {
      acc = acc ? `${acc}/${p}` : p;
      segs.push({ name: p, path: acc });
    });
  return segs;
});

const selectedItems = computed(() => items.value.filter((it) => sel.has(it._rel)));
const selTotalSize = computed(() =>
  selectedItems.value.reduce((s, it) => s + (it.is_dir ? 0 : it.size || 0), 0)
);
const infoItem = computed(() => selectedItems.value[0] || null);
const infoLoc = computed(() => {
  const it = infoItem.value;
  if (!it) return '';
  const loc = searchActive.value ? it._parentDisp : path.value;
  return loc ? loc : '根目录';
});

// 空间分段滑动白 pill（offsetLeft/offsetWidth 实测定位，重建/切换直接落位）
const spaceSegEl = ref(null);
const segPillStyle = ref({ width: '0px', transform: 'translateX(0px)' });
function moveSegPill(el, style, instant) {
  if (!el) return;
  const on = el.querySelector('button.on');
  if (!on) return;
  style.value = {
    width: `${on.offsetWidth}px`,
    transform: `translateX(${on.offsetLeft}px)`,
    transition: instant ? 'none' : undefined,
  };
  if (instant) {
    nextTick(() => {
      style.value = {
        width: `${on.offsetWidth}px`,
        transform: `translateX(${on.offsetLeft}px)`,
      };
    });
  }
}

function subText(it) {
  const meta = it.is_dir
    ? it.modified
      ? `${fmtDate(it.modified)} 更新`
      : ''
    : fmtSize(it.size) + (it.modified ? ` · ${fmtDate(it.modified)}` : '');
  return searchActive.value ? (meta ? `${meta} · ` : '') + (it._parentDisp || '') : meta;
}

/* ================= 列表加载 ================= */
function decorate(list, parentOfFn) {
  return sortItems(list).map((it) => {
    const parent = parentOfFn(it);
    it._rel = parent ? itemRel(parent, it.name) : it.name;
    return it;
  });
}

async function loadList(silent = false, forceRefresh = false) {
  sel.clear();
  anchorIdx = -1;
  loadError.value = '';
  if (!silent) {
    if (items.value.length) fading.value = true;
    loading.value = true;
  }
  try {
    const d = await listDir({
      space: space.value,
      path: path.value,
      refresh: !!forceRefresh,
    });
    manageable.value = d.manageable !== false;
    items.value = decorate(d.items || [], () => path.value);
    searchActive.value = false;
    animate.value = !silent;
    thumbFailed.clear();
  } catch (e) {
    loadError.value = e.message || '加载失败';
  } finally {
    loading.value = false;
    fading.value = false;
  }
}

async function doSearch(kw) {
  kw = String(kw || '').trim();
  if (!kw) {
    if (searchActive.value) exitSearch();
    return;
  }
  sel.clear();
  anchorIdx = -1;
  loadError.value = '';
  if (items.value.length) fading.value = true;
  loading.value = true;
  try {
    const d = await search({ space: space.value, keywords: kw, page: 1, perPage: 100 });
    searchKw.value = kw;
    searchTotal.value = d.total || 0;
    items.value = decorate(d.items || [], (it) =>
      String(it.parent || '').replace(/^\/+|\/+$/g, '')
    );
    items.value.forEach((it) => {
      it._parentDisp = it.parent ? String(it.parent).replace(/^\/+|\/+$/g, '') : '根目录';
    });
    searchActive.value = true;
    animate.value = true;
    thumbFailed.clear();
  } catch (e) {
    loadError.value = e.message || '搜索失败';
  } finally {
    loading.value = false;
    fading.value = false;
  }
}

function exitSearch() {
  searchKw.value = '';
  searchActive.value = false;
  loadList();
}

// forceRefresh：用户显式刷新（右键刷新/错误重试）时透传 OpenList 强制回源
function reloadCurrent(forceRefresh) {
  if (searchActive.value) doSearch(searchKw.value);
  else loadList(false, !!forceRefresh);
}

function navTo(p) {
  path.value = p;
  searchKw.value = '';
  searchActive.value = false;
  loadList();
  saveLastLoc(me, space.value, path.value); // 记住上次访问目录（按用户）
}

function switchSpace(k) {
  if (space.value === k) return;
  space.value = k;
  path.value = '';
  loadList();
  saveLastLoc(me, k, ''); // 记住上次访问空间（按用户）
  nextTick(() => moveSegPill(spaceSegEl.value, segPillStyle, true));
}

function switchView(v) {
  if (view.value === v) return;
  view.value = v;
  viewAnim.value = v;
  closeCtx();
  if (v === 'shares') loadShares();
}

/* ================= 选择 ================= */
function clearSel() {
  sel.clear();
  anchorIdx = -1;
}
function onCellClick(it, e) {
  const idx = items.value.indexOf(it);
  if (e.shiftKey && anchorIdx >= 0) {
    sel.clear();
    const [a, b] = anchorIdx <= idx ? [anchorIdx, idx] : [idx, anchorIdx];
    for (let i = a; i <= b; i++) sel.add(items.value[i]._rel);
  } else if (e.ctrlKey || e.metaKey) {
    if (sel.has(it._rel)) sel.delete(it._rel);
    else sel.add(it._rel);
    anchorIdx = idx;
  } else {
    sel.clear();
    sel.add(it._rel);
    anchorIdx = idx;
  }
}

/* ================= 框选 / 拖放（旧 netdisk.html 同口径） ================= */
const gridwrapEl = ref(null);
function setSel(next) {
  sel.clear();
  next.forEach((r) => sel.add(r));
}
// 手势期间（框选/拖拽移动）全页禁文字选中：body.nd-gesture + selectstart 拦截双保险，结束务必成对解除
function preventSelect(e) {
  e.preventDefault();
}
function gestureLock(on) {
  document.body.classList.toggle('nd-gesture', on);
  if (on) document.addEventListener('selectstart', preventSelect, true);
  else document.removeEventListener('selectstart', preventSelect, true);
}
// 客户端坐标 → gridwrap 内容坐标（滚动折算；rect 每次现取，锁定布局下仅滚动可变）
function contentPos(cx, cy) {
  const wrap = gridwrapEl.value;
  const r = wrap.getBoundingClientRect();
  return { x: cx - r.left + wrap.scrollLeft, y: cy - r.top + wrap.scrollTop };
}
let rubberEl = null;
let rubberStart = null;
let rubberBase = null;
let rubberMoved = false;
let rubberLast = null;
function onGridPointerDown(e) {
  if (e.button !== 0) return;
  if (e.target.closest && e.target.closest('.g-cell')) return; // 格子上的按下走 click/dblclick
  startRubber(e);
}
function startRubber(e) {
  rubberStart = contentPos(e.clientX, e.clientY);
  rubberLast = { x: e.clientX, y: e.clientY };
  rubberBase = e.ctrlKey || e.metaKey ? new Set(sel) : new Set();
  rubberMoved = false;
  gestureLock(true);
  const onMove = (ev) => {
    rubberLast = { x: ev.clientX, y: ev.clientY };
    moveRubber();
  };
  const onScroll = () => {
    if (rubberMoved) moveRubber();
  }; // 拖动中滚动：矩形随内容滚并重算命中
  const onUp = () => {
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', onUp);
    gridwrapEl.value.removeEventListener('scroll', onScroll);
    endRubber();
  };
  document.addEventListener('pointermove', onMove);
  document.addEventListener('pointerup', onUp);
  document.addEventListener('pointercancel', onUp);
  gridwrapEl.value.addEventListener('scroll', onScroll);
}
function moveRubber() {
  const cur = contentPos(rubberLast.x, rubberLast.y);
  const dx = cur.x - rubberStart.x;
  const dy = cur.y - rubberStart.y;
  if (!rubberMoved && Math.abs(dx) < 4 && Math.abs(dy) < 4) return;
  if (!rubberMoved) {
    rubberMoved = true;
    rubberEl = document.createElement('div');
    rubberEl.className = 'rubber';
    gridwrapEl.value.appendChild(rubberEl);
  }
  const x1 = Math.min(rubberStart.x, cur.x);
  const x2 = Math.max(rubberStart.x, cur.x);
  const y1 = Math.min(rubberStart.y, cur.y);
  const y2 = Math.max(rubberStart.y, cur.y);
  rubberEl.style.left = x1 + 'px';
  rubberEl.style.top = y1 + 'px';
  rubberEl.style.width = x2 - x1 + 'px';
  rubberEl.style.height = y2 - y1 + 'px';
  // 命中检测同用内容坐标（格子静态定位，offset 相对 position:relative 的 gridwrap）
  const hit = new Set(rubberBase);
  gridwrapEl.value.querySelectorAll('.g-cell[data-rel]').forEach((c) => {
    const l = c.offsetLeft;
    const t = c.offsetTop;
    if (l < x2 && l + c.offsetWidth > x1 && t < y2 && t + c.offsetHeight > y1)
      hit.add(c.dataset.rel);
  });
  setSel(hit);
}
function endRubber() {
  gestureLock(false);
  if (rubberEl) {
    const el = rubberEl;
    rubberEl = null;
    el.classList.add('out'); // 淡出后移除
    setTimeout(() => {
      el.remove();
    }, 130);
  }
  if (!rubberMoved) setSel(new Set(rubberBase)); // 纯点击空白：清空（Ctrl 保持原选择）
  rubberStart = null;
  rubberBase = null;
  rubberMoved = false;
  rubberLast = null;
}
// 内部拖拽移动（格子起拖）与拖入上传分流：自定义 dataTransfer type 判内拖
function isInternalMove(e) {
  const t = e.dataTransfer && e.dataTransfer.types;
  return !!(t && Array.prototype.indexOf.call(t, 'application/x-shade-move') >= 0);
}
function onCellDragStart(e, it) {
  if (!manageable.value) {
    e.preventDefault();
    return;
  }
  if (!sel.has(it._rel)) {
    setSel(new Set([it._rel])); // 起拖未选中格：先单选（多选格上起拖则整组移动）
    anchorIdx = items.value.indexOf(it);
  }
  if (!selectedItems.value.length) {
    e.preventDefault();
    return;
  }
  e.dataTransfer.setData('application/x-shade-move', String(sel.size)); // 标记位，数据取选择集
  e.dataTransfer.effectAllowed = 'move';
  gestureLock(true); // 拖动全程禁文字选中
}
function onCellDragEnd() {
  gestureLock(false);
  markDropCell(null);
  markCrumbDrop(null);
}
// 落点解析：仅文件夹格子可作目标目录（上传格/空白无落点，维持当前目录行为）
function findDropFolder(ev) {
  const cell = ev.target && ev.target.closest ? ev.target.closest('.g-cell[data-rel]') : null;
  if (!cell) return null;
  const it = items.value[Number(cell.dataset.idx)];
  return it && it.is_dir ? cell : null;
}
function markDropCell(cell) {
  if (cell === dropTargetCell) return;
  if (dropTargetCell) dropTargetCell.classList.remove('drop-target');
  dropTargetCell = cell;
  if (cell) cell.classList.add('drop-target');
}
function markCrumbDrop(a) {
  if (a === crumbDropEl) return;
  if (crumbDropEl) crumbDropEl.classList.remove('drop-hover');
  crumbDropEl = a;
  if (a) a.classList.add('drop-hover');
}
function crumbDropTarget(e) {
  return e.target && e.target.closest ? e.target.closest('.crumb a[data-path]') : null;
}
let dropTargetCell = null;
let crumbDropEl = null;
let dragDepth = 0;
function onViewDragEnter(e) {
  e.preventDefault();
  if (isInternalMove(e)) {
    markDropCell(findDropFolder(e));
    return;
  }
  dragDepth++;
  if (!searchActive.value) dragOver.value = true;
  markDropCell(findDropFolder(e));
}
function onViewDragOver(e) {
  e.preventDefault();
  if (isInternalMove(e)) {
    const cell = findDropFolder(e);
    e.dataTransfer.dropEffect = cell ? 'move' : 'none'; // 仅文件夹格是有效落点
    markDropCell(cell);
    return;
  }
  markDropCell(findDropFolder(e)); // 持续跟随指针换格高亮
}
function onViewDragLeave(e) {
  e.preventDefault();
  if (isInternalMove(e)) return; // 内拖高亮由 dragover 持续校正、dragend/drop 清理
  if (--dragDepth <= 0) {
    dragDepth = 0;
    dragOver.value = false;
    markDropCell(null);
  }
}
function onCrumbDragOver(e) {
  if (!isInternalMove(e)) return;
  const a = crumbDropTarget(e);
  if (!a) {
    markCrumbDrop(null);
    return;
  }
  e.preventDefault(); // 仅有效落点放行 drop
  e.dataTransfer.dropEffect = 'move';
  markCrumbDrop(a);
}
function onCrumbDragLeave(e) {
  if (!isInternalMove(e)) return;
  if (!e.currentTarget.contains(e.relatedTarget)) markCrumbDrop(null);
}
function onCrumbDrop(e) {
  if (!isInternalMove(e)) return;
  const a = crumbDropTarget(e);
  markCrumbDrop(null);
  if (!a) return;
  e.preventDefault();
  moveItemsTo(a.dataset.path);
}
// 内拖移动入口：把选中条目移入目标目录（'' = 空间根；校验自身/子目录与同目录）
let dndMoving = false;
function moveItemsTo(targetRel) {
  if (dndMoving) return;
  const list = selectedItems.value;
  if (!list.length || targetRel === null || targetRel === undefined) return;
  for (const it of list) {
    const r = it._rel;
    if (it.is_dir && (targetRel === r || targetRel.indexOf(r + '/') === 0)) {
      MessagePlugin.error('不能移动到自身或其子目录');
      return;
    }
  }
  const groups = {};
  list.forEach((it) => {
    const d = parentOf(it._rel);
    (groups[d] = groups[d] || []).push(it.name);
  });
  const dirs = Object.keys(groups);
  if (dirs.length === 1 && dirs[0] === targetRel) {
    MessagePlugin.error('源位置与目标位置相同');
    return;
  }
  dndMoving = true;
  Promise.resolve()
    .then(async () => {
      for (const d of dirs) {
        await moveItems({
          srcSpace: space.value,
          srcDir: d,
          names: groups[d],
          dstSpace: space.value,
          dstDir: targetRel,
        });
      }
    })
    .then(() => {
      dndMoving = false;
      MessagePlugin.success(`已移动 ${list.length} 项`);
      sel.clear();
      clearDirSizeCache();
      reloadCurrent();
    })
    .catch((e) => {
      dndMoving = false;
      MessagePlugin.error(e.message || '移动失败');
    });
}
// collectDropFiles 拖放项收集为 [{file,relPath}] / [{dir:true,relPath}]（目录感知，保留层级）
function collectDropFiles(dt) {
  const items = (dt && dt.items) || [];
  const entries = [];
  for (let i = 0; i < items.length; i++) {
    if (items[i].kind !== 'file') continue;
    const entry = items[i].webkitGetAsEntry && items[i].webkitGetAsEntry();
    if (entry) entries.push(entry);
  }
  if (!entries.length) {
    return Promise.resolve(
      Array.prototype.slice.call((dt && dt.files) || []).map((f) => ({ file: f, relPath: f.name }))
    );
  }
  const out = [];
  let chain = Promise.resolve();
  entries.forEach((entry) => {
    chain = chain.then(() => walkEntry(entry, '', out));
  });
  return chain.then(() => out);
}
// 单个 FileSystemEntry 递归收集（坏项跳过不中断整体）；目录项另推 {dir:true, relPath} 记录——
// 空文件夹借此建出（非空目录同样记录，uploadEntries 建树时按目录集去重）
function walkEntry(entry, prefix, out) {
  if (entry.isFile) {
    return new Promise((res, rej) => {
      entry.file(res, rej);
    })
      .then((f) => {
        out.push({ file: f, relPath: prefix + f.name });
      })
      .catch(() => {
        /* 读取失败的项跳过 */
      });
  }
  if (entry.isDirectory) {
    out.push({ dir: true, relPath: prefix + entry.name });
    return readEntriesAll(entry.createReader())
      .then((children) => {
        let chain = Promise.resolve();
        children.forEach((c) => {
          chain = chain.then(() => walkEntry(c, prefix + entry.name + '/', out));
        });
        return chain;
      })
      .catch(() => {
        /* 目录读取失败跳过 */
      });
  }
  return Promise.resolve();
}
// readEntries 每次最多 100 条，循环取到空为止
function readEntriesAll(reader) {
  const out = [];
  function step() {
    return new Promise((res, rej) => {
      reader.readEntries(res, rej);
    }).then((batch) => {
      if (!batch.length) return out;
      out.push(...batch);
      return step();
    });
  }
  return step();
}

/* ================= 网格/详情图标区媒体源（旧 cellIconHtml / gridImgUrl 同口径） ================
   位图走 /thumb 缩略图直链（lazy 交给浏览器）、svg 等小图 inline 直链、视频取首帧（preload=metadata）；
   其余走文件类型图标。缩略图加载失败回退类型图标（不回退原图直链，防大文件流量/内存回潮）。 */
const thumbFailed = reactive(new Set()); // 缩略图加载失败的 rel（该条目改走类型图标）
function gridImgSrc(it) {
  if (!it || it.is_dir) return '';
  const e = extOf(it.name);
  if (isImageName(it.name)) return thumbUrl({ space: space.value, path: it._rel });
  if (e === 'svg') return mediaUrl({ space: space.value, path: it._rel, disposition: 'inline' });
  return '';
}
function gridVideoSrc(it) {
  if (!it || it.is_dir || !isVideo(it.name)) return '';
  return mediaUrl({ space: space.value, path: it._rel, disposition: 'inline' });
}
function onThumbFail(rel) {
  thumbFailed.add(rel);
}

/* ================= 目录大小（单选文件夹：会话缓存 + 请求序号防过期覆盖） ================= */
const dirSizeCache = new Map();
const dirSizePending = new Set();
let dirSizeSeq = 0;
function clearDirSizeCache() {
  dirSizeSeq += 1;
  dirSizeCache.clear();
  dirSizePending.clear();
}
function fetchDirSize(spaceKey, rel) {
  const k = `${spaceKey}|${rel}`;
  if (dirSizePending.has(k)) return;
  dirSizePending.add(k);
  const seq = ++dirSizeSeq;
  dirSize({ space: spaceKey, path: rel })
    .then((d) => {
      dirSizePending.delete(k);
      if (seq !== dirSizeSeq) return;
      dirSizeCache.set(k, { size: d.size, files: d.files, truncated: !!d.truncated });
    })
    .catch(() => {
      dirSizePending.delete(k);
      if (seq !== dirSizeSeq) return;
      dirSizeCache.set(k, { error: true });
    });
}
function dirSizeText(it) {
  if (!it) return '—';
  if (!it.is_dir) return fmtSize(it.size);
  const k = `${space.value}|${it._rel}`;
  const rec = dirSizeCache.get(k);
  if (!rec) {
    fetchDirSize(space.value, it._rel);
    return '计算中…';
  }
  if (rec.error) return '计算失败';
  return (
    (rec.truncated ? '≥' : '') +
    fmtSize(rec.size) +
    (rec.truncated ? '（统计超上限）' : `（${rec.files} 个文件）`)
  );
}

/* ================= 打开 / 预览 / 下载 ================= */
function openItem(it) {
  if (!it) return;
  if (it.is_dir) {
    navTo(it._rel);
    return;
  }
  if (isImg(it.name)) return openViewer(it);
  if (isVideo(it.name)) return previewMedia(it);
  if (extOf(it.name) === 'pdf') {
    openOfficeRoute(it, 'view');
    return;
  }
  const kind = ooKind(it.name);
  if (kind === 'edit') {
    ooDlg.item = it;
    ooDlg.open = true;
    return;
  }
  if (kind === 'view') {
    openOfficeRoute(it, 'view');
    return;
  }
  if (isArchive(it.name)) return openArchive(it);
  downloadItems([it]);
}

// ONLYOFFICE 承载页当前窗口打开（space/path/mode 经 query 传递；back 回网盘）
function openOfficeRoute(it, mode) {
  router.push({
    path: '/office',
    query: {
      space: space.value,
      path: it._rel,
      name: it.name,
      mode: mode === 'view' ? 'view' : 'edit',
      back: '/netdisk',
    },
  });
}
const ooDlg = reactive({ open: false, item: null });
function openOfficePage(mode) {
  const it = ooDlg.item;
  ooDlg.open = false;
  if (it) openOfficeRoute(it, mode);
}

// 原生直链下载：临时 <a download> 触发浏览器保存，不经内存 Blob 中转（大文件即点即下，
// 浏览器下载器出进度）；name 仅作回退，缺省/为空时取响应 Content-Disposition 文件名
function triggerDownload(url, name) {
  if (!url) {
    MessagePlugin.info('演示模式不执行该下载操作，请切换 live 适配层');
    return;
  }
  const a = document.createElement('a');
  a.href = url;
  a.download = name || '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}
function downloadItem(it) {
  // 文件夹整包 ZIP 直链：后端递归流式打包，保留层级与空目录
  if (it.is_dir) {
    triggerDownload(dirZipUrl({ space: space.value, path: it._rel }), `${it.name}.zip`);
    return;
  }
  triggerDownload(
    mediaUrl({ space: space.value, path: it._rel, disposition: 'attachment' }),
    it.name
  );
}
function downloadItems(list) {
  if (!list.length) {
    MessagePlugin.info('请先选择要下载的内容');
    return;
  }
  list.forEach((f, i) => {
    setTimeout(() => downloadItem(f), i * 300);
  });
}

// 视频 Lightbox：URL 带 ?token= 鉴权直引（同旧 previewMedia）
const mediaDlg = reactive({ open: false, name: '', url: '' });
function previewMedia(it) {
  const url = mediaUrl({ space: space.value, path: it._rel, disposition: 'inline' });
  if (!url) {
    MessagePlugin.info('演示模式不执行该预览操作，请切换 live 适配层');
    return;
  }
  mediaDlg.url = url;
  mediaDlg.name = it.name;
  mediaDlg.open = true;
}
function closeMedia() {
  mediaDlg.open = false;
  mediaDlg.url = '';
}

/* ================= 图片全屏查看器（←→ 循环 / 滚轮缩放 0.1~8× / 拖拽平移 / 单击 适应↔原图 / Esc） ================= */
const vwr = reactive({
  open: false,
  imgs: [], // [{item, url}]
  idx: 0,
  mode: 'fit',
  scale: 1,
  tx: 0,
  ty: 0,
});
const vwrStageEl = ref(null);
const vwrImgEl = ref(null);
const vwrDragging = ref(false);
const vwrCurrentItem = computed(() => (vwr.imgs[vwr.idx] ? vwr.imgs[vwr.idx].item : null));
const vwrName = computed(() => (vwrCurrentItem.value ? vwrCurrentItem.value.name : ''));
const vwrUrl = computed(() => (vwr.imgs[vwr.idx] ? vwr.imgs[vwr.idx].url : ''));
const vwrTransform = computed(
  () => `transform: translate(-50%, -50%) translate(${vwr.tx}px, ${vwr.ty}px) scale(${vwr.scale})`
);

async function openViewer(it) {
  const imgs = items.value.filter((x) => !x.is_dir && isImg(x.name));
  const idx = imgs.indexOf(it);
  if (idx < 0) return;
  vwr.imgs = imgs.map((x) => ({
    item: x,
    url: mediaUrl({ space: space.value, path: x._rel, disposition: 'inline' }),
  }));
  vwr.idx = idx;
  vwr.open = true;
  vwr.mode = 'fit';
  vwr.scale = 1;
  vwr.tx = 0;
  vwr.ty = 0;
  document.body.style.overflow = 'hidden';
  window.addEventListener('keydown', onVwrKey, true);
  window.addEventListener('resize', onVwrResize);
  await ensureVwrUrl();
}
// ensureVwrUrl 当前图片取流（?token= 直链，同旧 vwrUrl；预生成 URL 缺失时补齐）
async function ensureVwrUrl() {
  const cur = vwr.imgs[vwr.idx];
  if (cur && !cur.url) {
    cur.url = mediaUrl({ space: space.value, path: cur.item._rel, disposition: 'inline' });
  }
}
function vwrShow() {
  vwr.mode = 'fit';
  vwr.tx = 0;
  vwr.ty = 0;
  vwr.scale = 1;
  ensureVwrUrl();
  nextTick(() => {
    const img = vwrImgEl.value;
    if (img && img.complete && img.naturalWidth) vwrFit();
  });
}
function vwrNav(d) {
  if (vwr.imgs.length < 2) return;
  vwr.idx = (vwr.idx + d + vwr.imgs.length) % vwr.imgs.length;
  vwrShow();
}
function vwrFit() {
  const img = vwrImgEl.value;
  const stage = vwrStageEl.value;
  if (!img || !stage) return;
  const natW = img.naturalWidth;
  const natH = img.naturalHeight;
  if (!natW || !natH) return;
  const r = stage.getBoundingClientRect();
  vwr.scale = Math.min(8, Math.max(0.1, Math.min(r.width / natW, r.height / natH)));
  vwr.tx = 0;
  vwr.ty = 0;
  vwr.mode = 'fit';
}
function vwrToggleFit() {
  if (vwr.mode === 'fit') {
    vwr.scale = 1;
    vwr.tx = 0;
    vwr.ty = 0;
    vwr.mode = 'free';
  } else vwrFit();
}
function onVwrLoad() {
  if (vwr.mode === 'fit') vwrFit();
}
function onVwrResize() {
  if (vwr.open && vwr.mode === 'fit') vwrFit();
}
// 滚轮缩放：光标指向的内容点在缩放前后保持不动
function onVwrWheel(e) {
  const ns = Math.min(8, Math.max(0.1, vwr.scale * (e.deltaY < 0 ? 1.15 : 1 / 1.15)));
  if (ns === vwr.scale) return;
  const stage = vwrStageEl.value;
  const r = stage.getBoundingClientRect();
  const px = e.clientX - r.left - r.width / 2;
  const py = e.clientY - r.top - r.height / 2;
  vwr.tx = px - (px - vwr.tx) * (ns / vwr.scale);
  vwr.ty = py - (py - vwr.ty) * (ns / vwr.scale);
  vwr.scale = ns;
  vwr.mode = 'free';
}
let vwrDrag = null;
function onVwrDown(e) {
  if (e.button !== 0) return;
  e.preventDefault();
  vwrDrag = { x: e.clientX, y: e.clientY, moved: false };
  vwrImgEl.value.setPointerCapture(e.pointerId);
  const onMove = (ev) => {
    if (!vwrDrag) return;
    const dx = ev.clientX - vwrDrag.x;
    const dy = ev.clientY - vwrDrag.y;
    if (!vwrDrag.moved && Math.abs(dx) < 4 && Math.abs(dy) < 4) return;
    vwrDrag.moved = true;
    if (vwr.mode !== 'free') return;
    vwrDragging.value = true;
    vwr.tx += dx;
    vwr.ty += dy;
    vwrDrag.x = ev.clientX;
    vwrDrag.y = ev.clientY;
  };
  const onUp = () => {
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', onUp);
    vwrDragging.value = false;
    const wasDrag = vwrDrag && vwrDrag.moved;
    vwrDrag = null;
    if (!wasDrag) vwrToggleFit();
  };
  document.addEventListener('pointermove', onMove);
  document.addEventListener('pointerup', onUp);
  document.addEventListener('pointercancel', onUp);
}
function onVwrKey(e) {
  if (!vwr.open) return;
  if (e.key === 'Escape') {
    e.stopPropagation();
    e.preventDefault();
    closeViewer();
  } else if (e.key === 'ArrowLeft') {
    e.stopPropagation();
    vwrNav(-1);
  } else if (e.key === 'ArrowRight') {
    e.stopPropagation();
    vwrNav(1);
  }
}
function closeViewer() {
  vwr.open = false;
  document.body.style.overflow = '';
  window.removeEventListener('keydown', onVwrKey, true);
  window.removeEventListener('resize', onVwrResize);
}

/* ================= 单输入弹层（新建文件夹 / 重命名 / 压缩密码） ================= */
const promptDlg = reactive({
  open: false,
  title: '',
  value: '',
  placeholder: '',
  okText: '确定',
  password: false,
  busy: false,
});
const promptInputEl = ref(null);
let promptResolve = null;
function openPrompt(opts) {
  return new Promise((resolve) => {
    promptResolve = resolve;
    Object.assign(promptDlg, {
      open: true,
      title: opts.title || '',
      value: opts.value || '',
      placeholder: opts.placeholder || '',
      okText: opts.okText || '确定',
      password: !!opts.password,
      busy: false,
    });
    nextTick(() => {
      const el = promptInputEl.value;
      if (!el) return;
      el.focus();
      if (opts.value) {
        const dot = opts.value.lastIndexOf('.');
        el.setSelectionRange(0, dot > 0 ? dot : opts.value.length);
      }
    });
  });
}
function closePrompt(v) {
  promptDlg.open = false;
  const r = promptResolve;
  promptResolve = null;
  if (r) r(v);
}
function confirmPrompt() {
  const v = promptDlg.value.trim();
  if (!v) {
    MessagePlugin.error(promptDlg.password ? '请输入压缩密码' : '请输入名称');
    return;
  }
  closePrompt(v);
}

function openMkdir() {
  if (searchActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览');
    return;
  }
  openPrompt({ title: '新建文件夹', placeholder: '请输入文件夹名称', okText: '创建' }).then(
    async (name) => {
      if (!name) return;
      try {
        await mkdir({ space: space.value, path: path.value, name });
        MessagePlugin.success('已创建');
        clearDirSizeCache();
        loadList();
      } catch (e) {
        MessagePlugin.error(e.message || '创建失败');
      }
    }
  );
}
function openRename(it) {
  if (!it) return;
  openPrompt({ title: '重命名', value: it.name, placeholder: '请输入新名称', okText: '保存' }).then(
    async (name) => {
      if (!name || name === it.name) return;
      try {
        await renameApi({ space: space.value, path: it._rel, name });
        MessagePlugin.success('已重命名');
        clearDirSizeCache();
        reloadCurrent();
      } catch (e) {
        MessagePlugin.error(e.message || '重命名失败');
      }
    }
  );
}

/* ================= 删除 ================= */
const delDlg = reactive({ open: false, busy: false, targets: [] });
function removeItems(list) {
  if (!list.length) return;
  delDlg.targets = list;
  delDlg.busy = false;
  delDlg.open = true;
}
async function confirmDelete() {
  delDlg.busy = true;
  try {
    await removeApi({ space: space.value, paths: delDlg.targets.map((it) => it._rel) });
    MessagePlugin.success('已删除');
    delDlg.open = false;
    sel.clear();
    clearDirSizeCache();
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  } finally {
    delDlg.busy = false;
  }
}

/* ================= 分片上传（features/netdisk/api.js uploadFile 管线） ================= */
const fileInput = ref(null);
const dirInput = ref(null);
const uploads = ref([]); // {id,file,name,disp,size,space,path,state,received,doneChunks,totalChunks,err,cancelled,batch,fading}
let upSeq = 0;
let upRunning = false;
const batches = reactive({}); // bid → {total, toasted}
let batchSeq = 0;

function pickUpload() {
  if (searchActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览再上传');
    return;
  }
  fileInput.value.value = '';
  fileInput.value.click();
}
function pickDirUpload() {
  if (searchActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览再上传');
    return;
  }
  dirInput.value.value = '';
  dirInput.value.click();
}
function onFilesPicked(e) {
  const list = Array.from(e.target.files || []);
  e.target.value = '';
  enqueueUploads(list);
}
function onDirPicked(e) {
  const files = Array.from(e.target.files || []);
  e.target.value = '';
  if (!files.length) return;
  uploadEntries(files.map((f) => ({ file: f, relPath: f.webkitRelativePath || f.name })));
}
// 拖放落点（旧 viewFiles drop 同口径）：内拖=移动到文件夹格；外拖=目录感知上传，
// 拖到文件夹格子上以该文件夹为目标目录，空白仍当前目录
function onDropFiles(e) {
  e.preventDefault();
  if (isInternalMove(e)) {
    const mcell = findDropFolder(e); // 先取落点再清理高亮
    markDropCell(null);
    if (mcell) moveItemsTo(mcell.dataset.rel); // 空白处非落点（浏览态即当前目录=源目录，静默不动作）
    return;
  }
  dragDepth = 0;
  dragOver.value = false;
  const cell = findDropFolder(e); // 先取落点再清理高亮
  markDropCell(null);
  const targetPath = cell ? cell.dataset.rel : null; // 文件夹格落点（搜索态仅此放行），空白仍当前目录
  collectDropFiles(e.dataTransfer).then((list) => {
    if (!list.length) return;
    uploadEntries(list, targetPath); // 统一走目录感知入口（扁平文件 relPath 即文件名，不触发建树）
  });
}
const dragOver = ref(false);

function enqueueOne(f, disp, dirPath, bid) {
  if (maxMb.value && f.size > maxMb.value * 1024 * 1024) {
    MessagePlugin.error(`「${f.name}」超过单文件上限 ${maxMb.value} MB，已跳过`);
    return false;
  }
  uploads.value.push({
    id: `u-${++upSeq}`,
    file: f,
    name: f.name,
    disp: disp || f.name,
    size: f.size,
    space: space.value,
    path: dirPath || path.value,
    state: 'pending',
    received: 0,
    doneChunks: 0,
    totalChunks: 0,
    err: '',
    cancelled: false,
    batch: bid || 0,
    fading: false,
  });
  return true;
}
function enqueueUploads(list) {
  let added = 0;
  list.forEach((f) => {
    if (enqueueOne(f)) added++;
  });
  if (added) pumpUploads();
}
// 文件夹上传：按 relPath 逐级建树（批内去重、已存在吞掉），再入队走分片管线
async function uploadEntries(entries, targetPath) {
  if (searchActive.value && !targetPath) {
    MessagePlugin.info('搜索状态下请先返回目录浏览再上传');
    return;
  }
  if (!entries.length) return;
  const target = targetPath || path.value;
  const dirSet = {};
  let fileCount = 0;
  entries.forEach((en) => {
    let d = en.file ? dirPart(en.relPath) : en.relPath;
    while (d) {
      dirSet[d] = 1;
      d = dirPart(d);
    }
    if (en.file) fileCount += 1;
  });
  const dirs = Object.keys(dirSet).sort((a, b) => a.split('/').length - b.split('/').length);
  if (dirs.length) {
    MessagePlugin.info('正在创建目录结构…');
    const made = {};
    for (const rel of dirs) {
      if (made[rel]) continue;
      made[rel] = 1;
      const parent = dirPart(rel);
      try {
        await mkdir({
          space: space.value,
          path: parent ? joinRel(target, parent) : target,
          name: basePart(rel),
        });
      } catch (e) {
        /* 目录已存在等错误吞掉，建树不中断 */
      }
    }
  }
  if (!fileCount) {
    if (dirs.length) {
      MessagePlugin.success('已创建目录结构');
      clearDirSizeCache();
      loadList(true);
    }
    return;
  }
  const bid = dirs.length ? ++batchSeq : 0;
  let added = 0;
  entries.forEach((en) => {
    if (!en.file) return;
    const dp = dirPart(en.relPath);
    if (enqueueOne(en.file, en.relPath, dp ? joinRel(target, dp) : target, bid)) added++;
  });
  if (!added) return;
  if (bid) batches[bid] = { total: added, toasted: false };
  pumpUploads();
}

async function pumpUploads() {
  if (upRunning) return;
  upRunning = true;
  for (;;) {
    const t = uploads.value.find((x) => x.state === 'pending');
    if (!t) break;
    t.state = 'uploading';
    t.err = '';
    try {
      await uploadFile({
        file: t.file,
        space: t.space,
        path: t.path,
        onProgress: (p) => {
          t.received = p.received;
          t.doneChunks = p.doneChunks;
          t.totalChunks = p.totalChunks;
        },
        isCancelled: () => t.cancelled,
      });
      if (t.cancelled) {
        t.state = 'aborted';
      } else {
        t.state = 'done';
        t.received = t.size;
        MessagePlugin.success(`「${t.disp || t.name}」上传完成`);
        // 上传完成格停留后淡出，再静默刷新当前目录
        if (space.value === t.space && path.value === t.path && !searchActive.value) {
          t.fading = true;
          clearDirSizeCache();
          await loadList(true);
          t.fading = false;
        }
      }
    } catch (e) {
      if (t.cancelled) {
        t.state = 'aborted';
      } else {
        t.state = 'error';
        t.err = e.message || '上传失败';
        MessagePlugin.error(`「${t.disp || t.name}」上传失败：${t.err}`);
      }
    }
  }
  upRunning = false;
  // 文件夹批次收尾：全部出队后汇总提示一次
  Object.keys(batches).forEach((bid) => {
    const b = batches[bid];
    if (!b || b.toasted) return;
    const ts = uploads.value.filter((t) => t.batch === Number(bid));
    if (ts.some((t) => t.state === 'pending' || t.state === 'uploading')) return;
    b.toasted = true;
    const doneCnt = ts.filter((t) => t.state === 'done').length;
    const failed = ts.filter((t) => t.state === 'error').length;
    if (failed) {
      MessagePlugin.warning(`文件夹上传完成：${doneCnt} 个文件，失败 ${failed} 个`);
    } else {
      MessagePlugin.success(`文件夹上传完成：${doneCnt} 个文件`);
    }
    delete batches[bid];
  });
}

function cancelUpload(t) {
  t.cancelled = true;
  if (t.state === 'pending') t.state = 'aborted';
}
function retryUpload(t) {
  t.state = 'pending';
  t.err = '';
  t.cancelled = false;
  t.received = 0;
  pumpUploads();
}
function dismissUpload(t) {
  if (t.state === 'uploading' || t.state === 'pending') {
    t.cancelled = true;
    if (t.state === 'pending') t.state = 'aborted';
  }
  uploads.value = uploads.value.filter((x) => x !== t);
}
function pctOf(t) {
  return t.size ? Math.min(100, Math.round((t.received / t.size) * 100)) : 100;
}
function upSubText(t) {
  if (t.state === 'error') return t.err || '上传失败';
  if (t.state === 'pending') return `等待上传 · ${fmtSize(t.size)}`;
  if (t.state === 'done') return `已完成 · ${fmtSize(t.size)}`;
  if (t.state === 'aborted') return '已取消';
  let s = `${fmtSize(t.received)} / ${fmtSize(t.size)} · ${pctOf(t)}%`;
  if (t.totalChunks) s += ` · 分片 ${Math.min(t.doneChunks, t.totalChunks)}/${t.totalChunks}`;
  return s;
}
function upTagClass(t) {
  if (t.state === 'error') return 'st-bad';
  if (t.state === 'done') return 'st-ok';
  return 'st-info';
}
function upTagText(t) {
  if (t.state === 'error') return '失败';
  if (t.state === 'done') return '已完成';
  return t.state === 'pending' ? '排队中' : '上传中';
}
const upSummary = computed(() => {
  const active = uploads.value.filter((t) => t.state === 'uploading').length;
  const pend = uploads.value.filter((t) => t.state === 'pending').length;
  const fail = uploads.value.filter((t) => t.state === 'error').length;
  const sum = [];
  if (active) sum.push(`${active} 个上传中`);
  if (pend) sum.push(`${pend} 个排队`);
  if (fail) sum.push(`${fail} 个失败`);
  return sum.length ? sum.join(' · ') : '已完成';
});
// 当前目录的内联上传格（浏览态且目标目录一致才显示）
const currentDirTasks = computed(() => {
  if (view.value !== 'files' || searchActive.value) return [];
  return uploads.value.filter(
    (t) =>
      t.space === space.value &&
      t.path === path.value &&
      ['pending', 'uploading', 'error', 'done'].includes(t.state)
  );
});

/* ================= 右键菜单（fixed；外部点按/Esc 关闭） ================= */
const ctx = reactive({ open: false, x: 0, y: 0, entries: [] });
function closeCtx() {
  ctx.open = false;
}
function openCtxAt(x, y, entries) {
  ctx.open = false;
  ctx.entries = entries;
  nextTick(() => {
    ctx.open = true;
    nextTick(() => {
      const el = document.querySelector('.ctxmenu');
      if (!el) return;
      let nx = x;
      let ny = y;
      const w = el.offsetWidth;
      const h = el.offsetHeight;
      if (nx + w > window.innerWidth - 8) nx = Math.max(8, window.innerWidth - 8 - w);
      if (ny + h > window.innerHeight - 8) ny = Math.max(8, window.innerHeight - 8 - h);
      ctx.x = nx;
      ctx.y = ny;
    });
  });
}
function runCtx(en) {
  if (!en || en === '-' || en.disabled) return;
  closeCtx();
  if (en.onClick) en.onClick();
}
function openItemMenu(it, e) {
  if (!sel.has(it._rel)) {
    sel.clear();
    sel.add(it._rel);
    anchorIdx = items.value.indexOf(it);
  }
  const list = selectedItems.value;
  const single = list.length === 1 ? list[0] : null;
  const entries = [
    {
      icon: single && single.is_dir ? 'folder-open' : 'file',
      label: '打开',
      disabled: !single,
      onClick: () => openItem(single),
    },
  ];
  if (single && !single.is_dir) {
    const kind = ooKind(single.name);
    if (kind === 'edit') {
      entries.push({
        icon: 'edit',
        label: '在线编辑',
        onClick: () => {
          ooDlg.item = single;
          ooDlg.open = true;
        },
      });
    }
    if (kind) {
      entries.push({
        icon: 'book',
        label: '在线预览',
        onClick: () => openOfficeRoute(single, 'view'),
      });
    }
    if (isVideo(single.name)) {
      entries.push({
        icon: 'book',
        label: '预览',
        onClick: () => previewMedia(single),
      });
    }
    if (isImg(single.name)) {
      entries.push({
        icon: 'image',
        label: '查看大图',
        onClick: () => openViewer(single),
      });
    }
    if (isArchive(single.name)) {
      entries.push({
        icon: 'folder-open',
        label: '解压到…',
        onClick: () => decompressArchive(single),
      });
    }
  }
  entries.push(
    {
      icon: 'download',
      label: single ? (single.is_dir ? '下载 ZIP' : '下载') : `下载（${list.length} 项）`,
      disabled: !list.length,
      onClick: () => downloadItems(list),
    },
    {
      icon: 'link',
      label: `分享（${list.length} 项）`,
      onClick: () => openShareModal(list),
    }
  );
  if (manageable.value) {
    entries.push({
      icon: 'swap',
      label: `移动（${list.length} 项）`,
      onClick: () => moveCopyItems('move', list),
    });
  }
  entries.push({
    icon: 'copy',
    label: `复制（${list.length} 项）`,
    onClick: () => moveCopyItems('copy', list),
  });
  if (manageable.value) {
    entries.push({
      icon: 'edit',
      label: '重命名',
      disabled: !single,
      onClick: () => openRename(single),
    });
    entries.push({
      icon: 'edit',
      label: `批量重命名（${list.length} 项）…`,
      disabled: list.length < 2,
      onClick: () => openBatchRename(list),
    });
    entries.push('-');
    entries.push({
      icon: 'delete',
      label: `删除（${list.length} 项）`,
      danger: true,
      onClick: () => removeItems(list),
    });
  }
  openCtxAt(e.clientX, e.clientY, entries);
}
function openBlankMenu(e) {
  const entries = [];
  if (!searchActive.value) {
    entries.push({ icon: 'folder-add', label: '新建文件夹', onClick: openMkdir });
    entries.push({ icon: 'upload', label: '上传文件', onClick: pickUpload });
    if (pasteClip.value) {
      entries.push({
        icon: 'copy',
        label: `粘贴（${pasteClip.value.items.length} 项）`,
        onClick: pasteItems,
      });
    }
    entries.push('-');
  }
  entries.push({ icon: 'refresh', label: '刷新', onClick: () => reloadCurrent(true) });
  openCtxAt(e.clientX, e.clientY, entries);
}

/* ================= 移动 / 复制（待粘贴状态） ================= */
const pasteClip = ref(null); // {op, srcSpace, items:[{name,_rel}]}
let pasting = false;
function moveCopyItems(op, list) {
  if (!list.length) return;
  const dirs = new Set(list.map((it) => parentOf(it._rel)));
  if (dirs.size > 1) {
    MessagePlugin.error('移动/复制的条目需在同一目录内');
    return;
  }
  pasteClip.value = {
    op,
    srcSpace: space.value,
    items: list.map((it) => ({ name: it.name, _rel: it._rel })),
  };
}
function cancelPaste() {
  pasteClip.value = null;
}
// 待粘贴提交：按源目录分组调 /move 或 /copy（支持跨空间）；源=目标与失败均保留待粘贴状态
async function pasteItems() {
  const clip = pasteClip.value;
  if (!clip || pasting) return;
  const dstSpace = space.value;
  const dstDir = path.value;
  const groups = {};
  clip.items.forEach((it) => {
    const d = parentOf(it._rel);
    (groups[d] = groups[d] || []).push(it.name);
  });
  const dirs = Object.keys(groups);
  if (clip.srcSpace === dstSpace && dirs.indexOf(dstDir) >= 0) {
    MessagePlugin.error('源位置与目标位置相同');
    return;
  }
  pasting = true;
  try {
    for (const d of dirs) {
      const params = {
        srcSpace: clip.srcSpace,
        srcDir: d,
        names: groups[d],
        dstSpace,
        dstDir,
      };
      if (clip.op === 'move') await moveItems(params);
      else await copyItems(params);
    }
    MessagePlugin.success(`${clip.op === 'move' ? '已移动 ' : '已复制 '}${clip.items.length} 项`);
    if (pasteClip.value === clip) pasteClip.value = null;
    sel.clear();
    clearDirSizeCache();
    reloadCurrent();
  } catch (e) {
    MessagePlugin.error(e.message || '操作失败');
  } finally {
    pasting = false;
  }
}

/* ================= 创建分享 / 分享管理 ================= */
const shareDlg = reactive({
  open: false,
  items: [],
  expireDays: 7,
  code: 'ABCD',
  busy: false,
  result: null, // {link, code, expire_at}
});
function openShareModal(list) {
  if (!list.length) return;
  if (list.length > 20) {
    MessagePlugin.error('单次最多分享 20 项');
    return;
  }
  Object.assign(shareDlg, {
    open: true,
    items: list,
    expireDays: 7,
    code: randShareCode(),
    busy: false,
    result: null,
  });
}
async function createShare() {
  shareDlg.busy = true;
  try {
    const d = await createShareApi({
      space: space.value,
      paths: shareDlg.items.map((it) => it._rel),
      password: shareDlg.code,
      expireDays: shareDlg.expireDays,
    });
    shareDlg.result = {
      link: shareLinkUrl(d.share_id),
      code: shareDlg.code,
      expire_at: d.expire_at,
    };
    if (view.value === 'shares') loadShares();
  } catch (e) {
    MessagePlugin.error(e.message || '创建分享失败');
  } finally {
    shareDlg.busy = false;
  }
}
function copyShareResult() {
  const r = shareDlg.result;
  copyText(`链接：${r.link}\n提取码：${r.code}`, '链接与提取码已复制');
}

const shareScope = ref('mine');
const shares = ref([]);
const sharesLoading = ref(false);
const sharesError = ref('');
function switchShareScope(s) {
  if (shareScope.value === s) return;
  shareScope.value = s;
  loadShares();
}
async function loadShares() {
  sharesLoading.value = true;
  sharesError.value = '';
  try {
    shares.value = (await listShares(shareScope.value)) || [];
  } catch (e) {
    sharesError.value = e.message || '加载失败';
  } finally {
    sharesLoading.value = false;
  }
}
function shareFirst(s) {
  return (s.items && s.items[0]) || '';
}
function shareFirstIsFile(s) {
  return (s.items || []).length === 1 && /\.[a-z0-9]{1,8}$/i.test(shareFirst(s));
}
function shareName(s) {
  const n = (s.items || []).length;
  const first = shareFirst(s);
  return n > 1 ? `${first} 等 ${n} 项` : first || '—';
}
function shareStateText(st) {
  return { active: '生效中', expired: '已过期', disabled: '已停用' }[st] || st || '—';
}
function shareStateCls(st) {
  return { active: 'st-ok', expired: 'st-warn', disabled: 'st-bad' }[st] || 'st-info';
}
function copyShareLink(s) {
  copyText(shareLinkUrl(s.share_id), '链接已复制');
}
async function setShareEnabled(s, on) {
  try {
    if (on) await enableShare(s.share_id);
    else await disableShare(s.share_id);
    MessagePlugin.success(on ? '已重新启用' : '已停用');
    loadShares();
  } catch (e) {
    MessagePlugin.error(e.message || '操作失败');
  }
}
async function removeShare(s) {
  try {
    await deleteShare(s.share_id);
    MessagePlugin.success('已删除');
    loadShares();
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  }
}

/* ================= 批量重命名（替换文本 / 添加文本 / 格式化；扩展名保留、实时预览、非法红标） ================= */
const BATCH_MODES = [
  ['replace', '替换文本'],
  ['add', '添加文本'],
  ['format', '格式化'],
];
const batchDlg = reactive({
  open: false,
  mode: 'replace',
  find: '',
  repl: '',
  affix: '',
  pos: 'after',
  fmt: '',
  numPos: 'after',
  start: 1,
  pad: 1,
  items: [],
  busy: false,
});
function openBatchRename(list) {
  if (!list || list.length < 2) return;
  if (list.length > 100) {
    MessagePlugin.error('批量重命名单次最多 100 项');
    return;
  }
  Object.assign(batchDlg, {
    open: true,
    mode: 'replace',
    find: '',
    repl: '',
    affix: '',
    pos: 'after',
    fmt: '',
    numPos: 'after',
    start: 1,
    pad: 1,
    items: list,
    busy: false,
  });
}
// 主名/扩展名拆分：最后一个 . 且不在首位才算扩展名；文件夹不按扩展名处理
function splitName(it) {
  if (it.is_dir) return { base: it.name, ext: '' };
  const d = it.name.lastIndexOf('.');
  return d > 0 ? { base: it.name.slice(0, d), ext: it.name.slice(d) } : { base: it.name, ext: '' };
}
function padNum(n) {
  let s = String(n);
  while (s.length < Math.max(1, Math.min(8, Number(batchDlg.pad) || 1))) s = '0' + s;
  return s;
}
function batchNewName(it, i) {
  if (batchDlg.mode === 'replace') {
    if (!batchDlg.find) return it.name;
    return it.name.split(batchDlg.find).join(batchDlg.repl);
  }
  const p = splitName(it);
  if (batchDlg.mode === 'add') {
    return batchDlg.pos === 'before'
      ? batchDlg.affix + p.base + p.ext
      : p.base + batchDlg.affix + p.ext;
  }
  const num = padNum((Number(batchDlg.start) || 0) + i);
  return (batchDlg.numPos === 'before' ? num + batchDlg.fmt : batchDlg.fmt + num) + p.ext;
}
// 校验口径同后端 validName：trim 后非空 ≤200、不含 / \ 换行、非 . / ..
function nameError(name) {
  const n = String(name || '').trim();
  if (!n) return '名称为空';
  if (n.length > 200) return '名称过长';
  if (/[/\\\r\n]/.test(n)) return '含非法字符';
  if (n === '.' || n === '..') return '非法名称';
  return '';
}
const batchPreview = computed(() => {
  const rows = batchDlg.items.map((it, i) => {
    const nn = batchNewName(it, i).trim();
    const err = nameError(nn);
    return { path: it._rel, old: it.name, name: nn, same: nn === it.name, bad: !!err, why: err };
  });
  // 批内同目录重名互标
  const seen = {};
  rows.forEach((r) => {
    if (r.bad || r.same) return;
    const key = parentOf(r.path) + '\n' + r.name;
    if (seen[key]) {
      r.bad = true;
      r.why = '批内重名';
      if (!seen[key].bad) {
        seen[key].bad = true;
        seen[key].why = '批内重名';
      }
    } else seen[key] = r;
  });
  // 与未选中兄弟项重名（浏览态可校验；搜索态非全量，交由后端报错）
  if (!searchActive.value) {
    const selMap = {};
    batchDlg.items.forEach((it) => {
      selMap[it._rel] = 1;
    });
    const siblings = {};
    items.value.forEach((sib) => {
      if (!selMap[sib._rel]) siblings[sib.name] = 1;
    });
    rows.forEach((r) => {
      if (!r.bad && !r.same && siblings[r.name]) {
        r.bad = true;
        r.why = '与现有项重名';
      }
    });
  }
  return rows;
});
const batchHasBad = computed(() => batchPreview.value.some((r) => r.bad));
async function confirmBatchRename() {
  const list = batchPreview.value;
  if (list.some((r) => r.bad)) {
    MessagePlugin.error('存在不合法的新名称，请调整');
    return;
  }
  const targets = list.filter((r) => !r.same);
  if (!targets.length) {
    MessagePlugin.error('新名称与原名称相同，无需重命名');
    return;
  }
  if (targets.length > 100) {
    MessagePlugin.error('批量重命名单次最多 100 项');
    return;
  }
  batchDlg.busy = true;
  try {
    const d = await renameBatch({
      space: space.value,
      items: targets.map((p) => ({ path: p.path, name: p.name })),
    });
    const failed = (d && d.failed) || [];
    if (failed.length) {
      MessagePlugin.warning(
        `成功 ${d.renamed} 项，失败 ${failed.length} 项：${failed[0].message || ''}`
      );
    } else {
      MessagePlugin.success(`已重命名 ${d.renamed} 项`);
    }
    batchDlg.open = false;
    sel.clear();
    clearDirSizeCache();
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '批量重命名失败');
  } finally {
    batchDlg.busy = false;
  }
}

/* ================= 压缩包预览 / 解压 ================= */
const arcDlg = reactive({
  open: false,
  item: null,
  stage: 'list', // list / pw
  knownEncrypted: false,
  pass: '',
  inner: '/',
  comment: '',
  items: [],
  loading: false,
  error: '',
});
function openArchive(it) {
  Object.assign(arcDlg, {
    open: true,
    item: it,
    stage: 'list',
    knownEncrypted: false,
    pass: '',
    inner: '/',
    comment: '',
    items: [],
    loading: false,
    error: '',
  });
  arcLoadMeta();
}
async function arcLoadMeta() {
  arcDlg.loading = true;
  try {
    const d = await archiveMeta({
      space: space.value,
      path: arcDlg.item._rel,
      archivePass: arcDlg.pass,
    });
    arcDlg.comment = d.comment || '';
    if (d.encrypted) {
      arcDlg.knownEncrypted = true;
      arcDlg.stage = 'pw';
    } else {
      arcDlg.stage = 'list';
      await arcLoad();
    }
  } catch (e) {
    arcDlg.knownEncrypted = false;
    arcDlg.stage = 'pw';
    arcDlg.error = e.message || '';
  } finally {
    arcDlg.loading = false;
  }
}
function submitArchivePass() {
  if (!arcDlg.pass) {
    arcDlg.error = '请输入压缩密码';
    return;
  }
  arcDlg.error = '';
  arcDlg.inner = '/';
  arcDlg.stage = 'list';
  arcLoad();
}
const arcCrumbs = computed(() => {
  const segs = [];
  if (!arcDlg.inner || arcDlg.inner === '/') return segs;
  let acc = '';
  arcDlg.inner
    .split('/')
    .filter(Boolean)
    .forEach((p) => {
      acc = acc ? `${acc}/${p}` : `/${p}`;
      segs.push({ name: p, rel: acc });
    });
  return segs;
});
function arcNavTo(rel) {
  arcDlg.inner = rel || '/';
  arcLoad();
}
function arcItemRel(x) {
  if (x.rel) return x.rel;
  return arcDlg.inner === '/' ? `/${x.name}` : `${arcDlg.inner}/${x.name}`;
}
async function arcLoad() {
  arcDlg.loading = true;
  arcDlg.error = '';
  try {
    const d = await archiveList({
      space: space.value,
      path: arcDlg.item._rel,
      innerPath: arcDlg.inner,
      archivePass: arcDlg.pass,
    });
    arcDlg.inner = d.inner_path || arcDlg.inner;
    arcDlg.items = sortItems((d && (d.content || d.items)) || []).map((x) => ({
      ...x,
      rel: x.rel || arcItemRel(x),
    }));
  } catch (e) {
    arcDlg.items = [];
    const msg = e.message || '加载失败';
    // 首层失败且已带密码：大概率密码错误，回密码行重输
    if (arcDlg.pass && arcDlg.inner === '/') {
      arcDlg.stage = 'pw';
      arcDlg.knownEncrypted = true;
      arcDlg.error = msg;
    } else {
      arcDlg.error = msg;
    }
  } finally {
    arcDlg.loading = false;
  }
}
async function downloadArchiveItem(x) {
  if (!x) return;
  try {
    const res = await archiveDownload({
      space: space.value,
      path: arcDlg.item._rel,
      inner: x.rel || arcItemRel(x),
      archivePass: arcDlg.pass,
    });
    await saveBlob(res, x.name);
  } catch (e) {
    MessagePlugin.error(e.message || '包内文件下载失败');
  }
}

/* ================= 目标位置选择弹窗（解压到…） ================= */
const pickDlg = reactive({
  open: false,
  title: '选择目标位置',
  confirmText: '确定',
  busyText: '处理中…',
  fixSpace: '',
  withNewDir: false,
  putIntoNewDir: true,
  onSubmit: null,
  space: 'my',
  dir: '',
  selName: '',
  dirs: [],
  loading: false,
  busy: false,
  error: '',
});
const pickPillStyle = ref({ width: '0px', transform: 'translateX(0px)' });
const pickCrumbs = computed(() => {
  const segs = [];
  if (!pickDlg.dir) return segs;
  let acc = '';
  pickDlg.dir
    .split('/')
    .filter(Boolean)
    .forEach((p) => {
      acc = acc ? `${acc}/${p}` : p;
      segs.push({ name: p, path: acc });
    });
  return segs;
});
function openPick(opts) {
  return new Promise((resolve) => {
    Object.assign(pickDlg, {
      open: true,
      title: opts.title || '选择目标位置',
      confirmText: opts.confirmText || '确定',
      busyText: opts.busyText || '处理中…',
      fixSpace: opts.fixSpace || '',
      withNewDir: !!opts.withNewDir,
      putIntoNewDir: true,
      onSubmit: opts.onSubmit || null,
      space: opts.fixSpace || space.value,
      dir: '',
      selName: '',
      dirs: [],
      loading: false,
      busy: false,
      error: '',
    });
    pickDlg._resolve = resolve;
    nextTick(() => {
      movePickPill(true);
      pickLoadDirs();
    });
  });
}
// 目标选择弹窗空间分段白 pill 落位（弹层内 DOM 需挂载后测量）
function movePickPill(instant) {
  const seg = document.querySelector('.nd-root .mask .seg[data-pick-seg]');
  moveSegPill(seg, pickPillStyle, instant);
}
function closePick(v) {
  pickDlg.open = false;
  const r = pickDlg._resolve;
  pickDlg._resolve = null;
  if (r) r(v);
}
function pickTargetDir() {
  return pickDlg.selName ? joinRel(pickDlg.dir, pickDlg.selName) : pickDlg.dir;
}
function switchPickSpace(k) {
  if (pickDlg.space === k) return;
  pickDlg.space = k;
  pickDlg.dir = '';
  pickDlg.selName = '';
  nextTick(() => movePickPill(false));
  pickLoadDirs();
}
function pickNavTo(p) {
  pickDlg.dir = p;
  pickDlg.selName = '';
  pickLoadDirs();
}
function togglePickSel(name) {
  pickDlg.selName = pickDlg.selName === name ? '' : name;
}
function pickEnter(name) {
  pickDlg.dir = joinRel(pickDlg.dir, name);
  pickDlg.selName = '';
  pickLoadDirs();
}
async function pickLoadDirs() {
  pickDlg.loading = true;
  pickDlg.error = '';
  try {
    const d = await listDir({ space: pickDlg.space, path: pickDlg.dir });
    let dirs = (d.items || []).filter((x) => x.is_dir);
    // 同空间下隐藏源条目自身，防止把文件夹移/复制进自己
    if (pickDlg.space === space.value && pasteClip.value) {
      const exclude = new Set(pasteClip.value.items.map((it) => it._rel));
      dirs = dirs.filter((x) => !exclude.has(joinRel(pickDlg.dir, x.name)));
    }
    dirs.sort((a, b) => String(a.name).localeCompare(String(b.name), 'zh'));
    pickDlg.dirs = dirs;
    pickDlg.selName = '';
  } catch (e) {
    pickDlg.dirs = [];
    pickDlg.error = e.message || '加载失败';
  } finally {
    pickDlg.loading = false;
  }
}
function pickMkdir() {
  openPrompt({
    title: '新建文件夹',
    placeholder: `在「${pickDlg.dir || '根目录'}」下新建`,
    okText: '创建',
  }).then(async (name) => {
    if (!name) return;
    try {
      await mkdir({ space: pickDlg.space, path: pickDlg.dir, name });
      MessagePlugin.success('已创建');
      pickLoadDirs();
    } catch (e) {
      MessagePlugin.error(e.message || '创建失败');
    }
  });
}
async function confirmPick() {
  const target = {
    space: pickDlg.space,
    dir: pickTargetDir(),
    putIntoNewDir: pickDlg.withNewDir ? pickDlg.putIntoNewDir : undefined,
  };
  if (!pickDlg.onSubmit) {
    closePick(target);
    return;
  }
  pickDlg.busy = true;
  try {
    await pickDlg.onSubmit(target);
    closePick(target);
  } catch (e) {
    MessagePlugin.error(e.message || '操作失败');
  } finally {
    pickDlg.busy = false;
  }
}

// 解压：meta 判加密（加密先收压缩密码），再走目标选择弹窗（锁定当前空间）
async function decompressArchive(it) {
  let pass = '';
  try {
    const meta = await archiveMeta({ space: space.value, path: it._rel });
    if (meta.encrypted) {
      pass = await openPrompt({
        title: `解压「${it.name}」`,
        placeholder: '该压缩包已加密，请输入压缩密码',
        okText: '确定',
        password: true,
      });
      if (pass === null || pass === undefined) return;
    }
  } catch (e) {
    MessagePlugin.error(e.message || '读取压缩包失败');
    return;
  }
  openPick({
    title: '解压到…',
    confirmText: '解压',
    busyText: '解压中…',
    fixSpace: space.value, // 解压接口无 dst_space，锁定当前空间
    withNewDir: true,
    onSubmit: async (target) => {
      await archiveDecompress({
        srcSpace: space.value,
        srcDir: path.value,
        names: [it.name],
        dstSpace: space.value,
        dstDir: target.dir,
        putIntoNewDir: target.putIntoNewDir !== false,
        archivePass: pass || '',
      });
      MessagePlugin.success('已解压');
      clearDirSizeCache();
      reloadCurrent();
    },
  });
}

/* ================= 全局事件（右键菜单外点关闭 / Esc） ================= */
function onDocDown(e) {
  if (ctx.open && !(e.target && e.target.closest && e.target.closest('.ctxmenu'))) closeCtx();
}
function onGlobalKey(e) {
  if (e.key !== 'Escape') return;
  if (vwr.open) return; // 查看器自有 Esc 处理
  if (ctx.open) {
    closeCtx();
    return;
  }
  if (pasteClip.value) {
    cancelPaste();
    return;
  }
  if (promptDlg.open) closePrompt(null);
  if (batchDlg.open) batchDlg.open = false;
  else if (arcDlg.open) arcDlg.open = false;
  else if (ooDlg.open) ooDlg.open = false;
  else if (mediaDlg.open) closeMedia();
  else if (delDlg.open) delDlg.open = false;
  else if (shareDlg.open) shareDlg.open = false;
}

/* ================= 初始化 ================= */
onMounted(async () => {
  document.addEventListener('mousedown', onDocDown, true);
  window.addEventListener('keydown', onGlobalKey, true);
  try {
    const d = await listSpaces();
    (d.spaces || []).forEach((sp) => {
      spaces[sp.key] = sp;
    });
    if (typeof d.max_upload_mb === 'number') maxMb.value = d.max_upload_mb;
    // 恢复上次访问位置（按用户记忆：空间 + 目录，进入网盘不重置回根目录；
    // 记录经 lastloc 合法性校验，空间已不可用时回落默认）
    const last = loadLastLoc(me);
    if (last && spaces[last.space]) {
      space.value = last.space;
      path.value = last.path;
    }
    if (spaces[space.value] === undefined) {
      space.value = spaceKeys.value[0] || 'my';
    }
  } catch (e) {
    MessagePlugin.error(e.message || '空间列表加载失败');
  }
  nextTick(() => moveSegPill(spaceSegEl.value, segPillStyle, true));
  await loadList();
  if (loadError.value && path.value) {
    // 记忆目录已失效（删除/改名）：回退空间根再加载一次
    path.value = '';
    await loadList();
  }
  saveLastLoc(me, space.value, path.value);
});

onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown, true);
  window.removeEventListener('keydown', onGlobalKey, true);
  window.removeEventListener('keydown', onVwrKey, true);
  window.removeEventListener('resize', onVwrResize);
});
</script>

<style scoped>
/* 网盘页样式：对照 server/public/netdisk.html 移植（旧 theme.css 变量名在 .nd-root 映射到
   web styles.css 的「政企蓝白」--tk-* token，色值随全局 token 走） */
.nd-root {
  --paper: var(--tk-bg);
  --card: #fff;
  --navy: var(--tk-ink);
  --orange: var(--tk-brand);
  --orange-soft: var(--tk-blue-soft);
  --red: var(--tk-red);
  --green: var(--tk-green);
  --yellow: var(--tk-yellow);
  --line: var(--tk-line);
  --hair: rgba(29, 33, 41, 0.06);
  --sub: var(--tk-sub);
  --ph: var(--tk-ph);
  --dash: var(--tk-dash);
  --fill: var(--tk-fill);
  --shadow-card: var(--tk-shadow-card);
  --shadow-hover: var(--tk-shadow-hover);
  --ease: var(--tk-ease);
  --radius: 12px;
  --radius-sm: 8px;
  --overlay: rgba(29, 33, 41, 0.45);
  --on-brand: #fff;
  --inverse-text: #fff;
  --warn-soft: var(--tk-yellow-soft);
  --warn-text: #d25f00;
  --ok-soft: var(--tk-green-soft);
  --ok-text: #0b8a37;
  --danger-soft: var(--tk-red-soft);
  --brand-glow: rgba(14, 61, 168, 0.12);
  --skeleton: rgba(29, 33, 41, 0.05);

  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.nd-root :deep(*) {
  box-sizing: border-box;
}
.nd-wrap {
  flex: 1;
  min-height: 0;
  width: 100%;
  max-width: 1560px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
}
.nd-view {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* ===== 页头 / 页签 ===== */
.nd-head h2 {
  margin: 0;
  font-size: 22px;
  font-weight: 700;
  letter-spacing: 1px;
}
.nd-head p {
  margin: 6px 0 0;
  font-size: 12.5px;
  color: var(--ph);
  letter-spacing: 0.5px;
}
.nd-tabs {
  display: flex;
  gap: 4px;
  margin-top: 16px;
  border-bottom: 1px solid var(--line);
}
.nd-tabs button {
  position: relative;
  padding: 9px 14px;
  border: none;
  background: transparent;
  cursor: pointer;
  font-size: 13.5px;
  color: var(--sub);
  letter-spacing: 0.5px;
  transition: color 0.15s;
}
.nd-tabs button:hover {
  color: var(--navy);
}
.nd-tabs button.on {
  color: var(--orange);
  font-weight: 600;
}
.nd-tabs button.on::after {
  content: '';
  position: absolute;
  left: 14px;
  right: 14px;
  bottom: -1px;
  height: 2px;
  background: var(--orange);
  border-radius: 2px;
}
.nd-view.view-in {
  animation: viewIn 0.3s var(--ease);
}
@keyframes viewIn {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
}

/* ===== 工具条：空间分段（灰槽 + 滑动白 pill）+ 选中操作区 + 右侧按钮 ===== */
.nd-toolbar {
  margin-top: 16px;
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: nowrap;
  overflow-x: auto;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.nd-toolbar::-webkit-scrollbar {
  height: 4px;
}
.nd-toolbar::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.nd-toolbar::-webkit-scrollbar-track {
  background: transparent;
}
.nd-toolbar .seg {
  flex: none;
}
.seg {
  display: inline-flex;
  position: relative;
  background: var(--line);
  border-radius: 8px;
  padding: 2px;
}
.seg-pill {
  position: absolute;
  top: 2px;
  bottom: 2px;
  left: 0;
  width: 0;
  pointer-events: none;
  background: var(--card);
  border-radius: 6px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  transition:
    transform 0.25s var(--ease),
    width 0.25s var(--ease);
}
.seg button {
  position: relative;
  z-index: 1;
  border: none;
  background: transparent;
  height: 30px;
  padding: 0 16px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--sub);
  letter-spacing: 1px;
  cursor: pointer;
  transition: color 0.15s;
}
.seg button.on {
  color: var(--orange);
}
/* 选中操作区：隐藏时零宽不占位，显示时淡入 */
.selbar {
  flex: none;
  white-space: nowrap;
  overflow: hidden;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  width: 0;
  padding-left: 0;
  margin-left: 0;
  border-left: 0 solid transparent;
  visibility: hidden;
  opacity: 0;
  transition:
    opacity 0.18s var(--ease),
    visibility 0s linear 0.18s;
}
.selbar.show {
  width: auto;
  padding-left: 12px;
  margin-left: 2px;
  border-left: 1px solid var(--line);
  visibility: visible;
  opacity: 1;
  transition: opacity 0.18s var(--ease);
}
.selbar .btn {
  flex: none;
}
.selbar .btn-del {
  color: var(--red);
}
.selbar .btn-del:hover {
  border-color: var(--red);
  color: var(--red);
}
.tb-right {
  flex: none;
  margin-left: auto;
  display: flex;
  gap: 10px;
  position: sticky;
  right: 0;
  background: var(--paper);
  padding-left: 12px;
  transition:
    opacity 0.18s var(--ease),
    visibility 0s linear 0.18s;
}
.tb-right.gone {
  width: 0;
  padding-left: 0;
  overflow: hidden;
  visibility: hidden;
  opacity: 0;
  transition:
    opacity 0.18s var(--ease),
    visibility 0s linear 0.18s;
}

/* ===== 面包屑 + 搜索 ===== */
.pathbar {
  margin-top: 14px;
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}
.crumb {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  min-width: 0;
  flex-wrap: wrap;
}
.crumb a {
  color: var(--orange);
  font-weight: 600;
  cursor: pointer;
  text-decoration: none;
}
.crumb a:hover {
  filter: brightness(0.85);
}
.crumb em {
  font-style: normal;
  color: var(--ph);
}
.crumb b {
  font-weight: 600;
  word-break: break-all;
}
.searchw {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 7px;
  height: 34px;
  width: 280px;
  padding: 0 12px;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 8px;
  color: var(--sub);
  transition:
    border-color 0.15s var(--ease),
    box-shadow 0.15s var(--ease);
}
.searchw:hover,
.searchw:focus-within {
  border-color: var(--orange);
}
.searchw:focus-within {
  box-shadow: 0 0 0 3px var(--brand-glow);
}
.searchw .ic {
  flex: none;
  display: inline-flex;
}
.searchw input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  font-size: 12.5px;
  color: var(--navy);
  font-family: inherit;
}
.searchw input::placeholder {
  color: var(--sub);
}
.sw-x {
  flex: none;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--dash);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
}
.sw-x:hover {
  background: var(--ph);
}
/* 待粘贴提示条：浅蓝底 + 左色条直角（提示信息块口径） */
.paste-bar {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  background: var(--orange-soft);
  border-left: 3px solid var(--orange);
  font-size: 12.5px;
  color: var(--navy);
}
.paste-bar .lk {
  margin-left: auto;
  flex: none;
}

/* ===== 图标网格 ===== */
.gridwrap {
  position: relative;
  margin-top: 8px;
  -webkit-user-select: none;
  user-select: none;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
}
.gridwrap::-webkit-scrollbar {
  width: 5px;
}
.gridwrap::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.gridwrap::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.gridwrap::-webkit-scrollbar-track {
  background: transparent;
}
.gridwrap.dragover {
  outline: 2px dashed var(--orange);
  outline-offset: -2px;
  border-radius: var(--radius);
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(118px, 1fr));
  gap: 4px;
  padding: 6px 0 12px;
  min-height: 100%;
  align-content: start;
  transition: opacity 0.14s ease;
}
.grid.g-out {
  opacity: 0;
}
.grid > .empty,
.grid > .nd-state {
  grid-column: 1 / -1;
}
.g-cell {
  position: relative;
  border-radius: 8px;
  padding: 14px 8px 10px;
  display: flex;
  flex-direction: column;
  align-items: center;
  cursor: default;
  transition:
    background 0.15s var(--ease),
    box-shadow 0.15s var(--ease);
}
.g-cell.cell-in {
  animation: cellIn 0.42s var(--ease) both;
}
@keyframes cellIn {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
}
.g-cell:hover {
  background: rgba(14, 61, 168, 0.06);
  box-shadow: inset 0 0 0 1px rgba(14, 61, 168, 0.18);
}
.g-cell.sel {
  background: rgba(14, 61, 168, 0.12);
  box-shadow: inset 0 0 0 1px rgba(14, 61, 168, 0.45);
}
.g-cell.sel:hover {
  background: rgba(14, 61, 168, 0.16);
  box-shadow: inset 0 0 0 1px rgba(14, 61, 168, 0.45);
}
.g-icon {
  height: 64px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.g-thumb {
  width: 96px;
  height: 64px;
  border-radius: 8px;
  object-fit: cover;
  background: var(--fill);
  display: block;
}
/* 真实文件类型图标（无底块直落）；img 404 由 @error 露出 .ft-fb 回退 */
.ft-ic {
  position: relative;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.ft-ic .ft-img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: contain;
}
.ft-ic .ft-fb {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
}
.ft-ic .ft-fb[hidden] {
  display: none;
}
.ft-fb-lb {
  width: 100%;
  height: 100%;
  border-radius: 22%;
  background: var(--orange-soft);
  color: var(--orange);
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.ft-fb .ic {
  color: var(--orange);
  display: inline-flex;
}
.g-name {
  margin-top: 8px;
  width: 100%;
  font-size: 12px;
  line-height: 1.45;
  text-align: center;
  word-break: break-all;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.g-cell.sel .g-name {
  font-weight: 600;
}
.g-sub {
  margin-top: 4px;
  max-width: 100%;
  font-size: 10.5px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 上传中格：左上 × 取消 / 右上状态标签 / 名称下细进度条 */
.g-x {
  position: absolute;
  top: 6px;
  left: 6px;
  z-index: 2;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: var(--card);
  box-shadow: var(--shadow-card);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ph);
}
.g-x:hover {
  color: var(--red);
}
.g-tag {
  position: absolute;
  top: 6px;
  right: 6px;
}
.g-prog {
  margin-top: 8px;
  width: 100%;
  height: 4px;
  border-radius: 2px;
  background: var(--line);
  overflow: hidden;
}
.g-prog i {
  display: block;
  height: 100%;
  width: 0;
  background: var(--orange);
  transition: width 0.25s var(--ease);
}
.g-up {
  transition: opacity 0.25s ease;
}
.g-up.gone {
  opacity: 0;
}
.g-up.err .g-prog i {
  background: var(--red);
}
/* 骨架格 */
.sk-cell {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 14px 8px 10px;
}
.sk-ic {
  width: 56px;
  height: 56px;
}
.sk-ln {
  width: 76%;
  height: 12px;
}

/* ===== 右键菜单 ===== */
.ctxmenu {
  position: fixed;
  z-index: 1200;
  width: 190px;
  background: var(--card);
  border-radius: 10px;
  box-shadow: var(--shadow-hover);
  border: 1px solid var(--line);
  padding: 5px;
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transform: scale(0.98);
  transition:
    opacity 0.12s ease,
    transform 0.12s ease,
    visibility 0s linear 0.12s;
}
.ctxmenu.open {
  opacity: 1;
  visibility: visible;
  pointer-events: auto;
  transform: none;
  transition: none;
  animation: popIn 0.16s var(--ease) both;
}
@keyframes popIn {
  from {
    opacity: 0;
    transform: scale(0.97);
  }
}
.ctx {
  display: flex;
  align-items: center;
  gap: 9px;
  height: 34px;
  padding: 0 10px;
  border-radius: 6px;
  font-size: 12.5px;
  color: var(--navy);
  cursor: pointer;
}
.ctx:hover {
  background: var(--fill);
}
.ctx .ic {
  display: inline-flex;
  color: var(--sub);
}
.ctx.dis {
  color: var(--dash);
  cursor: default;
}
.ctx.dis .ic {
  color: var(--dash);
}
.ctx.dis:hover {
  background: transparent;
}
.ctx.danger {
  color: var(--red);
}
.ctx.danger .ic {
  color: var(--red);
}
.ctx-sep {
  height: 1px;
  background: var(--line);
  margin: 5px 8px;
}

/* ===== 底部状态栏 ===== */
.statusbar {
  flex: none;
  border-top: 1px solid var(--line);
  padding: 10px 2px 0;
  display: flex;
  gap: 20px;
  font-size: 11.5px;
  color: var(--ph);
  flex-wrap: wrap;
}
.statusbar b {
  color: var(--sub);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* ===== 右侧常驻详情面板（无白卡，文字直落页面背景） ===== */
.nd-main {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 16px;
}
.nd-files-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.nd-info {
  flex: none;
  width: 280px;
  min-height: 0;
  margin-top: 8px;
  padding: 16px 4px 16px 16px;
  overflow-y: auto;
  scrollbar-width: thin;
  scrollbar-color: rgba(29, 33, 41, 0.2) transparent;
  font-size: 12.5px;
  color: var(--sub);
}
.nd-info::-webkit-scrollbar {
  width: 5px;
}
.nd-info::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.ni-head {
  padding-bottom: 12px;
  border-bottom: 1px solid var(--line);
}
.ni-name {
  margin-top: 10px;
  text-align: center;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--navy);
  word-break: break-all;
}
.ni-fields {
  margin: 0;
  padding-top: 4px;
}
.ni-row {
  display: flex;
  gap: 10px;
  padding: 8px 0;
  border-bottom: 1px solid var(--line);
}
.ni-row:last-child {
  border-bottom: none;
}
.ni-row dt {
  flex: none;
  width: 56px;
  color: var(--ph);
}
.ni-row dd {
  margin: 0;
  flex: 1;
  min-width: 0;
  color: var(--navy);
  word-break: break-all;
  font-variant-numeric: tabular-nums;
}
.ni-multi,
.ni-empty {
  padding: 44px 0;
  text-align: center;
}
.ni-empty {
  font-size: 12px;
  color: var(--ph);
}
.ni-multi b {
  display: block;
  font-size: 15px;
  color: var(--navy);
}
.ni-multi span {
  display: block;
  margin-top: 8px;
  font-size: 12px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
@media (max-width: 1024px) {
  .nd-info {
    display: none;
  }
}
.lk {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--orange);
  cursor: pointer;
}
.lk.gray {
  color: var(--sub);
}
.lk.red {
  color: var(--red);
}
.lk.dis {
  color: var(--dash);
  cursor: default;
}
.lk:not(.dis):hover {
  filter: brightness(0.85);
}

/* ===== 通用按钮 / 输入 / 标签 / 空态 / 加载（旧 theme.css 同口径） ===== */
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
  cursor: pointer;
  font-family: inherit;
  transition:
    border-color 0.15s var(--ease),
    color 0.15s,
    filter 0.15s,
    background 0.15s;
}
.btn .ic {
  display: inline-flex;
}
.btn:hover {
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
.btn-danger {
  color: var(--on-brand);
  border-color: transparent;
}
.btn-primary {
  background: var(--orange);
}
.btn-primary:hover {
  color: var(--on-brand);
  filter: brightness(0.92);
}
.btn-danger {
  background: var(--red);
}
.btn-danger:hover {
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
.tag i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}
.st-ok {
  background: var(--ok-soft);
  color: var(--ok-text);
}
.st-warn {
  background: var(--warn-soft);
  color: var(--warn-text);
}
.st-bad {
  background: var(--danger-soft);
  color: var(--red);
}
.st-info {
  background: var(--orange-soft);
  color: var(--orange);
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 20px;
  color: var(--ph);
}
.empty .ic {
  color: var(--dash);
  display: inline-flex;
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

/* ===== 通用弹层（Shade.modal 配套口径） ===== */
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
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transition:
    opacity 0.3s,
    visibility 0s linear 0.3s;
}
.mask.open {
  opacity: 1;
  visibility: visible;
  pointer-events: auto;
  transition: opacity 0.3s;
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
  transform: translateY(16px) scale(0.98);
  transition: transform 0.35s var(--ease);
}
.mask.open .modal {
  transform: none;
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
.m-scroll::-webkit-scrollbar {
  width: 5px;
}
.m-scroll::-webkit-scrollbar-thumb {
  background: rgba(29, 33, 41, 0.16);
  border-radius: 8px;
}
.m-scroll::-webkit-scrollbar-thumb:hover {
  background: rgba(29, 33, 41, 0.34);
}
.m-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.m-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.m-title {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  background: var(--navy);
  color: var(--inverse-text);
}
.m-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}
.lb-media {
  display: block;
  margin: 0 auto;
  max-width: 100%;
  max-height: 68vh;
  border-radius: 8px;
  background: var(--fill);
}
.m-note {
  margin-top: 14px;
  background: var(--orange-soft);
  border-left: 3px solid var(--orange);
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--sub);
}

/* ===== 创建分享模态 ===== */
.m-files {
  background: var(--fill);
  border-radius: 8px;
  padding: 10px 12px;
  max-height: 180px;
  overflow-y: auto;
}
.mf-rw {
  display: flex;
  align-items: center;
  gap: 9px;
  font-size: 12.5px;
  font-weight: 600;
}
.mf-rw + .mf-rw {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px dashed var(--line);
}
.mf-nm {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mf-sz {
  margin-left: auto;
  flex: none;
  font-weight: 400;
  color: var(--ph);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.kv {
  margin-top: 14px;
}
.kv > label {
  display: block;
  font-size: 11.5px;
  color: var(--ph);
  margin-bottom: 8px;
  letter-spacing: 0.5px;
}
.radios {
  display: flex;
  gap: 16px;
}
.rd {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  cursor: pointer;
}
.rd i {
  width: 15px;
  height: 15px;
  border-radius: 50%;
  border: 1.5px solid var(--dash);
  position: relative;
}
.rd.on {
  font-weight: 600;
}
.rd.on i {
  border-color: var(--orange);
}
.rd.on i::after {
  content: '';
  position: absolute;
  inset: 3px;
  border-radius: 50%;
  background: var(--orange);
}
.codebox {
  display: flex;
  align-items: center;
  gap: 10px;
}
.cd {
  width: 150px;
  height: 36px;
  border-radius: 8px;
  background: var(--fill);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 5px;
  font-variant-numeric: tabular-nums;
}
.rf {
  width: 36px;
  height: 36px;
  border-radius: 8px;
  background: var(--fill);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--sub);
}
.rf:hover {
  color: var(--orange);
}
.rs-link {
  padding: 9px 12px;
  background: var(--fill);
  border-radius: 8px;
  font-size: 12.5px;
  color: var(--orange);
  word-break: break-all;
  font-weight: 600;
}

/* ===== 目标位置选择弹窗 ===== */
.pk-crumb {
  margin-top: 12px;
}
.pk-list {
  margin-top: 10px;
  border: 1px solid var(--line);
  border-radius: 8px;
  min-height: 180px;
  max-height: 300px;
  overflow-y: auto;
}
.pk-row {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 8px 12px;
  font-size: 12.5px;
  cursor: pointer;
}
.pk-row:hover {
  background: var(--fill);
}
.pk-row.on {
  background: var(--orange-soft);
  font-weight: 600;
}
.pk-nm {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pk-empty {
  padding: 36px 20px;
}
.pk-mkdir {
  margin-right: auto;
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.ck-row {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-top: 12px;
  font-size: 12.5px;
  color: var(--sub);
  cursor: pointer;
  user-select: none;
}
.ck-row input {
  width: 14px;
  height: 14px;
  accent-color: var(--orange);
  cursor: pointer;
}

/* ===== 批量重命名弹窗 ===== */
.br-form {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 12px;
}
.br-form .input {
  flex: 1;
  min-width: 120px;
}
.br-form .br-num {
  flex: 0 0 84px;
  min-width: 0;
}
.br-form .br-label {
  flex: none;
  font-size: 12.5px;
  color: var(--sub);
}
.br-list {
  margin-top: 12px;
  max-height: 260px;
  overflow-y: auto;
  border: 1px solid var(--line);
  border-radius: 8px;
}
.br-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 12px;
  font-size: 12.5px;
  border-top: 1px solid var(--line);
}
.br-row:first-child {
  border-top: none;
}
.br-row .o,
.br-row .n {
  min-width: 0;
  word-break: break-all;
}
.br-row .o {
  color: var(--sub);
}
.br-row .a {
  flex: none;
  color: var(--ph);
}
.br-row .n {
  font-weight: 600;
}
.br-row.same .n {
  color: var(--ph);
  font-weight: 400;
}
.br-row.bad .n {
  color: var(--red);
}
.br-row .why {
  flex: none;
  margin-left: auto;
  font-size: 11.5px;
  color: var(--red);
}
.br-hint {
  margin-right: auto;
  font-size: 12px;
  color: var(--sub);
}
.br-hint.err {
  color: var(--red);
}

/* ===== 压缩包预览弹窗 ===== */
.ar-cmt {
  margin: -6px 0 10px;
  font-size: 11.5px;
  color: var(--ph);
  word-break: break-all;
}
.ar-crumb {
  margin-bottom: 10px;
}
.ar-list {
  border: 1px solid var(--line);
  border-radius: 8px;
  min-height: 160px;
  max-height: 380px;
  overflow-y: auto;
}
.ar-row {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 8px 12px;
  cursor: pointer;
}
.ar-row + .ar-row {
  border-top: 1px solid var(--fill);
}
.ar-row:hover {
  background: var(--fill);
}
.ar-mid {
  flex: 1;
  min-width: 0;
}
.ar-nm {
  display: block;
  font-size: 12.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ar-sub {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
.ar-pw {
  display: flex;
  gap: 10px;
  margin-top: 12px;
}
.ar-pw .input {
  flex: 1;
  min-width: 0;
  height: 40px;
}
.ar-pw .btn {
  height: 40px;
  flex: none;
}
.ar-err {
  margin-top: 10px;
  font-size: 12px;
  color: var(--red);
  letter-spacing: 0.5px;
}
.ar-empty {
  padding: 36px 20px;
}

/* ===== 分享管理（表格 + 状态小胶囊） ===== */
.notice {
  margin-top: 14px;
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--orange-soft);
  border-left: 3px solid var(--orange);
  padding: 9px 12px;
  font-size: 12.5px;
  color: var(--sub);
}
.notice .ic {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--orange);
  display: inline-flex;
}
.notice .seg {
  margin-left: auto;
}
.tbl-card {
  margin-top: 14px;
  overflow: hidden;
}
.nd-view .tbl-card {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  margin-bottom: 2px;
}
.rtable {
  width: 100%;
  border-collapse: collapse;
}
.rtable th {
  text-align: left;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--ph);
  letter-spacing: 0.5px;
  padding: 11px 16px;
  border-bottom: 1px solid var(--line);
  white-space: nowrap;
}
.rtable td {
  font-size: 13px;
  padding: 10px 16px;
  border-top: 1px solid var(--fill);
  vertical-align: middle;
}
.rtable tbody tr:hover {
  background: var(--fill);
}
.rtable tbody tr.row-in {
  animation: cellIn 0.42s var(--ease) both;
}
.nm-cell {
  display: flex;
  align-items: center;
  gap: 11px;
  min-width: 0;
}
.nm-wrap {
  min-width: 0;
}
.nm {
  display: block;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 420px;
}
.nm-sub {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--ph);
}
.num {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.t2 {
  color: var(--sub);
  white-space: nowrap;
}
.ops {
  display: flex;
  gap: 14px;
  white-space: nowrap;
}
.code {
  font-weight: 700;
  letter-spacing: 2px;
  font-variant-numeric: tabular-nums;
}

/* ===== 上传任务浮动面板 ===== */
.upanel {
  position: fixed;
  right: 24px;
  bottom: 24px;
  z-index: 300;
  width: 320px;
  background: var(--card);
  border-radius: var(--radius);
  box-shadow: 0 12px 32px rgba(29, 33, 41, 0.16);
  overflow: hidden;
  animation: fadeUp 0.3s var(--ease) both;
  max-height: 60vh;
  display: flex;
  flex-direction: column;
}
@keyframes fadeUp {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
}
.up-head {
  display: flex;
  align-items: center;
  padding: 11px 14px;
  border-bottom: 1px solid var(--line);
  flex: none;
}
.up-head b {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0.5px;
}
.up-head span {
  margin-left: auto;
  font-size: 11px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
.up-list {
  overflow-y: auto;
}
.up-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
}
.up-row + .up-row {
  border-top: 1px solid var(--fill);
}
.up-main {
  flex: 1;
  min-width: 0;
}
.up-name {
  display: block;
  font-size: 12px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.uprog {
  margin-top: 5px;
  height: 4px;
  border-radius: 2px;
  background: var(--line);
  overflow: hidden;
}
.uprog i {
  display: block;
  height: 100%;
  width: 0;
  background: var(--orange);
  transition: width 0.25s var(--ease);
}
.up-row.err .uprog i {
  background: var(--red);
}
.up-sub {
  display: block;
  margin-top: 3px;
  font-size: 10.5px;
  color: var(--ph);
  font-variant-numeric: tabular-nums;
}
.up-row.err .up-sub {
  color: var(--red);
}
.up-ops {
  flex: none;
  display: flex;
  align-items: center;
  gap: 2px;
}
.up-lk {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--orange);
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 6px;
}
.up-lk:hover {
  background: var(--orange-soft);
}
.up-ic {
  width: 26px;
  height: 26px;
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ph);
  cursor: pointer;
  border-radius: 6px;
}
.up-ic:hover {
  color: var(--red);
  background: var(--fill);
}

/* 网格内错误/空态块 */
.nd-state {
  padding: 40px 26px;
  text-align: center;
  font-size: 13px;
  color: var(--ph);
}
.nd-state .btn {
  margin-top: 14px;
}
.nd-ehint {
  font-size: 11.5px;
}
.nd-state .ic {
  display: inline-flex;
}

/* ===== 图片全屏查看器（深色底为查看器惯例） ===== */
.vwr {
  position: fixed;
  inset: 0;
  z-index: 2000;
  background: rgba(0, 0, 0, 0.88);
  display: flex;
  flex-direction: column;
  animation: vwrIn 0.18s ease both;
}
@keyframes vwrIn {
  from {
    opacity: 0;
  }
}
.vwr-top {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  color: #fff;
}
.vwr-name {
  font-size: 13.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vwr-idx {
  flex: none;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.6);
  font-variant-numeric: tabular-nums;
}
.vwr-x {
  margin-left: auto;
  flex: none;
  width: 34px;
  height: 34px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: rgba(255, 255, 255, 0.8);
  cursor: pointer;
  transition: background 0.15s;
}
.vwr-x:hover {
  background: rgba(255, 255, 255, 0.12);
  color: #fff;
}
.vwr-stage {
  flex: 1;
  min-height: 0;
  position: relative;
  overflow: hidden;
  margin: 0 4vw 4vh;
}
.vwr-img {
  position: absolute;
  left: 50%;
  top: 50%;
  transform-origin: center;
  user-select: none;
  cursor: zoom-in;
  transition: transform 0.15s var(--ease);
}
.vwr-img.free {
  cursor: grab;
}
.vwr-img.free.dragging {
  cursor: grabbing;
  transition: none;
}
.vwr-bar {
  flex: none;
  display: flex;
  justify-content: center;
  gap: 10px;
  padding: 4px 0 18px;
}
.vwr-bar button {
  height: 36px;
  min-width: 36px;
  padding: 0 12px;
  border-radius: 8px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  background: rgba(255, 255, 255, 0.08);
  color: #fff;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s;
}
.vwr-bar button:hover {
  background: rgba(255, 255, 255, 0.18);
}
.vwr-bar button[disabled] {
  opacity: 0.4;
  cursor: default;
}

/* ===== 框选 / 拖放落点（对照旧 netdisk.html 同口径） ===== */
/* 框选/拖拽手势期间：全页禁文字选中（手势结束移除，含 Safari 需 -webkit- 前缀） */
:global(body.nd-gesture),
:global(body.nd-gesture *) {
  -webkit-user-select: none !important;
  user-select: none !important;
}
/* 拖动框选矩形（自空白处拖出，命中即选；出现/消失轻微 opacity 过渡） */
.rubber {
  position: absolute;
  z-index: 20;
  pointer-events: none;
  border-radius: 2px;
  border: 1px solid rgba(14, 61, 168, 0.55);
  background: rgba(14, 61, 168, 0.1);
  animation: rubberIn 0.15s ease both;
  transition: opacity 0.12s ease;
}
.rubber.out {
  opacity: 0;
}
@keyframes rubberIn {
  from {
    opacity: 0;
  }
}
/* 拖放落点：拖入文件夹格子时高亮（drop-target），提示将上传进该文件夹 */
.g-cell.drop-target {
  background: rgba(14, 61, 168, 0.16);
  box-shadow: inset 0 0 0 2px rgba(14, 61, 168, 0.55);
}
/* 面包屑拖拽落点高亮（内拖移动至此目录） */
.crumb a.drop-hover {
  background: var(--brand-glow);
  box-shadow: inset 0 0 0 1px rgba(14, 61, 168, 0.45);
  border-radius: 4px;
  filter: none;
}

/* 系统深色：仅切换语义变量（值取 server/public/assets/theme.css 深色 token，不简单反色）；
   浅色结构与圆角不变；选中/拖拽等品牌色带换深色品牌 rgba(120,166,255,*) */
@media (prefers-color-scheme: dark) {
  .nd-root {
    --paper: #14171d;
    --card: #1d222b;
    --fill: #252b35;
    --navy: #e5e6eb;
    --sub: #a9b1bd;
    --ph: #8a94a3;
    --dash: #626d7d;
    --line: #343c49;
    --hair: rgba(255, 255, 255, 0.09);
    --orange: #78a6ff;
    --orange-soft: #1b2d4d;
    --on-brand: #101820;
    --inverse-text: #1d222b;
    --red: #ff7a73;
    --green: #4fd37e;
    --yellow: #f5a94a;
    --warn-soft: #382b16;
    --warn-text: #f2b56d;
    --ok-soft: #153322;
    --ok-text: #82d89b;
    --danger-soft: #3a1d1d;
    --overlay: rgba(0, 0, 0, 0.62);
    --skeleton: rgba(255, 255, 255, 0.08);
    --brand-glow: rgba(120, 166, 255, 0.18);
    --shadow-card: 0 2px 8px rgba(0, 0, 0, 0.24);
    --shadow-hover: 0 8px 20px rgba(0, 0, 0, 0.32);
  }
  .g-cell:hover {
    background: rgba(120, 166, 255, 0.1);
    box-shadow: inset 0 0 0 1px rgba(120, 166, 255, 0.22);
  }
  .g-cell.sel {
    background: rgba(120, 166, 255, 0.16);
    box-shadow: inset 0 0 0 1px rgba(120, 166, 255, 0.5);
  }
  .g-cell.sel:hover {
    background: rgba(120, 166, 255, 0.2);
    box-shadow: inset 0 0 0 1px rgba(120, 166, 255, 0.5);
  }
  .g-cell.drop-target {
    background: rgba(120, 166, 255, 0.2);
    box-shadow: inset 0 0 0 2px rgba(120, 166, 255, 0.55);
  }
  .rubber {
    border-color: rgba(120, 166, 255, 0.55);
    background: rgba(120, 166, 255, 0.12);
  }
  .crumb a.drop-hover {
    box-shadow: inset 0 0 0 1px rgba(120, 166, 255, 0.45);
  }
  .nd-toolbar,
  .gridwrap,
  .nd-info,
  .m-scroll {
    scrollbar-color: rgba(255, 255, 255, 0.2) transparent;
  }
  .nd-toolbar::-webkit-scrollbar-thumb,
  .gridwrap::-webkit-scrollbar-thumb,
  .nd-info::-webkit-scrollbar-thumb,
  .m-scroll::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.16);
  }
}

/* 动效降级（同旧 theme.css 全局口径，scoped 内自持）：减少动态偏好下关闭动画与延迟 */
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001s !important;
    animation-iteration-count: 1 !important;
    animation-delay: 0s !important;
    transition-duration: 0.001s !important;
    transition-delay: 0s !important;
  }
}
</style>
