import { reactive } from 'vue';

// 壳层运行中任务：runJob 起停时维护，页头显示数量，首页显示进度卡片。
// jobs 以 app + 实例为键，支持同一应用多实例并行。
export const taskStatus = reactive({ running: 0, jobs: {} });

let seq = 0;

export function addRunning(delta) {
  taskStatus.running = Math.max(0, taskStatus.running + delta);
}

export function startTask(appID, params = {}) {
  seq += 1;
  const key = `${appID}:${params.instance_id || 'default'}:${seq}`;
  taskStatus.jobs[key] = {
    key,
    appID,
    name: String(params.instance_name || '').trim(),
    current: 0,
    total: 0,
    pct: 0,
    label: '准备启动',
    text: '准备启动',
  };
  return key;
}

export function updateTask(key, progress = {}) {
  const job = taskStatus.jobs[key];
  if (!job) return;
  const current = Number(progress.current) || 0;
  const total = Number(progress.total) || 0;
  job.current = current;
  job.total = total;
  job.pct = total > 0 ? Math.min(100, Math.round((current * 100) / total)) : 0;
  job.label = String(progress.label || job.label || '');
  job.text = total > 0 ? `${job.label} ${current}/${total}` : job.label;
}

export function finishTask(key) {
  delete taskStatus.jobs[key];
}
