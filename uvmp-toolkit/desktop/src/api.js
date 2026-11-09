// 渲染进程 → Python 核心的调用封装（preload 暴露的 window.core 之上）
import { saveJobParams } from './lib/job-params.mjs';
import { addRunning, finishTask, startTask, updateTask } from './lib/task-status.mjs';

export function call(method, params) {
  // Vue 响应式 Proxy 过不了 Electron IPC 的结构化克隆（已踩坑："An object could not be cloned"）。
  // 参数设计上就是 JSON 可序列化数据，统一 JSON 往返净化成纯对象。
  const plain = params === undefined ? params : JSON.parse(JSON.stringify(params));
  return window.core.call(method, plain);
}

export function pickFile(filters) {
  return window.core.pickFile(filters);
}

export function pickDirectory(defaultPath) {
  return window.core.pickDirectory(defaultPath);
}

export function showItem(p) {
  return window.core.showItem(p);
}

// 题库搜题：壳层功能通道（不经 Python 核心），方法见 electron/preload.cjs quizSearch 段
export function qs() {
  return window.core.quizSearch;
}

/**
 * 跑一个核心任务并接线事件。
 * callbacks: onLog(line) / onProgress({current,total,label}) / onDone(job) / onError(err)
 * 返回 { jobId: Promise<string>, finished: Promise, cancel: fn }
 * finished 只等待任务终态；启动错误由 onError 报告，不在此重复 reject。
 *
 * 关键：job_id 由 runJob 响应带回，但核心在拿到任务后【立即】执行——
 * 快速失败的任务（如未配凭据）可能在响应到达前就发完全部事件。
 * 这里先把事件缓冲，job_id 到达后回放，防止「点了没反应」的假象（已踩坑）。
 */
export function runJob(appId, params, callbacks = {}) {
  let jobId = null;
  const buffered = [];
  let resolveFinished;
  const finished = new Promise((resolve) => {
    resolveFinished = resolve;
  });
  // 上报主进程"任务进行中"（托盘退出确认用），done/error/cancel 均收口一次
  window.core.jobRunning && window.core.jobRunning(1);
  addRunning(1);
  const taskKey = startTask(appId, params);
  let ended = false;
  let pollTimer = null;
  const endOnce = () => {
    if (!ended) {
      ended = true;
      if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
      finishTask(taskKey);
      window.core.jobRunning && window.core.jobRunning(-1);
      addRunning(-1);
    }
  };

  // 事件兜底：job.progress/job.done 偶发丢失时，轮询 getJob 补齐进度并收尾。
  // 多实例并行时每个 runJob 各自轮询自己的 job_id，互不覆盖。
  function startWatchdog(id) {
    if (ended) return;
    pollTimer = setInterval(async () => {
      if (ended) {
        clearInterval(pollTimer);
        pollTimer = null;
        return;
      }
      try {
        const job = await call('getJob', { job_id: id });
        if (!job || ended) return;
        if (job.progress) {
          updateTask(taskKey, job.progress);
          callbacks.onProgress && callbacks.onProgress(job.progress);
        }
        if (['success', 'failed', 'cancelled'].includes(job.status)) {
          handle({ event: 'job.done', job_id: id, job });
        }
      } catch (_e) {
        /* 轮询失败等待下一轮；事件仍可能正常到达 */
      }
    }, 1200);
  }

  function handle(ev) {
    if (ev.event === 'job.log') callbacks.onLog && callbacks.onLog(ev.line);
    else if (ev.event === 'job.progress') {
      updateTask(taskKey, ev);
      callbacks.onProgress && callbacks.onProgress(ev);
    } else if (ev.event === 'job.done') {
      if (ended) return;
      off();
      endOnce();
      resolveFinished(ev.job);
      callbacks.onDone && callbacks.onDone(ev.job);
    }
  }

  const off = window.core.onEvent((ev) => {
    if (!ev.job_id) return;
    if (!jobId) {
      buffered.push(ev);
      return;
    }
    if (ev.job_id === jobId) handle(ev);
  });

  const idPromise = call('runJob', { app_id: appId, params }).then((r) => {
    jobId = r.job_id;
    // 参数快照供执行历史「参数回填」；敏感键/超限/不支持的应用在模块内自动拒绝
    try {
      saveJobParams(window.localStorage, jobId, appId, params);
    } catch (_e) {
      /* 本地存储不可用时不影响任务 */
    }
    for (const ev of buffered) {
      if (ev.job_id === jobId) handle(ev);
    }
    buffered.length = 0;
    startWatchdog(jobId);
    return jobId;
  });
  // 取消与页面等待共用已处理的启动结果，保留 jobId 本身的拒绝语义。
  const cancelReady = idPromise.catch((err) => {
    off();
    endOnce();
    resolveFinished();
    callbacks.onError && callbacks.onError(err);
    return null;
  });

  return {
    jobId: idPromise,
    finished,
    cancel: () =>
      cancelReady.then((id) => {
        if (!id || ended) return undefined;
        // 只发送取消请求；终态必须等核心 job.done / getJob 轮询确认。
        // 若前端提前结束，旧任务仍在后端运行时点“开始”会收到
        // “该实例已有任务在运行”。
        return call('cancelJob', { job_id: id });
      }),
  };
}
