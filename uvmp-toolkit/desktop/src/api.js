// 渲染进程 → Python 核心的调用封装（preload 暴露的 window.core 之上）
import { saveJobParams } from './lib/job-params.mjs';
import { createFailureTracker } from './lib/poll.mjs';
import { addRunning, finishTask, startTask, updateTask } from './lib/task-status.mjs';

const TERMINAL = ['success', 'failed', 'cancelled'];
// 轮询兜底：偶发读取失败容忍若干次，连续失败说明核心已不可用
const READ_FAIL_LIMIT = 5;

// 在跑任务登记表：核心重启前可整体收口，避免 keep-alive 页面残留「执行中」
const activeJobs = new Set();

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
 * 统一收口所有在跑任务（核心重启/退出时调用）。
 * 每个任务的监听、轮询计时器、jobRunning 计数与壳层任务状态都在各自的 endOnce 里清理。
 */
export function abortAllJobs(message = '核心已退出，任务已中断') {
  for (const terminate of [...activeJobs]) terminate(message);
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
 *
 * 终态收口只有一条路 endOnce：正常 done / 任务丢失 / 轮询连续失败 / core.exit /
 * 启动失败 / abortAllJobs 全部走它，确保监听器、计时器、jobRunning 与任务状态不残留。
 */
export function runJob(appId, params, callbacks = {}) {
  let jobId = null;
  const buffered = [];
  let resolveFinished;
  const finished = new Promise((resolve) => {
    resolveFinished = resolve;
  });
  // 上报主进程"任务进行中"（托盘退出确认用），终态只收口一次
  window.core.jobRunning && window.core.jobRunning(1);
  addRunning(1);
  const taskKey = startTask(appId, params);
  const readFailures = createFailureTracker(READ_FAIL_LIMIT);
  let ended = false;
  let pollTimer = null;
  let off = null;
  let lostReads = 0;

  // 幂等收口：清计时器、解绑核心事件监听、归还 jobRunning 与壳层任务状态
  const endOnce = () => {
    if (ended) return;
    ended = true;
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
    if (off) {
      off();
      off = null;
    }
    activeJobs.delete(terminate);
    finishTask(taskKey);
    window.core.jobRunning && window.core.jobRunning(-1);
    addRunning(-1);
  };

  // 异常终态：统一交给 onError，finished 兑现（不 reject，避免页面出现未捕获拒绝）
  function terminate(message) {
    if (ended) return;
    endOnce();
    resolveFinished();
    callbacks.onError && callbacks.onError(new Error(message));
  }

  function jobLostMessage() {
    // 核心只回收终态任务（保留最近 50 个），因此这里多为进程重启；措辞不能断言任务失败
    return `任务状态已丢失（核心可能已重启），请到执行历史确认结果：${jobId || ''}`;
  }

  // 事件兜底：job.progress/job.done 偶发丢失时，轮询 getJob 补齐进度并收尾。
  // 多实例并行时每个 runJob 各自轮询自己的 job_id，互不覆盖。
  function startWatchdog(id) {
    if (ended) return;
    pollTimer = setInterval(async () => {
      if (ended) return;
      try {
        const job = await call('getJob', { job_id: id });
        if (ended) return;
        if (!job) {
          // 核心只保留最近 50 个终态任务，连续读不到说明任务已不在内存（多为进程重启），
          // 继续轮询永远等不到终态；再确认一次后收口，避免把偶发空值当失败。
          lostReads += 1;
          if (lostReads >= 2) terminate(jobLostMessage());
          return;
        }
        lostReads = 0;
        readFailures.ok();
        if (job.progress) {
          updateTask(taskKey, job.progress);
          callbacks.onProgress && callbacks.onProgress(job.progress);
        }
        if (TERMINAL.includes(job.status)) {
          handle({ event: 'job.done', job_id: id, job });
        }
      } catch (e) {
        if (ended) return;
        // 事件仍可能正常到达，先容忍；连续失败到上限则判定核心不可用
        if (readFailures.fail()) {
          terminate(
            `连续 ${readFailures.limit} 次读取任务状态失败（核心可能未运行）：${String(
              (e && e.message) || e
            )}`
          );
        }
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
      endOnce();
      resolveFinished(ev.job);
      callbacks.onDone && callbacks.onDone(ev.job);
    }
  }

  off = window.core.onEvent((ev) => {
    // 核心进程退出：所有在跑任务都不会再有终态事件，立即统一收口
    if (ev.event === 'core.exit') {
      terminate(`核心进程已退出（code ${ev.code ?? '未知'}），任务已中断`);
      return;
    }
    if (!ev.job_id) return;
    if (!jobId) {
      buffered.push(ev);
      return;
    }
    if (ev.job_id === jobId) handle(ev);
  });

  activeJobs.add(terminate);

  // call 同步抛错（如 preload 未注入）时不能留下已加的计数与监听
  let startPromise;
  try {
    startPromise = call('runJob', { app_id: appId, params });
  } catch (e) {
    startPromise = Promise.reject(e);
  }

  const idPromise = startPromise.then((r) => {
    jobId = r.job_id;
    // 参数快照供执行历史「参数回填」；敏感键/超限/不支持的应用在模块内自动拒绝
    try {
      saveJobParams(window.localStorage, jobId, appId, params);
    } catch (_e) {
      /* 本地存储不可用时不影响任务 */
    }
    // 启动期间已收到终态（如 core.exit）时不再回放缓冲，否则页面会被迟到的进度改回「执行中」
    if (!ended) {
      for (const ev of buffered) {
        if (ev.job_id === jobId) handle(ev);
      }
    }
    buffered.length = 0;
    startWatchdog(jobId);
    return jobId;
  });
  // 取消与页面等待共用已处理的启动结果，保留 jobId 本身的拒绝语义。
  idPromise.catch((err) => {
    terminate(String((err && err.message) || err));
    return null;
  });

  return {
    jobId: idPromise,
    finished,
    cancel: () =>
      idPromise
        .then((id) => {
          if (!id || ended) return undefined;
          // 只发送取消请求；终态必须等核心 job.done / getJob 轮询确认。
          // 若前端提前结束，旧任务仍在后端运行时点“开始”会收到
          // “该实例已有任务在运行”。
          return call('cancelJob', { job_id: id });
        })
        .catch(() => undefined),
  };
}
