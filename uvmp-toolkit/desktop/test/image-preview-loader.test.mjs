import test from 'node:test';
import assert from 'node:assert/strict';

import * as previewCache from '../src/imagetools/lib/preview-cache.mjs';

const { createPreviewLoader } = previewCache;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function imageFile(name, overrides = {}) {
  return {
    name,
    path: `C:/样本/${name}`,
    ext: '.jpg',
    size: 1000,
    mtime: 100,
    format: 'JPEG',
    mime: 'image/jpeg',
    supported: true,
    animated: false,
    width: 100,
    height: 80,
    ...overrides,
  };
}

function setup(overrides = {}) {
  assert.equal(typeof createPreviewLoader, 'function', '缺少全目录预览加载器');
  const calls = { previews: [], reads: [], decodes: [], revoked: [] };
  let urlSeq = 0;
  const preview = async (path, maxSize) => {
    calls.previews.push({ path, maxSize });
    return {
      path,
      url: 'shade-image://local?path=' + encodeURIComponent(path),
      mime: 'image/jpeg',
      width: 100,
      height: 80,
      animated: false,
      cached: false,
    };
  };
  const loader = createPreviewLoader({
    requestPreview: overrides.requestPreview || preview,
    getMaxSize: () => 1920,
    fetchImpl: async (url) => {
      calls.reads.push(url);
      return { ok: true, status: 200, blob: async () => ({ size: 1000 }) };
    },
    createObjectURL: () => `blob:preview-${++urlSeq}`,
    revokeObjectURL: (url) => calls.revoked.push(url),
    createImage: () => {
      const image = {
        src: '',
        naturalWidth: 100,
        naturalHeight: 80,
        decode: async () => {
          calls.decodes.push(image.src);
        },
      };
      return image;
    },
    idle: () => Promise.resolve(),
    maxEntries: 8,
    maxBlobBytes: 1024 * 1024,
    maxDecodedBytes: 1024 * 1024,
    ...overrides.options,
  });
  return { loader, calls };
}

test('预览加载完成协议读取和浏览器 decode，同图再次打开直接命中', async () => {
  const { loader, calls } = setup();
  const file = imageFile('a.jpg');
  const first = await loader.load(file);
  const second = await loader.load(file);
  assert.equal(first, second);
  assert.match(first.url, /^blob:preview-/);
  assert.equal(first.decodedBytes, 100 * 80 * 4);
  assert.equal(calls.previews.length, 1);
  assert.equal(calls.reads.length, 1);
  assert.equal(calls.decodes.length, 1);
  assert.equal(loader.stats().hits, 1);
});

test('同键在飞预览共享一个 Promise，不重复 RPC、读取或解码', async () => {
  const gate = deferred();
  const requests = [];
  const { loader, calls } = setup({
    options: {
      requestPreview: async (path) => {
        requests.push(path);
        await gate.promise;
        return {
          path,
          url: 'shade-image://local?path=' + encodeURIComponent(path),
          width: 100,
          height: 80,
        };
      },
    },
  });
  const file = imageFile('shared.jpg');
  const one = loader.load(file);
  const two = loader.load(file);
  assert.equal(requests.length, 1);
  gate.resolve();
  const [first, second] = await Promise.all([one, two]);
  assert.equal(first, second);
  assert.equal(calls.reads.length, 1);
  assert.equal(calls.decodes.length, 1);
});

test('全目录后台队列按当前距离排序并最终访问所有支持图片', async () => {
  const idleCalls = [];
  const { loader, calls } = setup({
    options: { idle: async (initial) => idleCalls.push(initial) },
  });
  const files = ['0.jpg', '1.jpg', '2.jpg', '3.jpg'].map((name) => imageFile(name));
  loader.setCurrent(files[1]);
  loader.schedule(files, 1);
  await loader.whenIdle();
  assert.deepEqual(
    calls.previews.map((item) => item.path),
    [files[1].path, files[2].path, files[0].path, files[3].path]
  );
  assert.deepEqual(idleCalls, [true, false, false, false]);
  assert.equal(loader.stats().visited, files.length);
  assert.equal(loader.stats().failed, 0);
});

