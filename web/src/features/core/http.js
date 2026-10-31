// 请求头归一（各 feature 适配层共用）：请求体为 FormData / Blob / 二进制 / URLSearchParams 时，
// Content-Type 必须交给浏览器或运行时自行生成（FormData 需要 multipart boundary，手动写
// application/json 会让服务端无法解析）；JSON 请求沿用既有 application/json 默认头。

// isRawBody 运行时自定 Content-Type 的请求体。
export function isRawBody(body) {
  if (body == null) return false;
  if (typeof FormData !== 'undefined' && body instanceof FormData) return true;
  if (typeof Blob !== 'undefined' && body instanceof Blob) return true;
  if (typeof ArrayBuffer !== 'undefined') {
    if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) return true;
  }
  if (typeof URLSearchParams !== 'undefined' && body instanceof URLSearchParams) return true;
  return false;
}

// applyBodyHeaders 按请求体类型归一请求头：raw 体移除 Content-Type；
// 有请求体或非 GET/HEAD 时缺省补 JSON；GET/HEAD 无请求体不补，避免无谓预检。
// 调用方显式给出的非 JSON Content-Type 不会被覆盖。
export function applyBodyHeaders(headers = {}, body, method = '') {
  const out = { ...headers };
  const hasContentType = Object.keys(out).some((k) => k.toLowerCase() === 'content-type');
  if (isRawBody(body)) {
    Object.keys(out).forEach((k) => {
      if (k.toLowerCase() === 'content-type') delete out[k];
    });
    return out;
  }
  const verb = String(method || '').toUpperCase();
  const hasBody = body !== undefined && body !== null;
  if (!hasContentType && (hasBody || (verb !== 'GET' && verb !== 'HEAD'))) {
    out['Content-Type'] = 'application/json';
  }
  return out;
}
