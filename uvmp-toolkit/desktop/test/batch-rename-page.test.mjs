// 批量重命名：纯函数契约（预览参数、混合选择、冲突与结果映射）+ 页面接线静态约束。
// 设计稿：design/桌面-批量重命名.html；后端契约：toolkit/apps/batch_rename/backend.py。
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import {
  APP_ID,
  kindOf,
  typeText,
  splitName,
  createRules,
  mergePickedFiles,
  buildRenameParams,
  previewCounts,
  rowTag,
  rowNameTone,
  rulesSummary,
  conflictTip,
  sequenceExample,
  batchOutcome,
  formatBatchHistory,
  resultRows,
  restoreBatchParams,
  pathKey,
  progressPlan,
  renameCounts,
  removeFile,
} from '../src/lib/batch-rename.mjs';

function read(rel) {
  return readFileSync(fileURLToPath(new URL(`../src/${rel}`, import.meta.url)), 'utf8');
}

test('文件类型只做展示分组，不绑定图片格式', () => {
  assert.equal(APP_ID, 'batch_rename');
  assert.equal(kindOf('现场照片.JPG'), 'image');
  assert.equal(kindOf('缺陷记录.docx'), 'doc');
  assert.equal(kindOf('工器具台账.xlsx'), 'sheet');
  assert.equal(kindOf('巡检说明.txt'), 'text');
  assert.equal(kindOf('压缩包.zip'), 'other');
  assert.equal(kindOf('无扩展名'), 'other');
  assert.equal(typeText('现场照片.JPG'), 'JPG · 图片');
  assert.equal(typeText('无扩展名'), '无扩展名 · 文件');
  assert.deepEqual(splitName('a.b.txt'), { main: 'a.b', ext: '.txt' });
  assert.deepEqual(splitName('.gitignore'), { main: '.gitignore', ext: '' });
  assert.deepEqual(splitName('README'), { main: 'README', ext: '' });
});

test('混合选择：按 Windows 口径去重、保持原顺序、忽略空值', () => {
  const first = mergePickedFiles([], ['C:\\资料\\A.jpg', 'C:\\资料\\B.docx']);
  assert.deepEqual(first.files, ['C:\\资料\\A.jpg', 'C:\\资料\\B.docx']);
  const second = mergePickedFiles(first.files, [
    'c:\\资料\\a.JPG',
    'C:\\资料\\C.xlsx',
    '   ',
    null,
    'C:\\资料\\B.docx',
  ]);
  assert.deepEqual(second.files, ['C:\\资料\\A.jpg', 'C:\\资料\\B.docx', 'C:\\资料\\C.xlsx']);
  assert.equal(second.added, 1);
  assert.equal(second.duplicates, 2);
  assert.deepEqual(mergePickedFiles(null, null).files, []);
});

test('预览参数：任意文件优先于目录，空规则不下发，冲突默认跳过', () => {
  const params = buildRenameParams({
    files: ['  C:\\资料\\A.jpg ', '', 'C:\\资料\\B.docx'],
    directory: 'C:\\资料',
    rules: {
      ...createRules(),
      replace: [
        { find: 'IMG_', replace: '现场巡检_', case_sensitive: false },
        { find: '  ', replace: 'x', case_sensitive: true },
      ],
      prefix: ' 2026秋检_ ',
      suffix: '',
      keep_extension: true,
      conflict: 'suffix',
      sequence: { enabled: false, base: '现场巡检', start: 1, step: 1, digits: 3 },
    },
  });
  assert.deepEqual(params.files, ['C:\\资料\\A.jpg', 'C:\\资料\\B.docx']);
  assert.equal(params.directory, '');
  // 查找内容首尾空白本身有意义，只过滤「纯空白」的组
  assert.deepEqual(params.replace, [{ find: 'IMG_', replace: '现场巡检_', case_sensitive: false }]);
  assert.equal(params.prefix, '2026秋检_');
  assert.equal(params.suffix, '');
  assert.equal(params.keep_extension, true);
  assert.equal(params.conflict, 'suffix');
  assert.deepEqual(params.sequence, {
    enabled: false,
    base: '现场巡检',
    start: 1,
    step: 1,
    digits: 3,
  });

  const byDir = buildRenameParams({ files: [], directory: ' C:\\资料 ', rules: createRules() });
  assert.deepEqual(byDir.files, []);
  assert.equal(byDir.directory, 'C:\\资料');
  assert.equal(byDir.conflict, 'skip');
  assert.equal(byDir.keep_extension, true);
  assert.deepEqual(byDir.replace, []);
});

