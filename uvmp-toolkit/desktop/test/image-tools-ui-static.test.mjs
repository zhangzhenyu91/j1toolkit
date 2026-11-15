// 图片工具独立窗口（imagetools.html）静态契约：页面接线、纯函数交互与格式边界。
// 只读源码与纯 ESM 模块，不启动浏览器，不碰真实文件系统。
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const ROOT = new URL('../', import.meta.url);

function read(rel) {
  return readFileSync(new URL(rel, ROOT), 'utf8');
}

function listSources(dir) {
  const abs = fileURLToPath(new URL(dir, ROOT));
  const out = [];
  for (const name of readdirSync(abs)) {
    const rel = `${dir}/${name}`;
    const st = statSync(fileURLToPath(new URL(rel, ROOT)));
    if (st.isDirectory()) out.push(...listSources(rel));
    else out.push(rel);
  }
  return out;
}

function sourceFiles() {
  return listSources('src/imagetools').map((rel) => [rel, read(rel)]);
}

function allSource() {
  return sourceFiles()
    .map(([, text]) => text)
    .join('\n');
}

const loadLib = (name) => import(new URL(`src/imagetools/lib/${name}`, ROOT).href);

/** 取一段以 header 开头的 CSS 规则块的完整内容（按花括号配对，缺块直接失败）。 */
function cssBlock(css, header) {
  const at = css.indexOf(header);
  assert.ok(at >= 0, `缺少样式段 ${header}`);
  const start = css.indexOf('{', at);
  let depth = 0;
  for (let i = start; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1;
    else if (css[i] === '}') {
      depth -= 1;
      if (depth === 0) return css.slice(start + 1, i);
    }
  }
  throw new Error(`${header} 未闭合`);
}

test('入口：imagetools.html 与 vite 多页入口都在，标题与容器齐备', () => {
  const html = read('imagetools.html');
  assert.match(html, /<div id="app">/);
  assert.match(html, /src="\/src\/imagetools\/main\.js"/);
  assert.match(html, /<html lang="zh-CN">/);
  assert.match(html, /<meta charset="utf-8"\s*\/?>/i);
  const vite = read('vite.config.js');
  assert.match(vite, /imagetools:\s*resolve\(__dirname, 'imagetools\.html'\)/);
});

