(function () {
  'use strict';

  var ALLOWED = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];
  var POLL_INTERVAL = 5000;
  var files = [];

  var dz = document.getElementById('dropzone');
  var fileInput = document.getElementById('fileInput');
  var tagsWrap = document.getElementById('tags');
  var genBtn = document.getElementById('genBtn');
  var dateInput = document.getElementById('dateInput');
  var datePreview = document.getElementById('datePreview');
  var overlay = document.getElementById('overlay');
  var nameInput = document.getElementById('nameInput');
  var cancelBtn = document.getElementById('cancelBtn');
  var confirmBtn = document.getElementById('confirmBtn');
  var recordList = document.getElementById('recordList');
  var emptyTip = document.getElementById('emptyTip');
  var toastEl = document.getElementById('toast');
  var toastTimer = null;

  /* 记录列表状态：按 id 跟踪行元素与上一次状态，用于增量渲染与动画触发 */
  var rowEls = {};       // id -> 行 DOM 元素
  var knownStatus = {};  // id -> 上一次渲染时的 status
  var firstRender = true;
  var pollTimer = null;
  var submitting = false;

  var DOC_ICON_SVG =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/>' +
    '<path d="M14 3v5h5"/><path d="M9 13h6M9 17h4"/></svg>';

  function checkSvg(animate) {
    return '<svg class="check-svg" viewBox="0 0 24 24" aria-hidden="true">' +
      '<circle cx="12" cy="12" r="10" fill="#ecfdf5" stroke="#059669" stroke-width="1.6"/>' +
      '<path class="check-path' + (animate ? ' animate' : '') + '" d="M8 12.4l2.6 2.6L16.2 9.4" fill="none" stroke="#059669" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  }

  var TRASH_SVG =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/>' +
    '<path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg>';

  function delBtnHtml(rec) {
    return '<button class="btn-del" type="button" data-id="' + escapeHtml(rec.id) + '" title="删除记录及生成的文件" aria-label="删除记录">' + TRASH_SVG + '</button>';
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function extOf(name) { return name.split('.').pop().toLowerCase(); }
  function baseOf(name) { return name.replace(/\.[^.]+$/, ''); }
  function toDots(iso) { return iso.replace(/-/g, '.'); }

  function showToast(msg) {
    toastEl.textContent = msg;
    toastEl.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { toastEl.classList.remove('show'); }, 2400);
  }

  /* ---------- API ---------- */
  function parseJson(res) {
    /* 会话失效：统一跳登录页（相对路径，兼容反代前缀部署） */
    if (res.status === 401) {
      location.href = 'login.html';
      throw new Error('登录已过期');
    }
    return res.json().catch(function () {
      throw new Error('服务器响应异常（' + res.status + '）');
    }).then(function (data) {
      if (!res.ok || !data || data.ok === false) {
        throw new Error((data && data.error) || ('请求失败（' + res.status + '）'));
      }
      return data;
    });
  }

  function fetchRecords() {
    return fetch('api/records', { headers: { 'Accept': 'application/json' } })
      .then(parseJson)
      .then(function (data) { return data.records || []; });
  }

  /* ---------- 轮询 ---------- */
  function stopPolling() {
    if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
  }
  function schedulePoll() {
    stopPolling();
    pollTimer = setTimeout(function () { refreshRecords(false); }, POLL_INTERVAL);
  }
  function hasProcessing(records) {
    return records.some(function (r) { return r.status === 'processing'; });
  }

  function refreshRecords(isInitial) {
    return fetchRecords().then(function (records) {
      renderRecords(records, isInitial);
      /* 存在 processing 记录才继续轮询，全部终态则停止 */
      if (hasProcessing(records)) { schedulePoll(); } else { stopPolling(); }
    }).catch(function (err) {
      showToast(err.message || '记录列表加载失败');
      /* 轮询中出错：若之前仍有处理中记录，保持轮询以免状态卡死 */
      if (!isInitial && Object.keys(knownStatus).some(function (id) { return knownStatus[id] === 'processing'; })) {
        schedulePoll();
      }
    });
  }

  /* ---------- 记录行渲染 ---------- */
  function statusHtml(rec, entering) {
    var enter = entering ? ' entering' : '';
    if (rec.status === 'done') {
      return '<span class="status status-done' + enter + '">' +
        '<span class="done-pill">' + checkSvg(entering) + '<span>已完成</span></span>' +
        '<button class="btn-dl" type="button" data-id="' + escapeHtml(rec.id) + '">下载</button>' +
        delBtnHtml(rec) +
        '</span>';
    }
    if (rec.status === 'failed') {
      return '<span class="status status-fail' + enter + '" title="' + escapeHtml(rec.error || '生成失败') + '">' +
        '<span>生成失败</span>' + delBtnHtml(rec) + '</span>';
    }
    return '<span class="status status-gen"><span class="spinner"></span><span class="gen-text">生成中</span>' + delBtnHtml(rec) + '</span>';
  }

  function swapStatus(row, rec, animate) {
    var statusBox = row.querySelector('.rec-status');
    var current = statusBox.firstElementChild;
    function apply() {
      statusBox.innerHTML = statusHtml(rec, animate);
      if (animate && rec.status === 'done') { row.classList.add('flash'); }
    }
    if (animate && current) {
      current.classList.add('leaving');
      setTimeout(apply, 260);
    } else {
      apply();
    }
  }

  function createRow(rec, animate) {
    var row = document.createElement('div');
    row.className = 'record' + (animate ? ' new' : '');
    row.setAttribute('data-id', rec.id);
    row.innerHTML =
      '<span class="rec-icon">' + DOC_ICON_SVG + '</span>' +
      '<div class="rec-main">' +
      '<div class="rec-title"></div>' +
      '<div class="rec-sub"></div>' +
      '</div>' +
      '<div class="rec-status">' + statusHtml(rec, false) + '</div>';
    updateRowText(row, rec);
    return row;
  }

  function updateRowText(row, rec) {
    row.querySelector('.rec-title').textContent = rec.name;
    row.querySelector('.rec-sub').textContent =
      '记录文件：' + rec.fileName + ' · ' + rec.sourceCount + ' 个源文件';
  }

  function renderRecords(records, isInitial) {
    var seen = {};
    records.forEach(function (rec) {
      var id = String(rec.id);
      seen[id] = true;
      var row = rowEls[id];
      if (!row) {
        /* 新记录：首屏静默渲染，之后出现的行滑入 */
        row = createRow(rec, !isInitial);
        rowEls[id] = row;
        recordList.appendChild(row);
        if (!isInitial) { row.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); }
      } else {
        updateRowText(row, rec);
        /* 按 id 对比前后状态，状态变化时播放过渡动画 */
        if (knownStatus[id] !== rec.status) {
          swapStatus(row, rec, knownStatus[id] === 'processing');
        }
      }
      knownStatus[id] = rec.status;
    });
    /* 移除已不存在的记录 */
    Object.keys(rowEls).forEach(function (id) {
      if (!seen[id]) {
        rowEls[id].remove();
        delete rowEls[id];
        delete knownStatus[id];
      }
    });
    /* 按后端返回顺序重排（appendChild 移动已有节点，不重建 DOM，动画不重置） */
    records.forEach(function (rec) { recordList.appendChild(rowEls[String(rec.id)]); });
    emptyTip.style.display = records.length ? 'none' : '';
    firstRender = false;
  }

  /* 下载/删除按钮：事件委托 */
  recordList.addEventListener('click', function (e) {
    var dl = e.target.closest ? e.target.closest('.btn-dl') : null;
    if (dl && recordList.contains(dl)) {
      location.href = 'api/records/' + encodeURIComponent(dl.getAttribute('data-id')) + '/download';
      return;
    }
    var del = e.target.closest ? e.target.closest('.btn-del') : null;
    if (del && recordList.contains(del)) {
      if (!window.confirm('确定删除这条记录吗？已生成的记录文件将一并删除。')) { return; }
      fetch('api/records/' + encodeURIComponent(del.getAttribute('data-id')), { method: 'DELETE' })
        .then(parseJson)
        .then(function () {
          showToast('记录已删除');
          refreshRecords(false);
        })
        .catch(function (err) { showToast(err.message || '删除失败'); });
    }
  });

  /* ---------- 文件选择与标签 ---------- */
  function isPdf(f) { return extOf(f.name) === 'pdf'; }

  function addFiles(fileList) {
    var rejected = 0;
    var incoming = [];
    Array.prototype.forEach.call(fileList, function (f) {
      if (ALLOWED.indexOf(extOf(f.name)) === -1) { rejected++; return; }
      if (files.some(function (x) { return x.name === f.name; })) { return; }
      if (incoming.some(function (x) { return x.name === f.name; })) { return; }
      incoming.push(f);
    });
    var merged = files.concat(incoming);
    /* 多文件时须全部为 PDF，否则整批阻止 */
    if (merged.length >= 2 && merged.some(function (f) { return !isPdf(f); })) {
      showToast('多文件合并仅支持 PDF，请先转换为 PDF 或逐个生成');
      return;
    }
    files = merged;
    if (rejected > 0) { showToast('已忽略 ' + rejected + ' 个不支持的文件'); }
    renderTags();
  }

  function renderTags() {
    tagsWrap.innerHTML = '';
    files.forEach(function (f, i) {
      var tag = document.createElement('span');
      tag.className = 'tag';
      tag.innerHTML =
        '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
        '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>' +
        '<span class="tag-name">' + escapeHtml(f.name) + '</span>';
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'tag-x';
      x.setAttribute('aria-label', '移除 ' + f.name);
      x.innerHTML = '&times;';
      x.addEventListener('click', function () {
        files.splice(i, 1);
        renderTags();
      });
      tag.appendChild(x);
      tagsWrap.appendChild(tag);
    });
    genBtn.disabled = files.length === 0;
  }

  function clearFiles() {
    files = [];
    renderTags();
  }

  dz.addEventListener('click', function () { fileInput.click(); });
  dz.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileInput.click(); }
  });
  fileInput.addEventListener('change', function () {
    addFiles(fileInput.files);
    fileInput.value = '';
  });

  var dragDepth = 0;
  dz.addEventListener('dragenter', function (e) {
    e.preventDefault();
    dragDepth++;
    dz.classList.add('dragover');
  });
  dz.addEventListener('dragover', function (e) { e.preventDefault(); });
  dz.addEventListener('dragleave', function () {
    dragDepth--;
    if (dragDepth <= 0) { dragDepth = 0; dz.classList.remove('dragover'); }
  });
  dz.addEventListener('drop', function (e) {
    e.preventDefault();
    dragDepth = 0;
    dz.classList.remove('dragover');
    if (e.dataTransfer && e.dataTransfer.files) { addFiles(e.dataTransfer.files); }
  });

  /* ---------- 日期 ---------- */
  function todayISO() {
    var d = new Date();
    var m = String(d.getMonth() + 1).padStart(2, '0');
    var day = String(d.getDate()).padStart(2, '0');
    return d.getFullYear() + '-' + m + '-' + day;
  }
  function syncDatePreview() {
    datePreview.textContent = dateInput.value ? ('提交格式：' + toDots(dateInput.value)) : '提交格式：—';
  }
  dateInput.value = todayISO();
  syncDatePreview();
  dateInput.addEventListener('input', syncDatePreview);

  /* ---------- 弹窗 ---------- */
  function openModal() {
    var names = files.map(function (f) { return baseOf(f.name); }).join('、');
    nameInput.value = '《' + names + '》';
    overlay.classList.add('open');
    document.body.style.overflow = 'hidden';
    setTimeout(function () { nameInput.focus(); nameInput.select(); }, 260);
  }
  function closeModal() {
    if (submitting) { return; }
    overlay.classList.remove('open');
    document.body.style.overflow = '';
  }
  function forceCloseModal() {
    overlay.classList.remove('open');
    document.body.style.overflow = '';
  }
  var modalX = document.getElementById('modalX');
  genBtn.addEventListener('click', openModal);
  cancelBtn.addEventListener('click', closeModal);
  modalX.addEventListener('click', closeModal);
  /* 不绑定遮罩点击关闭：防止误点空白处丢失已编辑内容 */
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && overlay.classList.contains('open')) { closeModal(); }
  });

  /* ---------- 生成记录 ---------- */
  function setSubmitting(on) {
    submitting = on;
    confirmBtn.disabled = on;
    cancelBtn.disabled = on;
    confirmBtn.textContent = on ? '正在生成…' : '确定，开始生成';
  }

  confirmBtn.addEventListener('click', function () {
    if (submitting) { return; }
    var name = nameInput.value.trim();
    if (!name) {
      showToast('请输入记录名称');
      nameInput.focus();
      return;
    }
    if (files.length === 0) {
      showToast('请先选择文件');
      return;
    }
    /* 提交前再校验一次多文件 PDF 规则 */
    if (files.length >= 2 && files.some(function (f) { return !isPdf(f); })) {
      showToast('多文件合并仅支持 PDF，请先转换为 PDF 或逐个生成');
      return;
    }
    var dateDots = toDots(dateInput.value || todayISO());

    var fd = new FormData();
    fd.append('name', name);
    fd.append('date', dateDots);
    files.forEach(function (f) { fd.append('files', f, f.name); });

    setSubmitting(true);
    fetch('api/generate', { method: 'POST', body: fd })
      .then(parseJson)
      .then(function () {
        forceCloseModal();
        clearFiles();
        showToast('已开始生成，请稍候…');
        /* 立即刷新列表；新记录为 processing，refresh 内部会恢复轮询 */
        refreshRecords(false);
      })
      .catch(function (err) {
        showToast(err.message || '生成请求失败');
      })
      .then(function () { setSubmitting(false); });
  });

  /* ---------- 当前用户与退出登录 ---------- */
  var userChip = document.getElementById('userChip');
  var userName = document.getElementById('userName');
  var logoutBtn = document.getElementById('logoutBtn');

  fetch('api/me', { headers: { 'Accept': 'application/json' } })
    .then(parseJson)
    .then(function (data) {
      userName.textContent = (data.user && (data.user.nickname || data.user.username)) || '';
      userChip.style.display = '';
    })
    .catch(function () { /* 401 时 parseJson 已跳转登录页；其他异常忽略 */ });

  logoutBtn.addEventListener('click', function () {
    fetch('api/logout', { method: 'POST' })
      .then(parseJson)
      .then(function () { location.href = 'login.html'; })
      .catch(function (err) { showToast(err.message || '退出失败'); });
  });

  /* ---------- 初始化 ---------- */
  refreshRecords(true);
})();
