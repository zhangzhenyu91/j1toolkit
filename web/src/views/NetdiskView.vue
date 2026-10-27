<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">团队网盘</h1>
      <span class="page-sub">班组文件共享 · 上传 · 分享 · 下载</span>
    </div>

    <div v-if="demoMode" class="demo-banner">
      <strong>演示状态</strong>
      <span
        >列表为静态演示数据；上传/分享/删除等写操作不执行、不伪造成功。切换 live 适配层后对接 Go
        API。</span
      >
    </div>

    <!-- 子导航：文件浏览 / 分享管理 -->
    <div class="subnav">
      <button
        type="button"
        class="subnav-item"
        :class="{ active: tab === 'files' }"
        @click="tab = 'files'"
      >
        文件
      </button>
      <button
        type="button"
        class="subnav-item"
        :class="{ active: tab === 'shares' }"
        @click="openSharesTab"
      >
        分享管理
      </button>
    </div>

    <!-- ================= 文件浏览 ================= -->
    <template v-if="tab === 'files'">
      <div class="card">
        <div class="nd-toolbar">
          <!-- 空间分段 -->
          <div class="nd-spaces">
            <button
              v-for="sp in spaces"
              :key="sp.key"
              type="button"
              class="nd-space-btn"
              :class="{ on: space === sp.key }"
              @click="switchSpace(sp.key)"
            >
              {{ sp.name }}<span v-if="sp.team" class="nd-space-team">{{ sp.team }}</span>
            </button>
          </div>
          <div class="spacer"></div>
          <t-input
            v-model="searchKw"
            class="nd-search"
            placeholder="搜索文件名"
            clearable
            @enter="doSearch"
            @clear="exitSearch"
          />
          <t-button size="small" variant="outline" @click="reloadCurrent">刷新</t-button>
          <t-button size="small" variant="outline" @click="openMkdir">新建文件夹</t-button>
          <t-button size="small" theme="primary" @click="pickUpload">上传文件</t-button>
          <input ref="fileInput" type="file" multiple hidden @change="onFilesPicked" />
        </div>

        <!-- 面包屑 -->
        <div class="nd-crumb">
          <template v-if="searchKwActive">
            <span class="muted">搜索「{{ searchKw }}」的结果</span>
            <t-button size="small" variant="text" @click="exitSearch">返回目录</t-button>
          </template>
          <template v-else>
            <a href="javascript:;" @click="navTo('')">根目录</a>
            <template v-for="(seg, i) in crumbSegs" :key="seg.path">
              <span class="nd-crumb-sep">/</span>
              <a v-if="i < crumbSegs.length - 1" href="javascript:;" @click="navTo(seg.path)">{{
                seg.name
              }}</a>
              <span v-else class="nd-crumb-cur">{{ seg.name }}</span>
            </template>
          </template>
        </div>

        <!-- 选择操作条 -->
        <div v-if="selected.length" class="nd-selbar">
          <span class="sub-text">已选 {{ selected.length }} 项</span>
          <t-button size="small" variant="outline" @click="downloadSelected">下载</t-button>
          <t-button size="small" variant="outline" @click="openShareModal(selectedItems)"
            >分享</t-button
          >
          <t-button size="small" variant="outline" @click="openMoveCopy('move')">移动</t-button>
          <t-button size="small" variant="outline" @click="openMoveCopy('copy')">复制</t-button>
          <t-button size="small" variant="outline" @click="openBatchRename">批量重命名</t-button>
          <t-button size="small" variant="outline" theme="danger" @click="removeSelected"
            >删除</t-button
          >
          <t-button size="small" variant="text" @click="selected = []">取消选择</t-button>
        </div>

        <t-loading :loading="loading" size="small">
          <table class="nd-table">
            <thead>
              <tr>
                <th class="nd-col-check">
                  <input type="checkbox" :checked="allChecked" @change="toggleAll" />
                </th>
                <th>名称</th>
                <th class="nd-col-size num">大小</th>
                <th class="nd-col-time num">修改时间</th>
                <th class="nd-col-op">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="it in items"
                :key="it._rel"
                :class="{ checked: selected.includes(it._rel) }"
              >
                <td class="nd-col-check">
                  <input
                    type="checkbox"
                    :checked="selected.includes(it._rel)"
                    @change="toggleSel(it._rel)"
                  />
                </td>
                <td>
                  <div class="nd-name" @click="openItem(it)">
                    <img
                      v-if="thumbs[it._rel]"
                      :src="thumbs[it._rel]"
                      class="nd-thumb"
                      alt=""
                      loading="lazy"
                    />
                    <span v-else class="nd-ic" :class="it.is_dir ? 'dir' : 'file'">{{
                      it.is_dir ? '夹' : '文'
                    }}</span>
                    <span class="nd-nm" :title="it.name">{{ it.name }}</span>
                  </div>
                  <div v-if="it._parentDisp" class="muted">{{ it._parentDisp }}</div>
                </td>
                <td class="nd-col-size num muted">{{ it.is_dir ? '—' : fmtSize(it.size) }}</td>
                <td class="nd-col-time num muted">{{ fmtSec(it.modified) }}</td>
                <td class="nd-col-op">
                  <t-button size="small" variant="text" @click="downloadItem(it)">下载</t-button>
                  <t-button size="small" variant="text" @click="openShareModal([it])"
                    >分享</t-button
                  >
                  <t-button size="small" variant="text" @click="openRename(it)">重命名</t-button>
                  <t-button
                    v-if="!it.is_dir && isImageName(it.name)"
                    size="small"
                    variant="text"
                    @click="previewImage(it)"
                  >
                    预览
                  </t-button>
                  <t-button
                    v-if="!it.is_dir && isArchiveName(it.name)"
                    size="small"
                    variant="text"
                    @click="openArchive(it)"
                  >
                    压缩包
                  </t-button>
                  <t-button v-if="it.is_dir" size="small" variant="text" @click="showDirSize(it)"
                    >大小</t-button
                  >
                  <t-button size="small" variant="text" theme="danger" @click="removeItems([it])"
                    >删除</t-button
                  >
                </td>
              </tr>
              <tr v-if="!loading && !items.length">
                <td colspan="5" class="nd-empty muted">{{ emptyText }}</td>
              </tr>
            </tbody>
          </table>
        </t-loading>
      </div>

      <!-- 上传队列 -->
      <div v-if="uploads.length" class="card">
        <h2 class="card-title">上传队列（{{ uploads.length }}）</h2>
        <div v-for="t in uploads" :key="t.id" class="nd-up-row">
          <div class="row-main">
            <div class="row-title">{{ t.name }}</div>
            <div class="row-sub num">{{ upSubText(t) }}</div>
            <div class="nd-up-bar">
              <i :style="{ width: pctOf(t) + '%' }" :class="{ fail: t.state === 'error' }"></i>
            </div>
          </div>
          <t-button
            v-if="t.state === 'uploading' || t.state === 'pending'"
            size="small"
            variant="text"
            @click="cancelUpload(t)"
          >
            取消
          </t-button>
          <t-button v-if="t.state === 'error'" size="small" variant="text" @click="retryUpload(t)"
            >重试</t-button
          >
          <t-button
            v-if="t.state === 'done' || t.state === 'error' || t.state === 'aborted'"
            size="small"
            variant="text"
            @click="dismissUpload(t)"
          >
            移除
          </t-button>
        </div>
      </div>
    </template>

    <!-- ================= 分享管理 ================= -->
    <template v-else>
      <div class="card">
        <div class="nd-toolbar">
          <h2 class="card-title" style="margin: 0">我的分享</h2>
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="loadShares">刷新</t-button>
        </div>
        <t-loading :loading="sharesLoading" size="small">
          <table class="nd-table">
            <thead>
              <tr>
                <th>分享内容</th>
                <th class="nd-col-code">提取码</th>
                <th class="nd-col-time num">有效期至</th>
                <th class="nd-col-state">状态</th>
                <th class="nd-col-op">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in shares" :key="s.share_id">
                <td>
                  <div class="nd-name">
                    <span class="nd-nm" :title="(s.items || []).join('、')">{{
                      (s.items || []).join('、') || '—'
                    }}</span>
                  </div>
                  <div class="muted">{{ s.share_id }}</div>
                </td>
                <td class="nd-col-code num">{{ s.password }}</td>
                <td class="nd-col-time num muted">
                  {{ s.expire_at ? fmtSec(s.expire_at) : '永久' }}
                </td>
                <td class="nd-col-state">
                  <span class="dot-tag" :class="shareStateColor(s.state)">
                    <span class="dot"></span>{{ shareStateText(s.state) }}
                  </span>
                </td>
                <td class="nd-col-op">
                  <t-button size="small" variant="text" @click="copyShare(s)">复制链接</t-button>
                  <t-button
                    v-if="s.state === 'active'"
                    size="small"
                    variant="text"
                    @click="setShareEnabled(s, false)"
                    >停用</t-button
                  >
                  <t-button v-else size="small" variant="text" @click="setShareEnabled(s, true)"
                    >启用</t-button
                  >
                  <t-button size="small" variant="text" theme="danger" @click="removeShare(s)"
                    >删除</t-button
                  >
                </td>
              </tr>
              <tr v-if="!sharesLoading && !shares.length">
                <td colspan="5" class="nd-empty muted">暂无分享</td>
              </tr>
            </tbody>
          </table>
        </t-loading>
      </div>
    </template>

    <!-- ================= 弹层：新建文件夹 / 重命名 ================= -->
    <div v-if="inputDlg.open" class="nd-mask">
      <div class="nd-dialog" role="dialog" aria-modal="true">
        <h3>{{ inputDlg.title }}</h3>
        <t-input
          v-model="inputDlg.value"
          :placeholder="inputDlg.placeholder"
          @enter="confirmInputDialog"
        />
        <div class="nd-dfoot">
          <t-button size="small" variant="outline" @click="inputDlg.open = false">取消</t-button>
          <t-button
            size="small"
            theme="primary"
            :loading="inputDlg.busy"
            @click="confirmInputDialog"
            >确定</t-button
          >
        </div>
      </div>
    </div>

    <!-- ================= 弹层：创建分享 ================= -->
    <div v-if="shareDlg.open" class="nd-mask">
      <div class="nd-dialog nd-dialog-wide" role="dialog" aria-modal="true">
        <template v-if="!shareDlg.result">
          <h3>创建分享</h3>
          <div class="nd-share-files">
            <div v-for="it in shareDlg.items" :key="it._rel" class="nd-share-file">
              <span class="nd-nm" :title="it.name">{{ it.name }}</span>
              <span class="muted">{{ it.is_dir ? '文件夹' : fmtSize(it.size) }}</span>
            </div>
          </div>
          <div class="nd-kv">
            <span class="field-label">有效期</span>
            <div class="nd-radios">
              <button
                v-for="d in [7, 30, 0]"
                :key="d"
                type="button"
                class="nd-rd"
                :class="{ on: shareDlg.expireDays === d }"
                @click="shareDlg.expireDays = d"
              >
                {{ d === 0 ? '永久' : d + ' 天' }}
              </button>
            </div>
          </div>
          <div class="nd-kv">
            <span class="field-label">提取码（可刷新）</span>
            <div class="nd-codebox">
              <span class="num">{{ shareDlg.code }}</span>
              <t-button size="small" variant="text" @click="shareDlg.code = randShareCode()"
                >刷新</t-button
              >
            </div>
          </div>
          <p class="muted">外部人员凭链接 + 提取码访问，对方无需登录。</p>
          <div class="nd-dfoot">
            <t-button size="small" variant="outline" @click="shareDlg.open = false">取消</t-button>
            <t-button size="small" theme="primary" :loading="shareDlg.busy" @click="createShare"
              >生成分享链接</t-button
            >
          </div>
        </template>
        <template v-else>
          <h3>分享已创建</h3>
          <p class="muted">
            链接与提取码请一并发送给访问人；有效期至
            {{ shareDlg.result.expire_at ? fmtSec(shareDlg.result.expire_at) : '永久' }}。
          </p>
          <div class="nd-kv">
            <span class="field-label">分享链接</span>
            <div class="nd-link num">{{ shareDlg.result.link }}</div>
          </div>
          <div class="nd-kv">
            <span class="field-label">提取码</span>
            <div class="nd-codebox">
              <span class="num">{{ shareDlg.result.code }}</span>
            </div>
          </div>
          <div class="nd-dfoot">
            <t-button size="small" variant="outline" @click="shareDlg.open = false">关闭</t-button>
            <t-button size="small" theme="primary" @click="copyShareResult">一键复制</t-button>
          </div>
        </template>
      </div>
    </div>

    <!-- ================= 弹层：删除确认 ================= -->
    <div v-if="delDlg.open" class="nd-mask">
      <div class="nd-dialog" role="dialog" aria-modal="true">
        <h3>删除确认</h3>
        <p class="sub-text">{{ delDlg.text }}</p>
        <div class="nd-dfoot">
          <t-button size="small" variant="outline" @click="delDlg.open = false">取消</t-button>
          <t-button size="small" theme="danger" :loading="delDlg.busy" @click="confirmDelete"
            >删除</t-button
          >
        </div>
      </div>
    </div>

    <!-- ================= 弹层：移动 / 复制 ================= -->
    <div v-if="mvDlg.open" class="nd-mask">
      <div class="nd-dialog nd-dialog-wide" role="dialog" aria-modal="true">
        <h3>{{ mvDlg.mode === 'move' ? '移动到' : '复制到' }}</h3>
        <div class="nd-share-files">
          <div v-for="it in mvDlg.items" :key="it._rel" class="nd-share-file">
            <span class="nd-nm" :title="it.name">{{ it.name }}</span>
            <span class="muted">{{ it.is_dir ? '文件夹' : fmtSize(it.size) }}</span>
          </div>
        </div>
        <div class="nd-kv">
          <span class="field-label">目标空间</span>
          <div class="nd-radios">
            <button
              v-for="sp in spaces"
              :key="sp.key"
              type="button"
              class="nd-rd"
              :class="{ on: mvDlg.dstSpace === sp.key }"
              @click="switchMvSpace(sp.key)"
            >
              {{ sp.name }}
            </button>
          </div>
        </div>
        <div class="nd-crumb">
          <a href="javascript:;" @click="mvNavTo('')">根目录</a>
          <template v-for="seg in mvCrumbSegs" :key="seg.path">
            <span class="nd-crumb-sep">/</span>
            <a v-if="seg.path !== mvDlg.dstPath" href="javascript:;" @click="mvNavTo(seg.path)">{{
              seg.name
            }}</a>
            <span v-else class="nd-crumb-cur">{{ seg.name }}</span>
          </template>
        </div>
        <t-loading :loading="mvDlg.loading" size="small">
          <div class="nd-mvlist">
            <div
              v-for="d in mvDlg.dirs"
              :key="d.name"
              class="nd-mvrow"
              @dblclick="mvNavTo(mvRel(d.name))"
            >
              <span class="nd-ic dir">夹</span>
              <span class="nd-nm">{{ d.name }}</span>
              <t-button size="small" variant="text" @click="mvNavTo(mvRel(d.name))">进入</t-button>
            </div>
            <div v-if="!mvDlg.loading && !mvDlg.dirs.length" class="muted nd-empty">
              当前目录没有子文件夹
            </div>
          </div>
        </t-loading>
        <div class="nd-dfoot">
          <span class="sub-text"
            >目标：{{ mvDlg.dstSpace === 'my' ? '我的空间' : '公共区' }}/{{
              mvDlg.dstPath || ''
            }}</span
          >
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="mvDlg.open = false">取消</t-button>
          <t-button size="small" theme="primary" :loading="mvDlg.busy" @click="confirmMoveCopy">
            {{ mvDlg.mode === 'move' ? '移动到这里' : '复制到这里' }}
          </t-button>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：批量重命名 ================= -->
    <div v-if="batchDlg.open" class="nd-mask">
      <div class="nd-dialog nd-dialog-wide" role="dialog" aria-modal="true">
        <h3>批量重命名（{{ batchDlg.items.length }} 项）</h3>
        <div class="nd-kv">
          <span class="field-label">方式</span>
          <div class="nd-radios">
            <button
              type="button"
              class="nd-rd"
              :class="{ on: batchDlg.mode === 'prefix' }"
              @click="batchDlg.mode = 'prefix'"
            >
              添加前缀
            </button>
            <button
              type="button"
              class="nd-rd"
              :class="{ on: batchDlg.mode === 'replace' }"
              @click="batchDlg.mode = 'replace'"
            >
              查找替换
            </button>
            <button
              type="button"
              class="nd-rd"
              :class="{ on: batchDlg.mode === 'number' }"
              @click="batchDlg.mode = 'number'"
            >
              编号命名
            </button>
          </div>
        </div>
        <div v-if="batchDlg.mode === 'prefix'" class="nd-kv">
          <span class="field-label">前缀</span>
          <t-input v-model="batchDlg.prefix" placeholder="如：2026-" />
        </div>
        <template v-else-if="batchDlg.mode === 'replace'">
          <div class="nd-kv">
            <span class="field-label">查找</span>
            <t-input v-model="batchDlg.find" placeholder="原文字" />
          </div>
          <div class="nd-kv">
            <span class="field-label">替换为</span>
            <t-input v-model="batchDlg.replace" placeholder="可为空（删除匹配文字）" />
          </div>
        </template>
        <template v-else>
          <div class="nd-kv">
            <span class="field-label">起始编号</span>
            <t-input v-model="batchDlg.start" class="nd-short" />
            <span class="field-label">位数</span>
            <t-input v-model="batchDlg.digits" class="nd-short" />
          </div>
          <p class="muted">命名形如「001.扩展名」，按当前列表顺序编号，扩展名保留。</p>
        </template>
        <div class="nd-share-files nd-preview-list">
          <div v-for="p in batchPreview" :key="p.path" class="nd-share-file">
            <span class="nd-nm" :title="p.old">{{ p.old }}</span>
            <span class="muted">→</span>
            <span
              class="nd-nm"
              :class="{ 'nd-bad': !p.name || p.name === p.old }"
              :title="p.name"
              >{{ p.name || '（不合法）' }}</span
            >
          </div>
        </div>
        <div class="nd-dfoot">
          <t-button size="small" variant="outline" @click="batchDlg.open = false">取消</t-button>
          <t-button
            size="small"
            theme="primary"
            :loading="batchDlg.busy"
            @click="confirmBatchRename"
            >应用</t-button
          >
        </div>
      </div>
    </div>

    <!-- ================= 弹层：目录大小 ================= -->
    <div v-if="sizeDlg.open" class="nd-mask">
      <div class="nd-dialog" role="dialog" aria-modal="true">
        <h3>目录大小</h3>
        <t-loading :loading="sizeDlg.loading" size="small">
          <div class="nd-kv">
            <span class="field-label">文件夹</span>
            <span class="nd-nm">{{ sizeDlg.name }}</span>
          </div>
          <template v-if="sizeDlg.stat">
            <div class="nd-kv">
              <span class="field-label">合计大小</span>
              <b class="num">{{ fmtSize(sizeDlg.stat.size) }}</b>
            </div>
            <div class="nd-kv">
              <span class="field-label">文件 / 文件夹</span>
              <span class="num">{{ sizeDlg.stat.files }} / {{ sizeDlg.stat.dirs }}</span>
            </div>
            <p v-if="sizeDlg.stat.truncated" class="nd-warn">
              条目过多或层级过深，以上为部分统计结果（已截断）。
            </p>
          </template>
        </t-loading>
        <div class="nd-dfoot">
          <t-button size="small" theme="primary" @click="sizeDlg.open = false">关闭</t-button>
        </div>
      </div>
    </div>

    <!-- ================= 弹层：压缩包管理 ================= -->
    <div v-if="arcDlg.open" class="nd-mask">
      <div class="nd-dialog nd-dialog-wide" role="dialog" aria-modal="true">
        <h3>压缩包 · {{ arcDlg.item ? arcDlg.item.name : '' }}</h3>
        <div v-if="arcDlg.meta" class="nd-kv">
          <span class="field-label">信息</span>
          <span class="muted num">{{
            fmtSize(arcDlg.meta.size || (arcDlg.item && arcDlg.item.size) || 0)
          }}</span>
          <span v-if="arcDlg.meta.encrypted" class="dot-tag orange"
            ><span class="dot"></span>加密</span
          >
        </div>
        <div v-if="arcDlg.needPass" class="nd-kv">
          <span class="field-label">压缩包密码</span>
          <t-input
            v-model="arcDlg.pass"
            placeholder="加密压缩包需填写密码"
            @enter="reloadArchive"
          />
          <t-button size="small" variant="outline" @click="reloadArchive">读取</t-button>
        </div>
        <div class="nd-crumb">
          <a href="javascript:;" @click="arcNavTo('/')">{{
            arcDlg.item ? arcDlg.item.name : ''
          }}</a>
          <template v-for="seg in arcCrumbSegs" :key="seg.path">
            <span class="nd-crumb-sep">/</span>
            <a v-if="seg.path !== arcDlg.inner" href="javascript:;" @click="arcNavTo(seg.path)">{{
              seg.name
            }}</a>
            <span v-else class="nd-crumb-cur">{{ seg.name }}</span>
          </template>
        </div>
        <t-loading :loading="arcDlg.loading" size="small">
          <table class="nd-table">
            <thead>
              <tr>
                <th>名称</th>
                <th class="nd-col-size num">大小</th>
                <th class="nd-col-op">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="it in arcDlg.items" :key="it.name">
                <td>
                  <div class="nd-name" @click="it.is_dir && arcNavTo(arcRel(it.name))">
                    <span class="nd-ic" :class="it.is_dir ? 'dir' : 'file'">{{
                      it.is_dir ? '夹' : '文'
                    }}</span>
                    <span class="nd-nm" :title="it.name">{{ it.name }}</span>
                  </div>
                </td>
                <td class="nd-col-size num muted">{{ it.is_dir ? '—' : fmtSize(it.size) }}</td>
                <td class="nd-col-op">
                  <t-button
                    v-if="it.is_dir"
                    size="small"
                    variant="text"
                    @click="arcNavTo(arcRel(it.name))"
                    >进入</t-button
                  >
                  <t-button v-else size="small" variant="text" @click="downloadArchiveItem(it)"
                    >下载</t-button
                  >
                </td>
              </tr>
              <tr v-if="!arcDlg.loading && !arcDlg.items.length">
                <td colspan="3" class="nd-empty muted">{{ arcDlg.error || '包内为空' }}</td>
              </tr>
            </tbody>
          </table>
        </t-loading>
        <div class="nd-dfoot">
          <span class="sub-text">解压到当前网盘目录{{ path ? '：' + path : '（根目录）' }}</span>
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="arcDlg.open = false">关闭</t-button>
          <t-button
            size="small"
            theme="primary"
            :loading="arcDlg.decompressing"
            @click="decompressArchive"
            >解压到当前目录</t-button
          >
        </div>
      </div>
    </div>

    <!-- ================= 弹层：图片预览 ================= -->
    <div v-if="imgDlg.open" class="nd-mask" @click="imgDlg.open = false">
      <div class="nd-imgbox" role="dialog" aria-modal="true" @click.stop>
        <img :src="imgDlg.url" :alt="imgDlg.name" />
        <div class="nd-imgbar">
          <span class="nd-nm">{{ imgDlg.name }}</span>
          <div class="spacer"></div>
          <t-button size="small" variant="outline" @click="imgDlg.open = false">关闭</t-button>
          <t-button size="small" theme="primary" @click="downloadItem(imgDlg.item)"
            >下载原图</t-button
          >
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 团队网盘（迁移 server/public/netdisk.html 核心功能）：空间/目录浏览、搜索、分片上传、
// 创建分享与分享管理、下载、重命名、删除；Office 文档经 /office 承载页打开。
// 已补齐：移动/复制（跨空间目标目录选择）、批量重命名、目录大小统计、压缩包管理
// （元信息/包内浏览/包内下载/解压到当前目录）、图片缩略图与预览。
// 未复刻（见交付说明缺口）：网格视图、ShadeNdPick 浏览式目录选择（存网盘为路径输入）。
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { MessagePlugin } from 'tdesign-vue-next';
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
  download,
  downloadDir,
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
  thumb,
  isArchiveName,
  isImageName,
} from '../features/netdisk/api.js';

