/* ============================================================
   Shade 壹匣 · 网页版「保存到网盘」目录选择器（我的空间 / 公共区）
   依赖 assets/icons.js 与 assets/common.js 先行加载（Shade.icon / Shade.api / Shade.toast / Shade.esc）

   API：window.ShadeNdPick.pickDir(opts) → Promise<{space, dir}|null>
     opts: { defaultPath, title, confirmText }
       defaultPath：打开时默认定位的「我的空间」内相对路径（如 '出工日志'；目录不存在自动回退根目录）
       title：弹层标题，缺省「保存到网盘：选择目录」；confirmText：确认按钮文案，缺省「确定」
     resolve({ space, dir })：用户确认；space='my' 我的空间 / 'public' 公共区（按班组隔离，
       未分配班组时分段隐藏仅我的空间）；dir 不带首尾斜杠，根目录为 ''（如 '出工日志/2026-10'）
     resolve(null)：用户取消（点遮罩 / Esc / 取消按钮 / 右上角 X）——取消不 reject

   交互为 netdisk.html pickTargetDir 的简化版：空间分段、面包屑回跳、
   只列文件夹、单击选中（选中即以其为目标子目录）/双击进入、就地新建文件夹
   （POST /api/v1/netdisk/mkdir {space, path: 当前目录, name}；公共区全员可建，与网盘主页同口径）。
   弹层外壳样式自包含（.ndp-* 注入一次）：worklog/quiz 等页的 .mask/.modal 口径各页自带，
   safeday 页无 .modal 样式，故本组件不复用 Shade.modal；按钮/输入框/空态/加载沿用 theme.css 全局 class。
   ============================================================ */
