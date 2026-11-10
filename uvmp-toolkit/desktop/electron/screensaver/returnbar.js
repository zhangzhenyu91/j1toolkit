/* 浏览器视图浮窗：当前实例状态 + 最近日志 + 返回实时大屏。 */
(() => {
  const dot = document.getElementById('dot');
  const name = document.getElementById('name');
  const meta = document.getElementById('meta');
  const logs = document.getElementById('logs');
  const returnBtn = document.getElementById('returnBtn');

  function nearBottom() {
    return logs.scrollHeight - logs.scrollTop - logs.clientHeight < 40;
  }

  function render(payload) {
    const item = payload && payload.instance;
    if (!item) {
      dot.className = 'dot';
      name.textContent = '审核运行日志';
      meta.textContent = '等待实例日志…';
      logs.innerHTML = '<div class="empty">暂无日志</div>';
      return;
    }
    dot.className = 'dot ' + (item.failed ? '' : item.running ? 'running' : '');
    dot.style.background = item.failed ? '#f53f3f' : item.running ? '#00b42a' : '#ff7d00';
    name.textContent = item.name || item.id || '实例';
    const taskTotal = Number(item.taskTotal) || 0;
    const taskDone = Number(item.taskDone) || 0;
    const imageTotal = Number(item.imageTotal) || 0;
    const imageDone = Number(item.imageDone) || 0;
    const pieces = [];
    if (item.statusText) pieces.push(item.statusText);
    if (taskTotal) pieces.push(`任务 ${taskDone}/${taskTotal}`);
    if (imageTotal) pieces.push(`当前图片 ${imageDone}/${imageTotal}`);
    if (item.stage) pieces.push(item.stage);
    meta.textContent = pieces.join(' · ') || '运行中';
    meta.title = meta.textContent;

    const wasNearBottom = nearBottom();
    const lines = Array.isArray(item.logs) ? item.logs : [];
    logs.innerHTML = '';
    if (!lines.length) {
      logs.innerHTML = '<div class="empty">暂无日志</div>';
      return;
    }
    const fragment = document.createDocumentFragment();
    for (const line of lines) {
      const div = document.createElement('div');
      div.className = 'line';
      div.textContent = String(line);
      fragment.appendChild(div);
    }
    logs.appendChild(fragment);
    if (wasNearBottom) logs.scrollTop = logs.scrollHeight;
  }

  returnBtn.addEventListener('click', () => {
    if (window.returnbar) window.returnbar.returnToScreensaver();
  });

  if (window.returnbar) {
    window.returnbar.onUpdate(render);
  }
  render(null);
})();
