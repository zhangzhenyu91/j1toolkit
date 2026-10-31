// 刷题批次对齐：wrong/fav（及 rand）按大纲稳定 id 取批；响应按大纲区间对齐，
// 缺失题目保留空槽，避免 filter 后下标前移导致答题/收藏落到别的题。
function stableBatchIds(outline, start, limit) {
  return (outline || [])
    .slice(start, start + limit)
    .map((o) => o && o.id)
    .filter(Boolean);
}

function alignBatch(outline, start, limit, raw) {
  const slice = (outline || []).slice(start, start + limit);
  if (!slice.length) return [];
  const map = {};
  (raw || []).forEach((q) => {
    if (q && q.id) map[q.id] = q;
  });
  return slice.map((o) => map[o && o.id] || null);
}

module.exports = { stableBatchIds, alignBatch };
