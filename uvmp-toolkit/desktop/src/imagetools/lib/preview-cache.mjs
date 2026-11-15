// 预览缓存与全目录后台加载：键必须含真实路径 + mtime + size。
// Blob URL 与图像对象只在当前窗口会话内保留，淘汰/换目录/关窗时必须释放。

/** 缓存键：路径 + 修改时间 + 字节数，三者任一变化都视为不同的图。 */
export function previewKey(file) {
  if (!file || !file.path) return '';
  const mtime = Number(file.mtime) || 0;
  const size = Number(file.size) || 0;
  return `${file.path}|${mtime}|${size}`;
}

/** 同时限制条目、压缩字节与估算解码字节的 LRU；当前图可由调用方保护。 */
export function createPreviewCache({
  limit = 12,
  maxBlobBytes = Number.POSITIVE_INFINITY,
  maxDecodedBytes = Number.POSITIVE_INFINITY,
  onEvict = () => {},
} = {}) {
  const map = new Map();
  const protectedKeys = new Set();
  const max = Math.max(1, Math.floor(Number(limit) || 12));
  const blobLimit = Math.max(0, Number(maxBlobBytes) || 0);
  const decodedLimit = Math.max(0, Number(maxDecodedBytes) || 0);
  let blobBytes = 0;
  let decodedBytes = 0;

  function cost(value, key) {
    return Math.max(0, Number(value && value[key]) || 0);
  }

  function remove(key) {
    if (!map.has(key)) return false;
    const value = map.get(key);
    map.delete(key);
    protectedKeys.delete(key);
    blobBytes -= cost(value, 'blobBytes');
    decodedBytes -= cost(value, 'decodedBytes');
    onEvict(value, key);
    return true;
  }

  function trim() {
    for (const [key] of map) {
      if (map.size <= max && blobBytes <= blobLimit && decodedBytes <= decodedLimit) return;
      if (protectedKeys.has(key)) continue;
      remove(key);
    }
  }

  return {
    get size() {
      return map.size;
    },
    get blobBytes() {
      return blobBytes;
    },
    get decodedBytes() {
      return decodedBytes;
    },
    protect(file) {
      const key = previewKey(file);
      if (key) {
        protectedKeys.delete(key);
        protectedKeys.add(key);
        while (protectedKeys.size > 2) {
          protectedKeys.delete(protectedKeys.values().next().value);
        }
      }
      trim();
    },
    has(file) {
      const key = previewKey(file);
      return !!key && map.has(key);
    },
    get(file) {
      const key = previewKey(file);
      if (!key || !map.has(key)) return undefined;
      const value = map.get(key);
      map.delete(key);
      map.set(key, value);
      return value;
    },
    set(file, value) {
      const key = previewKey(file);
      if (!key) return value;
      remove(key);
      map.set(key, value);
      blobBytes += cost(value, 'blobBytes');
      decodedBytes += cost(value, 'decodedBytes');
      trim();
      return value;
    },
    rekey(fromFile, toFile) {
      const from = previewKey(fromFile);
      const to = previewKey(toFile);
      if (!from || !to || from === to || !map.has(from)) return false;
      const value = map.get(from);
      const wasProtected = protectedKeys.has(from);
      map.delete(from);
      protectedKeys.delete(from);
      remove(to);
      map.set(to, value);
      if (wasProtected) protectedKeys.add(to);
      trim();
      return true;
    },
    delete(file) {
      const key = previewKey(file);
      return key ? remove(key) : false;
    },
    clear(preserveFile) {
      const preserve = previewKey(preserveFile);
      for (const key of [...map.keys()]) {
        if (!preserve || key !== preserve) remove(key);
      }
      protectedKeys.clear();
      if (preserve && map.has(preserve)) protectedKeys.add(preserve);
      if (!map.size) {
        blobBytes = 0;
        decodedBytes = 0;
      }
    },
  };
}

function cancelledError() {
  const error = new Error('预览已取消或失效');
  error.name = 'AbortError';
  return error;
}

function decodeError(detail) {
  return new Error(`IMG_DECODE_FAILED: ${detail || '无法解码图片'}`);
}

