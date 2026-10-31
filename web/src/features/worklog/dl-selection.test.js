import test from 'node:test';
import assert from 'node:assert/strict';
import { createResourceGates } from '../core/async.js';
import { invalidateScopeResources, reconcilePhotoSelection } from './dl-selection.js';

test('scope 切换不误作废 meta 请求', () => {
  const gates = createResourceGates(['month', 'logs', 'report', 'photos', 'paSync', 'meta']);
  const metaTicket = gates.next('meta');
  const photoTicket = gates.next('photos');
  invalidateScopeResources(gates);
  assert.equal(gates.isCurrent(metaTicket), true, 'meta 代际不应被 scope 切换作废');
  assert.equal(gates.isCurrent(photoTicket), false, '照片清单应随 scope 重查');
});

test('无关保存重载照片清单时保留仍存在勾选，仅移除已消失项', () => {
  const previous = ['p1', 'p2', 'p3'];
  const list = [{ id: 'p1' }, { id: 'p3' }, { id: 'p4' }];
  assert.deepEqual(reconcilePhotoSelection(previous, list), ['p1', 'p3']);
});

test('用户主动改范围查询时清空勾选（传空之前的选择）', () => {
  assert.deepEqual(reconcilePhotoSelection([], [{ id: 'p1' }]), []);
});
