// 安全日用户模板纯逻辑测试（node --test，无第三方依赖）。

import assert from 'node:assert/strict';
import test from 'node:test';

import {
  MAX_TEMPLATES,
  normalizeTemplateList,
  resolveTemplateId,
  templateCreateLimitError,
  templateSubmitError,
  validateTemplateForm,
} from './templates.js';

function userTemplates(n) {
  return Array.from({ length: n }, (_, i) => ({ id: `tpl-${i}`, mode: 'dynamic' }));
}

test('用户模板不足 50 套允许新建', () => {
  assert.equal(templateCreateLimitError(userTemplates(MAX_TEMPLATES - 1)), '');
  assert.equal(templateCreateLimitError([]), '');
});

test('用户模板达到 50 套明确阻止并提示', () => {
  const err = templateCreateLimitError(userTemplates(MAX_TEMPLATES));
  assert.match(err, /最多 50 套/);
  assert.match(err, /删除/);
});

test('新建表单只要求名称、章节和 API Key，不需要 docx', () => {
  const base = {
    name: '模板',
    rows: [{ key: 'part_a', title: '一、章节甲' }],
    apiKey: '',
    isNew: true,
  };
  assert.match(validateTemplateForm(base), /Dify API Key/);
  assert.equal(validateTemplateForm({ ...base, apiKey: 'k' }), '');
});

test('无用户模板时模板 ID 与默认 ID 都为空', () => {
  assert.equal(resolveTemplateId([], ''), '');
  assert.equal(resolveTemplateId([], 'builtin'), '');
  assert.deepEqual(normalizeTemplateList({ templates: [], defaultId: 'builtin' }), {
    templates: [],
    defaultId: '',
  });
});

test('模板选择依次使用记忆、用户默认和首个用户模板', () => {
  const list = [
    { id: 'tpl-first', isDefault: false },
    { id: 'tpl-default', isDefault: true },
  ];
  assert.equal(resolveTemplateId(list, 'tpl-first'), 'tpl-first');
  assert.equal(resolveTemplateId(list, 'deleted'), 'tpl-default');
  assert.equal(resolveTemplateId([list[0]], ''), 'tpl-first');
  assert.equal(
    normalizeTemplateList({ templates: list, defaultId: 'builtin' }).defaultId,
    'tpl-default'
  );
});

test('record_id 和 batch_id 不得作为章节变量', () => {
  for (const key of ['record_id', 'batch_id']) {
    const err = validateTemplateForm({
      name: '模板',
      rows: [{ key, title: '章节' }],
      apiKey: 'k',
      isNew: true,
    });
    assert.match(err, /保留字/);
  }
});

test('表单元数据或用户模板未就绪时不得提交', () => {
  const valid = { metaReady: true, templateLoading: false, templateError: '', templateId: 'tpl-1' };
  assert.match(templateSubmitError({ ...valid, metaReady: false }), /表单数据/);
  assert.match(templateSubmitError({ ...valid, templateLoading: true }), /模板/);
  assert.equal(templateSubmitError({ ...valid, templateError: '加载失败' }), '加载失败');
  assert.match(templateSubmitError({ ...valid, templateId: '' }), /创建并选择/);
  assert.equal(templateSubmitError(valid), '');
});
