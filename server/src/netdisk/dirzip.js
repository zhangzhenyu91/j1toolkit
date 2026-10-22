// 团队网盘：目录 ZIP 流式打包下载。逐层读取 OpenList 目录、逐文件流式写入，
// 保留目录层级与空文件夹；不整读文件入内存、不在网盘侧生成临时压缩包。
const archiver = require('archiver');

// ZIP 内单个路径段：拒绝分隔符/控制符与 .、..，防路径穿越与异常条目；
// 并拒绝 ^\w+: 前缀（archiver-utils.sanitizePath 落盘时会剥掉该前缀，entry 实名与请求名
// 不一致，appendFile 按名匹配将永不完成而挂起）
function zipSegment(name) {
  const n = String(name || '');
  if (!n || n === '.' || n === '..' || /[/\\\r\n]/.test(n) || /^\w+:/.test(n)) {
    const err = new Error('目录内存在不合法的名称，无法打包');
    err.expose = true;
    throw err;
  }
  return n;
}

// 等当前文件真正写入 ZIP 后再处理下一项，避免大目录同时打开大量上游下载流
function appendFile(archive, stream, name) {
  return new Promise((resolve, reject) => {
    let settled = false;
    function cleanup() {
      archive.off('entry', onEntry);
      archive.off('error', onError);
      archive.off('close', onClose);
      stream.off('error', onStreamError);
    }
    function done(err) {
      if (settled) return;
      settled = true;
      cleanup();
      if (err) reject(err);
      else resolve();
    }
    function onEntry(data) {
      if (data && data.name === name) done();
    }
    function onError(err) {
      done(err);
    }
    function onClose() {
      done(new Error('目录打包已中止'));
    }
    function onStreamError(err) {
      done(err);
    }
    archive.on('entry', onEntry);
    archive.on('error', onError);
    archive.on('close', onClose);
    stream.on('error', onStreamError);
    archive.append(stream, { name });
  });
}

async function pipeDirZip(res, absRoot, folderName, ol) {
  const rootName = zipSegment(folderName);
  const archive = archiver('zip', { store: true }); // 网盘文件多为已压缩格式，打包不二次压缩
  let currentStream = null;
  archive.on('warning', (err) => console.warn(`[网盘] 目录 ZIP 警告：${err.message}`));
  archive.on('error', (err) => {
    console.error(`[网盘] 目录 ZIP 错误（${absRoot}）：${err.message}`);
    if (!res.destroyed) res.destroy(err);
  });
  res.on('close', () => {
    if (currentStream && !currentStream.destroyed) currentStream.destroy();
    archive.destroy();
  });
  res.setHeader('Content-Type', 'application/zip');
  res.setHeader(
    'Content-Disposition',
    `attachment; filename="download.zip"; filename*=UTF-8''${encodeURIComponent(`${rootName}.zip`)}`
  );
  archive.pipe(res);

  async function appendDir(abs, zipPath) {
    if (archive.destroyed) return;
    // 每个目录都写入显式目录项，空文件夹也能保留在 ZIP 中
    archive.append(Buffer.alloc(0), { name: `${zipPath}/`, type: 'directory' });
    const data = await ol.fsList(abs);
    const items = (data && data.content) || [];
    for (const item of items) {
      if (archive.destroyed) return;
      const segment = zipSegment(item && item.name);
      const childAbs = `${abs}/${segment}`;
      const childZip = `${zipPath}/${segment}`;
      if (item.is_dir) {
        await appendDir(childAbs, childZip);
      } else {
        const r = await ol.downloadStream(childAbs);
        currentStream = r.data;
        await appendFile(archive, currentStream, childZip);
        currentStream = null;
      }
    }
  }

  await appendDir(absRoot, rootName);
  if (archive.destroyed) return;
  await archive.finalize();
}

module.exports = { pipeDirZip };
