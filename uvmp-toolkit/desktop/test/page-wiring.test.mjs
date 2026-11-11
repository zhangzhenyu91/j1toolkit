// 页面接线约束：这些行为只能从 SFC 源码确认，无法在纯 Node 里跑组件。
// 断言的是「接线存在且不再退化成旧写法」，而不是模板细节。
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

function read(rel) {
  return readFileSync(fileURLToPath(new URL(`../src/${rel}`, import.meta.url)), 'utf8');
}

test('机巡页：大屏「全部停止」走已确认路径，不再二次确认', () => {
  const source = read('pages/DefectReviewPage.vue');
  assert.match(source, /api\.onStopAll\(\(\) => \{\s*stopAllInstances\(\{ skipConfirm: true \}\);/);
  assert.match(source, /async function stopAllInstances\(\{ skipConfirm = false \} = \{\}\)/);
  assert.match(source, /if \(skipConfirm\) \{\s*await run\(\);/);
});

test('机巡页：离开页面停大屏同步，任务本身继续跑', () => {
  const source = read('pages/DefectReviewPage.vue');
  assert.match(source, /onDeactivated\(\(\) => \{\s*stopScreensaverSync\(\);\s*\}\);/);
  assert.match(source, /onActivated\(\(\) => \{[\s\S]*?refreshScreensaverState\(\);/);
});

test('机巡页：上一个任务的 finished 不再是无上限等待', () => {
  const source = read('pages/DefectReviewPage.vue');
  assert.doesNotMatch(source, /await previous\.finished;/);
  assert.match(source, /await waitFinished\(previous, \d+\)/);
});

test('学堂页：上一个任务的 finished 同样有超时兜底', () => {
  const source = read('pages/CourseAutoplayPage.vue');
  assert.doesNotMatch(source, /await previousJob\.finished;/);
  assert.match(source, /await waitFinished\(previousJob, \d+\)/);
});

test('机巡页：密码不回显掩码，留空沿用已存值', () => {
  const source = read('pages/DefectReviewPage.vue');
  assert.match(source, /form\.password = '';/);
  assert.doesNotMatch(source, /form\.password = params\.password/);
  assert.match(source, /resolveSecret\(form\.password, storedPassword\.value\)/);
  assert.match(source, /carrySecret\(/);
  // params 与 instance.params 两层都必须删占位密码，只有一层会漏到核心
  assert.match(source, /stripSecretPlaceholders\(\(instance && instance\.params\) \|\| \{\}\)/);
  assert.match(source, /instance: instance \? instancePatch : undefined/);
});

test('PCD / 每日同步：启动流程有同步占位，重复触发不连开两个任务', () => {
  for (const file of ['pages/PcdExportPage.vue', 'pages/DailySyncPage.vue']) {
    const source = read(file);
    assert.match(source, /createSingleFlight\(\)/, `${file} 未使用单飞占位`);
    assert.match(source, /starter\.run\(/, `${file} 未通过单飞执行启动`);
  }
});

test('PCD / 集群页：切走页面停轮询，切回续跟未完成任务', () => {
  const pcd = read('pages/PcdExportPage.vue');
  assert.match(pcd, /onDeactivated\(stopClusterPoll\);/);
  assert.match(pcd, /if \(clusterJobId\.value\) startClusterPoll\(\);/);
  const cluster = read('pages/ClusterPage.vue');
  assert.match(
    cluster,
    /onDeactivated\(\(\) => \{[\s\S]*?stopPolling\(\);[\s\S]*?stopUpdatePoll\(\);/
  );
  assert.match(cluster, /\['pending', 'running'\]\.includes\(updateJob\.value\.status\)/);
});

test('集群页：轮询连续失败要显式告知，不把旧快照当实时状态', () => {
  const source = read('pages/ClusterPage.vue');
  assert.match(source, /statusFailures\.fail\(\)/);
  assert.match(source, /statusStale/);
  assert.match(source, /updateFailures\.fail\(\)/);
});

test('PCD 集群轮询：连续失败收口并标注状态未知', () => {
  const source = read('pages/PcdExportPage.vue');
  assert.match(source, /clusterFailures\.fail\(\)/);
  assert.match(source, /status: '状态未知'/);
});

test('执行历史：读取失败要展示，不静默当空列表', () => {
  const card = read('components/HistoryCard.vue');
  assert.match(card, /loadError\.value = String/);
  assert.match(card, /v-if="loadError"/);
  const page = read('pages/HistoryPage.vue');
  assert.match(page, /if \(loading\.value\) return;/);
});

test('首页壳层：重启核心与核心退出都统一收口在跑任务', () => {
  const source = read('App.vue');
  assert.match(source, /abortAllJobs\('核心已重启，任务已中断'\)/);
  assert.match(source, /ev\.event !== 'core\.exit'/);
  assert.match(source, /abortAllJobs\(`核心进程已退出/);
});

test('同步页：重复触发启动直接返回', () => {
  for (const file of ['pages/TreeSyncPage.vue', 'pages/CrossingSyncPage.vue']) {
    const source = read(file);
    assert.match(
      source,
      /function start\(params, beginText\) \{\s*\/\/[^\n]*\n\s*if \(running\.value\) return;/
    );
  }
});

test('执行历史页：打开目录按整条记录传 job_id，旧档案才能精确定位', () => {
  const source = read('pages/HistoryPage.vue');
  assert.match(source, /@click="openDir\(entry\)"/);
  assert.match(source, /async function openDir\(entry\) \{/);
  assert.match(source, /call\('openPath', \{ path: dir, job_id: entry\.rec\.job_id \}\)/);
});

test('最近执行记录卡：打开目录带上当条记录的 job_id', () => {
  const source = read('components/HistoryCard.vue');
  assert.match(source, /@click="openDir\(row\)"/);
  assert.match(source, /async function openDir\(row\) \{/);
  assert.match(source, /call\('openPath', \{ path: row\.dir, job_id: row\.job_id \}\)/);
});

test('任务结果卡：有 jobId 时打开产物带 job_id，没有时只传路径', () => {
  const source = read('components/TaskResult.vue');
  assert.match(source, /jobId: \{ type: String, default: '' \}/);
  assert.match(source, /const params = \{ path \};/);
  assert.match(source, /if \(props\.jobId\) params\.job_id = props\.jobId;/);
  assert.match(source, /await call\('openPath', params\)/);
});

test('设置页：dev.allow_scripts 与 dev.mock 同次保存，关闭即禁止脚本测试', () => {
  const source = read('pages/SettingsPage.vue');
  assert.match(source, /v-model="devAllowScripts"/);
  assert.match(source, /devAllowScripts\.value = \(cfg\.dev \|\| \{\}\)\.allow_scripts !== false;/);
  assert.match(
    source,
    /call\('saveConfig', \{\s*dev: \{ mock: devMock\.value, allow_scripts: devAllowScripts\.value \},?\s*\}\)/
  );
  assert.match(source, /关闭后[^<]*脚本测试/);
});

test('脚本测试页：读得到开关就按开关禁用运行，并提示脚本可读本机配置凭据', () => {
  const source = read('pages/DevScriptPage.vue');
  assert.match(source, /const allowScripts = ref\(true\);/);
  assert.match(source, /allowScripts\.value = \(cfg\.dev \|\| \{\}\)\.allow_scripts !== false;/);
  assert.match(source, /running \|\| !allowScripts/);
  assert.match(source, /if \(!allowScripts\.value\) \{/);
  assert.match(source, /本机配置|凭据/);
});
