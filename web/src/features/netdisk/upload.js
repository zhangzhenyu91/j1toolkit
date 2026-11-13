// 团队网盘分片上传状态机（无浏览器依赖，便于 node --test 直接覆盖）。
//
// 能力：
// - 片内并发默认 6（OpenList 接收窗口 8 片，4MB 分片下 24MB 在飞）；
//   服务端暂存上传返回 chunk_concurrency=3，25MB 分片下 75MB 在飞；
// - XHR upload.onprogress 按实际发送字节更新，多片增量累计、同片重试不倒退；
// - 429/409/网络/5xx 分片退避重传，保持同一 upload_id；
// - failed_retriable 自动从第 0 片重传（OpenList 收到 0 片即重启管线）；
// - 普通直传 complete 先等 30s，超时/反代掐断转 status 长轮询（30 分钟）；
// - 服务端暂存收齐即返回 accepted，后台 OpenList 同步不阻塞用户上传任务；
// - 暂存会话 accepted 后由视图调用 followStagedUpload 后台跟随网盘写入状态，不占用上传队列。

const CHUNK_CONCURRENCY = 6;
const CHUNK_MAX_TRY = 5;
const RESTART_MAX = 2;
const COMPLETE_WAIT_MS = 30 * 1000;
const COMPLETE_POLL_MS = 3000;
const COMPLETE_POLL_MAX_MS = 30 * 60 * 1000;

// expandRanges 断点续传：分片区间数组 [[a,b],…] → 已收分片下标集合。
export function expandRanges(ranges) {
  const set = new Set();
  (ranges || []).forEach((r) => {
    if (!Array.isArray(r)) return;
    for (let i = r[0]; i <= r[1]; i++) set.add(i);
  });
  return set;
}

const defaultSleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// uploadWithAdapter 执行单文件上传；adapter 需实现 uploadInit/uploadChunk/uploadComplete/
// uploadStatus/uploadAbort（见 api.js live/demo 适配器）。
// opts: { file, space, path, adapter, onProgress, isCancelled, sleep? }
export async function uploadWithAdapter({
  file,
  space,
  path,
  adapter,
  onProgress,
  isCancelled,
  sleep = defaultSleep,
}) {
  const cancelled = () => (isCancelled ? isCancelled() : false);
  const init = await adapter.uploadInit({ space, path, name: file.name, size: file.size });
  if (init.instant || init.state === 'completed') {
    if (cancelled()) return { aborted: true };
    if (onProgress)
      onProgress({
        received: file.size,
        size: file.size,
        doneChunks: 0,
        totalChunks: 0,
        storageProgress: 100,
        frontier: 0,
        merging: false,
      });
    return { instant: true };
  }
  const uploadId = init.upload_id;
  if (cancelled()) {
    await adapter.uploadAbort({ uploadId }).catch(() => {});
    return { aborted: true };
  }
  const chunkSize = init.chunk_size || 4 * 1024 * 1024;
  const totalChunks = init.total_chunks || Math.ceil(file.size / chunkSize) || 1;
  const concurrency = Math.max(
    1,
    Math.min(init.chunk_concurrency || CHUNK_CONCURRENCY, CHUNK_CONCURRENCY)
  );
  let received = init.received_bytes || 0;
  let storageProgress = init.storage_progress || 0;
  let frontier = init.frontier || 0;
  let doneChunks = expandRanges(init.received).size;
  const report = (merging = false) => {
    if (onProgress)
      onProgress({
        received,
        size: file.size,
        doneChunks,
        totalChunks,
        storageProgress,
        frontier,
        merging,
      });
  };
  report();

  const applySnap = (snap) => {
    if (!snap) return;
    // 并发分片响应可能乱序到达；同一会话内取最大值，避免进度回退。
    if (snap.received_bytes != null) received = Math.max(received, snap.received_bytes);
    if (snap.storage_progress != null)
      storageProgress = Math.max(storageProgress, snap.storage_progress);
    if (snap.frontier != null) frontier = Math.max(frontier, snap.frontier);
  };

  const runChunks = async (skip) => {
    const indices = [];
    for (let i = 0; i < totalChunks; i++) if (!skip.has(i)) indices.push(i);
    const sent = new Map();
    let cursor = 0;
    let restart = false;
    let stop = false;

    const putChunk = async (idx) => {
      const start = idx * chunkSize;
      const blob = file.slice(start, Math.min(start + chunkSize, file.size));
      for (let attempt = 1; ; attempt++) {
        if (restart || stop || cancelled()) return;
        try {
          const snap = await adapter.uploadChunk({
            uploadId,
            index: idx,
            blob,
            onProgress: (loaded) => {
              const next = Math.max(0, Math.min(blob.size, loaded));
              const previous = sent.get(idx) || 0;
              if (next > previous) {
                sent.set(idx, next);
                received = Math.min(received + next - previous, file.size);
                report();
              }
            },
          });
          if (restart || stop) return;
          applySnap(snap);
          if (snap.received_bytes == null) {
            received = Math.min(received + blob.size - (sent.get(idx) || 0), file.size);
          }
          sent.set(idx, blob.size);
          return;
        } catch (e) {
          if (cancelled()) return;
          if (e.restart) {
            restart = true;
            throw e;
          }
          if (e.retry && attempt < CHUNK_MAX_TRY) {
            await sleep(1000 + Math.random() * 1000);
            continue;
          }
          stop = true;
          throw e;
        }
      }
    };

    const workers = [];
    for (let w = 0; w < concurrency; w++) {
      workers.push(
        (async () => {
          while (!restart && !stop && cursor < indices.length && !cancelled()) {
            const idx = indices[cursor++];
            await putChunk(idx);
            if (restart || stop || cancelled()) return;
            doneChunks++;
            report();
          }
        })()
      );
    }
    const results = await Promise.allSettled(workers);
    if (cancelled()) return { aborted: true };
    if (restart) return { restart: true };
    const bad = results.find((r) => r.status === 'rejected');
    if (bad) throw bad.reason;
    return {};
  };

  const finishUpload = async () => {
    report(true);
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), COMPLETE_WAIT_MS);
    // complete 请求最多等 30s；并行轻量轮询 status，避免等待期进度与
    // “网盘处理/网盘写入”文案完全停住。complete 返回后由下方长轮询接管。
    let statusBusy = false;
    let statusStopped = false;
    const statusTimer = setInterval(async () => {
      if (statusBusy) return;
      statusBusy = true;
      try {
        const s = await adapter.uploadStatus({ uploadId });
        if (statusStopped) return;
        applySnap(s);
        report(true);
      } catch (e) {
        /* 等待期轮询失败不致命，complete/后续长轮询兜底 */
      } finally {
        statusBusy = false;
      }
    }, COMPLETE_POLL_MS);
    try {
      const fin = await adapter.uploadComplete({ uploadId, signal: ac.signal });
      applySnap(fin);
      if (fin && fin.state === 'completed') return {};
    } catch (e) {
      if (cancelled()) return { aborted: true };
      if (e.restart || (e.data && e.data.restart)) return { restart: true };
      if (e.data && e.data.state === 'failed_permanent') {
        throw new Error(e.message || '网盘写入失败');
      }
      // 其余（30s 超时/反代 504/网络异常）转 status 轮询
    } finally {
      statusStopped = true;
      clearTimeout(timer);
      clearInterval(statusTimer);
    }

    const deadline = Date.now() + COMPLETE_POLL_MAX_MS;
    while (Date.now() < deadline) {
      if (cancelled()) return { aborted: true };
      await sleep(COMPLETE_POLL_MS);
      let s;
      try {
        s = await adapter.uploadStatus({ uploadId });
      } catch (e) {
        if (e.code === 404 || /not found/i.test(e.message || '')) {
          throw new Error('上传会话已失效，请重新上传');
        }
        continue; // 网络/5xx 继续轮询到 deadline
      }
      applySnap(s);
      report(true);
      if (s.state === 'completed') return {};
      if (s.state === 'failed_retriable') return { restart: true };
      if (s.state === 'failed_permanent' || s.state === 'aborted') {
        throw new Error(s.error || '网盘写入失败');
      }
    }
    throw new Error('网盘写入确认超时，请稍后刷新目录确认');
  };

  let restarts = 0;
  for (;;) {
    const run = await runChunks(restarts === 0 ? expandRanges(init.received) : new Set());
    if (run.aborted) {
      await adapter.uploadAbort({ uploadId }).catch(() => {});
      return { aborted: true };
    }
    if (run.restart) {
      if (++restarts > RESTART_MAX) throw new Error('网盘写入多次失败，请稍后重试');
      received = 0;
      doneChunks = 0;
      storageProgress = 0;
      frontier = 0;
      report();
      continue;
    }

    if (init.staged) {
      received = file.size;
      doneChunks = totalChunks;
      report();
      return { uploadId, accepted: true, staged: true };
    }

    const fin = await finishUpload();
    if (fin.aborted) {
      await adapter.uploadAbort({ uploadId }).catch(() => {});
      return { aborted: true };
    }
    if (fin.restart) {
      if (++restarts > RESTART_MAX) throw new Error('网盘写入多次失败，请稍后重试');
      received = 0;
      doneChunks = 0;
      storageProgress = 0;
      frontier = 0;
      report();
      continue;
    }
    received = file.size;
    storageProgress = 100;
    frontier = totalChunks;
    doneChunks = totalChunks;
    report();
    return { uploadId };
  }
}

// followStagedUpload 暂存会话 accepted 之后由视图在后台跟随网盘写入终态：
// 不占上传队列、不调 uploadAbort，失败/超时/会话失效都只回结果不抛错。
// opts: { adapter, uploadId, onSnap?, isCancelled?, sleep?, pollMs?, maxMs?, now? }
export async function followStagedUpload({
  adapter,
  uploadId,
  onSnap,
  isCancelled,
  sleep = defaultSleep,
  pollMs = COMPLETE_POLL_MS,
  maxMs = COMPLETE_POLL_MAX_MS,
  now = Date.now,
}) {
  const cancelled = () => (isCancelled ? isCancelled() : false);
  const deadline = now() + maxMs;
  for (;;) {
    await sleep(pollMs);
    if (cancelled()) return { cancelled: true };
    if (now() >= deadline) return { timeout: true };
    let snap;
    try {
      snap = await adapter.uploadStatus({ uploadId });
    } catch (e) {
      // 会话已被清理（服务重启/暂存目录回收）不必再轮询；服务端把上游 not found
      // 统一映射为 HTTP 404 + 业务码 40400「文件或目录不存在」。
      if (e.status === 404 || e.code === 404 || e.code === 40400) return { missing: true };
      if (/not found|不存在/i.test(e.message || '')) return { missing: true };
      continue; // 网络/5xx 继续轮询到 deadline
    }
    if (onSnap) onSnap(snap);
    if (snap.state === 'completed') return { completed: true };
    if (snap.state === 'failed_permanent' || snap.state === 'aborted') {
      return { failed: snap.error || '网盘写入失败' };
    }
  }
}
