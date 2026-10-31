// 应用目录分组测试（node --test）：每个登记了网页落地路由的应用都必须有已审核分组，
// 否则工作台/侧栏会静默隐藏合法入口（office / file-transfer 曾因此不可见）。
import assert from 'node:assert/strict';
import test from 'node:test';

import { APP_KEYS, APP_GROUPS, groupForApp, metaForApp, routeForApp } from './app-catalog.js';

test('目录内所有有网页落地页的应用都能进入已审核分组', () => {
  const groups = new Set(APP_GROUPS.map((g) => g.id));
  APP_KEYS.forEach((key) => {
    const route = routeForApp(key);
    if (!route) return;
    const group = groupForApp(key);
    assert.ok(group, `${key} 有路由 ${route} 但无分组，会被工作台静默隐藏`);
    assert.ok(groups.has(group), `${key} 的分组 ${group} 不在 APP_GROUPS`);
  });
});

test('办公协同：在线文档与文件传输不再被隐藏', () => {
  assert.equal(groupForApp('office'), 'office');
  assert.equal(groupForApp('file-transfer'), 'tools');
  assert.equal(routeForApp('office'), '/office');
  assert.equal(routeForApp('file-transfer'), '/kvm');
  assert.ok(metaForApp('office'));
  assert.ok(metaForApp('file-transfer'));
});

test('历史别名归一后分组一致', () => {
  assert.equal(groupForApp('filetransfer'), 'tools');
  assert.equal(groupForApp('treehazard'), 'ledger');
  assert.equal(groupForApp('wechatanswer'), 'admin');
});
