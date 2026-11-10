/* 机巡审核实时大屏：按实例展示日志与进度；点击卡片切到真实浏览器。 */
(() => {
  const grid = document.getElementById('grid');
  const overallBar = document.getElementById('overallBar');
  const overallText = document.getElementById('overallText');
  const overallPct = document.getElementById('overallPct');
  const overallCount = document.getElementById('overallCount');
  const closeBtn = document.getElementById('closeBtn');
  const stopAllBtn = document.getElementById('stopAllBtn');

  const state = { instances: [], tiles: new Map() };

  function pct(done, total) {
    if (!total) return 0;
    return Math.max(0, Math.min(100, Math.round((done * 100) / total)));
  }

  function statusOf(item) {
    if (item.abnormalReason) return { label: item.abnormalReason, cls: 'failed' };
    if (item.failed) return { label: '失败', cls: 'failed' };
    if (item.running) return { label: '运行中', cls: 'running' };
    if (item.starting) return { label: '启动中', cls: 'starting' };
    if (item.statusText === '完成') return { label: '已完成', cls: '' };
    if (item.statusText) return { label: item.statusText, cls: '' };
    return { label: '未开始', cls: '' };
  }

  function setBar(el, value, failed) {
    if (!el) return;
    el.style.width = value + '%';
    el.classList.toggle('failed', !!failed);
    el.classList.toggle('done', value >= 100 && !failed);
  }

  function makeTile(item) {
    const tile = document.createElement('article');
    tile.className = 'tile';
    tile.dataset.id = item.id;
    tile.innerHTML =
      '<div class="tile-head">' +
      '<span class="dot"></span>' +
      '<span class="name"></span>' +
      '<span class="state"></span>' +
      '<button class="open" type="button">进入浏览器</button>' +
      '</div>' +
      '<div class="tile-progress">' +
      '<div class="task-label">准备中</div>' +
      '<div class="bar"><i></i></div>' +
      '<div class="meta"><span class="left"></span><span class="right"></span></div>' +
      '</div>' +
      '<div class="logs-title">运行摘要</div>' +
      '<div class="logs"><div class="empty">暂无日志</div></div>';
    tile.querySelector('.open').addEventListener('click', (event) => {
      event.stopPropagation();
      focusInstance(item.id);
    });
    return tile;
  }

  function updateLogs(tile, item) {
    const box = tile.querySelector('.logs');
    const lines = Array.isArray(item.userLogs) ? item.userLogs.slice(-8) : [];
    const key = lines.join('\n');
    if (tile._logKey === key) return;
    tile._logKey = key;
    const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
    box.innerHTML = '';
    if (!lines.length) {
      box.innerHTML = '<div class="empty">暂无日志</div>';
      return;
    }
    const fragment = document.createDocumentFragment();
    for (const line of lines) {
      const div = document.createElement('div');
      div.className = 'logline';
      div.textContent = String(line);
      fragment.appendChild(div);
    }
    box.appendChild(fragment);
    if (nearBottom) box.scrollTop = box.scrollHeight;
  }

  function updateTile(tile, item) {
    const status = statusOf(item);
    const finished = ['完成', '已完成', '已停止', '已重置'].includes(item.statusText || '');
    const abnormal = !!(item.abnormal || item.abnormalReason);
    tile.classList.toggle('normal', !abnormal && (!!item.running || finished));
    tile.classList.toggle('abnormal', abnormal);
    tile.classList.toggle('pending', !!item.starting && !abnormal);
    const dot = tile.querySelector('.dot');
    dot.className = 'dot ' + status.cls;
    tile.querySelector('.name').textContent = item.name || item.id || '实例';
    tile.querySelector('.state').textContent = status.label;

    const taskTotal = Number(item.taskTotal) || 0;
    const taskDone = Number(item.taskDone) || 0;
    const imageTotal = Number(item.imageTotal) || 0;
    const imageDone = Number(item.imageDone) || 0;
    const value = taskTotal ? pct(taskDone, taskTotal) : pct(imageDone, imageTotal);
    const failed = !!item.failed;
    setBar(tile.querySelector('.bar i'), value, failed);
    tile.querySelector('.bar').classList.toggle('failed', failed);
    tile.querySelector('.task-label').textContent = item.taskLabel || item.stage || '准备中';
    const left = tile.querySelector('.left');
    const right = tile.querySelector('.right');
    if (taskTotal) {
      left.textContent = '任务 ' + taskDone + '/' + taskTotal;
      right.textContent = imageTotal ? '当前图片 ' + imageDone + '/' + imageTotal : status.label;
    } else if (imageTotal) {
      left.textContent = '图片 ' + imageDone + '/' + imageTotal;
      right.textContent = status.label;
    } else {
      left.textContent = '准备中';
      right.textContent = status.label;
    }
    updateLogs(tile, item);
  }

  function render() {
    const instances = state.instances || [];
    if (!instances.length) {
      grid.innerHTML = '<div class="empty">暂无运行中的实例</div>';
      state.tiles.clear();
      updateOverall([]);
      return;
    }
    const count = instances.length;
    let cols = 1;
    if (count === 1) cols = 1;
    else if (count === 2) cols = 2;
    else if (count <= 4) cols = 2;
    else if (count <= 6) cols = 3;
    else cols = Math.ceil(Math.sqrt(count));
    grid.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(0, 1fr))';
    grid.style.maxWidth = count === 1 ? '860px' : '1536px';
    const empty = grid.querySelector('.empty');
    if (empty) empty.remove();

    const ids = new Set(instances.map((item) => item.id));
    for (const [id, tile] of state.tiles.entries()) {
      if (!ids.has(id)) {
        tile.remove();
        state.tiles.delete(id);
      }
    }
    instances.forEach((item) => {
      let tile = state.tiles.get(item.id);
      if (!tile) {
        tile = makeTile(item);
        state.tiles.set(item.id, tile);
      }
      if (tile.parentElement !== grid) grid.appendChild(tile);
      updateTile(tile, item);
    });
    updateOverall(instances);
  }

  function updateOverall(instances) {
    const active = instances.filter((item) => item.running || item.starting).length;
    const done = instances.filter(
      (item) => item.statusText === '已完成' || item.statusText === '完成'
    ).length;
    const failed = instances.filter((item) => item.failed).length;
    const withTotals = instances.filter((item) => Number(item.taskTotal) > 0);
    const total = withTotals.reduce((sum, item) => sum + Number(item.taskTotal), 0);
    const current = withTotals.reduce((sum, item) => sum + Number(item.taskDone), 0);
    const value = total ? pct(current, total) : 0;
    setBar(overallBar, value, failed > 0);
    overallBar.style.width = value + '%';
    overallPct.textContent = total ? value + '%' : '0%';
    overallText.textContent = total ? current + ' / ' + total + ' 个任务' : '准备中';
    overallCount.textContent =
      '运行 ' + active + ' · 已完成 ' + done + (failed ? ' · 失败 ' + failed : '');
  }

  function focusInstance(id) {
    if (window.screensaver && window.screensaver.focusInstance) {
      window.screensaver.focusInstance(id);
    }
  }

  closeBtn.addEventListener('click', (event) => {
    event.stopPropagation();
    if (window.screensaver) window.screensaver.requestClose();
  });
  stopAllBtn.addEventListener('click', (event) => {
    event.stopPropagation();
    if (window.screensaver) window.screensaver.requestStopAll();
  });

  if (window.screensaver) {
    window.screensaver.onUpdate((payload) => {
      state.instances = (payload && payload.instances) || [];
      render();
    });
    window.screensaver
      .ready()
      .then((payload) => {
        state.instances = (payload && payload.instances) || [];
        render();
      })
      .catch(() => {});
  }

  render();
})();
