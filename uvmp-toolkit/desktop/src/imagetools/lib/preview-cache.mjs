// 预览结果缓存：键必须含真实路径 + mtime + size。
// 文件被外部改名/覆盖后 mtime 与 size 会变，旧预览自然失效；
// 预览失效（文件被替换、窗口重建、协议返回 404）时调用方先 delete 再强制重取。

/** 缓存键：路径 + 修改时间 + 字节数，三者任一变化都视为不同的图。 */
export function previewKey(file) {
  if (!file || !file.path) return '';
  const mtime = Number(file.mtime) || 0;
  const size = Number(file.size) || 0;
  return `${file.path}|${mtime}|${size}`;
}

/** 有上限的 LRU 预览缓存（只存 IPC 返回的小对象，不存像素）。 */
export function createPreviewCache({ limit = 12 } = {}) {
  const map = new Map();
  const max = Math.max(1, Math.floor(Number(limit) || 12));
  return {
    get size() {
      return map.size;
    },
    has(file) {
      const key = previewKey(file);
      return !!key && map.has(key);
    },
    get(file) {
      const key = previewKey(file);
      if (!key || !map.has(key)) return undefined;
      const value = map.get(key);
      // 触摸：最近使用的移到队尾，淘汰时先丢最旧的
      map.delete(key);
      map.set(key, value);
      return value;
    },
    set(file, value) {
      const key = previewKey(file);
      if (!key) return value;
      map.delete(key);
      map.set(key, value);
      while (map.size > max) map.delete(map.keys().next().value);
      return value;
    },
    delete(file) {
      const key = previewKey(file);
      if (!key) return false;
      return map.delete(key);
    },
    clear() {
      map.clear();
    },
  };
}
