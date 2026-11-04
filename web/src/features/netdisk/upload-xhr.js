const CHUNK_TIMEOUT_MS = 5 * 60 * 1000;

// putChunk 用 XHR 发出裸分片，并上传实际发送字节；响应仍交给 API 层解释平台信封。
export function putChunk({
  url = '',
  headers = {},
  body,
  timeout = CHUNK_TIMEOUT_MS,
  onProgress,
  createXHR = () => new XMLHttpRequest(),
}) {
  return new Promise((resolve, reject) => {
    const xhr = createXHR();
    const size = Math.max(0, Number(body.size) || 0);
    let lastLoaded = 0;
    const report = (loaded) => {
      const next = Math.max(lastLoaded, Math.min(size, Math.floor(Number(loaded) || 0)));
      if (next > lastLoaded) {
        lastLoaded = next;
        if (onProgress) onProgress(next);
      }
    };
    const networkFail = () => {
      const err = new Error('网络异常，分片上传失败');
      err.network = true;
      err.retry = true;
      reject(err);
    };

    xhr.open('PUT', url);
    xhr.timeout = timeout;
    Object.entries(headers).forEach(([name, value]) => xhr.setRequestHeader(name, value));
    if (xhr.upload) {
      xhr.upload.onprogress = (event) => report(event.loaded);
      xhr.upload.onload = () => report(size);
    }
    xhr.onload = () => {
      let responseBody = null;
      try {
        responseBody = JSON.parse(xhr.responseText);
      } catch (e) {
        responseBody = null;
      }
      resolve({ status: xhr.status, body: responseBody });
    };
    xhr.onerror = networkFail;
    xhr.ontimeout = networkFail;
    xhr.onabort = networkFail;
    xhr.send(body);
  });
}