test('预览参数：序号与冲突模式按后端允许范围收敛', () => {
  const params = buildRenameParams({
    files: ['C:\\a.txt'],
    rules: {
      ...createRules(),
      conflict: '覆盖',
      sequence: { enabled: true, base: ' 记录 ', start: '5', step: 0, digits: 99 },
    },
  });
  assert.equal(params.conflict, 'skip');
  assert.deepEqual(params.sequence, { enabled: true, base: '记录', start: 5, step: 1, digits: 9 });
});

test('预览计数：缺字段按 0 处理，warnings 原样透传', () => {
  assert.deepEqual(previewCounts(null), { total: 0, can: 0, skip: 0, same: 0, warnings: [] });
  const counts = previewCounts({
    total: 3,
    can: 1,
    skip: 1,
    same: 1,
    warnings: ['1 项可以改名，执行后不可撤销'],
  });
  assert.deepEqual(counts, {
    total: 3,
    can: 1,
    skip: 1,
    same: 1,
    warnings: ['1 项可以改名，执行后不可撤销'],
  });
});

test('行状态映射：非法名/互撞/磁盘同名分开提示，循环与大小写标注特殊改名', () => {
  assert.deepEqual(rowTag({ status: 'ok', reason: '', flags: [] }), {
    theme: 'success',
    text: '正常',
  });
  assert.deepEqual(rowTag({ status: 'ok', reason: '', flags: ['cycle'] }), {
    theme: 'primary',
    text: '循环改名',
  });
  assert.deepEqual(rowTag({ status: 'ok', reason: '', flags: ['case_only'] }), {
    theme: 'primary',
    text: '仅大小写',
  });
  assert.deepEqual(rowTag({ status: 'same', reason: '' }), { theme: 'default', text: '不修改' });
  assert.deepEqual(rowTag({ status: 'skip', reason: '名称非法：名称不能包含 /' }), {
    theme: 'danger',
    text: '非法名称',
  });
  assert.deepEqual(rowTag({ status: 'skip', reason: '与批内其他项目标互撞' }), {
    theme: 'danger',
    text: '目标互撞',
  });
  assert.deepEqual(rowTag({ status: 'skip', reason: '目标名称已存在' }), {
    theme: 'danger',
    text: '磁盘已有',
  });
  assert.deepEqual(rowTag({ status: 'skip', reason: '文件不存在' }), {
    theme: 'warning',
    text: '跳过',
  });
  assert.equal(rowNameTone({ status: 'ok', flags: [] }), 'ok');
  assert.equal(rowNameTone({ status: 'same', flags: [] }), 'same');
  assert.equal(rowNameTone({ status: 'skip', flags: [] }), 'bad');
  assert.equal(rowNameTone({ status: 'ok', flags: ['cycle'] }), 'info');
});

test('规则摘要、冲突提示与序号示例与设计稿一致', () => {
  const rules = {
    ...createRules(),
    replace: [{ find: 'IMG_', replace: '现场巡检_', case_sensitive: false }],
    prefix: '2026秋检_',
    sequence: { enabled: true, base: '现场巡检', start: 1, step: 1, digits: 3 },
  };
  assert.equal(rulesSummary(rules), '1 组替换 · 前缀 · 统一主名 + 序号');
  assert.equal(rulesSummary(createRules()), '未设置规则');
  assert.equal(conflictTip({ conflict: 'skip' }), '冲突默认跳过 · 执行前二次确认');
  assert.equal(conflictTip({ conflict: 'suffix' }), '冲突自动追加序号 · 仍不覆盖');
  assert.equal(
    sequenceExample({ ...rules, suffix: '_终稿' }, 'IMG_2043.jpg'),
    '2026秋检_现场巡检_001_终稿.jpg'
  );
  assert.equal(
    sequenceExample(
      { ...rules, prefix: '', suffix: '', sequence: { ...rules.sequence, digits: 2, start: 7 } },
      'a.png'
    ),
    '现场巡检_07.png'
  );
  // 不保留扩展名时示例不再带扩展名
  assert.equal(
    sequenceExample({ ...rules, prefix: '', keep_extension: false }, 'a.png'),
    '现场巡检_001'
  );
});

