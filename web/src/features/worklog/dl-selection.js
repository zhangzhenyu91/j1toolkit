// 出工日志批量照片勾选与 scope 资源代际：
// - scope（全部/我的）只影响 month/logs/report/photos/paSync，不得误作废 meta（数据源选项）；
// - 无关保存触发照片清单重查时保留仍存在的勾选，用户主动改范围查询才清空。

const SCOPE_RESOURCES = ['month', 'logs', 'report', 'photos', 'paSync'];

export function invalidateScopeResources(gates) {
  SCOPE_RESOURCES.forEach((name) => gates.invalidate(name));
}

// reconcilePhotoSelection 保留 previous 中仍存在于新清单的 id。
export function reconcilePhotoSelection(previous, list) {
  const available = new Set((list || []).map((p) => p && p.id));
  return (previous || []).filter((id) => available.has(id));
}
