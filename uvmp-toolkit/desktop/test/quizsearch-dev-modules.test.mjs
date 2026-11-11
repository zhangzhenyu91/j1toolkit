// 题库解析/模板模块必须在 Vite dev（原生 ESM，不做 CJS 转换）下可加载：
// 页面用动态 import 取模块，dev 阶段 Vite 只做 import 重写，`.cjs` 里的
// module.exports / require 会原样进浏览器并直接 ReferenceError。
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';

const DESKTOP = path.dirname(fileURLToPath(new URL('.', import.meta.url)));
const MODULES = ['/electron/quizsearch/workbook.mjs', '/electron/quizsearch/template.mjs'];

// 只看可执行代码：注释里提到 module.exports 不算 CJS 胶水
function stripComments(code) {
  return code.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
}

async function withDevServer(t, fn) {
  const server = await createServer({
    root: DESKTOP,
    configFile: false,
    logLevel: 'silent',
    appType: 'custom',
    server: { middlewareMode: true },
  });
  t.after(() => server.close());
  return fn(server);
}

test('Vite dev 以纯 ESM 提供题库模块（不含 CJS 胶水）', async (t) => {
  await withDevServer(t, async (server) => {
    for (const url of MODULES) {
      const out = await server.transformRequest(url);
      assert.ok(out && out.code, url + ' 应能取到 dev 转换结果');
      const code = stripComments(out.code);
      assert.doesNotMatch(code, /\bmodule\.exports\b/, url + ' 不得残留 module.exports');
      assert.doesNotMatch(code, /\brequire\s*\(/, url + ' 不得残留 require()');
      assert.match(code, /\bexport\b/, url + ' 必须暴露 ESM 导出');
    }
  });
});

test('dev 模块实际可执行：生成模板并被题库读取器原样读回', async (t) => {
  await withDevServer(t, async (server) => {
    const workbook = await server.ssrLoadModule('/electron/quizsearch/workbook.mjs');
    const template = await server.ssrLoadModule('/electron/quizsearch/template.mjs');
    assert.equal(typeof workbook.readWorkbookRows, 'function');
    assert.equal(typeof template.buildTemplateBlob, 'function');

    const blob = await template.buildTemplateBlob();
    assert.ok(blob.size > 0);
    const rows = await workbook.readWorkbookRows(new Uint8Array(await blob.arrayBuffer()));
    assert.deepEqual(rows[0].slice(0, 2), ['题干', '答案']);
    assert.equal(rows.length, 4);
  });
});