test('执行结果映射：成功/取消/失败都给出可读摘要与产物目录', () => {
  const success = batchOutcome({
    status: 'success',
    result: {
      success: 3,
      failed: 0,
      skipped: 1,
      unchanged: 1,
      not_run: 0,
      output_dir: 'C:\\资料',
    },
  });
  assert.equal(success.status, '成功');
  assert.equal(success.failed, false);
  assert.equal(success.summary, '重命名完成：成功 3，跳过 1，未变化 1');
  assert.deepEqual(success.artifact, { label: '打开原目录', path: 'C:\\资料' });

  const cancelled = batchOutcome({
    status: 'cancelled',
    result: { success: 2, failed: 0, skipped: 0, unchanged: 0, not_run: 4 },
  });
  assert.equal(cancelled.status, '已取消');
  assert.equal(cancelled.summary, '已取消：已完成 2，未执行 4');
  assert.deepEqual(cancelled.artifact, { label: '', path: '' });

  const failed = batchOutcome({
    status: 'failed',
    error: '磁盘已满',
    result: { success: 1, failed: 1, skipped: 0, unchanged: 0, not_run: 0 },
  });
  assert.equal(failed.status, '失败');
  assert.equal(failed.failed, true);
  assert.match(failed.summary, /重命名未完成：成功 1，失败 1/);

  const empty = batchOutcome({ status: 'success', result: {} });
  assert.equal(empty.summary, '重命名完成：成功 0');
});

test('历史摘要与结果行：状态中文、逐项原名称与目标名称', () => {
  assert.equal(
    formatBatchHistory({ success: 2, failed: 1, skipped: 0, unchanged: 0, not_run: 0 }),
    '成功 2，失败 1'
  );
  assert.equal(formatBatchHistory({}), '');
  const rows = resultRows({
    rows: [
      { name: 'IMG_1.jpg', new_name: '现场_1.jpg', result: 'success' },
      { name: 'IMG_2.jpg', new_name: '现场_2.jpg', result: 'failed', message: '文件被占用' },
      { name: 'IMG_3.jpg', new_name: 'IMG_3.jpg', result: 'skipped', reason: '目标名称已存在' },
      { name: 'IMG_4.jpg', new_name: 'IMG_4.jpg', result: 'unchanged' },
      { name: 'IMG_5.jpg', new_name: '现场_5.jpg', result: 'not_run', message: '已取消' },
    ],
  });
  assert.deepEqual(
    rows.map((row) => [row.name, row.status.text, row.status.theme]),
    [
      ['IMG_1.jpg', '成功', 'success'],
      ['IMG_2.jpg', '失败', 'danger'],
      ['IMG_3.jpg', '跳过', 'warning'],
      ['IMG_4.jpg', '未变化', 'default'],
      ['IMG_5.jpg', '未执行', 'default'],
    ]
  );
  assert.equal(rows[1].message, '文件被占用');
  assert.equal(rows[2].message, '目标名称已存在');
  assert.deepEqual(resultRows(null), []);
});

