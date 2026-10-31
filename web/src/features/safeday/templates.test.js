// 安全日模板纯逻辑测试（node --test，无第三方依赖）：
// 重点覆盖自定义模板 50 套前端拦截与既有表单校验不变。

import assert from 'node:assert/strict';
import test from 'node:test';

import { MAX_TEMPLATES, templateCreateLimitError, validateTemplateForm } from './templates.js';

function customTemplates(n) {
  return Array.from({ length: n }, (_, i) => ({ id: `tpl-${i}`, mode: 'dynamic' }));
}

test('自定义模板不足 50 套允许新建', () => {
  const list = [...customTemplates(MAX_TEMPLATES - 1), { id: 'builtin', mode: 'static' }];
  assert.equal(templateCreateLimitError(list), '');
});

test('自定义模板达到 50 套明确阻止并提示', () => {
  const list = [...customTemplates(MAX_TEMPLATES), { id: 'builtin', mode: 'static' }];
  const err = templateCreateLimitError(list);
  assert.match(err, /最多 50 套/);
  assert.match(err, /删除/);
});

test('内置模板不计入 50 套上限', () => {
  const list = [...customTemplates(MAX_TEMPLATES - 1), { id: 'builtin', mode: 'static' }];
  assert.equal(templateCreateLimitError(list), '');
  assert.equal(templateCreateLimitError([]), '');
});

test('新建表单仍要求 API Key 与 docx（无全局回退）', () => {
  const base = {
    name: '模板',
    rows: [{ key: 'part_a', title: '章节甲' }],
    apiKey: '',
    isNew: true,
    docx: { name: 't.docx', size: 1 },
  };
  assert.match(validateTemplateForm(base), /Dify API Key/);
  assert.equal(validateTemplateForm({ ...base, apiKey: 'k' }), '');
});
