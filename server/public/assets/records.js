/**
 * 树障 / 交跨回退页共享模块：暂存区、冲突对照与导出弹层的单套实现。
 *
 * 两个回退页（treehazard.html / crossing.html）的这三块逻辑原本逐行重复；
 * 本文件按台账配置参数化：API 前缀、业务字段表、日期列、长文本列、数字等值列、
 * 是否渲染处理状态行与导出文件名。页面只保留记录卡片、看板、表单等各自业务。
 *
 * 用法：<script src="/assets/records.js"></script> 后
 *   var panels = ShadeRecords.createPanels({ ... });
 *   loadConflicts / renderConflicts / ... 取 panels 对应方法。
 */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.ShadeRecords = factory();
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  function $(sel, scope) {
    return (scope || document).querySelector(sel);
  }
  function $$(sel, scope) {
    return Array.prototype.slice.call((scope || document).querySelectorAll(sel));
  }

  // sameVal 单元格等值判定（日期已归一 yyyy-MM-dd；数字列「3」与「3.0」视为相同）。
  function sameVal(key, a, b, numericKeys) {
    var sa = String(a == null ? '' : a).trim();
    var sb = String(b == null ? '' : b).trim();
    if (sa === sb) return true;
    if ((numericKeys || []).indexOf(key) >= 0 && sa && sb) {
      var na = Number(sa);
      var nb = Number(sb);
      if (!isNaN(na) && !isNaN(nb)) return na === nb;
    }
    return false;
  }

  // cftServerVal 系统当前值：冲突行平铺字段（记录已删时回退服务端快照）。
  function cftServerVal(c, key) {
    var v = c[key];
    if ((v === null || v === undefined) && c.server_snapshot) v = c.server_snapshot[key];
    return v;
  }

  // cftRows 冲突对照行：字段表 + 可选状态行；server/intranet 已格式化。
  function cftRows(c, cfg) {
    var snap = c.intranet_snapshot || {};
    var rows = (cfg.fields || []).map(function (f) {
      var key = f[0];
      var label = f[1];
      var sv = cftServerVal(c, key);
      var iv = snap[key];
      return {
        key: key,
        label: label,
        diff: !sameVal(key, sv, iv, cfg.numericKeys),
        server: key === 'voltage_level' ? cfg.fmtKv(sv) : cfg.dash(sv),
        intranet: key === 'voltage_level' ? cfg.fmtKv(iv) : cfg.dash(iv),
      };
    });
    if (cfg.showStatus) {
      var sStatus =
        c.record_status || (c.server_snapshot && c.server_snapshot.status) || 'unhandled';
      var iStatus = String(snap.handled_at || '').trim() ? 'handled' : 'unhandled';
      var stDiff = sStatus !== iStatus;
      rows.push({
        key: 'status',
        label: '状态',
        diff: stDiff,
        server: sStatus === 'handled' ? '已处理' : '未处理',
        intranet: iStatus === 'handled' ? '已处理' : '未处理',
      });
    }
    return rows;
  }

  function createPanels(cfg) {
    var apiBase = cfg.apiBase;
    var state = cfg.state;
    var fields = cfg.fields;
    var dateFields = cfg.dateFields || [];
    var noteField = cfg.noteField || '';
    var numericKeys = cfg.numericKeys || [];
    var showStatus = !!cfg.showStatus;
    var rowOpts = { fields: fields, numericKeys: numericKeys, showStatus: showStatus };

    function path(p) {
      return cfg.path(apiBase + p);
    }

    /* ================= 暂存区 ================= */
    async function loadStaging() {
      var body = $('#stgBody');
      state.stg.detail = null;
      body.innerHTML = '<div class="loading"><span class="spin"></span>加载中…</div>';
      try {
        var r = await cfg.api(path('/staging/batches'));
        state.stg.list = r.data || [];
        state.stg.err = '';
      } catch (e) {
        state.stg.list = [];
        state.stg.err = e.message || '加载失败';
      }
      renderStagingList();
    }

    /* 批次卡列表：群名 / 推送人 / 时间 / 条数 / 审核备注（浅底 + 左色条警示块） */
    function renderStagingList() {
      var body = $('#stgBody');
      if (state.stg.err) {
        body.innerHTML =
          '<div class="empty">' +
          cfg.icon('warn', 44) +
          '<p>' +
          cfg.esc(state.stg.err) +
          '</p>' +
          '<p><button class="btn btn-sm" data-stg="reload">重试</button></p></div>';
        return;
      }
      if (!state.stg.list.length) {
        body.innerHTML =
          '<div class="empty">' + cfg.icon('folder', 44) + '<p>暂无待导入批次</p></div>';
        return;
      }
      body.innerHTML = state.stg.list
        .map(function (b) {
          return (
            '<div class="card stg-card" data-batch="' +
            b.id +
            '">' +
            '<div class="stg-top"><b>' +
            cfg.esc(cfg.dash(b.group_name)) +
            '</b>' +
            '<span class="stg-meta">推送人 ' +
            cfg.esc(cfg.dash(b.pusher_name)) +
            ' · ' +
            cfg.esc(cfg.fmtDate(b.created_at, true)) +
            ' · ' +
            (b.record_count || 0) +
            ' 条</span>' +
            '<span class="stg-go">审核 ›</span></div>' +
            (String(b.review_note || '').trim()
              ? '<div class="stg-note"><b>审核备注：</b>' + cfg.esc(b.review_note) + '</div>'
              : '') +
            '</div>'
          );
        })
        .join('');
    }

    /* 批次详情：进入即拉取（业务列 inline 编辑）；silent=true 时 404（批次已转空）静默回列表 */
    async function openBatch(id, silent) {
      var body = $('#stgBody');
      if (!silent) body.innerHTML = '<div class="loading"><span class="spin"></span>加载中…</div>';
      try {
        var r = await cfg.api(path('/staging/batches/' + id));
        state.stg.detail = r.data || null;
        state.stgSel = {};
        renderBatchDetail();
      } catch (e) {
        state.stg.detail = null;
        if (silent) {
          loadStaging();
          return;
        }
        cfg.toast(e.message, 'error');
        renderStagingList();
      }
    }

    /* 详情行内输入单元格（长字段加宽；日期列用原生日期控件） */
    function stgCell(r, key) {
      var cls = key === 'line_name' ? ' w-line' : key === noteField ? ' w-note' : '';
      var v = r[key] == null ? '' : String(r[key]);
      if (dateFields.indexOf(key) >= 0) {
        return (
          '<td><input type="date" class="stg-in" data-f="' +
          key +
          '" value="' +
          cfg.esc(v) +
          '"></td>'
        );
      }
      return (
        '<td><input class="stg-in' +
        cls +
        '" data-f="' +
        key +
        '" maxlength="64" value="' +
        cfg.esc(v) +
        '"></td>'
      );
    }

    function renderBatchDetail() {
      var body = $('#stgBody');
      var d = state.stg.detail;
      if (!d) {
        renderStagingList();
        return;
      }
      var b = d.batch || {};
      var records = d.records || [];
      var selN = Object.keys(state.stgSel).filter(function (k) {
        return state.stgSel[k];
      }).length;
      var html =
        '<div class="stg-bar">' +
        '<button class="btn btn-sm back" data-stg="back">' +
        cfg.icon('chevron-left', 14) +
        '返回批次列表</button>' +
        '<span class="stg-title">' +
        cfg.esc(cfg.dash(b.group_name)) +
        '<small>推送人 ' +
        cfg.esc(cfg.dash(b.pusher_name)) +
        ' · ' +
        cfg.esc(cfg.fmtDate(b.created_at, true)) +
        ' · 剩余 ' +
        records.length +
        ' 条</small></span>' +
        '<span class="spring">' +
        '<button class="btn btn-sm" data-stg="import-sel"' +
        (selN ? '' : ' disabled') +
        '>转入选中（' +
        selN +
        '）</button>' +
        '<button class="btn btn-sm btn-primary" data-stg="import-all"' +
        (records.length ? '' : ' disabled') +
        '>全部转入</button>' +
        '<button class="btn btn-sm btn-danger" data-stg="discard">废弃批次</button>' +
        '</span></div>' +
        (String(b.review_note || '').trim()
          ? '<div class="stg-note" style="margin:0 0 12px"><b>审核备注：</b>' +
            cfg.esc(b.review_note) +
            '</div>'
          : '');
      if (!records.length) {
        html +=
          '<div class="empty">' + cfg.icon('folder', 44) + '<p>本批次记录已全部转入</p></div>';
        body.innerHTML = html;
        return;
      }
      html +=
        '<div class="stg-table"><table class="stg-tb"><thead><tr>' +
        '<th><input type="checkbox" class="stg-ck" data-stg="ck-all" title="全选"></th>' +
        fields
          .map(function (f) {
            return '<th>' + f[1] + '</th>';
          })
          .join('') +
        '<th>操作</th></tr></thead><tbody>' +
        records
          .map(function (r) {
            return (
              '<tr data-sid="' +
              r.id +
              '">' +
              '<td><input type="checkbox" class="stg-ck" data-stg="ck"' +
              (state.stgSel[r.id] ? ' checked' : '') +
              '></td>' +
              fields
                .map(function (f) {
                  return stgCell(r, f[0]);
                })
                .join('') +
              '<td><button class="linkbtn" data-stg="save">保存</button></td>' +
              '</tr>'
            );
          })
          .join('') +
        '</tbody></table></div>';
      body.innerHTML = html;
    }

    /* 逐行保存：收集该行全部业务列输入调 PUT（白名单字段，日期列后端归一） */
    async function saveStagingRow(tr, btn) {
      var id = Number(tr.dataset.sid);
      var body = {};
      $$('.stg-in[data-f]', tr).forEach(function (inp) {
        body[inp.dataset.f] = inp.value;
      });
      btn.disabled = true;
      try {
        await cfg.api(path('/staging/records/' + id), {
          method: 'PUT',
          body: cfg.teamBody(body),
        });
        cfg.toast('已保存', 'success');
      } catch (e) {
        cfg.toast(e.message, 'error');
      } finally {
        btn.disabled = false;
      }
    }

    /* 转入：recordIds 缺省（null）= 全部转入；成功后刷新看板角标并就地刷新详情/列表 */
    var importing = false; // 转入提交中（防连点：双击「全部转入」不得重复 POST）
    async function importBatch(recordIds) {
      if (importing) return;
      var d = state.stg.detail;
      if (!d || !d.batch) return;
      importing = true;
      $$('#stgBody [data-stg="import-sel"], #stgBody [data-stg="import-all"]').forEach(
        function (b) {
          b.disabled = true;
        }
      );
      var batchId = d.batch.id;
      var failed = false;
      try {
        var r = await cfg.api(path('/staging/batches/' + batchId + '/import'), {
          method: 'POST',
          body: cfg.teamBody(recordIds ? { record_ids: recordIds } : {}),
        });
        cfg.toast('已转入 ' + ((r.data && r.data.imported) || 0) + ' 条', 'success');
        cfg.refresh.dashboard();
        cfg.refresh.records();
        await openBatch(batchId, true); // 批次行空后服务端自动置 imported，详情 404 时静默回列表
      } catch (e) {
        failed = true;
        cfg.toast(e.message, 'error');
      } finally {
        importing = false;
        /* 失败保留详情：按当前勾选/剩余条数恢复按钮可用态；成功则等 openBatch 重绘后的新节点。
           恢复本身失败不得掩盖原业务错误（finally 内自行兜底）。 */
        if (failed) {
          try {
            var selN = Object.keys(state.stgSel).filter(function (k) {
              return state.stgSel[k];
            }).length;
            var detail = state.stg.detail;
            var records = (detail && detail.records) || [];
            var selBtn = $('#stgBody [data-stg="import-sel"]');
            var allBtn = $('#stgBody [data-stg="import-all"]');
            if (selBtn) selBtn.disabled = !selN;
            if (allBtn) allBtn.disabled = !records.length;
          } catch (restoreErr) {
            /* 忽略恢复失败，保留 catch 中的业务错误 */
          }
        }
      }
    }

    async function discardBatch() {
      var d = state.stg.detail;
      if (!d || !d.batch) return;
      var yes = await cfg.confirm({
        title: '废弃批次',
        content:
          '废弃后该批次及全部暂存记录将被删除且不可恢复，确定废弃「' +
          (d.batch.group_name || '本批次') +
          '」吗？',
        confirmText: '废弃',
        danger: true,
      });
      if (!yes) return;
      try {
        await cfg.api(path('/staging/batches/' + d.batch.id), { method: 'DELETE' });
        cfg.toast('已废弃', 'success');
        state.stg.detail = null;
        cfg.refresh.dashboard();
        loadStaging();
      } catch (e) {
        cfg.toast(e.message, 'error');
      }
    }

    /* ================= 冲突 ================= */
    async function loadConflicts() {
      var body = $('#cftBody');
      body.innerHTML = '<div class="loading"><span class="spin"></span>加载中…</div>';
      try {
        var r = await cfg.api(path('/conflicts'));
        state.cft.list = r.data || [];
        state.cft.err = '';
      } catch (e) {
        state.cft.list = [];
        state.cft.err = e.message || '加载失败';
      }
      renderConflicts();
    }

    /* 两列对照（系统值 | 内网值）：业务列 + 可选状态行，差异字段标红 */
    function cftCardHtml(c) {
      var rows = cftRows(c, {
        fields: fields,
        numericKeys: numericKeys,
        showStatus: showStatus,
        dash: cfg.dash,
        fmtKv: cfg.fmtKv,
      });
      var rowsServer = '';
      var rowsIntranet = '';
      rows.forEach(function (row) {
        rowsServer +=
          '<div class="cft-r"><span class="k">' +
          row.label +
          '</span><span class="v' +
          (row.diff ? ' df' : '') +
          '">' +
          cfg.esc(row.server) +
          '</span></div>';
        rowsIntranet +=
          '<div class="cft-r"><span class="k">' +
          row.label +
          '</span><span class="v' +
          (row.diff ? ' df' : '') +
          '">' +
          cfg.esc(row.intranet) +
          '</span></div>';
      });
      return (
        '<div class="card cft" data-cid="' +
        c.id +
        '">' +
        '<div class="cft-head"><b>' +
        cfg.esc(cfg.dash(cftServerVal(c, 'line_name'))) +
        ' · ' +
        cfg.esc(cfg.dash(cftServerVal(c, 'section'))) +
        '</b>' +
        '<span class="cft-time">' +
        cfg.esc(cfg.fmtDate(c.created_at, true)) +
        '</span></div>' +
        '<div class="cft-cmp">' +
        '<div class="cft-col"><div class="cft-col-h">系统值</div><div class="cft-rows">' +
        rowsServer +
        '</div></div>' +
        '<div class="cft-col"><div class="cft-col-h">内网值</div><div class="cft-rows">' +
        rowsIntranet +
        '</div></div>' +
        '</div>' +
        '<div class="cft-acts">' +
        '<button class="btn btn-sm" data-keep="server">以系统为准</button>' +
        '<button class="btn btn-sm btn-primary" data-keep="intranet">以内网为准</button>' +
        '</div></div>'
      );
    }

    function renderConflicts() {
      var body = $('#cftBody');
      if (state.cft.err) {
        body.innerHTML =
          '<div class="empty">' +
          cfg.icon('warn', 44) +
          '<p>' +
          cfg.esc(state.cft.err) +
          '</p>' +
          '<p><button class="btn btn-sm" data-cft="reload">重试</button></p></div>';
        return;
      }
      if (!state.cft.list.length) {
        body.innerHTML =
          '<div class="empty">' + cfg.icon('check-circle', 44) + '<p>暂无待解决冲突</p></div>';
        return;
      }
      body.innerHTML = state.cft.list.map(cftCardHtml).join('');
    }

    /* 解决冲突：keep=server 保留系统值 / intranet 采用内网值；解决后就地移除该卡并刷新角标 */
    async function resolveConflict(card, keep, btn) {
      var id = Number(card.dataset.cid);
      btn.disabled = true;
      try {
        await cfg.api(path('/conflicts/' + id + '/resolve'), {
          method: 'POST',
          body: cfg.teamBody({ keep: keep }),
        });
        state.cft.list = state.cft.list.filter(function (c) {
          return c.id !== id;
        });
        card.remove();
        if (!state.cft.list.length) renderConflicts();
        cfg.toast(keep === 'intranet' ? '已按内网值更新' : '已保留系统值', 'success');
        cfg.refresh.dashboard();
        if (keep === 'intranet') cfg.refresh.records(); // 内网值已覆盖记录，卡片流静默刷新
      } catch (e) {
        btn.disabled = false;
        cfg.toast(e.message, 'error');
      }
    }

    /* ================= 导出 ================= */
    async function exportPreview(btn, query) {
      btn.disabled = true;
      try {
        var r = await cfg.api(path('/export/preview' + (query || '')));
        if (r.data && r.data.url) {
          cfg.openPreviewUrl(r.data.url);
          return;
        }
        throw new Error('预览地址为空');
      } catch (e) {
        cfg.toast(e.message, 'error');
      } finally {
        btn.disabled = false;
      }
    }

    async function exportDownload(btn, query) {
      btn.disabled = true;
      try {
        /* 走 :name.xlsx 文件名变体路径，供查看器按扩展名识别格式；文件名以 Content-Disposition 为准 */
        var res = await cfg.apiRaw(
          path('/export/' + encodeURIComponent(cfg.exportName) + (query || ''))
        );
        if (!res.ok) {
          var ej = null;
          try {
            ej = await res.json();
          } catch (e) {}
          throw new Error((ej && ej.message) || '导出失败（' + res.status + '）');
        }
        await cfg.saveBlob(res, cfg.exportName);
      } catch (e) {
        cfg.toast(e.message, 'error');
      } finally {
        btn.disabled = false;
      }
    }

    /* 导出弹层（FAB 点击）：
       台账视图范围两项（默认「当前线路」；未匹配线路伪组不支持单线路导出，该项禁用）；线路视图默认「全部线路」，
       第二项为「选择线路」下拉（下拉即选中该单选）；「按线路分表」仅全部线路范围可勾（其余禁用并灰显说明） */
    function openExportModal() {
      if (!state.rec) {
        cfg.toast('记录加载中，请稍候', 'info');
        return;
      }
      var scope = cfg.exportScope();
      var inLedger = scope.inLedger;
      var g = scope.group;
      var canCur = !!(g && g.lineName); // 未匹配线路伪组聚合多条线路名，无法映射单一线路参数
      var lines = scope.lines || [];
      var html =
        cfg.mHead('导出统计表') +
        '<label class="f-lab">导出范围</label>' +
        (inLedger
          ? '<label class="ex-opt' +
            (canCur ? '' : ' dis') +
            '"><input type="radio" name="exRange" value="line"' +
            (canCur ? ' checked' : ' disabled') +
            '> 当前线路（' +
            cfg.esc(g ? g.title : '') +
            '）</label>' +
            '<label class="ex-opt"><input type="radio" name="exRange" value="all"' +
            (canCur ? '' : ' checked') +
            '> 全部线路</label>'
          : '<label class="ex-opt"><input type="radio" name="exRange" value="all" checked> 全部线路</label>' +
            '<label class="ex-opt' +
            (lines.length ? '' : ' dis') +
            '"><input type="radio" name="exRange" value="line"' +
            (lines.length ? '' : ' disabled') +
            '> 选择线路</label>' +
            '<select class="f-sel" id="exLine" style="margin-top:2px" disabled>' +
            lines
              .map(function (x) {
                return (
                  '<option value="' +
                  cfg.esc(x.line_name) +
                  '">' +
                  cfg.esc(cfg.fmtKv(x.voltage_level) + ' ' + x.line_name) +
                  '</option>'
                );
              })
              .join('') +
            '</select>') +
        '<label class="f-lab">分表</label>' +
        '<label class="ex-opt" id="exSplitOpt"><input type="checkbox" id="exSplit"> 按线路分表（每条线路一个工作表，表名 = 电压等级 + 线路名称）</label>' +
        '<div class="f-hint" id="exSplitHint">仅「全部线路」范围可分表；导出范围非全部线路时此项不可用。</div>' +
        '<div class="m-foot"><button class="btn btn-sm" data-ex="preview">' +
        cfg.icon('file', 15) +
        '预览</button>' +
        '<button class="btn btn-sm btn-primary" data-ex="download">' +
        cfg.icon('download', 15) +
        '下载</button></div>';
      var inst = cfg.modal(html, { width: 460 });
      var splitCk = $('#exSplit', inst.box);
      var splitOpt = $('#exSplitOpt', inst.box);
      var splitHint = $('#exSplitHint', inst.box);
      var lineSel = $('#exLine', inst.box);
      function curRange() {
        var v = 'all';
        $$('input[name="exRange"]', inst.box).forEach(function (r) {
          if (r.checked) v = r.value;
        });
        return v;
      }
      /* 分表勾选随范围联动：非全部线路 → 禁用 + 摘勾 + 灰显说明 */
      function syncSplit() {
        var off = curRange() !== 'all';
        splitCk.disabled = off;
        if (off) splitCk.checked = false;
        splitOpt.classList.toggle('dis', off);
        splitHint.style.opacity = off ? '.55' : '';
        if (lineSel) lineSel.disabled = curRange() !== 'line';
      }
      $$('input[name="exRange"]', inst.box).forEach(function (r) {
        r.addEventListener('change', syncSplit);
      });
      if (lineSel) {
        /* 下拉选线即视为选中「选择线路」 */
        lineSel.addEventListener('change', function () {
          var radio = $('input[name="exRange"][value="line"]', inst.box);
          if (radio && !radio.checked) {
            radio.checked = true;
            syncSplit();
          }
        });
      }
      syncSplit();
      /* 拼导出 query：分线路 line=（未匹配伪组无 lineName，已被 canCur 拦截）；全部线路 + 勾选分表 → split=1 */
      function buildQuery() {
        var range = curRange();
        var lineName = '';
        if (range === 'line')
          lineName = inLedger ? (g ? g.lineName : '') : lineSel ? lineSel.value : '';
        if (range === 'line' && !lineName) {
          cfg.toast('请选择线路', 'info');
          return null;
        }
        var q = [];
        if (lineName) q.push('line=' + encodeURIComponent(lineName));
        if (range === 'all' && splitCk.checked && !splitCk.disabled) q.push('split=1');
        return q.length ? '?' + q.join('&') : '';
      }
      $('[data-ex="preview"]', inst.box).addEventListener('click', function () {
        var q = buildQuery();
        if (q === null) return;
        inst.close();
        exportPreview(this, q);
      });
      $('[data-ex="download"]', inst.box).addEventListener('click', function () {
        var q = buildQuery();
        if (q === null) return;
        inst.close();
        exportDownload(this, q);
      });
    }

    return {
      loadStaging: loadStaging,
      renderStagingList: renderStagingList,
      openBatch: openBatch,
      renderBatchDetail: renderBatchDetail,
      saveStagingRow: saveStagingRow,
      importBatch: importBatch,
      discardBatch: discardBatch,
      loadConflicts: loadConflicts,
      renderConflicts: renderConflicts,
      resolveConflict: resolveConflict,
      exportPreview: exportPreview,
      exportDownload: exportDownload,
      openExportModal: openExportModal,
    };
  }

  return {
    createPanels: createPanels,
    // 纯逻辑暴露给 node --test（回退页无打包器）。
    sameVal: sameVal,
    cftServerVal: cftServerVal,
    cftRows: cftRows,
  };
});
