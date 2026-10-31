// 团队网盘纯逻辑测试（node --test）：两个解压入口的源目录/包名参数、自身与子目录粘贴防护。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  archiveDecompressParams,
  isDirIntoItself,
  pasteBlockedReason,
} from './logic.js';

test('解压参数：根目录压缩包源目录为空、包名正确', () => {
  const p = archiveDecompressParams({
    item: { name: 'a.zip', _rel: 'a.zip' },
    space: 'my',
    dstDir: 'out',
    putIntoNewDir: true,
    pass: 'pw',
  });
  assert.deepEqual(p, {
    srcSpace: 'my',
    srcDir: '',
    names: ['a.zip'],
    dstSpace: 'my',
    dstDir: 'out',
    putIntoNewDir: true,
    archivePass: 'pw',
  });
});

test('解压参数：搜索结果里的嵌套压缩包按父目录作源目录', () => {
  const p = archiveDecompressParams({
    item: { name: '线路资料.zip', _rel: '2026/资料/线路资料.zip' },
    space: 'public',
    dstDir: '2026/资料',
    putIntoNewDir: false,
  });
  assert.equal(p.srcDir, '2026/资料');
  assert.deepEqual(p.names, ['线路资料.zip']);
  assert.equal(p.dstSpace, 'public');
  assert.equal(p.putIntoNewDir, false);
  assert.equal(p.archivePass, '');
});

test('自身/子目录判定', () => {
  assert.equal(isDirIntoItself('a', 'a'), true);
  assert.equal(isDirIntoItself('a', 'a/b'), true);
  assert.equal(isDirIntoItself('a', 'a/b/c'), true);
  assert.equal(isDirIntoItself('a', 'ab'), false);
  assert.equal(isDirIntoItself('a', ''), false);
  assert.equal(isDirIntoItself('', 'a'), false);
});

test('粘贴防护：同目录、自身/子目录均阻止，跨空间放行', () => {
  const folder = { name: 'a', _rel: 'a', is_dir: true };
  assert.equal(
    pasteBlockedReason({
      items: [folder],
      srcSpace: 'my',
      dstSpace: 'my',
      dstDir: 'a/b',
    }),
    '不能粘贴到自身或其子目录'
  );
  assert.equal(
    pasteBlockedReason({
      items: [folder],
      srcSpace: 'my',
      dstSpace: 'my',
      dstDir: 'a',
    }),
    '不能粘贴到自身或其子目录'
  );
  assert.equal(
    pasteBlockedReason({
      items: [{ name: 'x.txt', _rel: 'd/x.txt', is_dir: false }],
      srcSpace: 'my',
      dstSpace: 'my',
      dstDir: 'd',
    }),
    '源位置与目标位置相同'
  );
  assert.equal(
    pasteBlockedReason({
      items: [folder],
      srcSpace: 'my',
      dstSpace: 'public',
      dstDir: 'a/b',
    }),
    ''
  );
  assert.equal(
    pasteBlockedReason({
      items: [folder],
      srcSpace: 'my',
      dstSpace: 'my',
      dstDir: 'other',
    }),
    ''
  );
});