(function () {
  if (window.ShadeNdPick) return;

  var CSS = ''
    + '.ndp-mask { position: fixed; inset: 0; z-index: 400; padding: 24px;'
    + '  background: rgba(29, 33, 41, .5); backdrop-filter: blur(3px);'
    + '  display: flex; align-items: center; justify-content: center; opacity: 0; transition: opacity .3s; }'
    + '.ndp-mask.open { opacity: 1; }'
    + '.ndp-modal { background: #fff; border: 1px solid var(--line); border-radius: 8px;'
    + '  width: 520px; max-width: 94vw; max-height: 86vh; overflow: hidden; padding: 6px;'
    + '  display: flex; flex-direction: column; box-shadow: 0 2px 8px rgba(0, 0, 0, .05);'
    + '  transform: translateY(16px) scale(.98); transition: transform .35s var(--ease); }'
    + '.ndp-mask.open .ndp-modal { transform: none; }'
    + '.ndp-scroll { flex: 1; min-height: 0; overflow-y: auto; border-radius: 8px; padding: 14px 18px 16px;'
    + '  scrollbar-gutter: stable both-edges; scrollbar-width: thin; scrollbar-color: rgba(29, 33, 41, .2) transparent; }'
    + '.ndp-scroll::-webkit-scrollbar { width: 5px; }'
    + '.ndp-scroll::-webkit-scrollbar-thumb { background: rgba(29, 33, 41, .16); border-radius: 8px; }'
    + '.ndp-scroll::-webkit-scrollbar-thumb:hover { background: rgba(29, 33, 41, .34); }'
    + '.ndp-scroll::-webkit-scrollbar-track { background: transparent; }'
    + '.ndp-head { display: flex; align-items: center; gap: 10px; }'
    + '.ndp-title { font-size: 16px; font-weight: 700; letter-spacing: 1px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }'
    + '.ndp-x { margin-left: auto; width: 30px; height: 30px; border: 1px solid var(--line); border-radius: 8px; background: #fff;'
    + '  color: var(--navy); cursor: pointer; display: flex; align-items: center; justify-content: center;'
    + '  box-shadow: 0 2px 8px rgba(0, 0, 0, .05); transition: background .15s, color .15s; }'
    + '.ndp-x:hover { background: var(--navy); color: #fff; }'
    + '.ndp-x svg { width: 14px; height: 14px; }'
    + '.ndp-spaces { display: flex; gap: 4px; margin-top: 12px; padding: 3px; background: var(--fill); border-radius: 8px; }'
    + '.ndp-space { flex: 1; height: 30px; border-radius: 6px; display: flex; align-items: center; justify-content: center;'
    + '  font-size: 12.5px; font-weight: 600; color: var(--sub); cursor: pointer; transition: background .15s, color .15s; }'
    + '.ndp-space.on { background: #fff; color: var(--orange); box-shadow: 0 1px 4px rgba(0, 0, 0, .06); }'
    + '.ndp-crumb { display: flex; align-items: center; gap: 7px; font-size: 13px; min-width: 0; flex-wrap: wrap; margin-top: 12px; }'
    + '.ndp-crumb a { color: var(--orange); font-weight: 600; cursor: pointer; }'
    + '.ndp-crumb a:hover { filter: brightness(.85); }'
    + '.ndp-crumb em { font-style: normal; color: var(--ph); }'
    + '.ndp-crumb b { font-weight: 600; word-break: break-all; }'
    + '.ndp-list { margin-top: 10px; border: 1px solid var(--line); border-radius: 8px;'
    + '  min-height: 180px; max-height: 300px; overflow-y: auto; }'
    + '.ndp-row { display: flex; align-items: center; gap: 9px; padding: 8px 12px; font-size: 12.5px; cursor: pointer; }'
    + '.ndp-row:hover { background: var(--fill); }'
    + '.ndp-row.on { background: var(--orange-soft); font-weight: 600; }'
    + '.ndp-row svg { flex: none; color: var(--navy); }'
    + '.ndp-nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }'
    + '.ndp-empty { padding: 36px 20px; }'
    + '.ndp-state { padding: 30px 20px; text-align: center; font-size: 12.5px; color: var(--sub); }'
    + '.ndp-mkdirrow { display: flex; align-items: center; gap: 8px; margin-top: 10px; }'
    + '.ndp-mkdirrow[hidden] { display: none; }'
    + '.ndp-mkdirrow .input { height: 34px; font-size: 12.5px; }'
    + '.ndp-foot { display: flex; justify-content: flex-end; align-items: center; gap: 10px; margin-top: 18px; }'
    + '.ndp-mkdir { margin-right: auto; display: inline-flex; align-items: center; gap: 5px;'
    + '  font-size: 12.5px; font-weight: 600; color: var(--orange); cursor: pointer; }'
    + '.ndp-mkdir:hover { filter: brightness(.85); }'
    + '.ndp-mkdir svg { width: 14px; height: 14px; }'
    + '@media (prefers-reduced-motion: reduce) { .ndp-mask, .ndp-modal { transition: none; } }';

  var cssDone = false;
  function injectCss() {
    if (cssDone) return;
    cssDone = true;
    var st = document.createElement('style');
    st.setAttribute('data-ndpick', '');
    st.textContent = CSS;
    document.head.appendChild(st);
  }

  function pickDir(opts) {
    opts = opts || {};
    injectCss();
    return new Promise(function (resolve) {
      var esc = Shade.esc;
      var done = false;
      function finish(v) { if (!done) { done = true; cleanup(); resolve(v); } }

      // 内部目录口径带前导斜杠（'/' 根目录），与 /list、/mkdir 接口一致；对外 resolve 时不带首尾斜杠
      var def = String(opts.defaultPath || '').trim().replace(/^\/+|\/+$/g, '');
      var pk = {
        space: 'my',
        spaces: [{ key: 'my', name: '我的空间' }],
        dir: def ? '/' + def : '/',
        selName: null,
        items: [],
      };

      var mask = document.createElement('div');
      mask.className = 'ndp-mask';
      mask.innerHTML = '<div class="ndp-modal"><div class="ndp-scroll">'
        + '<div class="ndp-head"><span class="ndp-title">' + esc(opts.title || '保存到网盘：选择目录') + '</span>'
        + '<button type="button" class="ndp-x">' + Shade.icon('close', 14) + '</button></div>'
        + '<div class="ndp-spaces" hidden></div>'
        + '<div class="ndp-crumb"></div>'
        + '<div class="ndp-list"></div>'
        + '<div class="ndp-mkdirrow" hidden>'
        + '<input class="input ndp-mkdir-in" type="text" maxlength="200" placeholder="文件夹名称" autocomplete="off">'
        + '<button type="button" class="btn btn-sm btn-primary" data-mk="yes">创建</button>'
        + '<button type="button" class="btn btn-sm" data-mk="no">取消</button></div>'
        + '<div class="ndp-foot"><span class="ndp-mkdir">' + Shade.icon('folder-add', 14) + '新建文件夹</span>'
        + '<button type="button" class="btn btn-sm" data-a="no">取消</button>'
        + '<button type="button" class="btn btn-sm btn-primary" data-a="yes">' + esc(opts.confirmText || '确定') + '</button></div>'
        + '</div></div>';
      document.body.appendChild(mask);
      requestAnimationFrame(function () { mask.classList.add('open'); });

      var spacesEl = mask.querySelector('.ndp-spaces');
      var crumbEl = mask.querySelector('.ndp-crumb');
      var listEl = mask.querySelector('.ndp-list');
      var mkdirRow = mask.querySelector('.ndp-mkdirrow');
      var mkdirIn = mask.querySelector('.ndp-mkdir-in');

      function cleanup() {
        document.removeEventListener('keydown', onKey, true);
        mask.classList.remove('open');
        setTimeout(function () { mask.remove(); }, 300);
      }
      // Esc 捕获阶段拦截并停传播：只关本弹层，不连带关页面既有弹层（common.js 的 Esc 监听在冒泡段）
      function onKey(e) { if (e.key === 'Escape') { e.stopPropagation(); finish(null); } }
      document.addEventListener('keydown', onKey, true);
      mask.addEventListener('mousedown', function (e) { if (e.target === mask) finish(null); });
      mask.querySelector('.ndp-x').addEventListener('click', function () { finish(null); });
      mask.querySelector('[data-a="no"]').addEventListener('click', function () { finish(null); });

      function joinRel(dir, name) { return (dir === '/' ? '' : dir) + '/' + name; }
      // 目标 = 选中文件夹（单击选中）或当前浏览目录；dir 不带首尾斜杠，根目录为 ''
      function targetRel() {
        return (pk.selName ? joinRel(pk.dir, pk.selName) : pk.dir).replace(/^\/+/, '');
      }
      function spaceName() {
        var s = pk.spaces.filter(function (x) { return x.key === pk.space; })[0];
        return (s && s.name) || '我的空间';
      }
      function paintSpaces() {
        if (pk.spaces.length < 2) { spacesEl.hidden = true; return; }
        spacesEl.hidden = false;
        spacesEl.innerHTML = pk.spaces.map(function (s) {
          return '<span class="ndp-space' + (pk.space === s.key ? ' on' : '') + '" data-key="' + esc(s.key) + '">' + esc(s.name) + '</span>';
        }).join('');
      }
      function paintCrumb() {
        var parts = pk.dir.split('/').filter(Boolean);
        var html = '<a data-path="/">' + esc(spaceName()) + '</a>';
        var acc = '';
        parts.forEach(function (p, i) {
          acc += '/' + p;
          html += '<em>/</em>' + (i === parts.length - 1
            ? '<b>' + esc(p) + '</b>'
            : '<a data-path="' + esc(acc) + '">' + esc(p) + '</a>');
        });
        crumbEl.innerHTML = html;
      }
      function paintRows() {
        if (!pk.items.length) {
          listEl.innerHTML = '<div class="empty ndp-empty">' + Shade.icon('folder', 40) + '<p>此目录下没有文件夹</p></div>';
          return;
        }
        listEl.innerHTML = pk.items.map(function (x) {
          return '<div class="ndp-row' + (pk.selName === x.name ? ' on' : '') + '" data-name="' + esc(x.name) + '">'
            + Shade.icon('folder', 20) + '<span class="ndp-nm">' + esc(x.name) + '</span></div>';
        }).join('');
      }
      function loadDirs(isInitial) {
        listEl.innerHTML = '<div class="loading"><span class="spin"></span>加载中…</div>';
        Shade.api('/api/v1/netdisk/list', { method: 'POST', body: { space: pk.space, path: pk.dir } })
          .then(function (r) {
            var dirs = ((r.data && r.data.items) || []).filter(function (x) { return x.is_dir; });
            dirs.sort(function (a, b) { return String(a.name).localeCompare(String(b.name), 'zh'); });
            pk.items = dirs;
            pk.selName = null;
            paintRows();
          })
          .catch(function (e) {
            // 默认定位目录不存在（如从未保存过）等：回退根目录重载
            if (isInitial && pk.dir !== '/') { pk.dir = '/'; paintCrumb(); loadDirs(false); return; }
            listEl.innerHTML = '<div class="ndp-state">' + esc(e.message || '加载失败')
              + ' <button type="button" class="btn btn-sm" data-retry="1" style="margin-left:10px">重试</button></div>';
          });
      }

      // 空间探测：公共区按班组隔离（未分配班组不下发，分段保持隐藏）；探测失败静默仅我的空间
      Shade.api('/api/v1/netdisk/spaces')
        .then(function (r) {
          var list = ((r.data && r.data.spaces) || [])
            .filter(function (s) { return s && (s.key === 'my' || s.key === 'public'); })
            .map(function (s) { return { key: s.key, name: s.name || (s.key === 'public' ? '公共区' : '我的空间') }; });
          if (list.length) { pk.spaces = list; paintSpaces(); paintCrumb(); }
        })
        .catch(function () { /* 仅我的空间 */ });

      spacesEl.addEventListener('click', function (e) {
        var seg = e.target.closest('.ndp-space');
        if (!seg || seg.dataset.key === pk.space) return;
        pk.space = seg.dataset.key;
        pk.dir = '/';
        pk.selName = null;
        mkdirRow.hidden = true;
        paintSpaces(); paintCrumb(); loadDirs(false);
      });
      crumbEl.addEventListener('click', function (e) {
        var a = e.target.closest('a[data-path]');
        if (!a || a.dataset.path === pk.dir) return;
        pk.dir = a.dataset.path;
        paintCrumb(); loadDirs(false);
      });
      listEl.addEventListener('click', function (e) {
        if (e.target.closest('[data-retry]')) { loadDirs(false); return; }
        var row = e.target.closest('.ndp-row');
        if (!row) return;
        // 单击选中/再点取消选中（目标回退为当前浏览目录）
        pk.selName = pk.selName === row.dataset.name ? null : row.dataset.name;
        paintRows();
      });
      listEl.addEventListener('dblclick', function (e) {
        var row = e.target.closest('.ndp-row');
        if (!row) return;
        pk.dir = joinRel(pk.dir, row.dataset.name);
        pk.selName = null;
        paintCrumb(); loadDirs(false);
      });

      // 就地新建文件夹（内联输入行；Enter 提交，Esc 收起走弹层统一 Esc）
      mask.querySelector('.ndp-mkdir').addEventListener('click', function () {
        mkdirRow.hidden = false;
        mkdirIn.value = '';
        mkdirIn.focus();
      });
      mkdirRow.querySelector('[data-mk="no"]').addEventListener('click', function () { mkdirRow.hidden = true; });
      function submitMkdir() {
        var name = mkdirIn.value.trim();
        if (!name) { Shade.toast('请输入文件夹名称', 'error'); return; }
        Shade.api('/api/v1/netdisk/mkdir', { method: 'POST', body: { space: pk.space, path: pk.dir, name: name } })
          .then(function () {
            Shade.toast('已创建', 'success');
            mkdirRow.hidden = true;
            loadDirs(false);
          })
          .catch(function (e) { Shade.toast(e.message, 'error'); });
      }
      mkdirRow.querySelector('[data-mk="yes"]').addEventListener('click', submitMkdir);
      mkdirIn.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' && !e.isComposing) submitMkdir();
      });

      mask.querySelector('[data-a="yes"]').addEventListener('click', function () {
        finish({ space: pk.space, dir: targetRel() });
      });

      paintSpaces();
      paintCrumb();
      loadDirs(true);
    });
  }

  window.ShadeNdPick = { pickDir: pickDir };
})();