function localPreviewUrl(value) {
  const url = String(value || '');
  return /^shade-image:\/\/local\/?\?path=[^&]+$/.test(url);
}

function idleTask(initial = false) {
  // 首轮给用户切图一个抢先窗口；后续仍以低优先级快速覆盖全目录。
  return new Promise((resolve) => setTimeout(resolve, initial ? 60 : 8));
}

/**
 * 当前图与全目录后台预加载共用一个入口。
 * requestPreview 只准备路径；load 还会读取 Blob 并完成浏览器 decode。
 */
export function createPreviewLoader({
  requestPreview,
  getMaxSize = () => 2560,
  fetchImpl = globalThis.fetch ? globalThis.fetch.bind(globalThis) : null,
  createObjectURL = (blob) => URL.createObjectURL(blob),
  revokeObjectURL = (url) => URL.revokeObjectURL(url),
  createImage = () => new Image(),
  idle = idleTask,
  onBackgroundError = () => {},
  now = () => performance.now(),
  maxEntries = 48,
  maxBlobBytes = 128 * 1024 * 1024,
  maxDecodedBytes = 256 * 1024 * 1024,
} = {}) {
  if (typeof requestPreview !== 'function') throw new Error('缺少预览请求函数');
  if (typeof fetchImpl !== 'function') throw new Error('缺少本地图片读取能力');

  let generation = 0;
  let currentKey = '';
  let queue = [];
  let visited = new Set();
  let background = null;
  let disposed = false;
  const inFlight = new Map();
  const versions = new Map();
  const idleWaiters = new Set();
  const metrics = { hits: 0, failed: 0, previews: 0, reads: 0, decodes: 0, last: null };

  function release(entry) {
    if (!entry || entry.released) return;
    entry.released = true;
    if (entry.image) {
      try {
        entry.image.src = '';
      } catch {
        // 图像对象已销毁时引用仍会随缓存淘汰释放。
      }
      entry.image = null;
    }
    if (entry.url) revokeObjectURL(entry.url);
  }

  const cache = createPreviewCache({
    limit: maxEntries,
    maxBlobBytes,
    maxDecodedBytes,
    onEvict: release,
  });

  function version(file) {
    return versions.get(previewKey(file)) || 0;
  }

  function active(gen, file, ver) {
    return !disposed && gen === generation && version(file) === ver;
  }

  function assertActive(gen, file, ver) {
    if (!active(gen, file, ver)) throw cancelledError();
  }

  function load(file, { force = false } = {}) {
    const key = previewKey(file);
    if (!key) return Promise.reject(decodeError('图片路径或缓存指纹为空'));
    if (file.supported === false) return Promise.reject(decodeError('不支持的图片格式'));
    if (force) {
      versions.set(key, version(file) + 1);
      cache.delete(file);
    } else {
      const cached = cache.get(file);
      if (cached) {
        metrics.hits += 1;
        return Promise.resolve(cached);
      }
    }

    const gen = generation;
    const ver = version(file);
    const requestKey = `${gen}:${key}:${ver}`;
    if (inFlight.has(requestKey)) return inFlight.get(requestKey);

    const promise = (async () => {
      const started = now();
      const coreStarted = now();
      const preview = await requestPreview(file.path, getMaxSize());
      metrics.previews += 1;
      const coreMs = now() - coreStarted;
      assertActive(gen, file, ver);
      if (!localPreviewUrl(preview && preview.url)) throw decodeError('预览地址不合法');

      const readStarted = now();
      const response = await fetchImpl(preview.url);
      metrics.reads += 1;
      if (!response || !response.ok) {
        throw decodeError(`预览读取失败（${response ? response.status : '无响应'}）`);
      }
      const blob = await response.blob();
      assertActive(gen, file, ver);
      const readMs = now() - readStarted;

      const entry = {
        ...preview,
        sourceUrl: preview.url,
        url: createObjectURL(blob),
        image: null,
        blobBytes: Math.max(0, Number(blob && blob.size) || 0),
        decodedBytes: 0,
        released: false,
      };
      try {
        const decodeStarted = now();
        const image = createImage();
        entry.image = image;
        image.src = entry.url;
        assertActive(gen, file, ver);
        await image.decode();
        metrics.decodes += 1;
        const width = Number(image.naturalWidth || preview.width) || 0;
        const height = Number(image.naturalHeight || preview.height) || 0;
        if (!(width > 0 && height > 0)) throw decodeError('图片尺寸无效');
        entry.width = width;
        entry.height = height;
        entry.decodedBytes = width * height * 4;
        entry.decodeMs = now() - decodeStarted;
        assertActive(gen, file, ver);
        cache.set(file, entry);
        metrics.last = {
          path: file.path,
          coreMs,
          readMs,
          decodeMs: entry.decodeMs,
          totalMs: now() - started,
          cached: cache.has(file),
        };
        return entry;
      } catch (error) {
        release(entry);
        if (error && error.name === 'AbortError') throw error;
        if (/^IMG_DECODE_FAILED:/.test(String((error && error.message) || error))) throw error;
        throw decodeError((error && error.message) || String(error));
      }
    })();

    inFlight.set(requestKey, promise);
    const cleanup = () => {
      if (inFlight.get(requestKey) === promise) inFlight.delete(requestKey);
    };
    promise.then(cleanup, cleanup);
    promise.then(
      () => {},
      () => {
        if (active(gen, file, ver)) metrics.failed += 1;
      }
    );
    return promise;
  }

  function notifyIdle() {
    if (background || queue.length) return;
    for (const resolve of idleWaiters) resolve();
    idleWaiters.clear();
  }

  function startBackground() {
    if (background || !queue.length || disposed) return;
    const gen = generation;
    let initial = true;
    const run = (async () => {
      while (queue.length && gen === generation && !disposed) {
        const file = queue.shift();
        const key = previewKey(file);
        if (!key || visited.has(key) || cache.has(file)) continue;
        visited.add(key);
        try {
          await idle(initial);
          initial = false;
          if (gen !== generation || disposed) break;
          await load(file);
        } catch (error) {
          if (!error || error.name !== 'AbortError') onBackgroundError(error, file);
        }
      }
    })();
    background = run;
    const finish = () => {
      if (background !== run) return;
      background = null;
      if (queue.length && gen === generation) startBackground();
      else notifyIdle();
    };
    run.then(finish, finish);
  }

  return {
    load,
    has(file) {
      return cache.has(file);
    },
    setCurrent(file) {
      currentKey = previewKey(file);
      if (currentKey) cache.protect(file);
    },
    schedule(files, center = 0) {
      if (disposed) return;
      const list = Array.isArray(files) ? files : [];
      const at = Math.max(0, Math.min(list.length - 1, Number(center) || 0));
      queue = list
        .map((file, index) => ({ file, index }))
        .filter(
          ({ file }) =>
            file && file.supported !== false && !visited.has(previewKey(file)) && !cache.has(file)
        )
        .sort((a, b) => {
          const da = a.index - at;
          const db = b.index - at;
          const distance = Math.abs(da) - Math.abs(db);
          if (distance) return distance;
          if (da === db) return 0;
          return da > 0 ? -1 : 1;
        })
        .map(({ file }) => file);
      startBackground();
    },
    rekey(fromFile, toFile) {
      const moved = cache.rekey(fromFile, toFile);
      if (moved && currentKey === previewKey(fromFile)) {
        currentKey = previewKey(toFile);
        cache.protect(toFile);
      }
      return moved;
    },
    invalidate(file) {
      const key = previewKey(file);
      if (!key) return;
      versions.set(key, version(file) + 1);
      cache.delete(file);
      visited.delete(key);
      queue = queue.filter((item) => previewKey(item) !== key);
    },
    reset(preserveFile) {
      generation += 1;
      queue = [];
      visited = new Set();
      versions.clear();
      currentKey = '';
      cache.clear(preserveFile);
      notifyIdle();
    },
    dispose() {
      if (disposed) return;
      this.reset();
      disposed = true;
    },
    stats() {
      return {
        cacheSize: cache.size,
        blobBytes: cache.blobBytes,
        decodedBytes: cache.decodedBytes,
        queued: queue.length,
        running: !!background,
        visited: visited.size,
        ...metrics,
      };
    },
    async whenIdle() {
      while (background || queue.length) {
        await new Promise((resolve) => idleWaiters.add(resolve));
      }
    },
  };
}