test('缓存超限淘汰最旧可淘汰项并释放 Blob，当前图受保护', async () => {
  const { loader, calls } = setup({ options: { maxEntries: 2 } });
  const files = ['current.jpg', 'middle.jpg', 'new.jpg'].map((name) => imageFile(name));
  loader.setCurrent(files[0]);
  await loader.load(files[0]);
  await loader.load(files[1]);
  await loader.load(files[2]);
  assert.equal(loader.has(files[0]), true);
  assert.equal(loader.has(files[1]), false);
  assert.equal(loader.has(files[2]), true);
  assert.equal(calls.revoked.length, 1);
});

test('换目录会使旧后台结果失效并释放已创建 URL', async () => {
  const started = deferred();
  const gate = deferred();
  const { loader, calls } = setup({
    options: {
      createImage: () => ({
        src: '',
        naturalWidth: 100,
        naturalHeight: 80,
        decode: async () => {
          started.resolve();
          await gate.promise;
        },
      }),
    },
  });
  const pending = loader.load(imageFile('old.jpg'));
  await started.promise;
  loader.reset();
  gate.resolve();
  await assert.rejects(pending, /取消|失效/);
  assert.equal(calls.revoked.length, 1);
  assert.equal(loader.stats().cacheSize, 0);
});

test('解码失败不进入缓存，主动重试会重新读取', async () => {
  let attempts = 0;
  const { loader, calls } = setup({
    options: {
      createImage: () => ({
        src: '',
        naturalWidth: 100,
        naturalHeight: 80,
        decode: async () => {
          attempts += 1;
          if (attempts === 1) throw new Error('broken pixels');
        },
      }),
    },
  });
  const file = imageFile('broken.jpg');
  await assert.rejects(loader.load(file), /IMG_DECODE_FAILED/);
  await loader.load(file);
  assert.equal(calls.reads.length, 2);
  assert.equal(calls.revoked.length, 1);
});

test('首图无元数据的缓存键可在目录返回后转为真实指纹', async () => {
  const { loader, calls } = setup();
  const provisional = imageFile('first.jpg', { size: 0, mtime: 0 });
  const enriched = imageFile('first.jpg', { size: 12345, mtime: 6789 });
  const entry = await loader.load(provisional);
  loader.rekey(provisional, enriched);
  assert.equal(loader.has(provisional), false);
  assert.equal(loader.has(enriched), true);
  assert.equal(await loader.load(enriched), entry);
  assert.equal(calls.reads.length, 1);
});

test('连续切图会解除旧当前图保护，缓存不会越过条目和字节预算', async () => {
  const { loader, calls } = setup({
    options: { maxEntries: 2, maxBlobBytes: 2000, maxDecodedBytes: 64000 },
  });
  const files = ['old.jpg', 'previous.jpg', 'current.jpg'].map((name) => imageFile(name));
  for (const file of files) {
    loader.setCurrent(file);
    await loader.load(file);
  }
  assert.equal(loader.has(files[0]), false);
  assert.equal(loader.has(files[1]), true);
  assert.equal(loader.has(files[2]), true);
  assert.equal(loader.stats().cacheSize, 2);
  assert.equal(loader.stats().blobBytes, 2000);
  assert.equal(loader.stats().decodedBytes, 64000);
  assert.ok(calls.revoked.length >= 1);
});

test('重扫可保留当前成功画面，同时释放其余 Blob 和旧任务', async () => {
  const { loader, calls } = setup();
  const current = imageFile('keep.jpg');
  const other = imageFile('release.jpg');
  loader.setCurrent(current);
  await loader.load(current);
  await loader.load(other);
  loader.reset(current);
  assert.equal(loader.has(current), true);
  assert.equal(loader.has(other), false);
  assert.equal(calls.revoked.length, 1);
  await loader.load(current);
  assert.equal(calls.reads.length, 2);
  assert.equal(loader.stats().hits, 1);
});

test('后台等待期间失效的图不得重新发起预览请求', async () => {
  const started = deferred();
  const release = deferred();
  const { loader, calls } = setup({
    options: {
      idle: async () => {
        started.resolve();
        await release.promise;
      },
    },
  });
  const file = imageFile('invalidate-before-start.jpg');
  loader.schedule([file], 0);
  await started.promise;
  loader.invalidate(file);
  release.resolve();
  await loader.whenIdle();
  assert.equal(calls.previews.length, 0);
  assert.equal(loader.has(file), false);
});