test('参数回填：只接受类型正确的字段，不自动执行', () => {
  const restored = restoreBatchParams({
    files: ['C:\\a.txt', 42, ''],
    directory: 'C:\\资料',
    replace: [{ find: 'a', replace: 'b', case_sensitive: true }, { find: '' }, 'x'],
    prefix: 'P_',
    suffix: 7,
    keep_extension: false,
    sequence: { enabled: 'yes', base: '记录', start: '3', step: '2', digits: '4' },
    conflict: 'suffix',
  });
  assert.deepEqual(restored.files, ['C:\\a.txt']);
  assert.equal(restored.directory, 'C:\\资料');
  assert.deepEqual(restored.rules.replace, [
    { find: 'a', replace: 'b', case_sensitive: true },
    { find: '', replace: '', case_sensitive: false },
  ]);
  assert.equal(restored.rules.prefix, 'P_');
  assert.equal(restored.rules.suffix, '');
  assert.equal(restored.rules.keep_extension, false);
  assert.deepEqual(restored.rules.sequence, {
    enabled: true,
    base: '记录',
    start: 3,
    step: 2,
    digits: 4,
  });
  assert.equal(restored.rules.conflict, 'suffix');
  const fallback = restoreBatchParams({});
  assert.deepEqual(fallback.files, []);
  assert.equal(fallback.directory, '');
  assert.equal(fallback.rules.conflict, 'skip');
});

test('路径口径：选择与回看都用同一份大小写不敏感的键', () => {
  assert.equal(pathKey(' C:\资料\A.JPG '), 'c:\资料\a.jpg');
  assert.equal(pathKey(null), '');
  // 选择阶段按 Windows 口径去重，回看阶段必须同口径才不会误删
  const merged = mergePickedFiles([], ['C:\资料\A.JPG']);
  assert.equal(merged.files.length, 1);
  assert.equal(pathKey(merged.files[0]), pathKey('c:\资料\a.jpg'));
});

test('进度计划与后端 len(rows) 对齐，预检跳过与未变化分开计数', () => {
  assert.deepEqual(progressPlan({ total: 7, can: 4, skip: 2, same: 1 }), {
    total: 7,
    planned: 4,
    skip: 2,
    same: 1,
  });
  assert.deepEqual(progressPlan(null), { total: 0, planned: 0, skip: 0, same: 0 });
  const counts = renameCounts({ success: 2, failed: 1, skipped: 3, unchanged: 4, not_run: 5 });
  assert.deepEqual(counts, {
    success: 2,
    failed: 1,
    skipped: 3,
    unchanged: 4,
    not_run: 5,
    total: 15,
  });
});

test('取消摘要含失败数，失败摘要与历史摘共用同一份计数', () => {
  const cancelled = batchOutcome({
    status: 'cancelled',
    result: { success: 2, failed: 1, skipped: 3, unchanged: 0, not_run: 4 },
  });
  assert.equal(cancelled.status, '已取消');
  assert.equal(cancelled.summary, '已取消：已完成 2，失败 1，未执行 4');

  const result = { success: 2, failed: 1, skipped: 3, unchanged: 4, not_run: 5 };
  assert.equal(formatBatchHistory(result), '成功 2，失败 1，跳过 3，未变化 4，未执行 5');
  const failed = batchOutcome({ status: 'failed', result, error: '磁盘已满' });
  assert.match(failed.summary, /^重命名未完成：成功 2，失败 1，跳过 3，未变化 4，未执行 5/);
  const success = batchOutcome({ status: 'success', result });
  assert.match(success.summary, /^重命名完成：成功 2，失败 1，跳过 3，未变化 4，未执行 5/);
});

test('空统一主名原样下发，示例沿用后端「文件」兜底', () => {
  const params = buildRenameParams({
    files: ['C:\a.txt'],
    rules: {
      ...createRules(),
      sequence: { enabled: true, base: '   ', start: 1, step: 1, digits: 3 },
    },
  });
  assert.equal(params.sequence.base, '');
  assert.equal(
    sequenceExample(
      {
        ...createRules(),
        prefix: '',
        suffix: '',
        sequence: { enabled: true, base: '', start: 2, step: 1, digits: 2 },
      },
      'a.png'
    ),
    '文件_02.png'
  );
});

