// 树障/交跨同步页：历史参数回填后按 restored.action 高亮对应主动作按钮，
// 且每页至多一个 primary（避免回填 nightly 后高亮按钮却执行 tick）。
// 用 @vue/compiler-sfc 读模板绑定，并实际执行页面里的按钮样式函数。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parse } from '@vue/compiler-sfc';

const PAGES = ['TreeSyncPage.vue', 'CrossingSyncPage.vue'];

function readPage(file) {
  return readFileSync(fileURLToPath(new URL(`../src/pages/${file}`, import.meta.url)), 'utf8');
}

function elements(node, out = []) {
  for (const child of node.children || []) {
    if (child.type === 1) {
      out.push(child);
    } else if (child.type === 11) {
      for (const branch of child.branches || []) elements(branch, out);
    }
    elements(child, out);
  }
  return out;
}

function directive(node, name, arg) {
  return (node.props || []).find(
    (prop) => prop.type === 7 && prop.name === name && (!arg || prop.arg?.content === arg)
  );
}

function scriptFunction(script, name) {
  const match = script.match(new RegExp(`function ${name}\\(action\\) \\{[\\s\\S]*?\\n\\}`));
  assert.ok(match, `缺少 ${name} 函数`);
  return match[0];
}

for (const file of PAGES) {
  test(`${file} 回填 action 后高亮对应主动作且至多一个 primary`, () => {
    const source = readPage(file);
    const { descriptor } = parse(source, { filename: file });
    const script = descriptor.scriptSetup?.content || '';
    const buttons = elements(descriptor.template.ast).filter((node) => node.tag === 't-button');

    const byAction = {};
    for (const button of buttons) {
      const click = directive(button, 'on', 'click');
      const action = click?.exp?.content;
      if (action !== 'startRun' && action !== 'startNightly') continue;
      const expected = action === 'startRun' ? 'tick' : 'nightly';
      const theme = directive(button, 'bind', 'theme');
      const variant = directive(button, 'bind', 'variant');
      assert.ok(theme, `${expected} 按钮缺少动态 theme 绑定`);
      assert.ok(variant, `${expected} 按钮缺少动态 variant 绑定`);
      assert.match(theme.exp.content, new RegExp(`syncButtonTheme\\('${expected}'\\)`));
      assert.match(variant.exp.content, new RegExp(`syncButtonVariant\\('${expected}'\\)`));
      const staticPrimary = (button.props || []).find(
        (prop) => prop.type === 6 && prop.name === 'theme' && prop.value?.content === 'primary'
      );
      assert.equal(staticPrimary, undefined, '不得保留静态 primary');
      byAction[expected] = button;
    }
    assert.deepEqual(Object.keys(byAction).sort(), ['nightly', 'tick']);

    assert.match(script, /const restoredAction = ref\('tick'\)/);
    assert.match(script, /restoredAction\.value = restored\.action/);

    const themeFn = scriptFunction(script, 'syncButtonTheme');
    const variantFn = scriptFunction(script, 'syncButtonVariant');
    const makeHelpers = new Function(
      'restoredAction',
      `${themeFn}\n${variantFn}\nreturn { syncButtonTheme, syncButtonVariant };`
    );
    const state = { value: 'nightly' };
    const helpers = makeHelpers(state);
    const themes = ['tick', 'nightly'].map(helpers.syncButtonTheme);
    assert.equal(themes.filter((theme) => theme === 'primary').length, 1);
    assert.equal(helpers.syncButtonTheme('nightly'), 'primary');
    assert.equal(helpers.syncButtonTheme('tick'), 'default');
    assert.equal(helpers.syncButtonVariant('nightly'), 'base');
    assert.equal(helpers.syncButtonVariant('tick'), 'outline');

    state.value = 'tick';
    assert.equal(helpers.syncButtonTheme('tick'), 'primary');
    assert.equal(helpers.syncButtonTheme('nightly'), 'default');
    assert.equal(helpers.syncButtonVariant('tick'), 'base');
    assert.equal(helpers.syncButtonVariant('nightly'), 'outline');
  });
}
