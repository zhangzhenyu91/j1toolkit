import test from 'node:test';
import assert from 'node:assert/strict';
import { makeInstance, nextCdpPort } from '../src/lib/instances.mjs';

test('实例端口按应用基址分配且避开已占用端口', () => {
  assert.equal(nextCdpPort([], 9300), 'http://127.0.0.1:9300');
  assert.equal(
    nextCdpPort(
      [
        { cdp_url: 'http://127.0.0.1:9300' },
        { cdp_url: 'http://127.0.0.1:9301' },
      ],
      9300
    ),
    'http://127.0.0.1:9302'
  );
  assert.equal(nextCdpPort([], 9400), 'http://127.0.0.1:9400');
});

test('新建实例生成独立 ID、端口与空参数', () => {
  const instance = makeInstance('defect_review', [], '实例 1', 9400);
  assert.equal(instance.name, '实例 1');
  assert.ok(instance.id.startsWith('defect_review-'));
  assert.equal(instance.cdp_url, 'http://127.0.0.1:9400');
  assert.deepEqual(instance.params, {});
});
