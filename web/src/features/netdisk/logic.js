// 团队网盘纯逻辑：解压源目录/包名（两个解压入口共用）、自身/子目录粘贴防护。
// 视图只消费本文件，node:test 直接导入，不加载 SFC。

// parentDir 相对路径父目录（'a/b.zip' → 'a'，'a.zip' → ''）。
function parentDir(rel) {
  const parts = String(rel || '')
    .split('/')
    .filter(Boolean);
  parts.pop();
  return parts.join('/');
}

// baseName 相对路径末段文件名。
function baseName(rel) {
  const parts = String(rel || '')
    .split('/')
    .filter(Boolean);
  return parts.length ? parts[parts.length - 1] : '';
}

// archiveSourceDir 压缩包所在源目录：必须由条目相对路径推导，不能用「当前浏览目录」——
// 搜索结果里的条目可能位于任意嵌套目录，用当前目录会把 /decompress 的源指错。
export function archiveSourceDir(rel) {
  return parentDir(rel);
}

// archiveDecompressParams 解压接口参数（上下文菜单与压缩包弹层两个入口共用同一口径）。
export function archiveDecompressParams({
  item,
  space,
  dstDir = '',
  putIntoNewDir = true,
  pass = '',
} = {}) {
  const rel = String((item && item._rel) || '');
  const name = (item && item.name) || baseName(rel);
  return {
    srcSpace: space,
    srcDir: archiveSourceDir(rel),
    names: [name],
    dstSpace: space,
    dstDir,
    putIntoNewDir: putIntoNewDir !== false,
    archivePass: pass || '',
  };
}

// isDirIntoItself 目标目录是否为源目录自身或其子目录。
export function isDirIntoItself(srcRel, dstDir) {
  const src = String(srcRel || '');
  const dst = String(dstDir || '');
  if (!src || !dst) return false;
  return dst === src || dst.startsWith(src + '/');
}

// pasteBlockedReason 移动/复制目标校验：返回阻止原因（空串=放行）。
// 跨空间时不存在自身/子目录关系；同空间必须同时拦住「源=目标」与「文件夹粘进自身/子目录」。
export function pasteBlockedReason({ items = [], srcSpace = '', dstSpace = '', dstDir = '' } = {}) {
  const list = Array.isArray(items) ? items.filter(Boolean) : [];
  if (srcSpace === dstSpace) {
    const sourceDirs = new Set(list.map((it) => parentDir(it._rel)));
    if (sourceDirs.size === 1 && sourceDirs.has(String(dstDir || ''))) {
      return '源位置与目标位置相同';
    }
    for (const it of list) {
      if (it.is_dir && isDirIntoItself(it._rel, dstDir)) return '不能粘贴到自身或其子目录';
    }
  }
  return '';
}