test('零网络：页面不引用外部资源，渲染层不发网络请求', () => {
  const html = read('imagetools.html');
  assert.doesNotMatch(html, /(?:src|href)="https?:/i);
  assert.doesNotMatch(html, /<link[^>]+href="\/\//i);
  for (const [rel, text] of sourceFiles()) {
    assert.doesNotMatch(text, /https?:\/\//, `${rel} 含外网地址`);
    assert.doesNotMatch(text, /\bfetch\s*\(/, `${rel} 使用了 fetch`);
    assert.doesNotMatch(text, /XMLHttpRequest|new WebSocket|EventSource/, `${rel} 发起网络连接`);
  }
});

test('渲染层只用 window.imageTools：不碰 window.core、不 require electron', () => {
  const api = read('src/imagetools/lib/api.mjs');
  assert.match(api, /window\.imageTools/);
  // 只列页面真正用到的桥接方法：open 仍是未使用的死导出；
  // inspect 自 v2.4.3 起被处理页真正使用（用核心规范路径统一 8.3 短名身份），不再算死导出。
  const methods = [
    'ready',
    'pickFiles',
    'pickDirectory',
    'list',
    'inspect',
    'preview',
    'rename',
    'runProcess',
    'cancelJob',
    'getJob',
    'getConfig',
    'saveQuickPhrases',
    'showItem',
    'openDefaultSettings',
    'setDefaultLinux',
    'getDefaultStatus',
    'onEvent',
  ];
  for (const method of methods) {
    assert.match(api, new RegExp(`window\\.imageTools\\.${method}\\(`), `缺 imageTools.${method}`);
  }
  // 死导出不得回潮
  assert.doesNotMatch(api, /export function open\(/, '未使用的 open 导出应保持移除');
  // inspect 是处理页的真实调用（短名归一），必须存在且只能是桥接直调
  assert.match(api, /export function inspect\(/, '处理页需要 inspect 取核心规范路径');
  assert.match(
    api,
    /export function inspect\(path\) \{\s*return window\.imageTools\.inspect\(path\);/
  );
  for (const [rel, text] of sourceFiles()) {
    if (rel === 'src/imagetools/lib/api.mjs') continue;
    assert.doesNotMatch(text, /window\.imageTools/, `${rel} 绕过 api.mjs 直接调 window.imageTools`);
    assert.doesNotMatch(text, /window\.core\b/, `${rel} 误用主客户端 window.core`);
    assert.doesNotMatch(text, /\brequire\s*\(/, `${rel} 在渲染层 require`);
    assert.doesNotMatch(text, /from ['"]electron['"]/, `${rel} 引入了 electron 模块`);
  }
});

test('导航只有图片查看 / 图片处理 + 设置，不出现批量重命名页签', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /图片查看/);
  assert.match(app, /图片处理/);
  assert.match(app, /设置/);
  assert.match(app, /role="tablist"/);
  const source = allSource();
  assert.doesNotMatch(source, /批量重命名|批量改名|BatchRename|batch_rename|batchRename/);
  assert.doesNotMatch(app, /AppSidebar|navigation\.mjs/);
});

test('查看器：滚轮以指针为锚、25%–400%、拖动、双击、左右键与 F2', () => {
  const viewer = read('src/imagetools/components/ViewerPane.vue');
  assert.match(viewer, /@wheel(?:\.prevent)?="/);
  assert.match(viewer, /@pointerdown="/);
  assert.match(viewer, /@pointermove="/);
  assert.match(viewer, /@dblclick="/);
  assert.match(viewer, /requestFullscreen/);
  // ← → 与 F2 由 App 的 document 级快捷键统一接管（见「快捷键」用例）：
  // 查看器只保留缩放键与抽屉 Esc，避免两处抢键互相触发
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /event\.key === 'ArrowLeft'/);
  assert.match(app, /event\.key === 'ArrowRight'/);
  assert.match(app, /event\.key === 'F2'/);
  assert.match(app, /hotkeyBlocked\(/);
  // 缩放/切图纯逻辑在 lib/viewer.mjs
  return loadLib('viewer.mjs').then((m) => {
    assert.equal(m.ZOOM_MIN, 0.25);
    assert.equal(m.ZOOM_MAX, 4);
    assert.equal(m.clampZoom(0.1), 0.25);
    assert.equal(m.clampZoom(9), 4);
    // 指针在画布正中心缩放：指针下的画面内容不动（平移量与缩放同比放大）
    const center = m.zoomAroundOffset({
      scale: 1,
      offsetX: 12,
      offsetY: -8,
      nextScale: 2,
      pointerX: 400,
      pointerY: 300,
      centerX: 400,
      centerY: 300,
    });
    assert.equal(center.offsetX, 24);
    assert.equal(center.offsetY, -16);
    assert.equal(center.scale, 2);
    // 不变量：指针处的图像坐标 (dx - offset) / scale 缩放前后一致
    const before = (0 - 12) / 1;
    const after = (0 - center.offsetX) / center.scale;
    assert.equal(after, before);
    assert.equal(center.offsetY / center.scale, -8);
    // 指针在中心右侧放大：内容相对画布向左移动
    const right = m.zoomAroundOffset({
      scale: 1,
      offsetX: 0,
      offsetY: 0,
      nextScale: 2,
      pointerX: 500,
      pointerY: 300,
      centerX: 400,
      centerY: 300,
    });
    assert.equal(right.offsetX, -100);
    // 适应窗口不放大超过 1:1
    assert.equal(m.fitScale(1920, 1280, 960, 640), 0.5);
    assert.equal(m.fitScale(320, 200, 960, 640), 1);
    // 首尾不循环
    assert.equal(m.stepIndex(0, -1, 6), -1);
    assert.equal(m.stepIndex(5, 1, 6), -1);
    assert.equal(m.stepIndex(0, 1, 6), 1);
    assert.equal(m.stepIndex(2, -1, 1), -1);
    assert.equal(m.canStep(0, -1, 6), false);
    assert.equal(m.canStep(0, 1, 6), true);
  });
});

test('信息栏六类信息齐备，修改时间精确到分钟', () => {
  const info = read('src/imagetools/components/InfoPanel.vue');
  for (const label of ['名称', '类型', '大小', '尺寸', '位置', '修改时间']) {
    assert.match(info, new RegExp(label), `信息栏缺「${label}」`);
  }
  assert.match(info, /打开所在文件夹/);
  assert.match(info, /重命名（F2）/);
  return loadLib('format.mjs').then((m) => {
    const seconds = new Date(2026, 9, 8, 14, 32, 45).getTime() / 1000;
    assert.equal(m.formatMtime(seconds), '2026-10-08 14:32');
    assert.equal(m.formatMtime(seconds).length, 16);
    assert.equal(m.formatSize(260791), '254.6 KB');
    assert.equal(m.formatBytesExact(260791), '260,791 字节');
    assert.equal(m.formatAspect(1920, 1280), '3:2');
    // 路径按目录分段省略：不把目录名从中间切断
    const elided = m.elidePath('D:\\班组资料\\2026 秋检\\宣传素材\\秋检现场-03.jpg', 20);
    assert.equal(elided, '…\\宣传素材\\秋检现场-03.jpg');
    assert.match(
      m.elidePath('/home/team/2026 秋检/宣传素材/秋检现场-03.jpg', 28),
      /秋检现场-03\.jpg$/
    );
    assert.match(elided, /宣传素材/, '省略后必须保留末尾目录');
    const short = m.elidePath('D:\\班组资料\\宣传素材\\很长很长的现场照片名称-03.jpg', 18);
    assert.match(short, /-03\.jpg$/, '宽度不足时保留扩展名');
    assert.doesNotMatch(short, /\\宣传素材\\很长/, '不应从中间切断目录名');
  });
});

test('重命名：短语插入光标处、不覆盖、五类失败反馈', () => {
  const renamePanel = read('src/imagetools/components/RenamePanel.vue');
  assert.match(renamePanel, /快捷输入/);
  assert.match(renamePanel, /Enter/);
  assert.match(renamePanel, /Esc/);
  assert.match(renamePanel, /insertPhrase/);
  return Promise.all([loadLib('viewer.mjs'), loadLib('rename.mjs')]).then(([viewer, rename]) => {
    const inserted = viewer.insertPhrase({
      value: '现场勘察秋检',
      start: 4,
      end: 4,
      phrase: '_2026秋检',
    });
    assert.equal(inserted.value, '现场勘察_2026秋检秋检');
    assert.equal(inserted.caret, 11);
    const replaced = viewer.insertPhrase({
      value: '现场勘察秋检',
      start: 4,
      end: 6,
      phrase: '_已核验',
    });
    assert.equal(replaced.value, '现场勘察_已核验');
    assert.equal(replaced.caret, 8);
    assert.match(rename.validateBaseName('').title, /主文件名为空/);
    assert.match(rename.validateBaseName('秋检/现场').title, /非法字符/);
    assert.match(rename.validateBaseName('现场.').title, /非法字符|句点/);
    assert.equal(rename.validateBaseName('秋检现场-03'), null);
    const conflict = rename.renameFeedback('IMG_RENAME_CONFLICT', '同目录已有「秋检.jpg」');
    assert.match(conflict.title, /同名|已存在/);
    assert.match(conflict.body, /未做任何修改|未做任何/);
    assert.match(rename.renameFeedback('FILE_NOT_FOUND', '').title, /删除/);
    assert.match(rename.renameFeedback('IMG_RENAME_DENIED', '').title, /只读|占用/);
    assert.match(rename.renameFeedback('IMG_RENAME_FAILED', '磁盘错误').title, /失败/);
    assert.equal(
      rename.parseErrorCode('Error: IMG_RENAME_CONFLICT: 同目录已有'),
      'IMG_RENAME_CONFLICT'
    );
    assert.equal(rename.parseErrorCode('随便的报错'), '');
  });
});

test('图片处理：三页签共享文件列表、输出策略与开始处理', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  for (const label of ['格式转换', '压缩体积', '尺寸修改']) assert.match(pane, new RegExp(label));
  assert.match(pane, /待处理文件/);
  assert.match(pane, /可处理/);
  assert.match(pane, /跳过/);
  assert.match(pane, /开始处理/);
  assert.match(pane, /processed_/);
  assert.match(pane, /不提供原地覆盖|不覆盖原图/);
  assert.match(pane, /同名冲突|追加/);
  assert.match(pane, /保持比例/);
  assert.match(pane, /不放大/);
  assert.match(pane, /透明背景/);
  assert.match(pane, /目标格式/);
  assert.match(pane, /质量档位/);
  assert.match(pane, /输出位置/);
  assert.match(pane, /选择/);
  assert.match(pane, /:disabled="!canStart"/);
  assert.match(pane, /暂无可处理项/);
  return loadLib('process.mjs').then((m) => {
    assert.deepEqual(m.OPERATIONS, ['convert', 'compress', 'resize']);
    assert.equal(m.VIEW_EXTS.length, 10);
    assert.equal(m.BATCH_FORMATS.length, 5);
    assert.equal(m.BATCH_EXTS.length, 7);
    assert.deepEqual(Object.keys(m.OUTPUT_FORMATS), ['jpeg', 'png', 'webp']);
    assert.equal(
      m.classifyFile({ ext: '.jpg', animated: false, supported: true }).processable,
      true
    );
    assert.equal(
      m.classifyFile({ ext: '.png', animated: false, supported: true }).processable,
      true
    );
    assert.equal(
      m.classifyFile({ ext: '.webp', animated: true, supported: true }).processable,
      false
    );
    assert.match(m.classifyFile({ ext: '.webp', animated: true, supported: true }).reason, /动画/);
    assert.equal(
      m.classifyFile({ ext: '.gif', animated: false, supported: true }).processable,
      false
    );
    assert.equal(
      m.classifyFile({ ext: '.heic', animated: false, supported: false }).processable,
      false
    );
    assert.equal(
      m.classifyFile({ ext: '.ico', animated: false, supported: true }).processable,
      false
    );
    assert.equal(
      m.classifyFile({ ext: '.tif', animated: false, supported: true }).processable,
      true
    );
    assert.deepEqual(
      m.COMPRESS_LEVELS.map((x) => x.quality),
      [92, 80, 65]
    );
    const summary = m.describeTask({
      operation: 'convert',
      options: { format: 'jpeg', quality: 88 },
      processable: 5,
      skipped: 2,
    });
    assert.match(summary, /格式转换/);
    assert.match(summary, /JPEG/);
    assert.match(summary, /88/);
    assert.match(summary, /5 项/);
    assert.match(summary, /跳过 2/);
    const disabled = m.describeAction({
      processable: 0,
      skipped: 2,
    });
    assert.equal(disabled.canStart, false);
    assert.match(disabled.reason, /暂无可处理项/);
    assert.match(m.formatConflictPolicy(), /\(2\)/);
  });
});

test('处理任务：输出目录、进度、取消与成功/失败/跳过结果', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  assert.match(pane, /runProcess/);
  assert.match(pane, /cancelJob/);
  assert.match(pane, /job\.progress/);
  assert.match(pane, /job\.done/);
  assert.match(pane, /pickDirectory/);
  assert.match(pane, /output_dir/);
  assert.match(pane, /打开输出目录/);
  assert.match(pane, /重试失败项/);
  assert.match(pane, /查看日志/);
  assert.match(pane, /取消/);
  assert.match(pane, /role="dialog"/);
  assert.match(pane, /aria-modal="true"/);
  return loadLib('process.mjs').then((m) => {
    const rows = [
      { status: 'success', size_before: 1000, size_after: 500, output: '/tmp/out/a.jpg' },
      { status: 'failed', reason: '输出目录只读' },
      { status: 'skipped', reason: '动画图' },
    ];
    const counts = m.classifyRows(rows);
    assert.deepEqual(counts, { success: 1, failed: 1, skipped: 1, total: 3 });
    assert.equal(m.progressPercent({ current: 3, total: 5 }), 60);
    assert.equal(m.progressPercent({ current: 0, total: 0 }), 0);
    assert.equal(m.progressPercent({ current: 9, total: 5 }), 100);
    assert.match(m.resultTone({ success: 2, failed: 0, skipped: 1 }), /success|done/);
    assert.match(m.resultTone({ success: 1, failed: 2, skipped: 0 }), /partial|warn/);
    assert.match(m.resultTone({ success: 0, failed: 3, skipped: 0 }), /failed|danger|error/);
    assert.match(m.outputPolicyText(), /不覆盖/);
  });
});

test('格式边界：8 种可查看、5 种可批处理、动画跳过与不支持格式', () => {
  const source = allSource();
  for (const label of ['JPEG', 'PNG', 'GIF', 'WebP', 'BMP', 'ICO', 'AVIF', 'TIFF']) {
    assert.match(source, new RegExp(label), `缺可查看格式 ${label}`);
  }
  assert.match(source, /HEIC/);
  assert.match(source, /动画/);
  assert.match(source, /不压成首帧|不压首帧/);
  assert.match(source, /暂不承诺/);
  return loadLib('process.mjs').then((m) => {
    assert.deepEqual(m.VIEW_EXTS, [
      '.jpg',
      '.jpeg',
      '.png',
      '.gif',
      '.webp',
      '.bmp',
      '.ico',
      '.avif',
      '.tif',
      '.tiff',
    ]);
    assert.deepEqual(m.BATCH_EXTS, ['.jpg', '.jpeg', '.png', '.webp', '.bmp', '.tif', '.tiff']);
    assert.deepEqual(m.BATCH_FORMATS, ['JPEG', 'PNG', 'WEBP', 'BMP', 'TIFF']);
    for (const ext of ['.heic', '.raw', '.psd', '.eps', '.pdf', '.svg']) {
      assert.equal(m.isBatchExt(ext), false, `${ext} 不应进入批处理`);
    }
    assert.match(m.UNSUPPORTED_NOTE, /HEIC/);
  });
});

test('900×600：窄窗口信息栏为覆盖式抽屉，处理页单列不横向溢出', () => {
  const css = read('src/imagetools/imagetools.css');
  // 宽窗口 300px 信息栏（基础样式，不在媒体段内）
  const wide = css.slice(0, css.indexOf('@media (max-width: 960px)'));
  assert.match(wide, /grid-template-columns: minmax\(0, 1fr\) 300px/);
  // 960px 媒体段自身必须含有抽屉与单列规则（防止规则写在别处而断言恒真）
  const narrow = cssBlock(css, '@media (max-width: 960px)');
  assert.match(narrow, /\.it-inspector\.is-drawer\s*\{/);
  assert.match(narrow, /position: absolute/);
  assert.match(narrow, /width: 300px/);
  assert.match(narrow, /\.it-tool-grid\s*\{[^}]*grid-template-columns: minmax\(0, 1fr\)/);
  assert.match(narrow, /\.it-bottom-row\s*\{[^}]*grid-template-columns: minmax\(0, 1fr\)/);
  assert.match(narrow, /\.it-file-card\s*\{[^}]*max-height/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(css, /font-variant-numeric: tabular-nums/);
  const viewer = read('src/imagetools/components/ViewerPane.vue');
  assert.match(viewer, /drawer/);
  assert.match(viewer, /it-scrim/);
  assert.match(viewer, /Escape/);
});

test('设置：多条短语增删改排序、保存、默认应用状态与引导', () => {
  const settings = read('src/imagetools/components/SettingsPane.vue');
  assert.match(settings, /快捷输入/);
  assert.match(settings, /恢复默认/);
  assert.match(settings, /保存设置/);
  // 限额文案由常量渲染（不写死数字），格式化换行不影响断言
  assert.match(settings, /最多\s*\{\{\s*MAX_PHRASES\s*\}\}\s*条/);
  assert.match(settings, /\{\{\s*MAX_PHRASE_LEN\s*\}\}\s*字/);
  assert.match(settings, /import \{[\s\S]*?MAX_PHRASES[\s\S]*?\} from '\.\.\/lib\/rename\.mjs'/);
  assert.match(settings, /MAX_PHRASE_LEN/);
  assert.match(settings, /添加/);
  assert.match(settings, /删除|移除/);
  assert.match(settings, /上移/);
  assert.match(settings, /下移/);
  assert.match(settings, /文件关联/);
  assert.match(settings, /Windows 10/);
  assert.match(settings, /麒麟|UOS/);
  assert.match(settings, /安装版/);
  assert.match(settings, /便携版/);
  assert.match(settings, /查看能力/);
  assert.match(settings, /getDefaultStatus/);
  assert.match(settings, /openDefaultSettings/);
  assert.match(settings, /setDefaultLinux/);
  // 默认应用状态必须来自真实查询：unknown 不得被渲染成「未设」或「已设」
  assert.match(settings, /windowsStatus/);
  assert.match(settings, /partial/);
  assert.match(settings, /unknown/);
  assert.match(settings, /无法确定/);
  assert.match(settings, /installed/);
  // 「重新检查」按钮仍在，且点击真的重新查询
  assert.match(settings, /重新检查/);
  assert.match(settings, /refreshDefaultStatus/);
  assert.match(settings, /打开系统默认应用设置/);
});

test('结果态：全失败 / 取消 / 核心退出都不显示「全部成功」', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  assert.match(pane, /resolveOutcome/);
  // 模板里的标题与标签必须来自结果判定，不能写死「全部成功」
  assert.match(pane, /\{\{\s*outcome\.title\s*\}\}/);
  assert.match(pane, /\{\{\s*outcome\.tag\s*\}\}/);
  const template = pane.slice(pane.indexOf('<template>'), pane.indexOf('</template>'));
  assert.doesNotMatch(template, /全部成功|部分失败|已取消/, '结果标签不得写死在模板里');
  assert.match(pane, /jobStatus/);
  assert.match(pane, /core\.exit|coreExit/i);
  return loadLib('process.mjs').then((m) => {
    assert.equal(typeof m.resolveOutcome, 'function');
    const failed = m.resolveOutcome({
      status: 'success',
      rows: [{ status: 'failed' }, { status: 'failed' }],
    });
    assert.equal(failed.state, 'failed');
    assert.match(failed.tag, /全部失败/);
    assert.doesNotMatch(failed.tag, /全部成功/);
    assert.equal(failed.counts.failed, 2);

    const partial = m.resolveOutcome({
      status: 'success',
      rows: [{ status: 'success' }, { status: 'failed' }],
    });
    assert.equal(partial.state, 'partial');
    assert.match(partial.tag, /部分失败/);

    const done = m.resolveOutcome({ status: 'success', rows: [{ status: 'success' }] });
    assert.equal(done.state, 'done');
    assert.equal(done.tag, '全部成功');

    // 取消：即使有成功项也只显示「已取消」，不得冒充全部成功
    const cancelled = m.resolveOutcome({
      status: 'cancelled',
      rows: [{ status: 'success' }],
    });
    assert.equal(cancelled.state, 'cancelled');
    assert.match(cancelled.tag, /已取消/);
    assert.doesNotMatch(cancelled.tag, /成功/);

    // 核心退出/异常：显示中断，不显示成功
    const errored = m.resolveOutcome({ status: 'error', rows: [] });
    assert.equal(errored.state, 'error');
    assert.match(errored.tag, /中断/);
    assert.doesNotMatch(errored.tag, /成功/);
    assert.match(errored.title, /中断/);

    // 作业本身失败但结果行缺失：按失败处理
    assert.equal(m.resolveOutcome({ status: 'failed', rows: [] }).state, 'failed');

    // 只有跳过项：不得显示「全部成功」
    const skippedOnly = m.resolveOutcome({ status: 'success', rows: [{ status: 'skipped' }] });
    assert.match(skippedOnly.tag, /跳过/);
    assert.doesNotMatch(skippedOnly.tag, /全部成功/);
  });
});

test('快捷键：F2 在 document 级生效，输入框/弹层/设置页不抢键', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /document\.addEventListener\('keydown'/);
  assert.match(app, /event\.key === 'F2'/);
  assert.match(app, /hotkeyBlocked/);
  assert.match(app, /startRename/);
  // 查看器不再自己抢 F2/方向键（否则 document 级与画布级会重复触发）
  const viewer = read('src/imagetools/components/ViewerPane.vue');
  assert.doesNotMatch(viewer, /key === 'F2'/);
  assert.doesNotMatch(viewer, /key === 'ArrowLeft'|key === 'ArrowRight'/);
  return loadLib('viewer.mjs').then((m) => {
    assert.equal(typeof m.hotkeyBlocked, 'function');
    assert.equal(m.hotkeyBlocked({ page: 'viewer' }), false);
    assert.equal(m.hotkeyBlocked({ page: 'settings' }), true);
    assert.equal(m.hotkeyBlocked({ page: 'process' }), true);
    assert.equal(m.hotkeyBlocked({ page: 'viewer', modalOpen: true }), true);
    assert.equal(m.hotkeyBlocked({ page: 'viewer', renaming: true }), true);
    const input = { closest: (sel) => (/input/.test(sel) ? {} : null) };
    assert.equal(m.hotkeyBlocked({ page: 'viewer', target: input }), true);
    const plain = { closest: () => null };
    assert.equal(m.hotkeyBlocked({ page: 'viewer', target: plain }), false);
  });
});

test('前往文件关联进入设置的文件关联页签，而不是停在快捷输入', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /settingsTab/);
  assert.match(app, /:tab="settingsTab"/);
  assert.match(app, /function openAssoc\(\)[\s\S]*?settingsTab\.value = 'assoc'/);
  assert.match(app, /function openSettings\(\)[\s\S]*?settingsTab\.value = 'quick'/);
  const settings = read('src/imagetools/components/SettingsPane.vue');
  assert.match(settings, /tab: \{ type: String/);
  assert.match(settings, /watch\(/);
  assert.match(settings, /emit\('tab-change'/);
});

test('处理中 busy 接真实运行态：禁用添加 / 移除 / 输出目录', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /:busy="processBusy"/);
  assert.match(app, /@running="onProcessRunning"/);
  assert.match(app, /function onProcessRunning/);
  const pane = read('src/imagetools/components/ProcessPane.vue');
  // 内部同时看运行态与外部 busy，避免只靠父组件传值
  assert.match(
    pane,
    /const busy = computed\(\(\) => props\.busy \|\| state\.value === 'running'\)/
  );
  assert.match(pane, /:disabled="busy" @click="addFiles"/);
  assert.match(pane, /:disabled="busy"\s*\n\s*@click="removeEntry/);
  assert.match(pane, /:disabled="busy"[^>]*@click\.prevent="chooseDir"/);
  // Prettier 会拆行：按「属性存在且值为运行态判定」匹配，不绑定排版
  assert.match(pane, /:disabled="busy \|\| state(?:\.value)? !== 'idle'"/);
});

test('预览缓存：键含 mtime 与 size，重扫清空，文件替换后可重取', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(app, /previewKey|createPreviewLoader/);
  assert.match(app, /previewLoader\.reset\(/);
  assert.match(app, /previewLoader\.invalidate\(/);
  assert.match(app, /previewLoader\.schedule\(/);
  return loadLib('preview-cache.mjs').then((m) => {
    const file = { path: '/tmp/a.jpg', mtime: 100, size: 2000 };
    assert.notEqual(m.previewKey(file), m.previewKey({ ...file, mtime: 101 }));
    assert.notEqual(m.previewKey(file), m.previewKey({ ...file, size: 2001 }));

    const cache = m.createPreviewCache({ limit: 2 });
    cache.set(file, { url: 'shade-image://a' });
    assert.equal(cache.get(file).url, 'shade-image://a');
    // mtime 变了必须视为未命中（文件被外部修改）
    assert.equal(cache.get({ ...file, mtime: 999 }), undefined);
    // size 变了同样未命中
    assert.equal(cache.get({ ...file, size: 1 }), undefined);
    // 失效项可被精确删除（文件被替换后重取前先删）
    assert.equal(cache.delete(file), true);
    assert.equal(cache.get(file), undefined);

    cache.set(file, { url: 'x' });
    cache.clear();
    assert.equal(cache.get(file), undefined);
    assert.equal(cache.size, 0);

    // 上限淘汰最旧项
    const small = m.createPreviewCache({ limit: 1 });
    small.set({ path: '/a', mtime: 1, size: 1 }, { url: 'a' });
    small.set({ path: '/b', mtime: 1, size: 1 }, { url: 'b' });
    assert.equal(small.get({ path: '/a', mtime: 1, size: 1 }), undefined);
    assert.equal(small.get({ path: '/b', mtime: 1, size: 1 }).url, 'b');
  });
});

test('取消弹层与日志抽屉支持 Esc 关闭，且 esc 不穿透到切图', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  assert.match(pane, /cancelOpen/);
  assert.match(pane, /Escape/);
  assert.match(pane, /logOpen\.value = false/);
  // Esc 只关弹层，不得让 document 级的切图快捷键同时生效
  assert.match(pane, /stopPropagation\(\)/);
  assert.match(pane, /onBeforeUnmount\(\(\) => \{[\s\S]*?removeEventListener\('keydown'/);
});

test('任务日志：先订阅后启动，jobId 前事件按序回放，并发数取真实配置', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  // 并发数不能写死 2：必须读 image_tools.workers 并夹到核心允许的 1..2
  assert.doesNotMatch(pane, /并发 2/, '开始日志写死「并发 2」会在 workers=1 时误导');
  assert.match(pane, /api\.getConfig\(\)/);
  assert.match(pane, /并发 \$\{[^}]*workers[^}]*\}/);

  // 先订阅、后启动：runProcess 返回 jobId 之前的首批日志也要收得到
  const subscribeAt = pane.indexOf('api.onEvent(onJobEvent)');
  const runAt = pane.indexOf('await api.runProcess(');
  assert.ok(subscribeAt > 0, '缺少 onEvent 订阅');
  assert.ok(runAt > 0, '缺少 runProcess 启动');
  assert.ok(subscribeAt < runAt, '必须先订阅事件再调用 runProcess，否则首批日志丢失');
  const startBlock = pane.slice(pane.indexOf('async function start('), runAt);
  assert.match(startBlock, /subscribeJobEvents\(\)/);

  // jobId 未定前的事件入缓冲，确定后按序回放入同一处理函数，再开始兜底轮询
  const assignAt = pane.indexOf('jobId.value = String((res && res.job_id)');
  const replayAt = pane.indexOf('for (const event of pending) handleJobEvent(event)');
  const pollAt = pane.indexOf('startPoll();', assignAt);
  assert.ok(assignAt > runAt, '必须先拿到 jobId 再回放');
  assert.ok(replayAt > assignAt, '回放必须在 jobId 赋值之后');
  assert.ok(pollAt > replayAt, '回放要先于兜底轮询，进度不用等轮询才出现');
  assert.match(pane, /earlyEvents\.push\(payload\)/);
  assert.match(pane, /const pending = earlyEvents;\s*\n\s*earlyEvents = \[\];/);
  // 回放与实时事件走同一处理函数，且按当前任务号过滤，旧任务事件不会串进来
  assert.match(
    pane,
    /function handleJobEvent\(payload\) \{\s*\n\s*if \(!payload \|\| payload\.job_id !== jobId\.value\) return;/
  );
  // 终态、核心退出与卸载共用一个幂等清理入口，避免重复退订或漏退订
  assert.match(
    pane,
    /function stopJobEvents\(\)[\s\S]*?unsubscribe = null;[\s\S]*?earlyEvents = \[\]/
  );
  assert.match(pane, /onBeforeUnmount\(\(\) => \{[\s\S]*?stopJobEvents\(\)/);
  assert.equal(
    pane.match(/unsubscribe && unsubscribe\(\)/g),
    null,
    '只保留 stopJobEvents 一处退订'
  );
});

test('快捷短语：空列表是合法持久状态，重开不回落默认，保存 0 条也生效', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  // 加载只认「是不是数组」：用户主动删光后的空数组必须原样保留，只有读不到才回落默认
  assert.match(app, /phrasesFromConfig/, '加载快捷短语必须走纯函数判空');
  assert.doesNotMatch(
    app,
    /cfg\.quick_phrases\.length/,
    '空数组被当成「未配置」会回落默认短语，重开后用户删光的状态丢失'
  );
  assert.match(app, /function onPhrasesSaved\(list\)[\s\S]*?Array\.isArray\(list\)/);
  const settings = read('src/imagetools/components/SettingsPane.vue');
  // 空态文案与 0 条计数都由状态渲染
  assert.match(settings, /it-phrase-empty/);
  assert.match(settings, /\{\{\s*draft\.length\s*\}\}\s*条/);
  // 保存 0 条时把空数组原样交给桥接层，不按「空即未改」过滤
  assert.match(settings, /saveQuickPhrases\(draft\.value\.map/);
  assert.doesNotMatch(settings, /if \(!draft\.value\.length\)/);
  return loadLib('rename.mjs').then((m) => {
    assert.equal(typeof m.phrasesFromConfig, 'function');
    // 空数组 = 用户删光了，必须保持为空
    assert.deepEqual(m.phrasesFromConfig([]), []);
    assert.deepEqual(m.phrasesFromConfig(['_夜巡', '_已核']), ['_夜巡', '_已核']);
    // 缺字段 / 非数组 / 读取失败才用默认
    for (const bad of [undefined, null, '', 'x', 0, {}, { length: 0 }]) {
      assert.deepEqual(m.phrasesFromConfig(bad), m.DEFAULT_PHRASES, `应回落默认：${String(bad)}`);
    }
    // 返回副本，不与默认常量共享引用
    const copy = m.phrasesFromConfig(m.DEFAULT_PHRASES);
    copy.push('_临时');
    assert.equal(m.DEFAULT_PHRASES.includes('_临时'), false);
    // 0 条通过校验，保存链路允许
    assert.equal(m.validatePhrases([]), null);
    assert.equal(m.validatePhrases(['']), '第 1 条：快捷短语不能为空');
  });
});

test('单图改名：主名未变化不发 RPC、不改文件，就地提示「名称未变化」', () => {
  const panel = read('src/imagetools/components/RenamePanel.vue');
  const app = read('src/imagetools/ImageToolsApp.vue');
  assert.match(panel, /名称未变化/);
  assert.match(panel, /isSameBaseName/);
  assert.match(panel, /hintText/, '未变化提示走输入框下方的就地提示，不弹成功 toast');
  // 未变化判定必须位于 emit('submit') 之前并提前返回（emit 出去就会走改名 RPC）
  const submit = panel.slice(panel.indexOf('function submit()'), panel.indexOf('watch('));
  const checkAt = submit.indexOf('isSameBaseName(props.file.name');
  const emitAt = submit.indexOf("emit('submit'");
  assert.ok(checkAt > 0, 'submit 内必须比对当前主名');
  assert.ok(emitAt > checkAt, '未变化判定必须在 emit submit 之前');
  assert.match(submit.slice(checkAt, emitAt), /return;/);
  // 真实改名照常：成功 toast 只属于 App 的改名成功分支
  assert.match(app, /已重命名为/);
  return loadLib('rename.mjs').then((m) => {
    assert.equal(typeof m.isSameBaseName, 'function');
    assert.equal(m.isSameBaseName('现场-01.jpg', '现场-01'), true);
    assert.equal(m.isSameBaseName('现场-01.jpg', ' 现场-01 '), true);
    assert.equal(m.isSameBaseName('现场-01.jpg', '现场-02'), false);
    assert.equal(m.isSameBaseName('现场-01.jpg', '现场-01.jpg'), false);
    assert.equal(m.isSameBaseName('无扩展名', '无扩展名'), true);
    assert.equal(m.isSameBaseName('.gitignore', '.gitignore'), true);
  });
});

test('视觉基线：遮罩浓度、标题层级与工具条计数器与设计稿同值', () => {
  const css = read('src/imagetools/imagetools.css');
  // 遮罩只在 ≤960px 的抽屉态存在；取值必须与 design/桌面-图片查看器.html 的 .drawer-scrim 一致
  const narrow = cssBlock(css, '@media (max-width: 960px)');
  const scrim = cssBlock(narrow, '.it-scrim {');
  assert.match(
    scrim,
    /background:\s*rgba\(29,\s*33,\s*41,\s*0\.38\)/,
    '抽屉遮罩应按设计稿取 .38；.18 会让 900 宽下几乎看不出压暗'
  );

  // 板块标题：卡片/关联对比/格式卡同一组，14.5px / 600
  const cardH3 = cssBlock(css, '.it-card-head h3,');
  assert.match(cardH3, /font-size:\s*14\.5px/);
  assert.match(cardH3, /font-weight:\s*600/);

  // 信息栏标题 13px / 600（字号不动，只把 700 收回 600）
  const insHeadB = cssBlock(css, '.it-ins-head b {');
  assert.match(insHeadB, /font-size:\s*13px/);
  assert.match(insHeadB, /font-weight:\s*600/);

  // 设置页标题 20px，保留 700
  const settingsH2 = cssBlock(css, '.it-settings-head h2 {');
  assert.match(settingsH2, /font-size:\s*20px/);
  assert.match(settingsH2, /font-weight:\s*700/);

  // 工具条计数器：52px / 10.5px，数字仍 600 且等宽数字
  const counter = cssBlock(css, '.it-counter {');
  assert.match(counter, /min-width:\s*52px/);
  assert.match(counter, /font-size:\s*10\.5px/);
  assert.match(counter, /font-variant-numeric:\s*tabular-nums/);
  const counterB = cssBlock(css, '.it-counter b {');
  assert.match(counterB, /font-weight:\s*600/);

  assert.doesNotMatch(
    cssBlock(css, '.it-counter {'),
    /font-size:\s*12px/,
    '计数器与 12px 的功能标签同重会抬高辅助信息的视觉权重'
  );
});

test('冷启动补取独立调度：任一初始化抛错也不能丢掉待开文件', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  // 补取必须是可重复调度的独立函数（不依赖 onMounted 里前面的 await 是否成功）
  assert.match(app, /function bootstrapPendingFiles/);
  assert.match(app, /bootstrapPendingFiles\(\)/);
  assert.match(app, /COLD_START_ATTEMPTS/);
  // 订阅与设置加载各自兜错，避免一处抛错拖垮冷启动
  assert.match(app, /try \{\s*unsubscribe = api\.onEvent\(onEvent\)/);
  assert.match(app, /function schedulePendingRetry|window\.setTimeout\(bootstrapPendingFiles/);
});

// ---- v2.4.3 CI：图片处理列表 rows=0（短名身份不同） ----
// 现场：渲染层拿到 8.3 短名（C:\Users\RUNNER~1\…），核心列举返回长名（…\runneradmin\…）。
// 旧实现把核心返回的长名当 Map 键，用传入的短名去 has()，必然不命中，
// 于是被判定「已不在原位置」全部跳过 → 列表 0 行。
// 下面用可控假件复现这条真实链路（不是静态文字断言）：
//   - known：核心列举结果，键是长名；
//   - inspect：向核心问「这个路径的真实身份」，输入短名返回长名（主进程仍校验目录授权）。
test('addPaths 路径身份：短名要靠核心规范路径归一，不能按字符串判定缺失', async () => {
  const { resolvePickedFiles } = await loadLib('process.mjs');
  assert.equal(typeof resolvePickedFiles, 'function', 'process.mjs 必须导出 resolvePickedFiles');

  const LONG_DIR = 'C:\\Users\\runneradmin\\AppData\\Local\\Temp\\it-gui\\样本';
  const SHORT_DIR = 'C:\\Users\\RUNNER~1\\AppData\\Local\\Temp\\it-gui\\样本';
  const longFile = (name) => `${LONG_DIR}\\${name}`;
  const shortFile = (name) => `${SHORT_DIR}\\${name}`;
  const known = new Map([
    [longFile('01-秋检现场.jpg'), { name: '01-秋检现场.jpg', path: longFile('01-秋检现场.jpg') }],
    [
      longFile('02-现场标识牌.png'),
      { name: '02-现场标识牌.png', path: longFile('02-现场标识牌.png') },
    ],
  ]);
  const listedDirs = new Set([SHORT_DIR]);
  // 核心的规范路径：短名 → 长名；不存在的文件返回空串（与主进程 inspect 失败同义）
  const canonical = (p) =>
    p.startsWith(SHORT_DIR + '\\')
      ? p.replace(SHORT_DIR, LONG_DIR)
      : p.startsWith(LONG_DIR + '\\')
        ? p
        : '';
  const calls = [];
  const inspect = async (p) => {
    calls.push(p);
    const target = canonical(p);
    return target && known.has(target) ? target : '';
  };
  const dirOf = (p) =>
    p.slice(0, Math.max(p.lastIndexOf(String.fromCharCode(92)), p.lastIndexOf('/')));

  const result = await resolvePickedFiles({
    picked: [shortFile('01-秋检现场.jpg'), shortFile('02-现场标识牌.png')],
    known,
    listedDirs,
    dirOf,
    inspect,
  });
  assert.deepEqual(
    result.entries.map((item) => item.path),
    [longFile('01-秋检现场.jpg'), longFile('02-现场标识牌.png')],
    '短名必须被归一成核心规范路径后进入列表（旧实现这里是空数组 → rows=0）'
  );
  assert.equal(result.missing.length, 0, '短名不是「已不在原位置」');
  assert.equal(result.entries[0].info.name, '01-秋检现场.jpg', '元数据仍来自核心列举');

  // 真实缺失：列举成功但核心也认不出 → 记为缺失并提示，不伪造记录
  const gone = await resolvePickedFiles({
    picked: [shortFile('09-已删除.jpg')],
    known,
    listedDirs,
    dirOf,
    inspect,
  });
  assert.deepEqual(gone.entries, [], '缺失文件不得进入列表');
  assert.deepEqual(gone.missing, [shortFile('09-已删除.jpg')], '缺失文件要计入提示数量');

  // 长名直通：已在 known 里就不必额外 inspect（保住常见路径的性能）
  const fastCalls = [];
  const fast = await resolvePickedFiles({
    picked: [longFile('01-秋检现场.jpg')],
    known,
    listedDirs: new Set([LONG_DIR]),
    dirOf,
    inspect: async (p) => {
      fastCalls.push(p);
      return p;
    },
  });
  assert.deepEqual(
    fast.entries.map((item) => item.path),
    [longFile('01-秋检现场.jpg')]
  );
  assert.deepEqual(fastCalls, [], '长名命中已知键时不应再调用 inspect');

  // 非法/空入参不炸
  assert.deepEqual(
    (await resolvePickedFiles({ picked: [], known, listedDirs, dirOf, inspect })).entries,
    []
  );
  assert.deepEqual(
    (await resolvePickedFiles({ picked: [null, '', 3], known, listedDirs, dirOf, inspect }))
      .entries,
    []
  );
});

// v2.4.4 CI：短名 TEMP 下「坏图」等无法解码的文件会让 inspect 抛错，
// 旧实现在此把「inspect 失败」当成「已不在原位置」，于是弹出误报提示。
// 事实是：核心列举（list）里明确有这张图 → 它存在，必须进列表且不得计入 missing。
test('addPaths 路径身份：核心列举里有、inspect 因解码失败报错时不得误报缺失', async () => {
  const { resolvePickedFiles } = await loadLib('process.mjs');
  const LONG_DIR = 'C:\\Users\\runneradmin\\AppData\\Local\\Temp\\it-gui\\样本';
  const SHORT_DIR = 'C:\\Users\\RUNNER~1\\AppData\\Local\\Temp\\it-gui\\样本';
  const longFile = (name) => `${LONG_DIR}\\${name}`;
  const shortFile = (name) => `${SHORT_DIR}\\${name}`;
  const dirOf = (p) =>
    p.slice(0, Math.max(p.lastIndexOf(String.fromCharCode(92)), p.lastIndexOf('/')));
  // 核心列举里有这张坏图（存在），只是解码失败所以 inspect 会 reject
  const known = new Map([
    [
      longFile('06-坏图.jpg'),
      { name: '06-坏图.jpg', path: longFile('06-坏图.jpg'), supported: false },
    ],
  ]);
  const listedDirs = new Set([SHORT_DIR]);
  const knownDirs = new Map([[SHORT_DIR, LONG_DIR]]);
  const inspectCalls = [];
  const inspect = async (p) => {
    inspectCalls.push(p);
    throw new Error('IMG_DECODE_FAILED');
  };

  const result = await resolvePickedFiles({
    picked: [shortFile('06-坏图.jpg')],
    known,
    listedDirs,
    knownDirs,
    dirOf,
    inspect,
  });
  assert.deepEqual(
    result.entries.map((item) => item.path),
    [longFile('06-坏图.jpg')],
    '核心列举证明它存在：必须进列表（短名经核心规范目录 + 精确文件名命中）'
  );
  assert.deepEqual(result.missing, [], '存在文件不得误报「已不在原位置」');

  // 真正缺失仍然要报：核心规范目录里没有这个名字
  const gone = await resolvePickedFiles({
    picked: [shortFile('09-已删除.jpg')],
    known,
    listedDirs,
    knownDirs,
    dirOf,
    inspect,
  });
  assert.deepEqual(gone.entries, [], '缺失文件不得进入列表');
  assert.deepEqual(
    gone.missing,
    [shortFile('09-已删除.jpg')],
    '缺失仍要计入提示，不能因修复而吞掉'
  );

  // inspect 成功这条路径不变：有规范路径就以它为准（即便列举里没有）
  const viaInspect = await resolvePickedFiles({
    picked: [shortFile('07-新拍.jpg')],
    known,
    listedDirs,
    knownDirs,
    dirOf,
    inspect: async () => longFile('07-新拍.jpg'),
  });
  assert.deepEqual(
    viaInspect.entries.map((item) => item.path),
    [longFile('07-新拍.jpg')]
  );
  assert.deepEqual(viaInspect.missing, [], 'inspect 证明存在即不得报缺失');
});

// v2.4.5 复核：坏图兜底分支不得写死 Windows 分隔符。
// POSIX 上核心 listing.directory 与 file.path 都用 '/'，若拼成 '/home/user/图片\06-坏图.jpg' 永不命中，
// 结果仍是「误报缺失 + 列表里丢掉这张图」——同一缺陷在 Linux/麒麟上原样存在。
test('addPaths 路径身份：POSIX 路径下坏图兜底按真实分隔符命中，不误报缺失', async () => {
  const { resolvePickedFiles } = await loadLib('process.mjs');
  const CANON = '/home/user/图片';
  const ALIAS = '/home/user/link-to-图片';
  const canonFile = (name) => `${CANON}/${name}`;
  const aliasFile = (name) => `${ALIAS}/${name}`;
  const known = new Map([
    [
      canonFile('06-坏图.jpg'),
      { name: '06-坏图.jpg', path: canonFile('06-坏图.jpg'), supported: false },
    ],
  ]);
  const dirOf = (p) => p.slice(0, p.lastIndexOf('/'));
  const inspectFail = async () => {
    throw new Error('IMG_DECODE_FAILED');
  };

  const result = await resolvePickedFiles({
    picked: [aliasFile('06-坏图.jpg')],
    known,
    listedDirs: new Set([ALIAS]),
    knownDirs: new Map([[ALIAS, CANON]]),
    dirOf,
    inspect: inspectFail,
  });
  assert.deepEqual(
    result.entries.map((item) => item.path),
    [canonFile('06-坏图.jpg')],
    'POSIX 下必须按 / 拼出规范路径并命中（写死 \\ 会得到 …图片\\06-坏图.jpg，永不命中）'
  );
  assert.deepEqual(result.missing, [], 'POSIX 存在文件不得误报「已不在原位置」');

  // 真缺失仍要报，不能因为修复而吞掉提示
  const gone = await resolvePickedFiles({
    picked: [aliasFile('09-已删除.jpg')],
    known,
    listedDirs: new Set([ALIAS]),
    knownDirs: new Map([[ALIAS, CANON]]),
    dirOf,
    inspect: inspectFail,
  });
  assert.deepEqual(gone.entries, [], '缺失文件不得进入列表');
  assert.deepEqual(gone.missing, [aliasFile('09-已删除.jpg')], 'POSIX 真缺失仍要计入提示');

  // 根目录与已有尾分隔符：不得拼出 '//' 或 '\\' 混用
  const rooted = await resolvePickedFiles({
    picked: ['/06-坏图.jpg'],
    known: new Map([['/06-坏图.jpg', { name: '06-坏图.jpg', supported: false }]]),
    listedDirs: new Set(['/']),
    knownDirs: new Map([['/', '/']]),
    dirOf: () => '/',
    inspect: inspectFail,
  });
  assert.deepEqual(
    rooted.entries.map((item) => item.path),
    ['/06-坏图.jpg'],
    '根目录规范路径已是 / 结尾，不得再拼一个分隔符'
  );
  assert.deepEqual(rooted.missing, []);

  const trailing = await resolvePickedFiles({
    picked: [`${CANON}/06-坏图.jpg`],
    known,
    listedDirs: new Set([CANON]),
    knownDirs: new Map([[CANON, `${CANON}/`]]),
    dirOf,
    inspect: inspectFail,
  });
  assert.deepEqual(
    trailing.entries.map((item) => item.path),
    [canonFile('06-坏图.jpg')],
    '规范目录带尾分隔符时不得拼出双斜杠'
  );
  assert.deepEqual(trailing.missing, []);
});

test('ProcessPane 接线：核心列举的 directory 要作为规范目录参与身份判定', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  const block = pane.slice(
    pane.indexOf('async function addPaths'),
    pane.indexOf('function removeEntry')
  );
  assert.match(pane, /knownDirs/, '必须把核心返回的规范目录传给 resolvePickedFiles');
  assert.match(block, /listing(\s*\.\s*directory|\?\.directory)/, '要读 listing.directory');
});

test('ProcessPane 接线：列表身份走 api.inspect，不按 basename/包含关系猜', () => {
  const pane = read('src/imagetools/components/ProcessPane.vue');
  const processLib = read('src/imagetools/lib/process.mjs');
  assert.match(
    processLib,
    /export async function resolvePickedFiles|export function resolvePickedFiles/
  );
  // ProcessPane 必须把「按核心规范路径归一」交给 resolvePickedFiles，并提供 inspect 探针
  assert.match(pane, /resolvePickedFiles\s*\(/);
  assert.match(pane, /api\.inspect\s*\(/, '必须用 api.inspect 取核心规范路径');
  // 旧缺陷形态不得回潮：直接用已知 Map 的 has(传入路径)
  assert.doesNotMatch(pane, /known\.has\(\s*path\s*\)/, '不得再用传入路径直接查核心列举的键');
  // 身份判定不得退化成名字匹配
  const identityBlock = pane.slice(
    pane.indexOf('async function addPaths'),
    pane.indexOf('function removeEntry')
  );
  assert.doesNotMatch(
    identityBlock,
    /\.name\s*===\s*.*basename|basename\([^)]*\)\s*===/,
    '不得用 basename 相等代替真实身份'
  );
});

test('查看器实际 load 后才 ready，并启动全目录后台预加载', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  const viewer = read('src/imagetools/components/ViewerPane.vue');
  assert.match(app, /createPreviewLoader/);
  assert.match(app, /function onPhotoLoad\(/);
  assert.match(app, /previewLoader\.schedule\(files\.value,\s*index\.value\)/);
  assert.match(viewer, /emit\('photo-load'/);
  const loadAt = app.slice(
    app.indexOf('async function loadAt('),
    app.indexOf('function onPhotoLoad(')
  );
  assert.ok(loadAt.length > 0);
  assert.doesNotMatch(loadAt, /state\.value = 'ready'/);
});

test('首图预览先于全目录列举发起，目录返回后复用首图缓存', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  const openPaths = app.slice(
    app.indexOf('async function openPaths('),
    app.indexOf('function normalizeFile(')
  );
  assert.ok(openPaths.length > 0);
  assert.ok(openPaths.indexOf('loadAt(0)') < openPaths.indexOf('api.list(first)'));
  assert.match(openPaths, /previewLoader\.rekey\(/);
});

test('切图时旧图迟到的 load/error 不得覆盖新图状态', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  const loadAt = app.slice(
    app.indexOf('async function loadAt('),
    app.indexOf('function onPhotoLoad(')
  );
  assert.match(loadAt, /previewUrl\.value = ''/);
  const loaded = app.slice(
    app.indexOf('function onPhotoLoad('),
    app.indexOf('function onPhotoError(')
  );
  const failed = app.slice(
    app.indexOf('function onPhotoError('),
    app.indexOf('function invalidatePreview(')
  );
  assert.match(loaded, /!url/);
  assert.match(failed, /url !== previewUrl\.value/);
});


test('切到不支持格式也会使上一张的在飞预览失效', () => {
  const app = read('src/imagetools/ImageToolsApp.vue');
  const loadAt = app.slice(app.indexOf('async function loadAt('), app.indexOf('function onPhotoLoad('));
  const sequence = loadAt.indexOf('const seq = ++previewSeq');
  const unsupported = loadAt.indexOf('if (!file.supported)');
  assert.ok(sequence >= 0 && unsupported >= 0 && sequence < unsupported);
});
