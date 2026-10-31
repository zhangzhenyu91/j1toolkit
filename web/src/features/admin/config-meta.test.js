import test from 'node:test';
import assert from 'node:assert/strict';
import { CONFIG_EFFECT_NOTICE, configEffectHint, requiresRestart } from './config-meta.js';

test('JWT 有效期键标记为需重启，其他键仍为热生效', () => {
  assert.equal(requiresRestart('JWT_EXPIRES'), true);
  assert.equal(requiresRestart('JWT_WEB_EXPIRES'), true);
  assert.equal(requiresRestart('DIFY_SAFEDAY_API_KEY'), false);
});

test('日志保留天数启动时读取，保存后需重启', () => {
  assert.equal(requiresRestart('APP_LOG_RETENTION_DAYS'), true);
  assert.equal(requiresRestart('APP_LOG_ARCHIVE_RETENTION_DAYS'), true);
  const hint = configEffectHint('APP_LOG_RETENTION_DAYS');
  assert.match(hint, /重启/);
  assert.doesNotMatch(hint, /即时生效/);
  assert.match(CONFIG_EFFECT_NOTICE, /日志保留/);
});

test('JWT 提示明确重启后仅对新签发令牌生效', () => {
  const hint = configEffectHint('JWT_EXPIRES');
  assert.match(hint, /重启/);
  assert.match(hint, /新签发/);
  assert.match(hint, /已登录/);
  assert.match(CONFIG_EFFECT_NOTICE, /重启/);
  assert.doesNotMatch(CONFIG_EFFECT_NOTICE, /保存立即热生效/);
});
