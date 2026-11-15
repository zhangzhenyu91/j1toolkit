// 批量重命名插件与图片工具独立窗口的双入口导航契约：
// - batch_rename 是普通业务插件页（进业务清单、可收藏、可回填）
// - image_tools 不是插件页，只作为独立窗口启动入口（不进 PAGE_MAP、不渲染组件）
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import {
  NAV_ITEMS,
  BUSINESS_IDS,
  navItem,
  navGroups,
  filterNavItems,
  commandPaletteItems,
  toggleFavorite,
  externalAction,
} from '../src/lib/navigation.mjs';

function read(rel) {
  return readFileSync(fileURLToPath(new URL(`../src/${rel}`, import.meta.url)), 'utf8');
}

test('批量重命名进主应用分组，顺序紧跟机巡审核', () => {
  const ids = NAV_ITEMS.map((item) => item.id);
  assert.equal(ids.indexOf('batch_rename'), ids.indexOf('defect_review') + 1);
  const groups = navGroups({ expandedDeveloper: true });
  const primary = groups.find((group) => group.id === 'primary');
  assert.deepEqual(
    primary.items.map((item) => item.id),
    ['pcd_export', 'quiz_search', 'course_autoplay', 'defect_review', 'batch_rename', 'image_tools']
  );
  assert.equal(navItem('batch_rename').business, true);
  assert.equal(navItem('batch_rename').icon, 'rename');
  assert.ok(BUSINESS_IDS.includes('batch_rename'));
});

test('图片工具是独立窗口入口：不进业务清单、不参与收藏', () => {
  const item = navItem('image_tools');
  assert.equal(item.business, undefined);
  assert.equal(item.external, 'openImageTools');
  assert.equal(externalAction('image_tools'), 'openImageTools');
  assert.equal(externalAction('batch_rename'), '');
  assert.equal(externalAction('home'), '');
  assert.equal(BUSINESS_IDS.includes('image_tools'), false);
  assert.deepEqual(toggleFavorite([], 'image_tools'), []);
  assert.equal(filterNavItems('图片')[0].id, 'image_tools');
});

test('命令面板：两个新入口都可搜索，图片工具没有收藏项', () => {
  const items = commandPaletteItems({ favorites: [] });
  const navigate = items.filter((item) => item.kind === 'navigate');
  assert.deepEqual(
    navigate
      .filter((item) => ['batch_rename', 'image_tools'].includes(item.pageID))
      .map((i) => i.title),
    ['批量重命名', '图片工具']
  );
  // 图片工具不是业务插件：命令面板不给收藏项；点它由壳层 externalAction 分流
  assert.equal(
    items.filter((item) => item.kind === 'toggleFavorite' && item.pageID === 'image_tools').length,
    0
  );
  assert.equal(items.length, NAV_ITEMS.length + BUSINESS_IDS.length);
  assert.equal(BUSINESS_IDS.length, 8);
});

test('App.vue：只登记 batch_rename 插件页，image_tools 走 openImageTools', () => {
  const source = read('App.vue');
  assert.match(source, /import BatchRenamePage from '\.\/pages\/BatchRenamePage\.vue';/);
  assert.match(source, /batch_rename: BatchRenamePage,/);
  // 图片工具不是插件页：PAGE_MAP 里不能出现 image_tools 的页面映射
  assert.doesNotMatch(source, /image_tools:\s*[A-Za-z]/);
  // 外部入口必须在 PAGE_MAP 守卫之前分流，否则点击无反应
  assert.match(
    source,
    /function navigate\(id\) \{\s*const action = externalAction\(id\);\s*if \(action\) \{[\s\S]*?return;[\s\S]*?\}\s*if \(!PAGE_MAP\[id\]\) return;/
  );
  assert.match(source, /await window\.core\.openImageTools\(\)/);
  assert.match(source, /externalAction/);
});

test('App.vue：外部入口打开失败要显式提示，不静默吞掉', () => {
  const source = read('App.vue');
  assert.match(source, /图片工具窗口打开失败/);
  assert.match(source, /MessagePlugin\.error/);
});

test('命令面板：独立窗口入口标注「另开窗口」，插件页不带该标记', () => {
  const items = commandPaletteItems({ favorites: [] });
  const image = items.find((item) => item.kind === 'navigate' && item.pageID === 'image_tools');
  const batch = items.find((item) => item.kind === 'navigate' && item.pageID === 'batch_rename');
  assert.equal(image.external, 'openImageTools');
  assert.equal(batch.external, '');
  const source = read('components/CommandPalette.vue');
  assert.match(source, /v-else-if="item\.external"/);
  assert.match(source, /另开窗口/);
});

test('图标集只保留被引用的图形：新增图标在、未用途的移除', () => {
  const source = read('components/ShadeIcon.vue');
  for (const name of ['rename', 'image', 'doc', 'sheet', 'text', 'arrow-right']) {
    assert.match(source, new RegExp(`'${name}':|\\b${name}:`), `缺少图标 ${name}`);
  }
  assert.doesNotMatch(source, /folder-plus/);
});
