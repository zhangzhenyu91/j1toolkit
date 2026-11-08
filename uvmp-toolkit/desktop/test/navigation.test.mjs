import test from 'node:test';
import assert from 'node:assert/strict';

import {
  NAV_ITEMS,
  navGroups,
  filterNavItems,
  commandPaletteItems,
  filterCommandItems,
  toggleFavorite,
  SETTINGS_SECTIONS,
  settingsMatches,
  matchedSettingsIDs,
} from '../src/lib/navigation.mjs';

test('十二页顺序与分组符合壳层现状', () => {
  assert.deepEqual(
    NAV_ITEMS.map((item) => item.id),
    [
      'home',
      'history',
      'pcd_export',
      'quiz_search',
      'course_autoplay',
      'defect_review',
      'cluster',
      'daily_sync',
      'tree_sync',
      'crossing_sync',
      'dev_script',
      'settings',
    ]
  );
  const groups = navGroups({ expandedDeveloper: false });
  assert.deepEqual(
    groups.map((group) => group.id),
    ['workbench', 'primary', 'platform']
  );
  assert.deepEqual(
    groups[0].items.map((item) => item.id),
    ['home', 'history']
  );
  assert.deepEqual(
    groups[1].items.map((item) => item.id),
    ['pcd_export', 'quiz_search', 'course_autoplay', 'defect_review']
  );
  assert.deepEqual(
    groups[2].items.map((item) => item.id),
    ['cluster']
  );
});

test('开发者工具展开后才出现；搜索时自动展开且只保留命中项', () => {
  const expanded = navGroups({ expandedDeveloper: true });
  assert.deepEqual(
    expanded.map((group) => group.id),
    ['workbench', 'primary', 'platform', 'developer']
  );
  assert.deepEqual(
    expanded[3].items.map((item) => item.id),
    ['daily_sync', 'tree_sync', 'crossing_sync', 'dev_script']
  );

  const searching = navGroups({ expandedDeveloper: false, query: '同步' });
  assert.deepEqual(
    searching.map((group) => group.id),
    ['developer']
  );
  assert.deepEqual(
    searching[0].items.map((item) => item.id),
    ['daily_sync', 'tree_sync', 'crossing_sync']
  );
});

test('侧栏搜索按标题与描述做大小写不敏感过滤', () => {
  assert.deepEqual(
    filterNavItems('题库').map((item) => item.id),
    ['quiz_search']
  );
  assert.deepEqual(
    filterNavItems('PDF').map((item) => item.id),
    ['pcd_export']
  );
  assert.deepEqual(filterNavItems('不存在的工具'), []);
});

test('命令面板包含十二页导航，业务工具额外提供收藏切换', () => {
  const items = commandPaletteItems({ favorites: ['pcd_export'] });
  assert.equal(items.length, NAV_ITEMS.length + 7);
  assert.equal(items[0].kind, 'navigate');
  assert.equal(items[0].pageID, 'home');
  const favoriteItems = items.filter((item) => item.kind === 'toggleFavorite');
  assert.equal(favoriteItems.length, 7);
  assert.equal(
    favoriteItems.find((item) => item.pageID === 'pcd_export').title,
    '取消收藏 轨迹派车单导出'
  );
  assert.equal(favoriteItems.find((item) => item.pageID === 'quiz_search').title, '收藏 题库搜题');
});

test('命令面板过滤与收藏切换是纯函数', () => {
  const items = commandPaletteItems({ favorites: [] });
  const filtered = filterCommandItems(items, '设置');
  assert.deepEqual(
    filtered.map((item) => item.title),
    ['设置']
  );
  assert.deepEqual(filterCommandItems(items, '没有这个').length, 0);
  assert.deepEqual(toggleFavorite([], 'tree_sync'), ['tree_sync']);
  assert.deepEqual(toggleFavorite(['tree_sync', 'pcd_export'], 'tree_sync'), ['pcd_export']);
  assert.deepEqual(toggleFavorite([], 'home'), []);
});

test('设置搜索按组名/字段/说明/关键词命中', () => {
  assert.equal(SETTINGS_SECTIONS.length, 5);
  assert.deepEqual(
    matchedSettingsIDs(''),
    SETTINGS_SECTIONS.map((s) => s.id)
  );
  assert.deepEqual(matchedSettingsIDs('并发'), ['export-background']);
  assert.deepEqual(matchedSettingsIDs('sso'), ['account']);
  assert.deepEqual(matchedSettingsIDs('日志'), ['developer-diagnostics']);
  assert.deepEqual(matchedSettingsIDs('快捷键'), ['shortcuts-about']);
  assert.deepEqual(matchedSettingsIDs('完全不存在'), []);
  assert.deepEqual(settingsMatches('浏览器'), [false, true, false, false, true]);
});
