/**
 * 图片加载失败恢复 —— URL 纯函数（无第三方依赖）
 * 约定：
 * - http(s) 地址首次失败以确定性 r=1 追加/替换查询参数自动重试一次，之后进失败态由用户点按重试、r 递增；
 * - 本地临时资源（blob: / data: / wxfile: 等）不加参数，重试仅重建 image 触发重新加载；
 * - 所有函数不改入参、不读全局状态，可安全复用于各分包。
 */

/** 是否本地临时资源（不可追加查询参数） */
function isLocalPhotoUrl(url) {
  return /^(blob:|data:|wxfile:|file:|http:\/\/tmp\/|http:\/\/usr\/)/i.test(String(url || ''));
}

/**
 * 生成第 n 次重试 URL（n≥1）。
 * 仅 http(s) 追加/替换 r=n 查询参数（去缓存穿透），其余协议原样返回。
 */
function photoRetryUrl(url, n) {
  const src = String(url || '');
  if (!src || isLocalPhotoUrl(src)) return src;
  const num = Math.max(1, parseInt(n, 10) || 1);
  const hashIdx = src.indexOf('#');
  const hash = hashIdx >= 0 ? src.slice(hashIdx) : '';
  const head = hashIdx >= 0 ? src.slice(0, hashIdx) : src;
  const qIdx = head.indexOf('?');
  const path = qIdx >= 0 ? head.slice(0, qIdx) : head;
  const params =
    qIdx >= 0
      ? head
          .slice(qIdx + 1)
          .split('&')
          .filter(Boolean)
      : [];
  const kept = params.filter((p) => !/^r=/i.test(p));
  kept.push('r=' + num);
  return path + '?' + kept.join('&') + hash;
}

/** 取当前应展示的 src：有重试变体用变体，否则原 URL */
function photoSrc(url, variants) {
  return (variants && variants[url]) || url;
}

/**
 * 图片失败恢复状态机（纯函数，逐 URL 独立）：
 * state = { level: number }，level=0 表示仍在原图；level=n 表示已用 r=n 变体。
 * photoLoadFail：一次 binderror 的处置 —— 首次失败对 http(s) 以 r=1 自动重试一次（failed:false），
 * 其后一律进失败态（failed:true），绝不循环自动重试；本地临时资源不加参数、直接进失败态。
 * photoLoadRetry：一次手动点按重试的处置 —— level 递增（首次手动 r=2），http(s) 换 r=level 变体，
 * 本地资源返回 src=null 表示按原地址重建 image。
 * 两个函数均不改入参、不读全局状态。
 */
function photoLoadFail(state, url) {
  const level = (state && state.level) || 0;
  if (level < 1 && !isLocalPhotoUrl(url)) {
    return { level: 1, src: photoRetryUrl(url, 1), failed: false };
  }
  return { level, src: null, failed: true };
}

function photoLoadRetry(state, url) {
  const level = ((state && state.level) || 0) + 1;
  return {
    level,
    src: isLocalPhotoUrl(url) ? null : photoRetryUrl(url, Math.max(1, level)),
    failed: false,
  };
}

module.exports = { isLocalPhotoUrl, photoRetryUrl, photoSrc, photoLoadFail, photoLoadRetry };