const router = useRouter();
const demoMode = isDemo();

// ---- 页面状态 ----
const tab = ref('files');
const spaces = ref([]);
const space = ref('my');
const path = ref(''); // 相对空间根目录路径（''=根）
const items = ref([]);
const loading = ref(false);
const selected = ref([]);
const searchKw = ref('');
const searchKwActive = ref(false);
const fileInput = ref(null);

// ---- 工具 ----
function fmtSize(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return Math.round(n / 1024) + ' KB';
  return `${n || 0} B`;
}
function fmtSec(d) {
  if (!d) return '—';
  const dt = new Date(String(d).replace(' ', 'T').replace(/\//g, '-'));
  if (Number.isNaN(dt.getTime())) return String(d);
  const p = (n) => String(n).padStart(2, '0');
  return `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}

const crumbSegs = computed(() => {
  const segs = [];
  if (!path.value) return segs;
  const parts = path.value.split('/').filter(Boolean);
  let acc = '';
  parts.forEach((p) => {
    acc = acc ? `${acc}/${p}` : p;
    segs.push({ name: p, path: acc });
  });
  return segs;
});

const selectedItems = computed(() => items.value.filter((it) => selected.value.includes(it._rel)));
const allChecked = computed(
  () => items.value.length > 0 && selected.value.length === items.value.length
);
const emptyText = computed(() => (searchKwActive.value ? '没有匹配的文件' : '该目录为空'));

// ---- 列表加载 ----
function decorate(list, parentOf) {
  return (list || [])
    .slice()
    .sort((a, b) => {
      if (!!a.is_dir !== !!b.is_dir) return a.is_dir ? -1 : 1;
      return String(a.name).localeCompare(String(b.name), 'zh');
    })
    .map((it) => {
      const parent = parentOf(it);
      it._rel = parent ? itemRel(parent, it.name) : it.name;
      return it;
    });
}

async function loadList(silent = false) {
  selected.value = [];
  if (!silent) loading.value = true;
  try {
    const d = await listDir({ space: space.value, path: path.value, refresh: !!silent });
    items.value = decorate(d.items || [], () => path.value);
    searchKwActive.value = false;
    loadThumbs(items.value);
  } catch (e) {
    MessagePlugin.error(e.message || '目录加载失败');
  } finally {
    loading.value = false;
  }
}

async function doSearch() {
  const kw = searchKw.value.trim();
  if (!kw) {
    exitSearch();
    return;
  }
  loading.value = true;
  try {
    const d = await search({ space: space.value, keywords: kw, page: 1, perPage: 100 });
    items.value = decorate(d.items || [], (it) =>
      it.parent && it.parent !== '/' ? it.parent : ''
    );
    searchKwActive.value = true;
    loadThumbs(items.value);
    selected.value = [];
  } catch (e) {
    MessagePlugin.error(e.message || '搜索失败');
  } finally {
    loading.value = false;
  }
}

function exitSearch() {
  searchKw.value = '';
  searchKwActive.value = false;
  loadList();
}

function reloadCurrent() {
  if (searchKwActive.value) doSearch();
  else loadList(true);
}

function navTo(p) {
  path.value = p;
  searchKw.value = '';
  searchKwActive.value = false;
  loadList();
}

function switchSpace(k) {
  if (space.value === k) return;
  space.value = k;
  path.value = '';
  loadList();
}

// ---- 选择 ----
function toggleSel(rel) {
  const i = selected.value.indexOf(rel);
  if (i >= 0) selected.value.splice(i, 1);
  else selected.value.push(rel);
}
function toggleAll() {
  selected.value = allChecked.value ? [] : items.value.map((it) => it._rel);
}

// ---- 打开条目 ----
const OFFICE_EXTS = ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx'];
const PREVIEW_ONLY_EXTS = ['pdf'];

function openItem(it) {
  if (it.is_dir) {
    navTo(it._rel);
    return;
  }
  const ext = it.name.split('.').pop().toLowerCase();
  if (OFFICE_EXTS.includes(ext)) {
    // 在线编辑（OO 承载页当前窗口打开，back 回本网盘）
    router.push({
      path: '/office',
      query: {
        space: space.value,
        path: it._rel,
        name: it.name,
        mode: 'edit',
        back: '/netdisk',
      },
    });
    return;
  }
  if (PREVIEW_ONLY_EXTS.includes(ext)) {
    router.push({
      path: '/office',
      query: {
        space: space.value,
        path: it._rel,
        name: it.name,
        mode: 'view',
        back: '/netdisk',
      },
    });
    return;
  }
  downloadItem(it);
}

// ---- 下载（Go 端仅认 Bearer 头，fetch + Blob 落地） ----
async function downloadItem(it) {
  try {
    if (it.is_dir) {
      MessagePlugin.info(`正在打包文件夹「${it.name}」，完成后将自动下载 ZIP`);
      const res = await downloadDir({ space: space.value, path: it._rel });
      await saveBlob(res, `${it.name}.zip`);
      return;
    }
    const res = await download({ space: space.value, path: it._rel });
    await saveBlob(res, it.name);
  } catch (e) {
    MessagePlugin.error(e.message || '下载失败');
  }
}
function downloadSelected() {
  selectedItems.value.forEach((it, i) => {
    setTimeout(() => downloadItem(it), i * 300);
  });
}

// ---- 新建文件夹 / 重命名 ----
const inputDlg = reactive({
  open: false,
  title: '',
  placeholder: '',
  value: '',
  busy: false,
  kind: '', // mkdir / rename
  target: null,
});

function openMkdir() {
  if (searchKwActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览');
    return;
  }
  Object.assign(inputDlg, {
    open: true,
    title: '新建文件夹',
    placeholder: '文件夹名称',
    value: '',
    busy: false,
    kind: 'mkdir',
    target: null,
  });
}

function openRename(it) {
  Object.assign(inputDlg, {
    open: true,
    title: '重命名',
    placeholder: '新名称',
    value: it.name,
    busy: false,
    kind: 'rename',
    target: it,
  });
}

async function confirmInputDialog() {
  const name = inputDlg.value.trim();
  if (!name) {
    MessagePlugin.error(inputDlg.kind === 'mkdir' ? '请输入文件夹名称' : '请输入名称');
    return;
  }
  inputDlg.busy = true;
  try {
    if (inputDlg.kind === 'mkdir') {
      await mkdir({ space: space.value, path: path.value, name });
      MessagePlugin.success('已创建');
    } else {
      await renameApi({ space: space.value, path: inputDlg.target._rel, name });
      MessagePlugin.success('已重命名');
    }
    inputDlg.open = false;
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '操作失败');
  } finally {
    inputDlg.busy = false;
  }
}

// ---- 删除 ----
const delDlg = reactive({ open: false, text: '', busy: false, targets: [] });

function removeItems(list) {
  if (!list.length) return;
  delDlg.open = true;
  delDlg.text = `确定删除选中的 ${list.length} 项吗？删除后不可恢复。`;
  delDlg.targets = list;
}
function removeSelected() {
  removeItems(selectedItems.value);
}

async function confirmDelete() {
  delDlg.busy = true;
  try {
    await removeApi({ space: space.value, paths: delDlg.targets.map((it) => it._rel) });
    MessagePlugin.success('已删除');
    delDlg.open = false;
    selected.value = [];
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '删除失败');
  } finally {
    delDlg.busy = false;
  }
}

// ---- 上传（分片管线在 feature api：init → chunk×N → complete） ----
const uploads = ref([]);
let upSeq = 0;
let upRunning = false;

function pickUpload() {
  if (searchKwActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览再上传');
    return;
  }
  fileInput.value.click();
}

function onFilesPicked(e) {
  const list = Array.from(e.target.files || []);
  e.target.value = '';
  enqueueUploads(list);
}

function enqueueUploads(list) {
  list.forEach((f) => {
    uploads.value.push({
      id: `u-${++upSeq}`,
      file: f,
      name: f.name,
      size: f.size,
      space: space.value,
      path: path.value,
      state: 'pending',
      received: 0,
      doneChunks: 0,
      totalChunks: 0,
      err: '',
      cancelled: false,
    });
  });
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
        MessagePlugin.success(`「${t.name}」上传完成`);
        if (space.value === t.space && path.value === t.path && !searchKwActive.value) {
          loadList(true);
        }
      }
    } catch (e) {
      if (t.cancelled) {
        t.state = 'aborted';
      } else {
        t.state = 'error';
        t.err = e.message || '上传失败';
        MessagePlugin.error(`「${t.name}」上传失败：${t.err}`);
      }
    }
  }
  upRunning = false;
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

// ---- 分享 ----
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
  } catch (e) {
    MessagePlugin.error(e.message || '创建分享失败');
  } finally {
    shareDlg.busy = false;
  }
}

function copyText(text, okMsg) {
  const done = () => MessagePlugin.success(okMsg);
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done, () => fallbackCopy(text, done));
  } else {
    fallbackCopy(text, done);
  }
}
function fallbackCopy(text, done) {
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
}

function copyShareResult() {
  const r = shareDlg.result;
  copyText(`链接：${r.link}\n提取码：${r.code}`, '链接与提取码已复制');
}

// ---- 分享管理 ----
const shares = ref([]);
const sharesLoading = ref(false);

function openSharesTab() {
  tab.value = 'shares';
  loadShares();
}

async function loadShares() {
  sharesLoading.value = true;
  try {
    shares.value = (await listShares('mine')) || [];
  } catch (e) {
    MessagePlugin.error(e.message || '分享列表加载失败');
  } finally {
    sharesLoading.value = false;
  }
}

function shareStateText(st) {
  return { active: '生效中', disabled: '已停用', expired: '已过期' }[st] || st || '—';
}
function shareStateColor(st) {
  return { active: 'green', disabled: 'orange', expired: 'red' }[st] || 'blue';
}

function copyShare(s) {
  copyText(`链接：${shareLinkUrl(s.share_id)}\n提取码：${s.password}`, '链接与提取码已复制');
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

// ---- 初始化 ----
onMounted(async () => {
  try {
    const d = await listSpaces();
    spaces.value = d.spaces || [];
    if (spaces.value.length && !spaces.value.some((sp) => sp.key === space.value)) {
      space.value = spaces.value[0].key;
    }
  } catch (e) {
    MessagePlugin.error(e.message || '空间列表加载失败');
  }
  loadList();
});

onBeforeUnmount(() => {
  Object.values(thumbs.value).forEach((u) => URL.revokeObjectURL(u));
  if (imgDlg.url) URL.revokeObjectURL(imgDlg.url);
});

// ---- 图片缩略图（懒加载，列表刷新时按图片扩展名逐个取 /thumb） ----
const thumbs = ref({}); // rel -> objectURL
const thumbPending = new Set();

function loadThumbs(list) {
  list
    .filter(
      (it) =>
        !it.is_dir && isImageName(it.name) && !thumbs.value[it._rel] && !thumbPending.has(it._rel)
    )
    .slice(0, 12)
    .forEach((it) => {
      thumbPending.add(it._rel);
      thumb({ space: space.value, path: it._rel })
        .then((res) => res.blob())
        .then((blob) => {
          thumbs.value = { ...thumbs.value, [it._rel]: URL.createObjectURL(blob) };
        })
        .catch(() => {
          /* 缩略图失败保持占位图标 */
        })
        .finally(() => thumbPending.delete(it._rel));
    });
}

// ---- 图片预览（缩略图放大走内联下载原图） ----
const imgDlg = reactive({ open: false, url: '', name: '', item: null });

async function previewImage(it) {
  try {
    const res = await download({ space: space.value, path: it._rel, disposition: 'inline' });
    const blob = await res.blob();
    if (imgDlg.url) URL.revokeObjectURL(imgDlg.url);
    imgDlg.url = URL.createObjectURL(blob);
    imgDlg.name = it.name;
    imgDlg.item = it;
    imgDlg.open = true;
  } catch (e) {
    MessagePlugin.error(e.message || '预览失败');
  }
}

// ---- 目录大小 ----
const sizeDlg = reactive({ open: false, loading: false, name: '', stat: null });

async function showDirSize(it) {
  Object.assign(sizeDlg, { open: true, loading: true, name: it.name, stat: null });
  try {
    sizeDlg.stat = await dirSize({ space: space.value, path: it._rel });
  } catch (e) {
    MessagePlugin.error(e.message || '目录大小计算失败');
    sizeDlg.open = false;
  } finally {
    sizeDlg.loading = false;
  }
}

// ---- 移动 / 复制 ----
const mvDlg = reactive({
  open: false,
  mode: 'move',
  items: [],
  dstSpace: 'my',
  dstPath: '',
  dirs: [],
  loading: false,
  busy: false,
});

function sameParentDir(list) {
  const dirs = new Set(
    list.map((it) => (it._rel.includes('/') ? it._rel.slice(0, it._rel.lastIndexOf('/')) : ''))
  );
  return dirs.size === 1 ? [...dirs][0] : null;
}

function openMoveCopy(mode) {
  const list = selectedItems.value;
  if (!list.length) return;
  if (list.length > 100) {
    MessagePlugin.error('单次最多操作 100 项');
    return;
  }
  const dir = sameParentDir(list);
  if (dir === null) {
    MessagePlugin.error('移动/复制的条目需在同一目录内');
    return;
  }
  Object.assign(mvDlg, {
    open: true,
    mode,
    items: list,
    dstSpace: space.value,
    dstPath: '',
    dirs: [],
    loading: false,
    busy: false,
  });
  mvLoadDirs();
}

function switchMvSpace(k) {
  if (mvDlg.dstSpace === k) return;
  mvDlg.dstSpace = k;
  mvDlg.dstPath = '';
  mvLoadDirs();
}

function mvRel(name) {
  return mvDlg.dstPath ? `${mvDlg.dstPath}/${name}` : name;
}

const mvCrumbSegs = computed(() => {
  const segs = [];
  if (!mvDlg.dstPath) return segs;
  const parts = mvDlg.dstPath.split('/').filter(Boolean);
  let acc = '';
  parts.forEach((p) => {
    acc = acc ? `${acc}/${p}` : p;
    segs.push({ name: p, path: acc });
  });
  return segs;
});

function mvNavTo(p) {
  mvDlg.dstPath = p;
  mvLoadDirs();
}

async function mvLoadDirs() {
  mvDlg.loading = true;
  try {
    const d = await listDir({ space: mvDlg.dstSpace, path: mvDlg.dstPath });
    mvDlg.dirs = (d.items || []).filter((it) => it.is_dir);
  } catch (e) {
    mvDlg.dirs = [];
    MessagePlugin.error(e.message || '目标目录加载失败');
  } finally {
    mvDlg.loading = false;
  }
}

async function confirmMoveCopy() {
  const srcDir = sameParentDir(mvDlg.items);
  if (srcDir === null) {
    MessagePlugin.error('移动/复制的条目需在同一目录内');
    return;
  }
  if (mvDlg.dstSpace === space.value && mvDlg.dstPath === srcDir) {
    MessagePlugin.error('源位置与目标位置相同');
    return;
  }
  mvDlg.busy = true;
  try {
    const params = {
      srcSpace: space.value,
      srcDir,
      names: mvDlg.items.map((it) => it.name),
      dstSpace: mvDlg.dstSpace,
      dstDir: mvDlg.dstPath,
    };
    if (mvDlg.mode === 'move') await moveItems(params);
    else await copyItems(params);
    MessagePlugin.success(mvDlg.mode === 'move' ? '已移动' : '已复制');
    mvDlg.open = false;
    selected.value = [];
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || (mvDlg.mode === 'move' ? '移动失败' : '复制失败'));
  } finally {
    mvDlg.busy = false;
  }
}

// ---- 批量重命名 ----
const batchDlg = reactive({
  open: false,
  mode: 'prefix',
  prefix: '',
  find: '',
  replace: '',
  start: '1',
  digits: '3',
  items: [],
  busy: false,
});

function openBatchRename() {
  const list = selectedItems.value;
  if (!list.length) return;
  if (list.length > 100) {
    MessagePlugin.error('批量重命名单次最多 100 项');
    return;
  }
  Object.assign(batchDlg, {
    open: true,
    mode: 'prefix',
    prefix: '',
    find: '',
    replace: '',
    start: '1',
    digits: '3',
    items: list,
    busy: false,
  });
}

// splitName 拆「不含扩展名的主名 / 扩展名（含点）」。
function splitName(name) {
  const i = name.lastIndexOf('.');
  if (i <= 0) return { base: name, ext: '' };
  return { base: name.slice(0, i), ext: name.slice(i) };
}

// batchPreview 重命名结果预览（空名/原名视为不合法或无效）。
const batchPreview = computed(() => {
  const n = parseInt(batchDlg.digits, 10);
  const digits = Number.isFinite(n) ? Math.min(8, Math.max(1, n)) : 3;
  const startRaw = parseInt(batchDlg.start, 10);
  const start = Number.isFinite(startRaw) ? startRaw : 1;
  return batchDlg.items.map((it, idx) => {
    const { base, ext } = splitName(it.name);
    let name = it.name;
    if (batchDlg.mode === 'prefix') {
      name = batchDlg.prefix + base + ext;
    } else if (batchDlg.mode === 'replace') {
      if (batchDlg.find) name = base.split(batchDlg.find).join(batchDlg.replace) + ext;
    } else {
      name = String(start + idx).padStart(digits, '0') + ext;
    }
    return { path: it._rel, old: it.name, name };
  });
});

async function confirmBatchRename() {
  const list = batchPreview.value;
  const invalid = list.find(
    (p) => !p.name || p.name === '.' || p.name === '..' || /[\\/:*?"<>|\r\n]/.test(p.name)
  );
  if (invalid) {
    MessagePlugin.error(`「${invalid.old}」的新名称不合法`);
    return;
  }
  const unchanged = list.filter((p) => p.name === p.old);
  if (unchanged.length === list.length) {
    MessagePlugin.error('新名称与原名称相同，无需重命名');
    return;
  }
  const targets = list.filter((p) => p.name !== p.old);
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
    selected.value = [];
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '批量重命名失败');
  } finally {
    batchDlg.busy = false;
  }
}

// ---- 压缩包管理 ----
const arcDlg = reactive({
  open: false,
  item: null,
  meta: null,
  items: [],
  inner: '/',
  pass: '',
  needPass: false,
  loading: false,
  decompressing: false,
  error: '',
});

function openArchive(it) {
  Object.assign(arcDlg, {
    open: true,
    item: it,
    meta: null,
    items: [],
    inner: '/',
    pass: '',
    needPass: false,
    loading: false,
    decompressing: false,
    error: '',
  });
  reloadArchive();
}

function reloadArchive() {
  arcLoadMeta();
  arcLoadList();
}

async function arcLoadMeta() {
  try {
    const d = await archiveMeta({
      space: space.value,
      path: arcDlg.item._rel,
      archivePass: arcDlg.pass,
    });
    arcDlg.meta = d || null;
    if (d && d.encrypted) {
      arcDlg.needPass = true;
    }
  } catch (e) {
    // 元信息失败不阻断列表（未加密包可能直接可列）
    arcDlg.meta = null;
    if (/密码|password|encrypt/i.test(e.message || '')) arcDlg.needPass = true;
  }
}

function arcRel(name) {
  return arcDlg.inner === '/' ? `/${name}` : `${arcDlg.inner}/${name}`;
}

const arcCrumbSegs = computed(() => {
  const segs = [];
  if (!arcDlg.inner || arcDlg.inner === '/') return segs;
  const parts = arcDlg.inner.split('/').filter(Boolean);
  let acc = '';
  parts.forEach((p) => {
    acc = acc ? `${acc}/${p}` : `/${p}`;
    segs.push({ name: p, path: acc });
  });
  return segs;
});

function arcNavTo(p) {
  arcDlg.inner = p || '/';
  arcLoadList();
}

async function arcLoadList() {
  arcDlg.loading = true;
  arcDlg.error = '';
  try {
    const d = await archiveList({
      space: space.value,
      path: arcDlg.item._rel,
      innerPath: arcDlg.inner,
      archivePass: arcDlg.pass,
    });
    arcDlg.items = (d && (d.content || d.items)) || [];
  } catch (e) {
    arcDlg.items = [];
    arcDlg.error = e.message || '包内列表加载失败';
    if (/密码|password|encrypt/i.test(e.message || '')) arcDlg.needPass = true;
  } finally {
    arcDlg.loading = false;
  }
}

async function downloadArchiveItem(it) {
  try {
    const res = await archiveDownload({
      space: space.value,
      path: arcDlg.item._rel,
      inner: arcRel(it.name),
      archivePass: arcDlg.pass,
    });
    await saveBlob(res, it.name);
  } catch (e) {
    MessagePlugin.error(e.message || '包内文件下载失败');
  }
}

async function decompressArchive() {
  if (searchKwActive.value) {
    MessagePlugin.info('搜索状态下请先返回目录浏览再解压');
    return;
  }
  arcDlg.decompressing = true;
  try {
    await archiveDecompress({
      srcSpace: space.value,
      srcDir: path.value,
      names: [arcDlg.item.name],
      dstSpace: space.value,
      dstDir: path.value,
      innerPath: arcDlg.inner,
      archivePass: arcDlg.pass,
    });
    MessagePlugin.success('已解压到当前目录');
    arcDlg.open = false;
    loadList(true);
  } catch (e) {
    MessagePlugin.error(e.message || '解压失败');
  } finally {
    arcDlg.decompressing = false;
  }
}
</script>

<style scoped>
/* 网盘页局部样式：沿用全局 token 与轻量白卡（card/subnav/dot-tag 见 styles.css） */
.nd-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.nd-toolbar .spacer {
  flex: 1;
}
.nd-spaces {
  display: flex;
  gap: 8px;
}
.nd-space-btn {
  height: 32px;
  padding: 0 14px;
  border-radius: 8px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-sub);
  font-size: 13px;
  cursor: pointer;
}
.nd-space-btn.on {
  background: var(--tk-blue-soft);
  border-color: transparent;
  color: var(--tk-brand);
  font-weight: 600;
}
.nd-space-team {
  margin-left: 6px;
  color: var(--tk-ph);
  font-weight: 400;
}
.nd-search {
  width: 200px;
}
.nd-crumb {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 10px;
  font-size: 13px;
}
.nd-crumb a {
  color: var(--tk-brand);
  text-decoration: none;
}
.nd-crumb-sep {
  color: var(--tk-dash);
}
.nd-crumb-cur {
  color: var(--tk-ink);
  font-weight: 600;
}
.nd-selbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  margin-bottom: 10px;
  background: var(--tk-blue-soft);
}
.nd-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.nd-table th {
  text-align: left;
  font-weight: 600;
  color: var(--tk-sub);
  padding: 8px 10px;
  border-bottom: 1px solid var(--tk-line);
}
.nd-table td {
  padding: 10px;
  border-bottom: 1px solid var(--tk-line);
}
.nd-table tr.checked td {
  background: var(--tk-blue-soft);
}
.nd-col-check {
  width: 34px;
}
.nd-col-size {
  width: 90px;
}
.nd-col-time {
  width: 130px;
}
.nd-col-code {
  width: 80px;
}
.nd-col-state {
  width: 90px;
}
.nd-col-op {
  width: 300px;
}
.nd-name {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.nd-ic {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  flex-shrink: 0;
}
.nd-ic.dir {
  background: var(--tk-blue-soft);
  color: var(--tk-brand);
}
.nd-ic.file {
  background: var(--tk-fill);
  color: var(--tk-sub);
}
.nd-nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nd-empty {
  text-align: center;
  padding: 32px 0;
}
.nd-up-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--tk-line);
}
.nd-up-row:last-child {
  border-bottom: none;
}
.nd-up-bar {
  height: 4px;
  margin-top: 6px;
  border-radius: 2px;
  background: var(--tk-fill);
  overflow: hidden;
}
.nd-up-bar i {
  display: block;
  height: 100%;
  background: var(--tk-brand);
  transition: width 0.2s var(--tk-ease);
}
.nd-up-bar i.fail {
  background: var(--tk-red);
}
/* 弹层：直角警示块除外，对话框为白卡 12px */
.nd-mask {
  position: fixed;
  inset: 0;
  z-index: 30;
  background: rgba(29, 33, 41, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
}
.nd-dialog {
  width: 400px;
  max-width: 92vw;
  max-height: 84vh;
  overflow: auto;
  background: #fff;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-hover);
  padding: 20px;
}
.nd-dialog-wide {
  width: 460px;
}
.nd-dialog h3 {
  margin: 0 0 12px;
  font-size: 16px;
}
.nd-dfoot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 16px;
}
.nd-dfoot .spacer {
  flex: 1;
}
.nd-share-files {
  max-height: 180px;
  overflow: auto;
  border: 1px solid var(--tk-line);
  padding: 8px 10px;
  margin-bottom: 12px;
}
.nd-share-file {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  padding: 4px 0;
  font-size: 13px;
}
.nd-kv {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 10px 0;
}
.nd-radios {
  display: flex;
  gap: 8px;
}
.nd-rd {
  height: 28px;
  padding: 0 12px;
  border-radius: 4px;
  border: 1px solid var(--tk-line);
  background: #fff;
  color: var(--tk-sub);
  font-size: 12px;
  cursor: pointer;
}
.nd-rd.on {
  background: var(--tk-blue-soft);
  border-color: var(--tk-brand);
  color: var(--tk-brand);
}
.nd-codebox {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 8px;
  background: var(--tk-fill);
  border-radius: 4px;
  font-size: 14px;
  font-weight: 600;
  letter-spacing: 2px;
}
.nd-link {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 6px 10px;
  background: var(--tk-fill);
  border-radius: 4px;
  font-size: 12px;
}
/* 列表缩略图 */
.nd-thumb {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  object-fit: cover;
  flex-shrink: 0;
  background: var(--tk-fill);
}
/* 移动/复制目标目录浏览 */
.nd-mvlist {
  max-height: 220px;
  overflow: auto;
  border: 1px solid var(--tk-line);
}
.nd-mvrow {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--tk-line);
  font-size: 13px;
  cursor: pointer;
}
.nd-mvrow:last-child {
  border-bottom: none;
}
.nd-mvrow:hover {
  background: var(--tk-fill);
}
.nd-mvrow .nd-nm {
  flex: 1;
}
.nd-short {
  width: 90px;
}
.nd-preview-list {
  max-height: 160px;
}
.nd-bad {
  color: var(--tk-red);
}
.nd-warn {
  margin: 8px 0 0;
  padding: 8px 10px;
  background: var(--tk-warn-soft, #fff7e8);
  border-left: 3px solid var(--tk-warn, #ff7d00);
  font-size: 12px;
  color: var(--tk-sub);
}
/* 图片预览 */
.nd-imgbox {
  max-width: min(860px, 92vw);
  max-height: 84vh;
  background: #fff;
  border-radius: 12px;
  box-shadow: var(--tk-shadow-hover);
  padding: 12px;
  display: flex;
  flex-direction: column;
}
.nd-imgbox img {
  max-width: 100%;
  max-height: 68vh;
  object-fit: contain;
  border: 1px solid var(--tk-line);
}
.nd-imgbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-top: 10px;
}
.nd-imgbar .nd-nm {
  font-weight: 600;
}
.nd-imgbar .spacer {
  flex: 1;
}
</style>