test('跨目录同名文件：移除只删目标项，不按文件名连坐', () => {
  const files = ['C:\甲\现场照片.jpg', 'D:\乙\现场照片.jpg', 'C:\甲\说明.txt'];
  assert.deepEqual(removeFile(files, 'C:\甲\现场照片.jpg'), [
    'D:\乙\现场照片.jpg',
    'C:\甲\说明.txt',
  ]);
  assert.deepEqual(removeFile(files, 'd:\乙\现场照片.jpg'), [
    'C:\甲\现场照片.jpg',
    'C:\甲\说明.txt',
  ]);
  // 只给文件名不得命中任何项：没有完整路径就无法唯一定位
  assert.deepEqual(removeFile(files, '现场照片.jpg'), files);
  assert.deepEqual(removeFile(files, ''), files);
  // 不得原地改写入参
  const snapshot = files.slice();
  removeFile(files, 'C:\甲\说明.txt');
  assert.deepEqual(files, snapshot);
});

test('页面接线：预览/执行/选择/结果/历史全部走既定契约', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /call\('preview', \{ app_id: APP_ID, params \}\)/);
  assert.match(source, /runJob\(\s*APP_ID,/);
  // 选择通道统一走 api.js 薄委托，页面不再直连 window.core
  assert.match(source, /import \{[^}]*\bpickFiles\b[^}]*\} from '\.\.\/api'/);
  assert.match(source, /await pickFiles\(\)/);
  assert.match(source, /await pickDirectory\(\)/);
  assert.doesNotMatch(source, /window\.core\.pickFiles/);
  assert.match(source, /<TaskResult\b/);
  assert.match(source, /<HistoryCard\b[^>]*app-id="batch_rename"/);
  assert.match(source, /job\.cancel\(\)/);
  // 预检只渲染后端结论：页面不重算冲突/新名
  assert.doesNotMatch(source, /function buildPreview|function validateName|function applyPairs/);
  // 执行前必须二次确认
  assert.match(source, /confirmVisible\.value = true/);
  assert.match(source, /确认重命名/);
});

test('页面接线：空列表与无规则时不发起预览，避免每次挂载都请求核心', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /if \(!params\.files\.length && !params\.directory\) \{/);
  assert.match(source, /previewResult\.value = null;/);
  // 防抖 + 序号丢弃过期响应
  assert.match(source, /clearTimeout\(previewTimer\)/);
  assert.match(source, /const token = \+\+previewToken;/);
  assert.match(source, /if \(token !== previewToken\) return;/);
});

test('页面接线：960×620 窄窗口收窄规则列，按钮全部声明 type', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /grid-template-columns: minmax\(0, 1fr\) 340px;/);
  assert.match(source, /@media \(max-width: 1060px\)/);
  assert.match(source, /grid-template-columns: minmax\(0, 1fr\) 268px;/);
  assert.match(source, /prefers-reduced-motion/);
  // 页面里不允许写死让窄窗溢出的最小宽度
  assert.doesNotMatch(source, /min-width: 9[0-9]{2}px/);
  const nativeButtons = source.match(/<button[^>]*>/g) || [];
  assert.ok(nativeButtons.length > 0);
  for (const tag of nativeButtons) {
    assert.match(tag, /type="button"/, `原生按钮缺少 type：${tag}`);
  }
});

test('页面接线：回填只填表不自动执行，且退出后清理定时器', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /restoreState\.appID !== APP_ID/);
  assert.match(source, /applyRestore\(restoreState\.params\)/);
  assert.doesNotMatch(source, /applyRestore[\s\S]{0,400}?startRun\(/);
  assert.match(source, /onUnmounted\(\(\) => \{\s*clearTimeout\(previewTimer\);/);
});

test('页面接线：执行中禁止回填并提示，预览态保留回填反馈', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /function applyRestore\(params\) \{\s*if \(running\.value\) \{/);
  assert.match(source, /任务执行中，无法回填参数/);
  assert.match(source, /restoreNotice\.value = restored\.directory/);
  assert.match(source, /已回填 \$\{restored\.files\.length\} 个文件/);
  assert.match(source, /<div v-if="restoreNotice" class="rename-restore">/);
  assert.match(source, /@click="restoreNotice = ''"/);
  // 运行中不切换视图：取消入口必须留在进度态
  const restoreBody = source.slice(
    source.indexOf('function applyRestore(params)'),
    source.indexOf('function applyRestore(params)') + 900
  );
  assert.match(restoreBody, /return;/);
});

test('页面接线：启动失败把真实错误写进日志与结果，不只留泛化摘要', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(
    source,
    /const raw = String\(\(err && err\.message\) \|\| err \|\| '任务启动失败'\)/
  );
  assert.match(source, /logs\.value\.push\(`启动失败：\$\{raw\}`\)/);
  assert.match(source, /summary: `启动失败：\$\{raw\}`/);
  assert.match(source, /<TaskResult :outcome="outcome" :logs="logs"/);
});

