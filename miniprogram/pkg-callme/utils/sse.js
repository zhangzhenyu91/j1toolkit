// 小程序端 SSE 工具：增量 UTF-8 解码器 + 兼容 data:/data: 两种格式的解析器 +
// 分块/完整响应回退装配器 + 静默看门狗（Call Me 对话流共用）
// 对接细节见《开发指南》第六章

// 增量 UTF-8 解码器：跨分片缓冲不完整的多字节序列。
// 旧实现按 chunk 独立 decodeURIComponent(escape(...))，中文（3 字节）被 chunk 边界
// 切断时整个 chunk 都会退化成乱码；这里改为字节级缓冲，只解码完整序列。
function createUtf8Decoder() {
  let tail = new Uint8Array(0); // 上次分片尾部未完成的字节

  // flush=false 时尾部不完整序列保留待下次；flush=true 时不完整序列以 U+FFFD 收尾
  function decode(bytes, flush) {
    let out = '';
    let i = 0;
    const n = bytes.length;
    while (i < n) {
      const b = bytes[i];
      let need = 0; // 续字节数
      if (b < 0x80) need = 0;
      else if (b >= 0xc2 && b < 0xe0) need = 1;
      else if (b >= 0xe0 && b < 0xf0) need = 2;
      else if (b >= 0xf0 && b < 0xf5) need = 3;
      else {
        out += '�'; // 非法前导字节
        i += 1;
        continue;
      }
      if (i + need >= n) {
        if (!flush) break; // 不完整序列留给下一分片
        out += '�';
        i += 1;
        continue;
      }
      let cp = b;
      let valid = true;
      for (let k = 1; k <= need; k += 1) {
        if ((bytes[i + k] & 0xc0) !== 0x80) {
          valid = false;
          break;
        }
      }
      if (valid && need > 0) {
        if (need === 1) cp = ((b & 0x1f) << 6) | (bytes[i + 1] & 0x3f);
        else if (need === 2) {
          cp = ((b & 0x0f) << 12) | ((bytes[i + 1] & 0x3f) << 6) | (bytes[i + 2] & 0x3f);
        } else {
          cp =
            ((b & 0x07) << 18) |
            ((bytes[i + 1] & 0x3f) << 12) |
            ((bytes[i + 2] & 0x3f) << 6) |
            (bytes[i + 3] & 0x3f);
        }
      }
      if (!valid) {
        out += '�';
        i += 1;
        continue;
      }
      out += cp > 0xffff ? String.fromCodePoint(cp) : String.fromCharCode(cp);
      i += need + 1;
    }
    return { text: out, rest: bytes.slice(i) };
  }

  return {
    // 传入一个分片的 ArrayBuffer，返回本次可解码出的字符串
    push(arrayBuffer) {
      const bytes = new Uint8Array(arrayBuffer);
      const all = new Uint8Array(tail.length + bytes.length);
      all.set(tail);
      all.set(bytes, tail.length);
      const r = decode(all, false);
      tail = r.rest;
      return r.text;
    },
    // 流结束：冲刷残余字节（不完整序列以替换字符收尾）
    end() {
      const r = decode(tail, true);
      tail = new Uint8Array(0);
      return r.text;
    },
  };
}

// 与服务端 utils/sse.js 同构的解析器：事件以 \n\n 分隔，忽略非 data 行（如心跳注释）
function createSseParser(onEvent) {
  let buffer = '';

  function handleEventStr(eventStr) {
    const lines = eventStr.split('\n');
    for (const line of lines) {
      if (!line.startsWith('data:')) continue;
      let jsonStr = line.substring(5);
      if (jsonStr.startsWith(' ')) jsonStr = jsonStr.substring(1);
      if (!jsonStr) continue;
      try {
        onEvent(JSON.parse(jsonStr));
      } catch (e) {
        // 忽略无法解析的行
      }
    }
  }

  return {
    push(chunkStr) {
      buffer += chunkStr;
      const events = buffer.split('\n\n');
      buffer = events.pop() || '';
      for (const eventStr of events) {
        handleEventStr(eventStr);
      }
    },
    end() {
      if (buffer.trim()) {
        const rest = buffer;
        buffer = '';
        handleEventStr(rest);
      }
    },
  };
}

// 分块数据统一转 ArrayBuffer（wx 分块为 ArrayBuffer，部分基础库可能给 TypedArray）
function toArrayBuffer(data) {
  if (data instanceof ArrayBuffer) return data;
  if (ArrayBuffer.isView(data)) return data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
  return new Uint8Array(0).buffer;
}

// SSE 流装配器：分块增量解析 + success 完整响应体回退。
// 部分基础库/网络路径不会回调 onChunkReceived，success 时需把 res.data 先推入 parser；
// 已收到过分块则忽略回退，避免整段回答重复追加。
function createSseStream(onEvent) {
  const parser = createSseParser(onEvent);
  const decoder = createUtf8Decoder();
  let chunks = 0;

  return {
    chunk(data) {
      if (data == null) return;
      chunks += 1;
      parser.push(decoder.push(toArrayBuffer(data)));
    },
    fallback(data) {
      if (chunks > 0 || data == null) return false;
      if (typeof data === 'string') {
        if (data) parser.push(data);
      } else {
        parser.push(decoder.push(toArrayBuffer(data)));
      }
      return true;
    },
    finish() {
      const rest = decoder.end();
      if (rest) parser.push(rest);
      parser.end();
    },
    chunkCount() {
      return chunks;
    },
  };
}

// 统一 HTTP 成功判定：仅 2xx 才允许把完整响应体交给 SSE fallback（错误 JSON 不喂解析器）。
function isSuccessfulStatus(code) {
  return typeof code === 'number' && code >= 200 && code < 300;
}

// 静默看门狗：每收到一个真实分块就重新计时；无分块超过 timeoutMs 判定连接死亡。
function createIdleWatchdog({ timeoutMs, onTimeout, setTimeoutFn, clearTimeoutFn }) {
  const setT = setTimeoutFn || setTimeout;
  const clearT = clearTimeoutFn || clearTimeout;
  let timer = null;
  let fired = false;
  const stop = () => {
    if (timer) {
      clearT(timer);
      timer = null;
    }
  };
  return {
    arm() {
      stop();
      timer = setT(() => {
        timer = null;
        fired = true;
        onTimeout();
      }, timeoutMs);
    },
    stop,
    hasFired() {
      return fired;
    },
  };
}

module.exports = { createUtf8Decoder, createSseParser, createSseStream, createIdleWatchdog, isSuccessfulStatus };
