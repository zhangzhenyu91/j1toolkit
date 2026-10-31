// 请求代际工具：页面快速切换选中项/路径/班组/分段时，只允许最新一次请求落地。
// 用法：this._guard = createRequestGuard(); const t = this._guard.begin(key);
//       const data = await request(...); if (!this._guard.isCurrent(t)) return;

// 带 key 的请求代际：同 key 的新请求也会作废旧请求（防同路径重复点击）
function createRequestGuard() {
  let seq = 0;
  let key = null;
  return {
    begin(nextKey) {
      seq += 1;
      key = nextKey;
      return { seq, key };
    },
    isCurrent(ticket) {
      return !!ticket && ticket.seq === seq && ticket.key === key;
    },
    invalidate() {
      seq += 1;
      key = null;
    },
  };
}

module.exports = { createRequestGuard };
