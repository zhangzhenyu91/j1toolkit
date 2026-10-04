// 渲染进程 → Python 核心的调用封装（preload 暴露的 window.core 之上）
export function call(method, params) {
  // Vue 响应式 Proxy 过不了 Electron IPC 的结构化克隆（已踩坑："An object could not be cloned"）。
  // 参数设计上就是 JSON 可序列化数据，统一 JSON 往返净化成纯对象。
  const plain = params === undefined ? params : JSON.parse(JSON.stringify(params))
  return window.core.call(method, plain)
}

export function pickFile(filters) {
  return window.core.pickFile(filters)
}

export function pickDirectory(defaultPath) {
  return window.core.pickDirectory(defaultPath)
}

export function showItem(p) {
  return window.core.showItem(p)
}

// 题库搜题：壳层功能通道（不经 Python 核心），方法见 electron/preload.cjs quizSearch 段
export function qs() {
  return window.core.quizSearch
}

/**
 * 跑一个核心任务并接线事件。
 * callbacks: onLog(line) / onProgress({current,total,label}) / onDone(job) / onError(err)
 * 返回 { jobId: Promise<string>, cancel: fn }
 *
 * 关键：job_id 由 runJob 响应带回，但核心在拿到任务后【立即】执行——
 * 快速失败的任务（如未配凭据）可能在响应到达前就发完全部事件。
 * 这里先把事件缓冲，job_id 到达后回放，防止「点了没反应」的假象（已踩坑）。
 */
export function runJob(appId, params, callbacks = {}) {
  let jobId = null
  const buffered = []
  // 上报主进程"任务进行中"（托盘退出确认用），done/error/cancel 均收口一次
  window.core.jobRunning && window.core.jobRunning(1)
  let ended = false
  const endOnce = () => {
    if (!ended) { ended = true; window.core.jobRunning && window.core.jobRunning(-1) }
  }

  function handle(ev) {
    if (ev.event === 'job.log') callbacks.onLog && callbacks.onLog(ev.line)
    else if (ev.event === 'job.progress') callbacks.onProgress && callbacks.onProgress(ev)
    else if (ev.event === 'job.done') {
      off()
      endOnce()
      callbacks.onDone && callbacks.onDone(ev.job)
    }
  }

  const off = window.core.onEvent((ev) => {
    if (!ev.job_id) return
    if (!jobId) { buffered.push(ev); return }
    if (ev.job_id === jobId) handle(ev)
  })

  const idPromise = call('runJob', { app_id: appId, params })
    .then((r) => {
      jobId = r.job_id
      for (const ev of buffered) {
        if (ev.job_id === jobId) handle(ev)
      }
      buffered.length = 0
      return jobId
    })
    .catch((err) => {
      off()
      endOnce()
      callbacks.onError && callbacks.onError(err)
      throw err
    })

  return {
    jobId: idPromise,
    cancel: () => { if (jobId) call('cancelJob', { job_id: jobId }) },
  }
}