test('页面接线：逐项移除按路径定位，回看筛选沿用选择阶段的大小写口径', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /function removeRow\(row\)/);
  assert.match(source, /pathKey\(row\.path\)/);
  assert.doesNotMatch(source, /position !== index/);
  const back = source.slice(
    source.indexOf('function backToPreview()'),
    source.indexOf('function finishResult()')
  );
  assert.match(back, /pathKey\(row\.path\)/);
  assert.match(back, /pathKey\(path\)/);
});

test('页面接线：进度总数取后端 rows 长度，计数分列预检跳过与未变化', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /progressPlan\(/);
  assert.match(source, /progress\.preSkip/);
  assert.match(source, /progress\.same/);
  assert.match(source, /预检跳过/);
  assert.match(source, /未变化/);
  // 分母不能退回「可提交数」：后端 total 是 len(rows)
  const start = source.indexOf('onProgress:');
  const body = source.slice(start, start + 600);
  assert.match(body, /processedTotal = Number\(p\?\.total\) \|\| totalRows/);
  assert.doesNotMatch(body, /\|\| total(?!Rows)/);
});

test('行数据必须携带完整路径：预览行与未预览行都是，移除只按 pathKey(path)', () => {
  const source = read('pages/BatchRenamePage.vue');
  const start = source.indexOf('const displayRows = computed(() => {');
  const body = source.slice(start, source.indexOf('const exampleName', start));
  // 两条分支（后端 rows / 未预览的本地 files）都要给 path
  assert.match(body, /path: String\(row\.path \|\| ''\)/);
  assert.match(body, /path: String\(path\)/);
  const removeStart = source.indexOf('function removeRow(row)');
  const removeBody = source.slice(
    removeStart,
    source.indexOf('async function addFiles', removeStart)
  );
  assert.match(removeBody, /removeFile\(files\.value, row\.path\)/);
  assert.doesNotMatch(removeBody, /stripDir/);
  assert.doesNotMatch(removeBody, /pathKey\(row\.name\)/);
});

test('进度区真正渲染「未变化」格，same 不能只赋值', () => {
  const source = read('pages/BatchRenamePage.vue');
  assert.match(source, /<b>\{\{ progress\.same \}\}<\/b><span>未变化<\/span>/);
});

test('回填提示在开始/清空/完成时清理，目录模式按目录提示', () => {
  const source = read('pages/BatchRenamePage.vue');
  const startRun = source.slice(
    source.indexOf('function startRun()'),
    source.indexOf('function startRun()') + 700
  );
  assert.match(startRun, /restoreNotice\.value = '';/);
  const clear = source.slice(
    source.indexOf('function clearList()'),
    source.indexOf('function clearList()') + 300
  );
  assert.match(clear, /restoreNotice\.value = '';/);
  assert.match(source, /function finishResult\(\) \{[\s\S]{0,400}?restoreNotice\.value = '';/);
  // 目录模式：不能提示「0 个文件」
  const restore = source.slice(source.indexOf('function applyRestore(params)'));
  assert.match(restore, /restored\.directory\s*\?/);
  assert.match(restore, /已回填目录/);
});

test('onDone 不再把已处理数改成计划改名数', () => {
  const source = read('pages/BatchRenamePage.vue');
  const done = source.slice(source.indexOf('onDone: (doneJob)'), source.indexOf('onError:'));
  assert.doesNotMatch(done, /progress\.done = progress\.planned/);
  assert.match(done, /progress\.done = totalRows/);
});
