// 机巡审核进度浮窗渲染：每行一个实例，直接轮询核心的实例/任务状态。
// 不依赖主窗口是否打开；拖动/缩放只向主进程报开始/结束，位移由主进程轮询 OS 光标。
const rowsEl = document.getElementById('rows');
const updatedEl = document.getElementById('updated');
const head = document.getElementById('head');
const grip = document.getElementById('grip');
const closeBtn = document.getElementById('close');

const panel = window.progressPanel || {
  dragStart: () => {},
  dragEnd: () => {},
  close: () => {},
};
const lastJobIds = new Map();

function esc(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function stateOf(job) {
  if (!job) {
    return { text: '未运行', cls: '', pct: 0, count: '—', label: '' };
  }
  const status = String(job.status || '');
  let text = status || '未知状态';
  let cls = '';
  if (status === 'running') {
    text = '运行中';
    cls = 'running';
  } else if (status === 'pending') {
    text = '排队中';
    cls = 'pending';
  } else if (status === 'success') {
    text = '已完成';
    cls = 'done';
  } else if (status === 'failed') {
    text = '失败';
    cls = 'failed';
  } else if (status === 'cancelled') {
    text = '已停止';
  }
  const progress = job.progress || {};
  const total = Number(progress.total) || 0;
  const current = Number(progress.current) || 0;
  let pct = total > 0 ? Math.round((current * 100) / total) : 0;
  if (status === 'success') pct = 100;
  pct = Math.max(0, Math.min(100, pct));
  return {
    text,
    cls,
    pct,
    count: total > 0 ? `${current}/${total}` : pct ? `${pct}%` : '—',
    label: String(progress.label || job.error || ''),
  };
}

function rowHtml(row) {
  if (row.error) {
    return (
      '<div class="row">' +
      '<div class="row-head"><span class="dot failed"></span>' +
      '<span class="name" title="' +
      esc(row.name) +
      '">' +
      esc(row.name) +
      '</span><span class="state-text">读取失败</span></div>' +
      '<div class="bar"><i style="width:0"></i></div>' +
      '<div class="meta"><span>—</span><span class="label" title="' +
      esc(row.error) +
      '">' +
      esc(row.error) +
      '</span></div>' +
      '</div>'
    );
  }
  const state = stateOf(row.job);
  return (
    '<div class="row">' +
    '<div class="row-head"><span class="dot ' +
    state.cls +
    '"></span>' +
    '<span class="name" title="' +
    esc(row.name) +
    '">' +
    esc(row.name) +
    '</span><span class="state-text">' +
    esc(state.text) +
    '</span></div>' +
    '<div class="bar"><i style="width:' +
    state.pct +
    '%"></i></div>' +
    '<div class="meta"><span>' +
    esc(state.count) +
    '</span><span class="label" title="' +
    esc(state.label) +
    '">' +
    esc(state.label) +
    '</span></div>' +
    '</div>'
  );
}

function render(rows, error) {
  if (error) {
    rowsEl.innerHTML = '<div class="error">' + esc(error) + '</div>';
    return;
  }
  if (!rows.length) {
    rowsEl.innerHTML = '<div class="empty">暂无实例</div>';
    return;
  }
  rowsEl.innerHTML = rows.map(rowHtml).join('');
}

async function fetchInstances() {
  const cfg = await window.core.call('getConfig');
  const app = (cfg || {}).defect_review || {};
  const list = Array.isArray(app.instances) ? app.instances : [];
  const source = list.length ? list : [{ id: 'default', name: '实例 1' }];
  return source.map((item, index) => ({
    id: item.id || 'default',
    name: item.name || `实例 ${index + 1}`,
  }));
}

async function loadRow(instance) {
  let job = null;
  try {
    const active = await window.core.call('getActiveJob', {
      app_id: 'defect_review',
      instance_id: instance.id,
    });
    if (active) {
      job = active;
      lastJobIds.set(instance.id, active.id);
    } else {
      const lastId = lastJobIds.get(instance.id);
      if (lastId) {
        job = await window.core.call('getJob', { job_id: lastId });
        if (!job) lastJobIds.delete(instance.id);
      }
    }
  } catch (e) {
    return { ...instance, job: null, error: String((e && e.message) || e) };
  }
  return { ...instance, job, error: '' };
}

async function tick() {
  const now = new Date();
  try {
    if (!window.core || !window.core.call) throw new Error('核心通道不可用');
    const instances = await fetchInstances();
    const rows = await Promise.all(instances.map(loadRow));
    render(rows, '');
    updatedEl.textContent =
      '已更新 ' +
      now.toLocaleTimeString('zh-CN', {
        hour12: false,
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
  } catch (e) {
    render([], String((e && e.message) || e));
    updatedEl.textContent = '核心未连接';
  }
  setTimeout(tick, 1000);
}

let dragging = false;

function dragBegin(event, mode) {
  if (event.target && event.target.closest('.close')) return;
  dragging = true;
  panel.dragStart(mode);
  event.preventDefault();
  event.stopPropagation();
}

function dragDone() {
  if (!dragging) return;
  dragging = false;
  panel.dragEnd();
}

head.addEventListener('pointerdown', (e) => dragBegin(e, 'move'));
grip.addEventListener('pointerdown', (e) => dragBegin(e, 'resize'));
head.addEventListener('pointerup', dragDone);
head.addEventListener('pointercancel', dragDone);
grip.addEventListener('pointerup', dragDone);
grip.addEventListener('pointercancel', dragDone);
window.addEventListener('blur', dragDone);
closeBtn.addEventListener('pointerdown', (e) => e.stopPropagation());
closeBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  panel.close();
});

tick();
